// render_text_test.go 字形文本渲染链路单测：字体探测与回退、字形光栅缓存、
// 版式换算（镜像/退化/亚像素/锚点选择）、MTEXT 列宽换行、端到端渲染含
// 字形墨迹与无字体回退路径。依赖系统字体的用例在无字体环境整体 skip。
package cad

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"golang.org/x/image/font/basicfont"
)

// countInk 统计画布上非白（非背景）像素数（阈值 200：排除轻微抗锯齿
// 边缘的疑似噪声），作为字形墨迹存在性粗验。
func countInk(img *image.RGBA) int {
	n := 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, _, _, _ := img.At(x, y).RGBA()
			if r>>8 < 200 {
				n++
			}
		}
	}
	return n
}

// requireRenderFont 无系统字体环境跳过字体相关用例（回退路径另行覆盖）。
func requireRenderFont(t *testing.T) {
	t.Helper()
	if loadFont("") == nil {
		t.Skip("无可用系统字体（wqy/noto/dejavu 均缺失），跳过字形用例")
	}
}

// TestAdaptiveLineWidth 默认线宽随渲染宽度自适应（2048 保持历史口径）。
func TestAdaptiveLineWidth(t *testing.T) {
	cases := []struct {
		width float64
		want  float64
	}{
		{1024, 0.75},
		{2048, 0.75},
		{4096, 1.5},
		{8192, 3.0},
	}
	for _, c := range cases {
		if got := adaptiveLineWidth(c.width); got != c.want {
			t.Errorf("adaptiveLineWidth(%v)=%v, want %v", c.width, got, c.want)
		}
	}
}

// TestParseFontFileMissing 缺失/无效字体文件返回 nil 不 panic。
func TestParseFontFileMissing(t *testing.T) {
	if f := parseFontFile("/nonexistent/dir/font.ttf"); f != nil {
		t.Fatal("缺失字体应返回 nil")
	}
	if f := parseFontFile(""); f != nil {
		t.Fatal("空路径应返回 nil")
	}
}

// TestGlyphRasterizeAndCache 字形光栅化与 (rune,档位) 缓存命中一致性；
// 空格无笔画、控制字符直接跳过。
func TestGlyphRasterizeAndCache(t *testing.T) {
	requireRenderFont(t)
	tr := newTextRenderer(&canvas{})
	sf := tr.faceFor(32)
	g1 := tr.glyph('中', sf)
	if g1 == nil || g1.mask == nil || g1.w <= 0 || g1.h <= 0 {
		t.Fatal("CJK 字形应光栅化出非空掩码")
	}
	g2 := tr.glyph('中', sf)
	if g1 != g2 {
		t.Fatal("同 (rune,档位) 应命中缓存返回同一对象")
	}
	if g := tr.glyph(' ', sf); g == nil || g.mask != nil {
		t.Fatal("空格应无笔画掩码")
	}
	if g := tr.glyph('\n', sf); g != nil {
		t.Fatal("控制字符应返回 nil")
	}
	// 不同档位独立缓存
	if g := tr.glyph('中', tr.faceFor(64)); g == g1 {
		t.Fatal("不同档位不应共用缓存条目")
	}
}

// TestTextLayoutOf 版式换算：镜像标志、退化向量与亚像素判定。
func TestTextLayoutOf(t *testing.T) {
	cv := &canvas{}
	cv.setTransform(box2{minX: 0, minY: 0, maxX: 100, maxY: 100}, 1)
	// 常规 + 双向镜像标志
	tx := &textInfo{hWorld: 2, ux: 1, vx: 0, vy: 1, gen: 0x2 | 0x4, hAlign: 1, vAlign: 2}
	lb := label{x: 10, y: 20, tx: tx}
	l, ok := textLayoutOf(cv, &lb, tx)
	if !ok || !l.mirrorX || !l.mirrorY || l.hAlign != 1 || l.vAlign != 2 {
		t.Fatalf("镜像/对齐标志换算错误: %+v ok=%v", l, ok)
	}
	// 像素坐标（Y 翻转）：世界 (10,20) → (10,80)
	if l.px != 10 || l.py != 80 {
		t.Fatalf("锚点像素坐标错误: (%v,%v)", l.px, l.py)
	}
	// 零向量退化
	tx0 := &textInfo{hWorld: 2}
	if _, ok := textLayoutOf(cv, &label{tx: tx0}, tx0); ok {
		t.Fatal("零基向量应判定退化")
	}
	// 亚像素文字跳过
	tiny := &textInfo{hWorld: 0.001, ux: 1, vx: 0, vy: 1}
	if _, ok := textLayoutOf(cv, &label{tx: tiny}, tiny); ok {
		t.Fatal("亚像素文字应判定跳过")
	}
}

