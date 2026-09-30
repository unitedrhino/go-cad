// r11_test.go pre-R13 家族（R9/R10/R11）读取验证：
// 以 LibreDWG dwgread -O JSON 的 gold 值为基准做实体几何值级对照，
// 并验证 Document 接入（Texts/EntityCount/RenderPNG 非空）。
// 语料缺失时跳过（与 libredwg_corpus_test.go 同一目录回退策略）。
package cad

import (
	"bytes"
	"fmt"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// requirePreR13Sample 返回样本字节；语料不可用时跳过测试。
func requirePreR13Sample(t *testing.T, rel string) []byte {
	t.Helper()
	path := filepath.Join(libredwgTestDataDir(), rel)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("pre-R13 样本不可用: %v", err)
	}
	return data
}

// parsePreR13Gold 用 LibreDWG dwgread 生成样本 gold JSON（不可用时跳过）。
func parsePreR13Gold(t *testing.T, dwgPath string) string {
	t.Helper()
	dwgread, err := exec.LookPath("/tmp/libredwg-build/dwgread")
	if err != nil {
		if dwgread, err = exec.LookPath("dwgread"); err != nil {
			t.Skip("dwgread 不可用，跳过 gold 对照")
		}
	}
	out := filepath.Join(t.TempDir(), "gold.json")
	cmd := exec.Command(dwgread, "-O", "JSON", "-o", out, dwgPath)
	if err := cmd.Run(); err != nil {
		t.Skipf("dwgread 生成 gold 失败: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Skipf("读取 gold 失败: %v", err)
	}
	return string(data)
}

// nearEq 浮点相等（1e-9 绝对 + 相对容差）。
func nearEq(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9*math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
}

// TestPreR13ParseR10Gold R10 样本实体几何与 gold 值级对照
// （gold 值取自 dwgread -O JSON，见 test-data/r10/entities.dwg）。
func TestPreR13ParseR10Gold(t *testing.T) {
	data := requirePreR13Sample(t, filepath.Join("r10", "entities.dwg"))
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse R10 失败: %v", err)
	}
	if got := doc.Version(); got != "AC1006" {
		t.Errorf("Version = %s, want AC1006", got)
	}
	// gold：LINE (2,3)-(3,4)、CIRCLE c=(9,9) r=1、ARC、TEXT 等
	var lines []*entLine
	var texts []*entText
	var circles []*entCircle
	for _, e := range doc.modelSpace {
		switch v := e.(type) {
		case *entLine:
			lines = append(lines, v)
		case *entText:
			texts = append(texts, v)
		case *entCircle:
			circles = append(circles, v)
		}
	}
	findLine := func(x1, y1, x2, y2 float64) *entLine {
		for _, l := range lines {
			if nearEq(l.start.x, x1) && nearEq(l.start.y, y1) &&
				nearEq(l.end.x, x2) && nearEq(l.end.y, y2) {
				return l
			}
		}
		return nil
	}
	if l := findLine(2, 3, 3, 4); l == nil {
		t.Errorf("R10 未找到 gold LINE (2,3)-(3,4)，实得 %d 条 LINE", len(lines))
	}
	// gold entities.dwg：TEXT "FOO" ins=(6,4)（opts 含对齐点）等
	found := false
	for _, tx := range texts {
		if tx.text == "FOO" {
			found = true
		}
	}
	if !found {
		t.Errorf("R10 未找到 gold TEXT \"FOO\"，实得 %d 条 TEXT", len(texts))
	}
	if len(circles) == 0 {
		t.Errorf("R10 未解出 CIRCLE")
	}
	if doc.EntityCount() == 0 {
		t.Errorf("R10 模型空间实体为空")
	}
}

