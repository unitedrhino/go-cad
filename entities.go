// entities.go 实现 DWG 实体解码：公共实体头（R13/R14 专用解析器 +
// R2000~R2018 各版本候选布局，headParsersForVersion）、handle 流解析，
// 以及渲染路径所需的图元字段解码（LINE/LWPOLYLINE/CIRCLE/ARC/TEXT/MTEXT/POINT/ELLIPSE/INSERT）。
// 字段序与位语义对照 LibreDWG dwg.spec / ODA 规范逐位校准。
package cad

import (
	"encoding/binary"
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/objrec"
	"math"
	"os"
	"strconv"
	"unicode"
)

// entColor 实体颜色：ACI 索引与/或 true color（0x00RRGGBB）。
// R2004+ 颜色段为 ENC 编码（BS raw = flag<<8|index，flag 位驱动 rgb/alpha），
// flag/rgb/alpha 原始值保存后供审计导出 color.rgb/color.flag/color.alpha 系子键。
type entColor struct {
	index     uint16
	hasIndex  bool
	trueColor uint32 // R2004+ 真彩色完整 32 位（首字节为索引/alpha，取色用低 24 位）
	hasTrue   bool
	flag      uint16 // ENC flag（raw>>8）：0x20 有 alpha、0x40 有句柄、0x80 有 rgb
	alphaRaw  uint32 // 透明度原始 32 位（alphaType<<24|alpha）
	alphaType uint8  // 0=ByLayer 1=ByBlock 3=按值
	alpha     uint8  // 透明度值
	hasAlpha  bool   // flag&0x20 是否读取了 alpha 段
}

// commonEntityHead 公共实体头（所有实体共享的解码结果）。
type commonEntityHead struct {
	objSizeBit     uint64 // 数据区结束位（handle 流起点）
	auditBitsize   uint64 // gold 口径 bitsize（数据区起点到 handle 流的位距，审计导出用）
	handle         uint64
	color          entColor
	entityMode     uint8
	numReactors    uint32
	xdicMissing    bool
	hasDsBinary    bool // R2013+ 才有该位
	ltypeFlags     uint8
	plotstyleFlgs  uint8
	materialFlags  uint8
	ltypeScale     float64
	shadowFlags    uint8
	visualStyle    [3]bool
	invisible      int16    // 不可见标志（BS 60）
	linewt         uint16   // 线宽（RC 370）
	r13r14         bool     // R13/R14 头部标记
	previewExists  bool     // 预览存在标志（preview_exists 位）
	preview        []byte   // 预览图形数据（preview_exists=1 时读入，审计导出用）
	noLinks        bool     // nolinks 位（R13~R2002 输出，块内实体常为 1）
	prevNextLinks  bool     // R13~R2000 语义：!nolinks 时 handle 流含 prev/next_entity
	isByLayerLtype bool     // isbylayerlt 位（R13/R14 输出）
	eed            []entEED // EED 应用数据链（组码对已解析，审计导出用）
	// 公共 handle 流的次级句柄（批次 B 建模：原读后即弃，保存供
	// gold 键导出对照；0 表示无该句柄）
	prevEntity      uint64 // prev_entity 链接句柄（R13~R2000）
	nextEntity      uint64 // next_entity 链接句柄（R13~R2000）
	xdicObjHandle   uint64 // 扩展字典句柄（xdic_missing=0 时）
	plotstyleHandle uint64 // 绘图样式句柄（plotstyle_flags=3 时）
}

// cadTraceHandle 环境变量 CAD_TRACE_HANDLE 指定的调试 handle；
// 设置后仅对该 handle 的实体输出逐字段位级 trace（与参考实现对齐用）。
var cadTraceHandle = func() uint64 {
	v := os.Getenv("CAD_TRACE_HANDLE")
	h, _ := strconv.ParseUint(v, 10, 64)
	return h
}()

// cadTraceField 输出一条字段 trace：名称、位区间、宽度、数值。
func cadTraceField(on bool, from, to uint64, name, val string) {
	if on {
		fmt.Fprintf(os.Stderr, "[head] %14s bits=[%d..%d) w=%d val=%s\n", name, from, to, to-from, val)
	}
}

// cadTraceFieldInt 整数字段 trace（cadTraceField 的惰性变体）：trace 关闭
// 时不做任何格式化求值——调用点遍布公共头解码热路径，原样内联
// fmt.Sprintf("%d", v) 会在 on=false 时也付出格式化开销（pprof ~6%）。
// 有符号格式化（%d 语义，负数带 - 号），与 fmt 输出逐字符一致。
func cadTraceFieldInt(on bool, from, to uint64, name string, v int64) {
	if on {
		cadTraceField(on, from, to, name, strconv.FormatInt(v, 10))
	}
}

// cadTraceFieldF64 浮点字段 trace（惰性变体，理由同 cadTraceFieldInt）：
// 'g' + 最短表示与 fmt %g 无精度格式输出一致。
func cadTraceFieldF64(on bool, from, to uint64, name string, v float64) {
	if on {
		cadTraceField(on, from, to, name, strconv.FormatFloat(v, 'g', -1, 64))
	}
}

// entEEDPair EED 应用数据中的单个组码对（对齐 gold JSON 展平结构）。
type entEEDPair struct {
	Code  int64
	Value any // string/float64/int64；二进制/句柄/3RD 为切片（审计不比对）
}

// entEED 单个 EED 项：应用句柄数据块（size 字节 raw 解析出的组码对序列）。
type entEED struct {
	Size  int64
	Pairs []entEEDPair
}

// parseEntityEEDChain 解析公共实体头的 EED 链：BS size → H 应用句柄 → raw 字节，
// raw 内按 LibreDWG dwg_decode_eed_data 的组码表解析 code/value 对。
// unicodeText 表示 R2007+ 的 code-0 字符串为 UTF-16。
// 返回链与解析是否成功（失败时调用方按头解析失败处理）。
func parseEntityEEDChain(r *bitstream.BitStream, unicodeText bool) ([]entEED, bool) {
	var out []entEED
	for {
		extSize, err := r.ReadBS()
		if err != nil {
			return out, false
		}
		if extSize == 0 {
			return out, true
		}
		if _, err := r.ReadH(); err != nil {
			return out, false
		}
		raw, err := r.ReadRCS(int(extSize))
		if err != nil {
			return out, false
		}
		out = append(out, entEED{Size: int64(extSize), Pairs: parseEEDPairs(raw, unicodeText)})
	}
}

// parseEEDPairs 解析 EED 项 raw 字节中的组码对。未知组码时放弃剩余
// （对齐 LibreDWG 跳到项尾的行为）。
func parseEEDPairs(raw []byte, unicodeText bool) []entEEDPair {
	var pairs []entEEDPair
	pos := 0
	need := func(n int) bool { return pos+n <= len(raw) }
	rd := func() int64 { b := raw[pos]; pos++; return int64(b) }
	rs := func() int64 { v := binary.LittleEndian.Uint16(raw[pos:]); pos += 2; return int64(v) }
	rd64 := func() float64 {
		v := math.Float64frombits(binary.LittleEndian.Uint64(raw[pos:]))
		pos += 8
		return v
	}
	for pos < len(raw) {
		code := rd()
		var val any
		ok := true
		switch code {
		case 0: // 字符串；R2007+ 为 RS len + TU，此前为 RC len + [RS_BE codepage] + bytes
			var l int
			if unicodeText {
				// R2007+ 的 code-0 串：len 为 RS（2 字节 LE）+ TU 字符流
				// （trace ex2007：raw 00 18 00 41 00 63…，与旧版 RC len +
				// 跳 2 字节读法错位 2 字节，LIGHT EED 实证）
				if !need(2) {
					ok = false
					break
				}
				l = int(rs())
				if !need(l * 2) {
					ok = false
					break
				}
				var us []uint16
				for i := 0; i < l; i++ {
					us = append(us, uint16(rs()))
				}
				val = utf16BEToString(us)
			} else {
				if !need(1) {
					ok = false
					break
				}
				l = int(rd())
				if !need(2 + l) {
					ok = false
					break
				}
				// codepage 为 RS_BE 大端（LibreDWG bit_read_RS_BE），按文档
				// 码页族解码（fzw EED 中文串按 GBK 实证）
				cp := uint16(raw[pos])<<8 | uint16(raw[pos+1])
				pos += 2
				val = bitstream.DecodeCodepage(raw[pos:pos+l], cp)
				pos += l
			}
		case 1: // appid index RS
			if !need(2) {
				ok = false
			} else {
				val = rs()
			}
		case 2: // open/close RC
			if !need(1) {
				ok = false
			} else {
				val = rd()
			}
		case 3, 5: // RLL 8 字节
			if !need(8) {
				ok = false
			} else {
				val = []int64{int64(binary.LittleEndian.Uint64(raw[pos : pos+8]))}
				pos += 8
			}
		case 4: // 二进制：RC len + bytes
			if !need(1) {
				ok = false
				break
			}
			l := int(rd())
			if !need(l) {
				ok = false
				break
			}
			val = append([]byte(nil), raw[pos:pos+l]...)
			pos += l
		case 10, 11, 12, 13, 14, 15: // 3RD
			if !need(24) {
				ok = false
			} else {
				var a []float64
				for i := 0; i < 3; i++ {
					a = append(a, rd64())
				}
				val = a
			}
		case 40, 41, 42: // RD
			if !need(8) {
				ok = false
			} else {
				val = rd64()
			}
		case 70: // RS（LibreDWG out_json VALUE_RSd 强转 int16 有符号输出）
			if !need(2) {
				ok = false
			} else {
				val = int64(int16(rs()))
			}
		case 71: // RL（LibreDWG out_json VALUE_RLd 强转 int32 有符号输出）
			if !need(4) {
				ok = false
			} else {
				val = int64(int32(binary.LittleEndian.Uint32(raw[pos:])))
				pos += 4
			}
		default:
			ok = false
		}
		pairs = append(pairs, entEEDPair{Code: code, Value: val})
		if !ok {
			break
		}
	}
	return pairs
}

// utf16BEToString UTF-16 码点序列转字符串。
func utf16BEToString(us []uint16) string {
	runes := make([]rune, 0, len(us))
	for i := 0; i < len(us); i++ {
		if i+1 < len(us) && us[i] >= 0xD800 && us[i] < 0xDC00 && us[i+1] >= 0xDC00 && us[i+1] < 0xE000 {
			runes = append(runes, ((rune(us[i])-0xD800)<<10|(rune(us[i+1])-0xDC00))+0x10000)
			i++
			continue
		}
		runes = append(runes, rune(us[i]))
	}
	return string(runes)
}

// parseEntityColorHead 解析公共实体头中的颜色段（R13+ 全版本同构，
// 已经多版本样本逐位 trace 验证）。位流结构为可变长 BS（R2004+ ENC raw）：
// 首位 1 → 短格式（次位 1=ByLayer 256、0=ByBlock 0）；首位 0 → 次位
// 1=RC 索引、0=RS 16 位 raw（raw=flag<<8|index，flag 0x80→rgb BL、
// 0x20→alpha BL）。raw 的 flag/index/alpha 原始值保存供审计导出。
// on 为 trace 开关（CAD_TRACE_HANDLE 调试用）。
func parseEntityColorHead(r *bitstream.BitStream, head *commonEntityHead, on bool) error {
	color := &head.color
	// color ENC（LibreDWG common_entity_data.spec SINCE R_2004a）：BS 读出
	// 16 位 raw，flag=raw>>8、index=raw&0x1FF；raw 的 BS 捷径码对应：
	// 11→256(ByLayer)、10→0(ByBlock)、01→RC 单字节 index、00→RS 完整值。
	// noLinks/second（mode）即该 BB 码的两个位。
	noLinks, err := r.ReadB()
	if err != nil {
		return err
	}
	cadTraceFieldInt(on, r.TellBits()-1, r.TellBits(), "no_links", int64(noLinks))
	color.hasIndex = true
	if noLinks != 0 {
		second, err := r.ReadB()
		if err != nil {
			return err
		}
		cadTraceFieldInt(on, r.TellBits()-1, r.TellBits(), "color.unknown", int64(second))
		if second == 1 {
			color.index = 256 // ByLayer：raw=0x100
			color.flag = 1
		} else {
			color.index = 0 // ByBlock：raw=0
			color.flag = 0
		}
		return nil
	}
	mode, err := r.ReadB()
	if err != nil {
		return err
	}
	cadTraceFieldInt(on, r.TellBits()-1, r.TellBits(), "color.mode", int64(mode))
	if mode == 1 {
		idx, err := r.ReadRC()
		if err != nil {
			return err
		}
		color.index = uint16(idx)
		color.flag = 0 // raw=idx（<256），flag=raw>>8 恒 0
		cadTraceFieldInt(on, r.TellBits()-8, r.TellBits(), "color.index", int64(idx))
		return nil
	}
	pos := r.TellBits()
	flags, err := r.ReadRS()
	if err != nil {
		return err
	}
	if on {
		cadTraceField(on, pos, r.TellBits(), "color.flags", fmt.Sprintf("%#06x", flags))
	}
	color.index = flags & 0x01FF
	color.flag = flags >> 8
	// spec 顺序：flag&0x20 alpha 在前；flag&0x40 句柄（在 handle 流，
	// dat 不占位）否则 flag&0x80 读 rgb——两者互斥
	if flags&0x2000 != 0 { // alpha（flag 0x20，透明度）
		pos = r.TellBits()
		raw, err := r.ReadBL()
		if err != nil {
			return err
		}
		color.alphaRaw = uint32(raw)
		color.alphaType = uint8(raw >> 24)
		color.alpha = uint8(raw)
		color.hasAlpha = true
		if on {
			cadTraceField(on, pos, r.TellBits(), "color.alpha", fmt.Sprintf("%#x", raw))
		}
	}
	if flags&0x4000 == 0 && flags&0x8000 != 0 { // flag 0x80：rgb（0x40 优先）
		pos = r.TellBits()
		rgb, err := r.ReadBL()
		if err != nil {
			return err
		}
		// 保留完整 32 位（LibreDWG _obj->rgb 语义，首字节为索引/alpha；
		// 导出 rgb 键时 %06x 超过 24 位自然输出 8 位 hex，消费侧取色已自带 &0xFFFFFF）
		color.trueColor = uint32(rgb)
		color.hasTrue = true
		if on {
			cadTraceField(on, pos, r.TellBits(), "color.rgb", fmt.Sprintf("%#x", rgb))
		}
	}
	return nil
}

// parseCommonEntityHeadR14 解析 R13/R14 公共实体头（ODA 规范布局，
// ODA 规范 R13/R14 布局对齐）：
// H + EED + pic(B+RL+data) + objSize RL（单位=位，位于 entmode 之前）+
// entmode BB + reactors BL + byLayerLtype B + noLinks B + color BS +
// ltypeScale BD + invisibility BS。无 xdic/ds/material/visual/lineweight。
func parseCommonEntityHeadR14(r *bitstream.BitStream, _ uint64) (commonEntityHead, error) {
	var head commonEntityHead
	trOn := cadTraceHandle != 0

	h, err := r.ReadH()
	if err != nil {
		return head, err
	}
	head.handle = h.Value

	// EED 链（含组码对解析，供审计导出）
	var eedOK bool
	if head.eed, eedOK = parseEntityEEDChain(r, false); !eedOK {
		return head, fmt.Errorf("cad: EED 链解析失败")
	}

	// 图形图像（picture）：RL 尺寸
	picFlag, err := r.ReadB()
	if err != nil {
		return head, err
	}
	head.previewExists = picFlag == 1
	if picFlag == 1 {
		graphicSize, err := r.ReadRL()
		if err != nil {
			return head, err
		}
		if uint64(graphicSize) > uint64(len(r.Src))*8 {
			return head, fmt.Errorf("cad: 图形图像大小异常 %d", graphicSize)
		}
		if head.preview, err = r.ReadRCS(int(graphicSize)); err != nil {
			return head, err
		}
	}

	objSize, err := r.ReadRL()
	if err != nil {
		return head, err
	}
	// R13/R14 流内 RL objSize 单位为位；其值即 gold bitsize（数据区起点到 handle 流）
	head.objSizeBit = uint64(objSize)
	head.auditBitsize = head.objSizeBit
	head.r13r14 = true
	// R13/R14 实体 handle 流含 prev/next_entity（!nolinks 时）
	head.prevNextLinks = true
	pos := r.TellBits()
	mode, err := r.ReadBB()
	if err != nil {
		return head, err
	}
	head.entityMode = mode
	cadTraceFieldInt(trOn, pos, r.TellBits(), "entmode", int64(mode))
	pos = r.TellBits()
	reactors, err := r.ReadBL()
	if err != nil {
		return head, err
	}
	if reactors > 1<<20 {
		return head, fmt.Errorf("cad: reactor 数量异常 %d", reactors)
	}
	head.numReactors = reactors
	cadTraceFieldInt(trOn, pos, r.TellBits(), "reactors", int64(reactors))
	// is_bylayer_ltype：1 → ltype flags 0，0 → 3（handle 流需读 ltype 句柄）
	pos = r.TellBits()
	byLayerLtype, err := r.ReadB()
	if err != nil {
		return head, err
	}
	head.isByLayerLtype = byLayerLtype == 1
	if byLayerLtype == 1 {
		head.ltypeFlags = 0
	} else {
		head.ltypeFlags = 3
	}
	if trOn {
		cadTraceField(trOn, pos, r.TellBits(), "bylayer_ltype", fmt.Sprintf("%d->%d", byLayerLtype, head.ltypeFlags))
	}
	pos = r.TellBits()
	noLinks, err := r.ReadB()
	if err != nil {
		return head, err
	}
	head.noLinks = noLinks == 1
	cadTraceFieldInt(trOn, pos, r.TellBits(), "no_links", int64(noLinks))
	// 颜色：BS 索引
	pos = r.TellBits()
	colorIdx, err := r.ReadBS()
	if err != nil {
		return head, err
	}
	head.color.index = colorIdx
	head.color.hasIndex = true
	head.color.flag = colorIdx >> 8
	cadTraceFieldInt(trOn, pos, r.TellBits(), "color.index", int64(colorIdx))
	pos = r.TellBits()
	if head.ltypeScale, err = r.ReadBD(); err != nil {
		return head, err
	}
	cadTraceFieldF64(trOn, pos, r.TellBits(), "ltype_scale", head.ltypeScale)
	pos = r.TellBits()
	if iv, err := r.ReadBS(); err != nil { // invisibility
		return head, err
	} else {
		head.invisible = int16(iv)
	}
	cadTraceFieldInt(trOn, pos, r.TellBits(), "invisibility", int64(head.invisible))
	return head, nil
}

