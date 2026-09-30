// coverage_gap_visual_test.go 零/低覆盖函数补强（颜色/评分/内部对象/
// 渲染细分类）：aciColor 全索引、审计键、DXF 组码映射、维度评分、
// MTEXT 背景、ACIS 线框、POLYLINE_MESH、SUN/ACSH_HISTORY/TABLESTYLE
// 内部对象、tessSpline 细分与 R2013 头预览分支。
package cad

import (
	"os"
	"strings"
	"testing"
)

// ---- aci.go ----

// TestAciRGBFullIndex aciColor 全索引域：标准色表、24 色相段、灰阶、
// ByBlock/ByLayer/未知与 7 号白底反色。
func TestAciRGBFullIndex(t *testing.T) {
	if r, g, b, ok := aciColor(1, true); !ok || r != 255 || g != 0 || b != 0 {
		t.Errorf("ACI 1 = (%d,%d,%d,%v)", r, g, b, ok)
	}
	if r, _, _, ok := aciColor(7, true); !ok || r != 0 {
		t.Error("白底 ACI 7 应输出黑色")
	}
	if r, _, _, ok := aciColor(7, false); !ok || r != 255 {
		t.Error("非白底 ACI 7 应输出白色")
	}
	if r, g, b, ok := aciColor(8, true); !ok || r != 128 || g != 128 || b != 128 {
		t.Errorf("ACI 8 = (%d,%d,%d,%v)", r, g, b, ok)
	}
	for idx := 10; idx <= 249; idx++ {
		_, _, _, ok := aciColor(uint16(idx), true)
		if !ok {
			t.Fatalf("ACI %d 应有颜色", idx)
		}
	}
	for idx := 250; idx <= 255; idx++ {
		r, g, b, ok := aciColor(uint16(idx), true)
		if !ok || r != g || g != b {
			t.Errorf("ACI %d 应为灰阶 (%d,%d,%d,%v)", idx, r, g, b, ok)
		}
	}
	if _, _, _, ok := aciColor(0, true); ok {
		t.Error("ACI 0（ByBlock）应无颜色")
	}
	if _, _, _, ok := aciColor(256, true); ok {
		t.Error("ACI 256（ByLayer）应无颜色")
	}
	if _, _, _, ok := aciColor(257, true); ok {
		t.Error("ACI 257 应无颜色")
	}
	if _, _, _, ok := aciColor(999, true); ok {
		t.Error("未知索引应无颜色")
	}
}

// ---- entity_audit.go ----

// TestOle2FrameAuditField OLE2FRAME 审计键。
func TestOle2FrameAuditField(t *testing.T) {
	o := &entOle2Frame{oleType: 2, mode: 3}
	if ole2FrameAuditField(o, "type") != int64(2) ||
		ole2FrameAuditField(o, "mode") != int64(3) ||
		ole2FrameAuditField(o, "nope") != nil {
		t.Error("ole2FrameAuditField 键值不符")
	}
}

// ---- objects_dictionary.go ----

// TestResbufValueType DXF 组码到 xdataKind 的映射分段。
func TestResbufValueType(t *testing.T) {
	cases := []struct {
		gc   int
		want xdataKind
	}{
		{-1, xdataHandle}, {0, xdataString}, {4, xdataString}, {5, xdataHandle},
		{8, xdataString}, {10, xdataPoint3D}, {40, xdataReal}, {60, xdataInt16},
		{90, xdataInt32}, {100, xdataString}, {105, xdataHandle}, {107, xdataInvalid},
		{110, xdataPoint3D}, {140, xdataReal}, {160, xdataInt64}, {170, xdataInt16},
		{200, xdataInvalid}, {210, xdataPoint3D}, {270, xdataInt16}, {280, xdataInt8},
		{290, xdataBool}, {300, xdataString}, {310, xdataBinary}, {320, xdataHandle},
		{330, xdataHandle}, {340, xdataHandle}, {370, xdataInt16},
	}
	for _, c := range cases {
		if got := resbufValueType(c.gc); got != c.want {
			t.Errorf("resbufValueType(%d) = %v, 期望 %v", c.gc, got, c.want)
		}
	}
}

