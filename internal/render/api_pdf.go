// api_pdf.go 实现 CAD 图纸的矢量 PDF 输出（RenderPDF / RenderSheetPDF /
// RenderSheetsPDF）：复用 RenderPNG/RenderSVG 的图元展开管线（INSERT 递归
// → 线段离散 → 放射线/原点锚定线过滤 → 鲁棒包围盒），线段输出为按颜色
// 分组的 PDF 路径（m/l + S），HATCH/SOLID 边界接合成闭合环后输出为真
// 填充路径（m/l/h + f，非零环绕，置于线稿之下——PDF 相对 PNG/SVG 描边
// 口径的格式增强，接合失败回退描边），文本经 pdf_text.go 的字形矢量
// 路径化输出为填充路径（f，非零环绕）——不嵌入字体，任意查看器无字体
// 依赖、无限缩放不失真，规避 CJK 字体子集化的授权与体积问题（代价：
// PDF 内文字不可选中复制，可复制文本走 SVG 输出）。
//
// 与 PNG/SVG 的口径对齐：
//   - 管线逐步同构（矢量口径展开预算沿用 svgExpandBudget，文本全覆盖）；
//   - 线宽取 PNG 圆盘笔刷直径（1.5px）的世界当量（vw/名义宽×1.5），
//     三格式视觉一致；
//   - 文本版式与 PNG 像素路径逐步同构（见 pdf_text.go 头注）；
//   - 占位框回退几何与 SVG appendLabelBox 一致。
//
// 坐标系：PDF 用户空间原点左下、Y 轴向上，与 CAD 世界坐标同向——只做
// 平移与缩放，无 Y 翻转。页面尺寸按工程惯例假设世界单位为毫米
// （×72/25.4 折算 pt，A1 图框出 A1 物理页），宽高比恒正确。
//
// 确定性：对象顺序固定（页序 + 颜色分组首现序）、数字定点格式化、
// zlib 压缩确定、不写 CreationDate——同输入两次输出字节一致（与 SVG
// 的确定性文化对齐，支撑字节级回归测试）。
//
// 文件主要模块划分：
//
//	pdfWriter：最小 PDF 1.4 writer（对象表、xref、Flate 流、trailer）
//	pdfEmitter：单页 content stream（HATCH/SOLID 填充 + 线段分组描边 +
//	          文本轮廓分组填充）
//	HATCH/SOLID 填充：hatchRings 边界段接合成闭合环（顺序闭合快路径 +
//	          端点哈希贪心串链），emitHatchFill 输出填充子路径
//	页面封装：renderPagePDF（视口 + 图元过滤 + emitter 组装）
//	导出入口：RenderPDF（整图单页）/ RenderSheetPDF（单图框）/
//	          RenderSheetsPDF（拆图多页单文件 + 书签树 = 图名列表）
package render

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"image/color"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/unitedrhino/go-cad/internal/drawing"
)

// pdfMM2PT 毫米 → PDF 点换算（1pt = 1/72 英寸）：工程图纸世界单位按
// 毫米假设（主流制图单位），A1 图框（841×594mm）输出物理 A1 页。
const pdfMM2PT = 72.0 / 25.4

// pdfMaxPagePt 页面单边尺寸上限（pt）：Acrobat 及多数查看器把 MediaBox
// 限制在 14400pt（200×200 英寸），超大图纸（总图 12000mm+ 直折超限会被
// 拒开）；超出时等比下调缩放系数——宽高比与视觉不变（矢量查看缩放
// 自由），仅"物理打印 1:1"语义让位于可打开性。
const pdfMaxPagePt = 14000

// pdfStrokeBatch 单条路径的线段批大小上限：PDF 规范对单 path 子路径数
// 无硬限制，但超长 path（百万段）在部分查看器上有解析性能/栈深问题，
// 按千段分批描边（批间 S 结束当前 path，下一批 m 重开）。
const pdfStrokeBatch = 1000

// pdfGlyphBatch 单次填充聚合的字形数上限：同一颜色连续字形的轮廓聚合
// 成一条 path 后统一 f（非零环绕），减少操作符数量；超限分批填充。
const pdfGlyphBatch = 256

// pdfHatchMaxSegs 单个 HATCH/SOLID 图元参与填充接合的段数上限：病态
// 数据（巨型离散环）下端点哈希接合的贪心串链与环检测开销线性增长，
// 超限直接放弃填充回退描边口径（不丢几何、不卡死），与展开侧的防御
// 策略同构。
const pdfHatchMaxSegs = 100000

// pdfColorOf color.RGBA → PDF 颜色分量（0~1，保留 3 位小数精度由
// appendPDFNum 处理）。
func pdfColorOf(c color.RGBA) rgbKey {
	return rgbKey{c.R, c.G, c.B}
}

// pdfGroup 同色图元分组：组内共享一次颜色/线宽设置，buf 收集路径算子，
// segs 记当前批内已聚合的图元数（达批上限写出一次 S/f 后清零）。
type pdfGroup struct {
	key  rgbKey
	buf  []byte
	segs int // 当前批内已聚合的线段/字形象数
}