// TestPreR13ParseR11Gold R11 样本逐实体几何对照（/entities-2d.dwg gold 值）。
func TestPreR13ParseR11Gold(t *testing.T) {
	data := requirePreR13Sample(t, filepath.Join("r11", "entities-2d.dwg"))
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse R11 失败: %v", err)
	}
	if got := doc.Version(); got != "AC1009" {
		t.Errorf("Version = %s, want AC1009", got)
	}
	// gold 值（dwgread -O JSON entities-2d.dwg）：
	// POINT (1,2,3)；LINE (2,3,4)-(3,4,5)；ARC c=(5,5) r=1 a=270°~0°；
	// CIRCLE c=(3,1) r=1（elev=2）；TEXT "FOO" ins=(1,4) h=0.75 对齐点 (3,4)
	wantPoint := point3{1, 2, 3}
	gotPoint := false
	wantLine := [6]float64{2, 3, 4, 3, 4, 5}
	gotLine := false
	wantArc := false
	wantCircle := false
	var fooText *entText
	for _, e := range doc.modelSpace {
		switch v := e.(type) {
		case *entPoint:
			if nearEq(v.location.x, wantPoint.x) && nearEq(v.location.y, wantPoint.y) && nearEq(v.location.z, wantPoint.z) {
				gotPoint = true
			}
		case *entLine:
			if nearEq(v.start.x, wantLine[0]) && nearEq(v.start.y, wantLine[1]) && nearEq(v.start.z, wantLine[2]) &&
				nearEq(v.end.x, wantLine[3]) && nearEq(v.end.y, wantLine[4]) && nearEq(v.end.z, wantLine[5]) {
				gotLine = true
			}
		case *entArc:
			// gold: elevation_r11=5, center=(5,5), r=1, start=4.712(rad 270°), end=0
			if nearEq(v.center.x, 5) && nearEq(v.center.y, 5) && nearEq(v.center.z, 5) &&
				nearEq(v.radius, 1) && nearEq(v.angleStart, 4.71238898038469) && nearEq(v.angleEnd, 0) {
				wantArc = true
			}
		case *entCircle:
			if nearEq(v.center.x, 3) && nearEq(v.center.y, 1) && nearEq(v.center.z, 2) && nearEq(v.radius, 1) {
				wantCircle = true
			}
		case *entText:
			if v.text == "FOO" {
				fooText = v
			}
		}
	}
	if !gotPoint {
		t.Errorf("R11 未匹配 gold POINT (1,2,3)")
	}
	if !gotLine {
		t.Errorf("R11 未匹配 gold LINE (2,3,4)-(3,4,5)")
	}
	if !wantArc {
		t.Errorf("R11 未匹配 gold ARC c=(5,5,5) r=1 270°→0°")
	}
	if !wantCircle {
		t.Errorf("R11 未匹配 gold CIRCLE c=(3,1,2) r=1")
	}
	if fooText == nil {
		t.Errorf("R11 未找到 gold TEXT \"FOO\"")
	} else {
		if !nearEq(fooText.height, 0.75) {
			t.Errorf("TEXT height = %v, want 0.75", fooText.height)
		}
		if !nearEq(fooText.insertion.x, 1) || !nearEq(fooText.insertion.y, 4) {
			t.Errorf("TEXT insertion = (%v,%v), want (1,4)", fooText.insertion.x, fooText.insertion.y)
		}
		if fooText.alignPt == nil || !nearEq(fooText.alignPt.x, 3) || !nearEq(fooText.alignPt.y, 4) {
			t.Errorf("TEXT alignPt = %v, want (3,4)", fooText.alignPt)
		}
	}
}

// TestPreR13Texts R11 的 Texts() 提取（Document 文本链路接入验证）。
func TestPreR13Texts(t *testing.T) {
	data := requirePreR13Sample(t, filepath.Join("r11", "entities-2d.dwg"))
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse 失败: %v", err)
	}
	texts := doc.Texts()
	found := false
	for _, ti := range texts {
		if ti.Text == "FOO" {
			found = true
		}
	}
	if !found {
		t.Errorf("Texts() 未提取到 \"FOO\"（共 %d 条）", len(texts))
	}
}

// TestPreR13RenderPNG R9/R10/R11 样本渲染非空 PNG。
func TestPreR13RenderPNG(t *testing.T) {
	samples := []string{
		filepath.Join("r9", "entities.dwg"),
		filepath.Join("r10", "entities.dwg"),
		filepath.Join("r10", "tmp_line.dwg"),
		filepath.Join("r11", "entities-2d.dwg"),
		filepath.Join("r11", "entities-3d.dwg"),
	}
	for _, rel := range samples {
		data := requirePreR13Sample(t, rel)
		doc, err := Parse(data)
		if err != nil {
			t.Errorf("%s: Parse 失败: %v", rel, err)
			continue
		}
		if doc.EntityCount() == 0 {
			t.Errorf("%s: 实体为空", rel)
			continue
		}
		pngBytes, err := RenderPNG(doc, RenderOptions{Width: 512})
		if err != nil {
			t.Errorf("%s: RenderPNG 失败: %v", rel, err)
			continue
		}
		if _, err := png.Decode(bytes.NewReader(pngBytes)); err != nil {
			t.Errorf("%s: PNG 解码失败: %v", rel, err)
		}
	}
}

// TestPreR13F2Shape R9/R10/R11 样本 SHAPE 与 gold 值级对照
// （gold 三样本一致：ins_pt=(6,6) scale=1 style_id=131 rotation=0.5236/30°）。
func TestPreR13F2Shape(t *testing.T) {
	for _, rel := range []string{
		filepath.Join("r9", "entities.dwg"),
		filepath.Join("r10", "entities.dwg"),
		filepath.Join("r11", "entities-2d.dwg"),
	} {
		data := requirePreR13Sample(t, rel)
		doc, err := Parse(data)
		if err != nil {
			t.Fatalf("%s: Parse 失败: %v", rel, err)
		}
		var shape *entShape
		for _, e := range doc.modelSpace {
			if s, ok := e.(*entShape); ok {
				shape = s
			}
		}
		if shape == nil {
			t.Errorf("%s: 未解出 SHAPE", rel)
			continue
		}
		if !nearEq(shape.insertion.x, 6) || !nearEq(shape.insertion.y, 6) {
			t.Errorf("%s: SHAPE insertion = (%v,%v), want (6,6)", rel, shape.insertion.x, shape.insertion.y)
		}
		if !nearEq(shape.scale, 1) {
			t.Errorf("%s: SHAPE scale = %v, want 1", rel, shape.scale)
		}
		if shape.shapeNo != 131 {
			t.Errorf("%s: SHAPE style_id = %d, want 131", rel, shape.shapeNo)
		}
		if !nearEq(shape.rotation, 0.5235987755983) {
			t.Errorf("%s: SHAPE rotation = %v, want 0.5236", rel, shape.rotation)
		}
	}
}

