// roundtrip_all_test 全类型 round-trip 穷举：样本内每个内部对象
// 编码 → 重解码 → 逐字段比对，暴露 dwgwrite 编码方向的类型覆盖面。
// 实体经 TestAllObjectsRoundTrip 同矩阵纳入（encodeEntityR200x 位串
// 回放 + entityField 键级比对）。
package cad

import (
	"encoding/json"
	"fmt"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/drawing"
	"github.com/unitedrhino/go-cad/internal/entity"
	"github.com/unitedrhino/go-cad/internal/object"
	"github.com/unitedrhino/go-cad/internal/objrec"
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"github.com/unitedrhino/go-cad/internal/writer"
	"os"
	"sort"
	"strings"
	"testing"
)

// rtDebug 是否输出 [rt] 逐对象编码进度到 stderr（环境变量 CAD_RT_DEBUG
// 门控，缺省关闭避免穷举矩阵刷屏）。
var rtDebug = os.Getenv("CAD_RT_DEBUG") != ""

// TestAllObjectsRoundTrip 九样本矩阵的全类型穷举：每样本内所有
// 内部对象 编码→重解码→逐字段比对（encodeInternalObjectR2000），
// 外加全部实体（doc.entityByHandle）的位串回放编码→重解码→
// entityField 键级比对。R2000-R2007（内联 RL 语义）全类型适用；
// R2010+ 无内联 RL，按记录元数据重建（preBits 前导回放）。
func TestAllObjectsRoundTrip(t *testing.T) {
	cases := []struct {
		sample string
		Ver    container.DwgVersion
	}{
		{"example_2000.dwg", container.VerR2000},
		{"example_2004.dwg", container.VerR2004},
		{"example_2007.dwg", container.VerR2007},
		{"example_2013.dwg", container.VerR2013},
		{"example_2018.dwg", container.VerR2018},
	}
	for _, c := range cases {
		c := c
		t.Run(c.sample, func(t *testing.T) {
			roundTripAll(t, c.sample, c.Ver)
		})
	}
}

