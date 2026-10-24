// api_pdf_hatch_test.go HATCH/SOLID 真填充的结构化测试：直接构造
// drawing.Primitive 喂给 pdfEmitter.emit 单测填充口径（比构造完整
// EntHatch 实体文档更小更可靠），覆盖顺序闭合（SOLID 四段常态）、乱序
// 段端点哈希贪心接合（单环/双环多边界）、开链接合失败回退描边、病态
// 段数超限回退、以及填充组先于线稿输出的层序约定。
package render

import (
	"bytes"
	"image/color"
	"testing"

	"github.com/unitedrhino/go-cad/internal/drawing"
	"github.com/unitedrhino/go-cad/internal/entity"
)

// newHatchTestEmitter 构造 100×100 世界单位视口的测试发射器：文档为空
// 结构（无图层表，实体色即最终色），不涉及文本路径（shaper 传 nil）。
// 背景固定白色（"1 1 1 rg ... re f"），与实体测试色（红/蓝）区分，
// 分组算子断言不歧义。
func newHatchTestEmitter() *pdfEmitter {
	return newPDFEmitter(
		drawing.Box2{MinX: 0, MinY: 0, MaxX: 100, MaxY: 100},
		2048, RenderOptions{Background: color.RGBA{R: 255, G: 255, B: 255, A: 255}},
		nil, &drawing.Document{},
	)
}

// redPrim 构造真彩红色（0xFF0000）的 stroke 图元——颜色经量化输出为
// "1 0 0"，可与背景/其他颜色组的算子明确区分，断言不歧义。
func redPrim(kind0 string, strokes []drawing.Stroke) drawing.Primitive {
	return drawing.Primitive{
		Kind: 0, Kind0: kind0, Strokes: strokes,
		Color: entity.EntColor{HasTrue: true, TrueColor: 0xFF0000},
	}
}

// quadStrokes 四边形 P0→P1→P2→P3→P0 的四段（SOLID 离散形态：末段
// 回到首点，顺序闭合快路径命中）。
func quadStrokes(x0, y0, x1, y1 float64) []drawing.Stroke {
	pts := [][2]float64{{x0, y0}, {x1, y0}, {x1, y1}, {x0, y1}}
	var s []drawing.Stroke
	for i := 0; i < 4; i++ {
		a, b := pts[i], pts[(i+1)%4]
		s = append(s, drawing.Stroke{X1: a[0], Y1: a[1], X2: b[0], Y2: b[1]})
	}
	return s
}

// TestPDFHatchFillSolid SOLID 顺序闭合四段 → 输出填充子路径（m + 4×l +
// h 闭合）而非描边：断言 hatch 颜色分组（rg）存在、h 闭合算子存在、
// 组尾统一 f 填充，且同色描边分组（RG）不出现。
func TestPDFHatchFillSolid(t *testing.T) {
	em := newHatchTestEmitter()
	em.emit(&drawing.Primitive{
		Kind: 0, Kind0: "SOLID", Strokes: quadStrokes(10, 10, 50, 40),
		Color: entity.EntColor{HasTrue: true, TrueColor: 0xFF0000},
	})
	c := em.finish()
	if !bytes.Contains(c, []byte("1 0 0 rg\n")) {
		t.Fatalf("缺少 SOLID 填充分组颜色头:\n%s", c)
	}
	if n := bytes.Count(c, []byte(" m\n")); n != 1 {
		t.Fatalf("填充子路径起点数 = %d, want 1:\n%s", n, c)
	}
	if n := bytes.Count(c, []byte(" l\n")); n != 4 {
		t.Fatalf("填充边数 = %d, want 4:\n%s", n, c)
	}
	if !bytes.Contains(c, []byte("h\nf\n")) {
		t.Fatalf("缺少闭合算子 h 与非零环绕填充 f:\n%s", c)
	}
	if bytes.Contains(c, []byte("RG")) {
		t.Fatalf("SOLID 应以填充替代描边，不应出现描边分组:\n%s", c)
	}
}

