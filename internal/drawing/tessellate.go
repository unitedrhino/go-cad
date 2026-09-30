// tessellate.go 图元离散与图纸遍历：实体 → 描边段/文字占位（primitive）
// 的坐标变换与逐类型离散，供 PNG/SVG 渲染与 DXF 写出的图幅范围计算共用。
// 本文件自 render.go 上提至 drawing 层（DXF 写出与渲染同源消费，
// Document 归 drawing，避免 drawing↔render 循环依赖）。
package drawing

import (
	"math"
	"strings"

	"github.com/unitedrhino/go-cad/internal/entity"
)

// stroke 一条世界坐标描边段。
type Stroke struct{ X1, Y1, X2, Y2 float64 }

// label 文字占位条。
type Label struct {
	X, Y, W, H, Rot float64        // 基线起点、宽高（世界单位）、旋转弧度
	Tx              *GlyphTextInfo // 真实字形版式信息（nil=纯占位条，仅几何回退路径产生）
	Text            string         // 剥离格式码后的文本内容（RenderSVG 的 <text> 元素用；PNG 字形路径经 tx.lines 取文本）
	Anchor          uint8          // 水平锚点：0=start 1=middle 2=end（SVG text-anchor；PNG 对齐经 tx.hAlign/attachment）
	Handle          uint64         // 源实体句柄（RenderSVG 的 <text data-h> 元数据用；占位条回退路径为 0）
}

// textInfo 真实字形渲染所需的文本版式信息（render_text.go 消费）。
// 占位几何估算与占位条回退只依赖 label 的既有字段，本结构仅字形路径读取。
type GlyphTextInfo struct {
	Lines       []string // 已剥离格式指令的文本行
	HWorld      float64  // 字高（世界单位）
	Ux, Uy      float64  // 世界空间推进方向基向量（含实体变换/镜像，模长含变换比例）
	Vx, Vy      float64  // 世界空间字面向上方向基向量
	Gen         uint16   // TEXT/ATTRIB 生成标志（0x2 X 镜像 / 0x4 Y 镜像）
	HAlign      uint16   // 水平对齐（DXF 0 左/1 中/2 右/3,4,5 对齐变体）
	VAlign      uint16   // 垂直对齐（0 基线/1 下/2 中/3 上）
	Attachment  uint16   // MTEXT 附着点 1-9（0=单行文本语义）
	RectWidth   float64  // MTEXT 列宽（世界单位，0=不换行）
	LineFactor  float64  // MTEXT 行距系数（DXF 44 linespace_factor；≤0=未存储，绘制侧按缺省行距兜底）
	WidthFactor float64  // 宽度因子（TEXT/ATTRIB 解码 width_factor 接入；MTEXT 恒 1）
	Oblique     float64  // 倾斜角弧度（默认 0；解码侧暂未接入该字段，预留参数）
}

// primitive 展开后的可绘制单元。
type Primitive struct {
	Kind    int // 0 stroke, 1 label
	Strokes []Stroke
	Lb      Label
	Color   entity.EntColor
	Layer   uint64
	Kind0   string // 类型名（调试/渲染策略）
}

// tessellator INSERT 展开与图元离散。
// 展开防爆双保险：insPath 环检测（真实 DWG 块引用为有向无环图，路径上
// 重复出现同一块定义即自引用/互引用环，短路展开）+ budget 全局预算
// （防无环但分支组合巨大的异常文件，量级按真实大图完整展开需求放大，
// 如 9 张 A1 图框 × 大样图块的用户案例全图展开消耗约 38 万实例）。
type Tessellator struct {
	Doc      *Document
	maxDepth int
	Budget   int
	insPath  []uint64 // 当前 INSERT 展开路径上的块定义句柄（环检测）
	vertex2d map[uint64]*entity.EntVertex2d
	vertex3d map[uint64]*entity.EntVertex3d
}

func NewTessellator(doc *Document) *Tessellator {
	return &Tessellator{Doc: doc, maxDepth: 12, Budget: 2000000}
}

// xform 仿射变换（世界坐标）。
type Xform struct {
	Sx, Sy   float64 // 缩放
	Cos, sin float64 // 旋转
	Tx, Ty   float64 // 平移
}

func IdentityXform() Xform { return Xform{Sx: 1, Sy: 1, Cos: 1} }

