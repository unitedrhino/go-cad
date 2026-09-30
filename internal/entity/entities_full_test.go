// entities_full_test.go 实体解码器单元测试：用 bitWriter 构造标准位流，
// 逐一验证全部图元的字段解码（公共头 + 专属数据 + handle 流）。
package entity

import (
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/objrec"
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"math"
	"testing"
)

func TestDecodeLineFromBits(t *testing.T) {
	// 公共头 + z_is_zero=1 + xs RD + xe DD + ys RD + ye DD + thickness + extrusion
	w := testsupport.WriteEntityPrefix(testsupport.NewBitWriter(), 0x13)
	testsupport.WriteCommonHead(w, 100, 2)
	w.B(1) // z 全零
	w.RD(10.0)
	w.DD(20.0, 10.0)
	w.RD(-5.5)
	w.DD(-5.5, -5.5)
	w.BT(0)
	w.BE(0, 0, 1)
	// handle 流（objSizeBit 指向的位置不关键：decodeOwnerLayer 失败容忍）
	h := objrec.ObjHeader{Rec: &objrec.ObjectRecord{Size: uint32(len(w.Bytes())), R2010Plus: true,
		HandleSizeFieldBits: 8, HandleStreamSizeBits: 8, Body: w.Bytes()}}
	_ = h
	r := bitstream.NewBitStream(w.Bytes()) // 位 0 = UMC → OT → 公共头
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	Head, err := ParseEntityHead(r, 0, FeatMaterialFlags|FeatVisualStyles|FeatDSBinary)
	if err != nil {
		t.Fatal(err)
	}
	if Head.Handle != 100 || Head.EntityMode != 2 {
		t.Fatalf("公共头: handle=%d entmode=%d", Head.Handle, Head.EntityMode)
	}
	ent, err := decodeLine(r, &Head)
	if err != nil {
		t.Fatal(err)
	}
	line := ent.(*EntLine)
	if line.Start.X != 10.0 || line.End.X != 20.0 || line.Start.Y != -5.5 || line.End.Y != -5.5 {
		t.Fatalf("LINE 几何: start=%v end=%v", line.Start, line.End)
	}
	if line.Mode != 2 {
		t.Fatalf("LINE entmode=%d", line.Mode)
	}
}

func TestDecodeCircleFromBits(t *testing.T) {
	w := testsupport.WriteEntityPrefix(testsupport.NewBitWriter(), 0x12)
	testsupport.WriteCommonHead(w, 7, 2)
	w.B3BD(1.0, 2.0, 0) // center
	w.BD(5.0)           // radius
	w.BT(0)
	w.BE(0, 0, 1)
	r := bitstream.NewBitStream(w.Bytes()) // 位 0 = UMC → OT → 公共头
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	Head, err := ParseEntityHead(r, 0, FeatMaterialFlags|FeatVisualStyles|FeatDSBinary)
	if err != nil {
		t.Fatal(err)
	}
	ent, err := decodeCircle(r, &Head)
	if err != nil {
		t.Fatal(err)
	}
	c := ent.(*EntCircle)
	if c.Center.X != 1.0 || c.Center.Y != 2.0 || c.Radius != 5.0 {
		t.Fatalf("CIRCLE: center=%v r=%v", c.Center, c.Radius)
	}
}

func TestDecodeArcFromBits(t *testing.T) {
	w := testsupport.WriteEntityPrefix(testsupport.NewBitWriter(), 0x11)
	testsupport.WriteCommonHead(w, 8, 2)
	w.B3BD(0, 0, 0)
	w.BD(10.0)
	w.BT(0)
	w.BE(0, 0, 1)
	w.BD(0.0)                              // angle start
	w.BD(math.Pi / 2)                      // angle end
	r := bitstream.NewBitStream(w.Bytes()) // 位 0 = UMC → OT → 公共头
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	Head, _ := ParseEntityHead(r, 0, FeatMaterialFlags|FeatVisualStyles|FeatDSBinary)
	ent, err := decodeArc(r, &Head)
	if err != nil {
		t.Fatal(err)
	}
	a := ent.(*EntArc)
	if a.Radius != 10.0 || a.AngleStart != 0 || math.Abs(a.AngleEnd-math.Pi/2) > 1e-9 {
		t.Fatalf("ARC: r=%v a0=%v a1=%v", a.Radius, a.AngleStart, a.AngleEnd)
	}
}

