// render_full_test.go 渲染层单元测试：tessellation、过滤、鲁棒视口、颜色解析。
package cad

import (
	"github.com/unitedrhino/go-cad/internal/drawing"
	"github.com/unitedrhino/go-cad/internal/entity"
	"math"
	"testing"
)

func TestTessCircleCloses(t *testing.T) {
	strokes := drawing.TessCircle(0, 0, 10, drawing.IdentityXform())
	if len(strokes) != 72 {
		t.Fatalf("圆期望 72 段得到 %d", len(strokes))
	}
	// 首尾相接：最后一段终点接近起点
	last := strokes[len(strokes)-1]
	if math.Abs(last.X2-10) > 0.1 || math.Abs(last.Y2) > 0.1 {
		t.Fatalf("圆未闭合: end=(%v,%v)", last.X2, last.Y2)
	}
	// 带变换：半径缩放
	strokes2 := drawing.TessCircle(0, 0, 10, drawing.Xform{Sx: 2, Sy: 2, Cos: 1})
	first := strokes2[0]
	if math.Abs(first.X1-20) > 0.1 {
		t.Fatalf("缩放圆首点: %v", first.X1)
	}
}

func TestTessArcSpan(t *testing.T) {
	// 90° 弧
	strokes := drawing.TessArc(0, 0, 5, 0, math.Pi/2, drawing.IdentityXform())
	if len(strokes) == 0 {
		t.Fatal("弧应产生线段")
	}
	// 起点在 (5,0)
	if math.Abs(strokes[0].X1-5) > 0.01 || math.Abs(strokes[0].Y1) > 0.01 {
		t.Fatalf("弧起点: (%v,%v)", strokes[0].X1, strokes[0].Y1)
	}
	// 终点在 (0,5)
	last := strokes[len(strokes)-1]
	if math.Abs(last.X2) > 0.01 || math.Abs(last.Y2-5) > 0.01 {
		t.Fatalf("弧终点: (%v,%v)", last.X2, last.Y2)
	}
	// 负跨度（a1 < a0）自动跨零
	strokes2 := drawing.TessArc(0, 0, 5, math.Pi/2, 0, drawing.IdentityXform())
	if len(strokes2) == 0 {
		t.Fatal("负跨度弧应产生线段")
	}
}

func TestTessLwPolylineLines(t *testing.T) {
	e := &entity.EntLwPolyline{Vertices: []entity.Point2{{0, 0}, {10, 0}, {10, 10}}}
	strokes := drawing.TessLwPolyline(e, drawing.IdentityXform())
	// 2 段直线
	if len(strokes) != 2 {
		t.Fatalf("折线期望 2 段得到 %d", len(strokes))
	}
	if strokes[0].X1 != 0 || strokes[0].Y1 != 0 || strokes[0].X2 != 10 {
		t.Fatalf("首段错误: %+v", strokes[0])
	}
}

func TestTessLwPolylineClosedByGeometry(t *testing.T) {
	// 首尾重合 → 几何闭合：最后一段 (10,0)→(0,0) 已回到起点，无需回连段
	e := &entity.EntLwPolyline{Vertices: []entity.Point2{{0, 0}, {10, 0}, {0, 0}}}
	strokes := drawing.TessLwPolyline(e, drawing.IdentityXform())
	if len(strokes) != 2 {
		t.Fatalf("闭合折线期望 2 段得到 %d", len(strokes))
	}
	last := strokes[len(strokes)-1]
	if last.X1 != 10 || last.X2 != 0 || last.Y2 != 0 {
		t.Fatalf("闭合末段错误: %+v", last)
	}
}

func TestTessLwPolylineBulgeArc(t *testing.T) {
	// bulge=1 → 半圆弧（圆心角 180°），弦 (0,0)-(10,0)
	e := &entity.EntLwPolyline{
		Vertices: []entity.Point2{{0, 0}, {10, 0}},
		Bulges:   []float64{1},
	}
	strokes := drawing.TessLwPolyline(e, drawing.IdentityXform())
	if len(strokes) == 0 {
		t.Fatal("凸度段应产生弧线段")
	}
	// 参考实现语义：bulge=1 从 (0,0) 到 (10,0) 的半圆凸向 -y（圆心在弦中点、
	// 角度从 π 递增到 2π），最低点 y≈-5
	minY := 0.0
	for _, s := range strokes {
		if s.Y1 < minY {
			minY = s.Y1
		}
		if s.Y2 < minY {
			minY = s.Y2
		}
	}
	if minY < -5.9 || minY > -4.9 {
		t.Fatalf("bulge=1 半圆最低点应≈-5，得到 %v", minY)
	}
}

