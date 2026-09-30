// entities_dimension.go 实现 DIMENSION 实体族（线性/对齐/角度/半径/直径/坐标）
// 的公共数据与各类型专属布局解码。字段序对照 LibreDWG dwg.spec
// COMMON_ENTITY_DIMENSION 与 ODA 20.4.22-20.4.27 逐位校准。
package cad

import "fmt"

// dimSpecificLayout 类型专属尾部布局（ODA spec 20.4.22-20.4.27）。
type dimSpecificLayout int

const (
	dimLayoutLinear   dimSpecificLayout = iota // 3BD13 + 3BD14 + 3BD10 + BD 扩线角 + BD 转角
	dimLayoutAligned                           // 3BD13 + 3BD14 + 3BD10 + BD 扩线角
	dimLayoutAng3Pt                            // 3BD10 + 3BD13 + 3BD14 + 3BD15
	dimLayoutAng2Ln                            // 2RD16 + 3BD13 + 3BD14 + 3BD15 + 3BD10
	dimLayoutOrdinate                          // 3BD10 + 3BD13 + 3BD14 + RC flags2
	dimLayoutRadius                            // 3BD10 + 3BD15 + BD 引线长
	dimLayoutDiameter                          // 3BD15 + 3BD10 + BD 引线长
	dimLayoutArc                               // 弧长：3BD def_pt + 3BD13 + 3BD14 + 3BD15 + B is_partial + BD×2 参数 + B has_leader + 3BD16 + 3BD17
)

// dimShape R2010+ 维度数据变体的特征位组合：bit0=流内 RC 版本字节、
// bit1=内联 TV 用户文字、bit2=挤出方向走 BE（1 位标志）而非 3BD、
// bit3=R2007+ 的 unknown/flip-arrow1/flip-arrow2 三个 B。
type dimShape uint8

const (
	dimShapeVersionByte dimShape = 1 << iota
	dimShapeUserText
	dimShapeExtrudeBE
	dimShapeR2007Flags
)

// r2010PlusDimShapes R2010+ 候选变体表（评分择优，平局取先）。
var r2010PlusDimShapes = []dimShape{
	dimShapeVersionByte | dimShapeUserText | dimShapeR2007Flags,
	dimShapeVersionByte | dimShapeR2007Flags,
	dimShapeUserText | dimShapeR2007Flags,
	dimShapeR2007Flags,
	dimShapeVersionByte | dimShapeUserText | dimShapeExtrudeBE | dimShapeR2007Flags,
	dimShapeVersionByte | dimShapeExtrudeBE | dimShapeR2007Flags,
	dimShapeUserText | dimShapeExtrudeBE | dimShapeR2007Flags,
	dimShapeExtrudeBE | dimShapeR2007Flags,
}

// R2000/R2004 变体特征位：bit0=attachment 组、bit1=unknown 位、
// bit2/3=flip_arrow1/2、bit4=clone_ins_pt(2RD)、bit5=style 段在公共头之前。
const (
	r2000ShapeAttachment dimShape = 1 << iota
	r2000ShapeUnknownBit
	r2000ShapeFlipArrow1
	r2000ShapeFlipArrow2
	r2000ShapeInsPoint
	r2000ShapeStyleFirst
)

// r2000DimShapes R2000/R2004 候选变体（规范布局优先，评分择优）。
var r2000DimShapes = []dimShape{
	// R13/R14 常见组合（无 attachment，clone_ins_pt 2RD 存在）
	r2000ShapeInsPoint,
	r2000ShapeAttachment | r2000ShapeInsPoint | r2000ShapeStyleFirst,
	r2000ShapeAttachment | r2000ShapeInsPoint,
	r2000ShapeAttachment | r2000ShapeUnknownBit | r2000ShapeFlipArrow1 | r2000ShapeFlipArrow2 | r2000ShapeInsPoint | r2000ShapeStyleFirst,
	r2000ShapeAttachment | r2000ShapeUnknownBit | r2000ShapeFlipArrow1 | r2000ShapeInsPoint | r2000ShapeStyleFirst,
	r2000ShapeAttachment | r2000ShapeUnknownBit | r2000ShapeInsPoint | r2000ShapeStyleFirst,
	r2000ShapeAttachment | r2000ShapeStyleFirst,
	r2000ShapeStyleFirst,
	r2000ShapeAttachment | r2000ShapeUnknownBit | r2000ShapeFlipArrow1 | r2000ShapeFlipArrow2 | r2000ShapeInsPoint,
	r2000ShapeAttachment | r2000ShapeUnknownBit | r2000ShapeFlipArrow1 | r2000ShapeInsPoint,
	r2000ShapeAttachment | r2000ShapeUnknownBit | r2000ShapeInsPoint,
	r2000ShapeAttachment,
	0,
}

