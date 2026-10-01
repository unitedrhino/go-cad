// main_test.go 验证 caddocling CLI 合同：manifest 关键字段（schema 锁死
// 字段逐项断言，不做逐字节比对）、DXF 输入解析、--width 传递与显式宽度
// 下停用自动重渲、--no-full-image 跳过整图预览（带图框文档走 emit 直测，
// 无图框兜底强制产出的语义由 DWG 用例反证）。
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unitedrhino/go-cad"
	"github.com/unitedrhino/go-cad/internal/drawing"
	"github.com/unitedrhino/go-cad/internal/entity"
)

// minimalDXF 最小 ASCII DXF 样例：HEADER 版本 + LAYER 表 + 一条 45° 旋转
// 的 TEXT 实体（覆盖层名/插入点/字高/旋转的端到端导出链路）。
const minimalDXF = `0
SECTION
2
HEADER
9
$ACADVER
1
AC1032
0
ENDSEC
0
SECTION
2
TABLES
0
TABLE
2
LAYER
5
2
70
1
0
LAYER
5
10
2
TESTLAYER
70
0
62
7
0
ENDTAB
0
ENDSEC
0
SECTION
2
ENTITIES
0
TEXT
5
30
8
TESTLAYER
10
100.0
20
200.0
30
0.0
40
3.5
1
HELLO DXF
50
45.0
0
ENDSEC
0
EOF
`

// writeManifestInput 写输入文件并返回路径。
func writeManifestInput(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写输入文件: %v", err)
	}
	return path
}

// parseManifest 读取并解析 manifest.json（断言存在）。
func parseManifest(t *testing.T, dir string) manifestDoc {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("读 manifest.json: %v", err)
	}
	var m manifestDoc
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("解析 manifest.json: %v", err)
	}
	if m.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, 期望 1", m.SchemaVersion)
	}
	return m
}

// lastManifestLine 取 stdout 末行并断言 manifest 输出合同，返回路径。
func lastManifestLine(t *testing.T, stdout string) string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[len(lines)-1], "manifest: ") {
		t.Fatalf("stdout 末行应为目标行, 得到: %q", stdout)
	}
	return strings.TrimPrefix(lines[len(lines)-1], "manifest: ")
}

// TestRunDWGManifest DWG 输入全链路：manifest 关键字段 + 文件落盘。
func TestRunDWGManifest(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	input := filepath.Join("..", "..", "testdata", "lw_example2018.dwg")
	if err := run([]string{input, "-o", dir}, &buf); err != nil {
		t.Fatalf("run: %v", err)
	}
	manifestPath := lastManifestLine(t, buf.String())
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("manifest 路径不可达 %s: %v", manifestPath, err)
	}
	m := parseManifest(t, dir)
	if m.Generator != "go-cad caddocling" {
		t.Fatalf("generator = %q", m.Generator)
	}
	if m.Source != "lw_example2018.dwg" {
		t.Fatalf("source = %q, 期望 lw_example2018.dwg", m.Source)
	}
	if m.DwgVersion == "" {
		t.Fatalf("dwg_version 为空")
	}
	if len(m.Sheets) < 1 {
		t.Fatalf("拆图记录为空")
	}
	s := m.Sheets[0]
	if s.Index != 1 || s.Image == "" || s.Width <= 0 {
		t.Fatalf("拆图记录异常: %+v", s)
	}
	imgPath := filepath.Join(dir, s.Image)
	if _, err := os.Stat(imgPath); err != nil {
		t.Fatalf("拆图文件缺失 %s: %v", s.Image, err)
	}
	if !strings.HasSuffix(s.Image, ".png") {
		t.Fatalf("缺省格式应为 png: %q", s.Image)
	}
	if s.PassRate < 0 {
		t.Fatalf("png 拆图达标率未统计: %v", s.PassRate)
	}
	// 无图框兜底或未禁用时整图预览必须产出
	if m.FullImage == "" {
		t.Fatalf("full_image 为空, 期望产出整图预览")
	}
	if _, err := os.Stat(filepath.Join(dir, m.FullImage)); err != nil {
		t.Fatalf("整图文件缺失 %s: %v", m.FullImage, err)
	}
	if m.Texts == nil {
		t.Fatalf("texts 应为数组（空也输出 []）")
	}
}

