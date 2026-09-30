// 本文件为批次 D 内部对象类长尾（SORTENTSTABLE/IMAGEDEF/
// IMAGEDEF_REACTOR/*_DEFINITION/UNDERLAY 系）的独立对照测试：以
// LibreDWG dwgread -O JSON 现场 gold 逐对象逐字段比对。gold 约定
// /tmp/d_<名>.json（CAD_LONGTAIL_GOLD 可覆盖目录），缺失即跳过
// （与 objects_internal_json_test.go 的现场 gold 约定一致；
// 生成命令见各类测试注释）。
package cad

import (
	"encoding/json"
	"fmt"
	"github.com/unitedrhino/go-cad/internal/entity"
	"github.com/unitedrhino/go-cad/internal/object"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// longtailGoldDir gold JSON 目录。
func longtailGoldDir() string {
	if d := os.Getenv("CAD_LONGTAIL_GOLD"); d != "" {
		return d
	}
	return "/tmp"
}

// loadLongtailGold 读取单个 gold 文件（缺失返回 false，测试跳过）。
func loadLongtailGold(t *testing.T, name string) ([]map[string]any, bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(longtailGoldDir(), name))
	if err != nil {
		t.Skipf("gold %s 不可用（现场生成：dwgread -O JSON -o /tmp/d_%s.json <样本>）", name, trimJSONExt(name))
		return nil, false
	}
	var gold struct {
		Objects []map[string]any `json:"OBJECTS"`
	}
	// LibreDWG JSON 对 NaN/Inf 输出 -nan/nan/inf（非法 JSON，上游缺陷，
	// gh44-error.dwg 的 RAPIDRTRENDERSETTINGS 实证）；与本批对照类无关，
	// 归一为 0 以便整文件加载
	body := string(raw)
	for _, tok := range []string{"-nan", "nan", "-inf", "inf"} {
		body = strings.ReplaceAll(body, ": "+tok, ": 0")
	}
	if err := json.Unmarshal([]byte(body), &gold); err != nil {
		t.Fatalf("gold %s 解析失败: %v", name, err)
	}
	return gold.Objects, true
}

// trimJSONExt 去掉 .json 后缀。
func trimJSONExt(name string) string {
	if len(name) > 5 && name[len(name)-5:] == ".json" {
		return name[:len(name)-5]
	}
	return name
}

// goldHandleRef gold 句柄数组 [code, size, ..., value, absolute_ref] 的末位。
func goldHandleRef(v any) (uint64, bool) {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return 0, false
	}
	f, ok := arr[len(arr)-1].(float64)
	if !ok {
		return 0, false
	}
	return uint64(f), true
}

// checkNumField 断言 objGeneric 标量字段与 gold 数值一致（不一致记入 bad；
// bool 字段按 0/1 归一，与审计框架 auditValueMatch 同规则）。
func checkNumField(bad *string, g *object.ObjGeneric, key string, want float64) {
	switch got := g.Field(key).(type) {
	case int64:
		if math.Abs(float64(got)-want) >= 1e-6 {
			*bad += fmt.Sprintf(" %s=%d(want %v)", key, got, want)
		}
	case bool:
		if got != (want != 0) {
			*bad += fmt.Sprintf(" %s=%v(want %v)", key, got, want)
		}
	default:
		*bad += fmt.Sprintf(" %s=缺失(want %v)", key, want)
	}
}

// checkHandleField 断言 objGeneric 单句柄字段与 gold 句柄数组末位一致。
func checkHandleField(bad *string, g *object.ObjGeneric, key string, goldVal any) {
	want, ok := goldHandleRef(goldVal)
	if !ok {
		return
	}
	got, ok := g.Field(key).(int64)
	if !ok {
		*bad += fmt.Sprintf(" %s=缺失(want %d)", key, want)
		return
	}
	if uint64(got) != want {
		*bad += fmt.Sprintf(" %s=%d(want %d)", key, got, want)
	}
}

// checkHandleVector 断言 objGeneric 展开键（key[i]）句柄向量与 gold 数组逐项一致。
func checkHandleVector(bad *string, g *object.ObjGeneric, key string, goldArr []any) {
	for i, gv := range goldArr {
		want, ok := goldHandleRef(gv)
		if !ok {
			continue
		}
		got, ok := g.Field(fmt.Sprintf("%s[%d]", key, i)).(int64)
		if !ok {
			*bad += fmt.Sprintf(" %s[%d]=缺失(want %d)", key, i, want)
			continue
		}
		if uint64(got) != want {
			*bad += fmt.Sprintf(" %s[%d]=%d(want %d)", key, i, got, want)
		}
	}
}

