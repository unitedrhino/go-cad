// text_percent.go 实现 CAD 文本 %% 特殊码的显示层翻译：TEXT/MTEXT/
// ATTRIB 中的 %%c（直径）、%%d（度）、%%p（公差）等控制码在解码层
// 原样保留（与 LibreDWG gold 逐键对齐口径），仅在图元 Label 生成时
// 统一翻译，渲染、智能拆图与文本提取三条消费路径同源一致。
package drawing

import "strings"

// expandPercentCodes 翻译 CAD %% 特殊码为显示字符：
//   - %%c / %%C → φ（直径，中文工程标注惯例字形）
//   - %%d / %%D → °（度）
//   - %%p / %%P → ±（公差）
//   - %%u / %%o（下/上划线开关）→ 移除（划线属渲染增强，不在字形层模拟）
//   - %%%      → %（转义）
//   - 其余 %%x → 原样保留
//
// 不含 %% 的文本零开销直返（中文工程图绝大多数文本不携带该码）。
func expandPercentCodes(s string) string {
	if !strings.Contains(s, "%%") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '%' || i+1 >= len(s) || s[i+1] != '%' {
			b.WriteByte(s[i])
			i++
			continue
		}
		if i+2 >= len(s) {
			// 尾部悬空 %%：按转义输出单个 %
			b.WriteByte('%')
			i += 2
			continue
		}
		switch s[i+2] {
		case 'c', 'C':
			b.WriteRune('φ')
			i += 3
		case 'd', 'D':
			b.WriteRune('°')
			i += 3
		case 'p', 'P':
			b.WriteRune('±')
			i += 3
		case '%':
			b.WriteByte('%')
			i += 3
		case 'u', 'U', 'o', 'O':
			i += 3
		default:
			b.WriteString("%%")
			i += 2
		}
	}
	return b.String()
}
