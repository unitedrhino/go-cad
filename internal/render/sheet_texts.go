// sheet_texts.go 实现图纸文本收集与按图框归属（SheetText + SheetTexts）：
// 面向 docling 等下游文本抽取场景，把模型空间全部文字（TEXT/MTEXT/ATTRIB，
// 含 INSERT 块引用内部文本）导出为「内容 + 世界坐标 + 字高 + 旋转 + 图层名
// + 所属图框序号」的平铺清单。图框复用 DetectSheets 的识别结果，归属判定
// 只做插入点落框（与图框识别的框内内容判定同口径），不重复实现图框识别。
//
// 主要模块划分：
//   - SheetText：单条文本的导出结构（Sheet 为 1-based 图框序号，0=框外）
//   - SheetTexts：全量展开收集 + 逐条归属（收集与渲染同一展开管线，
//     保证文本清单与出图内容一致）
package render

import (
	"math"
	"strings"

	"github.com/unitedrhino/go-cad/internal/drawing"
)

// SheetText 单条图纸文本及其图框归属（docling 集成的文本导出单元）。
type SheetText struct {
	Text     string  // 文本内容（MTEXT 已剥离格式码，多行以 \n 连接）
	X, Y     float64 // 插入点世界坐标（块内文本已经 INSERT 变换到世界系）
	Height   float64 // 世界字高（含块引用缩放，与渲染清晰度自检同口径）
	Rotation float64 // 基线旋转角（度，0~360；含块引用旋转与镜像后的实际方向）
	Layer    string  // 图层名（层表解析失败或未知名时为空串，不阻塞导出）
	Sheet    int     // 所属图框序号：1-based 对应 DetectSheets 输出顺序；0=不在任何图框内
}

// SheetTexts 收集文档全部文本并按图框归属。sheets 为 DetectSheets 的输出
// （调用方负责先识别；无图框兜底整图单张的场景由调用方剔除后传 nil/空，
// 门面 cad.SheetTexts 约定此时全部文本 Sheet=0）。
//
// 收集口径与渲染完全一致：复用 Tessellator 全量展开（INSERT 递归展开含
// 块变换、ATTRIB 属性文本按插入变换展开），取文字占位图元（label，含真实
// 字形版式信息）——因此块引用内部文本天然覆盖，且文本清单与出图内容同源；
// 边界：展开预算耗尽或块引用环被短路时，深处文本与渲染同样缺失（渲染也
// 画不出的文本不进清单，保证两者一致）。占位条回退路径（无真实文本的
// label）与空白文本跳过。
func SheetTexts(doc *drawing.Document, sheets []Sheet) []SheetText {
	if doc == nil {
		return nil
	}
	ts := drawing.NewTessellator(doc)
	prims := ts.ExpandAll()
	out := make([]SheetText, 0, len(prims)/4+1)
	for i := range prims {
		p := &prims[i]
		if p.Kind != 1 || p.Lb.Tx == nil {
			continue
		}
		text := strings.TrimSpace(p.Lb.Text)
		if text == "" || !drawing.Plausible(p.Lb.X, p.Lb.Y) {
			continue
		}
		// 世界字高与 sheetWidthFor/sheetTitle 同口径：推进基向量模长
		//（含块变换比例）× 局部字高
		h := math.Hypot(p.Lb.Tx.Ux, p.Lb.Tx.Uy) * p.Lb.Tx.HWorld
		out = append(out, SheetText{
			Text:     text,
			X:        p.Lb.X,
			Y:        p.Lb.Y,
			Height:   h,
			Rotation: sheetNormalizedDeg(p.Lb.Rot),
			Layer:    layerName(doc, p.Layer),
			Sheet:    sheetIndexOf(sheets, p.Lb.X, p.Lb.Y),
		})
	}
	return out
}

// layerName 图层句柄 → 层表真名（LayerColor.Name 由各版本 LAYER 记录解析
// 填充；句柄不在层表或层名未解析出时返回空串——导出不因层名缺失阻塞）。
func layerName(doc *drawing.Document, h uint64) string {
	return doc.LayerColors[h].Name
}

// sheetIndexOf 插入点落框归属：返回首个包含该点的图框序号（1-based，
// sheets 顺序即 DetectSheets 输出顺序；多框重叠时取序号最小者），框外
// 返回 0。含边界（与图框内容密度判定的闭区间口径一致）。
func sheetIndexOf(sheets []Sheet, x, y float64) int {
	for i := range sheets {
		if pointInBox(x, y, drawing.Box2{MinX: sheets[i].Box[0], MinY: sheets[i].Box[1], MaxX: sheets[i].Box[2], MaxY: sheets[i].Box[3]}) {
			return i + 1
		}
	}
	return 0
}

// sheetNormalizedDeg 弧度 → 规范化到 [0,360) 的角度（负角与多圈归一，
// 导出口径稳定便于下游比对）。
func sheetNormalizedDeg(rad float64) float64 {
	if math.IsNaN(rad) || math.IsInf(rad, 0) {
		// 错位解码可能产生 NaN/Inf 弧度，归一前剔除
		return 0
	}
	deg := math.Mod(rad*180/math.Pi, 360)
	if deg < 0 {
		deg += 360
	}
	return deg
}