func roundTripAll(t *testing.T, sample string, ver container.DwgVersion) {
	dir := testsupport.LibredwgTestDataDir()
	data, err := os.ReadFile(dir + "/" + sample)
	if err != nil {
		t.Skipf("样本不可用: %v", err)
	}
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	bodySnaps := map[uint64][]byte{}
	if ver >= container.VerR2013 {
		objectsData, err0 := container.LoadNamedSectionData(data, "AcDb:AcDbObjects")
		if err0 != nil {
			t.Logf("bodySnap: AcDbObjects 加载失败: %v", err0)
		} else {
			index, err1 := object.BuildObjectIndex(data)
			if err1 != nil {
				t.Logf("bodySnap: 索引失败: %v", err1)
			} else {
				for _, ref := range index {
					if rec, err2 := objrec.ParseObjectRecord(objectsData, ref, true); err2 == nil {
						bodySnaps[ref.Handle] = rec.Body
					}
				}
				t.Logf("bodySnap: 快照数=%d", len(bodySnaps))
			}
		}
	}
	pass, fail, skip := 0, 0, 0
	fails := map[string]int{}
	for h, g := range doc.InternalObjs {
		if g == nil || g.Unknown {
			continue
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Logf("%s h=%d panic: %v", g.Name, h, r)
					fails[g.Name+"(panic)"]++
					fail++
				}
			}()
			if rtDebug {
				fmt.Fprintf(os.Stderr, "[rt] %s h=%d\n", g.Name, h)
			}
			tc64, _ := g.Field("type").(int64)
			typeCode := uint16(0)
			if tc64 > 0 && tc64 < 500 {
				typeCode = uint16(tc64)
			}
			body2, _, err := writer.EncodeInternalObjectR2000(g, typeCode)
			if err != nil {
				if strings.Contains(err.Error(), "缺少 headRawBits") {
					// 桥接路由：DICTIONARY 系对象走专用编码器
					//（objDictionary 结构，TestDictionaryRoundTrip 同链路）
					if strings.Contains(g.Name, "DICTIONARY") {
						if d, ok := doc.Dictionaries[h]; ok && d != nil {
							db2, derr := writer.EncodeDictionaryR2000(d, ver, false)
							if derr == nil {
								drec := &objrec.ObjectRecord{Body: db2, BodyBitOffset: 0, Size: uint32(len(db2))}
								if _, derr = object.DecodeDictionaryObjectFull(drec.BodyBitStream(), drec, ver, false, false); derr == nil {
									pass++
									return
								}
							}
							if derr != nil {
								t.Logf("%s h=%d 桥接失败: %v", g.Name, h, derr)
							}
						}
					}
					skip++ // 桥接不适用（如 UNKNOWN_OBJ）或桥接失败
				} else {
					fails[g.Name]++
				}
				return
			}
			rec2 := &objrec.ObjectRecord{Body: body2, BodyBitOffset: g.BodyBitOff, Size: g.SizeBytes,
				R2010Plus: g.R2010Plus, HandleSizeFieldBits: g.HSizeField, HandleStreamSizeBits: g.HssBits}
			if !g.R2010Plus {
				rec2.Size = uint32(len(body2))
			}
			rr := rec2.BodyBitStream()
			if g.R2010Plus {
				// 调用契约：调用方需定位到 dataStartBit（记录头之后）
				rr.SetBitPos(g.BodyBitOff + uint64(len(g.PreBits)))
			}
			// 位级对照：body2 与原 body 快照的首个差异位
			if snap := bodySnaps[h]; snap != nil {
				limit := len(body2)
				if len(snap) < limit {
					limit = len(snap)
				}
				firstDiff := -1
				for bi := 0; bi < limit; bi++ {
					if body2[bi] != snap[bi] {
						firstDiff = bi
						break
					}
				}
				t.Logf("%s h=%d diff@%d body2=%dB snap=%dB", g.Name, h, firstDiff, len(body2), len(snap))
			} else {
				t.Logf("%s h=%d snap缺失", g.Name, h)
			}
			g2, err := object.DecodeInternalObject(rr, rec2, ver, ver >= container.VerR2013, typeCode, g.Name, 30)
			if err != nil {
				t.Logf("%s h=%d 重解码失败: %v", g.Name, h, err)
				fails[g.Name+"(重解码)"]++
				fail++
				return
			}
			for _, f := range g.Fields {
				switch f.Key {
				case "object", "type", "size", "bitsize", "dxfname":
					continue
				}
				v2 := g2.Field(f.Key)
				if !testsupport.AnyRoundTripEqual(f.Val, v2) && !testsupport.AnyEqual(f.Val, v2) {
					t.Logf("%s h=%d 字段 %s: %v != %v", g.Name, h, f.Key, f.Val, v2)
					fails[g.Name+"(字段)"]++
					fail++
					return
				}
			}
			pass++
		}()
	}
	keys := make([]string, 0, len(fails))
	for k := range fails {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Logf("未通过 %s ×%d", k, fails[k])
	}
	t.Logf("全类型 round-trip: pass=%d fail=%d skip=%d", pass, fail, skip)

	roundTripAllEntities(t, doc, data, sample, ver, bodySnaps)
}

