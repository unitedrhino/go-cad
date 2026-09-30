// dump_alignment_test.go 验证 DumpEntities 的 gold 键覆盖（批次 B）：
// 九样本 gold 展平键（标量/数组/句柄系）必须出现在 DumpEntities 输出中
// 且值一致。本库未建模的键按 dumpExemptKeys 豁免（清单即遗留工作项）。
package cad

import (
	"encoding/json"
	"fmt"
	"github.com/unitedrhino/go-cad/internal/entity"
	"os"
	"strings"
	"testing"
)

// dumpExemptKeys 键覆盖豁免清单（模型未建模或由专门链路覆盖）：
//   - DXF 名/类序号/未知残余位/reactors/EED 应用句柄：值级审计同口径排除
//   - acis_data/encr_sat_data：ACIS 数据段由 roundtrip 测试字节对照覆盖
//     （gold 行数组形态与导出口径不同）
//
// 极限批次 G 已建模导出的键从清单移除：ORDINATE 点组、ANG2LN xline 四点、
// clone_ins_pt/center_pt、TEXT/ATTRIB alignment_pt 与 style、MTEXT style、
// INSERT extrusion/seqend、POLYLINE*/PFACE vertex 系与 seqend、SPLINE
// beg/end_tan_vec 与 splineflags/knotparam、HATCH seeds、ACIS history_id、
// VIEWPORT 子对象句柄组（DumpEntities 仅导出模型空间口径，entmode≠2 的
// VIEWPORT 不在对照范围；其余句柄 gold 全 NULL 键可缺省）。
var dumpExemptKeys = map[string]bool{
	"_subclass": true, "index": true, "reactors": true, "dxfname": true,
	"unknown_bits": true, "num_unknown_bits": true,
	"acis_data": true, "encr_sat_data": true,
	"eed": true,
}

// TestDumpEntitiesGoldKeyCoverage 九样本键覆盖门禁：gold 展平键（豁免清单
// 外）⊆ DumpEntities 输出键且值一致。句柄键按"gold 数组末位 = 导出绝对
// 句柄"口径比对（gold 句柄值为 0 时键可缺省）。
func TestDumpEntitiesGoldKeyCoverage(t *testing.T) {
	dir := os.Getenv("CAD_LIBREDWG_DATA")
	if dir == "" {
		dir = "/tmp/libredwg/test/test-data"
	}
	var checked, missing, mismatch int
	for _, s := range injsonSamples {
		raw, err := os.ReadFile("/tmp/" + s.alias + ".json")
		if err != nil {
			t.Logf("%s: gold 不可用，跳过", s.alias)
			continue
		}
		dwg, err := os.ReadFile(filepath2(dir, s.sample))
		if err != nil {
			t.Logf("%s: 样本不可用，跳过", s.alias)
			continue
		}
		out, err := DumpEntities(dwg)
		if err != nil {
			t.Errorf("%s: DumpEntities 失败: %v", s.alias, err)
			continue
		}
		var rows []DumpEntityRow
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Errorf("%s: DumpEntities 输出非法 JSON: %v", s.alias, err)
			continue
		}
		byHandle := map[uint64]DumpEntityRow{}
		for _, r := range rows {
			byHandle[r.Handle] = r
		}
		var gold struct {
			Objects []map[string]any `json:"OBJECTS"`
		}
		if err := json.Unmarshal(raw, &gold); err != nil {
			t.Errorf("%s: gold 解析失败: %v", s.alias, err)
			continue
		}
		for _, o := range gold.Objects {
			ename, _ := o["entity"].(string)
			if ename == "" || ename == "ATTDEF" {
				continue
			}
			if em, _ := o["entmode"].(float64); em != 2 {
				continue // DumpEntities 只导出模型空间口径
			}
			h := jsonTestHandle(o["handle"])
			row, ok := byHandle[h]
			if !ok {
				t.Errorf("%s: DumpEntities 缺模型空间实体 h=%d (%s)", s.alias, h, ename)
				continue
			}
			flat := map[string]any{}
			flattenJSONGold("", o, &flat)
			for k, gv := range flat {
				base := k
				if i := strings.Index(base, "["); i > 0 {
					base = base[:i]
				}
				// 豁免匹配：首段宿主（paths[0].flag → paths）与尾段宿主
				// （paths[0].boundary_handles → boundary_handles）双口径
				tail := k
				if i := strings.LastIndex(tail, "."); i >= 0 {
					tail = tail[i+1:]
				}
				if i := strings.Index(tail, "["); i > 0 {
					tail = tail[:i]
				}
				if dumpExemptKeys[base] || dumpExemptKeys[ename+"."+base] ||
					dumpExemptKeys[tail] || dumpExemptKeys[ename+"."+tail] {
					continue
				}
				// gold 历史噪声（AcDs blob 场景等，与 62 行门禁同清单豁免）
				if isGoldNoise(s.alias, ename, h, k) || isGoldNoise(s.alias, ename, h, base) {
					continue
				}
				// gold NULL 句柄（[code,size,0(,0)] 末位 0）：键可缺省
				if jsonTestHandle(gv) == 0 && isHandleArray(gv) {
					continue
				}
				switch gv.(type) {
				case float64, string, bool, []any:
				default:
					continue
				}
				checked++
				dv, ok := row.DXF[k]
				if !ok {
					missing++
					if missing <= 30 {
						t.Errorf("%s: h=%d %s 缺键 %s（gold=%v）", s.alias, h, ename, k, briefGold(gv))
					}
					continue
				}
				if !dumpValueMatch(dv, gv) {
					mismatch++
					if mismatch <= 30 {
						t.Errorf("%s: h=%d %s 键 %s 值不一致: dump=%v gold=%v", s.alias, h, ename, k, dv, briefGold(gv))
					}
				}
			}
		}
	}
	t.Logf("键覆盖对照 %d 项：缺失 %d，不符 %d", checked, missing, mismatch)
	if missing > 0 || mismatch > 0 {
		t.Errorf("DumpEntities gold 键覆盖未达标（缺失 %d，不符 %d；豁免清单外须清零）", missing, mismatch)
	}
}

