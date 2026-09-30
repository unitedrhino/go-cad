// 本文件实现动态块族内部对象（dwg2.spec）：AcDbBlockElement（基于
// AcDbEvalExpr）、BlockGrip/BlockParameter/BlockAction/Block2Pt/1Pt
// 参数、ParamValueSet 等公共体，及 BLOCK*GRIP、BLOCK*PARAMETER、
// BLOCK*ACTION、BLOCKREPRESENTATION、DYNAMICBLOCKPURGEPREVENTER。
// 注意 gold JSON（out_json _path_field）会把 "xxx[i].field" 键剥为
// "field"（同名重复键后写覆盖），连接点的 code/name 即此口径。

package object

import (
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/objrec"
)

// setFieldTop 按 gold JSON 的重复键覆盖语义写字段：同名键已存在时
// 覆盖（保持位置不变）。
func (g *ObjGeneric) setFieldTop(f ObjField) {
	for i := len(g.Fields) - 1; i >= 0; i-- {
		if g.Fields[i].Key == f.Key {
			g.Fields[i] = f
			return
		}
	}
	g.Fields = append(g.Fields, f)
}

// Point3RD 读三个无条件 BD（FIELD_3BD_1 语义：无 BB 压缩前缀）。
func (f *GfRead) Point3RD(key string, g *ObjGeneric) error {
	x, err := f.R.ReadRD()
	if err != nil {
		return err
	}
	y, err := f.R.ReadRD()
	if err != nil {
		return err
	}
	z, err := f.R.ReadRD()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, ObjField{key, []float64{x, y, z}})
	return nil
}

// readBlockConnectionPts 读 BlockAction_ConnectionPts 向量：code/name
// 均写顶层覆盖键（gold JSON 重复键后写覆盖，最终 name=最后一个连接点
// 的 name、code=最后一个连接点的 code）。
func readBlockConnectionPts(R *bitstream.BitStream, fr *GfRead, g *ObjGeneric, n int) error {
	for i := 0; i < n; i++ {
		cv, err := R.ReadBL()
		if err != nil {
			return err
		}
		g.setFieldTop(ObjField{"code", int64(cv)})
		if err := fr.T("__conn_pts_name", g); err != nil {
			return err
		}
		val := g.Fields[len(g.Fields)-1].Val
		g.Fields = g.Fields[:len(g.Fields)-1]
		g.setFieldTop(ObjField{"name", val})
	}
	return nil
}

// decodeBlockElementFields AcDbBlockElement_fields：AcDbEvalExpr
// （parentid/major/minor/value_code/value/nodeid）+ name T + be_major/
// be_minor BL（DECODER-only，不产生 JSON 键）+ eed1071 BL。
func decodeBlockElementFields(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	hasHandle, err := decodeAcisEvalExprFields(R, Ver, fr, g)
	if err != nil {
		return err
	}
	g.ValueHandle91 = hasHandle
	if err := fr.T("name", g); err != nil {
		return err
	}
	for _, k := range []string{"be_major", "be_minor"} {
		if err := fr.BL(k, g); err != nil {
			return err
		}
	}
	return fr.BL("eed1071", g)
}

// decodeBlockGripFields AcDbBlockGrip_fields：BlockElement + bg_bl91/
// bg_bl92 BL + bg_location 3BD + bg_insert_cycling B +
// bg_insert_cycling_weight BLd。
func decodeBlockGripFields(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := decodeBlockElementFields(R, Ver, fr, g); err != nil {
		return err
	}
	if err := fr.BL("bg_bl91", g); err != nil {
		return err
	}
	if err := fr.BL("bg_bl92", g); err != nil {
		return err
	}
	if err := fr.Point3("bg_location", g); err != nil {
		return err
	}
	if err := fr.B("bg_insert_cycling", g); err != nil {
		return err
	}
	return fr.BLd("bg_insert_cycling_weight", g)
}

// decodeBlockParameterFields AcDbBlockParameter_fields：BlockElement +
// show_properties/chain_actions B。
func decodeBlockParameterFields(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := decodeBlockElementFields(R, Ver, fr, g); err != nil {
		return err
	}
	if err := fr.B("show_properties", g); err != nil {
		return err
	}
	return fr.B("chain_actions", g)
}

