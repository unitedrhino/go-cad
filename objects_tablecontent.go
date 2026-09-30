// 本文件实现 TABLECONTENT（类 530 后的 TABLE 类族）与 DATATABLE 内部
// 对象的解码（dwg2.spec DWG_OBJECT(TABLECONTENT)/DWG_OBJECT(DATATABLE)）。
// TABLECONTENT 主体为 TABLECONTENTs_fields 宏（AcDbLinkedData +
// AcDbLinkedTableData：cols/rows/cells/cell_contents 嵌套结构），依赖
// TABLE_value_fields（Dwg_TABLE_value）与 CellStyle_fields/
// ContentFormat_fields（objects_visual.go 已有）。
// 全部内嵌句柄（text_style/ltype/data_link/attdef/field_refs/tablestyle
// 等）在 handle 流，decode 阶段计数 num_content_handles，hdl 阶段按序读。
package cad

import (
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/objrec"
)

// readTableValueFields 读取 TABLE_value_fields 宏（Dwg_TABLE_value，
// dwg_spec_shared.h）：R2007+ 先 BL format_flags；BL data_type 按
// kXxx 类型分支读值；R2007+ 尾部 unit_type/format_string/value_string。
// data_type=64（kObjectId）的句柄在 handle 流，nHdl 计数。
func readTableValueFields(r *bitstream.BitStream, fr *gfRead, g *objGeneric, prefix string, nHdl *int) error {
	if verUntilR2004(fr.ver) {
		// PRE R_2007a：data_type &= ~0x200 为解码后值修饰，不占位
	} else {
		if err := fr.BL(prefix+"format_flags", g); err != nil {
			return err
		}
	}
	dt, err := fr.BLv(prefix+"data_type", g)
	if err != nil {
		return err
	}
	if verUntilR2004(fr.ver) {
		// PRE R_2007a：data_type &= ~0x200 为读后值修饰，不占位；
		// BLv 已按原始值入 Fields，这里追加修正值（Field 取末次）
		dt &^= 0x200
		g.Fields = append(g.Fields, objField{prefix + "data_type", dt})
	}
	// R2007+ 且 format_flags 低 2 位非 0：跳过按 data_type 的值分支
	skip := fr.ver >= container.VerR2007
	if skip {
		ff, _ := g.Field(prefix + "format_flags").(int64)
		if ff&3 == 0 {
			skip = false
		}
	}
	if !skip {
		switch dt {
		case 0, 1: // kUnknown / kLong
			if err := fr.BL(prefix+"data_long", g); err != nil {
				return err
			}
		case 2: // kDouble
			if err := fr.BD(prefix+"data_double", g); err != nil {
				return err
			}
		case 4: // kString
			if err := fr.T(prefix+"data_string", g); err != nil {
				return err
			}
		case 8: // kDate
			sz, e := fr.BLv(prefix+"data_size", g)
			if e != nil {
				return e
			}
			if sz < 0 || sz > 1<<20 {
				return fmt.Errorf("cad: TABLE value 日期长度异常 %d", sz)
			}
			b, e := r.ReadBitsBytes(int(sz))
			if e != nil {
				return e
			}
			g.Fields = append(g.Fields, objField{prefix + "data_date", b})
		case 16: // kPoint
			if _, e := fr.BLv(prefix+"data_size", g); e != nil {
				return e
			}
			if err := fr.Point2(prefix+"data_point", g); err != nil {
				return err
			}
		case 32: // k3dPoint
			if _, e := fr.BLv(prefix+"data_size", g); e != nil {
				return e
			}
			if err := fr.Point3(prefix+"data_3dpoint", g); err != nil {
				return err
			}
		case 64: // kObjectId：句柄在 handle 流
			*nHdl++
		case 512: // kGeneral（R2007+）
			if fr.ver >= container.VerR2007 {
				if _, e := fr.BLv(prefix+"data_size", g); e != nil {
					return e
				}
			}
		default:
			return fmt.Errorf("cad: TABLE value 未知数据类型 %d", dt)
		}
	}
	if fr.ver >= container.VerR2007 {
		ut, e := fr.BLv(prefix+"unit_type", g)
		if e != nil {
			return e
		}
		if err := fr.T(prefix+"format_string", g); err != nil {
			return err
		}
		if ut != 12 {
			if err := fr.T(prefix+"value_string", g); err != nil {
				return err
			}
		}
	}
	return nil
}

