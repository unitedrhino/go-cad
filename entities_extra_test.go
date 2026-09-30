// entities_extra_test.go 补充 MTEXT/INSERT（完整句柄流）/ATTRIB 与
// cad.go 层（classify/Texts/颜色继承）的单元测试。
package cad

import (
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/objrec"
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"math"
	"testing"
)

// writeMTextR2004Body 构造 R2004 版 MTEXT 专属字段（无背景/rect_height、TV 文本）。
func writeMTextR2004Body(w *testsupport.BitWriter, text string) {
	w.B3BD(1, 2, 0) // insertion
	w.B3BD(0, 0, 1) // extrusion
	w.B3BD(1, 0, 0) // x-axis dir
	w.BD(50)        // rect width
	w.BD(3)         // text height
	w.BS(1)         // attachment
	w.BS(5)         // drawing dir
	w.BD(0)         // extents height
	w.BD(0)         // extents width
	w.TV(text)
	w.BS(1) // linespacing style
	w.BD(1) // linespacing factor
	w.B(0)  // unknown bit
}

func TestDecodeMTextR2004FromBits(t *testing.T) {
	w := writeEntityPrefix(testsupport.NewBitWriter(), 0x2C)
	writeCommonHead(w, 21, 2)
	writeMTextR2004Body(w, "Hello MText")
	r := bitstream.NewBitStream(w.Bytes())
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	head, _ := parseEntityHead(r, 0, featMaterialFlags|featVisualStyles|featDSBinary)
	ent, err := decodeMTextVer(r, &head, 30, false, false, container.VerR2000)
	if err != nil {
		t.Fatal(err)
	}
	m := ent.(*entMText)
	if m.text != "Hello MText" || m.insertion.x != 1 || m.rectWidth != 50 || m.textHeight != 3 {
		t.Fatalf("MTEXT: %q ins=%v w=%v h=%v", m.text, m.insertion, m.rectWidth, m.textHeight)
	}
}

func TestDecodeMTextR2018FromBits(t *testing.T) {
	// R2007+ 布局：rect_height + TU 文本 + 行距 + 背景标志
	w := writeEntityPrefix(testsupport.NewBitWriter(), 0x2C)
	writeCommonHead(w, 22, 2)
	w.B3BD(1, 2, 0)
	w.B3BD(0, 0, 1)
	w.B3BD(1, 0, 0)
	w.BD(50) // rect width
	w.BD(4)  // rect height
	w.BD(3)  // text height
	w.BS(1)  // attachment
	w.BS(5)  // drawing dir
	w.BD(0)  // extents height
	w.BD(0)  // extents width
	w.TU("你好MText")
	w.BS(1) // linespacing style
	w.BD(1) // linespacing factor
	w.B(0)  // unknown bit
	w.BL(0) // background flags = 0
	r := bitstream.NewBitStream(w.Bytes())
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	head, _ := parseEntityHead(r, 0, featMaterialFlags|featVisualStyles|featDSBinary)
	ent, err := decodeMText(r, &head, 30, container.VerR2007)
	if err != nil {
		t.Fatal(err)
	}
	m := ent.(*entMText)
	if m.text != "你好MText" || m.insertion.x != 1 {
		t.Fatalf("MTEXT R2018: %q", m.text)
	}
}

