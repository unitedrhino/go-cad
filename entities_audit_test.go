// entities_audit_test.go 实体审计辅助测试：复制 TestEntityAudit 的对照循环，
// 通过 CAD_AUDIT_DUMP 环境变量输出全量差异聚合（按 类.键 计数，不受
// objects_alignment_audit_test.go 的 DIFF/MISS 前 40/20 条截断影响），
// 用于组 A 实体族逐类清零时定位残余差异。
package cad

import (
	"encoding/json"
	"fmt"
	"github.com/unitedrhino/go-cad/internal/objrec"
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestEntityAuditDump 全量差异聚合导出：CAD_AUDIT_DUMP=<文件> 时生效。
func TestEntityAuditDump(t *testing.T) {
	out := os.Getenv("CAD_AUDIT_DUMP")
	if out == "" {
		t.Skip("CAD_AUDIT_DUMP 未设置")
	}
	dir := testsupport.LibredwgTestDataDir()
	candidates := []string{
		"exr13", "exr14", "ex2000", "ex2004", "ex2007", "ex2013", "ex2018",
		"sample2000", "sample2018",
	}
	// agg[样本][类.键] = [缺失数, 不符数]
	agg := map[string]map[string][2]int{}
	summary := map[string][3]int{}
	var f *os.File
	if out != "" {
		var err error
		f, err = os.Create(out)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
	}
	for _, c := range candidates {
		jsonPath := "/tmp/" + c + ".json"
		raw, err := os.ReadFile(jsonPath)
		if err != nil {
			continue
		}
		base := filepath.Base(jsonPath)
		sample := ""
		if len(base) > 4 && base[:2] == "ex" {
			sample = "example_" + base[2:len(base)-5] + ".dwg"
		} else if len(base) > 10 && base[:6] == "sample" {
			sample = "sample_" + base[6:len(base)-5] + ".dwg"
		}
		data, err := os.ReadFile(filepath.Join(dir, sample))
		if err != nil {
			continue
		}
		doc, err := Parse(data)
		if err != nil {
			continue
		}
		var gold struct {
			Objects []map[string]any `json:"OBJECTS"`
		}
		if err := json.Unmarshal(raw, &gold); err != nil {
			continue
		}
		var total, miss, diff int
		for _, o := range gold.Objects {
			name, _ := o["object"].(string)
			isEntity := false
			if ename, ok := o["entity"].(string); ok && ename != "" {
				isEntity = true
				name = ename
			}
			if !isEntity || name == "" {
				continue
			}
			hv, _ := o["handle"].([]any)
			if len(hv) == 0 {
				continue
			}
			h := uint64(hv[len(hv)-1].(float64))
			ent := doc.EntityByHandle(h)
			if ent == nil {
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
				got := entityField(ent, k)
				if got == nil {
					miss++
					m := agg[c]
					if m == nil {
						m = map[string][2]int{}
						agg[c] = m
					}
					e := m[name+"."+k]
					e[0]++
					m[name+"."+k] = e
					continue
				}
				if !auditValueMatch(k, got, want) {
					diff++
					m := agg[c]
					if m == nil {
						m = map[string][2]int{}
						agg[c] = m
					}
					e := m[name+"."+k]
					e[1]++
					m[name+"."+k] = e
					if os.Getenv("CAD_AUDIT_GOT") != "" {
						fmt.Fprintf(f, "GOT %s %s h=%d %s: got %v want %v\n", c, name, h, k, got, want)
					}
				}
			}
		}
		summary[c] = [3]int{total, miss, diff}
	}
	names := make([]string, 0, len(summary))
	for c := range summary {
		names = append(names, c)
	}
	sort.Strings(names)
	for _, c := range names {
		s := summary[c]
		fmt.Fprintf(f, "== %s total=%d miss=%d diff=%d\n", c, s[0], s[1], s[2])
		keys := make([]string, 0, len(agg[c]))
		for k := range agg[c] {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			a, b := agg[c][keys[i]], agg[c][keys[j]]
			if a[0]+a[1] != b[0]+b[1] {
				return a[0]+a[1] > b[0]+b[1]
			}
			return keys[i] < keys[j]
		})
		for _, k := range keys {
			e := agg[c][k]
			fmt.Fprintf(f, "   %-46s miss=%d diff=%d\n", k, e[0], e[1])
		}
	}
}

