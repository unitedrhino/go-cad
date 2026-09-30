// objects.go 实现 DWG 对象层：对象索引构建（buildObjectIndex）与 R2010+
// 重复句柄候选消解（selectBestDuplicateHandles）。记录模型/对象图解码/
// 记录头定位/类型码命名等最底层记录原语已上提 internal/objrec（打破
// entity↔object 循环依赖），本文件保留依赖实体扫描的对象层逻辑。
package object

import (
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/entity"
	"github.com/unitedrhino/go-cad/internal/objrec"
	"math"
)

// buildObjectIndex 解析 AcDb:Handles 段，构建 handle→offset 对象索引。
func BuildObjectIndex(fileData []byte) ([]objrec.ObjectRef, error) {
	handlesData, err := container.LoadNamedSectionData(fileData, "AcDb:Handles")
	if err != nil {
		return nil, err
	}
	return objrec.ParseObjectMapHandles(handlesData)
}

// ---- R2010+ 重复句柄候选消解（对齐参考实现 permissive 路径） ----

// dedupeSelect 信息：单个候选记录的解析摘要。
type dedupeSelect struct {
	ParsedOK      bool
	typeCode      uint16
	dataSize      uint32
	isEntity      bool
	decodedHandle uint64 // 实体公共头解码出的句柄（0=未解出）
	hasDecoded    bool
}

// 重复句柄候选评分权重：类型码非零 +32、数据非空 +8、实体类 +16/对象类
// -8、公共头解码句柄与对象图句柄一致 +10000（不一致且非零 -5000）、
// LAYER 表记录与相邻同类型候选连续 +12000/+6000。
const (
	dedupeWeightTypeCode   = 32
	dedupeWeightDataSize   = 8
	dedupeWeightIsEntity   = 16
	dedupeWeightNotEntity  = -8
	dedupeWeightHandleHit  = 10000
	dedupeWeightHandleMiss = -5000
	dedupeWeightLayerBoth  = 12000
	dedupeWeightLayerNear  = 6000
)

// layerTypeCode LAYER 表记录类型码（候选连续性加权的目标类型）。
const layerTypeCode = 0x33

// selectBestDuplicateHandles 对重复句柄的候选记录评分择优，输出保持原顺序。
func SelectBestDuplicateHandles(objectsData []byte, refs []objrec.ObjectRef, Ver container.DwgVersion, dynamicTypes map[uint16]string) []objrec.ObjectRef {
	grouped := map[uint64][]objrec.ObjectRef{}
	for _, R := range refs {
		grouped[R.Handle] = append(grouped[R.Handle], R)
	}
	type candKey struct {
		Handle uint64
		offset uint32
	}
	infos := map[candKey]dedupeSelect{}
	for _, cands := range grouped {
		if len(cands) < 2 {
			continue
		}
		for _, c := range cands {
			infos[candKey{c.Handle, c.Offset}] = InspectCandidate(objectsData, c, Ver, dynamicTypes)
		}
	}
	nearLayer := func(target uint64, offset uint32, cands []objrec.ObjectRef, self uint64) bool {
		for _, c := range cands {
			if c.Handle == self {
				continue
			}
			info, ok := infos[candKey{c.Handle, c.Offset}]
			if !ok || !info.ParsedOK || uint64(info.typeCode) != target {
				continue
			}
			diff := int64(c.Offset) - int64(offset)
			if diff < 0 {
				diff = -diff
			}
			if diff <= 256 {
				return true
			}
		}
		return false
	}
	selected := map[uint64]uint32{}
	for Handle, cands := range grouped {
		if len(cands) == 1 {
			selected[Handle] = cands[0].Offset
			continue
		}
		bestScore := math.MinInt32
		bestOffset := cands[len(cands)-1].Offset
		for _, c := range cands {
			info, ok := infos[candKey{c.Handle, c.Offset}]
			if !ok {
				continue
			}
			if !info.ParsedOK {
				if bestScore == math.MinInt32 {
					bestScore = math.MinInt32 / 4
				}
				continue
			}
			score := 0
			if info.typeCode != 0 {
				score += dedupeWeightTypeCode
			}
			if info.dataSize > 0 {
				score += dedupeWeightDataSize
			}
			if info.isEntity {
				score += dedupeWeightIsEntity
			} else {
				score += dedupeWeightNotEntity
			}
			if info.hasDecoded {
				if info.decodedHandle == c.Handle {
					score += dedupeWeightHandleHit
				} else if info.decodedHandle != 0 {
					score += dedupeWeightHandleMiss
				}
			}
			if info.typeCode == layerTypeCode {
				// LAYER 表连续性加分：与前/后一句柄的同类型候选相邻
				matching := 0
				if nearLayer(layerTypeCode, c.Offset, grouped[Handle-1], Handle) {
					matching++
				}
				if nearLayer(layerTypeCode, c.Offset, grouped[Handle+1], Handle) {
					matching++
				}
				switch matching {
				case 2:
					score += dedupeWeightLayerBoth
				case 1:
					score += dedupeWeightLayerNear
				}
			}
			if score > bestScore || (score == bestScore && c.Offset > bestOffset) {
				bestScore = score
				bestOffset = c.Offset
			}
		}
		selected[Handle] = bestOffset
	}
	out := make([]objrec.ObjectRef, 0, len(refs))
	seen := map[uint64]bool{}
	for _, R := range refs {
		if sel, ok := selected[R.Handle]; ok && sel == R.Offset && !seen[R.Handle] {
			out = append(out, R)
			seen[R.Handle] = true
		}
	}
	return out
}

// inspectCandidate 解析单个候选记录的摘要信息：类型码/数据量/实体归类，
// 实体类再解公共头验证句柄一致性（仅常见实体类型码）。
func InspectCandidate(objectsData []byte, c objrec.ObjectRef, Ver container.DwgVersion, dynamicTypes map[uint16]string) dedupeSelect {
	var info dedupeSelect
	rec, err := objrec.ParseObjectRecord(objectsData, c, Ver.R2010Plus())
	if err != nil {
		return info
	}
	h, err := objrec.ParseObjHeader(rec)
	if err != nil {
		return info
	}
	info.ParsedOK = true
	info.typeCode = h.TypeCode
	info.dataSize = rec.Size
	info.isEntity = objrec.IsEntityType(h.TypeCode, dynamicTypes)
	if info.isEntity {
		R := rec.BodyBitStream()
		R.SetBitPos(h.DataStartBit)
		if head, err := entity.ParseCommonEntityHeadR2013(R, rec.DataEndBit()); err == nil {
			info.decodedHandle = head.Handle
			info.hasDecoded = true
		}
	}
	return info
}