// parseCommonEntityHeadR2013 解析 R2010+/R2013+ 公共实体头。
// dataEndBit 为对象数据区结束位（由记录头计算，R2010+ 不在流内存储 objSize）。
func parseCommonEntityHeadR2013(r *bitstream.BitStream, dataEndBit uint64) (commonEntityHead, error) {
	var head commonEntityHead
	head.objSizeBit = dataEndBit
	head.auditBitsize = dataEndBit

	h, err := r.ReadH()
	if err != nil {
		return head, err
	}
	head.handle = h.Value

	// EED（扩展字典数据）链：BS 长度，0 结束；每项为 H 应用句柄 + length 字节。
	var eedOK bool
	if head.eed, eedOK = parseEntityEEDChain(r, false); !eedOK {
		return head, fmt.Errorf("cad: EED 链解析失败")
	}

	// 图形图像（picture）标志与数据
	picFlag, err := r.ReadB()
	if err != nil {
		return head, err
	}
	head.previewExists = picFlag == 1
	if picFlag == 1 {
		graphicSize, err := r.ReadBLL()
		if err != nil {
			return head, err
		}
		if graphicSize > uint64(len(r.Src))*8 {
			return head, fmt.Errorf("cad: 图形图像大小异常 %d", graphicSize)
		}
		if head.preview, err = r.ReadRCS(int(graphicSize)); err != nil {
			return head, err
		}
	}

	trOn := os.Getenv("CAD_HEAD_TRACE") == "542" && true
	pos0 := r.TellBits()
	mode, err := r.ReadBB()
	if err != nil {
		return head, err
	}
	head.entityMode = mode
	cadTraceFieldInt(trOn, pos0, r.TellBits(), "entmode", int64(mode))
	reactors, err := r.ReadBL()
	if err != nil {
		return head, err
	}
	if reactors > 1<<20 {
		return head, fmt.Errorf("cad: reactor 数量异常 %d", reactors)
	}
	head.numReactors = reactors
	cadTraceFieldInt(trOn, pos0, r.TellBits(), "reactors", int64(reactors))
	if flag, err := r.ReadB(); err != nil {
		return head, err
	} else {
		head.xdicMissing = flag == 1
		cadTraceFieldInt(trOn, pos0, r.TellBits(), "xdic_missing", int64(flag))
	}
	if flag, err := r.ReadB(); err != nil {
		return head, err
	} else {
		head.hasDsBinary = flag == 1
		cadTraceFieldInt(trOn, pos0, r.TellBits(), "has_ds", int64(flag))
	}

	if err := parseEntityColorHead(r, &head, trOn); err != nil {
		return head, err
	}
	if head.ltypeScale, err = r.ReadBD(); err != nil { // ltype scale
		return head, err
	}
	cadTraceFieldF64(trOn, pos0, r.TellBits(), "ltype_scale", head.ltypeScale)
	if head.ltypeFlags, err = r.ReadBB(); err != nil {
		return head, err
	}
	if head.plotstyleFlgs, err = r.ReadBB(); err != nil {
		return head, err
	}
	if head.materialFlags, err = r.ReadBB(); err != nil {
		return head, err
	}
	pos0 = r.TellBits()
	if _, err := r.ReadRC(); err != nil { // shadow flags
		return head, err
	}
	cadTraceField(trOn, pos0, r.TellBits(), "shadow", "")
	for i := 0; i < 3; i++ { // full/face/edge visual style 标志
		if _, err := r.ReadB(); err != nil {
			return head, err
		}
	}
	pos0 = r.TellBits()
	if iv, err := r.ReadBS(); err != nil { // invisibility
		return head, err
	} else {
		head.invisible = int16(iv)
	}
	cadTraceFieldInt(trOn, pos0, r.TellBits(), "invisibility", int64(head.invisible))
	pos0 = r.TellBits()
	if lw, err := r.ReadRC(); err != nil { // line weight
		return head, err
	} else {
		head.linewt = uint16(lw)
	}
	cadTraceFieldInt(trOn, pos0, r.TellBits(), "linewt", int64(head.linewt))
	return head, nil
}

// parseCommonEntityHandles 解析公共 handle 流（owner/reactors/xdic/layer 等），
// 返回 owner 句柄与 layer 句柄（渲染所需的全部信息）。
// 顺序对齐 LibreDWG common_entity_handle_data.spec：
//   - R13/R14：owner(mode=0) → reactors → xdic → layer → ltype → prev/next
//   - R2000：owner(mode=0) → reactors → xdic → prev/next → layer → ltype → plotstyle
//   - R2004+：owner(mode=0) → reactors → xdic → layer → ltype →
//     material/shadow(R2007+) → plotstyle → visual styles(R2010+)
func parseCommonEntityHandles(r *bitstream.BitStream, head *commonEntityHead) (owner, layer uint64, err error) {
	if head.entityMode == 0 {
		if owner, err = objrec.ReadHandleReference(r, head.handle); err != nil {
			return
		}
	}
	for i := uint32(0); i < head.numReactors; i++ {
		if _, err = objrec.ReadHandleReference(r, head.handle); err != nil {
			return
		}
	}
	if !head.xdicMissing {
		if head.xdicObjHandle, err = objrec.ReadHandleReference(r, head.handle); err != nil {
			return
		}
	}
	if head.r13r14 {
		// R13/R14：layer 在前，ltype 由 isbylayerlt 推导（1→0 不占流，0→3 占流），
		// prev/next 在尾部
		if layer, err = objrec.ReadHandleReference(r, head.handle); err != nil {
			return
		}
		if head.ltypeFlags == 3 {
			if _, err = objrec.ReadHandleReference(r, head.handle); err != nil {
				return
			}
		}
		if !head.noLinks {
			if head.prevEntity, err = objrec.ReadHandleReference(r, head.handle); err != nil { // prev_entity
				return
			}
			if head.nextEntity, err = objrec.ReadHandleReference(r, head.handle); err != nil { // next_entity
				return
			}
		}
		return
	}
	if head.prevNextLinks && !head.noLinks {
		// R2000：prev/next 在 layer 之前（spec VERSIONS(R_13b1, R_2000) 块
		// 先于 SINCE(R_2000b) 的 layer）
		if head.prevEntity, err = objrec.ReadHandleReference(r, head.handle); err != nil { // prev_entity
			return
		}
		if head.nextEntity, err = objrec.ReadHandleReference(r, head.handle); err != nil { // next_entity
			return
		}
	}
	if layer, err = objrec.ReadHandleReference(r, head.handle); err != nil {
		return
	}
	if head.ltypeFlags == 3 {
		if _, err = objrec.ReadHandleReference(r, head.handle); err != nil {
			return
		}
	}
	if head.materialFlags == 3 {
		if _, err = objrec.ReadHandleReference(r, head.handle); err != nil {
			return
		}
	}
	if head.shadowFlags == 3 {
		if _, err = objrec.ReadHandleReference(r, head.handle); err != nil {
			return
		}
	}
	if head.plotstyleFlgs == 3 {
		if head.plotstyleHandle, err = objrec.ReadHandleReference(r, head.handle); err != nil {
			return
		}
	}
	for i := 0; i < 3; i++ { // full/face/edge visual style 句柄
		if head.visualStyle[i] {
			if _, err = objrec.ReadHandleReference(r, head.handle); err != nil {
				return
			}
		}
	}
	return
}

// baseEntity 全部图元的公共字段（内嵌于各图元）。
type baseEntity struct {
	handle        uint64
	color         entColor
	layer         uint64
	owner         uint64
	mode          uint8             // 原始 entmode：0=块内（有 owner）1=图纸空间 2=模型空间 3=其他
	objSizeBit    uint64            // handle 流起点位（公共头扫描结果），审计对照 gold bitsize 用
	recSize       uint32            // 记录 MS size（字节），审计对照 gold size 用
	head          *commonEntityHead // 解码出的公共头（扫描产物），供审计字段导出
	extra         map[string]any    // per-type 扩展字段（thickness/extrusion 等），审计导出用
	typeName      string            // 实体类型名（如 LINE/CIRCLE），审计对照 gold entity 键
	typeCode      uint16            // 类型码
	nolinks       bool              // 无链接标志（公共头）
	isbylayerlt   bool              // 按图层线型标志（公共头）
	previewExists bool              // 预览存在标志
	eed           []entEED          // EED 应用数据链（公共头解析产物）

	// ---- round-trip 位串收集（生产解码路径默认填充，供写出回放与测试共用，字段名对齐 objGeneric）----
	// 回放方案保持 body 坐标系不变：preBits 从 body 局部 0 起（含类型码
	// 前导），headRawBits 至 handle 流起点（含流内 RL objSize 原值），
	// RawHandleBits 至记录尾（含 R2007+ 字符串区与 R2010+ 尾部 UMC）
	preBits       string // body 局部 0 至命中候选起点的原始位串
	headRawBits   string // 命中候选起点至 handle 流起点的原始位串（公共头+专有字段）
	RawHandleBits string // handle 流起点至记录尾的原始位串
	r2010Plus     bool   // 源记录为 R2010+ 布局（重解码重建 rec 元数据用）
	sizeBytes     uint32 // 源记录 MS size
	hSizeField    uint32 // R2010+ handle-stream-size 字段位宽
	hssBits       uint32 // R2010+ handle-stream-size 值（位）
	bodyBitOff    uint64 // 源记录 bodyBitOffset（MS 结束位偏移）
}

// mode2 是否模型空间直属实体。
func (b *baseEntity) mode2() bool { return b.mode == 2 }

// entityCommon 图元公共字段访问接口。
type entityCommon interface{ common() *baseEntity }

func (b *baseEntity) common() *baseEntity { return b }

// decodeOwnerLayer 定位 handle 流并解析 owner/layer；失败不阻断（返回 0）。
func decodeOwnerLayer(r *bitstream.BitStream, head *commonEntityHead) (owner, layer uint64) {
	savedByte, savedBit := r.Cursor()
	r.SetBitPos(head.objSizeBit)
	owner, layer, err := parseCommonEntityHandles(r, head)
	if err != nil {
		r.Restore(savedByte, savedBit)
		return 0, 0
	}
	r.Restore(savedByte, savedBit)
	return owner, layer
}

// decodeTextStyleHandle 扫描实体 handle 流的文本样式句柄（TEXT/ATTRIB/MTEXT
// 专有段：公共句柄序之后的首个句柄引用，各版本同位）。失败返回 0（不阻断
// 主解码，与 decodeOwnerLayer 同宽容口径）。
func decodeTextStyleHandle(r *bitstream.BitStream, head *commonEntityHead) uint64 {
	savedByte, savedBit := r.Cursor()
	r.SetBitPos(head.objSizeBit)
	var style uint64
	if _, _, err := parseCommonEntityHandles(r, head); err == nil {
		if h, e := objrec.ReadHandleReference(r, head.handle); e == nil {
			style = h
		}
	}
	r.Restore(savedByte, savedBit)
	return style
}

// ---- 各图元 ----

// entLine 线段图元。
type entLine struct {
	baseEntity
	start, end point3
}

// entCircle 圆图元。
type entCircle struct {
	baseEntity
	center point3
	radius float64
}

// entArc 圆弧图元（角度为弧度，AutoCAD 内部以弧度存储）。
type entArc struct {
	baseEntity
	center               point3
	radius               float64
	angleStart, angleEnd float64
}

// entPoint 点图元。
type entPoint struct {
	baseEntity
	location point3
	rotation float64 // x 轴角度（BD，度）
}

// entEllipse 椭圆图元。
type entEllipse struct {
	baseEntity
	center    point3
	majorAxis point3  // 主轴向量（长度即主轴半径）
	extrusion point3  // 挤出向量（主体 3BD，ratio 之前；gold extrusion 键导出）
	ratio     float64 // 短轴/主轴比
	startAng  float64
	endAng    float64
}

// entLwPolyline 优化多段线：顶点 + 凸度（bulge）+ 段宽。
// 位流 flags 的 bit0 是「extrusion 3BD 存在」；闭合与否
// 由几何判定（首尾顶点重合），DXF 70 组码不在位流中。
type entLwPolyline struct {
	baseEntity
	flags      uint16
	vertices   []point2
	bulges     []float64     // 与 vertices 对齐（缺失补 0）
	widths     []lwPolyWidth // flag&32（R2000 HAS_NUM_WIDTHS）时的逐段起端/末端宽
	elevation  float64       // flag&8 时的标高 BD（gold elevation 键）
	constWidth float64       // flag&4 时的常量宽度 BD（gold const_width 键）
	thickness  float64       // flag&2 时的厚度 BD（gold thickness 键）
}

// lwPolyWidth LWPOLYLINE 单段宽度（gold widths[i].start/end 键）。
type lwPolyWidth struct {
	start float64
	end   float64
}

// isClosedByGeometry 首尾顶点重合即闭合。
func (e *entLwPolyline) isClosedByGeometry() bool {
	if len(e.vertices) < 2 {
		return false
	}
	f, l := e.vertices[0], e.vertices[len(e.vertices)-1]
	d := math.Abs(f.x-l.x) + math.Abs(f.y-l.y)
	return d < 1e-9
}

// entText 单行文本。
type entText struct {
	baseEntity
	text        string
	insertion   point3
	height      float64
	rotation    float64
	hAlign      uint16
	vAlign      uint16
	alignPt     *point2
	gen         uint16
	widthFactor float64 // 宽度因子（DXF 41；dataflags 位可省略，0=未读按 1 用）
	extrusion   point3  // 挤出向量（主体 BE，读入保存；gold extrusion 键导出）
	styleHandle uint64  // 文本样式句柄（handle 流专有段首项，gold style 键导出）
}

// entMText 多行文本。
type entMText struct {
	baseEntity
	text        string
	insertion   point3
	xAxisDir    point3
	rectWidth   float64
	textHeight  float64
	attachment  uint16
	lineFactor  float64 // 行距系数（DXF 44，R2000+ 存储；渲染 MTEXT 行距 = 系数×字高）
	extrusion   point3  // 挤出向量（主体 3BD，读入保存；gold extrusion 键导出）
	styleHandle uint64  // 文本样式句柄（handle 流专有段，gold style 键导出）
}

// entInsert 块参照。
type entInsert struct {
	baseEntity
	position    point3
	scale       point3
	rotation    float64
	extrusion   point3 // 挤出向量（主体 3BD，读入保存；gold extrusion 键导出）
	blockHeader uint64
	attribs     []uint64 // 关联 ATTRIB 的句柄
	seqend      uint64   // SEQEND 结束句柄（handle 流 attribs 后，gold seqend 键导出）
}

// entAttrib 块属性文本（ATTDEF 复用，prompt/default_value 仅 ATTDEF 有值）。
type entAttrib struct {
	baseEntity
	text        string
	tag         string
	insertion   point3
	height      float64
	rotation    float64
	prompt      string  // ATTDEF 提示串
	hAlign      uint16  // 水平对齐（gold horiz_alignment）
	vAlign      uint16  // 垂直对齐（gold vert_alignment）
	gen         uint16  // 文字生成标志（gold generation）
	alignPt     *point2 // 对齐点（gold alignment_pt，与 TEXT 同构；缺省 nil）
	widthFactor float64 // 宽度因子（与 TEXT 同构；0=未读按 1 用）
	extrusion   point3  // 挤出向量（与 TEXT 同构继承；gold extrusion 键导出）
	styleHandle uint64  // 文本样式句柄（handle 流专有段首项，gold style 键导出）
}

// entBlockLike 块结构标记实体（BLOCK 块开始 / ENDBLK 块结束 / SEQEND
// 序列结束）：无几何字段，BLOCK 仅多块名 name（与配对 BLOCK_HEADER
// 对象同名；gold JSON 中三者均以 entity 键输出为普通实体）。
type entBlockLike struct {
	baseEntity
	name string // 块名（仅 BLOCK 有；ENDBLK/SEQEND 恒空）
}

// point2/point3 平面与空间坐标点。
type point2 struct{ x, y float64 }

type point3 struct{ x, y, z float64 }

// hdlSizeFieldBits R2010+ 记录头的 handle-stream-size (UMC) 字段位宽。
// 该字段位于对象数据区之前，gold bitsize 不含它，审计导出时需扣除。
func hdlSizeFieldBits(h objrec.ObjHeader) uint64 {
	if h.Rec == nil {
		return 0
	}
	return uint64(h.Rec.HandleSizeFieldBits)
}

// headParser 公共头候选解析器。
type headParser struct {
	name  string
	parse func(*bitstream.BitStream, uint64) (commonEntityHead, error)
	// externalSize 为 true 表示 bitsize 不在流内存储（R2010+），由记录头
	// 推导；审计导出 bitsize 时需从 dataEnd 扣除 hdl size 字段位宽。
	externalSize bool
}

// entityGeometryFinite 图元核心几何的有限性检查（错位候选常解出 Inf/NaN）。
func entityGeometryFinite(ent any) bool {
	fin := func(fs ...float64) bool {
		for _, f := range fs {
			if !isFinite(f) {
				return false
			}
		}
		return true
	}
	switch e := ent.(type) {
	case *entLine:
		return fin(e.start.x, e.start.y, e.start.z, e.end.x, e.end.y, e.end.z)
	case *entCircle:
		return fin(e.center.x, e.center.y, e.center.z, e.radius)
	case *entArc:
		// 角度另限量级（弧度值 |a|<1e6）：错位读出的天文角度有限但必假
		return fin(e.center.x, e.center.y, e.center.z, e.radius, e.angleStart, e.angleEnd) &&
			math.Abs(e.angleStart) < 1e6 && math.Abs(e.angleEnd) < 1e6
	case *entPoint:
		return fin(e.location.x, e.location.y, e.location.z)
	case *entEllipse:
		return fin(e.center.x, e.center.y, e.center.z, e.majorAxis.x, e.majorAxis.y, e.majorAxis.z, e.ratio, e.startAng, e.endAng)
	case *entLwPolyline:
		if len(e.vertices) == 0 {
			return false
		}
		for _, v := range e.vertices {
			if !fin(v.x, v.y) {
				return false
			}
		}
		return true
	case *entText:
		return fin(e.height, e.insertion.x, e.insertion.y, e.insertion.z)
	case *entMText:
		return fin(e.textHeight, e.rectWidth, e.insertion.x, e.insertion.y, e.insertion.z)
	case *entInsert:
		return fin(e.position.x, e.position.y, e.position.z, e.scale.x, e.scale.y, e.scale.z, e.rotation)
	case *entSpline:
		for _, p := range e.controlPoints {
			if !fin(p.x, p.y, p.z) {
				return false
			}
		}
		for _, p := range e.fitPoints {
			if !fin(p.x, p.y, p.z) {
				return false
			}
		}
		return len(e.controlPoints) > 0 || len(e.fitPoints) > 0
	case *entDimension:
		return fin(e.point10.x, e.point10.y, e.point10.z,
			e.point13.x, e.point13.y, e.point13.z,
			e.point14.x, e.point14.y, e.point14.z)
	case *entHatch:
		for _, p := range e.paths {
			for _, v := range p.points {
				if !fin(v.x, v.y) {
					return false
				}
			}
		}
		return len(e.paths) > 0
	case *entImage:
		return fin(e.pt0.x, e.pt0.y, e.pt0.z,
			e.uvec.x, e.uvec.y, e.uvec.z,
			e.vvec.x, e.vvec.y, e.vvec.z,
			e.imageSize.x, e.imageSize.y)
	case *entOle2Frame, *entOleFrame:
		// OLE 框架无浮点几何，data_size/data 由解码器上限防护
		return true
	case *entProxyEntity:
		// 代理实体几何由宿主应用解释，元数据字段均为整型/字节
		return true
	case *entMpolygon:
		for _, p := range e.hatch.paths {
			for _, v := range p.points {
				if !fin(v.x, v.y) {
					return false
				}
			}
		}
		return len(e.hatch.paths) > 0
	case *entRay:
		return fin(e.start.x, e.start.y, e.start.z, e.unitVector.x, e.unitVector.y, e.unitVector.z)
	case *entSolid:
		return fin(e.p1.x, e.p1.y, e.p2.x, e.p2.y, e.p3.x, e.p3.y, e.p4.x, e.p4.y)
	case *entFace3d:
		return fin(e.p1.x, e.p1.y, e.p1.z, e.p2.x, e.p2.y, e.p2.z, e.p3.x, e.p3.y, e.p3.z, e.p4.x, e.p4.y, e.p4.z)
	case *entLeader:
		for _, p := range e.points {
			if !fin(p.x, p.y, p.z) {
				return false
			}
		}
		return len(e.points) > 0
	case *entMLine:
		for _, v := range e.vertices {
			if !fin(v.position.x, v.position.y, v.position.z) {
				return false
			}
		}
		return len(e.vertices) > 0
	case *entPolyline3d:
		return true
	case *entPolyline2d:
		return true
	}
	return true
}

