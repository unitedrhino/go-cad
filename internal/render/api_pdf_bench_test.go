// api_pdf_bench_test.go PDF 导出（RenderPDF / RenderSheetsPDF）性能基准。
//
// 测什么：端到端导出耗时——每次迭代包含 tessellate 展开、放射线/原点
// 锚定线过滤、文本字形矢量路径化（pdf_text.go）、content stream 发射与
// zlib 压缩，即调用方拿到 PDF 字节的全部成本；Parse 在计时外（口径同
// 顶层 bench_corpus_test.go 的渲染基准）。
//
// 样本选择（对齐 bench_corpus_test.go 的大中小档思路，按文件大小取档；
// 仓内 testdata 三档保证常跑，LibreDWG 语料大图档缺省 skip）：
//
//	小   testdata/line_2000.dwg        116 899 B（几何极简，管线固定成本基线）
//	中   testdata/lw_example2018.dwg   149 218 B（2018 代例图，实体/文本丰富）
//	大   testdata/lw_example2007.dwg   456 000 B（仓内最大样本之一，同源
//	     lw_example 系列取大者）
//	语料 corpus-s/fzw.dwg            8 165 070 B（505 中文 TEXT，文本字形
//	     路径化压力档；LibreDWG 语料目录缺失时该子基准自动 skip，口径
//	     同顶层 bench_corpus_test.go 的 benchSample）
//
// BenchmarkRenderPDFText 用合成文档做文本开销控制变量对比：等图元数的
// 纯 TEXT 与纯 LINE 两档，差值即字形轮廓化（quad→cubic 升级 + 逐字形
// 填充聚合）的净开销；无系统字体环境 TEXT 回退占位框，差值缩小但仍可比。
package render

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/unitedrhino/go-cad/internal/drawing"
	"github.com/unitedrhino/go-cad/internal/entity"
	"github.com/unitedrhino/go-cad/internal/testsupport"
)

// pdfBenchSink 防止编译器把被测调用优化掉：结果统一写入包级弃置变量
// （口径同顶层 bench_corpus_test.go 的 benchSinkData）。
var pdfBenchSink []byte

// pdfBenchSamplePath 单个基准样本的子基准名与磁盘路径。
type pdfBenchSamplePath struct {
	name string // 子基准名（文件基名去扩展，b.Run 按名展示）
	path string // 样本绝对/相对路径，按序回退取第一个可读者
}

// pdfBenchCorpusSDir LibreDWG corpus-s 语料目录：优先 testsupport 探测
// 结果下的 corpus-s 子目录，缺失时回落历史位置 /tmp（顶层
// bench_corpus_test.go 的 corpusSDataDir 同口径，但那是不导出的顶层包
// 函数，render 包内需自带一份）。
func pdfBenchCorpusSDir() string {
	dir := testsupport.LibredwgTestDataDir()
	if dir != "" {
		if info, err := os.Stat(filepath.Join(dir, "corpus-s")); err == nil && info.IsDir() {
			return filepath.Join(dir, "corpus-s")
		}
	}
	return "/tmp/libredwg/test/test-data/corpus-s"
}

// pdfBenchSamples 大中小三档 + 语料大图档：仓内 testdata 保证基准常跑，
// 语料档补足文本密集大图压力（不可读时由 pdfBenchLoadDoc skip）。
func pdfBenchSamples() []pdfBenchSamplePath {
	return []pdfBenchSamplePath{
		{"line_2000", testsupport.TestdataPath("line_2000.dwg")},
		{"lw_example2018", testsupport.TestdataPath("lw_example2018.dwg")},
		{"lw_example2007", testsupport.TestdataPath("lw_example2007.dwg")},
		{"fzw_corpus", filepath.Join(pdfBenchCorpusSDir(), "fzw.dwg")},
	}
}

// pdfBenchLoadDoc 读取并解析一个 DWG 样本为文档；不可读时 skip 当前
// 子基准（testdata 档保证存在，仅语料档可能触发 skip）。Parse 在计时
// 外完成，循环体内只测导出。
func pdfBenchLoadDoc(b *testing.B, path string) *drawing.Document {
	b.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		b.Skipf("样本不可用: %s: %v", path, err)
	}
	doc, err := drawing.Parse(data)
	if err != nil {
		b.Fatalf("Parse %s: %v", path, err)
	}
	return doc
}

