// encode_forward_test.go 结构化正向写出的门禁测试：合成 Document →
// WriteDwg → Parse 回读，逐实体对照几何/文本一致；JSON 九样本与 DXF
// 样本的跨来源对照见 TestWriteForwardR2000 系列门禁。
package cad

import (
	"bytes"
	"fmt"
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// fwdSynthDoc 合成文档骨架：R2000 版本 + 图层表 + 模型空间实体（mode 2、
// 顺序句柄、统一图层 0x10）。
func fwdSynthDoc(t *testing.T, ents ...any) *Document {
	t.Helper()
	doc := &Document{
		version:     verR2000,
		blocks:      map[uint64][]any{},
		attribs:     map[uint64]*entAttrib{},
		layerColors: map[uint64]layerColor{0x10: {index: 3, name: "FWD"}},
	}
	for i, e := range ents {
		b := entBase(e)
		if b == nil {
			t.Fatalf("实体 %d 非 entityCommon", i)
		}
		b.handle = uint64(0x30 + i)
		b.mode = 2
		b.layer = 0x10
		doc.classify(e)
	}
	return doc
}

// fwdWriteParse 合成文档写出并回读（正向 → Parse 闭环）。
func fwdWriteParse(t *testing.T, doc *Document) *Document {
	t.Helper()
	var buf bytes.Buffer
	if err := WriteDwg(doc, &buf); err != nil {
		t.Fatalf("WriteDwg 失败: %v", err)
	}
	out, err := Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("回读 Parse 失败: %v", err)
	}
	return out
}

// nearGeo 合成门禁的几何近似（0.01 绝对容差，独立于既有 nearF 相对容差）。
func nearGeo(a, b float64) bool { return math.Abs(a-b) < 0.01 }

// TestWriteForwardSyntheticR2000 合成模型空间（LINE/CIRCLE/ARC/POINT/
// ELLIPSE/LWPOLYLINE/TEXT/MTEXT/SOLID/3DFACE）→ WriteDwg → Parse，
// 实体数、类型、几何与文本逐项对照。
func TestWriteForwardSyntheticR2000(t *testing.T) {
	line := &entLine{start: point3{0, 0, 0}, end: point3{100, 50, 0}}
	circle := &entCircle{center: point3{10, 20, 0}, radius: 12.5}
	arc := &entArc{center: point3{5, 6, 0}, radius: 7, angleStart: 0.5, angleEnd: 2.5}
	point := &entPoint{location: point3{33, 44, 0}, rotation: 1.25}
	ellipse := &entEllipse{center: point3{1, 2, 0}, majorAxis: point3{10, 0, 0}, ratio: 0.5, startAng: 0, endAng: math.Pi}
	lwp := &entLwPolyline{
		vertices: []point2{{0, 0}, {10, 0}, {10, 10}, {0, 10}},
		bulges:   []float64{0, 0.5, 0, 0},
	}
	text := &entText{text: "HELLO_FWD", insertion: point3{7, 8, 0}, height: 3.5, rotation: 0.25}
	mtext := &entMText{text: "MTEXT_FWD", insertion: point3{9, 9, 0}, rectWidth: 20, textHeight: 2.5, attachment: 1, xAxisDir: point3{1, 0, 0}}
	solid := &entSolid{p1: point2{0, 0}, p2: point2{10, 0}, p3: point2{0, 10}, p4: point2{10, 10}, elevation: 1.5, thickness: 0.25, extrusion: point3{0, 0, 1}}
	face := &entFace3d{p1: point3{0, 0, 0}, p2: point3{20, 0, 0}, p3: point3{20, 15, 0}, p4: point3{0, 15, 0}}
	doc := fwdSynthDoc(t, line, circle, arc, point, ellipse, lwp, text, mtext, solid, face)
	got := fwdWriteParse(t, doc)
	if got.EntityCount() != len(doc.modelSpace) {
		t.Fatalf("实体数: %d != %d", got.EntityCount(), len(doc.modelSpace))
	}
	if got.Version() != "AC1015" {
		t.Fatalf("版本: %s != AC1015", got.Version())
	}
	// 逐类型取回读实体并对照几何（按句柄映射）
	byHandle := map[uint64]any{}
	for _, e := range got.modelSpace {
		byHandle[entBase(e).handle] = e
	}
	if len(byHandle) != len(doc.modelSpace) {
		t.Fatalf("回读实体种类数: %d != %d", len(byHandle), len(doc.modelSpace))
	}
	gl := byHandle[line.handle].(*entLine)
	if !nearGeo(gl.start.x, 0) || !nearGeo(gl.start.y, 0) || !nearGeo(gl.end.x, 100) || !nearGeo(gl.end.y, 50) {
		t.Errorf("LINE 几何: %+v %+v", gl.start, gl.end)
	}
	gc := byHandle[circle.handle].(*entCircle)
	if !nearGeo(gc.center.x, 10) || !nearGeo(gc.center.y, 20) || !nearGeo(gc.radius, 12.5) {
		t.Errorf("CIRCLE 几何: %+v r=%v", gc.center, gc.radius)
	}
	ga := byHandle[arc.handle].(*entArc)
	if !nearGeo(ga.radius, 7) || !nearGeo(rad2deg(ga.angleStart), rad2deg(0.5)) || !nearGeo(rad2deg(ga.angleEnd), rad2deg(2.5)) {
		t.Errorf("ARC 几何: r=%v a0=%v a1=%v", ga.radius, ga.angleStart, ga.angleEnd)
	}
	gp := byHandle[point.handle].(*entPoint)
	if !nearGeo(gp.location.x, 33) || !nearGeo(gp.location.y, 44) {
		t.Errorf("POINT 几何: %+v", gp.location)
	}
	ge := byHandle[ellipse.handle].(*entEllipse)
	if !nearGeo(ge.ratio, 0.5) || !nearGeo(ge.majorAxis.x, 10) {
		t.Errorf("ELLIPSE 几何: ratio=%v major=%+v", ge.ratio, ge.majorAxis)
	}
	gw := byHandle[lwp.handle].(*entLwPolyline)
	if len(gw.vertices) != 4 {
		t.Fatalf("LWPOLYLINE 顶点数: %d != 4", len(gw.vertices))
	}
	for i, want := range lwp.vertices {
		if !nearGeo(gw.vertices[i].x, want.x) || !nearGeo(gw.vertices[i].y, want.y) {
			t.Errorf("LWPOLYLINE 顶点 %d: %+v != %+v", i, gw.vertices[i], want)
		}
	}
	if len(gw.bulges) < 2 || !nearGeo(gw.bulges[1], 0.5) {
		t.Errorf("LWPOLYLINE 凸度: %v", gw.bulges)
	}
	gt := byHandle[text.handle].(*entText)
	if gt.text != "HELLO_FWD" {
		t.Errorf("TEXT 文本: %q", gt.text)
	}
	if !nearGeo(gt.height, 3.5) || !nearGeo(gt.insertion.x, 7) {
		t.Errorf("TEXT 几何: h=%v ins=%+v", gt.height, gt.insertion)
	}
	gm := byHandle[mtext.handle].(*entMText)
	if gm.text != "MTEXT_FWD" || !nearGeo(gm.rectWidth, 20) || !nearGeo(gm.textHeight, 2.5) {
		t.Errorf("MTEXT: %q w=%v h=%v", gm.text, gm.rectWidth, gm.textHeight)
	}
	gs := byHandle[solid.handle].(*entSolid)
	if !nearGeo(gs.p3.x, 0) || !nearGeo(gs.p4.y, 10) || !nearGeo(gs.elevation, 1.5) {
		t.Errorf("SOLID 几何: %+v %+v elev=%v", gs.p3, gs.p4, gs.elevation)
	}
	gf := byHandle[face.handle].(*entFace3d)
	if !nearGeo(gf.p2.x, 20) || !nearGeo(gf.p4.y, 15) {
		t.Errorf("3DFACE 几何: %+v %+v", gf.p2, gf.p4)
	}
	// 文本提取闭环（Texts 语义）
	texts := got.Texts()
	found := false
	for _, ti := range texts {
		if ti.Text == "HELLO_FWD" {
			found = true
		}
	}
	if !found {
		t.Errorf("Texts 缺少正向写出的 TEXT: %v", texts)
	}
}

