// coverage_gap_entities_test.go 补强 entities.go 的几何校验/评分/解码
// 分支：entityGeometryFinite 与 entityGeometryScore 全实体类型直调、
// R14 LINE 与 SPLINE（含 R2013+ 标志重算与双模式回退）合成位流。
package cad

import (
	"math"
	"testing"
)

// TestEntityGeometryFinite entityGeometryFinite 全实体类型分支。
func TestEntityGeometryFinite(t *testing.T) {
	ok := func(ent any) bool { return entityGeometryFinite(ent) }
	if !ok(&entLine{start: point3{0, 0, 0}, end: point3{1, 1, 0}}) {
		t.Error("LINE 合法值应通过")
	}
	if ok(&entLine{start: point3{math.Inf(1), 0, 0}}) {
		t.Error("LINE Inf 应拒绝")
	}
	if !ok(&entCircle{center: point3{}, radius: 5}) {
		t.Error("CIRCLE 合法值应通过")
	}
	if !ok(&entArc{center: point3{}, radius: 1, angleStart: 0.1, angleEnd: 1.1}) {
		t.Error("ARC 合法值应通过")
	}
	if ok(&entArc{angleStart: 1e7}) {
		t.Error("ARC 天文角度应拒绝")
	}
	if !ok(&entPoint{location: point3{1, 2, 3}}) {
		t.Error("POINT 合法值应通过")
	}
	if !ok(&entEllipse{center: point3{}, majorAxis: point3{1, 0, 0}, ratio: 0.5}) {
		t.Error("ELLIPSE 合法值应通过")
	}
	if !ok(&entLwPolyline{vertices: []point2{{0, 0}, {1, 1}}}) {
		t.Error("LWPOLYLINE 合法值应通过")
	}
	if ok(&entLwPolyline{}) {
		t.Error("LWPOLYLINE 空顶点应拒绝")
	}
	if !ok(&entText{height: 2.5, insertion: point3{}}) {
		t.Error("TEXT 合法值应通过")
	}
	if !ok(&entMText{textHeight: 2.5, rectWidth: 10, insertion: point3{}}) {
		t.Error("MTEXT 合法值应通过")
	}
	if !ok(&entInsert{position: point3{}, scale: point3{1, 1, 1}}) {
		t.Error("INSERT 合法值应通过")
	}
	if !ok(&entSpline{controlPoints: []point3{{0, 0, 0}}}) {
		t.Error("SPLINE 控制点应通过")
	}
	if !ok(&entSpline{fitPoints: []point3{{0, 0, 0}}}) {
		t.Error("SPLINE 拟合点应通过")
	}
	if ok(&entSpline{}) {
		t.Error("SPLINE 无点应拒绝")
	}
	if !ok(&entDimension{}) {
		t.Error("DIMENSION 零值应通过")
	}
	if !ok(&entHatch{paths: []hatchPath{{points: []point2{{1, 1}}}}}) {
		t.Error("HATCH 路径点应通过")
	}
	if ok(&entHatch{}) {
		t.Error("HATCH 无路径应拒绝")
	}
	if !ok(&entImage{}) {
		t.Error("IMAGE 零值应通过")
	}
	if !ok(&entOle2Frame{}) || !ok(&entOleFrame{}) {
		t.Error("OLE 框架应恒通过")
	}
	if !ok(&entProxyEntity{}) {
		t.Error("PROXY 应恒通过")
	}
	if !ok(&entMpolygon{hatch: &entHatch{paths: []hatchPath{{points: []point2{{1, 1}}}}}}) {
		t.Error("MPOLYGON 路径应通过")
	}
	if !ok(&entRay{start: point3{}, unitVector: point3{1, 0, 0}}) {
		t.Error("RAY 合法值应通过")
	}
	if !ok(&entSolid{}) {
		t.Error("SOLID 零值应通过")
	}
	if !ok(&entFace3d{}) {
		t.Error("3DFACE 零值应通过")
	}
	if !ok(&entLeader{points: []point3{{0, 0, 0}}}) {
		t.Error("LEADER 点列应通过")
	}
	if !ok(&entMLine{vertices: []entMLineVertex{{position: point3{}}}}) {
		t.Error("MLINE 应通过")
	}
	if !ok(&entPolyline3d{}) || !ok(&entPolyline2d{}) {
		t.Error("POLYLINE 应通过")
	}
}

