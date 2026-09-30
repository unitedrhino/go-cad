// 本文件为内部对象解码的通用框架：objGeneric 结果结构、gfRead 字段
// 读取原语（T/BS/BD/CMC 等，随版本与 R2007+ 字符串流切换）、解码器
// 注册表（固定码 + DXF 类名）、decodeInternalObject 主流程
// （公共头 → dat 流专有字段 → handle 流）与 verUntilR2004 版本判定。

package cad

import (
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"os"
	"strings"
)

// ---- 通用内部对象解码框架 ----
// 面向布局简单的非实体对象（SCALE/DICTIONARYVAR/APPID 等）：dat 流专有
// 字段由各类型解码器按 dwg.spec 顺序读取，handle 流（owner + reactors +
// xdic + 附加引用）统一处理。字段以 名称+值 对记录，供调试与验证导出。

// objField 内部对象的字段对。
type objField struct {
	Key string // 字段名（与 dwgread JSON 键一致）
	Val any    // 值（int64/float64/bool/string）
}

// objGeneric 通用内部对象解码结果。
type objGeneric struct {
	Name          string     // 对象类型名（固定码名或 DXF 类名）
	controlType   uint16     // CONTROL 对象的类型码（供 controlSpecs 查询）
	Handle        uint64     // 对象主句柄
	Owner         uint64     // ownerhandle 绝对句柄
	ObjSizeBit    uint64     // bitsize：handle 流起点（相对 MS 字段之后的位）
	NumReactors   int        // reactor 数量
	XdicMissing   bool       // R2004+：无 xdicobjhandle 标记
	HasDsData     bool       // R2013+：关联数据集标志
	Unknown       bool       // 无专门解码器，按 UNKNOWN_OBJ 兜底
	OrigClass     string     // Unknown 时的原始 DXF 类名（类表解析）
	RawHandleBits string     // handle 流原始位串（0/1），供重编码原样写回
	hdOffsetBits  uint64     // dat 段前导位（body 内 RL bitsize 起点；重编码位长校验用）
	headRawBits   string     // RL bitsize 之后至 handle 流起点的原始位串（H/EED/公共头/专有字段），供重编码原样写回
	r2010Plus     bool       // R2010+ 记录布局（无内联 RL，headRawBits 起点为 dataStartBit）
	valueHandle91 bool       // ACSH/ASSOCVARIABLE：EvalVariant/evalexpr 的 handle 值占 handle 流 1 个引用（解码→hdl 阶段传递）
	hdlCount      int        // SURFACEACTIONBODY 族：handle 流附加引用数（解码→hdl 阶段传递）
	preBits       string     // R2010+ body 中 dataStartBit 之前的记录头前导位串（MS size/hss UMC），重编码原样回放
	bodyBitOff    uint64     // R2010+ body 内 MS size 尾部对齐位（rec.bodyBitOffset），重解码重建用
	sizeBytes     uint32     // 源对象记录的 MS size（R2010+ 重编码重建 rec 元数据用）
	hSizeField    uint32     // R2010+ handle-stream-size 字段位宽
	hssBits       uint32     // R2010+ handle-stream-size 值（位）
	Fields        []objField // dat 流专有字段（按 spec 顺序）
	Handles       []uint64   // handle 流中 owner/reactors/xdic 之外的引用
}

// Field 按名称取字段值（未找到返回 nil）。
func (g *objGeneric) Field(key string) any {
	for _, f := range g.Fields {
		if f.Key == key {
			return f.Val
		}
	}
	return nil
}