// entDimension 标注实体（公共数据 + 类型专属字段）。
type entDimension struct {
	baseEntity
	extrusion         point3
	textMidpoint      point3
	elevation         float64
	dimFlags          uint8 // flag1：位流原始 RC 标志字节（合成 flag 见 dimFlag）
	dimFlag           uint8 // flag：按 LibreDWG DECODER 语义从 flag1/flag2 合成的输出标志
	flag2             uint8 // ORDINATE 专属 RC（bit7 覆盖）
	classVersion      uint8 // R2010+ class_version RC（恒 0）
	unknownFlag       bool  // R2007+ unknown B（spec 注释恒 0）
	flipArrow1        bool  // R2007+ flip_arrow1 B
	flipArrow2        bool  // R2007+ flip_arrow2 B
	userText          string
	textRotation      float64
	horizontalDir     float64
	insertScale       point3
	insertRotation    float64
	attachmentPoint   uint16
	lineSpacingStyle  uint16
	lineSpacingFactor float64
	actualMeasurement float64
	insertPoint       point3
	hasInsertPoint    bool
	dimstyleHandle    uint64
	anonymousBlock    uint64
	// 类型专属
	point13         point3
	point14         point3
	point10         point3
	extLineRotation float64
	dimRotation     float64
	point15         point3
	hasPoint15      bool
	point16x, p16y  float64
	hasPoint16      bool
	// 弧长专属（ARC_DIMENSION）
	defPt         point3
	isPartial     bool
	arcStartParam float64
	arcEndParam   float64
	hasLeader     bool
	leader1Pt     point3
	leader2Pt     point3
	leaderLen     float64 // RADIUS/DIAMETER 引线长（gold leader_len）
}

// dimSpecificData 类型专属字段解析结果。
type dimSpecificData struct {
	point13, point14, point10 point3
	extLineRotation           float64
	dimRotation               float64
	point15                   point3
	hasPoint15                bool
	p16x, p16y                float64
	hasPoint16                bool
	leaderLen                 float64 // RADIUS/DIAMETER 引线长（gold leader_len）
	flag2                     uint8   // ORDINATE 专属 RC
	// 弧长专属（ARC_DIMENSION）
	defPt         point3
	isPartial     bool
	arcStartParam float64
	arcEndParam   float64
	hasLeader     bool
	leader1Pt     point3
	leader2Pt     point3
}

// dimFieldStep 类型专属尾部的单字段读取步进。
type dimFieldStep func(r *bitStream, d *dimSpecificData) error

// dimStep3BD 读取一个 3BD 点到指定目标字段。
func dimStep3BD(dst func(*dimSpecificData) *point3, marked ...func(*dimSpecificData)) dimFieldStep {
	return func(r *bitStream, d *dimSpecificData) error {
		p, err := read3pt(r)
		if err != nil {
			return err
		}
		*dst(d) = p
		for _, mark := range marked {
			mark(d)
		}
		return nil
	}
}

// dimStepBD 读取一个 BD 到指定目标字段。
func dimStepBD(dst func(*dimSpecificData) *float64) dimFieldStep {
	return func(r *bitStream, d *dimSpecificData) error {
		v, err := r.readBD()
		if err != nil {
			return err
		}
		*dst(d) = v
		return nil
	}
}

// dimStepFlag 读取 1 位存入指定布尔字段。
func dimStepFlag(dst func(*dimSpecificData) *bool) dimFieldStep {
	return func(r *bitStream, d *dimSpecificData) error {
		v, err := r.readB()
		if err != nil {
			return err
		}
		*dst(d) = v != 0
		return nil
	}
}

// dimStep16 读取 2RD 到 (p16x, p16y)（ANG2LN 的 16 点）。
func dimStep16(r *bitStream, d *dimSpecificData) error {
	x, err := r.readRD()
	if err != nil {
		return err
	}
	y, err := r.readRD()
	if err != nil {
		return err
	}
	d.p16x, d.p16y = x, y
	d.hasPoint16 = true
	return nil
}

// dimStepFlag2 读取 ORDINATE 专属 RC 标志字节。
func dimStepFlag2(r *bitStream, d *dimSpecificData) error {
	v, err := r.readRC()
	if err != nil {
		return err
	}
	d.flag2 = v
	return nil
}

// dimStepLeaderLen 读取 RADIUS/DIAMETER 引线长。
func dimStepLeaderLen(r *bitStream, d *dimSpecificData) error {
	v, err := r.readBD()
	if err != nil {
		return err
	}
	d.leaderLen = v
	return nil
}

