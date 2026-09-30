// 本文件实现对象段 DICTIONARY（0x2A）与 XRECORD（0x4F）的解码：
// 布局对照 LibreDWG dwg.spec 对应块与 dwgread -v9 字段日志逐位验证；
// xdata 项解析（resbufValueType DXF 码段表）同在此处。
// 公共头 bitsize 定位随版本不同（见 objects_generic.go 的 dictBitsizePos）。
package object

import (
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/objrec"
	"os"
	"strings"
)

// objDictionary DICTIONARY 对象：键名列表 + 项句柄列表（顺序一一对应）。
type ObjDictionary struct {
	Handle        uint64     // 对象主句柄
	Owner         uint64     // ownerhandle 绝对句柄
	ObjSizeBit    uint64     // bitsize：handle 流起点（相对 MS 字段之后的位）
	NumReactors   int        // reactor 数量
	XdicMissing   bool       // R2004+：无 xdicobjhandle 标记
	NumItems      int        // texts/itemHandles 数量
	Cloning       uint16     // R2000b+ 克隆标志（DXF 281）
	IsHardOwner   bool       // 硬拥有标志（DXF 280）
	Texts         []string   // 键名列表（R2007+ 为 TU，更早为 TV）
	HdOffsetBits  uint64     // dat 段前导位（body 内 RL bitsize 起点；重编码位长校验用）
	textRawLens   []int      // texts 的原始 TV 位长（诊断用）
	TextRawBits   string     // texts 段原始位串（0/1），重编码原样写回
	ItemHandles   []uint64   // 项句柄列表（与 texts 顺序对应）
	RawHandleBits string     // handle 流原始位串（0/1），供重编码原样写回
	EedFields     []ObjField // EED 结构化字段（供 WDFLT 合并到通用对象）
	defaultID     uint64     // DICTIONARYWDFLT：默认项句柄（hdl 流尾部）
}

// decodeDictionaryObject 解析 DICTIONARY 对象（dwg.spec DWG_OBJECT(DICTIONARY)）。
// dat 流：[R2000-R2007 RL bitsize（H 之前）] + H handle + EED
// + [R13/R14 RL bitsize（EED 之后）] + BL num_reactors
// + [R2004+ B is_xdic_missing] + [R2013+ B has_ds_data] + BL numitems
// + [R2000b+ BS cloning] + RC is_hardowner + numitems×T 文字（R2007+ TU，更早 TV）。
// handle 流（bitsize 起）：ownerhandle + reactors + xdic + itemhandles×numitems。
func DecodeDictionaryObject(R *bitstream.BitStream, rec *objrec.ObjectRecord, Ver container.DwgVersion, r2013Plus bool) (*ObjDictionary, error) {
	return DecodeDictionaryObjectFull(R, rec, Ver, r2013Plus, false)
}

