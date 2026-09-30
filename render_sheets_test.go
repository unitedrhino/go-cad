// render_sheets_test.go 图框切分逐张出图的单元测试：
// DetectSheets 的候选识别/比例尺寸过滤/标题栏图名/嵌套块/重叠去重/
// 无图框兜底，RenderSheetPNG/SVG 的视口裁剪与默认 4096 宽，RenderAllSheets
// 的批量分派与错误口径，全部 testdata 样本 DetectSheets 不 panic。
package cad

import (
	"bytes"
	"fmt"
	"github.com/unitedrhino/go-cad/internal/entity"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// buildSheetDoc 构造多图框测试文档：blockHeader 句柄为 h 的图框块，
// 块内含 (0,0)-(w,h) 边界 LWPOLYLINE 与标题栏区（右下角 1/4×1/6）
// 内的图名 TEXT；插入点 offset。框内放置直属 LINE 内容（满足内容
// 密度判据，与真实图框"装内容"语义一致）。attachTitle 为 false 时
// 不放图名文字（验证块名/序号回退）。
func buildSheetDoc(t *testing.T, sheets []struct {
	h        uint64
	name     string
	title    string
	offset   [2]float64
	w, hgt   float64
	scale    float64
	rotation float64
}) *Document {
	t.Helper()
	doc := &Document{
		blocks:          map[uint64][]any{},
		attribs:         map[uint64]*entity.EntAttrib{},
		layerColors:     map[uint64]layerColor{},
		internalObjects: map[uint64]*objGeneric{},
	}
	for _, s := range sheets {
		w, hgt := s.w, s.hgt
		frame := []any{
			&entity.EntLwPolyline{Vertices: []entity.Point2{{0, 0}, {w, 0}, {w, hgt}, {0, hgt}, {0, 0}}},
		}
		if s.title != "" {
			// 图名放在右下角标题栏区（x ∈ [w-w/4, w]，y ∈ [0, h/6]）内
			frame = append(frame, &entity.EntText{
				Text:      s.title,
				Insertion: entity.Point3{w - w/8, hgt / 12, 0},
				Height:    hgt / 28,
			})
		}
		doc.blocks[s.h] = frame
		doc.internalObjects[s.h] = &objGeneric{
			Name:   "BLOCK_HEADER",
			Fields: []objField{{Key: "name", Val: s.name}},
		}
		ins := &entity.EntInsert{
			Position:    entity.Point3{s.offset[0], s.offset[1], 0},
			Scale:       entity.Point3{s.scale, s.scale, s.scale},
			Rotation:    s.rotation,
			BlockHeader: s.h,
		}
		doc.modelSpace = append(doc.modelSpace, ins)
		// 框内直属内容（内容密度判据，4 条线 = 8 个代表点达下限）
		ox, oy := s.offset[0], s.offset[1]
		doc.modelSpace = append(doc.modelSpace,
			&entity.EntLine{Start: entity.Point3{ox + w/4, oy + hgt/2, 0}, End: entity.Point3{ox + w*3/4, oy + hgt/2, 0}},
			&entity.EntLine{Start: entity.Point3{ox + w/4, oy + hgt/4, 0}, End: entity.Point3{ox + w/2, oy + hgt*3/4, 0}},
			&entity.EntLine{Start: entity.Point3{ox + w*3/5, oy + hgt/5, 0}, End: entity.Point3{ox + w*4/5, oy + hgt/5, 0}},
			&entity.EntLine{Start: entity.Point3{ox + w/6, oy + hgt/6, 0}, End: entity.Point3{ox + w/3, oy + hgt/3, 0}},
		)
	}
	return doc
}

// aSeriesSheet A 系图幅参数简写（1189×841 比例 √2，缩放 1）。
func aSeriesSheet(h uint64, name, title string, offset [2]float64) struct {
	h        uint64
	name     string
	title    string
	offset   [2]float64
	w, hgt   float64
	scale    float64
	rotation float64
} {
	return struct {
		h        uint64
		name     string
		title    string
		offset   [2]float64
		w, hgt   float64
		scale    float64
		rotation float64
	}{h: h, name: name, title: title, offset: offset, w: 1189, hgt: 841, scale: 1}
}

func TestDetectSheetsBasic(t *testing.T) {
	doc := buildSheetDoc(t, []struct {
		h        uint64
		name     string
		title    string
		offset   [2]float64
		w, hgt   float64
		scale    float64
		rotation float64
	}{
		aSeriesSheet(100, "A1图框", "配电系统图", [2]float64{0, 0}),
		aSeriesSheet(101, "A1图框", "照明平面图", [2]float64{1500, 0}),
	})
	sheets := DetectSheets(doc)
	if len(sheets) != 2 {
		t.Fatalf("应识别 2 张图框，实际 %d：%+v", len(sheets), sheets)
	}
	if sheets[0].Name != "配电系统图" || sheets[1].Name != "照明平面图" {
		t.Fatalf("图名错误: %q / %q", sheets[0].Name, sheets[1].Name)
	}
	b := sheets[0].Box
	if math.Abs(b[0]-0) > 1 || math.Abs(b[3]-841) > 1 {
		t.Fatalf("图框包围盒错误: %v", b)
	}
	// 第二张偏移 1500
	if math.Abs(sheets[1].Box[0]-1500) > 1 {
		t.Fatalf("第二张包围盒错误: %v", sheets[1].Box)
	}
}

func TestDetectSheetsRotation(t *testing.T) {
	// 旋转 90° 的图框：AABB 交换宽高，比例仍按大/小判定
	doc := buildSheetDoc(t, []struct {
		h        uint64
		name     string
		title    string
		offset   [2]float64
		w, hgt   float64
		scale    float64
		rotation float64
	}{aSeriesSheet(100, "A1图框", "原理图", [2]float64{0, 0})})
	doc.modelSpace[0].(*entity.EntInsert).Rotation = math.Pi / 2
	// 自动内容线在未旋转位置，旋转后落框外；按旋转后 AABB（x∈[-841,0]
	// y∈[0,1189]）补框内直属内容（4 条线 = 8 点）
	doc.modelSpace = append(doc.modelSpace,
		&entity.EntLine{Start: entity.Point3{-600, 300, 0}, End: entity.Point3{-200, 300, 0}},
		&entity.EntLine{Start: entity.Point3{-600, 600, 0}, End: entity.Point3{-300, 900, 0}},
		&entity.EntLine{Start: entity.Point3{-700, 150, 0}, End: entity.Point3{-400, 150, 0}},
		&entity.EntLine{Start: entity.Point3{-650, 1000, 0}, End: entity.Point3{-350, 1000, 0}},
	)
	sheets := DetectSheets(doc)
	if len(sheets) != 1 {
		t.Fatalf("旋转图框应识别 1 张，实际 %d", len(sheets))
	}
	b := sheets[0].Box
	if math.Abs((b[2]-b[0])-841) > 2 || math.Abs((b[3]-b[1])-1189) > 2 {
		t.Fatalf("旋转后 AABB 错误: %v", b)
	}
}

func TestDetectSheetsRatioAndSizeFilter(t *testing.T) {
	doc := buildSheetDoc(t, []struct {
		h        uint64
		name     string
		title    string
		offset   [2]float64
		w, hgt   float64
		scale    float64
		rotation float64
	}{
		// 正方形大块：比例 1.0 不入选
		{h: 100, name: "方块", offset: [2]float64{0, 0}, w: 2000, hgt: 2000, scale: 1},
		// A 系比例但太小（最短边 < 500）：不入选
		{h: 101, name: "小块", offset: [2]float64{0, 0}, w: 118.9, hgt: 84.1, scale: 1},
	})
	if got := DetectSheets(doc); len(got) != 1 {
		t.Fatalf("比例/尺寸过滤后应兜底整图 1 张，实际 %d：%+v", len(got), got)
	} else if got[0].Name != "整图" {
		t.Fatalf("兜底图名应为 整图，实际 %q", got[0].Name)
	}
}

func TestDetectSheetsTitleFallback(t *testing.T) {
	// 无标题栏文字 → 块名回退
	doc := buildSheetDoc(t, []struct {
		h        uint64
		name     string
		title    string
		offset   [2]float64
		w, hgt   float64
		scale    float64
		rotation float64
	}{aSeriesSheet(100, "A1图框", "", [2]float64{0, 0})})
	sheets := DetectSheets(doc)
	if len(sheets) != 1 || sheets[0].Name != "A1图框" {
		t.Fatalf("块名回退失败: %+v", sheets)
	}
	// 匿名块名（* 前缀）→ 序号回退
	doc.internalObjects[100] = &objGeneric{Fields: []objField{{Key: "name", Val: "*Model_Space"}}}
	sheets = DetectSheets(doc)
	if len(sheets) != 1 || sheets[0].Name != "图 1" {
		t.Fatalf("序号回退失败: %+v", sheets)
	}
}

func TestDetectSheetsNestedInsert(t *testing.T) {
	// 图框块 200 内嵌标题栏块 201（含图名 TEXT），外框直接 LINE
	doc := &Document{
		blocks: map[uint64][]any{
			200: {
				&entity.EntLwPolyline{Vertices: []entity.Point2{{0, 0}, {1189, 0}, {1189, 841}, {0, 841}, {0, 0}}},
				&entity.EntInsert{Position: entity.Point3{950, 20, 0}, Scale: entity.Point3{1, 1, 1}, BlockHeader: 201},
			},
			201: {&entity.EntText{Text: "嵌套图名", Insertion: entity.Point3{0, 30, 0}, Height: 30}},
		},
		attribs:         map[uint64]*entity.EntAttrib{},
		layerColors:     map[uint64]layerColor{},
		internalObjects: map[uint64]*objGeneric{},
	}
	doc.modelSpace = []any{
		&entity.EntInsert{Position: entity.Point3{0, 0, 0}, Scale: entity.Point3{1, 1, 1}, BlockHeader: 200},
		// 框内直属内容（内容密度判据，4 条线 = 8 点）
		&entity.EntLine{Start: entity.Point3{100, 400, 0}, End: entity.Point3{900, 400, 0}},
		&entity.EntLine{Start: entity.Point3{100, 200, 0}, End: entity.Point3{600, 600, 0}},
		&entity.EntLine{Start: entity.Point3{700, 150, 0}, End: entity.Point3{1000, 150, 0}},
		&entity.EntLine{Start: entity.Point3{150, 100, 0}, End: entity.Point3{400, 300, 0}},
	}
	sheets := DetectSheets(doc)
	if len(sheets) != 1 {
		t.Fatalf("嵌套图框应识别 1 张，实际 %d", len(sheets))
	}
	// 嵌套块内 TEXT 位于 (950,50)，在标题栏区 x∈[891.75,1189] y∈[0,140] 内 → 图名命中
	if sheets[0].Name != "嵌套图名" {
		t.Fatalf("嵌套标题栏图名提取失败: %q", sheets[0].Name)
	}
}

func TestDetectSheetsAttribTitle(t *testing.T) {
	// 图名是块参照的 ATTRIB 属性文字
	doc := &Document{
		blocks: map[uint64][]any{
			300: {&entity.EntLwPolyline{Vertices: []entity.Point2{{0, 0}, {1189, 0}, {1189, 841}, {0, 841}, {0, 0}}}},
		},
		attribs: map[uint64]*entity.EntAttrib{
			900: {Text: "属性图名", Insertion: entity.Point3{950, 60, 0}, Height: 30},
		},
		layerColors:     map[uint64]layerColor{},
		internalObjects: map[uint64]*objGeneric{},
	}
	doc.modelSpace = []any{
		&entity.EntInsert{
			Position: entity.Point3{0, 0, 0}, Scale: entity.Point3{1, 1, 1}, BlockHeader: 300,
			Attribs: []uint64{900},
		},
		// 框内直属内容（内容密度判据，4 条线 = 8 点）
		&entity.EntLine{Start: entity.Point3{100, 400, 0}, End: entity.Point3{900, 400, 0}},
		&entity.EntLine{Start: entity.Point3{100, 200, 0}, End: entity.Point3{600, 600, 0}},
		&entity.EntLine{Start: entity.Point3{700, 150, 0}, End: entity.Point3{1000, 150, 0}},
		&entity.EntLine{Start: entity.Point3{150, 100, 0}, End: entity.Point3{400, 300, 0}},
	}
	sheets := DetectSheets(doc)
	if len(sheets) != 1 || sheets[0].Name != "属性图名" {
		t.Fatalf("ATTRIB 图名提取失败: %+v", sheets)
	}
}

func TestDetectSheetsOverlapDedup(t *testing.T) {
	// 同一位置重复插入两份相同图框 → 去重为 1 张
	sheets := []struct {
		h        uint64
		name     string
		title    string
		offset   [2]float64
		w, hgt   float64
		scale    float64
		rotation float64
	}{
		aSeriesSheet(100, "A1图框", "重叠图名", [2]float64{0, 0}),
		aSeriesSheet(101, "A1图框", "重叠图名2", [2]float64{5, 5}),
	}
	doc := buildSheetDoc(t, sheets)
	got := DetectSheets(doc)
	if len(got) != 1 {
		t.Fatalf("重叠图框应去重为 1 张，实际 %d", len(got))
	}
	if got[0].Name != "重叠图名" {
		t.Fatalf("应保留面积更大的首个候选: %q", got[0].Name)
	}
}

func TestDetectSheetsFallbackNoInsert(t *testing.T) {
	// 纯图元模型空间（无 INSERT）→ 兜底整图单张
	doc := &Document{
		blocks:          map[uint64][]any{},
		attribs:         map[uint64]*entity.EntAttrib{},
		layerColors:     map[uint64]layerColor{},
		internalObjects: map[uint64]*objGeneric{},
		modelSpace: []any{
			&entity.EntLine{Start: entity.Point3{0, 0, 0}, End: entity.Point3{1000, 600, 0}},
		},
	}
	sheets := DetectSheets(doc)
	if len(sheets) != 1 || sheets[0].Name != "整图" {
		t.Fatalf("无图框应兜底整图: %+v", sheets)
	}
	if sheets[0].Box[2] <= sheets[0].Box[0] {
		t.Fatalf("兜底包围盒无效: %v", sheets[0].Box)
	}
}

func TestDetectSheetsNilDoc(t *testing.T) {
	if got := DetectSheets(nil); got != nil {
		t.Fatalf("nil 文档应返回 nil: %+v", got)
	}
}

func TestDetectSheetsAllTestdataNoPanic(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("testdata", "*.dwg"))
	if len(files) == 0 {
		t.Skip("无样本")
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		doc, err := Parse(data)
		if err != nil {
			continue // 解码失败样本由解码回归覆盖
		}
		sheets := DetectSheets(doc)
		if len(sheets) == 0 {
			t.Errorf("%s: DetectSheets 返回空（应至少兜底整图 1 张）", f)
		}
		for _, s := range sheets {
			b := s.Box
			if b[0] > b[2] || b[1] > b[3] {
				t.Errorf("%s: 图框 %q 包围盒非法: %v", f, s.Name, b)
			}
		}
	}
}

