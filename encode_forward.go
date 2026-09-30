// encode_forward.go 实现结构化正向写出层（dwgwrite 第三代编码方向）：
// 无容器回放素材（r2000Raw/r2004Raw/r2007Raw 均为 nil）的 Document——
// 即 ParseJSON / ParseDXF / 手工合成来源——按位流规范正向编码为 R2000
// （AC1015）容器 DWG。与 encode_object.go 的「原始位串回放」方案互补：
// 那是同版本位级往返，这里是跨来源结构重建（等价 LibreDWG dxf2dwg /
// dwgwrite -I json 的核心正向路径，对照其 encode.c 正向编码流程）。
//
// 主要模块划分：
//   - writeDwgForwardR2000：文件级组装（头部 → 段目录 → HeaderVars 段
//     （内嵌最小合法模板）→ Classes 段（空类表）→ 对象区 → 对象图）
//   - 实体侧正向编码器族 forwardEntityEncoders：公共头（handle/color/
//     layer 系 → handle 流）+ 16 类渲染同源主力实体的 body 字段位流
//   - 对象侧最小集：LAYER（表记录）与 DICTIONARY 的正向编码
//   - 位流辅助：BT/BE/3BD/DD 等读写原语的编码侧对称 + bitsize 两遍回填
package cad

import (
	"encoding/base64"
	"fmt"
	"math"
	"sort"
)

// forwardR2000FixedHeader example_2000.dwg 文件头 [0x00,0x15) 模板：版本串
// AC1015 + 未知区 + 码页。写出端回填 0x13 处 RS 码页（读侧 readCodepage
// 读取位），保证 detectVersion / readCodepage 与真实文件同构。
var forwardR2000FixedHeader = [0x15]byte{
	'A', 'C', '1', '0', '1', '5',
	0x00, 0x00, 0x00, 0x00, 0x00, 0x0F, 0x01, 0xDC, 0x00, 0x00, 0x00, 0x21, 0x1D,
	0x1E, 0x00, // RS codepage=30（ANSI_1252），写出端按 doc.codepage 回填
}

// forwardHeaderVarsB64 example_2000.dwg 的 HeaderVars 段整段字节（含前后
// 哨兵、RL size、RC 版本串、全量 R2000 图纸变量位流与段级 CRC），base64
// 内嵌。R2000 图纸变量共 129 项且位序随版本固定，逐项实现编码器收益低；
// 真实文件的合法段字节即最小模板——变量中的句柄指向的对象在正向场景
// 允许缺席（LibreDWG 读侧按 ref 悬空告警继续），本包读侧不解析该段。
var forwardHeaderVarsB64 = "z3sfI/3eOKlffGi4Tm0zXxwCAAAAAAcAH79V0JVAm0AqpSAtPNQ0QEzAtAkEpAaqkIQZBkGQZBkGQNRpQEEkyQAAAAAAABZQFqqqqoAAAAAAAA4D8AAAAAAAANEBQIuADqdCUAJrmMwQL4UlAAAE18BiAil8AgDil8AwIL51GKURFRFVEnURiqECMtXgdrxVEECMtXgdrxVEECMtXgdrxVEECMtXgdrxXEECMtXgdrxXEECMtXgdrxXEAAAAAGhmGcAAAABgZmYZwAAAAMDMEHFAAAAAjZkxakCqamUFCUKqqqqqqhdzbcIN3NEwQn1PYtY4U5wTrLX0rkFhrwB8DigL6EAqQQ8kNuDfBQwQTrLX0rkFhrQAAAAAAAAAAAAAAAAAAAAAAAAAAAAEB6QAAAAAAAkHJAqmplBQlCqqqqqqkAAAAAAAABEAAAAAAAAA5D8AAAAAAAADkAAAAAAAAA9D+qAQFCIAAAAAAAABEAAAAAAAAABECHWGQqFQKKQ/ZAAAAAAAAOQ/iBxVIEgSBIHSBJZRIRRA1ERUFBQUD+/z+/zEBMQIxAzEFMQYxBzEIMQkxCjIC0VENURcxDEBUalEaURlRDgdKgAAQSSd7RkRFQUQ1NzgtQTY1Mi0xMUQyLTlBMzUtMDA2MDA4OUIzQTNGfQBJ3syOUNFREY5Ny00NTJGLUFCNEYtOUE1QS1FNTdFODMyOTA2MzJ9AFFVUR9RFVEUURYhYEoiw0c0mJ6cNjCE4NwCIcdWoIOXR7GSzKA="

// forwardEntityCode 正向写出的实体类型码（objTypeCode 静态表的写出侧
// 子集，覆盖渲染同源主力实体与块结构标记）。
var forwardEntityCode = map[string]uint16{
	"TEXT":              0x01,
	"ATTRIB":            0x02,
	"ATTDEF":            0x03,
	"BLOCK":             0x04,
	"ENDBLK":            0x05,
	"SEQEND":            0x06,
	"INSERT":            0x07,
	"VERTEX_2D":         0x0A,
	"VERTEX_3D":         0x0B,
	"VERTEX_MESH":       0x0C,
	"VERTEX_PFACE":      0x0D,
	"VERTEX_PFACE_FACE": 0x0E,
	"POLYLINE_2D":       0x0F,
	"POLYLINE_3D":       0x10,
	"ARC":               0x11,
	"CIRCLE":            0x12,
	"LINE":              0x13,
	"DIM_ORDINATE":      0x14,
	"DIM_LINEAR":        0x15,
	"DIM_ALIGNED":       0x16,
	"DIM_ANG3PT":        0x17,
	"DIM_ANG2LN":        0x18,
	"DIM_RADIUS":        0x19,
	"DIM_DIAMETER":      0x1A,
	"POINT":             0x1B,
	"3DFACE":            0x1C,
	"POLYLINE_PFACE":    0x1D,
	"POLYLINE_MESH":     0x1E,
	"SOLID":             0x1F,
	"SHAPE":             0x21,
	"VIEWPORT":          0x22,
	"ELLIPSE":           0x23,
	"SPLINE":            0x24,
	"RAY":               0x28,
	"XLINE":             0x29,
	"MTEXT":             0x2C,
	"LEADER":            0x2D,
	"TOLERANCE":         0x2E,
	"MLINE":             0x2F,
	"LWPOLYLINE":        0x4D,
	"HATCH":             0x4E,
}

// forwardEntEncoder 实体 body 专有字段正向编码器：公共头之后、handle 流
// 之前的 dat 流段。编码顺序与读侧解码器（decodeLine/decodeCircle 等）
// 逐字段对称。
type forwardEntEncoder func(w *encWriter, ent any, ver dwgVersion) error

// forwardEntityEncoders 实体专有字段编码器注册表。SEQEND/ENDBLK 无
// 专有字段（BLOCK 为块名），注册表缺席即视为无字段。
var forwardEntityEncoders = map[string]forwardEntEncoder{
	"LINE":                   encFwdLine,
	"CIRCLE":                 encFwdCircle,
	"ARC":                    encFwdArc,
	"POINT":                  encFwdPoint,
	"ELLIPSE":                encFwdEllipse,
	"TEXT":                   encFwdText,
	"MTEXT":                  encFwdMText,
	"LWPOLYLINE":             encFwdLwPolyline,
	"INSERT":                 encFwdInsert,
	"ATTRIB":                 encFwdAttrib,
	"SOLID":                  encFwdSolid,
	"3DFACE":                 encFwdFace3d,
	"VERTEX_2D":              encFwdVertex2d,
	"POLYLINE_2D":            encFwdPolyline2d,
	"BLOCK":                  encFwdBlock,
	"SPLINE":                 encFwdSpline,
	"HATCH":                  encFwdHatch,
	"RAY":                    encFwdRay,
	"XLINE":                  encFwdRay,
	"LEADER":                 encFwdLeader,
	"MLINE":                  encFwdMLine,
	"TOLERANCE":              encFwdTolerance,
	"SHAPE":                  encFwdShape,
	"VIEWPORT":               encFwdViewport,
	"VERTEX_3D":              encFwdVertex3d,
	"VERTEX_MESH":            encFwdVertex3d,
	"VERTEX_PFACE":           encFwdVertex3d,
	"VERTEX_PFACE_FACE":      encFwdVertexPfaceFace,
	"POLYLINE_3D":            encFwdPolyline3d,
	"POLYLINE_PFACE":         encFwdPolylinePface,
	"POLYLINE_MESH":          encFwdPolylineMesh,
	"DIM_ORDINATE":           encFwdDimension,
	"DIM_LINEAR":             encFwdDimension,
	"DIM_ALIGNED":            encFwdDimension,
	"DIM_ANG3PT":             encFwdDimension,
	"DIM_ANG2LN":             encFwdDimension,
	"DIM_RADIUS":             encFwdDimension,
	"DIM_DIAMETER":           encFwdDimension,
	"ARC_DIMENSION":          encFwdDimension,
	"LARGE_RADIAL_DIMENSION": encFwdDimension,
	"IMAGE":                  encFwdImage,
	"WIPEOUT":                encFwdImage,
	"MULTILEADER":            encFwdMLeader,
	"LIGHT":                  encFwdLight,
}

// forwardEntityHandleExtra 类型专属 handle 流附加段（公共句柄之后的引用
// 序列，如 INSERT 的块头/属性/SEQEND、POLYLINE_2D 的首末顶点句柄、
// DIMENSION 的样式与匿名块、MLINE 的样式记录、TOLERANCE 的样式、
// POLYLINE_3D 的首末顶点、IMAGE 的定义句柄、MULTILEADER 的样式链）。
var forwardEntityHandleExtra = map[string]forwardEntEncoder{
	"INSERT":                 encFwdInsertHandles,
	"POLYLINE_2D":            encFwdPolylineHandles,
	"POLYLINE_3D":            encFwdPolyline3dHandles,
	"POLYLINE_PFACE":         encFwdPolylinePfaceHandles,
	"MLINE":                  encFwdMLineHandles,
	"TOLERANCE":              encFwdToleranceHandles,
	"IMAGE":                  encFwdImageHandles,
	"WIPEOUT":                encFwdImageHandles,
	"MULTILEADER":            encFwdMLeaderHandles,
	"DIM_ORDINATE":           encFwdDimensionHandles,
	"DIM_LINEAR":             encFwdDimensionHandles,
	"DIM_ALIGNED":            encFwdDimensionHandles,
	"DIM_ANG3PT":             encFwdDimensionHandles,
	"DIM_ANG2LN":             encFwdDimensionHandles,
	"DIM_RADIUS":             encFwdDimensionHandles,
	"DIM_DIAMETER":           encFwdDimensionHandles,
	"ARC_DIMENSION":          encFwdDimensionHandles,
	"LARGE_RADIAL_DIMENSION": encFwdDimensionHandles,
}

// forwardDynamicClassOrder 结构化正向写出的动态类注册表（R2000 容器的
// 动态类码从 500 起按本文档出现顺序分配，类段同步注册条目）。WIPEOUT 与
// IMAGE 位布局同构，分别注册（读侧按类名路由）。
var forwardDynamicClassOrder = []struct {
	dxf, cpp string
}{
	{"IMAGE", "AcDbRasterImage"},
	{"WIPEOUT", "AcDbWipeout"},
	{"MULTILEADER", "AcDbMLeader"},
	{"LIGHT", "AcDbLight"},
	{"ARC_DIMENSION", "AcDbArcDimension"},
	{"LARGE_RADIAL_DIMENSION", "AcDbLargeRadialDimension"},
}

// fwdDynamicEntityKind 实体 → 动态类注册名（非动态类返回空串）。动态类
// 的类型码不在 objTypeCode 静态表，写出需类段注册的动态码。
func fwdDynamicEntityKind(ent any) string {
	switch t := ent.(type) {
	case *entWipeout:
		if t.typeName == "IMAGE" || t.typeName == "WIPEOUT" {
			return t.typeName
		}
	case *entMLeader:
		return "MULTILEADER"
	case *entLight:
		return "LIGHT"
	case *entDimension:
		if t.typeName == "ARC_DIMENSION" || t.typeName == "LARGE_RADIAL_DIMENSION" {
			return t.typeName
		}
	}
	return ""
}

// fwdObject 待写出的单个对象：句柄 + 已编码 body 位流（字节对齐）。
type fwdObject struct {
	handle uint64
	body   []byte
}