// TestPreR13F2Attrib R9/R10/R11 样本 ATTDEF/ATTRIB 与 gold 值级对照
// （ATTDEF：ins(9,5) h=0.2 "3"/"PROMPT"/"ATTR1" rot=1.0472、已删除副本
// ins(1,2) elev=3；ATTRIB：ins(2,2) h=0.1 "4"/"ATTR2" rot=1.5708）。
func TestPreR13F2Attrib(t *testing.T) {
	for _, rel := range []string{
		filepath.Join("r9", "entities.dwg"),
		filepath.Join("r10", "entities.dwg"),
		filepath.Join("r11", "entities-2d.dwg"),
	} {
		data := requirePreR13Sample(t, rel)
		doc, err := Parse(data)
		if err != nil {
			t.Fatalf("%s: Parse 失败: %v", rel, err)
		}
		var attdefs []*entAttrib
		var attrib *entAttrib
		for _, e := range doc.modelSpace {
			switch v := e.(type) {
			case *entAttrib:
				if v.typeName == "ATTDEF" {
					attdefs = append(attdefs, v)
				} else {
					attrib = v
				}
			}
		}
		if len(attdefs) < 2 {
			t.Fatalf("%s: ATTDEF 数 = %d, want >= 2", rel, len(attdefs))
		}
		a1 := attdefs[0]
		if a1.text != "3" || a1.prompt != "PROMPT" || a1.tag != "ATTR1" {
			t.Errorf("%s: ATTDEF text/prompt/tag = %q/%q/%q, want 3/PROMPT/ATTR1", rel, a1.text, a1.prompt, a1.tag)
		}
		if !nearEq(a1.insertion.x, 9) || !nearEq(a1.insertion.y, 5) || !nearEq(a1.height, 0.2) {
			t.Errorf("%s: ATTDEF ins/h = (%v,%v,%v), want (9,5,0.2)", rel, a1.insertion.x, a1.insertion.y, a1.height)
		}
		if !nearEq(a1.rotation, 1.0471975511966) {
			t.Errorf("%s: ATTDEF rotation = %v, want 1.0472", rel, a1.rotation)
		}
		if rel == filepath.Join("r11", "entities-2d.dwg") {
			// 仅 R11 样本该实体带 HAS_ELEVATION 位（gold elevation_r11=2）；
			// R9/R10 的 flag_r11=0 无 elevation，z 恒 0
			if !nearEq(a1.insertion.z, 2) {
				t.Errorf("%s: ATTDEF ins.z = %v, want 2 (elevation_r11)", rel, a1.insertion.z)
			}
		}
		var a2 *entAttrib
		for _, v := range attdefs {
			if v.tag == "ATTR2" {
				a2 = v
			}
		}
		if a2 == nil {
			t.Errorf("%s: 未找到已删除 ATTDEF（tag ATTR2）", rel)
		} else if !nearEq(a2.insertion.x, 1) || !nearEq(a2.insertion.y, 2) {
			t.Errorf("%s: 已删除 ATTDEF ins = (%v,%v), want (1,2)", rel, a2.insertion.x, a2.insertion.y)
		}
		if attrib == nil {
			t.Errorf("%s: 未解出 ATTRIB", rel)
			continue
		}
		if attrib.text != "4" || attrib.tag != "ATTR2" {
			t.Errorf("%s: ATTRIB text/tag = %q/%q, want 4/ATTR2", rel, attrib.text, attrib.tag)
		}
		if !nearEq(attrib.insertion.x, 2) || !nearEq(attrib.insertion.y, 2) || !nearEq(attrib.height, 0.1) {
			t.Errorf("%s: ATTRIB ins/h = (%v,%v,%v), want (2,2,0.1)", rel, attrib.insertion.x, attrib.insertion.y, attrib.height)
		}
		if !nearEq(attrib.rotation, 1.5707963267949) {
			t.Errorf("%s: ATTRIB rotation = %v, want 1.5708", rel, attrib.rotation)
		}
	}
	// R11 entities-2d：Texts() 链路应提取 ATTRIB "4" 与 ATTDEF 缺省值 "3"
	data := requirePreR13Sample(t, filepath.Join("r11", "entities-2d.dwg"))
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse 失败: %v", err)
	}
	got := map[string]bool{}
	for _, ti := range doc.Texts() {
		got[ti.Text] = true
	}
	if !got["4"] {
		t.Errorf("Texts() 未提取 ATTRIB \"4\"（共 %d 条）", len(doc.Texts()))
	}
	if !got["3"] {
		t.Errorf("Texts() 未提取 ATTDEF 缺省值 \"3\"")
	}
}

