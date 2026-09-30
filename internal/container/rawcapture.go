// rawcapture.go DWG 容器原始素材捕获：解析路径从源文件保留的整段/整块
// 原始字节（R2000 段目录与对象区、R2004 加密头/页表/段表、R2007 第二头
// 与页表/段表），供写出端（internal/writer）做文件级回放。捕获为尽力
// 语义：单个结构越界只跳过该部分，不阻断解析。本文件自 encode_file.go
// 上提至 container 层，供解析编排（drawing）填充、写出层消费，避免
// drawing↔writer 循环依赖。
package container

import (
	"encoding/binary"
	"fmt"

	"github.com/unitedrhino/go-cad/internal/objrec"
)

// r2000RawData R2000 家族容器的原始写出素材：解析时从源文件保留的原样字节，
// 供 WriteDwgR2000 做文件级回放。仅在 R2000/R13/R14 解析路径填充。
type R2000RawData struct {
	// header 文件头 [0x00,0x15) 原始字节：版本串（AC1012/AC1014/AC1015）+
	// 未知区 + 码页。写出端原样回放，保证 detectVersion/readCodepage 一致。
	Header []byte
	// order 段号按源目录出现顺序（写出目录条目时保持同序）。
	Order []uint8
	// sections 段号 → 段原始字节（含段内哨兵/CRC，仅收录源目录中 size>0 的段）。
	Sections map[uint8][]byte
	// refs 对象图条目（handle 与源文件内绝对偏移），重建对象图流用。
	Refs []objrec.ObjectRef
	// objBase 对象区源基址（refs 中最小记录偏移）。
	ObjBase uint32
	// objBlob 对象区原始字节 [objBase, 最大记录尾)，含 MS 头与尾部 CRC，
	// 整块回放到新布局后基址平移，对象图差分随之重编。
	ObjBlob []byte
}

// captureR2000Raw 从源文件与对象图条目提取回放素材（解析路径的一次性钩子）：
// 尽力保留，任一段越界只跳过该段不阻断解析。
func CaptureR2000Raw(Data []byte, Refs []objrec.ObjectRef) *R2000RawData {
	raw := &R2000RawData{Refs: Refs, Sections: make(map[uint8][]byte)}
	if len(Data) >= 0x15 {
		raw.Header = append([]byte(nil), Data[:0x15]...)
	}
	locs, err := ParseR2000Directory(Data)
	if err == nil {
		for _, loc := range locs {
			raw.Order = append(raw.Order, loc.RecordNo)
			if loc.Size == 0 {
				continue
			}
			start, end := int(loc.Offset), int(loc.Offset)+int(loc.Size)
			if start > len(Data) || end > len(Data) || start > end {
				continue
			}
			raw.Sections[loc.RecordNo] = append([]byte(nil), Data[start:end]...)
		}
	}
	// 对象区跨度：覆盖对象图引用的全部记录（MS 头 + body + 2 字节尾部 CRC）。
	// 源文件中记录连续铺放，取 [最小偏移, 最大记录尾) 整块保留即可完整回放。
	minOff, maxEnd := ^uint64(0), uint64(0)
	for _, ref := range Refs {
		end, ok := r2000RecordEnd(Data, ref.Offset)
		if !ok {
			continue
		}
		if uint64(ref.Offset) < minOff {
			minOff = uint64(ref.Offset)
		}
		if end > maxEnd {
			maxEnd = end
		}
	}
	if maxEnd > 0 && minOff < maxEnd && maxEnd <= uint64(len(Data)) {
		raw.ObjBase = uint32(minOff)
		raw.ObjBlob = append([]byte(nil), Data[minOff:maxEnd]...)
	}
	return raw
}

