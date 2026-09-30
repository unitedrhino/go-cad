// api_svg_test.go 覆盖 RenderSVG 矢量输出：结构良构、坐标系 Y 翻转、
// 文本元素（转义/锚点/旋转/中文字面）、path 相对坐标合并、空文档兜底，
// 以及 testdata 全样本的渲染回归（PNG 金标路径不受影响由既有回归保障）。
package render

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"github.com/unitedrhino/go-cad/internal/drawing"
	"github.com/unitedrhino/go-cad/internal/entity"
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// svgSynthDoc 构造含基本图元的合成文档（LINE + 折线 LWPOLYLINE + TEXT）。
func svgSynthDoc() *drawing.Document {
	return &drawing.Document{
		ModelSpace: []any{
			&entity.EntLine{BaseEntity: entity.BaseEntity{}, Start: entity.Point3{X: 0, Y: 0, Z: 0}, End: entity.Point3{X: 100, Y: 50, Z: 0}},
			&entity.EntLwPolyline{BaseEntity: entity.BaseEntity{}, Vertices: []entity.Point2{{X: 10, Y: 10}, {X: 60, Y: 10}, {X: 60, Y: 40}}},
			&entity.EntText{BaseEntity: entity.BaseEntity{}, Text: "HELLO", Insertion: entity.Point3{X: 10, Y: 20, Z: 0}, Height: 2},
		},
	}
}

// svgWellFormedErr 用标准库 XML 解析器做良构校验（元素配对、属性引号完整），
// 不依赖外部 xmllint，单测内即可捕获序列化缺引号/标签错配类缺陷。
func svgWellFormedErr(svg []byte) error {
	dec := xml.NewDecoder(bytes.NewReader(svg))
	depth := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
			if depth < 0 {
				return errors.New("SVG 标签错配：多余的结束标签")
			}
		}
	}
	if depth != 0 {
		return fmt.Errorf("SVG 标签未闭合 depth=%d", depth)
	}
	return nil
}

// svgWellFormed svgWellFormedErr 的测试断言包装。
func svgWellFormed(t *testing.T, svg []byte) {
	t.Helper()
	if err := svgWellFormedErr(svg); err != nil {
		t.Fatal(err)
	}
}

// TestRenderSVGStructure 校验文档骨架：XML 声明、svg 元素、viewBox 与
// width/height 属性、背景 rect，并做标准库良构校验。
func TestRenderSVGStructure(t *testing.T) {
	svg, err := RenderSVG(svgSynthDoc(), RenderOptions{Width: 1000})
	if err != nil {
		t.Fatal(err)
	}
	s := string(svg)
	for _, want := range []string{
		`<?xml version="1.0" encoding="UTF-8"?>`,
		`<svg xmlns="http://www.w3.org/2000/svg"`,
		`viewBox="0 0 `,
		`width="1000"`,
		`<rect x="0" y="0"`,
		`fill="none"`,
		`stroke-linecap="round"`,
		`</svg>`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("SVG 缺少 %q\n%s", want, s)
		}
	}
	svgWellFormed(t, svg)
}

// TestRenderSVGYFlip 校验 CAD→SVG 坐标系翻转：世界 y=0 应映射到视口底部
// （y' = maxY - y），首段 M 坐标符合手算值。
func TestRenderSVGYFlip(t *testing.T) {
	doc := &drawing.Document{ModelSpace: []any{
		&entity.EntLine{BaseEntity: entity.BaseEntity{}, Start: entity.Point3{X: 0, Y: 0, Z: 0}, End: entity.Point3{X: 100, Y: 50, Z: 0}},
	}}
	svg, err := RenderSVG(doc, RenderOptions{Width: 1000})
	if err != nil {
		t.Fatal(err)
	}
	// 包围盒 (0,0)-(100,50)，5% 边距后 minX=-5/maxY=52.5：
	// 世界 (0,0) → 视口 (5, 52.5)；世界 (100,50) → (105, 2.5)，相对增量 l100 -50
	s := string(svg)
	if !strings.Contains(s, `M5 52.5`) {
		t.Fatalf("Y 翻转后首点应为 M5 52.5:\n%s", s)
	}
	if !strings.Contains(s, "l100-50") {
		t.Fatalf("缺相对段 l100-50:\n%s", s)
	}
	svgWellFormed(t, svg)
}