// decodeBlockActionFields AcDbBlockAction_fields（二进制序）：
// BlockElement + display_location 3BD + num_deps BL（deps 句柄在
// handle 流）+ num_actions BL + actions BL 向量。
func decodeBlockActionFields(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) (numDeps int, err error) {
	if err = decodeBlockElementFields(R, Ver, fr, g); err != nil {
		return
	}
	if err = fr.Point3("display_location", g); err != nil {
		return
	}
	nd, e := fr.BLv("num_deps", g)
	if e != nil {
		return 0, e
	}
	if nd < 0 || nd > 1_000_000 {
		return 0, fmt.Errorf("cad: BLOCKACTION deps 数异常 %d", nd)
	}
	na, err := fr.BLv("num_actions", g)
	if err != nil {
		return 0, err
	}
	if na < 0 || na > 1_000_000 {
		return 0, fmt.Errorf("cad: BLOCKACTION actions 数异常 %d", na)
	}
	acts := make([]int64, 0, na)
	for i := 0; i < int(na); i++ {
		av, e := R.ReadBL()
		if e != nil {
			return 0, e
		}
		acts = append(acts, int64(av))
	}
	g.Fields = append(g.Fields, ObjField{"actions", acts})
	return int(nd), nil
}

// readBlockPropInfo 读 BlockParam_PropInfo（前缀如 "prop1"）：
// num_connections BL + connections×N（code BL + name T，元素内键）。
func readBlockPropInfo(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric, prefix string) error {
	nc, err := fr.BLv(prefix+".num_connections", g)
	if err != nil {
		return err
	}
	if nc < 0 || nc > 1_000_000 {
		return fmt.Errorf("cad: BLOCKPARAMETER connections 数异常 %d", nc)
	}
	for j := 0; j < int(nc); j++ {
		cv, e := R.ReadBL()
		if e != nil {
			return e
		}
		g.Fields = append(g.Fields, ObjField{prefix + ".connections[" + itoa(j) + "].code", int64(cv)})
		if err := fr.T(prefix+".connections["+itoa(j)+"].name", g); err != nil {
			return err
		}
	}
	return nil
}

// decodeBlock2PtParameterFields AcDbBlock2PtParameter_fields：参数 +
// def_basept/def_endpt 3BD + prop1..4 PropInfo + prop_states BL×4 +
// parameter_base_location BS。
func decodeBlock2PtParameterFields(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := decodeBlockParameterFields(R, Ver, fr, g); err != nil {
		return err
	}
	if err := fr.Point3("def_basept", g); err != nil {
		return err
	}
	if err := fr.Point3("def_endpt", g); err != nil {
		return err
	}
	for _, p := range []string{"prop1", "prop2", "prop3", "prop4"} {
		if err := readBlockPropInfo(R, Ver, fr, g, p); err != nil {
			return err
		}
	}
	states := make([]int64, 4)
	for i := 0; i < 4; i++ {
		v, e := R.ReadBL()
		if e != nil {
			return e
		}
		states[i] = int64(v)
	}
	g.Fields = append(g.Fields, ObjField{"prop_states", states})
	return fr.BS("parameter_base_location", g)
}

// decodeBlock1PtParameterFields AcDbBlock1PtParameter_fields：参数 +
// def_pt 3BD + prop1/prop2 PropInfo + num_propinfos BL（尾部）。
func decodeBlock1PtParameterFields(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := decodeBlockParameterFields(R, Ver, fr, g); err != nil {
		return err
	}
	if err := fr.Point3("def_pt", g); err != nil {
		return err
	}
	for _, p := range []string{"prop1", "prop2"} {
		if err := readBlockPropInfo(R, Ver, fr, g, p); err != nil {
			return err
		}
	}
	_, err := fr.BLv("num_propinfos", g)
	return err
}

// decodeBlockParamValueSet AcDbBlockParamValueSet_fields：desc T +
// flags BL + minimum/maximum/increment BD + num_valuelist BS +
// valuelist BD 向量（gold 无 num_valuelist 键，仅消费位）。
func decodeBlockParamValueSet(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := fr.T("desc", g); err != nil {
		return err
	}
	if err := fr.BL("flags", g); err != nil {
		return err
	}
	if err := fr.BD("minimum", g); err != nil {
		return err
	}
	if err := fr.BD("maximum", g); err != nil {
		return err
	}
	if err := fr.BD("increment", g); err != nil {
		return err
	}
	nv, err := R.ReadBS()
	if err != nil {
		return err
	}
	if nv > 1000 {
		return fmt.Errorf("cad: ParamValueSet valuelist 数异常 %d", nv)
	}
	list := make([]float64, 0, nv)
	for i := uint16(0); i < nv; i++ {
		v, e := R.ReadBD()
		if e != nil {
			return e
		}
		list = append(list, v)
	}
	g.Fields = append(g.Fields, ObjField{"valuelist", list})
	return nil
}

