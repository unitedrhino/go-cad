// entities_more.go 实现补充实体族：射线/构造线、实心/轨迹、三维面、
// 引线、多线、二维/三维多段线及其顶点。
package entity

import (
	"bytes"
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/objrec"
	"math"
	"os"
)

// ---- RAY / XLINE ----

// entRay 射线（起点 + 单位方向）。XLINE 与之同构。
type EntRay struct {
	BaseEntity
	Start      Point3
	UnitVector Point3
	Xline      bool // true 为构造线（两端无限）
}

// decodeRay RAY/XLINE：3BD 起点 + 3BD 方向。
func decodeRay(r *bitstream.BitStream, Head *CommonEntityHead, Xline bool) (any, error) {
	sx, sy, sz, err := r.Read3BD()
	if err != nil {
		return nil, err
	}
	dx, dy, dz, err := r.Read3BD()
	if err != nil {
		return nil, err
	}
	Owner, Layer := decodeOwnerLayer(r, Head)
	return &EntRay{
		BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Layer: Layer, Owner: Owner, Mode: Head.EntityMode},
		Start:      Point3{sx, sy, sz},
		UnitVector: Point3{dx, dy, dz},
		Xline:      Xline,
	}, nil
}

// ---- SOLID / TRACE ----

// entSolid 实心填充（四角点）。TRACE 与之同构。
type EntSolid struct {
	BaseEntity
	P1, P2, P3, P4 Point2
	Elevation      float64
	Thickness      float64
	Extrusion      Point3
	Trace          bool
}

// decodeSolid SOLID/TRACE：BT 厚度 + BD 高程 + 4×2RD 角点 + BE 挤出。
func decodeSolid(r *bitstream.BitStream, Head *CommonEntityHead, Trace bool) (any, error) {
	s := &EntSolid{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}, Trace: Trace}
	var err error
	if s.Thickness, err = r.ReadBT(); err != nil {
		return nil, err
	}
	if s.Elevation, err = r.ReadBD(); err != nil {
		return nil, err
	}
	read2 := func() (Point2, error) {
		X, e := r.ReadRD()
		if e != nil {
			return Point2{}, e
		}
		Y, e := r.ReadRD()
		if e != nil {
			return Point2{}, e
		}
		return Point2{X, Y}, nil
	}
	if s.P1, err = read2(); err != nil {
		return nil, err
	}
	if s.P2, err = read2(); err != nil {
		return nil, err
	}
	if s.P3, err = read2(); err != nil {
		return nil, err
	}
	if s.P4, err = read2(); err != nil {
		return nil, err
	}
	X, Y, Z, err := r.ReadBE()
	if err != nil {
		return nil, err
	}
	s.Extrusion = Point3{X, Y, Z}
	Owner, Layer := decodeOwnerLayer(r, Head)
	s.Owner, s.Layer = Owner, Layer
	return s, nil
}

// ---- 3DFACE ----

// entFace3d 三维面（四点 + 不可见边标志）。
type EntFace3d struct {
	BaseEntity
	P1, P2, P3, P4     Point3
	hasNoFlags         bool // R2000+：无不可见边标志位（has_no_flags）
	zIsZero            bool // R2000+：首角点 z 恒零标志（z_is_zero）
	InvisibleEdgeFlags uint16
}

// decodeFace3dVer 3DFACE 按版本分发：R13/R14 为 4×3BD 直读 + BS 不可见标志；
// R2000+ 为标志位 + 差分 3DD 形式。
func decodeFace3dVer(r *bitstream.BitStream, Head *CommonEntityHead, R13r14 bool) (any, error) {
	if !R13r14 {
		return decodeFace3d(r, Head)
	}
	f := &EntFace3d{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}}
	var err error
	if f.P1, err = read3pt(r); err != nil {
		return nil, err
	}
	if f.P2, err = read3pt(r); err != nil {
		return nil, err
	}
	if f.P3, err = read3pt(r); err != nil {
		return nil, err
	}
	if f.P4, err = read3pt(r); err != nil {
		return nil, err
	}
	inv, err := r.ReadBS()
	if err != nil {
		return nil, err
	}
	f.InvisibleEdgeFlags = inv
	Owner, Layer := decodeOwnerLayer(r, Head)
	f.Owner, f.Layer = Owner, Layer
	return f, nil
}

// decodeFace3d 3DFACE：B 无标志位 + B z 全零 + 4 点（首点 RD×3，其余 3DD 差分）。
func decodeFace3d(r *bitstream.BitStream, Head *CommonEntityHead) (any, error) {
	noFlagInd, err := r.ReadB()
	if err != nil {
		return nil, err
	}
	zIsZero, err := r.ReadB()
	if err != nil {
		return nil, err
	}
	hasNoFlags := noFlagInd != 0
	zZero := zIsZero != 0
	x1, e := r.ReadRD()
	if e != nil {
		return nil, e
	}
	y1, e := r.ReadRD()
	if e != nil {
		return nil, e
	}
	var z1 float64
	if zIsZero == 0 {
		if z1, e = r.ReadRD(); e != nil {
			return nil, e
		}
	}
	P1 := Point3{x1, y1, z1}
	P2, err := read3DD(r, P1)
	if err != nil {
		return nil, err
	}
	P3, err := read3DD(r, P2)
	if err != nil {
		return nil, err
	}
	P4, err := read3DD(r, P3)
	if err != nil {
		return nil, err
	}
	var inv uint16
	if noFlagInd == 0 {
		if inv, err = r.ReadBS(); err != nil {
			return nil, err
		}
	}
	Owner, Layer := decodeOwnerLayer(r, Head)
	return &EntFace3d{
		BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Layer: Layer, Owner: Owner, Mode: Head.EntityMode},
		P1:         P1, P2: P2, P3: P3, P4: P4,
		hasNoFlags:         hasNoFlags,
		zIsZero:            zZero,
		InvisibleEdgeFlags: inv,
	}, nil
}

// read3DD 三个 DD 差分（以 default 为基准）。
func read3DD(r *bitstream.BitStream, def Point3) (Point3, error) {
	X, err := r.ReadDD(def.X)
	if err != nil {
		return Point3{}, err
	}
	Y, err := r.ReadDD(def.Y)
	if err != nil {
		return Point3{}, err
	}
	Z, err := r.ReadDD(def.Z)
	if err != nil {
		return Point3{}, err
	}
	return Point3{X, Y, Z}, nil
}

// ---- LEADER ----

// entLeader 引线（dwg.spec LEADER）：注释/路径类型 + 折点数组 +
// 尾部标志与 R14 专属尺寸字段。
type EntLeader struct {
	BaseEntity
	AnnotationType uint16
	PathType       uint16
	Points         []Point3
	UnknownBit1    bool    // 头部未知位
	Origin         Point3  // 原点（R2000+ gold 键）
	Extrusion      Point3  // 挤出方向
	XDirection     Point3  // X 方向
	InsptOffset    Point3  // 插入点偏移
	Endptproj      Point3  // 端点投影（R13c3~R2007）
	Dimgap         float64 // 标注间距（R14）
	dimasz         float64 // 箭头尺寸（R14）
	BoxHeight      float64 // 文本框高
	BoxWidth       float64 // 文本框宽
	HooklineDir    bool    // 钩线方向（x 向）
	ArrowheadOn    bool    // 箭头可见
	ArrowheadType  uint16  // BSx 箭头类型
	hooklineOn     bool    // 钩线开关（DECODER 按 spec 公式计算）
	unknownBit2    bool    // R14 未知位
	unknownBit3    bool    // R14 未知位
	unknownShort1  uint16  // R14 未知短整数
	byblockColor   uint16  // R14 byblock 颜色
	UnknownBit4    bool    // 尾部未知位（R14 与 R2000b+）
	UnknownBit5    bool    // 尾部未知位（R14 与 R2000b+）
}

// calcHooklineOn 复刻 dwg_calc_hookline_on：annot_type 低 2 位非 0 或
// path_type 末位非 0 或末两点几乎水平（|角度|≤π/12）时钩线关闭。
func (l *EntLeader) CalcHooklineOn() {
	const hooklineOffset = 3.141592653589793 / 12
	Angle := 3.141592653589793 / 2
	if n := len(l.Points); n > 2 {
		pt1, pt2 := l.Points[n-2], l.Points[n-1]
		Angle = atan2f(pt1.Y-pt2.Y, pt1.X-pt2.X)
	}
	off := func(a float64) bool {
		m := a
		if m < 0 {
			m = -m
		}
		return m <= hooklineOffset
	}
	l.hooklineOn = !(l.AnnotationType&0x3 != 0 || l.PathType&0x1 != 0 || off(Angle) || off(Angle-3.141592653589793))
}

// atan2f atan2 包装（隔离 math 导入差异）。
func atan2f(Y, X float64) float64 { return math.Atan2(Y, X) }