// fwdEntityKind 按 Go 类型推导实体的写出类型名与类型码（不依赖
// baseEntity.typeName：DXF 来源未填充该字段，且 JSON 的 DIMENSION_ 全名
// 需归一为内部短名）。返回空串表示无正向编码器。
func fwdEntityKind(ent any) (string, uint16) {
	switch ent.(type) {
	case *entLine:
		return "LINE", forwardEntityCode["LINE"]
	case *entCircle:
		return "CIRCLE", forwardEntityCode["CIRCLE"]
	case *entArc:
		return "ARC", forwardEntityCode["ARC"]
	case *entPoint:
		return "POINT", forwardEntityCode["POINT"]
	case *entEllipse:
		return "ELLIPSE", forwardEntityCode["ELLIPSE"]
	case *entText:
		return "TEXT", forwardEntityCode["TEXT"]
	case *entMText:
		return "MTEXT", forwardEntityCode["MTEXT"]
	case *entLwPolyline:
		return "LWPOLYLINE", forwardEntityCode["LWPOLYLINE"]
	case *entInsert:
		return "INSERT", forwardEntityCode["INSERT"]
	case *entAttrib:
		return "ATTRIB", forwardEntityCode["ATTRIB"]
	case *entSolid:
		return "SOLID", forwardEntityCode["SOLID"]
	case *entFace3d:
		return "3DFACE", forwardEntityCode["3DFACE"]
	case *entVertex2d:
		return "VERTEX_2D", forwardEntityCode["VERTEX_2D"]
	case *entPolyline2d:
		return "POLYLINE_2D", forwardEntityCode["POLYLINE_2D"]
	case *entBlockLike:
		b := entBase(ent)
		switch b.typeName {
		case "BLOCK":
			return "BLOCK", forwardEntityCode["BLOCK"]
		case "ENDBLK":
			return "ENDBLK", forwardEntityCode["ENDBLK"]
		default:
			return "SEQEND", forwardEntityCode["SEQEND"]
		}
	case *entSpline:
		return "SPLINE", forwardEntityCode["SPLINE"]
	case *entHatch:
		return "HATCH", forwardEntityCode["HATCH"]
	case *entRay:
		if ent.(*entRay).xline {
			return "XLINE", forwardEntityCode["XLINE"]
		}
		return "RAY", forwardEntityCode["RAY"]
	case *entLeader:
		return "LEADER", forwardEntityCode["LEADER"]
	case *entMLine:
		return "MLINE", forwardEntityCode["MLINE"]
	case *entTolerance:
		return "TOLERANCE", forwardEntityCode["TOLERANCE"]
	case *entShape:
		return "SHAPE", forwardEntityCode["SHAPE"]
	case *entViewport:
		return "VIEWPORT", forwardEntityCode["VIEWPORT"]
	case *entVertex3d:
		return "VERTEX_3D", forwardEntityCode["VERTEX_3D"]
	case *entVertexPface:
		name := "VERTEX_PFACE"
		if ent.(*entVertexPface).typeName == "VERTEX_MESH" {
			name = "VERTEX_MESH"
		}
		return name, forwardEntityCode[name]
	case *entVertexPfaceFace:
		return "VERTEX_PFACE_FACE", forwardEntityCode["VERTEX_PFACE_FACE"]
	case *entPolyline3d:
		return "POLYLINE_3D", forwardEntityCode["POLYLINE_3D"]
	case *entPolylinePface:
		return "POLYLINE_PFACE", forwardEntityCode["POLYLINE_PFACE"]
	case *entPolylineMesh:
		return "POLYLINE_MESH", forwardEntityCode["POLYLINE_MESH"]
	case *entWipeout:
		// IMAGE/WIPEOUT 为动态类，返回注册名（类型码由调用方查动态分配表）
		if name := fwdDynamicEntityKind(ent); name != "" {
			return name, 0
		}
	case *entMLeader, *entLight:
		return fwdDynamicEntityKind(ent), 0
	case *entDimension:
		d := ent.(*entDimension)
		if fwdDynamicEntityKind(ent) != "" {
			return d.typeName, 0
		}
		name := dimensionKindName(d.dimFlag)
		if name == "" {
			return "", 0
		}
		return name, forwardEntityCode[name]
	}
	return "", 0
}

// dimensionKindName DIMENSION 输出标志低 3 位 → 内部短名（与读侧
// decodeEntityFieldsVer 的类型码分派一致）；7（未知子类）返回空串。
func dimensionKindName(dimFlag uint8) string {
	switch dimFlag & 0x7 {
	case 0:
		return "DIM_LINEAR"
	case 1:
		return "DIM_ALIGNED"
	case 2:
		return "DIM_ANG2LN"
	case 3:
		return "DIM_DIAMETER"
	case 4:
		return "DIM_RADIUS"
	case 5:
		return "DIM_ANG3PT"
	case 6:
		return "DIM_ORDINATE"
	}
	return ""
}

// fwdDimLayout DIMENSION 子类标志 → 读侧类型专属尾部布局（dimSpecificLayout）。
func fwdDimLayout(dimFlag uint8) dimSpecificLayout {
	switch dimFlag & 0x7 {
	case 1:
		return dimLayoutAligned
	case 2:
		return dimLayoutAng2Ln
	case 3:
		return dimLayoutDiameter
	case 4:
		return dimLayoutRadius
	case 5:
		return dimLayoutAng3Pt
	case 6:
		return dimLayoutOrdinate
	default:
		return dimLayoutLinear
	}
}

// fwdHeadOf 取实体公共头（解码/JSON 来源实体携带 head；合成实体回退
// 缺省值：比例 1、线宽 ByLayer 29）。
func fwdHeadOf(b *baseEntity) *commonEntityHead {
	if b.head != nil {
		return b.head
	}
	return &commonEntityHead{handle: b.handle, color: b.color, entityMode: b.mode, ltypeScale: 1, linewt: 29}
}

// fwdExtraF 取 extra 标量（缺省 def）。解码/JSON 来源的 thickness/
// elevation 等扩展字段经 base.extra 传递，编码时对称取回。
func fwdExtraF(b *baseEntity, key string, def float64) float64 {
	if v, ok := b.extra[key].(float64); ok {
		return v
	}
	return def
}

// fwdExtraVec 取 extra 三维向量（缺省 def）。
func fwdExtraVec(b *baseEntity, key string, def point3) point3 {
	if v, ok := b.extra[key].([]float64); ok && len(v) == 3 {
		return point3{v[0], v[1], v[2]}
	}
	return def
}

// ---- 位流编码辅助（读写原语的编码侧对称） ----

// writeBT 位厚度：R2000+ 形式（1 位 mode，1 → 0.0，否则 BD）。
func writeBT(w *encWriter, v float64) {
	if v == 0 {
		w.writeB(true)
		return
	}
	w.writeB(false)
	w.writeBD(v)
}

// writeBE 位挤出方向：(0,0,1) 缺省走 1 位捷径，否则 3BD。
func writeBE(w *encWriter, x, y, z float64) {
	if x == 0 && y == 0 && z == 1 {
		w.writeB(true)
		return
	}
	w.writeB(false)
	w.writeBD(x)
	w.writeBD(y)
	w.writeBD(z)
}

// write3BD 三维点（3×BD 独立编码，无前导位）。
func write3BD(w *encWriter, p point3) {
	w.writeBD(p.x)
	w.writeBD(p.y)
	w.writeBD(p.z)
}

// writeHdlAbs 绝对句柄引用（code 5，counter 按值域最小化）。
func writeHdlAbs(w *encWriter, h uint64) {
	switch {
	case h == 0:
		w.writeH(5, 0, 0)
	case h <= 0xFF:
		w.writeH(5, 1, h)
	case h <= 0xFFFF:
		w.writeH(5, 2, h)
	case h <= 0xFFFFFF:
		w.writeH(5, 3, h)
	default:
		w.writeH(5, 4, h)
	}
}

// writeHdlNull 空句柄引用（xdic 等占位，读侧解析为 0）。
func writeHdlNull(w *encWriter) { w.writeH(5, 0, 0) }

// nearestACI 真彩色 → 最近 ACI 索引（RGB 欧氏距离最小）。R2000 容器的
// 颜色段仅承载索引（真彩 ENC 为 R2004+），跨版本写出时颜色降维。
func nearestACI(rgb uint32) uint16 {
	r, g, b := splitTrueColor(rgb)
	best, bestD := uint16(7), uint32(math.MaxUint32)
	for i := 1; i <= 255; i++ {
		rr, gg, bb, ok := aciColor(uint16(i), false)
		if !ok {
			continue
		}
		d := uint32(sq8(int(rr)-int(r))) + uint32(sq8(int(gg)-int(g))) + uint32(sq8(int(bb)-int(b)))
		if d < bestD {
			bestD, best = d, uint16(i)
		}
	}
	return best
}

// sq8 差的平方（nearestACI 专用）。
func sq8(d int) int { return d * d }

// writeForwardColor 颜色段 ENC 编码（parseEntityColorHead 的逆过程）：
// 11 → ByLayer 256、10 → ByBlock 0、01+RC → 单字节索引、否则 RS 完整值。
// 真彩先降维为最近 ACI；无颜色信息按 ByLayer。
func writeForwardColor(w *encWriter, c entColor) {
	switch {
	case c.hasTrue:
		if idx := nearestACI(c.trueColor); idx != 0 {
			w.writeB(false)
			w.writeB(true)
			w.writeRC(uint8(idx))
			return
		}
		w.writeB(true)
		w.writeB(true)
	case c.hasIndex && c.index == 256:
		w.writeB(true)
		w.writeB(true)
	case c.hasIndex && c.index == 0:
		w.writeB(true)
		w.writeB(false)
	case c.hasIndex && c.index < 256:
		w.writeB(false)
		w.writeB(true)
		w.writeRC(uint8(c.index))
	case c.hasIndex:
		// 256 以上窗口色等非法索引按 ByLayer 归一（RS 回写会被读侧
		// raw&0x1FF 截断为错误值）
		w.writeB(true)
		w.writeB(true)
	default:
		w.writeB(true)
		w.writeB(true)
	}
}

// patchRL 将 RL（小端 4 字节、每字节 MSB-first 位序）回填到缓冲 bitOff
// 起的位区间，供 bitsize 两遍法回填（RL 起点随 BS 类型码宽度非字节对齐）。
// 与 writeRL 的位序逐位对称。
func patchRL(w *encWriter, bitOff uint64, v uint32) {
	for byteIdx := 0; byteIdx < 4; byteIdx++ {
		b := uint8(v >> (8 * byteIdx))
		for i := 0; i < 8; i++ {
			bit := (b >> (7 - i)) & 1
			pos := bitOff + uint64(byteIdx*8+i)
			if pos+1 > uint64(len(w.data))*8 {
				return
			}
			mask := byte(1) << (7 - pos%8)
			if bit != 0 {
				w.data[pos/8] |= mask
			} else {
				w.data[pos/8] &^= mask
			}
		}
	}
}

// ---- 实体 body 正向编码 ----

// encodeForwardEntityBody 将单个实体正向编码为 R2000 记录 body 位流
// （字节对齐）：BS 类型码 + RL bitsize（占位后回填，两遍法）+ 公共头 +
// 专有字段 + handle 流。bitsize 语义与读侧一致：handle 流起点在 body 内
// 的绝对位（含 BS 类型码前导）。dyn 为动态类名 → 已分配类型码映射
// （writeDwgForwardR2000 按文档实际出现的动态类分配）。
func encodeForwardEntityBody(ent any, ver dwgVersion, dyn map[string]uint16) ([]byte, error) {
	b := entBase(ent)
	if b == nil || b.handle == 0 {
		return nil, fmt.Errorf("cad: 正向编码缺少实体句柄")
	}
	name, typeCode := fwdEntityKind(ent)
	if name == "" {
		return nil, fmt.Errorf("cad: 实体类型无正向编码器（%s）", b.typeName)
	}
	if typeCode == 0 {
		// 动态类：类型码取注册分配的动态码（≥500，类段条目对应）
		if dyn == nil {
			return nil, fmt.Errorf("cad: 动态类 %s 无注册码", name)
		}
		code, ok := dyn[name]
		if !ok {
			return nil, fmt.Errorf("cad: 动态类 %s 未注册", name)
		}
		typeCode = code
	}
	head := fwdHeadOf(b)
	w := newEncWriter()
	w.writeBS(typeCode)
	rlOff := w.tellBits()
	w.writeRL(0) // bitsize 占位
	// 公共头（R2000 主布局：objSizeInSub + pictureRL + nolinksBit + linewt RC）
	writeHdlSelf(w, b.handle)
	w.writeBS(0)    // EED 链终止（结构化正向不回放应用数据，见报告边界说明）
	w.writeB(false) // preview_exists
	w.writeBB(b.mode)
	w.writeBL(0)   // num_reactors（正向重建无 reactor 句柄来源，恒 0）
	w.writeB(true) // nolinks：无 prev/next 链接句柄
	writeForwardColor(w, b.color)
	if head.ltypeScale != 0 {
		w.writeBD(head.ltypeScale)
	} else {
		w.writeBD(1)
	}
	w.writeBB(0) // ltype_flags：ByLayer（无句柄）
	w.writeBB(0) // plotstyle_flags
	w.writeBS(uint16(head.invisible))
	if head.linewt != 0 {
		w.writeRC(uint8(head.linewt))
	} else {
		w.writeRC(29) // ByLayer
	}
	// 专有字段
	if enc, ok := forwardEntityEncoders[name]; ok {
		if err := enc(w, ent, ver); err != nil {
			return nil, err
		}
	}
	// bitsize 回填：handle 流起点（body 局部绝对位）
	patchRL(w, rlOff, uint32(w.tellBits()))
	// handle 流：owner（mode 0）→ xdic → layer → 类型专属附加
	if b.mode == 0 {
		writeHdlAbs(w, b.owner)
	}
	writeHdlNull(w)
	writeHdlAbs(w, b.layer)
	if enc, ok := forwardEntityHandleExtra[name]; ok {
		if err := enc(w, ent, ver); err != nil {
			return nil, err
		}
	}
	w.alignByte()
	return w.bytes(), nil
}

