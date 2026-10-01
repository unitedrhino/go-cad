// sheet_texts_test.go 验证图纸文本收集与按图框归属（SheetTexts）：
// 构造含标准图幅图框块（闭合矩形框线 + 标题栏文本，满足图框识别的组合
// 判据）与直属/框外文本的文档，断言归属序号与导出字段；无图框场景断言
// 全部文本 Sheet=0。
package render

import (
	"math"
	"testing"

	"github.com/unitedrhino/go-cad/internal/drawing"
	"github.com/unitedrhino/go-cad/internal/entity"
)

// buildSheetTextDoc 构造测试文档：一张 A 系比例（42000×29700，宽高比 1.414
// 落在标准图幅窗口）的图框块（闭合 LWPOLYLINE 矩形框线 + 标题文本，块内
// 实体数与框线判据满足 DetectSheets 组合筛选），模型空间挂图框 INSERT、
// 框内直属文本/线段与一条框外文本。图框内直属代表点 9 个（3 条 LINE 端点
// 6 + 文本插入点 3）≥ 图框内容密度下限 sheetMinContentDots。
func buildSheetTextDoc(t *testing.T) *drawing.Document {
	t.Helper()
	const frameHeader = 100
	lcWater := uint64(201)
	lcNote := uint64(202)
	doc := &drawing.Document{
		Ver:        0,
		ModelSpace: []any{},
		Blocks:     map[uint64][]any{},
		Attribs:    map[uint64]*entity.EntAttrib{},
		LayerColors: map[uint64]drawing.LayerColor{
			lcWater: {Index: 1, Name: "WATER"},
			lcNote:  {Index: 3, Name: "NOTE"},
		},
	}
	// 图框块定义：闭合矩形框线（顶点级框线判据）+ 标题栏文本（右下角
	// 标题栏区内，供图名提取；不超出框右缘防撑高块包围盒破坏框线判定）
	doc.Blocks[frameHeader] = []any{
		&entity.EntLwPolyline{
			BaseEntity: entity.BaseEntity{Handle: 1, Layer: lcNote, Mode: 0},
			Vertices: []entity.Point2{
				{X: 0, Y: 0}, {X: 42000, Y: 0}, {X: 42000, Y: 29700}, {X: 0, Y: 29700},
			},
		},
		&entity.EntText{
			BaseEntity: entity.BaseEntity{Handle: 2, Layer: lcNote, Mode: 0},
			Text:       "测试图", Insertion: entity.Point3{X: 40000, Y: 1500}, Height: 500,
		},
	}
	text := func(handle uint64, layer uint64, s string, x, y, h, rotDeg float64) *entity.EntText {
		return &entity.EntText{
			BaseEntity: entity.BaseEntity{Handle: handle, Layer: layer, Mode: 2},
			Text:       s,
			Insertion:  entity.Point3{X: x, Y: y},
			Height:     h,
			Rotation:   rotDeg * math.Pi / 180,
		}
	}
	line := func(handle uint64, x1, y1, x2, y2 float64) *entity.EntLine {
		return &entity.EntLine{
			BaseEntity: entity.BaseEntity{Handle: handle, Layer: lcNote, Mode: 2},
			Start:      entity.Point3{X: x1, Y: y1},
			End:        entity.Point3{X: x2, Y: y2},
		}
	}
	doc.ModelSpace = []any{
		&entity.EntInsert{
			BaseEntity:  entity.BaseEntity{Handle: 10, Layer: lcNote, Mode: 2},
			Position:    entity.Point3{},
			Scale:       entity.Point3{X: 1, Y: 1, Z: 1},
			BlockHeader: frameHeader,
		},
		text(11, lcWater, "给排水说明", 5000, 15000, 350, 0),
		text(12, lcWater, "旋转文本", 10000, 10000, 350, 45),
		&entity.EntMText{
			BaseEntity: entity.BaseEntity{Handle: 13, Layer: lcNote, Mode: 2},
			Text:       "多行\\P文本", Insertion: entity.Point3{X: 15000, Y: 5000},
			TextHeight: 300,
		},
		line(14, 2000, 2000, 8000, 2000),
		line(15, 2000, 3000, 8000, 3000),
		line(16, 2000, 4000, 8000, 4000),
		text(17, lcNote, "框外文本", 100000, 100000, 350, 0),
	}
	return doc
}

