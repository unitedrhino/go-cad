// compress_test.go 写出方向压缩原语测试：CRC16 写/读一致性与
// compressLZ77 / compressR21 与解压端的互逆验证（边界输入 + testdata
// 真实文件整流 + 从样本解出的真实压缩段）。
package cad

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// pseudoBytes 确定性伪随机字节串（固定种子，保证测试可重现）。
func pseudoBytes(n int) []byte {
	rng := rand.New(rand.NewSource(42))
	out := make([]byte, n)
	rng.Read(out)
	return out
}

// ---- CRC16 ----

func TestCRC16KnownVectors(t *testing.T) {
	// CRC-16/ARC 标准校验值："123456789" seed=0 → 0xBB3D
	if got := crc16DWG(0, []byte("123456789")); got != 0xBB3D {
		t.Fatalf("crc16DWG 标准向量期望 0xBB3D 得到 %#04x", got)
	}
	// 空数据返回 seed 本身
	if got := crc16DWG(0xC0C1, nil); got != 0xC0C1 {
		t.Fatalf("空数据 CRC 期望等于 seed 0xC0C1 得到 %#04x", got)
	}
	// 分段累积与一次性计算一致
	data := pseudoBytes(300)
	chained := crc16DWG(crc16DWG(0xC0C1, data[:100]), data[100:])
	if chained != crc16DWG(0xC0C1, data) {
		t.Fatalf("分段累积 %#04x != 一次性 %#04x", chained, crc16DWG(0xC0C1, data))
	}
}

func TestEncWriterWriteCRC(t *testing.T) {
	// 全区间：writeCRC(0) 覆盖从头到当前的全部字节，读侧 readCRC 校验通过
	w := newEncWriter()
	payload := []byte("123456789")
	w.writeTF(payload)
	written := w.writeCRC(0)
	if written != crc16DWG(0xC0C1, payload) {
		t.Fatalf("writeCRC 返回 %#04x 与重算 %#04x 不一致", written, crc16DWG(0xC0C1, payload))
	}
	r := newBitStream(w.bytes())
	if _, err := r.readRCS(len(payload)); err != nil {
		t.Fatal(err)
	}
	got, err := r.readCRC()
	if err != nil {
		t.Fatal(err)
	}
	if got != written {
		t.Fatalf("读侧 CRC %#04x != 写入 %#04x", got, written)
	}

	// 区间起点：前 4 字节不计入 CRC
	w2 := newEncWriter()
	w2.writeTF(payload[:4])
	start := w2.tellBits()
	w2.writeTF(payload[4:])
	w2.writeCRC(start)
	r2 := newBitStream(w2.bytes())
	for i := 0; i < len(payload); i++ { // 消费全部 payload，使读位置落在 CRC 上
		if _, err := r2.readRC(); err != nil {
			t.Fatal(err)
		}
	}
	got2, _ := r2.readCRC()
	if got2 != crc16DWG(0xC0C1, payload[4:]) {
		t.Fatalf("区间起点 CRC %#04x 期望 %#04x", got2, crc16DWG(0xC0C1, payload[4:]))
	}

	// 位未对齐写出：writeCRC 补零对齐后计算，读侧 alignByte + readCRC 对称
	w3 := newEncWriter()
	w3.writeTF(payload)
	w3.writeB(true) // 落入部分字节
	want3 := w3.writeCRC(0)
	r3 := newBitStream(w3.bytes())
	// 写出端 writeCRC 补零对齐后把填充字节一并纳入 CRC 区间，
	// 读侧对称：消费 payload + 填充字节再读 CRC
	if _, err := r3.readRCS(10); err != nil {
		t.Fatal(err)
	}
	got3, _ := r3.readCRC()
	if got3 != want3 {
		t.Fatalf("位未对齐场景读侧 CRC %#04x != 写入 %#04x", got3, want3)
	}
}

// ---- 互逆：构造边界输入 ----