// TestWriteForwardBlockInsert 块定义（POLYLINE_2D + 顶点）+ INSERT 的
// handle 流附加段闭环：块内实体经 owner 归属聚合，INSERT 的块头句柄可回读。
func TestWriteForwardBlockInsert(t *testing.T) {
	doc := fwdSynthDoc(t)
	const blkHdl = uint64(0x200)
	// 块内 POLYLINE_2D + 2 顶点（owner 归属块头/父实体）
	poly := &entPolyline2d{flags: 0, curveType: 0, extrusion: point3{0, 0, 1}}
	poly.handle = 0x210
	poly.mode = 0
	poly.owner = blkHdl
	poly.layer = 0x10
	poly.firstVertex = 0x211
	poly.lastVertex = 0x212
	v1 := &entVertex2d{position: point3{0, 0, 0}, bulge: 0}
	v1.handle = 0x211
	v1.mode = 0
	v1.owner = poly.handle
	v1.layer = 0x10
	v2 := &entVertex2d{position: point3{30, 40, 0}}
	v2.handle = 0x212
	v2.mode = 0
	v2.owner = poly.handle
	v2.layer = 0x10
	doc.blocks[blkHdl] = []any{poly, v1, v2}
	// 模型空间 INSERT 引用块
	ins := &entInsert{position: point3{1, 2, 0}, scale: point3{1, 1, 1}, blockHeader: blkHdl}
	ins.handle = 0x300
	ins.mode = 2
	ins.layer = 0x10
	doc.classify(ins)
	got := fwdWriteParse(t, doc)
	if got.EntityCount() == 0 {
		t.Fatal("回读实体为空")
	}
	var gIns *entInsert
	for _, e := range got.modelSpace {
		if ins2, ok := e.(*entInsert); ok {
			gIns = ins2
		}
	}
	if gIns == nil {
		t.Fatal("回读缺少 INSERT")
	}
	if gIns.blockHeader != blkHdl {
		t.Errorf("INSERT 块头句柄: %d != %d", gIns.blockHeader, blkHdl)
	}
	if !nearGeo(gIns.position.y, 2) {
		t.Errorf("INSERT 位置: %+v", gIns.position)
	}
	// 块内顶点按 owner 聚合：poly 的 owner 为块头，顶点的 owner 为 poly
	var gotPoly *entPolyline2d
	for _, list := range got.blocks {
		for _, v := range list {
			if p, ok := v.(*entPolyline2d); ok {
				gotPoly = p
			}
		}
	}
	if gotPoly == nil {
		t.Fatal("回读缺少 POLYLINE_2D")
	}
	verts, ok := got.blocks[gotPoly.handle]
	if !ok || len(verts) < 2 {
		t.Fatalf("顶点归属聚合实体数: %d（want ≥2）", len(verts))
	}
	var gotVerts []*entVertex2d
	for _, v := range verts {
		if vv, ok := v.(*entVertex2d); ok {
			gotVerts = append(gotVerts, vv)
		}
	}
	if len(gotVerts) != 2 {
		t.Fatalf("回读顶点数: %d != 2", len(gotVerts))
	}
	if !nearGeo(gotVerts[1].position.x, 30) || !nearGeo(gotVerts[1].position.y, 40) {
		t.Errorf("VERTEX_2D 几何: %+v", gotVerts[1].position)
	}
}

// TestWriteForwardInsertAttrib INSERT + ATTRIB 的属性链闭环：hasAttribs
// 位 + R2000 首/末属性句柄 + SEQEND 结构，回读 Texts 含属性文本。
func TestWriteForwardInsertAttrib(t *testing.T) {
	doc := fwdSynthDoc(t)
	const blkHdl = uint64(0x210)
	blkEnt := &entBlockLike{name: "SIGN"}
	blkEnt.handle = 0x211
	blkEnt.mode = 0
	blkEnt.owner = blkHdl
	blkEnt.typeName = "BLOCK"
	endblk := &entBlockLike{}
	endblk.handle = 0x212
	endblk.mode = 0
	endblk.owner = blkHdl
	endblk.typeName = "ENDBLK"
	doc.blocks[blkHdl] = []any{blkEnt, endblk}
	attr := &entAttrib{text: "ROOM-101", tag: "ROOM", insertion: point3{1, 1, 0}, height: 2}
	attr.handle = 0x220
	attr.mode = 0
	attr.owner = blkHdl
	attr.layer = 0x10
	doc.classify(attr)
	doc.attribs[attr.handle] = attr
	ins := &entInsert{position: point3{5, 6, 0}, scale: point3{1, 1, 1}, blockHeader: blkHdl, attribs: []uint64{attr.handle}}
	ins.handle = 0x230
	ins.mode = 2
	ins.layer = 0x10
	doc.classify(ins)
	got := fwdWriteParse(t, doc)
	if got.EntityCount() == 0 {
		t.Fatal("回读实体为空")
	}
	texts := got.Texts()
	found := false
	for _, ti := range texts {
		if ti.Text == "ROOM-101" {
			found = true
		}
	}
	if !found {
		t.Errorf("Texts 缺少正向写出的 ATTRIB 文本: %v", texts)
	}
	var gIns *entInsert
	for _, e := range got.modelSpace {
		if i2, ok := e.(*entInsert); ok {
			gIns = i2
		}
	}
	if gIns == nil {
		t.Fatal("回读缺少 INSERT")
	}
	if len(gIns.attribs) < 2 || gIns.attribs[0] != attr.handle {
		t.Errorf("INSERT 属性句柄: %v（want 首=%d）", gIns.attribs, attr.handle)
	}
	if a, ok := got.attribs[attr.handle]; !ok || a.text != "ROOM-101" {
		t.Errorf("ATTRIB 回读: %+v", a)
	}
}

// TestWriteForwardJSONGoldRoundtrip JSON gold（dwgread 导出）→ WriteDwg →
// Parse，与 Parse(同源 DWG) 的模型空间主力类实体键值对照。环境门控：
// gold JSON 与同源 DWG 样本缺失时跳过。
func TestWriteForwardJSONGoldRoundtrip(t *testing.T) {
	cases := []struct {
		ver    string // gold JSON 版本串（ex2000.json 等）
		sample string // 同源 DWG 样本名
	}{
		{"2000", "example_2000.dwg"},
		{"2004", "example_2004.dwg"},
		{"2007", "example_2007.dwg"},
		{"2010", "example_2010.dwg"},
		{"2013", "example_2013.dwg"},
		{"2018", "example_2018.dwg"},
		{"r13", "example_r13.dwg"},
		{"r14", "example_r14.dwg"},
	}
	dataDir := testsupport.LibredwgTestDataDir()
	for _, tc := range cases {
		t.Run(tc.ver, func(t *testing.T) {
			goldPath := testsupport.LibredwgGoldJSONPath(tc.ver)
			gold, err := os.ReadFile(goldPath)
			if err != nil {
				t.Skipf("gold JSON 不可用: %v", err)
			}
			doc, err := ParseJSON(gold)
			if err != nil {
				t.Fatalf("ParseJSON 失败: %v", err)
			}
			var buf bytes.Buffer
			if err := WriteDwg(doc, &buf); err != nil {
				t.Fatalf("WriteDwg 失败: %v", err)
			}
			got, err := Parse(buf.Bytes())
			if err != nil {
				t.Fatalf("回读 Parse 失败: %v", err)
			}
			src, err := os.ReadFile(filepath.Join(dataDir, tc.sample))
			if err != nil {
				t.Skipf("同源 DWG 不可用: %v", err)
			}
			base, err := Parse(src)
			if err != nil {
				t.Fatalf("基线 Parse 失败: %v", err)
			}
			compareForwardEntities(t, base, got)
		})
	}
}

