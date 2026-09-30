// cad.go 是 CAD 解析包的入口：解析 R13~R2018 全版本 DWG 为文档模型，
// 并提供 R2000/R2004/R2007 三代容器写出（WriteDwgR2000/WriteDwgR2004/
// WriteDwgR2007），供渲染（RenderPNG）与文本提取（Texts）消费。
package cad

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)

// Entity 对外暴露的图元接口（几何访问用于渲染与包围盒计算）。
type Entity interface {
	// Geometry 输出图元的描边段（折线近似；弧按弦高离散）与文字占位。
	bounds() box2
}

// TextInfo 提取的图纸文本及位置（世界坐标）。
type TextInfo struct {
	Text  string
	Layer uint64
	X, Y  float64
}

// box2 世界坐标包围盒。
type box2 struct {
	minX, minY, maxX, maxY float64
}

// Document 解析完成的 DWG 文档模型。
type Document struct {
	version     dwgVersion
	codepage    uint16
	modelSpace  []any                 // 模型空间图元（entmode==2）
	blocks      map[uint64][]any      // 块定义：BLOCK_HEADER handle → 块内图元
	attribs     map[uint64]*entAttrib // ATTRIB handle → 属性
	layerColors map[uint64]layerColor
	// lightingUnits NOD 字典 LIGHTINGUNITS 条目的 DICTIONARYVAR 值（预扫描
	// 产物，主循环前探测），=="2" 时 LIGHT 实体按光度分支解码
	lightingUnits string
	// pspaceSpace pre-R13 图纸空间实体（HAS_PSPACE 位，LibreDWG entmode=1）：
	// pre-R13 的图纸空间实体位于主实体区，与 R13+（图纸空间内容在
	// blocks 的 PAPER_SPACE 块头内）不同，需要单独存档；不参与模型空间
	// 渲染，仅保证解码结果可见（审计/对照）。
	pspaceSpace []any
	// R2013+ 内部对象：命名字典与扩展记录（键 = 对象句柄）
	dictionaries    map[uint64]*objDictionary
	internalObjects map[uint64]*objGeneric
	xrecords        map[uint64]*objXrecord // LAYER handle → 颜色
	skipped         int                    // 解码失败被跳过的对象数
	entityByHandle  map[uint64]any         // 实体句柄 → 实体对象（审计/round-trip 用）
	// debugFailures 调试：按句柄记录解码失败原因。
	debugFailures map[uint64]string
	// r2000Raw R2000/R13/R14 容器的原始写出素材（段整段字节、对象区整块
	// 字节与对象图条目），仅在对应版本解析路径保留，供 WriteDwgR2000
	// 做文件级回放写出。
	r2000Raw *r2000RawData
	// r2004Raw R2004 家族（AC1018~AC1032 同容器）的原始写出素材（头部
	// 字节、页表顺序、段表解压字节与各段解压数据），仅在对应版本解析路径
	// 保留，供 WriteDwgR2004 做文件级回放写出。
	r2004Raw *r2004RawData
	// r2007Raw R2007（AC1021）容器的原始写出素材（头部、第二头部 34 字段、
	// 页表顺序与各段解压数据），仅在 R2007 解析路径保留，供 WriteDwgR2007
	// 做文件级回放写出。
	r2007Raw *r2007RawData
	// HeaderVars JSON 输入（dwgread -O JSON）HEADER 段的全量键值（原样
	// 保存，不逐个建模；键名与 gold 一致，不带 $ 前缀，查询用 HeaderVar
	// 做 $ 容错）。仅 ParseJSON 路径填充；DWG/DXF 路径的头变量走各自结构。
	HeaderVars map[string]any
	// extMin/extMax 图幅范围（JSON HEADER EXTMIN/EXTMAX，渲染视口可用）；
	// insbase 插入基点（INSBASE）；ltscale 全局线型比例（LTSCALE）。
	// 与 HeaderVars 同源，仅 ParseJSON 路径填充。
	extMin, extMax point3
	insbase        point3
	ltscale        float64
}

// Version 返回 DWG 版本串（如 AC1032）。
func (d *Document) Version() string { return d.version.verString() }

// EntityByHandle 按句柄取实体对象（未找到返回 nil）。
func (d *Document) EntityByHandle(h uint64) any { return d.entityByHandle[h] }

// objRecordR2010Plus 当前版本的记录是否为 R2010+ 布局。
func (d *Document) objRecordR2010Plus() bool { return d.version.r2010Plus() }

// InternalObjects 返回通用内部对象解码结果（SCALE/DICTIONARYVAR/APPID 等）。
func (d *Document) InternalObjects() map[uint64]*objGeneric { return d.internalObjects }

// decodeInternalObjectOK 判断类型码/类名是否命中通用内部对象解码器。
func decodeInternalObjectOK(typeCode uint16, className string) bool {
	// DICTIONARYWDFLT（固定码 0x2B 或类名路由）
	if typeCode == 0x2B || className == "ACDBDICTIONARYWDFLT" || className == "DICTIONARYWDFLT" {
		return true
	}
	// UNKNOWN_OBJ 兜底：类类型（≥500）且无专门解码器的对象，
	// LibreDWG 同样以 UNKNOWN_OBJ 兜底（仅记录 unknown_bits）
	if typeCode >= 500 || className == "UNKNOWN_OBJ" || className == "ACDBASSOCPERSSUBENTMANAGER" {
		return true
	}
	if _, ok := internalFixedDecoders[typeCode]; ok {
		return true
	}
	if className != "" {
		if _, ok := internalClassDecoders[className]; ok {
			return true
		}
	}
	return false
}

// Skipped 返回解析失败被跳过的对象数量。
func (d *Document) Skipped() int { return d.skipped }

// Xrecords 返回全部 XRECORD 对象（含结构化 xdata）。
func (d *Document) Xrecords() map[uint64]*objXrecord { return d.xrecords }

// Parse 解析 DWG 字节流。首版支持 AC1032（R2018）。
func Parse(data []byte) (*Document, error) {
	version, err := detectVersion(data)
	if err != nil {
		return nil, err
	}
	if version == verR2000 || version == verR14 || version == verR13 {
		// R13/R14 与 R2000 共用段目录式容器
		return parseR2000Document(data)
	}
	if version.preR13() {
		// pre-R13 家族（R9/R10/R11）：固定偏移表驱动的字节布局，
		// 无对象图/句柄流，走独立解析路径（r11.go）
		return parsePreR13Document(data)
	}
	doc := &Document{
		version:         version,
		codepage:        readCodepage(data),
		blocks:          make(map[uint64][]any),
		attribs:         make(map[uint64]*entAttrib),
		layerColors:     make(map[uint64]layerColor),
		dictionaries:    make(map[uint64]*objDictionary),
		xrecords:        make(map[uint64]*objXrecord),
		internalObjects: make(map[uint64]*objGeneric),
	}
	switch version {
	case verR2007:
		// R2007 容器结构独立（RS 去交织 + R21 解压），单独捕获回放素材；
		// 失败不阻断解析（与 R2000/R2004 钩子同策略）
		doc.r2007Raw = captureR2007Raw(data)
	case verR2004, verR2010, verR2013, verR2018:
		// R2004 家族容器（AC1018~AC1032 共用页式容器）：保留回放素材供
		// WriteDwgR2004 文件级写出；失败不阻断解析（与 R2000 钩子同策略）
		doc.r2004Raw = captureR2004Raw(data)
	}
	if err := doc.decodeObjects(data); err != nil {
		return nil, err
	}
	return doc, nil
}