func TestDecodePointFromBits(t *testing.T) {
	w := testsupport.WriteEntityPrefix(testsupport.NewBitWriter(), 0x1B)
	testsupport.WriteCommonHead(w, 9, 2)
	w.B3BD(3.0, 4.0, 5.0)
	w.BT(0) // thickness（BT：1 位标志）
	w.BE(0, 0, 1)
	w.BD(0)                                // angle
	r := bitstream.NewBitStream(w.Bytes()) // 位 0 = UMC → OT → 公共头
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	Head, _ := ParseEntityHead(r, 0, FeatMaterialFlags|FeatVisualStyles|FeatDSBinary)
	ent, err := decodePoint(r, &Head)
	if err != nil {
		t.Fatal(err)
	}
	P := ent.(*EntPoint)
	if P.Location.X != 3 || P.Location.Y != 4 || P.Location.Z != 5 {
		t.Fatalf("POINT: %v", P.Location)
	}
}

func TestDecodeEllipseFromBits(t *testing.T) {
	w := testsupport.WriteEntityPrefix(testsupport.NewBitWriter(), 0x23)
	testsupport.WriteCommonHead(w, 10, 2)
	w.B3BD(1, 1, 0) // center
	w.B3BD(2, 0, 0) // major axis
	w.B3BD(0, 0, 1) // extrusion（在 ratio 之前）
	w.BD(0.5)       // ratio
	w.BD(0)         // start
	w.BD(math.Pi)   // end
	// ELLIPSE 无尾部 thickness/extrusion 读取
	r := bitstream.NewBitStream(w.Bytes()) // 位 0 = UMC → OT → 公共头
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	Head, _ := ParseEntityHead(r, 0, FeatMaterialFlags|FeatVisualStyles|FeatDSBinary)
	ent, err := decodeEllipse(r, &Head)
	if err != nil {
		t.Fatal(err)
	}
	e := ent.(*EntEllipse)
	if e.Center.X != 1 || math.Abs(e.Ratio-0.5) > 1e-9 || math.Abs(e.EndAng-math.Pi) > 1e-9 {
		t.Fatalf("ELLIPSE: ratio=%v end=%v", e.Ratio, e.EndAng)
	}
}

func TestDecodeLwPolylineFromBits(t *testing.T) {
	w := testsupport.WriteEntityPrefix(testsupport.NewBitWriter(), 0x4D)
	testsupport.WriteCommonHead(w, 11, 2)
	w.BS(0) // flags：无可选段
	w.BL(3) // 顶点数
	w.RD(0)
	w.RD(0)
	w.DD(10, 0)
	w.DD(0, 0)
	w.DD(10, 10)
	w.DD(10, 0)
	// 无 bulges/ids/widths
	r := bitstream.NewBitStream(w.Bytes()) // 位 0 = UMC → OT → 公共头
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	Head, _ := ParseEntityHead(r, 0, FeatMaterialFlags|FeatVisualStyles|FeatDSBinary)
	ent, err := decodeLwPolyline(r, &Head, 30, true)
	if err != nil {
		t.Fatal(err)
	}
	P := ent.(*EntLwPolyline)
	if len(P.Vertices) != 3 || P.Vertices[2].X != 10 || P.Vertices[2].Y != 10 {
		t.Fatalf("LWPOLYLINE: %v", P.Vertices)
	}
}

