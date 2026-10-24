// pdf_text.go 实现 PDF 导出的真实字形文本矢量路径化：
// 字体轮廓提取（sfnt LoadGlyph，ppem=unitsPerEm 即未缩放字体单位）→
// 归一化为 em 单位浮点段并按 rune 缓存 → 版式换算（对齐/换行/行距/
// 镜像/宽度因子/倾斜，与 render_text.go 的像素口径逐步同构）→ 输出
// 世界坐标路径段交 pdfEmitter 生成 PDF 填充路径。
//
// 与 PNG 光栅路径（render_text.go）的分工：
//   - PNG：字形按字号档光栅化为覆盖率掩码，逆映射逐像素搬运（warpMask）；
//   - PDF：字形轮廓直接经同一套基向量仿射到世界坐标，矢量填充（f 算子），
//     无字号分档（矢量天然无级缩放）、无像素清晰度概念。
//
// 版式逻辑（measureEm/对齐偏移/MTEXT 换行与附着）为 render_text.go 的
// 世界坐标同构实现，两处口径须同步维护；不共用一份代码的原因：像素路径
// 深度耦合字号分档与画布坐标（textLayout/sizedFace），强行统一会把
// 金标已固化的 PNG 渲染暴露给重构回归风险。
//
// 无可用 opentype 字体（basicfont 位图无轮廓）或 label 无字形版式
// （Tx == nil，PNG 同口径走占位条）时，由调用方回退半高占位框——
// 与 SVG appendLabelBox / PNG drawLabel 同款几何。
//
// 主要模块划分：
//
//	字形提取：pdfShaper.glyph（sfnt 轮廓 → em 浮点段缓存，缺字走后备链）
//	度量：pdfShaper.metrics / measureEmPDF（上延/下延/行宽，em 单位）
//	版式：pdfLayout / pdfLayoutOf（label 世界几何 → 基向量版式）
//	单行绘制：drawSingleLinePDF（TEXT/ATTRIB 对齐语义）
//	MTEXT 绘制：drawMTextPDF（贪心换行 + attachment 1-9 附着）
//	落笔：appendLineAt（逐 rune 推进，字形段仿射输出到 emitter）
package render

