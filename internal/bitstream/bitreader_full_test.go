// bitreader_full_test.go 位读取器全原语单元测试：与 testwriter 对称构造位流，
// 覆盖 RC/RS/RL/RCS/RD/BD/3BD/BT/BE/BL/BLL/MS/UMC/OT/TV/TU/CRC 与对齐/定位。
package bitstream

import (
	"math"
	"testing"

	"github.com/unitedrhino/go-cad/internal/testsupport"
)

func TestBitReaderReadRC(t *testing.T) {
	r := NewBitStream([]byte{0xAB, 0xCD})
	if v, _ := r.ReadRC(); v != 0xAB {
		t.Fatalf("期望 0xAB 得到 %#x", v)
	}
	if v, _ := r.ReadRC(); v != 0xCD {
		t.Fatalf("期望 0xCD 得到 %#x", v)
	}
	// 非对齐 RC：跨字节读取
	r2 := NewBitStream(testsupport.BitsToBytes("1" + "10101010")) // 1 位后跟 0xAA
	_, _ = r2.ReadB()
	if v, _ := r2.ReadRC(); v != 0xAA {
		t.Fatalf("非对齐 RC 期望 0xAA 得到 %#x", v)
	}
}

func TestBitReaderReadRSRL(t *testing.T) {
	// RS 小端：低字节在前
	r := NewBitStream([]byte{0x34, 0x12})
	if v, _ := r.ReadRS(); v != 0x1234 {
		t.Fatalf("RS 期望 0x1234 得到 %#x", v)
	}
	// RL = 两个 RS 拼接
	r2 := NewBitStream([]byte{0x78, 0x56, 0x34, 0x12})
	if v, _ := r2.ReadRL(); v != 0x12345678 {
		t.Fatalf("RL 期望 0x12345678 得到 %#x", v)
	}
}

func TestBitReaderReadRCS(t *testing.T) {
	r := NewBitStream([]byte{1, 2, 3, 4, 5})
	b, err := r.ReadRCS(3)
	if err != nil || len(b) != 3 || b[0] != 1 || b[2] != 3 {
		t.Fatalf("RCS 读取错误: %v %v", b, err)
	}
	// 越界报错
	if _, err := NewBitStream([]byte{9}).ReadRCS(2); err == nil {
		t.Error("RCS 越界应报错")
	}
}

func TestBitReaderReadBDBranches(t *testing.T) {
	// 前缀 01 → 1.0
	r := testsupport.NewBitWriter().BB(1).Bytes()
	if v, _ := NewBitStream(r).ReadBD(); v != 1.0 {
		t.Fatalf("BD(01) 期望 1.0 得到 %v", v)
	}
	// 前缀 10 → 0.0
	r = testsupport.NewBitWriter().BB(2).Bytes()
	if v, _ := NewBitStream(r).ReadBD(); v != 0.0 {
		t.Fatalf("BD(10) 期望 0.0 得到 %v", v)
	}
	// 前缀 00 → RD 全量
	r = testsupport.NewBitWriter().BB(0).RD(3.25).Bytes()
	if v, _ := NewBitStream(r).ReadBD(); v != 3.25 {
		t.Fatalf("BD(00+RD) 期望 3.25 得到 %v", v)
	}
}

func TestBitReaderRead3BD(t *testing.T) {
	r := NewBitStream(testsupport.NewBitWriter().B3BD(1.5, 2.0, 0).Bytes())
	x, y, z, err := r.Read3BD()
	if err != nil || x != 1.5 || y != 2.0 || z != 0 {
		t.Fatalf("3BD 错误: (%v,%v,%v) %v", x, y, z, err)
	}
}

func TestBitReaderReadBTBE(t *testing.T) {
	// BT 标志 1 → 0.0
	if v, _ := NewBitStream(testsupport.NewBitWriter().BT(0).Bytes()).ReadBT(); v != 0.0 {
		t.Fatalf("BT(0) 期望 0.0 得到 %v", v)
	}
	// BT 标志 0 + BD
	if v, _ := NewBitStream(testsupport.NewBitWriter().BT(2.5).Bytes()).ReadBT(); v != 2.5 {
		t.Fatalf("BT(2.5) 期望 2.5 得到 %v", v)
	}
	// BE 默认 (0,0,1)
	x, y, z, err := NewBitStream(testsupport.NewBitWriter().BE(0, 0, 1).Bytes()).ReadBE()
	if err != nil || x != 0 || y != 0 || z != 1 {
		t.Fatalf("BE 默认错误: (%v,%v,%v) %v", x, y, z, err)
	}
	// BE 自定义方向
	x, y, z, _ = NewBitStream(testsupport.NewBitWriter().BE(0, 1, 0).Bytes()).ReadBE()
	if x != 0 || y != 1 || z != 0 {
		t.Fatalf("BE(0,1,0) 错误: (%v,%v,%v)", x, y, z)
	}
}