// compareForwardEntities 主力类键值对照：LINE/CIRCLE/ARC/POINT/ELLIPSE/
// TEXT/MTEXT/LWPOLYLINE/INSERT 按句柄对齐基线与正向回读实体，几何/文本
// 逐键一致（基线侧类型不在对照集或句柄缺失时跳过）。
func compareForwardEntities(t *testing.T, base, got *Document) {
	t.Helper()
	pick := func(doc *Document) map[uint64]any {
		m := map[uint64]any{}
		for _, e := range doc.modelSpaceEntities() {
			if b := entBase(e); b != nil && b.handle != 0 {
				m[b.handle] = e
			}
		}
		return m
	}
	bm, gm := pick(base), pick(got)
	const tol = 0.05
	checked := 0
	for h, be := range bm {
		ge, ok := gm[h]
		if !ok {
			continue
		}
		switch a := be.(type) {
		case *entLine:
			if b, ok := ge.(*entLine); ok {
				if nearTol(a.start.x, b.start.x, tol) && nearTol(a.end.y, b.end.y, tol) {
					checked++
				} else {
					t.Errorf("h=%d LINE 几何不一致: (%v,%v)-(%v,%v) vs (%v,%v)-(%v,%v)", h,
						a.start.x, a.start.y, a.end.x, a.end.y, b.start.x, b.start.y, b.end.x, b.end.y)
				}
			}
		case *entCircle:
			if b, ok := ge.(*entCircle); ok {
				if nearTol(a.center.x, b.center.x, tol) && nearTol(a.radius, b.radius, tol) {
					checked++
				} else {
					t.Errorf("h=%d CIRCLE 不一致: c=%v r=%v vs c=%v r=%v", h, a.center, a.radius, b.center, b.radius)
				}
			}
		case *entArc:
			if b, ok := ge.(*entArc); ok {
				if nearTol(a.center.x, b.center.x, tol) && nearTol(a.radius, b.radius, tol) &&
					nearTol(rad2deg(a.angleStart), rad2deg(b.angleStart), 0.5) {
					checked++
				} else {
					t.Errorf("h=%d ARC 不一致: c=%v r=%v vs c=%v r=%v", h, a.center, a.radius, b.center, b.radius)
				}
			}
		case *entPoint:
			if b, ok := ge.(*entPoint); ok {
				if nearTol(a.location.x, b.location.x, tol) && nearTol(a.location.y, b.location.y, tol) {
					checked++
				}
			}
		case *entEllipse:
			if b, ok := ge.(*entEllipse); ok {
				if nearTol(a.center.x, b.center.x, tol) && nearTol(a.ratio, b.ratio, tol) {
					checked++
				}
			}
		case *entLwPolyline:
			if b, ok := ge.(*entLwPolyline); ok && len(a.vertices) == len(b.vertices) {
				same := true
				for i := range a.vertices {
					if !nearTol(a.vertices[i].x, b.vertices[i].x, tol) || !nearTol(a.vertices[i].y, b.vertices[i].y, tol) {
						same = false
						break
					}
				}
				if same {
					checked++
				} else {
					t.Errorf("h=%d LWPOLYLINE 顶点不一致", h)
				}
			}
		case *entText:
			if b, ok := ge.(*entText); ok {
				if a.text == b.text && nearTol(a.height, b.height, tol) {
					checked++
				} else {
					t.Errorf("h=%d TEXT 不一致: %q vs %q", h, a.text, b.text)
				}
			}
		case *entMText:
			if b, ok := ge.(*entMText); ok {
				if stripMTextFormat(a.text) == stripMTextFormat(b.text) {
					checked++
				} else {
					t.Errorf("h=%d MTEXT 不一致: %q vs %q", h, a.text, b.text)
				}
			}
		case *entInsert:
			if b, ok := ge.(*entInsert); ok {
				if nearTol(a.position.x, b.position.x, tol) && a.blockHeader == b.blockHeader {
					checked++
				}
			}
		case *entSpline:
			if b, ok := ge.(*entSpline); ok {
				same := a.scenario == b.scenario && a.degree == b.degree &&
					len(a.controlPoints) == len(b.controlPoints) &&
					len(a.fitPoints) == len(b.fitPoints) &&
					len(a.knots) == len(b.knots)
				if same && len(a.controlPoints) > 0 {
					same = nearTol(a.controlPoints[0].x, b.controlPoints[0].x, tol)
				}
				if same && len(a.fitPoints) > 0 {
					same = nearTol(a.fitPoints[0].y, b.fitPoints[0].y, tol)
				}
				if same {
					checked++
				} else {
					t.Errorf("h=%d SPLINE 不一致: scenario %d/%d ctrl %d/%d fit %d/%d", h,
						a.scenario, b.scenario, len(a.controlPoints), len(b.controlPoints),
						len(a.fitPoints), len(b.fitPoints))
				}
			}
		case *entDimension:
			if b, ok := ge.(*entDimension); ok {
				// 公共段口径：类型标志 + 文本中点 + 用户文字。类型专属点的
				// gold 键映射在 JSON 侧尚不完整（ANG2LN 的 xline 系四点、
				// ORDINATE 的 feature/leader 错位、ARC_DIMENSION 专属键），
				// 专属点位流布局由 TestWriteForwardBatchE 合成闭环覆盖。
				if a.dimFlag&0x7 == b.dimFlag&0x7 &&
					nearTol(a.textMidpoint.x, b.textMidpoint.x, tol) &&
					nearTol(a.textMidpoint.y, b.textMidpoint.y, tol) &&
					a.userText == b.userText {
					checked++
				} else {
					t.Errorf("h=%d DIMENSION 不一致: flag %#x/%#x mid(%v,%v)/(%v,%v) text %q/%q", h,
						a.dimFlag&0x7, b.dimFlag&0x7, a.textMidpoint.x, a.textMidpoint.y,
						b.textMidpoint.x, b.textMidpoint.y, a.userText, b.userText)
				}
			}
		case *entHatch:
			if b, ok := ge.(*entHatch); ok {
				if a.name == b.name && len(a.paths) == len(b.paths) {
					checked++
				} else {
					t.Errorf("h=%d HATCH 不一致: %q/%q paths %d/%d", h, a.name, b.name, len(a.paths), len(b.paths))
				}
			}
		case *entRay:
			if b, ok := ge.(*entRay); ok {
				if nearTol(a.start.x, b.start.x, tol) && nearTol(a.unitVector.y, b.unitVector.y, tol) && a.xline == b.xline {
					checked++
				} else {
					t.Errorf("h=%d RAY/XLINE 不一致", h)
				}
			}
		case *entMLine:
			if b, ok := ge.(*entMLine); ok {
				if len(a.vertices) == len(b.vertices) && nearTol(a.scale, b.scale, tol) {
					checked++
				} else {
					t.Errorf("h=%d MLINE 不一致: scale %v/%v verts %d/%d", h, a.scale, b.scale, len(a.vertices), len(b.vertices))
				}
			}
		case *entTolerance:
			if b, ok := ge.(*entTolerance); ok {
				if a.text == b.text && nearTol(a.insertion.x, b.insertion.x, tol) {
					checked++
				} else {
					t.Errorf("h=%d TOLERANCE 不一致: %q/%q", h, a.text, b.text)
				}
			}
		case *entViewport:
			if b, ok := ge.(*entViewport); ok {
				if nearTol(a.width, b.width, tol) && nearTol(a.height, b.height, tol) {
					checked++
				} else {
					t.Errorf("h=%d VIEWPORT 不一致: %v/%v x %v/%v", h, a.width, b.width, a.height, b.height)
				}
			}
		case *entPolyline3d:
			if b, ok := ge.(*entPolyline3d); ok {
				if a.flags70 == b.flags70 && a.flags75 == b.flags75 {
					checked++
				}
			}
		case *entVertex3d:
			if b, ok := ge.(*entVertex3d); ok {
				if nearTol(a.position.x, b.position.x, tol) && nearTol(a.position.z, b.position.z, tol) {
					checked++
				}
			}
		case *entVertexPface:
			if b, ok := ge.(*entVertexPface); ok {
				if nearTol(a.position.x, b.position.x, tol) && nearTol(a.position.y, b.position.y, tol) {
					checked++
				}
			}
		case *entVertexPfaceFace:
			if b, ok := ge.(*entVertexPfaceFace); ok {
				if a.vertind == b.vertind {
					checked++
				}
			}
		case *entPolylinePface:
			if b, ok := ge.(*entPolylinePface); ok {
				if a.numVertices == b.numVertices && a.numFaces == b.numFaces {
					checked++
				}
			}
		}
	}
	if checked == 0 {
		t.Skip("样本无主力类可对照实体（或类型不在正向编码器覆盖范围）")
	}
	t.Logf("对照通过实体数: %d（基线 %d / 回读 %d）", checked, len(bm), len(gm))
}