// decodeBlockActionWithBasePtFields AcDbBlockActionWithBasePt_fields：
// BlockAction + offset 3BD + ConnectionPts×2 + dependent B + base_pt 3BD。
func decodeBlockActionWithBasePtFields(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) (int, error) {
	nd, err := decodeBlockActionFields(R, Ver, fr, g)
	if err != nil {
		return 0, err
	}
	if err := fr.Point3("offset", g); err != nil {
		return 0, err
	}
	if err := readBlockConnectionPts(R, fr, g, 2); err != nil {
		return 0, err
	}
	if err := fr.B("dependent", g); err != nil {
		return 0, err
	}
	if err := fr.Point3("base_pt", g); err != nil {
		return 0, err
	}
	return nd, nil
}

// decodeBlockActionDoublesFields AcDbBlockAction_doubles_fields：
// action_offset_x/action_offset_y/angle_offset BD。
func decodeBlockActionDoublesFields(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	for _, k := range []string{"action_offset_x", "action_offset_y", "angle_offset"} {
		if err := fr.BD(k, g); err != nil {
			return err
		}
	}
	return nil
}

// ---- 各具体对象 ----

// decodeGenericBLOCKVISIBILITYGRIP 等纯 Grip 类（LOOKUP/ROTATION/XY
// 同布局无附加字段）。
func decodeGenericBLOCKVISIBILITYGRIP(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	return decodeBlockGripFields(R, Ver, fr, g)
}

// decodeGenericBLOCKORIENTATIONGRIP 带 orientation 3BD_1 的 Grip
// （ALIGNMENT/LINEAR）。
func decodeGenericBLOCKORIENTATIONGRIP(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := decodeBlockGripFields(R, Ver, fr, g); err != nil {
		return err
	}
	return fr.Point3RD("orientation", g)
}

// decodeGenericBLOCKFLIPGRIP BLOCKFLIPGRIP：Grip + combined_state BL +
// orientation 3BD_1。
func decodeGenericBLOCKFLIPGRIP(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := decodeBlockGripFields(R, Ver, fr, g); err != nil {
		return err
	}
	if err := fr.BL("combined_state", g); err != nil {
		return err
	}
	return fr.Point3RD("orientation", g)
}

// decodeGenericBLOCKGRIPLOCATIONCOMPONENT BLOCKGRIPLOCATIONCOMPONENT：
// AcDbEvalExpr + AcDbBlockGripExpr（grip_type BL + grip_expr T）。
func decodeGenericBLOCKGRIPLOCATIONCOMPONENT(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	hasHandle, err := decodeAcisEvalExprFields(R, Ver, fr, g)
	if err != nil {
		return err
	}
	g.ValueHandle91 = hasHandle
	if err := fr.BL("grip_type", g); err != nil {
		return err
	}
	return fr.T("grip_expr", g)
}

// decodeGenericBLOCKALIGNMENTPARAMETER：2Pt 参数 + align_perpendicular B。
func decodeGenericBLOCKALIGNMENTPARAMETER(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := decodeBlock2PtParameterFields(R, Ver, fr, g); err != nil {
		return err
	}
	return fr.B("align_perpendicular", g)
}

// decodeGenericBLOCKLINEARPARAMETER：2Pt 参数 + distance_name/
// distance_desc T + distance BD + ParamValueSet。
func decodeGenericBLOCKLINEARPARAMETER(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := decodeBlock2PtParameterFields(R, Ver, fr, g); err != nil {
		return err
	}
	if err := fr.T("distance_name", g); err != nil {
		return err
	}
	if err := fr.T("distance_desc", g); err != nil {
		return err
	}
	if err := fr.BD("distance", g); err != nil {
		return err
	}
	return decodeBlockParamValueSet(R, Ver, fr, g)
}

