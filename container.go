// container.go 实现 DWG R2004+（AC1018~AC1032）物理容器与 R2000（AC1015）
// 段目录的解析：加密容器头、页表（page map）、段表（section map）、数据页
// 解密解压与段数据装配。
//
// 模块划分：版本识别（detectVersion/verString）、R2004+ 头解密
// （decryptR2004Header/lcgKeyStream）、系统段解压（inflateSystemSection）、
// 页表/段表解析（parsePageMap/parseSectionMap）、段装配（assembleSection）、
// R2000 段目录（parseR2000Directory/readR2000Section）。
package cad

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

const (
	headerOffset         = 0x80 // R2004+ 加密头起始偏移
	headerSize           = 0x6C // 加密头长度
	sectionPageMapMagic  = 0x41630E3B
	sectionMapMagic      = 0x4163003B
	dataSectionMagic     = 0x4163043B
	systemSectionHdrSize = 0x14 // system section 头长度：4 个 u32 + crc
)

var (
	// sentinelClassesBefore/After AcDb:Classes 段前后哨兵字节。
	sentinelClassesBefore = [16]byte{0x8D, 0xA1, 0xC4, 0xB8, 0xC4, 0xA9, 0xF8, 0xC5, 0xC0, 0xDC, 0xF4, 0x5F, 0xE7, 0xCF, 0xB6, 0x8A}
	sentinelClassesAfter  = [16]byte{0x72, 0x5E, 0x3B, 0x47, 0x3B, 0x56, 0x07, 0x3A, 0x3F, 0x23, 0x0B, 0xA0, 0x18, 0x30, 0x49, 0x75}
)

// r2004Header R2004+ 加密头中与容器定位相关的字段。
type r2004Header struct {
	sectionPageMapAddress uint64
	sectionMapID          uint32
}

// pageSlot 页表项：id 为页编号（<0 为空洞），address 为页在文件中的物理地址。
type pageSlot struct {
	id      int32
	address uint64
}

// secEntry 段表项：一个逻辑段由若干页组成。
type secEntry struct {
	size              uint64
	maxDecompressedSz uint32
	compressed        uint32
	encrypted         uint32
	name              string
	pageIDs           []uint32
}

// dwgVersion DWG 版本枚举（按解码路径分组）。
type dwgVersion int

const (
	verR2000 dwgVersion = iota // AC1015
	verR14                     // AC1014（容器同 R2000，实体头/几何为 R13/R14 布局）
	verR13                     // AC1012（早于 R13c3：DICTIONARY 无 is_hardowner 等）
	verR2004                   // AC1018（含同容器路径的更高版本）
	verR2007                   // AC1021（独立容器：RS 去交织 + R21 解压）
	verR2010                   // AC1024
	verR2013                   // AC1027
	verR2018                   // AC1032
	verR9                      // AC1004（pre-R13 家族：固定偏移表驱动字节布局）
	verR10                     // AC1006（pre-R13：+UCS/VPORT 表）
	verR11                     // AC1009（pre-R13：+sentinel/CRC 包夹与 APPID/DIMSTYLE/VX 表）
)

// versionMagic 魔数 → 版本对照表（detectVersion 与 verString 共用一套映射，
// 表中缺省版本默认回落 AC1032）。
var versionMagic = map[dwgVersion]string{
	verR2000: "AC1015",
	verR14:   "AC1014",
	verR13:   "AC1012",
	verR2004: "AC1018",
	verR2007: "AC1021",
	verR2010: "AC1024",
	verR2013: "AC1027",
	verR2018: "AC1032",
	verR9:    "AC1004",
	verR10:   "AC1006",
	verR11:   "AC1009",
}

// verString 版本串。
func (v dwgVersion) verString() string {
	if s, ok := versionMagic[v]; ok {
		return s
	}
	return "AC1032"
}

// preR13 是否 pre-R13 家族（字节对齐固定偏移布局，与 R13+ 位流容器完全不同）。
func (v dwgVersion) preR13() bool { return v == verR9 || v == verR10 || v == verR11 }

// r2010Plus 对象记录是否带 UMC/OT 类型码前缀。
func (v dwgVersion) r2010Plus() bool { return v >= verR2010 }

// detectVersion 从文件头 6 字节魔数识别 DWG 版本。
func detectVersion(data []byte) (dwgVersion, error) {
	if len(data) < 6 {
		return 0, fmt.Errorf("cad: 文件过小，不是合法 DWG")
	}
	magic := string(data[:6])
	for ver, name := range versionMagic {
		if name == magic {
			return ver, nil
		}
	}
	return 0, fmt.Errorf("cad: 不支持的 DWG 版本 %q（支持 AC1004/AC1006/AC1009/AC1014/AC1015/AC1018/AC1021/AC1024/AC1027/AC1032）", magic)
}