// r2000RecordEnd 计算对象记录在文件中的结束偏移，与 parseObjectRecord 的
// 读取语义精确对齐：MS 大小字段（2 或 4 字节，RS 字高位为续传标志）+
// size 字节 body + 2 字节尾部 CRC。
func r2000RecordEnd(Data []byte, off uint32) (uint64, bool) {
	if uint64(off)+2 > uint64(len(Data)) {
		return 0, false
	}
	w0 := binary.LittleEndian.Uint16(Data[off:])
	msBytes := 2
	w1 := uint16(0)
	if w0&0x8000 != 0 {
		msBytes = 4
		if uint64(off)+4 > uint64(len(Data)) {
			return 0, false
		}
		w1 = binary.LittleEndian.Uint16(Data[off+2:])
	}
	Size := uint32(w0&0x7FFF) | uint32(w1&0x7FFF)<<15
	end := uint64(off) + uint64(msBytes) + uint64(Size) + 2
	if end > uint64(len(Data)) {
		return 0, false
	}
	return end, true
}

// ---- R2004 (AC1018~AC1032) 容器：加密头 + 页表 + 段表 + LZ77 压缩页 ----
//
// 写出与 container.go 读侧逐点对称（以读侧校验点为准）：
//   - 0x80 起 0x78 字节 LCG XOR 加密头（读侧 decryptR2004Header 只用其中
//     0x54/0x5C 两字段，写出端同步维护 CRC32 及全部定位字段）；
//   - 页表（page map）：签名 0x41630E3B 的 system section，内容为
//     (id i32, size u32) 序列，页地址从 0x100 起按 size 累加；
//   - 段表（Section Info）：签名 0x4163003B 的 system section，内容原样
//     回放源文件解压字节（页 id/页尺寸/页内偏移均不随重压缩变化）；
//   - 数据页：32 字节页头（XOR 0x4164536B^页地址 加密）+ 压缩载荷。

// r2004 section 系统段头长度（读侧 inflateSystemSection 的 5×u32）与页头长度。
const (
	R2004SystemHdrSize = 0x14 // 签名+解压尺寸+压缩尺寸+压缩类型+校验和
	R2004PageHdrSize   = 32   // 数据页加密页头
	R2004HdrPlainSize  = 0x78 // 加密头覆盖的明文长度（0x80~0xF8）
	// r2004HeaderPageSize 文件头明文区长度（加密头之前的部分）。
	r2004HeaderPageSize = 0x100
)

// r2004PageRef 数据段的页事实：
//   - startOffset 取自源数据页头（start_offset=页在段内的目标偏移，压缩段
//     按 maxDecomp 步进铺放）；
//   - dataOff/dataLen 为本页内容在读侧拼接视图（assembleSection 逐页补齐
//     maxDecomp 后顺序拼接、按段 size 截断）中的切片位置。
type R2004PageRef struct {
	Id          uint32
	StartOffset uint64
	DataOff     int
	DataLen     int
}

// r2004SectionData 一个数据段的回放素材：源段表描述字段 + 按源分页三元组
// 切出的解压后完整数据（即对象记录等业务字节，写出处整段重压缩）。
type R2004SectionData struct {
	Name       string
	SecType    uint32 // 段类型码（写回数据页头 section_type 字段）
	Size       uint64 // 段有效字节数（末页可短于 maxDecomp）
	maxDecomp  uint32 // 单页解压尺寸上限
	Compressed uint32 // 1=不压缩 2=LZ77（与读侧 secEntry.compressed 一致）
	encrypted  uint32 // 0=明文（加密段无法回放，捕获时放弃）
	Pages      []R2004PageRef
	Data       []byte
}

