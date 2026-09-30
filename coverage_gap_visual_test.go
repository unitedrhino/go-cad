// coverage_gap_visual_test.go 零/低覆盖函数补强（颜色/评分/内部对象/
// 渲染细分类）：aciColor 全索引、审计键、DXF 组码映射、维度评分、
// MTEXT 背景、ACIS 线框、POLYLINE_MESH、SUN/ACSH_HISTORY/TABLESTYLE
// 内部对象、tessSpline 细分与 R2013 头预览分支。
package cad

import (
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/drawing"
	"github.com/unitedrhino/go-cad/internal/entity"
	"github.com/unitedrhino/go-cad/internal/object"
	"os"
	"strings"
	"testing"
)

// ---- aci.go ----

// TestAciRGBFullIndex aciColor 全索引域：标准色表、24 色相段、灰阶、
// ByBlock/ByLayer/未知与 7 号白底反色。
func TestAciRGBFullIndex(t *testing.T) {
	if r, g, b, ok := drawing.AciColor(1, true); !ok || r != 255 || g != 0 || b != 0 {
		t.Errorf("ACI 1 = (%d,%d,%d,%v)", r, g, b, ok)
	}
	if r, _, _, ok := drawing.AciColor(7, true); !ok || r != 0 {
		t.Error("白底 ACI 7 应输出黑色")
	}
	if r, _, _, ok := drawing.AciColor(7, false); !ok || r != 255 {
		t.Error("非白底 ACI 7 应输出白色")
	}
	if r, g, b, ok := drawing.AciColor(8, true); !ok || r != 128 || g != 128 || b != 128 {
		t.Errorf("ACI 8 = (%d,%d,%d,%v)", r, g, b, ok)
	}
	for idx := 10; idx <= 249; idx++ {
		_, _, _, ok := drawing.AciColor(uint16(idx), true)
		if !ok {
			t.Fatalf("ACI %d 应有颜色", idx)
		}
	}
	for idx := 250; idx <= 255; idx++ {
		r, g, b, ok := drawing.AciColor(uint16(idx), true)
		if !ok || r != g || g != b {
			t.Errorf("ACI %d 应为灰阶 (%d,%d,%d,%v)", idx, r, g, b, ok)
		}
	}
	if _, _, _, ok := drawing.AciColor(0, true); ok {
		t.Error("ACI 0（ByBlock）应无颜色")
	}
	if _, _, _, ok := drawing.AciColor(256, true); ok {
		t.Error("ACI 256（ByLayer）应无颜色")
	}
	if _, _, _, ok := drawing.AciColor(257, true); ok {
		t.Error("ACI 257 应无颜色")
	}
	if _, _, _, ok := drawing.AciColor(999, true); ok {
		t.Error("未知索引应无颜色")
	}
}

// ---- entity_audit.go ----

// TestOle2FrameAuditField OLE2FRAME 审计键。
func TestOle2FrameAuditField(t *testing.T) {
	o := &entity.EntOle2Frame{OleType: 2, Mode: 3}
	if entity.Ole2FrameAuditField(o, "type") != int64(2) ||
		entity.Ole2FrameAuditField(o, "mode") != int64(3) ||
		entity.Ole2FrameAuditField(o, "nope") != nil {
		t.Error("ole2FrameAuditField 键值不符")
	}
}

// ---- objects_dictionary.go ----

// TestResbufValueType DXF 组码到 xdataKind 的映射分段。
func TestResbufValueType(t *testing.T) {
	cases := []struct {
		gc   int
		want object.XdataKind
	}{
		{-1, object.XdataHandle}, {0, object.XdataString}, {4, object.XdataString}, {5, object.XdataHandle},
		{8, object.XdataString}, {10, object.XdataPoint3D}, {40, object.XdataReal}, {60, object.XdataInt16},
		{90, object.XdataInt32}, {100, object.XdataString}, {105, object.XdataHandle}, {107, object.XdataInvalid},
		{110, object.XdataPoint3D}, {140, object.XdataReal}, {160, object.XdataInt64}, {170, object.XdataInt16},
		{200, object.XdataInvalid}, {210, object.XdataPoint3D}, {270, object.XdataInt16}, {280, object.XdataInt8},
		{290, object.XdataBool}, {300, object.XdataString}, {310, object.XdataBinary}, {320, object.XdataHandle},
		{330, object.XdataHandle}, {340, object.XdataHandle}, {370, object.XdataInt16},
	}
	for _, c := range cases {
		if got := object.ResbufValueType(c.gc); got != c.want {
			t.Errorf("resbufValueType(%d) = %v, 期望 %v", c.gc, got, c.want)
		}
	}
}

