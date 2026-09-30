// container_r2007.go 实现 R2007（AC1021）容器解析：第二头部（RS 去交织 +
// R21 解压）、页表、段表与数据页装配。
//
// 容器要点：R2007 的所谓 "Reed-Solomon" 层在写入端实际是字节去交织
// （转置），本文件按去交织语义解码；系统页 RS 块 239/255、数据页块
// 251/255，页对齐与块数推导遵循 ODA 5.4 布局描述。
package cad

import (
	"encoding/binary"
	"fmt"
)

// R2007 容器布局常量（ODA 5.4）。
const (
	r2007StreamBaseOffset     uint64 = 0x480 // 数据页基地址
	r2007SecondHeaderOffset   int    = 0x80  // 第二头部（RS 编码）起始
	r2007SecondHeaderRSSize   int    = 0x3D8 // 第二头部 RS 编码长度
	r2007SecondHeaderPayload  int    = 0x20  // 第二头部有效负载偏移
	r2007SecondHeaderBodySize int    = 0x110 // 第二头部解压后体长
	r2007SysPageRSDataSize    uint64 = 239   // 系统页 RS 数据块大小
	r2007SysPageRSCodeWord    uint64 = 255   // 系统页 RS 码字大小
	r2007SysPageCRCBlock      uint64 = 8     // 系统页 CRC 块大小（填充对齐）
	r2007SysPageAlign         uint64 = 0x20  // 系统页对齐
	r2007DataPageRSDataSize   uint64 = 251   // 数据页 RS 数据块大小
	r2007DataPageCRCBlock     uint64 = 8     // 数据页填充对齐
	r2007SectionEntrySize     int    = 8 * 8 // 段表条目大小（8 个 u64）
	r2007SectionPageSize      int    = 7 * 8 // 段页信息条目大小（7 个 u64）
)

// r2007Header 第二头部关键字段（u64；各字段在解压体中的槽位见
// decodeR2007Header）。
type r2007Header struct {
	pagesMapOffset              uint64
	pagesMapSizeCompressed      uint64
	pagesMapSizeUncompressed    uint64
	pagesMapCorrectionFactor    uint64
	sectionsMapID               uint64
	sectionsMapSizeCompressed   uint64
	sectionsMapSizeUncompressed uint64
	sectionsMapCorrectionFactor uint64
	sectionsAmount              uint64
}

// r2007PageSlot 页表条目：id + 原始大小 + 推导地址。
type r2007PageSlot struct {
	id      int64
	size    uint64
	address uint64
}

// r2007PageInfo 段内页信息。
type r2007PageInfo struct {
	offset           uint64
	id               uint64
	sizeUncompressed uint64
	sizeCompressed   uint64
}

// r2007SecEntry 段条目。
type r2007SecEntry struct {
	size    uint64
	encoded uint64
	name    string
	pages   []r2007PageInfo
}

