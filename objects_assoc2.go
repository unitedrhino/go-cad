// 本文件实现 ASSOC 关联族第二批内部对象（dwg2.spec）：
// ASSOC2DCONSTRAINTGROUP、ASSOCVARIABLE、ASSOCVALUEDEPENDENCY、
// ASSOCDIMDEPENDENCYBODY、ASSOCPATHACTIONPARAM 与 4 种
// SURFACEACTIONBODY（AcDbAssocPathBasedSurfaceActionBody 公共体）。
// gold JSON 中 pab.values/nodes 等数组以扁平键 pab.values[i].x 存取，
// 与 flattenGold 的展平口径一致。

package cad

import (
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/objrec"
)

// decodeAssocActionFields AcDbAssocAction_fields 的 dat 流部分：
// class_version BS（pre-R2010 为 1、R2013+ 为 2）+ geometry_status BL +
// action_index BL + max_assoc_dep_index BL + num_deps BL +
// deps[i].is_owned B×N + R2010+ 追加 4 个未收录字段（BS + BL
// num_owned_params + BS + BL num_values，v9 实测同 ASSOCNETWORK）。
// owningnetwork/actionbody/deps 句柄在 handle 流。
func decodeAssocActionFields(r *bitstream.BitStream, ver container.DwgVersion, fr *gfRead, g *objGeneric) (int, error) {
	if err := fr.BS("class_version", g); err != nil {
		return 0, err
	}
	if err := fr.BL("geometry_status", g); err != nil {
		return 0, err
	}
	if err := fr.BL("action_index", g); err != nil {
		return 0, err
	}
	if err := fr.BL("max_assoc_dep_index", g); err != nil {
		return 0, err
	}
	numDeps, err := fr.BLv("num_deps", g)
	if err != nil {
		return 0, err
	}
	if numDeps < 0 || numDeps > 1_000_000 {
		return 0, fmt.Errorf("cad: ASSOCACTION deps 数异常 %d", numDeps)
	}
	for i := 0; i < int(numDeps); i++ {
		if err := fr.B(fmt.Sprintf("deps[%d].is_owned", i), g); err != nil {
			return 0, err
		}
	}
	if ver >= container.VerR2010 {
		if err := fr.BS("assoc_unknown_bs1", g); err != nil {
			return 0, err
		}
		if _, err = fr.BLv("num_owned_params", g); err != nil {
			return 0, err
		}
		if err := fr.BS("assoc_unknown_bs2", g); err != nil {
			return 0, err
		}
		if _, err = fr.BLv("num_values", g); err != nil {
			return 0, err
		}
	}
	return int(numDeps), nil
}