func (t Xform) Apply(p entity.Point2) entity.Point2 {
	return entity.Point2{
		X: p.X*t.Sx*t.Cos - p.Y*t.Sy*t.sin + t.Tx,
		Y: p.X*t.Sx*t.sin + p.Y*t.Sy*t.Cos + t.Ty,
	}
}

func (t Xform) lengthScale() float64 {
	return math.Abs(t.Sx)
}

func (t Xform) Compose(child Xform) Xform {
	// 结果 = t ∘ child：先应用 child（缩放→旋转→平移），再应用 t
	cos := t.Cos*child.Cos - t.sin*child.sin
	sin := t.sin*child.Cos + t.Cos*child.sin
	return Xform{
		Sx: t.Sx * child.Sx, Sy: t.Sy * child.Sy,
		Cos: cos, sin: sin,
		Tx: t.Tx + t.Cos*t.Sx*child.Tx - t.sin*t.Sy*child.Ty,
		Ty: t.Ty + t.sin*t.Sx*child.Tx + t.Cos*t.Sy*child.Ty,
	}
}

// insertXform 构造 INSERT 的变换：缩放→旋转→平移插入点。
func InsertXform(e *entity.EntInsert) Xform {
	return Xform{
		Sx: e.Scale.X, Sy: e.Scale.Y,
		Cos: math.Cos(e.Rotation), sin: math.Sin(e.Rotation),
		Tx: e.Position.X, Ty: e.Position.Y,
	}
}

// buildVertexIndex 构建句柄 → 顶点实体的索引（POLYLINE 聚合用）。
// 先遍历计数顶点实体再按计数预分配 map：大图纸顶点数以万计，逐次
// 扩容 rehash 是渲染侧分配热点之一。
func (ts *Tessellator) BuildVertexIndex() {
	count2d, count3d := 0, 0
	tally := func(list []any) {
		for _, e := range list {
			switch e.(type) {
			case *entity.EntVertex2d:
				count2d++
			case *entity.EntVertex3d:
				count3d++
			}
		}
	}
	tally(ts.Doc.ModelSpace)
	tally(ts.Doc.PspaceSpace)
	for _, list := range ts.Doc.Blocks {
		tally(list)
	}
	ts.vertex2d = make(map[uint64]*entity.EntVertex2d, count2d)
	ts.vertex3d = make(map[uint64]*entity.EntVertex3d, count3d)
	add := func(list []any) {
		for _, e := range list {
			switch t := e.(type) {
			case *entity.EntVertex2d:
				ts.vertex2d[t.Handle] = t
			case *entity.EntVertex3d:
				ts.vertex3d[t.Handle] = t
			}
		}
	}
	add(ts.Doc.ModelSpace)
	add(ts.Doc.PspaceSpace)
	for _, list := range ts.Doc.Blocks {
		add(list)
	}
}

// expandAll 展开模型空间全部图元。
// pre-R13 图纸空间实体（pspaceSpace，R13+ 无此列表）一并展开：
// 图纸空间布局文件（如 ACEB10）的主内容位于该列表，跳过会渲染空白。
func (ts *Tessellator) ExpandAll() []Primitive {
	ts.BuildVertexIndex()
	// 展开结果数下界为直属实体数（INSERT 递归展开再按需增长），
	// 按下界预分配免前几次翻倍扩容
	out := make([]Primitive, 0, len(ts.Doc.ModelSpace)+len(ts.Doc.PspaceSpace))
	for _, ent := range ts.Doc.ModelSpace {
		out = ts.AppendEntity(out, ent, IdentityXform(), 0)
	}
	for _, ent := range ts.Doc.PspaceSpace {
		out = ts.AppendEntity(out, ent, IdentityXform(), 0)
	}
	return out
}

