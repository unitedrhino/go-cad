// entities_gap_test.go 实体侧 spec 查漏补缺批次（批次 G）新增实体的
// 单元测试：合成位流解码验证 + LibreDWG 官方语料对照。
package cad

import (
	"math"
	"testing"
)

// decodeEntityByName 经 decodeEntityByType 分发解码合成位流实体，
// 同时覆盖类型码 → 类型名 → 解码器分发的完整链路。
func decodeEntityByName(t *testing.T, w *bitWriter, typeCode uint16, dynamic map[uint16]string) any {
	t.Helper()
	r := newBitStream(w.bytes())
	_, _ = r.readUMC()
	_, _ = r.readOT()
	head, err := parseEntityHead(r, 0, featMaterialFlags|featVisualStyles|featDSBinary)
	if err != nil {
		t.Fatalf("公共头解析失败: %v", err)
	}
	h := objHeader{typeCode: typeCode}
	ent, err := decodeEntityByTypeVer(r, &head, h, head.handle, verR2018, h.typeCode, dynamic, "")
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	return ent
}

// TestDecodeLargeRadialDimR2018 R2018 LARGE_RADIAL_DIMENSION（动态类码 500）：
// COMMON_ENTITY_DIMENSION 公共段 + def_pt/chord_pt/jog_angle/ovr_center/
// jog_pt 专属尾部，经 decodeEntityByType 动态类名分发链路解码。
func TestDecodeLargeRadialDimR2018(t *testing.T) {
	w := writeEntityPrefix(newBitWriter(), 0x1F4)
	writeCommonHead(w, 900, 2)
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
	if !isEntityType(0x1F4, dynamic) {
		t.Fatal("isEntityType 未识别 LARGE_RADIAL_DIMENSION")
	}
	ent := decodeEntityByName(t, w, 0x1F4, dynamic)
	d, ok := ent.(*entDimension)
	if !ok {
		t.Fatalf("类型: %T", ent)
	}
	if d.point13.x != 30 || d.point13.y != 40 || d.point14.x != 60 || d.point14.y != 80 {
		t.Fatalf("def_pt/chord_pt: p13=%v p14=%v", d.point13, d.point14)
	}
	if math.Abs(d.extLineRotation-math.Pi/4) > 1e-9 {
		t.Fatalf("jog_angle: %v", d.extLineRotation)
	}
	if d.point15.x != 5 || d.point15.y != 6 || d.point10.x != 70 || d.point10.y != 90 {
		t.Fatalf("ovr_center/jog_pt: p15=%v p10=%v", d.point15, d.point10)
	}
	if d.actualMeasurement != 42.0 || d.insertPoint.x != 10 || d.insertPoint.y != 20 {
		t.Fatalf("公共段: measure=%v p12=%v", d.actualMeasurement, d.insertPoint)
	}
	// flag 合成：flag1=0 → bit7=1、bit5=0、无类型位 → 0x80
	if d.dimFlag != 0x80 {
		t.Fatalf("dimFlag: %#x", d.dimFlag)
	}
}

// TestDecodeLargeRadialDimR2000 R2000 布局：无 class_version 字节、
// 无 R2007+ 三标志位，attachment 段存在，TV 文本在主位流。
func TestDecodeLargeRadialDimR2000(t *testing.T) {
	w := writeEntityPrefix(newBitWriter(), 0x1F4)
	writeCommonHead(w, 901, 2)
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
	r := newBitStream(w.bytes())
	_, _ = r.readUMC()
	_, _ = r.readOT()
	head, err := parseEntityHead(r, 0, featMaterialFlags|featVisualStyles|featDSBinary)
	if err != nil {
		t.Fatal(err)
	}
	ent, err := decodeDimension(r, &head, verR2000, dimLayoutLargeRadial)
	if err != nil {
		t.Fatal(err)
	}
	d := ent.(*entDimension)
	if d.point13.x != 11 || d.point13.y != 22 || d.point14.x != 33 || d.point14.y != 44 {
		t.Fatalf("def_pt/chord_pt: p13=%v p14=%v", d.point13, d.point14)
	}
	if math.Abs(d.extLineRotation-0.5) > 1e-9 {
		t.Fatalf("jog_angle: %v", d.extLineRotation)
	}
	if d.point15.x != 1 || d.point10.x != 55 || d.point10.y != 66 {
		t.Fatalf("ovr_center/jog_pt: p15=%v p10=%v", d.point15, d.point10)
	}
	if d.actualMeasurement != 7.5 {
		t.Fatalf("测量值: %v", d.actualMeasurement)
	}
}
