// render_text_align_test.go 极限批次 R 回归测试：标题栏/会签栏文字叠印修复。
// 两项修复的口径守门（无案例资产也可运行）：
//  1. TEXT/ATTRIB vAlign=2（middle）字面 extent 中心对齐锚点——基线偏移
//     符号曾写反（+0.35h），公司名 TEXT 叠进上方轮廓字 Logo（消防施工图
//     实证：锚点 y=127432 墨带 [128594,129418]，正确应为 extent 中心≈锚点）。
//  2. MTEXT 行距 = linespace_factor×字高（DXF 44，gold extents_height 实证；
//     旧 1.66 常数使设计说明 11 行块底压进审定/审核签名栏）。factor 未存
//     （pre-R2000/JSON·DXF 未带）回退 1.66 历史口径。
package cad

import (
	"image"
	"image/color"
	"testing"
)

// inkBand 统计画布墨迹行的最小/最大行号（无墨迹返回 false）。
func inkBand(img *image.RGBA) (minY, maxY int, ok bool) {
	b := img.Bounds()
	first, last := -1, -1
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, _, _, _ := img.At(x, y).RGBA()
			if r>>8 < 200 {
				if first < 0 {
					first = y
				}
				last = y
				break
			}
		}
	}
	if first < 0 {
		return 0, 0, false
	}
	return first, last, true
}

// TestTextMiddleAlignExtentCenter TEXT vAlign=2 回归：middle 语义为字面
// extent 中心对齐锚点，墨迹带中心与锚点像素坐标偏差应远小于半个字高
// （修复前基线被抬到锚点上方 0.35h 再向上出墨，整体偏高 ~0.7 字高）。
func TestTextMiddleAlignExtentCenter(t *testing.T) {
	requireRenderFont(t)
	const w, h, em = 320, 160, 48.0
	draw := func(vAlign uint16) *image.RGBA {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		drawRect(img, img.Rect, color.RGBA{255, 255, 255, 255})
		cv := &canvas{img: img, width: w, height: h, lineWidth: 0.75}
		tr := newTextRenderer(cv)
		// 1 世界单位 = em 像素（hWorld=1，基向量承载像素比例，与真实
		// 管线 textLayoutOf 的 px/世界单位口径一致）
		l := textLayout{px: w / 2, py: h / 2, upx: em, vpy: -em, emPx: em, hWorld: 1, widthFactor: 1, hAlign: 1, vAlign: vAlign}
		tr.drawSingleLine(l, "中庚工程", 0, 0, color.RGBA{0, 0, 0, 255})
		return img
	}
	mid0, mid1, ok := inkBand(draw(2))
	if !ok {
		t.Fatal("middle 渲染无墨迹")
	}
	// extent 中心 = 锚点 → 墨带中点 ≈ py
	//（CJK 字面在 em 框内近似居中，容差 0.15×em）
	center := float64(mid0+mid1) / 2
	if d := center - h/2; d > 0.15*em || d < -0.15*em {
		t.Fatalf("middle 墨带中心偏离锚点 %.1fpx（>0.15×em=%.1f）：带 [%d,%d]", d, 0.15*em, mid0, mid1)
	}
	// 对照：基线模式（vAlign=0）墨带应整体在锚点上方（字形自基线上升），
	// 且 middle 带相对基线带整体下移 ≈0.35h——符号反转的直接证据
	b0, b1, ok := inkBand(draw(0))
	if !ok {
		t.Fatal("baseline 渲染无墨迹")
	}
	shift := (mid0 + mid1) - (b0 + b1) // 2×中心差
	if shift < 0 {
		t.Fatalf("middle 墨带应不低于 baseline 墨带（中心差 2×=%.0f）", float64(shift))
	}
}

// TestTextBaselineAnchoring TEXT vAlign=0 回归：基线锚定语义为字形坐在
// 锚点基线上——墨带底部应落在锚点 +0.15×em 内、字面高 ≈0.6~1.15×em。
// 旧 blitGlyph 口径 Cv=asc−dy 多计一段上延，全部文字整体抬高 ~1em
// （vAlign=0 实测墨带整体悬于基线上方 ~0.96em，红色注释错位实证）。
func TestTextBaselineAnchoring(t *testing.T) {
	requireRenderFont(t)
	const w, h, em = 320, 240, 48.0
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	drawRect(img, img.Rect, color.RGBA{255, 255, 255, 255})
	cv := &canvas{img: img, width: w, height: h, lineWidth: 0.75}
	tr := newTextRenderer(cv)
	l := textLayout{px: w / 2, py: h / 2, upx: em, vpy: -em, emPx: em, hWorld: 1, widthFactor: 1, hAlign: 1, vAlign: 0}
	tr.drawSingleLine(l, "中庚工程", 0, 0, color.RGBA{0, 0, 0, 255})
	b0, b1, ok := inkBand(img)
	if !ok {
		t.Fatal("渲染无墨迹")
	}
	anchor := h / 2
	if d := float64(b1 - anchor); d < -0.1*em || d > 0.15*em {
		t.Fatalf("墨带底 %d 偏离基线锚点 %d（%.2f×em）：字形未坐在基线上", b1, anchor, d/em)
	}
	if gh := float64(b1 - b0); gh < 0.6*em || gh > 1.15*em {
		t.Fatalf("字面高 %.2f×em 超出 CJK 合理范围", gh/em)
	}
}