// TestRenderSVGTextElement 校验文本输出：内容、font-size（世界字高）、
// 中文字面原样（UTF-8 直出）。
func TestRenderSVGTextElement(t *testing.T) {
	doc := &drawing.Document{ModelSpace: []any{
		&entity.EntText{BaseEntity: entity.BaseEntity{}, Text: "HELLO", Insertion: entity.Point3{X: 10, Y: 20, Z: 0}, Height: 2},
		&entity.EntMText{BaseEntity: entity.BaseEntity{}, Text: "中文标注", Insertion: entity.Point3{X: 0, Y: 0, Z: 0}, TextHeight: 3},
	}}
	svg, err := RenderSVG(doc, RenderOptions{Width: 800})
	if err != nil {
		t.Fatal(err)
	}
	s := string(svg)
	if !strings.Contains(s, ">HELLO</text>") {
		t.Fatalf("缺 HELLO 文本:\n%s", s)
	}
	if !strings.Contains(s, `font-size="2"`) {
		t.Fatalf("缺字高 font-size=2:\n%s", s)
	}
	if !strings.Contains(s, ">中文标注</text>") {
		t.Fatalf("中文应原样 UTF-8 输出:\n%s", s)
	}
	svgWellFormed(t, svg)
}

// TestRenderSVGMTextStrip 校验 MTEXT 格式码剥离后输出（\P 换行、颜色码等）。
func TestRenderSVGMTextStrip(t *testing.T) {
	doc := &drawing.Document{ModelSpace: []any{
		&entity.EntMText{BaseEntity: entity.BaseEntity{}, Text: `\A1;6X5.0X2.5`, Insertion: entity.Point3{X: 0, Y: 0, Z: 0}, TextHeight: 2},
	}}
	svg, err := RenderSVG(doc, RenderOptions{Width: 800})
	if err != nil {
		t.Fatal(err)
	}
	s := string(svg)
	if !strings.Contains(s, ">6X5.0X2.5</text>") {
		t.Fatalf("MTEXT 格式码应剥离:\n%s", s)
	}
	svgWellFormed(t, svg)
}

// TestRenderSVGMTextMultiline 校验 MTEXT 多行文本（\P）拆为 tspan 行：
// 首行随基线，后续行 dy 下移一行动距，无裸换行残留在文本内容中。
func TestRenderSVGMTextMultiline(t *testing.T) {
	doc := &drawing.Document{ModelSpace: []any{
		&entity.EntMText{BaseEntity: entity.BaseEntity{}, Text: `第一行\P第二行`, Insertion: entity.Point3{X: 0, Y: 0, Z: 0}, TextHeight: 2},
	}}
	svg, err := RenderSVG(doc, RenderOptions{Width: 800})
	if err != nil {
		t.Fatal(err)
	}
	s := string(svg)
	if !strings.Contains(s, ">第一行<tspan") || !strings.Contains(s, ">第二行</tspan></text>") {
		t.Fatalf("多行 MTEXT 应拆为 tspan:\n%s", s)
	}
	if !strings.Contains(s, `dy="2.4"`) { // 行距 = 1.2 × 字高 2
		t.Fatalf("tspan 行距应为 1.2×字高:\n%s", s)
	}
	svgWellFormed(t, svg)
}

// TestRenderSVGEscape 校验 XML 特殊字符转义与非法控制字符剔除。
func TestRenderSVGEscape(t *testing.T) {
	doc := &drawing.Document{ModelSpace: []any{
		&entity.EntText{BaseEntity: entity.BaseEntity{}, Text: "A<B&C>D\x01E", Insertion: entity.Point3{X: 0, Y: 0, Z: 0}, Height: 2},
	}}
	svg, err := RenderSVG(doc, RenderOptions{Width: 800})
	if err != nil {
		t.Fatal(err)
	}
	s := string(svg)
	if !strings.Contains(s, "A&lt;B&amp;C&gt;D") {
		t.Fatalf("XML 转义不符:\n%s", s)
	}
	if strings.Contains(s, "\x01") {
		t.Fatalf("XML 1.0 非法控制字符应剔除:\n%s", s)
	}
	svgWellFormed(t, svg)
}