// decodeLeader LEADER：按 dwg.spec 二进制序完整解析（头部标志/注释与
// 路径类型/折点/方向向量/文本框/钩线与箭头标志/R14 专属尺寸），尾部
// R2000b+ 未知位；associated_annotation/dimstyle 句柄在 handle 流。
func decodeLeader(r *bitstream.BitStream, Head *CommonEntityHead, ver container.DwgVersion) (any, error) {
	var err error
	var v uint8
	l := &EntLeader{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}}
	r14 := ver == container.VerR13 || ver == container.VerR14
	if v, err = r.ReadB(); err != nil { // unknown_bit_1
		return nil, err
	}
	l.UnknownBit1 = v != 0
	if l.AnnotationType, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if l.PathType, err = r.ReadBS(); err != nil {
		return nil, err
	}
	numPoints, err := r.ReadBL()
	if err != nil {
		return nil, err
	}
	if numPoints > 1_000_000 {
		return nil, fmt.Errorf("cad: LEADER 点数异常 %d", numPoints)
	}
	for i := uint32(0); i < numPoints; i++ {
		X, Y, Z, e := r.Read3BD()
		if e != nil {
			return nil, e
		}
		l.Points = append(l.Points, Point3{X, Y, Z})
	}
	if l.Origin, err = read3pt(r); err != nil {
		return nil, err
	}
	if l.Extrusion, err = read3pt(r); err != nil {
		return nil, err
	}
	if l.XDirection, err = read3pt(r); err != nil {
		return nil, err
	}
	if l.InsptOffset, err = read3pt(r); err != nil {
		return nil, err
	}
	if ver <= container.VerR2007 { // VERSIONS (R_13c3, R_2007)：endptproj（R2004/R2007 也在内）
		if l.Endptproj, err = read3pt(r); err != nil {
			return nil, err
		}
	}
	if r14 { // VERSIONS (R_13b1, R_14)
		if l.Dimgap, err = r.ReadBD(); err != nil {
			return nil, err
		}
	}
	if l.BoxHeight, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if l.BoxWidth, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if v, err = r.ReadB(); err != nil { // hookline_dir
		return nil, err
	}
	l.HooklineDir = v != 0
	if v, err = r.ReadB(); err != nil { // arrowhead_on
		return nil, err
	}
	l.ArrowheadOn = v != 0
	at, err := r.ReadBS() // arrowhead_type（BSx 无符号）
	if err != nil {
		return nil, err
	}
	l.ArrowheadType = at
	l.CalcHooklineOn() // DECODER：hookline_on 计算值
	if r14 {           // VERSIONS (R_13b1, R_14)
		if l.dimasz, err = r.ReadBD(); err != nil {
			return nil, err
		}
		if v, err = r.ReadB(); err != nil { // unknown_bit_2
			return nil, err
		}
		l.unknownBit2 = v != 0
		if v, err = r.ReadB(); err != nil { // unknown_bit_3
			return nil, err
		}
		l.unknownBit3 = v != 0
		if l.unknownShort1, err = r.ReadBS(); err != nil {
			return nil, err
		}
		if l.byblockColor, err = r.ReadBS(); err != nil {
			return nil, err
		}
		if v, err = r.ReadB(); err != nil { // unknown_bit_4
			return nil, err
		}
		l.UnknownBit4 = v != 0
		if v, err = r.ReadB(); err != nil { // unknown_bit_5
			return nil, err
		}
		l.UnknownBit5 = v != 0
	} else { // SINCE (R_2000b)
		if v, err = r.ReadB(); err != nil {
			return nil, err
		}
		l.UnknownBit4 = v != 0
		if v, err = r.ReadB(); err != nil {
			return nil, err
		}
		l.UnknownBit5 = v != 0
	}
	Owner, Layer := decodeOwnerLayer(r, Head)
	l.Owner, l.Layer = Owner, Layer
	return l, nil
}

// ---- MLINE ----

// entMLineVertex 多线顶点。segParams/areaParams 为全部样式线的扁平参数
// 数组（DXF 组码语义同构）；segCounts/areaCounts 记录每条样式线的参数数
// （DWG/JSON 来源可按线分组还原 gold lines[j].segparms，DXF 无分组信息）。
type EntMLineVertex struct {
	Position, Direction, Miter Point3
	SegParams                  []float64
	AreaParams                 []float64
	SegCounts                  []int
	AreaCounts                 []int
}

// entMLine 多线实体。
type EntMLine struct {
	BaseEntity
	Scale         float64
	Justification uint8
	OpenClosed    uint16
	LinesInStyle  uint8
	// basePoint/extrusion 主体 3BD（渲染平铺与法向，decodeMline 读取后
	// 保留；gold JSON 同名键导出对照）
	BasePoint   Point3
	Extrusion   Point3
	Vertices    []EntMLineVertex
	StyleHandle uint64
}

// decodeMline MLINE：BD 比例 + RC 对齐 + 3BD 基点 + 3BD 挤出 + BS 开闭 +
// RC 线数 + 顶点数组（3×3BD + 段参数/区域参数数组）。
func decodeMline(r *bitstream.BitStream, Head *CommonEntityHead) (any, error) {
	m := &EntMLine{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}}
	var err error
	if m.Scale, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if m.Justification, err = r.ReadRC(); err != nil {
		return nil, err
	}
	var bpx, bpy, bpz float64
	if bpx, bpy, bpz, err = r.Read3BD(); err != nil { // base point
		return nil, err
	}
	m.BasePoint = Point3{bpx, bpy, bpz}
	var ex, ey, ez float64
	if ex, ey, ez, err = r.Read3BD(); err != nil { // extrusion
		return nil, err
	}
	m.Extrusion = Point3{ex, ey, ez}
	if m.OpenClosed, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if m.LinesInStyle, err = r.ReadRC(); err != nil {
		return nil, err
	}
	numVerts, err := r.ReadBS()
	if err != nil {
		return nil, err
	}
	if uint32(numVerts) > 1_000_000 {
		return nil, fmt.Errorf("cad: MLINE 顶点数异常 %d", numVerts)
	}
	for i := uint32(0); i < uint32(numVerts); i++ {
		var v EntMLineVertex
		if v.Position, err = read3pt(r); err != nil {
			return nil, err
		}
		if v.Direction, err = read3pt(r); err != nil {
			return nil, err
		}
		if v.Miter, err = read3pt(r); err != nil {
			return nil, err
		}
		// 每条样式线的段参数与区域参数均无条件读取（dwg.spec MLINE 同序）
		for line := 0; line < int(m.LinesInStyle); line++ {
			numSeg, e := r.ReadBS()
			if e != nil {
				return nil, e
			}
			if uint32(numSeg) > 1_000_000 {
				return nil, fmt.Errorf("cad: MLINE 段参数数异常 %d", numSeg)
			}
			for j := uint32(0); j < uint32(numSeg); j++ {
				P, e := r.ReadBD()
				if e != nil {
					return nil, e
				}
				v.SegParams = append(v.SegParams, P)
			}
			v.SegCounts = append(v.SegCounts, int(numSeg))
			numArea, e := r.ReadBS()
			if e != nil {
				return nil, e
			}
			if uint32(numArea) > 1_000_000 {
				return nil, fmt.Errorf("cad: MLINE 区域参数数异常 %d", numArea)
			}
			for j := uint32(0); j < uint32(numArea); j++ {
				P, e := r.ReadBD()
				if e != nil {
					return nil, e
				}
				v.AreaParams = append(v.AreaParams, P)
			}
			v.AreaCounts = append(v.AreaCounts, int(numArea))
		}
		m.Vertices = append(m.Vertices, v)
	}
	Owner, Layer := decodeOwnerLayer(r, Head)
	m.Owner, m.Layer = Owner, Layer
	// handle 流：公共序（reactors/xdic/layer/ltype/prev/next）之后为
	// MLINESTYLE 句柄（批次 B 修正：原直读首个句柄会命中公共序前置项）
	r.SetBitPos(Head.ObjSizeBit)
	if owner2, layer2, e := ParseCommonEntityHandles(r, Head); e == nil {
		m.Owner, m.Layer = owner2, layer2
		if h, e2 := objrec.ReadHandleReference(r, Head.Handle); e2 == nil {
			m.StyleHandle = h
		}
	}
	return m, nil
}

// ---- POLYLINE_2D / POLYLINE_3D + VERTEX ----

// entVertex2d POLYLINE_2D 顶点。
type EntVertex2d struct {
	BaseEntity
	Flags      uint16
	Position   Point3
	StartWidth float64
	EndWidth   float64
	Bulge      float64
	// id 顶点标识符（R2010+ spec 字段 BL0，DXF 91；审计导出）
	id         int64
	TangentDir float64
}

// decodeVertex2d VERTEX_2D：RC 标志 + 3BD 位置 + 起末宽（负起始宽取绝对值并
// 复用）+ BD 凸度 + [R2010+ BL0 顶点 id] + BD 切向。
func decodeVertex2d(r *bitstream.BitStream, Head *CommonEntityHead, R2010Plus bool) (any, error) {
	Flags, err := r.ReadRC()
	if err != nil {
		return nil, err
	}
	v := &EntVertex2d{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}, Flags: uint16(Flags)}
	if v.Position, err = read3pt(r); err != nil {
		return nil, err
	}
	if v.StartWidth, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if v.StartWidth < 0 {
		v.StartWidth = -v.StartWidth
		v.EndWidth = v.StartWidth
	} else if v.EndWidth, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if v.Bulge, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if R2010Plus {
		// FIELD_BL0(id, 91)：R2010+ 顶点标识符（uhengshenhua 实证 714 顶点）
		var vid uint32
		if vid, err = r.ReadBL(); err != nil {
			return nil, err
		}
		v.id = int64(vid)
	}
	if v.TangentDir, err = r.ReadBD(); err != nil {
		return nil, err
	}
	Owner, Layer := decodeOwnerLayer(r, Head)
	v.Owner, v.Layer = Owner, Layer
	return v, nil
}

// entVertex3d POLYLINE_3D 顶点。
type EntVertex3d struct {
	BaseEntity
	Flags    uint8
	Position Point3
}

// decodeVertex3d VERTEX_3D：RC 标志 + 3BD 位置。
func decodeVertex3d(r *bitstream.BitStream, Head *CommonEntityHead) (any, error) {
	Flags, err := r.ReadRC()
	if err != nil {
		return nil, err
	}
	v := &EntVertex3d{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}, Flags: Flags}
	if v.Position, err = read3pt(r); err != nil {
		return nil, err
	}
	Owner, Layer := decodeOwnerLayer(r, Head)
	v.Owner, v.Layer = Owner, Layer
	return v, nil
}