// FieldPath 按点分路径取嵌套字段值（Field 的递归版，如
// "edge_color.rgb"）。匹配顺序：①整键精确（cells[0].geom_data_flag
// 这类展开键本身含点与下标，不能按点切断）；②展平视图（map 值的
// 内层键，如 "rowstyles[0].borders[0].color.rgb"）；③按顶层键 + 逐段
// 下钻 map。
func (g *objGeneric) FieldPath(key string) any {
	for _, f := range g.Fields {
		if f.Key == key {
			return f.Val
		}
	}
	if v, ok := g.DebugFields()[key]; ok {
		return v
	}
	parts := strings.Split(key, ".")
	var cur any
	for _, f := range g.Fields {
		if f.Key == parts[0] {
			cur = f.Val
			break
		}
	}
	for _, p := range parts[1:] {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

// DebugFields 导出全部字段（嵌套 map 值展开为 a.b 扁平键），
// 供值级对齐审计与调试对照 dwgread JSON 的扁平键。
func (g *objGeneric) DebugFields() map[string]any {
	out := map[string]any{}
	var add func(prefix string, m map[string]any)
	add = func(prefix string, m map[string]any) {
		for k, v := range m {
			if sub, ok := v.(map[string]any); ok {
				add(prefix+k+".", sub)
			} else {
				out[prefix+k] = v
			}
		}
	}
	for _, f := range g.Fields {
		if sub, ok := f.Val.(map[string]any); ok {
			add(f.Key+".", sub)
		} else {
			out[f.Key] = f.Val
		}
	}
	return out
}

// internalObjectSpec 内部对象类型描述。
type internalObjectSpec struct {
	decode          func(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error // dat 流专有字段
	hdl             func(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error // 自定义 handle 流（owner/reactors/xdic 之后调用）
	extraHandles    int                                                                           // handle 流中 xdic 之后的固定引用数
	handleVectorKey string                                                                        // 引用数量字段名（如 GROUP 的 num_groups）：handle 流读该数量的引用
	forceStrings    bool                                                                          // SCALE 特例：has_strings 位恒 0 但 LibreDWG 强制按 1 处理（decode_r2007.c FIXME wrong bit）
	jsonName        string                                                                        // gold JSON object 名与类表 DXF 名不一致时覆盖（如 DYNAMICBLOCKPURGEPREVENTER）
}

// internalFixedDecoders 固定类型码的内部对象（键为位级类型码）。

var internalFixedDecoders = map[uint16]internalObjectSpec{
	0x43:  {decode: decodeGenericAPPID, extraHandles: 1},                                    // APPID
	0x48:  {decode: decodeGenericGROUP, handleVectorKey: "num_groups"},                      // GROUP
	0x52:  {decode: decodeGenericLAYOUT, extraHandles: 4, handleVectorKey: "num_viewports"}, // LAYOUT
	0x30:  {decode: decodeGenericCONTROL, hdl: decodeGenericCONTROL_HDL},                    // BLOCK_CONTROL
	0x31:  {decode: decodeGenericBLOCKHEADER, hdl: decodeGenericBLOCKHEADER_HDL},            // BLOCK_HEADER
	0x32:  {decode: decodeGenericCONTROL, hdl: decodeGenericCONTROL_HDL},                    // LAYER_CONTROL
	0x34:  {decode: decodeGenericCONTROL, hdl: decodeGenericCONTROL_HDL},                    // STYLE_CONTROL
	0x38:  {decode: decodeGenericCONTROL, hdl: decodeGenericCONTROL_HDL},                    // LTYPE_CONTROL
	0x3C:  {decode: decodeGenericCONTROL, hdl: decodeGenericCONTROL_HDL},                    // VIEW_CONTROL
	0x3E:  {decode: decodeGenericCONTROL, hdl: decodeGenericCONTROL_HDL},                    // UCS_CONTROL
	0x40:  {decode: decodeGenericCONTROL, hdl: decodeGenericCONTROL_HDL},                    // VPORT_CONTROL
	0x42:  {decode: decodeGenericCONTROL, hdl: decodeGenericCONTROL_HDL},                    // APPID_CONTROL
	0x44:  {decode: decodeGenericCONTROL, hdl: decodeGenericCONTROL_HDL},                    // DIMSTYLE_CONTROL
	0x45:  {decode: decodeGenericDIMSTYLE, hdl: decodeGenericDIMSTYLE_HDL},                  // DIMSTYLE
	0x35:  {decode: decodeGenericSTYLE},                                                     // STYLE
	0x41:  {decode: decodeGenericVPORT, hdl: decodeGenericVPORT_HDL},                        // VPORT
	0x49:  {decode: decodeGenericMLINESTYLE, hdl: decodeGenericMLINESTYLE_HDL},              // MLINESTYLE
	0x46:  {decode: decodeGenericCONTROL, hdl: decodeGenericCONTROL_HDL},                    // VX_CONTROL
	0x47:  {decode: decodeGenericVX_TABLE_RECORD, hdl: decodeGenericVX_TABLE_RECORD_HDL},    // VX_TABLE_RECORD
	0x50:  {decode: decodeGenericPLACEHOLDER},                                               // PLACEHOLDER
	0x39:  {decode: decodeGenericLTYPE, extraHandles: 1},                                    // LTYPE
	0x1F3: {decode: decodeGenericPROXY_OBJECT, hdl: decodeGenericPROXY_OBJECT_HDL},          // PROXY_OBJECT
}

// internalClassDecoders 类类型内部对象（键为 DXF 类名，经类名表解析）。
var internalClassDecoders = map[string]internalObjectSpec{
	"LAYOUT":                 {decode: decodeGenericLAYOUT, extraHandles: 4, handleVectorKey: "num_viewports"},
	"DICTIONARYVAR":          {decode: decodeGenericDICTIONARYVAR},
	"TABLESTYLE":             {decode: decodeGenericTABLESTYLE, hdl: decodeGenericTABLESTYLE_HDL},
	"MLEADERSTYLE":           {decode: decodeGenericMLEADERSTYLE, hdl: decodeGenericMLEADERSTYLE_HDL},
	"ACDBMLEADERSTYLE":       {decode: decodeGenericMLEADERSTYLE, hdl: decodeGenericMLEADERSTYLE_HDL},
	"SECTIONVIEWSTYLE":       {decode: decodeGenericSECTIONVIEWSTYLE, hdl: decodeGenericSECTIONVIEWSTYLE_HDL},
	"ACDBSECTIONVIEWSTYLE":   {decode: decodeGenericSECTIONVIEWSTYLE, hdl: decodeGenericSECTIONVIEWSTYLE_HDL},
	"DETAILVIEWSTYLE":        {decode: decodeGenericDETAILVIEWSTYLE, hdl: decodeGenericDETAILVIEWSTYLE_HDL},
	"ACDBDETAILVIEWSTYLE":    {decode: decodeGenericDETAILVIEWSTYLE, hdl: decodeGenericDETAILVIEWSTYLE_HDL},
	"FIELDLIST":              {decode: decodeGenericFIELDLIST, handleVectorKey: "num_fields"},
	"SCALE":                  {decode: decodeGenericSCALE, forceStrings: true},
	"WIPEOUTVARIABLES":       {decode: decodeGenericWIPEOUTVARIABLES},
	"VISUALSTYLE":            {decode: decodeGenericVISUALSTYLE},
	"BLOCK_HEADER":           {decode: decodeGenericBLOCKHEADER, hdl: decodeGenericBLOCKHEADER_HDL},
	"LTYPE":                  {decode: decodeGenericLTYPE, extraHandles: 1},
	"MATERIAL":               {decode: decodeGenericMATERIAL},
	"DIMASSOC":               {decode: decodeGenericDIMASSOC, hdl: decodeGenericDIMASSOC_HDL},
	"ASSOCNETWORK":           {decode: decodeGenericASSOCNETWORK, hdl: decodeGenericASSOCNETWORK_HDL},
	"FIELD":                  {decode: decodeGenericFIELD, hdl: decodeGenericFIELD_HDL},
	"SUN":                    {decode: decodeGenericSUN},
	"SKYLIGHT_BACKGROUND":    {decode: decodeGenericSKYLIGHTBACKGROUND, extraHandles: 1},
	"MTEXTOBJECTCONTEXTDATA": {decode: decodeGenericMTEXTOBJECTCONTEXTDATA, extraHandles: 1},
	// R2000 类表的 DXF 名带 ACDB_ 前缀与 _CLASS 后缀（fzw 类表实证）
	"ACDB_MTEXTOBJECTCONTEXTDATA_CLASS": {decode: decodeGenericMTEXTOBJECTCONTEXTDATA, extraHandles: 1},
	"SPATIAL_FILTER":                    {decode: decodeGenericSPATIALFILTER},
	"ACSH_HISTORY_CLASS":                {decode: decodeGenericACSH_HISTORY_CLASS, extraHandles: 1},
	"TABLEGEOMETRY":                     {decode: decodeGenericTABLEGEOMETRY},
	// TABLECONTENT/DATATABLE 属 dwg2.spec 的 DEBUG_CLASSES 条件块：
	// LibreDWG 默认构建不解析（gold 输出 UNKNOWN_OBJ），暂不注册，
	// 待有真实样本基准验证后再启用（解码器与合成流测试已就绪）
	// "TABLECONTENT": {decode: decodeGenericTABLECONTENT, hdl: decodeGenericTABLECONTENT_HDL},
	// "DATATABLE":    {decode: decodeGenericDATATABLE},
	"ACDBPLACEHOLDER":                   {decode: decodeGenericPLACEHOLDER},
	"ACDBASSOCNETWORK":                  {decode: decodeGenericASSOCNETWORK, hdl: decodeGenericASSOCNETWORK_HDL},
	"ASSOCACTION":                       {decode: decodeGenericASSOCACTION, hdl: decodeGenericASSOCACTION_HDL},
	"ASSOCDEPENDENCY":                   {decode: decodeGenericASSOCDEPENDENCY, hdl: decodeGenericASSOCDEPENDENCY_HDL},
	"ASSOCGEOMDEPENDENCY":               {decode: decodeGenericASSOCGEOMDEPENDENCY, hdl: decodeGenericASSOCDEPENDENCY_HDL},
	"ACDBASSOCACTION":                   {decode: decodeGenericASSOCACTION, hdl: decodeGenericASSOCACTION_HDL},
	"ACDBASSOCDEPENDENCY":               {decode: decodeGenericASSOCDEPENDENCY, hdl: decodeGenericASSOCDEPENDENCY_HDL},
	"ACDBASSOCGEOMDEPENDENCY":           {decode: decodeGenericASSOCGEOMDEPENDENCY, hdl: decodeGenericASSOCDEPENDENCY_HDL},
	"EVALUATION_GRAPH":                  {decode: decodeGenericEVALUATION_GRAPH, hdl: decodeGenericEVALUATION_GRAPH_HDL},
	"ACAD_EVALUATION_GRAPH":             {decode: decodeGenericEVALUATION_GRAPH, hdl: decodeGenericEVALUATION_GRAPH_HDL},
	"CELLSTYLEMAP":                      {decode: decodeGenericCELLSTYLEMAP, hdl: decodeGenericCELLSTYLEMAP_HDL},
	"ASSOCOSNAPPOINTREFACTIONPARAM":     {decode: decodeGenericASSOCOSNAPPOINTREFACTIONPARAM, hdl: decodeGenericASSOCOSNAPPOINTREFACTIONPARAM_HDL},
	"ACDBASSOCOSNAPPOINTREFACTIONPARAM": {decode: decodeGenericASSOCOSNAPPOINTREFACTIONPARAM, hdl: decodeGenericASSOCOSNAPPOINTREFACTIONPARAM_HDL},
	"ASSOCVERTEXACTIONPARAM":            {decode: decodeGenericASSOCVERTEXACTIONPARAM},
	"ACDBASSOCVERTEXACTIONPARAM":        {decode: decodeGenericASSOCVERTEXACTIONPARAM},
	"SORTENTSTABLE":                     {decode: decodeGenericSORTENTSTABLE, hdl: decodeGenericSORTENTSTABLE_HDL},
	"IMAGEDEF":                          {decode: decodeGenericIMAGEDEF},
	"IMAGEDEF_REACTOR":                  {decode: decodeGenericIMAGEDEF_REACTOR},
	"RASTERVARIABLES":                   {decode: decodeGenericRASTERVARIABLES},
	"PDFDEFINITION":                     {decode: decodeGenericUNDERLAYDEFINITION},
	"DGNDEFINITION":                     {decode: decodeGenericUNDERLAYDEFINITION},
	"DWFDEFINITION":                     {decode: decodeGenericUNDERLAYDEFINITION},
	// 语料 0 实例类（dwg.spec 字段布局 + 合成位流单测自证，见 objects_longtail.go）
	"IDBUFFER":     {decode: decodeGenericIDBUFFER, handleVectorKey: "num_obj_ids"},
	"INDEX":        {decode: decodeGenericINDEX},
	"LAYER_INDEX":  {decode: decodeGenericLAYER_INDEX, hdl: decodeGenericLAYER_INDEX_HDL},
	"PROXY_OBJECT": {decode: decodeGenericPROXY_OBJECT, hdl: decodeGenericPROXY_OBJECT_HDL},
}

// init 合并 ACSH 形体系注册表（objects_acsh.go，公共前导 + primitive）。
func init() {
	for name, spec := range acshDecoders() {
		internalClassDecoders[name] = spec
	}
}

// verUntilR2004 版本是否为 R2004 及更早（版本枚举非时间序，禁止范围比较）。
func verUntilR2004(ver dwgVersion) bool {
	return ver == verR13 || ver == verR14 || ver == verR2000 || ver == verR2004
}

// gfRead 字段读取辅助：T 随版本与对象字符串流标志切换读取方式。
// R2007+ 且对象 has_strings=1 时，FIELD_T 从记录尾部的字符串区顺序取值
// （dat 流不占位，LibreDWG FIELD_T 宏行为）；否则 R2007 前 TV 内联。
type gfRead struct {
	r        *bitstream.BitStream
	ver      dwgVersion
	strs     []string // 字符串区预读的 TU 序列（R2007+ 字符串流对象）
	strIdx   int      // 下一个待取的字符串下标
	codepage uint16   // 文档码页（pre-R2007 的 TV 文本按此解码，fzw ANSI_936 实证）
}

// T 读文字字段。
func (f *gfRead) T(key string, g *objGeneric) error {
	if f.ver < verR2007 {
		s, err := f.r.ReadTV(f.codepage)
		if err != nil {
			return err
		}
		g.Fields = append(g.Fields, objField{key, s})
		return nil
	}
	// R2007+：从预读字符串流顺序取，dat 流不占位；流耗尽后返回空串且
	// 不占位（Dynblocks ASSOCVARIABLE 实证，>64 串对象的截断语义与
	// gold 一致）
	s := ""
	if f.strIdx < len(f.strs) {
		s = f.strs[f.strIdx]
		f.strIdx++
	}
	g.Fields = append(g.Fields, objField{key, s})
	return nil
}

// BS 读 BS 字段。
func (f gfRead) BS(key string, g *objGeneric) error {
	v, err := f.r.ReadBS()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, objField{key, int64(v)})
	return nil
}

// BD 读 BD 字段。
func (f gfRead) BD(key string, g *objGeneric) error {
	v, err := f.r.ReadBD()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, objField{key, v})
	return nil
}

// RC 读 RC 字段。
func (f gfRead) RC(key string, g *objGeneric) error {
	v, err := f.r.ReadRC()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, objField{key, int64(v)})
	return nil
}

// B 读位字段。
func (f gfRead) B(key string, g *objGeneric) error {
	v, err := f.r.ReadB()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, objField{key, v != 0})
	return nil
}

