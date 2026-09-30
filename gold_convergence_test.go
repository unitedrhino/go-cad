package cad

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
)

// parseGoldLinesFull 解析参照探针输出的 gold 文件，完整提取 6 个坐标。
func parseGoldLinesFull(path string) (map[uint64][6]float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[uint64][6]float64{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "LINE handle=") || strings.Contains(line, "ERROR") {
			continue
		}
		var h uint64
		var coords [6]float64
		n, _ := fmt.Sscanf(line,
			"LINE handle=%d start=(%f,%f,%f) end=(%f,%f,%f)",
			&h, &coords[0], &coords[1], &coords[2], &coords[3], &coords[4], &coords[5])
		if n != 7 {
			continue
		}
		out[h] = coords
	}
	return out, sc.Err()
}

// TestGoldLineMatch 与参照探针输出的 gold 逐实体对比 LINE 坐标。
// 需要 CAD_GOLD_LINES 环境变量指向 gold 文件，缺省跳过。
func TestGoldLineMatch(t *testing.T) {
	goldPath := os.Getenv("CAD_GOLD_LINES")
	if goldPath == "" {
		t.Skip("CAD_GOLD_LINES 未设置，跳过 gold 对照")
	}
	data, err := os.ReadFile("testdata/lw_example2018.dwg")
	if err != nil {
		t.Skipf("样本缺失: %v", err)
	}
	gold, err := parseGoldLinesFull(goldPath)
	if err != nil {
		t.Fatalf("解析 gold 失败: %v", err)
	}
	got := DebugLines(data)
	if len(got) == 0 {
		t.Fatalf("Go 解析出 0 条 LINE")
	}
	match, total, noGeom := 0, 0, 0
	var fails []string
	for h, g := range gold {
		o, ok := got[h]
		if !ok {
			noGeom++
			continue
		}
		total++
		same := true
		for i := 0; i < 6; i++ {
			if math.Abs(o[i]-g[i]) > 1e-6 {
				same = false
				break
			}
		}
		if same {
			match++
		} else {
			if len(fails) < 12 {
				fails = append(fails, fmt.Sprintf("h=%d gold=(%.3f,%.3f,%.3f|%.3f,%.3f,%.3f) got=(%.3f,%.3f,%.3f|%.3f,%.3f,%.3f)",
					h, g[0], g[1], g[2], g[3], g[4], g[5], o[0], o[1], o[2], o[3], o[4], o[5]))
			}
		}
	}
	t.Logf("gold=%d go_lines=%d compared=%d match=%d (%.1f%%) go_extra=%d go_missing=%d",
		len(gold), len(got), total, match, float64(match)*100/math.Max(1, float64(total)),
		len(got)-total, noGeom)
	for _, f := range fails {
		t.Logf("FAIL %s", f)
	}
	if match != total {
		t.Errorf("gold 匹配未达成: %d/%d", match, total)
	}
}

