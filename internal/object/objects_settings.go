// 本文件实现设置类内部对象（dwg2.spec/dwg.spec）：FIELD（AcDbField +
// TABLE_value_fields）、GEODATA（R2010+ 布局）、SECTION_MANAGER/
// SECTION_SETTINGS（SectionTypeSettings/GeometrySettings 树）、
// PLOTSETTINGS（AcDbPlotSettings）。

package object

import (
	"encoding/hex"
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/objrec"
	"strings"
)

// tableValueDataTypeKnown 判断 data_type 是否在 LibreDWG
// TABLE_value_fields 的 switch 全集内（含仅报错不重置的 kBuffer/kResBuf）。
func tableValueDataTypeKnown(dt int64) bool {
	switch dt {
	case 0, 1, 2, 4, 8, 16, 32, 64, 128, 256, 512:
		return true
	}
	return false
}

// hexUpper 字节串转大写 hex（gold JSON 的 BINARY 键导出形态，
// multileaders FIELD value.data_date 实证 'D6070C00...'）。
func hexUpper(b []byte) string {
	return strings.ToUpper(hex.EncodeToString(b))
}

// decodeTableValueFields 读 TABLE_value_fields（前缀如 "value."）：
// R2007+ 先 format_flags BL；data_type BL（R2000 清 0x200 位）+ 按
// 类型读 data_long/data_double/data_string 等。返回 data_type。
// data_type 不在 LibreDWG switch 全集（multileaders FIELD childval[1]
// 实证读出 1342603270）时对齐其 default 分支：format_flags/data_type
// 重置为 0 并按 kUnknown 输出 data_long=0，位流照旧推进到 unit_type。
func decodeTableValueFields(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric, prefix string) (int64, error) {
	var formatFlags int64
	if Ver >= container.VerR2007 {
		ff, err := R.ReadBL()
		if err != nil {
			return 0, err
		}
		formatFlags = int64(ff)
	}
	dt, err := R.ReadBL()
	if err != nil {
		return 0, err
	}
	dataType := int64(dt)
	if Ver < container.VerR2007 {
		dataType &^= 0x200 // PRE R_2007a：清除 0x200 位
	}
	// LibreDWG dwg_spec_shared.h TABLE_value_fields：R2007+ 且
	// format_flags&3 非零时值联合整体跳过（multileaders FIELD 实证：
	// format_flags=3 的 value 与 childval[1] 均无 data_long/data_double）
	skipValue := Ver >= container.VerR2007 && formatFlags&3 != 0
	// format_flags/data_type 键在重置判定后统一入 Fields（审计取首键，
	// 不能让原始垃圾值先入列）
	// switch 全集之外的 data_type 触发 LibreDWG default 分支的重置语义：
	// 键值归零 + data_long=0，与 gold JSON 序列化一致
	if !skipValue && !tableValueDataTypeKnown(dataType) {
		if Ver >= container.VerR2007 {
			g.Fields = append(g.Fields, ObjField{prefix + "format_flags", int64(0)})
		}
		g.Fields = append(g.Fields, ObjField{prefix + "data_type", int64(0)})
		g.Fields = append(g.Fields, ObjField{prefix + "data_long", int64(0)})
		skipValue = true
	} else {
		if Ver >= container.VerR2007 {
			g.Fields = append(g.Fields, ObjField{prefix + "format_flags", formatFlags})
		}
		g.Fields = append(g.Fields, ObjField{prefix + "data_type", dataType})
	}
	if !skipValue {
		switch dataType {
		case 0, 1: // kUnknown/kLong
			v, e := R.ReadBL()
			if e != nil {
				return 0, e
			}
			g.Fields = append(g.Fields, ObjField{prefix + "data_long", int64(v)})
		case 2: // kDouble
			v, e := R.ReadBD()
			if e != nil {
				return 0, e
			}
			g.Fields = append(g.Fields, ObjField{prefix + "data_double", v})
		case 4: // kString
			if err := fr.T(prefix+"data_string", g); err != nil {
				return 0, err
			}
		case 8: // kDate：BL size + 二进制（gold 以大写 hex 串导出）
			sz, e := R.ReadBL()
			if e != nil {
				return 0, e
			}
			if sz > 1_000_000 {
				return 0, fmt.Errorf("cad: TABLE value size 异常 %d", sz)
			}
			raw := make([]byte, 0, sz)
			for i := uint32(0); i < sz; i++ {
				b, e := R.ReadRC()
				if e != nil {
					return 0, e
				}
				raw = append(raw, byte(b))
			}
			g.Fields = append(g.Fields, ObjField{prefix + "data_size", int64(sz)})
			g.Fields = append(g.Fields, ObjField{prefix + "data_date", hexUpper(raw)})
		case 16: // kPoint：BL size + 2RD
			if _, e := R.ReadBL(); e != nil {
				return 0, e
			}
			x, e := R.ReadRD()
			if e != nil {
				return 0, e
			}
			y, e := R.ReadRD()
			if e != nil {
				return 0, e
			}
			g.Fields = append(g.Fields, ObjField{prefix + "data_point", []float64{x, y}})
		case 32: // k3dPoint：BL size + 3RD
			if _, e := R.ReadBL(); e != nil {
				return 0, e
			}
			var p3 [3]float64
			for i := range p3 {
				if p3[i], err = R.ReadRD(); err != nil {
					return 0, err
				}
			}
			g.Fields = append(g.Fields, ObjField{prefix + "data_3dpoint", p3[:]})
		case 64: // kObjectId：句柄在 handle 流，dat 流无位
			g.Fields = append(g.Fields, ObjField{prefix + "data_handle", nil})
		case 512: // kGeneral since r2007：BL size + 原始字节
			if Ver >= container.VerR2007 {
				sz, e := R.ReadBL()
				if e != nil {
					return 0, e
				}
				if sz > 1_000_000 {
					return 0, fmt.Errorf("cad: TABLE value size 异常 %d", sz)
				}
				for i := uint32(0); i < sz; i++ {
					if _, e = R.ReadRC(); e != nil {
						return 0, e
					}
				}
			}
		}
	}
	// SINCE R_2007a：unit_type + format_string +（unit_type≠12 时）value_string
	if Ver >= container.VerR2007 {
		ut, err := fr.BLv(prefix+"unit_type", g)
		if err != nil {
			return 0, err
		}
		if err := fr.T(prefix+"format_string", g); err != nil {
			return 0, err
		}
		if ut != 12 {
			if err := fr.T(prefix+"value_string", g); err != nil {
				return 0, err
			}
		}
	}
	return dataType, nil
}

