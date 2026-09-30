// entities_more.go 实现补充实体族：射线/构造线、实心/轨迹、三维面、
// 引线、多线、二维/三维多段线及其顶点。
package cad

import (
	"bytes"
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/objrec"
	"math"
	"os"
	"sort"
)

// ---- RAY / XLINE ----

// entRay 射线（起点 + 单位方向）。XLINE 与之同构。
type entRay struct {
	baseEntity
	start      point3
	unitVector point3
	xline      bool // true 为构造线（两端无限）
}

// decodeRay RAY/XLINE：3BD 起点 + 3BD 方向。
func decodeRay(r *bitstream.BitStream, head *commonEntityHead, xline bool) (any, error) {
	sx, sy, sz, err := r.Read3BD()
	if err != nil {
		return nil, err
	}
	dx, dy, dz, err := r.Read3BD()
	if err != nil {
		return nil, err
	}
	owner, layer := decodeOwnerLayer(r, head)
	return &entRay{
		baseEntity: baseEntity{handle: head.handle, color: head.color, layer: layer, owner: owner, mode: head.entityMode},
		start:      point3{sx, sy, sz},
		unitVector: point3{dx, dy, dz},
		xline:      xline,
	}, nil
}

// ---- SOLID / TRACE ----

// entSolid 实心填充（四角点）。TRACE 与之同构。
type entSolid struct {
	baseEntity
	p1, p2, p3, p4 point2
	elevation      float64
	thickness      float64
	extrusion      point3
	trace          bool
}

// decodeSolid SOLID/TRACE：BT 厚度 + BD 高程 + 4×2RD 角点 + BE 挤出。
func decodeSolid(r *bitstream.BitStream, head *commonEntityHead, trace bool) (any, error) {
	s := &entSolid{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}, trace: trace}
	var err error
	if s.thickness, err = r.ReadBT(); err != nil {
		return nil, err
	}
	if s.elevation, err = r.ReadBD(); err != nil {
		return nil, err
	}
	read2 := func() (point2, error) {
		x, e := r.ReadRD()
		if e != nil {
			return point2{}, e
		}
		y, e := r.ReadRD()
		if e != nil {
			return point2{}, e
		}
		return point2{x, y}, nil
	}
	if s.p1, err = read2(); err != nil {
		return nil, err
	}
	if s.p2, err = read2(); err != nil {
		return nil, err
	}
	if s.p3, err = read2(); err != nil {
		return nil, err
	}
	if s.p4, err = read2(); err != nil {
		return nil, err
	}
	x, y, z, err := r.ReadBE()
	if err != nil {
		return nil, err
	}
	s.extrusion = point3{x, y, z}
	owner, layer := decodeOwnerLayer(r, head)
	s.owner, s.layer = owner, layer
	return s, nil
}

// ---- 3DFACE ----

// entFace3d 三维面（四点 + 不可见边标志）。
type entFace3d struct {
	baseEntity
	p1, p2, p3, p4     point3
	hasNoFlags         bool // R2000+：无不可见边标志位（has_no_flags）
	zIsZero            bool // R2000+：首角点 z 恒零标志（z_is_zero）
	invisibleEdgeFlags uint16
}

// decodeFace3dVer 3DFACE 按版本分发：R13/R14 为 4×3BD 直读 + BS 不可见标志；
// R2000+ 为标志位 + 差分 3DD 形式。
func decodeFace3dVer(r *bitstream.BitStream, head *commonEntityHead, r13r14 bool) (any, error) {
	if !r13r14 {
		return decodeFace3d(r, head)
	}
	f := &entFace3d{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}}
	var err error
	if f.p1, err = read3pt(r); err != nil {
		return nil, err
	}
	if f.p2, err = read3pt(r); err != nil {
		return nil, err
	}
	if f.p3, err = read3pt(r); err != nil {
		return nil, err
	}
	if f.p4, err = read3pt(r); err != nil {
		return nil, err
	}
	inv, err := r.ReadBS()
	if err != nil {
		return nil, err
	}
	f.invisibleEdgeFlags = inv
	owner, layer := decodeOwnerLayer(r, head)
	f.owner, f.layer = owner, layer
	return f, nil
}

// decodeFace3d 3DFACE：B 无标志位 + B z 全零 + 4 点（首点 RD×3，其余 3DD 差分）。
func decodeFace3d(r *bitstream.BitStream, head *commonEntityHead) (any, error) {
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
	p1 := point3{x1, y1, z1}
	p2, err := read3DD(r, p1)
	if err != nil {
		return nil, err
	}
	p3, err := read3DD(r, p2)
	if err != nil {
		return nil, err
	}
	p4, err := read3DD(r, p3)
	if err != nil {
		return nil, err
	}
	var inv uint16
	if noFlagInd == 0 {
		if inv, err = r.ReadBS(); err != nil {
			return nil, err
		}
	}
	owner, layer := decodeOwnerLayer(r, head)
	return &entFace3d{
		baseEntity: baseEntity{handle: head.handle, color: head.color, layer: layer, owner: owner, mode: head.entityMode},
		p1:         p1, p2: p2, p3: p3, p4: p4,
		hasNoFlags:         hasNoFlags,
		zIsZero:            zZero,
		invisibleEdgeFlags: inv,
	}, nil
}

// read3DD 三个 DD 差分（以 default 为基准）。
func read3DD(r *bitstream.BitStream, def point3) (point3, error) {
	x, err := r.ReadDD(def.x)
	if err != nil {
		return point3{}, err
	}
	y, err := r.ReadDD(def.y)
	if err != nil {
		return point3{}, err
	}
	z, err := r.ReadDD(def.z)
	if err != nil {
		return point3{}, err
	}
	return point3{x, y, z}, nil
}

// ---- LEADER ----

// entLeader 引线（dwg.spec LEADER）：注释/路径类型 + 折点数组 +
// 尾部标志与 R14 专属尺寸字段。
type entLeader struct {
	baseEntity
	annotationType uint16
	pathType       uint16
	points         []point3
	unknownBit1    bool    // 头部未知位
	origin         point3  // 原点（R2000+ gold 键）
	extrusion      point3  // 挤出方向
	xDirection     point3  // X 方向
	insptOffset    point3  // 插入点偏移
	endptproj      point3  // 端点投影（R13c3~R2007）
	dimgap         float64 // 标注间距（R14）
	dimasz         float64 // 箭头尺寸（R14）
	boxHeight      float64 // 文本框高
	boxWidth       float64 // 文本框宽
	hooklineDir    bool    // 钩线方向（x 向）
	arrowheadOn    bool    // 箭头可见
	arrowheadType  uint16  // BSx 箭头类型
	hooklineOn     bool    // 钩线开关（DECODER 按 spec 公式计算）
	unknownBit2    bool    // R14 未知位
	unknownBit3    bool    // R14 未知位
	unknownShort1  uint16  // R14 未知短整数
	byblockColor   uint16  // R14 byblock 颜色
	unknownBit4    bool    // 尾部未知位（R14 与 R2000b+）
	unknownBit5    bool    // 尾部未知位（R14 与 R2000b+）
}

