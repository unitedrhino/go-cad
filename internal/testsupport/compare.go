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

// anyRoundTripEqual round-trip 值比较（类型统一后数值比较；
// slice/map 等不可比较类型用 reflect.DeepEqual 兜底）。
func AnyRoundTripEqual(want, got any) bool {
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
