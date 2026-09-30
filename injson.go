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
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/entity"
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
		attribs:         make(map[uint64]*entity.EntAttrib),
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
func jsonDetectVersion(root map[string]any) container.DwgVersion {
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
	return container.VerR2018
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
		doc.extMin = entity.Point3{jsonNumAt(arr, 0), jsonNumAt(arr, 1), jsonNumAt(arr, 2)}
	}
	if arr, ok := m["EXTMAX"].([]any); ok && len(arr) >= 2 {
		doc.extMax = entity.Point3{jsonNumAt(arr, 0), jsonNumAt(arr, 1), jsonNumAt(arr, 2)}
	}
	if arr, ok := m["INSBASE"].([]any); ok && len(arr) >= 2 {
		doc.insbase = entity.Point3{jsonNumAt(arr, 0), jsonNumAt(arr, 1), jsonNumAt(arr, 2)}
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
func versionFromStr(s string) (container.DwgVersion, bool) {
	switch s {
	case "AC1004":
		return container.VerR9, true
	case "AC1006":
		return container.VerR10, true
	case "AC1009":
		return container.VerR11, true
	case "AC1012":
		return container.VerR13, true
	case "AC1014":
		return container.VerR14, true
	case "AC1015":
		return container.VerR2000, true
	case "AC1018":
		return container.VerR2004, true
	case "AC1021":
		return container.VerR2007, true
	case "AC1024":
		return container.VerR2010, true
	case "AC1027":
		return container.VerR2013, true
	case "AC1032":
		return container.VerR2018, true
	}
	return container.VerR2018, false
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
func (o jsonObject) p2(key string) entity.Point2 {
	if arr, ok := o[key].([]any); ok {
		return entity.Point2{jsonNumAt(arr, 0), jsonNumAt(arr, 1)}
	}
	return entity.Point2{}
}

// p3 取 3D 点（[x, y(, z)]；gold 侧 2 元形态的 z 恒 0）。
func (o jsonObject) p3(key string) entity.Point3 {
	if arr, ok := o[key].([]any); ok {
		return entity.Point3{jsonNumAt(arr, 0), jsonNumAt(arr, 1), jsonNumAt(arr, 2)}
	}
	return entity.Point3{}
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
func (o jsonObject) p3s(key string) []entity.Point3 {
	arr, ok := o[key].([]any)
	if !ok {
		return nil
	}
	var out []entity.Point3
	for _, e := range arr {
		if pa, ok := e.([]any); ok {
			out = append(out, entity.Point3{jsonNumAt(pa, 0), jsonNumAt(pa, 1), jsonNumAt(pa, 2)})
		}
	}
	return out
}

// p2s 取 2D 点数组：兼容 [[x,y],...] 嵌套与 [x,y,x,y,...] 展平两种形态
// （LWPOLYLINE gold 的 points 为嵌套，展平形态见 LibreDWG 部分版本输出）。
func (o jsonObject) p2s(key string) []entity.Point2 {
	arr, ok := o[key].([]any)
	if !ok {
		return nil
	}
	if len(arr) > 0 {
		if _, isNested := arr[0].([]any); isNested {
			var out []entity.Point2
			for _, e := range arr {
				if pa, ok := e.([]any); ok {
					out = append(out, entity.Point2{jsonNumAt(pa, 0), jsonNumAt(pa, 1)})
				}
			}
			return out
		}
	}
	var out []entity.Point2
	for i := 0; i+1 < len(arr); i += 2 {
		out = append(out, entity.Point2{jsonNumAt(arr, i), jsonNumAt(arr, i+1)})
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
func jsonEntColor(v any) entity.EntColor {
	switch c := v.(type) {
	case float64:
		n := int64(c)
		if n&0xFF000000 == 0xC0000000 {
			return entity.EntColor{HasTrue: true, TrueColor: uint32(n) & 0xFFFFFF}
		}
		return entity.EntColor{HasIndex: true, Index: uint16(n)}
	case map[string]any:
		var col entity.EntColor
		m := jsonObject(c)
		if f, ok := m.num("index"); ok {
			col.HasIndex, col.Index = true, uint16(f)
		}
		if rgb := m.str("rgb"); rgb != "" {
			if v32, err := parseHexUint32(rgb); err == nil && (m.i64("flag")&0x80 != 0 || v32&0xFFFFFF != 0) {
				col.HasTrue, col.TrueColor = true, v32&0xFFFFFF
			}
		}
		return col
	}
	return entity.EntColor{}
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
func jsonBase(o jsonObject, goldName string) entity.BaseEntity {
	h := o.handle("handle")
	base := entity.BaseEntity{
		Handle:        h,
		Color:         jsonEntColor(o.raw("color")),
		Mode:          uint8(o.i64("entmode")),
		Owner:         o.handle("ownerhandle"),
		Layer:         o.handle("layer"),
		ObjSizeBit:    uint64(o.i64("bitsize")),
		RecSize:       uint32(o.i64("size")),
		TypeName:      jsonInternalTypeName(goldName),
		TypeCode:      uint16(o.i64("type")),
		Nolinks:       o.boolean("nolinks"),
		Isbylayerlt:   o.boolean("isbylayerlt"),
		PreviewExists: o.boolean("preview_exists"),
		Head: &entity.CommonEntityHead{
			Handle:        h,
			Color:         jsonEntColor(o.raw("color")),
			EntityMode:    uint8(o.i64("entmode")),
			LtypeScale:    o.f64("ltype_scale"),
			Invisible:     int16(o.i64("invisible")),
			Linewt:        uint16(o.i64("linewt")),
			LtypeFlags:    uint8(o.i64("ltype_flags")),
			PlotstyleFlgs: uint8(o.i64("plotstyle_flags")),
			MaterialFlags: uint8(o.i64("material_flags")),
			ShadowFlags:   uint8(o.i64("shadow_flags")),
			XdicMissing:   o.boolean("is_xdic_missing"),
			HasDsBinary:   o.boolean("has_ds_data"),
			VisualStyle: [3]bool{
				o.boolean("has_full_visualstyle"),
				o.boolean("has_face_visualstyle"),
				o.boolean("has_edge_visualstyle"),
			},
		},
	}
	if base.Head.LtypeScale == 0 {
		base.Head.LtypeScale = 1 // 解码侧缺省比例（gold 缺键即 1）
	}
	// preview 缩略图（hex 串 → 原始字节，entityField 以 %X 导出对照）
	if base.PreviewExists {
		if pv := jsonHexBytes(o.str("preview")); pv != nil {
			base.Head.Preview = pv
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
		if ins, ok := doc.entityByHandle[a.Owner].(*entity.EntInsert); ok {
			ins.Attribs = append(ins.Attribs, h)
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
func jsonWithExtra(base entity.BaseEntity, o jsonObject, keys ...string) entity.BaseEntity {
	for _, k := range keys {
		if !o.has(k) {
			continue
		}
		if base.Extra == nil {
			base.Extra = map[string]any{}
		}
		switch k {
		case "extrusion": // BE 单位向量 → []float64（与 readBE 后的存储一致）
			e := o.p3("extrusion")
			base.Extra[k] = []float64{e.X, e.Y, e.Z}
		case "z_is_zero", "scale_flag", "has_attribs", "dataflags", "flow_dir":
			base.Extra[k] = o.i64(k)
		default: // thickness/elevation/oblique_angle/width_factor/extents_* 等 BD 标量
			base.Extra[k] = o.f64(k)
		}
	}
	return base
}

func jsonBuildLine(o jsonObject) any {
	b := jsonWithExtra(jsonBase(o, "LINE"), o, "thickness", "extrusion", "z_is_zero")
	return &entity.EntLine{BaseEntity: b, Start: o.p3("start"), End: o.p3("end")}
}

func jsonBuildCircle(o jsonObject) any {
	b := jsonWithExtra(jsonBase(o, "CIRCLE"), o, "thickness", "extrusion")
	return &entity.EntCircle{BaseEntity: b, Center: o.p3("center"), Radius: o.f64("radius")}
}

func jsonBuildArc(o jsonObject) any {
	// gold start_angle/end_angle 与解码侧同为弧度（AutoCAD 内部存储）
	b := jsonWithExtra(jsonBase(o, "ARC"), o, "thickness", "extrusion")
	return &entity.EntArc{
		BaseEntity: b,
		Center:     o.p3("center"),
		Radius:     o.f64("radius"),
		AngleStart: o.f64("start_angle"),
		AngleEnd:   o.f64("end_angle"),
	}
}

func jsonBuildPoint(o jsonObject) any {
	// gold POINT 无 location 数组键，以 x/y/z 标量 + x_ang（x 轴角度）表达
	p := entity.Point3{o.f64("x"), o.f64("y"), o.f64("z")}
	if !o.has("x") {
		p = o.p3("location") // 兼容数组形态输出
	}
	b := jsonWithExtra(jsonBase(o, "POINT"), o, "thickness", "extrusion")
	return &entity.EntPoint{BaseEntity: b, Location: p, Rotation: o.f64("x_ang")}
}

func jsonBuildEllipse(o jsonObject) any {
	return &entity.EntEllipse{
		BaseEntity: jsonBase(o, "ELLIPSE"),
		Center:     o.p3("center"),
		MajorAxis:  o.p3("sm_axis"),
		Ratio:      o.f64("axis_ratio"),
		StartAng:   o.f64("start_angle"),
		EndAng:     o.f64("end_angle"),
	}
}

func jsonBuildLwPolyline(o jsonObject) any {
	// gold flag 键即解码侧 flags；points 数组为顶点（bulges 缺失段按 0 对齐）
	e := &entity.EntLwPolyline{
		BaseEntity: jsonBase(o, "LWPOLYLINE"),
		Flags:      uint16(o.i64("flag")),
		Vertices:   o.p2s("points"),
		Bulges:     o.f64s("bulges"),
		Elevation:  o.f64("elevation"),
		ConstWidth: o.f64("const_width"),
		Thickness:  o.f64("thickness"),
	}
	// bulges 与 vertices 等长对齐（解码侧缺失补 0 的逆向口径）
	for len(e.Bulges) < len(e.Vertices) {
		e.Bulges = append(e.Bulges, 0)
	}
	return e
}

func jsonBuildText(o jsonObject) any {
	b := jsonWithExtra(jsonBase(o, "TEXT"), o, "thickness", "elevation", "oblique_angle", "width_factor")
	t := &entity.EntText{
		BaseEntity:  b,
		Text:        o.str("text_value"),
		Insertion:   o.p3("ins_pt"),
		Height:      o.f64("height"),
		Rotation:    o.f64("rotation"),
		HAlign:      uint16(o.i64("horiz_alignment")),
		VAlign:      uint16(o.i64("vert_alignment")),
		Gen:         uint16(o.i64("generation")),
		Extrusion:   o.p3("extrusion"),
		StyleHandle: o.handle("style"),
	}
	if o.has("alignment_pt") {
		p := o.p2("alignment_pt")
		t.AlignPt = &p
	}
	return t
}

func jsonBuildMText(o jsonObject) any {
	// gold text 已按 bit_TV_to_utf8 展开 \U+XXXX，内部直接保存展开后文本
	b := jsonWithExtra(jsonBase(o, "MTEXT"), o, "flow_dir", "extents_height", "extents_width")
	m := &entity.EntMText{
		BaseEntity:  b,
		Text:        o.str("text"),
		Insertion:   o.p3("ins_pt"),
		XAxisDir:    o.p3("x_axis_dir"),
		RectWidth:   o.f64("rect_width"),
		TextHeight:  o.f64("text_height"),
		Attachment:  uint16(o.i64("attachment")),
		LineFactor:  o.f64("linespace_factor"),
		Extrusion:   o.p3("extrusion"),
		StyleHandle: o.handle("style"),
	}
	return m
}

func jsonBuildInsert(o jsonObject) any {
	// attribs 由 linkJSONAttribs 后处理按 ATTRIB owner 归属回填；
	// scale_flag/has_attribs 入 extra（entityField 导出口径与解码侧一致）
	return &entity.EntInsert{
		BaseEntity:  jsonWithExtra(jsonBase(o, "INSERT"), o, "scale_flag", "has_attribs"),
		Position:    o.p3("ins_pt"),
		Scale:       o.p3("scale"),
		Rotation:    o.f64("rotation"),
		Extrusion:   o.p3("extrusion"),
		BlockHeader: o.handle("block_header"),
		Seqend:      o.handle("seqend"),
	}
}

func jsonBuildAttrib(o jsonObject) any {
	b := jsonWithExtra(jsonBase(o, "ATTRIB"), o, "thickness", "elevation", "oblique_angle", "width_factor")
	a := &entity.EntAttrib{
		BaseEntity:  b,
		Text:        o.str("text_value"),
		Tag:         o.str("tag"),
		Insertion:   o.p3("ins_pt"),
		Height:      o.f64("height"),
		Rotation:    o.f64("rotation"),
		Prompt:      o.str("prompt"),
		HAlign:      uint16(o.i64("horiz_alignment")),
		VAlign:      uint16(o.i64("vert_alignment")),
		Gen:         uint16(o.i64("generation")),
		Extrusion:   o.p3("extrusion"),
		StyleHandle: o.handle("style"),
	}
	if o.has("alignment_pt") {
		p := o.p2("alignment_pt")
		a.AlignPt = &p
	}
	return a
}

func jsonBuildSolid(o jsonObject) any {
	name := "SOLID"
	if o.str("entity") == "TRACE" {
		name = "TRACE"
	}
	return &entity.EntSolid{
		BaseEntity: jsonBase(o, name),
		P1:         o.p2("corner1"),
		P2:         o.p2("corner2"),
		P3:         o.p2("corner3"),
		P4:         o.p2("corner4"),
		Elevation:  o.f64("elevation"),
		Thickness:  o.f64("thickness"),
		Extrusion:  o.p3("extrusion"),
		Trace:      name == "TRACE",
	}
}

func jsonBuildFace3d(o jsonObject) any {
	return &entity.EntFace3d{
		BaseEntity:         jsonBase(o, "3DFACE"),
		P1:                 o.p3("corner1"),
		P2:                 o.p3("corner2"),
		P3:                 o.p3("corner3"),
		P4:                 o.p3("corner4"),
		InvisibleEdgeFlags: uint16(o.i64("invis_flags")),
	}
}

func jsonBuildVertex2d(o jsonObject) any {
	return &entity.EntVertex2d{
		BaseEntity: jsonBase(o, "VERTEX_2D"),
		Flags:      uint16(o.i64("flag")),
		Position:   o.p3("point"),
		Bulge:      o.f64("bulge"),
		TangentDir: o.f64("tangent_dir"),
	}
}

func jsonBuildVertex3d(o jsonObject) any {
	return &entity.EntVertex3d{
		BaseEntity: jsonBase(o, "VERTEX_3D"),
		Flags:      uint8(o.i64("flag")),
		Position:   o.p3("point"),
	}
}

func jsonBuildVertexPface(o jsonObject) any {
	return &entity.EntVertexPface{
		BaseEntity: jsonBase(o, "VERTEX_PFACE"),
		Flag:       uint8(o.i64("flag")),
		Position:   o.p3("point"),
	}
}

func jsonBuildVertexPfaceFace(o jsonObject) any {
	f := &entity.EntVertexPfaceFace{
		BaseEntity: jsonBase(o, "VERTEX_PFACE_FACE"),
		Flag:       128, // 解码侧恒定值（LibreDWG 同口径），gold 亦输出 128
	}
	for i, v := range o.f64s("vertind") {
		if i >= 4 {
			break
		}
		f.Vertind[i] = int32(v)
	}
	return f
}

func jsonBuildPolyline2d(o jsonObject) any {
	return &entity.EntPolyline2d{
		BaseEntity: jsonBase(o, "POLYLINE_2D"),
		Flags:      uint16(o.i64("flag")),
		CurveType:  uint16(o.i64("curve_type")),
		WidthStart: o.f64("start_width"),
		WidthEnd:   o.f64("end_width"),
		Thickness:  o.f64("thickness"),
		Elevation:  o.f64("elevation"),
		Extrusion:  o.p3("extrusion"),
	}
}

func jsonBuildPolyline3d(o jsonObject) any {
	return &entity.EntPolyline3d{
		BaseEntity: jsonBase(o, "POLYLINE_3D"),
		Flags70:    uint8(o.i64("flag")),
		Flags75:    uint8(o.i64("curve_type")),
	}
}

func jsonBuildPolylinePface(o jsonObject) any {
	return &entity.EntPolylinePface{
		BaseEntity:  jsonBase(o, "POLYLINE_PFACE"),
		NumVertices: int(o.i64("numverts")),
		NumFaces:    int(o.i64("numfaces")),
	}
}

func jsonBuildBlockLike(o jsonObject) any {
	name := o.str("entity")
	return &entity.EntBlockLike{BaseEntity: jsonBase(o, name), Name: o.str("name")}
}

func jsonBuildSpline(o jsonObject) any {
	// 拟合/控制点与节点向量按 gold 数组还原；beg/end_tan_vec 解码侧未建模
	// （entSpline 无对应字段），不消费
	return &entity.EntSpline{
		BaseEntity:    jsonBase(o, "SPLINE"),
		Scenario:      uint32(o.i64("scenario")),
		Degree:        uint32(o.i64("degree")),
		FitTolerance:  o.f64("fit_tol"),
		KnotTolerance: o.f64("knot_tol"),
		CtrlTolerance: o.f64("ctrl_tol"),
		Knots:         o.f64s("knots"),
		ControlPoints: o.p3s("control_points"),
		Weights:       o.f64s("weights"),
		FitPoints:     o.p3s("fit_pts"),
	}
}

func jsonBuildRay(o jsonObject) any {
	name := o.str("entity")
	return &entity.EntRay{
		BaseEntity: jsonBase(o, name),
		Start:      o.p3("point"),
		UnitVector: o.p3("vector"),
		Xline:      name == "XLINE",
	}
}

func jsonBuildMLine(o jsonObject) any {
	// base_point/extrusion 为主体 3BD（渲染平铺与法向），verts 的
	// lines[j].segparms/areafillparms 为样式线段参数——按 gold 分组还原
	// 并记录每线计数（与 decodeMline 的计数口径一致，分组导出可对齐）
	m := &entity.EntMLine{
		BaseEntity:    jsonBase(o, "MLINE"),
		Scale:         o.f64("scale"),
		Justification: uint8(o.i64("justification")),
		OpenClosed:    uint16(o.i64("flags")),
		BasePoint:     o.p3("base_point"),
		Extrusion:     o.p3("extrusion"),
	}
	for _, v := range o.objs("verts") {
		mv := entity.EntMLineVertex{
			Position:  v.p3("vertex"),
			Direction: v.p3("vertex_direction"),
			Miter:     v.p3("miter_direction"),
		}
		for _, l := range v.objs("lines") {
			sp := l.f64s("segparms")
			ap := l.f64s("areafillparms")
			mv.SegParams = append(mv.SegParams, sp...)
			mv.AreaParams = append(mv.AreaParams, ap...)
			mv.SegCounts = append(mv.SegCounts, len(sp))
			mv.AreaCounts = append(mv.AreaCounts, len(ap))
		}
		m.Vertices = append(m.Vertices, mv)
	}
	return m
}

func jsonBuildHatch(o jsonObject) any {
	// 主体标量与边界路径还原（多段线路径细分出渲染点列；边集路径拼接
	// 直线段端点）；图案定义线（deflines）与样条段专有参数不还原
	h := &entity.EntHatch{
		BaseEntity:          jsonBase(o, "HATCH"),
		IsGradientFill:      uint32(o.i64("is_gradient_fill")),
		Reserved:            uint32(o.i64("reserved")),
		GradientAngle:       o.f64("gradient_angle"),
		GradientShift:       o.f64("gradient_shift"),
		SingleColorGradient: uint32(o.i64("single_color_gradient")),
		GradientTint:        o.f64("gradient_tint"),
		GradientName:        o.str("gradient_name"),
		Elevation:           o.f64("elevation"),
		Extrusion:           o.p3("extrusion"),
		Name:                o.str("name"),
		SolidFill:           o.boolean("is_solid_fill"),
		Associative:         o.boolean("is_associative"),
		Style:               uint16(o.i64("style")),
		PatternType:         uint16(o.i64("pattern_type")),
		Angle:               o.f64("angle"),
		ScaleSpacing:        o.f64("scale_spacing"),
		DoubleFlag:          o.boolean("double_flag"),
	}
	// 图案定义线（deflines）：angle + 原点 + 偏移 + 划线参数数组，
	// 与 decodeHatch 的 hatchDefLine 同构还原（渲染按线距平铺时消费）
	for _, d := range o.objs("deflines") {
		h.Deflines = append(h.Deflines, entity.HatchDefLine{
			Angle:  d.f64("angle"),
			Pt0:    d.p2("pt0"),
			Offset: d.p2("offset"),
			Dashes: d.f64s("dashes"),
		})
	}
	for _, po := range o.objs("paths") {
		p := entity.HatchPath{
			Flag:           uint32(po.i64("flag")),
			IsPolyline:     po.i64("flag")&2 != 0,
			BulgesPresent:  po.boolean("bulges_present"),
			Closed:         po.boolean("closed"),
			NumSegsOrPaths: uint32(po.i64("num_segs_or_paths")),
		}
		if p.IsPolyline {
			for _, vo := range po.objs("polyline_paths") {
				p.PolyVerts = append(p.PolyVerts, entity.HatchPolyVert{P: vo.p2("point"), Bulge: vo.f64("bulge")})
			}
			var verts []entity.Point2
			var bulges []float64
			for _, pv := range p.PolyVerts {
				verts = append(verts, pv.P)
				bulges = append(bulges, pv.Bulge)
			}
			pts := verts
			if p.BulgesPresent {
				pts = entity.PolylineWithBulges(verts, bulges, p.Closed, 64)
			}
			if p.Closed {
				pts = entity.ClosePath(pts)
			}
			p.Points = pts
		} else {
			var pts []entity.Point2
			for _, so := range po.objs("segs") {
				s := entity.HatchSeg{CurveType: uint8(so.i64("curve_type"))}
				switch s.CurveType {
				case 1: // 直线段：端点直接进入渲染点列
					s.First, s.Second = so.p2("first_endpoint"), so.p2("second_endpoint")
					if len(pts) == 0 {
						pts = append(pts, s.First)
					}
					pts = append(pts, s.Second)
				case 2, 3: // 圆弧/椭圆弧段：保留原始参数（不细分）
					s.Center = so.p2("center")
					s.Radius = so.f64("radius")
					s.Ratio = so.f64("minor_major_ratio")
					s.StartAng = so.f64("start_angle")
					s.EndAng = so.f64("end_angle")
					s.Ccw = so.boolean("is_ccw")
				}
				p.Segs = append(p.Segs, s)
			}
			p.Points = pts
		}
		h.Paths = append(h.Paths, p)
	}
	return h
}

func jsonBuildWipeout(o jsonObject) any {
	// WIPEOUT/IMAGE 同布局：标量 + 图像变换主键；裁剪顶点与 imagedef 句柄
	// 不还原（渲染仅消费 pt0/uvec/vvec/image_size 的边界框）
	name := o.str("entity")
	w := &entity.EntWipeout{
		BaseEntity:       jsonBase(o, name),
		ClassVersion:     uint32(o.i64("class_version")),
		Pt0:              o.p3("pt0"),
		Uvec:             o.p3("uvec"),
		Vvec:             o.p3("vvec"),
		ImageSize:        o.p2("image_size"),
		DisplayProps:     uint16(o.i64("display_props")),
		Clipping:         o.boolean("clipping"),
		Brightness:       uint8(o.i64("brightness")),
		Contrast:         uint8(o.i64("contrast")),
		Fade:             uint8(o.i64("fade")),
		ClipMode:         uint8(o.i64("clip_mode")),
		ClipBoundaryType: uint16(o.i64("clip_boundary_type")),
	}
	for _, cv := range o.p2s("clip_verts") {
		w.ClipVerts = append(w.ClipVerts, cv)
	}
	return w
}

func jsonBuildTolerance(o jsonObject) any {
	return &entity.EntTolerance{
		BaseEntity:   jsonBase(o, "TOLERANCE"),
		Text:         o.str("text_value"),
		UnknownShort: uint16(o.i64("unknown_short")),
		Insertion:    o.p3("ins_pt"),
		XDirection:   o.p3("x_direction"),
		Extrusion:    o.p3("extrusion"),
		Height:       o.f64("height"),
		Dimgap:       o.f64("dimgap"),
		Dimstyle:     o.handle("dimstyle"),
	}
}

func jsonBuildViewport(o jsonObject) any {
	return &entity.EntViewport{
		BaseEntity:          jsonBase(o, "VIEWPORT"),
		Center:              o.p3("center"),
		Width:               o.f64("width"),
		Height:              o.f64("height"),
		ViewTarget:          o.p3("view_target"),
		ViewDir:             o.p3("VIEWDIR"),
		ViewTwist:           o.f64("VIEWTWIST"),
		ViewSize:            o.f64("VIEWSIZE"),
		LensLength:          o.f64("LENSLENGTH"),
		FrontZ:              o.f64("FRONTZ"),
		BackZ:               o.f64("BACKZ"),
		SnapAng:             o.f64("SNAPANG"),
		ViewCtr:             o.p2("VIEWCTR"),
		SnapBase:            o.p2("SNAPBASE"),
		SnapUnit:            o.p2("SNAPUNIT"),
		GridUnit:            o.p2("GRIDUNIT"),
		CircleZoom:          uint16(o.i64("circle_zoom")),
		GridMajor:           uint16(o.i64("grid_major")),
		NumFrozenLayers:     uint32(o.i64("num_frozen_layers")),
		StatusFlag:          uint32(o.i64("status_flag")),
		StyleSheet:          o.str("style_sheet"),
		RenderMode:          uint8(o.i64("render_mode")),
		UcsVP:               o.boolean("UCSVP"),
		UcsAtOrigin:         o.boolean("ucs_at_origin"),
		Ucsorg:              o.p3("UCSORG"),
		Ucsxdir:             o.p3("UCSXDIR"),
		Ucsydir:             o.p3("UCSYDIR"),
		UcsElevation:        o.f64("ucs_elevation"),
		UcsOrthoView:        uint16(o.i64("UCSORTHOVIEW")),
		ShadeplotMode:       uint16(o.i64("shadeplot_mode")),
		UseDefaultLights:    o.boolean("use_default_lights"),
		DefaultLightingType: uint8(o.i64("default_lighting_type")),
		Brightness:          o.f64("brightness"),
		Contrast:            o.f64("contrast"),
	}
}

func jsonBuildLeader(o jsonObject) any {
	// LEADER 标量基键；annotated/points 等数组键由解码侧同样不入审计导出，
	// 渲染消费的顶点数组在 entLeader 中按需还原
	l := &entity.EntLeader{
		BaseEntity:     jsonBase(o, "LEADER"),
		AnnotationType: uint16(o.i64("annotation_type")),
		PathType:       uint16(o.i64("path_type")),
	}
	for _, pt := range o.p3s("points") {
		l.Points = append(l.Points, pt)
	}
	return l
}

func jsonBuildDimension(o jsonObject) any {
	gold := o.str("entity")
	d := &entity.EntDimension{BaseEntity: jsonBase(o, gold)}
	d.Extrusion = o.p3("extrusion")
	d.TextMidpoint = o.p3("text_midpt")
	d.Elevation = o.f64("elevation")
	d.DimFlag = uint8(o.i64("flag"))
	d.DimFlags = uint8(o.i64("flag1"))
	d.Flag2 = uint8(o.i64("flag2"))
	d.UserText = o.str("user_text")
	d.TextRotation = o.f64("text_rotation")
	d.HorizontalDir = o.f64("horiz_dir")
	d.InsertScale = o.p3("ins_scale")
	d.InsertRotation = o.f64("ins_rotation")
	d.AttachmentPoint = uint16(o.i64("attachment"))
	d.LineSpacingStyle = uint16(o.i64("lspace_style"))
	d.LineSpacingFactor = o.f64("lspace_factor")
	d.ActualMeasurement = o.f64("act_measurement")
	d.ClassVersion = uint8(o.i64("class_version"))
	// 公共点组：ORDINATE 为 def_pt(10)/feature_location_pt(13)/
	// leader_endpt(14)；ANG2LN 为 def_pt(2RD)→p16 + xline1start_pt(13)/
	// xline1end_pt(14)/xline2start_pt(15)/xline2end_pt(16 系)；其余类型
	// def_pt(10) + xline1_pt(13) + xline2_pt(14)（与 readDimSpecific
	// 各布局载体一一对应）
	d.Point10 = o.p3("def_pt")
	d.Point13 = o.p3("xline1_pt")
	d.Point14 = o.p3("xline2_pt")
	if o.has("feature_location_pt") {
		d.Point13 = o.p3("feature_location_pt")
	}
	if o.has("leader_endpt") {
		d.Point14 = o.p3("leader_endpt")
	}
	if o.has("xline1start_pt") {
		// ANG2LN：def_pt 为 2RD 形态存 p16x/p16y，四 xline 点按 13/14/
		// 15/16 组码序入载体 point13/14/15/10
		p2 := o.p2("def_pt")
		d.Point16x, d.P16y, d.HasPoint16 = p2.X, p2.Y, true
		d.Point13 = o.p3("xline1start_pt")
		d.Point14 = o.p3("xline1end_pt")
		d.Point15 = o.p3("xline2start_pt")
		d.HasPoint15 = true
		d.Point10 = o.p3("xline2end_pt")
	}
	d.InsertPoint = entity.Point3{d.Point10.X, d.Point10.Y, d.Elevation}
	d.HasInsertPoint = true
	d.ExtLineRotation = o.f64("oblique_angle")
	d.DimRotation = o.f64("dim_rotation")
	// 弧长专属（ARC_DIMENSION）
	d.DefPt = o.p3("def_pt")
	d.IsPartial = o.boolean("is_partial")
	d.ArcStartParam = o.f64("arc_start_param")
	d.ArcEndParam = o.f64("arc_end_param")
	d.HasLeader = o.boolean("has_leader")
	d.Leader1Pt = o.p3("leader1_pt")
	d.Leader2Pt = o.p3("leader2_pt")
	d.DimstyleHandle = o.handle("dimstyle")
	d.AnonymousBlock = o.handle("anonymous_block")
	return d
}

func jsonBuildAcis(o jsonObject) any {
	// 标量基键还原；acis_data（ACIS 文本模型）按 version 双形态还原到
	// acisData（与 decodeAcisVer 同一存储口径，JSON 来源实体消费侧完整）
	gold := o.str("entity")
	a := &entity.EntAcis{
		BaseEntity:           jsonBase(o, gold),
		AcisEmpty:            o.boolean("acis_empty"),
		AcisEmptyBit:         o.boolean("acis_empty_bit"),
		Unknown:              uint8(o.i64("unknown")),
		Version:              uint16(o.i64("version")),
		WireframeDataPresent: o.boolean("wireframe_data_present"),
		PointPresent:         o.boolean("point_present"),
		Isolines:             uint32(o.i64("isolines")),
		IsolinePresent:       o.boolean("isoline_present"),
		HasRevisionGuid:      o.boolean("has_revision_guid"),
		RevisionMajor:        uint32(o.i64("revision_major")),
		RevisionMinor1:       uint16(o.i64("revision_minor1")),
		RevisionMinor2:       uint16(o.i64("revision_minor2")),
		EndMarker:            uint32(o.i64("end_marker")),
	}
	// acis_data 数组还原（out_json json_3dsolid 的 ARRAY 输出）：
	// SAT（version<2）为按 \n 分行的文本数组（行分隔符不保留，重组以 \n
	// 连接，与 acis_empty=0 时的解混淆文本一致）；SAB（version=2）恒为
	// ["ACIS BinaryFile", <hex>] 双元素——首元素即 acisData 前 15 字节，
	// 次元素为剩余字节的 hex 串，sabSize 取总长（decodeAcisVer 的 end 语义）。
	// acis_empty=1 的实体无该键（gold 同口径不输出）。
	if !a.AcisEmpty {
		if lines, ok := o.raw("acis_data").([]any); ok && len(lines) > 0 {
			if a.Version >= 2 {
				data := []byte(o.strAt("acis_data", 0))
				data = append(data, jsonHexBytes(o.strAt("acis_data", 1))...)
				a.AcisData = data
				a.SabSize = len(data)
			} else {
				parts := make([]string, 0, len(lines))
				for _, ln := range lines {
					if s, ok := ln.(string); ok {
						parts = append(parts, s)
					}
				}
				a.AcisData = []byte(strings.Join(parts, "\n"))
			}
		}
		// encr_sat_data（DXF 往返产生的加密块 hex 串数组）→ blocks 原文
		if raw, ok := o.raw("encr_sat_data").([]any); ok {
			for _, e := range raw {
				if s, ok := e.(string); ok {
					if b := jsonHexBytes(s); b != nil {
						a.Blocks = append(a.Blocks, b)
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
	return &entity.EntOle2Frame{
		BaseEntity: jsonBase(o, gold),
		OleType:    uint16(o.i64("type")),
		Mode:       uint16(o.i64("mode")),
		LockAspect: uint8(o.i64("lock_aspect")),
		DataSize:   uint32(o.i64("data_size")),
		Data:       data,
	}
}

func jsonBuildOleFrame(o jsonObject) any {
	gold := o.str("entity")
	data := jsonHexBytes(o.str("data"))
	return &entity.EntOleFrame{
		BaseEntity: jsonBase(o, gold),
		Flag:       uint16(o.i64("flag")),
		Mode:       uint16(o.i64("mode")),
		DataSize:   uint32(o.i64("data_size")),
		Data:       data,
	}
}

func jsonBuildLight(o jsonObject) any {
	l := &entity.EntLight{
		BaseEntity:           jsonBase(o, "LIGHT"),
		ClassVersion:         uint32(o.i64("class_version")),
		Name:                 o.str("name"),
		LightType:            uint32(o.i64("type")),
		Status:               o.boolean("status"),
		PlotGlyph:            o.boolean("plot_glyph"),
		Intensity:            o.f64("intensity"),
		Position:             o.p3("position"),
		Target:               o.p3("target"),
		AttenuationType:      uint32(o.i64("attenuation_type")),
		UseAttenuationLimits: o.boolean("use_attenuation_limits"),
		AttenuationStart:     o.f64("attenuation_start_limit"),
		AttenuationEnd:       o.f64("attenuation_end_limit"),
		HotspotAngle:         o.f64("hotspot_angle"),
		FalloffAngle:         o.f64("falloff_angle"),
		CastShadows:          o.boolean("cast_shadows"),
		ShadowType:           uint32(o.i64("shadow_type")),
		ShadowMapSize:        uint16(o.i64("shadow_map_size")),
		ShadowMapSoftness:    int8(o.i64("shadow_map_softness")),
	}
	// light_color 双形态：标量（pre-R2004 索引）或 CMC 对象（R2004+）
	switch c := o.raw("light_color").(type) {
	case float64:
		l.LightColorIndex = uint16(int64(c))
	case map[string]any:
		m := jsonObject(c)
		l.HasLightColorTrue = true
		l.LightColorIndex = uint16(m.i64("index"))
		if v32, err := parseHexUint32(m.str("rgb")); err == nil {
			l.LightColorRGB = v32
		}
		l.LightColorFlag = uint8(m.i64("flag"))
	}
	return l
}

// jsonMLeaderCMC gold CMC 双形态 → mleaderCMC：对象形态（R2004+）与
// readMLeaderCMC 同口径还原——rgb method 越界时修正为 0xc2 前缀，index
// 按调色板反查覆盖（与 DWG 侧导出一致）；标量为 pre-R2004 索引。
func jsonMLeaderCMC(v any) entity.MleaderCMC {
	m, ok := v.(map[string]any)
	if !ok {
		if f, ok := v.(float64); ok {
			return entity.MleaderCMC{Index: uint16(f)}
		}
		return entity.MleaderCMC{}
	}
	o := jsonObject(m)
	var c entity.MleaderCMC
	c.IsTrue = true
	if v32, err := parseHexUint32(o.str("rgb")); err == nil {
		c.Rgb = v32
		if method := c.Rgb >> 24; method < 0xc0 || method > 0xc8 {
			c.Rgb = 0xc2000000 | (c.Rgb & 0xffffff)
		}
		c.Index = uint16(entity.DwgFindColorIndex(c.Rgb))
	}
	if f, ok := o.num("index"); ok && (o.str("rgb") == "" || c.Index == 0) {
		c.Index = uint16(f)
	}
	c.Flag = uint8(o.i64("flag"))
	return c
}

func jsonBuildMLeader(o jsonObject) any {
	// 顶层标量 + ctx 全结构还原（jsonMLeaderCtx：leaders/lines 三层嵌套、
	// txt/blk 内容两分支、base 三点组；键名均为 gold 展平键）+ 顶层 CMC
	// 双形态与句柄系，与 decodeMLeader 的模型字段一一对应
	m := &entity.EntMLeader{BaseEntity: jsonBase(o, "MULTILEADER")}
	if o.has("class_version") {
		m.HasVersion = true
		m.ClassVersion = uint16(o.i64("class_version"))
	}
	m.MleaderType = uint16(o.i64("type"))
	m.Flags = uint32(o.i64("flags"))
	m.LineColor = jsonMLeaderCMC(o.raw("line_color"))
	m.LineLinewt = int32(o.i64("line_linewt"))
	m.LineLtype = o.handle("line_ltype")
	m.HasLanding = o.boolean("has_landing")
	m.HasDogleg = o.boolean("has_dogleg")
	m.LandingDist = o.f64("landing_dist")
	m.ArrowHandle = o.handle("arrow_handle")
	m.ArrowSize = o.f64("arrow_size")
	m.StyleContent = uint16(o.i64("style_content"))
	m.TextStyle = o.handle("text_style")
	m.TextLeft = uint16(o.i64("text_left"))
	m.TextRight = uint16(o.i64("text_right"))
	m.TextAngletype = uint16(o.i64("text_angletype"))
	m.TextAlignment = uint16(o.i64("text_alignment"))
	m.TextColor = jsonMLeaderCMC(o.raw("text_color"))
	m.HasTextFrame = o.boolean("has_text_frame")
	m.BlockStyle = o.handle("block_style")
	m.BlockColor = jsonMLeaderCMC(o.raw("block_color"))
	m.BlockScale = o.p3("block_scale")
	m.BlockRotation = o.f64("block_rotation")
	m.StyleAttachment = uint16(o.i64("style_attachment"))
	m.IsAnnotative = o.boolean("is_annotative")
	m.IsNegTextdir = o.boolean("is_neg_textdir")
	m.IpeAlignment = uint16(o.i64("ipe_alignment"))
	m.Justification = uint16(o.i64("justification"))
	m.ScaleFactor = o.f64("scale_factor")
	// VERSIONS(R_14, R_2007) 的箭头/块标签数组与 SINCE R_2010b/R_2013b
	// 尾段：gold 按版本段条件输出，键缺失时零值（与解码侧分支一致）
	for _, ao := range o.objs("arrowheads") {
		m.Arrowheads = append(m.Arrowheads, entity.MleaderArrowhead{
			IsDefault: ao.boolean("is_default"),
			Arrowhead: ao.handle("arrowhead"),
		})
	}
	for _, bo := range o.objs("blocklabels") {
		m.Blocklabels = append(m.Blocklabels, entity.MleaderBlockLabel{
			Attdef:    bo.handle("attdef"),
			LabelText: bo.str("label_text"),
			UiIndex:   uint16(bo.i64("ui_index")),
			Width:     bo.f64("width"),
		})
	}
	if m.HasVersion {
		m.AttachDir = uint16(o.i64("attach_dir"))
		m.AttachTop = uint16(o.i64("attach_top"))
		m.AttachBottom = uint16(o.i64("attach_bottom"))
		m.IsTextExtended = o.boolean("is_text_extended")
	}
	m.MleaderStyle = o.handle("mleaderstyle")
	jsonMLeaderCtx(o, m)
	return m
}

// jsonMLeaderCtx gold 展平 ctx 键 → mleaderContextData：标量组、
// leaders→lines→breaks/points 三层嵌套、content txt/blk 内容两分支与
// base 三点组。gold 的 has_content_txt 缺省（pre-R2004 无该键形态）按
// content.txt 键存在性判定（LibreDWG HAS_CONTENT 分支输出键集互斥）。
func jsonMLeaderCtx(o jsonObject, m *entity.EntMLeader) {
	c := &m.Ctx
	c.NumLeaders = uint32(o.i64("ctx.num_leaders"))
	for _, lo := range o.objs("ctx.leaders") {
		var n entity.MleaderNode
		n.HasLastLeaderLinePoint = lo.boolean("has_lastleaderlinepoint")
		n.LastLeaderLinePoint = lo.p3("lastleaderlinepoint")
		n.HasDogleg = lo.boolean("has_dogleg")
		n.DoglegVector = lo.p3("dogleg_vector")
		n.NumBreaks = uint32(lo.i64("num_breaks"))
		for _, bo := range lo.objs("breaks") {
			n.Breaks = append(n.Breaks, entity.MleaderBreak{Start: bo.p3("start"), End: bo.p3("end")})
		}
		n.BranchIndex = uint32(lo.i64("branch_index"))
		n.DoglegLength = lo.f64("dogleg_length")
		n.NumLines = uint32(lo.i64("num_lines"))
		for _, lno := range lo.objs("lines") {
			var ln entity.MleaderLine
			ln.Points = lno.p3s("points")
			ln.NumBreaks = uint32(lno.i64("num_breaks"))
			for _, bo := range lno.objs("breaks") {
				ln.Breaks = append(ln.Breaks, entity.MleaderBreak{Start: bo.p3("start"), End: bo.p3("end")})
			}
			ln.LineIndex = uint32(lno.i64("line_index"))
			if m.HasVersion { // SINCE R_2010b 的引线线段专有键
				ln.MleaderType = uint16(lno.i64("type"))
				ln.Color = jsonMLeaderCMC(lno.raw("color"))
				ln.Linewt = int32(lno.i64("linewt"))
				ln.ArrowSize = lno.f64("arrow_size")
				ln.ArrowHandle = lno.handle("arrow_handle")
				ln.Flags = uint32(lno.i64("flags"))
			}
			n.Lines = append(n.Lines, ln)
		}
		if m.HasVersion {
			n.AttachDir = uint16(lo.i64("attach_dir"))
		}
		c.Leaders = append(c.Leaders, n)
	}
	c.ScaleFactor = o.f64("ctx.scale_factor")
	c.ContentBase = o.p3("ctx.content_base")
	c.TextHeight = o.f64("ctx.text_height")
	c.ArrowSize = o.f64("ctx.arrow_size")
	c.LandingGap = o.f64("ctx.landing_gap")
	c.TextLeft = uint16(o.i64("ctx.text_left"))
	c.TextRight = uint16(o.i64("ctx.text_right"))
	c.TextAngletype = uint16(o.i64("ctx.text_angletype"))
	c.TextAlignment = uint16(o.i64("ctx.text_alignment"))
	_, txtPresent := o["ctx.content.txt.default_text"]
	c.HasContentTxt = o.boolean("ctx.has_content_txt") || txtPresent
	if c.HasContentTxt {
		t := &c.Txt
		t.DefaultText = o.str("ctx.content.txt.default_text")
		t.Normal = o.p3("ctx.content.txt.normal")
		t.StyleHandle = o.handle("ctx.content.txt.style")
		t.Location = o.p3("ctx.content.txt.location")
		t.Direction = o.p3("ctx.content.txt.direction")
		t.Rotation = o.f64("ctx.content.txt.rotation")
		t.Width = o.f64("ctx.content.txt.width")
		t.Height = o.f64("ctx.content.txt.height")
		t.LineSpacingFactor = o.f64("ctx.content.txt.line_spacing_factor")
		t.LineSpacingStyle = uint16(o.i64("ctx.content.txt.line_spacing_style"))
		t.Color = jsonMLeaderCMC(o.raw("ctx.content.txt.color"))
		t.Alignment = uint16(o.i64("ctx.content.txt.alignment"))
		t.Flow = uint16(o.i64("ctx.content.txt.flow"))
		t.BgColor = jsonMLeaderCMC(o.raw("ctx.content.txt.bg_color"))
		t.BgScale = o.f64("ctx.content.txt.bg_scale")
		t.BgTransparency = uint32(o.i64("ctx.content.txt.bg_transparency"))
		t.IsBgFill = o.boolean("ctx.content.txt.is_bg_fill")
		t.IsBgMaskFill = o.boolean("ctx.content.txt.is_bg_mask_fill")
		t.ColType = uint16(o.i64("ctx.content.txt.col_type"))
		t.IsHeightAuto = o.boolean("ctx.content.txt.is_height_auto")
		t.ColWidth = o.f64("ctx.content.txt.col_width")
		t.ColGutter = o.f64("ctx.content.txt.col_gutter")
		t.IsColFlowReversed = o.boolean("ctx.content.txt.is_col_flow_reversed")
		t.NumColSizes = uint32(o.i64("ctx.content.txt.num_col_sizes"))
		t.ColSizes = o.f64s("ctx.content.txt.col_sizes")
		t.WordBreak = o.boolean("ctx.content.txt.word_break")
		t.Unknown = o.boolean("ctx.content.txt.unknown")
	} else {
		c.HasContentBlk = o.boolean("ctx.has_content_blk")
		if c.HasContentBlk {
			k := &c.Blk
			k.BlockTable = o.handle("ctx.content.blk.block_table")
			k.Normal = o.p3("ctx.content.blk.normal")
			k.Location = o.p3("ctx.content.blk.location")
			k.Scale = o.p3("ctx.content.blk.scale")
			k.Rotation = o.f64("ctx.content.blk.rotation")
			k.Color = jsonMLeaderCMC(o.raw("ctx.content.blk.color"))
			if tr := o.f64s("ctx.content.blk.transform"); len(tr) == 16 {
				copy(k.Transform[:], tr)
			}
		}
	}
	c.Base = o.p3("ctx.base")
	c.BaseDir = o.p3("ctx.base_dir")
	c.BaseVert = o.p3("ctx.base_vert")
	c.IsNormalReversed = o.boolean("ctx.is_normal_reversed")
	if m.HasVersion { // SINCE R_2010b
		c.TextTop = uint16(o.i64("ctx.text_top"))
		c.TextBottom = uint16(o.i64("ctx.text_bottom"))
	}
}

func jsonBuildShape(o jsonObject) any {
	return &entity.EntShape{
		BaseEntity:  jsonBase(o, "SHAPE"),
		Insertion:   o.p3("ins_pt"),
		Scale:       o.f64("scale"),
		Rotation:    o.f64("rotation"),
		WidthFactor: o.f64("width_factor"),
		Oblique:     o.f64("oblique_angle"),
		Thickness:   o.f64("thickness"),
	}
}

func jsonBuildProxyEntity(o jsonObject) any {
	gold := o.str("entity")
	p := &entity.EntProxyEntity{
		BaseEntity:    jsonBase(o, gold),
		ProxyID:       uint32(o.i64("proxy_id")),
		Version:       uint32(o.i64("version")),
		MaintVersion:  uint32(o.i64("maint_version")),
		DwgVersionNum: uint32(o.i64("dwg_version")),
		FromDxf:       o.boolean("from_dxf"),
		DataNumBits:   uint32(o.i64("data_numbits")),
		NumObjids:     uint32(o.i64("num_objids")),
		ProxyDataSize: uint32(o.i64("proxy_data_size")),
	}
	p.ProxyData = jsonHexBytes(o.str("proxy_data"))
	return p
}

func jsonBuildUnknownEnt(o jsonObject) any {
	name := o.str("entity")
	e := &entity.EntUnknownEnt{BaseEntity: jsonBase(o, name)}
	if dx := o.str("_subclass"); dx != "" {
		e.Extra = map[string]any{"dxfname": dx}
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
