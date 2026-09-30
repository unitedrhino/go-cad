// entity_audit.go 实体审计支持：字段导出器（对照 dwgread -O JSON 的
// gold 标量键）与实体句柄索引。键名与 LibreDWG JSON 输出一致
// （entity/bitsize/size/entmode/color/ltype_scale/linewt/z_is_zero/
// thickness/start/end/extrusion 等），供 TestAlignmentAudit 扩展后
// 对实体做与内部对象同口径的值级审计。
package cad

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// entityField 实体字段导出器：按 gold JSON 键名返回实体字段值。
// 公共键（所有实体共享，出自 commonEntityHead/baseEntity）：
// handle/bitsize/size/entmode/color/ltype_scale/ltype_flags/
// plotstyle_flags/invisible/linewt；per-type 几何键按类型分发。
// 未建模键返回 nil（审计侧按缺失处理）。
func entityField(ent any, key string) any {
	ec, ok := ent.(entityCommon)
	if !ok {
		return nil
	}
	b := ec.common()
	hd := b.head
	// DIMENSION 的 gold entity 键是全名（DIMENSION_LINEAR 等），内部短名
	// （DIM_LINEAR）需在此映射；公共 case 返回原始 typeName 不适用。
	if _, ok := ent.(*entDimension); ok && key == "entity" {
		return dimGoldEntityName(b.typeName)
	}
	// UNKNOWN_ENT 兜底实体的 gold entity 键强制 UNKNOWN_ENT（b.typeName
	// 保留原始动态类名如 ACAD_TABLE，fillMeta 覆盖后无法直接输出兜底名）。
	if _, ok := ent.(*entUnknownEnt); ok && key == "entity" {
		return "UNKNOWN_ENT"
	}
	// LIGHT 的 spec 自带 type 字段（70 组码）在 gold JSON 中覆盖顶层类型码键
	// （out_json 顶层先输出 type，专有字段同名后写覆盖），故 type 键返回
	// 光类型值而非流内类型码；MULTILEADER 同理（BS 170 的 type 字段）。
	if l, ok := ent.(*entLight); ok && key == "type" {
		return int64(l.lightType)
	}
	if m, ok := ent.(*entMLeader); ok && key == "type" {
		return int64(m.mleaderType)
	}
	// OLE2FRAME 实体内 BS type 与公共实体 type 同名，gold JSON 重复键
	// 后写覆盖为实体内的 type 值（如 2=Embedded）
	if o, ok := ent.(*entOle2Frame); ok && key == "type" {
		return int64(o.oleType)
	}
	if strings.HasPrefix(key, "eed[") {
		return eedFieldValue(b.eed, key)
	}
	switch key {
	case "handle":
		return b.handle
	case "entmode":
		return int64(b.mode)
	case "color":
		return colorAuditValue(b.color)
	case "color.index":
		// gold color 为嵌套对象时（R2007+ CMC）展平出 color.index 等子键；
		// gold 仅在 index 非 0 时输出该键，index=0（ByBlock）返回 nil
		if b.color.hasIndex && b.color.index != 0 {
			return int64(b.color.index)
		}
		return nil
	case "color.rgb":
		// gold 的 rgb 为 %06x 完整值（LibreDWG 不截断：32 位值含首字节
		// 索引/alpha 时输出 8 位 hex，纯 24 位时 6 位）
		return fmt.Sprintf("%06x", b.color.trueColor)
	case "color.flag":
		// gold 仅在 flag 非 0 时输出该键
		if b.color.flag != 0 {
			return int64(b.color.flag)
		}
		return nil
	case "color.alpha_raw":
		if b.color.hasAlpha {
			return int64(b.color.alphaRaw)
		}
		return nil
	case "color.alpha_type":
		if b.color.hasAlpha {
			return int64(b.color.alphaType)
		}
		return nil
	case "color.alpha":
		if b.color.hasAlpha {
			return int64(b.color.alpha)
		}
		return nil
	case "bitsize":
		return float64(b.objSizeBit)
	case "size":
		return float64(b.recSize)
	case "entity":
		if b.typeName != "" {
			// gold 对标注类实体输出 DIMENSION_ 前缀（内部类型名为 DIM_*）
			if strings.HasPrefix(b.typeName, "DIM_") {
				return "DIMENSION_" + b.typeName[4:]
			}
			return b.typeName
		}
		return nil
	case "type":
		// gold 的 type 键是数字类号（如 LINE=19），类名字符串只通过 entity 键导出
		if b.typeCode > 0 {
			return int64(b.typeCode)
		}
		return nil
	case "nolinks":
		return b2int(b.nolinks)
	case "isbylayerlt":
		return b2int(b.isbylayerlt)
	case "preview_exists":
		return b2int(b.previewExists)
	case "preview_is_proxy":
		// LibreDWG 由 proxy 类（is_zombie）推导；常规解码恒 0
		if b.previewExists {
			return int64(0)
		}
		return nil
	case "preview_size":
		if hd != nil && b.previewExists && hd.preview != nil {
			return int64(len(hd.preview))
		}
		return nil
	case "preview":
		if hd != nil && b.previewExists && hd.preview != nil {
			return fmt.Sprintf("%X", hd.preview)
		}
		return nil
	}
	if hd != nil {
		switch key {
		case "ltype_scale":
			return hd.ltypeScale
		case "ltype_flags":
			return int64(hd.ltypeFlags)
		case "plotstyle_flags":
			return int64(hd.plotstyleFlgs)
		case "material_flags":
			return int64(hd.materialFlags)
		case "invisible":
			return int64(hd.invisible)
		case "linewt":
			return int64(hd.linewt)
		case "shadow_flags":
			return int64(hd.shadowFlags)
		case "has_full_visualstyle":
			return b2int(hd.visualStyle[0])
		case "has_face_visualstyle":
			return b2int(hd.visualStyle[1])
		case "has_edge_visualstyle":
			return b2int(hd.visualStyle[2])
		case "is_xdic_missing":
			return b2int(hd.xdicMissing)
		case "has_ds_data":
			return b2int(hd.hasDsBinary)
		// 公共 handle 流次级句柄（批次 B 建模；gold 句柄为 0 时不输出键）
		case "ownerhandle":
			if b.owner != 0 {
				return b.owner
			}
			return nil
		case "layer":
			if b.layer != 0 {
				return b.layer
			}
			return nil
		case "prev_entity":
			if hd.prevEntity != 0 {
				return hd.prevEntity
			}
			return nil
		case "next_entity":
			if hd.nextEntity != 0 {
				return hd.nextEntity
			}
			return nil
		case "xdicobjhandle":
			if hd.xdicObjHandle != 0 {
				return hd.xdicObjHandle
			}
			return nil
		case "plotstyle":
			if hd.plotstyleHandle != 0 {
				return hd.plotstyleHandle
			}
			return nil
		}
	}
	if b.extra != nil {
		if v, ok := b.extra[key]; ok {
			return v
		}
	}
	// DIMENSION 族：body 标量键与 dwg.spec 字段一一对应。
	if d, ok := ent.(*entDimension); ok {
		switch key {
		case "entity":
			return dimGoldEntityName(b.typeName)
		// 公共点组与句柄系（批次 B 补齐；ORDINATE 专属点组为
		// def_pt(10)/feature_location_pt(13)/leader_endpt(14)，与
		// readDimSpecific 的 dimLayoutOrdinate 布局一一对应）
		case "def_pt":
			// def_pt（10 组码）：ARC_DIMENSION 的专用 defPt 优先（与
			// point10 同源）；ANG2LN 为 2RD 形态（p16x/p16y，R13~R2018
			// trace 实证 clone_ins_pt 后紧跟 def_pt 2RD）
			if b.typeName == "DIM_ANG2LN" {
				return []float64{d.point16x, d.p16y}
			}
			if d.defPt != (point3{}) {
				return point3Arr(d.defPt)
			}
			return point3Arr(d.point10)
		case "feature_location_pt":
			if b.typeName == "DIM_ORDINATE" {
				return point3Arr(d.point13)
			}
			return nil
		case "leader_endpt":
			if b.typeName == "DIM_ORDINATE" {
				return point3Arr(d.point14)
			}
			return nil
		case "xline1start_pt":
			if b.typeName == "DIM_ANG2LN" {
				return point3Arr(d.point13)
			}
			return nil
		case "xline1end_pt":
			if b.typeName == "DIM_ANG2LN" {
				return point3Arr(d.point14)
			}
			return nil
		case "xline2start_pt":
			if b.typeName == "DIM_ANG2LN" {
				return point3Arr(d.point15)
			}
			return nil
		case "xline2end_pt":
			if b.typeName == "DIM_ANG2LN" {
				return point3Arr(d.point10)
			}
			return nil
		case "xline1_pt":
			if b.typeName != "DIM_ORDINATE" && b.typeName != "DIM_ANG2LN" {
				return point3Arr(d.point13)
			}
			return nil
		case "xline2_pt":
			if b.typeName != "DIM_ORDINATE" && b.typeName != "DIM_ANG2LN" {
				return point3Arr(d.point14)
			}
			return nil
		case "clone_ins_pt":
			// clone_ins_pt（2RD，全维度族公共）：解码侧存入 insertPoint 的
			// x/y（gold 形态为二元组）
			if d.hasInsertPoint {
				return []float64{d.insertPoint.x, d.insertPoint.y}
			}
			return nil
		case "center_pt":
			// ARC_DIMENSION 中心点（15 组码，dimLayoutArc 第三点）
			if b.typeName == "ARC_DIMENSION" {
				return point3Arr(d.point15)
			}
			return nil
		case "leader1_pt":
			return point3Arr(d.leader1Pt)
		case "leader2_pt":
			return point3Arr(d.leader2Pt)
		case "text_midpt":
			return point3Arr(d.textMidpoint)
		case "extrusion":
			return vec3Arr(d.extrusion)
		case "ins_scale":
			return vec3Arr(d.insertScale)
		case "dimstyle":
			if d.dimstyleHandle != 0 {
				return d.dimstyleHandle
			}
			return nil
		case "block":
			if d.anonymousBlock != 0 {
				return d.anonymousBlock
			}
			return nil
		case "class_version":
			return int64(d.classVersion)
		case "elevation":
			return d.elevation
		case "flag":
			return int64(d.dimFlag)
		case "flag1":
			return int64(d.dimFlags)
		case "flag2":
			return int64(d.flag2)
		case "user_text":
			return d.userText
		case "text_rotation":
			return d.textRotation
		case "horiz_dir":
			return d.horizontalDir
		case "ins_rotation":
			return d.insertRotation
		case "attachment":
			return int64(d.attachmentPoint)
		case "lspace_style":
			return int64(d.lineSpacingStyle)
		case "lspace_factor":
			return d.lineSpacingFactor
		case "act_measurement":
			return d.actualMeasurement
		case "unknown":
			return b2int(d.unknownFlag)
		case "flip_arrow1":
			return b2int(d.flipArrow1)
		case "flip_arrow2":
			return b2int(d.flipArrow2)
		case "oblique_angle":
			return d.extLineRotation
		case "dim_rotation":
			return d.dimRotation
		case "is_partial":
			return b2int(d.isPartial)
		case "arc_start_param":
			return d.arcStartParam
		case "arc_end_param":
			return d.arcEndParam
		case "has_leader":
			return b2int(d.hasLeader)
		case "leader_len":
			return d.leaderLen
		}
	}
	switch e := ent.(type) {
	case *entLine:
		switch key {
		case "start":
			return point3Arr(e.start)
		case "end":
			return point3Arr(e.end)
		}
	case *entCircle:
		switch key {
		case "center":
			return point3Arr(e.center)
		case "radius":
			return e.radius
		}
	case *entArc:
		switch key {
		case "center":
			return point3Arr(e.center)
		case "radius":
			return e.radius
		case "start_angle":
			return e.angleStart
		case "end_angle":
			return e.angleEnd
		}
	case *entPoint:
		switch key {
		case "location":
			return point3Arr(e.location)
		case "rotation", "x_ang":
			// gold 的 x_ang 即 x 轴角度（解码侧 rotation）
			return e.rotation
		case "x":
			return e.location.x
		case "y":
			return e.location.y
		case "z":
			return e.location.z
		}
	case *entEllipse:
		switch key {
		case "center":
			return point3Arr(e.center)
		case "major_axis", "sm_axis":
			// gold 键名 sm_axis（本库自有口径 major_axis 双键导出）
			return vec3Arr(e.majorAxis)
		case "extrusion":
			return vec3Arr(e.extrusion)
		case "ratio", "axis_ratio":
			// gold 键名为 axis_ratio（流内 BD 40 原值，无倒数换算）
			return e.ratio
		case "start_angle":
			return e.startAng
		case "end_angle":
			return e.endAng
		}
	case *entLwPolyline:
		if idx, rest, ok := auditArrayIndex(key, "widths"); ok {
			// gold 的 widths 为 {start,end} 对象数组，展平出 widths[i].start/end
			if idx >= 0 && idx < len(e.widths) {
				switch rest {
				case "start":
					return e.widths[idx].start
				case "end":
					return e.widths[idx].end
				}
			}
			return nil
		}
		switch key {
		case "flags", "flag":
			// gold 的 LWPOLYLINE 标志键名为 flag
			return int64(e.flags)
		case "elevation":
			// flag&8 时输出标高；无标志段 gold 也不输出该键（返回 0 不参与）
			return e.elevation
		case "const_width":
			// flag&4 时的常量宽度（同 elevation 模式）
			return e.constWidth
		case "thickness":
			// flag&2 时的厚度（同 elevation 模式）
			return e.thickness
		case "vertices", "points":
			// gold 键名 points（展平 [[x,y],...] 嵌套形态由测试侧展开）
			return pt2Arr(e.vertices)
		case "bulges":
			return f64Arr(e.bulges)
		case "vertexids":
			return nil // gold 顶点索引数组（本库未建模）
		case "widths":
			return nil
		}
	case *entText:
		switch key {
		case "text", "text_value":
			// gold 的 text_value 即显示文本
			return e.text
		case "insertion", "ins_pt":
			// gold 的 ins_pt 即插入点
			return point3Arr(e.insertion)
		case "height":
			return e.height
		case "rotation":
			return e.rotation
		case "h_align", "horiz_alignment":
			// gold 的 horiz_alignment 即水平对齐（解码侧 hAlign）
			return int64(e.hAlign)
		case "v_align", "vert_alignment":
			// gold 的 vert_alignment 即垂直对齐（解码侧 vAlign）
			return int64(e.vAlign)
		case "generation":
			return int64(e.gen)
		case "alignment_pt":
			if e.alignPt != nil {
				return point2Arr(*e.alignPt)
			}
			return nil
		case "extrusion":
			return vec3Arr(e.extrusion)
		case "style":
			// 文本样式句柄（handle 流专有段首项；gold 句柄为 0 时不输出键）
			if e.styleHandle != 0 {
				return e.styleHandle
			}
			return nil
		}
	case *entMText:
		switch key {
		case "insertion", "ins_pt":
			// gold 的 ins_pt 即插入点
			return point3Arr(e.insertion)
		case "x_axis_dir":
			return vec3Arr(e.xAxisDir)
		case "rect_width":
			return e.rectWidth
		case "text_height":
			return e.textHeight
		case "attachment":
			return int64(e.attachment)
		case "extrusion":
			return vec3Arr(e.extrusion)
		case "style":
			// 文本样式句柄（handle 流专有段；gold 句柄为 0 时不输出键）
			if e.styleHandle != 0 {
				return e.styleHandle
			}
			return nil
		case "text":
			// gold 输出经 bit_TV_to_utf8 的 \U+XXXX 展开（AutoCAD 内联转义）
			return expandUnicodeEscapes(e.text)
		}
	case *entInsert:
		switch key {
		case "insertion", "position", "ins_pt":
			// gold 的 ins_pt 即插入点
			return point3Arr(e.position)
		case "scale":
			return vec3Arr(e.scale)
		case "rotation":
			return e.rotation
		case "block_header":
			return e.blockHeader
		case "extrusion":
			return vec3Arr(e.extrusion)
		case "seqend":
			// SEQEND 结束句柄（attribs 后；has_attribs=0 时无该句柄）
			if e.seqend != 0 {
				return e.seqend
			}
			return nil
		case "scale_flag":
			if v, ok := e.extra["scale_flag"]; ok {
				return v
			}
			return nil
		case "has_attribs":
			if v, ok := e.extra["has_attribs"]; ok {
				return v
			}
			return nil
		case "attribs":
			// 关联 ATTRIB 句柄链（ParseJSON 侧经 linkJSONAttribs 回填，
			// DWG 侧 R2004+ 为 owned 句柄、R13~R2000 为 first/last 对）
			out := make([]float64, 0, len(e.attribs))
			for _, ah := range e.attribs {
				out = append(out, float64(ah))
			}
			return out
		case "first_attrib":
			if len(e.attribs) > 0 {
				return e.attribs[0]
			}
			return nil
		case "last_attrib":
			if len(e.attribs) > 0 {
				return e.attribs[len(e.attribs)-1]
			}
			return nil
		}
	case *entAttrib:
		switch key {
		case "text", "text_value", "default_value":
			// ATTDEF 的 default_value 与 ATTRIB 的 text_value 同为显示文本
			return e.text
		case "prompt":
			// gold 仅 ATTDEF 有 prompt 键；ATTRIB 返回 nil 不比对
			if b.typeCode == 0x03 {
				return e.prompt
			}
			return nil
		case "tag":
			return e.tag
		case "insertion", "ins_pt":
			return point3Arr(e.insertion)
		case "height":
			return e.height
		case "rotation":
			return e.rotation
		case "horiz_alignment":
			return int64(e.hAlign)
		case "vert_alignment":
			return int64(e.vAlign)
		case "generation":
			return int64(e.gen)
		case "alignment_pt":
			if e.alignPt != nil {
				return point2Arr(*e.alignPt)
			}
			return nil
		case "extrusion":
			return vec3Arr(e.extrusion)
		case "style":
			// 文本样式句柄（handle 流专有段首项；gold 句柄为 0 时不输出键）
			if e.styleHandle != 0 {
				return e.styleHandle
			}
			return nil
		}
	case *entVertex2d:
		switch key {
		case "location", "position", "point":
			return point3Arr(e.position)
		case "bulge":
			return e.bulge
		case "flag":
			return int64(e.flags)
		case "id":
			// R2010+ 顶点标识符（BL0 spec 字段）；pre-R2010 样本 gold
			// 无 id 键，不会查询此 case
			return e.id
		case "tangent_dir":
			return e.tangentDir
		case "start_width":
			return e.startWidth
		case "end_width":
			return e.endWidth
		}
	case *entVertex3d:
		switch key {
		case "location", "position", "point":
			return point3Arr(e.position)
		case "flag":
			return int64(e.flags)
		}
	case *entVertexPface:
		switch key {
		case "flag":
			return int64(e.flag)
		case "location", "point":
			return point3Arr(e.position)
		}
	case *entVertexPfaceFace:
		switch key {
		case "flag":
			// flag 恒为 128，不从流读取（gold 同值输出）
			return int64(e.flag)
		case "vertind":
			return []float64{float64(e.vertind[0]), float64(e.vertind[1]), float64(e.vertind[2]), float64(e.vertind[3])}
		}
	case *entPolyline2d:
		switch key {
		case "flag":
			return int64(e.flags)
		case "curve_type":
			return int64(e.curveType)
		case "start_width":
			return e.widthStart
		case "end_width":
			return e.widthEnd
		case "thickness":
			return e.thickness
		case "elevation":
			return e.elevation
		case "extrusion":
			return vec3Arr(e.extrusion)
		case "first_vertex":
			if e.firstVertex != 0 {
				return e.firstVertex
			}
			return nil
		case "last_vertex":
			if e.lastVertex != 0 {
				return e.lastVertex
			}
			return nil
		case "vertex":
			// R2004+ owned 顶点句柄数组
			out := make([]float64, 0, len(e.ownedHandles))
			for _, vh := range e.ownedHandles {
				out = append(out, float64(vh))
			}
			return out
		case "seqend":
			if e.seqend != 0 {
				return e.seqend
			}
			return nil
		}
	case *entPolyline3d:
		switch key {
		case "flag":
			return int64(e.flags70)
		case "curve_type":
			return int64(e.flags75)
		case "first_vertex":
			if e.firstVertex != 0 {
				return e.firstVertex
			}
			return nil
		case "last_vertex":
			if e.lastVertex != 0 {
				return e.lastVertex
			}
			return nil
		case "vertex":
			out := make([]float64, 0, len(e.ownedHandles))
			for _, vh := range e.ownedHandles {
				out = append(out, float64(vh))
			}
			return out
		case "seqend":
			if e.seqend != 0 {
				return e.seqend
			}
			return nil
		}
	case *entPolylinePface:
		switch key {
		case "numverts":
			return int64(e.numVertices)
		case "numfaces":
			return int64(e.numFaces)
		case "first_vertex":
			// R13~R2000 handle 流首顶点句柄（R2004+ 为 owned 向量，无该键）
			if e.firstVertex != 0 {
				return e.firstVertex
			}
			return nil
		case "last_vertex":
			if e.lastVertex != 0 {
				return e.lastVertex
			}
			return nil
		case "vertex":
			// R2004+ owned 顶点句柄数组
			out := make([]float64, 0, len(e.ownedHandles))
			for _, vh := range e.ownedHandles {
				out = append(out, float64(vh))
			}
			return out
		case "seqend":
			if e.seqend != 0 {
				return e.seqend
			}
			return nil
		}
	case *entRay:
		switch key {
		case "start", "point":
			return point3Arr(e.start)
		case "direction", "vector":
			return vec3Arr(e.unitVector)
		}

	case *entBlockLike:
		switch key {
		case "name":
			// gold 仅 BLOCK 有 name 键（ENDBLK/SEQEND 无该键，返回 nil 不比对）
			if e.name != "" {
				return e.name
			}
			return nil
		}
	case *entSolid:
		switch key {
		case "corner1", "p1":
			return point2Arr(e.p1)
		case "corner2", "p2":
			return point2Arr(e.p2)
		case "corner3", "p3":
			return point2Arr(e.p3)
		case "corner4", "p4":
			return point2Arr(e.p4)
		case "thickness":
			return e.thickness
		case "elevation":
			return e.elevation
		case "extrusion":
			return vec3Arr(e.extrusion)
		}
	case *entFace3d:
		switch key {
		case "corner1", "p1":
			return point3Arr(e.p1)
		case "corner2", "p2":
			return point3Arr(e.p2)
		case "corner3", "p3":
			return point3Arr(e.p3)
		case "corner4", "p4":
			return point3Arr(e.p4)
		case "has_no_flags":
			return b2int(e.hasNoFlags)
		case "z_is_zero":
			return b2int(e.zIsZero)
		case "invis_flags":
			return int64(e.invisibleEdgeFlags)
		}
	case *entMLine:
		switch key {
		case "scale":
			return e.scale
		case "justification":
			return int64(e.justification)
		case "flags":
			return int64(e.openClosed)
		case "base_point":
			return point3Arr(e.basePoint)
		case "extrusion":
			return point3Arr(e.extrusion)
		case "mlinestyle":
			if e.styleHandle != 0 {
				return e.styleHandle
			}
			return nil
		}
		// 展平嵌套键：verts[i].vertex/vertex_direction/miter_direction 与
		// verts[i].lines[j].segparms/areafillparms（批次 B：按每线计数从
		// 扁平参数数组切片；DXF 来源无分组计数，返回 nil）
		if idx, rest, ok := auditArrayIndex(key, "verts"); ok {
			if idx < 0 || idx >= len(e.vertices) {
				return nil
			}
			v := &e.vertices[idx]
			switch rest {
			case "vertex":
				return point3Arr(v.position)
			case "vertex_direction":
				return point3Arr(v.direction)
			case "miter_direction":
				return point3Arr(v.miter)
			}
			if li, sub, ok := auditArrayIndex(rest, "lines"); ok {
				switch sub {
				case "segparms":
					return mlineLineParams(v.segParams, v.segCounts, li)
				case "areafillparms":
					return mlineLineParams(v.areaParams, v.areaCounts, li)
				}
			}
			return nil
		}
	case *entPolylineMesh:
		switch key {
		case "flag":
			return int64(e.flags)
		case "curve_type":
			return int64(e.curveType)
		case "m_density":
			return int64(e.mDensity)
		case "n_density":
			return int64(e.nDensity)
		}
	case *entSpline:
		// SPLINE 审计键（批次 B 补齐：主体标量 + 点/数组键；R2013+ 的
		// splineflags/knotparam 为位域合成口径，未导出）
		switch key {
		case "scenario":
			return int64(e.scenario)
		case "degree":
			return int64(e.degree)
		case "fit_tol":
			return e.fitTolerance
		case "knot_tol":
			return e.knotTolerance
		case "ctrl_tol":
			return e.ctrlTolerance
		case "knots":
			return f64Arr(e.knots)
		case "weights":
			return f64Arr(e.weights)
		case "fit_pts":
			return p3sFlat(e.fitPoints)
		case "rational":
			return b2int(e.rational)
		case "closed_b":
			return b2int(e.closed)
		case "periodic":
			return b2int(e.periodic)
		case "weighted":
			return b2int(e.weighted)
		}
		if idx, rest, ok := auditArrayIndex(key, "ctrl_pts"); ok {
			if idx < 0 || idx >= len(e.controlPoints) {
				return nil
			}
			switch rest {
			case "x":
				return e.controlPoints[idx].x
			case "y":
				return e.controlPoints[idx].y
			case "z":
				return e.controlPoints[idx].z
			}
			return nil
		}
		if key == "style" {
			// 样式句柄（handle 流，批次 B 建模）
			if e.styleHandle != 0 {
				return e.styleHandle
			}
			return nil
		}
	case *entShape:
		switch key {
		case "insertion":
			return point3Arr(e.insertion)
		case "scale":
			return e.scale
		case "rotation":
			return e.rotation
		case "width_factor":
			return e.widthFactor
		case "oblique", "oblique_angle":
			return e.oblique
		case "thickness":
			return e.thickness
		case "style_id":
			return int64(e.styleId)
		}
	case *entViewport:
		switch key {
		case "width":
			return e.width
		case "height":
			return e.height
		case "center":
			return point3Arr(e.center)
		case "view_target":
			return point3Arr(e.viewTarget)
		case "VIEWDIR":
			return vec3Arr(e.viewDir)
		case "VIEWCTR":
			return point2Arr(e.viewCtr)
		case "SNAPBASE":
			return point2Arr(e.snapBase)
		case "SNAPUNIT":
			return point2Arr(e.snapUnit)
		case "GRIDUNIT":
			return point2Arr(e.gridUnit)
		case "UCSORG":
			return point3Arr(e.ucsorg)
		case "UCSXDIR":
			return point3Arr(e.ucsxdir)
		case "UCSYDIR":
			return point3Arr(e.ucsydir)
		case "VIEWTWIST":
			return e.viewTwist
		case "VIEWSIZE":
			return e.viewSize
		case "LENSLENGTH":
			return e.lensLength
		case "FRONTZ":
			return e.frontZ
		case "BACKZ":
			return e.backZ
		case "SNAPANG":
			return e.snapAng
		case "circle_zoom":
			return int64(e.circleZoom)
		case "grid_major":
			return int64(e.gridMajor)
		case "status_flag":
			return int64(e.statusFlag)
		case "style_sheet":
			return e.styleSheet
		case "render_mode":
			return int64(e.renderMode)
		case "UCSVP":
			return b2int(e.ucsVP)
		case "ucs_at_origin":
			return b2int(e.ucsAtOrigin)
		case "ucs_elevation":
			return e.ucsElevation
		case "UCSORTHOVIEW":
			return int64(e.ucsOrthoView)
		case "shadeplot_mode":
			return int64(e.shadeplotMode)
		case "use_default_lights":
			return b2int(e.useDefaultLights)
		case "default_lighting_type":
			return int64(e.defaultLightingType)
		case "brightness":
			return e.brightness
		case "contrast":
			return e.contrast
		case "ambient_color.index":
			// 对齐 out_json field_cmc：流内 index=0 时按 method 从 rgb
			// 反查 ACI 调色板（如 0xc2333333 → 灰 250）
			if e.ambientIndex != 0 {
				return int64(e.ambientIndex)
			}
			return dwgFindColorIndex(e.ambientRGB)
		case "ambient_color.rgb":
			return fmt.Sprintf("%08x", e.ambientRGB)
		case "ambient_color.flag":
			return int64(0)
		}
	case *entTolerance:
		switch key {
		case "text_value":
			return e.text
		case "unknown_short":
			return int64(e.unknownShort)
		case "height":
			return e.height
		case "dimgap":
			return e.dimgap
		case "ins_pt", "insertion":
			return point3Arr(e.insertion)
		case "x_direction":
			return vec3Arr(e.xDirection)
		case "extrusion":
			return vec3Arr(e.extrusion)
		case "dimstyle":
			if e.dimstyle != 0 {
				return e.dimstyle
			}
			return nil
		}
	case *entLeader:
		switch key {
		case "annot_type", "annotation_type":
			return int64(e.annotationType)
		case "path_type":
			return int64(e.pathType)
		case "unknown_bit_1":
			return b2int(e.unknownBit1)
		case "arrowhead_on":
			return b2int(e.arrowheadOn)
		case "arrowhead_type":
			return int64(e.arrowheadType)
		case "box_height":
			return e.boxHeight
		case "box_width":
			return e.boxWidth
		case "hookline_dir":
			return b2int(e.hooklineDir)
		case "hookline_on":
			return b2int(e.hooklineOn)
		case "dimgap":
			return e.dimgap
		case "dimasz":
			return e.dimasz
		case "unknown_short_1":
			return int64(e.unknownShort1)
		case "byblock_color":
			return int64(e.byblockColor)
		case "unknown_bit_2":
			return b2int(e.unknownBit2)
		case "unknown_bit_3":
			return b2int(e.unknownBit3)
		case "unknown_bit_4":
			return b2int(e.unknownBit4)
		case "unknown_bit_5":
			return b2int(e.unknownBit5)
		}
	case *entLight:
		return lightAuditField(e, key)
	case *entMLeader:
		return mleaderAuditField(e, key)
	case *entHatch:
		return hatchAuditField(e, key)
	case *entAcis:
		return acisAuditField(e, key)
	case *entWipeout:
		return wipeoutAuditField(e, key)
	case *entImage:
		return imageAuditField(e, key)
	case *entOle2Frame:
		return ole2FrameAuditField(e, key)
	case *entOleFrame:
		return oleFrameAuditField(e, key)
	case *entProxyEntity:
		return proxyEntityAuditField(e, key)
	case *entUnderlay:
		return underlayAuditField(e, key)
	case *entMpolygon:
		return mpolygonAuditField(e, key)
	}
	return nil
}