// pdfEmitter 单页 PDF content stream 发射器：线段按颜色分组收集描边
// 路径，文本轮廓按颜色分组收集填充路径（文本置于线段之上，与 SVG 输出
// 层序一致，避免文字被线盖住）；HATCH/SOLID 边界接合成闭合环后按颜色
// 分组收集填充路径，置于线段之下（底色语义：填充盖背景、被线稿盖住，
// 避免 hatch 底色糊住图形细节）；占位框并入线段分组（描边口径同 SVG）。
type pdfEmitter struct {
	k            float64 // 世界单位 → pt 缩放
	offX, offY   float64 // 页面原点对应的世界坐标（vp.Min）
	pageW, pageH float64 // 页面尺寸（pt，背景矩形用）
	strokeW      float64 // 线宽（pt）
	bg           color.RGBA
	hatchGrp     []*pdfGroup
	hatchIdx     map[rgbKey]int
	strokeGrp    []*pdfGroup
	strokeIdx    map[rgbKey]int
	textGrp      []*pdfGroup
	textIdx      map[rgbKey]int
	shaper       *pdfShaper
	doc          *drawing.Document
	bgWhite      bool
}

// newPDFEmitter 构造页面发射器：vp 为页面世界视口，nominalWidth 为名义
// 像素宽度（线宽基准，与 SVG strokeW 口径一致）。
func newPDFEmitter(vp drawing.Box2, nominalWidth int, opts RenderOptions, shaper *pdfShaper, doc *drawing.Document) *pdfEmitter {
	vw := vp.MaxX - vp.MinX
	vh := vp.MaxY - vp.MinY
	if nominalWidth <= 0 {
		nominalWidth = 2048
	}
	// 页面尺寸钳制：毫米直折超查看器上限时等比下调。k 为最终 世界→pt
	// 绝对系数，上限除以世界尺寸（毫米）得到，与 appendPDFPage 的
	// MediaBox 钳制结果一致（否则内容坐标系与页面尺寸不匹配）
	k := pdfMM2PT
	if w, h := vw*k, vh*k; w > pdfMaxPagePt || h > pdfMaxPagePt {
		k = pdfMaxPagePt / math.Max(vw, vh)
	}
	strokeW := vw / float64(nominalWidth) * 1.5 * k
	return &pdfEmitter{
		k:         k,
		offX:      vp.MinX,
		offY:      vp.MinY,
		pageW:     vw * k,
		pageH:     vh * k,
		strokeW:   strokeW,
		bg:        opts.Background,
		hatchIdx:  map[rgbKey]int{},
		strokeIdx: map[rgbKey]int{},
		textIdx:   map[rgbKey]int{},
		shaper:    shaper,
		doc:       doc,
		bgWhite:   opts.Background.R > 127,
	}
}

// groupFor 取指定颜色分组（线段/文本各自的保序分组；首现顺序保证输出
// 确定性，与 svgEmitter 的分组策略同构）。
func (em *pdfEmitter) groupFor(grps *[]*pdfGroup, idx map[rgbKey]int, k rgbKey) *pdfGroup {
	if i, ok := idx[k]; ok {
		return (*grps)[i]
	}
	g := &pdfGroup{key: k, buf: make([]byte, 0, 4096)}
	idx[k] = len(*grps)
	*grps = append(*grps, g)
	return g
}

// xy 世界坐标 → 页面 pt 坐标（平移 + 缩放，Y 同向无翻转）。
func (em *pdfEmitter) xy(x, y float64) (float64, float64) {
	return (x - em.offX) * em.k, (y - em.offY) * em.k
}

// emit 序列化一个展开图元：stroke 图元逐段并入同色描边组（千段分批
// S），HATCH/SOLID 图元优先尝试把边界段接合成闭合环转为填充（PDF 相对
// PNG/SVG 描边口径的格式增强，接合失败回退描边，不丢几何），label 图元
// 有字形版式且有字体时走矢量文本，否则输出半高占位框（口径与 SVG emit
// 一致：Tx 为 nil / 无字体 / 空文本均回退占位框）。
func (em *pdfEmitter) emit(p *drawing.Primitive) {
	switch p.Kind {
	case 0:
		if len(p.Strokes) == 0 {
			return // 空图元不建组（SVG emit 同口径）
		}
		k := pdfColorOf(entityColor(em.doc, p, em.bgWhite))
		// HATCH/SOLID 真填充：成功即以填充替代描边（描边只是边界的降级
		// 表达），失败下落到通用描边路径保证几何不丢
		if p.Kind0 == "HATCH" || p.Kind0 == "SOLID" {
			if em.emitHatchFill(p, k) {
				return
			}
		}
		g := em.groupFor(&em.strokeGrp, em.strokeIdx, k)
		for _, s := range p.Strokes {
			if !drawing.Plausible(s.X1, s.Y1) || !drawing.Plausible(s.X2, s.Y2) {
				continue
			}
			x1, y1 := em.xy(s.X1, s.Y1)
			x2, y2 := em.xy(s.X2, s.Y2)
			if x1 == x2 && y1 == y2 {
				continue // 零长段无几何贡献（SVG 同口径）
			}
			g.buf = appendPDFNum(appendPDFNum(g.buf, ' ', x1), ' ', y1)
			g.buf = append(g.buf, " m"...)
			g.buf = appendPDFNum(appendPDFNum(g.buf, ' ', x2), ' ', y2)
			g.buf = append(g.buf, " l\n"...)
			g.segs++
			if g.segs >= pdfStrokeBatch {
				g.buf = append(g.buf, "S\n"...)
				g.segs = 0
			}
		}
	case 1:
		if !drawing.Plausible(p.Lb.X, p.Lb.Y) || p.Lb.H <= 0 {
			return
		}
		k := pdfColorOf(entityColor(em.doc, p, em.bgWhite))
		if p.Lb.Tx != nil && len(em.shaper.fonts) > 0 {
			em.shaper.drawTextPDF(em, &p.Lb, p.Lb.Tx, k)
			return
		}
		em.appendLabelBoxPDF(&p.Lb, k)
	}
}