import (
	"math"
	"strings"

	"github.com/unitedrhino/go-cad/internal/drawing"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// pdfSegOp 矢量路径段算子（与 sfnt.SegmentOp 一一对应；quad 在输出层
// 升 cubic，缓存层保留 quad 避免精度损耗）。
type pdfSegOp uint8

const (
	pdfSegMoveTo pdfSegOp = iota
	pdfSegLineTo
	pdfSegQuadTo
	pdfSegCubeTo
)

// pdfSeg 单段路径：点坐标为 em 单位（字体单位 / unitsPerEm），Y 轴向下
// （与 sfnt LoadGlyph 返回口径一致，落笔时经基向量仿射到世界坐标 Y 向上）。
type pdfSeg struct {
	op pdfSegOp
	// p 按算子语义取用：Move/Line 用 p[0]；Quad 用 p[0]=控制 p[1]=终点；
	// Cube 用 p[0]、p[1]=控制点，p[2]=终点（起点为上一段终点）。
	p [3][2]float64
}

// pdfGlyph 单 rune 矢量字形：em 单位轮廓段与推进宽。segs 为 nil 表示
// 无轮廓（空格、缺字或彩色字形）——只推进不出墨，与 PNG 空字形掩码
// 为空的口径一致。
type pdfGlyph struct {
	segs []pdfSeg
	adv  float64 // em 推进宽（含后备字体归一化；全链缺字默认半角宽）
}

// pdfShaper PDF 文本矢量整形器：持有字体链（主字体 + 后备，复用
// render_text.go 的进程级缓存 loadFontChain）与 rune 级字形缓存。
// opentype.Font 即 sfnt.Font（类型别名），直接使用 sfnt 底层 API。
// 非并发安全——单次渲染内创建使用（RenderSheetsPDF 跨页复用一份）。
type pdfShaper struct {
	fonts []*opentype.Font // 主字体 + 后备链；空切片=无 opentype 字体（全部回退占位框）
	buf   sfnt.Buffer      // sfnt 复用缓冲（LoadGlyph 返回段在 buf 复用后失效，提取时立即拷贝）
	cache map[rune]*pdfGlyph
}

// newPDFShaper 构造整形器：字体链与 PNG 渲染同一来源（RenderOptions.
// FontPath 覆盖 → 系统候选探测），保证 PDF 文本与 PNG 选用相同字形。
func newPDFShaper(fontPath string) *pdfShaper {
	main, fallbacks := loadFontChain(fontPath)
	if main == nil {
		return &pdfShaper{cache: map[rune]*pdfGlyph{}}
	}
	fonts := append([]*opentype.Font{main}, fallbacks...)
	return &pdfShaper{fonts: fonts, cache: map[rune]*pdfGlyph{}}
}

// glyph 取 rune 矢量字形（em 单位段，带缓存）：主字体缺字按序尝试后备
// 字体（与 rasterize 的补字链一致），全链缺字给默认半角宽不出墨。
// 控制字符无字形（与 PNG glyph 口径一致）。
func (sh *pdfShaper) glyph(r rune) *pdfGlyph {
	if r < 32 {
		return nil
	}
	if g, ok := sh.cache[r]; ok {
		return g
	}
	g := &pdfGlyph{adv: 0.5}
	for _, f := range sh.fonts {
		gid, err := f.GlyphIndex(&sh.buf, r)
		if err != nil || gid == 0 {
			continue
		}
		upem := fixed.Int26_6(f.UnitsPerEm())
		segs, err := f.LoadGlyph(&sh.buf, gid, upem, nil)
		if err != nil {
			continue // 彩色字形等无单色轮廓者跳过，尝试下一字体
		}
		if adv, err := f.GlyphAdvance(&sh.buf, gid, upem, font.HintingNone); err == nil {
			g.adv = float64(adv) / float64(f.UnitsPerEm())
		}
		g.segs = pdfSegsOf(segs, float64(f.UnitsPerEm()))
		break
	}
	sh.cache[r] = g
	return g
}

// pdfSegsOf sfnt 段列表 → em 单位浮点段（立即拷贝：sfnt.Buffer 复用后
// 原段失效）。坐标除以 unitsPerEm 归一化，跨字体（不同 upem）缓存同构。
func pdfSegsOf(segs sfnt.Segments, upem float64) []pdfSeg {
	if len(segs) == 0 || upem <= 0 {
		return nil
	}
	out := make([]pdfSeg, 0, len(segs))
	pt := func(p fixed.Point26_6) [2]float64 {
		return [2]float64{float64(p.X) / upem, float64(p.Y) / upem}
	}
	for _, s := range segs {
		switch s.Op {
		case sfnt.SegmentOpMoveTo:
			out = append(out, pdfSeg{op: pdfSegMoveTo, p: [3][2]float64{pt(s.Args[0])}})
		case sfnt.SegmentOpLineTo:
			out = append(out, pdfSeg{op: pdfSegLineTo, p: [3][2]float64{pt(s.Args[0])}})
		case sfnt.SegmentOpQuadTo:
			out = append(out, pdfSeg{op: pdfSegQuadTo, p: [3][2]float64{pt(s.Args[0]), pt(s.Args[1])}})
		case sfnt.SegmentOpCubeTo:
			out = append(out, pdfSeg{op: pdfSegCubeTo, p: [3][2]float64{pt(s.Args[0]), pt(s.Args[1]), pt(s.Args[2])}})
		}
	}
	return out
}

// pdfMetrics 主字体的上延/下延（em 单位）：vAlign=1（下对齐）与 MTEXT
// 首行基线计算用。ppem 传 unitsPerEm 得未缩放值再归一化。
func (sh *pdfShaper) metrics() (asc, desc float64) {
	if len(sh.fonts) == 0 {
		return 0.8, -0.2 // 无字体兜底：常用字体的大致纵度
	}
	f := sh.fonts[0]
	m, err := f.Metrics(&sh.buf, fixed.Int26_6(f.UnitsPerEm()), font.HintingNone)
	if err != nil {
		return 0.8, -0.2
	}
	upem := float64(f.UnitsPerEm())
	return float64(m.Ascent) / upem, float64(m.Descent) / upem
}

// measureEmPDF 量测单行文本宽（em 单位，含宽度因子；字形走缓存）。
func (sh *pdfShaper) measureEmPDF(line string, wf float64) float64 {
	w := 0.0
	for _, r := range line {
		if g := sh.glyph(r); g != nil {
			w += g.adv
		}
	}
	return w * wf
}

// pdfLayout 单条文本的世界坐标版式：基向量直接使用 GlyphTextInfo 的
// 世界基向量（未归一化，模长含 INSERT 变换比例），与 render_text.go 的
// textLayout 像素版逐步同构——那边乘 scale 并翻转 Y，这边保持世界系。
type pdfLayout struct {
	ax, ay float64 // 锚点世界坐标（基线起点）
	ux, uy float64 // 推进方向基向量（世界单位/单位推进，含变换与镜像）
	vx, vy float64 // 字面向上方向基向量
	h      float64 // 字高（世界单位）
	wf     float64 // 宽度因子
	ob     float64 // 倾斜角正切（近似斜切）
	mx, my bool    // 生成标志镜像：0x2 X 镜像（backwards）、0x4 Y 镜像（upside-down）
	hAlign uint16  // 水平对齐（DXF 0 左 / 1 中 / 2 右 / 3,4,5 对齐变体）
	vAlign uint16  // 垂直对齐（0 基线 / 1 下 / 2 中 / 3 上）
}

// pdfLayoutOf 由 label 世界几何与字形版式信息构造 PDF 版式；返回 false
// 表示基向量退化（零向量），调用方回退占位框。
func pdfLayoutOf(lb *drawing.Label, tx *drawing.GlyphTextInfo) (pdfLayout, bool) {
	if math.Hypot(tx.Ux, tx.Uy) < 1e-9 || math.Hypot(tx.Vx, tx.Vy) < 1e-9 {
		return pdfLayout{}, false
	}
	return pdfLayout{
		ax: lb.X, ay: lb.Y,
		ux: tx.Ux, uy: tx.Uy,
		vx: tx.Vx, vy: tx.Vy,
		h:      tx.HWorld,
		wf:     tx.WidthFactor,
		ob:     math.Tan(tx.Oblique),
		mx:     tx.Gen&0x2 != 0,
		my:     tx.Gen&0x4 != 0,
		hAlign: tx.HAlign,
		vAlign: tx.VAlign,
	}, true
}

// drawTextPDF 文本绘制入口（emitter 侧调用）：单行（TEXT/ATTRIB）与
// MTEXT 分派，与 drawLabelText 同构——attachment != 0 判 MTEXT，单行
// 取 tx.Lines[0]（离散阶段已把剥离格式码后的文本放入 Lines）。
func (sh *pdfShaper) drawTextPDF(em *pdfEmitter, lb *drawing.Label, tx *drawing.GlyphTextInfo, col rgbKey) {
	l, ok := pdfLayoutOf(lb, tx)
	if !ok {
		em.appendLabelBoxPDF(lb, col)
		return
	}
	if tx.Attachment != 0 {
		sh.drawMTextPDF(em, l, tx, col)
		return
	}
	line := ""
	if len(tx.Lines) > 0 {
		line = tx.Lines[0] // 单行语义（TEXT/ATTRIB），多行内容取首行
	}
	sh.drawSingleLinePDF(em, l, line, col)
}

// drawSingleLinePDF 绘制单行文本（TEXT/ATTRIB 语义）：hAlign/vAlign
// 行内偏移后落笔。与 drawSingleLine 逐步同构，对齐偏移度量口径一致
// （desc/0.35/0.70，见 render_text.go 的实证注释）。
func (sh *pdfShaper) drawSingleLinePDF(em *pdfEmitter, l pdfLayout, line string, col rgbKey) {
	_, desc := sh.metrics()
	wWorld := sh.measureEmPDF(line, l.wf) * l.h
	uOff, vOff := 0.0, 0.0
	// 水平：0 左 / 1,3,4,5 居中（aligned/fit 近似居中）/ 2 右
	switch l.hAlign {
	case 2:
		uOff -= wWorld
	case 1, 3, 4, 5:
		uOff -= wWorld / 2
	}
	// 垂直（字面纵度近似）：1 下 = 基线落底缘 / 2 中 = 基线落锚点下方
	// ~0.35 字高 / 3 上（与 drawSingleLine 的公司名叠 Logo 实证口径一致）
	switch l.vAlign {
	case 1:
		vOff += desc * l.h // desc 为 Go 字体惯例负值（下延），沿字面向下移基线
	case 2:
		vOff -= 0.35 * l.h
	case 3:
		vOff -= 0.70 * l.h
	}
	sh.appendLineAt(em, l, line, uOff, vOff, col)
}

// drawMTextPDF 绘制 MTEXT：rect_width 贪心换行 → attachment 1-9 计算
// 块内各行基线偏移（行距 = linespace_factor×字高，未存按 1.66 历史默认）
// → 逐行按块宽对齐落笔。与 drawMText 逐步同构。
func (sh *pdfShaper) drawMTextPDF(em *pdfEmitter, l pdfLayout, tx *drawing.GlyphTextInfo, col rgbKey) {
	lines := tx.Lines
	if tx.RectWidth > 0 {
		lines = sh.wrapLinesPDF(lines, tx.RectWidth/l.h, l.wf)
	}
	if len(lines) == 0 {
		return
	}
	ws := make([]float64, len(lines))
	maxW := 0.0
	for i, ln := range lines {
		ws[i] = sh.measureEmPDF(ln, l.wf) * l.h
		if ws[i] > maxW {
			maxW = ws[i]
		}
	}
	// 行距：linespace_factor（DXF 44）×字高，未存储时 1.66 兜底
	lh := drawing.MtextLineFactor * l.h
	if tx.LineFactor > 0 {
		lh = tx.LineFactor * l.h
	}
	ascEm, _ := sh.metrics()
	ascWorld := ascEm * l.h // 首行上延（世界单位）
	blockH := float64(len(lines)-1)*lh + l.h
	att := tx.Attachment
	if att < 1 || att > 9 {
		att = 1
	}
	// 垂直附着：1-3 行=上/中/下 → 首行基线相对锚点（v 向上为正）
	var baseline0 float64
	switch (att-1)/3 + 1 {
	case 2: // 中
		baseline0 = blockH/2 - ascWorld
	case 3: // 下
		baseline0 = blockH - ascWorld
	default: // 上
		baseline0 = -ascWorld
	}
	// 水平附着：1,4,7=左 / 2,5,8=中 / 3,6,9=右（逐行对齐块宽）
	for i, ln := range lines {
		uOff := 0.0
		switch (att-1)%3 + 1 {
		case 2:
			uOff = (maxW - ws[i]) / 2
		case 3:
			uOff = maxW - ws[i]
		}
		sh.appendLineAt(em, l, ln, uOff, baseline0-float64(i)*lh, col)
	}
}

// wrapLinesPDF 按列宽（em 单位 limit）贪心换行：与 wrapLines 同构——
// 累计推进超列宽即断行，优先最近空格（空格留上行行尾），无空格长串
// 按字符硬断。
func (sh *pdfShaper) wrapLinesPDF(lines []string, limit, wf float64) []string {
	if limit <= 0 {
		return lines
	}
	out := make([]string, 0, len(lines)+4)
	for _, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			out = append(out, "")
			continue
		}
		runes := []rune(line)
		start := 0
		w := 0.0
		lastSpace := -1
		for i, r := range runes {
			if g := sh.glyph(r); g != nil {
				w += g.adv * wf
			}
			if r == ' ' {
				lastSpace = i
			}
			if w > limit && i > start {
				cut := i
				if lastSpace > start {
					cut = lastSpace + 1
				}
				out = append(out, string(runes[start:cut]))
				start = cut
				w = sh.measureEmPDF(string(runes[start:i+1]), wf)
			}
		}
		out = append(out, string(runes[start:]))
	}
	return out
}

