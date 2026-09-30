// 本文件用 LibreDWG dwgread -O JSON 输出对 example_2018.dwg 的全部
// DICTIONARY/XRECORD 对象做逐字段交叉验证（不只是抽样）。
// JSON 路径经 libredwgGoldJSONPath 统一解析（CAD_LIBREDWG_JSON 或
// CAD_GOLD_JSON_DIR），缺省 /tmp/ex2018.json，不存在则跳过。
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

// jsonHandle JSON 中句柄数组 [code, size, ..., value]，取末位为绝对句柄值
func jsonHandle(v any) uint64 {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return 0
	}
	last := arr[len(arr)-1]
	f, ok := last.(float64)
	if !ok {
		return 0
	}
	return uint64(f)
}

// jsonNum JSON 数值转 int
func jsonNum(v any) int {
	f, _ := v.(float64)
	return int(f)
}

// TestDictionaryXrecordFullJSON 对照全部 DICTIONARY/XRECORD 与 dwgread JSON
func TestDictionaryXrecordFullJSON(t *testing.T) {
	dir := testsupport.LibredwgTestDataDir()
	jsonPath := testsupport.LibredwgGoldJSONPath("2018")
	// 样本路径：优先 CAD_LIBREDWG_SAMPLE；否则从 gold JSON 名推导
	// 保证解析样本与 gold 同源（ex2018.json → example_2018.dwg；
	// sample2018.json → sample_2018.dwg）
	base := filepath.Base(jsonPath)
	sample := os.Getenv("CAD_LIBREDWG_SAMPLE")
	if sample == "" {
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

	var nDicPass, nXrPass, nDicFail, nXrFail int
	for _, o := range gold.Objects {
		switch o["object"] {
		case "DICTIONARY":
			h := jsonHandle(o["handle"])
			dic, ok := doc.dictionaries[h]
			if !ok {
				t.Errorf("DICTIONARY h=%d 未解析到", h)
				nDicFail++
				continue
			}
			bad := ""
			if got, want := dic.numItems, jsonNum(o["numitems"]); got != want {
				bad += fmt.Sprintf(" numitems=%d(want %d)", got, want)
			}
			if got, want := int(dic.objSizeBit), jsonNum(o["bitsize"]); got != want {
				bad += fmt.Sprintf(" bitsize=%d(want %d)", got, want)
			}
			if got, want := dic.cloning, uint16(jsonNum(o["cloning"])); got != want {
				bad += fmt.Sprintf(" cloning=%d(want %d)", got, want)
			}
			if got, want := dic.xdicMissing, jsonNum(o["is_xdic_missing"]) == 1; got != want {
				bad += fmt.Sprintf(" xdic_missing=%v(want %v)", got, want)
			}
			// items map：键名 + 句柄（集合对照，JSON map 无序）。
			// gold 的 items 为 map，重复键（如多个空串键名）会被去重，
			// 数量以记录自身的 numitems 为准，items 仅校验键句柄匹配。
			if items, ok := o["items"].(map[string]any); ok {
				if want, ok2 := o["numitems"]; ok2 && len(dic.texts) != jsonNum(want) {
					bad += fmt.Sprintf(" numitems=%d(want %d)", len(dic.texts), jsonNum(want))
				}
				gotSet := map[string]uint64{}
				for i, k := range dic.texts {
					if i < len(dic.itemHandles) {
						gotSet[k] = dic.itemHandles[i]
					}
				}
				for k, hv := range items {
					want := jsonHandle(hv)
					if gotSet[k] != want {
						bad += fmt.Sprintf(" item[%s]=%d(want %d)", k, gotSet[k], want)
					}
				}
			}
			if bad != "" {
				t.Errorf("DICTIONARY h=%d:%s", h, bad)
				nDicFail++
			} else {
				nDicPass++
			}
		case "XRECORD":
			h := jsonHandle(o["handle"])
			xr, ok := doc.xrecords[h]
			if !ok {
				t.Errorf("XRECORD h=%d 未解析到", h)
				nXrFail++
				continue
			}
			bad := ""
			if got, want := int(xr.objSizeBit), jsonNum(o["bitsize"]); got != want {
				bad += fmt.Sprintf(" bitsize=%d(want %d)", got, want)
			}
			if got, want := xr.cloning, uint16(jsonNum(o["cloning"])); got != want {
				bad += fmt.Sprintf(" cloning=%d(want %d)", got, want)
			}
			if got, want := xr.xdicMissing, jsonNum(o["is_xdic_missing"]) == 1; got != want {
				bad += fmt.Sprintf(" xdic_missing=%v(want %v)", got, want)
			}
			if got, want := xr.xdataSize, jsonNum(o["xdata_size"]); got != want {
				bad += fmt.Sprintf(" xdata_size=%d(want %d)", got, want)
			}
			// xdata 类型化值逐项对照（gold 每项 [code, value]）
			if goldXdata, ok := o["xdata"].([]any); ok {
				if len(goldXdata) != len(xr.xdata) {
					bad += fmt.Sprintf(" xdata=%d 项(want %d)", len(xr.xdata), len(goldXdata))
				} else {
					for i, gv := range goldXdata {
						gp, ok := gv.([]any)
						if !ok || len(gp) != 2 {
							continue
						}
						got := xr.xdata[i]
						if got.Code != jsonNum(gp[0]) {
							bad += fmt.Sprintf(" xdata[%d].code=%d(want %d)", i, got.Code, jsonNum(gp[0]))
							continue
						}
						if !xdataValueEqual(got, gp[1]) {
							bad += fmt.Sprintf(" xdata[%d]=%v(want %v)", i, got.Val(), gp[1])
						}
					}
				}
			}
			// LibreDWG JSON 不输出 numreactors 键，实际值 = reactors 数组长度
			wantReactors := 0
			if reactors, ok := o["reactors"].([]any); ok {
				wantReactors = len(reactors)
			}
			if got, want := xr.numReactors, wantReactors; got != want {
				bad += fmt.Sprintf(" numreactors=%d(want %d)", got, want)
			}
			if bad != "" {
				t.Errorf("XRECORD h=%d:%s", h, bad)
				nXrFail++
			} else {
				nXrPass++
			}
		}
	}
	t.Logf("DICTIONARY 通过 %d/%d，XRECORD 通过 %d/%d", nDicPass, nDicPass+nDicFail, nXrPass, nXrPass+nXrFail)
	if nDicFail > 0 || nXrFail > 0 {
		t.Fail()
	}
}

// xdataValueEqual 比较 Go xdata 值与 gold JSON 值是否同形同值
func xdataValueEqual(it xdataItem, gold any) bool {
	switch it.Kind {
	case xdataReal:
		// LibreDWG JSON 以 %.14f 输出 REAL（去尾零），完整精度值需按容差比较
		g, ok := gold.(float64)
		return ok && math.Abs(it.Float-g) <= 5e-14*math.Max(1, math.Abs(g))
	case xdataPoint3D:
		g, ok := gold.([]any)
		if !ok || len(g) != 3 {
			return false
		}
		for i := 0; i < 3; i++ {
			gf, ok := g[i].(float64)
			if !ok || it.Point[i] != gf {
				return false
			}
		}
		return true
	case xdataString:
		g, ok := gold.(string)
		return ok && it.Str == g
	case xdataBinary:
		g, ok := gold.(string)
		return ok && fmt.Sprintf("%X", it.Bytes) == g
	default:
		g, ok := gold.(float64)
		return ok && it.Int == int64(g)
	}
}
