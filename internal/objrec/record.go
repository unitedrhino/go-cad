// record.go DWG 对象记录层：对象图条目（objectRef）、对象记录位级视图
// （objectRecord/objHeader）、对象图模块化差分解码与记录头定位
// （parseObjectRecord/parseObjHeader）。本包为实体/对象解码共用的最底层
// 记录原语（自 objects.go 上提，用于打破 entity↔object 循环依赖），
// 仅依赖 internal/bitstream。
package objrec

import (
	"encoding/binary"
	"fmt"

	"github.com/unitedrhino/go-cad/internal/bitstream"
)

// objectRef 对象图条目：句柄与对象记录在 AcDb:AcDbObjects 段内的偏移。
type ObjectRef struct {
	Handle uint64
	Offset uint32
}

// objectRecord 对象记录：MS size 字段之后的 body 位级视图。
// 位偏移均为 body 内局部位；bodyBitOffset 为 MS 结束处在当前字节内的位偏移
// （0-7，MS 按位流读取未必字节对齐），body 内所有定位都基于它。
type ObjectRecord struct {
	Offset        uint32
	Size          uint32 // MS 字段声明的数据大小（不含 CRC 与 R2010+ 的 UMC 字段）
	Body          []byte
	BodyBitOffset uint64
	// R2010+ 专用：MS size 不含尾部 handle-stream-size 字段，
	// 数据结束位（局部）= bodyBitOffset + handleSizeFieldBits + size*8 - handleStreamSizeBits
	R2010Plus            bool
	HandleStreamSizeBits uint32
	HandleSizeFieldBits  uint32
}

// bodyBitStream 返回定位到 MS 结束位的位读取游标。
func (Rec *ObjectRecord) BodyBitStream() *bitstream.BitStream {
	r := bitstream.NewBitStream(Rec.Body)
	r.SetBitPos(Rec.BodyBitOffset)
	return r
}

// dataEndBit 对象数据区结束局部位（= handle 流起点，即参考实现的 obj_size 语义）。
func (Rec *ObjectRecord) DataEndBit() uint64 {
	base := Rec.BodyBitOffset
	if Rec.R2010Plus {
		base += uint64(Rec.HandleSizeFieldBits)
	}
	return base + uint64(Rec.Size)*8 - uint64(Rec.HandleStreamSizeBits)
}

// objHeader 对象头：类型码 + 记录定位信息。
type ObjHeader struct {
	Rec          *ObjectRecord
	TypeCode     uint16
	DataStartBit uint64 // 实体/对象数据区起始绝对位（类型码之后）
	Handle       uint64 // 记录声明的主句柄（多数对象在数据区重述，以此兜底）
}

// parseObjHeader 解析类型码（body 局部偏移）：R2010+ 布局为 [UMC
// handle-stream-size][OT 类型码]；更早版本为 [BS 类型码]。个别记录以
// R2010+ 布局混存于早期版本，BS 解出 0 时按该布局重试。
func ParseObjHeader(Rec *ObjectRecord) (ObjHeader, error) {
	h := ObjHeader{Rec: Rec}
	r := Rec.BodyBitStream()
	if Rec.R2010Plus {
		if _, err := r.ReadUMC(); err != nil {
			return h, err
		}
		ot, err := r.ReadOT()
		if err != nil {
			return h, err
		}
		if ot == 0 {
			return h, fmt.Errorf("cad: 对象类型码为 0（offset %d）", Rec.Offset)
		}
		h.TypeCode = ot
		h.DataStartBit = r.TellBits()
		return h, nil
	}
	tc, err := r.ReadBS()
	if err != nil {
		return h, err
	}
	if tc != 0 {
		h.TypeCode = tc
		h.DataStartBit = r.TellBits()
		return h, nil
	}
	// 个别记录以 R2010+ 布局存储，按该布局重试
	r2 := Rec.BodyBitStream()
	if _, err := r2.ReadUMC(); err == nil {
		if ot, err := r2.ReadOT(); err == nil && ot != 0 {
			h.TypeCode = ot
			h.DataStartBit = r2.TellBits()
			return h, nil
		}
	}
	return h, fmt.Errorf("cad: 对象类型码为 0（offset %d）", Rec.Offset)
}

// objectMapChunk 对象图单块的解码产出：块内 (handle, offset) 增量对。
type objectMapChunk struct {
	entries []ObjectRef
}

// parseObjectMapHandles 解析对象图流：循环读取 BE u16 块大小；块内为
// (handle UMC 增量, offset MC 增量) 对；块尾 2 字节 CRC；大小为 2 的块为
// 终止块。宽容模式下每块差分从 0 重新累计（对齐参考实现 permissive 行为），
// 损坏的差分项（负句柄/越界偏移）回滚累计值并跳过。
func ParseObjectMapHandles(data []byte) ([]ObjectRef, error) {
	var objects []ObjectRef
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
		dHandle, err := ReadUnsignedModularChar(chunk, &pos)
		if err != nil {
			return out, err
		}
		dOffset, err := ReadModularChar(chunk, &pos)
		if err != nil {
			return out, err
		}
		lastHandle += dHandle
		lastOffset += dOffset
		if lastHandle < 0 || lastOffset < 0 || lastOffset > 0xFFFFFFFF {
			lastHandle, lastOffset = prevHandle, prevOffset
			continue
		}
		out.entries = append(out.entries, ObjectRef{Handle: uint64(lastHandle), Offset: uint32(lastOffset)})
	}
	return out, nil
}

// readUnsignedModularChar 无符号模块化字符（handle 增量专用；ODA 规范：
// 句柄恒为递增，无符号位）：7 位一组小端累加，最高位为续组标志，至多 5 组。
func ReadUnsignedModularChar(data []byte, pos *int) (int64, error) {
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
func ReadModularChar(data []byte, pos *int) (int64, error) {
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
func ParseObjectRecord(objectsData []byte, ref ObjectRef, R2010Plus bool) (*ObjectRecord, error) {
	Offset := int(ref.Offset)
	if Offset >= len(objectsData) {
		return nil, fmt.Errorf("cad: 对象偏移 %d 超出数据段", Offset)
	}
	r := bitstream.NewBitStream(objectsData[Offset:])
	Size, err := r.ReadMS()
	if err != nil {
		return nil, err
	}
	if Size == 0 {
		return nil, fmt.Errorf("cad: 对象记录大小为 0（handle %d）", ref.Handle)
	}
	Rec := &ObjectRecord{
		Offset:        uint32(Offset),
		Size:          Size,
		BodyBitOffset: uint64(r.Sub),
		R2010Plus:     R2010Plus,
	}
	bodyStart := r.Pos // 相对 objectsData[offset:] 切片的偏移
	bodyBits := uint64(Size) * 8
	if R2010Plus {
		fieldStart := r.TellBits()
		hss, err := r.ReadUMC()
		if err != nil {
			return nil, err
		}
		Rec.HandleStreamSizeBits = hss
		Rec.HandleSizeFieldBits = uint32(r.TellBits() - fieldStart)
		bodyBits += uint64(Rec.HandleSizeFieldBits)
	}
	bodyEnd := bodyStart + int((bodyBits+7)/8)
	if Offset+bodyEnd+2 > len(objectsData) {
		return nil, fmt.Errorf("cad: 对象记录越界（handle %d）", ref.Handle)
	}
	Rec.Body = objectsData[Offset+bodyStart : Offset+bodyEnd]
	return Rec, nil
}
