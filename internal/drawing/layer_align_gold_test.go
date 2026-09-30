// 本文件为 LAYER 表记录的颜色/名称值级对齐门禁：将 layerColors 解析结果
// 与 dwgread -O JSON gold 的 LAYER 对象逐个对照（颜色索引/真彩 + 名称）。
// 审计测试 TestInternalObjectAudit 不覆盖 LAYER（0x33 在 decodeObjects
// 早期消费、不进 internalObjects），本测试为其对象侧专项硬门禁；样本清单
// 与审计的 27 样本一致（gold 位于 /tmp/<alias>.json，缺失时跳过）。
package drawing

import (
	"encoding/json"
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"os"
	"path/filepath"
	"testing"
)

// layerAlignSamples 对齐样本别名清单（与 runAlignmentAudit 的 27 样本一致；
// exr13/exr14/ex2000 为批次 U 修复的 CMC 变体版本，必测）。
var layerAlignSamples = []string{
	"exr13", "exr14", "ex2000", "ex2004", "ex2007", "ex2010", "ex2013", "ex2018",
	"sample2000", "sample2018",
	"c_r14_Leader", "c_2004_Leader", "c_2007_Leader", "c_2018_Leader",
	"c_2004_Underlay", "c_2000_TS1", "c_2000_PolyLine2D",
	"c_2004_Surface", "c_2004_HatchG", "c_2007_ATMOS-DC22S",
	"c_2010_gh209_1", "c_2013_gh109_1", "c_2018_Dynblocks",
	"c_2000_entities-2d", "c_2000_entities-3d",
	"c_2000_Constraints", "c_2018_LiveSection1",
}

// TestLayerAlignGold LAYER 颜色/名称对 gold 硬门禁：
// R2000- 的 gold color 为标量索引（off 图层为负，本包不建模开关，按绝对值比）；
// R2004+ 为 {index, rgb}，index 是调色板反查派生值，真彩以 rgb 低 24 位为准。
// 名称三版本统一与 gold name 键逐字对照。
func TestLayerAlignGold(t *testing.T) {
	dir := testsupport.LibredwgTestDataDir()
	checked := 0
	for _, alias := range layerAlignSamples {
		raw, err := os.ReadFile("/tmp/" + alias + ".json")
		if err != nil {
			t.Logf("%s: gold 不可用，跳过", alias)
			continue
		}
		sample := testsupport.ResolveSamplePath(alias)
		if sample == "" {
			t.Errorf("%s: 无法从别名推导样本路径", alias)
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, sample))
		if err != nil {
			t.Logf("%s: 样本不可用，跳过", alias)
			continue
		}
		var gold struct {
			Objects []map[string]any `json:"OBJECTS"`
		}
		if err := json.Unmarshal(raw, &gold); err != nil {
			t.Fatalf("%s: gold 解析失败: %v", alias, err)
		}
		doc, err := Parse(data)
		if err != nil {
			t.Fatalf("%s: 解析失败: %v", alias, err)
		}
		n := 0
		for _, o := range gold.Objects {
			if o["object"] != "LAYER" {
				continue
			}
			hv, _ := o["handle"].([]any)
			if len(hv) == 0 {
				continue
			}
			h := uint64(hv[len(hv)-1].(float64))
			gn, _ := o["name"].(string)
			var gIdx float64
			var gLow uint32 // R2004+ 真彩低 24 位
			var gDict bool
			switch gc := o["color"].(type) {
			case float64:
				gIdx = gc
			case map[string]any:
				gDict = true
				if v, ok := gc["index"].(float64); ok {
					gIdx = v
				}
				var rgbHex string
				rgbHex, _ = gc["rgb"].(string)
				if len(rgbHex) >= 6 {
					var rgb uint32
					for _, ch := range rgbHex {
						var d uint32
						switch {
						case ch >= '0' && ch <= '9':
							d = uint32(ch - '0')
						case ch >= 'a' && ch <= 'f':
							d = uint32(ch-'a') + 10
						case ch >= 'A' && ch <= 'F':
							d = uint32(ch-'A') + 10
						default:
							continue
						}
						rgb = rgb<<4 | d
					}
					gLow = rgb & 0x00FFFFFF
				}
			default:
				continue
			}
			lc, ok := doc.LayerColors[h]
			if !ok {
				t.Errorf("[%s] LAYER h=%d 未解析（缺失）", alias, h)
				continue
			}
			n++
			if lc.Name != gn {
				t.Errorf("[%s] LAYER h=%d 名称: got %q want %q", alias, h, lc.Name, gn)
			}
			wantIdx := gIdx
			if wantIdx < 0 {
				wantIdx = -wantIdx // off 图层的 gold 负索引按绝对值比
			}
			if gDict {
				// R2004+：rgb 派生真彩与位流一致即对；index 键为调色板反查
				// 派生值（可能为 ByLayer 256 或反查失败值），不直接比
				if lc.HasTrue {
					if lc.TrueColor != gLow {
						t.Errorf("[%s] LAYER h=%d 真彩: got %#06x want %#06x", alias, h, lc.TrueColor, gLow)
					}
				} else if uint16(wantIdx) != lc.Index {
					t.Errorf("[%s] LAYER h=%d 索引: got %d want %d", alias, h, lc.Index, int(wantIdx))
				}
			} else {
				if lc.HasTrue {
					t.Errorf("[%s] LAYER h=%d 不应出现真彩（R2000- 标量索引）", alias, h)
				} else if uint16(wantIdx) != lc.Index {
					t.Errorf("[%s] LAYER h=%d 索引: got %d want %d", alias, h, lc.Index, int(wantIdx))
				}
			}
		}
		checked += n
		t.Logf("%s: 对照 LAYER %d 个", alias, n)
	}
	if checked == 0 {
		t.Skip("无可用 gold，跳过 LAYER 对齐门禁")
	}
}
