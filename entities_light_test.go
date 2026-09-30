// entities_light_test LIGHT 光源合成位流测试：光度分支（NOD 字典
// LIGHTINGUNITS=2 触发的 IES 子段）真实样本不触发，此处按 dwg2.spec
// 位序构造已知内容的 LIGHT 记录（R2004 布局，name/webfile TV 内联），
// 解码后逐字段断言；同时验证非光度场景不误读子段（位流回归检查）。
package cad

import (
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"testing"
)

// writeLightBase 写 LIGHT 基线 19 字段（R2004 布局：CMC 为 BS+BL+RC 结构、
// name TV 内联），参数控制 shadow 段之后的写入内容。
func writeLightBase(w *testsupport.BitWriter) {
	w.BL(2)                     // class_version
	w.TV("L1")                  // name
	w.BL(0)                     // type = 0 点光源
	w.B(1)                      // status
	w.BS(7)                     // light_color.index
	w.BL(0xc2000000 | 0x0000ff) // light_color.rgb（ByLayer method 高字节）
	w.RC(0)                     // light_color.flag
	w.B(1)                      // plot_glyph
	w.BD(1.5)                   // intensity
	w.B3BD(1, 2, 3)             // position
	w.B3BD(4, 5, 6)             // target
	w.BL(1)                     // attenuation_type
	w.B(0)                      // use_attenuation_limits
	w.BD(0)                     // attenuation_start_limit
	w.BD(0)                     // attenuation_end_limit
	w.BD(0.5)                   // hotspot_angle
	w.BD(1.2)                   // falloff_angle
	w.B(1)                      // cast_shadows
	w.BL(1)                     // shadow_type
	w.BS(512)                   // shadow_map_size
	w.RC(3)                     // shadow_map_softness
}

// writeLightPhotometric 写光度子段（is_photometric 且 has_photometric_data=1
// 时的 22 字段，spec 顺序）。
func writeLightPhotometric(w *testsupport.BitWriter) {
	w.B(1)                // has_photometric_data
	w.B(1)                // has_webfile
	w.TV("light.ies")     // webfile
	w.BS(1)               // physical_intensity_method
	w.BD(150)             // physical_intensity
	w.BD(2.5)             // illuminance_dist
	w.BS(0)               // lamp_color_type
	w.BD(3000)            // lamp_color_temp
	w.BS(4)               // lamp_color_preset
	w.B3BD(0.1, 0.2, 0.3) // web_rotation (3BD_1)
	w.BS(2)               // extlight_shape
	w.BD(0.4)             // extlight_length
	w.BD(0.5)             // extlight_width
	w.BD(0.6)             // extlight_radius
	w.BS(1)               // webfile_type
	w.BS(3)               // web_symetry
	w.BS(0)               // has_target_grip
	w.BD(800)             // web_flux
	w.BD(0.7)             // web_angle1
	w.BD(0.8)             // web_angle2
	w.BD(0.9)             // web_angle3
	w.BD(1.0)             // web_angle4
	w.BD(1.1)             // web_angle5
	w.BS(2)               // glyph_display_type
}

// decodeLightFromBits 走 TestDecodeLwPolylineFromBits 同款框架：
// 位 0 = UMC → OT → 公共头（R2013 noShadow-noLW 布局），随后 LIGHT 主体。
// dataEndBit 须在写 handle 流之前计算（主体结束位 = handle 流起点）。
func decodeLightFromBits(t *testing.T, body *testsupport.BitWriter, dataEndBit uint64, photometric bool, ver container.DwgVersion) *entLight {
	t.Helper()
	r := bitstream.NewBitStream(body.Bytes())
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	head, err := parseEntityHead(r, dataEndBit, featMaterialFlags|featVisualStyles|featDSBinary)
	if err != nil {
		t.Fatalf("公共头解析失败: %v", err)
	}
	ent, err := decodeLight(r, &head, ver, 30, photometric)
	if err != nil {
		t.Fatalf("LIGHT 解码失败: %v", err)
	}
	return ent.(*entLight)
}