// ---- 渲染 ----

func TestRenderSheetPNGViewport(t *testing.T) {
	doc := buildSheetDoc(t, []struct {
		h        uint64
		name     string
		title    string
		offset   [2]float64
		w, hgt   float64
		scale    float64
		rotation float64
	}{
		aSeriesSheet(100, "A1图框", "配电系统图", [2]float64{0, 0}),
		aSeriesSheet(101, "A1图框", "照明平面图", [2]float64{1500, 0}),
	})
	sheets := DetectSheets(doc)
	data, err := RenderSheetPNG(doc, sheets[1], RenderOptions{})
	if err != nil {
		t.Fatalf("RenderSheetPNG: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("PNG 解码: %v", err)
	}
	// 默认 4096 宽（单图幅文字清晰口径）
	if img.Bounds().Dx() != SheetDefaultWidth {
		t.Fatalf("默认宽度应 %d，实际 %d", SheetDefaultWidth, img.Bounds().Dx())
	}
	// 高度按 A 系比例（√2）自适应
	h := img.Bounds().Dy()
	if h < 2700 || h > 3100 {
		t.Fatalf("高度比例异常: %d", h)
	}
	// 调用方覆盖宽度生效
	data, err = RenderSheetPNG(doc, sheets[1], RenderOptions{Width: 1024})
	if err != nil {
		t.Fatalf("RenderSheetPNG 自定义宽度: %v", err)
	}
	img, _ = png.Decode(bytes.NewReader(data))
	if img.Bounds().Dx() != 1024 {
		t.Fatalf("自定义宽度未生效: %d", img.Bounds().Dx())
	}
}

func TestRenderSheetSVGViewport(t *testing.T) {
	doc := buildSheetDoc(t, []struct {
		h        uint64
		name     string
		title    string
		offset   [2]float64
		w, hgt   float64
		scale    float64
		rotation float64
	}{aSeriesSheet(100, "A1图框", "配电系统图", [2]float64{0, 0})})
	sheets := DetectSheets(doc)
	data, err := RenderSheetSVG(doc, sheets[0], RenderOptions{})
	if err != nil {
		t.Fatalf("RenderSheetSVG: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "<svg") || !strings.Contains(s, `</svg>`) {
		t.Fatal("输出非法 SVG")
	}
	// 图名以 text 元素输出且落在该图框视口内（第二张偏移出界不出现）
	if !strings.Contains(s, "配电系统图") {
		t.Fatal("SVG 应含本图框图名 text 元素")
	}
}

func TestRenderSheetNilDoc(t *testing.T) {
	if _, err := RenderSheetPNG(nil, Sheet{}, RenderOptions{}); err == nil {
		t.Fatal("nil 文档应报错")
	}
	if _, err := RenderSheetSVG(nil, Sheet{}, RenderOptions{}); err == nil {
		t.Fatal("nil 文档应报错")
	}
}

func TestRenderAllSheets(t *testing.T) {
	doc := buildSheetDoc(t, []struct {
		h        uint64
		name     string
		title    string
		offset   [2]float64
		w, hgt   float64
		scale    float64
		rotation float64
	}{
		aSeriesSheet(100, "A1图框", "配电系统图", [2]float64{0, 0}),
		aSeriesSheet(101, "A1图框", "照明平面图", [2]float64{1500, 0}),
	})
	results, err := RenderAllSheets(doc, RenderOptions{}, "png")
	if err != nil {
		t.Fatalf("RenderAllSheets: %v", err)
	}
	// 首项固定为整图全览，其后按序为标准图框
	if len(results) != 3 {
		t.Fatalf("应输出 3 张（整图全览 + 2 标准图框），实际 %d", len(results))
	}
	if results[0].Name != "整图全览" {
		t.Fatalf("首项应为整图全览，实际 %q", results[0].Name)
	}
	if results[1].Name != "配电系统图" || results[2].Name != "照明平面图" {
		t.Fatalf("结果图名错误: %q / %q", results[1].Name, results[2].Name)
	}
	img, err := png.Decode(bytes.NewReader(results[0].Data))
	if err != nil {
		t.Fatalf("PNG 解码: %v", err)
	}
	// 全览张未显式指定宽度时钳到 SheetDefaultWidth（防整图微缩字高把
	// 自适应宽度推满上限）
	if img.Bounds().Dx() != SheetDefaultWidth {
		t.Fatalf("全览默认宽度应 %d，实际 %d", SheetDefaultWidth, img.Bounds().Dx())
	}
	// 标准图框张默认宽度仍为自适应下限 SheetDefaultWidth
	img, err = png.Decode(bytes.NewReader(results[1].Data))
	if err != nil {
		t.Fatalf("PNG 解码: %v", err)
	}
	if img.Bounds().Dx() != SheetDefaultWidth {
		t.Fatalf("批量渲染默认宽度应 %d，实际 %d", SheetDefaultWidth, img.Bounds().Dx())
	}
	// SVG 分派（显式宽度对全览同样生效）
	results, err = RenderAllSheets(doc, RenderOptions{Width: 800}, "SVG")
	if err != nil {
		t.Fatalf("SVG 分派: %v", err)
	}
	if !bytes.Contains(results[0].Data, []byte("<svg")) {
		t.Fatal("SVG 格式未生效")
	}
	// 非法格式
	if _, err := RenderAllSheets(doc, RenderOptions{}, "jpg"); err == nil {
		t.Fatal("非法格式应报错")
	}
	// nil 文档
	if _, err := RenderAllSheets(nil, RenderOptions{}, "png"); err == nil {
		t.Fatal("nil 文档应报错")
	}
}

func TestRenderAllSheetsFallbackSingle(t *testing.T) {
	// 无图框样本 → 批量接口兜底整图单张
	doc := &Document{
		blocks:          map[uint64][]any{},
		attribs:         map[uint64]*entity.EntAttrib{},
		layerColors:     map[uint64]layerColor{},
		internalObjects: map[uint64]*objGeneric{},
		modelSpace: []any{
			&entity.EntLine{Start: entity.Point3{0, 0, 0}, End: entity.Point3{500, 300, 0}},
		},
	}
	results, err := RenderAllSheets(doc, RenderOptions{Width: 512}, "png")
	if err != nil {
		t.Fatalf("RenderAllSheets: %v", err)
	}
	if len(results) != 1 || results[0].Name != "整图" {
		t.Fatalf("无图框应兜底单张整图: %+v", results)
	}
}

func TestSanitizeSheetName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"配电系统图", "配电系统图"},
		{`照明/平面:图`, "照明-平面-图"},
		{"a<b>c|d*e?f\"g", "a-b-c-d-e-f-g"},
		{"多  空白\t制表", "多-空白-制表"},
		{"---标题---", "标题"},
		{"", "sheet"},
		{"///", "sheet"},
		{strings.Repeat("长", 100), strings.Repeat("长", 80)},
	}
	for _, c := range cases {
		if got := SanitizeSheetName(c.in); got != c.want {
			t.Errorf("SanitizeSheetName(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

// buildSheetNoDoc 构造带图号/图名标题栏的单图框文档（1189×841 A 系）：
// 图号 TEXT 位于右下角标题栏区（insertion (990, 50)），图名 TEXT 位于
// 图号正上方一个标题栏行距处（右端对齐），titleRegion 之外的布局与
// 真实设计院标题栏一致；noText/extra 控制干扰文本。
func buildSheetNoDoc(t *testing.T, no, name string, extra func(block []any) []any) *Document {
	t.Helper()
	doc := &Document{
		blocks:          map[uint64][]any{},
		attribs:         map[uint64]*entity.EntAttrib{},
		layerColors:     map[uint64]layerColor{},
		internalObjects: map[uint64]*objGeneric{},
	}
	block := []any{
		&entity.EntLwPolyline{Vertices: []entity.Point2{{0, 0}, {1189, 0}, {1189, 841}, {0, 841}, {0, 0}}},
	}
	if no != "" {
		block = append(block, &entity.EntText{Text: no, Insertion: entity.Point3{990, 50, 0}, Height: 30})
	}
	if name != "" {
		// 图名在图号上方 19×字高（真实标题栏行距），右端对齐
		rightNo := 990 + estTextWidth(no, 30)
		x := rightNo - estTextWidth(name, 30)
		block = append(block, &entity.EntText{Text: name, Insertion: entity.Point3{x, 50 + 19*30, 0}, Height: 30})
	}
	if extra != nil {
		block = extra(block)
	}
	doc.blocks[100] = block
	doc.internalObjects[100] = &objGeneric{Name: "BLOCK_HEADER", Fields: []objField{{Key: "name", Val: "图框"}}}
	doc.modelSpace = []any{
		&entity.EntInsert{Position: entity.Point3{0, 0, 0}, Scale: entity.Point3{1, 1, 1}, BlockHeader: 100},
		// 框内直属内容（内容密度判据，4 条线 = 8 点）
		&entity.EntLine{Start: entity.Point3{100, 400, 0}, End: entity.Point3{900, 400, 0}},
		&entity.EntLine{Start: entity.Point3{100, 200, 0}, End: entity.Point3{600, 600, 0}},
		&entity.EntLine{Start: entity.Point3{700, 150, 0}, End: entity.Point3{1000, 150, 0}},
		&entity.EntLine{Start: entity.Point3{150, 100, 0}, End: entity.Point3{400, 300, 0}},
	}
	return doc
}

func TestDetectSheetsSheetNoName(t *testing.T) {
	// 图号 + 图名（右端对齐排图号上方）→ 组合名 "图号-图名"
	doc := buildSheetNoDoc(t, "RD(XF)-05", "地下一层消防平面图", nil)
	sheets := DetectSheets(doc)
	if len(sheets) != 1 {
		t.Fatalf("应识别 1 张，实际 %d", len(sheets))
	}
	if got := sheets[0].Name; got != "RD(XF)-05-地下一层消防平面图" {
		t.Fatalf("图号-图名组合错误: %q", got)
	}
}

func TestDetectSheetsSheetNoPriority(t *testing.T) {
	// 标题栏区存在字号更大的中文图例/线缆文本时，图号模式仍优先，
	// 且图名不被左侧表格中文（右端不对齐）劫持
	doc := buildSheetNoDoc(t, "RD(XF)-05", "地下一层消防平面图", func(block []any) []any {
		return append(block,
			// 标题栏区内字号更大的线缆型号与图号左侧的图例中文：
			// 前者不匹配图号模式，后者右端与图号右端相距数百字高
			&entity.EntText{Text: "SC25", Insertion: entity.Point3{1000, 80, 0}, Height: 45},
			&entity.EntText{Text: "消防设备线型图例表说明文字", Insertion: entity.Point3{400, 80, 0}, Height: 40},
		)
	})
	if got := DetectSheets(doc)[0].Name; got != "RD(XF)-05-地下一层消防平面图" {
		t.Fatalf("图号应优先于更大字号图例文本: %q", got)
	}
}

func TestDetectSheetsSheetNoOnly(t *testing.T) {
	// 仅有图号（附近无中文长文本）→ 图名单独成图名
	doc := buildSheetNoDoc(t, "RD-24", "", nil)
	if got := DetectSheets(doc)[0].Name; got != "RD-24" {
		t.Fatalf("仅图号错误: %q", got)
	}
}

func TestDetectSheetsSheetNoAbsent(t *testing.T) {
	// 标题栏区无图号 → 回退区内最大字号文本（含更大字号干扰时取其内容）
	doc := buildSheetNoDoc(t, "", "普通图名", func(block []any) []any {
		return append(block, &entity.EntText{Text: "大字表格", Insertion: entity.Point3{940, 80, 0}, Height: 60})
	})
	if got := DetectSheets(doc)[0].Name; got != "大字表格" {
		t.Fatalf("无图号应回退最大字号文本: %q", got)
	}
}

func TestSheetCjkName(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"地下一层消防平面图", true},
		{"十层、十一层、十二层消防平面图", true},
		{"火灾报警二总线+电源总线", true},
		{"图名", false},                // 汉字数 <4
		{"WDZB1N-KYJY-4x2.5", false}, // 无汉字
		{"RD-24", false},
		{"SC管、RC管代用说明", true},
	}
	for _, c := range cases {
		if got := sheetCjkName(c.in); got != c.want {
			t.Errorf("sheetCjkName(%q) = %v, 期望 %v", c.in, got, c.want)
		}
	}
}

func TestSheetNoRe(t *testing.T) {
	yes := []string{"RD-24", "EL-1", "RD(XF)-05", "RD(XF)-13", "ABCDE(A)-123"}
	no := []string{"WDZB1N-KYJY-4x2.5,JDG25-CC.WC", "SC20", "1:150", "8", "S+D", "rd-24", "RD-12345", "RD(XF)-"}
	for _, s := range yes {
		if !sheetNoRe.MatchString(s) {
			t.Errorf("sheetNoRe 应匹配 %q", s)
		}
	}
	for _, s := range no {
		if sheetNoRe.MatchString(s) {
			t.Errorf("sheetNoRe 不应匹配 %q", s)
		}
	}
}
func TestQuantileSheetBounds(t *testing.T) {
	// 主体点云 [0,100]² + 少量天文数字离群点 → 分位包围盒裁掉离群
	var prims []primitive
	for i := 0; i < 200; i++ {
		prims = append(prims, primitive{kind: 0, strokes: []stroke{
			{float64(i % 100), float64(i / 2), float64(i%100 + 1), float64(i/2 + 1)},
		}})
	}
	prims = append(prims,
		primitive{kind: 0, strokes: []stroke{{0, 0, 1e6, 1e6}}},
		primitive{kind: 0, strokes: []stroke{{0, 0, -1e6, -1e6}}},
	)
	b := quantileSheetBounds(prims)
	if b.maxX > 200 || b.maxY > 200 || b.minX < -100 || b.minY < -100 {
		t.Fatalf("分位包围盒应裁掉离群点: %v", b)
	}
	if b.maxX-b.minX <= 0 || b.maxY-b.minY <= 0 {
		t.Fatalf("分位包围盒退化: %v", b)
	}
	// 坐标全重合（零宽窗口）→ 回退旧口径仍有有限包围盒
	same := []primitive{{kind: 0, strokes: []stroke{{5, 5, 5, 5}, {5, 5, 5, 5}, {5, 5, 5, 5}}}}
	if b2 := quantileSheetBounds(same); b2.maxX <= b2.minX || b2.maxY <= b2.minY {
		t.Fatalf("零宽窗口应回退旧口径: %v", b2)
	}
	// 样本过少 → primitivesBounds
	few := []primitive{{kind: 0, strokes: []stroke{{0, 0, 10, 10}}}}
	if b3 := quantileSheetBounds(few); b3.minX != 0 || b3.maxX != 10 {
		t.Fatalf("小样本应回退 primitivesBounds: %v", b3)
	}
	// 主体点云 + 远处孤立标注簇（占比超过分位窗口裁不掉）→ 主分量
	// 收紧把孤立簇裁出视口
	var cl []primitive
	for i := 0; i < 300; i++ {
		cl = append(cl, primitive{kind: 0, strokes: []stroke{
			{float64(i % 20), float64(i / 3), float64(i%20 + 1), float64(i/3 + 1)},
		}})
	}
	for i := 0; i < 40; i++ { // 孤立簇 (y≈5000)：占比 40/340 > 0.5%，分位保留
		cl = append(cl, primitive{kind: 0, strokes: []stroke{
			{float64(i), 5000, float64(i + 1), 5001},
		}})
	}
	bc := quantileSheetBounds(cl)
	if bc.maxY > 1000 {
		t.Fatalf("主分量收紧应裁掉远处孤立簇: %v", bc)
	}
	if bc.maxX-bc.minX <= 0 || bc.maxY-bc.minY <= 0 {
		t.Fatalf("收紧后包围盒退化: %v", bc)
	}
}

func TestDetectSheetsFallbackQuantileBox(t *testing.T) {
	// 无图框 + 离群实体：兜底整图包围盒应贴合主体而非被离群撑爆
	doc := &Document{
		blocks:          map[uint64][]any{},
		attribs:         map[uint64]*entity.EntAttrib{},
		layerColors:     map[uint64]layerColor{},
		internalObjects: map[uint64]*objGeneric{},
	}
	for i := 0; i < 100; i++ {
		x := float64(i%10) * 100
		y := float64(i/10) * 80
		doc.modelSpace = append(doc.modelSpace, &entity.EntLine{Start: entity.Point3{x, y, 0}, End: entity.Point3{x + 60, y + 40, 0}})
	}
	doc.modelSpace = append(doc.modelSpace,
		&entity.EntLine{Start: entity.Point3{500, 500, 0}, End: entity.Point3{-1.4e6, 6.3e6, 0}})
	sheets := DetectSheets(doc)
	if len(sheets) != 1 || sheets[0].Name != "整图" {
		t.Fatalf("应兜底整图单张: %+v", sheets)
	}
	b := sheets[0].Box
	if math.Abs(b[0]) > 500 || math.Abs(b[1]) > 500 {
		t.Fatalf("兜底包围盒应贴合主体内容: %v", b)
	}
}

// ---- 残余内容兜底切分（补充区域） ----

// buildResidualDoc 构造"标准图框 + 框外文字页"文档：标准框
// (0,0)-(1189000,841000)（A 系比例 ×1000，含框内直属内容），文字页
// 位于 (pageX,pageY) 起。坐标取真实施工图量级（百万单位），残余聚类
// 的格计数/密度阈值均按该量级整定。
func buildResidualDoc(t *testing.T) *Document {
	t.Helper()
	doc := &Document{
		blocks:          map[uint64][]any{},
		attribs:         map[uint64]*entity.EntAttrib{},
		layerColors:     map[uint64]layerColor{},
		internalObjects: map[uint64]*objGeneric{},
	}
	doc.blocks[100] = []any{
		&entity.EntLwPolyline{Vertices: []entity.Point2{{0, 0}, {1189000, 0}, {1189000, 841000}, {0, 841000}, {0, 0}}},
	}
	doc.internalObjects[100] = &objGeneric{Name: "BLOCK_HEADER", Fields: []objField{{Key: "name", Val: "A1图框"}}}
	doc.modelSpace = append(doc.modelSpace,
		&entity.EntInsert{Position: entity.Point3{0, 0, 0}, Scale: entity.Point3{1, 1, 1}, BlockHeader: 100},
		&entity.EntLine{Start: entity.Point3{100000, 400000, 0}, End: entity.Point3{900000, 400000, 0}},
		&entity.EntLine{Start: entity.Point3{100000, 200000, 0}, End: entity.Point3{600000, 600000, 0}},
		&entity.EntLine{Start: entity.Point3{700000, 150000, 0}, End: entity.Point3{1000000, 150000, 0}},
		&entity.EntLine{Start: entity.Point3{150000, 100000, 0}, End: entity.Point3{400000, 300000, 0}},
	)
	return doc
}

func TestDetectSheetsResidualTextPage(t *testing.T) {
	// 标准图框 + 框外密集文字页（设计说明场景）：识别 1 标准 + 1 补充；
	// 补充名取区域最大字号文本，标题基线高于点云上缘（页顶框线端点
	// 少不在分量内）时经外扩框仍可命中
	doc := buildResidualDoc(t)
	for i := 0; i < 6; i++ {
		for j := 0; j < 50; j++ {
			doc.modelSpace = append(doc.modelSpace, &entity.EntText{
				Text:      fmt.Sprintf("说明条目%d-%d", i, j),
				Insertion: entity.Point3{2000000 + float64(j)*250, 1500000 + float64(i)*1000, 0},
				Height:    500,
			})
		}
	}
	// 框外零星散点（真实图纸的轴号/标注散布）：撑大残余包围盒确定
	// 聚类网格粒度，自身格计数低于下限不成图
	for _, d := range [][2]float64{{0, 0}, {4000000, 0}, {0, 3000000}, {4000000, 3000000}} {
		doc.modelSpace = append(doc.modelSpace, &entity.EntLine{
			Start: entity.Point3{d[0], d[1], 0}, End: entity.Point3{d[0] + 100, d[1] + 100, 0},
		})
	}
	doc.modelSpace = append(doc.modelSpace, &entity.EntText{
		Text: "弱电设计说明（一）", Insertion: entity.Point3{2012500, 1508500, 0}, Height: 600,
	})
	sheets := DetectSheets(doc)
	if len(sheets) != 2 {
		t.Fatalf("应识别 1 标准 + 1 补充，实际 %d：%+v", len(sheets), sheets)
	}
	if sheets[0].Name != "A1图框" {
		t.Fatalf("标准图框应在前: %+v", sheets)
	}
	if sheets[1].Name != "弱电设计说明（一）" {
		t.Fatalf("补充名应取区域最大字号文本: %q", sheets[1].Name)
	}
	b := sheets[1].Box
	if b[0] > 2000000 || b[2] < 2012000 || b[1] > 1500000 {
		t.Fatalf("补充包围盒应覆盖文字页: %v", b)
	}
}

func TestDetectSheetsResidualUnnamed(t *testing.T) {
	// 补充分量内无可读文本（纯符号）→ "补充区域 1"
	doc := buildResidualDoc(t)
	for i := 0; i < 3; i++ {
		for j := 0; j < 20; j++ {
			doc.modelSpace = append(doc.modelSpace, &entity.EntText{
				Text:      "※",
				Insertion: entity.Point3{2000000 + float64(j)*250, 1500000 + float64(i)*500, 0},
				Height:    100,
			})
		}
	}
	for _, d := range [][2]float64{{0, 0}, {4000000, 0}, {0, 3000000}, {4000000, 3000000}} {
		doc.modelSpace = append(doc.modelSpace, &entity.EntLine{
			Start: entity.Point3{d[0], d[1], 0}, End: entity.Point3{d[0] + 100, d[1] + 100, 0},
		})
	}
	sheets := DetectSheets(doc)
	if len(sheets) != 2 {
		t.Fatalf("应识别 1 标准 + 1 补充，实际 %d：%+v", len(sheets), sheets)
	}
	if sheets[1].Name != "补充区域 1" {
		t.Fatalf("无可读文本应回退补充区域序号: %q", sheets[1].Name)
	}
}

func TestGridDensityGapJump(t *testing.T) {
	// 白盒：相隔恰好 1 空格的达标格经跳格桥接连通（文字行跳格/双栏
	// 中缝），相隔 2 空格保持分离（页间距离）
	dots := func() [][2]float64 {
		var d [][2]float64
		for i := 0; i < 5; i++ { // A 群：格 0
			d = append(d, [2]float64{float64(i) * 1000, 0})
		}
		for i := 0; i < 5; i++ { // B 群：格 2（与 A 隔 1 空格）
			d = append(d, [2]float64{21000 + float64(i)*1000, 0})
		}
		for i := 0; i < 5; i++ { // C 群：格 5（与 B 隔 2 空格）
			d = append(d, [2]float64{51000 + float64(i)*1000, 0})
		}
		return d
	}()
	p := residualGridParams{res: 512, cell: 10000, cellMin: 5, gapJump: true, minDots: 10}
	comps := gridDensityComponents(dots, p)
	if len(comps) != 1 || comps[0].dots != 10 {
		t.Fatalf("隔 1 空格应连通（A+B=10 点），C 隔 2 空格被点数阈值滤除: %+v", comps)
	}
	p.gapJump = false
	comps = gridDensityComponents(dots, p)
	if len(comps) != 0 {
		t.Fatalf("无跳格桥接时 A/B 各 5 点低于阈值应全滤除: %+v", comps)
	}
}

func TestResidualSheetsOverlapDrop(t *testing.T) {
	// 白盒：环绕标准框的高密度标注带（分量包围盒与框大面积重叠）应被
	// 剔除；远离框的同样点云正常产出补充区域
	doc := &Document{
		blocks:          map[uint64][]any{},
		attribs:         map[uint64]*entity.EntAttrib{},
		layerColors:     map[uint64]layerColor{},
		internalObjects: map[uint64]*objGeneric{},
	}
	sd := newSheetDetector(doc)
	// 环绕框 (0,0,1000,700) 一圈的高密度点云（每格多点达 cellMin）
	var dots [][2]float64
	for x := -500.0; x <= 1500; x += 6 {
		dots = append(dots, [2]float64{x, 800}, [2]float64{x, -100})
	}
	for y := -100.0; y <= 800; y += 3 {
		dots = append(dots, [2]float64{-500, y}, [2]float64{1500, y})
	}
	sd.dots = dots
	if got := sd.residualSheets([]box2{{0, 0, 1000, 700}}); len(got) != 0 {
		t.Fatalf("环绕标注带应被重叠剔除，实际 %d：%+v", len(got), got)
	}
	// 同样点云远离标准框 → 产出 1 张补充区域（无可读文本用序号）
	shifted := make([][2]float64, len(dots))
	for i, d := range dots {
		shifted[i] = [2]float64{d[0] + 100000, d[1]}
	}
	sd.dots = shifted
	got := sd.residualSheets([]box2{{0, 0, 1000, 700}})
	if len(got) != 1 || got[0].Name != "补充区域 1" {
		t.Fatalf("远离框点云应产出 1 张补充区域: %+v", got)
	}
}

// ---- 渲染后清晰度自检 + 自动重渲 ----

// captureSheetStdout 替换告警输出流执行 fn，返回捕获的告警文本（测试
// 结束恢复默认 stdout，同包测试串行执行无竞争）。
func captureSheetStdout(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	old := sheetStdout
	sheetStdout = &buf
	defer func() { sheetStdout = old }()
	fn()
	return buf.String()
}

// buildMicroTextDoc 单页文档：视口 (0,0)-(w,h) 内一个直属 TEXT（字高
// hWorld），供清晰度自检/自动重渲用例精确控制达标率。
func buildMicroTextDoc(t *testing.T, w, h, hWorld float64) *Document {
	t.Helper()
	return &Document{
		blocks:          map[uint64][]any{},
		attribs:         map[uint64]*entity.EntAttrib{},
		layerColors:     map[uint64]layerColor{},
		internalObjects: map[uint64]*objGeneric{},
		modelSpace: []any{
			&entity.EntText{Text: "微缩标注", Insertion: entity.Point3{w / 4, h / 2, 0}, Height: hWorld},
		},
	}
}

func TestSheetTextPassRate(t *testing.T) {
	// 视口内 3 个文本（像素高 16/8/4 @scale 2）→ 达标 2/3
	mkLabel := func(hWorld float64) primitive {
		return primitive{kind: 1, lb: label{
			x: 10, y: 10, tx: &textInfo{lines: []string{"t"}, hWorld: hWorld, ux: 1, uy: 0},
		}}
	}
	prims := []primitive{mkLabel(8), mkLabel(4), mkLabel(2)}
	rate := sheetTextPassRate(prims, box2{0, 0, 100, 100}, 2)
	if math.Abs(rate-2.0/3.0) > 1e-9 {
		t.Fatalf("达标率应 2/3，实际 %v", rate)
	}
	// 视口外文本不计入
	rate = sheetTextPassRate(prims, box2{50, 50, 100, 100}, 2)
	if rate != 1 {
		t.Fatalf("视口外文本应不计入（无文本=达标），实际 %v", rate)
	}
	// 无文本图框视为达标
	rate = sheetTextPassRate(nil, box2{0, 0, 1, 1}, 1)
	if rate != 1 {
		t.Fatalf("无文本应达标 1，实际 %v", rate)
	}
}

func TestSheetPixelsOK(t *testing.T) {
	// 32768×333 扁长画布 ~1094 万像素：允许
	if !sheetPixelsOK(box2{0, 0, 5100, 52}, sheetHardMaxWidth) {
		t.Fatal("扁长视口 32768 宽应在内存上限内")
	}
	// 32768×12603 ≈ 4.13 亿像素：拒绝（内存防爆）
	if sheetPixelsOK(box2{0, 0, 2652, 1020}, sheetHardMaxWidth) {
		t.Fatal("竖版视口 32768 宽应超内存上限被拒绝")
	}
	// 16384×6301 ≈ 1.03 亿像素：允许
	if !sheetPixelsOK(box2{0, 0, 2652, 1020}, 16384) {
		t.Fatal("16384 宽应在内存上限内")
	}
}

func TestRenderSheetPNGAutoRerender(t *testing.T) {
	// 微缩标注（字高 2，自适应宽度被钳 16384 → 像素高 6.4px 不达标）→
	// 自动重渲到 32768 硬上限（扁长视口内存可容）→ 像素高 12.8px 达标，
	// 全程无告警
	doc := buildMicroTextDoc(t, 5000, 50, 2)
	sp := prepareSheets(doc)
	sheet := Sheet{Name: "微缩", Box: [4]float64{0, 0, 5000, 50}}
	var out []byte
	width, rate, rerenders := 0, 0.0, 0
	std := captureSheetStdout(t, func() {
		var err error
		out, width, rate, rerenders, err = sp.renderPNG(sheet, RenderOptions{})
		if err != nil {
			t.Fatalf("renderPNG: %v", err)
		}
	})
	if std != "" {
		t.Fatalf("达标后不应有告警: %q", std)
	}
	if width != sheetHardMaxWidth {
		t.Fatalf("应自动重渲到 %d 宽，实际 %d", sheetHardMaxWidth, width)
	}
	if rate < sheetPassRateMin {
		t.Fatalf("重渲后应达标，实际 %v", rate)
	}
	if rerenders != 1 {
		t.Fatalf("应恰好自动重渲 1 次（16384→32768），实际 %d", rerenders)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("PNG 解码: %v", err)
	}
	if img.Bounds().Dx() != sheetHardMaxWidth {
		t.Fatalf("PNG 实宽应 %d，实际 %d", sheetHardMaxWidth, img.Bounds().Dx())
	}
}

func TestRenderSheetPNGPhysicalLimitWarn(t *testing.T) {
	// 字高 0.2：32768 宽下像素高仅 ~1.3px，物理上不可能达标 → 顶到
	// 硬上限退出并打印告警行（stdout 可见，用户无需人工判断）
	doc := buildMicroTextDoc(t, 5000, 50, 0.2)
	sp := prepareSheets(doc)
	sheet := Sheet{Name: "极限", Box: [4]float64{0, 0, 5000, 50}}
	var out []byte
	width, rate, rerenders := 0, 0.0, 0
	std := captureSheetStdout(t, func() {
		var err error
		out, width, rate, rerenders, err = sp.renderPNG(sheet, RenderOptions{})
		if err != nil {
			t.Fatalf("renderPNG: %v", err)
		}
	})
	if !strings.Contains(std, "警告") || !strings.Contains(std, "物理极限") || !strings.Contains(std, "极限") {
		t.Fatalf("物理极限应打印告警行: %q", std)
	}
	if width != sheetHardMaxWidth || rate >= sheetPassRateMin {
		t.Fatalf("应停在 %d 宽且不达标，实际 %d/%v", sheetHardMaxWidth, width, rate)
	}
	if rerenders != 1 {
		t.Fatalf("物理极限前应重渲 1 次（16384→32768），实际 %d", rerenders)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("PNG 解码: %v", err)
	}
	if img.Bounds().Dx() != sheetHardMaxWidth {
		t.Fatalf("PNG 实宽应 %d，实际 %d", sheetHardMaxWidth, img.Bounds().Dx())
	}
}

func TestRenderSheetPNGMemoryGuard(t *testing.T) {
	// 横版视口（32768 档约 4.13 亿像素超内存上限）：翻倍重渲被拒绝，
	// 保留 16384 宽最优结果并注明内存受限，不 OOM
	doc := buildMicroTextDoc(t, 2600, 1000, 0.5)
	sp := prepareSheets(doc)
	sheet := Sheet{Name: "内存防爆", Box: [4]float64{0, 0, 2600, 1000}}
	var out []byte
	width, rate, rerenders := 0, 0.0, 0
	std := captureSheetStdout(t, func() {
		var err error
		out, width, rate, rerenders, err = sp.renderPNG(sheet, RenderOptions{})
		if err != nil {
			t.Fatalf("renderPNG: %v", err)
		}
	})
	if !strings.Contains(std, "内存上限") || !strings.Contains(strconv.Itoa(width), "16384") {
		t.Fatalf("内存受限应告警并保留 16384 宽: %q (width=%d)", std, width)
	}
	if width != 16384 || rate >= sheetPassRateMin {
		t.Fatalf("应保留 16384 宽且不达标: %d/%v", width, rate)
	}
	if rerenders != 0 {
		t.Fatalf("内存拒绝档未实际渲染，重渲次数应为 0，实际 %d", rerenders)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("PNG 解码: %v", err)
	}
	if img.Bounds().Dx() != 16384 {
		t.Fatalf("PNG 实宽应 16384，实际 %d", img.Bounds().Dx())
	}
}

func TestRenderSheetPNGExplicitWidthNoRerender(t *testing.T) {
	// 显式宽度是用户明确控制：达标率照常统计，但不自动重渲、不告警
	doc := buildMicroTextDoc(t, 5000, 50, 0.2)
	sp := prepareSheets(doc)
	sheet := Sheet{Name: "显式", Box: [4]float64{0, 0, 5000, 50}}
	var out []byte
	width, rate, rerenders := 0, 0.0, 0
	std := captureSheetStdout(t, func() {
		var err error
		out, width, rate, rerenders, err = sp.renderPNG(sheet, RenderOptions{Width: 1024})
		if err != nil {
			t.Fatalf("renderPNG: %v", err)
		}
	})
	if std != "" {
		t.Fatalf("显式宽度不应触发告警: %q", std)
	}
	if width != 1024 || rate >= sheetPassRateMin {
		t.Fatalf("显式宽度应保持 1024 且统计达标率: %d/%v", width, rate)
	}
	if rerenders != 0 {
		t.Fatalf("显式宽度不应自动重渲，实际 %d 次", rerenders)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("PNG 解码: %v", err)
	}
	if img.Bounds().Dx() != 1024 {
		t.Fatalf("显式宽度被改动: %d", img.Bounds().Dx())
	}
}

func TestRenderAllSheetsStats(t *testing.T) {
	// 批量结果携带每张最终宽度与清晰度达标率：标准图框张自适应 4096
	// 宽、标题文字 30 世界单位 → ~99px 达标 100%；SVG 张达标率未统计
	// 为 -1
	doc := buildSheetDoc(t, []struct {
		h        uint64
		name     string
		title    string
		offset   [2]float64
		w, hgt   float64
		scale    float64
		rotation float64
	}{
		aSeriesSheet(100, "A1图框", "配电系统图", [2]float64{0, 0}),
		aSeriesSheet(101, "A1图框", "照明平面图", [2]float64{1500, 0}),
	})
	std := captureSheetStdout(t, func() {})
	results, err := RenderAllSheets(doc, RenderOptions{}, "png")
	if err != nil {
		t.Fatalf("RenderAllSheets: %v", err)
	}
	if std != "" {
		t.Fatalf("常规图框不应告警: %q", std)
	}
	if len(results) != 3 {
		t.Fatalf("应输出 3 张，实际 %d", len(results))
	}
	for i, want := range []struct {
		name    string
		minRate float64
	}{
		{"整图全览", 0}, {"配电系统图", sheetPassRateMin}, {"照明平面图", sheetPassRateMin},
	} {
		if results[i].Name != want.name {
			t.Fatalf("第 %d 张应 %q，实际 %q", i, want.name, results[i].Name)
		}
		if results[i].Width < SheetDefaultWidth {
			t.Fatalf("%q 宽度应 ≥%d，实际 %d", want.name, SheetDefaultWidth, results[i].Width)
		}
		if results[i].PassRate < want.minRate {
			t.Fatalf("%q 达标率应 ≥%v，实际 %v", want.name, want.minRate, results[i].PassRate)
		}
	}
	// SVG：达标率未统计为 -1，宽度为名义值
	results, err = RenderAllSheets(doc, RenderOptions{}, "svg")
	if err != nil {
		t.Fatalf("SVG: %v", err)
	}
	for _, r := range results {
		if r.PassRate != -1 {
			t.Fatalf("SVG 达标率应未统计（-1），实际 %v", r.PassRate)
		}
		if r.Width <= 0 {
			t.Fatalf("SVG 名义宽度应 >0，实际 %d", r.Width)
		}
	}
}

// ---- 残余切分间距自适应（相对判据） ----

func TestBoxGap(t *testing.T) {
	cases := []struct {
		a, b box2
		want float64
	}{
		{box2{0, 0, 10, 10}, box2{5, 5, 15, 15}, 0},                    // 相交
		{box2{0, 0, 10, 10}, box2{20, 0, 30, 10}, 10},                  // x 分离
		{box2{0, 0, 10, 10}, box2{0, -5, 10, -2}, 2},                   // y 分离
		{box2{0, 0, 10, 10}, box2{20, 20, 30, 30}, 14.142135623730951}, // 对角
	}
	for i, c := range cases {
		if got := boxGap(c.a, c.b); math.Abs(got-c.want) > 1e-6 {
			t.Errorf("case %d boxGap=%v 期望 %v", i, got, c.want)
		}
	}
}

func TestMergeCloseComps(t *testing.T) {
	mk := func(box box2, dots int) residualComp {
		return residualComp{box: box, dots: dots}
	}
	// 间距 2000 ≥ 0.3×5000=1500：不同页保持拆分
	in := []residualComp{
		mk(box2{0, 0, 10000, 5000}, 10),
		mk(box2{12000, 0, 22000, 5000}, 20),
	}
	got := mergeCloseComps(in, sheetResidualGapFrac)
	if len(got) != 2 {
		t.Fatalf("大间距应保持拆分: %+v", got)
	}
	// 间距 1000 < 1500：同页粘连合并（点/计数/包围盒累加）
	in = []residualComp{
		mk(box2{0, 0, 10000, 5000}, 10),
		mk(box2{11000, 0, 21000, 5000}, 20),
	}
	got = mergeCloseComps(in, sheetResidualGapFrac)
	if len(got) != 1 || got[0].dots != 30 {
		t.Fatalf("小间距应合并: %+v", got)
	}
	if got[0].box.minX != 0 || got[0].box.maxX != 21000 || got[0].box.minY != 0 || got[0].box.maxY != 5000 {
		t.Fatalf("合并包围盒错误: %v", got[0].box)
	}
	// 链式传递合并：A-B 间距 250、B-C 间距 250 均 < 300，A-C 间距 500 ≥ 300
	// → 三者经并查集归并为一组
	in = []residualComp{
		mk(box2{0, 0, 1000, 1000}, 1),
		mk(box2{1250, 0, 2250, 1000}, 2),
		mk(box2{1500, 0, 2500, 1000}, 4),
	}
	got = mergeCloseComps(in, sheetResidualGapFrac)
	if len(got) != 1 || got[0].dots != 7 {
		t.Fatalf("链式相邻应归并一组: %+v", got)
	}
	// 单分量原样返回
	single := []residualComp{mk(box2{0, 0, 1, 1}, 5)}
	if got = mergeCloseComps(single, sheetResidualGapFrac); len(got) != 1 || got[0].dots != 5 {
		t.Fatalf("单分量应原样: %+v", got)
	}
}