// entVertexPface 面网格顶点（VERTEX_PFACE/VERTEX_MESH 共用布局，gold 键
// point/flag；POLYLINE_PFACE 的顶点由 owner 归属聚合）。
type EntVertexPface struct {
	BaseEntity
	Flag     uint8
	Position Point3
}

// decodeVertexPface VERTEX_PFACE/VERTEX_MESH：RC 标志 + 3BD 位置
// （dwg.spec VERTEX_PFACE LATER_VERSIONS 分支；exr13 trace flag 0xc0
// = MESH|PFACE_MESH 位，point 为世界坐标）。
func DecodeVertexPface(r *bitstream.BitStream, Head *CommonEntityHead) (any, error) {
	Flag, err := r.ReadRC()
	if err != nil {
		return nil, err
	}
	v := &EntVertexPface{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}, Flag: Flag}
	if v.Position, err = read3pt(r); err != nil {
		return nil, err
	}
	v.Owner, v.Layer = decodeOwnerLayer(r, Head)
	return v, nil
}

// entVertexPfaceFace 面网格面记录（VERTEX_PFACE_FACE，gold 键 vertind）：
// POLYLINE_PFACE 的面顶点索引（1 基，0 表示边结束）。
type EntVertexPfaceFace struct {
	BaseEntity
	Flag    uint8
	Vertind [4]int32
}

// decodeVertexPfaceFace VERTEX_PFACE_FACE：4×BSd 顶点索引；flag 不从流读，
// LibreDWG 恒写 128（dwg.spec LATER_VERSIONS 分支 FIELD_VALUE (flag) = 128）。
func DecodeVertexPfaceFace(r *bitstream.BitStream, Head *CommonEntityHead) (any, error) {
	f := &EntVertexPfaceFace{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}, Flag: 128}
	for i := 0; i < 4; i++ {
		v, err := r.ReadBS()
		if err != nil {
			return nil, err
		}
		// BSd 有符号语义：顶点索引正常为 0~正数，负值按 int16 解释
		f.Vertind[i] = int32(int16(v))
	}
	f.Owner, f.Layer = decodeOwnerLayer(r, Head)
	return f, nil
}

// entUnknownEnt UNKNOWN_ENT 兜底实体：未建模动态类实体（如 ACAD_TABLE，
// LibreDWG 默认构建同样兜底为 UNKNOWN_ENT）的通用容器。位流布局按
// dwg2.spec UNKNOWN_ENT = HANDLE_UNKNOWN_BITS：公共头之外的主体不解析，
// 原始位串由 fillMeta/collectEntityRawBits 自动收集，roundtrip 回放保留。
type EntUnknownEnt struct {
	BaseEntity
}

// unknownEntFallbackNames UNKNOWN_ENT 兜底类名名单：仅收录 LibreDWG 默认
// 构建无专门解码器、实体输出 UNKNOWN_ENT 的动态类（example_r14 等九样本
// dwgread 重跑实证 type=528 ACAD_TABLE → UNKNOWN_ENT）。LIGHT/MULTILEADER
// 等有正式 spec 布局的类不在名单，兜底会掩盖真实类型导致对齐失真。
var UnknownEntFallbackNames = map[string]bool{
	"ACAD_TABLE": true,
}

// decodeUnknownEnt UNKNOWN_ENT 兜底解码：不读主体字段，仅解析 handle 流
// 的 owner/layer；原始类名（如 ACAD_TABLE）存 extra.dxfname，对齐
// LibreDWG「实体键输出 UNKNOWN_ENT、DXF 名保留原类」的兜底语义。
func decodeUnknownEnt(r *bitstream.BitStream, Head *CommonEntityHead, dxfname string) (any, error) {
	e := &EntUnknownEnt{BaseEntity: BaseEntity{
		Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode,
		Extra: map[string]any{"dxfname": dxfname},
	}}
	e.Owner, e.Layer = decodeOwnerLayer(r, Head)
	return e, nil
}

// entHelix 螺旋线实体（AcDbHelix：SPLINE 布局前缀 + 螺旋专有字段，
// dwg2.spec DWG_ENTITY (HELIX)，scenario/degree/num_knots 等经
// 2000/Helix.dwg dwgread -v9 trace 现场核对）。
type EntHelix struct {
	BaseEntity
	Scenario    uint32 // 1=控制点样条 2=拟合点样条
	splineFlags uint32 // R2013+ 流内标志（此前由 scenario 推导 8/9）
	knotParam   uint32 // R2013+ 节点参数化
	Degree      uint32
	Rational    bool
	Closed      bool
	Periodic    bool
	knotTol     float64
	ctrlTol     float64
	Knots       []float64
	ctrlPts     []Point3
	Weights     []float64 // weighted 时逐点权重，否则空
	fitTol      float64
	begTanVec   Point3
	endTanVec   Point3
	FitPts      []Point3
	// AcDbHelix 专有
	majorVersion   uint32
	MaintVersion   uint32
	AxisBasePt     Point3
	StartPt        Point3
	axisVector     Point3
	Radius         float64
	Turns          float64
	turnHeight     float64
	Handedness     bool
	constraintType uint8
}

// decodeHelixVer HELIX：scenario BL + [R2013+ splineflags/knotparam BL] +
// degree BL + scenario 分支字段 + AcDbHelix 专有（major/maint version +
// axis_base_pt/start_pt/axis_vector 3BD + radius/turns/turn_height BD +
// handedness B + constraint_type RC）。
func decodeHelixVer(r *bitstream.BitStream, Head *CommonEntityHead, ver container.DwgVersion) (any, error) {
	hx := &EntHelix{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}}
	var err error
	if hx.Scenario, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if ver >= container.VerR2013 {
		if hx.splineFlags, err = r.ReadBL(); err != nil {
			return nil, err
		}
		if hx.knotParam, err = r.ReadBL(); err != nil {
			return nil, err
		}
	} else {
		// UNTIL R_2013：无流内标志，scenario 推导（1→8 planar、2→9）
		if hx.Scenario != 1 && hx.Scenario != 2 {
			return nil, fmt.Errorf("cad: HELIX scenario 异常 %d", hx.Scenario)
		}
		if hx.Scenario == 1 {
			hx.splineFlags = 8
		} else {
			hx.splineFlags = 9
		}
	}
	if hx.Degree, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if hx.Scenario&1 != 0 { // 控制点样条
		var v uint8
		if v, err = r.ReadB(); err != nil {
			return nil, err
		}
		hx.Rational = v != 0
		if v, err = r.ReadB(); err != nil {
			return nil, err
		}
		hx.Closed = v != 0
		if v, err = r.ReadB(); err != nil {
			return nil, err
		}
		hx.Periodic = v != 0
		if hx.knotTol, err = r.ReadBD(); err != nil {
			return nil, err
		}
		if hx.ctrlTol, err = r.ReadBD(); err != nil {
			return nil, err
		}
		var numKnots, numCtrl uint32
		if numKnots, err = r.ReadBL(); err != nil {
			return nil, err
		}
		if numCtrl, err = r.ReadBL(); err != nil {
			return nil, err
		}
		if numKnots > 1_000_000 || numCtrl > 1_000_000 {
			return nil, fmt.Errorf("cad: HELIX 顶点/节点数异常 %d/%d", numKnots, numCtrl)
		}
		// spec 顺序：num_knots/num_ctrl_pts 之后先读 weighted 位，再是
		// knots 向量与控制点向量
		var weighted uint8
		if weighted, err = r.ReadB(); err != nil {
			return nil, err
		}
		for i := uint32(0); i < numKnots; i++ {
			var k float64
			if k, err = r.ReadBD(); err != nil {
				return nil, err
			}
			hx.Knots = append(hx.Knots, k)
		}
		for i := uint32(0); i < numCtrl; i++ {
			var P Point3
			if P, err = read3pt(r); err != nil {
				return nil, err
			}
			hx.ctrlPts = append(hx.ctrlPts, P)
			if weighted != 0 {
				var w float64
				if w, err = r.ReadBD(); err != nil {
					return nil, err
				}
				hx.Weights = append(hx.Weights, w)
			}
		}
	} else { // 拟合点样条（scenario 2）
		if hx.fitTol, err = r.ReadBD(); err != nil {
			return nil, err
		}
		if hx.begTanVec, err = read3pt(r); err != nil {
			return nil, err
		}
		if hx.endTanVec, err = read3pt(r); err != nil {
			return nil, err
		}
		var numFit uint32
		if numFit, err = r.ReadBL(); err != nil {
			return nil, err
		}
		if numFit > 1_000_000 {
			return nil, fmt.Errorf("cad: HELIX 拟合点数异常 %d", numFit)
		}
		for i := uint32(0); i < numFit; i++ {
			var P Point3
			if P, err = read3pt(r); err != nil {
				return nil, err
			}
			hx.FitPts = append(hx.FitPts, P)
		}
	}
	// AcDbHelix 专有
	if hx.majorVersion, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if hx.MaintVersion, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if hx.AxisBasePt, err = read3pt(r); err != nil {
		return nil, err
	}
	if hx.StartPt, err = read3pt(r); err != nil {
		return nil, err
	}
	if hx.axisVector, err = read3pt(r); err != nil {
		return nil, err
	}
	if hx.Radius, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if hx.Turns, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if hx.turnHeight, err = r.ReadBD(); err != nil {
		return nil, err
	}
	var hb uint8
	if hb, err = r.ReadB(); err != nil {
		return nil, err
	}
	hx.Handedness = hb != 0
	if hx.constraintType, err = r.ReadRC(); err != nil {
		return nil, err
	}
	Owner, Layer := decodeOwnerLayer(r, Head)
	hx.Owner, hx.Layer = Owner, Layer
	return hx, nil
}