// decodeGenericBLOCKFLIPPARAMETER：2Pt 参数 + 4 个状态标签 T +
// def_label_pt 3BD + bl96 BL + tooltip T。
func decodeGenericBLOCKFLIPPARAMETER(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := decodeBlock2PtParameterFields(R, Ver, fr, g); err != nil {
		return err
	}
	for _, k := range []string{"flip_label", "flip_label_desc", "base_state_label", "flipped_state_label"} {
		if err := fr.T(k, g); err != nil {
			return err
		}
	}
	if err := fr.Point3("def_label_pt", g); err != nil {
		return err
	}
	if err := fr.BL("bl96", g); err != nil {
		return err
	}
	return fr.T("tooltip", g)
}

// decodeGenericBLOCKROTATIONPARAMETER：2Pt 参数 + def_base_angle_pt
// 3BD + angle_name/angle_desc T + angle BD + ParamValueSet。
func decodeGenericBLOCKROTATIONPARAMETER(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := decodeBlock2PtParameterFields(R, Ver, fr, g); err != nil {
		return err
	}
	if err := fr.Point3("def_base_angle_pt", g); err != nil {
		return err
	}
	if err := fr.T("angle_name", g); err != nil {
		return err
	}
	if err := fr.T("angle_desc", g); err != nil {
		return err
	}
	if err := fr.BD("angle", g); err != nil {
		return err
	}
	return decodeBlockParamValueSet(R, Ver, fr, g)
}

// decodeGenericBLOCKPOLARPARAMETER：2Pt 参数 + 4 个名称/描述 T +
// offset BD + angle/distance 两个 ParamValueSet。
func decodeGenericBLOCKPOLARPARAMETER(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := decodeBlock2PtParameterFields(R, Ver, fr, g); err != nil {
		return err
	}
	for _, k := range []string{"angle_name", "angle_desc", "distance_name", "distance_desc"} {
		if err := fr.T(k, g); err != nil {
			return err
		}
	}
	if err := fr.BD("offset", g); err != nil {
		return err
	}
	if err := decodeBlockParamValueSet(R, Ver, fr, g); err != nil {
		return err
	}
	return decodeBlockParamValueSet(R, Ver, fr, g)
}

// decodeGenericBLOCKBASEPOINTPARAMETER：1Pt 参数 + pt/base_pt 3BD。
func decodeGenericBLOCKBASEPOINTPARAMETER(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := decodeBlock1PtParameterFields(R, Ver, fr, g); err != nil {
		return err
	}
	if err := fr.Point3("pt", g); err != nil {
		return err
	}
	return fr.Point3("base_pt", g)
}

// decodeGenericBLOCKPOINTPARAMETER：1Pt 参数 + position_name/
// position_desc T + def_label_pt 3BD。
func decodeGenericBLOCKPOINTPARAMETER(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := decodeBlock1PtParameterFields(R, Ver, fr, g); err != nil {
		return err
	}
	if err := fr.T("position_name", g); err != nil {
		return err
	}
	if err := fr.T("position_desc", g); err != nil {
		return err
	}
	return fr.Point3("def_label_pt", g)
}

// decodeGenericBLOCKXYPARAMETER：2Pt 参数 + 4 个标签 T + x_value/
// y_value BD + x/y 两个 ParamValueSet（二进制序 x 先）。
func decodeGenericBLOCKXYPARAMETER(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := decodeBlock2PtParameterFields(R, Ver, fr, g); err != nil {
		return err
	}
	for _, k := range []string{"x_label", "x_label_desc", "y_label", "y_label_desc"} {
		if err := fr.T(k, g); err != nil {
			return err
		}
	}
	if err := fr.BD("x_value", g); err != nil {
		return err
	}
	if err := fr.BD("y_value", g); err != nil {
		return err
	}
	if err := decodeBlockParamValueSet(R, Ver, fr, g); err != nil {
		return err
	}
	return decodeBlockParamValueSet(R, Ver, fr, g)
}

// decodeGenericBLOCKLOOKUPPARAMETER：1Pt 参数 + index BL + lookup_name/
// lookup_desc T + unknown_t T。
func decodeGenericBLOCKLOOKUPPARAMETER(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := decodeBlock1PtParameterFields(R, Ver, fr, g); err != nil {
		return err
	}
	if err := fr.BL("index", g); err != nil {
		return err
	}
	if err := fr.T("lookup_name", g); err != nil {
		return err
	}
	if err := fr.T("lookup_desc", g); err != nil {
		return err
	}
	return fr.T("unknown_t", g)
}