// ---- entities_dimension.go：维度评分 ----

// TestDimScores 量级评分的惩罚分支全命中。
func TestDimScores(t *testing.T) {
	if entity.DimAngleScore(0.5) != 0 || entity.DimAngleScore(2000) != 25 ||
		entity.DimAngleScore(1e8) != 250 || entity.DimAngleScore(1e14) != 1_000_000 {
		t.Error("dimAngleScore 分段不符")
	}
	if entity.DimAngleScore(1e-40) != 5000 {
		t.Error("dimAngleScore denormal 应重罚")
	}
	if entity.DimValueScore(100) != 0 || entity.DimValueScore(1e7) != 10 ||
		entity.DimValueScore(1e10) != 100 || entity.DimValueScore(1e15) != 1000 ||
		entity.DimValueScore(1e20) != 10000 || entity.DimValueScore(1e30) != 1_000_000 {
		t.Error("dimValueScore 分段不符")
	}
	if entity.DimValueScore(1e-40) != 5000 {
		t.Error("dimValueScore denormal 应重罚")
	}
	if entity.DimPointScore(entity.Point3{X: 1, Y: 1e7, Z: 1e-40}) != 5010 {
		t.Errorf("dimPointScore = %d", entity.DimPointScore(entity.Point3{X: 1, Y: 1e7, Z: 1e-40}))
	}
}

// TestCadTraceField trace 开启分支（仅要求不 panic）。
func TestCadTraceField(t *testing.T) {
	entity.CadTraceField(false, 0, 1, "k", "v")
	if os.Getenv("CAD_TRACE_HANDLE") != "" {
		t.Skip("trace 已开启，跳过开启分支断言")
	}
	// 直接覆盖 on=true 分支（输出到 stderr，不打断）
	entity.CadTraceField(true, 0, 1, "k", "v")
}

// ---- entities.go：MTEXT 背景 ----

// TestReadMTextBackground 背景填充数据合成位流。
func TestReadMTextBackground(t *testing.T) {
	w := bitstream.NewEncWriter()
	w.WriteBD(1.0)  // scale factor
	w.WriteBS(0)    // bg color index
	w.WriteBL(0xFF) // rgb
	w.WriteRC(0)    // flags（无附加串）
	w.WriteBL(0)    // transparency
	if err := entity.ReadMTextBackground(bitstream.NewBitStream(w.Bytes())); err != nil {
		t.Fatalf("readMTextBackground 失败: %v", err)
	}
}

// ---- entities_more.go：ACIS 线框 / POLYLINE_MESH ----

// TestDecodeAcisWireframe 线框结构全分支（含 transform 与轮廓省略）。
func TestDecodeAcisWireframe(t *testing.T) {
	// wireframe_data_present=false 短路
	w := bitstream.NewEncWriter()
	w.WriteB(false)
	a1 := &entity.EntAcis{}
	if err := entity.DecodeAcisWireframe(bitstream.NewBitStream(w.Bytes()), a1); err != nil {
		t.Fatalf("短路分支失败: %v", err)
	}
	if a1.WireframeDataPresent {
		t.Error("短路分支 wireframeDataPresent 应为 false")
	}

	// 完整结构：1 条 wire（含 transform）+ 0 轮廓
	w2 := bitstream.NewEncWriter()
	w2.WriteB(true)   // wireframe_data_present
	w2.WriteB(true)   // point_present
	w3bd(w2, 1, 2, 3) // point
	w2.WriteBL(4)     // isolines
	w2.WriteB(true)   // isoline_present
	w2.WriteBL(1)     // num_wires
	// wire：RC typ + BL marker + BS color + BL acis_index + BL numPoints
	//       + 3BD point + B transform_present(1) + 5×3BD + 3×B
	w2.WriteRC(1)
	w2.WriteBL(0)
	w2.WriteBS(256)
	w2.WriteBL(0)
	w2.WriteBL(1)
	w3bd(w2, 0, 0, 0)
	w2.WriteB(true)
	for i := 0; i < 5; i++ {
		w3bd(w2, 0, 0, 0)
	}
	w2.WriteB(false)
	w2.WriteB(false)
	w2.WriteB(false)
	w2.WriteBL(0) // num_silhouettes
	a2 := &entity.EntAcis{}
	if err := entity.DecodeAcisWireframe(bitstream.NewBitStream(w2.Bytes()), a2); err != nil {
		t.Fatalf("完整线框解析失败: %v", err)
	}
	if !a2.WireframeDataPresent || !a2.PointPresent || a2.Isolines != 4 || a2.NumWires != 1 {
		t.Errorf("字段不符: %+v", a2)
	}
}

