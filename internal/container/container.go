// container.go 实现 DWG R2004+（AC1018~AC1032）物理容器与 R2000（AC1015）
// 段目录的解析：加密容器头、页表（page map）、段表（section map）、数据页
// 解密解压与段数据装配。
//
// 模块划分：版本识别（detectVersion/verString）、R2004+ 头解密
// （decryptR2004Header/lcgKeyStream）、系统段解压（inflateSystemSection）、
// 页表/段表解析（parsePageMap/parseSectionMap）、段装配（assembleSection）、
// R2000 段目录（parseR2000Directory/readR2000Section）。
package container

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

const (
	HeaderOffset         = 0x80 // R2004+ 加密头起始偏移
	HeaderSize           = 0x6C // 加密头长度
	SectionPageMapMagic  = 0x41630E3B
	SectionMapMagic      = 0x4163003B
	DataSectionMagic     = 0x4163043B
	systemSectionHdrSize = 0x14 // system section 头长度：4 个 u32 + crc
)

var (
	// sentinelClassesBefore/After AcDb:Classes 段前后哨兵字节。
	SentinelClassesBefore = [16]byte{0x8D, 0xA1, 0xC4, 0xB8, 0xC4, 0xA9, 0xF8, 0xC5, 0xC0, 0xDC, 0xF4, 0x5F, 0xE7, 0xCF, 0xB6, 0x8A}
	SentinelClassesAfter  = [16]byte{0x72, 0x5E, 0x3B, 0x47, 0x3B, 0x56, 0x07, 0x3A, 0x3F, 0x23, 0x0B, 0xA0, 0x18, 0x30, 0x49, 0x75}
)

// r2004Header R2004+ 加密头中与容器定位相关的字段。
type r2004Header struct {
	SectionPageMapAddress uint64
	SectionMapID          uint32
}

// pageSlot 页表项：id 为页编号（<0 为空洞），address 为页在文件中的物理地址。
type PageSlot struct {
	Id      int32
	Address uint64
}

// secEntry 段表项：一个逻辑段由若干页组成。
type secEntry struct {
	Size              uint64
	MaxDecompressedSz uint32
	Compressed        uint32
	encrypted         uint32
	Name              string
	PageIDs           []uint32
}

// dwgVersion DWG 版本枚举（按解码路径分组）。
type DwgVersion int

const (
	VerR2000 DwgVersion = iota // AC1015
	VerR14                     // AC1014（容器同 R2000，实体头/几何为 R13/R14 布局）
	VerR13                     // AC1012（早于 R13c3：DICTIONARY 无 is_hardowner 等）
	VerR2004                   // AC1018（含同容器路径的更高版本）
	VerR2007                   // AC1021（独立容器：RS 去交织 + R21 解压）
	VerR2010                   // AC1024
	VerR2013                   // AC1027
	VerR2018                   // AC1032
	VerR9                      // AC1004（pre-R13 家族：固定偏移表驱动字节布局）
	VerR10                     // AC1006（pre-R13：+UCS/VPORT 表）
	VerR11                     // AC1009（pre-R13：+sentinel/CRC 包夹与 APPID/DIMSTYLE/VX 表）
)

// versionMagic 魔数 → 版本对照表（detectVersion 与 verString 共用一套映射，
// 表中缺省版本默认回落 AC1032）。
var versionMagic = map[DwgVersion]string{
	VerR2000: "AC1015",
	VerR14:   "AC1014",
	VerR13:   "AC1012",
	VerR2004: "AC1018",
	VerR2007: "AC1021",
	VerR2010: "AC1024",
	VerR2013: "AC1027",
	VerR2018: "AC1032",
	VerR9:    "AC1004",
	VerR10:   "AC1006",
	VerR11:   "AC1009",
}

// verString 版本串。
func (v DwgVersion) VerString() string {
	if s, ok := versionMagic[v]; ok {
		return s
	}
	return "AC1032"
}

// preR13 是否 pre-R13 家族（字节对齐固定偏移布局，与 R13+ 位流容器完全不同）。
func (v DwgVersion) PreR13() bool { return v == VerR9 || v == VerR10 || v == VerR11 }

// r2010Plus 对象记录是否带 UMC/OT 类型码前缀。
func (v DwgVersion) R2010Plus() bool { return v >= VerR2010 }

// detectVersion 从文件头 6 字节魔数识别 DWG 版本。
func DetectVersion(Data []byte) (DwgVersion, error) {
	if len(Data) < 6 {
		return 0, fmt.Errorf("cad: 文件过小，不是合法 DWG")
	}
	magic := string(Data[:6])
	for ver, Name := range versionMagic {
		if Name == magic {
			return ver, nil
		}
	}
	return 0, fmt.Errorf("cad: 不支持的 DWG 版本 %q（支持 AC1004/AC1006/AC1009/AC1014/AC1015/AC1018/AC1021/AC1024/AC1027/AC1032）", magic)
}

