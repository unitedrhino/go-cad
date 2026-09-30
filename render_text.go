// render_text.go 实现渲染链路的真实字形文本绘制：
// 字体探测（RenderOptions.FontPath 覆盖 → CJK 系统字体 wqy-zenhei.ttc 等
// 候选 → x/image/basicfont 回退，basicfont 仅覆盖 ASCII，CJK 需系统字体）
// → 按像素字号分档（8..256）光栅化 rune 覆盖率位图并按 (rune,档位) 缓存
// → 单一 2x2 仿射矩阵承载旋转/斜切/宽度因子/生成标志镜像/INSERT 负缩放，
// 逆映射 + 双线性采样写入画布（字形自带抗锯齿）。
// 对齐语义：TEXT/ATTRIB 按 hAlign/vAlign 锚定偏移（锚点由离散阶段按
// alignment_pt 语义选定为 insertion 或 alignPt）；MTEXT 按 attachment 1-9
// 附着、rect_width 贪心换行（空格优先断行）、linespace_factor×字高行距。
// 无可用字体或文字小于 0.75 像素时由调用方回退 textLabel 占位条或跳过。
//
// 主要模块划分：
//
//	字体加载：loadFont / parseFontFile / systemFont（sync.Once 缓存解析结果）
//	分档字面：textRenderer.face（按档位懒创建 opentype Face）
//	字形缓存：textRenderer.glyph / rasterize（map[glyphKey]*textGlyph）
//	版式换算：textLayoutOf（label 世界几何 → 像素空间基向量）
//	绘制入口：drawLabelText（canvas 懒初始化渲染器并分派单行/MTEXT）
//	掩码搬运：blitGlyph / warpMask（仿射逆映射 + 双线性覆盖率混合）
package cad