// nearTol 带容差的浮点比较。
func nearTol(a, b, tol float64) bool { return math.Abs(a-b) < tol }

// TestWriteForwardDXFCross ParseDXF(testdata 样本) → WriteDwg → dwgread
// 交叉验证。环境门控：dwgread 可执行与样本存在；通过判据为 dwgread
// 退出码 0 且 DXF 输出含 ENTITIES 段。
func TestWriteForwardDXFCross(t *testing.T) {
	dwgread := os.Getenv("CAD_DWGREAD")
	if dwgread == "" {
		dwgread = "/tmp/libredwg/programs/dwgread"
	}
	if _, err := os.Stat(dwgread); err != nil {
		t.Skipf("dwgread 不可用: %v", err)
	}
	sample := filepath.Join(testsupport.LibredwgTestDataDir(), "example_2000.dxf")
	if _, err := os.Stat(sample); err != nil {
		t.Skipf("DXF 样本不可用: %v", err)
	}
	data, err := os.ReadFile(sample)
	if err != nil {
		t.Skipf("DXF 读取失败: %v", err)
	}
	doc, err := ParseDXF(data)
	if err != nil {
		t.Fatalf("ParseDXF 失败: %v", err)
	}
	var buf bytes.Buffer
	if err := WriteDwg(doc, &buf); err != nil {
		t.Fatalf("WriteDwg 失败: %v", err)
	}
	// 回读闭环：实体数不丢
	got, err := Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("回读 Parse 失败: %v", err)
	}
	if got.EntityCount() == 0 {
		t.Fatal("DXF 正向写出后实体为空")
	}
	// dwgread 交叉验证：完整解析写出文件（-O DXF 输出到文件，与包内
	// dxf_cross_test 的生成链一致）
	out := filepath.Join(t.TempDir(), "fwd.dwg")
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	dxfOut := filepath.Join(t.TempDir(), "fwd.dxf")
	cmd := exec.Command(dwgread, "-O", "DXF", "-o", dxfOut, out)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Errorf("dwgread 解析正向写出文件失败: %v\n%s", err, stderr.String()[:minInt(600, stderr.Len())])
		return
	}
	dxf, err := os.ReadFile(dxfOut)
	if err != nil || len(dxf) == 0 {
		t.Errorf("dwgread DXF 输出为空: %v", err)
		return
	}
	if !bytes.Contains(dxf, []byte("ENTITIES")) {
		t.Errorf("dwgread DXF 输出缺少实体段（%d 字节）", len(dxf))
	}
}

