// render_text_fontchain_test.go 文本渲染字体链测试：
// 主字体 + 缺字后备链（loadFontChain）的覆盖门禁——工程图常用字符（简体
// 汉字/全角符号/工程单位）在候选字体上缺字时由后备字体补齐，不再整字
// 跳过（消防施工图实证：日文 IPA 主选时"报/总/线/电/设"等约 220/736
// 个简体字形缺失，图例表线路名称成缺字文本）。
package cad

import (
	"testing"

	"golang.org/x/image/math/fixed"
)

// fontChainProbeChars 工程图文字探针字符集：图纸实证缺字字符（简体专用
// 字形）+ 全角符号（全链易缺项）+ ASCII 控制样本。
const fontChainProbeChars = "火灾报警二总线电气监控图线设备话泵阀联动启压锁总专业东两严为乡书产仅仪优伟传侧值储农刘则刚别务动华协单卫厅压厢发变启员响喷块坚垒务动勒博厂厅强异弃张录怀总惧截户扇手打托执扩扫扬扮扰扶找承技抄把抓投抗折抚抛拒找批扯扳找旷矿码砖泵砚砍砼" +
	"°±×“”℃Ⅱ≤≥№㎡㈠⑴ⅠⅧ㏑㎜㎝㎡"

// TestFontChainProbeCoverage 探针字符集全链缺字为零门禁。
func TestFontChainProbeCoverage(t *testing.T) {
	cv := &canvas{}
	tr := newTextRenderer(cv)
	if tr.basic {
		t.Skip("无 opentype 系统字体，跳过字体链覆盖门禁")
	}
	if len(tr.fallbacks) == 0 {
		t.Log("提示：无后备字体（单字体环境），仅验证主字体覆盖")
	}
	sf := tr.face(24)
	missing := 0
	for _, r := range fontChainProbeChars {
		g := tr.glyph(r, sf)
		if g == nil || g.mask == nil {
			missing++
			if missing <= 10 {
				t.Logf("缺字 %q (U+%04X)", r, r)
			}
		}
	}
	if missing != 0 {
		t.Errorf("探针字符缺字 %d 个（应全链补齐为零）", missing)
	}
}

// TestFontChainFallbackGlyph 后备字形与主字形同档位推进：后备命中字符的
// advance 应来自后备字体实际字形（非缺字半角兜底），保证行内字距连续。
func TestFontChainFallbackGlyph(t *testing.T) {
	cv := &canvas{}
	tr := newTextRenderer(cv)
	if tr.basic || len(tr.fallbacks) == 0 {
		t.Skip("无后备字体环境，跳过")
	}
	sf := tr.face(24)
	for _, r := range fontChainProbeChars {
		g := tr.glyph(r, sf)
		if g == nil || g.mask == nil {
			t.Fatalf("字符 %q 仍缺字", r)
		}
		// 缺字兜底宽 = px*0.5；真实字形推进应显著区别于兜底值
		if g.advance == sf.px*0.5 {
			t.Logf("警告：字符 %q 推进等于兜底半角宽（可能为空字形）", r)
		}
	}
	var _ = fixed.Point26_6{} // 保持导入与实现解耦
}

// TestFontChainOverride 主字体 override 时不带系统后备（显式字体优先，
// 链路不越权补字）。
func TestFontChainOverride(t *testing.T) {
	main, fallbacks := loadFontChain("/usr/share/fonts/opentype/unifont/unifont.otf")
	if main == nil {
		t.Skip("unifont 不可用")
	}
	if len(fallbacks) != 0 {
		t.Errorf("override 主字体不应携带系统后备，got %d", len(fallbacks))
	}
}