// writeHdlSelf 写对象自身句柄（公共头首字段，code 0 当前句柄引用）。
func writeHdlSelf(w *encWriter, h uint64) {
	switch {
	case h <= 0xFF:
		w.writeH(0, 1, h)
	case h <= 0xFFFF:
		w.writeH(0, 2, h)
	case h <= 0xFFFFFF:
		w.writeH(0, 3, h)
	default:
		w.writeH(0, 4, h)
	}
}

// encFwdLine LINE：z 全零位 + x/y 起点直读、终点差分 + [z 对] + 厚度 + 挤出。
func encFwdLine(w *encWriter, ent any, _ dwgVersion) error {
	e := ent.(*entLine)
	zZero := e.start.z == 0 && e.end.z == 0
	w.writeB(zZero)
	w.writeRD(e.start.x)
	w.writeDD(e.end.x, e.start.x)
	w.writeRD(e.start.y)
	w.writeDD(e.end.y, e.start.y)
	if !zZero {
		w.writeRD(e.start.z)
		w.writeDD(e.end.z, e.start.z)
	}
	writeBT(w, fwdExtraF(&e.baseEntity, "thickness", 0))
	ex := fwdExtraVec(&e.baseEntity, "extrusion", point3{0, 0, 1})
	writeBE(w, ex.x, ex.y, ex.z)
	return nil
}

// encFwdCircle CIRCLE：3BD 圆心 + BD 半径 + 厚度 + 挤出。
func encFwdCircle(w *encWriter, ent any, _ dwgVersion) error {
	e := ent.(*entCircle)
	write3BD(w, e.center)
	w.writeBD(e.radius)
	writeBT(w, fwdExtraF(&e.baseEntity, "thickness", 0))
	ex := fwdExtraVec(&e.baseEntity, "extrusion", point3{0, 0, 1})
	writeBE(w, ex.x, ex.y, ex.z)
	return nil
}

// encFwdArc ARC：圆/厚/挤同 CIRCLE，多出起止角（弧度）。
func encFwdArc(w *encWriter, ent any, _ dwgVersion) error {
	e := ent.(*entArc)
	write3BD(w, e.center)
	w.writeBD(e.radius)
	writeBT(w, fwdExtraF(&e.baseEntity, "thickness", 0))
	ex := fwdExtraVec(&e.baseEntity, "extrusion", point3{0, 0, 1})
	writeBE(w, ex.x, ex.y, ex.z)
	w.writeBD(e.angleStart)
	w.writeBD(e.angleEnd)
	return nil
}

// encFwdPoint POINT：3BD 定位 + 厚度 + 挤出 + x 轴角度。
func encFwdPoint(w *encWriter, ent any, _ dwgVersion) error {
	e := ent.(*entPoint)
	write3BD(w, e.location)
	writeBT(w, fwdExtraF(&e.baseEntity, "thickness", 0))
	ex := fwdExtraVec(&e.baseEntity, "extrusion", point3{0, 0, 1})
	writeBE(w, ex.x, ex.y, ex.z)
	w.writeBD(e.rotation)
	return nil
}

// encFwdEllipse ELLIPSE：3BD 圆心 + 3BD 主轴 + 3BD 挤出（主体内）+
// BD 轴比 + 起止角。
func encFwdEllipse(w *encWriter, ent any, _ dwgVersion) error {
	e := ent.(*entEllipse)
	write3BD(w, e.center)
	write3BD(w, e.majorAxis)
	ex := fwdExtraVec(&e.baseEntity, "extrusion", point3{0, 0, 1})
	write3BD(w, ex)
	w.writeBD(e.ratio)
	w.writeBD(e.startAng)
	w.writeBD(e.endAng)
	return nil
}

// textFieldSource TEXT/ATTRIB 共用的文本字段视图（两者 dat 流前段同构）。
type textFieldSource struct {
	base             *baseEntity
	text             string
	insertion        point3
	height, rotation float64
	hAlign, vAlign   uint16
	gen              uint16
	alignPt          *point2
}

// encFwdTextFields TEXT 布局字段段：RC dataflags（0=全字段）+ 高程 +
// 2RD 插入点 + 2DD 对齐点（相对插入点差分）+ 挤出 + 厚度 + 倾角 + 旋转 +
// 字高 + 宽度因子 + 文本串 + 生成/水平/垂直对齐（顺序对照 decodeTextVer）。
func encFwdTextFields(w *encWriter, s textFieldSource, ver dwgVersion) {
	w.writeRC(0) // dataflags：全部字段在场
	w.writeRD(fwdExtraF(s.base, "elevation", s.insertion.z))
	w.writeRD(s.insertion.x)
	w.writeRD(s.insertion.y)
	ax, ay := s.insertion.x, s.insertion.y
	if s.alignPt != nil {
		ax, ay = s.alignPt.x, s.alignPt.y
	}
	w.writeDD(ax, s.insertion.x)
	w.writeDD(ay, s.insertion.y)
	ex := fwdExtraVec(s.base, "extrusion", point3{0, 0, 1})
	writeBE(w, ex.x, ex.y, ex.z)
	writeBT(w, fwdExtraF(s.base, "thickness", 0))
	w.writeRD(fwdExtraF(s.base, "oblique_angle", 0))
	w.writeRD(s.rotation)
	w.writeRD(s.height)
	w.writeRD(fwdExtraF(s.base, "width_factor", 1))
	if ver >= verR2007 {
		w.writeTU(s.text)
	} else {
		w.writeTV(s.text)
	}
	w.writeBS(s.gen)
	w.writeBS(s.hAlign)
	w.writeBS(s.vAlign)
}

// encFwdText TEXT 实体：字段段见 encFwdTextFields。
func encFwdText(w *encWriter, ent any, ver dwgVersion) error {
	e := ent.(*entText)
	encFwdTextFields(w, textFieldSource{
		base: &e.baseEntity, text: e.text, insertion: e.insertion,
		height: e.height, rotation: e.rotation,
		hAlign: e.hAlign, vAlign: e.vAlign, gen: e.gen, alignPt: e.alignPt,
	}, ver)
	return nil
}

// encFwdMText MTEXT：插入点/挤出/轴向量 3BD×3 + 矩形宽/字高 BD +
// 附加/流向 BS + 范围 BD×2 + 文本 + 行距样式/因子 + 未知位（顺序对照
// decodeMTextVer 的 R2000 分支；R2004+ 背景段不写）。
func encFwdMText(w *encWriter, ent any, ver dwgVersion) error {
	e := ent.(*entMText)
	write3BD(w, e.insertion)
	ex := fwdExtraVec(&e.baseEntity, "extrusion", point3{0, 0, 1})
	write3BD(w, ex)
	write3BD(w, e.xAxisDir)
	w.writeBD(e.rectWidth)
	w.writeBD(e.textHeight)
	w.writeBS(e.attachment)
	flow := int64(5) // by style 缺省（解码侧 flow_dir 缺省值）
	if v, ok := e.baseEntity.extra["flow_dir"].(int64); ok {
		flow = v
	}
	w.writeBS(uint16(flow))
	w.writeBD(fwdExtraF(&e.baseEntity, "extents_height", 0))
	w.writeBD(fwdExtraF(&e.baseEntity, "extents_width", 0))
	if ver >= verR2007 {
		w.writeTU(e.text)
	} else {
		w.writeTV(e.text)
	}
	w.writeBS(0) // linespacing style：at least
	w.writeBD(1) // linespacing factor
	w.writeB(false)
	return nil
}

// encFwdLwPolyline LWPOLYLINE：标志驱动的可选段 + 顶点差分数组
// （首点绝对 RD，其余 DD 相对前点）。标志位按内容反推（0x01 挤出 3BD、
// 0x02 厚度、0x04 常量宽、0x08 标高、0x10 凸度、0x20 段宽）。
func encFwdLwPolyline(w *encWriter, ent any, ver dwgVersion) error {
	e := ent.(*entLwPolyline)
	ex := fwdExtraVec(&e.baseEntity, "extrusion", point3{0, 0, 1})
	thickness := fwdExtraF(&e.baseEntity, "thickness", e.thickness)
	var flags uint16
	numBulges := 0
	for i, bl := range e.bulges {
		if bl != 0 {
			numBulges = i + 1
		}
	}
	if ex != (point3{0, 0, 1}) {
		flags |= 0x01
	}
	if thickness != 0 {
		flags |= 0x02
	}
	if e.constWidth != 0 {
		flags |= 0x04
	}
	if e.elevation != 0 {
		flags |= 0x08
	}
	if numBulges > 0 {
		flags |= 0x10
	}
	if len(e.widths) > 0 {
		flags |= 0x20
	}
	w.writeBS(flags)
	if flags&0x04 != 0 {
		w.writeBD(e.constWidth)
	}
	if flags&0x08 != 0 {
		w.writeBD(e.elevation)
	}
	if flags&0x02 != 0 {
		w.writeBD(thickness)
	}
	if flags&0x01 != 0 {
		write3BD(w, ex)
	}
	w.writeBL(uint32(len(e.vertices)))
	if flags&0x10 != 0 {
		w.writeBL(uint32(numBulges))
	}
	for i, v := range e.vertices {
		if i == 0 {
			w.writeRD(v.x)
			w.writeRD(v.y)
			continue
		}
		prev := e.vertices[i-1]
		w.writeDD(v.x, prev.x)
		w.writeDD(v.y, prev.y)
	}
	for i := 0; i < numBulges && i < len(e.bulges); i++ {
		w.writeBD(e.bulges[i])
	}
	for _, width := range e.widths {
		w.writeBD(width.start)
		w.writeBD(width.end)
	}
	return nil
}

// encFwdInsert INSERT：插入点 + BB 缩放标志（差分/全 1）+ 旋转 + 挤出 +
// 属性存在位。块头与属性句柄在 handle 流（encFwdInsertHandles）。
func encFwdInsert(w *encWriter, ent any, _ dwgVersion) error {
	e := ent.(*entInsert)
	write3BD(w, e.position)
	switch {
	case e.scale.x == 1 && e.scale.y == 1 && e.scale.z == 1:
		w.writeBB(0x03)
	case e.scale.x == e.scale.y && e.scale.y == e.scale.z:
		w.writeBB(0x02)
		w.writeRD(e.scale.x)
	default:
		w.writeBB(0x00)
		w.writeRD(e.scale.x)
		w.writeDD(e.scale.y, e.scale.x)
		w.writeDD(e.scale.z, e.scale.x)
	}
	w.writeBD(e.rotation)
	ex := fwdExtraVec(&e.baseEntity, "extrusion", point3{0, 0, 1})
	write3BD(w, ex) // 读侧 INSERT 挤出为 3BD（无 BE 前导位）
	w.writeB(len(e.attribs) > 0)
	return nil
}

// encFwdInsertHandles INSERT handle 流附加：块头句柄 + [R13~R2000 语义的
// 首/末属性句柄与 SEQEND]（对照 decodeInsert 的 handle 流消费顺序；
// 无属性时仅块头）。
func encFwdInsertHandles(w *encWriter, ent any, ver dwgVersion) error {
	e := ent.(*entInsert)
	writeHdlAbs(w, e.blockHeader)
	if len(e.attribs) == 0 {
		return nil
	}
	if ver == verR13 || ver == verR14 || ver == verR2000 {
		writeHdlAbs(w, e.attribs[0])
		writeHdlAbs(w, e.attribs[len(e.attribs)-1])
	} else {
		for _, h := range e.attribs {
			writeHdlAbs(w, h)
		}
	}
	writeHdlAbs(w, e.seqendHandle())
	return nil
}