// approxF 近似相等断言（DXF 解析的浮点存在位流舍入，不做精确比较）。
func approxF(t *testing.T, label string, got, want float64) {
	t.Helper()
	if got-want > 1e-9 || want-got > 1e-9 {
		t.Fatalf("%s = %v, 期望 %v", label, got, want)
	}
}

// TestRunDXF DXF 输入：文本/层名/旋转/坐标经解析-收集-清单链路导出。
func TestRunDXF(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	input := writeManifestInput(t, dir, "sample.dxf", minimalDXF)
	if err := run([]string{input}, &buf); err != nil {
		t.Fatalf("run: %v", err)
	}
	lastManifestLine(t, buf.String())
	// 缺省输出目录 = 输入同目录 <源名>_caddocling/
	outDir := filepath.Join(dir, "sample_caddocling")
	m := parseManifest(t, outDir)
	if m.DwgVersion == "" {
		t.Fatalf("dwg_version 为空")
	}
	if len(m.Texts) != 1 {
		t.Fatalf("texts 条数 = %d, 期望 1: %+v", len(m.Texts), m.Texts)
	}
	tx := m.Texts[0]
	if tx.Text != "HELLO DXF" {
		t.Fatalf("text = %q, 期望 HELLO DXF", tx.Text)
	}
	if tx.Layer != "TESTLAYER" {
		t.Fatalf("layer = %q, 期望 TESTLAYER", tx.Layer)
	}
	approxF(t, "插入点 X", tx.X, 100)
	approxF(t, "插入点 Y", tx.Y, 200)
	approxF(t, "字高", tx.Height, 3.5)
	approxF(t, "旋转", tx.Rotation, 45)
	if tx.Sheet != 0 {
		t.Fatalf("无图框 DXF 归属 = %d, 期望 0", tx.Sheet)
	}
	if m.FullImage == "" {
		t.Fatalf("无图框兜底时整图预览必须产出")
	}
}

// TestRunWidth --width 显式传递：拆图与整图均按指定宽度渲染，显式宽度
// 下清晰度自检不自动重渲。
func TestRunWidth(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	input := filepath.Join("..", "..", "testdata", "lw_example2018.dwg")
	if err := run([]string{input, "-o", dir, "--width", "3000"}, &buf); err != nil {
		t.Fatalf("run: %v", err)
	}
	lastManifestLine(t, buf.String())
	m := parseManifest(t, dir)
	if len(m.Sheets) < 1 || m.Sheets[0].Width != 3000 {
		t.Fatalf("拆图宽度 = %+v, 期望 3000", m.Sheets)
	}
	if m.Sheets[0].Rerenders != 0 {
		t.Fatalf("显式宽度不应自动重渲: %d", m.Sheets[0].Rerenders)
	}
	if !strings.Contains(buf.String(), "整图 3000 宽") {
		t.Fatalf("整图应按 --width 3000 渲染, stdout: %s", buf.String())
	}
}