// TestRenderSVGTextAnchor 校验对齐码 → text-anchor 映射：
// TEXT hAlign 1/4→middle、2→end、0→缺省；MTEXT attachment 右列→end。
func TestRenderSVGTextAnchor(t *testing.T) {
	doc := &drawing.Document{ModelSpace: []any{
		&entity.EntText{BaseEntity: entity.BaseEntity{}, Text: "C", Insertion: entity.Point3{X: 0, Y: 0, Z: 0}, Height: 2, HAlign: 1},
		&entity.EntText{BaseEntity: entity.BaseEntity{}, Text: "R", Insertion: entity.Point3{X: 0, Y: 0, Z: 0}, Height: 2, HAlign: 2},
		&entity.EntText{BaseEntity: entity.BaseEntity{}, Text: "L", Insertion: entity.Point3{X: 0, Y: 0, Z: 0}, Height: 2},
		&entity.EntMText{BaseEntity: entity.BaseEntity{}, Text: "TR", Insertion: entity.Point3{X: 0, Y: 0, Z: 0}, TextHeight: 2, Attachment: 3},
	}}
	svg, err := RenderSVG(doc, RenderOptions{Width: 800})
	if err != nil {
		t.Fatal(err)
	}
	s := string(svg)
	if !strings.Contains(s, `>C</text>`) || !strings.Contains(s, `text-anchor="middle"`) {
		t.Fatalf("hAlign=1 应映射 middle:\n%s", s)
	}
	if !strings.Contains(s, `text-anchor="end"`) {
		t.Fatalf("hAlign=2 与 attachment=3 应映射 end:\n%s", s)
	}
	if strings.Count(s, `text-anchor="middle"`) != 1 || strings.Count(s, `text-anchor="end"`) != 2 {
		t.Fatalf("anchor 出现次数不符:\n%s", s)
	}
	svgWellFormed(t, svg)
}

// TestRenderSVGTextRotate 校验旋转文本：Y 翻转后角度取负，绕基线起点旋转。
func TestRenderSVGTextRotate(t *testing.T) {
	doc := &drawing.Document{ModelSpace: []any{
		&entity.EntText{BaseEntity: entity.BaseEntity{}, Text: "R30", Insertion: entity.Point3{X: 0, Y: 0, Z: 0}, Height: 2, Rotation: math.Pi / 6},
	}}
	svg, err := RenderSVG(doc, RenderOptions{Width: 800})
	if err != nil {
		t.Fatal(err)
	}
	s := string(svg)
	if !strings.Contains(s, `transform="rotate(-30 `) {
		t.Fatalf("30° 逆时针应输出 rotate(-30 …):\n%s", s)
	}
	svgWellFormed(t, svg)
}

// TestRenderSVGColorGroup 校验同图层同色图元共享一个 <g> 组，属性不逐
// path 重复。（分组键为「颜色×图层」后，组开标签变为 <g id=… stroke=…>，
// 断言从 `<g stroke=` 前缀匹配改为对 stroke 属性计数。）
func TestRenderSVGColorGroup(t *testing.T) {
	doc := &drawing.Document{ModelSpace: []any{
		&entity.EntLine{BaseEntity: entity.BaseEntity{}, Start: entity.Point3{X: 0, Y: 0, Z: 0}, End: entity.Point3{X: 10, Y: 0, Z: 0}},
		&entity.EntLine{BaseEntity: entity.BaseEntity{}, Start: entity.Point3{X: 0, Y: 5, Z: 0}, End: entity.Point3{X: 10, Y: 5, Z: 0}},
	}}
	svg, err := RenderSVG(doc, RenderOptions{Width: 800})
	if err != nil {
		t.Fatal(err)
	}
	s := string(svg)
	if n := strings.Count(s, `stroke="#`); n != 1 {
		t.Fatalf("同色同图层线段应合并为一组, got %d:\n%s", n, s)
	}
	if n := strings.Count(s, `<path d="`); n != 1 {
		t.Fatalf("同组线段应合并为单一 path 元素, got %d:\n%s", n, s)
	}
	svgWellFormed(t, svg)
}