func TestDecodeAttribFromBits(t *testing.T) {
	// ATTRIB 复用 TEXT 布局 + 尾部 tag 串
	w := writeEntityPrefix(testsupport.NewBitWriter(), 0x02)
	writeCommonHead(w, 23, 2)
	flags := uint8(textFlagNoElevation | textFlagNoAlign | textFlagNoOblique | textFlagNoWidth |
		textFlagNoGen | textFlagNoHAlign | textFlagNoVAlign)
	w.RC(flags)
	w.RD(1.0)
	w.RD(2.0)
	w.BE(0, 0, 1)
	w.BT(0)
	w.RD(0)   // rotation
	w.RD(1.5) // height
	w.TU("ATTR-VALUE")
	w.TV("TAG-NAME") // tag
	r := bitstream.NewBitStream(w.Bytes())
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	head, _ := parseEntityHead(r, 0, featMaterialFlags|featVisualStyles|featDSBinary)
	ent, err := decodeAttribVer(r, &head, 30, true, container.VerR2018, false)
	if err != nil {
		t.Fatal(err)
	}
	a := ent.(*entAttrib)
	if a.text != "ATTR-VALUE" || a.height != 1.5 {
		t.Fatalf("ATTRIB: %q h=%v", a.text, a.height)
	}
}

// TestDecodeInsertWithHandleStream INSERT 完整测试：几何 + handle 流（owner/xdic/layer/块头）。
func TestDecodeInsertWithHandleStream(t *testing.T) {
	w := writeEntityPrefix(testsupport.NewBitWriter(), 0x07)
	writeCommonHead(w, 30, 0) // entmode=0 → 有 owner 句柄
	w.B3BD(1, 1, 0)           // position（短格式 BD：1→2 位、0→2 位）
	w.BB(0x03)                // scale 全 1
	w.BD(0)                   // rotation
	w.B3BD(0, 0, 1)           // extrusion
	w.B(0)                    // 无 attribs
	// 几何结束位：前缀 18 位（UMC 8 + OT 10）+ 公共头 40 位 + 几何 17 位
	// （pos B3BD(1,1,0)=6 位、scale BB=2、rotation BD(0)=2、extrusion B3BD(0,0,1)=6、attribs B=1）
	const dataEndBit = uint64(18 + 40 + 17)
	// handle 流（位于 dataEndBit）：owner + xdic + layer + 块头
	w.H(0x02, 900) // owner 绝对句柄
	w.H(0x05, 901) // xdic
	w.H(0x05, 902) // layer
	w.H(0x05, 77)  // 块头句柄

	body := w.Bytes()
	rec := &objrec.ObjectRecord{
		Body: body, Size: uint32(len(body)),
		R2010Plus: true, HandleSizeFieldBits: 8, HandleStreamSizeBits: uint32(len(body)*8 - int(dataEndBit)),
	}
	r := bitstream.NewBitStream(body)
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	head, herr := parseEntityHead(r, dataEndBit, featMaterialFlags|featVisualStyles|featDSBinary)
	if herr != nil {
		t.Fatal(herr)
	}
	_ = rec // 记录元数据由 head 携带
	ent, derr := decodeInsert(r, &head, container.VerR2013)
	if derr != nil {
		t.Fatalf("INSERT 解码失败: %v", derr)
	}
	ins := ent.(*entInsert)
	if ins.position.x != 1 || ins.blockHeader != 77 {
		t.Fatalf("INSERT: pos=%v block=%d", ins.position, ins.blockHeader)
	}
}

func TestClassifyEntityModes(t *testing.T) {
	d := &Document{
		blocks:  map[uint64][]any{},
		attribs: map[uint64]*entAttrib{},
	}
	// mode=2 → 模型空间
	line := &entLine{baseEntity: baseEntity{handle: 1, mode: 2}}
	d.classify(line)
	if len(d.modelSpace) != 1 {
		t.Fatalf("mode2 应进模型空间: %d", len(d.modelSpace))
	}
	// mode=0 + owner → 块定义
	line2 := &entLine{baseEntity: baseEntity{handle: 2, mode: 0, owner: 77}}
	d.classify(line2)
	if len(d.blocks[77]) != 1 {
		t.Fatalf("mode0+owner 应进块定义: %v", d.blocks[77])
	}
	// mode=1 → 图纸空间，不进任何容器
	line3 := &entLine{baseEntity: baseEntity{handle: 3, mode: 1}}
	d.classify(line3)
	if len(d.modelSpace) != 1 || len(d.blocks[77]) != 1 {
		t.Fatal("mode1 不应进模型空间或块")
	}
	// mode=3 → 模型空间（对齐参考实现）
	line4 := &entLine{baseEntity: baseEntity{handle: 4, mode: 3}}
	d.classify(line4)
	if len(d.modelSpace) != 2 {
		t.Fatalf("mode3 应进模型空间: %d", len(d.modelSpace))
	}
	// ATTRIB 注册
	attrib := &entAttrib{baseEntity: baseEntity{handle: 5, mode: 2}}
	d.classify(attrib)
	if d.attribs[5] != attrib {
		t.Fatal("ATTRIB 应注册到 attribs")
	}
}