// approx 近似相等断言（浮点口径）。
func approx(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("%s = %v, 期望 %v", label, got, want)
	}
}

// TestSheetTextsSheetAssign 带图框文档：全部 5 条文本（块内标题经 INSERT
// 展开 + 框内直属 + 框外）被收集，归属序号与导出字段逐条断言。
func TestSheetTextsSheetAssign(t *testing.T) {
	doc := buildSheetTextDoc(t)
	sheets := DetectSheets(doc)
	if len(sheets) != 1 {
		t.Fatalf("图框识别数 = %d, 期望 1（框名 %q）", len(sheets), sheetNames(sheets))
	}
	got := SheetTexts(doc, sheets)
	byText := map[string]SheetText{}
	for _, st := range got {
		byText[st.Text] = st
	}
	if len(got) != 5 {
		t.Fatalf("文本收集数 = %d, 期望 5（含块内标题）: %v", len(got), textsOf(got))
	}
	// 块内标题文本经 INSERT 展开归属图框
	title, ok := byText["测试图"]
	if !ok {
		t.Fatalf("块内标题文本未收集: %v", textsOf(got))
	}
	if title.Sheet != 1 {
		t.Fatalf("块内标题归属 = %d, 期望 1", title.Sheet)
	}
	approx(t, "标题 X", title.X, 40000)
	approx(t, "标题 Y", title.Y, 1500)
	approx(t, "标题 Height", title.Height, 500)
	// 框内直属文本：归属与字段（旋转 45° 输出为度、层名来自层表）
	in1 := byText["给排水说明"]
	if in1.Sheet != 1 {
		t.Fatalf("框内文本归属 = %d, 期望 1", in1.Sheet)
	}
	approx(t, "X", in1.X, 5000)
	approx(t, "Y", in1.Y, 15000)
	approx(t, "Height", in1.Height, 350)
	if in1.Layer != "WATER" {
		t.Fatalf("层名 = %q, 期望 WATER", in1.Layer)
	}
	approx(t, "Rotation", in1.Rotation, 0)
	rot := byText["旋转文本"]
	approx(t, "Rotation", rot.Rotation, 45)
	if rot.Sheet != 1 {
		t.Fatalf("旋转文本归属 = %d, 期望 1", rot.Sheet)
	}
	// MTEXT 剥离格式码后收集（\P → \n），归属图框
	mt := byText["多行\n文本"]
	if mt.Sheet != 1 || mt.Layer != "NOTE" {
		t.Fatalf("MTEXT 归属/层名 = %d/%q, 期望 1/NOTE", mt.Sheet, mt.Layer)
	}
	approx(t, "MTEXT Height", mt.Height, 300)
	// 框外文本：Sheet=0
	out := byText["框外文本"]
	if out.Sheet != 0 {
		t.Fatalf("框外文本归属 = %d, 期望 0", out.Sheet)
	}
}

// TestSheetTextsNoSheets 无图框（sheets 空）：全部文本 Sheet=0，字段照常
// 导出（调用方剔除兜底整图框后的归属口径）。
func TestSheetTextsNoSheets(t *testing.T) {
	doc := buildSheetTextDoc(t)
	got := SheetTexts(doc, nil)
	if len(got) != 5 {
		t.Fatalf("文本收集数 = %d, 期望 5", len(got))
	}
	for _, st := range got {
		if st.Sheet != 0 {
			t.Fatalf("无图框时 %q 归属 = %d, 期望 0", st.Text, st.Sheet)
		}
	}
}

// TestSheetTextsNilDoc 空文档防御。
func TestSheetTextsNilDoc(t *testing.T) {
	if got := SheetTexts(nil, nil); got != nil {
		t.Fatalf("nil 文档应返回 nil, 得到 %v", got)
	}
}

// sheetNames 图框名列表（错误信息用）。
func sheetNames(sheets []Sheet) []string {
	var out []string
	for _, s := range sheets {
		out = append(out, s.Name)
	}
	return out
}

// textsOf 文本清单摘要（错误信息用）。
func textsOf(list []SheetText) []string {
	var out []string
	for _, s := range list {
		out = append(out, s.Text)
	}
	return out
}