// appendEntity 展开单个图元（INSERT 递归），应用当前变换。
func (ts *Tessellator) AppendEntity(out []Primitive, ent any, t Xform, depth int) []Primitive {
	if depth > ts.maxDepth || ts.Budget <= 0 {
		return out
	}
	ts.Budget--
	switch e := ent.(type) {
	case *entity.EntLine:
		a, b := t.Apply(entity.Point2{X: e.Start.X, Y: e.Start.Y}), t.Apply(entity.Point2{X: e.End.X, Y: e.End.Y})
		out = append(out, Primitive{Kind: 0, Strokes: []Stroke{{a.X, a.Y, b.X, b.Y}}, Color: e.Color, Layer: e.Layer, Kind0: "LINE"})
	case *entity.EntCircle:
		out = append(out, Primitive{Kind: 0, Strokes: TessCircle(e.Center.X, e.Center.Y, e.Radius, t), Color: e.Color, Layer: e.Layer, Kind0: "CIRCLE"})
	case *entity.EntArc:
		out = append(out, Primitive{Kind: 0, Strokes: TessArc(e.Center.X, e.Center.Y, e.Radius, e.AngleStart, e.AngleEnd, t), Color: e.Color, Layer: e.Layer, Kind0: "ARC"})
	case *entity.EntPoint:
		p := t.Apply(entity.Point2{X: e.Location.X, Y: e.Location.Y})
		r := 0.02 * t.lengthScale()
		if r == 0 {
			r = 0.1
		}
		out = append(out, Primitive{Kind: 0, Strokes: []Stroke{
			{p.X - r, p.Y, p.X + r, p.Y}, {p.X, p.Y - r, p.X, p.Y + r},
		}, Color: e.Color, Layer: e.Layer, Kind0: "POINT"})
	case *entity.EntEllipse:
		out = append(out, Primitive{Kind: 0, Strokes: TessEllipse(e, t), Color: e.Color, Layer: e.Layer, Kind0: "ELLIPSE"})
	case *entity.EntLwPolyline:
		out = append(out, Primitive{Kind: 0, Strokes: TessLwPolyline(e, t), Color: e.Color, Layer: e.Layer, Kind0: "LWPOLYLINE"})
	case *entity.EntText:
		// 非默认对齐（h/vAlign 任一非零）时锚点取 alignment_pt（DXF 语义）
		x, y := e.Insertion.X, e.Insertion.Y
		if (e.HAlign != 0 || e.VAlign != 0) && e.AlignPt != nil {
			x, y = e.AlignPt.X, e.AlignPt.Y
		}
		out = append(out, ts.TextLabelWith(x, y, e.Height, e.Rotation, len([]rune(e.Text)), t, e, "TEXT", GlyphTextInfo{
			Lines: []string{e.Text}, Gen: e.Gen, HAlign: e.HAlign, VAlign: e.VAlign,
			WidthFactor: textWidthFactor(e.WidthFactor),
		}))
	case *entity.EntMText:
		rot := math.Atan2(e.XAxisDir.Y, e.XAxisDir.X)
		lines := strings.Split(StripMTextFormat(e.Text), "\n")
		n := 0
		for _, ln := range lines {
			n += len([]rune(ln))
		}
		out = append(out, ts.TextLabelWith(e.Insertion.X, e.Insertion.Y, e.TextHeight, rot, n, t, e, "MTEXT", GlyphTextInfo{
			Lines: lines, Attachment: e.Attachment, RectWidth: e.RectWidth,
			LineFactor: e.LineFactor,
		}))
	case *entity.EntAttrib:
		x, y := e.Insertion.X, e.Insertion.Y
		if (e.HAlign != 0 || e.VAlign != 0) && e.AlignPt != nil {
			x, y = e.AlignPt.X, e.AlignPt.Y
		}
		out = append(out, ts.TextLabelWith(x, y, e.Height, e.Rotation, len([]rune(e.Text)), t, e, "ATTRIB", GlyphTextInfo{
			Lines: []string{e.Text}, Gen: e.Gen, HAlign: e.HAlign, VAlign: e.VAlign,
			WidthFactor: textWidthFactor(e.WidthFactor),
		}))
	case *entity.EntSpline:
		out = append(out, Primitive{Kind: 0, Strokes: TessSpline(e, t), Color: e.Color, Layer: e.Layer, Kind0: "SPLINE"})
	case *entity.EntHelix:
		out = append(out, Primitive{Kind: 0, Strokes: tessHelix(e, t), Color: e.Color, Layer: e.Layer, Kind0: "HELIX"})
	case *entity.EntUnderlay:
		out = append(out, Primitive{Kind: 0, Strokes: tessUnderlay(e, t), Color: e.Color, Layer: e.Layer, Kind0: "UNDERLAY"})
	case *entity.EntHatch:
		for _, p := range e.Paths {
			var pts []entity.Point2
			for _, v := range p.Points {
				pts = append(pts, t.Apply(v))
			}
			var strokes []Stroke
			for i := 0; i+1 < len(pts); i++ {
				strokes = append(strokes, Stroke{pts[i].X, pts[i].Y, pts[i+1].X, pts[i+1].Y})
			}
			out = append(out, Primitive{Kind: 0, Strokes: strokes, Color: e.Color, Layer: e.Layer, Kind0: "HATCH"})
		}
	case *entity.EntDimension:
		// 标注：连接测量点与文字中点（简化可视表达）
		var pts []entity.Point2
		for _, p := range []entity.Point3{e.Point10, e.Point13, e.Point14} {
			pts = append(pts, t.Apply(entity.Point2{X: p.X, Y: p.Y}))
		}
		mid := t.Apply(entity.Point2{X: e.TextMidpoint.X, Y: e.TextMidpoint.Y})
		var strokes []Stroke
		for i := 0; i+1 < len(pts); i++ {
			strokes = append(strokes, Stroke{pts[i].X, pts[i].Y, pts[i+1].X, pts[i+1].Y})
		}
		if len(pts) > 0 {
			strokes = append(strokes, Stroke{pts[len(pts)-1].X, pts[len(pts)-1].Y, mid.X, mid.Y})
		}
		out = append(out, Primitive{Kind: 0, Strokes: strokes, Color: e.Color, Layer: e.Layer, Kind0: "DIMENSION"})
	case *entity.EntRay:
		a := t.Apply(entity.Point2{X: e.Start.X, Y: e.Start.Y})
		end := entity.Point2{X: e.Start.X + e.UnitVector.X*1e6, Y: e.Start.Y + e.UnitVector.Y*1e6}
		b := t.Apply(end)
		strokes := []Stroke{{a.X, a.Y, b.X, b.Y}}
		if e.Xline {
			c := t.Apply(entity.Point2{X: e.Start.X - e.UnitVector.X*1e6, Y: e.Start.Y - e.UnitVector.Y*1e6})
			strokes = append(strokes, Stroke{a.X, a.Y, c.X, c.Y})
		}
		kind0 := "RAY"
		if e.Xline {
			kind0 = "XLINE"
		}
		out = append(out, Primitive{Kind: 0, Strokes: strokes, Color: e.Color, Layer: e.Layer, Kind0: kind0})
	case *entity.EntSolid:
		pts := []entity.Point2{t.Apply(e.P1), t.Apply(e.P2), t.Apply(e.P3), t.Apply(e.P4)}
		var strokes []Stroke
		for i := 0; i+1 < len(pts); i++ {
			strokes = append(strokes, Stroke{pts[i].X, pts[i].Y, pts[i+1].X, pts[i+1].Y})
		}
		strokes = append(strokes, Stroke{pts[3].X, pts[3].Y, pts[0].X, pts[0].Y})
		kind0 := "SOLID"
		if e.Trace {
			kind0 = "TRACE"
		}
		out = append(out, Primitive{Kind: 0, Strokes: strokes, Color: e.Color, Layer: e.Layer, Kind0: kind0})
	case *entity.EntFace3d:
		pts := []entity.Point2{t.Apply(entity.Point2{X: e.P1.X, Y: e.P1.Y}), t.Apply(entity.Point2{X: e.P2.X, Y: e.P2.Y}),
			t.Apply(entity.Point2{X: e.P3.X, Y: e.P3.Y}), t.Apply(entity.Point2{X: e.P4.X, Y: e.P4.Y})}
		var strokes []Stroke
		for i := 0; i+1 < len(pts); i++ {
			strokes = append(strokes, Stroke{pts[i].X, pts[i].Y, pts[i+1].X, pts[i+1].Y})
		}
		strokes = append(strokes, Stroke{pts[3].X, pts[3].Y, pts[0].X, pts[0].Y})
		out = append(out, Primitive{Kind: 0, Strokes: strokes, Color: e.Color, Layer: e.Layer, Kind0: "3DFACE"})
	case *entity.EntLeader:
		if len(e.Points) >= 2 {
			var strokes []Stroke
			prev := t.Apply(entity.Point2{X: e.Points[0].X, Y: e.Points[0].Y})
			for _, p := range e.Points[1:] {
				cur := t.Apply(entity.Point2{X: p.X, Y: p.Y})
				strokes = append(strokes, Stroke{prev.X, prev.Y, cur.X, cur.Y})
				prev = cur
			}
			out = append(out, Primitive{Kind: 0, Strokes: strokes, Color: e.Color, Layer: e.Layer, Kind0: "LEADER"})
		}
	case *entity.EntMLine:
		if len(e.Vertices) >= 2 {
			var strokes []Stroke
			prev := t.Apply(entity.Point2{X: e.Vertices[0].Position.X, Y: e.Vertices[0].Position.Y})
			for _, v := range e.Vertices[1:] {
				cur := t.Apply(entity.Point2{X: v.Position.X, Y: v.Position.Y})
				strokes = append(strokes, Stroke{prev.X, prev.Y, cur.X, cur.Y})
				prev = cur
			}
			out = append(out, Primitive{Kind: 0, Strokes: strokes, Color: e.Color, Layer: e.Layer, Kind0: "MLINE"})
		}
	case *entity.EntPolyline2d:
		var strokes []Stroke
		var pts []entity.Point2
		for _, vh := range e.OwnedHandles {
			if v, ok := ts.vertex2d[vh]; ok {
				pts = append(pts, t.Apply(entity.Point2{X: v.Position.X, Y: v.Position.Y}))
			}
		}
		for i := 0; i+1 < len(pts); i++ {
			strokes = append(strokes, Stroke{pts[i].X, pts[i].Y, pts[i+1].X, pts[i+1].Y})
		}
		out = append(out, Primitive{Kind: 0, Strokes: strokes, Color: e.Color, Layer: e.Layer, Kind0: "POLYLINE_2D"})
	case *entity.EntPolyline3d:
		var strokes []Stroke
		var pts []entity.Point2
		for _, vh := range e.OwnedHandles {
			if v, ok := ts.vertex3d[vh]; ok {
				pts = append(pts, t.Apply(entity.Point2{X: v.Position.X, Y: v.Position.Y}))
			}
		}
		for i := 0; i+1 < len(pts); i++ {
			strokes = append(strokes, Stroke{pts[i].X, pts[i].Y, pts[i+1].X, pts[i+1].Y})
		}
		out = append(out, Primitive{Kind: 0, Strokes: strokes, Color: e.Color, Layer: e.Layer, Kind0: "POLYLINE_3D"})
	case *entity.EntInsert:
		// 环检测：展开路径上已出现同一块定义即自引用/互引用环，短路该
		// 分支（环分支不消耗预算，预算只对无环的巨量展开兜底）
		for _, h := range ts.insPath {
			if h == e.BlockHeader {
				return out
			}
		}
		ts.insPath = append(ts.insPath, e.BlockHeader)
		child := t.Compose(InsertXform(e))
		for _, inner := range ts.Doc.Blocks[e.BlockHeader] {
			out = ts.AppendEntity(out, inner, child, depth+1)
		}
		// 关联属性（文字位置在插入变换下的对应位置）
		for _, ah := range e.Attribs {
			if a, ok := ts.Doc.Attribs[ah]; ok {
				out = ts.AppendEntity(out, a, child, depth+1)
			}
		}
		ts.insPath = ts.insPath[:len(ts.insPath)-1]
	}
	return out
}

