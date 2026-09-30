// bench_corpus_test.go —— cad 包性能基准（全量对齐批次 Y）。
//
// 覆盖四档样本的解析/渲染、六代 DWG 写出与大样本 Texts/DumpEntities，
// 全部走 `go test -bench` 标准口径，每个基准 b.ReportAllocs()。
// 样本四档（大小以 2026-09-28 stat 为准）：
//
//	小   testdata/line_2000.dwg      116 899 字节（仓内 testdata，保证常跑）
//	中   example_2013.dwg            146 906 字节（LibreDWG 语料，缺省 skip）
//	大   corpus-s/fzw.dwg          8 165 070 字节（505 个中文 TEXT）
//	特大 2018/Dynblocks.dwg        2 179 277 字节（与 2007/ATMOS-DC22S.dwg
//	    398 400 字节 stat 取大者）
//
// 写出六代样本：testdata/line_2000/2004/2007/2010/2013.dwg（同源 line 系列）
// 与 testdata/lw_example2018.dwg（2018 代无 line 同源样本）；写出均经
// WriteDwg 按文档版本自动分派（R2007 → R2007 容器，2004~2018 → R2004
// 容器，2000 → R2000 容器）。
//
// == 基准数据（2026-09-28，Intel Xeon Gold 6226R @ 2.90GHz 32 核 / 62 GiB 内存 / go1.26.0 linux/amd64，-count=3 取中位）==
//
//	解析（ns/op | B/op | allocs/op）
//	ParseSmall   117 KB   3.39 ms |  2.52 MB |     12 893
//	ParseMedium  147 KB  12.9  ms |  8.45 MB |     20 585
//	ParseLarge   8.2 MB  854   ms |  336  MB |  3 085 584
//	ParseXLarge  2.2 MB  426   ms |  169  MB |  1 519 614
//
//	渲染 RenderPNG（默认 2048 宽）
//	Small   160 ms | 17.7 MB |     42 allocs（栅格/PNG 缓冲为主）
//	Medium  294 ms |  168 MB |  6 029 allocs
//	Large   852 ms |  381 MB | 212 032 allocs
//	XLarge  326 ms |  157 MB | 243 332 allocs
//
//	写出 WriteDwg（Parse 一次后循环写出）
//	2000  2.68 ms | 517 KB | 26 allocs    2010  2.76 ms | 2.67 MB | 189 allocs
//	2004  3.39 ms | 2.97 MB | 210 allocs   2013  1.13 ms | 1.43 MB | 124 allocs
//	2007  7.31 ms | 3.46 MB | 226 allocs   2018  7.79 ms | 5.88 MB | 380 allocs
//
//	文本与实体转储（大/特大两档）
//	TextsLarge          265 µs | 154 KB |  1 555 allocs（fzw 505 中文 TEXT）
//	TextsXLarge         335 µs | 4.6 KB |     11 allocs（Dynblocks 几乎无文本）
//	DumpEntitiesLarge   960 ms | 366 MB |  3 385 932 allocs
//	DumpEntitiesXLarge  453 ms | 180 MB |  1 625 463 allocs
//
// 性能画像结论（供 WZ 批次优化参考，详见本批报告）：
//   - 解析吞吐约 6~10 MB/s（大/特大档），分配字节为输入的 20~40 倍，
//     allocs 集中在逐字段 objectRecord 解码路径（见 memprofile Top10）；
//   - 渲染对小样本有 ~160 ms 固定开销（2048 宽像素缓冲 + PNG 编码），
//     与实体数弱相关；
//   - 写出为回放式（对象记录原样回放），毫秒级、分配极少，不是瓶颈。
//
// 与 LibreDWG 0.14 dwgread（C 实现）的解析吞吐对比：dwgread 计时含进程
// 启动与 JSON 序列化开销，对比时以 `dwgread --help` 空跑中位数扣除基线；
// 对比表记录在本批 git 提交说明中，不在此硬编码易过期数字。
package cad

import (
	"bytes"
	"os"
	"testing"
)

// corpusSDataDir 返回 LibreDWG corpus-s 语料目录（大样本档专用）：
// 优先 libredwgTestDataDir() 下的 corpus-s 子目录；缺失时回落历史位置
// /tmp/libredwg/test/test-data/corpus-s。目录不存在时仍返回后者，
// 由调用方按"样本不可用"口径跳过，与 libredwgTestDataDir 一致。
func corpusSDataDir() string {
	dir := libredwgTestDataDir()
	if info, err := os.Stat(dir + "/corpus-s"); err == nil && info.IsDir() {
		return dir + "/corpus-s"
	}
	return "/tmp/libredwg/test/test-data/corpus-s"
}

// benchSample 按序返回第一个可读样本的路径与字节数；全部不可读时跳过
// 当前基准（testdata 内样本保证存在，故纯仓内基准永不跳过）。
func benchSample(b *testing.B, paths ...string) ([]byte, string) {
	b.Helper()
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err == nil {
			return data, p
		}
	}
	b.Skipf("样本不可用: %v", paths)
	return nil, ""
}

