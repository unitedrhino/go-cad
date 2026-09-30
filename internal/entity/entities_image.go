// entities_image.go 实现批次 C 的 spec 级扩展实体：IMAGE（AcDbRasterImage，
// 位布局与 WIPEOUT 完全同构）、OLE2FRAME/OLEFRAME（OLE 框架 + 二进制数据块）、
// PROXY_ENTITY（ACAD 代理实体：元数据 + hdlpos 定界的原始数据位捕获）。
// 位级布局对照 LibreDWG dwg.spec 对应 DWG_ENTITY 定义；
// IMAGE 布局经 dwgread -v9 对 test-data 各版本 Leader.dwg 现场核对。
package entity

import (
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/objrec"
)

// entImage 栅格图像实体（AcDbRasterImage）。
type EntImage struct {
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
	ImageDef         uint64 // IMAGEDEF 硬指针句柄（handle 流，code 340）
	ImageDefReactor  uint64 // IMAGEDEF_REACTOR 硬属主句柄（handle 流，code 360）
}

// decodeImageVer IMAGE（R13+ 动态类，位布局与 WIPEOUT 同构）：
// class_version BL + pt0/uvec/vvec 3BD + image_size 2RD + display_props BS +
// clipping B + 亮度/对比/淡出 RC + [clip_mode B（R2010+）] +
// clip_boundary_type BS + 裁剪顶点 2RD 数组（boundary_type=1 固定两角）。
// handle 流：公共序列后接 imagedef(340) 与 imagedefreactor(360)
// （dwgread trace 核对：hdl 序列 reactors/xdic/prev/next/layer/imagedef/
// imagedefreactor，主体字段按 dat 流独立推进）。主体后的未记载位
// （padding 等）按 objSizeBit 截断跳过。
func decodeImageVer(r *bitstream.BitStream, Head *CommonEntityHead, ver container.DwgVersion) (any, error) {
	img := &EntImage{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}}
	var err error
	if img.ClassVersion, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if img.ClassVersion > 10 {
		return nil, fmt.Errorf("cad: IMAGE class_version 异常 %d", img.ClassVersion)
	}
	if img.Pt0, err = read3pt(r); err != nil {
		return nil, err
	}
	if img.Uvec, err = read3pt(r); err != nil {
		return nil, err
	}
	if img.Vvec, err = read3pt(r); err != nil {
		return nil, err
	}
	if img.ImageSize.X, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if img.ImageSize.Y, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if img.DisplayProps, err = r.ReadBS(); err != nil {
		return nil, err
	}
	var v uint8
	if v, err = r.ReadB(); err != nil {
		return nil, err
	}
	img.Clipping = v != 0
	if img.Brightness, err = r.ReadRC(); err != nil {
		return nil, err
	}
	if img.Contrast, err = r.ReadRC(); err != nil {
		return nil, err
	}
	if img.Fade, err = r.ReadRC(); err != nil {
		return nil, err
	}
	if ver >= container.VerR2010 {
		cm, err2 := r.ReadB() // clip_mode（R2010+）
		if err2 != nil {
			return nil, err2
		}
		img.ClipMode = uint8(cm)
	}
	if img.ClipBoundaryType, err = r.ReadBS(); err != nil {
		return nil, err
	}
	numVerts := uint32(2) // 矩形边界固定两角
	if img.ClipBoundaryType != 1 {
		if numVerts, err = r.ReadBL(); err != nil {
			return nil, err
		}
	}
	if numVerts > 100_000 {
		return nil, fmt.Errorf("cad: IMAGE 裁剪顶点数异常 %d", numVerts)
	}
	for i := uint32(0); i < numVerts; i++ {
		var P Point2
		if P.X, err = r.ReadRD(); err != nil {
			return nil, err
		}
		if P.Y, err = r.ReadRD(); err != nil {
			return nil, err
		}
		img.ClipVerts = append(img.ClipVerts, P)
	}
	Owner, Layer := decodeOwnerLayer(r, Head)
	img.Owner, img.Layer = Owner, Layer
	// handle 流：owner/layer 之后的公共序列后为 imagedef(5) 与 imagedefreactor(3)
	r.SetBitPos(Head.ObjSizeBit)
	if _, layer2, e := ParseCommonEntityHandles(r, Head); e == nil {
		img.Layer = layer2
	}
	if h, e := objrec.ReadHandleReference(r, Head.Handle); e == nil && h != 0 {
		img.ImageDef = h
	}
	if h, e := objrec.ReadHandleReference(r, Head.Handle); e == nil && h != 0 {
		img.ImageDefReactor = h
	}
	return img, nil

}