func TestTessEllipse(t *testing.T) {
	e := &entity.EntEllipse{
		Center:    entity.Point3{X: 0, Y: 0, Z: 0},
		MajorAxis: entity.Point3{X: 10, Y: 0, Z: 0},
		Ratio:     0.5,
		StartAng:  0,
		EndAng:    2 * math.Pi,
	}
	strokes := drawing.TessEllipse(e, drawing.IdentityXform())
	if len(strokes) == 0 {
		t.Fatal("椭圆应产生线段")
	}
	// 长轴端点 (10,0) 短轴端点 (0,5)
	hasMajorEnd, hasMinorEnd := false, false
	for _, s := range strokes {
		if entity.Near(s.X1, 10) && entity.Near(s.Y1, 0) {
			hasMajorEnd = true
		}
		if entity.Near(s.X1, 0) && entity.Near(s.Y1, 5) {
			hasMinorEnd = true
		}
	}
	if !hasMajorEnd || !hasMinorEnd {
		t.Fatalf("椭圆端点缺失: major=%v minor=%v", hasMajorEnd, hasMinorEnd)
	}
}

func TestFilterRadiatingStrokes(t *testing.T) {
	// 同锚点 20 条放射线（阈值 12）应被剔除；普通线保留
	var prims []drawing.Primitive
	for i := 0; i < 20; i++ {
		prims = append(prims, drawing.Primitive{Kind: 0, Strokes: []drawing.Stroke{{0, 0, float64(i + 1), 100}}})
	}
	prims = append(prims, drawing.Primitive{Kind: 0, Strokes: []drawing.Stroke{{50, 50, 60, 60}}})
	out := filterRadiatingStrokes(prims)
	if len(out) != 1 {
		t.Fatalf("放射过滤后期望 1 个图元得到 %d", len(out))
	}
}

func TestDropOriginAnchored(t *testing.T) {
	bbox := drawing.Box2{0, 0, 100, 100}
	prims := []drawing.Primitive{
		// 视口中心 (50,50) 锚定、远端超出 1.5 倍视口 → 剔除
		{Kind: 0, Strokes: []drawing.Stroke{{50, 50, 5000, 5000}}},
		{Kind: 0, Strokes: []drawing.Stroke{{10, 10, 50, 50}}}, // 视口内 → 保留
		{Kind: 1, Lb: drawing.Label{X: 30, Y: 30, W: 5, H: 2}}, // 文字 → 保留
	}
	out := dropOriginAnchored(prims, bbox)
	if len(out) != 2 {
		t.Fatalf("原点锚定过滤后期望 2 个得到 %d", len(out))
	}
}

func TestRobustBounds(t *testing.T) {
	// 大量正常点 + 少量离群点 → 离群点不影响视口
	var prims []drawing.Primitive
	for i := 0; i < 100; i++ {
		prims = append(prims, drawing.Primitive{Kind: 0, Strokes: []drawing.Stroke{
			{float64(i), float64(i), float64(i) + 1, float64(i) + 1}}})
	}
	prims = append(prims, drawing.Primitive{Kind: 0, Strokes: []drawing.Stroke{{1e6, 1e6, 1e6, 1e6}}})
	b := drawing.RobustBounds(prims)
	// 离群点 (1e6,1e6) 不得撑爆视口：范围应保持在正常数据量级（0~99）附近
	if b.MaxX > 1000 || b.MinX < -1000 {
		t.Fatalf("鲁棒视口应抗离群点: [%v,%v]", b.MinX, b.MaxX)
	}
	if b.MaxX < 50 || b.MinX > 50 {
		t.Fatalf("鲁棒视口应覆盖主体数据: [%v,%v]", b.MinX, b.MaxX)
	}
}

func TestMedianAndQuantile(t *testing.T) {
	if drawing.Median([]float64{3, 1, 2}) != 2 {
		t.Fatal("奇数中位数错误")
	}
	if drawing.Median([]float64{4, 1, 2, 3}) != 2.5 {
		t.Fatal("偶数中位数错误")
	}
}

