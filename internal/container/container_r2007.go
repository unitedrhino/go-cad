// container_r2007.go 实现 R2007（AC1021）容器解析：第二头部（RS 去交织 +
// R21 解压）、页表、段表与数据页装配。
//
// 容器要点：R2007 的所谓 "Reed-Solomon" 层在写入端实际是字节去交织
// （转置），本文件按去交织语义解码；系统页 RS 块 239/255、数据页块
// 251/255，页对齐与块数推导遵循 ODA 5.4 布局描述。
package container

import (
	"encoding/binary"
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
)

// R2007 容器布局常量（ODA 5.4）。
const (
	R2007StreamBaseOffset     uint64 = 0x480 // 数据页基地址
	R2007SecondHeaderOffset   int    = 0x80  // 第二头部（RS 编码）起始
	R2007SecondHeaderRSSize   int    = 0x3D8 // 第二头部 RS 编码长度
	R2007SecondHeaderPayload  int    = 0x20  // 第二头部有效负载偏移
	R2007SecondHeaderBodySize int    = 0x110 // 第二头部解压后体长
	R2007SysPageRSDataSize    uint64 = 239   // 系统页 RS 数据块大小
	R2007SysPageRSCodeWord    uint64 = 255   // 系统页 RS 码字大小
	R2007SysPageCRCBlock      uint64 = 8     // 系统页 CRC 块大小（填充对齐）
	R2007SysPageAlign         uint64 = 0x20  // 系统页对齐
	R2007DataPageRSDataSize   uint64 = 251   // 数据页 RS 数据块大小
	r2007DataPageCRCBlock     uint64 = 8     // 数据页填充对齐
	r2007SectionEntrySize     int    = 8 * 8 // 段表条目大小（8 个 u64）
	r2007SectionPageSize      int    = 7 * 8 // 段页信息条目大小（7 个 u64）
)

// r2007Header 第二头部关键字段（u64；各字段在解压体中的槽位见
// decodeR2007Header）。
type r2007Header struct {
	PagesMapOffset              uint64
	PagesMapSizeCompressed      uint64
	PagesMapSizeUncompressed    uint64
	PagesMapCorrectionFactor    uint64
	SectionsMapID               uint64
	SectionsMapSizeCompressed   uint64
	SectionsMapSizeUncompressed uint64
	SectionsMapCorrectionFactor uint64
	sectionsAmount              uint64
}

// r2007PageSlot 页表条目：id + 原始大小 + 推导地址。
type R2007PageSlot struct {
	Id      int64
	Size    uint64
	Address uint64
}

// r2007PageInfo 段内页信息。
type r2007PageInfo struct {
	Offset           uint64
	Id               uint64
	SizeUncompressed uint64
	SizeCompressed   uint64
}

// r2007SecEntry 段条目。
type r2007SecEntry struct {
	Size    uint64
	Encoded uint64
	Name    string
	Pages   []r2007PageInfo
}

// r2007Deinterleave 去交织：method 4 为转置读取——输出第 p 字节取自源
// 第 (p%k)*blockCount + p/k 字节；method 1 为恒等拷贝。输出 k*blockCount 字节。
func R2007Deinterleave(src []byte, k, blockCount int, method byte) ([]byte, error) {
	outSize := k * blockCount
	if outSize == 0 {
		return []byte{}, nil
	}
	if len(src) < outSize {
		return nil, fmt.Errorf("cad: R2007 RS 输入不足（%d < %d）", len(src), outSize)
	}
	switch method {
	case 4:
		out := make([]byte, outSize)
		for p := 0; p < outSize; p++ {
			out[p] = src[(p%k)*blockCount+p/k]
		}
		return out, nil
	case 1:
		return append([]byte(nil), src[:outSize]...), nil
	default:
		return nil, fmt.Errorf("cad: 不支持的 R2007 RS 方法 %d", method)
	}
}

// alignUp 向上对齐（align 为 0 时原值返回）。
func AlignUp(v, align uint64) uint64 {
	if align == 0 {
		return v
	}
	return (v + align - 1) / align * align
}

// divCeil 向上取整除法（0 值恒等于 0）。
func DivCeil(v, d uint64) uint64 {
	if v == 0 {
		return 0
	}
	return (v + d - 1) / d
}