// mlineLineParams 按每线计数从扁平参数数组切片出第 li 条样式线的参数
// （前缀和定位；计数缺失或越界返回 nil——DXF 来源无分组信息）。
func mlineLineParams(params []float64, counts []int, li int) []float64 {
	if li < 0 || li >= len(counts) {
		return nil
	}
	start := 0
	for i := 0; i < li; i++ {
		start += counts[i]
	}
	end := start + counts[li]
	if end > len(params) {
		return nil
	}
	return params[start:end]
}

// dimGoldEntityName 内部 DIM 短名映射为 gold JSON 的 DIMENSION 全名；
// 非 DIMENSION 类型原样返回。
func dimGoldEntityName(name string) string {
	const short = "DIM_"
	if len(name) > len(short) && name[:len(short)] == short {
		return "DIMENSION_" + name[len(short):]
	}
	return name
}

// mleaderCMCAuditValue CMC 键值导出（对齐 out_json field_cmc）：
// pre-R2004 gold 为标量索引数字（key 键本身），R2004+ 为对象展平子键
// ——index 仅反查结果非 0 时输出、rgb 恒为完整 32 位 %06x、flag 非 0 才输出。
func mleaderCMCAuditValue(c mleaderCMC, key string, scalarKey, prefix string) any {
	if !c.isTrue {
		if key == scalarKey {
			return int64(c.index)
		}
		return nil
	}
	switch key {
	case prefix + ".index":
		if c.index != 0 {
			return int64(c.index)
		}
	case prefix + ".rgb":
		return fmt.Sprintf("%06x", c.rgb)
	case prefix + ".flag":
		if c.flag != 0 {
			return int64(c.flag)
		}
	}
	return nil
}