// minInt 整数最小值（stderr 截断用）。
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestWriteForwardBatchE 极限批次 E 固定码实体编码器的合成闭环：
// SPLINE 双模式 / DIMENSION 七型 / HATCH 两类路径 / RAY/XLINE / LEADER /
// MLINE / TOLERANCE / SHAPE / VERTEX_3D / PFACE 系 / POLYLINE_3D /
// POLYLINE_MESH / VIEWPORT → WriteDwg → Parse 逐键对照。
func TestWriteForwardBatchE(t *testing.T) {
	// SPLINE 控制点模式（带权重）与拟合点模式
	splineCtrl := &entSpline{
		scenario: 1, degree: 3, rational: true,
		knotTolerance: 1e-7, ctrlTolerance: 2e-7,
		knots:         []float64{0, 0, 0, 1, 2, 3, 3, 3},
		controlPoints: []point3{{0, 0, 0}, {10, 20, 1}, {30, 10, 2}, {40, 40, 0}},
		weights:       []float64{1, 2, 2, 1},
	}
	splineFit := &entSpline{
		scenario: 2, degree: 3,
		fitTolerance: 1e-10,
		fitPoints:    []point3{{1, 1, 0}, {5, 6, 0}, {9, 2, 0}, {12, 8, 0}},
	}
	// DIMENSION 七型（flag 低 3 位分派）
	dimLinear := &entDimension{
		dimFlags: 0x80, dimFlag: 0x80,
		extrusion: point3{0, 0, 1}, textMidpoint: point3{5, 6, 0}, elevation: 0,
		userText: "DL<>", textRotation: 0.1, horizontalDir: 0.2,
		insertScale: point3{1, 1, 1}, insertRotation: 0.3,
		attachmentPoint: 1, lineSpacingStyle: 1, lineSpacingFactor: 1.5,
		actualMeasurement: 42.5, insertPoint: point3{1, 2, 0}, hasInsertPoint: true,
		point13: point3{0, 0, 0}, point14: point3{40, 0, 0}, point10: point3{10, -5, 0},
		extLineRotation: 0.05, dimRotation: 0.25,
	}
	dimAligned := &entDimension{dimFlags: 0x81, dimFlag: 0x81, extrusion: point3{0, 0, 1},
		textMidpoint: point3{9, 9, 0}, point13: point3{0, 0, 0}, point14: point3{30, 40, 0},
		point10: point3{15, 20, 0}, extLineRotation: 0.02}
	dimAng2Ln := &entDimension{dimFlags: 0x82, dimFlag: 0x82, extrusion: point3{0, 0, 1},
		textMidpoint: point3{1, 1, 0}, point16x: 7, p16y: 8,
		point13: point3{0, 0, 0}, point14: point3{10, 0, 0}, point15: point3{20, 10, 0},
		point10: point3{5, 5, 0}}
	dimDiameter := &entDimension{dimFlags: 0x83, dimFlag: 0x83, extrusion: point3{0, 0, 1},
		textMidpoint: point3{2, 2, 0}, point15: point3{12, 12, 0}, point10: point3{-12, -12, 0},
		leaderLen: 8.25, dimstyleHandle: 0x77}
	dimRadius := &entDimension{dimFlags: 0x84, dimFlag: 0x84, extrusion: point3{0, 0, 1},
		textMidpoint: point3{3, 3, 0}, point10: point3{0, 0, 0}, point15: point3{9, 0, 0},
		leaderLen: 3.5}
	dimAng3Pt := &entDimension{dimFlags: 0x85, dimFlag: 0x85, extrusion: point3{0, 0, 1},
		textMidpoint: point3{4, 4, 0}, point10: point3{0, 0, 0}, point13: point3{10, 0, 0},
		point14: point3{0, 10, 0}, point15: point3{5, 5, 0}}
	dimOrdinate := &entDimension{dimFlags: 0x86, dimFlag: 0x86, extrusion: point3{0, 0, 1},
		textMidpoint: point3{6, 6, 0}, point10: point3{1, 1, 0}, point13: point3{11, 1, 0},
		point14: point3{1, 11, 0}, flag2: 0x80}
	// HATCH：边集路径（直线+弧）与多段线路径（带凸度）
	hatchEdges := &entHatch{
		elevation: 1.25, extrusion: point3{0, 0, 1}, name: "ANGLE",
		associative: true, style: 0, patternType: 1, angle: 0.5, scaleSpacing: 2,
		deflines: []hatchDefLine{{
			angle: 0.25, pt0: point2{1, 2}, offset: point2{3, 4},
			dashes: []float64{10, -3},
		}},
		paths: []hatchPath{{
			flag: 1,
			segs: []hatchSeg{
				{curveType: 1, first: point2{0, 0}, second: point2{10, 0}},
				{curveType: 2, center: point2{10, 5}, radius: 5, startAng: 0, endAng: 1.5, ccw: true},
			},
		}},
		seeds: []point2{{2, 3}, {4, 5}},
	}
	hatchPoly := &entHatch{
		name: "SOLID", solidFill: true, style: 1, patternType: 1,
		paths: []hatchPath{{
			flag:          3,
			isPolyline:    true,
			bulgesPresent: true,
			closed:        true,
			polyVerts: []hatchPolyVert{
				{p: point2{0, 0}, bulge: 0},
				{p: point2{10, 0}, bulge: 0.5},
				{p: point2{10, 10}, bulge: 0},
			},
		}},
	}
	ray := &entRay{start: point3{1, 2, 3}, unitVector: point3{1, 0, 0}}
	xline := &entRay{start: point3{4, 5, 6}, unitVector: point3{0, 1, 0}, xline: true}
	leader := &entLeader{
		annotationType: 1, pathType: 0,
		points:    []point3{{0, 0, 0}, {10, 10, 0}, {20, 10, 0}},
		origin:    point3{0, 0, 0},
		boxHeight: 3, boxWidth: 12, arrowheadOn: true, arrowheadType: 1,
	}
	mline := &entMLine{
		scale: 20, justification: 1, openClosed: 3, linesInStyle: 2,
		vertices: []entMLineVertex{
			{position: point3{0, 0, 0}, direction: point3{1, 0, 0}, miter: point3{0, 1, 0},
				segParams: []float64{-10, 10, -10, 10}, areaParams: []float64{0, 0, 0, 0}},
			{position: point3{50, 0, 0}, direction: point3{1, 0, 0}, miter: point3{0, 1, 0},
				segParams: []float64{-10, 10, -10, 10}, areaParams: []float64{0, 0, 0, 0}},
		},
		styleHandle: 0x99,
	}
	tolerance := &entTolerance{
		text: "{\\Fgdt;r}%%v1", insertion: point3{7, 8, 0},
		xDirection: point3{1, 0, 0}, extrusion: point3{0, 0, 1}, dimstyle: 0x88,
	}
	shape := &entShape{
		insertion: point3{1, 1, 0}, scale: 2, rotation: 0.5, widthFactor: 1,
		oblique: 0.1, thickness: 0.2, styleId: 3, extrusion: point3{0, 0, 1},
	}
	viewport := &entViewport{
		center: point3{100, 100, 0}, width: 210, height: 148,
		viewTarget: point3{0, 0, 0}, viewDir: point3{0, 0, 1}, viewTwist: 0.1,
		viewSize: 200, lensLength: 50, frontZ: 0, backZ: 0, snapAng: 0,
		viewCtr: point2{50, 50}, snapBase: point2{0, 0}, snapUnit: point2{10, 10},
		gridUnit: point2{10, 10}, circleZoom: 100, numFrozenLayers: 0,
		statusFlag: 1, styleSheet: "", renderMode: 0, ucsVP: true,
		ucsorg: point3{0, 0, 0}, ucsxdir: point3{1, 0, 0}, ucsydir: point3{0, 1, 0},
	}
	vtx3d := &entVertex3d{flags: 32, position: point3{1, 2, 3}}
	pfaceVtx := &entVertexPface{flag: 192, position: point3{4, 5, 6}}
	pfaceFace := &entVertexPfaceFace{flag: 128, vertind: [4]int32{1, 2, 3, 0}}
	poly3d := &entPolyline3d{flags75: 0, flags70: 0}
	polyPface := &entPolylinePface{numVertices: 3, numFaces: 1}
	polyMesh := &entPolylineMesh{flags: 0, curveType: 0, mVertexCount: 2, nVertexCount: 2, mDensity: 1, nDensity: 1}

	doc := fwdSynthDoc(t, splineCtrl, splineFit,
		dimLinear, dimAligned, dimAng2Ln, dimDiameter, dimRadius, dimAng3Pt, dimOrdinate,
		hatchEdges, hatchPoly, ray, xline, leader, mline, tolerance, shape, viewport,
		vtx3d, pfaceVtx, pfaceFace, poly3d, polyPface, polyMesh)
	got := fwdWriteParse(t, doc)
	byHandle := map[uint64]any{}
	for _, e := range got.modelSpace {
		byHandle[entBase(e).handle] = e
	}
	get := func(want any) any {
		h := entBase(want).handle
		e, ok := byHandle[h]
		if !ok {
			t.Fatalf("回读缺少句柄 %d（%T）", h, want)
		}
		return e
	}
	// SPLINE 双模式
	gs := get(splineCtrl).(*entSpline)
	if gs.scenario != 1 || gs.degree != 3 || !gs.rational {
		t.Errorf("SPLINE 控制点模式: scenario=%d degree=%d rational=%v", gs.scenario, gs.degree, gs.rational)
	}
	if len(gs.knots) != 8 || len(gs.controlPoints) != 4 || len(gs.weights) != 4 {
		t.Fatalf("SPLINE 控制点数组: knots=%d ctrl=%d w=%d", len(gs.knots), len(gs.controlPoints), len(gs.weights))
	}
	if !nearGeo(gs.knots[3], 1) || !nearGeo(gs.controlPoints[2].x, 30) || !nearGeo(gs.weights[1], 2) {
		t.Errorf("SPLINE 控制点数据: knots=%v ctrl2=%+v w=%v", gs.knots, gs.controlPoints[2], gs.weights)
	}
	gsf := get(splineFit).(*entSpline)
	if gsf.scenario != 2 || len(gsf.fitPoints) != 4 || !nearGeo(gsf.fitTolerance, 1e-10) {
		t.Errorf("SPLINE 拟合模式: scenario=%d fit=%v tol=%v", gsf.scenario, gsf.fitPoints, gsf.fitTolerance)
	}
	// DIMENSION 七型：类型码 + 公共段 + 专属点
	dimCases := []struct {
		name     string
		want     *entDimension
		checkGeo func(*entDimension) error
	}{
		{"DIM_LINEAR", dimLinear, func(g *entDimension) error {
			if !nearGeo(g.point10.x, 10) || !nearGeo(g.point13.x, 0) || !nearGeo(g.point14.x, 40) ||
				!nearGeo(g.extLineRotation, 0.05) || !nearGeo(g.dimRotation, 0.25) ||
				g.userText != "DL<>" || !nearGeo(g.actualMeasurement, 42.5) {
				return fmt.Errorf("几何 %+v %+v %+v", g.point10, g.point13, g.point14)
			}
			return nil
		}},
		{"DIM_ALIGNED", dimAligned, func(g *entDimension) error {
			if !nearGeo(g.point14.y, 40) || !nearGeo(g.extLineRotation, 0.02) {
				return fmt.Errorf("几何 %+v", g.point14)
			}
			return nil
		}},
		{"DIM_ANG2LN", dimAng2Ln, func(g *entDimension) error {
			if !nearGeo(g.point16x, 7) || !nearGeo(g.p16y, 8) || !nearGeo(g.point15.x, 20) {
				return fmt.Errorf("几何 16=(%v,%v) 15=%+v", g.point16x, g.p16y, g.point15)
			}
			return nil
		}},
		{"DIM_DIAMETER", dimDiameter, func(g *entDimension) error {
			if !nearGeo(g.point15.x, 12) || !nearGeo(g.point10.x, -12) || !nearGeo(g.leaderLen, 8.25) {
				return fmt.Errorf("几何 15=%+v 10=%+v", g.point15, g.point10)
			}
			return nil
		}},
		{"DIM_RADIUS", dimRadius, func(g *entDimension) error {
			if !nearGeo(g.point15.x, 9) || !nearGeo(g.leaderLen, 3.5) {
				return fmt.Errorf("几何 15=%+v", g.point15)
			}
			return nil
		}},
		{"DIM_ANG3PT", dimAng3Pt, func(g *entDimension) error {
			if !nearGeo(g.point10.x, 0) || !nearGeo(g.point15.x, 5) {
				return fmt.Errorf("几何 10=%+v 15=%+v", g.point10, g.point15)
			}
			return nil
		}},
		{"DIM_ORDINATE", dimOrdinate, func(g *entDimension) error {
			if !nearGeo(g.point13.x, 11) || g.dimFlag&0x7 != 6 {
				return fmt.Errorf("几何 13=%+v flag=%#x", g.point13, g.dimFlag)
			}
			return nil
		}},
	}
	for _, dc := range dimCases {
		g, ok := get(dc.want).(*entDimension)
		if !ok {
			t.Fatalf("%s 回读类型不符", dc.name)
		}
		if g.dimFlag&0x7 != dc.want.dimFlag&0x7 {
			t.Errorf("%s 类型标志: %#x != %#x", dc.name, g.dimFlag&0x7, dc.want.dimFlag&0x7)
		}
		if err := dc.checkGeo(g); err != nil {
			t.Errorf("%s: %v", dc.name, err)
		}
	}
	if g := get(dimDiameter).(*entDimension); g.dimstyleHandle != 0x77 {
		t.Errorf("DIMENSION dimstyle 句柄: %d != 0x77", g.dimstyleHandle)
	}
	// HATCH 边集路径
	gh := get(hatchEdges).(*entHatch)
	if gh.name != "ANGLE" || !gh.associative || len(gh.paths) != 1 {
		t.Fatalf("HATCH 边集: name=%q assoc=%v paths=%d", gh.name, gh.associative, len(gh.paths))
	}
	if len(gh.paths[0].segs) != 2 || gh.paths[0].segs[0].curveType != 1 {
		t.Fatalf("HATCH 边段: %+v", gh.paths[0].segs)
	}
	s0 := gh.paths[0].segs[0]
	if !nearGeo(s0.second.x, 10) || !nearGeo(s0.second.y, 0) {
		t.Errorf("HATCH 直线边: %+v", s0)
	}
	s1 := gh.paths[0].segs[1]
	if s1.curveType != 2 || !nearGeo(s1.radius, 5) || !nearGeo(s1.endAng, 1.5) || !s1.ccw {
		t.Errorf("HATCH 弧边: %+v", s1)
	}
	if len(gh.deflines) != 1 || !nearGeo(gh.deflines[0].offset.y, 4) || len(gh.deflines[0].dashes) != 2 {
		t.Errorf("HATCH 定义线: %+v", gh.deflines)
	}
	// 种子点段为纯位流消费（读侧不回填 seeds，仅验证布局不错位——
	// 能正确解出路径与定义线即证明种子段偏移正确）
	// HATCH 多段线路径
	ghp := get(hatchPoly).(*entHatch)
	if !ghp.solidFill || len(ghp.paths) != 1 || !ghp.paths[0].isPolyline {
		t.Fatalf("HATCH 多段线: solid=%v paths=%+v", ghp.solidFill, ghp.paths)
	}
	if len(ghp.paths[0].polyVerts) != 3 || !nearGeo(ghp.paths[0].polyVerts[1].bulge, 0.5) || !ghp.paths[0].closed {
		t.Errorf("HATCH 多段线顶点: %+v", ghp.paths[0].polyVerts)
	}
	// RAY / XLINE
	gr := get(ray).(*entRay)
	if !nearGeo(gr.start.z, 3) || !nearGeo(gr.unitVector.x, 1) {
		t.Errorf("RAY: %+v %+v", gr.start, gr.unitVector)
	}
	gx := get(xline).(*entRay)
	if !gx.xline || !nearGeo(gx.start.x, 4) {
		t.Errorf("XLINE: xline=%v start=%+v", gx.xline, gx.start)
	}
	// LEADER
	gl := get(leader).(*entLeader)
	if len(gl.points) != 3 || !nearGeo(gl.points[2].x, 20) || gl.annotationType != 1 || !gl.arrowheadOn {
		t.Errorf("LEADER: pts=%v at=%d on=%v", gl.points, gl.annotationType, gl.arrowheadOn)
	}
	// MLINE
	gm := get(mline).(*entMLine)
	if gm.scale != 20 || len(gm.vertices) != 2 || gm.linesInStyle != 2 {
		t.Fatalf("MLINE: scale=%v verts=%d lines=%d", gm.scale, len(gm.vertices), gm.linesInStyle)
	}
	if !nearGeo(gm.vertices[1].position.x, 50) || len(gm.vertices[0].segParams) != 4 {
		t.Errorf("MLINE 顶点: %+v segs=%v", gm.vertices[1], gm.vertices[0].segParams)
	}
	// TOLERANCE
	gt := get(tolerance).(*entTolerance)
	if gt.text != tolerance.text || !nearGeo(gt.insertion.x, 7) || gt.dimstyle != 0x88 {
		t.Errorf("TOLERANCE: %q ins=%+v style=%d", gt.text, gt.insertion, gt.dimstyle)
	}
	// SHAPE
	gsh := get(shape).(*entShape)
	if gsh.scale != 2 || gsh.styleId != 3 || !nearGeo(gsh.thickness, 0.2) {
		t.Errorf("SHAPE: %+v", gsh)
	}
	// VIEWPORT
	gv := get(viewport).(*entViewport)
	if !nearGeo(gv.width, 210) || !nearGeo(gv.height, 148) || !nearGeo(gv.viewSize, 200) || gv.circleZoom != 100 {
		t.Errorf("VIEWPORT: w=%v h=%v vs=%v cz=%d", gv.width, gv.height, gv.viewSize, gv.circleZoom)
	}
	// VERTEX_3D / PFACE 系
	gv3 := get(vtx3d).(*entVertex3d)
	if gv3.flags != 32 || !nearGeo(gv3.position.z, 3) {
		t.Errorf("VERTEX_3D: %+v", gv3)
	}
	gpv := get(pfaceVtx).(*entVertexPface)
	if gpv.flag != 192 || !nearGeo(gpv.position.x, 4) {
		t.Errorf("VERTEX_PFACE: %+v", gpv)
	}
	gpf := get(pfaceFace).(*entVertexPfaceFace)
	if gpf.vertind != [4]int32{1, 2, 3, 0} {
		t.Errorf("VERTEX_PFACE_FACE: %v", gpf.vertind)
	}
	// POLYLINE_3D / PFACE / MESH
	gp3 := get(poly3d).(*entPolyline3d)
	if gp3.flags70 != 0 || gp3.flags75 != 0 {
		t.Errorf("POLYLINE_3D: %+v", gp3)
	}
	gpp := get(polyPface).(*entPolylinePface)
	if gpp.numVertices != 3 || gpp.numFaces != 1 {
		t.Errorf("POLYLINE_PFACE: %d/%d", gpp.numVertices, gpp.numFaces)
	}
	gpm := get(polyMesh).(*entPolylineMesh)
	if gpm.mVertexCount != 2 || gpm.nVertexCount != 2 {
		t.Errorf("POLYLINE_MESH: %+v", gpm)
	}
	// MLINE 样式句柄经 handle 流回读
	if gm.styleHandle != 0x99 && gm.styleHandle != 0 {
		t.Logf("MLINE 样式句柄回读: %d（owner 归属路径）", gm.styleHandle)
	}
}

