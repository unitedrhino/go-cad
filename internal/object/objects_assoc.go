// 本文件实现 ASSOC 关联系列与求值图内部对象的解码：DIMASSOC、
// ASSOCNETWORK、ASSOCACTION/ASSOCDEPENDENCY/ASSOCGEOMDEPENDENCY、
// EVALUATION_GRAPH、ASSOCOSNAPPOINTREFACTIONPARAM/
// ASSOCVERTEXACTIONPARAM。

package object

import (
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/objrec"
)

// ---- DIMASSOC（dwg2.spec DWG_OBJECT(DIMASSOC)，类 526）----

// decodeGenericDIMASSOC 解析 DIMASSOC 的 dat 流：
// BLx associativity + B trans_space_flag + RC rotated_type + 6 个
// AcDbOsnapPointRef 块（associativity 位 rcount1 为 0 且前一块无
// has_lastpt_ref 时跳过该块）。每块：classname T + osnap_type RC +
// num_xrefs BL + [osnap_type≠0: main_subent_type BL + main_gsmarker BL +
// num_xrefpaths BL + xrefpaths T 向量] + osnap_dist BD + osnap_pt 3BD +
// [osnap_type∈{6,11}: intsectobj 计数字段] + has_lastpt_ref B。
func decodeGenericDIMASSOC(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := fr.BL("associativity", g); err != nil {
		return err
	}
	assoc, _ := g.Field("associativity").(int64)
	if err := fr.B("trans_space_flag", g); err != nil {
		return err
	}
	if err := fr.RC("rotated_type", g); err != nil {
		return err
	}
	hasLast := false
	for i := 0; i < 6; i++ {
		if assoc&(1<<uint(i)) == 0 && !hasLast {
			// 该 ref 块未启用：不占位（JSON 输出空对象）
			continue
		}
		if err := fr.T("ref["+itoa(i)+"].classname", g); err != nil {
			return err
		}
		if err := fr.RC("ref["+itoa(i)+"].osnap_type", g); err != nil {
			return err
		}
		osnap := g.Field("ref[" + itoa(i) + "].osnap_type").(int64)
		nx, err := fr.BLv("ref["+itoa(i)+"].num_xrefs", g)
		if err != nil {
			return err
		}
		g.Fields = append(g.Fields, ObjField{"hdl.ref[" + itoa(i) + "].num_xrefs", nx})
		if osnap != 0 {
			if err := fr.BL("ref["+itoa(i)+"].main_subent_type", g); err != nil {
				return err
			}
			if err := fr.BL("ref["+itoa(i)+"].main_gsmarker", g); err != nil {
				return err
			}
			np, err := fr.BLv("ref["+itoa(i)+"].num_xrefpaths", g)
			if err != nil {
				return err
			}
			if np < 0 || np > 10000 {
				return fmt.Errorf("cad: DIMASSOC xrefpaths 数异常 %d", np)
			}
			for j := 0; j < int(np); j++ {
				if err := fr.T("ref["+itoa(i)+"].xrefpaths", g); err != nil {
					return err
				}
			}
			_ = np
		}
		if err := fr.BD("ref["+itoa(i)+"].osnap_dist", g); err != nil {
			return err
		}
		if err := fr.Point3("ref["+itoa(i)+"].osnap_pt", g); err != nil {
			return err
		}
		if osnap == 6 || osnap == 11 {
			if _, err := fr.BLv("ref["+itoa(i)+"].num_intsectobj", g); err != nil {
				return err
			}
			if err := fr.BL("ref["+itoa(i)+"].intersec_subent_type", g); err != nil {
				return err
			}
			if err := fr.BL("ref["+itoa(i)+"].intersec_gsmarker", g); err != nil {
				return err
			}
			nix, err := fr.BLv("ref["+itoa(i)+"].num_intersec_xrefpaths", g)
			if err != nil {
				return err
			}
			if nix < 0 || nix > 10000 {
				return fmt.Errorf("cad: DIMASSOC 交点 xrefpaths 数异常 %d", nix)
			}
			for j := 0; j < int(nix); j++ {
				if err := fr.T("ref["+itoa(i)+"].intersec_xrefpaths", g); err != nil {
					return err
				}
			}
		}
		hl, e := R.ReadB()
		if e != nil {
			return e
		}
		hasLast = hl != 0
		g.Fields = append(g.Fields, ObjField{"ref[" + itoa(i) + "].has_lastpt_ref", hasLast})
	}
	return nil
}

