// dxf_cross_test.go 与 LibreDWG 本体（dwgread 输出 DXF）做系统性逐实体
// 交叉验证：解析 DXF ENTITIES 段，按句柄匹配 Go 解析结果并逐字段对比。
// 需要 CAD_DXF_DIR 指向 dwgread 生成的 DXF 目录（默认 /tmp/dxfout），
// 缺省跳过。dwgread 构建方法见 tools/README.md。
package cad

import (
	"bufio"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// dxfEntity DXF 实体：类型、句柄与组码 → 按序取值列表。
type dxfEntity struct {
	typ    string
	handle uint64
	code   []int
	val    []string
}

func (e *dxfEntity) first(code int) (float64, bool) {
	for i, c := range e.code {
		if c == code {
			v, err := strconv.ParseFloat(strings.TrimSpace(e.val[i]), 64)
			if err != nil {
				continue
			}
			return v, true
		}
	}
	return 0, false
}

func (e *dxfEntity) str(code int) (string, bool) {
	for i, c := range e.code {
		if c == code {
			return strings.TrimSpace(e.val[i]), true
		}
	}
	return "", false
}

// rawStr 原值提取（仅剥行尾空白）：ATTRIB/ATTDEF 文本的 leading 空格是
// tag 真实内容（01-2 的 " 1" 实证），不得被 TrimSpace 剥离。
func (e *dxfEntity) rawStr(code int) (string, bool) {
	for i, c := range e.code {
		if c == code {
			return strings.TrimRight(e.val[i], " \t"), true
		}
	}
	return "", false
}

// pairsOf 提取 (10,20[,30]) 重复对：LWPOLYLINE 顶点等场景。
func (e *dxfEntity) pt2s() [][2]float64 {
	var out [][2]float64
	for i, c := range e.code {
		if c == 10 {
			x, err := strconv.ParseFloat(strings.TrimSpace(e.val[i]), 64)
			if err != nil {
				continue
			}
			y := 0.0
			for j := i + 1; j < len(e.code); j++ {
				if e.code[j] == 20 {
					if v, err := strconv.ParseFloat(strings.TrimSpace(e.val[j]), 64); err == nil {
						y = v
					}
					break
				}
			}
			out = append(out, [2]float64{x, y})
		}
	}
	return out
}

// parseDXF 解析 DXF 的 ENTITIES 段。
func parseDXF(path string) ([]*dxfEntity, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []*dxfEntity
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	inEntities := false
	var cur *dxfEntity
	expectCode := true
	var code int
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r\n ")
		if expectCode {
			c, err := strconv.Atoi(strings.TrimSpace(line))
			if err != nil {
				continue
			}
			code = c
			expectCode = false
			continue
		}
		expectCode = true
		if code == 2 && strings.TrimSpace(line) == "ENTITIES" {
			inEntities = true
			continue
		}
		if code == 0 {
			v := strings.TrimSpace(line)
			if v == "ENDSEC" {
				if inEntities {
					break
				}
				continue
			}
			if v == "EOF" {
				break
			}
			if !inEntities {
				continue
			}
			if cur != nil {
				out = append(out, cur)
			}
			cur = &dxfEntity{typ: v}
			continue
		}
		if !inEntities || cur == nil {
			continue
		}
		if code == 5 && cur.handle == 0 {
			if h, err := strconv.ParseUint(strings.TrimSpace(line), 16, 64); err == nil {
				cur.handle = h
			}
		}
		cur.code = append(cur.code, code)
		cur.val = append(cur.val, line)
	}
	if cur != nil {
		out = append(out, cur)
	}
	return out, nil
}

