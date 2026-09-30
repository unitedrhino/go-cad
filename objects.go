// objects.go 实现 DWG 数据库的对象索引与对象记录头解析：
// AcDb:Handles 段的 handle→offset 映射（模块化差分编码），以及对象记录的
// 尺寸前缀与 R2010+ 类型码前缀定位。
//
// 模块划分：记录模型（objectRecord/objHeader）、对象图解析
// （parseObjectMapHandles 与模块化字符解码）、记录头定位
// （parseObjectRecord）、类型码命名（objTypeCode/entityTypeName）、
// R2010+ 重复句柄候选消解（selectBestDuplicateHandles）。
package cad

import (
	"encoding/binary"
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"math"
)

// objectRef 对象图条目：句柄与对象记录在 AcDb:AcDbObjects 段内的偏移。
type objectRef struct {
	handle uint64
	offset uint32
}

// objectRecord 对象记录：MS size 字段之后的 body 位级视图。
// 位偏移均为 body 内局部位；bodyBitOffset 为 MS 结束处在当前字节内的位偏移
// （0-7，MS 按位流读取未必字节对齐），body 内所有定位都基于它。
type objectRecord struct {
	offset        uint32
	size          uint32 // MS 字段声明的数据大小（不含 CRC 与 R2010+ 的 UMC 字段）
	body          []byte
	bodyBitOffset uint64
	// R2010+ 专用：MS size 不含尾部 handle-stream-size 字段，
	// 数据结束位（局部）= bodyBitOffset + handleSizeFieldBits + size*8 - handleStreamSizeBits
	r2010Plus            bool
	handleStreamSizeBits uint32
	handleSizeFieldBits  uint32
}

// bodyBitStream 返回定位到 MS 结束位的位读取游标。
func (rec *objectRecord) bodyBitStream() *bitstream.BitStream {
	r := bitstream.NewBitStream(rec.body)
	r.SetBitPos(rec.bodyBitOffset)
	return r
}

// dataEndBit 对象数据区结束局部位（= handle 流起点，即参考实现的 obj_size 语义）。
func (rec *objectRecord) dataEndBit() uint64 {
	base := rec.bodyBitOffset
	if rec.r2010Plus {
		base += uint64(rec.handleSizeFieldBits)
	}
	return base + uint64(rec.size)*8 - uint64(rec.handleStreamSizeBits)
}

// objHeader 对象头：类型码 + 记录定位信息。
type objHeader struct {
	rec          *objectRecord
	typeCode     uint16
	dataStartBit uint64 // 实体/对象数据区起始绝对位（类型码之后）
	handle       uint64 // 记录声明的主句柄（多数对象在数据区重述，以此兜底）
}

// parseObjHeader 解析类型码（body 局部偏移）：R2010+ 布局为 [UMC
// handle-stream-size][OT 类型码]；更早版本为 [BS 类型码]。个别记录以
// R2010+ 布局混存于早期版本，BS 解出 0 时按该布局重试。
func parseObjHeader(rec *objectRecord) (objHeader, error) {
	h := objHeader{rec: rec}
	r := rec.bodyBitStream()
	if rec.r2010Plus {
		if _, err := r.ReadUMC(); err != nil {
			return h, err
		}
		ot, err := r.ReadOT()
		if err != nil {
			return h, err
		}
		if ot == 0 {
			return h, fmt.Errorf("cad: 对象类型码为 0（offset %d）", rec.offset)
		}
		h.typeCode = ot
		h.dataStartBit = r.TellBits()
		return h, nil
	}
	tc, err := r.ReadBS()
	if err != nil {
		return h, err
	}
	if tc != 0 {
		h.typeCode = tc
		h.dataStartBit = r.TellBits()
		return h, nil
	}
	// 个别记录以 R2010+ 布局存储，按该布局重试
	r2 := rec.bodyBitStream()
	if _, err := r2.ReadUMC(); err == nil {
		if ot, err := r2.ReadOT(); err == nil && ot != 0 {
			h.typeCode = ot
			h.dataStartBit = r2.TellBits()
			return h, nil
		}
	}
	return h, fmt.Errorf("cad: 对象类型码为 0（offset %d）", rec.offset)
}