// readTableCustomDataItems 读取 Dwg_TABLE_CustomDataItem 向量
// （name T + TABLE_value_fields），cell 与 row 复用。
func readTableCustomDataItems(r *bitstream.BitStream, fr *gfRead, g *objGeneric, prefix string, nHdl *int) error {
	n, err := fr.BLv(prefix+"num_customdata_items", g)
	if err != nil {
		return err
	}
	if n < 0 || n > 10000 {
		return fmt.Errorf("cad: TABLE customdata 项数异常 %d", n)
	}
	for i := 0; i < int(n); i++ {
		p := fmt.Sprintf("%scustomdata_items[%d].", prefix, i)
		if err := fr.T(p+"name", g); err != nil {
			return err
		}
		if err := readTableValueFields(r, fr, g, p+"value.", nHdl); err != nil {
			return err
		}
	}
	return nil
}

// readTableCellStyle 读取 cellstyle 前缀的 CellStyle_fields（句柄计数
// 由 readCellStyleFields 内部的 nHdl 自增完成：text_style 与 ltype）。
func readTableCellStyle(r *bitstream.BitStream, fr *gfRead, g *objGeneric, prefix string, nHdl *int) error {
	return readCellStyleFields(r, fr, g, prefix, nHdl)
}

// decodeGenericTABLECONTENT 解析 TABLECONTENT（TABLECONTENTs_fields，
// pg.237 20.4.97）：ldata.name/description + tdata.cols/rows/cells/
// cell_contents 嵌套 + field_refs 数量 + fdata.merged_cells。
// tablestyle 及全部内嵌句柄在 handle 流。
func decodeGenericTABLECONTENT(r *bitstream.BitStream, ver container.DwgVersion, fr *gfRead, g *objGeneric) error {
	nHdl := 0
	// AcDbLinkedData
	if err := fr.T("ldata.name", g); err != nil {
		return err
	}
	if err := fr.T("ldata.description", g); err != nil {
		return err
	}

	// AcDbLinkedTableData：cols
	nc, err := fr.BLv("tdata.num_cols", g)
	if err != nil {
		return err
	}
	if nc < 0 || nc > 10000 {
		return fmt.Errorf("cad: TABLECONTENT 列数异常 %d", nc)
	}
	for i := 0; i < int(nc); i++ {
		p := fmt.Sprintf("tdata.cols[%d].", i)
		if err := fr.T(p+"name", g); err != nil {
			return err
		}
		if _, err = fr.BLv(p+"custom_data", g); err != nil {
			return err
		}
		if err := readTableCellStyle(r, fr, g, p+"cellstyle.", &nHdl); err != nil {
			return err
		}
	}
	// rows → cells → cell_contents
	nr, err := fr.BLv("tdata.num_rows", g)
	if err != nil {
		return err
	}
	if nr < 0 || nr > 10000 {
		return fmt.Errorf("cad: TABLECONTENT 行数异常 %d", nr)
	}
	for i := 0; i < int(nr); i++ {
		rp := fmt.Sprintf("tdata.rows[%d].", i)
		ncell, e := fr.BLv(rp+"num_cells", g)
		if e != nil {
			return e
		}
		if ncell < 0 || ncell > 10000 {
			return fmt.Errorf("cad: TABLECONTENT 单元格数异常 %d", ncell)
		}
		for j := 0; j < int(ncell); j++ {
			cp := fmt.Sprintf("%scells[%d].", rp, j)
			if _, e = fr.BLv(cp+"flag", g); e != nil {
				return e
			}
			if e = fr.T(cp+"tooltip", g); e != nil {
				return e
			}
			if _, e = fr.BLv(cp+"customdata", g); e != nil {
				return e
			}
			if e = readTableCustomDataItems(r, fr, g, cp, &nHdl); e != nil {
				return e
			}
			// has_linked_data：data_link 句柄 + 行列数 + unknown
			hld, e := fr.BLv(cp+"has_linked_data", g)
			if e != nil {
				return e
			}
			if hld != 0 {
				nHdl++ // data_link
				for _, k := range []string{"num_rows", "num_cols", "unknown"} {
					if _, e = fr.BLv(cp+k, g); e != nil {
						return e
					}
				}
			}
			// cell_contents
			ncc, e := fr.BLv(cp+"num_cell_contents", g)
			if e != nil {
				return e
			}
			if ncc < 0 || ncc > 10000 {
				return fmt.Errorf("cad: TABLECONTENT 内容数异常 %d", ncc)
			}
			for k := 0; k < int(ncc); k++ {
				q := fmt.Sprintf("%scell_contents[%d].", cp, k)
				ct, e := fr.BLv(q+"type", g)
				if e != nil {
					return e
				}
				switch ct {
				case 1: // Value
					if e = readTableValueFields(r, fr, g, q+"value.", &nHdl); e != nil {
						return e
					}
				case 2, 4: // Field / Block：句柄在 handle 流
					nHdl++
				}
				// attrs
				na, e := fr.BLv(q+"num_attrs", g)
				if e != nil {
					return e
				}
				if na < 0 || na > 10000 {
					return fmt.Errorf("cad: TABLECONTENT attr 数异常 %d", na)
				}
				for m := 0; m < int(na); m++ {
					aq := fmt.Sprintf("%sattrs[%d].", q, m)
					nHdl++ // attdef
					if e = fr.T(aq+"value", g); e != nil {
						return e
					}
					if _, e = fr.BLv(aq+"index", g); e != nil {
						return e
					}
				}
				// has_content_format_overrides
				hcfo, e := fr.BSv(q+"has_content_format_overrides", g)
				if e != nil {
					return e
				}
				if hcfo != 0 {
					if e = readContentFormatFields(r, fr, g, q+"content_format.", &nHdl); e != nil {
						return e
					}
				}
			}
			// style_id + has_geom_data
			if _, e = fr.BLv(cp+"style_id", g); e != nil {
				return e
			}
			hgd, e := fr.BLv(cp+"has_geom_data", g)
			if e != nil {
				return e
			}
			if hgd != 0 {
				if _, e = fr.BLv(cp+"geom_data_flag", g); e != nil {
					return e
				}
				if e = fr.BD(cp+"width_w_gap", g); e != nil {
					return e
				}
				if e = fr.BD(cp+"height_w_gap", g); e != nil {
					return e
				}
				nHdl++ // tablegeometry
				ng, e := fr.BLv(cp+"num_geometry", g)
				if e != nil {
					return e
				}
				if ng < 0 || ng > 10000 {
					return fmt.Errorf("cad: TABLECONTENT geometry 数异常 %d", ng)
				}
				for m := 0; m < int(ng); m++ {
					gq := fmt.Sprintf("%sgeometry[%d].", cp, m)
					if e = fr.Point3(gq+"dist_top_left", g); e != nil {
						return e
					}
					if e = fr.Point3(gq+"dist_center", g); e != nil {
						return e
					}
					for _, k := range []string{"content_width", "content_height", "width", "height"} {
						if e = fr.BD(gq+k, g); e != nil {
							return e
						}
					}
					if _, e = fr.BLv(gq+"unknown", g); e != nil {
						return e
					}
				}
			}
		}
		// row 级 custom_data/items + cellstyle + style_id + height
		if _, e := fr.BLv(rp+"custom_data", g); e != nil {
			return e
		}
		if e := readTableCustomDataItems(r, fr, g, rp, &nHdl); e != nil {
			return e
		}
		if e := readTableCellStyle(r, fr, g, rp+"cellstyle.", &nHdl); e != nil {
			return e
		}
		if _, e := fr.BLv(rp+"style_id", g); e != nil {
			return e
		}
		if e := fr.BD(rp+"height", g); e != nil {
			return e
		}
	}
	// field_refs 数量（句柄在 handle 流）
	nfr, err := fr.BLv("tdata.num_field_refs", g)
	if err != nil {
		return err
	}
	if nfr < 0 || nfr > 10000 {
		return fmt.Errorf("cad: TABLECONTENT field_refs 数异常 %d", nfr)
	}
	nHdl += int(nfr)
	// fdata.merged_cells
	nmc, err := fr.BLv("fdata.num_merged_cells", g)
	if err != nil {
		return err
	}
	if nmc < 0 || nmc > 10000 {
		return fmt.Errorf("cad: TABLECONTENT merged_cells 数异常 %d", nmc)
	}
	for i := 0; i < int(nmc); i++ {
		p := fmt.Sprintf("fdata.merged_cells[%d].", i)
		for _, k := range []string{"top_row", "left_col", "bottom_row", "right_col"} {
			if _, err = fr.BLv(p+k, g); err != nil {
				return err
			}
		}
	}
	// tablestyle（handle 流尾部 1 个）
	nHdl++
	g.Fields = append(g.Fields, objField{"num_content_handles", int64(nHdl)})
	return nil
}