// mleaderAuditField MULTILEADER 审计键导出：顶层标量键 + ctx.* 展平键
// （ctx.leaders[i]…/ctx.leaders[i].lines[j]…，与 gold JSON 展平口径一致）。
// 点/数组键（content_base、points、breaks、block_scale 等 gold 数组形态）
// 不参与比对，统一返回 nil。
func mleaderAuditField(m *entMLeader, key string) any {
	switch key {
	case "class_version":
		if m.hasVersion {
			return int64(m.classVersion)
		}
		return nil
	case "flags":
		return int64(m.flags)
	case "line_linewt":
		return int64(m.lineLinewt)
	case "has_landing":
		return b2int(m.hasLanding)
	case "has_dogleg":
		return b2int(m.hasDogleg)
	case "landing_dist":
		return m.landingDist
	case "arrow_size":
		return m.arrowSize
	case "style_content":
		return int64(m.styleContent)
	case "text_left":
		return int64(m.textLeft)
	case "text_right":
		return int64(m.textRight)
	case "text_angletype":
		return int64(m.textAngletype)
	case "text_alignment":
		return int64(m.textAlignment)
	case "has_text_frame":
		return b2int(m.hasTextFrame)
	case "block_rotation":
		return m.blockRotation
	case "style_attachment":
		return int64(m.styleAttachment)
	case "is_annotative":
		return b2int(m.isAnnotative)
	case "is_neg_textdir":
		return b2int(m.isNegTextdir)
	case "ipe_alignment":
		return int64(m.ipeAlignment)
	case "justification":
		return int64(m.justification)
	case "scale_factor":
		return m.scaleFactor
	case "attach_dir":
		return int64(m.attachDir)
	case "attach_top":
		return int64(m.attachTop)
	case "attach_bottom":
		return int64(m.attachBottom)
	case "is_text_extended":
		if m.hasVersion { // R2013b+ 键；gold 仅 R2013/2018 出现
			return b2int(m.isTextExtended)
		}
		return nil
	}
	// 顶层 CMC 双形态（line_color/text_color/block_color）
	for _, e := range []struct {
		cmc    mleaderCMC
		prefix string
	}{{m.lineColor, "line_color"}, {m.textColor, "text_color"}, {m.blockColor, "block_color"}} {
		if v := mleaderCMCAuditValue(e.cmc, key, e.prefix, e.prefix); v != nil {
			return v
		}
	}
	// 句柄系（批次 B 补齐：gold 句柄为 0 时不输出键）
	switch key {
	case "mleaderstyle":
		if m.mleaderStyle != 0 {
			return m.mleaderStyle
		}
		return nil
	case "arrow_handle":
		if m.arrowHandle != 0 {
			return m.arrowHandle
		}
		return nil
	case "text_style":
		if m.textStyle != 0 {
			return m.textStyle
		}
		return nil
	case "block_style":
		if m.blockStyle != 0 {
			return m.blockStyle
		}
		return nil
	case "line_ltype":
		if m.lineLtype != 0 {
			return m.lineLtype
		}
		return nil
	case "block_scale":
		return vec3Arr(m.blockScale)
	}
	// ctx.* 键
	if v, ok := mleaderCtxAuditField(m, key); ok {
		return v
	}
	return nil
}

