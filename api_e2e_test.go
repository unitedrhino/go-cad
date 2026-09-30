// api_e2e_test.go 导出 API 端到端矩阵：全部导出 API × R13~R2018 全版本
// 代表样本，逐项断言返回非空/合法（PNG 可解码、WriteDwg 产物可再 Parse、
// 对照导出为合法 JSON 等）；任一 API 在任一版本 panic 或返回意外错误即判
// 不可用。同时覆盖零值/nil 文档的冒烟（只要求优雅处理不 panic）。
package cad

import (
	"bytes"
	"encoding/json"
	"github.com/unitedrhino/go-cad/internal/drawing"
	"github.com/unitedrhino/go-cad/internal/entity"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// apiE2EVersion 全版本矩阵的一行：版本串 + 代表样本（每版本至少 1 个）。
type apiE2EVersion struct {
	Ver    string // Parse 后 Document.Version() 的期望值
	sample string // testdata 下的样本文件名
}

// apiE2EVersions R13(AC1012)~R2018(AC1032) 每版本一个代表样本。
var apiE2EVersions = []apiE2EVersion{
	{"AC1012", "lw_example_r13.dwg"},
	{"AC1014", "line_R14.dwg"},
	{"AC1015", "line_2000.dwg"},
	{"AC1018", "line_2004.dwg"},
	{"AC1021", "line_2007.dwg"},
	{"AC1024", "line_2010.dwg"},
	{"AC1027", "line_2013.dwg"},
	{"AC1032", "lw_example2018.dwg"},
}

// loadAPIE2EData 读取矩阵样本字节流。
func loadAPIE2EData(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("读取样本 %s 失败: %v", name, err)
	}
	return data
}