// scanEntityBest 两阶段候选扫描：先 0..64 位逐位，全部无高分候选时
// 改按字节对齐（步长 8）扫到 1024 位宽容兜底。评分下限 20 过滤错位产物。
// 快路径：布局已与参考实现逐位对齐，delta=0 且解析出的句柄与记录句柄一致、
// 几何有限时直接采信，避免评分误选错位候选。
// hdlFieldBits 为 R2010+ 记录头 handle-stream-size 字段位宽（bitsize 导出扣除用）。
func scanEntityBest(
	r *bitstream.BitStream,
	base uint64,
	dataEnd uint64,
	hdlFieldBits uint64,
	parsers []headParser,
	objHandle uint64,
	recSize uint32,
	typeName string,
	typeCode uint16,
	decode func(r *bitstream.BitStream, head *commonEntityHead) (any, error),
) (any, int, error) {
	// fillMeta 回填公共头扫描结果与记录元数据（审计对照 gold 键用）；
	// startBit 为最终命中候选的起始位，用于 round-trip 位串收集
	fillMeta := func(ent any, head *commonEntityHead, externalSize bool, startBit uint64) {
		if ec, ok := ent.(entityCommon); ok {
			b := ec.common()
			b.objSizeBit = head.auditBitsize
			if externalSize {
				// R2010+：数据区结束位含头部 UMC 字段，gold bitsize 不含
				b.objSizeBit = dataEnd - hdlFieldBits
			}
			b.recSize = recSize
			b.head = head
			b.typeName = typeName
			b.typeCode = typeCode
			b.nolinks = head.noLinks
			b.isbylayerlt = head.isByLayerLtype
			b.previewExists = head.previewExists
			b.eed = head.eed
			collectEntityRawBits(r, b, head, startBit, dataEnd)
		}
	}
	for _, variant := range parsers {
		r.SetBitPos(base)
		head, err := variant.parse(r, dataEnd)
		if err != nil {
			if cadTraceHandle == objHandle {
				fmt.Fprintf(os.Stderr, "[fast] handle=%d layout=%s head err: %v\n", objHandle, variant.name, err)
			}
			continue
		}
		if head.handle == 0 {
			head.handle = objHandle
		}
		if head.handle != objHandle {
			if cadTraceHandle == objHandle {
				fmt.Fprintf(os.Stderr, "[fast] handle=%d layout=%s handle mismatch got %d\n", objHandle, variant.name, head.handle)
			}
			continue
		}
		// 流内 RL objSize（单位=位，即数据区起点到 handle 流）不得超过数据区
		// 总位长；错位候选会读出垃圾值（ACIS 系 R2007 实证 21200>13808 等），
		// 布局必然错位，不可走快路径直接采信
		if !variant.externalSize && head.auditBitsize > dataEnd-base {
			if cadTraceHandle == objHandle {
				fmt.Fprintf(os.Stderr, "[fast] handle=%d layout=%s objSize %d 超数据区位长 %d\n",
					objHandle, variant.name, head.auditBitsize, dataEnd-base)
			}
			continue
		}
		ent, derr := decode(r, &head)
		if derr != nil || !entityGeometryFinite(ent) {
			if cadTraceHandle == objHandle {
				fmt.Fprintf(os.Stderr, "[fast] handle=%d layout=%s decode err: %v finite=%v\n", objHandle, variant.name, derr, entityGeometryFinite(ent))
			}
			continue
		}
		fillMeta(ent, &head, variant.externalSize, base)
		score := commonHeadScore(&head, objHandle) + entityGeometryScore(ent)
		return ent, score, nil
	}
	var best any
	bestScore := -1 << 30
	lastErr := error(nil)
	var bestHead *commonEntityHead
	var bestExternal bool
	var bestStartBit uint64
	// 阶段 0：含小幅负偏移（R13 等版本头部长度存在 ±1~8 位抖动）
	for _, phase := range [3]struct{ minDelta, maxDelta, step int64 }{{0, 64, 1}, {-8, 0, 1}, {64, 1024, 8}} {
		for delta := phase.minDelta; delta <= phase.maxDelta; delta += phase.step {
			abs := int64(base) + delta
			if abs < 0 {
				continue
			}
			for _, variant := range parsers {
				r.SetBitPos(uint64(abs))
				head, err := variant.parse(r, dataEnd)
				if err != nil {
					lastErr = err
					continue
				}
				if head.handle == 0 {
					head.handle = objHandle
				}
				headScore := commonHeadScore(&head, objHandle)
				// 流内 RL objSize 合理性校验（单位=位，不得超过数据区总位长）：
				// 错位候选读出垃圾 objSize 时布局必然错位，直接跳过（省去
				// 全量主体解码，也避免垃圾候选凭几何偶然高分胜出）
				if !variant.externalSize && head.auditBitsize > dataEnd-base {
					if cadTraceHandle == objHandle {
						fmt.Fprintf(os.Stderr, "[scan] handle=%d delta=%d layout=%s objSize %d 超数据区位长 %d\n",
							objHandle, delta, variant.name, head.auditBitsize, dataEnd-base)
					}
					continue
				}
				// 头评分前置过滤：头本身不合理（非本记录句柄且字段异常）时
				// 跳过图元全解码（解码成本占扫描耗时大头）
				if headScore < 15 {
					continue
				}
				ent, derr := decode(r, &head)
				if derr != nil {
					lastErr = derr
					continue
				}
				// delta 惩罚：正确候选通常在较小 delta 处，
				// 远离 dataStart 的候选更可能是错位产物
				score := headScore + entityGeometryScore(ent) - int(delta/8)
				if cadTraceHandle == objHandle {
					fmt.Fprintf(os.Stderr, "[scan] handle=%d delta=%d layout=%s headScore=%d total=%d ent=%+v\n",
						objHandle, delta, variant.name, headScore, score, ent)
				}
				if score > bestScore {
					bestScore = score
					best = ent
					hd := head
					bestHead = &hd
					bestExternal = variant.externalSize
					bestStartBit = uint64(abs)
				}
				if bestScore >= 90 {
					fillMeta(best, bestHead, bestExternal, bestStartBit)
					return best, bestScore, nil
				}
			}
		}
		if best != nil {
			fillMeta(best, bestHead, bestExternal, bestStartBit)
			return best, bestScore, nil
		}
	}
	if best == nil || bestScore < 20 {
		return nil, bestScore, fmt.Errorf("cad: 实体候选扫描失败（handle %d）: %w", objHandle, lastErr)
	}
	fillMeta(best, bestHead, bestExternal, bestStartBit)
	return best, bestScore, nil
}

// commonHeadScore 公共头字段合理性评分。
func commonHeadScore(head *commonEntityHead, objHandle uint64) int {
	score := 0
	if head.handle == objHandle {
		score += 30
	} else if head.handle != 0 {
		score += 6
	}
	if head.ltypeScale > 0 && head.ltypeScale < 1e6 {
		score += 10
		if head.ltypeScale == 1.0 {
			score += 5
		}
	} else if !isFinite(head.ltypeScale) {
		score -= 30
	}
	// 仅显式索引色（1..255）给高分；短格式 ByLayer(256)/ByBlock(0) 位型在
	// 错位候选中同样高频，给 0 分以保持与旧行为一致的区分度
	if head.color.hasIndex && head.color.index >= 1 && head.color.index <= 255 {
		score += 6
	}
	if head.entityMode <= 3 {
		score += 8
	} else {
		score -= 20
	}
	if head.numReactors < 16 {
		score += 6
	} else {
		score -= 24
	}
	return score
}

// entityGeometryScore 图元几何合理性评分（区分真实工程坐标与错位垃圾值）。
func entityGeometryScore(ent any) int {
	switch e := ent.(type) {
	case *entLine:
		return pointPairScore(e.start, e.end)
	case *entCircle:
		return radiusScore(e.radius) + pointScore(e.center)
	case *entArc:
		// 角度量级参与评分：错位候选的天文角度应显著劣于合理候选
		if math.Abs(e.angleStart) > 1e6 || math.Abs(e.angleEnd) > 1e6 {
			return radiusScore(e.radius) + pointScore(e.center) - 40
		}
		return radiusScore(e.radius) + pointScore(e.center)
	case *entPoint:
		return pointScore(e.location)
	case *entEllipse:
		return radiusScore(math.Hypot(e.majorAxis.x, e.majorAxis.y)) + pointScore(e.center)
	case *entLwPolyline:
		if len(e.vertices) == 0 {
			return -50
		}
		s := 0
		for _, v := range e.vertices {
			s += pointScore(point3{v.x, v.y, 0})
			if s < -100 {
				return s
			}
		}
		return s
	case *entText:
		if e.height > 0 && e.height < 1e6 {
			return pointPairScore(e.insertion, point3{}) + 5
		}
		return -50
	case *entMText:
		if e.textHeight > 0 && e.textHeight < 1e6 && e.rectWidth >= 0 {
			// 文字质量参与评分：真实富文本远优于错位读出的 1~2 字符乱串
			return pointPairScore(e.insertion, point3{}) + 5 + decodedTextScore(e.text)/4
		}
		return -50
	case *entInsert:
		return pointPairScore(e.position, point3{})
	case *entAttrib:
		if e.height > 0 && e.height < 1e6 {
			return pointPairScore(e.insertion, point3{}) + 5 + decodedTextScore(e.text)/4
		}
		return -50
	}
	return 0
}

// pointPairScore 两点几何评分。
func pointPairScore(a, b point3) int { return pointScore(a) + pointScore(b) }

// pointScore 单点评分：坐标有限且量级合理（工程图纸 <1e9）得正分。
func pointScore(p point3) int {
	if !isFinite(p.x) || !isFinite(p.y) || !isFinite(p.z) {
		return -60
	}
	m := math.Abs(p.x)
	if math.Abs(p.y) > m {
		m = math.Abs(p.y)
	}
	if m < 1e9 {
		return 10
	}
	return -60
}

// radiusScore 半径评分：正且有限。
func radiusScore(r float64) int {
	if r > 0 && r < 1e9 {
		return 8
	}
	return -60
}

// decodeLine LINE：z 零标志 + 差分双精度端点。
func decodeLine(r *bitstream.BitStream, head *commonEntityHead) (any, error) {
	trOn := cadTraceHandle != 0 && cadTraceHandle == head.handle
	pos := r.TellBits()
	zIsZero, err := r.ReadB()
	if err != nil {
		return nil, err
	}
	cadTraceFieldInt(trOn, pos, r.TellBits(), "z_is_zero", int64(zIsZero))
	pos = r.TellBits()
	xs, err := r.ReadRD()
	if err != nil {
		return nil, err
	}
	cadTraceFieldF64(trOn, pos, r.TellBits(), "x_start", xs)
	pos = r.TellBits()
	xe, err := r.ReadDD(xs)
	if err != nil {
		return nil, err
	}
	cadTraceFieldF64(trOn, pos, r.TellBits(), "x_end", xe)
	pos = r.TellBits()
	ys, err := r.ReadRD()
	if err != nil {
		return nil, err
	}
	cadTraceFieldF64(trOn, pos, r.TellBits(), "y_start", ys)
	pos = r.TellBits()
	ye, err := r.ReadDD(ys)
	if err != nil {
		return nil, err
	}
	cadTraceFieldF64(trOn, pos, r.TellBits(), "y_end", ye)
	var zs, ze float64
	if zIsZero == 0 {
		pos = r.TellBits()
		if zs, err = r.ReadRD(); err != nil {
			return nil, err
		}
		pos = r.TellBits()
		if ze, err = r.ReadDD(zs); err != nil {
			return nil, err
		}
		if trOn {
			cadTraceField(trOn, pos, r.TellBits(), "z_pair", fmt.Sprintf("%g..%g", zs, ze))
		}
	}
	pos = r.TellBits()
	thickness, err := r.ReadBT()
	if err != nil {
		return nil, err
	}
	cadTraceFieldF64(trOn, pos, r.TellBits(), "thickness", thickness)
	pos = r.TellBits()
	exx, exy, exz, exerr := r.ReadBE()
	cadTraceField(trOn, pos, r.TellBits(), "extrusion", "-")
	_ = exerr
	owner, layer := decodeOwnerLayer(r, head)
	return &entLine{
		baseEntity: baseEntity{
			handle: head.handle, color: head.color, layer: layer, owner: owner, mode: head.entityMode,
			extra: map[string]any{"thickness": thickness, "extrusion": []float64{exx, exy, exz}, "z_is_zero": b2int(zIsZero == 1)},
		},
		start: point3{xs, ys, zs},
		end:   point3{xe, ye, ze},
	}, nil
}

// decodeLineR14 R13/R14 LINE：显式 3BD 起点 + 3BD 终点（非差分编码）。
func decodeLineR14(r *bitstream.BitStream, head *commonEntityHead) (any, error) {
	sx, sy, sz, err := r.Read3BD()
	if err != nil {
		return nil, err
	}
	ex, ey, ez, err := r.Read3BD()
	if err != nil {
		return nil, err
	}
	thickness, err := r.ReadBT()
	if err != nil {
		return nil, err
	}
	exx, exy, exz, err := r.ReadBE()
	if err != nil {
		return nil, err
	}
	owner, layer := decodeOwnerLayer(r, head)
	return &entLine{
		baseEntity: baseEntity{
			handle: head.handle, color: head.color, layer: layer, owner: owner, mode: head.entityMode,
			extra: map[string]any{"thickness": thickness, "extrusion": []float64{exx, exy, exz}},
		},
		start: point3{sx, sy, sz},
		end:   point3{ex, ey, ez},
	}, nil
}

// entSpline 样条曲线图元。
type entSpline struct {
	baseEntity
	scenario      uint32 // 1=控制点模式 2=拟合点模式
	splineFlags1  uint32 // R2013+ 标志
	knotParameter uint32 // R2013+ 节点参数化
	degree        uint32
	rational      bool
	closed        bool
	periodic      bool
	weighted      bool // 控制点模式尾部 weighted 回显位（=1 时控制点带 w）
	r2013Plus     bool // 是否来自 R2013+ 头（决定 flags1/节点参数是否有效）
	fitTolerance  float64
	knotTolerance float64
	ctrlTolerance float64
	knots         []float64
	controlPoints []point3
	weights       []float64
	fitPoints     []point3
	begTanVec     point3 // 起点切线（拟合点模式 3BD，gold beg_tan_vec 键导出）
	endTanVec     point3 // 终点切线（拟合点模式 3BD，gold end_tan_vec 键导出）
	styleHandle   uint64 // 样式句柄（handle 流 H 340，gold style 键导出）
}

// decodeSpline SPLINE：scenario BL + [R2013+ flags BL + 节点参数 BL] + degree BL，
// 之后按控制点/拟合点模式解析（scenario==2 为拟合点）。
func decodeSpline(r *bitstream.BitStream, head *commonEntityHead, r2013Plus bool) (any, error) {
	scenario, err := r.ReadBL()
	if err != nil {
		return nil, err
	}
	var splineFlags1, knotParameter uint32
	if r2013Plus {
		if splineFlags1, err = r.ReadBL(); err != nil {
			return nil, err
		}
		if knotParameter, err = r.ReadBL(); err != nil {
			return nil, err
		}
		// R2013+ 的 scenario 由标志重算（spec：flags&1 → 拟合点 2，
		// knotparam==15 → 控制点 1），流内 BL 原值不直接作判定
		if splineFlags1&1 == 1 {
			scenario = 2
		}
		if knotParameter == 15 {
			scenario = 1
		}
	}
	degree, err := r.ReadBL()
	if err != nil {
		return nil, err
	}
	sp := &entSpline{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode},
		scenario: scenario, splineFlags1: splineFlags1, knotParameter: knotParameter, degree: degree,
		r2013Plus: r2013Plus}
	// 双模式重试：scenario==2 优先拟合点，否则优先控制点；
	// 首选模式失败时回退另一模式（真实文件存在两模式混写的产物）。
	// 解析上限定为数据区终点：越入 handle 流即视为无效（缓冲区语义）。
	parsePos := r.TellBits()
	preferFit := scenario == 2
	err = parseSplineModeLimit(r, sp, preferFit, head.objSizeBit)
	if err != nil {
		// 首选模式失败：清空半途写入的字段（每次尝试以全新字段重解），
		// 回退另一模式重试。
		r.SetBitPos(parsePos)
		sp.knots = nil
		sp.controlPoints = nil
		sp.weights = nil
		sp.fitPoints = nil
		sp.begTanVec, sp.endTanVec = point3{}, point3{}
		sp.knotTolerance, sp.ctrlTolerance, sp.fitTolerance = 0, 0, 0
		sp.rational, sp.closed, sp.periodic = false, false, false
		if err2 := parseSplineModeLimit(r, sp, !preferFit, head.objSizeBit); err2 != nil {
			return nil, err
		}
	}
	// 审计导出键（R2013+ 的 splineflags/knotparam 非 R2013 恒 0；
	// beg/end_tan_vec 拟合点模式 3BD，控制点模式 gold 无键不导出）
	sp.extra = map[string]any{
		"scenario": int64(scenario), "degree": int64(degree),
		"splineflags": int64(splineFlags1), "knotparam": int64(knotParameter),
		"fit_tol":  sp.fitTolerance,
		"rational": b2int(sp.rational), "closed_b": b2int(sp.closed),
		"periodic": b2int(sp.periodic),
		"knot_tol": sp.knotTolerance, "ctrl_tol": sp.ctrlTolerance,
		"weighted": b2int(sp.weighted),
	}
	if scenario == 2 {
		sp.extra["beg_tan_vec"] = vec3Arr(sp.begTanVec)
		sp.extra["end_tan_vec"] = vec3Arr(sp.endTanVec)
	}
	for i, cp := range sp.controlPoints {
		sp.extra[fmt.Sprintf("ctrl_pts[%d].x", i)] = cp.x
		sp.extra[fmt.Sprintf("ctrl_pts[%d].y", i)] = cp.y
		sp.extra[fmt.Sprintf("ctrl_pts[%d].z", i)] = cp.z
	}
	for i, w := range sp.weights {
		sp.extra[fmt.Sprintf("ctrl_pts[%d].w", i)] = w
	}
	// handle 流（批次 B 建模：owner/layer 公共序 + style 句柄，gold
	// layer/next_entity/prev_entity/style 键导出）
	r.SetBitPos(head.objSizeBit)
	if owner, layer, e := parseCommonEntityHandles(r, head); e == nil {
		sp.owner, sp.layer = owner, layer
		if st, e2 := objrec.ReadHandleReference(r, head.handle); e2 == nil {
			sp.styleHandle = st
		}
	}
	return sp, nil
}

