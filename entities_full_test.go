// entities_full_test.go 实体解码器单元测试：用 bitWriter 构造标准位流，
// 逐一验证全部图元的字段解码（公共头 + 专属数据 + handle 流）。
package cad

import (
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"math"
	"testing"
)

// writeEntityPrefix 写记录前缀：UMC handle-stream-size（1 字节）+ OT 类型码。
func writeEntityPrefix(w *testsupport.BitWriter, typeCode uint16) *testsupport.BitWriter {
	return w.UMC(8).OT(typeCode)
}

// writeCommonHead 构造 R2013 布局（idxFlags-noShadow-noLW）公共实体头：
// H handle + EED(0) + pic(0) + entmode + reactors + xdic + ds + nolinks +
// color unknown 位 + ltscale + ltype + plot + mat + visual×3 + invis。
// 与读取侧特征位（featMaterialFlags|featVisualStyles|featDSBinary，noShadow-noLW 变体）严格对称。
// nolinks=1（ByLayer 颜色）路径：color 为 1 位 unknown。
func writeCommonHead(w *testsupport.BitWriter, handle uint64, entmode uint8) *testsupport.BitWriter {
	w.H(0, handle)
	w.BS(0)          // EED 结束
	w.B(0)           // pic 无
	w.BB(entmode)    // entmode
	w.BL(0)          // reactors
	w.B(0)           // xdic 存在
	w.B(0)           // ds binary
	w.B(1)           // nolinks=1 → ByLayer
	w.B(0)           // color unknown 位
	w.BD(1.0)        // ltscale
	w.BB(0)          // ltype flags
	w.BB(0)          // plotstyle flags
	w.BB(0)          // material flags
	w.B(0).B(0).B(0) // visual styles
	w.BS(0)          // invisibility
	// 无 lineweight：与 hasLineWt=false 布局对称
	return w
}

func TestDecodeLineFromBits(t *testing.T) {
	// 公共头 + z_is_zero=1 + xs RD + xe DD + ys RD + ye DD + thickness + extrusion
	w := writeEntityPrefix(testsupport.NewBitWriter(), 0x13)
	writeCommonHead(w, 100, 2)
	w.B(1) // z 全零
	w.RD(10.0)
	w.DD(20.0, 10.0)
	w.RD(-5.5)
	w.DD(-5.5, -5.5)
	w.BT(0)
	w.BE(0, 0, 1)
	// handle 流（objSizeBit 指向的位置不关键：decodeOwnerLayer 失败容忍）
	h := objHeader{rec: &objectRecord{size: uint32(len(w.Bytes())), r2010Plus: true,
		handleSizeFieldBits: 8, handleStreamSizeBits: 8, body: w.Bytes()}}
	_ = h
	r := newBitStream(w.Bytes()) // 位 0 = UMC → OT → 公共头
	_, _ = r.readUMC()
	_, _ = r.readOT()
	head, err := parseEntityHead(r, 0, featMaterialFlags|featVisualStyles|featDSBinary)
	if err != nil {
		t.Fatal(err)
	}
	if head.handle != 100 || head.entityMode != 2 {
		t.Fatalf("公共头: handle=%d entmode=%d", head.handle, head.entityMode)
	}
	ent, err := decodeLine(r, &head)
	if err != nil {
		t.Fatal(err)
	}
	line := ent.(*entLine)
	if line.start.x != 10.0 || line.end.x != 20.0 || line.start.y != -5.5 || line.end.y != -5.5 {
		t.Fatalf("LINE 几何: start=%v end=%v", line.start, line.end)
	}
	if line.mode != 2 {
		t.Fatalf("LINE entmode=%d", line.mode)
	}
}

func TestDecodeCircleFromBits(t *testing.T) {
	w := writeEntityPrefix(testsupport.NewBitWriter(), 0x12)
	writeCommonHead(w, 7, 2)
	w.B3BD(1.0, 2.0, 0) // center
	w.BD(5.0)           // radius
	w.BT(0)
	w.BE(0, 0, 1)
	r := newBitStream(w.Bytes()) // 位 0 = UMC → OT → 公共头
	_, _ = r.readUMC()
	_, _ = r.readOT()
	head, err := parseEntityHead(r, 0, featMaterialFlags|featVisualStyles|featDSBinary)
	if err != nil {
		t.Fatal(err)
	}
	ent, err := decodeCircle(r, &head)
	if err != nil {
		t.Fatal(err)
	}
	c := ent.(*entCircle)
	if c.center.x != 1.0 || c.center.y != 2.0 || c.radius != 5.0 {
		t.Fatalf("CIRCLE: center=%v r=%v", c.center, c.radius)
	}
}