// hatchKey 端点量化键：接合时以「坐标 ÷ 容差四舍五入」的整数格为端点
// 身份，距离小于容差的两个端点大概率落入同格即视为同一点。
type hatchKey struct{ x, y int64 }

// hatchQuantKey 端点量化：容差取图元包围盒对角线 × 1e-6（相对精度，
// 与坐标量级无关）。跨格边界的近邻对会被判为不同点——由此导致的接合
// 失败走回退描边兜底，不产生错误几何。
func hatchQuantKey(x, y, tol float64) hatchKey {
	return hatchKey{x: int64(math.Round(x / tol)), y: int64(math.Round(y / tol))}
}

// emitHatchFill 把 HATCH/SOLID 图元的边界段接合成闭合环，输出到同色
// 填充组（m/l + h 闭合子路径，非零环绕 f）——PDF 相对 PNG/SVG 描边口径
// 的格式增强：SOLID 本就是实心填充语义，HATCH 边界环围成区域填色后才
// 与 CAD 中的视觉重量一致。填充色与描边同口径取实体色。
//
// 已知近似（诚实边界，不做反向校正）：
//   - 非零环绕下同一 path 内方向相反的内外环自动挖洞；本实现不校验
//     边界环绕方向（DWG 侧不保证），方向一致时洞会被填充；
//   - 离散管线对多边界 HATCH 每个边界环输出独立图元（tessellate.go 的
//     EntHatch 分支），跨图元的内外环分属不同填充 path，挖洞不生效；
//   - 图元累计环段数超 pdfStrokeBatch 时按千段分批 f，跨批子路径分属
//     不同 path，挖洞近似同样失效。
//
// 返回 false 表示接合失败（开链 / 分叉 / 段数超限），调用方回退描边，
// 保证不出错、不丢几何。
func (em *pdfEmitter) emitHatchFill(p *drawing.Primitive, k rgbKey) bool {
	if len(p.Strokes) < 3 || len(p.Strokes) > pdfHatchMaxSegs {
		return false // 不足 3 段无法成环；病态超限防卡死——均回退描边
	}
	rings := hatchRings(p.Strokes)
	if rings == nil {
		return false
	}
	g := em.groupFor(&em.hatchGrp, em.hatchIdx, k)
	for _, ring := range rings {
		// 环内段已定向首尾相接（量化口径）：m 首段起点，逐段 l 到各自
		// 终点（相邻段共享点不重复输出），h 闭合回子路径起点
		for i, s := range ring {
			if i == 0 {
				x1, y1 := em.xy(s.X1, s.Y1)
				g.buf = appendPDFNum(appendPDFNum(g.buf, ' ', x1), ' ', y1)
				g.buf = append(g.buf, " m\n"...)
			}
			x2, y2 := em.xy(s.X2, s.Y2)
			g.buf = appendPDFNum(appendPDFNum(g.buf, ' ', x2), ' ', y2)
			g.buf = append(g.buf, " l\n"...)
		}
		g.buf = append(g.buf, "h\n"...)
		g.segs += len(ring)
		if g.segs >= pdfStrokeBatch {
			// 千段分批填充（与描边分批同口径，防超长 path 在部分查看器
			// 上的解析性能问题）；以整环为分批单位，单环不拆分（拆开的
			// 半个环会各自闭合形成错误填充几何）
			g.buf = append(g.buf, "f\n"...)
			g.segs = 0
		}
	}
	return true
}

// hatchRings 把图元边界段接合为闭合环列表：先试顺序闭合快路径（SOLID
// 四段与离散边界环通常按序首尾相接给出），失败再做端点哈希贪心串链。
// 任一段无法归入闭合环（坐标非法、开链、分叉贪心失败）则整体返回 nil，
// 调用方回退描边——宁可少一次填充增强，不给错误几何。
func hatchRings(strokes []drawing.Stroke) [][]drawing.Stroke {
	// 净化：坐标非法段排除（与描边路径的 Plausible 过滤同口径），同时
	// 累计包围盒供容差计算
	segs := make([]drawing.Stroke, 0, len(strokes))
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, s := range strokes {
		if !drawing.Plausible(s.X1, s.Y1) || !drawing.Plausible(s.X2, s.Y2) {
			continue
		}
		segs = append(segs, s)
		minX = math.Min(minX, math.Min(s.X1, s.X2))
		minY = math.Min(minY, math.Min(s.Y1, s.Y2))
		maxX = math.Max(maxX, math.Max(s.X1, s.X2))
		maxY = math.Max(maxY, math.Max(s.Y1, s.Y2))
	}
	// 容差：包围盒对角线 × 1e-6；对角线退化（全部段共点重合）时取
	// 1e-9 绝对值兜底（负零/NaN 一并被 !(tol > 0) 拦下）
	tol := math.Hypot(maxX-minX, maxY-minY) * 1e-6
	if !(tol > 0) {
		tol = 1e-9
	}
	// 两端同键的零长段无几何贡献，且会以「首尾同键」卡死贪心延伸，剔除
	n := 0
	for _, s := range segs {
		if hatchQuantKey(s.X1, s.Y1, tol) != hatchQuantKey(s.X2, s.Y2, tol) {
			segs[n] = s
			n++
		}
	}
	segs = segs[:n]
	if len(segs) < 3 {
		return nil
	}
	if hatchSequentialClosed(segs, tol) {
		return [][]drawing.Stroke{segs} // 快路径零复制：原序段即单环
	}
	return hatchRingsGreedy(segs, tol)
}

