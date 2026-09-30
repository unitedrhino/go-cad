// entities_image_audit_test.go IMAGE 专项值级审计：对九样本之外批 C 新增的
// Leader.dwg（r14~2018 各 5 实例）与 gh44-error.dwg（2013，3 实例）现场
// gold 做标量键对齐检查。gold 由 dwgread 现场生成（缺失时跳过，口径与
// TestDimensionEntityAudit 一致）：
//
//	/tmp/libredwg-build/dwgread -O JSON -o /tmp/gold_image_<v>.json \
//	    /tmp/libredwg/test/test-data/<v>/Leader.dwg   # v ∈ r14 2000 2004 2007 2010 2013 2018
//	/tmp/libredwg-build/dwgread -O JSON -o /tmp/gold_image_gh44.json \
//	    /tmp/libredwg/test/test-data/2013/gh44-error.dwg
package cad

import (
	"encoding/json"
	"github.com/unitedrhino/go-cad/internal/entity"
	"os"
	"regexp"
	"strings"
	"testing"
)

// nanTokenRe 宽松化 gh44-error 等错误样本 gold 中的非标准 JSON token
// （LibreDWG 对损坏字段输出裸 -nan/nan，标准解析器拒绝），统一替换为 null。
var nanTokenRe = regexp.MustCompile(`(:\s*)-?(?:nan|inf(?:inity)?)\b`)

// imageAuditCandidates IMAGE gold 对照清单：候选名 → (gold 路径, 样本路径)。
var imageAuditCandidates = []struct {
	name, gold, sample string
}{
	{"r14", "/tmp/gold_image_r14.json", "/tmp/libredwg/test/test-data/r14/Leader.dwg"},
	{"2000", "/tmp/gold_image_2000.json", "/tmp/libredwg/test/test-data/2000/Leader.dwg"},
	{"2004", "/tmp/gold_image_2004.json", "/tmp/libredwg/test/test-data/2004/Leader.dwg"},
	{"2007", "/tmp/gold_image_2007.json", "/tmp/libredwg/test/test-data/2007/Leader.dwg"},
	{"2010", "/tmp/gold_image_2010.json", "/tmp/libredwg/test/test-data/2010/Leader.dwg"},
	{"2013", "/tmp/gold_image_2013.json", "/tmp/libredwg/test/test-data/2013/Leader.dwg"},
	{"2018", "/tmp/gold_image_2018.json", "/tmp/libredwg/test/test-data/2018/Leader.dwg"},
	{"gh44", "/tmp/gold_image_gh44.json", "/tmp/libredwg/test/test-data/2013/gh44-error.dwg"},
}

// imageCoreKeys IMAGE 核心标量键：解码器主体字段的直接证据，必须全部命中；
// clip_mode 仅 R2010+ 存在，由核心集合单独断言。color.rgb 的 8 位 hex
// （gold 在 flag 置位时含 alpha 高字节）属公共 color 导出口径差异，
// 不属于 IMAGE 主体字段，不计入核心集。
var imageCoreKeys = []string{
	"class_version", "display_props", "clipping", "brightness",
	"contrast", "fade", "clip_boundary_type",
}

// TestImageEntityAudit IMAGE gold 值级审计：核心标量键缺失/不符计失败，
// 全键对齐率汇总输出（键过滤口径与 TestDimensionEntityAudit 一致）。
func TestImageEntityAudit(t *testing.T) {
	var gTotal, gMatch, gMiss, gDiff int
	coreChecked := 0
	for _, c := range imageAuditCandidates {
		raw, err := os.ReadFile(c.gold)
		if err != nil {
			t.Logf("%s: gold 不可用，跳过（%s）", c.name, c.gold)
			continue
		}
		data, err := os.ReadFile(c.sample)
		if err != nil {
			t.Logf("%s: 样本不可用，跳过（%s）", c.name, c.sample)
			continue
		}
		doc, err := Parse(data)
		if err != nil {
			t.Errorf("[%s] 解析失败 %v", c.name, err)
			continue
		}
		var gold struct {
			Objects []map[string]any `json:"OBJECTS"`
		}
		if err := json.Unmarshal(nanTokenRe.ReplaceAll(raw, []byte(`${1}null`)), &gold); err != nil {
			t.Errorf("[%s] gold 解析失败 %v", c.name, err)
			continue
		}
		var total, match, miss, diff int
		missKeys := map[string]int{}
		diffKeys := map[string]int{}
		for _, o := range gold.Objects {
			name, _ := o["entity"].(string)
			if name != "IMAGE" {
				continue
			}
			hv, _ := o["handle"].([]any)
			if len(hv) == 0 {
				continue
			}
			h := uint64(hv[len(hv)-1].(float64))
			ent := doc.EntityByHandle(h)
			if ent == nil {
				t.Errorf("  [%s] IMAGE h=%d 实体缺失（未解出）", c.name, h)
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
					// 公共键（bitsize/size/preview 等）允许缺失，仅计入汇总
					if isImageCoreKey(k) {
						miss++
						missKeys[k]++
					}
					continue
				}
				if auditValueMatch(k, got, want) {
					match++
				} else {
					diff++
					diffKeys[k]++
					if isImageCoreKey(k) {
						t.Errorf("  [%s] h=%d 核心键 %s 不符: got=%v want=%v", c.name, h, k, got, want)
					}
				}
			}
			// R2010+ 的 clip_mode 键单独核对（不在 gold 键集时跳过）
			if v, ok := flat["clip_mode"]; ok {
				coreChecked++
				if got := entity.EntityField(ent, "clip_mode"); got == nil || !auditValueMatch("clip_mode", got, v) {
					t.Errorf("  [%s] h=%d 核心键 clip_mode 不符: got=%v want=%v", c.name, h, got, v)
				}
			}
		}
		if total > 0 {
			t.Logf("[%s] IMAGE 对齐 %d/%d = %.1f%%（缺失 %d，不符 %d）%s",
				c.name, match, total, float64(match)*100/float64(total), miss, diff, formatKeyCounts(missKeys)+formatKeyCounts(diffKeys))
		} else {
			t.Logf("[%s] 无 IMAGE 实例", c.name)
		}
		gTotal += total
		gMatch += match
		gMiss += miss
		gDiff += diff
	}
	if gTotal == 0 {
		t.Skip("无可比 IMAGE 字段（gold 不可用）")
	}
	t.Logf("IMAGE 总计: 对齐 %d/%d = %.1f%%（核心缺失 %d，不符 %d，clip_mode 核对 %d）",
		gMatch, gTotal, float64(gMatch)*100/float64(gTotal), gMiss, gDiff, coreChecked)
}

// isImageCoreKey 判断是否 IMAGE 核心标量键（主体字段证据，缺失即失败）。
func isImageCoreKey(k string) bool {
	for _, c := range imageCoreKeys {
		if k == c {
			return true
		}
	}
	return false
}
