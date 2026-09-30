// utils.go 提供位级解码使用的数值与 UTF-16 转换辅助函数。
package bitstream

import "math"

// leF64 将小端 8 字节解释为 IEEE 双精度。
func leF64(b [8]byte) float64 {
	return math.Float64frombits(uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24 |
		uint64(b[4])<<32 | uint64(b[5])<<40 | uint64(b[6])<<48 | uint64(b[7])<<56)
}

// f64LeBytes 将 IEEE 双精度序列化为小端 8 字节。
func f64LeBytes(f float64) [8]byte {
	bits := math.Float64bits(f)
	var b [8]byte
	for i := 0; i < 8; i++ {
		b[i] = byte(bits >> (i * 8))
	}
	return b
}

// utf16Decode 将 UTF-16 单元序列解码为 UTF-8 字符串（含代理对处理）。
func Utf16Decode(units []uint16) []rune {
	runes := make([]rune, 0, len(units))
	for i := 0; i < len(units); i++ {
		u := units[i]
		switch {
		case u >= 0xD800 && u < 0xDC00 && i+1 < len(units):
			u2 := units[i+1]
			if u2 >= 0xDC00 && u2 < 0xE000 {
				runes = append(runes, (rune(u-0xD800)<<10|rune(u2-0xDC00))+0x10000)
				i++
				continue
			}
			runes = append(runes, '�')
		case u >= 0xD800 && u < 0xE000:
			runes = append(runes, '�')
		default:
			runes = append(runes, rune(u))
		}
	}
	return runes
}