// r2004RawData R2004 家族容器的原始写出素材：解析时从源文件保留的头部
// 字节、页表页顺序、段表解压字节与各段解压数据，供 WriteDwgR2004 做文件级
// 回放。仅在 R2004/R2010/R2013/R2018 解析路径填充（R2007 容器独立）。
type R2004RawData struct {
	// prefix 文件头明文区 [0x00,0x100) 原始字节：版本串、缩略图/摘要段
	// 地址等（写出端仅回填 0x0D/0x20/0x24 三处段地址）。
	Prefix [r2004HeaderPageSize]byte
	// hdrPlain 解密后的 r2004 加密头明文 [0x80,0xF8)（写出端回填定位字段
	// 并重算 CRC32 后重新加密）。
	HdrPlain [R2004HdrPlainSize]byte
	// pageOrder 页表页 id 按源顺序（已剔除 gap 与页表自身语义无关，仅用于
	// 保持物理布局顺序；页表/段表两页的尺寸在写出时重算）。
	PageOrder []int32
	// sysmapID/infoID 页表自身与段表所在页的页 id（写回加密头 0x50/0x5C）。
	SysmapID int32
	InfoID   int32
	// infoBlob 段表（Section Info）解压字节，原样回放。
	InfoBlob []byte
	// sections 各数据段素材（段表描述顺序）。
	Sections []R2004SectionData
}

// captureR2004Raw 从 R2004+ 源文件提取回放素材（解析路径的一次性钩子）：
// 走与读侧完全相同的解析链（decryptR2004Header→parsePageMap→段表→assembleSection），
// 任一环节失败返回 nil（不阻断解析，仅失去文件级写出能力）。
func CaptureR2004Raw(Data []byte) *R2004RawData {
	if len(Data) < r2004HeaderPageSize || (len(Data) >= 6 && string(Data[:6]) == "AC1021") {
		// R2007 容器结构独立，不在此捕获
		return nil
	}
	raw := &R2004RawData{}
	copy(raw.Prefix[:], Data[:r2004HeaderPageSize])
	// 解密 0x78 字节加密头（LCG XOR 与读侧 decryptR2004Header/lcgKeyStream 同源）
	pad := R2004LCGPad(R2004HdrPlainSize)
	for i := 0; i < R2004HdrPlainSize; i++ {
		raw.HdrPlain[i] = Data[0x80+i] ^ pad[i]
	}
	Header, err := DecryptR2004Header(Data)
	if err != nil {
		return nil
	}
	Pages, err := ParsePageMap(Data, Header)
	if err != nil {
		return nil
	}
	// 页表页顺序按源保留（剔除 gap），并定位页表/段表两页的页 id
	sysmapAddr := Header.SectionPageMapAddress + 0x100
	pageAddrByID := make(map[uint32]uint64, len(Pages))
	for _, p := range Pages {
		if p.Id < 0 {
			continue // gap 页：写出端不留空洞，直接剔除
		}
		raw.PageOrder = append(raw.PageOrder, p.Id)
		pageAddrByID[uint32(p.Id)] = p.Address
		if p.Address == sysmapAddr {
			raw.SysmapID = p.Id
		}
		if p.Id == int32(Header.SectionMapID) {
			raw.InfoID = p.Id
		}
	}
	if raw.SysmapID == 0 || raw.InfoID == 0 {
		return nil
	}
	// 段表解压字节（原样回放）与段描述解析
	InfoBlob, err := inflateSystemSection(Data, pageAddrByID[uint32(raw.InfoID)], SectionMapMagic)
	if err != nil {
		return nil
	}
	Sections, err := parseR2004SectionInfo(InfoBlob)
	if err != nil {
		return nil
	}
	raw.InfoBlob = append([]byte(nil), InfoBlob...)
	lookup := make(map[uint32]PageSlot, len(pageAddrByID))
	for Id, addr := range pageAddrByID {
		lookup[Id] = PageSlot{Id: int32(Id), Address: addr}
	}
	seenPageIDs := make(map[uint32]bool, 64)
	for i := range Sections {
		sec := &Sections[i]
		for _, ref := range sec.Pages {
			if _, ok := pageAddrByID[ref.Id]; !ok {
				return nil // 段引用的页不在页表中：素材不完整，放弃回放
			}
			if seenPageIDs[ref.Id] {
				return nil // 页 id 重复引用：无法唯一定位，放弃回放
			}
			seenPageIDs[ref.Id] = true
		}
		if sec.encrypted == 1 {
			return nil // 容器级加密段无法回放（encrypted=2 为内容级混淆，字节回放无碍）
		}
		// 段数据复用读侧 assembleSection（逐页解密解压拼装 + size 截断）
		entry := secEntry{
			Size:              sec.Size,
			MaxDecompressedSz: sec.maxDecomp,
			Compressed:        sec.Compressed,
			Name:              sec.Name,
		}
		for _, ref := range sec.Pages {
			entry.PageIDs = append(entry.PageIDs, ref.Id)
		}
		blob, err := assembleSection(Data, &entry, lookup)
		if err != nil {
			return nil
		}
		sec.Data = blob
		// 逐页读取源数据页头（权威分页事实），并在读侧拼接视图中定位本页
		// 解压流切片：压缩段按 maxDecomp 步进铺放（读侧逐页补齐），未压缩
		// 段按源页载荷连续拼接；页流超出段 size 的尾部被读侧截断，切片同样
		// 截到视图末尾即可保证回读一致
		cursor := 0
		for pi := range sec.Pages {
			ref := &sec.Pages[pi]
			addr, ok := pageAddrByID[ref.Id]
			if !ok || addr+32 > uint64(len(Data)) {
				return nil
			}
			h := UnmaskPageHeader(Data[addr:addr+32], addr)
			compSz := int(binary.LittleEndian.Uint32(h[8:]))
			ref.StartOffset = uint64(binary.LittleEndian.Uint32(h[16:]))
			if sec.Compressed == 2 {
				// 读侧把每页解压流补齐到 maxDecomp 后顺序拼接，切片取整块
				// （页头 decomp_size 并非流的真实长度，整块切片配合回读侧
				// 同样的补齐/截断口径即可保证逐字节一致；块尾补零经 LZ77
				// 压缩后只占数字节）
				ref.DataOff = pi * int(sec.maxDecomp)
				ref.DataLen = int(sec.maxDecomp)
			} else {
				// 未压缩段：读侧按源页载荷连续拼接
				ref.DataOff = cursor
				ref.DataLen = compSz
				cursor += compSz
			}
			if ref.DataOff > len(blob) {
				ref.DataLen = 0 // 页步进起点已在段视图之外：整页为空
			}
			if ref.DataOff+ref.DataLen > len(blob) {
				ref.DataLen = len(blob) - ref.DataOff
			}
		}
		raw.Sections = append(raw.Sections, *sec)
	}
	return raw
}