// parseSplineModeLimit 带数据区上限的模式解析：超出即报错（触发模式回退）。
func parseSplineModeLimit(r *bitstream.BitStream, sp *entSpline, fit bool, limitBit uint64) error {
	var err error
	if fit {
		err = parseSplineFitData(r, sp)
	} else {
		err = parseSplineControlData(r, sp)
	}
	if err != nil {
		return err
	}
	if r.TellBits() > limitBit {
		return fmt.Errorf("cad: SPLINE 解析越过数据区终点（%d > %d）", r.TellBits(), limitBit)
	}
	return nil
}

// parseSplineMode 按指定模式解析样条数据。
func parseSplineMode(r *bitstream.BitStream, sp *entSpline, fit bool) error {
	if fit {
		return parseSplineFitData(r, sp)
	}
	return parseSplineControlData(r, sp)
}

// parseSplineControlData 控制点模式：3 个标志位 + 两个容差 + 节点/控制点数组。
func parseSplineControlData(r *bitstream.BitStream, sp *entSpline) error {
	var err error
	var v uint8
	if v, err = r.ReadB(); err != nil {
		return err
	}
	sp.rational = v != 0
	if v, err = r.ReadB(); err != nil {
		return err
	}
	sp.closed = v != 0
	if v, err = r.ReadB(); err != nil {
		return err
	}
	sp.periodic = v != 0
	if sp.knotTolerance, err = r.ReadBD(); err != nil {
		return err
	}
	if sp.ctrlTolerance, err = r.ReadBD(); err != nil {
		return err
	}
	numKnots, err := r.ReadBL()
	if err != nil {
		return err
	}
	if numKnots > 1_000_000 {
		return fmt.Errorf("cad: SPLINE 节点数异常 %d", numKnots)
	}
	numCtrl, err := r.ReadBL()
	if err != nil {
		return err
	}
	if numCtrl > 1_000_000 {
		return fmt.Errorf("cad: SPLINE 控制点数异常 %d", numCtrl)
	}
	if w, err := r.ReadB(); err != nil { // weighted 回显位
		return err
	} else {
		sp.weighted = w != 0
	}
	for i := uint32(0); i < numKnots; i++ {
		k, err := r.ReadBD()
		if err != nil {
			return err
		}
		sp.knots = append(sp.knots, k)
	}
	for i := uint32(0); i < numCtrl; i++ {
		x, y, z, err := r.Read3BD()
		if err != nil {
			return err
		}
		sp.controlPoints = append(sp.controlPoints, point3{x, y, z})
		if sp.rational {
			w, err := r.ReadBD()
			if err != nil {
				return err
			}
			sp.weights = append(sp.weights, w)
		}
	}
	return nil
}

// parseSplineFitData 拟合点模式：容差 + 起末切线 + 拟合点数组。
func parseSplineFitData(r *bitstream.BitStream, sp *entSpline) error {
	var err error
	if sp.fitTolerance, err = r.ReadBD(); err != nil {
		return err
	}
	bx, by, bz, err := r.Read3BD() // 起点切线（读入保存，gold beg_tan_vec 键导出）
	if err != nil {
		return err
	}
	ex, ey, ez, err := r.Read3BD() // 终点切线（读入保存，gold end_tan_vec 键导出）
	if err != nil {
		return err
	}
	sp.begTanVec = point3{bx, by, bz}
	sp.endTanVec = point3{ex, ey, ez}
	numFit, err := r.ReadBL()
	if err != nil {
		return err
	}
	if numFit > 1_000_000 {
		return fmt.Errorf("cad: SPLINE 拟合点数异常 %d", numFit)
	}
	for i := uint32(0); i < numFit; i++ {
		x, y, z, err := r.Read3BD()
		if err != nil {
			return err
		}
		sp.fitPoints = append(sp.fitPoints, point3{x, y, z})
	}
	return nil
}

// decodeCircle CIRCLE：3BD 圆心 + BD 半径。
func decodeCircle(r *bitstream.BitStream, head *commonEntityHead) (any, error) {
	cx, cy, cz, err := r.Read3BD()
	if err != nil {
		return nil, err
	}
	radius, err := r.ReadBD()
	if err != nil {
		return nil, err
	}
	thickness, err := r.ReadBT()
	if err != nil {
		return nil, err
	}
	exx, exy, exz, err := r.ReadBE()
	if err != nil {
		return nil, err
	}
	owner, layer := decodeOwnerLayer(r, head)
	return &entCircle{
		baseEntity: baseEntity{
			handle: head.handle, color: head.color, layer: layer, owner: owner, mode: head.entityMode,
			extra: map[string]any{"thickness": thickness, "extrusion": []float64{exx, exy, exz}},
		},
		center: point3{cx, cy, cz},
		radius: radius,
	}, nil
}

// decodeArc ARC：同 CIRCLE，多出起始/终止角（弧度）。
func decodeArc(r *bitstream.BitStream, head *commonEntityHead) (any, error) {
	cx, cy, cz, err := r.Read3BD()
	if err != nil {
		return nil, err
	}
	radius, err := r.ReadBD()
	if err != nil {
		return nil, err
	}
	thickness, err := r.ReadBT()
	if err != nil {
		return nil, err
	}
	exx, exy, exz, err := r.ReadBE()
	if err != nil {
		return nil, err
	}
	a0, err := r.ReadBD()
	if err != nil {
		return nil, err
	}
	a1, err := r.ReadBD()
	if err != nil {
		return nil, err
	}
	owner, layer := decodeOwnerLayer(r, head)
	return &entArc{
		baseEntity: baseEntity{
			handle: head.handle, color: head.color, layer: layer, owner: owner, mode: head.entityMode,
			extra: map[string]any{"thickness": thickness, "extrusion": []float64{exx, exy, exz}},
		},
		center:     point3{cx, cy, cz},
		radius:     radius,
		angleStart: a0,
		angleEnd:   a1,
	}, nil
}

// decodePoint POINT：3BD 定位点。
func decodePoint(r *bitstream.BitStream, head *commonEntityHead) (any, error) {
	x, y, z, err := r.Read3BD()
	if err != nil {
		return nil, err
	}
	thickness, err := r.ReadBT() // thickness（BT：1 位标志 + 可选 BD）
	if err != nil {
		return nil, err
	}
	exx, exy, exz, err := r.ReadBE() // extrusion
	if err != nil {
		return nil, err
	}
	rotation, err := r.ReadBD() // x 轴角度
	if err != nil {
		return nil, err
	}
	owner, layer := decodeOwnerLayer(r, head)
	return &entPoint{
		baseEntity: baseEntity{
			handle: head.handle, color: head.color, layer: layer, owner: owner, mode: head.entityMode,
			extra: map[string]any{"thickness": thickness, "extrusion": []float64{exx, exy, exz}},
		},
		location: point3{x, y, z},
		rotation: rotation,
	}, nil
}

// decodeEllipse ELLIPSE：3BD 圆心 + 3BD 主轴向量 + BD 轴比 + 起止角。
func decodeEllipse(r *bitstream.BitStream, head *commonEntityHead) (any, error) {
	cx, cy, cz, err := r.Read3BD()
	if err != nil {
		return nil, err
	}
	mx, my, mz, err := r.Read3BD()
	if err != nil {
		return nil, err
	}
	ex, ey, ez, err := r.Read3BD() // extrusion（在 ratio 之前）
	if err != nil {
		return nil, err
	}
	ratio, err := r.ReadBD()
	if err != nil {
		return nil, err
	}
	a0, err := r.ReadBD()
	if err != nil {
		return nil, err
	}
	a1, err := r.ReadBD()
	if err != nil {
		return nil, err
	}
	// 注意：ELLIPSE 无尾部 thickness/extrusion 读取（decode_ellipse_with_header
	// 在 end_angle 后直接进 handle 流；extrusion 已在主体 3BD 中）。
	owner, layer := decodeOwnerLayer(r, head)
	return &entEllipse{
		baseEntity: baseEntity{handle: head.handle, color: head.color, layer: layer, owner: owner, mode: head.entityMode},
		center:     point3{cx, cy, cz},
		majorAxis:  point3{mx, my, mz},
		extrusion:  point3{ex, ey, ez},
		ratio:      ratio,
		startAng:   a0,
		endAng:     a1,
	}, nil
}

// LWPOLYLINE 位流 flags 位定义（ODA 规范）。
const (
	lwFlagHasNormal = 0x01 // 存在 extrusion（spec FIELD_3BD，无 BE 默认前缀位）
	lwFlagHasWidth  = 0x04 // 存在固定宽度
	lwFlagHasBulges = 0x10 // 存在凸度数组
)

// decodeLwPolyline LWPOLYLINE：标志驱动的可选段 + 顶点数组。
// r2000Plus 区分顶点编码：R2000+ 为 2DD 差分（首点绝对），R13/R14 为
// 2RD 逐点无条件双精度（对齐 dwg.spec LWPOLYLINE 的 VERSIONS 分支）。
func decodeLwPolyline(r *bitstream.BitStream, head *commonEntityHead, codepage uint16, r2000Plus bool) (any, error) {
	flags, err := r.ReadBS()
	if err != nil {
		return nil, err
	}
	var constWidth float64
	if flags&0x04 != 0 {
		var err error
		if constWidth, err = r.ReadBD(); err != nil {
			return nil, err
		}
	}
	var elevation float64
	if flags&0x08 != 0 {
		var err error
		if elevation, err = r.ReadBD(); err != nil {
			return nil, err
		}
	}
	var thickness float64
	if flags&0x02 != 0 {
		var err error
		if thickness, err = r.ReadBD(); err != nil {
			return nil, err
		}
	}
	if flags&0x01 != 0 {
		// extrusion 是 FIELD_3BD（3×BD 独立编码，无 BE 的默认前缀位）：
		// dwg.spec 注明该位与 DXF closed 512 冲突，fzw h=6610926 实证
		// (0,0,-1) 占 70bit（2+2+66），多读 1 位即令 num_points 错位
		if _, _, _, err := r.Read3BD(); err != nil { // extrusion
			return nil, err
		}
	}
	vertCount, err := r.ReadBL()
	if err != nil {
		return nil, err
	}
	if vertCount > 1_000_000 {
		return nil, fmt.Errorf("cad: LWPOLYLINE 顶点数异常 %d", vertCount)
	}
	numVerts := int(vertCount)
	numBulges := 0
	if flags&0x10 != 0 {
		if numBulges, err = readCount(r); err != nil {
			return nil, err
		}
	}
	numVertexIDs := 0
	if flags&0x400 != 0 {
		if numVertexIDs, err = readCount(r); err != nil {
			return nil, err
		}
	}
	numWidths := 0
	if flags&0x20 != 0 {
		if numWidths, err = readCount(r); err != nil {
			return nil, err
		}
	}
	var widths []lwPolyWidth
	if numWidths > 0 {
		widths = make([]lwPolyWidth, 0, numWidths)
	}

	vertices := make([]point2, 0, numVerts)
	if numVerts > 0 {
		if r2000Plus {
			x0, err := r.ReadRD()
			if err != nil {
				return nil, err
			}
			y0, err := r.ReadRD()
			if err != nil {
				return nil, err
			}
			vertices = append(vertices, point2{x0, y0})
			for i := 1; i < numVerts; i++ {
				last := vertices[len(vertices)-1]
				x, err := r.ReadDD(last.x)
				if err != nil {
					return nil, err
				}
				y, err := r.ReadDD(last.y)
				if err != nil {
					return nil, err
				}
				vertices = append(vertices, point2{x, y})
			}
		} else {
			// R13/R14：2RD_VECTOR，每点无条件双精度
			for i := 0; i < numVerts; i++ {
				x, err := r.ReadRD()
				if err != nil {
					return nil, err
				}
				y, err := r.ReadRD()
				if err != nil {
					return nil, err
				}
				vertices = append(vertices, point2{x, y})
			}
		}
	}
	bulges := make([]float64, numVerts)
	for i := 0; i < numBulges && i < numVerts; i++ {
		b, err := r.ReadBD()
		if err != nil {
			return nil, err
		}
		bulges[i] = b
	}
	for i := 0; i < numVertexIDs; i++ {
		if _, err := r.ReadBL(); err != nil {
			return nil, err
		}
	}
	for i := 0; i < numWidths; i++ {
		sw, err := r.ReadBD() // start width
		if err != nil {
			return nil, err
		}
		ew, err := r.ReadBD() // end width
		if err != nil {
			return nil, err
		}
		widths = append(widths, lwPolyWidth{start: sw, end: ew})
	}
	owner, layer := decodeOwnerLayer(r, head)
	return &entLwPolyline{
		baseEntity: baseEntity{handle: head.handle, color: head.color, layer: layer, owner: owner, mode: head.entityMode},
		flags:      flags,
		vertices:   vertices,
		bulges:     bulges,
		widths:     widths,
		elevation:  elevation,
		constWidth: constWidth,
		thickness:  thickness,
	}, nil
}

// readCount 读取数量字段并做安全上限校验。
func readCount(r *bitstream.BitStream) (int, error) {
	v, err := r.ReadBL()
	if err != nil {
		return 0, err
	}
	if v > 1_000_000 {
		return 0, fmt.Errorf("cad: 数量字段异常 %d", v)
	}
	return int(v), nil
}

// TEXT data_flags 位定义。
const (
	textFlagNoElevation = 0x01
	textFlagNoAlign     = 0x02
	textFlagNoOblique   = 0x04
	textFlagNoRotation  = 0x08
	textFlagNoWidth     = 0x10
	textFlagNoGen       = 0x20
	textFlagNoHAlign    = 0x40
	textFlagNoVAlign    = 0x80
)

// decodeText TEXT：R2013+ 文本尾为候选评分的 UTF-16/编码串。
func decodeText(r *bitstream.BitStream, head *commonEntityHead, codepage uint16) (any, error) {
	dataFlags, err := r.ReadRC()
	if err != nil {
		return nil, err
	}
	var textExtrusion point3 // 主体 BE 挤出向量（gold extrusion 键导出）
	var elevation float64
	if dataFlags&textFlagNoElevation == 0 {
		if elevation, err = r.ReadRD(); err != nil {
			return nil, err
		}
	}
	var thickness float64
	ix, err := r.ReadRD()
	if err != nil {
		return nil, err
	}
	iy, err := r.ReadRD()
	if err != nil {
		return nil, err
	}
	var alignPt *point2
	if dataFlags&textFlagNoAlign == 0 {
		ax, err := r.ReadDD(ix)
		if err != nil {
			return nil, err
		}
		ay, err := r.ReadDD(iy)
		if err != nil {
			return nil, err
		}
		alignPt = &point2{ax, ay}
	}
	if tex, tey, tez, eErr := r.ReadBE(); eErr != nil { // extrusion（读入保存）
		return nil, eErr
	} else {
		textExtrusion = point3{tex, tey, tez}
	}
	if _, err := r.ReadBT(); err != nil { // thickness
		return nil, err
	}
	var oblique float64
	if dataFlags&textFlagNoOblique == 0 {
		if oblique, err = r.ReadRD(); err != nil { // oblique angle
			return nil, err
		}
	}
	var rotation float64
	if dataFlags&textFlagNoRotation == 0 {
		if rotation, err = r.ReadRD(); err != nil {
			return nil, err
		}
	}
	height, err := r.ReadRD()
	if err != nil {
		return nil, err
	}
	var widthFactor float64
	if dataFlags&textFlagNoWidth == 0 {
		if widthFactor, err = r.ReadRD(); err != nil { // width factor
			return nil, err
		}
	}

	// R2010+ 文本尾：字段顺序在写入端存在两种排列，按参考实现枚举候选取最优。
	text, gen, hAlign, vAlign, err := decodeR21TextTail(r, dataFlags, codepage)
	if err != nil {
		return nil, err
	}
	owner, layer := decodeOwnerLayer(r, head)
	return &entText{
		baseEntity: baseEntity{
			handle: head.handle, color: head.color, layer: layer, owner: owner, mode: head.entityMode,
			extra: map[string]any{
				"dataflags": int64(dataFlags), "thickness": thickness, "elevation": elevation,
				"oblique_angle": oblique, "width_factor": widthFactor,
			},
		},
		text:        text,
		insertion:   point3{ix, iy, elevation},
		height:      height,
		rotation:    rotation,
		hAlign:      hAlign,
		vAlign:      vAlign,
		alignPt:     alignPt,
		gen:         gen,
		widthFactor: widthFactor,
		extrusion:   textExtrusion,
		styleHandle: decodeTextStyleHandle(r, head),
	}, nil
}

