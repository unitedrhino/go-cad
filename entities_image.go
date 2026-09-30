// entities_image.go 实现批次 C 的 spec 级扩展实体：IMAGE（AcDbRasterImage，
// 位布局与 WIPEOUT 完全同构）、OLE2FRAME/OLEFRAME（OLE 框架 + 二进制数据块）、
// PROXY_ENTITY（ACAD 代理实体：元数据 + hdlpos 定界的原始数据位捕获）。
// 位级布局对照 LibreDWG dwg.spec 对应 DWG_ENTITY 定义；
// IMAGE 布局经 dwgread -v9 对 test-data 各版本 Leader.dwg 现场核对。
package cad

import (
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
)

// entImage 栅格图像实体（AcDbRasterImage）。
type entImage struct {
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
	imageDef         uint64 // IMAGEDEF 硬指针句柄（handle 流，code 340）
	imageDefReactor  uint64 // IMAGEDEF_REACTOR 硬属主句柄（handle 流，code 360）
}

// decodeImageVer IMAGE（R13+ 动态类，位布局与 WIPEOUT 同构）：
// class_version BL + pt0/uvec/vvec 3BD + image_size 2RD + display_props BS +
// clipping B + 亮度/对比/淡出 RC + [clip_mode B（R2010+）] +
// clip_boundary_type BS + 裁剪顶点 2RD 数组（boundary_type=1 固定两角）。
// handle 流：公共序列后接 imagedef(340) 与 imagedefreactor(360)
// （dwgread trace 核对：hdl 序列 reactors/xdic/prev/next/layer/imagedef/
// imagedefreactor，主体字段按 dat 流独立推进）。主体后的未记载位
// （padding 等）按 objSizeBit 截断跳过。
func decodeImageVer(r *bitstream.BitStream, head *commonEntityHead, ver dwgVersion) (any, error) {
	img := &entImage{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}}
	var err error
	if img.classVersion, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if img.classVersion > 10 {
		return nil, fmt.Errorf("cad: IMAGE class_version 异常 %d", img.classVersion)
	}
	if img.pt0, err = read3pt(r); err != nil {
		return nil, err
	}
	if img.uvec, err = read3pt(r); err != nil {
		return nil, err
	}
	if img.vvec, err = read3pt(r); err != nil {
		return nil, err
	}
	if img.imageSize.x, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if img.imageSize.y, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if img.displayProps, err = r.ReadBS(); err != nil {
		return nil, err
	}
	var v uint8
	if v, err = r.ReadB(); err != nil {
		return nil, err
	}
	img.clipping = v != 0
	if img.brightness, err = r.ReadRC(); err != nil {
		return nil, err
	}
	if img.contrast, err = r.ReadRC(); err != nil {
		return nil, err
	}
	if img.fade, err = r.ReadRC(); err != nil {
		return nil, err
	}
	if ver >= verR2010 {
		cm, err2 := r.ReadB() // clip_mode（R2010+）
		if err2 != nil {
			return nil, err2
		}
		img.clipMode = uint8(cm)
	}
	if img.clipBoundaryType, err = r.ReadBS(); err != nil {
		return nil, err
	}
	numVerts := uint32(2) // 矩形边界固定两角
	if img.clipBoundaryType != 1 {
		if numVerts, err = r.ReadBL(); err != nil {
			return nil, err
		}
	}
	if numVerts > 100_000 {
		return nil, fmt.Errorf("cad: IMAGE 裁剪顶点数异常 %d", numVerts)
	}
	for i := uint32(0); i < numVerts; i++ {
		var p point2
		if p.x, err = r.ReadRD(); err != nil {
			return nil, err
		}
		if p.y, err = r.ReadRD(); err != nil {
			return nil, err
		}
		img.clipVerts = append(img.clipVerts, p)
	}
	owner, layer := decodeOwnerLayer(r, head)
	img.owner, img.layer = owner, layer
	// handle 流：owner/layer 之后的公共序列后为 imagedef(5) 与 imagedefreactor(3)
	r.SetBitPos(head.objSizeBit)
	if _, layer2, e := parseCommonEntityHandles(r, head); e == nil {
		img.layer = layer2
	}
	if h, e := readHandleReference(r, head.handle); e == nil && h != 0 {
		img.imageDef = h
	}
	if h, e := readHandleReference(r, head.handle); e == nil && h != 0 {
		img.imageDefReactor = h
	}
	return img, nil

}