// TestPDFHatchFillHatchClosed HATCH 顺序闭合环（离散边界常态）同样走
// 填充口径（Kind0=HATCH 分支覆盖）。
func TestPDFHatchFillHatchClosed(t *testing.T) {
	em := newHatchTestEmitter()
	p := redPrim("HATCH", quadStrokes(5, 5, 80, 60))
	em.emit(&p)
	c := em.finish()
	if !bytes.Contains(c, []byte("h\nf\n")) || bytes.Contains(c, []byte("RG")) {
		t.Fatalf("HATCH 闭合环应输出填充而非描边:\n%s", c)
	}
}

// TestPDFHatchFillGreedyJoin 乱序/反向段走端点哈希贪心接合：单环 4 段
// 打乱方向后仍成环；双四边形 8 段交错打乱应接合为同一 path 的 2 个闭合
// 子路径后统一一次 f（多边界单图元场景）。
func TestPDFHatchFillGreedyJoin(t *testing.T) {
	em := newHatchTestEmitter()
	// 单环：四段全部反向 + 顺序打乱（首尾端点哈希后可串成环）
	shuffled := []drawing.Stroke{
		{X1: 50, Y1: 10, X2: 10, Y2: 10}, // 反向底边
		{X1: 50, Y1: 40, X2: 50, Y2: 10}, // 反向右边
		{X1: 10, Y1: 10, X2: 10, Y2: 40}, // 反向左边
		{X1: 10, Y1: 40, X2: 50, Y2: 40}, // 反向顶边
	}
	p := redPrim("HATCH", shuffled)
	em.emit(&p)
	c := em.finish()
	if !bytes.Contains(c, []byte("h\nf\n")) {
		t.Fatalf("乱序段应贪心接合成环填充:\n%s", c)
	}

	// 双环：两个四边形 8 段交错（顺序闭合快路径必失败，强制走贪心）
	em2 := newHatchTestEmitter()
	mixed := append(quadStrokes(0, 0, 20, 20), quadStrokes(40, 40, 60, 60)...)
	// 交错排列：偶数位取环 A 段、奇数位取环 B 段
	inter := make([]drawing.Stroke, 0, len(mixed))
	for i := 0; i < 4; i++ {
		inter = append(inter, mixed[i], mixed[i+4])
	}
	p2 := redPrim("HATCH", inter)
	em2.emit(&p2)
	c2 := em2.finish()
	if n := bytes.Count(c2, []byte("h\n")); n != 2 {
		t.Fatalf("双环应接合为 2 个闭合子路径, got %d:\n%s", n, c2)
	}
	if !bytes.Contains(c2, []byte("h\nf\n")) || bytes.Contains(c2, []byte("RG")) {
		t.Fatalf("双环应统一填充且不描边:\n%s", c2)
	}
}

// TestPDFHatchFillFallbackOpenChain 开链（L 形三段，首尾不接）接合失败
// → 回退现有描边口径：断言同色描边分组（RG + S）出现、填充分组（rg，
// 背景矩形除外）不出现，保证不丢几何。
func TestPDFHatchFillFallbackOpenChain(t *testing.T) {
	em := newHatchTestEmitter()
	open := []drawing.Stroke{
		{X1: 10, Y1: 10, X2: 40, Y2: 10},
		{X1: 40, Y1: 10, X2: 40, Y2: 30},
		{X1: 40, Y1: 30, X2: 25, Y2: 45}, // 首尾不回到 (10,10)：开链
	}
	p := redPrim("HATCH", open)
	em.emit(&p)
	c := em.finish()
	if !bytes.Contains(c, []byte("1 0 0 RG")) || !bytes.Contains(c, []byte("S\n")) {
		t.Fatalf("开链接合失败应回退描边:\n%s", c)
	}
	// 背景矩形是 "0 0 0 rg ... re f"（黑），红色 rg 只能来自填充分组
	if bytes.Contains(c, []byte("1 0 0 rg")) {
		t.Fatalf("开链不应产生填充路径:\n%s", c)
	}
}

