// 本文件为内部对象解码的通用框架：objGeneric 结果结构、gfRead 字段
// 读取原语（T/BS/BD/CMC 等，随版本与 R2007+ 字符串流切换）、解码器
// 注册表（固定码 + DXF 类名）、decodeInternalObject 主流程
// （公共头 → dat 流专有字段 → handle 流）与 verUntilR2004 版本判定。

package object

import (
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/entity"
	"github.com/unitedrhino/go-cad/internal/objrec"
	"os"
	"strings"
)

// ---- 通用内部对象解码框架 ----
// 面向布局简单的非实体对象（SCALE/DICTIONARYVAR/APPID 等）：dat 流专有
// 字段由各类型解码器按 dwg.spec 顺序读取，handle 流（owner + reactors +
// xdic + 附加引用）统一处理。字段以 名称+值 对记录，供调试与验证导出。

// objField 内部对象的字段对。
type ObjField struct {
	Key string // 字段名（与 dwgread JSON 键一致）
	Val any    // 值（int64/float64/bool/string）
}

// objGeneric 通用内部对象解码结果。
type ObjGeneric struct {
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
	HdOffsetBits  uint64     // dat 段前导位（body 内 RL bitsize 起点；重编码位长校验用）
	HeadRawBits   string     // RL bitsize 之后至 handle 流起点的原始位串（H/EED/公共头/专有字段），供重编码原样写回
	R2010Plus     bool       // R2010+ 记录布局（无内联 RL，headRawBits 起点为 dataStartBit）
	ValueHandle91 bool       // ACSH/ASSOCVARIABLE：EvalVariant/evalexpr 的 handle 值占 handle 流 1 个引用（解码→hdl 阶段传递）
	HdlCount      int        // SURFACEACTIONBODY 族：handle 流附加引用数（解码→hdl 阶段传递）
	PreBits       string     // R2010+ body 中 dataStartBit 之前的记录头前导位串（MS size/hss UMC），重编码原样回放
	BodyBitOff    uint64     // R2010+ body 内 MS size 尾部对齐位（rec.bodyBitOffset），重解码重建用
	SizeBytes     uint32     // 源对象记录的 MS size（R2010+ 重编码重建 rec 元数据用）
	HSizeField    uint32     // R2010+ handle-stream-size 字段位宽
	HssBits       uint32     // R2010+ handle-stream-size 值（位）
	Fields        []ObjField // dat 流专有字段（按 spec 顺序）
	Handles       []uint64   // handle 流中 owner/reactors/xdic 之外的引用
}