// TestAPIE2EMatrix 全导出 API × 全版本样本端到端矩阵。
func TestAPIE2EMatrix(t *testing.T) {
	for _, row := range apiE2EVersions {
		row := row
		t.Run(row.Ver+"/"+row.sample, func(t *testing.T) {
			data := loadAPIE2EData(t, row.sample)

			// ---- Parse / 文档访问器 ----
			doc, err := Parse(data)
			if err != nil {
				t.Fatalf("Parse 失败: %v", err)
			}
			if got := doc.Version(); got != row.Ver {
				t.Fatalf("Version = %s, 期望 %s", got, row.Ver)
			}
			if n := doc.EntityCount(); n < 1 {
				t.Fatalf("EntityCount = %d, 期望 ≥1（样本含模型空间图元）", n)
			}
			if doc.Skipped() < 0 {
				t.Fatalf("Skipped = %d, 不应为负", doc.Skipped())
			}
			if doc.InternalObjects() == nil {
				t.Fatal("InternalObjects 不应为 nil map")
			}
			if doc.Xrecords() == nil {
				t.Fatal("Xrecords 不应为 nil map")
			}
			// DebugFailures 允许 nil（无失败时未初始化），只要求可序列化
			if _, err := json.Marshal(doc.DebugFailures()); err != nil {
				t.Fatalf("DebugFailures 不可序列化: %v", err)
			}
			if doc.DebugLayerColors() == nil {
				t.Fatal("DebugLayerColors 不应为 nil map")
			}

			// EntityByHandle：已解析句柄命中、未知句柄返回 nil
			var firstHandle uint64
			for h := range doc.ByHandle {
				firstHandle = h
				break
			}
			if firstHandle != 0 {
				if doc.EntityByHandle(firstHandle) == nil {
					t.Fatalf("EntityByHandle(%d) = nil, 期望命中", firstHandle)
				}
			}
			if doc.EntityByHandle(0xFFFFFFFE) != nil {
				t.Error("EntityByHandle(未知句柄) 应返回 nil")
			}

			// ---- Texts ----
			for _, txt := range doc.Texts() {
				if strings.TrimSpace(txt.Text) == "" && txt.X == 0 && txt.Y == 0 {
					t.Errorf("Texts 含全空项: %+v", txt)
				}
			}

			// ---- RenderPNG：产物必须可解码 ----
			pngData, err := RenderPNG(doc, RenderOptions{Width: 512})
			if err != nil {
				t.Fatalf("RenderPNG 失败: %v", err)
			}
			img, err := png.Decode(bytes.NewReader(pngData))
			if err != nil {
				t.Fatalf("RenderPNG 输出不是合法 PNG: %v", err)
			}
			if img.Bounds().Dx() != 512 {
				t.Fatalf("PNG 宽度 = %d, 期望 512", img.Bounds().Dx())
			}

			// ---- 对照/调试导出 ----
			dump, err := DumpEntities(data)
			if err != nil {
				t.Fatalf("DumpEntities 失败: %v", err)
			}
			if !json.Valid([]byte(dump)) {
				t.Fatal("DumpEntities 输出不是合法 JSON")
			}
			var rows []DumpEntityRow
			if err := json.Unmarshal([]byte(dump), &rows); err != nil {
				t.Fatalf("DumpEntities JSON 反序列化失败: %v", err)
			}
			if len(rows) == 0 {
				t.Fatal("DumpEntities 输出为空（模型空间应有实体）")
			}
			for _, r := range rows {
				if r.Handle == 0 {
					t.Fatalf("DumpEntities 行句柄为 0: %+v", r)
				}
			}

			// ---- R2004+ 命名段/对象图调试 API ----
			// R13/R14/R2000 为段目录式容器，无命名段，按设计返回错误；
			// 不允许 panic。
			isR2004Plus := row.Ver != "AC1012" && row.Ver != "AC1014" && row.Ver != "AC1015"
			secData, secErr := LoadNamedSectionDebug2(data, "AcDb:AcDbObjects")
			if isR2004Plus {
				if secErr != nil {
					t.Fatalf("LoadNamedSectionDebug2 失败: %v", secErr)
				}
				if len(secData) == 0 {
					t.Fatal("LoadNamedSectionDebug2 返回空段")
				}
			} else if secErr == nil {
				t.Error("R2000 家族无命名段，LoadNamedSectionDebug2 应返回错误")
			}

			idxRefs, idxErr := DebugObjectIndexExport(data)
			if isR2004Plus {
				if idxErr != nil {
					t.Fatalf("DebugObjectIndexExport 失败: %v", idxErr)
				}
				if len(idxRefs) == 0 {
					t.Fatal("DebugObjectIndexExport 输出为空")
				}
				// DebugRecord2：取对象图第一条解析 body
				body, _, size, err := DebugRecord2(secData, idxRefs[0], verStringR2010Plus(row.Ver))
				if err != nil {
					t.Fatalf("DebugRecord2 失败: %v", err)
				}
				if len(body) == 0 || size == 0 {
					t.Fatalf("DebugRecord2 返回空 body（size=%d）", size)
				}
				// DebugObjectBody：按句柄导出 body
				objBody, _, err := DebugObjectBody(data, idxRefs[0].Handle)
				if err != nil {
					t.Fatalf("DebugObjectBody 失败: %v", err)
				}
				if len(objBody) == 0 {
					t.Fatal("DebugObjectBody 返回空 body")
				}
			} else if idxErr == nil {
				t.Error("R2000 家族无命名段对象图，DebugObjectIndexExport 应返回错误")
			}

			// ---- 几何/扫描诊断 ----
			lines := DebugLines(data)
			if lines == nil {
				t.Fatal("DebugLines 不应返回 nil map")
			}
			gold := map[uint64][6]float64{}
			for h, v := range lines {
				gold[h] = v
			}
			// DebugScanGoldLines 为 R2018 对照诊断专用（内部按 R2018 布局
			// 解码），其他版本返回空切片属设计内；只要求可调用可迭代
			matched := DebugScanGoldLines(data, gold)
			for range matched {
			}

			// ---- WriteDwg 回放写出：产物必须可再 Parse 且版本一致 ----
			var out bytes.Buffer
			if err := WriteDwg(doc, &out); err != nil {
				t.Fatalf("WriteDwg 失败: %v", err)
			}
			if out.Len() == 0 {
				t.Fatal("WriteDwg 输出为空")
			}
			doc2, err := Parse(out.Bytes())
			if err != nil {
				t.Fatalf("WriteDwg 产物再 Parse 失败: %v", err)
			}
			if doc2.Version() != row.Ver {
				t.Fatalf("回写产物版本 = %s, 期望 %s", doc2.Version(), row.Ver)
			}
			if doc2.EntityCount() != doc.EntityCount() {
				t.Errorf("回写产物实体数 %d != 原始 %d", doc2.EntityCount(), doc.EntityCount())
			}
		})
	}
}