// probeLightingUnits 预扫描对象图，定位 LIGHTINGUNITS 条目的值：
// 仅解码 DICTIONARY（0x2A）与 DICTIONARYVAR（类路由）两类对象（远小于
// 全量解码的代价），在任一字典的条目名中匹配 LIGHTINGUNITS 后返回对应
// DICTIONARYVAR 的 strvalue（LibreDWG dwg_variable_dict 查询语义；
// 该条目名在图纸变量字典中唯一，无需回溯 NOD→ROOT 链）。
// LIGHT 实体的光度分支由该值 =="2" 触发，须在实体主体解码前可知。
// 失败静默返回空串（按非光度基线解码）。
func probeLightingUnits(refs []objectRef, objectsData []byte, d *Document, dynamicTypes map[uint16]string) string {
	vars := map[uint64]string{}
	dicts := map[uint64]*objDictionary{}
	for _, ref := range refs {
		rec, err := parseObjectRecord(objectsData, ref, d.objRecordR2010Plus())
		if err != nil {
			continue
		}
		h, err := parseObjHeader(rec)
		if err != nil {
			continue
		}
		r := rec.bodyBitStream()
		switch h.typeCode {
		case 0x2A:
			r.setBitPos(h.dataStartBit)
			if dd, err := decodeDictionaryObject(r, rec, d.version, d.version >= verR2013); err == nil {
				dicts[ref.handle] = dd
			}
		default:
			if entityTypeName(h.typeCode, dynamicTypes) == "DICTIONARYVAR" {
				r.setBitPos(h.dataStartBit)
				if g, err := decodeInternalObject(r, rec, d.version, d.version >= verR2013, h.typeCode, "DICTIONARYVAR", d.codepage); err == nil {
					if v, ok := g.Field("strvalue").(string); ok {
						vars[ref.handle] = v
					}
				}
			}
		}
	}
	for _, dic := range dicts {
		for i, txt := range dic.texts {
			if txt != "LIGHTINGUNITS" || i >= len(dic.itemHandles) {
				continue
			}
			if v, ok := vars[dic.itemHandles[i]]; ok {
				return v
			}
		}
	}
	return ""
}

// decodeObjects 遍历对象数据库，解码实体、图层颜色与属性。
func (d *Document) decodeObjects(fileData []byte) error {
	objectsData, err := loadNamedSectionData(fileData, "AcDb:AcDbObjects")
	if err != nil {
		return fmt.Errorf("cad: 加载对象数据段失败: %w", err)
	}
	index, err := buildObjectIndex(fileData)
	if err != nil {
		return fmt.Errorf("cad: 加载对象索引段失败: %w", err)
	}
	dynamicTypes, _ := d.loadDynamicTypes(fileData)
	ensureFixedEntityTypes(dynamicTypes)
	// 按对象图条目数预聚合容器容量（大图纸数千~数万条目，实体占多数，
	// classify 逐条写入时免 map 逐次扩容 rehash）
	d.entityByHandle = make(map[uint64]any, len(index))
	// 光度判定预扫描：NOD 字典 LIGHTINGUNITS 的 DICTIONARYVAR 值须在
	// LIGHT 实体主体解码前可知（影响位流长度），先于主循环探测
	d.lightingUnits = probeLightingUnits(index, objectsData, d, dynamicTypes)

	for _, ref := range index {
		rec, err := parseObjectRecord(objectsData, ref, d.objRecordR2010Plus())
		if err != nil {
			d.skipped++
			continue
		}
		h, err := parseObjHeader(rec)
		if err != nil {
			d.skipped++
			continue
		}
		switch h.typeCode {
		case 0x33: // LAYER
			if lc, err := decodeLayerRecord(rec, ref.handle, d.version); err == nil {
				d.layerColors[ref.handle] = lc
			}
			continue
		case 0x2A: // DICTIONARY
			r := rec.bodyBitStream()
			r.setBitPos(h.dataStartBit)
			if dd, err := decodeDictionaryObject(r, rec, d.version, d.version >= verR2013); err == nil {
				d.dictionaries[ref.handle] = dd
			} else if os.Getenv("CAD_DECODE_DBG") != "" {
				fmt.Fprintf(os.Stderr, "[dic] h=%d %v\n", ref.handle, err)
			}
			continue
		case 0x4F: // XRECORD
			r := rec.bodyBitStream()
			r.setBitPos(h.dataStartBit)
			if xx, err := decodeXrecordObject(r, rec, d.version, d.version >= verR2013); err == nil {
				d.xrecords[ref.handle] = xx
			} else if os.Getenv("CAD_DECODE_DBG") != "" {
				fmt.Fprintf(os.Stderr, "[xrec] h=%d %v\n", ref.handle, err)
			}
			continue
		}
		name := entityTypeName(h.typeCode, dynamicTypes)
		// 实体判定优先（WIPEOUT/LIGHT 等实体类不再被 UNKNOWN 对象兜底截走）
		if name == "" || !isEntityType(h.typeCode, dynamicTypes) {
			if decodeInternalObjectOK(h.typeCode, name) {
				r := rec.bodyBitStream()
				r.setBitPos(h.dataStartBit)
				if g, err := decodeInternalObject(r, rec, d.version, d.version >= verR2013, h.typeCode, name, d.codepage); err == nil {
					d.internalObjects[ref.handle] = g
				} else if os.Getenv("CAD_DECODE_DBG") != "" {
					fmt.Fprintf(os.Stderr, "[obj] h=%d type=%X %v\n", ref.handle, h.typeCode, err)
				}
			}
			continue
		}
		if name == "ATTDEF" {
			// 定义类标记：不参与渲染与文本，静默跳过
			continue
		}
		r := rec.bodyBitStream()
		r.setBitPos(h.dataStartBit)
		if isVersionedEntityKind(name) {
			// ACIS 系/WIPEOUT：版本感知专用解码（纳管进 entityByHandle）
			ent, err := decodeVersionedEntity(r, h, ref.handle, name, d.version)
			if err != nil {
				d.failBy(ref, fmt.Errorf("%s: %w", name, err))
				d.skipped++
				continue
			}
			d.classify(ent)
			continue
		}
		ent, err := decodeEntityFieldsVer(r, h, ref.handle, h.rec.size, entityTypeName(h.typeCode, dynamicTypes), h.typeCode, d.version, d.codepage, dynamicTypes, d.lightingUnits)
		if err != nil {
			d.failBy(ref, fmt.Errorf("%s: %w", entityTypeName(h.typeCode, dynamicTypes), err))
			d.skipped++
			continue
		}
		d.classify(ent)
	}
	// 对象图遍历完成后聚合 POLYLINE 顶点（VERTEX 子实体按 owner 归属）
	d.assemblePolylineChildren()
	return nil
}

// classify 按实体归属归类：entmode==2 → 模型空间；否则按 owner 归属块定义。
func (d *Document) classify(ent any) {
	ec, ok := ent.(entityCommon)
	if !ok {
		return
	}
	if d.entityByHandle == nil {
		d.entityByHandle = make(map[uint64]any)
	}
	if h := ec.common().handle; h != 0 {
		d.entityByHandle[h] = ent
	}
	switch e := ent.(type) {
	case *entAttrib:
		d.attribs[e.handle] = e
	}
	switch {
	case ec.common().mode == 1:
		// 图纸空间实体：不进模型空间渲染
	case ec.common().mode == 0:
		// 块定义内容：按 owner 归属（模型空间块头的内容渲染时按最大块启发式并入）
		if owner := ec.common().owner; owner != 0 {
			d.blocks[owner] = append(d.blocks[owner], ent)
		} else {
			d.modelSpace = append(d.modelSpace, ent)
		}
	default:
		// mode 2（模型空间）与 mode 3（未定归属）都进模型空间（对齐参考实现）
		d.modelSpace = append(d.modelSpace, ent)
	}
}

// entBase 提取实体公共字段。
func entBase(ent any) *baseEntity {
	if ec, ok := ent.(entityCommon); ok {
		return ec.common()
	}
	return nil
}