// seqendHandle INSERT 的 SEQEND 句柄占位：正向重建无独立记录来源，以
// 块内末属性 +1 不成立时回退块头句柄；读侧将该句柄保存至 seqend 字段
// （gold seqend 键对照用），值不参与渲染与文本提取，仅保持 handle 流
// 结构合法。
func (e *entInsert) seqendHandle() uint64 {
	if h := e.attribs[len(e.attribs)-1]; h != 0 {
		return h
	}
	return e.blockHeader
}

// seqendPlaceholder POLYLINE_2D/3D/PFACE 的 SEQEND 占位：正向重建无独立
// 记录来源，优先回显首末顶点句柄（非零时保证引用可解析），否则写 NULL
// 空引用；读侧保存至 seqend 字段（gold seqend 键对照用），值不参与渲染。
func (p *entPolyline2d) seqendPlaceholder() uint64 {
	if p.firstVertex != 0 {
		return p.firstVertex
	}
	return p.lastVertex
}

// seqendPlaceholder POLYLINE_3D 的 SEQEND 占位（语义同 POLYLINE_2D）。
func (p *entPolyline3d) seqendPlaceholder() uint64 {
	if p.firstVertex != 0 {
		return p.firstVertex
	}
	return p.lastVertex
}

// seqendPlaceholder POLYLINE_PFACE 的 SEQEND 占位（语义同 POLYLINE_2D）。
func (p *entPolylinePface) seqendPlaceholder() uint64 {
	if p.firstVertex != 0 {
		return p.firstVertex
	}
	return p.lastVertex
}

// encFwdAttrib ATTRIB：TEXT 同构字段（值文本）+ 标签串 + 字段长度 +
// 标志（对照 decodeAttribVer 的 R2000 分支）。
func encFwdAttrib(w *encWriter, ent any, ver dwgVersion) error {
	e := ent.(*entAttrib)
	encFwdTextFields(w, textFieldSource{
		base: &e.baseEntity, text: e.text, insertion: e.insertion,
		height: e.height, rotation: e.rotation,
		hAlign: e.hAlign, vAlign: e.vAlign, gen: e.gen, alignPt: nil,
	}, ver)
	if ver >= verR2007 {
		w.writeTU(e.tag)
	} else {
		w.writeTV(e.tag)
	}
	w.writeBS(0) // field_length
	w.writeRC(0) // flags
	return nil
}

// encFwdSolid SOLID/TRACE：厚度 + 高程 + 4 角点（2RD×4）+ 挤出。
func encFwdSolid(w *encWriter, ent any, _ dwgVersion) error {
	e := ent.(*entSolid)
	writeBT(w, e.thickness)
	w.writeBD(e.elevation)
	for _, p := range [4]point2{e.p1, e.p2, e.p3, e.p4} {
		w.writeRD(p.x)
		w.writeRD(p.y)
	}
	writeBE(w, e.extrusion.x, e.extrusion.y, e.extrusion.z)
	return nil
}

// encFwdFace3d 3DFACE：无标志位 + z 全零位 + 首点 RD + 3×3DD 差分 +
// [不可见边标志]（对照 decodeFace3d 的 R2000+ 分支）。
func encFwdFace3d(w *encWriter, ent any, _ dwgVersion) error {
	e := ent.(*entFace3d)
	noFlags := e.invisibleEdgeFlags == 0
	zZero := e.p1.z == 0
	w.writeB(noFlags)
	w.writeB(zZero)
	w.writeRD(e.p1.x)
	w.writeRD(e.p1.y)
	if !zZero {
		w.writeRD(e.p1.z)
	}
	prev := e.p1
	for _, p := range [3]point3{e.p2, e.p3, e.p4} {
		w.writeDD(p.x, prev.x)
		w.writeDD(p.y, prev.y)
		w.writeDD(p.z, prev.z)
		prev = p
	}
	if !noFlags {
		w.writeBS(e.invisibleEdgeFlags)
	}
	return nil
}

// encFwdVertex2d VERTEX_2D：RC 标志 + 3BD 位置 + 起末宽（起=末时以负起宽
// 复用）+ 凸度 + 切向（对照 decodeVertex2d）。
func encFwdVertex2d(w *encWriter, ent any, _ dwgVersion) error {
	e := ent.(*entVertex2d)
	w.writeRC(uint8(e.flags))
	write3BD(w, e.position)
	if e.startWidth != 0 && e.startWidth == e.endWidth {
		w.writeBD(-e.startWidth) // 负起宽：读侧复用为末宽（零宽不走捷径，避免位流错位）
	} else {
		w.writeBD(e.startWidth)
		w.writeBD(e.endWidth)
	}
	w.writeBD(e.bulge)
	w.writeBD(e.tangentDir)
	return nil
}

// encFwdPolyline2d POLYLINE_2D：标志 + 曲线类型 + 起末宽 + 厚度 + 高程 +
// 挤出（对照 decodePolyline2d 的 R2000 分支：无 owned 计数）。顶点句柄
// 在 handle 流附加段。
func encFwdPolyline2d(w *encWriter, ent any, _ dwgVersion) error {
	e := ent.(*entPolyline2d)
	w.writeBS(e.flags)
	w.writeBS(e.curveType)
	w.writeBD(e.widthStart)
	w.writeBD(e.widthEnd)
	writeBT(w, e.thickness)
	w.writeBD(e.elevation)
	write3BD(w, e.extrusion)
	return nil
}

// encFwdPolylineHandles POLYLINE_2D handle 流附加：首/末顶点句柄
// （R13~R2000 语义；R2004+ 读侧不消费，写不写均可，保持结构完整）。
// encFwdPolylineHandles POLYLINE_2D handle 流附加：R13~R2000 语义的
// 首/末顶点句柄 + SEQEND 占位（JSON 来源缺省空引用，顶点经 owner 归属
// 聚合还原；读侧保存 seqend 供 gold 对照，值不参与渲染）。
func encFwdPolylineHandles(w *encWriter, ent any, ver dwgVersion) error {
	e := ent.(*entPolyline2d)
	if ver == verR13 || ver == verR14 || ver == verR2000 {
		writeHdlAbs(w, e.firstVertex)
		writeHdlAbs(w, e.lastVertex)
	}
	writeHdlAbs(w, e.seqendPlaceholder())
	return nil
}

// encFwdBlock BLOCK：公共头后仅块名（对照 decodeBlockLike）。
func encFwdBlock(w *encWriter, ent any, ver dwgVersion) error {
	e := ent.(*entBlockLike)
	if ver >= verR2007 {
		w.writeTU(e.name)
	} else {
		w.writeTV(e.name)
	}
	return nil
}

// ---- 极限批次 E：实体编码器续作（对照各实体读侧解码器的 R2000 布局） ----

// encFwdSpline SPLINE：scenario BL + degree BL + 拟合点/控制点双模式数据
// （对照 parseSplineFitData/parseSplineControlData 的 R2000 布局）。
// 控制点模式：rational/closed/periodic 3 位 + 两容差 + 节点/控制点计数 +
// weighted 回显位 + 数组；拟合点模式：拟合容差 + 起末切线（结构化来源
// 无字段，零向量占位）+ 拟合点数组。
func encFwdSpline(w *encWriter, ent any, _ dwgVersion) error {
	e := ent.(*entSpline)
	w.writeBL(e.scenario)
	w.writeBL(e.degree)
	if e.scenario == 2 {
		w.writeBD(e.fitTolerance)
		write3BD(w, point3{}) // beg_tan_vec：JSON/DXF 来源未建模，零占位
		write3BD(w, point3{}) // end_tan_vec
		w.writeBL(uint32(len(e.fitPoints)))
		for _, p := range e.fitPoints {
			write3BD(w, p)
		}
		return nil
	}
	rational := e.rational || len(e.weights) > 0
	w.writeB(rational)
	w.writeB(e.closed)
	w.writeB(e.periodic)
	w.writeBD(e.knotTolerance)
	w.writeBD(e.ctrlTolerance)
	w.writeBL(uint32(len(e.knots)))
	w.writeBL(uint32(len(e.controlPoints)))
	weighted := rational && len(e.weights) >= len(e.controlPoints)
	w.writeB(weighted)
	for _, k := range e.knots {
		w.writeBD(k)
	}
	for i, p := range e.controlPoints {
		write3BD(w, p)
		if weighted {
			w.writeBD(e.weights[i])
		}
	}
	return nil
}

// encFwdHatch HATCH（R2000 无渐变段）：高程/挤出/图案名/填充与关联位 +
// 边界路径数组（边集逐段曲线类型分派 / 多段线顶点带可选凸度）+ 图案样式
// 与定义线段 + 种子点段（对照 decodeHatchBody 的 R2000 分支）。
func encFwdHatch(w *encWriter, ent any, _ dwgVersion) error {
	h := ent.(*entHatch)
	w.writeBD(h.elevation)
	write3BD(w, h.extrusion)
	w.writeTV(h.name)
	w.writeB(h.solidFill)
	w.writeB(h.associative)
	w.writeBL(uint32(len(h.paths)))
	for _, p := range h.paths {
		writeBL(w, p.flag)
		if p.flag&0x02 == 0 {
			w.writeBL(uint32(len(p.segs)))
			for _, seg := range p.segs {
				w.writeRC(seg.curveType)
				switch seg.curveType {
				case 1:
					w.writeRD(seg.first.x)
					w.writeRD(seg.first.y)
					w.writeRD(seg.second.x)
					w.writeRD(seg.second.y)
				case 2:
					w.writeRD(seg.center.x)
					w.writeRD(seg.center.y)
					w.writeBD(seg.radius)
					w.writeBD(seg.startAng)
					w.writeBD(seg.endAng)
					w.writeB(seg.ccw)
				case 3:
					w.writeRD(seg.center.x)
					w.writeRD(seg.center.y)
					w.writeRD(seg.endpoint.x)
					w.writeRD(seg.endpoint.y)
					w.writeBD(seg.ratio)
					w.writeBD(seg.startAng)
					w.writeBD(seg.endAng)
					w.writeB(seg.ccw)
				case 4:
					w.writeBL(seg.degree)
					w.writeB(seg.rational)
					w.writeB(seg.periodic)
					w.writeBL(uint32(len(seg.knots)))
					w.writeBL(uint32(len(seg.ctrl)))
					for _, k := range seg.knots {
						w.writeBD(k)
					}
					for i, c := range seg.ctrl {
						w.writeRD(c.x)
						w.writeRD(c.y)
						if seg.rational {
							var weight float64
							if i < len(seg.weights) {
								weight = seg.weights[i]
							}
							w.writeBD(weight)
						}
					}
				default:
					return fmt.Errorf("cad: HATCH 路径含未知边类型 %d", seg.curveType)
				}
			}
			w.writeBL(0) // 边界对象句柄数：结构化重建无边界句柄来源
		} else {
			bulgesPresent := p.bulgesPresent
			for _, pv := range p.polyVerts {
				if pv.bulge != 0 {
					bulgesPresent = true
				}
			}
			w.writeB(bulgesPresent)
			w.writeB(p.closed)
			w.writeBL(uint32(len(p.polyVerts)))
			for _, pv := range p.polyVerts {
				w.writeRD(pv.p.x)
				w.writeRD(pv.p.y)
				if bulgesPresent {
					w.writeBD(pv.bulge)
				}
			}
			w.writeBL(0) // 边界对象句柄数
		}
	}
	w.writeBS(h.style)
	w.writeBS(h.patternType)
	if !h.solidFill {
		w.writeBD(h.angle)
		w.writeBD(h.scaleSpacing)
		w.writeB(h.doubleFlag)
		w.writeBS(uint16(len(h.deflines)))
		for _, dl := range h.deflines {
			w.writeBD(dl.angle)
			w.writeBD(dl.pt0.x)
			w.writeBD(dl.pt0.y)
			w.writeBD(dl.offset.x)
			w.writeBD(dl.offset.y)
			w.writeBS(uint16(len(dl.dashes)))
			for _, d := range dl.dashes {
				w.writeBD(d)
			}
		}
	}
	if h.hasDerived {
		w.writeBD(h.pixelSize)
	}
	w.writeBL(uint32(len(h.seeds)))
	for _, s := range h.seeds {
		w.writeRD(s.x)
		w.writeRD(s.y)
	}
	return nil
}

// writeBL 路径 flag 的 BL 别名（可读性：hatchPath.flag 为 uint32）。
func writeBL(w *encWriter, v uint32) { w.writeBL(v) }

// encFwdRay RAY/XLINE：3BD 起点 + 3BD 单位方向。
func encFwdRay(w *encWriter, ent any, _ dwgVersion) error {
	e := ent.(*entRay)
	write3BD(w, e.start)
	write3BD(w, e.unitVector)
	return nil
}