// verStringR2010Plus 版本串是否为 R2010+ 记录布局。
func verStringR2010Plus(ver string) bool {
	return ver == "AC1024" || ver == "AC1027" || ver == "AC1032"
}

// TestAPIWriteDwgFamilyDispatch 三代容器写出器按文档版本的正确分派与拒绝：
// 匹配版本成功写出且产物可再 Parse；不匹配版本必须返回错误而非 panic。
func TestAPIWriteDwgFamilyDispatch(t *testing.T) {
	cases := []struct {
		sample string
		writer func(*Document, *bytes.Buffer) error
		kind   string // 期望成功的写出器代
	}{
		{"line_R14.dwg", func(d *Document, b *bytes.Buffer) error { return WriteDwgR2000(d, b) }, "R2000"},
		{"line_2000.dwg", func(d *Document, b *bytes.Buffer) error { return WriteDwgR2000(d, b) }, "R2000"},
		{"line_2004.dwg", func(d *Document, b *bytes.Buffer) error { return WriteDwgR2004(d, b) }, "R2004"},
		{"line_2007.dwg", func(d *Document, b *bytes.Buffer) error { return WriteDwgR2007(d, b) }, "R2007"},
		{"line_2010.dwg", func(d *Document, b *bytes.Buffer) error { return WriteDwgR2004(d, b) }, "R2004"},
		{"line_2013.dwg", func(d *Document, b *bytes.Buffer) error { return WriteDwgR2004(d, b) }, "R2004"},
		{"lw_example2018.dwg", func(d *Document, b *bytes.Buffer) error { return WriteDwgR2004(d, b) }, "R2004"},
	}
	for _, tc := range cases {
		doc := parseIntegration(t, tc.sample)
		var out bytes.Buffer
		if err := tc.writer(doc, &out); err != nil {
			t.Fatalf("%s 经 %s 写出失败: %v", tc.sample, tc.kind, err)
		}
		if out.Len() == 0 {
			t.Fatalf("%s 经 %s 写出为空", tc.sample, tc.kind)
		}
		// 交叉代写出必须拒绝（回放素材按代保留）
		var cross bytes.Buffer
		other := func(d *Document, b *bytes.Buffer) error {
			if tc.kind == "R2000" {
				return WriteDwgR2004(d, b)
			}
			return WriteDwgR2000(d, b)
		}
		if err := other(doc, &cross); err == nil {
			t.Errorf("%s 交叉代写出应返回错误", tc.sample)
		}
	}
}

// TestAPITextsConsistency 文本提取与对照导出的一致性：DumpEntities 只导出
// 模型空间直属实体 + 最大块启发式内容，而 Texts 的 INSERT 展开覆盖全部块，
// 故一致性口径取两者的共同实体集（模型空间直属 TEXT/MTEXT）：直属实体的
// 文本必须以相同形态出现在 DumpEntities 输出中。
func TestAPITextsConsistency(t *testing.T) {
	for _, sample := range []string{"text_2000.dwg", "text_2004.dwg", "mtext_2000.dwg", "lw_example2018.dwg"} {
		data := loadAPIE2EData(t, sample)
		doc, err := Parse(data)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", sample, err)
		}
		if len(doc.Texts()) == 0 {
			t.Fatalf("%s 未提取到任何文本", sample)
		}
		dump, err := DumpEntities(data)
		if err != nil {
			t.Fatalf("DumpEntities(%s) 失败: %v", sample, err)
		}
		direct := 0
		for _, ent := range doc.ModelSpace {
			var txt string
			switch e := ent.(type) {
			case *entity.EntText:
				txt = drawing.StripMTextFormat(e.Text)
			case *entity.EntMText:
				txt = drawing.StripMTextFormat(e.Text)
			default:
				continue
			}
			direct++
			if txt == "" {
				continue
			}
			if strings.Contains(txt, "\n") {
				continue // 多行 MTEXT 在 DumpEntities 展开口径下形态不同，跳过精确包含断言
			}
			if !strings.Contains(dump, txt) {
				t.Errorf("%s: 模型空间直属文本 %q 未出现在 DumpEntities 输出中", sample, txt)
			}
		}
		if direct == 0 {
			t.Errorf("%s 模型空间应存在直属文本实体", sample)
		}
	}
}