// calcHooklineOn 复刻 dwg_calc_hookline_on：annot_type 低 2 位非 0 或
// path_type 末位非 0 或末两点几乎水平（|角度|≤π/12）时钩线关闭。
func (l *entLeader) calcHooklineOn() {
	const hooklineOffset = 3.141592653589793 / 12
	angle := 3.141592653589793 / 2
	if n := len(l.points); n > 2 {
		pt1, pt2 := l.points[n-2], l.points[n-1]
		angle = atan2f(pt1.y-pt2.y, pt1.x-pt2.x)
	}
	off := func(a float64) bool {
		m := a
		if m < 0 {
			m = -m
		}
		return m <= hooklineOffset
	}
	l.hooklineOn = !(l.annotationType&0x3 != 0 || l.pathType&0x1 != 0 || off(angle) || off(angle-3.141592653589793))
}

// atan2f atan2 包装（隔离 math 导入差异）。
func atan2f(y, x float64) float64 { return math.Atan2(y, x) }

// decodeLeader LEADER：按 dwg.spec 二进制序完整解析（头部标志/注释与
// 路径类型/折点/方向向量/文本框/钩线与箭头标志/R14 专属尺寸），尾部
// R2000b+ 未知位；associated_annotation/dimstyle 句柄在 handle 流。
func decodeLeader(r *bitstream.BitStream, head *commonEntityHead, ver dwgVersion) (any, error) {
	var err error
	var v uint8
	l := &entLeader{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}}
	r14 := ver == verR13 || ver == verR14
	if v, err = r.ReadB(); err != nil { // unknown_bit_1
		return nil, err
	}
	l.unknownBit1 = v != 0
	if l.annotationType, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if l.pathType, err = r.ReadBS(); err != nil {
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
		x, y, z, e := r.Read3BD()
		if e != nil {
			return nil, e
		}
		l.points = append(l.points, point3{x, y, z})
	}
	if l.origin, err = read3pt(r); err != nil {
		return nil, err
	}
	if l.extrusion, err = read3pt(r); err != nil {
		return nil, err
	}
	if l.xDirection, err = read3pt(r); err != nil {
		return nil, err
	}
	if l.insptOffset, err = read3pt(r); err != nil {
		return nil, err
	}
	if ver <= verR2007 { // VERSIONS (R_13c3, R_2007)：endptproj（R2004/R2007 也在内）
		if l.endptproj, err = read3pt(r); err != nil {
			return nil, err
		}
	}
	if r14 { // VERSIONS (R_13b1, R_14)
		if l.dimgap, err = r.ReadBD(); err != nil {
			return nil, err
		}
	}
	if l.boxHeight, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if l.boxWidth, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if v, err = r.ReadB(); err != nil { // hookline_dir
		return nil, err
	}
	l.hooklineDir = v != 0
	if v, err = r.ReadB(); err != nil { // arrowhead_on
		return nil, err
	}
	l.arrowheadOn = v != 0
	at, err := r.ReadBS() // arrowhead_type（BSx 无符号）
	if err != nil {
		return nil, err
	}
	l.arrowheadType = at
	l.calcHooklineOn() // DECODER：hookline_on 计算值
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
		l.unknownBit4 = v != 0
		if v, err = r.ReadB(); err != nil { // unknown_bit_5
			return nil, err
		}
		l.unknownBit5 = v != 0
	} else { // SINCE (R_2000b)
		if v, err = r.ReadB(); err != nil {
			return nil, err
		}
		l.unknownBit4 = v != 0
		if v, err = r.ReadB(); err != nil {
			return nil, err
		}
		l.unknownBit5 = v != 0
	}
	owner, layer := decodeOwnerLayer(r, head)
	l.owner, l.layer = owner, layer
	return l, nil
}

// ---- MLINE ----

// entMLineVertex 多线顶点。segParams/areaParams 为全部样式线的扁平参数
// 数组（DXF 组码语义同构）；segCounts/areaCounts 记录每条样式线的参数数
// （DWG/JSON 来源可按线分组还原 gold lines[j].segparms，DXF 无分组信息）。
type entMLineVertex struct {
	position, direction, miter point3
	segParams                  []float64
	areaParams                 []float64
	segCounts                  []int
	areaCounts                 []int
}

// entMLine 多线实体。
type entMLine struct {
	baseEntity
	scale         float64
	justification uint8
	openClosed    uint16
	linesInStyle  uint8
	// basePoint/extrusion 主体 3BD（渲染平铺与法向，decodeMline 读取后
	// 保留；gold JSON 同名键导出对照）
	basePoint   point3
	extrusion   point3
	vertices    []entMLineVertex
	styleHandle uint64
}

// decodeMline MLINE：BD 比例 + RC 对齐 + 3BD 基点 + 3BD 挤出 + BS 开闭 +
// RC 线数 + 顶点数组（3×3BD + 段参数/区域参数数组）。
func decodeMline(r *bitstream.BitStream, head *commonEntityHead) (any, error) {
	m := &entMLine{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}}
	var err error
	if m.scale, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if m.justification, err = r.ReadRC(); err != nil {
		return nil, err
	}
	var bpx, bpy, bpz float64
	if bpx, bpy, bpz, err = r.Read3BD(); err != nil { // base point
		return nil, err
	}
	m.basePoint = point3{bpx, bpy, bpz}
	var ex, ey, ez float64
	if ex, ey, ez, err = r.Read3BD(); err != nil { // extrusion
		return nil, err
	}
	m.extrusion = point3{ex, ey, ez}
	if m.openClosed, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if m.linesInStyle, err = r.ReadRC(); err != nil {
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
		var v entMLineVertex
		if v.position, err = read3pt(r); err != nil {
			return nil, err
		}
		if v.direction, err = read3pt(r); err != nil {
			return nil, err
		}
		if v.miter, err = read3pt(r); err != nil {
			return nil, err
		}
		// 每条样式线的段参数与区域参数均无条件读取（dwg.spec MLINE 同序）
		for line := 0; line < int(m.linesInStyle); line++ {
			numSeg, e := r.ReadBS()
			if e != nil {
				return nil, e
			}
			if uint32(numSeg) > 1_000_000 {
				return nil, fmt.Errorf("cad: MLINE 段参数数异常 %d", numSeg)
			}
			for j := uint32(0); j < uint32(numSeg); j++ {
				p, e := r.ReadBD()
				if e != nil {
					return nil, e
				}
				v.segParams = append(v.segParams, p)
			}
			v.segCounts = append(v.segCounts, int(numSeg))
			numArea, e := r.ReadBS()
			if e != nil {
				return nil, e
			}
			if uint32(numArea) > 1_000_000 {
				return nil, fmt.Errorf("cad: MLINE 区域参数数异常 %d", numArea)
			}
			for j := uint32(0); j < uint32(numArea); j++ {
				p, e := r.ReadBD()
				if e != nil {
					return nil, e
				}
				v.areaParams = append(v.areaParams, p)
			}
			v.areaCounts = append(v.areaCounts, int(numArea))
		}
		m.vertices = append(m.vertices, v)
	}
	owner, layer := decodeOwnerLayer(r, head)
	m.owner, m.layer = owner, layer
	// handle 流：公共序（reactors/xdic/layer/ltype/prev/next）之后为
	// MLINESTYLE 句柄（批次 B 修正：原直读首个句柄会命中公共序前置项）
	r.SetBitPos(head.objSizeBit)
	if owner2, layer2, e := parseCommonEntityHandles(r, head); e == nil {
		m.owner, m.layer = owner2, layer2
		if h, e2 := objrec.ReadHandleReference(r, head.handle); e2 == nil {
			m.styleHandle = h
		}
	}
	return m, nil
}

// ---- POLYLINE_2D / POLYLINE_3D + VERTEX ----

// entVertex2d POLYLINE_2D 顶点。
type entVertex2d struct {
	baseEntity
	flags      uint16
	position   point3
	startWidth float64
	endWidth   float64
	bulge      float64
	// id 顶点标识符（R2010+ spec 字段 BL0，DXF 91；审计导出）
	id         int64
	tangentDir float64
}

