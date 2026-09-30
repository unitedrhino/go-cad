// 本文件验证通用内部对象（SCALE/DICTIONARYVAR/APPID 等）与 LibreDWG
// dwgread -O JSON 输出的字段级一致性。gold 路径环境变量与
// objects_internal_json_test.go 共用（经 libredwgGoldJSONPath 统一解析：
// CAD_LIBREDWG_JSON 或 CAD_GOLD_JSON_DIR；语料目录经 libredwgTestDataDir），
// 缺省对 example_2018 对照，不存在则跳过。
package cad

import (
	"encoding/json"
	"fmt"
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// goldFieldChecker 通用内部对象 gold 字段检查器：
// 键为 dwgread JSON 字段名，值为期望值（int64/float64/bool/string）。
type goldFieldChecker map[string]any

// internalGoldChecks 逐类型的 gold 字段校验规则（对每个该类型对象全量校验）。
// 值支持两种形式：直接期望值，或 func(any) bool 自定义判定。
var internalGoldChecks = map[string]goldFieldChecker{
	"DICTIONARYVAR": {
		"schema": func(v any) bool { _, ok := v.(int64); return ok },
	},
	"SCALE": {
		"name":          func(v any) bool { s, ok := v.(string); return ok && s != "" },
		"paper_units":   func(v any) bool { f, ok := v.(float64); return ok && f > 0 },
		"drawing_units": func(v any) bool { f, ok := v.(float64); return ok && f > 0 },
	},
	"APPID": {
		"name": func(v any) bool { s, ok := v.(string); return ok && s != "" },
	},
	"GROUP": {
		"unnamed":    func(v any) bool { _, ok := v.(int64); return ok },
		"selectable": func(v any) bool { _, ok := v.(int64); return ok },
		"num_groups": func(v any) bool { n, ok := v.(int64); return ok && n >= 0 },
	},
	"WIPEOUTVARIABLES": {
		"display_frame": func(v any) bool { _, ok := v.(int64); return ok },
	},
	"VISUALSTYLE": {
		"description": func(v any) bool { _, ok := v.(string); return ok },
		"style_type":  func(v any) bool { _, ok := v.(int64); return ok },
	},
	"LAYOUT": {
		"layout_name": func(v any) bool { s, ok := v.(string); return ok && s != "" },
	},
	"BLOCK_HEADER": {
		"name": func(v any) bool { _, ok := v.(string); return ok },
	},
	"LTYPE": {
		"description": func(v any) bool { _, ok := v.(string); return ok },
		"alignment":   func(v any) bool { _, ok := v.(int64); return ok },
	},
	"MATERIAL": {
		"name": func(v any) bool { s, ok := v.(string); return ok && s != "" },
	},
	"DIMASSOC": {
		"associativity": func(v any) bool { _, ok := v.(int64); return ok },
	},
	"ASSOCNETWORK": {
		"class_version":   func(v any) bool { _, ok := v.(int64); return ok },
		"network_version": func(v any) bool { _, ok := v.(int64); return ok },
	},
	"SUN": {
		"is_on":      func(v any) bool { _, ok := v.(bool); return ok },
		"intensity":  func(v any) bool { f, ok := v.(float64); return ok && f > 0 },
		"julian_day": func(v any) bool { _, ok := v.(int64); return ok },
	},
	"ACSH_HISTORY_CLASS": {
		"major": func(v any) bool { _, ok := v.(int64); return ok },
		"minor": func(v any) bool { _, ok := v.(int64); return ok },
	},
	"TABLEGEOMETRY": {
		"numrows": func(v any) bool { n, ok := v.(int64); return ok && n >= 0 },
		"numcols": func(v any) bool { n, ok := v.(int64); return ok && n >= 0 },
	},
	"DICTIONARYWDFLT": {
		"numitems": func(v any) bool { n, ok := v.(int64); return ok && n >= 0 },
	},
	"STYLE": {
		"name":      func(v any) bool { s, ok := v.(string); return ok && s != "" },
		"font_file": func(v any) bool { _, ok := v.(string); return ok },
	},
	"VPORT": {
		"name":        func(v any) bool { s, ok := v.(string); return ok && s != "" },
		"VIEWSIZE":    func(v any) bool { _, ok := v.(float64); return ok },
		"view_width":  func(v any) bool { _, ok := v.(float64); return ok },
		"circle_zoom": func(v any) bool { n, ok := v.(int64); return ok && n >= 0 },
		"SNAPISOPAIR": func(v any) bool { _, ok := v.(int64); return ok },
		// R13/R14 位流无 UCSORTHOVIEW（SINCE R_2000b），允许缺省
		"UCSORTHOVIEW": func(v any) bool {
			return v == nil || func() bool { _, ok := v.(int64); return ok }()
		},
	},
	"MLINESTYLE": {
		"name":            func(v any) bool { s, ok := v.(string); return ok && s != "" },
		"flag":            func(v any) bool { n, ok := v.(int64); return ok && n >= 0 },
		"start_angle":     func(v any) bool { f, ok := v.(float64); return ok && f > 1.5 && f < 1.6 },
		"end_angle":       func(v any) bool { f, ok := v.(float64); return ok && f > 1.5 && f < 1.6 },
		"num_lines":       func(v any) bool { n, ok := v.(int64); return ok && n > 0 },
		"lines[0].offset": func(v any) bool { _, ok := v.(float64); return ok },
		// R2018+ 改为 handle 流 lt_ltype，dat 流无 lt_index（允许缺省）
		"lines[0].lt_index": func(v any) bool {
			return v == nil || func() bool { n, ok := v.(int64); return ok && n > 0 }()
		},
	},
	"ASSOCOSNAPPOINTREFACTIONPARAM": {
		"is_r2013":   func(v any) bool { _, ok := v.(int64); return ok },
		"osnap_mode": func(v any) bool { _, ok := v.(int64); return ok },
	},
	"ASSOCVERTEXACTIONPARAM": {
		"is_r2013": func(v any) bool { _, ok := v.(int64); return ok },
	},
	"DIMSTYLE": {
		"name":     func(v any) bool { s, ok := v.(string); return ok && s != "" },
		"DIMSCALE": func(v any) bool { f, ok := v.(float64); return ok && f > 0 },
		"DIMASZ":   func(v any) bool { f, ok := v.(float64); return ok && f > 0 },
		"DIMEXO":   func(v any) bool { _, ok := v.(float64); return ok },
		"DIMGAP":   func(v any) bool { _, ok := v.(float64); return ok },
		"DIMTAD":   func(v any) bool { n, ok := v.(int64); return ok && n >= 0 },
		// R13/R14 位流无以下字段（R2000b+ 才有），允许缺省
		"DIMATFIT": func(v any) bool { return v == nil || func() bool { _, ok := v.(int64); return ok }() },
		"DIMLWD":   func(v any) bool { return v == nil || func() bool { _, ok := v.(int64); return ok }() },
		"DIMLWE":   func(v any) bool { return v == nil || func() bool { _, ok := v.(int64); return ok }() },
	},
	"BLOCK_CONTROL":       {},
	"LAYER_CONTROL":       {},
	"STYLE_CONTROL":       {},
	"LTYPE_CONTROL":       {},
	"VIEW_CONTROL":        {},
	"UCS_CONTROL":         {},
	"VPORT_CONTROL":       {},
	"APPID_CONTROL":       {},
	"DIMSTYLE_CONTROL":    {},
	"ASSOCACTION":         {},
	"ASSOCDEPENDENCY":     {},
	"ASSOCGEOMDEPENDENCY": {},
	"UNKNOWN_OBJ":         {},
	"VX_CONTROL":          {},
	"CELLSTYLEMAP": {
		"num_cells": func(v any) bool { n, ok := v.(int64); return ok && n >= 0 },
	},
}

// anyEqual gold 值与字段值相等判定（REAL 按容差）
func anyEqual(want any, got any) bool {
	switch w := want.(type) {
	case string:
		g, ok := got.(string)
		return ok && w == g
	case bool:
		g, ok := got.(bool)
		return ok && w == g
	case int:
		g, ok := got.(int64)
		return ok && int64(w) == g
	case float64:
		switch g := got.(type) {
		case float64:
			return math.Abs(g-w) <= 5e-14*maxAbs(1, g)
		case int64:
			return float64(g) == w
		}
		return false
	}
	return false
}

func maxAbs(a, b float64) float64 {
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	if a > b {
		return a
	}
	return b
}

// TestInternalObjectsGold 对照通用内部对象与 dwgread JSON 字段
func TestInternalObjectsGold(t *testing.T) {
	dir := testsupport.LibredwgTestDataDir()
	jsonPath := testsupport.LibredwgGoldJSONPath("2018")
	sample := os.Getenv("CAD_LIBREDWG_SAMPLE")
	if sample == "" {
		// 从 gold JSON 文件名推导同源样本（保证解析样本与 gold 一致）：
		// ex2018.json → example_2018.dwg；sample2018.json → sample_2018.dwg
		base := filepath.Base(jsonPath)
		sample = "example_2018.dwg"
		if len(base) > 4 && base[:2] == "ex" {
			sample = "example_" + base[2:len(base)-5] + ".dwg"
		} else if len(base) > 10 && base[:6] == "sample" {
			sample = "sample_" + base[6:len(base)-5] + ".dwg"
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, sample))
	if err != nil {
		t.Skipf("样本不可用: %v", err)
	}
	goldRaw, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Skipf("gold JSON 不可用: %v", err)
	}
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	var gold struct {
		Objects []map[string]any `json:"OBJECTS"`
	}
	if err := json.Unmarshal(goldRaw, &gold); err != nil {
		t.Fatalf("gold JSON 解析失败: %v", err)
	}
	var pass, fail int
	for _, o := range gold.Objects {
		name, _ := o["object"].(string)
		check, ok := internalGoldChecks[name]
		if !ok {
			continue
		}
		h := jsonHandle(o["handle"])
		g, ok := doc.internalObjects[h]
		if !ok {
			t.Errorf("%s h=%d 未解析到", name, h)
			fail++
			continue
		}
		bad := ""
		for key, want := range check {
			got := g.Field(key)
			switch w := want.(type) {
			case func(any) bool:
				if !w(got) {
					bad += fmt.Sprintf(" %s=%v(校验失败)", key, got)
				}
			default:
				if got == nil || !anyEqual(want, got) {
					bad += fmt.Sprintf(" %s=%v(want %v)", key, got, want)
				}
			}
		}
		if bad != "" {
			t.Errorf("%s h=%d:%s", name, h, bad)
			fail++
		} else {
			pass++
		}
	}
	t.Logf("内部对象字段对照通过 %d/%d", pass, pass+fail)
	if fail > 0 {
		t.Fail()
	}
}
