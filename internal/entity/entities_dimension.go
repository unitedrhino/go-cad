// entities_dimension.go 实现 DIMENSION 实体族（线性/对齐/角度/半径/直径/坐标）
// 的公共数据与各类型专属布局解码。字段序对照 LibreDWG dwg.spec
// COMMON_ENTITY_DIMENSION 与 ODA 20.4.22-20.4.27 逐位校准。
package entity

import (
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/objrec"
)

// dimSpecificLayout 类型专属尾部布局（ODA spec 20.4.22-20.4.27）。
type DimSpecificLayout int

const (
	DimLayoutLinear   DimSpecificLayout = iota // 3BD13 + 3BD14 + 3BD10 + BD 扩线角 + BD 转角
	DimLayoutAligned                           // 3BD13 + 3BD14 + 3BD10 + BD 扩线角
	DimLayoutAng3Pt                            // 3BD10 + 3BD13 + 3BD14 + 3BD15
	DimLayoutAng2Ln                            // 2RD16 + 3BD13 + 3BD14 + 3BD15 + 3BD10
	DimLayoutOrdinate                          // 3BD10 + 3BD13 + 3BD14 + RC flags2
	DimLayoutRadius                            // 3BD10 + 3BD15 + BD 引线长
	DimLayoutDiameter                          // 3BD15 + 3BD10 + BD 引线长
	DimLayoutArc                               // 弧长：3BD def_pt + 3BD13 + 3BD14 + 3BD15 + B is_partial + BD×2 参数 + B has_leader + 3BD16 + 3BD17
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
var R2000DimShapes = []dimShape{
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
type EntDimension struct {
	BaseEntity
	Extrusion         Point3
	TextMidpoint      Point3
	Elevation         float64
	DimFlags          uint8 // flag1：位流原始 RC 标志字节（合成 flag 见 dimFlag）
	DimFlag           uint8 // flag：按 LibreDWG DECODER 语义从 flag1/flag2 合成的输出标志
	Flag2             uint8 // ORDINATE 专属 RC（bit7 覆盖）
	ClassVersion      uint8 // R2010+ class_version RC（恒 0）
	unknownFlag       bool  // R2007+ unknown B（spec 注释恒 0）
	flipArrow1        bool  // R2007+ flip_arrow1 B
	flipArrow2        bool  // R2007+ flip_arrow2 B
	UserText          string
	TextRotation      float64
	HorizontalDir     float64
	InsertScale       Point3
	InsertRotation    float64
	AttachmentPoint   uint16
	LineSpacingStyle  uint16
	LineSpacingFactor float64
	ActualMeasurement float64
	InsertPoint       Point3
	HasInsertPoint    bool
	DimstyleHandle    uint64
	AnonymousBlock    uint64
	// 类型专属
	Point13         Point3
	Point14         Point3
	Point10         Point3
	ExtLineRotation float64
	DimRotation     float64
	Point15         Point3
	HasPoint15      bool
	Point16x, P16y  float64
	HasPoint16      bool
	// 弧长专属（ARC_DIMENSION）
	DefPt         Point3
	IsPartial     bool
	ArcStartParam float64
	ArcEndParam   float64
	HasLeader     bool
	Leader1Pt     Point3
	Leader2Pt     Point3
	LeaderLen     float64 // RADIUS/DIAMETER 引线长（gold leader_len）
}

// dimSpecificData 类型专属字段解析结果。
type dimSpecificData struct {
	Point13, Point14, Point10 Point3
	ExtLineRotation           float64
	DimRotation               float64
	Point15                   Point3
	HasPoint15                bool
	p16x, P16y                float64
	HasPoint16                bool
	LeaderLen                 float64 // RADIUS/DIAMETER 引线长（gold leader_len）
	Flag2                     uint8   // ORDINATE 专属 RC
	// 弧长专属（ARC_DIMENSION）
	DefPt         Point3
	IsPartial     bool
	ArcStartParam float64
	ArcEndParam   float64
	HasLeader     bool
	Leader1Pt     Point3
	Leader2Pt     Point3
}

// dimFieldStep 类型专属尾部的单字段读取步进。
type dimFieldStep func(r *bitstream.BitStream, d *dimSpecificData) error

// dimStep3BD 读取一个 3BD 点到指定目标字段。
func dimStep3BD(dst func(*dimSpecificData) *Point3, marked ...func(*dimSpecificData)) dimFieldStep {
	return func(r *bitstream.BitStream, d *dimSpecificData) error {
		P, err := read3pt(r)
		if err != nil {
			return err
		}
		*dst(d) = P
		for _, mark := range marked {
			mark(d)
		}
		return nil
	}
}

// dimStepBD 读取一个 BD 到指定目标字段。
func dimStepBD(dst func(*dimSpecificData) *float64) dimFieldStep {
	return func(r *bitstream.BitStream, d *dimSpecificData) error {
		v, err := r.ReadBD()
		if err != nil {
			return err
		}
		*dst(d) = v
		return nil
	}
}

// dimStepFlag 读取 1 位存入指定布尔字段。
func dimStepFlag(dst func(*dimSpecificData) *bool) dimFieldStep {
	return func(r *bitstream.BitStream, d *dimSpecificData) error {
		v, err := r.ReadB()
		if err != nil {
			return err
		}
		*dst(d) = v != 0
		return nil
	}
}

// dimStep16 读取 2RD 到 (p16x, p16y)（ANG2LN 的 16 点）。
func dimStep16(r *bitstream.BitStream, d *dimSpecificData) error {
	X, err := r.ReadRD()
	if err != nil {
		return err
	}
	Y, err := r.ReadRD()
	if err != nil {
		return err
	}
	d.p16x, d.P16y = X, Y
	d.HasPoint16 = true
	return nil
}

// dimStepFlag2 读取 ORDINATE 专属 RC 标志字节。
func dimStepFlag2(r *bitstream.BitStream, d *dimSpecificData) error {
	v, err := r.ReadRC()
	if err != nil {
		return err
	}
	d.Flag2 = v
	return nil
}

// dimStepLeaderLen 读取 RADIUS/DIAMETER 引线长。
func dimStepLeaderLen(r *bitstream.BitStream, d *dimSpecificData) error {
	v, err := r.ReadBD()
	if err != nil {
		return err
	}
	d.LeaderLen = v
	return nil
}

// dimStepArcParams 读取弧长段：is_partial 位 + 起末参数 + has_leader 位 +
// 两个引线点（dwg2.spec ARC_DIMENSION）。
func dimStepArcParams(r *bitstream.BitStream, d *dimSpecificData) error {
	if err := dimStepFlag(func(d *dimSpecificData) *bool { return &d.IsPartial })(r, d); err != nil {
		return err
	}
	if err := dimStepBD(func(d *dimSpecificData) *float64 { return &d.ArcStartParam })(r, d); err != nil {
		return err
	}
	if err := dimStepBD(func(d *dimSpecificData) *float64 { return &d.ArcEndParam })(r, d); err != nil {
		return err
	}
	if err := dimStepFlag(func(d *dimSpecificData) *bool { return &d.HasLeader })(r, d); err != nil {
		return err
	}
	if err := dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Leader1Pt })(r, d); err != nil {
		return err
	}
	return dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Leader2Pt })(r, d)
}