// decodeVertex2d VERTEX_2D：RC 标志 + 3BD 位置 + 起末宽（负起始宽取绝对值并
// 复用）+ BD 凸度 + [R2010+ BL0 顶点 id] + BD 切向。
func decodeVertex2d(r *bitstream.BitStream, head *commonEntityHead, r2010Plus bool) (any, error) {
	flags, err := r.ReadRC()
	if err != nil {
		return nil, err
	}
	v := &entVertex2d{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}, flags: uint16(flags)}
	if v.position, err = read3pt(r); err != nil {
		return nil, err
	}
	if v.startWidth, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if v.startWidth < 0 {
		v.startWidth = -v.startWidth
		v.endWidth = v.startWidth
	} else if v.endWidth, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if v.bulge, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if r2010Plus {
		// FIELD_BL0(id, 91)：R2010+ 顶点标识符（uhengshenhua 实证 714 顶点）
		var vid uint32
		if vid, err = r.ReadBL(); err != nil {
			return nil, err
		}
		v.id = int64(vid)
	}
	if v.tangentDir, err = r.ReadBD(); err != nil {
		return nil, err
	}
	owner, layer := decodeOwnerLayer(r, head)
	v.owner, v.layer = owner, layer
	return v, nil
}

// entVertex3d POLYLINE_3D 顶点。
type entVertex3d struct {
	baseEntity
	flags    uint8
	position point3
}

// decodeVertex3d VERTEX_3D：RC 标志 + 3BD 位置。
func decodeVertex3d(r *bitstream.BitStream, head *commonEntityHead) (any, error) {
	flags, err := r.ReadRC()
	if err != nil {
		return nil, err
	}
	v := &entVertex3d{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}, flags: flags}
	if v.position, err = read3pt(r); err != nil {
		return nil, err
	}
	owner, layer := decodeOwnerLayer(r, head)
	v.owner, v.layer = owner, layer
	return v, nil
}

// entVertexPface 面网格顶点（VERTEX_PFACE/VERTEX_MESH 共用布局，gold 键
// point/flag；POLYLINE_PFACE 的顶点由 owner 归属聚合）。
type entVertexPface struct {
	baseEntity
	flag     uint8
	position point3
}

// decodeVertexPface VERTEX_PFACE/VERTEX_MESH：RC 标志 + 3BD 位置
// （dwg.spec VERTEX_PFACE LATER_VERSIONS 分支；exr13 trace flag 0xc0
// = MESH|PFACE_MESH 位，point 为世界坐标）。
func decodeVertexPface(r *bitstream.BitStream, head *commonEntityHead) (any, error) {
	flag, err := r.ReadRC()
	if err != nil {
		return nil, err
	}
	v := &entVertexPface{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}, flag: flag}
	if v.position, err = read3pt(r); err != nil {
		return nil, err
	}
	v.owner, v.layer = decodeOwnerLayer(r, head)
	return v, nil
}

// entVertexPfaceFace 面网格面记录（VERTEX_PFACE_FACE，gold 键 vertind）：
// POLYLINE_PFACE 的面顶点索引（1 基，0 表示边结束）。
type entVertexPfaceFace struct {
	baseEntity
	flag    uint8
	vertind [4]int32
}

// decodeVertexPfaceFace VERTEX_PFACE_FACE：4×BSd 顶点索引；flag 不从流读，
// LibreDWG 恒写 128（dwg.spec LATER_VERSIONS 分支 FIELD_VALUE (flag) = 128）。
func decodeVertexPfaceFace(r *bitstream.BitStream, head *commonEntityHead) (any, error) {
	f := &entVertexPfaceFace{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}, flag: 128}
	for i := 0; i < 4; i++ {
		v, err := r.ReadBS()
		if err != nil {
			return nil, err
		}
		// BSd 有符号语义：顶点索引正常为 0~正数，负值按 int16 解释
		f.vertind[i] = int32(int16(v))
	}
	f.owner, f.layer = decodeOwnerLayer(r, head)
	return f, nil
}

// entUnknownEnt UNKNOWN_ENT 兜底实体：未建模动态类实体（如 ACAD_TABLE，
// LibreDWG 默认构建同样兜底为 UNKNOWN_ENT）的通用容器。位流布局按
// dwg2.spec UNKNOWN_ENT = HANDLE_UNKNOWN_BITS：公共头之外的主体不解析，
// 原始位串由 fillMeta/collectEntityRawBits 自动收集，roundtrip 回放保留。
type entUnknownEnt struct {
	baseEntity
}

// unknownEntFallbackNames UNKNOWN_ENT 兜底类名名单：仅收录 LibreDWG 默认
// 构建无专门解码器、实体输出 UNKNOWN_ENT 的动态类（example_r14 等九样本
// dwgread 重跑实证 type=528 ACAD_TABLE → UNKNOWN_ENT）。LIGHT/MULTILEADER
// 等有正式 spec 布局的类不在名单，兜底会掩盖真实类型导致对齐失真。
var unknownEntFallbackNames = map[string]bool{
	"ACAD_TABLE": true,
}

// decodeUnknownEnt UNKNOWN_ENT 兜底解码：不读主体字段，仅解析 handle 流
// 的 owner/layer；原始类名（如 ACAD_TABLE）存 extra.dxfname，对齐
// LibreDWG「实体键输出 UNKNOWN_ENT、DXF 名保留原类」的兜底语义。
func decodeUnknownEnt(r *bitstream.BitStream, head *commonEntityHead, dxfname string) (any, error) {
	e := &entUnknownEnt{baseEntity: baseEntity{
		handle: head.handle, color: head.color, mode: head.entityMode,
		extra: map[string]any{"dxfname": dxfname},
	}}
	e.owner, e.layer = decodeOwnerLayer(r, head)
	return e, nil
}

// entHelix 螺旋线实体（AcDbHelix：SPLINE 布局前缀 + 螺旋专有字段，
// dwg2.spec DWG_ENTITY (HELIX)，scenario/degree/num_knots 等经
// 2000/Helix.dwg dwgread -v9 trace 现场核对）。
type entHelix struct {
	baseEntity
	scenario    uint32 // 1=控制点样条 2=拟合点样条
	splineFlags uint32 // R2013+ 流内标志（此前由 scenario 推导 8/9）
	knotParam   uint32 // R2013+ 节点参数化
	degree      uint32
	rational    bool
	closed      bool
	periodic    bool
	knotTol     float64
	ctrlTol     float64
	knots       []float64
	ctrlPts     []point3
	weights     []float64 // weighted 时逐点权重，否则空
	fitTol      float64
	begTanVec   point3
	endTanVec   point3
	fitPts      []point3
	// AcDbHelix 专有
	majorVersion   uint32
	maintVersion   uint32
	axisBasePt     point3
	startPt        point3
	axisVector     point3
	radius         float64
	turns          float64
	turnHeight     float64
	handedness     bool
	constraintType uint8
}

