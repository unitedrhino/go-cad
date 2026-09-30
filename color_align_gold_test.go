// color_align_gold_test.go 全案例颜色还原度批量对照门禁：
// 对用户案例 DWG 与其 LibreDWG dwgread -O JSON gold 逐实体/逐图层对照
// 颜色解析（实体 color 键 + LAYER color 键 vs 本库解析），统计不符率并
// 以阈值把守。背景：LAYER CMC 的 32 位 rgb 值存在方法字节变体（0xC3
// 索引形/0xC2 真彩形），R2010+ 工程图图层几乎全为 0xC3 索引形，误当
// RGB 取色曾把消防线型图例表等 ByLayer 彩色内容整体画成深蓝近黑
// （样本 /tmp/usercase2/消防施工图_3.10.dwg 实证，该批对照即回归门禁）。
// gold 缺失时跳过（门禁依赖用户案例资产，不入库）。
package cad

import (
	"encoding/json"
	"fmt"
	"github.com/unitedrhino/go-cad/internal/entity"
	"os"
	"testing"
)

// colorAlignSamples 颜色对照样本清单：dwg 与 gold JSON 成对；
// gold 为空串时仅做解析冒烟（样本存在性依赖本地案例目录）。
var colorAlignSamples = []struct {
	name string
	dwg  string
	gold string
}{
	{"消防施工图", "/tmp/usercase2/消防施工图_3.10.dwg", "/tmp/usercase2/消防施工图_3.10.json"},
	{"室外弱电平面图", "/tmp/usercase2/室外弱电平面图3.10.dwg", "/tmp/usercase2/室外弱电平面图3.10.json"},
	{"弱电施工图", "/tmp/usercase2/弱电施工图 3.10_t3_t3.dwg", "/tmp/usercase2/弱电施工图 3.10_t3_t3.json"},
	{"case 冒烟", "/tmp/usercase/case.dwg", ""},
}

// goldColor gold 实体/图层 color 键（LibreDWG 输出：index + 8 位 hex rgb）。
type goldColor struct {
	Index *int    `json:"index"`
	RGB   *string `json:"rgb"`
}

// goldObject gold OBJECTS 条目的颜色对照所需字段。
type goldObject struct {
	Object string     `json:"object"`
	Entity string     `json:"entity"`
	Handle []any      `json:"handle"`
	Color  *goldColor `json:"color"`
}

// goldRGB 解析 gold rgb 十六进制串（长度容忍：6 位纯 RGB 或 8 位含方法字节）。
func goldRGB(s string) (uint32, bool) {
	var v uint32
	for _, ch := range s {
		var d uint32
		switch {
		case ch >= '0' && ch <= '9':
			d = uint32(ch - '0')
		case ch >= 'a' && ch <= 'f':
			d = uint32(ch-'a') + 10
		case ch >= 'A' && ch <= 'F':
			d = uint32(ch-'A') + 10
		default:
			return 0, false
		}
		v = v<<4 | d
	}
	return v, true
}

// buildEntityColorIndex 全文档实体句柄 → 颜色索引（模型空间 + 块内递归；
// gold 实体不分归属统一按句柄对照）。
func buildEntityColorIndex(doc *Document) map[uint64]*entity.EntColor {
	out := make(map[uint64]*entity.EntColor)
	var walk func(list []any)
	walk = func(list []any) {
		for _, e := range list {
			b := entity.EntityBase(e)
			if b == nil {
				continue
			}
			if _, dup := out[b.Handle]; !dup {
				c := b.Color
				out[b.Handle] = &c
			}
		}
	}
	walk(doc.modelSpace)
	walk(doc.pspaceSpace)
	for _, list := range doc.blocks {
		walk(list)
	}
	return out
}

// entityColorMatches 实体颜色对照：真彩（rgb 高字节非 0）比真彩取色，
// 显式索引（1~255）比索引值，BYLAYER/缺键跳过（图层侧另行覆盖）。
func entityColorMatches(got *entity.EntColor, g *goldColor) (bool, bool) {
	if g.RGB != nil && *g.RGB != "000000" {
		rgb, ok := goldRGB(*g.RGB)
		if !ok {
			return true, false // 不可解析跳过
		}
		if !got.HasTrue {
			return false, true
		}
		// 实体真彩保留完整 32 位，取色 uint8 截断天然剥方法字节
		return got.TrueColor&0x00FFFFFF == rgb&0x00FFFFFF, true
	}
	if g.Index != nil && *g.Index >= 1 && *g.Index <= 255 {
		return got.HasIndex && !got.HasTrue && got.Index == uint16(*g.Index), true
	}
	return true, false // BYLAYER/0/缺键：不对照
}