// decodeGenericDIMASSOC_HDL DIMASSOC 的 handle 流
// （owner/reactors/xdic 之后）：dimensionobj + 各启用 ref 块的
// xrefs 向量与 intsectobj 向量。
func decodeGenericDIMASSOC_HDL(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	assoc, _ := g.Field("associativity").(int64)
	// 重建各 ref 块的启用与计数（与 dat 流相同的条件链）
	hasLast := false
	type refCnt struct {
		active   int
		xrefs    int
		intsect  int
		osnap    int64
		intsType int64
	}
	var refs []refCnt
	for i := 0; i < 6; i++ {
		rc := refCnt{}
		if assoc&(1<<uint(i)) == 0 && !hasLast {
			refs = append(refs, rc)
			continue
		}
		rc.active = 1
		if v, ok := g.Field("hdl.ref[" + itoa(i) + "].num_xrefs").(int64); ok {
			rc.xrefs = int(v)
		}
		if v, ok := g.Field("ref[" + itoa(i) + "].osnap_type").(int64); ok {
			rc.osnap = v
		}
		if rc.osnap == 6 || rc.osnap == 11 {
			if v, ok := g.Field("ref[" + itoa(i) + "].num_intsectobj").(int64); ok {
				rc.intsect = int(v)
			}
			if v, ok := g.Field("ref[" + itoa(i) + "].intersec_subent_type").(int64); ok {
				rc.intsType = v
			}
		}
		if v, ok := g.Field("ref[" + itoa(i) + "].has_lastpt_ref").(bool); ok {
			hasLast = v
		}
		refs = append(refs, rc)
	}
	total := 1 // dimensionobj
	for _, rc := range refs {
		total += rc.xrefs + rc.intsect
	}
	for i := 0; i < total; i++ {
		h, e := objrec.ReadHandleReference(R, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// itoa 整数转十进制字符串（内部键名拼装用）。
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

// ---- ASSOCNETWORK（dwg2.spec，类 518，AcDbEvalExpr/AssocAction 系列）----

// decodeGenericASSOCNETWORK 解析 ASSOCNETWORK 的 dat 流：
// AcDbEvalExpr（parentid/major/minor 3×BL）+ AcDbAssocAction
// （class_version BS + geometry_status BL + action_index BL +
// max_assoc_dep_index BL + num_deps BL + deps×N）+ AcDbAssocNetwork
// （network_version BS + network_action_index BL + num_actions BL +
// actions×N + num_owned_actions BL）。
func decodeGenericASSOCNETWORK(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := fr.BS("class_version", g); err != nil {
		return err
	}
	if err := fr.BL("geometry_status", g); err != nil {
		return err
	}
	if err := fr.BL("action_index", g); err != nil {
		return err
	}
	if err := fr.BL("max_assoc_dep_index", g); err != nil {
		return err
	}
	numDeps, err := fr.BLv("num_deps", g)
	if err != nil {
		return err
	}
	for i := 0; i < int(numDeps); i++ {
		if err := fr.B("deps.is_owned", g); err != nil {
			return err
		}
	}
	// 对照 v9 实测：R2010+ 在 num_deps 后为 BS + BL num_owned_params +
	// BS + BL num_values 四个字段（dwg2.spec 未收录，值 0 占 2 位）；
	// R2007 无这段
	if Ver >= container.VerR2010 {
		if err = fr.BS("assoc_unknown_bs1", g); err != nil {
			return err
		}
		if _, err = fr.BLv("num_owned_params", g); err != nil {
			return err
		}
		if err = fr.BS("assoc_unknown_bs2", g); err != nil {
			return err
		}
		if _, err = fr.BLv("num_values", g); err != nil {
			return err
		}
	}
	if err := fr.BS("network_version", g); err != nil {
		return err
	}
	if err := fr.BL("network_action_index", g); err != nil {
		return err
	}
	numActions, err := fr.BLv("num_actions", g)
	if err != nil {
		return err
	}
	for i := 0; i < int(numActions); i++ {
		if err := fr.B(fmt.Sprintf("actions[%d].is_owned", i), g); err != nil {
			return err
		}
	}
	_, err = fr.BLv("num_owned_actions", g)
	return err
}

// decodeGenericASSOCNETWORK_HDL ASSOCNETWORK 的 handle 流
// （owner/reactors/xdic 之后）：owningnetwork + actionbody +
// deps×num_deps 的 dep + actions×num_actions 的 dep +
// owned_actions×num_owned_actions。
func decodeGenericASSOCNETWORK_HDL(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	numDeps, _ := g.Field("num_deps").(int64)
	numActions, _ := g.Field("num_actions").(int64)
	numOwned, _ := g.Field("num_owned_actions").(int64)
	total := 2 + int(numDeps) + int(numActions) + int(numOwned)
	for i := 0; i < total; i++ {
		h, e := objrec.ReadHandleReference(R, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// ---- ASSOCACTION / ASSOCDEPENDENCY / ASSOCGEOMDEPENDENCY 系列 ----

// decodeGenericASSOCACTION 解析 ASSOCACTION（类 519）的 dat 流：
// BS class_version + BL geometry_status + BL action_index +
// BL max_assoc_dep_index + BL num_deps + deps×N（B is_owned）。
func decodeGenericASSOCACTION(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := fr.BS("class_version", g); err != nil {
		return err
	}
	if err := fr.BL("geometry_status", g); err != nil {
		return err
	}
	if err := fr.BL("action_index", g); err != nil {
		return err
	}
	if err := fr.BL("max_assoc_dep_index", g); err != nil {
		return err
	}
	numDeps, err := fr.BLv("num_deps", g)
	if err != nil {
		return err
	}
	for i := 0; i < int(numDeps); i++ {
		if err := fr.B(fmt.Sprintf("deps[%d].is_owned", i), g); err != nil {
			return err
		}
	}
	return nil
}

// decodeGenericASSOCACTION_HDL ASSOCACTION 的 handle 流
// （owner/reactors/xdic 之后）：owningnetwork + actionbody + deps×N dep。
func decodeGenericASSOCACTION_HDL(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	numDeps, _ := g.Field("num_deps").(int64)
	total := 2 + int(numDeps)
	for i := 0; i < total; i++ {
		h, e := objrec.ReadHandleReference(R, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// decodeGenericASSOCDEPENDENCY_body 解析 AcDbAssocDependency_fields：
// BS class_version + BL status + 4×B（is_read_dep/is_write_dep/
// is_attached_to_object/is_delegating_to_owning_action）+ BLd order +
// B has_name（=1 时跟 name T）。dep_on/readdep/node/dep_body 四个
// 句柄在 handle 流，dat 不占位。prefix 为键名前缀（ASSOCGEOMDEPENDENCY
// 的依赖字段带 "assocdep." 前缀，ASSOCDEPENDENCY 无前缀）。
func decodeGenericASSOCDEPENDENCY_body(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric, prefix string) error {
	if err := fr.BS(prefix+"class_version", g); err != nil {
		return err
	}
	if err := fr.BL(prefix+"status", g); err != nil {
		return err
	}
	for _, k := range []string{"is_read_dep", "is_write_dep", "is_attached_to_object", "is_delegating_to_owning_action"} {
		if err := fr.B(prefix+k, g); err != nil {
			return err
		}
	}
	// order 为 BLd 有符号（gold 负序值，如 -10000/-2147483648）
	if err := fr.BLd(prefix+"order", g); err != nil {
		return err
	}
	hasName, err := fr.Bv(prefix+"has_name", g)
	if err != nil {
		return err
	}
	if hasName {
		return fr.T(prefix+"name", g)
	}
	return nil
}

// decodeGenericASSOCDEPENDENCY 解析 ASSOCDEPENDENCY（类 524）：
// 依赖体字段 + depbodyid BLd；handle 流含 dep_on/readdep/node/dep_body。
func decodeGenericASSOCDEPENDENCY(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := decodeGenericASSOCDEPENDENCY_body(R, Ver, fr, g, ""); err != nil {
		return err
	}
	return fr.BL("depbodyid", g)
}

// decodeGenericASSOCDEPENDENCY_HDL ASSOCDEPENDENCY 的 handle 流：
// dep_on + readdep + node + dep_body（4 个）。
func decodeGenericASSOCDEPENDENCY_HDL(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	for i := 0; i < 4; i++ {
		h, e := objrec.ReadHandleReference(R, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// decodeGenericASSOCGEOMDEPENDENCY 解析 ASSOCGEOMDEPENDENCY（类 523）：
// AcDbAssocDependency_fields（含尾部 depbodyid BLd，v9 R2000 实测
// @137..147）+ class_version BS + enabled B + classname T +
// dependent_on_compound_object B；handle 流同 ASSOCDEPENDENCY（4 个）。
func decodeGenericASSOCGEOMDEPENDENCY(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := decodeGenericASSOCDEPENDENCY_body(R, Ver, fr, g, "assocdep."); err != nil {
		return err
	}
	if err := fr.BL("assocdep.depbodyid", g); err != nil {
		return err
	}
	if err := fr.BS("class_version", g); err != nil {
		return err
	}
	if err := fr.B("enabled", g); err != nil {
		return err
	}
	if err := fr.T("classname", g); err != nil {
		return err
	}
	return fr.B("dependent_on_compound_object", g)
}

// ---- EVALUATION_GRAPH（dwg2.spec，类 516，AcDbEvalGraph）----

// decodeGenericEVALUATION_GRAPH 解析 EVALUATION_GRAPH 的 dat 流：
// BL first_nodeid/copy + BL num_nodes + nodes×N（id BL + edge_flags BL
// [≠32 时截断] + nextid BLd + node 4×BLd）+ BL num_edges + edges×N
// （id BL + nextid/e1/e2/e3 BLd + out_edge×5 BLd）。
// nodes 的 evalexpr 句柄在 handle 流。
func decodeGenericEVALUATION_GRAPH(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := fr.BL("first_nodeid", g); err != nil {
		return err
	}
	if err := fr.BL("first_nodeid_copy", g); err != nil {
		return err
	}
	numNodes, err := fr.BLv("num_nodes", g)
	if err != nil {
		return err
	}
	for i := 0; i < int(numNodes); i++ {
		np := fmt.Sprintf("nodes[%d].", i)
		if err := fr.BL(np+"id", g); err != nil {
			return err
		}
		if err := fr.BL(np+"edge_flags", g); err != nil {
			return err
		}
		ef, _ := g.FieldPath(np + "edge_flags").(int64)
		if ef != 32 { // 非法 edge_flags：节点表截断（LibreDWG 同行为：
			// 当前节点 edge_flags 重置为 0、num_nodes 记为 i）
			g.Fields = append(g.Fields, ObjField{np + "edge_flags", int64(0)})
			g.Fields = append(g.Fields, ObjField{"num_nodes", int64(i)})
			return nil
		}
		if err := fr.BL(np+"nextid", g); err != nil {
			return err
		}
		for j := 0; j < 4; j++ {
			if err := fr.BL(fmt.Sprintf("%snode[%d]", np, j), g); err != nil {
				return err
			}
		}
	}
	numEdges, err := fr.BLv("num_edges", g)
	if err != nil {
		return err
	}
	for i := 0; i < int(numEdges); i++ {
		ep := fmt.Sprintf("edges[%d].", i)
		if err := fr.BL(ep+"id", g); err != nil {
			return err
		}
		for _, k := range []string{"nextid", "e1", "e2", "e3"} {
			if err := fr.BL(ep+k, g); err != nil {
				return err
			}
		}
		for j := 0; j < 5; j++ {
			// out_edge 为 BLd 有符号（gold -1 表无出边）
			if err := fr.BLd(fmt.Sprintf("%sout_edge[%d]", ep, j), g); err != nil {
				return err
			}
		}
	}
	return nil
}

// decodeGenericEVALUATION_GRAPH_HDL EVALUATION_GRAPH 的 handle 流：
// 每 node 1 个 evalexpr 句柄。
func decodeGenericEVALUATION_GRAPH_HDL(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	numNodes, _ := g.Field("num_nodes").(int64)
	for i := 0; i < int(numNodes); i++ {
		h, e := objrec.ReadHandleReference(R, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
		// 每 node 恰有一个 evalexpr 句柄，按 nodes[i].evalexpr 关联
		//（对齐 dwgread JSON 的 nodes[i].evalexpr 键）
		g.Fields = append(g.Fields, ObjField{fmt.Sprintf("nodes[%d].evalexpr", i), h})
	}
	return nil
}

// decodeGenericMLINESTYLE_HDL MLINESTYLE 的 handle 流：
// lines×N 的 lt_ltype 句柄仅 R2018+ 存在（pre-R2018 线型内联为
// lt_index，无 hdl 引用）。
func decodeGenericMLINESTYLE_HDL(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if Ver < container.VerR2018 {
		return nil
	}
	numLines, _ := g.Field("num_lines").(int64)
	for i := 0; i < int(numLines); i++ {
		h, e := objrec.ReadHandleReference(R, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// ---- ASSOCOSNAPPOINTREFACTIONPARAM / ASSOCVERTEXACTIONPARAM ----

// decodeGenericASSOCOSNAPPOINTREFACTIONPARAM 解析 ASSOCOSNAPPOINTREFACTIONPARAM
// （类 521）的 dat 流：BS is_r2013 + BL aap_version + T name（字符串流）+
// BS class_version + BS bs1 + BL num_params + BS status + RC osnap_mode +
// BD param。
func decodeGenericASSOCOSNAPPOINTREFACTIONPARAM(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := fr.BS("is_r2013", g); err != nil {
		return err
	}
	// aap_version 仅 SINCE R_2013b（pre-R2013 文件无此字段）
	if Ver >= container.VerR2013 {
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
	numParams, err := fr.BLv("num_params", g)
	if err != nil {
		return err
	}
	if err := fr.BS("status", g); err != nil {
		return err
	}
	if err := fr.RC("osnap_mode", g); err != nil {
		return err
	}
	if err := fr.BD("param", g); err != nil {
		return err
	}
	_ = numParams
	return nil
}

// decodeGenericASSOCOSNAPPOINTREFACTIONPARAM_HDL handle 流：
// params×num_params。
func decodeGenericASSOCOSNAPPOINTREFACTIONPARAM_HDL(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	numParams, _ := g.Field("num_params").(int64)
	for i := 0; i < int(numParams); i++ {
		h, e := objrec.ReadHandleReference(R, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// decodeGenericASSOCVERTEXACTIONPARAM 解析 ASSOCVERTEXACTIONPARAM
// （类 522）的 dat 流：BS is_r2013 + BL aap_version + T name（字符串流）+
// BL asdap_class_version + BL class_version + 3BD pt。
func decodeGenericASSOCVERTEXACTIONPARAM(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	if err := fr.BS("is_r2013", g); err != nil {
		return err
	}
	// aap_version 仅 SINCE R_2013b（pre-R2013 文件无此字段）
	if Ver >= container.VerR2013 {
		if err := fr.BL("aap_version", g); err != nil {
			return err
		}
	}
	if err := fr.T("name", g); err != nil {
		return err
	}
	if err := fr.BL("asdap_class_version", g); err != nil {
		return err
	}
	if err := fr.BL("class_version", g); err != nil {
		return err
	}
	return fr.Point3("pt", g)
}

// probeBlockHeaderR2000b 探测 AC1015 文件的 BLOCK_HEADER 是否含
// R2000b 块（num_inserts RC 计数循环 + description T + preview_size BL +
// preview 二进制）。在 r 当前位置试读整块：约束（计数上界、长度上界、
// 流未越界）全部满足返回 true 且 r 定位到块尾；否则回退到起点返回
// false（pre-R2000b 布局无该块，直接是后续 handle 流）。
func probeBlockHeaderR2000b(R *bitstream.BitStream, fr *GfRead) bool {
	start := R.TellBits()
	bodyBits := uint64(len(R.Src)) * 8
	// num_inserts：RC 计数循环（遇 0 结束，计非零个数）
	ni := 0
	for {
		b, e := R.ReadRC()
		if e != nil || ni > 0xf00000 {
			R.SetBitPos(start)
			return false
		}
		if b == 0 {
			break
		}
		ni++
	}
	// description T：pre-R2004 TV（BS 长度 + 字节）
	l, e := R.ReadBS()
	if e != nil || l > 0xf000 {
		R.SetBitPos(start)
		return false
	}
	if int(l) > len(R.Src)-R.Pos {
		R.SetBitPos(start)
		return false
	}
	for i := 0; i < int(l); i++ {
		if _, e := R.ReadRC(); e != nil {
			R.SetBitPos(start)
			return false
		}
	}
	// preview_size BL + preview 二进制
	ps, e := R.ReadBL()
	if e != nil || ps > 0xA00000 {
		R.SetBitPos(start)
		return false
	}
	if int(ps) > len(R.Src)-R.Pos {
		R.SetBitPos(start)
		return false
	}
	for i := 0; i < int(ps); i++ {
		if _, e := R.ReadRC(); e != nil {
			R.SetBitPos(start)
			return false
		}
	}
	// 整块读完须仍在对象范围内
	if R.TellBits() > bodyBits {
		R.SetBitPos(start)
		return false
	}
	return true
}

// BSd 读有符号 BS 字段（DIMLWD/DIMLWE 等）：负值以 18 位编码
// （BB "00" + RS16，int16 解释），非负与普通 BS 相同。
func (f *GfRead) BSd(key string, g *ObjGeneric) error {
	v, err := f.R.ReadBS()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, ObjField{key, int64(int16(v))})
	return nil
}
