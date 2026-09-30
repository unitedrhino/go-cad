// 本文件实现展示类内部对象的解码：VISUALSTYLE（507）、LAYOUT（82）、
// MATERIAL（506）、SUN、ACSH_HISTORY_CLASS、TABLEGEOMETRY、PLACEHOLDER、
// DICTIONARYWDFLT。字段序对照 dwg.spec / dwg2.spec 与 dwgread -v9 日志。

package cad

import (
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"os"
)

// ---- VISUALSTYLE（dwg2.spec DWG_OBJECT(VISUALSTYLE)，类 507）----

// decodeGenericVISUALSTYLE 解析 VISUALSTYLE：
// R2007 前与 R2007 为老序列（face/edge 字段 + 条件尾块）；
// R2010b+ 为 ext_lighting_model + internal_only + 字段对（值+_int 标志）；
// R2013b+ 再追加 b_prop/s 系列属性对（num_props 固定 58，dat 流不存）。
func decodeGenericVISUALSTYLE(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := fr.T("description", g); err != nil {
		return err
	}
	if err := fr.BL("style_type", g); err != nil {
		return err
	}
	if ver < verR2010 {
		return decodeVisualStyleOld(r, ver, fr, g)
	}
	return decodeVisualStyleNew(r, ver, fr, g)
}

// decodeVisualStyleOld R2007 及更早的 VISUALSTYLE 字段序列。
func decodeVisualStyleOld(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	steps := []struct {
		read func() error
	}{
		{func() error { return fr.BL("face_lighting_model", g) }},
		{func() error { return fr.BL("face_lighting_quality", g) }},
		{func() error { return fr.BL("face_color_mode", g) }},
		{func() error { return fr.BD("face_opacity", g) }},
		{func() error { return fr.BD("face_specular", g) }},
		{func() error { return fr.CMC("face_mono_color", g) }},
		{func() error { return fr.BL("face_modifier", g) }},
		{func() error { return fr.BL("edge_model", g) }},
		{func() error { return fr.BL("edge_style", g) }},
		{func() error { return fr.CMC("edge_intersection_color", g) }},
		{func() error { return fr.CMC("edge_obscured_color", g) }},
		{func() error { return fr.BL("edge_obscured_ltype", g) }},
		{func() error { return fr.BD("edge_crease_angle", g) }},
		{func() error { return fr.BL("edge_modifier", g) }},
		{func() error { return fr.CMC("edge_color", g) }},
		{func() error { return fr.BD("edge_opacity", g) }},
		{func() error { return fr.BS("edge_width", g) }},
		{func() error { return fr.BS("edge_overhang", g) }},
		{func() error { return fr.BL("edge_jitter", g) }},
		{func() error { return fr.CMC("edge_silhouette_color", g) }},
		{func() error { return fr.BS("edge_silhouette_width", g) }},
		{func() error { return fr.RC("edge_halo_gap", g) }},
		{func() error { return fr.BS("edge_isolines", g) }},
		{func() error { return fr.B("edge_do_hide_precision", g) }},
		{func() error { return fr.BS("edge_style_apply", g) }},
		{func() error { return fr.BS("edge_intersection_ltype", g) }},
		{func() error { return fr.BL("display_settings", g) }},
		{func() error {
			// BLd 有符号（gold 出现 -50 等，uint32 直读会得 4294967246）
			v, err := fr.r.ReadBL()
			if err != nil {
				return err
			}
			g.Fields = append(g.Fields, objField{"display_brightness_bl", int64(int32(v))})
			return nil
		}},
	}
	for _, s := range steps {
		if err := s.read(); err != nil {
			return err
		}
	}
	// 条件尾块：handle 流起点减当前位若容得下则继续
	// （display_shadow_type BL + [R2007: bd2007_45 BD] + internal_only B）
	tail := []struct {
		r2007Only bool
		read      func() error
	}{
		{false, func() error { return fr.BL("display_shadow_type", g) }},
		{true, func() error { return fr.BD("bd2007_45", g) }},
		{false, func() error { return fr.B("internal_only", g) }},
	}
	for _, s := range tail {
		if s.r2007Only && ver != verR2007 {
			continue
		}
		if err := s.read(); err != nil {
			return err
		}
	}
	return nil
}

