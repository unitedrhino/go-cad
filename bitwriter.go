// 本文件实现位级写入器（dwgwrite 编码方向的基座）：与 bitStream 对称的
// 写原语。位流按 MSB 优先逐位写入，多字节整数按 DWG 惯例小端。
// 供对象/实体重编码（round-trip 往返验证）与后续 dwgwrite 复刻使用。
package cad

import (
	"encoding/binary"
	"math"
	"unicode/utf16"
)

// encWriter 位级写入器。
type encWriter struct {
	data []byte // 输出缓冲
	bit  uint8  // 当前字节内已占用的位数（0-7）
}

// newEncWriter 创建位写入器。
func newEncWriter() *encWriter { return &encWriter{} }

// writeBitsMsb 按 MSB 优先写入 n 位（n ≤ 64）。
func (w *encWriter) writeBitsMsb(v uint64, n uint8) {
	for i := int(n) - 1; i >= 0; i-- {
		bit := uint8((v >> uint(i)) & 1)
		if w.bit == 0 {
			w.data = append(w.data, 0)
		}
		if bit != 0 {
			w.data[len(w.data)-1] |= 1 << (7 - w.bit)
		}
		w.bit = (w.bit + 1) % 8
	}
}

// writeRC 写 1 字节。
func (w *encWriter) writeRC(v uint8) { w.writeBitsMsb(uint64(v), 8) }

// writeRS 写 16 位小端整数。
func (w *encWriter) writeRS(v uint16) {
	w.writeRC(uint8(v))
	w.writeRC(uint8(v >> 8))
}

// writeRL 写 32 位小端整数。
func (w *encWriter) writeRL(v uint32) {
	w.writeRC(uint8(v))
	w.writeRC(uint8(v >> 8))
	w.writeRC(uint8(v >> 16))
	w.writeRC(uint8(v >> 24))
}

// writeBLLc 写 BLL 压缩编码（对齐 LibreDWG bit_write_BLL，与 readBLL 对称）：
// 3 位长度码，码值 1/2/4 写 RC/RS/RL，其余码值写对应字节数的小端序列。
func (w *encWriter) writeBLLc(v uint64) {
	n := 8
	for n > 0 && v>>(8*(n-1)) == 0 {
		n--
	}
	if n == 0 {
		n = 1
	}
	w.write3B(uint8(n))
	switch n {
	case 1:
		w.writeRC(uint8(v))
	case 2:
		w.writeRS(uint16(v))
	case 4:
		w.writeRL(uint32(v))
	default:
		for i := 0; i < n; i++ {
			w.writeRC(uint8(v >> (8 * i)))
		}
	}
}

// writeRD 写 64 位 IEEE double：8 字节小端、每字节 MSB 优先逐位写
// （与 readRD 的 8×readRC + leF64 对称，支持非字节对齐位置）。
func (w *encWriter) writeRD(f float64) {
	bits := math.Float64bits(f)
	for i := 0; i < 8; i++ {
		w.writeBitsMsb((bits>>(8*i))&0xFF, 8)
	}
}

// writeB 写 1 位。
func (w *encWriter) writeB(v bool) { w.writeBitsMsb(boolBit(v), 1) }

// writeBB 写 2 位。
func (w *encWriter) writeBB(v uint8) { w.writeBitsMsb(uint64(v), 2) }

// write3B 写 3 位。
func (w *encWriter) write3B(v uint8) { w.writeBitsMsb(uint64(v), 3) }

// writeBS 写压缩短整数：≤255 走 BB01+RC，0 走 BB10，256 走 BB11，其余 BB00+RS。
func (w *encWriter) writeBS(v uint16) {
	switch {
	case v <= 255:
		if v == 0 {
			w.writeBB(2)
			return
		}
		w.writeBB(1)
		w.writeRC(uint8(v))
	case v == 256:
		w.writeBB(3)
	default:
		w.writeBB(0)
		w.writeRS(v)
	}
}

// writeBL 写压缩长整数：0 走 BB10，≤255 走 BB01+RC，其余 BB00+RL。
func (w *encWriter) writeBL(v uint32) {
	switch {
	case v == 0:
		w.writeBB(2)
	case v == 256:
		w.writeBB(3)
	case v <= 255:
		w.writeBB(1)
		w.writeRC(uint8(v))
	default:
		w.writeBB(0)
		w.writeRL(v)
	}
}

// writeBLLv 写压缩 64 位整数：0 走 BB10，≤255 走 BB01+RC，≤65535 走 BB00+RS，
// 其余 BB11+BLL。
func (w *encWriter) writeBLLv(v uint64) {
	// 委托 writeBLLc（与 readBLL 的 3 位长度码完全对称）。原实现误用
	// 2 位 BB 码分支，读侧 readBLL 以 3 位码解析，写出即不可读。
	w.writeBLLc(v)
}

// writeBD 写压缩双精度（对齐 readBD 语义）：1.0 走 BB01、0.0 走 BB10，
// 其余 BB00+RD 全量。
func (w *encWriter) writeBD(f float64) {
	switch {
	case f == 1:
		w.writeBB(1)
	case f == 0:
		w.writeBB(2)
	default:
		w.writeBB(0)
		w.writeRD(f)
	}
}

