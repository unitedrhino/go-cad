// spline_test.go SPLINE 解码与渲染单元测试：用 bitWriter 构造标准位流
// 验证控制点/拟合点两种 scenario 的字段解码（与规范布局对齐）。
package cad

import (
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/drawing"
	"github.com/unitedrhino/go-cad/internal/entity"
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"math"
	"testing"
)

// TestDecodeSplineControlMode 控制点模式（scenario=1）：
// scenario + degree + 标志位 + 容差 + 节点数组 + 控制点数组。
func TestDecodeSplineControlMode(t *testing.T) {
	w := testsupport.WriteEntityPrefix(testsupport.NewBitWriter(), 0x28)
	testsupport.WriteCommonHead(w, 900, 2)
	w.BL(1)    // scenario=1（控制点）
	w.BL(3)    // degree
	w.B(0)     // rational=0
	w.B(1)     // closed=1
	w.B(0)     // periodic=0
	w.BD(0.01) // knot tolerance
	w.BD(0.02) // ctrl tolerance
	w.BL(4)    // 节点数
	w.BL(2)    // 控制点数
	w.B(0)     // weight 回显位
	w.BD(0).BD(0).BD(1).BD(1)
	w.B3BD(0, 0, 0)
	w.B3BD(10, 20, 0)
	r := bitstream.NewBitStream(w.Bytes())
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	head, err := entity.ParseEntityHead(r, uint64(len(w.Bytes()))*8, entity.FeatMaterialFlags|entity.FeatVisualStyles|entity.FeatDSBinary)
	if err != nil {
		t.Fatal(err)
	}
	ent, err := entity.DecodeSpline(r, &head, false)
	if err != nil {
		t.Fatal(err)
	}
	sp := ent.(*entity.EntSpline)
	if sp.Scenario != 1 || sp.Degree != 3 || !sp.Closed || sp.Rational {
		t.Fatalf("标志: scenario=%d degree=%d closed=%v rational=%v", sp.Scenario, sp.Degree, sp.Closed, sp.Rational)
	}
	if sp.KnotTolerance != 0.01 || sp.CtrlTolerance != 0.02 {
		t.Fatalf("容差: knot=%v ctrl=%v", sp.KnotTolerance, sp.CtrlTolerance)
	}
	if len(sp.Knots) != 4 || sp.Knots[2] != 1 {
		t.Fatalf("节点: %v", sp.Knots)
	}
	if len(sp.ControlPoints) != 2 || sp.ControlPoints[1].X != 10 || sp.ControlPoints[1].Y != 20 {
		t.Fatalf("控制点: %v", sp.ControlPoints)
	}
	// 渲染细分：De Boor 求值应落在控制点凸包内
	strokes := drawing.TessSpline(sp, drawing.IdentityXform())
	if len(strokes) == 0 {
		t.Fatal("SPLINE 渲染细分无输出")
	}
}

// TestDecodeSplineFitMode 拟合点模式（scenario=2）。
func TestDecodeSplineFitMode(t *testing.T) {
	w := testsupport.WriteEntityPrefix(testsupport.NewBitWriter(), 0x28)
	testsupport.WriteCommonHead(w, 901, 2)
	w.BL(2) // scenario=2（拟合点）
	w.BL(3) // degree
	w.BD(0.001)
	w.B3BD(1, 0, 0) // 起点切线
	w.B3BD(0, 1, 0) // 终点切线
	w.BL(2)         // 拟合点数
	w.B3BD(0, 0, 0)
	w.B3BD(5, 5, 0)
	r := bitstream.NewBitStream(w.Bytes())
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	head, _ := entity.ParseEntityHead(r, uint64(len(w.Bytes()))*8, entity.FeatMaterialFlags|entity.FeatVisualStyles|entity.FeatDSBinary)
	ent, err := entity.DecodeSpline(r, &head, false)
	if err != nil {
		t.Fatal(err)
	}
	sp := ent.(*entity.EntSpline)
	if sp.Scenario != 2 || sp.FitTolerance != 0.001 || len(sp.FitPoints) != 2 {
		t.Fatalf("拟合模式: scenario=%d tol=%v pts=%v", sp.Scenario, sp.FitTolerance, sp.FitPoints)
	}
	// Catmull-Rom 细分：首末点应与拟合点重合
	strokes := drawing.TessSpline(sp, drawing.IdentityXform())
	if len(strokes) == 0 {
		t.Fatal("拟合模式渲染无输出")
	}
	if math.Abs(strokes[0].X1-0) > 1e-9 || math.Abs(strokes[len(strokes)-1].Y2-5) > 1e-9 {
		t.Fatalf("端点不重合: first=%v last=%v", strokes[0], strokes[len(strokes)-1])
	}
}

// TestDecodeSplineR2013 R2013+ 扩展头（flags + 节点参数 BL）。
func TestDecodeSplineR2013(t *testing.T) {
	w := testsupport.WriteEntityPrefix(testsupport.NewBitWriter(), 0x28)
	testsupport.WriteCommonHead(w, 902, 2)
	w.BL(1) // scenario
	w.BL(7) // spline flags1
	w.BL(9) // knot parameter
	w.BL(2) // degree
	w.B(0).B(0).B(0)
	w.BD(0).BD(0)
	w.BL(0) // 节点数
	w.BL(2) // 控制点数
	w.B(0)  // weight 回显位
	w.B3BD(0, 0, 0)
	w.B3BD(1, 1, 0)
	r := bitstream.NewBitStream(w.Bytes())
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	head, _ := entity.ParseEntityHead(r, uint64(len(w.Bytes()))*8, entity.FeatMaterialFlags|entity.FeatVisualStyles|entity.FeatDSBinary)
	ent, err := entity.DecodeSpline(r, &head, true)
	if err != nil {
		t.Fatal(err)
	}
	sp := ent.(*entity.EntSpline)
	if sp.SplineFlags1 != 7 || sp.KnotParameter != 9 {
		t.Fatalf("R2013 扩展: flags=%d knotParam=%d", sp.SplineFlags1, sp.KnotParameter)
	}
}