// TestSORTENTSTABLEGold 对照 SORTENTSTABLE 与 dwgread gold。
// gold 生成（样本同目录）：
//
//	dwgread -O JSON -o /tmp/d_<名>.json <样本>
//
// 样本集覆盖 R2000/R2004/R2007/R2010/R2013/R2018 共 16+ 实例
// （含 R2010+ 派生 bitsize 布局与 R2013+ has_ds_data 位）。
func TestSORTENTSTABLEGold(t *testing.T) {
	dir := os.Getenv("CAD_LIBREDWG_DATA")
	if dir == "" {
		dir = "/tmp/libredwg/test/test-data"
	}
	cases := []struct{ sample, gold string }{
		{"2000/PolyLine2D.dwg", "d_PolyLine2D.json"},
		{"2000/TS1.dwg", "d_TS1.json"},
		{"2004/HatchG.dwg", "d_HatchG.json"},
		{"2004/Surface.dwg", "d_Surface.json"},
		{"2007/ATMOS-DC22S.dwg", "d_ATMOS-DC22S.json"},
		{"2010/gh209_1.dwg", "d_gh209_1.json"},
		{"2013/gh109_1.dwg", "d_gh109_1.json"},
		{"2013/gh44-error.dwg", "d_gh44-error.json"},
		{"2018/Dynblocks.dwg", "d_Dynblocks.json"},
	}
	total := 0
	for _, c := range cases {
		c := c
		t.Run(c.sample, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, c.sample))
			if err != nil {
				t.Skipf("样本不可用: %v", err)
			}
			goldObjs, ok := loadLongtailGold(t, c.gold)
			if !ok {
				return
			}
			doc, err := Parse(data)
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			objs := doc.InternalObjects()
			n := 0
			for _, o := range goldObjs {
				if o["object"] != "SORTENTSTABLE" {
					continue
				}
				h, ok := goldHandleRef(o["handle"])
				if !ok {
					continue
				}
				g, ok := objs[h]
				if !ok {
					t.Errorf("SORTENTSTABLE h=%d 未解析到（落入 UNKNOWN/跳过）", h)
					continue
				}
				if g.Name != "SORTENTSTABLE" {
					t.Errorf("SORTENTSTABLE h=%d 类型名 %s", h, g.Name)
					continue
				}
				bad := ""
				checkNumField(&bad, g, "bitsize", goldFloat(o["bitsize"]))
				checkNumField(&bad, g, "size", goldFloat(o["size"]))
				if xm, ok := o["is_xdic_missing"]; ok {
					checkNumField(&bad, g, "is_xdic_missing", goldFloat(xm))
				}
				// gold 不输出 num_ents：以 sort_ents 数组长度为准
				sortEnts, _ := o["sort_ents"].([]any)
				checkNumField(&bad, g, "num_ents", float64(len(sortEnts)))
				checkHandleVector(&bad, g, "sort_ents", sortEnts)
				checkHandleField(&bad, g, "block_owner", o["block_owner"])
				ents, _ := o["ents"].([]any)
				checkHandleVector(&bad, g, "ents", ents)
				if bad != "" {
					t.Errorf("SORTENTSTABLE h=%d:%s", h, bad)
				}
				n++
				total++
			}
			if n == 0 {
				t.Errorf("%s: gold 含 SORTENTSTABLE 但未匹配到任何实例", c.sample)
			}
		})
	}
	if total > 0 {
		t.Logf("SORTENTSTABLE 对照通过 %d 实例", total)
	}
}

// goldFloat gold 数值取值（缺失/非数返回 0 并由调用方键存在性保证）。
func goldFloat(v any) float64 {
	f, _ := v.(float64)
	return f
}

// checkStrField 断言 objGeneric 字符串字段与 gold 一致。
func checkStrField(bad *string, g *object.ObjGeneric, key string, want any) {
	w, ok := want.(string)
	if !ok {
		return
	}
	got, ok := g.Field(key).(string)
	if !ok {
		*bad += fmt.Sprintf(" %s=缺失(want %q)", key, w)
		return
	}
	if got != w {
		*bad += fmt.Sprintf(" %s=%q(want %q)", key, got, w)
	}
}

