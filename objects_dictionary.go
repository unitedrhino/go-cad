// 本文件实现对象段 DICTIONARY（0x2A）与 XRECORD（0x4F）的解码：
// 布局对照 LibreDWG dwg.spec 对应块与 dwgread -v9 字段日志逐位验证；
// xdata 项解析（resbufValueType DXF 码段表）同在此处。
// 公共头 bitsize 定位随版本不同（见 objects_generic.go 的 dictBitsizePos）。
package cad

import (
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/objrec"
	"os"
	"strings"
)

// objDictionary DICTIONARY 对象：键名列表 + 项句柄列表（顺序一一对应）。
type objDictionary struct {
	handle        uint64     // 对象主句柄
	owner         uint64     // ownerhandle 绝对句柄
	objSizeBit    uint64     // bitsize：handle 流起点（相对 MS 字段之后的位）
	numReactors   int        // reactor 数量
	xdicMissing   bool       // R2004+：无 xdicobjhandle 标记
	numItems      int        // texts/itemHandles 数量
	cloning       uint16     // R2000b+ 克隆标志（DXF 281）
	isHardOwner   bool       // 硬拥有标志（DXF 280）
	texts         []string   // 键名列表（R2007+ 为 TU，更早为 TV）
	hdOffsetBits  uint64     // dat 段前导位（body 内 RL bitsize 起点；重编码位长校验用）
	textRawLens   []int      // texts 的原始 TV 位长（诊断用）
	textRawBits   string     // texts 段原始位串（0/1），重编码原样写回
	itemHandles   []uint64   // 项句柄列表（与 texts 顺序对应）
	RawHandleBits string     // handle 流原始位串（0/1），供重编码原样写回
	EedFields     []objField // EED 结构化字段（供 WDFLT 合并到通用对象）
	defaultID     uint64     // DICTIONARYWDFLT：默认项句柄（hdl 流尾部）
}

// decodeDictionaryObject 解析 DICTIONARY 对象（dwg.spec DWG_OBJECT(DICTIONARY)）。
// dat 流：[R2000-R2007 RL bitsize（H 之前）] + H handle + EED
// + [R13/R14 RL bitsize（EED 之后）] + BL num_reactors
// + [R2004+ B is_xdic_missing] + [R2013+ B has_ds_data] + BL numitems
// + [R2000b+ BS cloning] + RC is_hardowner + numitems×T 文字（R2007+ TU，更早 TV）。
// handle 流（bitsize 起）：ownerhandle + reactors + xdic + itemhandles×numitems。
func decodeDictionaryObject(r *bitstream.BitStream, rec *objrec.ObjectRecord, ver container.DwgVersion, r2013Plus bool) (*objDictionary, error) {
	return decodeDictionaryObjectFull(r, rec, ver, r2013Plus, false)
}