// roundTripCases 互逆边界样本：空、极短、高重复、随机、超 64KB 等。
func roundTripCases(t *testing.T) map[string][]byte {
	t.Helper()
	repeat := func(b byte, n int) []byte { return bytes.Repeat([]byte{b}, n) }
	cases := map[string][]byte{
		"空输入":      {},
		"单字节":      {'A'},
		"两字节":      {'A', 'B'},
		"三字节":      {'A', 'B', 'C'},
		"四字节":      {'A', 'B', 'C', 'D'},
		"全相同1MB":   repeat(0x00, 1<<20),
		"全相同FF70K": repeat(0xFF, 70*1024),
		"随机128K":   pseudoBytes(128 * 1024),
		"AB交替重叠":   bytes.Repeat([]byte("AB"), 4096),
		"文本重复":     bytes.Repeat([]byte("the quick brown fox "), 5000),
		"特殊字节":     bytes.Repeat([]byte{0x00, 0x11, 0xFF, 0x10, 0x20, 0x40}, 3000),
		"混合70K": func() []byte {
			out := make([]byte, 0, 70*1024)
			out = append(out, pseudoBytes(30*1024)...)
			out = append(out, bytes.Repeat([]byte("Z"), 20*1024)...)
			out = append(out, pseudoBytes(20*1024)...)
			return out
		}(),
	}
	return cases
}

func assertRoundTrip(t *testing.T, name string, src []byte, compress func([]byte) []byte, decompress func([]byte, int) ([]byte, error)) {
	t.Helper()
	comp := compress(src)
	back, err := decompress(comp, len(src))
	if err != nil {
		t.Fatalf("%s: 解压失败（压缩后 %d 字节）: %v", name, len(comp), err)
	}
	if !bytes.Equal(back, src) {
		t.Fatalf("%s: 互逆不一致（原 %d 字节，解回 %d 字节）", name, len(src), len(back))
	}
}

func TestCompressLZ77RoundTripEdges(t *testing.T) {
	for name, src := range roundTripCases(t) {
		assertRoundTrip(t, "LZ77/"+name, src, compressLZ77, decompressLZ77)
	}
}

func TestCompressR21RoundTripEdges(t *testing.T) {
	for name, src := range roundTripCases(t) {
		assertRoundTrip(t, "R21/"+name, src, compressR21, decompressR21)
	}
}

// ---- 互逆：testdata 真实文件整流 ----

func TestCompressRoundTripRealFiles(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) != ".dwg" {
			continue
		}
		data, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		assertRoundTrip(t, name, data, compressLZ77, decompressLZ77)
		assertRoundTrip(t, name, data, compressR21, decompressR21)
		checked++
	}
	if checked < 50 {
		t.Fatalf("testdata 样本数异常: %d", checked)
	}
}

// compStream 一段真实压缩流与其解压声明尺寸。
type compStream struct {
	src     []byte
	dstSize int
}

// checkStreams 对真实压缩流验证 解压→压缩→再解压 逐字节一致。
func checkStreams(t *testing.T, label string, streams []compStream, compress func([]byte) []byte, decompress func([]byte, int) ([]byte, error)) {
	t.Helper()
	if len(streams) == 0 {
		t.Fatalf("%s: 未收集到任何真实压缩流", label)
	}
	var plainTotal, compTotal int
	for i, s := range streams {
		plain, err := decompress(s.src, s.dstSize)
		if err != nil {
			t.Fatalf("%s[%d]: 真实流解压失败: %v", label, i, err)
		}
		re := compress(plain)
		again, err := decompress(re, s.dstSize)
		if err != nil {
			t.Fatalf("%s[%d]: 重压缩流解压失败: %v", label, i, err)
		}
		if !bytes.Equal(again, plain) {
			t.Fatalf("%s[%d]: 解压→压缩→再解压不一致", label, i)
		}
		plainTotal += len(plain)
		compTotal += len(re)
	}
	t.Logf("%s: %d 段，明文 %d 字节 → 本压缩器 %d 字节（%.1f%%）",
		label, len(streams), plainTotal, compTotal, float64(compTotal)/float64(plainTotal)*100)
}

// ---- 互逆：R2004+ 容器真实 LZ77 数据页 ----

func TestCompressLZ77RealPageStreams(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) != ".dwg" {
			continue
		}
		data, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) < 6 || string(data[:6]) == "AC1015" ||
			string(data[:6]) == "AC1014" || string(data[:6]) == "AC1012" ||
			string(data[:6]) == "AC1021" {
			continue // R2000 家族无压缩段；R2007 走独立容器
		}
		streams, err := r2004PageStreams(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(streams) == 0 {
			continue
		}
		checkStreams(t, name, streams, compressLZ77, decompressLZ77)
		checked++
	}
	if checked < 30 {
		t.Fatalf("覆盖的 R2004+ 样本数异常: %d", checked)
	}
}