// Texts 提取图纸全部文本：模型空间直接文本 + INSERT 属性文本（递归展开块）。
func (d *Document) Texts() []TextInfo {
	var out []TextInfo
	seen := make(map[uint64]bool)
	var collect func(ent any)
	collect = func(ent any) {
		switch e := ent.(type) {
		case *entText:
			out = append(out, TextInfo{Text: stripMTextFormat(e.text), Layer: e.layer, X: e.insertion.x, Y: e.insertion.y})
		case *entMText:
			out = append(out, TextInfo{Text: stripMTextFormat(e.text), Layer: e.layer, X: e.insertion.x, Y: e.insertion.y})
		case *entAttrib:
			if !seen[e.handle] {
				seen[e.handle] = true
				out = append(out, TextInfo{Text: stripMTextFormat(e.text), Layer: e.layer, X: e.insertion.x, Y: e.insertion.y})
			}
		case *entInsert:
			if seen[e.handle] {
				return
			}
			seen[e.handle] = true
			for _, ah := range e.attribs {
				if a, ok := d.attribs[ah]; ok {
					collect(a)
				}
			}
			for _, inner := range d.blocks[e.blockHeader] {
				collect(inner)
			}
		}
	}
	for _, ent := range d.modelSpace {
		collect(ent)
	}
	return out
}

// loadDynamicTypes 解析 AcDb:Classes 动态类名表（≥500 类型码）。
// 解析失败时返回空表（基础图元类型码固定，不依赖该表）。
func (d *Document) loadDynamicTypes(fileData []byte) (map[uint16]string, error) {
	data, err := loadNamedSectionData(fileData, "AcDb:Classes")
	if err != nil {
		return nil, err
	}
	return parseClassesSection(data, d.version)
}

// ensureFixedEntityTypes 补录 objTypeCode 静态表缺失的固定实体类型码
// （REGION=0x25/3DSOLID=0x26/BODY=0x27、HATCH=0x4E 在 R2000+ 为固定码但
// 静态表未收录；OLEFRAME=0x2B/OLE2FRAME=0x4A 为固定码），使 entityTypeName
// 与 isEntityType 能将这些对象路由到实体解码器而非静默跳过。仅在动态类名表
// 无同名映射时注入，不覆盖类段解析结果。dynamic 可为 nil（CLASSES 段
// 损坏时类表不可用），此时自建映射保证固定类型码解码不受影响。
func ensureFixedEntityTypes(dynamic map[uint16]string) {
	if dynamic == nil {
		// CLASSES 段损坏（变异/畸形输入）时类表为 nil，自建映射
		// 继续注入固定类型码（批次 Q/T 模糊测试实证 nil 直接写会 panic）
		dynamic = map[uint16]string{}
	}
	fixed := map[uint16]string{
		0x25:  "REGION",
		0x26:  "3DSOLID",
		0x27:  "BODY",
		0x4E:  "HATCH", // R2000+ 固定码（R13/R14 为动态类，由类名表提供）
		0x2B:  "OLEFRAME",
		0x4A:  "OLE2FRAME",
		0x1F2: "PROXY_ENTITY",
	}
	for code, name := range fixed {
		if _, ok := dynamic[code]; !ok {
			dynamic[code] = name
		}
	}
}

// isVersionedEntityKind 判断实体类型是否需要版本感知的专用解码路径
// （ACIS 系与 WIPEOUT：entities.go 的类型分发无对应版本参数，由 cad.go
// 特判调用 decodeAcisVer/decodeWipeoutVer 后纳入 entityByHandle）。
func isVersionedEntityKind(name string) bool {
	switch name {
	case "REGION", "3DSOLID", "BODY", "WIPEOUT", "TOLERANCE", "VIEWPORT":
		return true
	}
	return false
}

// decodeVersionedEntity 版本感知实体的扫描解码：与 decodeEntityFieldsVer
// 同一候选扫描框架，主体解码按类型分发到 ACIS 系/WIPEOUT 专用解码器。
func decodeVersionedEntity(r *bitStream, h objHeader, objHandle uint64, typeName string, ver dwgVersion) (any, error) {
	dataEnd := h.rec.dataEndBit()
	startByte, startBit := r.cursor()
	base := uint64(startByte)*8 + uint64(startBit)
	parsers := headParsersForVersion(ver)
	ent, _, err := scanEntityBest(r, base, dataEnd, hdlSizeFieldBits(h), parsers, objHandle, h.rec.size, typeName, h.typeCode, func(r *bitStream, head *commonEntityHead) (any, error) {
		switch typeName {
		case "WIPEOUT":
			return decodeWipeoutVer(r, head, ver)
		case "TOLERANCE":
			return decodeToleranceVer(r, head, ver)
		case "VIEWPORT":
			return decodeViewportVer(r, head, ver)
		}
		return decodeAcisVer(r, head, typeName, ver, ver)
	})
	attachEntityRecordMeta(ent, h.rec)
	return ent, err
}

// isEntityType 判断类型码是否为可渲染实体：图元直判 + 动态类名命中常见实体后缀。
func isEntityType(code uint16, dynamic map[uint16]string) bool {
	switch objTypeCode[code] {
	case "TEXT", "ATTRIB", "ATTDEF", "INSERT", "MINSERT", "ARC", "CIRCLE", "LINE",
		"POINT", "ELLIPSE", "MTEXT", "LWPOLYLINE", "VERTEX_2D", "VERTEX_3D",
		"VERTEX_MESH", "VERTEX_PFACE", "VERTEX_PFACE_FACE",
		"POLYLINE_2D", "POLYLINE_3D", "SEQEND", "BLOCK", "ENDBLK",
		"SPLINE", "LEADER", "3DFACE", "SOLID", "RAY", "XLINE", "MLINE",
		"TOLERANCE", "POLYLINE_PFACE", "POLYLINE_MESH", "SHAPE", "VIEWPORT",
		"REGION", "3DSOLID", "BODY",
		"OLEFRAME", "OLE2FRAME", "PROXY_ENTITY",
		"DIM_ORDINATE", "DIM_LINEAR", "DIM_ALIGNED", "DIM_ANG3PT", "DIM_ANG2LN",
		"DIM_RADIUS", "DIM_DIAMETER":
		return true
	}
	if name, ok := dynamic[code]; ok {
		switch name {
		case "ACDBLINE", "ACDBCIRCLE", "ACDBARC", "ACDBPOINT", "ACDBELLIPSE",
			"ACDBMTEXT", "ACDBTEXT", "ACDBLWPOLYLINE", "ACDBINSERT", "ACDBATTRIB",
			"ACDBPOLYLINE", "ACDBSPLINE", "ACDBHATCH", "ACDBDIMENSION",
			"HATCH",                     // R13/R14 类段中 HATCH 以本名注册（type 536/539）
			"REGION", "3DSOLID", "BODY", // 注入动态表的固定码实体（0x25/26/27）
			// 实体类（gold 确认身份；专有几何暂未实现，走通用实体头）
			"ACDBWIPEOUT", "WIPEOUT", "IMAGE", "ACDBRASTERIMAGE",
			"LWPOLYLINE", // R14 类段以本名注册（type 535；R2000+ 为固定码 0x0F）
			"MPOLYGON", "ACDBMPOLYGON",
			"ACDBTABLE", "ACAD_TABLE",
			"ACDBARCALIGNEDTEXT", "ARC_DIMENSION", "MULTILEADER",
			"LARGE_RADIAL_DIMENSION", // R2000+ 大半径标注（DIMENSION 同框架解码）
			"ACDBMLINESTYLE",         /*占位无*/
			// 螺旋线（dwgread 亦仅记录 unknown_bits，无内嵌几何）
			"HELIX",
			// 底图引用（几何在外部 PDF/DGN/DWF 文件）
			"PDFUNDERLAY", "DGNUNDERLAY", "DWFUNDERLAY":
			return true
		case "LIGHT", "ACDBLIGHT":
			return true
		}
	}
	return false
}

