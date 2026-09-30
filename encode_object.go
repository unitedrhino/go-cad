// 本文件实现内部对象的位级重编码（dwgwrite 编码方向的起点）：
// 以 R2000-R2007 家族为例，从解码结果 objGeneric 重写对象 body
// （OT 类型码 + RL bitsize + 句柄链 + 公共尾），供 round-trip 往返
// 测试（解码→编码→重解码逐字段一致）使用。
package cad

import (
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/entity"
	"github.com/unitedrhino/go-cad/internal/object"
	"os"
	"strings"
)

// encodeInternalObjectR2000 将 R2000-R2007 家族的内部对象重编码为
// 对象 body 位流。g 为首次解码结果；采用"原始位串回放"通用方案：
// RL bitsize 占位 + headRawBits（H/EED/公共头/专有字段原样）+
// RawHandleBits（handle 流原样），对一切有专门解码器的类型位级往返。
// 返回 body 字节与 dat/handle 分界位（即 LibreDWG 语义的 bitsize）。
func encodeInternalObjectR2000(g *object.ObjGeneric, typeCode uint16) ([]byte, uint64, error) {
	w := bitstream.NewEncWriter()
	_ = typeCode
	if g.HeadRawBits == "" {
		return nil, 0, fmt.Errorf("cad: round-trip 缺少 headRawBits")
	}
	if g.R2010Plus {
		// R2010+：无内联 RL，body = 记录头前导位串（preBits）+
		// headRawBits（dataStartBit 起的专有字段段）+ RawHandleBits
		// （handle-stream 段）原样回放，与源 body 布局同构
		if g.PreBits != "" {
			w.WriteBitsString(g.PreBits)
		}
		w.WriteBitsString(g.HeadRawBits)
		datEnd := w.TellBits()
		w.WriteBitsString(g.RawHandleBits)
		return w.Bytes(), datEnd, nil
	}
	// R2000-R2007 内联 RL 语义：RL bitsize 占位（body 位 0 起字节对齐，
	// dat 写完后回填小端 4 字节）
	w.WriteRL(0)
	// H/EED/公共头/专有字段原始位串原样写回（与源对象位级一致）
	w.WriteBitsString(g.HeadRawBits)
	// dat 重编码位级校验：dat 结束位 + 前导位（RL bitsize 字段在原始
	// body 内的起点）应等于源对象的 bitsize（body 内 handle 流起点）
	datEnd := w.TellBits()
	if g.ObjSizeBit != 0 && datEnd+g.HdOffsetBits != g.ObjSizeBit {
		return nil, 0, fmt.Errorf("cad: round-trip dat 位长不一致 %d+%d != %d", datEnd, g.HdOffsetBits, g.ObjSizeBit)
	}
	// 回填 bitsize RL（位 0 起字节对齐，小端 4 字节，值 = dat 位长）
	w.Data[0] = uint8(datEnd)
	w.Data[1] = uint8(datEnd >> 8)
	w.Data[2] = uint8(datEnd >> 16)
	w.Data[3] = uint8(datEnd >> 24)
	// handle 流原始位串原样写回（含 owner/reactors/xdic 与尾部 padding，
	// 位级与源对象一致）
	w.WriteBitsString(g.RawHandleBits)
	return w.Bytes(), datEnd, nil
}

// encodeXdataItems 将 xdataItem 列表编码为扩展数据字节区
// （与 decodeXdataItems 对称；r2007Plus 决定字符串的 UTF-16 形态）。
func encodeXdataItems(w *bitstream.EncWriter, items []object.XdataItem, r2007Plus bool) error {
	for _, it := range items {
		w.WriteRS(uint16(it.Code))
		switch it.Kind {
		case object.XdataString:
			if r2007Plus {
				units := bitstream.Utf16Encode(it.Str)
				w.WriteRS(uint16(len(units)))
				for _, u := range units {
					w.WriteRS(u)
				}
			} else {
				b := []byte(it.Str)
				w.WriteRS(uint16(len(b)))
				w.WriteRC(30) // ANSI_1252 语义由调用侧保证；空串场景无影响
				w.WriteTF(b)
			}
		case object.XdataReal:
			w.WriteRD(it.Float)
		case object.XdataBool, object.XdataInt8:
			w.WriteRC(uint8(it.Int))
		case object.XdataInt16:
			w.WriteRS(uint16(it.Int))
		case object.XdataInt32:
			w.WriteRL(uint32(it.Int))
		case object.XdataInt64:
			w.WriteRLL(uint64(it.Int))
		case object.XdataPoint3D:
			for _, f := range it.Point {
				w.WriteRD(f)
			}
		case object.XdataBinary:
			w.WriteRC(uint8(len(it.Bytes)))
			w.WriteTF(it.Bytes)
		case object.XdataHandle:
			w.WriteRLL(uint64(it.Int))
		default:
			return fmt.Errorf("cad: xdata 未知类型 %d", it.Kind)
		}
	}
	return nil
}

