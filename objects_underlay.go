// 本文件实现 UNDERLAY 引用实体（PDFUNDERLAY/DWFUNDERLAY/DGNUNDERLAY）
// 的位级解码：dwg2.spec DWG_ENTITY(PDFUNDERLAY) 的 UNDERLAY_fields。
// 该类为实体布局（实体公共头 + 专有字段 + COMMON_ENTITY_HANDLE_DATA），
// 但在本仓架构中经对象分发（isEntityType 不识别该类，此前以
// UNKNOWN_OBJ 兜底）；此处复用实体头扫描（scanEntityBest）正确定位
// 后读取专有字段，结果转为 objGeneric 记录，字段键对齐 dwgread JSON。

package cad

import (
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/entity"
	"github.com/unitedrhino/go-cad/internal/objrec"
)

// underlayFields UNDERLAY 引用实体的专有字段（UNDERLAY_fields）与
// 公共头提取结果。
type underlayFields struct {
	DefinitionID uint64                   // definition_id 硬引用（*_DEFINITION 对象）
	Extrusion    []float64                // 3BD 拉伸矢量
	InsPt        []float64                // 3BD 插入点
	Angle        float64                  // BD 旋转角（弧度）
	Scale        []float64                // 3BD_1 缩放
	Flag         uint8                    // RC0 标志（bit4=有 clip_inverts）
	Contrast     uint8                    // RCd 对比度（20-100）
	Fade         uint8                    // RCd 淡入度（0-80）
	ClipVerts    [][2]float64             // 2RD 裁剪多边形顶点
	ClipInverts  [][2]float64             // flag&16 时的反向裁剪顶点
	Owner        uint64                   // COMMON_ENTITY_HANDLE_DATA ownerhandle
	Layer        uint64                   // COMMON_ENTITY_HANDLE_DATA layer
	ObjSizeBit   uint64                   // handle 流起点（body 局部位，公共头扫描结果）
	AuditBitsize uint64                   // gold 口径 bitsize
	Head         *entity.CommonEntityHead // 公共头扫描结果（preview/color 等提取源）
}

