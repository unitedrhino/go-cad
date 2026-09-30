// entity_audit.go 实体审计支持：字段导出器（对照 dwgread -O JSON 的
// gold 标量键）与实体句柄索引。键名与 LibreDWG JSON 输出一致
// （entity/bitsize/size/entmode/color/ltype_scale/linewt/z_is_zero/
// thickness/start/end/extrusion 等），供 TestAlignmentAudit 扩展后
// 对实体做与内部对象同口径的值级审计。
package entity

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
func EntityField(ent any, key string) any {
	ec, ok := ent.(EntityCommon)
	if !ok {
		return nil
	}
	b := ec.Common()
	hd := b.Head
	// DIMENSION 的 gold entity 键是全名（DIMENSION_LINEAR 等），内部短名
	// （DIM_LINEAR）需在此映射；公共 case 返回原始 typeName 不适用。
	if _, ok := ent.(*EntDimension); ok && key == "entity" {
		return DimGoldEntityName(b.TypeName)
	}
	// UNKNOWN_ENT 兜底实体的 gold entity 键强制 UNKNOWN_ENT（b.typeName
	// 保留原始动态类名如 ACAD_TABLE，fillMeta 覆盖后无法直接输出兜底名）。
	if _, ok := ent.(*EntUnknownEnt); ok && key == "entity" {
		return "UNKNOWN_ENT"
	}
	// LIGHT 的 spec 自带 type 字段（70 组码）在 gold JSON 中覆盖顶层类型码键
	// （out_json 顶层先输出 type，专有字段同名后写覆盖），故 type 键返回
	// 光类型值而非流内类型码；MULTILEADER 同理（BS 170 的 type 字段）。
	if l, ok := ent.(*EntLight); ok && key == "type" {
		return int64(l.LightType)
	}
	if m, ok := ent.(*EntMLeader); ok && key == "type" {
		return int64(m.MleaderType)
	}
	// OLE2FRAME 实体内 BS type 与公共实体 type 同名，gold JSON 重复键
	// 后写覆盖为实体内的 type 值（如 2=Embedded）
	if o, ok := ent.(*EntOle2Frame); ok && key == "type" {
		return int64(o.OleType)
	}
	if strings.HasPrefix(key, "eed[") {
		return eedFieldValue(b.eed, key)
	}
	switch key {
	case "handle":
		return b.Handle
	case "entmode":
		return int64(b.Mode)
	case "color":
		return colorAuditValue(b.Color)
	case "color.index":
		// gold color 为嵌套对象时（R2007+ CMC）展平出 color.index 等子键；
		// gold 仅在 index 非 0 时输出该键，index=0（ByBlock）返回 nil
		if b.Color.HasIndex && b.Color.Index != 0 {
			return int64(b.Color.Index)
		}
		return nil
	case "color.rgb":
		// gold 的 rgb 为 %06x 完整值（LibreDWG 不截断：32 位值含首字节
		// 索引/alpha 时输出 8 位 hex，纯 24 位时 6 位）
		return fmt.Sprintf("%06x", b.Color.TrueColor)
	case "color.flag":
		// gold 仅在 flag 非 0 时输出该键
		if b.Color.Flag != 0 {
			return int64(b.Color.Flag)
		}
		return nil
	case "color.alpha_raw":
		if b.Color.HasAlpha {
			return int64(b.Color.alphaRaw)
		}
		return nil
	case "color.alpha_type":
		if b.Color.HasAlpha {
			return int64(b.Color.alphaType)
		}
		return nil
	case "color.alpha":
		if b.Color.HasAlpha {
			return int64(b.Color.alpha)
		}
		return nil
	case "bitsize":
		return float64(b.ObjSizeBit)
	case "size":
		return float64(b.RecSize)
	case "entity":
		if b.TypeName != "" {
			// gold 对标注类实体输出 DIMENSION_ 前缀（内部类型名为 DIM_*）
			if strings.HasPrefix(b.TypeName, "DIM_") {
				return "DIMENSION_" + b.TypeName[4:]
			}
			return b.TypeName
		}
		return nil
	case "type":
		// gold 的 type 键是数字类号（如 LINE=19），类名字符串只通过 entity 键导出
		if b.TypeCode > 0 {
			return int64(b.TypeCode)
		}
		return nil
	case "nolinks":
		return B2int(b.Nolinks)
	case "isbylayerlt":
		return B2int(b.Isbylayerlt)
	case "preview_exists":
		return B2int(b.PreviewExists)
	case "preview_is_proxy":
		// LibreDWG 由 proxy 类（is_zombie）推导；常规解码恒 0
		if b.PreviewExists {
			return int64(0)
		}
		return nil
	case "preview_size":
		if hd != nil && b.PreviewExists && hd.Preview != nil {
			return int64(len(hd.Preview))
		}
		return nil
	case "preview":
		if hd != nil && b.PreviewExists && hd.Preview != nil {
			return fmt.Sprintf("%X", hd.Preview)
		}
		return nil
	}
	if hd != nil {
		switch key {
		case "ltype_scale":
			return hd.LtypeScale
		case "ltype_flags":
			return int64(hd.LtypeFlags)
		case "plotstyle_flags":
			return int64(hd.PlotstyleFlgs)
		case "material_flags":
			return int64(hd.MaterialFlags)
		case "invisible":
			return int64(hd.Invisible)
		case "linewt":
			return int64(hd.Linewt)
		case "shadow_flags":
			return int64(hd.ShadowFlags)
		case "has_full_visualstyle":
			return B2int(hd.VisualStyle[0])
		case "has_face_visualstyle":
			return B2int(hd.VisualStyle[1])
		case "has_edge_visualstyle":
			return B2int(hd.VisualStyle[2])
		case "is_xdic_missing":
			return B2int(hd.XdicMissing)
		case "has_ds_data":
			return B2int(hd.HasDsBinary)
		// 公共 handle 流次级句柄（批次 B 建模；gold 句柄为 0 时不输出键）
		case "ownerhandle":
			if b.Owner != 0 {
				return b.Owner
			}
			return nil
		case "layer":
			if b.Layer != 0 {
				return b.Layer
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
	if b.Extra != nil {
		if v, ok := b.Extra[key]; ok {
			return v
		}
	}
	// DIMENSION 族：body 标量键与 dwg.spec 字段一一对应。
	if d, ok := ent.(*EntDimension); ok {
		switch key {
		case "entity":
			return DimGoldEntityName(b.TypeName)
		// 公共点组与句柄系（批次 B 补齐；ORDINATE 专属点组为
		// def_pt(10)/feature_location_pt(13)/leader_endpt(14)，与
		// readDimSpecific 的 dimLayoutOrdinate 布局一一对应）
		case "def_pt":
			// def_pt（10 组码）：ARC_DIMENSION 的专用 defPt 优先（与
			// point10 同源）；ANG2LN 为 2RD 形态（p16x/p16y，R13~R2018
			// trace 实证 clone_ins_pt 后紧跟 def_pt 2RD）
			if b.TypeName == "DIM_ANG2LN" {
				return []float64{d.Point16x, d.P16y}
			}
			if d.DefPt != (Point3{}) {
				return point3Arr(d.DefPt)
			}
			return point3Arr(d.Point10)
		case "feature_location_pt":
			if b.TypeName == "DIM_ORDINATE" {
				return point3Arr(d.Point13)
			}
			return nil
		case "leader_endpt":
			if b.TypeName == "DIM_ORDINATE" {
				return point3Arr(d.Point14)
			}
			return nil
		case "xline1start_pt":
			if b.TypeName == "DIM_ANG2LN" {
				return point3Arr(d.Point13)
			}
			return nil
		case "xline1end_pt":
			if b.TypeName == "DIM_ANG2LN" {
				return point3Arr(d.Point14)
			}
			return nil
		case "xline2start_pt":
			if b.TypeName == "DIM_ANG2LN" {
				return point3Arr(d.Point15)
			}
			return nil
		case "xline2end_pt":
			if b.TypeName == "DIM_ANG2LN" {
				return point3Arr(d.Point10)
			}
			return nil
		case "xline1_pt":
			if b.TypeName != "DIM_ORDINATE" && b.TypeName != "DIM_ANG2LN" {
				return point3Arr(d.Point13)
			}
			return nil
		case "xline2_pt":
			if b.TypeName != "DIM_ORDINATE" && b.TypeName != "DIM_ANG2LN" {
				return point3Arr(d.Point14)
			}
			return nil
		case "clone_ins_pt":
			// clone_ins_pt（2RD，全维度族公共）：解码侧存入 insertPoint 的
			// x/y（gold 形态为二元组）
			if d.HasInsertPoint {
				return []float64{d.InsertPoint.X, d.InsertPoint.Y}
			}
			return nil
		case "center_pt":
			// ARC_DIMENSION 中心点（15 组码，dimLayoutArc 第三点）
			if b.TypeName == "ARC_DIMENSION" {
				return point3Arr(d.Point15)
			}
			return nil
		case "leader1_pt":
			return point3Arr(d.Leader1Pt)
		case "leader2_pt":
			return point3Arr(d.Leader2Pt)
		case "text_midpt":
			return point3Arr(d.TextMidpoint)
		case "extrusion":
			return Vec3Arr(d.Extrusion)
		case "ins_scale":
			return Vec3Arr(d.InsertScale)
		case "dimstyle":
			if d.DimstyleHandle != 0 {
				return d.DimstyleHandle
			}
			return nil
		case "block":
			if d.AnonymousBlock != 0 {
				return d.AnonymousBlock
			}
			return nil
		case "class_version":
			return int64(d.ClassVersion)
		case "elevation":
			return d.Elevation
		case "flag":
			return int64(d.DimFlag)
		case "flag1":
			return int64(d.DimFlags)
		case "flag2":
			return int64(d.Flag2)
		case "user_text":
			return d.UserText
		case "text_rotation":
			return d.TextRotation
		case "horiz_dir":
			return d.HorizontalDir
		case "ins_rotation":
			return d.InsertRotation
		case "attachment":
			return int64(d.AttachmentPoint)
		case "lspace_style":
			return int64(d.LineSpacingStyle)
		case "lspace_factor":
			return d.LineSpacingFactor
		case "act_measurement":
			return d.ActualMeasurement
		case "unknown":
			return B2int(d.unknownFlag)
		case "flip_arrow1":
			return B2int(d.flipArrow1)
		case "flip_arrow2":
			return B2int(d.flipArrow2)
		case "oblique_angle":
			return d.ExtLineRotation
		case "dim_rotation":
			return d.DimRotation
		case "is_partial":
			return B2int(d.IsPartial)
		case "arc_start_param":
			return d.ArcStartParam
		case "arc_end_param":
			return d.ArcEndParam
		case "has_leader":
			return B2int(d.HasLeader)
		case "leader_len":
			return d.LeaderLen
		}
	}
	switch e := ent.(type) {
	case *EntLine:
		switch key {
		case "start":
			return point3Arr(e.Start)
		case "end":
			return point3Arr(e.End)
		}
	case *EntCircle:
		switch key {
		case "center":
			return point3Arr(e.Center)
		case "radius":
			return e.Radius
		}
	case *EntArc:
		switch key {
		case "center":
			return point3Arr(e.Center)
		case "radius":
			return e.Radius
		case "start_angle":
			return e.AngleStart
		case "end_angle":
			return e.AngleEnd
		}
	case *EntPoint:
		switch key {
		case "location":
			return point3Arr(e.Location)
		case "rotation", "x_ang":
			// gold 的 x_ang 即 x 轴角度（解码侧 rotation）
			return e.Rotation
		case "x":
			return e.Location.X
		case "y":
			return e.Location.Y
		case "z":
			return e.Location.Z
		}
	case *EntEllipse:
		switch key {
		case "center":
			return point3Arr(e.Center)
		case "major_axis", "sm_axis":
			// gold 键名 sm_axis（本库自有口径 major_axis 双键导出）
			return Vec3Arr(e.MajorAxis)
		case "extrusion":
			return Vec3Arr(e.Extrusion)
		case "ratio", "axis_ratio":
			// gold 键名为 axis_ratio（流内 BD 40 原值，无倒数换算）
			return e.Ratio
		case "start_angle":
			return e.StartAng
		case "end_angle":
			return e.EndAng
		}
	case *EntLwPolyline:
		if idx, rest, ok := auditArrayIndex(key, "widths"); ok {
			// gold 的 widths 为 {start,end} 对象数组，展平出 widths[i].start/end
			if idx >= 0 && idx < len(e.Widths) {
				switch rest {
				case "start":
					return e.Widths[idx].Start
				case "end":
					return e.Widths[idx].End
				}
			}
			return nil
		}
		switch key {
		case "flags", "flag":
			// gold 的 LWPOLYLINE 标志键名为 flag
			return int64(e.Flags)
		case "elevation":
			// flag&8 时输出标高；无标志段 gold 也不输出该键（返回 0 不参与）
			return e.Elevation
		case "const_width":
			// flag&4 时的常量宽度（同 elevation 模式）
			return e.ConstWidth
		case "thickness":
			// flag&2 时的厚度（同 elevation 模式）
			return e.Thickness
		case "vertices", "points":
			// gold 键名 points（展平 [[x,y],...] 嵌套形态由测试侧展开）
			return Pt2Arr(e.Vertices)
		case "bulges":
			return F64Arr(e.Bulges)
		case "vertexids":
			return nil // gold 顶点索引数组（本库未建模）
		case "widths":
			return nil
		}
	case *EntText:
		switch key {
		case "text", "text_value":
			// gold 的 text_value 即显示文本
			return e.Text
		case "insertion", "ins_pt":
			// gold 的 ins_pt 即插入点
			return point3Arr(e.Insertion)
		case "height":
			return e.Height
		case "rotation":
			return e.Rotation
		case "h_align", "horiz_alignment":
			// gold 的 horiz_alignment 即水平对齐（解码侧 hAlign）
			return int64(e.HAlign)
		case "v_align", "vert_alignment":
			// gold 的 vert_alignment 即垂直对齐（解码侧 vAlign）
			return int64(e.VAlign)
		case "generation":
			return int64(e.Gen)
		case "alignment_pt":
			if e.AlignPt != nil {
				return Point2Arr(*e.AlignPt)
			}
			return nil
		case "extrusion":
			return Vec3Arr(e.Extrusion)
		case "style":
			// 文本样式句柄（handle 流专有段首项；gold 句柄为 0 时不输出键）
			if e.StyleHandle != 0 {
				return e.StyleHandle
			}
			return nil
		}
	case *EntMText:
		switch key {
		case "insertion", "ins_pt":
			// gold 的 ins_pt 即插入点
			return point3Arr(e.Insertion)
		case "x_axis_dir":
			return Vec3Arr(e.XAxisDir)
		case "rect_width":
			return e.RectWidth
		case "text_height":
			return e.TextHeight
		case "attachment":
			return int64(e.Attachment)
		case "extrusion":
			return Vec3Arr(e.Extrusion)
		case "style":
			// 文本样式句柄（handle 流专有段；gold 句柄为 0 时不输出键）
			if e.StyleHandle != 0 {
				return e.StyleHandle
			}
			return nil
		case "text":
			// gold 输出经 bit_TV_to_utf8 的 \U+XXXX 展开（AutoCAD 内联转义）
			return expandUnicodeEscapes(e.Text)
		}
	case *EntInsert:
		switch key {
		case "insertion", "position", "ins_pt":
			// gold 的 ins_pt 即插入点
			return point3Arr(e.Position)
		case "scale":
			return Vec3Arr(e.Scale)
		case "rotation":
			return e.Rotation
		case "block_header":
			return e.BlockHeader
		case "extrusion":
			return Vec3Arr(e.Extrusion)
		case "seqend":
			// SEQEND 结束句柄（attribs 后；has_attribs=0 时无该句柄）
			if e.Seqend != 0 {
				return e.Seqend
			}
			return nil
		case "scale_flag":
			if v, ok := e.Extra["scale_flag"]; ok {
				return v
			}
			return nil
		case "has_attribs":
			if v, ok := e.Extra["has_attribs"]; ok {
				return v
			}
			return nil
		case "attribs":
			// 关联 ATTRIB 句柄链（ParseJSON 侧经 linkJSONAttribs 回填，
			// DWG 侧 R2004+ 为 owned 句柄、R13~R2000 为 first/last 对）
			out := make([]float64, 0, len(e.Attribs))
			for _, ah := range e.Attribs {
				out = append(out, float64(ah))
			}
			return out
		case "first_attrib":
			if len(e.Attribs) > 0 {
				return e.Attribs[0]
			}
			return nil
		case "last_attrib":
			if len(e.Attribs) > 0 {
				return e.Attribs[len(e.Attribs)-1]
			}
			return nil
		}
	case *EntAttrib:
		switch key {
		case "text", "text_value", "default_value":
			// ATTDEF 的 default_value 与 ATTRIB 的 text_value 同为显示文本
			return e.Text
		case "prompt":
			// gold 仅 ATTDEF 有 prompt 键；ATTRIB 返回 nil 不比对
			if b.TypeCode == 0x03 {
				return e.Prompt
			}
			return nil
		case "tag":
			return e.Tag
		case "insertion", "ins_pt":
			return point3Arr(e.Insertion)
		case "height":
			return e.Height
		case "rotation":
			return e.Rotation
		case "horiz_alignment":
			return int64(e.HAlign)
		case "vert_alignment":
			return int64(e.VAlign)
		case "generation":
			return int64(e.Gen)
		case "alignment_pt":
			if e.AlignPt != nil {
				return Point2Arr(*e.AlignPt)
			}
			return nil
		case "extrusion":
			return Vec3Arr(e.Extrusion)
		case "style":
			// 文本样式句柄（handle 流专有段首项；gold 句柄为 0 时不输出键）
			if e.StyleHandle != 0 {
				return e.StyleHandle
			}
			return nil
		}
	case *EntVertex2d:
		switch key {
		case "location", "position", "point":
			return point3Arr(e.Position)
		case "bulge":
			return e.Bulge
		case "flag":
			return int64(e.Flags)
		case "id":
			// R2010+ 顶点标识符（BL0 spec 字段）；pre-R2010 样本 gold
			// 无 id 键，不会查询此 case
			return e.id
		case "tangent_dir":
			return e.TangentDir
		case "start_width":
			return e.StartWidth
		case "end_width":
			return e.EndWidth
		}
	case *EntVertex3d:
		switch key {
		case "location", "position", "point":
			return point3Arr(e.Position)
		case "flag":
			return int64(e.Flags)
		}
	case *EntVertexPface:
		switch key {
		case "flag":
			return int64(e.Flag)
		case "location", "point":
			return point3Arr(e.Position)
		}
	case *EntVertexPfaceFace:
		switch key {
		case "flag":
			// flag 恒为 128，不从流读取（gold 同值输出）
			return int64(e.Flag)
		case "vertind":
			return []float64{float64(e.Vertind[0]), float64(e.Vertind[1]), float64(e.Vertind[2]), float64(e.Vertind[3])}
		}
	case *EntPolyline2d:
		switch key {
		case "flag":
			return int64(e.Flags)
		case "curve_type":
			return int64(e.CurveType)
		case "start_width":
			return e.WidthStart
		case "end_width":
			return e.WidthEnd
		case "thickness":
			return e.Thickness
		case "elevation":
			return e.Elevation
		case "extrusion":
			return Vec3Arr(e.Extrusion)
		case "first_vertex":
			if e.FirstVertex != 0 {
				return e.FirstVertex
			}
			return nil
		case "last_vertex":
			if e.LastVertex != 0 {
				return e.LastVertex
			}
			return nil
		case "vertex":
			// R2004+ owned 顶点句柄数组
			out := make([]float64, 0, len(e.OwnedHandles))
			for _, vh := range e.OwnedHandles {
				out = append(out, float64(vh))
			}
			return out
		case "seqend":
			if e.Seqend != 0 {
				return e.Seqend
			}
			return nil
		}
	case *EntPolyline3d:
		switch key {
		case "flag":
			return int64(e.Flags70)
		case "curve_type":
			return int64(e.Flags75)
		case "first_vertex":
			if e.FirstVertex != 0 {
				return e.FirstVertex
			}
			return nil
		case "last_vertex":
			if e.LastVertex != 0 {
				return e.LastVertex
			}
			return nil
		case "vertex":
			out := make([]float64, 0, len(e.OwnedHandles))
			for _, vh := range e.OwnedHandles {
				out = append(out, float64(vh))
			}
			return out
		case "seqend":
			if e.Seqend != 0 {
				return e.Seqend
			}
			return nil
		}
	case *EntPolylinePface:
		switch key {
		case "numverts":
			return int64(e.NumVertices)
		case "numfaces":
			return int64(e.NumFaces)
		case "first_vertex":
			// R13~R2000 handle 流首顶点句柄（R2004+ 为 owned 向量，无该键）
			if e.FirstVertex != 0 {
				return e.FirstVertex
			}
			return nil
		case "last_vertex":
			if e.LastVertex != 0 {
				return e.LastVertex
			}
			return nil
		case "vertex":
			// R2004+ owned 顶点句柄数组
			out := make([]float64, 0, len(e.OwnedHandles))
			for _, vh := range e.OwnedHandles {
				out = append(out, float64(vh))
			}
			return out
		case "seqend":
			if e.Seqend != 0 {
				return e.Seqend
			}
			return nil
		}
	case *EntRay:
		switch key {
		case "start", "point":
			return point3Arr(e.Start)
		case "direction", "vector":
			return Vec3Arr(e.UnitVector)
		}

	case *EntBlockLike:
		switch key {
		case "name":
			// gold 仅 BLOCK 有 name 键（ENDBLK/SEQEND 无该键，返回 nil 不比对）
			if e.Name != "" {
				return e.Name
			}
			return nil
		}
	case *EntSolid:
		switch key {
		case "corner1", "p1":
			return Point2Arr(e.P1)
		case "corner2", "p2":
			return Point2Arr(e.P2)
		case "corner3", "p3":
			return Point2Arr(e.P3)
		case "corner4", "p4":
			return Point2Arr(e.P4)
		case "thickness":
			return e.Thickness
		case "elevation":
			return e.Elevation
		case "extrusion":
			return Vec3Arr(e.Extrusion)
		}
	case *EntFace3d:
		switch key {
		case "corner1", "p1":
			return point3Arr(e.P1)
		case "corner2", "p2":
			return point3Arr(e.P2)
		case "corner3", "p3":
			return point3Arr(e.P3)
		case "corner4", "p4":
			return point3Arr(e.P4)
		case "has_no_flags":
			return B2int(e.hasNoFlags)
		case "z_is_zero":
			return B2int(e.zIsZero)
		case "invis_flags":
			return int64(e.InvisibleEdgeFlags)
		}
	case *EntMLine:
		switch key {
		case "scale":
			return e.Scale
		case "justification":
			return int64(e.Justification)
		case "flags":
			return int64(e.OpenClosed)
		case "base_point":
			return point3Arr(e.BasePoint)
		case "extrusion":
			return point3Arr(e.Extrusion)
		case "mlinestyle":
			if e.StyleHandle != 0 {
				return e.StyleHandle
			}
			return nil
		}
		// 展平嵌套键：verts[i].vertex/vertex_direction/miter_direction 与
		// verts[i].lines[j].segparms/areafillparms（批次 B：按每线计数从
		// 扁平参数数组切片；DXF 来源无分组计数，返回 nil）
		if idx, rest, ok := auditArrayIndex(key, "verts"); ok {
			if idx < 0 || idx >= len(e.Vertices) {
				return nil
			}
			v := &e.Vertices[idx]
			switch rest {
			case "vertex":
				return point3Arr(v.Position)
			case "vertex_direction":
				return point3Arr(v.Direction)
			case "miter_direction":
				return point3Arr(v.Miter)
			}
			if li, sub, ok := auditArrayIndex(rest, "lines"); ok {
				switch sub {
				case "segparms":
					return mlineLineParams(v.SegParams, v.SegCounts, li)
				case "areafillparms":
					return mlineLineParams(v.AreaParams, v.AreaCounts, li)
				}
			}
			return nil
		}
	case *EntPolylineMesh:
		switch key {
		case "flag":
			return int64(e.Flags)
		case "curve_type":
			return int64(e.CurveType)
		case "m_density":
			return int64(e.MDensity)
		case "n_density":
			return int64(e.NDensity)
		}
	case *EntSpline:
		// SPLINE 审计键（批次 B 补齐：主体标量 + 点/数组键；R2013+ 的
		// splineflags/knotparam 为位域合成口径，未导出）
		switch key {
		case "scenario":
			return int64(e.Scenario)
		case "degree":
			return int64(e.Degree)
		case "fit_tol":
			return e.FitTolerance
		case "knot_tol":
			return e.KnotTolerance
		case "ctrl_tol":
			return e.CtrlTolerance
		case "knots":
			return F64Arr(e.Knots)
		case "weights":
			return F64Arr(e.Weights)
		case "fit_pts":
			return p3sFlat(e.FitPoints)
		case "rational":
			return B2int(e.Rational)
		case "closed_b":
			return B2int(e.Closed)
		case "periodic":
			return B2int(e.Periodic)
		case "weighted":
			return B2int(e.weighted)
		}
		if idx, rest, ok := auditArrayIndex(key, "ctrl_pts"); ok {
			if idx < 0 || idx >= len(e.ControlPoints) {
				return nil
			}
			switch rest {
			case "x":
				return e.ControlPoints[idx].X
			case "y":
				return e.ControlPoints[idx].Y
			case "z":
				return e.ControlPoints[idx].Z
			}
			return nil
		}
		if key == "style" {
			// 样式句柄（handle 流，批次 B 建模）
			if e.StyleHandle != 0 {
				return e.StyleHandle
			}
			return nil
		}
	case *EntShape:
		switch key {
		case "insertion":
			return point3Arr(e.Insertion)
		case "scale":
			return e.Scale
		case "rotation":
			return e.Rotation
		case "width_factor":
			return e.WidthFactor
		case "oblique", "oblique_angle":
			return e.Oblique
		case "thickness":
			return e.Thickness
		case "style_id":
			return int64(e.StyleId)
		}
	case *EntViewport:
		switch key {
		case "width":
			return e.Width
		case "height":
			return e.Height
		case "center":
			return point3Arr(e.Center)
		case "view_target":
			return point3Arr(e.ViewTarget)
		case "VIEWDIR":
			return Vec3Arr(e.ViewDir)
		case "VIEWCTR":
			return Point2Arr(e.ViewCtr)
		case "SNAPBASE":
			return Point2Arr(e.SnapBase)
		case "SNAPUNIT":
			return Point2Arr(e.SnapUnit)
		case "GRIDUNIT":
			return Point2Arr(e.GridUnit)
		case "UCSORG":
			return point3Arr(e.Ucsorg)
		case "UCSXDIR":
			return point3Arr(e.Ucsxdir)
		case "UCSYDIR":
			return point3Arr(e.Ucsydir)
		case "VIEWTWIST":
			return e.ViewTwist
		case "VIEWSIZE":
			return e.ViewSize
		case "LENSLENGTH":
			return e.LensLength
		case "FRONTZ":
			return e.FrontZ
		case "BACKZ":
			return e.BackZ
		case "SNAPANG":
			return e.SnapAng
		case "circle_zoom":
			return int64(e.CircleZoom)
		case "grid_major":
			return int64(e.GridMajor)
		case "status_flag":
			return int64(e.StatusFlag)
		case "style_sheet":
			return e.StyleSheet
		case "render_mode":
			return int64(e.RenderMode)
		case "UCSVP":
			return B2int(e.UcsVP)
		case "ucs_at_origin":
			return B2int(e.UcsAtOrigin)
		case "ucs_elevation":
			return e.UcsElevation
		case "UCSORTHOVIEW":
			return int64(e.UcsOrthoView)
		case "shadeplot_mode":
			return int64(e.ShadeplotMode)
		case "use_default_lights":
			return B2int(e.UseDefaultLights)
		case "default_lighting_type":
			return int64(e.DefaultLightingType)
		case "brightness":
			return e.Brightness
		case "contrast":
			return e.Contrast
		case "ambient_color.index":
			// 对齐 out_json field_cmc：流内 index=0 时按 method 从 rgb
			// 反查 ACI 调色板（如 0xc2333333 → 灰 250）
			if e.ambientIndex != 0 {
				return int64(e.ambientIndex)
			}
			return DwgFindColorIndex(e.ambientRGB)
		case "ambient_color.rgb":
			return fmt.Sprintf("%08x", e.ambientRGB)
		case "ambient_color.flag":
			return int64(0)
		}
	case *EntTolerance:
		switch key {
		case "text_value":
			return e.Text
		case "unknown_short":
			return int64(e.UnknownShort)
		case "height":
			return e.Height
		case "dimgap":
			return e.Dimgap
		case "ins_pt", "insertion":
			return point3Arr(e.Insertion)
		case "x_direction":
			return Vec3Arr(e.XDirection)
		case "extrusion":
			return Vec3Arr(e.Extrusion)
		case "dimstyle":
			if e.Dimstyle != 0 {
				return e.Dimstyle
			}
			return nil
		}
	case *EntLeader:
		switch key {
		case "annot_type", "annotation_type":
			return int64(e.AnnotationType)
		case "path_type":
			return int64(e.PathType)
		case "unknown_bit_1":
			return B2int(e.UnknownBit1)
		case "arrowhead_on":
			return B2int(e.ArrowheadOn)
		case "arrowhead_type":
			return int64(e.ArrowheadType)
		case "box_height":
			return e.BoxHeight
		case "box_width":
			return e.BoxWidth
		case "hookline_dir":
			return B2int(e.HooklineDir)
		case "hookline_on":
			return B2int(e.hooklineOn)
		case "dimgap":
			return e.Dimgap
		case "dimasz":
			return e.dimasz
		case "unknown_short_1":
			return int64(e.unknownShort1)
		case "byblock_color":
			return int64(e.byblockColor)
		case "unknown_bit_2":
			return B2int(e.unknownBit2)
		case "unknown_bit_3":
			return B2int(e.unknownBit3)
		case "unknown_bit_4":
			return B2int(e.UnknownBit4)
		case "unknown_bit_5":
			return B2int(e.UnknownBit5)
		}
	case *EntLight:
		return lightAuditField(e, key)
	case *EntMLeader:
		return mleaderAuditField(e, key)
	case *EntHatch:
		return hatchAuditField(e, key)
	case *EntAcis:
		return acisAuditField(e, key)
	case *EntWipeout:
		return wipeoutAuditField(e, key)
	case *EntImage:
		return imageAuditField(e, key)
	case *EntOle2Frame:
		return Ole2FrameAuditField(e, key)
	case *EntOleFrame:
		return OleFrameAuditField(e, key)
	case *EntProxyEntity:
		return ProxyEntityAuditField(e, key)
	case *EntUnderlay:
		return underlayAuditField(e, key)
	case *EntMpolygon:
		return MpolygonAuditField(e, key)
	}
	return nil
}

// mlineLineParams 按每线计数从扁平参数数组切片出第 li 条样式线的参数
// （前缀和定位；计数缺失或越界返回 nil——DXF 来源无分组信息）。
func mlineLineParams(params []float64, counts []int, li int) []float64 {
	if li < 0 || li >= len(counts) {
		return nil
	}
	Start := 0
	for i := 0; i < li; i++ {
		Start += counts[i]
	}
	End := Start + counts[li]
	if End > len(params) {
		return nil
	}
	return params[Start:End]
}

// dimGoldEntityName 内部 DIM 短名映射为 gold JSON 的 DIMENSION 全名；
// 非 DIMENSION 类型原样返回。
func DimGoldEntityName(Name string) string {
	const short = "DIM_"
	if len(Name) > len(short) && Name[:len(short)] == short {
		return "DIMENSION_" + Name[len(short):]
	}
	return Name
}

// mleaderCMCAuditValue CMC 键值导出（对齐 out_json field_cmc）：
// pre-R2004 gold 为标量索引数字（key 键本身），R2004+ 为对象展平子键
// ——index 仅反查结果非 0 时输出、rgb 恒为完整 32 位 %06x、flag 非 0 才输出。
func mleaderCMCAuditValue(c MleaderCMC, key string, scalarKey, prefix string) any {
	if !c.IsTrue {
		if key == scalarKey {
			return int64(c.Index)
		}
		return nil
	}
	switch key {
	case prefix + ".index":
		if c.Index != 0 {
			return int64(c.Index)
		}
	case prefix + ".rgb":
		return fmt.Sprintf("%06x", c.Rgb)
	case prefix + ".flag":
		if c.Flag != 0 {
			return int64(c.Flag)
		}
	}
	return nil
}

// mleaderAuditField MULTILEADER 审计键导出：顶层标量键 + ctx.* 展平键
// （ctx.leaders[i]…/ctx.leaders[i].lines[j]…，与 gold JSON 展平口径一致）。
// 点/数组键（content_base、points、breaks、block_scale 等 gold 数组形态）
// 不参与比对，统一返回 nil。
func mleaderAuditField(m *EntMLeader, key string) any {
	switch key {
	case "class_version":
		if m.HasVersion {
			return int64(m.ClassVersion)
		}
		return nil
	case "flags":
		return int64(m.Flags)
	case "line_linewt":
		return int64(m.LineLinewt)
	case "has_landing":
		return B2int(m.HasLanding)
	case "has_dogleg":
		return B2int(m.HasDogleg)
	case "landing_dist":
		return m.LandingDist
	case "arrow_size":
		return m.ArrowSize
	case "style_content":
		return int64(m.StyleContent)
	case "text_left":
		return int64(m.TextLeft)
	case "text_right":
		return int64(m.TextRight)
	case "text_angletype":
		return int64(m.TextAngletype)
	case "text_alignment":
		return int64(m.TextAlignment)
	case "has_text_frame":
		return B2int(m.HasTextFrame)
	case "block_rotation":
		return m.BlockRotation
	case "style_attachment":
		return int64(m.StyleAttachment)
	case "is_annotative":
		return B2int(m.IsAnnotative)
	case "is_neg_textdir":
		return B2int(m.IsNegTextdir)
	case "ipe_alignment":
		return int64(m.IpeAlignment)
	case "justification":
		return int64(m.Justification)
	case "scale_factor":
		return m.ScaleFactor
	case "attach_dir":
		return int64(m.AttachDir)
	case "attach_top":
		return int64(m.AttachTop)
	case "attach_bottom":
		return int64(m.AttachBottom)
	case "is_text_extended":
		if m.HasVersion { // R2013b+ 键；gold 仅 R2013/2018 出现
			return B2int(m.IsTextExtended)
		}
		return nil
	}
	// 顶层 CMC 双形态（line_color/text_color/block_color）
	for _, e := range []struct {
		cmc    MleaderCMC
		prefix string
	}{{m.LineColor, "line_color"}, {m.TextColor, "text_color"}, {m.BlockColor, "block_color"}} {
		if v := mleaderCMCAuditValue(e.cmc, key, e.prefix, e.prefix); v != nil {
			return v
		}
	}
	// 句柄系（批次 B 补齐：gold 句柄为 0 时不输出键）
	switch key {
	case "mleaderstyle":
		if m.MleaderStyle != 0 {
			return m.MleaderStyle
		}
		return nil
	case "arrow_handle":
		if m.ArrowHandle != 0 {
			return m.ArrowHandle
		}
		return nil
	case "text_style":
		if m.TextStyle != 0 {
			return m.TextStyle
		}
		return nil
	case "block_style":
		if m.BlockStyle != 0 {
			return m.BlockStyle
		}
		return nil
	case "line_ltype":
		if m.LineLtype != 0 {
			return m.LineLtype
		}
		return nil
	case "block_scale":
		return Vec3Arr(m.BlockScale)
	}
	// ctx.* 键
	if v, ok := mleaderCtxAuditField(m, key); ok {
		return v
	}
	return nil
}

// mleaderCtxAuditField ctx 展平键导出；ok=false 表示键不归属 ctx 段。
func mleaderCtxAuditField(m *EntMLeader, key string) (any, bool) {
	c := &m.Ctx
	switch key {
	case "ctx.num_leaders":
		return int64(c.NumLeaders), true
	case "ctx.scale_factor":
		return c.ScaleFactor, true
	case "ctx.text_height":
		return c.TextHeight, true
	case "ctx.arrow_size":
		return c.ArrowSize, true
	case "ctx.landing_gap":
		return c.LandingGap, true
	case "ctx.text_left":
		return int64(c.TextLeft), true
	case "ctx.text_right":
		return int64(c.TextRight), true
	case "ctx.text_angletype":
		return int64(c.TextAngletype), true
	case "ctx.text_alignment":
		return int64(c.TextAlignment), true
	case "ctx.has_content_txt":
		return B2int(c.HasContentTxt), true
	case "ctx.has_content_blk":
		return B2int(c.HasContentBlk), true
	case "ctx.is_normal_reversed":
		return B2int(c.IsNormalReversed), true
	case "ctx.text_top":
		if m.HasVersion {
			return int64(c.TextTop), true
		}
		return nil, true
	case "ctx.text_bottom":
		if m.HasVersion {
			return int64(c.TextBottom), true
		}
		return nil, true
	// ctx 点组（批次 B 补齐数组导出）
	case "ctx.base":
		return point3Arr(c.Base), true
	case "ctx.base_dir":
		return point3Arr(c.BaseDir), true
	case "ctx.base_vert":
		return point3Arr(c.BaseVert), true
	case "ctx.content_base":
		return point3Arr(c.ContentBase), true
	case "ctx.content.txt.normal":
		return point3Arr(c.Txt.Normal), true
	case "ctx.content.txt.location":
		return point3Arr(c.Txt.Location), true
	case "ctx.content.txt.direction":
		return point3Arr(c.Txt.Direction), true
	case "ctx.content.txt.style":
		if c.Txt.StyleHandle != 0 {
			return c.Txt.StyleHandle, true
		}
		return nil, true
	case "ctx.content.blk.block_table":
		if c.Blk.BlockTable != 0 {
			return c.Blk.BlockTable, true
		}
		return nil, true
	case "ctx.content.blk.normal":
		return point3Arr(c.Blk.Normal), true
	case "ctx.content.blk.location":
		return point3Arr(c.Blk.Location), true
	case "ctx.content.blk.scale":
		return Vec3Arr(c.Blk.Scale), true
	case "ctx.content.blk.transform":
		return F64Arr(c.Blk.Transform[:]), true
	}
	if idx, rest, ok := auditArrayIndex(key, "ctx.leaders"); ok {
		if idx < 0 || idx >= len(c.Leaders) {
			return nil, true
		}
		return mleaderNodeAuditField(m, &c.Leaders[idx], rest), true
	}
	if strings.HasPrefix(key, "ctx.content.txt.") {
		return mleaderTxtAuditField(&c.Txt, strings.TrimPrefix(key, "ctx.content.txt.")), true
	}
	if strings.HasPrefix(key, "ctx.content.blk.") {
		b := &c.Blk
		switch strings.TrimPrefix(key, "ctx.content.blk.") {
		case "rotation":
			return b.Rotation, true
		case "color.rgb":
			// CMC rgb 完整 32 位（对齐 gold %08x/%06x 双形态）
			return fmt.Sprintf("%06x", b.Color.Rgb), true
		case "color.index":
			return int64(b.Color.Index), true
		}
		// normal/location/scale/transform 为数组键，block_table 为句柄键
		return nil, true
	}
	// blocklabels[i].*（R14-R2007 块标签数组，multileaders 真实样本实证）
	if idx, rest, ok := auditArrayIndex(key, "blocklabels"); ok {
		if idx < 0 || idx >= len(m.Blocklabels) {
			return nil, true
		}
		bl := &m.Blocklabels[idx]
		switch rest {
		case "label_text":
			return bl.LabelText, true
		case "ui_index":
			return int64(bl.UiIndex), true
		case "width":
			return bl.Width, true
		}
		return nil, true // attdef 句柄键
	}
	return nil, false
}

// mleaderNodeAuditField ctx.leaders[i] 展平键导出（rest 为前缀后的子键）。
func mleaderNodeAuditField(m *EntMLeader, n *MleaderNode, rest string) any {
	switch rest {
	case "has_lastleaderlinepoint":
		return B2int(n.HasLastLeaderLinePoint)
	case "has_dogleg":
		return B2int(n.HasDogleg)
	case "branch_index":
		return int64(n.BranchIndex)
	case "dogleg_length":
		return n.DoglegLength
	case "lastleaderlinepoint":
		if n.HasLastLeaderLinePoint {
			return point3Arr(n.LastLeaderLinePoint)
		}
		return nil
	case "dogleg_vector":
		if n.HasDogleg {
			return point3Arr(n.DoglegVector)
		}
		return nil
	case "attach_dir":
		if m.HasVersion {
			return int64(n.AttachDir)
		}
		return nil
	}
	if idx, sub, ok := auditArrayIndex(rest, "lines"); ok {
		if idx < 0 || idx >= len(n.Lines) {
			return nil
		}
		line := &n.Lines[idx]
		switch sub {
		case "line_index":
			return int64(line.LineIndex)
		case "type":
			if m.HasVersion {
				return int64(line.MleaderType)
			}
			return nil
		case "linewt":
			if m.HasVersion {
				return int64(line.Linewt)
			}
			return nil
		case "arrow_size":
			if m.HasVersion {
				return line.ArrowSize
			}
			return nil
		case "flags":
			if m.HasVersion {
				return int64(line.Flags)
			}
			return nil
		case "points":
			return p3sFlat(line.Points)
		case "ltype":
			if m.HasVersion && line.ltype != 0 {
				return line.ltype
			}
			return nil
		case "arrow_handle":
			if m.HasVersion && line.ArrowHandle != 0 {
				return line.ArrowHandle
			}
			return nil
		}
		if m.HasVersion && strings.HasPrefix(sub, "color") {
			// R2010b+ 的 lline.color：R2004+ 结构下展平出 color.rgb 等子键
			return mleaderCMCAuditValue(line.Color, sub, "color", "color")
		}
		return nil
	}
	return nil // num_breaks/num_lines/points/breaks 数组或计数键
}

// mleaderTxtAuditField ctx.content.txt 展平键导出。
func mleaderTxtAuditField(t *mleaderTxtContent, rest string) any {
	switch rest {
	case "default_text":
		return t.DefaultText
	case "rotation":
		return t.Rotation
	case "width":
		return t.Width
	case "height":
		return t.Height
	case "line_spacing_factor":
		return t.LineSpacingFactor
	case "line_spacing_style":
		return int64(t.LineSpacingStyle)
	case "alignment":
		return int64(t.Alignment)
	case "flow":
		return int64(t.Flow)
	case "bg_scale":
		return t.BgScale
	case "bg_transparency":
		return int64(t.BgTransparency)
	case "is_bg_fill":
		return B2int(t.IsBgFill)
	case "is_bg_mask_fill":
		return B2int(t.IsBgMaskFill)
	case "col_type":
		return int64(t.ColType)
	case "is_height_auto":
		return B2int(t.IsHeightAuto)
	case "col_width":
		return t.ColWidth
	case "col_gutter":
		return t.ColGutter
	case "is_col_flow_reversed":
		return B2int(t.IsColFlowReversed)
	case "num_col_sizes":
		return int64(t.NumColSizes)
	case "word_break":
		return B2int(t.WordBreak)
	case "unknown":
		return B2int(t.Unknown)
	}
	if strings.HasPrefix(rest, "color") {
		return mleaderCMCAuditValue(t.Color, rest, "color", "color")
	}
	if strings.HasPrefix(rest, "bg_color") {
		return mleaderCMCAuditValue(t.BgColor, rest, "bg_color", "bg_color")
	}
	return nil // normal/location/direction/col_sizes/style 数组或句柄键
}

// lightAuditField LIGHT 审计键导出：基线标量键 + light_color CMC 子键。
// light_color 双形态：pre-R2004 gold 为标量索引数字（light_color 键），
// R2004+ gold 为对象展平出的 light_color.index/.rgb/.flag 子键——
// out_json 的 index 键仅在反查结果非 0 时输出，rgb 恒为完整 32 位
// rgb（含 method 高字节）的 %06x 形态，flag 仅非 0 时输出。
func lightAuditField(l *EntLight, key string) any {
	switch key {
	case "class_version":
		return int64(l.ClassVersion)
	case "name":
		return l.Name
	case "status":
		return B2int(l.Status)
	case "light_color":
		if !l.HasLightColorTrue {
			return int64(l.LightColorIndex)
		}
		return nil
	case "light_color.index":
		if l.HasLightColorTrue && l.LightColorIndex != 0 {
			return int64(l.LightColorIndex)
		}
		return nil
	case "light_color.rgb":
		if l.HasLightColorTrue {
			return fmt.Sprintf("%06x", l.LightColorRGB)
		}
		return nil
	case "light_color.flag":
		if l.HasLightColorTrue && l.LightColorFlag != 0 {
			return int64(l.LightColorFlag)
		}
		return nil
	case "plot_glyph":
		return B2int(l.PlotGlyph)
	case "intensity":
		return l.Intensity
	case "attenuation_type":
		return int64(l.AttenuationType)
	case "use_attenuation_limits":
		return B2int(l.UseAttenuationLimits)
	case "attenuation_start_limit":
		return l.AttenuationStart
	case "attenuation_end_limit":
		return l.AttenuationEnd
	case "hotspot_angle":
		return l.HotspotAngle
	case "falloff_angle":
		return l.FalloffAngle
	case "cast_shadows":
		return B2int(l.CastShadows)
	case "shadow_type":
		return int64(l.ShadowType)
	case "shadow_map_size":
		return int64(l.ShadowMapSize)
	case "shadow_map_softness":
		return int64(l.ShadowMapSoftness)
	case "position":
		return point3Arr(l.Position)
	case "target":
		return point3Arr(l.Target)
	}
	return nil // light_color 数组形态等不参与比对
}

// acisAuditField ACIS 系（REGION/3DSOLID/BODY）审计键导出。
func acisAuditField(a *EntAcis, key string) any {
	switch key {
	case "acis_empty":
		return B2int(a.AcisEmpty)
	case "acis_empty_bit":
		return B2int(a.AcisEmptyBit)
	case "unknown":
		return int64(a.Unknown)
	case "version":
		return int64(a.Version)
	case "wireframe_data_present":
		return B2int(a.WireframeDataPresent)
	case "point_present":
		return B2int(a.PointPresent)
	case "isolines":
		return int64(a.Isolines)
	case "isoline_present":
		return B2int(a.IsolinePresent)
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
					return B2int(sil.VpPerspective)
				case "has_wires":
					return B2int(sil.HasWires)
				}
				if v, ok := acisWireField(sil.Wires, restKey); ok {
					return v
				}
			}
		}
	}
	switch key {
	case "has_revision_guid":
		return B2int(a.HasRevisionGuid)
	case "revision_major":
		return int64(a.RevisionMajor)
	case "revision_minor1":
		return int64(a.RevisionMinor1)
	case "revision_minor2":
		return int64(a.RevisionMinor2)
	case "revision_bytes":
		return fmt.Sprintf("%X", a.revisionBytes)
	case "end_marker":
		return int64(a.EndMarker)
	case "point":
		// point_present=1 时的参考点（COMMON_3DSOLID）
		if a.PointPresent {
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
		return B2int(w.TransformPresent), true
	case "has_rotation":
		return B2int(w.HasRotation), true
	case "has_reflection":
		return B2int(w.HasReflection), true
	case "has_shear":
		return B2int(w.HasShear), true
	}
	return nil, false // points/axis_* 数组键
}

// wipeoutAuditField WIPEOUT 审计键导出（IMAGE 布局字段）。
func wipeoutAuditField(w *EntWipeout, key string) any {
	switch key {
	case "class_version":
		return int64(w.ClassVersion)
	case "display_props":
		return int64(w.DisplayProps)
	case "clipping":
		return B2int(w.Clipping)
	case "brightness":
		return int64(w.Brightness)
	case "contrast":
		return int64(w.Contrast)
	case "fade":
		return int64(w.Fade)
	case "clip_boundary_type":
		return int64(w.ClipBoundaryType)
	case "clip_mode":
		return int64(w.ClipMode)
	case "pt0":
		return point3Arr(w.Pt0)
	case "uvec":
		return Vec3Arr(w.Uvec)
	case "vvec":
		return Vec3Arr(w.Vvec)
	case "image_size":
		return []float64{w.ImageSize.X, w.ImageSize.Y}
	case "clip_verts":
		out := make([]float64, 0, len(w.ClipVerts)*2)
		for _, cv := range w.ClipVerts {
			out = append(out, cv.X, cv.Y)
		}
		return out
	}
	return nil // imagedef 等句柄键
}

// imageAuditField IMAGE 审计键导出（与 WIPEOUT 同布局，标量键同集合；
// pt0/uvec/vvec/image_size/clip_verts/imagedef 为数组或句柄键，
// gold 展平口径不进入标量对照）。
func imageAuditField(img *EntImage, key string) any {
	switch key {
	case "class_version":
		return int64(img.ClassVersion)
	case "display_props":
		return int64(img.DisplayProps)
	case "clipping":
		return B2int(img.Clipping)
	case "brightness":
		return int64(img.Brightness)
	case "contrast":
		return int64(img.Contrast)
	case "fade":
		return int64(img.Fade)
	case "clip_boundary_type":
		return int64(img.ClipBoundaryType)
	case "clip_mode":
		return int64(img.ClipMode)
	case "pt0":
		return point3Arr(img.Pt0)
	case "uvec":
		return Vec3Arr(img.Uvec)
	case "vvec":
		return Vec3Arr(img.Vvec)
	case "image_size":
		return []float64{img.ImageSize.X, img.ImageSize.Y}
	case "clip_verts":
		out := make([]float64, 0, len(img.ClipVerts)*2)
		for _, cv := range img.ClipVerts {
			out = append(out, cv.X, cv.Y)
		}
		return out
	case "imagedef":
		if img.ImageDef != 0 {
			return img.ImageDef
		}
		return nil
	case "imagedefreactor":
		if img.ImageDefReactor != 0 {
			return img.ImageDefReactor
		}
		return nil
	}
	return nil
}

// ole2FrameAuditField OLE2FRAME 审计键导出（gold 标量键 mode/type；
// data 为超长 hex 字符串，由对照测试直接断言，此处不重复导出）。
func Ole2FrameAuditField(o *EntOle2Frame, key string) any {
	switch key {
	case "type":
		return int64(o.OleType)
	case "mode":
		return int64(o.Mode)
	case "lock_aspect":
		return int64(o.LockAspect)
	case "data":
		return fmt.Sprintf("%X", o.Data)
	}
	return nil
}

// oleFrameAuditField OLEFRAME 审计键导出。
func OleFrameAuditField(o *EntOleFrame, key string) any {
	switch key {
	case "flag":
		return int64(o.Flag)
	case "mode":
		return int64(o.Mode)
	case "data":
		return fmt.Sprintf("%X", o.Data)
	}
	return nil
}

// proxyEntityAuditField PROXY_ENTITY 审计键导出（gold 标量键口径；
// proxy_data/data/objids 为数组键不进入标量对照，data_numbits 为
// LibreDWG DXF/JSON 导出键）。
func ProxyEntityAuditField(P *EntProxyEntity, key string) any {
	switch key {
	case "proxy_id":
		return int64(P.ProxyID)
	case "version":
		return int64(P.Version)
	case "maint_version":
		return int64(P.MaintVersion)
	case "dwg_version":
		return int64(P.DwgVersionNum)
	case "from_dxf":
		return B2int(P.FromDxf)
	case "data_numbits":
		return int64(P.DataNumBits)
	case "num_objids":
		return int64(P.NumObjids)
	case "proxy_data_size":
		return int64(P.ProxyDataSize)
	}
	return nil
}

// mpolygonAuditField MPOLYGON 审计键导出：主体标量键（含 HATCH 同构的
// 渐变/图案字段）+ 路径展平键复用 HATCH 路径导出。
func MpolygonAuditField(m *EntMpolygon, key string) any {
	switch key {
	case "style":
		return int64(m.Style)
	case "style_tail":
		return int64(m.StyleTail)
	case "x_dir":
		return []float64{m.XDir.X, m.XDir.Y}
	}
	if v := hatchAuditField(m.Hatch, key); v != nil {
		return v
	}
	// 展平嵌套键：paths[i]…（复用 HATCH 路径导出）
	if idx, rest, ok := auditArrayIndex(key, "paths"); ok {
		if idx < 0 || idx >= len(m.Hatch.Paths) {
			return nil
		}
		return HatchPathAuditField(&m.Hatch.Paths[idx], rest)
	}
	return nil
}

// hatchAuditField HATCH 审计键导出：主体标量键 + 路径/定义线的
// 展平嵌套键（paths[i].flag、paths[i].segs[j].curve_type、
// paths[i].polyline_paths[k].bulge、deflines[m].angle 等，与 gold
// JSON 展平口径一致）。数组键（knots/points/dashes 等）返回 nil，
// 由审计侧按非标量跳过。
func hatchAuditField(h *EntHatch, key string) any {
	switch key {
	case "elevation":
		return h.Elevation
	case "extrusion":
		return Vec3Arr(h.Extrusion)
	case "name":
		return h.Name
	case "is_solid_fill":
		return B2int(h.SolidFill)
	case "is_associative":
		return B2int(h.Associative)
	case "style":
		return int64(h.Style)
	case "pattern_type":
		return int64(h.PatternType)
	case "angle":
		return h.Angle
	case "scale_spacing":
		return h.ScaleSpacing
	case "double_flag":
		return B2int(h.DoubleFlag)
	case "is_gradient_fill":
		return int64(h.IsGradientFill)
	case "reserved":
		return int64(h.Reserved)
	case "gradient_angle":
		return h.GradientAngle
	case "gradient_shift":
		return h.GradientShift
	case "single_color_gradient":
		return int64(h.SingleColorGradient)
	case "gradient_tint":
		return h.GradientTint
	case "gradient_name":
		return h.GradientName
	case "has_derived":
		return B2int(h.HasDerived)
	case "pixel_size":
		if h.HasDerived {
			return h.PixelSize
		}
		return nil // gold 仅 has_derived=1 时输出
	case "seeds":
		// 种子点数组（gold 形态 [[x,y],...]，2RD 对）
		out := make([][]float64, 0, len(h.Seeds))
		for _, s := range h.Seeds {
			out = append(out, []float64{s.X, s.Y})
		}
		return out
	case "num_seeds", "deflines", "paths", "num_paths":
		return nil // 数组/计数码，审计按非标量跳过或无对照价值
	}
	if rest, ok := strings.CutPrefix(key, "colors["); ok {
		i := strings.Index(rest, "]")
		if i > 0 {
			idx, e := strconv.Atoi(rest[:i])
			if e == nil && idx >= 0 && idx < len(h.Colors) {
				gc := h.Colors[idx]
				switch rest[i+2:] {
				case "shift_value":
					return gc.ShiftValue
				case "color.index":
					return gc.ColorIndex
				case "color.rgb":
					return gc.ColorRGB
				}
			}
		}
		return nil
	}
	// 展平嵌套键：paths[i]… / deflines[i]…
	if idx, rest, ok := auditArrayIndex(key, "paths"); ok {
		if idx < 0 || idx >= len(h.Paths) {
			return nil
		}
		return HatchPathAuditField(&h.Paths[idx], rest)
	}
	if idx, rest, ok := auditArrayIndex(key, "deflines"); ok {
		if idx < 0 || idx >= len(h.Deflines) {
			return nil
		}
		return hatchDefLineAuditField(&h.Deflines[idx], rest)
	}
	return nil
}

// hatchPathAuditField HATCH 单条路径的展平键导出（rest 为 paths[i]. 之后的子键）。
func HatchPathAuditField(P *HatchPath, rest string) any {
	switch rest {
	case "flag":
		return int64(P.Flag)
	case "bulges_present":
		if !P.IsPolyline {
			return nil
		}
		return B2int(P.BulgesPresent)
	case "closed":
		if !P.IsPolyline {
			return nil
		}
		return B2int(P.Closed)
	case "num_segs_or_paths":
		return int64(P.NumSegsOrPaths)
	case "segs", "polyline_paths":
		return nil // 数组本身
	case "boundary_handles":
		// 边界对象句柄数组（handle 流，按 path 尾部计数读入；gold 逐位
		// 对照末位句柄值）
		if len(P.boundaryHandles) == 0 {
			return nil
		}
		out := make([]float64, 0, len(P.boundaryHandles))
		for _, hh := range P.boundaryHandles {
			out = append(out, float64(hh))
		}
		return out
	}
	if idx, sub, ok := auditArrayIndex(rest, "segs"); ok {
		if idx < 0 || idx >= len(P.Segs) {
			return nil
		}
		return HatchSegAuditField(&P.Segs[idx], sub)
	}
	if idx, sub, ok := auditArrayIndex(rest, "polyline_paths"); ok {
		if idx < 0 || idx >= len(P.PolyVerts) {
			return nil
		}
		switch sub {
		case "point":
			return nil // 数组
		case "bulge":
			return P.PolyVerts[idx].Bulge
		}
	}
	return nil
}

// hatchSegAuditField HATCH 边集段的展平键导出（sub 为 segs[j]. 之后的子键）。
func HatchSegAuditField(s *HatchSeg, sub string) any {
	switch sub {
	case "curve_type":
		return int64(s.CurveType)
	case "radius":
		return s.Radius
	case "minor_major_ratio":
		return s.Ratio
	case "start_angle":
		return s.StartAng
	case "end_angle":
		return s.EndAng
	case "is_ccw":
		return B2int(s.Ccw)
	case "degree":
		return int64(s.Degree)
	case "is_rational":
		return B2int(s.Rational)
	case "is_periodic":
		return B2int(s.Periodic)
	case "num_knots":
		return int64(len(s.Knots))
	case "num_control_points":
		return int64(len(s.Ctrl))
	case "num_fitpts":
		return int64(len(s.FitPts))
	case "first_endpoint":
		return Point2Arr(s.First)
	case "second_endpoint":
		return Point2Arr(s.Second)
	case "center":
		return Point2Arr(s.Center)
	case "endpoint":
		return Point2Arr(s.Endpoint)
	case "knots":
		return F64Arr(s.Knots)
	case "weights":
		return F64Arr(s.Weights)
	case "start_tangent":
		return Point2Arr(s.startTan)
	case "end_tangent":
		return Point2Arr(s.endTan)
	case "control_points", "fitpts":
		// gold 为 {point:[x,y]} 对象数组（flattenJSONGold 展平为
		// control_points[i].point），由数组下标键导出
		return nil
	}
	if idx, rest, ok := auditArrayIndex(sub, "control_points"); ok {
		if idx < 0 || idx >= len(s.Ctrl) {
			return nil
		}
		if rest == "point" {
			return Point2Arr(s.Ctrl[idx])
		}
		return nil
	}
	if idx, rest, ok := auditArrayIndex(sub, "fitpts"); ok {
		if idx < 0 || idx >= len(s.FitPts) {
			return nil
		}
		if rest == "point" {
			return Point2Arr(s.FitPts[idx])
		}
	}
	return nil
}

// hatchDefLineAuditField HATCH 定义线的展平键导出（rest 为 deflines[i]. 之后的子键）。
func hatchDefLineAuditField(dl *HatchDefLine, rest string) any {
	switch rest {
	case "angle":
		return dl.Angle
	case "num_dashes":
		return int64(len(dl.Dashes))
	case "pt0":
		return Point2Arr(dl.Pt0)
	case "offset":
		return Point2Arr(dl.Offset)
	case "dashes":
		return F64Arr(dl.Dashes)
	}
	return nil
}

// auditArrayIndex 解析展平键的数组下标前缀：key 形如 "paths[2].flag"、
// name 为 "paths" 时返回 (2, "flag")。无方括号返回 ok=false。
func auditArrayIndex(key, Name string) (idx int, rest string, ok bool) {
	if !strings.HasPrefix(key, Name+"[") {
		return 0, "", false
	}
	close := strings.Index(key, "]")
	if close < 0 {
		return 0, "", false
	}
	n, err := strconv.Atoi(key[len(Name)+1 : close])
	if err != nil {
		return 0, "", false
	}
	rest = key[close+1:]
	rest = strings.TrimPrefix(rest, ".")
	return n, rest, true
}

// b2int bool 转 0/1 整型。返回 int64 与审计值比较口径一致
// （auditValueMatch 的数值分支只识别 float64/int64/bool）。
func B2int(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func point3Arr(P Point3) []float64 { return []float64{P.X, P.Y, P.Z} }

func Vec3Arr(v Point3) []float64 { return []float64{v.X, v.Y, v.Z} }

func Pt2Arr(P []Point2) []float64 {
	out := make([]float64, 0, len(P)*2)
	for _, e := range P {
		out = append(out, e.X, e.Y)
	}
	return out
}

func F64Arr(v []float64) []float64 { return append([]float64(nil), v...) }

// colorAuditValue gold 的 color 键：ByLayer/索引色输出 ACI 索引；
// 真彩色输出 0xC0000000|RGB 形态的十进制值。
func colorAuditValue(c EntColor) any {
	if c.HasTrue {
		return int64(0xC0000000) | int64(c.TrueColor&0xFFFFFF)
	}
	if c.HasIndex {
		return int64(c.Index)
	}
	return int64(256) // BYLAYER
}

// nearF 浮点近似比较（审计容差与内部对象口径一致）。
func NearF(a, b float64) bool {
	if a == b {
		return true
	}
	return math.Abs(a-b) <= 1e-6*(math.Abs(a)+math.Abs(b)+1)
}

// entityValueEqual 实体字段值比较：float 容差、切片逐元素、其余直接相等。
func EntityValueEqual(got, want any) bool {
	switch w := want.(type) {
	case float64:
		g, ok := got.(float64)
		return ok && NearF(g, w)
	case []float64:
		g, ok := got.([]float64)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !NearF(g[i], w[i]) {
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

func Point2Arr(P Point2) []float64 { return []float64{P.X, P.Y} }

// p3sFlat 点数组展平为 [x,y,z,x,y,z,...]（gold 二维嵌套数组的导出形态，
// 测试侧 jsonTestValueMatch 做嵌套展开对照）。
func p3sFlat(pts []Point3) []float64 {
	out := make([]float64, 0, len(pts)*3)
	for _, P := range pts {
		out = append(out, P.X, P.Y, P.Z)
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
		for pi, P := range it.Pairs {
			if flat == idx {
				switch field {
				case "code":
					return P.Code
				case "size":
					if pi == 0 {
						return it.Size
					}
					return nil
				case "value":
					return P.Value
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
func underlayAuditField(u *EntUnderlay, key string) any {
	switch key {
	case "angle":
		// gold JSON 输出弧度原值（out_json 无角度制转换）
		return u.Angle
	case "flag":
		return int64(u.Flag)
	case "contrast":
		return int64(u.Contrast)
	case "fade":
		return int64(u.Fade)
	}
	return nil
}
