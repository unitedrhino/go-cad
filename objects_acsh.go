// 本文件实现 ACSH 形体系内部对象（dwg2.spec ACSH_*_CLASS）的解码：
// 公共前导 AcDbEvalExpr_fields + AcDbShHistoryNode_fields（evalexpr/
// history_node 模式，与 ASSOC 求值图族同源），后接各 primitive 专有
// 字段；ACSH_BREP_CLASS 额外内嵌 ACTION_3DSOLID（DECODE_3DSOLID +
// COMMON_3DSOLID）。handle 流顺序 = handle91（value_code=91 时）+
// history_node.material（+ BREP 的 materials/history_id）。

package cad

import (
	"bytes"
	"fmt"
)

// decodeAcisEvalExprFields 读 AcDbEvalExpr_fields（dwg2.spec 公共宏，
// 二进制序）：parentid BLd + major/minor BL + value_code BSd（有符号）
// + value（按 value_code 的 union，-9999 无）+ nodeid BL。
// 返回 value_code==91（handle 值）时 handle 流是否多占一个引用。
func decodeAcisEvalExprFields(r *bitStream, ver dwgVersion, fr *gfRead, g *objGeneric) (bool, error) {
	if err := fr.BLd("evalexpr.parentid", g); err != nil {
		return false, err
	}
	if err := fr.BL("evalexpr.major", g); err != nil {
		return false, err
	}
	if err := fr.BL("evalexpr.minor", g); err != nil {
		return false, err
	}
	raw, err := r.readBS()
	if err != nil {
		return false, err
	}
	code := int64(int16(raw))
	g.Fields = append(g.Fields, objField{"evalexpr.value_code", code})
	hasHandle := false
	switch code {
	case 40: // BD 数值
		if err := fr.BD("evalexpr.value.num40", g); err != nil {
			return false, err
		}
	case 10, 11: // 2D/3D 点（spec 二进制均读 2RD）
		if err := fr.RD2("evalexpr.value.pt", g); err != nil {
			return false, err
		}
	case 1: // 文本
		if err := fr.T("evalexpr.value.text1", g); err != nil {
			return false, err
		}
	case 90: // 长整数
		if err := fr.BL("evalexpr.value.long90", g); err != nil {
			return false, err
		}
	case 91: // 句柄（handle 流占位）
		hasHandle = true
	case 70: // 短整数
		if err := fr.BS("evalexpr.value.short70", g); err != nil {
			return false, err
		}
	}
	return hasHandle, fr.BL("evalexpr.nodeid", g)
}

// decodeAcisShHistoryNodeFields 读 AcDbShHistoryNode_fields：major/minor
// BL + trans 16×BD + color CMC + step_id BL；material 句柄在 handle 流。
func decodeAcisShHistoryNodeFields(r *bitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := fr.BL("history_node.major", g); err != nil {
		return err
	}
	if err := fr.BL("history_node.minor", g); err != nil {
		return err
	}
	trans := make([]float64, 16)
	for i := 0; i < 16; i++ {
		v, e := r.readBD()
		if e != nil {
			return e
		}
		trans[i] = v
	}
	g.Fields = append(g.Fields, objField{"history_node.trans", trans})
	if err := fr.CMC("history_node.color", g); err != nil {
		return err
	}
	return fr.BL("history_node.step_id", g)
}

// acshWithCommon 为 ACSH primitive 解码器包上公共前导
// （AcDbEvalExpr_fields + AcDbShHistoryNode_fields），并记录
// value_code==91 的 handle 流占位供 hdl 阶段使用。
func acshWithCommon(d func(*bitStream, dwgVersion, *gfRead, *objGeneric) error) func(*bitStream, dwgVersion, *gfRead, *objGeneric) error {
	return func(r *bitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
		hasHandle, err := decodeAcisEvalExprFields(r, ver, fr, g)
		if err != nil {
			return err
		}
		g.valueHandle91 = hasHandle
		if err := decodeAcisShHistoryNodeFields(r, ver, fr, g); err != nil {
			return err
		}
		return d(r, ver, fr, g)
	}
}

// acisPrimitiveBody 读类版本 major/minor（各 primitive 公共尾导）。
func acisPrimitiveBody(r *bitStream, fr *gfRead, g *objGeneric) error {
	if err := fr.BL("major", g); err != nil {
		return err
	}
	return fr.BL("minor", g)
}