// decodeR2007Header 解析第二头部：0x80 起 0x3D8 字节按 RS(239,3,method4)
// 去交织，前 0x20 为 crc/key/dataCRC/compressedSize/length2 五槽，随后按
// compressedSize 取压缩体并解压 0x110 字节（34 个 u64），按槽位取字段。
func DecodeR2007Header(Data []byte) (r2007Header, error) {
	var hdr r2007Header
	if len(Data) < R2007SecondHeaderOffset+R2007SecondHeaderRSSize {
		return hdr, fmt.Errorf("cad: 文件过小，缺少 R2007 第二头部")
	}
	Encoded := Data[R2007SecondHeaderOffset : R2007SecondHeaderOffset+R2007SecondHeaderRSSize]
	decoded, err := R2007Deinterleave(Encoded, 239, 3, 4)
	if err != nil {
		return hdr, err
	}
	if len(decoded) < R2007SecondHeaderPayload {
		return hdr, fmt.Errorf("cad: R2007 第二头部去交织结果截断")
	}
	// 头部五槽：0x00 crc、0x08 加密 key（未加密段不使用）、0x10 dataCRC、
	// 0x18 compressedSize（负值表示 store）、0x20 length2（未使用）
	compressedSize := int64(binary.LittleEndian.Uint32(decoded[24:]))

	var body []byte
	switch {
	case compressedSize < 0:
		Size := int(-compressedSize)
		if R2007SecondHeaderPayload+Size > len(decoded) {
			return hdr, fmt.Errorf("cad: R2007 第二头部体越界")
		}
		body = append([]byte(nil), decoded[R2007SecondHeaderPayload:R2007SecondHeaderPayload+Size]...)
	case compressedSize > 0:
		if R2007SecondHeaderPayload+int(compressedSize) > len(decoded) {
			return hdr, fmt.Errorf("cad: R2007 第二头部压缩体越界")
		}
		body, err = DecompressR21(decoded[R2007SecondHeaderPayload:R2007SecondHeaderPayload+int(compressedSize)], R2007SecondHeaderBodySize)
		if err != nil {
			return hdr, err
		}
	default:
		return hdr, fmt.Errorf("cad: R2007 第二头部 compressedSize 为 0")
	}
	if len(body) < R2007SecondHeaderBodySize {
		return hdr, fmt.Errorf("cad: R2007 第二头部体截断")
	}

	// 字段槽位：体为 34 个 u64，按槽号取值
	field := func(slot int) uint64 {
		return binary.LittleEndian.Uint64(body[slot*8:])
	}
	return r2007Header{
		PagesMapOffset:              field(7),
		PagesMapSizeCompressed:      field(10),
		PagesMapSizeUncompressed:    field(11),
		PagesMapCorrectionFactor:    field(3),
		SectionsMapID:               field(24),
		SectionsMapSizeCompressed:   field(22),
		SectionsMapSizeUncompressed: field(25),
		SectionsMapCorrectionFactor: field(27),
		sectionsAmount:              field(20),
	}, nil
}

// parseR2007PageMap 解析页表：0x480 起的系统页，条目为 size u64 + id u64
// 的 16 字节对，页地址自 0x480 起按 size 累加；(0,0) 条目为表尾。
func ParseR2007PageMap(Data []byte, hdr r2007Header) ([]R2007PageSlot, error) {
	Address := R2007StreamBaseOffset + hdr.PagesMapOffset
	sysData, err := readR2007SystemPage(Data, Address, hdr.PagesMapSizeCompressed,
		hdr.PagesMapSizeUncompressed, hdr.PagesMapCorrectionFactor)
	if err != nil {
		return nil, err
	}
	var slots []R2007PageSlot
	current := R2007StreamBaseOffset
	for rest := sysData; len(rest) >= 16; rest = rest[16:] {
		Size := binary.LittleEndian.Uint64(rest)
		Id := binary.LittleEndian.Uint64(rest[8:])
		if Size == 0 && Id == 0 {
			break
		}
		if Size == 0 || Size >= 1<<63 {
			return nil, fmt.Errorf("cad: R2007 页表条目 size 非法 %d", Size)
		}
		slots = append(slots, R2007PageSlot{Id: int64(Id), Size: Size, Address: current})
		current += Size
	}
	if len(slots) == 0 {
		return nil, fmt.Errorf("cad: R2007 页表为空")
	}
	return slots, nil
}