// stripMTextFormat 剥离 MTEXT 行内格式控制码（\\P 换行、{...} 分组、\\X 等）。
func stripMTextFormat(s string) string {
	var buf bytes.Buffer
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s):
			next := s[i+1]
			switch next {
			case 'P': // 段落换行
				buf.WriteByte('\n')
				i++
			case '~': // 不换行空格
				buf.WriteByte(' ')
				i++
			case 'X': // 列分隔
				buf.WriteByte(' ')
				i++
			case 'A', 'C', 'c', 'F', 'f', 'H', 'L', 'l', 'O', 'o', 'Q', 'S', 'T', 'W':
				// 带参数指令：跳过指令字符与其参数（到 ';' 结束）
				i++
				for i < len(s) && s[i] != ';' {
					i++
				}
			case '\\':
				buf.WriteByte('\\')
				i++
			default:
				i++
			}
		case c == '{' || c == '}':
			// 分组括号剥离
		default:
			buf.WriteByte(c)
		}
	}
	return buf.String()
}

// failBy 记录单个对象的失败原因（调试辅助）。
func (d *Document) failBy(ref objectRef, err error) {
	if d.debugFailures == nil {
		d.debugFailures = map[uint64]string{}
	}
	d.debugFailures[ref.handle] = err.Error()
}

// DebugFailures 返回按句柄的失败原因（调试辅助）。
func (d *Document) DebugFailures() map[uint64]string { return d.debugFailures }

// EntityCount 返回模型空间实体数量（含最大块启发式并入的内容）。
func (d *Document) EntityCount() int {
	n := len(d.modelSpace)
	for _, list := range d.blocks {
		n += len(list)
	}
	return n
}

// parseR2000Document 解析 R2000(AC1015)/R14(AC1014) 文档：段数据不压缩，
// 对象图为 2 号段，对象记录偏移指向文件主体。
func parseR2000Document(data []byte) (*Document, error) {
	version, err := detectVersion(data)
	if err != nil {
		return nil, err
	}
	doc := &Document{
		version:     version,
		codepage:    readCodepage(data),
		blocks:      make(map[uint64][]any),
		attribs:     make(map[uint64]*entAttrib),
		layerColors: make(map[uint64]layerColor),
	}
	doc.dictionaries = make(map[uint64]*objDictionary)
	doc.xrecords = make(map[uint64]*objXrecord)
	doc.internalObjects = make(map[uint64]*objGeneric)
	objectMap, err := readR2000Section(data, r2000SecObjectMap)
	if err != nil {
		return nil, fmt.Errorf("cad: 加载对象图失败: %w", err)
	}
	refs, err := parseObjectMapHandles(objectMap)
	if err != nil {
		return nil, fmt.Errorf("cad: 解析 R2000 对象图失败: %w", err)
	}
	// 保留容器原始素材（段整段、对象区整块），供 WriteDwgR2000 回放式写出
	doc.r2000Raw = captureR2000Raw(data, refs)
	dynamicTypes, _ := doc.loadR2000Classes(data)
	ensureFixedEntityTypes(dynamicTypes)
	// 按对象图条目数预聚合实体句柄索引容量（同 decodeObjects）
	doc.entityByHandle = make(map[uint64]any, len(refs))
	doc.lightingUnits = probeLightingUnits(refs, data, doc, dynamicTypes)

	for _, ref := range refs {
		rec, err := parseObjectRecord(data, ref, false) // R2000 记录无 UMC/OT 前缀，偏移即文件内位置
		if err != nil {
			doc.skipped++
			continue
		}
		h, err := parseObjHeader(rec)
		if err != nil {
			doc.skipped++
			continue
		}
		switch h.typeCode {
		case 0x33: // LAYER
			if lc, err := decodeLayerRecord(rec, ref.handle, doc.version); err == nil {
				doc.layerColors[ref.handle] = lc
			}
			continue
		case 0x2A: // DICTIONARY
			r := rec.bodyBitStream()
			r.setBitPos(h.dataStartBit)
			if dd, err := decodeDictionaryObject(r, rec, doc.version, false); err == nil {
				doc.dictionaries[ref.handle] = dd
			}
			continue
		case 0x4F: // XRECORD
			r := rec.bodyBitStream()
			r.setBitPos(h.dataStartBit)
			if xx, err := decodeXrecordObject(r, rec, doc.version, false); err == nil {
				doc.xrecords[ref.handle] = xx
			}
			continue
		}
		name := entityTypeName(h.typeCode, dynamicTypes)
		if name == "XRECORD" {
			// R13/R14 的 XRECORD 为类类型（type≥500 经类名表解析），非固定 0x4F
			r := rec.bodyBitStream()
			r.setBitPos(h.dataStartBit)
			if xx, err := decodeXrecordObject(r, rec, doc.version, false); err == nil {
				doc.xrecords[ref.handle] = xx
			}
			continue
		}
		// 实体判定优先（同 decodeObjects）
		if name != "" && isEntityType(h.typeCode, dynamicTypes) {
			// 落到下方实体解码
		} else if decodeInternalObjectOK(h.typeCode, name) {
			r := rec.bodyBitStream()
			r.setBitPos(h.dataStartBit)
			if g, err := decodeInternalObject(r, rec, doc.version, false, h.typeCode, name, doc.codepage); err == nil {
				doc.internalObjects[ref.handle] = g
			} else if os.Getenv("CAD_DECODE_DBG") != "" {
				fmt.Fprintf(os.Stderr, "[obj] h=%d type=%X %v\n", ref.handle, h.typeCode, err)
			}
			continue
		} else if name == "" {
			continue
		}
		r := rec.bodyBitStream()
		r.setBitPos(h.dataStartBit)
		if isVersionedEntityKind(name) {
			// ACIS 系/WIPEOUT：版本感知专用解码（纳管进 entityByHandle）
			ent, err := decodeVersionedEntity(r, h, ref.handle, name, doc.version)
			if err != nil {
				doc.failBy(ref, fmt.Errorf("%s: %w", name, err))
				doc.skipped++
				continue
			}
			doc.classify(ent)
			continue
		}
		ent, err := decodeEntityFieldsVer(r, h, ref.handle, h.rec.size, entityTypeName(h.typeCode, dynamicTypes), h.typeCode, doc.version, doc.codepage, dynamicTypes, doc.lightingUnits)
		if err != nil {
			doc.failBy(ref, fmt.Errorf("%s: %w", entityTypeName(h.typeCode, dynamicTypes), err))
			doc.skipped++
			continue
		}
		doc.classify(ent)
	}
	// 对象图遍历完成后聚合 POLYLINE 顶点（VERTEX 子实体按 owner 归属）
	doc.assemblePolylineChildren()
	return doc, nil
}

// loadR2000Classes R2000 类名表：名字以 TV 直接在主流。
func (d *Document) loadR2000Classes(data []byte) (map[uint16]string, error) {
	classData, err := readR2000Section(data, r2000SecClasses)
	if err != nil {
		return nil, err
	}
	return parseClassesSectionR13R15(classData)
}

// DebugObjectIndexExport 调试用：导出对象图。
func DebugObjectIndexExport(data []byte) ([]struct {
	Handle uint64
	Offset uint32
}, error) {
	refs, err := buildObjectIndex(data)
	if err != nil {
		return nil, err
	}
	out := make([]struct {
		Handle uint64
		Offset uint32
	}, 0, len(refs))
	for _, r := range refs {
		out = append(out, struct {
			Handle uint64
			Offset uint32
		}{r.handle, r.offset})
	}
	return out, nil
}

// DebugRecord2 调试用：解析记录并返回 body。
func DebugRecord2(objectsData []byte, ref struct {
	Handle uint64
	Offset uint32
}, r2010Plus bool) (body []byte, bitOff uint64, size uint32, err error) {
	rec, err := parseObjectRecord(objectsData, objectRef{ref.Handle, ref.Offset}, r2010Plus)
	if err != nil {
		return nil, 0, 0, err
	}
	return rec.body, rec.bodyBitOffset, rec.size, nil
}

