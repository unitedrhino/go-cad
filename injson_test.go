// injson_test.go 验证 ParseJSON（LibreDWG dwgread -O JSON → Document）的
// 逆向解析质量：九样本 gold JSON 与同源 DWG 经 Parse 解析殊途同归——
// 实体数量、几何/公共字段（entityField 导出键值）、文本提取一致，
// 且消费侧（RenderPNG/Texts）可用、WriteDwg 对 JSON 来源文档经结构化
// 正向编码层（encode_forward.go）写出并可回读闭环。坏输入走优雅降级。
package cad

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// injsonSample roundtrip 样本：alias 为 gold JSON（/tmp/<alias>.json），
// sample 为同源 DWG 相对路径（与值级审计同一批九样本）。
type injsonSample struct {
	alias  string
	sample string
}

// injsonSamples 九样本矩阵（exr13~sample2018）。
var injsonSamples = []injsonSample{
	{"exr13", "example_r13.dwg"},
	{"exr14", "example_r14.dwg"},
	{"ex2000", "example_2000.dwg"},
	{"ex2004", "example_2004.dwg"},
	{"ex2007", "example_2007.dwg"},
	{"ex2010", "example_2010.dwg"},
	{"ex2013", "example_2013.dwg"},
	{"ex2018", "example_2018.dwg"},
	{"sample2000", "sample_2000.dwg"},
	{"sample2018", "sample_2018.dwg"},
}

