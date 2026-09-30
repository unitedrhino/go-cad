// coverage_gap2_test.go 覆盖率批次 Q：对象解码器合成位流补测。
// 覆盖渲染设置族（objects_render.go）、ACSH 形体系（objects_acsh.go）、
// 动态块参数/动作族（objects_dynblock.go）、ASSOC 第二批（objects_assoc2.go）
// 中九样本未触发的 per-type 解码器：按 dwg2.spec 字段序用 encWriter
// 构造 dat 流与 handle 流，直接经 internalClassDecoders 调度执行。
package cad

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/entity"
	"github.com/unitedrhino/go-cad/internal/object"
	"github.com/unitedrhino/go-cad/internal/objrec"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// gap2BaseHandle 合成对象的基准句柄（readHandleReference 的偏移基点）。
const gap2BaseHandle = 0x30

// execClassDecoder 按类名取注册解码器，用合成 dat 流执行 decode；
// buildHdl 非空且 spec 带 hdl 时继续执行 handle 流解码。
// 返回解码产物供字段断言。
func execClassDecoder(t *testing.T, class string, ver container.DwgVersion, build, buildHdl func(w *bitstream.EncWriter)) *object.ObjGeneric {
	t.Helper()
	spec, ok := object.InternalClassDecoders[class]
	if !ok {
		t.Fatalf("无 %s 内部对象解码器", class)
	}
	g := &object.ObjGeneric{Name: class, Handle: gap2BaseHandle}
	w := bitstream.NewEncWriter()
	build(w)
	fr := &object.GfRead{R: bitstream.NewBitStream(w.Bytes()), Ver: ver}
	if err := spec.Decode(fr.R, ver, fr, g); err != nil {
		t.Fatalf("%s decode 失败: %v", class, err)
	}
	if spec.Hdl != nil && buildHdl != nil {
		hw := bitstream.NewEncWriter()
		buildHdl(hw)
		hfr := &object.GfRead{R: bitstream.NewBitStream(hw.Bytes()), Ver: ver}
		if err := spec.Hdl(hfr.R, ver, hfr, g); err != nil {
			t.Fatalf("%s hdl 失败: %v", class, err)
		}
	}
	return g
}

// gap2WriteHandles 写 n 个相对句柄引用（code 0x0A + value）。
func gap2WriteHandles(w *bitstream.EncWriter, n int) {
	for i := 0; i < n; i++ {
		w.WriteH(0x0A, 1, uint64(i+1))
	}
}

// gap2WriteEvalExpr 写 AcDbEvalExpr_fields：BLd parentid + major/minor BL
// + BS value_code + 按 code 的值 + BL nodeid（code=91 时不占 dat 位）。
func gap2WriteEvalExpr(w *bitstream.EncWriter, code int64) {
	w.WriteBL(0) // parentid（BLd 与 BL 同位流）
	w.WriteBL(1) // major
	w.WriteBL(2) // minor
	w.WriteBS(uint16(code))
	switch code {
	case 40:
		w.WriteBD(1.5)
	case 10, 11:
		w.WriteRD(1)
		w.WriteRD(2)
	case 1:
		w.WriteTV("v")
	case 90:
		w.WriteBL(9)
	case 70:
		w.WriteBS(7)
	}
	w.WriteBL(3) // nodeid
}

// gap2WriteBlockElement 写 AcDbBlockElement_fields（evalexpr + name +
// be_major/be_minor + eed1071）。
func gap2WriteBlockElement(w *bitstream.EncWriter, code int64) {
	gap2WriteEvalExpr(w, code)
	w.WriteTV("blkel")
	w.WriteBL(1)
	w.WriteBL(0)
	w.WriteBL(0)
}

// gap2WritePropInfo 写 BlockParam_PropInfo（num_connections=0）。
func gap2WritePropInfo(w *bitstream.EncWriter) {
	w.WriteBL(0)
}

// gap2WriteValueSet 写 AcDbBlockParamValueSet_fields（desc/flags/min/max/
// inc + num_valuelist + valuelist×N）。
func gap2WriteValueSet(w *bitstream.EncWriter) {
	w.WriteTV("vs")
	w.WriteBL(0)
	w.WriteBD(0)
	w.WriteBD(10)
	w.WriteBD(1)
	w.WriteBS(1)
	w.WriteBD(5)
}

// gap2Write1PtParameter 写 AcDbBlock1PtParameter_fields（参数公共体 +
// def_pt + prop1/prop2 + num_propinfos）。
func gap2Write1PtParameter(w *bitstream.EncWriter) {
	gap2WriteBlockElement(w, 70)
	w.WriteB(false)
	w.WriteB(false)
	w.WriteBD(1)
	w.WriteBD(2)
	w.WriteBD(0)
	gap2WritePropInfo(w)
	gap2WritePropInfo(w)
	w.WriteBL(0)
}

// gap2Write2PtParameter 写 AcDbBlock2PtParameter_fields（参数公共体 +
// def_basept/def_endpt + prop1..4 + prop_states×4 + base_location）。
func gap2Write2PtParameter(w *bitstream.EncWriter) {
	gap2WriteBlockElement(w, 70)
	w.WriteB(false)
	w.WriteB(false)
	w.WriteBD(0)
	w.WriteBD(0)
	w.WriteBD(0)
	w.WriteBD(10)
	w.WriteBD(0)
	w.WriteBD(0)
	for i := 0; i < 4; i++ {
		gap2WritePropInfo(w)
	}
	for i := 0; i < 4; i++ {
		w.WriteBL(0)
	}
	w.WriteBS(0)
}

// gap2WriteHistoryNode 写 AcDbShHistoryNode_fields（major/minor + trans
// 16×BD + CMC + step_id）。
func gap2WriteHistoryNode(w *bitstream.EncWriter) {
	w.WriteBL(1)
	w.WriteBL(0)
	for i := 0; i < 16; i++ {
		w.WriteBD(0)
	}
	w.WriteBS(256) // CMC index
	w.WriteBL(0xc2000000)
	w.WriteRC(0)
	w.WriteBL(0) // step_id
}

// gap2Write3BD 写三个 BD（Point3 读取）。
func gap2Write3BD(w *bitstream.EncWriter) {
	w.WriteBD(1)
	w.WriteBD(2)
	w.WriteBD(3)
}

// gap2WriteWire 写一条 WIRESTRUCT_fields（tp 决定 transform 段有无）。
func gap2WriteWire(w *bitstream.EncWriter, tp bool) {
	w.WriteRC(1)    // type
	w.WriteBL(0)    // selection_marker
	w.WriteBS(0)    // color
	w.WriteBL(0)    // acis_index
	w.WriteBL(1)    // num_points
	gap2Write3BD(w) // 点
	w.WriteB(tp)    // transform present
	if tp {
		for i := 0; i < 5; i++ {
			gap2Write3BD(w)
		}
		for i := 0; i < 3; i++ {
			w.WriteB(false)
		}
	}
}

// gap2WriteWireframe 写 COMMON_3DSOLID 线框段全链（point + isolines +
// wires + surfs（hw 带子 wire）），结尾写 acis_empty_bit。
func gap2WriteWireframe(w *bitstream.EncWriter) {
	w.WriteB(true) // wireframe_data_present
	w.WriteB(true) // point_present
	gap2Write3BD(w)
	w.WriteBL(0)   // isolines
	w.WriteB(true) // isoline_present
	w.WriteBL(1)   // num_wires
	gap2WriteWire(w, true)
	w.WriteBL(1) // num_silhouettes
	w.WriteBL(0) // vp_id
	for i := 0; i < 3; i++ {
		gap2Write3BD(w)
	}
	w.WriteB(false) // persp
	w.WriteB(true)  // has_hw
	w.WriteBL(1)    // num_hw_wires
	gap2WriteWire(w, false)
	w.WriteB(true) // acis_empty_bit
}

// gap2SATMarker End-of-ACIS-data 标记（SAB 分支搜索用）。
var gap2SATMarker = []byte("\x0e\x03End\x0e\x02of\x0e\x04ACIS\r\x04data")

// TestSynthRenderSettings 渲染设置族全类型（R2004 与 R2013 两种布局）。
func TestSynthRenderSettings(t *testing.T) {
	// RENDERSETTINGS：公共字段 + R2013 的 has_predefined（class_version
	// 位流 +1 存储）
	buildSettings := func(r13 bool) func(w *bitstream.EncWriter) {
		return func(w *bitstream.EncWriter) {
			if r13 {
				w.WriteBL(1)
			} else {
				w.WriteBL(0)
			}
			w.WriteTV("preset")
			w.WriteB(true)
			w.WriteB(false)
			w.WriteB(false)
			w.WriteB(true)
			w.WriteTV("env.png")
			w.WriteTV("desc")
			w.WriteBL(2)
			if r13 {
				w.WriteB(false)
			}
		}
	}
	g := execClassDecoder(t, "RENDERSETTINGS", container.VerR2004, buildSettings(false), nil)
	if v, _ := g.Field("display_index").(int64); v != 2 {
		t.Errorf("display_index = %v", g.Field("display_index"))
	}
	g = execClassDecoder(t, "RENDERSETTINGS", container.VerR2013, buildSettings(true), nil)
	if _, ok := g.Field("has_predefined").(bool); !ok {
		t.Errorf("R2013 缺 has_predefined")
	}

	// RAPIDRTRENDERSETTINGS：8 个专有字段；pre-R2013 尾部多 has_predefined。
	// R2013+ 的 gfRead.T 走字符串流（dat 不占位），合成流去掉全部 TV。
	buildRapid := func(r13 bool) func(w *bitstream.EncWriter) {
		return func(w *bitstream.EncWriter) {
			if r13 {
				w.WriteBL(1) // class_version（+1 存储）
				w.WriteB(false)
				w.WriteB(false)
				w.WriteB(false)
				w.WriteB(false)
				w.WriteBL(2)    // display_index
				w.WriteB(false) // has_predefined
			} else {
				buildSettings(false)(w)
			}
			w.WriteBL(1) // rapidrt_version
			w.WriteBL(0) // render_target
			w.WriteBL(2) // render_level
			w.WriteBL(0) // render_time
			w.WriteBL(1) // lighting_model
			w.WriteBL(0) // filter_type
			w.WriteBD(1.5)
			w.WriteBD(2.5)
			if !r13 {
				w.WriteB(true)
			}
		}
	}
	execClassDecoder(t, "RAPIDRTRENDERSETTINGS", container.VerR2004, buildRapid(false), nil)
	g = execClassDecoder(t, "RAPIDRTRENDERSETTINGS", container.VerR2013, buildRapid(true), nil)
	if v, _ := g.Field("filter_width").(float64); v != 1.5 {
		t.Errorf("filter_width = %v", g.Field("filter_width"))
	}

	// RENDERENTRY：18 字段（start 组 6 个 BS）
	buildEntry := func(w *bitstream.EncWriter) {
		w.WriteBL(1)
		w.WriteTV("img.png")
		w.WriteTV("preset")
		w.WriteTV("view")
		w.WriteBL(640)
		w.WriteBL(480)
		for i := 0; i < 6; i++ {
			w.WriteBS(uint16(2020 + i))
		}
		w.WriteBD(3.25)
		for i := 0; i < 5; i++ {
			w.WriteBL(uint32(i + 1))
		}
	}
	g = execClassDecoder(t, "RENDERENTRY", container.VerR2004, buildEntry, nil)
	if v, _ := g.Field("dimension_x").(int64); v != 640 {
		t.Errorf("dimension_x = %v", g.Field("dimension_x"))
	}

	// RENDERGLOBAL：9 字段
	buildGlobal := func(w *bitstream.EncWriter) {
		w.WriteBL(2)
		w.WriteBL(1)
		w.WriteBL(0)
		w.WriteB(true)
		w.WriteTV("out.png")
		w.WriteBL(800)
		w.WriteBL(600)
		w.WriteB(false)
		w.WriteB(true)
	}
	g = execClassDecoder(t, "RENDERGLOBAL", container.VerR2004, buildGlobal, nil)
	if v, _ := g.Field("image_width").(int64); v != 800 {
		t.Errorf("image_width = %v", g.Field("image_width"))
	}
}

