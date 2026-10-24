// api_pdf_test.go PDF 导出的结构化测试：良构校验器（对应 SVG 测试的
// svgWellFormedErr 角色，自研轻量 xref/流长度校验，不依赖外部工具） +
// 单页/多页/确定性/全样本冒烟。字形路径细节测试在有系统字体时才详查
// （CI 无 CJK 字体环境下回退占位框路径同样合法）。
package render

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/unitedrhino/go-cad/internal/drawing"
	"github.com/unitedrhino/go-cad/internal/testsupport"
)

// pdfWellFormedErr PDF 良构校验：header/EOF、startxref→xref 偏移逐对象
// 回验、流 Length 声明与实际字节数一致、Catalog/Pages 存在。单测内即可
// 捕获 xref 偏移错位、流长度不符、字典残缺类序列化缺陷（对应 SVG 的
// XML 良构校验）。
func pdfWellFormedErr(pdf []byte) error {
	if !bytes.HasPrefix(pdf, []byte("%PDF-1.4\n")) {
		return fmt.Errorf("缺少 PDF header")
	}
	if !bytes.HasSuffix(pdf, []byte("%%EOF\n")) {
		return fmt.Errorf("缺少 %%EOF 结尾")
	}
	// startxref → xref 表偏移
	sx := bytes.LastIndex(pdf, []byte("startxref\n"))
	if sx < 0 {
		return fmt.Errorf("缺少 startxref")
	}
	nl := bytes.IndexByte(pdf[sx+10:], '\n')
	off, err := strconv.Atoi(string(pdf[sx+10 : sx+10+nl]))
	if err != nil {
		return fmt.Errorf("startxref 偏移非数字: %v", err)
	}
	if !bytes.HasPrefix(pdf[off:], []byte("xref")) {
		return fmt.Errorf("startxref 偏移 %d 处不是 xref 表", off)
	}
	// xref 条目：偏移回验每个对象头（头两行为 "xref" 与 "0 N"）
	xbody := pdf[off:]
	firstNl := bytes.IndexByte(xbody, '\n')
	secondNl := bytes.IndexByte(xbody[firstNl+1:], '\n')
	hdr := strings.Fields(string(xbody[:firstNl+1+secondNl])) // "xref" "0" "N"
	if len(hdr) != 3 {
		return fmt.Errorf("xref 头异常: %q", string(xbody[:firstNl]))
	}
	total, _ := strconv.Atoi(hdr[2])
	re := regexp.MustCompile(`(?m)^(\d{10}) 00000 n $`)
	entries := re.FindAllSubmatch(xbody, -1)
	if len(entries) != total-1 {
		return fmt.Errorf("xref 条目数 %d != Size-1 %d", len(entries), total-1)
	}
	for i, e := range entries {
		o, _ := strconv.Atoi(string(e[1]))
		want := []byte(strconv.Itoa(i+1) + " 0 obj")
		if !bytes.HasPrefix(pdf[o:], want) {
			return fmt.Errorf("对象 %d xref 偏移 %d 处不是 %q", i+1, o, want)
		}
	}
	// 流 Length 校验：/Length N 与实际压缩数据长度一致
	lre := regexp.MustCompile(`/Length (\d+) /Filter /FlateDecode >>\nstream\n`)
	for _, m := range lre.FindAllSubmatch(pdf, -1) {
		n, _ := strconv.Atoi(string(m[1]))
		start := bytes.Index(pdf, m[0]) + len(m[0])
		if !bytes.HasPrefix(pdf[start+n:], []byte("\nendstream")) {
			return fmt.Errorf("流 Length 声明 %d 与实际不符", n)
		}
	}
	if !bytes.Contains(pdf, []byte("/Type /Catalog")) {
		return fmt.Errorf("缺少 Catalog")
	}
	if !bytes.Contains(pdf, []byte("/Type /Pages")) {
		return fmt.Errorf("缺少 Pages")
	}
	return nil
}

// pdfWellFormed pdfWellFormedErr 的测试断言包装。
func pdfWellFormed(t *testing.T, pdf []byte) {
	t.Helper()
	if err := pdfWellFormedErr(pdf); err != nil {
		t.Fatal(err)
	}
}

