// encode_file.go 实现 DWG 文件级写出（dwg.write 第一/二代）：R2000（AC1015）
// 家族采用「回放式」写出——段数据整段原样、对象记录整记录原样（解析时保留
// 的原始字节），仅段目录与对象图按新布局重建。容器是三代中最简单的：段数据
// 不压缩，段目录为 0x15 处的 record_no+offset+size 表 + 尾部 CRC + 哨兵。
// R2004（AC1018~AC1032，加密头+页表+LZ77 压缩段）同样采用回放式：各段解压
// 数据、段表（Section Info）与页表条目顺序整段原样保留，仅按 compressLZ77
// 重新分页压缩并重建页表/加密头校验和。R2007（AC1021，RS 去交织+R21 解压
// 的第三代容器）见本文件尾部：captureR2007Raw 采集素材，WriteDwgR2007 按
// compressR21 重压缩分页、RS 交织重建页表/段表/第二头部。
package cad

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/objrec"
	"hash/crc32"
	"io"
)

// r2000RawData R2000 家族容器的原始写出素材：解析时从源文件保留的原样字节，
// 供 WriteDwgR2000 做文件级回放。仅在 R2000/R13/R14 解析路径填充。
type r2000RawData struct {
	// header 文件头 [0x00,0x15) 原始字节：版本串（AC1012/AC1014/AC1015）+
	// 未知区 + 码页。写出端原样回放，保证 detectVersion/readCodepage 一致。
	header []byte
	// order 段号按源目录出现顺序（写出目录条目时保持同序）。
	order []uint8
	// sections 段号 → 段原始字节（含段内哨兵/CRC，仅收录源目录中 size>0 的段）。
	sections map[uint8][]byte
	// refs 对象图条目（handle 与源文件内绝对偏移），重建对象图流用。
	refs []objrec.ObjectRef
	// objBase 对象区源基址（refs 中最小记录偏移）。
	objBase uint32
	// objBlob 对象区原始字节 [objBase, 最大记录尾)，含 MS 头与尾部 CRC，
	// 整块回放到新布局后基址平移，对象图差分随之重编。
	objBlob []byte
}