// TestMTextLineAdvanceFactor MTEXT 行距回归：lineFactor=0.9 时相邻两行
// 墨带间距 ≈0.9×字高（世界=像素 1:1 版式），而非 1.66×0.9；factor 缺省
// 回退 1.66 历史口径。
func TestMTextLineAdvanceFactor(t *testing.T) {
	requireRenderFont(t)
	const em = 20.0
	draw := func(lineFactor float64) *image.RGBA {
		img := image.NewRGBA(image.Rect(0, 0, 600, 300))
		drawRect(img, img.Rect, color.RGBA{255, 255, 255, 255})
		cv := &canvas{img: img, width: 600, height: 300, lineWidth: 0.75}
		tr := newTextRenderer(cv)
		// 1 世界单位 = em 像素（基向量承载像素比例，与真实管线一致）
		l := textLayout{px: 20, py: 120, upx: em, vpy: -em, emPx: em, hWorld: 1, widthFactor: 1}
		tr.drawMText(l, &textInfo{
			lines:      []string{"一", "一"}, // 扁平横笔画行，墨带不粘连可测行距
			hWorld:     1,
			lineFactor: lineFactor,
		}, color.RGBA{0, 0, 0, 255})
		return img
	}
	bands := func(img *image.RGBA) [][2]int {
		var out [][2]int
		b := img.Bounds()
		cur := [2]int{-1, -1}
		for y := b.Min.Y; y < b.Max.Y; y++ {
			ink := false
			for x := b.Min.X; x < b.Max.X; x++ {
				r, _, _, _ := img.At(x, y).RGBA()
				if r>>8 < 200 {
					ink = true
					break
				}
			}
			if ink {
				if cur[0] < 0 {
					cur = [2]int{y, y}
				} else {
					cur[1] = y
				}
			} else if cur[0] >= 0 {
				out = append(out, cur)
				cur = [2]int{-1, -1}
			}
		}
		if cur[0] >= 0 {
			out = append(out, cur)
		}
		return out
	}
	gap := func(img *image.RGBA) float64 {
		bs := bands(img)
		if len(bs) < 2 {
			return -1
		}
		return float64(bs[len(bs)-1][0] - bs[len(bs)-2][0])
	}
	if g := gap(draw(0.9)); g < 0.75*em || g > 1.05*em {
		t.Fatalf("lineFactor=0.9 行距 %.1f 不在 0.9×em±15%%（%.1f）", g, 0.9*em)
	}
	if g := gap(draw(0)); g < 1.4*em || g > 1.9*em {
		t.Fatalf("lineFactor 缺省应回退 1.66×em，实际 %.1f", g)
	}
}

// TestMTextBlockBounds MTEXT 包围盒方向回归：附着 1-3（顶排）块体自锚点
// 向下、7-9（底排）向上，primitivesBounds 应覆盖块体而非仅锚点上方整字高
// （修复前单 MTEXT 样本墨迹全部落在视口外渲染空，reliability_corpus
// Text.dwg 六版本实证）。
func TestMTextBlockBounds(t *testing.T) {
	newPrim := func(att uint16) primitive {
		return primitive{kind: 1, lb: label{
			x: 100, y: 200, w: 50, h: 10,
			tx: &textInfo{lines: []string{"第一行", "第二行", "第三行", "第四行"},
				hWorld: 10, attachment: att, rectWidth: 50, lineFactor: 1},
		}}
	}
	// 顶排附着：块顶在锚点，4 行 ×1.0×10 = 30 行距 + 10 字高 = 块底 200−40=160
	b := primitivesBounds([]primitive{newPrim(1)})
	if b.minY > 200-40+1e-9 || b.maxY < 200-40-1e-9 {
		t.Fatalf("顶排附着块底应≈%v，实得 minY=%g maxY=%g", 200-40, b.minY, b.maxY)
	}
	// 底排附着：块底在锚点，块顶 200+40
	b2 := primitivesBounds([]primitive{newPrim(7)})
	if b2.maxY < 200+40-1e-9 {
		t.Fatalf("底排附着块顶应≥%v，实得 maxY=%g", 200+40, b2.maxY)
	}
	// 单行语义（attachment=0）不触发块体扩展，行为不变
	b3 := primitivesBounds([]primitive{newPrim(0)})
	if b3.maxY > 200+10+1e-9 || b3.minY < 200-1e-9 {
		t.Fatalf("attachment=0 应保持旧口径，实得 [%g,%g]", b3.minY, b3.maxY)
	}
}

// TestMTextLineFactorDecodeWiring 解码接线：JSON 构建路径把 gold 的
// linespace_factor 键带入 entMText.lineFactor（渲染行距口径的数据来源）。
func TestMTextLineFactorDecodeWiring(t *testing.T) {
	o := jsonObject{"linespace_factor": 0.9, "text": "x", "attachment": int64(1)}
	m, ok := jsonBuildMText(o).(*entMText)
	if !ok {
		t.Fatalf("jsonBuildMText 返回类型: %T", jsonBuildMText(o))
	}
	if m.lineFactor != 0.9 {
		t.Fatalf("lineFactor=%v, 期望 0.9", m.lineFactor)
	}
	// 缺键 → 0（绘制侧按缺省行距兜底）
	m2 := jsonBuildMText(jsonObject{"text": "x"}).(*entMText)
	if m2.lineFactor != 0 {
		t.Fatalf("缺键 lineFactor 应为 0，实际 %v", m2.lineFactor)
	}
}