// dimStepArcParams 读取弧长段：is_partial 位 + 起末参数 + has_leader 位 +
// 两个引线点（dwg2.spec ARC_DIMENSION）。
func dimStepArcParams(r *bitStream, d *dimSpecificData) error {
	if err := dimStepFlag(func(d *dimSpecificData) *bool { return &d.isPartial })(r, d); err != nil {
		return err
	}
	if err := dimStepBD(func(d *dimSpecificData) *float64 { return &d.arcStartParam })(r, d); err != nil {
		return err
	}
	if err := dimStepBD(func(d *dimSpecificData) *float64 { return &d.arcEndParam })(r, d); err != nil {
		return err
	}
	if err := dimStepFlag(func(d *dimSpecificData) *bool { return &d.hasLeader })(r, d); err != nil {
		return err
	}
	if err := dimStep3BD(func(d *dimSpecificData) *point3 { return &d.leader1Pt })(r, d); err != nil {
		return err
	}
	return dimStep3BD(func(d *dimSpecificData) *point3 { return &d.leader2Pt })(r, d)
}

// dimLayoutSteps 各类型专属尾部的字段步进序列（ODA spec 20.4.22-20.4.27，
// ARC/LARGE_RADIAL 见各自注释）。读取顺序即步进顺序。
var dimLayoutSteps = map[dimSpecificLayout][]dimFieldStep{
	dimLayoutLinear: {
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.point13 }),
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.point14 }),
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.point10 }),
		dimStepBD(func(d *dimSpecificData) *float64 { return &d.extLineRotation }),
		dimStepBD(func(d *dimSpecificData) *float64 { return &d.dimRotation }),
	},
	dimLayoutAligned: {
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.point13 }),
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.point14 }),
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.point10 }),
		dimStepBD(func(d *dimSpecificData) *float64 { return &d.extLineRotation }),
	},
	dimLayoutAng3Pt: {
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.point10 }),
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.point13 }),
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.point14 }),
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.point15 }, func(d *dimSpecificData) { d.hasPoint15 = true }),
	},
	dimLayoutAng2Ln: {
		dimStep16,
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.point13 }),
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.point14 }),
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.point15 }, func(d *dimSpecificData) { d.hasPoint15 = true }),
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.point10 }),
	},
	dimLayoutOrdinate: {
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.point10 }),
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.point13 }),
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.point14 }),
		dimStepFlag2,
	},
	dimLayoutRadius: {
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.point10 }),
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.point15 }, func(d *dimSpecificData) { d.hasPoint15 = true }),
		dimStepLeaderLen,
	},
	dimLayoutDiameter: {
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.point15 }, func(d *dimSpecificData) { d.hasPoint15 = true }),
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.point10 }),
		dimStepLeaderLen,
	},
	dimLayoutArc: {
		// dwg2.spec ARC_DIMENSION：def_pt(0) + xline1_pt(13) + xline2_pt(14)
		// + center_pt(15) + is_partial(B) + arc_start_param(BD)
		// + arc_end_param(BD) + has_leader(B) + leader1_pt(16) + leader2_pt(17)
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.defPt }),
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.point13 }),
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.point14 }),
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.point15 }, func(d *dimSpecificData) { d.hasPoint15 = true }),
		dimStepArcParams,
	},
	dimLayoutLargeRadial: {
		// LARGE_RADIAL_DIMENSION 专属尾部（dwg.spec else 分支）：
		// def_pt + chord_pt + jog_angle + ovr_center + jog_pt。
		// 载体字段复用（无新增字段）：point13←def_pt、point14←chord_pt、
		// extLineRotation←jog_angle、point15←ovr_center、point10←jog_pt
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.point13 }),
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.point14 }),
		dimStepBD(func(d *dimSpecificData) *float64 { return &d.extLineRotation }),
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.point15 }, func(d *dimSpecificData) { d.hasPoint15 = true }),
		dimStep3BD(func(d *dimSpecificData) *point3 { return &d.point10 }),
	},
}

// readDimSpecific 按类型专属布局步进表解析尾部字段。
func readDimSpecific(r *bitStream, layout dimSpecificLayout) (dimSpecificData, error) {
	var d dimSpecificData
	for _, step := range dimLayoutSteps[layout] {
		if err := step(r, &d); err != nil {
			return d, err
		}
	}
	return d, nil
}

