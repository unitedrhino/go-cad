// objects_longtail_test 语料 0 实例对象（IDBUFFER/INDEX/LAYER_INDEX/
// PROXY_OBJECT）的合成位流门禁测试：按 dwg.spec 字段布局手工构造已知
// 内容的位流，经 decodeInternalObject 框架解码后逐字段断言（含 handle
// 流句柄）。语料中无真实实例，此处以「构造合法序列 → 解码 → 字段一致」
// 合成自证；真实样本出现后需补 gold 对照。
package cad

import (
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/objrec"
	"strings"
	"testing"
)

// writeR2000ObjectPrefix 构造 R2000 对象记录前缀：RL bitsize 占位 +
// H handle + BS EED 终止 + BL num_reactors（R2000 无 xdic/ds 位）。
// 返回回填 bitsize 的写入口。
func writeR2000ObjectPrefix(w *bitstream.EncWriter, handle uint64) {
	w.WriteRL(0) // bitsize 占位
	w.WriteH(0, 1, handle)
	w.WriteBS(0) // EED 终止
	w.WriteBL(0) // num_reactors = 0
}

// fillR2000Bitsize 回填 body 开头的 RL bitsize（datEnd：专有字段结束位）。
func fillR2000Bitsize(body []byte, datEnd uint64) {
	body[0] = uint8(datEnd)
	body[1] = uint8(datEnd >> 8)
	body[2] = uint8(datEnd >> 16)
	body[3] = uint8(datEnd >> 24)
}

// padToByte 位流补齐到字节边界（合成 handle 流后的尾部对齐）。
func padToByte(w *bitstream.EncWriter) {
	for w.Bit != 0 {
		w.WriteBitsMsb(0, 1)
	}
}

// TestIDBufferSynthetic 合成 R2000 IDBUFFER：unknown RC=7 +
// num_obj_ids=2 + handle 流 owner/xdic/obj_ids×2。
func TestIDBufferSynthetic(t *testing.T) {
	w := bitstream.NewEncWriter()
	writeR2000ObjectPrefix(w, 0x64)
	w.WriteRC(7) // unknown
	w.WriteBL(2) // num_obj_ids
	datEnd := w.TellBits()
	w.WriteH(4, 1, 10)   // owner
	w.WriteH(3, 0, 0)    // xdic
	w.WriteH(5, 2, 0x91) // obj_ids[0]
	w.WriteH(5, 2, 0x92) // obj_ids[1]
	padToByte(w)
	body := w.Bytes()
	fillR2000Bitsize(body, datEnd)

	rec := &objrec.ObjectRecord{Body: body, BodyBitOffset: 0, Size: uint32(len(body))}
	g, err := decodeInternalObject(rec.BodyBitStream(), rec, verR2000, false, 0x51, "IDBUFFER", 30)
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if g.Name != "IDBUFFER" {
		t.Errorf("Name=%q", g.Name)
	}
	assertField(t, g, "unknown", int64(7))
	assertField(t, g, "num_obj_ids", int64(2))
	if g.Handle != 0x64 {
		t.Errorf("Handle=%d", g.Handle)
	}
	if g.Owner != 10 {
		t.Errorf("Owner=%d", g.Owner)
	}
	if len(g.Handles) != 2 || g.Handles[0] != 0x91 || g.Handles[1] != 0x92 {
		t.Errorf("obj_ids=%v (want [0x91 0x92])", g.Handles)
	}
}

// TestIndexSynthetic 合成 R2000 INDEX：TIMEBLL last_updated=[days, ms]。
func TestIndexSynthetic(t *testing.T) {
	w := bitstream.NewEncWriter()
	writeR2000ObjectPrefix(w, 0x65)
	w.WriteBL(40210)  // last_updated.days
	w.WriteBL(314159) // last_updated.ms
	datEnd := w.TellBits()
	w.WriteH(4, 1, 11) // owner
	w.WriteH(3, 0, 0)  // xdic
	padToByte(w)
	body := w.Bytes()
	fillR2000Bitsize(body, datEnd)

	rec := &objrec.ObjectRecord{Body: body, BodyBitOffset: 0, Size: uint32(len(body))}
	// 0x54：无固定解码器冲突的占位类型码（INDEX 无 R13+ 固定码，经类表路由）
	g, err := decodeInternalObject(rec.BodyBitStream(), rec, verR2000, false, 0x54, "INDEX", 30)
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if g.Name != "INDEX" {
		t.Errorf("Name=%q", g.Name)
	}
	lu, ok := g.Field("last_updated").([]int64)
	if !ok || len(lu) != 2 || lu[0] != 40210 || lu[1] != 314159 {
		t.Errorf("last_updated=%v (want [40210 314159])", g.Field("last_updated"))
	}
	if g.Owner != 11 {
		t.Errorf("Owner=%d", g.Owner)
	}
}