// buildObjectIndex 解析 AcDb:Handles 段，构建 handle→offset 对象索引。
func buildObjectIndex(fileData []byte) ([]objectRef, error) {
	handlesData, err := loadNamedSectionData(fileData, "AcDb:Handles")
	if err != nil {
		return nil, err
	}
	return parseObjectMapHandles(handlesData)
}

// objectMapChunk 对象图单块的解码产出：块内 (handle, offset) 增量对。
type objectMapChunk struct {
	entries []objectRef
}

// parseObjectMapHandles 解析对象图流：循环读取 BE u16 块大小；块内为
// (handle UMC 增量, offset MC 增量) 对；块尾 2 字节 CRC；大小为 2 的块为
// 终止块。宽容模式下每块差分从 0 重新累计（对齐参考实现 permissive 行为），
// 损坏的差分项（负句柄/越界偏移）回滚累计值并跳过。
func parseObjectMapHandles(data []byte) ([]objectRef, error) {
	var objects []objectRef
	pos := 0
	for {
		if len(data)-pos < 2 {
			break
		}
		chunkSize := int(binary.BigEndian.Uint16(data[pos:]))
		pos += 2
		if chunkSize == 2 {
			break // 终止块
		}
		if chunkSize < 2 || len(data)-pos < chunkSize-2 {
			return nil, fmt.Errorf("cad: 对象图块大小非法 %d (dataLen=%d pos=%d)", chunkSize, len(data), pos)
		}
		chunk, err := decodeObjectMapChunk(data[pos : pos+chunkSize-2])
		if err != nil {
			return nil, err
		}
		objects = append(objects, chunk.entries...)
		pos += chunkSize - 2
		if len(data)-pos < 2 {
			break
		}
		pos += 2 // 块 CRC
	}
	return objects, nil
}

// decodeObjectMapChunk 解码对象图单块：从 0 起累计句柄/偏移差分，
// 非法差分项回滚跳过。
func decodeObjectMapChunk(chunk []byte) (objectMapChunk, error) {
	var out objectMapChunk
	var lastHandle, lastOffset int64
	pos := 0
	for pos < len(chunk) {
		prevHandle, prevOffset := lastHandle, lastOffset
		dHandle, err := readUnsignedModularChar(chunk, &pos)
		if err != nil {
			return out, err
		}
		dOffset, err := readModularChar(chunk, &pos)
		if err != nil {
			return out, err
		}
		lastHandle += dHandle
		lastOffset += dOffset
		if lastHandle < 0 || lastOffset < 0 || lastOffset > 0xFFFFFFFF {
			lastHandle, lastOffset = prevHandle, prevOffset
			continue
		}
		out.entries = append(out.entries, objectRef{handle: uint64(lastHandle), offset: uint32(lastOffset)})
	}
	return out, nil
}

// readUnsignedModularChar 无符号模块化字符（handle 增量专用；ODA 规范：
// 句柄恒为递增，无符号位）：7 位一组小端累加，最高位为续组标志，至多 5 组。
func readUnsignedModularChar(data []byte, pos *int) (int64, error) {
	var value int64
	for shift := uint(0); ; shift += 7 {
		if *pos >= len(data) {
			return 0, bitstream.ErrUnexpectedEOF
		}
		b := data[*pos]
		*pos++
		value |= int64(b&0x7F) << shift
		if b&0x80 == 0 {
			return value, nil
		}
		if shift >= 28 {
			return value, nil
		}
	}
}

// readModularChar 有符号模块化字符（offset 增量）：7 位一组小端累加，
// 终止字节的 0x40 位为符号（清位后取值、按位取负），至多 4 组。
func readModularChar(data []byte, pos *int) (int64, error) {
	var value int64
	for shift := uint(0); shift < 28; shift += 7 {
		if *pos >= len(data) {
			return 0, bitstream.ErrUnexpectedEOF
		}
		b := data[*pos]
		*pos++
		if b&0x80 == 0 {
			negative := b&0x40 != 0
			if negative {
				b &= 0xBF
			}
			value |= int64(b) << shift
			if negative {
				return -value, nil
			}
			return value, nil
		}
		value |= int64(b&0x7F) << shift
	}
	return value, nil
}