// decodeVisualStyleNew R2010b+ 的字段对序列（值 + _int 标志）；
// R2013b+ 追加 b_prop/s 系列属性对。
func decodeVisualStyleNew(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := fr.BS("ext_lighting_model", g); err != nil {
		return err
	}
	if err := fr.B("internal_only", g); err != nil {
		return err
	}
	pairs := []struct {
		read func() error
		intF func() error
	}{
		{func() error { return fr.BL("face_lighting_model", g) }, func() error { return fr.BS("face_lighting_model_int", g) }},
		{func() error { return fr.BL("face_lighting_quality", g) }, func() error { return fr.BS("face_lighting_quality_int", g) }},
		{func() error { return fr.BL("face_color_mode", g) }, func() error { return fr.BS("face_color_mode_int", g) }},
		{func() error { return fr.BS("face_modifier", g) }, func() error { return fr.BS("face_modifier_int", g) }},
		{func() error { return fr.BD("face_opacity", g) }, func() error { return fr.BS("face_opacity_int", g) }},
		{func() error { return fr.BD("face_specular", g) }, func() error { return fr.BS("face_specular_int", g) }},
		{func() error { return fr.CMC("face_mono_color", g) }, func() error { return fr.BS("face_mono_color_int", g) }},
		{func() error { return fr.BL("edge_model", g) }, func() error { return fr.BS("edge_model_int", g) }},
		{func() error { return fr.BL("edge_style", g) }, func() error { return fr.BS("edge_style_int", g) }},
		{func() error { return fr.CMC("edge_intersection_color", g) }, func() error { return fr.BS("edge_intersection_color_int", g) }},
		{func() error { return fr.CMC("edge_obscured_color", g) }, func() error { return fr.BS("edge_obscured_color_int", g) }},
		{func() error { return fr.BL("edge_obscured_ltype", g) }, func() error { return fr.BS("edge_obscured_ltype_int", g) }},
		{func() error { return fr.BL("edge_intersection_ltype", g) }, func() error { return fr.BS("edge_intersection_ltype_int", g) }},
		{func() error { return fr.BD("edge_crease_angle", g) }, func() error { return fr.BS("edge_crease_angle_int", g) }},
		{func() error { return fr.BL("edge_modifier", g) }, func() error { return fr.BS("edge_modifier_int", g) }},
		{func() error { return fr.CMC("edge_color", g) }, func() error { return fr.BS("edge_color_int", g) }},
		{func() error { return fr.BD("edge_opacity", g) }, func() error { return fr.BS("edge_opacity_int", g) }},
		{func() error { return fr.BL("edge_width", g) }, func() error { return fr.BS("edge_width_int", g) }},
		{func() error { return fr.BL("edge_overhang", g) }, func() error { return fr.BS("edge_overhang_int", g) }},
		{func() error { return fr.BL("edge_jitter", g) }, func() error { return fr.BS("edge_jitter_int", g) }},
		{func() error { return fr.CMC("edge_silhouette_color", g) }, func() error { return fr.BS("edge_silhouette_color_int", g) }},
		{func() error { return fr.BL("edge_silhouette_width", g) }, func() error { return fr.BS("edge_silhouette_width_int", g) }},
		{func() error { return fr.BL("edge_halo_gap", g) }, func() error { return fr.BS("edge_halo_gap_int", g) }},
		{func() error { return fr.BL("edge_isolines", g) }, func() error { return fr.BS("edge_isolines_int", g) }},
		{func() error { return fr.B("edge_do_hide_precision", g) }, func() error { return fr.BS("edge_do_hide_precision_int", g) }},
		{func() error { return fr.BL("display_settings", g) }, func() error { return fr.BS("display_settings_int", g) }},
		{func() error { return fr.BD("display_brightness", g) }, func() error { return fr.BS("display_brightness_int", g) }},
		{func() error { return fr.BL("display_shadow_type", g) }, func() error { return fr.BS("display_shadow_type_int", g) }},
	}
	for _, p := range pairs {
		if err := p.read(); err != nil {
			return err
		}
		if err := p.intF(); err != nil {
			return err
		}
	}
	if ver >= verR2013 {
		return decodeVisualStyleProps(ver, fr, g)
	}
	return nil
}

// decodeVisualStyleProps R2013b+ 的 b_prop/s 系列属性对（num_props=58）。
func decodeVisualStyleProps(ver dwgVersion, fr *gfRead, g *objGeneric) error {
	bNames := []string{"b_prop1c", "b_prop1d", "b_prop1e", "b_prop1f", "b_prop20",
		"b_prop21", "b_prop22", "b_prop23", "b_prop24"}
	for _, n := range bNames {
		if err := fr.B(n, g); err != nil {
			return err
		}
		if err := fr.BS(n+"_int", g); err != nil {
			return err
		}
	}
	steps := []struct {
		read func() error
		intF func() error
	}{
		{func() error { return fr.BL("bl_prop25", g) }, func() error { return fr.BS("bl_prop25_int", g) }},
		{func() error { return fr.BD("bd_prop26", g) }, func() error { return fr.BS("bd_prop26_int", g) }},
		{func() error { return fr.BD("bd_prop27", g) }, func() error { return fr.BS("bd_prop27_int", g) }},
		{func() error { return fr.BL("bl_prop28", g) }, func() error { return fr.BS("bl_prop28_int", g) }},
		{func() error { return fr.CMC("c_prop29", g) }, func() error { return fr.BS("c_prop29_int", g) }},
		{func() error { return fr.BL("bl_prop2a", g) }, func() error { return fr.BS("bl_prop2a_int", g) }},
		{func() error { return fr.BL("bl_prop2b", g) }, func() error { return fr.BS("bl_prop2b_int", g) }},
		{func() error { return fr.CMC("c_prop2c", g) }, func() error { return fr.BS("c_prop2c_int", g) }},
		{func() error { return fr.B("b_prop2d", g) }, func() error { return fr.BS("b_prop2d_int", g) }},
		{func() error { return fr.BL("bl_prop2e", g) }, func() error { return fr.BS("bl_prop2e_int", g) }},
		{func() error { return fr.BL("bl_prop2f", g) }, func() error { return fr.BS("bl_prop2f_int", g) }},
		{func() error { return fr.BL("bl_prop30", g) }, func() error { return fr.BS("bl_prop30_int", g) }},
		{func() error { return fr.B("b_prop31", g) }, func() error { return fr.BS("b_prop31_int", g) }},
		{func() error { return fr.BL("bl_prop32", g) }, func() error { return fr.BS("bl_prop32_int", g) }},
		{func() error { return fr.CMC("c_prop33", g) }, func() error { return fr.BS("c_prop33_int", g) }},
		{func() error { return fr.BD("bd_prop34", g) }, func() error { return fr.BS("bd_prop34_int", g) }},
		{func() error { return fr.BL("edge_wiggle", g) }, func() error { return fr.BS("edge_wiggle_int", g) }},
		{func() error { return fr.T("strokes", g) }, func() error { return fr.BS("strokes_int", g) }},
		{func() error { return fr.B("b_prop37", g) }, func() error { return fr.BS("b_prop37_int", g) }},
		{func() error { return fr.BD("bd_prop38", g) }, func() error { return fr.BS("bd_prop38_int", g) }},
		{func() error { return fr.BD("bd_prop39", g) }, func() error { return fr.BS("bd_prop39_int", g) }},
	}
	for _, p := range steps {
		if err := p.read(); err != nil {
			return err
		}
		if err := p.intF(); err != nil {
			return err
		}
	}
	return nil
}

// ---- LAYOUT（dwg.spec DWG_OBJECT(LAYOUT)，类 82，含 plotsettings 块）----