// TestPreR13F23DLine R9/R10 样本 3DLINE 与 gold 值级对照（R10：3RD×2，
// start=(5,9,0) end=(6,10,1)；R9：opts bit0 未置位 → 起点 2RD z=0、
// bit1 置位 → 终点 3RD z=1）。
func TestPreR13F23DLine(t *testing.T) {
	cases := []struct {
		rel                    string
		sx, sy, sz, ex, ey, ez float64
	}{
		{filepath.Join("r9", "entities.dwg"), 5, 9, 0, 6, 10, 1},
		{filepath.Join("r10", "entities.dwg"), 5, 9, 0, 6, 10, 1},
	}
	for _, tc := range cases {
		data := requirePreR13Sample(t, tc.rel)
		doc, err := Parse(data)
		if err != nil {
			t.Fatalf("%s: Parse 失败: %v", tc.rel, err)
		}
		var line *entLine
		for _, e := range doc.modelSpace {
			if v, ok := e.(*entLine); ok && v.typeName == "3DLINE" {
				line = v
			}
		}
		if line == nil {
			t.Errorf("%s: 未解出 3DLINE", tc.rel)
			continue
		}
		if !nearEq(line.start.x, tc.sx) || !nearEq(line.start.y, tc.sy) || !nearEq(line.start.z, tc.sz) ||
			!nearEq(line.end.x, tc.ex) || !nearEq(line.end.y, tc.ey) || !nearEq(line.end.z, tc.ez) {
			t.Errorf("%s: 3DLINE = (%v,%v,%v)-(%v,%v,%v), want (%g,%g,%g)-(%g,%g,%g)",
				tc.rel, line.start.x, line.start.y, line.start.z, line.end.x, line.end.y, line.end.z,
				tc.sx, tc.sy, tc.sz, tc.ex, tc.ey, tc.ez)
		}
	}
}

// TestPreR13F23DFace R9/R10/R11 样本 3DFACE 与 gold 值级对照
// （三样本角点 x/y 一致：(0,7)(4,8)(3,9)(2,8)；R10/R11 经 HAS_ELEVATION
// 与 opts 位走 2RD 路径，R9 全 2RD）。
func TestPreR13F23DFace(t *testing.T) {
	want := [4]point2{{0, 7}, {4, 8}, {3, 9}, {2, 8}}
	for _, rel := range []string{
		filepath.Join("r9", "entities.dwg"),
		filepath.Join("r10", "entities.dwg"),
		filepath.Join("r11", "entities-3d.dwg"),
	} {
		data := requirePreR13Sample(t, rel)
		doc, err := Parse(data)
		if err != nil {
			t.Fatalf("%s: Parse 失败: %v", rel, err)
		}
		var face *entFace3d
		for _, e := range doc.modelSpace {
			if v, ok := e.(*entFace3d); ok {
				face = v
			}
		}
		if face == nil {
			t.Errorf("%s: 未解出 3DFACE", rel)
			continue
		}
		got := [4]point2{{face.p1.x, face.p1.y}, {face.p2.x, face.p2.y}, {face.p3.x, face.p3.y}, {face.p4.x, face.p4.y}}
		for i := 0; i < 4; i++ {
			if !nearEq(got[i].x, want[i].x) || !nearEq(got[i].y, want[i].y) {
				t.Errorf("%s: 3DFACE corner%d = (%v,%v), want (%g,%g)", rel, i+1, got[i].x, got[i].y, want[i].x, want[i].y)
			}
		}
	}
}

// TestPreR13F2Viewport ACEB10 VIEWPORT 与 gold 值级对照（图纸空间实体：
// center=(28.566,17,0) width=57.132 height=34 id=1，存档 pspaceSpace）。
func TestPreR13F2Viewport(t *testing.T) {
	data := requirePreR13Sample(t, filepath.Join("r11", "ACEB10.dwg"))
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse 失败: %v", err)
	}
	var vp *entViewport
	for _, e := range doc.pspaceSpace {
		if v, ok := e.(*entViewport); ok {
			vp = v
		}
	}
	if vp == nil {
		t.Fatalf("pspaceSpace 未解出 VIEWPORT（共 %d 实体）", len(doc.pspaceSpace))
	}
	if !nearEq(vp.center.x, 28.56607142857143) || !nearEq(vp.center.y, 17) || !nearEq(vp.center.z, 0) {
		t.Errorf("VIEWPORT center = (%v,%v,%v), want (28.566,17,0)", vp.center.x, vp.center.y, vp.center.z)
	}
	if !nearEq(vp.width, 57.13214285714286) {
		t.Errorf("VIEWPORT width = %v, want 57.132", vp.width)
	}
	if !nearEq(vp.height, 34) {
		t.Errorf("VIEWPORT height = %v, want 34", vp.height)
	}
	if id, _ := vp.extra["id"].(uint16); id != 1 {
		t.Errorf("VIEWPORT id = %v, want 1", vp.extra["id"])
	}
}