// decodeHelixVer HELIX：scenario BL + [R2013+ splineflags/knotparam BL] +
// degree BL + scenario 分支字段 + AcDbHelix 专有（major/maint version +
// axis_base_pt/start_pt/axis_vector 3BD + radius/turns/turn_height BD +
// handedness B + constraint_type RC）。
func decodeHelixVer(r *bitstream.BitStream, head *commonEntityHead, ver dwgVersion) (any, error) {
	hx := &entHelix{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}}
	var err error
	if hx.scenario, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if ver >= verR2013 {
		if hx.splineFlags, err = r.ReadBL(); err != nil {
			return nil, err
		}
		if hx.knotParam, err = r.ReadBL(); err != nil {
			return nil, err
		}
	} else {
		// UNTIL R_2013：无流内标志，scenario 推导（1→8 planar、2→9）
		if hx.scenario != 1 && hx.scenario != 2 {
			return nil, fmt.Errorf("cad: HELIX scenario 异常 %d", hx.scenario)
		}
		if hx.scenario == 1 {
			hx.splineFlags = 8
		} else {
			hx.splineFlags = 9
		}
	}
	if hx.degree, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if hx.scenario&1 != 0 { // 控制点样条
		var v uint8
		if v, err = r.ReadB(); err != nil {
			return nil, err
		}
		hx.rational = v != 0
		if v, err = r.ReadB(); err != nil {
			return nil, err
		}
		hx.closed = v != 0
		if v, err = r.ReadB(); err != nil {
			return nil, err
		}
		hx.periodic = v != 0
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
			hx.knots = append(hx.knots, k)
		}
		for i := uint32(0); i < numCtrl; i++ {
			var p point3
			if p, err = read3pt(r); err != nil {
				return nil, err
			}
			hx.ctrlPts = append(hx.ctrlPts, p)
			if weighted != 0 {
				var w float64
				if w, err = r.ReadBD(); err != nil {
					return nil, err
				}
				hx.weights = append(hx.weights, w)
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
			var p point3
			if p, err = read3pt(r); err != nil {
				return nil, err
			}
			hx.fitPts = append(hx.fitPts, p)
		}
	}
	// AcDbHelix 专有
	if hx.majorVersion, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if hx.maintVersion, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if hx.axisBasePt, err = read3pt(r); err != nil {
		return nil, err
	}
	if hx.startPt, err = read3pt(r); err != nil {
		return nil, err
	}
	if hx.axisVector, err = read3pt(r); err != nil {
		return nil, err
	}
	if hx.radius, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if hx.turns, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if hx.turnHeight, err = r.ReadBD(); err != nil {
		return nil, err
	}
	var hb uint8
	if hb, err = r.ReadB(); err != nil {
		return nil, err
	}
	hx.handedness = hb != 0
	if hx.constraintType, err = r.ReadRC(); err != nil {
		return nil, err
	}
	owner, layer := decodeOwnerLayer(r, head)
	hx.owner, hx.layer = owner, layer
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
type entPolyline2d struct {
	baseEntity
	flags      uint16
	curveType  uint16
	widthStart float64
	widthEnd   float64
	thickness  float64
	elevation  float64
	extrusion  point3
	// firstVertex/lastVertex R13~R2000 handle 流的首末顶点句柄（顶点全量
	// 由 owner 归属聚合，见 assemblePolylineChildren）
	firstVertex  uint64
	lastVertex   uint64
	ownedHandles []uint64
	seqend       uint64 // SEQEND 结束句柄（顶点句柄后，gold seqend 键导出）
}

// decodePolyline2d POLYLINE_2D：BS 标志 + BS 曲线类型 + 起末宽 + BT 厚 +
// BD 高程 + BE 挤出 + [R2004+ BL 顶点数] + handle 流顶点句柄。
func decodePolyline2d(r *bitstream.BitStream, head *commonEntityHead, hasOwnedCount bool) (any, error) {
	p := &entPolyline2d{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}}
	var err error
	if p.flags, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if p.curveType, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if p.widthStart, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if p.widthEnd, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if p.thickness, err = r.ReadBT(); err != nil {
		return nil, err
	}
	if p.elevation, err = r.ReadBD(); err != nil {
		return nil, err
	}
	// extrusion 为 FIELD_BE（单位向量压缩，默认 (0,0,1) 仅 2 位）：
	// 3BD 读法在默认值时多读 4 位使 num_owned 错位
	// （uhengshenhua h=17453 num_owned=418 实证，LibreDWG spec 同为 BE）
	if ex, ey, ez, eErr := r.ReadBE(); eErr != nil {
		return nil, eErr
	} else {
		p.extrusion = point3{ex, ey, ez}
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
	owner, layer := decodeOwnerLayer(r, head)
	p.owner, p.layer = owner, layer
	// handle 流：顶点句柄在公共句柄之后
	r.SetBitPos(head.objSizeBit)
	// 先按公共头结构读 owner/reactors/xdic/layer 等，再读 owned 句柄
	if _, layer2, e := parseCommonEntityHandles(r, head); e == nil {
		p.layer = layer2
	}
	if !hasOwnedCount {
		// R13~R2000（dwg.spec POLYLINE VERSIONS(R_13,R_2000) 分支）：
		// 公共句柄流后为 first_vertex/last_vertex 两个句柄引用
		if p.firstVertex, err = objrec.ReadHandleReference(r, head.handle); err != nil {
			return nil, err
		}
		if p.lastVertex, err = objrec.ReadHandleReference(r, head.handle); err != nil {
			return nil, err
		}
	}
	for i := 0; i < ownedCount; i++ {
		h, e := objrec.ReadHandleReference(r, head.handle)
		if e != nil {
			break
		}
		p.ownedHandles = append(p.ownedHandles, h)
	}
	// SEQEND 结束句柄（顶点句柄之后恒在，gold seqend 键导出）
	if h, e := objrec.ReadHandleReference(r, head.handle); e == nil {
		p.seqend = h
	}
	return p, nil
}

// entPolyline3d 三维多段线。
type entPolyline3d struct {
	baseEntity
	flags75 uint8
	flags70 uint8
	// firstVertex/lastVertex R13~R2000 handle 流的首末顶点句柄（顶点全量
	// 由 owner 归属聚合，见 assemblePolylineChildren）
	firstVertex  uint64
	lastVertex   uint64
	ownedHandles []uint64
	seqend       uint64 // SEQEND 结束句柄（顶点句柄后，gold seqend 键导出）
}

// decodePolyline3d POLYLINE_3D：RC×2 标志 + [R2004+ BL 顶点数] + handle 流顶点句柄。
func decodePolyline3d(r *bitstream.BitStream, head *commonEntityHead, hasOwnedCount bool) (any, error) {
	p := &entPolyline3d{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}}
	var err error
	if p.flags75, err = r.ReadRC(); err != nil {
		return nil, err
	}
	if p.flags70, err = r.ReadRC(); err != nil {
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
	owner, layer := decodeOwnerLayer(r, head)
	p.owner, p.layer = owner, layer
	r.SetBitPos(head.objSizeBit)
	if _, layer2, e := parseCommonEntityHandles(r, head); e == nil {
		p.layer = layer2
	}
	if !hasOwnedCount {
		// R13~R2000（dwg.spec POLYLINE VERSIONS(R_13,R_2000) 分支）：
		// 公共句柄流后为 first_vertex/last_vertex 两个句柄引用
		if p.firstVertex, err = objrec.ReadHandleReference(r, head.handle); err != nil {
			return nil, err
		}
		if p.lastVertex, err = objrec.ReadHandleReference(r, head.handle); err != nil {
			return nil, err
		}
	}
	for i := 0; i < ownedCount; i++ {
		h, e := objrec.ReadHandleReference(r, head.handle)
		if e != nil {
			break
		}
		p.ownedHandles = append(p.ownedHandles, h)
	}
	// SEQEND 结束句柄（顶点句柄之后恒在，gold seqend 键导出）
	if h, e := objrec.ReadHandleReference(r, head.handle); e == nil {
		p.seqend = h
	}
	return p, nil
}

