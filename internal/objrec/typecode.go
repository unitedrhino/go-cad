// typecode.go DWG 类型码命名与实体判定：固定类型码静态表（objTypeCode）、
// 动态类名表查询（EntityTypeName）与实体类型码判定（IsEntityType）。
// 纯表驱动逻辑，供实体/对象/文档编排多层共用（自 objects.go/cad.go 上提）。
package objrec

import "github.com/unitedrhino/go-cad/internal/bitstream"

// objTypeCodeByName 常用类型码 → 名称静态表（<500 的固定段，按码值
// 紧凑排列；0x1F3 PROXY_OBJECT 为高位特例）。
var ObjTypeCode = map[uint16]string{
	0x01:  "TEXT",
	0x02:  "ATTRIB",
	0x03:  "ATTDEF",
	0x04:  "BLOCK",
	0x05:  "ENDBLK",
	0x06:  "SEQEND",
	0x07:  "INSERT",
	0x08:  "MINSERT",
	0x0A:  "VERTEX_2D",
	0x0B:  "VERTEX_3D",
	0x0C:  "VERTEX_MESH",
	0x0D:  "VERTEX_PFACE",
	0x0E:  "VERTEX_PFACE_FACE",
	0x0F:  "POLYLINE_2D",
	0x10:  "POLYLINE_3D",
	0x11:  "ARC",
	0x12:  "CIRCLE",
	0x13:  "LINE",
	0x14:  "DIM_ORDINATE",
	0x15:  "DIM_LINEAR",
	0x16:  "DIM_ALIGNED",
	0x17:  "DIM_ANG3PT",
	0x18:  "DIM_ANG2LN",
	0x19:  "DIM_RADIUS",
	0x1A:  "DIM_DIAMETER",
	0x1B:  "POINT",
	0x1C:  "3DFACE",
	0x1D:  "POLYLINE_PFACE",
	0x1E:  "POLYLINE_MESH",
	0x1F:  "SOLID",
	0x20:  "TRACE",
	0x21:  "SHAPE",
	0x22:  "VIEWPORT",
	0x23:  "ELLIPSE",
	0x24:  "SPLINE",
	0x28:  "RAY",
	0x29:  "XLINE",
	0x2A:  "DICTIONARY",
	0x2C:  "MTEXT",
	0x2D:  "LEADER",
	0x2E:  "TOLERANCE",
	0x2F:  "MLINE",
	0x30:  "BLOCK_CONTROL",
	0x31:  "BLOCK_HEADER",
	0x32:  "LAYER_CONTROL",
	0x33:  "LAYER",
	0x34:  "STYLE_CONTROL",
	0x35:  "STYLE",
	0x38:  "LTYPE_CONTROL",
	0x39:  "LTYPE",
	0x3C:  "VIEW_CONTROL",
	0x3D:  "VIEW",
	0x3E:  "UCS_CONTROL",
	0x3F:  "UCS",
	0x40:  "VPORT_CONTROL",
	0x41:  "VPORT",
	0x42:  "APPID_CONTROL",
	0x43:  "APPID",
	0x44:  "DIMSTYLE_CONTROL",
	0x45:  "DIMSTYLE",
	0x46:  "VX_CONTROL",
	0x47:  "VX_TABLE_RECORD",
	0x48:  "GROUP",
	0x49:  "MLINESTYLE",
	0x4D:  "LWPOLYLINE",
	0x4F:  "XRECORD",
	0x50:  "PLACEHOLDER",
	0x52:  "LAYOUT",
	0x1F3: "PROXY_OBJECT",
}

// entityTypeName 返回类型码名称：先查静态表，再查动态类名表（≥500）。
func EntityTypeName(code uint16, dynamic map[uint16]string) string {
	if name, ok := ObjTypeCode[code]; ok {
		return name
	}
	if name, ok := dynamic[code]; ok {
		return name
	}
	return ""
}

