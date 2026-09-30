// bitreader.go 提供 DWG 位流的读取原语。
// DWG 对象数据按位打包而非字节对齐；本文件实现规范定义的全部位编码
// （B/BB/BS/BL/BD/DD/H 等，与 AutoCAD DWG 位编码规范术语一一对应），
// 是容器解包之上所有实体/对象解码的底层依赖。
//
// 主要模块划分：游标定位（cursor/restore/setBitPos/alignByte）、原始位与
// 字节读取（readB~readRL/readRD）、压缩编码读取（readBD/readDD/readBS/
// readBL/readBLL/readMS/readUMC/readOT）、句柄与文本（readH/readTV/readTU）、
// 码页转换（decodeCodepage）。
package cad

import (
	"errors"
	"fmt"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// errUnexpectedEOF 位流提前结束（无法在当前对齐状态下继续读取）。
var errUnexpectedEOF = errors.New("cad: 位流意外结束")

// handleRef DWG 句柄引用：code 为引用语义（软/硬/相对），counter 为编码字节数，
// value 为句柄数值本体。
type handleRef struct {
	code    uint8
	counter uint8
	value   uint64
}

// bitStream 位流读取游标：自 MSB 起逐位消费；pos 为当前字节下标，sub 为该
// 字节内已消费的高位偏移（0..7）。字节对齐读取（sub==0）在各原语内走免拼合
// 快速通道，是全包解析吞吐的关键路径。
type bitStream struct {
	src []byte
	pos int
	sub uint8
	// legacyBT 标记 R13/R14 位流：BT（位厚度）在该版本前无 1 位 mode 前缀，
	// 直接为 BD（LibreDWG bit_read_BT 的 from_version 分支）。
	legacyBT bool
	// textLimit 文本长度字段的安全上限（1 Mi 字符），拦截脏数据导致的超大分配。
	textLimit int
}

// newBitStream 构造定位到流首的位读取游标。
func newBitStream(data []byte) *bitStream {
	return &bitStream{src: data, textLimit: 1 << 20}
}

// tellBits 当前绝对位偏移。
func (r *bitStream) tellBits() uint64 {
	return uint64(r.pos)*8 + uint64(r.sub)
}

// totalBits 缓冲区总位数。
func (r *bitStream) totalBits() uint64 {
	return uint64(len(r.src)) * 8
}

// cursor 返回 (字节下标, 字节内位偏移) 快照，可交给 restore 恢复。
func (r *bitStream) cursor() (int, uint8) {
	return r.pos, r.sub
}

// restore 恢复到 cursor 快照位置；位偏移越界值按 7 截断。
func (r *bitStream) restore(byteIdx int, bitIdx uint8) {
	if bitIdx > 7 {
		bitIdx = 7
	}
	r.pos, r.sub = byteIdx, bitIdx
}

// setBitPos 直接以绝对位偏移定位。
func (r *bitStream) setBitPos(bitPos uint64) {
	r.pos = int(bitPos / 8)
	r.sub = uint8(bitPos % 8)
}

// alignByte 丢弃当前字节剩余位，对齐到下一字节边界。
func (r *bitStream) alignByte() {
	if r.sub != 0 {
		r.sub = 0
		r.pos++
	}
}

// readB 读取单个原始位（raw bit）。
func (r *bitStream) readB() (uint8, error) {
	if r.pos >= len(r.src) {
		return 0, errUnexpectedEOF
	}
	v := (r.src[r.pos] & (0x80 >> r.sub)) >> (7 - r.sub)
	if r.sub == 7 {
		r.sub = 0
		r.pos++
	} else {
		r.sub++
	}
	return v, nil
}

// readBB 读取 2 位。
func (r *bitStream) readBB() (uint8, error) {
	v, err := r.readBitsMsb(2)
	return uint8(v), err
}

// read3B 读取 3 位。
func (r *bitStream) read3B() (uint8, error) {
	v, err := r.readBitsMsb(3)
	return uint8(v), err
}

// readBitsMsb 按大端位序读取 n（≤64）位组装为整数。
// 实现分三段推进：当前字节内的剩余高位 → 中间整字节 → 末字节剩余低位，
// 位值组装语义与逐位实现一致；逐位推进是全部上层解码的基础原语。
func (r *bitStream) readBitsMsb(n uint8) (uint64, error) {
	if n > 64 {
		return 0, fmt.Errorf("cad: 位读取宽度超限 %d", n)
	}
	var value uint64
	remaining := int(n)
	// 头段：先消费当前字节 sub 位之后的可用高位（不足 n 位时取部分）
	if r.sub != 0 && remaining > 0 {
		if r.pos >= len(r.src) {
			return 0, errUnexpectedEOF
		}
		avail := 8 - int(r.sub)
		take := avail
		if remaining < take {
			take = remaining
		}
		seg := uint64(r.src[r.pos]>>uint(avail-take)) & uint64((1<<take)-1)
		value = value<<uint(take) | seg
		remaining -= take
		r.sub += uint8(take)
		if r.sub == 8 {
			r.sub = 0
			r.pos++
		}
	}
	// 中段：字节对齐后的整字节直取
	for remaining >= 8 {
		if r.pos >= len(r.src) {
			return 0, errUnexpectedEOF
		}
		value = value<<8 | uint64(r.src[r.pos])
		r.pos++
		remaining -= 8
	}
	// 尾段：不足一字节的剩余位逐位提取
	for remaining > 0 {
		if r.pos >= len(r.src) {
			return 0, errUnexpectedEOF
		}
		value = value<<1 | uint64(r.src[r.pos]>>uint(7-r.sub))&1
		if r.sub == 7 {
			r.sub = 0
			r.pos++
		} else {
			r.sub++
		}
		remaining--
	}
	return value, nil
}

// readRC 读取原始字节（8 位，允许跨字节边界）。
// 字节对齐时走单字节直取快速通道，免跨字节拼合。
func (r *bitStream) readRC() (uint8, error) {
	if r.sub == 0 {
		if r.pos >= len(r.src) {
			return 0, errUnexpectedEOF
		}
		b := r.src[r.pos]
		r.pos++
		return b, nil
	}
	if r.pos >= len(r.src) {
		return 0, errUnexpectedEOF
	}
	joined := uint16(r.src[r.pos]) << r.sub
	if r.pos+1 < len(r.src) {
		joined |= uint16(r.src[r.pos+1]) >> (8 - r.sub)
	}
	r.pos++
	return uint8(joined & 0xFF), nil
}

// collectRaw 从流内连续取 n 个原始字节：对齐时直接在底层缓冲上切片返回
// （零拷贝，只读语义——对象数据段缓冲区被全部记录共享，禁止原地修改），
// 非对齐时逐字节拼合。
func (r *bitStream) collectRaw(n int) ([]byte, error) {
	if r.sub == 0 {
		if r.pos+n > len(r.src) {
			return nil, errUnexpectedEOF
		}
		out := r.src[r.pos : r.pos+n]
		r.pos += n
		return out, nil
	}
	out := make([]byte, n)
	for i := range out {
		b, err := r.readRC()
		if err != nil {
			return nil, err
		}
		out[i] = b
	}
	return out, nil
}

// readRCS 读取 count 个原始字节。
func (r *bitStream) readRCS(count int) ([]byte, error) {
	if count == 0 {
		return nil, nil
	}
	return r.collectRaw(count)
}

// readRS 读取 16 位字（小端：低字节在前）。
func (r *bitStream) readRS() (uint16, error) {
	lo, err := r.readRC()
	if err != nil {
		return 0, err
	}
	hi, err := r.readRC()
	if err != nil {
		return 0, err
	}
	return uint16(hi)<<8 | uint16(lo), nil
}

// readRL 读取 32 位长字（小端）。
func (r *bitStream) readRL() (uint32, error) {
	low, err := r.readRS()
	if err != nil {
		return 0, err
	}
	high, err := r.readRS()
	if err != nil {
		return 0, err
	}
	return uint32(high)<<16 | uint32(low), nil
}

// read2RD 连读两个原始双精度（2RD 向量的高频组合）。
func (r *bitStream) read2RD() (x, y float64, err error) {
	if x, err = r.readRD(); err != nil {
		return 0, 0, err
	}
	y, err = r.readRD()
	return x, y, err
}

// readRD 读取 64 位 IEEE 双精度浮点（小端字节序）。
// 字节对齐时一次拷贝 8 字节，坐标读取是解析最高频操作之一。
func (r *bitStream) readRD() (float64, error) {
	raw, err := r.collectRaw(8)
	if err != nil {
		return 0, err
	}
	var buf [8]byte
	copy(buf[:], raw)
	return leF64(buf), nil
}

// readBD 位压缩双精度：2 位前缀区分 全量/1.0/0.0 三种形态。
func (r *bitStream) readBD() (float64, error) {
	prefix, err := r.readBB()
	if err != nil {
		return 0, err
	}
	switch prefix {
	case 0:
		return r.readRD()
	case 1:
		return 1.0, nil
	case 2:
		return 0.0, nil
	default:
		// BB=11 非法码（LibreDWG 同样报错并按 NaN/0 处理）
		return 0.0, nil
	}
}

// read3BD 读取 3D 坐标点（依次 x/y/z 三个 BD）。
func (r *bitStream) read3BD() (x, y, z float64, err error) {
	if x, err = r.readBD(); err != nil {
		return
	}
	if y, err = r.readBD(); err != nil {
		return
	}
	z, err = r.readBD()
	return
}

// readDD 读取与默认值相关的差分双精度：2 位前缀指示需从流内补齐的
// 字节数（0~3），补齐后与默认值的 IEEE 字节序列合成完整浮点。
func (r *bitStream) readDD(def float64) (float64, error) {
	prefix, err := r.readBB()
	if err != nil {
		return 0, err
	}
	switch prefix {
	case 0:
		return def, nil
	case 3:
		return r.readRD()
	}
	data := f64LeBytes(def)
	switch prefix {
	case 1:
		// 差分编码 1：顺序覆盖低 4 字节（尾数段）
		for i := 0; i < 4; i++ {
			b, e := r.readRC()
			if e != nil {
				return 0, e
			}
			data[i] = b
		}
	case 2:
		// 差分编码 2：仅更新 6 字节——先补指数段低 2 字节（data[4..6)），
		// 再补尾数低 4 字节（data[0..4)）；data[6..8) 保留默认值
		// （符号与指数高位不变），补字节顺序与 LibreDWG bit_read_DD 一致。
		for i := 4; i < 6; i++ {
			b, e := r.readRC()
			if e != nil {
				return 0, e
			}
			data[i] = b
		}
		for i := 0; i < 4; i++ {
			b, e := r.readRC()
			if e != nil {
				return 0, e
			}
			data[i] = b
		}
	}
	return leF64(data), nil
}

// readBT 位厚度：R2000+ 为 1 位 mode（1 → 0.0，否则 BD）；
// R13/R14 无 mode 位、直接读 BD（LibreDWG bit_read_BT 同款版本分支）。
func (r *bitStream) readBT() (float64, error) {
	if r.legacyBT {
		return r.readBD()
	}
	mode, err := r.readB()
	if err != nil {
		return 0, err
	}
	if mode == 1 {
		return 0.0, nil
	}
	return r.readBD()
}

// readBE 位挤出方向（对齐 LibreDWG bit_read_BE 的版本分支）：
// R2000+ 为 1 位 flag（1 → (0,0,1)，否则 3BD）；R13/R14 无 flag 位、
// 直接读 3×BD。
func (r *bitStream) readBE() (x, y, z float64, err error) {
	if r.legacyBT {
		return r.read3BD()
	}
	flag, err := r.readB()
	if err != nil {
		return
	}
	if flag == 1 {
		return 0, 0, 1, nil
	}
	return r.read3BD()
}

// readBS 位压缩短整型：2 位前缀区分 RS/RC/0/256 四种形态。
func (r *bitStream) readBS() (uint16, error) {
	prefix, err := r.readBB()
	if err != nil {
		return 0, err
	}
	switch prefix {
	case 0:
		return r.readRS()
	case 1:
		b, e := r.readRC()
		return uint16(b), e
	case 3:
		return 256, nil
	default:
		return 0, nil
	}
}

// readBL 位压缩长整型：2 位前缀区分 RL/RC/0/256（BB11 → 256，
// LibreDWG bit_read_BL：AutoCAD 以 BL=256 表示 ByLayer 颜色占位，
// 如 TABLESTYLE border 颜色）。
func (r *bitStream) readBL() (uint32, error) {
	prefix, err := r.readBB()
	if err != nil {
		return 0, err
	}
	switch prefix {
	case 0:
		return r.readRL()
	case 1:
		b, e := r.readRC()
		return uint32(b), e
	case 3:
		return 256, nil
	default:
		return 0, nil
	}
}

// readBLL 位压缩超长整型（对齐 LibreDWG bit_read_BLL）：3 位长度码，
// 码值 1/2/4 直接走 RC/RS/RL 小端原语，其余码值按对应字节数小端组装。
func (r *bitStream) readBLL() (uint64, error) {
	length, err := r.read3B()
	if err != nil {
		return 0, err
	}
	switch length {
	case 1:
		b, e := r.readRC()
		if e != nil {
			return 0, e
		}
		return uint64(b), nil
	case 2:
		v, e := r.readRS()
		if e != nil {
			return 0, e
		}
		return uint64(v), nil
	case 4:
		v, e := r.readRL()
		if e != nil {
			return 0, e
		}
		return uint64(v), nil
	}
	var value uint64
	for i := uint8(0); i < length; i++ {
		b, e := r.readRC()
		if e != nil {
			return 0, e
		}
		value |= uint64(b) << (8 * i)
	}
	return value, nil
}

// readMS 模块化短整型（对象记录 size 字段）：每个 15 位组以最高位作续组
// 标志，至多两组；恰好多数记录为单组，循环展开为两次定长尝试。
func (r *bitStream) readMS() (uint32, error) {
	first, err := r.readRS()
	if err != nil {
		return 0, err
	}
	value := uint32(first & 0x7FFF)
	if first&0x8000 == 0 {
		return value, nil
	}
	second, err := r.readRS()
	if err != nil {
		return 0, err
	}
	return value | uint32(second&0x7FFF)<<15, nil
}

// readUMC 无符号模块化字符（R2010+ handle-stream-size）：7 位一组小端
// 累加，最高位为续组标志，至多 5 组（35 位覆盖 uint32）。
func (r *bitStream) readUMC() (uint32, error) {
	var value uint32
	for shift := uint(0); ; shift += 7 {
		b, err := r.readRC()
		if err != nil {
			return 0, err
		}
		value |= uint32(b&0x7F) << shift
		if b&0x80 == 0 {
			return value, nil
		}
		if shift >= 28 {
			return value, nil
		}
	}
}

// readOT R2010+ 对象类型码：BB opcode 0→RC，1→RC+0x1F0，2/3→RS。
func (r *bitStream) readOT() (uint16, error) {
	opcode, err := r.readBB()
	if err != nil {
		return 0, err
	}
	switch opcode {
	case 0:
		b, e := r.readRC()
		return uint16(b), e
	case 1:
		b, e := r.readRC()
		return uint16(b) + 0x01F0, e
	default:
		return r.readRS()
	}
}

// readH 句柄：高 4 位为 code，低 4 位为编码字节数 counter，随后 counter
// 字节按大端组装为数值。
func (r *bitStream) readH() (handleRef, error) {
	b, err := r.readRC()
	if err != nil {
		return handleRef{}, err
	}
	ref := handleRef{code: b >> 4, counter: b & 0x0F}
	if ref.counter > 4 {
		return handleRef{}, fmt.Errorf("cad: 非法句柄字节数 %d", ref.counter)
	}
	for i := uint8(0); i < ref.counter; i++ {
		nb, e := r.readRC()
		if e != nil {
			return handleRef{}, e
		}
		ref.value = ref.value<<8 | uint64(nb)
	}
	return ref, nil
}

// stripNUL 过滤字节序列中的内嵌 0 字节（DWG 文本字段的历史填充习惯）。
func stripNUL(raw []byte) []byte {
	out := make([]byte, 0, len(raw))
	for _, b := range raw {
		if b != 0 {
			out = append(out, b)
		}
	}
	return out
}

// readTV 文本（codepage 编码，如 GBK）：BS 长度 + 字节流，过滤内嵌 0 字节
// 后按码页解码。
func (r *bitStream) readTV(codepage uint16) (string, error) {
	length, err := r.readBS()
	if err != nil {
		return "", err
	}
	if int(length) > r.textLimit {
		return "", fmt.Errorf("cad: TV 文本长度异常 %d", length)
	}
	raw, err := r.collectRaw(int(length))
	if err != nil {
		return "", err
	}
	return decodeCodepage(stripNUL(raw), codepage), nil
}

// readTU 文本（UTF-16LE）：BS 长度 + 长度个 16 位单元，拼合后按码点解码。
func (r *bitStream) readTU() (string, error) {
	length, err := r.readBS()
	if err != nil {
		return "", err
	}
	if int(length) > r.textLimit {
		return "", fmt.Errorf("cad: TU 文本长度异常 %d", length)
	}
	raw, err := r.collectRaw(int(length) * 2)
	if err != nil {
		return "", err
	}
	units := make([]uint16, length)
	for i := range units {
		units[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	return string(utf16Decode(units)), nil
}

// readCRC 对齐字节后读取 16 位 CRC。
func (r *bitStream) readCRC() (uint16, error) {
	if r.sub > 0 {
		r.restore(r.pos+1, 0)
	}
	return r.readRS()
}

// readBitsBytes 从当前位位置读取 n 字节（支持非字节对齐）。
func (r *bitStream) readBitsBytes(n int) ([]byte, error) {
	out := make([]byte, n)
	for i := range out {
		v, err := r.readRC()
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// decodeCodepage 按 DWG codepage 编号把字节串转为 Go 字符串：
// 0/1 为 ASCII 直通；30/2/11/12 为 windows-1252 家族（Latin-1 近似，
// 可见字符区一致）；31/39 为 ANSI_936/GBK（中文图纸最常见）；
// 未登记码页或转码失败按原始字节返回。
func decodeCodepage(b []byte, codepage uint16) string {
	if len(b) == 0 {
		return ""
	}
	switch codepage {
	case 0, 1:
		return string(b)
	case 30, 2, 11, 12:
		if out, err := charmap.ISO8859_1.NewDecoder().Bytes(b); err == nil {
			return string(out)
		}
	case 31, 39:
		if out, err := simplifiedchinese.GBK.NewDecoder().Bytes(b); err == nil {
			return string(out)
		}
	}
	return string(b)
}
