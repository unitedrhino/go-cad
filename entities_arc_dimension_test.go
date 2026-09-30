// entities_arc_dimension_test.go ARC_DIMENSION（弧长标注）单元测试：
// bitWriter 构造标准位流验证专属字段解码，另用 example_r13/example_2013
// 样本 gold 值（handle 921）做端到端断言。
package cad

import (
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/entity"
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"math"
	"os"
	"testing"
)

// TestDecodeArcDimensionR2018 R2018 弧长标注：R2010b class_version +
// COMMON_ENTITY_DIMENSION 公共段 + 弧长专属 10 字段。注意 R2007+ 的
// user_text 走字符串流，主位流不占位，构造时不写 TV。
func TestDecodeArcDimensionR2018(t *testing.T) {
	w := testsupport.WriteEntityPrefix(testsupport.NewBitWriter(), 0x20) // 类型码仅占位，走直接分发
	testsupport.WriteCommonHead(w, 921, 2)
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
	r := bitstream.NewBitStream(w.Bytes())
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	head, err := entity.ParseEntityHead(r, 0, entity.FeatMaterialFlags|entity.FeatVisualStyles|entity.FeatDSBinary)
	if err != nil {
		t.Fatal(err)
	}
	ent, err := entity.DecodeDimension(r, &head, container.VerR2018, entity.DimLayoutArc)
	if err != nil {
		t.Fatal(err)
	}
	d := ent.(*entity.EntDimension)
	if d.DefPt.X != 1 || d.Point13.X != 3 || d.Point14.X != 5 || d.Point15.X != 7 {
		t.Fatalf("专属点: def=%v p13=%v p14=%v p15=%v", d.DefPt, d.Point13, d.Point14, d.Point15)
	}
	if !d.IsPartial || math.Abs(d.ArcStartParam-0.5) > 1e-9 || math.Abs(d.ArcEndParam-2.5) > 1e-9 {
		t.Fatalf("弧参数: partial=%v start=%v end=%v", d.IsPartial, d.ArcStartParam, d.ArcEndParam)
	}
	if !d.HasLeader || d.Leader1Pt.X != 9 || d.Leader2Pt.X != 11 {
		t.Fatalf("引线: leader=%v p1=%v p2=%v", d.HasLeader, d.Leader1Pt, d.Leader2Pt)
	}
	// flag 合成：flag1=0x0b → bit0=1 清 bit7、bit1=1 置 bit5，ARC 低 3 位=5
	if d.DimFlag != 0x25 {
		t.Fatalf("flag: %#x", d.DimFlag)
	}
	if d.ClassVersion != 0 {
		t.Fatalf("class_version: %d", d.ClassVersion)
	}
	if entity.EntityField(d, "is_partial") != int64(1) ||
		entity.EntityField(d, "has_leader") != int64(1) {
		t.Fatalf("审计键: is_partial=%v has_leader=%v",
			entity.EntityField(d, "is_partial"), entity.EntityField(d, "has_leader"))
	}
	if entity.EntityField(d, "entity") == "ARC_DIMENSION" {
		t.Fatal("直接调用不经 fillMeta，entity 键应为空")
	}
	// dimGoldEntityName 对无 DIM_ 前缀的 ARC_DIMENSION 直通
	if entity.DimGoldEntityName("ARC_DIMENSION") != "ARC_DIMENSION" {
		t.Fatalf("entity 映射: %v", entity.DimGoldEntityName("ARC_DIMENSION"))
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
	d, ok := doc.EntityByHandle(921).(*entity.EntDimension)
	if !ok {
		t.Fatalf("handle 921 不是 entDimension: %T", doc.EntityByHandle(921))
	}
	if d.DimFlag != 37 || d.DimFlags != 11 {
		t.Fatalf("flag=%d flag1=%d", d.DimFlag, d.DimFlags)
	}
	if math.Abs(d.DefPt.X-6125.3814140265295) > 1e-6 || math.Abs(d.DefPt.Y-2865.1655686581416) > 1e-6 {
		t.Fatalf("def_pt: %v", d.DefPt)
	}
	if math.Abs(d.ArcStartParam-4.13129349503489) > 1e-9 || math.Abs(d.ArcEndParam-1.16526492696508) > 1e-9 {
		t.Fatalf("弧参数: %v..%v", d.ArcStartParam, d.ArcEndParam)
	}
	if d.IsPartial || d.HasLeader {
		t.Fatalf("partial=%v leader=%v", d.IsPartial, d.HasLeader)
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
	d, ok := doc.EntityByHandle(921).(*entity.EntDimension)
	if !ok {
		t.Fatalf("handle 921 不是 entDimension: %T", doc.EntityByHandle(921))
	}
	if d.ClassVersion != 0 {
		t.Fatalf("class_version: %d", d.ClassVersion)
	}
	if math.Abs(d.ArcStartParam-4.13129349503489) > 1e-9 {
		t.Fatalf("arc_start_param: %v", d.ArcStartParam)
	}
	if entity.EntityField(d, "class_version") != int64(0) {
		t.Fatalf("审计键 class_version: %v", entity.EntityField(d, "class_version"))
	}
}