// layerColorMatches 图层颜色对照（渲染取色语义）：gold rgb 高字节非 0 时
// 低 24 位 ≤0xFF 为 ACI 索引形（期望本库取色等价 aciColor），>0xFF 为真彩
// 形（期望 hasTrue 且真彩一致）；rgb 为 0 时按 gold index 比索引。
func layerColorMatches(lc layerColor, g *goldColor) (bool, bool) {
	if g.RGB == nil {
		return true, false
	}
	rgb, ok := goldRGB(*g.RGB)
	if !ok {
		return true, false
	}
	if rgb>>24 == 0 {
		if g.Index != nil && *g.Index >= 1 && *g.Index <= 255 && !lc.hasTrue {
			return lc.index == uint16(*g.Index), true
		}
		return true, false
	}
	low := rgb & 0x00FFFFFF
	// 索引形与真彩形都以渲染取色为准（与 entityColor 的图层消费完全同径）
	got, gok := layerRenderColor(lc, true)
	if low <= 0xFF {
		if !gok {
			return false, true
		}
		r, g, b, _ := aciColor(uint16(low), true)
		return got.R == r && got.G == g && got.B == b, true
	}
	return lc.hasTrue && lc.trueColor&0x00FFFFFF == low, true
}

// TestColorAlignGold 全案例颜色批量对照：实体直接色与图层色两路统计，
// 图层不符必须为零（彩色图层失色是渲染级缺陷），实体不符率阈值 1%
// （长尾容差：alpha 组合/透明度变体不在取色路径）。
func TestColorAlignGold(t *testing.T) {
	checkedSamples := 0
	for _, s := range colorAlignSamples {
		data, err := os.ReadFile(s.dwg)
		if err != nil {
			t.Logf("[%s] 样本缺失，跳过: %v", s.name, err)
			continue
		}
		doc, err := Parse(data)
		if err != nil {
			t.Errorf("[%s] 解析失败: %v", s.name, err)
			continue
		}
		checkedSamples++
		if s.gold == "" {
			t.Logf("[%s] 无 gold，仅解析冒烟（实体 %d，图层 %d）", s.name, len(doc.modelSpace), len(doc.layerColors))
			continue
		}
		raw, err := os.ReadFile(s.gold)
		if err != nil {
			t.Logf("[%s] gold 缺失，跳过: %v", s.name, err)
			continue
		}
		var gold struct {
			Objects []goldObject `json:"OBJECTS"`
		}
		if err := json.Unmarshal(raw, &gold); err != nil {
			t.Fatalf("[%s] gold 解析失败: %v", s.name, err)
		}
		ents := buildEntityColorIndex(doc)
		entTotal, entBad := 0, 0
		layerTotal, layerBad := 0, 0
		var layerFails, entFails string
		for _, o := range gold.Objects {
			hv := o.Handle
			if len(hv) == 0 {
				continue
			}
			hf, _ := hv[len(hv)-1].(float64)
			h := uint64(hf)
			if o.Object == "LAYER" {
				lc, has := doc.layerColors[h]
				if !has {
					continue
				}
				if o.Color == nil {
					continue
				}
				ok, counted := layerColorMatches(lc, o.Color)
				if !counted {
					continue
				}
				layerTotal++
				if !ok {
					layerBad++
					if layerFails == "" || layerBad <= 5 {
						layerFails += fmt.Sprintf("\n  h=%d name=%v gold={idx:%v rgb:%v} got=(idx=%d tc=%#x hasTrue=%v)",
							h, doc.layerColors[h].name, *o.Color.Index, *o.Color.RGB, lc.index, lc.trueColor, lc.hasTrue)
					}
				}
				continue
			}
			if o.Entity == "" || o.Color == nil {
				continue
			}
			c, has := ents[h]
			if !has {
				continue
			}
			ok, counted := entityColorMatches(c, o.Color)
			if !counted {
				continue
			}
			entTotal++
			if !ok {
				entBad++
				if entFails == "" || entBad <= 5 {
					entFails += fmt.Sprintf("\n  h=%d %s gold={idx:%v rgb:%v} got=(idx=%d hasIdx=%v tc=%#x hasTrue=%v)",
						h, o.Entity, *o.Color.Index, *o.Color.RGB, c.Index, c.HasIndex, c.TrueColor, c.HasTrue)
				}
			}
		}
		entRate := 0.0
		if entTotal > 0 {
			entRate = float64(entBad) / float64(entTotal) * 100
		}
		layerRate := 0.0
		if layerTotal > 0 {
			layerRate = float64(layerBad) / float64(layerTotal) * 100
		}
		t.Logf("[%s] 实体色 %d/%d 不符（%.3f%%），图层色 %d/%d 不符（%.3f%%）",
			s.name, entBad, entTotal, entRate, layerBad, layerTotal, layerRate)
		if layerFails != "" {
			t.Logf("[%s] 图层不符样本:%s", s.name, layerFails)
		}
		if entFails != "" {
			t.Logf("[%s] 实体不符样本:%s", s.name, entFails)
		}
		if layerBad != 0 {
			t.Errorf("[%s] 图层颜色不符 %d/%d（彩色图层失色是渲染级缺陷）", s.name, layerBad, layerTotal)
		}
		if entRate > 1.0 {
			t.Errorf("[%s] 实体颜色不符率 %.3f%% 超阈值 1%%（%d/%d）", s.name, entRate, entBad, entTotal)
		}
	}
	if checkedSamples == 0 {
		t.Skip("无可用样本，跳过颜色对照门禁")
	}
}