// hatchSequentialClosed 顺序闭合判定：段按序首尾相接（量化键口径）且
// 末段终点回到首段起点。SOLID 四点与多数离散边界环的常态，一遍扫描
// 免去哈希接合的分配开销。
func hatchSequentialClosed(segs []drawing.Stroke, tol float64) bool {
	for i := 1; i < len(segs); i++ {
		if hatchQuantKey(segs[i-1].X2, segs[i-1].Y2, tol) != hatchQuantKey(segs[i].X1, segs[i].Y1, tol) {
			return false
		}
	}
	last := segs[len(segs)-1]
	return hatchQuantKey(last.X2, last.Y2, tol) == hatchQuantKey(segs[0].X1, segs[0].Y1, tol)
}

// hatchRingsGreedy 端点哈希 + 贪心串链成环：从任一未用段出发定向为
// a→b，沿链尾键在端点索引中找未用段接续（命中候选段终点则反向接入），
// 直到链尾键回到链首键成环；剩余段继续串链（多边界图元多环场景）。
// 任一链延伸到死端且首尾不接（开链）、或分叉端点上贪心选错，均整体
// 返回 nil 由调用方回退描边——贪心失败只损失填充增强，不损失几何。
func hatchRingsGreedy(segs []drawing.Stroke, tol float64) [][]drawing.Stroke {
	// endRef 端点引用：段号 + 命中端是否为该段终点（反向接入用）
	type endRef struct {
		seg  int
		tail bool
	}
	ends := make(map[hatchKey][]endRef, len(segs)*2)
	for i, s := range segs {
		ends[hatchQuantKey(s.X1, s.Y1, tol)] = append(ends[hatchQuantKey(s.X1, s.Y1, tol)], endRef{i, false})
		ends[hatchQuantKey(s.X2, s.Y2, tol)] = append(ends[hatchQuantKey(s.X2, s.Y2, tol)], endRef{i, true})
	}
	used := make([]bool, len(segs))
	var rings [][]drawing.Stroke
	// take 在键上的候选里取首个未用段（分叉时贪心取先到者）
	take := func(k hatchKey) (int, bool) {
		for _, r := range ends[k] {
			if !used[r.seg] {
				return r.seg, r.tail
			}
		}
		return -1, false
	}
	for i := range segs {
		if used[i] {
			continue
		}
		used[i] = true
		chain := []drawing.Stroke{segs[i]}
		head := hatchQuantKey(segs[i].X1, segs[i].Y1, tol)
		tail := hatchQuantKey(segs[i].X2, segs[i].Y2, tol)
		for tail != head {
			j, hitTail := take(tail)
			if j < 0 {
				return nil // 开链死端：无法闭合，整体回退描边
			}
			used[j] = true
			if hitTail {
				// 候选段终点命中链尾：反向 b→a 接入，保持链内方向一致
				chain = append(chain, drawing.Stroke{X1: segs[j].X2, Y1: segs[j].Y2, X2: segs[j].X1, Y2: segs[j].Y1})
				tail = hatchQuantKey(segs[j].X1, segs[j].Y1, tol)
			} else {
				chain = append(chain, segs[j])
				tail = hatchQuantKey(segs[j].X2, segs[j].Y2, tol)
			}
		}
		rings = append(rings, chain)
	}
	return rings
}

// appendLabelBoxPDF 输出文字占位框：基线左端为原点、向上半高、随 rot
// 旋转（几何与 SVG appendLabelBox / PNG drawLabel 一致），并入同色描边组。
func (em *pdfEmitter) appendLabelBoxPDF(lb *drawing.Label, k rgbKey) {
	g := em.groupFor(&em.strokeGrp, em.strokeIdx, k)
	cos, sin := math.Cos(lb.Rot), math.Sin(lb.Rot)
	half := lb.H / 2
	for i, c := range [4][2]float64{{0, 0}, {lb.W, 0}, {lb.W, half}, {0, half}} {
		x, y := em.xy(lb.X+c[0]*cos-c[1]*sin, lb.Y+c[0]*sin+c[1]*cos)
		g.buf = appendPDFNum(appendPDFNum(g.buf, ' ', x), ' ', y)
		if i == 0 {
			g.buf = append(g.buf, " m\n"...)
		} else {
			g.buf = append(g.buf, " l\n"...)
		}
	}
	g.buf = append(g.buf, "h\nS\n"...)
	g.segs = 0
}