// decodeTextUnicode R2007 TEXT 解码（字符串区路径）：text_value 位于对象
// 尾部字符串流（LibreDWG FIELD_T 在 R2007+ 重定向 str_dat，主体位流的
// T 位点不消耗位），主体位流依次为 dataflags RC → 标量段（按 dataflags
// 位跳过）→ generation/horiz_alignment/vert_alignment BS（spec 固定序，
// chuandongzhou trace @53.4 实证 hAlign 紧随 height、TU 位置不推进）。
// 该路径不走候选评分：位流错位候选的字符可读性分可能反超短文本正确解
// （chuandongzhou "A" 13 分 < 错位串 15 分），故仅采纳经 has_strings 位
// 与 data_size 双重校验的字符串流结果；流不可读或标量段失败时返回
// false，由调用方回退 R21 候选评分路径（与 decodeAttribUnicode 同模式）。
func decodeTextUnicode(r *bitstream.BitStream, head *commonEntityHead, codepage uint16) (*entText, bool) {
	strs := readStringAreaStrings(r, head, 1)
	if len(strs) == 0 {
		return nil, false
	}
	dataFlags, err := r.ReadRC()
	if err != nil {
		return nil, false
	}
	var elevation float64
	if dataFlags&textFlagNoElevation == 0 {
		if elevation, err = r.ReadRD(); err != nil {
			return nil, false
		}
	}
	var thickness float64
	ix, err := r.ReadRD()
	if err != nil {
		return nil, false
	}
	iy, err := r.ReadRD()
	if err != nil {
		return nil, false
	}
	var alignPt *point2
	if dataFlags&textFlagNoAlign == 0 {
		ax, err := r.ReadDD(ix)
		if err != nil {
			return nil, false
		}
		ay, err := r.ReadDD(iy)
		if err != nil {
			return nil, false
		}
		alignPt = &point2{ax, ay}
	}
	var textExtrusion point3
	if tex, tey, tez, eErr := r.ReadBE(); eErr != nil { // extrusion（读入保存，gold extrusion 键导出）
		return nil, false
	} else {
		textExtrusion = point3{tex, tey, tez}
	}
	if _, err := r.ReadBT(); err != nil { // thickness
		return nil, false
	}
	var oblique float64
	if dataFlags&textFlagNoOblique == 0 {
		if oblique, err = r.ReadRD(); err != nil { // oblique angle
			return nil, false
		}
	}
	var rotation float64
	if dataFlags&textFlagNoRotation == 0 {
		if rotation, err = r.ReadRD(); err != nil {
			return nil, false
		}
	}
	height, err := r.ReadRD()
	if err != nil {
		return nil, false
	}
	var widthFactor float64
	if dataFlags&textFlagNoWidth == 0 {
		if widthFactor, err = r.ReadRD(); err != nil { // width factor
			return nil, false
		}
	}
	// 对齐三字段为主位流 BS（非字符串区），spec 固定序 gen→hAlign→vAlign
	var gen, hAlign, vAlign uint16
	if dataFlags&textFlagNoGen == 0 {
		v, e := r.ReadBS()
		if e != nil {
			return nil, false
		}
		gen = v
	}
	if dataFlags&textFlagNoHAlign == 0 {
		v, e := r.ReadBS()
		if e != nil {
			return nil, false
		}
		hAlign = v
	}
	if dataFlags&textFlagNoVAlign == 0 {
		v, e := r.ReadBS()
		if e != nil {
			return nil, false
		}
		vAlign = v
	}
	owner, layer := decodeOwnerLayer(r, head)
	return &entText{
		baseEntity: baseEntity{
			handle: head.handle, color: head.color, layer: layer, owner: owner, mode: head.entityMode,
			extra: map[string]any{
				"dataflags": int64(dataFlags), "thickness": thickness, "elevation": elevation,
				"oblique_angle": oblique, "width_factor": widthFactor,
			},
		},
		text:        strs[0],
		insertion:   point3{ix, iy, elevation},
		height:      height,
		rotation:    rotation,
		hAlign:      hAlign,
		vAlign:      vAlign,
		alignPt:     alignPt,
		gen:         gen,
		widthFactor: widthFactor,
		extrusion:   textExtrusion,
		styleHandle: decodeTextStyleHandle(r, head),
	}, true
}

// textTailField 文本尾可选字段：flag 为 data_flags 中的「缺省」位，
// kind 区分 generation/hAlign/vAlign 三个槽位。
type textTailField struct {
	flag uint8
	kind int // 0=gen 1=hAlign 2=vAlign
}

// indexPermutations 字典序全排列的下标序列（与逐元素展开顺序一致，
// 平局时保留靠前候选）。
func indexPermutations(n int) [][]int {
	if n == 0 {
		return [][]int{nil}
	}
	cur := make([]int, n)
	for i := range cur {
		cur[i] = i
	}
	var out [][]int
	for {
		out = append(out, append([]int(nil), cur...))
		// 字典序下一排列：从右向左找首个降序拐点，与拐点右侧大于它的
		// 最小者交换，再反转后缀
		i := n - 2
		for i >= 0 && cur[i] >= cur[i+1] {
			i--
		}
		if i < 0 {
			return out
		}
		j := n - 1
		for cur[j] <= cur[i] {
			j--
		}
		cur[i], cur[j] = cur[j], cur[i]
		for l, r := i+1, n-1; l < r; l, r = l+1, r-1 {
			cur[l], cur[r] = cur[r], cur[l]
		}
	}
}

// decodeR21TextTail R2010+/R2007+ 文本尾解析：generation/hAlign/vAlign 与
// 字符串的相对顺序存在歧义，字符串有 TU/TV 两种编码。枚举「字段全排列 ×
// 编码 × 切分点」候选，按字符可读性与对齐值合理性评分取最优（并列保留
// 首个）；全部候选失败时按旧版字段顺序 + 双编码兜底重试。
func decodeR21TextTail(r *bitstream.BitStream, dataFlags uint8, codepage uint16) (text string, gen, hAlign, vAlign uint16, err error) {
	var present []textTailField
	for _, f := range []textTailField{{textFlagNoGen, 0}, {textFlagNoHAlign, 1}, {textFlagNoVAlign, 2}} {
		if dataFlags&f.flag == 0 {
			present = append(present, f)
		}
	}
	assign := func(f textTailField, v uint16, cg, ch, cv *uint16) {
		switch f.kind {
		case 0:
			*cg = v
		case 1:
			*ch = v
		case 2:
			*cv = v
		}
	}
	orders := indexPermutations(len(present))

	readStr := func(r2 *bitstream.BitStream, tu bool) (string, error) {
		if tu {
			return r2.ReadTU()
		}
		return r2.ReadTV(codepage)
	}
	const minInt = -int(^uint(0)>>1) - 1
	bestScore := minInt / 4
	var bestText string
	var bestGen, bestH, bestV uint16
	hasBest := false
	savedByte, savedBit := r.Cursor()
	for _, tu := range []bool{true, false} {
		for _, order := range orders {
			for split := 0; split <= len(order); split++ {
				r.Restore(savedByte, savedBit)
				candGen, candH, candV := uint16(0), uint16(0), uint16(0)
				ok := true
				for _, idx := range order[:split] {
					v, e := r.ReadBS()
					if e != nil {
						ok = false
						break
					}
					assign(present[idx], v, &candGen, &candH, &candV)
				}
				if !ok {
					continue
				}
				s, e := readStr(r, tu)
				if e != nil {
					continue
				}
				for _, idx := range order[split:] {
					v, e := r.ReadBS()
					if e != nil {
						ok = false
						break
					}
					assign(present[idx], v, &candGen, &candH, &candV)
				}
				if !ok {
					continue
				}
				score := textTailCandidateScore(s, candGen, candH, candV, tu)
				if cadTraceHandle != 0 {
					fmt.Fprintf(os.Stderr, "[tail] tu=%v split=%d score=%d text=%q gen=%d h=%d v=%d end=%d\n", tu, split, score, s, candGen, candH, candV, r.TellBits())
				}
				if !hasBest || score > bestScore {
					hasBest = true
					bestScore, bestText, bestGen, bestH, bestV = score, s, candGen, candH, candV
				}
			}
		}
	}
	if hasBest {
		return bestText, bestGen, bestH, bestV, nil
	}
	// 兜底：旧版字段顺序 + 两种编码
	for _, tu := range []bool{true, false} {
		r.Restore(savedByte, savedBit)
		s, e := readStr(r, tu)
		if e != nil {
			continue
		}
		if dataFlags&textFlagNoGen == 0 {
			if gen, e = r.ReadBS(); e != nil {
				continue
			}
		}
		if dataFlags&textFlagNoHAlign == 0 {
			if hAlign, e = r.ReadBS(); e != nil {
				continue
			}
		}
		if dataFlags&textFlagNoVAlign == 0 {
			if vAlign, e = r.ReadBS(); e != nil {
				continue
			}
		}
		return s, gen, hAlign, vAlign, nil
	}
	r.Restore(savedByte, savedBit)
	return "", 0, 0, 0, fmt.Errorf("cad: R21 文本尾全部候选失败")
}

// readMTextStringBest MTEXT 文字读取：先直接读 TU/TV；若结果过短
// （疑似错位）则在 0..64 位窗口内重扫，按文字评分择优，返回文字与
// 读取结束位。
func readMTextStringBest(r *bitstream.BitStream, codepage uint16) (string, uint64, error) {
	savedByte, savedBit := r.Cursor()
	text, err := r.ReadTU()
	if err != nil {
		r.Restore(savedByte, savedBit)
		if text, err = r.ReadTV(codepage); err != nil {
			return "", 0, err
		}
	}
	if len([]rune(text)) >= 3 && decodedTextScore(text) >= 8 {
		return text, r.TellBits(), nil
	}
	// 错位疑似：窗口内全扫描择优
	base := uint64(savedByte)*8 + uint64(savedBit)
	bestScore := decodedTextScore(text)
	bestText, bestEnd := text, r.TellBits()
	for shift := int64(-64); shift <= 64; shift++ {
		pos := int64(base) + shift
		if pos < 0 {
			continue
		}
		r3 := *r
		r3.SetBitPos(uint64(pos))
		if s, e := r3.ReadTU(); e == nil {
			if sc := decodedTextScore(s); sc > bestScore {
				bestScore, bestText, bestEnd = sc, s, r3.TellBits()
			}
		}
		r3 = *r
		r3.SetBitPos(uint64(pos))
		if s, e := r3.ReadTV(codepage); e == nil {
			if sc := decodedTextScore(s); sc > bestScore {
				bestScore, bestText, bestEnd = sc, s, r3.TellBits()
			}
		}
	}
	return bestText, bestEnd, nil
}

// decodeTextR13R14 R13/R14 TEXT：无条件字段序（elevation BD + ins 2RD +
// alignment 2RD + extrusion 3BD + thickness BD + oblique BD + rotation BD +
// height BD + width BD + TV 文字 + 3×BS 对齐）。
func decodeTextR13R14(r *bitstream.BitStream, head *commonEntityHead, codepage uint16) (any, error) {
	var elevation float64
	var err error
	if elevation, err = r.ReadBD(); err != nil {
		return nil, err
	}
	var thickness, oblique, widthFactor float64
	ix, err := r.ReadRD()
	if err != nil {
		return nil, err
	}
	iy, err := r.ReadRD()
	if err != nil {
		return nil, err
	}
	ax, aerr := r.ReadRD()
	if aerr != nil {
		return nil, aerr
	}
	ay, aerr2 := r.ReadRD()
	if aerr2 != nil {
		return nil, aerr2
	}
	alignPt := &point2{ax, ay} // R13 无 dataflags 位，alignment 恒在流内
	var textExtrusion point3
	if tex, tey, tez, aerr3 := r.Read3BD(); aerr3 != nil { // extrusion（读入保存）
		return nil, aerr3
	} else {
		textExtrusion = point3{tex, tey, tez}
	}
	if thickness, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if oblique, err = r.ReadBD(); err != nil {
		return nil, err
	}
	var rotation float64
	if rotation, err = r.ReadBD(); err != nil {
		return nil, err
	}
	var height float64
	if height, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if widthFactor, err = r.ReadBD(); err != nil {
		return nil, err
	}
	text, err := r.ReadTV(512)
	if err != nil {
		return nil, err
	}
	gen, e1 := r.ReadBS()
	hAlign, e2 := r.ReadBS()
	vAlign, e3 := r.ReadBS()
	if e1 != nil || e2 != nil || e3 != nil {
		return nil, e1
	}
	owner, layer := decodeOwnerLayer(r, head)
	return &entText{
		baseEntity: baseEntity{
			handle: head.handle, color: head.color, layer: layer, owner: owner, mode: head.entityMode,
			extra: map[string]any{
				"thickness": thickness, "elevation": elevation,
				"oblique_angle": oblique, "width_factor": widthFactor,
			},
		},
		text:        text,
		insertion:   point3{ix, iy, elevation},
		height:      height,
		rotation:    rotation,
		hAlign:      hAlign,
		vAlign:      vAlign,
		alignPt:     alignPt,
		gen:         gen,
		widthFactor: widthFactor,
		extrusion:   textExtrusion,
		styleHandle: decodeTextStyleHandle(r, head),
	}, nil
}

// textTailCandidateScore 文本尾候选评分：字符可读性为主，叠加 generation/
// 对齐值的合理范围奖励与 UTF-16 编码先验微奖。
func textTailCandidateScore(text string, gen, hAlign, vAlign uint16, tu bool) int {
	if text == "" {
		return -int(^uint(0)>>1)/2 - 1 // 空串强否决
	}
	score := 0
	for _, ch := range text {
		score += textTailCharWeight(ch)
	}
	if gen <= 6 {
		score += 2
	} else {
		score -= 10
	}
	if hAlign <= 5 {
		score += 6
	} else {
		score -= 12
	}
	if vAlign <= 5 {
		score += 6
	} else {
		score -= 12
	}
	if tu {
		score++
	}
	return score
}

// textTailCharWeight 单字符可读性评分：替换符/控制字符重罚；ASCII 可见
// 字符与空白奖励；CJK 各区（标点/平假名/片假名/表意/全角形式）与箭头、
// 几何图形区按工程图纸常见度加权；其余字母数字轻奖、杂符轻罚。
func textTailCharWeight(ch rune) int {
	const replacement = '\uFFFD'
	if ch == replacement {
		return -24
	}
	if unicode.IsControl(ch) && ch != '\n' && ch != '\r' && ch != '\t' {
		return -20
	}
	if ch < 0x80 && unicode.IsGraphic(ch) {
		return 4
	}
	switch ch {
	case ' ', '\n', '\r', '\t':
		return 4
	}
	for _, band := range []struct{ lo, hi rune }{
		{0x3000, 0x303F}, // CJK 标点
		{0x3040, 0x309F}, // 平假名
		{0x30A0, 0x30FF}, // 片假名
		{0x4E00, 0x9FFF}, // CJK 统一表意
		{0xFF01, 0xFF60}, // 全角形式
		{0xFF61, 0xFF9F}, // 半角片假名
	} {
		if ch >= band.lo && ch <= band.hi {
			return 6
		}
	}
	switch {
	case ch >= 0x2190 && ch <= 0x21FF, // 箭头
		ch >= 0x25A0 && ch <= 0x25FF: // 几何图形
		return 4
	}
	if unicode.IsLetter(ch) || unicode.IsDigit(ch) {
		return 2
	}
	return -4
}

// readTextString 尝试 TU（UTF-16）与 TV（codepage）两种编码读取文本。
func readTextString(r *bitstream.BitStream, codepage uint16) (string, error) {
	sb, sbit := r.Cursor()
	s1, err1 := r.ReadTU()
	if err1 == nil && decodedTextScore(s1) > -8 {
		return s1, nil
	}
	r.Restore(sb, sbit)
	s2, err2 := r.ReadTV(codepage)
	if err2 == nil && decodedTextScore(s2) >= decodedTextScore(s1) {
		return s2, nil
	}
	if err1 == nil {
		r.Restore(sb, sbit)
		_, _ = r.ReadTU()
		return s1, nil
	}
	return s2, err2
}

// decodedTextScore 评估解码文本的可读性：可打印 ASCII 与 CJK 得正分，
// 控制字符/替换符得负分。
func decodedTextScore(s string) int {
	score := 0
	for _, ch := range s {
		switch {
		case ch >= 0x20 && ch < 0x7F:
			score += 2
		case ch >= 0x4E00 && ch <= 0x9FFF, // CJK 统一表意
			ch >= 0x3000 && ch <= 0x303F, // CJK 标点
			ch >= 0xFF00 && ch <= 0xFFEF: // 全角字符
			score += 2
		case ch == '\t', ch == '\n':
			score += 1
		case ch == '�':
			score -= 8
		case ch < 0x20:
			score -= 4
		default:
			score += 1
		}
	}
	return score
}