// parseObjectRecord 解析对象记录头。R2010+ 记录的 MS size 不含尾部的
// handle-stream-size 字段宽度，需探针测量后扩展 body。
func parseObjectRecord(objectsData []byte, ref objectRef, r2010Plus bool) (*objectRecord, error) {
	offset := int(ref.offset)
	if offset >= len(objectsData) {
		return nil, fmt.Errorf("cad: 对象偏移 %d 超出数据段", offset)
	}
	r := bitstream.NewBitStream(objectsData[offset:])
	size, err := r.ReadMS()
	if err != nil {
		return nil, err
	}
	if size == 0 {
		return nil, fmt.Errorf("cad: 对象记录大小为 0（handle %d）", ref.handle)
	}
	rec := &objectRecord{
		offset:        uint32(offset),
		size:          size,
		bodyBitOffset: uint64(r.Sub),
		r2010Plus:     r2010Plus,
	}
	bodyStart := r.Pos // 相对 objectsData[offset:] 切片的偏移
	bodyBits := uint64(size) * 8
	if r2010Plus {
		fieldStart := r.TellBits()
		hss, err := r.ReadUMC()
		if err != nil {
			return nil, err
		}
		rec.handleStreamSizeBits = hss
		rec.handleSizeFieldBits = uint32(r.TellBits() - fieldStart)
		bodyBits += uint64(rec.handleSizeFieldBits)
	}
	bodyEnd := bodyStart + int((bodyBits+7)/8)
	if offset+bodyEnd+2 > len(objectsData) {
		return nil, fmt.Errorf("cad: 对象记录越界（handle %d）", ref.handle)
	}
	rec.body = objectsData[offset+bodyStart : offset+bodyEnd]
	return rec, nil
}

// objTypeCodeByName 常用类型码 → 名称静态表（<500 的固定段，按码值
// 紧凑排列；0x1F3 PROXY_OBJECT 为高位特例）。
var objTypeCode = map[uint16]string{
	0x01:  "TEXT",
	0x02:  "ATTRIB",
	0x03:  "ATTDEF",
	0x04:  "BLOCK",
	0x05:  "ENDBLK",
	0x06:  "SEQEND",
	0x07:  "INSERT",
	0x08:  "MINSERT",
	0x0A:  "VERTEX_2D",
	0x0B:  "VERTEX_3D",
	0x0C:  "VERTEX_MESH",
	0x0D:  "VERTEX_PFACE",
	0x0E:  "VERTEX_PFACE_FACE",
	0x0F:  "POLYLINE_2D",
	0x10:  "POLYLINE_3D",
	0x11:  "ARC",
	0x12:  "CIRCLE",
	0x13:  "LINE",
	0x14:  "DIM_ORDINATE",
	0x15:  "DIM_LINEAR",
	0x16:  "DIM_ALIGNED",
	0x17:  "DIM_ANG3PT",
	0x18:  "DIM_ANG2LN",
	0x19:  "DIM_RADIUS",
	0x1A:  "DIM_DIAMETER",
	0x1B:  "POINT",
	0x1C:  "3DFACE",
	0x1D:  "POLYLINE_PFACE",
	0x1E:  "POLYLINE_MESH",
	0x1F:  "SOLID",
	0x20:  "TRACE",
	0x21:  "SHAPE",
	0x22:  "VIEWPORT",
	0x23:  "ELLIPSE",
	0x24:  "SPLINE",
	0x28:  "RAY",
	0x29:  "XLINE",
	0x2A:  "DICTIONARY",
	0x2C:  "MTEXT",
	0x2D:  "LEADER",
	0x2E:  "TOLERANCE",
	0x2F:  "MLINE",
	0x30:  "BLOCK_CONTROL",
	0x31:  "BLOCK_HEADER",
	0x32:  "LAYER_CONTROL",
	0x33:  "LAYER",
	0x34:  "STYLE_CONTROL",
	0x35:  "STYLE",
	0x38:  "LTYPE_CONTROL",
	0x39:  "LTYPE",
	0x3C:  "VIEW_CONTROL",
	0x3D:  "VIEW",
	0x3E:  "UCS_CONTROL",
	0x3F:  "UCS",
	0x40:  "VPORT_CONTROL",
	0x41:  "VPORT",
	0x42:  "APPID_CONTROL",
	0x43:  "APPID",
	0x44:  "DIMSTYLE_CONTROL",
	0x45:  "DIMSTYLE",
	0x46:  "VX_CONTROL",
	0x47:  "VX_TABLE_RECORD",
	0x48:  "GROUP",
	0x49:  "MLINESTYLE",
	0x4D:  "LWPOLYLINE",
	0x4F:  "XRECORD",
	0x50:  "PLACEHOLDER",
	0x52:  "LAYOUT",
	0x1F3: "PROXY_OBJECT",
}

