// render.go 实现纯 Go 的模型空间光栅化渲染：
// INSERT 递归展开（仿射变换）→ 包围盒自适应视口 → 图元离散为线段 → 厚线段绘制。
// 文本经 textLabel 估算占位几何并携带 textInfo 版式，由 render_text.go 以
// 系统字体真实字形绘制（旋转/镜像/对齐/换行），无可用字体时回退基线占位条。
package render

import (
	"bytes"
	"fmt"
	"github.com/unitedrhino/go-cad/internal/drawing"
	"github.com/unitedrhino/go-cad/internal/entity"
	"image"
	"image/color"
	"image/png"
	"math"
)

// RenderOptions 渲染参数。
type RenderOptions struct {
	Width      int        // 输出宽度像素，默认 2048；高度按包围盒比例确定
	Background color.RGBA // 背景色，默认白色
	FontPath   string     // 字形渲染字体文件路径（空=自动探测系统字体；无可用字体回退占位条）
}

// RenderPNG 将文档模型空间渲染为 PNG 字节流。
func RenderPNG(doc *drawing.Document, opts RenderOptions) ([]byte, error) {
	// nil 文档防御：与其他导出 API 的错误返回口径一致，不 panic
	if doc == nil {
		return nil, fmt.Errorf("cad: 文档为空，无法渲染")
	}
	if opts.Width <= 0 {
		opts.Width = 2048
	}
	if opts.Background == (color.RGBA{}) {
		opts.Background = color.RGBA{255, 255, 255, 255}
	}

	// 1. 展开全部图元（INSERT 递归变换）到世界坐标描边列表
	prim := drawing.NewTessellator(doc)
	prims := prim.ExpandAll()
	prims = filterRadiatingStrokes(prims)

	// 2. 鲁棒包围盒（中位数±分位数，抗错位垃圾坐标干扰）
	bbox := drawing.RobustBounds(prims)
	if bbox.Invalid() {
		bbox = drawing.Box2{MinX: 0, MinY: 0, MaxX: 1, MaxY: 1}
	}
	// 剔除「原点锚定的超长线」：错位解码的 LINE 常一端落在原点附近、
	// 另一端指向真实图形位置，形成放射状噪声
	prims = dropOriginAnchored(prims, bbox)
	width := float64(opts.Width)
	marginX := (bbox.MaxX - bbox.MinX) * 0.05
	marginY := (bbox.MaxY - bbox.MinY) * 0.05
	if marginX == 0 && marginY == 0 {
		marginX, marginY = 1, 1 // 退化（单点/空图）兜底
	} else if marginX == 0 {
		marginX = marginY
	} else if marginY == 0 {
		marginY = marginX
	}
	bbox.MinX -= marginX
	bbox.MaxX += marginX
	bbox.MinY -= marginY
	bbox.MaxY += marginY
	scale := width / (bbox.MaxX - bbox.MinX)
	height := int((bbox.MaxY-bbox.MinY)*scale + 0.5)
	if height < 1 {
		height = 1
	}

	// 3. 光栅化
	img := image.NewRGBA(image.Rect(0, 0, int(width), height))
	drawRect(img, img.Rect, opts.Background)
	cv := &canvas{img: img, width: float64(width), height: float64(height), lineWidth: adaptiveLineWidth(width), fontPath: opts.FontPath}
	cv.setTransform(bbox, scale)

	bgWhite := opts.Background.R > 127
	for _, p := range prims {
		cv.drawPrimitive(p, doc, bgWhite)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---- 图元展开（tessellation） ----

// canvas 像素画布与世界→像素变换。
type canvas struct {
	img           *image.RGBA
	width, height float64
	scale         float64
	minX, maxY    float64
	lineWidth     float64       // 默认线宽（像素，随渲染宽度自适应，见 adaptiveLineWidth）
	fontPath      string        // 字形渲染字体路径（空=自动探测系统字体）
	tr            *textRenderer // 文本渲染器（首次画文本懒初始化，单协程使用）
}

// adaptiveLineWidth 默认线宽随渲染宽度自适应：2048 宽保持历史口径 0.75px
// 笔刷半径，更大宽度按比例放大（4096 → 1.5px、8192 → 3px），避免高分辨率
// 下线宽视觉过细；实体 linewt 字段暂未参与线宽计算（维持现状）。
func adaptiveLineWidth(width float64) float64 {
	return math.Max(0.75, 0.75*width/2048)
}

// setTransform 设置世界→像素变换（Y 轴翻转）。
func (c *canvas) setTransform(bbox drawing.Box2, scale float64) {
	c.scale = scale
	c.minX = bbox.MinX
	c.maxY = bbox.MaxY
}

func (c *canvas) toPixel(p entity.Point2) (float64, float64) {
	return (p.X - c.minX) * c.scale, (c.maxY - p.Y) * c.scale
}

// entityColor 解析实体最终颜色（true color > 实体 ACI > 图层 > 默认黑）。
func entityColor(doc *drawing.Document, e *drawing.Primitive, bgWhite bool) color.RGBA {
	if e.Color.HasTrue {
		r, g, b := drawing.SplitTrueColor(e.Color.TrueColor)
		return color.RGBA{r, g, b, 255}
	}
	if e.Color.HasIndex && e.Color.Index != 0 && e.Color.Index != 256 && e.Color.Index != 257 {
		if r, g, b, ok := drawing.AciColor(e.Color.Index, bgWhite); ok {
			return color.RGBA{r, g, b, 255}
		}
	}
	if lc, ok := doc.LayerColors[e.Layer]; ok {
		if c, ok := LayerRenderColor(lc, bgWhite); ok {
			return c
		}
	}
	// 默认（ByLayer 无图层信息 / ACI 7）：白底黑
	if bgWhite {
		return color.RGBA{0, 0, 0, 255}
	}
	return color.RGBA{255, 255, 255, 255}
}

// layerRenderColor 图层记录的渲染取色（与实体色 32 位保留口径配套）：
// 图层 CMC 的 32 位 rgb 值低 24 位 ≤0xFF 时为 ACI 索引形（方法字节
// 0xC3=ByLayer/0xC1=ByACI 变体，R2010+ 图层记录的常态——LibreDWG gold
// 中该形占 260/261，如 0xC3000007 即 ACI 7），低 24 位 >0xFF 时才是 RGB
// 真彩形（0xC2/0xC0，如 0xC2FFFFFF 白）。索引形不做真彩取色，否则
// ACI 1~8 的彩色图层（红/黄/绿/青/品红等）被画成 RGB(0,0,n) 深蓝近黑，
// 消防线型图例表等 ByLayer 彩色内容整体失色。无有效色返回 false。
func LayerRenderColor(lc drawing.LayerColor, bgWhite bool) (color.RGBA, bool) {
	if lc.HasTrue {
		if low := lc.TrueColor & 0x00FFFFFF; low <= 0xFF {
			if r, g, b, ok := drawing.AciColor(uint16(low), bgWhite); ok {
				return color.RGBA{r, g, b, 255}, true
			}
		} else {
			r, g, b := drawing.SplitTrueColor(lc.TrueColor)
			return color.RGBA{r, g, b, 255}, true
		}
	}
	if lc.Index != 0 && lc.Index != 256 {
		if r, g, b, ok := drawing.AciColor(lc.Index, bgWhite); ok {
			return color.RGBA{r, g, b, 255}, true
		}
	}
	return color.RGBA{}, false
}

// drawPrimitive 绘制一个展开后的图元。
func (c *canvas) drawPrimitive(p drawing.Primitive, doc *drawing.Document, bgWhite bool) {
	col := entityColor(doc, &p, bgWhite)
	switch p.Kind {
	case 0:
		for _, s := range p.Strokes {
			if !drawing.Plausible(s.X1, s.Y1) || !drawing.Plausible(s.X2, s.Y2) {
				continue
			}
			c.drawLine(s, col)
		}
	case 1:
		if drawing.Plausible(p.Lb.X, p.Lb.Y) {
			if p.Lb.Tx != nil {
				c.drawLabelText(p.Lb, p.Lb.Tx, col)
			} else {
				c.drawLabel(p.Lb, col)
			}
		}
	}
}

// drawLine 世界坐标线段绘制。
func (c *canvas) drawLine(s drawing.Stroke, col color.RGBA) {
	x1, y1 := c.toPixel(entity.Point2{X: s.X1, Y: s.Y1})
	x2, y2 := c.toPixel(entity.Point2{X: s.X2, Y: s.Y2})
	c.drawPixelLine(x1, y1, x2, y2, col)
}

// drawPixelLine 像素坐标线段绘制（先按画布裁剪，再步进采样 + 圆盘笔刷）。
func (c *canvas) drawPixelLine(x1, y1, x2, y2 float64, col color.RGBA) {
	brush := c.lineWidth // 笔刷半径（像素，随渲染宽度自适应）
	if brush <= 0 {
		brush = 0.75 // 兜底（测试直接构造 canvas 未设线宽时保持历史口径）
	}
	w := float64(c.img.Bounds().Dx())
	h := float64(c.img.Bounds().Dy())
	// 圆盘笔刷判定：Hypot(d)≤brush ⟺ dX²+dY²≤brush²（brush>0），平方比较
	// 免每像素开方（笔刷判定是逐像素热循环，pprof archHypot ~5%）
	limit2 := brush * brush
	bDx, bDy := c.img.Bounds().Dx(), c.img.Bounds().Dy()
	// 参数化裁剪到画布外扩 1 像素的范围，离群长线段不逐像素推进
	t0, t1 := 0.0, 1.0
	dx, dy := x2-x1, y2-y1
	clip := func(p, q float64) bool {
		if p == 0 {
			return q >= 0
		}
		t := q / p
		if p < 0 {
			if t > t1 {
				return false
			}
			if t > t0 {
				t0 = t
			}
		} else {
			if t < t0 {
				return false
			}
			if t < t1 {
				t1 = t
			}
		}
		return true
	}
	if !clip(-dx, x1+1) || !clip(dx, w-x1+1) || !clip(-dy, y1+1) || !clip(dy, h-y1+1) {
		return // 完全在画布外
	}
	sx, sy := x1+dx*t0, y1+dy*t0
	ex, ey := x1+dx*t1, y1+dy*t1
	length := math.Hypot(ex-sx, ey-sy)
	steps := int(length*2) + 1
	// 步进上限随画布放大：8192 宽对角线约 1.6 万像素，旧固定 8192 上限
	// 会在超长线段上产生采样断点（虚线状）
	maxSteps := 4 * (bDx + bDy)
	if maxSteps < 8192 {
		maxSteps = 8192
	}
	if steps > maxSteps {
		steps = maxSteps
	}
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		cx, cy := sx+(ex-sx)*t, sy+(ey-sy)*t
		x0, xEnd := int(cx-brush), int(cx+brush)
		y0, yEnd := int(cy-brush), int(cy+brush)
		for py := y0; py <= yEnd; py++ {
			if py < 0 || py >= bDy {
				continue
			}
			dyPix := float64(py) + 0.5 - cy
			dy2 := dyPix * dyPix
			for px := x0; px <= xEnd; px++ {
				if px < 0 || px >= bDx {
					continue
				}
				dxPix := float64(px) + 0.5 - cx
				if dxPix*dxPix+dy2 <= limit2 {
					c.img.SetRGBA(px, py, col)
				}
			}
		}
	}
}

// drawLabel 文字占位条：基线位置画高度一半的条形，宽度按估算字宽。
func (c *canvas) drawLabel(lb drawing.Label, col color.RGBA) {
	half := lb.H / 2
	cos, sin := math.Cos(lb.Rot), math.Sin(lb.Rot)
	// 矩形四角（基线左端为原点，向上为半高）——先算世界坐标，再一次转换到像素
	corners := [4][2]float64{
		{0, 0}, {lb.W, 0}, {lb.W, half}, {0, half},
	}
	pix := make([][2]float64, 4)
	for i, c0 := range corners {
		wx := lb.X + c0[0]*cos - c0[1]*sin
		wy := lb.Y + c0[0]*sin + c0[1]*cos
		pix[i][0], pix[i][1] = c.toPixel(entity.Point2{X: wx, Y: wy})
	}
	// 四条边（像素坐标直绘）
	edges := [4][2]int{{0, 1}, {1, 2}, {2, 3}, {3, 0}}
	for _, e := range edges {
		c.drawPixelLine(pix[e[0]][0], pix[e[0]][1], pix[e[1]][0], pix[e[1]][1], col)
	}
}

// drawRect 填充矩形区域：按行 copy 预填字节行（背景整幅填充在大画布上
// 可达数百万像素，逐像素 SetRGBA 是渲染热点之一；Pix 布局 RGBA 与
// SetRGBA 逐字节一致）。
func drawRect(img *image.RGBA, r image.Rectangle, col color.RGBA) {
	w := r.Dx()
	row := make([]byte, w*4)
	for x := 0; x < w; x++ {
		row[4*x], row[4*x+1], row[4*x+2], row[4*x+3] = col.R, col.G, col.B, col.A
	}
	off := img.PixOffset(r.Min.X, r.Min.Y)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		copy(img.Pix[off:off+w*4], row)
		off += img.Stride
	}
}

