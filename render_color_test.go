// render_color_test.go 实体/图层颜色的渲染取色测试：
// LAYER CMC 32 位 rgb 值的方法字节变体（索引形 0xC3/0xC1 vs 真彩形
// 0xC2/0xC0）消费侧分类的正确性门禁——消防施工图等工程图大量 ByLayer
// 彩色图层（红/黄/绿/青/蓝）以 0xC300000N 形式存储，误当 RGB 取色会把
// 整张图例表画成 RGB(0,0,N) 深蓝近黑（样本 /tmp/usercase2 实证）。
package cad

import (
	"github.com/unitedrhino/go-cad/internal/entity"
	"image/color"
	"testing"
)

// TestEntityColorLayerIndexForm 索引形图层色（低 24 位 ≤0xFF）按 ACI 渲染。
func TestEntityColorLayerIndexForm(t *testing.T) {
	doc := &Document{layerColors: map[uint64]layerColor{
		1: {index: 0, hasTrue: true, trueColor: 0x000001}, // 0xC3000001 红
		2: {index: 0, hasTrue: true, trueColor: 0x000002}, // 黄
		3: {index: 0, hasTrue: true, trueColor: 0x000003}, // 绿
		4: {index: 0, hasTrue: true, trueColor: 0x000004}, // 青
		5: {index: 0, hasTrue: true, trueColor: 0x000005}, // 蓝
		7: {index: 0, hasTrue: true, trueColor: 0x000007}, // 黑/白
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
		p := &primitive{kind: 0, layer: c.layer}
		if got := entityColor(doc, p, true); got != c.want {
			t.Errorf("图层 %d 索引形取色: got %v want %v", c.layer, got, c.want)
		}
	}
	// ACI 7 黑底取白
	p := &primitive{kind: 0, layer: 7}
	if got := entityColor(doc, p, false); got != (color.RGBA{255, 255, 255, 255}) {
		t.Errorf("ACI 7 黑底取色: got %v", got)
	}
}

// TestEntityColorLayerTrueColorForm 真彩形图层色（低 24 位 >0xFF）按 RGB 渲染。
func TestEntityColorLayerTrueColorForm(t *testing.T) {
	doc := &Document{layerColors: map[uint64]layerColor{
		10: {index: 7, hasTrue: true, trueColor: 0xFFFFFF}, // 0xC2FFFFFF 白（yichutu 图层实证）
		11: {index: 3, hasTrue: true, trueColor: 0x00FF00}, // 0xC200FF00 绿（EQUIP-弱电设备 实证）
	}}
	p := &primitive{kind: 0, layer: 10}
	if got := entityColor(doc, p, true); got != (color.RGBA{255, 255, 255, 255}) {
		t.Errorf("真彩白取色: got %v", got)
	}
	p = &primitive{kind: 0, layer: 11}
	if got := entityColor(doc, p, true); got != (color.RGBA{0, 255, 0, 255}) {
		t.Errorf("真彩绿取色: got %v", got)
	}
}

// TestEntityColorLayerPlainIndex 纯索引图层色（preR2004 无 rgb 段）不受影响。
func TestEntityColorLayerPlainIndex(t *testing.T) {
	doc := &Document{layerColors: map[uint64]layerColor{
		3: {index: 3},
	}}
	p := &primitive{kind: 0, layer: 3}
	if got := entityColor(doc, p, true); got != (color.RGBA{0, 255, 0, 255}) {
		t.Errorf("纯索引绿取色: got %v", got)
	}
}

// TestEntityColorEntityTrueColor 实体真彩 32 位（0xC2 高字节）取色不受高字节干扰。
func TestEntityColorEntityTrueColor(t *testing.T) {
	doc := &Document{layerColors: map[uint64]layerColor{}}
	p := &primitive{kind: 0, layer: 1, color: entity.EntColor{HasTrue: true, TrueColor: 0xC2FFFFFF}}
	if got := entityColor(doc, p, true); got != (color.RGBA{255, 255, 255, 255}) {
		t.Errorf("实体真彩白取色: got %v", got)
	}
}