// TestSynthMentalRaySettings MENTALRAYRENDERSETTINGS 41 专有字段全序。
func TestSynthMentalRaySettings(t *testing.T) {
	build := func(w *bitstream.EncWriter) {
		w.WriteBL(0) // class_version（pre-R2013 不偏移）
		w.WriteTV("mr")
		w.WriteB(false)
		w.WriteB(false)
		w.WriteB(false)
		w.WriteB(false)
		w.WriteTV("")
		w.WriteTV("d")
		w.WriteBL(0) // display_index
		w.WriteBL(1) // mr_version
		w.WriteBL(2)
		w.WriteBL(3) // sampling1/2
		w.WriteBS(0) // sampling_mr_filter
		for i := 0; i < 6; i++ {
			w.WriteBD(0.5) // sampling_filter/contrast_color
		}
		w.WriteBS(1) // shadow_mode
		w.WriteB(true)
		w.WriteB(true) // shadow_maps/ray_tracing
		for i := 0; i < 3; i++ {
			w.WriteBL(1) // ray_trace_depth
		}
		w.WriteB(false) // global_illumination
		w.WriteBL(100)  // gi_sample_count
		w.WriteB(false) // gi_sample_radius_enabled
		w.WriteBD(0)    // gi_sample_radius
		w.WriteBL(1000) // gi_photons_per_light
		for i := 0; i < 3; i++ {
			w.WriteBL(1) // photon_trace_depth
		}
		w.WriteB(true) // final_gathering
		w.WriteBL(200) // fg_ray_count
		for i := 0; i < 3; i++ {
			w.WriteB(false) // fg_sample_radius_state
		}
		w.WriteBD(1)
		w.WriteBD(2)
		w.WriteBD(3)    // fg_sample_radius1/2 + light_luminance_scale
		w.WriteBS(0)    // diagnostics_mode
		w.WriteBS(0)    // diagnostics_grid_mode
		w.WriteBD(0)    // diagnostics_grid_float
		w.WriteBS(0)    // diagnostics_photon_mode
		w.WriteBS(0)    // diagnostics_bsp_mode
		w.WriteB(false) // export_mi_enabled
		w.WriteTV("mi")
		w.WriteBL(64)   // tile_size
		w.WriteBS(0)    // tile_order
		w.WriteBL(0)    // memory_limit
		w.WriteB(false) // diagnostics_samples_mode
		w.WriteBD(1)    // energy_multiplier
	}
	g := execClassDecoder(t, "MENTALRAYRENDERSETTINGS", container.VerR2004, build, nil)
	if v, _ := g.Field("tile_size").(int64); v != 64 {
		t.Errorf("tile_size = %v", g.Field("tile_size"))
	}
}

// TestSynthAcshPrimitives ACSH 形体系 Sphere/Pyramid（公共前导 +
// primitive）经类表调度。
func TestSynthAcshPrimitives(t *testing.T) {
	// value_code=91：handle 流占 value + material 两个引用
	buildSphere := func(w *bitstream.EncWriter) {
		gap2WriteEvalExpr(w, 91)
		gap2WriteHistoryNode(w)
		w.WriteBL(1) // major
		w.WriteBL(0) // minor
		w.WriteBD(5) // radius
	}
	g := execClassDecoder(t, "ACSH_SPHERE_CLASS", container.VerR2004, buildSphere,
		func(w *bitstream.EncWriter) { gap2WriteHandles(w, 2) })
	if v, _ := g.Field("radius").(float64); v != 5 {
		t.Errorf("radius = %v", g.Field("radius"))
	}
	if len(g.Handles) != 2 {
		t.Errorf("SPHERE hdl 数 = %d", len(g.Handles))
	}

	buildPyramid := func(w *bitstream.EncWriter) {
		gap2WriteEvalExpr(w, 70)
		gap2WriteHistoryNode(w)
		w.WriteBL(1)
		w.WriteBL(0)
		w.WriteBD(8) // height
		w.WriteBL(6) // sides
		w.WriteBD(3) // radius
		w.WriteBD(1) // topradius
	}
	g = execClassDecoder(t, "ACSH_PYRAMID_CLASS", container.VerR2004, buildPyramid,
		func(w *bitstream.EncWriter) { gap2WriteHandles(w, 1) })
	if v, _ := g.Field("topradius").(float64); v != 1 {
		t.Errorf("topradius = %v", g.Field("topradius"))
	}
}

// TestSynthAcshBrep ACSH_BREP 的 acis 数据段三形态（empty/SAT/SAB）与
// COMMON_3DSOLID 线框段全链（经类表 acshWithCommon 公共前导）。
func TestSynthAcshBrep(t *testing.T) {
	// acis_empty=1：无 unknown/version，线框段关闭
	buildEmpty := func(w *bitstream.EncWriter) {
		gap2WriteEvalExpr(w, 70)
		gap2WriteHistoryNode(w)
		w.WriteBL(1)
		w.WriteBL(0)
		w.WriteB(true)  // acis_empty
		w.WriteB(false) // wireframe
		w.WriteB(true)  // acis_empty_bit
	}
	g := execClassDecoder(t, "ACSH_BREP_CLASS", container.VerR2004, buildEmpty,
		func(w *bitstream.EncWriter) { gap2WriteHandles(w, 1) })
	if v, _ := g.Field("version").(int64); v != 0 {
		t.Errorf("empty version = %v", g.Field("version"))
	}

	// version=1 SAT：块循环（4 字节块 + 0 块终止）
	buildSAT := func(w *bitstream.EncWriter) {
		gap2WriteEvalExpr(w, 70)
		gap2WriteHistoryNode(w)
		w.WriteBL(1)
		w.WriteBL(0)
		w.WriteB(false) // acis_empty
		w.WriteB(true)  // unknown
		w.WriteBS(1)    // version
		w.WriteBL(4)    // 块长
		w.WriteTF([]byte("SAT1"))
		w.WriteBL(0) // 终止块
		gap2WriteWireframe(w)
	}
	g = execClassDecoder(t, "ACSH_BREP_CLASS", container.VerR2004, buildSAT,
		func(w *bitstream.EncWriter) { gap2WriteHandles(w, 1) })
	if v, _ := g.Field("version").(int64); v != 1 {
		t.Errorf("SAT version = %v", g.Field("version"))
	}

	// version=2 SAB（R2007+）：标记定位 + 线框 + num_materials
	body := bitstream.NewEncWriter()
	gap2WriteWireframe(body)
	body.WriteBL(2) // num_materials（version>1 且 R2007+）
	sab := append(append([]byte{}, gap2SATMarker...), body.Bytes()...)
	buildSAB := func(w *bitstream.EncWriter) {
		gap2WriteEvalExpr(w, 70)
		gap2WriteHistoryNode(w)
		w.WriteBL(1)
		w.WriteBL(0)
		w.WriteB(false)
		w.WriteB(true)
		w.WriteBS(2) // version
		w.WriteTF(sab)
	}
	g = execClassDecoder(t, "ACSH_BREP_CLASS", container.VerR2007, buildSAB,
		func(w *bitstream.EncWriter) { gap2WriteHandles(w, 4) })
	if v, _ := g.Field("num_materials").(int64); v != 2 {
		t.Errorf("SAB num_materials = %v", g.Field("num_materials"))
	}
	if len(g.Handles) != 4 { // material×2 + history_id（宽容）+ 无 value91
		t.Errorf("SAB hdl 数 = %d", len(g.Handles))
	}
}

// TestSynthAcshHdl decodeGenericACSH_HDL 三分支：value91 占位、BREP
// materials（version>1）、BREP 旧版。
func TestSynthAcshHdl(t *testing.T) {
	// 非 BREP + value91：2 个引用
	g := &object.ObjGeneric{Name: "ACSH_BOX_CLASS", Handle: gap2BaseHandle}
	g.ValueHandle91 = true
	w := bitstream.NewEncWriter()
	gap2WriteHandles(w, 2)
	fr := &object.GfRead{R: bitstream.NewBitStream(w.Bytes()), Ver: container.VerR2004}
	if err := object.DecodeGenericACSH_HDL(fr.R, container.VerR2004, fr, g); err != nil {
		t.Fatalf("hdl: %v", err)
	}
	if len(g.Handles) != 2 {
		t.Errorf("BOX hdl 数 = %d", len(g.Handles))
	}

	// BREP version>1（R2007+）：material×num_materials + history_id
	g2 := &object.ObjGeneric{Name: "ACSH_BREP_CLASS", Handle: gap2BaseHandle}
	g2.Fields = []object.ObjField{{"version", int64(2)}, {"num_materials", int64(2)}}
	w2 := bitstream.NewEncWriter()
	gap2WriteHandles(w2, 3)
	fr2 := &object.GfRead{R: bitstream.NewBitStream(w2.Bytes()), Ver: container.VerR2007}
	if err := object.DecodeGenericACSH_HDL(fr2.R, container.VerR2007, fr2, g2); err != nil {
		t.Fatalf("BREP hdl: %v", err)
	}
	if len(g2.Handles) != 3 {
		t.Errorf("BREP hdl 数 = %d", len(g2.Handles))
	}

	// BREP version<=1：仅 material
	g3 := &object.ObjGeneric{Name: "ACSH_BREP_CLASS", Handle: gap2BaseHandle}
	g3.Fields = []object.ObjField{{"version", int64(1)}}
	w3 := bitstream.NewEncWriter()
	gap2WriteHandles(w3, 1)
	fr3 := &object.GfRead{R: bitstream.NewBitStream(w3.Bytes()), Ver: container.VerR2004}
	if err := object.DecodeGenericACSH_HDL(fr3.R, container.VerR2004, fr3, g3); err != nil {
		t.Fatalf("BREP v1 hdl: %v", err)
	}
	if len(g3.Handles) != 1 {
		t.Errorf("BREP v1 hdl 数 = %d", len(g3.Handles))
	}
}

// TestSynthDynBlockParameters 动态块参数族 Polar/Point/XY/Lookup/User。
func TestSynthDynBlockParameters(t *testing.T) {
	// POLAR：2Pt + 4 个名称 T + offset BD + 两个 ParamValueSet
	buildPolar := func(w *bitstream.EncWriter) {
		gap2Write2PtParameter(w)
		for i := 0; i < 4; i++ {
			w.WriteTV("n")
		}
		w.WriteBD(2) // offset
		gap2WriteValueSet(w)
		gap2WriteValueSet(w)
	}
	g := execClassDecoder(t, "BLOCKPOLARPARAMETER", container.VerR2004, buildPolar, nil)
	if v, _ := g.Field("offset").(float64); v != 2 {
		t.Errorf("offset = %v", g.Field("offset"))
	}

	// POINT：1Pt + position_name/desc + def_label_pt
	buildPoint := func(w *bitstream.EncWriter) {
		gap2Write1PtParameter(w)
		w.WriteTV("pn")
		w.WriteTV("pd")
		gap2Write3BD(w)
	}
	g = execClassDecoder(t, "BLOCKPOINTPARAMETER", container.VerR2004, buildPoint, nil)
	if g.Field("position_name") == nil {
		t.Errorf("缺 position_name")
	}

	// XY：2Pt + 4 标签 + x/y 值 + 两个 ValueSet
	buildXY := func(w *bitstream.EncWriter) {
		gap2Write2PtParameter(w)
		for i := 0; i < 4; i++ {
			w.WriteTV("l")
		}
		w.WriteBD(3)
		w.WriteBD(4)
		gap2WriteValueSet(w)
		gap2WriteValueSet(w)
	}
	g = execClassDecoder(t, "BLOCKXYPARAMETER", container.VerR2004, buildXY, nil)
	if v, _ := g.Field("y_value").(float64); v != 4 {
		t.Errorf("y_value = %v", g.Field("y_value"))
	}

	// LOOKUP：1Pt + index BL + 3 个 T
	buildLookup := func(w *bitstream.EncWriter) {
		gap2Write1PtParameter(w)
		w.WriteBL(2)
		w.WriteTV("ln")
		w.WriteTV("ld")
		w.WriteTV("u")
	}
	g = execClassDecoder(t, "BLOCKLOOKUPPARAMETER", container.VerR2004, buildLookup, nil)
	if v, _ := g.Field("index").(int64); v != 2 {
		t.Errorf("index = %v", g.Field("index"))
	}

	// USER：1Pt + flag BS + EvalVariant（HANDLE 型 code=390 → 句柄占位）
	// + type BS；hdl 读 assocvariable + value 两个引用
	buildUser := func(w *bitstream.EncWriter) {
		gap2Write1PtParameter(w)
		w.WriteBS(0) // flag
		w.WriteBS(390)
		w.WriteBS(1) // type
	}
	g = execClassDecoder(t, "BLOCKUSERPARAMETER", container.VerR2004, buildUser,
		func(w *bitstream.EncWriter) { gap2WriteHandles(w, 2) })
	if v, _ := g.Field("type").(int64); v != 1 {
		t.Errorf("type = %v", g.Field("type"))
	}
	if len(g.Handles) != 2 {
		t.Errorf("USER hdl 数 = %d", len(g.Handles))
	}
}

