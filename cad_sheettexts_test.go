// cad_sheettexts_test.go 验证门面 cad.SheetTexts 的图框归属与无图框兜底
// 口径：带图框文档归属 1-based 图框序号；无图框文档（DetectSheets 兜底
// "整图"单张）全部文本 Sheet=0。
package cad

import (
	"testing"

	"github.com/unitedrhino/go-cad/internal/drawing"
	"github.com/unitedrhino/go-cad/internal/entity"
)

// buildFacadeDoc 构造最小图框文档：A 系比例闭合矩形框线块（42000×29700，
// 落在标准图幅窗口）+ 框内/框外直属文本与足量框内代表点（满足图框内容
// 密度判据），字段构造口径与 render 包归属测试一致。
func buildFacadeDoc() *Document {
	const frameHeader = 100
	doc := &Document{
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

// TestSheetTextsFacadeWithFrame 带图框：框内文本（含块内标题）归属 1，
// 框外归属 0。
func TestSheetTextsFacadeWithFrame(t *testing.T) {
	got := SheetTexts(buildFacadeDoc())
	sheet := map[string]int{}
	for _, st := range got {
		sheet[st.Text] = st.Sheet
	}
	if len(got) != 5 {
		t.Fatalf("文本收集数 = %d, 期望 5", len(got))
	}
	if sheet["框内文本"] != 1 || sheet["测试图"] != 1 {
		t.Fatalf("框内归属错误: %v", sheet)
	}
	if sheet["框外文本"] != 0 {
		t.Fatalf("框外文本归属 = %d, 期望 0", sheet["框外文本"])
	}
}

// TestSheetTextsFacadeNoFrame 无图框：DetectSheets 兜底"整图"单张被门面
// 剔除，全部文本 Sheet=0。
func TestSheetTextsFacadeNoFrame(t *testing.T) {
	doc := buildFacadeDoc()
	// 去掉图框 INSERT 与框外文本，仅留直属文本 → 无图框候选走兜底
	doc.ModelSpace = doc.ModelSpace[1:4]
	got := SheetTexts(doc)
	if len(got) != 3 {
		t.Fatalf("文本收集数 = %d, 期望 3", len(got))
	}
	for _, st := range got {
		if st.Sheet != 0 {
			t.Fatalf("无图框时 %q 归属 = %d, 期望 0", st.Text, st.Sheet)
		}
	}
}
