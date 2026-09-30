// entities_arc_dimension_test.go ARC_DIMENSION（弧长标注）单元测试：
// bitWriter 构造标准位流验证专属字段解码，另用 example_r13/example_2013
// 样本 gold 值（handle 921）做端到端断言。
package cad

import (
	"math"
	"os"
	"testing"
)

// TestDecodeArcDimensionR2018 R2018 弧长标注：R2010b class_version +
// COMMON_ENTITY_DIMENSION 公共段 + 弧长专属 10 字段。注意 R2007+ 的
// user_text 走字符串流，主位流不占位，构造时不写 TV。
func TestDecodeArcDimensionR2018(t *testing.T) {
	w := writeEntityPrefix(newBitWriter(), 0x20) // 类型码仅占位，走直接分发
	writeCommonHead(w, 921, 2)
	w.RC(0)          // R2010b class_version
	w.B3BD(0, 0, 1)  // extrusion
	w.RD(100).RD(50) // text midpoint x/y
	w.BD(0)          // elevation
	w.RC(0x0b)       // flag1（bit0/bit1/bit3）
	// user_text：R2007+ 主位流 0 位（字符串流承载）
	w.BD(0)             // text rotation
	w.BD(0)             // horizontal direction
	w.BD(1).BD(1).BD(1) // insert scale
	w.BD(0)             // insert rotation
	w.BS(5)             // attachment
	w.BS(1)             // line spacing style
	w.BD(1)             // line spacing factor
	w.BD(25.0)          // actual measurement
	w.B(0).B(0).B(0)    // R2007+ unknown/flip×2
	w.RD(10).RD(20)     // clone_ins_pt
	// 弧长专属：def_pt + xline1_pt + xline2_pt + center_pt + is_partial
	// + arc_start_param + arc_end_param + has_leader + leader1_pt + leader2_pt
	w.B3BD(1, 2, 0)
	w.B3BD(3, 4, 0)
	w.B3BD(5, 6, 0)
	w.B3BD(7, 8, 0)
	w.B(1)           // is_partial
	w.BD(0.5)        // arc_start_param
	w.BD(2.5)        // arc_end_param
	w.B(1)           // has_leader
	w.B3BD(9, 10, 0) // leader1_pt
	w.B3BD(11, 12, 0)
	r := newBitStream(w.bytes())
	_, _ = r.readUMC()
	_, _ = r.readOT()
	head, err := parseEntityHead(r, 0, featMaterialFlags|featVisualStyles|featDSBinary)
	if err != nil {
		t.Fatal(err)
	}
	ent, err := decodeDimension(r, &head, verR2018, dimLayoutArc)
	if err != nil {
		t.Fatal(err)
	}
	d := ent.(*entDimension)
	if d.defPt.x != 1 || d.point13.x != 3 || d.point14.x != 5 || d.point15.x != 7 {
		t.Fatalf("专属点: def=%v p13=%v p14=%v p15=%v", d.defPt, d.point13, d.point14, d.point15)
	}
	if !d.isPartial || math.Abs(d.arcStartParam-0.5) > 1e-9 || math.Abs(d.arcEndParam-2.5) > 1e-9 {
		t.Fatalf("弧参数: partial=%v start=%v end=%v", d.isPartial, d.arcStartParam, d.arcEndParam)
	}
	if !d.hasLeader || d.leader1Pt.x != 9 || d.leader2Pt.x != 11 {
		t.Fatalf("引线: leader=%v p1=%v p2=%v", d.hasLeader, d.leader1Pt, d.leader2Pt)
	}
	// flag 合成：flag1=0x0b → bit0=1 清 bit7、bit1=1 置 bit5，ARC 低 3 位=5
	if d.dimFlag != 0x25 {
		t.Fatalf("flag: %#x", d.dimFlag)
	}
	if d.classVersion != 0 {
		t.Fatalf("class_version: %d", d.classVersion)
	}
	if entityField(d, "is_partial") != int64(1) ||
		entityField(d, "has_leader") != int64(1) {
		t.Fatalf("审计键: is_partial=%v has_leader=%v",
			entityField(d, "is_partial"), entityField(d, "has_leader"))
	}
	if entityField(d, "entity") == "ARC_DIMENSION" {
		t.Fatal("直接调用不经 fillMeta，entity 键应为空")
	}
	// dimGoldEntityName 对无 DIM_ 前缀的 ARC_DIMENSION 直通
	if dimGoldEntityName("ARC_DIMENSION") != "ARC_DIMENSION" {
		t.Fatalf("entity 映射: %v", dimGoldEntityName("ARC_DIMENSION"))
	}
}

// TestArcDimensionGoldR13 example_r13 样本端到端：h=921 对照 gold 关键值
// （flag=37、def_pt、弧参数）。
func TestArcDimensionGoldR13(t *testing.T) {
	data, err := os.ReadFile("/tmp/libredwg/test/test-data/example_r13.dwg")
	if err != nil {
		t.Skipf("样本不可用: %v", err)
	}
	doc, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := doc.EntityByHandle(921).(*entDimension)
	if !ok {
		t.Fatalf("handle 921 不是 entDimension: %T", doc.EntityByHandle(921))
	}
	if d.dimFlag != 37 || d.dimFlags != 11 {
		t.Fatalf("flag=%d flag1=%d", d.dimFlag, d.dimFlags)
	}
	if math.Abs(d.defPt.x-6125.3814140265295) > 1e-6 || math.Abs(d.defPt.y-2865.1655686581416) > 1e-6 {
		t.Fatalf("def_pt: %v", d.defPt)
	}
	if math.Abs(d.arcStartParam-4.13129349503489) > 1e-9 || math.Abs(d.arcEndParam-1.16526492696508) > 1e-9 {
		t.Fatalf("弧参数: %v..%v", d.arcStartParam, d.arcEndParam)
	}
	if d.isPartial || d.hasLeader {
		t.Fatalf("partial=%v leader=%v", d.isPartial, d.hasLeader)
	}
}

// TestArcDimensionGoldR2013 example_2013 样本端到端：R2010b class_version
// 字段存在（gold 同名键对照经 TestEntityAudit 覆盖，此处断言字段值）。
func TestArcDimensionGoldR2013(t *testing.T) {
	data, err := os.ReadFile("/tmp/libredwg/test/test-data/example_2013.dwg")
	if err != nil {
		t.Skipf("样本不可用: %v", err)
	}
	doc, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := doc.EntityByHandle(921).(*entDimension)
	if !ok {
		t.Fatalf("handle 921 不是 entDimension: %T", doc.EntityByHandle(921))
	}
	if d.classVersion != 0 {
		t.Fatalf("class_version: %d", d.classVersion)
	}
	if math.Abs(d.arcStartParam-4.13129349503489) > 1e-9 {
		t.Fatalf("arc_start_param: %v", d.arcStartParam)
	}
	if entityField(d, "class_version") != int64(0) {
		t.Fatalf("审计键 class_version: %v", entityField(d, "class_version"))
	}
}