// r2007Deinterleave 去交织：method 4 为转置读取——输出第 p 字节取自源
// 第 (p%k)*blockCount + p/k 字节；method 1 为恒等拷贝。输出 k*blockCount 字节。
func r2007Deinterleave(src []byte, k, blockCount int, method byte) ([]byte, error) {
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
func alignUp(v, align uint64) uint64 {
	if align == 0 {
		return v
	}
	return (v + align - 1) / align * align
}

// divCeil 向上取整除法（0 值恒等于 0）。
func divCeil(v, d uint64) uint64 {
	if v == 0 {
		return 0
	}
	return (v + d - 1) / d
}

// decodeR2007Header 解析第二头部：0x80 起 0x3D8 字节按 RS(239,3,method4)
// 去交织，前 0x20 为 crc/key/dataCRC/compressedSize/length2 五槽，随后按
// compressedSize 取压缩体并解压 0x110 字节（34 个 u64），按槽位取字段。
func decodeR2007Header(data []byte) (r2007Header, error) {
	var hdr r2007Header
	if len(data) < r2007SecondHeaderOffset+r2007SecondHeaderRSSize {
		return hdr, fmt.Errorf("cad: 文件过小，缺少 R2007 第二头部")
	}
	encoded := data[r2007SecondHeaderOffset : r2007SecondHeaderOffset+r2007SecondHeaderRSSize]
	decoded, err := r2007Deinterleave(encoded, 239, 3, 4)
	if err != nil {
		return hdr, err
	}
	if len(decoded) < r2007SecondHeaderPayload {
		return hdr, fmt.Errorf("cad: R2007 第二头部去交织结果截断")
	}
	// 头部五槽：0x00 crc、0x08 加密 key（未加密段不使用）、0x10 dataCRC、
	// 0x18 compressedSize（负值表示 store）、0x20 length2（未使用）
	compressedSize := int64(binary.LittleEndian.Uint32(decoded[24:]))

	var body []byte
	switch {
	case compressedSize < 0:
		size := int(-compressedSize)
		if r2007SecondHeaderPayload+size > len(decoded) {
			return hdr, fmt.Errorf("cad: R2007 第二头部体越界")
		}
		body = append([]byte(nil), decoded[r2007SecondHeaderPayload:r2007SecondHeaderPayload+size]...)
	case compressedSize > 0:
		if r2007SecondHeaderPayload+int(compressedSize) > len(decoded) {
			return hdr, fmt.Errorf("cad: R2007 第二头部压缩体越界")
		}
		body, err = decompressR21(decoded[r2007SecondHeaderPayload:r2007SecondHeaderPayload+int(compressedSize)], r2007SecondHeaderBodySize)
		if err != nil {
			return hdr, err
		}
	default:
		return hdr, fmt.Errorf("cad: R2007 第二头部 compressedSize 为 0")
	}
	if len(body) < r2007SecondHeaderBodySize {
		return hdr, fmt.Errorf("cad: R2007 第二头部体截断")
	}

	// 字段槽位：体为 34 个 u64，按槽号取值
	field := func(slot int) uint64 {
		return binary.LittleEndian.Uint64(body[slot*8:])
	}
	return r2007Header{
		pagesMapOffset:              field(7),
		pagesMapSizeCompressed:      field(10),
		pagesMapSizeUncompressed:    field(11),
		pagesMapCorrectionFactor:    field(3),
		sectionsMapID:               field(24),
		sectionsMapSizeCompressed:   field(22),
		sectionsMapSizeUncompressed: field(25),
		sectionsMapCorrectionFactor: field(27),
		sectionsAmount:              field(20),
	}, nil
}

// parseR2007PageMap 解析页表：0x480 起的系统页，条目为 size u64 + id u64
// 的 16 字节对，页地址自 0x480 起按 size 累加；(0,0) 条目为表尾。
func parseR2007PageMap(data []byte, hdr r2007Header) ([]r2007PageSlot, error) {
	address := r2007StreamBaseOffset + hdr.pagesMapOffset
	sysData, err := readR2007SystemPage(data, address, hdr.pagesMapSizeCompressed,
		hdr.pagesMapSizeUncompressed, hdr.pagesMapCorrectionFactor)
	if err != nil {
		return nil, err
	}
	var slots []r2007PageSlot
	current := r2007StreamBaseOffset
	for rest := sysData; len(rest) >= 16; rest = rest[16:] {
		size := binary.LittleEndian.Uint64(rest)
		id := binary.LittleEndian.Uint64(rest[8:])
		if size == 0 && id == 0 {
			break
		}
		if size == 0 || size >= 1<<63 {
			return nil, fmt.Errorf("cad: R2007 页表条目 size 非法 %d", size)
		}
		slots = append(slots, r2007PageSlot{id: int64(id), size: size, address: current})
		current += size
	}
	if len(slots) == 0 {
		return nil, fmt.Errorf("cad: R2007 页表为空")
	}
	return slots, nil
}

// parseR2007SectionMap 解析段表：8 个 u64 头（size/max/encrypted/hash/
// nameLength/unknown/encoded/pageCount）+ 名称（UTF-16，NUL 截断）+
// 每页 7 个 u64（offset/size/id/sizeUC/sizeC/校验/CRC 中取用 4 项）。
func parseR2007SectionMap(data []byte, hdr r2007Header, pages []r2007PageSlot) ([]r2007SecEntry, error) {
	secMapPage := findR2007Page(pages, int64(hdr.sectionsMapID))
	if secMapPage == nil {
		return nil, fmt.Errorf("cad: R2007 段表页不存在（id=%d）", hdr.sectionsMapID)
	}
	sysData, err := readR2007SystemPage(data, secMapPage.address, hdr.sectionsMapSizeCompressed,
		hdr.sectionsMapSizeUncompressed, hdr.sectionsMapCorrectionFactor)
	if err != nil {
		return nil, err
	}
	maxSections := int(^uint(0) >> 1)
	if hdr.sectionsAmount > 0 {
		maxSections = int(hdr.sectionsAmount - 1)
	}
	var sections []r2007SecEntry
	rest := sysData
	for len(rest) >= r2007SectionEntrySize && len(sections) < maxSections {
		head := rest[:r2007SectionEntrySize]
		rest = rest[r2007SectionEntrySize:]
		u64At := func(i int) uint64 { return binary.LittleEndian.Uint64(head[i*8:]) }
		size := u64At(0)
		// 槽 1 max size、槽 3 hash code、槽 5 unknown：未使用
		nameLength := u64At(4)
		encoded := u64At(6)
		pageCount := u64At(7)
		if size == 0 && pageCount == 0 && nameLength == 0 {
			break
		}
		// LibreDWG decode_r2007.c 对 encrypted 标志仅记录不使用：
		// 数据页读取流程与普通段一致（RS 去交织 + R21 解压），照常解析
		if nameLength > uint64(len(rest)) {
			return nil, fmt.Errorf("cad: R2007 段名超长 %d", nameLength)
		}
		name := decodeUTF16LE(rest[:nameLength])
		// 段名以 NUL 结尾，截断至首个 NUL
		if i := indexByteStr(name, 0); i >= 0 {
			name = name[:i]
		}
		rest = rest[nameLength:]
		sec := r2007SecEntry{size: size, encoded: encoded, name: name}
		for i := uint64(0); i < pageCount; i++ {
			if len(rest) < r2007SectionPageSize {
				return nil, fmt.Errorf("cad: R2007 段页信息截断")
			}
			page := rest[:r2007SectionPageSize]
			rest = rest[r2007SectionPageSize:]
			sec.pages = append(sec.pages, r2007PageInfo{
				offset:           binary.LittleEndian.Uint64(page),
				id:               binary.LittleEndian.Uint64(page[16:]),
				sizeUncompressed: binary.LittleEndian.Uint64(page[24:]),
				sizeCompressed:   binary.LittleEndian.Uint64(page[32:]),
			})
		}
		sections = append(sections, sec)
	}
	if len(sections) == 0 {
		return nil, fmt.Errorf("cad: R2007 段表为空")
	}
	return sections, nil
}

// findR2007Page 在页表中查找指定 id 的页（未命中返回 nil）。
func findR2007Page(pages []r2007PageSlot, id int64) *r2007PageSlot {
	for i := range pages {
		if pages[i].id == id {
			return &pages[i]
		}
	}
	return nil
}

// readR2007SystemPage 读取系统页（页表/段表）：按校正因子与 RS 块大小
// 推导页占位，RS(239) 去交织后按压缩标记分流 R21 解压或原样截取。
func readR2007SystemPage(data []byte, address, sizeCompressed, sizeUncompressed, correctionFactor uint64) ([]byte, error) {
	compressedPadded := alignUp(sizeCompressed, r2007SysPageCRCBlock)
	rsPreEncoded := compressedPadded * correctionFactor
	blockCount := divCeil(rsPreEncoded, r2007SysPageRSDataSize)
	pageSize := alignUp(blockCount*r2007SysPageRSCodeWord, r2007SysPageAlign)
	start, end := int(address), int(address+pageSize)
	if start > len(data) || end > len(data) || start > end {
		return nil, fmt.Errorf("cad: R2007 系统页越界（addr=%d size=%d）", address, pageSize)
	}
	decoded, err := r2007Deinterleave(data[start:end], 239, int(blockCount), 4)
	if err != nil {
		return nil, err
	}
	return r2007TakePayload(decoded, sizeCompressed, sizeUncompressed, "系统页")
}

// r2007TakePayload 系统页/数据页共用的载荷截取：压缩态走 R21 解压，
// store 态按未压缩尺寸截取，统一做上界防护。
func r2007TakePayload(decoded []byte, sizeCompressed, sizeUncompressed uint64, what string) ([]byte, error) {
	if sizeCompressed < sizeUncompressed {
		if sizeCompressed > uint64(len(decoded)) {
			return nil, fmt.Errorf("cad: R2007 %s压缩数据越界", what)
		}
		return decompressR21(decoded[:sizeCompressed], int(sizeUncompressed))
	}
	if sizeUncompressed > uint64(len(decoded)) {
		return nil, fmt.Errorf("cad: R2007 %s数据越界", what)
	}
	return decoded[:sizeUncompressed], nil
}

// r2007DataPageBlocks 数据页 RS 块数：页数据先按 8 字节填充再编码，
// 块数须按填充后大小推导（ODA 5.4）。
func r2007DataPageBlocks(sizeCompressed uint64) uint64 {
	return divCeil(alignUp(sizeCompressed, r2007DataPageCRCBlock), r2007DataPageRSDataSize)
}

// readR2007DataPage 读取并解压一个数据页：按编码方法去交织（0 原样、
// 1/4 走 RS），压缩态 R21 解压；解压失败时按页占位大小反推候选块数重试
// （兼容不按填充规则推导块数的写入方）。
func readR2007DataPage(data []byte, slot r2007PageSlot, encoded uint64, sizeCompressed, sizeUncompressed uint64) ([]byte, error) {
	blocks := r2007DataPageBlocks(sizeCompressed)
	minPageSize := r2007DataPageRSDataSize * blocks
	readSize := slot.size
	if readSize < minPageSize {
		readSize = minPageSize
	}
	start, end := int(slot.address), int(slot.address+readSize)
	if start > len(data) || end > len(data) || start > end {
		return nil, fmt.Errorf("cad: R2007 数据页越界（addr=%d size=%d）", slot.address, readSize)
	}
	pageBuf := data[start:end]
	method := byte(encoded)
	decodePage := func(bc int) ([]byte, error) {
		switch method {
		case 0:
			return append([]byte(nil), pageBuf...), nil
		case 1, 4:
			return r2007Deinterleave(pageBuf, 251, bc, method)
		default:
			return nil, fmt.Errorf("cad: 不支持的 R2007 数据页编码 %d", encoded)
		}
	}
	decoded, err := decodePage(int(blocks))
	if err != nil {
		return nil, err
	}
	if sizeCompressed < sizeUncompressed {
		out, derr := r2007TakePayload(decoded, sizeCompressed, sizeUncompressed, "数据页")
		if derr == nil {
			return out, nil
		}
		if method != 4 {
			return out, derr
		}
		// 候选块数重试：按页占位大小除以码字（及向上取整变体）反推
		byPage := slot.size / 255
		byPageCeil := divCeil(slot.size, 255)
		for _, cand := range []uint64{byPage, byPageCeil} {
			if cand == blocks || cand == 0 {
				continue
			}
			if cand*251 < sizeCompressed || cand*251 > uint64(len(pageBuf)) {
				continue
			}
			if alt, aerr := decodePage(int(cand)); aerr == nil {
				if out2, derr2 := decompressR21(alt[:sizeCompressed], int(sizeUncompressed)); derr2 == nil {
					return out2, nil
				}
			}
		}
		return out, derr
	}
	return r2007TakePayload(decoded, sizeCompressed, sizeUncompressed, "数据页")
}

// assembleR2007Section 按段页表把各页内容写回段内声明的偏移位置，
// 装配段完整数据。
func assembleR2007Section(data []byte, sec *r2007SecEntry, pages []r2007PageSlot) ([]byte, error) {
	if sec.size > 1<<31 {
		return nil, fmt.Errorf("cad: R2007 段大小异常 %d", sec.size)
	}
	out := make([]byte, sec.size)
	lookup := make(map[int64]r2007PageSlot, len(pages))
	for _, p := range pages {
		lookup[p.id] = p
	}
	for _, page := range sec.pages {
		slot, ok := lookup[int64(page.id)]
		if !ok {
			return nil, fmt.Errorf("cad: R2007 段页 %d 不在页表中", page.id)
		}
		pageData, err := readR2007DataPage(data, slot, sec.encoded, page.sizeCompressed, page.sizeUncompressed)
		if err != nil {
			return nil, err
		}
		start := int(page.offset)
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
func loadNamedSectionDataR2007(data []byte, name string) ([]byte, error) {
	hdr, err := decodeR2007Header(data)
	if err != nil {
		return nil, err
	}
	pages, err := parseR2007PageMap(data, hdr)
	if err != nil {
		return nil, err
	}
	sections, err := parseR2007SectionMap(data, hdr, pages)
	if err != nil {
		return nil, err
	}
	for i := range sections {
		if sections[i].name == name {
			return assembleR2007Section(data, &sections[i], pages)
		}
	}
	return nil, fmt.Errorf("cad: 段 %q 不存在（R2007）", name)
}

// decodeUTF16LE 解码 UTF-16LE 字节串。
func decodeUTF16LE(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, binary.LittleEndian.Uint16(b[i:]))
	}
	return string(utf16Decode(u))
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