// BenchmarkRenderPDF 整图单页 PDF 导出基准：每样本一个子基准（按文件
// 名命名），测展开 → 过滤 → 字形路径化 → 序列化压缩的端到端开销，
// 与顶层 BenchmarkRenderPNG* 四档口径互补（PDF 为矢量输出，无像素
// 缓冲固定成本，成本随图元/文本量变化）。
func BenchmarkRenderPDF(b *testing.B) {
	for _, s := range pdfBenchSamples() {
		b.Run(s.name, func(b *testing.B) {
			doc := pdfBenchLoadDoc(b, s.path)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				pdf, err := RenderPDF(doc, RenderOptions{})
				if err != nil {
					b.Fatalf("RenderPDF %s: %v", s.path, err)
				}
				pdfBenchSink = pdf
			}
		})
	}
}

// BenchmarkRenderSheetsPDF 多页拆图 PDF 导出基准：同款样本走
// RenderSheetsPDF（DetectSheets 识别图框 → 逐页视口裁剪渲染 → 书签树
// 组装）；无图框样本兜底"整图"单张，有图框样本额外多一页整图全览，
// 因此该基准同时覆盖图框识别与逐页裁剪路径的端到端成本。
func BenchmarkRenderSheetsPDF(b *testing.B) {
	for _, s := range pdfBenchSamples() {
		b.Run(s.name, func(b *testing.B) {
			doc := pdfBenchLoadDoc(b, s.path)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				pdf, err := RenderSheetsPDF(doc, RenderOptions{})
				if err != nil {
					b.Fatalf("RenderSheetsPDF %s: %v", s.path, err)
				}
				pdfBenchSink = pdf
			}
		})
	}
}

// pdfBenchTextDoc 合成纯文本文档：n 条固定混合文本（中文 + ASCII，
// 模拟工程图重复标注场景——同文本使字形轮廓缓存命中，量级由条数驱动）
// 按网格铺开（100 列），字高 5。
func pdfBenchTextDoc(n int) *drawing.Document {
	const text = "标注文字ABC123"
	model := make([]any, 0, n)
	for i := 0; i < n; i++ {
		model = append(model, &entity.EntText{
			BaseEntity: entity.BaseEntity{},
			Text:       text,
			Insertion:  entity.Point3{X: float64(i%100) * 40, Y: float64(i/100) * 20, Z: 0},
			Height:     5,
		})
	}
	return &drawing.Document{ModelSpace: model}
}

// pdfBenchLineDoc 合成纯线段文档：n 条等长 LINE 按同款网格铺开，与
// pdfBenchTextDoc 等图元数、同空间分布——控制变量后的对照组。
func pdfBenchLineDoc(n int) *drawing.Document {
	model := make([]any, 0, n)
	for i := 0; i < n; i++ {
		x, y := float64(i%100)*40, float64(i/100)*20
		model = append(model, &entity.EntLine{
			BaseEntity: entity.BaseEntity{},
			Start:      entity.Point3{X: x, Y: y, Z: 0},
			End:        entity.Point3{X: x + 30, Y: y + 5, Z: 0},
		})
	}
	return &drawing.Document{ModelSpace: model}
}

// BenchmarkRenderPDFText 文本字形路径化开销对比：TextHeavy（1000 条
// TEXT）与 LinesOnly（1000 条 LINE，等图元数对照）各渲染整图 PDF，
// 两者差值即字形轮廓化的净成本。有系统 opentype 字体时走真实字形
// 路径（本类环境即如此）；无字体环境 TEXT 回退占位框描边，差值缩小
// 为占位框几何成本，仍具回归对照意义。
func BenchmarkRenderPDFText(b *testing.B) {
	const n = 1000
	for _, c := range []struct {
		name string
		doc  *drawing.Document
	}{
		{"TextHeavy", pdfBenchTextDoc(n)},
		{"LinesOnly", pdfBenchLineDoc(n)},
	} {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				pdf, err := RenderPDF(c.doc, RenderOptions{})
				if err != nil {
					b.Fatalf("RenderPDF %s: %v", c.name, err)
				}
				pdfBenchSink = pdf
			}
		})
	}
}