// captureR2000Raw 从源文件与对象图条目提取回放素材（解析路径的一次性钩子）：
// 尽力保留，任一段越界只跳过该段不阻断解析。
func captureR2000Raw(data []byte, refs []objrec.ObjectRef) *r2000RawData {
	raw := &r2000RawData{refs: refs, sections: make(map[uint8][]byte)}
	if len(data) >= 0x15 {
		raw.header = append([]byte(nil), data[:0x15]...)
	}
	locs, err := parseR2000Directory(data)
	if err == nil {
		for _, loc := range locs {
			raw.order = append(raw.order, loc.recordNo)
			if loc.size == 0 {
				continue
			}
			start, end := int(loc.offset), int(loc.offset)+int(loc.size)
			if start > len(data) || end > len(data) || start > end {
				continue
			}
			raw.sections[loc.recordNo] = append([]byte(nil), data[start:end]...)
		}
	}
	// 对象区跨度：覆盖对象图引用的全部记录（MS 头 + body + 2 字节尾部 CRC）。
	// 源文件中记录连续铺放，取 [最小偏移, 最大记录尾) 整块保留即可完整回放。
	minOff, maxEnd := ^uint64(0), uint64(0)
	for _, ref := range refs {
		end, ok := r2000RecordEnd(data, ref.Offset)
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
	if maxEnd > 0 && minOff < maxEnd && maxEnd <= uint64(len(data)) {
		raw.objBase = uint32(minOff)
		raw.objBlob = append([]byte(nil), data[minOff:maxEnd]...)
	}
	return raw
}

// r2000RecordEnd 计算对象记录在文件中的结束偏移，与 parseObjectRecord 的
// 读取语义精确对齐：MS 大小字段（2 或 4 字节，RS 字高位为续传标志）+
// size 字节 body + 2 字节尾部 CRC。
func r2000RecordEnd(data []byte, off uint32) (uint64, bool) {
	if uint64(off)+2 > uint64(len(data)) {
		return 0, false
	}
	w0 := binary.LittleEndian.Uint16(data[off:])
	msBytes := 2
	w1 := uint16(0)
	if w0&0x8000 != 0 {
		msBytes = 4
		if uint64(off)+4 > uint64(len(data)) {
			return 0, false
		}
		w1 = binary.LittleEndian.Uint16(data[off+2:])
	}
	size := uint32(w0&0x7FFF) | uint32(w1&0x7FFF)<<15
	end := uint64(off) + uint64(msBytes) + uint64(size) + 2
	if end > uint64(len(data)) {
		return 0, false
	}
	return end, true
}

// WriteDwgR2000 将 R2000（AC1015）家族文档（含同容器路径的 R13/R14，版本串
// 随素材原样保留）写出为 DWG 字节流。文档必须来自 Parse（内部保留回放素材）。
//
// 文件布局：头部 15 字节回放 → 段目录（条目重建 + CRC(seed 0xC0C1，覆盖
// 从字节 0 起) + 16 字节哨兵）→ 各段原始字节（源目录顺序，对象图段除外）
// → 对象区整块回放 → 重建的对象图。写出的文件可被本包 Parse 与 LibreDWG
// 重新读取（见 encode_file_test.go 门禁）。
func WriteDwgR2000(doc *Document, w io.Writer) error {
	if doc == nil || doc.r2000Raw == nil {
		return fmt.Errorf("cad: 非 R2000 家族文档或缺少回放素材，无法写出")
	}
	out, err := writeR2000Sections(doc.r2000Raw)
	if err != nil {
		return err
	}
	_, err = w.Write(out)
	return err
}

// writeR2000Sections 按新布局组装 R2000 文件字节流。段写出按容器版本分派：
// R2000 家族走回放式；R2004/R2007 容器由后续版本的段写出器在此分派扩展。
func writeR2000Sections(raw *r2000RawData) ([]byte, error) {
	if len(raw.order) == 0 {
		return nil, fmt.Errorf("cad: 源文件缺少 R2000 段目录")
	}
	// 布局分配：目录区尺寸只依赖条目数，先于各段定址；对象图流长度与
	// 绝对偏移无关（差分编码），且置于文件末尾，无循环依赖。
	mapPayload := []byte(nil)
	hasMap := false
	if blob := raw.sections[r2000SecObjectMap]; len(blob) > 0 || len(raw.refs) > 0 {
		hasMap = true
		// 对象区新基址 = 目录尾 + 各回放段总长；先累加得到再重建对象图
		objNewBase := uint64(0x15 + 4 + len(raw.order)*9 + 2 + len(r2000LocatorSentinel))
		for _, no := range raw.order {
			if no == r2000SecObjectMap {
				continue
			}
			objNewBase += uint64(len(raw.sections[no]))
		}
		delta := int64(objNewBase) - int64(raw.objBase)
		mapPayload = buildR2000ObjectMap(raw.refs, delta)
	}
	// 各段与对象区顺序定址
	type placed struct {
		no     uint8
		offset uint32
		size   uint32
	}
	placements := make([]placed, 0, len(raw.order))
	cursor := uint64(0x15 + 4 + len(raw.order)*9 + 2 + len(r2000LocatorSentinel))
	for _, no := range raw.order {
		if no == r2000SecObjectMap {
			continue
		}
		payload := raw.sections[no]
		placements = append(placements, placed{no: no, offset: uint32(cursor), size: uint32(len(payload))})
		cursor += uint64(len(payload))
	}
	// 对象区紧跟各回放段顺序铺放；对象图置于文件末尾
	cursor += uint64(len(raw.objBlob))
	mapOffset := cursor
	cursor += uint64(len(mapPayload))
	if cursor > 0xFFFFFFFF {
		return nil, fmt.Errorf("cad: R2000 写出体积超过 4GB 布局上限")
	}
	// 目录条目：对象图段用重建流尺寸；素材缺失的段回退 0/0
	byNo := map[uint8]placed{}
	for _, p := range placements {
		byNo[p.no] = p
	}
	w := bitstream.NewEncWriter()
	w.WriteTF(raw.header)
	w.WriteRL(uint32(len(raw.order)))
	for _, no := range raw.order {
		if no == r2000SecObjectMap {
			if hasMap {
				w.WriteRC(no)
				w.WriteRL(uint32(mapOffset))
				w.WriteRL(uint32(len(mapPayload)))
			} else {
				w.WriteRC(no)
				w.WriteRL(0)
				w.WriteRL(0)
			}
			continue
		}
		p, ok := byNo[no]
		w.WriteRC(no)
		if ok {
			w.WriteRL(p.offset)
			w.WriteRL(p.size)
		} else {
			w.WriteRL(0)
			w.WriteRL(0)
		}
	}
	// 目录 CRC：与 LibreDWG 一致，覆盖从字节 0 到条目结束（非仅目录区）
	w.WriteCRCSeed(0, 0xC0C1)
	w.WriteTF(r2000LocatorSentinel[:])
	// 段数据与对象区按定址顺序回放
	for _, p := range placements {
		w.WriteTF(raw.sections[p.no])
	}
	w.WriteTF(raw.objBlob)
	w.WriteTF(mapPayload)
	return w.Bytes(), nil
}

// buildR2000ObjectMap 重建对象图流：条目为 (handle UMC 增量, offset MC 增量)
// 差分对，按 LibreDWG 规则分块（块长含 size 字段超过 2030 字节换块），每块
// [BE u16 size][差分对][BE u16 CRC(seed 0xC0C1)]，块内差分从 0 重新累计，
// 以 size=2 的终止块收尾。baseDelta 为对象区基址平移量（新偏移 = 源偏移 +
// baseDelta）。
func buildR2000ObjectMap(refs []objrec.ObjectRef, baseDelta int64) []byte {
	out := make([]byte, 0, len(refs)*6+16)
	chunkStart := 0
	lastHandle := int64(0)
	lastOffset := int64(0)
	// closeChunk 回填块 size 字段并追加 BE CRC。size 从块首（size 字段
	// 起）计到差分对结束，即 total 本身（读侧 end = 块首 + size - 2 恰为
	// 差分对尾）；CRC 覆盖 [块首, 差分对结束)，与 bit_write_CRC_BE 一致。
	closeChunk := func() {
		total := len(out) - chunkStart
		binary.BigEndian.PutUint16(out[chunkStart:], uint16(total))
		crc := bitstream.Crc16DWG(0xC0C1, out[chunkStart:chunkStart+total])
		out = binary.BigEndian.AppendUint16(out, crc)
	}
	chunkStart = len(out)
	out = append(out, 0, 0) // size 字段占位
	for _, ref := range refs {
		newOffset := int64(ref.Offset) + baseDelta
		out = putUMC(out, uint64(int64(ref.Handle)-lastHandle))
		out = putMC(out, newOffset-lastOffset)
		lastHandle = int64(ref.Handle)
		lastOffset = newOffset
		if len(out)-chunkStart > 2030 {
			closeChunk()
			chunkStart = len(out)
			out = append(out, 0, 0)
			lastHandle, lastOffset = 0, 0
		}
	}
	closeChunk()
	// 终止块：size=2 + 覆盖自身的 BE CRC
	termStart := len(out)
	out = append(out, 0, 2)
	crc := bitstream.Crc16DWG(0xC0C1, out[termStart:])
	out = binary.BigEndian.AppendUint16(out, crc)
	return out
}

// putUMC 追加无符号模块化字符（对象图 handle 增量专用）：每字节 7 位低位在前，
// 最高位为续传标志，最多 5 字节（与 readUnsignedModularChar 对称）。
func putUMC(b []byte, v uint64) []byte {
	for i := 0; i < 5; i++ {
		cur := byte(v & 0x7F)
		v >>= 7
		if v != 0 {
			b = append(b, cur|0x80)
			continue
		}
		return append(b, cur)
	}
	return b
}

// putMC 追加有符号模块化字符（对象图 offset 增量）：终止字节最高位为 0、
// 0x40 位为符号（负数写绝对值后置符号位），故终止字节至多承载 6 个数值位，
// 剩余值含 0x40 位时需补一个续传字节；最多 4 字节（与 readModularChar、
// LibreDWG bit_write_MC 对称）。
func putMC(b []byte, v int64) []byte {
	neg := v < 0
	u := uint64(v)
	if neg {
		u = uint64(-v)
	}
	for i := 0; i < 4; i++ {
		if u < 64 {
			cur := byte(u)
			if neg {
				cur |= 0x40
			}
			return append(b, cur)
		}
		b = append(b, byte(u&0x7F)|0x80)
		u >>= 7
	}
	return append(b, 0)
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
	r2004SystemHdrSize = 0x14 // 签名+解压尺寸+压缩尺寸+压缩类型+校验和
	r2004PageHdrSize   = 32   // 数据页加密页头
	r2004HdrPlainSize  = 0x78 // 加密头覆盖的明文长度（0x80~0xF8）
	// r2004HeaderPageSize 文件头明文区长度（加密头之前的部分）。
	r2004HeaderPageSize = 0x100
)

// r2004PageRef 数据段的页事实：
//   - startOffset 取自源数据页头（start_offset=页在段内的目标偏移，压缩段
//     按 maxDecomp 步进铺放）；
//   - dataOff/dataLen 为本页内容在读侧拼接视图（assembleSection 逐页补齐
//     maxDecomp 后顺序拼接、按段 size 截断）中的切片位置。
type r2004PageRef struct {
	id          uint32
	startOffset uint64
	dataOff     int
	dataLen     int
}

// r2004SectionData 一个数据段的回放素材：源段表描述字段 + 按源分页三元组
// 切出的解压后完整数据（即对象记录等业务字节，写出处整段重压缩）。
type r2004SectionData struct {
	name       string
	secType    uint32 // 段类型码（写回数据页头 section_type 字段）
	size       uint64 // 段有效字节数（末页可短于 maxDecomp）
	maxDecomp  uint32 // 单页解压尺寸上限
	compressed uint32 // 1=不压缩 2=LZ77（与读侧 secEntry.compressed 一致）
	encrypted  uint32 // 0=明文（加密段无法回放，捕获时放弃）
	pages      []r2004PageRef
	data       []byte
}

// r2004RawData R2004 家族容器的原始写出素材：解析时从源文件保留的头部
// 字节、页表页顺序、段表解压字节与各段解压数据，供 WriteDwgR2004 做文件级
// 回放。仅在 R2004/R2010/R2013/R2018 解析路径填充（R2007 容器独立）。
type r2004RawData struct {
	// prefix 文件头明文区 [0x00,0x100) 原始字节：版本串、缩略图/摘要段
	// 地址等（写出端仅回填 0x0D/0x20/0x24 三处段地址）。
	prefix [r2004HeaderPageSize]byte
	// hdrPlain 解密后的 r2004 加密头明文 [0x80,0xF8)（写出端回填定位字段
	// 并重算 CRC32 后重新加密）。
	hdrPlain [r2004HdrPlainSize]byte
	// pageOrder 页表页 id 按源顺序（已剔除 gap 与页表自身语义无关，仅用于
	// 保持物理布局顺序；页表/段表两页的尺寸在写出时重算）。
	pageOrder []int32
	// sysmapID/infoID 页表自身与段表所在页的页 id（写回加密头 0x50/0x5C）。
	sysmapID int32
	infoID   int32
	// infoBlob 段表（Section Info）解压字节，原样回放。
	infoBlob []byte
	// sections 各数据段素材（段表描述顺序）。
	sections []r2004SectionData
}

// captureR2004Raw 从 R2004+ 源文件提取回放素材（解析路径的一次性钩子）：
// 走与读侧完全相同的解析链（decryptR2004Header→parsePageMap→段表→assembleSection），
// 任一环节失败返回 nil（不阻断解析，仅失去文件级写出能力）。
func captureR2004Raw(data []byte) *r2004RawData {
	if len(data) < r2004HeaderPageSize || (len(data) >= 6 && string(data[:6]) == "AC1021") {
		// R2007 容器结构独立，不在此捕获
		return nil
	}
	raw := &r2004RawData{}
	copy(raw.prefix[:], data[:r2004HeaderPageSize])
	// 解密 0x78 字节加密头（LCG XOR 与读侧 decryptR2004Header/lcgKeyStream 同源）
	pad := r2004LCGPad(r2004HdrPlainSize)
	for i := 0; i < r2004HdrPlainSize; i++ {
		raw.hdrPlain[i] = data[0x80+i] ^ pad[i]
	}
	header, err := decryptR2004Header(data)
	if err != nil {
		return nil
	}
	pages, err := parsePageMap(data, header)
	if err != nil {
		return nil
	}
	// 页表页顺序按源保留（剔除 gap），并定位页表/段表两页的页 id
	sysmapAddr := header.sectionPageMapAddress + 0x100
	pageAddrByID := make(map[uint32]uint64, len(pages))
	for _, p := range pages {
		if p.id < 0 {
			continue // gap 页：写出端不留空洞，直接剔除
		}
		raw.pageOrder = append(raw.pageOrder, p.id)
		pageAddrByID[uint32(p.id)] = p.address
		if p.address == sysmapAddr {
			raw.sysmapID = p.id
		}
		if p.id == int32(header.sectionMapID) {
			raw.infoID = p.id
		}
	}
	if raw.sysmapID == 0 || raw.infoID == 0 {
		return nil
	}
	// 段表解压字节（原样回放）与段描述解析
	infoBlob, err := inflateSystemSection(data, pageAddrByID[uint32(raw.infoID)], sectionMapMagic)
	if err != nil {
		return nil
	}
	sections, err := parseR2004SectionInfo(infoBlob)
	if err != nil {
		return nil
	}
	raw.infoBlob = append([]byte(nil), infoBlob...)
	lookup := make(map[uint32]pageSlot, len(pageAddrByID))
	for id, addr := range pageAddrByID {
		lookup[id] = pageSlot{id: int32(id), address: addr}
	}
	seenPageIDs := make(map[uint32]bool, 64)
	for i := range sections {
		sec := &sections[i]
		for _, ref := range sec.pages {
			if _, ok := pageAddrByID[ref.id]; !ok {
				return nil // 段引用的页不在页表中：素材不完整，放弃回放
			}
			if seenPageIDs[ref.id] {
				return nil // 页 id 重复引用：无法唯一定位，放弃回放
			}
			seenPageIDs[ref.id] = true
		}
		if sec.encrypted == 1 {
			return nil // 容器级加密段无法回放（encrypted=2 为内容级混淆，字节回放无碍）
		}
		// 段数据复用读侧 assembleSection（逐页解密解压拼装 + size 截断）
		entry := secEntry{
			size:              sec.size,
			maxDecompressedSz: sec.maxDecomp,
			compressed:        sec.compressed,
			name:              sec.name,
		}
		for _, ref := range sec.pages {
			entry.pageIDs = append(entry.pageIDs, ref.id)
		}
		blob, err := assembleSection(data, &entry, lookup)
		if err != nil {
			return nil
		}
		sec.data = blob
		// 逐页读取源数据页头（权威分页事实），并在读侧拼接视图中定位本页
		// 解压流切片：压缩段按 maxDecomp 步进铺放（读侧逐页补齐），未压缩
		// 段按源页载荷连续拼接；页流超出段 size 的尾部被读侧截断，切片同样
		// 截到视图末尾即可保证回读一致
		cursor := 0
		for pi := range sec.pages {
			ref := &sec.pages[pi]
			addr, ok := pageAddrByID[ref.id]
			if !ok || addr+32 > uint64(len(data)) {
				return nil
			}
			h := unmaskPageHeader(data[addr:addr+32], addr)
			compSz := int(binary.LittleEndian.Uint32(h[8:]))
			ref.startOffset = uint64(binary.LittleEndian.Uint32(h[16:]))
			if sec.compressed == 2 {
				// 读侧把每页解压流补齐到 maxDecomp 后顺序拼接，切片取整块
				// （页头 decomp_size 并非流的真实长度，整块切片配合回读侧
				// 同样的补齐/截断口径即可保证逐字节一致；块尾补零经 LZ77
				// 压缩后只占数字节）
				ref.dataOff = pi * int(sec.maxDecomp)
				ref.dataLen = int(sec.maxDecomp)
			} else {
				// 未压缩段：读侧按源页载荷连续拼接
				ref.dataOff = cursor
				ref.dataLen = compSz
				cursor += compSz
			}
			if ref.dataOff > len(blob) {
				ref.dataLen = 0 // 页步进起点已在段视图之外：整页为空
			}
			if ref.dataOff+ref.dataLen > len(blob) {
				ref.dataLen = len(blob) - ref.dataOff
			}
		}
		raw.sections = append(raw.sections, *sec)
	}
	return raw
}

// parseR2004SectionInfo 解析段表解压字节：5×u32 头 + 逐段 96 字节描述 +
// 逐页 16 字节三元组（与读侧 parseSectionMap 字段偏移一致）。
func parseR2004SectionInfo(blob []byte) ([]r2004SectionData, error) {
	if len(blob) < 20 {
		return nil, fmt.Errorf("cad: 段表头截断")
	}
	count := int(binary.LittleEndian.Uint32(blob))
	pos := 20
	out := make([]r2004SectionData, 0, count)
	for i := 0; i < count; i++ {
		if len(blob)-pos < 96 {
			return nil, fmt.Errorf("cad: 段表条目截断")
		}
		sec := r2004SectionData{
			size:       binary.LittleEndian.Uint64(blob[pos:]),
			maxDecomp:  binary.LittleEndian.Uint32(blob[pos+12:]),
			compressed: binary.LittleEndian.Uint32(blob[pos+20:]),
			secType:    binary.LittleEndian.Uint32(blob[pos+24:]),
			encrypted:  binary.LittleEndian.Uint32(blob[pos+28:]),
			name:       readCString(blob[pos+32 : pos+96]),
		}
		pageCount := int(binary.LittleEndian.Uint32(blob[pos+8:]))
		pos += 96
		for p := 0; p < pageCount; p++ {
			if len(blob)-pos < 16 {
				return nil, fmt.Errorf("cad: 段页描述截断")
			}
			// 段表三元组为 (页 id, 页压缩尺寸, 页目标偏移)；页压缩尺寸仅作
			// 记录，真实解压尺寸与偏移以数据页头为准（捕获阶段回填）
			sec.pages = append(sec.pages, r2004PageRef{
				id:          binary.LittleEndian.Uint32(blob[pos:]),
				startOffset: binary.LittleEndian.Uint64(blob[pos+8:]),
			})
			pos += 16
		}
		out = append(out, sec)
	}
	return out, nil
}

// r2004LCGPad 生成 n 字节 LCG 伪随机序列（与 lcgKeyStream 同算法，
// 覆盖 0x78 全头长度）。
func r2004LCGPad(n int) []byte {
	pad := make([]byte, n)
	var seed uint32 = 1
	for i := range pad {
		seed = seed*0x343FD + 0x269EC3
		pad[i] = byte(seed >> 16)
	}
	return pad
}

// r2004PageChecksum DWG 段页校验和（Adler 变体，与 LibreDWG
// dwg_section_page_checksum 一致）：seed 拆高低 16 位累加，每 0x15B0 字节
// 归一次模 0xFFF1。parts 依次累积；skipZero 为按 0 贡献 sum1、照常推进
// sum2 的字节数（对齐读侧跳过 system section 头中校验和字段本身的语义）。
func r2004PageChecksum(seed uint32, skipZero int, parts ...[]byte) uint32 {
	sum1 := seed & 0xFFFF
	sum2 := seed >> 16
	advance := func(b byte) {
		sum1 += uint32(b)
		sum2 += sum1
	}
	flush := func() {
		sum1 %= 0xFFF1
		sum2 %= 0xFFF1
	}
	for _, part := range parts {
		for len(part) > 0 {
			chunk := len(part)
			if chunk > 0x15B0 {
				chunk = 0x15B0
			}
			for i := 0; i < chunk; i++ {
				advance(part[i])
			}
			part = part[chunk:]
			flush()
		}
	}
	for i := 0; i < skipZero; i++ {
		advance(0)
	}
	flush()
	return sum2<<16 | sum1&0xFFFF
}

// WriteDwg 按 Document 版本自动分派容器写出器：R2007（AC1021）走
// WriteDwgR2007，R2004 家族（含 R2010/R2013/R2018，同一页式容器）走
// WriteDwgR2004，其余走 WriteDwgR2000。三者均无回放素材时（JSON/DXF/
// 合成来源）自动降级到结构化正向路径 writeDwgForwardR2000——按位流
// 规范重建 R2000 容器，不再报「缺少回放素材」。
func WriteDwg(doc *Document, w io.Writer) error {
	if doc != nil && doc.r2007Raw != nil {
		return WriteDwgR2007(doc, w)
	}
	if doc != nil && doc.r2004Raw != nil {
		return WriteDwgR2004(doc, w)
	}
	if doc != nil && doc.r2000Raw != nil {
		return WriteDwgR2000(doc, w)
	}
	out, err := writeDwgForwardR2000(doc)
	if err != nil {
		return err
	}
	_, err = w.Write(out)
	return err
}

// WriteDwgR2004 将 R2004 家族文档（AC1018~AC1032 同容器）写出为 DWG 字节流。
// 文档必须来自 Parse（内部保留回放素材）。布局：头部 0x100 字节（含重加密
// 的 r2004 头）→ 按源页表顺序铺放各数据页/段表页/页表页（数据页重压缩，
// 段表内容原样回放）→ 尾部 secondheader 区（20 字节伪 system 段头 + 加密
// 头副本）。对象记录字节随 AcDb:AcDbObjects 段原样回放，句柄偏移不变。
func WriteDwgR2004(doc *Document, w io.Writer) error {
	if doc == nil || doc.r2004Raw == nil {
		return fmt.Errorf("cad: 非 R2004 家族文档或缺少回放素材，无法写出")
	}
	out, err := writeR2004Sections(doc.r2004Raw)
	if err != nil {
		return err
	}
	_, err = w.Write(out)
	return err
}

// r2004PlacedPage 一个待铺放页的寻址与载荷：kind 区分数据页/段表页/页表页。
type r2004PlacedPage struct {
	id      int32
	address uint64
	size    uint32
	kind    int // 0=数据页 1=段表(Section Info) 2=页表(System Map)
	payload []byte
}

// r2004CompressBlock 将页内容块压缩为 R2004 LZ77 流。与 compressLZ77 同源
// （复用 lzGreedyMatches/lzWriteLitLen/lzWriteExtLen），但只用与读侧
// lz77.go 及 LibreDWG decompress_R2004_section 语义完全一致的 opcode 子集：
//   - 字面量：0x01-0x0F 短长度 / 0x00 扩展链；
//   - 近距匹配 0x40-0xFF（距离 ≤0x400，长 3..14）；
//   - 中距匹配 0x21-0x3F（距离 ≤0x400，长 3..33）与 0x20 长度扩展；
//   - 0x11 终止符。
//
// 不发出 0x10/0x12-0x1F 远距 opcode：两套解压器对其基准位移/长度链的解释
// 不一致（早期对齐基准 0x3FFF，LibreDWG 规格 0x4000/0x8000 且 0x18 走扩展
// 长度链），真实文件亦从不使用；把匹配窗口限制在 0x400 内即可完全回避，
// 代价仅为远距重复退化为字面量（约几个百分点的压缩率）。
func r2004CompressBlock(src []byte) []byte {
	n := len(src)
	if n == 0 {
		return []byte{0x11}
	}
	if n < 4 {
		// 字面量游程最短可编码长度为 4：补零占位，解压端按声明尺寸截断
		data := make([]byte, 4)
		copy(data, src)
		return append(append([]byte{0x01}, data...), 0x11)
	}
	matches := lzGreedyMatches(src, 4, 0x400, n)
	out := make([]byte, 0, n/2+16)
	if len(matches) == 0 {
		out = lzWriteLitLen(out, n)
		out = append(out, src...)
		return append(out, 0x11)
	}
	if matches[0].pos > 0 {
		// 首匹配起点 ≥ minMatch，前导字面量段必然可独立编码
		out = lzWriteLitLen(out, matches[0].pos)
		out = append(out, src[:matches[0].pos]...)
	}
	for i, m := range matches {
		// 该匹配之后的字面量段（至下一匹配或流尾）：1..3 内嵌进匹配块
		// 低 2 位，≥4 单独编码——与解压端 litCount 分支一一对应
		segStart := m.pos + m.length
		segEnd := n
		if i+1 < len(matches) {
			segEnd = matches[i+1].pos
		}
		litLen := segEnd - segStart
		litCount := 0
		if litLen <= 3 {
			litCount = litLen
		}
		switch {
		case m.length <= 14:
			off := m.dist - 1
			op := byte((m.length+1)<<4) | byte(off&0x03)<<2 | byte(litCount)
			out = append(out, op, byte(off>>2))
		case m.length <= 33:
			off := m.dist - 1
			out = append(out, byte(0x1E+m.length), byte(off&0x3F)<<2|byte(litCount), byte(off>>6))
		default:
			off := m.dist - 1
			out = append(out, 0x20)
			out = lzWriteExtLen(out, m.length-0x21)
			out = append(out, byte(off&0x3F)<<2|byte(litCount), byte(off>>6))
		}
		if litCount > 0 {
			out = append(out, src[segStart:segEnd]...)
		} else if litLen > 0 {
			out = lzWriteLitLen(out, litLen)
			out = append(out, src[segStart:segEnd]...)
		}
	}
	return append(out, 0x11)
}

// writeR2004Sections 按新布局组装 R2004 文件字节流。
func writeR2004Sections(raw *r2004RawData) ([]byte, error) {
	// ① 各数据页重压缩：按源页头事实切片（整块）→ r2004CompressBlock，并
	//    逐页自检（压缩结果经自家解压器还原必须逐字节一致，失败即拒绝写出）
	payloads := make(map[uint32][]byte, 64)
	for si := range raw.sections {
		sec := &raw.sections[si]
		for pi, ref := range sec.pages {
			chunk := sec.data[ref.dataOff : ref.dataOff+ref.dataLen]
			var payload []byte
			if sec.compressed == 2 {
				payload = r2004CompressBlock(chunk)
				back, err := decompressLZ77(payload, len(chunk))
				if err != nil || !bytes.Equal(back, chunk) {
					return nil, fmt.Errorf("cad: 段 %q 第 %d 页压缩自检失败", sec.name, pi)
				}
			} else {
				payload = chunk
			}
			payloads[ref.id] = payload
		}
	}
	// ② 段表页与页表页载荷（段表内容原样回放，仅重新压缩）
	infoPayload := compressLZ77(raw.infoBlob)
	infoSize := uint32(r2004SystemHdrSize + len(infoPayload))
	// 页表自身条目尺寸与内容相互依赖（条目写在内容里）。页表页采用
	// store（不压缩）写出（对齐 LibreDWG write 端 SECTION_SYSTEM_MAP 的
	// store 策略）：store 下页尺寸是内容长度的确定性函数，与自身条目的
	// 数值无关，迭代一次即收敛。此前对页表内容做 LZ77 重压缩，压缩大小
	// 随自身条目数值抖动，Leader/material/skylight 语料出现 173↔175 的
	// 2-循环振荡，固定点不存在导致写出失败。
	buildMap := func(ownSize uint32) []byte {
		buf := make([]byte, 0, len(raw.pageOrder)*8)
		for _, id := range raw.pageOrder {
			size := uint32(0)
			switch id {
			case raw.infoID:
				size = infoSize
			case raw.sysmapID:
				size = ownSize
			default:
				if pl, ok := payloads[uint32(id)]; ok {
					size = uint32(r2004PageHdrSize + len(pl))
				}
			}
			buf = binary.LittleEndian.AppendUint32(buf, uint32(id))
			buf = binary.LittleEndian.AppendUint32(buf, size)
		}
		return buf
	}
	// 页表内容压缩为全字面量 LZ77 流（compressLZ77LiteralOnly）：压缩后
	// 尺寸是内容长度的确定性函数，与自身条目数值无关，自引用尺寸一次
	// 收敛。此前 store 写出（compression_type=1）不被 LibreDWG 读侧识别
	// （其系统段解压无视该字段），启用匹配的压缩则因数值抖动振荡。
	sysSize := uint32(r2004SystemHdrSize)
	var mapPayload []byte
	for iter := 0; ; iter++ {
		mapPayload = compressLZ77LiteralOnly(buildMap(sysSize))
		next := uint32(r2004SystemHdrSize + len(mapPayload))
		if next == sysSize {
			break
		}
		sysSize = next
		if iter >= 32 {
			return nil, fmt.Errorf("cad: 页表自引用尺寸不收敛")
		}
	}
	// ③ 定址：页表条目顺序即物理布局顺序，页地址从 0x100 起累加；
	//    未被任何段引用的孤立页条目剔除（gap 已在捕获时剔除）
	placed := make([]r2004PlacedPage, 0, len(raw.pageOrder))
	address := uint64(0x100)
	for _, id := range raw.pageOrder {
		var p r2004PlacedPage
		p.id = id
		p.address = address
		switch {
		case id == raw.infoID:
			p.kind, p.size, p.payload = 1, infoSize, infoPayload
		case id == raw.sysmapID:
			p.kind, p.size, p.payload = 2, sysSize, mapPayload
		default:
			pl, ok := payloads[uint32(id)]
			if !ok {
				continue // 孤立页：丢弃
			}
			p.kind = 0
			p.size = uint32(r2004PageHdrSize + len(pl))
			p.payload = pl
		}
		placed = append(placed, p)
		address += uint64(p.size)
	}
	if len(placed) == 0 {
		return nil, fmt.Errorf("cad: 页表为空，无法写出 R2004 容器")
	}
	lastEnd := address
	// ④ 文件体：头部明文区 + 按定址顺序铺放各页 + 尾部 secondheader 区
	out := make([]byte, lastEnd+128)
	copy(out, raw.prefix[:])
	// 数据页页头需要段描述（sec_type、页三元组），先建页 id → 段下标映射
	secByPage := make(map[uint32]int, len(payloads))
	for si := range raw.sections {
		for _, ref := range raw.sections[si].pages {
			secByPage[ref.id] = si
		}
	}
	for _, p := range placed {
		if p.kind == 0 {
			r2004WriteDataPage(out, p, &raw.sections[secByPage[uint32(p.id)]])
			continue
		}
		magic := uint32(sectionPageMapMagic)
		decompSize := 8 * len(raw.pageOrder) // 页表内容固定为 (id,size) 8 字节序列
		if p.kind == 1 {
			magic = sectionMapMagic
			decompSize = len(raw.infoBlob)
		}
		// 页表页（kind 2）payload 为 store 原始字节（见上方收敛注释），
		// 压缩类型写 1；段表页（kind 1）保持 LZ77 压缩（类型 2）
		r2004WriteSystemSection(out, p.address, magic, p.payload, decompSize, false)
	}
	// 尾部 secondheader：20 字节伪页表段头 + 加密头副本（对齐 LibreDWG
	// 写出端；读侧不校验，仅保持布局兼容）
	sh := int(lastEnd)
	binary.LittleEndian.PutUint32(out[sh:], sectionPageMapMagic)
	out[sh+12] = 0x02
	// ⑤ 明文头三处段地址回填（缩略图/摘要信息首页 +32，VBA 工程首页原址；
	//    读侧仅判非零决定是否加载对应段）
	patchAddr := func(name string, at int, plusHeader bool) {
		for si := range raw.sections {
			sec := &raw.sections[si]
			if sec.name != name || len(sec.pages) == 0 {
				continue
			}
			addr := r2004PageAddress(placed, sec.pages[0].id)
			if plusHeader {
				addr += r2004PageHdrSize
			}
			binary.LittleEndian.PutUint32(out[at:], uint32(addr))
			return
		}
		binary.LittleEndian.PutUint32(out[at:], 0)
	}
	patchAddr("AcDb:Preview", 0x0D, true)
	patchAddr("AcDb:SummaryInfo", 0x20, true)
	patchAddr("AcDb:VBAProject", 0x24, false)
	// ⑥ 加密头回填定位字段并重加密（读侧 read_R2004_section_map 的一致性
	//    检查点：last_section_address、numsections、section_array_size 等）
	hdr := raw.hdrPlain
	maxID := int32(0)
	for _, p := range placed {
		if p.id > maxID {
			maxID = p.id
		}
	}
	binary.LittleEndian.PutUint32(hdr[0x28:], uint32(placed[len(placed)-1].id))
	binary.LittleEndian.PutUint64(hdr[0x2C:], lastEnd-0x100)
	binary.LittleEndian.PutUint64(hdr[0x34:], lastEnd+20)
	binary.LittleEndian.PutUint32(hdr[0x3C:], 0) // gap 已剔除
	binary.LittleEndian.PutUint32(hdr[0x40:], uint32(len(placed)))
	binary.LittleEndian.PutUint32(hdr[0x50:], uint32(raw.sysmapID))
	binary.LittleEndian.PutUint64(hdr[0x54:], r2004PageAddress(placed, uint32(raw.sysmapID))-0x100)
	binary.LittleEndian.PutUint32(hdr[0x5C:], uint32(raw.infoID))
	binary.LittleEndian.PutUint32(hdr[0x60:], uint32(maxID))
	binary.LittleEndian.PutUint32(hdr[0x64:], 0)
	binary.LittleEndian.PutUint32(hdr[0x68:], 0)
	sum := crc32.Update(0, crc32.IEEETable, hdr[:0x6C])
	binary.LittleEndian.PutUint32(hdr[0x68:], sum)
	pad := r2004LCGPad(r2004HdrPlainSize)
	for i := 0; i < r2004HdrPlainSize; i++ {
		hdr[i] ^= pad[i]
	}
	copy(out[0x80:], hdr[:])
	return out, nil
}

// r2004PageAddress 按页 id 查已定址页的物理地址（未找到返回 0）。
func r2004PageAddress(placed []r2004PlacedPage, id uint32) uint64 {
	for _, p := range placed {
		if p.id == int32(id) {
			return p.address
		}
	}
	return 0
}

// r2004WriteSystemSection 写一个系统段页（页表/段表）：20 字节段头 +
// 载荷。校验和按读侧口径计算：先对头部 20 字节累积（校验和字段本身按 0
// 跳过），再以其为种子累积载荷。stored=true 时压缩类型写 1（载荷原样
// 存储），否则写 2（LZ77 压缩，载荷须为压缩后的字节）。decompSize 为
// 解压后内容字节数。
func r2004WriteSystemSection(out []byte, address uint64, magic uint32, payload []byte, decompSize int, stored bool) {
	off := int(address)
	binary.LittleEndian.PutUint32(out[off:], magic)
	binary.LittleEndian.PutUint32(out[off+4:], uint32(decompSize))
	binary.LittleEndian.PutUint32(out[off+8:], uint32(len(payload)))
	compressType := uint32(2)
	if stored {
		compressType = 1
	}
	binary.LittleEndian.PutUint32(out[off+12:], compressType)
	binary.LittleEndian.PutUint32(out[off+16:], 0)
	seed := r2004PageChecksum(0, 4, out[off:off+16])
	sum := r2004PageChecksum(seed, 0, payload)
	binary.LittleEndian.PutUint32(out[off+16:], sum)
	copy(out[off+r2004SystemHdrSize:], payload)
}

// r2004WriteDataPage 写一个数据页：构建 8×u32 明文页头（含页载荷校验和与
// 页头校验和），按 0x4164536B^页地址 掩码逐 4 字节加密后落位。
func r2004WriteDataPage(out []byte, p r2004PlacedPage, sec *r2004SectionData) {
	var ref *r2004PageRef
	for i := range sec.pages {
		if sec.pages[i].id == uint32(p.id) {
			ref = &sec.pages[i]
			break
		}
	}
	off := int(p.address)
	dataCRC := r2004PageChecksum(0, 0, p.payload)
	var hdr [32]byte
	binary.LittleEndian.PutUint32(hdr[0:], dataSectionMagic)
	binary.LittleEndian.PutUint32(hdr[4:], sec.secType)
	binary.LittleEndian.PutUint32(hdr[8:], uint32(len(p.payload)))
	binary.LittleEndian.PutUint32(hdr[12:], uint32(len(p.payload)))
	if ref != nil {
		// 页目标偏移保持源布局（段表三元组同步），页尺寸写实际载荷长度
		binary.LittleEndian.PutUint32(hdr[16:], uint32(ref.startOffset))
	}
	binary.LittleEndian.PutUint32(hdr[20:], 0)
	binary.LittleEndian.PutUint32(hdr[28:], dataCRC)
	hdrCRC := r2004PageChecksum(dataCRC, 0, hdr[:])
	binary.LittleEndian.PutUint32(hdr[24:], hdrCRC)
	mask := uint32(0x4164536B ^ uint32(p.address))
	for i := 0; i < 32; i += 4 {
		v := binary.LittleEndian.Uint32(hdr[i:]) ^ mask
		binary.LittleEndian.PutUint32(out[off+i:], v)
	}
	copy(out[off+r2004PageHdrSize:], p.payload)
}

// ---- R2007 (AC1021) 容器：RS 交织 + R21 压缩页 + 第二头部 ----
//
// 写出与 container_r2007.go 读侧逐点对称（以读侧校验点为准）：
//   - 0x80 起 0x3D8 字节第二头部：RS(239,块=3,method4) 交织区内前 32 字节为
//     (crc64,key64,dataCRC64,comprLen i32,len2)，comprLen<0 表示后续体未压缩
//     （写侧统一取 -0x110），体为 34×u64 的文件头字段（LibreDWG
//     Dwg_R2007_Header 布局，见 r2007HeaderField 下标常量）；
//   - 0x458~0x480 为 0x28 字节间隔区（读侧原样跳过，写侧源样回放）；
//   - 0x480 起为页数据区：页表条目 (size u64,id u64) 序列累加定址，页表与
//     段表是系统页（RS(239) 交织 + 重复 correction 份的块数冗余，内容不压缩）；
//   - 数据页：R21 压缩（压缩流不更短时原样存储）+ RS(251) 交织，物理页尺寸
//     为 align32(块数×255)。段表内每页 7×u64（offset/size/id/uncomp/comp/
//     checksum/crc），size 即页物理尺寸，LibreDWG 以「页表 size == RS 编码
//     形式大小」判定走 RS 路径，与写侧口径绑定。

// r2007HeaderField 第二头部 34×u64 体中写侧需要回填的字段下标（其余保留源值）。
const (
	r2007FieldFileSize     = 1  // 文件总长（LibreDWG VALID_SIZE 校验点）
	r2007FieldPagesMapCorr = 3  // 页表系统页 correction（冗余份数，保留源值）
	r2007FieldPagesMapOff  = 7  // 页表页在页数据区内的偏移（相对 0x480）
	r2007FieldPagesMapComp = 10 // 页表系统页压缩尺寸
	r2007FieldPagesMapUnc  = 11 // 页表系统页解压尺寸
	r2007FieldPagesAmount  = 12 // 页总数
	r2007FieldPagesMaxID   = 13 // 最大页 id
	r2007FieldNumSections  = 20 // 段数声明（读侧按 sectionsAmount-1 截断，保留源值）
	r2007FieldSecMapComp   = 22 // 段表系统页压缩尺寸
	r2007FieldSecMapID     = 24 // 段表页 id（保留源值）
	r2007FieldSecMapUnc    = 25 // 段表系统页解压尺寸
	r2007FieldSecMapCorr   = 27 // 段表系统页 correction（保留源值）
)

// r2007SectionPageRaw 段内页的写出素材：源布局事实（页在段内偏移、页 id、
// 解压/压缩字节数），压缩尺寸随写出重压缩变化（仅捕获时供段装配链使用）。
type r2007SectionPageRaw struct {
	offset     uint64
	id         uint64
	uncompSize uint64
	compSize   uint64
}

// r2007SectionRaw 段的写出素材：段表条目全字段（读侧 r2007SecEntry 会
// 丢弃 max_size/hashcode/unknown，这里独立解析保留）+ 整段解压数据。
type r2007SectionRaw struct {
	name     string
	size     uint64 // 段有效字节数（data size）
	maxSize  uint64 // 段分配上限（max size）
	encoded  uint64 // 页编码方式（写侧统一重写为 4=RS）
	hashcode uint64
	unknown  uint64
	pages    []r2007SectionPageRaw
	data     []byte
}

// r2007RawData R2007 容器的原始写出素材：解析时从源文件保留的头部、第二
// 头部 34 字段、页表顺序与各段解压数据，供 WriteDwgR2007 做文件级回放。
type r2007RawData struct {
	// prefix 文件头 [0x00,0x80)：版本串与缩略图/摘要信息段地址等（写出端
	// 回填段地址）。
	prefix [r2007SecondHeaderOffset]byte
	// headerGap [0x458,0x480) 的 0x28 字节间隔区（读侧跳过，源样回放）。
	headerGap [0x28]byte
	// fields 第二头部解压体 34×u64（写出端回填布局相关字段）。
	fields [34]uint64
	// pageOrder 页表条目按源顺序（含页表/段表自身两页，物理布局顺序）。
	pageOrder []r2007PageSlot
	// sysmapID/secmapID 页表页与段表页的页 id。
	sysmapID int64
	secmapID int64
	// pmCorr/smCorr 页表与段表系统页的 correction 冗余份数（参与块数推导，
	// 保留源值使 RS 去交织块数与读侧口径完全一致）。
	pmCorr uint64
	smCorr uint64
	// sections 各段素材（段表顺序，含尾部空条目）。
	sections []r2007SectionRaw
	// trailingEmpty 段表尾部是否存在空条目（data_size/name_length/
	// num_pages 全零）：LibreDWG 对其报 "Invalid num_pages 0"（源文件基线
	// 错误的一部分），写侧重建段表时原样补回以保持基线一致。
	trailingEmpty bool
}

// r2007DecodeHeaderBody 解出第二头部 34×u64 字段体（captureR2007Raw 专用，
// 与读侧 decodeR2007Header 同链但保留全量字段）。
func r2007DecodeHeaderBody(data []byte) ([34]uint64, error) {
	var fields [34]uint64
	if len(data) < r2007SecondHeaderOffset+r2007SecondHeaderRSSize {
		return fields, fmt.Errorf("cad: 文件过小，缺少 R2007 第二头部")
	}
	decoded, err := r2007Deinterleave(data[r2007SecondHeaderOffset:r2007SecondHeaderOffset+r2007SecondHeaderRSSize], 239, 3, 4)
	if err != nil {
		return fields, err
	}
	comprLen := int64(int32(binary.LittleEndian.Uint32(decoded[24:])))
	var body []byte
	switch {
	case comprLen < 0:
		size := int(-comprLen)
		if r2007SecondHeaderPayload+size > len(decoded) || size < r2007SecondHeaderBodySize {
			return fields, fmt.Errorf("cad: R2007 第二头部体越界")
		}
		body = decoded[r2007SecondHeaderPayload : r2007SecondHeaderPayload+size]
	case comprLen > 0:
		if r2007SecondHeaderPayload+int(comprLen) > len(decoded) {
			return fields, fmt.Errorf("cad: R2007 第二头部压缩体越界")
		}
		body, err = decompressR21(decoded[r2007SecondHeaderPayload:r2007SecondHeaderPayload+int(comprLen)], r2007SecondHeaderBodySize)
		if err != nil {
			return fields, err
		}
	default:
		return fields, fmt.Errorf("cad: R2007 第二头部 compressedSize 为 0")
	}
	if len(body) < r2007SecondHeaderBodySize {
		return fields, fmt.Errorf("cad: R2007 第二头部体截断")
	}
	for i := range fields {
		fields[i] = binary.LittleEndian.Uint64(body[i*8:])
	}
	return fields, nil
}

// r2007CaptureSectionMap 解析段表系统页并保留条目全字段（8×u64 头 + UTF-16
// 段名 + 每页 7×u64 中的布局事实），读侧同名解析会丢弃 max_size 等字段。
// 第二返回值表示段表尾部是否存在空条目。
func r2007CaptureSectionMap(data []byte, hdr r2007Header, pages []r2007PageSlot) ([]r2007SectionRaw, bool, error) {
	trailingEmpty := false
	var secMapPage *r2007PageSlot
	for i := range pages {
		if pages[i].id == int64(hdr.sectionsMapID) {
			secMapPage = &pages[i]
			break
		}
	}
	if secMapPage == nil {
		return nil, false, fmt.Errorf("cad: R2007 段表页不存在（id=%d）", hdr.sectionsMapID)
	}
	sysData, err := readR2007SystemPage(data, secMapPage.address, hdr.sectionsMapSizeCompressed,
		hdr.sectionsMapSizeUncompressed, hdr.sectionsMapCorrectionFactor)
	if err != nil {
		return nil, false, err
	}
	var sections []r2007SectionRaw
	pos := 0
	for len(sysData)-pos >= r2007SectionEntrySize {
		read := func() uint64 {
			v := binary.LittleEndian.Uint64(sysData[pos:])
			pos += 8
			return v
		}
		size := read()
		maxSize := read()
		read() // encrypted：数据页读取流程与其无关，不保留
		hashcode := read()
		nameLength := read()
		unknown := read()
		encoded := read()
		pageCount := read()
		if size == 0 && pageCount == 0 && nameLength == 0 {
			// 段表尾部空条目：本包读侧视为终止符，LibreDWG 报
			// "Invalid num_pages 0"（基线错误），捕获标志供写侧补回
			trailingEmpty = true
			break
		}
		if nameLength > uint64(len(sysData)-pos) {
			return nil, false, fmt.Errorf("cad: R2007 段名超长 %d", nameLength)
		}
		name := decodeUTF16LE(sysData[pos : pos+int(nameLength)])
		if i := indexByteStr(name, 0); i >= 0 {
			name = name[:i]
		}
		pos += int(nameLength)
		sec := r2007SectionRaw{
			name:     name,
			size:     size,
			maxSize:  maxSize,
			encoded:  encoded,
			hashcode: hashcode,
			unknown:  unknown,
		}
		for i := uint64(0); i < pageCount; i++ {
			if len(sysData)-pos < r2007SectionPageSize {
				return nil, false, fmt.Errorf("cad: R2007 段页信息截断")
			}
			sec.pages = append(sec.pages, r2007SectionPageRaw{
				offset:     binary.LittleEndian.Uint64(sysData[pos:]),
				id:         binary.LittleEndian.Uint64(sysData[pos+16:]),
				uncompSize: binary.LittleEndian.Uint64(sysData[pos+24:]),
				compSize:   binary.LittleEndian.Uint64(sysData[pos+32:]),
			})
			pos += r2007SectionPageSize
		}
		sections = append(sections, sec)
	}
	if len(sections) == 0 {
		return nil, false, fmt.Errorf("cad: R2007 段表为空")
	}
	return sections, trailingEmpty, nil
}

// captureR2007Raw 从 R2007 源文件提取回放素材（解析路径的一次性钩子）：
// 走与读侧完全相同的解析链（第二头部→页表→段表→逐段装配），任一环节失败
// 返回 nil（不阻断解析，仅失去文件级写出能力）。
func captureR2007Raw(data []byte) *r2007RawData {
	if len(data) < 6 || string(data[:6]) != "AC1021" || uint64(len(data)) < r2007StreamBaseOffset {
		return nil
	}
	hdr, err := decodeR2007Header(data)
	if err != nil {
		return nil
	}
	pages, err := parseR2007PageMap(data, hdr)
	if err != nil {
		return nil
	}
	fields, err := r2007DecodeHeaderBody(data)
	if err != nil {
		return nil
	}
	sections, trailingEmpty, err := r2007CaptureSectionMap(data, hdr, pages)
	if err != nil {
		return nil
	}
	raw := &r2007RawData{
		fields:        fields,
		pageOrder:     append([]r2007PageSlot(nil), pages...),
		sysmapID:      int64(hdr.sectionsMapID),
		secmapID:      int64(hdr.sectionsMapID),
		pmCorr:        hdr.pagesMapCorrectionFactor,
		smCorr:        hdr.sectionsMapCorrectionFactor,
		sections:      sections,
		trailingEmpty: trailingEmpty,
	}
	// 页表页 id：地址为 0x480+pagesMapOffset 的页（fields[7] 源样保留，
	// 该页在新布局中仍按页表条目定位）
	sysAddr := r2007StreamBaseOffset + hdr.pagesMapOffset
	for _, p := range pages {
		if p.address == sysAddr {
			raw.sysmapID = p.id
			break
		}
	}
	copy(raw.prefix[:], data[:r2007SecondHeaderOffset])
	if uint64(len(data)) >= r2007StreamBaseOffset {
		copy(raw.headerGap[:], data[0x458:0x480])
	}
	lookup := make(map[int64]r2007PageSlot, len(pages))
	for _, p := range pages {
		lookup[p.id] = p
	}
	for i := range sections {
		entry := r2007SecEntry{size: sections[i].size, encoded: sections[i].encoded, name: sections[i].name}
		for _, p := range sections[i].pages {
			entry.pages = append(entry.pages, r2007PageInfo{
				offset: p.offset, id: p.id, sizeUncompressed: p.uncompSize, sizeCompressed: p.compSize,
			})
		}
		blob, err := assembleR2007Section(data, &entry, pages)
		if err != nil {
			return nil // 段装配失败：素材不完整，放弃回放
		}
		sections[i].data = blob
	}
	raw.sections = sections
	return raw
}

// r2007EncodeRS RS 交织（读侧 r2007Deinterleave method4 的逆）：把 pedata 按
// k 字节一块转置铺列，src[blockCount*idx+bc] = pedata[bc*k+idx]，去交织即
// 还原。输出长度恰为块数×k（物理页再补零到块数×255 并对齐）。
func r2007EncodeRS(pedata []byte, k, blockCount int) []byte {
	out := make([]byte, blockCount*k)
	for bc := 0; bc < blockCount; bc++ {
		for idx := 0; idx < k; idx++ {
			out[blockCount*idx+bc] = pedata[bc*k+idx]
		}
	}
	return out
}

// r2007EncodeSystemPage 编码一个系统页（页表/段表）：内容不压缩
// （size_comp=size_uncomp，读侧走 memcpy 分支），RS(239) 交织，块数按
// correction 份冗余推导（与读侧 readR2007SystemPage 口径一致）。返回物理页
// 字节与压缩/解压尺寸。
func r2007EncodeSystemPage(content []byte, correction uint64) (phys []byte, sizeComp, sizeUncomp uint64) {
	sizeUncomp = uint64(len(content))
	sizeComp = sizeUncomp
	pesize := alignUp(sizeComp, r2007SysPageCRCBlock) * correction
	blockCount := int(divCeil(pesize, r2007SysPageRSDataSize))
	if blockCount <= 0 {
		blockCount = 1
	}
	pedata := make([]byte, blockCount*int(r2007SysPageRSDataSize))
	copy(pedata, content)
	phys = r2007PadPage(r2007EncodeRS(pedata, int(r2007SysPageRSDataSize), blockCount), blockCount, int(r2007SysPageRSCodeWord))
	return phys, sizeComp, sizeUncomp
}

// r2007DataPagePhysSize 数据页物理尺寸：align32(块数×255)，块数按压缩尺寸
// 推导（读侧 r2007DataPageBlocks 同口径）。空载荷按 1 块兜底，避免
// 页表出现 size=0 的非法条目。
func r2007DataPagePhysSize(sizeComp uint64) uint64 {
	blocks := r2007DataPageBlocks(sizeComp)
	if blocks == 0 {
		blocks = 1
	}
	return alignUp(blocks*r2007SysPageRSCodeWord, r2007SysPageAlign)
}

// r2007EncodeDataPage 编码一个数据页：RS(251) 交织后补零到块数×255 并对齐
// 0x20。payload 为压缩（或原样）页内容。
func r2007EncodeDataPage(payload []byte) []byte {
	blocks := int(r2007DataPageBlocks(uint64(len(payload))))
	if blocks <= 0 {
		blocks = 1
	}
	pedata := make([]byte, blocks*251)
	copy(pedata, payload)
	return r2007PadPage(r2007EncodeRS(pedata, 251, blocks), blocks, 255)
}

// r2007PadPage 把交织输出补零到块数×码字长度并向上对齐 0x20（物理页尺寸）。
func r2007PadPage(body []byte, blockCount, codeWord int) []byte {
	total := alignUp(uint64(blockCount*codeWord), r2007SysPageAlign)
	out := make([]byte, total)
	copy(out, body)
	return out
}

// r2007PlacedPage 一个已定址待铺放页：kind 区分数据页/段表页/页表页。
type r2007PlacedPage struct {
	id      int64
	address uint64
	size    uint64
	kind    int // 0=数据页 1=段表页 2=页表页
	payload []byte
}

// WriteDwgR2007 将 R2007（AC1021）文档写出为 DWG 字节流。文档必须来自
// Parse（内部保留回放素材）。布局：头部 0x80 字节（回填段地址）→ RS 交织
// 的第二头部（34 字段回填后未压缩存储）→ 0x28 间隔区 → 页数据区（页表页、
// 段表页与各数据页，数据页按 compressR21 重压缩 + RS 交织）。对象记录字节
// 随 AcDb:AcDbObjects / AcDb:Handles 段回放，句柄偏移不变。
func WriteDwgR2007(doc *Document, w io.Writer) error {
	if doc == nil || doc.r2007Raw == nil {
		return fmt.Errorf("cad: 非 R2007 文档或缺少回放素材，无法写出")
	}
	out, err := writeR2007Sections(doc.r2007Raw)
	if err != nil {
		return err
	}
	_, err = w.Write(out)
	return err
}

// writeR2007Sections 按新布局组装 R2007 文件字节流。
func writeR2007Sections(raw *r2007RawData) ([]byte, error) {
	// ① 数据页重压缩 + RS 交织，并逐页自检（压缩结果经自家解压器还原必须
	//    逐字节一致，失败即拒绝写出）；页内容按源页偏移/解压尺寸切片
	payloads := make(map[uint64][]byte, 64)
	for si := range raw.sections {
		sec := &raw.sections[si]
		for pi, page := range sec.pages {
			start := page.offset
			if start > sec.size {
				start = sec.size
			}
			end := page.offset + page.uncompSize
			if end > sec.size {
				end = sec.size
			}
			chunk := sec.data[start:end]
			comp := compressR21(chunk)
			if len(comp) >= len(chunk) {
				// 压不短则原样存储：读侧仅按 comp_size<uncomp_size 判定解压，
				// 原样页（comp==uncomp）走直拷分支，不做 R21 自检
				comp = append([]byte(nil), chunk...)
			} else if len(chunk) > 0 {
				back, err := decompressR21(comp, len(chunk))
				if err != nil || !bytes.Equal(back, chunk) {
					return nil, fmt.Errorf("cad: 段 %q 第 %d 页压缩自检失败", sec.name, pi)
				}
			}
			payloads[page.id] = comp
		}
	}
	// ② 段表内容重建：段表条目全字段 + 段名 + 每页 7×u64（页 size 写物理
	//    尺寸，LibreDWG 以此判定 RS 路径；checksum/crc 读侧不校验写 0）
	buildSectionMap := func() []byte {
		buf := make([]byte, 0, 4096)
		putName := func(name string) {
			for _, r := range name {
				buf = binary.LittleEndian.AppendUint16(buf, uint16(r))
			}
			buf = binary.LittleEndian.AppendUint16(buf, 0) // NUL 终止
		}
		for si := range raw.sections {
			sec := &raw.sections[si]
			encoded := uint64(4) // 写侧统一 RS 编码
			if len(sec.pages) == 0 {
				encoded = sec.encoded
			}
			buf = binary.LittleEndian.AppendUint64(buf, sec.size)
			buf = binary.LittleEndian.AppendUint64(buf, sec.maxSize)
			buf = binary.LittleEndian.AppendUint64(buf, 0) // encrypted
			buf = binary.LittleEndian.AppendUint64(buf, 0) // hashcode
			buf = binary.LittleEndian.AppendUint64(buf, uint64((len(sec.name)+1)*2))
			buf = binary.LittleEndian.AppendUint64(buf, sec.unknown)
			buf = binary.LittleEndian.AppendUint64(buf, encoded)
			buf = binary.LittleEndian.AppendUint64(buf, uint64(len(sec.pages)))
			putName(sec.name)
			for _, page := range sec.pages {
				compLen := uint64(len(payloads[page.id]))
				buf = binary.LittleEndian.AppendUint64(buf, page.offset)                    // 页在段内偏移
				buf = binary.LittleEndian.AppendUint64(buf, r2007DataPagePhysSize(compLen)) // 页物理尺寸
				buf = binary.LittleEndian.AppendUint64(buf, page.id)                        // 页 id
				buf = binary.LittleEndian.AppendUint64(buf, page.uncompSize)                // 解压尺寸
				buf = binary.LittleEndian.AppendUint64(buf, compLen)                        // 压缩尺寸
				buf = binary.LittleEndian.AppendUint64(buf, 0)                              // checksum
				buf = binary.LittleEndian.AppendUint64(buf, 0)                              // crc64
			}
		}
		if raw.trailingEmpty {
			// 源段表尾部的空条目原样补回（基线行为对齐，见 r2007RawData 注释）
			for i := 0; i < 8; i++ {
				buf = binary.LittleEndian.AppendUint64(buf, 0)
			}
		}
		return buf
	}
	smContent := buildSectionMap()
	smPhys, smComp, smUncomp := r2007EncodeSystemPage(smContent, raw.smCorr)
	// ③ 页表内容重建（页表页自身条目尺寸自引用，迭代收敛）
	sectionMapSize := uint64(len(smPhys))
	buildPageMap := func(sysmapSize uint64) []byte {
		buf := make([]byte, 0, (len(raw.pageOrder)+1)*16)
		for _, p := range raw.pageOrder {
			var size uint64
			switch {
			case p.id == raw.sysmapID:
				size = sysmapSize
			case p.id == raw.secmapID:
				size = sectionMapSize
			default:
				// 数据页与孤立页（如页表副本页 pages_map2，无段引用）：
				// 孤立页载荷为空，按最小零页占位，保证条目 size 非零
				size = r2007DataPagePhysSize(uint64(len(payloads[uint64(p.id)])))
			}
			buf = binary.LittleEndian.AppendUint64(buf, size)
			buf = binary.LittleEndian.AppendUint64(buf, uint64(p.id))
		}
		// 终止对（读侧遇 size=0 且 id=0 停止）
		buf = binary.LittleEndian.AppendUint64(buf, 0)
		buf = binary.LittleEndian.AppendUint64(buf, 0)
		return buf
	}
	sysmapSize := uint64(r2007SysPageAlign)
	var pmContent []byte
	for iter := 0; ; iter++ {
		pmContent = buildPageMap(sysmapSize)
		_, _, unc := r2007EncodeSystemPage(pmContent, raw.pmCorr)
		blocks := int(divCeil(alignUp(unc, r2007SysPageCRCBlock)*raw.pmCorr, r2007SysPageRSDataSize))
		if blocks <= 0 {
			blocks = 1
		}
		next := alignUp(uint64(blocks)*r2007SysPageRSCodeWord, r2007SysPageAlign)
		if next == sysmapSize {
			break
		}
		sysmapSize = next
		if iter >= 32 {
			return nil, fmt.Errorf("cad: 页表自引用尺寸不收敛")
		}
	}
	pmPhys, pmComp, pmUncomp := r2007EncodeSystemPage(pmContent, raw.pmCorr)
	// ④ 定址：0x480 起按源页表顺序铺放，页地址 = 页表条目累计
	placed := make([]r2007PlacedPage, 0, len(raw.pageOrder))
	address := r2007StreamBaseOffset
	for _, p := range raw.pageOrder {
		pp := r2007PlacedPage{id: p.id, address: address}
		switch {
		case p.id == raw.sysmapID:
			pp.kind, pp.size, pp.payload = 2, uint64(len(pmPhys)), pmPhys
		case p.id == raw.secmapID:
			pp.kind, pp.size, pp.payload = 1, uint64(len(smPhys)), smPhys
		default:
			pl := payloads[uint64(p.id)] // 孤立页无载荷：铺最小零页占位
			pp.kind, pp.size, pp.payload = 0, r2007DataPagePhysSize(uint64(len(pl))), r2007EncodeDataPage(pl)
		}
		placed = append(placed, pp)
		address += pp.size
	}
	total := address
	// ⑤ 第二头部 34 字段回填（comprLen=-0x110 未压缩存储体）
	fields := raw.fields
	fields[r2007FieldFileSize] = total
	fields[r2007FieldPagesMapComp] = pmComp
	fields[r2007FieldPagesMapUnc] = pmUncomp
	fields[r2007FieldPagesAmount] = uint64(len(placed))
	maxID := int64(0)
	for _, p := range placed {
		if p.id > maxID {
			maxID = p.id
		}
	}
	fields[r2007FieldPagesMaxID] = uint64(maxID)
	fields[r2007FieldSecMapComp] = smComp
	fields[r2007FieldSecMapUnc] = smUncomp
	// header2_offset（idx 9）回填第二头部区起点：源值指向旧文件的第二头部
	// 副本偏移，随重布局失效；LibreDWG 对该字段做 VALID_SIZE 越界校验，
	// 超出新文件大小会直接中断元数据读取（num_objects 归零）
	fields[9] = uint64(r2007SecondHeaderOffset)
	// 页表页数据区偏移 = 页表页新地址 - 0x480
	pmAddr := r2007PageAddressR2007(placed, raw.sysmapID)
	fields[r2007FieldPagesMapOff] = pmAddr - r2007StreamBaseOffset
	// ⑥ 文件体组装：头部 + 第二头部 + 间隔区 + 各页
	out := make([]byte, total)
	copy(out, raw.prefix[:])
	// 头部段地址回填：Preview/SummaryInfo/VBAProject 首页（读侧按非零判定
	// 是否加载对应段；段缺失写 0）
	patchAddr := func(name string, at int) {
		for si := range raw.sections {
			sec := &raw.sections[si]
			if sec.name != name || len(sec.pages) == 0 {
				continue
			}
			binary.LittleEndian.PutUint32(out[at:], uint32(r2007PageAddressR2007(placed, int64(sec.pages[0].id))))
			return
		}
		binary.LittleEndian.PutUint32(out[at:], 0)
	}
	patchAddr("AcDb:Preview", 0x11)
	patchAddr("AcDb:SummaryInfo", 0x24)
	patchAddr("AcDb:VBAProject", 0x28)
	// 第二头部：32 字节头 + 34×u64 体。体统一走 R21 压缩存储（comprLen
	// 为正数）：本包读侧把 comprLen 按 u32→int64 解释，负长度分支不可达，
	// 且压缩流上界（约体长 + 8%）远小于 RS 区 685 字节容量，两读侧一致。
	body := make([]byte, 0, r2007SecondHeaderBodySize)
	for _, f := range fields {
		body = binary.LittleEndian.AppendUint64(body, f)
	}
	compBody := compressR21(body)
	if back, err := decompressR21(compBody, r2007SecondHeaderBodySize); err != nil || !bytes.Equal(back, body) {
		return nil, fmt.Errorf("cad: R2007 第二头部体压缩自检失败")
	}
	head := make([]byte, r2007SecondHeaderPayload)
	binary.LittleEndian.PutUint32(head[24:], uint32(len(compBody)))
	pedata := make([]byte, 3*int(r2007SysPageRSDataSize))
	copy(pedata, head)
	copy(pedata[r2007SecondHeaderPayload:], compBody)
	copy(out[r2007SecondHeaderOffset:], r2007EncodeRS(pedata, int(r2007SysPageRSDataSize), 3))
	copy(out[0x458:], raw.headerGap[:])
	for _, p := range placed {
		copy(out[p.address:], p.payload)
	}
	return out, nil
}

// r2007PageAddressR2007 按页 id 查已定址页的物理地址（未找到返回 0）。
func r2007PageAddressR2007(placed []r2007PlacedPage, id int64) uint64 {
	for _, p := range placed {
		if p.id == id {
			return p.address
		}
	}
	return 0
}