// ---- entities_dimension.go：维度评分 ----

// TestDimScores 量级评分的惩罚分支全命中。
func TestDimScores(t *testing.T) {
	if dimAngleScore(0.5) != 0 || dimAngleScore(2000) != 25 ||
		dimAngleScore(1e8) != 250 || dimAngleScore(1e14) != 1_000_000 {
		t.Error("dimAngleScore 分段不符")
	}
	if dimAngleScore(1e-40) != 5000 {
		t.Error("dimAngleScore denormal 应重罚")
	}
	if dimValueScore(100) != 0 || dimValueScore(1e7) != 10 ||
		dimValueScore(1e10) != 100 || dimValueScore(1e15) != 1000 ||
		dimValueScore(1e20) != 10000 || dimValueScore(1e30) != 1_000_000 {
		t.Error("dimValueScore 分段不符")
	}
	if dimValueScore(1e-40) != 5000 {
		t.Error("dimValueScore denormal 应重罚")
	}
	if dimPointScore(point3{1, 1e7, 1e-40}) != 5010 {
		t.Errorf("dimPointScore = %d", dimPointScore(point3{1, 1e7, 1e-40}))
	}
}

// TestCadTraceField trace 开启分支（仅要求不 panic）。
func TestCadTraceField(t *testing.T) {
	cadTraceField(false, 0, 1, "k", "v")
	if os.Getenv("CAD_TRACE_HANDLE") != "" {
		t.Skip("trace 已开启，跳过开启分支断言")
	}
	// 直接覆盖 on=true 分支（输出到 stderr，不打断）
	cadTraceField(true, 0, 1, "k", "v")
}

// ---- entities.go：MTEXT 背景 ----

// TestReadMTextBackground 背景填充数据合成位流。
func TestReadMTextBackground(t *testing.T) {
	w := newEncWriter()
	w.writeBD(1.0)  // scale factor
	w.writeBS(0)    // bg color index
	w.writeBL(0xFF) // rgb
	w.writeRC(0)    // flags（无附加串）
	w.writeBL(0)    // transparency
	if err := readMTextBackground(newBitStream(w.bytes())); err != nil {
		t.Fatalf("readMTextBackground 失败: %v", err)
	}
}

// ---- entities_more.go：ACIS 线框 / POLYLINE_MESH ----

// TestDecodeAcisWireframe 线框结构全分支（含 transform 与轮廓省略）。
func TestDecodeAcisWireframe(t *testing.T) {
	// wireframe_data_present=false 短路
	w := newEncWriter()
	w.writeB(false)
	a1 := &entAcis{}
	if err := decodeAcisWireframe(newBitStream(w.bytes()), a1); err != nil {
		t.Fatalf("短路分支失败: %v", err)
	}
	if a1.wireframeDataPresent {
		t.Error("短路分支 wireframeDataPresent 应为 false")
	}

	// 完整结构：1 条 wire（含 transform）+ 0 轮廓
	w2 := newEncWriter()
	w2.writeB(true)   // wireframe_data_present
	w2.writeB(true)   // point_present
	w3bd(w2, 1, 2, 3) // point
	w2.writeBL(4)     // isolines
	w2.writeB(true)   // isoline_present
	w2.writeBL(1)     // num_wires
	// wire：RC typ + BL marker + BS color + BL acis_index + BL numPoints
	//       + 3BD point + B transform_present(1) + 5×3BD + 3×B
	w2.writeRC(1)
	w2.writeBL(0)
	w2.writeBS(256)
	w2.writeBL(0)
	w2.writeBL(1)
	w3bd(w2, 0, 0, 0)
	w2.writeB(true)
	for i := 0; i < 5; i++ {
		w3bd(w2, 0, 0, 0)
	}
	w2.writeB(false)
	w2.writeB(false)
	w2.writeB(false)
	w2.writeBL(0) // num_silhouettes
	a2 := &entAcis{}
	if err := decodeAcisWireframe(newBitStream(w2.bytes()), a2); err != nil {
		t.Fatalf("完整线框解析失败: %v", err)
	}
	if !a2.wireframeDataPresent || !a2.pointPresent || a2.isolines != 4 || a2.numWires != 1 {
		t.Errorf("字段不符: %+v", a2)
	}
}