// ---- OLE2FRAME / OLEFRAME（OLE 对象框架，含二进制数据块）----

// oleDataMaxSize OLE 数据块的防御性上限（错位候选的 data_size 常为天文数；
// 真实 OLE 数据最大为嵌入了完整 OLE 流的记录，百 MB 级不可能出现）。
const oleDataMaxSize = 64 << 20

// entOle2Frame OLE2 框架实体（AcDbOle2Frame，固定类型码 0x4A）。
type entOle2Frame struct {
	baseEntity
	oleType    uint16 // type（BS 71）：1=Link 2=Embedded 3=Static
	mode       uint16 // mode/tile_mode（BS 72，R2000b+）：0=mspace 1=pspace
	dataSize   uint32 // data_size（BL 90）
	data       []byte // OLE 二进制数据（FIELD_BINARY=TF，任意位对齐字节串）
	lockAspect uint8  // lock_aspect（RC，R2000b+ 主体尾部）
}

// entOleFrame OLE 1.0 框架实体（pre-R13c4，固定类型码 0x2B；
// 打开时按需转换为 OLE2FRAME）。
type entOleFrame struct {
	baseEntity
	flag     uint16 // flag（BS 70）
	mode     uint16 // mode（BS，R2000b+）
	dataSize uint32 // data_size（BL 90）
	data     []byte // OLE 二进制数据（TF）
}

// decodeOle2FrameVer OLE2FRAME：type BS + [mode BS（R2000b+）] + data_size BL +
// data TF + [lock_aspect RC（R2000b+）]（dwg.spec DWG_ENTITY (OLE2FRAME)）。
// 注意 dwgVersion 枚举按容器路径排序（verR2000=0），R2000b+ 判定用
// 「非 R13/R14」口径而非大小比较。
func decodeOle2FrameVer(r *bitstream.BitStream, head *commonEntityHead, ver dwgVersion) (any, error) {
	ole := &entOle2Frame{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}}
	var err error
	if ole.oleType, err = r.ReadBS(); err != nil {
		return nil, err
	}
	r2000b := ver != verR13 && ver != verR14
	if r2000b {
		if ole.mode, err = r.ReadBS(); err != nil {
			return nil, err
		}
	}
	if ole.dataSize, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if ole.dataSize > oleDataMaxSize {
		return nil, fmt.Errorf("cad: OLE2FRAME data_size 异常 %d", ole.dataSize)
	}
	if ole.data, err = r.ReadRCS(int(ole.dataSize)); err != nil {
		return nil, err
	}
	if r2000b {
		if ole.lockAspect, err = r.ReadRC(); err != nil {
			return nil, err
		}
	}
	owner, layer := decodeOwnerLayer(r, head)
	ole.owner, ole.layer = owner, layer
	return ole, nil
}

// ---- PROXY_ENTITY（ACAD 代理实体，固定类型码 0x1F2）----

// proxyDataMaxBits 原始代理数据位捕获的防御性上限（错位候选的
// objSizeBit 垃圾值会产生天文位长；真实代理数据上限为数 MB）。
const proxyDataMaxBits = 64 << 23

// entProxyEntity ACAD 代理实体（第三方应用创建的自定义实体，
// 几何由宿主应用解释；本库保留元数据与原始数据位供审计/回写）。
type entProxyEntity struct {
	baseEntity
	proxyID       uint32 // proxy_id（BL 90，恒 499）
	version       uint32 // version（BLx 95，PRE R2018）：高 8 位 maint、低 8 位 dwg
	maintVersion  uint32
	dwgVersionNum uint32
	fromDxf       bool   // Original Data Format（B 70，R2000b+）：0=dwg 1=dxf
	proxyDataSize uint32 // = 公共头 preview_size（spec：proxy_data_size 即 preview_size）
	proxyData     []byte // 代理图形数据（TF）
	dataNumBits   uint32 // 主体结束到 hdlpos 之间的原始位长（DECODER data_numbits）
	data          []byte // 原始数据位（MSB 序，bit_read_bits 语义）
	numObjids     uint32 // handle 流 common 序列之后的剩余句柄数
	objids        []uint64
}