// decodeGenericFIELD 解析 FIELD（dwg.spec）：id/code T + num_childs BL +
// num_objects BL + format T（pre-R2007）+ 评估组 5×BL +
// evaluation_error_msg T + TABLE_value_fields(value) + value_string T +
// value_string_length BL + num_childval BL + childval×N（key T + value）。
// childs/objects 句柄在 handle 流。
func decodeGenericFIELD(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := fr.T("id", g); err != nil {
		return err
	}
	if err := fr.T("code", g); err != nil {
		return err
	}
	nc, err := fr.BLv("num_childs", g)
	if err != nil {
		return err
	}
	if nc < 0 || nc > 20000 {
		return fmt.Errorf("cad: FIELD childs 数异常 %d", nc)
	}
	no, err := fr.BLv("num_objects", g)
	if err != nil {
		return err
	}
	if no < 0 || no > 20000 {
		return fmt.Errorf("cad: FIELD objects 数异常 %d", no)
	}
	if Ver < container.VerR2007 {
		if err := fr.T("format", g); err != nil {
			return err
		}
	}
	for _, k := range []string{"evaluation_option", "filing_option", "field_state", "evaluation_status", "evaluation_error_code"} {
		if err := fr.BL(k, g); err != nil {
			return err
		}
	}
	if err := fr.T("evaluation_error_msg", g); err != nil {
		return err
	}
	if _, err := decodeTableValueFields(R, Ver, fr, g, "value."); err != nil {
		return err
	}
	if err := fr.T("value_string", g); err != nil {
		return err
	}
	if err := fr.BL("value_string_length", g); err != nil {
		return err
	}
	nv, err := fr.BLv("num_childval", g)
	if err != nil {
		return err
	}
	if nv < 0 || nv > 20000 {
		return fmt.Errorf("cad: FIELD childval 数异常 %d", nv)
	}
	for i := 0; i < int(nv); i++ {
		p := "childval[" + itoa(i) + "]."
		if err := fr.T(p+"key", g); err != nil {
			return err
		}
		if _, err := decodeTableValueFields(R, Ver, fr, g, p+"value."); err != nil {
			return err
		}
	}
	g.HdlCount = int(nc) + int(no)
	return nil
}

