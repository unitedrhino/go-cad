// bitwriter 原语 round-trip 测试：write→read 逐原语比对，验证位级写入器
// 与解码端 bitStream 的对称性（dwgwrite 编码方向的基建门禁）。
package bitstream

import (
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"math"
	"strings"
	"testing"
)

func TestBitWriterRoundTrip(t *testing.T) {
	w := NewEncWriter()
	type wfn func()
	type rfn func(*BitStream) any

	cases := []struct {
		name string
		w    wfn
		r    rfn
		want any
	}{
		{"RC", func() { w.WriteRC(0xA7) }, func(r *BitStream) any { v, _ := r.ReadRC(); return v }, uint8(0xA7)},
		{"RS", func() { w.WriteRS(0x1234) }, func(r *BitStream) any { v, _ := r.ReadRS(); return v }, uint16(0x1234)},
		{"RL", func() { w.WriteRL(0xDEADBEEF) }, func(r *BitStream) any { v, _ := r.ReadRL(); return v }, uint32(0xDEADBEEF)},
		{"BLLc", func() { w.WriteBLLc(0x123456789A) }, func(r *BitStream) any { v, _ := r.ReadBLL(); return v }, uint64(0x123456789A)},
		{"BLLc-small", func() { w.WriteBLLc(0x42) }, func(r *BitStream) any { v, _ := r.ReadBLL(); return v }, uint64(0x42)},
		{"RD", func() { w.WriteRD(3.14159) }, func(r *BitStream) any { v, _ := r.ReadRD(); return v }, 3.14159},
		{"B-true", func() { w.WriteB(true) }, func(r *BitStream) any { v, _ := r.ReadB(); return v == 1 }, true},
		{"B-false", func() { w.WriteB(false) }, func(r *BitStream) any { v, _ := r.ReadB(); return v == 1 }, false},
		{"BB", func() { w.WriteBB(3) }, func(r *BitStream) any { v, _ := r.ReadBB(); return v }, uint8(3)},
		{"3B", func() { w.write3B(5) }, func(r *BitStream) any { v, _ := r.read3B(); return v }, uint8(5)},
		{"BS-small", func() { w.WriteBS(200) }, func(r *BitStream) any { v, _ := r.ReadBS(); return v }, uint16(200)},
		{"BS-zero", func() { w.WriteBS(0) }, func(r *BitStream) any { v, _ := r.ReadBS(); return v }, uint16(0)},
		{"BS-256", func() { w.WriteBS(256) }, func(r *BitStream) any { v, _ := r.ReadBS(); return v }, uint16(256)},
		{"BS-large", func() { w.WriteBS(0xFEDC) }, func(r *BitStream) any { v, _ := r.ReadBS(); return v }, uint16(0xFEDC)},
		{"BL-small", func() { w.WriteBL(9) }, func(r *BitStream) any { v, _ := r.ReadBL(); return v }, uint32(9)},
		{"BL-zero", func() { w.WriteBL(0) }, func(r *BitStream) any { v, _ := r.ReadBL(); return v }, uint32(0)},
		{"BL-large", func() { w.WriteBL(0xCAFEBABE) }, func(r *BitStream) any { v, _ := r.ReadBL(); return v }, uint32(0xCAFEBABE)},

		{"BD-zero", func() { w.WriteBD(0) }, func(r *BitStream) any { v, _ := r.ReadBD(); return v }, 0.0},
		{"BD-one", func() { w.WriteBD(1) }, func(r *BitStream) any { v, _ := r.ReadBD(); return v }, 1.0},
		{"BD-pi", func() { w.WriteBD(math.Pi) }, func(r *BitStream) any { v, _ := r.ReadBD(); return v }, math.Pi},
		{"H", func() { w.WriteH(5, 2, 0x1F3) }, func(r *BitStream) any {
			h, _ := r.ReadH()
			return h
		}, handleRef{Code: 5, Counter: 2, Value: 0x1F3}},
		{"TV", func() { w.WriteTV("ISO-25") }, func(r *BitStream) any { v, _ := r.ReadTV(0); return v }, "ISO-25"},
		{"TU", func() { w.WriteTU("Standard") }, func(r *BitStream) any {
			v, _ := r.ReadTU()
			// 真实格式长度含结尾 \0，比对时去除
			return strings.TrimSuffix(v, "\x00")
		}, "Standard"},
	}

	// 全部写入同一缓冲（保证有跨字节位对齐场景）
	for _, c := range cases {
		c.w()
	}
	r := NewBitStream(w.Bytes())
	for _, c := range cases {
		got := c.r(r)
		if !testsupport.AnyRoundTripEqual(c.want, got) {
			t.Errorf("%s: round-trip 不一致 got=%v want=%v", c.name, got, c.want)
		}
	}
}

// TestWriteDDReadDDRoundTrip writeDD/readDD 四分支互逆门禁：same(0)/低 4 字节(1)/
// 次高 2 字节(2)/全量(3)。消费位数与分支一一对应（2/34/50/66），间接校验写出前缀。
func TestWriteDDReadDDRoundTrip(t *testing.T) {
	one := math.Float64frombits(0x3FF0000000000000) // 与 1.0 同高 4 字节、低 4 字节可变
	cases := []struct {
		name     string
		v, def   float64
		consumed uint64 // readDD 应消费的总位数
	}{
		{"same-0", 3.5, 3.5, 2},
		{"low4-1", math.Float64frombits(0x3FF0000000000001), one, 34},
		{"mid6-2", math.Float64frombits(0x3FF8AABBCCDDEEFF), 1.5, 50},
		{"full-3", -5.0, 1.0, 66},
	}
	for _, c := range cases {
		w := NewEncWriter()
		w.WriteDD(c.v, c.def)
		r := NewBitStream(w.Bytes())
		got, err := r.ReadDD(c.def)
		if err != nil {
			t.Fatalf("%s: 读回失败: %v", c.name, err)
		}
		if got != c.v {
			t.Fatalf("%s: 互逆不一致 got=%v want=%v", c.name, got, c.v)
		}
		if r.TellBits() != c.consumed {
			t.Fatalf("%s: 消费位数期望 %d 得到 %d（前缀或负载长度错误）", c.name, c.consumed, r.TellBits())
		}
	}
}