// parseR2004SectionInfo 解析段表解压字节：5×u32 头 + 逐段 96 字节描述 +
// 逐页 16 字节三元组（与读侧 parseSectionMap 字段偏移一致）。
func parseR2004SectionInfo(blob []byte) ([]R2004SectionData, error) {
	if len(blob) < 20 {
		return nil, fmt.Errorf("cad: 段表头截断")
	}
	count := int(binary.LittleEndian.Uint32(blob))
	Pos := 20
	out := make([]R2004SectionData, 0, count)
	for i := 0; i < count; i++ {
		if len(blob)-Pos < 96 {
			return nil, fmt.Errorf("cad: 段表条目截断")
		}
		sec := R2004SectionData{
			Size:       binary.LittleEndian.Uint64(blob[Pos:]),
			maxDecomp:  binary.LittleEndian.Uint32(blob[Pos+12:]),
			Compressed: binary.LittleEndian.Uint32(blob[Pos+20:]),
			SecType:    binary.LittleEndian.Uint32(blob[Pos+24:]),
			encrypted:  binary.LittleEndian.Uint32(blob[Pos+28:]),
			Name:       ReadCString(blob[Pos+32 : Pos+96]),
		}
		pageCount := int(binary.LittleEndian.Uint32(blob[Pos+8:]))
		Pos += 96
		for p := 0; p < pageCount; p++ {
			if len(blob)-Pos < 16 {
				return nil, fmt.Errorf("cad: 段页描述截断")
			}
			// 段表三元组为 (页 id, 页压缩尺寸, 页目标偏移)；页压缩尺寸仅作
			// 记录，真实解压尺寸与偏移以数据页头为准（捕获阶段回填）
			sec.Pages = append(sec.Pages, R2004PageRef{
				Id:          binary.LittleEndian.Uint32(blob[Pos:]),
				StartOffset: binary.LittleEndian.Uint64(blob[Pos+8:]),
			})
			Pos += 16
		}
		out = append(out, sec)
	}
	return out, nil
}