// mleaderCtxAuditField ctx 展平键导出；ok=false 表示键不归属 ctx 段。
func mleaderCtxAuditField(m *entMLeader, key string) (any, bool) {
	c := &m.ctx
	switch key {
	case "ctx.num_leaders":
		return int64(c.numLeaders), true
	case "ctx.scale_factor":
		return c.scaleFactor, true
	case "ctx.text_height":
		return c.textHeight, true
	case "ctx.arrow_size":
		return c.arrowSize, true
	case "ctx.landing_gap":
		return c.landingGap, true
	case "ctx.text_left":
		return int64(c.textLeft), true
	case "ctx.text_right":
		return int64(c.textRight), true
	case "ctx.text_angletype":
		return int64(c.textAngletype), true
	case "ctx.text_alignment":
		return int64(c.textAlignment), true
	case "ctx.has_content_txt":
		return b2int(c.hasContentTxt), true
	case "ctx.has_content_blk":
		return b2int(c.hasContentBlk), true
	case "ctx.is_normal_reversed":
		return b2int(c.isNormalReversed), true
	case "ctx.text_top":
		if m.hasVersion {
			return int64(c.textTop), true
		}
		return nil, true
	case "ctx.text_bottom":
		if m.hasVersion {
			return int64(c.textBottom), true
		}
		return nil, true
	// ctx 点组（批次 B 补齐数组导出）
	case "ctx.base":
		return point3Arr(c.base), true
	case "ctx.base_dir":
		return point3Arr(c.baseDir), true
	case "ctx.base_vert":
		return point3Arr(c.baseVert), true
	case "ctx.content_base":
		return point3Arr(c.contentBase), true
	case "ctx.content.txt.normal":
		return point3Arr(c.txt.normal), true
	case "ctx.content.txt.location":
		return point3Arr(c.txt.location), true
	case "ctx.content.txt.direction":
		return point3Arr(c.txt.direction), true
	case "ctx.content.txt.style":
		if c.txt.styleHandle != 0 {
			return c.txt.styleHandle, true
		}
		return nil, true
	case "ctx.content.blk.block_table":
		if c.blk.blockTable != 0 {
			return c.blk.blockTable, true
		}
		return nil, true
	case "ctx.content.blk.normal":
		return point3Arr(c.blk.normal), true
	case "ctx.content.blk.location":
		return point3Arr(c.blk.location), true
	case "ctx.content.blk.scale":
		return vec3Arr(c.blk.scale), true
	case "ctx.content.blk.transform":
		return f64Arr(c.blk.transform[:]), true
	}
	if idx, rest, ok := auditArrayIndex(key, "ctx.leaders"); ok {
		if idx < 0 || idx >= len(c.leaders) {
			return nil, true
		}
		return mleaderNodeAuditField(m, &c.leaders[idx], rest), true
	}
	if strings.HasPrefix(key, "ctx.content.txt.") {
		return mleaderTxtAuditField(&c.txt, strings.TrimPrefix(key, "ctx.content.txt.")), true
	}
	if strings.HasPrefix(key, "ctx.content.blk.") {
		b := &c.blk
		switch strings.TrimPrefix(key, "ctx.content.blk.") {
		case "rotation":
			return b.rotation, true
		case "color.rgb":
			// CMC rgb 完整 32 位（对齐 gold %08x/%06x 双形态）
			return fmt.Sprintf("%06x", b.color.rgb), true
		case "color.index":
			return int64(b.color.index), true
		}
		// normal/location/scale/transform 为数组键，block_table 为句柄键
		return nil, true
	}
	// blocklabels[i].*（R14-R2007 块标签数组，multileaders 真实样本实证）
	if idx, rest, ok := auditArrayIndex(key, "blocklabels"); ok {
		if idx < 0 || idx >= len(m.blocklabels) {
			return nil, true
		}
		bl := &m.blocklabels[idx]
		switch rest {
		case "label_text":
			return bl.labelText, true
		case "ui_index":
			return int64(bl.uiIndex), true
		case "width":
			return bl.width, true
		}
		return nil, true // attdef 句柄键
	}
	return nil, false
}