// injsonGeomKeys 实体类型 → roundtrip 对照的 per-type 键集（entityField
// 的 gold 键名；覆盖几何与关键专有字段，长尾类的标量基键抽样）。
var injsonGeomKeys = map[string][]string{
	"LINE":              {"start", "end", "thickness", "extrusion"},
	"CIRCLE":            {"center", "radius", "thickness"},
	"ARC":               {"center", "radius", "start_angle", "end_angle"},
	"POINT":             {"location", "x_ang"},
	"ELLIPSE":           {"center", "major_axis", "axis_ratio", "start_angle", "end_angle"},
	"LWPOLYLINE":        {"flag", "vertices", "bulges", "elevation", "const_width"},
	"TEXT":              {"text_value", "ins_pt", "height", "rotation", "horiz_alignment", "vert_alignment", "generation"},
	"MTEXT":             {"ins_pt", "x_axis_dir", "rect_width", "text_height", "attachment", "text"},
	"INSERT":            {"ins_pt", "scale", "rotation", "has_attribs"},
	"ATTRIB":            {"text_value", "tag", "ins_pt", "height", "rotation", "horiz_alignment", "vert_alignment"},
	"SOLID":             {"corner1", "corner2", "corner3", "corner4", "thickness", "elevation"},
	"3DFACE":            {"corner1", "corner2", "corner3", "corner4", "invis_flags"},
	"VERTEX_2D":         {"point", "bulge", "flag", "tangent_dir"},
	"VERTEX_3D":         {"point", "flag"},
	"VERTEX_PFACE":      {"point", "flag"},
	"POLYLINE_2D":       {"flag", "curve_type", "start_width", "end_width", "thickness", "elevation"},
	"POLYLINE_3D":       {"flag", "curve_type"},
	"POLYLINE_PFACE":    {"numverts", "numfaces"},
	"VERTEX_PFACE_FACE": {"flag"},
	"RAY":               {"point", "vector"},
	"XLINE":             {"point", "vector"},
	// SPLINE 主体键（scenario/degree/fit_pts 等）entityField 未导出
	// （值级审计同口径），roundtrip 仅对照公共键
	// MLINE 主体键（批次 B：base_point/extrusion 建模导出；verts[i].lines[j]
	// 样式线段参数在测试循环按 gold 展平键动态追加）
	"MLINE":     {"scale", "justification", "flags", "base_point", "extrusion"},
	"HATCH":     {"elevation", "name", "is_solid_fill", "is_associative", "style", "pattern_type", "angle", "scale_spacing"},
	"REGION":    {"acis_empty", "version", "wireframe_data_present", "point_present", "isolines", "isoline_present"},
	"3DSOLID":   {"acis_empty", "version", "wireframe_data_present", "point_present", "isolines", "isoline_present"},
	"BODY":      {"acis_empty", "version", "wireframe_data_present", "point_present", "isolines", "isoline_present"},
	"VIEWPORT":  {"width", "height", "VIEWTWIST", "VIEWSIZE", "LENSLENGTH", "FRONTZ", "BACKZ", "SNAPANG", "circle_zoom", "status_flag"},
	"TOLERANCE": {"text_value", "height", "dimgap"},
	// WIPEOUT/IMAGE 的 pt0/uvec/vvec/image_size 为数组键，entityField 按
	// gold 展平口径不导出（值级审计同口径）
	"WIPEOUT":                {"class_version", "clipping", "brightness", "contrast", "fade", "clip_boundary_type"},
	"IMAGE":                  {"class_version", "clipping", "brightness", "contrast", "fade", "clip_boundary_type"},
	"LEADER":                 {"annotation_type", "path_type"},
	"DIMENSION_ORDINATE":     {"elevation", "flag", "flag1", "flag2", "user_text", "text_rotation", "horiz_dir", "ins_rotation"},
	"DIMENSION_LINEAR":       {"elevation", "flag", "flag1", "user_text", "text_rotation", "horiz_dir", "ins_rotation", "oblique_angle", "dim_rotation"},
	"DIMENSION_ALIGNED":      {"elevation", "flag", "flag1", "user_text", "text_rotation", "horiz_dir", "ins_rotation", "oblique_angle"},
	"DIMENSION_ANG3PT":       {"elevation", "flag", "flag1", "user_text", "text_rotation"},
	"DIMENSION_ANG2LN":       {"elevation", "flag", "flag1", "user_text", "text_rotation"},
	"DIMENSION_RADIUS":       {"elevation", "flag", "flag1", "user_text"},
	"DIMENSION_DIAMETER":     {"elevation", "flag", "flag1", "user_text"},
	"ARC_DIMENSION":          {"elevation", "flag", "flag1", "user_text", "is_partial", "arc_start_param", "arc_end_param"},
	"LARGE_RADIAL_DIMENSION": {"elevation", "flag", "flag1", "user_text"},
	"LIGHT":                  {"class_version", "name", "status", "plot_glyph", "intensity", "attenuation_type", "attenuation_start_limit", "attenuation_end_limit", "hotspot_angle", "falloff_angle", "cast_shadows", "shadow_type"},
	"MULTILEADER":            {"flags", "has_landing", "has_dogleg", "landing_dist", "arrow_size", "style_content", "text_angletype", "text_alignment", "has_text_frame", "style_attachment", "is_annotative", "justification", "scale_factor"},
	"SHAPE":                  {"ins_pt", "scale", "rotation", "width_factor", "oblique_angle", "thickness"},
	"OLE2FRAME":              {"type", "mode", "lock_aspect"},
	"OLEFRAME":               {"flag", "mode"},
	"PROXY_ENTITY":           {"proxy_id", "version", "from_dxf", "data_numbits", "num_objids", "proxy_data_size"},
}

// injsonCommonKeys 全实体公共键（gold 标量，跳过 bitsize/size/handle 数组）。
var injsonCommonKeys = []string{"entmode", "color", "ltype_scale", "invisible", "linewt", "nolinks", "isbylayerlt"}