// decodeMText MTEXT（R2007+ 布局）：插入点/方向/宽度/高度 + 富文本 + 行距 +
// 背景数据 + R2018 标注/分栏标志。
func decodeMText(r *bitstream.BitStream, head *commonEntityHead, codepage uint16, ver container.DwgVersion) (any, error) {
	ix, iy, iz, err := r.Read3BD()
	if err != nil {
		return nil, err
	}
	exx, exy, exz, err := r.Read3BD() // extrusion（读入保存，gold 键导出）
	if err != nil {
		return nil, err
	}
	xx, xy, xz, err := r.Read3BD() // x-axis dir
	if err != nil {
		return nil, err
	}
	rectWidth, err := r.ReadBD()
	if err != nil {
		return nil, err
	}
	rectHeight, err := r.ReadBD() // rect height（R2007+ 恒有）
	if err != nil {
		return nil, err
	}
	textHeight, err := r.ReadBD()
	if err != nil {
		return nil, err
	}
	attachment, err := r.ReadBS()
	if err != nil {
		return nil, err
	}
	flowDir, err := r.ReadBS() // drawing dir
	if err != nil {
		return nil, err
	}
	extentsHeight, err := r.ReadBD()
	if err != nil {
		return nil, err
	}
	extentsWidth, err := r.ReadBD()
	if err != nil {
		return nil, err
	}
	// R2007+ 文字存于记录尾部字符串区（LibreDWG obj_string_stream 机制）：
	// [文字区 data_size 字节][has_strings B @bitsize-1][data_size RS @bitsize-17][handle 流]
	var text string
	if strs := readStringAreaStrings(r, head, 1); len(strs) > 0 {
		// R2007+ 字符串区命中：text 在数据流中不占位（obj string stream 机制）
		text = strs[0]
	} else {
		// 字符串区缺失：数据流内联读取，错位时窗口扫描择优
		var textEnd uint64
		var rErr error
		if text, textEnd, rErr = readMTextStringBest(r, codepage); rErr != nil {
			return nil, rErr
		}
		r.SetBitPos(textEnd)
	}
	lsStyle, err := r.ReadBS() // linespacing style
	if err != nil {
		return nil, err
	}
	lsFactor, err := r.ReadBD() // linespacing factor
	if err != nil {
		return nil, err
	}
	var unknownB0 int64
	if ub, e := r.ReadB(); e != nil { // unknown bit
		return nil, e
	} else {
		unknownB0 = b2int(ub == 1)
	}
	// 背景数据（R2004+ 背景标志字段，R2007+ 恒有）
	bgFlags, err := r.ReadBL()
	if err != nil {
		return nil, err
	}
	if bgFlags&0x01 != 0 || bgFlags&0x10 != 0 {
		sb, sbit := r.Cursor()
		if err := readMTextBackground(r); err != nil {
			// 背景解析失败不影响主文本，回滚跳过
			r.Restore(sb, sbit)
		}
	}
	// R2018 标注/分栏标志（spec SINCE R_2018：is_not_annotative 及可选列数据）
	var isNotAnnotative, classVersion, defaultFlag, ignoreAttachment, columnType int64
	var colHeightCount int64
	var colHeights []float64
	var colWidth, colGutter float64
	var colAutoHeight, colFlowReversed int64
	if ver >= container.VerR2018 {
		if na, e := r.ReadB(); e != nil {
			return nil, e
		} else {
			isNotAnnotative = b2int(na == 1)
		}
		if isNotAnnotative == 1 {
			if cv, e := r.ReadBS(); e == nil {
				classVersion = int64(cv)
			}
			if df, e := r.ReadB(); e == nil {
				defaultFlag = b2int(df == 1)
			}
			// appid 句柄存于 handle 流（LibreDWG trace @222.0），数据流不占位
			if ia, e := r.ReadBL(); e == nil {
				ignoreAttachment = int64(ia)
			}
			if _, _, _, e := r.Read3BD(); e != nil { // x_axis_dir 冗余
				return nil, e
			}
			if _, _, _, e := r.Read3BD(); e != nil { // ins_pt 冗余
				return nil, e
			}
			for _, bd := range [4]func() (float64, error){
				r.ReadBD, r.ReadBD, r.ReadBD, r.ReadBD, // rect_w/rect_h/ext_w/ext_h 冗余
			} {
				if _, e := bd(); e != nil {
					return nil, e
				}
			}
			if ct, e := r.ReadBS(); e == nil {
				columnType = int64(ct)
			}
			// 分栏尾段（spec SINCE R_2018，column_type!=0 时存在）：
			// 计数 BL → 列宽 BD → 间距 BD → 自适应高 B → 流反转 B →
			// n×列高 BD（仅 !auto_height && column_type==2；type==1 时
			// 该 BL 键为 numfragments，无列高）。字段序经 dwg.spec MTEXT
			// decoder 段与 sunmingle trace @111.5 逐位实证，与 dwgread
			// gold 的 JSON 键序（width/gutter/auto/flow/heights）一致。
			if columnType != 0 {
				if cnt, e := r.ReadBL(); e == nil {
					colHeightCount = int64(cnt)
					if cw, e := r.ReadBD(); e == nil {
						colWidth = cw
					}
					if g, e := r.ReadBD(); e == nil {
						colGutter = g
					}
					if ah, e := r.ReadB(); e == nil {
						colAutoHeight = b2int(ah == 1)
					}
					if fr, e := r.ReadB(); e == nil {
						colFlowReversed = b2int(fr == 1)
					}
					if colAutoHeight == 0 && columnType == 2 {
						heights := make([]float64, 0, min(int(cnt), 1024))
						for i := 0; i < int(cnt) && i < 1024; i++ {
							hv, he := r.ReadBD()
							if he != nil {
								break
							}
							heights = append(heights, hv)
						}
						colHeights = heights
					}
				}
			}
		}
	}
	owner, layer := decodeOwnerLayer(r, head)
	return &entMText{
		baseEntity: baseEntity{
			handle: head.handle, color: head.color, layer: layer, owner: owner, mode: head.entityMode,
			extra: map[string]any{
				"flow_dir": int64(flowDir), "extents_height": extentsHeight, "extents_width": extentsWidth,
				"linespace_style": int64(lsStyle), "linespace_factor": lsFactor, "unknown_b0": unknownB0,
				"rect_height": rectHeight, "bg_fill_flag": int64(bgFlags),
				"is_not_annotative": isNotAnnotative, "class_version": classVersion,
				"default_flag": defaultFlag, "ignore_attachment": ignoreAttachment,
				"column_type":         columnType,
				"column_height_count": colHeightCount, "column_heights": colHeights,
				"column_width": colWidth, "gutter": colGutter,
				"auto_height": colAutoHeight, "flow_reversed": colFlowReversed,
			},
		},
		text:       text,
		insertion:  point3{ix, iy, iz},
		xAxisDir:   point3{xx, xy, xz},
		rectWidth:  rectWidth,
		textHeight: textHeight,
		attachment: attachment,
		lineFactor: lsFactor,
		extrusion:  point3{exx, exy, exz},

		styleHandle: decodeTextStyleHandle(r, head),
	}, nil
}

// readMTextBackground 读取 MTEXT 背景填充数据（CMC 颜色形式 + 缩放 + 透明度）。
func readMTextBackground(r *bitstream.BitStream) error {
	if _, err := r.ReadBD(); err != nil { // scale factor
		return err
	}
	if _, err := r.ReadBS(); err != nil { // bg color index
		return err
	}
	if _, err := r.ReadBL(); err != nil { // rgb
		return err
	}
	flagByte, err := r.ReadRC()
	if err != nil {
		return err
	}
	if flagByte&0x01 != 0 {
		if _, err := r.ReadTU(); err != nil {
			return err
		}
	}
	if flagByte&0x02 != 0 {
		if _, err := r.ReadTU(); err != nil {
			return err
		}
	}
	if _, err := r.ReadBL(); err != nil { // transparency
		return err
	}
	return nil
}

// decodeInsert INSERT：插入点 + 缩放标志驱动 + 旋转 + 块头与属性句柄。
// ver 决定版本分支：R13/R14 无缩放标志（3BD_1）且无 num_owned；
// 属性句柄 R13~R2000 为 first/last 两个，R2004+ 为 num_owned 计数向量
// （对齐 dwg.spec INSERT 的 VERSIONS/SINCE 分支）。
func decodeInsert(r *bitstream.BitStream, head *commonEntityHead, ver container.DwgVersion) (any, error) {
	r13r14 := ver == container.VerR13 || ver == container.VerR14
	px, py, pz, err := r.Read3BD()
	if err != nil {
		return nil, err
	}
	var sx, sy, sz float64 = 1, 1, 1
	// scaleFlag R2000+ 的 BB 缩放标志（R13/R14 无此字段，恒 0）
	var scaleFlag int64
	if r13r14 {
		// R13/R14：3BD_1 = 三个无条件 BD（LibreDWG FIELD_3BD_1），无 BB 标志
		var e1, e2, e3 error
		sx, e1 = r.ReadBD()
		sy, e2 = r.ReadBD()
		sz, e3 = r.ReadBD()
		if e1 != nil {
			return nil, e1
		}
		if e2 != nil {
			return nil, e2
		}
		if e3 != nil {
			return nil, e3
		}
	} else {
		// R2000+：BB 标志 + 差分/混合形式
		var dataFlags uint8
		dataFlags, err = r.ReadBB()
		if err != nil {
			return nil, err
		}
		scaleFlag = int64(dataFlags)
		switch dataFlags {
		case 0x03: // 全 1
		case 0x01:
			if sy, err = r.ReadDD(1.0); err != nil {
				return nil, err
			}
			if sz, err = r.ReadDD(1.0); err != nil {
				return nil, err
			}
		case 0x02:
			if sx, err = r.ReadRD(); err != nil {
				return nil, err
			}
			sy, sz = sx, sx
		default:
			if sx, err = r.ReadRD(); err != nil {
				return nil, err
			}
			if sy, err = r.ReadDD(sx); err != nil {
				return nil, err
			}
			if sz, err = r.ReadDD(sx); err != nil {
				return nil, err
			}
		}
	}
	rotation, err := r.ReadBD()
	if err != nil {
		return nil, err
	}
	exx, exy, exz, err := r.Read3BD() // extrusion（读入保存，gold extrusion 键导出）
	if err != nil {
		return nil, err
	}
	hasAttribs, err := r.ReadB()
	if err != nil {
		return nil, err
	}
	hasAttribsFlag := b2int(hasAttribs == 1)
	// num_owned 计数 R2004+ 才在主位流（spec SINCE(R_2004a) FIELD_BL num_owned）
	ownedCount := uint32(0)
	if hasAttribs == 1 && ver >= container.VerR2004 {
		if ownedCount, err = r.ReadBL(); err != nil {
			return nil, err
		}
	}

	// 块头与属性句柄在 handle 流中，位于公共 handle 之后。
	savedByte, savedBit := r.Cursor()
	r.SetBitPos(head.objSizeBit)
	var blockHeader uint64
	var attribHandles []uint64
	var seqendHandle uint64
	if owner, layer, err := parseCommonEntityHandles(r, head); err == nil {
		if blockHeader, err = objrec.ReadHandleReference(r, head.handle); err == nil {
			if hasAttribs == 1 {
				if ver == container.VerR13 || ver == container.VerR14 || ver == container.VerR2000 {
					// R13~R2000：固定 first/last 两个句柄
					first, e1 := objrec.ReadHandleReference(r, head.handle)
					last, e2 := objrec.ReadHandleReference(r, head.handle)
					if e1 == nil && e2 == nil {
						attribHandles = append(attribHandles, first, last)
					}
				} else {
					for i := uint32(0); i < ownedCount; i++ {
						h, herr := objrec.ReadHandleReference(r, head.handle)
						if herr != nil {
							break
						}
						attribHandles = append(attribHandles, h)
					}
				}
				// SEQEND 结束句柄（attribs 之后恒在，gold seqend 键导出）
				if h, e := objrec.ReadHandleReference(r, head.handle); e == nil {
					seqendHandle = h
				}
			}
			r.Restore(savedByte, savedBit)
			return &entInsert{
				baseEntity: baseEntity{
					handle: head.handle, color: head.color, layer: layer, owner: owner, mode: head.entityMode,
					extra: map[string]any{"scale_flag": scaleFlag, "has_attribs": hasAttribsFlag},
				},
				position:    point3{px, py, pz},
				scale:       point3{sx, sy, sz},
				rotation:    rotation,
				extrusion:   point3{exx, exy, exz},
				blockHeader: blockHeader,
				attribs:     attribHandles,
				seqend:      seqendHandle,
			}, nil
		}
	}
	r.Restore(savedByte, savedBit)
	return nil, fmt.Errorf("cad: INSERT 块头句柄解析失败（handle %d）", head.handle)
}

// decodeAttrib ATTRIB：字段布局与 TEXT 同构 + 标签串。
func decodeAttrib(r *bitstream.BitStream, head *commonEntityHead, codepage uint16) (any, error) {
	t, err := decodeText(r, head, codepage)
	if err != nil {
		return nil, err
	}
	text := t.(*entText)
	// ATTRIB 在 TEXT 尾部 handle 流后还有 tag/version 等字段；tag 为 TV/TU 串。
	tag, err := readTextString(r, codepage)
	if err != nil {
		// 标签解析失败时保留文本主体
		tag = ""
	}
	return &entAttrib{
		baseEntity:  text.baseEntity,
		text:        text.text,
		tag:         tag,
		insertion:   text.insertion,
		height:      text.height,
		rotation:    text.rotation,
		hAlign:      text.hAlign,
		vAlign:      text.vAlign,
		gen:         text.gen,
		alignPt:     text.alignPt, // 对齐点与 TEXT 同构继承
		widthFactor: text.widthFactor,
		extrusion:   text.extrusion,
		styleHandle: text.styleHandle,
	}, nil
}

// parseCommonEntityHeadR2013B 公共实体头变体 B：material 标志后无 shadow 字节。
// 部分 R2013+ 写入端存在该布局差异，用于候选评分对照（变体 A 含 shadow 字节）。
func parseCommonEntityHeadR2013B(r *bitstream.BitStream, dataEnd uint64) (commonEntityHead, error) {
	var head commonEntityHead
	head.objSizeBit = dataEnd
	head.auditBitsize = dataEnd

	h, err := r.ReadH()
	if err != nil {
		return head, err
	}
	head.handle = h.Value

	// EED 链（含组码对解析，供审计导出）
	var eedOK bool
	if head.eed, eedOK = parseEntityEEDChain(r, false); !eedOK {
		return head, fmt.Errorf("cad: EED 链解析失败")
	}

	picFlag, err := r.ReadB()
	if err != nil {
		return head, err
	}
	head.previewExists = picFlag == 1
	if picFlag == 1 {
		graphicSize, err := r.ReadBLL()
		if err != nil {
			return head, err
		}
		if graphicSize > uint64(len(r.Src))*8 {
			return head, fmt.Errorf("cad: 图形图像大小异常 %d", graphicSize)
		}
		if head.preview, err = r.ReadRCS(int(graphicSize)); err != nil {
			return head, err
		}
	}

	if head.entityMode, err = r.ReadBB(); err != nil {
		return head, err
	}
	reactors, err := r.ReadBL()
	if err != nil {
		return head, err
	}
	if reactors > 1<<20 {
		return head, fmt.Errorf("cad: reactor 数量异常 %d", reactors)
	}
	head.numReactors = reactors
	if flag, err := r.ReadB(); err != nil {
		return head, err
	} else {
		head.xdicMissing = flag == 1
	}
	if flag, err := r.ReadB(); err != nil {
		return head, err
	} else {
		head.hasDsBinary = flag == 1
	}

	noLinks, err := r.ReadB()
	if err != nil {
		return head, err
	}
	head.noLinks = noLinks == 1
	if noLinks == 0 {
		colorMode, err := r.ReadB()
		if err != nil {
			return head, err
		}
		head.color.hasIndex = true
		if colorMode == 1 {
			idx, err := r.ReadRC()
			if err != nil {
				return head, err
			}
			head.color.index, head.color.flag = uint16(idx), uint16(idx)>>8
		} else {
			flags, err := r.ReadRS()
			if err != nil {
				return head, err
			}
			head.color.index, head.color.flag = flags&0x01FF, flags>>8
			if flags&0x8000 != 0 {
				rgb, err := r.ReadBL()
				if err != nil {
					return head, err
				}
				// 同上：保留完整 32 位对齐 LibreDWG rgb 键口径
				head.color.trueColor, head.color.hasTrue = rgb, true
			}
			if flags&0x2000 != 0 {
				raw, err := r.ReadBL()
				if err != nil {
					return head, err
				}
				head.color.alphaRaw, head.color.alphaType, head.color.alpha = uint32(raw), uint8(raw>>24), uint8(raw)
				head.color.hasAlpha = true
			}
		}
	} else {
		// BS 短格式：次位 1=256（ByLayer，flag 1），0=0（ByBlock，flag 0）
		second, err := r.ReadB()
		if err != nil {
			return head, err
		}
		head.color.hasIndex = true
		if second == 1 {
			head.color.index, head.color.flag = 256, 1
		} else {
			head.color.index, head.color.flag = 0, 0
		}
	}

	if head.ltypeScale, err = r.ReadBD(); err != nil {
		return head, err
	}
	if head.ltypeFlags, err = r.ReadBB(); err != nil {
		return head, err
	}
	if head.plotstyleFlgs, err = r.ReadBB(); err != nil {
		return head, err
	}
	if head.materialFlags, err = r.ReadBB(); err != nil {
		return head, err
	}
	// 变体 B：无 shadow 字节
	for i := 0; i < 3; i++ {
		if _, err := r.ReadB(); err != nil {
			return head, err
		}
	}
	if _, err := r.ReadBS(); err != nil {
		return head, err
	}
	if _, err := r.ReadRC(); err != nil {
		return head, err
	}
	return head, nil
}

// ---- 版本相关的公共实体头 ----

// ---- 版本化文本解码 ----

// decodeTextVer TEXT：R2010+ 走 R21 候选尾，R2000/R2004 走 TV 顺序字段。
func decodeTextVer(r *bitstream.BitStream, head *commonEntityHead, codepage uint16, unicodeText bool) (any, error) {
	if unicodeText {
		return decodeText(r, head, codepage)
	}
	// R13/R14：数据流中无 data_flags，字段无条件排列
	if head.r13r14 {
		return decodeTextR13R14(r, head, codepage)
	}
	dataFlags, err := r.ReadRC()
	if err != nil {
		return nil, err
	}
	var textExtrusion point3 // 主体 BE 挤出向量（gold extrusion 键导出）
	var elevation float64
	if dataFlags&textFlagNoElevation == 0 {
		if elevation, err = r.ReadRD(); err != nil {
			return nil, err
		}
	}
	ix, err := r.ReadRD()
	if err != nil {
		return nil, err
	}
	iy, err := r.ReadRD()
	if err != nil {
		return nil, err
	}
	var alignPt *point2
	if dataFlags&textFlagNoAlign == 0 {
		ax, err := r.ReadDD(ix)
		if err != nil {
			return nil, err
		}
		ay, err := r.ReadDD(iy)
		if err != nil {
			return nil, err
		}
		alignPt = &point2{ax, ay}
	}
	if tex, tey, tez, err := r.ReadBE(); err != nil { // extrusion（读入保存）
		return nil, err
	} else {
		textExtrusion = point3{tex, tey, tez}
	}
	var thickness float64
	if thickness, err = r.ReadBT(); err != nil {
		return nil, err
	}
	var oblique float64
	if dataFlags&textFlagNoOblique == 0 {
		if oblique, err = r.ReadRD(); err != nil {
			return nil, err
		}
	}
	var rotation float64
	if dataFlags&textFlagNoRotation == 0 {
		if rotation, err = r.ReadRD(); err != nil {
			return nil, err
		}
	}
	height, err := r.ReadRD()
	if err != nil {
		return nil, err
	}
	var widthFactor float64
	if dataFlags&textFlagNoWidth == 0 {
		if widthFactor, err = r.ReadRD(); err != nil {
			return nil, err
		}
	}
	text, err := r.ReadTV(codepage)
	if err != nil {
		return nil, err
	}
	gen, hAlign, vAlign := uint16(0), uint16(0), uint16(0)
	if dataFlags&textFlagNoGen == 0 {
		if gen, err = r.ReadBS(); err != nil {
			return nil, err
		}
	}
	if dataFlags&textFlagNoHAlign == 0 {
		if hAlign, err = r.ReadBS(); err != nil {
			return nil, err
		}
	}
	if dataFlags&textFlagNoVAlign == 0 {
		if vAlign, err = r.ReadBS(); err != nil {
			return nil, err
		}
	}
	owner, layer := decodeOwnerLayer(r, head)
	return &entText{
		baseEntity: baseEntity{
			handle: head.handle, color: head.color, layer: layer, owner: owner, mode: head.entityMode,
			extra: map[string]any{
				"dataflags": int64(dataFlags), "thickness": thickness, "elevation": elevation,
				"oblique_angle": oblique, "width_factor": widthFactor,
			},
		},
		text:        text,
		insertion:   point3{ix, iy, elevation},
		height:      height,
		rotation:    rotation,
		hAlign:      hAlign,
		vAlign:      vAlign,
		alignPt:     alignPt,
		gen:         gen,
		widthFactor: widthFactor,
		extrusion:   textExtrusion,
		styleHandle: decodeTextStyleHandle(r, head),
	}, nil
}