// TestTextLabelWithBasis textLabelWith 差分基向量：旋转 90° 下推进/字面
// 方向互换且含正确符号。
func TestTextLabelWithBasis(t *testing.T) {
	ts := newTessellator(&Document{})
	e := &entText{baseEntity: baseEntity{}, text: "AB", insertion: point3{1, 2, 0}, height: 3}
	p := ts.textLabelWith(1, 2, 3, mathPiHalf(), 2, identityXform(), e, "TEXT", textInfo{lines: []string{"AB"}})
	tx := p.lb.tx
	if tx == nil {
		t.Fatal("textLabelWith 应附字形版式信息")
	}
	if d := tx.ux - 0; d > 1e-9 || tx.uy-1 > 1e-9 {
		t.Fatalf("推进基向量应为 (0,1): (%v,%v)", tx.ux, tx.uy)
	}
	if tx.vx+1 > 1e-9 || tx.vy-0 > 1e-9 {
		t.Fatalf("字面向上基向量应为 (-1,0): (%v,%v)", tx.vx, tx.vy)
	}
	if tx.hWorld != 3 || tx.widthFactor != 1 || tx.oblique != 0 {
		t.Fatalf("版式默认值错误: %+v", tx)
	}
}

// mathPiHalf 测试用 π/2 常量（避免与 math 包名冲突的别名导入）。
func mathPiHalf() float64 { return 3.14159265358979323846 / 2 }

// TestTextLabelAnchorSelection 非默认对齐时锚点取 alignment_pt（DXF 语义）。
func TestTextLabelAnchorSelection(t *testing.T) {
	ts := newTessellator(&Document{})
	e := &entText{baseEntity: baseEntity{}, text: "X", insertion: point3{9, 9, 0},
		height: 1, hAlign: 1, vAlign: 2, alignPt: &point2{3, 4}}
	prim := ts.appendEntity(nil, e, identityXform(), 0)
	if len(prim) != 1 || prim[0].lb.x != 3 || prim[0].lb.y != 4 {
		t.Fatalf("对齐锚点应取 alignment_pt: %+v", prim[0].lb)
	}
	// 默认对齐回到 insertion
	e2 := &entText{baseEntity: baseEntity{}, text: "X", insertion: point3{9, 9, 0}, height: 1}
	prim2 := ts.appendEntity(nil, e2, identityXform(), 0)
	if prim2[0].lb.x != 9 || prim2[0].lb.y != 9 {
		t.Fatalf("默认对齐锚点应为 insertion: (%v,%v)", prim2[0].lb.x, prim2[0].lb.y)
	}
}

// TestMTextLabelInfo MTEXT 版式信息传播：\P 分行、attachment/rectWidth 透传。
func TestMTextLabelInfo(t *testing.T) {
	ts := newTessellator(&Document{})
	e := &entMText{baseEntity: baseEntity{}, text: "A\\PB", insertion: point3{0, 0, 0},
		textHeight: 2, attachment: 7, rectWidth: 10}
	prim := ts.appendEntity(nil, e, identityXform(), 0)
	tx := prim[0].lb.tx
	if tx == nil || tx.attachment != 7 || tx.rectWidth != 10 {
		t.Fatalf("MTEXT 版式信息错误: %+v", tx)
	}
	if len(tx.lines) != 2 || tx.lines[0] != "A" || tx.lines[1] != "B" {
		t.Fatalf("\\P 应分行: %q", tx.lines)
	}
}

// TestBasicFontFallbackDraw basicfont 回退字面：ASCII 可出墨迹、镜像/
// 斜切变换不 panic、缺字（CJK）安全跳过。
func TestBasicFontFallbackDraw(t *testing.T) {
	cv := &canvas{img: image.NewRGBA(image.Rect(0, 0, 300, 60)), lineWidth: 0.75}
	tr := &textRenderer{
		cv:     cv,
		basic:  true,
		faces:  map[int]sizedFace{basicBucket: newSizedFace(basicfont.Face7x13, basicBucket)},
		glyphs: map[glyphKey]*textGlyph{},
	}
	l := textLayout{px: 20, py: 40, upx: 1, vpy: -1, emPx: 13, hWorld: 1, widthFactor: 1}
	tr.drawSingleLine(l, "ABC", 0, 0, color.RGBA{0, 0, 0, 255})
	if n := countInk(cv.img); n == 0 {
		t.Fatal("basicfont 回退应绘出 ASCII 墨迹")
	}
	// 镜像 + 斜切：仍应有墨迹且不 panic
	cv.img = image.NewRGBA(image.Rect(0, 0, 300, 60))
	l2 := l
	l2.mirrorX, l2.mirrorY, l2.obliqueRad = true, true, 0.3
	tr.drawSingleLine(l2, "XY", 0, 0, color.RGBA{0, 0, 0, 255})
	if n := countInk(cv.img); n == 0 {
		t.Fatal("镜像+斜切应仍绘出墨迹")
	}
	// CJK 缺字：安全跳过（无墨迹、无 panic）
	cv.img = image.NewRGBA(image.Rect(0, 0, 300, 60))
	tr.drawSingleLine(l, "中", 0, 0, color.RGBA{0, 0, 0, 255})
}