// RD2 读 2RD 坐标对（两个 RD），以 [x y] 数组记录（对齐 dwgread JSON 形状）。
func (f gfRead) RD2(key string, g *objGeneric) error {
	x, err := f.r.ReadRD()
	if err != nil {
		return err
	}
	y, err := f.r.ReadRD()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, objField{key, []float64{x, y}})
	return nil
}

// decodeGenericAPPID 解析 APPID（dwg.spec DWG_TABLE(APPID)）：
// dat 流 = COMMON_TABLE_FLAGS（name T + xref 标志）+ RC unknown(71)；
// handle 流 = owner + reactors + xdic + xref。
func decodeGenericAPPID(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {

	if err := fr.T("name", g); err != nil {
		return err
	}
	if err := readCommonTableFlags(r, fr, g, ver); err != nil {
		return err
	}
	return fr.RC("unknown", g)
}

// decodeGenericDICTIONARYVAR 解析 DICTIONARYVAR（dwg.spec）：
// dat 流 = RCd schema(280) + T strvalue。
func decodeGenericDICTIONARYVAR(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {

	if err := fr.RC("schema", g); err != nil {
		return err
	}
	return fr.T("strvalue", g)
}

// decodeGenericSCALE 解析 SCALE（dwg2.spec DWG_OBJECT(SCALE)）：
// dat 流 = BS flag(70) + T name(300) + BD paper_units(140) +
// BD drawing_units(141) + B is_unit_scale(290)。
func decodeGenericSCALE(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {

	if err := fr.BS("flag", g); err != nil {
		return err
	}
	if err := fr.T("name", g); err != nil {
		return err
	}
	if err := fr.BD("paper_units", g); err != nil {
		return err
	}
	if err := fr.BD("drawing_units", g); err != nil {
		return err
	}
	return fr.B("is_unit_scale", g)
}

// decodeInternalObject 按类型码/类名查找并解码通用内部对象：
// dat 流专有字段 → handle 流（owner + reactors + xdic + 附加引用）。
func decodeInternalObject(r *bitstream.BitStream, rec *objectRecord, ver dwgVersion, r2013Plus bool, typeCode uint16, className string, codepage uint16) (*objGeneric, error) {
	// UNDERLAY 引用实体：实体布局但经对象分发（此前 UNKNOWN_OBJ 兜底），
	// 按实体头 + UNDERLAY_fields 解码（见 objects_underlay.go）
	if className == "PDFUNDERLAY" || className == "DWFUNDERLAY" || className == "DGNUNDERLAY" {
		return decodeUnderlayEntity(r, rec, ver, typeCode, className)
	}
	// UNKNOWN_OBJ：无法识别的类，记录元数据与未知位区（LibreDWG 同样
	// 仅存储 unknown_bits，不解析字段）
	if className == "UNKNOWN_OBJ" || className == "ACDBASSOCPERSSUBENTMANAGER" {
		className = "UNKNOWN_OBJ"
		unkStart := r.TellBits()
		ug := &objGeneric{Name: className}
		bitsizePosU := dictBitsizePos(ver)
		if bitsizePosU == bitsizePosHead {
			ug.ObjSizeBit, _ = readInlineBitsize(r)
		}
		// 未知类的流结构不可预知：全程宽容读取，失败即返回已得元数据
		ug.Handle, _ = readHandleValue(r)
		ug.Fields = append(ug.Fields,
			objField{"object", ug.Name},
			objField{"type", int64(typeCode)},
			objField{"size", int64(rec.size)},
			objField{"has_ds_data", false},
		)
		_ = skipEEDChain(r)
		if bitsizePosU == bitsizePosTail {
			ug.ObjSizeBit, _ = readInlineBitsize(r)
		}
		if nr, uerr := r.ReadBL(); uerr == nil && nr <= 4096 {
			ug.NumReactors = int(nr)
		}
		if ver >= verR2004 {
			if xd, e := r.ReadB(); e == nil {
				ug.XdicMissing = xd == 1
			}
		}
		if r2013Plus {
			if _, e := r.ReadB(); e != nil {
				return ug, nil
			}
		}
		if bitsizePosU == bitsizePosDerived {
			ug.ObjSizeBit = rec.dataEndBit() - rec.bodyBitOffset - uint64(rec.handleSizeFieldBits)
			r.SetBitPos(rec.dataEndBit())
		} else {
			r.SetBitPos(rec.bodyBitOffset + ug.ObjSizeBit)
		}
		ug.Fields = append(ug.Fields, objField{"is_xdic_missing", ug.XdicMissing})
		ug.Fields = append(ug.Fields, objField{"bitsize", int64(ug.ObjSizeBit)})
		ug.Owner, _ = readOwnerHandle(r, ug.Handle)
		for i := 0; i < ug.NumReactors; i++ {
			if _, e := readHandleReference(r, ug.Handle); e != nil {
				break
			}
		}
		if !ug.XdicMissing {
			readHandleReference(r, ug.Handle)
		}
		// 回放收集：data 段与 handle 流的原始位串（同通用路径模式）
		unkEnd := r.TellBits()
		ug.headRawBits = bitstream.CollectBits(r, unkStart, unkEnd)
		ug.RawHandleBits = bitstream.CollectBits(r, unkEnd, uint64(len(r.Src))*8)
		ug.hdOffsetBits = unkStart - rec.bodyBitOffset
		return ug, nil
	}
	// DICTIONARYWDFLT：DICTIONARY 布局 + hdl 尾 defaultid
	if typeCode == 0x2B || className == "ACDBDICTIONARYWDFLT" || className == "DICTIONARYWDFLT" {
		startPos := r.TellBits()
		dd, e := decodeDictionaryObjectFull(r, rec, ver, r2013Plus, true)
		if e != nil {
			return nil, e
		}
		// 位串收集：data 段 + handle 流 + 元数据（与通用路径同构）
		hdOff := startPos - rec.bodyBitOffset
		headRaw := bitstream.CollectBits(r, startPos+32, rec.dataEndBit())
		flds := []objField{
			{"object", "DICTIONARYWDFLT"},
			{"type", int64(typeCode)},
			{"size", int64(rec.size)},
			{"bitsize", int64(dd.objSizeBit)},
			{"num_reactors", int64(dd.numReactors)},
			{"is_xdic_missing", dd.xdicMissing},
		}
		if r2013Plus {
			flds = append(flds, objField{"has_ds_data", false})
		}
		flds = append(flds,
			objField{"dxfname", "ACDBDICTIONARYWDFLT"},
			objField{"numitems", int64(dd.numItems)},
			objField{"cloning", int64(dd.cloning)},
			objField{"is_hardowner", dd.isHardOwner},
		)
		flds = append(flds, dd.EedFields...)
		return &objGeneric{
			Name: "DICTIONARYWDFLT", Handle: dd.handle, Owner: dd.owner,
			ObjSizeBit: dd.objSizeBit, NumReactors: dd.numReactors,
			XdicMissing:  dd.xdicMissing,
			Fields:       flds,
			Handles:      dd.itemHandles,
			headRawBits:  headRaw,
			hdOffsetBits: hdOff,
		}, nil
	}
	spec, ok := internalFixedDecoders[typeCode]
	if !ok && className != "" {
		spec, ok = internalClassDecoders[className]
	}
	if !ok {
		if className == "" {
			// 类名未解析且非已知固定码：不处理
			return nil, fmt.Errorf("cad: 无 %X 内部对象解码器", typeCode)
		}
		// 兜底：类类型对象且类名已解析但无专门解码器 → 按 UNKNOWN_OBJ
		// 宽容处理（LibreDWG 同样以 UNKNOWN_OBJ 兜底，仅记录元数据）；
		// 保留原始类名供统计与后续补齐解码器
		origClass := className
		className = "UNKNOWN_OBJ"
		spec = internalObjectSpec{decode: func(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, gg *objGeneric) error {
			gg.Unknown = true
			gg.OrigClass = origClass
			return nil
		}}
		ok = true
	}
	g := &objGeneric{Name: className}
	if g.Name == "" {
		if n, ok := objTypeCode[typeCode]; ok {
			g.Name = n
		}
	}
	bitsizePos := dictBitsizePos(ver)
	// dat 段前导位：RL bitsize 字段起点（body 内），重编码位长校验用
	g.hdOffsetBits = r.TellBits() - rec.bodyBitOffset
	if bitsizePos == bitsizePosHead {
		bs, e := readInlineBitsize(r)
		if e != nil {
			return nil, e
		}
		g.ObjSizeBit = bs
	}
	var err error
	_dbg := os.Getenv("CAD_DECODE_DBG") != ""
	if _dbg {
		fmt.Fprintf(os.Stderr, "[hdr] start@%d type=%X\n", r.TellBits(), typeCode)
	}
	if g.Handle, err = readHandleValue(r); err != nil {
		return nil, fmt.Errorf("hdlv@%d: %w", r.TellBits(), err)
	}
	if _dbg {
		fmt.Fprintf(os.Stderr, "[hdr] handle done @%d\n", r.TellBits())
	}
	if err = parseEEDChain(r, ver, &g.Fields); err != nil {
		return nil, fmt.Errorf("eed@%d: %w", r.TellBits(), err)
	}
	if _dbg {
		fmt.Fprintf(os.Stderr, "[hdr] eed done @%d\n", r.TellBits())
	}
	if bitsizePos == bitsizePosTail {
		if g.ObjSizeBit, err = readInlineBitsize(r); err != nil {
			return nil, err
		}
	}
	var numReactors uint32
	if numReactors, err = r.ReadBL(); err != nil {
		return nil, fmt.Errorf("nr@%d: %w", r.TellBits(), err)
	}
	if _dbg {
		fmt.Fprintf(os.Stderr, "[hdr] reactors done @%d (n=%d)\n", r.TellBits(), numReactors)
	}
	if numReactors > 4096 {
		return nil, fmt.Errorf("cad: 内部对象 reactors 异常 %d", numReactors)
	}
	g.NumReactors = int(numReactors)
	if ver >= verR2004 {
		if xdic, e := r.ReadB(); e != nil {
			return nil, e
		} else {
			g.XdicMissing = xdic == 1
		}
	}
	if r2013Plus {
		if ds, e := r.ReadB(); e != nil { // has_ds_data
			return nil, e
		} else {
			g.HasDsData = ds == 1
		}
	}
	// R2007+ 字符串流对象：bitsize-1 处 has_strings 位为 1 时，
	// FIELD_T 从记录尾部字符串区顺序取值（dat 流不占位），读前保存
	// 专有字段起点、读后恢复。
	// 注意基准：LibreDWG 的 bitsize 相对 UMC（handle-stream-size）字段之后，
	// 故位串检测与字符串区定位需加 rec.handleSizeFieldBits。
	g.controlType = typeCode
	fr := &gfRead{r: r, ver: ver, codepage: codepage}
	if ver >= verR2007 {
		bitsize := g.ObjSizeBit
		if bitsize == 0 { // R2010+：由记录头推导
			bitsize = rec.dataEndBit() - rec.bodyBitOffset - uint64(rec.handleSizeFieldBits)
			g.ObjSizeBit = bitsize
		}
		libreBase := uint64(rec.handleSizeFieldBits) + rec.bodyBitOffset
		savedBits := r.TellBits()
		r.SetBitPos(libreBase + bitsize - 1)
		b, e := r.ReadB()
		if (e == nil && b == 1) || spec.forceStrings {
			fr.strs = readStringAreaBitRange(r, libreBase+bitsize, 64, ver >= verR2013)
		}
		r.SetBitPos(savedBits)
	}
	if err = spec.decode(r, ver, fr, g); err != nil {
		return nil, fmt.Errorf("decode@%d: %w", r.TellBits(), err)
	}
	// 专有字段段原始位串：RL bitsize 之后至 handle 流起点（bitsize）。
	// 按 objSizeBit 定界（而非当前位）：解码器内部可能跳过预览位串等
	// 非连续段，原样回放须包含全部位。
	// R2010+（bitsizePosDerived）无内联 RL：起点为 dataStartBit，
	// 终点为记录数据结束位（dataEndBit），并保存重建 rec 元数据。
	if bitsizePos == bitsizePosHead {
		if g.ObjSizeBit > g.hdOffsetBits+32 {
			g.headRawBits = bitstream.CollectBits(r, rec.bodyBitOffset+g.hdOffsetBits+32, rec.bodyBitOffset+g.ObjSizeBit)
		}
	} else {
		g.r2010Plus = true
		g.sizeBytes = rec.size
		g.hSizeField = rec.handleSizeFieldBits
		g.hssBits = rec.handleStreamSizeBits
		g.bodyBitOff = rec.bodyBitOffset
		g.preBits = bitstream.CollectBits(r, rec.bodyBitOffset, rec.bodyBitOffset+g.hdOffsetBits)
		// 注意：终点 dataEndBit 与 RawHandleBits 起点（bitsize）存在
		// hSizeField 位重叠——R2010+ 首轮 handle 定位（setBitPos
		// dataEndBit）能通过 gold 的机理未明，修正需连同首轮定位
		// 一起统一，见任务文档 R2010+ 卡点记录
		g.headRawBits = bitstream.CollectBits(r, rec.bodyBitOffset+g.hdOffsetBits, rec.dataEndBit())
	}
	// handle 流
	switch bitsizePos {
	case bitsizePosDerived:
		g.ObjSizeBit = rec.dataEndBit() - rec.bodyBitOffset - uint64(rec.handleSizeFieldBits)
		r.SetBitPos(rec.dataEndBit())
	default:
		r.SetBitPos(rec.bodyBitOffset + g.ObjSizeBit)
	}
	// handle 流原始位串：起点（bitsize）至 body 尾，供重编码原样写回
	g.RawHandleBits = bitstream.CollectBits(r, r.TellBits(), uint64(len(r.Src))*8)
	if g.Owner, err = readOwnerHandle(r, g.Handle); err != nil {
		return nil, fmt.Errorf("hdl.owner@%d: %w", r.TellBits(), err)
	}
	for i := 0; i < g.NumReactors; i++ {
		if _, err = readHandleReference(r, g.Handle); err != nil {
			return nil, fmt.Errorf("hdl.reactor%d@%d: %w", i, r.TellBits(), err)
		}
	}
	if !g.XdicMissing {
		if _, err = readHandleReference(r, g.Handle); err != nil {
			return nil, fmt.Errorf("hdl.xdic@%d: %w", r.TellBits(), err)
		}
	}
	// 公共元数据字段（对照 dwgread JSON 的公共键，供导出与值级审计）
	objName := g.Name
	if spec.jsonName != "" {
		objName = spec.jsonName
	}
	g.Fields = append(g.Fields,
		objField{"object", objName},
		objField{"type", int64(typeCode)},
		objField{"size", int64(rec.size)},
		objField{"bitsize", int64(g.ObjSizeBit)},
		objField{"num_reactors", int64(g.NumReactors)},
		objField{"is_xdic_missing", g.XdicMissing},
		objField{"has_ds_data", g.HasDsData},
		objField{"dxfname", g.Name},
	)
	if spec.hdl != nil {
		return g, spec.hdl(r, ver, fr, g)
	}
	n := spec.extraHandles
	vecN := 0
	if spec.handleVectorKey != "" {
		if v, ok := g.Field(spec.handleVectorKey).(int64); ok {
			vecN = int(v)
		}
	}
	n += vecN
	for i := 0; i < n; i++ {
		h, e := readHandleReference(r, g.Handle)
		if e != nil {
			return nil, fmt.Errorf("hdl.vec%d/%d@%d: %w", i, n, r.TellBits(), e)
		}
		g.Handles = append(g.Handles, h)
	}
	return g, nil
}

// readStringAreaBitRange 在指定位流上按 R2013+ 字符串区公式顺序读取至多
// max 个 TU 字符串：RS dataSize @ bitsize-17（0x8000 高位为扩展格式标志），
// 区起点 = bitsize-17-dataSize，内容为顺序 TU。bitsize 为相对该读取器
// 0 点（MS 字段之后）的位长。
func readStringAreaBitRange(r *bitstream.BitStream, bitsize uint64, max int, r2013Plus bool) []string {
	if bitsize < 34 {
		return nil
	}
	pos := bitsize - 17
	r.SetBitPos(pos)
	dataSize, err := r.ReadRS()
	if err != nil {
		return nil
	}
	if dataSize&0x8000 != 0 {
		// 扩展格式：hi_size RS 位于 bitsize-33，真实大小 =
		// (data_size&0x7FFF) | (hi_size<<15)（对齐 obj_string_stream）
		r.SetBitPos(bitsize - 33)
		hi, e := r.ReadRS()
		if e != nil {
			return nil
		}
		dataSize = (dataSize & 0x7FFF) | (hi << 15)
	}
	if dataSize == 0 || int(dataSize) > 1<<20 {
		return nil
	}
	start := uint64(int64(bitsize) - 17 - int64(dataSize))
	if start >= bitsize {
		return nil
	}
	r.SetBitPos(start)
	out := make([]string, 0, max)
	for i := 0; i < max; i++ {
		s, e := r.ReadTU()
		if e != nil {
			break
		}
		out = append(out, s)
	}
	return out
}

// BL 读 BL 字段。
func (f gfRead) BL(key string, g *objGeneric) error {
	v, err := f.r.ReadBL()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, objField{key, int64(v)})
	return nil
}

// BLd 读有符号 BL 字段（LibreDWG BLd：位流同 BL，输出按 int32 解释），
// 如 EVALUATION_GRAPH out_edge 的 -1 表无、ASSOCDEPENDENCY order 的负序值。
func (f gfRead) BLd(key string, g *objGeneric) error {
	v, err := f.r.ReadBL()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, objField{key, int64(int32(v))})
	return nil
}

// decodeGenericGROUP 解析 GROUP（dwg.spec DWG_OBJECT(GROUP)）：
// dat 流 = T name(300) + BS unnamed(70) + BS selectable(71) + BL num_groups；
// handle 流 = owner + reactors + xdic + groups×num_groups。
func decodeGenericGROUP(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := fr.T("name", g); err != nil {
		return err
	}
	if err := fr.BS("unnamed", g); err != nil {
		return err
	}
	if err := fr.BS("selectable", g); err != nil {
		return err
	}
	return fr.BL("num_groups", g)
}

// decodeGenericWIPEOUTVARIABLES 解析 WIPEOUTVARIABLES（dwg2.spec）：
// dat 流 = BS display_frame(70)。
func decodeGenericWIPEOUTVARIABLES(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	return fr.BS("display_frame", g)
}

// decodeGenericSORTENTSTABLE 解析 SORTENTSTABLE（dwg2.spec
// DWG_OBJECT(SORTENTSTABLE)）：dat 流 = BL num_ents(0) +
// sort_ents×num_ents —— 该句柄数组特殊地内联在 dat 流（spec 以
// str_dat=hdl_dat; hdl_dat=dat 显式切换，code 0 绝对引用），
// 先于公共 handle 流存储。
func decodeGenericSORTENTSTABLE(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	num, err := r.ReadBL()
	if err != nil {
		return err
	}
	// spec VALUEOUTOFBOUNDS(num_ents, 50000)：越界视为流错位，拒绝解码
	if num > 50000 {
		return fmt.Errorf("cad: SORTENTSTABLE num_ents 越界 %d", num)
	}
	g.Fields = append(g.Fields, objField{"num_ents", int64(num)})
	for i := 0; i < int(num); i++ {
		h, e := readHandleReference(r, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
		g.Fields = append(g.Fields, objField{fmt.Sprintf("sort_ents[%d]", i), int64(h)})
	}
	return nil
}

// decodeGenericSORTENTSTABLE_HDL SORTENTSTABLE 的 handle 流附加引用：
// block_owner（排序所属的 mspace/pspace BLOCK_HEADER，soft owner）+
// ents×num_ents（排序前顺序的实体引用，与 sort_ents 按下标配对）。
func decodeGenericSORTENTSTABLE_HDL(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	h, e := readHandleReference(r, g.Handle)
	if e != nil {
		return e
	}
	g.Handles = append(g.Handles, h)
	g.Fields = append(g.Fields, objField{"block_owner", int64(h)})
	num, _ := g.Field("num_ents").(int64)
	for i := 0; i < int(num); i++ {
		h, e := readHandleReference(r, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
		g.Fields = append(g.Fields, objField{fmt.Sprintf("ents[%d]", i), int64(h)})
	}
	return nil
}

// decodeGenericIMAGEDEF 解析 IMAGEDEF（dwg.spec DWG_OBJECT(IMAGEDEF)，
// AcDbRasterImageDef）：dat 流 = BL class_version(90) + 2RD image_size +
// T file_path + B is_loaded + RC resunits + 2RD pixel_size。位级字段
// 顺序与 DXF 顺序不同（DXF 端 file_path 首位）；R2007+ 的 file_path
// 走对象字符串流（gfRead.T）。
func decodeGenericIMAGEDEF(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := fr.BL("class_version", g); err != nil {
		return err
	}
	// spec VALUEOUTOFBOUNDS：class_version>10 视为流错位
	if v, _ := g.Field("class_version").(int64); v > 10 {
		return fmt.Errorf("cad: IMAGEDEF class_version 越界 %d", v)
	}
	if err := fr.RD2("image_size", g); err != nil {
		return err
	}
	if err := fr.T("file_path", g); err != nil {
		return err
	}
	if err := fr.B("is_loaded", g); err != nil {
		return err
	}
	if err := fr.RC("resunits", g); err != nil {
		return err
	}
	return fr.RD2("pixel_size", g)
}

// decodeGenericIMAGEDEF_REACTOR 解析 IMAGEDEF_REACTOR（dwg.spec
// DWG_OBJECT(IMAGEDEF_REACTOR)，AcDbRasterImageDefReactor）：dat 流仅
// BL class_version(90)，其余为公共 handle 流（框架统一处理）。
func decodeGenericIMAGEDEF_REACTOR(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := fr.BL("class_version", g); err != nil {
		return err
	}
	// spec VALUEOUTOFBOUNDS：class_version>10 视为流错位
	if v, _ := g.Field("class_version").(int64); v > 10 {
		return fmt.Errorf("cad: IMAGEDEF_REACTOR class_version 越界 %d", v)
	}
	return nil
}

// decodeGenericRASTERVARIABLES 解析 RASTERVARIABLES（dwg2.spec
// AcDbRasterVariables）：BL class_version（>10 越界放弃）+ BS
// image_frame + BS image_quality + BS units。
func decodeGenericRASTERVARIABLES(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	cv, err := fr.BLv("class_version", g)
	if err != nil {
		return err
	}
	if cv > 10 { // spec VALUEOUTOFBOUNDS：越界放弃后续字段
		return nil
	}
	if err := fr.BS("image_frame", g); err != nil {
		return err
	}
	if err := fr.BS("image_quality", g); err != nil {
		return err
	}
	return fr.BS("units", g)
}

// decodeGenericUNDERLAYDEFINITION 解析 UNDERLAY 定义对象（dwg2.spec
// PDFDEFINITION/DGNDEFINITION/DWFDEFINITION，AcDbUnderlayDefinition）：
// dat 流 = T filename(1) + T name(2)；三类同构共用，R2007+ 走字符串流。
func decodeGenericUNDERLAYDEFINITION(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := fr.T("filename", g); err != nil {
		return err
	}
	return fr.T("name", g)
}

// ---- CMC 颜色原语（对照 LibreDWG bit_read_CMC）----

// readCMC 读颜色字段：R2004+ 为 BS index + BL rgb + RC flag
// （flag&1 → name T、flag&2 → book_name T，从字符串流），并校验
// method（rgb 高字节）∈[0xC0,0xC8]；R2004 前仅 BS index。
// 返回与 dwgread JSON 同形的 map（{"index":..,"rgb":"..","name":..}）。
func (f *gfRead) CMC(key string, g *objGeneric) error {
	if f.ver < verR2004 {
		idx, err := f.r.ReadBS()
		if err != nil {
			return err
		}
		// pre-R2004 的 CMC 仅 index（dwgread JSON 输出为纯数值）
		g.Fields = append(g.Fields, objField{key, int64(idx)})
		return nil
	}
	return f.readCMCR2004(key, g)
}

// CMTC 强制按 R2004+ 的 CMC 读法（LibreDWG FIELD_CMTC 语义：
// TABLESTYLE 等样式类即使出现在早期版本也按 true color 结构）。
func (f *gfRead) CMTC(key string, g *objGeneric) error {
	return f.readCMCR2004(key, g)
}

// readCMCR2004 R2004+ 的 CMC 结构：BS index + BL rgb + RC flag
// （flag&1 → name T、flag&2 → book_name，从字符串流）。
func (f *gfRead) readCMCR2004(key string, g *objGeneric) error {
	dbgC := os.Getenv("CAD_CMTC_DBG") != "" && strings.Contains(key, "borders[0].color")
	startPos := f.r.TellBits()
	if dbgC {
		fmt.Fprintf(os.Stderr, "[cmtcPRE] h=%d key=%s pos=%d-40..%s\n", g.Handle, key, startPos, bitstream.CollectBits(f.r, startPos-40, startPos+100))
	}
	idx, err := f.r.ReadBS()
	if err != nil {
		return err
	}
	rgb, err := f.r.ReadBL()
	if err != nil {
		return err
	}
	method := rgb >> 24
	flag, err := f.r.ReadRC()
	if err != nil {
		return err
	}
	if dbgC {
		fmt.Fprintf(os.Stderr, "[cmtc2] h=%d key=%s start=%d end=%d idx=%d rgb=%08x flag=%d bits=%s\n",
			g.Handle, key, startPos, f.r.TellBits(), idx, rgb, flag, bitstream.CollectBits(f.r, startPos, startPos+60))
	}
	cm := map[string]any{"index": int64(idx), "rgb": fmt.Sprintf("%08x", rgb)}
	if flag < 4 {
		if flag&1 != 0 {
			name := ""
			if f.strIdx < len(f.strs) {
				name = f.strs[f.strIdx]
				f.strIdx++
			}
			cm["name"] = name
		}
		if flag&2 != 0 {
			bn := ""
			if f.strIdx < len(f.strs) {
				bn = f.strs[f.strIdx]
				f.strIdx++
			}
			cm["book_name"] = bn
		}
	}
	if method < 0xc0 || method > 0xc8 {
		cm["rgb"] = fmt.Sprintf("%08x", 0xc2000000|rgb&0xffffff)
	}
	// 对照 LibreDWG bit_read_CMC：按 palette 反查修正 index
	// （如 0xc0000000 → 256 ByLayer）
	cm["index"] = dwgFindColorIndex(rgb)
	g.Fields = append(g.Fields, objField{key, cm})
	return nil
}

// dwgRGBPalette ACAD 标准 256 色 RGB 表（对照 LibreDWG rgb_palette）。
var dwgRGBPalette = [256]struct{ r, g, b uint8 }{
	{0x00, 0x00, 0x00}, {0xFF, 0x00, 0x00}, {0xFF, 0xFF, 0x00}, {0x00, 0xFF, 0x00},
	{0x00, 0xFF, 0xFF}, {0x00, 0x00, 0xFF}, {0xFF, 0x00, 0xFF}, {0xFF, 0xFF, 0xFF},
	{0x41, 0x41, 0x41}, {0x80, 0x80, 0x80}, {0xFF, 0x00, 0x00}, {0xFF, 0xAA, 0xAA},
	{0xBD, 0x00, 0x00}, {0xBD, 0x7E, 0x7E}, {0x81, 0x00, 0x00}, {0x81, 0x56, 0x56},
	{0x68, 0x00, 0x00}, {0x68, 0x45, 0x45}, {0x4F, 0x00, 0x00}, {0x4F, 0x35, 0x35},
	{0xFF, 0x3F, 0x00}, {0xFF, 0xBF, 0xAA}, {0xBD, 0x2E, 0x00}, {0xBD, 0x8D, 0x7E},
	{0x81, 0x1F, 0x00}, {0x81, 0x60, 0x56}, {0x68, 0x19, 0x00}, {0x68, 0x4E, 0x45},
	{0x4F, 0x13, 0x00}, {0x4F, 0x3B, 0x35}, {0xFF, 0x7F, 0x00}, {0xFF, 0xD4, 0xAA},
	{0xBD, 0x5E, 0x00}, {0xBD, 0x9D, 0x7E}, {0x81, 0x40, 0x00}, {0x81, 0x6B, 0x56},
	{0x68, 0x34, 0x00}, {0x68, 0x56, 0x45}, {0x4F, 0x27, 0x00}, {0x4F, 0x42, 0x35},
	{0xFF, 0xBF, 0x00}, {0xFF, 0xEA, 0xAA}, {0xBD, 0x8D, 0x00}, {0xBD, 0xAD, 0x7E},
	{0x81, 0x60, 0x00}, {0x81, 0x76, 0x56}, {0x68, 0x4E, 0x00}, {0x68, 0x5F, 0x45},
	{0x4F, 0x3B, 0x00}, {0x4F, 0x49, 0x35}, {0xFF, 0xFF, 0x00}, {0xFF, 0xFF, 0xAA},
	{0xBD, 0xBD, 0x00}, {0xBD, 0xBD, 0x7E}, {0x81, 0x81, 0x00}, {0x81, 0x81, 0x56},
	{0x68, 0x68, 0x00}, {0x68, 0x68, 0x45}, {0x4F, 0x4F, 0x00}, {0x4F, 0x4F, 0x35},
	{0xBF, 0xFF, 0x00}, {0xEA, 0xFF, 0xAA}, {0x8D, 0xBD, 0x00}, {0xAD, 0xBD, 0x7E},
	{0x60, 0x81, 0x00}, {0x76, 0x81, 0x56}, {0x4E, 0x68, 0x00}, {0x5F, 0x68, 0x45},
	{0x3B, 0x4F, 0x00}, {0x49, 0x4F, 0x35}, {0x7F, 0xFF, 0x00}, {0xD4, 0xFF, 0xAA},
	{0x5E, 0xBD, 0x00}, {0x9D, 0xBD, 0x7E}, {0x40, 0x81, 0x00}, {0x6B, 0x81, 0x56},
	{0x34, 0x68, 0x00}, {0x56, 0x68, 0x45}, {0x27, 0x4F, 0x00}, {0x42, 0x4F, 0x35},
	{0x3F, 0xFF, 0x00}, {0xBF, 0xFF, 0xAA}, {0x2E, 0xBD, 0x00}, {0x8D, 0xBD, 0x7E},
	{0x1F, 0x81, 0x00}, {0x60, 0x81, 0x56}, {0x19, 0x68, 0x00}, {0x4E, 0x68, 0x45},
	{0x13, 0x4F, 0x00}, {0x3B, 0x4F, 0x35}, {0x00, 0xFF, 0x00}, {0xAA, 0xFF, 0xAA},
	{0x00, 0xBD, 0x00}, {0x7E, 0xBD, 0x7E}, {0x00, 0x81, 0x00}, {0x56, 0x81, 0x56},
	{0x00, 0x68, 0x00}, {0x45, 0x68, 0x45}, {0x00, 0x4F, 0x00}, {0x35, 0x4F, 0x35},
	{0x00, 0xFF, 0x3F}, {0xAA, 0xFF, 0xBF}, {0x00, 0xBD, 0x2E}, {0x7E, 0xBD, 0x8D},
	{0x00, 0x81, 0x1F}, {0x56, 0x81, 0x60}, {0x00, 0x68, 0x19}, {0x45, 0x68, 0x4E},
	{0x00, 0x4F, 0x13}, {0x35, 0x4F, 0x3B}, {0x00, 0xFF, 0x7F}, {0xAA, 0xFF, 0xD4},
	{0x00, 0xBD, 0x5E}, {0x7E, 0xBD, 0x9D}, {0x00, 0x81, 0x40}, {0x56, 0x81, 0x6B},
	{0x00, 0x68, 0x34}, {0x45, 0x68, 0x56}, {0x00, 0x4F, 0x27}, {0x35, 0x4F, 0x42},
	{0x00, 0xFF, 0xBF}, {0xAA, 0xFF, 0xEA}, {0x00, 0xBD, 0x8D}, {0x7E, 0xBD, 0xAD},
	{0x00, 0x81, 0x60}, {0x56, 0x81, 0x76}, {0x00, 0x68, 0x4E}, {0x45, 0x68, 0x5F},
	{0x00, 0x4F, 0x3B}, {0x35, 0x4F, 0x49}, {0x00, 0xFF, 0xFF}, {0xAA, 0xFF, 0xFF},
	{0x00, 0xBD, 0xBD}, {0x7E, 0xBD, 0xBD}, {0x00, 0x81, 0x81}, {0x56, 0x81, 0x81},
	{0x00, 0x68, 0x68}, {0x45, 0x68, 0x68}, {0x00, 0x4F, 0x4F}, {0x35, 0x4F, 0x4F},
	{0x00, 0xBF, 0xFF}, {0xAA, 0xEA, 0xFF}, {0x00, 0x8D, 0xBD}, {0x7E, 0xAD, 0xBD},
	{0x00, 0x60, 0x81}, {0x56, 0x76, 0x81}, {0x00, 0x4E, 0x68}, {0x45, 0x5F, 0x68},
	{0x00, 0x3B, 0x4F}, {0x35, 0x49, 0x4F}, {0x00, 0x7F, 0xFF}, {0xAA, 0xD4, 0xFF},
	{0x00, 0x5E, 0xBD}, {0x7E, 0x9D, 0xBD}, {0x00, 0x40, 0x81}, {0x56, 0x6B, 0x81},
	{0x00, 0x34, 0x68}, {0x45, 0x56, 0x68}, {0x00, 0x27, 0x4F}, {0x35, 0x42, 0x4F},
	{0x00, 0x3F, 0xFF}, {0xAA, 0xBF, 0xFF}, {0x00, 0x2E, 0xBD}, {0x7E, 0x8D, 0xBD},
	{0x00, 0x1F, 0x81}, {0x56, 0x60, 0x81}, {0x00, 0x19, 0x68}, {0x45, 0x4E, 0x68},
	{0x00, 0x13, 0x4F}, {0x35, 0x3B, 0x4F}, {0x00, 0x00, 0xFF}, {0xAA, 0xAA, 0xFF},
	{0x00, 0x00, 0xBD}, {0x7E, 0x7E, 0xBD}, {0x00, 0x00, 0x81}, {0x56, 0x56, 0x81},
	{0x00, 0x00, 0x68}, {0x45, 0x45, 0x68}, {0x00, 0x00, 0x4F}, {0x35, 0x35, 0x4F},
	{0x3F, 0x00, 0xFF}, {0xBF, 0xAA, 0xFF}, {0x2E, 0x00, 0xBD}, {0x8D, 0x7E, 0xBD},
	{0x1F, 0x00, 0x81}, {0x60, 0x56, 0x81}, {0x19, 0x00, 0x68}, {0x4E, 0x45, 0x68},
	{0x13, 0x00, 0x4F}, {0x3B, 0x35, 0x4F}, {0x7F, 0x00, 0xFF}, {0xD4, 0xAA, 0xFF},
	{0x5E, 0x00, 0xBD}, {0x9D, 0x7E, 0xBD}, {0x40, 0x00, 0x81}, {0x6B, 0x56, 0x81},
	{0x34, 0x00, 0x68}, {0x56, 0x45, 0x68}, {0x27, 0x00, 0x4F}, {0x42, 0x35, 0x4F},
	{0xBF, 0x00, 0xFF}, {0xEA, 0xAA, 0xFF}, {0x8D, 0x00, 0xBD}, {0xAD, 0x7E, 0xBD},
	{0x60, 0x00, 0x81}, {0x76, 0x56, 0x81}, {0x4E, 0x00, 0x68}, {0x5F, 0x45, 0x68},
	{0x3B, 0x00, 0x4F}, {0x49, 0x35, 0x4F}, {0xFF, 0x00, 0xFF}, {0xFF, 0xAA, 0xFF},
	{0xBD, 0x00, 0xBD}, {0xBD, 0x7E, 0xBD}, {0x81, 0x00, 0x81}, {0x81, 0x56, 0x81},
	{0x68, 0x00, 0x68}, {0x68, 0x45, 0x68}, {0x4F, 0x00, 0x4F}, {0x4F, 0x35, 0x4F},
	{0xFF, 0x00, 0xBF}, {0xFF, 0xAA, 0xEA}, {0xBD, 0x00, 0x8D}, {0xBD, 0x7E, 0xAD},
	{0x81, 0x00, 0x60}, {0x81, 0x56, 0x76}, {0x68, 0x00, 0x4E}, {0x68, 0x45, 0x5F},
	{0x4F, 0x00, 0x3B}, {0x4F, 0x35, 0x49}, {0xFF, 0x00, 0x7F}, {0xFF, 0xAA, 0xD4},
	{0xBD, 0x00, 0x5E}, {0xBD, 0x7E, 0x9D}, {0x81, 0x00, 0x40}, {0x81, 0x56, 0x6B},
	{0x68, 0x00, 0x34}, {0x68, 0x45, 0x56}, {0x4F, 0x00, 0x27}, {0x4F, 0x35, 0x42},
	{0xFF, 0x00, 0x3F}, {0xFF, 0xAA, 0xBF}, {0xBD, 0x00, 0x2E}, {0xBD, 0x7E, 0x8D},
	{0x81, 0x00, 0x1F}, {0x81, 0x56, 0x60}, {0x68, 0x00, 0x19}, {0x68, 0x45, 0x4E},
	{0x4F, 0x00, 0x13}, {0x4F, 0x35, 0x3B}, {0x33, 0x33, 0x33}, {0x50, 0x50, 0x50},
	{0x69, 0x69, 0x69}, {0x82, 0x82, 0x82}, {0xBE, 0xBE, 0xBE}, {0xFF, 0xFF, 0xFF},
}

// dwgFindColorIndex 按 ACAD 256 色表反查 rgb 对应的索引
// （对照 LibreDWG dwg_find_color_index，未命中返回 256）。
func dwgFindColorIndex(rgb uint32) int64 {
	r := uint8((rgb >> 16) & 0xFF)
	g := uint8((rgb >> 8) & 0xFF)
	b := uint8(rgb & 0xFF)
	for i := 0; i < 256; i++ {
		if dwgRGBPalette[i].r == r && dwgRGBPalette[i].g == g && dwgRGBPalette[i].b == b {
			return int64(i)
		}
	}
	return 256
}

// Bv 读位字段并返回数值（条件字段如 has_name 用）。
func (f *gfRead) Bv(key string, g *objGeneric) (bool, error) {
	v, err := f.r.ReadB()
	if err != nil {
		return false, err
	}
	g.Fields = append(g.Fields, objField{key, v != 0})
	return v != 0, nil
}