// appendLineAt 以锚点偏移（世界单位）绘制一行：逐 rune 累计推进（em，
// 含宽度因子压缩），字形轮廓段经基向量仿射输出到 emitter。
// 与 drawLineAt 同构；仿射矩阵与 blitGlyph 的掩码版逐步等价：
//
//	字形 em 点 (gx, gy)（Y 向下）→ 世界
//	W = P0 + U·h·(su·pen) + gx·A + gy·B
//	A = U·(h·su·wf)，B = U·(−h·su·ob) + V·(−h·sv)
//
// 其中 P0 = 锚点 + U·uOff + V·vOff，su/sv 为镜像符号，U/V 为基向量。
// gy 项系数取负的来源：字形 Y 向下、世界字面向上，同掩码版 v 轴 -1/b。
func (sh *pdfShaper) appendLineAt(em *pdfEmitter, l pdfLayout, line string, uOff, vOff float64, col rgbKey) {
	// 锚点偏移后的基线落笔原点（世界坐标）
	p0x := l.ax + l.ux*uOff + l.vx*vOff
	p0y := l.ay + l.uy*uOff + l.vy*vOff
	su, sv := 1.0, 1.0
	if l.mx {
		su = -1
	}
	if l.my {
		sv = -1
	}
	// 字形段仿射系数（世界单位/em）
	ax, ay := l.ux*l.h*su*l.wf, l.uy*l.h*su*l.wf
	bx, by := l.ux*(-l.h*su*l.ob)+l.vx*(-l.h*sv), l.uy*(-l.h*su*l.ob)+l.vy*(-l.h*sv)
	pen := 0.0 // em 推进（advance 未含宽度因子，落笔时与 wf 一并折算）
	for _, r := range line {
		g := sh.glyph(r)
		if g == nil {
			continue
		}
		if len(g.segs) > 0 {
			// 常量项：pen 推进 + 镜像符号（gx 项已含 su，pen 侧单独乘）
			cx := p0x + l.ux*l.h*su*pen
			cy := p0y + l.uy*l.h*su*pen
			em.appendGlyphSegs(g.segs, cx, cy, ax, ay, bx, by, col)
		}
		pen += g.adv * l.wf
	}
}