// encFwdLeader LEADER：未知位 + 注释/路径类型 + 折点数组 + 原点/挤出/X 向/
// 插入偏移/端点投影 3BD×5 + 文本框宽高 + 钩线与箭头标志 + 箭头类型 +
// 尾部两未知位（对照 decodeLeader 的 R2000 分支，R14 专属段不写）。
func encFwdLeader(w *encWriter, ent any, _ dwgVersion) error {
	l := ent.(*entLeader)
	w.writeB(l.unknownBit1)
	w.writeBS(l.annotationType)
	w.writeBS(l.pathType)
	w.writeBL(uint32(len(l.points)))
	for _, p := range l.points {
		write3BD(w, p)
	}
	write3BD(w, l.origin)
	write3BD(w, l.extrusion)
	write3BD(w, l.xDirection)
	write3BD(w, l.insptOffset)
	write3BD(w, l.endptproj) // R13c3~R2007 段
	w.writeBD(l.boxHeight)
	w.writeBD(l.boxWidth)
	w.writeB(l.hooklineDir)
	w.writeB(l.arrowheadOn)
	w.writeBS(l.arrowheadType)
	w.writeB(l.unknownBit4)
	w.writeB(l.unknownBit5)
	return nil
}

// splitEven 均分数组为 n 组：MLINE 顶点的段/区域参数在模型中是逐线扁平
// 数组，写出时按线数均分（余数并入最后一组），还原读侧逐线独立计数布局。
func splitEven(arr []float64, n int) [][]float64 {
	if n <= 0 {
		return nil
	}
	out := make([][]float64, n)
	chunk := len(arr) / n
	for i := 0; i < n; i++ {
		start := i * chunk
		end := start + chunk
		if i == n-1 {
			end = len(arr)
		}
		if start > len(arr) {
			start = len(arr)
		}
		if end < start {
			end = start
		}
		out[i] = arr[start:end]
	}
	return out
}

// encFwdMLine MLINE：比例/对齐 RC + 基点与挤出 3BD 占位 + 开闭标志 +
// 线数 RC + 顶点数组（位置/方向/miter 3BD×3 + 逐线段/区域参数计数式数组）
// （对照 decodeMline；基点/挤出 JSON 来源未建模，零占位）。
func encFwdMLine(w *encWriter, ent any, _ dwgVersion) error {
	m := ent.(*entMLine)
	w.writeBD(m.scale)
	w.writeRC(m.justification)
	write3BD(w, point3{}) // base_point
	write3BD(w, point3{}) // extrusion
	w.writeBS(m.openClosed)
	w.writeRC(m.linesInStyle)
	w.writeBS(uint16(len(m.vertices)))
	for _, v := range m.vertices {
		write3BD(w, v.position)
		write3BD(w, v.direction)
		write3BD(w, v.miter)
		segLines := splitEven(v.segParams, int(m.linesInStyle))
		areaLines := splitEven(v.areaParams, int(m.linesInStyle))
		for line := 0; line < int(m.linesInStyle); line++ {
			var segs, areas []float64
			if line < len(segLines) {
				segs = segLines[line]
			}
			if line < len(areaLines) {
				areas = areaLines[line]
			}
			w.writeBS(uint16(len(segs)))
			for _, p := range segs {
				w.writeBD(p)
			}
			w.writeBS(uint16(len(areas)))
			for _, p := range areas {
				w.writeBD(p)
			}
		}
	}
	return nil
}

// encFwdMLineHandles MLINE handle 流附加：多线样式记录句柄（code 5，
// DXF 340；读侧从 common 流后按序取首个引用）。
func encFwdMLineHandles(w *encWriter, ent any, _ dwgVersion) error {
	writeHdlCode(w, 5, ent.(*entMLine).styleHandle)
	return nil
}

// encFwdTolerance TOLERANCE（R2000 无 R13/R14 头）：插入点/对称轴/挤出
// 3BD×3 + 标注文本内联 TV（对照 decodeToleranceVer 的非 R13/R14 分支）。
func encFwdTolerance(w *encWriter, ent any, ver dwgVersion) error {
	t := ent.(*entTolerance)
	write3BD(w, t.insertion)
	write3BD(w, t.xDirection)
	write3BD(w, t.extrusion)
	if ver >= verR2007 {
		w.writeTU(t.text)
	} else {
		w.writeTV(t.text)
	}
	return nil
}

// encFwdToleranceHandles TOLERANCE handle 流附加：标注样式句柄。
func encFwdToleranceHandles(w *encWriter, ent any, _ dwgVersion) error {
	writeHdlCode(w, 5, ent.(*entTolerance).dimstyle)
	return nil
}

// encFwdShape SHAPE：插入点/缩放/旋转/宽度因子/倾斜/厚度 + STYLE 表索引
// BS + 挤出（对照 decodeShape 与 dwg.spec SHAPE SINCE(R_13b1)；样式记录
// 句柄以空引用占位）。
func encFwdShape(w *encWriter, ent any, _ dwgVersion) error {
	s := ent.(*entShape)
	write3BD(w, s.insertion)
	w.writeBD(s.scale)
	w.writeBD(s.rotation)
	w.writeBD(s.widthFactor)
	w.writeBD(s.oblique)
	w.writeBD(s.thickness)
	w.writeBS(s.styleId)
	write3BD(w, s.extrusion)
	return nil
}

// encFwdViewport VIEWPORT（R2000 布局）：中心/宽高 + 视图目标/方向 + 视角
// 尺寸/镜头/前后裁剪/捕捉角 + 视图中心/捕捉基点/捕捉间距/网格间距 2RD×4 +
// 圆缩放 BS + 冻结层数/状态 BL + 样式表 TV + 渲染模式 RC + UCS 段
// （对照 decodeViewportVer 的 R2000 分支；R2004+ shadeplot 与 R2007+
// grid_major/灯光段不写）。冻结层句柄数恒 0（无句柄来源）。
func encFwdViewport(w *encWriter, ent any, ver dwgVersion) error {
	vp := ent.(*entViewport)
	write3BD(w, vp.center)
	w.writeBD(vp.width)
	w.writeBD(vp.height)
	write3BD(w, vp.viewTarget)
	write3BD(w, vp.viewDir)
	w.writeBD(vp.viewTwist)
	w.writeBD(vp.viewSize)
	w.writeBD(vp.lensLength)
	w.writeBD(vp.frontZ)
	w.writeBD(vp.backZ)
	w.writeBD(vp.snapAng)
	w.writeRD(vp.viewCtr.x)
	w.writeRD(vp.viewCtr.y)
	w.writeRD(vp.snapBase.x)
	w.writeRD(vp.snapBase.y)
	w.writeRD(vp.snapUnit.x)
	w.writeRD(vp.snapUnit.y)
	w.writeRD(vp.gridUnit.x)
	w.writeRD(vp.gridUnit.y)
	w.writeBS(vp.circleZoom)
	w.writeBL(vp.numFrozenLayers)
	w.writeBL(vp.statusFlag)
	if ver < verR2007 {
		w.writeTV(vp.styleSheet)
	}
	w.writeRC(vp.renderMode)
	w.writeB(vp.ucsAtOrigin)
	w.writeB(vp.ucsVP)
	write3BD(w, vp.ucsorg)
	write3BD(w, vp.ucsxdir)
	write3BD(w, vp.ucsydir)
	w.writeBD(vp.ucsElevation)
	w.writeBS(vp.ucsOrthoView)
	return nil
}

// encFwdVertex3d VERTEX_3D/VERTEX_MESH/VERTEX_PFACE：RC 标志 + 3BD 位置
// （三顶点变体同布局，对照 decodeVertex3d/decodeVertexPface）。
func encFwdVertex3d(w *encWriter, ent any, _ dwgVersion) error {
	switch v := ent.(type) {
	case *entVertex3d:
		w.writeRC(v.flags)
		write3BD(w, v.position)
	case *entVertexPface:
		w.writeRC(v.flag)
		write3BD(w, v.position)
	}
	return nil
}

// encFwdVertexPfaceFace VERTEX_PFACE_FACE：4×BS 顶点索引（1 基，0 表边
// 结束；负值按 BSd 有符号语义回绕，对照 decodeVertexPfaceFace）。
func encFwdVertexPfaceFace(w *encWriter, ent any, _ dwgVersion) error {
	f := ent.(*entVertexPfaceFace)
	for i := 0; i < 4; i++ {
		w.writeBS(uint16(int16(f.vertind[i])))
	}
	return nil
}

// encFwdPolyline3d POLYLINE_3D：曲线类型/标志 RC×2（R2000 无 owned 计数，
// 顶点句柄在 handle 流附加段，对照 decodePolyline3d 的 R2000 分支）。
func encFwdPolyline3d(w *encWriter, ent any, _ dwgVersion) error {
	p := ent.(*entPolyline3d)
	w.writeRC(p.flags75)
	w.writeRC(p.flags70)
	return nil
}

// encFwdPolyline3dHandles POLYLINE_3D handle 流附加：R13~R2000 语义的
// 首/末顶点句柄 + SEQEND 占位（JSON 来源缺省空引用，顶点经 owner 归属
// 聚合还原；读侧保存 seqend 供 gold 对照，值不参与渲染）。
func encFwdPolyline3dHandles(w *encWriter, ent any, ver dwgVersion) error {
	p := ent.(*entPolyline3d)
	if ver == verR13 || ver == verR14 || ver == verR2000 {
		writeHdlAbs(w, p.firstVertex)
		writeHdlAbs(w, p.lastVertex)
	}
	writeHdlAbs(w, p.seqendPlaceholder())
	return nil
}

// encFwdPolylinePfaceHandles POLYLINE_PFACE handle 流附加：R13~R2000 语义
// 的首/末顶点句柄 + SEQEND 占位（与 POLYLINE_2D 同构，对照
// decodePolylinePface 的 R13~R2000 分支；R2004+ 语义的 owned 向量正向
// 编码器不产生——writeDwgForwardR2000 恒为 R2000 布局）。
func encFwdPolylinePfaceHandles(w *encWriter, ent any, ver dwgVersion) error {
	p := ent.(*entPolylinePface)
	if ver == verR13 || ver == verR14 || ver == verR2000 {
		writeHdlAbs(w, p.firstVertex)
		writeHdlAbs(w, p.lastVertex)
	}
	writeHdlAbs(w, p.seqendPlaceholder())
	return nil
}

// encFwdPolylinePface POLYLINE_PFACE：BS 顶点数 + BS 面数
// （对照 decodePolylinePface；顶点/面记录由 owner 归属聚合）。
func encFwdPolylinePface(w *encWriter, ent any, _ dwgVersion) error {
	p := ent.(*entPolylinePface)
	w.writeBS(uint16(p.numVertices))
	w.writeBS(uint16(p.numFaces))
	return nil
}

// encFwdPolylineMesh POLYLINE_MESH：6×BS 网格参数（R2000 无 owned 计数，
// 对照 decodePolylineMesh 的 R2000 分支）。
func encFwdPolylineMesh(w *encWriter, ent any, _ dwgVersion) error {
	m := ent.(*entPolylineMesh)
	w.writeBS(m.flags)
	w.writeBS(m.curveType)
	w.writeBS(m.mVertexCount)
	w.writeBS(m.nVertexCount)
	w.writeBS(m.mDensity)
	w.writeBS(m.nDensity)
	return nil
}

// encFwdDimension DIMENSION 公共段 + 类型专属尾部（逆 decodeDimCanonical
// 的 R2000 布局：extrusion 3BD → text_midpt 2RD → elevation BD → flag1 RC
// → user_text TV → 文本/水平角 BD → ins_scale 3BD → ins_rotation BD →
// attachment/lspace/measurement → clone_ins_pt 2RD → 专属尾部）。
// R2007+ 字符串流与 R2010+ 类版本段不写。
func encFwdDimension(w *encWriter, ent any, ver dwgVersion) error {
	d := ent.(*entDimension)
	write3BD(w, d.extrusion)
	w.writeRD(d.textMidpoint.x)
	w.writeRD(d.textMidpoint.y)
	w.writeBD(d.elevation)
	w.writeRC(d.dimFlags)
	if ver >= verR2007 {
		w.writeTU(d.userText)
	} else {
		w.writeTV(d.userText)
	}
	w.writeBD(d.textRotation)
	w.writeBD(d.horizontalDir)
	write3BD(w, d.insertScale)
	w.writeBD(d.insertRotation)
	w.writeBS(d.attachmentPoint)
	w.writeBS(d.lineSpacingStyle)
	w.writeBD(d.lineSpacingFactor)
	w.writeBD(d.actualMeasurement)
	// clone_ins_pt：无来源时以 10 组码 def 点占位（读侧无缺省判定位）
	writeRD(w, d.insertPoint.x)
	writeRD(w, d.insertPoint.y)
	writeDimSpecific(w, fwdDimensionLayout(d), d)
	return nil
}

