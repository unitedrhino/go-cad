// coverage_gap_entities_test.go 补强 entities.go 的几何校验/评分/解码
// 分支：entityGeometryFinite 与 entityGeometryScore 全实体类型直调、
// R14 LINE 与 SPLINE（含 R2013+ 标志重算与双模式回退）合成位流。
package cad

import (
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/entity"
	"math"
	"testing"
)

// TestEntityGeometryFinite entityGeometryFinite 全实体类型分支。
func TestEntityGeometryFinite(t *testing.T) {
	ok := func(ent any) bool { return entity.EntityGeometryFinite(ent) }
	if !ok(&entity.EntLine{Start: entity.Point3{0, 0, 0}, End: entity.Point3{1, 1, 0}}) {
		t.Error("LINE 合法值应通过")
	}
	if ok(&entity.EntLine{Start: entity.Point3{math.Inf(1), 0, 0}}) {
		t.Error("LINE Inf 应拒绝")
	}
	if !ok(&entity.EntCircle{Center: entity.Point3{}, Radius: 5}) {
		t.Error("CIRCLE 合法值应通过")
	}
	if !ok(&entity.EntArc{Center: entity.Point3{}, Radius: 1, AngleStart: 0.1, AngleEnd: 1.1}) {
		t.Error("ARC 合法值应通过")
	}
	if ok(&entity.EntArc{AngleStart: 1e7}) {
		t.Error("ARC 天文角度应拒绝")
	}
	if !ok(&entity.EntPoint{Location: entity.Point3{1, 2, 3}}) {
		t.Error("POINT 合法值应通过")
	}
	if !ok(&entity.EntEllipse{Center: entity.Point3{}, MajorAxis: entity.Point3{1, 0, 0}, Ratio: 0.5}) {
		t.Error("ELLIPSE 合法值应通过")
	}
	if !ok(&entity.EntLwPolyline{Vertices: []entity.Point2{{0, 0}, {1, 1}}}) {
		t.Error("LWPOLYLINE 合法值应通过")
	}
	if ok(&entity.EntLwPolyline{}) {
		t.Error("LWPOLYLINE 空顶点应拒绝")
	}
	if !ok(&entity.EntText{Height: 2.5, Insertion: entity.Point3{}}) {
		t.Error("TEXT 合法值应通过")
	}
	if !ok(&entity.EntMText{TextHeight: 2.5, RectWidth: 10, Insertion: entity.Point3{}}) {
		t.Error("MTEXT 合法值应通过")
	}
	if !ok(&entity.EntInsert{Position: entity.Point3{}, Scale: entity.Point3{1, 1, 1}}) {
		t.Error("INSERT 合法值应通过")
	}
	if !ok(&entity.EntSpline{ControlPoints: []entity.Point3{{0, 0, 0}}}) {
		t.Error("SPLINE 控制点应通过")
	}
	if !ok(&entity.EntSpline{FitPoints: []entity.Point3{{0, 0, 0}}}) {
		t.Error("SPLINE 拟合点应通过")
	}
	if ok(&entity.EntSpline{}) {
		t.Error("SPLINE 无点应拒绝")
	}
	if !ok(&entity.EntDimension{}) {
		t.Error("DIMENSION 零值应通过")
	}
	if !ok(&entity.EntHatch{Paths: []entity.HatchPath{{Points: []entity.Point2{{1, 1}}}}}) {
		t.Error("HATCH 路径点应通过")
	}
	if ok(&entity.EntHatch{}) {
		t.Error("HATCH 无路径应拒绝")
	}
	if !ok(&entity.EntImage{}) {
		t.Error("IMAGE 零值应通过")
	}
	if !ok(&entity.EntOle2Frame{}) || !ok(&entity.EntOleFrame{}) {
		t.Error("OLE 框架应恒通过")
	}
	if !ok(&entity.EntProxyEntity{}) {
		t.Error("PROXY 应恒通过")
	}
	if !ok(&entity.EntMpolygon{Hatch: &entity.EntHatch{Paths: []entity.HatchPath{{Points: []entity.Point2{{1, 1}}}}}}) {
		t.Error("MPOLYGON 路径应通过")
	}
	if !ok(&entity.EntRay{Start: entity.Point3{}, UnitVector: entity.Point3{1, 0, 0}}) {
		t.Error("RAY 合法值应通过")
	}
	if !ok(&entity.EntSolid{}) {
		t.Error("SOLID 零值应通过")
	}
	if !ok(&entity.EntFace3d{}) {
		t.Error("3DFACE 零值应通过")
	}
	if !ok(&entity.EntLeader{Points: []entity.Point3{{0, 0, 0}}}) {
		t.Error("LEADER 点列应通过")
	}
	if !ok(&entity.EntMLine{Vertices: []entity.EntMLineVertex{{Position: entity.Point3{}}}}) {
		t.Error("MLINE 应通过")
	}
	if !ok(&entity.EntPolyline3d{}) || !ok(&entity.EntPolyline2d{}) {
		t.Error("POLYLINE 应通过")
	}
}