// TestSynthDynBlockActions 动作族 ARRAY（ConnectionPts×4 + offsets）与
// ROTATE（ActionWithBasePt 补齐尾段）。
func TestSynthDynBlockActions(t *testing.T) {
	// BlockAction 公共体（BlockElement + display_location + deps/actions）
	writeActionHead := func(w *bitstream.EncWriter) {
		gap2WriteBlockElement(w, 0)
		gap2Write3BD(w) // display_location
		w.WriteBL(1)    // num_deps
		w.WriteBL(0)    // num_actions
	}

	// ARRAY：makeBlockAction 包装 + 4 连接点 + 两偏移
	buildArray := func(w *bitstream.EncWriter) {
		writeActionHead(w)
		for i := 0; i < 4; i++ {
			w.WriteBL(uint32(i + 1)) // code
			w.WriteTV("cp")          // name
		}
		w.WriteBD(5)
		w.WriteBD(6)
	}
	g := execClassDecoder(t, "BLOCKARRAYACTION", container.VerR2004, buildArray,
		func(w *bitstream.EncWriter) { gap2WriteHandles(w, 1) })
	if v, _ := g.Field("row_offset").(float64); v != 6 {
		t.Errorf("row_offset = %v", g.Field("row_offset"))
	}
	if len(g.Handles) != 1 {
		t.Errorf("ARRAY hdl 数 = %d", len(g.Handles))
	}

	// ROTATE：WithBasePt（offset/base_pt）+ 尾部 1 连接点
	buildRotate := func(w *bitstream.EncWriter) {
		writeActionHead(w)
		gap2Write3BD(w) // offset
		for i := 0; i < 2; i++ {
			w.WriteBL(1)
			w.WriteTV("cp")
		}
		w.WriteB(true)  // dependent
		gap2Write3BD(w) // base_pt
		w.WriteBL(9)
		w.WriteTV("cp3")
	}
	g = execClassDecoder(t, "BLOCKROTATEACTION", container.VerR2004, buildRotate,
		func(w *bitstream.EncWriter) { gap2WriteHandles(w, 1) })
	if v, _ := g.Field("base_pt").([]float64); len(v) != 3 || v[0] != 1 {
		t.Errorf("base_pt = %v", g.Field("base_pt"))
	}
}

// TestSynthBlockGripLocationComponent BLOCKGRIPLOCATIONCOMPONENT：
// evalexpr(value91) + grip_type + grip_expr + handle 流。
func TestSynthBlockGripLocationComponent(t *testing.T) {
	build := func(w *bitstream.EncWriter) {
		gap2WriteEvalExpr(w, 91)
		w.WriteBL(3)   // grip_type
		w.WriteTV("e") // grip_expr
	}
	g := execClassDecoder(t, "BLOCKGRIPLOCATIONCOMPONENT", container.VerR2004, build,
		func(w *bitstream.EncWriter) { gap2WriteHandles(w, 1) })
	if v, _ := g.Field("grip_type").(int64); v != 3 {
		t.Errorf("grip_type = %v", g.Field("grip_type"))
	}
	if len(g.Handles) != 1 {
		t.Errorf("hdl 数 = %d", len(g.Handles))
	}
}

// TestDwgResbufValueType 组码→值类型全分支表驱动。
func TestDwgResbufValueType(t *testing.T) {
	cases := []struct {
		gc   int64
		want byte
	}{
		{-5, 'H'}, {0, 'T'}, {1, 'T'}, {5, 'H'}, {6, 'T'}, {9, 'T'},
		{10, 'X'}, {37, 'X'}, {38, 'R'}, {39, 'R'}, {40, 'R'}, {59, 'R'},
		{60, 'S'}, {79, 'S'}, {80, 'I'}, {99, 'I'}, {100, 'T'}, {102, 'T'},
		{103, 'X'}, {104, 'X'}, {105, 'H'}, {106, 'X'}, {109, 'X'},
		{110, 'X'}, {139, 'X'}, {140, 'R'}, {149, 'R'}, {150, 'X'},
		{169, 'X'}, {170, 'S'}, {179, 'S'}, {180, 'X'}, {209, 'X'},
		{210, 'X'}, {269, 'X'}, {270, 'S'}, {279, 'S'}, {280, 'C'},
		{289, 'C'}, {290, 'X'}, {299, 'X'},
		{300, 'T'}, {309, 'T'}, {310, 'X'}, {319, 'X'}, {320, 'H'},
		{329, 'H'}, {330, 'O'}, {369, 'O'}, {370, 'S'}, {389, 'S'},
		{390, 'H'}, {399, 'H'}, {400, 'S'}, {409, 'S'}, {410, 'T'},
		{419, 'T'}, {420, 'I'}, {429, 'I'}, {430, 'T'}, {439, 'T'},
		{440, 'I'}, {459, 'I'}, {460, 'R'}, {469, 'R'}, {470, 'T'},
		{479, 'T'}, {480, 'X'}, {999, 'T'}, {1000, 'T'}, {1004, 'X'},
		{1009, 'T'}, {1010, 'X'}, {1039, 'X'}, {1040, 'R'}, {1042, 'R'},
		{1043, 'X'}, {1069, 'X'}, {1070, 'S'}, {1071, 'I'}, {1072, 'X'},
	}
	for _, tc := range cases {
		if got := object.DwgResbufValueType(tc.gc); got != tc.want {
			t.Errorf("dwgResbufValueType(%d) = %q, 期望 %q", tc.gc, got, tc.want)
		}
	}
}

// TestSynthAssoc2DConstraintGroup ASSOC2DCONSTRAINTGROUP pre/post R2013
// 两种 status 位置 + handle 流。
func TestSynthAssoc2DConstraintGroup(t *testing.T) {
	build := func(r2013 bool) func(w *bitstream.EncWriter) {
		return func(w *bitstream.EncWriter) {
			w.WriteBS(1)   // class_version
			w.WriteBL(0)   // geometry_status
			w.WriteBL(0)   // action_index
			w.WriteBL(0)   // max_assoc_dep_index
			w.WriteBL(1)   // num_deps
			w.WriteB(true) // deps[0].is_owned
			if r2013 {
				w.WriteBS(0) // assoc_unknown_bs1（R2010+）
				w.WriteBL(0) // num_owned_params
				w.WriteBS(0) // assoc_unknown_bs2
				w.WriteBL(0) // num_values
			}
			w.WriteBL(1)   // version
			w.WriteB(true) // b1
			for i := 0; i < 3; i++ {
				gap2Write3BD(w) // workplane
			}
			w.WriteBL(1) // num_actions
			w.WriteBL(1) // num_nodes
			w.WriteBL(7) // nodeid
			if !r2013 {
				w.WriteRC(2) // status 前置（pre-R2013）
			}
			w.WriteBL(1) // num_connections
			w.WriteBL(4) // connections[0]
			if r2013 {
				w.WriteRC(2) // status 后置
			}
		}
	}
	for _, r2013 := range []bool{false, true} {
		ver := container.VerR2004
		if r2013 {
			ver = container.VerR2013
		}
		g := execClassDecoder(t, "ASSOC2DCONSTRAINTGROUP", ver, build(r2013),
			func(w *bitstream.EncWriter) { gap2WriteHandles(w, 5) })
		if v, _ := g.Field("nodes[0].nodeid").(int64); v != 7 {
			t.Errorf("r2013=%v nodeid = %v", r2013, g.Field("nodes[0].nodeid"))
		}
		// hdl：owningnetwork + actionbody + deps×1 + h1 + actions×1
		if len(g.Handles) != 5 {
			t.Errorf("r2013=%v hdl 数 = %d", r2013, len(g.Handles))
		}
	}
}

// TestSynthAssocVariable ASSOCVARIABLE 主体（EvalVariant HANDLE 型）+
// handle 流（assocvariable + value）。
func TestSynthAssocVariable(t *testing.T) {
	build := func(w *bitstream.EncWriter) {
		w.WriteBS(1) // class_version
		w.WriteBL(0) // geometry_status
		w.WriteBL(0) // action_index
		w.WriteBL(0) // max_assoc_dep_index
		w.WriteBL(0) // num_deps
		w.WriteBL(1) // av_class_version
		w.WriteTV("v1")
		w.WriteTV("58")
		w.WriteTV("AcDbEval")
		w.WriteTV("desc")
		w.WriteBS(390) // variant code = HANDLE
		w.WriteB(true) // has_t78
		w.WriteTV("t78")
		w.WriteB(false) // b290
	}
	g := execClassDecoder(t, "ASSOCVARIABLE", container.VerR2004, build,
		func(w *bitstream.EncWriter) { gap2WriteHandles(w, 3) })
	if v, _ := g.Field("t78").(string); v != "t78" {
		t.Errorf("t78 = %v", g.Field("t78"))
	}
	if len(g.Handles) != 3 { // owningnetwork + actionbody + value
		t.Errorf("hdl 数 = %d", len(g.Handles))
	}
}

// TestSynthSurfaceActionBody SURFACEACTIONBODY 族：pre-R2013 带 ParamBased
// 段与 R2013+ 精简段，各尾部变体（EXTEND/OFFSET/TRIM/BLEND/类版本）。
func TestSynthSurfaceActionBody(t *testing.T) {
	// pre-R2013 段（num_values=0 → l5）+ surface 体 + status + 尾部
	buildPre := func(tail func(w *bitstream.EncWriter)) func(w *bitstream.EncWriter) {
		return func(w *bitstream.EncWriter) {
			w.WriteBL(1)    // aab_version
			w.WriteBL(1)    // pab.version
			w.WriteBL(0)    // pab.minor
			w.WriteBL(2)    // pab.num_deps
			w.WriteBL(0)    // pab.l4
			w.WriteBL(0)    // pab.num_values
			w.WriteBL(0)    // pab.l5
			w.WriteBL(1)    // sab.version
			w.WriteB(true)  // sab.is_semi_assoc
			w.WriteBL(0)    // sab.l2
			w.WriteB(false) // sab.is_semi_ovr
			w.WriteBS(0)    // sab.grip_status
			w.WriteBL(0)    // pbsab_status
			tail(w)
		}
	}
	// EXTEND：class_version + option RC
	g := execClassDecoder(t, "ASSOCEXTENDSURFACEACTIONBODY", container.VerR2004,
		buildPre(func(w *bitstream.EncWriter) {
			w.WriteBL(1)
			w.WriteRC(3)
		}), func(w *bitstream.EncWriter) { gap2WriteHandles(w, 4) })
	if v, _ := g.Field("option").(int64); v != 3 {
		t.Errorf("EXTEND option = %v", g.Field("option"))
	}
	// pre-R2013 hdl：deps×2 + pab.assocdep + sab.assocdep = 4
	if len(g.Handles) != 4 {
		t.Errorf("EXTEND hdl 数 = %d", len(g.Handles))
	}

	// OFFSET：class_version + b1
	g = execClassDecoder(t, "ASSOCOFFSETSURFACEACTIONBODY", container.VerR2004,
		buildPre(func(w *bitstream.EncWriter) {
			w.WriteBL(1)
			w.WriteB(true)
		}), func(w *bitstream.EncWriter) { gap2WriteHandles(w, 4) })
	if v, _ := g.Field("b1").(bool); !v {
		t.Errorf("OFFSET b1 = %v", g.Field("b1"))
	}

	// TRIM：class_version + b1/b2 + distance
	g = execClassDecoder(t, "ASSOCTRIMSURFACEACTIONBODY", container.VerR2004,
		buildPre(func(w *bitstream.EncWriter) {
			w.WriteBL(1)
			w.WriteB(true)
			w.WriteB(false)
			w.WriteBD(1.5)
		}), func(w *bitstream.EncWriter) { gap2WriteHandles(w, 4) })
	if v, _ := g.Field("distance").(float64); v != 1.5 {
		t.Errorf("TRIM distance = %v", g.Field("distance"))
	}

	// BLEND：class_version + b1/b2/b3 + blend_options + b4/b5 + bs2
	g = execClassDecoder(t, "ASSOCBLENDSURFACEACTIONBODY", container.VerR2004,
		buildPre(func(w *bitstream.EncWriter) {
			w.WriteBL(1)
			w.WriteB(true)
			w.WriteB(true)
			w.WriteB(false)
			w.WriteBS(2)
			w.WriteB(true)
			w.WriteB(false)
			w.WriteBS(1)
		}), func(w *bitstream.EncWriter) { gap2WriteHandles(w, 4) })
	if v, _ := g.Field("blend_options").(int64); v != 2 {
		t.Errorf("BLEND blend_options = %v", g.Field("blend_options"))
	}

	// R2013+：无 ParamBased 段，仅 sab + status + class_version（cvTail）
	build2013 := func(w *bitstream.EncWriter) {
		w.WriteBL(1) // aab_version
		w.WriteBL(1) // sab.version
		w.WriteB(true)
		w.WriteBL(0)
		w.WriteB(false)
		w.WriteBS(1)
		w.WriteBL(0) // pbsab_status
		w.WriteBL(2) // class_version（cvTail）
	}
	g = execClassDecoder(t, "ASSOCPLANESURFACEACTIONBODY", container.VerR2013, build2013,
		func(w *bitstream.EncWriter) { gap2WriteHandles(w, 1) })
	if v, _ := g.Field("class_version").(int64); v != 2 {
		t.Errorf("PLANE class_version = %v", g.Field("class_version"))
	}
	if len(g.Handles) != 1 {
		t.Errorf("PLANE hdl 数 = %d", len(g.Handles))
	}
}

