// text_percent_test.go 覆盖 %% 特殊码显示层翻译：直径/度/公差翻译、
// 划线开关移除、%% 转义、未知码保留与零开销直返路径。
package drawing

import "testing"

func TestExpandPercentCodes(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"%%C14", "φ14"},     // 直径（大写）
		{"%%c25", "φ25"},     // 直径（小写）
		{"45%%d", "45°"},     // 度
		{"%%p0.1", "±0.1"},   // 公差
		{"%%%%", "%%"},       // 转义百分号（两个转义各出一个 %）
		{"%%u下划线%%u", "下划线"}, // 划线开关移除
		{"M10%%x", "M10%%x"}, // 未知码原样保留
		{"无特殊码", "无特殊码"},     // 零开销直返
		{"尾部%%", "尾部%"},      // 尾部悬空按转义
		{"%%C14-%%c25-45%%d-%%p0.05", "φ14-φ25-45°-±0.05"}, // 混合
	}
	for _, tc := range cases {
		if got := expandPercentCodes(tc.in); got != tc.want {
			t.Errorf("expandPercentCodes(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// 直返路径必须返回原字符串（无分配语义由实现保证，这里验证值相等）
	const plain = "普通文本123"
	if expandPercentCodes(plain) != plain {
		t.Errorf("plain text changed: %q", plain)
	}
}
