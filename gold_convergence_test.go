package cad

import (
	"bufio"
	"fmt"
	"github.com/unitedrhino/go-cad/internal/entity"
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
	p3 := func(p entity.Point3) string { return fv(p.X) + " " + fv(p.Y) + " " + fv(p.Z) }
	var walk func(ents []any)
	walk = func(ents []any) {
		for _, e := range ents {
			base := entity.EntityBase(e)
			if base == nil {
				continue
			}
			var typ, fields string
			switch t := e.(type) {
			case *entity.EntLine:
				typ, fields = "LINE", p3(t.Start)+" "+p3(t.End)
			case *entity.EntCircle:
				typ, fields = "CIRCLE", p3(t.Center)+" "+fv(t.Radius)
			case *entity.EntArc:
				typ, fields = "ARC", p3(t.Center)+" "+fv(t.Radius)+" "+fv(t.AngleStart)+" "+fv(t.AngleEnd)
			case *entity.EntPoint:
				typ, fields = "POINT", p3(t.Location)+" "+fv(t.Rotation)
			case *entity.EntEllipse:
				typ, fields = "ELLIPSE", p3(t.Center)+" "+p3(t.MajorAxis)+" "+fv(t.Ratio)+" "+fv(t.StartAng)+" "+fv(t.EndAng)
			case *entity.EntLwPolyline:
				typ = "LWPOLYLINE"
				fields = strconv.Itoa(len(t.Vertices))
				for i, v := range t.Vertices {
					bulge := 0.0
					if i < len(t.Bulges) {
						bulge = t.Bulges[i]
					}
					fields += " " + fv(v.X) + " " + fv(v.Y) + " " + fv(bulge)
				}
			case *entity.EntText:
				typ, fields = "TEXT", strings.ReplaceAll(t.Text, " ", `\s`)+" "+fv(t.Height)+" "+fv(t.Insertion.X)+" "+fv(t.Insertion.Y)
			case *entity.EntMText:
				typ, fields = "MTEXT", strings.ReplaceAll(t.Text, " ", `\s`)+" "+fv(t.TextHeight)+" "+fv(t.Insertion.X)+" "+fv(t.Insertion.Y)
			case *entity.EntInsert:
				typ, fields = "INSERT", p3(t.Position)+" "+p3(t.Scale)+" "+fv(t.Rotation)+" "+strconv.FormatUint(t.BlockHeader, 10)
			case *entity.EntSpline:
				typ = "SPLINE"
				optU := func(v uint32, has bool) string {
					if !has {
						return "None"
					}
					return fmt.Sprintf("Some(%d)", v)
				}
				fields = fmt.Sprintf("%d %s %s %d %d %d", t.Scenario, optU(t.SplineFlags1, t.R2013Plus), optU(t.KnotParameter, t.R2013Plus), t.Degree, len(t.Knots), len(t.ControlPoints))
				for _, k := range t.Knots {
					fields += " " + fv(k)
				}
				for _, p := range t.ControlPoints {
					fields += " " + p3(p)
				}
				for _, p := range t.FitPoints {
					fields += fmt.Sprintf(" F %s %s", fv(p.X), fv(p.Y))
				}
			case *entity.EntRay:
				kind0 := "RAY"
				if t.Xline {
					kind0 = "XLINE"
				}
				typ, fields = kind0, p3(t.Start)+" "+p3(t.UnitVector)
			case *entity.EntSolid:
				kind0 := "SOLID"
				if t.Trace {
					kind0 = "TRACE"
				}
				typ, fields = kind0, fv(t.P1.X)+" "+fv(t.P1.Y)+" "+fv(t.P2.X)+" "+fv(t.P2.Y)+" "+
					fv(t.P3.X)+" "+fv(t.P3.Y)+" "+fv(t.P4.X)+" "+fv(t.P4.Y)+" "+fv(t.Elevation)+
					" "+fv(t.Thickness)+" "+fv(t.Extrusion.X)+" "+fv(t.Extrusion.Y)
			case *entity.EntFace3d:
				typ, fields = "3DFACE", p3(t.P1)+" "+p3(t.P2)+" "+p3(t.P3)+" "+p3(t.P4)+" "+strconv.Itoa(int(t.InvisibleEdgeFlags))
			case *entity.EntLeader:
				typ = "LEADER"
				fields = strconv.Itoa(int(t.AnnotationType)) + " " + strconv.Itoa(int(t.PathType)) + " " + strconv.Itoa(len(t.Points))
				for _, p := range t.Points {
					fields += " " + p3(p)
				}
			case *entity.EntMLine:
				typ = "MLINE"
				fields = fv(t.Scale) + " " + strconv.Itoa(int(t.Justification)) + " " + strconv.Itoa(int(t.OpenClosed)) + " " +
					strconv.Itoa(int(t.LinesInStyle)) + " " + strconv.Itoa(len(t.Vertices))
				for _, v := range t.Vertices {
					fields += " " + p3(v.Position)
				}
			case *entity.EntVertex2d:
				typ, fields = "VERTEX_2D", strconv.Itoa(int(t.Flags))+" "+p3(t.Position)+" "+fv(t.StartWidth)+" "+fv(t.EndWidth)+" "+fv(t.Bulge)
			case *entity.EntVertex3d:
				typ, fields = "VERTEX_3D", strconv.Itoa(int(t.Flags))+" "+p3(t.Position)
			case *entity.EntPolyline2d:
				typ = "POLYLINE_2D"
				fields = "0 0 " + strconv.Itoa(len(t.OwnedHandles))
				for _, h := range t.OwnedHandles {
					fields += " " + strconv.FormatUint(h, 10)
				}
			case *entity.EntPolyline3d:
				typ = "POLYLINE_3D"
				fields = strconv.Itoa(int(t.Flags75)) + " " + strconv.Itoa(int(t.Flags70)) + " " + strconv.Itoa(len(t.OwnedHandles))
				for _, h := range t.OwnedHandles {
					fields += " " + strconv.FormatUint(h, 10)
				}
			case *entity.EntDimension:
				// 与探针格式一致：每点仅 x y 两值；测量值用 Rust Debug 全精度（最短往返）
				typ = "DIM"
				f2 := func(p entity.Point3) string { return fv(p.X) + " " + fv(p.Y) }
				fields = f2(t.Point10) + " " + f2(t.Point13) + " " + f2(t.Point14) + " " +
					f2(t.TextMidpoint) + " Some(" + rustDbgF64(t.ActualMeasurement) + ")"
			case *entity.EntHatch:
				typ = "HATCH"
				sfb, ab := 0, 0
				if t.SolidFill {
					sfb = 1
				}
				if t.Associative {
					ab = 1
				}
				fields = fmt.Sprintf("%d %d %d %s", sfb, ab, len(t.Paths), t.Name)
				for _, p := range t.Paths {
					cb := 0
					if p.Closed {
						cb = 1
					}
					fields += fmt.Sprintf(" P%d %d", cb, len(p.Points))
					for _, v := range p.Points {
						fields += fmt.Sprintf(" %s,%s", fv(v.X), fv(v.Y))
					}
				}
			case *entity.EntTolerance:
				typ, fields = "TOLERANCE", strings.ReplaceAll(t.Text, " ", `\s`)+" "+p3(t.Insertion)+" "+fv(t.XDirection.X)+" "+fv(t.XDirection.Y)
			case *entity.EntPolylinePface:
				typ, fields = "POLYLINE_PFACE", strconv.Itoa(t.NumVertices)+" "+strconv.Itoa(t.NumFaces)
			case *entity.EntViewport:
				typ, fields = "VIEWPORT", ""
			case *entity.EntShape:
				typ, fields = "SHAPE", p3(t.Insertion)+" "+fv(t.Scale)+" "+fv(t.Rotation)+" "+fv(t.WidthFactor)+" "+
					fv(t.Oblique)+" "+fv(t.Thickness)+" "+strconv.Itoa(int(t.StyleId))+" "+fv(t.Extrusion.X)
			case *entity.EntPolylineMesh:
				typ = "POLYLINE_MESH"
				fields = strconv.Itoa(int(t.Flags)) + " " + strconv.Itoa(int(t.CurveType)) + " " +
					strconv.Itoa(int(t.MVertexCount)) + " " + strconv.Itoa(int(t.NVertexCount)) + " " +
					strconv.Itoa(int(t.MDensity)) + " " + strconv.Itoa(int(t.NDensity)) + " " +
					strconv.Itoa(len(t.OwnedHandles))
				for _, h := range t.OwnedHandles {
					fields += " " + strconv.FormatUint(h, 10)
				}
			default:
				continue
			}
			if _, dup := out[base.Handle]; !dup {
				out[base.Handle] = [2]string{typ, fields}
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
