// 本文件实现 UNDERLAY 引用实体（PDFUNDERLAY/DWFUNDERLAY/DGNUNDERLAY）
// 的位级解码：dwg2.spec DWG_ENTITY(PDFUNDERLAY) 的 UNDERLAY_fields。
// 该类为实体布局（实体公共头 + 专有字段 + COMMON_ENTITY_HANDLE_DATA），
// 但在本仓架构中经对象分发（isEntityType 不识别该类，此前以
// UNKNOWN_OBJ 兜底）；此处复用实体头扫描（scanEntityBest）正确定位
// 后读取专有字段，结果转为 objGeneric 记录，字段键对齐 dwgread JSON。

package cad

import "fmt"

// underlayFields UNDERLAY 引用实体的专有字段（UNDERLAY_fields）与
// 公共头提取结果。
type underlayFields struct {
	definitionID uint64            // definition_id 硬引用（*_DEFINITION 对象）
	extrusion    []float64         // 3BD 拉伸矢量
	insPt        []float64         // 3BD 插入点
	angle        float64           // BD 旋转角（弧度）
	scale        []float64         // 3BD_1 缩放
	flag         uint8             // RC0 标志（bit4=有 clip_inverts）
	contrast     uint8             // RCd 对比度（20-100）
	fade         uint8             // RCd 淡入度（0-80）
	clipVerts    [][2]float64      // 2RD 裁剪多边形顶点
	clipInverts  [][2]float64      // flag&16 时的反向裁剪顶点
	owner        uint64            // COMMON_ENTITY_HANDLE_DATA ownerhandle
	layer        uint64            // COMMON_ENTITY_HANDLE_DATA layer
	objSizeBit   uint64            // handle 流起点（body 局部位，公共头扫描结果）
	auditBitsize uint64            // gold 口径 bitsize
	head         *commonEntityHead // 公共头扫描结果（preview/color 等提取源）
}

// decodeUnderlayEntity 解析 UNDERLAY 引用实体并转为 objGeneric。
// r 已定位到类型码之后（body 局部坐标）；rec/ver 用于实体头布局选择。
func decodeUnderlayEntity(r *bitStream, rec *objectRecord, ver dwgVersion, typeCode uint16, className string) (*objGeneric, error) {
	base := r.tellBits()
	dataEnd := rec.dataEndBit()
	// 实体头布局：typecode 后先有流内 RL objSize（R2010+ 除外）再有
	// 主句柄（H），与对象头顺序不同。逐布局探出主句柄供
	// scanEntityBest 的头句柄一致性校验，再回卷交给扫描
	parsers := headParsersForVersion(ver)
	var objHandle uint64
	for _, p := range parsers {
		r.setBitPos(base)
		if !p.externalSize {
			if _, err := r.readRL(); err != nil {
				continue
			}
		}
		h, err := r.readH()
		if err == nil && h.value != 0 {
			objHandle = h.value
			break
		}
	}
	r.setBitPos(base)
	res, _, ferr := scanEntityBest(r, base, dataEnd, uint64(rec.handleSizeFieldBits),
		parsers, objHandle, rec.size, className, typeCode,
		func(r *bitStream, head *commonEntityHead) (any, error) {
			return readUnderlayFields(r, head)
		})
	if ferr != nil {
		return nil, ferr
	}
	uf, ok := res.(*underlayFields)
	if !ok || uf == nil || uf.head == nil {
		return nil, fmt.Errorf("cad: %s 解码结果异常", className)
	}
	head := uf.head
	g := &objGeneric{
		Name:        className,
		Handle:      head.handle,
		Owner:       uf.owner,
		ObjSizeBit:  uf.objSizeBit,
		NumReactors: int(head.numReactors),
		XdicMissing: head.xdicMissing,
	}
	g.Fields = append(g.Fields,
		objField{"definition_id", int64(uf.definitionID)},
		objField{"extrusion", uf.extrusion},
		objField{"ins_pt", uf.insPt},
		objField{"angle", uf.angle},
		objField{"scale", uf.scale},
		objField{"flag", int64(uf.flag)},
		objField{"contrast", int64(uf.contrast)},
		objField{"fade", int64(uf.fade)},
	)
	g.Fields = append(g.Fields, objField{"num_clip_verts", int64(len(uf.clipVerts))})
	for i, v := range uf.clipVerts {
		g.Fields = append(g.Fields, objField{fmt.Sprintf("clip_verts[%d]", i), []float64{v[0], v[1]}})
	}
	for i, v := range uf.clipInverts {
		g.Fields = append(g.Fields, objField{fmt.Sprintf("clip_inverts[%d]", i), []float64{v[0], v[1]}})
	}
	// 公共头关键字段（键名对齐 dwgread JSON）
	g.Fields = append(g.Fields,
		objField{"preview_exists", head.previewExists},
		objField{"preview_size", int64(len(head.preview))},
		objField{"preview", fmt.Sprintf("%x", head.preview)},
		objField{"entmode", int64(head.entityMode)},
		objField{"color", underlayColorMap(head.color)},
		objField{"ltype_scale", head.ltypeScale},
		objField{"ltype_flags", int64(head.ltypeFlags)},
		objField{"plotstyle_flags", int64(head.plotstyleFlgs)},
		objField{"invisible", int64(head.invisible)},
		objField{"linewt", int64(head.linewt)},
	)
	if uf.layer != 0 {
		g.Handles = append(g.Handles, uf.layer)
	}
	g.Handles = append(g.Handles, uf.definitionID)
	// 公共元数据键（与其他内部对象同形，供导出与审计）
	g.Fields = append(g.Fields,
		objField{"object", g.Name},
		objField{"type", int64(typeCode)},
		objField{"size", int64(rec.size)},
		objField{"bitsize", int64(uf.auditBitsize)},
		objField{"num_reactors", int64(head.numReactors)},
		objField{"is_xdic_missing", head.xdicMissing},
		objField{"has_ds_data", head.hasDsBinary},
		objField{"dxfname", g.Name},
	)
	// 位串收集（对齐 UNKNOWN_OBJ 兜底语义）：hdOffsetBits 为类型码后
	// 前导位，headRawBits 覆盖公共头+专有字段至 handle 流起点，
	// RawHandleBits 覆盖 handle 流起点至记录尾
	g.hdOffsetBits = base - rec.bodyBitOffset
	if uf.objSizeBit > base {
		g.headRawBits = collectBits(r, base, uf.objSizeBit)
	}
	g.RawHandleBits = collectBits(r, uf.objSizeBit, uint64(len(r.src))*8)
	return g, nil
}

