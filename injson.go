// injson.go 实现 LibreDWG dwgread -O JSON 风格 JSON 的逆向解析：
// gold JSON（OBJECTS 数组 + HEADER/FILEHEADER 段）→ Document 模型，与
// Parse（DWG 字节流）殊途同归。实体按 entity 键分发到 entXxx 构造
// （字段从 gold 键反向填充，即 entityField 导出器的逆映射，手工
// per-type setter 保证类型安全）；内部对象按 object 键以 objGeneric
// 的 Fields 中间表示收录；LAYER 对象映射到 layerColors。
//
// 写出边界：Parse 的 WriteDwg 路径依赖解析时捕获的容器回放素材
// （r2000Raw/r2004Raw/r2007Raw），JSON 输入天然没有这些素材，因此
// ParseJSON 产出的文档无法 WriteDwg（返回"缺少回放素材"错误）——
// 结构化正向编码（实体侧正向写位流）未建；本文件的降级目标是消费侧
// 完整：Document 可供 RenderPNG/Texts/modelSpaceEntities 审计对照使用。
package cad

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// ParseJSON 解析 LibreDWG dwgread -O JSON 风格 JSON 为 Document。
// 版本取 HEADER.$ACADVER（约定键），缺失时回退 FILEHEADER.version
// （九样本 gold 实际携带位置）。缺 OBJECTS 数组返回错误；未知实体类
// 跳过并计入 Skipped（对齐解析器兜底惯例，不阻断其余对象）。
// HEADER 段全量键值存入 Document.HeaderVars（原样保存不逐个建模，
// 查询走 HeaderVar 的 $ 前缀容错），渲染相关键（EXTMIN/EXTMAX/INSBASE/
// LTSCALE）另提升为结构化字段。
func ParseJSON(data []byte) (*Document, error) {
	// dwgread 对非数值 BD 输出裸 nan/-nan/NaN 字面量，标准 json 不支持，
	// 预处理在值位置归一为 null（与 gold 侧非数值语义等价）。
	if bytes.Contains(data, []byte("nan")) {
		re := regexp.MustCompile(`([:\[,]\s*)-?[nN]a[nN]`)
		data = re.ReplaceAll(data, []byte("${1}null"))
	}
	root := map[string]any{}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("cad: JSON 解析失败: %w", err)
	}
	objsRaw, ok := root["OBJECTS"].([]any)
	if !ok {
		return nil, fmt.Errorf("cad: JSON 缺少 OBJECTS 数组，不是 dwgread -O JSON 输出")
	}
	doc := &Document{
		version:         jsonDetectVersion(root),
		blocks:          make(map[uint64][]any),
		attribs:         make(map[uint64]*entAttrib),
		layerColors:     make(map[uint64]layerColor),
		dictionaries:    make(map[uint64]*objDictionary),
		xrecords:        make(map[uint64]*objXrecord),
		internalObjects: make(map[uint64]*objGeneric),
		entityByHandle:  make(map[uint64]any),
		HeaderVars:      make(map[string]any),
		ltscale:         1, // 解码侧缺省比例
	}
	jsonApplyHeader(doc, root["HEADER"])
	for _, it := range objsRaw {
		m, ok := it.(map[string]any)
		if !ok {
			doc.skipped++
			continue
		}
		o := jsonObject(m)
		if name := o.str("entity"); name != "" {
			if name == "ATTDEF" {
				// 定义类标记：与 Parse 的 decodeObjects 同口径静默跳过
				// （不入 entityByHandle，不参与渲染与文本）
				continue
			}
			ent := buildJSONEntity(o, name)
			if ent == nil {
				doc.skipped++ // 未知实体类：跳过计数，不阻断其余对象
				continue
			}
			doc.classify(ent)
			continue
		}
		if name := o.str("object"); name != "" {
			h := o.handle("handle")
			if name == "LAYER" {
				doc.layerColors[h] = jsonLayerColor(o)
				continue
			}
			doc.internalObjects[h] = jsonGenericObject(o, name, h)
			continue
		}
		doc.skipped++ // 既无 entity 也无 object 键：坏条目
	}
	linkJSONAttribs(doc)
	return doc, nil
}

// jsonDetectVersion 从 gold JSON 顶层段推导 DWG 版本：优先 HEADER 的
// $ACADVER 约定键，回退 FILEHEADER.version（dwgread 实际输出位置）；
// 均缺失时按最新版本处理（实体几何键与版本无关）。
func jsonDetectVersion(root map[string]any) dwgVersion {
	if h, ok := root["HEADER"].(map[string]any); ok {
		if v := jsonVerString(h["$ACADVER"]); v != "" {
			if ver, ok2 := versionFromStr(v); ok2 {
				return ver
			}
		}
		if v := jsonVerString(h["ACADVER"]); v != "" {
			if ver, ok2 := versionFromStr(v); ok2 {
				return ver
			}
		}
	}
	if fh, ok := root["FILEHEADER"].(map[string]any); ok {
		if v := jsonVerString(fh["version"]); v != "" {
			if ver, ok2 := versionFromStr(v); ok2 {
				return ver
			}
		}
	}
	return verR2018
}

// jsonApplyHeader 消费 gold HEADER 段：全量键值存 HeaderVars（原样），
// 渲染相关键提升为结构化字段。$ACADVER 由 jsonDetectVersion 消费，
// 值仍保留在 HeaderVars。EXTMIN/EXTMAX/INSBASE 为 [x,y,z] 数组、LTSCALE
// 为标量；类型不符的键原样保留在 HeaderVars（不提升）。
func jsonApplyHeader(doc *Document, h any) {
	m, ok := h.(map[string]any)
	if !ok {
		return
	}
	for k, v := range m {
		doc.HeaderVars[k] = v
	}
	if arr, ok := m["EXTMIN"].([]any); ok && len(arr) >= 2 {
		doc.extMin = point3{jsonNumAt(arr, 0), jsonNumAt(arr, 1), jsonNumAt(arr, 2)}
	}
	if arr, ok := m["EXTMAX"].([]any); ok && len(arr) >= 2 {
		doc.extMax = point3{jsonNumAt(arr, 0), jsonNumAt(arr, 1), jsonNumAt(arr, 2)}
	}
	if arr, ok := m["INSBASE"].([]any); ok && len(arr) >= 2 {
		doc.insbase = point3{jsonNumAt(arr, 0), jsonNumAt(arr, 1), jsonNumAt(arr, 2)}
	}
	if f, ok := m["LTSCALE"].(float64); ok {
		doc.ltscale = f
	}
}

// HeaderVar 查询 HEADER 变量：键名带 $ 前缀容错（DXF 惯例 $EXTMIN 与
// gold 键 EXTMIN 等价查询）。未设置返回 (nil, false)。
func (d *Document) HeaderVar(key string) (any, bool) {
	if d == nil || d.HeaderVars == nil {
		return nil, false
	}
	if v, ok := d.HeaderVars[key]; ok {
		return v, true
	}
	if trimmed, ok := strings.CutPrefix(key, "$"); ok {
		v, ok2 := d.HeaderVars[trimmed]
		return v, ok2
	}
	return nil, false
}

// jsonVerString 提取版本串值（仅接受字符串形态）。
func jsonVerString(v any) string {
	s, _ := v.(string)
	return s
}

// versionFromStr 版本串 → dwgVersion（与 container.go detectVersion 同表）。
func versionFromStr(s string) (dwgVersion, bool) {
	switch s {
	case "AC1004":
		return verR9, true
	case "AC1006":
		return verR10, true
	case "AC1009":
		return verR11, true
	case "AC1012":
		return verR13, true
	case "AC1014":
		return verR14, true
	case "AC1015":
		return verR2000, true
	case "AC1018":
		return verR2004, true
	case "AC1021":
		return verR2007, true
	case "AC1024":
		return verR2010, true
	case "AC1027":
		return verR2013, true
	case "AC1032":
		return verR2018, true
	}
	return verR2018, false
}

// ---- gold JSON 条目的类型化取值辅助 ----

// jsonObject gold JSON 的单个 OBJECTS 条目（顶层键值原样保留）。
type jsonObject map[string]any

