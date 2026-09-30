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
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/objrec"
	"hash/crc32"
	"io"
)

// WriteDwgR2000 将 R2000（AC1015）家族文档（含同容器路径的 R13/R14，版本串
// 随素材原样保留）写出为 DWG 字节流。文档必须来自 Parse（内部保留回放素材）。
//
// 文件布局：头部 15 字节回放 → 段目录（条目重建 + CRC(seed 0xC0C1，覆盖
// 从字节 0 起) + 16 字节哨兵）→ 各段原始字节（源目录顺序，对象图段除外）
// → 对象区整块回放 → 重建的对象图。写出的文件可被本包 Parse 与 LibreDWG
// 重新读取（见 encode_file_test.go 门禁）。
func WriteDwgR2000(doc *Document, w io.Writer) error {
	if doc == nil || doc.R2000Raw == nil {
		return fmt.Errorf("cad: 非 R2000 家族文档或缺少回放素材，无法写出")
	}
	out, err := writeR2000Sections(doc.R2000Raw)
	if err != nil {
		return err
	}
	_, err = w.Write(out)
	return err
}

// writeR2000Sections 按新布局组装 R2000 文件字节流。段写出按容器版本分派：
// R2000 家族走回放式；R2004/R2007 容器由后续版本的段写出器在此分派扩展。
func writeR2000Sections(raw *container.R2000RawData) ([]byte, error) {
	if len(raw.Order) == 0 {
		return nil, fmt.Errorf("cad: 源文件缺少 R2000 段目录")
	}
	// 布局分配：目录区尺寸只依赖条目数，先于各段定址；对象图流长度与
	// 绝对偏移无关（差分编码），且置于文件末尾，无循环依赖。
	mapPayload := []byte(nil)
	hasMap := false
	if blob := raw.Sections[container.R2000SecObjectMap]; len(blob) > 0 || len(raw.Refs) > 0 {
		hasMap = true
		// 对象区新基址 = 目录尾 + 各回放段总长；先累加得到再重建对象图
		objNewBase := uint64(0x15 + 4 + len(raw.Order)*9 + 2 + len(container.R2000LocatorSentinel))
		for _, no := range raw.Order {
			if no == container.R2000SecObjectMap {
				continue
			}
			objNewBase += uint64(len(raw.Sections[no]))
		}
		delta := int64(objNewBase) - int64(raw.ObjBase)
		mapPayload = buildR2000ObjectMap(raw.Refs, delta)
	}
	// 各段与对象区顺序定址
	type placed struct {
		no     uint8
		offset uint32
		size   uint32
	}
	placements := make([]placed, 0, len(raw.Order))
	cursor := uint64(0x15 + 4 + len(raw.Order)*9 + 2 + len(container.R2000LocatorSentinel))
	for _, no := range raw.Order {
		if no == container.R2000SecObjectMap {
			continue
		}
		payload := raw.Sections[no]
		placements = append(placements, placed{no: no, offset: uint32(cursor), size: uint32(len(payload))})
		cursor += uint64(len(payload))
	}
	// 对象区紧跟各回放段顺序铺放；对象图置于文件末尾
	cursor += uint64(len(raw.ObjBlob))
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
	w.WriteTF(raw.Header)
	w.WriteRL(uint32(len(raw.Order)))
	for _, no := range raw.Order {
		if no == container.R2000SecObjectMap {
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
	w.WriteTF(container.R2000LocatorSentinel[:])
	// 段数据与对象区按定址顺序回放
	for _, p := range placements {
		w.WriteTF(raw.Sections[p.no])
	}
	w.WriteTF(raw.ObjBlob)
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
	if doc != nil && doc.R2007Raw != nil {
		return WriteDwgR2007(doc, w)
	}
	if doc != nil && doc.R2004Raw != nil {
		return WriteDwgR2004(doc, w)
	}
	if doc != nil && doc.R2000Raw != nil {
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
	if doc == nil || doc.R2004Raw == nil {
		return fmt.Errorf("cad: 非 R2004 家族文档或缺少回放素材，无法写出")
	}
	out, err := writeR2004Sections(doc.R2004Raw)
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
	matches := container.LzGreedyMatches(src, 4, 0x400, n)
	out := make([]byte, 0, n/2+16)
	if len(matches) == 0 {
		out = container.LzWriteLitLen(out, n)
		out = append(out, src...)
		return append(out, 0x11)
	}
	if matches[0].Pos > 0 {
		// 首匹配起点 ≥ minMatch，前导字面量段必然可独立编码
		out = container.LzWriteLitLen(out, matches[0].Pos)
		out = append(out, src[:matches[0].Pos]...)
	}
	for i, m := range matches {
		// 该匹配之后的字面量段（至下一匹配或流尾）：1..3 内嵌进匹配块
		// 低 2 位，≥4 单独编码——与解压端 litCount 分支一一对应
		segStart := m.Pos + m.Length
		segEnd := n
		if i+1 < len(matches) {
			segEnd = matches[i+1].Pos
		}
		litLen := segEnd - segStart
		litCount := 0
		if litLen <= 3 {
			litCount = litLen
		}
		switch {
		case m.Length <= 14:
			off := m.Dist - 1
			op := byte((m.Length+1)<<4) | byte(off&0x03)<<2 | byte(litCount)
			out = append(out, op, byte(off>>2))
		case m.Length <= 33:
			off := m.Dist - 1
			out = append(out, byte(0x1E+m.Length), byte(off&0x3F)<<2|byte(litCount), byte(off>>6))
		default:
			off := m.Dist - 1
			out = append(out, 0x20)
			out = container.LzWriteExtLen(out, m.Length-0x21)
			out = append(out, byte(off&0x3F)<<2|byte(litCount), byte(off>>6))
		}
		if litCount > 0 {
			out = append(out, src[segStart:segEnd]...)
		} else if litLen > 0 {
			out = container.LzWriteLitLen(out, litLen)
			out = append(out, src[segStart:segEnd]...)
		}
	}
	return append(out, 0x11)
}

// writeR2004Sections 按新布局组装 R2004 文件字节流。
func writeR2004Sections(raw *container.R2004RawData) ([]byte, error) {
	// ① 各数据页重压缩：按源页头事实切片（整块）→ r2004CompressBlock，并
	//    逐页自检（压缩结果经自家解压器还原必须逐字节一致，失败即拒绝写出）
	payloads := make(map[uint32][]byte, 64)
	for si := range raw.Sections {
		sec := &raw.Sections[si]
		for pi, ref := range sec.Pages {
			chunk := sec.Data[ref.DataOff : ref.DataOff+ref.DataLen]
			var payload []byte
			if sec.Compressed == 2 {
				payload = r2004CompressBlock(chunk)
				back, err := container.DecompressLZ77(payload, len(chunk))
				if err != nil || !bytes.Equal(back, chunk) {
					return nil, fmt.Errorf("cad: 段 %q 第 %d 页压缩自检失败", sec.Name, pi)
				}
			} else {
				payload = chunk
			}
			payloads[ref.Id] = payload
		}
	}
	// ② 段表页与页表页载荷（段表内容原样回放，仅重新压缩）
	infoPayload := container.CompressLZ77(raw.InfoBlob)
	infoSize := uint32(container.R2004SystemHdrSize + len(infoPayload))
	// 页表自身条目尺寸与内容相互依赖（条目写在内容里）。页表页采用
	// store（不压缩）写出（对齐 LibreDWG write 端 SECTION_SYSTEM_MAP 的
	// store 策略）：store 下页尺寸是内容长度的确定性函数，与自身条目的
	// 数值无关，迭代一次即收敛。此前对页表内容做 LZ77 重压缩，压缩大小
	// 随自身条目数值抖动，Leader/material/skylight 语料出现 173↔175 的
	// 2-循环振荡，固定点不存在导致写出失败。
	buildMap := func(ownSize uint32) []byte {
		buf := make([]byte, 0, len(raw.PageOrder)*8)
		for _, id := range raw.PageOrder {
			size := uint32(0)
			switch id {
			case raw.InfoID:
				size = infoSize
			case raw.SysmapID:
				size = ownSize
			default:
				if pl, ok := payloads[uint32(id)]; ok {
					size = uint32(container.R2004PageHdrSize + len(pl))
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
	sysSize := uint32(container.R2004SystemHdrSize)
	var mapPayload []byte
	for iter := 0; ; iter++ {
		mapPayload = container.CompressLZ77LiteralOnly(buildMap(sysSize))
		next := uint32(container.R2004SystemHdrSize + len(mapPayload))
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
	placed := make([]r2004PlacedPage, 0, len(raw.PageOrder))
	address := uint64(0x100)
	for _, id := range raw.PageOrder {
		var p r2004PlacedPage
		p.id = id
		p.address = address
		switch {
		case id == raw.InfoID:
			p.kind, p.size, p.payload = 1, infoSize, infoPayload
		case id == raw.SysmapID:
			p.kind, p.size, p.payload = 2, sysSize, mapPayload
		default:
			pl, ok := payloads[uint32(id)]
			if !ok {
				continue // 孤立页：丢弃
			}
			p.kind = 0
			p.size = uint32(container.R2004PageHdrSize + len(pl))
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
	copy(out, raw.Prefix[:])
	// 数据页页头需要段描述（sec_type、页三元组），先建页 id → 段下标映射
	secByPage := make(map[uint32]int, len(payloads))
	for si := range raw.Sections {
		for _, ref := range raw.Sections[si].Pages {
			secByPage[ref.Id] = si
		}
	}
	for _, p := range placed {
		if p.kind == 0 {
			r2004WriteDataPage(out, p, &raw.Sections[secByPage[uint32(p.id)]])
			continue
		}
		magic := uint32(container.SectionPageMapMagic)
		decompSize := 8 * len(raw.PageOrder) // 页表内容固定为 (id,size) 8 字节序列
		if p.kind == 1 {
			magic = container.SectionMapMagic
			decompSize = len(raw.InfoBlob)
		}
		// 页表页（kind 2）payload 为 store 原始字节（见上方收敛注释），
		// 压缩类型写 1；段表页（kind 1）保持 LZ77 压缩（类型 2）
		r2004WriteSystemSection(out, p.address, magic, p.payload, decompSize, false)
	}
	// 尾部 secondheader：20 字节伪页表段头 + 加密头副本（对齐 LibreDWG
	// 写出端；读侧不校验，仅保持布局兼容）
	sh := int(lastEnd)
	binary.LittleEndian.PutUint32(out[sh:], container.SectionPageMapMagic)
	out[sh+12] = 0x02
	// ⑤ 明文头三处段地址回填（缩略图/摘要信息首页 +32，VBA 工程首页原址；
	//    读侧仅判非零决定是否加载对应段）
	patchAddr := func(name string, at int, plusHeader bool) {
		for si := range raw.Sections {
			sec := &raw.Sections[si]
			if sec.Name != name || len(sec.Pages) == 0 {
				continue
			}
			addr := r2004PageAddress(placed, sec.Pages[0].Id)
			if plusHeader {
				addr += container.R2004PageHdrSize
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
	hdr := raw.HdrPlain
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
	binary.LittleEndian.PutUint32(hdr[0x50:], uint32(raw.SysmapID))
	binary.LittleEndian.PutUint64(hdr[0x54:], r2004PageAddress(placed, uint32(raw.SysmapID))-0x100)
	binary.LittleEndian.PutUint32(hdr[0x5C:], uint32(raw.InfoID))
	binary.LittleEndian.PutUint32(hdr[0x60:], uint32(maxID))
	binary.LittleEndian.PutUint32(hdr[0x64:], 0)
	binary.LittleEndian.PutUint32(hdr[0x68:], 0)
	sum := crc32.Update(0, crc32.IEEETable, hdr[:0x6C])
	binary.LittleEndian.PutUint32(hdr[0x68:], sum)
	pad := container.R2004LCGPad(container.R2004HdrPlainSize)
	for i := 0; i < container.R2004HdrPlainSize; i++ {
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
	copy(out[off+container.R2004SystemHdrSize:], payload)
}

// r2004WriteDataPage 写一个数据页：构建 8×u32 明文页头（含页载荷校验和与
// 页头校验和），按 0x4164536B^页地址 掩码逐 4 字节加密后落位。
func r2004WriteDataPage(out []byte, p r2004PlacedPage, sec *container.R2004SectionData) {
	var ref *container.R2004PageRef
	for i := range sec.Pages {
		if sec.Pages[i].Id == uint32(p.id) {
			ref = &sec.Pages[i]
			break
		}
	}
	off := int(p.address)
	dataCRC := r2004PageChecksum(0, 0, p.payload)
	var hdr [32]byte
	binary.LittleEndian.PutUint32(hdr[0:], container.DataSectionMagic)
	binary.LittleEndian.PutUint32(hdr[4:], sec.SecType)
	binary.LittleEndian.PutUint32(hdr[8:], uint32(len(p.payload)))
	binary.LittleEndian.PutUint32(hdr[12:], uint32(len(p.payload)))
	if ref != nil {
		// 页目标偏移保持源布局（段表三元组同步），页尺寸写实际载荷长度
		binary.LittleEndian.PutUint32(hdr[16:], uint32(ref.StartOffset))
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
	copy(out[off+container.R2004PageHdrSize:], p.payload)
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
	pesize := container.AlignUp(sizeComp, container.R2007SysPageCRCBlock) * correction
	blockCount := int(container.DivCeil(pesize, container.R2007SysPageRSDataSize))
	if blockCount <= 0 {
		blockCount = 1
	}
	pedata := make([]byte, blockCount*int(container.R2007SysPageRSDataSize))
	copy(pedata, content)
	phys = r2007PadPage(r2007EncodeRS(pedata, int(container.R2007SysPageRSDataSize), blockCount), blockCount, int(container.R2007SysPageRSCodeWord))
	return phys, sizeComp, sizeUncomp
}

// r2007DataPagePhysSize 数据页物理尺寸：align32(块数×255)，块数按压缩尺寸
// 推导（读侧 r2007DataPageBlocks 同口径）。空载荷按 1 块兜底，避免
// 页表出现 size=0 的非法条目。
func r2007DataPagePhysSize(sizeComp uint64) uint64 {
	blocks := container.R2007DataPageBlocks(sizeComp)
	if blocks == 0 {
		blocks = 1
	}
	return container.AlignUp(blocks*container.R2007SysPageRSCodeWord, container.R2007SysPageAlign)
}

// r2007EncodeDataPage 编码一个数据页：RS(251) 交织后补零到块数×255 并对齐
// 0x20。payload 为压缩（或原样）页内容。
func r2007EncodeDataPage(payload []byte) []byte {
	blocks := int(container.R2007DataPageBlocks(uint64(len(payload))))
	if blocks <= 0 {
		blocks = 1
	}
	pedata := make([]byte, blocks*251)
	copy(pedata, payload)
	return r2007PadPage(r2007EncodeRS(pedata, 251, blocks), blocks, 255)
}

// r2007PadPage 把交织输出补零到块数×码字长度并向上对齐 0x20（物理页尺寸）。
func r2007PadPage(body []byte, blockCount, codeWord int) []byte {
	total := container.AlignUp(uint64(blockCount*codeWord), container.R2007SysPageAlign)
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
	if doc == nil || doc.R2007Raw == nil {
		return fmt.Errorf("cad: 非 R2007 文档或缺少回放素材，无法写出")
	}
	out, err := writeR2007Sections(doc.R2007Raw)
	if err != nil {
		return err
	}
	_, err = w.Write(out)
	return err
}

// writeR2007Sections 按新布局组装 R2007 文件字节流。
func writeR2007Sections(raw *container.R2007RawData) ([]byte, error) {
	// ① 数据页重压缩 + RS 交织，并逐页自检（压缩结果经自家解压器还原必须
	//    逐字节一致，失败即拒绝写出）；页内容按源页偏移/解压尺寸切片
	payloads := make(map[uint64][]byte, 64)
	for si := range raw.Sections {
		sec := &raw.Sections[si]
		for pi, page := range sec.Pages {
			start := page.Offset
			if start > sec.Size {
				start = sec.Size
			}
			end := page.Offset + page.UncompSize
			if end > sec.Size {
				end = sec.Size
			}
			chunk := sec.Data[start:end]
			comp := container.CompressR21(chunk)
			if len(comp) >= len(chunk) {
				// 压不短则原样存储：读侧仅按 comp_size<uncomp_size 判定解压，
				// 原样页（comp==uncomp）走直拷分支，不做 R21 自检
				comp = append([]byte(nil), chunk...)
			} else if len(chunk) > 0 {
				back, err := container.DecompressR21(comp, len(chunk))
				if err != nil || !bytes.Equal(back, chunk) {
					return nil, fmt.Errorf("cad: 段 %q 第 %d 页压缩自检失败", sec.Name, pi)
				}
			}
			payloads[page.Id] = comp
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
		for si := range raw.Sections {
			sec := &raw.Sections[si]
			encoded := uint64(4) // 写侧统一 RS 编码
			if len(sec.Pages) == 0 {
				encoded = sec.Encoded
			}
			buf = binary.LittleEndian.AppendUint64(buf, sec.Size)
			buf = binary.LittleEndian.AppendUint64(buf, sec.MaxSize)
			buf = binary.LittleEndian.AppendUint64(buf, 0) // encrypted
			buf = binary.LittleEndian.AppendUint64(buf, 0) // hashcode
			buf = binary.LittleEndian.AppendUint64(buf, uint64((len(sec.Name)+1)*2))
			buf = binary.LittleEndian.AppendUint64(buf, sec.Unknown)
			buf = binary.LittleEndian.AppendUint64(buf, encoded)
			buf = binary.LittleEndian.AppendUint64(buf, uint64(len(sec.Pages)))
			putName(sec.Name)
			for _, page := range sec.Pages {
				compLen := uint64(len(payloads[page.Id]))
				buf = binary.LittleEndian.AppendUint64(buf, page.Offset)                    // 页在段内偏移
				buf = binary.LittleEndian.AppendUint64(buf, r2007DataPagePhysSize(compLen)) // 页物理尺寸
				buf = binary.LittleEndian.AppendUint64(buf, page.Id)                        // 页 id
				buf = binary.LittleEndian.AppendUint64(buf, page.UncompSize)                // 解压尺寸
				buf = binary.LittleEndian.AppendUint64(buf, compLen)                        // 压缩尺寸
				buf = binary.LittleEndian.AppendUint64(buf, 0)                              // checksum
				buf = binary.LittleEndian.AppendUint64(buf, 0)                              // crc64
			}
		}
		if raw.TrailingEmpty {
			// 源段表尾部的空条目原样补回（基线行为对齐，见 r2007RawData 注释）
			for i := 0; i < 8; i++ {
				buf = binary.LittleEndian.AppendUint64(buf, 0)
			}
		}
		return buf
	}
	smContent := buildSectionMap()
	smPhys, smComp, smUncomp := r2007EncodeSystemPage(smContent, raw.SmCorr)
	// ③ 页表内容重建（页表页自身条目尺寸自引用，迭代收敛）
	sectionMapSize := uint64(len(smPhys))
	buildPageMap := func(sysmapSize uint64) []byte {
		buf := make([]byte, 0, (len(raw.PageOrder)+1)*16)
		for _, p := range raw.PageOrder {
			var size uint64
			switch {
			case p.Id == raw.SysmapID:
				size = sysmapSize
			case p.Id == raw.SecmapID:
				size = sectionMapSize
			default:
				// 数据页与孤立页（如页表副本页 pages_map2，无段引用）：
				// 孤立页载荷为空，按最小零页占位，保证条目 size 非零
				size = r2007DataPagePhysSize(uint64(len(payloads[uint64(p.Id)])))
			}
			buf = binary.LittleEndian.AppendUint64(buf, size)
			buf = binary.LittleEndian.AppendUint64(buf, uint64(p.Id))
		}
		// 终止对（读侧遇 size=0 且 id=0 停止）
		buf = binary.LittleEndian.AppendUint64(buf, 0)
		buf = binary.LittleEndian.AppendUint64(buf, 0)
		return buf
	}
	sysmapSize := uint64(container.R2007SysPageAlign)
	var pmContent []byte
	for iter := 0; ; iter++ {
		pmContent = buildPageMap(sysmapSize)
		_, _, unc := r2007EncodeSystemPage(pmContent, raw.PmCorr)
		blocks := int(container.DivCeil(container.AlignUp(unc, container.R2007SysPageCRCBlock)*raw.PmCorr, container.R2007SysPageRSDataSize))
		if blocks <= 0 {
			blocks = 1
		}
		next := container.AlignUp(uint64(blocks)*container.R2007SysPageRSCodeWord, container.R2007SysPageAlign)
		if next == sysmapSize {
			break
		}
		sysmapSize = next
		if iter >= 32 {
			return nil, fmt.Errorf("cad: 页表自引用尺寸不收敛")
		}
	}
	pmPhys, pmComp, pmUncomp := r2007EncodeSystemPage(pmContent, raw.PmCorr)
	// ④ 定址：0x480 起按源页表顺序铺放，页地址 = 页表条目累计
	placed := make([]r2007PlacedPage, 0, len(raw.PageOrder))
	address := container.R2007StreamBaseOffset
	for _, p := range raw.PageOrder {
		pp := r2007PlacedPage{id: p.Id, address: address}
		switch {
		case p.Id == raw.SysmapID:
			pp.kind, pp.size, pp.payload = 2, uint64(len(pmPhys)), pmPhys
		case p.Id == raw.SecmapID:
			pp.kind, pp.size, pp.payload = 1, uint64(len(smPhys)), smPhys
		default:
			pl := payloads[uint64(p.Id)] // 孤立页无载荷：铺最小零页占位
			pp.kind, pp.size, pp.payload = 0, r2007DataPagePhysSize(uint64(len(pl))), r2007EncodeDataPage(pl)
		}
		placed = append(placed, pp)
		address += pp.size
	}
	total := address
	// ⑤ 第二头部 34 字段回填（comprLen=-0x110 未压缩存储体）
	fields := raw.Fields
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
	fields[9] = uint64(container.R2007SecondHeaderOffset)
	// 页表页数据区偏移 = 页表页新地址 - 0x480
	pmAddr := r2007PageAddressR2007(placed, raw.SysmapID)
	fields[r2007FieldPagesMapOff] = pmAddr - container.R2007StreamBaseOffset
	// ⑥ 文件体组装：头部 + 第二头部 + 间隔区 + 各页
	out := make([]byte, total)
	copy(out, raw.Prefix[:])
	// 头部段地址回填：Preview/SummaryInfo/VBAProject 首页（读侧按非零判定
	// 是否加载对应段；段缺失写 0）
	patchAddr := func(name string, at int) {
		for si := range raw.Sections {
			sec := &raw.Sections[si]
			if sec.Name != name || len(sec.Pages) == 0 {
				continue
			}
			binary.LittleEndian.PutUint32(out[at:], uint32(r2007PageAddressR2007(placed, int64(sec.Pages[0].Id))))
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
	body := make([]byte, 0, container.R2007SecondHeaderBodySize)
	for _, f := range fields {
		body = binary.LittleEndian.AppendUint64(body, f)
	}
	compBody := container.CompressR21(body)
	if back, err := container.DecompressR21(compBody, container.R2007SecondHeaderBodySize); err != nil || !bytes.Equal(back, body) {
		return nil, fmt.Errorf("cad: R2007 第二头部体压缩自检失败")
	}
	head := make([]byte, container.R2007SecondHeaderPayload)
	binary.LittleEndian.PutUint32(head[24:], uint32(len(compBody)))
	pedata := make([]byte, 3*int(container.R2007SysPageRSDataSize))
	copy(pedata, head)
	copy(pedata[container.R2007SecondHeaderPayload:], compBody)
	copy(out[container.R2007SecondHeaderOffset:], r2007EncodeRS(pedata, int(container.R2007SysPageRSDataSize), 3))
	copy(out[0x458:], raw.HeaderGap[:])
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
