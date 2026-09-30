// versions_test.go 多版本集成测试：多版本样本库（历史对齐批次沉淀）的
// R2000/R2004/R2010/R2013 单实体样本按「版本×类型能力矩阵」验证。
// 各版本×类型组合的解码能力均已对齐（原 R2004/R2010 未对齐组合的
// 结构级 t.Skip 条件已删除），统一按精确/结构断言严格校验。
package cad

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// parseSample 解析 testdata 下的样本文件。
func parseSample(t *testing.T, name string) *Document {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("读取样本 %s 失败: %v", name, err)
	}
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", name, err)
	}
	return doc
}

// allEntities 模型空间全部实体（含模型空间块头启发式并入的内容）。
func allEntities(d *Document) []any {
	return append(append([]any{}, d.modelSpace...), d.largestBlockEntities()...)
}

func findEntity[T any](d *Document) *T {
	for _, e := range allEntities(d) {
		if v, ok := e.(*T); ok {
			return v
		}
	}
	return nil
}

func TestLineSamplesAllVersions(t *testing.T) {
	for _, name := range []string{"line_2000.dwg", "line_2004.dwg", "line_2010.dwg", "line_2013.dwg"} {
		t.Run(name, func(t *testing.T) {
			doc := parseSample(t, name)
			line := findEntity[entLine](doc)
			if line == nil {
				t.Fatal("未解出 LINE")
			}
			if !isFinite(line.start.x) || math.Abs(line.start.x) > 1e7 {
				t.Fatalf("%s LINE start 异常: %v", name, line.start)
			}
		})
	}
}

func TestCircleSamples(t *testing.T) {
	for _, name := range []string{"circle_2004.dwg", "circle_2010.dwg", "circle_2013.dwg"} {
		t.Run(name, func(t *testing.T) {
			doc := parseSample(t, name)
			c := findEntity[entCircle](doc)
			if c == nil {
				t.Fatal("未解出 CIRCLE")
			}
			if name == "circle_2013.dwg" {
				if math.Abs(c.radius-50) > 0.01 {
					t.Fatalf("CIRCLE 半径期望 50 得到 %v", c.radius)
				}
			} else if !isFinite(c.radius) || c.radius < 0 || c.radius > 1e7 {
				t.Fatalf("CIRCLE 半径异常: %v", c.radius)
			}
		})
	}
}

func TestArcSamples(t *testing.T) {
	for _, name := range []string{"arc_2004.dwg", "arc_2010.dwg", "arc_2013.dwg"} {
		t.Run(name, func(t *testing.T) {
			doc := parseSample(t, name)
			a := findEntity[entArc](doc)
			if a == nil {
				t.Fatal("未解出 ARC")
			}
			if !isFinite(a.radius) || a.radius < 0 || a.radius > 1e7 {
				t.Fatalf("ARC 半径异常: %v", a.radius)
			}
		})
	}
}

func TestPointSamples(t *testing.T) {
	for _, name := range []string{"point2d_2013.dwg", "point3d_2013.dwg"} {
		t.Run(name, func(t *testing.T) {
			doc := parseSample(t, name)
			if findEntity[entPoint](doc) == nil {
				t.Fatal("未解出 POINT")
			}
		})
	}
}

func TestEllipseSamples(t *testing.T) {
	for _, name := range []string{"ellipse_2004.dwg", "ellipse_2010.dwg", "ellipse_2013.dwg"} {
		t.Run(name, func(t *testing.T) {
			doc := parseSample(t, name)
			e := findEntity[entEllipse](doc)
			if e == nil {
				t.Fatal("未解出 ELLIPSE")
			}
			if !isFinite(e.ratio) || e.ratio <= 0 || e.ratio > 1 {
				t.Fatalf("ELLIPSE 轴比异常: %v", e.ratio)
			}
		})
	}
}

func TestPolyline2DSamples(t *testing.T) {
	for _, name := range []string{"polyline2d_line_2004.dwg", "polyline2d_line_2010.dwg", "polyline2d_line_2013.dwg"} {
		t.Run(name, func(t *testing.T) {
			doc := parseSample(t, name)
			p := findEntity[entLwPolyline](doc)
			if p == nil || len(p.vertices) == 0 {
				t.Fatal("未解出 LWPOLYLINE 或无顶点")
			}
		})
	}
}

func TestMText2000Sample(t *testing.T) {
	doc := parseSample(t, "mtext_2000.dwg")
	m := findEntity[entMText](doc)
	if m == nil {
		t.Fatal("未解出 MTEXT")
	}
	if !strings.Contains(m.text, "Hello") {
		t.Fatalf("MTEXT 文本: %q", m.text)
	}
}

func TestText2000Sample(t *testing.T) {
	doc := parseSample(t, "text_2000.dwg")
	txt := findEntity[entText](doc)
	if txt == nil {
		t.Fatal("未解出 TEXT")
	}
	if !strings.Contains(txt.text, "Hello") {
		t.Fatalf("TEXT 文本: %q", txt.text)
	}
}

func TestR14SamplesParse(t *testing.T) {
	// R14（AC1014）自 2026-09 起已支持：容器与 R2000 同路径，实体头为 R13/R14 布局。
	for _, name := range []string{"line_R14.dwg", "arc_R14.dwg"} {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatalf("读取 %s: %v", name, err)
		}
		doc, err := Parse(data)
		if err != nil {
			t.Errorf("%s 应可解析: %v", name, err)
			continue
		}
		if doc.version != verR14 {
			t.Errorf("%s 版本应为 verR14，得到 %v", name, doc.version)
		}
		if len(doc.modelSpace) == 0 {
			t.Errorf("%s 应解出至少 1 个模型空间实体", name)
		}
	}
}