// decodeGenericBLOCKUSERPARAMETER：1Pt 参数 + flag BS + expr T +
// AcDbEvalVariant + type BS；assocvariable/value.handle 句柄在 handle 流。
func decodeGenericBLOCKUSERPARAMETER(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := decodeBlock1PtParameterFields(R, Ver, fr, g); err != nil {
		return err
	}
	if err := fr.BS("flag", g); err != nil {
		return err
	}
	isHandle, err := decodeEvalVariantFields(R, Ver, fr, g, "value.")
	if err != nil {
		return err
	}
	g.ValueHandle91 = isHandle
	return fr.BS("type", g)
}

// decodeGenericBLOCKVISIBILITYPARAMETER：1Pt 参数 + is_initialized B +
// blockvisi_name/blockvisi_desc T + unknown_bool B + num_blocks BL
// （blocks 句柄向量）+ num_states BL + states×N（name T + num_blocks
// BL + blocks 句柄 + num_params BL + params 句柄）。
func decodeGenericBLOCKVISIBILITYPARAMETER(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := decodeBlock1PtParameterFields(R, Ver, fr, g); err != nil {
		return err
	}
	if err := fr.B("is_initialized", g); err != nil {
		return err
	}
	if err := fr.T("blockvisi_name", g); err != nil {
		return err
	}
	if err := fr.T("blockvisi_desc", g); err != nil {
		return err
	}
	if err := fr.B("unknown_bool", g); err != nil {
		return err
	}
	nb, err := fr.BLv("num_blocks", g)
	if err != nil {
		return err
	}
	if nb < 0 || nb > 1_000_000 {
		return fmt.Errorf("cad: BLOCKVISIBILITYPARAMETER blocks 数异常 %d", nb)
	}
	ns, err := fr.BLv("num_states", g)
	if err != nil {
		return err
	}
	if ns < 0 || ns > 1_000_000 {
		return fmt.Errorf("cad: BLOCKVISIBILITYPARAMETER states 数异常 %d", ns)
	}
	// handle 流引用数：顶层 blocks + 各 state 的 blocks/params
	HdlCount := int(nb)
	for i := 0; i < int(ns); i++ {
		if err := fr.T(fmt.Sprintf("states[%d].name", i), g); err != nil {
			return err
		}
		sb, err := fr.BLv(fmt.Sprintf("states[%d].num_blocks", i), g)
		if err != nil {
			return err
		}
		if sb < 0 || sb > 1_000_000 {
			return fmt.Errorf("cad: BLOCKVISIBILITYPARAMETER state blocks 数异常 %d", sb)
		}
		HdlCount += int(sb)
		sp, err := fr.BLv(fmt.Sprintf("states[%d].num_params", i), g)
		if err != nil {
			return err
		}
		if sp < 0 || sp > 1_000_000 {
			return fmt.Errorf("cad: BLOCKVISIBILITYPARAMETER state params 数异常 %d", sp)
		}
		HdlCount += int(sp)
	}
	g.HdlCount = HdlCount
	return nil
}