// readCodepage 读取文件头 0x13 处的 2 字节 codepage 编号（过短文件按 30 兜底）。
func readCodepage(data []byte) uint16 {
	if len(data) < 0x15 {
		return 30
	}
	return binary.LittleEndian.Uint16(data[0x13:])
}

// lcgKeyStream 生成与 DWG 写出端一致的 LCG 伪随机字节序列
// （seed=1，x*=0x343FD，x+=0x269EC3，取 >>16 字节）。
func lcgKeyStream() [headerSize]byte {
	var seq [headerSize]byte
	var seed uint32 = 1
	for i := range seq {
		seed = seed*0x343FD + 0x269EC3
		seq[i] = byte(seed >> 16)
	}
	return seq
}

// decryptR2004Header 解密 0x80 起 0x6C 字节的 R2004+ 头，提取页表地址与
// 段表页 id：密文逐字节与 LCG 密钥流异或，随后按固定偏移取两个字段。
func decryptR2004Header(data []byte) (r2004Header, error) {
	if len(data) < headerOffset+headerSize {
		return r2004Header{}, fmt.Errorf("cad: 文件过小，缺少 R2004+ 容器头")
	}
	keys := lcgKeyStream()
	plain := make([]byte, headerSize)
	for i := range plain {
		plain[i] = data[headerOffset+i] ^ keys[i]
	}
	if len(plain) < 0x50+24 {
		return r2004Header{}, fmt.Errorf("cad: R2004+ 容器头字段越界")
	}
	return r2004Header{
		sectionPageMapAddress: binary.LittleEndian.Uint64(plain[0x54:]),
		sectionMapID:          binary.LittleEndian.Uint32(plain[0x5C:]),
	}, nil
}

// inflateSystemSection 读取并解压一个 system section（页表/段表所在的
// 压缩结构）：校验签名后按压缩类型分流——0x01 原样存储（截齐声明尺寸），
// 0x02 走 LZ77；其余类型拒绝。
func inflateSystemSection(data []byte, address uint64, expectSignature uint32) ([]byte, error) {
	offset := int(address)
	// 垃圾地址（如 R2000 文件误入 R2004+ 容器路径）经 int 截断可能为负，
	// 必须与上界一并检查，避免 data[offset:] 负索引 panic
	if offset < 0 || offset+systemSectionHdrSize > len(data) {
		return nil, fmt.Errorf("cad: system section 头越界")
	}
	signature := binary.LittleEndian.Uint32(data[offset:])
	plainSize := binary.LittleEndian.Uint32(data[offset+4:])
	packedSize := binary.LittleEndian.Uint32(data[offset+8:])
	codec := binary.LittleEndian.Uint32(data[offset+12:])
	if signature != expectSignature {
		return nil, fmt.Errorf("cad: system section 签名不匹配 0x%08X（期望 0x%08X）", signature, expectSignature)
	}
	if packedSize == 0 {
		return nil, nil
	}
	start := offset + systemSectionHdrSize
	packed := data[start : start+int(packedSize)]
	switch codec {
	case 0x01:
		// store：载荷原样存储（写端页表页的 store 策略与部分源文件的
		// 未压缩段），长度截齐解压目标尺寸
		if int(plainSize) > len(packed) {
			return nil, fmt.Errorf("cad: system section store 长度不足")
		}
		return packed[:plainSize], nil
	case 0x02:
		return decompressLZ77(packed, int(plainSize))
	default:
		return nil, fmt.Errorf("cad: 不支持的 system section 压缩类型 0x%X", codec)
	}
}

// parsePageMap 解析页表：内容为 (id i32, size u32) 序列，页地址自 0x100 起
// 按 size 累加；id<0 表示空洞，条目后跟 16 字节空洞描述一并跳过。
func parsePageMap(data []byte, header r2004Header) ([]pageSlot, error) {
	blob, err := inflateSystemSection(data, header.sectionPageMapAddress+0x100, sectionPageMapMagic)
	if err != nil {
		return nil, err
	}
	var slots []pageSlot
	var pageAddress uint64 = 0x100
	for rest := blob; len(rest) >= 8; {
		id := int32(binary.LittleEndian.Uint32(rest))
		pageSize := binary.LittleEndian.Uint32(rest[4:])
		rest = rest[8:]
		slots = append(slots, pageSlot{id: id, address: pageAddress})
		pageAddress += uint64(pageSize)
		if id < 0 {
			if len(rest) < 16 {
				return nil, fmt.Errorf("cad: 页表 gap 条目截断")
			}
			rest = rest[16:]
		}
	}
	return slots, nil
}