// fwdDimensionLayout DIMENSION 实体的专属尾部布局：弧长/大半径家族按
// 实体名分派（动态类，dimFlag 低 3 位与 ANG3PT 重合），其余按输出标志。
func fwdDimensionLayout(d *entDimension) dimSpecificLayout {
	switch d.typeName {
	case "ARC_DIMENSION":
		return dimLayoutArc
	case "LARGE_RADIAL_DIMENSION":
		return dimLayoutLargeRadial
	}
	return fwdDimLayout(d.dimFlag)
}

// writeRD 2RD 字段别名（insertPoint 的 clone_ins_pt 2RD 编码）。
func writeRD(w *encWriter, v float64) { w.writeRD(v) }

// writeDimSpecific DIMENSION 类型专属尾部（readDimSpecific 的逆过程，
// 布局逐分支对称；弧长 LARGE_RADIAL 无独立正向类不涉及）。
func writeDimSpecific(w *encWriter, layout dimSpecificLayout, d *entDimension) {
	write3 := func(p point3) { write3BD(w, p) }
	switch layout {
	case dimLayoutLinear:
		write3(d.point13)
		write3(d.point14)
		write3(d.point10)
		w.writeBD(d.extLineRotation)
		w.writeBD(d.dimRotation)
	case dimLayoutAligned:
		write3(d.point13)
		write3(d.point14)
		write3(d.point10)
		w.writeBD(d.extLineRotation)
	case dimLayoutAng3Pt:
		write3(d.point10)
		write3(d.point13)
		write3(d.point14)
		write3(d.point15)
	case dimLayoutAng2Ln:
		w.writeRD(d.point16x)
		w.writeRD(d.p16y)
		write3(d.point13)
		write3(d.point14)
		write3(d.point15)
		write3(d.point10)
	case dimLayoutOrdinate:
		write3(d.point10)
		write3(d.point13)
		write3(d.point14)
		w.writeRC(d.flag2)
	case dimLayoutRadius:
		write3(d.point10)
		write3(d.point15)
		w.writeBD(d.leaderLen)
	case dimLayoutDiameter:
		write3(d.point15)
		write3(d.point10)
		w.writeBD(d.leaderLen)
	case dimLayoutArc:
		write3(d.defPt)
		write3(d.point13)
		write3(d.point14)
		write3(d.point15)
		w.writeB(d.isPartial)
		w.writeBD(d.arcStartParam)
		w.writeBD(d.arcEndParam)
		w.writeB(d.hasLeader)
		write3(d.leader1Pt)
		write3(d.leader2Pt)
	case dimLayoutLargeRadial:
		write3(d.point13)
		write3(d.point14)
		w.writeBD(d.extLineRotation)
		write3(d.point15)
		write3(d.point10)
	}
}

// encFwdDimensionHandles DIMENSION handle 流附加：标注样式 + 匿名块
// （顺序对照 decodeDimHandles）。
func encFwdDimensionHandles(w *encWriter, ent any, _ dwgVersion) error {
	d := ent.(*entDimension)
	writeHdlCode(w, 5, d.dimstyleHandle)
	writeHdlCode(w, 2, d.anonymousBlock)
	return nil
}

// ---- 极限批次 E：动态类实体（类段注册 + ≥500 动态类型码） ----

// encFwdImage IMAGE/WIPEOUT（AcDbRasterImage 同布局）：类版本 + 图像三轴
// 3BD + 尺寸 2RD + 显示属性 + 裁剪位与亮度组 + 裁剪边界（类型 1 固定两角，
// 其余 BL 计数式，对照 decodeImageVer 的 R2000 分支）。imagedef 系句柄在
// handle 流附加段。
func encFwdImage(w *encWriter, ent any, ver dwgVersion) error {
	img := ent.(*entWipeout)
	w.writeBL(img.classVersion)
	write3BD(w, img.pt0)
	write3BD(w, img.uvec)
	write3BD(w, img.vvec)
	w.writeRD(img.imageSize.x)
	w.writeRD(img.imageSize.y)
	w.writeBS(img.displayProps)
	w.writeB(img.clipping)
	w.writeRC(img.brightness)
	w.writeRC(img.contrast)
	w.writeRC(img.fade)
	if ver >= verR2010 {
		w.writeB(img.clipMode != 0)
	}
	w.writeBS(img.clipBoundaryType)
	verts := img.clipVerts
	if img.clipBoundaryType == 1 {
		// 矩形边界固定两角：读侧不读计数
		if len(verts) > 2 {
			verts = verts[:2]
		}
		for len(verts) < 2 {
			verts = append(verts, point2{})
		}
		for _, p := range verts {
			w.writeRD(p.x)
			w.writeRD(p.y)
		}
		return nil
	}
	w.writeBL(uint32(len(verts)))
	for _, p := range verts {
		w.writeRD(p.x)
		w.writeRD(p.y)
	}
	return nil
}

// encFwdImageHandles IMAGE handle 流附加：图像定义（code 5）+ 定义反应器
// （code 3），顺序对照 decodeImageVer；无来源时写空引用保持流结构。
func encFwdImageHandles(w *encWriter, ent any, _ dwgVersion) error {
	img := ent.(*entWipeout)
	writeHdlCode(w, 5, 0)
	writeHdlCode(w, 3, 0)
	_ = img
	return nil
}

// encFwdMLeader MULTILEADER（缺省上下文形态）：结构化来源不携带引线
// 上下文（leaders 空、无文字/块内容），写出空引线数组 + 上下文标量段 +
// 主体尾段（对照 decodeMLeader 的 R2000 分支：R2010+ 类版本段与
// R2007+ 字符串流不写；颜色 CMC 为 R2004 前的 BS 索引形态）。
func encFwdMLeader(w *encWriter, ent any, _ dwgVersion) error {
	m := ent.(*entMLeader)
	// 上下文：空引线 + 全缺省标量（读侧 decodeMLeaderLeaders/Context 逐字段消费）
	w.writeBL(0)          // num_leaders
	w.writeBD(0)          // ctx.scale
	write3BD(w, point3{}) // content_base
	w.writeBD(0)          // ctx.text_height
	w.writeBD(0)          // ctx.arrow_size
	w.writeBD(0)          // landing_gap
	w.writeBS(0)          // text_left
	w.writeBS(0)          // text_right
	w.writeBS(0)          // text_angletype
	w.writeBS(0)          // text_alignment
	w.writeB(false)       // has_content_txt
	w.writeB(false)       // has_content_blk
	write3BD(w, point3{}) // base
	write3BD(w, point3{}) // base_dir
	write3BD(w, point3{}) // base_vert
	w.writeB(false)       // is_normal_reversed：JSON 来源未建模
	// 主体尾段
	w.writeBL(m.flags)
	w.writeBS(m.mleaderType)
	writeMLeaderCMCIndex(w)
	w.writeBL(uint32(m.lineLinewt))
	w.writeB(m.hasLanding)
	w.writeB(m.hasDogleg)
	w.writeBD(m.landingDist)
	w.writeBD(m.arrowSize)
	w.writeBS(m.styleContent)
	w.writeBS(m.textLeft)
	w.writeBS(m.textRight)
	w.writeBS(m.textAngletype)
	w.writeBS(m.textAlignment)
	writeMLeaderCMCIndex(w) // text_color
	w.writeB(m.hasTextFrame)
	writeMLeaderCMCIndex(w) // block_color
	write3BD(w, point3{})   // block_scale
	w.writeBD(m.blockRotation)
	w.writeBS(m.styleAttachment)
	w.writeB(m.isAnnotative)
	// VERSIONS(R_14, R_2007) 段（R2000 在内）
	w.writeBL(0) // num_arrowheads
	w.writeBL(0) // num_blocklabels
	w.writeB(m.isNegTextdir)
	w.writeBS(m.ipeAlignment)
	w.writeBS(m.justification)
	w.writeBD(m.scaleFactor)
	return nil
}

// writeMLeaderCMCIndex 空颜色 CMC（R2004 前形态：BS 索引 0，ByLayer）。
func writeMLeaderCMCIndex(w *encWriter) { w.writeBS(0) }

// encFwdMLeaderHandles MULTILEADER handle 流附加：多线样式/箭头/文字样式/
// 块样式/线型五引用（顺序对照 decodeMLeader 尾部；空引线上下文时
// leaders/content 系列无句柄）。
func encFwdMLeaderHandles(w *encWriter, ent any, _ dwgVersion) error {
	m := ent.(*entMLeader)
	writeHdlCode(w, 5, m.mleaderStyle)
	writeHdlCode(w, 5, m.arrowHandle)
	writeHdlCode(w, 5, m.textStyle)
	writeHdlCode(w, 5, m.blockStyle)
	writeHdlCode(w, 5, m.lineLtype)
	return nil
}

// encFwdLight LIGHT（R2000 基线布局，光度子段不写）：类版本 + 名称 +
// 类型/状态/颜色索引 + 强度与位置/目标 + 衰减段 + 阴影段
// （对照 decodeLight 的非 CMC 分支）。
func encFwdLight(w *encWriter, ent any, ver dwgVersion) error {
	l := ent.(*entLight)
	w.writeBL(l.classVersion)
	if ver >= verR2007 {
		w.writeTU(l.name)
	} else {
		w.writeTV(l.name)
	}
	w.writeBL(l.lightType)
	w.writeB(l.status)
	if ver >= verR2004 {
		// CMC 结构（BS 索引 + BL rgb + RC flag）：结构化来源仅索引
		w.writeBS(l.lightColorIndex)
		w.writeBL(l.lightColorRGB)
		w.writeRC(l.lightColorFlag)
	} else {
		w.writeBS(l.lightColorIndex)
	}
	w.writeB(l.plotGlyph)
	w.writeBD(l.intensity)
	write3BD(w, l.position)
	write3BD(w, l.target)
	w.writeBL(l.attenuationType)
	w.writeB(l.useAttenuationLimits)
	w.writeBD(l.attenuationStart)
	w.writeBD(l.attenuationEnd)
	w.writeBD(l.hotspotAngle)
	w.writeBD(l.falloffAngle)
	w.writeB(l.castShadows)
	w.writeBL(l.shadowType)
	w.writeBS(l.shadowMapSize)
	w.writeRC(uint8(l.shadowMapSoftness))
	return nil
}

// ---- 对象侧正向编码（最小集） ----

// encodeForwardLayerBody LAYER 表记录（对照 parseLayerHeaderPreR2004 的
// R2000 dat 流 + LibreDWG COMMON_TABLE_FLAGS 的 handle 流）：RL bitsize +
// H + EED + BL reactors + TV 名称 + xref 标志组 + flag0 位包 + BS 颜色
// 索引；handle 流 = owner → xdic → xref → plotstyle → ltype（后三者为
// 表记录公共句柄，结构化重建以空引用占位）。
func encodeForwardLayerBody(handle uint64, lc layerColor, owner uint64) ([]byte, error) {
	idx := lc.index
	if lc.hasTrue {
		idx = nearestACI(lc.trueColor)
	}
	name := lc.name
	if name == "" {
		name = fmt.Sprintf("LAYER_%X", handle)
	}
	w := newEncWriter()
	w.writeBS(uint16(0x33))
	rlOff := w.tellBits()
	w.writeRL(0)
	writeHdlSelf(w, handle)
	w.writeBS(0) // EED 终止
	w.writeBL(0) // num_reactors
	w.writeTV(name)
	w.writeB(false) // is_xref_ref
	w.writeBS(1)    // is_xref_resolved
	w.writeB(false) // is_xref_dep
	w.writeBS(0)    // flag0 位包（frozen/off/locked 等合成缺省）
	w.writeBS(idx)  // CMC（R2004 前形态：BS 索引）
	patchRL(w, rlOff, uint32(w.tellBits()))
	writeHdlCode(w, 4, owner) // ownerhandle
	writeHdlNull(w)           // xdicobjhandle
	writeHdlNull(w)           // xref
	writeHdlNull(w)           // plotstyle
	writeHdlNull(w)           // ltype
	w.alignByte()
	return w.bytes(), nil
}

// writeHdlCode 指定 code 的句柄引用（表记录 owner=4、entries=2 等语义码）。
func writeHdlCode(w *encWriter, code uint8, h uint64) {
	if h == 0 {
		w.writeH(code, 0, 0)
		return
	}
	var counter uint8
	switch {
	case h <= 0xFF:
		counter = 1
	case h <= 0xFFFF:
		counter = 2
	case h <= 0xFFFFFF:
		counter = 3
	default:
		counter = 4
	}
	w.writeH(code, counter, h)
}

// forwardTableControl 表控制对象骨架描述：类型码、句柄与 entries。
type forwardTableControl struct {
	typeCode uint16
	handle   uint64
	entries  []uint64
}