// readHandleReference 解析相对句柄引用：code 6/8 为 ±1，A/C 为加减偏移。
func ReadHandleReference(r *bitstream.BitStream, base uint64) (uint64, error) {
	h, err := r.ReadH()
	if err != nil {
		return 0, err
	}
	switch h.Code {
	case 0x06:
		return base + 1, nil
	case 0x08:
		if base == 0 {
			return 0, nil
		}
		return base - 1, nil
	case 0x0A:
		return base + h.Value, nil
	case 0x0C:
		if h.Value > base {
			return 0, nil
		}
		return base - h.Value, nil
	default:
		return h.Value, nil
	}
}

// layerTypeCode LAYER 表记录类型码（候选连续性加权的目标类型）。
const layerTypeCode = 0x33

// isEntityType 判断类型码是否为可渲染实体：图元直判 + 动态类名命中常见实体后缀。
func IsEntityType(code uint16, dynamic map[uint16]string) bool {
	switch ObjTypeCode[code] {
	case "TEXT", "ATTRIB", "ATTDEF", "INSERT", "MINSERT", "ARC", "CIRCLE", "LINE",
		"POINT", "ELLIPSE", "MTEXT", "LWPOLYLINE", "VERTEX_2D", "VERTEX_3D",
		"VERTEX_MESH", "VERTEX_PFACE", "VERTEX_PFACE_FACE",
		"POLYLINE_2D", "POLYLINE_3D", "SEQEND", "BLOCK", "ENDBLK",
		"SPLINE", "LEADER", "3DFACE", "SOLID", "RAY", "XLINE", "MLINE",
		"TOLERANCE", "POLYLINE_PFACE", "POLYLINE_MESH", "SHAPE", "VIEWPORT",
		"REGION", "3DSOLID", "BODY",
		"OLEFRAME", "OLE2FRAME", "PROXY_ENTITY",
		"DIM_ORDINATE", "DIM_LINEAR", "DIM_ALIGNED", "DIM_ANG3PT", "DIM_ANG2LN",
		"DIM_RADIUS", "DIM_DIAMETER":
		return true
	}
	if name, ok := dynamic[code]; ok {
		switch name {
		case "ACDBLINE", "ACDBCIRCLE", "ACDBARC", "ACDBPOINT", "ACDBELLIPSE",
			"ACDBMTEXT", "ACDBTEXT", "ACDBLWPOLYLINE", "ACDBINSERT", "ACDBATTRIB",
			"ACDBPOLYLINE", "ACDBSPLINE", "ACDBHATCH", "ACDBDIMENSION",
			"HATCH",                     // R13/R14 类段中 HATCH 以本名注册（type 536/539）
			"REGION", "3DSOLID", "BODY", // 注入动态表的固定码实体（0x25/26/27）
			// 实体类（gold 确认身份；专有几何暂未实现，走通用实体头）
			"ACDBWIPEOUT", "WIPEOUT", "IMAGE", "ACDBRASTERIMAGE",
			"LWPOLYLINE", // R14 类段以本名注册（type 535；R2000+ 为固定码 0x0F）
			"MPOLYGON", "ACDBMPOLYGON",
			"ACDBTABLE", "ACAD_TABLE",
			"ACDBARCALIGNEDTEXT", "ARC_DIMENSION", "MULTILEADER",
			"LARGE_RADIAL_DIMENSION", // R2000+ 大半径标注（DIMENSION 同框架解码）
			"ACDBMLINESTYLE",         /*占位无*/
			// 螺旋线（dwgread 亦仅记录 unknown_bits，无内嵌几何）
			"HELIX",
			// 底图引用（几何在外部 PDF/DGN/DWF 文件）
			"PDFUNDERLAY", "DGNUNDERLAY", "DWFUNDERLAY":
			return true
		case "LIGHT", "ACDBLIGHT":
			return true
		}
	}
	return false
}