func TestEntityColorPriority(t *testing.T) {
	doc := &Document{LayerColors: map[uint64]drawing.LayerColor{
		10: {Index: 5, HasTrue: false},
	}}
	// true color 优先
	e := &drawing.Primitive{Color: entity.EntColor{HasTrue: true, TrueColor: 0xFF0000}, Layer: 10}
	c := entityColor(doc, e, true)
	if c.R != 255 || c.G != 0 || c.B != 0 {
		t.Fatalf("true color 优先失败: %v", c)
	}
	// 实体 ACI
	e2 := &drawing.Primitive{Color: entity.EntColor{HasIndex: true, Index: 1}, Layer: 10}
	c2 := entityColor(doc, e2, true)
	if c2.R != 255 || c2.G != 0 {
		t.Fatalf("实体 ACI 失败: %v", c2)
	}
	// 图层 ACI 继承（ACI 5 = 蓝）
	e3 := &drawing.Primitive{Color: entity.EntColor{}, Layer: 10}
	c3 := entityColor(doc, e3, true)
	if c3.R != 0 || c3.G != 0 || c3.B != 255 {
		t.Fatalf("图层 ACI 继承失败: %v", c3)
	}
	// 默认黑
	e4 := &drawing.Primitive{Color: entity.EntColor{}, Layer: 999}
	c4 := entityColor(doc, e4, true)
	if c4.R != 0 || c4.G != 0 || c4.B != 0 {
		t.Fatalf("默认黑失败: %v", c4)
	}
}

func TestPrimitivesBounds(t *testing.T) {
	prims := []drawing.Primitive{
		{Kind: 0, Strokes: []drawing.Stroke{{0, 0, 10, 10}}},
		{Kind: 1, Lb: drawing.Label{X: -5, Y: -5, W: 2, H: 1}},
	}
	b := drawing.PrimitivesBounds(prims)
	if b.MinX != -5 || b.MinY != -5 || b.MaxX != 10 || b.MaxY != 10 {
		t.Fatalf("包围盒错误: %+v", b)
	}
	// 空 → invalid
	var empty []drawing.Primitive
	if !drawing.PrimitivesBounds(empty).Invalid() {
		t.Fatal("空包围盒应 invalid")
	}
}

func TestInsertXformAndCompose(t *testing.T) {
	// INSERT：插入点 (10,20)、旋转 90°、缩放 2 → 局部点 (1,0) → (10+0, 20+2)=(10,22)
	ins := &entity.EntInsert{
		Position: entity.Point3{X: 10, Y: 20, Z: 0},
		Scale:    entity.Point3{X: 2, Y: 2, Z: 2},
		Rotation: math.Pi / 2,
	}
	xf := drawing.InsertXform(ins)
	p := xf.Apply(entity.Point2{X: 1, Y: 0})
	if math.Abs(p.X-10) > 1e-9 || math.Abs(p.Y-22) > 1e-9 {
		t.Fatalf("INSERT 变换: (%v,%v) 期望 (10,22)", p.X, p.Y)
	}
}

func TestTextLabelWidth(t *testing.T) {
	ts := drawing.NewTessellator(&Document{})
	e := &entity.EntText{BaseEntity: entity.BaseEntity{}, Text: "ABCD", Insertion: entity.Point3{X: 0, Y: 0, Z: 0}, Height: 2}
	prim := ts.TextLabel(0, 0, 2, 0, 4, drawing.IdentityXform(), e, "TEXT", e.Text, 0)
	if prim.Kind != 1 {
		t.Fatal("文字应产生 label 图元")
	}
	// 宽度 = 4 字符 × 2 字高 × 0.8
	if math.Abs(prim.Lb.W-4*2*0.8) > 1e-9 {
		t.Fatalf("文字占位宽=%v", prim.Lb.W)
	}
}

func TestCanvasToPixelYFlip(t *testing.T) {
	imgW, imgH := 100.0, 50.0
	cv := &canvas{}
	cv.setTransform(drawing.Box2{MinX: 0, MinY: 0, MaxX: 100, MaxY: 50}, 1)
	if cv.scale != 1 {
		t.Fatal("scale 应为 1")
	}
	_ = imgW
	_ = imgH
	px, py := cv.toPixel(entity.Point2{X: 0, Y: 50}) // 世界顶部 → 像素顶部
	if px != 0 || py != 0 {
		t.Fatalf("Y 翻转错误: (%v,%v)", px, py)
	}
	px, py = cv.toPixel(entity.Point2{X: 100, Y: 0}) // 世界底部 → 像素底部
	if px != 100 || py != 50 {
		t.Fatalf("像素换算错误: (%v,%v)", px, py)
	}
}

func TestPlausible(t *testing.T) {
	if !drawing.Plausible(0, 0) || !drawing.Plausible(1e6, -1e6) {
		t.Error("工程量级坐标应合理")
	}
	if drawing.Plausible(1e8, 0) || drawing.Plausible(math.NaN(), 0) || drawing.Plausible(0, math.Inf(1)) {
		t.Error("天文数字/NaN/Inf 应不合理")
	}
}