// assemblePolylineChildren 将顶点子实体聚合到所属多段线（渲染/对照用），
// 在 classify 之后由 decodeObjects 统一调用。
// R2004+ 的顶点句柄已在 decodePolyline* 的 handle 流中解码（ownedHandles
// 非空，按流内顺序），不重复聚合；R13~R2000 通过 VERTEX.owner 归属收集，
// 按 handle 升序排列（顶点句柄连续分配，升序即 first→last 链序）。
func (d *Document) assemblePolylineChildren() {
	parent2 := map[uint64]*entPolyline2d{}
	parent3 := map[uint64]*entPolyline3d{}
	link := func(list []any) {
		for _, e := range list {
			switch t := e.(type) {
			case *entPolyline2d:
				parent2[t.handle] = t
			case *entPolyline3d:
				parent3[t.handle] = t
			}
		}
	}
	link(d.modelSpace)
	for _, list := range d.blocks {
		link(list)
	}
	// verts2d/verts3d 按 owner 收集顶点句柄（升序由最终 sort 保证）
	verts2d := map[uint64][]uint64{}
	verts3d := map[uint64][]uint64{}
	gather := func(list []any) {
		for _, e := range list {
			switch v := e.(type) {
			case *entVertex2d:
				if v.owner != 0 {
					verts2d[v.owner] = append(verts2d[v.owner], v.handle)
				}
			case *entVertex3d:
				if v.owner != 0 {
					verts3d[v.owner] = append(verts3d[v.owner], v.handle)
				}
			}
		}
	}
	gather(d.modelSpace)
	for _, list := range d.blocks {
		gather(list)
	}
	for h, p := range parent2 {
		if len(p.ownedHandles) > 0 {
			continue
		}
		p.ownedHandles = append(p.ownedHandles, verts2d[h]...)
		sortHandles(p.ownedHandles)
	}
	for h, p := range parent3 {
		if len(p.ownedHandles) > 0 {
			continue
		}
		p.ownedHandles = append(p.ownedHandles, verts3d[h]...)
		sortHandles(p.ownedHandles)
	}
}

// sortHandles 句柄升序排序（顶点句柄连续分配时等价于 first→last 链序）。
func sortHandles(hs []uint64) {
	sort.Slice(hs, func(i, j int) bool { return hs[i] < hs[j] })
}

// entTolerance 形位公差实体。
type entTolerance struct {
	baseEntity
	text         string // text_value：标注文本（R2007+ 存于字符串区）
	unknownShort uint16 // R13/R14 头部的未知短整型
	insertion    point3 // ins_pt 插入点
	xDirection   point3 // x_direction 对称轴方向
	extrusion    point3 // 挤出方向
	height       float64
	dimgap       float64
	dimstyle     uint64 // dimstyle 句柄（handle 流）
	polyline     []point2
}

// decodeTolerance TOLERANCE 解码兼容入口（版本由 head 推断）。
func decodeTolerance(r *bitstream.BitStream, head *commonEntityHead) (any, error) {
	ver := verR2013
	if head.r13r14 {
		ver = verR14
	}
	return decodeToleranceVer(r, head, ver)
}

// decodeToleranceVer TOLERANCE（AcDbFcf）：[R13/R14: unknown_short BS +
// height BD + dimgap BD] + ins_pt/x_direction/extrusion 3BD + text_value
// （R2007+ 字符串区零占位，否则内联 TV）→ handle 流（owner/layer/dimstyle）。
func decodeToleranceVer(r *bitstream.BitStream, head *commonEntityHead, ver dwgVersion) (any, error) {
	tol := &entTolerance{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}}
	var err error
	if ver == verR13 || ver == verR14 {
		var us uint16
		if us, err = r.ReadBS(); err != nil {
			return nil, err
		}
		tol.unknownShort = us
		if tol.height, err = r.ReadBD(); err != nil {
			return nil, err
		}
		if tol.dimgap, err = r.ReadBD(); err != nil {
			return nil, err
		}
	}
	if tol.insertion, err = read3pt(r); err != nil {
		return nil, err
	}
	if tol.xDirection, err = read3pt(r); err != nil {
		return nil, err
	}
	if tol.extrusion, err = read3pt(r); err != nil {
		return nil, err
	}
	if ver < verR2007 {
		if tol.text, err = r.ReadTV(512); err != nil {
			return nil, err
		}
	}
	owner, layer := decodeOwnerLayer(r, head)
	tol.owner, tol.layer = owner, layer
	// handle 流：owner/layer 之后为 dimstyle 引用
	r.SetBitPos(head.objSizeBit)
	if _, layer2, e := parseCommonEntityHandles(r, head); e == nil {
		tol.layer = layer2
	}
	if h, e := objrec.ReadHandleReference(r, head.handle); e == nil && h != 0 {
		tol.dimstyle = h
	}
	if ver >= verR2007 {
		tol.text = streamAreaText(r, head)
	}
	return tol, nil
}

// entPolylinePface 多面网格（顶点/面数由子实体归属）。
// entPolylinePface 三维多面网格（顶点由 owner 归属聚合）。
type entPolylinePface struct {
	baseEntity
	numVertices int
	numFaces    int
	// firstVertex/lastVertex R13~R2000 handle 流的首末顶点句柄；R2004+ 为
	// num_owned 计数向量（ownedHandles），与 POLYLINE_2D 同构
	firstVertex  uint64
	lastVertex   uint64
	ownedHandles []uint64
	seqend       uint64 // SEQEND 结束句柄（顶点句柄后，gold seqend 键导出）
}

// decodePolylinePface POLYLINE_PFACE：BS 顶点数 + BS 面数 + [R2004+ BL
// 顶点数] + handle 流顶点句柄（R13~R2000 first/last、R2004+ owned 向量）
// + SEQEND。
func decodePolylinePface(r *bitstream.BitStream, head *commonEntityHead, hasOwnedCount bool) (any, error) {
	p := &entPolylinePface{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}}
	nv, err := r.ReadBS()
	if err != nil {
		return nil, err
	}
	nf, err := r.ReadBS()
	if err != nil {
		return nil, err
	}
	p.numVertices, p.numFaces = int(nv), int(nf)
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
	owner, layer := decodeOwnerLayer(r, head)
	p.owner, p.layer = owner, layer
	r.SetBitPos(head.objSizeBit)
	if _, layer2, e := parseCommonEntityHandles(r, head); e == nil {
		p.layer = layer2
	}
	if !hasOwnedCount {
		// R13~R2000：公共句柄流后为 first_vertex/last_vertex 两个句柄引用
		if p.firstVertex, err = objrec.ReadHandleReference(r, head.handle); err != nil {
			return nil, err
		}
		if p.lastVertex, err = objrec.ReadHandleReference(r, head.handle); err != nil {
			return nil, err
		}
	}
	for i := 0; i < ownedCount; i++ {
		h, e := objrec.ReadHandleReference(r, head.handle)
		if e != nil {
			break
		}
		p.ownedHandles = append(p.ownedHandles, h)
	}
	// SEQEND 结束句柄（顶点句柄之后恒在，gold seqend 键导出）
	if h, e := objrec.ReadHandleReference(r, head.handle); e == nil {
		p.seqend = h
	}
	return p, nil
}

// ---- VIEWPORT / SHAPE / POLYLINE_MESH ----

