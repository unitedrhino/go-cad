// testwriter_test.go 测试专用位流构造器：与 bitStream 对称的写入原语，
// 用于在单元测试中构造任意位流以驱动解码器。
// 仅在测试文件中使用（_test.go 包内）。
package cad

import (
	"encoding/binary"
	"math"
)

// bitWriter 测试辅助：按 MSB 位序写入，与 bitStream 的读取语义一一对应。
type bitWriter struct {
	data []byte
	bit  int // 当前字节内已写入的高位偏移（0-7）
}

func newBitWriter() *bitWriter { return &bitWriter{} }

// bits 写入 n 位（MSB 在前），v 的高位对齐（低 n 位有效）。
func (w *bitWriter) bits(v uint64, n int) *bitWriter {
	for i := n - 1; i >= 0; i-- {
		b := byte((v >> uint(i)) & 1)
		if w.bit == 0 {
			w.data = append(w.data, 0)
		}
		if b == 1 {
			w.data[len(w.data)-1] |= 0x80 >> uint(w.bit)
		}
		w.bit = (w.bit + 1) % 8
	}
	return w
}

// B 写 1 位。
func (w *bitWriter) B(v uint8) *bitWriter { return w.bits(uint64(v), 1) }

// BB 写 2 位。
func (w *bitWriter) BB(v uint8) *bitWriter { return w.bits(uint64(v), 2) }

// B3 写 3 位。
func (w *bitWriter) B3(v uint8) *bitWriter { return w.bits(uint64(v), 3) }

// RC 写原始字节。
func (w *bitWriter) RC(v uint8) *bitWriter { return w.bits(uint64(v), 8) }

// RCS 写 N 个原始字节。
func (w *bitWriter) RCS(b []byte) *bitWriter {
	for _, v := range b {
		w.RC(v)
	}
	return w
}

// RS 写 16 位（小端语义：低字节在前）。
func (w *bitWriter) RS(v uint16) *bitWriter {
	return w.RC(byte(v)).RC(byte(v >> 8))
}

// RL 写 32 位（小端语义）。
func (w *bitWriter) RL(v uint32) *bitWriter {
	return w.RS(uint16(v)).RS(uint16(v >> 16))
}

// RD 写 64 位 IEEE 双精度（小端字节序）。
func (w *bitWriter) RD(v float64) *bitWriter {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], math.Float64bits(v))
	return w.RCS(b[:])
}

// BD 写位压缩双精度：00=RD 全量、01=1.0、10=0.0、11=0.0。
func (w *bitWriter) BD(v float64) *bitWriter {
	switch v {
	case 1.0:
		return w.BB(1)
	case 0.0:
		return w.BB(2)
	default:
		return w.BB(0).RD(v)
	}
}

// B3BD 写 3D 点（三个 BD）。
func (w *bitWriter) B3BD(x, y, z float64) *bitWriter { return w.BD(x).BD(y).BD(z) }

// DD 写差分双精度（与 default 相同 → 前缀 00；否则前缀 11 全量 RD）。
// 测试构造通常用全量或相同值两种即可覆盖解码分支。
func (w *bitWriter) DD(v, def float64) *bitWriter {
	if v == def {
		return w.BB(0)
	}
	return w.BB(3).RD(v)
}

// BT 写位厚度（0 → BD；1 → 0.0）。
func (w *bitWriter) BT(v float64) *bitWriter {
	if v == 0.0 {
		return w.B(1)
	}
	return w.B(0).BD(v)
}

// BE 写挤出方向（(0,0,1) → 1 位 1；否则 1 位 0 + 3BD）。
func (w *bitWriter) BE(x, y, z float64) *bitWriter {
	if x == 0 && y == 0 && z == 1 {
		return w.B(1)
	}
	return w.B(0).B3BD(x, y, z)
}