// decodeDimension 按版本解码标注实体。R13+ 各版本位流均为确定性布局
// （LibreDWG dwg.spec COMMON_ENTITY_DIMENSION，已经 dwgread -v9 trace
// 逐字段核对），优先确定性解码；公共头边界异常导致 body 中途失败时
// 才回退变体扫描兜底。
func decodeDimension(r *bitStream, head *commonEntityHead, ver dwgVersion, layout dimSpecificLayout) (any, error) {
	pos := r.tellBits()
	if d, err := decodeDimCanonical(r, head, ver, layout); err == nil {
		return d, nil
	}
	// 兜底：canonical 失败（公共头边界异常等）时从原起点走变体扫描。
	r.setBitPos(pos)
	if ver == verR2000 || ver == verR13 || ver == verR14 || ver == verR2004 {
		if ent, err := scanDimShapes(r, head, layout, r2000DimShapes, decodeDimR2000Variant); err == nil {
			return ent, nil
		} else {
			return ent, fmt.Errorf("cad: DIMENSION R2000 变体全部失败: %w", err)
		}
	}
	if ent, err := scanDimShapes(r, head, layout, r2010PlusDimShapes, decodeDimR2010PlusVariant); err == nil {
		return ent, nil
	} else {
		return ent, fmt.Errorf("cad: DIMENSION R2010+ 变体全部失败: %w", err)
	}
}

// dimVerR2007Plus R2007 及以后版本（R2007 三个标志位段起始版本）。
// 显式枚举判断，避免 iota 顺序变化时 >= 比较失真。
func dimVerR2007Plus(v dwgVersion) bool { return v == verR2007 || v.r2010Plus() }

// dimStringStream R2007+ 对象尾部字符串流读取器。R2007+ 的字符串内容
// 不占主位流，集中存放在 bitsize 前的 string stream 区域
// （LibreDWG obj_string_stream 口径，已经 trace 逐位核对）：
// bitsize-1 位是 has_strings 标志，其前是 data_size（RS,LE）指示的数据区。
type dimStringStream struct {
	r   *bitStream
	pos uint64 // 字符串流自身的读取位（独立于主位流游标）
}

// newDimStringStream 定位字符串流数据区起点；失败返回 nil
// （此时各 T 字段按空串处理，与 !has_strings 行为一致）。
// 主位流游标在返回前恢复到 enterPos。
func newDimStringStream(r *bitStream, objSizeBit, enterPos uint64) *dimStringStream {
	defer r.setBitPos(enterPos)
	p0 := int64(objSizeBit) - 1
	if p0 < 66 { // 1 位标志 + 2~6 字节 size 指示 + 至少一个字符串的余量
		return nil
	}
	r.setBitPos(uint64(p0))
	has, err := r.readB()
	if err != nil || has == 0 {
		return nil
	}
	// 回退到 p0-16 位读 data_size（RS 小端）
	r.setBitPos(uint64(p0) - 16)
	dataSize, err := r.readRS()
	if err != nil {
		return nil
	}
	if dataSize&0x8000 != 0 {
		// 扩展：hi 16 位在更前 2 字节，data_size 回退 4 字节重读
		r.setBitPos(uint64(p0) - 48)
		hi, e := r.readRS()
		if e != nil {
			return nil
		}
		dataSize = dataSize&0x7FFF | hi<<15
		if uint64(dataSize) > objSizeBit {
			return nil
		}
		r.setBitPos(uint64(p0) - 32 - uint64(dataSize))
	} else {
		if uint64(dataSize) > objSizeBit {
			return nil
		}
		r.setBitPos(uint64(p0) - 16 - uint64(dataSize))
	}
	return &dimStringStream{r: r, pos: r.tellBits()}
}

// readTU 从字符串流读下一个 TU 文本；流不可用或读失败返回空串。
// 借用主位流游标读取，结束时恢复到 enterPos（主位流 0 位推进语义）。
func (s *dimStringStream) readTU(r *bitStream, enterPos uint64) string {
	if s == nil {
		return ""
	}
	r.setBitPos(s.pos)
	tu, err := s.r.readTU()
	if err != nil {
		return ""
	}
	s.pos = s.r.tellBits()
	r.setBitPos(enterPos)
	return tu
}