// TestDecodeLightPhotometric 光度分支：基线 + IES 子段全部字段一致。
func TestDecodeLightPhotometric(t *testing.T) {
	w := writeEntityPrefix(testsupport.NewBitWriter(), 0x60)
	writeCommonHead(w, 200, 2)
	writeLightBase(w)
	writeLightPhotometric(w)
	// handle 流起点（主体结束位）：须在写 handle 流之前取
	dataEndBit := uint64((len(w.Data)-1)*8 + int(w.Bit))
	// handle 流：xdic + layer
	w.H(5, 0)   // xdic null
	w.H(5, 300) // layer

	l := decodeLightFromBits(t, w, dataEndBit, true, container.VerR2004)
	// 基线字段
	if l.classVersion != 2 || l.name != "L1" || l.lightType != 0 || !l.status {
		t.Errorf("基线字段: ver=%d name=%q type=%d status=%v", l.classVersion, l.name, l.lightType, l.status)
	}
	// rgb 0xc2000000|0x0000ff（蓝）经调色板反查 index=5（decodeCodepage 同口径）
	if l.lightColorIndex != 5 || !testsupport.NearEq(l.intensity, 1.5) {
		t.Errorf("颜色/强度: idx=%d intensity=%v", l.lightColorIndex, l.intensity)
	}
	if !l.castShadows || l.shadowMapSize != 512 || l.shadowMapSoftness != 3 {
		t.Errorf("shadow: cast=%v size=%d soft=%d", l.castShadows, l.shadowMapSize, l.shadowMapSoftness)
	}
	// 光度字段
	if !l.isPhotometric || !l.hasPhotometricImg || !l.hasWebfile {
		t.Fatalf("光度标志: isPhoto=%v hasData=%v hasWeb=%v", l.isPhotometric, l.hasPhotometricImg, l.hasWebfile)
	}
	if l.webfile != "light.ies" || l.physIntensityMthd != 1 || !testsupport.NearEq(l.physIntensity, 150) {
		t.Errorf("webfile=%q method=%d intensity=%v", l.webfile, l.physIntensityMthd, l.physIntensity)
	}
	if !testsupport.NearEq(l.illuminanceDist, 2.5) || l.lampColorType != 0 || !testsupport.NearEq(l.lampColorTemp, 3000) || l.lampColorPreset != 4 {
		t.Errorf("照度/灯色: dist=%v type=%d temp=%v preset=%d", l.illuminanceDist, l.lampColorType, l.lampColorTemp, l.lampColorPreset)
	}
	if !testsupport.NearEq(l.webRotation.x, 0.1) || !testsupport.NearEq(l.webRotation.y, 0.2) || !testsupport.NearEq(l.webRotation.z, 0.3) {
		t.Errorf("web_rotation=%v", l.webRotation)
	}
	if l.extlightShape != 2 || !testsupport.NearEq(l.extlightLength, 0.4) || !testsupport.NearEq(l.extlightWidth, 0.5) || !testsupport.NearEq(l.extlightRadius, 0.6) {
		t.Errorf("扩展灯形: shape=%d len=%v w=%v r=%v", l.extlightShape, l.extlightLength, l.extlightWidth, l.extlightRadius)
	}
	if l.webfileType != 1 || l.webSymetry != 3 || l.hasTargetGrip != 0 {
		t.Errorf("光域网: type=%d sym=%d grip=%d", l.webfileType, l.webSymetry, l.hasTargetGrip)
	}
	if !testsupport.NearEq(l.webFlux, 800) {
		t.Errorf("web_flux=%v", l.webFlux)
	}
	wantAngles := []float64{0.7, 0.8, 0.9, 1.0, 1.1}
	if len(l.webAngles) != 5 {
		t.Fatalf("web_angles=%v", l.webAngles)
	}
	for i, a := range wantAngles {
		if !testsupport.NearEq(l.webAngles[i], a) {
			t.Errorf("web_angle%d=%v want %v", i+1, l.webAngles[i], a)
		}
	}
	if l.glyphDisplayType != 2 {
		t.Errorf("glyph_display_type=%d", l.glyphDisplayType)
	}
	// handle 流定位正确（光度段全部消费后）：layer=300
	if l.layer != 300 {
		t.Errorf("layer=%d want 300（光度子段未精确消费）", l.layer)
	}
}

// TestDecodeLightBaseline 非光度场景：基线后直接进 handle 流，
// 若误读光度位会错位导致 layer 不符。
func TestDecodeLightBaseline(t *testing.T) {
	w := writeEntityPrefix(testsupport.NewBitWriter(), 0x60)
	writeCommonHead(w, 201, 2)
	writeLightBase(w)
	writeLightPhotometric(w) // 位流里"多出"的子段：非光度路径必须整体跳过
	dataEndBit := uint64((len(w.Data)-1)*8 + int(w.Bit))
	w.H(5, 0)
	w.H(5, 301)

	l := decodeLightFromBits(t, w, dataEndBit, false, container.VerR2004)
	if l.isPhotometric {
		t.Fatal("非光度上下文 isPhotometric 应为 false")
	}
	if l.classVersion != 2 || l.name != "L1" {
		t.Errorf("基线字段: ver=%d name=%q", l.classVersion, l.name)
	}
	if l.layer != 301 {
		t.Errorf("layer=%d want 301（基线路径不应消费光度位）", l.layer)
	}
}
