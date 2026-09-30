// 本文件实现渲染设置族内部对象（dwg2.spec）：RENDERSETTINGS/
// MENTALRAYRENDERSETTINGS/RAPIDRTRENDERSETTINGS（AcDbRenderSettings
// 公共前导）、RENDERENTRY、RENDERGLOBAL。

package object

import (
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
)

// decodeAcisRenderSettingsFields 读 AcDbRenderSettings_fields：
// class_version BL（R2013+ 位流存 class_version+1，读后减 1 且 JSON
// 不输出该键——LibreDWG VALUE_BL 语义）+ name T + fog/backfaces/
// environ 4×B + environ_image_filename T + description T +
// display_index BL + has_predefined B（仅 R2013+）。
func decodeAcisRenderSettingsFields(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	cv, err := R.ReadBL()
	if err != nil {
		return err
	}
	if Ver >= container.VerR2013 {
		// R2013+ 位流为 +1 偏移；VALUE_BL 不产生 JSON 键，此处保存仅供调试
		g.Fields = append(g.Fields, ObjField{"class_version", int64(cv) - 1})
	} else {
		g.Fields = append(g.Fields, ObjField{"class_version", int64(cv)})
	}
	if err := fr.T("name", g); err != nil {
		return err
	}
	if err := fr.B("fog_enabled", g); err != nil {
		return err
	}
	if err := fr.B("fog_background_enabled", g); err != nil {
		return err
	}
	if err := fr.B("backfaces_enabled", g); err != nil {
		return err
	}
	if err := fr.B("environ_image_enabled", g); err != nil {
		return err
	}
	if err := fr.T("environ_image_filename", g); err != nil {
		return err
	}
	if err := fr.T("description", g); err != nil {
		return err
	}
	if err := fr.BL("display_index", g); err != nil {
		return err
	}
	if Ver >= container.VerR2013 {
		return fr.B("has_predefined", g)
	}
	return nil
}

// decodeGenericRENDERSETTINGS RENDERSETTINGS 基类：仅 settings 公共字段。
func decodeGenericRENDERSETTINGS(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	return decodeAcisRenderSettingsFields(R, Ver, fr, g)
}

// decodeGenericMENTALRAYRENDERSETTINGS MENTALRAYRENDERSETTINGS：settings
// 公共字段 + AcDbMentalRayRenderSettings 专有（mr_version 起 41 字段）。
func decodeGenericMENTALRAYRENDERSETTINGS(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := decodeAcisRenderSettingsFields(R, Ver, fr, g); err != nil {
		return err
	}
	if err := fr.BL("mr_version", g); err != nil {
		return err
	}
	for _, k := range []string{"sampling1", "sampling2"} {
		if err := fr.BL(k, g); err != nil {
			return err
		}
	}
	if err := fr.BS("sampling_mr_filter", g); err != nil {
		return err
	}
	for _, k := range []string{"sampling_filter1", "sampling_filter2",
		"sampling_contrast_color1", "sampling_contrast_color2",
		"sampling_contrast_color3", "sampling_contrast_color4"} {
		if err := fr.BD(k, g); err != nil {
			return err
		}
	}
	if err := fr.BS("shadow_mode", g); err != nil {
		return err
	}
	if err := fr.B("shadow_maps_enabled", g); err != nil {
		return err
	}
	if err := fr.B("ray_tracing_enabled", g); err != nil {
		return err
	}
	for _, k := range []string{"ray_trace_depth1", "ray_trace_depth2", "ray_trace_depth3"} {
		if err := fr.BL(k, g); err != nil {
			return err
		}
	}
	if err := fr.B("global_illumination_enabled", g); err != nil {
		return err
	}
	if err := fr.BL("gi_sample_count", g); err != nil {
		return err
	}
	if err := fr.B("gi_sample_radius_enabled", g); err != nil {
		return err
	}
	if err := fr.BD("gi_sample_radius", g); err != nil {
		return err
	}
	if err := fr.BL("gi_photons_per_light", g); err != nil {
		return err
	}
	for _, k := range []string{"photon_trace_depth1", "photon_trace_depth2", "photon_trace_depth3"} {
		if err := fr.BL(k, g); err != nil {
			return err
		}
	}
	if err := fr.B("final_gathering_enabled", g); err != nil {
		return err
	}
	if err := fr.BL("fg_ray_count", g); err != nil {
		return err
	}
	for _, k := range []string{"fg_sample_radius_state1", "fg_sample_radius_state2", "fg_sample_radius_state3"} {
		if err := fr.B(k, g); err != nil {
			return err
		}
	}
	if err := fr.BD("fg_sample_radius1", g); err != nil {
		return err
	}
	if err := fr.BD("fg_sample_radius2", g); err != nil {
		return err
	}
	if err := fr.BD("light_luminance_scale", g); err != nil {
		return err
	}
	if err := fr.BS("diagnostics_mode", g); err != nil {
		return err
	}
	if err := fr.BS("diagnostics_grid_mode", g); err != nil {
		return err
	}
	if err := fr.BD("diagnostics_grid_float", g); err != nil {
		return err
	}
	if err := fr.BS("diagnostics_photon_mode", g); err != nil {
		return err
	}
	if err := fr.BS("diagnostics_bsp_mode", g); err != nil {
		return err
	}
	if err := fr.B("export_mi_enabled", g); err != nil {
		return err
	}
	if err := fr.T("mr_description", g); err != nil {
		return err
	}
	if err := fr.BL("tile_size", g); err != nil {
		return err
	}
	if err := fr.BS("tile_order", g); err != nil {
		return err
	}
	if err := fr.BL("memory_limit", g); err != nil {
		return err
	}
	if err := fr.B("diagnostics_samples_mode", g); err != nil {
		return err
	}
	return fr.BD("energy_multiplier", g)
}