// dropOversizeStrokes 剔除长度超过视口对角线 1.5 倍的线段图元。
// 错位解码的 LINE 常表现为「一端在原点/锚点、另一端在远处」的超长线。
func DropOversizeStrokes(prims []drawing.Primitive, bbox drawing.Box2) []drawing.Primitive {
	diag := math.Hypot(bbox.MaxX-bbox.MinX, bbox.MaxY-bbox.MinY)
	limit := diag * 1.5
	out := make([]drawing.Primitive, 0, len(prims))
	for _, p := range prims {
		if p.Kind != 0 {
			out = append(out, p)
			continue
		}
		kept := p.Strokes[:0:0]
		for _, s := range p.Strokes {
			if math.Hypot(s.X2-s.X1, s.Y2-s.Y1) > limit {
				continue
			}
			kept = append(kept, s)
		}
		if len(kept) > 0 {
			p.Strokes = kept
			out = append(out, p)
		}
	}
	return out
}

// dropOriginAnchored 剔除一端在原点附近（视口 4% 内）、另一端超出视口 1.5 倍的线段。
func dropOriginAnchored(prims []drawing.Primitive, bbox drawing.Box2) []drawing.Primitive {
	cx := (bbox.MinX + bbox.MaxX) / 2
	cy := (bbox.MinY + bbox.MaxY) / 2
	halfW := (bbox.MaxX - bbox.MinX) / 2
	halfH := (bbox.MaxY - bbox.MinY) / 2
	nearR := math.Hypot(halfW, halfH) * 0.04
	farR := math.Hypot(halfW, halfH) * 1.5
	out := make([]drawing.Primitive, 0, len(prims))
	for _, p := range prims {
		if p.Kind != 0 {
			out = append(out, p)
			continue
		}
		// 原地过滤：写入位置恒不超前读取位置，安全复用底层数组
		// （p.strokes 旧数组无其他引用，避免数十万段级别的重复分配）
		kept := p.Strokes[:0]
		for _, s := range p.Strokes {
			d1 := math.Hypot(s.X1-cx, s.Y1-cy)
			d2 := math.Hypot(s.X2-cx, s.Y2-cy)
			if (d1 < nearR && d2 > farR) || (d2 < nearR && d1 > farR) {
				continue
			}
			kept = append(kept, s)
		}
		if len(kept) > 0 {
			p.Strokes = kept
			out = append(out, p)
		}
	}
	return out
}