// decodeGenericLAYOUT 解析 LAYOUT（R2004+ 样本路径）：
// dat 流 = AcDbPlotSettings 块（打印机/纸张/边距/原点/窗口/比例/阴影
// 打印设置）+ AcDbLayout 块（名称/tab 序/标志/范围/UCS/视口数）。
// 全部 T 字段经字符串流（has_strings 时）；handle 流 = owner + reactors +
// xdic + block_header + active_viewport + base_ucs + named_ucs +
// viewports×num_viewports。
func decodeGenericLAYOUT(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	steps := []func() error{
		func() error { return fr.T("plotsettings.printer_cfg_file", g) },
		func() error { return fr.T("plotsettings.paper_size", g) },
		func() error { return fr.BS("plotsettings.plot_flags", g) },
		func() error { return fr.BD("plotsettings.left_margin", g) },
		func() error { return fr.BD("plotsettings.bottom_margin", g) },
		func() error { return fr.BD("plotsettings.right_margin", g) },
		func() error { return fr.BD("plotsettings.top_margin", g) },
		func() error { return fr.BD("plotsettings.paper_width", g) },
		func() error { return fr.BD("plotsettings.paper_height", g) },
		func() error { return fr.T("plotsettings.canonical_media_name", g) },
		func() error { return fr.Point2("plotsettings.plot_origin", g) },
		func() error { return fr.BS("plotsettings.plot_paper_unit", g) },
		func() error { return fr.BS("plotsettings.plot_rotation_mode", g) },
		func() error { return fr.BS("plotsettings.plot_type", g) },
		func() error { return fr.Point2("plotsettings.plot_window_ll", g) },
		func() error { return fr.Point2("plotsettings.plot_window_ur", g) },
		func() error {
			// R2002 及更早（R13/R14/R2000）：plotview_name T
			// （R2004+ 改为 handle 流的 plotview 句柄）
			if ver == verR13 || ver == verR14 || ver == verR2000 {
				return fr.T("plotsettings.plotview_name", g)
			}
			return nil
		},
		func() error { return fr.BD("plotsettings.paper_units", g) },
		func() error { return fr.BD("plotsettings.drawing_units", g) },
		func() error { return fr.T("plotsettings.stylesheet", g) },
		func() error { return fr.BS("plotsettings.std_scale_type", g) },
		func() error { return fr.BD("plotsettings.std_scale_factor", g) },
		func() error { return fr.Point2("plotsettings.paper_image_origin", g) },
		func() error {
			if ver < verR2004 {
				return nil // shadeplot 系列为 R2004a+
			}
			return fr.BS("plotsettings.shadeplot_type", g)
		},
		func() error {
			if ver < verR2004 {
				return nil
			}
			return fr.BS("plotsettings.shadeplot_reslevel", g)
		},
		func() error {
			if ver < verR2004 {
				return nil
			}
			return fr.BS("plotsettings.shadeplot_customdpi", g)
		},
		func() error { return fr.T("layout_name", g) },
		func() error { return fr.BS("tab_order", g) },
		func() error { return fr.BS("layout_flags", g) },
		func() error { return fr.Point3("INSBASE", g) },
		func() error { return fr.Point2RD("LIMMIN", g) },
		func() error { return fr.Point2RD("LIMMAX", g) },
		func() error { return fr.Point3("UCSORG", g) },
		func() error { return fr.Point3("UCSXDIR", g) },
		func() error { return fr.Point3("UCSYDIR", g) },
		func() error { return fr.BD("ucs_elevation", g) },
		func() error { return fr.BS("UCSORTHOVIEW", g) },
		func() error { return fr.Point3("EXTMIN", g) },
		func() error { return fr.Point3("EXTMAX", g) },
		func() error {
			if ver < verR2004 {
				return nil // num_viewports 为 R2004a+
			}
			return fr.BL("num_viewports", g)
		},
	}
	_dbgLayout := os.Getenv("CAD_CLASSES_DBG") != ""
	for i, s := range steps {
		if err := s(); err != nil {
			if _dbgLayout {
				fmt.Fprintf(os.Stderr, "[lay] step %d err=%v\n", i, err)
			}
			return err
		}
	}
	return nil
}

// Point2 读 2 个 BD（FIELD_2BD_1：BB 前缀位压缩双精度，0.0 仅 2 位）。
func (f *gfRead) Point2(key string, g *objGeneric) error {
	x, err := f.r.ReadBD()
	if err != nil {
		return err
	}
	y, err := f.r.ReadBD()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, objField{key, []float64{x, y}})
	return nil
}

// Point3 读 3BD（BB 前缀三坐标）。
func (f *gfRead) Point3(key string, g *objGeneric) error {
	x, y, z, err := f.r.Read3BD()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, objField{key, []float64{x, y, z}})
	return nil
}

// Point2RD 读 2 个 raw double（FIELD_2RD：无压缩前缀，LIMMIN/LIMMAX 等）。
func (f *gfRead) Point2RD(key string, g *objGeneric) error {
	x, err := f.r.ReadRD()
	if err != nil {
		return err
	}
	y, err := f.r.ReadRD()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, objField{key, []float64{x, y}})
	return nil
}

// ---- MATERIAL（dwg2.spec DWG_OBJECT(MATERIAL)，类 506）----