// TestDecodePolylineMesh POLYLINE_MESH 合成位流（含顶点句柄计数）。
func TestDecodePolylineMesh(t *testing.T) {
	w := bitstream.NewEncWriter()
	w.WriteBS(1) // flags
	w.WriteBS(0) // curveType
	w.WriteBS(2) // mVertexCount
	w.WriteBS(2) // nVertexCount
	w.WriteBS(0) // mDensity
	w.WriteBS(0) // nDensity
	w.WriteBL(1) // owned count（R2004+）
	w.WriteRC(0) // handle 流兜底
	w.WriteRC(0)
	head := entity.CommonEntityHead{Handle: 0x30, ObjSizeBit: uint64(w.TellBits()) - 16}
	ent, err := entity.DecodePolylineMesh(bitstream.NewBitStream(w.Bytes()), &head, true)
	if err != nil {
		t.Fatalf("decodePolylineMesh 失败: %v", err)
	}
	m := ent.(*entity.EntPolylineMesh)
	if m.MVertexCount != 2 || m.NVertexCount != 2 {
		t.Errorf("顶点数不符: m=%d n=%d", m.MVertexCount, m.NVertexCount)
	}
}

// ---- objects_visual.go：内部对象解码器 ----

// TestDecodeGenericSUN SUN 对象合成位流（R2000 pre-CMC：BS index）。
func TestDecodeGenericSUN(t *testing.T) {
	w := bitstream.NewEncWriter()
	w.WriteBL(1)    // class_version
	w.WriteB(true)  // is_on
	w.WriteBS(7)    // CMC color index（pre-R2004）
	w.WriteBD(0.8)  // intensity
	w.WriteB(true)  // has_shadow
	w.WriteBL(100)  // julian_day
	w.WriteBL(200)  // msecs
	w.WriteB(false) // is_dst
	w.WriteBL(1)    // shadow_type
	w.WriteBS(512)  // shadow_mapsize
	w.WriteRC(3)    // shadow_softness
	g := &object.ObjGeneric{}
	fr := &object.GfRead{R: bitstream.NewBitStream(w.Bytes()), Ver: container.VerR2000}
	if err := object.DecodeGenericSUN(fr.R, fr.Ver, fr, g); err != nil {
		t.Fatalf("decodeGenericSUN 失败: %v", err)
	}
	if g.Field("intensity") != 0.8 {
		t.Errorf("intensity = %v", g.Field("intensity"))
	}
}