// parseR2007SectionMap 解析段表：8 个 u64 头（size/max/encrypted/hash/
// nameLength/unknown/encoded/pageCount）+ 名称（UTF-16，NUL 截断）+
// 每页 7 个 u64（offset/size/id/sizeUC/sizeC/校验/CRC 中取用 4 项）。
func ParseR2007SectionMap(Data []byte, hdr r2007Header, Pages []R2007PageSlot) ([]r2007SecEntry, error) {
	secMapPage := findR2007Page(Pages, int64(hdr.SectionsMapID))
	if secMapPage == nil {
		return nil, fmt.Errorf("cad: R2007 段表页不存在（id=%d）", hdr.SectionsMapID)
	}
	sysData, err := readR2007SystemPage(Data, secMapPage.Address, hdr.SectionsMapSizeCompressed,
		hdr.SectionsMapSizeUncompressed, hdr.SectionsMapCorrectionFactor)
	if err != nil {
		return nil, err
	}
	maxSections := int(^uint(0) >> 1)
	if hdr.sectionsAmount > 0 {
		maxSections = int(hdr.sectionsAmount - 1)
	}
	var Sections []r2007SecEntry
	rest := sysData
	for len(rest) >= r2007SectionEntrySize && len(Sections) < maxSections {
		head := rest[:r2007SectionEntrySize]
		rest = rest[r2007SectionEntrySize:]
		u64At := func(i int) uint64 { return binary.LittleEndian.Uint64(head[i*8:]) }
		Size := u64At(0)
		// 槽 1 max size、槽 3 hash code、槽 5 unknown：未使用
		nameLength := u64At(4)
		Encoded := u64At(6)
		pageCount := u64At(7)
		if Size == 0 && pageCount == 0 && nameLength == 0 {
			break
		}
		// LibreDWG decode_r2007.c 对 encrypted 标志仅记录不使用：
		// 数据页读取流程与普通段一致（RS 去交织 + R21 解压），照常解析
		if nameLength > uint64(len(rest)) {
			return nil, fmt.Errorf("cad: R2007 段名超长 %d", nameLength)
		}
		Name := DecodeUTF16LE(rest[:nameLength])
		// 段名以 NUL 结尾，截断至首个 NUL
		if i := indexByteStr(Name, 0); i >= 0 {
			Name = Name[:i]
		}
		rest = rest[nameLength:]
		sec := r2007SecEntry{Size: Size, Encoded: Encoded, Name: Name}
		for i := uint64(0); i < pageCount; i++ {
			if len(rest) < r2007SectionPageSize {
				return nil, fmt.Errorf("cad: R2007 段页信息截断")
			}
			page := rest[:r2007SectionPageSize]
			rest = rest[r2007SectionPageSize:]
			sec.Pages = append(sec.Pages, r2007PageInfo{
				Offset:           binary.LittleEndian.Uint64(page),
				Id:               binary.LittleEndian.Uint64(page[16:]),
				SizeUncompressed: binary.LittleEndian.Uint64(page[24:]),
				SizeCompressed:   binary.LittleEndian.Uint64(page[32:]),
			})
		}
		Sections = append(Sections, sec)
	}
	if len(Sections) == 0 {
		return nil, fmt.Errorf("cad: R2007 段表为空")
	}
	return Sections, nil
}

// findR2007Page 在页表中查找指定 id 的页（未命中返回 nil）。
func findR2007Page(Pages []R2007PageSlot, Id int64) *R2007PageSlot {
	for i := range Pages {
		if Pages[i].Id == Id {
			return &Pages[i]
		}
	}
	return nil
}

// readR2007SystemPage 读取系统页（页表/段表）：按校正因子与 RS 块大小
// 推导页占位，RS(239) 去交织后按压缩标记分流 R21 解压或原样截取。
func readR2007SystemPage(Data []byte, Address, SizeCompressed, SizeUncompressed, correctionFactor uint64) ([]byte, error) {
	compressedPadded := AlignUp(SizeCompressed, R2007SysPageCRCBlock)
	rsPreEncoded := compressedPadded * correctionFactor
	blockCount := DivCeil(rsPreEncoded, R2007SysPageRSDataSize)
	pageSize := AlignUp(blockCount*R2007SysPageRSCodeWord, R2007SysPageAlign)
	start, end := int(Address), int(Address+pageSize)
	if start > len(Data) || end > len(Data) || start > end {
		return nil, fmt.Errorf("cad: R2007 系统页越界（addr=%d size=%d）", Address, pageSize)
	}
	decoded, err := R2007Deinterleave(Data[start:end], 239, int(blockCount), 4)
	if err != nil {
		return nil, err
	}
	return r2007TakePayload(decoded, SizeCompressed, SizeUncompressed, "系统页")
}

// r2007TakePayload 系统页/数据页共用的载荷截取：压缩态走 R21 解压，
// store 态按未压缩尺寸截取，统一做上界防护。
func r2007TakePayload(decoded []byte, SizeCompressed, SizeUncompressed uint64, what string) ([]byte, error) {
	if SizeCompressed < SizeUncompressed {
		if SizeCompressed > uint64(len(decoded)) {
			return nil, fmt.Errorf("cad: R2007 %s压缩数据越界", what)
		}
		return DecompressR21(decoded[:SizeCompressed], int(SizeUncompressed))
	}
	if SizeUncompressed > uint64(len(decoded)) {
		return nil, fmt.Errorf("cad: R2007 %s数据越界", what)
	}
	return decoded[:SizeUncompressed], nil
}

// r2007DataPageBlocks 数据页 RS 块数：页数据先按 8 字节填充再编码，
// 块数须按填充后大小推导（ODA 5.4）。
func R2007DataPageBlocks(SizeCompressed uint64) uint64 {
	return DivCeil(AlignUp(SizeCompressed, r2007DataPageCRCBlock), R2007DataPageRSDataSize)
}