// rustDbgF64 模拟 Rust {:?} 的 f64 最短往返表示（整数值补 .0）。
func rustDbgF64(v float64) string {
	s := strconv.FormatFloat(v, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}

// DebugEntitiesFlat 输出全部实体的平面化字段（字段顺序与探针 gold 一致），
// 用于与参考实现逐值对照。返回 handle → [类型, 字段串]。
func DebugEntitiesFlat(data []byte) map[uint64][2]string {
	doc, err := Parse(data)
	if err != nil {
		return nil
	}
	out := map[uint64][2]string{}
	fv := func(v float64) string {
		if v == math.Trunc(v) && math.Abs(v) < 1e15 {
			return strconv.FormatFloat(v, 'f', 1, 64)
		}
		return strconv.FormatFloat(v, 'f', 12, 64)
	}
	p3 := func(p point3) string { return fv(p.x) + " " + fv(p.y) + " " + fv(p.z) }
	var walk func(ents []any)
	walk = func(ents []any) {
		for _, e := range ents {
			base := entBase(e)
			if base == nil {
				continue
			}
			var typ, fields string
			switch t := e.(type) {
			case *entLine:
				typ, fields = "LINE", p3(t.start)+" "+p3(t.end)
			case *entCircle:
				typ, fields = "CIRCLE", p3(t.center)+" "+fv(t.radius)
			case *entArc:
				typ, fields = "ARC", p3(t.center)+" "+fv(t.radius)+" "+fv(t.angleStart)+" "+fv(t.angleEnd)
			case *entPoint:
				typ, fields = "POINT", p3(t.location)+" "+fv(t.rotation)
			case *entEllipse:
				typ, fields = "ELLIPSE", p3(t.center)+" "+p3(t.majorAxis)+" "+fv(t.ratio)+" "+fv(t.startAng)+" "+fv(t.endAng)
			case *entLwPolyline:
				typ = "LWPOLYLINE"
				fields = strconv.Itoa(len(t.vertices))
				for i, v := range t.vertices {
					bulge := 0.0
					if i < len(t.bulges) {
						bulge = t.bulges[i]
					}
					fields += " " + fv(v.x) + " " + fv(v.y) + " " + fv(bulge)
				}
			case *entText:
				typ, fields = "TEXT", strings.ReplaceAll(t.text, " ", `\s`)+" "+fv(t.height)+" "+fv(t.insertion.x)+" "+fv(t.insertion.y)
			case *entMText:
				typ, fields = "MTEXT", strings.ReplaceAll(t.text, " ", `\s`)+" "+fv(t.textHeight)+" "+fv(t.insertion.x)+" "+fv(t.insertion.y)
			case *entInsert:
				typ, fields = "INSERT", p3(t.position)+" "+p3(t.scale)+" "+fv(t.rotation)+" "+strconv.FormatUint(t.blockHeader, 10)
			case *entSpline:
				typ = "SPLINE"
				optU := func(v uint32, has bool) string {
					if !has {
						return "None"
					}
					return fmt.Sprintf("Some(%d)", v)
				}
				fields = fmt.Sprintf("%d %s %s %d %d %d", t.scenario, optU(t.splineFlags1, t.r2013Plus), optU(t.knotParameter, t.r2013Plus), t.degree, len(t.knots), len(t.controlPoints))
				for _, k := range t.knots {
					fields += " " + fv(k)
				}
				for _, p := range t.controlPoints {
					fields += " " + p3(p)
				}
				for _, p := range t.fitPoints {
					fields += fmt.Sprintf(" F %s %s", fv(p.x), fv(p.y))
				}
			case *entRay:
				kind0 := "RAY"
				if t.xline {
					kind0 = "XLINE"
				}
				typ, fields = kind0, p3(t.start)+" "+p3(t.unitVector)
			case *entSolid:
				kind0 := "SOLID"
				if t.trace {
					kind0 = "TRACE"
				}
				typ, fields = kind0, fv(t.p1.x)+" "+fv(t.p1.y)+" "+fv(t.p2.x)+" "+fv(t.p2.y)+" "+
					fv(t.p3.x)+" "+fv(t.p3.y)+" "+fv(t.p4.x)+" "+fv(t.p4.y)+" "+fv(t.elevation)+
					" "+fv(t.thickness)+" "+fv(t.extrusion.x)+" "+fv(t.extrusion.y)
			case *entFace3d:
				typ, fields = "3DFACE", p3(t.p1)+" "+p3(t.p2)+" "+p3(t.p3)+" "+p3(t.p4)+" "+strconv.Itoa(int(t.invisibleEdgeFlags))
			case *entLeader:
				typ = "LEADER"
				fields = strconv.Itoa(int(t.annotationType)) + " " + strconv.Itoa(int(t.pathType)) + " " + strconv.Itoa(len(t.points))
				for _, p := range t.points {
					fields += " " + p3(p)
				}
			case *entMLine:
				typ = "MLINE"
				fields = fv(t.scale) + " " + strconv.Itoa(int(t.justification)) + " " + strconv.Itoa(int(t.openClosed)) + " " +
					strconv.Itoa(int(t.linesInStyle)) + " " + strconv.Itoa(len(t.vertices))
				for _, v := range t.vertices {
					fields += " " + p3(v.position)
				}
			case *entVertex2d:
				typ, fields = "VERTEX_2D", strconv.Itoa(int(t.flags))+" "+p3(t.position)+" "+fv(t.startWidth)+" "+fv(t.endWidth)+" "+fv(t.bulge)
			case *entVertex3d:
				typ, fields = "VERTEX_3D", strconv.Itoa(int(t.flags))+" "+p3(t.position)
			case *entPolyline2d:
				typ = "POLYLINE_2D"
				fields = "0 0 " + strconv.Itoa(len(t.ownedHandles))
				for _, h := range t.ownedHandles {
					fields += " " + strconv.FormatUint(h, 10)
				}
			case *entPolyline3d:
				typ = "POLYLINE_3D"
				fields = strconv.Itoa(int(t.flags75)) + " " + strconv.Itoa(int(t.flags70)) + " " + strconv.Itoa(len(t.ownedHandles))
				for _, h := range t.ownedHandles {
					fields += " " + strconv.FormatUint(h, 10)
				}
			case *entDimension:
				// 与探针格式一致：每点仅 x y 两值；测量值用 Rust Debug 全精度（最短往返）
				typ = "DIM"
				f2 := func(p point3) string { return fv(p.x) + " " + fv(p.y) }
				fields = f2(t.point10) + " " + f2(t.point13) + " " + f2(t.point14) + " " +
					f2(t.textMidpoint) + " Some(" + rustDbgF64(t.actualMeasurement) + ")"
			case *entHatch:
				typ = "HATCH"
				sfb, ab := 0, 0
				if t.solidFill {
					sfb = 1
				}
				if t.associative {
					ab = 1
				}
				fields = fmt.Sprintf("%d %d %d %s", sfb, ab, len(t.paths), t.name)
				for _, p := range t.paths {
					cb := 0
					if p.closed {
						cb = 1
					}
					fields += fmt.Sprintf(" P%d %d", cb, len(p.points))
					for _, v := range p.points {
						fields += fmt.Sprintf(" %s,%s", fv(v.x), fv(v.y))
					}
				}
			case *entTolerance:
				typ, fields = "TOLERANCE", strings.ReplaceAll(t.text, " ", `\s`)+" "+p3(t.insertion)+" "+fv(t.xDirection.x)+" "+fv(t.xDirection.y)
			case *entPolylinePface:
				typ, fields = "POLYLINE_PFACE", strconv.Itoa(t.numVertices)+" "+strconv.Itoa(t.numFaces)
			case *entViewport:
				typ, fields = "VIEWPORT", ""
			case *entShape:
				typ, fields = "SHAPE", p3(t.insertion)+" "+fv(t.scale)+" "+fv(t.rotation)+" "+fv(t.widthFactor)+" "+
					fv(t.oblique)+" "+fv(t.thickness)+" "+strconv.Itoa(int(t.styleId))+" "+fv(t.extrusion.x)
			case *entPolylineMesh:
				typ = "POLYLINE_MESH"
				fields = strconv.Itoa(int(t.flags)) + " " + strconv.Itoa(int(t.curveType)) + " " +
					strconv.Itoa(int(t.mVertexCount)) + " " + strconv.Itoa(int(t.nVertexCount)) + " " +
					strconv.Itoa(int(t.mDensity)) + " " + strconv.Itoa(int(t.nDensity)) + " " +
					strconv.Itoa(len(t.ownedHandles))
				for _, h := range t.ownedHandles {
					fields += " " + strconv.FormatUint(h, 10)
				}
			default:
				continue
			}
			if _, dup := out[base.handle]; !dup {
				out[base.handle] = [2]string{typ, fields}
			}
		}
	}
	walk(doc.modelSpace)
	for _, list := range doc.blocks {
		walk(list)
	}
	return out
}

// TestGoldAllEntitiesMatch 与参照探针的 ENT 输出全类型对照。
// 需要 CAD_GOLD_ENTS 指向 gold 文件、CAD_SAMPLE 指向样本（缺省 01-1.dwg），缺省跳过。
func TestGoldAllEntitiesMatch(t *testing.T) {
	goldPath := os.Getenv("CAD_GOLD_ENTS")
	if goldPath == "" {
		t.Skip("CAD_GOLD_ENTS 未设置，跳过全类型 gold 对照")
	}
	sample := os.Getenv("CAD_SAMPLE")
	if sample == "" {
		sample = "testdata/lw_example2018.dwg"
	}
	data, err := os.ReadFile(sample)
	if err != nil {
		t.Skipf("样本缺失: %v", err)
	}
	gf, err := os.Open(goldPath)
	if err != nil {
		t.Fatalf("打开 gold 失败: %v", err)
	}
	defer gf.Close()
	type goldRow struct {
		handle uint64
		typ    string
		fields string
	}
	var gold []goldRow
	sc := bufio.NewScanner(gf)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "ENT handle=") || strings.Contains(line, "ERROR") {
			continue
		}
		rest := strings.TrimPrefix(line, "ENT handle=")
		sp := strings.IndexByte(rest, ' ')
		if sp < 0 {
			continue
		}
		h, _ := strconv.ParseUint(rest[:sp], 10, 64)
		body := strings.Fields(rest[sp+1:])
		if len(body) < 2 {
			continue
		}
		gold = append(gold, goldRow{h, body[0], strings.Join(body[1:], " ")})
	}
	got := DebugEntitiesFlat(data)
	if len(got) == 0 {
		t.Fatalf("Go 解析出 0 个实体")
	}
	type stats struct{ total, match int }
	perType := map[string]*stats{}
	match, total := 0, 0
	var fails []string
	for _, g := range gold {
		st := perType[g.typ]
		if st == nil {
			st = &stats{}
			perType[g.typ] = st
		}
		o, ok := got[g.handle]
		if !ok || o[0] != g.typ {
			continue // 类型不符或缺失，不计入逐值对比
		}
		st.total++
		total++
		if o[1] == g.fields {
			st.match++
			match++
			continue
		}
		if len(fails) < 8 {
			fails = append(fails, fmt.Sprintf("h=%d %s\n  gold: %s\n  got:  %s", g.handle, g.typ, g.fields, o[1]))
		}
	}
	summary := ""
	for _, typ := range []string{"LINE", "LWPOLYLINE", "TEXT", "CIRCLE", "ARC", "POINT", "ELLIPSE", "MTEXT", "INSERT"} {
		if st := perType[typ]; st != nil {
			summary += fmt.Sprintf("%s=%d/%d ", typ, st.match, st.total)
		}
	}
	t.Logf("gold=%d compared=%d match=%d (%.1f%%) go_ents=%d | %s", len(gold), total, match,
		float64(match)*100/math.Max(1, float64(total)), len(got), summary)
	for _, f := range fails {
		t.Logf("FAIL %s", f)
	}
	if match != total {
		t.Errorf("全类型 gold 匹配未达成: %d/%d", match, total)
	}
}

