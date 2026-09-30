// render.go 实现纯 Go 的模型空间光栅化渲染：
// INSERT 递归展开（仿射变换）→ 包围盒自适应视口 → 图元离散为线段 → 厚线段绘制。
// 文本经 textLabel 估算占位几何并携带 textInfo 版式，由 render_text.go 以
// 系统字体真实字形绘制（旋转/镜像/对齐/换行），无可用字体时回退基线占位条。
package cad

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"sort"
	"strings"
)

// RenderOptions 渲染参数。
type RenderOptions struct {
	Width      int        // 输出宽度像素，默认 2048；高度按包围盒比例确定
	Background color.RGBA // 背景色，默认白色
	FontPath   string     // 字形渲染字体文件路径（空=自动探测系统字体；无可用字体回退占位条）
}

// RenderPNG 将文档模型空间渲染为 PNG 字节流。
func RenderPNG(doc *Document, opts RenderOptions) ([]byte, error) {
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
	prim := newTessellator(doc)
	prims := prim.expandAll()
	prims = filterRadiatingStrokes(prims)

	// 2. 鲁棒包围盒（中位数±分位数，抗错位垃圾坐标干扰）
	bbox := robustBounds(prims)
	if bbox.invalid() {
		bbox = box2{0, 0, 1, 1}
	}
	// 剔除「原点锚定的超长线」：错位解码的 LINE 常一端落在原点附近、
	// 另一端指向真实图形位置，形成放射状噪声
	prims = dropOriginAnchored(prims, bbox)
	width := float64(opts.Width)
	marginX := (bbox.maxX - bbox.minX) * 0.05
	marginY := (bbox.maxY - bbox.minY) * 0.05
	if marginX == 0 && marginY == 0 {
		marginX, marginY = 1, 1 // 退化（单点/空图）兜底
	} else if marginX == 0 {
		marginX = marginY
	} else if marginY == 0 {
		marginY = marginX
	}
	bbox.minX -= marginX
	bbox.maxX += marginX
	bbox.minY -= marginY
	bbox.maxY += marginY
	scale := width / (bbox.maxX - bbox.minX)
	height := int((bbox.maxY-bbox.minY)*scale + 0.5)
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

// stroke 一条世界坐标描边段。
type stroke struct{ x1, y1, x2, y2 float64 }

// label 文字占位条。
type label struct {
	x, y, w, h, rot float64   // 基线起点、宽高（世界单位）、旋转弧度
	tx              *textInfo // 真实字形版式信息（nil=纯占位条，仅几何回退路径产生）
	text            string    // 剥离格式码后的文本内容（RenderSVG 的 <text> 元素用；PNG 字形路径经 tx.lines 取文本）
	anchor          uint8     // 水平锚点：0=start 1=middle 2=end（SVG text-anchor；PNG 对齐经 tx.hAlign/attachment）
	handle          uint64    // 源实体句柄（RenderSVG 的 <text data-h> 元数据用；占位条回退路径为 0）
}

// textInfo 真实字形渲染所需的文本版式信息（render_text.go 消费）。
// 占位几何估算与占位条回退只依赖 label 的既有字段，本结构仅字形路径读取。
type textInfo struct {
	lines       []string // 已剥离格式指令的文本行
	hWorld      float64  // 字高（世界单位）
	ux, uy      float64  // 世界空间推进方向基向量（含实体变换/镜像，模长含变换比例）
	vx, vy      float64  // 世界空间字面向上方向基向量
	gen         uint16   // TEXT/ATTRIB 生成标志（0x2 X 镜像 / 0x4 Y 镜像）
	hAlign      uint16   // 水平对齐（DXF 0 左/1 中/2 右/3,4,5 对齐变体）
	vAlign      uint16   // 垂直对齐（0 基线/1 下/2 中/3 上）
	attachment  uint16   // MTEXT 附着点 1-9（0=单行文本语义）
	rectWidth   float64  // MTEXT 列宽（世界单位，0=不换行）
	lineFactor  float64  // MTEXT 行距系数（DXF 44 linespace_factor；≤0=未存储，绘制侧按缺省行距兜底）
	widthFactor float64  // 宽度因子（TEXT/ATTRIB 解码 width_factor 接入；MTEXT 恒 1）
	oblique     float64  // 倾斜角弧度（默认 0；解码侧暂未接入该字段，预留参数）
}

// primitive 展开后的可绘制单元。
type primitive struct {
	kind    int // 0 stroke, 1 label
	strokes []stroke
	lb      label
	color   entColor
	layer   uint64
	kind0   string // 类型名（调试/渲染策略）
}

// tessellator INSERT 展开与图元离散。
// 展开防爆双保险：insPath 环检测（真实 DWG 块引用为有向无环图，路径上
// 重复出现同一块定义即自引用/互引用环，短路展开）+ budget 全局预算
// （防无环但分支组合巨大的异常文件，量级按真实大图完整展开需求放大，
// 如 9 张 A1 图框 × 大样图块的用户案例全图展开消耗约 38 万实例）。
type tessellator struct {
	doc      *Document
	maxDepth int
	budget   int
	insPath  []uint64 // 当前 INSERT 展开路径上的块定义句柄（环检测）
	vertex2d map[uint64]*entVertex2d
	vertex3d map[uint64]*entVertex3d
}

func newTessellator(doc *Document) *tessellator {
	return &tessellator{doc: doc, maxDepth: 12, budget: 2000000}
}

// xform 仿射变换（世界坐标）。
type xform struct {
	sx, sy   float64 // 缩放
	cos, sin float64 // 旋转
	tx, ty   float64 // 平移
}

func identityXform() xform { return xform{sx: 1, sy: 1, cos: 1} }

func (t xform) apply(p point2) point2 {
	return point2{
		x: p.x*t.sx*t.cos - p.y*t.sy*t.sin + t.tx,
		y: p.x*t.sx*t.sin + p.y*t.sy*t.cos + t.ty,
	}
}

func (t xform) lengthScale() float64 {
	return math.Abs(t.sx)
}

func (t xform) compose(child xform) xform {
	// 结果 = t ∘ child：先应用 child（缩放→旋转→平移），再应用 t
	cos := t.cos*child.cos - t.sin*child.sin
	sin := t.sin*child.cos + t.cos*child.sin
	return xform{
		sx: t.sx * child.sx, sy: t.sy * child.sy,
		cos: cos, sin: sin,
		tx: t.tx + t.cos*t.sx*child.tx - t.sin*t.sy*child.ty,
		ty: t.ty + t.sin*t.sx*child.tx + t.cos*t.sy*child.ty,
	}
}

// insertXform 构造 INSERT 的变换：缩放→旋转→平移插入点。
func insertXform(e *entInsert) xform {
	return xform{
		sx: e.scale.x, sy: e.scale.y,
		cos: math.Cos(e.rotation), sin: math.Sin(e.rotation),
		tx: e.position.x, ty: e.position.y,
	}
}

// buildVertexIndex 构建句柄 → 顶点实体的索引（POLYLINE 聚合用）。
// 先遍历计数顶点实体再按计数预分配 map：大图纸顶点数以万计，逐次
// 扩容 rehash 是渲染侧分配热点之一。
func (ts *tessellator) buildVertexIndex() {
	count2d, count3d := 0, 0
	tally := func(list []any) {
		for _, e := range list {
			switch e.(type) {
			case *entVertex2d:
				count2d++
			case *entVertex3d:
				count3d++
			}
		}
	}
	tally(ts.doc.modelSpace)
	tally(ts.doc.pspaceSpace)
	for _, list := range ts.doc.blocks {
		tally(list)
	}
	ts.vertex2d = make(map[uint64]*entVertex2d, count2d)
	ts.vertex3d = make(map[uint64]*entVertex3d, count3d)
	add := func(list []any) {
		for _, e := range list {
			switch t := e.(type) {
			case *entVertex2d:
				ts.vertex2d[t.handle] = t
			case *entVertex3d:
				ts.vertex3d[t.handle] = t
			}
		}
	}
	add(ts.doc.modelSpace)
	add(ts.doc.pspaceSpace)
	for _, list := range ts.doc.blocks {
		add(list)
	}
}

// expandAll 展开模型空间全部图元。
// pre-R13 图纸空间实体（pspaceSpace，R13+ 无此列表）一并展开：
// 图纸空间布局文件（如 ACEB10）的主内容位于该列表，跳过会渲染空白。
func (ts *tessellator) expandAll() []primitive {
	ts.buildVertexIndex()
	// 展开结果数下界为直属实体数（INSERT 递归展开再按需增长），
	// 按下界预分配免前几次翻倍扩容
	out := make([]primitive, 0, len(ts.doc.modelSpace)+len(ts.doc.pspaceSpace))
	for _, ent := range ts.doc.modelSpace {
		out = ts.appendEntity(out, ent, identityXform(), 0)
	}
	for _, ent := range ts.doc.pspaceSpace {
		out = ts.appendEntity(out, ent, identityXform(), 0)
	}
	return out
}

// appendEntity 展开单个图元（INSERT 递归），应用当前变换。
func (ts *tessellator) appendEntity(out []primitive, ent any, t xform, depth int) []primitive {
	if depth > ts.maxDepth || ts.budget <= 0 {
		return out
	}
	ts.budget--
	switch e := ent.(type) {
	case *entLine:
		a, b := t.apply(point2{e.start.x, e.start.y}), t.apply(point2{e.end.x, e.end.y})
		out = append(out, primitive{kind: 0, strokes: []stroke{{a.x, a.y, b.x, b.y}}, color: e.color, layer: e.layer, kind0: "LINE"})
	case *entCircle:
		out = append(out, primitive{kind: 0, strokes: tessCircle(e.center.x, e.center.y, e.radius, t), color: e.color, layer: e.layer, kind0: "CIRCLE"})
	case *entArc:
		out = append(out, primitive{kind: 0, strokes: tessArc(e.center.x, e.center.y, e.radius, e.angleStart, e.angleEnd, t), color: e.color, layer: e.layer, kind0: "ARC"})
	case *entPoint:
		p := t.apply(point2{e.location.x, e.location.y})
		r := 0.02 * t.lengthScale()
		if r == 0 {
			r = 0.1
		}
		out = append(out, primitive{kind: 0, strokes: []stroke{
			{p.x - r, p.y, p.x + r, p.y}, {p.x, p.y - r, p.x, p.y + r},
		}, color: e.color, layer: e.layer, kind0: "POINT"})
	case *entEllipse:
		out = append(out, primitive{kind: 0, strokes: tessEllipse(e, t), color: e.color, layer: e.layer, kind0: "ELLIPSE"})
	case *entLwPolyline:
		out = append(out, primitive{kind: 0, strokes: tessLwPolyline(e, t), color: e.color, layer: e.layer, kind0: "LWPOLYLINE"})
	case *entText:
		// 非默认对齐（h/vAlign 任一非零）时锚点取 alignment_pt（DXF 语义）
		x, y := e.insertion.x, e.insertion.y
		if (e.hAlign != 0 || e.vAlign != 0) && e.alignPt != nil {
			x, y = e.alignPt.x, e.alignPt.y
		}
		out = append(out, ts.textLabelWith(x, y, e.height, e.rotation, len([]rune(e.text)), t, e, "TEXT", textInfo{
			lines: []string{e.text}, gen: e.gen, hAlign: e.hAlign, vAlign: e.vAlign,
			widthFactor: textWidthFactor(e.widthFactor),
		}))
	case *entMText:
		rot := math.Atan2(e.xAxisDir.y, e.xAxisDir.x)
		lines := strings.Split(stripMTextFormat(e.text), "\n")
		n := 0
		for _, ln := range lines {
			n += len([]rune(ln))
		}
		out = append(out, ts.textLabelWith(e.insertion.x, e.insertion.y, e.textHeight, rot, n, t, e, "MTEXT", textInfo{
			lines: lines, attachment: e.attachment, rectWidth: e.rectWidth,
			lineFactor: e.lineFactor,
		}))
	case *entAttrib:
		x, y := e.insertion.x, e.insertion.y
		if (e.hAlign != 0 || e.vAlign != 0) && e.alignPt != nil {
			x, y = e.alignPt.x, e.alignPt.y
		}
		out = append(out, ts.textLabelWith(x, y, e.height, e.rotation, len([]rune(e.text)), t, e, "ATTRIB", textInfo{
			lines: []string{e.text}, gen: e.gen, hAlign: e.hAlign, vAlign: e.vAlign,
			widthFactor: textWidthFactor(e.widthFactor),
		}))
	case *entSpline:
		out = append(out, primitive{kind: 0, strokes: tessSpline(e, t), color: e.color, layer: e.layer, kind0: "SPLINE"})
	case *entHelix:
		out = append(out, primitive{kind: 0, strokes: tessHelix(e, t), color: e.color, layer: e.layer, kind0: "HELIX"})
	case *entUnderlay:
		out = append(out, primitive{kind: 0, strokes: tessUnderlay(e, t), color: e.color, layer: e.layer, kind0: "UNDERLAY"})
	case *entHatch:
		for _, p := range e.paths {
			var pts []point2
			for _, v := range p.points {
				pts = append(pts, t.apply(v))
			}
			var strokes []stroke
			for i := 0; i+1 < len(pts); i++ {
				strokes = append(strokes, stroke{pts[i].x, pts[i].y, pts[i+1].x, pts[i+1].y})
			}
			out = append(out, primitive{kind: 0, strokes: strokes, color: e.color, layer: e.layer, kind0: "HATCH"})
		}
	case *entDimension:
		// 标注：连接测量点与文字中点（简化可视表达）
		var pts []point2
		for _, p := range []point3{e.point10, e.point13, e.point14} {
			pts = append(pts, t.apply(point2{p.x, p.y}))
		}
		mid := t.apply(point2{e.textMidpoint.x, e.textMidpoint.y})
		var strokes []stroke
		for i := 0; i+1 < len(pts); i++ {
			strokes = append(strokes, stroke{pts[i].x, pts[i].y, pts[i+1].x, pts[i+1].y})
		}
		if len(pts) > 0 {
			strokes = append(strokes, stroke{pts[len(pts)-1].x, pts[len(pts)-1].y, mid.x, mid.y})
		}
		out = append(out, primitive{kind: 0, strokes: strokes, color: e.color, layer: e.layer, kind0: "DIMENSION"})
	case *entRay:
		a := t.apply(point2{e.start.x, e.start.y})
		end := point2{
			e.start.x + e.unitVector.x*1e6,
			e.start.y + e.unitVector.y*1e6,
		}
		b := t.apply(end)
		strokes := []stroke{{a.x, a.y, b.x, b.y}}
		if e.xline {
			c := t.apply(point2{
				e.start.x - e.unitVector.x*1e6,
				e.start.y - e.unitVector.y*1e6,
			})
			strokes = append(strokes, stroke{a.x, a.y, c.x, c.y})
		}
		kind0 := "RAY"
		if e.xline {
			kind0 = "XLINE"
		}
		out = append(out, primitive{kind: 0, strokes: strokes, color: e.color, layer: e.layer, kind0: kind0})
	case *entSolid:
		pts := []point2{t.apply(e.p1), t.apply(e.p2), t.apply(e.p3), t.apply(e.p4)}
		var strokes []stroke
		for i := 0; i+1 < len(pts); i++ {
			strokes = append(strokes, stroke{pts[i].x, pts[i].y, pts[i+1].x, pts[i+1].y})
		}
		strokes = append(strokes, stroke{pts[3].x, pts[3].y, pts[0].x, pts[0].y})
		kind0 := "SOLID"
		if e.trace {
			kind0 = "TRACE"
		}
		out = append(out, primitive{kind: 0, strokes: strokes, color: e.color, layer: e.layer, kind0: kind0})
	case *entFace3d:
		pts := []point2{t.apply(point2{e.p1.x, e.p1.y}), t.apply(point2{e.p2.x, e.p2.y}),
			t.apply(point2{e.p3.x, e.p3.y}), t.apply(point2{e.p4.x, e.p4.y})}
		var strokes []stroke
		for i := 0; i+1 < len(pts); i++ {
			strokes = append(strokes, stroke{pts[i].x, pts[i].y, pts[i+1].x, pts[i+1].y})
		}
		strokes = append(strokes, stroke{pts[3].x, pts[3].y, pts[0].x, pts[0].y})
		out = append(out, primitive{kind: 0, strokes: strokes, color: e.color, layer: e.layer, kind0: "3DFACE"})
	case *entLeader:
		if len(e.points) >= 2 {
			var strokes []stroke
			prev := t.apply(point2{e.points[0].x, e.points[0].y})
			for _, p := range e.points[1:] {
				cur := t.apply(point2{p.x, p.y})
				strokes = append(strokes, stroke{prev.x, prev.y, cur.x, cur.y})
				prev = cur
			}
			out = append(out, primitive{kind: 0, strokes: strokes, color: e.color, layer: e.layer, kind0: "LEADER"})
		}
	case *entMLine:
		if len(e.vertices) >= 2 {
			var strokes []stroke
			prev := t.apply(point2{e.vertices[0].position.x, e.vertices[0].position.y})
			for _, v := range e.vertices[1:] {
				cur := t.apply(point2{v.position.x, v.position.y})
				strokes = append(strokes, stroke{prev.x, prev.y, cur.x, cur.y})
				prev = cur
			}
			out = append(out, primitive{kind: 0, strokes: strokes, color: e.color, layer: e.layer, kind0: "MLINE"})
		}
	case *entPolyline2d:
		var strokes []stroke
		var pts []point2
		for _, vh := range e.ownedHandles {
			if v, ok := ts.vertex2d[vh]; ok {
				pts = append(pts, t.apply(point2{v.position.x, v.position.y}))
			}
		}
		for i := 0; i+1 < len(pts); i++ {
			strokes = append(strokes, stroke{pts[i].x, pts[i].y, pts[i+1].x, pts[i+1].y})
		}
		out = append(out, primitive{kind: 0, strokes: strokes, color: e.color, layer: e.layer, kind0: "POLYLINE_2D"})
	case *entPolyline3d:
		var strokes []stroke
		var pts []point2
		for _, vh := range e.ownedHandles {
			if v, ok := ts.vertex3d[vh]; ok {
				pts = append(pts, t.apply(point2{v.position.x, v.position.y}))
			}
		}
		for i := 0; i+1 < len(pts); i++ {
			strokes = append(strokes, stroke{pts[i].x, pts[i].y, pts[i+1].x, pts[i+1].y})
		}
		out = append(out, primitive{kind: 0, strokes: strokes, color: e.color, layer: e.layer, kind0: "POLYLINE_3D"})
	case *entInsert:
		// 环检测：展开路径上已出现同一块定义即自引用/互引用环，短路该
		// 分支（环分支不消耗预算，预算只对无环的巨量展开兜底）
		for _, h := range ts.insPath {
			if h == e.blockHeader {
				return out
			}
		}
		ts.insPath = append(ts.insPath, e.blockHeader)
		child := t.compose(insertXform(e))
		for _, inner := range ts.doc.blocks[e.blockHeader] {
			out = ts.appendEntity(out, inner, child, depth+1)
		}
		// 关联属性（文字位置在插入变换下的对应位置）
		for _, ah := range e.attribs {
			if a, ok := ts.doc.attribs[ah]; ok {
				out = ts.appendEntity(out, a, child, depth+1)
			}
		}
		ts.insPath = ts.insPath[:len(ts.insPath)-1]
	}
	return out
}

// textLabel 文字占位条：按字符数与字高估算占位宽度。
// text 携带剥离格式码后的文本内容、anchor 携带水平锚点（RenderSVG 矢量
// 输出用；PNG 路径仅消费几何字段，文本为空时 SVG 退化为占位框）。
func (ts *tessellator) textLabel(x, y, h, rot float64, nChars int, t xform, e entityCommon, kind, text string, anchor uint8) primitive {
	if nChars <= 0 {
		nChars = 1
	}
	// 世界宽度 ≈ 字符数 × 字高 × 0.8（中文按全角 1.0/字符的上界收缩）
	w := float64(nChars) * h * 0.8
	if w > h*80 {
		w = h * 80
	}
	p1 := t.apply(point2{x, y})
	p2 := t.apply(point2{x + w*math.Cos(rot), y + w*math.Sin(rot)})
	scaledH := h * t.lengthScale()
	return primitive{
		kind:  1,
		lb:    label{x: p1.x, y: p1.y, w: p2.x - p1.x, h: scaledH, rot: math.Atan2(p2.y-p1.y, p2.x-p1.x), text: text, anchor: anchor},
		color: e.common().color, layer: e.common().layer, kind0: kind,
	}
}

// textWidthFactor 宽度因子取值：解码未读（0）按 DXF 默认 1；异常放大
// 钳到上限 10 防 0 宽字形粘连或无限宽（下限不需钳制：AutoCAD 合法域
// 0.01~100，工程实用 ≥0.1）。
func textWidthFactor(wf float64) float64 {
	if wf <= 0 {
		return 1
	}
	if wf > 10 {
		return 10
	}
	return wf
}

// textLabelWith 在 textLabel 占位几何基础上附真实字形版式信息与 SVG 文本
// 内容。推进/字面向上基向量按实体变换差分获得（世界单位，INSERT 负缩放
// 镜像下方向精确），render_text.go 据此换算像素版式绘制真实字形；text/
// anchor 供 RenderSVG 的 <text> 元素消费；handle 附源实体句柄
// （<text data-h> AI 元数据）。
func (ts *tessellator) textLabelWith(x, y, h, rot float64, nChars int, t xform, e entityCommon, kind string, tx textInfo) primitive {
	p := ts.textLabel(x, y, h, rot, nChars, t, e, kind, "", 0)
	p.lb.handle = e.common().handle
	c, s := math.Cos(rot), math.Sin(rot)
	o := t.apply(point2{x, y})
	u := t.apply(point2{x + c, y + s})
	v := t.apply(point2{x - s, y + c})
	tx.ux, tx.uy = u.x-o.x, u.y-o.y
	tx.vx, tx.vy = v.x-o.x, v.y-o.y
	tx.hWorld = h
	if tx.widthFactor > 0 {
		// 占位条宽度同步宽度因子（TEXT/ATTRIB 压缩字宽；MTEXT 恒 1）
		p.lb.w *= tx.widthFactor
	} else {
		tx.widthFactor = 1
	}
	tx.oblique = 0
	p.lb.tx = &tx
	p.lb.text = strings.Join(tx.lines, "\n")
	if tx.attachment != 0 {
		p.lb.anchor = textAnchorAttachment(tx.attachment)
	} else {
		p.lb.anchor = textAnchorHAlign(tx.hAlign)
	}
	return p
}

// textAnchorHAlign TEXT/ATTRIB 水平对齐码 → text-anchor 锚点：
// 1=Center / 4=Middle 映射 middle，2=Right 映射 end，
// 其余（含 Aligned/Fit 两点对齐）近似 start。
func textAnchorHAlign(h uint16) uint8 {
	switch h {
	case 1, 4:
		return 1
	case 2:
		return 2
	}
	return 0
}

// textAnchorAttachment MTEXT 附着点 1~9（1=TL…9=BR）按列映射 text-anchor：
// 左列 start、中列 middle、右列 end，越界退回 start。
func textAnchorAttachment(att uint16) uint8 {
	if att < 1 || att > 9 {
		return 0
	}
	switch (att - 1) % 3 {
	case 1:
		return 1
	case 2:
		return 2
	}
	return 0
}

// tessCircle 圆离散为 72 段。
func tessCircle(cx, cy, r float64, t xform) []stroke {
	const n = 72
	out := make([]stroke, 0, n)
	prev := t.apply(point2{cx + r, cy})
	for i := 1; i <= n; i++ {
		a := float64(i) / n * 2 * math.Pi
		cur := t.apply(point2{cx + r*math.Cos(a), cy + r*math.Sin(a)})
		out = append(out, stroke{prev.x, prev.y, cur.x, cur.y})
		prev = cur
	}
	return out
}

// tessArc 圆弧按 3°/段离散（跨零角处理）。span 用取模归一化：
// 错位解码可能产生 1e48 量级的角度差，逐次减 2π 的循环会退化为亿年级死循环。
func tessArc(cx, cy, r, a0, a1 float64, t xform) []stroke {
	// 变异输入可产生 NaN/Inf 角度（错位解码实证）：Mod 后 span 仍为 NaN
	// 会导致 segments 计算溢出为负，make cap panic，须先做有限性防御
	if !isFinite(a0) || !isFinite(a1) || !isFinite(cx) || !isFinite(cy) || !isFinite(r) {
		return nil
	}
	span := math.Mod(a1-a0, 2*math.Pi)
	if span < 0 {
		span += 2 * math.Pi
	}
	segments := int(span/(3*math.Pi/180)) + 1
	if segments > 720 {
		segments = 720
	}
	out := make([]stroke, 0, segments)
	prevA := a0
	for i := 1; i <= segments; i++ {
		a := a0 + span*float64(i)/float64(segments)
		p1 := t.apply(point2{cx + r*math.Cos(prevA), cy + r*math.Sin(prevA)})
		p2 := t.apply(point2{cx + r*math.Cos(a), cy + r*math.Sin(a)})
		out = append(out, stroke{p1.x, p1.y, p2.x, p2.y})
		prevA = a
	}
	return out
}

// tessEllipse 椭圆参数方程离散。
func tessEllipse(e *entEllipse, t xform) []stroke {
	majorLen := math.Hypot(e.majorAxis.x, e.majorAxis.y)
	if majorLen == 0 || e.ratio <= 0 {
		return nil
	}
	majorAng := math.Atan2(e.majorAxis.y, e.majorAxis.x)
	span := e.endAng - e.startAng
	segments := 96
	out := make([]stroke, 0, segments)
	pointAt := func(a float64) point2 {
		// 椭圆角度相对主轴方向
		rx := majorLen * math.Cos(a)
		ry := majorLen * e.ratio * math.Sin(a)
		wx := rx*math.Cos(majorAng) - ry*math.Sin(majorAng)
		wy := rx*math.Sin(majorAng) + ry*math.Cos(majorAng)
		return t.apply(point2{e.center.x + wx, e.center.y + wy})
	}
	prev := pointAt(e.startAng)
	for i := 1; i <= segments; i++ {
		a := e.startAng + span*float64(i)/float64(segments)
		cur := pointAt(a)
		out = append(out, stroke{prev.x, prev.y, cur.x, cur.y})
		prev = cur
	}
	return out
}

// tessLwPolyline 多段线离散：直线顶点间连线，bulge≠0 顶点间插弧。
// bulge = tan(θ/4)，θ 为该段弧的圆心角。
func tessLwPolyline(e *entLwPolyline, t xform) []stroke {
	// 段数下界 = 顶点数-1（bulge 段按需增长）；顶点级大多段线下预分配
	// 免反复翻倍扩容（pprof 渲染分配 ~21%）
	out := make([]stroke, 0, len(e.vertices)+8)
	n := len(e.vertices)
	if n == 0 {
		return nil
	}
	emitArc := func(p1, p2 point2, bulge float64) {
		theta := 4 * math.Atan(bulge)
		// 弦中点到圆心的距离
		chord := math.Hypot(p2.x-p1.x, p2.y-p1.y)
		if chord < 1e-12 {
			return
		}
		r := chord / (2 * math.Sin(math.Abs(theta)/2))
		// 圆心在弦的垂直平分线上，偏向 bulge 符号一侧
		midX, midY := (p1.x+p2.x)/2, (p1.y+p2.y)/2
		dist := math.Sqrt(math.Max(0, r*r-chord*chord/4))
		nx, ny := -(p2.y-p1.y)/chord, (p2.x-p1.x)/chord
		if bulge < 0 {
			nx, ny = -nx, -ny
		}
		cx, cy := midX+nx*dist, midY+ny*dist
		a0 := math.Atan2(p1.y-cy, p1.x-cx)
		a1 := a0 + theta
		out = append(out, tessArc(cx, cy, math.Abs(r), a0, a1, t)...)
	}
	bulgeAt := func(i int) float64 {
		if i < len(e.bulges) {
			return e.bulges[i]
		}
		return 0
	}
	for i := 0; i < n-1; i++ {
		p1 := t.apply(e.vertices[i])
		p2 := t.apply(e.vertices[i+1])
		if b := bulgeAt(i); b != 0 {
			emitArc(p1, p2, b)
		} else {
			out = append(out, stroke{p1.x, p1.y, p2.x, p2.y})
		}
	}
	// 几何闭合（首尾顶点重合）时最后一段已回到起点，无需回连段
	return out
}

// ---- 光栅画布 ----

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
func (c *canvas) setTransform(bbox box2, scale float64) {
	c.scale = scale
	c.minX = bbox.minX
	c.maxY = bbox.maxY
}

func (c *canvas) toPixel(p point2) (float64, float64) {
	return (p.x - c.minX) * c.scale, (c.maxY - p.y) * c.scale
}

// entityColor 解析实体最终颜色（true color > 实体 ACI > 图层 > 默认黑）。
func entityColor(doc *Document, e *primitive, bgWhite bool) color.RGBA {
	if e.color.hasTrue {
		r, g, b := splitTrueColor(e.color.trueColor)
		return color.RGBA{r, g, b, 255}
	}
	if e.color.hasIndex && e.color.index != 0 && e.color.index != 256 && e.color.index != 257 {
		if r, g, b, ok := aciColor(e.color.index, bgWhite); ok {
			return color.RGBA{r, g, b, 255}
		}
	}
	if lc, ok := doc.layerColors[e.layer]; ok {
		if c, ok := layerRenderColor(lc, bgWhite); ok {
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
func layerRenderColor(lc layerColor, bgWhite bool) (color.RGBA, bool) {
	if lc.hasTrue {
		if low := lc.trueColor & 0x00FFFFFF; low <= 0xFF {
			if r, g, b, ok := aciColor(uint16(low), bgWhite); ok {
				return color.RGBA{r, g, b, 255}, true
			}
		} else {
			r, g, b := splitTrueColor(lc.trueColor)
			return color.RGBA{r, g, b, 255}, true
		}
	}
	if lc.index != 0 && lc.index != 256 {
		if r, g, b, ok := aciColor(lc.index, bgWhite); ok {
			return color.RGBA{r, g, b, 255}, true
		}
	}
	return color.RGBA{}, false
}

// drawPrimitive 绘制一个展开后的图元。
func (c *canvas) drawPrimitive(p primitive, doc *Document, bgWhite bool) {
	col := entityColor(doc, &p, bgWhite)
	switch p.kind {
	case 0:
		for _, s := range p.strokes {
			if !plausible(s.x1, s.y1) || !plausible(s.x2, s.y2) {
				continue
			}
			c.drawLine(s, col)
		}
	case 1:
		if plausible(p.lb.x, p.lb.y) {
			if p.lb.tx != nil {
				c.drawLabelText(p.lb, p.lb.tx, col)
			} else {
				c.drawLabel(p.lb, col)
			}
		}
	}
}

// drawLine 世界坐标线段绘制。
func (c *canvas) drawLine(s stroke, col color.RGBA) {
	x1, y1 := c.toPixel(point2{s.x1, s.y1})
	x2, y2 := c.toPixel(point2{s.x2, s.y2})
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
func (c *canvas) drawLabel(lb label, col color.RGBA) {
	half := lb.h / 2
	cos, sin := math.Cos(lb.rot), math.Sin(lb.rot)
	// 矩形四角（基线左端为原点，向上为半高）——先算世界坐标，再一次转换到像素
	corners := [4][2]float64{
		{0, 0}, {lb.w, 0}, {lb.w, half}, {0, half},
	}
	pix := make([][2]float64, 4)
	for i, c0 := range corners {
		wx := lb.x + c0[0]*cos - c0[1]*sin
		wy := lb.y + c0[0]*sin + c0[1]*cos
		pix[i][0], pix[i][1] = c.toPixel(point2{wx, wy})
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

// robustBounds 鲁棒包围盒：以各端点坐标中位数为中心，取离群鲁棒的分位范围，
// 放大 3 倍作为视口；错位解码产生的少量天文数字坐标不影响视口。
// 性能：坐标收集按端点数预分配、中位/分位共用排序副本并原地变换为偏差
// 数组——大图元集（数十万端点）下 append 翻倍与重复复制排序曾是渲染
// 侧最大分配源（pprof ~35%）。
func robustBounds(prims []primitive) box2 {
	n := 0
	for _, p := range prims {
		switch p.kind {
		case 0:
			n += len(p.strokes)
		case 1:
			if plausible(p.lb.x, p.lb.y) {
				n++
			}
		}
	}
	if n == 0 {
		return primitivesBounds(prims)
	}
	xs := make([]float64, 0, n)
	ys := make([]float64, 0, n)
	for _, p := range prims {
		switch p.kind {
		case 0:
			for _, s := range p.strokes {
				if plausible(s.x1, s.y1) {
					xs = append(xs, s.x1)
					ys = append(ys, s.y1)
				}
				if plausible(s.x2, s.y2) {
					xs = append(xs, s.x2)
					ys = append(ys, s.y2)
				}
			}
		case 1:
			if plausible(p.lb.x, p.lb.y) {
				xs = append(xs, p.lb.x)
				ys = append(ys, p.lb.y)
			}
		}
	}
	if len(xs) < 4 {
		return primitivesBounds(prims)
	}
	// 排序副本求中位数（xs/ys 成对收集，长度恒相等）
	sortedX := append([]float64(nil), xs...)
	sort.Float64s(sortedX)
	sortedY := append([]float64(nil), ys...)
	sort.Float64s(sortedY)
	mx := sortedMedian(sortedX)
	my := sortedMedian(sortedY)
	// 原地变换为相对中位的偏差数组，排序后取 90 分位（xs/ys 此后不再使用）
	for i := range xs {
		xs[i] = math.Abs(xs[i] - mx)
		ys[i] = math.Abs(ys[i] - my)
	}
	sort.Float64s(xs)
	sort.Float64s(ys)
	last := len(xs) - 1
	rx := xs[int(float64(last)*0.9)] * 3
	ry := ys[int(float64(last)*0.9)] * 3
	if rx < 1e-6 {
		rx = 10
	}
	if ry < 1e-6 {
		ry = 10
	}
	return box2{minX: mx - rx, minY: my - ry, maxX: mx + rx, maxY: my + ry}
}

// sortedMedian 已升序数组的中位数（robustBounds 内部用，免重复排序）。
func sortedMedian(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// median 中位数（测试与通用用途；robustBounds 走 sortedMedian 免重复排序）。
func median(v []float64) float64 {
	sorted := append([]float64(nil), v...)
	sort.Float64s(sorted)
	return sortedMedian(sorted)
}

// primitivesBounds 计算图元世界包围盒。
func primitivesBounds(prims []primitive) box2 {
	var b box2
	b.minX, b.minY = math.Inf(1), math.Inf(1)
	b.maxX, b.maxY = math.Inf(-1), math.Inf(-1)
	for _, p := range prims {
		switch p.kind {
		case 0:
			for _, s := range p.strokes {
				// 剔除错位解码产生的异常坐标（超出工程量级），避免包围盒被撑爆
				if !plausible(s.x1, s.y1) || !plausible(s.x2, s.y2) {
					continue
				}
				b.extend(s.x1, s.y1)
				b.extend(s.x2, s.y2)
			}
		case 1:
			if !plausible(p.lb.x, p.lb.y) {
				continue
			}
			cos, sin := math.Cos(p.lb.rot), math.Sin(p.lb.rot)
			b.extend(p.lb.x, p.lb.y)
			b.extend(p.lb.x+p.lb.w*cos, p.lb.y+p.lb.w*sin)
			// 字形字面上延接近整字高（真实字形路径 ~0.88em），按整高计入
			// 包围盒，避免贴近视口边缘的文字整体越界被裁
			b.extend(p.lb.x+p.lb.w/2, p.lb.y+p.lb.h)
			// MTEXT 块自锚点向下（附着 1-3）/双向（4-6）/向上（7-9）排布，
			// 按换行行数与行距系数估算块高计入包围盒：墨迹锚定位置修复后
			// 仅向上扩展整字高不再覆盖下行块（2000/Text.dwg 单 MTEXT 样本
			// 渲染空实证——块体全部落在锚点下方视口之外）
			if p.lb.tx != nil && p.lb.tx.attachment != 0 {
				bh := mtextBlockHeightEstimate(p.lb.tx)
				mid := p.lb.x + p.lb.w*cos/2
				switch (p.lb.tx.attachment-1)/3 + 1 {
				case 2:
					b.extend(mid, p.lb.y-bh/2)
					b.extend(mid, p.lb.y+bh/2)
				case 3:
					b.extend(mid, p.lb.y+bh)
				default:
					b.extend(mid, p.lb.y-bh)
				}
			}
		}
	}
	return b
}

// mtextBlockHeightEstimate MTEXT 块高估算（包围盒口径）：按列宽贪心换行的
// 行数（字符宽 CJK 1.0/其余 0.5 em，与 render 侧换行同口径）与行距系数
// （DXF 44，未存储按 1.66 缺省行距兜底）估算自块顶到块底的世界高度。
func mtextBlockHeightEstimate(tx *textInfo) float64 {
	h := tx.hWorld
	if h <= 0 {
		h = 1
	}
	limit := tx.rectWidth / h
	lines := 0
	for _, ln := range tx.lines {
		em, n := 0.0, 1
		for _, r := range ln {
			w := 1.0
			if r <= 0x2e80 {
				w = 0.5
			}
			if limit > 0 && em+w > limit {
				n++
				em = w
			} else {
				em += w
			}
		}
		lines += n
	}
	if lines < 1 {
		lines = 1
	}
	f := tx.lineFactor
	if f <= 0 {
		f = mtextLineFactor
	}
	return float64(lines-1)*f*h + h
}

// plausible 坐标量级合理性（工程图纸世界坐标通常 <1e7）。
func plausible(x, y float64) bool {
	return isFinite(x) && isFinite(y) && math.Abs(x) < 1e7 && math.Abs(y) < 1e7
}

// extend 扩展包围盒以包含点。
func (b *box2) extend(x, y float64) {
	if x < b.minX {
		b.minX = x
	}
	if y < b.minY {
		b.minY = y
	}
	if x > b.maxX {
		b.maxX = x
	}
	if y > b.maxY {
		b.maxY = y
	}
}

// invalid 包围盒是否无有效内容（从未扩展过）。
func (b box2) invalid() bool {
	return math.IsInf(b.minX, 1) || math.IsInf(b.maxX, -1)
}

// dropOversizeStrokes 剔除长度超过视口对角线 1.5 倍的线段图元。
// 错位解码的 LINE 常表现为「一端在原点/锚点、另一端在远处」的超长线。
func dropOversizeStrokes(prims []primitive, bbox box2) []primitive {
	diag := math.Hypot(bbox.maxX-bbox.minX, bbox.maxY-bbox.minY)
	limit := diag * 1.5
	out := make([]primitive, 0, len(prims))
	for _, p := range prims {
		if p.kind != 0 {
			out = append(out, p)
			continue
		}
		kept := p.strokes[:0:0]
		for _, s := range p.strokes {
			if math.Hypot(s.x2-s.x1, s.y2-s.y1) > limit {
				continue
			}
			kept = append(kept, s)
		}
		if len(kept) > 0 {
			p.strokes = kept
			out = append(out, p)
		}
	}
	return out
}

// dropOriginAnchored 剔除一端在原点附近（视口 4% 内）、另一端超出视口 1.5 倍的线段。
func dropOriginAnchored(prims []primitive, bbox box2) []primitive {
	cx := (bbox.minX + bbox.maxX) / 2
	cy := (bbox.minY + bbox.maxY) / 2
	halfW := (bbox.maxX - bbox.minX) / 2
	halfH := (bbox.maxY - bbox.minY) / 2
	nearR := math.Hypot(halfW, halfH) * 0.04
	farR := math.Hypot(halfW, halfH) * 1.5
	out := make([]primitive, 0, len(prims))
	for _, p := range prims {
		if p.kind != 0 {
			out = append(out, p)
			continue
		}
		// 原地过滤：写入位置恒不超前读取位置，安全复用底层数组
		// （p.strokes 旧数组无其他引用，避免数十万段级别的重复分配）
		kept := p.strokes[:0]
		for _, s := range p.strokes {
			d1 := math.Hypot(s.x1-cx, s.y1-cy)
			d2 := math.Hypot(s.x2-cx, s.y2-cy)
			if (d1 < nearR && d2 > farR) || (d2 < nearR && d1 > farR) {
				continue
			}
			kept = append(kept, s)
		}
		if len(kept) > 0 {
			p.strokes = kept
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
func filterRadiatingStrokes(prims []primitive) []primitive {
	type key struct{ x, y int64 }
	const q = 1e-6
	const radiatingThreshold = 12
	// 重合计数 map 按端点数上界预分配：数十万端点下逐次扩容 rehash
	// 曾占渲染分配大头（pprof ~22%）
	total := 0
	for _, p := range prims {
		if p.kind == 0 {
			total += len(p.strokes)
		}
	}
	count := make(map[key]int, total*2)
	for _, p := range prims {
		for _, s := range p.strokes {
			count[key{int64(s.x1 / q), int64(s.y1 / q)}]++
			count[key{int64(s.x2 / q), int64(s.y2 / q)}]++
		}
	}
	out := make([]primitive, 0, len(prims))
	for _, p := range prims {
		if p.kind != 0 {
			out = append(out, p)
			continue
		}
		// 原地过滤（同 dropOriginAnchored）：安全复用底层数组免重复分配
		kept := p.strokes[:0]
		for _, s := range p.strokes {
			if count[key{int64(s.x1 / q), int64(s.y1 / q)}] > radiatingThreshold ||
				count[key{int64(s.x2 / q), int64(s.y2 / q)}] > radiatingThreshold {
				continue
			}
			kept = append(kept, s)
		}
		if len(kept) > 0 {
			p.strokes = kept
			out = append(out, p)
		}
	}
	return out
}

// tessSpline 样条曲线细分：拟合点模式用向心 Catmull-Rom 平滑；
// 控制点模式用 De Boor 递推按节点向量求值（度数 ≤ 3 时精确）。
func tessSpline(e *entSpline, t xform) []stroke {
	if e.scenario == 2 && len(e.fitPoints) >= 2 {
		pts := catmullRomSpline(e.fitPoints, e.closed, 16)
		return polylineStrokes(pts, t)
	}
	if len(e.controlPoints) < 2 {
		return nil
	}
	deg := int(e.degree)
	if deg <= 0 || deg >= len(e.controlPoints) {
		// 度数退化：退化为控制点折线
		return polylineStrokes(e.controlPoints, t)
	}
	if len(e.knots) < len(e.controlPoints)+deg+1 {
		return polylineStrokes(e.controlPoints, t)
	}
	// De Boor 求值：每段控制点区间取 16 个采样
	var pts []point3
	spanCount := len(e.controlPoints) - deg
	for i := 0; i < spanCount; i++ {
		t0, t1 := e.knots[i+deg], e.knots[i+deg+1]
		if t1 <= t0 {
			continue
		}
		for s := 0; s < 16; s++ {
			u := t0 + (t1-t0)*float64(s)/16
			p := deBoor(e.controlPoints, e.weights, e.knots, deg, i+deg, u)
			pts = append(pts, p)
		}
	}
	last := e.controlPoints[len(e.controlPoints)-1]
	pts = append(pts, last)
	return polylineStrokes(pts, t)
}

// deBoor De Boor 递推求 B 样条上参数 u 处的点（knotSpan 为 u 所在节点区间上限索引）。
func deBoor(ctrl []point3, weights []float64, knots []float64, degree, knotSpan int, u float64) point3 {
	d := make([]point3, degree+1)
	w := make([]float64, degree+1)
	for j := 0; j <= degree; j++ {
		idx := knotSpan - degree + j
		if idx >= len(ctrl) {
			idx = len(ctrl) - 1
		}
		d[j] = ctrl[idx]
		if idx < len(weights) {
			w[j] = weights[idx]
		} else {
			w[j] = 1
		}
	}
	for r := 1; r <= degree; r++ {
		for j := degree; j >= r; j-- {
			i := knotSpan - degree + j
			if i-1 < 0 || i >= len(knots) {
				continue
			}
			denom := knots[i+degree-r+1] - knots[i]
			alpha := 0.0
			if denom != 0 {
				alpha = (u - knots[i]) / denom
			}
			// 有理权重插值
			d[j] = point3{
				(1-alpha)*d[j].x*w[j] + alpha*d[j-1].x*w[j-1],
				(1-alpha)*d[j].y*w[j] + alpha*d[j-1].y*w[j-1],
				(1-alpha)*d[j].z*w[j] + alpha*d[j-1].z*w[j-1],
			}
			w[j] = (1-alpha)*w[j] + alpha*w[j-1]
			if w[j] != 0 {
				d[j] = point3{d[j].x / w[j], d[j].y / w[j], d[j].z / w[j]}
			}
		}
	}
	return d[degree]
}

// catmullRomSpline 向心 Catmull-Rom 平滑（拟合点模式）。
func catmullRomSpline(points []point3, closed bool, segments int) []point3 {
	if len(points) < 2 {
		return points
	}
	if segments < 1 {
		segments = 1
	}
	var out []point3
	n := len(points)
	segCount := n
	if !closed {
		segCount = n - 1
	}
	at := func(i int) point3 {
		if closed {
			return points[(i%n+n)%n]
		}
		if i < 0 {
			return points[0]
		}
		if i >= n {
			return points[n-1]
		}
		return points[i]
	}
	for i := 0; i < segCount; i++ {
		p0, p1, p2, p3 := at(i-1), at(i), at(i+1), at(i+2)
		t0 := 0.0
		t1 := t0 + crDist(p0, p1)
		t2 := t1 + crDist(p1, p2)
		t3 := t2 + crDist(p2, p3)
		for s := 0; s <= segments; s++ {
			if i > 0 && s == 0 {
				continue
			}
			u := float64(s) / float64(segments)
			tt := t1 + (t2-t1)*u
			out = append(out, crPoint(p0, p1, p2, p3, t0, t1, t2, t3, tt))
		}
	}
	return out
}

func crDist(a, b point3) float64 {
	dx, dy, dz := a.x-b.x, a.y-b.y, a.z-b.z
	return math.Pow(dx*dx+dy*dy+dz*dz, 0.25) // 距离^alpha，alpha=0.5
}

func crPoint(p0, p1, p2, p3 point3, t0, t1, t2, t3, t float64) point3 {
	crLerp := func(a, b point3, ta, tb float64) point3 {
		if math.Abs(tb-ta) < 1e-12 {
			return a
		}
		w0 := (tb - t) / (tb - ta)
		w1 := (t - ta) / (tb - ta)
		return point3{w0*a.x + w1*b.x, w0*a.y + w1*b.y, w0*a.z + w1*b.z}
	}
	a1 := crLerp(p0, p1, t0, t1)
	a2 := crLerp(p1, p2, t1, t2)
	a3 := crLerp(p2, p3, t2, t3)
	b1 := crLerp(a1, a2, t0, t2)
	b2 := crLerp(a2, a3, t1, t3)
	return crLerp(b1, b2, t1, t2)
}

// polylineStrokes 点列转折线段（应用变换）。
func polylineStrokes(pts []point3, t xform) []stroke {
	if len(pts) < 2 {
		return nil
	}
	var out []stroke
	prev := t.apply(point2{pts[0].x, pts[0].y})
	for _, p := range pts[1:] {
		cur := t.apply(point2{p.x, p.y})
		out = append(out, stroke{prev.x, prev.y, cur.x, cur.y})
		prev = cur
	}
	return out
}

// tessHelix 螺旋线 2D 投影离散：绕轴点（XY 投影）按 turns 圈数参数化，
// 半径取 spec radius，起角由 start_pt 相对轴点的 XY 方位确定（z 分量
// 不参与 2D 视口投影）。
func tessHelix(e *entHelix, t xform) []stroke {
	if e.radius <= 0 || e.turns <= 0 {
		return nil
	}
	const stepsPerTurn = 32
	total := int(e.turns * stepsPerTurn)
	if total < 8 {
		total = 8
	}
	if total > 20000 {
		total = 20000
	}
	cx, cy := e.axisBasePt.x, e.axisBasePt.y
	startAng := math.Atan2(e.startPt.y-cy, e.startPt.x-cx)
	// 左右手决定旋向（ handedness：true=逆时针/右手，false=顺时针）
	dir := 1.0
	if !e.handedness {
		dir = -1.0
	}
	pts := make([]point3, 0, total+1)
	for i := 0; i <= total; i++ {
		frac := float64(i) / stepsPerTurn
		ang := startAng + dir*frac*2*math.Pi
		pts = append(pts, point3{cx + e.radius*math.Cos(ang), cy + e.radius*math.Sin(ang), 0})
	}
	return polylineStrokes(pts, t)
}

// tessUnderlay 底图引用框离散：裁剪多边形顶点按 scale/angle 变换后
// 平移到插入点（定义坐标系 → 世界坐标），闭合描边呈现引用范围。
func tessUnderlay(e *entUnderlay, t xform) []stroke {
	if len(e.clipVerts) < 2 {
		return nil
	}
	ca, sa := math.Cos(e.angle), math.Sin(e.angle)
	pts := make([]point3, 0, len(e.clipVerts)+1)
	for _, v := range e.clipVerts {
		wx := v.x * e.scale.x
		wy := v.y * e.scale.y
		pts = append(pts, point3{
			e.insPt.x + wx*ca - wy*sa,
			e.insPt.y + wx*sa + wy*ca,
			0,
		})
	}
	// 闭合多边形
	pts = append(pts, pts[0])
	return polylineStrokes(pts, t)
}