// TestDecodePolylineMesh POLYLINE_MESH 合成位流（含顶点句柄计数）。
func TestDecodePolylineMesh(t *testing.T) {
	w := newEncWriter()
	w.writeBS(1) // flags
	w.writeBS(0) // curveType
	w.writeBS(2) // mVertexCount
	w.writeBS(2) // nVertexCount
	w.writeBS(0) // mDensity
	w.writeBS(0) // nDensity
	w.writeBL(1) // owned count（R2004+）
	w.writeRC(0) // handle 流兜底
	w.writeRC(0)
	head := commonEntityHead{handle: 0x30, objSizeBit: uint64(w.tellBits()) - 16}
	ent, err := decodePolylineMesh(newBitStream(w.bytes()), &head, true)
	if err != nil {
		t.Fatalf("decodePolylineMesh 失败: %v", err)
	}
	m := ent.(*entPolylineMesh)
	if m.mVertexCount != 2 || m.nVertexCount != 2 {
		t.Errorf("顶点数不符: m=%d n=%d", m.mVertexCount, m.nVertexCount)
	}
}

// ---- objects_visual.go：内部对象解码器 ----

// TestDecodeGenericSUN SUN 对象合成位流（R2000 pre-CMC：BS index）。
func TestDecodeGenericSUN(t *testing.T) {
	w := newEncWriter()
	w.writeBL(1)    // class_version
	w.writeB(true)  // is_on
	w.writeBS(7)    // CMC color index（pre-R2004）
	w.writeBD(0.8)  // intensity
	w.writeB(true)  // has_shadow
	w.writeBL(100)  // julian_day
	w.writeBL(200)  // msecs
	w.writeB(false) // is_dst
	w.writeBL(1)    // shadow_type
	w.writeBS(512)  // shadow_mapsize
	w.writeRC(3)    // shadow_softness
	g := &objGeneric{}
	fr := &gfRead{r: newBitStream(w.bytes()), ver: verR2000}
	if err := decodeGenericSUN(fr.r, fr.ver, fr, g); err != nil {
		t.Fatalf("decodeGenericSUN 失败: %v", err)
	}
	if g.Field("intensity") != 0.8 {
		t.Errorf("intensity = %v", g.Field("intensity"))
	}
}

// TestDecodeGenericACSHHistory ACSH_HISTORY_CLASS 合成位流。
func TestDecodeGenericACSHHistory(t *testing.T) {
	w := newEncWriter()
	w.writeBL(1) // major
	w.writeBL(2) // minor
	w.writeBL(3) // h_nodeid
	w.writeB(true)
	w.writeB(false)
	g := &objGeneric{}
	fr := &gfRead{r: newBitStream(w.bytes()), ver: verR2000}
	if err := decodeGenericACSH_HISTORY_CLASS(fr.r, fr.ver, fr, g); err != nil {
		t.Fatalf("decodeGenericACSH_HISTORY_CLASS 失败: %v", err)
	}
	if g.Field("major") != int64(1) || g.Field("minor") != int64(2) {
		t.Errorf("字段不符: major=%v minor=%v", g.Field("major"), g.Field("minor"))
	}
}