// TestEntityScanDebug 单实体扫描调试：CAD_MTEXT_DEBUG=<文件名>#<句柄> 时
// 手动解剖该实体的头解析与图元字段位流。
func TestEntityScanDebug(t *testing.T) {
	spec := os.Getenv("CAD_MTEXT_DEBUG")
	if spec == "" {
		t.Skip("CAD_MTEXT_DEBUG 未设置")
	}
	parts := strings.SplitN(spec, "#", 2)
	var wantH uint64
	fmt.Sscanf(parts[1], "%d", &wantH)
	data, err := os.ReadFile(filepath.Join(testsupport.LibredwgTestDataDir(), parts[0]))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	objectMap, err := readR2000Section(data, r2000SecObjectMap)
	if err != nil {
		t.Fatal(err)
	}
	index, err := objrec.ParseObjectMapHandles(objectMap)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range index {
		if ref.Handle != wantH {
			continue
		}
		rec, err := objrec.ParseObjectRecord(data, ref, false)
		if err != nil {
			t.Fatal(err)
		}
		h, err := objrec.ParseObjHeader(rec)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Printf("type=%d dataStartBit=%d size=%d bodyLen=%d dataEnd=%d codepage=%d\n",
			h.TypeCode, h.DataStartBit, rec.Size, len(rec.Body)*8, rec.DataEndBit(), doc.codepage)
		r := rec.BodyBitStream()
		r.SetBitPos(h.DataStartBit)
		head, err := parseCommonEntityHeadR14(r, rec.DataEndBit())
		if err != nil {
			t.Fatalf("head: %v", err)
		}
		fmt.Printf("head ok: handle=%d objSizeBit=%d entmode=%d color=%d scale=%g bitsEnd=%d\n",
			head.handle, head.objSizeBit, head.entityMode, head.color.index, head.ltypeScale, r.TellBits())
		// 手动逐字段读 MTEXT 主体
		rd := func(name string, v float64, e error) {
			if e != nil {
				fmt.Printf("  %s ERR %v\n", name, e)
				panic(e)
			}
			fmt.Printf("  %s = %g (@%d)\n", name, v, r.TellBits())
		}
		ix, iy, iz, e1 := r.Read3BD()
		if e1 != nil {
			t.Fatalf("ins 3BD: %v", e1)
		}
		fmt.Printf("  ins = %g,%g,%g (@%d)\n", ix, iy, iz, r.TellBits())
		ex, ey, ez, e2 := r.Read3BD()
		if e2 != nil {
			t.Fatalf("extr 3BD: %v", e2)
		}
		fmt.Printf("  extr = %g,%g,%g (@%d)\n", ex, ey, ez, r.TellBits())
		xx, xy, xz, e3 := r.Read3BD()
		if e3 != nil {
			t.Fatalf("xdir 3BD: %v", e3)
		}
		fmt.Printf("  xdir = %g,%g,%g (@%d)\n", xx, xy, xz, r.TellBits())
		rw, e4 := r.ReadBD()
		rd("rect_width", rw, e4)
		th, e5 := r.ReadBD()
		rd("text_height", th, e5)
		att, e6 := r.ReadBS()
		if e6 != nil {
			t.Fatalf("attachment: %v", e6)
		}
		fmt.Printf("  attachment = %d (@%d)\n", att, r.TellBits())
		fd, e7 := r.ReadBS()
		if e7 != nil {
			t.Fatalf("flow: %v", e7)
		}
		fmt.Printf("  flow_dir = %d (@%d)\n", fd, r.TellBits())
		eh, e8 := r.ReadBD()
		rd("extents_h", eh, e8)
		ew, e9 := r.ReadBD()
		rd("extents_w", ew, e9)
		tv, e10 := r.ReadTV(doc.codepage)
		if e10 != nil {
			t.Fatalf("text: %v", e10)
		}
		fmt.Printf("  text = %q (@%d)\n", tv, r.TellBits())
		ls, e11 := r.ReadBS()
		if e11 != nil {
			t.Fatalf("ls style: %v", e11)
		}
		fmt.Printf("  linespace_style = %d (@%d)\n", ls, r.TellBits())
	}
}