// decodeGenericMATERIAL 解析 MATERIAL：name/description + 3 组 MAT_COLOR
// + 6 个 MAT_MAP（blendfactor/projection/tiling/autotransform/
// transmatrix 16×BD/source/[filename|texture]）+ 标量属性 + R2007a+
// 的 6 个尾字段。
func decodeGenericMATERIAL(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := fr.T("name", g); err != nil {
		return err
	}
	if err := fr.T("description", g); err != nil {
		return err
	}
	// MAT_COLOR：RC flag + BD factor + [flag==1: BLx rgb]
	matColor := func(prefix string) error {
		if err := fr.RC(prefix+".flag", g); err != nil {
			return err
		}
		if err := fr.BD(prefix+".factor", g); err != nil {
			return err
		}
		if v, _ := g.Field(prefix + ".flag").(int64); v == 1 {
			return fr.BL(prefix+".rgb", g)
		}
		return nil
	}
	// MAT_MAPPER：projection/tiling/autotransform RC + transmatrix 16×BD
	matMapper := func(prefix string) error {
		for _, k := range []string{"projection", "tiling", "autotransform"} {
			if err := fr.RC(prefix+"."+k, g); err != nil {
				return err
			}
		}
		tm := make([]float64, 16)
		for i := range tm {
			v, e := r.ReadBD()
			if e != nil {
				return e
			}
			tm[i] = v
		}
		g.Fields = append(g.Fields, objField{prefix + ".transmatrix", tm})
		return nil
	}
	// MAT_MAP：blendfactor BD + mapper + source RC + [1: filename T | 2: 纹理]
	matMap := func(prefix string) error {
		if err := fr.BD(prefix+".blendfactor", g); err != nil {
			return err
		}
		if err := matMapper(prefix); err != nil {
			return err
		}
		if err := fr.RC(prefix+".source", g); err != nil {
			return err
		}
		switch v, _ := g.Field(prefix + ".source").(int64); v {
		case 1:
			return fr.T(prefix+".filename", g)
		case 2:
			return fmt.Errorf("cad: MATERIAL %s 程序化纹理(source=2)暂不支持", prefix)
		}
		return nil
	}
	// 注意：color 与 map 在 dat 流中交错（ambient → diffuse → diffusemap →
	// specular_color → specularmap → ...），并非按类型分组
	if err := matColor("ambient_color"); err != nil {
		return err
	}
	if err := matColor("diffuse_color"); err != nil {
		return err
	}
	if err := matMap("diffusemap"); err != nil {
		return err
	}
	if err := matColor("specular_color"); err != nil {
		return err
	}
	if err := matMap("specularmap"); err != nil {
		return err
	}
	if err := fr.BD("specular_gloss_factor", g); err != nil {
		return err
	}
	if err := matMap("reflectionmap"); err != nil {
		return err
	}
	if err := fr.BD("opacity_percent", g); err != nil {
		return err
	}
	if err := matMap("opacitymap"); err != nil {
		return err
	}
	if err := matMap("bumpmap"); err != nil {
		return err
	}
	if err := fr.BD("refraction_index", g); err != nil {
		return err
	}
	if err := matMap("refractionmap"); err != nil {
		return err
	}
	if ver < verR2007 {
		return nil
	}
	for _, k := range []string{"translucence", "self_illumination"} {
		if err := fr.BD(k, g); err != nil {
			return err
		}
	}
	if err := fr.BD("reflectivity", g); err != nil {
		return err
	}
	for _, k := range []string{"illumination_model", "channel_flags", "mode"} {
		if err := fr.BL(k, g); err != nil {
			return err
		}
	}
	return nil
}

// ---- 长尾批次：SUN / ACSH_HISTORY_CLASS / TABLEGEOMETRY / PLACEHOLDER /
//      DICTIONARYWDFLT ----

// decodeGenericSUN 解析 SUN（dwg2.spec，类 534）：
// BL class_version + B is_on + CMC color + BD intensity + B has_shadow +
// BL julian_day + BL msecs + B is_dst + BL shadow_type +
// BS shadow_mapsize + RCd shadow_softness。
// decodeGenericSKYLIGHTBACKGROUND SKYLIGHT_BACKGROUND（AcDbSkyBackground）：
// BL class_version + sunid handle（handle 流，extraHandles=1）。
// R2010 可视化样本 skylight 首次暴露（51 实例）。
func decodeGenericSKYLIGHTBACKGROUND(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	return fr.BL("class_version", g)
}

func decodeGenericSUN(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := fr.BL("class_version", g); err != nil {
		return err
	}
	if err := fr.B("is_on", g); err != nil {
		return err
	}
	if err := fr.CMC("color", g); err != nil {
		return err
	}
	if err := fr.BD("intensity", g); err != nil {
		return err
	}
	if err := fr.B("has_shadow", g); err != nil {
		return err
	}
	if err := fr.BL("julian_day", g); err != nil {
		return err
	}
	if err := fr.BL("msecs", g); err != nil {
		return err
	}
	if err := fr.B("is_dst", g); err != nil {
		return err
	}
	if err := fr.BL("shadow_type", g); err != nil {
		return err
	}
	if err := fr.BS("shadow_mapsize", g); err != nil {
		return err
	}
	return fr.RC("shadow_softness", g)
}

// decodeGenericACSH_HISTORY_CLASS 解析 ACSH_HISTORY_CLASS（类 517）：
// BL major + BL minor + BL h_nodeid + B show_history + B record_history；
// handle 流额外含 owner H（1 个）。
func decodeGenericACSH_HISTORY_CLASS(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := fr.BL("major", g); err != nil {
		return err
	}
	if err := fr.BL("minor", g); err != nil {
		return err
	}
	if err := fr.BL("h_nodeid", g); err != nil {
		return err
	}
	if err := fr.B("show_history", g); err != nil {
		return err
	}
	return fr.B("record_history", g)
}

// decodeGenericTABLEGEOMETRY 解析 TABLEGEOMETRY（类 530）头部：
// BL numrows + BL numcols + BL num_cells；cells 向量内容复杂
// （geom_data_flag/尺寸/嵌套 handle），当前仅解析行列数。
func decodeGenericTABLEGEOMETRY(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := fr.BL("numrows", g); err != nil {
		return err
	}
	if err := fr.BL("numcols", g); err != nil {
		return err
	}
	n, err := fr.BLv("num_cells", g)
	if err != nil {
		return err
	}
	if n > 10000 {
		n = 10000
	}
	for i := 0; i < int(n); i++ {
		p := fmt.Sprintf("cells[%d].", i)
		if err := fr.BL(p+"geom_data_flag", g); err != nil {
			return err
		}
		if err := fr.BD(p+"width_w_gap", g); err != nil {
			return err
		}
		if err := fr.BD(p+"height_w_gap", g); err != nil {
			return err
		}
		// tablegeometry 句柄在 handle 流
		ng, err := fr.BLv(p+"num_geometry", g)
		if err != nil {
			return err
		}
		if ng > 10000 {
			ng = 10000
		}
		for j := 0; j < int(ng); j++ {
			q := fmt.Sprintf("%sgeometry[%d].", p, j)
			if err := fr.Point3(q+"dist_top_left", g); err != nil {
				return err
			}
			if err := fr.Point3(q+"dist_center", g); err != nil {
				return err
			}
			for _, k := range []string{"content_width", "content_height", "width", "height"} {
				if err := fr.BD(q+k, g); err != nil {
					return err
				}
			}
			if err := fr.BL(q+"unknown", g); err != nil {
				return err
			}
		}
	}
	return nil
}

// decodeGenericPLACEHOLDER 解析 PLACEHOLDER（固定码 0x50）：无 dat 字段。
func decodeGenericPLACEHOLDER(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	return nil
}

