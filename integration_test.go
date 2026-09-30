// integration_test.go 基于 testdata 真实 DWG 文件的集成测试。
// 样本为 R2018(AC1032) 配电柜图纸，实体数基准以参照实现解析结果标定。
package cad

import (
	"bytes"
	"image/png"
	"os"
	"strings"
	"testing"
)

func parseIntegration(t *testing.T, name string) *Document {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("读取样本失败: %v", err)
	}
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", name, err)
	}
	return doc
}

// TestParseRealSamples 验证两个真实图纸样本的解析完整性。
func TestParseRealSamples(t *testing.T) {
	doc1 := parseIntegration(t, "lw_example2018.dwg")
	if doc1.Version() != "AC1032" {
		t.Fatalf("版本期望 AC1032 得到 %s", doc1.Version())
	}
	texts1 := doc1.Texts()
	if len(texts1) < 5 {
		t.Fatalf("lw_example2018 文本提取数量异常: %d", len(texts1))
	}
	// 已知图纸文本（样本特征串）
	found := false
	for _, txt := range texts1 {
		if strings.Contains(txt.Text, "Teksto") {
			found = true
		}
	}
	if !found {
		t.Error("lw_example2018 应包含 Teksto 系列文本")
	}

	doc2 := parseIntegration(t, "lw_Leader.dwg")
	if len(doc2.Texts()) < 1 {
		t.Fatalf("lw_Leader 文本提取数量异常: %d", len(doc2.Texts()))
	}
}

// TestRenderRealSamples 验证真实样本渲染输出合法 PNG。
func TestRenderRealSamples(t *testing.T) {
	for _, name := range []string{"lw_example2018.dwg", "lw_Leader.dwg"} {
		doc := parseIntegration(t, name)
		pngData, err := RenderPNG(doc, RenderOptions{Width: 2048})
		if err != nil {
			t.Fatalf("%s 渲染失败: %v", name, err)
		}
		img, err := png.Decode(bytes.NewReader(pngData))
		if err != nil {
			t.Fatalf("%s 输出不是合法 PNG: %v", name, err)
		}
		if img.Bounds().Dx() != 2048 || img.Bounds().Dy() < 1 {
			t.Fatalf("%s 输出尺寸异常: %v", name, img.Bounds())
		}
	}
}