// pdfContentStreams 解出全部 Flate 内容流（解压），供 content 断言。
func pdfContentStreams(t *testing.T, pdf []byte) [][]byte {
	t.Helper()
	lre := regexp.MustCompile(`/Length (\d+) /Filter /FlateDecode >>\nstream\n`)
	var out [][]byte
	for _, m := range lre.FindAllSubmatch(pdf, -1) {
		n, _ := strconv.Atoi(string(m[1]))
		start := bytes.Index(pdf, m[0]) + len(m[0])
		zr, err := zlib.NewReader(bytes.NewReader(pdf[start : start+n]))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(zr)
		if err != nil {
			t.Fatal(err)
		}
		_ = zr.Close()
		out = append(out, raw)
	}
	return out
}

// pdfPageCount 统计页对象数（/Type /Page 排除 /Pages）。
func pdfPageCount(pdf []byte) int {
	return len(regexp.MustCompile(`/Type /Page[^s]`).FindAll(pdf, -1))
}

// TestRenderPDFStructure 校验单页 PDF 骨架：header、Catalog/Pages/Page、
// MediaBox、背景填充与线段描边操作符、良构校验通过。
func TestRenderPDFStructure(t *testing.T) {
	pdf, err := RenderPDF(svgSynthDoc(), RenderOptions{Width: 1000})
	if err != nil {
		t.Fatal(err)
	}
	pdfWellFormed(t, pdf)
	if n := pdfPageCount(pdf); n != 1 {
		t.Fatalf("页数 = %d，want 1", n)
	}
	if !bytes.Contains(pdf, []byte("/MediaBox [0 0 ")) {
		t.Fatal("缺少 MediaBox")
	}
	streams := pdfContentStreams(t, pdf)
	if len(streams) != 1 {
		t.Fatalf("内容流数 = %d，want 1", len(streams))
	}
	c := streams[0]
	// 背景矩形填充 + 线段描边（后缀语法：数字在前、操作符在后）
	for _, want := range []string{" re f\n", " RG", " w", " m", " l\n", "S\n"} {
		if !bytes.Contains(c, []byte(want)) {
			t.Fatalf("content 缺少 %q\n%s", want, c[:400])
		}
	}
}

// TestRenderPDFContentSyntax 逐 token 重放 content stream 语法：每个操作符
// 前必须已压入足量操作数（PDF 后缀语法），操作符后的 token 不能粘连成
// 未知词。防 m/l/c 顺序类缺陷回归（曾因 SVG 前缀习惯引入）。
func TestRenderPDFContentSyntax(t *testing.T) {
	pdf, err := RenderPDF(svgSynthDoc(), RenderOptions{Width: 1000})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range pdfContentStreams(t, pdf) {
		if err := pdfContentSyntaxErr(c); err != nil {
			t.Fatal(err)
		}
	}
}

// pdfContentSyntaxErr 单条内容流的操作数栈重放校验。
func pdfContentSyntaxErr(c []byte) error {
	need := map[string]int{"m": 2, "l": 2, "c": 6, "re": 4, "w": 1, "rg": 3, "RG": 3, "f": 0, "S": 0, "h": 0}
	stack := 0
	for _, tok := range strings.Fields(string(c)) {
		if n, ok := need[tok]; ok {
			if stack < n {
				return fmt.Errorf("操作符 %q 缺操作数（栈 %d want %d）", tok, stack, n)
			}
			stack -= n
			continue
		}
		if _, err := strconv.ParseFloat(tok, 64); err != nil {
			return fmt.Errorf("未知 token %q", tok)
		}
		stack++
	}
	return nil
}

// TestRenderPDFGlyphPaths 有系统矢量字体时校验文本轮廓填充路径存在
// （f 填充 + 曲线 c 段——"HELLO" 的字形轮廓含曲线）；无字体环境跳过
// （CI 无 CJK 字体，回退占位框路径由冒烟测试兜底）。
func TestRenderPDFGlyphPaths(t *testing.T) {
	if len(newPDFShaper("").fonts) == 0 {
		t.Skip("无可用 opentype 字体，跳过字形路径断言")
	}
	pdf, err := RenderPDF(svgSynthDoc(), RenderOptions{Width: 1000})
	if err != nil {
		t.Fatal(err)
	}
	c := pdfContentStreams(t, pdf)[0]
	if !bytes.Contains(c, []byte("f\n")) {
		t.Fatal("content 无填充路径（文本未输出轮廓）")
	}
	if !bytes.Contains(c, []byte(" c\n")) {
		t.Fatal("content 无曲线段（字形轮廓应含二次曲线升级的 cubic）")
	}
}