// entViewport 视口实体（dwg.spec VIEWPORT：center/width/height 全版本 +
// R2000+ 视图/捕捉/UCS 参数 + R2007+ 灯光/环境色）。
type entViewport struct {
	baseEntity
	center               point3
	width, height        float64
	viewTarget           point3
	viewDir              point3
	viewTwist            float64
	viewSize             float64
	lensLength           float64
	frontZ, backZ        float64
	snapAng              float64
	viewCtr              point2
	snapBase             point2
	snapUnit             point2
	gridUnit             point2
	circleZoom           uint16
	gridMajor            uint16
	numFrozenLayers      uint32
	statusFlag           uint32
	styleSheet           string
	renderMode           uint8
	ucsAtOrigin, ucsVP   bool
	ucsorg               point3
	ucsxdir, ucsydir     point3
	ucsElevation         float64
	ucsOrthoView         uint16
	shadeplotMode        uint16
	useDefaultLights     bool
	defaultLightingType  uint8
	brightness, contrast float64
	ambientIndex         uint16
	ambientRGB           uint32
}

// decodeViewport VIEWPORT 解码兼容入口（版本由 head 推断）。
func decodeViewport(r *bitstream.BitStream, head *commonEntityHead) (any, error) {
	ver := verR2013
	if head.r13r14 {
		ver = verR14
	}
	return decodeViewportVer(r, head, ver)
}

// decodeViewportVer VIEWPORT：center 3BD + width/height BD 全版本；
// R2000+ 依次为 view_target/VIEWDIR 3BD、VIEWTWIST/VIEWSIZE/LENSLENGTH/
// FRONTZ/BACKZ/SNAPANG BD、VIEWCTR/SNAPBASE/SNAPUNIT/GRIDUNIT 2RD、
// circle_zoom BS、num_frozen_layers BL、status_flag BL、style_sheet T、
// render_mode RC、UCS 段、shadeplot（R2004+）、grid_major（R2007+）；
// R2007+ 尾部为灯光段（use_default_lights/default_lighting_type/
// brightness/contrast/ambient_color CMC）。style_sheet 与字符串类字段
// 一致：R2007+ 存于字符串区主数据流零占位。
func decodeViewportVer(r *bitstream.BitStream, head *commonEntityHead, ver dwgVersion) (any, error) {
	vp := &entViewport{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}}
	var err error
	if vp.center, err = read3pt(r); err != nil {
		return nil, err
	}
	if vp.width, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if vp.height, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if ver == verR13 || ver == verR14 {
		owner, layer := decodeOwnerLayer(r, head)
		vp.owner, vp.layer = owner, layer
		return vp, nil
	}
	if vp.viewTarget, err = read3pt(r); err != nil {
		return nil, err
	}
	if vp.viewDir, err = read3pt(r); err != nil {
		return nil, err
	}
	if vp.viewTwist, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if vp.viewSize, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if vp.lensLength, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if vp.frontZ, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if vp.backZ, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if vp.snapAng, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if vp.viewCtr.x, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if vp.viewCtr.y, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if vp.snapBase.x, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if vp.snapBase.y, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if vp.snapUnit.x, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if vp.snapUnit.y, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if vp.gridUnit.x, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if vp.gridUnit.y, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if vp.circleZoom, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if ver >= verR2007 {
		if vp.gridMajor, err = r.ReadBS(); err != nil {
			return nil, err
		}
	}
	if vp.numFrozenLayers, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if vp.statusFlag, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if ver < verR2007 {
		if vp.styleSheet, err = r.ReadTV(256); err != nil {
			return nil, err
		}
	}
	if vp.renderMode, err = r.ReadRC(); err != nil {
		return nil, err
	}
	var bv uint8
	if bv, err = r.ReadB(); err != nil {
		return nil, err
	}
	vp.ucsAtOrigin = bv != 0
	if bv, err = r.ReadB(); err != nil {
		return nil, err
	}
	vp.ucsVP = bv != 0
	if vp.ucsorg, err = read3pt(r); err != nil {
		return nil, err
	}
	if vp.ucsxdir, err = read3pt(r); err != nil {
		return nil, err
	}
	if vp.ucsydir, err = read3pt(r); err != nil {
		return nil, err
	}
	if vp.ucsElevation, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if vp.ucsOrthoView, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if ver >= verR2004 {
		if vp.shadeplotMode, err = r.ReadBS(); err != nil {
			return nil, err
		}
	}
	if ver >= verR2007 {
		var bv uint8
		if bv, err = r.ReadB(); err != nil {
			return nil, err
		}
		vp.useDefaultLights = bv != 0
		if vp.defaultLightingType, err = r.ReadRC(); err != nil {
			return nil, err
		}
		if vp.brightness, err = r.ReadBD(); err != nil {
			return nil, err
		}
		if vp.contrast, err = r.ReadBD(); err != nil {
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
	owner, layer := decodeOwnerLayer(r, head)
	vp.owner, vp.layer = owner, layer
	if ver >= verR2007 {
		vp.styleSheet = streamAreaText(r, head)
	}
	return vp, nil
}

// entShape 形参照实体。
type entShape struct {
	baseEntity
	insertion   point3
	scale       float64
	rotation    float64
	widthFactor float64
	oblique     float64
	thickness   float64
	styleId     uint16 // STYLE 表索引（gold style_id 键）
	shapeNo     uint16 // SHAPEFILE 内形编号（pre-R13 的 1 字节表索引，R13+ 在位流 BS）
	extrusion   point3
}

// decodeShape SHAPE：3BD 插入点 + BD 缩放/旋转/宽度因子/倾斜/厚度 +
// BS 形编号 + 3BD 挤出。
func decodeShape(r *bitstream.BitStream, head *commonEntityHead) (any, error) {
	s := &entShape{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}}
	var err error
	if s.insertion, err = read3pt(r); err != nil {
		return nil, err
	}
	if s.scale, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if s.rotation, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if s.widthFactor, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if s.oblique, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if s.thickness, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if s.styleId, err = r.ReadBS(); err != nil { // STYLE 表索引（gold style_id）
		return nil, err
	}
	if s.extrusion, err = read3pt(r); err != nil {
		return nil, err
	}
	owner, layer := decodeOwnerLayer(r, head)
	s.owner, s.layer = owner, layer
	return s, nil
}

// entPolylineMesh 多边形网格。
type entPolylineMesh struct {
	baseEntity
	flags        uint16
	curveType    uint16
	mVertexCount uint16
	nVertexCount uint16
	mDensity     uint16
	nDensity     uint16
	ownedHandles []uint64
}

// decodePolylineMesh POLYLINE_MESH：6×BS 参数 + [R2004+ BL 顶点数] +
// handle 流顶点句柄。
func decodePolylineMesh(r *bitstream.BitStream, head *commonEntityHead, hasOwnedCount bool) (any, error) {
	m := &entPolylineMesh{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}}
	var err error
	if m.flags, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if m.curveType, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if m.mVertexCount, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if m.nVertexCount, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if m.mDensity, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if m.nDensity, err = r.ReadBS(); err != nil {
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
	owner, layer := decodeOwnerLayer(r, head)
	m.owner, m.layer = owner, layer
	r.SetBitPos(head.objSizeBit)
	if _, layer2, e := parseCommonEntityHandles(r, head); e == nil {
		m.layer = layer2
	}
	for i := 0; i < ownedCount; i++ {
		h, e := objrec.ReadHandleReference(r, head.handle)
		if e != nil {
			break
		}
		m.ownedHandles = append(m.ownedHandles, h)
	}
	return m, nil
}