// mleaderNodeAuditField ctx.leaders[i] 展平键导出（rest 为前缀后的子键）。
func mleaderNodeAuditField(m *entMLeader, n *mleaderNode, rest string) any {
	switch rest {
	case "has_lastleaderlinepoint":
		return b2int(n.hasLastLeaderLinePoint)
	case "has_dogleg":
		return b2int(n.hasDogleg)
	case "branch_index":
		return int64(n.branchIndex)
	case "dogleg_length":
		return n.doglegLength
	case "lastleaderlinepoint":
		if n.hasLastLeaderLinePoint {
			return point3Arr(n.lastLeaderLinePoint)
		}
		return nil
	case "dogleg_vector":
		if n.hasDogleg {
			return point3Arr(n.doglegVector)
		}
		return nil
	case "attach_dir":
		if m.hasVersion {
			return int64(n.attachDir)
		}
		return nil
	}
	if idx, sub, ok := auditArrayIndex(rest, "lines"); ok {
		if idx < 0 || idx >= len(n.lines) {
			return nil
		}
		line := &n.lines[idx]
		switch sub {
		case "line_index":
			return int64(line.lineIndex)
		case "type":
			if m.hasVersion {
				return int64(line.mleaderType)
			}
			return nil
		case "linewt":
			if m.hasVersion {
				return int64(line.linewt)
			}
			return nil
		case "arrow_size":
			if m.hasVersion {
				return line.arrowSize
			}
			return nil
		case "flags":
			if m.hasVersion {
				return int64(line.flags)
			}
			return nil
		case "points":
			return p3sFlat(line.points)
		case "ltype":
			if m.hasVersion && line.ltype != 0 {
				return line.ltype
			}
			return nil
		case "arrow_handle":
			if m.hasVersion && line.arrowHandle != 0 {
				return line.arrowHandle
			}
			return nil
		}
		if m.hasVersion && strings.HasPrefix(sub, "color") {
			// R2010b+ 的 lline.color：R2004+ 结构下展平出 color.rgb 等子键
			return mleaderCMCAuditValue(line.color, sub, "color", "color")
		}
		return nil
	}
	return nil // num_breaks/num_lines/points/breaks 数组或计数键
}