// decodeGenericBLOCKVISIBILITYPARAMETER_HDL handle 流：handle91 + 顶层
// blocks 与 states 的 blocks/params 句柄（按 hdlCount 累计数）。
func decodeGenericBLOCKVISIBILITYPARAMETER_HDL(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if g.ValueHandle91 {
		if _, e := objrec.ReadHandleReference(R, g.Handle); e != nil {
			return e
		}
	}
	for i := 0; i < g.HdlCount; i++ {
		h, e := objrec.ReadHandleReference(R, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// makeBlockAction 生成 BLOCK*ACTION 解码器（deps 句柄数经 hdlCount
// 传递给 handle 流）。
func makeBlockAction(body func(*bitstream.BitStream, container.DwgVersion, *GfRead, *ObjGeneric) error) func(*bitstream.BitStream, container.DwgVersion, *GfRead, *ObjGeneric) error {
	return func(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
		nd, err := decodeBlockActionFields(R, Ver, fr, g)
		if err != nil {
			return err
		}
		g.HdlCount = nd
		return body(R, Ver, fr, g)
	}
}

// decodeGenericBLOCKMOVEACTION：Action + ConnectionPt×2 + doubles。
func decodeGenericBLOCKMOVEACTION(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := readBlockConnectionPts(R, fr, g, 2); err != nil {
		return err
	}
	return decodeBlockActionDoublesFields(R, Ver, fr, g)
}

// decodeGenericBLOCKFLIPACTION：Action + ConnectionPts×4。
func decodeGenericBLOCKFLIPACTION(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	return readBlockConnectionPts(R, fr, g, 4)
}

// decodeGenericBLOCKARRAYACTION：Action + ConnectionPts×4 +
// column_offset/row_offset BD。
func decodeGenericBLOCKARRAYACTION(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := readBlockConnectionPts(R, fr, g, 4); err != nil {
		return err
	}
	if err := fr.BD("column_offset", g); err != nil {
		return err
	}
	return fr.BD("row_offset", g)
}

// decodeGenericBLOCKROTATEACTION：ActionWithBasePt（自含 BlockAction
// 公共头）+ ConnectionPt（第 3 个，spec conn_pts[2] 起点 1 个）。
// hdlCount 在此设置：WithBasePt 族注册不经 makeBlockAction（否则公共头
// 被读两遍，dat 流错位）。
func decodeGenericBLOCKROTATEACTION(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	nd, err := decodeBlockActionWithBasePtFields(R, Ver, fr, g)
	if err != nil {
		return err
	}
	g.HdlCount = nd
	return readBlockConnectionPts(R, fr, g, 1)
}

// decodeGenericBLOCKSCALEACTION：ActionWithBasePt（自含公共头）+
// ConnectionPts×3。
func decodeGenericBLOCKSCALEACTION(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	nd, err := decodeBlockActionWithBasePtFields(R, Ver, fr, g)
	if err != nil {
		return err
	}
	g.HdlCount = nd
	return readBlockConnectionPts(R, fr, g, 3)
}

// decodeGenericBLOCKSTRETCHACTION：Action + ConnectionPt×2 + num_pts
// BL + pts 2RD 向量 + num_hdls BL + hdls（hdl 句柄 + num_indexes BS +
// indexes BL 向量）+ num_codes BL + codes（bl95 BL + num_indexes BS +
// indexes BL 向量）+ doubles。hdls 句柄占 handle 流。
func decodeGenericBLOCKSTRETCHACTION(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := readBlockConnectionPts(R, fr, g, 2); err != nil {
		return err
	}
	npts, err := fr.BLv("num_pts", g)
	if err != nil {
		return err
	}
	if npts > 1_000_000 {
		return fmt.Errorf("cad: BLOCKSTRETCHACTION pts 数异常 %d", npts)
	}
	pts := make([][]float64, 0, npts)
	for i := 0; i < int(npts); i++ {
		x, e1 := R.ReadRD()
		if e1 != nil {
			return e1
		}
		y, e2 := R.ReadRD()
		if e2 != nil {
			return e2
		}
		pts = append(pts, []float64{x, y})
	}
	g.Fields = append(g.Fields, ObjField{"pts", pts})
	nh, err := fr.BLv("num_hdls", g)
	if err != nil {
		return err
	}
	if nh > 1_000_000 {
		return fmt.Errorf("cad: BLOCKSTRETCHACTION hdls 数异常 %d", nh)
	}
	for i := 0; i < int(nh); i++ {
		// hdl 句柄在 handle 流，此处仅记录序号位（ Consumption 顺序：
		// 实际 hdl 位在 handle 流，dat 流只有 num_indexes + indexes）
		ni, e := R.ReadBS()
		if e != nil {
			return e
		}
		if ni > 1000 {
			return fmt.Errorf("cad: BLOCKSTRETCHACTION indexes 数异常 %d", ni)
		}
		indexes := make([]int64, 0, ni)
		for j := uint16(0); j < ni; j++ {
			iv, e := R.ReadBL()
			if e != nil {
				return e
			}
			indexes = append(indexes, int64(iv))
		}
		g.Fields = append(g.Fields, ObjField{fmt.Sprintf("hdls[%d].indexes", i), indexes})
	}
	nc, err := fr.BLv("num_codes", g)
	if err != nil {
		return err
	}
	if nc > 1_000_000 {
		return fmt.Errorf("cad: BLOCKSTRETCHACTION codes 数异常 %d", nc)
	}
	for i := 0; i < int(nc); i++ {
		bl, e := R.ReadBL()
		if e != nil {
			return e
		}
		g.Fields = append(g.Fields, ObjField{fmt.Sprintf("codes[%d].bl95", i), int64(bl)})
		ni, e := R.ReadBS()
		if e != nil {
			return e
		}
		if ni > 1000 {
			return fmt.Errorf("cad: BLOCKSTRETCHACTION code indexes 数异常 %d", ni)
		}
		indexes := make([]int64, 0, ni)
		for j := uint16(0); j < ni; j++ {
			iv, e := R.ReadBL()
			if e != nil {
				return e
			}
			indexes = append(indexes, int64(iv))
		}
		g.Fields = append(g.Fields, ObjField{fmt.Sprintf("codes[%d].indexes", i), indexes})
	}
	return decodeBlockActionDoublesFields(R, Ver, fr, g)
}

// decodeGenericBLOCKSTRETCHACTION_HDL handle 流：deps×num_deps +
// hdls×num_hdls（spec 声明顺序：BlockAction deps 在前）。
func decodeGenericBLOCKSTRETCHACTION_HDL(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if g.ValueHandle91 {
		if _, e := objrec.ReadHandleReference(R, g.Handle); e != nil {
			return e
		}
	}
	nd, _ := g.Field("num_deps").(int64)
	if err := decodeBlockActionHandles(R, g, int(nd)); err != nil {
		return err
	}
	nh, _ := g.Field("num_hdls").(int64)
	for i := 0; i < int(nh); i++ {
		h, e := objrec.ReadHandleReference(R, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// decodeBlockActionHandles 读 num_deps 个 deps 句柄。
func decodeBlockActionHandles(R *bitstream.BitStream, g *ObjGeneric, numDeps int) error {
	for i := 0; i < numDeps; i++ {
		h, e := objrec.ReadHandleReference(R, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// decodeGenericBLOCKACTION_HDL 通用 BLOCK*ACTION handle 流：deps×N。
func decodeGenericBLOCKACTION_HDL(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if g.ValueHandle91 {
		if _, e := objrec.ReadHandleReference(R, g.Handle); e != nil {
			return e
		}
	}
	nd := g.HdlCount
	g.HdlCount = 0
	return decodeBlockActionHandles(R, g, nd)
}

// decodeGenericBLOCKGRIPLOCATIONCOMPONENT_HDL handle 流：
// evalexpr.value.handle91（value_code=91 时占 1 个引用）。
func decodeGenericBLOCKGRIPLOCATIONCOMPONENT_HDL(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if g.ValueHandle91 {
		h, e := objrec.ReadHandleReference(R, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// decodeGenericBLOCKUSERPARAMETER_HDL handle 流：assocvariable +
// value.u.handle（EvalVariant 为 HANDLE 类型时）。
func decodeGenericBLOCKUSERPARAMETER_HDL(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	h, e := objrec.ReadHandleReference(R, g.Handle) // assocvariable
	if e != nil {
		return e
	}
	g.Handles = append(g.Handles, h)
	if g.ValueHandle91 {
		h, e := objrec.ReadHandleReference(R, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// decodeGenericBLOCKREPRESENTATION / DYNAMICBLOCKPURGEPREVENTER：
// flag BS；block 句柄在 handle 流（START_OBJECT_HANDLE_STREAM 之后
// 的 FIELD_HANDLE(block)）。
func decodeGenericBLOCKREPRESENTATION(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	return fr.BS("flag", g)
}

// decodeGenericBLOCKREPRESENTATION_HDL handle 流：block 句柄。
func decodeGenericBLOCKREPRESENTATION_HDL(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	h, e := objrec.ReadHandleReference(R, g.Handle)
	if e != nil {
		return e
	}
	g.Handles = append(g.Handles, h)
	return nil
}

// init 注册动态块族解码器。
func init() {
	grip := func(d func(*bitstream.BitStream, container.DwgVersion, *GfRead, *ObjGeneric) error) internalObjectSpec {
		return internalObjectSpec{Decode: d}
	}
	action := func(d func(*bitstream.BitStream, container.DwgVersion, *GfRead, *ObjGeneric) error) internalObjectSpec {
		return internalObjectSpec{Decode: makeBlockAction(d), Hdl: decodeGenericBLOCKACTION_HDL}
	}
	for name, spec := range map[string]internalObjectSpec{
		"BLOCKVISIBILITYGRIP":        grip(decodeGenericBLOCKVISIBILITYGRIP),
		"BLOCKLOOKUPGRIP":            grip(decodeGenericBLOCKVISIBILITYGRIP),
		"BLOCKROTATIONGRIP":          grip(decodeGenericBLOCKVISIBILITYGRIP),
		"BLOCKXYGRIP":                grip(decodeGenericBLOCKVISIBILITYGRIP),
		"BLOCKALIGNMENTGRIP":         grip(decodeGenericBLOCKORIENTATIONGRIP),
		"BLOCKLINEARGRIP":            grip(decodeGenericBLOCKORIENTATIONGRIP),
		"BLOCKFLIPGRIP":              grip(decodeGenericBLOCKFLIPGRIP),
		"BLOCKGRIPLOCATIONCOMPONENT": {Decode: decodeGenericBLOCKGRIPLOCATIONCOMPONENT, Hdl: decodeGenericBLOCKGRIPLOCATIONCOMPONENT_HDL},
		"BLOCKALIGNMENTPARAMETER":    grip(decodeGenericBLOCKALIGNMENTPARAMETER),
		"BLOCKLINEARPARAMETER":       grip(decodeGenericBLOCKLINEARPARAMETER),
		"BLOCKFLIPPARAMETER":         grip(decodeGenericBLOCKFLIPPARAMETER),
		"BLOCKROTATIONPARAMETER":     grip(decodeGenericBLOCKROTATIONPARAMETER),
		"BLOCKPOLARPARAMETER":        grip(decodeGenericBLOCKPOLARPARAMETER),
		"BLOCKBASEPOINTPARAMETER":    grip(decodeGenericBLOCKBASEPOINTPARAMETER),
		"BLOCKPOINTPARAMETER":        grip(decodeGenericBLOCKPOINTPARAMETER),
		"BLOCKXYPARAMETER":           grip(decodeGenericBLOCKXYPARAMETER),
		"BLOCKLOOKUPPARAMETER":       grip(decodeGenericBLOCKLOOKUPPARAMETER),
		"BLOCKUSERPARAMETER":         {Decode: decodeGenericBLOCKUSERPARAMETER, Hdl: decodeGenericBLOCKUSERPARAMETER_HDL},
		"BLOCKVISIBILITYPARAMETER":   {Decode: decodeGenericBLOCKVISIBILITYPARAMETER, Hdl: decodeGenericBLOCKVISIBILITYPARAMETER_HDL},
		"BLOCKMOVEACTION":            action(decodeGenericBLOCKMOVEACTION),
		"BLOCKFLIPACTION":            action(decodeGenericBLOCKFLIPACTION),
		"BLOCKARRAYACTION":           action(decodeGenericBLOCKARRAYACTION),
		"BLOCKROTATEACTION":          {Decode: decodeGenericBLOCKROTATEACTION, Hdl: decodeGenericBLOCKACTION_HDL},
		"BLOCKSCALEACTION":           {Decode: decodeGenericBLOCKSCALEACTION, Hdl: decodeGenericBLOCKACTION_HDL},
		"BLOCKSTRETCHACTION":         {Decode: makeBlockAction(decodeGenericBLOCKSTRETCHACTION), Hdl: decodeGenericBLOCKSTRETCHACTION_HDL},
		"BLOCKREPRESENTATION":        {Decode: decodeGenericBLOCKREPRESENTATION, Hdl: decodeGenericBLOCKREPRESENTATION_HDL, jsonName: "BLOCKREPRESENTATION"},
		"DYNAMICBLOCKPURGEPREVENTER": {Decode: decodeGenericBLOCKREPRESENTATION, Hdl: decodeGenericBLOCKREPRESENTATION_HDL, jsonName: "DYNAMICBLOCKPURGEPREVENTER"},
		// 类名表给出的 DXF 名（dxfname）注册
		"ACDB_BLOCKREPRESENTATION_DATA":           {Decode: decodeGenericBLOCKREPRESENTATION, Hdl: decodeGenericBLOCKREPRESENTATION_HDL, jsonName: "BLOCKREPRESENTATION"},
		"ACDB_DYNAMICBLOCKPURGEPREVENTER_VERSION": {Decode: decodeGenericBLOCKREPRESENTATION, Hdl: decodeGenericBLOCKREPRESENTATION_HDL, jsonName: "DYNAMICBLOCKPURGEPREVENTER"},
	} {
		InternalClassDecoders[name] = spec
	}
}