// entityTypeName 返回类型码名称：先查静态表，再查动态类名表（≥500）。
func entityTypeName(code uint16, dynamic map[uint16]string) string {
	if name, ok := objTypeCode[code]; ok {
		return name
	}
	if name, ok := dynamic[code]; ok {
		return name
	}
	return ""
}

// ---- R2010+ 重复句柄候选消解（对齐参考实现 permissive 路径） ----

// dedupeSelect 信息：单个候选记录的解析摘要。
type dedupeSelect struct {
	parsedOK      bool
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
func selectBestDuplicateHandles(objectsData []byte, refs []objectRef, ver dwgVersion, dynamicTypes map[uint16]string) []objectRef {
	grouped := map[uint64][]objectRef{}
	for _, r := range refs {
		grouped[r.handle] = append(grouped[r.handle], r)
	}
	type candKey struct {
		handle uint64
		offset uint32
	}
	infos := map[candKey]dedupeSelect{}
	for _, cands := range grouped {
		if len(cands) < 2 {
			continue
		}
		for _, c := range cands {
			infos[candKey{c.handle, c.offset}] = inspectCandidate(objectsData, c, ver, dynamicTypes)
		}
	}
	nearLayer := func(target uint64, offset uint32, cands []objectRef, self uint64) bool {
		for _, c := range cands {
			if c.handle == self {
				continue
			}
			info, ok := infos[candKey{c.handle, c.offset}]
			if !ok || !info.parsedOK || uint64(info.typeCode) != target {
				continue
			}
			diff := int64(c.offset) - int64(offset)
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
	for handle, cands := range grouped {
		if len(cands) == 1 {
			selected[handle] = cands[0].offset
			continue
		}
		bestScore := math.MinInt32
		bestOffset := cands[len(cands)-1].offset
		for _, c := range cands {
			info, ok := infos[candKey{c.handle, c.offset}]
			if !ok {
				continue
			}
			if !info.parsedOK {
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
				if info.decodedHandle == c.handle {
					score += dedupeWeightHandleHit
				} else if info.decodedHandle != 0 {
					score += dedupeWeightHandleMiss
				}
			}
			if info.typeCode == layerTypeCode {
				// LAYER 表连续性加分：与前/后一句柄的同类型候选相邻
				matching := 0
				if nearLayer(layerTypeCode, c.offset, grouped[handle-1], handle) {
					matching++
				}
				if nearLayer(layerTypeCode, c.offset, grouped[handle+1], handle) {
					matching++
				}
				switch matching {
				case 2:
					score += dedupeWeightLayerBoth
				case 1:
					score += dedupeWeightLayerNear
				}
			}
			if score > bestScore || (score == bestScore && c.offset > bestOffset) {
				bestScore = score
				bestOffset = c.offset
			}
		}
		selected[handle] = bestOffset
	}
	out := make([]objectRef, 0, len(refs))
	seen := map[uint64]bool{}
	for _, r := range refs {
		if sel, ok := selected[r.handle]; ok && sel == r.offset && !seen[r.handle] {
			out = append(out, r)
			seen[r.handle] = true
		}
	}
	return out
}

// inspectCandidate 解析单个候选记录的摘要信息：类型码/数据量/实体归类，
// 实体类再解公共头验证句柄一致性（仅常见实体类型码）。
func inspectCandidate(objectsData []byte, c objectRef, ver dwgVersion, dynamicTypes map[uint16]string) dedupeSelect {
	var info dedupeSelect
	rec, err := parseObjectRecord(objectsData, c, ver.r2010Plus())
	if err != nil {
		return info
	}
	h, err := parseObjHeader(rec)
	if err != nil {
		return info
	}
	info.parsedOK = true
	info.typeCode = h.typeCode
	info.dataSize = rec.size
	info.isEntity = isEntityType(h.typeCode, dynamicTypes)
	if info.isEntity {
		r := rec.bodyBitStream()
		r.SetBitPos(h.dataStartBit)
		if head, err := parseCommonEntityHeadR2013(r, rec.dataEndBit()); err == nil {
			info.decodedHandle = head.handle
			info.hasDecoded = true
		}
	}
	return info
}