// ---- TABLESTYLE（dwg2.spec DWG_OBJECT(TABLESTYLE)，类 522）----

// readContentFormatFields 读取 ContentFormat_fields 宏（cell 内容格式）。
// text_style 句柄在 handle 流，nHdl 计数。
func readContentFormatFields(r *bitstream.BitStream, fr *gfRead, g *objGeneric, prefix string, nHdl *int) error {
	if err := fr.BL(prefix+"property_override_flags", g); err != nil {
		return err
	}
	if err := fr.BL(prefix+"property_flags", g); err != nil {
		return err
	}
	if err := fr.BL(prefix+"value_data_type", g); err != nil {
		return err
	}
	if err := fr.BL(prefix+"value_unit_type", g); err != nil {
		return err
	}
	if err := fr.T(prefix+"value_format_string", g); err != nil {
		return err
	}
	if err := fr.BD(prefix+"rotation", g); err != nil {
		return err
	}
	if err := fr.BD(prefix+"block_scale", g); err != nil {
		return err
	}
	if err := fr.BL(prefix+"cell_alignment", g); err != nil {
		return err
	}
	if err := fr.CMTC(prefix+"content_color", g); err != nil {
		return err
	}
	*nHdl++ // text_style
	if err := fr.BD(prefix+"text_height", g); err != nil {
		return err
	}
	return nil
}

// readCellStyleFields 读取 CellStyle_fields 宏（TABLESTYLE/TABLE/
// TABLECONTENT/CELLSTYLEMAP 共用的单元格样式，20.4.101.4）。
// borders[].ltype 与 content_format.text_style 句柄在 handle 流。
func readCellStyleFields(r *bitstream.BitStream, fr *gfRead, g *objGeneric, prefix string, nHdl *int) error {
	if err := fr.BL(prefix+"type", g); err != nil {
		return err
	}
	if err := fr.BS(prefix+"data_flags", g); err != nil {
		return err
	}
	df, _ := g.Field(prefix + "data_flags").(int64)
	if df == 0 {
		return nil
	}
	if err := fr.BL(prefix+"property_override_flags", g); err != nil {
		return err
	}
	if err := fr.BL(prefix+"merge_flags", g); err != nil {
		return err
	}
	if err := fr.CMTC(prefix+"bg_color", g); err != nil {
		return err
	}
	if err := fr.BL(prefix+"content_layout", g); err != nil {
		return err
	}
	if err := readContentFormatFields(r, fr, g, prefix+"content_format.", nHdl); err != nil {
		return err
	}
	if err := fr.BS(prefix+"margin_override_flags", g); err != nil {
		return err
	}
	mo, _ := g.Field(prefix + "margin_override_flags").(int64)
	if mo != 0 {
		for _, k := range []string{"vert_margin", "horiz_margin", "bottom_margin",
			"right_margin", "margin_horiz_spacing", "margin_vert_spacing"} {
			if err := fr.BD(prefix+k, g); err != nil {
				return err
			}
		}
	}
	nb, err := fr.BLv(prefix+"num_borders", g)
	if err != nil {
		return err
	}
	if nb > 6 {
		nb = 6
	}
	for i := 0; i < int(nb); i++ {
		im, err := fr.BLv(fmt.Sprintf("%sborders[%d].index_mask", prefix, i), g)
		if err != nil {
			return err
		}
		if im == 0 {
			continue
		}
		if err := fr.BL(fmt.Sprintf("%sborders[%d].border_overrides", prefix, i), g); err != nil {
			return err
		}
		if err := fr.BL(fmt.Sprintf("%sborders[%d].border_type", prefix, i), g); err != nil {
			return err
		}
		if err := fr.CMTC(fmt.Sprintf("%sborders[%d].color", prefix, i), g); err != nil {
			return err
		}
		// linewt 为 BLd 有符号（-2 = BYLAYER）
		lw, err := fr.r.ReadBL()
		if err != nil {
			return err
		}
		g.Fields = append(g.Fields, objField{fmt.Sprintf("%sborders[%d].linewt", prefix, i), int64(int32(lw))})
		*nHdl++ // ltype
		if err := fr.BL(fmt.Sprintf("%sborders[%d].visible", prefix, i), g); err != nil {
			return err
		}
		ds, err := fr.r.ReadBD()
		if err != nil {
			return err
		}
		g.Fields = append(g.Fields, objField{fmt.Sprintf("%sborders[%d].double_line_spacing", prefix, i), ds})
	}
	return nil
}