// decodeGenericFIELD_HDL handle 流：childs×num_childs + objects×num_objects。
func decodeGenericFIELD_HDL(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	for i := 0; i < g.HdlCount; i++ {
		h, e := objrec.ReadHandleReference(R, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// decodeGenericGEODATA 解析 GEODATA（R2010+ 布局）：class_version BL +
// host_block 句柄 + coord_type BS + design_pt/ref_pt 3BD +
// unit_scale_horiz BD + units_value_horiz BL + unit_scale_vert BD +
// units_value_vert BL + up_dir 3BD + north_dir 2RD + scale_est BL +
// user_scale_factor BD + do_sea_level_corr B + sea_level_elev BD +
// coord_proj_radius BD + coord_system_def/geo_rss_tag T + 3 个
// observation tag T + geomesh 点/面网格 + sea_level…（host_block 句柄
// 在 handle 流）。
func decodeGenericGEODATA(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := fr.BL("class_version", g); err != nil {
		return err
	}
	if v, _ := g.Field("class_version").(int64); v > 10 {
		return fmt.Errorf("cad: GEODATA class_version 越界 %d", v)
	}
	if err := fr.BS("coord_type", g); err != nil {
		return err
	}
	if err := fr.Point3("design_pt", g); err != nil {
		return err
	}
	if err := fr.Point3("ref_pt", g); err != nil {
		return err
	}
	if err := fr.BD("unit_scale_horiz", g); err != nil {
		return err
	}
	if err := fr.BL("units_value_horiz", g); err != nil {
		return err
	}
	if err := fr.BD("unit_scale_vert", g); err != nil {
		return err
	}
	if err := fr.BL("units_value_vert", g); err != nil {
		return err
	}
	if err := fr.Point3("up_dir", g); err != nil {
		return err
	}
	if err := fr.Point2RD("north_dir", g); err != nil {
		return err
	}
	if err := fr.BL("scale_est", g); err != nil {
		return err
	}
	if err := fr.BD("user_scale_factor", g); err != nil {
		return err
	}
	if err := fr.B("do_sea_level_corr", g); err != nil {
		return err
	}
	if err := fr.BD("sea_level_elev", g); err != nil {
		return err
	}
	if err := fr.BD("coord_proj_radius", g); err != nil {
		return err
	}
	if err := fr.T("coord_system_def", g); err != nil {
		return err
	}
	if err := fr.T("geo_rss_tag", g); err != nil {
		return err
	}
	for _, k := range []string{"observation_from_tag", "observation_to_tag", "observation_coverage_tag"} {
		if err := fr.T(k, g); err != nil {
			return err
		}
	}
	np, err := fr.BLv("num_geomesh_pts", g)
	if err != nil {
		return err
	}
	if np > 50000 {
		return fmt.Errorf("cad: GEODATA geomesh_pts 数异常 %d", np)
	}
	for i := 0; i < int(np); i++ {
		if err := fr.Point2RD(fmt.Sprintf("geomesh_pts[%d].source_pt", i), g); err != nil {
			return err
		}
		if err := fr.Point2RD(fmt.Sprintf("geomesh_pts[%d].dest_pt", i), g); err != nil {
			return err
		}
	}
	nf, err := fr.BLv("num_geomesh_faces", g)
	if err != nil {
		return err
	}
	if nf > 50000 {
		return fmt.Errorf("cad: GEODATA geomesh_faces 数异常 %d", nf)
	}
	for i := 0; i < int(nf); i++ {
		for _, k := range []string{"face1", "face2", "face3"} {
			if _, err = fr.BLv(fmt.Sprintf("geomesh_faces[%d].%s", i, k), g); err != nil {
				return err
			}
		}
	}
	return nil
}

// decodeGenericGEODATA_HDL handle 流：host_block（1 个）。
func decodeGenericGEODATA_HDL(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	h, e := objrec.ReadHandleReference(R, g.Handle)
	if e != nil {
		return e
	}
	g.Handles = append(g.Handles, h)
	return nil
}

// decodeGenericSECTION_MANAGER 解析 SECTION_MANAGER：is_live B +
// num_sections BS；sections 句柄在 handle 流。
func decodeGenericSECTION_MANAGER(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := fr.B("is_live", g); err != nil {
		return err
	}
	ns, err := fr.R.ReadBS()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, ObjField{"num_sections", int64(ns)})
	g.HdlCount = int(ns)
	return nil
}

// decodeGenericSECTION_MANAGER_HDL handle 流：sections×num_sections。
func decodeGenericSECTION_MANAGER_HDL(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	for i := 0; i < g.HdlCount; i++ {
		h, e := objrec.ReadHandleReference(R, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// decodeGenericSECTION_SETTINGS 解析 SECTION_SETTINGS：curr_type BL +
// num_types BL + types×N（type/generation/num_sources BL +
// sources 句柄 + destblock 句柄 + destfile T + num_geom BL + geom×M
// 几何设置组）。
func decodeGenericSECTION_SETTINGS(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := fr.BL("curr_type", g); err != nil {
		return err
	}
	nt, err := fr.BLv("num_types", g)
	if err != nil {
		return err
	}
	if nt < 0 || nt > 4 {
		return fmt.Errorf("cad: SECTION_SETTINGS types 数异常 %d", nt)
	}
	for i := 0; i < int(nt); i++ {
		p := fmt.Sprintf("types[%d].", i)
		if err := fr.BL(p+"type", g); err != nil {
			return err
		}
		if err := fr.BL(p+"generation", g); err != nil {
			return err
		}
		ns, err := fr.BLv(p+"num_sources", g)
		if err != nil {
			return err
		}
		if ns < 0 || ns > 1_000_000 {
			return fmt.Errorf("cad: SECTION_SETTINGS sources 数异常 %d", ns)
		}
		g.HdlCount += int(ns)
		g.HdlCount++ // destblock
		if err := fr.T(p+"destfile", g); err != nil {
			return err
		}
		ng, err := fr.BLv(p+"num_geom", g)
		if err != nil {
			return err
		}
		if ng < 0 || ng > 1_000_000 {
			return fmt.Errorf("cad: SECTION_SETTINGS geom 数异常 %d", ng)
		}
		for j := 0; j < int(ng); j++ {
			gp := p + "geom[" + itoa(j) + "]."
			if err := fr.BL(gp+"num_geoms", g); err != nil {
				return err
			}
			if err := fr.BL(gp+"hexindex", g); err != nil {
				return err
			}
			if err := fr.BL(gp+"flags", g); err != nil {
				return err
			}
			if err := fr.CMC(gp+"color", g); err != nil {
				return err
			}
			for _, k := range []string{"layer", "ltype"} {
				if err := fr.T(gp+k, g); err != nil {
					return err
				}
			}
			if err := fr.BD(gp+"ltype_scale", g); err != nil {
				return err
			}
			if err := fr.T(gp+"plotstyle", g); err != nil {
				return err
			}
			if Ver >= container.VerR2004 { // SINCE (R_2000b)：R2000a 前无此字段
				if err := fr.BLd(gp+"linewt", g); err != nil {
					return err
				}
			}
			if err := fr.BS(gp+"face_transparency", g); err != nil {
				return err
			}
			if err := fr.BS(gp+"edge_transparency", g); err != nil {
				return err
			}
			if err := fr.BS(gp+"hatch_type", g); err != nil {
				return err
			}
			if err := fr.T(gp+"hatch_pattern", g); err != nil {
				return err
			}
			// spec DECODER：空 hatch_pattern 置默认 "SOLID"
			if v, _ := g.FieldPath(gp + "hatch_pattern").(string); v == "" {
				g.setFieldTop(ObjField{gp + "hatch_pattern", "SOLID"})
			}
			if err := fr.BD(gp+"hatch_angle", g); err != nil {
				return err
			}
			if err := fr.BD(gp+"hatch_spacing", g); err != nil {
				return err
			}
			if err := fr.BD(gp+"hatch_scale", g); err != nil {
				return err
			}
		}
	}
	return nil
}

// decodeGenericSECTION_SETTINGS_HDL handle 流：各 type 的 sources 与
// destblock（按 hdlCount 累计数）。
func decodeGenericSECTION_SETTINGS_HDL(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	for i := 0; i < g.HdlCount; i++ {
		h, e := objrec.ReadHandleReference(R, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// decodeGenericPLOTSETTINGS 解析 PLOTSETTINGS（dwg.spec）：设备/纸张名
// 与边距 + plot 视窗/单位 + stylesheet + std_scale + shadeplot。
// plotview（R2002+）/shadeplot（R2007a+）句柄在 handle 流。
func decodeGenericPLOTSETTINGS(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := fr.T("printer_cfg_file", g); err != nil {
		return err
	}
	if err := fr.T("paper_size", g); err != nil {
		return err
	}
	if err := fr.BS("plot_flags", g); err != nil {
		return err
	}
	for _, k := range []string{"left_margin", "bottom_margin", "right_margin", "top_margin", "paper_width", "paper_height"} {
		if err := fr.BD(k, g); err != nil {
			return err
		}
	}
	if err := fr.T("canonical_media_name", g); err != nil {
		return err
	}
	if err := fr.Point2("plot_origin", g); err != nil {
		return err
	}
	if err := fr.BS("plot_paper_unit", g); err != nil {
		return err
	}
	if err := fr.BS("plot_rotation_mode", g); err != nil {
		return err
	}
	if err := fr.BS("plot_type", g); err != nil {
		return err
	}
	if err := fr.Point2("plot_window_ll", g); err != nil {
		return err
	}
	if err := fr.Point2("plot_window_ur", g); err != nil {
		return err
	}
	if Ver >= container.VerR2000 {
		g.HdlCount++ // plotview（R2002+ 为句柄；R13-R14 段为 plotview_name T）
	}
	if err := fr.BD("paper_units", g); err != nil {
		return err
	}
	if err := fr.BD("drawing_units", g); err != nil {
		return err
	}
	if err := fr.T("stylesheet", g); err != nil {
		return err
	}
	if err := fr.BS("std_scale_type", g); err != nil {
		return err
	}
	if err := fr.BD("std_scale_factor", g); err != nil {
		return err
	}
	if err := fr.Point2("paper_image_origin", g); err != nil {
		return err
	}
	if Ver >= container.VerR2004 {
		for _, k := range []string{"shadeplot_type", "shadeplot_reslevel", "shadeplot_customdpi"} {
			if err := fr.BS(k, g); err != nil {
				return err
			}
		}
		if Ver >= container.VerR2007 {
			g.HdlCount++ // shadeplot
		}
	}
	return nil
}

// decodeGenericPLOTSETTINGS_HDL handle 流：plotview（R2002+）+
// shadeplot（R2007a+）。R13/R14 走 plotview_name T（无句柄）。
func decodeGenericPLOTSETTINGS_HDL(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	for i := 0; i < g.HdlCount; i++ {
		h, e := objrec.ReadHandleReference(R, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// decodeGenericLEADEROBJECTCONTEXTDATA 解析 LEADEROBJECTCONTEXTDATA
// （dwg2.spec，AcDbObjectContextData + AnnotScale + Leader 专有）：
// class_version BS + is_default B + num_points BL + points 3BD 向量 +
// x_direction 3BD + b290 B + inspt_offset/endptproj 3BD。scale 句柄在
// handle 流。
func decodeGenericLEADEROBJECTCONTEXTDATA(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := fr.BS("class_version", g); err != nil {
		return err
	}
	if err := fr.B("is_default", g); err != nil {
		return err
	}
	np, err := fr.BLv("num_points", g)
	if err != nil {
		return err
	}
	if np < 0 || np > 1_000_000 {
		return fmt.Errorf("cad: LEADEROBJECTCONTEXTDATA points 数异常 %d", np)
	}
	pts := make([][]float64, 0, np)
	for i := 0; i < int(np); i++ {
		x, y, z, e := R.Read3BD()
		if e != nil {
			return e
		}
		pts = append(pts, []float64{x, y, z})
	}
	g.Fields = append(g.Fields, ObjField{"points", pts})
	if err := fr.Point3("x_direction", g); err != nil {
		return err
	}
	if err := fr.B("b290", g); err != nil {
		return err
	}
	if err := fr.Point3("inspt_offset", g); err != nil {
		return err
	}
	return fr.Point3("endptproj", g)
}

// decodeGenericLEADEROBJECTCONTEXTDATA_HDL handle 流：scale（1 个）。
func decodeGenericLEADEROBJECTCONTEXTDATA_HDL(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	h, e := objrec.ReadHandleReference(R, g.Handle)
	if e != nil {
		return e
	}
	g.Handles = append(g.Handles, h)
	return nil
}

// init 注册设置类解码器。
func init() {
	// 类表 DXF 名与 gold object 名不同，经 jsonName 映射
	InternalClassDecoders["LEADEROBJECTCONTEXTDATA"] = internalObjectSpec{Decode: decodeGenericLEADEROBJECTCONTEXTDATA, Hdl: decodeGenericLEADEROBJECTCONTEXTDATA_HDL, jsonName: "LEADEROBJECTCONTEXTDATA"}
	InternalClassDecoders["ACDB_LEADEROBJECTCONTEXTDATA_CLASS"] = internalObjectSpec{Decode: decodeGenericLEADEROBJECTCONTEXTDATA, Hdl: decodeGenericLEADEROBJECTCONTEXTDATA_HDL, jsonName: "LEADEROBJECTCONTEXTDATA"}
	for name, d := range map[string]func(*bitstream.BitStream, container.DwgVersion, *GfRead, *ObjGeneric) error{
		"FIELD":            decodeGenericFIELD,
		"GEODATA":          decodeGenericGEODATA,
		"SECTION_MANAGER":  decodeGenericSECTION_MANAGER,
		"SECTION_SETTINGS": decodeGenericSECTION_SETTINGS,
		"PLOTSETTINGS":     decodeGenericPLOTSETTINGS,
	} {
		Hdl := decodeGenericFIELD_HDL
		switch name {
		case "GEODATA":
			Hdl = decodeGenericGEODATA_HDL
		case "SECTION_MANAGER":
			Hdl = decodeGenericSECTION_MANAGER_HDL
		case "SECTION_SETTINGS":
			Hdl = decodeGenericSECTION_SETTINGS_HDL
		case "PLOTSETTINGS":
			Hdl = decodeGenericPLOTSETTINGS_HDL
		}
		InternalClassDecoders[name] = internalObjectSpec{Decode: d, Hdl: Hdl}
	}
}