// decodeDimCanonical 按 LibreDWG 确定性布局解析 DIMENSION：
// [R2010+ class_version RC] extrusion 3BD → text_midpt 2RD → elevation BD →
// flag1 RC → user_text T → text_rotation BD → horiz_dir BD → ins_scale 3BD →
// ins_rotation BD → [R2000+ attachment/lspace/measurement] →
// [R2007+ unknown/flip×2] → clone_ins_pt 2RD → 类型专属尾部 → handle 流
// （common → dimstyle → block，顺序与 dwg.spec 一致）。
func decodeDimCanonical(r *bitStream, head *commonEntityHead, ver dwgVersion, layout dimSpecificLayout) (*entDimension, error) {
	trOn := cadTraceHandle != 0 && cadTraceHandle == head.handle
	// dimR2000Plus attachment 段起始于 R2000；注意 verR2000 是枚举零值，
	// 不可用 >= 判断（R13/R14 枚举值更大但布局更旧）。
	dimR2000Plus := ver == verR2000 || ver == verR2004 || dimVerR2007Plus(ver)
	d := &entDimension{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}}
	var err error
	traceRC := func(name string, dst *uint8) error {
		pos := r.tellBits()
		v, e := r.readRC()
		if e == nil {
			*dst = v
			if trOn {
				cadTraceField(trOn, pos, r.tellBits(), name, fmt.Sprintf("%#x", v))
			}
		}
		return e
	}
	traceBD := func(name string, dst *float64) error {
		pos := r.tellBits()
		v, e := r.readBD()
		if e == nil {
			*dst = v
			if trOn {
				cadTraceField(trOn, pos, r.tellBits(), name, fmt.Sprintf("%g", v))
			}
		}
		return e
	}
	trace3BD := func(name string, dst *point3) error {
		pos := r.tellBits()
		v, e := read3pt(r)
		if e == nil {
			*dst = v
			if trOn {
				cadTraceField(trOn, pos, r.tellBits(), name, fmt.Sprintf("(%g,%g,%g)", v.x, v.y, v.z))
			}
		}
		return e
	}
	if ver.r2010Plus() {
		if err = traceRC("class_version", &d.classVersion); err != nil {
			return nil, err
		}
	}
	if err = trace3BD("extrusion", &d.extrusion); err != nil {
		return nil, err
	}
	var mx, my float64
	pos := r.tellBits()
	if mx, err = r.readRD(); err != nil {
		return nil, err
	}
	if my, err = r.readRD(); err != nil {
		return nil, err
	}
	if trOn {
		cadTraceField(trOn, pos, r.tellBits(), "text_midpt", fmt.Sprintf("(%g,%g)", mx, my))
	}
	if err = traceBD("elevation", &d.elevation); err != nil {
		return nil, err
	}
	d.textMidpoint = point3{mx, my, d.elevation}
	if err = traceRC("flag1", &d.dimFlags); err != nil {
		return nil, err
	}
	// R2007+ 的 T 字段内容在字符串流中，主位流 0 位；R13-R2004 的 TV 在主位流。
	var ss *dimStringStream
	if dimVerR2007Plus(ver) {
		ss = newDimStringStream(r, head.objSizeBit, r.tellBits())
	}
	pos = r.tellBits()
	if dimVerR2007Plus(ver) {
		d.userText = ss.readTU(r, pos)
	} else {
		if d.userText, err = r.readTV(512); err != nil {
			return nil, err
		}
	}
	cadTraceField(trOn, pos, r.tellBits(), "user_text", d.userText)
	if err = traceBD("text_rotation", &d.textRotation); err != nil {
		return nil, err
	}
	if err = traceBD("horiz_dir", &d.horizontalDir); err != nil {
		return nil, err
	}
	if err = trace3BD("ins_scale", &d.insertScale); err != nil {
		return nil, err
	}
	if err = traceBD("ins_rotation", &d.insertRotation); err != nil {
		return nil, err
	}
	if dimR2000Plus {
		pos = r.tellBits()
		if d.attachmentPoint, err = r.readBS(); err != nil {
			return nil, err
		}
		cadTraceFieldInt(trOn, pos, r.tellBits(), "attachment", int64(d.attachmentPoint))
		pos = r.tellBits()
		if d.lineSpacingStyle, err = r.readBS(); err != nil {
			return nil, err
		}
		cadTraceFieldInt(trOn, pos, r.tellBits(), "lspace_style", int64(d.lineSpacingStyle))
		if err = traceBD("lspace_factor", &d.lineSpacingFactor); err != nil {
			return nil, err
		}
		if err = traceBD("act_measurement", &d.actualMeasurement); err != nil {
			return nil, err
		}
	}
	if dimVerR2007Plus(ver) {
		pos = r.tellBits()
		var b uint8
		if b, err = r.readB(); err != nil {
			return nil, err
		}
		d.unknownFlag = b != 0
		cadTraceFieldInt(trOn, pos, r.tellBits(), "unknown", int64(b))
		if b, err = r.readB(); err != nil {
			return nil, err
		}
		d.flipArrow1 = b != 0
		cadTraceFieldInt(trOn, pos+1, r.tellBits(), "flip_arrow1", int64(b))
		if b, err = r.readB(); err != nil {
			return nil, err
		}
		d.flipArrow2 = b != 0
		cadTraceFieldInt(trOn, pos+2, r.tellBits(), "flip_arrow2", int64(b))
	}
	var p12x, p12y float64
	pos = r.tellBits()
	if p12x, err = r.readRD(); err != nil {
		return nil, err
	}
	if p12y, err = r.readRD(); err != nil {
		return nil, err
	}
	if trOn {
		cadTraceField(trOn, pos, r.tellBits(), "clone_ins_pt", fmt.Sprintf("(%g,%g)", p12x, p12y))
	}
	d.insertPoint = point3{p12x, p12y, d.elevation}
	d.hasInsertPoint = true
	var spec dimSpecificData
	if spec, err = readDimSpecific(r, layout); err != nil {
		return nil, err
	}
	applyDimSpecific(d, spec)
	d.computeDimFlag(layout)
	decodeDimHandles(r, head, d)
	return d, nil
}