// textLabel 文字占位条：按字符数与字高估算占位宽度。
// text 携带剥离格式码后的文本内容、anchor 携带水平锚点（RenderSVG 矢量
// 输出用；PNG 路径仅消费几何字段，文本为空时 SVG 退化为占位框）。
func (ts *Tessellator) TextLabel(x, y, h, rot float64, nChars int, t Xform, e entity.EntityCommon, kind, text string, anchor uint8) Primitive {
	if nChars <= 0 {
		nChars = 1
	}
	// 世界宽度 ≈ 字符数 × 字高 × 0.8（中文按全角 1.0/字符的上界收缩）
	w := float64(nChars) * h * 0.8
	if w > h*80 {
		w = h * 80
	}
	p1 := t.Apply(entity.Point2{X: x, Y: y})
	p2 := t.Apply(entity.Point2{X: x + w*math.Cos(rot), Y: y + w*math.Sin(rot)})
	scaledH := h * t.lengthScale()
	return Primitive{
		Kind:  1,
		Lb:    Label{X: p1.X, Y: p1.Y, W: p2.X - p1.X, H: scaledH, Rot: math.Atan2(p2.Y-p1.Y, p2.X-p1.X), Text: text, Anchor: anchor},
		Color: e.Common().Color, Layer: e.Common().Layer, Kind0: kind,
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
func (ts *Tessellator) TextLabelWith(x, y, h, rot float64, nChars int, t Xform, e entity.EntityCommon, kind string, tx GlyphTextInfo) Primitive {
	p := ts.TextLabel(x, y, h, rot, nChars, t, e, kind, "", 0)
	p.Lb.Handle = e.Common().Handle
	c, s := math.Cos(rot), math.Sin(rot)
	o := t.Apply(entity.Point2{X: x, Y: y})
	u := t.Apply(entity.Point2{X: x + c, Y: y + s})
	v := t.Apply(entity.Point2{X: x - s, Y: y + c})
	tx.Ux, tx.Uy = u.X-o.X, u.Y-o.Y
	tx.Vx, tx.Vy = v.X-o.X, v.Y-o.Y
	tx.HWorld = h
	if tx.WidthFactor > 0 {
		// 占位条宽度同步宽度因子（TEXT/ATTRIB 压缩字宽；MTEXT 恒 1）
		p.Lb.W *= tx.WidthFactor
	} else {
		tx.WidthFactor = 1
	}
	tx.Oblique = 0
	p.Lb.Tx = &tx
	p.Lb.Text = strings.Join(tx.Lines, "\n")
	if tx.Attachment != 0 {
		p.Lb.Anchor = textAnchorAttachment(tx.Attachment)
	} else {
		p.Lb.Anchor = textAnchorHAlign(tx.HAlign)
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
func TessCircle(cx, cy, r float64, t Xform) []Stroke {
	const n = 72
	out := make([]Stroke, 0, n)
	prev := t.Apply(entity.Point2{X: cx + r, Y: cy})
	for i := 1; i <= n; i++ {
		a := float64(i) / n * 2 * math.Pi
		cur := t.Apply(entity.Point2{X: cx + r*math.Cos(a), Y: cy + r*math.Sin(a)})
		out = append(out, Stroke{prev.X, prev.Y, cur.X, cur.Y})
		prev = cur
	}
	return out
}

// tessArc 圆弧按 3°/段离散（跨零角处理）。span 用取模归一化：
// 错位解码可能产生 1e48 量级的角度差，逐次减 2π 的循环会退化为亿年级死循环。
func TessArc(cx, cy, r, a0, a1 float64, t Xform) []Stroke {
	// 变异输入可产生 NaN/Inf 角度（错位解码实证）：Mod 后 span 仍为 NaN
	// 会导致 segments 计算溢出为负，make cap panic，须先做有限性防御
	if !entity.IsFinite(a0) || !entity.IsFinite(a1) || !entity.IsFinite(cx) || !entity.IsFinite(cy) || !entity.IsFinite(r) {
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
	out := make([]Stroke, 0, segments)
	prevA := a0
	for i := 1; i <= segments; i++ {
		a := a0 + span*float64(i)/float64(segments)
		p1 := t.Apply(entity.Point2{X: cx + r*math.Cos(prevA), Y: cy + r*math.Sin(prevA)})
		p2 := t.Apply(entity.Point2{X: cx + r*math.Cos(a), Y: cy + r*math.Sin(a)})
		out = append(out, Stroke{p1.X, p1.Y, p2.X, p2.Y})
		prevA = a
	}
	return out
}

// tessEllipse 椭圆参数方程离散。
func TessEllipse(e *entity.EntEllipse, t Xform) []Stroke {
	majorLen := math.Hypot(e.MajorAxis.X, e.MajorAxis.Y)
	if majorLen == 0 || e.Ratio <= 0 {
		return nil
	}
	majorAng := math.Atan2(e.MajorAxis.Y, e.MajorAxis.X)
	span := e.EndAng - e.StartAng
	segments := 96
	out := make([]Stroke, 0, segments)
	pointAt := func(a float64) entity.Point2 {
		// 椭圆角度相对主轴方向
		rx := majorLen * math.Cos(a)
		ry := majorLen * e.Ratio * math.Sin(a)
		wx := rx*math.Cos(majorAng) - ry*math.Sin(majorAng)
		wy := rx*math.Sin(majorAng) + ry*math.Cos(majorAng)
		return t.Apply(entity.Point2{X: e.Center.X + wx, Y: e.Center.Y + wy})
	}
	prev := pointAt(e.StartAng)
	for i := 1; i <= segments; i++ {
		a := e.StartAng + span*float64(i)/float64(segments)
		cur := pointAt(a)
		out = append(out, Stroke{prev.X, prev.Y, cur.X, cur.Y})
		prev = cur
	}
	return out
}

// tessLwPolyline 多段线离散：直线顶点间连线，bulge≠0 顶点间插弧。
// bulge = tan(θ/4)，θ 为该段弧的圆心角。
func TessLwPolyline(e *entity.EntLwPolyline, t Xform) []Stroke {
	// 段数下界 = 顶点数-1（bulge 段按需增长）；顶点级大多段线下预分配
	// 免反复翻倍扩容（pprof 渲染分配 ~21%）
	out := make([]Stroke, 0, len(e.Vertices)+8)
	n := len(e.Vertices)
	if n == 0 {
		return nil
	}
	emitArc := func(p1, p2 entity.Point2, bulge float64) {
		theta := 4 * math.Atan(bulge)
		// 弦中点到圆心的距离
		chord := math.Hypot(p2.X-p1.X, p2.Y-p1.Y)
		if chord < 1e-12 {
			return
		}
		r := chord / (2 * math.Sin(math.Abs(theta)/2))
		// 圆心在弦的垂直平分线上，偏向 bulge 符号一侧
		midX, midY := (p1.X+p2.X)/2, (p1.Y+p2.Y)/2
		dist := math.Sqrt(math.Max(0, r*r-chord*chord/4))
		nx, ny := -(p2.Y-p1.Y)/chord, (p2.X-p1.X)/chord
		if bulge < 0 {
			nx, ny = -nx, -ny
		}
		cx, cy := midX+nx*dist, midY+ny*dist
		a0 := math.Atan2(p1.Y-cy, p1.X-cx)
		a1 := a0 + theta
		out = append(out, TessArc(cx, cy, math.Abs(r), a0, a1, t)...)
	}
	bulgeAt := func(i int) float64 {
		if i < len(e.Bulges) {
			return e.Bulges[i]
		}
		return 0
	}
	for i := 0; i < n-1; i++ {
		p1 := t.Apply(e.Vertices[i])
		p2 := t.Apply(e.Vertices[i+1])
		if b := bulgeAt(i); b != 0 {
			emitArc(p1, p2, b)
		} else {
			out = append(out, Stroke{p1.X, p1.Y, p2.X, p2.Y})
		}
	}
	// 几何闭合（首尾顶点重合）时最后一段已回到起点，无需回连段
	return out
}

// ---- 光栅画布 ----

// tessSpline 样条曲线细分：拟合点模式用向心 Catmull-Rom 平滑；
// 控制点模式用 De Boor 递推按节点向量求值（度数 ≤ 3 时精确）。
func TessSpline(e *entity.EntSpline, t Xform) []Stroke {
	if e.Scenario == 2 && len(e.FitPoints) >= 2 {
		pts := catmullRomSpline(e.FitPoints, e.Closed, 16)
		return polylineStrokes(pts, t)
	}
	if len(e.ControlPoints) < 2 {
		return nil
	}
	deg := int(e.Degree)
	if deg <= 0 || deg >= len(e.ControlPoints) {
		// 度数退化：退化为控制点折线
		return polylineStrokes(e.ControlPoints, t)
	}
	if len(e.Knots) < len(e.ControlPoints)+deg+1 {
		return polylineStrokes(e.ControlPoints, t)
	}
	// De Boor 求值：每段控制点区间取 16 个采样
	var pts []entity.Point3
	spanCount := len(e.ControlPoints) - deg
	for i := 0; i < spanCount; i++ {
		t0, t1 := e.Knots[i+deg], e.Knots[i+deg+1]
		if t1 <= t0 {
			continue
		}
		for s := 0; s < 16; s++ {
			u := t0 + (t1-t0)*float64(s)/16
			p := entity.DeBoor(e.ControlPoints, e.Weights, e.Knots, deg, i+deg, u)
			pts = append(pts, p)
		}
	}
	last := e.ControlPoints[len(e.ControlPoints)-1]
	pts = append(pts, last)
	return polylineStrokes(pts, t)
}

// tessHelix 螺旋线 2D 投影离散：绕轴点（XY 投影）按 turns 圈数参数化，
// 半径取 spec radius，起角由 start_pt 相对轴点的 XY 方位确定（z 分量
// 不参与 2D 视口投影）。
func tessHelix(e *entity.EntHelix, t Xform) []Stroke {
	if e.Radius <= 0 || e.Turns <= 0 {
		return nil
	}
	const stepsPerTurn = 32
	total := int(e.Turns * stepsPerTurn)
	if total < 8 {
		total = 8
	}
	if total > 20000 {
		total = 20000
	}
	cx, cy := e.AxisBasePt.X, e.AxisBasePt.Y
	startAng := math.Atan2(e.StartPt.Y-cy, e.StartPt.X-cx)
	// 左右手决定旋向（ handedness：true=逆时针/右手，false=顺时针）
	dir := 1.0
	if !e.Handedness {
		dir = -1.0
	}
	pts := make([]entity.Point3, 0, total+1)
	for i := 0; i <= total; i++ {
		frac := float64(i) / stepsPerTurn
		ang := startAng + dir*frac*2*math.Pi
		pts = append(pts, entity.Point3{X: cx + e.Radius*math.Cos(ang), Y: cy + e.Radius*math.Sin(ang), Z: 0})
	}
	return polylineStrokes(pts, t)
}

// tessUnderlay 底图引用框离散：裁剪多边形顶点按 scale/angle 变换后
// 平移到插入点（定义坐标系 → 世界坐标），闭合描边呈现引用范围。
func tessUnderlay(e *entity.EntUnderlay, t Xform) []Stroke {
	if len(e.ClipVerts) < 2 {
		return nil
	}
	ca, sa := math.Cos(e.Angle), math.Sin(e.Angle)
	pts := make([]entity.Point3, 0, len(e.ClipVerts)+1)
	for _, v := range e.ClipVerts {
		wx := v.X * e.Scale.X
		wy := v.Y * e.Scale.Y
		pts = append(pts, entity.Point3{X: e.InsPt.X + wx*ca - wy*sa, Y: e.InsPt.Y + wx*sa + wy*ca, Z: 0})
	}
	// 闭合多边形
	pts = append(pts, pts[0])
	return polylineStrokes(pts, t)
}

// catmullRomSpline 向心 Catmull-Rom 平滑（拟合点模式）。
func catmullRomSpline(points []entity.Point3, closed bool, segments int) []entity.Point3 {
	if len(points) < 2 {
		return points
	}
	if segments < 1 {
		segments = 1
	}
	var out []entity.Point3
	n := len(points)
	segCount := n
	if !closed {
		segCount = n - 1
	}
	at := func(i int) entity.Point3 {
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

// polylineStrokes 点列转折线段（应用变换）。
func polylineStrokes(pts []entity.Point3, t Xform) []Stroke {
	if len(pts) < 2 {
		return nil
	}
	var out []Stroke
	prev := t.Apply(entity.Point2{X: pts[0].X, Y: pts[0].Y})
	for _, p := range pts[1:] {
		cur := t.Apply(entity.Point2{X: p.X, Y: p.Y})
		out = append(out, Stroke{prev.X, prev.Y, cur.X, cur.Y})
		prev = cur
	}
	return out
}

func crDist(a, b entity.Point3) float64 {
	dx, dy, dz := a.X-b.X, a.Y-b.Y, a.Z-b.Z
	return math.Pow(dx*dx+dy*dy+dz*dz, 0.25) // 距离^alpha，alpha=0.5
}

func crPoint(p0, p1, p2, p3 entity.Point3, t0, t1, t2, t3, t float64) entity.Point3 {
	crLerp := func(a, b entity.Point3, ta, tb float64) entity.Point3 {
		if math.Abs(tb-ta) < 1e-12 {
			return a
		}
		w0 := (tb - t) / (tb - ta)
		w1 := (t - ta) / (tb - ta)
		return entity.Point3{X: w0*a.X + w1*b.X, Y: w0*a.Y + w1*b.Y, Z: w0*a.Z + w1*b.Z}
	}
	a1 := crLerp(p0, p1, t0, t1)
	a2 := crLerp(p1, p2, t1, t2)
	a3 := crLerp(p2, p3, t2, t3)
	b1 := crLerp(a1, a2, t0, t2)
	b2 := crLerp(a2, a3, t1, t3)
	return crLerp(b1, b2, t1, t2)
}