// decodeDictionaryObjectFull 解析 DICTIONARY；withDefault 为 true 时
// 按 DICTIONARYWDFLT 在 itemhandles 后追加读取 defaultid 句柄。
func decodeDictionaryObjectFull(r *bitstream.BitStream, rec *objrec.ObjectRecord, ver container.DwgVersion, r2013Plus bool, withDefault bool) (*objDictionary, error) {
	d := &objDictionary{}
	var err error
	// bitsize 定位策略：R2000-R2007 内联 RL 在最前；R13/R14 在 EED 后；
	// R2010+ 由记录头推导（无内联字段）
	bitsizePos := dictBitsizePos(ver)
	// dat 段前导位：RL bitsize 字段起点（body 内）；原始流中 bitsize RL
	// 之前可能有对象 section 头的残留位，重编码从 RL 占位起编，位长
	// 校验需补回前导
	d.hdOffsetBits = r.TellBits() - rec.BodyBitOffset
	if bitsizePos == bitsizePosHead {
		if d.objSizeBit, err = readInlineBitsize(r); err != nil {
			return nil, err
		}
	}
	if d.handle, err = readHandleValue(r); err != nil {
		return nil, err
	}
	if err = parseEEDChain(r, ver, &d.EedFields); err != nil {
		return nil, err
	}
	if bitsizePos == bitsizePosTail {
		if d.objSizeBit, err = readInlineBitsize(r); err != nil {
			return nil, err
		}
	}
	var numReactors uint32
	if numReactors, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if numReactors > 4096 {
		return nil, fmt.Errorf("cad: DICTIONARY reactors 异常 %d", numReactors)
	}
	d.numReactors = int(numReactors)
	if ver >= container.VerR2004 {
		if xdic, e := r.ReadB(); e != nil {
			return nil, e
		} else {
			d.xdicMissing = xdic == 1
		}
	}
	if r2013Plus {
		if _, e := r.ReadB(); e != nil { // has_ds_data
			return nil, e
		}
	}
	var numItems uint32
	if numItems, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if numItems > 100_000 {
		return nil, fmt.Errorf("cad: DICTIONARY 项数异常 %d", numItems)
	}
	d.numItems = int(numItems)
	// cloning BS 仅 R2000b+（R13/R14 无）；is_hardowner RC 自 R13c3
	// 起（AC1012 早于 R13c3 无此字段）。
	// DICTIONARYWDFLT（withDefault）例外：spec 中 cloning/is_hardowner
	// 为无条件字段（该类为后期补充，文件内字段恒存在）。
	if withDefault || (ver != container.VerR13 && ver != container.VerR14) {
		if d.cloning, err = r.ReadBS(); err != nil {
			return nil, err
		}
	}
	if withDefault || ver != container.VerR13 {
		if isHardOwner, e := r.ReadRC(); e != nil {
			return nil, e
		} else {
			d.isHardOwner = isHardOwner != 0
		}
	}
	// 文字：dat 流内联 T 序列（R2007+ TU = BS 长度 + UTF-16LE；更早为 TV）；
	// 记录原始 TV 位长与整段位串（长度含 \0 与否因写入方而异，重编码
	// 原样写回）
	textsStart := r.TellBits()
	for i := 0; i < d.numItems; i++ {
		tvStart := r.TellBits()
		var s string
		if ver >= container.VerR2007 {
			if s, err = r.ReadTU(); err != nil {
				return nil, err
			}
		} else {
			if s, err = r.ReadTV(0); err != nil {
				return nil, err
			}
		}
		d.textRawLens = append(d.textRawLens, int(r.TellBits()-tvStart))
		d.texts = append(d.texts, s)
	}
	d.textRawBits = bitstream.CollectBits(r, textsStart, r.TellBits())
	// handle 流起点：内联 bitsize 相对 MS 字段之后；R2010+ 无内联字段，
	// 直接取记录数据结束位（含 handle-stream-size 字段自身的位长）
	switch bitsizePos {
	case bitsizePosDerived:
		d.objSizeBit = rec.DataEndBit() - rec.BodyBitOffset - uint64(rec.HandleSizeFieldBits)
		r.SetBitPos(rec.DataEndBit())
	default:
		r.SetBitPos(rec.BodyBitOffset + d.objSizeBit)
	}
	// handle 流原始位串：起点（bitsize）至 body 尾，供重编码原样写回
	d.RawHandleBits = bitstream.CollectBits(r, r.TellBits(), uint64(len(r.Src))*8)
	if d.owner, err = readOwnerHandle(r, d.handle); err != nil {
		return nil, err
	}
	for i := 0; i < d.numReactors; i++ {
		if _, err = objrec.ReadHandleReference(r, d.handle); err != nil {
			return nil, err
		}
	}
	if !d.xdicMissing {
		if _, err = objrec.ReadHandleReference(r, d.handle); err != nil {
			return nil, err
		}
	}
	for i := 0; i < d.numItems; i++ {
		h, e := objrec.ReadHandleReference(r, d.handle)
		if e != nil {
			return nil, e
		}
		d.itemHandles = append(d.itemHandles, h)
	}
	if withDefault {
		if d.defaultID, err = objrec.ReadHandleReference(r, d.handle); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// objXrecord XRECORD 对象：应用自定义扩展数据容器。
type objXrecord struct {
	handle          uint64      // 对象主句柄
	owner           uint64      // ownerhandle 绝对句柄
	objSizeBit      uint64      // bitsize：handle 流起点（相对 MS 字段之后的位）
	numReactors     int         // reactor 数量
	xdicMissing     bool        // R2004+：无 xdicobjhandle 标记
	cloning         uint16      // R2000b+ 克隆标志（DXF 280）
	xdataSize       int         // 扩展数据字节数
	xdata           []xdataItem // 扩展数据类型化值序列
	numObjidHandles int         // objid 句柄数量（由 handle 流推导，流中无此字段，对应 dwgread JSON 的 num_objid_handles）
	objidHandles    []uint64    // objid 句柄向量
	EedFields       []objField  // EED 结构化字段（供 round-trip 重编码）
	RawHandleBits   string      // handle 流原始位串（0/1），供重编码原样写回
}

// XdataSize 返回 xdata 字节数。
func (x *objXrecord) XdataSize() int { return x.xdataSize }

// XdataItems 返回 xdata 类型化值序列。
func (x *objXrecord) XdataItems() []xdataItem { return x.xdata }

// decodeXrecordObject 解析 XRECORD 对象（dwg2.spec DWG_OBJECT(XRECORD)）。
// dat 流：[版本相关内联 RL bitsize] + H handle + EED + BL num_reactors
// + [R2004+ B is_xdic_missing] + [R2013+ B has_ds_data] + BL xdata_size
// + xdata 原始字节（内容暂不解析） + [R2000b+ BS cloning]。
// handle 流（bitsize 起）：ownerhandle + reactors + xdic + objid_handles 至流尾。
func decodeXrecordObject(r *bitstream.BitStream, rec *objrec.ObjectRecord, ver container.DwgVersion, r2013Plus bool) (*objXrecord, error) {
	x := &objXrecord{}
	var err error
	bitsizePos := dictBitsizePos(ver)
	if bitsizePos == bitsizePosHead {
		if x.objSizeBit, err = readInlineBitsize(r); err != nil {
			return nil, err
		}
	}
	if os.Getenv("CAD_DECODE_DBG") != "" {
		fmt.Fprintf(os.Stderr, "[xr] RL done objSizeBit=%d @%d\n", x.objSizeBit, r.TellBits())
	}
	if x.handle, err = readHandleValue(r); err != nil {
		return nil, err
	}
	if os.Getenv("CAD_DECODE_DBG") != "" {
		fmt.Fprintf(os.Stderr, "[xr] handle done @%d\n", r.TellBits())
	}
	if os.Getenv("CAD_DECODE_DBG") != "" {
		fmt.Fprintf(os.Stderr, "[xr] skipEED 前 @%d\n", r.TellBits())
	}
	if err = parseEEDChain(r, ver, &x.EedFields); err != nil {
		if os.Getenv("CAD_DECODE_DBG") != "" {
			fmt.Fprintf(os.Stderr, "[xr] EED err: %v\n", err)
		}
		return nil, err
	}
	if os.Getenv("CAD_DECODE_DBG") != "" {
		fmt.Fprintf(os.Stderr, "[xr] skipEED 后 @%d\n", r.TellBits())
	}
	if bitsizePos == bitsizePosTail {
		if x.objSizeBit, err = readInlineBitsize(r); err != nil {
			return nil, err
		}
	}
	var numReactors uint32
	if numReactors, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if os.Getenv("CAD_DECODE_DBG") != "" {
		fmt.Fprintf(os.Stderr, "[xr] num_reactors=%d @%d\n", numReactors, r.TellBits())
	}
	if numReactors > 4096 {
		return nil, fmt.Errorf("cad: XRECORD reactors 异常 %d", numReactors)
	}
	x.numReactors = int(numReactors)
	if ver >= container.VerR2004 {
		if xdic, e := r.ReadB(); e != nil {
			return nil, e
		} else {
			x.xdicMissing = xdic == 1
		}
	}
	if r2013Plus {
		if _, e := r.ReadB(); e != nil { // has_ds_data
			return nil, e
		}
	}
	var xdataSize uint32
	if xdataSize, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if os.Getenv("CAD_DECODE_DBG") != "" {
		fmt.Fprintf(os.Stderr, "[xr] xdata_size=%d @%d\n", xdataSize, r.TellBits())
	}
	if xdataSize > 1<<24 {
		return nil, fmt.Errorf("cad: XRECORD 扩展数据过大 %d", xdataSize)
	}
	x.xdataSize = int(xdataSize)
	if os.Getenv("CAD_DECODE_DBG") != "" {
		fmt.Fprintf(os.Stderr, "[xr] xdataSize=%d @%d 位串(36..100)=%v\n", x.xdataSize, r.TellBits(), bitstream.CollectBits(r, 36, 100))
	}
	// 扩展数据：类型化值序列（DXF 组码 + 对应类型值，字节定长区）
	if x.xdata, err = decodeXdataItems(r, x.xdataSize, ver >= container.VerR2007); err != nil {
		return nil, err
	}
	if os.Getenv("CAD_DECODE_DBG") != "" {
		fmt.Fprintf(os.Stderr, "[xr] xdata done @%d\n", r.TellBits())
	}
	// cloning BS 为 R2000b+ 字段（R13/R14 无）。注意流中没有
	// num_objid_handles 字段：LibreDWG dwg2.spec 中该值由解码端在
	// handle 流中推导（读到 handlestream_size 为止）
	if ver != container.VerR13 && ver != container.VerR14 {
		if x.cloning, err = r.ReadBS(); err != nil {
			return nil, err
		}
	}
	// handle 流起点：内联 bitsize 相对 MS 字段之后；R2010+ 无内联字段，
	// 直接取记录数据结束位（含 handle-stream-size 字段自身的位长）
	switch bitsizePos {
	case bitsizePosDerived:
		x.objSizeBit = rec.DataEndBit() - rec.BodyBitOffset - uint64(rec.HandleSizeFieldBits)
		r.SetBitPos(rec.DataEndBit())
	default:
		r.SetBitPos(rec.BodyBitOffset + x.objSizeBit)
	}
	// handle 流原始位串：起点（bitsize）至 body 尾，供重编码原样写回
	x.RawHandleBits = bitstream.CollectBits(r, r.TellBits(), uint64(len(r.Src))*8)
	if x.owner, err = readOwnerHandle(r, x.handle); err != nil {
		return nil, err
	}
	for i := 0; i < x.numReactors; i++ {
		if _, err = objrec.ReadHandleReference(r, x.handle); err != nil {
			return nil, err
		}
	}
	if !x.xdicMissing {
		if _, err = objrec.ReadHandleReference(r, x.handle); err != nil {
			return nil, err
		}
	}
	// objid 句柄：读到 handle 流尾（handlestream_size = body 尾 - bitsize，
	// 即 RawHandleBits 区间）为止；读到无效句柄即停（对应 spec 的
	// if (!FIELD_VALUE) break），数量记入 numObjidHandles
	{
		hdlEnd := r.TellBits() + uint64(len(x.RawHandleBits))
		for r.TellBits() < hdlEnd && x.numObjidHandles <= 4096 {
			h, e := objrec.ReadHandleReference(r, x.handle)
			if e != nil || h == 0 {
				break
			}
			x.objidHandles = append(x.objidHandles, h)
			x.numObjidHandles++
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
func dictBitsizePos(ver container.DwgVersion) objBitsizePos {
	switch {
	case ver >= container.VerR2010:
		return bitsizePosDerived
	case ver == container.VerR2000 || ver == container.VerR2004 || ver == container.VerR2007:
		return bitsizePosHead
	default:
		return bitsizePosTail
	}
}

// readInlineBitsize 读取内联 RL bitsize 并校验上界（相对 MS 字段之后的位）。
func readInlineBitsize(r *bitstream.BitStream) (uint64, error) {
	bitsize, err := r.ReadRL()
	if err != nil {
		return 0, err
	}
	if bitsize > 1<<28 {
		return 0, fmt.Errorf("cad: 对象 bitsize 异常 %d", bitsize)
	}
	return uint64(bitsize), nil
}

// readHandleValue 读取对象主句柄（H：4bit code + 4bit size + size 字节）。
func readHandleValue(r *bitstream.BitStream) (uint64, error) {
	h, err := r.ReadH()
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
func parseEEDChain(r *bitstream.BitStream, ver container.DwgVersion, out *[]objField) error {
	start := r.TellBits()
	savedLen := len(*out)
	i := 0
	fail := func(err error) error {
		// 回退：截断结构化字段，收集整条链的原始位串（供重编码原样
		// 写回），并按 skip 语义推进到链尾
		r.SetBitPos(start)
		*out = (*out)[:savedLen]
		raw, err2 := skipEEDChainCollect(r)
		if err2 != nil {
			return err2
		}
		*out = append(*out, objField{"eed_raw_bits", raw})
		return nil
	}
	for {
		sz, err := r.ReadBS()
		if err != nil {
			return fail(err)
		}
		if os.Getenv("CAD_DECODE_DBG") != "" {
			fmt.Fprintf(os.Stderr, "[pEED] sz=%d @%d r.pos=%d dataLen=%d\n", sz, r.TellBits(), r.Pos, len(r.Src))
		}
		if sz == 0 {
			return nil
		}
		h, err := r.ReadH()
		if err != nil {
			return fail(err)
		}
		if int(sz) > len(r.Src)-r.Pos {
			return fail(bitstream.ErrUnexpectedEOF)
		}
		end := r.Pos + int(sz)
		first := true
		for r.Pos < end {
			code, err := r.ReadRC()
			if err != nil {
				return fail(err)
			}
			if os.Getenv("CAD_DECODE_DBG") != "" {
				fmt.Fprintf(os.Stderr, "[eed] blk sz=%d code=%d byte@%d end@%d pos=%d bits=%v\n", sz, code, r.Pos, end, r.TellBits(), dumpBits(r, r.TellBits(), 40))
			}
			p := fmt.Sprintf("eed[%d].", i)
			var val any
			switch code {
			case 0:
				if ver >= container.VerR2007 {
					l, e := r.ReadRS()
					if e != nil {
						return fail(e)
					}
					us := make([]uint16, l)
					for j := range us {
						us[j], e = r.ReadRS()
						if e != nil {
							return fail(e)
						}
					}
					// LibreDWG bit_read_TU 语义：NUL 终止，输出不含 NUL
					val = strings.SplitN(string(bitstream.Utf16Decode(us)), "\x00", 2)[0]
				} else {
					l, e := r.ReadRC()
					if e != nil {
						return fail(e)
					}
					cpHi, e := r.ReadRC()
					if e != nil {
						return fail(e)
					}
					cpLo, e := r.ReadRC()
					if e != nil {
						return fail(e)
					}
					cp := uint16(cpHi)<<8 | uint16(cpLo) // RS_BE 大端
					b := make([]byte, l)
					for j := 0; j < int(l); j++ {
						var cb uint8
						var e2 error
						cb, e2 = r.ReadRC()
						if e2 != nil {
							return fail(e2)
						}
						b[j] = cb
					}
					// TV 同为 NUL 终止字符串
					val = strings.SplitN(bitstream.DecodeCodepage(b, cp), "\x00", 2)[0]
				}
			case 1:
				v, e := r.ReadRS()
				if e != nil {
					return fail(e)
				}
				val = int64(v)
			case 2:
				v, err := r.ReadRC()
				if err != nil {
					return fail(err)
				}
				val = int64(v)
			case 3:
				// layer：RS + RLL（LibreDWG 同款双读）
				if _, e := r.ReadRS(); e != nil {
					return fail(e)
				}
				v, e := r.ReadBLL()
				if e != nil {
					return fail(e)
				}
				val = int64(v)
			case 4:
				l, e := r.ReadRC()
				if e != nil {
					return fail(e)
				}
				b := make([]byte, l)
				for j := 0; j < int(l); j++ {
					cb, e2 := r.ReadRC()
					if e2 != nil {
						return fail(e2)
					}
					b[j] = cb
				}
				val = fmt.Sprintf("%X", b)
			case 5:
				// entity：RLL 大端
				v, e := r.ReadBitsMsb(64)
				if e != nil {
					return fail(e)
				}
				val = int64(v)
			case 10, 11, 12, 13, 14, 15:
				pt := make([]float64, 3)
				for j := 0; j < 3; j++ {
					var e error
					pt[j], e = r.ReadRD()
					if e != nil {
						return fail(e)
					}
				}
				val = pt
			case 40, 41, 42:
				f, e := r.ReadRD()
				if e != nil {
					return fail(e)
				}
				val = f
			case 70:
				v, e := r.ReadRS()
				if e != nil {
					return fail(e)
				}
				val = int64(int16(v))
			case 71:
				v, e := r.ReadRL()
				if e != nil {
					return fail(e)
				}
				val = int64(int32(v))
			default:
				val = int64(0)
			}
			*out = append(*out,
				objField{p + "code", int64(code)},
				objField{p + "value", val})
			if first {
				*out = append(*out,
					objField{p + "size", int64(sz)},
					objField{p + "handle", []uint64{uint64(h.Code), uint64(h.Counter), h.Value}})
				first = false
			}
			i++
		}
		r.Pos = end
	}
}

// skipEEDChainCollect 跳过 EED 链并返回链的完整位串（0/1 字符串，
// 含尾字节对齐填充），供重编码原样写回。
func skipEEDChainCollect(r *bitstream.BitStream) (string, error) {
	start := r.TellBits()
	if err := skipEEDChain(r); err != nil {
		return "", err
	}
	end := r.TellBits()
	var out []byte
	for p := start; p < end; p++ {
		if int(p/8) >= len(r.Src) {
			break
		}
		b := (r.Src[p/8] >> (7 - p%8)) & 1
		out = append(out, '0'+b)
	}
	return string(out), nil
}

func skipEEDChain(r *bitstream.BitStream) error {
	for {
		sz, err := r.ReadBS()
		if err != nil {
			return err
		}
		if sz == 0 {
			return nil
		}
		if _, err = r.ReadH(); err != nil {
			return err
		}
		if _, err = r.ReadRCS(int(sz)); err != nil {
			return err
		}
	}
}

// readOwnerHandle 读取 ownerhandle（H，绝对引用）。
func readOwnerHandle(r *bitstream.BitStream, cur uint64) (uint64, error) {
	return objrec.ReadHandleReference(r, cur)
}

// xdataItem XRECORD 扩展数据的一个类型化值项（LibreDWG Dwg_Resbuf）。
// Kind 决定哪个值字段有效；Val 返回与 dwgread JSON 输出同形的值：
// 整数类为 int64、REAL 为 float64、POINT3D 为 [3]float64、
// STRING 为 string、BINARY 为小写 hex 字符串、HANDLE/OBJECTID 为 uint64。
type xdataItem struct {
	Code  int // DXF 组码（RS 存储）
	Kind  xdataKind
	Int   int64
	Float float64
	Point [3]float64
	Str   string
	Bytes []byte
}

// KindName 返回类型的调试名（kind 枚举：0=Invalid 1=String 2=Real 3=Int8 4=Int16 5=Int32 6=Point3D 7=Binary 8=Handle）。
func (it xdataItem) KindName() string {
	names := [...]string{"Invalid", "String", "Real", "Int8", "Int16", "Int32", "Int64", "Point3D", "Binary", "Handle", "Unknown"}
	if int(it.Kind) < len(names) {
		return names[it.Kind]
	}
	return "Unknown"
}

// Val 返回 JSON 同形值
func (it xdataItem) Val() any {
	switch it.Kind {
	case xdataReal:
		return it.Float
	case xdataPoint3D:
		return it.Point
	case xdataString:
		return it.Str
	case xdataBinary:
		return fmt.Sprintf("%X", it.Bytes)
	default:
		return it.Int
	}
}

// xdataKind 扩展数据值类型（对应 LibreDWG DWG_VT_*）
type xdataKind int

const (
	xdataInvalid xdataKind = iota
	xdataString
	xdataReal
	xdataInt8
	xdataInt16
	xdataInt32
	xdataInt64
	xdataPoint3D
	xdataBinary
	xdataHandle // HANDLE / OBJECTID
	xdataBool
)

// resbufValueType DXF 组码 → 值类型（对照 LibreDWG dwg_resbuf_value_type）
func resbufValueType(gc int) xdataKind {
	switch {
	case gc < 0:
		return xdataHandle
	case gc <= 4:
		return xdataString
	case gc == 5:
		return xdataHandle
	case gc <= 9:
		return xdataString
	case gc <= 37:
		return xdataPoint3D
	case gc <= 59:
		return xdataReal
	case gc <= 79:
		return xdataInt16
	case gc <= 99:
		return xdataInt32
	case gc <= 102:
		return xdataString
	case gc == 105:
		return xdataHandle
	case gc <= 109:
		return xdataInvalid
	case gc <= 139:
		return xdataPoint3D
	case gc <= 149:
		return xdataReal
	case gc <= 169:
		return xdataInt64
	case gc <= 179:
		return xdataInt16
	case gc <= 209:
		return xdataInvalid
	case gc <= 269:
		return xdataPoint3D
	case gc <= 279:
		return xdataInt16
	case gc <= 289:
		return xdataInt8
	case gc <= 299:
		return xdataBool
	case gc <= 309:
		return xdataString
	case gc <= 319:
		return xdataBinary
	case gc <= 329:
		return xdataHandle
	case gc <= 369:
		return xdataHandle // OBJECTID：软/硬指针与拥有句柄
	case gc <= 389:
		return xdataInt16
	case gc <= 399:
		return xdataHandle
	case gc <= 409:
		return xdataInt16
	case gc <= 419:
		return xdataString
	case gc <= 429:
		return xdataInt32
	case gc <= 439:
		return xdataString
	case gc <= 459:
		return xdataInt32
	case gc <= 469:
		return xdataReal
	case gc <= 479:
		return xdataString
	case gc == 999:
		return xdataString
	// 1004 二进制必须先于 1009 段匹配：Go switch 按序命中，
	// 放在 gc<=1009 之后将永不可达（LibreDWG dwg_resbuf_value_type 同序）
	case gc == 1004:
		return xdataBinary
	case gc <= 1009:
		return xdataString
	case gc <= 1039:
		return xdataPoint3D
	case gc <= 1042:
		return xdataReal
	case gc <= 1069:
		return xdataPoint3D
	case gc <= 1070:
		return xdataInt16
	case gc == 1071:
		return xdataInt32
	default:
		return xdataInvalid
	}
}

// decodeXdataItems 解析 XRECORD 扩展数据：每项 = RS 类型码 + 类型对应值，
// 总长 sizeBytes 字节（对照 LibreDWG dwg_decode_xdata）。
// 终点按字节位置判定（位流原语读取，与参考实现行为一致）。
func decodeXdataItems(r *bitstream.BitStream, sizeBytes int, r2007Plus bool) ([]xdataItem, error) {
	startBits := r.TellBits()
	endBits := startBits + uint64(sizeBytes)*8
	items := make([]xdataItem, 0, 8)
	for uint64(r.TellBits()) < endBits {
		code, err := r.ReadRS()
		if err != nil {
			return nil, err
		}
		if uint64(r.TellBits()) >= endBits {
			// 类型码后已到终点：丢弃（与参考实现防死循环一致）
			break
		}
		if code >= 2000 {
			break
		}
		it := xdataItem{Code: int(code), Kind: resbufValueType(int(code))}
		switch it.Kind {
		case xdataString:
			length, e := r.ReadRS()
			if e != nil {
				return nil, e
			}
			if r2007Plus {
				if int(length) > 0 && uint64(r.TellBits())+uint64(length)*16 <= endBits {
					var sb []byte
					for i := uint16(0); i < length; i++ {
						ch, e := r.ReadRS()
						if e != nil {
							return nil, e
						}
						sb = append(sb, byte(ch), byte(ch>>8))
					}
					it.Str = container.DecodeUTF16LE(sb)
				}
			} else {
				cp, e := r.ReadRC()
				if e != nil {
					return nil, e
				}
				if uint64(r.TellBits())+uint64(length)*8 <= endBits {
					b, e := r.ReadBitsBytes(int(length))
					if e != nil {
						return nil, e
					}
					it.Str = bitstream.DecodeCodepage(b, uint16(cp))
				}
			}
		case xdataReal:
			if uint64(r.TellBits())+8 > endBits {
				break
			}
			if it.Float, err = r.ReadRD(); err != nil {
				return nil, err
			}
		case xdataBool, xdataInt8:
			if uint64(r.TellBits())+8 > endBits {
				break
			}
			b, e := r.ReadRC()
			if e != nil {
				return nil, e
			}
			it.Int = int64(b)
		case xdataInt16:
			if uint64(r.TellBits())+2 > endBits {
				break
			}
			v, e := r.ReadRS()
			if e != nil {
				return nil, e
			}
			it.Int = int64(int16(v))
		case xdataInt32:
			if uint64(r.TellBits())+4 > endBits {
				break
			}
			v, e := r.ReadRL()
			if e != nil {
				return nil, e
			}
			it.Int = int64(int32(v))
		case xdataInt64:
			if uint64(r.TellBits())+8 > endBits {
				break
			}
			v, e := r.ReadRLL()
			if e != nil {
				return nil, e
			}
			it.Int = int64(v)
		case xdataPoint3D:
			if uint64(r.TellBits())+24 > endBits {
				break
			}
			if it.Point[0], err = r.ReadRD(); err != nil {
				return nil, err
			}
			if it.Point[1], err = r.ReadRD(); err != nil {
				return nil, err
			}
			if it.Point[2], err = r.ReadRD(); err != nil {
				return nil, err
			}
		case xdataBinary:
			sz, e := r.ReadRC()
			if e != nil {
				return nil, e
			}
			if uint64(r.TellBits())+uint64(sz)*8 <= endBits {
				b, e := r.ReadBitsBytes(int(sz))
				if e != nil {
					return nil, e
				}
				it.Bytes = b
			}
		case xdataHandle:
			if uint64(r.TellBits())+8 > endBits {
				break
			}
			v, e := r.ReadRLL()
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
func dumpBits(r *bitstream.BitStream, pos uint64, n int) string {
	out := make([]byte, 0, n)
	for i := 0; i < n; i++ {
		p := pos + uint64(i)
		if int(p/8) >= len(r.Src) {
			break
		}
		b := (r.Src[p/8] >> (7 - p%8)) & 1
		out = append(out, '0'+b)
	}
	return string(out)
}