// readCodepage 读取文件头 0x13 处的 2 字节 codepage 编号（过短文件按 30 兜底）。
func ReadCodepage(Data []byte) uint16 {
	if len(Data) < 0x15 {
		return 30
	}
	return binary.LittleEndian.Uint16(Data[0x13:])
}

// lcgKeyStream 生成与 DWG 写出端一致的 LCG 伪随机字节序列
// （seed=1，x*=0x343FD，x+=0x269EC3，取 >>16 字节）。
func LcgKeyStream() [HeaderSize]byte {
	var seq [HeaderSize]byte
	var seed uint32 = 1
	for i := range seq {
		seed = seed*0x343FD + 0x269EC3
		seq[i] = byte(seed >> 16)
	}
	return seq
}

// decryptR2004Header 解密 0x80 起 0x6C 字节的 R2004+ 头，提取页表地址与
// 段表页 id：密文逐字节与 LCG 密钥流异或，随后按固定偏移取两个字段。
func DecryptR2004Header(Data []byte) (r2004Header, error) {
	if len(Data) < HeaderOffset+HeaderSize {
		return r2004Header{}, fmt.Errorf("cad: 文件过小，缺少 R2004+ 容器头")
	}
	keys := LcgKeyStream()
	plain := make([]byte, HeaderSize)
	for i := range plain {
		plain[i] = Data[HeaderOffset+i] ^ keys[i]
	}
	if len(plain) < 0x50+24 {
		return r2004Header{}, fmt.Errorf("cad: R2004+ 容器头字段越界")
	}
	return r2004Header{
		SectionPageMapAddress: binary.LittleEndian.Uint64(plain[0x54:]),
		SectionMapID:          binary.LittleEndian.Uint32(plain[0x5C:]),
	}, nil
}

// inflateSystemSection 读取并解压一个 system section（页表/段表所在的
// 压缩结构）：校验签名后按压缩类型分流——0x01 原样存储（截齐声明尺寸），
// 0x02 走 LZ77；其余类型拒绝。
func inflateSystemSection(Data []byte, Address uint64, expectSignature uint32) ([]byte, error) {
	Offset := int(Address)
	// 垃圾地址（如 R2000 文件误入 R2004+ 容器路径）经 int 截断可能为负，
	// 必须与上界一并检查，避免 data[offset:] 负索引 panic
	if Offset < 0 || Offset+systemSectionHdrSize > len(Data) {
		return nil, fmt.Errorf("cad: system section 头越界")
	}
	signature := binary.LittleEndian.Uint32(Data[Offset:])
	plainSize := binary.LittleEndian.Uint32(Data[Offset+4:])
	packedSize := binary.LittleEndian.Uint32(Data[Offset+8:])
	codec := binary.LittleEndian.Uint32(Data[Offset+12:])
	if signature != expectSignature {
		return nil, fmt.Errorf("cad: system section 签名不匹配 0x%08X（期望 0x%08X）", signature, expectSignature)
	}
	if packedSize == 0 {
		return nil, nil
	}
	start := Offset + systemSectionHdrSize
	packed := Data[start : start+int(packedSize)]
	switch codec {
	case 0x01:
		// store：载荷原样存储（写端页表页的 store 策略与部分源文件的
		// 未压缩段），长度截齐解压目标尺寸
		if int(plainSize) > len(packed) {
			return nil, fmt.Errorf("cad: system section store 长度不足")
		}
		return packed[:plainSize], nil
	case 0x02:
		return DecompressLZ77(packed, int(plainSize))
	default:
		return nil, fmt.Errorf("cad: 不支持的 system section 压缩类型 0x%X", codec)
	}
}