// TestParseJSONRoundtripNineSamples 九样本 roundtrip：gold JSON →
// ParseJSON → Document，与 Parse(同源 DWG) 逐实体逐键对照（json vs gold
// 自洽 + json vs dwg 殊途同归），并验证 Texts/RenderPNG/WriteDwg 消费侧。
func TestParseJSONRoundtripNineSamples(t *testing.T) {
	dir := os.Getenv("CAD_LIBREDWG_DATA")
	if dir == "" {
		dir = "/tmp/libredwg/test/test-data"
	}
	for _, s := range injsonSamples {
		raw, err := os.ReadFile("/tmp/" + s.alias + ".json")
		if err != nil {
			t.Logf("%s: gold 不可用，跳过", s.alias)
			continue
		}
		dwg, err := os.ReadFile(filepath.Join(dir, s.sample))
		if err != nil {
			t.Logf("%s: 样本不可用，跳过", s.alias)
			continue
		}
		jdoc, err := ParseJSON(raw)
		if err != nil {
			t.Errorf("%s: ParseJSON 失败: %v", s.alias, err)
			continue
		}
		ddoc, err := Parse(dwg)
		if err != nil {
			t.Errorf("%s: DWG 解析失败: %v", s.alias, err)
			continue
		}

		// gold 结构（复用值级审计的展平与句柄提取口径）
		var gold struct {
			Objects    []map[string]any `json:"OBJECTS"`
			Header     map[string]any   `json:"HEADER"`
			FileHeader map[string]any   `json:"FILEHEADER"`
		}
		if err := json.Unmarshal(raw, &gold); err != nil {
			t.Errorf("%s: gold 解析失败: %v", s.alias, err)
			continue
		}

		// 1. 版本：HEADER.$ACADVER 优先，回退 FILEHEADER.version（gold 实际位置）
		wantVer := ""
		if v, ok := gold.Header["$ACADVER"].(string); ok {
			wantVer = v
		} else if v, ok := gold.FileHeader["version"].(string); ok {
			wantVer = v
		}
		if wantVer != "" && jdoc.Version() != wantVer {
			t.Errorf("%s: 版本不一致 json=%s gold=%s", s.alias, jdoc.Version(), wantVer)
		}

		// 2. 实体数：JSON 侧 = gold 实体条目数（排除 ATTDEF；ParseJSON 与
		// Parse 的 R2004+ 路径同口径跳过定义类标记）
		goldEnts := 0
		for _, o := range gold.Objects {
			if ename, _ := o["entity"].(string); ename != "" && ename != "ATTDEF" {
				goldEnts++
			}
		}
		if got := len(jdoc.entityByHandle); got != goldEnts {
			t.Errorf("%s: 实体数不一致 json=%d gold(非ATTDEF)=%d", s.alias, got, goldEnts)
		}
		if jdoc.Skipped() != 0 {
			t.Errorf("%s: ParseJSON 跳过 %d 个对象（九样本应零跳过）", s.alias, jdoc.Skipped())
		}

		// 3. 逐实体逐键对照
		var compared, dwgOnly, diff int
		for _, o := range gold.Objects {
			ename, _ := o["entity"].(string)
			if ename == "" || ename == "ATTDEF" {
				continue
			}
			h := jsonTestHandle(o["handle"])
			je := jdoc.entityByHandle[h]
			if je == nil {
				t.Errorf("%s: JSON 侧缺实体 h=%d (%s)", s.alias, h, ename)
				continue
			}
			flat := map[string]any{}
			flattenJSONGold("", o, &flat)
			de := ddoc.entityByHandle[h]
			if de == nil {
				dwgOnly++
			}
			keys := append([]string{}, injsonCommonKeys...)
			keys = append(keys, injsonGeomKeys[ename]...)
			if ename == "MLINE" {
				// MLINE 样式线段参数：gold 展平键 verts[i].vertex 等
				// 动态加入对照（segparms/areafillparms 按线计数切片）
				for k := range flat {
					if strings.HasPrefix(k, "verts[") {
						keys = append(keys, k)
					}
				}
			}
			if ename == "MULTILEADER" {
				// MULTILEADER ctx 全结构（批次 B）：gold 展平标量键动态
				// 加入对照（ctx.* 与 blocklabels[i].*/arrowheads[i].*、
				// 顶层 CMC 双形态键）；数组/句柄键仍按值级审计口径跳过
				for k, v := range flat {
					if !strings.HasPrefix(k, "ctx") && !strings.HasPrefix(k, "blocklabels") &&
						!strings.HasPrefix(k, "arrowheads") && !strings.HasPrefix(k, "line_color") &&
						!strings.HasPrefix(k, "text_color") && !strings.HasPrefix(k, "block_color") {
						continue
					}
					switch v.(type) {
					case float64, string, bool:
						keys = append(keys, k)
					}
				}
			}
			for _, k := range keys {
				gv, ok := flat[k]
				if !ok {
					continue // gold 未输出该键（可选字段）
				}
				jv := entityField(je, k)
				if jv == nil {
					// gold 键名与 entityField 导出键名的已知别名
					// （审计侧数组键不对照故未覆盖的同义键）
					if alt := injsonKeyAliases[k]; alt != "" {
						jv = entityField(je, alt)
					}
				}
				if !jsonTestValueMatch(jv, gv) {
					diff++
					if diff <= 5 {
						t.Errorf("%s: h=%d %s 键 %s: json=%v gold=%v", s.alias, h, ename, k, jv, gv)
					}
					continue
				}
				if de == nil {
					continue
				}
				// 殊途同归：两侧实体都存在时键值必须一致（json vs dwg）
				dv := entityField(de, k)
				if dv == nil {
					if alt := injsonKeyAliases[k]; alt != "" {
						dv = entityField(de, alt)
					}
					if dv == nil {
						continue // DWG 侧未导出该键（解码缺口），已由 json vs gold 覆盖
					}
				}
				// gold 历史噪声豁免（与值级审计同清单：如 ex2013 REGION 的
				// AcDs blob 场景 acis_empty 误读），不计入差异
				if isGoldNoise(s.alias, ename, h, k) {
					continue
				}
				if !entityValueEqual(dv, jv) && !entityValueEqual(jv, dv) && !jsonTestValueMatch(jv, dv) {
					diff++
					if diff <= 5 {
						t.Errorf("%s: h=%d %s 键 %s: json=%v dwg=%v（与 gold=%v）", s.alias, h, ename, k, jv, dv, gv)
					}
				}
			}
			compared++
			// 3.5 ACIS 数据段（批次 B）：gold 携带 acis_data（acis_empty=0）
			// 时，JSON 侧须还原出非空 acisData，且与 DWG 侧解混淆文本
			// 归一化后一致——gold 行数组不保留行尾 \r 与末尾换行
			// （out_json json_3dsolid 分割语义，R13 样本行尾 \r\n、R14+
			// 尾 \n，均属 JSON 序列化信息损失），对照按剥 \r+裁尾 \n 归一；
			// DWG 侧 acis_empty=1（AcDs blob 场景，gold 历史噪声清单同源）
			// 无文本可比，仅验证 JSON 侧还原完整性。
			if ja, ok := je.(*entAcis); ok && !ja.acisEmpty {
				if _, has := o["acis_data"]; has && len(ja.acisData) == 0 {
					t.Errorf("%s: h=%d %s JSON 侧 acis_data 未还原", s.alias, h, ename)
				}
				if de != nil {
					if da, ok := de.(*entAcis); ok && !da.acisEmpty {
						norm := func(b []byte) string {
							return strings.TrimRight(strings.ReplaceAll(string(b), "\r", ""), "\n")
						}
						if norm(da.acisData) != norm(ja.acisData) {
							t.Errorf("%s: h=%d %s acisData 归一化后不一致 json=%d 字节 dwg=%d 字节",
								s.alias, h, ename, len(ja.acisData), len(da.acisData))
						}
					}
				}
			}
			// 3.6 HATCH/MPOLYGON 图案定义线（批次 B）：gold deflines 数组
			// 逐条还原（angle/pt0/offset/dashes），与 DWG 侧同构一致
			var jDeflines []hatchDefLine
			switch t := je.(type) {
			case *entHatch:
				jDeflines = t.deflines
			case *entMpolygon:
				jDeflines = t.hatch.deflines
			}
			if jDeflines != nil || o["deflines"] != nil {
				var dDeflines []hatchDefLine
				switch t := de.(type) {
				case *entHatch:
					dDeflines = t.deflines
				case *entMpolygon:
					dDeflines = t.hatch.deflines
				}
				if len(dDeflines) != len(jDeflines) {
					t.Errorf("%s: h=%d %s deflines 数不一致 json=%d dwg=%d",
						s.alias, h, ename, len(jDeflines), len(dDeflines))
				} else {
					for i := range jDeflines {
						if !hatchDeflineEqual(jDeflines[i], dDeflines[i]) {
							t.Errorf("%s: h=%d %s deflines[%d] 不一致 json=%+v dwg=%+v",
								s.alias, h, ename, i, jDeflines[i], dDeflines[i])
						}
					}
				}
				// gold 条目逐条对照（angle/dashes 值级 + pt0/offset 数组）
				gdls, _ := o["deflines"].([]any)
				if len(gdls) != len(jDeflines) {
					t.Errorf("%s: h=%d %s deflines 数与 gold 不一致 json=%d gold=%d",
						s.alias, h, ename, len(jDeflines), len(gdls))
				}
				for i, ge := range gdls {
					if i >= len(jDeflines) {
						break
					}
					gm, _ := ge.(map[string]any)
					if gm == nil {
						continue
					}
					dl := jDeflines[i]
					if gf, _ := gm["angle"].(float64); !nearF(dl.angle, gf) {
						t.Errorf("%s: h=%d %s deflines[%d].angle json=%v gold=%v",
							s.alias, h, ename, i, dl.angle, gf)
					}
					if pa, ok := gm["pt0"].([]any); ok {
						if !nearF(dl.pt0.x, jsonNumAt(pa, 0)) || !nearF(dl.pt0.y, jsonNumAt(pa, 1)) {
							t.Errorf("%s: h=%d %s deflines[%d].pt0 json=(%v,%v) gold=%v",
								s.alias, h, ename, i, dl.pt0.x, dl.pt0.y, pa)
						}
					}
					if pa, ok := gm["offset"].([]any); ok {
						if !nearF(dl.offset.x, jsonNumAt(pa, 0)) || !nearF(dl.offset.y, jsonNumAt(pa, 1)) {
							t.Errorf("%s: h=%d %s deflines[%d].offset json=(%v,%v) gold=%v",
								s.alias, h, ename, i, dl.offset.x, dl.offset.y, pa)
						}
					}
					if da, ok := gm["dashes"].([]any); ok {
						if len(da) != len(dl.dashes) {
							t.Errorf("%s: h=%d %s deflines[%d].dashes 数 json=%d gold=%d",
								s.alias, h, ename, i, len(dl.dashes), len(da))
						}
						for j, ge2 := range da {
							if j >= len(dl.dashes) {
								break
							}
							gf, _ := ge2.(float64)
							if !nearF(dl.dashes[j], gf) {
								t.Errorf("%s: h=%d %s deflines[%d].dashes[%d] json=%v gold=%v",
									s.alias, h, ename, i, j, dl.dashes[j], gf)
							}
						}
					}
				}
			}
		}

		// 4. Texts：JSON 侧须覆盖 gold 模型空间直出文本（TEXT/MTEXT，entmode=2）
		type txtKey struct {
			text string
			x, y float64
		}
		jtexts := map[txtKey]bool{}
		for _, ti := range jdoc.Texts() {
			jtexts[txtKey{ti.Text, ti.X, ti.Y}] = true
		}
		var goldTexts int
		for _, o := range gold.Objects {
			ename, _ := o["entity"].(string)
			if ename != "TEXT" && ename != "MTEXT" {
				continue
			}
			if em, _ := o["entmode"].(float64); em != 2 {
				continue // 块内文本的可见性经 INSERT 展开，不在此硬断言
			}
			flat := map[string]any{}
			flattenJSONGold("", o, &flat)
			raw, isStr := flat["text_value"]
			if !isStr {
				raw, isStr = flat["text"]
			}
			text, _ := raw.(string)
			if !isStr {
				continue
			}
			if ename == "MTEXT" {
				text = stripMTextFormat(text)
			}
			ins, _ := flat["ins_pt"].([]any)
			if len(ins) < 2 {
				continue
			}
			key := txtKey{text, ins[0].(float64), ins[1].(float64)}
			if !jtexts[key] {
				t.Errorf("%s: JSON Texts 缺 gold 模型空间文本 %q @(%v,%v)", s.alias, key.text, key.x, key.y)
				continue
			}
			goldTexts++
		}
		t.Logf("%s: 对照实体 %d（DWG 侧缺 %d），文本覆盖 %d，差异 %d", s.alias, compared, dwgOnly, goldTexts, diff)
	}
}