// TestEntityGeometryScore entityGeometryScore 全实体类型分支。
func TestEntityGeometryScore(t *testing.T) {
	if entity.EntityGeometryScore(&entity.EntLine{Start: entity.Point3{0, 0, 0}, End: entity.Point3{1, 1, 0}}) <= 0 {
		t.Error("LINE 合理几何应为正分")
	}
	if entity.EntityGeometryScore(&entity.EntCircle{Radius: 5, Center: entity.Point3{}}) <= 0 {
		t.Error("CIRCLE 合理几何应为正分")
	}
	// ARC 天文角度惩罚
	good := entity.EntityGeometryScore(&entity.EntArc{Radius: 5, Center: entity.Point3{}, AngleStart: 0, AngleEnd: 1})
	bad := entity.EntityGeometryScore(&entity.EntArc{Radius: 5, Center: entity.Point3{}, AngleStart: 1e7, AngleEnd: 1e8})
	if bad != good-40 {
		t.Errorf("ARC 天文角度应罚 40 分: good=%d bad=%d", good, bad)
	}
	if entity.EntityGeometryScore(&entity.EntPoint{Location: entity.Point3{}}) <= 0 {
		t.Error("POINT 应为正分")
	}
	if entity.EntityGeometryScore(&entity.EntEllipse{MajorAxis: entity.Point3{2, 0, 0}, Center: entity.Point3{}}) <= 0 {
		t.Error("ELLIPSE 应为正分")
	}
	if entity.EntityGeometryScore(&entity.EntLwPolyline{}) != -50 {
		t.Error("LWPOLYLINE 空顶点应为 -50")
	}
	if entity.EntityGeometryScore(&entity.EntLwPolyline{Vertices: []entity.Point2{{1, 1}}}) <= 0 {
		t.Error("LWPOLYLINE 合法顶点应为正分")
	}
	if entity.EntityGeometryScore(&entity.EntText{Height: 2.5}) <= 0 {
		t.Error("TEXT 合法高度应为正分")
	}
	if entity.EntityGeometryScore(&entity.EntText{Height: -1}) != -50 {
		t.Error("TEXT 非法高度应为 -50")
	}
	if entity.EntityGeometryScore(&entity.EntMText{TextHeight: 2.5, RectWidth: 10, Text: "LONG TEXT"}) <= 0 {
		t.Error("MTEXT 合法应为正分")
	}
	if entity.EntityGeometryScore(&entity.EntMText{TextHeight: 0, RectWidth: -1}) != -50 {
		t.Error("MTEXT 非法应为 -50")
	}
	if entity.EntityGeometryScore(&entity.EntInsert{Position: entity.Point3{1, 1, 1}}) <= 0 {
		t.Error("INSERT 应为正分")
	}
	if entity.EntityGeometryScore(&entity.EntAttrib{Height: 2.5, Text: "TAG"}) <= 0 {
		t.Error("ATTRIB 合法应为正分")
	}
	if entity.EntityGeometryScore(&entity.EntAttrib{Height: -1}) != -50 {
		t.Error("ATTRIB 非法应为 -50")
	}
	if entity.EntityGeometryScore("unknown") != 0 {
		t.Error("未知类型应为 0")
	}
	// pointScore/radiusScore 边界
	if entity.PointScore(entity.Point3{math.NaN(), 0, 0}) != -60 {
		t.Error("NaN 应罚 -60")
	}
	if entity.PointScore(entity.Point3{1e12, 0, 0}) != -60 {
		t.Error("超量级应罚 -60")
	}
	if entity.RadiusScore(0) != -60 || entity.RadiusScore(-1) != -60 {
		t.Error("非法半径应罚 -60")
	}
}