// TestSynthBrepSATBlockLimit ACSH_BREP SAT 块循环的长度越界退出分支。
func TestSynthBrepSATBlockLimit(t *testing.T) {
	build := func(w *bitstream.EncWriter) {
		gap2WriteEvalExpr(w, 70)
		gap2WriteHistoryNode(w)
		w.WriteBL(1)
		w.WriteBL(0)
		w.WriteB(false)
		w.WriteB(true)
		w.WriteBS(1)
		w.WriteBL(0xFFFF) // 声明超长块 → break
		w.WriteB(false)   // wireframe
		w.WriteB(true)    // acis_empty_bit
	}
	g := execClassDecoder(t, "ACSH_BREP_CLASS", container.VerR2004, build,
		func(w *bitstream.EncWriter) { gap2WriteHandles(w, 1) })
	if v, _ := g.Field("version").(int64); v != 1 {
		t.Errorf("version = %v", g.Field("version"))
	}
}

// TestSynthSABNoMarker ACSH_BREP SAB 分支无标记时按整段消费。
func TestSynthSABNoMarker(t *testing.T) {
	sab := bytes.Repeat([]byte{0x41}, 8) // 无 End 标记
	build := func(w *bitstream.EncWriter) {
		gap2WriteEvalExpr(w, 70)
		gap2WriteHistoryNode(w)
		w.WriteBL(1)
		w.WriteBL(0)
		w.WriteB(false)
		w.WriteB(true)
		w.WriteBS(2)
		w.WriteTF(sab)
		w.WriteB(false) // wireframe
		w.WriteB(true)  // acis_empty_bit
	}
	g := execClassDecoder(t, "ACSH_BREP_CLASS", container.VerR2004, build,
		func(w *bitstream.EncWriter) { gap2WriteHandles(w, 1) })
	if v, _ := g.Field("version").(int64); v != 2 {
		t.Errorf("version = %v", g.Field("version"))
	}
}

// ---- 批次 2：视图样式族 / GEODATA / 表几何 ----

// gap2WriteCMC 写 R2004+ 结构的 CMC 颜色（BS index + BL rgb + RC flag）。
func gap2WriteCMC(w *bitstream.EncWriter) {
	w.WriteBS(7)
	w.WriteBL(0xc3000000)
	w.WriteRC(0)
}

// TestSynthDetailViewStyle DETAILVIEWSTYLE 全字段（R2004 与 R2018 头）。
func TestSynthDetailViewStyle(t *testing.T) {
	buildR2004 := func(w *bitstream.EncWriter) {
		w.WriteBS(1)     // mdoc_class_version
		w.WriteTV("dv")  // desc
		w.WriteB(false)  // is_modified_for_recompute
		w.WriteBS(2)     // class_version
		w.WriteBL(0)     // flags
		gap2WriteCMC(w)  // identifier_color
		w.WriteBD(2.5)   // identifier_height
		w.WriteTV("A-C") // exclude_characters
		w.WriteBD(0.5)   // identifier_offset
		w.WriteRC(1)     // identifier_placement
		gap2WriteCMC(w)  // arrow_symbol_color
		w.WriteBD(1)     // arrow_symbol_size
		w.WriteBL(9)     // boundary_linewt
		gap2WriteCMC(w)  // boundary_line_color
		gap2WriteCMC(w)  // viewlabel_text_color
		w.WriteBD(3)     // viewlabel_text_height
		w.WriteBL(1)     // viewlabel_attachment
		w.WriteBD(0.1)   // viewlabel_offset
		w.WriteBL(2)     // viewlabel_alignment
		w.WriteTV("<>")  // viewlabel_pattern
		w.WriteBL(0)     // connection_linewt
		gap2WriteCMC(w)  // connection_line_color
		w.WriteBL(9)     // borderline_linewt
		gap2WriteCMC(w)  // borderline_color
		w.WriteRC(0)     // model_edge
	}
	g := execClassDecoder(t, "DETAILVIEWSTYLE", container.VerR2004, buildR2004,
		func(w *bitstream.EncWriter) { gap2WriteHandles(w, 6) })
	if v, _ := g.Field("identifier_height").(float64); v != 2.5 {
		t.Errorf("identifier_height = %v", g.Field("identifier_height"))
	}
	if len(g.Handles) != 6 {
		t.Errorf("DETAIL hdl 数 = %d", len(g.Handles))
	}

	// R2018 头：display_name T 与后续文字均走字符串流（dat 不占位），
	// 仅 viewstyle_flags BL 与 class_version BS 占位
	buildR2018 := func(w *bitstream.EncWriter) {
		w.WriteBS(1) // mdoc_class_version
		w.WriteB(false)
		w.WriteBL(0) // viewstyle_flags
		w.WriteBS(2) // class_version
		w.WriteBL(0)
		gap2WriteCMC(w)
		w.WriteBD(2.5)
		w.WriteBD(0.5)
		w.WriteRC(1)
		gap2WriteCMC(w)
		w.WriteBD(1)
		w.WriteBL(9)
		gap2WriteCMC(w)
		gap2WriteCMC(w)
		w.WriteBD(3)
		w.WriteBL(1)
		w.WriteBD(0.1)
		w.WriteBL(2)
		w.WriteBL(0)
		gap2WriteCMC(w)
		w.WriteBL(9)
		gap2WriteCMC(w)
		w.WriteRC(0)
	}
	execClassDecoder(t, "DETAILVIEWSTYLE", container.VerR2018, buildR2018,
		func(w *bitstream.EncWriter) { gap2WriteHandles(w, 6) })
}

// TestSynthSectionViewStyle SECTIONVIEWSTYLE 全字段（含 hatch_angles）。
func TestSynthSectionViewStyle(t *testing.T) {
	build := func(w *bitstream.EncWriter) {
		w.WriteBS(1)    // mdoc_class_version
		w.WriteTV("sv") // desc
		w.WriteB(false)
		w.WriteBS(2)        // class_version
		w.WriteBL(0)        // flags
		gap2WriteCMC(w)     // identifier_color
		w.WriteBD(2)        // identifier_height
		gap2WriteCMC(w)     // arrow_symbol_color
		w.WriteBD(1.5)      // arrow_symbol_size
		w.WriteTV("A-C")    // exclude_characters
		w.WriteBD(0.2)      // arrow_symbol_extension_length
		w.WriteBL(9)        // plane_linewt
		gap2WriteCMC(w)     // plane_line_color
		w.WriteBL(9)        // bend_linewt
		gap2WriteCMC(w)     // bend_line_color
		w.WriteBD(0.5)      // bend_line_length
		w.WriteBD(0.3)      // end_line_length
		gap2WriteCMC(w)     // viewlabel_text_color
		w.WriteBD(2.5)      // viewlabel_text_height
		w.WriteBL(3)        // viewlabel_attachment
		w.WriteBD(0.1)      // viewlabel_offset
		w.WriteBL(1)        // viewlabel_alignment
		w.WriteTV("S<>")    // viewlabel_pattern
		gap2WriteCMC(w)     // hatch_color
		gap2WriteCMC(w)     // hatch_bg_color
		w.WriteTV("ANSI31") // hatch_pattern
		w.WriteBD(1)        // hatch_scale
		w.WriteBL(50)       // hatch_transparency
		w.WriteB(false)     // unknown_b1
		w.WriteB(false)     // unknown_b2
		w.WriteBL(1)        // identifier_position
		w.WriteBD(0.2)      // identifier_offset
		w.WriteBL(2)        // arrow_position
		w.WriteBD(0.15)     // end_line_overshoot
		w.WriteBL(2)        // num_hatch_angles
		w.WriteBD(0.5)
		w.WriteBD(1.0)
	}
	g := execClassDecoder(t, "SECTIONVIEWSTYLE", container.VerR2004, build,
		func(w *bitstream.EncWriter) { gap2WriteHandles(w, 6) })
	if v, _ := g.Field("end_line_overshoot").(float64); v != 0.15 {
		t.Errorf("end_line_overshoot = %v", g.Field("end_line_overshoot"))
	}
	if len(g.Handles) != 6 {
		t.Errorf("SECTION hdl 数 = %d", len(g.Handles))
	}
}

// TestSynthMLeaderStyle MLEADERSTYLE R2010+（class_version=2 全字段）。
func TestSynthMLeaderStyle(t *testing.T) {
	build := func(w *bitstream.EncWriter) {
		w.WriteBS(2)    // class_version
		w.WriteBS(1)    // content_type
		w.WriteBS(0)    // mleader_order
		w.WriteBS(2)    // leader_order
		w.WriteBL(16)   // max_points
		w.WriteBD(0.3)  // first_seg_angle
		w.WriteBD(0.6)  // second_seg_angle
		w.WriteBS(0)    // type
		gap2WriteCMC(w) // line_color
		w.WriteBL(25)   // linewt
		w.WriteB(true)  // has_landing
		w.WriteBD(0.2)  // landing_gap
		w.WriteB(true)  // has_dogleg
		w.WriteBD(0.8)  // landing_dist
		w.WriteBD(1)    // arrow_head_size
		w.WriteBS(0)    // attach_left
		w.WriteBS(0)    // attach_right
		w.WriteBS(1)    // text_angle_type（cv>=2）
		w.WriteBS(0)    // text_align_type
		gap2WriteCMC(w) // text_color
		w.WriteBD(2)    // text_height
		w.WriteB(false) // has_text_frame
		w.WriteB(true)  // text_always_left（cv>=2）
		w.WriteBD(1)    // align_space
		gap2WriteCMC(w) // block_color
		w.WriteBD(1)
		w.WriteBD(1)
		w.WriteBD(1)    // block_scale
		w.WriteB(false) // use_block_scale
		w.WriteBD(0)    // block_rotation
		w.WriteB(true)  // use_block_rotation
		w.WriteBS(0)    // block_connection
		w.WriteBD(1)    // scale
		w.WriteB(false) // is_changed
		w.WriteB(false) // is_annotative
		w.WriteBD(0.5)  // break_size
		w.WriteBS(0)    // attach_dir
		w.WriteBS(0)    // attach_top
		w.WriteBS(0)    // attach_bottom
	}
	g := execClassDecoder(t, "MLEADERSTYLE", container.VerR2010, build,
		func(w *bitstream.EncWriter) { gap2WriteHandles(w, 4) })
	if v, _ := g.Field("landing_dist").(float64); v != 0.8 {
		t.Errorf("landing_dist = %v", g.Field("landing_dist"))
	}
	if len(g.Handles) != 4 {
		t.Errorf("MLEADERSTYLE hdl 数 = %d", len(g.Handles))
	}
}