// readR2007DataPage 读取并解压一个数据页：按编码方法去交织（0 原样、
// 1/4 走 RS），压缩态 R21 解压；解压失败时按页占位大小反推候选块数重试
// （兼容不按填充规则推导块数的写入方）。
func readR2007DataPage(Data []byte, slot R2007PageSlot, Encoded uint64, SizeCompressed, SizeUncompressed uint64) ([]byte, error) {
	blocks := R2007DataPageBlocks(SizeCompressed)
	minPageSize := R2007DataPageRSDataSize * blocks
	readSize := slot.Size
	if readSize < minPageSize {
		readSize = minPageSize
	}
	start, end := int(slot.Address), int(slot.Address+readSize)
	if start > len(Data) || end > len(Data) || start > end {
		return nil, fmt.Errorf("cad: R2007 数据页越界（addr=%d size=%d）", slot.Address, readSize)
	}
	pageBuf := Data[start:end]
	method := byte(Encoded)
	decodePage := func(bc int) ([]byte, error) {
		switch method {
		case 0:
			return append([]byte(nil), pageBuf...), nil
		case 1, 4:
			return R2007Deinterleave(pageBuf, 251, bc, method)
		default:
			return nil, fmt.Errorf("cad: 不支持的 R2007 数据页编码 %d", Encoded)
		}
	}
	decoded, err := decodePage(int(blocks))
	if err != nil {
		return nil, err
	}
	if SizeCompressed < SizeUncompressed {
		out, derr := r2007TakePayload(decoded, SizeCompressed, SizeUncompressed, "数据页")
		if derr == nil {
			return out, nil
		}
		if method != 4 {
			return out, derr
		}
		// 候选块数重试：按页占位大小除以码字（及向上取整变体）反推
		byPage := slot.Size / 255
		byPageCeil := DivCeil(slot.Size, 255)
		for _, cand := range []uint64{byPage, byPageCeil} {
			if cand == blocks || cand == 0 {
				continue
			}
			if cand*251 < SizeCompressed || cand*251 > uint64(len(pageBuf)) {
				continue
			}
			if alt, aerr := decodePage(int(cand)); aerr == nil {
				if out2, derr2 := DecompressR21(alt[:SizeCompressed], int(SizeUncompressed)); derr2 == nil {
					return out2, nil
				}
			}
		}
		return out, derr
	}
	return r2007TakePayload(decoded, SizeCompressed, SizeUncompressed, "数据页")
}

// assembleR2007Section 按段页表把各页内容写回段内声明的偏移位置，
// 装配段完整数据。
func assembleR2007Section(Data []byte, sec *r2007SecEntry, Pages []R2007PageSlot) ([]byte, error) {
	if sec.Size > 1<<31 {
		return nil, fmt.Errorf("cad: R2007 段大小异常 %d", sec.Size)
	}
	out := make([]byte, sec.Size)
	lookup := make(map[int64]R2007PageSlot, len(Pages))
	for _, p := range Pages {
		lookup[p.Id] = p
	}
	for _, page := range sec.Pages {
		slot, ok := lookup[int64(page.Id)]
		if !ok {
			return nil, fmt.Errorf("cad: R2007 段页 %d 不在页表中", page.Id)
		}
		pageData, err := readR2007DataPage(Data, slot, sec.Encoded, page.SizeCompressed, page.SizeUncompressed)
		if err != nil {
			return nil, err
		}
		start := int(page.Offset)
		if start >= len(out) {
			continue
		}
		end := start + len(pageData)
		if end > len(out) {
			end = len(out)
		}
		copy(out[start:end], pageData[:end-start])
	}
	return out, nil
}

// loadNamedSectionDataR2007 R2007 按名称加载段。
func loadNamedSectionDataR2007(Data []byte, Name string) ([]byte, error) {
	hdr, err := DecodeR2007Header(Data)
	if err != nil {
		return nil, err
	}
	Pages, err := ParseR2007PageMap(Data, hdr)
	if err != nil {
		return nil, err
	}
	Sections, err := ParseR2007SectionMap(Data, hdr, Pages)
	if err != nil {
		return nil, err
	}
	for i := range Sections {
		if Sections[i].Name == Name {
			return assembleR2007Section(Data, &Sections[i], Pages)
		}
	}
	return nil, fmt.Errorf("cad: 段 %q 不存在（R2007）", Name)
}

// decodeUTF16LE 解码 UTF-16LE 字节串。
func DecodeUTF16LE(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, binary.LittleEndian.Uint16(b[i:]))
	}
	return string(bitstream.Utf16Decode(u))
}

// indexByteStr 字符串内查找字节（NUL 截断用）。
func indexByteStr(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
