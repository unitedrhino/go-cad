// main.go 实现 dwg2png 命令行工具：把 AutoCAD DWG / DXF 图纸渲染为
// PNG 位图或 SVG 矢量图。
//
// 用法：
//
//	dwg2png <input.dwg|input.dxf> [-o out.png|out.svg] [-width 2048] [-sheets]
//
// 参数说明：
//   - input.dwg / input.dxf：输入图纸文件路径（必填，与选项参数可任意顺序
//     书写）；按扩展名分派解析器——.dxf/.dxfb 走 cad.ParseDXF（ASCII 与
//     二进制 DXF 均支持），其余（.dwg）走 cad.Parse
//   - -o：输出图片路径，缺省为输入路径去掉扩展名后加 .png；输出格式按
//     扩展名分派——.svg 走 cad.RenderSVG 矢量输出（文字以 <text> 元素由
//     查看器字体渲染，线条无限缩放不失真），其余走 cad.RenderPNG
//   - -width：输出图片宽度（像素）；未指定时整图模式默认 2048、-sheets
//     单图幅模式默认 4096（单图幅下字号像素高比整图大一个数量级）；高度
//     按图纸包围盒比例自适应，SVG 中决定线宽与固有尺寸基准（viewBox
//     保持世界坐标，缩放无损）
//   - -sheets：按图框切分逐张出图（cad.RenderAllSheets）——识别模型空间
//     平铺的标准图幅图框，每个图框渲染一张（无图框兜底整图单张），输出
//     `<输出名>-<序号>-<图名>.png/svg` 多文件，图名经文件名净化；单张
//     缺省按框内文字自适应起步并渲染后自检清晰度（文字 ≥8px 占比低于
//     60% 时自动加倍宽度重渲，画布像素超内存上限拒绝），stdout 逐张
//     汇总最终宽度与达标率，出图即清晰、无需人工调参选宽度
//
// 处理流程：读取文件 → cad.Parse / cad.ParseDXF 解析 → 按输出扩展名与
// -sheets 开关分派 cad.RenderPNG / cad.RenderSVG / cad.RenderAllSheets
// → 写出图片文件。任一步骤失败时错误信息输出到 stderr，进程以退出码 1 结束。
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/unitedrhino/go-cad"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "dwg2png:", err)
		os.Exit(1)
	}
}

// run 执行一次完整的「解析 → 渲染 → 写文件」流程，返回需报告给用户的错误。
func run(args []string) error {
	fs := flag.NewFlagSet("dwg2png", flag.ContinueOnError)
	out := fs.String("o", "", "输出图片路径（.svg 为矢量输出），缺省为输入路径去扩展名加 .png")
	// 宽度缺省 0=按模式自动：整图 2048、-sheets 单图幅 4096（文字清晰口径）
	width := fs.Int("width", 0, "输出宽度（像素）；缺省整图 2048、-sheets 模式 4096")
	sheets := fs.Bool("sheets", false, "按图框切分逐张出图（输出 <输入文件名>-<序号>-<图名>.png/svg 多文件）")
	// flag 包遇到首个位置参数即停止解析，这里先重排参数：以 - 开头的选项连同
	// 其取值移到最前，使「dwg2png input.dwg -o out.png」这类书写顺序可用；
	// 布尔型选项（-sheets）后不吞取值，避免误把位置参数当选项值
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
		return fmt.Errorf("缺少输入文件，用法: dwg2png <input.dwg> [-o out.png] [-width 2048] [-sheets]")
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("多余的参数: %s", fs.Arg(1))
	}
	output := *out
	if output == "" {
		output = strings.TrimSuffix(input, filepath.Ext(input)) + ".png"
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
	// -sheets 图框切分逐张出图：输出 <输入文件名>-<序号>-<图名>.<ext>
	// 多文件（前缀固定取输入 DWG 文件名，保证多图纸批量时对应关系明确；
	// -o 仅决定输出目录与格式扩展名）
	if *sheets {
		return renderSheets(doc, input, output, *width)
	}
	sheetWidth := *width
	if sheetWidth <= 0 {
		sheetWidth = 2048
	}
	// 输出格式按 -o 扩展名分派：.svg 矢量输出（文字/线条无限缩放清晰），
	// 其余（含缺省 .png）走 PNG 位图
	var imgData []byte
	if strings.EqualFold(filepath.Ext(output), ".svg") {
		imgData, err = cad.RenderSVG(doc, cad.RenderOptions{Width: sheetWidth})
	} else {
		imgData, err = cad.RenderPNG(doc, cad.RenderOptions{Width: sheetWidth})
	}
	if err != nil {
		return fmt.Errorf("渲染 %s: %w", input, err)
	}
	if err := os.WriteFile(output, imgData, 0o644); err != nil {
		return fmt.Errorf("写出 %s: %w", output, err)
	}
	fmt.Fprintf(os.Stdout, "%s -> %s（%d 字节）\n", input, output, len(imgData))
	return nil
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

// renderSheets 图框切分逐张出图：按 -o 扩展名分派格式，输出
// <输入文件名>-<序号>-<图名>.<ext> 多文件到 -o 所在目录（前缀取输入
// DWG 文件名去扩展名，多图纸批量时对应关系明确）；宽度 <=0 时由
// 渲染层按框内文字自适应起步，渲染后自检文字清晰达标率并自动加倍
// 宽度重渲（内存上限内），用户无需人工调参选宽度。stdout 逐张汇总
// 最终宽度与达标率（如 "RD-31 14839 宽 达标 76%"），不达标时渲染层
// 另行打印告警行。
func renderSheets(doc *cad.Document, input, output string, width int) error {
	format := "png"
	if strings.EqualFold(filepath.Ext(output), ".svg") {
		format = "svg"
	}
	results, err := cad.RenderAllSheets(doc, cad.RenderOptions{Width: width}, format)
	if err != nil {
		return fmt.Errorf("图框切分渲染: %w", err)
	}
	dir := filepath.Dir(output)
	base := strings.TrimSuffix(filepath.Base(input), filepath.Ext(input))
	for i, r := range results {
		name := fmt.Sprintf("%s-%02d-%s.%s", base, i+1, cad.SanitizeSheetName(r.Name), format)
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, r.Data, 0o644); err != nil {
			return fmt.Errorf("写出 %s: %w", path, err)
		}
		fmt.Fprintf(os.Stdout, "图框 %d/%d %q %d 宽 达标 %s%s -> %s（%d 字节）\n",
			i+1, len(results), r.Name, r.Width, passRateText(r.PassRate), rerendersText(r.Rerenders), path, len(r.Data))
	}
	fmt.Fprintf(os.Stdout, "共输出 %d 张（%s）\n", len(results), format)
	return nil
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