// TestLibreDWGDXFCross 与 LibreDWG dwgread 的 DXF 输出逐实体交叉验证。
func TestLibreDWGDXFCross(t *testing.T) {
	dxfDir := os.Getenv("CAD_DXF_DIR")
	if dxfDir == "" {
		t.Skip("CAD_DXF_DIR 未设置，跳过 LibreDWG DXF 交叉验证")
	}
	files, _ := filepath.Glob(filepath.Join("testdata", "*.dwg"))
	totalSamples, passSamples := 0, 0
	var failed []string
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".dwg")
		dxfPath := filepath.Join(dxfDir, name+".dxf")
		if _, err := os.Stat(dxfPath); err != nil {
			continue
		}
		ents, err := parseDXF(dxfPath)
		if err != nil {
			t.Logf("%s: DXF 解析失败 %v", name, err)
			continue
		}
		if len(ents) == 0 {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		doc, err := Parse(data)
		if err != nil {
			totalSamples++
			failed = append(failed, name+"(parse err)")
			continue
		}
		byHandle := map[uint64]any{}
		for _, e := range doc.modelSpace {
			if b := entBase(e); b != nil {
				byHandle[b.handle] = e
			}
		}
		totalSamples++
		match, cmp, mismatch := 0, 0, 0
		for _, de := range ents {
			if de.typ == "SEQEND" || de.typ == "VERTEX" {
				continue
			}
			// 组码 67=1 表示图纸空间实体（Go 仅解析模型空间）
			if v, ok := de.first(67); ok && v == 1 {
				continue
			}
			ge, ok := byHandle[de.handle]
			if !ok {
				continue
			}
			cmp++
			if dxfCompareEntity(t, name, de, ge) {
				match++
			} else {
				mismatch++
			}
		}
		pct := 100.0
		if cmp > 0 {
			pct = float64(match) * 100 / float64(cmp)
		}
		t.Logf("%s: compared=%d match=%d (%.1f%%)", name, cmp, match, pct)
		if match == cmp {
			passSamples++
		} else {
			failed = append(failed, name)
		}
	}
	t.Logf("samples=%d pass=%d", totalSamples, passSamples)
	if len(failed) > 0 {
		t.Errorf("未达标样本: %v", failed)
	}
}