// readBDSlice 连续读取 n 个 BD。
func readBDSlice(r *bitstream.BitStream, n int) ([]float64, error) {
	out := make([]float64, 0, n)
	for i := 0; i < n; i++ {
		v, err := r.ReadBD()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// entPolyline2d 二维多段线（顶点由 owner 归属聚合）。
type EntPolyline2d struct {
	BaseEntity
	Flags      uint16
	CurveType  uint16
	WidthStart float64
	WidthEnd   float64
	Thickness  float64
	Elevation  float64
	Extrusion  Point3
	// firstVertex/lastVertex R13~R2000 handle 流的首末顶点句柄（顶点全量
	// 由 owner 归属聚合，见 assemblePolylineChildren）
	FirstVertex  uint64
	LastVertex   uint64
	OwnedHandles []uint64
	Seqend       uint64 // SEQEND 结束句柄（顶点句柄后，gold seqend 键导出）
}

// decodePolyline2d POLYLINE_2D：BS 标志 + BS 曲线类型 + 起末宽 + BT 厚 +
// BD 高程 + BE 挤出 + [R2004+ BL 顶点数] + handle 流顶点句柄。
func decodePolyline2d(r *bitstream.BitStream, Head *CommonEntityHead, hasOwnedCount bool) (any, error) {
	P := &EntPolyline2d{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}}
	var err error
	if P.Flags, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if P.CurveType, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if P.WidthStart, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if P.WidthEnd, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if P.Thickness, err = r.ReadBT(); err != nil {
		return nil, err
	}
	if P.Elevation, err = r.ReadBD(); err != nil {
		return nil, err
	}
	// extrusion 为 FIELD_BE（单位向量压缩，默认 (0,0,1) 仅 2 位）：
	// 3BD 读法在默认值时多读 4 位使 num_owned 错位
	// （uhengshenhua h=17453 num_owned=418 实证，LibreDWG spec 同为 BE）
	if ex, ey, ez, eErr := r.ReadBE(); eErr != nil {
		return nil, eErr
	} else {
		P.Extrusion = Point3{ex, ey, ez}
	}
	ownedCount := 0
	if hasOwnedCount {
		var n uint32
		if n, err = r.ReadBL(); err != nil {
			return nil, err
		}
		if n > 1_000_000 {
			return nil, fmt.Errorf("cad: POLYLINE_2D 顶点数异常 %d", n)
		}
		ownedCount = int(n)
	}
	Owner, Layer := decodeOwnerLayer(r, Head)
	P.Owner, P.Layer = Owner, Layer
	// handle 流：顶点句柄在公共句柄之后
	r.SetBitPos(Head.ObjSizeBit)
	// 先按公共头结构读 owner/reactors/xdic/layer 等，再读 owned 句柄
	if _, layer2, e := ParseCommonEntityHandles(r, Head); e == nil {
		P.Layer = layer2
	}
	if !hasOwnedCount {
		// R13~R2000（dwg.spec POLYLINE VERSIONS(R_13,R_2000) 分支）：
		// 公共句柄流后为 first_vertex/last_vertex 两个句柄引用
		if P.FirstVertex, err = objrec.ReadHandleReference(r, Head.Handle); err != nil {
			return nil, err
		}
		if P.LastVertex, err = objrec.ReadHandleReference(r, Head.Handle); err != nil {
			return nil, err
		}
	}
	for i := 0; i < ownedCount; i++ {
		h, e := objrec.ReadHandleReference(r, Head.Handle)
		if e != nil {
			break
		}
		P.OwnedHandles = append(P.OwnedHandles, h)
	}
	// SEQEND 结束句柄（顶点句柄之后恒在，gold seqend 键导出）
	if h, e := objrec.ReadHandleReference(r, Head.Handle); e == nil {
		P.Seqend = h
	}
	return P, nil
}

// entPolyline3d 三维多段线。
type EntPolyline3d struct {
	BaseEntity
	Flags75 uint8
	Flags70 uint8
	// firstVertex/lastVertex R13~R2000 handle 流的首末顶点句柄（顶点全量
	// 由 owner 归属聚合，见 assemblePolylineChildren）
	FirstVertex  uint64
	LastVertex   uint64
	OwnedHandles []uint64
	Seqend       uint64 // SEQEND 结束句柄（顶点句柄后，gold seqend 键导出）
}

// decodePolyline3d POLYLINE_3D：RC×2 标志 + [R2004+ BL 顶点数] + handle 流顶点句柄。
func decodePolyline3d(r *bitstream.BitStream, Head *CommonEntityHead, hasOwnedCount bool) (any, error) {
	P := &EntPolyline3d{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}}
	var err error
	if P.Flags75, err = r.ReadRC(); err != nil {
		return nil, err
	}
	if P.Flags70, err = r.ReadRC(); err != nil {
		return nil, err
	}
	ownedCount := 0
	if hasOwnedCount {
		var n uint32
		if n, err = r.ReadBL(); err != nil {
			return nil, err
		}
		if n > 1_000_000 {
			return nil, fmt.Errorf("cad: POLYLINE_3D 顶点数异常 %d", n)
		}
		ownedCount = int(n)
	}
	Owner, Layer := decodeOwnerLayer(r, Head)
	P.Owner, P.Layer = Owner, Layer
	r.SetBitPos(Head.ObjSizeBit)
	if _, layer2, e := ParseCommonEntityHandles(r, Head); e == nil {
		P.Layer = layer2
	}
	if !hasOwnedCount {
		// R13~R2000（dwg.spec POLYLINE VERSIONS(R_13,R_2000) 分支）：
		// 公共句柄流后为 first_vertex/last_vertex 两个句柄引用
		if P.FirstVertex, err = objrec.ReadHandleReference(r, Head.Handle); err != nil {
			return nil, err
		}
		if P.LastVertex, err = objrec.ReadHandleReference(r, Head.Handle); err != nil {
			return nil, err
		}
	}
	for i := 0; i < ownedCount; i++ {
		h, e := objrec.ReadHandleReference(r, Head.Handle)
		if e != nil {
			break
		}
		P.OwnedHandles = append(P.OwnedHandles, h)
	}
	// SEQEND 结束句柄（顶点句柄之后恒在，gold seqend 键导出）
	if h, e := objrec.ReadHandleReference(r, Head.Handle); e == nil {
		P.Seqend = h
	}
	return P, nil
}

// entTolerance 形位公差实体。
type EntTolerance struct {
	BaseEntity
	Text         string // text_value：标注文本（R2007+ 存于字符串区）
	UnknownShort uint16 // R13/R14 头部的未知短整型
	Insertion    Point3 // ins_pt 插入点
	XDirection   Point3 // x_direction 对称轴方向
	Extrusion    Point3 // 挤出方向
	Height       float64
	Dimgap       float64
	Dimstyle     uint64 // dimstyle 句柄（handle 流）
	polyline     []Point2
}

// decodeTolerance TOLERANCE 解码兼容入口（版本由 head 推断）。
func DecodeTolerance(r *bitstream.BitStream, Head *CommonEntityHead) (any, error) {
	ver := container.VerR2013
	if Head.R13r14 {
		ver = container.VerR14
	}
	return DecodeToleranceVer(r, Head, ver)
}

// decodeToleranceVer TOLERANCE（AcDbFcf）：[R13/R14: unknown_short BS +
// height BD + dimgap BD] + ins_pt/x_direction/extrusion 3BD + text_value
// （R2007+ 字符串区零占位，否则内联 TV）→ handle 流（owner/layer/dimstyle）。
func DecodeToleranceVer(r *bitstream.BitStream, Head *CommonEntityHead, ver container.DwgVersion) (any, error) {
	tol := &EntTolerance{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}}
	var err error
	if ver == container.VerR13 || ver == container.VerR14 {
		var us uint16
		if us, err = r.ReadBS(); err != nil {
			return nil, err
		}
		tol.UnknownShort = us
		if tol.Height, err = r.ReadBD(); err != nil {
			return nil, err
		}
		if tol.Dimgap, err = r.ReadBD(); err != nil {
			return nil, err
		}
	}
	if tol.Insertion, err = read3pt(r); err != nil {
		return nil, err
	}
	if tol.XDirection, err = read3pt(r); err != nil {
		return nil, err
	}
	if tol.Extrusion, err = read3pt(r); err != nil {
		return nil, err
	}
	if ver < container.VerR2007 {
		if tol.Text, err = r.ReadTV(512); err != nil {
			return nil, err
		}
	}
	Owner, Layer := decodeOwnerLayer(r, Head)
	tol.Owner, tol.Layer = Owner, Layer
	// handle 流：owner/layer 之后为 dimstyle 引用
	r.SetBitPos(Head.ObjSizeBit)
	if _, layer2, e := ParseCommonEntityHandles(r, Head); e == nil {
		tol.Layer = layer2
	}
	if h, e := objrec.ReadHandleReference(r, Head.Handle); e == nil && h != 0 {
		tol.Dimstyle = h
	}
	if ver >= container.VerR2007 {
		tol.Text = streamAreaText(r, Head)
	}
	return tol, nil
}

// entPolylinePface 多面网格（顶点/面数由子实体归属）。
// entPolylinePface 三维多面网格（顶点由 owner 归属聚合）。
type EntPolylinePface struct {
	BaseEntity
	NumVertices int
	NumFaces    int
	// firstVertex/lastVertex R13~R2000 handle 流的首末顶点句柄；R2004+ 为
	// num_owned 计数向量（ownedHandles），与 POLYLINE_2D 同构
	FirstVertex  uint64
	LastVertex   uint64
	OwnedHandles []uint64
	Seqend       uint64 // SEQEND 结束句柄（顶点句柄后，gold seqend 键导出）
}