// mleaderTxtAuditField ctx.content.txt 展平键导出。
func mleaderTxtAuditField(t *mleaderTxtContent, rest string) any {
	switch rest {
	case "default_text":
		return t.defaultText
	case "rotation":
		return t.rotation
	case "width":
		return t.width
	case "height":
		return t.height
	case "line_spacing_factor":
		return t.lineSpacingFactor
	case "line_spacing_style":
		return int64(t.lineSpacingStyle)
	case "alignment":
		return int64(t.alignment)
	case "flow":
		return int64(t.flow)
	case "bg_scale":
		return t.bgScale
	case "bg_transparency":
		return int64(t.bgTransparency)
	case "is_bg_fill":
		return b2int(t.isBgFill)
	case "is_bg_mask_fill":
		return b2int(t.isBgMaskFill)
	case "col_type":
		return int64(t.colType)
	case "is_height_auto":
		return b2int(t.isHeightAuto)
	case "col_width":
		return t.colWidth
	case "col_gutter":
		return t.colGutter
	case "is_col_flow_reversed":
		return b2int(t.isColFlowReversed)
	case "num_col_sizes":
		return int64(t.numColSizes)
	case "word_break":
		return b2int(t.wordBreak)
	case "unknown":
		return b2int(t.unknown)
	}
	if strings.HasPrefix(rest, "color") {
		return mleaderCMCAuditValue(t.color, rest, "color", "color")
	}
	if strings.HasPrefix(rest, "bg_color") {
		return mleaderCMCAuditValue(t.bgColor, rest, "bg_color", "bg_color")
	}
	return nil // normal/location/direction/col_sizes/style 数组或句柄键
}

// lightAuditField LIGHT 审计键导出：基线标量键 + light_color CMC 子键。
// light_color 双形态：pre-R2004 gold 为标量索引数字（light_color 键），
// R2004+ gold 为对象展平出的 light_color.index/.rgb/.flag 子键——
// out_json 的 index 键仅在反查结果非 0 时输出，rgb 恒为完整 32 位
// rgb（含 method 高字节）的 %06x 形态，flag 仅非 0 时输出。
func lightAuditField(l *entLight, key string) any {
	switch key {
	case "class_version":
		return int64(l.classVersion)
	case "name":
		return l.name
	case "status":
		return b2int(l.status)
	case "light_color":
		if !l.hasLightColorTrue {
			return int64(l.lightColorIndex)
		}
		return nil
	case "light_color.index":
		if l.hasLightColorTrue && l.lightColorIndex != 0 {
			return int64(l.lightColorIndex)
		}
		return nil
	case "light_color.rgb":
		if l.hasLightColorTrue {
			return fmt.Sprintf("%06x", l.lightColorRGB)
		}
		return nil
	case "light_color.flag":
		if l.hasLightColorTrue && l.lightColorFlag != 0 {
			return int64(l.lightColorFlag)
		}
		return nil
	case "plot_glyph":
		return b2int(l.plotGlyph)
	case "intensity":
		return l.intensity
	case "attenuation_type":
		return int64(l.attenuationType)
	case "use_attenuation_limits":
		return b2int(l.useAttenuationLimits)
	case "attenuation_start_limit":
		return l.attenuationStart
	case "attenuation_end_limit":
		return l.attenuationEnd
	case "hotspot_angle":
		return l.hotspotAngle
	case "falloff_angle":
		return l.falloffAngle
	case "cast_shadows":
		return b2int(l.castShadows)
	case "shadow_type":
		return int64(l.shadowType)
	case "shadow_map_size":
		return int64(l.shadowMapSize)
	case "shadow_map_softness":
		return int64(l.shadowMapSoftness)
	case "position":
		return point3Arr(l.position)
	case "target":
		return point3Arr(l.target)
	}
	return nil // light_color 数组形态等不参与比对
}

// acisAuditField ACIS 系（REGION/3DSOLID/BODY）审计键导出。
func acisAuditField(a *entAcis, key string) any {
	switch key {
	case "acis_empty":
		return b2int(a.acisEmpty)
	case "acis_empty_bit":
		return b2int(a.acisEmptyBit)
	case "unknown":
		return int64(a.unknown)
	case "version":
		return int64(a.version)
	case "wireframe_data_present":
		return b2int(a.wireframeDataPresent)
	case "point_present":
		return b2int(a.pointPresent)
	case "isolines":
		return int64(a.isolines)
	case "isoline_present":
		return b2int(a.isolinePresent)
	case "num_wires", "num_silhouettes":
		return nil // 仅 isoline_present=1 时写入 gold，数组宿主键无对照价值
	case "history_id":
		// history_id 句柄（R2004+ handle 流公共序后首项）：gold 非空为
		// [code,size,abs,abs] 句柄数组、NULL 为 [0,0] 二元数组，导出侧
		// 按同构形态输出（非空→句柄数字，NULL→[0,0]；R13/R14 无该键
		// 不对照，输出 [0,0] 无影响）
		if a.historyId != 0 {
			return a.historyId
		}
		return []float64{0, 0}
	}
	// wires[i].* 与 silhouettes[i].wires[j].* 标量键（flattenGold 展开）
	if v, ok := acisWireField(a.wires, key); ok {
		return v
	}
	if rest, ok := strings.CutPrefix(key, "materials["); ok {
		i := strings.Index(rest, "]")
		if i > 0 {
			idx, e := strconv.Atoi(rest[:i])
			if e == nil && idx < len(a.materials) {
				switch rest[i+2:] {
				case "array_index":
					return int64(a.materials[idx].ArrayIndex)
				case "mat_absref":
					return int64(a.materials[idx].MatAbsref)
				}
			}
		}
	}
	if rest, ok := strings.CutPrefix(key, "silhouettes["); ok {
		i := strings.Index(rest, "]")
		if i > 0 {
			idx, e := strconv.Atoi(rest[:i])
			if e == nil && idx < len(a.silhouettes) {
				sil := a.silhouettes[idx]
				restKey := rest[i+2:]
				switch restKey {
				case "vp_id":
					return int64(sil.VpID)
				case "vp_perspective":
					return b2int(sil.VpPerspective)
				case "has_wires":
					return b2int(sil.HasWires)
				}
				if v, ok := acisWireField(sil.Wires, restKey); ok {
					return v
				}
			}
		}
	}
	switch key {
	case "has_revision_guid":
		return b2int(a.hasRevisionGuid)
	case "revision_major":
		return int64(a.revisionMajor)
	case "revision_minor1":
		return int64(a.revisionMinor1)
	case "revision_minor2":
		return int64(a.revisionMinor2)
	case "revision_bytes":
		return fmt.Sprintf("%X", a.revisionBytes)
	case "end_marker":
		return int64(a.endMarker)
	case "point":
		// point_present=1 时的参考点（COMMON_3DSOLID）
		if a.pointPresent {
			return point3Arr(a.point)
		}
		return nil
	}
	return nil // acis_data/encr_sat_data/history_id 等数组或句柄键（acis_data 由 roundtrip 字节对照覆盖）
}

// acisWireField 按 "wires[i].field"（或宿主注入后的 "[i].field"）键取
// 线框标量；points/变换矩阵等数组键返回 ok=false。
func acisWireField(wires []acisWire, key string) (any, bool) {
	rest, ok := strings.CutPrefix(key, "wires[")
	if !ok {
		return nil, false
	}
	i := strings.Index(rest, "]")
	if i <= 0 {
		return nil, false
	}
	idx, e := strconv.Atoi(rest[:i])
	if e != nil || idx >= len(wires) {
		return nil, false
	}
	w := wires[idx]
	switch rest[i+2:] {
	case "type":
		return int64(w.Type), true
	case "selection_marker":
		return int64(w.SelectionMarker), true
	case "color":
		return w.Color, true
	case "acis_index":
		return int64(w.AcisIndex), true
	case "transform_present":
		return b2int(w.TransformPresent), true
	case "has_rotation":
		return b2int(w.HasRotation), true
	case "has_reflection":
		return b2int(w.HasReflection), true
	case "has_shear":
		return b2int(w.HasShear), true
	}
	return nil, false // points/axis_* 数组键
}