// ---- OLE2FRAME / OLEFRAME（OLE 对象框架，含二进制数据块）----

// oleDataMaxSize OLE 数据块的防御性上限（错位候选的 data_size 常为天文数；
// 真实 OLE 数据最大为嵌入了完整 OLE 流的记录，百 MB 级不可能出现）。
const oleDataMaxSize = 64 << 20

// entOle2Frame OLE2 框架实体（AcDbOle2Frame，固定类型码 0x4A）。
type EntOle2Frame struct {
	BaseEntity
	OleType    uint16 // type（BS 71）：1=Link 2=Embedded 3=Static
	Mode       uint16 // mode/tile_mode（BS 72，R2000b+）：0=mspace 1=pspace
	DataSize   uint32 // data_size（BL 90）
	Data       []byte // OLE 二进制数据（FIELD_BINARY=TF，任意位对齐字节串）
	LockAspect uint8  // lock_aspect（RC，R2000b+ 主体尾部）
}

// entOleFrame OLE 1.0 框架实体（pre-R13c4，固定类型码 0x2B；
// 打开时按需转换为 OLE2FRAME）。
type EntOleFrame struct {
	BaseEntity
	Flag     uint16 // flag（BS 70）
	Mode     uint16 // mode（BS，R2000b+）
	DataSize uint32 // data_size（BL 90）
	Data     []byte // OLE 二进制数据（TF）
}

// decodeOle2FrameVer OLE2FRAME：type BS + [mode BS（R2000b+）] + data_size BL +
// data TF + [lock_aspect RC（R2000b+）]（dwg.spec DWG_ENTITY (OLE2FRAME)）。
// 注意 dwgVersion 枚举按容器路径排序（verR2000=0），R2000b+ 判定用
// 「非 R13/R14」口径而非大小比较。
func DecodeOle2FrameVer(r *bitstream.BitStream, Head *CommonEntityHead, ver container.DwgVersion) (any, error) {
	ole := &EntOle2Frame{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}}
	var err error
	if ole.OleType, err = r.ReadBS(); err != nil {
		return nil, err
	}
	r2000b := ver != container.VerR13 && ver != container.VerR14
	if r2000b {
		if ole.Mode, err = r.ReadBS(); err != nil {
			return nil, err
		}
	}
	if ole.DataSize, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if ole.DataSize > oleDataMaxSize {
		return nil, fmt.Errorf("cad: OLE2FRAME data_size 异常 %d", ole.DataSize)
	}
	if ole.Data, err = r.ReadRCS(int(ole.DataSize)); err != nil {
		return nil, err
	}
	if r2000b {
		if ole.LockAspect, err = r.ReadRC(); err != nil {
			return nil, err
		}
	}
	Owner, Layer := decodeOwnerLayer(r, Head)
	ole.Owner, ole.Layer = Owner, Layer
	return ole, nil
}

// ---- PROXY_ENTITY（ACAD 代理实体，固定类型码 0x1F2）----

// proxyDataMaxBits 原始代理数据位捕获的防御性上限（错位候选的
// objSizeBit 垃圾值会产生天文位长；真实代理数据上限为数 MB）。
const ProxyDataMaxBits = 64 << 23

// entProxyEntity ACAD 代理实体（第三方应用创建的自定义实体，
// 几何由宿主应用解释；本库保留元数据与原始数据位供审计/回写）。
type EntProxyEntity struct {
	BaseEntity
	ProxyID       uint32 // proxy_id（BL 90，恒 499）
	Version       uint32 // version（BLx 95，PRE R2018）：高 8 位 maint、低 8 位 dwg
	MaintVersion  uint32
	DwgVersionNum uint32
	FromDxf       bool   // Original Data Format（B 70，R2000b+）：0=dwg 1=dxf
	ProxyDataSize uint32 // = 公共头 preview_size（spec：proxy_data_size 即 preview_size）
	ProxyData     []byte // 代理图形数据（TF）
	DataNumBits   uint32 // 主体结束到 hdlpos 之间的原始位长（DECODER data_numbits）
	Data          []byte // 原始数据位（MSB 序，bit_read_bits 语义）
	NumObjids     uint32 // handle 流 common 序列之后的剩余句柄数
	objids        []uint64
}