// TestLayerIndexSynthetic 合成 R2000 LAYER_INDEX：TIMEBLL + num_entries=2 +
// 2×(numlayers BL + name TV) + handle 流 owner/xdic/layer handles×2。
func TestLayerIndexSynthetic(t *testing.T) {
	w := bitstream.NewEncWriter()
	writeR2000ObjectPrefix(w, 0x66)
	w.WriteBL(40000)  // last_updated.days
	w.WriteBL(250000) // last_updated.ms
	w.WriteBL(2)      // num_entries
	w.WriteBL(1)      // entries[0].numlayers
	w.WriteTV("Wall") // entries[0].name
	w.WriteBL(2)      // entries[1].numlayers
	w.WriteTV("Door") // entries[1].name
	datEnd := w.TellBits()
	w.WriteH(4, 1, 12)   // owner
	w.WriteH(3, 0, 0)    // xdic
	w.WriteH(5, 2, 0x8A) // entries[0].handle
	w.WriteH(5, 2, 0x8B) // entries[1].handle
	padToByte(w)
	body := w.Bytes()
	fillR2000Bitsize(body, datEnd)

	rec := &objrec.ObjectRecord{Body: body, BodyBitOffset: 0, Size: uint32(len(body))}
	g, err := decodeInternalObject(rec.BodyBitStream(), rec, verR2000, false, 0x53, "LAYER_INDEX", 30)
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if g.Name != "LAYER_INDEX" {
		t.Errorf("Name=%q", g.Name)
	}
	assertField(t, g, "num_entries", int64(2))
	assertField(t, g, "entries[0].numlayers", int64(1))
	assertField(t, g, "entries[0].name", "Wall")
	assertField(t, g, "entries[1].numlayers", int64(2))
	assertField(t, g, "entries[1].name", "Door")
	if len(g.Handles) != 2 || g.Handles[0] != 0x8A || g.Handles[1] != 0x8B {
		t.Errorf("entries handles=%v (want [0x8A 0x8B])", g.Handles)
	}
	assertField(t, g, "entries[0].handle", int64(0x8A))
	assertField(t, g, "entries[1].handle", int64(0x8B))
}

// TestProxyObjectSynthetic 合成 R2000 PROXY_OBJECT（固定码 0x1F3）：
// proxy_id=499 + version=0x0102 + from_dxf=0 + 3 字节原始数据位 +
// handle 流 owner/xdic/objids×2。
func TestProxyObjectSynthetic(t *testing.T) {
	w := bitstream.NewEncWriter()
	writeR2000ObjectPrefix(w, 0x67)
	w.WriteBL(499)    // proxy_id（恒 499）
	w.WriteBL(0x0102) // version：maint=1、dwg=2
	w.WriteB(false)   // from_dxf
	// 原始数据位串：任意内容，写入后位长由 bitsize 定界捕获
	dataBytes := []byte{0xAB, 0xCD, 0xEF}
	w.WriteTF(dataBytes)
	datEnd := w.TellBits()
	w.WriteH(4, 1, 13)   // owner
	w.WriteH(3, 0, 0)    // xdic
	w.WriteH(5, 2, 0x93) // objids[0]
	w.WriteH(5, 2, 0x94) // objids[1]
	padToByte(w)
	body := w.Bytes()
	fillR2000Bitsize(body, datEnd)

	rec := &objrec.ObjectRecord{Body: body, BodyBitOffset: 0, Size: uint32(len(body))}
	g, err := decodeInternalObject(rec.BodyBitStream(), rec, verR2000, false, 0x1F3, "PROXY_OBJECT", 30)
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	assertField(t, g, "proxy_id", int64(499))
	assertField(t, g, "version", int64(0x0102))
	assertField(t, g, "maint_version", int64(0x01))
	assertField(t, g, "dwg_version", int64(0x02))
	if fv, ok := g.Field("from_dxf").(bool); !ok || fv {
		t.Errorf("from_dxf=%v (want false)", g.Field("from_dxf"))
	}
	// data 捕获：位长 = bitsize - proxy_id/version/from_dxf 之后的位，
	// hex 字符串与写入字节一致（gold JSON 同为 hex 串形状）
	nb, ok := g.Field("data_numbits").(int64)
	if !ok || nb <= 0 {
		t.Fatalf("data_numbits=%v（应捕获到原始数据位）", g.Field("data_numbits"))
	}
	ds, ok := g.Field("data").(string)
	if !ok || !strings.EqualFold(ds, "ABCDEF") {
		t.Errorf("data=%q (want %q)", ds, "ABCDEF")
	}
	// handle 流：owner/xdic 之后的 objids 全量
	if len(g.Handles) != 2 || g.Handles[0] != 0x93 || g.Handles[1] != 0x94 {
		t.Errorf("objids=%v (want [0x93 0x94])", g.Handles)
	}
}
