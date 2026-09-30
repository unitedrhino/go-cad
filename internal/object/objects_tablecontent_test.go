// objects_tablecontent_test TABLECONTENT/DATATABLE 解码器的合成位流
// 门禁测试：手工构造已知内容的位流，解码后逐字段断言（含嵌套
// cols/rows/cells/cell_contents 与 handle 流句柄数）。
package object

import (
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/objrec"
	"math"
	"testing"
)

// TestTableContentSynthetic 合成 R2000 TABLECONTENT 位流：
// 1 列 × 1 行 × 1 个 kLong 单元格内容，cellstyle data_flags=0 终止，
// handle 流 = owner + xdic + tablestyle。
func TestTableContentSynthetic(t *testing.T) {
	// TABLECONTENT 属 DEBUG_CLASSES 类未注册进解码表，测试内临时注册
	InternalClassDecoders["TABLECONTENT"] = internalObjectSpec{Decode: DecodeGenericTABLECONTENT, Hdl: decodeGenericTABLECONTENT_HDL}
	defer delete(InternalClassDecoders, "TABLECONTENT")
	w := bitstream.NewEncWriter()
	w.WriteRL(0) // bitsize 占位
	w.WriteH(0, 1, 0x64)
	w.WriteBS(0) // EED 终止
	w.WriteBL(0) // num_reactors = 0
	// R2000 无 xdic/ds 位
	// AcDbLinkedData
	w.WriteTV("MyTable")
	w.WriteTV("desc")
	// cols
	w.WriteBL(1)
	w.WriteTV("col0")
	w.WriteBL(0) // custom_data
	w.WriteBL(0) // cellstyle.type
	w.WriteBS(0) // cellstyle.data_flags = 0（终止）
	// rows
	w.WriteBL(1)
	w.WriteBL(1)  // num_cells
	w.WriteBL(0)  // flag
	w.WriteTV("") // tooltip
	w.WriteBL(0)  // customdata
	w.WriteBL(0)  // num_customdata_items
	w.WriteBL(0)  // has_linked_data
	w.WriteBL(1)  // num_cell_contents
	// cell_contents[0]：kLong = 42
	w.WriteBL(1) // type
	w.WriteBL(1) // value.data_type = kLong
	w.WriteBL(42)
	w.WriteBL(0) // num_attrs
	w.WriteBS(0) // has_content_format_overrides
	w.WriteBL(0) // style_id
	w.WriteBL(0) // has_geom_data
	// row 级
	w.WriteBL(0) // custom_data
	w.WriteBL(0) // num_customdata_items
	w.WriteBL(0) // cellstyle.type
	w.WriteBS(0) // cellstyle.data_flags
	w.WriteBL(0) // style_id
	w.WriteBD(5.0)
	// field_refs + merged_cells
	w.WriteBL(0)
	w.WriteBL(0)
	// handle 流
	datEnd := w.TellBits()
	w.WriteH(4, 1, 10)   // owner
	w.WriteH(3, 0, 0)    // xdic
	w.WriteH(3, 1, 0x66) // tablestyle
	for w.Bit != 0 {
		w.WriteBitsMsb(0, 1)
	}
	original := w.Bytes()
	original[0] = uint8(datEnd)
	original[1] = uint8(datEnd >> 8)
	original[2] = uint8(datEnd >> 16)
	original[3] = uint8(datEnd >> 24)

	rec := &objrec.ObjectRecord{Body: original, BodyBitOffset: 0, Size: uint32(len(original))}
	g, err := DecodeInternalObject(rec.BodyBitStream(), rec, container.VerR2000, false, 537, "TABLECONTENT", 30)
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	assertField(t, g, "ldata.name", "MyTable")
	assertField(t, g, "ldata.description", "desc")
	assertField(t, g, "tdata.num_cols", int64(1))
	assertField(t, g, "tdata.cols[0].name", "col0")
	assertField(t, g, "tdata.num_rows", int64(1))
	assertField(t, g, "tdata.rows[0].cells[0].cell_contents[0].value.data_long", int64(42))
	assertField(t, g, "tdata.rows[0].height", 5.0)
	// handle 流：tablestyle 1 个（cellstyle/field_refs 等均无）
	assertField(t, g, "num_content_handles", int64(1))
	if len(g.Handles) != 1 || g.Handles[0] != 0x66 {
		t.Errorf("Handles: %v (want [0x66])", g.Handles)
	}
}

