// cad.go 是 go-cad 的门面包（facade）：解析 R9~R2018 全版本 DWG/DXF 为
// 文档模型，并提供 DWG 三代容器写出、DXF 写出、JSON 导入、渲染与文本
// 提取等公共 API。实现按职责分包在 internal/ 之下，本文件以类型别名与
// 函数转发保持既有外部使用方式零变化；各包符号经门面即可达。
package cad

import (
	"io"

	"github.com/unitedrhino/go-cad/internal/drawing"
	"github.com/unitedrhino/go-cad/internal/render"
	"github.com/unitedrhino/go-cad/internal/writer"
)

// Document 解析完成的 DWG 文档模型（实现见 internal/drawing）。
type Document = drawing.Document

// TextInfo 提取的图纸文本及位置（世界坐标）。
type TextInfo = drawing.TextInfo

// DumpEntityRow 单个实体的对照字段（键名与参考实现对齐）。
type DumpEntityRow = drawing.DumpEntityRow

// Entity 对外暴露的图元接口（几何访问用于渲染与包围盒计算）。
// bounds 为非导出方法：仅本包模型可满足，外部只读消费。
type Entity interface {
	bounds() drawing.Box2
}

// RenderOptions 渲染选项（尺寸/背景等）。
type RenderOptions = render.RenderOptions

// Sheet 图纸分幅定义。
type Sheet = render.Sheet

// SheetResult 单图纸渲染产出。
type SheetResult = render.SheetResult

// SheetText 单条图纸文本及其图框归属（docling 集成的文本导出单元）。
type SheetText = render.SheetText

// RenderPNG 将文档渲染为 PNG 字节流。
func RenderPNG(doc *Document, opts RenderOptions) ([]byte, error) {
	return render.RenderPNG(doc, opts)
}

// RenderSVG 将文档渲染为 SVG 字节流。
func RenderSVG(doc *Document, opts RenderOptions) ([]byte, error) {
	return render.RenderSVG(doc, opts)
}

// RenderSheetPNG 渲染单张图纸为 PNG。
func RenderSheetPNG(doc *Document, sheet Sheet, opts RenderOptions) ([]byte, error) {
	return render.RenderSheetPNG(doc, sheet, opts)
}

// RenderSheetSVG 渲染单张图纸为 SVG。
func RenderSheetSVG(doc *Document, sheet Sheet, opts RenderOptions) ([]byte, error) {
	return render.RenderSheetSVG(doc, sheet, opts)
}

// RenderAllSheets 渲染全部图纸（format 为 "png"/"svg"）。
func RenderAllSheets(doc *Document, opts RenderOptions, format string) ([]SheetResult, error) {
	return render.RenderAllSheets(doc, opts, format)
}

// DetectSheets 检测图纸分幅（图框识别）。
func DetectSheets(doc *Document) []Sheet { return render.DetectSheets(doc) }

// SheetTexts 收集图纸全部文本并按图框归属（docling 集成入口）：内部先
// DetectSheets 识别图框再逐条归属，Sheet 为 1-based 图框序号、0 表示不在
// 任何图框内。无图框图纸 DetectSheets 会兜底返回名为"整图"的整图单张
// （render 包内部约定），该兜底框不是真实图框、不参与归属——剔除后全部
// 文本 Sheet=0，与 RenderAllSheets 的兜底判定同口径。
func SheetTexts(doc *Document) []SheetText {
	sheets := DetectSheets(doc)
	if len(sheets) == 1 && sheets[0].Name == "整图" {
		sheets = nil
	}
	return render.SheetTexts(doc, sheets)
}

// SanitizeSheetName 规范化图纸名（文件名安全口径）。
func SanitizeSheetName(name string) string { return render.SanitizeSheetName(name) }

// WriteDwg 按文档携带的容器素材写出 DWG（R2000/R2004/R2007 家族自适应）。
func WriteDwg(doc *Document, w io.Writer) error { return writer.WriteDwg(doc, w) }

// WriteDwgR2000 写出 R2000（AC1015）家族 DWG（含同容器 R13/R14）。
func WriteDwgR2000(doc *Document, w io.Writer) error { return writer.WriteDwgR2000(doc, w) }

// WriteDwgR2004 写出 R2004（AC1018）家族 DWG。
func WriteDwgR2004(doc *Document, w io.Writer) error { return writer.WriteDwgR2004(doc, w) }

// WriteDwgR2007 写出 R2007（AC1021）DWG。
func WriteDwgR2007(doc *Document, w io.Writer) error { return writer.WriteDwgR2007(doc, w) }

// Parse 解析 DWG 字节流（R13~R2018 主流容器 + R9/R10/R11 pre-R13）。
func Parse(data []byte) (*Document, error) { return drawing.Parse(data) }

// ParseDXF 解析 DXF 字节流。
func ParseDXF(data []byte) (*Document, error) { return drawing.ParseDXF(data) }

// ParseJSON 解析 dwgread -O JSON 结构（导入路径）。
func ParseJSON(data []byte) (*Document, error) { return drawing.ParseJSON(data) }

// WriteDXF 将文档写出为 DXF 字节流。
func WriteDXF(doc *Document, w io.Writer) error { return drawing.WriteDXF(doc, w) }

// DumpEntities 输出模型空间全部实体的对照 JSON（handle/type/字段），
// 用于与参考实现输出做逐字段一致性对比。
func DumpEntities(data []byte) (string, error) { return drawing.DumpEntities(data) }

// DumpEntitiesDoc 文档级对照导出：ParseJSON 等外部构造的文档与 Parse
// 同口径（JSON 输入长尾段的输出对照入口）。
func DumpEntitiesDoc(doc *Document) (string, error) { return drawing.DumpEntitiesDoc(doc) }

// DebugObjectIndexExport 调试用：导出对象图。
func DebugObjectIndexExport(data []byte) ([]struct {
	Handle uint64
	Offset uint32
}, error) {
	return drawing.DebugObjectIndexExport(data)
}

// DebugRecord2 调试用：解析记录并返回 body。
func DebugRecord2(objectsData []byte, ref struct {
	Handle uint64
	Offset uint32
}, r2010Plus bool) (body []byte, bitOff uint64, size uint32, err error) {
	return drawing.DebugRecord2(objectsData, ref, r2010Plus)
}

// LoadNamedSectionDebug2 调试用：加载段数据。
func LoadNamedSectionDebug2(data []byte, name string) ([]byte, error) {
	return drawing.LoadNamedSectionDebug2(data, name)
}

// DebugLines 调试用：输出全部 LINE 几何（handle → 6 坐标），用于与参考实现对照。
func DebugLines(data []byte) map[uint64][6]float64 { return drawing.DebugLines(data) }

// DebugScanGoldLines 调试用：扫描全部原始对象图条目，
// 找出解码后坐标与 gold 匹配的 LINE 记录及其句柄/偏移。
func DebugScanGoldLines(data []byte, gold map[uint64][6]float64) []string {
	return drawing.DebugScanGoldLines(data, gold)
}

// DebugObjectBody 调试用：导出指定句柄对象的原始 body 位流与起始位偏移。
func DebugObjectBody(data []byte, handle uint64) (body []byte, bitOffset uint64, err error) {
	return drawing.DebugObjectBody(data, handle)
}
