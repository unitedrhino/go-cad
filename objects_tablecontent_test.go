// objects_tablecontent_test TABLECONTENT/DATATABLE 解码器的合成位流
// 门禁测试：手工构造已知内容的位流，解码后逐字段断言（含嵌套
// cols/rows/cells/cell_contents 与 handle 流句柄数）。
package cad

import (
	"math"
	"testing"
)

// TestTableContentSynthetic 合成 R2000 TABLECONTENT 位流：
// 1 列 × 1 行 × 1 个 kLong 单元格内容，cellstyle data_flags=0 终止，
// handle 流 = owner + xdic + tablestyle。
func TestTableContentSynthetic(t *testing.T) {
	// TABLECONTENT 属 DEBUG_CLASSES 类未注册进解码表，测试内临时注册
	internalClassDecoders["TABLECONTENT"] = internalObjectSpec{decode: decodeGenericTABLECONTENT, hdl: decodeGenericTABLECONTENT_HDL}
	defer delete(internalClassDecoders, "TABLECONTENT")
	w := newEncWriter()
	w.writeRL(0) // bitsize 占位
	w.writeH(0, 1, 0x64)
	w.writeBS(0) // EED 终止
	w.writeBL(0) // num_reactors = 0
	// R2000 无 xdic/ds 位
	// AcDbLinkedData
	w.writeTV("MyTable")
	w.writeTV("desc")
	// cols
	w.writeBL(1)
	w.writeTV("col0")
	w.writeBL(0) // custom_data
	w.writeBL(0) // cellstyle.type
	w.writeBS(0) // cellstyle.data_flags = 0（终止）
	// rows
	w.writeBL(1)
	w.writeBL(1)  // num_cells
	w.writeBL(0)  // flag
	w.writeTV("") // tooltip
	w.writeBL(0)  // customdata
	w.writeBL(0)  // num_customdata_items
	w.writeBL(0)  // has_linked_data
	w.writeBL(1)  // num_cell_contents
	// cell_contents[0]：kLong = 42
	w.writeBL(1) // type
	w.writeBL(1) // value.data_type = kLong
	w.writeBL(42)
	w.writeBL(0) // num_attrs
	w.writeBS(0) // has_content_format_overrides
	w.writeBL(0) // style_id
	w.writeBL(0) // has_geom_data
	// row 级
	w.writeBL(0) // custom_data
	w.writeBL(0) // num_customdata_items
	w.writeBL(0) // cellstyle.type
	w.writeBS(0) // cellstyle.data_flags
	w.writeBL(0) // style_id
	w.writeBD(5.0)
	// field_refs + merged_cells
	w.writeBL(0)
	w.writeBL(0)
	// handle 流
	datEnd := w.tellBits()
	w.writeH(4, 1, 10)   // owner
	w.writeH(3, 0, 0)    // xdic
	w.writeH(3, 1, 0x66) // tablestyle
	for w.bit != 0 {
		w.writeBitsMsb(0, 1)
	}
	original := w.bytes()
	original[0] = uint8(datEnd)
	original[1] = uint8(datEnd >> 8)
	original[2] = uint8(datEnd >> 16)
	original[3] = uint8(datEnd >> 24)

	rec := &objectRecord{body: original, bodyBitOffset: 0, size: uint32(len(original))}
	g, err := decodeInternalObject(rec.bodyBitStream(), rec, verR2000, false, 537, "TABLECONTENT", 30)
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
	internalClassDecoders["DATATABLE"] = internalObjectSpec{decode: decodeGenericDATATABLE}
	defer delete(internalClassDecoders, "DATATABLE")
	w := newEncWriter()
	w.writeRL(0)
	w.writeH(0, 1, 0x64)
	w.writeBS(0) // EED
	w.writeBL(0) // num_reactors
	w.writeBS(1) // flags
	w.writeBL(2) // num_cols
	w.writeBL(1) // num_rows
	w.writeTV("DT")
	// cols[0]
	w.writeBL(1) // type
	w.writeTV("c0")
	w.writeBL(7)    // rows[0].value.data_long
	w.writeBD(1.5)  // data_double（解码端 readBD 压缩格式）
	w.writeTV("s0") // data_string
	// cols[1]
	w.writeBL(2) // type
	w.writeTV("c1")
	w.writeBL(8)
	w.writeBD(2.5)
	w.writeTV("s1")
	// handle 流
	datEnd := w.tellBits()
	w.writeH(4, 1, 10)
	w.writeH(3, 0, 0)
	for w.bit != 0 {
		w.writeBitsMsb(0, 1)
	}
	original := w.bytes()
	original[0] = uint8(datEnd)
	original[1] = uint8(datEnd >> 8)
	original[2] = uint8(datEnd >> 16)
	original[3] = uint8(datEnd >> 24)

	rec := &objectRecord{body: original, bodyBitOffset: 0, size: uint32(len(original))}
	g, err := decodeInternalObject(rec.bodyBitStream(), rec, verR2000, false, 541, "DATATABLE", 30)
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
func assertField(t *testing.T, g *objGeneric, key string, want any) {
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
