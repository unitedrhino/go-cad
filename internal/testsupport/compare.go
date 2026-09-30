// compare.go 测试通用比较助手：位串转字节与浮点相对误差比较。
package testsupport

import "math"

// BitsToBytes 将 '0'/'1' 位串按 MSB 在前转换为字节（尾部补零对齐）。
func BitsToBytes(bits string) []byte {
	out := make([]byte, 0, len(bits)/8+1)
	cur := byte(0)
	n := 0
	for _, c := range bits {
		cur <<= 1
		if c == '1' {
			cur |= 1
		}
		n++
		if n == 8 {
			out = append(out, cur)
			cur = 0
			n = 0
		}
	}
	if n > 0 {
		cur <<= 8 - uint(n)
		out = append(out, cur)
	}
	return out
}

// NearEq 浮点相对误差比较（1e-9 相对容差）。
func NearEq(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9*math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
}