// parsePageMap 解析页表：内容为 (id i32, size u32) 序列，页地址自 0x100 起
// 按 size 累加；id<0 表示空洞，条目后跟 16 字节空洞描述一并跳过。
func ParsePageMap(Data []byte, Header r2004Header) ([]PageSlot, error) {
	blob, err := inflateSystemSection(Data, Header.SectionPageMapAddress+0x100, SectionPageMapMagic)
	if err != nil {
		return nil, err
	}
	var slots []PageSlot
	var pageAddress uint64 = 0x100
	for rest := blob; len(rest) >= 8; {
		Id := int32(binary.LittleEndian.Uint32(rest))
		pageSize := binary.LittleEndian.Uint32(rest[4:])
		rest = rest[8:]
		slots = append(slots, PageSlot{Id: Id, Address: pageAddress})
		pageAddress += uint64(pageSize)
		if Id < 0 {
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
func ParseSectionMap(Data []byte, Header r2004Header, Pages []PageSlot) ([]secEntry, error) {
	mapPage := findPageByID(Pages, int32(Header.SectionMapID))
	if mapPage == nil {
		return nil, fmt.Errorf("cad: 页表中找不到段表页 id=%d", Header.SectionMapID)
	}
	blob, err := inflateSystemSection(Data, mapPage.Address, SectionMapMagic)
	if err != nil {
		return nil, err
	}
	if len(blob) < 20 {
		return nil, fmt.Errorf("cad: 段表头截断")
	}
	entryCount := int(binary.LittleEndian.Uint32(blob))
	Sections := make([]secEntry, 0, entryCount)
	rest := blob[20:]
	for i := 0; i < entryCount; i++ {
		if len(rest) < 96 {
			return nil, fmt.Errorf("cad: 段表条目截断")
		}
		desc := rest[:96]
		rest = rest[96:]
		pageCount := int(binary.LittleEndian.Uint32(desc[8:]))
		entry := secEntry{
			Size:              binary.LittleEndian.Uint64(desc),
			MaxDecompressedSz: binary.LittleEndian.Uint32(desc[12:]),
			Compressed:        binary.LittleEndian.Uint32(desc[20:]),
			encrypted:         binary.LittleEndian.Uint32(desc[28:]),
			Name:              ReadCString(desc[32:96]),
		}
		for p := 0; p < pageCount; p++ {
			if len(rest) < 16 {
				return nil, fmt.Errorf("cad: 段页描述截断")
			}
			entry.PageIDs = append(entry.PageIDs, binary.LittleEndian.Uint32(rest))
			rest = rest[16:]
		}
		Sections = append(Sections, entry)
	}
	return Sections, nil
}

// findPageByID 在页表中查找指定 id 的页（未命中返回 nil）。
func findPageByID(Pages []PageSlot, Id int32) *PageSlot {
	for i := range Pages {
		if Pages[i].Id == Id {
			return &Pages[i]
		}
	}
	return nil
}

// loadSectionPage 读取并解压段内单个数据页：解密页头、校验签名，按段
// 压缩方法返回页内容（codec 2 走 LZ77，解压目标尺寸用段的
// maxDecompressedSz——页头字段并非该值）。
func loadSectionPage(Data []byte, slot PageSlot, sec *secEntry) ([]byte, error) {
	pageOffset := int(slot.Address)
	if pageOffset+32 > len(Data) {
		return nil, fmt.Errorf("cad: 数据页头越界")
	}
	Header := UnmaskPageHeader(Data[pageOffset:pageOffset+32], slot.Address)
	if sig := binary.LittleEndian.Uint32(Header); sig != DataSectionMagic {
		return nil, fmt.Errorf("cad: 数据页签名不匹配 0x%08X", sig)
	}
	packedSize := int(binary.LittleEndian.Uint32(Header[8:]))
	dataStart := pageOffset + 32
	dataEnd := dataStart + packedSize
	if dataEnd > len(Data) || dataStart > dataEnd {
		return nil, fmt.Errorf("cad: 数据页内容越界")
	}
	packed := Data[dataStart:dataEnd]
	if sec.Compressed == 2 {
		return DecompressLZ77(packed, int(sec.MaxDecompressedSz))
	}
	return packed, nil
}

// assembleSection 按页表装配一个逻辑段的完整数据：逐页解密解压拼接，
// 末页填充截齐——先按 pageSize×页数封顶，再按段声明 size 截断。
func assembleSection(Data []byte, section *secEntry, pageLookup map[uint32]PageSlot) ([]byte, error) {
	if section.encrypted == 1 {
		return nil, fmt.Errorf("cad: 暂不支持加密段 %q", section.Name)
	}
	pageSize := int(section.MaxDecompressedSz)
	capacity := pageSize * len(section.PageIDs)
	if capacity == 0 {
		return nil, nil
	}
	out := make([]byte, 0, capacity)
	for _, pageID := range section.PageIDs {
		slot, ok := pageLookup[pageID]
		if !ok {
			return nil, fmt.Errorf("cad: 页表中找不到页 id=%d（段 %q）", pageID, section.Name)
		}
		pageData, err := loadSectionPage(Data, slot, section)
		if err != nil {
			return nil, err
		}
		out = append(out, pageData...)
	}
	if len(out) > capacity {
		out = out[:capacity]
	}
	if declared := int(section.Size); declared > 0 && declared <= len(out) {
		out = out[:declared]
	}
	return out, nil
}

// unmaskPageHeader 解密 32 字节数据页头：按 4 字节分块与 0x4164536B^页地址
// 异或。
func UnmaskPageHeader(b []byte, pageAddress uint64) []byte {
	out := make([]byte, 32)
	copy(out, b[:32])
	mask := uint32(0x4164536B ^ uint32(pageAddress))
	for i := 0; i+4 <= 32; i += 4 {
		binary.LittleEndian.PutUint32(out[i:], binary.LittleEndian.Uint32(out[i:])^mask)
	}
	return out
}

// pageLookupTable 由页表构建 id→页 的查询表（仅收录 id>0 的有效页）。
func pageLookupTable(Pages []PageSlot) map[uint32]PageSlot {
	lookup := make(map[uint32]PageSlot, len(Pages))
	for _, p := range Pages {
		if p.Id > 0 {
			lookup[uint32(p.Id)] = p
		}
	}
	return lookup
}

// loadNamedSectionData 按名称（如 "AcDb:AcDbObjects"）加载段的完整解压数据。
// R2007（AC1021）容器结构独立，单独路由。
func LoadNamedSectionData(Data []byte, Name string) ([]byte, error) {
	if len(Data) >= 6 && string(Data[:6]) == "AC1021" {
		return loadNamedSectionDataR2007(Data, Name)
	}
	Header, err := DecryptR2004Header(Data)
	if err != nil {
		return nil, err
	}
	Pages, err := ParsePageMap(Data, Header)
	if err != nil {
		return nil, err
	}
	Sections, err := ParseSectionMap(Data, Header, Pages)
	if err != nil {
		return nil, err
	}
	for i := range Sections {
		if Sections[i].Name == Name {
			return assembleSection(Data, &Sections[i], pageLookupTable(Pages))
		}
	}
	return nil, fmt.Errorf("cad: 段 %q 不存在", Name)
}

// readCString 读取 NUL 结尾的字节串。
func ReadCString(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		return string(b[:i])
	}
	return string(b)
}

// ---- R2000 (AC1015) 容器：段目录在 0x15，段数据不压缩直接切片 ----

// r2000Locator R2000 段定位记录。
type r2000Locator struct {
	RecordNo uint8
	Offset   uint32
	Size     uint32
}

var R2000LocatorSentinel = [16]byte{0x95, 0xA0, 0x4E, 0x28, 0x99, 0x82, 0x1A, 0xE5, 0x5E, 0x41, 0xE0, 0x5F, 0x9D, 0x3A, 0x4D, 0x00}

// parseR2000Directory 解析 R2000 段目录：0x15 处 u32 条目数，每条
// record_no(u8)+offset(u32)+size(u32)，尾部 CRC 与哨兵。
func ParseR2000Directory(Data []byte) ([]r2000Locator, error) {
	if len(Data) < 0x15+4 {
		return nil, fmt.Errorf("cad: 文件过小，缺少 R2000 段目录")
	}
	count := int(binary.LittleEndian.Uint32(Data[0x15:]))
	if count > 64 {
		return nil, fmt.Errorf("cad: R2000 段目录条目数异常 %d", count)
	}
	entries := make([]r2000Locator, 0, count)
	rest := Data[0x19:]
	for i := 0; i < count && len(rest) >= 9; i++ {
		entries = append(entries, r2000Locator{
			RecordNo: rest[0],
			Offset:   binary.LittleEndian.Uint32(rest[1:]),
			Size:     binary.LittleEndian.Uint32(rest[5:]),
		})
		rest = rest[9:]
	}
	return entries, nil
}

// r2000SectionRecordNo 段号语义（参考实现 SectionKind）。
const (
	R2000SecHeaderVars  = 0
	R2000SecClasses     = 1
	R2000SecObjectMap   = 2
	r2000SecUnknown3    = 3
	R2000SecMeasurement = 4
)

// readR2000Section 按段号取 R2000 段原始数据（不压缩）。
func ReadR2000Section(Data []byte, RecordNo uint8) ([]byte, error) {
	locators, err := ParseR2000Directory(Data)
	if err != nil {
		return nil, err
	}
	for _, loc := range locators {
		if loc.RecordNo != RecordNo {
			continue
		}
		start, end := int(loc.Offset), int(loc.Offset)+int(loc.Size)
		if start > len(Data) || end > len(Data) || start > end {
			return nil, fmt.Errorf("cad: R2000 段 %d 越界", RecordNo)
		}
		return Data[start:end], nil
	}
	return nil, fmt.Errorf("cad: R2000 段 %d 不存在", RecordNo)
}