// dimLayoutSteps 各类型专属尾部的字段步进序列（ODA spec 20.4.22-20.4.27，
// ARC/LARGE_RADIAL 见各自注释）。读取顺序即步进顺序。
var dimLayoutSteps = map[DimSpecificLayout][]dimFieldStep{
	DimLayoutLinear: {
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Point13 }),
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Point14 }),
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Point10 }),
		dimStepBD(func(d *dimSpecificData) *float64 { return &d.ExtLineRotation }),
		dimStepBD(func(d *dimSpecificData) *float64 { return &d.DimRotation }),
	},
	DimLayoutAligned: {
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Point13 }),
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Point14 }),
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Point10 }),
		dimStepBD(func(d *dimSpecificData) *float64 { return &d.ExtLineRotation }),
	},
	DimLayoutAng3Pt: {
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Point10 }),
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Point13 }),
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Point14 }),
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Point15 }, func(d *dimSpecificData) { d.HasPoint15 = true }),
	},
	DimLayoutAng2Ln: {
		dimStep16,
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Point13 }),
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Point14 }),
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Point15 }, func(d *dimSpecificData) { d.HasPoint15 = true }),
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Point10 }),
	},
	DimLayoutOrdinate: {
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Point10 }),
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Point13 }),
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Point14 }),
		dimStepFlag2,
	},
	DimLayoutRadius: {
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Point10 }),
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Point15 }, func(d *dimSpecificData) { d.HasPoint15 = true }),
		dimStepLeaderLen,
	},
	DimLayoutDiameter: {
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Point15 }, func(d *dimSpecificData) { d.HasPoint15 = true }),
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Point10 }),
		dimStepLeaderLen,
	},
	DimLayoutArc: {
		// dwg2.spec ARC_DIMENSION：def_pt(0) + xline1_pt(13) + xline2_pt(14)
		// + center_pt(15) + is_partial(B) + arc_start_param(BD)
		// + arc_end_param(BD) + has_leader(B) + leader1_pt(16) + leader2_pt(17)
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.DefPt }),
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Point13 }),
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Point14 }),
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Point15 }, func(d *dimSpecificData) { d.HasPoint15 = true }),
		dimStepArcParams,
	},
	DimLayoutLargeRadial: {
		// LARGE_RADIAL_DIMENSION 专属尾部（dwg.spec else 分支）：
		// def_pt + chord_pt + jog_angle + ovr_center + jog_pt。
		// 载体字段复用（无新增字段）：point13←def_pt、point14←chord_pt、
		// extLineRotation←jog_angle、point15←ovr_center、point10←jog_pt
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Point13 }),
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Point14 }),
		dimStepBD(func(d *dimSpecificData) *float64 { return &d.ExtLineRotation }),
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Point15 }, func(d *dimSpecificData) { d.HasPoint15 = true }),
		dimStep3BD(func(d *dimSpecificData) *Point3 { return &d.Point10 }),
	},
}