// TestPDFHatchFillFallbackShort 段数不足 3（无法成环）直接回退描边。
func TestPDFHatchFillFallbackShort(t *testing.T) {
	em := newHatchTestEmitter()
	p := redPrim("SOLID", []drawing.Stroke{{X1: 0, Y1: 0, X2: 10, Y2: 0}, {X1: 10, Y1: 0, X2: 10, Y2: 10}})
	em.emit(&p)
	c := em.finish()
	if !bytes.Contains(c, []byte("1 0 0 RG")) {
		t.Fatalf("两段无法成环应回退描边:\n%s", c)
	}
	if bytes.Contains(c, []byte("1 0 0 rg")) {
		t.Fatalf("两段不应产生填充:\n%s", c)
	}
}

// TestPDFHatchFillSegLimit 单图元段数超 pdfHatchMaxSegs 时跳过接合直接
// 回退描边（病态数据防卡死防御）。
func TestPDFHatchFillSegLimit(t *testing.T) {
	em := newHatchTestEmitter()
	strokes := make([]drawing.Stroke, 0, pdfHatchMaxSegs+1)
	for i := 0; i <= pdfHatchMaxSegs; i++ {
		strokes = append(strokes, drawing.Stroke{X1: float64(i), Y1: 0, X2: float64(i) + 1, Y2: 0})
	}
	p := redPrim("HATCH", strokes)
	em.emit(&p)
	c := em.finish()
	if !bytes.Contains(c, []byte("1 0 0 RG")) {
		t.Fatalf("超限图元应回退描边:\n%s", c[:256])
	}
	if bytes.Contains(c, []byte("1 0 0 rg")) {
		t.Fatalf("超限图元不应产生填充:\n%s", c[:256])
	}
}

// TestPDFHatchFillLayerOrder 层序约定：HATCH 填充分组输出在线段描边
// 分组之前（填充在最底层，hatch 底色不盖线稿）——以两分组颜色头的
// 字节位置断言。
func TestPDFHatchFillLayerOrder(t *testing.T) {
	em := newHatchTestEmitter()
	solid := drawing.Primitive{
		Kind: 0, Kind0: "SOLID", Strokes: quadStrokes(10, 10, 60, 50),
		Color: entity.EntColor{HasTrue: true, TrueColor: 0xFF0000},
	}
	em.emit(&solid)
	line := drawing.Primitive{
		Kind: 0, Kind0: "LINE",
		Strokes: []drawing.Stroke{{X1: 0, Y1: 0, X2: 100, Y2: 100}},
		Color:   entity.EntColor{HasTrue: true, TrueColor: 0x0000FF},
	}
	em.emit(&line)
	c := em.finish()
	hatchAt := bytes.Index(c, []byte("1 0 0 rg\n"))
	strokeAt := bytes.Index(c, []byte("0 0 1 RG"))
	if hatchAt < 0 || strokeAt < 0 {
		t.Fatalf("填充/描边分组缺失:\n%s", c)
	}
	if hatchAt > strokeAt {
		t.Fatalf("填充分组(%d)应先于描边分组(%d)输出:\n%s", hatchAt, strokeAt, c)
	}
}

// TestPDFHatchFillNearCoincident 近重合端点（差值小于容差 = 对角线×
// 1e-6）应被量化视为同点接合成环——覆盖容差口径而非严格相等。
func TestPDFHatchFillNearCoincident(t *testing.T) {
	em := newHatchTestEmitter()
	// 对角线 ≈ 70，容差 ≈ 7e-5：末段终点与首段起点差 1e-6，量化同格
	eps := 1e-6
	strokes := []drawing.Stroke{
		{X1: 10, Y1: 10, X2: 50, Y2: 10},
		{X1: 50, Y1: 10, X2: 50, Y2: 60},
		{X1: 50, Y1: 60, X2: 10, Y2: 60},
		{X1: 10, Y1: 60, X2: 10 - eps, Y2: 10 + eps},
	}
	p := redPrim("SOLID", strokes)
	em.emit(&p)
	c := em.finish()
	if !bytes.Contains(c, []byte("h\nf\n")) {
		t.Fatalf("近重合端点应按容差接合成环填充:\n%s", c)
	}
}