// decodeMTextVer MTEXT：R2004 无背景/rect_height、TV 文本；R2007+ 带背景与 TU。
func decodeMTextVer(r *bitstream.BitStream, head *commonEntityHead, codepage uint16, unicodeText, hasBackground bool, ver container.DwgVersion) (any, error) {
	if hasBackground {
		return decodeMText(r, head, codepage, ver)
	}
	ix, iy, iz, err := r.Read3BD()
	if err != nil {
		return nil, err
	}
	exx, exy, exz, err := r.Read3BD() // extrusion（读入保存，gold 键导出）
	if err != nil {
		return nil, err
	}
	xx, xy, xz, err := r.Read3BD()
	if err != nil {
		return nil, err
	}
	rectWidth, err := r.ReadBD()
	if err != nil {
		return nil, err
	}
	textHeight, err := r.ReadBD()
	if err != nil {
		return nil, err
	}
	attachment, err := r.ReadBS()
	if err != nil {
		return nil, err
	}
	flowDir, err := r.ReadBS() // drawing dir
	if err != nil {
		return nil, err
	}
	extentsHeight, err := r.ReadBD()
	if err != nil {
		return nil, err
	}
	extentsWidth, err := r.ReadBD()
	if err != nil {
		return nil, err
	}
	text, err := r.ReadTV(codepage)
	if err != nil {
		return nil, err
	}
	// linespacing 三件套仅 R2000+ 存在（LibreDWG spec SINCE R_2000b）；
	// R13/R14 的 text 之后即 handle 流，多读会越界导致整个实体错位
	var lsStyle uint16
	var unknownB0, bgFlags int64
	var lsFactor float64
	if !head.r13r14 {
		if lsStyle, err = r.ReadBS(); err != nil { // linespacing style
			return nil, err
		}
		if lsFactor, err = r.ReadBD(); err != nil { // linespacing factor
			return nil, err
		}
		if ub, e := r.ReadB(); e != nil { // unknown bit
			return nil, e
		} else {
			unknownB0 = b2int(ub == 1)
		}
		if ver >= container.VerR2004 { // 背景标志（spec SINCE R_2004a，R2007+ 走背景路径）
			bf, e := r.ReadBL()
			if e != nil {
				return nil, e
			}
			bgFlags = int64(bf)
			if bgFlags&0x01 != 0 {
				sb, sbit := r.Cursor()
				if bErr := readMTextBackground(r); bErr != nil {
					r.Restore(sb, sbit)
				}
			}
		}
	}
	owner, layer := decodeOwnerLayer(r, head)
	return &entMText{
		baseEntity: baseEntity{
			handle: head.handle, color: head.color, layer: layer, owner: owner, mode: head.entityMode,
			extra: map[string]any{
				"flow_dir": int64(flowDir), "extents_height": extentsHeight, "extents_width": extentsWidth,
				"linespace_style": int64(lsStyle), "linespace_factor": lsFactor, "unknown_b0": unknownB0,
				"bg_fill_flag": bgFlags,
			},
		},
		text:        text,
		insertion:   point3{ix, iy, iz},
		xAxisDir:    point3{xx, xy, xz},
		rectWidth:   rectWidth,
		textHeight:  textHeight,
		attachment:  attachment,
		lineFactor:  lsFactor,
		extrusion:   point3{exx, exy, exz},
		styleHandle: decodeTextStyleHandle(r, head),
	}, nil
}

// decodeAttribUnicode R2007+ 属性解码（字符串区路径）：text_value/tag/prompt
// 位于对象字符串流，主体位流的 TU 读点不消耗位（LibreDWG FIELD_T 在
// R2007+ 重定向 str_dat）。位流主体依次为：dataflags RC → TEXT 标量段
// （按 dataflags 位跳过，text 位点空置）→ is_locked_in_block RC(R2010+) →
// mtext_type RC(R2018+) → tag（字符串区）→ field_length BS → flags RC →
// lock_position_flag B(R2007+) → keep_duplicate_records RC(R2010+)。
// 字符串区不可读或标量段失败时返回 false，调用方回退宽容路径。
func decodeAttribUnicode(r *bitstream.BitStream, head *commonEntityHead, ver container.DwgVersion, attdef bool) (*entAttrib, bool) {
	strs := readStringAreaStrings(r, head, 3)
	if len(strs) == 0 {
		return nil, false
	}
	df, err := r.ReadRC()
	if err != nil {
		return nil, false
	}
	var elevation float64
	if df&0x01 == 0 {
		if elevation, err = r.ReadRD(); err != nil {
			return nil, false
		}
	}
	ix, err := r.ReadRD()
	if err != nil {
		return nil, false
	}
	iy, err := r.ReadRD()
	if err != nil {
		return nil, false
	}
	var alignPt *point2
	if df&0x02 == 0 {
		ax, e1 := r.ReadDD(ix)
		if e1 != nil {
			return nil, false
		}
		ay, e2 := r.ReadDD(iy)
		if e2 != nil {
			return nil, false
		}
		alignPt = &point2{ax, ay}
	}
	var textExtrusion point3
	if tex, tey, tez, eErr := r.ReadBE(); eErr != nil { // extrusion（读入保存，gold extrusion 键导出）
		return nil, false
	} else {
		textExtrusion = point3{tex, tey, tez}
	}
	var thickness float64
	if thickness, err = r.ReadBT(); err != nil {
		return nil, false
	}
	var oblique float64
	if df&0x04 == 0 {
		if oblique, err = r.ReadRD(); err != nil {
			return nil, false
		}
	}
	var rotation float64
	if df&0x08 == 0 {
		if rotation, err = r.ReadRD(); err != nil {
			return nil, false
		}
	}
	height, err := r.ReadRD()
	if err != nil {
		return nil, false
	}
	widthFactor := 1.0
	if df&0x10 == 0 {
		if widthFactor, err = r.ReadRD(); err != nil {
			return nil, false
		}
	}
	// text_value：字符串区 strs[0]（位点不消耗位流）
	// 文字对齐三字段为位流 BS（非字符串区），受 dataflags 位控制
	// （0x20 无 generation、0x40 无 horiz、0x80 无 vert，同 TEXT 布局）
	var gen, hAlign, vAlign int64
	if df&0x20 == 0 {
		g2, e := r.ReadBS()
		if e != nil {
			return nil, false
		}
		gen = int64(g2)
	}
	if df&0x40 == 0 {
		h2, e := r.ReadBS()
		if e != nil {
			return nil, false
		}
		hAlign = int64(h2)
	}
	if df&0x80 == 0 {
		v2, e := r.ReadBS()
		if e != nil {
			return nil, false
		}
		vAlign = int64(v2)
	}
	var locked int64
	if ver >= container.VerR2010 {
		lb, e := r.ReadRC()
		if e != nil {
			return nil, false
		}
		locked = int64(lb)
		if locked > 2 { // VALUEOUTOFBOUNDS(is_locked_in_block, 2)
			locked = 0
		}
	}
	var mtextType int64
	if ver >= container.VerR2018 {
		mt, e := r.ReadRC()
		if e != nil {
			return nil, false
		}
		mtextType = int64(mt)
		if mtextType > 1 { // 多行属性含嵌入 MTEXT 段，暂走宽容路径
			return nil, false
		}
	}
	tag := ""
	if len(strs) >= 2 {
		tag = strs[1]
	}
	var fieldLength, flags uint16
	var lockPosition, keepDuplicate int64
	fl, e := r.ReadBS()
	if e != nil {
		return nil, false
	}
	fieldLength = fl
	fg, e := r.ReadRC()
	if e != nil {
		return nil, false
	}
	flags = uint16(fg)
	lp, e := r.ReadB()
	if e != nil {
		return nil, false
	}
	lockPosition = b2int(lp == 1)
	if ver >= container.VerR2010 {
		kd, e := r.ReadRC()
		if e != nil {
			return nil, false
		}
		keepDuplicate = int64(kd)
		if keepDuplicate > 1 { // VALUEOUTOFBOUNDS(keep_duplicate_records, 1)
			keepDuplicate = 0
		}
	}
	var prompt string
	if attdef && len(strs) >= 3 {
		prompt = strs[2]
	}
	extra := map[string]any{
		"dataflags": int64(df),
		"thickness": thickness,
		// gold elevation/oblique_angle 键（R21 路径同导出口径）
		"elevation":              elevation,
		"oblique_angle":          oblique,
		"width_factor":           widthFactor,
		"field_length":           int64(fieldLength),
		"flags":                  int64(flags),
		"lock_position_flag":     lockPosition,
		"is_locked_in_block":     locked,
		"keep_duplicate_records": keepDuplicate,
		"mtext_type":             mtextType,
	}
	return &entAttrib{
		baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode, extra: extra},
		text:       strs[0], tag: tag, prompt: prompt,
		insertion: point3{ix, iy, elevation},
		height:    height, rotation: rotation,
		hAlign: uint16(hAlign), vAlign: uint16(vAlign), gen: uint16(gen),
		alignPt:     alignPt,
		widthFactor: widthFactor,
		extrusion:   textExtrusion,
		styleHandle: decodeTextStyleHandle(r, head),
	}, true
}

// decodeAttribVer ATTRIB/ATTDEF：按版本复用 TEXT 解码后再读标签与属性标志段
// （tag TV → field_length BS → flags RC → lock_position B(R2007+) →
// keep_duplicate RC(R2010+) → mtext_type RC(R2018+) → prompt TV(仅 ATTDEF)）。
func decodeAttribVer(r *bitstream.BitStream, head *commonEntityHead, codepage uint16, unicodeText bool, ver container.DwgVersion, attdef bool) (any, error) {
	entryByte, entryBit := r.Cursor()
	if unicodeText {
		// R2007+ 首选字符串区路径（主体位流不含字符串数据）
		if a, ok := decodeAttribUnicode(r, head, ver, attdef); ok {
			return a, nil
		}
		r.Restore(entryByte, entryBit)
	}
	var t any
	var err error
	if unicodeText {
		t, err = decodeText(r, head, codepage)
	} else {
		t, err = decodeTextVer(r, head, codepage, false)
	}
	if err != nil {
		// annotative 等 ATTRIB 在宽度因子与文字之间存在未公开字段
		// （实测 27 位）：常规解析失败时在窗口内前向扫描 TU/TV 文字串，
		// 取首个可读串并同步读取位置（宽容模式，与参考实现行为对齐）。
		if !unicodeText {
			return nil, err
		}
		// R2013+ 字符串区优先：文字与标签存于记录尾部字符串区
		if strs := readStringAreaStrings(r, head, 2); len(strs) >= 1 {
			// 重读前段几何字段
			r2 := *r
			r2.Restore(entryByte, entryBit)
			df, dfe := r2.ReadRC()
			if dfe == nil {
				var elevation, height float64
				if df&0x01 == 0 {
					elevation, _ = r2.ReadRD()
				}
				ix, _ := r2.ReadRD()
				iy, _ := r2.ReadRD()
				if df&0x02 == 0 {
					r2.ReadDD(ix)
					r2.ReadDD(iy)
				}
				r2.ReadBE()
				r2.ReadBT()
				if df&0x04 == 0 {
					r2.ReadRD()
				}
				if df&0x08 == 0 {
					r2.ReadRD()
				}
				height, _ = r2.ReadRD()
				if df&0x10 == 0 {
					r2.ReadRD()
				}
				tag := ""
				if len(strs) >= 2 {
					tag = strs[1]
				}
				return &entAttrib{
					baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode},
					text:       strs[0], tag: tag, insertion: point3{ix, iy, elevation},
					height: height,
				}, nil
			}
		}
		// 重读前段字段（data_flags/插入点/高度/宽度等已验证可正确解码）
		r2 := *r
		r2.Restore(entryByte, entryBit)
		df, dfe := r2.ReadRC()
		if dfe != nil {
			return nil, err
		}
		var elevation float64
		if df&0x01 == 0 {
			elevation, _ = r2.ReadRD()
		}
		ix, _ := r2.ReadRD()
		iy, _ := r2.ReadRD()
		ins := point3{ix, iy, elevation}
		if df&0x02 == 0 {
			_, e1 := r2.ReadDD(ix)
			_, e2 := r2.ReadDD(iy)
			if e1 != nil || e2 != nil {
				return nil, err
			}
		}
		if _, _, _, e1 := r2.ReadBE(); e1 != nil {
			return nil, err
		}
		if _, e1 := r2.ReadBT(); e1 != nil {
			return nil, err
		}
		if df&0x04 == 0 {
			r2.ReadRD()
		}
		var rotation float64
		if df&0x08 == 0 {
			rotation, _ = r2.ReadRD()
		}
		height, _ := r2.ReadRD()
		if df&0x10 == 0 {
			r2.ReadRD()
		}
		base := r2.TellBits()
		bestScore := math.MinInt32
		bestText := ""
		bestEnd := base
		for shift := uint64(0); shift <= 64; shift++ {
			r3 := r2
			r3.SetBitPos(base + shift)
			var s string
			var e1, e2 error
			if s, e1 = r3.ReadTU(); e1 == nil {
				// 连带校验：文字后必须紧跟可打印的 tag 串，拒绝垃圾候选
				tag := ""
				if tg, e := r3.ReadTV(256); e == nil {
					tag = tg
				}
				sc := decodedTextScore(s) + decodedTextScore(tag)
				if printableText(s) && sc > bestScore {
					bestScore, bestText, bestEnd = sc, s, r3.TellBits()
				}
			}
			r3 = r2
			r3.SetBitPos(base + shift)
			if s, e2 = r3.ReadTV(256); e2 == nil {
				tag := ""
				if tg, e := r3.ReadTV(256); e == nil {
					tag = tg
				}
				sc := decodedTextScore(s) + decodedTextScore(tag)
				if printableText(s) && sc > bestScore {
					bestScore, bestText, bestEnd = sc, s, r3.TellBits()
				}
			}
		}
		if bestText != "" {
			r2.SetBitPos(bestEnd)
			tag := ""
			if tg, e := r2.ReadTV(256); e == nil {
				tag = tg
			}
			return &entAttrib{
				baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode},
				text:       bestText, tag: tag, insertion: ins, height: height, rotation: rotation,
			}, nil
		}
		return nil, err
	}
	text := t.(*entText)
	extra := text.extra
	if extra == nil {
		extra = map[string]any{}
	}
	// 文本对齐字段由 TEXT 布局解出，随属性实体一并导出
	attrHAlign, attrVAlign, attrGen := text.hAlign, text.vAlign, text.gen
	// AcDbAttribute 标志段（位于文本对齐字段与 tag 之间）：
	// is_locked_in_block RC(R2010+) → mtext_type RC(R2018+)
	var lockedInBlock, mtextType int64
	if ver >= container.VerR2010 {
		if lb, e := r.ReadRC(); e == nil {
			lockedInBlock = int64(lb)
		}
	}
	if ver >= container.VerR2018 {
		if mt, e := r.ReadRC(); e == nil {
			mtextType = int64(mt)
		}
	}
	// tag（及 ATTDEF 的 prompt）按版本选编码：R2007+ TU，否则 TV
	readVerStr := func() string {
		if unicodeText {
			if s, e := r.ReadTU(); e == nil {
				return s
			}
			return ""
		}
		if s, e := r.ReadTV(codepage); e == nil {
			return s
		}
		return ""
	}
	var tag string
	if unicodeText {
		if strs := readStringAreaStrings(r, head, 2); len(strs) > 1 {
			tag = strs[1]
		}
	}
	if tag == "" {
		tag = readVerStr()
	}
	// tag 之后、COMMON_ENTITY_HANDLE_DATA 之前：
	// field_length BS → flags RC → lock_position_flag B(R2007+) →
	// keep_duplicate_records RC(R2010+) → prompt TV(仅 ATTDEF)
	var fieldLength, flags uint16
	var lockPosition, keepDuplicate int64
	var prompt string
	if fl, e := r.ReadBS(); e == nil {
		fieldLength = fl
		if fg, e := r.ReadRC(); e == nil {
			flags = uint16(fg)
			if unicodeText {
				if lp, e := r.ReadB(); e == nil {
					lockPosition = b2int(lp == 1)
					if ver >= container.VerR2010 {
						if kd, e := r.ReadRC(); e == nil {
							keepDuplicate = int64(kd)
							if keepDuplicate > 1 { // VALUEOUTOFBOUNDS：越界置 0（LibreDWG 同）
								keepDuplicate = 0
							}
						}
					}
				}
			}
		}
	}
	if attdef {
		prompt = readVerStr()
	}
	extra["field_length"] = int64(fieldLength)
	extra["flags"] = int64(flags)
	extra["lock_position_flag"] = lockPosition
	extra["is_locked_in_block"] = lockedInBlock
	extra["keep_duplicate_records"] = keepDuplicate
	extra["mtext_type"] = mtextType
	return &entAttrib{
		baseEntity:  text.baseEntity,
		text:        text.text,
		tag:         tag,
		prompt:      prompt,
		insertion:   text.insertion,
		height:      text.height,
		rotation:    text.rotation,
		hAlign:      attrHAlign,
		vAlign:      attrVAlign,
		gen:         attrGen,
		alignPt:     text.alignPt,
		extrusion:   text.extrusion,
		styleHandle: text.styleHandle,
	}, nil
}

// ---- 公共实体头：版本化布局特征与解析 ----

// headFeature 公共实体头的版本化布局特征位。各版本写入端在 objSize 存储、
// 图像尺寸编码、材质/阴影/视觉样式/ds 位、尾部线宽、nolinks 位语义与 EED
// 字符串编码上存在布局差异，一个特征位组合即描述一种候选布局。
type headFeature uint16

const (
	featObjSizeInStream headFeature = 1 << iota // objSize(RL，单位=位) 在流内显式存储（R2000/R2004）
	featPictureRL                               // 图形图像尺寸用 RL（R2000/R2004）；否则 BLL（R2010+）
	featMaterialFlags                           // material flags BB（R2007+）
	featShadowFlags                             // shadow 字节 RC（与 material flags 分离：部分文件只有 BB）
	featVisualStyles                            // 3 个 visual style 位（R2010+）
	featDSBinary                                // ds binary 位（R2013+）
	featLineWeight                              // 尾部 lineweight 字节
	featNolinksBit                              // entmode 之后的独立位为 nolinks（R13~R2002）；否则为 is_xdic_missing
	featUnicodeEED                              // EED code-0 字符串为 UTF-16（R2007+）
)

// headLayoutCandidate 一条候选布局：调试标识 + 特征位组合。
type headLayoutCandidate struct {
	tag   string
	feats headFeature
}