// filterRadiatingStrokes 过滤「放射状」错位线段：
// 错位解码的 LINE 常共享同一错误锚点（位级相同坐标），形成大量共点放射线；
// 统计端点重合计数，超过阈值的线段视为错位产物剔除。
// 量化粒度取 1e-6 世界单位（近精确重合）：错位锚点是同一错误位模式解码
// 结果，精确重合成立；真实密集图元（圆弧离散、DONUT 环带）端点沿轮廓连续
// 分布，相邻段端点仅各重合 2 次，粗粒度量化（如 4 单位）会把它们压进同一
// 格子误判为放射线，导致 DONUT 等小尺寸图纸整图被清空（批次 T 语料实证）。
func filterRadiatingStrokes(prims []drawing.Primitive) []drawing.Primitive {
	type key struct{ X, y int64 }
	const q = 1e-6
	const radiatingThreshold = 12
	// 重合计数 map 按端点数上界预分配：数十万端点下逐次扩容 rehash
	// 曾占渲染分配大头（pprof ~22%）
	total := 0
	for _, p := range prims {
		if p.Kind == 0 {
			total += len(p.Strokes)
		}
	}
	count := make(map[key]int, total*2)
	for _, p := range prims {
		for _, s := range p.Strokes {
			count[key{int64(s.X1 / q), int64(s.Y1 / q)}]++
			count[key{int64(s.X2 / q), int64(s.Y2 / q)}]++
		}
	}
	out := make([]drawing.Primitive, 0, len(prims))
	for _, p := range prims {
		if p.Kind != 0 {
			out = append(out, p)
			continue
		}
		// 原地过滤（同 dropOriginAnchored）：安全复用底层数组免重复分配
		kept := p.Strokes[:0]
		for _, s := range p.Strokes {
			if count[key{int64(s.X1 / q), int64(s.Y1 / q)}] > radiatingThreshold ||
				count[key{int64(s.X2 / q), int64(s.Y2 / q)}] > radiatingThreshold {
				continue
			}
			kept = append(kept, s)
		}
		if len(kept) > 0 {
			p.Strokes = kept
			out = append(out, p)
		}
	}
	return out
}