// BS 写位压缩短整型：值 ≤255 → 前缀 01+RC；256 → 11；0 → 10；其余 00+RS。
func (w *bitWriter) BS(v uint16) *bitWriter {
	switch {
	case v == 0:
		return w.BB(2)
	case v == 256:
		return w.BB(3)
	case v <= 255:
		return w.BB(1).RC(uint8(v))
	default:
		return w.BB(0).RS(v)
	}
}

// BL 写位压缩长整型：值 ≤255 → 01+RC；0 → 10；其余 00+RL。
func (w *bitWriter) BL(v uint32) *bitWriter {
	switch {
	case v == 0:
		return w.BB(2)
	case v <= 255:
		return w.BB(1).RC(uint8(v))
	default:
		return w.BB(0).RL(v)
	}
}

// BLL 写位压缩超长整型（3 位长度码 + 小端字节，对齐 readBLL）。
func (w *bitWriter) BLL(v uint64) *bitWriter {
	var b []byte
	for x := v; x > 0; x >>= 8 {
		b = append(b, byte(x))
	}
	if len(b) == 0 {
		b = []byte{0}
	}
	w.B3(uint8(len(b)))
	return w.RCS(b)
}

// MS 写模块化短整型（≤2 个 15 位组，LE 半字、高位为续组标志）。
func (w *bitWriter) MS(v uint32) *bitWriter {
	if v < 0x7FFF {
		return w.RS(uint16(v))
	}
	_ = v
	// 测试场景不构造超 15 位值
	return w.RS(uint16(v))
}

// UMC 写无符号模块化字符（7 位组小端累加，最高位为续组标志）。
func (w *bitWriter) UMC(v uint32) *bitWriter {
	x := v
	for {
		b := byte(x & 0x7F)
		x >>= 7
		if x != 0 {
			b |= 0x80
		}
		w.RC(b)
		if x == 0 {
			break
		}
	}
	return w
}

// OT 写 R2010+ 对象类型码：≤255 → 00+RC；0x1F0~0x2EF → 01+RC；否则 10+RS。
func (w *bitWriter) OT(v uint16) *bitWriter {
	switch {
	case v <= 0xFF:
		return w.BB(0).RC(uint8(v))
	case v >= 0x01F0 && v <= 0x02EF:
		return w.BB(1).RC(uint8(v - 0x01F0))
	default:
		return w.BB(2).RS(v)
	}
}

// H 写句柄（高 4 位 code、低 4 位字节数 counter、大端数值）。
func (w *bitWriter) H(code uint8, value uint64) *bitWriter {
	var b []byte
	for x := value; x > 0; x >>= 8 {
		b = append([]byte{byte(x)}, b...)
	}
	if len(b) == 0 {
		b = []byte{0}
	}
	return w.RC(code<<4 | uint8(len(b))).RCS(b)
}

// TV 写 codepage 文本（BS 长度 + 字节）。
func (w *bitWriter) TV(s string) *bitWriter {
	return w.BS(uint16(len(s))).RCS([]byte(s))
}

// TU 写 UTF-16 文本（BS 单元数 + LE 单元）。
func (w *bitWriter) TU(s string) *bitWriter {
	runes := []rune(s)
	units := make([]uint16, 0, len(runes))
	for _, r := range runes {
		if r > 0xFFFF {
			r -= 0x10000
			units = append(units, uint16(0xD800+(r>>10)), uint16(0xDC00+(r&0x3FF)))
		} else {
			units = append(units, uint16(r))
		}
	}
	w.BS(uint16(len(units)))
	for _, u := range units {
		w.RS(u)
	}
	return w
}

// CRC 占位写 2 字节。
func (w *bitWriter) CRC() *bitWriter { return w.RS(0) }

// bytes 输出位流（尾部补零对齐字节）。
func (w *bitWriter) bytes() []byte {
	if w.bit != 0 {
		w.data = append(w.data, 0) // 已在 bits 中按需 append；此处仅保证对齐
		w.bit = 0
	}
	return w.data
}
