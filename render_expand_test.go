// render_expand_test.go INSERT 展开防爆机制回归测试：
// 覆盖环检测（自引用/互引用块短路）与全局展开预算量级（真实大图
// 完整展开不截断），以及负缩放（镜像）INSERT 的展开坐标正确性。
package cad

import (
	"github.com/unitedrhino/go-cad/internal/drawing"
	"github.com/unitedrhino/go-cad/internal/entity"
	"math"
	"os"
	"strings"
	"testing"
)

// buildLineBlockDoc 构造测试文档：块定义 h 含 n 条水平 LINE（局部 x 0..9），
// modelSpace 放 nIns 个 INSERT 引用该块（缩放 1、位置 (i*100, 0)）。
func buildLineBlockDoc(blockHandle uint64, nLines, nIns int) *Document {
	inner := make([]any, 0, nLines)
	for i := 0; i < nLines; i++ {
		inner = append(inner, &entity.EntLine{
			BaseEntity: entity.BaseEntity{},
			Start:      entity.Point3{X: float64(i), Y: 0, Z: 0},
			End:        entity.Point3{X: float64(i), Y: 9, Z: 0},
		})
	}
	doc := &Document{Blocks: map[uint64][]any{blockHandle: inner}}
	for i := 0; i < nIns; i++ {
		doc.ModelSpace = append(doc.ModelSpace, &entity.EntInsert{
			Position:    entity.Point3{X: float64(i * 100), Y: 0, Z: 0},
			Scale:       entity.Point3{X: 1, Y: 1, Z: 1},
			BlockHeader: blockHandle,
		})
	}
	return doc
}

// TestExpandAllCycleGuard 块引用环检测：自引用与互引用环短路展开、
// 非环内容照常产出（真实 DWG 块引用为 DAG，环只来自异常文件）。
func TestExpandAllCycleGuard(t *testing.T) {
	line := func(x1, x2 float64) *entity.EntLine {
		return &entity.EntLine{BaseEntity: entity.BaseEntity{}, Start: entity.Point3{X: x1, Y: 0, Z: 0}, End: entity.Point3{X: x2, Y: 0, Z: 0}}
	}
	// 自引用：块 A = LINE + 引用 A 的 INSERT
	selfIns := &entity.EntInsert{Scale: entity.Point3{X: 1, Y: 1, Z: 1}, BlockHeader: 0xA}
	docA := &Document{
		ModelSpace: []any{&entity.EntInsert{Scale: entity.Point3{X: 1, Y: 1, Z: 1}, BlockHeader: 0xA}},
		Blocks:     map[uint64][]any{0xA: {line(0, 5), selfIns}},
	}
	prims := drawing.NewTessellator(docA).ExpandAll()
	if len(prims) != 1 {
		t.Fatalf("自引用环应短路且保留非环 LINE，实际 %d 图元", len(prims))
	}
	// 互引用：块 A 引用 B，块 B 引用 A；模型空间引用 A
	docB := &Document{
		ModelSpace: []any{&entity.EntInsert{Scale: entity.Point3{X: 1, Y: 1, Z: 1}, BlockHeader: 0xA}},
		Blocks: map[uint64][]any{
			0xA: {line(0, 5), &entity.EntInsert{Scale: entity.Point3{X: 1, Y: 1, Z: 1}, BlockHeader: 0xB}},
			0xB: {line(10, 15), &entity.EntInsert{Scale: entity.Point3{X: 1, Y: 1, Z: 1}, BlockHeader: 0xA}},
		},
	}
	prims = drawing.NewTessellator(docB).ExpandAll()
	if len(prims) != 2 {
		t.Fatalf("互引用环应短路且保留两侧 LINE，实际 %d 图元", len(prims))
	}
	// 非环的同一块多次引用（DAG 分支）：块 B 被 A 引用 3 次，每次展开完整
	docC := &Document{
		ModelSpace: []any{
			&entity.EntInsert{Scale: entity.Point3{X: 1, Y: 1, Z: 1}, BlockHeader: 0xA},
		},
		Blocks: map[uint64][]any{
			0xA: {
				line(0, 1),
				&entity.EntInsert{Scale: entity.Point3{X: 1, Y: 1, Z: 1}, BlockHeader: 0xB},
				&entity.EntInsert{Scale: entity.Point3{X: 1, Y: 1, Z: 1}, BlockHeader: 0xB},
				&entity.EntInsert{Scale: entity.Point3{X: 1, Y: 1, Z: 1}, BlockHeader: 0xB},
			},
			0xB: {line(0, 2)},
		},
	}
	prims = drawing.NewTessellator(docC).ExpandAll()
	if len(prims) != 4 {
		t.Fatalf("DAG 同块多引用应全部展开（1+3），实际 %d 图元", len(prims))
	}
}