// decodeAssocActionHandles AcDbAssocAction_fields 的附加 handle 流：
// owningnetwork + actionbody + deps×num_deps。
func decodeAssocActionHandles(r *bitstream.BitStream, g *objGeneric, numDeps int) error {
	for i := 0; i < 2+numDeps; i++ {
		h, e := objrec.ReadHandleReference(r, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// dwgResbufValueType 复刻 LibreDWG dwg_resbuf_value_type 的 DXF 组码
// → 值类型判定（'R'=REAL、'I'=INT32、'S'=INT16、'C'=INT8、'T'=STRING、
// 'H'=HANDLE、'O'=OBJECTID(不读)、'X'=INVALID(不读)）。
func dwgResbufValueType(gc int64) byte {
	switch {
	case gc >= 300:
		switch {
		case gc >= 440:
			switch {
			case gc >= 1000:
				switch {
				case gc == 1004:
					return 'X'
				case gc <= 1009:
					return 'T'
				case gc <= 1039:
					return 'X'
				case gc <= 1042:
					return 'R'
				case gc <= 1069:
					return 'X'
				case gc <= 1070:
					return 'S'
				case gc == 1071:
					return 'I'
				}
				return 'X'
			case gc <= 459:
				return 'I'
			case gc <= 469:
				return 'R'
			case gc <= 479:
				return 'T'
			case gc == 999:
				return 'T'
			}
			return 'X'
		case gc >= 390:
			switch {
			case gc <= 399:
				return 'H'
			case gc <= 409:
				return 'S'
			case gc <= 419:
				return 'T'
			case gc <= 429:
				return 'I'
			case gc <= 439:
				return 'T'
			}
		case gc <= 309:
			return 'T'
		case gc <= 319:
			return 'X'
		case gc <= 329:
			return 'H'
		case gc <= 369:
			return 'O'
		case gc <= 389:
			return 'S'
		}
	case gc >= 105:
		switch {
		case gc >= 210:
			switch {
			case gc <= 269:
				return 'X'
			case gc <= 279:
				return 'S'
			case gc <= 289:
				return 'C'
			case gc <= 299:
				return 'X'
			}
		case gc == 105:
			return 'H'
		case gc <= 109:
			return 'X'
		case gc <= 139:
			return 'X'
		case gc <= 149:
			return 'R'
		case gc <= 169:
			return 'X'
		case gc <= 179:
			return 'S'
		case gc <= 209:
			return 'X'
		}
	default:
		switch {
		case gc >= 38:
			switch {
			case gc <= 59:
				return 'R'
			case gc <= 79:
				return 'S'
			case gc <= 99:
				return 'I'
			case gc <= 102:
				return 'T'
			}
		case gc < 0:
			return 'H'
		case gc == 5:
			return 'H'
		case gc <= 9:
			return 'T'
		case gc <= 37:
			return 'X'
		}
	}
	return 'X'
}

// decodeEvalVariantFields AcDbEvalVariant_fields：code BSd（有符号）
// + 按组码类型的值（REAL→u.bd、INT32→u.bl、INT16→u.bs、INT8→u.rc、
// STRING→u.text、HANDLE→u.handle 占 handle 流；OBJECTID 等类型
// LibreDWG default 分支不读）。返回 handle 流是否占位。
func decodeEvalVariantFields(r *bitstream.BitStream, ver container.DwgVersion, fr *gfRead, g *objGeneric, prefix string) (bool, error) {
	raw, err := r.ReadBS()
	if err != nil {
		return false, err
	}
	code := int64(int16(raw))
	g.Fields = append(g.Fields, objField{prefix + "code", code})
	isHandle := false
	switch dwgResbufValueType(code) {
	case 'R':
		v, e := r.ReadBD()
		if e != nil {
			return false, e
		}
		g.Fields = append(g.Fields, objField{prefix + "u.bd", v})
	case 'I':
		v, e := r.ReadBL()
		if e != nil {
			return false, e
		}
		g.Fields = append(g.Fields, objField{prefix + "u.bl", int64(v)})
	case 'S':
		v, e := r.ReadBS()
		if e != nil {
			return false, e
		}
		g.Fields = append(g.Fields, objField{prefix + "u.bs", int64(v)})
	case 'C':
		v, e := r.ReadRC()
		if e != nil {
			return false, e
		}
		g.Fields = append(g.Fields, objField{prefix + "u.rc", int64(v)})
	case 'T':
		if err := fr.T(prefix+"u.text", g); err != nil {
			return false, err
		}
	case 'H':
		isHandle = true
	}
	return isHandle, nil
}

// ---- ASSOC2DCONSTRAINTGROUP（dwg2.spec，AcDbAssocAction + 约束组）----

// decodeGenericASSOC2DCONSTRAINTGROUP 解析 ASSOC2DCONSTRAINTGROUP：
// AcDbAssocAction_fields + version BL + b1 B + workplane 3BD×3 +
// num_actions BL（actions handle 向量）+ num_nodes BL +
// nodes×N（nodeid BLd + status RC（pre-R2013b）+ num_connections BL +
// connections BL 向量；R2013b+ status 后置）。
func decodeGenericASSOC2DCONSTRAINTGROUP(r *bitstream.BitStream, ver container.DwgVersion, fr *gfRead, g *objGeneric) error {
	if _, err := decodeAssocActionFields(r, ver, fr, g); err != nil {
		return err
	}
	if err := fr.BL("version", g); err != nil {
		return err
	}
	if err := fr.B("b1", g); err != nil {
		return err
	}
	for i := 0; i < 3; i++ {
		if err := fr.Point3(fmt.Sprintf("workplane[%d]", i), g); err != nil {
			return err
		}
	}
	numActions, err := fr.BLv("num_actions", g)
	if err != nil {
		return err
	}
	if numActions < 0 || numActions > 10000 {
		return fmt.Errorf("cad: 2DCONSTRAINTGROUP actions 数异常 %d", numActions)
	}
	nn, err := fr.BLv("num_nodes", g)
	if err != nil {
		return err
	}
	if nn < 0 || nn > 1_000_000 {
		return fmt.Errorf("cad: 2DCONSTRAINTGROUP nodes 数异常 %d", nn)
	}
	preR2013 := ver < container.VerR2013
	for i := 0; i < int(nn); i++ {
		if err := fr.BLd(fmt.Sprintf("nodes[%d].nodeid", i), g); err != nil {
			return err
		}
		var status int64
		if preR2013 {
			v, e := r.ReadRC()
			if e != nil {
				return e
			}
			status = int64(v)
		}
		nc, err := r.ReadBL()
		if err != nil {
			return err
		}
		if nc > 1_000_000 {
			return fmt.Errorf("cad: 2DCONSTRAINTGROUP connections 数异常 %d", nc)
		}
		conns := make([]int64, 0, nc)
		for j := uint32(0); j < nc; j++ {
			cv, e := r.ReadBL()
			if e != nil {
				return e
			}
			conns = append(conns, int64(cv))
		}
		if !preR2013 {
			v, e := r.ReadRC()
			if e != nil {
				return e
			}
			status = int64(v)
		}
		g.Fields = append(g.Fields,
			objField{fmt.Sprintf("nodes[%d].status", i), status},
			objField{fmt.Sprintf("nodes[%d].connections", i), conns})
	}
	return nil
}

// decodeGenericASSOC2DCONSTRAINTGROUP_HDL handle 流：owningnetwork +
// actionbody + deps×N + h1 + actions×num_actions。
func decodeGenericASSOC2DCONSTRAINTGROUP_HDL(r *bitstream.BitStream, ver container.DwgVersion, fr *gfRead, g *objGeneric) error {
	numDeps, _ := g.Field("num_deps").(int64)
	numActions, _ := g.Field("num_actions").(int64)
	total := 2 + int(numDeps) + 1 + int(numActions)
	for i := 0; i < total; i++ {
		h, e := objrec.ReadHandleReference(r, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// ---- ASSOCVARIABLE（AcDbAssocAction + AcDbAssocVariable）----

// decodeGenericASSOCVARIABLE 解析 ASSOCVARIABLE：AcDbAssocAction_fields
// + av_class_version BL + name/t58/evaluator/desc T + AcDbEvalVariant
// （code + u.*，顶层键）+ has_t78 B + t78 T + b290 B。
func decodeGenericASSOCVARIABLE(r *bitstream.BitStream, ver container.DwgVersion, fr *gfRead, g *objGeneric) error {
	if _, err := decodeAssocActionFields(r, ver, fr, g); err != nil {
		return err
	}
	if err := fr.BL("av_class_version", g); err != nil {
		return err
	}
	for _, k := range []string{"name", "t58", "evaluator", "desc"} {
		if err := fr.T(k, g); err != nil {
			return err
		}
	}
	isHandle, err := decodeEvalVariantFields(r, ver, fr, g, "")
	if err != nil {
		return err
	}
	g.valueHandle91 = isHandle // 复用 ACSH 占位标志传递 u.handle
	if err := fr.B("has_t78", g); err != nil {
		return err
	}
	if err := fr.T("t78", g); err != nil {
		return err
	}
	return fr.B("b290", g)
}

// decodeGenericASSOCVARIABLE_HDL handle 流：owningnetwork + actionbody +
// deps×N + u.handle（code 为 HANDLE 类型时）。
func decodeGenericASSOCVARIABLE_HDL(r *bitstream.BitStream, ver container.DwgVersion, fr *gfRead, g *objGeneric) error {
	numDeps, _ := g.Field("num_deps").(int64)
	if err := decodeAssocActionHandles(r, g, int(numDeps)); err != nil {
		return err
	}
	if g.valueHandle91 {
		h, e := objrec.ReadHandleReference(r, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// ---- ASSOCVALUEDEPENDENCY / ASSOCDIMDEPENDENCYBODY ----

// decodeGenericASSOCVALUEDEPENDENCY 解析 ASSOCVALUEDEPENDENCY：与
// ASSOCGEOMDEPENDENCY 同用 AcDbAssocDependency_fields（assocdep. 前缀）
// + 尾部 depbodyid BLd；handle 流 dep_on/readdep/node/dep_body（4 个）。
func decodeGenericASSOCVALUEDEPENDENCY(r *bitstream.BitStream, ver container.DwgVersion, fr *gfRead, g *objGeneric) error {
	if err := decodeGenericASSOCDEPENDENCY_body(r, ver, fr, g, "assocdep."); err != nil {
		return err
	}
	return fr.BL("assocdep.depbodyid", g)
}

// decodeGenericASSOCDIMDEPENDENCYBODY 解析 ASSOCDIMDEPENDENCYBODY：
// adb_version BS + dimbase_version BS + name T + class_version BS
// （spec 均注 always 1/1/…）；无附加 handle。
func decodeGenericASSOCDIMDEPENDENCYBODY(r *bitstream.BitStream, ver container.DwgVersion, fr *gfRead, g *objGeneric) error {
	if err := fr.BS("adb_version", g); err != nil {
		return err
	}
	if err := fr.BS("dimbase_version", g); err != nil {
		return err
	}
	if err := fr.T("name", g); err != nil {
		return err
	}
	return fr.BS("class_version", g)
}

// ---- ASSOCPATHACTIONPARAM（AcDbAssocActionParam + Compound + Path）----

// decodeGenericASSOCPATHACTIONPARAM 解析 ASSOCPATHACTIONPARAM：
// is_r2013 BS（R2013b+ 置 1）+ [aap_version BL（R2013b+）] + name T +
// class_version BS + bs1 BS + num_params BL（params handle 向量；
// has_child_param 恒 0 跳过）+ version BL。
func decodeGenericASSOCPATHACTIONPARAM(r *bitstream.BitStream, ver container.DwgVersion, fr *gfRead, g *objGeneric) error {
	if err := fr.BS("is_r2013", g); err != nil {
		return err
	}
	if ver >= container.VerR2013 {
		if err := fr.BL("aap_version", g); err != nil {
			return err
		}
	}
	if err := fr.T("name", g); err != nil {
		return err
	}
	if err := fr.BS("class_version", g); err != nil {
		return err
	}
	if err := fr.BS("bs1", g); err != nil {
		return err
	}
	if _, err := fr.BLv("num_params", g); err != nil {
		return err
	}
	return fr.BL("version", g)
}

// decodeGenericASSOCPATHACTIONPARAM_HDL handle 流：params×num_params。
func decodeGenericASSOCPATHACTIONPARAM_HDL(r *bitstream.BitStream, ver container.DwgVersion, fr *gfRead, g *objGeneric) error {
	numParams, _ := g.Field("num_params").(int64)
	for i := 0; i < int(numParams); i++ {
		h, e := objrec.ReadHandleReference(r, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// ---- SURFACEACTIONBODY 族（AcDbAssocPathBasedSurfaceActionBody）----

// decodeAcisValueParam 读 AcDbValueParam_fields（前缀如
// "pab.values[0]."）：class_version BL + name T + unit_type BL +
// num_vars BL + vars×N（AcDbEvalVariant + vars[j].handle handle 流）+
// controlled_objdep handle 流。
func decodeAcisValueParam(r *bitstream.BitStream, ver container.DwgVersion, fr *gfRead, g *objGeneric, prefix string, hdlCount *int) error {
	if err := fr.BL(prefix+"class_version", g); err != nil {
		return err
	}
	if err := fr.T(prefix+"name", g); err != nil {
		return err
	}
	if err := fr.BL(prefix+"unit_type", g); err != nil {
		return err
	}
	nv, err := fr.BLv(prefix+"num_vars", g)
	if err != nil {
		return err
	}
	if nv < 0 || nv > 1_000_000 {
		return fmt.Errorf("cad: ValueParam vars 数异常 %d", nv)
	}
	for j := 0; j < int(nv); j++ {
		vp := prefix + "vars[" + itoa(j) + "]."
		isHandle, e := decodeEvalVariantFields(r, ver, fr, g, vp)
		if e != nil {
			return e
		}
		if isHandle {
			*hdlCount++
		}
	}
	*hdlCount++ // controlled_objdep
	return nil
}

// decodeAcisParamBasedActionBody 读 AcDbAssocParamBasedActionBody_fields
// （仅 pre-R2013b 有此段）：version/minor/num_deps BL + l4/num_values BL
// + [num_values=0：l5 BL] + values×num_values（AcDbValueParam）。
// deps/pab.assocdep/values 内部句柄在 handle 流，hdlCount 累加。
func decodeAcisParamBasedActionBody(r *bitstream.BitStream, ver container.DwgVersion, fr *gfRead, g *objGeneric, hdlCount *int) error {
	if ver >= container.VerR2013 { // PRE (R_2013b)：R2013b+ 整段不存在
		return nil
	}
	if err := fr.BL("version", g); err != nil {
		return err
	}
	if err := fr.BL("minor", g); err != nil {
		return err
	}
	nd, err := fr.BLv("num_deps", g)
	if err != nil {
		return err
	}
	if nd < 0 || nd > 1_000_000 {
		return fmt.Errorf("cad: pab deps 数异常 %d", nd)
	}
	*hdlCount += int(nd)
	if err := fr.BL("l4", g); err != nil {
		return err
	}
	nv, err := fr.BLv("num_values", g)
	if err != nil {
		return err
	}
	if nv < 0 || nv > 1_000_000 {
		return fmt.Errorf("cad: pab values 数异常 %d", nv)
	}
	if nv == 0 {
		if err := fr.BL("l5", g); err != nil {
			return err
		}
		*hdlCount++ // pab.assocdep
	}
	for i := 0; i < int(nv); i++ {
		if err := decodeAcisValueParam(r, ver, fr, g, "pab.values["+itoa(i)+"].", hdlCount); err != nil {
			return err
		}
	}
	return nil
}

// decodeAcisSurfaceActionBody 读 AcDbAssocSurfaceActionBody_fields：
// version BL + is_semi_assoc B + l2 BL + is_semi_ovr B + grip_status BS
// （sab.assocdep 句柄占 handle 流 1 个）。
func decodeAcisSurfaceActionBody(r *bitstream.BitStream, ver container.DwgVersion, fr *gfRead, g *objGeneric, hdlCount *int) error {
	if err := fr.BL("version", g); err != nil {
		return err
	}
	*hdlCount++ // sab.assocdep
	if err := fr.B("is_semi_assoc", g); err != nil {
		return err
	}
	if err := fr.BL("l2", g); err != nil {
		return err
	}
	if err := fr.B("is_semi_ovr", g); err != nil {
		return err
	}
	return fr.BS("grip_status", g)
}

// makeGenericSURFACEACTIONBODY 生成 SURFACEACTIONBODY 族解码器：
// AcDbAssocActionBody（aab_version BL）+ ParamBased + Surface 体 +
// pbsab_status BL + 各类尾部 class_version BL。
func makeGenericSURFACEACTIONBODY(tail func(*bitstream.BitStream, container.DwgVersion, *gfRead, *objGeneric) error) func(*bitstream.BitStream, container.DwgVersion, *gfRead, *objGeneric) error {
	return func(r *bitstream.BitStream, ver container.DwgVersion, fr *gfRead, g *objGeneric) error {
		if err := fr.BL("aab_version", g); err != nil {
			return err
		}
		hdlCount := 0
		if err := decodeAcisParamBasedActionBody(r, ver, fr, g, &hdlCount); err != nil {
			return err
		}
		if err := decodeAcisSurfaceActionBody(r, ver, fr, g, &hdlCount); err != nil {
			return err
		}
		if err := fr.BL("pbsab_status", g); err != nil {
			return err
		}
		g.hdlCount = hdlCount
		return tail(r, ver, fr, g)
	}
}

// decodeGenericSURFACEACTIONBODY_HDL SURFACEACTIONBODY 族 handle 流：
// pab.deps/pab.assocdep/values 句柄/sab.assocdep（按 hdlCount）。
func decodeGenericSURFACEACTIONBODY_HDL(r *bitstream.BitStream, ver container.DwgVersion, fr *gfRead, g *objGeneric) error {
	for i := 0; i < g.hdlCount; i++ {
		h, e := objrec.ReadHandleReference(r, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// init 注册 ASSOC 族第二批解码器。SURFACEACTIONBODY 各变体的尾部
// 专有字段按 dwg2.spec 实现（NETWORK 无尾字段）。
func init() {
	// EXTEND：class_version BL + option RC
	extendTail := func(r *bitstream.BitStream, ver container.DwgVersion, fr *gfRead, g *objGeneric) error {
		if err := fr.BL("class_version", g); err != nil {
			return err
		}
		return fr.RC("option", g)
	}
	// OFFSET：class_version BL + b1 B
	offsetTail := func(r *bitstream.BitStream, ver container.DwgVersion, fr *gfRead, g *objGeneric) error {
		if err := fr.BL("class_version", g); err != nil {
			return err
		}
		return fr.B("b1", g)
	}
	// TRIM：class_version BL + b1/b2 B + distance BD
	trimTail := func(r *bitstream.BitStream, ver container.DwgVersion, fr *gfRead, g *objGeneric) error {
		if err := fr.BL("class_version", g); err != nil {
			return err
		}
		if err := fr.B("b1", g); err != nil {
			return err
		}
		if err := fr.B("b2", g); err != nil {
			return err
		}
		return fr.BD("distance", g)
	}
	// BLEND：class_version BL + b1/b2/b3 B + blend_options BS +
	// b4/b5 B + bs2 BS
	blendTail := func(r *bitstream.BitStream, ver container.DwgVersion, fr *gfRead, g *objGeneric) error {
		if err := fr.BL("class_version", g); err != nil {
			return err
		}
		if err := fr.B("b1", g); err != nil {
			return err
		}
		if err := fr.B("b2", g); err != nil {
			return err
		}
		if err := fr.B("b3", g); err != nil {
			return err
		}
		if err := fr.BS("blend_options", g); err != nil {
			return err
		}
		if err := fr.B("b4", g); err != nil {
			return err
		}
		if err := fr.B("b5", g); err != nil {
			return err
		}
		return fr.BS("bs2", g)
	}
	cvTail := func(r *bitstream.BitStream, ver container.DwgVersion, fr *gfRead, g *objGeneric) error {
		return fr.BL("class_version", g)
	}
	surfaceSpec := func(d func(*bitstream.BitStream, container.DwgVersion, *gfRead, *objGeneric) error) internalObjectSpec {
		return internalObjectSpec{
			decode: makeGenericSURFACEACTIONBODY(d),
			hdl:    decodeGenericSURFACEACTIONBODY_HDL,
		}
	}
	for name, spec := range map[string]internalObjectSpec{
		"ASSOC2DCONSTRAINTGROUP":             {decode: decodeGenericASSOC2DCONSTRAINTGROUP, hdl: decodeGenericASSOC2DCONSTRAINTGROUP_HDL},
		"ACDBASSOC2DCONSTRAINTGROUP":         {decode: decodeGenericASSOC2DCONSTRAINTGROUP, hdl: decodeGenericASSOC2DCONSTRAINTGROUP_HDL},
		"ASSOCVARIABLE":                      {decode: decodeGenericASSOCVARIABLE, hdl: decodeGenericASSOCVARIABLE_HDL},
		"ACDBASSOCVARIABLE":                  {decode: decodeGenericASSOCVARIABLE, hdl: decodeGenericASSOCVARIABLE_HDL},
		"ASSOCVALUEDEPENDENCY":               {decode: decodeGenericASSOCVALUEDEPENDENCY, hdl: decodeGenericASSOCDEPENDENCY_HDL},
		"ACDBASSOCVALUEDEPENDENCY":           {decode: decodeGenericASSOCVALUEDEPENDENCY, hdl: decodeGenericASSOCDEPENDENCY_HDL},
		"ASSOCDIMDEPENDENCYBODY":             {decode: decodeGenericASSOCDIMDEPENDENCYBODY},
		"ACDBASSOCDIMDEPENDENCYBODY":         {decode: decodeGenericASSOCDIMDEPENDENCYBODY},
		"ASSOCPATHACTIONPARAM":               {decode: decodeGenericASSOCPATHACTIONPARAM, hdl: decodeGenericASSOCPATHACTIONPARAM_HDL},
		"ACDBASSOCPATHACTIONPARAM":           {decode: decodeGenericASSOCPATHACTIONPARAM, hdl: decodeGenericASSOCPATHACTIONPARAM_HDL},
		"ASSOCPLANESURFACEACTIONBODY":        surfaceSpec(cvTail),
		"ACDBASSOCPLANESURFACEACTIONBODY":    surfaceSpec(cvTail),
		"ASSOCEXTRUDEDSURFACEACTIONBODY":     surfaceSpec(cvTail),
		"ACDBASSOCEXTRUDEDSURFACEACTIONBODY": surfaceSpec(cvTail),
		"ASSOCLOFTEDSURFACEACTIONBODY":       surfaceSpec(cvTail),
		"ACDBASSOCLOFTEDSURFACEACTIONBODY":   surfaceSpec(cvTail),
		"ASSOCREVOLVEDSURFACEACTIONBODY":     surfaceSpec(cvTail),
		"ACDBASSOCREVOLVEDSURFACEACTIONBODY": surfaceSpec(cvTail),
		"ASSOCNETWORKSURFACEACTIONBODY":      surfaceSpec(func(*bitstream.BitStream, container.DwgVersion, *gfRead, *objGeneric) error { return nil }),
		"ACDBASSOCNETWORKSURFACEACTIONBODY":  surfaceSpec(func(*bitstream.BitStream, container.DwgVersion, *gfRead, *objGeneric) error { return nil }),
		"ASSOCEXTENDSURFACEACTIONBODY":       surfaceSpec(extendTail),
		"ACDBASSOCEXTENDSURFACEACTIONBODY":   surfaceSpec(extendTail),
		"ASSOCOFFSETSURFACEACTIONBODY":       surfaceSpec(offsetTail),
		"ACDBASSOCOFFSETSURFACEACTIONBODY":   surfaceSpec(offsetTail),
		"ASSOCTRIMSURFACEACTIONBODY":         surfaceSpec(trimTail),
		"ACDBASSOCTRIMSURFACEACTIONBODY":     surfaceSpec(trimTail),
		"ASSOCBLENDSURFACEACTIONBODY":        surfaceSpec(blendTail),
		"ACDBASSOCBLENDSURFACEACTIONBODY":    surfaceSpec(blendTail),
	} {
		internalClassDecoders[name] = spec
	}
}