// readDimSpecific 按类型专属布局步进表解析尾部字段。
func readDimSpecific(r *bitstream.BitStream, layout DimSpecificLayout) (dimSpecificData, error) {
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
func DecodeDimension(r *bitstream.BitStream, Head *CommonEntityHead, ver container.DwgVersion, layout DimSpecificLayout) (any, error) {
	pos := r.TellBits()
	if d, err := decodeDimCanonical(r, Head, ver, layout); err == nil {
		return d, nil
	}
	// 兜底：canonical 失败（公共头边界异常等）时从原起点走变体扫描。
	r.SetBitPos(pos)
	if ver == container.VerR2000 || ver == container.VerR13 || ver == container.VerR14 || ver == container.VerR2004 {
		if ent, err := ScanDimShapes(r, Head, layout, R2000DimShapes, DecodeDimR2000Variant); err == nil {
			return ent, nil
		} else {
			return ent, fmt.Errorf("cad: DIMENSION R2000 变体全部失败: %w", err)
		}
	}
	if ent, err := ScanDimShapes(r, Head, layout, r2010PlusDimShapes, decodeDimR2010PlusVariant); err == nil {
		return ent, nil
	} else {
		return ent, fmt.Errorf("cad: DIMENSION R2010+ 变体全部失败: %w", err)
	}
}

// dimVerR2007Plus R2007 及以后版本（R2007 三个标志位段起始版本）。
// 显式枚举判断，避免 iota 顺序变化时 >= 比较失真。
func dimVerR2007Plus(v container.DwgVersion) bool { return v == container.VerR2007 || v.R2010Plus() }

// dimStringStream R2007+ 对象尾部字符串流读取器。R2007+ 的字符串内容
// 不占主位流，集中存放在 bitsize 前的 string stream 区域
// （LibreDWG obj_string_stream 口径，已经 trace 逐位核对）：
// bitsize-1 位是 has_strings 标志，其前是 data_size（RS,LE）指示的数据区。
type dimStringStream struct {
	r   *bitstream.BitStream
	pos uint64 // 字符串流自身的读取位（独立于主位流游标）
}

// newDimStringStream 定位字符串流数据区起点；失败返回 nil
// （此时各 T 字段按空串处理，与 !has_strings 行为一致）。
// 主位流游标在返回前恢复到 enterPos。
func newDimStringStream(r *bitstream.BitStream, ObjSizeBit, enterPos uint64) *dimStringStream {
	defer r.SetBitPos(enterPos)
	p0 := int64(ObjSizeBit) - 1
	if p0 < 66 { // 1 位标志 + 2~6 字节 size 指示 + 至少一个字符串的余量
		return nil
	}
	r.SetBitPos(uint64(p0))
	has, err := r.ReadB()
	if err != nil || has == 0 {
		return nil
	}
	// 回退到 p0-16 位读 data_size（RS 小端）
	r.SetBitPos(uint64(p0) - 16)
	DataSize, err := r.ReadRS()
	if err != nil {
		return nil
	}
	if DataSize&0x8000 != 0 {
		// 扩展：hi 16 位在更前 2 字节，data_size 回退 4 字节重读
		r.SetBitPos(uint64(p0) - 48)
		hi, e := r.ReadRS()
		if e != nil {
			return nil
		}
		DataSize = DataSize&0x7FFF | hi<<15
		if uint64(DataSize) > ObjSizeBit {
			return nil
		}
		r.SetBitPos(uint64(p0) - 32 - uint64(DataSize))
	} else {
		if uint64(DataSize) > ObjSizeBit {
			return nil
		}
		r.SetBitPos(uint64(p0) - 16 - uint64(DataSize))
	}
	return &dimStringStream{r: r, pos: r.TellBits()}
}

// readTU 从字符串流读下一个 TU 文本；流不可用或读失败返回空串。
// 借用主位流游标读取，结束时恢复到 enterPos（主位流 0 位推进语义）。
func (s *dimStringStream) readTU(r *bitstream.BitStream, enterPos uint64) string {
	if s == nil {
		return ""
	}
	r.SetBitPos(s.pos)
	tu, err := s.r.ReadTU()
	if err != nil {
		return ""
	}
	s.pos = s.r.TellBits()
	r.SetBitPos(enterPos)
	return tu
}

// decodeDimCanonical 按 LibreDWG 确定性布局解析 DIMENSION：
// [R2010+ class_version RC] extrusion 3BD → text_midpt 2RD → elevation BD →
// flag1 RC → user_text T → text_rotation BD → horiz_dir BD → ins_scale 3BD →
// ins_rotation BD → [R2000+ attachment/lspace/measurement] →
// [R2007+ unknown/flip×2] → clone_ins_pt 2RD → 类型专属尾部 → handle 流
// （common → dimstyle → block，顺序与 dwg.spec 一致）。
func decodeDimCanonical(r *bitstream.BitStream, Head *CommonEntityHead, ver container.DwgVersion, layout DimSpecificLayout) (*EntDimension, error) {
	trOn := cadTraceHandle != 0 && cadTraceHandle == Head.Handle
	// dimR2000Plus attachment 段起始于 R2000；注意 verR2000 是枚举零值，
	// 不可用 >= 判断（R13/R14 枚举值更大但布局更旧）。
	dimR2000Plus := ver == container.VerR2000 || ver == container.VerR2004 || dimVerR2007Plus(ver)
	d := &EntDimension{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}}
	var err error
	traceRC := func(Name string, dst *uint8) error {
		pos := r.TellBits()
		v, e := r.ReadRC()
		if e == nil {
			*dst = v
			if trOn {
				CadTraceField(trOn, pos, r.TellBits(), Name, fmt.Sprintf("%#x", v))
			}
		}
		return e
	}
	traceBD := func(Name string, dst *float64) error {
		pos := r.TellBits()
		v, e := r.ReadBD()
		if e == nil {
			*dst = v
			if trOn {
				CadTraceField(trOn, pos, r.TellBits(), Name, fmt.Sprintf("%g", v))
			}
		}
		return e
	}
	trace3BD := func(Name string, dst *Point3) error {
		pos := r.TellBits()
		v, e := read3pt(r)
		if e == nil {
			*dst = v
			if trOn {
				CadTraceField(trOn, pos, r.TellBits(), Name, fmt.Sprintf("(%g,%g,%g)", v.X, v.Y, v.Z))
			}
		}
		return e
	}
	if ver.R2010Plus() {
		if err = traceRC("class_version", &d.ClassVersion); err != nil {
			return nil, err
		}
	}
	if err = trace3BD("extrusion", &d.Extrusion); err != nil {
		return nil, err
	}
	var mx, my float64
	pos := r.TellBits()
	if mx, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if my, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if trOn {
		CadTraceField(trOn, pos, r.TellBits(), "text_midpt", fmt.Sprintf("(%g,%g)", mx, my))
	}
	if err = traceBD("elevation", &d.Elevation); err != nil {
		return nil, err
	}
	d.TextMidpoint = Point3{mx, my, d.Elevation}
	if err = traceRC("flag1", &d.DimFlags); err != nil {
		return nil, err
	}
	// R2007+ 的 T 字段内容在字符串流中，主位流 0 位；R13-R2004 的 TV 在主位流。
	var ss *dimStringStream
	if dimVerR2007Plus(ver) {
		ss = newDimStringStream(r, Head.ObjSizeBit, r.TellBits())
	}
	pos = r.TellBits()
	if dimVerR2007Plus(ver) {
		d.UserText = ss.readTU(r, pos)
	} else {
		if d.UserText, err = r.ReadTV(512); err != nil {
			return nil, err
		}
	}
	CadTraceField(trOn, pos, r.TellBits(), "user_text", d.UserText)
	if err = traceBD("text_rotation", &d.TextRotation); err != nil {
		return nil, err
	}
	if err = traceBD("horiz_dir", &d.HorizontalDir); err != nil {
		return nil, err
	}
	if err = trace3BD("ins_scale", &d.InsertScale); err != nil {
		return nil, err
	}
	if err = traceBD("ins_rotation", &d.InsertRotation); err != nil {
		return nil, err
	}
	if dimR2000Plus {
		pos = r.TellBits()
		if d.AttachmentPoint, err = r.ReadBS(); err != nil {
			return nil, err
		}
		cadTraceFieldInt(trOn, pos, r.TellBits(), "attachment", int64(d.AttachmentPoint))
		pos = r.TellBits()
		if d.LineSpacingStyle, err = r.ReadBS(); err != nil {
			return nil, err
		}
		cadTraceFieldInt(trOn, pos, r.TellBits(), "lspace_style", int64(d.LineSpacingStyle))
		if err = traceBD("lspace_factor", &d.LineSpacingFactor); err != nil {
			return nil, err
		}
		if err = traceBD("act_measurement", &d.ActualMeasurement); err != nil {
			return nil, err
		}
	}
	if dimVerR2007Plus(ver) {
		pos = r.TellBits()
		var b uint8
		if b, err = r.ReadB(); err != nil {
			return nil, err
		}
		d.unknownFlag = b != 0
		cadTraceFieldInt(trOn, pos, r.TellBits(), "unknown", int64(b))
		if b, err = r.ReadB(); err != nil {
			return nil, err
		}
		d.flipArrow1 = b != 0
		cadTraceFieldInt(trOn, pos+1, r.TellBits(), "flip_arrow1", int64(b))
		if b, err = r.ReadB(); err != nil {
			return nil, err
		}
		d.flipArrow2 = b != 0
		cadTraceFieldInt(trOn, pos+2, r.TellBits(), "flip_arrow2", int64(b))
	}
	var p12x, p12y float64
	pos = r.TellBits()
	if p12x, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if p12y, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if trOn {
		CadTraceField(trOn, pos, r.TellBits(), "clone_ins_pt", fmt.Sprintf("(%g,%g)", p12x, p12y))
	}
	d.InsertPoint = Point3{p12x, p12y, d.Elevation}
	d.HasInsertPoint = true
	var spec dimSpecificData
	if spec, err = readDimSpecific(r, layout); err != nil {
		return nil, err
	}
	applyDimSpecific(d, spec)
	d.computeDimFlag(layout)
	decodeDimHandles(r, Head, d)
	return d, nil
}