// r2004LCGPad 生成 n 字节 LCG 伪随机序列（与 lcgKeyStream 同算法，
// 覆盖 0x78 全头长度）。
func R2004LCGPad(n int) []byte {
	pad := make([]byte, n)
	var seed uint32 = 1
	for i := range pad {
		seed = seed*0x343FD + 0x269EC3
		pad[i] = byte(seed >> 16)
	}
	return pad
}

// r2007SectionPageRaw 段内页的写出素材：源布局事实（页在段内偏移、页 id、
// 解压/压缩字节数），压缩尺寸随写出重压缩变化（仅捕获时供段装配链使用）。
type r2007SectionPageRaw struct {
	Offset     uint64
	Id         uint64
	UncompSize uint64
	compSize   uint64
}

// r2007SectionRaw 段的写出素材：段表条目全字段（读侧 r2007SecEntry 会
// 丢弃 max_size/hashcode/unknown，这里独立解析保留）+ 整段解压数据。
type r2007SectionRaw struct {
	Name     string
	Size     uint64 // 段有效字节数（data size）
	MaxSize  uint64 // 段分配上限（max size）
	Encoded  uint64 // 页编码方式（写侧统一重写为 4=RS）
	hashcode uint64
	Unknown  uint64
	Pages    []r2007SectionPageRaw
	Data     []byte
}

// r2007RawData R2007 容器的原始写出素材：解析时从源文件保留的头部、第二
// 头部 34 字段、页表顺序与各段解压数据，供 WriteDwgR2007 做文件级回放。
type R2007RawData struct {
	// prefix 文件头 [0x00,0x80)：版本串与缩略图/摘要信息段地址等（写出端
	// 回填段地址）。
	Prefix [R2007SecondHeaderOffset]byte
	// headerGap [0x458,0x480) 的 0x28 字节间隔区（读侧跳过，源样回放）。
	HeaderGap [0x28]byte
	// fields 第二头部解压体 34×u64（写出端回填布局相关字段）。
	Fields [34]uint64
	// pageOrder 页表条目按源顺序（含页表/段表自身两页，物理布局顺序）。
	PageOrder []R2007PageSlot
	// sysmapID/secmapID 页表页与段表页的页 id。
	SysmapID int64
	SecmapID int64
	// pmCorr/smCorr 页表与段表系统页的 correction 冗余份数（参与块数推导，
	// 保留源值使 RS 去交织块数与读侧口径完全一致）。
	PmCorr uint64
	SmCorr uint64
	// sections 各段素材（段表顺序，含尾部空条目）。
	Sections []r2007SectionRaw
	// trailingEmpty 段表尾部是否存在空条目（data_size/name_length/
	// num_pages 全零）：LibreDWG 对其报 "Invalid num_pages 0"（源文件基线
	// 错误的一部分），写侧重建段表时原样补回以保持基线一致。
	TrailingEmpty bool
}