func TestDecodeLwPolylineClosedWithBulges(t *testing.T) {
	w := testsupport.WriteEntityPrefix(testsupport.NewBitWriter(), 0x4D)
	testsupport.WriteCommonHead(w, 12, 2)
	w.BS(lwFlagHasNormal | lwFlagHasBulges) // extrusion + bulges
	w.B3BD(0, 0, 1)                         // extrusion（spec FIELD_3BD，无 BE 默认前缀位）
	// 位流顺序：numVerts → numBulges → numIDs → numWidths → 顶点 → bulges
	w.BL(3) // 顶点数
	w.BL(3) // bulge 数
	w.RD(0)
	w.RD(0)
	w.DD(10, 0)
	w.DD(0, 0)
	w.DD(0, 10)
	w.DD(10, 0)
	w.BD(0.5)
	w.BD(-0.5)
	w.BD(0)
	r := bitstream.NewBitStream(w.Bytes()) // 位 0 = UMC → OT → 公共头
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	Head, _ := ParseEntityHead(r, 0, FeatMaterialFlags|FeatVisualStyles|FeatDSBinary)
	ent, err := decodeLwPolyline(r, &Head, 30, true)
	if err != nil {
		t.Fatal(err)
	}
	P := ent.(*EntLwPolyline)
	if len(P.Vertices) != 3 || len(P.Bulges) != 3 || P.Bulges[0] != 0.5 {
		t.Fatalf("LWPOLYLINE verts=%d bulges=%v", len(P.Vertices), P.Bulges)
	}
	if P.IsClosedByGeometry() {
		t.Fatal("首尾不重合不应判定闭合")
	}
	// 首尾重合 → 几何闭合
	P.Vertices = append(P.Vertices, P.Vertices[0])
	if !P.IsClosedByGeometry() {
		t.Fatal("首尾重合应判定闭合")
	}
}

func TestDecodeTextFromBits(t *testing.T) {
	w := testsupport.WriteEntityPrefix(testsupport.NewBitWriter(), 0x01)
	testsupport.WriteCommonHead(w, 13, 2)
	Flags := uint8(TextFlagNoElevation | TextFlagNoAlign | TextFlagNoOblique | TextFlagNoWidth |
		TextFlagNoGen | TextFlagNoHAlign | TextFlagNoVAlign) // 仅 rotation + 必要字段
	w.RC(Flags)
	// 无 elevation
	w.RD(1.0) // insertion x
	w.RD(2.0) // insertion y
	w.BE(0, 0, 1)
	w.BT(0)
	// 无 oblique
	w.RD(math.Pi / 2) // rotation
	w.RD(2.5)         // height
	// 无 width
	// R21 文本尾：无 gen/halign/valign 位 → 直接 TU 串
	w.TU("Hello")
	r := bitstream.NewBitStream(w.Bytes()) // 位 0 = UMC → OT → 公共头
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	Head, _ := ParseEntityHead(r, 0, FeatMaterialFlags|FeatVisualStyles|FeatDSBinary)
	ent, err := decodeText(r, &Head, 30)
	if err != nil {
		t.Fatal(err)
	}
	Txt := ent.(*EntText)
	if Txt.Text != "Hello" || Txt.Insertion.X != 1.0 || Txt.Height != 2.5 {
		t.Fatalf("TEXT: %q ins=%v h=%v", Txt.Text, Txt.Insertion, Txt.Height)
	}
	// 角度按弧度存储（渲染层转角度）
	if math.Abs(Txt.Rotation-math.Pi/2) > 1e-9 {
		t.Fatalf("TEXT rotation=%v", Txt.Rotation)
	}
}

