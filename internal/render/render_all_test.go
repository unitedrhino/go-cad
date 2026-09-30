// render_all_test.go 渲染链路全样本回归：全部 testdata 样本（R14~R2018）
// 均需可解析并成功渲染出非空 PNG，防止渲染路径随解码改动而退化。
package render

import (
	"github.com/unitedrhino/go-cad/internal/drawing"
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"os"
	"path/filepath"
	"testing"
)

func TestRenderAllSamples(t *testing.T) {
	files, _ := filepath.Glob(testsupport.TestdataPath("*.dwg"))
	if len(files) == 0 {
		t.Skip("无样本")
	}
	ok, fail := 0, 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		doc, err := drawing.Parse(data)
		if err != nil {
			fail++
			t.Errorf("%s: parse: %v", f, err)
			continue
		}
		png, err := RenderPNG(doc, RenderOptions{Width: 800})
		if err != nil {
			fail++
			t.Errorf("%s: render: %v", f, err)
			continue
		}
		if len(png) < 100 {
			fail++
			t.Errorf("%s: png 过小 %d", f, len(png))
			continue
		}
		ok++
	}
	t.Logf("render ok=%d fail=%d", ok, fail)
	if fail > 0 {
		t.Fatalf("渲染回归失败 %d 个样本", fail)
	}
}