// checkPoint2Field 断言 objGeneric 2RD 字段（[]float64{x y}）与 gold 数组一致。
func checkPoint2Field(bad *string, g *object.ObjGeneric, key string, goldVal any) {
	arr, ok := goldVal.([]any)
	if !ok || len(arr) != 2 {
		return
	}
	got, ok := g.Field(key).([]float64)
	if !ok {
		*bad += fmt.Sprintf(" %s=缺失(want %v)", key, arr)
		return
	}
	for i := 0; i < 2; i++ {
		w, _ := arr[i].(float64)
		if math.Abs(got[i]-w) >= 1e-6 && fmt.Sprintf("%f", got[i]) != fmt.Sprintf("%f", w) {
			*bad += fmt.Sprintf(" %s[%d]=%v(want %v)", key, i, got[i], w)
		}
	}
}

// TestIMAGEDEFGold 对照 IMAGEDEF 与 dwgread gold。gold 生成：
//
//	dwgread -O JSON -o /tmp/d_<ver>_Leader.json <样本>
//
// 覆盖 R14~R2013 共 8 样本 14 实例（R2007+ 验证字符串流 file_path；
// ATMOS 样本验证非 ASCII 路径）。
func TestIMAGEDEFGold(t *testing.T) {
	dir := os.Getenv("CAD_LIBREDWG_DATA")
	if dir == "" {
		dir = "/tmp/libredwg/test/test-data"
	}
	cases := []struct{ sample, gold string }{
		{"r14/Leader.dwg", "d_r14_Leader.json"},
		{"2000/Leader.dwg", "d_2000_Leader.json"},
		{"2004/HatchG.dwg", "d_HatchG.json"},
		{"2004/Leader.dwg", "d_2004_Leader.json"},
		{"2007/ATMOS-DC22S.dwg", "d_ATMOS-DC22S.json"},
		{"2007/Leader.dwg", "d_2007_Leader.json"},
		{"2010/Leader.dwg", "d_2010_Leader.json"},
		{"2013/Leader.dwg", "d_2013_Leader.json"},
		{"2013/gh44-error.dwg", "d_gh44-error.json"},
	}
	total := 0
	for _, c := range cases {
		c := c
		t.Run(c.sample, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, c.sample))
			if err != nil {
				t.Skipf("样本不可用: %v", err)
			}
			goldObjs, ok := loadLongtailGold(t, c.gold)
			if !ok {
				return
			}
			doc, err := Parse(data)
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			objs := doc.InternalObjects()
			n := 0
			for _, o := range goldObjs {
				if o["object"] != "IMAGEDEF" {
					continue
				}
				h, ok := goldHandleRef(o["handle"])
				if !ok {
					continue
				}
				g, ok := objs[h]
				if !ok {
					t.Errorf("IMAGEDEF h=%d 未解析到", h)
					continue
				}
				if g.Name != "IMAGEDEF" {
					t.Errorf("IMAGEDEF h=%d 类型名 %s", h, g.Name)
					continue
				}
				bad := ""
				checkNumField(&bad, g, "bitsize", goldFloat(o["bitsize"]))
				checkNumField(&bad, g, "class_version", goldFloat(o["class_version"]))
				checkPoint2Field(&bad, g, "image_size", o["image_size"])
				checkStrField(&bad, g, "file_path", o["file_path"])
				checkNumField(&bad, g, "is_loaded", goldFloat(o["is_loaded"]))
				checkNumField(&bad, g, "resunits", goldFloat(o["resunits"]))
				checkPoint2Field(&bad, g, "pixel_size", o["pixel_size"])
				if bad != "" {
					t.Errorf("IMAGEDEF h=%d:%s", h, bad)
				}
				n++
				total++
			}
			if n == 0 {
				t.Errorf("%s: gold 含 IMAGEDEF 但未匹配到任何实例", c.sample)
			}
		})
	}
	if total > 0 {
		t.Logf("IMAGEDEF 对照通过 %d 实例", total)
	}
}