// parseSectionMap 解析段表：按段表页 id 定位页，读取段描述与各页 id。
// 段条目 96 字节：size u64、page_count u32、max_decompressed u32、unknown u32、
// compressed u32、section_id u32、encrypted u32、name[64]；每页描述 16 字节
// （page_id u32、data_size u32、start_offset u64）。
func parseSectionMap(data []byte, header r2004Header, pages []pageSlot) ([]secEntry, error) {
	mapPage := findPageByID(pages, int32(header.sectionMapID))
	if mapPage == nil {
		return nil, fmt.Errorf("cad: 页表中找不到段表页 id=%d", header.sectionMapID)
	}
	blob, err := inflateSystemSection(data, mapPage.address, sectionMapMagic)
	if err != nil {
		return nil, err
	}
	if len(blob) < 20 {
		return nil, fmt.Errorf("cad: 段表头截断")
	}
	entryCount := int(binary.LittleEndian.Uint32(blob))
	sections := make([]secEntry, 0, entryCount)
	rest := blob[20:]
	for i := 0; i < entryCount; i++ {
		if len(rest) < 96 {
			return nil, fmt.Errorf("cad: 段表条目截断")
		}
		desc := rest[:96]
		rest = rest[96:]
		pageCount := int(binary.LittleEndian.Uint32(desc[8:]))
		entry := secEntry{
			size:              binary.LittleEndian.Uint64(desc),
			maxDecompressedSz: binary.LittleEndian.Uint32(desc[12:]),
			compressed:        binary.LittleEndian.Uint32(desc[20:]),
			encrypted:         binary.LittleEndian.Uint32(desc[28:]),
			name:              readCString(desc[32:96]),
		}
		for p := 0; p < pageCount; p++ {
			if len(rest) < 16 {
				return nil, fmt.Errorf("cad: 段页描述截断")
			}
			entry.pageIDs = append(entry.pageIDs, binary.LittleEndian.Uint32(rest))
			rest = rest[16:]
		}
		sections = append(sections, entry)
	}
	return sections, nil
}

// findPageByID 在页表中查找指定 id 的页（未命中返回 nil）。
func findPageByID(pages []pageSlot, id int32) *pageSlot {
	for i := range pages {
		if pages[i].id == id {
			return &pages[i]
		}
	}
	return nil
}

// loadSectionPage 读取并解压段内单个数据页：解密页头、校验签名，按段
// 压缩方法返回页内容（codec 2 走 LZ77，解压目标尺寸用段的
// maxDecompressedSz——页头字段并非该值）。
func loadSectionPage(data []byte, slot pageSlot, sec *secEntry) ([]byte, error) {
	pageOffset := int(slot.address)
	if pageOffset+32 > len(data) {
		return nil, fmt.Errorf("cad: 数据页头越界")
	}
	header := unmaskPageHeader(data[pageOffset:pageOffset+32], slot.address)
	if sig := binary.LittleEndian.Uint32(header); sig != dataSectionMagic {
		return nil, fmt.Errorf("cad: 数据页签名不匹配 0x%08X", sig)
	}
	packedSize := int(binary.LittleEndian.Uint32(header[8:]))
	dataStart := pageOffset + 32
	dataEnd := dataStart + packedSize
	if dataEnd > len(data) || dataStart > dataEnd {
		return nil, fmt.Errorf("cad: 数据页内容越界")
	}
	packed := data[dataStart:dataEnd]
	if sec.compressed == 2 {
		return decompressLZ77(packed, int(sec.maxDecompressedSz))
	}
	return packed, nil
}

// assembleSection 按页表装配一个逻辑段的完整数据：逐页解密解压拼接，
// 末页填充截齐——先按 pageSize×页数封顶，再按段声明 size 截断。
func assembleSection(data []byte, section *secEntry, pageLookup map[uint32]pageSlot) ([]byte, error) {
	if section.encrypted == 1 {
		return nil, fmt.Errorf("cad: 暂不支持加密段 %q", section.name)
	}
	pageSize := int(section.maxDecompressedSz)
	capacity := pageSize * len(section.pageIDs)
	if capacity == 0 {
		return nil, nil
	}
	out := make([]byte, 0, capacity)
	for _, pageID := range section.pageIDs {
		slot, ok := pageLookup[pageID]
		if !ok {
			return nil, fmt.Errorf("cad: 页表中找不到页 id=%d（段 %q）", pageID, section.name)
		}
		pageData, err := loadSectionPage(data, slot, section)
		if err != nil {
			return nil, err
		}
		out = append(out, pageData...)
	}
	if len(out) > capacity {
		out = out[:capacity]
	}
	if declared := int(section.size); declared > 0 && declared <= len(out) {
		out = out[:declared]
	}
	return out, nil
}

