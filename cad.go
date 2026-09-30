// cad.go 是 go-cad 的门面包（facade）：解析 R9~R2018 全版本 DWG/DXF 为
// 文档模型，并提供 DWG 三代容器写出、DXF 写出、JSON 导入、渲染与文本
// 提取等公共 API。实现按职责分包在 internal/ 之下，本文件以类型别名与
// 函数转发保持既有外部使用方式零变化；各包符号经门面即可达。
package cad

import (
	"io"

	"github.com/unitedrhino/go-cad/internal/drawing"
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