// r2004PageStreams 解析 R2004+ 容器页表/段表，切出每个压缩数据页的
// 原始 LZ77 压缩流（与 assembleSection 的页遍历同一路径）。
func r2004PageStreams(data []byte) ([]compStream, error) {
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
	lookup := make(map[uint32]pageSlot, len(pages))
	for _, p := range pages {
		if p.id > 0 {
			lookup[uint32(p.id)] = p
		}
	}
	var streams []compStream
	for i := range sections {
		sec := &sections[i]
		if sec.compressed != 2 || len(sec.pageIDs) == 0 {
			continue
		}
		for _, pageID := range sec.pageIDs {
			entry, ok := lookup[pageID]
			if !ok {
				continue
			}
			if int(entry.address)+32 > len(data) {
				return nil, fmt.Errorf("cad: 数据页头越界")
			}
			hb := unmaskPageHeader(data[entry.address:int(entry.address)+32], entry.address)
			if sig := binary.LittleEndian.Uint32(hb); sig != dataSectionMagic {
				continue
			}
			compSize := int(binary.LittleEndian.Uint32(hb[8:]))
			start := int(entry.address) + 32
			end := start + compSize
			if end > len(data) {
				return nil, fmt.Errorf("cad: 数据页内容越界")
			}
			streams = append(streams, compStream{
				src:     data[start:end],
				dstSize: int(sec.maxDecompressedSz),
			})
		}
	}
	return streams, nil
}

// ---- 互逆：R2007 容器真实 R21 流（第二头部体 + 系统页 + 数据页） ----

func TestCompressR21RealStreams(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) != ".dwg" {
			continue
		}
		data, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) < 6 || string(data[:6]) != "AC1021" {
			continue
		}
		streams, err := r2007R21Streams(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(streams) == 0 {
			continue
		}
		checkStreams(t, name, streams, compressR21, decompressR21)
		checked++
	}
	if checked < 5 {
		t.Fatalf("覆盖的 R2007 样本数异常: %d", checked)
	}
}

// r2007R21Streams 从 R2007 容器收集全部真实 R21 压缩流：
// 第二头部压缩体、页表/段表系统页与各段数据页（复刻读路径的 RS 去交织前置）。
func r2007R21Streams(data []byte) ([]compStream, error) {
	var streams []compStream

	// 第二头部：0x80 起 RS(239,3,method4) 去交织，0x20 偏移处为压缩体
	encoded := data[r2007SecondHeaderOffset : r2007SecondHeaderOffset+r2007SecondHeaderRSSize]
	decoded, err := r2007Deinterleave(encoded, 239, 3, 4)
	if err != nil {
		return nil, err
	}
	if compressedSize := int64(binary.LittleEndian.Uint32(decoded[24:])); compressedSize > 0 {
		streams = append(streams, compStream{
			src:     decoded[r2007SecondHeaderPayload : r2007SecondHeaderPayload+int(compressedSize)],
			dstSize: r2007SecondHeaderBodySize,
		})
	}

	hdr, err := decodeR2007Header(data)
	if err != nil {
		return nil, err
	}
	// 系统页压缩流（页表 / 段表）
	sysStream := func(address, sizeCompressed, sizeUncompressed, cf uint64) error {
		if sizeCompressed >= sizeUncompressed {
			return nil // 存储态非压缩流
		}
		compressedPadded := alignUp(sizeCompressed, r2007SysPageCRCBlock)
		blockCount := divCeil(compressedPadded*cf, r2007SysPageRSDataSize)
		pageSize := alignUp(blockCount*r2007SysPageRSCodeWord, r2007SysPageAlign)
		start, end := int(address), int(address+pageSize)
		if start > len(data) || end > len(data) || start > end {
			return nil
		}
		dec, err := r2007Deinterleave(data[start:end], 239, int(blockCount), 4)
		if err != nil {
			return nil
		}
		if sizeCompressed > uint64(len(dec)) {
			return nil
		}
		streams = append(streams, compStream{dec[:sizeCompressed], int(sizeUncompressed)})
		return nil
	}
	if err := sysStream(r2007StreamBaseOffset+hdr.pagesMapOffset, hdr.pagesMapSizeCompressed,
		hdr.pagesMapSizeUncompressed, hdr.pagesMapCorrectionFactor); err != nil {
		return nil, err
	}
	pages, err := parseR2007PageMap(data, hdr)
	if err != nil {
		return nil, err
	}
	for _, p := range pages {
		if p.id == int64(hdr.sectionsMapID) {
			if err := sysStream(p.address, hdr.sectionsMapSizeCompressed,
				hdr.sectionsMapSizeUncompressed, hdr.sectionsMapCorrectionFactor); err != nil {
				return nil, err
			}
			break
		}
	}
	// 数据页压缩流
	sections, err := parseR2007SectionMap(data, hdr, pages)
	if err != nil {
		return nil, err
	}
	lookup := make(map[int64]r2007PageSlot, len(pages))
	for _, p := range pages {
		lookup[p.id] = p
	}
	for si := range sections {
		sec := &sections[si]
		for _, pg := range sec.pages {
			if pg.sizeCompressed >= pg.sizeUncompressed {
				continue
			}
			entry, ok := lookup[int64(pg.id)]
			if !ok {
				continue
			}
			blockCount := r2007DataPageBlocks(pg.sizeCompressed)
			readSize := entry.size
			if minSize := r2007DataPageRSDataSize * blockCount; readSize < minSize {
				readSize = minSize
			}
			start, end := int(entry.address), int(entry.address+readSize)
			if start > len(data) || end > len(data) || start > end {
				continue
			}
			var dec []byte
			switch method := byte(sec.encoded); method {
			case 0:
				dec = append([]byte(nil), data[start:end]...)
			case 1, 4:
				dec, err = r2007Deinterleave(data[start:end], 251, int(blockCount), method)
				if err != nil {
					continue
				}
			default:
				continue
			}
			if pg.sizeCompressed > uint64(len(dec)) {
				continue
			}
			streams = append(streams, compStream{dec[:pg.sizeCompressed], int(pg.sizeUncompressed)})
		}
	}
	return streams, nil
}