import (
	"image"
	"image/color"
	"math"
	"os"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// systemFontCandidates 系统字体探测路径（按序取第一个可解析者）。
// CJK 正黑优先（任务口径 wqy-zenhei.ttc 居首；实测其子字体在 x/image
// sfnt 下报 invalid table offset，解析失败自动落到下一候选）；
// DroidSansFallbackFull 为 Google 官方 CJK 回退矢量字体，简体全覆盖
// （实测本仓图纸 736 个非 ASCII 字符仅缺全角符号 9 个，由后备链补齐；
// 旧"缺 CJK cmap"结论系早期测试口径有误）；IPA Gothic 为日文矢量，
// 简体专用字形（报/总/线/电/设 等约 220/736）不在 JIS 集内仅作兜底；
// unifont 全 Unicode 点阵风格兜底；DejaVu 仅覆盖西文。
var systemFontCandidates = []string{
	"/usr/share/fonts/truetype/wqy/wqy-zenhei.ttc",              // 文泉驿正黑（CJK 主选）
	"/usr/share/fonts/truetype/droid/DroidSansFallbackFull.ttf", // Droid CJK 回退（简体全覆盖矢量）
	"/usr/share/fonts/opentype/ipafont-gothic/ipagp.ttf",        // IPA Gothic（日文矢量）
	"/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",    // Noto CJK（Debian 系）
	"/usr/share/fonts/truetype/wqy/wqy-microhei.ttc",            // 文泉驿微米黑
	"/usr/share/fonts/opentype/unifont/unifont.otf",             // Unifont（全 Unicode 兜底）
	"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",           // 仅西文兜底
}

// glyphBuckets 字形光栅字号档位（像素）：目标字号落档后缩放比 ≤1.5，
// 双线性采样质量稳定；超出 256 走最高档放大（大标题场景轻微软化）。
var glyphBuckets = []int{8, 12, 16, 24, 32, 48, 64, 96, 128, 192, 256}

// 文本渲染防御常量。
const (
	glyphCacheLimit = 8192    // rune 位图缓存条目上限（防异常海量字符撑爆内存）
	glyphAreaLimit  = 2 << 20 // 单字形目标面积上限（像素，超出按步长抽稀防病态大字卡死）
	minTextPx       = 0.75    // 低于该像素高的文字视为亚像素，跳过绘制
	mtextLineFactor = 1.66    // MTEXT 缺省行距（linespace_factor 未存储时的历史默认口径）
	basicBucket     = 13      // basicfont 固定位图字号
)

// 字体解析为进程级单例：opentype 解析十几 MB 的 TTC 有可观测开销，
// 解析结果可安全共享（opentype.Font 并发只读；Face 按次渲染单独创建）。
var (
	systemFontOnce  sync.Once
	cachedMainFont  *opentype.Font   // 首个可用的系统字体（nil=已探测且无可用字体）
	cachedFallbacks []*opentype.Font // 缺字后备字体链（≤2 个，按候选序）
)

// loadFont 加载渲染主字体：override 非空时优先即时解析（失败继续走
// 系统探测）；系统探测结果经 sync.Once 缓存。返回 nil 表示无可用
// opentype 字体（调用方回退 basicfont）。
func loadFont(override string) *opentype.Font {
	f, _ := loadFontChain(override)
	return f
}

// parseFontFile 解析单个字体文件（.ttf/.otf 直解析，.ttc 取集合首字体）。
// 解析失败返回 nil（文件缺失/损坏/非 OpenType 均按无字体处理）。
func parseFontFile(path string) *opentype.Font {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if f, err := opentype.Parse(data); err == nil {
		return f
	}
	if c, err := opentype.ParseCollection(data); err == nil {
		if f, err := c.Font(0); err == nil {
			return f
		}
	}
	return nil
}

// sizedFace 某一字号档的可用字面及其度量（有效像素字号 + 纵度）。
type sizedFace struct {
	face font.Face
	px   float64 // 有效像素字号（opentype=请求档位；basicfont 回退=13）
	asc  float64 // 上延（基线到字面顶部，px 像素）
	desc float64 // 下延（基线到字面底部，px 像素）
}

// newSizedFace 从 font.Face 提取度量（26.6 定点 → 像素）。
func newSizedFace(f font.Face, px float64) sizedFace {
	m := f.Metrics()
	return sizedFace{face: f, px: px, asc: float64(m.Ascent) / 64, desc: float64(m.Descent) / 64}
}

// textGlyph 单 rune 光栅化结果（某一字号档）。
type textGlyph struct {
	mask    *image.Gray // 覆盖率掩码（0..255）；nil 表示空字形（如空格）
	w, h    int         // 掩码尺寸（像素）
	dx, dy  int         // 掩码左上角相对基线原点的偏移（y 向下为正）
	advance float64     // 推进宽（档位像素；缺字给默认半角宽防粘连）
}

// glyphKey 字形缓存键：(rune, 字号档)。
type glyphKey struct {
	r      rune
	bucket int
}

// textRenderer 单次渲染的文本绘制器：持有画布引用、分档字面与字形缓存。
// 非并发安全——每个 RenderPNG 调用内懒创建、单协程使用。
type textRenderer struct {
	cv        *canvas
	otFont    *opentype.Font   // opentype 主字体（nil=basicfont 回退）
	fallbacks []*opentype.Font // 后备字体链：主字体缺字时按序补字（如 Droid 简体缺全角符号由 unifont 兜底）
	basic     bool             // basicfont 回退模式（固定 13px，仅 ASCII）
	faces     map[int]sizedFace
	fbFaces   []map[int]sizedFace // 与 fallbacks 一一对应的字号档缓存
	glyphs    map[glyphKey]*textGlyph
}

// newTextRenderer 构造文本渲染器：优先 override/系统 opentype 字体，
// 其余可解析系统候选按序留作后备字体（主字体缺字时补字）；无可用字体
// 时回退 x/image/basicfont（仅 ASCII 0x20-0x7E；CJK 需系统字体）。
func newTextRenderer(cv *canvas) *textRenderer {
	main, fallbacks := loadFontChain(cv.fontPath)
	if main != nil {
		return &textRenderer{
			cv: cv, otFont: main, fallbacks: fallbacks,
			faces: map[int]sizedFace{}, fbFaces: make([]map[int]sizedFace, len(fallbacks)),
			glyphs: map[glyphKey]*textGlyph{},
		}
	}
	return &textRenderer{
		cv:     cv,
		basic:  true,
		faces:  map[int]sizedFace{basicBucket: newSizedFace(basicfont.Face7x13, basicBucket)},
		glyphs: map[glyphKey]*textGlyph{},
	}
}

// loadFontChain 字体链加载：override 非空时解析为唯一主字体（系统候选
// 不再作后备）；否则系统候选按序取首个可解析者为主字体，其余可解析者
// 留作缺字后备（上限两个，防多份十几 MB TTC 常驻内存）。系统探测结果
// 经 systemFontOnce 缓存（与 loadFont 共用）。
func loadFontChain(override string) (main *opentype.Font, fallbacks []*opentype.Font) {
	if override != "" {
		if f := parseFontFile(override); f != nil {
			return f, nil
		}
	}
	systemFontOnce.Do(func() {
		for _, p := range systemFontCandidates {
			f := parseFontFile(p)
			if f == nil {
				continue
			}
			if cachedMainFont == nil {
				cachedMainFont = f
			} else if len(cachedFallbacks) < 2 {
				cachedFallbacks = append(cachedFallbacks, f)
			}
		}
	})
	return cachedMainFont, cachedFallbacks
}

// fallbackFace 取后备字体指定字号档的字面（懒创建；失败回退 basicfont
// 字面保持度量自洽——后备字形与主字形同档位像素，推进/缩放口径一致）。
func (tr *textRenderer) fallbackFace(i, bucket int) sizedFace {
	if sf, ok := tr.fbFaces[i][bucket]; ok {
		return sf
	}
	if tr.fbFaces[i] == nil {
		tr.fbFaces[i] = map[int]sizedFace{}
	}
	f, err := opentype.NewFace(tr.fallbacks[i], &opentype.FaceOptions{
		Size:    float64(bucket),
		DPI:     72,
		Hinting: font.HintingNone,
	})
	if err != nil {
		return newSizedFace(basicfont.Face7x13, basicBucket)
	}
	sf := newSizedFace(f, float64(bucket))
	tr.fbFaces[i][bucket] = sf
	return sf
}

// faceFor 按目标 em 像素高取字面（basic 模式固定 13px 档；其余按档位懒创建）。
func (tr *textRenderer) faceFor(emPx float64) sizedFace {
	bucket := basicBucket
	if !tr.basic {
		bucket = pickBucket(emPx)
	}
	return tr.face(bucket)
}

// face 取指定字号档的字面（懒创建）。NewFace 失败（合法字体下罕见）时
// 回退 basicfont 字面，px 记录实际有效字号保证后续度量自洽。
func (tr *textRenderer) face(bucket int) sizedFace {
	if sf, ok := tr.faces[bucket]; ok {
		return sf
	}
	f, err := opentype.NewFace(tr.otFont, &opentype.FaceOptions{
		Size:    float64(bucket),
		DPI:     72,
		Hinting: font.HintingNone,
	})
	if err != nil {
		return newSizedFace(basicfont.Face7x13, basicBucket)
	}
	sf := newSizedFace(f, float64(bucket))
	tr.faces[bucket] = sf
	return sf
}

// pickBucket 选字号档：取首个 ≥ 目标像素的档位，超界取最高档。
func pickBucket(px float64) int {
	for _, b := range glyphBuckets {
		if px <= float64(b) {
			return b
		}
	}
	return glyphBuckets[len(glyphBuckets)-1]
}

// glyph 取 rune 位图（带缓存；控制字符返回 nil）。缓存满后仍绘制但不再入缓存。
func (tr *textRenderer) glyph(r rune, sf sizedFace) *textGlyph {
	if r < 32 {
		return nil
	}
	key := glyphKey{r: r, bucket: int(sf.px + 0.5)}
	if g, ok := tr.glyphs[key]; ok {
		return g
	}
	g := tr.rasterize(r, sf)
	if len(tr.glyphs) < glyphCacheLimit {
		tr.glyphs[key] = g
	}
	return g
}

// rasterize 光栅化单 rune：主字体缺字时按序尝试后备字体补字（如 Droid
// 简体字形缺 ℃/≤/全角引号时由 unifont 全 Unicode 兜底），避免工程图
// 常用字符整字跳过形成缺字文本；掩码统一转为 *image.Gray 覆盖率图；
// 异常巨型字形（超过有效字号 3 倍，错位字体可能产生）丢弃笔画仅保留推进宽。
func (tr *textRenderer) rasterize(r rune, sf sizedFace) *textGlyph {
	dr, mask, maskp, adv, ok := sf.face.Glyph(fixed.Point26_6{}, r)
	if !ok {
		for i := range tr.fallbacks {
			sf2 := tr.fallbackFace(i, int(sf.px+0.5))
			dr2, mask2, maskp2, adv2, ok2 := sf2.face.Glyph(fixed.Point26_6{}, r)
			if !ok2 {
				continue
			}
			g := &textGlyph{advance: float64(adv2) / 64}
			buildGlyphMask(g, dr2, mask2, maskp2, sf2)
			return g
		}
		return &textGlyph{advance: sf.px * 0.5} // 全链缺字默认半角宽，避免整词粘连
	}
	g := &textGlyph{advance: float64(adv) / 64}
	buildGlyphMask(g, dr, mask, maskp, sf)
	return g
}

// buildGlyphMask 填充字形覆盖率掩码：异常巨型字形（宽高超字号 3 倍）
// 保留推进宽、不留掩码（绘制侧跳过笔画）。
func buildGlyphMask(g *textGlyph, dr image.Rectangle, mask image.Image, maskp image.Point, sf sizedFace) {
	w, h := dr.Dx(), dr.Dy()
	if w <= 0 || h <= 0 || w > int(sf.px)*3+8 || h > int(sf.px)*3+8 {
		return
	}
	gray := image.NewGray(image.Rect(0, 0, w, h))
	if am, isAlpha := mask.(*image.Alpha); isAlpha {
		// 快路径：opentype 常态输出 *image.Alpha，逐像素直拷
		for y := 0; y < h; y++ {
			row := gray.Pix[y*gray.Stride : y*gray.Stride+w]
			for x := 0; x < w; x++ {
				row[x] = am.AlphaAt(maskp.X+x, maskp.Y+y).A
			}
		}
	} else {
		// 通用路径：basicfont 的位图等掩码经 At() 取 alpha 覆盖率
		for y := 0; y < h; y++ {
			row := gray.Pix[y*gray.Stride : y*gray.Stride+w]
			for x := 0; x < w; x++ {
				_, _, _, aa := mask.At(maskp.X+x, maskp.Y+y).RGBA()
				row[x] = byte(aa >> 8)
			}
		}
	}
	g.mask = gray
	g.w, g.h = w, h
	g.dx, g.dy = dr.Min.X, dr.Min.Y
}

// textLayout 单条文本的像素空间版式（由 label 世界几何换算）。
type textLayout struct {
	px, py      float64 // 锚点像素坐标
	upx, upy    float64 // 推进方向基向量（像素/世界单位，已含实体变换与镜像）
	vpx, vpy    float64 // 字面向上方向基向量（像素/世界单位）
	emPx        float64 // em 像素高（= 字高 × 推进方向像素比例）
	hWorld      float64 // 字高（世界单位）
	widthFactor float64 // 宽度因子（TEXT/ATTRIB 解码 width_factor，未读为 1；MTEXT 恒 1）
	obliqueRad  float64 // 倾斜角（弧度，近似斜切 shear，默认 0，预留参数）
	mirrorX     bool    // 生成标志 0x2：X 镜像（backwards）
	mirrorY     bool    // 生成标志 0x4：Y 镜像（upside-down）
	hAlign      uint16  // 水平对齐（DXF 0 左 / 1 中 / 2 右 / 3,4,5 对齐变体）
	vAlign      uint16  // 垂直对齐（0 基线 / 1 下 / 2 中 / 3 上）
}

// textLayoutOf 由 label 携带的世界几何换算像素版式。
// 方向基向量在离散阶段按实体变换差分获得（INSERT 负缩放镜像精确成立），
// 世界→像素为等比缩放 + Y 翻转，基向量直接线性映射。
// 返回 false 表示几何退化（零向量）或亚像素文字，调用方回退占位条/跳过。
func textLayoutOf(c *canvas, lb *label, tx *textInfo) (textLayout, bool) {
	upx, upy := tx.ux*c.scale, -tx.uy*c.scale
	vpx, vpy := tx.vx*c.scale, -tx.vy*c.scale
	uLen := math.Hypot(upx, upy)
	vLen := math.Hypot(vpx, vpy)
	if uLen < 1e-9 || vLen < 1e-9 {
		return textLayout{}, false
	}
	emPx := uLen * tx.hWorld
	if emPx < minTextPx {
		return textLayout{}, false
	}
	px, py := c.toPixel(point2{lb.x, lb.y})
	return textLayout{
		px: px, py: py,
		upx: upx, upy: upy,
		vpx: vpx, vpy: vpy,
		emPx:        emPx,
		hWorld:      tx.hWorld,
		widthFactor: tx.widthFactor,
		obliqueRad:  tx.oblique,
		mirrorX:     tx.gen&0x2 != 0,
		mirrorY:     tx.gen&0x4 != 0,
		hAlign:      tx.hAlign,
		vAlign:      tx.vAlign,
	}, true
}

// measureEm 量测单行文本宽（em 单位，含宽度因子；触发字形光栅化并走缓存）。
func (tr *textRenderer) measureEm(line string, sf sizedFace, wf float64) float64 {
	w := 0.0
	for _, r := range line {
		g := tr.glyph(r, sf)
		if g == nil {
			continue
		}
		w += g.advance
	}
	return w / sf.px * wf
}

// drawSingleLine 绘制单行文本（TEXT/ATTRIB 语义）：按 hAlign/vAlign 计算
// 行内偏移后落笔。uOff/vOff 为世界单位（v 正方向 = 字面向上）。
func (tr *textRenderer) drawSingleLine(l textLayout, line string, uOff, vOff float64, col color.RGBA) {
	sf := tr.faceFor(l.emPx)
	wWorld := tr.measureEm(line, sf, l.widthFactor) * l.hWorld
	// 水平：0 左 / 1,3,4,5 居中（aligned/fit 近似居中）/ 2 右
	switch l.hAlign {
	case 2:
		uOff -= wWorld
	case 1, 3, 4, 5:
		uOff -= wWorld / 2
	}
	// 垂直（字面纵度近似）：1 下 = 基线落底缘 / 2 中 = 字面 extent 中心对齐锚点
	//（DXF 73=middle 语义；字形从基线向上出墨，基线须落在锚点下方 ~0.35 字高，
	// 若取 +0.35h 字形整体高 ~0.7 字高——公司名叠进上方轮廓 Logo 实证）
	switch l.vAlign {
	case 1:
		vOff += sf.desc / sf.px * l.hWorld
	case 2:
		vOff -= 0.35 * l.hWorld
	case 3:
		vOff -= 0.70 * l.hWorld
	}
	tr.drawLineAt(l, line, sf, uOff, vOff, col)
}

// drawLineAt 以锚点偏移（世界单位）绘制一行：逐 rune 累计推进并按仿射
// 矩阵搬运字形掩码。
func (tr *textRenderer) drawLineAt(l textLayout, line string, sf sizedFace, uOff, vOff float64, col color.RGBA) {
	// 锚点偏移：世界单位沿基向量方向映射像素（基向量已含世界→像素比例）
	p0x := l.px + l.upx*uOff + l.vpx*vOff
	p0y := l.py + l.upy*uOff + l.vpy*vOff
	pen := 0.0 // em 单位推进（advance 已含宽度因子）
	for _, r := range line {
		g := tr.glyph(r, sf)
		if g == nil {
			continue
		}
		if g.mask != nil {
			tr.blitGlyph(g, l, p0x, p0y, pen, sf, col)
		}
		pen += g.advance / sf.px * l.widthFactor
	}
}

// blitGlyph 计算单字形掩码坐标→像素的仿射并搬运：
// em 空间先做宽度因子/斜切/镜像（u,v 轴），再经基向量映射像素。
// 掩码 (mx,my)（y 向下）→ 像素 Q = C + A*mx + B*my，逆映射逐像素采样。
func (tr *textRenderer) blitGlyph(g *textGlyph, l textLayout, p0x, p0y, penEm float64, sf sizedFace, col color.RGBA) {
	b := sf.px
	// 镜像符号：X 镜像翻转推进与掩码 u；Y 镜像翻转 v（基线偏移整体翻转）
	su, sv := 1.0, 1.0
	if l.mirrorX {
		su = -1
	}
	if l.mirrorY {
		sv = -1
	}
	// em 空间常量项（掩码原点 (0,0) 处）：
	// Cu = su*(pen + wf*dx/b + ob*topEm)；Cv = sv*topEm，
	// topEm = -dy/b 为掩码顶行相对基线的高度（dy 负=基线上方，Go 字形
	// 矩形 Y 向下）。掩码行偏移已含基线→顶行的全部信息，不再叠加 asc——
	// 旧口径 Cv=asc−dy 多计一段上延，全部文字整体抬高 ~1em（红色注释
	// 错位/单元格文字浮出实证）；penEm 由 drawLineAt 按 wf 压缩后的推进
	// 传入，此处不再乘 wf——双重压缩会把字符推进压到 wf²（width_factor
	// 0.55 实证）
	ob := math.Tan(l.obliqueRad)
	dxE, dyE := float64(g.dx)/b, float64(g.dy)/b
	topEm := -dyE
	cu := su * (penEm + l.widthFactor*dxE + ob*topEm)
	cv := sv * topEm
	// 掩码坐标系数（像素/掩码px）：u 轴 wf/b，u-v 斜切耦合 ob/b，v 轴 -1/b
	h := l.hWorld
	ax := l.upx * h * su * l.widthFactor / b
	ay := l.upy * h * su * l.widthFactor / b
	bx := l.upx*h*(-su*ob/b) + l.vpx*h*(-sv/b)
	by := l.upy*h*(-su*ob/b) + l.vpy*h*(-sv/b)
	cx := p0x + l.upx*h*cu + l.vpx*h*cv
	cy := p0y + l.upy*h*cu + l.vpy*h*cv
	tr.warpMask(g.mask, cx, cy, ax, ay, bx, by, col)
}

// warpMask 仿射逆映射搬运掩码：目标包围盒内逐像素反解掩码坐标，
// 双线性采样覆盖率后做 source-over 预乘混合。
// cx,cy 为掩码 (0,0) 的像素坐标；(ax,ay) 为掩码 x 步进向量，(bx,by) 为 y 步进。
func (tr *textRenderer) warpMask(mask *image.Gray, cx, cy, ax, ay, bx, by float64, col color.RGBA) {
	det := ax*by - bx*ay
	if math.Abs(det) < 1e-12 {
		return // 退化变换（零尺度/完全剪切塌缩），无可视字形
	}
	w, h := mask.Rect.Dx(), mask.Rect.Dy()
	fw, fh := float64(w), float64(h)
	xs := [4]float64{cx, cx + ax*fw, cx + bx*fh, cx + ax*fw + bx*fh}
	ys := [4]float64{cy, cy + ay*fw, cy + by*fh, cy + ay*fw + by*fh}
	minX, maxX := xs[0], xs[0]
	minY, maxY := ys[0], ys[0]
	for i := 1; i < 4; i++ {
		if xs[i] < minX {
			minX = xs[i]
		}
		if xs[i] > maxX {
			maxX = xs[i]
		}
		if ys[i] < minY {
			minY = ys[i]
		}
		if ys[i] > maxY {
			maxY = ys[i]
		}
	}
	bounds := tr.cv.img.Bounds()
	px0, py0 := int(math.Floor(minX))-1, int(math.Floor(minY))-1
	px1, py1 := int(math.Ceil(maxX))+1, int(math.Ceil(maxY))+1
	if px0 < bounds.Min.X {
		px0 = bounds.Min.X
	}
	if py0 < bounds.Min.Y {
		py0 = bounds.Min.Y
	}
	if px1 > bounds.Max.X {
		px1 = bounds.Max.X
	}
	if py1 > bounds.Max.Y {
		py1 = bounds.Max.Y
	}
	if px0 >= px1 || py0 >= py1 {
		return
	}
	// 病态大字防御：目标面积超限时按步长抽稀（标题字仍可见，避免单字卡死）
	stride := 1
	if area := float64((px1 - px0) * (py1 - py0)); area > glyphAreaLimit {
		stride = int(math.Sqrt(area/glyphAreaLimit)) + 1
	}
	invDet := 1 / det
	pix := mask.Pix
	strideMask := mask.Stride
	for py := py0; py < py1; py += stride {
		fy := float64(py) + 0.5 - cy
		for px := px0; px < px1; px += stride {
			fx := float64(px) + 0.5 - cx
			mx := (fx*by - bx*fy) * invDet
			my := (ax*fy - fx*ay) * invDet
			if mx < 0 || my < 0 || mx >= fw || my >= fh {
				continue
			}
			// 双线性覆盖率采样（越界钳制到边缘）
			x0, y0 := int(mx), int(my)
			x1, y1 := x0+1, y0+1
			if x1 >= w {
				x1 = w - 1
			}
			if y1 >= h {
				y1 = h - 1
			}
			fx2, fy2 := mx-float64(x0), my-float64(y0)
			c00 := float64(pix[y0*strideMask+x0])
			c10 := float64(pix[y0*strideMask+x1])
			c01 := float64(pix[y1*strideMask+x0])
			c11 := float64(pix[y1*strideMask+x1])
			cov := ((c00*(1-fx2)+c10*fx2)*(1-fy2) + (c01*(1-fx2)+c11*fx2)*fy2) / 255
			if cov > 0 {
				blendCoverage(tr.cv.img, px, py, col, cov)
			}
		}
	}
}

// blendCoverage source-over 预乘混合：col 视为不透明前景，按覆盖率 cov 与
// 目标既有像素合成（image.RGBA Pix 为预乘布局，字形边缘平滑过渡）。
func blendCoverage(img *image.RGBA, x, y int, col color.RGBA, cov float64) {
	if cov > 1 {
		cov = 1
	}
	i := img.PixOffset(x, y)
	keep := 1 - cov
	img.Pix[i+0] = byte(float64(col.R)*cov + float64(img.Pix[i+0])*keep + 0.5)
	img.Pix[i+1] = byte(float64(col.G)*cov + float64(img.Pix[i+1])*keep + 0.5)
	img.Pix[i+2] = byte(float64(col.B)*cov + float64(img.Pix[i+2])*keep + 0.5)
	img.Pix[i+3] = byte(255*cov + float64(img.Pix[i+3])*keep + 0.5)
}

// drawMText 绘制 MTEXT：rect_width 贪心换行 → attachment 1-9 计算块内
// 各行基线偏移（行距 = linespace_factor×字高，实测 AutoCAD extents 口径；
// factor 未存时按旧 1.66 兜底）→ 逐行按块宽对齐落笔。
func (tr *textRenderer) drawMText(l textLayout, tx *textInfo, col color.RGBA) {
	sf := tr.faceFor(l.emPx)
	lines := tx.lines
	if tx.rectWidth > 0 {
		lines = tr.wrapLines(lines, tx.rectWidth/l.hWorld, sf, l.widthFactor)
	}
	if len(lines) == 0 {
		return
	}
	// 各行宽（世界单位）与块最大宽
	ws := make([]float64, len(lines))
	maxW := 0.0
	for i, ln := range lines {
		ws[i] = tr.measureEm(ln, sf, l.widthFactor) * l.hWorld
		if ws[i] > maxW {
			maxW = ws[i]
		}
	}
	// 行距：linespace_factor（DXF 44，R2000+ 存储）×字高。1.66 旧口径经
	// gold extents 实证虚高 66%（设计说明 11 行块底压进签名栏，extents_height
	// 3566.7 仅容 factor×h 口径）；factor 未存储（pre-R2000/JSON·DXF 未带）
	// 时保留 1.66 兜底（AutoCAD 历史默认单倍行距语义）
	lh := mtextLineFactor * l.hWorld
	if tx.lineFactor > 0 {
		lh = tx.lineFactor * l.hWorld
	}
	ascWorld := sf.asc / sf.px * l.hWorld         // 首行上延（世界单位）
	blockH := float64(len(lines)-1)*lh + l.hWorld // 块高：末行基线 + 名义字高
	att := tx.attachment                          // 附着点 1-9，越界按左上
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
		tr.drawLineAt(l, ln, sf, uOff, baseline0-float64(i)*lh, col)
	}
}