// r2007DecodeHeaderBody 解出第二头部 34×u64 字段体（captureR2007Raw 专用，
// 与读侧 decodeR2007Header 同链但保留全量字段）。
func r2007DecodeHeaderBody(Data []byte) ([34]uint64, error) {
	var Fields [34]uint64
	if len(Data) < R2007SecondHeaderOffset+R2007SecondHeaderRSSize {
		return Fields, fmt.Errorf("cad: 文件过小，缺少 R2007 第二头部")
	}
	decoded, err := R2007Deinterleave(Data[R2007SecondHeaderOffset:R2007SecondHeaderOffset+R2007SecondHeaderRSSize], 239, 3, 4)
	if err != nil {
		return Fields, err
	}
	comprLen := int64(int32(binary.LittleEndian.Uint32(decoded[24:])))
	var body []byte
	switch {
	case comprLen < 0:
		Size := int(-comprLen)
		if R2007SecondHeaderPayload+Size > len(decoded) || Size < R2007SecondHeaderBodySize {
			return Fields, fmt.Errorf("cad: R2007 第二头部体越界")
		}
		body = decoded[R2007SecondHeaderPayload : R2007SecondHeaderPayload+Size]
	case comprLen > 0:
		if R2007SecondHeaderPayload+int(comprLen) > len(decoded) {
			return Fields, fmt.Errorf("cad: R2007 第二头部压缩体越界")
		}
		body, err = DecompressR21(decoded[R2007SecondHeaderPayload:R2007SecondHeaderPayload+int(comprLen)], R2007SecondHeaderBodySize)
		if err != nil {
			return Fields, err
		}
	default:
		return Fields, fmt.Errorf("cad: R2007 第二头部 compressedSize 为 0")
	}
	if len(body) < R2007SecondHeaderBodySize {
		return Fields, fmt.Errorf("cad: R2007 第二头部体截断")
	}
	for i := range Fields {
		Fields[i] = binary.LittleEndian.Uint64(body[i*8:])
	}
	return Fields, nil
}

// r2007CaptureSectionMap 解析段表系统页并保留条目全字段（8×u64 头 + UTF-16
// 段名 + 每页 7×u64 中的布局事实），读侧同名解析会丢弃 max_size 等字段。
// 第二返回值表示段表尾部是否存在空条目。
func r2007CaptureSectionMap(Data []byte, hdr r2007Header, Pages []R2007PageSlot) ([]r2007SectionRaw, bool, error) {
	TrailingEmpty := false
	var secMapPage *R2007PageSlot
	for i := range Pages {
		if Pages[i].Id == int64(hdr.SectionsMapID) {
			secMapPage = &Pages[i]
			break
		}
	}
	if secMapPage == nil {
		return nil, false, fmt.Errorf("cad: R2007 段表页不存在（id=%d）", hdr.SectionsMapID)
	}
	sysData, err := readR2007SystemPage(Data, secMapPage.Address, hdr.SectionsMapSizeCompressed,
		hdr.SectionsMapSizeUncompressed, hdr.SectionsMapCorrectionFactor)
	if err != nil {
		return nil, false, err
	}
	var Sections []r2007SectionRaw
	Pos := 0
	for len(sysData)-Pos >= r2007SectionEntrySize {
		read := func() uint64 {
			v := binary.LittleEndian.Uint64(sysData[Pos:])
			Pos += 8
			return v
		}
		Size := read()
		MaxSize := read()
		read() // encrypted：数据页读取流程与其无关，不保留
		hashcode := read()
		nameLength := read()
		Unknown := read()
		Encoded := read()
		pageCount := read()
		if Size == 0 && pageCount == 0 && nameLength == 0 {
			// 段表尾部空条目：本包读侧视为终止符，LibreDWG 报
			// "Invalid num_pages 0"（基线错误），捕获标志供写侧补回
			TrailingEmpty = true
			break
		}
		if nameLength > uint64(len(sysData)-Pos) {
			return nil, false, fmt.Errorf("cad: R2007 段名超长 %d", nameLength)
		}
		Name := DecodeUTF16LE(sysData[Pos : Pos+int(nameLength)])
		if i := indexByteStr(Name, 0); i >= 0 {
			Name = Name[:i]
		}
		Pos += int(nameLength)
		sec := r2007SectionRaw{
			Name:     Name,
			Size:     Size,
			MaxSize:  MaxSize,
			Encoded:  Encoded,
			hashcode: hashcode,
			Unknown:  Unknown,
		}
		for i := uint64(0); i < pageCount; i++ {
			if len(sysData)-Pos < r2007SectionPageSize {
				return nil, false, fmt.Errorf("cad: R2007 段页信息截断")
			}
			sec.Pages = append(sec.Pages, r2007SectionPageRaw{
				Offset:     binary.LittleEndian.Uint64(sysData[Pos:]),
				Id:         binary.LittleEndian.Uint64(sysData[Pos+16:]),
				UncompSize: binary.LittleEndian.Uint64(sysData[Pos+24:]),
				compSize:   binary.LittleEndian.Uint64(sysData[Pos+32:]),
			})
			Pos += r2007SectionPageSize
		}
		Sections = append(Sections, sec)
	}
	if len(Sections) == 0 {
		return nil, false, fmt.Errorf("cad: R2007 段表为空")
	}
	return Sections, TrailingEmpty, nil
}