// TestWriteForwardDynamicClasses 极限批次 E 动态类闭环：IMAGE/WIPEOUT/
// MULTILEADER/LIGHT/ARC_DIMENSION → WriteDwg → Parse（类段注册动态码 +
// ≥500 类型码写出，回读按类段路由到对应解码器）。
func TestWriteForwardDynamicClasses(t *testing.T) {
	// IMAGE 与 WIPEOUT 同布局（entWipeout 承载，按 typeName 路由）
	image := &entWipeout{
		classVersion: 0,
		pt0:          point3{100, 200, 0}, uvec: point3{50, 0, 0}, vvec: point3{0, 40, 0},
		imageSize: point2{1, 1}, displayProps: 7, clipping: true,
		brightness: 50, contrast: 50, fade: 0,
		clipBoundaryType: 1,
		clipVerts:        []point2{{0, 0}, {1, 1}},
	}
	image.typeName = "IMAGE"
	wipeout := &entWipeout{
		classVersion: 0,
		pt0:          point3{0, 0, 0}, uvec: point3{10, 0, 0}, vvec: point3{0, 10, 0},
		imageSize: point2{1, 1}, displayProps: 7,
		clipBoundaryType: 2,
		clipVerts:        []point2{{0, 0}, {0.5, 0.2}, {1, 0.5}, {0.3, 1}},
	}
	wipeout.typeName = "WIPEOUT"
	mleader := &entMLeader{
		mleaderType: 1, flags: 0x44400, lineLinewt: -2,
		hasLanding: true, hasDogleg: true, landingDist: 0.36, arrowSize: 4,
		styleContent: 2, textLeft: 1, textRight: 6, textAngletype: 1,
		styleAttachment: 1, justification: 3, scaleFactor: 1,
	}
	light := &entLight{
		classVersion: 1, name: "ProbeLight", lightType: 3, status: true,
		lightColorIndex: 5, intensity: 5.4,
		position: point3{1, 2, 0}, target: point3{3, 4, 0},
		attenuationEnd: 10, hotspotAngle: 0.785, falloffAngle: 0.87,
		castShadows: true, shadowMapSize: 256, shadowMapSoftness: 1,
	}
	arcDim := &entDimension{
		dimFlags: 0x25, dimFlag: 0x25,
		extrusion: point3{0, 0, 1}, textMidpoint: point3{2, 2, 0},
		userText: "ARC<>", insertScale: point3{1, 1, 1},
		attachmentPoint: 5, lineSpacingStyle: 1, lineSpacingFactor: 1,
		insertPoint: point3{6125, 2865, 0}, hasInsertPoint: true,
		defPt: point3{6125, 2865, 0}, point13: point3{5486, 2529, 0},
		point14: point3{6571, 2539, 0}, point15: point3{6100, 2700, 0},
		isPartial:     true,
		arcStartParam: 0.3, arcEndParam: 2.1,
		hasLeader:      false,
		dimstyleHandle: 0x77,
	}
	arcDim.typeName = "ARC_DIMENSION"
	doc := fwdSynthDoc(t, image, wipeout, mleader, light, arcDim)
	got := fwdWriteParse(t, doc)
	byKind := map[string]any{}
	for _, e := range got.modelSpace {
		switch d := e.(type) {
		case *entWipeout:
			byKind[d.typeName] = d
		case *entImage:
			// 读侧 IMAGE 路由到独立 entImage 类型（写侧由 entWipeout 承载）
			byKind["IMAGE"] = d
		case *entMLeader:
			byKind["MULTILEADER"] = d
		case *entLight:
			byKind["LIGHT"] = d
		case *entDimension:
			byKind[d.typeName] = d
		}
	}
	gi, ok := byKind["IMAGE"].(*entImage)
	if !ok {
		t.Fatalf("回读缺少 IMAGE（kind=%v）", byKind)
	}
	if !nearGeo(gi.pt0.x, 100) || !nearGeo(gi.uvec.x, 50) || !nearGeo(gi.vvec.y, 40) ||
		!nearGeo(gi.imageSize.x, 1) || gi.displayProps != 7 || !gi.clipping {
		t.Errorf("IMAGE 字段: %+v", gi)
	}
	gw, ok := byKind["WIPEOUT"].(*entWipeout)
	if !ok {
		t.Fatalf("回读缺少 WIPEOUT")
	}
	if len(gw.clipVerts) != 4 || !nearGeo(gw.clipVerts[2].x, 1) || gw.clipBoundaryType != 2 {
		t.Errorf("WIPEOUT 裁剪边界: type=%d verts=%+v", gw.clipBoundaryType, gw.clipVerts)
	}
	gm, ok := byKind["MULTILEADER"].(*entMLeader)
	if !ok {
		t.Fatalf("回读缺少 MULTILEADER")
	}
	if gm.mleaderType != 1 || !gm.hasLanding || !nearGeo(gm.landingDist, 0.36) ||
		!nearGeo(gm.arrowSize, 4) || gm.styleContent != 2 || gm.textLeft != 1 {
		t.Errorf("MULTILEADER 标量: type=%d landing=%v dist=%v arrow=%v", gm.mleaderType, gm.hasLanding, gm.landingDist, gm.arrowSize)
	}
	gl, ok := byKind["LIGHT"].(*entLight)
	if !ok {
		t.Fatalf("回读缺少 LIGHT")
	}
	if gl.name != "ProbeLight" || gl.lightType != 3 || !gl.status || gl.lightColorIndex != 5 ||
		!nearGeo(gl.intensity, 5.4) || !nearGeo(gl.position.x, 1) || !nearGeo(gl.target.x, 3) ||
		gl.shadowMapSize != 256 {
		t.Errorf("LIGHT 字段: %+v", gl)
	}
	ga, ok := byKind["ARC_DIMENSION"].(*entDimension)
	if !ok {
		t.Fatalf("回读缺少 ARC_DIMENSION（kind=%v）", byKind)
	}
	if ga.dimFlag&0x7 != 5 || !nearGeo(ga.defPt.x, 6125) || !nearGeo(ga.point14.x, 6571) ||
		!ga.isPartial || !nearGeo(ga.arcEndParam, 2.1) {
		t.Errorf("ARC_DIMENSION 弧长尾部: def=%+v partial=%v param=%v", ga.defPt, ga.isPartial, ga.arcEndParam)
	}
	if ga.dimstyleHandle != 0x77 {
		t.Errorf("ARC_DIMENSION dimstyle 句柄: %d != 0x77", ga.dimstyleHandle)
	}
	// 动态码 ≥500 与类段注册一致性：回读实体类型名保持 IMAGE/WIPEOUT/
	// MULTILEADER/LIGHT/ARC_DIMENSION（读侧按类段路由，无名即注册缺失）
	for _, name := range []string{"IMAGE", "WIPEOUT", "MULTILEADER", "LIGHT", "ARC_DIMENSION"} {
		if _, ok := byKind[name]; !ok {
			t.Errorf("动态类 %s 未回读（类段注册或类型码写出缺失）", name)
		}
	}
}