// decodePolylinePface POLYLINE_PFACE：BS 顶点数 + BS 面数 + [R2004+ BL
// 顶点数] + handle 流顶点句柄（R13~R2000 first/last、R2004+ owned 向量）
// + SEQEND。
func decodePolylinePface(r *bitstream.BitStream, Head *CommonEntityHead, hasOwnedCount bool) (any, error) {
	P := &EntPolylinePface{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}}
	nv, err := r.ReadBS()
	if err != nil {
		return nil, err
	}
	nf, err := r.ReadBS()
	if err != nil {
		return nil, err
	}
	P.NumVertices, P.NumFaces = int(nv), int(nf)
	ownedCount := 0
	if hasOwnedCount {
		var n uint32
		if n, err = r.ReadBL(); err != nil {
			return nil, err
		}
		if n > 1_000_000 {
			return nil, fmt.Errorf("cad: POLYLINE_PFACE 顶点数异常 %d", n)
		}
		ownedCount = int(n)
	}
	Owner, Layer := decodeOwnerLayer(r, Head)
	P.Owner, P.Layer = Owner, Layer
	r.SetBitPos(Head.ObjSizeBit)
	if _, layer2, e := ParseCommonEntityHandles(r, Head); e == nil {
		P.Layer = layer2
	}
	if !hasOwnedCount {
		// R13~R2000：公共句柄流后为 first_vertex/last_vertex 两个句柄引用
		if P.FirstVertex, err = objrec.ReadHandleReference(r, Head.Handle); err != nil {
			return nil, err
		}
		if P.LastVertex, err = objrec.ReadHandleReference(r, Head.Handle); err != nil {
			return nil, err
		}
	}
	for i := 0; i < ownedCount; i++ {
		h, e := objrec.ReadHandleReference(r, Head.Handle)
		if e != nil {
			break
		}
		P.OwnedHandles = append(P.OwnedHandles, h)
	}
	// SEQEND 结束句柄（顶点句柄之后恒在，gold seqend 键导出）
	if h, e := objrec.ReadHandleReference(r, Head.Handle); e == nil {
		P.Seqend = h
	}
	return P, nil
}

// ---- VIEWPORT / SHAPE / POLYLINE_MESH ----

// entViewport 视口实体（dwg.spec VIEWPORT：center/width/height 全版本 +
// R2000+ 视图/捕捉/UCS 参数 + R2007+ 灯光/环境色）。
type EntViewport struct {
	BaseEntity
	Center               Point3
	Width, Height        float64
	ViewTarget           Point3
	ViewDir              Point3
	ViewTwist            float64
	ViewSize             float64
	LensLength           float64
	FrontZ, BackZ        float64
	SnapAng              float64
	ViewCtr              Point2
	SnapBase             Point2
	SnapUnit             Point2
	GridUnit             Point2
	CircleZoom           uint16
	GridMajor            uint16
	NumFrozenLayers      uint32
	StatusFlag           uint32
	StyleSheet           string
	RenderMode           uint8
	UcsAtOrigin, UcsVP   bool
	Ucsorg               Point3
	Ucsxdir, Ucsydir     Point3
	UcsElevation         float64
	UcsOrthoView         uint16
	ShadeplotMode        uint16
	UseDefaultLights     bool
	DefaultLightingType  uint8
	Brightness, Contrast float64
	ambientIndex         uint16
	ambientRGB           uint32
}

// decodeViewport VIEWPORT 解码兼容入口（版本由 head 推断）。
func DecodeViewport(r *bitstream.BitStream, Head *CommonEntityHead) (any, error) {
	ver := container.VerR2013
	if Head.R13r14 {
		ver = container.VerR14
	}
	return DecodeViewportVer(r, Head, ver)
}

// decodeViewportVer VIEWPORT：center 3BD + width/height BD 全版本；
// R2000+ 依次为 view_target/VIEWDIR 3BD、VIEWTWIST/VIEWSIZE/LENSLENGTH/
// FRONTZ/BACKZ/SNAPANG BD、VIEWCTR/SNAPBASE/SNAPUNIT/GRIDUNIT 2RD、
// circle_zoom BS、num_frozen_layers BL、status_flag BL、style_sheet T、
// render_mode RC、UCS 段、shadeplot（R2004+）、grid_major（R2007+）；
// R2007+ 尾部为灯光段（use_default_lights/default_lighting_type/
// brightness/contrast/ambient_color CMC）。style_sheet 与字符串类字段
// 一致：R2007+ 存于字符串区主数据流零占位。
func DecodeViewportVer(r *bitstream.BitStream, Head *CommonEntityHead, ver container.DwgVersion) (any, error) {
	vp := &EntViewport{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}}
	var err error
	if vp.Center, err = read3pt(r); err != nil {
		return nil, err
	}
	if vp.Width, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if vp.Height, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if ver == container.VerR13 || ver == container.VerR14 {
		Owner, Layer := decodeOwnerLayer(r, Head)
		vp.Owner, vp.Layer = Owner, Layer
		return vp, nil
	}
	if vp.ViewTarget, err = read3pt(r); err != nil {
		return nil, err
	}
	if vp.ViewDir, err = read3pt(r); err != nil {
		return nil, err
	}
	if vp.ViewTwist, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if vp.ViewSize, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if vp.LensLength, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if vp.FrontZ, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if vp.BackZ, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if vp.SnapAng, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if vp.ViewCtr.X, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if vp.ViewCtr.Y, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if vp.SnapBase.X, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if vp.SnapBase.Y, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if vp.SnapUnit.X, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if vp.SnapUnit.Y, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if vp.GridUnit.X, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if vp.GridUnit.Y, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if vp.CircleZoom, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if ver >= container.VerR2007 {
		if vp.GridMajor, err = r.ReadBS(); err != nil {
			return nil, err
		}
	}
	if vp.NumFrozenLayers, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if vp.StatusFlag, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if ver < container.VerR2007 {
		if vp.StyleSheet, err = r.ReadTV(256); err != nil {
			return nil, err
		}
	}
	if vp.RenderMode, err = r.ReadRC(); err != nil {
		return nil, err
	}
	var bv uint8
	if bv, err = r.ReadB(); err != nil {
		return nil, err
	}
	vp.UcsAtOrigin = bv != 0
	if bv, err = r.ReadB(); err != nil {
		return nil, err
	}
	vp.UcsVP = bv != 0
	if vp.Ucsorg, err = read3pt(r); err != nil {
		return nil, err
	}
	if vp.Ucsxdir, err = read3pt(r); err != nil {
		return nil, err
	}
	if vp.Ucsydir, err = read3pt(r); err != nil {
		return nil, err
	}
	if vp.UcsElevation, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if vp.UcsOrthoView, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if ver >= container.VerR2004 {
		if vp.ShadeplotMode, err = r.ReadBS(); err != nil {
			return nil, err
		}
	}
	if ver >= container.VerR2007 {
		var bv uint8
		if bv, err = r.ReadB(); err != nil {
			return nil, err
		}
		vp.UseDefaultLights = bv != 0
		if vp.DefaultLightingType, err = r.ReadRC(); err != nil {
			return nil, err
		}
		if vp.Brightness, err = r.ReadBD(); err != nil {
			return nil, err
		}
		if vp.Contrast, err = r.ReadBD(); err != nil {
			return nil, err
		}
		if vp.ambientIndex, err = r.ReadBS(); err != nil {
			return nil, err
		}
		if vp.ambientRGB, err = r.ReadBL(); err != nil {
			return nil, err
		}
		var cflag uint8
		if cflag, err = r.ReadRC(); err != nil { // ambient CMC flag
			return nil, err
		}
		// CMC 名称字段（flag 置位时为主数据流内 TU；样本中 flag=0 仍有空串占位）
		if cflag < 4 && cflag&1 != 0 {
			if _, err = r.ReadTU(); err != nil {
				return nil, err
			}
		}
		if cflag < 4 && cflag&2 != 0 {
			if _, err = r.ReadTU(); err != nil {
				return nil, err
			}
		}
	}
	Owner, Layer := decodeOwnerLayer(r, Head)
	vp.Owner, vp.Layer = Owner, Layer
	if ver >= container.VerR2007 {
		vp.StyleSheet = streamAreaText(r, Head)
	}
	return vp, nil
}

// entShape 形参照实体。
type EntShape struct {
	BaseEntity
	Insertion   Point3
	Scale       float64
	Rotation    float64
	WidthFactor float64
	Oblique     float64
	Thickness   float64
	StyleId     uint16 // STYLE 表索引（gold style_id 键）
	ShapeNo     uint16 // SHAPEFILE 内形编号（pre-R13 的 1 字节表索引，R13+ 在位流 BS）
	Extrusion   Point3
}

// decodeShape SHAPE：3BD 插入点 + BD 缩放/旋转/宽度因子/倾斜/厚度 +
// BS 形编号 + 3BD 挤出。
func decodeShape(r *bitstream.BitStream, Head *CommonEntityHead) (any, error) {
	s := &EntShape{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}}
	var err error
	if s.Insertion, err = read3pt(r); err != nil {
		return nil, err
	}
	if s.Scale, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if s.Rotation, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if s.WidthFactor, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if s.Oblique, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if s.Thickness, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if s.StyleId, err = r.ReadBS(); err != nil { // STYLE 表索引（gold style_id）
		return nil, err
	}
	if s.Extrusion, err = read3pt(r); err != nil {
		return nil, err
	}
	Owner, Layer := decodeOwnerLayer(r, Head)
	s.Owner, s.Layer = Owner, Layer
	return s, nil
}

// entPolylineMesh 多边形网格。
type EntPolylineMesh struct {
	BaseEntity
	Flags        uint16
	CurveType    uint16
	MVertexCount uint16
	NVertexCount uint16
	MDensity     uint16
	NDensity     uint16
	OwnedHandles []uint64
}