// TestRenderSVGPathMerge 校验 LWPOLYLINE 连续顶点合并为相对段（无逐段 M）。
func TestRenderSVGPathMerge(t *testing.T) {
	doc := &drawing.Document{ModelSpace: []any{
		&entity.EntLwPolyline{BaseEntity: entity.BaseEntity{}, Vertices: []entity.Point2{{X: 0, Y: 0}, {X: 10, Y: 0}, {X: 10, Y: 10}, {X: 0, Y: 10}}},
	}}
	svg, err := RenderSVG(doc, RenderOptions{Width: 800})
	if err != nil {
		t.Fatal(err)
	}
	s := string(svg)
	if strings.Count(s, "M") != 1 {
		t.Fatalf("连续折线应单 M 起:\n%s", s)
	}
	for _, want := range []string{"h 10", "v-10", "h-10"} {
		if !strings.Contains(s, want) {
			t.Fatalf("缺水平/垂直相对段 %q:\n%s", want, s)
		}
	}
	svgWellFormed(t, svg)
}

// TestRenderSVGEmptyDoc 空文档应仍输出合法 SVG 骨架（不 panic、无 path）。
func TestRenderSVGEmptyDoc(t *testing.T) {
	svg, err := RenderSVG(&drawing.Document{}, RenderOptions{Width: 800})
	if err != nil {
		t.Fatal(err)
	}
	s := string(svg)
	if !strings.HasPrefix(s, `<?xml version="1.0"`) || !strings.HasSuffix(s, "</svg>\n") {
		t.Fatalf("空文档 SVG 骨架不完整:\n%s", s)
	}
	if strings.Contains(s, `<path`) {
		t.Fatalf("空文档不应有 path:\n%s", s)
	}
	svgWellFormed(t, svg)
}

// TestRenderSVGNilDoc nil 文档应报错（与 RenderPNG 口径一致）。
func TestRenderSVGNilDoc(t *testing.T) {
	if _, err := RenderSVG(nil, RenderOptions{Width: 800}); err == nil {
		t.Fatal("nil 文档应返回错误")
	}
}

// TestRenderSVGPNGUnaffected label 携带 text/anchor 字段后，PNG 占位条
// 渲染路径应不受影响（金标路径回归哨兵）。
func TestRenderSVGPNGUnaffected(t *testing.T) {
	png, err := RenderPNG(svgSynthDoc(), RenderOptions{Width: 400})
	if err != nil {
		t.Fatal(err)
	}
	if len(png) < 100 {
		t.Fatalf("PNG 过小 %d", len(png))
	}
}

// nextNum 从 d 的 k 位置读取下一个数字并推进 k（测试解析器辅助）。
func nextNum(d string, k *int) float64 {
	for *k < len(d) && (d[*k] == ' ' || d[*k] == ',') {
		*k++
	}
	n := 0
	for *k+n < len(d) {
		c := d[*k+n]
		if c == '.' || (c >= '0' && c <= '9') {
			n++
			continue
		}
		if c == '-' && n > 0 {
			break
		}
		if c == '-' && n == 0 {
			n++
			continue
		}
		break
	}
	v, _ := strconv.ParseFloat(d[*k:*k+n], 64)
	*k += n
	return v
}