// encodeXrecordR2000 重编码 R2000-R2007 家族的 XRECORD 对象 body
// （两遍法：先编码 xdata items 得字节数，再整编含 BL 压缩长度的完整流）。
// EED 维持跳过语义（对称写终止 BS 0）；objid 句柄值为占位。
func encodeXrecordR2000(x *object.ObjXrecord, ver container.DwgVersion) ([]byte, error) {
	writeHead := func(w *bitstream.EncWriter) {
		w.WriteRL(0) // bitsize 占位
		dbg := os.Getenv("CAD_DECODE_DBG") != ""
		if dbg {
			fmt.Fprintf(os.Stderr, "[dS] RL @%d\n", w.TellBits())
		}
		switch {
		case x.Handle == 0:
			w.WriteH(0, 0, 0)
		case x.Handle <= 0xFF:
			w.WriteH(0, 1, x.Handle)
		case x.Handle <= 0xFFFF:
			w.WriteH(0, 2, x.Handle)
		case x.Handle <= 0xFFFFFF:
			w.WriteH(0, 3, x.Handle)
		default:
			w.WriteH(0, 4, x.Handle)
		}
		w.WriteBS(0) // EED 终止（XRECORD 路径解码端为跳过语义）
	}
	// 第一遍：编码 xdata items 得字节数
	tmp := bitstream.NewEncWriter()
	if err := encodeXdataItems(tmp, x.Xdata, ver >= container.VerR2007); err != nil {
		return nil, err
	}
	xdBytes := tmp.Bytes()

	w := bitstream.NewEncWriter()
	writeHead(w)
	w.WriteBL(uint32(x.NumReactors)) // num_reactors（解码端在 EED 后、xdata_size 前读取）
	w.WriteBL(uint32(len(xdBytes)))
	if err := encodeXdataItems(w, x.Xdata, ver >= container.VerR2007); err != nil {
		return nil, err
	}
	if ver != container.VerR13 && ver != container.VerR14 {
		w.WriteBS(x.Cloning)
		// num_objid_handles 非流字段：objid 句柄原样保留在
		// RawHandleBits 位串中，无需单独写出
	}
	// dat 结束，回填 bitsize（body 位 0 起字节对齐，小端 4 字节）
	datEnd := w.TellBits()
	w.Data[0] = uint8(datEnd)
	w.Data[1] = uint8(datEnd >> 8)
	w.Data[2] = uint8(datEnd >> 16)
	w.Data[3] = uint8(datEnd >> 24)
	// handle 流原始位串原样写回（owner/reactors/objid 全在原始位串中）
	w.WriteBitsString(x.RawHandleBits)
	for w.Bit != 0 {
		w.WriteBitsMsb(0, 1)
	}
	return w.Bytes(), nil
}