// readBDVector 读 count 个 BD 并以数组记录（edges/radiuses 等向量，
// 值级审计不对照数组键，仅消费位序）。
func readBDVector(r *bitStream, key string, count int, g *objGeneric) error {
	if count < 0 || count > 1_000_000 {
		return fmt.Errorf("cad: ACSH %s 数异常 %d", key, count)
	}
	out := make([]float64, 0, count)
	for i := 0; i < count; i++ {
		v, e := r.readBD()
		if e != nil {
			return e
		}
		out = append(out, v)
	}
	g.Fields = append(g.Fields, objField{key, out})
	return nil
}

// readBLVector 读 count 个 BL 并以数组记录。
func readBLVector(r *bitStream, key string, count int, g *objGeneric) error {
	if count < 0 || count > 1_000_000 {
		return fmt.Errorf("cad: ACSH %s 数异常 %d", key, count)
	}
	out := make([]int64, 0, count)
	for i := 0; i < count; i++ {
		v, e := r.readBL()
		if e != nil {
			return e
		}
		out = append(out, int64(v))
	}
	g.Fields = append(g.Fields, objField{key, out})
	return nil
}

// decodeGenericACSH_BOX / ACSH_WEDGE：major/minor + length/width/height。
func decodeGenericACSH_BOX(r *bitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := acisPrimitiveBody(r, fr, g); err != nil {
		return err
	}
	for _, k := range []string{"length", "width", "height"} {
		if err := fr.BD(k, g); err != nil {
			return err
		}
	}
	return nil
}

// decodeGenericACSH_SPHERE：major/minor + radius。
func decodeGenericACSH_SPHERE(r *bitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := acisPrimitiveBody(r, fr, g); err != nil {
		return err
	}
	return fr.BD("radius", g)
}

// decodeGenericACSH_TORUS：major/minor + major_radius/minor_radius。
func decodeGenericACSH_TORUS(r *bitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := acisPrimitiveBody(r, fr, g); err != nil {
		return err
	}
	if err := fr.BD("major_radius", g); err != nil {
		return err
	}
	return fr.BD("minor_radius", g)
}

// decodeGenericACSH_CYLINDER / ACSH_CONE：major/minor + height/
// major_radius/minor_radius/x_radius（Cone 与 Cylinder 同布局）。
func decodeGenericACSH_CYLINDER(r *bitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := acisPrimitiveBody(r, fr, g); err != nil {
		return err
	}
	for _, k := range []string{"height", "major_radius", "minor_radius", "x_radius"} {
		if err := fr.BD(k, g); err != nil {
			return err
		}
	}
	return nil
}

// decodeGenericACSH_PYRAMID：major/minor + height BD + sides BL +
// radius/topradius BD。
func decodeGenericACSH_PYRAMID(r *bitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := acisPrimitiveBody(r, fr, g); err != nil {
		return err
	}
	if err := fr.BD("height", g); err != nil {
		return err
	}
	if err := fr.BL("sides", g); err != nil {
		return err
	}
	if err := fr.BD("radius", g); err != nil {
		return err
	}
	return fr.BD("topradius", g)
}

// decodeGenericACSH_FILLET：major/minor/method BL + edges BL 向量 +
// radiuses BD 向量 + start/end setbacks BD 向量（spec 顺序：先读两个
// 计数再读 endsetbacks、startsetbacks 向量）。
func decodeGenericACSH_FILLET(r *bitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := acisPrimitiveBody(r, fr, g); err != nil {
		return err
	}
	if err := fr.BL("method", g); err != nil {
		return err
	}
	ne, err := fr.BLv("num_edges", g)
	if err != nil {
		return err
	}
	if err := readBLVector(r, "edges", int(ne), g); err != nil {
		return err
	}
	nr, err := fr.BLv("num_radiuses", g)
	if err != nil {
		return err
	}
	if err := readBDVector(r, "radiuses", int(nr), g); err != nil {
		return err
	}
	nss, err := fr.BLv("num_startsetbacks", g)
	if err != nil {
		return err
	}
	nes, err := fr.BLv("num_endsetbacks", g)
	if err != nil {
		return err
	}
	if err := readBDVector(r, "endsetbacks", int(nes), g); err != nil {
		return err
	}
	return readBDVector(r, "startsetbacks", int(nss), g)
}

// decodeGenericACSH_CHAMFER：major/minor/method BL + base_dist/
// other_dist BD + edges BL 向量 + base_face BL。
func decodeGenericACSH_CHAMFER(r *bitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := acisPrimitiveBody(r, fr, g); err != nil {
		return err
	}
	if err := fr.BL("method", g); err != nil {
		return err
	}
	if err := fr.BD("base_dist", g); err != nil {
		return err
	}
	if err := fr.BD("other_dist", g); err != nil {
		return err
	}
	ne, err := fr.BLv("num_edges", g)
	if err != nil {
		return err
	}
	if err := readBLVector(r, "edges", int(ne), g); err != nil {
		return err
	}
	return fr.BL("base_face", g)
}