// TestParseJSONConsumers 消费侧验证：RenderPNG 出图、Texts 提取、
// modelSpaceEntities 完整；WriteDwg 对 JSON 来源文档返回明确错误
// （回放素材只存在于 Parse 的容器解析路径）。
func TestParseJSONConsumers(t *testing.T) {
	raw, err := os.ReadFile("/tmp/exr13.json")
	if err != nil {
		t.Skip("exr13 gold 不可用")
	}
	doc, err := ParseJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if doc.EntityCount() == 0 {
		t.Fatal("模型空间实体为空")
	}
	if len(doc.Texts()) == 0 {
		t.Fatal("文本提取为空")
	}
	png, err := RenderPNG(doc, RenderOptions{Width: 640})
	if err != nil {
		t.Fatalf("RenderPNG 失败: %v", err)
	}
	if len(png) == 0 || !bytes.HasPrefix(png, []byte{0x89, 'P', 'N', 'G'}) {
		t.Fatalf("RenderPNG 输出非法（%d 字节）", len(png))
	}
	// 写出闭环（LIM-D 正向编码层）：JSON 来源文档经结构化正向路径写出
	// R2000 容器，产物可被 Parse 回读；正向编码器覆盖渲染同源主力类，
	// 无编码器实体（动态类等）不写出，故回读数 ≤ 原数且非空
	var wbuf bytes.Buffer
	if werr := WriteDwg(doc, &wbuf); werr != nil {
		t.Fatalf("WriteDwg 对 JSON 来源文档应走正向路径成功: %v", werr)
	}
	back, err := Parse(wbuf.Bytes())
	if err != nil {
		t.Fatalf("JSON 正向写出产物回读失败: %v", err)
	}
	if n := back.EntityCount(); n == 0 || n > doc.EntityCount() {
		t.Errorf("JSON 正向写出实体数越界: %d（原 %d）", n, doc.EntityCount())
	}
	// 图层颜色：LAYER 对象 → layerColors
	if len(doc.layerColors) == 0 {
		t.Fatal("LAYER 对象未映射到 layerColors")
	}
}