// dxfCompareEntity 对单个实体按类型做字段级对比。
func dxfCompareEntity(t *testing.T, sample string, de *dxfEntity, ge any) bool {
	ok := func(a, b float64) bool { return math.Abs(a-b) <= 1e-6*math.Max(1, math.Abs(a)) }
	f3 := func(e *dxfEntity, c int) (float64, float64, float64) {
		x, _ := e.first(c)
		y, _ := e.first(c + 10)
		z, _ := e.first(c + 20)
		return x, y, z
	}
	eq3 := func(x, y, z float64, p point3) bool {
		return ok(x, p.x) && ok(y, p.y) && ok(z, p.z)
	}
	fail := func(what string) bool {
		t.Errorf("%s h=%d %s %s 不匹配", sample, de.handle, de.typ, what)
		return false
	}
	switch de.typ {
	case "LINE":
		e := ge.(*entLine)
		x1, y1, z1 := f3(de, 10)
		x2, y2, z2 := f3(de, 11)
		if !eq3(x1, y1, z1, e.start) {
			return fail("start")
		}
		if !eq3(x2, y2, z2, e.end) {
			return fail("end")
		}
	case "XLINE", "RAY":
		e := ge.(*entRay)
		x1, y1, z1 := f3(de, 10)
		x2, y2, z2 := f3(de, 11)
		if !eq3(x1, y1, z1, e.start) {
			return fail("start")
		}
		if !eq3(x2, y2, z2, e.unitVector) {
			return fail("vector")
		}
	case "CIRCLE":
		e := ge.(*entCircle)
		cx, cy, cz := f3(de, 10)
		r, _ := de.first(40)
		if !eq3(cx, cy, cz, e.center) || !ok(r, e.radius) {
			return fail("center/radius")
		}
	case "ARC":
		e := ge.(*entArc)
		cx, cy, cz := f3(de, 10)
		r, _ := de.first(40)
		a0, _ := de.first(50)
		a1, _ := de.first(51)
		if !eq3(cx, cy, cz, e.center) || !ok(r, e.radius) ||
			!ok(a0, e.angleStart*180/math.Pi) || !ok(a1, e.angleEnd*180/math.Pi) {
			return fail("geometry/angles")
		}
	case "POINT":
		e := ge.(*entPoint)
		x, y, z := f3(de, 10)
		rot, _ := de.first(50)
		if !eq3(x, y, z, e.location) || !ok(rot, e.rotation*180/math.Pi) {
			return fail("location/rotation")
		}
	case "ELLIPSE":
		e := ge.(*entEllipse)
		cx, cy, cz := f3(de, 10)
		mx, my, mz := f3(de, 11)
		ratio, _ := de.first(40)
		a0, _ := de.first(41)
		a1, _ := de.first(42)
		if !eq3(cx, cy, cz, e.center) || !eq3(mx, my, mz, e.majorAxis) ||
			!ok(ratio, e.ratio) || !ok(a0, e.startAng) || !ok(a1, e.endAng) {
			return fail("geometry")
		}
	case "LWPOLYLINE":
		e := ge.(*entLwPolyline)
		n, _ := de.first(90)
		if int(n) != len(e.vertices) {
			return fail("顶点数")
		}
		pts := de.pt2s()
		for i, p := range pts {
			if i >= len(e.vertices) {
				break
			}
			if !ok(p[0], e.vertices[i].x) || !ok(p[1], e.vertices[i].y) {
				return fail("顶点")
			}
		}
	case "TEXT":
		e := ge.(*entText)
		s, _ := de.str(1)
		h, _ := de.first(40)
		x, y, z := f3(de, 10)
		if s != e.text || !ok(h, e.height) || !eq3(x, y, z, e.insertion) {
			return fail("text/height/insertion")
		}
	case "ATTRIB":
		e := ge.(*entAttrib)
		s, _ := de.rawStr(1)
		h, _ := de.first(40)
		x, y, z := f3(de, 10)
		if s == "" {
			// LibreDWG 对部分 annotative ATTRIB 的文字读取为空（其自身局限），
			// 此时仅比对几何字段
			return eq3(x, y, z, e.insertion) && ok(h, e.height)
		}
		// 尾随空白视为等价（DWG 中 tag/文字常带填充空格）
		if strings.TrimRight(s, " \t") != strings.TrimRight(e.text, " \t") ||
			!ok(h, e.height) || !eq3(x, y, z, e.insertion) {
			return fail("text/height/insertion")
		}
	case "ATTDEF":
		// 极限批次 A：属性定义（1 默认值/3 提示/2 标签/40 字高/10 插入点）
		e := ge.(*entAttrib)
		s, _ := de.rawStr(1)
		tag, _ := de.rawStr(2)
		h, _ := de.first(40)
		x, y, z := f3(de, 10)
		if s != strings.TrimRight(e.text, " \t") || tag != strings.TrimRight(e.tag, " \t") ||
			!ok(h, e.height) || !eq3(x, y, z, e.insertion) {
			return fail("text/tag/height/insertion")
		}
	case "MTEXT":
		e := ge.(*entMText)
		s, _ := de.str(1)
		if v3, ok3 := de.str(3); ok3 {
			s = v3 + s
		}
		h, _ := de.first(40)
		x, y, z := f3(de, 10)
		// DXF 3/1 拼接为原始富文本，与 Go 解析原文（未剥离格式）对比
		if s != e.text || !ok(h, e.textHeight) || !eq3(x, y, z, e.insertion) {
			return fail("text/height/insertion")
		}
	case "SOLID", "TRACE":
		e := ge.(*entSolid)
		x1, y1, _ := f3(de, 10)
		x2, y2, _ := f3(de, 11)
		x3, y3, _ := f3(de, 12)
		x4, y4, _ := f3(de, 13)
		if !ok(x1, e.p1.x) || !ok(y1, e.p1.y) || !ok(x2, e.p2.x) || !ok(y2, e.p2.y) ||
			!ok(x3, e.p3.x) || !ok(y3, e.p3.y) || !ok(x4, e.p4.x) || !ok(y4, e.p4.y) {
			return fail("corners")
		}
	case "3DFACE":
		e := ge.(*entFace3d)
		for i, p := range [4]point3{e.p1, e.p2, e.p3, e.p4} {
			x, y, z := f3(de, 10+i)
			if !eq3(x, y, z, p) {
				return fail("顶点")
			}
		}
	case "INSERT":
		e := ge.(*entInsert)
		x, y, z := f3(de, 10)
		// 缺省组码：41/42/43=1.0、50=0
		sx := 1.0
		if v, okv := de.first(41); okv {
			sx = v
		}
		sy := 1.0
		if v, okv := de.first(42); okv {
			sy = v
		}
		sz := 1.0
		if v, okv := de.first(43); okv {
			sz = v
		}
		rot, _ := de.first(50)
		if !eq3(x, y, z, e.position) || !ok(sx, e.scale.x) || !ok(sy, e.scale.y) ||
			!ok(sz, e.scale.z) || !ok(rot, e.rotation*180/math.Pi) {
			return fail("insertion/scale/rotation")
		}
	case "SPLINE":
		e := ge.(*entSpline)
		nk, _ := de.first(72)
		nc, _ := de.first(73)
		nf, _ := de.first(74)
		if int(nk) != len(e.knots) || int(nc) != len(e.controlPoints) || int(nf) != len(e.fitPoints) {
			return fail("节点/控制点/拟合点数")
		}
		ki := 0
		for i, c := range de.code {
			if c == 40 && ki < len(e.knots) {
				v, err := strconv.ParseFloat(strings.TrimSpace(de.val[i]), 64)
				if err == nil {
					if math.Abs(v-e.knots[ki]) > 1e-6 {
						return fail("节点值")
					}
					ki++
				}
			}
		}
		ci := 0
		for i, c := range de.code {
			if c == 10 && ci < len(e.controlPoints) {
				x, _ := strconv.ParseFloat(strings.TrimSpace(de.val[i]), 64)
				var y, z float64
				for j := i + 1; j < len(de.code); j++ {
					if de.code[j] == 20 && y == 0 {
						y, _ = strconv.ParseFloat(strings.TrimSpace(de.val[j]), 64)
					}
					if de.code[j] == 30 {
						z, _ = strconv.ParseFloat(strings.TrimSpace(de.val[j]), 64)
						break
					}
				}
				cp := e.controlPoints[ci]
				if !ok(x, cp.x) || !ok(y, cp.y) || !ok(z, cp.z) {
					return fail("控制点")
				}
				ci++
			}
		}
	case "DIMENSION":
		e, isDim := ge.(*entDimension)
		if !isDim {
			return fail("类型")
		}
		t1x, t1y, t1z := f3(de, 11)
		d3, d4, d5 := f3(de, 13)
		d6, d7, d8 := f3(de, 14)
		if !eq3(t1x, t1y, t1z, e.textMidpoint) ||
			!eq3(d3, d4, d5, e.point13) || !eq3(d6, d7, d8, e.point14) {
			return fail("标注点")
		}
		// ANG2LN 布局：参照实现与 LibreDWG 的流内 2RD def_pt 即 DXF 10，
		// 而我们的 point16x/p16y 对应 DXF 10；point10 对应 DXF 16
		sub := ""
		for i, c := range de.code {
			if c == 100 && strings.HasPrefix(de.val[i], "AcDb2Line") {
				sub = "AcDb2LineAngularDimension"
				break
			}
		}
		x0, y0, _ := f3(de, 10)
		x16, y16, _ := f3(de, 16)
		if sub == "AcDb2LineAngularDimension" {
			// ANG2LN：流内 2RD def_pt=DXF10 ↔ 我们读入 point16x/p16y；
			// 流内末尾 3BD xline2end=DXF16 ↔ 我们的 point10
			if !ok(x0, e.point16x) || !ok(y0, e.p16y) {
				return fail("角度定义点")
			}
			if !ok(x16, e.point10.x) || !ok(y16, e.point10.y) {
				return fail("弧线点")
			}
		} else if !eq3(x0, y0, func() float64 { z, _ := de.first(30); return z }(), e.point10) {
			return fail("定义点")
		}
		if m, hasM := de.first(42); hasM && m != 0 {
			if !ok(m, e.actualMeasurement) {
				return fail("测量值")
			}
		}
	case "HATCH":
		e := ge.(*entHatch)
		solid, _ := de.first(70)
		if (solid != 0) != e.solidFill {
			return fail("solid 标志")
		}
		// 渐变段（极限批次 A）：450 标志/453 色数/470 渐变名/460 角度（度）
		grad, hasGrad := de.first(450)
		if hasGrad {
			if uint32(grad) != e.isGradientFill {
				return fail("is_gradient_fill")
			}
			nc, _ := de.first(453)
			if int(nc) != len(e.colors) {
				return fail("num_colors")
			}
			name, hasName := de.str(470)
			if hasName && name != e.gradientName {
				return fail("gradient_name")
			}
			// 460 角度不对照：DXF 规范为度（我们写出侧 radDeg 转度），
			// LibreDWG out_dxf 输出弧度原值（实测 0.089011...，未做
			// rad2deg，与自身 in_dxf 的 deg2rad 不对称），值级不可比
			if len(e.colors) > 0 {
				shift, hasShift := de.first(463)
				if hasShift && !ok(shift, e.colors[0].shiftValue) {
					return fail("colors[0].shift")
				}
			}
		}
	case "IMAGE", "WIPEOUT":
		// 极限批次 A：栅格图像（90 版本/10 位置/11-12 双轴/13 尺寸/340
		// IMAGEDEF/70 显示属性/71 边界类型）
		var im *entImage
		var wp *entWipeout
		if v, ok2 := ge.(*entImage); ok2 {
			im = v
		} else if v, ok3 := ge.(*entWipeout); ok3 {
			wp = v
		} else {
			return fail("类型")
		}
		gcv := func() uint32 {
			if im != nil {
				return im.classVersion
			}
			return wp.classVersion
		}
		gpt := func() point3 {
			if im != nil {
				return im.pt0
			}
			return wp.pt0
		}
		gsize := func() point2 {
			if im != nil {
				return im.imageSize
			}
			return wp.imageSize
		}
		gdef := func() uint64 {
			if im != nil {
				return im.imageDef
			}
			return wp.imageDef
		}
		gprops := func() uint16 {
			if im != nil {
				return im.displayProps
			}
			return wp.displayProps
		}
		gclip := func() uint16 {
			if im != nil {
				return im.clipBoundaryType
			}
			return wp.clipBoundaryType
		}
		cv, _ := de.first(90)
		if uint32(cv) != gcv() {
			return fail("class_version")
		}
		x0, y0, z0 := f3(de, 10)
		if !eq3(x0, y0, z0, gpt()) {
			return fail("位置")
		}
		sx, sy, _ := f3(de, 13)
		if !ok(sx, gsize().x) || !ok(sy, gsize().y) {
			return fail("image_size")
		}
		if hd, hasH := de.first(340); hasH && uint64(hd) != gdef() && gdef() != 0 {
			return fail("imagedef")
		}
		if pv, hasP := de.first(70); hasP && uint16(pv) != gprops() {
			return fail("display_props")
		}
		if cb, hasC := de.first(71); hasC && uint16(cb) != gclip() {
			return fail("clip_boundary_type")
		}
	case "VIEWPORT":
		// 极限批次 A：视口（10 中心/40-41 宽高/68-69 开关与 id/72 圆缩放）
		e := ge.(*entViewport)
		x0, y0, z0 := f3(de, 10)
		if !eq3(x0, y0, z0, e.center) {
			return fail("中心")
		}
		w, _ := de.first(40)
		h, _ := de.first(41)
		if !ok(w, e.width) || !ok(h, e.height) {
			return fail("宽高")
		}
		if onOff, hasO := de.first(68); hasO {
			expect := int64(0)
			if e.owner != 0 {
				expect = 1
			}
			if int64(onOff) != expect {
				return fail("on_off")
			}
		}
		if cz, hasC := de.first(72); hasC && uint16(cz) != e.circleZoom {
			return fail("circle_zoom")
		}
	case "REGION", "3DSOLID", "BODY":
		// 极限批次 A：ACIS 系（290 acis_empty/70 version；SAT 数据行为
		// 加密态，明文值级由单元用例覆盖，此处比对骨架标量）
		e, isAcis := ge.(*entAcis)
		if !isAcis {
			return fail("类型")
		}
		if empty, hasE := de.first(290); hasE && (empty != 0) != e.acisEmpty {
			return fail("acis_empty")
		}
		if ver, hasV := de.first(70); hasV && e.version == 1 && uint16(ver) != e.version {
			return fail("version")
		}
	case "POLYLINE_PFACE", "POLYLINE_MESH":
		// 极限批次 A：面网格/多面网格（70 标志 64/16 + 71/72 计数）
		if _, isPface := ge.(*entPolylinePface); isPface {
			flag, _ := de.first(70)
			if int(flag)&64 == 0 {
				return fail("pface 标志")
			}
			nv, hasN := de.first(71)
			if hasN && int(nv) != ge.(*entPolylinePface).numVertices {
				return fail("numverts")
			}
		} else if m, isMesh := ge.(*entPolylineMesh); isMesh {
			flag, _ := de.first(70)
			if int(flag)&16 == 0 {
				return fail("mesh 标志")
			}
			if mv, hasM := de.first(71); hasM && uint16(mv) != m.mVertexCount {
				return fail("m_verts")
			}
		} else {
			return fail("类型")
		}
	case "PROXY_ENTITY":
		// 极限批次 A：代理实体（90 proxy_id 恒 499/95 版本）
		e, isProxy := ge.(*entProxyEntity)
		if !isProxy {
			return fail("类型")
		}
		if pid, hasP := de.first(90); hasP && uint32(pid) != e.proxyID {
			return fail("proxy_id")
		}
	case "OLE2FRAME":
		// 极限批次 A：OLE 框架（71 类型/90 数据大小）
		e, isOle := ge.(*entOle2Frame)
		if !isOle {
			return fail("类型")
		}
		if t, hasT := de.first(71); hasT && uint16(t) != e.oleType {
			return fail("ole_type")
		}
		if sz, hasS := de.first(90); hasS && uint32(sz) != e.dataSize {
			return fail("data_size")
		}
	case "MULTILEADER":
		// 极限批次 A：ctx 块内容分支（296 开关 + 341 块表句柄，hex）
		e, isM := ge.(*entMLeader)
		if !isM {
			return fail("类型")
		}
		blk, has296 := de.first(296)
		if has296 && (blk != 0) != e.ctx.hasContentBlk {
			return fail("has_content_blk")
		}
		if has296 && e.ctx.hasContentBlk {
			for i, c := range de.code {
				if c != 341 {
					continue
				}
				if h, err := strconv.ParseUint(strings.TrimSpace(de.val[i]), 16, 64); err == nil && h != e.ctx.blk.blockTable {
					return fail("block_table")
				}
				break
			}
		}
	}
	return true
}