// readUnderlayFields 读取 UNDERLAY_fields 与 COMMON_ENTITY_HANDLE_DATA。
// LibreDWG 实体为双游标模型：dat 流读标量字段，hdl 流读句柄引用，
// 两个游标独立推进（spec 文本顺序即各流内的读取顺序）。故：
// hdl 流 = COMMON_ENTITY_HANDLE_DATA（owner/reactors/xdic/layer/...）
// 之后紧跟 definition_id；dat 流 = extrusion → ins_pt → angle → scale →
// flag → contrast → fade → num_clip_verts → clip_verts →（flag&16）
// clip_inverts。
func readUnderlayFields(r *bitStream, head *commonEntityHead) (any, error) {
	uf := &underlayFields{}
	// hdl 流：公共实体句柄 + definition_id（硬引用 *_DEFINITION 对象）
	savedByte, savedBit := r.cursor()
	r.setBitPos(head.objSizeBit)
	owner, layer, err := parseCommonEntityHandles(r, head)
	if err != nil {
		r.restore(savedByte, savedBit)
		return nil, err
	}
	defID, err := readHandleReference(r, head.handle)
	if err != nil {
		r.restore(savedByte, savedBit)
		return nil, err
	}
	r.restore(savedByte, savedBit)
	uf.owner = owner
	uf.layer = layer
	uf.definitionID = defID
	// dat 流：标量字段
	if uf.extrusion, err = readBD3(r); err != nil {
		return nil, err
	}
	if uf.insPt, err = readBD3(r); err != nil {
		return nil, err
	}
	if uf.angle, err = r.readBD(); err != nil {
		return nil, err
	}
	if uf.scale, err = readBD3(r); err != nil {
		return nil, err
	}
	if uf.flag, err = r.readRC(); err != nil {
		return nil, err
	}
	if uf.contrast, err = r.readRC(); err != nil {
		return nil, err
	}
	if uf.fade, err = r.readRC(); err != nil {
		return nil, err
	}
	numClip, err := r.readBL()
	if err != nil {
		return nil, err
	}
	// spec VALUEOUTOFBOUNDS(num_clip_verts, 5000)
	if numClip > 5000 {
		return nil, fmt.Errorf("cad: UNDERLAY num_clip_verts 越界 %d", numClip)
	}
	for i := uint32(0); i < numClip; i++ {
		x, e := r.readRD()
		if e != nil {
			return nil, e
		}
		y, e := r.readRD()
		if e != nil {
			return nil, e
		}
		uf.clipVerts = append(uf.clipVerts, [2]float64{x, y})
	}
	if uf.flag&16 != 0 {
		// flag&16：clip_inverts 存在。语料实证（Underlay.dwg 三实例）
		// LibreDWG 在此处已越出 bitsize 读出垃圾计数（68/7880）且 gold
		// 输出为空数组——对齐其宽容行为：计数非法或空间不足时留空，
		// 不作为解码失败
		numInv, e := r.readBS()
		if e == nil && numInv <= 5000 && numInv > 0 {
			for i := uint16(0); i < numInv; i++ {
				x, e := r.readRD()
				if e != nil {
					break
				}
				y, e := r.readRD()
				if e != nil {
					break
				}
				uf.clipInverts = append(uf.clipInverts, [2]float64{x, y})
			}
		}
	}
	uf.objSizeBit = head.objSizeBit
	uf.auditBitsize = head.auditBitsize
	uf.head = head
	return uf, nil
}

// readBD3 读三个 BD（FIELD_3BD / FIELD_3DPOINT / FIELD_3BD_1 的解码端
// 均为三个 BD，DD 差分语义由 readBD 内建）。
func readBD3(r *bitStream) ([]float64, error) {
	x, err := r.readBD()
	if err != nil {
		return nil, err
	}
	y, err := r.readBD()
	if err != nil {
		return nil, err
	}
	z, err := r.readBD()
	if err != nil {
		return nil, err
	}
	return []float64{x, y, z}, nil
}

// underlayColorMap 将实体头颜色转为 dwgread JSON 的 color 键形状。
func underlayColorMap(c entColor) map[string]any {
	m := map[string]any{"index": int64(c.index)}
	if c.hasTrue {
		m["rgb"] = fmt.Sprintf("%06x", c.trueColor&0xFFFFFF)
	} else {
		m["rgb"] = "000000"
	}
	m["flag"] = int64(c.flag)
	return m
}