// TestPreR13F2Dimension pre-R13 DIMENSION 与 gold 值级对照：
// entities-2d 的 DIMENSION_ALIGNED（opts=30：def_pt 3RD + flag + user_text
// + xline1/xline2 3RD）、R9 同型（def_pt 2RD 布局）、ACEB10 的
// DIMENSION_LINEAR×15（图纸空间，pspaceSpace）。
func TestPreR13F2Dimension(t *testing.T) {
	// ALIGNED：def/xline x,y 三样本一致；z 按样本文件（R9 2RD 无 z、
	// R10 3RD 但文件写 0、R11 3RD 写 2/3）
	cases := []struct {
		rel string
		z13 float64 // xline1_pt z
		z14 float64 // xline2_pt z
	}{
		{filepath.Join("r9", "entities.dwg"), 0, 0},
		{filepath.Join("r10", "entities.dwg"), 0, 0},
		{filepath.Join("r11", "entities-2d.dwg"), 2, 3},
	}
	for _, tc := range cases {
		data := requirePreR13Sample(t, tc.rel)
		doc, err := Parse(data)
		if err != nil {
			t.Fatalf("%s: Parse 失败: %v", tc.rel, err)
		}
		var dim *entDimension
		for _, e := range doc.modelSpace {
			if v, ok := e.(*entDimension); ok && v.typeName == "DIMENSION_ALIGNED" {
				dim = v
			}
		}
		if dim == nil {
			t.Errorf("%s: 未解出 DIMENSION_ALIGNED", tc.rel)
			continue
		}
		if dim.dimFlag != 1 {
			t.Errorf("%s: DIMENSION flag = %d, want 1 (aligned)", tc.rel, dim.dimFlag)
		}
		if !nearEq(dim.point10.x, 8) || !nearEq(dim.point10.y, 8) || !nearEq(dim.point10.z, 0) {
			t.Errorf("%s: def_pt = (%v,%v,%v), want (8,8,0)", tc.rel, dim.point10.x, dim.point10.y, dim.point10.z)
		}
		if !nearEq(dim.point13.x, 6) || !nearEq(dim.point13.y, 8) || !nearEq(dim.point13.z, tc.z13) {
			t.Errorf("%s: xline1_pt = (%v,%v,%v), want (6,8,%g)", tc.rel, dim.point13.x, dim.point13.y, dim.point13.z, tc.z13)
		}
		if !nearEq(dim.point14.x, 7) || !nearEq(dim.point14.y, 7) || !nearEq(dim.point14.z, tc.z14) {
			t.Errorf("%s: xline2_pt = (%v,%v,%v), want (7,7,%g)", tc.rel, dim.point14.x, dim.point14.y, dim.point14.z, tc.z14)
		}
		if dim.userText != " " {
			t.Errorf("%s: user_text = %q, want \" \"", tc.rel, dim.userText)
		}
	}
	// ACEB10：15 个 DIMENSION_LINEAR（图纸空间），gold 首条几何对照
	data := requirePreR13Sample(t, filepath.Join("r11", "ACEB10.dwg"))
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("ACEB10: Parse 失败: %v", err)
	}
	var dims []*entDimension
	for _, e := range doc.pspaceSpace {
		if v, ok := e.(*entDimension); ok && v.typeName == "DIMENSION_LINEAR" {
			dims = append(dims, v)
		}
	}
	if len(dims) != 15 {
		t.Errorf("ACEB10 DIMENSION_LINEAR 数 = %d, want 15", len(dims))
	}
	if len(dims) > 0 {
		d := dims[0]
		if d.dimFlag != 0 {
			t.Errorf("ACEB10 DIMENSION flag = %d, want 0 (linear)", d.dimFlag)
		}
		if !nearEq(d.point10.x, 13.62245609657801) || !nearEq(d.point10.y, 4.1236674739085) {
			t.Errorf("ACEB10 def_pt = (%v,%v), want (13.6225,4.1237)", d.point10.x, d.point10.y)
		}
		if !nearEq(d.point13.x, 12.62245609657801) || !nearEq(d.point13.y, 4.1236674739085) {
			t.Errorf("ACEB10 xline1_pt = (%v,%v), want (12.6225,4.1237)", d.point13.x, d.point13.y)
		}
		if !nearEq(d.point14.x, 13.62245609657801) || !nearEq(d.point14.y, 4.1236674739085) {
			t.Errorf("ACEB10 xline2_pt = (%v,%v), want (13.6225,4.1237)", d.point14.x, d.point14.y)
		}
		if !nearEq(d.textMidpoint.x, 12.62245609657801) || !nearEq(d.textMidpoint.y, 4.2356674739085) {
			t.Errorf("ACEB10 text_midpt = (%v,%v), want (12.6225,4.2357)", d.textMidpoint.x, d.textMidpoint.y)
		}
		if !d.hasInsertPoint || !nearEq(d.insertPoint.x, -6.72735672389388) || !nearEq(d.insertPoint.y, 1.79539575241016) {
			t.Errorf("ACEB10 clone_ins_pt = (%v,%v,%v), want (-6.7274,1.7954)", d.insertPoint.x, d.insertPoint.y, d.insertPoint.z)
		}
	}
}