// decodeGenericRAPIDRTRENDERSETTINGS RAPIDRTRENDERSETTINGS：settings
// 公共字段 + AcDbRapidRTRenderSettings 专有（rapidrt_version 起 8 字段；
// pre-R2013 时 has_predefined 移到体尾）。
func decodeGenericRAPIDRTRENDERSETTINGS(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := decodeAcisRenderSettingsFields(R, Ver, fr, g); err != nil {
		return err
	}
	if err := fr.BL("rapidrt_version", g); err != nil {
		return err
	}
	if err := fr.BL("render_target", g); err != nil {
		return err
	}
	if err := fr.BL("render_level", g); err != nil {
		return err
	}
	if err := fr.BL("render_time", g); err != nil {
		return err
	}
	if err := fr.BL("lighting_model", g); err != nil {
		return err
	}
	if err := fr.BL("filter_type", g); err != nil {
		return err
	}
	if err := fr.BD("filter_width", g); err != nil {
		return err
	}
	if err := fr.BD("filter_height", g); err != nil {
		return err
	}
	if Ver < container.VerR2013 {
		return fr.B("has_predefined", g)
	}
	return nil
}

// decodeGenericRENDERENTRY RENDERENTRY：AcDbRenderEntry 全字段
// （class_version 起 18 字段，start 组无 hour——spec 即 6 个 BS）。
func decodeGenericRENDERENTRY(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := fr.BL("class_version", g); err != nil {
		return err
	}
	if err := fr.T("image_file_name", g); err != nil {
		return err
	}
	if err := fr.T("preset_name", g); err != nil {
		return err
	}
	if err := fr.T("view_name", g); err != nil {
		return err
	}
	if err := fr.BL("dimension_x", g); err != nil {
		return err
	}
	if err := fr.BL("dimension_y", g); err != nil {
		return err
	}
	for _, k := range []string{"start_year", "start_month", "start_day",
		"start_minute", "start_second", "start_msec"} {
		if err := fr.BS(k, g); err != nil {
			return err
		}
	}
	if err := fr.BD("render_time", g); err != nil {
		return err
	}
	for _, k := range []string{"memory_amount", "material_count",
		"light_count", "triangle_count", "display_index"} {
		if err := fr.BL(k, g); err != nil {
			return err
		}
	}
	return nil
}

// decodeGenericRENDERGLOBAL RENDERGLOBAL：AcDbRenderGlobal 全字段。
func decodeGenericRENDERGLOBAL(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := fr.BL("class_version", g); err != nil {
		return err
	}
	if err := fr.BL("procedure", g); err != nil {
		return err
	}
	if err := fr.BL("destination", g); err != nil {
		return err
	}
	if err := fr.B("save_enabled", g); err != nil {
		return err
	}
	if err := fr.T("save_filename", g); err != nil {
		return err
	}
	if err := fr.BL("image_width", g); err != nil {
		return err
	}
	if err := fr.BL("image_height", g); err != nil {
		return err
	}
	if err := fr.B("predef_presets_first", g); err != nil {
		return err
	}
	return fr.B("highlevel_info", g)
}

// init 注册渲染设置族解码器。
func init() {
	for name, d := range map[string]func(*bitstream.BitStream, container.DwgVersion, *GfRead, *ObjGeneric) error{
		"RENDERSETTINGS":          decodeGenericRENDERSETTINGS,
		"MENTALRAYRENDERSETTINGS": decodeGenericMENTALRAYRENDERSETTINGS,
		"RAPIDRTRENDERSETTINGS":   decodeGenericRAPIDRTRENDERSETTINGS,
		"RENDERENTRY":             decodeGenericRENDERENTRY,
		"RENDERGLOBAL":            decodeGenericRENDERGLOBAL,
	} {
		InternalClassDecoders[name] = internalObjectSpec{Decode: d}
	}
}