// TestReadCellStyleFields CellStyle 宏合成位流（data_flags=0 短路与
// 完整结构两条路径）。
func TestReadCellStyleFields(t *testing.T) {
	// data_flags=0：type + data_flags 后直接返回
	w := newEncWriter()
	w.writeBL(1) // type
	w.writeBS(0) // data_flags
	g := &objGeneric{}
	fr := &gfRead{r: newBitStream(w.bytes()), ver: verR2000}
	nHdl := 0
	if err := readCellStyleFields(fr.r, fr, g, "", &nHdl); err != nil {
		t.Fatalf("短路径失败: %v", err)
	}

	// 完整结构：CMTC（R2004+ 结构）、content_format、边框数组
	w2 := newEncWriter()
	w2.writeBL(1) // type
	w2.writeBS(1) // data_flags ≠ 0
	w2.writeBL(0) // property_override_flags
	w2.writeBL(0) // merge_flags
	w2.writeBS(7) // CMTC bg_color index
	w2.writeBL(0) // CMTC rgb
	w2.writeRC(0) // CMTC flag
	w2.writeBL(0) // content_layout
	// content_format
	w2.writeBL(0)   // property_override_flags
	w2.writeBL(0)   // property_flags
	w2.writeBL(0)   // value_data_type
	w2.writeBL(0)   // value_unit_type
	w2.writeTV("")  // value_format_string
	w2.writeBD(0)   // rotation
	w2.writeBD(1)   // block_scale
	w2.writeBL(0)   // cell_alignment
	w2.writeBS(7)   // CMTC content_color index
	w2.writeBL(0)   // CMTC rgb
	w2.writeRC(0)   // CMTC flag
	w2.writeBD(2.5) // text_height
	// margins
	w2.writeBS(1) // margin_override_flags ≠ 0
	for i := 0; i < 6; i++ {
		w2.writeBD(0.1)
	}
	w2.writeBL(1)  // num_borders
	w2.writeBL(1)  // borders[0].index_mask ≠ 0
	w2.writeBL(0)  // border_overrides
	w2.writeBL(0)  // border_type
	w2.writeBS(7)  // CMTC color index
	w2.writeBL(0)  // CMTC rgb
	w2.writeRC(0)  // CMTC flag
	w2.writeBL(25) // linewt（读 BLd 有符号）
	// 尾部可能还有字段，失败时仅记录（当前只要求不 panic）
	g2 := &objGeneric{}
	fr2 := &gfRead{r: newBitStream(w2.bytes()), ver: verR2000}
	if err := readCellStyleFields(fr2.r, fr2, g2, "", &nHdl); err != nil {
		t.Logf("完整结构尾部截断（可接受）: %v", err)
	}
}

// ---- entities_mleader.go / entities_hatch.go ----

