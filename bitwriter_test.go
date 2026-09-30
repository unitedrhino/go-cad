// bitwriter 原语 round-trip 测试：write→read 逐原语比对，验证位级写入器
// 与解码端 bitStream 的对称性（dwgwrite 编码方向的基建门禁）。
package cad

import (
	"math"
	"strings"
	"testing"
)

func TestBitWriterRoundTrip(t *testing.T) {
	w := newEncWriter()
	type wfn func()
	type rfn func(*bitStream) any

	cases := []struct {
		name string
		w    wfn
		r    rfn
		want any
	}{
		{"RC", func() { w.writeRC(0xA7) }, func(r *bitStream) any { v, _ := r.readRC(); return v }, uint8(0xA7)},
		{"RS", func() { w.writeRS(0x1234) }, func(r *bitStream) any { v, _ := r.readRS(); return v }, uint16(0x1234)},
		{"RL", func() { w.writeRL(0xDEADBEEF) }, func(r *bitStream) any { v, _ := r.readRL(); return v }, uint32(0xDEADBEEF)},
		{"BLLc", func() { w.writeBLLc(0x123456789A) }, func(r *bitStream) any { v, _ := r.readBLL(); return v }, uint64(0x123456789A)},
		{"BLLc-small", func() { w.writeBLLc(0x42) }, func(r *bitStream) any { v, _ := r.readBLL(); return v }, uint64(0x42)},
		{"RD", func() { w.writeRD(3.14159) }, func(r *bitStream) any { v, _ := r.readRD(); return v }, 3.14159},
		{"B-true", func() { w.writeB(true) }, func(r *bitStream) any { v, _ := r.readB(); return v == 1 }, true},
		{"B-false", func() { w.writeB(false) }, func(r *bitStream) any { v, _ := r.readB(); return v == 1 }, false},
		{"BB", func() { w.writeBB(3) }, func(r *bitStream) any { v, _ := r.readBB(); return v }, uint8(3)},
		{"3B", func() { w.write3B(5) }, func(r *bitStream) any { v, _ := r.read3B(); return v }, uint8(5)},
		{"BS-small", func() { w.writeBS(200) }, func(r *bitStream) any { v, _ := r.readBS(); return v }, uint16(200)},
		{"BS-zero", func() { w.writeBS(0) }, func(r *bitStream) any { v, _ := r.readBS(); return v }, uint16(0)},
		{"BS-256", func() { w.writeBS(256) }, func(r *bitStream) any { v, _ := r.readBS(); return v }, uint16(256)},
		{"BS-large", func() { w.writeBS(0xFEDC) }, func(r *bitStream) any { v, _ := r.readBS(); return v }, uint16(0xFEDC)},
		{"BL-small", func() { w.writeBL(9) }, func(r *bitStream) any { v, _ := r.readBL(); return v }, uint32(9)},
		{"BL-zero", func() { w.writeBL(0) }, func(r *bitStream) any { v, _ := r.readBL(); return v }, uint32(0)},
		{"BL-large", func() { w.writeBL(0xCAFEBABE) }, func(r *bitStream) any { v, _ := r.readBL(); return v }, uint32(0xCAFEBABE)},

		{"BD-zero", func() { w.writeBD(0) }, func(r *bitStream) any { v, _ := r.readBD(); return v }, 0.0},
		{"BD-one", func() { w.writeBD(1) }, func(r *bitStream) any { v, _ := r.readBD(); return v }, 1.0},
		{"BD-pi", func() { w.writeBD(math.Pi) }, func(r *bitStream) any { v, _ := r.readBD(); return v }, math.Pi},
		{"H", func() { w.writeH(5, 2, 0x1F3) }, func(r *bitStream) any {
			h, _ := r.readH()
			return h
		}, handleRef{code: 5, counter: 2, value: 0x1F3}},
		{"TV", func() { w.writeTV("ISO-25") }, func(r *bitStream) any { v, _ := r.readTV(0); return v }, "ISO-25"},
		{"TU", func() { w.writeTU("Standard") }, func(r *bitStream) any {
			v, _ := r.readTU()
			// 真实格式长度含结尾 \0，比对时去除
			return strings.TrimSuffix(v, "\x00")
		}, "Standard"},
	}

	// 全部写入同一缓冲（保证有跨字节位对齐场景）
	for _, c := range cases {
		c.w()
	}
	r := newBitStream(w.bytes())
	for _, c := range cases {
		got := c.r(r)
		if !anyRoundTripEqual(c.want, got) {
			t.Errorf("%s: round-trip 不一致 got=%v want=%v", c.name, got, c.want)
		}
	}
}

// anyRoundTripEqual round-trip 值比较（类型统一后数值比较；
// slice/map 等不可比较类型用 reflect.DeepEqual 兜底）。
func anyRoundTripEqual(want, got any) bool {
	switch a := want.(type) {
	case float64:
		g, ok := got.(float64)
		return ok && math.Abs(g-a) < 1e-9
	case []uint64:
		g, ok := got.([]uint64)
		if !ok || len(a) != len(g) {
			return false
		}
		for i := range a {
			if a[i] != g[i] {
				return false
			}
		}
		return true
	case []float64:
		g, ok := got.([]float64)
		if !ok || len(a) != len(g) {
			return false
		}
		for i := range a {
			if math.Abs(a[i]-g[i]) >= 1e-9 {
				return false
			}
		}
		return true
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok || len(a) != len(g) {
			return false
		}
		for k, av := range a {
			bv, ok := g[k]
			if !ok {
				return false
			}
			if af, ok := av.(float64); ok {
				if bf, ok := bv.(float64); !ok || math.Abs(af-bf) >= 1e-9 {
					return false
				}
			} else if af, ok := av.(int64); ok {
				if bf, ok := bv.(int64); !ok || af != bf {
					return false
				}
			} else if av != bv {
				return false
			}
		}
		return true
	default:
		return want == got
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
		w := newEncWriter()
		w.writeDD(c.v, c.def)
		r := newBitStream(w.bytes())
		got, err := r.readDD(c.def)
		if err != nil {
			t.Fatalf("%s: 读回失败: %v", c.name, err)
		}
		if got != c.v {
			t.Fatalf("%s: 互逆不一致 got=%v want=%v", c.name, got, c.v)
		}
		if r.tellBits() != c.consumed {
			t.Fatalf("%s: 消费位数期望 %d 得到 %d（前缀或负载长度错误）", c.name, c.consumed, r.tellBits())
		}
	}
}
