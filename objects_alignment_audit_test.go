// 本文件为值级对齐审计测试：将我们解码的内部对象字段与 dwgread -O JSON
// gold 的标量字段逐键对比，量化"字段级对齐率"（区别于谓词型检查项的
// TestInternalObjectsGold）。TestInternalObjectAudit 与 TestEntityAudit
// 为 27 样本全量硬门禁（批次 0 九样本+ex2010、批次 I 扩容样本自批次 K
// 起全部 strict）：缺失+不符须为 0；gold 历史噪声按 goldNoiseKeys 清单
// 注释豁免，不计入门禁基数。
package cad

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/unitedrhino/go-cad/internal/entity"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// flattenGold 展开 gold 对象的嵌套键（lines[0].offset 等）为扁平键值。
func flattenGold(prefix string, m map[string]any, out *map[string]any) {
	for k, v := range m {
		switch vv := v.(type) {
		case map[string]any:
			flattenGold(prefix+k+".", vv, out)
		case []any:
			for i, e := range vv {
				if em, ok := e.(map[string]any); ok {
					flattenGold(fmt.Sprintf("%s%s[%d].", prefix, k, i), em, out)
				}
			}
		default:
			(*out)[prefix+k] = v
		}
	}
}

// floatFormatF 模拟 LibreDWG 输出的浮点格式化：gold JSON 中 BD 系字段
// 经 %.14f+去零输出，BT 系（thickness 等）经 %g（6 位有效）输出——
// 极小值（如 1.08e-14）会失真为 1e-14、91.75472353226601 截为
// 91.7547（dwgread DXF 全精度输出交叉验证两者为同一内存值），对照时
// 按更粗的 %g 归一两侧。
func floatFormatF(v float64) string {
	return fmt.Sprintf("%.6g", v)
}

// auditValueMatch gold 值与解码值等价判定（数值容差、%f 格式化归一、
// bool ↔ 0/1、类名字段剥离 ACDB/ACAD_ 前缀）。
func auditValueMatch(key string, got any, want any) bool {
	switch w := want.(type) {
	case float64:
		switch g := got.(type) {
		case float64:
			return math.Abs(g-w) < 1e-6 || floatFormatF(g) == floatFormatF(w)
		case int64:
			return math.Abs(float64(g)-w) < 1e-6
		case bool:
			return (w != 0) == g
		}
	case string:
		if g, ok := got.(string); ok {
			if g == w {
				return true
			}
			if strings.HasSuffix(key, "object") {
				norm := func(s string) string {
					s = strings.TrimPrefix(s, "ACDB")
					s = strings.TrimPrefix(s, "ACAD_")
					return s
				}
				// R2000 类表的 DXF 名形态：ACDB_前缀 + _CLASS 后缀
				// （fzw 的 ACDB_MTEXTOBJECTCONTEXTDATA_CLASS 实证）
				norm2 := func(s string) string {
					s = strings.TrimPrefix(s, "ACDB_")
					return strings.TrimSuffix(s, "_CLASS")
				}
				return norm(g) == norm(w) || norm2(g) == norm2(w) ||
					norm2(strings.TrimPrefix(g, "ACDB")) == norm2(strings.TrimPrefix(w, "ACDB"))
			}
		}
	case bool:
		if g, ok := got.(bool); ok {
			return g == w
		}
		if g, ok := got.(int64); ok {
			return (w && g == 1) || (!w && g == 0)
		}
	}
	return false
}

// TestInternalObjectAudit 内部对象值级对齐审计（批次 0 硬门禁+批次 I 进度追踪）。
func TestInternalObjectAudit(t *testing.T) { runAlignmentAudit(t, false) }

// TestEntityAudit 实体值级对齐审计（批次 0 硬门禁+批次 I 进度追踪）。
func TestEntityAudit(t *testing.T) { runAlignmentAudit(t, true) }