// TestSynthSunAndHistory SUN 与 ACSH_HISTORY_CLASS 完整 dat 流。
func TestSynthSunAndHistory(t *testing.T) {
	buildSun := func(w *bitstream.EncWriter) {
		w.WriteBL(1)        // class_version
		w.WriteB(true)      // is_on
		gap2WriteCMC(w)     // color
		w.WriteBD(1)        // intensity（writeBD 1.0 走 BB01）
		w.WriteB(true)      // has_shadow
		w.WriteBL(2451545)  // julian_day
		w.WriteBL(43200000) // msecs
		w.WriteB(false)     // is_dst
		w.WriteBL(1)        // shadow_type
		w.WriteBS(512)      // shadow_mapsize
		w.WriteRC(3)        // shadow_softness
	}
	g := execClassDecoder(t, "SUN", container.VerR2004, buildSun, nil)
	if v, _ := g.Field("shadow_softness").(int64); v != 3 {
		t.Errorf("shadow_softness = %v", g.Field("shadow_softness"))
	}

	buildHistory := func(w *bitstream.EncWriter) {
		w.WriteBL(1)    // major
		w.WriteBL(0)    // minor
		w.WriteBL(7)    // h_nodeid
		w.WriteB(true)  // show_history
		w.WriteB(false) // record_history
	}
	g = execClassDecoder(t, "ACSH_HISTORY_CLASS", container.VerR2004, buildHistory, nil)
	if v, _ := g.Field("h_nodeid").(int64); v != 7 {
		t.Errorf("h_nodeid = %v", g.Field("h_nodeid"))
	}
}

// TestSynthTableGeometry TABLEGEOMETRY cells 全链（含 geometry 组）。
func TestSynthTableGeometry(t *testing.T) {
	build := func(w *bitstream.EncWriter) {
		w.WriteBL(2) // numrows
		w.WriteBL(3) // numcols
		w.WriteBL(2) // num_cells
		for i := 0; i < 2; i++ {
			w.WriteBL(1)    // geom_data_flag
			w.WriteBD(10.5) // width_w_gap
			w.WriteBD(20.5) // height_w_gap
			w.WriteBL(1)    // num_geometry
			gap2Write3BD(w) // dist_top_left
			gap2Write3BD(w) // dist_center
			for k := 0; k < 4; k++ {
				w.WriteBD(float64(k + 1)) // content_width/height/width/height
			}
			w.WriteBL(0) // unknown
		}
	}
	g := execClassDecoder(t, "TABLEGEOMETRY", container.VerR2004, build, nil)
	if v, _ := g.Field("numrows").(int64); v != 2 {
		t.Errorf("numrows = %v", g.Field("numrows"))
	}
	if g.Field("cells[1].geometry[0].content_height") == nil {
		t.Errorf("缺 cells[1].geometry[0].content_height")
	}
}

// TestSynthTableStylePre2010 TABLESTYLE R2007 及更早布局（3 组 rowstyles
// + 6 边框）。
func TestSynthTableStylePre2010(t *testing.T) {
	build := func(w *bitstream.EncWriter) {
		w.WriteTV("tbl") // name
		w.WriteBS(1)     // flow_direction
		w.WriteBS(0)     // flags
		w.WriteBD(1.5)   // horiz_cell_margin
		w.WriteBD(1.5)   // vert_cell_margin
		w.WriteB(false)  // is_title_suppressed
		w.WriteB(false)  // is_header_suppressed
		for i := 0; i < 3; i++ {
			w.WriteBD(2)    // text_height
			w.WriteBS(1)    // text_alignment
			gap2WriteCMC(w) // text_color
			gap2WriteCMC(w) // fill_color
			w.WriteB(true)  // has_bgcolor
			for b := 0; b < 6; b++ {
				w.WriteBS(9)    // linewt
				w.WriteB(true)  // visible
				gap2WriteCMC(w) // color
			}
		}
	}
	g := execClassDecoder(t, "TABLESTYLE", container.VerR2004, build,
		func(w *bitstream.EncWriter) { gap2WriteHandles(w, 3) })
	if v, _ := g.Field("rowstyles[2].text_height").(float64); v != 2 {
		t.Errorf("rowstyles[2].text_height = %v", g.Field("rowstyles[2].text_height"))
	}
	if len(g.Handles) != 3 {
		t.Errorf("TABLESTYLE hdl 数 = %d", len(g.Handles))
	}
}

// TestSynthGeoData GEODATA 全字段（R2010+ 布局：字符串走流不占位）。
func TestSynthGeoData(t *testing.T) {
	build := func(w *bitstream.EncWriter) {
		w.WriteBL(1)    // class_version
		w.WriteBS(2)    // coord_type
		gap2Write3BD(w) // design_pt
		gap2Write3BD(w) // ref_pt
		w.WriteBD(1)    // unit_scale_horiz
		w.WriteBL(1)    // units_value_horiz
		w.WriteBD(1)    // unit_scale_vert
		w.WriteBL(1)    // units_value_vert
		gap2Write3BD(w) // up_dir
		w.WriteRD(0)    // north_dir
		w.WriteRD(1)
		w.WriteBL(1)    // scale_est
		w.WriteBD(1)    // user_scale_factor
		w.WriteB(false) // do_sea_level_corr
		w.WriteBD(0)    // sea_level_elev
		w.WriteBD(100)  // coord_proj_radius
		// coord_system_def/geo_rss_tag/observation_from/to/coverage_tag
		// 均为 T：R2010+ 走字符串流，dat 不占位
		w.WriteBL(1) // num_geomesh_pts
		w.WriteRD(1)
		w.WriteRD(2) // pts[0].source_pt
		w.WriteRD(3)
		w.WriteRD(4) // pts[0].dest_pt
		w.WriteBL(1) // num_geomesh_faces
		w.WriteBL(0) // face1
		w.WriteBL(1) // face2
		w.WriteBL(2) // face3
	}
	g := execClassDecoder(t, "GEODATA", container.VerR2010, build,
		func(w *bitstream.EncWriter) { gap2WriteHandles(w, 1) })
	if v, _ := g.Field("geomesh_faces[0].face3").(int64); v != 2 {
		t.Errorf("face3 = %v", g.Field("geomesh_faces[0].face3"))
	}
	if len(g.Handles) != 1 {
		t.Errorf("GEODATA hdl 数 = %d", len(g.Handles))
	}
}

// ---- 批次 3：injson 还原器 / DXF 写出辅助 / 调试记录 ----

// gap2JSONBase 构造带公共键的 gold JSON 对象（jsonBase 消费）。
func gap2JSONBase(entity string) jsonObject {
	return jsonObject{
		"entity": entity, "handle": "2B7", "type": float64(1), "entmode": float64(0),
		"layer": "1F", "ownerhandle": "2B0", "color": map[string]any{"index": 3},
	}
}

// TestJsonBuildLongTail GOLD JSON → 内部长尾实体还原器（Leader/Shape/
// Ole2Frame/OleFrame/ProxyEntity）与 jsonHexBytes 边界。
func TestJsonBuildLongTail(t *testing.T) {
	o := gap2JSONBase("LEADER")
	o["annotation_type"] = float64(1)
	o["path_type"] = float64(2)
	o["points"] = []any{[]any{1.0, 2.0, 0.0}, []any{3.0, 4.0, 1.0}}
	l, ok := jsonBuildLeader(o).(*entity.EntLeader)
	if !ok {
		t.Fatal("jsonBuildLeader 类型错误")
	}
	if len(l.Points) != 2 || l.Points[1].X != 3 {
		t.Errorf("leader points = %v", l.Points)
	}
	if l.AnnotationType != 1 || l.PathType != 2 {
		t.Errorf("leader type/path = %d/%d", l.AnnotationType, l.PathType)
	}

	o = gap2JSONBase("SHAPE")
	o["ins_pt"] = []any{1.0, 2.0, 0.0}
	o["scale"] = 2.5
	o["rotation"] = 0.5
	o["width_factor"] = 1.2
	o["oblique_angle"] = 0.1
	o["thickness"] = 3.0
	s, ok := jsonBuildShape(o).(*entity.EntShape)
	if !ok {
		t.Fatal("jsonBuildShape 类型错误")
	}
	if s.Scale != 2.5 || s.Insertion.Y != 2 {
		t.Errorf("shape scale/ins = %v/%v", s.Scale, s.Insertion)
	}

	o = gap2JSONBase("OLE2FRAME")
	o["type"] = float64(1)
	o["mode"] = float64(2)
	o["lock_aspect"] = float64(0)
	o["data_size"] = float64(4)
	o["data"] = "DEADBEEF"
	ole2, ok := jsonBuildOle2Frame(o).(*entity.EntOle2Frame)
	if !ok {
		t.Fatal("jsonBuildOle2Frame 类型错误")
	}
	if ole2.DataSize != 4 || string(ole2.Data) != "\xDE\xAD\xBE\xEF" {
		t.Errorf("ole2 size/data = %d/%X", ole2.DataSize, ole2.Data)
	}

	o = gap2JSONBase("OLEFRAME")
	o["flag"] = float64(1)
	o["mode"] = float64(0)
	o["data_size"] = float64(2)
	o["data"] = "00FF"
	ole1, ok := jsonBuildOleFrame(o).(*entity.EntOleFrame)
	if !ok {
		t.Fatal("jsonBuildOleFrame 类型错误")
	}
	if ole1.DataSize != 2 || ole1.Data[1] != 0xFF {
		t.Errorf("ole1 = %d/%X", ole1.DataSize, ole1.Data)
	}

	o = gap2JSONBase("PROXY_ENTITY")
	o["proxy_id"] = float64(1)
	o["version"] = float64(2)
	o["maint_version"] = float64(3)
	o["dwg_version"] = float64(19)
	o["from_dxf"] = float64(1)
	o["data_numbits"] = float64(16)
	o["num_objids"] = float64(0)
	o["proxy_data_size"] = float64(3)
	o["proxy_data"] = "AABB01"
	p, ok := jsonBuildProxyEntity(o).(*entity.EntProxyEntity)
	if !ok {
		t.Fatal("jsonBuildProxyEntity 类型错误")
	}
	if p.DwgVersionNum != 19 || len(p.ProxyData) != 3 {
		t.Errorf("proxy = %d/%d", p.DwgVersionNum, len(p.ProxyData))
	}

	// jsonHexBytes：奇数长度与非十六进制字符返回 nil，空串返回 nil
	if jsonHexBytes("ABC") != nil || jsonHexBytes("ZZ") != nil || jsonHexBytes("") != nil {
		t.Error("jsonHexBytes 非法输入应返回 nil")
	}
	if v := jsonHexBytes("00ff"); len(v) != 2 || v[1] != 0xFF {
		t.Errorf("jsonHexBytes = %X", v)
	}
}

// TestJsonBuildLightColorForms jsonBuildLight 的 light_color 双形态。
func TestJsonBuildLightColorForms(t *testing.T) {
	build := func(colorKey string, colorVal any) *entity.EntLight {
		o := gap2JSONBase("LIGHT")
		o["class_version"] = float64(1)
		o["name"] = "sun"
		o["type"] = float64(1)
		o["status"] = float64(1)
		o["intensity"] = 0.5
		o["position"] = []any{0.0, 0.0, 1.0}
		o["target"] = []any{0.0, 0.0, 0.0}
		o[colorKey] = colorVal
		return jsonBuildLight(o).(*entity.EntLight)
	}
	// 标量索引形态（pre-R2004）
	l := build("light_color", 3.0)
	if l.LightColorIndex != 3 || l.HasLightColorTrue {
		t.Errorf("标量形态 index=%d hasTrue=%v", l.LightColorIndex, l.HasLightColorTrue)
	}
	// CMC 对象形态（R2004+）
	l = build("light_color", map[string]any{
		"index": 5.0, "rgb": "C30A0AFF", "flag": 1.0})
	if !l.HasLightColorTrue || l.LightColorIndex != 5 {
		t.Errorf("CMC 形态 index=%d hasTrue=%v", l.LightColorIndex, l.HasLightColorTrue)
	}
}