// wipeoutAuditField WIPEOUT 审计键导出（IMAGE 布局字段）。
func wipeoutAuditField(w *entWipeout, key string) any {
	switch key {
	case "class_version":
		return int64(w.classVersion)
	case "display_props":
		return int64(w.displayProps)
	case "clipping":
		return b2int(w.clipping)
	case "brightness":
		return int64(w.brightness)
	case "contrast":
		return int64(w.contrast)
	case "fade":
		return int64(w.fade)
	case "clip_boundary_type":
		return int64(w.clipBoundaryType)
	case "clip_mode":
		return int64(w.clipMode)
	case "pt0":
		return point3Arr(w.pt0)
	case "uvec":
		return vec3Arr(w.uvec)
	case "vvec":
		return vec3Arr(w.vvec)
	case "image_size":
		return []float64{w.imageSize.x, w.imageSize.y}
	case "clip_verts":
		out := make([]float64, 0, len(w.clipVerts)*2)
		for _, cv := range w.clipVerts {
			out = append(out, cv.x, cv.y)
		}
		return out
	}
	return nil // imagedef 等句柄键
}

// imageAuditField IMAGE 审计键导出（与 WIPEOUT 同布局，标量键同集合；
// pt0/uvec/vvec/image_size/clip_verts/imagedef 为数组或句柄键，
// gold 展平口径不进入标量对照）。
func imageAuditField(img *entImage, key string) any {
	switch key {
	case "class_version":
		return int64(img.classVersion)
	case "display_props":
		return int64(img.displayProps)
	case "clipping":
		return b2int(img.clipping)
	case "brightness":
		return int64(img.brightness)
	case "contrast":
		return int64(img.contrast)
	case "fade":
		return int64(img.fade)
	case "clip_boundary_type":
		return int64(img.clipBoundaryType)
	case "clip_mode":
		return int64(img.clipMode)
	case "pt0":
		return point3Arr(img.pt0)
	case "uvec":
		return vec3Arr(img.uvec)
	case "vvec":
		return vec3Arr(img.vvec)
	case "image_size":
		return []float64{img.imageSize.x, img.imageSize.y}
	case "clip_verts":
		out := make([]float64, 0, len(img.clipVerts)*2)
		for _, cv := range img.clipVerts {
			out = append(out, cv.x, cv.y)
		}
		return out
	case "imagedef":
		if img.imageDef != 0 {
			return img.imageDef
		}
		return nil
	case "imagedefreactor":
		if img.imageDefReactor != 0 {
			return img.imageDefReactor
		}
		return nil
	}
	return nil
}

// ole2FrameAuditField OLE2FRAME 审计键导出（gold 标量键 mode/type；
// data 为超长 hex 字符串，由对照测试直接断言，此处不重复导出）。
func ole2FrameAuditField(o *entOle2Frame, key string) any {
	switch key {
	case "type":
		return int64(o.oleType)
	case "mode":
		return int64(o.mode)
	case "lock_aspect":
		return int64(o.lockAspect)
	case "data":
		return fmt.Sprintf("%X", o.data)
	}
	return nil
}

// oleFrameAuditField OLEFRAME 审计键导出。
func oleFrameAuditField(o *entOleFrame, key string) any {
	switch key {
	case "flag":
		return int64(o.flag)
	case "mode":
		return int64(o.mode)
	case "data":
		return fmt.Sprintf("%X", o.data)
	}
	return nil
}

// proxyEntityAuditField PROXY_ENTITY 审计键导出（gold 标量键口径；
// proxy_data/data/objids 为数组键不进入标量对照，data_numbits 为
// LibreDWG DXF/JSON 导出键）。
func proxyEntityAuditField(p *entProxyEntity, key string) any {
	switch key {
	case "proxy_id":
		return int64(p.proxyID)
	case "version":
		return int64(p.version)
	case "maint_version":
		return int64(p.maintVersion)
	case "dwg_version":
		return int64(p.dwgVersionNum)
	case "from_dxf":
		return b2int(p.fromDxf)
	case "data_numbits":
		return int64(p.dataNumBits)
	case "num_objids":
		return int64(p.numObjids)
	case "proxy_data_size":
		return int64(p.proxyDataSize)
	}
	return nil
}

// mpolygonAuditField MPOLYGON 审计键导出：主体标量键（含 HATCH 同构的
// 渐变/图案字段）+ 路径展平键复用 HATCH 路径导出。
func mpolygonAuditField(m *entMpolygon, key string) any {
	switch key {
	case "style":
		return int64(m.style)
	case "style_tail":
		return int64(m.styleTail)
	case "x_dir":
		return []float64{m.xDir.x, m.xDir.y}
	}
	if v := hatchAuditField(m.hatch, key); v != nil {
		return v
	}
	// 展平嵌套键：paths[i]…（复用 HATCH 路径导出）
	if idx, rest, ok := auditArrayIndex(key, "paths"); ok {
		if idx < 0 || idx >= len(m.hatch.paths) {
			return nil
		}
		return hatchPathAuditField(&m.hatch.paths[idx], rest)
	}
	return nil
}

// hatchAuditField HATCH 审计键导出：主体标量键 + 路径/定义线的
// 展平嵌套键（paths[i].flag、paths[i].segs[j].curve_type、
// paths[i].polyline_paths[k].bulge、deflines[m].angle 等，与 gold
// JSON 展平口径一致）。数组键（knots/points/dashes 等）返回 nil，
// 由审计侧按非标量跳过。
func hatchAuditField(h *entHatch, key string) any {
	switch key {
	case "elevation":
		return h.elevation
	case "extrusion":
		return vec3Arr(h.extrusion)
	case "name":
		return h.name
	case "is_solid_fill":
		return b2int(h.solidFill)
	case "is_associative":
		return b2int(h.associative)
	case "style":
		return int64(h.style)
	case "pattern_type":
		return int64(h.patternType)
	case "angle":
		return h.angle
	case "scale_spacing":
		return h.scaleSpacing
	case "double_flag":
		return b2int(h.doubleFlag)
	case "is_gradient_fill":
		return int64(h.isGradientFill)
	case "reserved":
		return int64(h.reserved)
	case "gradient_angle":
		return h.gradientAngle
	case "gradient_shift":
		return h.gradientShift
	case "single_color_gradient":
		return int64(h.singleColorGradient)
	case "gradient_tint":
		return h.gradientTint
	case "gradient_name":
		return h.gradientName
	case "has_derived":
		return b2int(h.hasDerived)
	case "pixel_size":
		if h.hasDerived {
			return h.pixelSize
		}
		return nil // gold 仅 has_derived=1 时输出
	case "seeds":
		// 种子点数组（gold 形态 [[x,y],...]，2RD 对）
		out := make([][]float64, 0, len(h.seeds))
		for _, s := range h.seeds {
			out = append(out, []float64{s.x, s.y})
		}
		return out
	case "num_seeds", "deflines", "paths", "num_paths":
		return nil // 数组/计数码，审计按非标量跳过或无对照价值
	}
	if rest, ok := strings.CutPrefix(key, "colors["); ok {
		i := strings.Index(rest, "]")
		if i > 0 {
			idx, e := strconv.Atoi(rest[:i])
			if e == nil && idx >= 0 && idx < len(h.colors) {
				gc := h.colors[idx]
				switch rest[i+2:] {
				case "shift_value":
					return gc.shiftValue
				case "color.index":
					return gc.colorIndex
				case "color.rgb":
					return gc.colorRGB
				}
			}
		}
		return nil
	}
	// 展平嵌套键：paths[i]… / deflines[i]…
	if idx, rest, ok := auditArrayIndex(key, "paths"); ok {
		if idx < 0 || idx >= len(h.paths) {
			return nil
		}
		return hatchPathAuditField(&h.paths[idx], rest)
	}
	if idx, rest, ok := auditArrayIndex(key, "deflines"); ok {
		if idx < 0 || idx >= len(h.deflines) {
			return nil
		}
		return hatchDefLineAuditField(&h.deflines[idx], rest)
	}
	return nil
}