// encodeDictionaryR2000 重编码 R2000-R2007 家族的 DICTIONARY/
// DICTIONARYWDFLT 对象 body。
func encodeDictionaryR2000(d *object.ObjDictionary, ver container.DwgVersion, withDefault bool) ([]byte, error) {
	w := bitstream.NewEncWriter()
	w.WriteRL(0) // bitsize 占位
	dbg := os.Getenv("CAD_DECODE_DBG") != ""
	if dbg {
		fmt.Fprintf(os.Stderr, "[dS] RL @%d\n", w.TellBits())
	}
	switch {
	case d.Handle == 0:
		w.WriteH(0, 0, 0)
	case d.Handle <= 0xFF:
		w.WriteH(0, 1, d.Handle)
	case d.Handle <= 0xFFFF:
		w.WriteH(0, 2, d.Handle)
	case d.Handle <= 0xFFFFFF:
		w.WriteH(0, 3, d.Handle)
	default:
		w.WriteH(0, 4, d.Handle)
	}
	// EED：结构化字段非空时对称重建，否则写终止 BS 0
	if len(d.EedFields) != 0 {
		if err := encodeEEDFields(w, d.EedFields); err != nil {
			return nil, err
		}
	} else {
		w.WriteBS(0)
	}
	if dbg {
		fmt.Fprintf(os.Stderr, "[dS] EED @%d\n", w.TellBits())
	}
	w.WriteBL(uint32(d.NumReactors))
	if dbg {
		fmt.Fprintf(os.Stderr, "[dS] nR @%d\n", w.TellBits())
	}
	w.WriteBL(uint32(d.NumItems))
	if dbg {
		fmt.Fprintf(os.Stderr, "[dS] nI @%d\n", w.TellBits())
	}
	w.WriteBS(d.Cloning)
	if dbg {
		fmt.Fprintf(os.Stderr, "[dS] cloning @%d\n", w.TellBits())
	}
	if d.IsHardOwner {
		w.WriteRC(1)
	} else {
		w.WriteRC(0)
	}
	if dbg {
		fmt.Fprintf(os.Stderr, "[dS] hard @%d\n", w.TellBits())
	}
	// texts：TV 长度含 \0 与否因写入方而异，优先按解码时记录的原始
	// 位串原样写回；无记录（合成场景）时按 writeTV（长度不含 \0）
	if d.TextRawBits != "" {
		w.WriteBitsString(d.TextRawBits)
	} else {
		for _, s := range d.Texts {
			w.WriteTV(s)
		}
	}
	// dat 重编码位级校验（ObjSizeBit 含 OT 类型码位）
	datEnd := w.TellBits()
	if os.Getenv("CAD_DECODE_DBG") != "" {
		fmt.Fprintf(os.Stderr, "[dRT] dat=%d objSizeBit=%d handle=%d nR=%d nI=%d texts=%q cloning=%d hard=%v rawHB=%d\n",
			datEnd, d.ObjSizeBit, d.Handle, d.NumReactors, d.NumItems, d.Texts, d.Cloning, d.IsHardOwner, len(d.RawHandleBits))
	}
	if d.ObjSizeBit != 0 && datEnd+d.HdOffsetBits != d.ObjSizeBit {
		return nil, fmt.Errorf("cad: DICTIONARY round-trip dat 位长不一致 %d+%d != %d", datEnd, d.HdOffsetBits, d.ObjSizeBit)
	}
	// bitsize RL 回填（dat 结束位，小端 4 字节；重解码端按 RL 值
	// 定位 handle 流起点）
	w.Data[0] = uint8(datEnd)
	w.Data[1] = uint8(datEnd >> 8)
	w.Data[2] = uint8(datEnd >> 16)
	w.Data[3] = uint8(datEnd >> 24)
	// handle 流原始位串原样写回（owner/reactors/xdic/itemhandles/
	// defaultid 全在原始位串中）
	w.WriteBitsString(d.RawHandleBits)
	for w.Bit != 0 {
		w.WriteBitsMsb(0, 1)
	}
	return w.Bytes(), nil
}

// eedInt64Field EED 标量字段（.code/.size）的安全类型断言：
// 键协议由 fmt.Sscanf 解析，值类型不受编译期约束，类型不符时报错
// 返回而非 panic（畸形字段链不致打断整个写出流程）。
func eedInt64Field(key string, v any) (int64, error) {
	n, ok := v.(int64)
	if !ok {
		return 0, fmt.Errorf("cad: EED 字段 %q 值类型异常（期望 int64，得 %T）", key, v)
	}
	return n, nil
}

// ---- 极限批次 E：objGeneric.Fields → 位流的通用对象正向编码器 ----
// 与读侧 gfRead/decoders 表镜像：按对象类型专属写出器（gfWriters）以
// 显式原语（BS/BL/BD/TV/RC/B/Point）序列化 Fields 展平键值，使 JSON
// 来源的 LAYOUT/GROUP/XRECORD 等内部对象可结构化正向写出。