// LoadNamedSectionDebug2 调试用：加载段数据。
func LoadNamedSectionDebug2(data []byte, name string) ([]byte, error) {
	return loadNamedSectionData(data, name)
}

// DebugLines 调试用：输出全部 LINE 几何（handle → 6 坐标），用于与参考实现对照。
func DebugLines(data []byte) map[uint64][6]float64 {
	doc, err := Parse(data)
	out := map[uint64][6]float64{}
	if err != nil {
		return out
	}
	var walk func(ents []any)
	walk = func(ents []any) {
		for _, e := range ents {
			if l, ok := e.(*entLine); ok {
				out[l.handle] = [6]float64{l.start.x, l.start.y, l.start.z, l.end.x, l.end.y, l.end.z}
			}
		}
	}
	walk(doc.modelSpace)
	for _, list := range doc.blocks {
		walk(list)
	}
	return out
}

// ---- 对照诊断导出：与参考实现输出做逐字段对比用 ----

// DumpEntityRow 单个实体的对照字段（键名与参考实现对齐）。
type DumpEntityRow struct {
	Handle uint64         `json:"handle"`
	Type   string         `json:"type"`
	DXF    map[string]any `json:"dxf"`
}

// dumpEntityCommonKeys 全实体公共键序列（批次 B 全键对齐：entity/handle/
// type 公共头标量 + color 嵌套系 + 句柄系 + EED 展平对；[i] 为数组占位，
// 由 expandAuditKeys 按实体实际数组长度展开，条件缺省键经 entityField
// 返回 nil 自动跳过——与 gold 条件输出语义一致）。
var dumpEntityCommonKeys = []string{
	"entity", "handle", "type", "entmode", "bitsize", "size",
	"color", "color.index", "color.rgb", "color.flag",
	"color.alpha_raw", "color.alpha_type", "color.alpha",
	"layer", "ownerhandle", "ltype_scale", "ltype_flags",
	"plotstyle_flags", "plotstyle", "material_flags", "shadow_flags",
	"invisible", "linewt", "nolinks", "isbylayerlt",
	"is_xdic_missing", "has_ds_data",
	"has_full_visualstyle", "has_face_visualstyle", "has_edge_visualstyle",
	"preview_exists", "preview_is_proxy", "preview_size", "preview",
	"prev_entity", "next_entity", "xdicobjhandle",
	"eed[i].code", "eed[i].size", "eed[i].value",
}