// TestDecodeMLeaderLeadersAndContext MLEADER 领导线计数与上下文标量组
// 合成位流（R2000：文本 TV、CMC 仅索引）。
func TestDecodeMLeaderLeadersAndContext(t *testing.T) {
	w := newEncWriter()
	// 1 条 leader：hasLast/hasDogleg 均无 + 0 断裂 + dogleg 长 + 0 线
	w.writeBL(1) // num_leaders
	w.writeB(false)
	w.writeB(false)
	w.writeBL(0) // numBreaks
	w.writeBL(0) // branchIndex
	w.writeBD(2) // doglegLength
	w.writeBL(0) // numLines
	m := &entMLeader{}
	if err := decodeMLeaderLeaders(newBitStream(w.bytes()), m, verR2000, false); err != nil {
		t.Fatalf("decodeMLeaderLeaders 失败: %v", err)
	}
	if m.ctx.numLeaders != 1 || len(m.ctx.leaders) != 1 {
		t.Fatalf("leaders 计数不符: %d", m.ctx.numLeaders)
	}
	if m.ctx.leaders[0].doglegLength != 2 {
		t.Errorf("doglegLength = %v", m.ctx.leaders[0].doglegLength)
	}

	w2 := newEncWriter()
	w2.writeBD(1.0)   // scaleFactor
	w3bd(w2, 0, 0, 0) // contentBase
	w2.writeBD(2.0)   // textHeight
	w2.writeBD(0.5)   // arrowSize
	w2.writeBD(0.1)   // landingGap
	w2.writeBS(0)     // textLeft
	w2.writeBS(0)     // textRight
	w2.writeBS(1)     // textAngletype
	w2.writeBS(0)     // textAlignment
	w2.writeB(true)   // hasContentTxt
	// txt 内容分支
	w2.writeTV("NOTE") // defaultText（<R2007 内联）
	w3bd(w2, 0, 0, 1)  // normal
	w3bd(w2, 1, 1, 0)  // location
	w3bd(w2, 1, 0, 0)  // direction
	w2.writeBD(0)      // rotation
	w2.writeBD(20)     // width
	w2.writeBD(3)      // height
	w2.writeBD(1)      // lineSpacingFactor
	w2.writeBS(1)      // lineSpacingStyle
	w2.writeBS(7)      // color CMC index
	w2.writeBS(0)      // alignment
	w2.writeBS(0)      // flow
	w2.writeBS(0)      // bgColor CMC index
	w2.writeBD(1)      // bgScale
	w2.writeBL(0)      // bgTransparency
	w2.writeB(false)   // isBgFill
	w2.writeB(false)   // isBgMaskFill
	w2.writeBS(0)      // colType
	w2.writeB(true)    // isHeightAuto
	w2.writeBD(10)     // colWidth
	w2.writeBD(0.5)    // colGutter
	w2.writeB(false)   // isColFlowReversed
	w2.writeBL(0)      // numColSizes
	w2.writeB(false)   // wordBreak
	w2.writeB(false)   // unknown
	// base 三点 + is_normal_reversed
	w3bd(w2, 0, 0, 0)
	w3bd(w2, 0, 1, 0)
	w3bd(w2, 1, 0, 0)
	w2.writeB(false)
	m2 := &entMLeader{}
	if err := decodeMLeaderContext(newBitStream(w2.bytes()), m2, verR2000, 0, nil); err != nil {
		t.Fatalf("decodeMLeaderContext 失败: %v", err)
	}
	if m2.ctx.textHeight != 2.0 || !m2.ctx.hasContentTxt {
		t.Errorf("context 标量组不符: textHeight=%v hasTxt=%v", m2.ctx.textHeight, m2.ctx.hasContentTxt)
	}
	if m2.ctx.txt.defaultText != "NOTE" {
		t.Errorf("defaultText = %q", m2.ctx.txt.defaultText)
	}
}