// decodeProxyEntityVer PROXY_ENTITY：proxy_id BL + [version BLx（PRE R2018）
// 或 dwg/maint BLx 对（R2018+）] + from_dxf B（R2000b+）+ proxy_data TF
// （长度取公共头 preview_size）+ 原始数据位捕获（当前位置到 hdlpos 的全部
// 位，即 LibreDWG DECODER 的 data_numbits/data）+ handle 流剩余句柄全量
// 记为 objids（LibreDWG num_objids 循环口径）。
func decodeProxyEntityVer(r *bitstream.BitStream, Head *CommonEntityHead, dataEnd uint64, ver container.DwgVersion) (any, error) {
	px := &EntProxyEntity{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}}
	var err error
	if px.ProxyID, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if ver >= container.VerR2018 {
		if px.DwgVersionNum, err = r.ReadBL(); err != nil {
			return nil, err
		}
		if px.MaintVersion, err = r.ReadBL(); err != nil {
			return nil, err
		}
	} else {
		if px.Version, err = r.ReadBL(); err != nil {
			return nil, err
		}
		px.MaintVersion = px.Version >> 8
		px.DwgVersionNum = px.Version & 0xFF
	}
	if ver != container.VerR13 && ver != container.VerR14 {
		var v uint8
		if v, err = r.ReadB(); err != nil {
			return nil, err
		}
		px.FromDxf = v != 0
	}
	// proxy_data_size 即公共头 preview_size（spec DXF_OR_PRINT else 分支）
	px.ProxyDataSize = uint32(len(Head.Preview))
	if px.ProxyDataSize > 0 {
		if px.ProxyData, err = r.ReadRCS(int(px.ProxyDataSize)); err != nil {
			return nil, err
		}
	}
	// 原始数据位捕获：当前位置到 hdlpos（head.objSizeBit）的全部位
	if pos := r.TellBits(); Head.ObjSizeBit > pos && Head.ObjSizeBit <= dataEnd {
		n := Head.ObjSizeBit - pos
		if n > ProxyDataMaxBits {
			return nil, fmt.Errorf("cad: PROXY_ENTITY data 位长异常 %d", n)
		}
		px.DataNumBits = uint32(n)
		if px.Data, err = r.ReadBitsBytes(int((n + 7) / 8)); err != nil {
			return nil, err
		}
	}
	Owner, Layer := decodeOwnerLayer(r, Head)
	px.Owner, px.Layer = Owner, Layer
	// handle 流剩余句柄全量收集（LibreDWG while(hdl_dat->byte < hdl_dat->size) 口径，
	// 含尾部 CRC 字节被当作句柄的差异，与参考实现一致）
	r.SetBitPos(Head.ObjSizeBit)
	if _, _, e := ParseCommonEntityHandles(r, Head); e == nil {
		for r.TellBits()+8 <= dataEnd {
			h, e := objrec.ReadHandleReference(r, Head.Handle)
			if e != nil {
				break
			}
			px.NumObjids++
			px.objids = append(px.objids, h)
		}
	}
	return px, nil
}

// decodeOleFrameVer OLEFRAME：flag BS + [mode BS（R2000b+）] + data_size BL +
// data TF（dwg.spec DWG_ENTITY (OLEFRAME)）。
func DecodeOleFrameVer(r *bitstream.BitStream, Head *CommonEntityHead, ver container.DwgVersion) (any, error) {
	ole := &EntOleFrame{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}}
	var err error
	if ole.Flag, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if ver != container.VerR13 && ver != container.VerR14 {
		if ole.Mode, err = r.ReadBS(); err != nil {
			return nil, err
		}
	}
	if ole.DataSize, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if ole.DataSize > oleDataMaxSize {
		return nil, fmt.Errorf("cad: OLEFRAME data_size 异常 %d", ole.DataSize)
	}
	if ole.Data, err = r.ReadRCS(int(ole.DataSize)); err != nil {
		return nil, err
	}
	Owner, Layer := decodeOwnerLayer(r, Head)
	ole.Owner, ole.Layer = Owner, Layer
	return ole, nil
}

// entUnderlay 底图引用实体（PDFUNDERLAY/DGNUNDERLAY/DWFUNDERLAY 共用
// AcDbUnderlayReference 布局，dwg2.spec UNDERLAY_fields；字段序经
// 2004/Underlay.dwg dwgread -v9 trace 现场核对：definition_id 位于
// handle 流区，主体为 extrusion→ins_pt→angle→scale→flag→contrast→fade
// →clip 顶点）。几何来自外部 PDF/DGN/DWF 文件，DWG 内仅存引用框变换
// 与裁剪多边形。
type EntUnderlay struct {
	BaseEntity
	DefinitionID uint64   // PDFDEFINITION/DGNDEFINITION/DWFDEFINITION 硬指针（handle 流）
	Extrusion    Point3   // 挤出方向（3BD）
	InsPt        Point3   // 插入点（3RD）
	Angle        float64  // 旋转角（BD）
	Scale        Point3   // 三轴缩放（3BD）
	Flag         uint8    // 状态标志（显示/裁剪相关位）
	Contrast     int8     // 对比度 20~100（RCd 有符号）
	Fade         int8     // 淡出 0~80（RCd 有符号）
	ClipVerts    []Point2 // 裁剪多边形顶点（2RD）
}