// entityGoldKeys gold entity 名 → per-type 键序列（对齐 out_json 键集；
// [i] 数组占位同公共键。本库未建模的键不列入——VIEWPORT 子对象句柄组、
// SPLINE tan 向量、MTEXT 背景长尾、acis_data 数组（roundtrip 字节对照
// 覆盖）等，见测试豁免清单）。
var entityGoldKeys = map[string][]string{
	"LINE":    {"start", "end", "thickness", "extrusion", "z_is_zero"},
	"CIRCLE":  {"center", "radius", "thickness", "extrusion"},
	"ARC":     {"center", "radius", "start_angle", "end_angle", "thickness", "extrusion"},
	"POINT":   {"location", "x", "y", "z", "x_ang", "thickness", "extrusion"},
	"ELLIPSE": {"center", "sm_axis", "major_axis", "axis_ratio", "start_angle", "end_angle", "extrusion"},
	"LWPOLYLINE": {"flag", "points", "bulges", "vertexids", "widths[i].start", "widths[i].end",
		"elevation", "const_width", "thickness", "extrusion"},
	"TEXT": {"text_value", "ins_pt", "height", "rotation", "dataflags", "horiz_alignment",
		"vert_alignment", "alignment_pt", "generation", "thickness", "elevation",
		"oblique_angle", "width_factor", "extrusion", "style"},
	"MTEXT": {"ins_pt", "x_axis_dir", "rect_width", "rect_height", "text_height", "attachment",
		"text", "extrusion", "flow_dir", "extents_height", "extents_width",
		"linespace_style", "linespace_factor", "unknown_b0", "bg_fill_flag",
		"class_version", "column_type", "default_flag", "ignore_attachment",
		"is_not_annotative", "style"},
	"INSERT": {"ins_pt", "scale", "rotation", "scale_flag", "has_attribs", "block_header",
		"attribs", "first_attrib", "last_attrib", "extrusion", "seqend"},
	"MINSERT": {"ins_pt", "scale", "rotation", "scale_flag", "has_attribs", "block_header",
		"attribs", "first_attrib", "last_attrib", "extrusion", "seqend"},
	"ATTRIB": {"text_value", "tag", "ins_pt", "height", "rotation", "horiz_alignment",
		"vert_alignment", "alignment_pt", "generation", "extrusion", "style"},
	"SOLID":             {"corner1", "corner2", "corner3", "corner4", "thickness", "elevation", "extrusion"},
	"TRACE":             {"corner1", "corner2", "corner3", "corner4", "thickness", "elevation", "extrusion"},
	"3DFACE":            {"corner1", "corner2", "corner3", "corner4", "has_no_flags", "z_is_zero", "invis_flags"},
	"VERTEX_2D":         {"point", "bulge", "flag", "tangent_dir", "start_width", "end_width"},
	"VERTEX_3D":         {"point", "flag"},
	"VERTEX_MESH":       {"point", "flag"},
	"VERTEX_PFACE":      {"point", "flag"},
	"VERTEX_PFACE_FACE": {"flag", "vertind"},
	"POLYLINE_2D": {"flag", "curve_type", "start_width", "end_width", "thickness", "elevation",
		"first_vertex", "last_vertex", "vertex", "extrusion", "seqend"},
	"POLYLINE_3D":    {"flag", "curve_type", "first_vertex", "last_vertex", "vertex", "seqend"},
	"POLYLINE_PFACE": {"numverts", "numfaces", "first_vertex", "last_vertex", "vertex", "seqend"},
	"POLYLINE_MESH":  {"flag", "curve_type", "m_density", "n_density"},
	"BLOCK":          {"name"},
	"SPLINE": {"scenario", "degree", "fit_tol", "knot_tol", "ctrl_tol", "knots",
		"ctrl_pts[i].x", "ctrl_pts[i].y", "ctrl_pts[i].z", "weights", "fit_pts",
		"rational", "closed_b", "periodic", "weighted", "style",
		"splineflags", "knotparam", "beg_tan_vec", "end_tan_vec"},
	"RAY":   {"point", "vector"},
	"XLINE": {"point", "vector"},
	"MLINE": {"scale", "justification", "flags", "base_point", "extrusion", "mlinestyle",
		"verts[i].vertex", "verts[i].vertex_direction", "verts[i].miter_direction",
		"verts[i].lines[j].segparms", "verts[i].lines[j].areafillparms"},
	"HATCH": {"elevation", "extrusion", "name", "is_solid_fill", "is_associative", "style",
		"pattern_type", "angle", "scale_spacing", "double_flag", "is_gradient_fill",
		"reserved", "gradient_angle", "gradient_shift", "single_color_gradient",
		"gradient_tint", "gradient_name", "has_derived", "pixel_size",
		"colors[i].shift_value", "colors[i].color.index", "colors[i].color.rgb",
		"deflines[i].angle", "deflines[i].pt0", "deflines[i].offset", "deflines[i].dashes",
		"paths[i].flag", "paths[i].bulges_present", "paths[i].closed", "paths[i].num_segs_or_paths",
		"paths[i].segs[j].curve_type", "paths[i].segs[j].first_endpoint", "paths[i].segs[j].second_endpoint",
		"paths[i].segs[j].center", "paths[i].segs[j].radius", "paths[i].segs[j].minor_major_ratio",
		"paths[i].segs[j].start_angle", "paths[i].segs[j].end_angle", "paths[i].segs[j].is_ccw",
		"paths[i].segs[j].endpoint", "paths[i].segs[j].degree", "paths[i].segs[j].is_rational",
		"paths[i].segs[j].is_periodic", "paths[i].segs[j].num_knots", "paths[i].segs[j].num_control_points",
		"paths[i].segs[j].num_fitpts", "paths[i].segs[j].knots", "paths[i].segs[j].weights",
		"paths[i].segs[j].control_points[k].point", "paths[i].segs[j].fitpts[l].point",
		"paths[i].polyline_paths[k].point", "paths[i].polyline_paths[k].bulge", "seeds",
		"paths[i].boundary_handles"},
	"MPOLYGON": {"style", "style_tail", "x_dir", "elevation", "name", "is_solid_fill",
		"is_associative", "style", "pattern_type", "angle", "scale_spacing", "double_flag",
		"deflines[i].angle", "deflines[i].pt0", "deflines[i].offset", "deflines[i].dashes",
		"paths[i].flag", "paths[i].bulges_present", "paths[i].closed", "paths[i].num_segs_or_paths",
		"paths[i].polyline_paths[k].point", "paths[i].polyline_paths[k].bulge"},
	"WIPEOUT": {"class_version", "pt0", "uvec", "vvec", "image_size", "display_props",
		"clipping", "brightness", "contrast", "fade", "clip_mode", "clip_boundary_type",
		"clip_verts", "imagedef", "imagedefreactor"},
	"IMAGE": {"class_version", "pt0", "uvec", "vvec", "image_size", "display_props",
		"clipping", "brightness", "contrast", "fade", "clip_mode", "clip_boundary_type",
		"clip_verts", "imagedef", "imagedefreactor"},
	"TOLERANCE": {"text_value", "unknown_short", "ins_pt", "x_direction", "extrusion",
		"height", "dimgap", "dimstyle"},
	"VIEWPORT": {"center", "width", "height", "view_target", "VIEWDIR", "VIEWTWIST",
		"VIEWSIZE", "LENSLENGTH", "FRONTZ", "BACKZ", "SNAPANG", "VIEWCTR", "SNAPBASE",
		"SNAPUNIT", "GRIDUNIT", "circle_zoom", "grid_major", "status_flag", "style_sheet",
		"render_mode", "UCSVP", "ucs_at_origin", "UCSORG", "UCSXDIR", "UCSYDIR",
		"ucs_elevation", "UCSORTHOVIEW", "shadeplot_mode", "use_default_lights",
		"default_lighting_type", "brightness", "contrast",
		"ambient_color.index", "ambient_color.rgb", "ambient_color.flag"},
	"LEADER": {"annot_type", "path_type", "unknown_bit_1", "arrowhead_on", "arrowhead_type",
		"box_height", "box_width", "hookline_dir", "hookline_on", "dimgap", "dimasz",
		"unknown_short_1", "byblock_color", "unknown_bit_2", "unknown_bit_3",
		"unknown_bit_4", "unknown_bit_5", "points"},
	"DIMENSION_ORDINATE":     dimGoldKeys(),
	"DIMENSION_LINEAR":       dimGoldKeys(),
	"DIMENSION_ALIGNED":      dimGoldKeys(),
	"DIMENSION_ANG3PT":       dimGoldKeys(),
	"DIMENSION_ANG2LN":       dimGoldKeys(),
	"DIMENSION_RADIUS":       dimGoldKeys(),
	"DIMENSION_DIAMETER":     dimGoldKeys(),
	"ARC_DIMENSION":          dimGoldKeys(),
	"LARGE_RADIAL_DIMENSION": dimGoldKeys(),
	"DIMENSION_ANGULAR":      dimGoldKeys(),
	"3DSOLID":                acisGoldKeys(),
	"REGION":                 acisGoldKeys(),
	"BODY":                   acisGoldKeys(),
	"OLE2FRAME":              {"mode", "lock_aspect", "data"},
	"OLEFRAME":               {"mode", "data"},
	"LIGHT": {"class_version", "name", "status", "light_color", "light_color.index",
		"light_color.rgb", "light_color.flag", "plot_glyph", "intensity", "position",
		"target", "attenuation_type", "use_attenuation_limits", "attenuation_start_limit",
		"attenuation_end_limit", "hotspot_angle", "falloff_angle", "cast_shadows",
		"shadow_type", "shadow_map_size", "shadow_map_softness"},
	"MULTILEADER": {"class_version", "mleaderstyle", "flags", "line_color", "line_color.index",
		"line_color.rgb", "line_color.flag", "line_ltype", "line_linewt", "has_landing",
		"has_dogleg", "landing_dist", "arrow_handle", "arrow_size", "style_content",
		"text_style", "text_left", "text_right", "text_angletype", "text_alignment",
		"text_color", "text_color.index", "text_color.rgb", "text_color.flag",
		"has_text_frame", "block_style", "block_color", "block_color.index",
		"block_color.rgb", "block_color.flag", "block_scale", "block_rotation",
		"style_attachment", "is_annotative", "arrowheads[i].is_default",
		"blocklabels[i].label_text", "blocklabels[i].ui_index", "blocklabels[i].width",
		"is_neg_textdir", "ipe_alignment", "justification", "scale_factor",
		"attach_dir", "attach_top", "attach_bottom", "is_text_extended",
		"ctx.num_leaders", "ctx.leaders[i].has_lastleaderlinepoint",
		"ctx.leaders[i].has_dogleg", "ctx.leaders[i].lastleaderlinepoint",
		"ctx.leaders[i].dogleg_vector", "ctx.leaders[i].branch_index",
		"ctx.leaders[i].dogleg_length", "ctx.leaders[i].attach_dir",
		"ctx.leaders[i].lines[j].points", "ctx.leaders[i].lines[j].line_index",
		"ctx.leaders[i].lines[j].type", "ctx.leaders[i].lines[j].color.rgb",
		"ctx.leaders[i].lines[j].color.index", "ctx.leaders[i].lines[j].linewt",
		"ctx.leaders[i].lines[j].arrow_size", "ctx.leaders[i].lines[j].arrow_handle",
		"ctx.leaders[i].lines[j].flags", "ctx.leaders[i].lines[j].ltype",
		"ctx.scale_factor", "ctx.content_base", "ctx.text_height", "ctx.arrow_size",
		"ctx.landing_gap", "ctx.text_left", "ctx.text_right", "ctx.text_angletype",
		"ctx.text_alignment", "ctx.has_content_txt", "ctx.content.txt.default_text",
		"ctx.content.txt.normal", "ctx.content.txt.style", "ctx.content.txt.location",
		"ctx.content.txt.direction", "ctx.content.txt.rotation", "ctx.content.txt.width",
		"ctx.content.txt.height", "ctx.content.txt.line_spacing_factor",
		"ctx.content.txt.line_spacing_style", "ctx.content.txt.color",
		"ctx.content.txt.color.rgb",
		"ctx.content.txt.color.index", "ctx.content.txt.alignment", "ctx.content.txt.flow",
		"ctx.content.txt.bg_color",
		"ctx.content.txt.bg_color.rgb", "ctx.content.txt.bg_color.index",
		"ctx.content.txt.bg_scale", "ctx.content.txt.bg_transparency",
		"ctx.content.txt.is_bg_fill", "ctx.content.txt.is_bg_mask_fill",
		"ctx.content.txt.col_type", "ctx.content.txt.is_height_auto",
		"ctx.content.txt.col_width", "ctx.content.txt.col_gutter",
		"ctx.content.txt.is_col_flow_reversed", "ctx.content.txt.num_col_sizes",
		"ctx.content.txt.col_sizes", "ctx.content.txt.word_break", "ctx.content.txt.unknown",
		"ctx.has_content_blk", "ctx.content.blk.block_table", "ctx.content.blk.normal",
		"ctx.content.blk.location", "ctx.content.blk.scale", "ctx.content.blk.rotation",
		"ctx.content.blk.color", "ctx.content.blk.color.rgb", "ctx.content.blk.color.index",
		"ctx.content.blk.transform", "ctx.base", "ctx.base_dir", "ctx.base_vert",
		"is_normal_reversed", "ctx.is_normal_reversed", "ctx.text_top", "ctx.text_bottom"},
	"SHAPE": {"ins_pt", "scale", "rotation", "width_factor", "oblique_angle",
		"thickness", "style_id", "extrusion"},
	"PROXY_ENTITY": {"proxy_id", "version", "maint_version", "dwg_version", "from_dxf",
		"data_numbits", "num_objids", "proxy_data_size"},
	"UNKNOWN_ENT": {"entity", "type", "dxfname"},
}