// decodeDictionaryObjectFull 解析 DICTIONARY；withDefault 为 true 时
// 按 DICTIONARYWDFLT 在 itemhandles 后追加读取 defaultid 句柄。
func DecodeDictionaryObjectFull(R *bitstream.BitStream, rec *objrec.ObjectRecord, Ver container.DwgVersion, r2013Plus bool, withDefault bool) (*ObjDictionary, error) {
	d := &ObjDictionary{}
	var err error
	// bitsize 定位策略：R2000-R2007 内联 RL 在最前；R13/R14 在 EED 后；
	// R2010+ 由记录头推导（无内联字段）
	bitsizePos := dictBitsizePos(Ver)
	// dat 段前导位：RL bitsize 字段起点（body 内）；原始流中 bitsize RL
	// 之前可能有对象 section 头的残留位，重编码从 RL 占位起编，位长
	// 校验需补回前导
	d.HdOffsetBits = R.TellBits() - rec.BodyBitOffset
	if bitsizePos == bitsizePosHead {
		if d.ObjSizeBit, err = ReadInlineBitsize(R); err != nil {
			return nil, err
		}
	}
	if d.Handle, err = readHandleValue(R); err != nil {
		return nil, err
	}
	if err = parseEEDChain(R, Ver, &d.EedFields); err != nil {
		return nil, err
	}
	if bitsizePos == bitsizePosTail {
		if d.ObjSizeBit, err = ReadInlineBitsize(R); err != nil {
			return nil, err
		}
	}
	var NumReactors uint32
	if NumReactors, err = R.ReadBL(); err != nil {
		return nil, err
	}
	if NumReactors > 4096 {
		return nil, fmt.Errorf("cad: DICTIONARY reactors 异常 %d", NumReactors)
	}
	d.NumReactors = int(NumReactors)
	if Ver >= container.VerR2004 {
		if xdic, e := R.ReadB(); e != nil {
			return nil, e
		} else {
			d.XdicMissing = xdic == 1
		}
	}
	if r2013Plus {
		if _, e := R.ReadB(); e != nil { // has_ds_data
			return nil, e
		}
	}
	var NumItems uint32
	if NumItems, err = R.ReadBL(); err != nil {
		return nil, err
	}
	if NumItems > 100_000 {
		return nil, fmt.Errorf("cad: DICTIONARY 项数异常 %d", NumItems)
	}
	d.NumItems = int(NumItems)
	// cloning BS 仅 R2000b+（R13/R14 无）；is_hardowner RC 自 R13c3
	// 起（AC1012 早于 R13c3 无此字段）。
	// DICTIONARYWDFLT（withDefault）例外：spec 中 cloning/is_hardowner
	// 为无条件字段（该类为后期补充，文件内字段恒存在）。
	if withDefault || (Ver != container.VerR13 && Ver != container.VerR14) {
		if d.Cloning, err = R.ReadBS(); err != nil {
			return nil, err
		}
	}
	if withDefault || Ver != container.VerR13 {
		if IsHardOwner, e := R.ReadRC(); e != nil {
			return nil, e
		} else {
			d.IsHardOwner = IsHardOwner != 0
		}
	}
	// 文字：dat 流内联 T 序列（R2007+ TU = BS 长度 + UTF-16LE；更早为 TV）；
	// 记录原始 TV 位长与整段位串（长度含 \0 与否因写入方而异，重编码
	// 原样写回）
	textsStart := R.TellBits()
	for i := 0; i < d.NumItems; i++ {
		tvStart := R.TellBits()
		var s string
		if Ver >= container.VerR2007 {
			if s, err = R.ReadTU(); err != nil {
				return nil, err
			}
		} else {
			if s, err = R.ReadTV(0); err != nil {
				return nil, err
			}
		}
		d.textRawLens = append(d.textRawLens, int(R.TellBits()-tvStart))
		d.Texts = append(d.Texts, s)
	}
	d.TextRawBits = bitstream.CollectBits(R, textsStart, R.TellBits())
	// handle 流起点：内联 bitsize 相对 MS 字段之后；R2010+ 无内联字段，
	// 直接取记录数据结束位（含 handle-stream-size 字段自身的位长）
	switch bitsizePos {
	case bitsizePosDerived:
		d.ObjSizeBit = rec.DataEndBit() - rec.BodyBitOffset - uint64(rec.HandleSizeFieldBits)
		R.SetBitPos(rec.DataEndBit())
	default:
		R.SetBitPos(rec.BodyBitOffset + d.ObjSizeBit)
	}
	// handle 流原始位串：起点（bitsize）至 body 尾，供重编码原样写回
	d.RawHandleBits = bitstream.CollectBits(R, R.TellBits(), uint64(len(R.Src))*8)
	if d.Owner, err = readOwnerHandle(R, d.Handle); err != nil {
		return nil, err
	}
	for i := 0; i < d.NumReactors; i++ {
		if _, err = objrec.ReadHandleReference(R, d.Handle); err != nil {
			return nil, err
		}
	}
	if !d.XdicMissing {
		if _, err = objrec.ReadHandleReference(R, d.Handle); err != nil {
			return nil, err
		}
	}
	for i := 0; i < d.NumItems; i++ {
		h, e := objrec.ReadHandleReference(R, d.Handle)
		if e != nil {
			return nil, e
		}
		d.ItemHandles = append(d.ItemHandles, h)
	}
	if withDefault {
		if d.defaultID, err = objrec.ReadHandleReference(R, d.Handle); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// objXrecord XRECORD 对象：应用自定义扩展数据容器。
type ObjXrecord struct {
	Handle          uint64      // 对象主句柄
	Owner           uint64      // ownerhandle 绝对句柄
	ObjSizeBit      uint64      // bitsize：handle 流起点（相对 MS 字段之后的位）
	NumReactors     int         // reactor 数量
	XdicMissing     bool        // R2004+：无 xdicobjhandle 标记
	Cloning         uint16      // R2000b+ 克隆标志（DXF 280）
	XdataSize       int         // 扩展数据字节数
	Xdata           []XdataItem // 扩展数据类型化值序列
	NumObjidHandles int         // objid 句柄数量（由 handle 流推导，流中无此字段，对应 dwgread JSON 的 num_objid_handles）
	objidHandles    []uint64    // objid 句柄向量
	EedFields       []ObjField  // EED 结构化字段（供 round-trip 重编码）
	RawHandleBits   string      // handle 流原始位串（0/1），供重编码原样写回
}

// XdataSizeBytes 返回 xdata 字节数（与导出字段 XdataSize 同值；
// 原访问器与字段导出后同名冲突，方法改名 Bytes 后缀）。
func (x *ObjXrecord) XdataSizeBytes() int { return x.XdataSize }

// XdataItems 返回 xdata 类型化值序列。
func (x *ObjXrecord) XdataItems() []XdataItem { return x.Xdata }

// decodeXrecordObject 解析 XRECORD 对象（dwg2.spec DWG_OBJECT(XRECORD)）。
// dat 流：[版本相关内联 RL bitsize] + H handle + EED + BL num_reactors
// + [R2004+ B is_xdic_missing] + [R2013+ B has_ds_data] + BL xdata_size
// + xdata 原始字节（内容暂不解析） + [R2000b+ BS cloning]。
// handle 流（bitsize 起）：ownerhandle + reactors + xdic + objid_handles 至流尾。
func DecodeXrecordObject(R *bitstream.BitStream, rec *objrec.ObjectRecord, Ver container.DwgVersion, r2013Plus bool) (*ObjXrecord, error) {
	x := &ObjXrecord{}
	var err error
	bitsizePos := dictBitsizePos(Ver)
	if bitsizePos == bitsizePosHead {
		if x.ObjSizeBit, err = ReadInlineBitsize(R); err != nil {
			return nil, err
		}
	}
	if os.Getenv("CAD_DECODE_DBG") != "" {
		fmt.Fprintf(os.Stderr, "[xr] RL done objSizeBit=%d @%d\n", x.ObjSizeBit, R.TellBits())
	}
	if x.Handle, err = readHandleValue(R); err != nil {
		return nil, err
	}
	if os.Getenv("CAD_DECODE_DBG") != "" {
		fmt.Fprintf(os.Stderr, "[xr] handle done @%d\n", R.TellBits())
	}
	if os.Getenv("CAD_DECODE_DBG") != "" {
		fmt.Fprintf(os.Stderr, "[xr] skipEED 前 @%d\n", R.TellBits())
	}
	if err = parseEEDChain(R, Ver, &x.EedFields); err != nil {
		if os.Getenv("CAD_DECODE_DBG") != "" {
			fmt.Fprintf(os.Stderr, "[xr] EED err: %v\n", err)
		}
		return nil, err
	}
	if os.Getenv("CAD_DECODE_DBG") != "" {
		fmt.Fprintf(os.Stderr, "[xr] skipEED 后 @%d\n", R.TellBits())
	}
	if bitsizePos == bitsizePosTail {
		if x.ObjSizeBit, err = ReadInlineBitsize(R); err != nil {
			return nil, err
		}
	}
	var NumReactors uint32
	if NumReactors, err = R.ReadBL(); err != nil {
		return nil, err
	}
	if os.Getenv("CAD_DECODE_DBG") != "" {
		fmt.Fprintf(os.Stderr, "[xr] num_reactors=%d @%d\n", NumReactors, R.TellBits())
	}
	if NumReactors > 4096 {
		return nil, fmt.Errorf("cad: XRECORD reactors 异常 %d", NumReactors)
	}
	x.NumReactors = int(NumReactors)
	if Ver >= container.VerR2004 {
		if xdic, e := R.ReadB(); e != nil {
			return nil, e
		} else {
			x.XdicMissing = xdic == 1
		}
	}
	if r2013Plus {
		if _, e := R.ReadB(); e != nil { // has_ds_data
			return nil, e
		}
	}
	var XdataSize uint32
	if XdataSize, err = R.ReadBL(); err != nil {
		return nil, err
	}
	if os.Getenv("CAD_DECODE_DBG") != "" {
		fmt.Fprintf(os.Stderr, "[xr] xdata_size=%d @%d\n", XdataSize, R.TellBits())
	}
	if XdataSize > 1<<24 {
		return nil, fmt.Errorf("cad: XRECORD 扩展数据过大 %d", XdataSize)
	}
	x.XdataSize = int(XdataSize)
	if os.Getenv("CAD_DECODE_DBG") != "" {
		fmt.Fprintf(os.Stderr, "[xr] xdataSize=%d @%d 位串(36..100)=%v\n", x.XdataSize, R.TellBits(), bitstream.CollectBits(R, 36, 100))
	}
	// 扩展数据：类型化值序列（DXF 组码 + 对应类型值，字节定长区）
	if x.Xdata, err = decodeXdataItems(R, x.XdataSize, Ver >= container.VerR2007); err != nil {
		return nil, err
	}
	if os.Getenv("CAD_DECODE_DBG") != "" {
		fmt.Fprintf(os.Stderr, "[xr] xdata done @%d\n", R.TellBits())
	}
	// cloning BS 为 R2000b+ 字段（R13/R14 无）。注意流中没有
	// num_objid_handles 字段：LibreDWG dwg2.spec 中该值由解码端在
	// handle 流中推导（读到 handlestream_size 为止）
	if Ver != container.VerR13 && Ver != container.VerR14 {
		if x.Cloning, err = R.ReadBS(); err != nil {
			return nil, err
		}
	}
	// handle 流起点：内联 bitsize 相对 MS 字段之后；R2010+ 无内联字段，
	// 直接取记录数据结束位（含 handle-stream-size 字段自身的位长）
	switch bitsizePos {
	case bitsizePosDerived:
		x.ObjSizeBit = rec.DataEndBit() - rec.BodyBitOffset - uint64(rec.HandleSizeFieldBits)
		R.SetBitPos(rec.DataEndBit())
	default:
		R.SetBitPos(rec.BodyBitOffset + x.ObjSizeBit)
	}
	// handle 流原始位串：起点（bitsize）至 body 尾，供重编码原样写回
	x.RawHandleBits = bitstream.CollectBits(R, R.TellBits(), uint64(len(R.Src))*8)
	if x.Owner, err = readOwnerHandle(R, x.Handle); err != nil {
		return nil, err
	}
	for i := 0; i < x.NumReactors; i++ {
		if _, err = objrec.ReadHandleReference(R, x.Handle); err != nil {
			return nil, err
		}
	}
	if !x.XdicMissing {
		if _, err = objrec.ReadHandleReference(R, x.Handle); err != nil {
			return nil, err
		}
	}
	// objid 句柄：读到 handle 流尾（handlestream_size = body 尾 - bitsize，
	// 即 RawHandleBits 区间）为止；读到无效句柄即停（对应 spec 的
	// if (!FIELD_VALUE) break），数量记入 numObjidHandles
	{
		hdlEnd := R.TellBits() + uint64(len(x.RawHandleBits))
		for R.TellBits() < hdlEnd && x.NumObjidHandles <= 4096 {
			h, e := objrec.ReadHandleReference(R, x.Handle)
			if e != nil || h == 0 {
				break
			}
			x.objidHandles = append(x.objidHandles, h)
			x.NumObjidHandles++
		}
	}
	return x, nil
}

// objBitsizePos 对象 bitsize 字段的定位策略。
type objBitsizePos int

const (
	bitsizePosDerived objBitsizePos = iota // R2010+：由记录头推导，无内联字段
	bitsizePosHead                         // R2000-R2007：内联 RL 在 H 之前
	bitsizePosTail                         // R13/R14：内联 RL 在 EED 之后
)

// dictBitsizePos 返回版本的 DICTIONARY/XRECORD bitsize 定位策略。
// 注意版本枚举非时间序（verR2000=0、verR14=1），范围比较会把 R14
// 误判为 R2000+，这里必须显式枚举成员。
func dictBitsizePos(Ver container.DwgVersion) objBitsizePos {
	switch {
	case Ver >= container.VerR2010:
		return bitsizePosDerived
	case Ver == container.VerR2000 || Ver == container.VerR2004 || Ver == container.VerR2007:
		return bitsizePosHead
	default:
		return bitsizePosTail
	}
}

// readInlineBitsize 读取内联 RL bitsize 并校验上界（相对 MS 字段之后的位）。
func ReadInlineBitsize(R *bitstream.BitStream) (uint64, error) {
	bitsize, err := R.ReadRL()
	if err != nil {
		return 0, err
	}
	if bitsize > 1<<28 {
		return 0, fmt.Errorf("cad: 对象 bitsize 异常 %d", bitsize)
	}
	return uint64(bitsize), nil
}

// readHandleValue 读取对象主句柄（H：4bit code + 4bit size + size 字节）。
func readHandleValue(R *bitstream.BitStream) (uint64, error) {
	h, err := R.ReadH()
	if err != nil {
		return 0, err
	}
	return h.Value, nil
}

// skipEEDChain 跳过 EED 链：BS 大小为 0 表示结束，否则 H + size 字节。
// parseEEDChain 结构化解析 EED 链并存入 g.Fields（键形如
// eed[i].size/handle/code/value，与 dwgread JSON 的 eed 数组展开同构：
// 块首对附加 size/handle，每对 (code,value) 一个数组元素）。
// value 类型按 code：0=字符串（R2007a+ 为 UTF-16，pre-R13b1 无码页，
// 其余 RC 长度 + RS_BE 码页）、1=RS、2=RC、3=layer(RS+RLL)、4=二进制、
// 5=RLL_BE、10-15=3×RD、40-42=RD、70=RS 符号、71=RL 符号。
// 解析失败时回退到 skipEEDChain 语义（保序跳过，不产生 eed 字段）。
func parseEEDChain(R *bitstream.BitStream, Ver container.DwgVersion, out *[]ObjField) error {
	start := R.TellBits()
	savedLen := len(*out)
	i := 0
	fail := func(err error) error {
		// 回退：截断结构化字段，收集整条链的原始位串（供重编码原样
		// 写回），并按 skip 语义推进到链尾
		R.SetBitPos(start)
		*out = (*out)[:savedLen]
		raw, err2 := SkipEEDChainCollect(R)
		if err2 != nil {
			return err2
		}
		*out = append(*out, ObjField{"eed_raw_bits", raw})
		return nil
	}
	for {
		sz, err := R.ReadBS()
		if err != nil {
			return fail(err)
		}
		if os.Getenv("CAD_DECODE_DBG") != "" {
			fmt.Fprintf(os.Stderr, "[pEED] sz=%d @%d r.pos=%d dataLen=%d\n", sz, R.TellBits(), R.Pos, len(R.Src))
		}
		if sz == 0 {
			return nil
		}
		h, err := R.ReadH()
		if err != nil {
			return fail(err)
		}
		if int(sz) > len(R.Src)-R.Pos {
			return fail(bitstream.ErrUnexpectedEOF)
		}
		end := R.Pos + int(sz)
		first := true
		for R.Pos < end {
			code, err := R.ReadRC()
			if err != nil {
				return fail(err)
			}
			if os.Getenv("CAD_DECODE_DBG") != "" {
				fmt.Fprintf(os.Stderr, "[eed] blk sz=%d code=%d byte@%d end@%d pos=%d bits=%v\n", sz, code, R.Pos, end, R.TellBits(), DumpBits(R, R.TellBits(), 40))
			}
			p := fmt.Sprintf("eed[%d].", i)
			var val any
			switch code {
			case 0:
				if Ver >= container.VerR2007 {
					l, e := R.ReadRS()
					if e != nil {
						return fail(e)
					}
					us := make([]uint16, l)
					for j := range us {
						us[j], e = R.ReadRS()
						if e != nil {
							return fail(e)
						}
					}
					// LibreDWG bit_read_TU 语义：NUL 终止，输出不含 NUL
					val = strings.SplitN(string(bitstream.Utf16Decode(us)), "\x00", 2)[0]
				} else {
					l, e := R.ReadRC()
					if e != nil {
						return fail(e)
					}
					cpHi, e := R.ReadRC()
					if e != nil {
						return fail(e)
					}
					cpLo, e := R.ReadRC()
					if e != nil {
						return fail(e)
					}
					cp := uint16(cpHi)<<8 | uint16(cpLo) // RS_BE 大端
					b := make([]byte, l)
					for j := 0; j < int(l); j++ {
						var cb uint8
						var e2 error
						cb, e2 = R.ReadRC()
						if e2 != nil {
							return fail(e2)
						}
						b[j] = cb
					}
					// TV 同为 NUL 终止字符串
					val = strings.SplitN(bitstream.DecodeCodepage(b, cp), "\x00", 2)[0]
				}
			case 1:
				v, e := R.ReadRS()
				if e != nil {
					return fail(e)
				}
				val = int64(v)
			case 2:
				v, err := R.ReadRC()
				if err != nil {
					return fail(err)
				}
				val = int64(v)
			case 3:
				// layer：RS + RLL（LibreDWG 同款双读）
				if _, e := R.ReadRS(); e != nil {
					return fail(e)
				}
				v, e := R.ReadBLL()
				if e != nil {
					return fail(e)
				}
				val = int64(v)
			case 4:
				l, e := R.ReadRC()
				if e != nil {
					return fail(e)
				}
				b := make([]byte, l)
				for j := 0; j < int(l); j++ {
					cb, e2 := R.ReadRC()
					if e2 != nil {
						return fail(e2)
					}
					b[j] = cb
				}
				val = fmt.Sprintf("%X", b)
			case 5:
				// entity：RLL 大端
				v, e := R.ReadBitsMsb(64)
				if e != nil {
					return fail(e)
				}
				val = int64(v)
			case 10, 11, 12, 13, 14, 15:
				pt := make([]float64, 3)
				for j := 0; j < 3; j++ {
					var e error
					pt[j], e = R.ReadRD()
					if e != nil {
						return fail(e)
					}
				}
				val = pt
			case 40, 41, 42:
				f, e := R.ReadRD()
				if e != nil {
					return fail(e)
				}
				val = f
			case 70:
				v, e := R.ReadRS()
				if e != nil {
					return fail(e)
				}
				val = int64(int16(v))
			case 71:
				v, e := R.ReadRL()
				if e != nil {
					return fail(e)
				}
				val = int64(int32(v))
			default:
				val = int64(0)
			}
			*out = append(*out,
				ObjField{p + "code", int64(code)},
				ObjField{p + "value", val})
			if first {
				*out = append(*out,
					ObjField{p + "size", int64(sz)},
					ObjField{p + "handle", []uint64{uint64(h.Code), uint64(h.Counter), h.Value}})
				first = false
			}
			i++
		}
		R.Pos = end
	}
}

// skipEEDChainCollect 跳过 EED 链并返回链的完整位串（0/1 字符串，
// 含尾字节对齐填充），供重编码原样写回。
func SkipEEDChainCollect(R *bitstream.BitStream) (string, error) {
	start := R.TellBits()
	if err := skipEEDChain(R); err != nil {
		return "", err
	}
	end := R.TellBits()
	var out []byte
	for p := start; p < end; p++ {
		if int(p/8) >= len(R.Src) {
			break
		}
		b := (R.Src[p/8] >> (7 - p%8)) & 1
		out = append(out, '0'+b)
	}
	return string(out), nil
}

func skipEEDChain(R *bitstream.BitStream) error {
	for {
		sz, err := R.ReadBS()
		if err != nil {
			return err
		}
		if sz == 0 {
			return nil
		}
		if _, err = R.ReadH(); err != nil {
			return err
		}
		if _, err = R.ReadRCS(int(sz)); err != nil {
			return err
		}
	}
}

// readOwnerHandle 读取 ownerhandle（H，绝对引用）。
func readOwnerHandle(R *bitstream.BitStream, cur uint64) (uint64, error) {
	return objrec.ReadHandleReference(R, cur)
}

// xdataItem XRECORD 扩展数据的一个类型化值项（LibreDWG Dwg_Resbuf）。
// Kind 决定哪个值字段有效；Val 返回与 dwgread JSON 输出同形的值：
// 整数类为 int64、REAL 为 float64、POINT3D 为 [3]float64、
// STRING 为 string、BINARY 为小写 hex 字符串、HANDLE/OBJECTID 为 uint64。
type XdataItem struct {
	Code  int // DXF 组码（RS 存储）
	Kind  XdataKind
	Int   int64
	Float float64
	Point [3]float64
	Str   string
	Bytes []byte
}

// KindName 返回类型的调试名（kind 枚举：0=Invalid 1=String 2=Real 3=Int8 4=Int16 5=Int32 6=Point3D 7=Binary 8=Handle）。
func (it XdataItem) KindName() string {
	names := [...]string{"Invalid", "String", "Real", "Int8", "Int16", "Int32", "Int64", "Point3D", "Binary", "Handle", "Unknown"}
	if int(it.Kind) < len(names) {
		return names[it.Kind]
	}
	return "Unknown"
}

// Val 返回 JSON 同形值
func (it XdataItem) Val() any {
	switch it.Kind {
	case XdataReal:
		return it.Float
	case XdataPoint3D:
		return it.Point
	case XdataString:
		return it.Str
	case XdataBinary:
		return fmt.Sprintf("%X", it.Bytes)
	default:
		return it.Int
	}
}

// xdataKind 扩展数据值类型（对应 LibreDWG DWG_VT_*）
type XdataKind int

const (
	XdataInvalid XdataKind = iota
	XdataString
	XdataReal
	XdataInt8
	XdataInt16
	XdataInt32
	XdataInt64
	XdataPoint3D
	XdataBinary
	XdataHandle // HANDLE / OBJECTID
	XdataBool
)

// resbufValueType DXF 组码 → 值类型（对照 LibreDWG dwg_resbuf_value_type）
func ResbufValueType(gc int) XdataKind {
	switch {
	case gc < 0:
		return XdataHandle
	case gc <= 4:
		return XdataString
	case gc == 5:
		return XdataHandle
	case gc <= 9:
		return XdataString
	case gc <= 37:
		return XdataPoint3D
	case gc <= 59:
		return XdataReal
	case gc <= 79:
		return XdataInt16
	case gc <= 99:
		return XdataInt32
	case gc <= 102:
		return XdataString
	case gc == 105:
		return XdataHandle
	case gc <= 109:
		return XdataInvalid
	case gc <= 139:
		return XdataPoint3D
	case gc <= 149:
		return XdataReal
	case gc <= 169:
		return XdataInt64
	case gc <= 179:
		return XdataInt16
	case gc <= 209:
		return XdataInvalid
	case gc <= 269:
		return XdataPoint3D
	case gc <= 279:
		return XdataInt16
	case gc <= 289:
		return XdataInt8
	case gc <= 299:
		return XdataBool
	case gc <= 309:
		return XdataString
	case gc <= 319:
		return XdataBinary
	case gc <= 329:
		return XdataHandle
	case gc <= 369:
		return XdataHandle // OBJECTID：软/硬指针与拥有句柄
	case gc <= 389:
		return XdataInt16
	case gc <= 399:
		return XdataHandle
	case gc <= 409:
		return XdataInt16
	case gc <= 419:
		return XdataString
	case gc <= 429:
		return XdataInt32
	case gc <= 439:
		return XdataString
	case gc <= 459:
		return XdataInt32
	case gc <= 469:
		return XdataReal
	case gc <= 479:
		return XdataString
	case gc == 999:
		return XdataString
	// 1004 二进制必须先于 1009 段匹配：Go switch 按序命中，
	// 放在 gc<=1009 之后将永不可达（LibreDWG dwg_resbuf_value_type 同序）
	case gc == 1004:
		return XdataBinary
	case gc <= 1009:
		return XdataString
	case gc <= 1039:
		return XdataPoint3D
	case gc <= 1042:
		return XdataReal
	case gc <= 1069:
		return XdataPoint3D
	case gc <= 1070:
		return XdataInt16
	case gc == 1071:
		return XdataInt32
	default:
		return XdataInvalid
	}
}

// decodeXdataItems 解析 XRECORD 扩展数据：每项 = RS 类型码 + 类型对应值，
// 总长 sizeBytes 字节（对照 LibreDWG dwg_decode_xdata）。
// 终点按字节位置判定（位流原语读取，与参考实现行为一致）。
func decodeXdataItems(R *bitstream.BitStream, SizeBytes int, r2007Plus bool) ([]XdataItem, error) {
	startBits := R.TellBits()
	endBits := startBits + uint64(SizeBytes)*8
	items := make([]XdataItem, 0, 8)
	for uint64(R.TellBits()) < endBits {
		code, err := R.ReadRS()
		if err != nil {
			return nil, err
		}
		if uint64(R.TellBits()) >= endBits {
			// 类型码后已到终点：丢弃（与参考实现防死循环一致）
			break
		}
		if code >= 2000 {
			break
		}
		it := XdataItem{Code: int(code), Kind: ResbufValueType(int(code))}
		switch it.Kind {
		case XdataString:
			length, e := R.ReadRS()
			if e != nil {
				return nil, e
			}
			if r2007Plus {
				if int(length) > 0 && uint64(R.TellBits())+uint64(length)*16 <= endBits {
					var sb []byte
					for i := uint16(0); i < length; i++ {
						ch, e := R.ReadRS()
						if e != nil {
							return nil, e
						}
						sb = append(sb, byte(ch), byte(ch>>8))
					}
					it.Str = container.DecodeUTF16LE(sb)
				}
			} else {
				cp, e := R.ReadRC()
				if e != nil {
					return nil, e
				}
				if uint64(R.TellBits())+uint64(length)*8 <= endBits {
					b, e := R.ReadBitsBytes(int(length))
					if e != nil {
						return nil, e
					}
					it.Str = bitstream.DecodeCodepage(b, uint16(cp))
				}
			}
		case XdataReal:
			if uint64(R.TellBits())+8 > endBits {
				break
			}
			if it.Float, err = R.ReadRD(); err != nil {
				return nil, err
			}
		case XdataBool, XdataInt8:
			if uint64(R.TellBits())+8 > endBits {
				break
			}
			b, e := R.ReadRC()
			if e != nil {
				return nil, e
			}
			it.Int = int64(b)
		case XdataInt16:
			if uint64(R.TellBits())+2 > endBits {
				break
			}
			v, e := R.ReadRS()
			if e != nil {
				return nil, e
			}
			it.Int = int64(int16(v))
		case XdataInt32:
			if uint64(R.TellBits())+4 > endBits {
				break
			}
			v, e := R.ReadRL()
			if e != nil {
				return nil, e
			}
			it.Int = int64(int32(v))
		case XdataInt64:
			if uint64(R.TellBits())+8 > endBits {
				break
			}
			v, e := R.ReadRLL()
			if e != nil {
				return nil, e
			}
			it.Int = int64(v)
		case XdataPoint3D:
			if uint64(R.TellBits())+24 > endBits {
				break
			}
			if it.Point[0], err = R.ReadRD(); err != nil {
				return nil, err
			}
			if it.Point[1], err = R.ReadRD(); err != nil {
				return nil, err
			}
			if it.Point[2], err = R.ReadRD(); err != nil {
				return nil, err
			}
		case XdataBinary:
			sz, e := R.ReadRC()
			if e != nil {
				return nil, e
			}
			if uint64(R.TellBits())+uint64(sz)*8 <= endBits {
				b, e := R.ReadBitsBytes(int(sz))
				if e != nil {
					return nil, e
				}
				it.Bytes = b
			}
		case XdataHandle:
			if uint64(R.TellBits())+8 > endBits {
				break
			}
			v, e := R.ReadRLL()
			if e != nil {
				return nil, e
			}
			it.Int = int64(v)
		default: // INVALID：与参考实现一致，终止本 xdata 区
			return items, nil
		}
		items = append(items, it)
	}
	return items, nil
}

// dumpBits 调试用：从指定位起输出 n 位 0/1 串。
func DumpBits(R *bitstream.BitStream, pos uint64, n int) string {
	out := make([]byte, 0, n)
	for i := 0; i < n; i++ {
		p := pos + uint64(i)
		if int(p/8) >= len(R.Src) {
			break
		}
		b := (R.Src[p/8] >> (7 - p%8)) & 1
		out = append(out, '0'+b)
	}
	return string(out)
}