// raw 取原始值（键缺失返回 nil）。
func (o jsonObject) raw(key string) any { return o[key] }

// has 判断键存在（值为 null 也算存在）。
func (o jsonObject) has(key string) bool { _, ok := o[key]; return ok }

// str 取字符串值。
func (o jsonObject) str(key string) string {
	s, _ := o[key].(string)
	return s
}

// strAt 取字符串数组的指定下标元素（越界/类型不符返回空串）。
func (o jsonObject) strAt(key string, i int) string {
	arr, ok := o[key].([]any)
	if !ok || i >= len(arr) {
		return ""
	}
	s, _ := arr[i].(string)
	return s
}

// num 取数值（JSON 数字统一为 float64）。
func (o jsonObject) num(key string) (float64, bool) {
	f, ok := o[key].(float64)
	return f, ok
}

// i64 取整数值（缺省 0）。
func (o jsonObject) i64(key string) int64 {
	f, ok := o[key].(float64)
	if !ok {
		return 0
	}
	return int64(f)
}

// f64 取浮点值（缺省 0）。
func (o jsonObject) f64(key string) float64 {
	f, _ := o[key].(float64)
	return f
}

// boolean 取布尔值（gold 以 0/1 整数表达）。
func (o jsonObject) boolean(key string) bool { return o.i64(key) != 0 }

// handle 取句柄引用值：gold 句柄为 [code, size, value(, absValue)] 数组，
// 末位是绝对句柄（LibreDWG 句柄语义）；非数组/空数组返回 0。
func (o jsonObject) handle(key string) uint64 {
	return jsonHandleValue(o[key])
}

// jsonHandleValue 句柄数组 → 绝对句柄。
func jsonHandleValue(v any) uint64 {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return 0
	}
	f, ok := arr[len(arr)-1].(float64)
	if !ok {
		return 0
	}
	return uint64(f)
}

// p2 取 2D 点（[x, y]；长度不足按 0 补齐）。
func (o jsonObject) p2(key string) point2 {
	if arr, ok := o[key].([]any); ok {
		return point2{jsonNumAt(arr, 0), jsonNumAt(arr, 1)}
	}
	return point2{}
}

// p3 取 3D 点（[x, y(, z)]；gold 侧 2 元形态的 z 恒 0）。
func (o jsonObject) p3(key string) point3 {
	if arr, ok := o[key].([]any); ok {
		return point3{jsonNumAt(arr, 0), jsonNumAt(arr, 1), jsonNumAt(arr, 2)}
	}
	return point3{}
}

// jsonNumAt 数组指定下标的数值（越界/类型不符返回 0）。
func jsonNumAt(arr []any, i int) float64 {
	if i >= len(arr) {
		return 0
	}
	f, _ := arr[i].(float64)
	return f
}

// p3s 取 3D 点数组（[[x,y,z], ...]）。
func (o jsonObject) p3s(key string) []point3 {
	arr, ok := o[key].([]any)
	if !ok {
		return nil
	}
	var out []point3
	for _, e := range arr {
		if pa, ok := e.([]any); ok {
			out = append(out, point3{jsonNumAt(pa, 0), jsonNumAt(pa, 1), jsonNumAt(pa, 2)})
		}
	}
	return out
}

// p2s 取 2D 点数组：兼容 [[x,y],...] 嵌套与 [x,y,x,y,...] 展平两种形态
// （LWPOLYLINE gold 的 points 为嵌套，展平形态见 LibreDWG 部分版本输出）。
func (o jsonObject) p2s(key string) []point2 {
	arr, ok := o[key].([]any)
	if !ok {
		return nil
	}
	if len(arr) > 0 {
		if _, isNested := arr[0].([]any); isNested {
			var out []point2
			for _, e := range arr {
				if pa, ok := e.([]any); ok {
					out = append(out, point2{jsonNumAt(pa, 0), jsonNumAt(pa, 1)})
				}
			}
			return out
		}
	}
	var out []point2
	for i := 0; i+1 < len(arr); i += 2 {
		out = append(out, point2{jsonNumAt(arr, i), jsonNumAt(arr, i+1)})
	}
	return out
}

// f64s 取浮点数组。
func (o jsonObject) f64s(key string) []float64 {
	arr, ok := o[key].([]any)
	if !ok {
		return nil
	}
	out := make([]float64, 0, len(arr))
	for _, e := range arr {
		f, _ := e.(float64)
		out = append(out, f)
	}
	return out
}

// objs 取嵌套对象数组（paths[i]/verts[i] 等）。
func (o jsonObject) objs(key string) []jsonObject {
	arr, ok := o[key].([]any)
	if !ok {
		return nil
	}
	var out []jsonObject
	for _, e := range arr {
		if m, ok := e.(map[string]any); ok {
			out = append(out, jsonObject(m))
		}
	}
	return out
}

// ---- 公共头与颜色 ----

// jsonEntColor gold color 键 → entColor。双形态：pre-R2004 为标量
// （ACI 索引 / 0xC0000000|RGB 真彩色 / 256 BYLAYER / 0 ByBlock），
// R2004+ 为 CMC 对象 {index, rgb, flag}。标量逆映射保持 hasIndex 语义
// 与解码侧一致（colorAuditValue 对 hasIndex 原值导出：0→(true,0)，
// 256→(true,256)），仅真彩色走 hasTrue 分支。
func jsonEntColor(v any) entColor {
	switch c := v.(type) {
	case float64:
		n := int64(c)
		if n&0xFF000000 == 0xC0000000 {
			return entColor{hasTrue: true, trueColor: uint32(n) & 0xFFFFFF}
		}
		return entColor{hasIndex: true, index: uint16(n)}
	case map[string]any:
		var col entColor
		m := jsonObject(c)
		if f, ok := m.num("index"); ok {
			col.hasIndex, col.index = true, uint16(f)
		}
		if rgb := m.str("rgb"); rgb != "" {
			if v32, err := parseHexUint32(rgb); err == nil && (m.i64("flag")&0x80 != 0 || v32&0xFFFFFF != 0) {
				col.hasTrue, col.trueColor = true, v32&0xFFFFFF
			}
		}
		return col
	}
	return entColor{}
}