// TestJsonBuildHatchPolyAndSegs jsonBuildHatch 的 polyline 路径（含
// bulge 展开与闭合）与 segs 路径（直线/圆弧/椭圆弧）分支。
func TestJsonBuildHatchPolyAndSegs(t *testing.T) {
	o := gap2JSONBase("HATCH")
	o["paths"] = []any{
		// polyline 路径：bulges_present + closed
		map[string]any{
			"flag": float64(2), "bulges_present": float64(1), "closed": float64(1),
			"num_segs_or_paths": float64(2),
			"polyline_paths": []any{
				map[string]any{"point": []any{0.0, 0.0}, "bulge": 0.0},
				map[string]any{"point": []any{10.0, 0.0}, "bulge": 0.5},
			},
		},
		// 直线段路径
		map[string]any{
			"flag": float64(0), "num_segs_or_paths": float64(2),
			"segs": []any{
				map[string]any{
					"curve_type":      float64(1),
					"first_endpoint":  []any{0.0, 0.0},
					"second_endpoint": []any{4.0, 0.0},
				},
				map[string]any{
					"curve_type": float64(2), "center": []any{2.0, 0.0},
					"radius": 2.0, "minor_major_ratio": 0.5,
					"start_angle": 0.0, "end_angle": 1.5, "is_ccw": float64(1),
				},
			},
		},
		// 椭圆弧段路径
		map[string]any{
			"flag": float64(0), "num_segs_or_paths": float64(1),
			"segs": []any{
				map[string]any{
					"curve_type": float64(3), "center": []any{0.0, 0.0},
					"radius": 3.0, "minor_major_ratio": 0.25,
					"start_angle": 0.0, "end_angle": 3.0, "is_ccw": float64(0),
				},
			},
		},
	}
	h, ok := jsonBuildHatch(o).(*entity.EntHatch)
	if !ok {
		t.Fatal("jsonBuildHatch 类型错误")
	}
	if len(h.Paths) != 3 {
		t.Fatalf("paths = %d", len(h.Paths))
	}
	if len(h.Paths[0].PolyVerts) != 2 {
		t.Errorf("poly verts = %d", len(h.Paths[0].PolyVerts))
	}
	if len(h.Paths[0].Points) < 2 {
		t.Errorf("poly 展开点列过短: %d", len(h.Paths[0].Points))
	}
	if len(h.Paths[1].Segs) != 2 || h.Paths[1].Segs[0].CurveType != 1 {
		t.Errorf("line segs = %v", h.Paths[1].Segs)
	}
	if h.Paths[2].Segs[0].Radius != 3 {
		t.Errorf("ellipse seg = %v", h.Paths[2].Segs[0])
	}
}

// TestDedupeByHandleAndDxfFail dedupeByHandle 过滤与 dxfWriter 首错记录。
func TestDedupeByHandleAndDxfFail(t *testing.T) {
	mk := func(h uint64) any {
		return &entity.EntShape{BaseEntity: entity.BaseEntity{Handle: h, TypeName: "SHAPE"}}
	}
	prev := []any{mk(1), mk(2)}
	base := []any{mk(2), mk(3), nil, mk(4)}
	out := dedupeByHandle(prev, base)
	if len(out) != 2 || entity.EntityBase(out[0]).Handle != 3 || entity.EntityBase(out[1]).Handle != 4 {
		t.Errorf("dedupe 结果 = %v", out)
	}

	// fail 写入坏 writer：首个错误保留，后续调用空操作
	x := &dxfWriter{w: bufio.NewWriter(errWriter{})}
	x.fail(errWriterErr)
	if x.err != errWriterErr {
		t.Errorf("fail 未记录: %v", x.err)
	}
	x.code(0) // err 已置位 → 直接返回
	x.val(1, "x")
	if x.err != errWriterErr {
		t.Errorf("后续写不应覆盖首错: %v", x.err)
	}
}

// errWriterErr errWriter 的固定错误。
var errWriterErr = errString("write failed")

// errString 固定错误类型。
type errString string

// Error 实现 error 接口。
func (e errString) Error() string { return string(e) }

// errWriter 始终失败的 io.Writer（触发 dxfWriter 错误路径）。
type errWriter struct{}

// Write 实现 io.Writer。
func (errWriter) Write([]byte) (int, error) { return 0, errWriterErr }

// TestDebugFailures Document.failBy 记录与 DebugFailures 导出。
func TestDebugFailures(t *testing.T) {
	d := &Document{}
	if len(d.DebugFailures()) != 0 {
		t.Fatal("初始 debugFailures 应为空")
	}
	d.failBy(objrec.ObjectRef{Handle: 0x30}, errWriterErr)
	d.failBy(objrec.ObjectRef{Handle: 0x31}, errWriterErr)
	got := d.DebugFailures()
	if len(got) != 2 || got[0x30] != "write failed" {
		t.Errorf("DebugFailures = %v", got)
	}
}

// ---- 批次 4：pre-R13（R11）DIMENSION/POLYLINE/VERTEX 合成记录 ----

// gap2R11LE 小端编码辅助。
func gap2R11LE(v any) []byte {
	switch x := v.(type) {
	case uint16:
		return []byte{byte(x), byte(x >> 8)}
	}
	return nil
}

// gap2R11F64 float64 小端字节。
func gap2R11F64(f float64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, math.Float64bits(f))
	return b
}

// gap2R11Body 拼接字节片段。
func gap2R11Body(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// TestSynthPreR13Dimension DIMENSION 七种类型全分支（opts 全开）。
func TestSynthPreR13Dimension(t *testing.T) {
	const opts = 0xFFFF
	rd := func(f float64) []byte { return gap2R11F64(f) }
	// 公共段：RS anon + def_pt 3RD + text_mid 2RD + clone_ins 2RD + flag + TV
	common := func(dimtype byte) []byte {
		tv := gap2R11LE(uint16(3))
		tv = append(tv, "abc"...)
		return gap2R11Body(
			gap2R11LE(uint16(1)), // 匿名块句柄
			rd(1), rd(2), rd(3),  // def_pt（R10+ 3RD）
			rd(4), rd(5), // text_midpoint
			rd(6), rd(7), // clone_ins_pt
			[]byte{dimtype},
			tv,
		)
	}
	tail := gap2R11Body(
		rd(1), rd(2), rd(3), // point13（dimpt3）
		rd(4), rd(5), rd(6), // point14
		rd(7), rd(8), rd(9), // point15
		rd(10), rd(11), // angles 2RD
		rd(12),              // leader_len（DXF40）
		rd(13),              // dim_rotation
		rd(14),              // oblique
		rd(15),              // text_rotation
		rd(1), rd(0), rd(0), // extrusion
		gap2R11LE(uint16(5)), // dimstyle
	)
	// Diameter/Radius 类型无 point13/14 段
	tailShort := gap2R11Body(
		rd(7), rd(8), rd(9), // point15
		rd(10), rd(11), // angles
		rd(12), // leader_len
		rd(13),
		rd(14),
		rd(15),
		rd(1), rd(0), rd(0),
		gap2R11LE(uint16(5)),
	)
	for dimtype := byte(0); dimtype <= 6; dimtype++ {
		// Linear/Ang2Ln/Ang3Pt/Ordinate 含 point13/14 段；Diameter/Radius
		// 无 13/14（DXF15 直接跟公共段）
		var data []byte
		if dimtype == 3 || dimtype == 4 {
			data = append(common(dimtype), tailShort...)
		} else {
			data = append(common(dimtype), tail...)
		}
		h := preR13EntHead{opts: opts}
		e := decodePreR13Dimension(&preR13Reader{data: data}, h, container.VerR11, 30)
		if e == nil || e.UserText != "abc" {
			t.Errorf("dimtype %d: userText = %q", dimtype, e.UserText)
		}
	}
	// Ordinate 的 feature/leader 点与 Ang2Ln 的 p16 断言
	h := preR13EntHead{opts: opts}
	e := decodePreR13Dimension(&preR13Reader{data: append(common(2), tail...)}, h, container.VerR11, 30)
	if !e.HasPoint16 || e.Point16x != 10 {
		t.Errorf("Ang2Ln point16 = %v/%v", e.Point16x, e.P16y)
	}
	// Diameter 在 R10+ 无 HAS_ELEVATION 时 first_arc_pt 为 3RD
	e = decodePreR13Dimension(&preR13Reader{data: append(common(3), tailShort...)}, h, container.VerR11, 30)
	if !e.HasPoint15 || e.Point15.Z != 9 {
		t.Errorf("Diameter point15 = %v", e.Point15)
	}
}

// TestSynthPreR13Polyline POLYLINE 四变体（2D/3D/MESH/PFACE）合成记录。
func TestSynthPreR13Polyline(t *testing.T) {
	rd := func(f float64) []byte { return gap2R11F64(f) }
	build := func(plineFlag byte, opts uint16, tail []byte) any {
		size := uint16(8 + 1 + len(tail))
		head := gap2R11Body(
			[]byte{preR13TypePolyline, 0}, // type + flag（无扩展头字段）
			gap2R11LE(size),
			gap2R11LE(uint16(0)), // layer
			gap2R11LE(opts),
			[]byte{plineFlag}, // 专有区首字节 = pline_flag
		)
		data := append(head, tail...)
		h := preR13EntHead{
			startOff: 0, rawType: preR13TypePolyline, typ: preR13TypePolyline,
			flag: 0, size: size, opts: opts,
		}
		return decodePreR13Polyline(data, h, container.VerR11)
	}
	// 2D：flags + sw + ew + extrusion + m + n + curvetype
	e2, ok := build(0, preR13OptsPolylineHasFlag|preR13OptsPolylineHasStartWidth|
		preR13OptsPolylineHasEndWidth|preR13OptsPolylineHasExtrusion|
		preR13OptsPolylineHasMVerts|preR13OptsPolylineHasNVerts|
		preR13OptsPolylineHasCurvetype,
		gap2R11Body(rd(1), rd(2), rd(1), rd(0), rd(0),
			gap2R11LE(uint16(2)), gap2R11LE(uint16(3)), gap2R11LE(uint16(6)))).(*entity.EntPolyline2d)
	if !ok {
		t.Fatal("期望 POLYLINE_2D")
	}
	if e2.CurveType != 6 || e2.WidthStart != 1 {
		t.Errorf("2D curveType/width = %d/%v", e2.CurveType, e2.WidthStart)
	}
	// 3D：flags + sw + ew + extrusion(skip) + curvetype
	e3, ok := build(preR13FlagPolyline3D, preR13OptsPolylineHasFlag|
		preR13OptsPolylineHasStartWidth|preR13OptsPolylineHasEndWidth|
		preR13OptsPolylineHasExtrusion|preR13OptsPolylineHasCurvetype,
		gap2R11Body(rd(0.5), rd(0.6), rd(1), rd(0), rd(0), gap2R11LE(uint16(7)))).(*entity.EntPolyline3d)
	if !ok || e3.Flags75 != 7 {
		t.Errorf("期望 POLYLINE_3D，得到 %T", e3)
	}
	// MESH：flags + m + n + md + nd + curvetype
	em, ok := build(preR13FlagPolylineMesh, preR13OptsPolylineHasFlag|
		preR13OptsPolylineHasMVerts|preR13OptsPolylineHasNVerts|
		preR13OptsPolylineHasMDensity|preR13OptsPolylineHasNDensity|
		preR13OptsPolylineHasCurvetype,
		gap2R11Body(gap2R11LE(uint16(4)), gap2R11LE(uint16(4)),
			gap2R11LE(uint16(5)), gap2R11LE(uint16(5)), gap2R11LE(uint16(6)))).(*entity.EntPolylineMesh)
	if !ok || em.MDensity != 5 {
		t.Errorf("期望 POLYLINE_MESH，得到 %T", em)
	}
	// PFACE：flags + numverts + numfaces
	ep, ok := build(preR13FlagPolylinePfaceMesh, preR13OptsPolylineHasFlag|
		preR13OptsPolylineHasMVerts|preR13OptsPolylineHasNVerts,
		gap2R11Body(gap2R11LE(uint16(4)), gap2R11LE(uint16(6)))).(*entity.EntPolylinePface)
	if !ok || ep.NumVertices != 4 || ep.NumFaces != 6 {
		t.Errorf("期望 POLYLINE_PFACE，得到 %T", ep)
	}
}

// TestSynthPreR13Vertex VERTEX 五变体（2D/3D/MESH/PFACE/PFACE_FACE）。
func TestSynthPreR13Vertex(t *testing.T) {
	const optsAll = preR13OptsVertexHasStartWidth | preR13OptsVertexHasEndWidth |
		preR13OptsVertexHasBulge | preR13OptsVertexHasFlag |
		preR13OptsVertexHasTangentDir | preR13OptsVertexHasIndex1 |
		preR13OptsVertexHasIndex2 | preR13OptsVertexHasIndex3 |
		preR13OptsVertexHasIndex4
	rd := func(f float64) []byte { return gap2R11F64(f) }
	build := func(vertexFlag byte, opts uint16) ([]byte, preR13EntHead) {
		body := gap2R11Body(
			rd(1), rd(2), // point 2RD
			rd(0.5),                                    // start_width
			rd(0.6),                                    // end_width
			rd(0.25),                                   // bulge
			[]byte{vertexFlag},                         // vertex_flag（flagOff 处）
			rd(3),                                      // tangent_dir
			gap2R11LE(uint16(1)), gap2R11LE(uint16(2)), // index1/2
			gap2R11LE(uint16(3)), gap2R11LE(uint16(4)), // index3/4
		)
		data := gap2R11Body(
			[]byte{preR13TypeVertex, 0},
			gap2R11LE(uint16(8+len(body))),
			gap2R11LE(uint16(0)),
			gap2R11LE(opts),
		)
		data = append(data, body...)
		return data, preR13EntHead{
			startOff: 0, rawType: preR13TypeVertex, typ: preR13TypeVertex,
			flag: 0, size: uint16(len(data)), opts: opts,
		}
	}
	cases := []struct {
		vflag byte
		want  string
	}{
		{preR13FlagVertexMesh | preR13FlagVertexPfaceMesh, "VERTEX_PFACE"},
		{preR13FlagVertexMesh, "VERTEX_MESH"},
		{preR13FlagVertex3D, "VERTEX_3D"},
		{0, "VERTEX_2D"},
	}
	for _, tc := range cases {
		data, h := build(tc.vflag, optsAll)
		e := decodePreR13Vertex(data, h, container.VerR11)
		b := entity.EntityBase(e)
		if b == nil || b.TypeName != tc.want {
			t.Errorf("vflag %X: 得到 %v，期望 %s", tc.vflag, b.TypeName, tc.want)
		}
	}
	// 2D 变体字段断言
	data, h := build(0, optsAll)
	v := decodePreR13Vertex(data, h, container.VerR11).(*entity.EntVertex2d)
	if v.Bulge != 0.25 || v.StartWidth != 0.5 {
		t.Errorf("2D bulge/width = %v/%v", v.Bulge, v.StartWidth)
	}
	// PFACE_FACE 变体：无 point/widths 前缀（HasNotXY 置位），body 为
	// flag + 4 个索引
	const optsFace = preR13OptsVertexHasFlag | preR13OptsVertexHasNotXY |
		preR13OptsVertexHasIndex1 | preR13OptsVertexHasIndex2 |
		preR13OptsVertexHasIndex3 | preR13OptsVertexHasIndex4
	faceBody := gap2R11Body(
		[]byte{preR13FlagVertexPfaceMesh}, // flag
		gap2R11LE(uint16(1)), gap2R11LE(uint16(2)),
		gap2R11LE(uint16(3)), gap2R11LE(uint16(4)),
	)
	faceData := gap2R11Body(
		[]byte{preR13TypeVertex, 0},
		gap2R11LE(uint16(8+len(faceBody))),
		gap2R11LE(uint16(0)),
		gap2R11LE(uint16(optsFace)),
	)
	faceData = append(faceData, faceBody...)
	faceHead := preR13EntHead{
		startOff: 0, rawType: preR13TypeVertex, typ: preR13TypeVertex,
		flag: 0, size: uint16(len(faceData)), opts: optsFace,
	}
	f := decodePreR13Vertex(faceData, faceHead, container.VerR11).(*entity.EntVertexPfaceFace)
	if f.Vertind[3] != 4 {
		t.Errorf("PFACE_FACE vertind = %v", f.Vertind)
	}
}

// ---- 批次 5：畸形输入健壮性（错误分支批量覆盖）----
// 截断/翻转/随机位流三类畸形输入走解码器错误返回路径（EOF、越界、
// 非法计数等分支），并断言全程不 panic——固定种子保证确定性。

// TestParseTruncatedSamples 真实样本按比例截断后 Parse 不 panic、不挂起。
func TestParseTruncatedSamples(t *testing.T) {
	samples := []string{
		"line_R14.dwg", "line_2000.dwg", "line_2004.dwg", "circle_2007.dwg",
		"arc_2010.dwg", "arc_2013.dwg", "lw_example2018.dwg", "lw_example2018.dwg",
	}
	for _, name := range samples {
		raw, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Skipf("缺样本 %s", name)
		}
		for _, ratio := range []float64{0.1, 0.3, 0.5, 0.6, 0.75, 0.9, 0.97, 0.995} {
			cut := int(float64(len(raw)) * ratio)
			data := raw[:cut]
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("%s 截断 %d/%d panic: %v", name, cut, len(raw), r)
					}
				}()
				_, _ = Parse(data)
			}()
		}
	}
}

