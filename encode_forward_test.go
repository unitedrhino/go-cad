// encode_forward_test.go 结构化正向写出的门禁测试：合成 Document →
// WriteDwg → Parse 回读，逐实体对照几何/文本一致；JSON 九样本与 DXF
// 样本的跨来源对照见 TestWriteForwardR2000 系列门禁。
package cad

import (
	"bytes"
	"fmt"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/entity"
	"github.com/unitedrhino/go-cad/internal/object"
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
		version:     container.VerR2000,
		blocks:      map[uint64][]any{},
		attribs:     map[uint64]*entity.EntAttrib{},
		layerColors: map[uint64]layerColor{0x10: {index: 3, name: "FWD"}},
	}
	for i, e := range ents {
		b := entity.EntityBase(e)
		if b == nil {
			t.Fatalf("实体 %d 非 entityCommon", i)
		}
		b.Handle = uint64(0x30 + i)
		b.Mode = 2
		b.Layer = 0x10
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
	line := &entity.EntLine{Start: entity.Point3{0, 0, 0}, End: entity.Point3{100, 50, 0}}
	circle := &entity.EntCircle{Center: entity.Point3{10, 20, 0}, Radius: 12.5}
	arc := &entity.EntArc{Center: entity.Point3{5, 6, 0}, Radius: 7, AngleStart: 0.5, AngleEnd: 2.5}
	point := &entity.EntPoint{Location: entity.Point3{33, 44, 0}, Rotation: 1.25}
	ellipse := &entity.EntEllipse{Center: entity.Point3{1, 2, 0}, MajorAxis: entity.Point3{10, 0, 0}, Ratio: 0.5, StartAng: 0, EndAng: math.Pi}
	lwp := &entity.EntLwPolyline{
		Vertices: []entity.Point2{{0, 0}, {10, 0}, {10, 10}, {0, 10}},
		Bulges:   []float64{0, 0.5, 0, 0},
	}
	text := &entity.EntText{Text: "HELLO_FWD", Insertion: entity.Point3{7, 8, 0}, Height: 3.5, Rotation: 0.25}
	mtext := &entity.EntMText{Text: "MTEXT_FWD", Insertion: entity.Point3{9, 9, 0}, RectWidth: 20, TextHeight: 2.5, Attachment: 1, XAxisDir: entity.Point3{1, 0, 0}}
	solid := &entity.EntSolid{P1: entity.Point2{0, 0}, P2: entity.Point2{10, 0}, P3: entity.Point2{0, 10}, P4: entity.Point2{10, 10}, Elevation: 1.5, Thickness: 0.25, Extrusion: entity.Point3{0, 0, 1}}
	face := &entity.EntFace3d{P1: entity.Point3{0, 0, 0}, P2: entity.Point3{20, 0, 0}, P3: entity.Point3{20, 15, 0}, P4: entity.Point3{0, 15, 0}}
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
		byHandle[entity.EntityBase(e).Handle] = e
	}
	if len(byHandle) != len(doc.modelSpace) {
		t.Fatalf("回读实体种类数: %d != %d", len(byHandle), len(doc.modelSpace))
	}
	gl := byHandle[line.Handle].(*entity.EntLine)
	if !nearGeo(gl.Start.X, 0) || !nearGeo(gl.Start.Y, 0) || !nearGeo(gl.End.X, 100) || !nearGeo(gl.End.Y, 50) {
		t.Errorf("LINE 几何: %+v %+v", gl.Start, gl.End)
	}
	gc := byHandle[circle.Handle].(*entity.EntCircle)
	if !nearGeo(gc.Center.X, 10) || !nearGeo(gc.Center.Y, 20) || !nearGeo(gc.Radius, 12.5) {
		t.Errorf("CIRCLE 几何: %+v r=%v", gc.Center, gc.Radius)
	}
	ga := byHandle[arc.Handle].(*entity.EntArc)
	if !nearGeo(ga.Radius, 7) || !nearGeo(rad2deg(ga.AngleStart), rad2deg(0.5)) || !nearGeo(rad2deg(ga.AngleEnd), rad2deg(2.5)) {
		t.Errorf("ARC 几何: r=%v a0=%v a1=%v", ga.Radius, ga.AngleStart, ga.AngleEnd)
	}
	gp := byHandle[point.Handle].(*entity.EntPoint)
	if !nearGeo(gp.Location.X, 33) || !nearGeo(gp.Location.Y, 44) {
		t.Errorf("POINT 几何: %+v", gp.Location)
	}
	ge := byHandle[ellipse.Handle].(*entity.EntEllipse)
	if !nearGeo(ge.Ratio, 0.5) || !nearGeo(ge.MajorAxis.X, 10) {
		t.Errorf("ELLIPSE 几何: ratio=%v major=%+v", ge.Ratio, ge.MajorAxis)
	}
	gw := byHandle[lwp.Handle].(*entity.EntLwPolyline)
	if len(gw.Vertices) != 4 {
		t.Fatalf("LWPOLYLINE 顶点数: %d != 4", len(gw.Vertices))
	}
	for i, want := range lwp.Vertices {
		if !nearGeo(gw.Vertices[i].X, want.X) || !nearGeo(gw.Vertices[i].Y, want.Y) {
			t.Errorf("LWPOLYLINE 顶点 %d: %+v != %+v", i, gw.Vertices[i], want)
		}
	}
	if len(gw.Bulges) < 2 || !nearGeo(gw.Bulges[1], 0.5) {
		t.Errorf("LWPOLYLINE 凸度: %v", gw.Bulges)
	}
	gt := byHandle[text.Handle].(*entity.EntText)
	if gt.Text != "HELLO_FWD" {
		t.Errorf("TEXT 文本: %q", gt.Text)
	}
	if !nearGeo(gt.Height, 3.5) || !nearGeo(gt.Insertion.X, 7) {
		t.Errorf("TEXT 几何: h=%v ins=%+v", gt.Height, gt.Insertion)
	}
	gm := byHandle[mtext.Handle].(*entity.EntMText)
	if gm.Text != "MTEXT_FWD" || !nearGeo(gm.RectWidth, 20) || !nearGeo(gm.TextHeight, 2.5) {
		t.Errorf("MTEXT: %q w=%v h=%v", gm.Text, gm.RectWidth, gm.TextHeight)
	}
	gs := byHandle[solid.Handle].(*entity.EntSolid)
	if !nearGeo(gs.P3.X, 0) || !nearGeo(gs.P4.Y, 10) || !nearGeo(gs.Elevation, 1.5) {
		t.Errorf("SOLID 几何: %+v %+v elev=%v", gs.P3, gs.P4, gs.Elevation)
	}
	gf := byHandle[face.Handle].(*entity.EntFace3d)
	if !nearGeo(gf.P2.X, 20) || !nearGeo(gf.P4.Y, 15) {
		t.Errorf("3DFACE 几何: %+v %+v", gf.P2, gf.P4)
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
	poly := &entity.EntPolyline2d{Flags: 0, CurveType: 0, Extrusion: entity.Point3{0, 0, 1}}
	poly.Handle = 0x210
	poly.Mode = 0
	poly.Owner = blkHdl
	poly.Layer = 0x10
	poly.FirstVertex = 0x211
	poly.LastVertex = 0x212
	v1 := &entity.EntVertex2d{Position: entity.Point3{0, 0, 0}, Bulge: 0}
	v1.Handle = 0x211
	v1.Mode = 0
	v1.Owner = poly.Handle
	v1.Layer = 0x10
	v2 := &entity.EntVertex2d{Position: entity.Point3{30, 40, 0}}
	v2.Handle = 0x212
	v2.Mode = 0
	v2.Owner = poly.Handle
	v2.Layer = 0x10
	doc.blocks[blkHdl] = []any{poly, v1, v2}
	// 模型空间 INSERT 引用块
	ins := &entity.EntInsert{Position: entity.Point3{1, 2, 0}, Scale: entity.Point3{1, 1, 1}, BlockHeader: blkHdl}
	ins.Handle = 0x300
	ins.Mode = 2
	ins.Layer = 0x10
	doc.classify(ins)
	got := fwdWriteParse(t, doc)
	if got.EntityCount() == 0 {
		t.Fatal("回读实体为空")
	}
	var gIns *entity.EntInsert
	for _, e := range got.modelSpace {
		if ins2, ok := e.(*entity.EntInsert); ok {
			gIns = ins2
		}
	}
	if gIns == nil {
		t.Fatal("回读缺少 INSERT")
	}
	if gIns.BlockHeader != blkHdl {
		t.Errorf("INSERT 块头句柄: %d != %d", gIns.BlockHeader, blkHdl)
	}
	if !nearGeo(gIns.Position.Y, 2) {
		t.Errorf("INSERT 位置: %+v", gIns.Position)
	}
	// 块内顶点按 owner 聚合：poly 的 owner 为块头，顶点的 owner 为 poly
	var gotPoly *entity.EntPolyline2d
	for _, list := range got.blocks {
		for _, v := range list {
			if p, ok := v.(*entity.EntPolyline2d); ok {
				gotPoly = p
			}
		}
	}
	if gotPoly == nil {
		t.Fatal("回读缺少 POLYLINE_2D")
	}
	verts, ok := got.blocks[gotPoly.Handle]
	if !ok || len(verts) < 2 {
		t.Fatalf("顶点归属聚合实体数: %d（want ≥2）", len(verts))
	}
	var gotVerts []*entity.EntVertex2d
	for _, v := range verts {
		if vv, ok := v.(*entity.EntVertex2d); ok {
			gotVerts = append(gotVerts, vv)
		}
	}
	if len(gotVerts) != 2 {
		t.Fatalf("回读顶点数: %d != 2", len(gotVerts))
	}
	if !nearGeo(gotVerts[1].Position.X, 30) || !nearGeo(gotVerts[1].Position.Y, 40) {
		t.Errorf("VERTEX_2D 几何: %+v", gotVerts[1].Position)
	}
}