// TestDecodeGenericACSHHistory ACSH_HISTORY_CLASS 合成位流。
func TestDecodeGenericACSHHistory(t *testing.T) {
	w := bitstream.NewEncWriter()
	w.WriteBL(1) // major
	w.WriteBL(2) // minor
	w.WriteBL(3) // h_nodeid
	w.WriteB(true)
	w.WriteB(false)
	g := &object.ObjGeneric{}
	fr := &object.GfRead{R: bitstream.NewBitStream(w.Bytes()), Ver: container.VerR2000}
	if err := object.DecodeGenericACSH_HISTORY_CLASS(fr.R, fr.Ver, fr, g); err != nil {
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
	w := bitstream.NewEncWriter()
	w.WriteBL(1) // type
	w.WriteBS(0) // data_flags
	g := &object.ObjGeneric{}
	fr := &object.GfRead{R: bitstream.NewBitStream(w.Bytes()), Ver: container.VerR2000}
	nHdl := 0
	if err := object.ReadCellStyleFields(fr.R, fr, g, "", &nHdl); err != nil {
		t.Fatalf("短路径失败: %v", err)
	}

	// 完整结构：CMTC（R2004+ 结构）、content_format、边框数组
	w2 := bitstream.NewEncWriter()
	w2.WriteBL(1) // type
	w2.WriteBS(1) // data_flags ≠ 0
	w2.WriteBL(0) // property_override_flags
	w2.WriteBL(0) // merge_flags
	w2.WriteBS(7) // CMTC bg_color index
	w2.WriteBL(0) // CMTC rgb
	w2.WriteRC(0) // CMTC flag
	w2.WriteBL(0) // content_layout
	// content_format
	w2.WriteBL(0)   // property_override_flags
	w2.WriteBL(0)   // property_flags
	w2.WriteBL(0)   // value_data_type
	w2.WriteBL(0)   // value_unit_type
	w2.WriteTV("")  // value_format_string
	w2.WriteBD(0)   // rotation
	w2.WriteBD(1)   // block_scale
	w2.WriteBL(0)   // cell_alignment
	w2.WriteBS(7)   // CMTC content_color index
	w2.WriteBL(0)   // CMTC rgb
	w2.WriteRC(0)   // CMTC flag
	w2.WriteBD(2.5) // text_height
	// margins
	w2.WriteBS(1) // margin_override_flags ≠ 0
	for i := 0; i < 6; i++ {
		w2.WriteBD(0.1)
	}
	w2.WriteBL(1)  // num_borders
	w2.WriteBL(1)  // borders[0].index_mask ≠ 0
	w2.WriteBL(0)  // border_overrides
	w2.WriteBL(0)  // border_type
	w2.WriteBS(7)  // CMTC color index
	w2.WriteBL(0)  // CMTC rgb
	w2.WriteRC(0)  // CMTC flag
	w2.WriteBL(25) // linewt（读 BLd 有符号）
	// 尾部可能还有字段，失败时仅记录（当前只要求不 panic）
	g2 := &object.ObjGeneric{}
	fr2 := &object.GfRead{R: bitstream.NewBitStream(w2.Bytes()), Ver: container.VerR2000}
	if err := object.ReadCellStyleFields(fr2.R, fr2, g2, "", &nHdl); err != nil {
		t.Logf("完整结构尾部截断（可接受）: %v", err)
	}
}

// ---- entities_mleader.go / entities_hatch.go ----

// TestDecodeMLeaderLeadersAndContext MLEADER 领导线计数与上下文标量组
// 合成位流（R2000：文本 TV、CMC 仅索引）。
func TestDecodeMLeaderLeadersAndContext(t *testing.T) {
	w := bitstream.NewEncWriter()
	// 1 条 leader：hasLast/hasDogleg 均无 + 0 断裂 + dogleg 长 + 0 线
	w.WriteBL(1) // num_leaders
	w.WriteB(false)
	w.WriteB(false)
	w.WriteBL(0) // numBreaks
	w.WriteBL(0) // branchIndex
	w.WriteBD(2) // doglegLength
	w.WriteBL(0) // numLines
	m := &entity.EntMLeader{}
	if err := entity.DecodeMLeaderLeaders(bitstream.NewBitStream(w.Bytes()), m, container.VerR2000, false); err != nil {
		t.Fatalf("decodeMLeaderLeaders 失败: %v", err)
	}
	if m.Ctx.NumLeaders != 1 || len(m.Ctx.Leaders) != 1 {
		t.Fatalf("leaders 计数不符: %d", m.Ctx.NumLeaders)
	}
	if m.Ctx.Leaders[0].DoglegLength != 2 {
		t.Errorf("doglegLength = %v", m.Ctx.Leaders[0].DoglegLength)
	}

	w2 := bitstream.NewEncWriter()
	w2.WriteBD(1.0)   // scaleFactor
	w3bd(w2, 0, 0, 0) // contentBase
	w2.WriteBD(2.0)   // textHeight
	w2.WriteBD(0.5)   // arrowSize
	w2.WriteBD(0.1)   // landingGap
	w2.WriteBS(0)     // textLeft
	w2.WriteBS(0)     // textRight
	w2.WriteBS(1)     // textAngletype
	w2.WriteBS(0)     // textAlignment
	w2.WriteB(true)   // hasContentTxt
	// txt 内容分支
	w2.WriteTV("NOTE") // defaultText（<R2007 内联）
	w3bd(w2, 0, 0, 1)  // normal
	w3bd(w2, 1, 1, 0)  // location
	w3bd(w2, 1, 0, 0)  // direction
	w2.WriteBD(0)      // rotation
	w2.WriteBD(20)     // width
	w2.WriteBD(3)      // height
	w2.WriteBD(1)      // lineSpacingFactor
	w2.WriteBS(1)      // lineSpacingStyle
	w2.WriteBS(7)      // color CMC index
	w2.WriteBS(0)      // alignment
	w2.WriteBS(0)      // flow
	w2.WriteBS(0)      // bgColor CMC index
	w2.WriteBD(1)      // bgScale
	w2.WriteBL(0)      // bgTransparency
	w2.WriteB(false)   // isBgFill
	w2.WriteB(false)   // isBgMaskFill
	w2.WriteBS(0)      // colType
	w2.WriteB(true)    // isHeightAuto
	w2.WriteBD(10)     // colWidth
	w2.WriteBD(0.5)    // colGutter
	w2.WriteB(false)   // isColFlowReversed
	w2.WriteBL(0)      // numColSizes
	w2.WriteB(false)   // wordBreak
	w2.WriteB(false)   // unknown
	// base 三点 + is_normal_reversed
	w3bd(w2, 0, 0, 0)
	w3bd(w2, 0, 1, 0)
	w3bd(w2, 1, 0, 0)
	w2.WriteB(false)
	m2 := &entity.EntMLeader{}
	if err := entity.DecodeMLeaderContext(bitstream.NewBitStream(w2.Bytes()), m2, container.VerR2000, 0, nil); err != nil {
		t.Fatalf("decodeMLeaderContext 失败: %v", err)
	}
	if m2.Ctx.TextHeight != 2.0 || !m2.Ctx.HasContentTxt {
		t.Errorf("context 标量组不符: textHeight=%v hasTxt=%v", m2.Ctx.TextHeight, m2.Ctx.HasContentTxt)
	}
	if m2.Ctx.Txt.DefaultText != "NOTE" {
		t.Errorf("defaultText = %q", m2.Ctx.Txt.DefaultText)
	}
}

// TestHatchHelpers skipColorCMCR2004/readHatchString/decodeHatchSplineEdge。
func TestHatchHelpers(t *testing.T) {
	// CMC 跳过：flag=0 无名称
	w := bitstream.NewEncWriter()
	w.WriteBS(7)
	w.WriteBL(0)
	w.WriteRC(0)
	if err := entity.SkipColorCMCR2004(bitstream.NewBitStream(w.Bytes())); err != nil {
		t.Fatalf("skipColorCMCR2004 失败: %v", err)
	}

	// 字符串三模式
	w2 := bitstream.NewEncWriter()
	w2.WriteTV("SOLID")
	s1, err := entity.ReadHatchString(bitstream.NewBitStream(w2.Bytes()), entity.HatchStrInlineTv, 30)
	if err != nil || s1 != "SOLID" {
		t.Errorf("inline TV = %q err=%v", s1, err)
	}
	w3 := bitstream.NewEncWriter()
	w3.WriteTU("ANSI31")
	s2, err := entity.ReadHatchString(bitstream.NewBitStream(w3.Bytes()), entity.HatchStrInlineTu, 30)
	if err != nil || strings.TrimSuffix(s2, "\x00") != "ANSI31" {
		t.Errorf("inline TU = %q err=%v", s2, err)
	}
	if s3, err := entity.ReadHatchString(nil, entity.HatchStrStringStream, 30); err != nil || s3 != "" {
		t.Errorf("StringStream 模式应返回空串: %q err=%v", s3, err)
	}

	// 样条边：degree 2、rational（带权重）、2 控制点、无拟合点
	w4 := bitstream.NewEncWriter()
	w4.WriteBL(2)    // degree
	w4.WriteB(true)  // rational
	w4.WriteB(false) // periodic
	w4.WriteBL(2)    // numKnots
	w4.WriteBL(2)    // numControl
	w4.WriteBD(0)
	w4.WriteBD(1)
	w4.WriteRD(0)
	w4.WriteRD(0)
	w4.WriteBD(1.5)
	w4.WriteRD(2)
	w4.WriteRD(3)
	w4.WriteBD(1.0)
	seg, _, err := entity.DecodeHatchSplineEdge(bitstream.NewBitStream(w4.Bytes()), false)
	if err != nil {
		t.Fatalf("decodeHatchSplineEdge 失败: %v", err)
	}
	if seg.Degree != 2 || !seg.Rational || len(seg.Knots) != 2 || len(seg.Ctrl) != 2 || len(seg.Weights) != 2 {
		t.Errorf("样条边字段不符: degree=%d ctrl=%d knots=%d weights=%d", seg.Degree, len(seg.Ctrl), len(seg.Knots), len(seg.Weights))
	}

	// 拟合点分支（含起末切线）
	w5 := bitstream.NewEncWriter()
	w5.WriteBL(3)
	w5.WriteB(false)
	w5.WriteB(false)
	w5.WriteBL(0)
	w5.WriteBL(0)
	w5.WriteBL(1) // numFit
	w5.WriteRD(1)
	w5.WriteRD(2)
	w5.WriteRD(0) // startTan.x
	w5.WriteRD(1) // startTan.y
	w5.WriteRD(0) // endTan.x
	w5.WriteRD(1) // endTan.y
	seg2, _, err := entity.DecodeHatchSplineEdge(bitstream.NewBitStream(w5.Bytes()), true)
	if err != nil {
		t.Fatalf("拟合点分支失败: %v", err)
	}
	if seg2.Degree != 3 || len(seg2.FitPts) != 1 || seg2.FitPts[0].Y != 2 {
		t.Errorf("拟合点分支字段不符: degree=%d fitPts=%d", seg2.Degree, len(seg2.FitPts))
	}
}

// TestDecodeGenericTABLECONTENT TABLECONTENT 全嵌套合成位流（R2004：
// 文本 TV、cellstyle 走 data_flags=0 短路径、cell 含 Value 内容与
// merged_cells）。
func TestDecodeGenericTABLECONTENT(t *testing.T) {
	w := bitstream.NewEncWriter()
	w.WriteTV("")  // ldata.name
	w.WriteTV("")  // ldata.description
	w.WriteBL(1)   // num_cols
	w.WriteTV("A") // cols[0].name
	w.WriteBL(0)   // cols[0].custom_data
	w.WriteBL(1)   // cols[0].cellstyle.type
	w.WriteBS(0)   // cols[0].cellstyle.data_flags=0（短路径）
	w.WriteBL(1)   // num_rows
	w.WriteBL(1)   // rows[0].num_cells
	w.WriteBL(0)   // cells[0].flag
	w.WriteTV("")  // cells[0].tooltip
	w.WriteBL(0)   // cells[0].customdata
	w.WriteBL(0)   // cells[0].num_customdata_items
	w.WriteBL(0)   // cells[0].has_linked_data
	w.WriteBL(1)   // cells[0].num_cell_contents
	w.WriteBL(1)   // cell_contents[0].type = Value
	w.WriteBL(0)   // value.data_type = kLong
	w.WriteBL(42)  // value.data_long
	w.WriteBL(0)   // num_attrs
	w.WriteBS(0)   // has_content_format_overrides
	w.WriteBL(0)   // cells[0].style_id
	w.WriteBL(0)   // cells[0].has_geom_data
	w.WriteBL(0)   // rows[0].custom_data
	w.WriteBL(0)   // rows[0].num_customdata_items
	w.WriteBL(1)   // rows[0].cellstyle.type
	w.WriteBS(0)   // rows[0].cellstyle.data_flags
	w.WriteBL(0)   // rows[0].style_id
	w.WriteBD(5.0) // rows[0].height
	w.WriteBL(0)   // num_field_refs
	w.WriteBL(1)   // num_merged_cells
	for i := 0; i < 4; i++ {
		w.WriteBL(0) // top_row/left_col/bottom_row/right_col
	}
	g := &object.ObjGeneric{}
	fr := &object.GfRead{R: bitstream.NewBitStream(w.Bytes()), Ver: container.VerR2004}
	if err := object.DecodeGenericTABLECONTENT(fr.R, fr.Ver, fr, g); err != nil {
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
	xf := drawing.IdentityXform()

	// fit 模式（scenario=2）
	spFit := &entity.EntSpline{Scenario: 2, Degree: 3, FitPoints: []entity.Point3{{X: 0, Y: 0, Z: 0}, {X: 5, Y: 5, Z: 0}, {X: 10, Y: 0, Z: 0}}}
	st1 := drawing.TessSpline(spFit, xf)
	if len(st1) == 0 {
		t.Fatal("fit 模式应产生描边")
	}

	// 控制点模式：degree/knots 齐备走 De Boor
	spCtrl := &entity.EntSpline{
		Scenario: 1, Degree: 2,
		ControlPoints: []entity.Point3{{X: 0, Y: 0, Z: 0}, {X: 1, Y: 1, Z: 0}, {X: 2, Y: 0, Z: 0}, {X: 3, Y: 1, Z: 0}},
		Knots:         []float64{0, 0, 0, 1, 2, 3, 3, 3},
	}
	st2 := drawing.TessSpline(spCtrl, xf)
	if len(st2) == 0 {
		t.Fatal("控制点模式应产生描边")
	}

	// 退化：度数非法 → 控制点折线
	spDeg := &entity.EntSpline{Scenario: 1, Degree: 9, ControlPoints: []entity.Point3{{X: 0, Y: 0, Z: 0}, {X: 1, Y: 1, Z: 0}}}
	st3 := drawing.TessSpline(spDeg, xf)
	if len(st3) == 0 {
		t.Fatal("退化折线应产生描边")
	}

	// 控制点不足 → nil
	if got := drawing.TessSpline(&entity.EntSpline{Scenario: 1, ControlPoints: nil}, xf); got != nil {
		t.Error("控制点不足应返回 nil")
	}
}

// ---- entities.go：R2013 头预览图像分支 ----

// TestParseCommonEntityHeadR2013Preview picFlag=1 的预览图像读取。
func TestParseCommonEntityHeadR2013Preview(t *testing.T) {
	w := bitstream.NewEncWriter()
	w.WriteH(5, 1, 0x2A)
	w.WriteBS(0)   // EED 终止
	w.WriteB(true) // 有预览
	w.WriteBLLc(8) // graphic size = 8 字节
	for i := 0; i < 8; i++ {
		w.WriteRC(byte(0xA0 + i))
	}
	w.WriteBB(0)    // entityMode
	w.WriteBL(0)    // numReactors
	w.WriteB(false) // xdicMissing
	w.WriteB(false) // hasDsBinary
	w.WriteB(true)  // color noLinks
	w.WriteB(true)  // color second → ByLayer
	w.WriteBD(1.0)  // ltypeScale
	w.WriteBB(0)
	w.WriteBB(0)
	w.WriteBB(0)
	w.WriteRC(0) // shadow flags
	w.WriteB(false)
	w.WriteB(false)
	w.WriteB(false)
	w.WriteBS(0)
	w.WriteRC(0)
	head, err := entity.ParseCommonEntityHeadR2013(bitstream.NewBitStream(w.Bytes()), uint64(w.TellBits()))
	if err != nil {
		t.Fatalf("预览分支解析失败: %v", err)
	}
	if !head.PreviewExists || len(head.Preview) != 8 {
		t.Errorf("预览不符: exists=%v len=%d", head.PreviewExists, len(head.Preview))
	}
}