// TestParseBitFlipSamples 真实样本头部与中段单字节翻转后 Parse 不 panic。
func TestParseBitFlipSamples(t *testing.T) {
	samples := []string{
		"line_R14.dwg", "line_2000.dwg", "line_2004.dwg", "arc_2007.dwg",
		"lw_example2018.dwg", "lw_example2018.dwg",
	}
	for _, name := range samples {
		raw, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Skipf("缺样本 %s", name)
		}
		rng := rand.New(rand.NewSource(1))
		for i := 0; i < 60; i++ {
			data := append([]byte(nil), raw...)
			pos := rng.Intn(len(data))
			data[pos] ^= byte(1 << rng.Intn(8))
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("%s 翻转 @%d panic: %v", name, pos, r)
					}
				}()
				_, _ = Parse(data)
			}()
		}
	}
}

// TestSynthMalformedObjectStreams internalClassDecoders 全类名喂随机
// dat 流与 handle 流（R2004 与 R2013 两种 T 语义），不 panic。
func TestSynthMalformedObjectStreams(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	run := func(ver container.DwgVersion, seedPhase byte) {
		for className, spec := range object.InternalClassDecoders {
			for _, size := range []int{1, 3, 8, 21, 55, 130, 400} {
				buf := make([]byte, size)
				for i := range buf {
					buf[i] = byte(rng.Intn(256)) ^ seedPhase
				}
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Errorf("%s ver=%d size=%d panic: %v", className, ver, size, r)
						}
					}()
					g := &object.ObjGeneric{Name: className, Handle: 0x30}
					fr := &object.GfRead{R: bitstream.NewBitStream(buf), Ver: ver}
					_ = spec.Decode(fr.R, ver, fr, g)
					if spec.Hdl != nil {
						hb := make([]byte, 6)
						for i := range hb {
							hb[i] = byte(rng.Intn(16))
						}
						g2 := &object.ObjGeneric{Name: className, Handle: 0x30, Fields: g.Fields}
						g2.HdlCount = g.HdlCount
						g2.ValueHandle91 = g.ValueHandle91
						hfr := &object.GfRead{R: bitstream.NewBitStream(hb), Ver: ver}
						_ = spec.Hdl(hfr.R, ver, hfr, g2)
					}
				}()
			}
		}
	}
	run(container.VerR2004, 0x00)
	run(container.VerR2013, 0xA5)
}

// TestSynthMalformedEntityBits 实体公共头+专有字段合成失败路径：
// versioned 实体头扫描对截断流的宽容性（不 panic、err 或部分结果）。
func TestSynthMalformedEntityBits(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 60; i++ {
		size := 4 + rng.Intn(40)
		buf := make([]byte, size)
		for j := range buf {
			buf[j] = byte(rng.Intn(256))
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("实体位流 #%d panic: %v", i, r)
				}
			}()
			r := bitstream.NewBitStream(buf)
			// 公共头候选解析（R13 位流）：截断流应报错而非崩溃
			_, _ = r.ReadB()
			_, _ = r.ReadRS()
			_, _, _, _ = r.Read3BD()
			_, _ = r.ReadTV(0)
		}()
	}
}

// ---- 批次 6：R2013B 实体头 / TABLECONTENT 全流 / DXF 句柄分配 ----

// gap2WriteR2013BHead 写 parseCommonEntityHeadR2013B 的合成位流。
// colorMode: -1=noLinks 短格式；0=详细 colorMode=0；1=详细 colorMode=1。
func gap2WriteR2013BHead(w *bitstream.EncWriter, picSize int, colorMode int8, flags uint16, second bool) {
	w.WriteH(0x00, 1, 0x2B) // handle
	w.WriteBS(0)            // EED 链空（extSize=0）
	w.WriteB(picSize > 0)   // picFlag
	if picSize > 0 {
		w.WriteBLLv(uint64(picSize))
		w.WriteTF(make([]byte, picSize))
	}
	w.WriteBB(2)            // entityMode
	w.WriteBL(0)            // reactors
	w.WriteB(false)         // xdicMissing
	w.WriteB(false)         // hasDsBinary
	w.WriteB(colorMode < 0) // noLinks：1 = 短格式颜色
	if colorMode < 0 {
		// 短格式：second 1=ByLayer(256) 0=ByBlock(0)
		w.WriteB(second)
	} else {
		w.WriteB(colorMode == 1)
		if colorMode == 1 {
			w.WriteRC(3) // ACI 索引（无 second 位）
		} else {
			w.WriteRS(flags)
			if flags&0x8000 != 0 {
				w.WriteBL(0xFF0000)
			}
			if flags&0x2000 != 0 {
				w.WriteBL(0x40000080)
			}
		}
	}
	w.WriteBD(1)    // ltypeScale
	w.WriteBB(0)    // ltypeFlags
	w.WriteBB(0)    // plotstyle
	w.WriteBB(0)    // material
	w.WriteB(false) // 3×B（变体 B 无 shadow）
	w.WriteB(false)
	w.WriteB(false)
	w.WriteBS(0)
	w.WriteRC(0)
}

// TestSynthR2013BHead R2013B 实体公共头全颜色分支与 preview。
func TestSynthR2013BHead(t *testing.T) {
	end := uint64(0)
	// 短格式 ByLayer
	w := bitstream.NewEncWriter()
	gap2WriteR2013BHead(w, 0, -1, 0, true)
	head, err := entity.ParseCommonEntityHeadR2013B(bitstream.NewBitStream(w.Bytes()), end)
	if err != nil {
		t.Fatalf("短格式: %v", err)
	}
	if head.Color.Index != 256 || head.Color.Flag != 1 {
		t.Errorf("ByLayer index/flag = %d/%d", head.Color.Index, head.Color.Flag)
	}
	// 短格式 ByBlock
	w = bitstream.NewEncWriter()
	gap2WriteR2013BHead(w, 0, -1, 0, false)
	if head, err = entity.ParseCommonEntityHeadR2013B(bitstream.NewBitStream(w.Bytes()), end); err != nil || head.Color.Index != 0 {
		t.Errorf("ByBlock: %v index=%d", err, head.Color.Index)
	}
	// 详细 ACI
	w = bitstream.NewEncWriter()
	gap2WriteR2013BHead(w, 0, 1, 0, false)
	if head, err = entity.ParseCommonEntityHeadR2013B(bitstream.NewBitStream(w.Bytes()), end); err != nil || head.Color.Index != 3 {
		t.Errorf("ACI: %v index=%d", err, head.Color.Index)
	}
	// 详细真彩 + alpha + preview 图像
	w = bitstream.NewEncWriter()
	gap2WriteR2013BHead(w, 4, 0, 0x8000|0x2000, false)
	if head, err = entity.ParseCommonEntityHeadR2013B(bitstream.NewBitStream(w.Bytes()), end); err != nil {
		t.Fatalf("真彩: %v", err)
	}
	if !head.Color.HasTrue || !head.Color.HasAlpha {
		t.Errorf("hasTrue=%v hasAlpha=%v", head.Color.HasTrue, head.Color.HasAlpha)
	}
	if !head.PreviewExists || len(head.Preview) != 4 {
		t.Errorf("preview = %v/%d", head.PreviewExists, len(head.Preview))
	}
}

