// cad_test.go cad 包单元测试：位级读取原语、LZ77 解压、对象图差分解码、
// 文本评分与 MTEXT 格式剥离。
package cad

import (
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"math"
	"testing"
)

func TestBitReaderReadB(t *testing.T) {
	r := bitstream.NewBitStream([]byte{0b10110000})
	if v, _ := r.ReadB(); v != 1 {
		t.Fatalf("期望 1 得到 %d", v)
	}
	if v, _ := r.ReadB(); v != 0 {
		t.Fatalf("期望 0 得到 %d", v)
	}
	if v, _ := r.ReadB(); v != 1 {
		t.Fatalf("期望 1 得到 %d", v)
	}
}

func TestBitReaderReadBS(t *testing.T) {
	// BS 前缀 01 → 后续 RC 单字节值 0x2A
	r := bitstream.NewBitStream(testsupport.BitsToBytes("01" + "00101010"))
	v, err := r.ReadBS()
	if err != nil {
		t.Fatal(err)
	}
	if v != 0x2A {
		t.Fatalf("期望 0x2A 得到 %#x", v)
	}
}

func TestBitReaderReadBD(t *testing.T) {
	// BD 前缀 01 → 1.0；前缀 10 → 0.0
	r := bitstream.NewBitStream(testsupport.BitsToBytes("01" + "10"))
	v1, err := r.ReadBD()
	if err != nil {
		t.Fatal(err)
	}
	if v1 != 1.0 {
		t.Fatalf("期望 1.0 得到 %v", v1)
	}
	v2, _ := r.ReadBD()
	if v2 != 0.0 {
		t.Fatalf("期望 0.0 得到 %v", v2)
	}
}

func TestBitReaderReadDD(t *testing.T) {
	// DD 前缀 00 → 直接使用默认值
	r := bitstream.NewBitStream(testsupport.BitsToBytes("00"))
	v, err := r.ReadDD(3.14)
	if err != nil {
		t.Fatal(err)
	}
	if v != 3.14 {
		t.Fatalf("期望默认值 3.14 得到 %v", v)
	}
}

func TestBitReaderReadH(t *testing.T) {
	// code=0 counter=2 值 0x06CE（1742）
	r := bitstream.NewBitStream([]byte{0x02, 0x06, 0xCE})
	h, err := r.ReadH()
	if err != nil {
		t.Fatal(err)
	}
	if h.Code != 0 || h.Counter != 2 || h.Value != 1742 {
		t.Fatalf("句柄解析错误: %+v", h)
	}
}

func TestBitReaderReadTU(t *testing.T) {
	// BS 长度=1（前缀 00 + RS 16 位小端 1）+ UTF-16LE 单元 "A"(0x0041)
	r := bitstream.NewBitStream(testsupport.BitsToBytes("00" + "00000001" + "00000000" + "01000001" + "00000000"))
	v, err := r.ReadTU()
	if err != nil {
		t.Fatal(err)
	}
	if v != "A" {
		t.Fatalf("期望 %q 得到 %q", "A", v)
	}
}

func TestReadHandleReferenceRelative(t *testing.T) {
	// code=0x0A（软引用 +offset），counter=1 值 5：头字节 0xA1（code 高 4 位 + counter 低 4 位）
	r := bitstream.NewBitStream([]byte{0xA1, 0x05})
	v, err := readHandleReference(r, 100)
	if err != nil {
		t.Fatal(err)
	}
	if v != 105 {
		t.Fatalf("期望 105 得到 %d", v)
	}
	// code=0x06（+1），counter=0：头字节 0x60
	r2 := bitstream.NewBitStream([]byte{0x60})
	v2, _ := readHandleReference(r2, 100)
	if v2 != 101 {
		t.Fatalf("期望 101 得到 %d", v2)
	}
	// code=0x0C（-offset），counter=1 值 3：头字节 0xC1
	r3 := bitstream.NewBitStream([]byte{0xC1, 0x03})
	v3, _ := readHandleReference(r3, 100)
	if v3 != 97 {
		t.Fatalf("期望 97 得到 %d", v3)
	}
}