// ---- REGION / 3DSOLID / BODY / WIPEOUT（R2000+ 覆盖实体）----

// entWipeout 区域覆盖实体（AcDbWipeout，位布局同 AcDbRasterImage）。
type entWipeout struct {
	baseEntity
	classVersion     uint32
	pt0, uvec, vvec  point3
	imageSize        point2
	displayProps     uint16
	clipping         bool
	brightness       uint8
	contrast         uint8
	fade             uint8
	clipMode         uint8 // 裁剪模式（clip_mode，R2010+）
	clipBoundaryType uint16
	clipVerts        []point2
	imageDef         uint64
	imageDefReactor  uint64
}

// decodeWipeoutVer WIPEOUT（R2000+）：class_version BL + pt0/uvec/vvec 3BD +
// image_size 2RD + display_props BS + clipping B + 亮度/对比/淡出 RC +
// clip_boundary_type BS + 裁剪顶点数组（dwg2.spec WIPEOUT，同 IMAGE 布局；
// imagedef/imagedefreactor 句柄在 handle 流）。主体后的未记载位
// （preview 等）按 objSizeBit 截断跳过。
func decodeWipeoutVer(r *bitstream.BitStream, head *commonEntityHead, ver dwgVersion) (any, error) {
	w := &entWipeout{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}}
	var err error
	if w.classVersion, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if w.classVersion > 10 {
		return nil, fmt.Errorf("cad: WIPEOUT class_version 异常 %d", w.classVersion)
	}
	if w.pt0, err = read3pt(r); err != nil {
		return nil, err
	}
	if w.uvec, err = read3pt(r); err != nil {
		return nil, err
	}
	if w.vvec, err = read3pt(r); err != nil {
		return nil, err
	}
	if w.imageSize.x, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if w.imageSize.y, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if w.displayProps, err = r.ReadBS(); err != nil {
		return nil, err
	}
	var v uint8
	if v, err = r.ReadB(); err != nil {
		return nil, err
	}
	w.clipping = v != 0
	if w.brightness, err = r.ReadRC(); err != nil {
		return nil, err
	}
	if w.contrast, err = r.ReadRC(); err != nil {
		return nil, err
	}
	if w.fade, err = r.ReadRC(); err != nil {
		return nil, err
	}
	if ver >= verR2010 {
		cm, err2 := r.ReadB() // clip_mode（R2010+）
		if err2 != nil {
			return nil, err2
		}
		w.clipMode = uint8(cm)
	}
	if w.clipBoundaryType, err = r.ReadBS(); err != nil {
		return nil, err
	}
	numVerts := uint32(2) // 矩形边界固定两角
	if w.clipBoundaryType != 1 {
		if numVerts, err = r.ReadBL(); err != nil {
			return nil, err
		}
	}
	if numVerts > 100_000 {
		return nil, fmt.Errorf("cad: WIPEOUT 裁剪顶点数异常 %d", numVerts)
	}
	for i := uint32(0); i < numVerts; i++ {
		var p point2
		if p.x, err = r.ReadRD(); err != nil {
			return nil, err
		}
		if p.y, err = r.ReadRD(); err != nil {
			return nil, err
		}
		w.clipVerts = append(w.clipVerts, p)
	}
	owner, layer := decodeOwnerLayer(r, head)
	w.owner, w.layer = owner, layer
	// handle 流：owner/layer 之后为 imagedef(5) 与 imagedefreactor(3)
	r.SetBitPos(head.objSizeBit)
	if _, layer2, e := parseCommonEntityHandles(r, head); e == nil {
		w.layer = layer2
	}
	if h, e := objrec.ReadHandleReference(r, head.handle); e == nil && h != 0 {
		w.imageDef = h
	}
	if h, e := objrec.ReadHandleReference(r, head.handle); e == nil && h != 0 {
		w.imageDefReactor = h
	}
	return w, nil
}

// ---- REGION / 3DSOLID / BODY（ACIS 类实体）----