// decodeUnderlayEntity 解析 UNDERLAY 引用实体并转为 objGeneric。
// r 已定位到类型码之后（body 局部坐标）；rec/ver 用于实体头布局选择。
func decodeUnderlayEntity(r *bitstream.BitStream, rec *objrec.ObjectRecord, ver container.DwgVersion, typeCode uint16, className string) (*objGeneric, error) {
	base := r.TellBits()
	dataEnd := rec.DataEndBit()
	// 实体头布局：typecode 后先有流内 RL objSize（R2010+ 除外）再有
	// 主句柄（H），与对象头顺序不同。逐布局探出主句柄供
	// scanEntityBest 的头句柄一致性校验，再回卷交给扫描
	parsers := entity.HeadParsersForVersion(ver)
	var objHandle uint64
	for _, p := range parsers {
		r.SetBitPos(base)
		if !p.ExternalSize {
			if _, err := r.ReadRL(); err != nil {
				continue
			}
		}
		h, err := r.ReadH()
		if err == nil && h.Value != 0 {
			objHandle = h.Value
			break
		}
	}
	r.SetBitPos(base)
	res, _, ferr := entity.ScanEntityBest(r, base, dataEnd, uint64(rec.HandleSizeFieldBits),
		parsers, objHandle, rec.Size, className, typeCode,
		func(r *bitstream.BitStream, head *entity.CommonEntityHead) (any, error) {
			return readUnderlayFields(r, head)
		})
	if ferr != nil {
		return nil, ferr
	}
	uf, ok := res.(*underlayFields)
	if !ok || uf == nil || uf.Head == nil {
		return nil, fmt.Errorf("cad: %s 解码结果异常", className)
	}
	head := uf.Head
	g := &objGeneric{
		Name:        className,
		Handle:      head.Handle,
		Owner:       uf.Owner,
		ObjSizeBit:  uf.ObjSizeBit,
		NumReactors: int(head.NumReactors),
		XdicMissing: head.XdicMissing,
	}
	g.Fields = append(g.Fields,
		objField{"definition_id", int64(uf.DefinitionID)},
		objField{"extrusion", uf.Extrusion},
		objField{"ins_pt", uf.InsPt},
		objField{"angle", uf.Angle},
		objField{"scale", uf.Scale},
		objField{"flag", int64(uf.Flag)},
		objField{"contrast", int64(uf.Contrast)},
		objField{"fade", int64(uf.Fade)},
	)
	g.Fields = append(g.Fields, objField{"num_clip_verts", int64(len(uf.ClipVerts))})
	for i, v := range uf.ClipVerts {
		g.Fields = append(g.Fields, objField{fmt.Sprintf("clip_verts[%d]", i), []float64{v[0], v[1]}})
	}
	for i, v := range uf.ClipInverts {
		g.Fields = append(g.Fields, objField{fmt.Sprintf("clip_inverts[%d]", i), []float64{v[0], v[1]}})
	}
	// 公共头关键字段（键名对齐 dwgread JSON）
	g.Fields = append(g.Fields,
		objField{"preview_exists", head.PreviewExists},
		objField{"preview_size", int64(len(head.Preview))},
		objField{"preview", fmt.Sprintf("%x", head.Preview)},
		objField{"entmode", int64(head.EntityMode)},
		objField{"color", underlayColorMap(head.Color)},
		objField{"ltype_scale", head.LtypeScale},
		objField{"ltype_flags", int64(head.LtypeFlags)},
		objField{"plotstyle_flags", int64(head.PlotstyleFlgs)},
		objField{"invisible", int64(head.Invisible)},
		objField{"linewt", int64(head.Linewt)},
	)
	if uf.Layer != 0 {
		g.Handles = append(g.Handles, uf.Layer)
	}
	g.Handles = append(g.Handles, uf.DefinitionID)
	// 公共元数据键（与其他内部对象同形，供导出与审计）
	g.Fields = append(g.Fields,
		objField{"object", g.Name},
		objField{"type", int64(typeCode)},
		objField{"size", int64(rec.Size)},
		objField{"bitsize", int64(uf.AuditBitsize)},
		objField{"num_reactors", int64(head.NumReactors)},
		objField{"is_xdic_missing", head.XdicMissing},
		objField{"has_ds_data", head.HasDsBinary},
		objField{"dxfname", g.Name},
	)
	// 位串收集（对齐 UNKNOWN_OBJ 兜底语义）：hdOffsetBits 为类型码后
	// 前导位，headRawBits 覆盖公共头+专有字段至 handle 流起点，
	// RawHandleBits 覆盖 handle 流起点至记录尾
	g.hdOffsetBits = base - rec.BodyBitOffset
	if uf.ObjSizeBit > base {
		g.headRawBits = bitstream.CollectBits(r, base, uf.ObjSizeBit)
	}
	g.RawHandleBits = bitstream.CollectBits(r, uf.ObjSizeBit, uint64(len(r.Src))*8)
	return g, nil
}