// filepath2 测试侧路径拼接（避免与已有别名冲突的简名包装）。
func filepath2(dir, name string) string { return dir + "/" + name }

// briefGold gold 值的短描述（错误消息用，长数组截断）。
func briefGold(v any) string {
	if arr, ok := v.([]any); ok && len(arr) > 4 {
		return fmt.Sprintf("数组[%d]{%v...}", len(arr), arr[:4])
	}
	return fmt.Sprintf("%v", v)
}

// isHandleArray gold 值是否为句柄数组形态（首元素为组码 2~6 的数字）。
func isHandleArray(v any) bool {
	arr, ok := v.([]any)
	if !ok || len(arr) < 3 {
		return false
	}
	code, ok := arr[0].(float64)
	return ok && code >= 2 && code <= 6
}

// dumpValueMatch DumpEntities 导出值与 gold 展平值的等价判定：
// 标量容差、数组逐元素（gold 嵌套展平 + 2/3 元点补齐）、句柄数组取末位。
// got 为 JSON 反序列化形态（[]any/float64/int64 不定），统一归一比对。
func dumpValueMatch(got, want any) bool {
	switch w := want.(type) {
	case float64:
		switch g := got.(type) {
		case float64:
			return entity.NearF(g, w)
		case int64:
			return entity.NearF(float64(g), w)
		}
		return false
	case string:
		g, ok := got.(string)
		return ok && g == w
	case bool:
		switch g := got.(type) {
		case bool:
			return g == w
		case float64:
			return (w && g == 1) || (!w && g == 0)
		}
		return false
	case []any:
		// 句柄数组的数组（INSERT attribs / POLYLINE vertex：gold 为
		// [[code,size,abs,abs],...]）：导出数字数组逐位对应各句柄末位
		if len(w) > 0 {
			if inner, ok := w[0].([]any); ok && len(inner) >= 3 {
				if code, ok := inner[0].(float64); ok && code >= 2 && code <= 6 {
					g := toFlatF64(got)
					if g == nil || len(g) != len(w) {
						return false
					}
					for i, e := range w {
						hv := jsonTestHandle(e)
						if hv == 0 || uint64(g[i]) != hv {
							return false
						}
					}
					return true
				}
			}
		}
		// 句柄数组形态（[code,size,abs(,abs)]）：取末位与导出数字比对
		if hv := jsonTestHandle(w); hv != 0 {
			switch g := got.(type) {
			case float64:
				return uint64(g) == hv
			case int64:
				return uint64(g) == hv
			}
		}
		// 普通数组：gold 嵌套展平后与导出数组逐元素比对（got 为 JSON
		// 反序列化的 []any 或测试侧 []float64，统一展平）
		g := toFlatF64(got)
		if g == nil {
			return false
		}
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
			if !entity.NearF(g[i], flat[i]) {
				return false
			}
		}
		return true
	}
	return false
}

// toFlatF64 JSON 导出值（[]any）或测试侧切片（[]float64）展平为数字切片
// （含嵌套一层）；非数组形态返回 nil。
func toFlatF64(v any) []float64 {
	switch t := v.(type) {
	case []float64:
		return t
	case []any:
		var out []float64
		for _, e := range t {
			switch ev := e.(type) {
			case float64:
				out = append(out, ev)
			case []any:
				for _, e2 := range ev {
					if f2, ok := e2.(float64); ok {
						out = append(out, f2)
					}
				}
			default:
				return nil
			}
		}
		return out
	}
	return nil
}