// dimGoldKeys DIMENSION 族公共键序列（点组/句柄系 + spec 标量全键；
// 类型专属键由 entityField 按类型名返回 nil 自动跳过）。
func dimGoldKeys() []string {
	return []string{
		"class_version", "elevation", "extrusion", "def_pt", "text_midpt",
		"flag", "flag1", "flag2", "user_text", "text_rotation", "horiz_dir",
		"ins_scale", "ins_rotation", "attachment", "lspace_style", "lspace_factor",
		"act_measurement", "unknown", "flip_arrow1", "flip_arrow2",
		"oblique_angle", "dim_rotation", "xline1_pt", "xline2_pt",
		"feature_location_pt", "leader_endpt", "dimstyle", "block",
		"is_partial", "arc_start_param", "arc_end_param", "has_leader",
		"leader1_pt", "leader2_pt", "leader_len",
		"clone_ins_pt", "center_pt",
		"xline1start_pt", "xline1end_pt", "xline2start_pt", "xline2end_pt",
	}
}

// acisGoldKeys ACIS 系（REGION/3DSOLID/BODY）键序列（acis_data/
// encr_sat_data 数组由 roundtrip 字节对照覆盖，不进 JSON 导出）。
func acisGoldKeys() []string {
	return []string{
		"acis_empty", "unknown", "version", "wireframe_data_present",
		"point_present", "point", "isolines", "isoline_present",
		"wires[i].type", "wires[i].selection_marker", "wires[i].color",
		"wires[i].acis_index", "wires[i].transform_present",
		"wires[i].has_rotation", "wires[i].has_reflection", "wires[i].has_shear",
		"silhouettes[i].vp_id", "silhouettes[i].vp_perspective", "silhouettes[i].has_wires",
		"silhouettes[i].wires[j].type", "silhouettes[i].wires[j].selection_marker",
		"materials[i].array_index", "materials[i].mat_absref",
		"acis_empty_bit", "has_revision_guid", "revision_major", "revision_minor1",
		"revision_minor2", "revision_bytes", "end_marker",
		"history_id",
	}
}

// expandAuditKeys 展开键模式表：含 [i]/[j]/[k] 占位的模式按实体实际
// 数组长度递归展开（占位下标从 0 递增，以 entityField 返回 nil 终止，
// 与 gold 条件输出语义一致）；宿主键形态（父层占位无直接值，如
// paths[i].segs[j].*）以内层首个展开值判定非空。
func expandAuditKeys(ent any, patterns []string) []string {
	var out []string
	for _, p := range patterns {
		out = append(out, expandOneKey(ent, p)...)
	}
	return out
}

// expandOneKey 展开单个键模式（取首个 [i]/[j]/[k] 占位符逐一下标步进；
// 已替换层不影响后续占位识别。混合路径数组的空窗容忍 256——多段线/边集
// 混排的 HATCH paths 中某层无子键时继续步进，连续空窗超限才终止）。
func expandOneKey(ent any, pattern string) []string {
	if !strings.Contains(pattern, "[i]") && !strings.Contains(pattern, "[j]") &&
		!strings.Contains(pattern, "[k]") {
		if entityField(ent, pattern) == nil {
			return nil
		}
		return []string{pattern}
	}
	idx := len(pattern)
	for _, ph := range []string{"[i]", "[j]", "[k]"} {
		if p := strings.Index(pattern, ph); p >= 0 && p < idx {
			idx = p
		}
	}
	var out []string
	empty := 0
	for i := 0; i < 65536; i++ {
		stepped := pattern[:idx] + "[" + strconv.Itoa(i) + "]" + pattern[idx+3:]
		if inner := expandOneKey(ent, stepped); len(inner) > 0 {
			out = append(out, inner...)
			empty = 0
			continue
		}
		// 单层模式（stepped 已无占位）遇空即终止；双层模式容忍空窗
		// （多段线/边集混排的 HATCH paths 某层无子键时继续步进）
		if !strings.Contains(stepped, "[i]") && !strings.Contains(stepped, "[j]") &&
			!strings.Contains(stepped, "[k]") {
			break
		}
		empty++
		if empty > 16 {
			break
		}
	}
	return out
}

// colorResolved 计算解析后的颜色（实体颜色优先，其次图层颜色）。
func (d *Document) colorResolved(base *baseEntity) (any, any) {
	if base.color.hasTrue {
		return nil, base.color.trueColor & 0x00FFFFFF
	}
	if base.color.hasIndex && base.color.index != 0 && base.color.index != 256 && base.color.index != 257 {
		return int64(base.color.index), nil
	}
	if lc, ok := d.layerColors[base.layer]; ok {
		if lc.hasTrue {
			return nil, lc.trueColor & 0x00FFFFFF
		}
		if lc.index != 0 && lc.index != 256 {
			return int64(lc.index), nil
		}
	}
	return nil, nil
}

// commonDXF 公共字段。
func (d *Document) commonDXF(base *baseEntity) map[string]any {
	owner := any(nil)
	if base.owner != 0 {
		owner = base.owner
	}
	ci, tc := d.colorResolved(base)
	return map[string]any{
		"owner_handle": owner,
		"color_index":  ci,
		"true_color":   tc,
		"layer_handle": base.layer,
	}
}

// DumpEntities 输出模型空间全部实体的对照 JSON（handle/type/字段），
// 用于与参考实现输出做逐字段一致性对比。批次 B 全键对齐：dxf 覆盖
// out_json 公共键 + per-type 键（几何数组/颜色嵌套/EED/句柄系），值
// 经 entityField 统一导出，另保留本库自有口径键（text/insert 等）供
// 既有消费方使用。
func DumpEntities(data []byte) (string, error) {
	doc, err := Parse(data)
	if err != nil {
		return "", err
	}
	return dumpEntities(doc)
}

// DumpEntitiesDoc 文档级对照导出：ParseJSON 等外部构造的文档与 Parse
// 同口径（JSON 输入长尾段的输出对照入口）。
func DumpEntitiesDoc(doc *Document) (string, error) {
	return dumpEntities(doc)
}