// decodeGenericTABLESTYLE 解析 TABLESTYLE（类 522）：
// R2010+ 为 unknown_rc + name + unknown_bl×2 + cellstyle 句柄 +
// sty.cellstyle（CellStyle_fields）+ sty.id/type/name + numoverrides
// （≠0 时 ovr.cellstyle 同套）；R2007 及更早为 name/flow_direction/
// flags/margins + 3 组 rowstyles（text_style 句柄在 handle 流）。
func decodeGenericTABLESTYLE(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	nHdl := 0
	if ver <= verR2007 {
		if err := fr.T("name", g); err != nil {
			return err
		}
		if err := fr.BS("flow_direction", g); err != nil {
			return err
		}
		if err := fr.BS("flags", g); err != nil {
			return err
		}
		if err := fr.BD("horiz_cell_margin", g); err != nil {
			return err
		}
		if err := fr.BD("vert_cell_margin", g); err != nil {
			return err
		}
		if err := fr.B("is_title_suppressed", g); err != nil {
			return err
		}
		if err := fr.B("is_header_suppressed", g); err != nil {
			return err
		}
		// 3 组 rowstyles：0=data 1=title 2=header
		for i := 0; i < 3; i++ {
			nHdl++ // text_style
			if err := fr.BD(fmt.Sprintf("rowstyles[%d].text_height", i), g); err != nil {
				return err
			}
			if err := fr.BS(fmt.Sprintf("rowstyles[%d].text_alignment", i), g); err != nil {
				return err
			}
			if err := fr.CMTC(fmt.Sprintf("rowstyles[%d].text_color", i), g); err != nil {
				return err
			}
			if err := fr.CMTC(fmt.Sprintf("rowstyles[%d].fill_color", i), g); err != nil {
				return err
			}
			if err := fr.B(fmt.Sprintf("rowstyles[%d].has_bgcolor", i), g); err != nil {
				return err
			}
			for b := 0; b < 6; b++ {
				// borders：BSd linewt + B visible + CMTC color
				lw, e := fr.r.ReadBS()
				if e != nil {
					return e
				}
				g.Fields = append(g.Fields, objField{
					fmt.Sprintf("rowstyles[%d].borders[%d].linewt", i, b), int64(int16(lw))})
				if err := fr.B(fmt.Sprintf("rowstyles[%d].borders[%d].visible", i, b), g); err != nil {
					return err
				}
				if err := fr.CMTC(fmt.Sprintf("rowstyles[%d].borders[%d].color", i, b), g); err != nil {
					return err
				}
			}
		}
		g.Fields = append(g.Fields, objField{"num_style_handles", int64(nHdl)})
		return nil
	}
	// R2010+ 布局
	if err := fr.RC("unknown_rc", g); err != nil {
		return err
	}
	if err := fr.T("name", g); err != nil {
		return err
	}
	if err := fr.BL("unknown_bl1", g); err != nil {
		return err
	}
	if err := fr.BL("unknown_bl2", g); err != nil {
		return err
	}
	nHdl++ // cellstyle 硬拥有句柄
	if err := readCellStyleFields(r, fr, g, "sty.cellstyle.", &nHdl); err != nil {
		return err
	}
	if err := fr.BL("sty.id", g); err != nil {
		return err
	}
	if err := fr.BL("sty.type", g); err != nil {
		return err
	}
	if err := fr.T("sty.name", g); err != nil {
		return err
	}
	no, err := fr.BLv("numoverrides", g)
	if err != nil {
		return err
	}
	if no != 0 {
		if err := fr.BL("unknown_bl3", g); err != nil {
			return err
		}
		if err := readCellStyleFields(r, fr, g, "ovr.cellstyle.", &nHdl); err != nil {
			return err
		}
		if err := fr.BL("ovr.id", g); err != nil {
			return err
		}
		if err := fr.BL("ovr.type", g); err != nil {
			return err
		}
		if err := fr.T("ovr.name", g); err != nil {
			return err
		}
	}
	g.Fields = append(g.Fields, objField{"num_style_handles", int64(nHdl)})
	return nil
}

