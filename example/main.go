// example 是 cad 库的一键演示程序：无需任何参数，默认使用包内 testdata
// 样本完整走一遍「解析 → 文本提取 → PNG 渲染 → DWG 回写 → 再解析校验 →
// DXF 双向 → JSON 结构导出」全链路，产物落在当前目录（可用 -o 改输出目录）。
// 用法：
//
//	cd backend/share && go run ./cad/example              # 一键演示
//	go run ./cad/example -f ./cad/testdata/01-2.dwg      # 指定样本
//	go run ./cad/example -f ./cad/testdata/lw_example_r13.dwg -width 1024
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"time"

	cad "github.com/unitedrhino/go-cad"
)

func main() {
	in := flag.String("f", filepath.Join("testdata", "lw_example2018.dwg"), "输入 DWG/DXF 文件")
	outDir := flag.String("o", ".", "输出目录")
	width := flag.Int("width", 2048, "渲染 PNG 宽度")
	flag.Parse()

	data, err := os.ReadFile(*in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取失败: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "创建输出目录失败: %v\n", err)
		os.Exit(1)
	}
	base := filepath.Base(*in)
	fmt.Printf("=== cad 库全链路演示（%s，%d 字节）===\n\n", base, len(data))

	// ① 解析
	t0 := time.Now()
	doc, err := cad.Parse(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "解析失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[1] Parse          版本=%s  实体数=%d  跳过=%d  耗时=%v\n",
		doc.Version(), doc.EntityCount(), doc.Skipped(), time.Since(t0))

	// ② 文本提取
	texts := doc.Texts()
	fmt.Printf("[2] Texts          提取文本 %d 条", len(texts))
	if len(texts) > 0 {
		show := texts[0].Text
		if len([]rune(show)) > 30 {
			show = string([]rune(show)[:30]) + "…"
		}
		fmt.Printf("（首条: %s）", show)
	}
	fmt.Println()

	// ③ PNG 渲染
	t0 = time.Now()
	pngData, err := cad.RenderPNG(doc, cad.RenderOptions{Width: *width})
	must(err, "渲染失败")
	pngPath := filepath.Join(*outDir, base+".png")
	must(os.WriteFile(pngPath, pngData, 0o644), "写出 PNG 失败")
	img, err := png.Decode(bytes.NewReader(pngData))
	must(err, "PNG 自检失败")
	fmt.Printf("[3] RenderPNG      %dx%d  %d 字节  耗时=%v  → %s\n",
		img.Bounds().Dx(), img.Bounds().Dy(), len(pngData), time.Since(t0), pngPath)

	// ④ DWG 回写 + 再解析校验（按文档版本自动分派六代容器）
	var buf bytes.Buffer
	t0 = time.Now()
	must(cad.WriteDwg(doc, &buf), "DWG 回写失败")
	dwgPath := filepath.Join(*outDir, base+".rewrite.dwg")
	must(os.WriteFile(dwgPath, buf.Bytes(), 0o644), "写出 DWG 失败")
	doc2, err := cad.Parse(buf.Bytes())
	must(err, "回写文件再解析失败")
	fmt.Printf("[4] WriteDwg       %d 字节  回读实体数一致=%v  耗时=%v  → %s\n",
		buf.Len(), doc2.EntityCount() == doc.EntityCount(), time.Since(t0), dwgPath)

	// ⑤ DXF 双向
	t0 = time.Now()
	var dxf bytes.Buffer
	must(cad.WriteDXF(doc, &dxf), "DXF 写出失败")
	dxfPath := filepath.Join(*outDir, base+".dxf")
	must(os.WriteFile(dxfPath, dxf.Bytes(), 0o644), "写出 DXF 失败")
	dxfDoc, err := cad.ParseDXF(dxf.Bytes())
	fmt.Printf("[5] WriteDXF/ParseDXF  DXF %d 字节  回读实体数=%d  耗时=%v  → %s\n",
		dxf.Len(), dxfDoc.EntityCount(), time.Since(t0), dxfPath)

	// ⑥ JSON 结构导出
	t0 = time.Now()
	js, err := cad.DumpEntities(data)
	must(err, "DumpEntities 失败")
	fmt.Printf("[6] DumpEntities   %d 字节 JSON  耗时=%v\n", len(js), time.Since(t0))

	fmt.Printf("\n全部完成，产物在 %s（*.png / *.rewrite.dwg / *.dxf）\n", *outDir)
}

func must(err error, what string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", what, err)
		os.Exit(1)
	}
}