// TestExpandAllBudgetCoversLargeDAG 全局展开预算量级：无环大图展开
// 实例数超过旧预算（200000）时不得静默截断（RD-29 用户案例根因）。
// 6000 实例 INSERT × 每块 40 LINE = 240000 实例，全量展开应产出等量图元。
func TestExpandAllBudgetCoversLargeDAG(t *testing.T) {
	doc := buildLineBlockDoc(0x300, 40, 6000)
	prims := drawing.NewTessellator(doc).ExpandAll()
	if len(prims) != 40*6000 {
		t.Fatalf("大图完整展开：期望 %d 图元，实际 %d（预算截断）", 40*6000, len(prims))
	}
}

// TestInsertMirrorExpand 负 X 缩放（镜像）INSERT 展开：块内 LINE 沿局部
// +X，镜像后世界坐标 X 反向（RD-29 框 62 个 -0.7499 镜像块的特征路径）。
func TestInsertMirrorExpand(t *testing.T) {
	doc := &Document{
		ModelSpace: []any{&entity.EntInsert{
			Position:    entity.Point3{X: 100, Y: 0, Z: 0},
			Scale:       entity.Point3{X: -1, Y: 1, Z: 1},
			BlockHeader: 0xA,
		}},
		Blocks: map[uint64][]any{
			0xA: {&entity.EntLine{BaseEntity: entity.BaseEntity{}, Start: entity.Point3{X: 0, Y: 0, Z: 0}, End: entity.Point3{X: 10, Y: 0, Z: 0}}},
		},
	}
	prims := drawing.NewTessellator(doc).ExpandAll()
	if len(prims) != 1 {
		t.Fatalf("镜像 INSERT 应展开 1 条 LINE，实际 %d", len(prims))
	}
	s := prims[0].Strokes[0]
	// 局部 (0,0)→(100,0)、(10,0)→(90,0)：X 翻转、Y 不变
	if math.Abs(s.X1-100) > 1e-9 || math.Abs(s.Y1) > 1e-9 ||
		math.Abs(s.X2-90) > 1e-9 || math.Abs(s.Y2) > 1e-9 {
		t.Fatalf("镜像展开坐标错误: (%v,%v)-(%v,%v) 期望 (100,0)-(90,0)", s.X1, s.Y1, s.X2, s.Y2)
	}
}

// TestSheetsRD29Usercase 真实案例回归（外部文件缺失时跳过）：
// 智能化深化图纸 9 张 A1 图框全图展开，每框框内命中的展开图元
// 应达数千量级且含文字——修复前展开预算 200000 在展开中段耗尽，
// 后半模型空间（RD-29~RD-32）的 INSERT 内容被静默截断丢失。
func TestSheetsRD29Usercase(t *testing.T) {
	const path = "/tmp/usercase/case.dwg"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("用户案例缺失（%s）: %v", path, err)
	}
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("解析 %s: %v", path, err)
	}
	sheets := DetectSheets(doc)
	// LIM-L2 起残余内容兜底切分会把标准框之外的无框内容区（设计说明
	// 页/图例表/系统图小页）作为补充 Sheet 追加在标准框之后，标准框
	// 恒为清单前 9 张，展开质量回归只针对标准图框
	if len(sheets) < 9 {
		t.Fatalf("应至少识别 9 张标准图框，实际 %d", len(sheets))
	}
	for i, s := range sheets[:9] {
		if !strings.HasPrefix(s.Name, "RD-") {
			t.Fatalf("前 9 张应为标准图框（RD-*），第 %d 张 %q", i+1, s.Name)
		}
	}
	sheets = sheets[:9]
	sp := prepareSheets(doc)
	for _, s := range sheets {
		vp := sheetViewport(s.Box)
		stroke, label := 0, 0
		for i := range sp.prims {
			if !primInBox(&sp.prims[i], vp) {
				continue
			}
			if sp.prims[i].Kind == 0 {
				stroke++
			} else {
				label++
			}
		}
		// 修复前 RD-29 命中 44 stroke / 0 label；完整展开后数千以上
		if stroke < 1000 || label < 100 {
			t.Errorf("图框 %q 框内命中过少: stroke=%d label=%d（展开截断回归？）", s.Name, stroke, label)
		}
	}
	// RD-29（任务基准框）专项口径：图名按 "图号-图名" 规则命名，
	// 以图号 RD-29 前缀锚定（有图名为 "RD-29-九层弱电平面图"，
	// 无图名时恰为 "RD-29"）
	var rd29 *Sheet
	for i := range sheets {
		if sheets[i].Name == "RD-29" || strings.HasPrefix(sheets[i].Name, "RD-29-") {
			rd29 = &sheets[i]
		}
	}
	if rd29 == nil {
		t.Fatal("未识别到 RD-29 图框")
	}
	nIns := 0
	for _, ent := range doc.ModelSpace {
		if ins, ok := ent.(*entity.EntInsert); ok && pointInBox(ins.Position.X, ins.Position.Y,
			drawing.Box2{MinX: rd29.Box[0], MinY: rd29.Box[1], MaxX: rd29.Box[2], MaxY: rd29.Box[3]}) {
			nIns++
		}
	}
	if nIns < 200 {
		t.Errorf("RD-29 框内 INSERT 应约 201，实际 %d", nIns)
	}
}