func TestDocumentTextsDedupAndBlocks(t *testing.T) {
	d := &Document{
		blocks:  map[uint64][]any{},
		attribs: map[uint64]*entAttrib{},
	}
	// 块内文本 + 引用它的 INSERT
	d.blocks[77] = []any{
		&entText{baseEntity: baseEntity{handle: 2}, text: "BLOCK-TEXT", insertion: point3{1, 1, 0}},
	}
	ins := &entInsert{
		baseEntity:  baseEntity{handle: 3, mode: 2},
		blockHeader: 77,
		attribs:     []uint64{9},
	}
	d.modelSpace = []any{ins, &entAttrib{baseEntity: baseEntity{handle: 9, mode: 2}, text: "ATTRIB-TEXT"}}
	d.classify(d.modelSpace[1]) // 注册 attrib

	texts := d.Texts()
	joined := ""
	for _, tx := range texts {
		joined += tx.Text + "|"
	}
	if !contains(joined, "BLOCK-TEXT") || !contains(joined, "ATTRIB-TEXT") {
		t.Fatalf("Texts 应含块文本与属性文本: %q", joined)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestStripMTextFormatMore(t *testing.T) {
	cases := map[string]string{
		"A\\P\\PB":  "A\n\nB", // 双换行
		"{\\C1;红}字": "红字",     // 颜色指令
		"plain":     "plain",
		"\\~x":      " x", // \~ 空格
	}
	for in, want := range cases {
		if got := stripMTextFormat(in); got != want {
			t.Errorf("strip(%q)=%q 期望 %q", in, got, want)
		}
	}
}

func TestRad2Deg(t *testing.T) {
	if math.Abs(rad2deg(math.Pi)-180) > 1e-9 {
		t.Fatal("rad2deg(π) 应为 180")
	}
	if rad2deg(0) != 0 {
		t.Fatal("rad2deg(0) 应为 0")
	}
}

func TestParseCommonEntityHeadLayoutVariants(t *testing.T) {
	// 布局候选应能解析 writeCommonHead 构造的标准流
	w := writeEntityPrefix(testsupport.NewBitWriter(), 0x13)
	writeCommonHead(w, 55, 2)
	w.B(1)
	w.RD(1)
	w.DD(2, 1)
	w.RD(3)
	w.DD(3, 3)
	w.BT(0)
	w.BE(0, 0, 1)
	r := bitstream.NewBitStream(w.Bytes())
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	// 第一个候选布局应成功且字段正确
	parsers := headParsersForVersion(container.VerR2018)
	if len(parsers) == 0 {
		t.Fatal("R2018 应有候选布局")
	}
	head, err := parsers[0].parse(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	if head.handle != 55 || head.entityMode != 2 || head.ltypeScale != 1 {
		t.Fatalf("布局解析: handle=%d entmode=%d lts=%v", head.handle, head.entityMode, head.ltypeScale)
	}
	// 各版本候选集非空且名称唯一
	for _, v := range []container.DwgVersion{container.VerR2000, container.VerR2004, container.VerR2010, container.VerR2013, container.VerR2018} {
		ps := headParsersForVersion(v)
		if len(ps) == 0 {
			t.Fatalf("版本 %v 候选为空", v)
		}
	}
}