func TestBitReaderReadBLBLL(t *testing.T) {
	// BL 前缀 01 + RC
	r := NewBitStream(testsupport.NewBitWriter().BL(200).Bytes())
	if v, _ := r.ReadBL(); v != 200 {
		t.Fatalf("BL(200) 得到 %d", v)
	}
	// BL 前缀 00 + RL
	r = NewBitStream(testsupport.NewBitWriter().BL(70000).Bytes())
	if v, _ := r.ReadBL(); v != 70000 {
		t.Fatalf("BL(70000) 得到 %d", v)
	}
	// BLL：3 位长度码 + 小端数据（码值 2 → RS 小端）
	r = NewBitStream(testsupport.NewBitWriter().BLL(0x0102).Bytes())
	if v, _ := r.ReadBLL(); v != 0x0102 {
		t.Fatalf("BLL(0x0102) 得到 %#x", v)
	}
}

func TestBitReaderReadMS(t *testing.T) {
	// MS：单 15 位组（<0x7FFF 无续位标志）
	r := NewBitStream([]byte{0x34, 0x12})
	if v, _ := r.ReadMS(); v != 0x1234 {
		t.Fatalf("MS 期望 0x1234 得到 %#x", v)
	}
}

func TestBitReaderReadUMC(t *testing.T) {
	// 单字节
	r := NewBitStream([]byte{0x05})
	if v, _ := r.ReadUMC(); v != 5 {
		t.Fatalf("UMC(5) 得到 %d", v)
	}
	// 双字节（高位置续组标志）：0x85 0x00 → 5 | 0<<7 = 5
	r2 := NewBitStream([]byte{0x85, 0x00})
	if v, _ := r2.ReadUMC(); v != 5 {
		t.Fatalf("UMC(0x85 0x00) 得到 %d", v)
	}
	// 多字节：300 = 0b100101100 → 低 7 位 0101100(0x2C|0x80) 高 0x02
	r3 := NewBitStream([]byte{0xAC, 0x02})
	if v, _ := r3.ReadUMC(); v != 300 {
		t.Fatalf("UMC(300) 得到 %d", v)
	}
}

func TestBitReaderReadOT(t *testing.T) {
	// opcode 00 + RC
	r := NewBitStream(testsupport.NewBitWriter().OT(0x13).Bytes())
	if v, _ := r.ReadOT(); v != 0x13 {
		t.Fatalf("OT(0x13) 得到 %#x", v)
	}
	// opcode 01 + RC → +0x1F0
	r = NewBitStream(testsupport.NewBitWriter().OT(0x200).Bytes())
	if v, _ := r.ReadOT(); v != 0x200 {
		t.Fatalf("OT(0x200) 得到 %#x", v)
	}
	// opcode 10 + RS
	r = NewBitStream(testsupport.NewBitWriter().OT(0x0777).Bytes())
	if v, _ := r.ReadOT(); v != 0x0777 {
		t.Fatalf("OT(0x777) 得到 %#x", v)
	}
}

func TestBitReaderReadTVGBK(t *testing.T) {
	// GBK 编码「中文」= D6 D0 CE C4（codepage 31/39）
	gbk := []byte{0xD6, 0xD0, 0xCE, 0xC4}
	r := NewBitStream(testsupport.NewBitWriter().BS(uint16(len(gbk))).RCS(gbk).Bytes())
	v, err := r.ReadTV(31)
	if err != nil {
		t.Fatal(err)
	}
	if v != "中文" {
		t.Fatalf("GBK TV 期望「中文」得到 %q", v)
	}
	// codepage 0/1 按 ASCII 直取
	r2 := NewBitStream(testsupport.NewBitWriter().BS(3).RCS([]byte("abc")).Bytes())
	if v, _ := r2.ReadTV(0); v != "abc" {
		t.Fatalf("ASCII TV 得到 %q", v)
	}
}

func TestBitReaderReadCRCAlign(t *testing.T) {
	// readCRC 语义：位未对齐时丢弃当前字节剩余位、跳到下一字节边界。
	// 构造：字节 0 = 1 位占位 + 7 位填充；RS(0x1234) 从字节 1 起。
	r := NewBitStream(testsupport.NewBitWriter().B(1).BB(0).B3(0).BB(2).RS(0x1234).Bytes())
	_, _ = r.ReadB() // 消费占位位，bit=1
	if v, _ := r.ReadCRC(); v != 0x1234 {
		t.Fatalf("CRC 对齐后期望 0x1234 得到 %#x", v)
	}
	// 已对齐时直接读
	r2 := NewBitStream([]byte{0x34, 0x12})
	if v, _ := r2.ReadCRC(); v != 0x1234 {
		t.Fatalf("对齐 CRC 期望 0x1234 得到 %#x", v)
	}
}