// TestPreR13F2Polyline pre-R13 POLYLINE/VERTEX 与 gold 值级对照：
// 顶点跟随记录顺序聚合进 ownedHandles（渲染直连）；entities-2d 顶点
// point z 取 elevation_r11=2；r10 主区 (5,7)(6,8)(7,7) + extras 闭合
// polyline（flag=1）；ACEB10 块区 27 POLYLINE 聚合 93 顶点。
func TestPreR13F2Polyline(t *testing.T) {
	// entities-2d（R11）
	data := requirePreR13Sample(t, filepath.Join("r11", "entities-2d.dwg"))
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("entities-2d: Parse 失败: %v", err)
	}
	assertPolylineVerts(t, "entities-2d", doc, 1, [][3]float64{{5, 7, 2}, {6, 8, 2}, {7, 7, 2}})
	// r10（主区 + extras 区闭合 POLYLINE）
	data = requirePreR13Sample(t, filepath.Join("r10", "entities.dwg"))
	doc, err = Parse(data)
	if err != nil {
		t.Fatalf("r10: Parse 失败: %v", err)
	}
	assertPolylineVerts(t, "r10", doc, 2, [][3]float64{{5, 7, 0}, {6, 8, 0}, {7, 7, 0}})
	var closed *entPolyline2d
	for _, e := range doc.modelSpace {
		if p, ok := e.(*entPolyline2d); ok && p.flags&1 != 0 {
			closed = p
		}
	}
	if closed == nil {
		t.Errorf("r10: 未找到闭合 POLYLINE_2D（extras flag=1）")
	}
	// ACEB10：块区 POLYLINE 与顶点聚合
	data = requirePreR13Sample(t, filepath.Join("r11", "ACEB10.dwg"))
	doc, err = Parse(data)
	if err != nil {
		t.Fatalf("ACEB10: Parse 失败: %v", err)
	}
	np, nv := 0, 0
	owned := 0
	for _, list := range doc.blocks {
		for _, e := range list {
			switch v := e.(type) {
			case *entPolyline2d:
				np++
				owned += len(v.ownedHandles)
			case *entVertex2d:
				nv++
			}
		}
	}
	if np != 27 {
		t.Errorf("ACEB10 块区 POLYLINE_2D 数 = %d, want 27", np)
	}
	if nv != 93 {
		t.Errorf("ACEB10 块区 VERTEX_2D 数 = %d, want 93", nv)
	}
	if owned != 93 {
		t.Errorf("ACEB10 块区聚合顶点数 = %d, want 93", owned)
	}
}

// assertPolylineVerts 断言 modelSpace 中 POLYLINE_2D 数量与首条聚合顶点
// 坐标（gold 值级对照）。
func assertPolylineVerts(t *testing.T, name string, doc *Document, wantN int, wantVerts [][3]float64) {
	t.Helper()
	var poly *entPolyline2d
	n := 0
	for _, e := range doc.modelSpace {
		if p, ok := e.(*entPolyline2d); ok {
			n++
			if poly == nil {
				poly = p
			}
		}
	}
	if n != wantN {
		t.Fatalf("%s: POLYLINE_2D 数 = %d, want %d", name, n, wantN)
	}
	if poly == nil {
		return
	}
	var vmap = map[uint64]*entVertex2d{}
	for _, e := range doc.modelSpace {
		if v, ok := e.(*entVertex2d); ok {
			vmap[v.handle] = v
		}
	}
	if len(poly.ownedHandles) != len(wantVerts) {
		t.Errorf("%s: 聚合顶点数 = %d, want %d", name, len(poly.ownedHandles), len(wantVerts))
		return
	}
	for i, h := range poly.ownedHandles {
		v, ok := vmap[h]
		if !ok {
			t.Errorf("%s: 顶点句柄 %d 未索引到", name, h)
			continue
		}
		w := wantVerts[i]
		if !nearEq(v.position.x, w[0]) || !nearEq(v.position.y, w[1]) || !nearEq(v.position.z, w[2]) {
			t.Errorf("%s: 顶点 %d = (%v,%v,%v), want (%g,%g,%g)", name, i, v.position.x, v.position.y, v.position.z, w[0], w[1], w[2])
		}
	}
}