// applyDimSpecific 把类型专属尾部解析结果写入实体字段。
func applyDimSpecific(d *EntDimension, spec dimSpecificData) {
	d.Point13, d.Point14, d.Point10 = spec.Point13, spec.Point14, spec.Point10
	d.ExtLineRotation, d.DimRotation = spec.ExtLineRotation, spec.DimRotation
	d.Point15, d.HasPoint15 = spec.Point15, spec.HasPoint15
	d.Point16x, d.P16y, d.HasPoint16 = spec.p16x, spec.P16y, spec.HasPoint16
	d.Flag2 = spec.Flag2
	d.DefPt, d.IsPartial = spec.DefPt, spec.IsPartial
	d.ArcStartParam, d.ArcEndParam = spec.ArcStartParam, spec.ArcEndParam
	d.HasLeader = spec.HasLeader
	d.Leader1Pt, d.Leader2Pt = spec.Leader1Pt, spec.Leader2Pt
	d.LeaderLen = spec.LeaderLen
}

// computeDimFlag 按 LibreDWG DIMENSION DECODER 语义合成输出 flag：
// 保留 flag1 高 3 位；bit7 取 flag1 bit0 的反（非默认样式位）；
// bit5 取 flag1 bit1（R13 起恒置 1）；低 3 位按实体类型补齐
// （ALIGNED=1/ANG2LN=2/DIAMETER=3/RADIUS=4/ANG3PT=5/ORDINATE=6/LINEAR=0；
// ARC_DIMENSION 同 ANG3PT 取 5，见 dwg_spec_shared.h DECODER 分支）；
// ORDINATE 再用 flag2 bit0 覆盖 bit7。
func (d *EntDimension) computeDimFlag(layout DimSpecificLayout) {
	Flag := d.DimFlags & 0xe0
	if d.DimFlags&1 != 0 {
		Flag &= 0x7f
	} else {
		Flag |= 0x80
	}
	if d.DimFlags&2 != 0 {
		Flag |= 0x20
	} else {
		Flag &= 0xdf
	}
	switch layout {
	case DimLayoutAligned:
		Flag |= 1
	case DimLayoutAng2Ln:
		Flag |= 2
	case DimLayoutDiameter:
		Flag |= 3
	case DimLayoutRadius:
		Flag |= 4
	case DimLayoutAng3Pt, DimLayoutArc:
		Flag |= 5
	case DimLayoutOrdinate:
		Flag |= 6
	}
	if layout == DimLayoutOrdinate {
		if d.Flag2&1 != 0 {
			Flag |= 0x80
		} else {
			Flag &^= 0x80
		}
	}
	d.DimFlag = Flag
}