// TestIMAGEDEFREACTORGold 对照 IMAGEDEF_REACTOR 与 dwgread gold。
// dat 流仅 class_version，重点验证位流在公共 handle 流前不留残余
// （bitsize/size 与 gold 一致即证明定位正确）。
func TestIMAGEDEFREACTORGold(t *testing.T) {
	dir := os.Getenv("CAD_LIBREDWG_DATA")
	if dir == "" {
		dir = "/tmp/libredwg/test/test-data"
	}
	cases := []struct{ sample, gold string }{
		{"r14/Leader.dwg", "d_r14_Leader.json"},
		{"2000/Leader.dwg", "d_2000_Leader.json"},
		{"2004/Leader.dwg", "d_2004_Leader.json"},
		{"2007/Leader.dwg", "d_2007_Leader.json"},
		{"2010/Leader.dwg", "d_2010_Leader.json"},
		{"2013/Leader.dwg", "d_2013_Leader.json"},
		{"2013/gh44-error.dwg", "d_gh44-error.json"},
	}
	total := 0
	for _, c := range cases {
		c := c
		t.Run(c.sample, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, c.sample))
			if err != nil {
				t.Skipf("样本不可用: %v", err)
			}
			goldObjs, ok := loadLongtailGold(t, c.gold)
			if !ok {
				return
			}
			doc, err := Parse(data)
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			objs := doc.InternalObjects()
			n := 0
			for _, o := range goldObjs {
				if o["object"] != "IMAGEDEF_REACTOR" {
					continue
				}
				h, ok := goldHandleRef(o["handle"])
				if !ok {
					continue
				}
				g, ok := objs[h]
				if !ok {
					t.Errorf("IMAGEDEF_REACTOR h=%d 未解析到", h)
					continue
				}
				if g.Name != "IMAGEDEF_REACTOR" {
					t.Errorf("IMAGEDEF_REACTOR h=%d 类型名 %s", h, g.Name)
					continue
				}
				bad := ""
				checkNumField(&bad, g, "bitsize", goldFloat(o["bitsize"]))
				checkNumField(&bad, g, "size", goldFloat(o["size"]))
				checkNumField(&bad, g, "class_version", goldFloat(o["class_version"]))
				if bad != "" {
					t.Errorf("IMAGEDEF_REACTOR h=%d:%s", h, bad)
				}
				n++
				total++
			}
			if n == 0 {
				t.Errorf("%s: gold 含 IMAGEDEF_REACTOR 但未匹配到任何实例", c.sample)
			}
		})
	}
	if total > 0 {
		t.Logf("IMAGEDEF_REACTOR 对照通过 %d 实例", total)
	}
}

// TestUNDERLAYDEFINITIONGold 对照 PDFDEFINITION 与 dwgread gold（2004/
// Underlay.dwg 唯一语料实例；DGN/DWFDEFINITION 语料无实例，解码器同构
// 注册）。gold 生成：dwgread -O JSON -o /tmp/d_Underlay.json <样本>
func TestUNDERLAYDEFINITIONGold(t *testing.T) {
	dir := os.Getenv("CAD_LIBREDWG_DATA")
	if dir == "" {
		dir = "/tmp/libredwg/test/test-data"
	}
	data, err := os.ReadFile(filepath.Join(dir, "2004/Underlay.dwg"))
	if err != nil {
		t.Skipf("样本不可用: %v", err)
	}
	goldObjs, ok := loadLongtailGold(t, "d_Underlay.json")
	if !ok {
		return
	}
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	objs := doc.InternalObjects()
	n := 0
	for _, o := range goldObjs {
		if o["object"] != "PDFDEFINITION" {
			continue
		}
		h, ok := goldHandleRef(o["handle"])
		if !ok {
			continue
		}
		g, ok := objs[h]
		if !ok {
			t.Errorf("PDFDEFINITION h=%d 未解析到", h)
			continue
		}
		if g.Name != "PDFDEFINITION" {
			t.Errorf("PDFDEFINITION h=%d 类型名 %s", h, g.Name)
			continue
		}
		bad := ""
		checkNumField(&bad, g, "bitsize", goldFloat(o["bitsize"]))
		checkStrField(&bad, g, "filename", o["filename"])
		checkStrField(&bad, g, "name", o["name"])
		if bad != "" {
			t.Errorf("PDFDEFINITION h=%d:%s", h, bad)
		}
		n++
	}
	if n == 0 {
		t.Error("gold 含 PDFDEFINITION 但未匹配到任何实例")
	} else {
		t.Logf("PDFDEFINITION 对照通过 %d 实例", n)
	}
}