// decodePolylineMesh POLYLINE_MESH：6×BS 参数 + [R2004+ BL 顶点数] +
// handle 流顶点句柄。
func DecodePolylineMesh(r *bitstream.BitStream, Head *CommonEntityHead, hasOwnedCount bool) (any, error) {
	m := &EntPolylineMesh{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}}
	var err error
	if m.Flags, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if m.CurveType, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if m.MVertexCount, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if m.NVertexCount, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if m.MDensity, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if m.NDensity, err = r.ReadBS(); err != nil {
		return nil, err
	}
	ownedCount := 0
	if hasOwnedCount {
		var n uint32
		if n, err = r.ReadBL(); err != nil {
			return nil, err
		}
		if n > 1_000_000 {
			return nil, fmt.Errorf("cad: POLYLINE_MESH 顶点数异常 %d", n)
		}
		ownedCount = int(n)
	}
	Owner, Layer := decodeOwnerLayer(r, Head)
	m.Owner, m.Layer = Owner, Layer
	r.SetBitPos(Head.ObjSizeBit)
	if _, layer2, e := ParseCommonEntityHandles(r, Head); e == nil {
		m.Layer = layer2
	}
	for i := 0; i < ownedCount; i++ {
		h, e := objrec.ReadHandleReference(r, Head.Handle)
		if e != nil {
			break
		}
		m.OwnedHandles = append(m.OwnedHandles, h)
	}
	return m, nil
}

// ---- REGION / 3DSOLID / BODY / WIPEOUT（R2000+ 覆盖实体）----

// entWipeout 区域覆盖实体（AcDbWipeout，位布局同 AcDbRasterImage）。
type EntWipeout struct {
	BaseEntity
	ClassVersion     uint32
	Pt0, Uvec, Vvec  Point3
	ImageSize        Point2
	DisplayProps     uint16
	Clipping         bool
	Brightness       uint8
	Contrast         uint8
	Fade             uint8
	ClipMode         uint8 // 裁剪模式（clip_mode，R2010+）
	ClipBoundaryType uint16
	ClipVerts        []Point2
	ImageDef         uint64
	ImageDefReactor  uint64
}

// decodeWipeoutVer WIPEOUT（R2000+）：class_version BL + pt0/uvec/vvec 3BD +
// image_size 2RD + display_props BS + clipping B + 亮度/对比/淡出 RC +
// clip_boundary_type BS + 裁剪顶点数组（dwg2.spec WIPEOUT，同 IMAGE 布局；
// imagedef/imagedefreactor 句柄在 handle 流）。主体后的未记载位
// （preview 等）按 objSizeBit 截断跳过。
func DecodeWipeoutVer(r *bitstream.BitStream, Head *CommonEntityHead, ver container.DwgVersion) (any, error) {
	w := &EntWipeout{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}}
	var err error
	if w.ClassVersion, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if w.ClassVersion > 10 {
		return nil, fmt.Errorf("cad: WIPEOUT class_version 异常 %d", w.ClassVersion)
	}
	if w.Pt0, err = read3pt(r); err != nil {
		return nil, err
	}
	if w.Uvec, err = read3pt(r); err != nil {
		return nil, err
	}
	if w.Vvec, err = read3pt(r); err != nil {
		return nil, err
	}
	if w.ImageSize.X, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if w.ImageSize.Y, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if w.DisplayProps, err = r.ReadBS(); err != nil {
		return nil, err
	}
	var v uint8
	if v, err = r.ReadB(); err != nil {
		return nil, err
	}
	w.Clipping = v != 0
	if w.Brightness, err = r.ReadRC(); err != nil {
		return nil, err
	}
	if w.Contrast, err = r.ReadRC(); err != nil {
		return nil, err
	}
	if w.Fade, err = r.ReadRC(); err != nil {
		return nil, err
	}
	if ver >= container.VerR2010 {
		cm, err2 := r.ReadB() // clip_mode（R2010+）
		if err2 != nil {
			return nil, err2
		}
		w.ClipMode = uint8(cm)
	}
	if w.ClipBoundaryType, err = r.ReadBS(); err != nil {
		return nil, err
	}
	numVerts := uint32(2) // 矩形边界固定两角
	if w.ClipBoundaryType != 1 {
		if numVerts, err = r.ReadBL(); err != nil {
			return nil, err
		}
	}
	if numVerts > 100_000 {
		return nil, fmt.Errorf("cad: WIPEOUT 裁剪顶点数异常 %d", numVerts)
	}
	for i := uint32(0); i < numVerts; i++ {
		var P Point2
		if P.X, err = r.ReadRD(); err != nil {
			return nil, err
		}
		if P.Y, err = r.ReadRD(); err != nil {
			return nil, err
		}
		w.ClipVerts = append(w.ClipVerts, P)
	}
	Owner, Layer := decodeOwnerLayer(r, Head)
	w.Owner, w.Layer = Owner, Layer
	// handle 流：owner/layer 之后为 imagedef(5) 与 imagedefreactor(3)
	r.SetBitPos(Head.ObjSizeBit)
	if _, layer2, e := ParseCommonEntityHandles(r, Head); e == nil {
		w.Layer = layer2
	}
	if h, e := objrec.ReadHandleReference(r, Head.Handle); e == nil && h != 0 {
		w.ImageDef = h
	}
	if h, e := objrec.ReadHandleReference(r, Head.Handle); e == nil && h != 0 {
		w.ImageDefReactor = h
	}
	return w, nil
}

// ---- REGION / 3DSOLID / BODY（ACIS 类实体）----

// entAcis ACIS 类实体（REGION/3DSOLID/BODY）：
// 按 LibreDWG DECODE_3DSOLID 提取 SAT/SAB 文本块（几何内核不在解析范围），
// 尾部含 COMMON_3DSOLID 的线框/轮廓/材质/修订段。
type EntAcis struct {
	BaseEntity
	AcisEmpty   bool
	Version     uint16 // 1=SAT(ACIS 4.0 加密文本) 2=SAB(二进制)
	Blocks      [][]byte
	AcisData    []byte // 解混淆后的 SAT 文本
	SabSize     int
	acisHandles []uint64
	historyId   uint64 // history_id 句柄（R2004+ handle 流，gold history_id 键导出）
	Kind        string
	// COMMON_3DSOLID 尾部字段
	Unknown              uint8 // acis 主体前的未知位
	WireframeDataPresent bool
	PointPresent         bool
	point                Point3
	Isolines             uint32
	IsolinePresent       bool
	NumWires             uint32
	numSilhouettes       uint32
	wires                []acisWire       // 线框明细（wires[i] 标量，gold 值级对照）
	silhouettes          []acisSilhouette // 轮廓视图明细（silhouettes[i] 标量）
	AcisEmptyBit         bool
	numMaterials         uint32
	materials            []acisMaterial // 材质数组标量（handle 在 handle 流）
	HasRevisionGuid      bool
	RevisionMajor        uint32
	RevisionMinor1       uint16
	RevisionMinor2       uint16
	revisionBytes        []byte // R2013+ 修订 GUID 原始 8 字节
	EndMarker            uint32
}

// acisWire WIRESTRUCT_fields（dwg_spec_shared.h）的单条线框标量：
// points 与变换矩阵为数组键不参与值级对照，仅保存审计用标量。
type acisWire struct {
	Type             uint8 // RC 线框类型
	SelectionMarker  int32 // BLd 有符号选择标记（gold 出现 -1）
	Color            int64 // BS 读入（FIELD_CAST BS→BL）
	AcisIndex        int32 // BLd 有符号 ACIS 索引（gold 出现 -1）
	TransformPresent bool  // 是否跟 5×3BD 变换 + 3 个标志位
	HasRotation      bool  // 变换标志（transform_present=1 时有效）
	HasReflection    bool  // 变换标志（transform_present=1 时有效）
	HasShear         bool  // 变换标志（transform_present=1 时有效）
}

// acisSilhouette COMMON_3DSOLID 轮廓视图标量：视口目标/方向向量为
// 数组键，仅保存值级对照用标量与内嵌线框。
type acisSilhouette struct {
	VpID          uint32     // BL 视口 id
	VpPerspective bool       // B 透视标志
	HasWires      bool       // B 是否内嵌线框
	Wires         []acisWire // 内嵌线框（silhouettes[i].wires[j]）
}

// acisMaterial COMMON_3DSOLID 材质条目标量：material_handle 在 handle
// 流（宿主键无值级对照），仅保存两个标量。
type acisMaterial struct {
	ArrayIndex uint32 // BL 材质在文档材质表中的索引
	MatAbsref  uint32 // BL 材质绝对引用（gold 可为 0）
}

// acisDeobfuscate ACIS 文本解混淆：≤32 的字节保留，其余取 159-字节。
func AcisDeobfuscate(raw []byte) []byte {
	out := make([]byte, len(raw))
	for i, b := range raw {
		if b <= 32 {
			out[i] = b
		} else {
			out[i] = 159 - b
		}
	}
	return out
}

// decodeAcis ACIS 实体解码的兼容入口（版本由 head 推断：R13/R14 或
// R2007+ 最新布局）。版本感知路径经 cad.go 特判走 decodeAcisVer。
func decodeAcis(r *bitstream.BitStream, Head *CommonEntityHead, Kind string, srcVer container.DwgVersion) (any, error) {
	ver := container.VerR2013
	if Head.R13r14 {
		ver = container.VerR14
	}
	return DecodeAcisVer(r, Head, Kind, ver, srcVer)
}