// decodeUnderlayVer 底图引用解码（三个 UNDERLAY 类共用布局）。
func decodeUnderlayVer(r *bitstream.BitStream, Head *CommonEntityHead) (any, error) {
	u := &EntUnderlay{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}}
	trOn := cadTraceHandle != 0 && cadTraceHandle == Head.Handle
	var err error
	pos := r.TellBits()
	if u.Extrusion, err = read3pt(r); err != nil {
		return nil, err
	}
	if trOn {
		CadTraceField(trOn, pos, r.TellBits(), "extrusion", fmt.Sprintf("(%v,%v,%v)", u.Extrusion.X, u.Extrusion.Y, u.Extrusion.Z))
	}
	pos = r.TellBits()
	if u.InsPt, err = read3pt(r); err != nil {
		return nil, err
	}
	if trOn {
		CadTraceField(trOn, pos, r.TellBits(), "ins_pt", fmt.Sprintf("(%v,%v,%v)", u.InsPt.X, u.InsPt.Y, u.InsPt.Z))
	}
	pos = r.TellBits()
	if u.Angle, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if trOn {
		CadTraceField(trOn, pos, r.TellBits(), "angle", fmt.Sprintf("%v", u.Angle))
	}
	pos = r.TellBits()
	if u.Scale, err = read3pt(r); err != nil {
		return nil, err
	}
	if trOn {
		CadTraceField(trOn, pos, r.TellBits(), "scale", fmt.Sprintf("(%v,%v,%v)", u.Scale.X, u.Scale.Y, u.Scale.Z))
	}
	pos = r.TellBits()
	if u.Flag, err = r.ReadRC(); err != nil {
		return nil, err
	}
	cadTraceFieldInt(trOn, pos, r.TellBits(), "flag", int64(u.Flag))
	pos = r.TellBits()
	var c, f uint8
	if c, err = r.ReadRC(); err != nil {
		return nil, err
	}
	u.Contrast = int8(c)
	cadTraceFieldInt(trOn, pos, r.TellBits(), "contrast", int64(u.Contrast))
	pos = r.TellBits()
	if f, err = r.ReadRC(); err != nil {
		return nil, err
	}
	u.Fade = int8(f)
	cadTraceFieldInt(trOn, pos, r.TellBits(), "fade", int64(u.Fade))
	pos = r.TellBits()
	var numClip uint32
	if numClip, err = r.ReadBL(); err != nil {
		return nil, err
	}
	cadTraceFieldInt(trOn, pos, r.TellBits(), "num_clip_verts", int64(numClip))
	if numClip > 5000 { // VALUEOUTOFBOUNDS(5000) 对齐
		return nil, fmt.Errorf("cad: UNDERLAY 裁剪顶点数异常 %d", numClip)
	}
	for i := uint32(0); i < numClip; i++ {
		var P Point2
		if P.X, err = r.ReadRD(); err != nil {
			return nil, err
		}
		if P.Y, err = r.ReadRD(); err != nil {
			return nil, err
		}
		u.ClipVerts = append(u.ClipVerts, P)
		if trOn {
			CadTraceField(trOn, 0, r.TellBits(), fmt.Sprintf("clip[%d]", i), fmt.Sprintf("(%v,%v)", P.X, P.Y))
		}
	}
	// 0.14 实测（2004/Underlay.dwg trace）：clip_verts 之后直接进
	// handle 流；dwg2.spec 的 flag&16 clip_inverts 分支在本批语料的
	// 0.14 解码器中不存在（flag=30 亦无 clip_inverts 输出），不对齐
	// 该分支以免吞掉 handle 流起始位。
	Owner, Layer := decodeOwnerLayer(r, Head)
	u.Owner, u.Layer = Owner, Layer
	// handle 流：公共序列后为 definition_id（code 340）
	r.SetBitPos(Head.ObjSizeBit)
	if _, layer2, e := ParseCommonEntityHandles(r, Head); e == nil {
		u.Layer = layer2
	}
	if h, e := objrec.ReadHandleReference(r, Head.Handle); e == nil {
		u.DefinitionID = h
	}
	return u, nil
}