// readUnderlayFields 读取 UNDERLAY_fields 与 COMMON_ENTITY_HANDLE_DATA。
// LibreDWG 实体为双游标模型：dat 流读标量字段，hdl 流读句柄引用，
// 两个游标独立推进（spec 文本顺序即各流内的读取顺序）。故：
// hdl 流 = COMMON_ENTITY_HANDLE_DATA（owner/reactors/xdic/layer/...）
// 之后紧跟 definition_id；dat 流 = extrusion → ins_pt → angle → scale →
// flag → contrast → fade → num_clip_verts → clip_verts →（flag&16）
// clip_inverts。
func readUnderlayFields(r *bitstream.BitStream, head *entity.CommonEntityHead) (any, error) {
	uf := &underlayFields{}
	// hdl 流：公共实体句柄 + definition_id（硬引用 *_DEFINITION 对象）
	savedByte, savedBit := r.Cursor()
	r.SetBitPos(head.ObjSizeBit)
	owner, layer, err := entity.ParseCommonEntityHandles(r, head)
	if err != nil {
		r.Restore(savedByte, savedBit)
		return nil, err
	}
	defID, err := objrec.ReadHandleReference(r, head.Handle)
	if err != nil {
		r.Restore(savedByte, savedBit)
		return nil, err
	}
	r.Restore(savedByte, savedBit)
	uf.Owner = owner
	uf.Layer = layer
	uf.DefinitionID = defID
	// dat 流：标量字段
	if uf.Extrusion, err = readBD3(r); err != nil {
		return nil, err
	}
	if uf.InsPt, err = readBD3(r); err != nil {
		return nil, err
	}
	if uf.Angle, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if uf.Scale, err = readBD3(r); err != nil {
		return nil, err
	}
	if uf.Flag, err = r.ReadRC(); err != nil {
		return nil, err
	}
	if uf.Contrast, err = r.ReadRC(); err != nil {
		return nil, err
	}
	if uf.Fade, err = r.ReadRC(); err != nil {
		return nil, err
	}
	numClip, err := r.ReadBL()
	if err != nil {
		return nil, err
	}
	// spec VALUEOUTOFBOUNDS(num_clip_verts, 5000)
	if numClip > 5000 {
		return nil, fmt.Errorf("cad: UNDERLAY num_clip_verts 越界 %d", numClip)
	}
	for i := uint32(0); i < numClip; i++ {
		x, e := r.ReadRD()
		if e != nil {
			return nil, e
		}
		y, e := r.ReadRD()
		if e != nil {
			return nil, e
		}
		uf.ClipVerts = append(uf.ClipVerts, [2]float64{x, y})
	}
	if uf.Flag&16 != 0 {
		// flag&16：clip_inverts 存在。语料实证（Underlay.dwg 三实例）
		// LibreDWG 在此处已越出 bitsize 读出垃圾计数（68/7880）且 gold
		// 输出为空数组——对齐其宽容行为：计数非法或空间不足时留空，
		// 不作为解码失败
		numInv, e := r.ReadBS()
		if e == nil && numInv <= 5000 && numInv > 0 {
			for i := uint16(0); i < numInv; i++ {
				x, e := r.ReadRD()
				if e != nil {
					break
				}
				y, e := r.ReadRD()
				if e != nil {
					break
				}
				uf.ClipInverts = append(uf.ClipInverts, [2]float64{x, y})
			}
		}
	}
	uf.ObjSizeBit = head.ObjSizeBit
	uf.AuditBitsize = head.AuditBitsize
	uf.Head = head
	return uf, nil
}

// readBD3 读三个 BD（FIELD_3BD / FIELD_3DPOINT / FIELD_3BD_1 的解码端
// 均为三个 BD，DD 差分语义由 readBD 内建）。
func readBD3(r *bitstream.BitStream) ([]float64, error) {
	x, err := r.ReadBD()
	if err != nil {
		return nil, err
	}
	y, err := r.ReadBD()
	if err != nil {
		return nil, err
	}
	z, err := r.ReadBD()
	if err != nil {
		return nil, err
	}
	return []float64{x, y, z}, nil
}

// underlayColorMap 将实体头颜色转为 dwgread JSON 的 color 键形状。
func underlayColorMap(c entity.EntColor) map[string]any {
	m := map[string]any{"index": int64(c.Index)}
	if c.HasTrue {
		m["rgb"] = fmt.Sprintf("%06x", c.TrueColor&0xFFFFFF)
	} else {
		m["rgb"] = "000000"
	}
	m["flag"] = int64(c.Flag)
	return m
}