// appendGlyphSegs 追加一个字形的全部轮廓段到同色填充组：段点 (gx,gy)
// （em 单位，Y 向下）经仿射 W = C + A·gx + B·gy（系数由 appendLineAt
// 计算）到页面坐标；quad 段升 cubic（PDF 无二次曲线算子，控制点取
// 2/3 权重精确等价）；每 pdfGlyphBatch 个字形填充一次（非零环绕）。
func (em *pdfEmitter) appendGlyphSegs(segs []pdfSeg, cx, cy, ax, ay, bx, by float64, col rgbKey) {
	g := em.groupFor(&em.textGrp, em.textIdx, col)
	// 世界坐标仿射常数项/系数 → 页面坐标（平移到视口原点 + mm→pt 缩放）
	cx, cy = (cx-em.offX)*em.k, (cy-em.offY)*em.k
	ax, ay, bx, by = ax*em.k, ay*em.k, bx*em.k, by*em.k
	mapPt := func(p [2]float64) (float64, float64) {
		return cx + ax*p[0] + bx*p[1], cy + ay*p[0] + by*p[1]
	}
	curX, curY := 0.0, 0.0
	for _, s := range segs {
		switch s.op {
		case pdfSegMoveTo:
			x, y := mapPt(s.p[0])
			g.buf = appendPDFNum(appendPDFNum(g.buf, ' ', x), ' ', y)
			g.buf = append(g.buf, " m\n"...)
			curX, curY = x, y
		case pdfSegLineTo:
			x, y := mapPt(s.p[0])
			g.buf = appendPDFNum(g.buf, ' ', x)
			g.buf = appendPDFNum(g.buf, ' ', y)
			g.buf = append(g.buf, " l\n"...)
			curX, curY = x, y
		case pdfSegQuadTo:
			// quad → cubic：C1 = P0 + 2/3(Q1-P0)、C2 = Q2 + 2/3(Q1-Q2)
			qx, qy := mapPt(s.p[0])
			ex, ey := mapPt(s.p[1])
			c1x, c1y := curX+2.0/3.0*(qx-curX), curY+2.0/3.0*(qy-curY)
			c2x, c2y := ex+2.0/3.0*(qx-ex), ey+2.0/3.0*(qy-ey)
			g.buf = appendPDFNum(g.buf, ' ', c1x)
			g.buf = appendPDFNum(g.buf, ' ', c1y)
			g.buf = appendPDFNum(g.buf, ' ', c2x)
			g.buf = appendPDFNum(g.buf, ' ', c2y)
			g.buf = appendPDFNum(g.buf, ' ', ex)
			g.buf = appendPDFNum(g.buf, ' ', ey)
			g.buf = append(g.buf, " c\n"...)
			curX, curY = ex, ey
		case pdfSegCubeTo:
			c1x, c1y := mapPt(s.p[0])
			c2x, c2y := mapPt(s.p[1])
			ex, ey := mapPt(s.p[2])
			g.buf = appendPDFNum(g.buf, ' ', c1x)
			g.buf = appendPDFNum(g.buf, ' ', c1y)
			g.buf = appendPDFNum(g.buf, ' ', c2x)
			g.buf = appendPDFNum(g.buf, ' ', c2y)
			g.buf = appendPDFNum(g.buf, ' ', ex)
			g.buf = appendPDFNum(g.buf, ' ', ey)
			g.buf = append(g.buf, " c\n"...)
			curX, curY = ex, ey
		}
	}
	g.segs++
	if g.segs >= pdfGlyphBatch {
		g.buf = append(g.buf, "f\n"...)
		g.segs = 0
	}
}

// finish 组装单页 content stream：背景填充 → HATCH/SOLID 填充分组
// （rg 颜色 + 闭合子路径 + f，置于线稿之下）→ 线段分组（RG 颜色 +
// w 线宽 + 批量路径 + S）→ 文本分组（rg 颜色 + 轮廓路径 + f）。
func (em *pdfEmitter) finish() []byte {
	var b []byte
	// 背景：整页矩形填充（0 0 pageW pageH re f）
	b = appendPDFNum(b, ' ', float64(em.bg.R)/255)
	b = appendPDFNum(b, ' ', float64(em.bg.G)/255)
	b = appendPDFNum(b, ' ', float64(em.bg.B)/255)
	b = append(b, " rg 0 0 "...)

	b = appendPDFNum(b, ' ', em.pageW)
	b = appendPDFNum(b, ' ', em.pageH)
	b = append(b, " re f\n"...)
	// HATCH/SOLID 填充组先于线稿输出（底色语义：填充盖背景、线段描边
	// 再盖填充——hatch 底色在线稿之下，不糊住图形边界细节）；填充分组
	// 无线宽设置（填充与线宽无关）
	for _, g := range em.hatchGrp {
		if g.segs == 0 && len(g.buf) == 0 {
			continue // 空组（图元全部段被过滤时残留）不输出
		}
		b = appendPDFNum(b, ' ', float64(g.key.r)/255)
		b = appendPDFNum(b, ' ', float64(g.key.G)/255)
		b = appendPDFNum(b, ' ', float64(g.key.b)/255)
		b = append(b, " rg\n"...)
		b = append(b, g.buf...)
		if g.segs > 0 {
			b = append(b, "f\n"...)
		}
	}
	for _, g := range em.strokeGrp {
		if g.segs == 0 && len(g.buf) == 0 {
			continue // 空组（图元全部段被过滤时残留）不输出
		}
		b = appendPDFNum(b, ' ', float64(g.key.r)/255)
		b = appendPDFNum(b, ' ', float64(g.key.G)/255)
		b = appendPDFNum(b, ' ', float64(g.key.b)/255)
		b = append(b, " RG"...)
		b = appendPDFNum(b, ' ', em.strokeW)
		b = append(b, " w\n"...)
		b = append(b, g.buf...)
		if g.segs > 0 {
			b = append(b, "S\n"...)
		}
	}
	for _, g := range em.textGrp {
		if g.segs == 0 && len(g.buf) == 0 {
			continue
		}
		b = appendPDFNum(b, ' ', float64(g.key.r)/255)
		b = appendPDFNum(b, ' ', float64(g.key.G)/255)
		b = appendPDFNum(b, ' ', float64(g.key.b)/255)
		b = append(b, " rg\n"...)
		b = append(b, g.buf...)
		if g.segs > 0 {
			b = append(b, "f\n"...)
		}
	}
	return b
}