// decodeProxyEntityVer PROXY_ENTITY：proxy_id BL + [version BLx（PRE R2018）
// 或 dwg/maint BLx 对（R2018+）] + from_dxf B（R2000b+）+ proxy_data TF
// （长度取公共头 preview_size）+ 原始数据位捕获（当前位置到 hdlpos 的全部
// 位，即 LibreDWG DECODER 的 data_numbits/data）+ handle 流剩余句柄全量
// 记为 objids（LibreDWG num_objids 循环口径）。
func decodeProxyEntityVer(r *bitstream.BitStream, head *commonEntityHead, dataEnd uint64, ver dwgVersion) (any, error) {
	px := &entProxyEntity{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}}
	var err error
	if px.proxyID, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if ver >= verR2018 {
		if px.dwgVersionNum, err = r.ReadBL(); err != nil {
			return nil, err
		}
		if px.maintVersion, err = r.ReadBL(); err != nil {
			return nil, err
		}
	} else {
		if px.version, err = r.ReadBL(); err != nil {
			return nil, err
		}
		px.maintVersion = px.version >> 8
		px.dwgVersionNum = px.version & 0xFF
	}
	if ver != verR13 && ver != verR14 {
		var v uint8
		if v, err = r.ReadB(); err != nil {
			return nil, err
		}
		px.fromDxf = v != 0
	}
	// proxy_data_size 即公共头 preview_size（spec DXF_OR_PRINT else 分支）
	px.proxyDataSize = uint32(len(head.preview))
	if px.proxyDataSize > 0 {
		if px.proxyData, err = r.ReadRCS(int(px.proxyDataSize)); err != nil {
			return nil, err
		}
	}
	// 原始数据位捕获：当前位置到 hdlpos（head.objSizeBit）的全部位
	if pos := r.TellBits(); head.objSizeBit > pos && head.objSizeBit <= dataEnd {
		n := head.objSizeBit - pos
		if n > proxyDataMaxBits {
			return nil, fmt.Errorf("cad: PROXY_ENTITY data 位长异常 %d", n)
		}
		px.dataNumBits = uint32(n)
		if px.data, err = r.ReadBitsBytes(int((n + 7) / 8)); err != nil {
			return nil, err
		}
	}
	owner, layer := decodeOwnerLayer(r, head)
	px.owner, px.layer = owner, layer
	// handle 流剩余句柄全量收集（LibreDWG while(hdl_dat->byte < hdl_dat->size) 口径，
	// 含尾部 CRC 字节被当作句柄的差异，与参考实现一致）
	r.SetBitPos(head.objSizeBit)
	if _, _, e := parseCommonEntityHandles(r, head); e == nil {
		for r.TellBits()+8 <= dataEnd {
			h, e := readHandleReference(r, head.handle)
			if e != nil {
				break
			}
			px.numObjids++
			px.objids = append(px.objids, h)
		}
	}
	return px, nil
}

// decodeOleFrameVer OLEFRAME：flag BS + [mode BS（R2000b+）] + data_size BL +
// data TF（dwg.spec DWG_ENTITY (OLEFRAME)）。
func decodeOleFrameVer(r *bitstream.BitStream, head *commonEntityHead, ver dwgVersion) (any, error) {
	ole := &entOleFrame{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}}
	var err error
	if ole.flag, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if ver != verR13 && ver != verR14 {
		if ole.mode, err = r.ReadBS(); err != nil {
			return nil, err
		}
	}
	if ole.dataSize, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if ole.dataSize > oleDataMaxSize {
		return nil, fmt.Errorf("cad: OLEFRAME data_size 异常 %d", ole.dataSize)
	}
	if ole.data, err = r.ReadRCS(int(ole.dataSize)); err != nil {
		return nil, err
	}
	owner, layer := decodeOwnerLayer(r, head)
	ole.owner, ole.layer = owner, layer
	return ole, nil
}