// applyDimSpecific 把类型专属尾部解析结果写入实体字段。
func applyDimSpecific(d *entDimension, spec dimSpecificData) {
	d.point13, d.point14, d.point10 = spec.point13, spec.point14, spec.point10
	d.extLineRotation, d.dimRotation = spec.extLineRotation, spec.dimRotation
	d.point15, d.hasPoint15 = spec.point15, spec.hasPoint15
	d.point16x, d.p16y, d.hasPoint16 = spec.p16x, spec.p16y, spec.hasPoint16
	d.flag2 = spec.flag2
	d.defPt, d.isPartial = spec.defPt, spec.isPartial
	d.arcStartParam, d.arcEndParam = spec.arcStartParam, spec.arcEndParam
	d.hasLeader = spec.hasLeader
	d.leader1Pt, d.leader2Pt = spec.leader1Pt, spec.leader2Pt
	d.leaderLen = spec.leaderLen
}

// computeDimFlag 按 LibreDWG DIMENSION DECODER 语义合成输出 flag：
// 保留 flag1 高 3 位；bit7 取 flag1 bit0 的反（非默认样式位）；
// bit5 取 flag1 bit1（R13 起恒置 1）；低 3 位按实体类型补齐
// （ALIGNED=1/ANG2LN=2/DIAMETER=3/RADIUS=4/ANG3PT=5/ORDINATE=6/LINEAR=0；
// ARC_DIMENSION 同 ANG3PT 取 5，见 dwg_spec_shared.h DECODER 分支）；
// ORDINATE 再用 flag2 bit0 覆盖 bit7。
func (d *entDimension) computeDimFlag(layout dimSpecificLayout) {
	flag := d.dimFlags & 0xe0
	if d.dimFlags&1 != 0 {
		flag &= 0x7f
	} else {
		flag |= 0x80
	}
	if d.dimFlags&2 != 0 {
		flag |= 0x20
	} else {
		flag &= 0xdf
	}
	switch layout {
	case dimLayoutAligned:
		flag |= 1
	case dimLayoutAng2Ln:
		flag |= 2
	case dimLayoutDiameter:
		flag |= 3
	case dimLayoutRadius:
		flag |= 4
	case dimLayoutAng3Pt, dimLayoutArc:
		flag |= 5
	case dimLayoutOrdinate:
		flag |= 6
	}
	if layout == dimLayoutOrdinate {
		if d.flag2&1 != 0 {
			flag |= 0x80
		} else {
			flag &^= 0x80
		}
	}
	d.dimFlag = flag
}

// scanDimShapes 遍历候选变体表取最优：逐变体从同一起点试解，按
// dimPlausibilityScore 评分择优（值小者胜，平局取先）；全部失败时
// 返回末次错误。
func scanDimShapes(r *bitStream, head *commonEntityHead, layout dimSpecificLayout, shapes []dimShape, decode func(*bitStream, *commonEntityHead, dimShape, dimSpecificLayout) (*entDimension, error)) (any, error) {
	pos := r.tellBits()
	var best *entDimension
	bestScore := uint64(0)
	var lastErr error
	for _, shape := range shapes {
		r.setBitPos(pos)
		ent, err := decode(r, head, shape, layout)
		if err != nil {
			lastErr = err
			continue
		}
		score := dimPlausibilityScore(ent)
		if best == nil || score < bestScore {
			best, bestScore = ent, score
		}
	}
	if best != nil {
		return best, nil
	}
	return nil, lastErr
}