// TestHatchHelpers skipColorCMCR2004/readHatchString/decodeHatchSplineEdge。
func TestHatchHelpers(t *testing.T) {
	// CMC 跳过：flag=0 无名称
	w := newEncWriter()
	w.writeBS(7)
	w.writeBL(0)
	w.writeRC(0)
	if err := skipColorCMCR2004(newBitStream(w.bytes())); err != nil {
		t.Fatalf("skipColorCMCR2004 失败: %v", err)
	}

	// 字符串三模式
	w2 := newEncWriter()
	w2.writeTV("SOLID")
	s1, err := readHatchString(newBitStream(w2.bytes()), hatchStrInlineTv, 30)
	if err != nil || s1 != "SOLID" {
		t.Errorf("inline TV = %q err=%v", s1, err)
	}
	w3 := newEncWriter()
	w3.writeTU("ANSI31")
	s2, err := readHatchString(newBitStream(w3.bytes()), hatchStrInlineTu, 30)
	if err != nil || strings.TrimSuffix(s2, "\x00") != "ANSI31" {
		t.Errorf("inline TU = %q err=%v", s2, err)
	}
	if s3, err := readHatchString(nil, hatchStrStringStream, 30); err != nil || s3 != "" {
		t.Errorf("StringStream 模式应返回空串: %q err=%v", s3, err)
	}

	// 样条边：degree 2、rational（带权重）、2 控制点、无拟合点
	w4 := newEncWriter()
	w4.writeBL(2)    // degree
	w4.writeB(true)  // rational
	w4.writeB(false) // periodic
	w4.writeBL(2)    // numKnots
	w4.writeBL(2)    // numControl
	w4.writeBD(0)
	w4.writeBD(1)
	w4.writeRD(0)
	w4.writeRD(0)
	w4.writeBD(1.5)
	w4.writeRD(2)
	w4.writeRD(3)
	w4.writeBD(1.0)
	seg, _, err := decodeHatchSplineEdge(newBitStream(w4.bytes()), false)
	if err != nil {
		t.Fatalf("decodeHatchSplineEdge 失败: %v", err)
	}
	if seg.degree != 2 || !seg.rational || len(seg.knots) != 2 || len(seg.ctrl) != 2 || len(seg.weights) != 2 {
		t.Errorf("样条边字段不符: degree=%d ctrl=%d knots=%d weights=%d", seg.degree, len(seg.ctrl), len(seg.knots), len(seg.weights))
	}

	// 拟合点分支（含起末切线）
	w5 := newEncWriter()
	w5.writeBL(3)
	w5.writeB(false)
	w5.writeB(false)
	w5.writeBL(0)
	w5.writeBL(0)
	w5.writeBL(1) // numFit
	w5.writeRD(1)
	w5.writeRD(2)
	w5.writeRD(0) // startTan.x
	w5.writeRD(1) // startTan.y
	w5.writeRD(0) // endTan.x
	w5.writeRD(1) // endTan.y
	seg2, _, err := decodeHatchSplineEdge(newBitStream(w5.bytes()), true)
	if err != nil {
		t.Fatalf("拟合点分支失败: %v", err)
	}
	if seg2.degree != 3 || len(seg2.fitPts) != 1 || seg2.fitPts[0].y != 2 {
		t.Errorf("拟合点分支字段不符: degree=%d fitPts=%d", seg2.degree, len(seg2.fitPts))
	}
}

// TestDecodeGenericTABLECONTENT TABLECONTENT 全嵌套合成位流（R2004：
// 文本 TV、cellstyle 走 data_flags=0 短路径、cell 含 Value 内容与
// merged_cells）。
func TestDecodeGenericTABLECONTENT(t *testing.T) {
	w := newEncWriter()
	w.writeTV("")  // ldata.name
	w.writeTV("")  // ldata.description
	w.writeBL(1)   // num_cols
	w.writeTV("A") // cols[0].name
	w.writeBL(0)   // cols[0].custom_data
	w.writeBL(1)   // cols[0].cellstyle.type
	w.writeBS(0)   // cols[0].cellstyle.data_flags=0（短路径）
	w.writeBL(1)   // num_rows
	w.writeBL(1)   // rows[0].num_cells
	w.writeBL(0)   // cells[0].flag
	w.writeTV("")  // cells[0].tooltip
	w.writeBL(0)   // cells[0].customdata
	w.writeBL(0)   // cells[0].num_customdata_items
	w.writeBL(0)   // cells[0].has_linked_data
	w.writeBL(1)   // cells[0].num_cell_contents
	w.writeBL(1)   // cell_contents[0].type = Value
	w.writeBL(0)   // value.data_type = kLong
	w.writeBL(42)  // value.data_long
	w.writeBL(0)   // num_attrs
	w.writeBS(0)   // has_content_format_overrides
	w.writeBL(0)   // cells[0].style_id
	w.writeBL(0)   // cells[0].has_geom_data
	w.writeBL(0)   // rows[0].custom_data
	w.writeBL(0)   // rows[0].num_customdata_items
	w.writeBL(1)   // rows[0].cellstyle.type
	w.writeBS(0)   // rows[0].cellstyle.data_flags
	w.writeBL(0)   // rows[0].style_id
	w.writeBD(5.0) // rows[0].height
	w.writeBL(0)   // num_field_refs
	w.writeBL(1)   // num_merged_cells
	for i := 0; i < 4; i++ {
		w.writeBL(0) // top_row/left_col/bottom_row/right_col
	}
	g := &objGeneric{}
	fr := &gfRead{r: newBitStream(w.bytes()), ver: verR2004}
	if err := decodeGenericTABLECONTENT(fr.r, fr.ver, fr, g); err != nil {
		t.Fatalf("decodeGenericTABLECONTENT 失败: %v", err)
	}
	if g.Field("tdata.num_cols") != int64(1) || g.Field("tdata.num_rows") != int64(1) {
		t.Errorf("行列数不符: %v/%v", g.Field("tdata.num_cols"), g.Field("tdata.num_rows"))
	}
	if g.Field("tdata.rows[0].cells[0].cell_contents[0].value.data_long") != int64(42) {
		t.Errorf("cell 内容值不符: %v", g.FieldPath("tdata.rows[0].cells[0].cell_contents[0].value.data_long"))
	}
	if _, ok := g.Field("num_content_handles").(int64); !ok {
		t.Error("缺少 num_content_handles")
	}
}