// dumpEntities 文档级导出主体：模型空间实体逐个展开 gold 键 + 自有键。
func dumpEntities(doc *Document) (string, error) {
	rows := make([]DumpEntityRow, 0, len(doc.modelSpace)+len(doc.blocks)+64)
	for _, ent := range doc.modelSpaceEntities() {
		base := entBase(ent)
		if base == nil {
			continue
		}
		row := DumpEntityRow{Handle: base.handle, DXF: dxfOf(doc, ent, base)}
		if t := dimGoldEntityName(base.typeName); t != "" {
			row.Type = t
		} else {
			row.Type = "UNKNOWN"
		}
		rows = append(rows, row)
	}
	// JSON 无法表达 Inf/NaN：错位候选解出的非法浮点统一置为 null，
	// 保证 dump 始终是合法 JSON。
	for i := range rows {
		rows[i].DXF = sanitizeJSONFloats(rows[i].DXF).(map[string]any)
	}
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(rows); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// sanitizeJSONFloats 递归遍历 map/slice，将 Inf/NaN 浮点替换为 nil。
func sanitizeJSONFloats(v any) any {
	switch t := v.(type) {
	case float64:
		if math.IsInf(t, 0) || math.IsNaN(t) {
			return nil
		}
		return t
	case map[string]any:
		for k, val := range t {
			t[k] = sanitizeJSONFloats(val)
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = sanitizeJSONFloats(val)
		}
		return t
	default:
		return v
	}
}

// dxfOf 组装实体与参考实现对齐的字段集：gold 全键（entityGoldKeys +
// 公共键序列）经 entityField 导出，另保留本库自有口径键（text/insert/
// xscale 等既有消费方依赖）。
func dxfOf(d *Document, ent any, base *baseEntity) map[string]any {
	dxf := d.commonDXF(base)
	// gold 全键：公共序列 + per-type 序列（[i] 占位按实体数组展开）
	keys := make([]string, 0, len(dumpEntityCommonKeys)+48)
	seen := map[string]bool{}
	add := func(k string) {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for _, k := range dumpEntityCommonKeys {
		add(k)
	}
	if tk, ok := entityGoldKeys[dimGoldEntityName(base.typeName)]; ok {
		for _, k := range tk {
			add(k)
		}
	}
	for _, k := range expandAuditKeys(ent, keys) {
		if v := entityField(ent, k); v != nil {
			dxf[k] = v
		}
	}
	// 自有口径键（既有消费方：文本提取一致性、几何渲染消费）
	switch e := ent.(type) {
	case *entLine:
		dxf["start"] = p3slice(e.start)
		dxf["end"] = p3slice(e.end)
	case *entCircle:
		dxf["center"] = p3slice(e.center)
		dxf["radius"] = e.radius
	case *entArc:
		dxf["center"] = p3slice(e.center)
		dxf["radius"] = e.radius
		dxf["angle_start"] = rad2deg(e.angleStart)
		dxf["angle_end"] = rad2deg(e.angleEnd)
	case *entPoint:
		dxf["location"] = p3slice(e.location)
	case *entEllipse:
		dxf["center"] = p3slice(e.center)
		dxf["major_axis"] = p3slice(e.majorAxis)
		dxf["axis_ratio"] = e.ratio
	case *entLwPolyline:
		pts := make([][]float64, 0, len(e.vertices))
		for _, v := range e.vertices {
			pts = append(pts, []float64{v.x, v.y, 0})
		}
		dxf["own_points"] = pts
		dxf["own_flags"] = int64(e.flags)
		dxf["closed"] = e.isClosedByGeometry()
	case *entText:
		dxf["text"] = e.text
		dxf["insert"] = p3slice(e.insertion)
		dxf["height"] = e.height
		dxf["rotation_deg"] = rad2deg(e.rotation)
	case *entMText:
		dxf["own_text"] = stripMTextFormat(e.text)
		dxf["insert"] = p3slice(e.insertion)
		dxf["own_text_height"] = e.textHeight
		dxf["own_rect_width"] = e.rectWidth
	case *entInsert:
		dxf["own_insert"] = p3slice(e.position)
		dxf["xscale"] = e.scale.x
		dxf["yscale"] = e.scale.y
		dxf["zscale"] = e.scale.z
		dxf["rotation_deg"] = rad2deg(e.rotation)
	case *entAttrib:
		dxf["own_text"] = e.text
		dxf["own_insert"] = p3slice(e.insertion)
		dxf["own_height"] = e.height
	}
	return dxf
}

// p3slice 3D 点转数组。
func p3slice(p point3) []float64 { return []float64{p.x, p.y, p.z} }

// p2slice 2D 点转数组。
func p2slice(p point2) []float64 { return []float64{p.x, p.y} }

// rad2deg 弧度转角度（参考实现以角度对外）。
func rad2deg(rad float64) float64 { return rad * 180 / math.Pi }

// largestBlockHeader 返回实体数最多的块定义句柄（模型空间块头启发式）。
func (d *Document) largestBlockHeader() uint64 {
	bestHandle := uint64(0)
	bestLen := 0
	for h, list := range d.blocks {
		if len(list) > bestLen {
			bestLen = len(list)
			bestHandle = h
		}
	}
	return bestHandle
}

// largestBlockEntities 返回实体数最多的块定义内容。
func (d *Document) largestBlockEntities() []any {
	bestHandle := d.largestBlockHeader()
	if bestHandle == 0 {
		return nil
	}
	return d.blocks[bestHandle]
}

// modelSpaceEntities 模型空间完整实体集：直属实体 + 模型空间块头内容。
func (d *Document) modelSpaceEntities() []any {
	out := make([]any, 0, len(d.modelSpace)+len(d.blocks))
	out = append(out, d.modelSpace...)
	out = append(out, d.largestBlockEntities()...)
	return out
}

// DebugScanGoldLines 调试用：扫描全部原始对象图条目，
// 找出解码后坐标与 gold 匹配的 LINE 记录及其句柄/偏移。
func DebugScanGoldLines(data []byte, gold map[uint64][6]float64) []string {
	objectsData, err := loadNamedSectionData(data, "AcDb:AcDbObjects")
	if err != nil {
		return []string{"seg: " + err.Error()}
	}
	refs, err := parseObjectMapHandles(mustHandles(data))
	if err != nil {
		return []string{"idx: " + err.Error()}
	}
	var out []string
	for _, ref := range refs {
		g, ok := gold[ref.handle]
		if !ok || int(ref.offset)+64 > len(objectsData) {
			continue
		}
		rec, err := parseObjectRecord(objectsData, ref, true)
		if err != nil {
			continue
		}
		h, err := parseObjHeader(rec)
		if err != nil || h.typeCode != 0x13 {
			continue
		}
		r := rec.bodyBitStream()
		r.setBitPos(h.dataStartBit)
		ent, err := decodeEntityFieldsVer(r, h, ref.handle, h.rec.size, "LINE", 30, verR2018, 0, nil, "")
		if err != nil {
			continue
		}
		line := ent.(*entLine)
		if near(line.start.x, g[0]) && near(line.start.y, g[1]) && near(line.end.x, g[3]) && near(line.end.y, g[4]) {
			out = append(out, fmt.Sprintf("handle=%d offset=%d 匹配", ref.handle, ref.offset))
		}
	}
	return out
}

// mustHandles 调试用。
func mustHandles(data []byte) []byte {
	b, _ := loadNamedSectionData(data, "AcDb:Handles")
	return b
}

// DebugLayerColors 调试用：输出已解析的图层颜色映射。
func (d *Document) DebugLayerColors() map[uint64]string {
	out := map[uint64]string{}
	for h, lc := range d.layerColors {
		s := fmt.Sprintf("idx=%d", lc.index)
		if lc.hasTrue {
			s += fmt.Sprintf(" true=%#06x", lc.trueColor)
		}
		out[h] = s
	}
	return out
}

// DebugObjectBody 调试用：导出指定句柄对象的原始 body 位流与起始位偏移
// （bodyBitOffset 为 MS 字段结束处在首字节内的位偏移，用于位级对账）。
func DebugObjectBody(data []byte, handle uint64) (body []byte, bitOffset uint64, err error) {
	objectsData, err := loadNamedSectionData(data, "AcDb:AcDbObjects")
	if err != nil {
		return nil, 0, err
	}
	index, err := buildObjectIndex(data)
	if err != nil {
		return nil, 0, err
	}
	ver, err := detectVersion(data)
	if err != nil {
		return nil, 0, err
	}
	r2010Plus := ver == verR2010 || ver == verR2013 || ver == verR2018
	var found *objectRef
	for i := range index {
		if index[i].handle == handle {
			found = &index[i] // 与 Parse 一致：同句柄取最后一次出现
		}
	}
	if found == nil {
		return nil, 0, fmt.Errorf("cad: 对象 %d 不在对象图", handle)
	}
	rec, err := parseObjectRecord(objectsData, *found, r2010Plus)
	if err != nil {
		return nil, 0, err
	}
	return rec.body, rec.bodyBitOffset, nil
}