// decodeDimR2010PlusVariant 按单一 R2010+ 变体解析。
func decodeDimR2010PlusVariant(r *bitStream, head *commonEntityHead, shape dimShape, layout dimSpecificLayout) (*entDimension, error) {
	if shape&dimShapeVersionByte != 0 {
		if _, err := r.readRC(); err != nil {
			return nil, err
		}
	}
	d := &entDimension{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}}
	var err error
	if shape&dimShapeExtrudeBE != 0 {
		x, y, z, e := r.readBE()
		if e != nil {
			return nil, e
		}
		d.extrusion = point3{x, y, z}
	} else {
		if d.extrusion, err = read3pt(r); err != nil {
			return nil, err
		}
	}
	mx, my, err := r.read2RD()
	if err != nil {
		return nil, err
	}
	if d.elevation, err = r.readBD(); err != nil {
		return nil, err
	}
	d.textMidpoint = point3{mx, my, d.elevation}
	if d.dimFlags, err = r.readRC(); err != nil {
		return nil, err
	}
	if shape&dimShapeUserText != 0 {
		if d.userText, err = r.readTV(512); err != nil {
			return nil, err
		}
	}
	if err = readDimCommonTail(r, d); err != nil {
		return nil, err
	}
	if shape&dimShapeR2007Flags != 0 {
		for i := 0; i < 3; i++ { // unknown + flip_arrow1 + flip_arrow2
			if _, err = r.readB(); err != nil {
				return nil, err
			}
		}
	}
	p12x, p12y, err := r.read2RD()
	if err != nil {
		return nil, err
	}
	d.insertPoint = point3{p12x, p12y, d.elevation}
	d.hasInsertPoint = true
	return finishDimension(r, head, d, layout)
}

// readDimCommonTail 维度公共尾段（R2010+ 形态）：text_rotation → horiz_dir →
// ins_scale → ins_rotation → attachment → linespacing style/factor →
// actual_measurement（后四项无条件存在）。
func readDimCommonTail(r *bitStream, d *entDimension) error {
	steps := append(dimTailThroughRotation(r, d),
		func(r *bitStream, d *entDimension) error { var e error; d.attachmentPoint, e = r.readBS(); return e },
		func(r *bitStream, d *entDimension) error { var e error; d.lineSpacingStyle, e = r.readBS(); return e },
		func(r *bitStream, d *entDimension) error { var e error; d.lineSpacingFactor, e = r.readBD(); return e },
		func(r *bitStream, d *entDimension) error { var e error; d.actualMeasurement, e = r.readBD(); return e },
	)
	for _, step := range steps {
		if err := step(r, d); err != nil {
			return err
		}
	}
	return nil
}

// dimTailThroughRotation R2000 族与 R2010+ 共有的前四步：
// text_rotation → horiz_dir → ins_scale → ins_rotation。
func dimTailThroughRotation(r *bitStream, d *entDimension) []func(*bitStream, *entDimension) error {
	return []func(*bitStream, *entDimension) error{
		func(r *bitStream, d *entDimension) error { var e error; d.textRotation, e = r.readBD(); return e },
		func(r *bitStream, d *entDimension) error { var e error; d.horizontalDir, e = r.readBD(); return e },
		func(r *bitStream, d *entDimension) error { var e error; d.insertScale, e = read3pt(r); return e },
		func(r *bitStream, d *entDimension) error { var e error; d.insertRotation, e = r.readBD(); return e },
	}
}

// finishDimension 收尾：类型专属段解析 + 标志合成 + handle 流。
func finishDimension(r *bitStream, head *commonEntityHead, d *entDimension, layout dimSpecificLayout) (*entDimension, error) {
	spec, err := readDimSpecific(r, layout)
	if err != nil {
		return nil, err
	}
	applyDimSpecific(d, spec)
	d.computeDimFlag(layout)
	decodeDimHandles(r, head, d)
	return d, nil
}

// decodeDimR2000Variant 按单一 R2000/R2004 变体解析。
func decodeDimR2000Variant(r *bitStream, head *commonEntityHead, shape dimShape, layout dimSpecificLayout) (*entDimension, error) {
	d := &entDimension{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}}
	var err error
	if d.extrusion, err = read3pt(r); err != nil {
		return nil, err
	}
	mx, my, err := r.read2RD()
	if err != nil {
		return nil, err
	}
	if d.elevation, err = r.readBD(); err != nil {
		return nil, err
	}
	d.textMidpoint = point3{mx, my, d.elevation}
	if d.dimFlags, err = r.readRC(); err != nil {
		return nil, err
	}
	if d.userText, err = r.readTV(512); err != nil {
		return nil, err
	}
	// R2000 族公共段止于 ins_rotation；attachment/linespacing 组由变体位决定
	for _, step := range dimTailThroughRotation(r, d) {
		if err = step(r, d); err != nil {
			return nil, err
		}
	}
	if shape&r2000ShapeAttachment != 0 {
		var att uint16
		if att, err = r.readBS(); err != nil {
			return nil, err
		}
		d.attachmentPoint = att
		var lsStyle uint16
		if lsStyle, err = r.readBS(); err != nil {
			return nil, err
		}
		d.lineSpacingStyle = lsStyle
		if d.lineSpacingFactor, err = r.readBD(); err != nil {
			return nil, err
		}
		if d.actualMeasurement, err = r.readBD(); err != nil {
			return nil, err
		}
	}
	if shape&r2000ShapeUnknownBit != 0 {
		if _, err = r.readB(); err != nil {
			return nil, err
		}
	}
	if shape&r2000ShapeFlipArrow1 != 0 {
		if _, err = r.readB(); err != nil {
			return nil, err
		}
	}
	if shape&r2000ShapeFlipArrow2 != 0 {
		if _, err = r.readB(); err != nil {
			return nil, err
		}
	}
	if shape&r2000ShapeInsPoint != 0 {
		p12x, p12y, e := r.read2RD()
		if e != nil {
			return nil, e
		}
		d.insertPoint = point3{p12x, p12y, d.elevation}
		d.hasInsertPoint = true
	}
	return finishDimension(r, head, d, layout)
}