// TestEntityGeometryScore entityGeometryScore 全实体类型分支。
func TestEntityGeometryScore(t *testing.T) {
	if entityGeometryScore(&entLine{start: point3{0, 0, 0}, end: point3{1, 1, 0}}) <= 0 {
		t.Error("LINE 合理几何应为正分")
	}
	if entityGeometryScore(&entCircle{radius: 5, center: point3{}}) <= 0 {
		t.Error("CIRCLE 合理几何应为正分")
	}
	// ARC 天文角度惩罚
	good := entityGeometryScore(&entArc{radius: 5, center: point3{}, angleStart: 0, angleEnd: 1})
	bad := entityGeometryScore(&entArc{radius: 5, center: point3{}, angleStart: 1e7, angleEnd: 1e8})
	if bad != good-40 {
		t.Errorf("ARC 天文角度应罚 40 分: good=%d bad=%d", good, bad)
	}
	if entityGeometryScore(&entPoint{location: point3{}}) <= 0 {
		t.Error("POINT 应为正分")
	}
	if entityGeometryScore(&entEllipse{majorAxis: point3{2, 0, 0}, center: point3{}}) <= 0 {
		t.Error("ELLIPSE 应为正分")
	}
	if entityGeometryScore(&entLwPolyline{}) != -50 {
		t.Error("LWPOLYLINE 空顶点应为 -50")
	}
	if entityGeometryScore(&entLwPolyline{vertices: []point2{{1, 1}}}) <= 0 {
		t.Error("LWPOLYLINE 合法顶点应为正分")
	}
	if entityGeometryScore(&entText{height: 2.5}) <= 0 {
		t.Error("TEXT 合法高度应为正分")
	}
	if entityGeometryScore(&entText{height: -1}) != -50 {
		t.Error("TEXT 非法高度应为 -50")
	}
	if entityGeometryScore(&entMText{textHeight: 2.5, rectWidth: 10, text: "LONG TEXT"}) <= 0 {
		t.Error("MTEXT 合法应为正分")
	}
	if entityGeometryScore(&entMText{textHeight: 0, rectWidth: -1}) != -50 {
		t.Error("MTEXT 非法应为 -50")
	}
	if entityGeometryScore(&entInsert{position: point3{1, 1, 1}}) <= 0 {
		t.Error("INSERT 应为正分")
	}
	if entityGeometryScore(&entAttrib{height: 2.5, text: "TAG"}) <= 0 {
		t.Error("ATTRIB 合法应为正分")
	}
	if entityGeometryScore(&entAttrib{height: -1}) != -50 {
		t.Error("ATTRIB 非法应为 -50")
	}
	if entityGeometryScore("unknown") != 0 {
		t.Error("未知类型应为 0")
	}
	// pointScore/radiusScore 边界
	if pointScore(point3{math.NaN(), 0, 0}) != -60 {
		t.Error("NaN 应罚 -60")
	}
	if pointScore(point3{1e12, 0, 0}) != -60 {
		t.Error("超量级应罚 -60")
	}
	if radiusScore(0) != -60 || radiusScore(-1) != -60 {
		t.Error("非法半径应罚 -60")
	}
}

// TestDecodeLineR14 R13/R14 LINE 合成位流。
func TestDecodeLineR14(t *testing.T) {
	w := newEncWriter()
	w3bd(w, 0, 0, 0)  // start
	w3bd(w, 10, 0, 0) // end
	w.writeB(true)    // BT flag → thickness 0
	w.writeB(true)    // BE flag → (0,0,1)
	w.writeRC(0)
	w.writeRC(0)
	head := commonEntityHead{handle: 0x11, objSizeBit: uint64(w.tellBits()) - 16}
	ent, err := decodeLineR14(newBitStream(w.bytes()), &head)
	if err != nil {
		t.Fatalf("decodeLineR14 失败: %v", err)
	}
	line := ent.(*entLine)
	if line.start.x != 0 || line.end.x != 10 {
		t.Errorf("端点不符: start=%v end=%v", line.start, line.end)
	}
}

// TestDecodeSpline SPLINE 主入口：R2013+ 标志重算与双模式回退。
func TestDecodeSpline(t *testing.T) {
	// R2013+：flags&1 → scenario=2（拟合点模式）
	w := newEncWriter()
	w.writeBL(1) // scenario 原值
	w.writeBL(1) // splineFlags1（&1=1 → fit）
	w.writeBL(0) // knotParameter
	w.writeBL(3) // degree
	// fit 数据：容差 + 起末切线 + 拟合点
	w.writeBD(0.001)
	w3bd(w, 1, 0, 0)
	w3bd(w, 0, 1, 0)
	w.writeBL(2)
	w3bd(w, 0, 0, 0)
	w3bd(w, 4, 4, 0)
	// handle 流兜底
	for i := 0; i < 2; i++ {
		w.writeRC(0)
	}
	head := commonEntityHead{handle: 0x70, objSizeBit: uint64(w.tellBits()) - 16}
	ent, err := decodeSpline(newBitStream(w.bytes()), &head, true)
	if err != nil {
		t.Fatalf("decodeSpline 失败: %v", err)
	}
	sp := ent.(*entSpline)
	if sp.scenario != 2 || len(sp.fitPoints) != 2 {
		t.Errorf("R2013+ fit 重算不符: scenario=%d fit=%d", sp.scenario, len(sp.fitPoints))
	}

	// 控制点模式：knotParameter=15 → scenario=1
	w2 := newEncWriter()
	w2.writeBL(2)  // scenario 原值
	w2.writeBL(0)  // splineFlags1
	w2.writeBL(15) // knotParameter=15 → 控制点
	w2.writeBL(2)  // degree
	// control 数据：3B 标志 + 2 容差 + 数量 + weight 位 + knots + ctrl
	w2.writeB(false)
	w2.writeB(false)
	w2.writeB(false)
	w2.writeBD(0.01)
	w2.writeBD(0.01)
	w2.writeBL(3)
	w2.writeBL(2)
	w2.writeB(false)
	w2.writeBD(0)
	w2.writeBD(0.5)
	w2.writeBD(1)
	w3bd(w2, 0, 0, 0)
	w3bd(w2, 2, 2, 0)
	for i := 0; i < 2; i++ {
		w2.writeRC(0)
	}
	head2 := commonEntityHead{handle: 0x71, objSizeBit: uint64(w2.tellBits()) - 16}
	ent2, err := decodeSpline(newBitStream(w2.bytes()), &head2, true)
	if err != nil {
		t.Fatalf("decodeSpline 控制点模式失败: %v", err)
	}
	sp2 := ent2.(*entSpline)
	if sp2.scenario != 1 || len(sp2.controlPoints) != 2 {
		t.Errorf("控制点模式不符: scenario=%d ctrl=%d", sp2.scenario, len(sp2.controlPoints))
	}
}