// TestRenderSVGPathRoundTrip 数值级回归：把输出的 path d 坐标解析回来，
// 与源图元坐标逐段对照（连写分隔 bug——"4.15 109.1" 缺分隔连成
// "4.15109.1" 被解析为 4.15109+0.1——只能靠数值对照发现）。
func TestRenderSVGPathRoundTrip(t *testing.T) {
	doc := &drawing.Document{ModelSpace: []any{
		// 折线含正/负/小数增量与斜向段，覆盖 l/h/v 与省字母续写
		&entity.EntLwPolyline{BaseEntity: entity.BaseEntity{}, Vertices: []entity.Point2{{X: 0, Y: 0}, {X: 109.123, Y: 4.15}, {X: 218.223, Y: 5.14}, {X: 218.223, Y: 105.14}, {X: 118.223, Y: 105.14}}},
	}}
	svg, err := RenderSVG(doc, RenderOptions{Width: 800})
	if err != nil {
		t.Fatal(err)
	}
	s := string(svg)
	m := regexp.MustCompile(`<path d="([^"]*)"`).FindStringSubmatch(s)
	if m == nil {
		t.Fatal("无 path")
	}
	d := m[1]
	// 简易 d 解析：数字流（含命令字母与 . - 空格）
	want := []struct{ X, y float64 }{
		{0, 0}, {109.123, 4.15}, {218.223, 5.14}, {218.223, 105.14}, {118.223, 105.14},
	}
	// 顺序扫描 d：命令字母切换状态，数字按当前命令消耗（M 绝对两点、
	// l 相对两点、h/v 相对单坐标）
	x, y := 0.0, 0.0
	var pts [][2]float64
	cmd := byte(0)
	for k := 0; k < len(d); {
		c := d[k]
		switch c {
		case 'M', 'l', 'h', 'v':
			cmd = c
			k++
			continue
		case ' ', ',':
			k++
			continue
		}
		n := 0
		for k+n < len(d) {
			ch := d[k+n]
			if ch == '.' || (ch >= '0' && ch <= '9') {
				n++
				continue
			}
			if ch == '-' && n > 0 {
				break
			}
			if ch == '-' && n == 0 {
				n++
				continue
			}
			break
		}
		v, err := strconv.ParseFloat(d[k:k+n], 64)
		if err != nil {
			t.Fatalf("解析 d 中的数字 %q: %v", d[k:k+n], err)
		}
		k += n
		switch cmd {
		case 'M':
			x, y = v, nextNum(d, &k)
			pts = append(pts, [2]float64{x, y})
		case 'l':
			x += v
			y += nextNum(d, &k)
			pts = append(pts, [2]float64{x, y})
		case 'h':
			x += v
			pts = append(pts, [2]float64{x, y})
		case 'v':
			y += v
			pts = append(pts, [2]float64{x, y})
		}
	}
	if len(pts) != len(want) {
		t.Fatalf("复原点数 %d ≠ %d, d=%q", len(pts), len(want), d)
	}
	// 视口起点由 robustBounds 统计包围盒决定（非几何极值），无法手算；
	// 验证增量序列（形状不变性：Y 翻转使 dy 取反）。自适应位数最粗 1 位
	// 小数，容差取 0.06 单位。
	for i := 1; i < len(pts); i++ {
		dx, dy := pts[i][0]-pts[i-1][0], pts[i][1]-pts[i-1][1]
		wx, wy := want[i].X-want[i-1].X, -(want[i].y - want[i-1].y)
		if math.Abs(dx-wx) > 0.06 || math.Abs(dy-wy) > 0.06 {
			t.Fatalf("增量 %d: (%g,%g) ≠ (%g,%g), d=%q", i, dx, dy, wx, wy, d)
		}
	}
}

// TestRenderSVGAllSamples testdata 全版本样本（R14~R2018）RenderSVG 回归：
// 均需可解析并输出含文本组骨架的 SVG。
func TestRenderSVGAllSamples(t *testing.T) {
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
		svg, err := RenderSVG(doc, RenderOptions{Width: 800})
		if err != nil {
			fail++
			t.Errorf("%s: render: %v", f, err)
			continue
		}
		s := string(svg)
		if !strings.HasPrefix(s, `<?xml`) || !strings.HasSuffix(s, "</svg>\n") {
			fail++
			t.Errorf("%s: SVG 骨架不完整", f)
			continue
		}
		if err := svgWellFormedErr(svg); err != nil {
			fail++
			t.Errorf("%s: %v", f, err)
			continue
		}
		ok++
	}
	t.Logf("svg ok=%d fail=%d", ok, fail)
	if fail > 0 {
		t.Fatalf("SVG 渲染回归失败 %d 个样本", fail)
	}
}

// svgMetaJSON 提取 <metadata> 内图纸摘要 JSON 并解析为 map（测试辅助）。
func svgMetaJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	m := regexp.MustCompile(`<metadata>(.*)</metadata>`).FindStringSubmatch(s)
	if m == nil {
		t.Fatalf("缺 <metadata> 图纸摘要:\n%s", s)
	}
	var mm map[string]any
	if err := json.Unmarshal([]byte(m[1]), &mm); err != nil {
		t.Fatalf("metadata 非 JSON: %v\n%s", err, m[1])
	}
	return mm
}