// TestUNDERLAYGold 对照 UNDERLAY 引用实体（PDFUNDERLAY）与 dwgread gold。
// 语料仅 2004/Underlay.dwg 3 实例（DWF/DGNUNDERLAY 无实例，同构解码器
// 随拦截路径一并生效）。批次 T 起 PDFUNDERLAY 经 isEntityType 白名单走
// 正式实体解码（entUnderlay，entity 键对齐 gold），本测试改从
// EntityByHandle 取实体做字段对照；字段含实体头关键字段（preview/
// entmode/color/linewt 等）与 UNDERLAY_fields 全部专有字段。
func TestUNDERLAYGold(t *testing.T) {
	dir := os.Getenv("CAD_LIBREDWG_DATA")
	if dir == "" {
		dir = "/tmp/libredwg/test/test-data"
	}
	data, err := os.ReadFile(filepath.Join(dir, "2004/Underlay.dwg"))
	if err != nil {
		t.Skipf("样本不可用: %v", err)
	}
	goldObjs, ok := loadLongtailGold(t, "d_Underlay.json")
	if !ok {
		return
	}
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	n := 0
	for _, o := range goldObjs {
		if o["entity"] != "PDFUNDERLAY" {
			continue
		}
		h, ok := goldHandleRef(o["handle"])
		if !ok {
			continue
		}
		ent, ok := doc.EntityByHandle(h).(*entity.EntUnderlay)
		if !ok {
			t.Errorf("PDFUNDERLAY h=%d 未解析到（实体侧）", h)
			continue
		}
		base := entity.EntityBase(ent)
		bad := ""
		// 专有字段（UNDERLAY_fields）
		checkEqU64 := func(name string, got, want uint64) {
			if got != want {
				bad += fmt.Sprintf(" %s=%d(want %d)", name, got, want)
			}
		}
		checkF := func(name string, got, want float64) {
			if math.Abs(got-want) >= 1e-6 {
				bad += fmt.Sprintf(" %s=%v(want %v)", name, got, want)
			}
		}
		checkI := func(name string, got, want int64) {
			if got != want {
				bad += fmt.Sprintf(" %s=%d(want %d)", name, got, want)
			}
		}
		checkP3 := func(name string, got entity.Point3, goldVal any) {
			arr, ok := goldVal.([]any)
			if !ok || len(arr) != 3 {
				return
			}
			gv := [3]float64{got.X, got.Y, got.Z}
			for i := 0; i < 3; i++ {
				w, _ := arr[i].(float64)
				checkF(fmt.Sprintf("%s[%d]", name, i), gv[i], w)
			}
		}
		checkEqU64("definition_id", ent.DefinitionID, jsonHandleNum(o["definition_id"]))
		checkP3("extrusion", ent.Extrusion, o["extrusion"])
		checkP3("ins_pt", ent.InsPt, o["ins_pt"])
		checkF("angle", ent.Angle, goldFloat(o["angle"]))
		checkP3("scale", ent.Scale, o["scale"])
		checkI("flag", int64(ent.Flag), int64(goldFloat(o["flag"])))
		checkI("contrast", int64(ent.Contrast), int64(goldFloat(o["contrast"])))
		checkI("fade", int64(ent.Fade), int64(goldFloat(o["fade"])))
		if arr, ok := o["clip_verts"].([]any); ok {
			if len(ent.ClipVerts) != len(arr) {
				bad += fmt.Sprintf(" clip_verts 数量=%d(want %d)", len(ent.ClipVerts), len(arr))
			}
			for i, gv := range arr {
				if i >= len(ent.ClipVerts) {
					break
				}
				pt, ok := gv.([]any)
				if !ok || len(pt) != 2 {
					continue
				}
				w0, _ := pt[0].(float64)
				w1, _ := pt[1].(float64)
				checkF(fmt.Sprintf("clip_verts[%d][0]", i), ent.ClipVerts[i].X, w0)
				checkF(fmt.Sprintf("clip_verts[%d][1]", i), ent.ClipVerts[i].Y, w1)
			}
		}
		// 实体头关键字段
		if base.Head == nil {
			bad += " 公共头缺失"
		} else {
			hd := base.Head
			checkI("preview_exists", entity.B2int(base.PreviewExists), int64(goldFloat(o["preview_exists"])))
			if want, ok := o["preview"].(string); ok && base.PreviewExists {
				got := strings.ToUpper(fmt.Sprintf("%X", hd.Preview))
				if got != want {
					bad += fmt.Sprintf(" preview=%q(want %q)", got, want)
				}
			}
			checkI("entmode", int64(base.Mode), int64(goldFloat(o["entmode"])))
			checkF("ltype_scale", hd.LtypeScale, goldFloat(o["ltype_scale"]))
			checkI("ltype_flags", int64(hd.LtypeFlags), int64(goldFloat(o["ltype_flags"])))
			checkI("plotstyle_flags", int64(hd.PlotstyleFlgs), int64(goldFloat(o["plotstyle_flags"])))
			checkI("invisible", int64(hd.Invisible), int64(goldFloat(o["invisible"])))
			checkI("linewt", int64(hd.Linewt), int64(goldFloat(o["linewt"])))
			if cm, ok := o["color"].(map[string]any); ok {
				checkI("color.index", int64(base.Color.Index), int64(goldFloat(cm["index"])))
			}
			if xm, ok := o["is_xdic_missing"]; ok {
				checkI("is_xdic_missing", entity.B2int(hd.XdicMissing), int64(goldFloat(xm)))
			}
		}
		checkF("bitsize", float64(base.ObjSizeBit), goldFloat(o["bitsize"]))
		checkF("size", float64(base.RecSize), goldFloat(o["size"]))
		if bad != "" {
			t.Errorf("PDFUNDERLAY h=%d:%s", h, bad)
		}
		n++
	}
	if n == 0 {
		t.Error("gold 含 PDFUNDERLAY 但未匹配到任何实例")
	} else {
		t.Logf("PDFUNDERLAY 对照通过 %d 实例", n)
	}
}