// encodeForwardControlBody 表控制对象（BLOCK_CONTROL/LAYER_CONTROL 等，
// 对照 LibreDWG CONTROL_HANDLE_STREAM + HANDLE_VECTOR）：dat 流 =
// BL num_entries（LTYPE_CONTROL 为 BS）+ [DIMSTYLE_CONTROL 的 RC
// num_morehandles]，handle 流 = owner → xdic → entries → 对象专属句柄
// （块表 model/paper_space、线型表 byblock/bylayer）。
func encodeForwardControlBody(tc forwardTableControl, modelSpace, paperSpace uint64) ([]byte, error) {
	w := newEncWriter()
	w.writeBS(tc.typeCode)
	rlOff := w.tellBits()
	w.writeRL(0)
	writeHdlSelf(w, tc.handle)
	w.writeBS(0) // EED 终止
	w.writeBL(0) // num_reactors（对象公共头字段）
	switch tc.typeCode {
	case 0x38: // LTYPE_CONTROL：num_entries 为 BS
		w.writeBS(uint16(len(tc.entries)))
	case 0x44: // DIMSTYLE_CONTROL：尾部 RC num_morehandles
		w.writeBL(uint32(len(tc.entries)))
		w.writeRC(0)
	default:
		w.writeBL(uint32(len(tc.entries)))
	}
	patchRL(w, rlOff, uint32(w.tellBits()))
	writeHdlCode(w, 4, 0) // ownerhandle（表控制为根对象，owner 空）
	writeHdlNull(w)       // xdicobjhandle
	for _, e := range tc.entries {
		writeHdlCode(w, 2, e)
	}
	switch tc.typeCode {
	case 0x30: // BLOCK_CONTROL 专属：模型/图纸空间块头
		writeHdlCode(w, 3, modelSpace)
		writeHdlCode(w, 3, paperSpace)
	case 0x38: // LTYPE_CONTROL 专属：ByBlock/ByLayer 线型
		writeHdlNull(w)
		writeHdlNull(w)
	}
	w.alignByte()
	return w.bytes(), nil
}

// forwardBlockHeader 块头骨架描述：句柄、名称、基点与首末实体句柄。
type forwardBlockHeader struct {
	handle      uint64
	name        string
	basePt      point3
	firstEntity uint64
	lastEntity  uint64
}

// encodeForwardBlockHeaderBody BLOCK_HEADER 表记录（对照 LibreDWG
// COMMON_TABLE_FLAGS(Block) + BLOCK_HEADER SINCE(R_2000b) 的 R2000 布局）：
// dat 流 = 名称 + xref 标志组 + 4 标志位 + 基点 3BD + xref 名称 +
// RL num_inserts + 描述 + BL preview_size(0)；handle 流 = owner → xdic →
// xref → block_entity → first/last_entity → endblk_entity。
// blockEnt/endblkEnt 为块内配对 BLOCK/ENDBLK 标记实体的句柄（缺失时 0）。
func encodeForwardBlockHeaderBody(bh forwardBlockHeader, blockEnt, endblkEnt uint64) ([]byte, error) {
	w := newEncWriter()
	w.writeBS(uint16(0x31))
	rlOff := w.tellBits()
	w.writeRL(0)
	writeHdlSelf(w, bh.handle)
	w.writeBS(0) // EED 终止
	w.writeBL(0) // num_reactors
	w.writeTV(bh.name)
	w.writeB(false) // is_xref_ref
	w.writeBS(1)    // is_xref_resolved
	w.writeB(false) // is_xref_dep
	w.writeB(false) // anonymous
	w.writeB(false) // hasattrs
	w.writeB(false) // blkisxref
	w.writeB(false) // xrefoverlaid
	w.writeB(false) // xref_loaded（R2000b 位）
	write3BD(w, bh.basePt)
	w.writeTV("") // xref_pname
	w.writeRC(0)  // num_inserts：RC 计数终止式（0 = 空）
	w.writeTV("") // description
	w.writeBL(0)  // preview_size（无预览位串）
	patchRL(w, rlOff, uint32(w.tellBits()))
	writeHdlCode(w, 4, 0) // ownerhandle
	writeHdlNull(w)       // xdicobjhandle
	writeHdlNull(w)       // xref
	writeHdlCode(w, 3, blockEnt)
	writeHdlCode(w, 4, bh.firstEntity)
	writeHdlCode(w, 4, bh.lastEntity)
	writeHdlCode(w, 3, endblkEnt)
	w.alignByte()
	return w.bytes(), nil
}

// encodeForwardDictionaryBody DICTIONARY 对象（对照 decodeDictionaryObject
// 的 R2000 dat 流）：reactors 原值保留但句柄以空引用占位（解码侧不保留
// reactor 句柄列表，结构化重建以数量守恒为准）。
func encodeForwardDictionaryBody(d *objDictionary, ver dwgVersion) ([]byte, error) {
	w := newEncWriter()
	w.writeBS(uint16(0x2A))
	rlOff := w.tellBits()
	w.writeRL(0)
	writeHdlSelf(w, d.handle)
	if len(d.EedFields) != 0 {
		if err := encodeEEDFields(w, d.EedFields); err != nil {
			return nil, err
		}
	} else {
		w.writeBS(0)
	}
	w.writeBL(uint32(d.numReactors))
	w.writeBL(uint32(d.numItems))
	w.writeBS(d.cloning)
	if d.isHardOwner {
		w.writeRC(1)
	} else {
		w.writeRC(0)
	}
	for _, s := range d.texts {
		if ver >= verR2007 {
			w.writeTU(s)
		} else {
			w.writeTV(s)
		}
	}
	patchRL(w, rlOff, uint32(w.tellBits()))
	writeHdlAbs(w, d.owner)
	for i := 0; i < d.numReactors; i++ {
		writeHdlNull(w)
	}
	writeHdlNull(w)
	for _, h := range d.itemHandles {
		writeHdlAbs(w, h)
	}
	w.alignByte()
	return w.bytes(), nil
}

// ---- 文件级组装 ----

// writeDwgForwardR2000 将无回放素材的 Document 组装为 R2000 容器 DWG
// 字节流。对象图按句柄升序铺放（差分编码要求单调），布局为：头部 0x15
// → 段目录（3 条目 + CRC + 哨兵）→ HeaderVars 段（模板回放）→ Classes
// 段（空类表：固定码实体无动态类）→ 对象区 → 对象图。
func writeDwgForwardR2000(doc *Document) ([]byte, error) {
	if doc == nil {
		return nil, fmt.Errorf("cad: 文档为空，无法正向写出")
	}
	dynClasses, dynCodes := allocateForwardDynamicClasses(doc)
	objs, err := collectForwardObjects(doc, dynCodes)
	if err != nil {
		return nil, err
	}
	// 实体级空文档保持「无可写内容」错误契约（骨架对象不计入；空 DWG
	// 的写出能力以文档实际携带实体为准）
	hasEntities := len(doc.modelSpace) > 0 || len(doc.blocks) > 0 || len(doc.attribs) > 0 || len(doc.entityByHandle) > 0
	if !hasEntities && len(doc.layerColors) == 0 {
		return nil, fmt.Errorf("cad: 文档无实体，正向写出无意义")
	}
	if len(objs) == 0 {
		return nil, fmt.Errorf("cad: 文档无可写出的对象（实体缺句柄）")
	}
	sort.Slice(objs, func(i, j int) bool { return objs[i].handle < objs[j].handle })
	hvSec, err := base64.StdEncoding.DecodeString(forwardHeaderVarsB64)
	if err != nil || len(hvSec) < 48 {
		return nil, fmt.Errorf("cad: HeaderVars 模板非法: %w", err)
	}
	clsEntries := make([]forwardClassInfo, 0, len(dynClasses))
	for _, name := range dynClasses {
		cpp := ""
		isEntity := true
		if code := gfFixedObjectCode[name]; code == 0 {
			for _, objName := range forwardObjectDynamicClasses {
				if objName == name {
					isEntity = false
				}
			}
		}
		for _, reg := range forwardDynamicClassOrder {
			if reg.dxf == name {
				cpp = reg.cpp
			}
		}
		clsEntries = append(clsEntries, forwardClassInfo{
			classNumber: dynCodes[name], dxfName: name, cppName: cpp, isEntity: isEntity,
		})
	}
	clsSec := buildForwardClassesSection(clsEntries)
	// 布局定址（目录条目数固定 3：HeaderVars/Classes/ObjectMap）
	dirEnd := uint64(0x15 + 4 + 3*9 + 2 + len(r2000LocatorSentinel))
	hvOff := dirEnd
	clsOff := hvOff + uint64(len(hvSec))
	objBase := clsOff + uint64(len(clsSec))
	blob := make([]byte, 0, len(objs)*24)
	refs := make([]objectRef, 0, len(objs))
	for _, o := range objs {
		rec, err := buildForwardObjectRecord(o)
		if err != nil {
			return nil, fmt.Errorf("cad: 对象 %d 记录组装失败: %w", o.handle, err)
		}
		refs = append(refs, objectRef{handle: o.handle, offset: uint32(objBase + uint64(len(blob)))})
		blob = append(blob, rec...)
	}
	mapPayload := buildR2000ObjectMap(refs, 0)
	mapOff := objBase + uint64(len(blob))
	if mapOff+uint64(len(mapPayload)) > 0xFFFFFFFF {
		return nil, fmt.Errorf("cad: 正向写出生成体积超过 4GB 布局上限")
	}
	// 文件头 + 段目录
	w := newEncWriter()
	hdr := forwardR2000FixedHeader
	cp := uint16(30)
	if doc.codepage != 0 {
		cp = doc.codepage
	}
	hdr[0x13] = uint8(cp)
	hdr[0x14] = uint8(cp >> 8)
	w.writeTF(hdr[:])
	w.writeRL(3)
	w.writeRC(r2000SecHeaderVars)
	w.writeRL(uint32(hvOff))
	w.writeRL(uint32(len(hvSec)))
	w.writeRC(r2000SecClasses)
	w.writeRL(uint32(clsOff))
	w.writeRL(uint32(len(clsSec)))
	w.writeRC(r2000SecObjectMap)
	w.writeRL(uint32(mapOff))
	w.writeRL(uint32(len(mapPayload)))
	w.writeCRCSeed(0, 0xC0C1)
	w.writeTF(r2000LocatorSentinel[:])
	// 段数据：HeaderVars 模板 → Classes → 对象区 → 对象图
	w.writeTF(hvSec)
	w.writeTF(clsSec)
	w.writeTF(blob)
	w.writeTF(mapPayload)
	return w.bytes(), nil
}

// allocateForwardDynamicClasses 扫描文档实体与通用对象，为出现的动态
// 类按注册表顺序分配 ≥500 的类型码（实体类在前、对象类在后，同一计数
// 器连续分配）。返回有序类名与 名称→码 映射。
func allocateForwardDynamicClasses(doc *Document) ([]string, map[string]uint16) {
	present := map[string]bool{}
	visit := func(list []any) {
		for _, ent := range list {
			if name := fwdDynamicEntityKind(ent); name != "" {
				present[name] = true
			}
		}
	}
	visit(doc.modelSpace)
	visit(doc.pspaceSpace)
	for _, list := range doc.blocks {
		visit(list)
	}
	for _, a := range doc.attribs {
		if a != nil {
			if name := fwdDynamicEntityKind(a); name != "" {
				present[name] = true
			}
		}
	}
	if doc.entityByHandle != nil {
		for _, ent := range doc.entityByHandle {
			if name := fwdDynamicEntityKind(ent); name != "" {
				present[name] = true
			}
		}
	}
	for h, g := range doc.internalObjects {
		if g == nil || h == 0 {
			continue
		}
		if _, ok := gfWriters[g.Name]; ok && gfFixedObjectCode[g.Name] == 0 {
			for _, name := range forwardObjectDynamicClasses {
				if name == g.Name {
					present[name] = true
				}
			}
		}
	}
	var order []string
	codes := map[string]uint16{}
	next := uint16(500)
	for _, reg := range forwardDynamicClassOrder {
		if !present[reg.dxf] {
			continue
		}
		codes[reg.dxf] = next
		order = append(order, reg.dxf)
		next++
	}
	for _, name := range forwardObjectDynamicClasses {
		if !present[name] {
			continue
		}
		codes[name] = next
		order = append(order, name)
		next++
	}
	return order, codes
}