func TestBitReaderAlignSetPos(t *testing.T) {
	r := NewBitStream([]byte{0xFF, 0x00})
	_, _ = r.ReadB()
	r.AlignByte()
	if r.Sub != 0 || r.Pos != 1 {
		t.Fatalf("alignByte 后位=%d 字节=%d", r.Sub, r.Pos)
	}
	// setBitPos / tellBits 往返
	r.SetBitPos(19)
	if r.TellBits() != 19 {
		t.Fatalf("setBitPos(19) 后 tellBits=%d", r.TellBits())
	}
	// pos/setPos 快照
	r.SetBitPos(3)
	b, Bit := r.Cursor()
	r.SetBitPos(10)
	r.Restore(b, Bit)
	if r.TellBits() != 3 {
		t.Fatalf("快照恢复失败: tellBits=%d", r.TellBits())
	}
}

func TestBitReaderEOFAfterBits(t *testing.T) {
	r := NewBitStream([]byte{0xFF})
	for i := 0; i < 8; i++ {
		if _, err := r.ReadB(); err != nil {
			t.Fatalf("第 %d 位不应报错: %v", i, err)
		}
	}
	if _, err := r.ReadB(); err == nil {
		t.Error("流耗尽后 readB 应报错")
	}
	// 对齐状态下 readRC 越界报错
	r2 := NewBitStream([]byte{0x01})
	if _, err := r2.ReadRC(); err != nil {
		t.Fatalf("正常 RC 不应报错: %v", err)
	}
	if _, err := r2.ReadRC(); err == nil {
		t.Error("流耗尽后 readRC 应报错")
	}
}

func TestReadDDPrefixes(t *testing.T) {
	// 前缀 1：低 4 字节替换
	def := 1.0
	var want [8]byte = f64LeBytes(def)
	want[0], want[1], want[2], want[3] = 0x11, 0x22, 0x33, 0x44
	r := NewBitStream(testsupport.NewBitWriter().BB(1).RC(0x11).RC(0x22).RC(0x33).RC(0x44).Bytes())
	v, err := r.ReadDD(def)
	if err != nil {
		t.Fatal(err)
	}
	if leF64(want) != v {
		t.Fatalf("DD 前缀 1 期望 %v 得到 %v", leF64(want), v)
	}
	// 前缀 2：仅更新 6 字节——先 data[4]、data[5]，再 data[0..4)；
	// data[6..8) 沿用默认值（符号+指数高位不变）。
	def2 := 1.5
	r2 := NewBitStream(testsupport.NewBitWriter().BB(2).
		RC(0xAA).RC(0xBB).                   // → data[4]、data[5]
		RC(0x11).RC(0x22).RC(0x33).RC(0x44). // → data[0..4)
		Bytes())
	var exp [8]byte = f64LeBytes(def2)
	exp[4], exp[5] = 0xAA, 0xBB
	exp[0], exp[1], exp[2], exp[3] = 0x11, 0x22, 0x33, 0x44
	v2, _ := r2.ReadDD(def2)
	if leF64(exp) != v2 {
		t.Fatalf("DD 前缀 2 期望 %v 得到 %v", leF64(exp), v2)
	}
	// 前缀 3：全量 RD
	r3 := NewBitStream(testsupport.NewBitWriter().BB(3).RD(7.75).Bytes())
	if v3, _ := r3.ReadDD(0); v3 != 7.75 {
		t.Fatalf("DD 前缀 3 期望 7.75 得到 %v", v3)
	}
}

func TestF64RoundTrip(t *testing.T) {
	for _, v := range []float64{0, 1, -1.5, math.MaxFloat64 / 2, math.SmallestNonzeroFloat64} {
		if leF64(f64LeBytes(v)) != v {
			t.Fatalf("f64 往返失败: %v", v)
		}
	}
}

func TestUTF16Decode(t *testing.T) {
	// 基本平面
	if s := string(Utf16Decode([]uint16{0x0041, 0x4E2D})); s != "A中" {
		t.Fatalf("UTF16 基本平面: %q", s)
	}
	// 代理对（U+1F600）
	hi, lo := uint16(0xD83D), uint16(0xDE00)
	if s := string(Utf16Decode([]uint16{hi, lo})); s != "\U0001F600" {
		t.Fatalf("UTF16 代理对: %q", s)
	}
	// 孤立高位代理 → 替换符
	if s := string(Utf16Decode([]uint16{hi})); s != "�" {
		t.Fatalf("孤立代理: %q", s)
	}
}

func TestDecodeCodepage(t *testing.T) {
	if DecodeCodepage(nil, 30) != "" {
		t.Error("空串应返回空")
	}
	if DecodeCodepage([]byte("hi"), 0) != "hi" {
		t.Error("codepage 0 应按 ASCII")
	}
}