// decodeDimHandles 解析 handle 流：common → dimstyle + anonymous block。
// 顺序与 dwg.spec 一致（COMMON_ENTITY_HANDLE_DATA 在 dimstyle/block 之前，
// 已由 exr13/ex2004/ex2018 trace 核对）。失败时容忍（退化为仅图层句柄）。
func decodeDimHandles(r *bitStream, head *commonEntityHead, d *entDimension) {
	r.setBitPos(head.objSizeBit)
	owner, layer, e3 := parseCommonEntityHandles(r, head)
	_ = owner
	dimstyle, e1 := readHandleReference(r, head.handle)
	block, e2 := readHandleReference(r, head.handle)
	if e1 == nil && e2 == nil && e3 == nil {
		d.dimstyleHandle, d.anonymousBlock, d.layer = dimstyle, block, layer
	} else {
		r.setBitPos(head.objSizeBit)
		if _, layer, e := parseCommonEntityHandles(r, head); e == nil {
			d.layer = layer
		}
	}
}

// read3pt 读取 3BD 为 point3。
func read3pt(r *bitStream) (point3, error) {
	x, y, z, err := r.read3BD()
	return point3{x, y, z}, err
}

// dimPlausibilityScore 标注字段合理性评分（值越小越可信）：
// 微量级垃圾值、天文角度与越界枚举是错位候选的主要特征。
func dimPlausibilityScore(d *entDimension) uint64 {
	score := uint64(0)
	for _, p := range []point3{d.point10, d.point13, d.point14, d.textMidpoint} {
		score += dimPointScore(p)
	}
	if d.hasInsertPoint {
		score += dimPointScore(d.insertPoint)
	}
	if d.hasPoint15 {
		score += dimPointScore(d.point15)
	}
	if d.hasPoint16 {
		score += dimValueScore(d.point16x) + dimValueScore(d.p16y)
	}
	score += dimPointScore(d.extrusion)
	score += dimPointScore(d.insertScale)
	score += dimAngleScore(d.textRotation)
	score += dimAngleScore(d.horizontalDir)
	score += dimAngleScore(d.extLineRotation)
	score += dimAngleScore(d.dimRotation)
	score += dimAngleScore(d.insertRotation)
	score += dimValueScore(d.actualMeasurement)
	score += dimValueScore(d.lineSpacingFactor)
	if d.attachmentPoint > 9 {
		score += 10000
	}
	if d.lineSpacingStyle > 2 {
		score += 10000
	}
	if d.dimFlags > 0x3F {
		score += 1000
	}
	return score
}

// dimGarbageMagnitude 低于该量级（但非零）的值是错位读取的特征。
const dimGarbageMagnitude = 1.0e-30

// dimAngleScore 角度合理性：|v|>1000 渐进惩罚。
func dimAngleScore(v float64) uint64 {
	abs := absF(v)
	if abs > 0 && abs < dimGarbageMagnitude {
		return 5000
	}
	switch {
	case abs <= 1000:
		return 0
	case abs <= 1e6:
		return 25
	case abs <= 1e12:
		return 250
	default:
		return 1_000_000
	}
}

// dimValueScore 数值合理性：超过 1e6 渐进惩罚，非有限值重罚。
func dimValueScore(v float64) uint64 {
	abs := absF(v)
	if abs > 0 && abs < dimGarbageMagnitude {
		return 5000
	}
	switch {
	case abs <= 1e6:
		return 0
	case abs <= 1e9:
		return 10
	case abs <= 1e12:
		return 100
	case abs <= 1e18:
		return 1000
	case abs <= 1e24:
		return 10000
	default:
		return 1_000_000
	}
}

// dimPointScore 三坐标数值评分之和。
func dimPointScore(p point3) uint64 {
	return dimValueScore(p.x) + dimValueScore(p.y) + dimValueScore(p.z)
}

// absF 浮点绝对值。
func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