// decodeGenericTABLECONTENT_HDL TABLECONTENT 的 handle 流：按 decode
// 阶段记录的 num_content_handles 顺序读取（cellstyle text_style/ltype、
// customdata value 句柄、data_link、field/block 句柄、attdef、
// tablegeometry、field_refs、tablestyle）。
func decodeGenericTABLECONTENT_HDL(r *bitstream.BitStream, ver container.DwgVersion, fr *gfRead, g *objGeneric) error {
	n := 0
	if v, ok := g.Field("num_content_handles").(int64); ok {
		n = int(v)
	}
	for i := 0; i < n; i++ {
		h, e := objrec.ReadHandleReference(r, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// decodeGenericDATATABLE 解析 DATATABLE（dwg2.spec DWG_OBJECT(DATATABLE)，
// DEBUG_CLASSES 调试类）：flags BS + num_cols/num_rows BL + table_name T
// + 列（type BL + text T + 行值向量）。行值按 spec 无条件读
// data_long BL + data_double BD + data_string T 三键（与 LibreDWG 一致）。
func decodeGenericDATATABLE(r *bitstream.BitStream, ver container.DwgVersion, fr *gfRead, g *objGeneric) error {
	if err := fr.BS("flags", g); err != nil {
		return err
	}
	nc, err := fr.BLv("num_cols", g)
	if err != nil {
		return err
	}
	nrows, err := fr.BLv("num_rows", g)
	if err != nil {
		return err
	}
	if nc < 0 || nc > 10000 || nrows < 0 || nrows > 10000 {
		return fmt.Errorf("cad: DATATABLE 规模异常 cols=%d rows=%d", nc, nrows)
	}
	if err := fr.T("table_name", g); err != nil {
		return err
	}
	for i := 0; i < int(nc); i++ {
		p := fmt.Sprintf("cols[%d].", i)
		if _, err = fr.BLv(p+"type", g); err != nil {
			return err
		}
		if err = fr.T(p+"text", g); err != nil {
			return err
		}
		for j := 0; j < int(nrows); j++ {
			q := fmt.Sprintf("%srows[%d].value.", p, j)
			if err = fr.BL(q+"data_long", g); err != nil {
				return err
			}
			if err = fr.BD(q+"data_double", g); err != nil {
				return err
			}
			if err = fr.T(q+"data_string", g); err != nil {
				return err
			}
		}
	}
	return nil
}