// decodeAcisVer ACIS/REGION/3DSOLID：acis_empty B → [unknown B + version BS +
// SAT 块循环（BL 大小 + TF 文本）| SAB 数据至 End 标记] → 线框/轮廓/材质/
// 修订段（COMMON_3DSOLID）→ handle 流。r13r14 为 R13/R14 布局（无 R2007+
// 材质与 R2013+ 修订段由版本条件内部判断，调用方传 ver）；srcVer 为分发层
// 真实文件版本（history_id 的 R2004+ 判断用它，宽容推断版 ver 不作依据）。
func DecodeAcisVer(r *bitstream.BitStream, Head *CommonEntityHead, Kind string, ver container.DwgVersion, srcVer container.DwgVersion) (any, error) {
	a := &EntAcis{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}, Kind: Kind}
	AcisEmpty, err := r.ReadB()
	if err != nil {
		return nil, err
	}
	a.AcisEmpty = AcisEmpty == 1
	if !a.AcisEmpty {
		var Unknown uint8
		if Unknown, err = r.ReadB(); err != nil {
			return nil, err
		}
		a.Unknown = Unknown
		if a.Version, err = r.ReadBS(); err != nil {
			return nil, err
		}
		switch a.Version {
		case 1: // SAT：BL 块大小 + TF 文本，0 块终止
			total := 0
			for {
				var blockSize uint32
				if blockSize, err = r.ReadBL(); err != nil {
					return nil, err
				}
				if blockSize == 0 || blockSize > 1<<28 || int64(r.TotalBits())-int64(r.TellBits()) < int64(blockSize)*8 {
					break
				}
				raw := make([]byte, blockSize)
				for i := uint32(0); i < blockSize; i++ {
					if raw[i], err = r.ReadRC(); err != nil {
						return nil, err
					}
				}
				a.Blocks = append(a.Blocks, AcisDeobfuscate(raw))
				total += len(raw)
				if blockSize == 0 {
					break
				}
			}
			a.AcisData = make([]byte, 0, total)
			for _, Blk := range a.Blocks {
				a.AcisData = append(a.AcisData, Blk...)
			}
		case 2: // SAB：二进制未加密，至 End-of-ACIS-data 标记或记录尾
			// 对齐 LibreDWG decode_3dsolid：SAB 块起点可处于位流任意位
			// （acis_data TFF 前的 BS 字段导致 1~7 位偏移），须先按位读出
			// 对齐字节流再在其中搜索 End 标记；命中后读位置回退到标记
			// 终点继续解 COMMON_3DSOLID 段，未命中则停在 TFF 尾。
			const (
				endACIS = "\x0e\x03End\x0e\x02of\x0e\x04ACIS\r\x04data"
				endASM  = "\x0e\x03End\x0e\x02of\x0e\x03ASM\r\x04data"
			)
			startBit := r.TellBits()
			size := int((r.TotalBits() - startBit) / 8)
			if size > 0 {
				size-- // 预留 CRC 等尾部字节（LibreDWG: dat->size - pos - 1）
				if buf, e := r.ReadRCS(size); e == nil {
					End := -1
					for _, marker := range []string{endACIS, endASM} {
						if i := bytes.Index(buf, []byte(marker)); i >= 0 {
							End = i + len(marker)
							break
						}
					}
					if End < 0 {
						End = size
					}
					if cadTraceHandle != 0 && cadTraceHandle == Head.Handle {
						fmt.Fprintf(os.Stderr, "[acis] handle=%d startBit=%d totalBits=%d size=%d end=%d head.objSizeBit=%v\n",
							Head.Handle, startBit, r.TotalBits(), size, End, Head.ObjSizeBit)
					}
					a.SabSize = End
					a.AcisData = append([]byte(nil), buf[:End]...)
					r.SetBitPos(startBit + uint64(End)*8)
				}
			}
		}
	}
	// COMMON_3DSOLID：线框/参考点/轮廓线段
	if err = DecodeAcisWireframe(r, a); err != nil {
		// 线框/轮廓段损坏（LibreDWG 对此类记录同样错位报错）：
		// 放弃尾部字段降级纳管，直接落到 handle 流保证对象可用
		//（位流已错位，history_id 不可信不读取——R2013 样本实证）
		Owner, Layer := decodeOwnerLayer(r, Head)
		a.Owner, a.Layer = Owner, Layer
		r.SetBitPos(Head.ObjSizeBit)
		if _, layer2, e := ParseCommonEntityHandles(r, Head); e == nil {
			a.Layer = layer2
		}
		a.acisHandles = readAcisHandles(r, Head)
		return a, nil
	}
	var v uint8
	if v, err = r.ReadB(); err != nil { // acis_empty_bit
		return nil, err
	}
	a.AcisEmptyBit = v != 0
	if a.Version > 1 {
		if ver >= container.VerR2007 {
			if a.numMaterials, err = r.ReadBL(); err != nil {
				return nil, err
			}
			// 材质循环（array_index BL + mat_absref BL + material handle）。
			// LibreDWG REPEAT_CHKCOUNT 口径：条数×sizeof(条目字节)与剩余
			// 位**不对称比较**（24×n > AVAIL_BITS 即拒绝；skylight 实证
			// h=2001 144×24=3456>43 位拒绝、h=3330 1×24=24≤85 位通过）。
			// 拒绝时清零计数并放弃尾部字段、按 handle 流降级纳管——返回
			// 错误会令扫描框架淘汰正确起点候选，反而解出错位对象
			if a.numMaterials > 1_000_000 ||
				int64(a.numMaterials)*24 > int64(r.TotalBits()-r.TellBits()) {
				a.numMaterials = 0
				Owner, Layer := decodeOwnerLayer(r, Head)
				a.Owner, a.Layer = Owner, Layer
				r.SetBitPos(Head.ObjSizeBit)
				if _, layer2, e := ParseCommonEntityHandles(r, Head); e == nil {
					a.Layer = layer2
				}
				// 位流已错位（材质计数越界），history_id 不可信不读取
				a.acisHandles = readAcisHandles(r, Head)
				return a, nil
			}
			for i := uint32(0); i < a.numMaterials; i++ {
				var ai, ma uint32
				if ai, err = r.ReadBL(); err != nil {
					return nil, err
				}
				if ma, err = r.ReadBL(); err != nil {
					return nil, err
				}
				// material_handle 在 handle 流（COMMON_ENTITY_HANDLE_DATA
				// 的 layer 之后，skylight h=3330 trace 实证），dat 流只读标量
				a.materials = append(a.materials, acisMaterial{ArrayIndex: ai, MatAbsref: ma})
			}
		}
	}
	// 修订段（COMMON_3DSOLID SINCE R_2013b）不受 version>1 约束：
	// AcDs 场景（has_ds_data=1、acis_empty=1）同样存在（spec 无版本内嵌）
	if ver >= container.VerR2013 {
		if v, err = r.ReadB(); err != nil { // has_revision_guid
			return nil, err
		}
		a.HasRevisionGuid = v != 0
		if a.RevisionMajor, err = r.ReadBL(); err != nil {
			return nil, err
		}
		var m uint16
		if m, err = r.ReadBS(); err != nil { // revision_minor1
			return nil, err
		}
		a.RevisionMinor1 = m
		if m, err = r.ReadBS(); err != nil { // revision_minor2
			return nil, err
		}
		a.RevisionMinor2 = m
		a.revisionBytes = make([]byte, 8)
		for i := 0; i < 8; i++ {
			if a.revisionBytes[i], err = r.ReadRC(); err != nil {
				return nil, err
			}
		}
		if a.EndMarker, err = r.ReadBL(); err != nil {
			return nil, err
		}
	}
	Owner, Layer := decodeOwnerLayer(r, Head)
	a.Owner, a.Layer = Owner, Layer
	for i := 0; i < int(a.numMaterials); i++ {
		if _, e := objrec.ReadHandleReference(r, Head.Handle); e != nil {
			break
		}
	}
	r.SetBitPos(Head.ObjSizeBit)
	if owner2, layer2, e := ParseCommonEntityHandles(r, Head); e == nil {
		a.Owner, a.Layer = owner2, layer2
	}
	// history_id：R2004+ 公共句柄序之后的首个句柄引用（可 NULL）。
	// R13~R2000 handle 流在 prev/next 后即结束；acis_empty=1 的 AcDs
	// 空记录 R2013+ 修订段为错位垃圾（LibreDWG 同点 ERROR），不读取。
	if srcVer >= container.VerR2004 && !a.AcisEmpty {
		if h, e := objrec.ReadHandleReference(r, Head.Handle); e == nil {
			a.historyId = h
		}
	}
	a.acisHandles = readAcisHandles(r, Head)
	return a, nil
}

// readAcisHandles 读取 ACIS 实体 handle 流的引用句柄（至多 8 个）。
func readAcisHandles(r *bitstream.BitStream, Head *CommonEntityHead) []uint64 {
	var out []uint64
	for i := 0; i < 8; i++ {
		h, err := objrec.ReadHandleReference(r, Head.Handle)
		if err != nil {
			break
		}
		if h == 0 {
			continue
		}
		out = append(out, h)
	}
	return out
}