// TestParseJSONBadInput 坏输入优雅处理：非 JSON、缺 OBJECTS、未知实体类
// （跳过计数不阻断）、非对象条目。
func TestParseJSONBadInput(t *testing.T) {
	if _, err := ParseJSON([]byte("not json at all")); err == nil {
		t.Error("非法 JSON 应返回错误")
	}
	if _, err := ParseJSON([]byte(`{"HEADER": {}, "FILEHEADER": {"version": "AC1015"}}`)); err == nil {
		t.Error("缺 OBJECTS 数组应返回错误")
	}
	// 未知实体类：跳过计数，已知实体照常入文档
	doc, err := ParseJSON([]byte(`{
		"FILEHEADER": {"version": "AC1015"},
		"OBJECTS": [
			{"entity": "FUTURE_THING", "handle": [0, 1, 9], "type": 999},
			{"entity": "LINE", "handle": [0, 1, 10], "type": 19, "entmode": 2,
			 "start": [0, 0, 0], "end": [10, 10, 0]},
			"junk-entry",
			{"object": "NOT_A_KNOWN_OBJECT", "handle": [0, 1, 11]}
		]
	}`))
	if err != nil {
		t.Fatalf("含未知实体类的合法 JSON 不应报错: %v", err)
	}
	if doc.Skipped() != 2 { // 未知实体类 + junk 条目
		t.Errorf("skipped=%d, 期望 2（未知实体类 + 非对象条目）", doc.Skipped())
	}
	if doc.entityByHandle[10] == nil {
		t.Error("已知实体（LINE）应正常入文档")
	}
	if doc.internalObjects[11] == nil {
		t.Error("未知对象名应以 objGeneric 兜底入 internalObjects")
	}
	if l, ok := doc.entityByHandle[10].(*entLine); !ok || l.end.x != 10 {
		t.Errorf("LINE 几何还原错误: %#v", l)
	}
}