// TestRenderPDFDeterministic 同一文档两次渲染字节一致（对象顺序/数字
// 格式化/压缩均确定，无时间戳），支撑字节级回归。
func TestRenderPDFDeterministic(t *testing.T) {
	doc := svgSynthDoc()
	a, err := RenderPDF(doc, RenderOptions{Width: 1000})
	if err != nil {
		t.Fatal(err)
	}
	b, err := RenderPDF(doc, RenderOptions{Width: 1000})
	if err != nil {
		t.Fatal(err)
	}
	ha, hb := sha256.Sum256(a), sha256.Sum256(b)
	if !bytes.Equal(ha[:], hb[:]) {
		t.Fatalf("两次渲染不一致: %s vs %s", hex.EncodeToString(ha[:8]), hex.EncodeToString(hb[:8]))
	}
}

// TestRenderSheetsPDFMultiPage 多图框拆分：首页整图全览 + 逐图框一页，
// 页数与书签数一致，书签标题以 UTF-16BE 十六进制串承载中文图名。
func TestRenderSheetsPDFMultiPage(t *testing.T) {
	doc := buildSheetDoc(t, []struct {
		h        uint64
		name     string
		title    string
		offset   [2]float64
		W, hgt   float64
		scale    float64
		rotation float64
	}{
		aSeriesSheet(0xA1, "SHEET1", "首层平面图", [2]float64{0, 0}),
		aSeriesSheet(0xA2, "SHEET2", "二层平面图", [2]float64{2000, 0}),
		aSeriesSheet(0xA3, "SHEET3", "屋面平面图", [2]float64{4000, 0}),
	})
	sheets := DetectSheets(doc)
	if len(sheets) < 2 {
		t.Fatalf("合成文档未识别出多图框: %d", len(sheets))
	}
	pdf, err := RenderSheetsPDF(doc, RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pdfWellFormed(t, pdf)
	want := len(sheets) + 1 // 整图全览首页（口径同 RenderAllSheets）
	if n := pdfPageCount(pdf); n != want {
		t.Fatalf("页数 = %d，want %d（全览 + %d 图框）", n, want, len(sheets))
	}
	if !bytes.Contains(pdf, []byte("/Type /Outlines")) {
		t.Fatal("多页 PDF 缺少书签树")
	}
	if !bytes.Contains(pdf, []byte("/PageMode /UseOutlines")) {
		t.Fatal("缺少 /PageMode /UseOutlines（打开即显示书签）")
	}
	// 中文图名走 UTF-16BE hex 串（BOM FEFF）
	if bytes.Count(pdf, []byte("<FEFF")) < len(sheets) {
		t.Fatalf("书签标题 hex 串数量不足: %d", bytes.Count(pdf, []byte("<FEFF")))
	}
}

// TestRenderSheetPDFSingle 单图框单页 PDF：无书签（单页无导航意义）、
// 良构、MediaBox 为图框比例。
func TestRenderSheetPDFSingle(t *testing.T) {
	doc := buildSheetDoc(t, []struct {
		h        uint64
		name     string
		title    string
		offset   [2]float64
		W, hgt   float64
		scale    float64
		rotation float64
	}{aSeriesSheet(0xA1, "SHEET1", "首层平面图", [2]float64{0, 0})})
	sheets := DetectSheets(doc)
	if len(sheets) != 1 {
		t.Fatalf("合成单图框识别异常: %d", len(sheets))
	}
	pdf, err := RenderSheetPDF(doc, sheets[0], RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pdfWellFormed(t, pdf)
	if n := pdfPageCount(pdf); n != 1 {
		t.Fatalf("页数 = %d，want 1", n)
	}
	if bytes.Contains(pdf, []byte("/Outlines")) {
		t.Fatal("单页 PDF 不应有书签")
	}
}

// TestRenderPDFNilDoc nil 文档防御：返回错误不 panic（与其他导出 API 口径一致）。
func TestRenderPDFNilDoc(t *testing.T) {
	for name, fn := range map[string]func() error{
		"RenderPDF": func() error { _, err := RenderPDF(nil, RenderOptions{}); return err },
		"RenderSheetPDF": func() error {
			_, err := RenderSheetPDF(nil, Sheet{}, RenderOptions{})
			return err
		},
		"RenderSheetsPDF": func() error { _, err := RenderSheetsPDF(nil, RenderOptions{}); return err },
	} {
		if err := fn(); err == nil {
			t.Fatalf("%s: nil 文档未返回错误", name)
		}
	}
}

// TestRenderPDFAllSamples 全样本冒烟：testdata 全部 DWG 渲染为整图 PDF
// 与拆图多页 PDF，良构校验通过（对齐 TestRenderSVGAllSamples 口径）。
func TestRenderPDFAllSamples(t *testing.T) {
	files, _ := filepath.Glob(testsupport.TestdataPath("*.dwg"))
	if len(files) == 0 {
		t.Skip("无样本")
	}
	ok, fail := 0, 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		doc, err := drawing.Parse(data)
		if err != nil {
			fail++
			t.Errorf("%s: parse: %v", f, err)
			continue
		}
		pdf, err := RenderPDF(doc, RenderOptions{Width: 800})
		if err != nil {
			fail++
			t.Errorf("%s: RenderPDF: %v", f, err)
			continue
		}
		if err := pdfWellFormedErr(pdf); err != nil {
			fail++
			t.Errorf("%s: 良构: %v", f, err)
			continue
		}
		for _, c := range pdfContentStreams(t, pdf) {
			if err := pdfContentSyntaxErr(c); err != nil {
				fail++
				t.Errorf("%s: content 语法: %v", f, err)
				break
			}
		}
		ok++
	}
	if fail > 0 {
		t.Fatalf("冒烟失败 %d / %d", fail, ok+fail)
	}
	t.Logf("整图 PDF 冒烟 %d 样本通过", ok)
}

// pdfMediaBoxOf 提取 PDF 首个 MediaBox 的宽高（pt），供幅面断言。
func pdfMediaBoxOf(t *testing.T, pdf []byte) (float64, float64) {
	t.Helper()
	// MediaBox 序列化时字面前缀与 appendPDFNum 的前导空格叠加，数值前
	// 是双空格，正则按"一或多个空格"匹配
	m := regexp.MustCompile(`/MediaBox \[0 0 +([\d.]+) +([\d.]+)\]`).FindSubmatch(pdf)
	if m == nil {
		t.Fatal("未找到 MediaBox")
	}
	w, err := strconv.ParseFloat(string(m[1]), 64)
	if err != nil {
		t.Fatal(err)
	}
	h, err := strconv.ParseFloat(string(m[2]), 64)
	if err != nil {
		t.Fatal(err)
	}
	return w, h
}

// TestRenderSheetPDFStdPage A0-A4 标准幅面吸附（RenderOptions.PageStdA）：
// 按标准幅面 1:1 绘制的图框吸附出同幅面物理页。A 系各档宽高比同为 √2，
// 分档依赖尺寸判据（纯比例会一律命中表首 A0），故 A1 档（841×594）与
// A0 档（aSeriesSheet 的 1189×841）各验一例；默认关闭与非标准比例
// 保持毫米直折现行为（100×30 低于图框识别最短边下限，走不了
// RenderSheetPDF 链路，以 pdfPageLayout 纯函数直测回退路径）。
func TestRenderSheetPDFStdPage(t *testing.T) {
	// buildStdSheet 构造指定幅面的单图框文档并取识别结果
	buildStdSheet := func(w, hgt float64) (*drawing.Document, Sheet) {
		doc := buildSheetDoc(t, []struct {
			h        uint64
			name     string
			title    string
			offset   [2]float64
			W, hgt   float64
			scale    float64
			rotation float64
		}{{h: 100, name: "SHEET1", title: "平面图", offset: [2]float64{0, 0}, W: w, hgt: hgt, scale: 1}})
		sheets := DetectSheets(doc)
		if len(sheets) != 1 {
			t.Fatalf("单图框识别异常: %d", len(sheets))
		}
		return doc, sheets[0]
	}
	const tol = 2 // pt：MediaBox 定点 2 位输出 + 页面折算舍入余量

	// 841×594（A1 横向 1:1 绘制）+ 吸附 → A1 物理页（841mm/594mm 折算 pt）
	docA1, sheetA1 := buildStdSheet(841, 594)
	pdf, err := RenderSheetPDF(docA1, sheetA1, RenderOptions{PageStdA: true})
	if err != nil {
		t.Fatal(err)
	}
	pdfWellFormed(t, pdf)
	w, h := pdfMediaBoxOf(t, pdf)
	if dw, dh := w-841*pdfMM2PT, h-594*pdfMM2PT; math.Abs(dw) > tol || math.Abs(dh) > tol {
		t.Fatalf("A1 图框吸附后 MediaBox = %.2f×%.2f，want ≈%.2f×%.2f（偏差 %.2f/%.2f）",
			w, h, 841*pdfMM2PT, 594*pdfMM2PT, dw, dh)
	}

	// aSeriesSheet 构造（1189×841：A 系 √2 比例、A0 档 1:1 尺寸）+ 吸附
	// → 按尺寸分档吸附到 A0 而非 A1
	docA0 := buildSheetDoc(t, []struct {
		h        uint64
		name     string
		title    string
		offset   [2]float64
		W, hgt   float64
		scale    float64
		rotation float64
	}{aSeriesSheet(100, "SHEET1", "平面图", [2]float64{0, 0})})
	sheetsA0 := DetectSheets(docA0)
	if len(sheetsA0) != 1 {
		t.Fatalf("aSeriesSheet 单图框识别异常: %d", len(sheetsA0))
	}
	pdf, err = RenderSheetPDF(docA0, sheetsA0[0], RenderOptions{PageStdA: true})
	if err != nil {
		t.Fatal(err)
	}
	w, h = pdfMediaBoxOf(t, pdf)
	if dw, dh := w-1189*pdfMM2PT, h-841*pdfMM2PT; math.Abs(dw) > tol || math.Abs(dh) > tol {
		t.Fatalf("A0 图框吸附后 MediaBox = %.2f×%.2f，want ≈%.2f×%.2f（偏差 %.2f/%.2f）",
			w, h, 1189*pdfMM2PT, 841*pdfMM2PT, dw, dh)
	}

	// 默认关闭（PageStdA=false）：A1 图框保持毫米直折现行为
	//（视口 = 图框包围盒每边外扩 sheetViewportMargin，宽高各 +4%）
	pdf, err = RenderSheetPDF(docA1, sheetA1, RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	w, h = pdfMediaBoxOf(t, pdf)
	wantW := 841 * (1 + 2*sheetViewportMargin) * pdfMM2PT
	wantH := 594 * (1 + 2*sheetViewportMargin) * pdfMM2PT
	if dw, dh := w-wantW, h-wantH; math.Abs(dw) > tol || math.Abs(dh) > tol {
		t.Fatalf("默认关闭时 MediaBox = %.2f×%.2f，want 直折 ≈%.2f×%.2f", w, h, wantW, wantH)
	}

	// 非标准比例（100×30）不吸附：回退毫米直折（k/原点均为直折口径；
	// 页面尺寸用微小容差比较——编译期常量乘法与运行时乘法存在 1 ulp
	// 双舍入差，位级比较会误报）
	pw, ph, k, offX, offY := pdfPageLayout(drawing.Box2{MinX: 0, MinY: 0, MaxX: 100, MaxY: 30}, true)
	if math.Abs(pw-100*pdfMM2PT) > 1e-6 || math.Abs(ph-30*pdfMM2PT) > 1e-6 ||
		k != pdfMM2PT || offX != 0 || offY != 0 {
		t.Fatalf("非标准比例应回退直折: got %.2f×%.2f k=%v off=(%v,%v)", pw, ph, k, offX, offY)
	}

	// 竖向图框（594×841，含外扩）同样吸附：比较归一到横向、页面方向
	// 跟随视口（宽 < 高 → 竖向 A1 幅面）
	pw, ph, _, _, _ = pdfPageLayout(
		drawing.Box2{MinX: 10, MinY: 20, MaxX: 10 + 594*(1+2*sheetViewportMargin), MaxY: 20 + 841*(1+2*sheetViewportMargin)}, true)
	if dw, dh := pw-594*pdfMM2PT, ph-841*pdfMM2PT; math.Abs(dw) > tol || math.Abs(dh) > tol {
		t.Fatalf("竖向 A1 吸附 MediaBox = %.2f×%.2f，want ≈%.2f×%.2f", pw, ph, 594*pdfMM2PT, 841*pdfMM2PT)
	}
}