// collectForwardObjects 从 Document 收集全部可写对象：实体（模型空间 +
// 块定义 + 属性）经正向实体编码器，LAYER 表记录与 DICTIONARY 经对象侧
// 编码器。无正向编码器或无句柄的实体跳过（能力边界，见报告）。
// dyn 为动态类名 → 类型码映射（无动态类实体时可为 nil）。
func collectForwardObjects(doc *Document, dyn map[string]uint16) ([]fwdObject, error) {
	out := make([]fwdObject, 0, len(doc.entityByHandle)+len(doc.layerColors)+8)
	seen := map[uint64]bool{}
	add := func(handle uint64, body []byte) {
		if handle == 0 || seen[handle] {
			return
		}
		seen[handle] = true
		out = append(out, fwdObject{handle: handle, body: body})
	}
	entities := make([]any, 0, len(doc.entityByHandle)+8)
	entities = append(entities, doc.modelSpace...)
	entities = append(entities, doc.pspaceSpace...)
	for _, list := range doc.blocks {
		entities = append(entities, list...)
	}
	for _, a := range doc.attribs {
		entities = append(entities, a)
	}
	if doc.entityByHandle != nil {
		for _, ent := range doc.entityByHandle {
			entities = append(entities, ent)
		}
	}
	for _, ent := range entities {
		b := entBase(ent)
		if b == nil || b.handle == 0 {
			continue
		}
		if seen[b.handle] {
			continue
		}
		kind, code := fwdEntityKind(ent)
		if code == 0 && kind == "" {
			continue // 无编码器：能力边界外实体
		}
		body, err := encodeForwardEntityBody(ent, verR2000, dyn)
		if err != nil {
			return nil, fmt.Errorf("cad: 实体 %s(%d) 编码失败: %w", b.typeName, b.handle, err)
		}
		add(b.handle, body)
	}
	// LAYER 表记录（渲染必需：颜色与名称）
	for h, lc := range doc.layerColors {
		if seen[h] {
			continue
		}
		body, err := encodeForwardLayerBody(h, lc, 0)
		if err != nil {
			return nil, fmt.Errorf("cad: LAYER %d 编码失败: %w", h, err)
		}
		add(h, body)
	}
	// 通用对象（Fields 驱动 gfWrite）：JSON 来源的 XRECORD/LAYOUT/GROUP 等
	for h, g := range doc.internalObjects {
		if g == nil || seen[h] {
			continue
		}
		if _, ok := gfWriters[g.Name]; !ok {
			continue // 无 gfWrite 编码器的类型：能力边界外
		}
		body, err := encodeForwardGenericObject(g, verR2000, dyn)
		if err != nil {
			return nil, fmt.Errorf("cad: 通用对象 %s(%d) 编码失败: %w", g.Name, h, err)
		}
		add(h, body)
	}
	// DICTIONARY（结构化正向；JSON 来源的 objGeneric 形式字典不在范围）
	for h, d := range doc.dictionaries {
		if d == nil || seen[h] {
			continue
		}
		body, err := encodeForwardDictionaryBody(d, verR2000)
		if err != nil {
			return nil, fmt.Errorf("cad: DICTIONARY %d 编码失败: %w", h, err)
		}
		add(h, body)
	}
	appendForwardSkeleton(doc, &out, seen)
	return out, nil
}

// forwardSkeletonHandles R2000 骨架对象的惯例句柄（与内嵌 HeaderVars 模板
// 的引用一致：BLOCK_RECORD_MSPACE=0x1F、表控制对象 0x1~0xA、
// BLOCK_RECORD_PSPACE=0x55）。被文档实体占用时按 allocate 降级换位。
const (
	fwdHdlBlockControl    = uint64(0x1)
	fwdHdlLayerControl    = uint64(0x2)
	fwdHdlStyleControl    = uint64(0x3)
	fwdHdlLtypeControl    = uint64(0x5)
	fwdHdlViewControl     = uint64(0x6)
	fwdHdlUcsControl      = uint64(0x7)
	fwdHdlVportControl    = uint64(0x8)
	fwdHdlAppidControl    = uint64(0x9)
	fwdHdlDimstyleControl = uint64(0xA)
	fwdHdlModelSpace      = uint64(0x1F)
	fwdHdlPaperSpace      = uint64(0x55)
)

// appendForwardSkeleton 构建写出必需的骨架对象（LibreDWG dwgread 输出
// DXF 的 BLOCK_CONTROL/*MODEL_SPACE 链）：表控制对象 ×9、*MODEL_SPACE 与
// *PAPER_SPACE 块头、各块定义块头。句柄与文档实体冲突时从最大句柄之上
// 顺序分配（模板句柄引用降级为悬空，dwgread 走 find_first_type 兜底）。
func appendForwardSkeleton(doc *Document, out *[]fwdObject, seen map[uint64]bool) {
	allocate := func(preferred uint64) uint64 {
		if !seen[preferred] {
			return preferred
		}
		maxH := preferred
		for h := range seen {
			if h > maxH {
				maxH = h
			}
		}
		h := maxH + 1
		for seen[h] {
			h++
		}
		return h
	}
	// 块定义块头（含块内首末实体句柄）；块名优先取 JSON 侧 internalObjects
	// 的 name 字段，缺失时以句柄合成
	type blkDef struct {
		handle      uint64
		name        string
		first, last uint64
		blockEnt    uint64
		endblkEnt   uint64
		ents        []uint64
	}
	var blocks []blkDef
	for h, list := range doc.blocks {
		if h == 0 {
			continue
		}
		bd := blkDef{handle: allocate(h), name: ""}
		if g := doc.internalObjects[h]; g != nil {
			if n, ok := g.Field("name").(string); ok {
				bd.name = n
			}
		}
		if bd.name == "" {
			bd.name = fmt.Sprintf("*B_%X", h)
		}
		for _, ent := range list {
			b := entBase(ent)
			if b == nil || b.handle == 0 {
				continue
			}
			bd.ents = append(bd.ents, b.handle)
			switch b.typeName {
			case "BLOCK":
				bd.blockEnt = b.handle
			case "ENDBLK":
				bd.endblkEnt = b.handle
			}
		}
		blocks = append(blocks, bd)
	}
	// *MODEL_SPACE：模型空间直属实体（含最大块启发式并入由读侧处理，
	// 此处仅挂 modelSpace 列表）
	ms := blkDef{handle: allocate(fwdHdlModelSpace), name: "*Model_Space"}
	for _, ent := range doc.modelSpace {
		if b := entBase(ent); b != nil && b.handle != 0 {
			ms.ents = append(ms.ents, b.handle)
		}
	}
	sort.Slice(ms.ents, func(i, j int) bool { return ms.ents[i] < ms.ents[j] })
	if n := len(ms.ents); n > 0 {
		ms.first, ms.last = ms.ents[0], ms.ents[n-1]
	}
	ps := blkDef{handle: allocate(fwdHdlPaperSpace), name: "*Paper_Space"}
	blocks = append(blocks, ms, ps)
	seenMark := func(h uint64) {
		if h != 0 {
			seen[h] = true
		}
	}
	// 块头对象（first/last 升序化）
	for i := range blocks {
		bd := &blocks[i]
		sort.Slice(bd.ents, func(a, b int) bool { return bd.ents[a] < bd.ents[b] })
		if n := len(bd.ents); n > 0 && bd.first == 0 {
			bd.first, bd.last = bd.ents[0], bd.ents[n-1]
		}
		body, err := encodeForwardBlockHeaderBody(forwardBlockHeader{
			handle:      bd.handle,
			name:        bd.name,
			firstEntity: bd.first,
			lastEntity:  bd.last,
		}, bd.blockEnt, bd.endblkEnt)
		if err != nil {
			continue
		}
		seenMark(bd.handle)
		*out = append(*out, fwdObject{handle: bd.handle, body: body})
	}
	// 表控制对象：entries 指向块头/LAYER 记录
	blkEntries := make([]uint64, 0, len(blocks))
	for _, bd := range blocks {
		blkEntries = append(blkEntries, bd.handle)
	}
	layerEntries := make([]uint64, 0, len(doc.layerColors))
	for h := range doc.layerColors {
		layerEntries = append(layerEntries, h)
	}
	sort.Slice(layerEntries, func(i, j int) bool { return layerEntries[i] < layerEntries[j] })
	controls := []forwardTableControl{
		{typeCode: 0x30, handle: allocate(fwdHdlBlockControl), entries: blkEntries},
		{typeCode: 0x32, handle: allocate(fwdHdlLayerControl), entries: layerEntries},
		{typeCode: 0x34, handle: allocate(fwdHdlStyleControl)},
		{typeCode: 0x38, handle: allocate(fwdHdlLtypeControl)},
		{typeCode: 0x3C, handle: allocate(fwdHdlViewControl)},
		{typeCode: 0x3E, handle: allocate(fwdHdlUcsControl)},
		{typeCode: 0x40, handle: allocate(fwdHdlVportControl)},
		{typeCode: 0x42, handle: allocate(fwdHdlAppidControl)},
		{typeCode: 0x44, handle: allocate(fwdHdlDimstyleControl)},
	}
	for _, tc := range controls {
		body, err := encodeForwardControlBody(tc, ms.handle, ps.handle)
		if err != nil {
			continue
		}
		seenMark(tc.handle)
		*out = append(*out, fwdObject{handle: tc.handle, body: body})
	}
}

// buildForwardObjectRecord 组装对象记录：MS size（< 0x7FFF 为 2 字节，
// 否则 4 字节续组，与 readMS/r2000RecordEnd 的 15 位组语义对称）+ body +
// 尾部 CRC16（覆盖 MS 与 body，seed 0xC0C1）。
func buildForwardObjectRecord(o fwdObject) ([]byte, error) {
	size := len(o.body)
	if size == 0 || size >= 0x40000000 {
		return nil, fmt.Errorf("cad: 对象 body 尺寸越界 %d", size)
	}
	msLen := 2
	if size >= 0x7FFF {
		msLen = 4
	}
	out := make([]byte, msLen+len(o.body)+2)
	if msLen == 2 {
		out[0] = uint8(size)
		out[1] = uint8(size >> 8)
	} else {
		// 4 字节续组：w0 = 低 15 位 | 0x8000 续传标志，w1 = 15..29 位（无标志）
		out[0] = uint8(size)
		out[1] = uint8(size>>8)&0x7F | 0x80
		out[2] = uint8(size >> 15)
		out[3] = uint8(size >> 23)
	}
	copy(out[msLen:], o.body)
	crc := crc16DWG(0xC0C1, out[:msLen+len(o.body)])
	out[msLen+len(o.body)] = uint8(crc)
	out[msLen+len(o.body)+1] = uint8(crc >> 8)
	return out, nil
}

// buildForwardClassesSection 空类表段：前哨兵 + RL size + BS 类数(0) +
// RC 占位 + CRC + 后哨兵。正向写出的主力实体全部使用固定类型码（<500），
// 无动态类需求。
// forwardClassInfo 类段注册条目：分配的动态类码与 DXF/CPP 类名
// （isEntity 决定条目的 itemClassID：0x1F2 实体类 / 0x1F3 对象类）。
type forwardClassInfo struct {
	classNumber uint16
	dxfName     string
	cppName     string
	isEntity    bool
}

// buildForwardClassesSection 构建 Classes 段：entries 为空时写空类表
// （固定码实体无需动态类），非空时逐条目写 BS 类码 + BS 代理标志 +
// 3×TV（app/cpp/dxf）+ B zombie + BS 实体类 id（对照
// parseClassesSectionR13R15 的 R2000 条目布局，app 名用 ObjectDBX 惯例）。
func buildForwardClassesSection(entries []forwardClassInfo) []byte {
	w := newEncWriter()
	w.writeTF(sentinelClassesBefore[:])
	sizePos := w.tellBits()
	if len(entries) == 0 {
		w.writeRL(0)
		w.writeBS(0) // num_classes = 0
		w.writeRC(0) // 占位字节
		w.alignByte()
		sizeEnd := w.tellBits()
		patchRL(w, sizePos, uint32((sizeEnd-sizePos-32)/8))
		w.writeCRCSeed(sizePos, 0xC0C1)
		w.writeTF(sentinelClassesAfter[:])
		return w.bytes()
	}
	dataStart := w.tellBits()
	w.writeRL(0) // dataSize 占位（纯条目区字节长，不含 RL/CRC，含尾部对齐）
	dataStart = w.tellBits()
	for _, e := range entries {
		w.writeBS(e.classNumber)
		w.writeBS(0) // proxy flags：非代理
		w.writeTV("ObjectDBX Classes")
		w.writeTV(e.cppName)
		w.writeTV(e.dxfName)
		w.writeB(false) // zombie
		if e.isEntity {
			w.writeBS(0x1F2)
		} else {
			w.writeBS(0x1F3)
		}
	}
	padBits := (8 - (w.tellBits()-dataStart)%8) % 8
	for i := uint64(0); i < padBits; i++ {
		w.writeB(false)
	}
	patchRL(w, sizePos, uint32((w.tellBits()-dataStart)/8))
	w.writeCRCSeed(sizePos, 0xC0C1)
	w.writeTF(sentinelClassesAfter[:])
	return w.bytes()
}