func TestDecodeArcFromBits(t *testing.T) {
	w := writeEntityPrefix(testsupport.NewBitWriter(), 0x11)
	writeCommonHead(w, 8, 2)
	w.B3BD(0, 0, 0)
	w.BD(10.0)
	w.BT(0)
	w.BE(0, 0, 1)
	w.BD(0.0)                    // angle start
	w.BD(math.Pi / 2)            // angle end
	r := newBitStream(w.Bytes()) // 位 0 = UMC → OT → 公共头
	_, _ = r.readUMC()
	_, _ = r.readOT()
	head, _ := parseEntityHead(r, 0, featMaterialFlags|featVisualStyles|featDSBinary)
	ent, err := decodeArc(r, &head)
	if err != nil {
		t.Fatal(err)
	}
	a := ent.(*entArc)
	if a.radius != 10.0 || a.angleStart != 0 || math.Abs(a.angleEnd-math.Pi/2) > 1e-9 {
		t.Fatalf("ARC: r=%v a0=%v a1=%v", a.radius, a.angleStart, a.angleEnd)
	}
}

func TestDecodePointFromBits(t *testing.T) {
	w := writeEntityPrefix(testsupport.NewBitWriter(), 0x1B)
	writeCommonHead(w, 9, 2)
	w.B3BD(3.0, 4.0, 5.0)
	w.BT(0) // thickness（BT：1 位标志）
	w.BE(0, 0, 1)
	w.BD(0)                      // angle
	r := newBitStream(w.Bytes()) // 位 0 = UMC → OT → 公共头
	_, _ = r.readUMC()
	_, _ = r.readOT()
	head, _ := parseEntityHead(r, 0, featMaterialFlags|featVisualStyles|featDSBinary)
	ent, err := decodePoint(r, &head)
	if err != nil {
		t.Fatal(err)
	}
	p := ent.(*entPoint)
	if p.location.x != 3 || p.location.y != 4 || p.location.z != 5 {
		t.Fatalf("POINT: %v", p.location)
	}
}

func TestDecodeEllipseFromBits(t *testing.T) {
	w := writeEntityPrefix(testsupport.NewBitWriter(), 0x23)
	writeCommonHead(w, 10, 2)
	w.B3BD(1, 1, 0) // center
	w.B3BD(2, 0, 0) // major axis
	w.B3BD(0, 0, 1) // extrusion（在 ratio 之前）
	w.BD(0.5)       // ratio
	w.BD(0)         // start
	w.BD(math.Pi)   // end
	// ELLIPSE 无尾部 thickness/extrusion 读取
	r := newBitStream(w.Bytes()) // 位 0 = UMC → OT → 公共头
	_, _ = r.readUMC()
	_, _ = r.readOT()
	head, _ := parseEntityHead(r, 0, featMaterialFlags|featVisualStyles|featDSBinary)
	ent, err := decodeEllipse(r, &head)
	if err != nil {
		t.Fatal(err)
	}
	e := ent.(*entEllipse)
	if e.center.x != 1 || math.Abs(e.ratio-0.5) > 1e-9 || math.Abs(e.endAng-math.Pi) > 1e-9 {
		t.Fatalf("ELLIPSE: ratio=%v end=%v", e.ratio, e.endAng)
	}
}

func TestDecodeLwPolylineFromBits(t *testing.T) {
	w := writeEntityPrefix(testsupport.NewBitWriter(), 0x4D)
	writeCommonHead(w, 11, 2)
	w.BS(0) // flags：无可选段
	w.BL(3) // 顶点数
	w.RD(0)
	w.RD(0)
	w.DD(10, 0)
	w.DD(0, 0)
	w.DD(10, 10)
	w.DD(10, 0)
	// 无 bulges/ids/widths
	r := newBitStream(w.Bytes()) // 位 0 = UMC → OT → 公共头
	_, _ = r.readUMC()
	_, _ = r.readOT()
	head, _ := parseEntityHead(r, 0, featMaterialFlags|featVisualStyles|featDSBinary)
	ent, err := decodeLwPolyline(r, &head, 30, true)
	if err != nil {
		t.Fatal(err)
	}
	p := ent.(*entLwPolyline)
	if len(p.vertices) != 3 || p.vertices[2].x != 10 || p.vertices[2].y != 10 {
		t.Fatalf("LWPOLYLINE: %v", p.vertices)
	}
}

func TestDecodeLwPolylineClosedWithBulges(t *testing.T) {
	w := writeEntityPrefix(testsupport.NewBitWriter(), 0x4D)
	writeCommonHead(w, 12, 2)
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
	r := newBitStream(w.Bytes()) // 位 0 = UMC → OT → 公共头
	_, _ = r.readUMC()
	_, _ = r.readOT()
	head, _ := parseEntityHead(r, 0, featMaterialFlags|featVisualStyles|featDSBinary)
	ent, err := decodeLwPolyline(r, &head, 30, true)
	if err != nil {
		t.Fatal(err)
	}
	p := ent.(*entLwPolyline)
	if len(p.vertices) != 3 || len(p.bulges) != 3 || p.bulges[0] != 0.5 {
		t.Fatalf("LWPOLYLINE verts=%d bulges=%v", len(p.vertices), p.bulges)
	}
	if p.isClosedByGeometry() {
		t.Fatal("首尾不重合不应判定闭合")
	}
	// 首尾重合 → 几何闭合
	p.vertices = append(p.vertices, p.vertices[0])
	if !p.isClosedByGeometry() {
		t.Fatal("首尾重合应判定闭合")
	}
}