// appendPDFNum 追加一个数字：先写前导字符（空格/字母，负号自带分隔），
// 再写定点 2 位小数并去尾零。PDF 数字语法与 SVG 共通，负号可作分隔符。
func appendPDFNum(b []byte, lead byte, v float64) []byte {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return append(b, lead, '0') // 非有限值防御：0 兜底（Plausible 已滤大部分）
	}
	b = append(b, lead)
	// 定点 2 位（pt 精度 0.01 远超视觉阈值），去尾零与小数点
	s := strconv.AppendFloat(make([]byte, 0, 12), v, 'f', 2, 64)
	if bytes.HasSuffix(s, []byte("00")) {
		s = s[:len(s)-3]
		if len(s) == 0 || (len(s) == 1 && s[0] == '-') {
			s = append(s, '0')
		}
	} else if bytes.HasSuffix(s, []byte("0")) {
		s = s[:len(s)-1]
	}
	return append(b, s...)
}

// pdfWriter 最小 PDF 1.4 writer：对象体顺序收集（编号 = 下标 + 1），
// finish 时统一序列化 header + 对象 + xref + trailer。对象 1 约定为
// Catalog；流对象经 addStream 自动 Flate 压缩包装。
type pdfWriter struct {
	objs [][]byte
}

// alloc 预留一个对象编号（前向引用占位：页树需要先知道子页编号）。
func (w *pdfWriter) alloc() int {
	w.objs = append(w.objs, nil)
	return len(w.objs)
}

// set 填充对象体（不含 "n 0 obj"/"endobj" 包装，由 finish 统一加）。
func (w *pdfWriter) set(num int, body []byte) {
	w.objs[num-1] = body
}

// addStream 写入一个 Flate 压缩流对象，返回对象编号。
func (w *pdfWriter) addStream(data []byte) int {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	_, _ = zw.Write(data)
	_ = zw.Close()
	body := make([]byte, 0, z.Len()+64)
	body = append(body, "<< /Length "...)
	body = strconv.AppendInt(body, int64(z.Len()), 10)
	body = append(body, " /Filter /FlateDecode >>\nstream\n"...)
	body = append(body, z.Bytes()...)
	body = append(body, "\nendstream"...)
	num := w.alloc()
	w.set(num, body)
	return num
}

// bytes 序列化最终 PDF 文档：header（含二进制标记注释）→ 对象 → xref
// （偏移按字节累计）→ trailer（Root 固定对象 1）→ startxref → %%EOF。
func (w *pdfWriter) bytes() []byte {
	var b []byte
	b = append(b, "%PDF-1.4\n%\xE2\xE3\xCF\xD3\n"...)
	offsets := make([]int, len(w.objs))
	for i, body := range w.objs {
		offsets[i] = len(b)
		b = strconv.AppendInt(b, int64(i+1), 10)
		b = append(b, " 0 obj\n"...)
		b = append(b, body...)
		b = append(b, "\nendobj\n"...)
	}
	xref := len(b)
	b = append(b, "xref\n0 "...)
	b = strconv.AppendInt(b, int64(len(w.objs)+1), 10)
	b = append(b, "\n0000000000 65535 f \n"...)
	for _, off := range offsets {
		b = append(b, fmt.Sprintf("%010d 00000 n \n", off)...)
	}
	b = append(b, "trailer\n<< /Size "...)
	b = strconv.AppendInt(b, int64(len(w.objs)+1), 10)
	b = append(b, " /Root 1 0 R >>\nstartxref\n"...)
	b = strconv.AppendInt(b, int64(xref), 10)
	b = append(b, "\n%%EOF\n"...)
	return b
}