// buildFrameDoc 构造带标准图幅图框的最小文档（口径与 render 包归属测试
// 一致）：闭合矩形框线块 + 标题文本 + 框内/框外直属文本与足量框内代表点，
// 保证 DetectSheets 识别出恰好 1 个图框。
func buildFrameDoc() *cad.Document {
	const frameHeader = 100
	doc := &cad.Document{
		ModelSpace: []any{},
		Blocks:     map[uint64][]any{},
		Attribs:    map[uint64]*entity.EntAttrib{},
		LayerColors: map[uint64]drawing.LayerColor{
			201: {Index: 1, Name: "WATER"},
		},
	}
	doc.Blocks[frameHeader] = []any{
		&entity.EntLwPolyline{
			BaseEntity: entity.BaseEntity{Handle: 1, Layer: 201, Mode: 0},
			Vertices: []entity.Point2{
				{X: 0, Y: 0}, {X: 42000, Y: 0}, {X: 42000, Y: 29700}, {X: 0, Y: 29700},
			},
		},
		&entity.EntText{
			BaseEntity: entity.BaseEntity{Handle: 2, Layer: 201, Mode: 0},
			Text:       "测试图", Insertion: entity.Point3{X: 40000, Y: 1500}, Height: 500,
		},
	}
	text := func(handle uint64, s string, x, y float64) *entity.EntText {
		return &entity.EntText{
			BaseEntity: entity.BaseEntity{Handle: handle, Layer: 201, Mode: 2},
			Text:       s, Insertion: entity.Point3{X: x, Y: y}, Height: 350,
		}
	}
	line := func(handle uint64, x1, y1, x2, y2 float64) *entity.EntLine {
		return &entity.EntLine{
			BaseEntity: entity.BaseEntity{Handle: handle, Layer: 201, Mode: 2},
			Start:      entity.Point3{X: x1, Y: y1},
			End:        entity.Point3{X: x2, Y: y2},
		}
	}
	doc.ModelSpace = []any{
		&entity.EntInsert{
			BaseEntity:  entity.BaseEntity{Handle: 10, Layer: 201, Mode: 2},
			Scale:       entity.Point3{X: 1, Y: 1, Z: 1},
			BlockHeader: frameHeader,
		},
		text(11, "框内文本", 5000, 15000),
		text(12, "框内线段一", 2000, 2000),
		text(13, "框内线段二", 2000, 3000),
		// 框内代表点补足图框内容密度判据（TEXT 插入点 3 + LINE 端点 6 ≥ 8）
		line(15, 2000, 4000, 8000, 4000),
		line(16, 2000, 5000, 8000, 5000),
		line(17, 2000, 6000, 8000, 6000),
		text(14, "框外文本", 100000, 100000),
	}
	return doc
}

// TestEmitNoFullImageWithFrame 带图框文档 + --no-full-image：整图预览跳过；
// 拆图文件序号与 texts 归属序号对齐（全览张不占 01 号）。
func TestEmitNoFullImageWithFrame(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	if _, err := emit(buildFrameDoc(), "frame.dwg", dir, 0, true, "png", &buf); err != nil {
		t.Fatalf("emit: %v", err)
	}
	lastManifestLine(t, buf.String())
	m := parseManifest(t, dir)
	if m.FullImage != "" {
		t.Fatalf("--no-full-image 应跳过整图预览, 得到 %q", m.FullImage)
	}
	if _, err := os.Stat(filepath.Join(dir, "frame-full.png")); !os.IsNotExist(err) {
		t.Fatalf("整图文件不应产出")
	}
	// 拆图仅图框张（全览张跳过），序号从 01 起与归属序号对齐
	if len(m.Sheets) != 1 {
		t.Fatalf("拆图记录数 = %d, 期望 1（仅图框张）: %+v", len(m.Sheets), m.Sheets)
	}
	s := m.Sheets[0]
	if s.Index != 1 || s.Name != "测试图" {
		t.Fatalf("拆图记录 = %+v, 期望 index=1 name=测试图", s)
	}
	if s.Image != "frame-01-测试图.png" {
		t.Fatalf("拆图文件名 = %q, 期望 frame-01-测试图.png", s.Image)
	}
	if _, err := os.Stat(filepath.Join(dir, s.Image)); err != nil {
		t.Fatalf("拆图文件缺失: %v", err)
	}
	if s.TextCount != 4 {
		t.Fatalf("text_count = %d, 期望 4（框内直属 3 + 块内标题 1）", s.TextCount)
	}
	sheets := map[string]int{}
	for _, tx := range m.Texts {
		sheets[tx.Text] = tx.Sheet
	}
	if len(m.Texts) != 5 || sheets["框外文本"] != 0 || sheets["框内文本"] != 1 {
		t.Fatalf("texts 归属异常: %+v", m.Texts)
	}
}
