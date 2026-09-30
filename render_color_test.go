// render_color_test.go 实体/图层颜色的渲染取色测试：
// LAYER CMC 32 位 rgb 值的方法字节变体（索引形 0xC3/0xC1 vs 真彩形
// 0xC2/0xC0）消费侧分类的正确性门禁——消防施工图等工程图大量 ByLayer
// 彩色图层（红/黄/绿/青/蓝）以 0xC300000N 形式存储，误当 RGB 取色会把
// 整张图例表画成 RGB(0,0,N) 深蓝近黑（样本 /tmp/usercase2 实证）。
package cad

import (
	"github.com/unitedrhino/go-cad/internal/drawing"
	"github.com/unitedrhino/go-cad/internal/entity"
	"image/color"
	"testing"
)

// TestEntityColorLayerIndexForm 索引形图层色（低 24 位 ≤0xFF）按 ACI 渲染。
func TestEntityColorLayerIndexForm(t *testing.T) {
	doc := &Document{LayerColors: map[uint64]drawing.LayerColor{
		1: {Index: 0, HasTrue: true, TrueColor: 0x000001}, // 0xC3000001 红
		2: {Index: 0, HasTrue: true, TrueColor: 0x000002}, // 黄
		3: {Index: 0, HasTrue: true, TrueColor: 0x000003}, // 绿
		4: {Index: 0, HasTrue: true, TrueColor: 0x000004}, // 青
		5: {Index: 0, HasTrue: true, TrueColor: 0x000005}, // 蓝
		7: {Index: 0, HasTrue: true, TrueColor: 0x000007}, // 黑/白
	}}
	cases := []struct {
		layer uint64
		want  color.RGBA
	}{
		{1, color.RGBA{255, 0, 0, 255}},
		{2, color.RGBA{255, 255, 0, 255}},
		{3, color.RGBA{0, 255, 0, 255}},
		{4, color.RGBA{0, 255, 255, 255}},
		{5, color.RGBA{0, 0, 255, 255}},
		{7, color.RGBA{0, 0, 0, 255}}, // ACI 7 白底取黑
	}
	for _, c := range cases {
		p := &drawing.Primitive{Kind: 0, Layer: c.layer}
		if got := entityColor(doc, p, true); got != c.want {
			t.Errorf("图层 %d 索引形取色: got %v want %v", c.layer, got, c.want)
		}
	}
	// ACI 7 黑底取白
	p := &drawing.Primitive{Kind: 0, Layer: 7}
	if got := entityColor(doc, p, false); got != (color.RGBA{255, 255, 255, 255}) {
		t.Errorf("ACI 7 黑底取色: got %v", got)
	}
}

// TestEntityColorLayerTrueColorForm 真彩形图层色（低 24 位 >0xFF）按 RGB 渲染。
func TestEntityColorLayerTrueColorForm(t *testing.T) {
	doc := &Document{LayerColors: map[uint64]drawing.LayerColor{
		10: {Index: 7, HasTrue: true, TrueColor: 0xFFFFFF}, // 0xC2FFFFFF 白（yichutu 图层实证）
		11: {Index: 3, HasTrue: true, TrueColor: 0x00FF00}, // 0xC200FF00 绿（EQUIP-弱电设备 实证）
	}}
	p := &drawing.Primitive{Kind: 0, Layer: 10}
	if got := entityColor(doc, p, true); got != (color.RGBA{255, 255, 255, 255}) {
		t.Errorf("真彩白取色: got %v", got)
	}
	p = &drawing.Primitive{Kind: 0, Layer: 11}
	if got := entityColor(doc, p, true); got != (color.RGBA{0, 255, 0, 255}) {
		t.Errorf("真彩绿取色: got %v", got)
	}
}

// TestEntityColorLayerPlainIndex 纯索引图层色（preR2004 无 rgb 段）不受影响。
func TestEntityColorLayerPlainIndex(t *testing.T) {
	doc := &Document{LayerColors: map[uint64]drawing.LayerColor{
		3: {Index: 3},
	}}
	p := &drawing.Primitive{Kind: 0, Layer: 3}
	if got := entityColor(doc, p, true); got != (color.RGBA{0, 255, 0, 255}) {
		t.Errorf("纯索引绿取色: got %v", got)
	}
}

// TestEntityColorEntityTrueColor 实体真彩 32 位（0xC2 高字节）取色不受高字节干扰。
func TestEntityColorEntityTrueColor(t *testing.T) {
	doc := &Document{LayerColors: map[uint64]drawing.LayerColor{}}
	p := &drawing.Primitive{Kind: 0, Layer: 1, Color: entity.EntColor{HasTrue: true, TrueColor: 0xC2FFFFFF}}
	if got := entityColor(doc, p, true); got != (color.RGBA{255, 255, 255, 255}) {
		t.Errorf("实体真彩白取色: got %v", got)
	}
}