// TestGoldLayerColors 与参照探针的 LAYER 颜色输出逐图层对照。
// 需要 CAD_GOLD_LAYERS 指向 gold 文件、CAD_SAMPLE 指向样本，缺省跳过。
func TestGoldLayerColors(t *testing.T) {
	goldPath := os.Getenv("CAD_GOLD_LAYERS")
	sample := os.Getenv("CAD_SAMPLE")
	if goldPath == "" || sample == "" {
		t.Skip("CAD_GOLD_LAYERS/CAD_SAMPLE 未设置，跳过图层颜色对照")
	}
	data, err := os.ReadFile(sample)
	if err != nil {
		t.Fatalf("样本缺失: %v", err)
	}
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	gf, err := os.Open(goldPath)
	if err != nil {
		t.Fatal(err)
	}
	defer gf.Close()
	type goldRow struct {
		handle uint64
		idx    uint16
		tc     uint32
		hasTC  bool
	}
	var gold []goldRow
	sc := bufio.NewScanner(gf)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "ENT handle=") || !strings.Contains(line, " LAYER ") {
			continue
		}
		rest := strings.TrimPrefix(line, "ENT handle=")
		f := strings.Fields(rest)
		// handle=N LAYER idx Some(0x...)|None
		h, e := strconv.ParseUint(strings.TrimPrefix(f[0], "handle="), 10, 64)
		if e != nil {
			continue
		}
		idx, e := strconv.ParseUint(f[2], 10, 16)
		if e != nil {
			continue
		}
		row := goldRow{handle: h, idx: uint16(idx)}
		if f[3] == "None" {
			// 无 true color
		} else {
			v := strings.TrimSuffix(strings.TrimPrefix(f[3], "Some(0x"), ")")
			tc, e := strconv.ParseUint(v, 16, 32)
			if e != nil {
				continue
			}
			row.tc, row.hasTC = uint32(tc), true
		}
		gold = append(gold, row)
	}
	match, total := 0, 0
	for _, g := range gold {
		lc, ok := doc.layerColors[g.handle]
		if !ok {
			t.Logf("MISS h=%d 图层缺失", g.handle)
			continue
		}
		total++
		okIdx := lc.index == g.idx
		okTC := lc.hasTrue == g.hasTC && (!g.hasTC || lc.trueColor == g.tc)
		if okIdx && okTC {
			match++
		} else {
			t.Logf("FAIL h=%d gold=(idx=%d tc=%v hasTC=%v) got=(idx=%d tc=%#x hasTC=%v)",
				g.handle, g.idx, g.tc, g.hasTC, lc.index, lc.trueColor, lc.hasTrue)
		}
	}
	t.Logf("layers: gold=%d compared=%d match=%d (%.1f%%)", len(gold), total, match,
		float64(match)*100/math.Max(1, float64(total)))
	if match != total {
		t.Errorf("图层颜色对照未达成: %d/%d", match, total)
	}
}
