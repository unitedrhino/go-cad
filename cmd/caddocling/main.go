// main.go 实现 caddocling 命令行工具：面向 docling 集成的图纸出图与文本
// 导出一站式入口——按图框拆分逐张出图 + 整图预览 + manifest.json 清单
// （图纸文本按图框归属的平铺清单），下游据此做 RAG/版面还原无需再解析
// CAD 几何。
//
// 用法：
//
//	caddocling <input.dwg|.dxf|.dxfb> [-o outdir] [--width N] [--no-full-image] [--format png|svg]
//
// 参数说明：
//   - input：输入图纸文件路径（必填，与选项参数可任意顺序书写）；按扩展
//     名分派解析器——.dxf/.dxfb 走 cad.ParseDXF（ASCII 与二进制 DXF 均支
//     持），其余（.dwg）走 cad.Parse
//   - -o：输出目录，缺省为输入文件同目录 <源名>_caddocling/；不存在自动
//     创建
//   - --width：输出图片宽度（像素）；语义与 dwg2png 相同——整图缺省
//     2048、拆图缺省 0（渲染层按框内文字自适应起步 + 渲染后清晰度自检
//     自动加倍重渲，出图即清晰无需人工调参）
//   - --no-full-image：跳过整图预览 <源名>-full.<ext>；无图框兜底图纸
//     （拆图仅一张整图）时整图预览是唯一全览出口，仍强制产出
//   - --format：图片格式 png（缺省）或 svg；svg 时达标率未统计（pass_rate
//     为 -1）且无自动重渲
//
// 产出（写入 outdir）：
//   - manifest.json：UTF-8 清单（schema 见 manifestDoc，schema_version=1），
//     stdout 末行输出 "manifest: <绝对路径>" 供调用方解析
//   - 拆图 <源名>-<序号两位>-<图名>.<ext>：图框逐张出图（命名规则与
//     dwg2png -sheets 一致，图名经 cad.SanitizeSheetName 净化）；有图框时
//     RenderAllSheets 首项的"整图全览"单张不输出——整图预览由 full 图
//     承担，且跳过后拆图文件序号与 manifest texts 的图框序号（sheet 字段）
//     严格对齐，调用方无需做 +1 换算
//   - 整图 <源名>-full.<ext>：整图预览（--no-full-image 跳过；无图框兜底
//     时必须产出）
//
// 处理流程：读取文件 → 解析 → cad.SheetTexts 文本收集与图框归属 +
// cad.RenderAllSheets 拆图渲染 → 整图渲染 → 写 manifest.json。任一步骤
// 失败时错误信息输出到 stderr，进程以退出码 1 结束。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/unitedrhino/go-cad"
)

// manifestSchemaVersion manifest 合同版本：字段集锁死，新增字段才升版。
const manifestSchemaVersion = 1

// manifestDoc manifest.json 顶层结构（schema_version=1，字段名锁死）。
type manifestDoc struct {
	SchemaVersion int             `json:"schema_version"`
	Generator     string          `json:"generator"`
	Source        string          `json:"source"`
	DwgVersion    string          `json:"dwg_version"`
	Sheets        []manifestSheet `json:"sheets"`
	FullImage     string          `json:"full_image"`
	Texts         []manifestText  `json:"texts"`
}

// manifestSheet 单张拆图记录（index 即文件序号，与 texts[].sheet 对齐）。
type manifestSheet struct {
	Index     int     `json:"index"`
	Name      string  `json:"name"`
	Image     string  `json:"image"`
	Width     int     `json:"width"`
	PassRate  float64 `json:"pass_rate"`
	Rerenders int     `json:"rerenders"`
	TextCount int     `json:"text_count"`
}

// manifestText 单条图纸文本（世界坐标口径，见 cad.SheetText）。
type manifestText struct {
	Text     string  `json:"text"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Height   float64 `json:"height"`
	Rotation float64 `json:"rotation"`
	Layer    string  `json:"layer"`
	Sheet    int     `json:"sheet"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "caddocling:", err)
		os.Exit(1)
	}
}