// decodeAcisWireframe 解析 COMMON_3DSOLID 线框/参考点/轮廓线段：
// wireframe_data_present → point_present(+point 3BD) → isolines BL →
// isoline_present → wires/silhouettes 递归结构（WIRESTRUCT_fields）。
// 线框明细不进入实体模型，仅按结构跳过保证位序正确。
func DecodeAcisWireframe(r *bitstream.BitStream, a *EntAcis) error {
	var err error
	var v uint8
	if v, err = r.ReadB(); err != nil {
		return err
	}
	a.WireframeDataPresent = v != 0
	if !a.WireframeDataPresent {
		return nil
	}
	if v, err = r.ReadB(); err != nil {
		return err
	}
	a.PointPresent = v != 0
	if a.PointPresent {
		X, Y, Z, err := r.Read3BD()
		if err != nil {
			return err
		}
		a.point = Point3{X, Y, Z}
	}
	if a.Isolines, err = r.ReadBL(); err != nil {
		return err
	}
	if v, err = r.ReadB(); err != nil {
		return err
	}
	a.IsolinePresent = v != 0
	if !a.IsolinePresent {
		return nil
	}
	skipWire := func() (acisWire, error) {
		var w acisWire
		var typ uint8
		if typ, err = r.ReadRC(); err != nil {
			return w, err
		}
		w.Type = typ
		var sm int32
		{
			var raw uint32
			if raw, err = r.ReadBL(); err != nil { // selection_marker（BLd 有符号）
				return w, err
			}
			sm = int32(raw)
		}
		w.SelectionMarker = sm
		var c uint16
		if c, err = r.ReadBS(); err != nil { // color（FIELD_CAST BS→BL）
			return w, err
		}
		w.Color = int64(c)
		{
			var raw uint32
			if raw, err = r.ReadBL(); err != nil { // acis_index（BLd 有符号）
				return w, err
			}
			w.AcisIndex = int32(raw)
		}
		numPoints, err := r.ReadBL()
		if err != nil {
			return w, err
		}
		if numPoints > 1_000_000 {
			return w, fmt.Errorf("cad: ACIS 线框点数异常 %d", numPoints)
		}
		for i := uint32(0); i < numPoints; i++ {
			if _, _, _, err = r.Read3BD(); err != nil {
				return w, err
			}
		}
		if v, err = r.ReadB(); err != nil { // transform_present
			return w, err
		}
		w.TransformPresent = v != 0
		if v != 0 {
			for k := 0; k < 5; k++ {
				if _, _, _, err = r.Read3BD(); err != nil {
					return w, err
				}
			}
			for k := 0; k < 3; k++ {
				if v, err = r.ReadB(); err != nil {
					return w, err
				}
				switch k {
				case 0:
					w.HasRotation = v != 0
				case 1:
					w.HasReflection = v != 0
				case 2:
					w.HasShear = v != 0
				}
			}
		}
		return w, nil
	}
	if a.NumWires, err = r.ReadBL(); err != nil {
		return err
	}
	if a.NumWires > 1_000_000 {
		return fmt.Errorf("cad: ACIS 线框数异常 %d", a.NumWires)
	}
	for i := uint32(0); i < a.NumWires; i++ {
		w, e := skipWire()
		if e != nil {
			return e
		}
		a.wires = append(a.wires, w)
	}
	if a.numSilhouettes, err = r.ReadBL(); err != nil {
		return err
	}
	if a.numSilhouettes > 1_000_000 {
		return fmt.Errorf("cad: ACIS 轮廓数异常 %d", a.numSilhouettes)
	}
	for i := uint32(0); i < a.numSilhouettes; i++ {
		sil := acisSilhouette{}
		if sil.VpID, err = r.ReadBL(); err != nil { // vp_id
			return err
		}
		for k := 0; k < 3; k++ {
			if _, _, _, err = r.Read3BD(); err != nil { // vp_target/dir/up
				return err
			}
		}
		if v, err = r.ReadB(); err != nil { // vp_perspective
			return err
		}
		sil.VpPerspective = v != 0
		if v, err = r.ReadB(); err != nil { // has_wires
			return err
		}
		sil.HasWires = v != 0
		if v != 0 {
			var nw uint32
			if nw, err = r.ReadBL(); err != nil {
				return err
			}
			if nw > 1_000_000 {
				return fmt.Errorf("cad: ACIS 轮廓线框数异常 %d", nw)
			}
			for j := uint32(0); j < nw; j++ {
				w, e := skipWire()
				if e != nil {
					return e
				}
				sil.Wires = append(sil.Wires, w)
			}
		}
		a.silhouettes = append(a.silhouettes, sil)
	}
	return nil
}

// ---- R2013+ 字符串区（string stream）读取 ----

// readStringAreaStrings 读取 R2007+ 对象的字符串区内容。
// 布局（LibreDWG obj_string_stream）：[文字数据区 data_size 字节]
// [has_strings B @bitsize-1][data_size RS @bitsize-17][handle 流]。
// 字符串区内为顺序排列的 TU 串（正文、标签等），空串是合法元素
// （chuandongzhou ATTRIB text_value="" 后紧跟 tag 实证），一并返回，
// 读取失败即止。
func readStringAreaStrings(r *bitstream.BitStream, Head *CommonEntityHead, max int) []string {
	if Head.ObjSizeBit < 34 {
		return nil
	}
	r2 := *r
	r2.SetBitPos(Head.ObjSizeBit - 1)
	hs, err := r2.ReadB()
	if err != nil || hs != 1 {
		return nil
	}
	r2.SetBitPos(Head.ObjSizeBit - 17)
	ds, err := r2.ReadRS()
	if err != nil {
		return nil
	}
	if ds&0x8000 != 0 {
		r2.SetBitPos(Head.ObjSizeBit - 33)
		hi, e := r2.ReadRS()
		if e != nil {
			return nil
		}
		ds = ds&0x7fff | uint16(uint32(hi)<<15)
	}
	areaStart := int64(Head.ObjSizeBit) - 17 - int64(ds)
	if areaStart < 0 {
		return nil
	}
	var out []string
	pos := areaStart
	for i := 0; i < max; i++ {
		r3 := *r
		r3.SetBitPos(uint64(pos))
		s, e := r3.ReadTU()
		if e != nil {
			break
		}
		out = append(out, s)
		pos = int64(r3.TellBits())
	}
	return out
}

// decodeSolidTolerant R13/R14 SOLID/TRACE：R13 头部偶有 1 位长度抖动，
// 首次解码结果坐标量级异常（denormal 型错位读数）时回退 1 位重试。
func DecodeSolidTolerant(r *bitstream.BitStream, Head *CommonEntityHead, Trace bool) (any, error) {
	savedByte, savedBit := r.Cursor()
	ent, err := decodeSolid(r, Head, Trace)
	if err == nil {
		if s, ok := ent.(*EntSolid); ok && entitySolidSane(s) {
			return ent, nil
		}
	}
	r.Restore(savedByte, savedBit)
	r2 := *r
	bytePos, bitPos := r2.Cursor()
	if bytePos == 0 && bitPos == 0 {
		// 实体数据起点已在位 0：无从回退 1 位，按首遍结果返回
		if err == nil {
			return ent, nil
		}
		return nil, err
	}
	if bitPos == 0 {
		r2.Restore(bytePos-1, 7)
	} else {
		r2.Restore(bytePos, bitPos-1)
	}
	ent2, err2 := decodeSolid(&r2, Head, Trace)
	if err2 == nil {
		if s, ok := ent2.(*EntSolid); ok && entitySolidSane(s) {
			return ent2, nil
		}
	}
	if err == nil {
		return ent, nil
	}
	if err2 == nil {
		return ent2, nil
	}
	return nil, err
}

// decodeArcTolerant ARC 容忍解码：部分 R13/R14 编码器的 BT（位厚度）比
// LibreDWG 规范少 1 位 mode 前缀（thickness 码位与后续 BE flag 位合并消费），
// 导致起止角错位读出天文值。首选常规读法，角度不合理时翻转 BT 语义重试
// （实体数据起点不动，center/radius 读取不受影响）。
func decodeArcTolerant(r *bitstream.BitStream, Head *CommonEntityHead) (any, error) {
	savedByte, savedBit := r.Cursor()
	ent, err := decodeArc(r, Head)
	if err == nil {
		if a, ok := ent.(*EntArc); ok && arcAnglesSane(a) {
			return ent, nil
		}
	}
	r2 := *r
	r2.Restore(savedByte, savedBit)
	r2.LegacyBT = !r2.LegacyBT
	ent2, err2 := decodeArc(&r2, Head)
	if err2 == nil {
		if a, ok := ent2.(*EntArc); ok && arcAnglesSane(a) {
			return ent2, nil
		}
	}
	if err == nil {
		return ent, nil
	}
	if err2 == nil {
		return ent2, nil
	}
	return nil, err
}

// arcAnglesSane 圆弧起止角量级检查（拒绝错位读出的天文角度）。
func arcAnglesSane(a *EntArc) bool {
	return IsFinite(a.AngleStart) && IsFinite(a.AngleEnd) &&
		math.Abs(a.AngleStart) < 1e6 && math.Abs(a.AngleEnd) < 1e6
}

// entitySolidSane SOLID 角点量级检查（拒绝 1e-150 型错位读数）。
func entitySolidSane(e *EntSolid) bool {
	for _, P := range [4]Point2{e.P1, e.P2, e.P3, e.P4} {
		v := math.Abs(P.X) + math.Abs(P.Y)
		if v > 0 && v < 1e-30 {
			return false
		}
	}
	return true
}

// seqendHandle INSERT 的 SEQEND 句柄占位：正向重建无独立记录来源，以
// 块内末属性 +1 不成立时回退块头句柄；读侧将该句柄保存至 seqend 字段
// （gold seqend 键对照用），值不参与渲染与文本提取，仅保持 handle 流
// 结构合法。
func (e *EntInsert) SeqendHandle() uint64 {
	if h := e.Attribs[len(e.Attribs)-1]; h != 0 {
		return h
	}
	return e.BlockHeader
}

// seqendPlaceholder POLYLINE_2D/3D/PFACE 的 SEQEND 占位：正向重建无独立
// 记录来源，优先回显首末顶点句柄（非零时保证引用可解析），否则写 NULL
// 空引用；读侧保存至 seqend 字段（gold seqend 键对照用），值不参与渲染。
func (p *EntPolyline2d) SeqendPlaceholder() uint64 {
	if p.FirstVertex != 0 {
		return p.FirstVertex
	}
	return p.LastVertex
}

// seqendPlaceholder POLYLINE_3D 的 SEQEND 占位（语义同 POLYLINE_2D）。
func (p *EntPolyline3d) SeqendPlaceholder() uint64 {
	if p.FirstVertex != 0 {
		return p.FirstVertex
	}
	return p.LastVertex
}

// seqendPlaceholder POLYLINE_PFACE 的 SEQEND 占位（语义同 POLYLINE_2D）。
func (p *EntPolylinePface) SeqendPlaceholder() uint64 {
	if p.FirstVertex != 0 {
		return p.FirstVertex
	}
	return p.LastVertex
}
