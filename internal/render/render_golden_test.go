// render_golden_test.go 渲染像素级金标对照测试：
// 用 testdata 中人工固化的参照 PNG（lw_example2018.dwg.png / lw_Leader.dwg.png）与当前
// RenderPNG 输出逐像素比较（RGB 任一通道不同即计为不同像素），
// 不相等像素占比超过阈值即判渲染行为退化。
//
// 基线决策记录（2026-09 实测）：
//   - 仓库原参照图为智能化案例人工固化产物（已被移出仓库，样本改用公开语料 lw_example2018/lw_Leader），
//     与切换前渲染高度不一致——渲染管线
//     后续加入鲁棒包围盒与放射线过滤后视口比例已变，逐像素差异率无定义，
//     等比缩放对无抗锯齿的线图也会引入大范围插值伪差，故不采用缩放比较。
//   - 因此参照图已按当前渲染输出重新固化（防退化金标），宽度 2048 与
//     RenderOptions.Width 对齐；固化后实测差异率 0%（逐位一致），且渲染
//     跨运行二进制确定（两次渲染 sha256 相同）。
//   - 阈值取 0.5%：实测 0% 向上留余量，容忍跨平台/架构浮点舍入带来的
//     边缘像素微差；若后续有意调整渲染逻辑，需重新固化参照图并更新本注释。
//   - 2026-09-28 批次 I 重固化：decodeAttribUnicode 补读文字对齐三 BS
//     （generation/horiz/vert，R2007+ 位流在字符串区数值之外）与
//     is_locked_in_block/mtext_type 定位修正后，ATTRIB/ATTDEF 文本走
//     正确单行路径（gold mtext_type 全 1），包围盒与文本位置随之修正
//     （lw_example2018: 视口自适应），参照图按修复后渲染重新固化。
//   - 2026-09-28 批次 K 重固化：decodeLeader 按 dwg.spec 完整实现
//     （origin/extrusion/x_direction/inspt_offset/endptproj 等此前被
//     尽力跳过的字段进入包围盒），实体 ENC color 修正 alpha 先于 rgb
//     的 spec 顺序，包围盒再次修正（视口自适应、
//     lw_Leader: 视口自适应→2048x903），参照图按修复后渲染重新固化。
//   - 2026-09-29 批次 F 重固化：R2007/R2018 字符串区路径修复后
//     ATTRIB 空文本（text_value=""）与空串后 tag 读取更正，01-1 值级
//     对齐 99.61%→100.00%（dwgread gold 对照，48242 键）、01-2
//     99.26%→100.00%；包围盒随之微调（01-1: 2048x912→2048x911，
//     01-2 尺寸不变、像素随文本修正），参照图按修复后渲染重新固化。
//   - 2026-09-29 批次 I（渲染质量升级）重固化：文本渲染从 textLabel
//     占位线框升级为真实字形（render_text.go，golang.org/x/image
//     opentype + 双线性仿射搬运，自带抗锯齿），线宽随渲染宽度自适应
//     （2048 口径 0.75px 不变，>2048 按比例放大，金标口径不受影响），
//     primitivesBounds
//     文字上延由半高改整高（防边缘文字裁切，本两样本视口未受影响：
//     01-1 2048x911、01-2 2048x903 尺寸与上一版一致）。占位条→字形
//     属渲染口径升级，差异率 9.51%/12.03% 全部来自文本区域像素，
//     参照图按字形渲染重新固化（二次渲染 sha256 一致，跨运行确定）。
//   - 2026-09-30 批次 Q（颜色与字体链修复）重固化：LAYER CMC 32 位 rgb
//     方法字节消费侧分类（0xC3 索引形按 ACI 取色，0xC2 真彩形按 RGB——
//     修复前索引形图层被当 RGB 取色整体失色为 RGB(0,0,n) 深蓝近黑）；
//     渲染字体链重排（DroidSansFallbackFull 简体全覆盖矢量为主字体，
//     缺字后备链补全角符号）+ TEXT/ATTRIB width_factor 接入 + blitGlyph
//     推进双重压缩修正。两样本尺寸不变（2048x911/2048x903），差异率
//     15.10%/13.9% 全部来自图层色与字形像素（渲染口径有意变更），
//     参照图按修复后渲染重新固化。
//   - 2026-09-30 批次 R（标题栏文字垂直定位修复）重固化：blitGlyph 掩码
//     v 常量 Cv=asc−dy 多计一段上延（dy 已相对基线，Go 字形矩形 Y 向下），
//     全部 PNG 文字整体抬高 ~1em；drawSingleLine vAlign=2（middle）基线
//     偏移符号反转（extent 中心对齐锚点语义）；MTEXT 行距改
//     linespace_factor×字高（gold extents_height 实证，1.66 常数仅作
//     factor 未存的兜底）；primitivesBounds 补 MTEXT 块体方向扩展
//     （附着 1-3 向下/4-6 双向/7-9 向上，2000/Text.dwg 单 MTEXT 样本
//     墨迹锚定修复后全部落在视口外渲染空实证）。两样本尺寸不变
//     （2048x911/2048x903），差异率 18.0%/21.0% 全部来自文字像素整体
//     下移（渲染口径有意变更），参照图按修复后渲染重新固化（二次渲染
//     逐位一致）。
package render