// hatchPathAuditField HATCH 单条路径的展平键导出（rest 为 paths[i]. 之后的子键）。
func hatchPathAuditField(p *hatchPath, rest string) any {
	switch rest {
	case "flag":
		return int64(p.flag)
	case "bulges_present":
		if !p.isPolyline {
			return nil
		}
		return b2int(p.bulgesPresent)
	case "closed":
		if !p.isPolyline {
			return nil
		}
		return b2int(p.closed)
	case "num_segs_or_paths":
		return int64(p.numSegsOrPaths)
	case "segs", "polyline_paths":
		return nil // 数组本身
	case "boundary_handles":
		// 边界对象句柄数组（handle 流，按 path 尾部计数读入；gold 逐位
		// 对照末位句柄值）
		if len(p.boundaryHandles) == 0 {
			return nil
		}
		out := make([]float64, 0, len(p.boundaryHandles))
		for _, hh := range p.boundaryHandles {
			out = append(out, float64(hh))
		}
		return out
	}
	if idx, sub, ok := auditArrayIndex(rest, "segs"); ok {
		if idx < 0 || idx >= len(p.segs) {
			return nil
		}
		return hatchSegAuditField(&p.segs[idx], sub)
	}
	if idx, sub, ok := auditArrayIndex(rest, "polyline_paths"); ok {
		if idx < 0 || idx >= len(p.polyVerts) {
			return nil
		}
		switch sub {
		case "point":
			return nil // 数组
		case "bulge":
			return p.polyVerts[idx].bulge
		}
	}
	return nil
}

// hatchSegAuditField HATCH 边集段的展平键导出（sub 为 segs[j]. 之后的子键）。
func hatchSegAuditField(s *hatchSeg, sub string) any {
	switch sub {
	case "curve_type":
		return int64(s.curveType)
	case "radius":
		return s.radius
	case "minor_major_ratio":
		return s.ratio
	case "start_angle":
		return s.startAng
	case "end_angle":
		return s.endAng
	case "is_ccw":
		return b2int(s.ccw)
	case "degree":
		return int64(s.degree)
	case "is_rational":
		return b2int(s.rational)
	case "is_periodic":
		return b2int(s.periodic)
	case "num_knots":
		return int64(len(s.knots))
	case "num_control_points":
		return int64(len(s.ctrl))
	case "num_fitpts":
		return int64(len(s.fitPts))
	case "first_endpoint":
		return point2Arr(s.first)
	case "second_endpoint":
		return point2Arr(s.second)
	case "center":
		return point2Arr(s.center)
	case "endpoint":
		return point2Arr(s.endpoint)
	case "knots":
		return f64Arr(s.knots)
	case "weights":
		return f64Arr(s.weights)
	case "start_tangent":
		return point2Arr(s.startTan)
	case "end_tangent":
		return point2Arr(s.endTan)
	case "control_points", "fitpts":
		// gold 为 {point:[x,y]} 对象数组（flattenJSONGold 展平为
		// control_points[i].point），由数组下标键导出
		return nil
	}
	if idx, rest, ok := auditArrayIndex(sub, "control_points"); ok {
		if idx < 0 || idx >= len(s.ctrl) {
			return nil
		}
		if rest == "point" {
			return point2Arr(s.ctrl[idx])
		}
		return nil
	}
	if idx, rest, ok := auditArrayIndex(sub, "fitpts"); ok {
		if idx < 0 || idx >= len(s.fitPts) {
			return nil
		}
		if rest == "point" {
			return point2Arr(s.fitPts[idx])
		}
	}
	return nil
}

// hatchDefLineAuditField HATCH 定义线的展平键导出（rest 为 deflines[i]. 之后的子键）。
func hatchDefLineAuditField(dl *hatchDefLine, rest string) any {
	switch rest {
	case "angle":
		return dl.angle
	case "num_dashes":
		return int64(len(dl.dashes))
	case "pt0":
		return point2Arr(dl.pt0)
	case "offset":
		return point2Arr(dl.offset)
	case "dashes":
		return f64Arr(dl.dashes)
	}
	return nil
}

// auditArrayIndex 解析展平键的数组下标前缀：key 形如 "paths[2].flag"、
// name 为 "paths" 时返回 (2, "flag")。无方括号返回 ok=false。
func auditArrayIndex(key, name string) (idx int, rest string, ok bool) {
	if !strings.HasPrefix(key, name+"[") {
		return 0, "", false
	}
	close := strings.Index(key, "]")
	if close < 0 {
		return 0, "", false
	}
	n, err := strconv.Atoi(key[len(name)+1 : close])
	if err != nil {
		return 0, "", false
	}
	rest = key[close+1:]
	rest = strings.TrimPrefix(rest, ".")
	return n, rest, true
}

// b2int bool 转 0/1 整型。返回 int64 与审计值比较口径一致
// （auditValueMatch 的数值分支只识别 float64/int64/bool）。
func b2int(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func point3Arr(p point3) []float64 { return []float64{p.x, p.y, p.z} }

func vec3Arr(v point3) []float64 { return []float64{v.x, v.y, v.z} }

func pt2Arr(p []point2) []float64 {
	out := make([]float64, 0, len(p)*2)
	for _, e := range p {
		out = append(out, e.x, e.y)
	}
	return out
}

func f64Arr(v []float64) []float64 { return append([]float64(nil), v...) }

// colorAuditValue gold 的 color 键：ByLayer/索引色输出 ACI 索引；
// 真彩色输出 0xC0000000|RGB 形态的十进制值。
func colorAuditValue(c entColor) any {
	if c.hasTrue {
		return int64(0xC0000000) | int64(c.trueColor&0xFFFFFF)
	}
	if c.hasIndex {
		return int64(c.index)
	}
	return int64(256) // BYLAYER
}

// nearF 浮点近似比较（审计容差与内部对象口径一致）。
func nearF(a, b float64) bool {
	if a == b {
		return true
	}
	return math.Abs(a-b) <= 1e-6*(math.Abs(a)+math.Abs(b)+1)
}

// entityValueEqual 实体字段值比较：float 容差、切片逐元素、其余直接相等。
func entityValueEqual(got, want any) bool {
	switch w := want.(type) {
	case float64:
		g, ok := got.(float64)
		return ok && nearF(g, w)
	case []float64:
		g, ok := got.([]float64)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !nearF(g[i], w[i]) {
				return false
			}
		}
		return true
	case int64:
		g, ok := got.(int64)
		return ok && g == w
	case uint16:
		g, ok := got.(uint16)
		return ok && g == w
	case string:
		g, ok := got.(string)
		return ok && g == w
	case uint64:
		g, ok := got.(uint64)
		return ok && g == w
	}
	return false
}

func point2Arr(p point2) []float64 { return []float64{p.x, p.y} }

// p3sFlat 点数组展平为 [x,y,z,x,y,z,...]（gold 二维嵌套数组的导出形态，
// 测试侧 jsonTestValueMatch 做嵌套展开对照）。
func p3sFlat(pts []point3) []float64 {
	out := make([]float64, 0, len(pts)*3)
	for _, p := range pts {
		out = append(out, p.x, p.y, p.z)
	}
	return out
}

// expandUnicodeEscapes 展开 MTEXT 内联转义：\U+XXXX → 对应 UTF-8 字符
// （对齐 LibreDWG bit_TV_to_utf8 的 bit_u_expand 行为）。\M+nXXXX 罕见，不处理。
func expandUnicodeEscapes(s string) string {
	if !strings.Contains(s, `\U+`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if i+7 <= len(s) && s[i] == '\\' && s[i+1] == 'U' && s[i+2] == '+' {
			if v, err := strconv.ParseUint(s[i+3:i+7], 16, 32); err == nil {
				b.WriteRune(rune(v))
				i += 7
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// eedFieldValue 导出 gold 展平的 EED 组码对键（eed[i].code/size/value）：
// 各 EED 项的组码对按顺序展平编号，项的第一个对上可查 size（项数据字节数）。
func eedFieldValue(items []entEED, key string) any {
	rb := strings.Index(key, "]")
	if !strings.HasPrefix(key, "eed[") || rb < 0 {
		return nil
	}
	idx, err := strconv.Atoi(key[4:rb])
	if err != nil {
		return nil
	}
	field := key[rb+2:]
	flat := 0
	for _, it := range items {
		for pi, p := range it.Pairs {
			if flat == idx {
				switch field {
				case "code":
					return p.Code
				case "size":
					if pi == 0 {
						return it.Size
					}
					return nil
				case "value":
					return p.Value
				}
				return nil
			}
			flat++
		}
	}
	return nil
}

// underlayAuditField UNDERLAY 家族审计键导出（gold 标量键 angle/flag/
// contrast/fade；definition_id/extrusion/ins_pt/scale/clip_verts 为数组
// 键，审计展平循环按类型过滤跳过，无需导出）。
func underlayAuditField(u *entUnderlay, key string) any {
	switch key {
	case "angle":
		// gold JSON 输出弧度原值（out_json 无角度制转换）
		return u.angle
	case "flag":
		return int64(u.flag)
	case "contrast":
		return int64(u.contrast)
	case "fade":
		return int64(u.fade)
	}
	return nil
}