// entUnderlay 底图引用实体（PDFUNDERLAY/DGNUNDERLAY/DWFUNDERLAY 共用
// AcDbUnderlayReference 布局，dwg2.spec UNDERLAY_fields；字段序经
// 2004/Underlay.dwg dwgread -v9 trace 现场核对：definition_id 位于
// handle 流区，主体为 extrusion→ins_pt→angle→scale→flag→contrast→fade
// →clip 顶点）。几何来自外部 PDF/DGN/DWF 文件，DWG 内仅存引用框变换
// 与裁剪多边形。
type entUnderlay struct {
	baseEntity
	definitionID uint64   // PDFDEFINITION/DGNDEFINITION/DWFDEFINITION 硬指针（handle 流）
	extrusion    point3   // 挤出方向（3BD）
	insPt        point3   // 插入点（3RD）
	angle        float64  // 旋转角（BD）
	scale        point3   // 三轴缩放（3BD）
	flag         uint8    // 状态标志（显示/裁剪相关位）
	contrast     int8     // 对比度 20~100（RCd 有符号）
	fade         int8     // 淡出 0~80（RCd 有符号）
	clipVerts    []point2 // 裁剪多边形顶点（2RD）
}

// decodeUnderlayVer 底图引用解码（三个 UNDERLAY 类共用布局）。
func decodeUnderlayVer(r *bitstream.BitStream, head *commonEntityHead) (any, error) {
	u := &entUnderlay{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}}
	trOn := cadTraceHandle != 0 && cadTraceHandle == head.handle
	var err error
	pos := r.TellBits()
	if u.extrusion, err = read3pt(r); err != nil {
		return nil, err
	}
	if trOn {
		cadTraceField(trOn, pos, r.TellBits(), "extrusion", fmt.Sprintf("(%v,%v,%v)", u.extrusion.x, u.extrusion.y, u.extrusion.z))
	}
	pos = r.TellBits()
	if u.insPt, err = read3pt(r); err != nil {
		return nil, err
	}
	if trOn {
		cadTraceField(trOn, pos, r.TellBits(), "ins_pt", fmt.Sprintf("(%v,%v,%v)", u.insPt.x, u.insPt.y, u.insPt.z))
	}
	pos = r.TellBits()
	if u.angle, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if trOn {
		cadTraceField(trOn, pos, r.TellBits(), "angle", fmt.Sprintf("%v", u.angle))
	}
	pos = r.TellBits()
	if u.scale, err = read3pt(r); err != nil {
		return nil, err
	}
	if trOn {
		cadTraceField(trOn, pos, r.TellBits(), "scale", fmt.Sprintf("(%v,%v,%v)", u.scale.x, u.scale.y, u.scale.z))
	}
	pos = r.TellBits()
	if u.flag, err = r.ReadRC(); err != nil {
		return nil, err
	}
	cadTraceFieldInt(trOn, pos, r.TellBits(), "flag", int64(u.flag))
	pos = r.TellBits()
	var c, f uint8
	if c, err = r.ReadRC(); err != nil {
		return nil, err
	}
	u.contrast = int8(c)
	cadTraceFieldInt(trOn, pos, r.TellBits(), "contrast", int64(u.contrast))
	pos = r.TellBits()
	if f, err = r.ReadRC(); err != nil {
		return nil, err
	}
	u.fade = int8(f)
	cadTraceFieldInt(trOn, pos, r.TellBits(), "fade", int64(u.fade))
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
		var p point2
		if p.x, err = r.ReadRD(); err != nil {
			return nil, err
		}
		if p.y, err = r.ReadRD(); err != nil {
			return nil, err
		}
		u.clipVerts = append(u.clipVerts, p)
		if trOn {
			cadTraceField(trOn, 0, r.TellBits(), fmt.Sprintf("clip[%d]", i), fmt.Sprintf("(%v,%v)", p.x, p.y))
		}
	}
	// 0.14 实测（2004/Underlay.dwg trace）：clip_verts 之后直接进
	// handle 流；dwg2.spec 的 flag&16 clip_inverts 分支在本批语料的
	// 0.14 解码器中不存在（flag=30 亦无 clip_inverts 输出），不对齐
	// 该分支以免吞掉 handle 流起始位。
	owner, layer := decodeOwnerLayer(r, head)
	u.owner, u.layer = owner, layer
	// handle 流：公共序列后为 definition_id（code 340）
	r.SetBitPos(head.objSizeBit)
	if _, layer2, e := parseCommonEntityHandles(r, head); e == nil {
		u.layer = layer2
	}
	if h, e := readHandleReference(r, head.handle); e == nil {
		u.definitionID = h
	}
	return u, nil
}
