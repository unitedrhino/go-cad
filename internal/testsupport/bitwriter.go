// bitwriter.go 测试专用位流构造器：与 bitStream 对称的写入原语，用于在
// 单元测试中构造任意位流以驱动解码器。本包为纯测试支撑包，不依赖仓库内
// 任何其他包，可被所有 internal 子包的测试安全导入（不引入测试导入环）。
package testsupport

import (
	"encoding/binary"
	"math"
)

// BitWriter 测试辅助：按 MSB 位序写入，与 bitStream 的读取语义一一对应。
type BitWriter struct {
	Data []byte
	Bit  int // 当前字节内已写入的高位偏移（0-7）
}

// NewBitWriter 创建空位流构造器。
func NewBitWriter() *BitWriter { return &BitWriter{} }

// bits 写入 n 位（MSB 在前），v 的高位对齐（低 n 位有效）。
func (w *BitWriter) bits(v uint64, n int) *BitWriter {
	for i := n - 1; i >= 0; i-- {
		b := byte((v >> uint(i)) & 1)
		if w.Bit == 0 {
			w.Data = append(w.Data, 0)
		}
		if b == 1 {
			w.Data[len(w.Data)-1] |= 0x80 >> uint(w.Bit)
		}
		w.Bit = (w.Bit + 1) % 8
	}
	return w
}

// B 写 1 位。
func (w *BitWriter) B(v uint8) *BitWriter { return w.bits(uint64(v), 1) }

// BB 写 2 位。
func (w *BitWriter) BB(v uint8) *BitWriter { return w.bits(uint64(v), 2) }

// B3 写 3 位。
func (w *BitWriter) B3(v uint8) *BitWriter { return w.bits(uint64(v), 3) }

// RC 写原始字节。
func (w *BitWriter) RC(v uint8) *BitWriter { return w.bits(uint64(v), 8) }

// RCS 写 N 个原始字节。
func (w *BitWriter) RCS(b []byte) *BitWriter {
	for _, v := range b {
		w.RC(v)
	}
	return w
}

// RS 写 16 位（小端语义：低字节在前）。
func (w *BitWriter) RS(v uint16) *BitWriter {
	return w.RC(byte(v)).RC(byte(v >> 8))
}

// RL 写 32 位（小端语义）。
func (w *BitWriter) RL(v uint32) *BitWriter {
	return w.RS(uint16(v)).RS(uint16(v >> 16))
}

// RD 写 64 位 IEEE 双精度（小端字节序）。
func (w *BitWriter) RD(v float64) *BitWriter {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], math.Float64bits(v))
	return w.RCS(b[:])
}

// BD 写位压缩双精度：00=RD 全量、01=1.0、10=0.0、11=0.0。
func (w *BitWriter) BD(v float64) *BitWriter {
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
func (w *BitWriter) B3BD(x, y, z float64) *BitWriter { return w.BD(x).BD(y).BD(z) }

// DD 写差分双精度（与 default 相同 → 前缀 00；否则前缀 11 全量 RD）。
// 测试构造通常用全量或相同值两种即可覆盖解码分支。
func (w *BitWriter) DD(v, def float64) *BitWriter {
	if v == def {
		return w.BB(0)
	}
	return w.BB(3).RD(v)
}

// BT 写位厚度（0 → BD；1 → 0.0）。
func (w *BitWriter) BT(v float64) *BitWriter {
	if v == 0.0 {
		return w.B(1)
	}
	return w.B(0).BD(v)
}

// BE 写挤出方向（(0,0,1) → 1 位 1；否则 1 位 0 + 3BD）。
func (w *BitWriter) BE(x, y, z float64) *BitWriter {
	if x == 0 && y == 0 && z == 1 {
		return w.B(1)
	}
	return w.B(0).B3BD(x, y, z)
}

// BS 写位压缩短整型：值 ≤255 → 前缀 01+RC；256 → 11；0 → 10；其余 00+RS。
func (w *BitWriter) BS(v uint16) *BitWriter {
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
func (w *BitWriter) BL(v uint32) *BitWriter {
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
func (w *BitWriter) BLL(v uint64) *BitWriter {
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
func (w *BitWriter) MS(v uint32) *BitWriter {
	if v < 0x7FFF {
		return w.RS(uint16(v))
	}
	_ = v
	// 测试场景不构造超 15 位值
	return w.RS(uint16(v))
}

// UMC 写无符号模块化字符（7 位组小端累加，最高位为续组标志）。
func (w *BitWriter) UMC(v uint32) *BitWriter {
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
func (w *BitWriter) OT(v uint16) *BitWriter {
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
func (w *BitWriter) H(code uint8, value uint64) *BitWriter {
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
func (w *BitWriter) TV(s string) *BitWriter {
	return w.BS(uint16(len(s))).RCS([]byte(s))
}

// TU 写 UTF-16 文本（BS 单元数 + LE 单元）。
func (w *BitWriter) TU(s string) *BitWriter {
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
func (w *BitWriter) CRC() *BitWriter { return w.RS(0) }

// Bytes 输出位流（尾部补零对齐字节）。
func (w *BitWriter) Bytes() []byte {
	if w.Bit != 0 {
		w.Data = append(w.Data, 0) // 已在 bits 中按需 append；此处仅保证对齐
		w.Bit = 0
	}
	return w.Data
}