// pdfTitleString 标题 → PDF 文本字符串：非 ASCII（中文图名）必须用
// UTF-16BE 十六进制串（<FEFF...>），纯 ASCII 用字面串更省字节；括号与
// 反斜杠转义防注入。
func pdfTitleString(s string) string {
	ascii := true
	for _, r := range s {
		if r > 126 || r < 32 {
			ascii = false
			break
		}
	}
	if ascii {
		var b strings.Builder
		b.WriteByte('(')
		for _, r := range s {
			if r == '(' || r == ')' || r == '\\' {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
		b.WriteByte(')')
		return b.String()
	}
	var b strings.Builder
	b.WriteString("<FEFF")
	for _, u := range utf16.Encode([]rune(s)) {
		fmt.Fprintf(&b, "%04X", u)
	}
	b.WriteByte('>')
	return b.String()
}

// renderPagePDF 渲染单个视口为一页 content stream。crop 为 true 时做
// 框外图元预剔除（拆图逐张口径：primInBox，跨框长线由页面坐标自然
// 裁剪）；false 为整图口径（RenderPNG/RenderSVG 全量输出，页面外内容
// 由查看器按 MediaBox 裁剪——quantile 视口外仍有有效图元，剔了会缺内容）。
// shaper 跨页复用（字形轮廓缓存共享）。
func renderPagePDF(sp *sheetPipeline, vp drawing.Box2, nominalWidth int, opts RenderOptions, shaper *pdfShaper, crop bool) []byte {
	em := newPDFEmitter(vp, nominalWidth, opts, shaper, sp.doc)
	for i := range sp.prims {
		if crop && !primInBox(&sp.prims[i], vp) {
			continue
		}
		em.emit(&sp.prims[i])
	}
	return em.finish()
}

// pdfPageMeta 单页组装结果：content stream 对象号与页面尺寸（pt，
// MediaBox 用；退化视口兜底 1×1pt 防零尺寸页）。
type pdfPageMeta struct {
	contentNum int
	w, h       float64
}

// appendPDFPage 向 writer 追加一页（Page 对象 + Flate 内容流），返回
// 页面对象编号。MediaBox 原点固定 (0,0)，尺寸取视口世界宽高折算 pt。
func appendPDFPage(w *pdfWriter, content []byte, vp drawing.Box2) int {
	pw := (vp.MaxX - vp.MinX) * pdfMM2PT
	ph := (vp.MaxY - vp.MinY) * pdfMM2PT
	if pw < 1 {
		pw = 1
	}
	if ph < 1 {
		ph = 1
	}
	k := pdfMM2PT
	if pw > pdfMaxPagePt || ph > pdfMaxPagePt {
		k = pdfMaxPagePt / math.Max(pw, ph)
		pw, ph = pw*k, ph*k
	}
	cnum := w.addStream(content)
	pnum := w.alloc()
	body := make([]byte, 0, 128)
	body = append(body, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 "...)
	body = appendPDFNum(body, ' ', math.Round(pw*100)/100)
	body = appendPDFNum(body, ' ', math.Round(ph*100)/100)
	body = append(body, "] /Contents "...)
	body = strconv.AppendInt(body, int64(cnum), 10)
	body = append(body, " 0 R >>"...)
	w.set(pnum, body)
	return pnum
}

// pdfAppendOutlines 追加书签树（每页一项，图名即书签名）并返回
// Outlines 根对象编号；单页不输出书签（返回 0）。
func pdfAppendOutlines(w *pdfWriter, names []string, pageObjs []int) int {
	if len(names) < 2 {
		return 0
	}
	rootNum := w.alloc()
	items := make([]int, len(names))
	for i := range items {
		items[i] = w.alloc()
	}
	for i, name := range names {
		var body []byte
		body = append(body, "<< /Title "...)

		body = append(body, pdfTitleString(name)...)
		body = append(body, " /Parent "...)

		body = strconv.AppendInt(body, int64(rootNum), 10)
		body = append(body, " 0 R"...)
		if i > 0 {
			body = append(body, " /Prev "...)

			body = strconv.AppendInt(body, int64(items[i-1]), 10)
			body = append(body, " 0 R"...)
		}
		if i < len(items)-1 {
			body = append(body, " /Next "...)

			body = strconv.AppendInt(body, int64(items[i+1]), 10)
			body = append(body, " 0 R"...)
		}
		body = append(body, " /Dest ["...)
		body = strconv.AppendInt(body, int64(pageObjs[i]), 10)
		body = append(body, " 0 R /Fit] >>"...)

		w.set(items[i], body)
	}
	var root []byte
	root = append(root, "<< /Type /Outlines /First "...)

	root = strconv.AppendInt(root, int64(items[0]), 10)
	root = append(root, " 0 R /Last "...)

	root = strconv.AppendInt(root, int64(items[len(items)-1]), 10)
	root = append(root, " 0 R /Count "...)

	root = strconv.AppendInt(root, int64(len(names)), 10)
	root = append(root, " 0 R >>"...)

	w.set(rootNum, root)
	return rootNum
}

// RenderPDF 将文档模型空间渲染为单页矢量 PDF 字节流。
// 展开预算与 RenderSVG 同口径（矢量全展开保证文本覆盖）；opts.Width
// 仅作线宽名义基准（默认 2048，与 SVG 视觉一致），页面尺寸按世界单位
// 毫米假设折算 pt。文本无字体可用时回退占位框（诚实边界见 README）。
func RenderPDF(doc *drawing.Document, opts RenderOptions) ([]byte, error) {
	if doc == nil {
		return nil, fmt.Errorf("cad: 文档为空，无法渲染")
	}
	if opts.Width <= 0 {
		opts.Width = 2048
	}
	if opts.Background == (color.RGBA{}) {
		opts.Background = color.RGBA{255, 255, 255, 255}
	}
	// 与 RenderSVG 完全一致的展开与过滤管线（矢量预算：大图全展开）
	prim := drawing.NewTessellator(doc)
	prim.Budget = svgExpandBudget
	prims := prim.ExpandAll()
	prims = filterRadiatingStrokes(prims)
	bbox := quantileSheetBounds(prims)
	if bbox.Invalid() {
		bbox = drawing.Box2{MinX: 0, MinY: 0, MaxX: 1, MaxY: 1}
	}
	prims = dropOriginAnchored(prims, bbox)
	marginX := (bbox.MaxX - bbox.MinX) * 0.05
	marginY := (bbox.MaxY - bbox.MinY) * 0.05
	if marginX == 0 && marginY == 0 {
		marginX, marginY = 1, 1
	} else if marginX == 0 {
		marginX = marginY
	} else if marginY == 0 {
		marginY = marginX
	}
	vp := drawing.Box2{
		MinX: bbox.MinX - marginX, MinY: bbox.MinY - marginY,
		MaxX: bbox.MaxX + marginX, MaxY: bbox.MaxY + marginY,
	}
	sp := &sheetPipeline{doc: doc, prims: prims}
	content := renderPagePDF(sp, vp, opts.Width, opts, newPDFShaper(opts.FontPath), false)

	w := &pdfWriter{}
	catalogNum := w.alloc() // 对象 1 约定 Catalog
	pagesNum := w.alloc()
	pnum := appendPDFPage(w, content, vp)
	w.set(pagesNum, []byte(fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", pnum)))
	w.set(catalogNum, []byte(fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pagesNum)))
	return w.bytes(), nil
}

// RenderSheetPDF 渲染单个图框为单页矢量 PDF（视口裁剪与名义宽度自适应
// 口径同 RenderSheetSVG，无书签——单页书签无导航意义）。
func RenderSheetPDF(doc *drawing.Document, sheet Sheet, opts RenderOptions) ([]byte, error) {
	if doc == nil {
		return nil, fmt.Errorf("cad: 文档为空，无法渲染")
	}
	opts = normalizeSheetOptions(opts)
	sp := prepareSheets(doc)
	vp := sheetViewport(sheet.Box)
	width := sp.sheetWidthFor(vp, sheet, opts.Width)
	content := renderPagePDF(sp, vp, width, opts, newPDFShaper(opts.FontPath), true)
	w := &pdfWriter{}
	catalogNum := w.alloc()
	pagesNum := w.alloc()
	pnum := appendPDFPage(w, content, vp)
	w.set(pagesNum, []byte(fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", pnum)))
	w.set(catalogNum, []byte(fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pagesNum)))
	return w.bytes(), nil
}

// RenderSheetsPDF 图框切分渲染为单个多页 PDF：DetectSheets 识别全部图框
// （无图框兜底整图单张），首页固定"整图全览"（口径同 RenderAllSheets：
// 全览张未显式指定名义宽度时钳到 SheetDefaultWidth），逐图框一页，
// 书签树 = 图名列表（打开即显示书签）。整图展开一次逐页复用，文本
// 整形器（字形轮廓缓存）跨页共享。
func RenderSheetsPDF(doc *drawing.Document, opts RenderOptions) ([]byte, error) {
	if doc == nil {
		return nil, fmt.Errorf("cad: 文档为空，无法渲染")
	}
	opts = normalizeSheetOptions(opts)
	sp := prepareSheets(doc)
	sheets := DetectSheets(doc)
	if len(sheets) != 1 || sheets[0].Name != "整图" {
		if b := quantileSheetBounds(sp.prims); !b.Invalid() {
			sheets = append([]Sheet{{Name: "整图全览", Box: [4]float64{b.MinX, b.MinY, b.MaxX, b.MaxY}}}, sheets...)
		}
	}
	shaper := newPDFShaper(opts.FontPath)
	w := &pdfWriter{}
	catalogNum := w.alloc()
	pagesNum := w.alloc()
	pageObjs := make([]int, 0, len(sheets))
	for _, s := range sheets {
		vp := sheetViewport(s.Box)
		width := sp.sheetWidthFor(vp, s, opts.Width)
		// 全览张未显式指定宽度时钳到 SheetDefaultWidth（口径同
		// RenderAllSheets：全览是预览定位，不参与自适应放大）
		if s.Name == "整图全览" && opts.Width <= 0 {
			width = SheetDefaultWidth
		}
		content := renderPagePDF(sp, vp, width, opts, shaper, true)
		pageObjs = append(pageObjs, appendPDFPage(w, content, vp))
	}
	kids := make([]string, len(pageObjs))
	for i, p := range pageObjs {
		kids[i] = fmt.Sprintf("%d 0 R", p)
	}
	w.set(pagesNum, []byte(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>",
		strings.Join(kids, " "), len(pageObjs))))
	catalog := fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R", pagesNum)
	if outlines := pdfAppendOutlines(w, pdfSheetNames(sheets), pageObjs); outlines > 0 {
		catalog += fmt.Sprintf(" /Outlines %d 0 R /PageMode /UseOutlines", outlines)
	}
	w.set(catalogNum, []byte(catalog+" >>"))
	return w.bytes(), nil
}

// pdfSheetNames 图框名清单（书签标题用）。
func pdfSheetNames(sheets []Sheet) []string {
	names := make([]string, len(sheets))
	for i, s := range sheets {
		names[i] = s.Name
	}
	return names
}