// captureR2007Raw 从 R2007 源文件提取回放素材（解析路径的一次性钩子）：
// 走与读侧完全相同的解析链（第二头部→页表→段表→逐段装配），任一环节失败
// 返回 nil（不阻断解析，仅失去文件级写出能力）。
func CaptureR2007Raw(Data []byte) *R2007RawData {
	if len(Data) < 6 || string(Data[:6]) != "AC1021" || uint64(len(Data)) < R2007StreamBaseOffset {
		return nil
	}
	hdr, err := DecodeR2007Header(Data)
	if err != nil {
		return nil
	}
	Pages, err := ParseR2007PageMap(Data, hdr)
	if err != nil {
		return nil
	}
	Fields, err := r2007DecodeHeaderBody(Data)
	if err != nil {
		return nil
	}
	Sections, TrailingEmpty, err := r2007CaptureSectionMap(Data, hdr, Pages)
	if err != nil {
		return nil
	}
	raw := &R2007RawData{
		Fields:        Fields,
		PageOrder:     append([]R2007PageSlot(nil), Pages...),
		SysmapID:      int64(hdr.SectionsMapID),
		SecmapID:      int64(hdr.SectionsMapID),
		PmCorr:        hdr.PagesMapCorrectionFactor,
		SmCorr:        hdr.SectionsMapCorrectionFactor,
		Sections:      Sections,
		TrailingEmpty: TrailingEmpty,
	}
	// 页表页 id：地址为 0x480+pagesMapOffset 的页（fields[7] 源样保留，
	// 该页在新布局中仍按页表条目定位）
	sysAddr := R2007StreamBaseOffset + hdr.PagesMapOffset
	for _, p := range Pages {
		if p.Address == sysAddr {
			raw.SysmapID = p.Id
			break
		}
	}
	copy(raw.Prefix[:], Data[:R2007SecondHeaderOffset])
	if uint64(len(Data)) >= R2007StreamBaseOffset {
		copy(raw.HeaderGap[:], Data[0x458:0x480])
	}
	lookup := make(map[int64]R2007PageSlot, len(Pages))
	for _, p := range Pages {
		lookup[p.Id] = p
	}
	for i := range Sections {
		entry := r2007SecEntry{Size: Sections[i].Size, Encoded: Sections[i].Encoded, Name: Sections[i].Name}
		for _, p := range Sections[i].Pages {
			entry.Pages = append(entry.Pages, r2007PageInfo{
				Offset: p.Offset, Id: p.Id, SizeUncompressed: p.UncompSize, SizeCompressed: p.compSize,
			})
		}
		blob, err := assembleR2007Section(Data, &entry, Pages)
		if err != nil {
			return nil // 段装配失败：素材不完整，放弃回放
		}
		Sections[i].Data = blob
	}
	raw.Sections = Sections
	return raw
}