// decodeGenericACSH_BOOLEAN：major/minor + operation RCd +
// operand1/operand2 BL。
func decodeGenericACSH_BOOLEAN(r *bitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := acisPrimitiveBody(r, fr, g); err != nil {
		return err
	}
	if err := fr.RC("operation", g); err != nil {
		return err
	}
	if err := fr.BL("operand1", g); err != nil {
		return err
	}
	return fr.BL("operand2", g)
}

// decodeGenericACSH_BREP：major/minor + ACTION_3DSOLID（DECODE_3DSOLID
// 的 acis 数据段 + COMMON_3DSOLID 线框段）。键名与 3DSOLID 实体一致。
func decodeGenericACSH_BREP(r *bitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := acisPrimitiveBody(r, fr, g); err != nil {
		return err
	}
	// DECODE_3DSOLID：acis_empty → [unknown + version + acis 数据段]
	acisEmpty, err := r.readB()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, objField{"acis_empty", b2int(acisEmpty != 0)})
	version := int64(0)
	if acisEmpty == 0 {
		unk, e := r.readB()
		if e != nil {
			return e
		}
		g.Fields = append(g.Fields, objField{"unknown", b2int(unk != 0)})
		v, e := r.readBS()
		if e != nil {
			return e
		}
		version = int64(v)
		g.Fields = append(g.Fields, objField{"version", version})
		switch version {
		case 1: // SAT：块循环（BL 长度 + 加密 TF 块）
			for i := 0; ; i++ {
				bl, e := r.readBL()
				if e != nil {
					return e
				}
				if bl == 0 || int64(bl) > int64(r.totalBits()-r.tellBits())/8 {
					break
				}
				raw := make([]byte, bl)
				for j := uint32(0); j < bl; j++ {
					if raw[j], e = r.readRC(); e != nil {
						return e
					}
				}
				_ = raw
				if i > 1_000_000 {
					return fmt.Errorf("cad: BREP SAT 块数异常")
				}
				if r.totalBits()-r.tellBits() < 16 {
					break
				}
			}
		case 2: // SAB：按位读出后搜 End-of-ACIS-data 标记
			startBit := r.tellBits()
			size := int((r.totalBits() - startBit) / 8)
			if size > 0 {
				size--
				if buf, e := r.readRCS(size); e == nil {
					end := -1
					for _, marker := range []string{
						"\x0e\x03End\x0e\x02of\x0e\x04ACIS\r\x04data",
						"\x0e\x03End\x0e\x02of\x0e\x03ASM\r\x04data"} {
						if i := bytes.Index(buf, []byte(marker)); i >= 0 {
							end = i + len(marker)
							break
						}
					}
					if end < 0 {
						end = size
					}
					r.setBitPos(startBit + uint64(end)*8)
				}
			}
		}
	} else {
		g.Fields = append(g.Fields, objField{"unknown", int64(0)})
		g.Fields = append(g.Fields, objField{"version", int64(0)})
	}
	// COMMON_3DSOLID：wireframe_data_present → …（同 3DSOLID 实体，仅消费位）
	wire, err := r.readB()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, objField{"wireframe_data_present", b2int(wire != 0)})
	if wire != 0 {
		pp, e := r.readB()
		if e != nil {
			return e
		}
		g.Fields = append(g.Fields, objField{"point_present", b2int(pp != 0)})
		if pp != 0 {
			if _, _, _, e := r.read3BD(); e != nil {
				return e
			}
		}
		if _, e = r.readBL(); e != nil { // isolines
			return e
		}
		ip, e := r.readB()
		if e != nil {
			return e
		}
		g.Fields = append(g.Fields, objField{"isoline_present", b2int(ip != 0)})
		if ip != 0 {
			nw, e := r.readBL()
			if e != nil {
				return e
			}
			for i := uint32(0); i < nw && i < 1_000_000; i++ {
				if e := skipAcisWireBits(r); e != nil {
					return e
				}
			}
			ns, e := r.readBL()
			if e != nil {
				return e
			}
			for i := uint32(0); i < ns && i < 1_000_000; i++ {
				if _, e = r.readBL(); e != nil { // vp_id
					return e
				}
				for k := 0; k < 3; k++ {
					if _, _, _, e = r.read3BD(); e != nil {
						return e
					}
				}
				per, e := r.readB()
				if e != nil {
					return e
				}
				hw, e := r.readB()
				if e != nil {
					return e
				}
				if hw != 0 {
					n2, e := r.readBL()
					if e != nil {
						return e
					}
					for j := uint32(0); j < n2 && j < 1_000_000; j++ {
						if e := skipAcisWireBits(r); e != nil {
							return e
						}
					}
				}
				_ = per
			}
		}
	}
	aeb, err := r.readB()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, objField{"acis_empty_bit", b2int(aeb != 0)})
	// version>1 且 R2007+：num_materials（materials 数组，句柄在 handle 流）
	if version > 1 && ver >= verR2007 {
		return fr.BL("num_materials", g)
	}
	return nil
}