func jsonHandleNum(v any) uint64 {
	h, _ := goldHandleRef(v)
	return h
}

// checkNum1 断言浮点字段（BD angle/ltype_scale 等），经 FieldPath 取值
// （支持 color.index 这类嵌套 map 键下钻；int64/float64 均接受）。
func checkNum1(bad *string, g *object.ObjGeneric, key string, want float64) {
	var got float64
	switch v := g.FieldPath(key).(type) {
	case float64:
		got = v
	case int64:
		got = float64(v)
	default:
		*bad += fmt.Sprintf(" %s=缺失(want %v)", key, want)
		return
	}
	if math.Abs(got-want) >= 1e-6 {
		*bad += fmt.Sprintf(" %s=%v(want %v)", key, got, want)
	}
}

// checkPoint3Field 断言 3BD 字段（[]float64{x y z}）与 gold 数组一致。
func checkPoint3Field(bad *string, g *object.ObjGeneric, key string, goldVal any) {
	arr, ok := goldVal.([]any)
	if !ok || len(arr) != 3 {
		return
	}
	got, ok := g.Field(key).([]float64)
	if !ok {
		*bad += fmt.Sprintf(" %s=缺失(want %v)", key, arr)
		return
	}
	for i := 0; i < 3; i++ {
		w, _ := arr[i].(float64)
		if math.Abs(got[i]-w) >= 1e-6 {
			*bad += fmt.Sprintf(" %s[%d]=%v(want %v)", key, i, got[i], w)
		}
	}
}

// checkClipVerts 断言 2RD 顶点数组（clip_verts[i] 键）与 gold 嵌套数组一致。
func checkClipVerts(bad *string, g *object.ObjGeneric, key string, goldVal any) {
	arr, ok := goldVal.([]any)
	if !ok {
		return
	}
	for i, gv := range arr {
		pt, ok := gv.([]any)
		if !ok || len(pt) != 2 {
			continue
		}
		got, ok := g.Field(fmt.Sprintf("%s[%d]", key, i)).([]float64)
		if !ok {
			*bad += fmt.Sprintf(" %s[%d]=缺失", key, i)
			continue
		}
		for j := 0; j < 2; j++ {
			w, _ := pt[j].(float64)
			if math.Abs(got[j]-w) >= 1e-6 {
				*bad += fmt.Sprintf(" %s[%d][%d]=%v(want %v)", key, i, j, got[j], w)
			}
		}
	}
}