// TestDataTableSynthetic 合成 R2000 DATATABLE 位流：2 列 × 1 行，
// 行值按 spec 无条件读三键。
func TestDataTableSynthetic(t *testing.T) {
	InternalClassDecoders["DATATABLE"] = internalObjectSpec{Decode: decodeGenericDATATABLE}
	defer delete(InternalClassDecoders, "DATATABLE")
	w := bitstream.NewEncWriter()
	w.WriteRL(0)
	w.WriteH(0, 1, 0x64)
	w.WriteBS(0) // EED
	w.WriteBL(0) // num_reactors
	w.WriteBS(1) // flags
	w.WriteBL(2) // num_cols
	w.WriteBL(1) // num_rows
	w.WriteTV("DT")
	// cols[0]
	w.WriteBL(1) // type
	w.WriteTV("c0")
	w.WriteBL(7)    // rows[0].value.data_long
	w.WriteBD(1.5)  // data_double（解码端 readBD 压缩格式）
	w.WriteTV("s0") // data_string
	// cols[1]
	w.WriteBL(2) // type
	w.WriteTV("c1")
	w.WriteBL(8)
	w.WriteBD(2.5)
	w.WriteTV("s1")
	// handle 流
	datEnd := w.TellBits()
	w.WriteH(4, 1, 10)
	w.WriteH(3, 0, 0)
	for w.Bit != 0 {
		w.WriteBitsMsb(0, 1)
	}
	original := w.Bytes()
	original[0] = uint8(datEnd)
	original[1] = uint8(datEnd >> 8)
	original[2] = uint8(datEnd >> 16)
	original[3] = uint8(datEnd >> 24)

	rec := &objrec.ObjectRecord{Body: original, BodyBitOffset: 0, Size: uint32(len(original))}
	g, err := DecodeInternalObject(rec.BodyBitStream(), rec, container.VerR2000, false, 541, "DATATABLE", 30)
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	assertField(t, g, "flags", int64(1))
	assertField(t, g, "num_cols", int64(2))
	assertField(t, g, "num_rows", int64(1))
	assertField(t, g, "table_name", "DT")
	assertField(t, g, "cols[0].rows[0].value.data_long", int64(7))
	assertField(t, g, "cols[0].rows[0].value.data_string", "s0")
	assertField(t, g, "cols[1].rows[0].value.data_double", 2.5)
}

// assertField 断言 objGeneric 字段值：got 与 want 做数值归一化比较
// （int64/float64 互转，float 容差），字符串直接比较。
func assertField(t *testing.T, g *ObjGeneric, key string, want any) {
	t.Helper()
	got := g.Field(key)
	if got == nil {
		t.Errorf("字段 %s 缺失", key)
		return
	}
	switch w := want.(type) {
	case string:
		if s, ok := got.(string); !ok || s != w {
			t.Errorf("字段 %s: got %v want %q", key, got, w)
		}
	case int64:
		if n, ok := got.(int64); !ok || n != w {
			t.Errorf("字段 %s: got %v want %d", key, got, w)
		}
	case float64:
		switch n := got.(type) {
		case float64:
			if math.Abs(n-w) > 1e-9 {
				t.Errorf("字段 %s: got %v want %v", key, got, w)
			}
		case int64:
			if float64(n) != w {
				t.Errorf("字段 %s: got %v want %v", key, got, w)
			}
		default:
			t.Errorf("字段 %s: got %T want float64", key, got)
		}
	default:
		t.Errorf("assertField 不支持 want 类型 %T", want)
	}
}