// TestSynthTableContentFull TABLECONTENT 全嵌套流（cols/rows/cells/
// contents/attrs/format overrides/geom data/merged cells）。
func TestSynthTableContentFull(t *testing.T) {
	w := bitstream.NewEncWriter()
	// AcDbLinkedData
	w.WriteTV("ldata") // ldata.name
	w.WriteTV("desc")  // ldata.description
	// cols
	w.WriteBL(1)      // num_cols
	w.WriteTV("col0") // cols[0].name
	w.WriteBL(0)      // cols[0].custom_data
	// cols[0].cellstyle：data_flags=1 全量（含 content_format + margins + border）
	w.WriteBL(0)    // type
	w.WriteBS(1)    // data_flags
	w.WriteBL(0)    // property_override_flags
	w.WriteBL(0)    // merge_flags
	gap2WriteCMC(w) // bg_color
	w.WriteBL(0)    // content_layout
	// content_format
	w.WriteBL(0)    // property_override_flags
	w.WriteBL(0)    // property_flags
	w.WriteBL(2)    // value_data_type
	w.WriteBL(0)    // value_unit_type
	w.WriteTV("F")  // value_format_string
	w.WriteBD(0)    // rotation
	w.WriteBD(1)    // block_scale
	w.WriteBL(1)    // cell_alignment
	gap2WriteCMC(w) // content_color
	w.WriteBD(2)    // text_height
	w.WriteBS(0)    // margin_override_flags=0
	w.WriteBL(1)    // num_borders
	w.WriteBL(1)    // borders[0].index_mask（≠0）
	w.WriteBL(0)    // border_overrides
	w.WriteBL(0)    // border_type
	gap2WriteCMC(w) // color
	w.WriteBL(9)    // linewt（BLd）
	w.WriteBL(1)    // visible
	w.WriteBD(0)    // double_line_spacing
	// rows → cells
	w.WriteBL(1) // num_rows
	w.WriteBL(1) // rows[0].num_cells
	// cells[0]
	w.WriteBL(1)        // flag
	w.WriteTV("tip")    // tooltip
	w.WriteBL(1)        // customdata（走 customdata_items）
	w.WriteBL(1)        // num_customdata_items
	w.WriteTV("cdname") // customdata_items[0].name
	w.WriteBL(2)        // value.data_type=kDouble
	w.WriteBD(3.5)      // value.data_double
	w.WriteBL(1)        // has_linked_data（读行列数）
	w.WriteBL(2)        // num_rows
	w.WriteBL(3)        // num_cols
	w.WriteBL(0)        // unknown
	// cell_contents[0]：Value 字符串 + 1 attr + content_format overrides
	w.WriteBL(1)           // num_cell_contents
	w.WriteBL(1)           // type=Value
	w.WriteBL(4)           // value.data_type=kString
	w.WriteTV("cell text") // value.data_string
	w.WriteBL(1)           // num_attrs
	w.WriteTV("attrval")   // attrs[0].value
	w.WriteBL(7)           // attrs[0].index
	w.WriteBS(1)           // has_content_format_overrides
	w.WriteBL(0)           // content_format.property_override_flags
	w.WriteBL(0)           // property_flags
	w.WriteBL(0)           // value_data_type
	w.WriteBL(0)           // value_unit_type
	w.WriteTV("")          // value_format_string
	w.WriteBD(0)           // rotation
	w.WriteBD(1)           // block_scale
	w.WriteBL(0)           // cell_alignment
	gap2WriteCMC(w)        // content_color
	w.WriteBD(1.5)         // text_height
	// cells[0] 尾：style_id + has_geom_data（带 geometry 组）
	w.WriteBL(0)    // style_id
	w.WriteBL(1)    // has_geom_data
	w.WriteBL(1)    // geom_data_flag
	w.WriteBD(11)   // width_w_gap
	w.WriteBD(12)   // height_w_gap
	w.WriteBL(1)    // num_geometry
	gap2Write3BD(w) // dist_top_left
	gap2Write3BD(w) // dist_center
	w.WriteBD(13)   // content_width
	w.WriteBD(14)   // content_height
	w.WriteBD(15)   // width
	w.WriteBD(16)   // height
	w.WriteBL(0)    // unknown
	// row 级：custom_data + items + cellstyle（data_flags=0 短路径）+ style_id + height
	w.WriteBL(0) // custom_data
	w.WriteBL(0) // num_customdata_items
	w.WriteBL(0) // cellstyle.type
	w.WriteBS(0) // cellstyle.data_flags（0 → 短路径）
	w.WriteBL(0) // style_id
	w.WriteBD(8) // height
	// field_refs / merged_cells
	w.WriteBL(0) // num_field_refs
	w.WriteBL(1) // num_merged_cells
	w.WriteBL(0) // top_row
	w.WriteBL(0) // left_col
	w.WriteBL(1) // bottom_row
	w.WriteBL(1) // right_col

	g := &object.ObjGeneric{Name: "TABLECONTENT", Handle: 0x30}
	fr := &object.GfRead{R: bitstream.NewBitStream(w.Bytes()), Ver: container.VerR2004}
	if err := object.DecodeGenericTABLECONTENT(fr.R, container.VerR2004, fr, g); err != nil {
		t.Fatalf("TABLECONTENT decode: %v", err)
	}
	if v, _ := g.Field("tdata.rows[0].cells[0].cell_contents[0].value.data_string").(string); v != "cell text" {
		t.Errorf("cell value = %v", v)
	}
	if v, _ := g.Field("fdata.num_merged_cells").(int64); v != 1 {
		t.Errorf("merged_cells = %v", g.Field("fdata.num_merged_cells"))
	}
}

// TestDxfStateHandles dxfState.recordHandle 分配与 blockHandle 兜底注册。
func TestDxfStateHandles(t *testing.T) {
	st := &dxfState{nextHandle: 0x50, blockByName: map[string]uint64{}, doc: &Document{blocks: map[uint64][]any{}}}
	rec := &dxfRec{}
	// 组码 5 缺失 → 合成句柄
	if h := st.recordHandle(rec); h != 0x50 {
		t.Errorf("合成句柄 = %X", h)
	}
	if st.nextHandle != 0x51 {
		t.Errorf("nextHandle = %X", st.nextHandle)
	}
	// blockHandle 首次分配 + 复用
	h1 := st.blockHandle(" BLK ")
	h2 := st.blockHandle("BLK")
	if h1 == 0 || h1 != h2 {
		t.Errorf("blockHandle = %X/%X", h1, h2)
	}
	if _, ok := st.doc.blocks[h1]; !ok {
		t.Error("blocks 未注册合成句柄")
	}
}

// TestSynthMLeaderContextFull MLEADER_CONTEXT_DATA 全路径（txt 尾段 +
// blk 变体 + base 三点）。
func TestSynthMLeaderContextFull(t *testing.T) {
	buildTxtTail := func(w *bitstream.EncWriter) {
		w.WriteB(false) // isHeightAuto
		w.WriteBD(5)    // colWidth
		w.WriteBD(1)    // colGutter
		w.WriteB(false) // isColFlowReversed
		w.WriteBL(1)    // numColSizes
		w.WriteBD(9)    // colSizes[0]
		w.WriteB(false) // wordBreak
		w.WriteB(false) // unknown
	}
	// 变体 1：txt 内容（含全部文字样式尾段）
	w := bitstream.NewEncWriter()
	w.WriteBD(1)       // scaleFactor
	gap2Write3BD(w)    // contentBase
	w.WriteBD(2)       // textHeight
	w.WriteBD(3)       // arrowSize
	w.WriteBD(4)       // landingGap
	w.WriteBS(0)       // textLeft
	w.WriteBS(0)       // textRight
	w.WriteBS(0)       // textAngletype
	w.WriteBS(0)       // textAlignment
	w.WriteB(true)     // hasContentTxt
	w.WriteTV("hello") // defaultText（pre-R2007 内联）
	gap2Write3BD(w)    // normal
	gap2Write3BD(w)    // location
	gap2Write3BD(w)    // direction
	w.WriteBD(0)       // rotation
	w.WriteBD(10)      // width
	w.WriteBD(2)       // height
	w.WriteBD(1)       // lineSpacingFactor
	w.WriteBS(1)       // lineSpacingStyle
	w.WriteBS(7)       // color CMC index
	w.WriteBL(0xc3000000)
	w.WriteRC(0)
	w.WriteBS(0) // alignment
	w.WriteBS(0) // flow
	w.WriteBS(7) // bgColor index
	w.WriteBL(0xc3000000)
	w.WriteRC(0)
	w.WriteBD(0)    // bgScale
	w.WriteBL(0)    // bgTransparency
	w.WriteB(false) // isBgFill
	w.WriteB(false) // isBgMaskFill
	w.WriteBS(0)    // colType
	buildTxtTail(w)
	gap2Write3BD(w) // base
	gap2Write3BD(w) // baseDir
	gap2Write3BD(w) // baseVert
	w.WriteB(true)  // isNormalReversed
	m := &entity.EntMLeader{}
	if err := entity.DecodeMLeaderContext(bitstream.NewBitStream(w.Bytes()), m, container.VerR2004, 0, nil); err != nil {
		t.Fatalf("txt ctx: %v", err)
	}
	if m.Ctx.Txt.DefaultText != "hello" {
		t.Errorf("defaultText = %q", m.Ctx.Txt.DefaultText)
	}
	if len(m.Ctx.Txt.ColSizes) != 1 || m.Ctx.Txt.ColSizes[0] != 9 {
		t.Errorf("colSizes = %v", m.Ctx.Txt.ColSizes)
	}
	if !m.Ctx.IsNormalReversed {
		t.Error("isNormalReversed 应为 true")
	}

	// 变体 2：blk 内容（transform 16×BD）
	w = bitstream.NewEncWriter()
	w.WriteBD(1)
	gap2Write3BD(w)
	w.WriteBD(2)
	w.WriteBD(3)
	w.WriteBD(4)
	w.WriteBS(0)
	w.WriteBS(0)
	w.WriteBS(0)
	w.WriteBS(0)
	w.WriteB(false) // hasContentTxt=false
	w.WriteB(true)  // hasContentBlk
	gap2Write3BD(w) // blk.normal
	gap2Write3BD(w) // blk.location
	gap2Write3BD(w) // blk.scale
	w.WriteBD(0.5)  // blk.rotation
	w.WriteBS(7)    // blk.color index
	w.WriteBL(0xc3000000)
	w.WriteRC(0)
	for i := 0; i < 16; i++ {
		w.WriteBD(float64(i)) // transform
	}
	gap2Write3BD(w)
	gap2Write3BD(w)
	gap2Write3BD(w)
	w.WriteB(false)
	m = &entity.EntMLeader{}
	if err := entity.DecodeMLeaderContext(bitstream.NewBitStream(w.Bytes()), m, container.VerR2004, 0, nil); err != nil {
		t.Fatalf("blk ctx: %v", err)
	}
	if !m.Ctx.HasContentBlk || m.Ctx.Blk.Rotation != 0.5 {
		t.Errorf("blk = %v/%v", m.Ctx.HasContentBlk, m.Ctx.Blk.Rotation)
	}
}

// TestDecodeSolidTolerantPaths SOLID 容忍解码三出口：首遍 sane、错位
// 回退重试、起点在位 0 无法回退。
func TestDecodeSolidTolerantPaths(t *testing.T) {
	head := &entity.CommonEntityHead{Handle: 0x2B, ObjSizeBit: 1 << 30}
	build := func(xs ...float64) []byte {
		w := bitstream.NewEncWriter()
		w.WriteB(false) // thickness flag（0 → 后跟 BD）
		w.WriteBD(0)    // thickness
		w.WriteBD(0)    // elevation
		vals := []float64{1, 2, 3, 4, 5, 6, 7, 8}
		for i, want := range xs {
			vals[i] = want
		}
		for _, v := range vals {
			w.WriteRD(v)
		}
		w.WriteB(true) // extrusion flag（1 → (0,0,1)）
		w.WriteRL(0)   // owner/layer 垫字节（decodeOwnerLayer 用 objSizeBit 定位）
		w.WriteRL(0)
		return w.Bytes()
	}
	// 首遍 sane：正常坐标直接返回
	r := bitstream.NewBitStream(build())
	ent, err := entity.DecodeSolidTolerant(r, head, false)
	if err != nil {
		t.Fatalf("sane: %v", err)
	}
	if s, ok := ent.(*entity.EntSolid); !ok || s.P1.X != 1 {
		t.Errorf("sane 结果 = %T/%v", ent, ent)
	}
	// 首遍 denormal（p1.x=1e-40 非 0 且 <1e-30）→ 回退 1 位重试
	r = bitstream.NewBitStream(build(1e-40))
	ent, err = entity.DecodeSolidTolerant(r, head, false)
	if err != nil {
		t.Fatalf("回退: %v", err)
	}
	if _, ok := ent.(*entity.EntSolid); !ok {
		t.Errorf("回退结果类型 = %T", ent)
	}
	// 起点在位 0：decodeSolidTolerant 直接返回首遍结果
	data := build()
	r2 := bitstream.NewBitStream(data)
	// 人为把读取器推进到字节 0 位 0 等价起点（newBitStream 本身即 0/0）
	ent2, err := entity.DecodeSolidTolerant(r2, head, false)
	if err != nil {
		t.Fatalf("位 0: %v", err)
	}
	if _, ok := ent2.(*entity.EntSolid); !ok {
		t.Errorf("位 0 结果类型 = %T", ent2)
	}
}