// skipAcisWireBits 按 WIRESTRUCT_fields 消费单条线框位（不存值，
// BREP 对象侧样本无 wireframe 数据，仅保证结构通读）。
func skipAcisWireBits(r *bitStream) error {
	if _, err := r.readRC(); err != nil { // type
		return err
	}
	if _, err := r.readBL(); err != nil { // selection_marker
		return err
	}
	if _, err := r.readBS(); err != nil { // color
		return err
	}
	if _, err := r.readBL(); err != nil { // acis_index
		return err
	}
	np, err := r.readBL()
	if err != nil {
		return err
	}
	if np > 1_000_000 {
		return fmt.Errorf("cad: BREP 线框点数异常 %d", np)
	}
	for i := uint32(0); i < np; i++ {
		if _, _, _, err := r.read3BD(); err != nil {
			return err
		}
	}
	tp, err := r.readB()
	if err != nil {
		return err
	}
	if tp != 0 {
		for k := 0; k < 5; k++ {
			if _, _, _, err := r.read3BD(); err != nil {
				return err
			}
		}
		for k := 0; k < 3; k++ {
			if _, err := r.readB(); err != nil {
				return err
			}
		}
	}
	return nil
}

// decodeGenericACSH_HDL ACSH 形体系的 handle 流（owner/reactors/xdic
// 之后）：evalexpr.value.handle91（value_code=91 时）+
// history_node.material；BREP 再加 materials×N（version>1 且 R2007+）
// 与 history_id（version>1，宽容读取）。
func decodeGenericACSH_HDL(r *bitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if g.valueHandle91 {
		h, e := readHandleReference(r, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	h, e := readHandleReference(r, g.Handle) // history_node.material
	if e != nil {
		return e
	}
	g.Handles = append(g.Handles, h)
	if g.Name == "ACSH_BREP_CLASS" {
		version, _ := g.Field("version").(int64)
		if version > 1 && ver >= verR2007 {
			if nm, _ := g.Field("num_materials").(int64); nm > 0 && nm <= 1_000_000 {
				for i := int64(0); i < nm; i++ {
					h, e := readHandleReference(r, g.Handle)
					if e != nil {
						return e
					}
					g.Handles = append(g.Handles, h)
				}
			}
		}
		if version > 1 {
			// history_id：spec 解码器分支带 AVAIL_BITS 防御，错位即放弃
			if h, e := readHandleReference(r, g.Handle); e == nil {
				g.Handles = append(g.Handles, h)
			}
		}
	}
	return nil
}

// acshDecoders ACSH 形体系注册表（Box/Wedge/Sphere 同布局共用，
// 全部包公共前导）。
func acshDecoders() map[string]internalObjectSpec {
	spec := func(d func(*bitStream, dwgVersion, *gfRead, *objGeneric) error) internalObjectSpec {
		return internalObjectSpec{decode: acshWithCommon(d), hdl: decodeGenericACSH_HDL}
	}
	return map[string]internalObjectSpec{
		"ACSH_BOX_CLASS":      spec(decodeGenericACSH_BOX),
		"ACSH_WEDGE_CLASS":    spec(decodeGenericACSH_BOX),
		"ACSH_SPHERE_CLASS":   spec(decodeGenericACSH_SPHERE),
		"ACSH_TORUS_CLASS":    spec(decodeGenericACSH_TORUS),
		"ACSH_CYLINDER_CLASS": spec(decodeGenericACSH_CYLINDER),
		"ACSH_CONE_CLASS":     spec(decodeGenericACSH_CYLINDER),
		"ACSH_PYRAMID_CLASS":  spec(decodeGenericACSH_PYRAMID),
		"ACSH_FILLET_CLASS":   spec(decodeGenericACSH_FILLET),
		"ACSH_CHAMFER_CLASS":  spec(decodeGenericACSH_CHAMFER),
		"ACSH_BOOLEAN_CLASS":  spec(decodeGenericACSH_BOOLEAN),
		"ACSH_BREP_CLASS":     spec(decodeGenericACSH_BREP),
	}
}