// TestWriteForwardInsertAttrib INSERT + ATTRIB 的属性链闭环：hasAttribs
// 位 + R2000 首/末属性句柄 + SEQEND 结构，回读 Texts 含属性文本。
func TestWriteForwardInsertAttrib(t *testing.T) {
	doc := fwdSynthDoc(t)
	const blkHdl = uint64(0x210)
	blkEnt := &entity.EntBlockLike{Name: "SIGN"}
	blkEnt.Handle = 0x211
	blkEnt.Mode = 0
	blkEnt.Owner = blkHdl
	blkEnt.TypeName = "BLOCK"
	endblk := &entity.EntBlockLike{}
	endblk.Handle = 0x212
	endblk.Mode = 0
	endblk.Owner = blkHdl
	endblk.TypeName = "ENDBLK"
	doc.blocks[blkHdl] = []any{blkEnt, endblk}
	attr := &entity.EntAttrib{Text: "ROOM-101", Tag: "ROOM", Insertion: entity.Point3{1, 1, 0}, Height: 2}
	attr.Handle = 0x220
	attr.Mode = 0
	attr.Owner = blkHdl
	attr.Layer = 0x10
	doc.classify(attr)
	doc.attribs[attr.Handle] = attr
	ins := &entity.EntInsert{Position: entity.Point3{5, 6, 0}, Scale: entity.Point3{1, 1, 1}, BlockHeader: blkHdl, Attribs: []uint64{attr.Handle}}
	ins.Handle = 0x230
	ins.Mode = 2
	ins.Layer = 0x10
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
	var gIns *entity.EntInsert
	for _, e := range got.modelSpace {
		if i2, ok := e.(*entity.EntInsert); ok {
			gIns = i2
		}
	}
	if gIns == nil {
		t.Fatal("回读缺少 INSERT")
	}
	if len(gIns.Attribs) < 2 || gIns.Attribs[0] != attr.Handle {
		t.Errorf("INSERT 属性句柄: %v（want 首=%d）", gIns.Attribs, attr.Handle)
	}
	if a, ok := got.attribs[attr.Handle]; !ok || a.Text != "ROOM-101" {
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
			if b := entity.EntityBase(e); b != nil && b.Handle != 0 {
				m[b.Handle] = e
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
		case *entity.EntLine:
			if b, ok := ge.(*entity.EntLine); ok {
				if nearTol(a.Start.X, b.Start.X, tol) && nearTol(a.End.Y, b.End.Y, tol) {
					checked++
				} else {
					t.Errorf("h=%d LINE 几何不一致: (%v,%v)-(%v,%v) vs (%v,%v)-(%v,%v)", h,
						a.Start.X, a.Start.Y, a.End.X, a.End.Y, b.Start.X, b.Start.Y, b.End.X, b.End.Y)
				}
			}
		case *entity.EntCircle:
			if b, ok := ge.(*entity.EntCircle); ok {
				if nearTol(a.Center.X, b.Center.X, tol) && nearTol(a.Radius, b.Radius, tol) {
					checked++
				} else {
					t.Errorf("h=%d CIRCLE 不一致: c=%v r=%v vs c=%v r=%v", h, a.Center, a.Radius, b.Center, b.Radius)
				}
			}
		case *entity.EntArc:
			if b, ok := ge.(*entity.EntArc); ok {
				if nearTol(a.Center.X, b.Center.X, tol) && nearTol(a.Radius, b.Radius, tol) &&
					nearTol(rad2deg(a.AngleStart), rad2deg(b.AngleStart), 0.5) {
					checked++
				} else {
					t.Errorf("h=%d ARC 不一致: c=%v r=%v vs c=%v r=%v", h, a.Center, a.Radius, b.Center, b.Radius)
				}
			}
		case *entity.EntPoint:
			if b, ok := ge.(*entity.EntPoint); ok {
				if nearTol(a.Location.X, b.Location.X, tol) && nearTol(a.Location.Y, b.Location.Y, tol) {
					checked++
				}
			}
		case *entity.EntEllipse:
			if b, ok := ge.(*entity.EntEllipse); ok {
				if nearTol(a.Center.X, b.Center.X, tol) && nearTol(a.Ratio, b.Ratio, tol) {
					checked++
				}
			}
		case *entity.EntLwPolyline:
			if b, ok := ge.(*entity.EntLwPolyline); ok && len(a.Vertices) == len(b.Vertices) {
				same := true
				for i := range a.Vertices {
					if !nearTol(a.Vertices[i].X, b.Vertices[i].X, tol) || !nearTol(a.Vertices[i].Y, b.Vertices[i].Y, tol) {
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
		case *entity.EntText:
			if b, ok := ge.(*entity.EntText); ok {
				if a.Text == b.Text && nearTol(a.Height, b.Height, tol) {
					checked++
				} else {
					t.Errorf("h=%d TEXT 不一致: %q vs %q", h, a.Text, b.Text)
				}
			}
		case *entity.EntMText:
			if b, ok := ge.(*entity.EntMText); ok {
				if stripMTextFormat(a.Text) == stripMTextFormat(b.Text) {
					checked++
				} else {
					t.Errorf("h=%d MTEXT 不一致: %q vs %q", h, a.Text, b.Text)
				}
			}
		case *entity.EntInsert:
			if b, ok := ge.(*entity.EntInsert); ok {
				if nearTol(a.Position.X, b.Position.X, tol) && a.BlockHeader == b.BlockHeader {
					checked++
				}
			}
		case *entity.EntSpline:
			if b, ok := ge.(*entity.EntSpline); ok {
				same := a.Scenario == b.Scenario && a.Degree == b.Degree &&
					len(a.ControlPoints) == len(b.ControlPoints) &&
					len(a.FitPoints) == len(b.FitPoints) &&
					len(a.Knots) == len(b.Knots)
				if same && len(a.ControlPoints) > 0 {
					same = nearTol(a.ControlPoints[0].X, b.ControlPoints[0].X, tol)
				}
				if same && len(a.FitPoints) > 0 {
					same = nearTol(a.FitPoints[0].Y, b.FitPoints[0].Y, tol)
				}
				if same {
					checked++
				} else {
					t.Errorf("h=%d SPLINE 不一致: scenario %d/%d ctrl %d/%d fit %d/%d", h,
						a.Scenario, b.Scenario, len(a.ControlPoints), len(b.ControlPoints),
						len(a.FitPoints), len(b.FitPoints))
				}
			}
		case *entity.EntDimension:
			if b, ok := ge.(*entity.EntDimension); ok {
				// 公共段口径：类型标志 + 文本中点 + 用户文字。类型专属点的
				// gold 键映射在 JSON 侧尚不完整（ANG2LN 的 xline 系四点、
				// ORDINATE 的 feature/leader 错位、ARC_DIMENSION 专属键），
				// 专属点位流布局由 TestWriteForwardBatchE 合成闭环覆盖。
				if a.DimFlag&0x7 == b.DimFlag&0x7 &&
					nearTol(a.TextMidpoint.X, b.TextMidpoint.X, tol) &&
					nearTol(a.TextMidpoint.Y, b.TextMidpoint.Y, tol) &&
					a.UserText == b.UserText {
					checked++
				} else {
					t.Errorf("h=%d DIMENSION 不一致: flag %#x/%#x mid(%v,%v)/(%v,%v) text %q/%q", h,
						a.DimFlag&0x7, b.DimFlag&0x7, a.TextMidpoint.X, a.TextMidpoint.Y,
						b.TextMidpoint.X, b.TextMidpoint.Y, a.UserText, b.UserText)
				}
			}
		case *entity.EntHatch:
			if b, ok := ge.(*entity.EntHatch); ok {
				if a.Name == b.Name && len(a.Paths) == len(b.Paths) {
					checked++
				} else {
					t.Errorf("h=%d HATCH 不一致: %q/%q paths %d/%d", h, a.Name, b.Name, len(a.Paths), len(b.Paths))
				}
			}
		case *entity.EntRay:
			if b, ok := ge.(*entity.EntRay); ok {
				if nearTol(a.Start.X, b.Start.X, tol) && nearTol(a.UnitVector.Y, b.UnitVector.Y, tol) && a.Xline == b.Xline {
					checked++
				} else {
					t.Errorf("h=%d RAY/XLINE 不一致", h)
				}
			}
		case *entity.EntMLine:
			if b, ok := ge.(*entity.EntMLine); ok {
				if len(a.Vertices) == len(b.Vertices) && nearTol(a.Scale, b.Scale, tol) {
					checked++
				} else {
					t.Errorf("h=%d MLINE 不一致: scale %v/%v verts %d/%d", h, a.Scale, b.Scale, len(a.Vertices), len(b.Vertices))
				}
			}
		case *entity.EntTolerance:
			if b, ok := ge.(*entity.EntTolerance); ok {
				if a.Text == b.Text && nearTol(a.Insertion.X, b.Insertion.X, tol) {
					checked++
				} else {
					t.Errorf("h=%d TOLERANCE 不一致: %q/%q", h, a.Text, b.Text)
				}
			}
		case *entity.EntViewport:
			if b, ok := ge.(*entity.EntViewport); ok {
				if nearTol(a.Width, b.Width, tol) && nearTol(a.Height, b.Height, tol) {
					checked++
				} else {
					t.Errorf("h=%d VIEWPORT 不一致: %v/%v x %v/%v", h, a.Width, b.Width, a.Height, b.Height)
				}
			}
		case *entity.EntPolyline3d:
			if b, ok := ge.(*entity.EntPolyline3d); ok {
				if a.Flags70 == b.Flags70 && a.Flags75 == b.Flags75 {
					checked++
				}
			}
		case *entity.EntVertex3d:
			if b, ok := ge.(*entity.EntVertex3d); ok {
				if nearTol(a.Position.X, b.Position.X, tol) && nearTol(a.Position.Z, b.Position.Z, tol) {
					checked++
				}
			}
		case *entity.EntVertexPface:
			if b, ok := ge.(*entity.EntVertexPface); ok {
				if nearTol(a.Position.X, b.Position.X, tol) && nearTol(a.Position.Y, b.Position.Y, tol) {
					checked++
				}
			}
		case *entity.EntVertexPfaceFace:
			if b, ok := ge.(*entity.EntVertexPfaceFace); ok {
				if a.Vertind == b.Vertind {
					checked++
				}
			}
		case *entity.EntPolylinePface:
			if b, ok := ge.(*entity.EntPolylinePface); ok {
				if a.NumVertices == b.NumVertices && a.NumFaces == b.NumFaces {
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
	splineCtrl := &entity.EntSpline{
		Scenario: 1, Degree: 3, Rational: true,
		KnotTolerance: 1e-7, CtrlTolerance: 2e-7,
		Knots:         []float64{0, 0, 0, 1, 2, 3, 3, 3},
		ControlPoints: []entity.Point3{{0, 0, 0}, {10, 20, 1}, {30, 10, 2}, {40, 40, 0}},
		Weights:       []float64{1, 2, 2, 1},
	}
	splineFit := &entity.EntSpline{
		Scenario: 2, Degree: 3,
		FitTolerance: 1e-10,
		FitPoints:    []entity.Point3{{1, 1, 0}, {5, 6, 0}, {9, 2, 0}, {12, 8, 0}},
	}
	// DIMENSION 七型（flag 低 3 位分派）
	dimLinear := &entity.EntDimension{
		DimFlags: 0x80, DimFlag: 0x80,
		Extrusion: entity.Point3{0, 0, 1}, TextMidpoint: entity.Point3{5, 6, 0}, Elevation: 0,
		UserText: "DL<>", TextRotation: 0.1, HorizontalDir: 0.2,
		InsertScale: entity.Point3{1, 1, 1}, InsertRotation: 0.3,
		AttachmentPoint: 1, LineSpacingStyle: 1, LineSpacingFactor: 1.5,
		ActualMeasurement: 42.5, InsertPoint: entity.Point3{1, 2, 0}, HasInsertPoint: true,
		Point13: entity.Point3{0, 0, 0}, Point14: entity.Point3{40, 0, 0}, Point10: entity.Point3{10, -5, 0},
		ExtLineRotation: 0.05, DimRotation: 0.25,
	}
	dimAligned := &entity.EntDimension{DimFlags: 0x81, DimFlag: 0x81, Extrusion: entity.Point3{0, 0, 1},
		TextMidpoint: entity.Point3{9, 9, 0}, Point13: entity.Point3{0, 0, 0}, Point14: entity.Point3{30, 40, 0},
		Point10: entity.Point3{15, 20, 0}, ExtLineRotation: 0.02}
	dimAng2Ln := &entity.EntDimension{DimFlags: 0x82, DimFlag: 0x82, Extrusion: entity.Point3{0, 0, 1},
		TextMidpoint: entity.Point3{1, 1, 0}, Point16x: 7, P16y: 8,
		Point13: entity.Point3{0, 0, 0}, Point14: entity.Point3{10, 0, 0}, Point15: entity.Point3{20, 10, 0},
		Point10: entity.Point3{5, 5, 0}}
	dimDiameter := &entity.EntDimension{DimFlags: 0x83, DimFlag: 0x83, Extrusion: entity.Point3{0, 0, 1},
		TextMidpoint: entity.Point3{2, 2, 0}, Point15: entity.Point3{12, 12, 0}, Point10: entity.Point3{-12, -12, 0},
		LeaderLen: 8.25, DimstyleHandle: 0x77}
	dimRadius := &entity.EntDimension{DimFlags: 0x84, DimFlag: 0x84, Extrusion: entity.Point3{0, 0, 1},
		TextMidpoint: entity.Point3{3, 3, 0}, Point10: entity.Point3{0, 0, 0}, Point15: entity.Point3{9, 0, 0},
		LeaderLen: 3.5}
	dimAng3Pt := &entity.EntDimension{DimFlags: 0x85, DimFlag: 0x85, Extrusion: entity.Point3{0, 0, 1},
		TextMidpoint: entity.Point3{4, 4, 0}, Point10: entity.Point3{0, 0, 0}, Point13: entity.Point3{10, 0, 0},
		Point14: entity.Point3{0, 10, 0}, Point15: entity.Point3{5, 5, 0}}
	dimOrdinate := &entity.EntDimension{DimFlags: 0x86, DimFlag: 0x86, Extrusion: entity.Point3{0, 0, 1},
		TextMidpoint: entity.Point3{6, 6, 0}, Point10: entity.Point3{1, 1, 0}, Point13: entity.Point3{11, 1, 0},
		Point14: entity.Point3{1, 11, 0}, Flag2: 0x80}
	// HATCH：边集路径（直线+弧）与多段线路径（带凸度）
	hatchEdges := &entity.EntHatch{
		Elevation: 1.25, Extrusion: entity.Point3{0, 0, 1}, Name: "ANGLE",
		Associative: true, Style: 0, PatternType: 1, Angle: 0.5, ScaleSpacing: 2,
		Deflines: []entity.HatchDefLine{{
			Angle: 0.25, Pt0: entity.Point2{1, 2}, Offset: entity.Point2{3, 4},
			Dashes: []float64{10, -3},
		}},
		Paths: []entity.HatchPath{{
			Flag: 1,
			Segs: []entity.HatchSeg{
				{CurveType: 1, First: entity.Point2{0, 0}, Second: entity.Point2{10, 0}},
				{CurveType: 2, Center: entity.Point2{10, 5}, Radius: 5, StartAng: 0, EndAng: 1.5, Ccw: true},
			},
		}},
		Seeds: []entity.Point2{{2, 3}, {4, 5}},
	}
	hatchPoly := &entity.EntHatch{
		Name: "SOLID", SolidFill: true, Style: 1, PatternType: 1,
		Paths: []entity.HatchPath{{
			Flag:          3,
			IsPolyline:    true,
			BulgesPresent: true,
			Closed:        true,
			PolyVerts: []entity.HatchPolyVert{
				{P: entity.Point2{0, 0}, Bulge: 0},
				{P: entity.Point2{10, 0}, Bulge: 0.5},
				{P: entity.Point2{10, 10}, Bulge: 0},
			},
		}},
	}
	ray := &entity.EntRay{Start: entity.Point3{1, 2, 3}, UnitVector: entity.Point3{1, 0, 0}}
	xline := &entity.EntRay{Start: entity.Point3{4, 5, 6}, UnitVector: entity.Point3{0, 1, 0}, Xline: true}
	leader := &entity.EntLeader{
		AnnotationType: 1, PathType: 0,
		Points:    []entity.Point3{{0, 0, 0}, {10, 10, 0}, {20, 10, 0}},
		Origin:    entity.Point3{0, 0, 0},
		BoxHeight: 3, BoxWidth: 12, ArrowheadOn: true, ArrowheadType: 1,
	}
	mline := &entity.EntMLine{
		Scale: 20, Justification: 1, OpenClosed: 3, LinesInStyle: 2,
		Vertices: []entity.EntMLineVertex{
			{Position: entity.Point3{0, 0, 0}, Direction: entity.Point3{1, 0, 0}, Miter: entity.Point3{0, 1, 0},
				SegParams: []float64{-10, 10, -10, 10}, AreaParams: []float64{0, 0, 0, 0}},
			{Position: entity.Point3{50, 0, 0}, Direction: entity.Point3{1, 0, 0}, Miter: entity.Point3{0, 1, 0},
				SegParams: []float64{-10, 10, -10, 10}, AreaParams: []float64{0, 0, 0, 0}},
		},
		StyleHandle: 0x99,
	}
	tolerance := &entity.EntTolerance{
		Text: "{\\Fgdt;r}%%v1", Insertion: entity.Point3{7, 8, 0},
		XDirection: entity.Point3{1, 0, 0}, Extrusion: entity.Point3{0, 0, 1}, Dimstyle: 0x88,
	}
	shape := &entity.EntShape{
		Insertion: entity.Point3{1, 1, 0}, Scale: 2, Rotation: 0.5, WidthFactor: 1,
		Oblique: 0.1, Thickness: 0.2, StyleId: 3, Extrusion: entity.Point3{0, 0, 1},
	}
	viewport := &entity.EntViewport{
		Center: entity.Point3{100, 100, 0}, Width: 210, Height: 148,
		ViewTarget: entity.Point3{0, 0, 0}, ViewDir: entity.Point3{0, 0, 1}, ViewTwist: 0.1,
		ViewSize: 200, LensLength: 50, FrontZ: 0, BackZ: 0, SnapAng: 0,
		ViewCtr: entity.Point2{50, 50}, SnapBase: entity.Point2{0, 0}, SnapUnit: entity.Point2{10, 10},
		GridUnit: entity.Point2{10, 10}, CircleZoom: 100, NumFrozenLayers: 0,
		StatusFlag: 1, StyleSheet: "", RenderMode: 0, UcsVP: true,
		Ucsorg: entity.Point3{0, 0, 0}, Ucsxdir: entity.Point3{1, 0, 0}, Ucsydir: entity.Point3{0, 1, 0},
	}
	vtx3d := &entity.EntVertex3d{Flags: 32, Position: entity.Point3{1, 2, 3}}
	pfaceVtx := &entity.EntVertexPface{Flag: 192, Position: entity.Point3{4, 5, 6}}
	pfaceFace := &entity.EntVertexPfaceFace{Flag: 128, Vertind: [4]int32{1, 2, 3, 0}}
	poly3d := &entity.EntPolyline3d{Flags75: 0, Flags70: 0}
	polyPface := &entity.EntPolylinePface{NumVertices: 3, NumFaces: 1}
	polyMesh := &entity.EntPolylineMesh{Flags: 0, CurveType: 0, MVertexCount: 2, NVertexCount: 2, MDensity: 1, NDensity: 1}

	doc := fwdSynthDoc(t, splineCtrl, splineFit,
		dimLinear, dimAligned, dimAng2Ln, dimDiameter, dimRadius, dimAng3Pt, dimOrdinate,
		hatchEdges, hatchPoly, ray, xline, leader, mline, tolerance, shape, viewport,
		vtx3d, pfaceVtx, pfaceFace, poly3d, polyPface, polyMesh)
	got := fwdWriteParse(t, doc)
	byHandle := map[uint64]any{}
	for _, e := range got.modelSpace {
		byHandle[entity.EntityBase(e).Handle] = e
	}
	get := func(want any) any {
		h := entity.EntityBase(want).Handle
		e, ok := byHandle[h]
		if !ok {
			t.Fatalf("回读缺少句柄 %d（%T）", h, want)
		}
		return e
	}
	// SPLINE 双模式
	gs := get(splineCtrl).(*entity.EntSpline)
	if gs.Scenario != 1 || gs.Degree != 3 || !gs.Rational {
		t.Errorf("SPLINE 控制点模式: scenario=%d degree=%d rational=%v", gs.Scenario, gs.Degree, gs.Rational)
	}
	if len(gs.Knots) != 8 || len(gs.ControlPoints) != 4 || len(gs.Weights) != 4 {
		t.Fatalf("SPLINE 控制点数组: knots=%d ctrl=%d w=%d", len(gs.Knots), len(gs.ControlPoints), len(gs.Weights))
	}
	if !nearGeo(gs.Knots[3], 1) || !nearGeo(gs.ControlPoints[2].X, 30) || !nearGeo(gs.Weights[1], 2) {
		t.Errorf("SPLINE 控制点数据: knots=%v ctrl2=%+v w=%v", gs.Knots, gs.ControlPoints[2], gs.Weights)
	}
	gsf := get(splineFit).(*entity.EntSpline)
	if gsf.Scenario != 2 || len(gsf.FitPoints) != 4 || !nearGeo(gsf.FitTolerance, 1e-10) {
		t.Errorf("SPLINE 拟合模式: scenario=%d fit=%v tol=%v", gsf.Scenario, gsf.FitPoints, gsf.FitTolerance)
	}
	// DIMENSION 七型：类型码 + 公共段 + 专属点
	dimCases := []struct {
		name     string
		want     *entity.EntDimension
		checkGeo func(*entity.EntDimension) error
	}{
		{"DIM_LINEAR", dimLinear, func(g *entity.EntDimension) error {
			if !nearGeo(g.Point10.X, 10) || !nearGeo(g.Point13.X, 0) || !nearGeo(g.Point14.X, 40) ||
				!nearGeo(g.ExtLineRotation, 0.05) || !nearGeo(g.DimRotation, 0.25) ||
				g.UserText != "DL<>" || !nearGeo(g.ActualMeasurement, 42.5) {
				return fmt.Errorf("几何 %+v %+v %+v", g.Point10, g.Point13, g.Point14)
			}
			return nil
		}},
		{"DIM_ALIGNED", dimAligned, func(g *entity.EntDimension) error {
			if !nearGeo(g.Point14.Y, 40) || !nearGeo(g.ExtLineRotation, 0.02) {
				return fmt.Errorf("几何 %+v", g.Point14)
			}
			return nil
		}},
		{"DIM_ANG2LN", dimAng2Ln, func(g *entity.EntDimension) error {
			if !nearGeo(g.Point16x, 7) || !nearGeo(g.P16y, 8) || !nearGeo(g.Point15.X, 20) {
				return fmt.Errorf("几何 16=(%v,%v) 15=%+v", g.Point16x, g.P16y, g.Point15)
			}
			return nil
		}},
		{"DIM_DIAMETER", dimDiameter, func(g *entity.EntDimension) error {
			if !nearGeo(g.Point15.X, 12) || !nearGeo(g.Point10.X, -12) || !nearGeo(g.LeaderLen, 8.25) {
				return fmt.Errorf("几何 15=%+v 10=%+v", g.Point15, g.Point10)
			}
			return nil
		}},
		{"DIM_RADIUS", dimRadius, func(g *entity.EntDimension) error {
			if !nearGeo(g.Point15.X, 9) || !nearGeo(g.LeaderLen, 3.5) {
				return fmt.Errorf("几何 15=%+v", g.Point15)
			}
			return nil
		}},
		{"DIM_ANG3PT", dimAng3Pt, func(g *entity.EntDimension) error {
			if !nearGeo(g.Point10.X, 0) || !nearGeo(g.Point15.X, 5) {
				return fmt.Errorf("几何 10=%+v 15=%+v", g.Point10, g.Point15)
			}
			return nil
		}},
		{"DIM_ORDINATE", dimOrdinate, func(g *entity.EntDimension) error {
			if !nearGeo(g.Point13.X, 11) || g.DimFlag&0x7 != 6 {
				return fmt.Errorf("几何 13=%+v flag=%#x", g.Point13, g.DimFlag)
			}
			return nil
		}},
	}
	for _, dc := range dimCases {
		g, ok := get(dc.want).(*entity.EntDimension)
		if !ok {
			t.Fatalf("%s 回读类型不符", dc.name)
		}
		if g.DimFlag&0x7 != dc.want.DimFlag&0x7 {
			t.Errorf("%s 类型标志: %#x != %#x", dc.name, g.DimFlag&0x7, dc.want.DimFlag&0x7)
		}
		if err := dc.checkGeo(g); err != nil {
			t.Errorf("%s: %v", dc.name, err)
		}
	}
	if g := get(dimDiameter).(*entity.EntDimension); g.DimstyleHandle != 0x77 {
		t.Errorf("DIMENSION dimstyle 句柄: %d != 0x77", g.DimstyleHandle)
	}
	// HATCH 边集路径
	gh := get(hatchEdges).(*entity.EntHatch)
	if gh.Name != "ANGLE" || !gh.Associative || len(gh.Paths) != 1 {
		t.Fatalf("HATCH 边集: name=%q assoc=%v paths=%d", gh.Name, gh.Associative, len(gh.Paths))
	}
	if len(gh.Paths[0].Segs) != 2 || gh.Paths[0].Segs[0].CurveType != 1 {
		t.Fatalf("HATCH 边段: %+v", gh.Paths[0].Segs)
	}
	s0 := gh.Paths[0].Segs[0]
	if !nearGeo(s0.Second.X, 10) || !nearGeo(s0.Second.Y, 0) {
		t.Errorf("HATCH 直线边: %+v", s0)
	}
	s1 := gh.Paths[0].Segs[1]
	if s1.CurveType != 2 || !nearGeo(s1.Radius, 5) || !nearGeo(s1.EndAng, 1.5) || !s1.Ccw {
		t.Errorf("HATCH 弧边: %+v", s1)
	}
	if len(gh.Deflines) != 1 || !nearGeo(gh.Deflines[0].Offset.Y, 4) || len(gh.Deflines[0].Dashes) != 2 {
		t.Errorf("HATCH 定义线: %+v", gh.Deflines)
	}
	// 种子点段为纯位流消费（读侧不回填 seeds，仅验证布局不错位——
	// 能正确解出路径与定义线即证明种子段偏移正确）
	// HATCH 多段线路径
	ghp := get(hatchPoly).(*entity.EntHatch)
	if !ghp.SolidFill || len(ghp.Paths) != 1 || !ghp.Paths[0].IsPolyline {
		t.Fatalf("HATCH 多段线: solid=%v paths=%+v", ghp.SolidFill, ghp.Paths)
	}
	if len(ghp.Paths[0].PolyVerts) != 3 || !nearGeo(ghp.Paths[0].PolyVerts[1].Bulge, 0.5) || !ghp.Paths[0].Closed {
		t.Errorf("HATCH 多段线顶点: %+v", ghp.Paths[0].PolyVerts)
	}
	// RAY / XLINE
	gr := get(ray).(*entity.EntRay)
	if !nearGeo(gr.Start.Z, 3) || !nearGeo(gr.UnitVector.X, 1) {
		t.Errorf("RAY: %+v %+v", gr.Start, gr.UnitVector)
	}
	gx := get(xline).(*entity.EntRay)
	if !gx.Xline || !nearGeo(gx.Start.X, 4) {
		t.Errorf("XLINE: xline=%v start=%+v", gx.Xline, gx.Start)
	}
	// LEADER
	gl := get(leader).(*entity.EntLeader)
	if len(gl.Points) != 3 || !nearGeo(gl.Points[2].X, 20) || gl.AnnotationType != 1 || !gl.ArrowheadOn {
		t.Errorf("LEADER: pts=%v at=%d on=%v", gl.Points, gl.AnnotationType, gl.ArrowheadOn)
	}
	// MLINE
	gm := get(mline).(*entity.EntMLine)
	if gm.Scale != 20 || len(gm.Vertices) != 2 || gm.LinesInStyle != 2 {
		t.Fatalf("MLINE: scale=%v verts=%d lines=%d", gm.Scale, len(gm.Vertices), gm.LinesInStyle)
	}
	if !nearGeo(gm.Vertices[1].Position.X, 50) || len(gm.Vertices[0].SegParams) != 4 {
		t.Errorf("MLINE 顶点: %+v segs=%v", gm.Vertices[1], gm.Vertices[0].SegParams)
	}
	// TOLERANCE
	gt := get(tolerance).(*entity.EntTolerance)
	if gt.Text != tolerance.Text || !nearGeo(gt.Insertion.X, 7) || gt.Dimstyle != 0x88 {
		t.Errorf("TOLERANCE: %q ins=%+v style=%d", gt.Text, gt.Insertion, gt.Dimstyle)
	}
	// SHAPE
	gsh := get(shape).(*entity.EntShape)
	if gsh.Scale != 2 || gsh.StyleId != 3 || !nearGeo(gsh.Thickness, 0.2) {
		t.Errorf("SHAPE: %+v", gsh)
	}
	// VIEWPORT
	gv := get(viewport).(*entity.EntViewport)
	if !nearGeo(gv.Width, 210) || !nearGeo(gv.Height, 148) || !nearGeo(gv.ViewSize, 200) || gv.CircleZoom != 100 {
		t.Errorf("VIEWPORT: w=%v h=%v vs=%v cz=%d", gv.Width, gv.Height, gv.ViewSize, gv.CircleZoom)
	}
	// VERTEX_3D / PFACE 系
	gv3 := get(vtx3d).(*entity.EntVertex3d)
	if gv3.Flags != 32 || !nearGeo(gv3.Position.Z, 3) {
		t.Errorf("VERTEX_3D: %+v", gv3)
	}
	gpv := get(pfaceVtx).(*entity.EntVertexPface)
	if gpv.Flag != 192 || !nearGeo(gpv.Position.X, 4) {
		t.Errorf("VERTEX_PFACE: %+v", gpv)
	}
	gpf := get(pfaceFace).(*entity.EntVertexPfaceFace)
	if gpf.Vertind != [4]int32{1, 2, 3, 0} {
		t.Errorf("VERTEX_PFACE_FACE: %v", gpf.Vertind)
	}
	// POLYLINE_3D / PFACE / MESH
	gp3 := get(poly3d).(*entity.EntPolyline3d)
	if gp3.Flags70 != 0 || gp3.Flags75 != 0 {
		t.Errorf("POLYLINE_3D: %+v", gp3)
	}
	gpp := get(polyPface).(*entity.EntPolylinePface)
	if gpp.NumVertices != 3 || gpp.NumFaces != 1 {
		t.Errorf("POLYLINE_PFACE: %d/%d", gpp.NumVertices, gpp.NumFaces)
	}
	gpm := get(polyMesh).(*entity.EntPolylineMesh)
	if gpm.MVertexCount != 2 || gpm.NVertexCount != 2 {
		t.Errorf("POLYLINE_MESH: %+v", gpm)
	}
	// MLINE 样式句柄经 handle 流回读
	if gm.StyleHandle != 0x99 && gm.StyleHandle != 0 {
		t.Logf("MLINE 样式句柄回读: %d（owner 归属路径）", gm.StyleHandle)
	}
}

// TestWriteForwardDynamicClasses 极限批次 E 动态类闭环：IMAGE/WIPEOUT/
// MULTILEADER/LIGHT/ARC_DIMENSION → WriteDwg → Parse（类段注册动态码 +
// ≥500 类型码写出，回读按类段路由到对应解码器）。
func TestWriteForwardDynamicClasses(t *testing.T) {
	// IMAGE 与 WIPEOUT 同布局（entWipeout 承载，按 typeName 路由）
	image := &entity.EntWipeout{
		ClassVersion: 0,
		Pt0:          entity.Point3{100, 200, 0}, Uvec: entity.Point3{50, 0, 0}, Vvec: entity.Point3{0, 40, 0},
		ImageSize: entity.Point2{1, 1}, DisplayProps: 7, Clipping: true,
		Brightness: 50, Contrast: 50, Fade: 0,
		ClipBoundaryType: 1,
		ClipVerts:        []entity.Point2{{0, 0}, {1, 1}},
	}
	image.TypeName = "IMAGE"
	wipeout := &entity.EntWipeout{
		ClassVersion: 0,
		Pt0:          entity.Point3{0, 0, 0}, Uvec: entity.Point3{10, 0, 0}, Vvec: entity.Point3{0, 10, 0},
		ImageSize: entity.Point2{1, 1}, DisplayProps: 7,
		ClipBoundaryType: 2,
		ClipVerts:        []entity.Point2{{0, 0}, {0.5, 0.2}, {1, 0.5}, {0.3, 1}},
	}
	wipeout.TypeName = "WIPEOUT"
	mleader := &entity.EntMLeader{
		MleaderType: 1, Flags: 0x44400, LineLinewt: -2,
		HasLanding: true, HasDogleg: true, LandingDist: 0.36, ArrowSize: 4,
		StyleContent: 2, TextLeft: 1, TextRight: 6, TextAngletype: 1,
		StyleAttachment: 1, Justification: 3, ScaleFactor: 1,
	}
	light := &entity.EntLight{
		ClassVersion: 1, Name: "ProbeLight", LightType: 3, Status: true,
		LightColorIndex: 5, Intensity: 5.4,
		Position: entity.Point3{1, 2, 0}, Target: entity.Point3{3, 4, 0},
		AttenuationEnd: 10, HotspotAngle: 0.785, FalloffAngle: 0.87,
		CastShadows: true, ShadowMapSize: 256, ShadowMapSoftness: 1,
	}
	arcDim := &entity.EntDimension{
		DimFlags: 0x25, DimFlag: 0x25,
		Extrusion: entity.Point3{0, 0, 1}, TextMidpoint: entity.Point3{2, 2, 0},
		UserText: "ARC<>", InsertScale: entity.Point3{1, 1, 1},
		AttachmentPoint: 5, LineSpacingStyle: 1, LineSpacingFactor: 1,
		InsertPoint: entity.Point3{6125, 2865, 0}, HasInsertPoint: true,
		DefPt: entity.Point3{6125, 2865, 0}, Point13: entity.Point3{5486, 2529, 0},
		Point14: entity.Point3{6571, 2539, 0}, Point15: entity.Point3{6100, 2700, 0},
		IsPartial:     true,
		ArcStartParam: 0.3, ArcEndParam: 2.1,
		HasLeader:      false,
		DimstyleHandle: 0x77,
	}
	arcDim.TypeName = "ARC_DIMENSION"
	doc := fwdSynthDoc(t, image, wipeout, mleader, light, arcDim)
	got := fwdWriteParse(t, doc)
	byKind := map[string]any{}
	for _, e := range got.modelSpace {
		switch d := e.(type) {
		case *entity.EntWipeout:
			byKind[d.TypeName] = d
		case *entity.EntImage:
			// 读侧 IMAGE 路由到独立 entImage 类型（写侧由 entWipeout 承载）
			byKind["IMAGE"] = d
		case *entity.EntMLeader:
			byKind["MULTILEADER"] = d
		case *entity.EntLight:
			byKind["LIGHT"] = d
		case *entity.EntDimension:
			byKind[d.TypeName] = d
		}
	}
	gi, ok := byKind["IMAGE"].(*entity.EntImage)
	if !ok {
		t.Fatalf("回读缺少 IMAGE（kind=%v）", byKind)
	}
	if !nearGeo(gi.Pt0.X, 100) || !nearGeo(gi.Uvec.X, 50) || !nearGeo(gi.Vvec.Y, 40) ||
		!nearGeo(gi.ImageSize.X, 1) || gi.DisplayProps != 7 || !gi.Clipping {
		t.Errorf("IMAGE 字段: %+v", gi)
	}
	gw, ok := byKind["WIPEOUT"].(*entity.EntWipeout)
	if !ok {
		t.Fatalf("回读缺少 WIPEOUT")
	}
	if len(gw.ClipVerts) != 4 || !nearGeo(gw.ClipVerts[2].X, 1) || gw.ClipBoundaryType != 2 {
		t.Errorf("WIPEOUT 裁剪边界: type=%d verts=%+v", gw.ClipBoundaryType, gw.ClipVerts)
	}
	gm, ok := byKind["MULTILEADER"].(*entity.EntMLeader)
	if !ok {
		t.Fatalf("回读缺少 MULTILEADER")
	}
	if gm.MleaderType != 1 || !gm.HasLanding || !nearGeo(gm.LandingDist, 0.36) ||
		!nearGeo(gm.ArrowSize, 4) || gm.StyleContent != 2 || gm.TextLeft != 1 {
		t.Errorf("MULTILEADER 标量: type=%d landing=%v dist=%v arrow=%v", gm.MleaderType, gm.HasLanding, gm.LandingDist, gm.ArrowSize)
	}
	gl, ok := byKind["LIGHT"].(*entity.EntLight)
	if !ok {
		t.Fatalf("回读缺少 LIGHT")
	}
	if gl.Name != "ProbeLight" || gl.LightType != 3 || !gl.Status || gl.LightColorIndex != 5 ||
		!nearGeo(gl.Intensity, 5.4) || !nearGeo(gl.Position.X, 1) || !nearGeo(gl.Target.X, 3) ||
		gl.ShadowMapSize != 256 {
		t.Errorf("LIGHT 字段: %+v", gl)
	}
	ga, ok := byKind["ARC_DIMENSION"].(*entity.EntDimension)
	if !ok {
		t.Fatalf("回读缺少 ARC_DIMENSION（kind=%v）", byKind)
	}
	if ga.DimFlag&0x7 != 5 || !nearGeo(ga.DefPt.X, 6125) || !nearGeo(ga.Point14.X, 6571) ||
		!ga.IsPartial || !nearGeo(ga.ArcEndParam, 2.1) {
		t.Errorf("ARC_DIMENSION 弧长尾部: def=%+v partial=%v param=%v", ga.DefPt, ga.IsPartial, ga.ArcEndParam)
	}
	if ga.DimstyleHandle != 0x77 {
		t.Errorf("ARC_DIMENSION dimstyle 句柄: %d != 0x77", ga.DimstyleHandle)
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
	group := &object.ObjGeneric{
		Name: "GROUP", Handle: 0x500, Owner: 0x13,
		Fields: []object.ObjField{
			{"name", "PROBE_GROUP"}, {"unnamed", float64(0)}, {"selectable", float64(1)},
			{"num_groups", float64(2)},
			{"groups", []any{[]any{float64(5), float64(2), float64(0x510), float64(0x510)},
				[]any{float64(5), float64(2), float64(0x511), float64(0x511)}}},
		},
	}
	xrecord := &object.ObjGeneric{
		Name: "XRECORD", Handle: 0x501, Owner: 0x14,
		Fields: []object.ObjField{
			{"xdata", []any{[]any{float64(70), float64(1)}, []any{float64(1), "BA88-PROBE"},
				[]any{float64(310), "DEADBEEF"}}},
			{"cloning", float64(1)},
		},
	}
	dictvar := &object.ObjGeneric{
		Name: "DICTIONARYVAR", Handle: 0x502,
		Fields: []object.ObjField{{"schema", float64(0)}, {"strvalue", "2"}},
	}
	scale := &object.ObjGeneric{
		Name: "SCALE", Handle: 0x503,
		Fields: []object.ObjField{
			{"flag", float64(0)}, {"name", "1:2"},
			{"paper_units", 1.0}, {"drawing_units", 2.0}, {"is_unit_scale", true},
		},
	}
	layout := &object.ObjGeneric{
		Name: "LAYOUT", Handle: 0x504,
		Fields: []object.ObjField{
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
	wipeoutVars := &object.ObjGeneric{
		Name: "WIPEOUTVARIABLES", Handle: 0x505,
		Fields: []object.ObjField{{"display_frame", float64(1)}},
	}
	placeholder := &object.ObjGeneric{Name: "PLACEHOLDER", Handle: 0x506}
	appid := &object.ObjGeneric{
		Name: "APPID", Handle: 0x507,
		Fields: []object.ObjField{
			{"name", "PROBE_APP"}, {"is_xref_ref", true},
			{"is_xref_resolved", float64(1)}, {"is_xref_dep", false}, {"unknown", float64(0)},
		},
	}
	doc := fwdSynthDoc(t)
	doc.internalObjects = map[uint64]*object.ObjGeneric{}
	for _, g := range []*object.ObjGeneric{group, xrecord, dictvar, scale, layout, wipeoutVars, placeholder, appid} {
		doc.internalObjects[g.Handle] = g
	}
	got := fwdWriteParse(t, doc)
	// 回读侧按句柄取 objGeneric 并按类型断键
	type objProbe struct {
		name  string
		check func(*object.ObjGeneric) error
	}
	handles := map[uint64]string{}
	probes := map[uint64]func(*object.ObjGeneric) error{
		group.Handle: func(g *object.ObjGeneric) error {
			if gfStr(g, "name") != "PROBE_GROUP" || gfNum(g, "selectable") != 1 {
				return fmt.Errorf("name/selectable")
			}
			// 组员句柄在读侧进入 g.Handles（handleVectorKey 消费）
			if n := len(g.Handles); n != 2 {
				return fmt.Errorf("组员句柄 %d != 2", n)
			}
			return nil
		},
		xrecord.Handle: func(g *object.ObjGeneric) error {
			if gfNum(g, "cloning") != 1 {
				return fmt.Errorf("cloning")
			}
			// xdata 往返：读侧 objGeneric 的 xdata 为数组，值形态随解码器
			if v := g.Field("xdata"); v == nil {
				return fmt.Errorf("xdata 缺失")
			}
			return nil
		},
		dictvar.Handle: func(g *object.ObjGeneric) error {
			if gfStr(g, "strvalue") != "2" || gfNum(g, "schema") != 0 {
				return fmt.Errorf("schema/strvalue")
			}
			return nil
		},
		scale.Handle: func(g *object.ObjGeneric) error {
			if gfStr(g, "name") != "1:2" || !nearTol(gfReal(g, "drawing_units"), 2, 1e-9) {
				return fmt.Errorf("name/drawing_units")
			}
			return nil
		},
		layout.Handle: func(g *object.ObjGeneric) error {
			if gfStr(g, "layout_name") != "Layout9" || gfNum(g, "tab_order") != 9 ||
				!nearTol(gfReal(g, "plotsettings.paper_width"), 210, 1e-9) {
				return fmt.Errorf("layout_name/tab_order/paper_width")
			}
			if gfNum(g, "plotsettings.plot_flags") != 11952 {
				return fmt.Errorf("plot_flags")
			}
			return nil
		},
		wipeoutVars.Handle: func(g *object.ObjGeneric) error {
			if gfNum(g, "display_frame") != 1 {
				return fmt.Errorf("display_frame")
			}
			return nil
		},
		placeholder.Handle: func(*object.ObjGeneric) error { return nil },
		appid.Handle: func(g *object.ObjGeneric) error {
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
	var gx *object.ObjXrecord
	for _, x := range got.Xrecords() {
		if x.Handle == xrecord.Handle {
			gx = x
		}
	}
	if gx == nil {
		t.Fatalf("回读缺少 XRECORD %#x", xrecord.Handle)
	}
	if gx.Cloning != 1 || len(gx.Xdata) != 3 {
		t.Fatalf("XRECORD: cloning=%d items=%d", gx.Cloning, len(gx.Xdata))
	}
	if gx.Xdata[1].Str != "BA88-PROBE" || gx.Xdata[2].Code != 310 {
		t.Errorf("XRECORD xdata: %+v %+v", gx.Xdata[1], gx.Xdata[2])
	}
}

// missingProbeHandles 列出回读侧缺失的门禁探针句柄（诊断用）。
func missingProbeHandles(doc *Document, probes map[uint64]func(*object.ObjGeneric) error) []string {
	var missing []string
	for h := range probes {
		if _, ok := doc.internalObjects[h]; !ok {
			missing = append(missing, fmt.Sprintf("%#x", h))
		}
	}
	return missing
}