// unmaskPageHeader 解密 32 字节数据页头：按 4 字节分块与 0x4164536B^页地址
// 异或。
func unmaskPageHeader(b []byte, pageAddress uint64) []byte {
	out := make([]byte, 32)
	copy(out, b[:32])
	mask := uint32(0x4164536B ^ uint32(pageAddress))
	for i := 0; i+4 <= 32; i += 4 {
		binary.LittleEndian.PutUint32(out[i:], binary.LittleEndian.Uint32(out[i:])^mask)
	}
	return out
}

// pageLookupTable 由页表构建 id→页 的查询表（仅收录 id>0 的有效页）。
func pageLookupTable(pages []pageSlot) map[uint32]pageSlot {
	lookup := make(map[uint32]pageSlot, len(pages))
	for _, p := range pages {
		if p.id > 0 {
			lookup[uint32(p.id)] = p
		}
	}
	return lookup
}

// loadNamedSectionData 按名称（如 "AcDb:AcDbObjects"）加载段的完整解压数据。
// R2007（AC1021）容器结构独立，单独路由。
func loadNamedSectionData(data []byte, name string) ([]byte, error) {
	if len(data) >= 6 && string(data[:6]) == "AC1021" {
		return loadNamedSectionDataR2007(data, name)
	}
	header, err := decryptR2004Header(data)
	if err != nil {
		return nil, err
	}
	pages, err := parsePageMap(data, header)
	if err != nil {
		return nil, err
	}
	sections, err := parseSectionMap(data, header, pages)
	if err != nil {
		return nil, err
	}
	for i := range sections {
		if sections[i].name == name {
			return assembleSection(data, &sections[i], pageLookupTable(pages))
		}
	}
	return nil, fmt.Errorf("cad: 段 %q 不存在", name)
}

// readCString 读取 NUL 结尾的字节串。
func readCString(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		return string(b[:i])
	}
	return string(b)
}

// ---- R2000 (AC1015) 容器：段目录在 0x15，段数据不压缩直接切片 ----

// r2000Locator R2000 段定位记录。
type r2000Locator struct {
	recordNo uint8
	offset   uint32
	size     uint32
}

var r2000LocatorSentinel = [16]byte{0x95, 0xA0, 0x4E, 0x28, 0x99, 0x82, 0x1A, 0xE5, 0x5E, 0x41, 0xE0, 0x5F, 0x9D, 0x3A, 0x4D, 0x00}

// parseR2000Directory 解析 R2000 段目录：0x15 处 u32 条目数，每条
// record_no(u8)+offset(u32)+size(u32)，尾部 CRC 与哨兵。
func parseR2000Directory(data []byte) ([]r2000Locator, error) {
	if len(data) < 0x15+4 {
		return nil, fmt.Errorf("cad: 文件过小，缺少 R2000 段目录")
	}
	count := int(binary.LittleEndian.Uint32(data[0x15:]))
	if count > 64 {
		return nil, fmt.Errorf("cad: R2000 段目录条目数异常 %d", count)
	}
	entries := make([]r2000Locator, 0, count)
	rest := data[0x19:]
	for i := 0; i < count && len(rest) >= 9; i++ {
		entries = append(entries, r2000Locator{
			recordNo: rest[0],
			offset:   binary.LittleEndian.Uint32(rest[1:]),
			size:     binary.LittleEndian.Uint32(rest[5:]),
		})
		rest = rest[9:]
	}
	return entries, nil
}

// r2000SectionRecordNo 段号语义（参考实现 SectionKind）。
const (
	r2000SecHeaderVars  = 0
	r2000SecClasses     = 1
	r2000SecObjectMap   = 2
	r2000SecUnknown3    = 3
	r2000SecMeasurement = 4
)

// readR2000Section 按段号取 R2000 段原始数据（不压缩）。
func readR2000Section(data []byte, recordNo uint8) ([]byte, error) {
	locators, err := parseR2000Directory(data)
	if err != nil {
		return nil, err
	}
	for _, loc := range locators {
		if loc.recordNo != recordNo {
			continue
		}
		start, end := int(loc.offset), int(loc.offset)+int(loc.size)
		if start > len(data) || end > len(data) || start > end {
			return nil, fmt.Errorf("cad: R2000 段 %d 越界", recordNo)
		}
		return data[start:end], nil
	}
	return nil, fmt.Errorf("cad: R2000 段 %d 不存在", recordNo)
}