// goldNoiseKeys gold 历史噪声豁免清单（样本:类型:句柄:键）。
// ex2013/ex2018 的 REGION/3DSOLID h=374/893/737 均为 AcDs blob 场景
// （has_ds_data=1，ACIS 数据存于 AcDs 流而非对象主体）。gold JSON 由
// 旧版 dwgread 生成，误读 acis_empty=0 并据此错位解出 version=2 与
// revision_bytes 零串；当前版 dwgread（0.14）与本实现一致输出
// acis_empty=1、放弃错位尾部段（h=893 的 wires 段 LibreDWG 自身亦报
// ERROR）。噪声键逐条实证豁免，不参与门禁计数。
// fzw 的 BLOCKSTRETCHACTION.angle_offset 为 gold JSON 序列化截断：
// 当前版 dwgread 的 v9 trace 与本实现逐位一致（同解出
// 6.832569235718229e+275，含相同的 is_zombie 类错位读法与 handle 流
// OVERSHOOT），但其 JSON 输出对 BD 用 %.14f 缓冲序列化，276 位整数超出
// 缓冲截为前 254 位，重新解析即失真为 ~6.8e+253（两值前 14 位尾数逐位
// 一致实证同源）。
var goldNoiseKeys = map[string]bool{
	"ex2013:REGION:374:acis_empty":     true,
	"ex2013:REGION:374:version":        true,
	"ex2013:REGION:893:acis_empty":     true,
	"ex2013:REGION:893:version":        true,
	"ex2013:REGION:893:revision_bytes": true,
	"ex2013:3DSOLID:737:acis_empty":    true,
	"ex2013:3DSOLID:737:version":       true,
	"ex2018:REGION:893:revision_bytes": true,

	"c_corpus-s_fzw:BLOCKSTRETCHACTION:7277793:angle_offset": true,

	// vlvchi 批次 C：dwgread JSON 对 U+00B1（±）输出 \U+XXXX 转义而
	// 其余 BMP 文本（含中文）直出 UTF-8，转义与原文语义等价（±0.00），
	// 属 gold 序列化表示差异而非解码分歧。
	"c_corpus-hunt2_vlvchi:ATTRIB:399809:text_value": true,
	"c_corpus-hunt2_vlvchi:ATTRIB:399867:text_value": true,
	"c_corpus-hunt2_vlvchi:ATTRIB:400063:text_value": true,
	"c_corpus-hunt2_vlvchi:ATTRIB:400276:text_value": true,
	"c_corpus-hunt2_vlvchi:ATTRIB:400343:text_value": true,
	"c_corpus-hunt2_vlvchi:ATTRIB:400545:text_value": true,

	// uhengshenhua 批次 H（两类 gold 历史噪声，均经 dwgread -v9 trace 实证）：
	// 1) BLOCKGRIPLOCATIONCOMPONENT.evalexpr.value.num40 ×15 与 fzw
	//    angle_offset 同为 gold JSON 序列化截断——当前版 dwgread trace
	//    输出 1.79769e+307 与本实现逐位同源，其 JSON 对 BD 用 %.14f 缓冲
	//    序列化，309 位整数展开超出缓冲截为前 254 位，重新解析即失真为
	//    ~1.7976931348623e+253（尾数前 14 位一致实证同源）。
	// 2) BLOCKSTRETCHACTION h=17962/17967 共 3 键：该类版本 0x481 被
	//    LibreDWG 判 Unstable，gold 与当前版 dwgread 均走 unknown_bits
	//    解码（gold 对象自带 num_unknown_bits=672/688 实证），其
	//    angle_offset/action_offset_x 为 unknown 路径占位默认 0 而非
	//    位流真值；本实现按 spec 完整解码保留真实值，不回退。
}

// init 注册 uhengshenhua 批次 H 的成组豁免键（同构对象逐句柄展开）。
func init() {
	for _, h := range []int{1784, 1785, 12672, 12673, 12677, 12678, 17960, 17961, 17965, 17966, 42457, 42458, 52038, 52039, 17962} {
		goldNoiseKeys[fmt.Sprintf("c_corpus-hunt2_uhengshenhua:BLOCKGRIPLOCATIONCOMPONENT:%d:evalexpr.value.num40", h)] = true
	}
	for _, e := range [][2]any{{17962, "angle_offset"}, {17962, "action_offset_x"}, {17967, "action_offset_x"}} {
		goldNoiseKeys[fmt.Sprintf("c_corpus-hunt2_uhengshenhua:BLOCKSTRETCHACTION:%d:%s", e[0], e[1])] = true
	}
}

// isGoldNoise 判断（样本:类型:句柄:键）是否命中豁免清单。
func isGoldNoise(sample, name string, h uint64, k string) bool {
	return goldNoiseKeys[fmt.Sprintf("%s:%s:%d:%s", sample, name, h, k)]
}

// auditSample 审计样本：alias 为 gold JSON 别名（gold 位于 /tmp/<alias>.json，
// 不入库，按 README 流程本地生成）；strict 标记是否硬门禁。
type auditSample struct {
	alias  string
	strict bool
}

// resolveSamplePath 由 gold 别名推导 test-data 相对路径。三条规则：
// ex*→example_*.dwg、sample*→sample_*.dwg（批次 0 根目录样本）；
// c_<目录>_<文件名>→<目录>/<文件名>.dwg（批次 I 语料扩容样本，文件名
// 保留原始大小写，目录取别名首个下划线前的版本段）。
func resolveSamplePath(alias string) string {
	base := alias + ".json"
	if len(base) > 4 && base[:2] == "ex" {
		return "example_" + base[2:len(base)-5] + ".dwg"
	}
	if len(base) > 10 && base[:6] == "sample" {
		return "sample_" + base[6:len(base)-5] + ".dwg"
	}
	if len(alias) > 2 && alias[:2] == "c_" {
		if i := strings.Index(alias[2:], "_"); i >= 0 {
			return alias[2:2+i] + "/" + alias[3+i:] + ".dwg"
		}
	}
	return ""
}