// TestRenderSVGMetadata 校验根摘要 <metadata>：位于首子元素（背景 rect 前），
// JSON 含生成器/版本/图元数/文本数。
func TestRenderSVGMetadata(t *testing.T) {
	svg, err := RenderSVG(svgSynthDoc(), RenderOptions{Width: 1000})
	if err != nil {
		t.Fatal(err)
	}
	s := string(svg)
	mm := svgMetaJSON(t, s)
	if mm["generator"] != "go-cad" {
		t.Fatalf("generator 应为 go-cad: %v", mm["generator"])
	}
	if v, _ := mm["version"].(string); v == "" {
		t.Fatalf("缺 version: %v", mm)
	}
	// svgSynthDoc = LINE + LWPOLYLINE + TEXT → 3 图元、1 文本
	if mm["entities"] != float64(3) || mm["texts"] != float64(1) {
		t.Fatalf("entities/texts 应为 3/1: %v", mm)
	}
	// 首子元素：metadata 结束位置在背景 rect 之前
	if strings.Index(s, "</metadata>") > strings.Index(s, "<rect") {
		t.Fatalf("metadata 应在背景 rect 之前:\n%s", s)
	}
	svgWellFormed(t, svg)
}

// TestRenderSVGLayerGroups 校验图层分组：组 id 为 layer:图层名，同图层
// 同色合并为一组，不同图层（同色）与不同颜色各自成组。
func TestRenderSVGLayerGroups(t *testing.T) {
	doc := &drawing.Document{
		ModelSpace: []any{
			&entity.EntLine{BaseEntity: entity.BaseEntity{Layer: 0x10}, Start: entity.Point3{X: 0, Y: 0, Z: 0}, End: entity.Point3{X: 10, Y: 0, Z: 0}},
			&entity.EntLine{BaseEntity: entity.BaseEntity{Layer: 0x10}, Start: entity.Point3{X: 0, Y: 5, Z: 0}, End: entity.Point3{X: 10, Y: 5, Z: 0}},
			&entity.EntLine{BaseEntity: entity.BaseEntity{Layer: 0x20}, Start: entity.Point3{X: 0, Y: -5, Z: 0}, End: entity.Point3{X: 10, Y: -5, Z: 0}},
		},
		LayerColors: map[uint64]drawing.LayerColor{
			0x10: {Name: "WALL"},
			0x20: {Name: "AXIS", Index: 1}, // ACI 1 红，与 WALL 默认黑区分
		},
	}
	svg, err := RenderSVG(doc, RenderOptions{Width: 800})
	if err != nil {
		t.Fatal(err)
	}
	s := string(svg)
	if strings.Count(s, `<g id="layer:WALL" `) != 1 {
		t.Fatalf("WALL 两线段应合并为单一图层组:\n%s", s)
	}
	if !strings.Contains(s, `<g id="layer:AXIS" stroke="#ff0000"`) {
		t.Fatalf("AXIS 组应携带 ACI 1 红色:\n%s", s)
	}
	if n := strings.Count(s, `stroke="#`); n != 2 {
		t.Fatalf("两组（WALL 黑 + AXIS 红）之外不应有其余组, got %d:\n%s", n, s)
	}
	svgWellFormed(t, svg)
}

// TestRenderSVGLayerHandleFallback 无名图层（layerColors 无记录）分组 id
// 回退为 layer:handle-<hex>。
func TestRenderSVGLayerHandleFallback(t *testing.T) {
	doc := &drawing.Document{ModelSpace: []any{
		&entity.EntLine{BaseEntity: entity.BaseEntity{Layer: 0x1f}, Start: entity.Point3{X: 0, Y: 0, Z: 0}, End: entity.Point3{X: 10, Y: 0, Z: 0}},
	}}
	svg, err := RenderSVG(doc, RenderOptions{Width: 800})
	if err != nil {
		t.Fatal(err)
	}
	s := string(svg)
	if !strings.Contains(s, `<g id="layer:handle-1F" stroke="`) {
		t.Fatalf("无名图层应回退 handle-<hex> id:\n%s", s)
	}
	svgWellFormed(t, svg)
}