// parseHexUint32 解析 gold 的 %06x/%08x 十六进制串。
func parseHexUint32(s string) (uint32, error) {
	var v uint32
	for i := 0; i < len(s); i++ {
		c := s[i]
		var d uint32
		switch {
		case c >= '0' && c <= '9':
			d = uint32(c - '0')
		case c >= 'a' && c <= 'f':
			d = uint32(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = uint32(c-'A') + 10
		default:
			return 0, fmt.Errorf("cad: 非法 hex 字符 %q", c)
		}
		v = v<<4 | d
	}
	return v, nil
}

// jsonBase 从 gold 条目填充公共实体字段：baseEntity + commonEntityHead
// （head 供 entityField 审计导出 ltype_scale/invisible/linewt 等公共键；
// objSizeBit/recSize 取 gold bitsize/size 使 JSON 来源实体的审计导出与
// gold 一致）。内部类型名按 DIMENSION_→DIM_ 前缀还原。
func jsonBase(o jsonObject, goldName string) baseEntity {
	h := o.handle("handle")
	base := baseEntity{
		handle:        h,
		color:         jsonEntColor(o.raw("color")),
		mode:          uint8(o.i64("entmode")),
		owner:         o.handle("ownerhandle"),
		layer:         o.handle("layer"),
		objSizeBit:    uint64(o.i64("bitsize")),
		recSize:       uint32(o.i64("size")),
		typeName:      jsonInternalTypeName(goldName),
		typeCode:      uint16(o.i64("type")),
		nolinks:       o.boolean("nolinks"),
		isbylayerlt:   o.boolean("isbylayerlt"),
		previewExists: o.boolean("preview_exists"),
		head: &commonEntityHead{
			handle:        h,
			color:         jsonEntColor(o.raw("color")),
			entityMode:    uint8(o.i64("entmode")),
			ltypeScale:    o.f64("ltype_scale"),
			invisible:     int16(o.i64("invisible")),
			linewt:        uint16(o.i64("linewt")),
			ltypeFlags:    uint8(o.i64("ltype_flags")),
			plotstyleFlgs: uint8(o.i64("plotstyle_flags")),
			materialFlags: uint8(o.i64("material_flags")),
			shadowFlags:   uint8(o.i64("shadow_flags")),
			xdicMissing:   o.boolean("is_xdic_missing"),
			hasDsBinary:   o.boolean("has_ds_data"),
			visualStyle: [3]bool{
				o.boolean("has_full_visualstyle"),
				o.boolean("has_face_visualstyle"),
				o.boolean("has_edge_visualstyle"),
			},
		},
	}
	if base.head.ltypeScale == 0 {
		base.head.ltypeScale = 1 // 解码侧缺省比例（gold 缺键即 1）
	}
	// preview 缩略图（hex 串 → 原始字节，entityField 以 %X 导出对照）
	if base.previewExists {
		if pv := jsonHexBytes(o.str("preview")); pv != nil {
			base.head.preview = pv
		}
	}
	return base
}

// jsonInternalTypeName gold entity 名 → 内部类型名（DIMENSION_LINEAR →
// DIM_LINEAR，与 decodeEntityByTypeVer 的分发表的短名一致；entityField
// 的 entity 键会再映射回全名）。
func jsonInternalTypeName(gold string) string {
	const full = "DIMENSION_"
	if strings.HasPrefix(gold, full) {
		return "DIM_" + gold[len(full):]
	}
	return gold
}

// jsonLayerColor LAYER 对象的 gold color 键 → layerColor（标量索引或
// CMC 对象双形态）；name 为图层真名（DXF 写出消费）。
func jsonLayerColor(o jsonObject) layerColor {
	var lc layerColor
	lc.name = o.str("name")
	switch c := o.raw("color").(type) {
	case float64:
		lc.index = uint16(int64(c))
	case map[string]any:
		m := jsonObject(c)
		if f, ok := m.num("index"); ok {
			lc.index = uint16(f)
		}
		if rgb := m.str("rgb"); rgb != "" {
			if v32, err := parseHexUint32(rgb); err == nil && v32&0xFFFFFF != 0 {
				lc.hasTrue, lc.trueColor = true, v32&0xFFFFFF
			}
		}
	}
	return lc
}

// jsonGenericObject 非实体对象 → objGeneric：gold 展平键值对存入 Fields
// 中间表示（FieldPath 可直接按展平键查询，与解码对象的消费口径一致）。
func jsonGenericObject(o jsonObject, name string, h uint64) *objGeneric {
	g := &objGeneric{Name: name, Handle: h}
	for k, v := range o {
		switch v.(type) {
		case float64, string, bool, []any, map[string]any:
			g.Fields = append(g.Fields, objField{Key: k, Val: v})
		}
	}
	return g
}

// linkJSONAttribs 将 ATTRIB 关联到 owner INSERT 的 attribs 句柄链
// （DWG 侧该链存于 INSERT 的 handle 流，JSON 无流可读；ATTRIB 的
// gold ownerhandle 即宿主 INSERT 句柄， Texts 的 INSERT 属性展开依赖）。
func linkJSONAttribs(doc *Document) {
	for h, a := range doc.attribs {
		if ins, ok := doc.entityByHandle[a.owner].(*entInsert); ok {
			ins.attribs = append(ins.attribs, h)
		}
	}
}

// ---- 实体构造（entityField 的逆映射，手工 per-type setter）----

// jsonEntityBuilder 单个实体类型的 gold 键 → 实体构造器。
type jsonEntityBuilder func(o jsonObject) any

// jsonEntityBuilders gold entity 名 → 构造器。覆盖九样本全部实体类型
// （ATTDEF 按 Parse 口径在 ParseJSON 中先行跳过，不入表）；长尾复杂类
// （HATCH/3DSOLID 系/MULTILEADER 等）填充标量字段与几何主键，二进制
// 专有段（ACIS 数据、HATCH 图案定义线等）不还原，见各构造器注释。
var jsonEntityBuilders = map[string]jsonEntityBuilder{
	"LINE":                   jsonBuildLine,
	"CIRCLE":                 jsonBuildCircle,
	"ARC":                    jsonBuildArc,
	"POINT":                  jsonBuildPoint,
	"ELLIPSE":                jsonBuildEllipse,
	"LWPOLYLINE":             jsonBuildLwPolyline,
	"TEXT":                   jsonBuildText,
	"MTEXT":                  jsonBuildMText,
	"INSERT":                 jsonBuildInsert,
	"MINSERT":                jsonBuildInsert,
	"ATTRIB":                 jsonBuildAttrib,
	"SOLID":                  jsonBuildSolid,
	"TRACE":                  jsonBuildSolid,
	"3DFACE":                 jsonBuildFace3d,
	"VERTEX_2D":              jsonBuildVertex2d,
	"VERTEX_3D":              jsonBuildVertex3d,
	"VERTEX_MESH":            jsonBuildVertexPface,
	"VERTEX_PFACE":           jsonBuildVertexPface,
	"VERTEX_PFACE_FACE":      jsonBuildVertexPfaceFace,
	"POLYLINE_2D":            jsonBuildPolyline2d,
	"POLYLINE_3D":            jsonBuildPolyline3d,
	"POLYLINE_PFACE":         jsonBuildPolylinePface,
	"POLYLINE_MESH":          jsonBuildPolylinePface,
	"BLOCK":                  jsonBuildBlockLike,
	"ENDBLK":                 jsonBuildBlockLike,
	"SEQEND":                 jsonBuildBlockLike,
	"SPLINE":                 jsonBuildSpline,
	"RAY":                    jsonBuildRay,
	"XLINE":                  jsonBuildRay,
	"MLINE":                  jsonBuildMLine,
	"HATCH":                  jsonBuildHatch,
	"MPOLYGON":               jsonBuildHatch,
	"WIPEOUT":                jsonBuildWipeout,
	"IMAGE":                  jsonBuildWipeout,
	"TOLERANCE":              jsonBuildTolerance,
	"VIEWPORT":               jsonBuildViewport,
	"LEADER":                 jsonBuildLeader,
	"DIMENSION_ORDINATE":     jsonBuildDimension,
	"DIMENSION_LINEAR":       jsonBuildDimension,
	"DIMENSION_ALIGNED":      jsonBuildDimension,
	"DIMENSION_ANG3PT":       jsonBuildDimension,
	"DIMENSION_ANG2LN":       jsonBuildDimension,
	"DIMENSION_RADIUS":       jsonBuildDimension,
	"DIMENSION_DIAMETER":     jsonBuildDimension,
	"ARC_DIMENSION":          jsonBuildDimension,
	"LARGE_RADIAL_DIMENSION": jsonBuildDimension,
	"DIMENSION_ANGULAR":      jsonBuildDimension,
	"3DSOLID":                jsonBuildAcis,
	"REGION":                 jsonBuildAcis,
	"BODY":                   jsonBuildAcis,
	"OLE2FRAME":              jsonBuildOle2Frame,
	"OLEFRAME":               jsonBuildOleFrame,
	"LIGHT":                  jsonBuildLight,
	"MULTILEADER":            jsonBuildMLeader,
	"SHAPE":                  jsonBuildShape,
	"PROXY_ENTITY":           jsonBuildProxyEntity,
	"UNKNOWN_ENT":            jsonBuildUnknownEnt,
}

// buildJSONEntity 按 gold entity 名分发构造；未注册类型返回 nil。
func buildJSONEntity(o jsonObject, name string) any {
	if b, ok := jsonEntityBuilders[name]; ok {
		return b(o)
	}
	return nil
}

// jsonWithExtra 将 gold 条目中实际存在的扩展键填入 base.extra（键名与
// 取值类型对齐 DWG 解码器的 extra 填充口径——thickness/extrusion 等
// 标量由 entityField 经 extra 导出，json 与 dwg 两侧导出键值须一致）。
func jsonWithExtra(base baseEntity, o jsonObject, keys ...string) baseEntity {
	for _, k := range keys {
		if !o.has(k) {
			continue
		}
		if base.extra == nil {
			base.extra = map[string]any{}
		}
		switch k {
		case "extrusion": // BE 单位向量 → []float64（与 readBE 后的存储一致）
			e := o.p3("extrusion")
			base.extra[k] = []float64{e.x, e.y, e.z}
		case "z_is_zero", "scale_flag", "has_attribs", "dataflags", "flow_dir":
			base.extra[k] = o.i64(k)
		default: // thickness/elevation/oblique_angle/width_factor/extents_* 等 BD 标量
			base.extra[k] = o.f64(k)
		}
	}
	return base
}

func jsonBuildLine(o jsonObject) any {
	b := jsonWithExtra(jsonBase(o, "LINE"), o, "thickness", "extrusion", "z_is_zero")
	return &entLine{baseEntity: b, start: o.p3("start"), end: o.p3("end")}
}

func jsonBuildCircle(o jsonObject) any {
	b := jsonWithExtra(jsonBase(o, "CIRCLE"), o, "thickness", "extrusion")
	return &entCircle{baseEntity: b, center: o.p3("center"), radius: o.f64("radius")}
}

func jsonBuildArc(o jsonObject) any {
	// gold start_angle/end_angle 与解码侧同为弧度（AutoCAD 内部存储）
	b := jsonWithExtra(jsonBase(o, "ARC"), o, "thickness", "extrusion")
	return &entArc{
		baseEntity: b,
		center:     o.p3("center"),
		radius:     o.f64("radius"),
		angleStart: o.f64("start_angle"),
		angleEnd:   o.f64("end_angle"),
	}
}

func jsonBuildPoint(o jsonObject) any {
	// gold POINT 无 location 数组键，以 x/y/z 标量 + x_ang（x 轴角度）表达
	p := point3{o.f64("x"), o.f64("y"), o.f64("z")}
	if !o.has("x") {
		p = o.p3("location") // 兼容数组形态输出
	}
	b := jsonWithExtra(jsonBase(o, "POINT"), o, "thickness", "extrusion")
	return &entPoint{baseEntity: b, location: p, rotation: o.f64("x_ang")}
}

func jsonBuildEllipse(o jsonObject) any {
	return &entEllipse{
		baseEntity: jsonBase(o, "ELLIPSE"),
		center:     o.p3("center"),
		majorAxis:  o.p3("sm_axis"),
		ratio:      o.f64("axis_ratio"),
		startAng:   o.f64("start_angle"),
		endAng:     o.f64("end_angle"),
	}
}

func jsonBuildLwPolyline(o jsonObject) any {
	// gold flag 键即解码侧 flags；points 数组为顶点（bulges 缺失段按 0 对齐）
	e := &entLwPolyline{
		baseEntity: jsonBase(o, "LWPOLYLINE"),
		flags:      uint16(o.i64("flag")),
		vertices:   o.p2s("points"),
		bulges:     o.f64s("bulges"),
		elevation:  o.f64("elevation"),
		constWidth: o.f64("const_width"),
		thickness:  o.f64("thickness"),
	}
	// bulges 与 vertices 等长对齐（解码侧缺失补 0 的逆向口径）
	for len(e.bulges) < len(e.vertices) {
		e.bulges = append(e.bulges, 0)
	}
	return e
}

func jsonBuildText(o jsonObject) any {
	b := jsonWithExtra(jsonBase(o, "TEXT"), o, "thickness", "elevation", "oblique_angle", "width_factor")
	t := &entText{
		baseEntity:  b,
		text:        o.str("text_value"),
		insertion:   o.p3("ins_pt"),
		height:      o.f64("height"),
		rotation:    o.f64("rotation"),
		hAlign:      uint16(o.i64("horiz_alignment")),
		vAlign:      uint16(o.i64("vert_alignment")),
		gen:         uint16(o.i64("generation")),
		extrusion:   o.p3("extrusion"),
		styleHandle: o.handle("style"),
	}
	if o.has("alignment_pt") {
		p := o.p2("alignment_pt")
		t.alignPt = &p
	}
	return t
}

func jsonBuildMText(o jsonObject) any {
	// gold text 已按 bit_TV_to_utf8 展开 \U+XXXX，内部直接保存展开后文本
	b := jsonWithExtra(jsonBase(o, "MTEXT"), o, "flow_dir", "extents_height", "extents_width")
	m := &entMText{
		baseEntity:  b,
		text:        o.str("text"),
		insertion:   o.p3("ins_pt"),
		xAxisDir:    o.p3("x_axis_dir"),
		rectWidth:   o.f64("rect_width"),
		textHeight:  o.f64("text_height"),
		attachment:  uint16(o.i64("attachment")),
		lineFactor:  o.f64("linespace_factor"),
		extrusion:   o.p3("extrusion"),
		styleHandle: o.handle("style"),
	}
	return m
}

func jsonBuildInsert(o jsonObject) any {
	// attribs 由 linkJSONAttribs 后处理按 ATTRIB owner 归属回填；
	// scale_flag/has_attribs 入 extra（entityField 导出口径与解码侧一致）
	return &entInsert{
		baseEntity:  jsonWithExtra(jsonBase(o, "INSERT"), o, "scale_flag", "has_attribs"),
		position:    o.p3("ins_pt"),
		scale:       o.p3("scale"),
		rotation:    o.f64("rotation"),
		extrusion:   o.p3("extrusion"),
		blockHeader: o.handle("block_header"),
		seqend:      o.handle("seqend"),
	}
}

func jsonBuildAttrib(o jsonObject) any {
	b := jsonWithExtra(jsonBase(o, "ATTRIB"), o, "thickness", "elevation", "oblique_angle", "width_factor")
	a := &entAttrib{
		baseEntity:  b,
		text:        o.str("text_value"),
		tag:         o.str("tag"),
		insertion:   o.p3("ins_pt"),
		height:      o.f64("height"),
		rotation:    o.f64("rotation"),
		prompt:      o.str("prompt"),
		hAlign:      uint16(o.i64("horiz_alignment")),
		vAlign:      uint16(o.i64("vert_alignment")),
		gen:         uint16(o.i64("generation")),
		extrusion:   o.p3("extrusion"),
		styleHandle: o.handle("style"),
	}
	if o.has("alignment_pt") {
		p := o.p2("alignment_pt")
		a.alignPt = &p
	}
	return a
}

func jsonBuildSolid(o jsonObject) any {
	name := "SOLID"
	if o.str("entity") == "TRACE" {
		name = "TRACE"
	}
	return &entSolid{
		baseEntity: jsonBase(o, name),
		p1:         o.p2("corner1"),
		p2:         o.p2("corner2"),
		p3:         o.p2("corner3"),
		p4:         o.p2("corner4"),
		elevation:  o.f64("elevation"),
		thickness:  o.f64("thickness"),
		extrusion:  o.p3("extrusion"),
		trace:      name == "TRACE",
	}
}

func jsonBuildFace3d(o jsonObject) any {
	return &entFace3d{
		baseEntity:         jsonBase(o, "3DFACE"),
		p1:                 o.p3("corner1"),
		p2:                 o.p3("corner2"),
		p3:                 o.p3("corner3"),
		p4:                 o.p3("corner4"),
		invisibleEdgeFlags: uint16(o.i64("invis_flags")),
	}
}

func jsonBuildVertex2d(o jsonObject) any {
	return &entVertex2d{
		baseEntity: jsonBase(o, "VERTEX_2D"),
		flags:      uint16(o.i64("flag")),
		position:   o.p3("point"),
		bulge:      o.f64("bulge"),
		tangentDir: o.f64("tangent_dir"),
	}
}

func jsonBuildVertex3d(o jsonObject) any {
	return &entVertex3d{
		baseEntity: jsonBase(o, "VERTEX_3D"),
		flags:      uint8(o.i64("flag")),
		position:   o.p3("point"),
	}
}

func jsonBuildVertexPface(o jsonObject) any {
	return &entVertexPface{
		baseEntity: jsonBase(o, "VERTEX_PFACE"),
		flag:       uint8(o.i64("flag")),
		position:   o.p3("point"),
	}
}

func jsonBuildVertexPfaceFace(o jsonObject) any {
	f := &entVertexPfaceFace{
		baseEntity: jsonBase(o, "VERTEX_PFACE_FACE"),
		flag:       128, // 解码侧恒定值（LibreDWG 同口径），gold 亦输出 128
	}
	for i, v := range o.f64s("vertind") {
		if i >= 4 {
			break
		}
		f.vertind[i] = int32(v)
	}
	return f
}

func jsonBuildPolyline2d(o jsonObject) any {
	return &entPolyline2d{
		baseEntity: jsonBase(o, "POLYLINE_2D"),
		flags:      uint16(o.i64("flag")),
		curveType:  uint16(o.i64("curve_type")),
		widthStart: o.f64("start_width"),
		widthEnd:   o.f64("end_width"),
		thickness:  o.f64("thickness"),
		elevation:  o.f64("elevation"),
		extrusion:  o.p3("extrusion"),
	}
}

func jsonBuildPolyline3d(o jsonObject) any {
	return &entPolyline3d{
		baseEntity: jsonBase(o, "POLYLINE_3D"),
		flags70:    uint8(o.i64("flag")),
		flags75:    uint8(o.i64("curve_type")),
	}
}

func jsonBuildPolylinePface(o jsonObject) any {
	return &entPolylinePface{
		baseEntity:  jsonBase(o, "POLYLINE_PFACE"),
		numVertices: int(o.i64("numverts")),
		numFaces:    int(o.i64("numfaces")),
	}
}

func jsonBuildBlockLike(o jsonObject) any {
	name := o.str("entity")
	return &entBlockLike{baseEntity: jsonBase(o, name), name: o.str("name")}
}

func jsonBuildSpline(o jsonObject) any {
	// 拟合/控制点与节点向量按 gold 数组还原；beg/end_tan_vec 解码侧未建模
	// （entSpline 无对应字段），不消费
	return &entSpline{
		baseEntity:    jsonBase(o, "SPLINE"),
		scenario:      uint32(o.i64("scenario")),
		degree:        uint32(o.i64("degree")),
		fitTolerance:  o.f64("fit_tol"),
		knotTolerance: o.f64("knot_tol"),
		ctrlTolerance: o.f64("ctrl_tol"),
		knots:         o.f64s("knots"),
		controlPoints: o.p3s("control_points"),
		weights:       o.f64s("weights"),
		fitPoints:     o.p3s("fit_pts"),
	}
}

func jsonBuildRay(o jsonObject) any {
	name := o.str("entity")
	return &entRay{
		baseEntity: jsonBase(o, name),
		start:      o.p3("point"),
		unitVector: o.p3("vector"),
		xline:      name == "XLINE",
	}
}

func jsonBuildMLine(o jsonObject) any {
	// base_point/extrusion 为主体 3BD（渲染平铺与法向），verts 的
	// lines[j].segparms/areafillparms 为样式线段参数——按 gold 分组还原
	// 并记录每线计数（与 decodeMline 的计数口径一致，分组导出可对齐）
	m := &entMLine{
		baseEntity:    jsonBase(o, "MLINE"),
		scale:         o.f64("scale"),
		justification: uint8(o.i64("justification")),
		openClosed:    uint16(o.i64("flags")),
		basePoint:     o.p3("base_point"),
		extrusion:     o.p3("extrusion"),
	}
	for _, v := range o.objs("verts") {
		mv := entMLineVertex{
			position:  v.p3("vertex"),
			direction: v.p3("vertex_direction"),
			miter:     v.p3("miter_direction"),
		}
		for _, l := range v.objs("lines") {
			sp := l.f64s("segparms")
			ap := l.f64s("areafillparms")
			mv.segParams = append(mv.segParams, sp...)
			mv.areaParams = append(mv.areaParams, ap...)
			mv.segCounts = append(mv.segCounts, len(sp))
			mv.areaCounts = append(mv.areaCounts, len(ap))
		}
		m.vertices = append(m.vertices, mv)
	}
	return m
}

func jsonBuildHatch(o jsonObject) any {
	// 主体标量与边界路径还原（多段线路径细分出渲染点列；边集路径拼接
	// 直线段端点）；图案定义线（deflines）与样条段专有参数不还原
	h := &entHatch{
		baseEntity:          jsonBase(o, "HATCH"),
		isGradientFill:      uint32(o.i64("is_gradient_fill")),
		reserved:            uint32(o.i64("reserved")),
		gradientAngle:       o.f64("gradient_angle"),
		gradientShift:       o.f64("gradient_shift"),
		singleColorGradient: uint32(o.i64("single_color_gradient")),
		gradientTint:        o.f64("gradient_tint"),
		gradientName:        o.str("gradient_name"),
		elevation:           o.f64("elevation"),
		extrusion:           o.p3("extrusion"),
		name:                o.str("name"),
		solidFill:           o.boolean("is_solid_fill"),
		associative:         o.boolean("is_associative"),
		style:               uint16(o.i64("style")),
		patternType:         uint16(o.i64("pattern_type")),
		angle:               o.f64("angle"),
		scaleSpacing:        o.f64("scale_spacing"),
		doubleFlag:          o.boolean("double_flag"),
	}
	// 图案定义线（deflines）：angle + 原点 + 偏移 + 划线参数数组，
	// 与 decodeHatch 的 hatchDefLine 同构还原（渲染按线距平铺时消费）
	for _, d := range o.objs("deflines") {
		h.deflines = append(h.deflines, hatchDefLine{
			angle:  d.f64("angle"),
			pt0:    d.p2("pt0"),
			offset: d.p2("offset"),
			dashes: d.f64s("dashes"),
		})
	}
	for _, po := range o.objs("paths") {
		p := hatchPath{
			flag:           uint32(po.i64("flag")),
			isPolyline:     po.i64("flag")&2 != 0,
			bulgesPresent:  po.boolean("bulges_present"),
			closed:         po.boolean("closed"),
			numSegsOrPaths: uint32(po.i64("num_segs_or_paths")),
		}
		if p.isPolyline {
			for _, vo := range po.objs("polyline_paths") {
				p.polyVerts = append(p.polyVerts, hatchPolyVert{p: vo.p2("point"), bulge: vo.f64("bulge")})
			}
			var verts []point2
			var bulges []float64
			for _, pv := range p.polyVerts {
				verts = append(verts, pv.p)
				bulges = append(bulges, pv.bulge)
			}
			pts := verts
			if p.bulgesPresent {
				pts = polylineWithBulges(verts, bulges, p.closed, 64)
			}
			if p.closed {
				pts = closePath(pts)
			}
			p.points = pts
		} else {
			var pts []point2
			for _, so := range po.objs("segs") {
				s := hatchSeg{curveType: uint8(so.i64("curve_type"))}
				switch s.curveType {
				case 1: // 直线段：端点直接进入渲染点列
					s.first, s.second = so.p2("first_endpoint"), so.p2("second_endpoint")
					if len(pts) == 0 {
						pts = append(pts, s.first)
					}
					pts = append(pts, s.second)
				case 2, 3: // 圆弧/椭圆弧段：保留原始参数（不细分）
					s.center = so.p2("center")
					s.radius = so.f64("radius")
					s.ratio = so.f64("minor_major_ratio")
					s.startAng = so.f64("start_angle")
					s.endAng = so.f64("end_angle")
					s.ccw = so.boolean("is_ccw")
				}
				p.segs = append(p.segs, s)
			}
			p.points = pts
		}
		h.paths = append(h.paths, p)
	}
	return h
}

func jsonBuildWipeout(o jsonObject) any {
	// WIPEOUT/IMAGE 同布局：标量 + 图像变换主键；裁剪顶点与 imagedef 句柄
	// 不还原（渲染仅消费 pt0/uvec/vvec/image_size 的边界框）
	name := o.str("entity")
	w := &entWipeout{
		baseEntity:       jsonBase(o, name),
		classVersion:     uint32(o.i64("class_version")),
		pt0:              o.p3("pt0"),
		uvec:             o.p3("uvec"),
		vvec:             o.p3("vvec"),
		imageSize:        o.p2("image_size"),
		displayProps:     uint16(o.i64("display_props")),
		clipping:         o.boolean("clipping"),
		brightness:       uint8(o.i64("brightness")),
		contrast:         uint8(o.i64("contrast")),
		fade:             uint8(o.i64("fade")),
		clipMode:         uint8(o.i64("clip_mode")),
		clipBoundaryType: uint16(o.i64("clip_boundary_type")),
	}
	for _, cv := range o.p2s("clip_verts") {
		w.clipVerts = append(w.clipVerts, cv)
	}
	return w
}

func jsonBuildTolerance(o jsonObject) any {
	return &entTolerance{
		baseEntity:   jsonBase(o, "TOLERANCE"),
		text:         o.str("text_value"),
		unknownShort: uint16(o.i64("unknown_short")),
		insertion:    o.p3("ins_pt"),
		xDirection:   o.p3("x_direction"),
		extrusion:    o.p3("extrusion"),
		height:       o.f64("height"),
		dimgap:       o.f64("dimgap"),
		dimstyle:     o.handle("dimstyle"),
	}
}

func jsonBuildViewport(o jsonObject) any {
	return &entViewport{
		baseEntity:          jsonBase(o, "VIEWPORT"),
		center:              o.p3("center"),
		width:               o.f64("width"),
		height:              o.f64("height"),
		viewTarget:          o.p3("view_target"),
		viewDir:             o.p3("VIEWDIR"),
		viewTwist:           o.f64("VIEWTWIST"),
		viewSize:            o.f64("VIEWSIZE"),
		lensLength:          o.f64("LENSLENGTH"),
		frontZ:              o.f64("FRONTZ"),
		backZ:               o.f64("BACKZ"),
		snapAng:             o.f64("SNAPANG"),
		viewCtr:             o.p2("VIEWCTR"),
		snapBase:            o.p2("SNAPBASE"),
		snapUnit:            o.p2("SNAPUNIT"),
		gridUnit:            o.p2("GRIDUNIT"),
		circleZoom:          uint16(o.i64("circle_zoom")),
		gridMajor:           uint16(o.i64("grid_major")),
		numFrozenLayers:     uint32(o.i64("num_frozen_layers")),
		statusFlag:          uint32(o.i64("status_flag")),
		styleSheet:          o.str("style_sheet"),
		renderMode:          uint8(o.i64("render_mode")),
		ucsVP:               o.boolean("UCSVP"),
		ucsAtOrigin:         o.boolean("ucs_at_origin"),
		ucsorg:              o.p3("UCSORG"),
		ucsxdir:             o.p3("UCSXDIR"),
		ucsydir:             o.p3("UCSYDIR"),
		ucsElevation:        o.f64("ucs_elevation"),
		ucsOrthoView:        uint16(o.i64("UCSORTHOVIEW")),
		shadeplotMode:       uint16(o.i64("shadeplot_mode")),
		useDefaultLights:    o.boolean("use_default_lights"),
		defaultLightingType: uint8(o.i64("default_lighting_type")),
		brightness:          o.f64("brightness"),
		contrast:            o.f64("contrast"),
	}
}

func jsonBuildLeader(o jsonObject) any {
	// LEADER 标量基键；annotated/points 等数组键由解码侧同样不入审计导出，
	// 渲染消费的顶点数组在 entLeader 中按需还原
	l := &entLeader{
		baseEntity:     jsonBase(o, "LEADER"),
		annotationType: uint16(o.i64("annotation_type")),
		pathType:       uint16(o.i64("path_type")),
	}
	for _, pt := range o.p3s("points") {
		l.points = append(l.points, pt)
	}
	return l
}

func jsonBuildDimension(o jsonObject) any {
	gold := o.str("entity")
	d := &entDimension{baseEntity: jsonBase(o, gold)}
	d.extrusion = o.p3("extrusion")
	d.textMidpoint = o.p3("text_midpt")
	d.elevation = o.f64("elevation")
	d.dimFlag = uint8(o.i64("flag"))
	d.dimFlags = uint8(o.i64("flag1"))
	d.flag2 = uint8(o.i64("flag2"))
	d.userText = o.str("user_text")
	d.textRotation = o.f64("text_rotation")
	d.horizontalDir = o.f64("horiz_dir")
	d.insertScale = o.p3("ins_scale")
	d.insertRotation = o.f64("ins_rotation")
	d.attachmentPoint = uint16(o.i64("attachment"))
	d.lineSpacingStyle = uint16(o.i64("lspace_style"))
	d.lineSpacingFactor = o.f64("lspace_factor")
	d.actualMeasurement = o.f64("act_measurement")
	d.classVersion = uint8(o.i64("class_version"))
	// 公共点组：ORDINATE 为 def_pt(10)/feature_location_pt(13)/
	// leader_endpt(14)；ANG2LN 为 def_pt(2RD)→p16 + xline1start_pt(13)/
	// xline1end_pt(14)/xline2start_pt(15)/xline2end_pt(16 系)；其余类型
	// def_pt(10) + xline1_pt(13) + xline2_pt(14)（与 readDimSpecific
	// 各布局载体一一对应）
	d.point10 = o.p3("def_pt")
	d.point13 = o.p3("xline1_pt")
	d.point14 = o.p3("xline2_pt")
	if o.has("feature_location_pt") {
		d.point13 = o.p3("feature_location_pt")
	}
	if o.has("leader_endpt") {
		d.point14 = o.p3("leader_endpt")
	}
	if o.has("xline1start_pt") {
		// ANG2LN：def_pt 为 2RD 形态存 p16x/p16y，四 xline 点按 13/14/
		// 15/16 组码序入载体 point13/14/15/10
		p2 := o.p2("def_pt")
		d.point16x, d.p16y, d.hasPoint16 = p2.x, p2.y, true
		d.point13 = o.p3("xline1start_pt")
		d.point14 = o.p3("xline1end_pt")
		d.point15 = o.p3("xline2start_pt")
		d.hasPoint15 = true
		d.point10 = o.p3("xline2end_pt")
	}
	d.insertPoint = point3{d.point10.x, d.point10.y, d.elevation}
	d.hasInsertPoint = true
	d.extLineRotation = o.f64("oblique_angle")
	d.dimRotation = o.f64("dim_rotation")
	// 弧长专属（ARC_DIMENSION）
	d.defPt = o.p3("def_pt")
	d.isPartial = o.boolean("is_partial")
	d.arcStartParam = o.f64("arc_start_param")
	d.arcEndParam = o.f64("arc_end_param")
	d.hasLeader = o.boolean("has_leader")
	d.leader1Pt = o.p3("leader1_pt")
	d.leader2Pt = o.p3("leader2_pt")
	d.dimstyleHandle = o.handle("dimstyle")
	d.anonymousBlock = o.handle("anonymous_block")
	return d
}

func jsonBuildAcis(o jsonObject) any {
	// 标量基键还原；acis_data（ACIS 文本模型）按 version 双形态还原到
	// acisData（与 decodeAcisVer 同一存储口径，JSON 来源实体消费侧完整）
	gold := o.str("entity")
	a := &entAcis{
		baseEntity:           jsonBase(o, gold),
		acisEmpty:            o.boolean("acis_empty"),
		acisEmptyBit:         o.boolean("acis_empty_bit"),
		unknown:              uint8(o.i64("unknown")),
		version:              uint16(o.i64("version")),
		wireframeDataPresent: o.boolean("wireframe_data_present"),
		pointPresent:         o.boolean("point_present"),
		isolines:             uint32(o.i64("isolines")),
		isolinePresent:       o.boolean("isoline_present"),
		hasRevisionGuid:      o.boolean("has_revision_guid"),
		revisionMajor:        uint32(o.i64("revision_major")),
		revisionMinor1:       uint16(o.i64("revision_minor1")),
		revisionMinor2:       uint16(o.i64("revision_minor2")),
		endMarker:            uint32(o.i64("end_marker")),
	}
	// acis_data 数组还原（out_json json_3dsolid 的 ARRAY 输出）：
	// SAT（version<2）为按 \n 分行的文本数组（行分隔符不保留，重组以 \n
	// 连接，与 acis_empty=0 时的解混淆文本一致）；SAB（version=2）恒为
	// ["ACIS BinaryFile", <hex>] 双元素——首元素即 acisData 前 15 字节，
	// 次元素为剩余字节的 hex 串，sabSize 取总长（decodeAcisVer 的 end 语义）。
	// acis_empty=1 的实体无该键（gold 同口径不输出）。
	if !a.acisEmpty {
		if lines, ok := o.raw("acis_data").([]any); ok && len(lines) > 0 {
			if a.version >= 2 {
				data := []byte(o.strAt("acis_data", 0))
				data = append(data, jsonHexBytes(o.strAt("acis_data", 1))...)
				a.acisData = data
				a.sabSize = len(data)
			} else {
				parts := make([]string, 0, len(lines))
				for _, ln := range lines {
					if s, ok := ln.(string); ok {
						parts = append(parts, s)
					}
				}
				a.acisData = []byte(strings.Join(parts, "\n"))
			}
		}
		// encr_sat_data（DXF 往返产生的加密块 hex 串数组）→ blocks 原文
		if raw, ok := o.raw("encr_sat_data").([]any); ok {
			for _, e := range raw {
				if s, ok := e.(string); ok {
					if b := jsonHexBytes(s); b != nil {
						a.blocks = append(a.blocks, b)
					}
				}
			}
		}
	}
	return a
}

func jsonBuildOle2Frame(o jsonObject) any {
	gold := o.str("entity")
	data := jsonHexBytes(o.str("data"))
	return &entOle2Frame{
		baseEntity: jsonBase(o, gold),
		oleType:    uint16(o.i64("type")),
		mode:       uint16(o.i64("mode")),
		lockAspect: uint8(o.i64("lock_aspect")),
		dataSize:   uint32(o.i64("data_size")),
		data:       data,
	}
}

func jsonBuildOleFrame(o jsonObject) any {
	gold := o.str("entity")
	data := jsonHexBytes(o.str("data"))
	return &entOleFrame{
		baseEntity: jsonBase(o, gold),
		flag:       uint16(o.i64("flag")),
		mode:       uint16(o.i64("mode")),
		dataSize:   uint32(o.i64("data_size")),
		data:       data,
	}
}

func jsonBuildLight(o jsonObject) any {
	l := &entLight{
		baseEntity:           jsonBase(o, "LIGHT"),
		classVersion:         uint32(o.i64("class_version")),
		name:                 o.str("name"),
		lightType:            uint32(o.i64("type")),
		status:               o.boolean("status"),
		plotGlyph:            o.boolean("plot_glyph"),
		intensity:            o.f64("intensity"),
		position:             o.p3("position"),
		target:               o.p3("target"),
		attenuationType:      uint32(o.i64("attenuation_type")),
		useAttenuationLimits: o.boolean("use_attenuation_limits"),
		attenuationStart:     o.f64("attenuation_start_limit"),
		attenuationEnd:       o.f64("attenuation_end_limit"),
		hotspotAngle:         o.f64("hotspot_angle"),
		falloffAngle:         o.f64("falloff_angle"),
		castShadows:          o.boolean("cast_shadows"),
		shadowType:           uint32(o.i64("shadow_type")),
		shadowMapSize:        uint16(o.i64("shadow_map_size")),
		shadowMapSoftness:    int8(o.i64("shadow_map_softness")),
	}
	// light_color 双形态：标量（pre-R2004 索引）或 CMC 对象（R2004+）
	switch c := o.raw("light_color").(type) {
	case float64:
		l.lightColorIndex = uint16(int64(c))
	case map[string]any:
		m := jsonObject(c)
		l.hasLightColorTrue = true
		l.lightColorIndex = uint16(m.i64("index"))
		if v32, err := parseHexUint32(m.str("rgb")); err == nil {
			l.lightColorRGB = v32
		}
		l.lightColorFlag = uint8(m.i64("flag"))
	}
	return l
}

// jsonMLeaderCMC gold CMC 双形态 → mleaderCMC：对象形态（R2004+）与
// readMLeaderCMC 同口径还原——rgb method 越界时修正为 0xc2 前缀，index
// 按调色板反查覆盖（与 DWG 侧导出一致）；标量为 pre-R2004 索引。
func jsonMLeaderCMC(v any) mleaderCMC {
	m, ok := v.(map[string]any)
	if !ok {
		if f, ok := v.(float64); ok {
			return mleaderCMC{index: uint16(f)}
		}
		return mleaderCMC{}
	}
	o := jsonObject(m)
	var c mleaderCMC
	c.isTrue = true
	if v32, err := parseHexUint32(o.str("rgb")); err == nil {
		c.rgb = v32
		if method := c.rgb >> 24; method < 0xc0 || method > 0xc8 {
			c.rgb = 0xc2000000 | (c.rgb & 0xffffff)
		}
		c.index = uint16(dwgFindColorIndex(c.rgb))
	}
	if f, ok := o.num("index"); ok && (o.str("rgb") == "" || c.index == 0) {
		c.index = uint16(f)
	}
	c.flag = uint8(o.i64("flag"))
	return c
}

func jsonBuildMLeader(o jsonObject) any {
	// 顶层标量 + ctx 全结构还原（jsonMLeaderCtx：leaders/lines 三层嵌套、
	// txt/blk 内容两分支、base 三点组；键名均为 gold 展平键）+ 顶层 CMC
	// 双形态与句柄系，与 decodeMLeader 的模型字段一一对应
	m := &entMLeader{baseEntity: jsonBase(o, "MULTILEADER")}
	if o.has("class_version") {
		m.hasVersion = true
		m.classVersion = uint16(o.i64("class_version"))
	}
	m.mleaderType = uint16(o.i64("type"))
	m.flags = uint32(o.i64("flags"))
	m.lineColor = jsonMLeaderCMC(o.raw("line_color"))
	m.lineLinewt = int32(o.i64("line_linewt"))
	m.lineLtype = o.handle("line_ltype")
	m.hasLanding = o.boolean("has_landing")
	m.hasDogleg = o.boolean("has_dogleg")
	m.landingDist = o.f64("landing_dist")
	m.arrowHandle = o.handle("arrow_handle")
	m.arrowSize = o.f64("arrow_size")
	m.styleContent = uint16(o.i64("style_content"))
	m.textStyle = o.handle("text_style")
	m.textLeft = uint16(o.i64("text_left"))
	m.textRight = uint16(o.i64("text_right"))
	m.textAngletype = uint16(o.i64("text_angletype"))
	m.textAlignment = uint16(o.i64("text_alignment"))
	m.textColor = jsonMLeaderCMC(o.raw("text_color"))
	m.hasTextFrame = o.boolean("has_text_frame")
	m.blockStyle = o.handle("block_style")
	m.blockColor = jsonMLeaderCMC(o.raw("block_color"))
	m.blockScale = o.p3("block_scale")
	m.blockRotation = o.f64("block_rotation")
	m.styleAttachment = uint16(o.i64("style_attachment"))
	m.isAnnotative = o.boolean("is_annotative")
	m.isNegTextdir = o.boolean("is_neg_textdir")
	m.ipeAlignment = uint16(o.i64("ipe_alignment"))
	m.justification = uint16(o.i64("justification"))
	m.scaleFactor = o.f64("scale_factor")
	// VERSIONS(R_14, R_2007) 的箭头/块标签数组与 SINCE R_2010b/R_2013b
	// 尾段：gold 按版本段条件输出，键缺失时零值（与解码侧分支一致）
	for _, ao := range o.objs("arrowheads") {
		m.arrowheads = append(m.arrowheads, mleaderArrowhead{
			isDefault: ao.boolean("is_default"),
			arrowhead: ao.handle("arrowhead"),
		})
	}
	for _, bo := range o.objs("blocklabels") {
		m.blocklabels = append(m.blocklabels, mleaderBlockLabel{
			attdef:    bo.handle("attdef"),
			labelText: bo.str("label_text"),
			uiIndex:   uint16(bo.i64("ui_index")),
			width:     bo.f64("width"),
		})
	}
	if m.hasVersion {
		m.attachDir = uint16(o.i64("attach_dir"))
		m.attachTop = uint16(o.i64("attach_top"))
		m.attachBottom = uint16(o.i64("attach_bottom"))
		m.isTextExtended = o.boolean("is_text_extended")
	}
	m.mleaderStyle = o.handle("mleaderstyle")
	jsonMLeaderCtx(o, m)
	return m
}

// jsonMLeaderCtx gold 展平 ctx 键 → mleaderContextData：标量组、
// leaders→lines→breaks/points 三层嵌套、content txt/blk 内容两分支与
// base 三点组。gold 的 has_content_txt 缺省（pre-R2004 无该键形态）按
// content.txt 键存在性判定（LibreDWG HAS_CONTENT 分支输出键集互斥）。
func jsonMLeaderCtx(o jsonObject, m *entMLeader) {
	c := &m.ctx
	c.numLeaders = uint32(o.i64("ctx.num_leaders"))
	for _, lo := range o.objs("ctx.leaders") {
		var n mleaderNode
		n.hasLastLeaderLinePoint = lo.boolean("has_lastleaderlinepoint")
		n.lastLeaderLinePoint = lo.p3("lastleaderlinepoint")
		n.hasDogleg = lo.boolean("has_dogleg")
		n.doglegVector = lo.p3("dogleg_vector")
		n.numBreaks = uint32(lo.i64("num_breaks"))
		for _, bo := range lo.objs("breaks") {
			n.breaks = append(n.breaks, mleaderBreak{start: bo.p3("start"), end: bo.p3("end")})
		}
		n.branchIndex = uint32(lo.i64("branch_index"))
		n.doglegLength = lo.f64("dogleg_length")
		n.numLines = uint32(lo.i64("num_lines"))
		for _, lno := range lo.objs("lines") {
			var ln mleaderLine
			ln.points = lno.p3s("points")
			ln.numBreaks = uint32(lno.i64("num_breaks"))
			for _, bo := range lno.objs("breaks") {
				ln.breaks = append(ln.breaks, mleaderBreak{start: bo.p3("start"), end: bo.p3("end")})
			}
			ln.lineIndex = uint32(lno.i64("line_index"))
			if m.hasVersion { // SINCE R_2010b 的引线线段专有键
				ln.mleaderType = uint16(lno.i64("type"))
				ln.color = jsonMLeaderCMC(lno.raw("color"))
				ln.linewt = int32(lno.i64("linewt"))
				ln.arrowSize = lno.f64("arrow_size")
				ln.arrowHandle = lno.handle("arrow_handle")
				ln.flags = uint32(lno.i64("flags"))
			}
			n.lines = append(n.lines, ln)
		}
		if m.hasVersion {
			n.attachDir = uint16(lo.i64("attach_dir"))
		}
		c.leaders = append(c.leaders, n)
	}
	c.scaleFactor = o.f64("ctx.scale_factor")
	c.contentBase = o.p3("ctx.content_base")
	c.textHeight = o.f64("ctx.text_height")
	c.arrowSize = o.f64("ctx.arrow_size")
	c.landingGap = o.f64("ctx.landing_gap")
	c.textLeft = uint16(o.i64("ctx.text_left"))
	c.textRight = uint16(o.i64("ctx.text_right"))
	c.textAngletype = uint16(o.i64("ctx.text_angletype"))
	c.textAlignment = uint16(o.i64("ctx.text_alignment"))
	_, txtPresent := o["ctx.content.txt.default_text"]
	c.hasContentTxt = o.boolean("ctx.has_content_txt") || txtPresent
	if c.hasContentTxt {
		t := &c.txt
		t.defaultText = o.str("ctx.content.txt.default_text")
		t.normal = o.p3("ctx.content.txt.normal")
		t.styleHandle = o.handle("ctx.content.txt.style")
		t.location = o.p3("ctx.content.txt.location")
		t.direction = o.p3("ctx.content.txt.direction")
		t.rotation = o.f64("ctx.content.txt.rotation")
		t.width = o.f64("ctx.content.txt.width")
		t.height = o.f64("ctx.content.txt.height")
		t.lineSpacingFactor = o.f64("ctx.content.txt.line_spacing_factor")
		t.lineSpacingStyle = uint16(o.i64("ctx.content.txt.line_spacing_style"))
		t.color = jsonMLeaderCMC(o.raw("ctx.content.txt.color"))
		t.alignment = uint16(o.i64("ctx.content.txt.alignment"))
		t.flow = uint16(o.i64("ctx.content.txt.flow"))
		t.bgColor = jsonMLeaderCMC(o.raw("ctx.content.txt.bg_color"))
		t.bgScale = o.f64("ctx.content.txt.bg_scale")
		t.bgTransparency = uint32(o.i64("ctx.content.txt.bg_transparency"))
		t.isBgFill = o.boolean("ctx.content.txt.is_bg_fill")
		t.isBgMaskFill = o.boolean("ctx.content.txt.is_bg_mask_fill")
		t.colType = uint16(o.i64("ctx.content.txt.col_type"))
		t.isHeightAuto = o.boolean("ctx.content.txt.is_height_auto")
		t.colWidth = o.f64("ctx.content.txt.col_width")
		t.colGutter = o.f64("ctx.content.txt.col_gutter")
		t.isColFlowReversed = o.boolean("ctx.content.txt.is_col_flow_reversed")
		t.numColSizes = uint32(o.i64("ctx.content.txt.num_col_sizes"))
		t.colSizes = o.f64s("ctx.content.txt.col_sizes")
		t.wordBreak = o.boolean("ctx.content.txt.word_break")
		t.unknown = o.boolean("ctx.content.txt.unknown")
	} else {
		c.hasContentBlk = o.boolean("ctx.has_content_blk")
		if c.hasContentBlk {
			k := &c.blk
			k.blockTable = o.handle("ctx.content.blk.block_table")
			k.normal = o.p3("ctx.content.blk.normal")
			k.location = o.p3("ctx.content.blk.location")
			k.scale = o.p3("ctx.content.blk.scale")
			k.rotation = o.f64("ctx.content.blk.rotation")
			k.color = jsonMLeaderCMC(o.raw("ctx.content.blk.color"))
			if tr := o.f64s("ctx.content.blk.transform"); len(tr) == 16 {
				copy(k.transform[:], tr)
			}
		}
	}
	c.base = o.p3("ctx.base")
	c.baseDir = o.p3("ctx.base_dir")
	c.baseVert = o.p3("ctx.base_vert")
	c.isNormalReversed = o.boolean("ctx.is_normal_reversed")
	if m.hasVersion { // SINCE R_2010b
		c.textTop = uint16(o.i64("ctx.text_top"))
		c.textBottom = uint16(o.i64("ctx.text_bottom"))
	}
}

func jsonBuildShape(o jsonObject) any {
	return &entShape{
		baseEntity:  jsonBase(o, "SHAPE"),
		insertion:   o.p3("ins_pt"),
		scale:       o.f64("scale"),
		rotation:    o.f64("rotation"),
		widthFactor: o.f64("width_factor"),
		oblique:     o.f64("oblique_angle"),
		thickness:   o.f64("thickness"),
	}
}

func jsonBuildProxyEntity(o jsonObject) any {
	gold := o.str("entity")
	p := &entProxyEntity{
		baseEntity:    jsonBase(o, gold),
		proxyID:       uint32(o.i64("proxy_id")),
		version:       uint32(o.i64("version")),
		maintVersion:  uint32(o.i64("maint_version")),
		dwgVersionNum: uint32(o.i64("dwg_version")),
		fromDxf:       o.boolean("from_dxf"),
		dataNumBits:   uint32(o.i64("data_numbits")),
		numObjids:     uint32(o.i64("num_objids")),
		proxyDataSize: uint32(o.i64("proxy_data_size")),
	}
	p.proxyData = jsonHexBytes(o.str("proxy_data"))
	return p
}

func jsonBuildUnknownEnt(o jsonObject) any {
	name := o.str("entity")
	e := &entUnknownEnt{baseEntity: jsonBase(o, name)}
	if dx := o.str("_subclass"); dx != "" {
		e.extra = map[string]any{"dxfname": dx}
	}
	return e
}

// jsonHexBytes gold 的十六进制串（data/preview 等）还原为字节；空串返回 nil。
func jsonHexBytes(s string) []byte {
	if s == "" || len(s)%2 != 0 {
		return nil
	}
	out := make([]byte, 0, len(s)/2)
	for i := 0; i+1 < len(s); i += 2 {
		v, err := parseHexUint32(s[i : i+2])
		if err != nil {
			return nil
		}
		out = append(out, byte(v))
	}
	return out
}