// run 执行一次完整的「解析 → 拆图/整图渲染 → manifest 写出」流程，返回
// 需报告给用户的错误；进度与结果行写入 stdout（末行为 manifest 路径）。
// stdout 独立成参供测试捕获断言。
func run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("caddocling", flag.ContinueOnError)
	outDir := fs.String("o", "", "输出目录，缺省为输入同目录 <源名>_caddocling/")
	// 宽度缺省 0=按模式自动：整图 2048、拆图渲染层自适应+自动重渲（同 dwg2png）
	width := fs.Int("width", 0, "输出宽度（像素）；缺省整图 2048、拆图自适应")
	noFull := fs.Bool("no-full-image", false, "跳过整图预览（无图框兜底图纸仍强制产出）")
	format := fs.String("format", "png", "图片格式 png|svg")
	// flag 包遇到首个位置参数即停止解析，这里先重排参数：以 - 开头的选项
	// 连同其取值移到最前，使「caddocling input.dwg -o outdir」这类书写
	// 顺序可用；布尔型选项后不吞取值，避免误把位置参数当选项值
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && i+1 < len(args) && takesValue(fs, a) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positional = append(positional, a)
	}
	if err := fs.Parse(append(flags, positional...)); err != nil {
		return err
	}
	input := fs.Arg(0)
	if input == "" {
		return fmt.Errorf("缺少输入文件，用法: caddocling <input.dwg|.dxf|.dxfb> [-o outdir] [--width N] [--no-full-image] [--format png|svg]")
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("多余的参数: %s", fs.Arg(1))
	}
	switch strings.ToLower(*format) {
	case "png", "svg":
	default:
		return fmt.Errorf("不支持的格式 %q（可选 png/svg）", *format)
	}
	dir := *outDir
	if dir == "" {
		base := strings.TrimSuffix(filepath.Base(input), filepath.Ext(input))
		dir = filepath.Join(filepath.Dir(input), base+"_caddocling")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建输出目录 %s: %w", dir, err)
	}
	data, err := os.ReadFile(input)
	if err != nil {
		return fmt.Errorf("读取 %s: %w", input, err)
	}
	// 按扩展名分派解析器：.dxf（ASCII）/ .dxfb（二进制）走 DXF 读取，
	// 其余按 DWG 处理（大小写不敏感）
	var doc *cad.Document
	switch strings.ToLower(filepath.Ext(input)) {
	case ".dxf", ".dxfb":
		doc, err = cad.ParseDXF(data)
	default:
		doc, err = cad.Parse(data)
	}
	if err != nil {
		return fmt.Errorf("解析 %s: %w", input, err)
	}
	_, err = emit(doc, filepath.Base(input), dir, *width, *noFull, *format, stdout)
	return err
}