// versionHeadLayouts 各版本的候选布局表（按优先序，评分择优）。布局均经
// 多版本真实样本逐位校准：R2007 主布局 = 流内 objSize + picture RL +
// material BB + shadow RC + lineweight RC；R2000/R2004 无 material，且
// R2000 的独立位是 nolinks；R2010+ 的 objSize 由记录头推导、picture 走
// BLL，R2010 无 ds 位而 R2013/R2018 有；部分源文件缺 shadow/lineweight，
// 以降级候选兜底。
var versionHeadLayouts = map[container.DwgVersion][]headLayoutCandidate{
	container.VerR2007: {
		{"R2007", featObjSizeInStream | featPictureRL | featMaterialFlags | featShadowFlags | featLineWeight | featUnicodeEED},
	},
	container.VerR2000: {
		{"R2000+LW", featObjSizeInStream | featPictureRL | featLineWeight | featNolinksBit},
		{"R2000-noObjSize+LW", featPictureRL | featLineWeight | featNolinksBit},
		{"R2000", featObjSizeInStream | featPictureRL | featNolinksBit},
	},
	container.VerR2004: {
		{"R2004+LW", featObjSizeInStream | featPictureRL | featLineWeight},
		{"R2004-noObjSize+LW", featPictureRL | featLineWeight},
		{"R2004", featObjSizeInStream | featPictureRL},
	},
	container.VerR2010: {
		{"R2010", featMaterialFlags | featShadowFlags | featVisualStyles | featLineWeight | featUnicodeEED},
		{"R2013-like", featMaterialFlags | featVisualStyles | featDSBinary | featUnicodeEED},
	},
}

// headLayoutsFor 取版本的候选布局序列；R2013/R2018 走 R2013 主布局及其
// 降级变体（缺 shadow 或连 lineweight 一并省略的源文件变体）。
func headLayoutsFor(ver container.DwgVersion) []headLayoutCandidate {
	if layouts, ok := versionHeadLayouts[ver]; ok {
		return layouts
	}
	return []headLayoutCandidate{
		{"R2013", featMaterialFlags | featShadowFlags | featVisualStyles | featDSBinary | featLineWeight | featUnicodeEED},
		{"R2013-noShadow-noLW", featMaterialFlags | featVisualStyles | featDSBinary | featUnicodeEED},
		{"R2013-noShadow", featMaterialFlags | featVisualStyles | featDSBinary | featLineWeight | featUnicodeEED},
	}
}

// parseEntityHead 按指定特征组合解析公共实体头。
func parseEntityHead(r *bitstream.BitStream, dataEnd uint64, feats headFeature) (commonEntityHead, error) {
	var head commonEntityHead
	head.objSizeBit = dataEnd
	head.auditBitsize = dataEnd

	if feats&featObjSizeInStream != 0 {
		objSize, err := r.ReadRL()
		if err != nil {
			return head, err
		}
		// R13~R2004 流内 RL objSize 的单位是「位」而非字节
		head.objSizeBit = uint64(objSize)
		head.auditBitsize = head.objSizeBit
	}

	h, err := r.ReadH()
	if err != nil {
		return head, err
	}
	head.handle = h.Value

	// EED 链：BS 长度，0 结束；每项为 H 应用句柄 + length 字节。
	// R2007+ 的 EED 字符串为 UTF-16（featUnicodeEED 标记）
	var eedOK bool
	if head.eed, eedOK = parseEntityEEDChain(r, feats&featUnicodeEED != 0); !eedOK {
		return head, fmt.Errorf("cad: EED 链解析失败")
	}

	// 图形图像（picture）
	picFlag, err := r.ReadB()
	if err != nil {
		return head, err
	}
	head.previewExists = picFlag == 1
	if picFlag == 1 {
		var graphicSize uint64
		if feats&featPictureRL != 0 {
			var v uint32
			if v, err = r.ReadRL(); err != nil {
				return head, err
			}
			graphicSize = uint64(v)
		} else {
			if graphicSize, err = r.ReadBLL(); err != nil {
				return head, err
			}
		}
		if graphicSize > uint64(len(r.Src))*8 {
			return head, fmt.Errorf("cad: 图形图像大小异常 %d", graphicSize)
		}
		if head.preview, err = r.ReadRCS(int(graphicSize)); err != nil {
			return head, err
		}
	}

	trOn := cadTraceHandle != 0 && cadTraceHandle == head.handle
	pos := r.TellBits()
	if head.entityMode, err = r.ReadBB(); err != nil {
		return head, err
	}
	cadTraceFieldInt(trOn, pos, r.TellBits(), "entmode", int64(head.entityMode))
	pos = r.TellBits()
	reactors, err := r.ReadBL()
	if err != nil {
		return head, err
	}
	cadTraceFieldInt(trOn, pos, r.TellBits(), "reactors", int64(reactors))
	if reactors > 1<<20 {
		return head, fmt.Errorf("cad: reactor 数量异常 %d", reactors)
	}
	head.numReactors = reactors
	pos = r.TellBits()
	flag, err := r.ReadB()
	if err != nil {
		return head, err
	}
	if feats&featNolinksBit != 0 {
		// R13~R2000：该位为 nolinks（无 prev/next 链接句柄）
		head.noLinks = flag == 1
		head.prevNextLinks = true
	} else {
		head.xdicMissing = flag == 1
	}
	if trOn {
		cadTraceField(trOn, pos, r.TellBits(), "xdic", fmt.Sprintf("%t", head.xdicMissing))
	}
	if feats&featDSBinary != 0 {
		pos = r.TellBits()
		if flag, err = r.ReadB(); err != nil {
			return head, err
		}
		head.hasDsBinary = flag == 1
		if trOn {
			cadTraceField(trOn, pos, r.TellBits(), "ds", fmt.Sprintf("%t", head.hasDsBinary))
		}
	}

	if err := parseEntityColorHead(r, &head, trOn); err != nil {
		return head, err
	}

	pos = r.TellBits()
	if head.ltypeScale, err = r.ReadBD(); err != nil {
		return head, err
	}
	cadTraceFieldF64(trOn, pos, r.TellBits(), "ltype_scale", head.ltypeScale)
	pos = r.TellBits()
	if head.ltypeFlags, err = r.ReadBB(); err != nil {
		return head, err
	}
	cadTraceFieldInt(trOn, pos, r.TellBits(), "ltype_flags", int64(head.ltypeFlags))
	pos = r.TellBits()
	if head.plotstyleFlgs, err = r.ReadBB(); err != nil {
		return head, err
	}
	cadTraceFieldInt(trOn, pos, r.TellBits(), "plotstyle_flags", int64(head.plotstyleFlgs))
	if feats&featMaterialFlags != 0 {
		pos = r.TellBits()
		if head.materialFlags, err = r.ReadBB(); err != nil {
			return head, err
		}
		cadTraceFieldInt(trOn, pos, r.TellBits(), "material_flags", int64(head.materialFlags))
	}
	if feats&featShadowFlags != 0 {
		pos = r.TellBits()
		if head.shadowFlags, err = r.ReadRC(); err != nil {
			return head, err
		}
		cadTraceFieldInt(trOn, pos, r.TellBits(), "shadow_flags", int64(head.shadowFlags))
	}
	if feats&featVisualStyles != 0 {
		pos = r.TellBits()
		for i := 0; i < 3; i++ {
			vb, e := r.ReadB()
			if e != nil {
				return head, e
			}
			head.visualStyle[i] = vb != 0
		}
		cadTraceField(trOn, pos, r.TellBits(), "visual_styles", "-")
	}
	pos = r.TellBits()
	iv, err := r.ReadBS()
	if err != nil {
		return head, err
	}
	head.invisible = int16(iv)
	cadTraceFieldInt(trOn, pos, r.TellBits(), "invisibility", int64(head.invisible))
	if feats&featLineWeight != 0 {
		pos = r.TellBits()
		lw, e := r.ReadRC()
		if e != nil {
			return head, e
		}
		head.linewt = uint16(lw)
		cadTraceField(trOn, pos, r.TellBits(), "line_weight", "-")
	}
	if trOn {
		fmt.Fprintf(os.Stderr, "[head] END total_head_bits_from_entmode=%d\n", r.TellBits())
	}
	return head, nil
}

// headParsersForVersion 各版本的公共头候选布局解析器（按优先序）。
func headParsersForVersion(ver container.DwgVersion) []headParser {
	if ver == container.VerR13 || ver == container.VerR14 {
		return []headParser{{name: "R14", parse: parseCommonEntityHeadR14}}
	}
	layouts := headLayoutsFor(ver)
	out := make([]headParser, 0, len(layouts))
	for _, lay := range layouts {
		lay := lay
		// R2010+ 布局不在流内存储 objSize，bitsize 由记录头推导（externalSize）
		out = append(out, headParser{name: lay.tag, externalSize: lay.feats&featObjSizeInStream == 0,
			parse: func(r *bitstream.BitStream, dataEnd uint64) (commonEntityHead, error) {
				return parseEntityHead(r, dataEnd, lay.feats)
			}})
	}
	return out
}

// isFinite 浮点有限性判断。
func isFinite(f float64) bool {
	return f == f && f < 1e308 && f > -1e308
}

// decodeEntityFieldsVer 按版本解码实体：在类型码后的位窗口内扫描
// 「位偏移 × 版本布局」候选，公共头 + 图元几何联合评分取最优。
func decodeEntityFieldsVer(r *bitstream.BitStream, h objrec.ObjHeader, objHandle uint64, recSize uint32, typeName string, typeCode uint16, ver container.DwgVersion, codepage uint16, dynamicTypes map[uint16]string, lightingUnits string) (any, error) {
	// R13/R14 的 BT（位厚度）无 mode 前缀位（LibreDWG bit_read_BT 版本分支），
	// 随位读取器传入各图元解码器
	r.LegacyBT = ver == container.VerR13 || ver == container.VerR14
	dataEnd := h.Rec.DataEndBit()
	startByte, startBit := r.Cursor()
	base := uint64(startByte)*8 + uint64(startBit)
	parsers := headParsersForVersion(ver)
	ent, _, err := scanEntityBest(r, base, dataEnd, hdlSizeFieldBits(h), parsers, objHandle, recSize, typeName, typeCode, func(r *bitstream.BitStream, head *commonEntityHead) (any, error) {
		return decodeEntityByTypeVer(r, head, h, objHandle, ver, codepage, dynamicTypes, lightingUnits)
	})
	attachEntityRecordMeta(ent, h.Rec)
	return ent, err
}

// decodeBlockLike 解析 BLOCK/ENDBLK/SEQEND 标记实体（dwg.spec
// DWG_ENTITY 三分支）：公共头后 BLOCK 读块名（R2007+ 为 TU，其余 TV 按
// 文档码页，fzw 中文块名实证），ENDBLK/SEQEND 无附加字段；尾部 handle
// 流由 decodeOwnerLayer 读取。
func decodeBlockLike(r *bitstream.BitStream, head *commonEntityHead, typeName string, ver container.DwgVersion, codepage uint16) (any, error) {
	e := &entBlockLike{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}}
	if typeName == "BLOCK" {
		var err error
		if ver >= container.VerR2007 {
			if e.name, err = r.ReadTU(); err != nil {
				return nil, err
			}
		} else {
			if e.name, err = r.ReadTV(codepage); err != nil {
				return nil, err
			}
		}
	}
	e.owner, e.layer = decodeOwnerLayer(r, head)
	return e, nil
}

// decodeEntityByTypeVer 按版本与类型分发图元解码。
func decodeEntityByTypeVer(r *bitstream.BitStream, head *commonEntityHead, h objrec.ObjHeader, objHandle uint64, ver container.DwgVersion, codepage uint16, dynamicTypes map[uint16]string, lightingUnits string) (any, error) {
	name := objrec.EntityTypeName(h.TypeCode, dynamicTypes)
	unicodeText := ver >= container.VerR2007 // R2007+ 文本为 UTF-16（LibreDWG FIELD_T 版本分支）
	switch name {
	case "BLOCK", "ENDBLK", "SEQEND":
		return decodeBlockLike(r, head, name, ver, codepage)
	case "LINE":
		if ver == container.VerR13 || ver == container.VerR14 {
			return decodeLineR14(r, head)
		}
		return decodeLine(r, head)
	case "CIRCLE":
		return decodeCircle(r, head)
	case "ARC":
		return decodeArcTolerant(r, head)
	case "POINT":
		return decodePoint(r, head)
	case "ELLIPSE":
		return decodeEllipse(r, head)
	case "LWPOLYLINE":
		// 版本枚举非时间序（verR2000=0、verR14=1），不能范围比较：
		// 仅 R13/R14 走 2RD 顶点，R2000+（含 R2004+）走 2DD 差分
		return decodeLwPolyline(r, head, codepage, ver != container.VerR13 && ver != container.VerR14)
	case "VIEWPORT":
		return decodeViewport(r, head)
	case "SHAPE":
		return decodeShape(r, head)
	case "POLYLINE_MESH":
		return decodePolylineMesh(r, head, ver >= container.VerR2004)
	case "REGION":
		return decodeAcis(r, head, "REGION", ver)
	case "3DSOLID":
		return decodeAcis(r, head, "3DSOLID", ver)
	case "BODY":
		return decodeAcis(r, head, "BODY", ver)
	case "TEXT":
		// R2007+ 首选字符串区路径（text_value 在对象尾部字符串流，主体
		// 位流的 T 位点不消耗位；LibreDWG FIELD_T 对 TEXT 恒走 str_dat，
		// chuandongzhou 75 处 R2007 与 uhengshenhua R2018 TEXT 实证），
		// 流不可读时回退 R21 候选评分路径。
		if unicodeText {
			entryByte, entryBit := r.Cursor()
			if t, ok := decodeTextUnicode(r, head, codepage); ok {
				return t, nil
			}
			r.Restore(entryByte, entryBit)
		}
		return decodeTextVer(r, head, codepage, unicodeText)
	case "MTEXT":
		// R2004 无背景数据/rect_height；R2007+ 才有（dwg.spec MTEXT 版本分支同参数）
		return decodeMTextVer(r, head, codepage, unicodeText, ver >= container.VerR2007, ver)
	case "SPLINE":
		return decodeSpline(r, head, ver >= container.VerR2013)
	case "HATCH":
		return decodeHatch(r, head, ver, codepage)
	case "IMAGE":
		return decodeImageVer(r, head, ver)
	case "HELIX":
		return decodeHelixVer(r, head, ver)
	case "PDFUNDERLAY", "DGNUNDERLAY", "DWFUNDERLAY":
		return decodeUnderlayVer(r, head)
	case "MPOLYGON":
		return decodeMpolygonVer(r, head, ver, codepage)
	case "OLE2FRAME":
		return decodeOle2FrameVer(r, head, ver)
	case "OLEFRAME":
		return decodeOleFrameVer(r, head, ver)
	case "PROXY_ENTITY":
		return decodeProxyEntityVer(r, head, h.Rec.DataEndBit(), ver)
	case "TOLERANCE":
		return decodeTolerance(r, head)
	case "POLYLINE_PFACE":
		return decodePolylinePface(r, head, ver >= container.VerR2004)
	case "RAY":
		return decodeRay(r, head, false)
	case "XLINE":
		return decodeRay(r, head, true)
	case "3DFACE":
		return decodeFace3dVer(r, head, ver == container.VerR13 || ver == container.VerR14)
	case "SOLID":
		return decodeSolidTolerant(r, head, false)
	case "TRACE":
		return decodeSolidTolerant(r, head, true)
	case "LEADER":
		return decodeLeader(r, head, ver)
	case "MLINE":
		return decodeMline(r, head)
	case "VERTEX_2D":
		return decodeVertex2d(r, head, ver >= container.VerR2010)
	case "VERTEX_3D":
		return decodeVertex3d(r, head)
	case "VERTEX_MESH", "VERTEX_PFACE":
		return decodeVertexPface(r, head)
	case "VERTEX_PFACE_FACE":
		return decodeVertexPfaceFace(r, head)
	case "POLYLINE_2D":
		return decodePolyline2d(r, head, ver >= container.VerR2004)
	case "POLYLINE_3D":
		return decodePolyline3d(r, head, ver >= container.VerR2004)
	case "DIM_LINEAR":
		return decodeDimension(r, head, ver, dimLayoutLinear)
	case "DIM_ALIGNED":
		return decodeDimension(r, head, ver, dimLayoutAligned)
	case "DIM_ANG3PT":
		return decodeDimension(r, head, ver, dimLayoutAng3Pt)
	case "DIM_ANG2LN":
		return decodeDimension(r, head, ver, dimLayoutAng2Ln)
	case "DIM_ORDINATE":
		return decodeDimension(r, head, ver, dimLayoutOrdinate)
	case "DIM_RADIUS":
		return decodeDimension(r, head, ver, dimLayoutRadius)
	case "DIM_DIAMETER":
		return decodeDimension(r, head, ver, dimLayoutDiameter)
	case "ARC_DIMENSION":
		return decodeDimension(r, head, ver, dimLayoutArc)
	case "LARGE_RADIAL_DIMENSION":
		return decodeDimension(r, head, ver, dimLayoutLargeRadial)
	case "INSERT", "MINSERT":
		if cadTraceHandle != 0 && cadTraceHandle == head.handle {
			fmt.Fprintf(os.Stderr, "[ins] ver=%d r13r14=%v handle=%d\n", ver, ver == container.VerR13 || ver == container.VerR14, head.handle)
		}
		return decodeInsert(r, head, ver)
	case "ATTRIB":
		return decodeAttribVer(r, head, codepage, unicodeText, ver, false)
	case "ATTDEF":
		return decodeAttribVer(r, head, codepage, unicodeText, ver, true)
	case "LIGHT":
		return decodeLight(r, head, ver, codepage, lightingUnits == "2")
	case "MULTILEADER":
		return decodeMLeader(r, head, ver, codepage)
	default:
		// UNKNOWN_ENT 兜底（dwg2.spec UNKNOWN_ENT = HANDLE_UNKNOWN_BITS）：
		// 仅对 LibreDWG 默认构建同样兜底为 UNKNOWN_ENT 的动态类生效
		// （如 type=528 ACAD_TABLE），主体归入 unknown_bits 位串；
		// 其余未建模类（LIGHT/MULTILEADER 等有正式 spec 布局）保持报错，
		// 避免以兜底产物污染审计分母，待专项实现接入
		if unknownEntFallbackNames[name] {
			return decodeUnknownEnt(r, head, name)
		}
		return nil, fmt.Errorf("cad: 不支持的实体类型 %s (0x%X)", name, h.TypeCode)
	}
}

// near 坐标近似比较（容差 0.01）。
func near(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 0.01
}

// printableText 文本无控制字符/替换符（拒绝错位读取的垃圾位型）。
func printableText(s string) bool {
	if s == "" {
		return false
	}
	for _, ch := range s {
		if ch == '\uFFFD' || (unicode.IsControl(ch) && ch != '\n' && ch != '\r' && ch != '\t') {
			return false
		}
	}
	return true
}