// TestParseJSONLayers 图层映射：LAYER 对象 color（标量与 CMC 对象双形态）
// → layerColors，供渲染的图层颜色继承。
func TestParseJSONLayers(t *testing.T) {
	doc, err := ParseJSON([]byte(`{
		"FILEHEADER": {"version": "AC1015"},
		"OBJECTS": [
			{"object": "LAYER", "handle": [0, 1, 16], "name": "0", "color": 7},
			{"object": "LAYER", "handle": [0, 1, 17], "name": "true", "color": {"index": 5, "rgb": "00ff00", "flag": 128}}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.layerColors) != 2 {
		t.Fatalf("layerColors=%d, 期望 2", len(doc.layerColors))
	}
	if lc := doc.layerColors[16]; lc.index != 7 || lc.hasTrue {
		t.Errorf("标量 color 还原错误: %+v", lc)
	}
	if lc := doc.layerColors[17]; !lc.hasTrue || lc.trueColor != 0x00ff00 {
		t.Errorf("CMC color 还原错误: %+v", lc)
	}
}

// hatchDeflineEqual 图案定义线全字段等价（浮点容差 + dashes 逐元素）。
func hatchDeflineEqual(a, b hatchDefLine) bool {
	if !nearF(a.angle, b.angle) || !nearF(a.pt0.x, b.pt0.x) || !nearF(a.pt0.y, b.pt0.y) ||
		!nearF(a.offset.x, b.offset.x) || !nearF(a.offset.y, b.offset.y) ||
		len(a.dashes) != len(b.dashes) {
		return false
	}
	for i := range a.dashes {
		if !nearF(a.dashes[i], b.dashes[i]) {
			return false
		}
	}
	return true
}

// jsonTestHandle gold 句柄数组末位 → 绝对句柄（与 jsonHandleValue 同口径；
// 测试侧用于直接从 map[string]any 取值）。
func jsonTestHandle(v any) uint64 {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return 0
	}
	f, _ := arr[len(arr)-1].(float64)
	return uint64(f)
}

// injsonKeyAliases gold 键名 → entityField 导出键名的别名表（同义几何键；
// 值级审计的展平口径丢弃数字数组故从未对照这些键，roundtrip 补齐）。
var injsonKeyAliases = map[string]string{
	"point":   "position",   // VERTEX_2D/VERTEX_3D 的 gold point 键
	"vector":  "direction",  // RAY/XLINE 的 gold vector 键
	"ins_pt":  "insertion",  // TEXT 的 gold ins_pt 键（MTEXT/INSERT 本就双键）
	"sm_axis": "major_axis", // ELLIPSE 的 gold 主轴键（entityField 本就双键，防御）
}

// flattenJSONGold 展开 gold 条目为扁平键值。与值级审计的 flattenGold 同构，
// 区别：数字/字符串数组（LINE.start、LWPOLYLINE.points 等）整体保留为值
// ——roundtrip 需对照几何数组键，而审计侧此类键本就按非标量跳过。
func flattenJSONGold(prefix string, m map[string]any, out *map[string]any) {
	for k, v := range m {
		switch vv := v.(type) {
		case map[string]any:
			flattenJSONGold(prefix+k+".", vv, out)
		case []any:
			if len(vv) > 0 {
				if _, isObj := vv[0].(map[string]any); !isObj {
					(*out)[prefix+k] = vv // 数字/字符串数组整体保留
					continue
				}
			}
			for i, e := range vv {
				if em, ok := e.(map[string]any); ok {
					flattenJSONGold(fmt.Sprintf("%s%s[%d].", prefix, k, i), em, out)
				}
			}
		default:
			(*out)[prefix+k] = v
		}
	}
}

// jsonTestValueMatch gold 值与 entityField 导出值等价判定：JSON 侧数值
// 统一 float64、数组为 []any，导出侧为 int64/float64/[]float64/string。
func jsonTestValueMatch(got, want any) bool {
	if got == nil {
		return false
	}
	switch w := want.(type) {
	case float64:
		switch g := got.(type) {
		case float64:
			return nearF(g, w)
		case int64:
			return nearF(float64(g), w)
		case uint16:
			return nearF(float64(g), w)
		}
	case string:
		g, ok := got.(string)
		return ok && g == w
	case bool:
		if g, ok := got.(bool); ok {
			return g == w
		}
		if g, ok := got.(int64); ok {
			return (w && g == 1) || (!w && g == 0)
		}
	case []any:
		g, ok := got.([]float64)
		if !ok {
			if f, ok2 := got.(float64); ok2 && len(w) == 1 {
				if wf, ok3 := w[0].(float64); ok3 {
					return nearF(f, wf)
				}
			}
			return false
		}
		// gold 二维嵌套数组（LWPOLYLINE points=[[x,y],...]）展开后与导出侧
		// 的展平 []float64（pt2Arr 口径）逐元素比较
		var flat []float64
		for _, e := range w {
			switch ev := e.(type) {
			case float64:
				flat = append(flat, ev)
			case []any:
				for _, e2 := range ev {
					if f2, ok := e2.(float64); ok {
						flat = append(flat, f2)
					}
				}
			default:
				return false
			}
		}
		if len(g) != len(flat) {
			// gold 的平面点为 2 元（z 缺省 0），导出侧 point3Arr 恒 3 元：
			// 短的一方按 0 补齐后逐元素比较
			n := len(g)
			if len(flat) > n {
				n = len(flat)
			}
			pad := func(s []float64) []float64 {
				out := make([]float64, n)
				copy(out, s)
				return out
			}
			g, flat = pad(g), pad(flat)
		}
		for i := range flat {
			if !nearF(g[i], flat[i]) {
				return false
			}
		}
		return true
	}
	return false
}

// ---- 批次 R：HEADER 变量消费 ----

// TestParseJSONHeaderVars HEADER 段消费：全量键入 HeaderVars、渲染相关键
// 提升结构化字段、HeaderVar 的 $ 前缀容错与缺键返回。
func TestParseJSONHeaderVars(t *testing.T) {
	goldPath := testsupport.LibredwgGoldJSONPath("2018")
	raw, err := os.ReadFile(goldPath)
	if err != nil {
		t.Skipf("gold JSON 缺失: %v", err)
	}
	doc, err := ParseJSON(raw)
	if err != nil {
		t.Fatalf("ParseJSON 失败: %v", err)
	}
	if len(doc.HeaderVars) < 200 {
		t.Errorf("HeaderVars 键数异常: %d（期望 gold 全量 ≥200）", len(doc.HeaderVars))
	}
	// gold 的 EXTMIN/EXTMAX 与结构化字段一致
	want, ok := doc.HeaderVars["EXTMIN"].([]any)
	if !ok || len(want) < 2 {
		t.Fatalf("HeaderVars 缺 EXTMIN")
	}
	if doc.extMin.x != want[0].(float64) || doc.extMin.y != want[1].(float64) {
		t.Errorf("extMin 与 EXTMIN 不一致: %v vs %v", doc.extMin, want)
	}
	maxArr := doc.HeaderVars["EXTMAX"].([]any)
	if doc.extMax.x != maxArr[0].(float64) || doc.extMax.y != maxArr[1].(float64) {
		t.Errorf("extMax 与 EXTMAX 不一致: %v vs %v", doc.extMax, maxArr)
	}
	// INSBASE/LTSCALE 提升与 $ 前缀容错查询
	if v, ok := doc.HeaderVar("$INSBASE"); !ok {
		t.Errorf("HeaderVar($INSBASE) 未命中")
	} else if arr := v.([]any); doc.insbase.x != arr[0].(float64) {
		t.Errorf("insbase 与 INSBASE 不一致: %v vs %v", doc.insbase, arr)
	}
	if v, ok := doc.HeaderVar("LTSCALE"); !ok || v.(float64) != doc.ltscale {
		t.Errorf("LTSCALE 提升不符: %v %v", v, doc.ltscale)
	}
	if doc.ltscale != 1 {
		t.Errorf("gold LTSCALE 应为 1: %v", doc.ltscale)
	}
	// 缺键与未知键（ex 系 gold 的 ACADVER 在 FILEHEADER.version，HEADER
	// 段无该键，HeaderVar 应容错返回未命中）
	if _, ok := doc.HeaderVar("$NO_SUCH_KEY"); ok {
		t.Errorf("未知键不应命中")
	}
	if _, ok := doc.HeaderVar("$ACADVER"); ok {
		t.Errorf("gold HEADER 无 ACADVER 键，不应命中")
	}
	if doc.Version() != "AC1032" {
		t.Errorf("版本回退 FILEHEADER 失效: %s", doc.Version())
	}
}

// TestParseJSONHeaderVarsEmpty 无 HEADER 段的 JSON：HeaderVars 空但解析
// 不失败（向后兼容）。
func TestParseJSONHeaderVarsEmpty(t *testing.T) {
	doc, err := ParseJSON([]byte(`{"OBJECTS":[]}`))
	if err != nil {
		t.Fatalf("ParseJSON 失败: %v", err)
	}
	if len(doc.HeaderVars) != 0 {
		t.Errorf("HeaderVars 应为空: %d", len(doc.HeaderVars))
	}
	if _, ok := doc.HeaderVar("$EXTMIN"); ok {
		t.Errorf("空文档不应命中 EXTMIN")
	}
	if doc.ltscale != 1 {
		t.Errorf("缺省 ltscale 应为 1: %v", doc.ltscale)
	}
}