// TestRenderSVGGroupCapDegraded 图层分组数触顶（200）后退化为纯颜色分组：
// 输出说明注释、无新增图层 id 组、全部图元仍有组覆盖。
func TestRenderSVGGroupCapDegraded(t *testing.T) {
	const nLayers = svgMaxGroups + 1
	lc := make(map[uint64]drawing.LayerColor, nLayers)
	ents := make([]any, 0, nLayers)
	for i := 0; i < nLayers; i++ {
		h := uint64(0x100 + i)
		lc[h] = drawing.LayerColor{Name: fmt.Sprintf("L%03d", i)}
		ents = append(ents, &entity.EntLine{
			BaseEntity: entity.BaseEntity{Layer: h},
			Start:      entity.Point3{X: float64(i), Y: 0, Z: 0}, End: entity.Point3{X: float64(i) + 5, Y: 0, Z: 0},
		})
	}
	doc := &drawing.Document{ModelSpace: ents, LayerColors: lc}
	svg, err := RenderSVG(doc, RenderOptions{Width: 800})
	if err != nil {
		t.Fatal(err)
	}
	s := string(svg)
	if !strings.Contains(s, "<!-- 线段分组数超过上限 200") {
		t.Fatalf("触顶应输出退化说明注释:\n%s", s[:min(len(s), 800)])
	}
	if strings.Contains(s, `id="layer:L200"`) {
		t.Fatalf("触顶后不应再建图层组:\n%s", s[:min(len(s), 800)])
	}
	if n := strings.Count(s, `<path d="`); n != nLayers {
		t.Fatalf("全部图元都应入组, path=%d want=%d", n, nLayers)
	}
	svgWellFormed(t, svg)
}

// TestRenderSVGTextMeta 校验文本 AI 元数据：<text> 携带 data-type（TEXT/
// MTEXT/ATTRIB）与 data-h（源实体句柄十六进制）。
func TestRenderSVGTextMeta(t *testing.T) {
	doc := &drawing.Document{ModelSpace: []any{
		&entity.EntText{BaseEntity: entity.BaseEntity{Handle: 0x2a}, Text: "T1", Insertion: entity.Point3{X: 0, Y: 0, Z: 0}, Height: 2},
		&entity.EntMText{BaseEntity: entity.BaseEntity{Handle: 0x2b}, Text: "M1", Insertion: entity.Point3{X: 20, Y: 0, Z: 0}, TextHeight: 2},
		&entity.EntAttrib{BaseEntity: entity.BaseEntity{Handle: 0x2c}, Text: "A1", Insertion: entity.Point3{X: 40, Y: 0, Z: 0}, Height: 2},
	}}
	svg, err := RenderSVG(doc, RenderOptions{Width: 800})
	if err != nil {
		t.Fatal(err)
	}
	s := string(svg)
	for _, want := range []string{
		`data-type="TEXT" data-h="2A"`,
		`data-type="MTEXT" data-h="2B"`,
		`data-type="ATTRIB" data-h="2C"`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("缺文本元数据 %q:\n%s", want, s)
		}
	}
	svgWellFormed(t, svg)
}

// TestRenderSheetSVGMetadata 拆图模式 metadata：图框名（sheet 字段）进入
// 图纸摘要，图层清单与版本随文档填充。
func TestRenderSheetSVGMetadata(t *testing.T) {
	doc := buildSheetDoc(t, []struct {
		h        uint64
		name     string
		title    string
		offset   [2]float64
		W, hgt   float64
		scale    float64
		rotation float64
	}{aSeriesSheet(100, "A1图框", "配电系统图", [2]float64{0, 0})})
	sheets := DetectSheets(doc)
	data, err := RenderSheetSVG(doc, sheets[0], RenderOptions{})
	if err != nil {
		t.Fatalf("RenderSheetSVG: %v", err)
	}
	mm := svgMetaJSON(t, string(data))
	if mm["sheet"] != "配电系统图" {
		t.Fatalf("metadata.sheet 应为图框名: %v", mm["sheet"])
	}
	if v, _ := mm["version"].(string); v == "" {
		t.Fatalf("metadata 缺 version: %v", mm)
	}
	svgWellFormed(t, data)
}