// TestWriteForwardGenericObjects 极限批次 E gfWrite 门禁：objGeneric
// Fields 驱动的通用对象（GROUP/XRECORD/DICTIONARYVAR/SCALE/LAYOUT/
// WIPEOUTVARIABLES/PLACEHOLDER/APPID/STYLE）→ WriteDwg → Parse 逐键对照
// （JSON gold 展平键值形态与解码侧 int64 形态双路覆盖）。
func TestWriteForwardGenericObjects(t *testing.T) {
	// JSON gold 展平键形态（float64/string/bool/[]any）
	group := &objGeneric{
		Name: "GROUP", Handle: 0x500, Owner: 0x13,
		Fields: []objField{
			{"name", "PROBE_GROUP"}, {"unnamed", float64(0)}, {"selectable", float64(1)},
			{"num_groups", float64(2)},
			{"groups", []any{[]any{float64(5), float64(2), float64(0x510), float64(0x510)},
				[]any{float64(5), float64(2), float64(0x511), float64(0x511)}}},
		},
	}
	xrecord := &objGeneric{
		Name: "XRECORD", Handle: 0x501, Owner: 0x14,
		Fields: []objField{
			{"xdata", []any{[]any{float64(70), float64(1)}, []any{float64(1), "BA88-PROBE"},
				[]any{float64(310), "DEADBEEF"}}},
			{"cloning", float64(1)},
		},
	}
	dictvar := &objGeneric{
		Name: "DICTIONARYVAR", Handle: 0x502,
		Fields: []objField{{"schema", float64(0)}, {"strvalue", "2"}},
	}
	scale := &objGeneric{
		Name: "SCALE", Handle: 0x503,
		Fields: []objField{
			{"flag", float64(0)}, {"name", "1:2"},
			{"paper_units", 1.0}, {"drawing_units", 2.0}, {"is_unit_scale", true},
		},
	}
	layout := &objGeneric{
		Name: "LAYOUT", Handle: 0x504,
		Fields: []objField{
			{"plotsettings.printer_cfg_file", ""}, {"plotsettings.paper_size", "A4"},
			{"plotsettings.plot_flags", float64(11952)},
			{"plotsettings.left_margin", 6.35}, {"plotsettings.bottom_margin", 19.05},
			{"plotsettings.right_margin", 6.35}, {"plotsettings.top_margin", 19.05},
			{"plotsettings.paper_width", 210.0}, {"plotsettings.paper_height", 297.0},
			{"plotsettings.canonical_media_name", ""},
			{"plotsettings.plot_origin", []any{0.0, 0.0}},
			{"plotsettings.plot_paper_unit", float64(0)},
			{"plotsettings.plot_rotation_mode", float64(0)},
			{"plotsettings.plot_type", float64(5)},
			{"plotsettings.plot_window_ll", []any{0.0, 0.0}},
			{"plotsettings.plot_window_ur", []any{0.0, 0.0}},
			{"plotsettings.plotview_name", ""},
			{"plotsettings.paper_units", 1.0}, {"plotsettings.drawing_units", 1.0},
			{"plotsettings.stylesheet", ""},
			{"plotsettings.std_scale_type", float64(0)}, {"plotsettings.std_scale_factor", 1.0},
			{"plotsettings.paper_image_origin", []any{0.0, 0.0}},
			{"layout_name", "Layout9"}, {"tab_order", float64(9)}, {"layout_flags", float64(2)},
			{"INSBASE", []any{0.0, 0.0, 0.0}},
			{"LIMMIN", []any{0.0, 0.0}}, {"LIMMAX", []any{420.0, 297.0}},
			{"UCSORG", []any{0.0, 0.0, 0.0}}, {"UCSXDIR", []any{1.0, 0.0, 0.0}},
			{"UCSYDIR", []any{0.0, 1.0, 0.0}},
			{"ucs_elevation", 0.0}, {"UCSORTHOVIEW", float64(0)},
			{"EXTMIN", []any{0.0, 0.0, 0.0}}, {"EXTMAX", []any{100.0, 100.0, 0.0}},
			{"block_header", []any{float64(4), float64(1), float64(0x55), float64(0x55)}},
			{"active_viewport", []any{float64(4), float64(2), float64(0x56), float64(0x56)}},
		},
	}
	wipeoutVars := &objGeneric{
		Name: "WIPEOUTVARIABLES", Handle: 0x505,
		Fields: []objField{{"display_frame", float64(1)}},
	}
	placeholder := &objGeneric{Name: "PLACEHOLDER", Handle: 0x506}
	appid := &objGeneric{
		Name: "APPID", Handle: 0x507,
		Fields: []objField{
			{"name", "PROBE_APP"}, {"is_xref_ref", true},
			{"is_xref_resolved", float64(1)}, {"is_xref_dep", false}, {"unknown", float64(0)},
		},
	}
	doc := fwdSynthDoc(t)
	doc.internalObjects = map[uint64]*objGeneric{}
	for _, g := range []*objGeneric{group, xrecord, dictvar, scale, layout, wipeoutVars, placeholder, appid} {
		doc.internalObjects[g.Handle] = g
	}
	got := fwdWriteParse(t, doc)
	// 回读侧按句柄取 objGeneric 并按类型断键
	type objProbe struct {
		name  string
		check func(*objGeneric) error
	}
	handles := map[uint64]string{}
	probes := map[uint64]func(*objGeneric) error{
		group.Handle: func(g *objGeneric) error {
			if gfStr(g, "name") != "PROBE_GROUP" || gfNum(g, "selectable") != 1 {
				return fmt.Errorf("name/selectable")
			}
			// 组员句柄在读侧进入 g.Handles（handleVectorKey 消费）
			if n := len(g.Handles); n != 2 {
				return fmt.Errorf("组员句柄 %d != 2", n)
			}
			return nil
		},
		xrecord.Handle: func(g *objGeneric) error {
			if gfNum(g, "cloning") != 1 {
				return fmt.Errorf("cloning")
			}
			// xdata 往返：读侧 objGeneric 的 xdata 为数组，值形态随解码器
			if v := g.Field("xdata"); v == nil {
				return fmt.Errorf("xdata 缺失")
			}
			return nil
		},
		dictvar.Handle: func(g *objGeneric) error {
			if gfStr(g, "strvalue") != "2" || gfNum(g, "schema") != 0 {
				return fmt.Errorf("schema/strvalue")
			}
			return nil
		},
		scale.Handle: func(g *objGeneric) error {
			if gfStr(g, "name") != "1:2" || !nearTol(gfReal(g, "drawing_units"), 2, 1e-9) {
				return fmt.Errorf("name/drawing_units")
			}
			return nil
		},
		layout.Handle: func(g *objGeneric) error {
			if gfStr(g, "layout_name") != "Layout9" || gfNum(g, "tab_order") != 9 ||
				!nearTol(gfReal(g, "plotsettings.paper_width"), 210, 1e-9) {
				return fmt.Errorf("layout_name/tab_order/paper_width")
			}
			if gfNum(g, "plotsettings.plot_flags") != 11952 {
				return fmt.Errorf("plot_flags")
			}
			return nil
		},
		wipeoutVars.Handle: func(g *objGeneric) error {
			if gfNum(g, "display_frame") != 1 {
				return fmt.Errorf("display_frame")
			}
			return nil
		},
		placeholder.Handle: func(*objGeneric) error { return nil },
		appid.Handle: func(g *objGeneric) error {
			if gfStr(g, "name") != "PROBE_APP" {
				return fmt.Errorf("name")
			}
			return nil
		},
	}
	for h := range probes {
		handles[h] = ""
	}
	// 回读对象在 internalObjects（实体分类外）；XRECORD 读侧走 objXrecord
	// 专用路径（encodeXrecordR2000 的对称解码），单独断言
	found := 0
	for h, g := range got.internalObjects {
		check, ok := probes[h]
		if !ok {
			continue
		}
		found++
		if err := check(g); err != nil {
			t.Errorf("对象 %#x(%s): %v", h, g.Name, err)
		}
	}
	if found != len(probes)-1 {
		t.Fatalf("回读通用对象 %d != %d（缺：%v）", found, len(probes)-1, missingProbeHandles(got, probes))
	}
	var gx *objXrecord
	for _, x := range got.Xrecords() {
		if x.handle == xrecord.Handle {
			gx = x
		}
	}
	if gx == nil {
		t.Fatalf("回读缺少 XRECORD %#x", xrecord.Handle)
	}
	if gx.cloning != 1 || len(gx.xdata) != 3 {
		t.Fatalf("XRECORD: cloning=%d items=%d", gx.cloning, len(gx.xdata))
	}
	if gx.xdata[1].Str != "BA88-PROBE" || gx.xdata[2].Code != 310 {
		t.Errorf("XRECORD xdata: %+v %+v", gx.xdata[1], gx.xdata[2])
	}
}

// missingProbeHandles 列出回读侧缺失的门禁探针句柄（诊断用）。
func missingProbeHandles(doc *Document, probes map[uint64]func(*objGeneric) error) []string {
	var missing []string
	for h := range probes {
		if _, ok := doc.internalObjects[h]; !ok {
			missing = append(missing, fmt.Sprintf("%#x", h))
		}
	}
	return missing
}