// TestWrapLinesLogic rect_width 列宽换行：超宽断行、空格优先、空行保留、
// 无空格硬断。
func TestWrapLinesLogic(t *testing.T) {
	requireRenderFont(t)
	tr := newTextRenderer(&canvas{})
	sf := tr.faceFor(32)
	wf := 1.0
	// 空格优先断行：全部 token 保留且行数 > 1（断点行尾空格保留，比较时剔除）
	lines := tr.wrapLines([]string{"AAAA BBBB CCCC DDDD"}, 2.5, sf, wf)
	if len(lines) < 2 {
		t.Fatalf("超列宽应断行: %q", lines)
	}
	joined := ""
	for _, l := range lines {
		trimmed := strings.TrimRight(l, " ")
		joined += trimmed
		if tr.measureEm(trimmed, sf, wf) > 2.5+0.01 {
			t.Fatalf("断行后行宽超限: %q", l)
		}
	}
	if joined != "AAAABBBBCCCCDDDD" {
		t.Fatalf("换行不应丢字符: %q", joined)
	}
	// 无空格硬断
	hard := tr.wrapLines([]string{"ABCDEFGHIJKL"}, 0.6, sf, wf)
	if len(hard) < 2 {
		t.Fatalf("无空格长串应硬断: %q", hard)
	}
	// 空行保留
	kept := tr.wrapLines([]string{"", "X"}, 10, sf, wf)
	if len(kept) != 2 || kept[0] != "" || kept[1] != "X" {
		t.Fatalf("空行应保留: %q", kept)
	}
	// 非正列宽原样返回
	same := []string{"A B"}
	if got := tr.wrapLines(same, 0, sf, wf); len(got) != 1 || got[0] != "A B" {
		t.Fatal("零列宽不应换行")
	}
}

// TestDrawMTextBlocks MTEXT 多行/附着点绘制：中中附着与越界附着（按左上
// 兜底）均出墨迹且不 panic；换行生效（块高含多行）。
func TestDrawMTextBlocks(t *testing.T) {
	requireRenderFont(t)
	newCanvas := func() *canvas {
		return &canvas{img: image.NewRGBA(image.Rect(0, 0, 400, 300)), lineWidth: 0.75}
	}
	for _, att := range []uint16{5, 99, 0} {
		cv := newCanvas()
		tr := newTextRenderer(cv)
		l := textLayout{px: 60, py: 60, upx: 1, vpy: -1, emPx: 24, hWorld: 1, widthFactor: 1}
		tr.drawMText(l, &textInfo{
			lines:      []string{"中文第一行", "second line"},
			hWorld:     1,
			attachment: att,
			rectWidth:  8, // 触发第二行换行
		}, color.RGBA{0, 0, 0, 255})
		if n := countInk(cv.img); n == 0 {
			t.Fatalf("attachment=%d 应绘出墨迹", att)
		}
	}
}

// TestWarpMaskDegenerate 退化仿射（零尺度）安全返回不 panic。
func TestWarpMaskDegenerate(t *testing.T) {
	cv := &canvas{img: image.NewRGBA(image.Rect(0, 0, 50, 50)), lineWidth: 0.75}
	tr := &textRenderer{cv: cv, basic: true,
		faces:  map[int]sizedFace{basicBucket: newSizedFace(basicfont.Face7x13, basicBucket)},
		glyphs: map[glyphKey]*textGlyph{}}
	tr.warpMask(image.NewGray(image.Rect(0, 0, 8, 8)), 10, 10, 0, 0, 0, 0, color.RGBA{0, 0, 0, 255})
}

// TestRenderPNGGlyphTextE2E 端到端：单 TEXT 实体渲染出字形墨迹（非占位
// 线框需≥ 10 像素墨迹），PNG 合法且尺寸随宽度比例。
func TestRenderPNGGlyphTextE2E(t *testing.T) {
	requireRenderFont(t)
	doc := &Document{modelSpace: []any{
		&entText{baseEntity: baseEntity{}, text: "测试ABC", insertion: point3{0, 0, 0}, height: 1},
	}}
	data, err := RenderPNG(doc, RenderOptions{Width: 512})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("输出非合法 PNG: %v", err)
	}
	rgba, ok := img.(*image.RGBA)
	if !ok {
		t.Fatalf("输出图像类型: %T", img)
	}
	if n := countInk(rgba); n < 10 {
		t.Fatalf("字形墨迹不足（占位线框应≥几十像素）: %d", n)
	}
	if img.Bounds().Dx() != 512 {
		t.Fatalf("宽度口径错误: %d", img.Bounds().Dx())
	}
}

// TestRenderPNGFontPathOverride FontPath 指向缺失文件时应继续系统探测
// （本机有 wqy）仍出字形；整体不 panic。
func TestRenderPNGFontPathOverride(t *testing.T) {
	doc := &Document{modelSpace: []any{
		&entText{baseEntity: baseEntity{}, text: "OK", insertion: point3{0, 0, 0}, height: 1},
	}}
	data, err := RenderPNG(doc, RenderOptions{Width: 256, FontPath: "/nonexistent/font.ttf"})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("输出为空")
	}
}