// Field 按名称取字段值（未找到返回 nil）。
func (g *ObjGeneric) Field(key string) any {
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
func (g *ObjGeneric) FieldPath(key string) any {
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
func (g *ObjGeneric) DebugFields() map[string]any {
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
	Decode          func(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error // dat 流专有字段
	Hdl             func(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error // 自定义 handle 流（owner/reactors/xdic 之后调用）
	extraHandles    int                                                                                     // handle 流中 xdic 之后的固定引用数
	handleVectorKey string                                                                                  // 引用数量字段名（如 GROUP 的 num_groups）：handle 流读该数量的引用
	forceStrings    bool                                                                                    // SCALE 特例：has_strings 位恒 0 但 LibreDWG 强制按 1 处理（decode_r2007.c FIXME wrong bit）
	jsonName        string                                                                                  // gold JSON object 名与类表 DXF 名不一致时覆盖（如 DYNAMICBLOCKPURGEPREVENTER）
}

// internalFixedDecoders 固定类型码的内部对象（键为位级类型码）。

var InternalFixedDecoders = map[uint16]internalObjectSpec{
	0x43:  {Decode: decodeGenericAPPID, extraHandles: 1},                                    // APPID
	0x48:  {Decode: decodeGenericGROUP, handleVectorKey: "num_groups"},                      // GROUP
	0x52:  {Decode: decodeGenericLAYOUT, extraHandles: 4, handleVectorKey: "num_viewports"}, // LAYOUT
	0x30:  {Decode: decodeGenericCONTROL, Hdl: decodeGenericCONTROL_HDL},                    // BLOCK_CONTROL
	0x31:  {Decode: decodeGenericBLOCKHEADER, Hdl: decodeGenericBLOCKHEADER_HDL},            // BLOCK_HEADER
	0x32:  {Decode: decodeGenericCONTROL, Hdl: decodeGenericCONTROL_HDL},                    // LAYER_CONTROL
	0x34:  {Decode: decodeGenericCONTROL, Hdl: decodeGenericCONTROL_HDL},                    // STYLE_CONTROL
	0x38:  {Decode: decodeGenericCONTROL, Hdl: decodeGenericCONTROL_HDL},                    // LTYPE_CONTROL
	0x3C:  {Decode: decodeGenericCONTROL, Hdl: decodeGenericCONTROL_HDL},                    // VIEW_CONTROL
	0x3E:  {Decode: decodeGenericCONTROL, Hdl: decodeGenericCONTROL_HDL},                    // UCS_CONTROL
	0x40:  {Decode: decodeGenericCONTROL, Hdl: decodeGenericCONTROL_HDL},                    // VPORT_CONTROL
	0x42:  {Decode: decodeGenericCONTROL, Hdl: decodeGenericCONTROL_HDL},                    // APPID_CONTROL
	0x44:  {Decode: decodeGenericCONTROL, Hdl: decodeGenericCONTROL_HDL},                    // DIMSTYLE_CONTROL
	0x45:  {Decode: decodeGenericDIMSTYLE, Hdl: decodeGenericDIMSTYLE_HDL},                  // DIMSTYLE
	0x35:  {Decode: decodeGenericSTYLE},                                                     // STYLE
	0x41:  {Decode: decodeGenericVPORT, Hdl: decodeGenericVPORT_HDL},                        // VPORT
	0x49:  {Decode: decodeGenericMLINESTYLE, Hdl: decodeGenericMLINESTYLE_HDL},              // MLINESTYLE
	0x46:  {Decode: decodeGenericCONTROL, Hdl: decodeGenericCONTROL_HDL},                    // VX_CONTROL
	0x47:  {Decode: decodeGenericVX_TABLE_RECORD, Hdl: decodeGenericVX_TABLE_RECORD_HDL},    // VX_TABLE_RECORD
	0x50:  {Decode: decodeGenericPLACEHOLDER},                                               // PLACEHOLDER
	0x39:  {Decode: decodeGenericLTYPE, extraHandles: 1},                                    // LTYPE
	0x1F3: {Decode: decodeGenericPROXY_OBJECT, Hdl: decodeGenericPROXY_OBJECT_HDL},          // PROXY_OBJECT
}

// internalClassDecoders 类类型内部对象（键为 DXF 类名，经类名表解析）。
var InternalClassDecoders = map[string]internalObjectSpec{
	"LAYOUT":                 {Decode: decodeGenericLAYOUT, extraHandles: 4, handleVectorKey: "num_viewports"},
	"DICTIONARYVAR":          {Decode: decodeGenericDICTIONARYVAR},
	"TABLESTYLE":             {Decode: decodeGenericTABLESTYLE, Hdl: decodeGenericTABLESTYLE_HDL},
	"MLEADERSTYLE":           {Decode: decodeGenericMLEADERSTYLE, Hdl: decodeGenericMLEADERSTYLE_HDL},
	"ACDBMLEADERSTYLE":       {Decode: decodeGenericMLEADERSTYLE, Hdl: decodeGenericMLEADERSTYLE_HDL},
	"SECTIONVIEWSTYLE":       {Decode: decodeGenericSECTIONVIEWSTYLE, Hdl: decodeGenericSECTIONVIEWSTYLE_HDL},
	"ACDBSECTIONVIEWSTYLE":   {Decode: decodeGenericSECTIONVIEWSTYLE, Hdl: decodeGenericSECTIONVIEWSTYLE_HDL},
	"DETAILVIEWSTYLE":        {Decode: decodeGenericDETAILVIEWSTYLE, Hdl: decodeGenericDETAILVIEWSTYLE_HDL},
	"ACDBDETAILVIEWSTYLE":    {Decode: decodeGenericDETAILVIEWSTYLE, Hdl: decodeGenericDETAILVIEWSTYLE_HDL},
	"FIELDLIST":              {Decode: decodeGenericFIELDLIST, handleVectorKey: "num_fields"},
	"SCALE":                  {Decode: decodeGenericSCALE, forceStrings: true},
	"WIPEOUTVARIABLES":       {Decode: decodeGenericWIPEOUTVARIABLES},
	"VISUALSTYLE":            {Decode: decodeGenericVISUALSTYLE},
	"BLOCK_HEADER":           {Decode: decodeGenericBLOCKHEADER, Hdl: decodeGenericBLOCKHEADER_HDL},
	"LTYPE":                  {Decode: decodeGenericLTYPE, extraHandles: 1},
	"MATERIAL":               {Decode: decodeGenericMATERIAL},
	"DIMASSOC":               {Decode: decodeGenericDIMASSOC, Hdl: decodeGenericDIMASSOC_HDL},
	"ASSOCNETWORK":           {Decode: decodeGenericASSOCNETWORK, Hdl: decodeGenericASSOCNETWORK_HDL},
	"FIELD":                  {Decode: decodeGenericFIELD, Hdl: decodeGenericFIELD_HDL},
	"SUN":                    {Decode: DecodeGenericSUN},
	"SKYLIGHT_BACKGROUND":    {Decode: decodeGenericSKYLIGHTBACKGROUND, extraHandles: 1},
	"MTEXTOBJECTCONTEXTDATA": {Decode: decodeGenericMTEXTOBJECTCONTEXTDATA, extraHandles: 1},
	// R2000 类表的 DXF 名带 ACDB_ 前缀与 _CLASS 后缀（fzw 类表实证）
	"ACDB_MTEXTOBJECTCONTEXTDATA_CLASS": {Decode: decodeGenericMTEXTOBJECTCONTEXTDATA, extraHandles: 1},
	"SPATIAL_FILTER":                    {Decode: decodeGenericSPATIALFILTER},
	"ACSH_HISTORY_CLASS":                {Decode: DecodeGenericACSH_HISTORY_CLASS, extraHandles: 1},
	"TABLEGEOMETRY":                     {Decode: decodeGenericTABLEGEOMETRY},
	// TABLECONTENT/DATATABLE 属 dwg2.spec 的 DEBUG_CLASSES 条件块：
	// LibreDWG 默认构建不解析（gold 输出 UNKNOWN_OBJ），暂不注册，
	// 待有真实样本基准验证后再启用（解码器与合成流测试已就绪）
	// "TABLECONTENT": {decode: decodeGenericTABLECONTENT, hdl: decodeGenericTABLECONTENT_HDL},
	// "DATATABLE":    {decode: decodeGenericDATATABLE},
	"ACDBPLACEHOLDER":                   {Decode: decodeGenericPLACEHOLDER},
	"ACDBASSOCNETWORK":                  {Decode: decodeGenericASSOCNETWORK, Hdl: decodeGenericASSOCNETWORK_HDL},
	"ASSOCACTION":                       {Decode: decodeGenericASSOCACTION, Hdl: decodeGenericASSOCACTION_HDL},
	"ASSOCDEPENDENCY":                   {Decode: decodeGenericASSOCDEPENDENCY, Hdl: decodeGenericASSOCDEPENDENCY_HDL},
	"ASSOCGEOMDEPENDENCY":               {Decode: decodeGenericASSOCGEOMDEPENDENCY, Hdl: decodeGenericASSOCDEPENDENCY_HDL},
	"ACDBASSOCACTION":                   {Decode: decodeGenericASSOCACTION, Hdl: decodeGenericASSOCACTION_HDL},
	"ACDBASSOCDEPENDENCY":               {Decode: decodeGenericASSOCDEPENDENCY, Hdl: decodeGenericASSOCDEPENDENCY_HDL},
	"ACDBASSOCGEOMDEPENDENCY":           {Decode: decodeGenericASSOCGEOMDEPENDENCY, Hdl: decodeGenericASSOCDEPENDENCY_HDL},
	"EVALUATION_GRAPH":                  {Decode: decodeGenericEVALUATION_GRAPH, Hdl: decodeGenericEVALUATION_GRAPH_HDL},
	"ACAD_EVALUATION_GRAPH":             {Decode: decodeGenericEVALUATION_GRAPH, Hdl: decodeGenericEVALUATION_GRAPH_HDL},
	"CELLSTYLEMAP":                      {Decode: decodeGenericCELLSTYLEMAP, Hdl: decodeGenericCELLSTYLEMAP_HDL},
	"ASSOCOSNAPPOINTREFACTIONPARAM":     {Decode: decodeGenericASSOCOSNAPPOINTREFACTIONPARAM, Hdl: decodeGenericASSOCOSNAPPOINTREFACTIONPARAM_HDL},
	"ACDBASSOCOSNAPPOINTREFACTIONPARAM": {Decode: decodeGenericASSOCOSNAPPOINTREFACTIONPARAM, Hdl: decodeGenericASSOCOSNAPPOINTREFACTIONPARAM_HDL},
	"ASSOCVERTEXACTIONPARAM":            {Decode: decodeGenericASSOCVERTEXACTIONPARAM},
	"ACDBASSOCVERTEXACTIONPARAM":        {Decode: decodeGenericASSOCVERTEXACTIONPARAM},
	"SORTENTSTABLE":                     {Decode: decodeGenericSORTENTSTABLE, Hdl: decodeGenericSORTENTSTABLE_HDL},
	"IMAGEDEF":                          {Decode: decodeGenericIMAGEDEF},
	"IMAGEDEF_REACTOR":                  {Decode: decodeGenericIMAGEDEF_REACTOR},
	"RASTERVARIABLES":                   {Decode: decodeGenericRASTERVARIABLES},
	"PDFDEFINITION":                     {Decode: decodeGenericUNDERLAYDEFINITION},
	"DGNDEFINITION":                     {Decode: decodeGenericUNDERLAYDEFINITION},
	"DWFDEFINITION":                     {Decode: decodeGenericUNDERLAYDEFINITION},
	// 语料 0 实例类（dwg.spec 字段布局 + 合成位流单测自证，见 objects_longtail.go）
	"IDBUFFER":     {Decode: decodeGenericIDBUFFER, handleVectorKey: "num_obj_ids"},
	"INDEX":        {Decode: decodeGenericINDEX},
	"LAYER_INDEX":  {Decode: decodeGenericLAYER_INDEX, Hdl: decodeGenericLAYER_INDEX_HDL},
	"PROXY_OBJECT": {Decode: decodeGenericPROXY_OBJECT, Hdl: decodeGenericPROXY_OBJECT_HDL},
}

// init 合并 ACSH 形体系注册表（objects_acsh.go，公共前导 + primitive）。
func init() {
	for name, spec := range acshDecoders() {
		InternalClassDecoders[name] = spec
	}
}

// verUntilR2004 版本是否为 R2004 及更早（版本枚举非时间序，禁止范围比较）。
func VerUntilR2004(Ver container.DwgVersion) bool {
	return Ver == container.VerR13 || Ver == container.VerR14 || Ver == container.VerR2000 || Ver == container.VerR2004
}

// gfRead 字段读取辅助：T 随版本与对象字符串流标志切换读取方式。
// R2007+ 且对象 has_strings=1 时，FIELD_T 从记录尾部的字符串区顺序取值
// （dat 流不占位，LibreDWG FIELD_T 宏行为）；否则 R2007 前 TV 内联。
type GfRead struct {
	R        *bitstream.BitStream
	Ver      container.DwgVersion
	strs     []string // 字符串区预读的 TU 序列（R2007+ 字符串流对象）
	strIdx   int      // 下一个待取的字符串下标
	codepage uint16   // 文档码页（pre-R2007 的 TV 文本按此解码，fzw ANSI_936 实证）
}

// T 读文字字段。
func (f *GfRead) T(key string, g *ObjGeneric) error {
	if f.Ver < container.VerR2007 {
		s, err := f.R.ReadTV(f.codepage)
		if err != nil {
			return err
		}
		g.Fields = append(g.Fields, ObjField{key, s})
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
	g.Fields = append(g.Fields, ObjField{key, s})
	return nil
}

// BS 读 BS 字段。
func (f GfRead) BS(key string, g *ObjGeneric) error {
	v, err := f.R.ReadBS()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, ObjField{key, int64(v)})
	return nil
}

// BD 读 BD 字段。
func (f GfRead) BD(key string, g *ObjGeneric) error {
	v, err := f.R.ReadBD()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, ObjField{key, v})
	return nil
}

// RC 读 RC 字段。
func (f GfRead) RC(key string, g *ObjGeneric) error {
	v, err := f.R.ReadRC()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, ObjField{key, int64(v)})
	return nil
}

// B 读位字段。
func (f GfRead) B(key string, g *ObjGeneric) error {
	v, err := f.R.ReadB()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, ObjField{key, v != 0})
	return nil
}

// RD2 读 2RD 坐标对（两个 RD），以 [x y] 数组记录（对齐 dwgread JSON 形状）。
func (f GfRead) RD2(key string, g *ObjGeneric) error {
	x, err := f.R.ReadRD()
	if err != nil {
		return err
	}
	y, err := f.R.ReadRD()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, ObjField{key, []float64{x, y}})
	return nil
}

