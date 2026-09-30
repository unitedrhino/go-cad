// entities_dimension_test.go / hatch 单元测试：bitWriter 构造标准位流，
// 验证 DIMENSION 与 HATCH 的字段解码（与参考布局逐位对齐）。
package entity

import (
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"math"
	"testing"
)

// TestDecodeDimLinearR2018 R2018 DIM_LINEAR：版本字节 + 3BD 挤出 + 中点 + ...
func TestDecodeDimLinearR2018(t *testing.T) {
	w := testsupport.WriteEntityPrefix(testsupport.NewBitWriter(), 0x15)
	testsupport.WriteCommonHead(w, 700, 2)
	w.RC(1)             // dimension version（变体 hasDimensionVersion）
	w.B3BD(0, 0, 1)     // extrusion（3BD，非 BE）
	w.RD(100).RD(50)    // text midpoint x/y
	w.BD(0)             // elevation
	w.RC(0)             // dim flags
	w.TV("")            // 用户文字
	w.BD(0)             // text rotation
	w.BD(0)             // horizontal direction
	w.BD(1).BD(1).BD(1) // insert scale
	w.BD(0)             // insert rotation
	w.BS(1)             // attachment
	w.BS(1)             // line spacing style
	w.BD(1)             // line spacing factor
	w.BD(25.0)          // actual measurement
	w.B(0).B(0).B(0)    // R2007+ flags
	w.RD(10).RD(20)     // 12-pt
	// LINEAR 专属：13-pt + 14-pt + 10-pt + 两个旋转
	w.B3BD(10, 10, 0)
	w.B3BD(110, 10, 0)
	w.B3BD(30, 50, 0)
	w.BD(0)
	w.BD(0)
	r := bitstream.NewBitStream(w.Bytes())
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	Head, err := ParseEntityHead(r, 0, FeatMaterialFlags|FeatVisualStyles|FeatDSBinary)
	if err != nil {
		t.Fatal(err)
	}
	ent, err := DecodeDimension(r, &Head, container.VerR2018, DimLayoutLinear)
	if err != nil {
		t.Fatal(err)
	}
	d := ent.(*EntDimension)
	if d.Point13.X != 10 || d.Point13.Y != 10 || d.Point14.X != 110 || d.Point10.X != 30 {
		t.Fatalf("点: p13=%v p14=%v p10=%v", d.Point13, d.Point14, d.Point10)
	}
	if math.Abs(d.ActualMeasurement-25.0) > 1e-9 {
		t.Fatalf("测量值: %v", d.ActualMeasurement)
	}
	if d.InsertPoint.X != 10 || d.InsertPoint.Y != 20 {
		t.Fatalf("12-pt: %v", d.InsertPoint)
	}
}

// TestDecodeHatchPolylinePath HATCH 多段线路径。
func TestDecodeHatchPolylinePath(t *testing.T) {
	w := testsupport.WriteEntityPrefix(testsupport.NewBitWriter(), 0x4F)
	testsupport.WriteCommonHead(w, 800, 2)
	// R2004+ 渐变段（无渐变填充：固定字段 + 2 个默认色；R2007+ 渐变名在字符串区）
	w.BL(0) // is_gradient_fill
	w.BL(0) // reserved
	w.BD(0) // gradient_angle
	w.BD(0) // gradient_shift
	w.BL(0) // single_color_gradient
	w.BD(0) // gradient_tint
	w.BL(2) // num_colors
	for i := 0; i < 2; i++ {
		w.BD(0) // shift value
		w.BS(0) // CMC index
		w.BL(0) // CMC rgb
		w.RC(0) // CMC flag
	}
	w.BD(0)         // elevation
	w.B3BD(0, 0, 1) // extrusion
	// 图案名：R2018 在字符串区，主数据流零占位
	w.B(1)  // solid fill
	w.B(0)  // associative
	w.BL(1) // 路径数
	w.BL(2) // path flag：多段线
	w.B(0)  // 无凸度
	w.B(1)  // 闭合
	w.BL(3) // 顶点数
	w.RD(0).RD(0)
	w.RD(100).RD(0)
	w.RD(100).RD(80)
	w.BL(0) // 边界句柄数
	// 图案定义段（solid fill → 只有两个 BS）+ 种子点数（无条件 BL）
	w.BS(0)
	w.BS(0)
	w.BL(0)
	r := bitstream.NewBitStream(w.Bytes())
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	Head, err := ParseEntityHead(r, 0, FeatMaterialFlags|FeatVisualStyles|FeatDSBinary)
	if err != nil {
		t.Fatal(err)
	}
	ent, err := decodeHatch(r, &Head, container.VerR2018, 30)
	if err != nil {
		t.Fatal(err)
	}
	h := ent.(*EntHatch)
	if !h.SolidFill {
		t.Fatalf("solid 标志: %v", h.SolidFill)
	}
	if len(h.Paths) != 1 || len(h.Paths[0].Points) < 3 {
		t.Fatalf("路径: %d 条", len(h.Paths))
	}
	pts := h.Paths[0].Points
	if pts[0].X != 0 || pts[len(pts)-1].X != 0 {
		t.Fatalf("闭合失败: 首尾不一致 %v %v", pts[0], pts[len(pts)-1])
	}
}
