// entities_ole_test.go OLE2FRAME/OLEFRAME 测试：TS1.dwg 现场 gold 对照
// （data 块 hex 级全等）+ 合成位流单测（R2000+/R13 字段序、data_size 上限、
// 版本分支）。gold 生成方式（缺失时跳过，口径与 TestImageEntityAudit 一致）：
//
//	/tmp/libredwg-build/dwgread -O JSON -o /tmp/gold_ole2_2000.json \
//	    /tmp/libredwg/test/test-data/2000/TS1.dwg
package cad

import (
	"encoding/json"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"os"
	"testing"
)

// TestOle2FrameEntityAudit TS1.dwg 的 OLE2FRAME 值级对照：固定码 0x4A、
// mode 与 data 二进制块的精确对齐。gold 的 type 键为 LibreDWG JSON 导出口径
// （值 2，与记录头实际固定码 74 不同），不参与对照。
func TestOle2FrameEntityAudit(t *testing.T) {
	raw, err := os.ReadFile("/tmp/gold_ole2_2000.json")
	if err != nil {
		t.Skip("gold 不可用（/tmp/gold_ole2_2000.json）")
	}
	data, err := os.ReadFile("/tmp/libredwg/test/test-data/2000/TS1.dwg")
	if err != nil {
		t.Skip("样本不可用（2000/TS1.dwg）")
	}
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("解析失败 %v", err)
	}
	var gold struct {
		Objects []map[string]any `json:"OBJECTS"`
	}
	if err := json.Unmarshal(raw, &gold); err != nil {
		t.Fatalf("gold 解析失败 %v", err)
	}
	checked := 0
	for _, o := range gold.Objects {
		name, _ := o["entity"].(string)
		hv, _ := o["handle"].([]any)
		if name != "OLE2FRAME" || len(hv) == 0 {
			continue
		}
		h := uint64(hv[len(hv)-1].(float64))
		ent := doc.EntityByHandle(h)
		if ent == nil {
			t.Errorf("OLE2FRAME h=%d 实体缺失（未解出）", h)
			continue
		}
		ole, ok := ent.(*entOle2Frame)
		if !ok {
			t.Errorf("OLE2FRAME h=%d 类型不符: %T", h, ent)
			continue
		}
		if want, ok := o["data"].(string); ok {
			got := entityField(ent, "data").(string)
			if got != want {
				t.Errorf("OLE2FRAME h=%d data 块不符: got len=%d want len=%d（hex 级全等失败）",
					h, len(got), len(want))
			}
		}
		if want, ok := o["mode"].(float64); ok {
			if got := int64(ole.mode); got != int64(want) {
				t.Errorf("OLE2FRAME h=%d mode 不符: got=%d want=%d", h, got, int64(want))
			}
		}
		checked++
	}
	if checked == 0 {
		t.Skip("gold 中无 OLE2FRAME 实例")
	}
	t.Logf("OLE2FRAME 对照完成: %d 实例（data hex 全等 + mode）", checked)
}

// TestDecodeOle2FrameFromBits 合成位流：R2000+ 布局 type/mode/data_size/data/
// lock_aspect 逐字段解码验证。
func TestDecodeOle2FrameFromBits(t *testing.T) {
	w := writeEntityPrefix(testsupport.NewBitWriter(), 0x4A)
	writeCommonHead(w, 300, 2)
	w.BS(2)                    // type=2 Embedded
	w.BS(1)                    // mode=1 pspace
	w.BL(9)                    // data_size
	w.RCS([]byte("OLEDATA!!")) // data（9 字节）
	w.RC(1)                    // lock_aspect
	r := bitstream.NewBitStream(w.Bytes())
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	head, err := parseEntityHead(r, 0, featMaterialFlags|featVisualStyles|featDSBinary)
	if err != nil {
		t.Fatal(err)
	}
	ent, err := decodeOle2FrameVer(r, &head, container.VerR2013)
	if err != nil {
		t.Fatal(err)
	}
	ole := ent.(*entOle2Frame)
	if ole.oleType != 2 || ole.mode != 1 || ole.lockAspect != 1 {
		t.Fatalf("OLE2FRAME 标量字段: type=%d mode=%d lock=%d", ole.oleType, ole.mode, ole.lockAspect)
	}
	if string(ole.data) != "OLEDATA!!" {
		t.Fatalf("OLE2FRAME data: %q", ole.data)
	}
}

// TestDecodeOleFrameFromBits 合成位流：R13/R14 布局（无 mode）flag/data_size/data。
func TestDecodeOleFrameFromBits(t *testing.T) {
	w := writeEntityPrefix(testsupport.NewBitWriter(), 0x2B)
	writeCommonHead(w, 400, 2)
	w.BS(0) // flag
	w.BL(4) // data_size
	w.RCS([]byte("OLE1"))
	r := bitstream.NewBitStream(w.Bytes())
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	head, err := parseEntityHead(r, 0, featMaterialFlags|featVisualStyles|featDSBinary)
	if err != nil {
		t.Fatal(err)
	}
	ent, err := decodeOleFrameVer(r, &head, container.VerR14)
	if err != nil {
		t.Fatal(err)
	}
	ole := ent.(*entOleFrame)
	if ole.flag != 0 || ole.mode != 0 {
		t.Fatalf("OLEFRAME 标量字段: flag=%d mode=%d", ole.flag, ole.mode)
	}
	if string(ole.data) != "OLE1" {
		t.Fatalf("OLEFRAME data: %q", ole.data)
	}
}

// TestDecodeOleFrameR2000FromBits R2000+ 布局补验：flag 后有 mode 字段。
func TestDecodeOleFrameR2000FromBits(t *testing.T) {
	w := writeEntityPrefix(testsupport.NewBitWriter(), 0x2B)
	writeCommonHead(w, 401, 2)
	w.BS(1) // flag
	w.BS(0) // mode
	w.BL(2) // data_size
	w.RCS([]byte{0xDE, 0xAD})
	r := bitstream.NewBitStream(w.Bytes())
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	head, err := parseEntityHead(r, 0, featMaterialFlags|featVisualStyles|featDSBinary)
	if err != nil {
		t.Fatal(err)
	}
	ent, err := decodeOleFrameVer(r, &head, container.VerR2000)
	if err != nil {
		t.Fatal(err)
	}
	ole := ent.(*entOleFrame)
	if ole.flag != 1 || ole.mode != 0 {
		t.Fatalf("OLEFRAME R2000 标量字段: flag=%d mode=%d", ole.flag, ole.mode)
	}
	if len(ole.data) != 2 || ole.data[0] != 0xDE {
		t.Fatalf("OLEFRAME R2000 data: %v", ole.data)
	}
}