// decodeGenericAPPID 解析 APPID（dwg.spec DWG_TABLE(APPID)）：
// dat 流 = COMMON_TABLE_FLAGS（name T + xref 标志）+ RC unknown(71)；
// handle 流 = owner + reactors + xdic + xref。
func decodeGenericAPPID(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {

	if err := fr.T("name", g); err != nil {
		return err
	}
	if err := readCommonTableFlags(R, fr, g, Ver); err != nil {
		return err
	}
	return fr.RC("unknown", g)
}

// decodeGenericDICTIONARYVAR 解析 DICTIONARYVAR（dwg.spec）：
// dat 流 = RCd schema(280) + T strvalue。
func decodeGenericDICTIONARYVAR(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {

	if err := fr.RC("schema", g); err != nil {
		return err
	}
	return fr.T("strvalue", g)
}

// decodeGenericSCALE 解析 SCALE（dwg2.spec DWG_OBJECT(SCALE)）：
// dat 流 = BS flag(70) + T name(300) + BD paper_units(140) +
// BD drawing_units(141) + B is_unit_scale(290)。
func decodeGenericSCALE(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {

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
func DecodeInternalObject(R *bitstream.BitStream, rec *objrec.ObjectRecord, Ver container.DwgVersion, r2013Plus bool, typeCode uint16, className string, codepage uint16) (*ObjGeneric, error) {
	// UNDERLAY 引用实体：实体布局但经对象分发（此前 UNKNOWN_OBJ 兜底），
	// 按实体头 + UNDERLAY_fields 解码（见 objects_underlay.go）
	if className == "PDFUNDERLAY" || className == "DWFUNDERLAY" || className == "DGNUNDERLAY" {
		return decodeUnderlayEntity(R, rec, Ver, typeCode, className)
	}
	// UNKNOWN_OBJ：无法识别的类，记录元数据与未知位区（LibreDWG 同样
	// 仅存储 unknown_bits，不解析字段）
	if className == "UNKNOWN_OBJ" || className == "ACDBASSOCPERSSUBENTMANAGER" {
		className = "UNKNOWN_OBJ"
		unkStart := R.TellBits()
		ug := &ObjGeneric{Name: className}
		bitsizePosU := dictBitsizePos(Ver)
		if bitsizePosU == bitsizePosHead {
			ug.ObjSizeBit, _ = ReadInlineBitsize(R)
		}
		// 未知类的流结构不可预知：全程宽容读取，失败即返回已得元数据
		ug.Handle, _ = readHandleValue(R)
		ug.Fields = append(ug.Fields,
			ObjField{"object", ug.Name},
			ObjField{"type", int64(typeCode)},
			ObjField{"size", int64(rec.Size)},
			ObjField{"has_ds_data", false},
		)
		_ = skipEEDChain(R)
		if bitsizePosU == bitsizePosTail {
			ug.ObjSizeBit, _ = ReadInlineBitsize(R)
		}
		if nr, uerr := R.ReadBL(); uerr == nil && nr <= 4096 {
			ug.NumReactors = int(nr)
		}
		if Ver >= container.VerR2004 {
			if xd, e := R.ReadB(); e == nil {
				ug.XdicMissing = xd == 1
			}
		}
		if r2013Plus {
			if _, e := R.ReadB(); e != nil {
				return ug, nil
			}
		}
		if bitsizePosU == bitsizePosDerived {
			ug.ObjSizeBit = rec.DataEndBit() - rec.BodyBitOffset - uint64(rec.HandleSizeFieldBits)
			R.SetBitPos(rec.DataEndBit())
		} else {
			R.SetBitPos(rec.BodyBitOffset + ug.ObjSizeBit)
		}
		ug.Fields = append(ug.Fields, ObjField{"is_xdic_missing", ug.XdicMissing})
		ug.Fields = append(ug.Fields, ObjField{"bitsize", int64(ug.ObjSizeBit)})
		ug.Owner, _ = readOwnerHandle(R, ug.Handle)
		for i := 0; i < ug.NumReactors; i++ {
			if _, e := objrec.ReadHandleReference(R, ug.Handle); e != nil {
				break
			}
		}
		if !ug.XdicMissing {
			objrec.ReadHandleReference(R, ug.Handle)
		}
		// 回放收集：data 段与 handle 流的原始位串（同通用路径模式）
		unkEnd := R.TellBits()
		ug.HeadRawBits = bitstream.CollectBits(R, unkStart, unkEnd)
		ug.RawHandleBits = bitstream.CollectBits(R, unkEnd, uint64(len(R.Src))*8)
		ug.HdOffsetBits = unkStart - rec.BodyBitOffset
		return ug, nil
	}
	// DICTIONARYWDFLT：DICTIONARY 布局 + hdl 尾 defaultid
	if typeCode == 0x2B || className == "ACDBDICTIONARYWDFLT" || className == "DICTIONARYWDFLT" {
		startPos := R.TellBits()
		dd, e := DecodeDictionaryObjectFull(R, rec, Ver, r2013Plus, true)
		if e != nil {
			return nil, e
		}
		// 位串收集：data 段 + handle 流 + 元数据（与通用路径同构）
		hdOff := startPos - rec.BodyBitOffset
		headRaw := bitstream.CollectBits(R, startPos+32, rec.DataEndBit())
		flds := []ObjField{
			{"object", "DICTIONARYWDFLT"},
			{"type", int64(typeCode)},
			{"size", int64(rec.Size)},
			{"bitsize", int64(dd.ObjSizeBit)},
			{"num_reactors", int64(dd.NumReactors)},
			{"is_xdic_missing", dd.XdicMissing},
		}
		if r2013Plus {
			flds = append(flds, ObjField{"has_ds_data", false})
		}
		flds = append(flds,
			ObjField{"dxfname", "ACDBDICTIONARYWDFLT"},
			ObjField{"numitems", int64(dd.NumItems)},
			ObjField{"cloning", int64(dd.Cloning)},
			ObjField{"is_hardowner", dd.IsHardOwner},
		)
		flds = append(flds, dd.EedFields...)
		return &ObjGeneric{
			Name: "DICTIONARYWDFLT", Handle: dd.Handle, Owner: dd.Owner,
			ObjSizeBit: dd.ObjSizeBit, NumReactors: dd.NumReactors,
			XdicMissing:  dd.XdicMissing,
			Fields:       flds,
			Handles:      dd.ItemHandles,
			HeadRawBits:  headRaw,
			HdOffsetBits: hdOff,
		}, nil
	}
	spec, ok := InternalFixedDecoders[typeCode]
	if !ok && className != "" {
		spec, ok = InternalClassDecoders[className]
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
		spec = internalObjectSpec{Decode: func(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, gg *ObjGeneric) error {
			gg.Unknown = true
			gg.OrigClass = origClass
			return nil
		}}
		ok = true
	}
	g := &ObjGeneric{Name: className}
	if g.Name == "" {
		if n, ok := objrec.ObjTypeCode[typeCode]; ok {
			g.Name = n
		}
	}
	bitsizePos := dictBitsizePos(Ver)
	// dat 段前导位：RL bitsize 字段起点（body 内），重编码位长校验用
	g.HdOffsetBits = R.TellBits() - rec.BodyBitOffset
	if bitsizePos == bitsizePosHead {
		bs, e := ReadInlineBitsize(R)
		if e != nil {
			return nil, e
		}
		g.ObjSizeBit = bs
	}
	var err error
	_dbg := os.Getenv("CAD_DECODE_DBG") != ""
	if _dbg {
		fmt.Fprintf(os.Stderr, "[hdr] start@%d type=%X\n", R.TellBits(), typeCode)
	}
	if g.Handle, err = readHandleValue(R); err != nil {
		return nil, fmt.Errorf("hdlv@%d: %w", R.TellBits(), err)
	}
	if _dbg {
		fmt.Fprintf(os.Stderr, "[hdr] handle done @%d\n", R.TellBits())
	}
	if err = parseEEDChain(R, Ver, &g.Fields); err != nil {
		return nil, fmt.Errorf("eed@%d: %w", R.TellBits(), err)
	}
	if _dbg {
		fmt.Fprintf(os.Stderr, "[hdr] eed done @%d\n", R.TellBits())
	}
	if bitsizePos == bitsizePosTail {
		if g.ObjSizeBit, err = ReadInlineBitsize(R); err != nil {
			return nil, err
		}
	}
	var NumReactors uint32
	if NumReactors, err = R.ReadBL(); err != nil {
		return nil, fmt.Errorf("nr@%d: %w", R.TellBits(), err)
	}
	if _dbg {
		fmt.Fprintf(os.Stderr, "[hdr] reactors done @%d (n=%d)\n", R.TellBits(), NumReactors)
	}
	if NumReactors > 4096 {
		return nil, fmt.Errorf("cad: 内部对象 reactors 异常 %d", NumReactors)
	}
	g.NumReactors = int(NumReactors)
	if Ver >= container.VerR2004 {
		if xdic, e := R.ReadB(); e != nil {
			return nil, e
		} else {
			g.XdicMissing = xdic == 1
		}
	}
	if r2013Plus {
		if ds, e := R.ReadB(); e != nil { // has_ds_data
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
	fr := &GfRead{R: R, Ver: Ver, codepage: codepage}
	if Ver >= container.VerR2007 {
		bitsize := g.ObjSizeBit
		if bitsize == 0 { // R2010+：由记录头推导
			bitsize = rec.DataEndBit() - rec.BodyBitOffset - uint64(rec.HandleSizeFieldBits)
			g.ObjSizeBit = bitsize
		}
		libreBase := uint64(rec.HandleSizeFieldBits) + rec.BodyBitOffset
		savedBits := R.TellBits()
		R.SetBitPos(libreBase + bitsize - 1)
		b, e := R.ReadB()
		if (e == nil && b == 1) || spec.forceStrings {
			fr.strs = ReadStringAreaBitRange(R, libreBase+bitsize, 64, Ver >= container.VerR2013)
		}
		R.SetBitPos(savedBits)
	}
	if err = spec.Decode(R, Ver, fr, g); err != nil {
		return nil, fmt.Errorf("decode@%d: %w", R.TellBits(), err)
	}
	// 专有字段段原始位串：RL bitsize 之后至 handle 流起点（bitsize）。
	// 按 objSizeBit 定界（而非当前位）：解码器内部可能跳过预览位串等
	// 非连续段，原样回放须包含全部位。
	// R2010+（bitsizePosDerived）无内联 RL：起点为 dataStartBit，
	// 终点为记录数据结束位（dataEndBit），并保存重建 rec 元数据。
	if bitsizePos == bitsizePosHead {
		if g.ObjSizeBit > g.HdOffsetBits+32 {
			g.HeadRawBits = bitstream.CollectBits(R, rec.BodyBitOffset+g.HdOffsetBits+32, rec.BodyBitOffset+g.ObjSizeBit)
		}
	} else {
		g.R2010Plus = true
		g.SizeBytes = rec.Size
		g.HSizeField = rec.HandleSizeFieldBits
		g.HssBits = rec.HandleStreamSizeBits
		g.BodyBitOff = rec.BodyBitOffset
		g.PreBits = bitstream.CollectBits(R, rec.BodyBitOffset, rec.BodyBitOffset+g.HdOffsetBits)
		// 注意：终点 dataEndBit 与 RawHandleBits 起点（bitsize）存在
		// hSizeField 位重叠——R2010+ 首轮 handle 定位（setBitPos
		// dataEndBit）能通过 gold 的机理未明，修正需连同首轮定位
		// 一起统一，见任务文档 R2010+ 卡点记录
		g.HeadRawBits = bitstream.CollectBits(R, rec.BodyBitOffset+g.HdOffsetBits, rec.DataEndBit())
	}
	// handle 流
	switch bitsizePos {
	case bitsizePosDerived:
		g.ObjSizeBit = rec.DataEndBit() - rec.BodyBitOffset - uint64(rec.HandleSizeFieldBits)
		R.SetBitPos(rec.DataEndBit())
	default:
		R.SetBitPos(rec.BodyBitOffset + g.ObjSizeBit)
	}
	// handle 流原始位串：起点（bitsize）至 body 尾，供重编码原样写回
	g.RawHandleBits = bitstream.CollectBits(R, R.TellBits(), uint64(len(R.Src))*8)
	if g.Owner, err = readOwnerHandle(R, g.Handle); err != nil {
		return nil, fmt.Errorf("hdl.owner@%d: %w", R.TellBits(), err)
	}
	for i := 0; i < g.NumReactors; i++ {
		if _, err = objrec.ReadHandleReference(R, g.Handle); err != nil {
			return nil, fmt.Errorf("hdl.reactor%d@%d: %w", i, R.TellBits(), err)
		}
	}
	if !g.XdicMissing {
		if _, err = objrec.ReadHandleReference(R, g.Handle); err != nil {
			return nil, fmt.Errorf("hdl.xdic@%d: %w", R.TellBits(), err)
		}
	}
	// 公共元数据字段（对照 dwgread JSON 的公共键，供导出与值级审计）
	objName := g.Name
	if spec.jsonName != "" {
		objName = spec.jsonName
	}
	g.Fields = append(g.Fields,
		ObjField{"object", objName},
		ObjField{"type", int64(typeCode)},
		ObjField{"size", int64(rec.Size)},
		ObjField{"bitsize", int64(g.ObjSizeBit)},
		ObjField{"num_reactors", int64(g.NumReactors)},
		ObjField{"is_xdic_missing", g.XdicMissing},
		ObjField{"has_ds_data", g.HasDsData},
		ObjField{"dxfname", g.Name},
	)
	if spec.Hdl != nil {
		return g, spec.Hdl(R, Ver, fr, g)
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
		h, e := objrec.ReadHandleReference(R, g.Handle)
		if e != nil {
			return nil, fmt.Errorf("hdl.vec%d/%d@%d: %w", i, n, R.TellBits(), e)
		}
		g.Handles = append(g.Handles, h)
	}
	return g, nil
}

// readStringAreaBitRange 在指定位流上按 R2013+ 字符串区公式顺序读取至多
// max 个 TU 字符串：RS dataSize @ bitsize-17（0x8000 高位为扩展格式标志），
// 区起点 = bitsize-17-dataSize，内容为顺序 TU。bitsize 为相对该读取器
// 0 点（MS 字段之后）的位长。
func ReadStringAreaBitRange(R *bitstream.BitStream, bitsize uint64, max int, r2013Plus bool) []string {
	if bitsize < 34 {
		return nil
	}
	pos := bitsize - 17
	R.SetBitPos(pos)
	dataSize, err := R.ReadRS()
	if err != nil {
		return nil
	}
	if dataSize&0x8000 != 0 {
		// 扩展格式：hi_size RS 位于 bitsize-33，真实大小 =
		// (data_size&0x7FFF) | (hi_size<<15)（对齐 obj_string_stream）
		R.SetBitPos(bitsize - 33)
		hi, e := R.ReadRS()
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
	R.SetBitPos(start)
	out := make([]string, 0, max)
	for i := 0; i < max; i++ {
		s, e := R.ReadTU()
		if e != nil {
			break
		}
		out = append(out, s)
	}
	return out
}

// BL 读 BL 字段。
func (f GfRead) BL(key string, g *ObjGeneric) error {
	v, err := f.R.ReadBL()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, ObjField{key, int64(v)})
	return nil
}

// BLd 读有符号 BL 字段（LibreDWG BLd：位流同 BL，输出按 int32 解释），
// 如 EVALUATION_GRAPH out_edge 的 -1 表无、ASSOCDEPENDENCY order 的负序值。
func (f GfRead) BLd(key string, g *ObjGeneric) error {
	v, err := f.R.ReadBL()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, ObjField{key, int64(int32(v))})
	return nil
}

// decodeGenericGROUP 解析 GROUP（dwg.spec DWG_OBJECT(GROUP)）：
// dat 流 = T name(300) + BS unnamed(70) + BS selectable(71) + BL num_groups；
// handle 流 = owner + reactors + xdic + groups×num_groups。
func decodeGenericGROUP(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
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
func decodeGenericWIPEOUTVARIABLES(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	return fr.BS("display_frame", g)
}

// decodeGenericSORTENTSTABLE 解析 SORTENTSTABLE（dwg2.spec
// DWG_OBJECT(SORTENTSTABLE)）：dat 流 = BL num_ents(0) +
// sort_ents×num_ents —— 该句柄数组特殊地内联在 dat 流（spec 以
// str_dat=hdl_dat; hdl_dat=dat 显式切换，code 0 绝对引用），
// 先于公共 handle 流存储。
func decodeGenericSORTENTSTABLE(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	num, err := R.ReadBL()
	if err != nil {
		return err
	}
	// spec VALUEOUTOFBOUNDS(num_ents, 50000)：越界视为流错位，拒绝解码
	if num > 50000 {
		return fmt.Errorf("cad: SORTENTSTABLE num_ents 越界 %d", num)
	}
	g.Fields = append(g.Fields, ObjField{"num_ents", int64(num)})
	for i := 0; i < int(num); i++ {
		h, e := objrec.ReadHandleReference(R, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
		g.Fields = append(g.Fields, ObjField{fmt.Sprintf("sort_ents[%d]", i), int64(h)})
	}
	return nil
}

// decodeGenericSORTENTSTABLE_HDL SORTENTSTABLE 的 handle 流附加引用：
// block_owner（排序所属的 mspace/pspace BLOCK_HEADER，soft owner）+
// ents×num_ents（排序前顺序的实体引用，与 sort_ents 按下标配对）。
func decodeGenericSORTENTSTABLE_HDL(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
	h, e := objrec.ReadHandleReference(R, g.Handle)
	if e != nil {
		return e
	}
	g.Handles = append(g.Handles, h)
	g.Fields = append(g.Fields, ObjField{"block_owner", int64(h)})
	num, _ := g.Field("num_ents").(int64)
	for i := 0; i < int(num); i++ {
		h, e := objrec.ReadHandleReference(R, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
		g.Fields = append(g.Fields, ObjField{fmt.Sprintf("ents[%d]", i), int64(h)})
	}
	return nil
}

// decodeGenericIMAGEDEF 解析 IMAGEDEF（dwg.spec DWG_OBJECT(IMAGEDEF)，
// AcDbRasterImageDef）：dat 流 = BL class_version(90) + 2RD image_size +
// T file_path + B is_loaded + RC resunits + 2RD pixel_size。位级字段
// 顺序与 DXF 顺序不同（DXF 端 file_path 首位）；R2007+ 的 file_path
// 走对象字符串流（gfRead.T）。
func decodeGenericIMAGEDEF(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
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
func decodeGenericIMAGEDEF_REACTOR(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
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
func decodeGenericRASTERVARIABLES(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
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
func decodeGenericUNDERLAYDEFINITION(R *bitstream.BitStream, Ver container.DwgVersion, fr *GfRead, g *ObjGeneric) error {
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
func (f *GfRead) CMC(key string, g *ObjGeneric) error {
	if f.Ver < container.VerR2004 {
		idx, err := f.R.ReadBS()
		if err != nil {
			return err
		}
		// pre-R2004 的 CMC 仅 index（dwgread JSON 输出为纯数值）
		g.Fields = append(g.Fields, ObjField{key, int64(idx)})
		return nil
	}
	return f.readCMCR2004(key, g)
}

// CMTC 强制按 R2004+ 的 CMC 读法（LibreDWG FIELD_CMTC 语义：
// TABLESTYLE 等样式类即使出现在早期版本也按 true color 结构）。
func (f *GfRead) CMTC(key string, g *ObjGeneric) error {
	return f.readCMCR2004(key, g)
}

// readCMCR2004 R2004+ 的 CMC 结构：BS index + BL rgb + RC flag
// （flag&1 → name T、flag&2 → book_name，从字符串流）。
func (f *GfRead) readCMCR2004(key string, g *ObjGeneric) error {
	dbgC := os.Getenv("CAD_CMTC_DBG") != "" && strings.Contains(key, "borders[0].color")
	startPos := f.R.TellBits()
	if dbgC {
		fmt.Fprintf(os.Stderr, "[cmtcPRE] h=%d key=%s pos=%d-40..%s\n", g.Handle, key, startPos, bitstream.CollectBits(f.R, startPos-40, startPos+100))
	}
	idx, err := f.R.ReadBS()
	if err != nil {
		return err
	}
	rgb, err := f.R.ReadBL()
	if err != nil {
		return err
	}
	method := rgb >> 24
	flag, err := f.R.ReadRC()
	if err != nil {
		return err
	}
	if dbgC {
		fmt.Fprintf(os.Stderr, "[cmtc2] h=%d key=%s start=%d end=%d idx=%d rgb=%08x flag=%d bits=%s\n",
			g.Handle, key, startPos, f.R.TellBits(), idx, rgb, flag, bitstream.CollectBits(f.R, startPos, startPos+60))
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
	cm["index"] = entity.DwgFindColorIndex(rgb)
	g.Fields = append(g.Fields, ObjField{key, cm})
	return nil
}

// Bv 读位字段并返回数值（条件字段如 has_name 用）。
func (f *GfRead) Bv(key string, g *ObjGeneric) (bool, error) {
	v, err := f.R.ReadB()
	if err != nil {
		return false, err
	}
	g.Fields = append(g.Fields, ObjField{key, v != 0})
	return v != 0, nil
}