import (
	"bytes"
	"github.com/unitedrhino/go-cad/internal/drawing"
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"image/png"
	"os"
	"testing"
)

// goldenMaxDiffRatio 允许的不相等像素占比上限（0.5%，见文件头基线决策记录）。
const goldenMaxDiffRatio = 0.005

// goldenCases 金标样本：dwg 为输入图纸，ref 为固化参照图，width 与固化时一致。
var goldenCases = []struct {
	dwg   string
	ref   string
	width int
}{
	{"lw_example2018.dwg", "lw_example2018.dwg.png", 2048},
	{"lw_Leader.dwg", "lw_Leader.dwg.png", 2048},
}

// TestRenderGoldenMatch 对每个金标样本渲染并与参照图逐像素对照。
func TestRenderGoldenMatch(t *testing.T) {
	for _, tc := range goldenCases {
		t.Run(tc.dwg, func(t *testing.T) {
			refData, err := os.ReadFile(testsupport.TestdataPath(tc.ref))
			if err != nil {
				if os.IsNotExist(err) {
					t.Skipf("参照图缺失，跳过金标对照: %s", tc.ref)
				}
				t.Fatalf("读取参照图失败: %v", err)
			}
			ref, err := png.Decode(bytes.NewReader(refData))
			if err != nil {
				t.Fatalf("参照图不是合法 PNG: %v", err)
			}
			doc := parseIntegration(t, tc.dwg)
			pngData, err := RenderPNG(doc, RenderOptions{Width: tc.width})
			if err != nil {
				t.Fatalf("渲染失败: %v", err)
			}
			cur, err := png.Decode(bytes.NewReader(pngData))
			if err != nil {
				t.Fatalf("渲染输出不是合法 PNG: %v", err)
			}
			// 参照图按当前渲染固化，尺寸应严格一致（宽=固化宽度，高=包围盒比例）；
			// 不一致说明视口/比例逻辑已变，直接失败而非缩放比较（见文件头决策记录）
			rb, cb := ref.Bounds(), cur.Bounds()
			if rb.Dx() != cb.Dx() || rb.Dy() != cb.Dy() {
				t.Fatalf("尺寸与参照图不一致: cur=%dx%d ref=%dx%d（渲染视口逻辑已变，需重新固化参照图）",
					cb.Dx(), cb.Dy(), rb.Dx(), rb.Dy())
			}
			diff, total := 0, rb.Dx()*rb.Dy()
			for y := 0; y < rb.Dy(); y++ {
				for x := 0; x < rb.Dx(); x++ {
					cr, cg, cbl, _ := cur.At(cb.Min.X+x, cb.Min.Y+y).RGBA()
					rr, rg, rl, _ := ref.At(rb.Min.X+x, rb.Min.Y+y).RGBA()
					if channelDelta(cr, rr) || channelDelta(cg, rg) || channelDelta(cbl, rl) {
						diff++
					}
				}
			}
			ratio := float64(diff) / float64(total)
			t.Logf("不相等像素 %d/%d（%.4f%%），阈值 %.2f%%", diff, total, ratio*100, goldenMaxDiffRatio*100)
			if ratio > goldenMaxDiffRatio {
				t.Errorf("渲染结果与金标差异超阈值: %.4f%% > %.2f%%", ratio*100, goldenMaxDiffRatio*100)
			}
		})
	}
}

// channelDelta 判断单通道 16 位值是否不同（量化到 8 位后比较，消除 PNG 位深表达差异）。
func channelDelta(a, b uint32) bool {
	return a>>8 != b>>8
}

// parseIntegration 读取渲染门禁样本并解析（render 包本地副本；路径相对
// internal/render 测试工作目录，样本经 ../../testdata 取用）。
func parseIntegration(t *testing.T, name string) *drawing.Document {
	t.Helper()
	data, err := os.ReadFile(testsupport.TestdataPath(name))
	if err != nil {
		t.Skipf("样本缺失（%s）: %v", name, err)
	}
	doc, err := drawing.Parse(data)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", name, err)
	}
	return doc
}