// 四档样本路径：仓内 testdata 直接相对路径；LibreDWG 语料走
// libredwgTestDataDir()/corpusSDataDir() 的缺省 skip 口径。
func benchSmallPath() string  { return "testdata/line_2000.dwg" }
func benchMediumPath() string { return libredwgTestDataDir() + "/example_2013.dwg" }
func benchLargePath() string  { return corpusSDataDir() + "/fzw.dwg" }
func benchXLargePath() string { return libredwgTestDataDir() + "/2018/Dynblocks.dwg" }

// benchSink 防止编译器把被测调用优化掉：结果统一写入包级弃置变量。
var (
	benchSinkDoc  *Document
	benchSinkInt  int
	benchSinkStr  string
	benchSinkData []byte
)

// ---- 解析基准：四档 ----

func benchParse(b *testing.B, path string) {
	b.Helper()
	data, _ := benchSample(b, path)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		doc, err := Parse(data)
		if err != nil {
			b.Fatalf("Parse %s: %v", path, err)
		}
		benchSinkDoc = doc
	}
}

func BenchmarkParseSmall(b *testing.B)  { benchParse(b, benchSmallPath()) }
func BenchmarkParseMedium(b *testing.B) { benchParse(b, benchMediumPath()) }
func BenchmarkParseLarge(b *testing.B)  { benchParse(b, benchLargePath()) }
func BenchmarkParseXLarge(b *testing.B) { benchParse(b, benchXLargePath()) }

// ---- 渲染基准：同四档（默认 2048 宽）----

func benchRenderPNG(b *testing.B, path string) {
	b.Helper()
	data, _ := benchSample(b, path)
	doc, err := Parse(data)
	if err != nil {
		b.Fatalf("Parse %s: %v", path, err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		png, err := RenderPNG(doc, RenderOptions{})
		if err != nil {
			b.Fatalf("RenderPNG %s: %v", path, err)
		}
		benchSinkData = png
	}
}

func BenchmarkRenderPNGSmall(b *testing.B)  { benchRenderPNG(b, benchSmallPath()) }
func BenchmarkRenderPNGMedium(b *testing.B) { benchRenderPNG(b, benchMediumPath()) }
func BenchmarkRenderPNGLarge(b *testing.B)  { benchRenderPNG(b, benchLargePath()) }
func BenchmarkRenderPNGXLarge(b *testing.B) { benchRenderPNG(b, benchXLargePath()) }

// ---- 写出基准：六代各一 ----
// 样本同源 line 系列（2018 代用 lw_example2018.dwg），Parse 一次后循环写出；
// WriteDwg 按 doc 版本自动分派三代容器写出器。

func benchWriteDwg(b *testing.B, path string) {
	b.Helper()
	data, _ := benchSample(b, path)
	doc, err := Parse(data)
	if err != nil {
		b.Fatalf("Parse %s: %v", path, err)
	}
	var buf bytes.Buffer
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		buf.Reset()
		if err := WriteDwg(doc, &buf); err != nil {
			b.Fatalf("WriteDwg %s: %v", path, err)
		}
		benchSinkInt = buf.Len()
	}
}

func BenchmarkWriteDwg2000(b *testing.B) { benchWriteDwg(b, "testdata/line_2000.dwg") }
func BenchmarkWriteDwg2004(b *testing.B) { benchWriteDwg(b, "testdata/line_2004.dwg") }
func BenchmarkWriteDwg2007(b *testing.B) { benchWriteDwg(b, "testdata/line_2007.dwg") }
func BenchmarkWriteDwg2010(b *testing.B) { benchWriteDwg(b, "testdata/line_2010.dwg") }
func BenchmarkWriteDwg2013(b *testing.B) { benchWriteDwg(b, "testdata/line_2013.dwg") }
func BenchmarkWriteDwg2018(b *testing.B) { benchWriteDwg(b, "testdata/lw_example2018.dwg") }

// ---- Texts / DumpEntities 基准：大样本两档 ----

func benchTexts(b *testing.B, path string) {
	b.Helper()
	data, _ := benchSample(b, path)
	doc, err := Parse(data)
	if err != nil {
		b.Fatalf("Parse %s: %v", path, err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		benchSinkInt = len(doc.Texts())
	}
}

func benchDumpEntities(b *testing.B, path string) {
	b.Helper()
	data, _ := benchSample(b, path)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s, err := DumpEntities(data)
		if err != nil {
			b.Fatalf("DumpEntities %s: %v", path, err)
		}
		benchSinkStr = s
	}
}

func BenchmarkTextsLarge(b *testing.B)         { benchTexts(b, benchLargePath()) }
func BenchmarkTextsXLarge(b *testing.B)        { benchTexts(b, benchXLargePath()) }
func BenchmarkDumpEntitiesLarge(b *testing.B)  { benchDumpEntities(b, benchLargePath()) }
func BenchmarkDumpEntitiesXLarge(b *testing.B) { benchDumpEntities(b, benchXLargePath()) }
