// entities_dimension_audit_test.go DIMENSION 族专项值级审计：
// 只统计 gold 中 DIMENSION_* 实体的标量键对齐情况，按布局分样本汇总
// 缺失/不符，供 P1.4 组 B 清零验证与回归追踪（不作为通过门槛）。
package cad

import (
	"encoding/json"
	"fmt"
	"github.com/unitedrhino/go-cad/internal/entity"
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"os"
	"sort"
	"strings"
	"testing"
)

// TestDimensionEntityAudit 九样本 DIMENSION 族值级审计：
// 输出总体对齐率与缺失/不符键分布（Top），环境变量 CAD_DIM_AUDIT_QUIET
// 可抑制明细。键过滤口径与 objects_alignment_audit_test.go 保持一致。
func TestDimensionEntityAudit(t *testing.T) {
	candidates := []string{
		"exr13", "exr14", "ex2000", "ex2004", "ex2007", "ex2013", "ex2018",
		"sample2000", "sample2018",
	}
	var gTotal, gMatch, gMiss, gDiff int
	perLayout := map[string]*[4]int{} // 布局名 → [total, match, miss, diff]
	layoutInc := func(name string, ti, mi, si, di int) {
		a, ok := perLayout[name]
		if !ok {
			a = &[4]int{}
			perLayout[name] = a
		}
		a[0] += ti
		a[1] += mi
		a[2] += si
		a[3] += di
	}
	for _, c := range candidates {
		raw, err := os.ReadFile("/tmp/" + c + ".json")
		if err != nil {
			t.Logf("%s: gold 不可用，跳过", c)
			continue
		}
		sample := ""
		switch {
		case strings.HasPrefix(c, "ex"):
			sample = "example_" + c[2:] + ".dwg"
		case strings.HasPrefix(c, "sample"):
			sample = "sample_" + c[6:] + ".dwg"
		}
		data, err := os.ReadFile(testsupport.LibredwgTestDataDir() + "/" + sample)
		if err != nil {
			t.Logf("%s: 样本不可用，跳过", c)
			continue
		}
		doc, err := Parse(data)
		if err != nil {
			t.Errorf("%s: 解析失败 %v", c, err)
			continue
		}
		var gold struct {
			Objects []map[string]any `json:"OBJECTS"`
		}
		if err := json.Unmarshal(raw, &gold); err != nil {
			t.Errorf("%s: gold 解析失败 %v", c, err)
			continue
		}
		var total, match, miss, diff int
		missKeys := map[string]int{}
		diffKeys := map[string]int{}
		for _, o := range gold.Objects {
			name, _ := o["entity"].(string)
			if !strings.HasPrefix(name, "DIMENSION") {
				continue
			}
			hv, _ := o["handle"].([]any)
			if len(hv) == 0 {
				continue
			}
			h := uint64(hv[len(hv)-1].(float64))
			ent := doc.EntityByHandle(h)
			if ent == nil {
				t.Logf("  [%s] %s h=%d 实体缺失（未解出）", c, name, h)
				continue
			}
			flat := map[string]any{}
			flattenGold("", o, &flat)
			for k, want := range flat {
				if strings.HasSuffix(k, "handle") || strings.HasSuffix(k, "_handle") ||
					k == "index" || strings.Contains(k, "reactors") ||
					k == "dxfname" || k == "_subclass" || k == "eed" ||
					k == "unknown_bits" || k == "num_unknown_bits" {
					continue
				}
				switch want.(type) {
				case float64, string, bool:
				default:
					continue
				}
				total++
				got := entity.EntityField(ent, k)
				if got == nil {
					miss++
					missKeys[k]++
					layoutInc(name, 1, 0, 1, 0)
					continue
				}
				if auditValueMatch(k, got, want) {
					match++
					layoutInc(name, 1, 1, 0, 0)
				} else {
					diff++
					diffKeys[k]++
					layoutInc(name, 1, 0, 0, 1)
				}
			}
		}
		if total > 0 {
			t.Logf("%s: DIMENSION 对齐 %d/%d = %.1f%%（缺失 %d，不符 %d）",
				c, match, total, float64(match)*100/float64(total), miss, diff)
		} else {
			t.Logf("%s: 无 DIMENSION 实例", c)
		}
		if len(missKeys) > 0 {
			t.Logf("  [%s] 缺失键: %s", c, formatKeyCounts(missKeys))
		}
		if len(diffKeys) > 0 {
			t.Logf("  [%s] 不符键: %s", c, formatKeyCounts(diffKeys))
		}
		gTotal += total
		gMatch += match
		gMiss += miss
		gDiff += diff
	}
	if gTotal == 0 {
		t.Skip("无可比 DIMENSION 字段（gold 不可用）")
	}
	names := make([]string, 0, len(perLayout))
	for n := range perLayout {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		a := perLayout[n]
		t.Logf("布局 %-19s 对齐 %4d/%4d = %5.1f%%（缺失 %3d，不符 %2d）",
			n, a[1], a[0], float64(a[1])*100/float64(a[0]), a[2], a[3])
	}
	t.Logf("DIMENSION 总计: 对齐 %d/%d = %.1f%%（缺失 %d，不符 %d）",
		gMatch, gTotal, float64(gMatch)*100/float64(gTotal), gMiss, gDiff)
}

// formatKeyCounts 键计数按次数降序渲染（Top 12）。
func formatKeyCounts(m map[string]int) string {
	type kv struct {
		k string
		n int
	}
	l := make([]kv, 0, len(m))
	for k, n := range m {
		l = append(l, kv{k, n})
	}
	sort.Slice(l, func(i, j int) bool { return l[i].n > l[j].n })
	if len(l) > 12 {
		l = l[:12]
	}
	parts := make([]string, 0, len(l))
	for _, e := range l {
		parts = append(parts, fmt.Sprintf("%s×%d", e.k, e.n))
	}
	return strings.Join(parts, " ")
}