func TestParseObjectMapHandles(t *testing.T) {
	// 块大小 8（含自身）+ 3 个条目 + CRC + 终止块
	// 条目: (handle +5, offset +10), (+84, +1), (+30, +5)
	data := []byte{
		0x00, 0x08,
		0x05, 0x0A,
		0x54, 0x01,
		0x1E, 0x05,
		0x00, 0x00, // CRC
		0x00, 0x02, // 终止块
	}
	objects, err := parseObjectMapHandles(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 3 {
		t.Fatalf("期望 3 个条目得到 %d", len(objects))
	}
	expect := []objectRef{{5, 10}, {89, 11}, {119, 16}}
	for i, e := range expect {
		if objects[i] != e {
			t.Fatalf("条目 %d: 期望 %+v 得到 %+v", i, e, objects[i])
		}
	}
}

func TestParseObjectMapHandlesSkipsNegative(t *testing.T) {
	// 第二条 offset 差分为负导致累计为负 → 跳过该条目
	data := []byte{
		0x00, 0x08,
		0x05, 0x0A, // (5, 10)
		0x01, 0x54, // handle+1, offset-20 → 非法，跳过
		0x1E, 0x05, // (35, 15)
		0x00, 0x00,
		0x00, 0x02,
	}
	objects, err := parseObjectMapHandles(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 2 || objects[1].handle != 35 || objects[1].offset != 15 {
		t.Fatalf("负差分跳过结果错误: %+v", objects)
	}
}

func TestDecompressLZ77Literal(t *testing.T) {
	// 字面量长度字段 0x05 → 5+3=8 字节字面量
	src := append([]byte{0x05}, []byte("ABCDEFGH")...)
	out, err := decompressLZ77(src, 8)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "ABCDEFGH" {
		t.Fatalf("字面量解压错误: %q", out)
	}
}

func TestDecompressLZ77Terminator(t *testing.T) {
	// 字面量长度字段 0x01 → 1+3=4 字节 "ABCD"，随后 0x11 终止
	out, err := decompressLZ77([]byte{0x01, 'A', 'B', 'C', 'D', 0x11}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "ABCD" {
		t.Fatalf("期望 %q 得到 %q", "ABCD", out)
	}
}

func TestLCGMagicSequence(t *testing.T) {
	seq := lcgKeyStream()
	// LCG(seed=1) 首字节: seed=1*0x343FD+0x269EC3=0x29E3C0 → >>16 = 0x29
	if seq[0] != 0x29 {
		t.Fatalf("LCG 序列首字节期望 0x29 得到 %#x", seq[0])
	}
}

func TestReadHeaderData(t *testing.T) {
	// 构造加密头：明文字段 + XOR 加密
	hdr := make([]byte, headerSize)
	// 0x50 处: page map id(u32) + address(u64) + section map id(u32)
	put := func(off int, b []byte) { copy(hdr[off:], b) }
	u32 := func(v uint32) []byte { return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)} }
	u64 := func(v uint64) []byte {
		out := make([]byte, 8)
		for i := 0; i < 8; i++ {
			out[i] = byte(v >> (i * 8))
		}
		return out
	}
	put(0x50, u32(23))
	put(0x54, u64(1000))
	put(0x5C, u32(7))

	magic := lcgKeyStream()
	encrypted := make([]byte, headerSize)
	for i := range encrypted {
		encrypted[i] = hdr[i] ^ magic[i]
	}
	file := make([]byte, headerOffset+headerSize)
	copy(file[headerOffset:], encrypted)

	got, err := decryptR2004Header(file)
	if err != nil {
		t.Fatal(err)
	}
	if got.sectionPageMapAddress != 1000 || got.sectionMapID != 7 {
		t.Fatalf("容器头解析错误: %+v", got)
	}
}

func TestStripMTextFormat(t *testing.T) {
	cases := map[string]string{
		"Hello\\PWorld":  "Hello\nWorld", // \P 换行
		"{\\fSimHei;中文}": "中文",           // 字体指令剥离
		"A\\X B":         "A  B",         // \X 占位空格 + 原空格
		"100\\S上+下;mm":   "100mm",        // 分数指令剥离到分号
	}
	for in, want := range cases {
		if got := stripMTextFormat(in); got != want {
			t.Errorf("stripMTextFormat(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestScoreText(t *testing.T) {
	if decodedTextScore("正常文本") <= 0 {
		t.Error("可读文本应为正分")
	}
	if decodedTextScore("\x00\x01\x02") >= 0 {
		t.Error("控制字符应为负分")
	}
}

func TestACIRGB(t *testing.T) {
	r, g, b, ok := aciColor(1, true)
	if !ok || r != 255 || g != 0 || b != 0 {
		t.Fatalf("ACI 1 应为红色: (%d,%d,%d,%v)", r, g, b, ok)
	}
	// ACI 7 白底输出黑色
	r, _, _, _ = aciColor(7, true)
	if r != 0 {
		t.Fatalf("白底 ACI 7 应输出黑色，得到 %d", r)
	}
	// ByLayer/ByBlock 无颜色
	if _, _, _, ok := aciColor(256, true); ok {
		t.Error("ACI 256 不应产生颜色")
	}
	// 灰阶
	r, _, _, _ = aciColor(255, true)
	if r != 255 {
		t.Fatalf("ACI 255 应为白色，得到 %d", r)
	}
}

func TestTrueColorRGB(t *testing.T) {
	r, g, b := splitTrueColor(0x1F2E3D)
	if r != 0x1F || g != 0x2E || b != 0x3D {
		t.Fatalf("true color 拆分错误: (%#x,%#x,%#x)", r, g, b)
	}
}

func TestXformCompose(t *testing.T) {
	// 外层平移 (10,20)，内层缩放 2 + 平移 (1,1)：点 (1,1) → 内层 (3,3) → 外层 (13,23)
	outer := xform{sx: 1, sy: 1, cos: 1, tx: 10, ty: 20}
	inner := xform{sx: 2, sy: 2, cos: 1, tx: 1, ty: 1}
	got := outer.compose(inner).apply(point2{1, 1})
	if math.Abs(got.x-13) > 1e-9 || math.Abs(got.y-23) > 1e-9 {
		t.Fatalf("复合变换错误: (%v,%v)", got.x, got.y)
	}
}

func TestDetectVersion(t *testing.T) {
	for magic, want := range map[string]dwgVersion{
		"AC1015": verR2000,
		"AC1018": verR2004,
		"AC1024": verR2010,
		"AC1027": verR2013,
		"AC1032": verR2018,
	} {
		v, err := detectVersion([]byte(magic + "xxxxxxxx"))
		if err != nil || v != want {
			t.Errorf("%s 识别失败: %v %v", magic, v, err)
		}
	}
	if _, err := detectVersion([]byte("XXXXXX")); err == nil {
		t.Error("非法魔数应报错")
	}
}
