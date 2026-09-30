// entities_gap_test.go 实体侧 spec 查漏补缺批次（批次 G）新增实体的
// 单元测试：合成位流解码验证 + LibreDWG 官方语料对照。
package entity

import (
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/objrec"
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"math"
	"testing"
)

// decodeEntityByName 经 decodeEntityByType 分发解码合成位流实体，
// 同时覆盖类型码 → 类型名 → 解码器分发的完整链路。
func decodeEntityByName(t *testing.T, w *testsupport.BitWriter, TypeCode uint16, dynamic map[uint16]string) any {
	t.Helper()
	r := bitstream.NewBitStream(w.Bytes())
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	Head, err := ParseEntityHead(r, 0, FeatMaterialFlags|FeatVisualStyles|FeatDSBinary)
	if err != nil {
		t.Fatalf("公共头解析失败: %v", err)
	}
	h := objrec.ObjHeader{TypeCode: TypeCode}
	ent, err := decodeEntityByTypeVer(r, &Head, h, Head.Handle, container.VerR2018, h.TypeCode, dynamic, "")
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	return ent
}

// TestDecodeLargeRadialDimR2018 R2018 LARGE_RADIAL_DIMENSION（动态类码 500）：
// COMMON_ENTITY_DIMENSION 公共段 + def_pt/chord_pt/jog_angle/ovr_center/
// jog_pt 专属尾部，经 decodeEntityByType 动态类名分发链路解码。
func TestDecodeLargeRadialDimR2018(t *testing.T) {
	w := testsupport.WriteEntityPrefix(testsupport.NewBitWriter(), 0x1F4)
	testsupport.WriteCommonHead(w, 900, 2)
	w.RC(0)          // class_version
	w.B3BD(0, 0, 1)  // extrusion
	w.RD(100).RD(50) // text midpoint
	w.BD(0)          // elevation
	w.RC(0)          // dim flags
	// R2007+ 用户文字在对象尾字符串流中，主位流无 TV 字段（与
	// decodeDimCanonical 读侧口径一致）
	w.BD(0)             // text rotation
	w.BD(0)             // horizontal direction
	w.BD(1).BD(1).BD(1) // insert scale
	w.BD(0)             // insert rotation
	w.BS(1)             // attachment
	w.BS(1)             // line spacing style
	w.BD(1)             // line spacing factor
	w.BD(42.0)          // actual measurement
	w.B(0).B(0).B(0)    // R2007+ flags
	w.RD(10).RD(20)     // 12-pt
	// LARGE_RADIAL 专属尾部
	w.B3BD(30, 40, 0) // def_pt
	w.B3BD(60, 80, 0) // chord_pt
	w.BD(math.Pi / 4) // jog_angle
	w.B3BD(5, 6, 0)   // ovr_center
	w.B3BD(70, 90, 0) // jog_pt
	dynamic := map[uint16]string{0x1F4: "LARGE_RADIAL_DIMENSION"}
	if !objrec.IsEntityType(0x1F4, dynamic) {
		t.Fatal("isEntityType 未识别 LARGE_RADIAL_DIMENSION")
	}
	ent := decodeEntityByName(t, w, 0x1F4, dynamic)
	d, ok := ent.(*EntDimension)
	if !ok {
		t.Fatalf("类型: %T", ent)
	}
	if d.Point13.X != 30 || d.Point13.Y != 40 || d.Point14.X != 60 || d.Point14.Y != 80 {
		t.Fatalf("def_pt/chord_pt: p13=%v p14=%v", d.Point13, d.Point14)
	}
	if math.Abs(d.ExtLineRotation-math.Pi/4) > 1e-9 {
		t.Fatalf("jog_angle: %v", d.ExtLineRotation)
	}
	if d.Point15.X != 5 || d.Point15.Y != 6 || d.Point10.X != 70 || d.Point10.Y != 90 {
		t.Fatalf("ovr_center/jog_pt: p15=%v p10=%v", d.Point15, d.Point10)
	}
	if d.ActualMeasurement != 42.0 || d.InsertPoint.X != 10 || d.InsertPoint.Y != 20 {
		t.Fatalf("公共段: measure=%v p12=%v", d.ActualMeasurement, d.InsertPoint)
	}
	// flag 合成：flag1=0 → bit7=1、bit5=0、无类型位 → 0x80
	if d.DimFlag != 0x80 {
		t.Fatalf("dimFlag: %#x", d.DimFlag)
	}
}

// TestDecodeLargeRadialDimR2000 R2000 布局：无 class_version 字节、
// 无 R2007+ 三标志位，attachment 段存在，TV 文本在主位流。
func TestDecodeLargeRadialDimR2000(t *testing.T) {
	w := testsupport.WriteEntityPrefix(testsupport.NewBitWriter(), 0x1F4)
	testsupport.WriteCommonHead(w, 901, 2)
	w.B3BD(0, 0, 1)     // extrusion（无版本字节）
	w.RD(1).RD(2)       // text midpoint
	w.BD(0)             // elevation
	w.RC(0)             // dim flags
	w.TV("")            // 用户文字（R2000 主位流 TV）
	w.BD(0)             // text rotation
	w.BD(0)             // horizontal direction
	w.BD(1).BD(1).BD(1) // insert scale
	w.BD(0)             // insert rotation
	w.BS(1)             // attachment
	w.BS(1)             // line spacing style
	w.BD(1)             // line spacing factor
	w.BD(7.5)           // actual measurement
	w.RD(3).RD(4)       // 12-pt
	w.B3BD(11, 22, 0)   // def_pt
	w.B3BD(33, 44, 0)   // chord_pt
	w.BD(0.5)           // jog_angle
	w.B3BD(1, 1, 0)     // ovr_center
	w.B3BD(55, 66, 0)   // jog_pt
	r := bitstream.NewBitStream(w.Bytes())
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	Head, err := ParseEntityHead(r, 0, FeatMaterialFlags|FeatVisualStyles|FeatDSBinary)
	if err != nil {
		t.Fatal(err)
	}
	ent, err := DecodeDimension(r, &Head, container.VerR2000, DimLayoutLargeRadial)
	if err != nil {
		t.Fatal(err)
	}
	d := ent.(*EntDimension)
	if d.Point13.X != 11 || d.Point13.Y != 22 || d.Point14.X != 33 || d.Point14.Y != 44 {
		t.Fatalf("def_pt/chord_pt: p13=%v p14=%v", d.Point13, d.Point14)
	}
	if math.Abs(d.ExtLineRotation-0.5) > 1e-9 {
		t.Fatalf("jog_angle: %v", d.ExtLineRotation)
	}
	if d.Point15.X != 1 || d.Point10.X != 55 || d.Point10.Y != 66 {
		t.Fatalf("ovr_center/jog_pt: p15=%v p10=%v", d.Point15, d.Point10)
	}
	if d.ActualMeasurement != 7.5 {
		t.Fatalf("测量值: %v", d.ActualMeasurement)
	}
}