// decodeGenericTABLESTYLE_HDL TABLESTYLE 的 handle 流：cellstyle +
// cellstyle 内 text_style/borders ltype + overrides 同套
// （顺序由 decode 阶段记录的 num_style_handles 决定）。
func decodeGenericTABLESTYLE_HDL(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	n := 0
	if v, ok := g.Field("num_style_handles").(int64); ok {
		n = int(v)
	}
	for i := 0; i < n; i++ {
		h, e := readHandleReference(r, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// ---- MLEADERSTYLE / SECTIONVIEWSTYLE / DETAILVIEWSTYLE（dwg2.spec）----

// decodeGenericMLEADERSTYLE 解析 MLEADERSTYLE（类 517）：
// SINCE R_2010b 读 class_version BS；内容/引线次序 BS 系 + 几何 BD 系 +
// line_color CMC + 描述/箭头/文字系 + block_scale 3BD（非 JSON 为 3×BD）
// + 尾部 SINCE R_2010b attach 系与 R_2013b text_extended。
// line_type/arrow_head/text_style/block 句柄在 handle 流（4 个）。
func decodeGenericMLEADERSTYLE(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	classVersion := int64(2)
	if ver >= verR2010 {
		// SINCE R_2010b 从流读 class_version；更早版本固定为 2（不占位）
		cv, err := fr.BSv("class_version", g)
		if err != nil {
			return err
		}
		classVersion = cv
	}
	g.Fields = append(g.Fields, objField{"class_version", classVersion})
	for _, k := range []string{"content_type", "mleader_order", "leader_order"} {
		if err := fr.BS(k, g); err != nil {
			return err
		}
	}
	if err := fr.BL("max_points", g); err != nil {
		return err
	}
	if err := fr.BD("first_seg_angle", g); err != nil {
		return err
	}
	if err := fr.BD("second_seg_angle", g); err != nil {
		return err
	}
	if err := fr.BS("type", g); err != nil {
		return err
	}
	if err := fr.CMC("line_color", g); err != nil {
		return err
	}
	// linewt 为 BLd 有符号（-2 = BYLAYER）
	lw, err := fr.r.ReadBL()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, objField{"linewt", int64(int32(lw))})
	if err := fr.B("has_landing", g); err != nil {
		return err
	}
	if err := fr.BD("landing_gap", g); err != nil {
		return err
	}
	if err := fr.B("has_dogleg", g); err != nil {
		return err
	}
	if err := fr.BD("landing_dist", g); err != nil {
		return err
	}
	if err := fr.T("description", g); err != nil {
		return err
	}
	if err := fr.BD("arrow_head_size", g); err != nil {
		return err
	}
	if err := fr.T("text_default", g); err != nil {
		return err
	}
	if err := fr.BS("attach_left", g); err != nil {
		return err
	}
	if err := fr.BS("attach_right", g); err != nil {
		return err
	}
	if classVersion >= 2 {
		if err := fr.BS("text_angle_type", g); err != nil {
			return err
		}
	}
	if err := fr.BS("text_align_type", g); err != nil {
		return err
	}
	if err := fr.CMC("text_color", g); err != nil {
		return err
	}
	if err := fr.BD("text_height", g); err != nil {
		return err
	}
	if err := fr.B("has_text_frame", g); err != nil {
		return err
	}
	if classVersion >= 2 {
		if err := fr.B("text_always_left", g); err != nil {
			return err
		}
	}
	if err := fr.BD("align_space", g); err != nil {
		return err
	}
	if err := fr.CMC("block_color", g); err != nil {
		return err
	}
	// block_scale 为 3×BD（DWG 流；DXF 才是 3BD 单字段）
	for _, k := range []string{"block_scale.x", "block_scale.y", "block_scale.z"} {
		if err := fr.BD(k, g); err != nil {
			return err
		}
	}
	if err := fr.B("use_block_scale", g); err != nil {
		return err
	}
	if err := fr.BD("block_rotation", g); err != nil {
		return err
	}
	if err := fr.B("use_block_rotation", g); err != nil {
		return err
	}
	if err := fr.BS("block_connection", g); err != nil {
		return err
	}
	if err := fr.BD("scale", g); err != nil {
		return err
	}
	if err := fr.B("is_changed", g); err != nil {
		return err
	}
	if err := fr.B("is_annotative", g); err != nil {
		return err
	}
	if err := fr.BD("break_size", g); err != nil {
		return err
	}
	if ver >= verR2010 {
		for _, k := range []string{"attach_dir", "attach_top", "attach_bottom"} {
			if err := fr.BS(k, g); err != nil {
				return err
			}
		}
	}
	if ver >= verR2013 {
		if err := fr.B("text_extended", g); err != nil {
			return err
		}
	}
	return nil
}

// decodeGenericMLEADERSTYLE_HDL MLEADERSTYLE 的 handle 流：
// line_type + arrow_head + text_style + block（4 个）。
func decodeGenericMLEADERSTYLE_HDL(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	for i := 0; i < 4; i++ {
		h, e := readHandleReference(r, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// readModelDocViewStyleHead 读取 SECTIONVIEWSTYLE/DETAILVIEWSTYLE 共用
// 的 AcDbModelDocViewStyle 头：mdoc_class_version BS + desc T +
// is_modified_for_recompute B + [R2018+ display_name T + viewstyle_flags BL]。
func readModelDocViewStyleHead(r *bitstream.BitStream, fr *gfRead, g *objGeneric, ver dwgVersion) error {
	if err := fr.BS("mdoc_class_version", g); err != nil {
		return err
	}
	if err := fr.T("desc", g); err != nil {
		return err
	}
	if err := fr.B("is_modified_for_recompute", g); err != nil {
		return err
	}
	if ver >= verR2018 {
		if err := fr.T("display_name", g); err != nil {
			return err
		}
		if err := fr.BL("viewstyle_flags", g); err != nil {
			return err
		}
	}
	return fr.BS("class_version", g)
}

// decodeGenericDETAILVIEWSTYLE 解析 DETAILVIEWSTYLE（类 525）：
// 模型文档视图头 + flags BL + 标识/箭头/边界/标签/连接/边框六个区块。
// 6 个句柄（identifier_style/arrow_symbol/boundary_ltype/
// viewlabel_text_style/connection_ltype/borderline_ltype）在 handle 流。
func decodeGenericDETAILVIEWSTYLE(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := readModelDocViewStyleHead(r, fr, g, ver); err != nil {
		return err
	}
	if err := fr.BL("flags", g); err != nil {
		return err
	}
	if err := fr.CMC("identifier_color", g); err != nil {
		return err
	}
	if err := fr.BD("identifier_height", g); err != nil {
		return err
	}
	if err := fr.T("identifier_exclude_characters", g); err != nil {
		return err
	}
	if err := fr.BD("identifier_offset", g); err != nil {
		return err
	}
	if err := fr.RC("identifier_placement", g); err != nil {
		return err
	}
	if err := fr.CMC("arrow_symbol_color", g); err != nil {
		return err
	}
	if err := fr.BD("arrow_symbol_size", g); err != nil {
		return err
	}
	if err := fr.BL("boundary_linewt", g); err != nil {
		return err
	}
	if err := fr.CMC("boundary_line_color", g); err != nil {
		return err
	}
	if err := fr.CMC("viewlabel_text_color", g); err != nil {
		return err
	}
	if err := fr.BD("viewlabel_text_height", g); err != nil {
		return err
	}
	if err := fr.BL("viewlabel_attachment", g); err != nil {
		return err
	}
	if err := fr.BD("viewlabel_offset", g); err != nil {
		return err
	}
	if err := fr.BL("viewlabel_alignment", g); err != nil {
		return err
	}
	if err := fr.T("viewlabel_pattern", g); err != nil {
		return err
	}
	// 连接线与边框线区（v9 实测 R2018 序）
	if err := fr.BL("connection_linewt", g); err != nil {
		return err
	}
	if err := fr.CMC("connection_line_color", g); err != nil {
		return err
	}
	if err := fr.BL("borderline_linewt", g); err != nil {
		return err
	}
	if err := fr.CMC("borderline_color", g); err != nil {
		return err
	}
	return fr.RC("model_edge", g)
}

// decodeGenericDETAILVIEWSTYLE_HDL DETAILVIEWSTYLE 的 handle 流 6 个。
func decodeGenericDETAILVIEWSTYLE_HDL(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	for i := 0; i < 6; i++ {
		h, e := readHandleReference(r, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// decodeGenericSECTIONVIEWSTYLE 解析 SECTIONVIEWSTYLE（类 526）：
// 模型文档视图头 + flags + 标识/箭头（起止）/剖切线/折弯线/标签/
// 填充区块 + 尾部位置偏移与 hatch_angles 向量。
// 6 个句柄（identifier_style/arrow_start_symbol/arrow_end_symbol/
// plane_ltype/bend_ltype/viewlabel_text_style）在 handle 流。
func decodeGenericSECTIONVIEWSTYLE(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := readModelDocViewStyleHead(r, fr, g, ver); err != nil {
		return err
	}
	if err := fr.BL("flags", g); err != nil {
		return err
	}
	if err := fr.CMC("identifier_color", g); err != nil {
		return err
	}
	if err := fr.BD("identifier_height", g); err != nil {
		return err
	}
	if err := fr.CMC("arrow_symbol_color", g); err != nil {
		return err
	}
	if err := fr.BD("arrow_symbol_size", g); err != nil {
		return err
	}
	if err := fr.T("identifier_exclude_characters", g); err != nil {
		return err
	}
	if err := fr.BD("arrow_symbol_extension_length", g); err != nil {
		return err
	}
	if err := fr.BL("plane_linewt", g); err != nil {
		return err
	}
	if err := fr.CMC("plane_line_color", g); err != nil {
		return err
	}
	if err := fr.BL("bend_linewt", g); err != nil {
		return err
	}
	if err := fr.CMC("bend_line_color", g); err != nil {
		return err
	}
	if err := fr.BD("bend_line_length", g); err != nil {
		return err
	}
	if err := fr.BD("end_line_length", g); err != nil {
		return err
	}
	if err := fr.CMC("viewlabel_text_color", g); err != nil {
		return err
	}
	if err := fr.BD("viewlabel_text_height", g); err != nil {
		return err
	}
	if err := fr.BL("viewlabel_attachment", g); err != nil {
		return err
	}
	if err := fr.BD("viewlabel_offset", g); err != nil {
		return err
	}
	if err := fr.BL("viewlabel_alignment", g); err != nil {
		return err
	}
	if err := fr.T("viewlabel_pattern", g); err != nil {
		return err
	}
	if err := fr.CMC("hatch_color", g); err != nil {
		return err
	}
	if err := fr.CMC("hatch_bg_color", g); err != nil {
		return err
	}
	if err := fr.T("hatch_pattern", g); err != nil {
		return err
	}
	if err := fr.BD("hatch_scale", g); err != nil {
		return err
	}
	if err := fr.BL("hatch_transparency", g); err != nil {
		return err
	}
	if err := fr.B("unknown_b1", g); err != nil {
		return err
	}
	if err := fr.B("unknown_b2", g); err != nil {
		return err
	}
	if err := fr.BL("identifier_position", g); err != nil {
		return err
	}
	if err := fr.BD("identifier_offset", g); err != nil {
		return err
	}
	if err := fr.BL("arrow_position", g); err != nil {
		return err
	}
	if err := fr.BD("end_line_overshoot", g); err != nil {
		return err
	}
	na, err := fr.BLv("num_hatch_angles", g)
	if err != nil {
		return err
	}
	for i := 0; i < int(na); i++ {
		if err := fr.BD(fmt.Sprintf("hatch_angles[%d]", i), g); err != nil {
			return err
		}
	}
	return nil
}

// decodeGenericSECTIONVIEWSTYLE_HDL SECTIONVIEWSTYLE 的 handle 流 6 个。
func decodeGenericSECTIONVIEWSTYLE_HDL(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	for i := 0; i < 6; i++ {
		h, e := readHandleReference(r, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// decodeGenericFIELDLIST 解析 FIELDLIST（AcDbIdSet）：
// BL num_fields + B unknown；fields 句柄向量在 handle 流。
func decodeGenericFIELDLIST(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	n, err := fr.BLv("num_fields", g)
	if err != nil {
		return err
	}
	if n > 20000 {
		n = 20000
	}
	return fr.B("unknown", g)
}

// decodeGenericMTEXTOBJECTCONTEXTDATA MTEXTOBJECTCONTEXTDATA
// （AcDbAnnotScaleObjectContextData，R2010+ 注释对象上下文）：BS
// class_version + B is_default + BL attachment + 3BD x_axis_dir/ins_pt
// （二进制序 ODA bug 反转）+ 4×BD 矩形/范围 + 分栏组；scale 句柄在
// handle 流（extraHandles=1）。fzw 4 实例实证。
func decodeGenericMTEXTOBJECTCONTEXTDATA(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := fr.BS("class_version", g); err != nil {
		return err
	}
	if err := fr.B("is_default", g); err != nil {
		return err
	}
	if err := fr.BL("attachment", g); err != nil {
		return err
	}
	if err := fr.Point3("x_axis_dir", g); err != nil {
		return err
	}
	if err := fr.Point3("ins_pt", g); err != nil {
		return err
	}
	for _, k := range []string{"rect_width", "rect_height", "extents_width", "extents_height"} {
		if err := fr.BD(k, g); err != nil {
			return err
		}
	}
	ct, err := fr.BLv("column_type", g)
	if err != nil {
		return err
	}
	if ct < 0 || ct > 2 {
		return fmt.Errorf("cad: MTEXTOBJECTCONTEXTDATA column_type 越界 %d", ct)
	}
	if ct != 0 {
		nch, err := fr.BLv("num_column_heights", g)
		if err != nil {
			return err
		}
		if err := fr.BD("column_width", g); err != nil {
			return err
		}
		if err := fr.BD("gutter", g); err != nil {
			return err
		}
		ah, err := fr.Bv("auto_height", g)
		if err != nil {
			return err
		}
		if err := fr.B("flow_reversed", g); err != nil {
			return err
		}
		if !ah && ct == 2 {
			for i := 0; i < int(nch); i++ {
				if err := fr.BD(fmt.Sprintf("column_heights[%d]", i), g); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// decodeGenericSPATIALFILTER SPATIAL_FILTER（AcDbSpatialFilter 剪裁过滤）：
// BS num_clip_verts + 2RD 向量 + 3BD 挤出/原点 + 显示/前后裁剪标志 +
// 2×12 BD 变换矩阵。fzw 1 实例实证。
func decodeGenericSPATIALFILTER(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	ncv, err := fr.BSv("num_clip_verts", g)
	if err != nil {
		return err
	}
	if ncv < 0 || ncv > 10000 {
		return fmt.Errorf("cad: SPATIAL_FILTER 剪裁点数异常 %d", ncv)
	}
	for i := 0; i < int(ncv); i++ {
		// FIELD_2RD_VECTOR：raw double 无 BB 前缀（LibreDWG [2RD] trace）
		if err := fr.Point2RD(fmt.Sprintf("clip_verts[%d]", i), g); err != nil {
			return err
		}
	}
	if err := fr.Point3("extrusion", g); err != nil {
		return err
	}
	if err := fr.Point3("origin", g); err != nil {
		return err
	}
	if err := fr.BS("display_boundary_on", g); err != nil {
		return err
	}
	fc, err := fr.BSv("front_clip_on", g)
	if err != nil {
		return err
	}
	if fc != 0 {
		if err := fr.BD("front_clip_z", g); err != nil {
			return err
		}
	}
	bc, err := fr.BSv("back_clip_on", g)
	if err != nil {
		return err
	}
	if bc != 0 {
		if err := fr.BD("back_clip_z", g); err != nil {
			return err
		}
	}
	for i := 0; i < 24; i++ {
		k := fmt.Sprintf("inverse_transform[%d]", i)
		if i >= 12 {
			k = fmt.Sprintf("transform[%d]", i-12)
		}
		if err := fr.BD(k, g); err != nil {
			return err
		}
	}
	return nil
}