func TestDecodeInsertFromBits(t *testing.T) {
	w := testsupport.WriteEntityPrefix(testsupport.NewBitWriter(), 0x07)
	testsupport.WriteCommonHead(w, 14, 2)
	w.B3BD(5, 6, 0) // position
	w.BB(0x03)      // scale 全 1
	w.BD(0)         // rotation
	w.B3BD(0, 0, 1) // extrusion
	w.B(0)          // 无 attribs
	// handle 流（entmode=2 无 owner、无 reactors）：xdic + layer + 块头，
	// 起点 = 几何结束位（与真实记录的 dataEndBit→handle 流布局一致）
	dataEndBit := uint64(len(w.Data)) * 8
	if w.Bit > 0 {
		dataEndBit = uint64((len(w.Data)-1)*8 + w.Bit)
	}
	w.H(0x05, 901)                         // xdic
	w.H(0x05, 902)                         // layer
	w.H(0x05, 77)                          // 块头句柄
	r := bitstream.NewBitStream(w.Bytes()) // 位 0 = UMC → OT → 公共头
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	Head, err := ParseEntityHead(r, dataEndBit, FeatMaterialFlags|FeatVisualStyles|FeatDSBinary)
	if err != nil {
		t.Fatal(err)
	}
	ent, err := DecodeInsert(r, &Head, container.VerR2013)
	if err != nil {
		t.Fatal(err)
	}
	ins := ent.(*EntInsert)
	if ins.Position.X != 5 || ins.Position.Y != 6 || ins.Scale.X != 1 {
		t.Fatalf("INSERT: pos=%v scale=%v", ins.Position, ins.Scale)
	}
	if ins.BlockHeader != 77 || ins.Layer != 902 {
		t.Fatalf("INSERT handle 流: blockHeader=%d layer=%d", ins.BlockHeader, ins.Layer)
	}
}

func TestReadHandleReferenceAllCodes(t *testing.T) {
	cases := []struct {
		code uint8
		val  uint64
		Base uint64
		want uint64
	}{
		{0x02, 5, 100, 5},   // 绝对
		{0x05, 7, 100, 7},   // 绝对（软拥有）
		{0x06, 0, 100, 101}, // +1
		{0x08, 0, 100, 99},  // -1
		{0x0A, 3, 100, 103}, // +偏移
		{0x0C, 4, 100, 96},  // -偏移
		{0x04, 9, 100, 9},   // 默认绝对
	}
	for _, c := range cases {
		r := bitstream.NewBitStream(testsupport.NewBitWriter().H(c.code, c.val).Bytes())
		got, err := objrec.ReadHandleReference(r, c.Base)
		if err != nil {
			t.Fatalf("code=%#x: %v", c.code, err)
		}
		if got != c.want {
			t.Errorf("code=%#x val=%d: 期望 %d 得到 %d", c.code, c.val, c.want, got)
		}
	}
	// base=0 时 -1 钳位到 0
	r := bitstream.NewBitStream(testsupport.NewBitWriter().H(0x08, 0).Bytes())
	if got, _ := objrec.ReadHandleReference(r, 0); got != 0 {
		t.Fatalf("base=0 -1 应钳位 0，得到 %d", got)
	}
}

func TestCommonHeadScoreDiscrimination(t *testing.T) {
	// handle 匹配的候选应显著优于不匹配的
	good := CommonEntityHead{Handle: 42, LtypeScale: 1.0, EntityMode: 2, Color: EntColor{HasIndex: true, Index: 7}}
	bad := CommonEntityHead{Handle: 99, LtypeScale: -1e300, EntityMode: 9}
	if commonHeadScore(&good, 42) <= commonHeadScore(&bad, 42) {
		t.Fatalf("评分区分失败: good=%d bad=%d", commonHeadScore(&good, 42), commonHeadScore(&bad, 42))
	}
}

func TestEntityGeometryScoreRejectsGarbage(t *testing.T) {
	garbage := &EntLine{Start: Point3{1e300, 2, 0}, End: Point3{3, 4, 0}}
	sane := &EntLine{Start: Point3{1, 2, 0}, End: Point3{3, 4, 0}}
	if EntityGeometryScore(garbage) >= EntityGeometryScore(sane) {
		t.Fatal("垃圾几何评分应低于正常几何")
	}
	if EntityGeometryScore(&EntLine{Start: Point3{math.Inf(1), 0, 0}, End: Point3{}}) >= 0 {
		t.Fatal("Inf 坐标应为负分")
	}
}