// TestAPIE2ETextSamples 文本样本的端到端最小断言（矩阵样本无文字，
// 这里补齐文字类样本的 Parse/Texts/RenderPNG 链路）。
func TestAPIE2ETextSamples(t *testing.T) {
	for _, sample := range []string{"text_2000.dwg", "text_2004.dwg", "mtext_2000.dwg", "mtext_2004.dwg"} {
		doc := parseIntegration(t, sample)
		if len(doc.Texts()) == 0 {
			t.Errorf("%s 未提取到文本", sample)
		}
		pngData, err := RenderPNG(doc, RenderOptions{})
		if err != nil {
			t.Fatalf("%s 渲染失败: %v", sample, err)
		}
		if _, err := png.Decode(bytes.NewReader(pngData)); err != nil {
			t.Fatalf("%s 渲染输出不是合法 PNG: %v", sample, err)
		}
	}
}

// TestAPINilAndEmptySafety 零值/nil 文档冒烟：全部导出 API 对 nil 与
// 空文档必须优雅处理（返回错误/零值），不允许 panic。
func TestAPINilAndEmptySafety(t *testing.T) {
	// nil 文档
	if _, err := RenderPNG(nil, RenderOptions{}); err == nil {
		t.Error("RenderPNG(nil) 应返回错误")
	}
	var buf bytes.Buffer
	if err := WriteDwg(nil, &buf); err == nil {
		t.Error("WriteDwg(nil) 应返回错误")
	}
	if err := WriteDwgR2000(nil, &buf); err == nil {
		t.Error("WriteDwgR2000(nil) 应返回错误")
	}
	if err := WriteDwgR2004(nil, &buf); err == nil {
		t.Error("WriteDwgR2004(nil) 应返回错误")
	}
	if err := WriteDwgR2007(nil, &buf); err == nil {
		t.Error("WriteDwgR2007(nil) 应返回错误")
	}
	if _, err := DumpEntities(nil); err == nil {
		t.Error("DumpEntities(nil) 应返回错误")
	}
	if len(DebugLines(nil)) != 0 {
		t.Error("DebugLines(nil) 应返回空 map")
	}
	if _, err := DebugObjectIndexExport(nil); err == nil {
		t.Error("DebugObjectIndexExport(nil) 应返回错误")
	}
	DebugScanGoldLines(nil, nil) // 只要求不 panic
	if _, _, err := DebugObjectBody(nil, 1); err == nil {
		t.Error("DebugObjectBody(nil) 应返回错误")
	}
	if _, err := LoadNamedSectionDebug2(nil, "AcDb:AcDbObjects"); err == nil {
		t.Error("LoadNamedSectionDebug2(nil) 应返回错误")
	}
	if _, _, _, err := DebugRecord2(nil, struct {
		Handle uint64
		Offset uint32
	}{1, 0}, true); err == nil {
		t.Error("DebugRecord2(nil) 应返回错误")
	}

	// 空文档（非 nil，零值）
	empty := &Document{}
	_ = empty.Version()
	if empty.EntityCount() != 0 {
		t.Error("空文档 EntityCount 应为 0")
	}
	if len(empty.Texts()) != 0 {
		t.Error("空文档 Texts 应为空")
	}
	if empty.EntityByHandle(1) != nil {
		t.Error("空文档 EntityByHandle 应返回 nil")
	}
	_ = empty.InternalObjects()
	_ = empty.Xrecords()
	_ = empty.Skipped()
	_ = empty.DebugFailures()
	_ = empty.DebugLayerColors()
	if _, err := RenderPNG(empty, RenderOptions{}); err != nil {
		t.Errorf("空文档渲染应产出兜底图像而非错误: %v", err)
	}
	if err := WriteDwg(empty, &buf); err == nil {
		t.Error("空文档 WriteDwg 应返回错误（无回放素材）")
	}
}