// emit 渲染出图并写 manifest.json：文档级核心流程独立成参，便于测试直接
// 构造文档（如图框归属场景）绕过文件往返。返回 manifest 绝对路径。
func emit(doc *cad.Document, source, dir string, width int, noFull bool, format string, stdout io.Writer) (string, error) {
	base := strings.TrimSuffix(source, filepath.Ext(source))
	ext := strings.ToLower(format)
	// 文本收集与归属先行（与拆图共用 DetectSheets 判定，序号口径一致）
	texts := cad.SheetTexts(doc)
	results, err := cad.RenderAllSheets(doc, cad.RenderOptions{Width: width}, format)
	if err != nil {
		return "", fmt.Errorf("图框切分渲染: %w", err)
	}
	// 有图框时 RenderAllSheets 首项固定为"整图全览"单张（render 包约定）：
	// 整图预览职责由 full 图承担，且跳过后拆图文件序号与 texts[].sheet
	// 序号严格对齐（图框 k 的文件即 <源名>-<k 两位>-<图名>），调用方无需
	// 换算。剩余情形（无图框兜底单张 / 极端下全览视口无效未追加）全部
	// 结果原序输出，序号仍与 DetectSheets 顺序一致
	overview := len(results) > 1 && results[0].Name == "整图全览"
	if overview {
		results = results[1:]
	}
	// 无图框兜底（拆图仅一张整图）时整图预览是唯一全览出口，--no-full-image
	// 不生效（fallbackWhole 覆盖跳过开关）
	fallbackWhole := len(results) == 1 && results[0].Name == "整图"
	m := manifestDoc{
		SchemaVersion: manifestSchemaVersion,
		Generator:     "go-cad caddocling",
		Source:        source,
		DwgVersion:    doc.Version(),
		Sheets:        make([]manifestSheet, 0, len(results)),
		FullImage:     "",
		Texts:         make([]manifestText, 0, len(texts)),
	}
	for _, t := range texts {
		m.Texts = append(m.Texts, manifestText{
			Text: t.Text, X: t.X, Y: t.Y, Height: t.Height,
			Rotation: t.Rotation, Layer: t.Layer, Sheet: t.Sheet,
		})
	}
	for i, r := range results {
		index := i + 1
		name := fmt.Sprintf("%s-%02d-%s.%s", base, index, cad.SanitizeSheetName(r.Name), ext)
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, r.Data, 0o644); err != nil {
			return "", fmt.Errorf("写出 %s: %w", path, err)
		}
		fmt.Fprintf(stdout, "拆图 %d/%d %q %d 宽 达标 %s%s -> %s（%d 字节）\n",
			index, len(results), r.Name, r.Width, passRateText(r.PassRate), rerendersText(r.Rerenders), path, len(r.Data))
		m.Sheets = append(m.Sheets, manifestSheet{
			Index: index,
			Name:  r.Name,
			Image: name,
			Width: r.Width,
			// SVG 矢量无像素概念，达标率未统计（-1）原样输出
			PassRate:  r.PassRate,
			Rerenders: r.Rerenders,
			TextCount: countSheetTexts(texts, index),
		})
	}
	// 整图预览：缺省宽 2048（同 dwg2png 整图口径）；无图框兜底时强制产出
	if !noFull || fallbackWhole {
		fullWidth := width
		if fullWidth <= 0 {
			fullWidth = 2048
		}
		var imgData []byte
		if ext == "svg" {
			imgData, err = cad.RenderSVG(doc, cad.RenderOptions{Width: fullWidth})
		} else {
			imgData, err = cad.RenderPNG(doc, cad.RenderOptions{Width: fullWidth})
		}
		if err != nil {
			return "", fmt.Errorf("渲染整图: %w", err)
		}
		name := fmt.Sprintf("%s-full.%s", base, ext)
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, imgData, 0o644); err != nil {
			return "", fmt.Errorf("写出 %s: %w", path, err)
		}
		m.FullImage = name
		fmt.Fprintf(stdout, "整图 %d 宽 -> %s（%d 字节）\n", fullWidth, path, len(imgData))
	}
	// texts 为空时输出 [] 而非 null（schema 稳定，下游无需判空分支）
	if m.Texts == nil {
		m.Texts = []manifestText{}
	}
	buf, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", fmt.Errorf("编码 manifest: %w", err)
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifestPath, append(buf, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("写出 %s: %w", manifestPath, err)
	}
	abs, err := filepath.Abs(manifestPath)
	if err != nil {
		abs = manifestPath
	}
	fmt.Fprintf(stdout, "manifest: %s\n", abs)
	return abs, nil
}

// countSheetTexts 统计归属到指定图框序号的文本条数（manifest text_count：
// 该拆图张对应图框内的文本量；框外文本 sheet=0 不计入任何张）。
func countSheetTexts(texts []cad.SheetText, sheet int) int {
	n := 0
	for _, t := range texts {
		if t.Sheet == sheet {
			n++
		}
	}
	return n
}

// takesValue 判断选项是否需要取值（布尔型选项返回 false；flag.Flag 不
// 暴露 IsBoolFlag，经 Value 接口断言识别）。
func takesValue(fs *flag.FlagSet, arg string) bool {
	f := fs.Lookup(strings.TrimLeft(arg, "-"))
	if f == nil {
		return false
	}
	bf, isBool := f.Value.(interface{ IsBoolFlag() bool })
	return !(isBool && bf.IsBoolFlag())
}

// rerendersText 自动重渲次数展示：0 次省略（一次成型），N>0 显示
// " 重渲 N 次"。
func rerendersText(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf(" 重渲 %d 次", n)
}

// passRateText 达标率展示：百分比取整（如 "76%"）；SVG 等未统计（<0）
// 显示 "--"。
func passRateText(rate float64) string {
	if rate < 0 {
		return "--"
	}
	return fmt.Sprintf("%.0f%%", rate*100)
}