// ---- 压缩率观察（真实样本段，输出到测试日志） ----

func TestCompressRatioSampleSummary(t *testing.T) {
	var lzPlain, lzComp, r21Plain, r21Comp int
	entries, _ := os.ReadDir("testdata")
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) != ".dwg" {
			continue
		}
		data, err := os.ReadFile("testdata/" + name)
		if err != nil {
			continue
		}
		if len(data) >= 6 && string(data[:6]) != "AC1021" && string(data[:6]) != "AC1015" &&
			string(data[:6]) != "AC1014" && string(data[:6]) != "AC1012" {
			if streams, err := r2004PageStreams(data); err == nil {
				for _, s := range streams {
					if plain, err := decompressLZ77(s.src, s.dstSize); err == nil {
						lzPlain += len(plain)
						lzComp += len(compressLZ77(plain))
					}
				}
			}
		}
		if len(data) >= 6 && string(data[:6]) == "AC1021" {
			if streams, err := r2007R21Streams(data); err == nil {
				for _, s := range streams {
					if plain, err := decompressR21(s.src, s.dstSize); err == nil {
						r21Plain += len(plain)
						r21Comp += len(compressR21(plain))
					}
				}
			}
		}
	}
	if lzPlain > 0 {
		t.Logf("LZ77 真实段: %d 字节 → %d 字节（%.1f%%）", lzPlain, lzComp, float64(lzComp)/float64(lzPlain)*100)
	}
	if r21Plain > 0 {
		t.Logf("R21 真实段: %d 字节 → %d 字节（%.1f%%）", r21Plain, r21Comp, float64(r21Comp)/float64(r21Plain)*100)
	}
	if lzPlain == 0 || r21Plain == 0 {
		t.Fatal("真实段收集为空")
	}
	// 兜底防呆：压缩结果不应超过合理的膨胀上限（格式开销 + 无法匹配时的字面量直写）
	if lzComp > lzPlain+lzPlain/2 {
		t.Fatalf("LZ77 压缩膨胀异常: %d → %d", lzPlain, lzComp)
	}
	if r21Comp > r21Plain+r21Plain/2 {
		t.Fatalf("R21 压缩膨胀异常: %d → %d", r21Plain, r21Comp)
	}
}
