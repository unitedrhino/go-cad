// encode_forward.go 实现结构化正向写出层（dwgwrite 第三代编码方向）：
// 无容器回放素材（r2000Raw/r2004Raw/r2007Raw 均为 nil）的 drawing.Document——
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
package writer

import (
	"encoding/base64"
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/drawing"
	"github.com/unitedrhino/go-cad/internal/entity"
	"github.com/unitedrhino/go-cad/internal/object"
	"github.com/unitedrhino/go-cad/internal/objrec"
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
type forwardEntEncoder func(w *bitstream.EncWriter, ent any, ver container.DwgVersion) error

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
	case *entity.EntWipeout:
		if t.TypeName == "IMAGE" || t.TypeName == "WIPEOUT" {
			return t.TypeName
		}
	case *entity.EntMLeader:
		return "MULTILEADER"
	case *entity.EntLight:
		return "LIGHT"
	case *entity.EntDimension:
		if t.TypeName == "ARC_DIMENSION" || t.TypeName == "LARGE_RADIAL_DIMENSION" {
			return t.TypeName
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
	case *entity.EntLine:
		return "LINE", forwardEntityCode["LINE"]
	case *entity.EntCircle:
		return "CIRCLE", forwardEntityCode["CIRCLE"]
	case *entity.EntArc:
		return "ARC", forwardEntityCode["ARC"]
	case *entity.EntPoint:
		return "POINT", forwardEntityCode["POINT"]
	case *entity.EntEllipse:
		return "ELLIPSE", forwardEntityCode["ELLIPSE"]
	case *entity.EntText:
		return "TEXT", forwardEntityCode["TEXT"]
	case *entity.EntMText:
		return "MTEXT", forwardEntityCode["MTEXT"]
	case *entity.EntLwPolyline:
		return "LWPOLYLINE", forwardEntityCode["LWPOLYLINE"]
	case *entity.EntInsert:
		return "INSERT", forwardEntityCode["INSERT"]
	case *entity.EntAttrib:
		return "ATTRIB", forwardEntityCode["ATTRIB"]
	case *entity.EntSolid:
		return "SOLID", forwardEntityCode["SOLID"]
	case *entity.EntFace3d:
		return "3DFACE", forwardEntityCode["3DFACE"]
	case *entity.EntVertex2d:
		return "VERTEX_2D", forwardEntityCode["VERTEX_2D"]
	case *entity.EntPolyline2d:
		return "POLYLINE_2D", forwardEntityCode["POLYLINE_2D"]
	case *entity.EntBlockLike:
		b := entity.EntityBase(ent)
		switch b.TypeName {
		case "BLOCK":
			return "BLOCK", forwardEntityCode["BLOCK"]
		case "ENDBLK":
			return "ENDBLK", forwardEntityCode["ENDBLK"]
		default:
			return "SEQEND", forwardEntityCode["SEQEND"]
		}
	case *entity.EntSpline:
		return "SPLINE", forwardEntityCode["SPLINE"]
	case *entity.EntHatch:
		return "HATCH", forwardEntityCode["HATCH"]
	case *entity.EntRay:
		if ent.(*entity.EntRay).Xline {
			return "XLINE", forwardEntityCode["XLINE"]
		}
		return "RAY", forwardEntityCode["RAY"]
	case *entity.EntLeader:
		return "LEADER", forwardEntityCode["LEADER"]
	case *entity.EntMLine:
		return "MLINE", forwardEntityCode["MLINE"]
	case *entity.EntTolerance:
		return "TOLERANCE", forwardEntityCode["TOLERANCE"]
	case *entity.EntShape:
		return "SHAPE", forwardEntityCode["SHAPE"]
	case *entity.EntViewport:
		return "VIEWPORT", forwardEntityCode["VIEWPORT"]
	case *entity.EntVertex3d:
		return "VERTEX_3D", forwardEntityCode["VERTEX_3D"]
	case *entity.EntVertexPface:
		name := "VERTEX_PFACE"
		if ent.(*entity.EntVertexPface).TypeName == "VERTEX_MESH" {
			name = "VERTEX_MESH"
		}
		return name, forwardEntityCode[name]
	case *entity.EntVertexPfaceFace:
		return "VERTEX_PFACE_FACE", forwardEntityCode["VERTEX_PFACE_FACE"]
	case *entity.EntPolyline3d:
		return "POLYLINE_3D", forwardEntityCode["POLYLINE_3D"]
	case *entity.EntPolylinePface:
		return "POLYLINE_PFACE", forwardEntityCode["POLYLINE_PFACE"]
	case *entity.EntPolylineMesh:
		return "POLYLINE_MESH", forwardEntityCode["POLYLINE_MESH"]
	case *entity.EntWipeout:
		// IMAGE/WIPEOUT 为动态类，返回注册名（类型码由调用方查动态分配表）
		if name := fwdDynamicEntityKind(ent); name != "" {
			return name, 0
		}
	case *entity.EntMLeader, *entity.EntLight:
		return fwdDynamicEntityKind(ent), 0
	case *entity.EntDimension:
		d := ent.(*entity.EntDimension)
		if fwdDynamicEntityKind(ent) != "" {
			return d.TypeName, 0
		}
		name := dimensionKindName(d.DimFlag)
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
func fwdDimLayout(dimFlag uint8) entity.DimSpecificLayout {
	switch dimFlag & 0x7 {
	case 1:
		return entity.DimLayoutAligned
	case 2:
		return entity.DimLayoutAng2Ln
	case 3:
		return entity.DimLayoutDiameter
	case 4:
		return entity.DimLayoutRadius
	case 5:
		return entity.DimLayoutAng3Pt
	case 6:
		return entity.DimLayoutOrdinate
	default:
		return entity.DimLayoutLinear
	}
}

// fwdHeadOf 取实体公共头（解码/JSON 来源实体携带 head；合成实体回退
// 缺省值：比例 1、线宽 ByLayer 29）。
func fwdHeadOf(b *entity.BaseEntity) *entity.CommonEntityHead {
	if b.Head != nil {
		return b.Head
	}
	return &entity.CommonEntityHead{Handle: b.Handle, Color: b.Color, EntityMode: b.Mode, LtypeScale: 1, Linewt: 29}
}

// fwdExtraF 取 extra 标量（缺省 def）。解码/JSON 来源的 thickness/
// elevation 等扩展字段经 base.extra 传递，编码时对称取回。
func fwdExtraF(b *entity.BaseEntity, key string, def float64) float64 {
	if v, ok := b.Extra[key].(float64); ok {
		return v
	}
	return def
}

// fwdExtraVec 取 extra 三维向量（缺省 def）。
func fwdExtraVec(b *entity.BaseEntity, key string, def entity.Point3) entity.Point3 {
	if v, ok := b.Extra[key].([]float64); ok && len(v) == 3 {
		return entity.Point3{X: v[0], Y: v[1], Z: v[2]}
	}
	return def
}

// ---- 位流编码辅助（读写原语的编码侧对称） ----

// writeBT 位厚度：R2000+ 形式（1 位 mode，1 → 0.0，否则 BD）。
func writeBT(w *bitstream.EncWriter, v float64) {
	if v == 0 {
		w.WriteB(true)
		return
	}
	w.WriteB(false)
	w.WriteBD(v)
}

// writeBE 位挤出方向：(0,0,1) 缺省走 1 位捷径，否则 3BD。
func writeBE(w *bitstream.EncWriter, x, y, z float64) {
	if x == 0 && y == 0 && z == 1 {
		w.WriteB(true)
		return
	}
	w.WriteB(false)
	w.WriteBD(x)
	w.WriteBD(y)
	w.WriteBD(z)
}

// write3BD 三维点（3×BD 独立编码，无前导位）。
func write3BD(w *bitstream.EncWriter, p entity.Point3) {
	w.WriteBD(p.X)
	w.WriteBD(p.Y)
	w.WriteBD(p.Z)
}

// writeHdlAbs 绝对句柄引用（code 5，counter 按值域最小化）。
func writeHdlAbs(w *bitstream.EncWriter, h uint64) {
	switch {
	case h == 0:
		w.WriteH(5, 0, 0)
	case h <= 0xFF:
		w.WriteH(5, 1, h)
	case h <= 0xFFFF:
		w.WriteH(5, 2, h)
	case h <= 0xFFFFFF:
		w.WriteH(5, 3, h)
	default:
		w.WriteH(5, 4, h)
	}
}

// writeHdlNull 空句柄引用（xdic 等占位，读侧解析为 0）。
func writeHdlNull(w *bitstream.EncWriter) { w.WriteH(5, 0, 0) }

// nearestACI 真彩色 → 最近 ACI 索引（RGB 欧氏距离最小）。R2000 容器的
// 颜色段仅承载索引（真彩 ENC 为 R2004+），跨版本写出时颜色降维。
func nearestACI(rgb uint32) uint16 {
	r, g, b := drawing.SplitTrueColor(rgb)
	best, bestD := uint16(7), uint32(math.MaxUint32)
	for i := 1; i <= 255; i++ {
		rr, gg, bb, ok := drawing.AciColor(uint16(i), false)
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
func writeForwardColor(w *bitstream.EncWriter, c entity.EntColor) {
	switch {
	case c.HasTrue:
		if idx := nearestACI(c.TrueColor); idx != 0 {
			w.WriteB(false)
			w.WriteB(true)
			w.WriteRC(uint8(idx))
			return
		}
		w.WriteB(true)
		w.WriteB(true)
	case c.HasIndex && c.Index == 256:
		w.WriteB(true)
		w.WriteB(true)
	case c.HasIndex && c.Index == 0:
		w.WriteB(true)
		w.WriteB(false)
	case c.HasIndex && c.Index < 256:
		w.WriteB(false)
		w.WriteB(true)
		w.WriteRC(uint8(c.Index))
	case c.HasIndex:
		// 256 以上窗口色等非法索引按 ByLayer 归一（RS 回写会被读侧
		// raw&0x1FF 截断为错误值）
		w.WriteB(true)
		w.WriteB(true)
	default:
		w.WriteB(true)
		w.WriteB(true)
	}
}

// patchRL 将 RL（小端 4 字节、每字节 MSB-first 位序）回填到缓冲 bitOff
// 起的位区间，供 bitsize 两遍法回填（RL 起点随 BS 类型码宽度非字节对齐）。
// 与 writeRL 的位序逐位对称。
func patchRL(w *bitstream.EncWriter, bitOff uint64, v uint32) {
	for byteIdx := 0; byteIdx < 4; byteIdx++ {
		b := uint8(v >> (8 * byteIdx))
		for i := 0; i < 8; i++ {
			bit := (b >> (7 - i)) & 1
			pos := bitOff + uint64(byteIdx*8+i)
			if pos+1 > uint64(len(w.Data))*8 {
				return
			}
			mask := byte(1) << (7 - pos%8)
			if bit != 0 {
				w.Data[pos/8] |= mask
			} else {
				w.Data[pos/8] &^= mask
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
func encodeForwardEntityBody(ent any, ver container.DwgVersion, dyn map[string]uint16) ([]byte, error) {
	b := entity.EntityBase(ent)
	if b == nil || b.Handle == 0 {
		return nil, fmt.Errorf("cad: 正向编码缺少实体句柄")
	}
	name, typeCode := fwdEntityKind(ent)
	if name == "" {
		return nil, fmt.Errorf("cad: 实体类型无正向编码器（%s）", b.TypeName)
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
	w := bitstream.NewEncWriter()
	w.WriteBS(typeCode)
	rlOff := w.TellBits()
	w.WriteRL(0) // bitsize 占位
	// 公共头（R2000 主布局：objSizeInSub + pictureRL + nolinksBit + linewt RC）
	writeHdlSelf(w, b.Handle)
	w.WriteBS(0)    // EED 链终止（结构化正向不回放应用数据，见报告边界说明）
	w.WriteB(false) // preview_exists
	w.WriteBB(b.Mode)
	w.WriteBL(0)   // num_reactors（正向重建无 reactor 句柄来源，恒 0）
	w.WriteB(true) // nolinks：无 prev/next 链接句柄
	writeForwardColor(w, b.Color)
	if head.LtypeScale != 0 {
		w.WriteBD(head.LtypeScale)
	} else {
		w.WriteBD(1)
	}
	w.WriteBB(0) // ltype_flags：ByLayer（无句柄）
	w.WriteBB(0) // plotstyle_flags
	w.WriteBS(uint16(head.Invisible))
	if head.Linewt != 0 {
		w.WriteRC(uint8(head.Linewt))
	} else {
		w.WriteRC(29) // ByLayer
	}
	// 专有字段
	if enc, ok := forwardEntityEncoders[name]; ok {
		if err := enc(w, ent, ver); err != nil {
			return nil, err
		}
	}
	// bitsize 回填：handle 流起点（body 局部绝对位）
	patchRL(w, rlOff, uint32(w.TellBits()))
	// handle 流：owner（mode 0）→ xdic → layer → 类型专属附加
	if b.Mode == 0 {
		writeHdlAbs(w, b.Owner)
	}
	writeHdlNull(w)
	writeHdlAbs(w, b.Layer)
	if enc, ok := forwardEntityHandleExtra[name]; ok {
		if err := enc(w, ent, ver); err != nil {
			return nil, err
		}
	}
	w.AlignByte()
	return w.Bytes(), nil
}

// writeHdlSelf 写对象自身句柄（公共头首字段，code 0 当前句柄引用）。
func writeHdlSelf(w *bitstream.EncWriter, h uint64) {
	switch {
	case h <= 0xFF:
		w.WriteH(0, 1, h)
	case h <= 0xFFFF:
		w.WriteH(0, 2, h)
	case h <= 0xFFFFFF:
		w.WriteH(0, 3, h)
	default:
		w.WriteH(0, 4, h)
	}
}

// encFwdLine LINE：z 全零位 + x/y 起点直读、终点差分 + [z 对] + 厚度 + 挤出。
func encFwdLine(w *bitstream.EncWriter, ent any, _ container.DwgVersion) error {
	e := ent.(*entity.EntLine)
	zZero := e.Start.Z == 0 && e.End.Z == 0
	w.WriteB(zZero)
	w.WriteRD(e.Start.X)
	w.WriteDD(e.End.X, e.Start.X)
	w.WriteRD(e.Start.Y)
	w.WriteDD(e.End.Y, e.Start.Y)
	if !zZero {
		w.WriteRD(e.Start.Z)
		w.WriteDD(e.End.Z, e.Start.Z)
	}
	writeBT(w, fwdExtraF(&e.BaseEntity, "thickness", 0))
	ex := fwdExtraVec(&e.BaseEntity, "extrusion", entity.Point3{X: 0, Y: 0, Z: 1})
	writeBE(w, ex.X, ex.Y, ex.Z)
	return nil
}

// encFwdCircle CIRCLE：3BD 圆心 + BD 半径 + 厚度 + 挤出。
func encFwdCircle(w *bitstream.EncWriter, ent any, _ container.DwgVersion) error {
	e := ent.(*entity.EntCircle)
	write3BD(w, e.Center)
	w.WriteBD(e.Radius)
	writeBT(w, fwdExtraF(&e.BaseEntity, "thickness", 0))
	ex := fwdExtraVec(&e.BaseEntity, "extrusion", entity.Point3{X: 0, Y: 0, Z: 1})
	writeBE(w, ex.X, ex.Y, ex.Z)
	return nil
}

// encFwdArc ARC：圆/厚/挤同 CIRCLE，多出起止角（弧度）。
func encFwdArc(w *bitstream.EncWriter, ent any, _ container.DwgVersion) error {
	e := ent.(*entity.EntArc)
	write3BD(w, e.Center)
	w.WriteBD(e.Radius)
	writeBT(w, fwdExtraF(&e.BaseEntity, "thickness", 0))
	ex := fwdExtraVec(&e.BaseEntity, "extrusion", entity.Point3{X: 0, Y: 0, Z: 1})
	writeBE(w, ex.X, ex.Y, ex.Z)
	w.WriteBD(e.AngleStart)
	w.WriteBD(e.AngleEnd)
	return nil
}

// encFwdPoint POINT：3BD 定位 + 厚度 + 挤出 + x 轴角度。
func encFwdPoint(w *bitstream.EncWriter, ent any, _ container.DwgVersion) error {
	e := ent.(*entity.EntPoint)
	write3BD(w, e.Location)
	writeBT(w, fwdExtraF(&e.BaseEntity, "thickness", 0))
	ex := fwdExtraVec(&e.BaseEntity, "extrusion", entity.Point3{X: 0, Y: 0, Z: 1})
	writeBE(w, ex.X, ex.Y, ex.Z)
	w.WriteBD(e.Rotation)
	return nil
}

// encFwdEllipse ELLIPSE：3BD 圆心 + 3BD 主轴 + 3BD 挤出（主体内）+
// BD 轴比 + 起止角。
func encFwdEllipse(w *bitstream.EncWriter, ent any, _ container.DwgVersion) error {
	e := ent.(*entity.EntEllipse)
	write3BD(w, e.Center)
	write3BD(w, e.MajorAxis)
	ex := fwdExtraVec(&e.BaseEntity, "extrusion", entity.Point3{X: 0, Y: 0, Z: 1})
	write3BD(w, ex)
	w.WriteBD(e.Ratio)
	w.WriteBD(e.StartAng)
	w.WriteBD(e.EndAng)
	return nil
}

// textFieldSource TEXT/ATTRIB 共用的文本字段视图（两者 dat 流前段同构）。
type textFieldSource struct {
	base             *entity.BaseEntity
	text             string
	insertion        entity.Point3
	height, rotation float64
	hAlign, vAlign   uint16
	gen              uint16
	alignPt          *entity.Point2
}

// encFwdTextFields TEXT 布局字段段：RC dataflags（0=全字段）+ 高程 +
// 2RD 插入点 + 2DD 对齐点（相对插入点差分）+ 挤出 + 厚度 + 倾角 + 旋转 +
// 字高 + 宽度因子 + 文本串 + 生成/水平/垂直对齐（顺序对照 decodeTextVer）。
func encFwdTextFields(w *bitstream.EncWriter, s textFieldSource, ver container.DwgVersion) {
	w.WriteRC(0) // dataflags：全部字段在场
	w.WriteRD(fwdExtraF(s.base, "elevation", s.insertion.Z))
	w.WriteRD(s.insertion.X)
	w.WriteRD(s.insertion.Y)
	ax, ay := s.insertion.X, s.insertion.Y
	if s.alignPt != nil {
		ax, ay = s.alignPt.X, s.alignPt.Y
	}
	w.WriteDD(ax, s.insertion.X)
	w.WriteDD(ay, s.insertion.Y)
	ex := fwdExtraVec(s.base, "extrusion", entity.Point3{X: 0, Y: 0, Z: 1})
	writeBE(w, ex.X, ex.Y, ex.Z)
	writeBT(w, fwdExtraF(s.base, "thickness", 0))
	w.WriteRD(fwdExtraF(s.base, "oblique_angle", 0))
	w.WriteRD(s.rotation)
	w.WriteRD(s.height)
	w.WriteRD(fwdExtraF(s.base, "width_factor", 1))
	if ver >= container.VerR2007 {
		w.WriteTU(s.text)
	} else {
		w.WriteTV(s.text)
	}
	w.WriteBS(s.gen)
	w.WriteBS(s.hAlign)
	w.WriteBS(s.vAlign)
}

// encFwdText TEXT 实体：字段段见 encFwdTextFields。
func encFwdText(w *bitstream.EncWriter, ent any, ver container.DwgVersion) error {
	e := ent.(*entity.EntText)
	encFwdTextFields(w, textFieldSource{
		base: &e.BaseEntity, text: e.Text, insertion: e.Insertion,
		height: e.Height, rotation: e.Rotation,
		hAlign: e.HAlign, vAlign: e.VAlign, gen: e.Gen, alignPt: e.AlignPt,
	}, ver)
	return nil
}

// encFwdMText MTEXT：插入点/挤出/轴向量 3BD×3 + 矩形宽/字高 BD +
// 附加/流向 BS + 范围 BD×2 + 文本 + 行距样式/因子 + 未知位（顺序对照
// decodeMTextVer 的 R2000 分支；R2004+ 背景段不写）。
func encFwdMText(w *bitstream.EncWriter, ent any, ver container.DwgVersion) error {
	e := ent.(*entity.EntMText)
	write3BD(w, e.Insertion)
	ex := fwdExtraVec(&e.BaseEntity, "extrusion", entity.Point3{X: 0, Y: 0, Z: 1})
	write3BD(w, ex)
	write3BD(w, e.XAxisDir)
	w.WriteBD(e.RectWidth)
	w.WriteBD(e.TextHeight)
	w.WriteBS(e.Attachment)
	flow := int64(5) // by style 缺省（解码侧 flow_dir 缺省值）
	if v, ok := e.BaseEntity.Extra["flow_dir"].(int64); ok {
		flow = v
	}
	w.WriteBS(uint16(flow))
	w.WriteBD(fwdExtraF(&e.BaseEntity, "extents_height", 0))
	w.WriteBD(fwdExtraF(&e.BaseEntity, "extents_width", 0))
	if ver >= container.VerR2007 {
		w.WriteTU(e.Text)
	} else {
		w.WriteTV(e.Text)
	}
	w.WriteBS(0) // linespacing style：at least
	w.WriteBD(1) // linespacing factor
	w.WriteB(false)
	return nil
}

// encFwdLwPolyline LWPOLYLINE：标志驱动的可选段 + 顶点差分数组
// （首点绝对 RD，其余 DD 相对前点）。标志位按内容反推（0x01 挤出 3BD、
// 0x02 厚度、0x04 常量宽、0x08 标高、0x10 凸度、0x20 段宽）。
func encFwdLwPolyline(w *bitstream.EncWriter, ent any, ver container.DwgVersion) error {
	e := ent.(*entity.EntLwPolyline)
	ex := fwdExtraVec(&e.BaseEntity, "extrusion", entity.Point3{X: 0, Y: 0, Z: 1})
	thickness := fwdExtraF(&e.BaseEntity, "thickness", e.Thickness)
	var flags uint16
	numBulges := 0
	for i, bl := range e.Bulges {
		if bl != 0 {
			numBulges = i + 1
		}
	}
	if ex != (entity.Point3{X: 0, Y: 0, Z: 1}) {
		flags |= 0x01
	}
	if thickness != 0 {
		flags |= 0x02
	}
	if e.ConstWidth != 0 {
		flags |= 0x04
	}
	if e.Elevation != 0 {
		flags |= 0x08
	}
	if numBulges > 0 {
		flags |= 0x10
	}
	if len(e.Widths) > 0 {
		flags |= 0x20
	}
	w.WriteBS(flags)
	if flags&0x04 != 0 {
		w.WriteBD(e.ConstWidth)
	}
	if flags&0x08 != 0 {
		w.WriteBD(e.Elevation)
	}
	if flags&0x02 != 0 {
		w.WriteBD(thickness)
	}
	if flags&0x01 != 0 {
		write3BD(w, ex)
	}
	w.WriteBL(uint32(len(e.Vertices)))
	if flags&0x10 != 0 {
		w.WriteBL(uint32(numBulges))
	}
	for i, v := range e.Vertices {
		if i == 0 {
			w.WriteRD(v.X)
			w.WriteRD(v.Y)
			continue
		}
		prev := e.Vertices[i-1]
		w.WriteDD(v.X, prev.X)
		w.WriteDD(v.Y, prev.Y)
	}
	for i := 0; i < numBulges && i < len(e.Bulges); i++ {
		w.WriteBD(e.Bulges[i])
	}
	for _, width := range e.Widths {
		w.WriteBD(width.Start)
		w.WriteBD(width.End)
	}
	return nil
}

// encFwdInsert INSERT：插入点 + BB 缩放标志（差分/全 1）+ 旋转 + 挤出 +
// 属性存在位。块头与属性句柄在 handle 流（encFwdInsertHandles）。
func encFwdInsert(w *bitstream.EncWriter, ent any, _ container.DwgVersion) error {
	e := ent.(*entity.EntInsert)
	write3BD(w, e.Position)
	switch {
	case e.Scale.X == 1 && e.Scale.Y == 1 && e.Scale.Z == 1:
		w.WriteBB(0x03)
	case e.Scale.X == e.Scale.Y && e.Scale.Y == e.Scale.Z:
		w.WriteBB(0x02)
		w.WriteRD(e.Scale.X)
	default:
		w.WriteBB(0x00)
		w.WriteRD(e.Scale.X)
		w.WriteDD(e.Scale.Y, e.Scale.X)
		w.WriteDD(e.Scale.Z, e.Scale.X)
	}
	w.WriteBD(e.Rotation)
	ex := fwdExtraVec(&e.BaseEntity, "extrusion", entity.Point3{X: 0, Y: 0, Z: 1})
	write3BD(w, ex) // 读侧 INSERT 挤出为 3BD（无 BE 前导位）
	w.WriteB(len(e.Attribs) > 0)
	return nil
}

// encFwdInsertHandles INSERT handle 流附加：块头句柄 + [R13~R2000 语义的
// 首/末属性句柄与 SEQEND]（对照 decodeInsert 的 handle 流消费顺序；
// 无属性时仅块头）。
func encFwdInsertHandles(w *bitstream.EncWriter, ent any, ver container.DwgVersion) error {
	e := ent.(*entity.EntInsert)
	writeHdlAbs(w, e.BlockHeader)
	if len(e.Attribs) == 0 {
		return nil
	}
	if ver == container.VerR13 || ver == container.VerR14 || ver == container.VerR2000 {
		writeHdlAbs(w, e.Attribs[0])
		writeHdlAbs(w, e.Attribs[len(e.Attribs)-1])
	} else {
		for _, h := range e.Attribs {
			writeHdlAbs(w, h)
		}
	}
	writeHdlAbs(w, e.SeqendHandle())
	return nil
}

// encFwdAttrib ATTRIB：TEXT 同构字段（值文本）+ 标签串 + 字段长度 +
// 标志（对照 decodeAttribVer 的 R2000 分支）。
func encFwdAttrib(w *bitstream.EncWriter, ent any, ver container.DwgVersion) error {
	e := ent.(*entity.EntAttrib)
	encFwdTextFields(w, textFieldSource{
		base: &e.BaseEntity, text: e.Text, insertion: e.Insertion,
		height: e.Height, rotation: e.Rotation,
		hAlign: e.HAlign, vAlign: e.VAlign, gen: e.Gen, alignPt: nil,
	}, ver)
	if ver >= container.VerR2007 {
		w.WriteTU(e.Tag)
	} else {
		w.WriteTV(e.Tag)
	}
	w.WriteBS(0) // field_length
	w.WriteRC(0) // flags
	return nil
}

// encFwdSolid SOLID/TRACE：厚度 + 高程 + 4 角点（2RD×4）+ 挤出。
func encFwdSolid(w *bitstream.EncWriter, ent any, _ container.DwgVersion) error {
	e := ent.(*entity.EntSolid)
	writeBT(w, e.Thickness)
	w.WriteBD(e.Elevation)
	for _, p := range [4]entity.Point2{e.P1, e.P2, e.P3, e.P4} {
		w.WriteRD(p.X)
		w.WriteRD(p.Y)
	}
	writeBE(w, e.Extrusion.X, e.Extrusion.Y, e.Extrusion.Z)
	return nil
}

// encFwdFace3d 3DFACE：无标志位 + z 全零位 + 首点 RD + 3×3DD 差分 +
// [不可见边标志]（对照 decodeFace3d 的 R2000+ 分支）。
func encFwdFace3d(w *bitstream.EncWriter, ent any, _ container.DwgVersion) error {
	e := ent.(*entity.EntFace3d)
	noFlags := e.InvisibleEdgeFlags == 0
	zZero := e.P1.Z == 0
	w.WriteB(noFlags)
	w.WriteB(zZero)
	w.WriteRD(e.P1.X)
	w.WriteRD(e.P1.Y)
	if !zZero {
		w.WriteRD(e.P1.Z)
	}
	prev := e.P1
	for _, p := range [3]entity.Point3{e.P2, e.P3, e.P4} {
		w.WriteDD(p.X, prev.X)
		w.WriteDD(p.Y, prev.Y)
		w.WriteDD(p.Z, prev.Z)
		prev = p
	}
	if !noFlags {
		w.WriteBS(e.InvisibleEdgeFlags)
	}
	return nil
}

// encFwdVertex2d VERTEX_2D：RC 标志 + 3BD 位置 + 起末宽（起=末时以负起宽
// 复用）+ 凸度 + 切向（对照 decodeVertex2d）。
func encFwdVertex2d(w *bitstream.EncWriter, ent any, _ container.DwgVersion) error {
	e := ent.(*entity.EntVertex2d)
	w.WriteRC(uint8(e.Flags))
	write3BD(w, e.Position)
	if e.StartWidth != 0 && e.StartWidth == e.EndWidth {
		w.WriteBD(-e.StartWidth) // 负起宽：读侧复用为末宽（零宽不走捷径，避免位流错位）
	} else {
		w.WriteBD(e.StartWidth)
		w.WriteBD(e.EndWidth)
	}
	w.WriteBD(e.Bulge)
	w.WriteBD(e.TangentDir)
	return nil
}

// encFwdPolyline2d POLYLINE_2D：标志 + 曲线类型 + 起末宽 + 厚度 + 高程 +
// 挤出（对照 decodePolyline2d 的 R2000 分支：无 owned 计数）。顶点句柄
// 在 handle 流附加段。
func encFwdPolyline2d(w *bitstream.EncWriter, ent any, _ container.DwgVersion) error {
	e := ent.(*entity.EntPolyline2d)
	w.WriteBS(e.Flags)
	w.WriteBS(e.CurveType)
	w.WriteBD(e.WidthStart)
	w.WriteBD(e.WidthEnd)
	writeBT(w, e.Thickness)
	w.WriteBD(e.Elevation)
	write3BD(w, e.Extrusion)
	return nil
}

// encFwdPolylineHandles POLYLINE_2D handle 流附加：首/末顶点句柄
// （R13~R2000 语义；R2004+ 读侧不消费，写不写均可，保持结构完整）。
// encFwdPolylineHandles POLYLINE_2D handle 流附加：R13~R2000 语义的
// 首/末顶点句柄 + SEQEND 占位（JSON 来源缺省空引用，顶点经 owner 归属
// 聚合还原；读侧保存 seqend 供 gold 对照，值不参与渲染）。
func encFwdPolylineHandles(w *bitstream.EncWriter, ent any, ver container.DwgVersion) error {
	e := ent.(*entity.EntPolyline2d)
	if ver == container.VerR13 || ver == container.VerR14 || ver == container.VerR2000 {
		writeHdlAbs(w, e.FirstVertex)
		writeHdlAbs(w, e.LastVertex)
	}
	writeHdlAbs(w, e.SeqendPlaceholder())
	return nil
}

// encFwdBlock BLOCK：公共头后仅块名（对照 decodeBlockLike）。
func encFwdBlock(w *bitstream.EncWriter, ent any, ver container.DwgVersion) error {
	e := ent.(*entity.EntBlockLike)
	if ver >= container.VerR2007 {
		w.WriteTU(e.Name)
	} else {
		w.WriteTV(e.Name)
	}
	return nil
}

// ---- 极限批次 E：实体编码器续作（对照各实体读侧解码器的 R2000 布局） ----

// encFwdSpline SPLINE：scenario BL + degree BL + 拟合点/控制点双模式数据
// （对照 parseSplineFitData/parseSplineControlData 的 R2000 布局）。
// 控制点模式：rational/closed/periodic 3 位 + 两容差 + 节点/控制点计数 +
// weighted 回显位 + 数组；拟合点模式：拟合容差 + 起末切线（结构化来源
// 无字段，零向量占位）+ 拟合点数组。
func encFwdSpline(w *bitstream.EncWriter, ent any, _ container.DwgVersion) error {
	e := ent.(*entity.EntSpline)
	w.WriteBL(e.Scenario)
	w.WriteBL(e.Degree)
	if e.Scenario == 2 {
		w.WriteBD(e.FitTolerance)
		write3BD(w, entity.Point3{}) // beg_tan_vec：JSON/DXF 来源未建模，零占位
		write3BD(w, entity.Point3{}) // end_tan_vec
		w.WriteBL(uint32(len(e.FitPoints)))
		for _, p := range e.FitPoints {
			write3BD(w, p)
		}
		return nil
	}
	rational := e.Rational || len(e.Weights) > 0
	w.WriteB(rational)
	w.WriteB(e.Closed)
	w.WriteB(e.Periodic)
	w.WriteBD(e.KnotTolerance)
	w.WriteBD(e.CtrlTolerance)
	w.WriteBL(uint32(len(e.Knots)))
	w.WriteBL(uint32(len(e.ControlPoints)))
	weighted := rational && len(e.Weights) >= len(e.ControlPoints)
	w.WriteB(weighted)
	for _, k := range e.Knots {
		w.WriteBD(k)
	}
	for i, p := range e.ControlPoints {
		write3BD(w, p)
		if weighted {
			w.WriteBD(e.Weights[i])
		}
	}
	return nil
}

// encFwdHatch HATCH（R2000 无渐变段）：高程/挤出/图案名/填充与关联位 +
// 边界路径数组（边集逐段曲线类型分派 / 多段线顶点带可选凸度）+ 图案样式
// 与定义线段 + 种子点段（对照 decodeHatchBody 的 R2000 分支）。
func encFwdHatch(w *bitstream.EncWriter, ent any, _ container.DwgVersion) error {
	h := ent.(*entity.EntHatch)
	w.WriteBD(h.Elevation)
	write3BD(w, h.Extrusion)
	w.WriteTV(h.Name)
	w.WriteB(h.SolidFill)
	w.WriteB(h.Associative)
	w.WriteBL(uint32(len(h.Paths)))
	for _, p := range h.Paths {
		writeBL(w, p.Flag)
		if p.Flag&0x02 == 0 {
			w.WriteBL(uint32(len(p.Segs)))
			for _, seg := range p.Segs {
				w.WriteRC(seg.CurveType)
				switch seg.CurveType {
				case 1:
					w.WriteRD(seg.First.X)
					w.WriteRD(seg.First.Y)
					w.WriteRD(seg.Second.X)
					w.WriteRD(seg.Second.Y)
				case 2:
					w.WriteRD(seg.Center.X)
					w.WriteRD(seg.Center.Y)
					w.WriteBD(seg.Radius)
					w.WriteBD(seg.StartAng)
					w.WriteBD(seg.EndAng)
					w.WriteB(seg.Ccw)
				case 3:
					w.WriteRD(seg.Center.X)
					w.WriteRD(seg.Center.Y)
					w.WriteRD(seg.Endpoint.X)
					w.WriteRD(seg.Endpoint.Y)
					w.WriteBD(seg.Ratio)
					w.WriteBD(seg.StartAng)
					w.WriteBD(seg.EndAng)
					w.WriteB(seg.Ccw)
				case 4:
					w.WriteBL(seg.Degree)
					w.WriteB(seg.Rational)
					w.WriteB(seg.Periodic)
					w.WriteBL(uint32(len(seg.Knots)))
					w.WriteBL(uint32(len(seg.Ctrl)))
					for _, k := range seg.Knots {
						w.WriteBD(k)
					}
					for i, c := range seg.Ctrl {
						w.WriteRD(c.X)
						w.WriteRD(c.Y)
						if seg.Rational {
							var weight float64
							if i < len(seg.Weights) {
								weight = seg.Weights[i]
							}
							w.WriteBD(weight)
						}
					}
				default:
					return fmt.Errorf("cad: HATCH 路径含未知边类型 %d", seg.CurveType)
				}
			}
			w.WriteBL(0) // 边界对象句柄数：结构化重建无边界句柄来源
		} else {
			bulgesPresent := p.BulgesPresent
			for _, pv := range p.PolyVerts {
				if pv.Bulge != 0 {
					bulgesPresent = true
				}
			}
			w.WriteB(bulgesPresent)
			w.WriteB(p.Closed)
			w.WriteBL(uint32(len(p.PolyVerts)))
			for _, pv := range p.PolyVerts {
				w.WriteRD(pv.P.X)
				w.WriteRD(pv.P.Y)
				if bulgesPresent {
					w.WriteBD(pv.Bulge)
				}
			}
			w.WriteBL(0) // 边界对象句柄数
		}
	}
	w.WriteBS(h.Style)
	w.WriteBS(h.PatternType)
	if !h.SolidFill {
		w.WriteBD(h.Angle)
		w.WriteBD(h.ScaleSpacing)
		w.WriteB(h.DoubleFlag)
		w.WriteBS(uint16(len(h.Deflines)))
		for _, dl := range h.Deflines {
			w.WriteBD(dl.Angle)
			w.WriteBD(dl.Pt0.X)
			w.WriteBD(dl.Pt0.Y)
			w.WriteBD(dl.Offset.X)
			w.WriteBD(dl.Offset.Y)
			w.WriteBS(uint16(len(dl.Dashes)))
			for _, d := range dl.Dashes {
				w.WriteBD(d)
			}
		}
	}
	if h.HasDerived {
		w.WriteBD(h.PixelSize)
	}
	w.WriteBL(uint32(len(h.Seeds)))
	for _, s := range h.Seeds {
		w.WriteRD(s.X)
		w.WriteRD(s.Y)
	}
	return nil
}

// writeBL 路径 flag 的 BL 别名（可读性：hatchPath.flag 为 uint32）。
func writeBL(w *bitstream.EncWriter, v uint32) { w.WriteBL(v) }

// encFwdRay RAY/XLINE：3BD 起点 + 3BD 单位方向。
func encFwdRay(w *bitstream.EncWriter, ent any, _ container.DwgVersion) error {
	e := ent.(*entity.EntRay)
	write3BD(w, e.Start)
	write3BD(w, e.UnitVector)
	return nil
}

// encFwdLeader LEADER：未知位 + 注释/路径类型 + 折点数组 + 原点/挤出/X 向/
// 插入偏移/端点投影 3BD×5 + 文本框宽高 + 钩线与箭头标志 + 箭头类型 +
// 尾部两未知位（对照 decodeLeader 的 R2000 分支，R14 专属段不写）。
func encFwdLeader(w *bitstream.EncWriter, ent any, _ container.DwgVersion) error {
	l := ent.(*entity.EntLeader)
	w.WriteB(l.UnknownBit1)
	w.WriteBS(l.AnnotationType)
	w.WriteBS(l.PathType)
	w.WriteBL(uint32(len(l.Points)))
	for _, p := range l.Points {
		write3BD(w, p)
	}
	write3BD(w, l.Origin)
	write3BD(w, l.Extrusion)
	write3BD(w, l.XDirection)
	write3BD(w, l.InsptOffset)
	write3BD(w, l.Endptproj) // R13c3~R2007 段
	w.WriteBD(l.BoxHeight)
	w.WriteBD(l.BoxWidth)
	w.WriteB(l.HooklineDir)
	w.WriteB(l.ArrowheadOn)
	w.WriteBS(l.ArrowheadType)
	w.WriteB(l.UnknownBit4)
	w.WriteB(l.UnknownBit5)
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
func encFwdMLine(w *bitstream.EncWriter, ent any, _ container.DwgVersion) error {
	m := ent.(*entity.EntMLine)
	w.WriteBD(m.Scale)
	w.WriteRC(m.Justification)
	write3BD(w, entity.Point3{}) // base_point
	write3BD(w, entity.Point3{}) // extrusion
	w.WriteBS(m.OpenClosed)
	w.WriteRC(m.LinesInStyle)
	w.WriteBS(uint16(len(m.Vertices)))
	for _, v := range m.Vertices {
		write3BD(w, v.Position)
		write3BD(w, v.Direction)
		write3BD(w, v.Miter)
		segLines := splitEven(v.SegParams, int(m.LinesInStyle))
		areaLines := splitEven(v.AreaParams, int(m.LinesInStyle))
		for line := 0; line < int(m.LinesInStyle); line++ {
			var segs, areas []float64
			if line < len(segLines) {
				segs = segLines[line]
			}
			if line < len(areaLines) {
				areas = areaLines[line]
			}
			w.WriteBS(uint16(len(segs)))
			for _, p := range segs {
				w.WriteBD(p)
			}
			w.WriteBS(uint16(len(areas)))
			for _, p := range areas {
				w.WriteBD(p)
			}
		}
	}
	return nil
}

// encFwdMLineHandles MLINE handle 流附加：多线样式记录句柄（code 5，
// DXF 340；读侧从 common 流后按序取首个引用）。
func encFwdMLineHandles(w *bitstream.EncWriter, ent any, _ container.DwgVersion) error {
	writeHdlCode(w, 5, ent.(*entity.EntMLine).StyleHandle)
	return nil
}

// encFwdTolerance TOLERANCE（R2000 无 R13/R14 头）：插入点/对称轴/挤出
// 3BD×3 + 标注文本内联 TV（对照 decodeToleranceVer 的非 R13/R14 分支）。
func encFwdTolerance(w *bitstream.EncWriter, ent any, ver container.DwgVersion) error {
	t := ent.(*entity.EntTolerance)
	write3BD(w, t.Insertion)
	write3BD(w, t.XDirection)
	write3BD(w, t.Extrusion)
	if ver >= container.VerR2007 {
		w.WriteTU(t.Text)
	} else {
		w.WriteTV(t.Text)
	}
	return nil
}

// encFwdToleranceHandles TOLERANCE handle 流附加：标注样式句柄。
func encFwdToleranceHandles(w *bitstream.EncWriter, ent any, _ container.DwgVersion) error {
	writeHdlCode(w, 5, ent.(*entity.EntTolerance).Dimstyle)
	return nil
}

// encFwdShape SHAPE：插入点/缩放/旋转/宽度因子/倾斜/厚度 + STYLE 表索引
// BS + 挤出（对照 decodeShape 与 dwg.spec SHAPE SINCE(R_13b1)；样式记录
// 句柄以空引用占位）。
func encFwdShape(w *bitstream.EncWriter, ent any, _ container.DwgVersion) error {
	s := ent.(*entity.EntShape)
	write3BD(w, s.Insertion)
	w.WriteBD(s.Scale)
	w.WriteBD(s.Rotation)
	w.WriteBD(s.WidthFactor)
	w.WriteBD(s.Oblique)
	w.WriteBD(s.Thickness)
	w.WriteBS(s.StyleId)
	write3BD(w, s.Extrusion)
	return nil
}

// encFwdViewport VIEWPORT（R2000 布局）：中心/宽高 + 视图目标/方向 + 视角
// 尺寸/镜头/前后裁剪/捕捉角 + 视图中心/捕捉基点/捕捉间距/网格间距 2RD×4 +
// 圆缩放 BS + 冻结层数/状态 BL + 样式表 TV + 渲染模式 RC + UCS 段
// （对照 decodeViewportVer 的 R2000 分支；R2004+ shadeplot 与 R2007+
// grid_major/灯光段不写）。冻结层句柄数恒 0（无句柄来源）。
func encFwdViewport(w *bitstream.EncWriter, ent any, ver container.DwgVersion) error {
	vp := ent.(*entity.EntViewport)
	write3BD(w, vp.Center)
	w.WriteBD(vp.Width)
	w.WriteBD(vp.Height)
	write3BD(w, vp.ViewTarget)
	write3BD(w, vp.ViewDir)
	w.WriteBD(vp.ViewTwist)
	w.WriteBD(vp.ViewSize)
	w.WriteBD(vp.LensLength)
	w.WriteBD(vp.FrontZ)
	w.WriteBD(vp.BackZ)
	w.WriteBD(vp.SnapAng)
	w.WriteRD(vp.ViewCtr.X)
	w.WriteRD(vp.ViewCtr.Y)
	w.WriteRD(vp.SnapBase.X)
	w.WriteRD(vp.SnapBase.Y)
	w.WriteRD(vp.SnapUnit.X)
	w.WriteRD(vp.SnapUnit.Y)
	w.WriteRD(vp.GridUnit.X)
	w.WriteRD(vp.GridUnit.Y)
	w.WriteBS(vp.CircleZoom)
	w.WriteBL(vp.NumFrozenLayers)
	w.WriteBL(vp.StatusFlag)
	if ver < container.VerR2007 {
		w.WriteTV(vp.StyleSheet)
	}
	w.WriteRC(vp.RenderMode)
	w.WriteB(vp.UcsAtOrigin)
	w.WriteB(vp.UcsVP)
	write3BD(w, vp.Ucsorg)
	write3BD(w, vp.Ucsxdir)
	write3BD(w, vp.Ucsydir)
	w.WriteBD(vp.UcsElevation)
	w.WriteBS(vp.UcsOrthoView)
	return nil
}

// encFwdVertex3d VERTEX_3D/VERTEX_MESH/VERTEX_PFACE：RC 标志 + 3BD 位置
// （三顶点变体同布局，对照 decodeVertex3d/decodeVertexPface）。
func encFwdVertex3d(w *bitstream.EncWriter, ent any, _ container.DwgVersion) error {
	switch v := ent.(type) {
	case *entity.EntVertex3d:
		w.WriteRC(v.Flags)
		write3BD(w, v.Position)
	case *entity.EntVertexPface:
		w.WriteRC(v.Flag)
		write3BD(w, v.Position)
	}
	return nil
}

// encFwdVertexPfaceFace VERTEX_PFACE_FACE：4×BS 顶点索引（1 基，0 表边
// 结束；负值按 BSd 有符号语义回绕，对照 decodeVertexPfaceFace）。
func encFwdVertexPfaceFace(w *bitstream.EncWriter, ent any, _ container.DwgVersion) error {
	f := ent.(*entity.EntVertexPfaceFace)
	for i := 0; i < 4; i++ {
		w.WriteBS(uint16(int16(f.Vertind[i])))
	}
	return nil
}

// encFwdPolyline3d POLYLINE_3D：曲线类型/标志 RC×2（R2000 无 owned 计数，
// 顶点句柄在 handle 流附加段，对照 decodePolyline3d 的 R2000 分支）。
func encFwdPolyline3d(w *bitstream.EncWriter, ent any, _ container.DwgVersion) error {
	p := ent.(*entity.EntPolyline3d)
	w.WriteRC(p.Flags75)
	w.WriteRC(p.Flags70)
	return nil
}

// encFwdPolyline3dHandles POLYLINE_3D handle 流附加：R13~R2000 语义的
// 首/末顶点句柄 + SEQEND 占位（JSON 来源缺省空引用，顶点经 owner 归属
// 聚合还原；读侧保存 seqend 供 gold 对照，值不参与渲染）。
func encFwdPolyline3dHandles(w *bitstream.EncWriter, ent any, ver container.DwgVersion) error {
	p := ent.(*entity.EntPolyline3d)
	if ver == container.VerR13 || ver == container.VerR14 || ver == container.VerR2000 {
		writeHdlAbs(w, p.FirstVertex)
		writeHdlAbs(w, p.LastVertex)
	}
	writeHdlAbs(w, p.SeqendPlaceholder())
	return nil
}

// encFwdPolylinePfaceHandles POLYLINE_PFACE handle 流附加：R13~R2000 语义
// 的首/末顶点句柄 + SEQEND 占位（与 POLYLINE_2D 同构，对照
// decodePolylinePface 的 R13~R2000 分支；R2004+ 语义的 owned 向量正向
// 编码器不产生——writeDwgForwardR2000 恒为 R2000 布局）。
func encFwdPolylinePfaceHandles(w *bitstream.EncWriter, ent any, ver container.DwgVersion) error {
	p := ent.(*entity.EntPolylinePface)
	if ver == container.VerR13 || ver == container.VerR14 || ver == container.VerR2000 {
		writeHdlAbs(w, p.FirstVertex)
		writeHdlAbs(w, p.LastVertex)
	}
	writeHdlAbs(w, p.SeqendPlaceholder())
	return nil
}

// encFwdPolylinePface POLYLINE_PFACE：BS 顶点数 + BS 面数
// （对照 decodePolylinePface；顶点/面记录由 owner 归属聚合）。
func encFwdPolylinePface(w *bitstream.EncWriter, ent any, _ container.DwgVersion) error {
	p := ent.(*entity.EntPolylinePface)
	w.WriteBS(uint16(p.NumVertices))
	w.WriteBS(uint16(p.NumFaces))
	return nil
}

// encFwdPolylineMesh POLYLINE_MESH：6×BS 网格参数（R2000 无 owned 计数，
// 对照 decodePolylineMesh 的 R2000 分支）。
func encFwdPolylineMesh(w *bitstream.EncWriter, ent any, _ container.DwgVersion) error {
	m := ent.(*entity.EntPolylineMesh)
	w.WriteBS(m.Flags)
	w.WriteBS(m.CurveType)
	w.WriteBS(m.MVertexCount)
	w.WriteBS(m.NVertexCount)
	w.WriteBS(m.MDensity)
	w.WriteBS(m.NDensity)
	return nil
}

// encFwdDimension DIMENSION 公共段 + 类型专属尾部（逆 decodeDimCanonical
// 的 R2000 布局：extrusion 3BD → text_midpt 2RD → elevation BD → flag1 RC
// → user_text TV → 文本/水平角 BD → ins_scale 3BD → ins_rotation BD →
// attachment/lspace/measurement → clone_ins_pt 2RD → 专属尾部）。
// R2007+ 字符串流与 R2010+ 类版本段不写。
func encFwdDimension(w *bitstream.EncWriter, ent any, ver container.DwgVersion) error {
	d := ent.(*entity.EntDimension)
	write3BD(w, d.Extrusion)
	w.WriteRD(d.TextMidpoint.X)
	w.WriteRD(d.TextMidpoint.Y)
	w.WriteBD(d.Elevation)
	w.WriteRC(d.DimFlags)
	if ver >= container.VerR2007 {
		w.WriteTU(d.UserText)
	} else {
		w.WriteTV(d.UserText)
	}
	w.WriteBD(d.TextRotation)
	w.WriteBD(d.HorizontalDir)
	write3BD(w, d.InsertScale)
	w.WriteBD(d.InsertRotation)
	w.WriteBS(d.AttachmentPoint)
	w.WriteBS(d.LineSpacingStyle)
	w.WriteBD(d.LineSpacingFactor)
	w.WriteBD(d.ActualMeasurement)
	// clone_ins_pt：无来源时以 10 组码 def 点占位（读侧无缺省判定位）
	writeRD(w, d.InsertPoint.X)
	writeRD(w, d.InsertPoint.Y)
	writeDimSpecific(w, fwdDimensionLayout(d), d)
	return nil
}

// fwdDimensionLayout DIMENSION 实体的专属尾部布局：弧长/大半径家族按
// 实体名分派（动态类，dimFlag 低 3 位与 ANG3PT 重合），其余按输出标志。
func fwdDimensionLayout(d *entity.EntDimension) entity.DimSpecificLayout {
	switch d.TypeName {
	case "ARC_DIMENSION":
		return entity.DimLayoutArc
	case "LARGE_RADIAL_DIMENSION":
		return entity.DimLayoutLargeRadial
	}
	return fwdDimLayout(d.DimFlag)
}

// writeRD 2RD 字段别名（insertPoint 的 clone_ins_pt 2RD 编码）。
func writeRD(w *bitstream.EncWriter, v float64) { w.WriteRD(v) }

// writeDimSpecific DIMENSION 类型专属尾部（readDimSpecific 的逆过程，
// 布局逐分支对称；弧长 LARGE_RADIAL 无独立正向类不涉及）。
func writeDimSpecific(w *bitstream.EncWriter, layout entity.DimSpecificLayout, d *entity.EntDimension) {
	write3 := func(p entity.Point3) { write3BD(w, p) }
	switch layout {
	case entity.DimLayoutLinear:
		write3(d.Point13)
		write3(d.Point14)
		write3(d.Point10)
		w.WriteBD(d.ExtLineRotation)
		w.WriteBD(d.DimRotation)
	case entity.DimLayoutAligned:
		write3(d.Point13)
		write3(d.Point14)
		write3(d.Point10)
		w.WriteBD(d.ExtLineRotation)
	case entity.DimLayoutAng3Pt:
		write3(d.Point10)
		write3(d.Point13)
		write3(d.Point14)
		write3(d.Point15)
	case entity.DimLayoutAng2Ln:
		w.WriteRD(d.Point16x)
		w.WriteRD(d.P16y)
		write3(d.Point13)
		write3(d.Point14)
		write3(d.Point15)
		write3(d.Point10)
	case entity.DimLayoutOrdinate:
		write3(d.Point10)
		write3(d.Point13)
		write3(d.Point14)
		w.WriteRC(d.Flag2)
	case entity.DimLayoutRadius:
		write3(d.Point10)
		write3(d.Point15)
		w.WriteBD(d.LeaderLen)
	case entity.DimLayoutDiameter:
		write3(d.Point15)
		write3(d.Point10)
		w.WriteBD(d.LeaderLen)
	case entity.DimLayoutArc:
		write3(d.DefPt)
		write3(d.Point13)
		write3(d.Point14)
		write3(d.Point15)
		w.WriteB(d.IsPartial)
		w.WriteBD(d.ArcStartParam)
		w.WriteBD(d.ArcEndParam)
		w.WriteB(d.HasLeader)
		write3(d.Leader1Pt)
		write3(d.Leader2Pt)
	case entity.DimLayoutLargeRadial:
		write3(d.Point13)
		write3(d.Point14)
		w.WriteBD(d.ExtLineRotation)
		write3(d.Point15)
		write3(d.Point10)
	}
}

// encFwdDimensionHandles DIMENSION handle 流附加：标注样式 + 匿名块
// （顺序对照 decodeDimHandles）。
func encFwdDimensionHandles(w *bitstream.EncWriter, ent any, _ container.DwgVersion) error {
	d := ent.(*entity.EntDimension)
	writeHdlCode(w, 5, d.DimstyleHandle)
	writeHdlCode(w, 2, d.AnonymousBlock)
	return nil
}

// ---- 极限批次 E：动态类实体（类段注册 + ≥500 动态类型码） ----

// encFwdImage IMAGE/WIPEOUT（AcDbRasterImage 同布局）：类版本 + 图像三轴
// 3BD + 尺寸 2RD + 显示属性 + 裁剪位与亮度组 + 裁剪边界（类型 1 固定两角，
// 其余 BL 计数式，对照 decodeImageVer 的 R2000 分支）。imagedef 系句柄在
// handle 流附加段。
func encFwdImage(w *bitstream.EncWriter, ent any, ver container.DwgVersion) error {
	img := ent.(*entity.EntWipeout)
	w.WriteBL(img.ClassVersion)
	write3BD(w, img.Pt0)
	write3BD(w, img.Uvec)
	write3BD(w, img.Vvec)
	w.WriteRD(img.ImageSize.X)
	w.WriteRD(img.ImageSize.Y)
	w.WriteBS(img.DisplayProps)
	w.WriteB(img.Clipping)
	w.WriteRC(img.Brightness)
	w.WriteRC(img.Contrast)
	w.WriteRC(img.Fade)
	if ver >= container.VerR2010 {
		w.WriteB(img.ClipMode != 0)
	}
	w.WriteBS(img.ClipBoundaryType)
	verts := img.ClipVerts
	if img.ClipBoundaryType == 1 {
		// 矩形边界固定两角：读侧不读计数
		if len(verts) > 2 {
			verts = verts[:2]
		}
		for len(verts) < 2 {
			verts = append(verts, entity.Point2{})
		}
		for _, p := range verts {
			w.WriteRD(p.X)
			w.WriteRD(p.Y)
		}
		return nil
	}
	w.WriteBL(uint32(len(verts)))
	for _, p := range verts {
		w.WriteRD(p.X)
		w.WriteRD(p.Y)
	}
	return nil
}

// encFwdImageHandles IMAGE handle 流附加：图像定义（code 5）+ 定义反应器
// （code 3），顺序对照 decodeImageVer；无来源时写空引用保持流结构。
func encFwdImageHandles(w *bitstream.EncWriter, ent any, _ container.DwgVersion) error {
	img := ent.(*entity.EntWipeout)
	writeHdlCode(w, 5, 0)
	writeHdlCode(w, 3, 0)
	_ = img
	return nil
}

// encFwdMLeader MULTILEADER（缺省上下文形态）：结构化来源不携带引线
// 上下文（leaders 空、无文字/块内容），写出空引线数组 + 上下文标量段 +
// 主体尾段（对照 decodeMLeader 的 R2000 分支：R2010+ 类版本段与
// R2007+ 字符串流不写；颜色 CMC 为 R2004 前的 BS 索引形态）。
func encFwdMLeader(w *bitstream.EncWriter, ent any, _ container.DwgVersion) error {
	m := ent.(*entity.EntMLeader)
	// 上下文：空引线 + 全缺省标量（读侧 decodeMLeaderLeaders/Context 逐字段消费）
	w.WriteBL(0)                 // num_leaders
	w.WriteBD(0)                 // ctx.scale
	write3BD(w, entity.Point3{}) // content_base
	w.WriteBD(0)                 // ctx.text_height
	w.WriteBD(0)                 // ctx.arrow_size
	w.WriteBD(0)                 // landing_gap
	w.WriteBS(0)                 // text_left
	w.WriteBS(0)                 // text_right
	w.WriteBS(0)                 // text_angletype
	w.WriteBS(0)                 // text_alignment
	w.WriteB(false)              // has_content_txt
	w.WriteB(false)              // has_content_blk
	write3BD(w, entity.Point3{}) // base
	write3BD(w, entity.Point3{}) // base_dir
	write3BD(w, entity.Point3{}) // base_vert
	w.WriteB(false)              // is_normal_reversed：JSON 来源未建模
	// 主体尾段
	w.WriteBL(m.Flags)
	w.WriteBS(m.MleaderType)
	writeMLeaderCMCIndex(w)
	w.WriteBL(uint32(m.LineLinewt))
	w.WriteB(m.HasLanding)
	w.WriteB(m.HasDogleg)
	w.WriteBD(m.LandingDist)
	w.WriteBD(m.ArrowSize)
	w.WriteBS(m.StyleContent)
	w.WriteBS(m.TextLeft)
	w.WriteBS(m.TextRight)
	w.WriteBS(m.TextAngletype)
	w.WriteBS(m.TextAlignment)
	writeMLeaderCMCIndex(w) // text_color
	w.WriteB(m.HasTextFrame)
	writeMLeaderCMCIndex(w)      // block_color
	write3BD(w, entity.Point3{}) // block_scale
	w.WriteBD(m.BlockRotation)
	w.WriteBS(m.StyleAttachment)
	w.WriteB(m.IsAnnotative)
	// VERSIONS(R_14, R_2007) 段（R2000 在内）
	w.WriteBL(0) // num_arrowheads
	w.WriteBL(0) // num_blocklabels
	w.WriteB(m.IsNegTextdir)
	w.WriteBS(m.IpeAlignment)
	w.WriteBS(m.Justification)
	w.WriteBD(m.ScaleFactor)
	return nil
}

// writeMLeaderCMCIndex 空颜色 CMC（R2004 前形态：BS 索引 0，ByLayer）。
func writeMLeaderCMCIndex(w *bitstream.EncWriter) { w.WriteBS(0) }

// encFwdMLeaderHandles MULTILEADER handle 流附加：多线样式/箭头/文字样式/
// 块样式/线型五引用（顺序对照 decodeMLeader 尾部；空引线上下文时
// leaders/content 系列无句柄）。
func encFwdMLeaderHandles(w *bitstream.EncWriter, ent any, _ container.DwgVersion) error {
	m := ent.(*entity.EntMLeader)
	writeHdlCode(w, 5, m.MleaderStyle)
	writeHdlCode(w, 5, m.ArrowHandle)
	writeHdlCode(w, 5, m.TextStyle)
	writeHdlCode(w, 5, m.BlockStyle)
	writeHdlCode(w, 5, m.LineLtype)
	return nil
}

// encFwdLight LIGHT（R2000 基线布局，光度子段不写）：类版本 + 名称 +
// 类型/状态/颜色索引 + 强度与位置/目标 + 衰减段 + 阴影段
// （对照 decodeLight 的非 CMC 分支）。
func encFwdLight(w *bitstream.EncWriter, ent any, ver container.DwgVersion) error {
	l := ent.(*entity.EntLight)
	w.WriteBL(l.ClassVersion)
	if ver >= container.VerR2007 {
		w.WriteTU(l.Name)
	} else {
		w.WriteTV(l.Name)
	}
	w.WriteBL(l.LightType)
	w.WriteB(l.Status)
	if ver >= container.VerR2004 {
		// CMC 结构（BS 索引 + BL rgb + RC flag）：结构化来源仅索引
		w.WriteBS(l.LightColorIndex)
		w.WriteBL(l.LightColorRGB)
		w.WriteRC(l.LightColorFlag)
	} else {
		w.WriteBS(l.LightColorIndex)
	}
	w.WriteB(l.PlotGlyph)
	w.WriteBD(l.Intensity)
	write3BD(w, l.Position)
	write3BD(w, l.Target)
	w.WriteBL(l.AttenuationType)
	w.WriteB(l.UseAttenuationLimits)
	w.WriteBD(l.AttenuationStart)
	w.WriteBD(l.AttenuationEnd)
	w.WriteBD(l.HotspotAngle)
	w.WriteBD(l.FalloffAngle)
	w.WriteB(l.CastShadows)
	w.WriteBL(l.ShadowType)
	w.WriteBS(l.ShadowMapSize)
	w.WriteRC(uint8(l.ShadowMapSoftness))
	return nil
}

// ---- 对象侧正向编码（最小集） ----

// encodeForwardLayerBody LAYER 表记录（对照 parseLayerHeaderPreR2004 的
// R2000 dat 流 + LibreDWG COMMON_TABLE_FLAGS 的 handle 流）：RL bitsize +
// H + EED + BL reactors + TV 名称 + xref 标志组 + flag0 位包 + BS 颜色
// 索引；handle 流 = owner → xdic → xref → plotstyle → ltype（后三者为
// 表记录公共句柄，结构化重建以空引用占位）。
func encodeForwardLayerBody(handle uint64, lc drawing.LayerColor, owner uint64) ([]byte, error) {
	idx := lc.Index
	if lc.HasTrue {
		idx = nearestACI(lc.TrueColor)
	}
	name := lc.Name
	if name == "" {
		name = fmt.Sprintf("LAYER_%X", handle)
	}
	w := bitstream.NewEncWriter()
	w.WriteBS(uint16(0x33))
	rlOff := w.TellBits()
	w.WriteRL(0)
	writeHdlSelf(w, handle)
	w.WriteBS(0) // EED 终止
	w.WriteBL(0) // num_reactors
	w.WriteTV(name)
	w.WriteB(false) // is_xref_ref
	w.WriteBS(1)    // is_xref_resolved
	w.WriteB(false) // is_xref_dep
	w.WriteBS(0)    // flag0 位包（frozen/off/locked 等合成缺省）
	w.WriteBS(idx)  // CMC（R2004 前形态：BS 索引）
	patchRL(w, rlOff, uint32(w.TellBits()))
	writeHdlCode(w, 4, owner) // ownerhandle
	writeHdlNull(w)           // xdicobjhandle
	writeHdlNull(w)           // xref
	writeHdlNull(w)           // plotstyle
	writeHdlNull(w)           // ltype
	w.AlignByte()
	return w.Bytes(), nil
}

// writeHdlCode 指定 code 的句柄引用（表记录 owner=4、entries=2 等语义码）。
func writeHdlCode(w *bitstream.EncWriter, code uint8, h uint64) {
	if h == 0 {
		w.WriteH(code, 0, 0)
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
	w.WriteH(code, counter, h)
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
	w := bitstream.NewEncWriter()
	w.WriteBS(tc.typeCode)
	rlOff := w.TellBits()
	w.WriteRL(0)
	writeHdlSelf(w, tc.handle)
	w.WriteBS(0) // EED 终止
	w.WriteBL(0) // num_reactors（对象公共头字段）
	switch tc.typeCode {
	case 0x38: // LTYPE_CONTROL：num_entries 为 BS
		w.WriteBS(uint16(len(tc.entries)))
	case 0x44: // DIMSTYLE_CONTROL：尾部 RC num_morehandles
		w.WriteBL(uint32(len(tc.entries)))
		w.WriteRC(0)
	default:
		w.WriteBL(uint32(len(tc.entries)))
	}
	patchRL(w, rlOff, uint32(w.TellBits()))
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
	w.AlignByte()
	return w.Bytes(), nil
}

// forwardBlockHeader 块头骨架描述：句柄、名称、基点与首末实体句柄。
type forwardBlockHeader struct {
	handle      uint64
	name        string
	basePt      entity.Point3
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
	w := bitstream.NewEncWriter()
	w.WriteBS(uint16(0x31))
	rlOff := w.TellBits()
	w.WriteRL(0)
	writeHdlSelf(w, bh.handle)
	w.WriteBS(0) // EED 终止
	w.WriteBL(0) // num_reactors
	w.WriteTV(bh.name)
	w.WriteB(false) // is_xref_ref
	w.WriteBS(1)    // is_xref_resolved
	w.WriteB(false) // is_xref_dep
	w.WriteB(false) // anonymous
	w.WriteB(false) // hasattrs
	w.WriteB(false) // blkisxref
	w.WriteB(false) // xrefoverlaid
	w.WriteB(false) // xref_loaded（R2000b 位）
	write3BD(w, bh.basePt)
	w.WriteTV("") // xref_pname
	w.WriteRC(0)  // num_inserts：RC 计数终止式（0 = 空）
	w.WriteTV("") // description
	w.WriteBL(0)  // preview_size（无预览位串）
	patchRL(w, rlOff, uint32(w.TellBits()))
	writeHdlCode(w, 4, 0) // ownerhandle
	writeHdlNull(w)       // xdicobjhandle
	writeHdlNull(w)       // xref
	writeHdlCode(w, 3, blockEnt)
	writeHdlCode(w, 4, bh.firstEntity)
	writeHdlCode(w, 4, bh.lastEntity)
	writeHdlCode(w, 3, endblkEnt)
	w.AlignByte()
	return w.Bytes(), nil
}

// encodeForwardDictionaryBody DICTIONARY 对象（对照 decodeDictionaryObject
// 的 R2000 dat 流）：reactors 原值保留但句柄以空引用占位（解码侧不保留
// reactor 句柄列表，结构化重建以数量守恒为准）。
func encodeForwardDictionaryBody(d *object.ObjDictionary, ver container.DwgVersion) ([]byte, error) {
	w := bitstream.NewEncWriter()
	w.WriteBS(uint16(0x2A))
	rlOff := w.TellBits()
	w.WriteRL(0)
	writeHdlSelf(w, d.Handle)
	if len(d.EedFields) != 0 {
		if err := encodeEEDFields(w, d.EedFields); err != nil {
			return nil, err
		}
	} else {
		w.WriteBS(0)
	}
	w.WriteBL(uint32(d.NumReactors))
	w.WriteBL(uint32(d.NumItems))
	w.WriteBS(d.Cloning)
	if d.IsHardOwner {
		w.WriteRC(1)
	} else {
		w.WriteRC(0)
	}
	for _, s := range d.Texts {
		if ver >= container.VerR2007 {
			w.WriteTU(s)
		} else {
			w.WriteTV(s)
		}
	}
	patchRL(w, rlOff, uint32(w.TellBits()))
	writeHdlAbs(w, d.Owner)
	for i := 0; i < d.NumReactors; i++ {
		writeHdlNull(w)
	}
	writeHdlNull(w)
	for _, h := range d.ItemHandles {
		writeHdlAbs(w, h)
	}
	w.AlignByte()
	return w.Bytes(), nil
}

// ---- 文件级组装 ----

// writeDwgForwardR2000 将无回放素材的 drawing.Document 组装为 R2000 容器 DWG
// 字节流。对象图按句柄升序铺放（差分编码要求单调），布局为：头部 0x15
// → 段目录（3 条目 + CRC + 哨兵）→ HeaderVars 段（模板回放）→ Classes
// 段（空类表：固定码实体无动态类）→ 对象区 → 对象图。
func writeDwgForwardR2000(doc *drawing.Document) ([]byte, error) {
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
	hasEntities := len(doc.ModelSpace) > 0 || len(doc.Blocks) > 0 || len(doc.Attribs) > 0 || len(doc.ByHandle) > 0
	if !hasEntities && len(doc.LayerColors) == 0 {
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
	dirEnd := uint64(0x15 + 4 + 3*9 + 2 + len(container.R2000LocatorSentinel))
	hvOff := dirEnd
	clsOff := hvOff + uint64(len(hvSec))
	objBase := clsOff + uint64(len(clsSec))
	blob := make([]byte, 0, len(objs)*24)
	refs := make([]objrec.ObjectRef, 0, len(objs))
	for _, o := range objs {
		rec, err := buildForwardObjectRecord(o)
		if err != nil {
			return nil, fmt.Errorf("cad: 对象 %d 记录组装失败: %w", o.handle, err)
		}
		refs = append(refs, objrec.ObjectRef{Handle: o.handle, Offset: uint32(objBase + uint64(len(blob)))})
		blob = append(blob, rec...)
	}
	mapPayload := buildR2000ObjectMap(refs, 0)
	mapOff := objBase + uint64(len(blob))
	if mapOff+uint64(len(mapPayload)) > 0xFFFFFFFF {
		return nil, fmt.Errorf("cad: 正向写出生成体积超过 4GB 布局上限")
	}
	// 文件头 + 段目录
	w := bitstream.NewEncWriter()
	hdr := forwardR2000FixedHeader
	cp := uint16(30)
	if doc.Codepage != 0 {
		cp = doc.Codepage
	}
	hdr[0x13] = uint8(cp)
	hdr[0x14] = uint8(cp >> 8)
	w.WriteTF(hdr[:])
	w.WriteRL(3)
	w.WriteRC(container.R2000SecHeaderVars)
	w.WriteRL(uint32(hvOff))
	w.WriteRL(uint32(len(hvSec)))
	w.WriteRC(container.R2000SecClasses)
	w.WriteRL(uint32(clsOff))
	w.WriteRL(uint32(len(clsSec)))
	w.WriteRC(container.R2000SecObjectMap)
	w.WriteRL(uint32(mapOff))
	w.WriteRL(uint32(len(mapPayload)))
	w.WriteCRCSeed(0, 0xC0C1)
	w.WriteTF(container.R2000LocatorSentinel[:])
	// 段数据：HeaderVars 模板 → Classes → 对象区 → 对象图
	w.WriteTF(hvSec)
	w.WriteTF(clsSec)
	w.WriteTF(blob)
	w.WriteTF(mapPayload)
	return w.Bytes(), nil
}

// allocateForwardDynamicClasses 扫描文档实体与通用对象，为出现的动态
// 类按注册表顺序分配 ≥500 的类型码（实体类在前、对象类在后，同一计数
// 器连续分配）。返回有序类名与 名称→码 映射。
func allocateForwardDynamicClasses(doc *drawing.Document) ([]string, map[string]uint16) {
	present := map[string]bool{}
	visit := func(list []any) {
		for _, ent := range list {
			if name := fwdDynamicEntityKind(ent); name != "" {
				present[name] = true
			}
		}
	}
	visit(doc.ModelSpace)
	visit(doc.PspaceSpace)
	for _, list := range doc.Blocks {
		visit(list)
	}
	for _, a := range doc.Attribs {
		if a != nil {
			if name := fwdDynamicEntityKind(a); name != "" {
				present[name] = true
			}
		}
	}
	if doc.ByHandle != nil {
		for _, ent := range doc.ByHandle {
			if name := fwdDynamicEntityKind(ent); name != "" {
				present[name] = true
			}
		}
	}
	for h, g := range doc.InternalObjs {
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

// collectForwardObjects 从 drawing.Document 收集全部可写对象：实体（模型空间 +
// 块定义 + 属性）经正向实体编码器，LAYER 表记录与 DICTIONARY 经对象侧
// 编码器。无正向编码器或无句柄的实体跳过（能力边界，见报告）。
// dyn 为动态类名 → 类型码映射（无动态类实体时可为 nil）。
func collectForwardObjects(doc *drawing.Document, dyn map[string]uint16) ([]fwdObject, error) {
	out := make([]fwdObject, 0, len(doc.ByHandle)+len(doc.LayerColors)+8)
	seen := map[uint64]bool{}
	add := func(handle uint64, body []byte) {
		if handle == 0 || seen[handle] {
			return
		}
		seen[handle] = true
		out = append(out, fwdObject{handle: handle, body: body})
	}
	entities := make([]any, 0, len(doc.ByHandle)+8)
	entities = append(entities, doc.ModelSpace...)
	entities = append(entities, doc.PspaceSpace...)
	for _, list := range doc.Blocks {
		entities = append(entities, list...)
	}
	for _, a := range doc.Attribs {
		entities = append(entities, a)
	}
	if doc.ByHandle != nil {
		for _, ent := range doc.ByHandle {
			entities = append(entities, ent)
		}
	}
	for _, ent := range entities {
		b := entity.EntityBase(ent)
		if b == nil || b.Handle == 0 {
			continue
		}
		if seen[b.Handle] {
			continue
		}
		kind, code := fwdEntityKind(ent)
		if code == 0 && kind == "" {
			continue // 无编码器：能力边界外实体
		}
		body, err := encodeForwardEntityBody(ent, container.VerR2000, dyn)
		if err != nil {
			return nil, fmt.Errorf("cad: 实体 %s(%d) 编码失败: %w", b.TypeName, b.Handle, err)
		}
		add(b.Handle, body)
	}
	// LAYER 表记录（渲染必需：颜色与名称）
	for h, lc := range doc.LayerColors {
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
	for h, g := range doc.InternalObjs {
		if g == nil || seen[h] {
			continue
		}
		if _, ok := gfWriters[g.Name]; !ok {
			continue // 无 gfWrite 编码器的类型：能力边界外
		}
		body, err := encodeForwardGenericObject(g, container.VerR2000, dyn)
		if err != nil {
			return nil, fmt.Errorf("cad: 通用对象 %s(%d) 编码失败: %w", g.Name, h, err)
		}
		add(h, body)
	}
	// DICTIONARY（结构化正向；JSON 来源的 objGeneric 形式字典不在范围）
	for h, d := range doc.Dictionaries {
		if d == nil || seen[h] {
			continue
		}
		body, err := encodeForwardDictionaryBody(d, container.VerR2000)
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
func appendForwardSkeleton(doc *drawing.Document, out *[]fwdObject, seen map[uint64]bool) {
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
	for h, list := range doc.Blocks {
		if h == 0 {
			continue
		}
		bd := blkDef{handle: allocate(h), name: ""}
		if g := doc.InternalObjs[h]; g != nil {
			if n, ok := g.Field("name").(string); ok {
				bd.name = n
			}
		}
		if bd.name == "" {
			bd.name = fmt.Sprintf("*B_%X", h)
		}
		for _, ent := range list {
			b := entity.EntityBase(ent)
			if b == nil || b.Handle == 0 {
				continue
			}
			bd.ents = append(bd.ents, b.Handle)
			switch b.TypeName {
			case "BLOCK":
				bd.blockEnt = b.Handle
			case "ENDBLK":
				bd.endblkEnt = b.Handle
			}
		}
		blocks = append(blocks, bd)
	}
	// *MODEL_SPACE：模型空间直属实体（含最大块启发式并入由读侧处理，
	// 此处仅挂 modelSpace 列表）
	ms := blkDef{handle: allocate(fwdHdlModelSpace), name: "*Model_Space"}
	for _, ent := range doc.ModelSpace {
		if b := entity.EntityBase(ent); b != nil && b.Handle != 0 {
			ms.ents = append(ms.ents, b.Handle)
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
	layerEntries := make([]uint64, 0, len(doc.LayerColors))
	for h := range doc.LayerColors {
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
	crc := bitstream.Crc16DWG(0xC0C1, out[:msLen+len(o.body)])
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
	w := bitstream.NewEncWriter()
	w.WriteTF(container.SentinelClassesBefore[:])
	sizePos := w.TellBits()
	if len(entries) == 0 {
		w.WriteRL(0)
		w.WriteBS(0) // num_classes = 0
		w.WriteRC(0) // 占位字节
		w.AlignByte()
		sizeEnd := w.TellBits()
		patchRL(w, sizePos, uint32((sizeEnd-sizePos-32)/8))
		w.WriteCRCSeed(sizePos, 0xC0C1)
		w.WriteTF(container.SentinelClassesAfter[:])
		return w.Bytes()
	}
	dataStart := w.TellBits()
	w.WriteRL(0) // dataSize 占位（纯条目区字节长，不含 RL/CRC，含尾部对齐）
	dataStart = w.TellBits()
	for _, e := range entries {
		w.WriteBS(e.classNumber)
		w.WriteBS(0) // proxy flags：非代理
		w.WriteTV("ObjectDBX Classes")
		w.WriteTV(e.cppName)
		w.WriteTV(e.dxfName)
		w.WriteB(false) // zombie
		if e.isEntity {
			w.WriteBS(0x1F2)
		} else {
			w.WriteBS(0x1F3)
		}
	}
	padBits := (8 - (w.TellBits()-dataStart)%8) % 8
	for i := uint64(0); i < padBits; i++ {
		w.WriteB(false)
	}
	patchRL(w, sizePos, uint32((w.TellBits()-dataStart)/8))
	w.WriteCRCSeed(sizePos, 0xC0C1)
	w.WriteTF(container.SentinelClassesAfter[:])
	return w.Bytes()
}