// wrapLines 按列宽（em 单位 limit）贪心换行：累计推进超过列宽即断行，
// 优先最近空格（空格留在上行行尾），无空格长串按字符硬断。
// 近似 AutoCAD rect_width 列宽排版。
func (tr *textRenderer) wrapLines(lines []string, limit float64, sf sizedFace, wf float64) []string {
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
			g := tr.glyph(r, sf)
			if g != nil {
				w += g.advance / sf.px * wf
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
				w = tr.measureEm(string(runes[start:i+1]), sf, wf)
			}
		}
		out = append(out, string(runes[start:]))
	}
	return out
}

// drawLabelText canvas 侧文本绘制入口：懒初始化渲染器后按单行/MTEXT
// 分派；无可用字体或版式退化时回退 textLabel 占位条。
func (c *canvas) drawLabelText(lb label, tx *textInfo, col color.RGBA) {
	if c.tr == nil {
		c.tr = newTextRenderer(c)
	}
	tr := c.tr
	l, ok := textLayoutOf(c, &lb, tx)
	if !ok {
		c.drawLabel(lb, col)
		return
	}
	if tx.attachment != 0 {
		tr.drawMText(l, tx, col)
		return
	}
	line := ""
	if len(tx.lines) > 0 {
		line = tx.lines[0] // 单行语义（TEXT/ATTRIB），多行内容取首行
	}
	tr.drawSingleLine(l, line, 0, 0, col)
}