// roundTripAllEntities 实体纳入矩阵：doc.entityByHandle 全部句柄逐个
// encodeEntityR200x 回放 → 重建 objectRecord 重解码 → entityField 键级
// 比对（键集取自 gold JSON 展平键，过滤口径与 TestEntityAuditDump 一致；
// 首次解码 got==nil 的键视为未建模跳过）。位级诊断复用 bodySnap 快照。
// gold JSON 缺失时降级为仅回放+重解码验证（不计失败，log 说明）。
func roundTripAllEntities(t *testing.T, doc *Document, data []byte, sample string, ver container.DwgVersion, bodySnaps map[uint64][]byte) {
	goldFlats := loadGoldEntityFlats(sample)
	if goldFlats == nil {
		t.Logf("gold JSON 缺失，实体字段级比对降级为回放+重解码验证")
	}
	// 重解码端按版本重建动态类名表（≥500 类型码实体需要）
	dynamicTypes := map[uint16]string{}
	if ver == container.VerR2000 || ver == container.VerR14 || ver == container.VerR13 {
		dynamicTypes, _ = doc.LoadR2000Classes(data)
	} else {
		dynamicTypes, _ = doc.LoadDynamicTypes(data)
	}
	drawing.EnsureFixedEntityTypes(dynamicTypes)

	pass, fail := 0, 0
	fails := map[string]int{}
	for h, e1 := range doc.ByHandle {
		if e1 == nil {
			continue
		}
		b1 := entity.EntityBase(e1)
		if b1 == nil {
			continue
		}
		typeName := b1.TypeName
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Logf("%s h=%d panic: %v", typeName, h, r)
					fails[typeName+"(panic)"]++
					fail++
				}
			}()
			body2, _, err := writer.EncodeEntityR200x(e1, ver)
			if err != nil {
				t.Logf("%s h=%d 回放编码失败: %v (pre=%d head=%d raw=%d objSizeBit=%d recSize=%d r2010=%v)",
					typeName, h, err, len(b1.PreBits), len(b1.HeadRawBits), len(b1.RawHandleBits), b1.ObjSizeBit, b1.RecSize, b1.R2010Plus)
				fails[typeName+"(编码)"]++
				fail++
				return
			}
			// 位级诊断：回放 body 与源 body 快照的首个差异位
			if snap := bodySnaps[h]; snap != nil {
				limit := len(body2)
				if len(snap) < limit {
					limit = len(snap)
				}
				firstDiff := -1
				for bi := 0; bi < limit; bi++ {
					if body2[bi] != snap[bi] {
						firstDiff = bi
						break
					}
				}
				t.Logf("%s h=%d diff@%d body2=%dB snap=%dB", typeName, h, firstDiff, len(body2), len(snap))
			}
			// 重建记录并重解码（坐标系与源一致，走标准实体入口）
			rec2 := &objrec.ObjectRecord{Body: body2, BodyBitOffset: b1.BodyBitOff, Size: b1.SizeBytes,
				R2010Plus: b1.R2010Plus, HandleSizeFieldBits: b1.HSizeField, HandleStreamSizeBits: b1.HssBits}
			h2, err := objrec.ParseObjHeader(rec2)
			if err != nil {
				t.Logf("%s h=%d 重解码记录头失败: %v", typeName, h, err)
				fails[typeName+"(重解码)"]++
				fail++
				return
			}
			rr := rec2.BodyBitStream()
			rr.SetBitPos(h2.DataStartBit)
			var e2 any
			if drawing.IsVersionedEntityKind(typeName) {
				e2, err = drawing.DecodeVersionedEntity(rr, h2, h, typeName, ver)
			} else {
				e2, err = entity.DecodeEntityFieldsVer(rr, h2, h, rec2.Size, typeName, b1.TypeCode, ver, doc.Codepage, dynamicTypes, doc.LightingUnits)
			}
			if err != nil {
				t.Logf("%s h=%d 重解码失败: %v", typeName, h, err)
				fails[typeName+"(重解码)"]++
				fail++
				return
			}
			// entityField 键级比对：键集来自 gold 展平键（got==nil 跳过）
			for k, want := range goldFlats[h] {
				v1 := entity.EntityField(e1, k)
				if v1 == nil {
					continue
				}
				v2 := entity.EntityField(e2, k)
				if !entity.EntityValueEqual(v1, v2) {
					t.Logf("%s h=%d 字段 %s: %v != %v (gold %v)", typeName, h, k, v2, v1, want)
					fails[typeName+"(字段)"]++
					fail++
					return
				}
			}
			pass++
		}()
	}
	keys := make([]string, 0, len(fails))
	for k := range fails {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Logf("实体未通过 %s ×%d", k, fails[k])
	}
	t.Logf("实体 round-trip: pass=%d fail=%d", pass, fail)
}

// loadGoldEntityFlats 加载样本对应的 dwgread gold JSON，展平实体对象键
// （example_2000.dwg → /tmp/ex2000.json；路径经 libredwgGoldJSONPath
// 统一解析，可由 CAD_LIBREDWG_JSON/CAD_GOLD_JSON_DIR 覆盖），
// 键过滤与 TestEntityAuditDump 一致：句柄后缀键、index、reactors、
// dxfname、_subclass、eed、unknown_bits 系跳过，仅保留标量键。
// 按 gold handle 分桶返回；样本 JSON 不存在时返回 nil。
func loadGoldEntityFlats(sample string) map[uint64]map[string]any {
	base := strings.TrimSuffix(sample, ".dwg")
	jsonPath := testsupport.LibredwgGoldJSONPath(strings.TrimPrefix(base, "example_"))
	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		return nil
	}
	var gold struct {
		Objects []map[string]any `json:"OBJECTS"`
	}
	if err := json.Unmarshal(raw, &gold); err != nil {
		return nil
	}
	out := map[uint64]map[string]any{}
	for _, o := range gold.Objects {
		if ename, ok := o["entity"].(string); !ok || ename == "" {
			continue
		}
		hv, _ := o["handle"].([]any)
		if len(hv) == 0 {
			continue
		}
		h := uint64(hv[len(hv)-1].(float64))
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
			if out[h] == nil {
				out[h] = map[string]any{}
			}
			out[h][k] = want
		}
	}
	return out
}