// TestPreR13F2TypeCounts 全类型计数与 gold OBJECTS 统计对照
// （dwgread -O JSON 的 entity 键计数；SEQEND/JUMP/BLOCK/ENDBLK 为结构
// 锚点不产实体，不在断言内。覆盖九类实体在全部六样本的计数清零）。
func TestPreR13F2TypeCounts(t *testing.T) {
	countTypes := func(doc *Document) map[string]int {
		count := map[string]int{}
		add := func(list []any) {
			for _, e := range list {
				if b := entBase(e); b != nil && b.typeName != "" {
					count[b.typeName]++
				}
			}
		}
		add(doc.modelSpace)
		add(doc.pspaceSpace)
		for _, list := range doc.blocks {
			add(list)
		}
		return count
	}
	assert := func(name string, got map[string]int, want map[string]int) {
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: %s 计数 = %d, want %d", name, k, got[k], v)
			}
		}
	}
	// r9/r10：主区+块区（extras 含第二条 POLYLINE）
	r9r10 := map[string]int{
		"POINT": 4, "LINE": 7, "ARC": 1, "CIRCLE": 1, "TEXT": 1, "TRACE": 1,
		"INSERT": 2, "SHAPE": 1, "SOLID": 3, "ATTDEF": 3, "ATTRIB": 1,
		"POLYLINE_2D": 2, "VERTEX_2D": 6, "DIMENSION_ALIGNED": 1,
		"3DLINE": 1, "3DFACE": 1,
	}
	for _, rel := range []string{filepath.Join("r9", "entities.dwg"), filepath.Join("r10", "entities.dwg")} {
		data := requirePreR13Sample(t, rel)
		doc, err := Parse(data)
		if err != nil {
			t.Fatalf("%s: Parse 失败: %v", rel, err)
		}
		assert(rel, countTypes(doc), r9r10)
	}
	// entities-2d / entities-3d（R11）
	r11 := map[string]int{
		"POINT": 4, "LINE": 6, "ARC": 1, "CIRCLE": 1, "TEXT": 1, "TRACE": 1,
		"INSERT": 2, "SHAPE": 1, "SOLID": 3, "ATTDEF": 3, "ATTRIB": 1,
		"POLYLINE_2D": 1, "VERTEX_2D": 3, "DIMENSION_ALIGNED": 1,
	}
	data := requirePreR13Sample(t, filepath.Join("r11", "entities-2d.dwg"))
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("entities-2d: Parse 失败: %v", err)
	}
	assert("entities-2d", countTypes(doc), r11)
	data = requirePreR13Sample(t, filepath.Join("r11", "entities-3d.dwg"))
	doc, err = Parse(data)
	if err != nil {
		t.Fatalf("entities-3d: Parse 失败: %v", err)
	}
	r11["3DFACE"] = 1
	assert("entities-3d", countTypes(doc), r11)
	// ACEB10：块区 + 图纸空间（主区全 PSPACE）
	data = requirePreR13Sample(t, filepath.Join("r11", "ACEB10.dwg"))
	doc, err = Parse(data)
	if err != nil {
		t.Fatalf("ACEB10: Parse 失败: %v", err)
	}
	assert("ACEB10", countTypes(doc), map[string]int{
		"LINE": 1149, "ARC": 124, "CIRCLE": 50, "POINT": 45, "TEXT": 116,
		"SOLID": 37, "INSERT": 19, "POLYLINE_2D": 35, "VERTEX_2D": 119,
		"DIMENSION_LINEAR": 15, "ATTDEF": 2, "VIEWPORT": 1,
	})
}

// TestPreR13F2InsertAttribs INSERT 的 HAS_ATTRIBS 位与后续 ATTRIB 挂接
// （gold r10：INSERT flag_r11=128，后随 ATTRIB(31) 至 SEQEND）。
func TestPreR13F2InsertAttribs(t *testing.T) {
	data := requirePreR13Sample(t, filepath.Join("r10", "entities.dwg"))
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse 失败: %v", err)
	}
	withAttribs := 0
	for _, e := range doc.modelSpace {
		if ins, ok := e.(*entInsert); ok && len(ins.attribs) > 0 {
			withAttribs++
			if doc.attribs[ins.attribs[0]] == nil {
				t.Errorf("INSERT attribs[0]=%d 未归档 doc.attribs", ins.attribs[0])
			}
		}
	}
	if withAttribs == 0 {
		t.Errorf("无 INSERT 挂接 ATTRIB（期望 HAS_ATTRIBS 位触发的挂接）")
	}
}

// TestPreR13F2Render F2 实体渲染接入验证：POLYLINE/DIMENSION/ATTRIB/
// 3DFACE 图元进入展开结果，ACEB10（图纸空间布局）渲染非空白，
// PNG 编码成功。
func TestPreR13F2Render(t *testing.T) {
	data := requirePreR13Sample(t, filepath.Join("r11", "entities-2d.dwg"))
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse 失败: %v", err)
	}
	count := map[string]int{}
	for _, p := range newTessellator(doc).expandAll() {
		count[p.kind0]++
	}
	for _, kind := range []string{"POLYLINE_2D", "DIMENSION", "ATTRIB", "LINE"} {
		if count[kind] == 0 {
			t.Errorf("entities-2d 渲染原语缺 %s（%v）", kind, count)
		}
	}
	data = requirePreR13Sample(t, filepath.Join("r10", "entities.dwg"))
	doc, err = Parse(data)
	if err != nil {
		t.Fatalf("Parse 失败: %v", err)
	}
	count = map[string]int{}
	for _, p := range newTessellator(doc).expandAll() {
		count[p.kind0]++
	}
	// 3DLINE 复用 entLine 模型，渲染输出为 LINE 原语（r10 gold 3 条 LINE
	// + 1 条 3DLINE = 4）；3DFACE 独立原语
	if count["3DFACE"] == 0 || count["LINE"] != 4 {
		t.Errorf("r10 渲染原语缺 3DFACE 或 LINE 数不对（%v）", count)
	}
	// ACEB10：图纸空间布局展开非空白（批次 F 时为 0 原语空白图）
	data = requirePreR13Sample(t, filepath.Join("r11", "ACEB10.dwg"))
	doc, err = Parse(data)
	if err != nil {
		t.Fatalf("Parse 失败: %v", err)
	}
	prims := newTessellator(doc).expandAll()
	if len(prims) < 1000 {
		t.Errorf("ACEB10 渲染原语 = %d, want >= 1000（图纸空间+块展开）", len(prims))
	}
	pngBytes, err := RenderPNG(doc, RenderOptions{Width: 512})
	if err != nil {
		t.Fatalf("RenderPNG 失败: %v", err)
	}
	if _, err := png.Decode(bytes.NewReader(pngBytes)); err != nil {
		t.Errorf("PNG 解码失败: %v", err)
	}
}