// TestDecodeLineR14 R13/R14 LINE 合成位流。
func TestDecodeLineR14(t *testing.T) {
	w := bitstream.NewEncWriter()
	w3bd(w, 0, 0, 0)  // start
	w3bd(w, 10, 0, 0) // end
	w.WriteB(true)    // BT flag → thickness 0
	w.WriteB(true)    // BE flag → (0,0,1)
	w.WriteRC(0)
	w.WriteRC(0)
	head := entity.CommonEntityHead{Handle: 0x11, ObjSizeBit: uint64(w.TellBits()) - 16}
	ent, err := entity.DecodeLineR14(bitstream.NewBitStream(w.Bytes()), &head)
	if err != nil {
		t.Fatalf("decodeLineR14 失败: %v", err)
	}
	line := ent.(*entity.EntLine)
	if line.Start.X != 0 || line.End.X != 10 {
		t.Errorf("端点不符: start=%v end=%v", line.Start, line.End)
	}
}

// TestDecodeSpline SPLINE 主入口：R2013+ 标志重算与双模式回退。
func TestDecodeSpline(t *testing.T) {
	// R2013+：flags&1 → scenario=2（拟合点模式）
	w := bitstream.NewEncWriter()
	w.WriteBL(1) // scenario 原值
	w.WriteBL(1) // splineFlags1（&1=1 → fit）
	w.WriteBL(0) // knotParameter
	w.WriteBL(3) // degree
	// fit 数据：容差 + 起末切线 + 拟合点
	w.WriteBD(0.001)
	w3bd(w, 1, 0, 0)
	w3bd(w, 0, 1, 0)
	w.WriteBL(2)
	w3bd(w, 0, 0, 0)
	w3bd(w, 4, 4, 0)
	// handle 流兜底
	for i := 0; i < 2; i++ {
		w.WriteRC(0)
	}
	head := entity.CommonEntityHead{Handle: 0x70, ObjSizeBit: uint64(w.TellBits()) - 16}
	ent, err := entity.DecodeSpline(bitstream.NewBitStream(w.Bytes()), &head, true)
	if err != nil {
		t.Fatalf("decodeSpline 失败: %v", err)
	}
	sp := ent.(*entity.EntSpline)
	if sp.Scenario != 2 || len(sp.FitPoints) != 2 {
		t.Errorf("R2013+ fit 重算不符: scenario=%d fit=%d", sp.Scenario, len(sp.FitPoints))
	}

	// 控制点模式：knotParameter=15 → scenario=1
	w2 := bitstream.NewEncWriter()
	w2.WriteBL(2)  // scenario 原值
	w2.WriteBL(0)  // splineFlags1
	w2.WriteBL(15) // knotParameter=15 → 控制点
	w2.WriteBL(2)  // degree
	// control 数据：3B 标志 + 2 容差 + 数量 + weight 位 + knots + ctrl
	w2.WriteB(false)
	w2.WriteB(false)
	w2.WriteB(false)
	w2.WriteBD(0.01)
	w2.WriteBD(0.01)
	w2.WriteBL(3)
	w2.WriteBL(2)
	w2.WriteB(false)
	w2.WriteBD(0)
	w2.WriteBD(0.5)
	w2.WriteBD(1)
	w3bd(w2, 0, 0, 0)
	w3bd(w2, 2, 2, 0)
	for i := 0; i < 2; i++ {
		w2.WriteRC(0)
	}
	head2 := entity.CommonEntityHead{Handle: 0x71, ObjSizeBit: uint64(w2.TellBits()) - 16}
	ent2, err := entity.DecodeSpline(bitstream.NewBitStream(w2.Bytes()), &head2, true)
	if err != nil {
		t.Fatalf("decodeSpline 控制点模式失败: %v", err)
	}
	sp2 := ent2.(*entity.EntSpline)
	if sp2.Scenario != 1 || len(sp2.ControlPoints) != 2 {
		t.Errorf("控制点模式不符: scenario=%d ctrl=%d", sp2.Scenario, len(sp2.ControlPoints))
	}
}