// writeDD 写差分双精度（相对默认值，与 readDD / LibreDWG bit_write_DD 对称）：
// 码 0=与默认值位级相同不写数据、1=仅低 4 字节不同写 4 字节、
// 2=次高 2 字节不同写 6 字节（覆盖序 data[4],data[5],data[0..4)，最高 2 字节保留默认值）、
// 3=高 2 字节不同走全量 RD。
func (w *encWriter) writeDD(v, def float64) {
	nv := f64bytes(v)
	dv := f64bytes(def)
	if nv == dv {
		w.writeBB(0)
		return
	}
	if bytesEq(nv[6:8], dv[6:8]) {
		if bytesEq(nv[4:6], dv[4:6]) {
			w.writeBB(1)
			for i := 0; i < 4; i++ {
				w.writeRC(nv[i])
			}
			return
		}
		w.writeBB(2)
		for _, i := range [6]int{4, 5, 0, 1, 2, 3} {
			w.writeRC(nv[i])
		}
		return
	}
	w.writeBB(3)
	w.writeRD(v)
}

// writeH 写句柄引用：4 位 code + 4 位 counter + counter 字节大端值。
func (w *encWriter) writeH(code uint8, counter uint8, value uint64) {
	w.writeBitsMsb(uint64(code), 4)
	w.writeBitsMsb(uint64(counter), 4)
	for i := int(counter) - 1; i >= 0; i-- {
		w.writeRC(uint8(value >> (8 * i)))
	}
}

// writeTV 写文本（RC 长度语义由调用方决定长度是否含 \0）：BS 长度 + 字节。
func (w *encWriter) writeTV(s string) {
	b := []byte(s)
	w.writeBS(uint16(len(b)))
	for _, c := range b {
		w.writeRC(c)
	}
}

// writeTU 写 UTF-16LE 文本：BS 长度（含 \0）+ 单元序列（尾部补 \0）。
func (w *encWriter) writeTU(s string) {
	units := utf16Encode(s)
	w.writeBS(uint16(len(units) + 1))
	for _, u := range units {
		w.writeRS(u)
	}
	w.writeRS(0)
}

// writeTF 写定长字节串（逐 RC）。
func (w *encWriter) writeTF(b []byte) {
	for _, c := range b {
		w.writeRC(c)
	}
}

// tellBits 返回当前写入位位置：bit>0 时末字节为部分字节，
// 位置 = (len-1)*8 + bit；bit==0 时位置 = len*8。
func (w *encWriter) tellBits() uint64 {
	if w.bit != 0 {
		return uint64(len(w.data)-1)*8 + uint64(w.bit)
	}
	return uint64(len(w.data)) * 8
}

// bytes 返回当前缓冲（尾部未满字节以 0 填充）。
func (w *encWriter) bytes() []byte {
	if len(w.data) == 0 || w.bit != 0 {
		out := make([]byte, len(w.data), len(w.data)+1)
		copy(out, w.data)
		return out
	}
	return w.data
}

// bytesEq 字节切片相等判断。
func bytesEq(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// utf16Encode UTF-8 字符串转 UTF-16LE 单元序列（不含结尾 \0）。
func utf16Encode(s string) []uint16 {
	return utf16.Encode([]rune(s))
}

// f64bytes float64 的 IEEE 小端字节表示。
func f64bytes(f float64) [8]byte {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], math.Float64bits(f))
	return buf
}

// boolBit bool 转 1 位值。
func boolBit(v bool) uint64 {
	if v {
		return 1
	}
	return 0
}

// writeRLL 写 64 位小端整数（两次 32 位拼合，与 readRLL 对称）。
func (w *encWriter) writeRLL(v uint64) {
	w.writeRL(uint32(v))
	w.writeRL(uint32(v >> 32))
}

// byteBitsTable 每个字节值预展开为 8 个 '0'/'1' 字符（collectBits 整字节
// 快路径用）：解析热路径每个实体都要收集三段回放位串，逐位循环是解析
// 侧最大热点（pprof CPU ~20%），查表一次展开 8 位，输出逐位一致。
var byteBitsTable [256][8]byte

func init() {
	for v := 0; v < 256; v++ {
		for b := 0; b < 8; b++ {
			byteBitsTable[v][b] = '0' + byte((uint8(v)>>(7-b))&1)
		}
	}
}

// collectBits 收集位流 [start, end) 区间的 0/1 位串（重编码原样写回用）。
// 首尾部分位逐位生成，中间整字节查表展开（性能：解析热点，见 byteBitsTable）。
func collectBits(r *bitStream, start, end uint64) string {
	total := uint64(len(r.src)) * 8
	if end > total {
		end = total
	}
	if start >= end {
		return ""
	}
	n := end - start
	out := make([]byte, n)
	p, off := start, uint64(0)
	// 首字节未对齐部分：逐位
	if p%8 != 0 {
		for ; p%8 != 0 && p < end; p++ {
			out[off] = '0' + (r.src[p/8]>>(7-p%8))&1
			off++
		}
	}
	// 整字节快路径：查表一次展开 8 位
	for p+8 <= end {
		copy(out[off:], byteBitsTable[r.src[p/8]][:])
		p += 8
		off += 8
	}
	// 尾部不足一字节部分：逐位
	for ; p < end; p++ {
		out[off] = '0' + (r.src[p/8]>>(7-p%8))&1
		off++
	}
	return string(out)
}

// writeBitsString 将 0/1 位串原样写入位流。
func (w *encWriter) writeBitsString(s string) {
	for i := 0; i < len(s); i++ {
		w.writeBitsMsb(uint64(s[i]-'0'), 1)
	}
}