// scanDimShapes 遍历候选变体表取最优：逐变体从同一起点试解，按
// dimPlausibilityScore 评分择优（值小者胜，平局取先）；全部失败时
// 返回末次错误。
func ScanDimShapes(r *bitstream.BitStream, Head *CommonEntityHead, layout DimSpecificLayout, shapes []dimShape, decode func(*bitstream.BitStream, *CommonEntityHead, dimShape, DimSpecificLayout) (*EntDimension, error)) (any, error) {
	pos := r.TellBits()
	var best *EntDimension
	bestScore := uint64(0)
	var lastErr error
	for _, shape := range shapes {
		r.SetBitPos(pos)
		ent, err := decode(r, Head, shape, layout)
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
func decodeDimR2010PlusVariant(r *bitstream.BitStream, Head *CommonEntityHead, shape dimShape, layout DimSpecificLayout) (*EntDimension, error) {
	if shape&dimShapeVersionByte != 0 {
		if _, err := r.ReadRC(); err != nil {
			return nil, err
		}
	}
	d := &EntDimension{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}}
	var err error
	if shape&dimShapeExtrudeBE != 0 {
		X, Y, Z, e := r.ReadBE()
		if e != nil {
			return nil, e
		}
		d.Extrusion = Point3{X, Y, Z}
	} else {
		if d.Extrusion, err = read3pt(r); err != nil {
			return nil, err
		}
	}
	mx, my, err := r.Read2RD()
	if err != nil {
		return nil, err
	}
	if d.Elevation, err = r.ReadBD(); err != nil {
		return nil, err
	}
	d.TextMidpoint = Point3{mx, my, d.Elevation}
	if d.DimFlags, err = r.ReadRC(); err != nil {
		return nil, err
	}
	if shape&dimShapeUserText != 0 {
		if d.UserText, err = r.ReadTV(512); err != nil {
			return nil, err
		}
	}
	if err = readDimCommonTail(r, d); err != nil {
		return nil, err
	}
	if shape&dimShapeR2007Flags != 0 {
		for i := 0; i < 3; i++ { // unknown + flip_arrow1 + flip_arrow2
			if _, err = r.ReadB(); err != nil {
				return nil, err
			}
		}
	}
	p12x, p12y, err := r.Read2RD()
	if err != nil {
		return nil, err
	}
	d.InsertPoint = Point3{p12x, p12y, d.Elevation}
	d.HasInsertPoint = true
	return finishDimension(r, Head, d, layout)
}

// readDimCommonTail 维度公共尾段（R2010+ 形态）：text_rotation → horiz_dir →
// ins_scale → ins_rotation → attachment → linespacing style/factor →
// actual_measurement（后四项无条件存在）。
func readDimCommonTail(r *bitstream.BitStream, d *EntDimension) error {
	steps := append(dimTailThroughRotation(r, d),
		func(r *bitstream.BitStream, d *EntDimension) error {
			var e error
			d.AttachmentPoint, e = r.ReadBS()
			return e
		},
		func(r *bitstream.BitStream, d *EntDimension) error {
			var e error
			d.LineSpacingStyle, e = r.ReadBS()
			return e
		},
		func(r *bitstream.BitStream, d *EntDimension) error {
			var e error
			d.LineSpacingFactor, e = r.ReadBD()
			return e
		},
		func(r *bitstream.BitStream, d *EntDimension) error {
			var e error
			d.ActualMeasurement, e = r.ReadBD()
			return e
		},
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
func dimTailThroughRotation(r *bitstream.BitStream, d *EntDimension) []func(*bitstream.BitStream, *EntDimension) error {
	return []func(*bitstream.BitStream, *EntDimension) error{
		func(r *bitstream.BitStream, d *EntDimension) error {
			var e error
			d.TextRotation, e = r.ReadBD()
			return e
		},
		func(r *bitstream.BitStream, d *EntDimension) error {
			var e error
			d.HorizontalDir, e = r.ReadBD()
			return e
		},
		func(r *bitstream.BitStream, d *EntDimension) error {
			var e error
			d.InsertScale, e = read3pt(r)
			return e
		},
		func(r *bitstream.BitStream, d *EntDimension) error {
			var e error
			d.InsertRotation, e = r.ReadBD()
			return e
		},
	}
}

// finishDimension 收尾：类型专属段解析 + 标志合成 + handle 流。
func finishDimension(r *bitstream.BitStream, Head *CommonEntityHead, d *EntDimension, layout DimSpecificLayout) (*EntDimension, error) {
	spec, err := readDimSpecific(r, layout)
	if err != nil {
		return nil, err
	}
	applyDimSpecific(d, spec)
	d.computeDimFlag(layout)
	decodeDimHandles(r, Head, d)
	return d, nil
}

// decodeDimR2000Variant 按单一 R2000/R2004 变体解析。
func DecodeDimR2000Variant(r *bitstream.BitStream, Head *CommonEntityHead, shape dimShape, layout DimSpecificLayout) (*EntDimension, error) {
	d := &EntDimension{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}}
	var err error
	if d.Extrusion, err = read3pt(r); err != nil {
		return nil, err
	}
	mx, my, err := r.Read2RD()
	if err != nil {
		return nil, err
	}
	if d.Elevation, err = r.ReadBD(); err != nil {
		return nil, err
	}
	d.TextMidpoint = Point3{mx, my, d.Elevation}
	if d.DimFlags, err = r.ReadRC(); err != nil {
		return nil, err
	}
	if d.UserText, err = r.ReadTV(512); err != nil {
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
		if att, err = r.ReadBS(); err != nil {
			return nil, err
		}
		d.AttachmentPoint = att
		var lsStyle uint16
		if lsStyle, err = r.ReadBS(); err != nil {
			return nil, err
		}
		d.LineSpacingStyle = lsStyle
		if d.LineSpacingFactor, err = r.ReadBD(); err != nil {
			return nil, err
		}
		if d.ActualMeasurement, err = r.ReadBD(); err != nil {
			return nil, err
		}
	}
	if shape&r2000ShapeUnknownBit != 0 {
		if _, err = r.ReadB(); err != nil {
			return nil, err
		}
	}
	if shape&r2000ShapeFlipArrow1 != 0 {
		if _, err = r.ReadB(); err != nil {
			return nil, err
		}
	}
	if shape&r2000ShapeFlipArrow2 != 0 {
		if _, err = r.ReadB(); err != nil {
			return nil, err
		}
	}
	if shape&r2000ShapeInsPoint != 0 {
		p12x, p12y, e := r.Read2RD()
		if e != nil {
			return nil, e
		}
		d.InsertPoint = Point3{p12x, p12y, d.Elevation}
		d.HasInsertPoint = true
	}
	return finishDimension(r, Head, d, layout)
}

// decodeDimHandles 解析 handle 流：common → dimstyle + anonymous block。
// 顺序与 dwg.spec 一致（COMMON_ENTITY_HANDLE_DATA 在 dimstyle/block 之前，
// 已由 exr13/ex2004/ex2018 trace 核对）。失败时容忍（退化为仅图层句柄）。
func decodeDimHandles(r *bitstream.BitStream, Head *CommonEntityHead, d *EntDimension) {
	r.SetBitPos(Head.ObjSizeBit)
	Owner, Layer, e3 := ParseCommonEntityHandles(r, Head)
	_ = Owner
	Dimstyle, e1 := objrec.ReadHandleReference(r, Head.Handle)
	block, e2 := objrec.ReadHandleReference(r, Head.Handle)
	if e1 == nil && e2 == nil && e3 == nil {
		d.DimstyleHandle, d.AnonymousBlock, d.Layer = Dimstyle, block, Layer
	} else {
		r.SetBitPos(Head.ObjSizeBit)
		if _, Layer, e := ParseCommonEntityHandles(r, Head); e == nil {
			d.Layer = Layer
		}
	}
}

// read3pt 读取 3BD 为 point3。
func read3pt(r *bitstream.BitStream) (Point3, error) {
	X, Y, Z, err := r.Read3BD()
	return Point3{X, Y, Z}, err
}

// dimPlausibilityScore 标注字段合理性评分（值越小越可信）：
// 微量级垃圾值、天文角度与越界枚举是错位候选的主要特征。
func dimPlausibilityScore(d *EntDimension) uint64 {
	score := uint64(0)
	for _, P := range []Point3{d.Point10, d.Point13, d.Point14, d.TextMidpoint} {
		score += DimPointScore(P)
	}
	if d.HasInsertPoint {
		score += DimPointScore(d.InsertPoint)
	}
	if d.HasPoint15 {
		score += DimPointScore(d.Point15)
	}
	if d.HasPoint16 {
		score += DimValueScore(d.Point16x) + DimValueScore(d.P16y)
	}
	score += DimPointScore(d.Extrusion)
	score += DimPointScore(d.InsertScale)
	score += DimAngleScore(d.TextRotation)
	score += DimAngleScore(d.HorizontalDir)
	score += DimAngleScore(d.ExtLineRotation)
	score += DimAngleScore(d.DimRotation)
	score += DimAngleScore(d.InsertRotation)
	score += DimValueScore(d.ActualMeasurement)
	score += DimValueScore(d.LineSpacingFactor)
	if d.AttachmentPoint > 9 {
		score += 10000
	}
	if d.LineSpacingStyle > 2 {
		score += 10000
	}
	if d.DimFlags > 0x3F {
		score += 1000
	}
	return score
}

// dimGarbageMagnitude 低于该量级（但非零）的值是错位读取的特征。
const dimGarbageMagnitude = 1.0e-30

// dimAngleScore 角度合理性：|v|>1000 渐进惩罚。
func DimAngleScore(v float64) uint64 {
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
func DimValueScore(v float64) uint64 {
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
func DimPointScore(P Point3) uint64 {
	return DimValueScore(P.X) + DimValueScore(P.Y) + DimValueScore(P.Z)
}

// absF 浮点绝对值。
func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