func TestDecodeTextFromBits(t *testing.T) {
	w := writeEntityPrefix(testsupport.NewBitWriter(), 0x01)
	writeCommonHead(w, 13, 2)
	flags := uint8(textFlagNoElevation | textFlagNoAlign | textFlagNoOblique | textFlagNoWidth |
		textFlagNoGen | textFlagNoHAlign | textFlagNoVAlign) // 仅 rotation + 必要字段
	w.RC(flags)
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
	r := newBitStream(w.Bytes()) // 位 0 = UMC → OT → 公共头
	_, _ = r.readUMC()
	_, _ = r.readOT()
	head, _ := parseEntityHead(r, 0, featMaterialFlags|featVisualStyles|featDSBinary)
	ent, err := decodeText(r, &head, 30)
	if err != nil {
		t.Fatal(err)
	}
	txt := ent.(*entText)
	if txt.text != "Hello" || txt.insertion.x != 1.0 || txt.height != 2.5 {
		t.Fatalf("TEXT: %q ins=%v h=%v", txt.text, txt.insertion, txt.height)
	}
	// 角度按弧度存储（渲染层转角度）
	if math.Abs(txt.rotation-math.Pi/2) > 1e-9 {
		t.Fatalf("TEXT rotation=%v", txt.rotation)
	}
}

func TestDecodeInsertFromBits(t *testing.T) {
	w := writeEntityPrefix(testsupport.NewBitWriter(), 0x07)
	writeCommonHead(w, 14, 2)
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
	w.H(0x05, 901)               // xdic
	w.H(0x05, 902)               // layer
	w.H(0x05, 77)                // 块头句柄
	r := newBitStream(w.Bytes()) // 位 0 = UMC → OT → 公共头
	_, _ = r.readUMC()
	_, _ = r.readOT()
	head, err := parseEntityHead(r, dataEndBit, featMaterialFlags|featVisualStyles|featDSBinary)
	if err != nil {
		t.Fatal(err)
	}
	ent, err := decodeInsert(r, &head, verR2013)
	if err != nil {
		t.Fatal(err)
	}
	ins := ent.(*entInsert)
	if ins.position.x != 5 || ins.position.y != 6 || ins.scale.x != 1 {
		t.Fatalf("INSERT: pos=%v scale=%v", ins.position, ins.scale)
	}
	if ins.blockHeader != 77 || ins.layer != 902 {
		t.Fatalf("INSERT handle 流: blockHeader=%d layer=%d", ins.blockHeader, ins.layer)
	}
}

func TestReadHandleReferenceAllCodes(t *testing.T) {
	cases := []struct {
		code uint8
		val  uint64
		base uint64
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
		r := newBitStream(testsupport.NewBitWriter().H(c.code, c.val).Bytes())
		got, err := readHandleReference(r, c.base)
		if err != nil {
			t.Fatalf("code=%#x: %v", c.code, err)
		}
		if got != c.want {
			t.Errorf("code=%#x val=%d: 期望 %d 得到 %d", c.code, c.val, c.want, got)
		}
	}
	// base=0 时 -1 钳位到 0
	r := newBitStream(testsupport.NewBitWriter().H(0x08, 0).Bytes())
	if got, _ := readHandleReference(r, 0); got != 0 {
		t.Fatalf("base=0 -1 应钳位 0，得到 %d", got)
	}
}

func TestCommonHeadScoreDiscrimination(t *testing.T) {
	// handle 匹配的候选应显著优于不匹配的
	good := commonEntityHead{handle: 42, ltypeScale: 1.0, entityMode: 2, color: entColor{hasIndex: true, index: 7}}
	bad := commonEntityHead{handle: 99, ltypeScale: -1e300, entityMode: 9}
	if commonHeadScore(&good, 42) <= commonHeadScore(&bad, 42) {
		t.Fatalf("评分区分失败: good=%d bad=%d", commonHeadScore(&good, 42), commonHeadScore(&bad, 42))
	}
}

func TestEntityGeometryScoreRejectsGarbage(t *testing.T) {
	garbage := &entLine{start: point3{1e300, 2, 0}, end: point3{3, 4, 0}}
	sane := &entLine{start: point3{1, 2, 0}, end: point3{3, 4, 0}}
	if entityGeometryScore(garbage) >= entityGeometryScore(sane) {
		t.Fatal("垃圾几何评分应低于正常几何")
	}
	if entityGeometryScore(&entLine{start: point3{math.Inf(1), 0, 0}, end: point3{}}) >= 0 {
		t.Fatal("Inf 坐标应为负分")
	}
}