// gfNum 数值字段读取（JSON 来源 float64、解码来源 int64 双形态）。
func gfNum(g *object.ObjGeneric, key string) int64 {
	switch v := g.Field(key).(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return 0
}

// gfReal 实数字段读取。
func gfReal(g *object.ObjGeneric, key string) float64 {
	switch v := g.Field(key).(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	}
	return 0
}

// gfBool 布尔字段读取（JSON 侧 bool、解码侧 int64 双形态）。
func gfBool(g *object.ObjGeneric, key string) bool {
	switch v := g.Field(key).(type) {
	case bool:
		return v
	case int64:
		return v != 0
	case float64:
		return v != 0
	}
	return false
}

// gfStr 字符串字段读取。
func gfStr(g *object.ObjGeneric, key string) string {
	s, _ := g.Field(key).(string)
	return s
}

// gfPoint 点字段读取（解码侧 []float64、JSON 侧 []any 双形态，n 个分量）。
func gfPoint(g *object.ObjGeneric, key string, n int) []float64 {
	out := make([]float64, n)
	switch v := g.Field(key).(type) {
	case []float64:
		for i := 0; i < n && i < len(v); i++ {
			out[i] = v[i]
		}
	case []any:
		for i := 0; i < n && i < len(v); i++ {
			if f, ok := v[i].(float64); ok {
				out[i] = f
			}
		}
	}
	return out
}

// gfHandleValues 句柄引用数组解析：gold 形态 [code, size, ref, abs]
// （abs 为末位绝对句柄）；JSON 侧 []any、解码侧 []uint64 双形态。
// 返回全部绝对句柄。
func gfHandleValues(v any) []uint64 {
	switch items := v.(type) {
	case []any:
		out := make([]uint64, 0, len(items))
		for _, it := range items {
			switch h := it.(type) {
			case []any:
				if len(h) > 0 {
					if f, ok := h[len(h)-1].(float64); ok {
						out = append(out, uint64(f))
					}
				}
			case float64:
				out = append(out, uint64(h))
			}
		}
		return out
	case []uint64:
		return items
	}
	return nil
}

// gfWritePoint2/Point3/TV/RC/BS/BL 字段原语包装（缺省值兜底写出，保证
// 位流布局与读侧解码器逐字段对齐）。
func gfWriteTV(w *bitstream.EncWriter, g *object.ObjGeneric, key string) { w.WriteTV(gfStr(g, key)) }
func gfWriteRC(w *bitstream.EncWriter, g *object.ObjGeneric, key string) {
	w.WriteRC(uint8(gfNum(g, key)))
}
func gfWriteBS(w *bitstream.EncWriter, g *object.ObjGeneric, key string) {
	w.WriteBS(uint16(gfNum(g, key)))
}
func gfWriteBL(w *bitstream.EncWriter, g *object.ObjGeneric, key string) {
	w.WriteBL(uint32(gfNum(g, key)))
}
func gfWriteBD(w *bitstream.EncWriter, g *object.ObjGeneric, key string) { w.WriteBD(gfReal(g, key)) }
func gfWriteB(w *bitstream.EncWriter, g *object.ObjGeneric, key string)  { w.WriteB(gfBool(g, key)) }

// gfWritePoint2 BD 压缩点（gfRead Point2 的逆）。
func gfWritePoint2(w *bitstream.EncWriter, g *object.ObjGeneric, key string) {
	p := gfPoint(g, key, 2)
	w.WriteBD(p[0])
	w.WriteBD(p[1])
}

// gfWritePoint2RD raw double 点（gfRead Point2RD 的逆）。
func gfWritePoint2RD(w *bitstream.EncWriter, g *object.ObjGeneric, key string) {
	p := gfPoint(g, key, 2)
	w.WriteRD(p[0])
	w.WriteRD(p[1])
}

// gfWritePoint3 3BD 点（gfRead Point3 的逆）。
func gfWritePoint3(w *bitstream.EncWriter, g *object.ObjGeneric, key string) {
	p := gfPoint(g, key, 3)
	write3BD(w, entity.Point3{p[0], p[1], p[2]})
}

// gfWriteCommonTableFlags COMMON_TABLE_FLAGS 写出（readCommonTableFlags
// 的逆）：pre-R2004 为 B is_xref_ref + BS is_xref_resolved + B is_xref_dep，
// R2004+ 仅 BS is_xref_resolved。
func gfWriteCommonTableFlags(w *bitstream.EncWriter, g *object.ObjGeneric, ver container.DwgVersion) {
	if object.VerUntilR2004(ver) {
		w.WriteB(gfBool(g, "is_xref_ref"))
		w.WriteBS(uint16(gfNum(g, "is_xref_resolved")))
		w.WriteB(gfBool(g, "is_xref_dep"))
		return
	}
	w.WriteBS(uint16(gfNum(g, "is_xref_resolved")))
}

// gfWriteXdataItemsFromFields 从 Fields 的 xdata 数组（JSON 形态
// [[code, value], ...]）重建 xdataItem 列表：组码按 resbufValueType
// 分派值类型（与 decodeXdataItems 的读侧分派一致）。
func gfWriteXdataItemsFromFields(g *object.ObjGeneric) []object.XdataItem {
	raw, ok := g.Field("xdata").([]any)
	if !ok {
		return nil
	}
	items := make([]object.XdataItem, 0, len(raw))
	for _, it := range raw {
		pair, ok := it.([]any)
		if !ok || len(pair) < 2 {
			continue
		}
		cf, ok := pair[0].(float64)
		if !ok {
			continue
		}
		item := object.XdataItem{Code: int(cf), Kind: object.ResbufValueType(int(cf))}
		switch item.Kind {
		case object.XdataString:
			s, _ := pair[1].(string)
			item.Str = s
		case object.XdataReal:
			f, _ := pair[1].(float64)
			item.Float = f
		case object.XdataBinary:
			// gold JSON 的二进制 xdata 为十六进制串（dwgread 输出口径）
			if s, ok := pair[1].(string); ok {
				b := make([]byte, len(s)/2)
				for j := 0; j < len(b); j++ {
					fmt.Sscanf(s[2*j:2*j+2], "%02X", &b[j])
				}
				item.Bytes = b
			}
		case object.XdataHandle:
			// 句柄 xdata：gold JSON 十六进制串或数值双形态
			switch hv := pair[1].(type) {
			case string:
				fmt.Sscanf(hv, "%X", &item.Int)
			case float64:
				item.Int = int64(hv)
			}
		case object.XdataPoint3D:
			if pt, ok := pair[1].([]any); ok {
				for i := 0; i < 3 && i < len(pt); i++ {
					if fv, ok := pt[i].(float64); ok {
						item.Point[i] = fv
					}
				}
			}
		default:
			switch n := pair[1].(type) {
			case float64:
				item.Int = int64(n)
			case int64:
				item.Int = n
			}
		}
		items = append(items, item)
	}
	return items
}

// gfWriters 通用对象专有字段写出器（镜像读侧 decoders 表的 R2000 分支；
// 缺席类型无正向编码器，collectForwardObjects 跳过）。
var gfWriters = map[string]func(w *bitstream.EncWriter, g *object.ObjGeneric, ver container.DwgVersion) error{
	"PLACEHOLDER":      func(*bitstream.EncWriter, *object.ObjGeneric, container.DwgVersion) error { return nil }, // 无专有字段
	"DICTIONARYVAR":    gfWriteDictionaryVar,
	"SCALE":            gfWriteScale,
	"GROUP":            gfWriteGroup,
	"WIPEOUTVARIABLES": gfWriteWipeoutVariables,
	"APPID":            gfWriteTableRecordSimple,
	"STYLE":            gfWriteStyle,
	"XRECORD":          gfWriteXrecordGeneric,
	"LAYOUT":           gfWriteLayout,
}

// gfWriteDictionaryVar DICTIONARYVAR：RC schema(280) + T strvalue
// （镜像 decodeGenericDICTIONARYVAR）。
func gfWriteDictionaryVar(w *bitstream.EncWriter, g *object.ObjGeneric, _ container.DwgVersion) error {
	gfWriteRC(w, g, "schema")
	gfWriteTV(w, g, "strvalue")
	return nil
}

// gfWriteScale SCALE：BS flag(70) + T name(300) + BD paper_units(140) +
// BD drawing_units(141) + B is_unit_scale(290)（镜像 decodeGenericSCALE）。
func gfWriteScale(w *bitstream.EncWriter, g *object.ObjGeneric, _ container.DwgVersion) error {
	gfWriteBS(w, g, "flag")
	gfWriteTV(w, g, "name")
	gfWriteBD(w, g, "paper_units")
	gfWriteBD(w, g, "drawing_units")
	gfWriteB(w, g, "is_unit_scale")
	return nil
}

// gfWriteGroup GROUP：T name + BS unnamed + BS selectable + BL num_groups
// （gold JSON 无 num_groups 键，以 groups 数组长度为准；组员句柄在
// handle 流，镜像 decodeGenericGROUP）。
func gfWriteGroup(w *bitstream.EncWriter, g *object.ObjGeneric, _ container.DwgVersion) error {
	gfWriteTV(w, g, "name")
	gfWriteBS(w, g, "unnamed")
	gfWriteBS(w, g, "selectable")
	w.WriteBL(uint32(len(gfHandleValues(g.Field("groups")))))
	return nil
}

// gfWriteWipeoutVariables WIPEOUTVARIABLES：BS display_frame(70)。
func gfWriteWipeoutVariables(w *bitstream.EncWriter, g *object.ObjGeneric, _ container.DwgVersion) error {
	gfWriteBS(w, g, "display_frame")
	return nil
}

// gfWriteTableRecordSimple 简单表记录（APPID）：T name +
// COMMON_TABLE_FLAGS + RC unknown（镜像 decodeGenericAPPID）。
func gfWriteTableRecordSimple(w *bitstream.EncWriter, g *object.ObjGeneric, ver container.DwgVersion) error {
	gfWriteTV(w, g, "name")
	gfWriteCommonTableFlags(w, g, ver)
	gfWriteRC(w, g, "unknown")
	return nil
}

// gfWriteStyle STYLE 表记录：T name + COMMON_TABLE_FLAGS + is_shape/
// is_vertical B + 字体尺寸组 + T font_file/bigfont_file
// （镜像 decodeGenericSTYLE 的 R2000 分支）。
func gfWriteStyle(w *bitstream.EncWriter, g *object.ObjGeneric, ver container.DwgVersion) error {
	gfWriteTV(w, g, "name")
	gfWriteCommonTableFlags(w, g, ver)
	gfWriteB(w, g, "is_shape")
	gfWriteB(w, g, "is_vertical")
	gfWriteBD(w, g, "text_size")
	gfWriteBD(w, g, "width_factor")
	gfWriteBD(w, g, "oblique_angle")
	gfWriteRC(w, g, "generation")
	gfWriteBD(w, g, "last_height")
	gfWriteTV(w, g, "font_file")
	gfWriteTV(w, g, "bigfont_file")
	return nil
}

// gfWriteXrecordGeneric objGeneric 形态的 XRECORD（JSON 来源）：BL
// num_reactors（公共段）之后为 BL xdata_size + xdata items（两遍法，
// 同 encodeXrecordR2000 布局）+ BS cloning。objid 句柄在 handle 流占位。
func gfWriteXrecordGeneric(w *bitstream.EncWriter, g *object.ObjGeneric, ver container.DwgVersion) error {
	items := gfWriteXdataItemsFromFields(g)
	tmp := bitstream.NewEncWriter()
	if err := encodeXdataItems(tmp, items, ver >= container.VerR2007); err != nil {
		return err
	}
	// xdata_size 以重编码实际字节数为准（写读自洽，不回放 gold 声明值）
	w.WriteBL(uint32(len(tmp.Bytes())))
	w.WriteTF(tmp.Bytes())
	if ver != container.VerR13 && ver != container.VerR14 {
		w.WriteBS(uint16(gfNum(g, "cloning")))
	}
	return nil
}

// gfWriteLayout LAYOUT（R2000 分支，镜像 decodeGenericLAYOUT 的步骤序）：
// plotsettings 系 + plotview_name T（R13~R2000）+ layout 头 + INSBASE/
// LIMMIN/LIMMAX/UCS 段 + EXTMIN/EXTMAX；num_viewports 为 R2004a+ 不写。
func gfWriteLayout(w *bitstream.EncWriter, g *object.ObjGeneric, ver container.DwgVersion) error {
	gfWriteTV(w, g, "plotsettings.printer_cfg_file")
	gfWriteTV(w, g, "plotsettings.paper_size")
	gfWriteBS(w, g, "plotsettings.plot_flags")
	gfWriteBD(w, g, "plotsettings.left_margin")
	gfWriteBD(w, g, "plotsettings.bottom_margin")
	gfWriteBD(w, g, "plotsettings.right_margin")
	gfWriteBD(w, g, "plotsettings.top_margin")
	gfWriteBD(w, g, "plotsettings.paper_width")
	gfWriteBD(w, g, "plotsettings.paper_height")
	gfWriteTV(w, g, "plotsettings.canonical_media_name")
	gfWritePoint2(w, g, "plotsettings.plot_origin")
	gfWriteBS(w, g, "plotsettings.plot_paper_unit")
	gfWriteBS(w, g, "plotsettings.plot_rotation_mode")
	gfWriteBS(w, g, "plotsettings.plot_type")
	gfWritePoint2(w, g, "plotsettings.plot_window_ll")
	gfWritePoint2(w, g, "plotsettings.plot_window_ur")
	if ver == container.VerR13 || ver == container.VerR14 || ver == container.VerR2000 {
		gfWriteTV(w, g, "plotsettings.plotview_name")
	}
	gfWriteBD(w, g, "plotsettings.paper_units")
	gfWriteBD(w, g, "plotsettings.drawing_units")
	gfWriteTV(w, g, "plotsettings.stylesheet")
	gfWriteBS(w, g, "plotsettings.std_scale_type")
	gfWriteBD(w, g, "plotsettings.std_scale_factor")
	gfWritePoint2(w, g, "plotsettings.paper_image_origin")
	if ver >= container.VerR2004 {
		gfWriteBS(w, g, "plotsettings.shadeplot_type")
		gfWriteBS(w, g, "plotsettings.shadeplot_reslevel")
		gfWriteBS(w, g, "plotsettings.shadeplot_customdpi")
	}
	gfWriteTV(w, g, "layout_name")
	gfWriteBS(w, g, "tab_order")
	gfWriteBS(w, g, "layout_flags")
	gfWritePoint3(w, g, "INSBASE")
	gfWritePoint2RD(w, g, "LIMMIN")
	gfWritePoint2RD(w, g, "LIMMAX")
	gfWritePoint3(w, g, "UCSORG")
	gfWritePoint3(w, g, "UCSXDIR")
	gfWritePoint3(w, g, "UCSYDIR")
	gfWriteBD(w, g, "ucs_elevation")
	gfWriteBS(w, g, "UCSORTHOVIEW")
	gfWritePoint3(w, g, "EXTMIN")
	gfWritePoint3(w, g, "EXTMAX")
	// R2004a+ 的 BL num_viewports：R2000 布局不写
	return nil
}

// gfFixedObjectCode 通用对象固定类型码（objTypeCode 静态表的写出侧
// 子集；动态类对象经 forwardObjectDynamicClasses 注册 ≥500 码）。
var gfFixedObjectCode = map[string]uint16{
	"PLACEHOLDER": 0x50,
	"GROUP":       0x48,
	"XRECORD":     0x4F,
	"APPID":       0x43,
	"STYLE":       0x35,
	"DICTIONARY":  0x2A,
}

// forwardObjectDynamicClasses 对象侧动态类注册表（类段条目
// itemClassID=0x1F3；按文档实际出现顺序在实体动态类之后分配码）。
var forwardObjectDynamicClasses = []string{
	"LAYOUT",
	"DICTIONARYVAR",
	"SCALE",
	"WIPEOUTVARIABLES",
}

// gfExtraHandleCount 类型专属 handle 流附加引用数（镜像读侧 spec 的
// extraHandles：LAYOUT 4 个布局关联句柄、APPID/STYLE 的 xref 引用；
// GROUP 的组员向量单独处理）。
func gfExtraHandleCount(name string) int {
	switch name {
	case "LAYOUT":
		return 4
	case "APPID", "STYLE":
		return 1
	}
	return 0
}

// gfHandleFirst 取单句柄字段的绝对句柄（Fields 键的 [code,..,abs] 形态）。
func gfHandleFirst(g *object.ObjGeneric, key string) uint64 {
	if hv := gfHandleValues(g.Field(key)); len(hv) > 0 {
		return hv[len(hv)-1]
	}
	return 0
}

// encodeForwardGenericObject 将 objGeneric（JSON 来源展平 Fields）正向
// 编码为 R2000 对象 body：RL bitsize + H self + EED 终止 + BL num_reactors
// + 类型专属字段（gfWriters）→ handle 流（owner + reactors + xdic +
// 类型专属引用）。dyn 为动态类码表（对象类名 → ≥500 码）。
func encodeForwardGenericObject(g *object.ObjGeneric, ver container.DwgVersion, dyn map[string]uint16) ([]byte, error) {
	if g == nil || g.Handle == 0 {
		return nil, fmt.Errorf("cad: 通用对象缺少句柄")
	}
	write, ok := gfWriters[g.Name]
	if !ok {
		return nil, fmt.Errorf("cad: 对象类型 %s 无正向编码器", g.Name)
	}
	typeCode := gfFixedObjectCode[g.Name]
	if typeCode == 0 {
		code, ok := dyn[g.Name]
		if !ok {
			return nil, fmt.Errorf("cad: 动态对象类 %s 未注册", g.Name)
		}
		typeCode = code
	}
	w := bitstream.NewEncWriter()
	w.WriteBS(typeCode)
	rlOff := w.TellBits()
	w.WriteRL(0)
	writeHdlSelf(w, g.Handle)
	w.WriteBS(0) // EED 终止
	// num_reactors：Fields 的 reactors 数组长度（JSON 来源 NumReactors 未填）
	numReactors := len(gfHandleValues(g.Field("reactors")))
	if g.NumReactors != 0 {
		numReactors = g.NumReactors
	}
	w.WriteBL(uint32(numReactors))
	if err := write(w, g, ver); err != nil {
		return nil, err
	}
	patchRL(w, rlOff, uint32(w.TellBits()))
	// handle 流：owner（Fields 的 ownerhandle 兜底）→ reactors（空引用
	// 占位）→ xdic → 类型专属引用
	owner := g.Owner
	if owner == 0 {
		if hv := gfHandleValues(g.Field("ownerhandle")); len(hv) > 0 {
			owner = hv[len(hv)-1]
		}
	}
	writeHdlCode(w, 4, owner)
	for i := 0; i < numReactors; i++ {
		writeHdlNull(w)
	}
	writeHdlNull(w) // xdicobjhandle
	switch g.Name {
	case "GROUP":
		// 组员句柄向量（code 2 soft pointer，顺序对照读侧 handleVectorKey 消费）
		for _, h := range gfHandleValues(g.Field("groups")) {
			writeHdlCode(w, 2, h)
		}
	case "XRECORD":
		// objid handles：JSON 来源无独立句柄键，写克隆标志声明数（gold 常为 0）
	case "LAYOUT":
		// 布局关联句柄（读侧顺序 block_header + active_viewport +
		// base_ucs + named_ucs；后两者无来源写空引用）
		writeHdlCode(w, 4, gfHandleFirst(g, "block_header"))
		writeHdlCode(w, 4, gfHandleFirst(g, "active_viewport"))
		writeHdlNull(w)
		writeHdlNull(w)
	default:
		for i := 0; i < gfExtraHandleCount(g.Name); i++ {
			writeHdlNull(w)
		}
	}
	w.AlignByte()
	return w.Bytes(), nil
}

// encodeEEDFields 将 parseEEDChain 产出的结构化 EED 字段对称编码回
// EED 位流（含各块 size BS + handle H + (code RC + value)* 序列与
// 终止 BS 0）。键形如 eed[i].size/handle/code/value；带 size 的元素
// 为块首。
func encodeEEDFields(w *bitstream.EncWriter, fields []object.ObjField) error {
	// 按数组下标分组
	type pair struct {
		code  int64
		val   any
		size  int64
		hdl   []uint64
		first bool
	}
	byIdx := map[int]*pair{}
	maxIdx := -1
	for _, f := range fields {
		var idx int
		var key string
		if _, err := fmt.Sscanf(f.Key, "eed[%d].%s", &idx, &key); err != nil {
			return fmt.Errorf("cad: EED 字段键异常 %q", f.Key)
		}
		p := byIdx[idx]
		if p == nil {
			p = &pair{}
			byIdx[idx] = p
			if idx > maxIdx {
				maxIdx = idx
			}
		}
		switch {
		case strings.HasSuffix(f.Key, ".code"):
			n, err := eedInt64Field(f.Key, f.Val)
			if err != nil {
				return err
			}
			p.code = n
		case strings.HasSuffix(f.Key, ".size"):
			n, err := eedInt64Field(f.Key, f.Val)
			if err != nil {
				return err
			}
			p.size = n
			p.first = true
		case strings.HasSuffix(f.Key, ".value"):
			p.val = f.Val
		case strings.HasSuffix(f.Key, ".handle"):
			if hv, ok := f.Val.([]uint64); ok && len(hv) == 3 {
				p.hdl = hv
			}
		}
	}
	for i := 0; i <= maxIdx; i++ {
		p := byIdx[i]
		if p == nil {
			return fmt.Errorf("cad: EED 字段下标 %d 缺失", i)
		}
		if p.first {
			w.WriteBS(uint16(p.size))
			if len(p.hdl) == 3 {
				w.WriteH(uint8(p.hdl[0]), uint8(p.hdl[1]), p.hdl[2])
			}
		}
		w.WriteRC(uint8(p.code))
		switch p.code {
		case 0:
			s, _ := p.val.(string)
			b := []byte(s)
			w.WriteRC(uint8(len(b)))
			w.WriteRS(30) // 码页占位（ANSI_1252 语义）
			w.WriteTF(b)
		case 1:
			w.WriteRS(uint16(p.val.(int64)))
		case 2:
			w.WriteRC(uint8(p.val.(int64)))
		case 3:
			w.WriteRS(0)
			w.WriteRLL(uint64(p.val.(int64)))
		case 4:
			b, _ := p.val.(string)
			raw := make([]byte, len(b)/2)
			for j := 0; j < len(raw); j++ {
				fmt.Sscanf(b[2*j:2*j+2], "%02X", &raw[j])
			}
			w.WriteRC(uint8(len(raw)))
			w.WriteTF(raw)
		case 5:
			w.WriteRLL(uint64(p.val.(int64)))
		case 10, 11, 12, 13, 14, 15:
			pt, _ := p.val.([]float64)
			for _, f := range pt {
				w.WriteRD(f)
			}
		case 40, 41, 42:
			w.WriteRD(p.val.(float64))
		case 70:
			w.WriteRS(uint16(int16(p.val.(int64))))
		case 71:
			w.WriteRL(uint32(int32(p.val.(int64))))
		default:
			w.WriteRC(0)
		}
	}
	w.WriteBS(0) // 链终止
	return nil
}