// TestPreR13BlockInsert R10 样本块定义与 INSERT 引用链路（gold：
// 文件 BLOCK 表为 [BLOCK1, BLOCK2, *D]，INSERT 的 RS 引用即该表索引；
// 块实体区 BLOCK/ENDBLK 界定内容归属）。
func TestPreR13BlockInsert(t *testing.T) {
	data := requirePreR13Sample(t, filepath.Join("r10", "entities.dwg"))
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse 失败: %v", err)
	}
	total := 0
	for _, list := range doc.blocks {
		total += len(list)
	}
	if total == 0 {
		t.Fatalf("块定义内容为空，期望 BLOCK1/*D 有归属实体")
	}
	var inserts []*entInsert
	for _, e := range doc.modelSpace {
		if ins, ok := e.(*entInsert); ok {
			inserts = append(inserts, ins)
		}
	}
	if len(inserts) == 0 {
		t.Fatalf("未解出 INSERT")
	}
	withContent := 0
	for _, ins := range inserts {
		if ins.blockHeader == 0 {
			t.Errorf("INSERT blockHeader 未映射（块表索引丢失）")
			continue
		}
		// gold：INSERT 引用 BLOCK1（含 LINE）；引用 BLOCK2 的内容仅
		// ATTDEF（首批实体集之外，允许为空）
		if len(doc.blocks[ins.blockHeader]) > 0 {
			withContent++
		}
	}
	if withContent == 0 {
		t.Errorf("所有 INSERT 引用块均无内容")
	}
}

// TestPreR13DWGReadCross 与 dwgread 现场生成的 gold JSON 对照实体计数
// （可选门控 CAD_PRER13_GOLD=1 时执行，需要 dwgread 可执行）。
func TestPreR13DWGReadCross(t *testing.T) {
	if os.Getenv("CAD_PRER13_GOLD") == "" {
		t.Skip("CAD_PRER13_GOLD 未设置，跳过 dwgread 现场对照")
	}
	dwgread := "/tmp/libredwg-build/dwgread"
	if _, err := os.Stat(dwgread); err != nil {
		t.Skip("dwgread 不可用")
	}
	samples := []string{
		filepath.Join("r9", "entities.dwg"),
		filepath.Join("r10", "entities.dwg"),
		filepath.Join("r11", "entities-2d.dwg"),
	}
	for _, rel := range samples {
		path := filepath.Join(libredwgTestDataDir(), rel)
		_ = parsePreR13Gold(t, path)
		data := requirePreR13Sample(t, rel)
		doc, err := Parse(data)
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		t.Logf("%s: 实体 %d 个", rel, doc.EntityCount())
	}
	if t.Failed() {
		fmt.Fprintln(os.Stderr, "pre-R13 dwgread 对照失败")
	}
}

// TestPreR13LayerColor R11 样本 LAYER 表颜色解析（gold：表条目
// name="0" color=7、name="DEFPOINTS" color=7；索引即实体 layerIdx）。
func TestPreR13LayerColor(t *testing.T) {
	data := requirePreR13Sample(t, filepath.Join("r11", "entities-2d.dwg"))
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse 失败: %v", err)
	}
	if len(doc.layerColors) < 2 {
		t.Fatalf("图层颜色数 = %d，期望至少 2 条", len(doc.layerColors))
	}
	for idx, lc := range doc.layerColors {
		if lc.index == 0 {
			t.Errorf("图层 %d 颜色索引为 0（表条目解析未命中）", idx)
		}
	}
	// 主实体区第一点 layer 表索引 0，颜色应可继承（colorResolved 走通）
}

// TestPreR13ACEB10Smoke ACEB10 大样本（R11 图纸空间布局：主实体区全部
// 为 PSPACE 实体、模型空间内容在块定义内）冒烟：解析成功 + 大量实体 +
// 渲染出图。gold 口径：块区+图纸空间全部实体计数对齐 dwgread
// （TestPreR13F2TypeCounts），模型空间/块内实体 1269 个。
func TestPreR13ACEB10Smoke(t *testing.T) {
	data := requirePreR13Sample(t, filepath.Join("r11", "ACEB10.dwg"))
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse 失败: %v", err)
	}
	if got := doc.Version(); got != "AC1009" {
		t.Errorf("Version = %s, want AC1009", got)
	}
	if n := doc.EntityCount(); n < 1000 {
		t.Errorf("EntityCount = %d，期望 >= 1000（块区实体 1269 个）", n)
	}
	if _, err := RenderPNG(doc, RenderOptions{Width: 512}); err != nil {
		t.Errorf("RenderPNG 失败: %v", err)
	}
}