// runAlignmentAudit 值级对齐审计（批次 0 十样本硬门禁+批次 I 扩容样本矩阵）。
// includeEntities 控制是否纳入实体对象（gold "entity" 键）。
func runAlignmentAudit(t *testing.T, includeEntities bool) {
	dir := os.Getenv("CAD_LIBREDWG_DATA")
	if dir == "" {
		dir = "/tmp/libredwg/test/test-data"
	}
	// 批次 0：根目录 example/sample 系，100% 硬门禁。
	// 批次 I：r14/2000~2018 目录语料扩容（稀有类+命名代表样本），
	// 先进度追踪，全部达标后升硬门禁。
	// 批次 K 收敛完成：27 样本全部升为硬门禁（对齐率 100%，
	// 缺失+不符须为 0；gold 历史噪声按 goldNoiseKeys 豁免）。
	candidates := []auditSample{
		{"exr13", true}, {"exr14", true}, {"ex2000", true}, {"ex2004", true},
		{"ex2007", true}, {"ex2010", true}, {"ex2013", true}, {"ex2018", true},
		{"sample2000", true}, {"sample2018", true},
		{"c_r14_Leader", true}, {"c_2004_Leader", true}, {"c_2007_Leader", true},
		{"c_2018_Leader", true},
		{"c_2004_Underlay", true}, {"c_2000_TS1", true}, {"c_2000_PolyLine2D", true},
		{"c_2004_Surface", true}, {"c_2004_HatchG", true}, {"c_2007_ATMOS-DC22S", true},
		{"c_2010_gh209_1", true}, {"c_2013_gh109_1", true}, {"c_2018_Dynblocks", true},
		{"c_2000_entities-2d", true}, {"c_2000_entities-3d", true},
		{"c_2000_Constraints", true}, {"c_2018_LiveSection1", true},
		// 批次 S：真实语料狩猎样本（LibreDWG issue 附件 + Autodesk 官方
		// sample，均放 corpus-s/ 目录；命中场景见各注释）。
		// fzw：LibreDWG issue #428 附件，R2000 + ANSI_936 中文码页（505 个
		// 中文 TEXT/BLOCK 名真实解码，EED/码页链路修复的验证基准）；
		// multileaders：Autodesk 官方 MLEADER 样本（36 实体，含 3 个 blk
		// 内容分支——blk 导出路径的验证基准）；
		// skylight/conference：Autodesk 官方可视化样本（R2010，17 个非光度
		// LIGHT 真实基线段 + 3DSOLID 块内材质；光度分支仍无真实样本，
		// 见 entities_light_test）。
		// 批次 X 收敛完成：四样本全部升为硬门禁（fzw 对象级仅存
		// BLOCKSTRETCHACTION.angle_offset 一处 gold 序列化截断噪声，按
		// goldNoiseKeys 豁免；其余 31 样本 100% 对齐）。
		{"c_corpus-s_fzw", true}, {"c_corpus-s_multileaders", true},
		{"c_corpus-s_skylight", true}, {"c_corpus-s_conference", true},
		// 批次 C：第二轮案例狩猎（GitHub 国产 CAD 生态仓库 lele-dawang/
		// cad-portfolio 等），真实中文工程图纸端到端（Text 值含中文工程
		// 注记与 MTEXT 宋体 c134 码页控制码，补 fzw 之外的 R2004/2007/
		// 2018 中文场景）；光度 LIGHT/pre-R13/PROXY_OBJECT 三目标场景
		// 本轮仍未命中，无对应样本。
		// 批次 F 收敛：vlvchi/chuandongzhou/sunmingle_danti 三样本升
		// strict（chuandongzhou 为 R2007 TEXT 字符串区路径 + ATTRIB 空
		// 串首元素修复，sunmingle 为 R2018 MTEXT 分栏尾段 spec 字段序
		// 修复）；shuichang_zongpingmian 无可比字段（无对象键值对），
		// 暂留进度追踪。
		{"c_corpus-hunt2_chuandongzhou", true},
		{"c_corpus-hunt2_shuichang_zongpingmian", false},
		{"c_corpus-hunt2_vlvchi", true},
		{"c_corpus-hunt2_sunmingle_danti", true},
		// 批次 H：用户上传真实工程图（R2018 智能化深化设计图，108,850
		// 对象/96,577 实体/1721 个 HATCH），13 个样条边界渐变 HATCH 的
		// 切线越位修复验证基准；样本入 corpus-hunt2/（同 vlvchi 目录口径）。
		// 批次 H 收敛：实体 2504197/对象 55605 键全对齐（唯一大样本
		// 百万键级 100%），仅存 18 处 gold 历史噪声按 goldNoiseKeys 豁免。
		{"c_corpus-hunt2_uhengshenhua", true},
	}
	for _, cand := range candidates {
		c := cand.alias
		jsonPath := "/tmp/" + c + ".json"
		raw, err := os.ReadFile(jsonPath)
		if err != nil {
			t.Logf("%s: gold 不可用，跳过", c)
			continue
		}
		sample := resolveSamplePath(c)
		if sample == "" {
			t.Errorf("%s: 无法从别名推导样本路径", c)
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, sample))
		if err != nil {
			t.Logf("%s: 样本不可用，跳过", c)
			continue
		}
		doc, err := Parse(data)
		if err != nil {
			t.Errorf("%s: 解析失败 %v", c, err)
			continue
		}
		objs := doc.InternalObjects()
		entObjs := func(h uint64) any { return doc.EntityByHandle(h) }
		var gold struct {
			Objects []map[string]any `json:"OBJECTS"`
		}
		// dwgread 对非数值 BD 输出裸 nan/-nan/NaN 字面量，标准 json
		// 不支持，与 ParseJSON 同口径在值位置预处理归一为 null。
		if bytes.Contains(raw, []byte("nan")) {
			re := regexp.MustCompile(`([:\[,]\s*)-?[nN]a[nN]`)
			raw = re.ReplaceAll(raw, []byte("${1}null"))
		}
		if err := json.Unmarshal(raw, &gold); err != nil {
			t.Errorf("%s: gold 解析失败 %v", c, err)
			continue
		}
		var total, match, miss, diff int
		missKeys := map[string]int{}
		for _, o := range gold.Objects {
			name, _ := o["object"].(string)
			isEntity := false
			if ename, ok := o["entity"].(string); ok && ename != "" {
				isEntity = true
				name = ename
			}
			if name == "" {
				continue
			}
			if isEntity && !includeEntities {
				continue
			}
			if !isEntity && includeEntities {
				continue
			}
			if k, ok := o["handle"]; !ok || k == nil {
				continue
			}
			hv, _ := o["handle"].([]any)
			if len(hv) == 0 {
				continue
			}
			h := uint64(hv[len(hv)-1].(float64))
			var getVal func(k string) any
			if isEntity {
				ent := entObjs(h)
				if ent == nil {
					continue
				}
				getVal = func(k string) any { return entity.EntityField(ent, k) }
			} else {
				g, ok := objs[h]
				if !ok {
					continue
				}
				getVal = func(k string) any { return g.FieldPath(k) }
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
				// 豁免键不计入门禁基数（对象级同样适用：fzw angle_offset
				// 为 gold JSON 序列化截断，属对象字段）
				if isGoldNoise(c, name, h, k) {
					total--
					continue
				}
				got := getVal(k)
				if got == nil {
					miss++
					if os.Getenv("CAD_AUDIT_MISS") != "" {
						missKeys[name+"."+k]++
					}
					continue
				}
				if auditValueMatch(k, got, want) {
					match++
				} else {
					diff++
					if os.Getenv("CAD_AUDIT_DIFF") != "" && diff <= 40 {
						t.Logf("  [%s] %s h=%d %s: got %v want %v", c, name, h, k, got, want)
					}
				}
			}
			_ = name
		}
		if os.Getenv("CAD_AUDIT_MISS") != "" {
			type kv struct {
				k string
				n int
			}
			mk := make([]kv, 0, len(missKeys))
			for k, n := range missKeys {
				mk = append(mk, kv{k, n})
			}
			sort.Slice(mk, func(i, j int) bool { return mk[i].n > mk[j].n })
			for i, e := range mk {
				if i >= 20 {
					break
				}
				t.Logf("[%s] 缺失 %s ×%d", c, e.k, e.n)
			}
		}
		if total == 0 {
			t.Logf("%s: 无可比字段", c)
			continue
		}
		t.Logf("%s: 值级对齐 %d/%d = %.1f%%（缺失 %d，不符 %d）",
			c, match, total, float64(match)*100/float64(total), miss, diff)
		if miss+diff > 0 {
			if cand.strict {
				t.Errorf("%s: 实体值级门禁未达标（缺失 %d，不符 %d，豁免清单外须清零）",
					c, miss, diff)
			} else {
				// 批次 I 扩容样本：进度追踪口径，缺口按类×键聚合后由后续批次收敛。
				t.Logf("[批次I] %s: 进度口径未达标（缺失 %d，不符 %d），待后续修复批次收敛",
					c, miss, diff)
			}
		}
	}
	sort.Strings(nil)
}