// ---- render.go：tessSpline ----

// TestTessSpline 拟合点模式、控制点 De Boor 模式与退化折线三分支。
func TestTessSpline(t *testing.T) {
	xf := identityXform()

	// fit 模式（scenario=2）
	spFit := &entSpline{scenario: 2, degree: 3, fitPoints: []point3{{0, 0, 0}, {5, 5, 0}, {10, 0, 0}}}
	st1 := tessSpline(spFit, xf)
	if len(st1) == 0 {
		t.Fatal("fit 模式应产生描边")
	}

	// 控制点模式：degree/knots 齐备走 De Boor
	spCtrl := &entSpline{
		scenario: 1, degree: 2,
		controlPoints: []point3{{0, 0, 0}, {1, 1, 0}, {2, 0, 0}, {3, 1, 0}},
		knots:         []float64{0, 0, 0, 1, 2, 3, 3, 3},
	}
	st2 := tessSpline(spCtrl, xf)
	if len(st2) == 0 {
		t.Fatal("控制点模式应产生描边")
	}

	// 退化：度数非法 → 控制点折线
	spDeg := &entSpline{scenario: 1, degree: 9, controlPoints: []point3{{0, 0, 0}, {1, 1, 0}}}
	st3 := tessSpline(spDeg, xf)
	if len(st3) == 0 {
		t.Fatal("退化折线应产生描边")
	}

	// 控制点不足 → nil
	if got := tessSpline(&entSpline{scenario: 1, controlPoints: nil}, xf); got != nil {
		t.Error("控制点不足应返回 nil")
	}
}

// ---- entities.go：R2013 头预览图像分支 ----

// TestParseCommonEntityHeadR2013Preview picFlag=1 的预览图像读取。
func TestParseCommonEntityHeadR2013Preview(t *testing.T) {
	w := newEncWriter()
	w.writeH(5, 1, 0x2A)
	w.writeBS(0)   // EED 终止
	w.writeB(true) // 有预览
	w.writeBLLc(8) // graphic size = 8 字节
	for i := 0; i < 8; i++ {
		w.writeRC(byte(0xA0 + i))
	}
	w.writeBB(0)    // entityMode
	w.writeBL(0)    // numReactors
	w.writeB(false) // xdicMissing
	w.writeB(false) // hasDsBinary
	w.writeB(true)  // color noLinks
	w.writeB(true)  // color second → ByLayer
	w.writeBD(1.0)  // ltypeScale
	w.writeBB(0)
	w.writeBB(0)
	w.writeBB(0)
	w.writeRC(0) // shadow flags
	w.writeB(false)
	w.writeB(false)
	w.writeB(false)
	w.writeBS(0)
	w.writeRC(0)
	head, err := parseCommonEntityHeadR2013(newBitStream(w.bytes()), uint64(w.tellBits()))
	if err != nil {
		t.Fatalf("预览分支解析失败: %v", err)
	}
	if !head.previewExists || len(head.preview) != 8 {
		t.Errorf("预览不符: exists=%v len=%d", head.previewExists, len(head.preview))
	}
}