// entAcis ACIS 类实体（REGION/3DSOLID/BODY）：
// 按 LibreDWG DECODE_3DSOLID 提取 SAT/SAB 文本块（几何内核不在解析范围），
// 尾部含 COMMON_3DSOLID 的线框/轮廓/材质/修订段。
type entAcis struct {
	baseEntity
	acisEmpty   bool
	version     uint16 // 1=SAT(ACIS 4.0 加密文本) 2=SAB(二进制)
	blocks      [][]byte
	acisData    []byte // 解混淆后的 SAT 文本
	sabSize     int
	acisHandles []uint64
	historyId   uint64 // history_id 句柄（R2004+ handle 流，gold history_id 键导出）
	kind        string
	// COMMON_3DSOLID 尾部字段
	unknown              uint8 // acis 主体前的未知位
	wireframeDataPresent bool
	pointPresent         bool
	point                point3
	isolines             uint32
	isolinePresent       bool
	numWires             uint32
	numSilhouettes       uint32
	wires                []acisWire       // 线框明细（wires[i] 标量，gold 值级对照）
	silhouettes          []acisSilhouette // 轮廓视图明细（silhouettes[i] 标量）
	acisEmptyBit         bool
	numMaterials         uint32
	materials            []acisMaterial // 材质数组标量（handle 在 handle 流）
	hasRevisionGuid      bool
	revisionMajor        uint32
	revisionMinor1       uint16
	revisionMinor2       uint16
	revisionBytes        []byte // R2013+ 修订 GUID 原始 8 字节
	endMarker            uint32
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
func acisDeobfuscate(raw []byte) []byte {
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
func decodeAcis(r *bitstream.BitStream, head *commonEntityHead, kind string, srcVer dwgVersion) (any, error) {
	ver := verR2013
	if head.r13r14 {
		ver = verR14
	}
	return decodeAcisVer(r, head, kind, ver, srcVer)
}

// decodeAcisVer ACIS/REGION/3DSOLID：acis_empty B → [unknown B + version BS +
// SAT 块循环（BL 大小 + TF 文本）| SAB 数据至 End 标记] → 线框/轮廓/材质/
// 修订段（COMMON_3DSOLID）→ handle 流。r13r14 为 R13/R14 布局（无 R2007+
// 材质与 R2013+ 修订段由版本条件内部判断，调用方传 ver）；srcVer 为分发层
// 真实文件版本（history_id 的 R2004+ 判断用它，宽容推断版 ver 不作依据）。
func decodeAcisVer(r *bitstream.BitStream, head *commonEntityHead, kind string, ver dwgVersion, srcVer dwgVersion) (any, error) {
	a := &entAcis{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}, kind: kind}
	acisEmpty, err := r.ReadB()
	if err != nil {
		return nil, err
	}
	a.acisEmpty = acisEmpty == 1
	if !a.acisEmpty {
		var unknown uint8
		if unknown, err = r.ReadB(); err != nil {
			return nil, err
		}
		a.unknown = unknown
		if a.version, err = r.ReadBS(); err != nil {
			return nil, err
		}
		switch a.version {
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
				a.blocks = append(a.blocks, acisDeobfuscate(raw))
				total += len(raw)
				if blockSize == 0 {
					break
				}
			}
			a.acisData = make([]byte, 0, total)
			for _, blk := range a.blocks {
				a.acisData = append(a.acisData, blk...)
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
					end := -1
					for _, marker := range []string{endACIS, endASM} {
						if i := bytes.Index(buf, []byte(marker)); i >= 0 {
							end = i + len(marker)
							break
						}
					}
					if end < 0 {
						end = size
					}
					if cadTraceHandle != 0 && cadTraceHandle == head.handle {
						fmt.Fprintf(os.Stderr, "[acis] handle=%d startBit=%d totalBits=%d size=%d end=%d head.objSizeBit=%v\n",
							head.handle, startBit, r.TotalBits(), size, end, head.objSizeBit)
					}
					a.sabSize = end
					a.acisData = append([]byte(nil), buf[:end]...)
					r.SetBitPos(startBit + uint64(end)*8)
				}
			}
		}
	}
	// COMMON_3DSOLID：线框/参考点/轮廓线段
	if err = decodeAcisWireframe(r, a); err != nil {
		// 线框/轮廓段损坏（LibreDWG 对此类记录同样错位报错）：
		// 放弃尾部字段降级纳管，直接落到 handle 流保证对象可用
		//（位流已错位，history_id 不可信不读取——R2013 样本实证）
		owner, layer := decodeOwnerLayer(r, head)
		a.owner, a.layer = owner, layer
		r.SetBitPos(head.objSizeBit)
		if _, layer2, e := parseCommonEntityHandles(r, head); e == nil {
			a.layer = layer2
		}
		a.acisHandles = readAcisHandles(r, head)
		return a, nil
	}
	var v uint8
	if v, err = r.ReadB(); err != nil { // acis_empty_bit
		return nil, err
	}
	a.acisEmptyBit = v != 0
	if a.version > 1 {
		if ver >= verR2007 {
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
				owner, layer := decodeOwnerLayer(r, head)
				a.owner, a.layer = owner, layer
				r.SetBitPos(head.objSizeBit)
				if _, layer2, e := parseCommonEntityHandles(r, head); e == nil {
					a.layer = layer2
				}
				// 位流已错位（材质计数越界），history_id 不可信不读取
				a.acisHandles = readAcisHandles(r, head)
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
	if ver >= verR2013 {
		if v, err = r.ReadB(); err != nil { // has_revision_guid
			return nil, err
		}
		a.hasRevisionGuid = v != 0
		if a.revisionMajor, err = r.ReadBL(); err != nil {
			return nil, err
		}
		var m uint16
		if m, err = r.ReadBS(); err != nil { // revision_minor1
			return nil, err
		}
		a.revisionMinor1 = m
		if m, err = r.ReadBS(); err != nil { // revision_minor2
			return nil, err
		}
		a.revisionMinor2 = m
		a.revisionBytes = make([]byte, 8)
		for i := 0; i < 8; i++ {
			if a.revisionBytes[i], err = r.ReadRC(); err != nil {
				return nil, err
			}
		}
		if a.endMarker, err = r.ReadBL(); err != nil {
			return nil, err
		}
	}
	owner, layer := decodeOwnerLayer(r, head)
	a.owner, a.layer = owner, layer
	for i := 0; i < int(a.numMaterials); i++ {
		if _, e := objrec.ReadHandleReference(r, head.handle); e != nil {
			break
		}
	}
	r.SetBitPos(head.objSizeBit)
	if owner2, layer2, e := parseCommonEntityHandles(r, head); e == nil {
		a.owner, a.layer = owner2, layer2
	}
	// history_id：R2004+ 公共句柄序之后的首个句柄引用（可 NULL）。
	// R13~R2000 handle 流在 prev/next 后即结束；acis_empty=1 的 AcDs
	// 空记录 R2013+ 修订段为错位垃圾（LibreDWG 同点 ERROR），不读取。
	if srcVer >= verR2004 && !a.acisEmpty {
		if h, e := objrec.ReadHandleReference(r, head.handle); e == nil {
			a.historyId = h
		}
	}
	a.acisHandles = readAcisHandles(r, head)
	return a, nil
}

// readAcisHandles 读取 ACIS 实体 handle 流的引用句柄（至多 8 个）。
func readAcisHandles(r *bitstream.BitStream, head *commonEntityHead) []uint64 {
	var out []uint64
	for i := 0; i < 8; i++ {
		h, err := objrec.ReadHandleReference(r, head.handle)
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
func decodeAcisWireframe(r *bitstream.BitStream, a *entAcis) error {
	var err error
	var v uint8
	if v, err = r.ReadB(); err != nil {
		return err
	}
	a.wireframeDataPresent = v != 0
	if !a.wireframeDataPresent {
		return nil
	}
	if v, err = r.ReadB(); err != nil {
		return err
	}
	a.pointPresent = v != 0
	if a.pointPresent {
		x, y, z, err := r.Read3BD()
		if err != nil {
			return err
		}
		a.point = point3{x, y, z}
	}
	if a.isolines, err = r.ReadBL(); err != nil {
		return err
	}
	if v, err = r.ReadB(); err != nil {
		return err
	}
	a.isolinePresent = v != 0
	if !a.isolinePresent {
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
	if a.numWires, err = r.ReadBL(); err != nil {
		return err
	}
	if a.numWires > 1_000_000 {
		return fmt.Errorf("cad: ACIS 线框数异常 %d", a.numWires)
	}
	for i := uint32(0); i < a.numWires; i++ {
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
func readStringAreaStrings(r *bitstream.BitStream, head *commonEntityHead, max int) []string {
	if head.objSizeBit < 34 {
		return nil
	}
	r2 := *r
	r2.SetBitPos(head.objSizeBit - 1)
	hs, err := r2.ReadB()
	if err != nil || hs != 1 {
		return nil
	}
	r2.SetBitPos(head.objSizeBit - 17)
	ds, err := r2.ReadRS()
	if err != nil {
		return nil
	}
	if ds&0x8000 != 0 {
		r2.SetBitPos(head.objSizeBit - 33)
		hi, e := r2.ReadRS()
		if e != nil {
			return nil
		}
		ds = ds&0x7fff | uint16(uint32(hi)<<15)
	}
	areaStart := int64(head.objSizeBit) - 17 - int64(ds)
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
func decodeSolidTolerant(r *bitstream.BitStream, head *commonEntityHead, trace bool) (any, error) {
	savedByte, savedBit := r.Cursor()
	ent, err := decodeSolid(r, head, trace)
	if err == nil {
		if s, ok := ent.(*entSolid); ok && entitySolidSane(s) {
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
	ent2, err2 := decodeSolid(&r2, head, trace)
	if err2 == nil {
		if s, ok := ent2.(*entSolid); ok && entitySolidSane(s) {
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
func decodeArcTolerant(r *bitstream.BitStream, head *commonEntityHead) (any, error) {
	savedByte, savedBit := r.Cursor()
	ent, err := decodeArc(r, head)
	if err == nil {
		if a, ok := ent.(*entArc); ok && arcAnglesSane(a) {
			return ent, nil
		}
	}
	r2 := *r
	r2.Restore(savedByte, savedBit)
	r2.LegacyBT = !r2.LegacyBT
	ent2, err2 := decodeArc(&r2, head)
	if err2 == nil {
		if a, ok := ent2.(*entArc); ok && arcAnglesSane(a) {
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
func arcAnglesSane(a *entArc) bool {
	return isFinite(a.angleStart) && isFinite(a.angleEnd) &&
		math.Abs(a.angleStart) < 1e6 && math.Abs(a.angleEnd) < 1e6
}

// entitySolidSane SOLID 角点量级检查（拒绝 1e-150 型错位读数）。
func entitySolidSane(e *entSolid) bool {
	for _, p := range [4]point2{e.p1, e.p2, e.p3, e.p4} {
		v := math.Abs(p.x) + math.Abs(p.y)
		if v > 0 && v < 1e-30 {
			return false
		}
	}
	return true
}
