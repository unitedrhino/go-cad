// dxf_read.go 实现 DXF（Drawing eXchange Format）读取：ASCII 与二进制
// （"AutoCAD Binary DXF"）两种编码统一抽象为组码对流，再按段语义构建与
// DWG 解析一致的 Document 模型。
//
// 主要模块划分：
//   - 词法层 dxfLexer：ASCII 组码行/值行解析（容忍空格填充与 CR 行尾），
//     二进制变体的魔数识别、pre-R14 单字节组码探测与按组码类型的记录读取；
//   - 语义层：HEADER（$ACADVER → 版本枚举）、TABLES（LAYER 表：句柄+颜色）、
//     BLOCKS（块定义 + *Model_Space 特判）、ENTITIES（主力图元组码 → entXxx）；
//   - 公开入口 ParseDXF。
//
// 支持的实体类型（与既有 Document 模型对齐）：LINE/CIRCLE/ARC/POINT/
// ELLIPSE/TEXT/MTEXT/LWPOLYLINE/POLYLINE+VERTEX/PFACE/MESH/INSERT/ATTRIB/
// ATTDEF/SOLID/TRACE/3DFACE/RAY/XLINE/SPLINE，以及批次 R 复杂实体
// DIMENSION(7 型)/HATCH(渐变段+种子点)/LEADER/MULTILEADER(txt+blk 内容)/
// MLINE/TOLERANCE/VIEWPORT 与极限批次 A 的 REGION/3DSOLID/BODY(SAT 文本
// 行拼接)。DXF 格式参考 LibreDWG in_dxf.c（读侧）与 dwg.spec/dwg2.spec
// 的 DXF 组码标注。
package cad

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/entity"
	"github.com/unitedrhino/go-cad/internal/object"
	"math"
	"strconv"
	"strings"
)

// dxfBinaryMagic 二进制 DXF 文件头（22 字节，LibreDWG dwg_read_dxf 同款）。
var dxfBinaryMagic = []byte("AutoCAD Binary DXF\r\n\x1a\x00")

// ParseDXF 解析 DXF 字节流（自动识别 ASCII 与二进制编码）为文档模型。
// 返回的 Document 与 DWG 解析共用：modelSpace/blocks/attribs/layerColors
// 均按 DWG 语义填充（图层句柄来自 LAYER 表，INSERT.blockHeader 指向
// BLOCKS 段同名块），可直接用于 RenderPNG / Texts。
func ParseDXF(data []byte) (*Document, error) {
	lex, err := newDXFLexer(data)
	if err != nil {
		return nil, err
	}
	doc := &Document{
		version:         lex.version,
		codepage:        30, // ANSI_1252 的 DWG 编号（HEADER $DWGCODEPAGE 可覆盖）
		blocks:          make(map[uint64][]any),
		attribs:         make(map[uint64]*entity.EntAttrib),
		layerColors:     make(map[uint64]layerColor),
		internalObjects: make(map[uint64]*object.ObjGeneric),
	}
	st := &dxfState{doc: doc, lexer: lex,
		layerByName: map[string]uint64{},
		blockByName: map[string]uint64{},
		nextHandle:  dxfFirstSynthHandle,
	}
	if err := st.parseSections(); err != nil {
		return nil, err
	}
	if !st.sawSection {
		return nil, fmt.Errorf("cad: DXF 输入中未找到 SECTION 段")
	}
	return doc, nil
}

// dxfFirstSynthHandle 合成句柄起点：DXF 记录缺组码 5 时（R12 之前的手写
// 文件常见）从该值起递增，避开真实句柄常驻的低段位。
const dxfFirstSynthHandle = 0x100000

// dxfState DXF 语义解析的会话状态。
type dxfState struct {
	doc         *Document
	lexer       *dxfLexer
	layerByName map[string]uint64 // LAYER 表：名字 → 句柄
	blockByName map[string]uint64 // BLOCKS 段：块名 → 句柄
	nextHandle  uint64            // 合成句柄分配器
	sawSection  bool              // 是否进入过任一 SECTION
	unsupported int               // 跳过的未支持实体计数
	// curPolylineHost 最近一个 POLYLINE 宿主：R12 布局的 VERTEX 无 330
	// owner，按文件顺序归属最近的 POLYLINE，并回填其顶点句柄表
	curPolylineHost any
	// curInsert 最近一个带 66=1 的 INSERT：其后的 ATTRIB 序列归属该块参照
	curInsert *entity.EntInsert
}

// parseSections 主循环：逐段分发，未知段整段跳过。
func (st *dxfState) parseSections() error {
	for {
		p, ok, err := st.lexer.next()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if p.code != 0 {
			continue // 段外散落组码对：容忍并忽略
		}
		switch p.strValue() {
		case "EOF":
			return nil
		case "SECTION":
			st.sawSection = true
			name, err := st.readSectionName()
			if err != nil {
				return err
			}
			if err := st.dispatchSection(name); err != nil {
				return err
			}
		}
	}
}

// readSectionName 段头之后的 (2, 段名) 对。
func (st *dxfState) readSectionName() (string, error) {
	p, ok, err := st.lexer.next()
	if err != nil {
		return "", err
	}
	if !ok || p.code != 2 {
		return "", fmt.Errorf("cad: DXF SECTION 后缺少段名")
	}
	return p.strValue(), nil
}

// dispatchSection 按段名分发；HEADER/TABLES/BLOCKS/ENTITIES 有专门解析，
// 其余（CLASSES/OBJECTS/ACAD_XREC 等）跳过到 ENDSEC。
func (st *dxfState) dispatchSection(name string) error {
	switch name {
	case "HEADER":
		return st.parseHeader()
	case "TABLES":
		return st.parseTables()
	case "BLOCKS":
		return st.parseBlocks()
	case "ENTITIES":
		return st.parseEntitiesSpace()
	default:
		return st.skipToEndSec()
	}
}

// skipToEndSec 消费组码对直到 (0, ENDSEC) 或文件结束。
func (st *dxfState) skipToEndSec() error {
	for {
		p, ok, err := st.lexer.next()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if p.code == 0 && (p.strValue() == "ENDSEC" || p.strValue() == "EOF") {
			return nil
		}
	}
}

// ---- HEADER 段 ----

// parseHeader 头部变量：$ACADVER（版本枚举）与 $DWGCODEPAGE（码页），
// 其余键从略。读到码页后，pre-R13（R12 及更早）的 DXF 文本按该码页
// 解码（复用 DWG 侧 decodeCodepage，GBK 等；R13+ 文本保持字节直读）。
func (st *dxfState) parseHeader() error {
	for {
		p, ok, err := st.lexer.next()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if p.code == 0 {
			if p.strValue() == "ENDSEC" {
				return nil
			}
			continue
		}
		if p.code != 9 {
			continue
		}
		switch p.strValue() {
		case "$ACADVER":
			v, ok, err := st.lexer.next()
			if err != nil {
				return err
			}
			if ok {
				st.doc.version = dxfVersionEnum(v.strValue())
				st.applyCodepage()
			}
		case "$DWGCODEPAGE":
			v, ok, err := st.lexer.next()
			if err != nil {
				return err
			}
			if ok {
				st.doc.codepage = dxfCodepageValue(v.strValue())
				st.applyCodepage()
			}
		default:
			// 其余头部变量连同其值对一并略过（值对由下一轮循环消费）
		}
	}
}

// applyCodepage HEADER 键就绪后同步词法层码页：仅 pre-R13（R12 系）
// 应用——R13+ 的 DXF 文本不走码页；未知码页（0）显式禁用（字节直读）。
func (st *dxfState) applyCodepage() {
	if st.doc.version.PreR13() {
		st.lexer.cp = st.doc.codepage
	}
}

// dxfCodepageValue DXF $DWGCODEPAGE 名 → DWG 码页编号（decodeCodepage
// 的输入域）：ANSI_936/GBK → 31；windows-125x 家族 → N-1222（1252→30）；
// 未知名返回 0（保持字节直读，避免历史上 uint16 负溢出产生无效编号）。
func dxfCodepageValue(name string) uint16 {
	name = strings.TrimSpace(name)
	if name == "GBK" {
		return 31
	}
	if n, ok := strings.CutPrefix(name, "ANSI_"); ok {
		if n == "936" {
			return 31
		}
		if c, err := strconv.Atoi(n); err == nil && c >= 1250 && c <= 1252 {
			return uint16(c - 1222)
		}
	}
	return 0
}

// dxfVersionMap DXF $ACADVER 版本串 → DWG 版本枚举。
var dxfVersionMap = map[string]container.DwgVersion{
	"AC1004": container.VerR9,
	"AC1006": container.VerR10,
	"AC1009": container.VerR11,
	"AC1012": container.VerR13,
	"AC1014": container.VerR14,
	"AC1015": container.VerR2000,
	"AC1018": container.VerR2004,
	"AC1021": container.VerR2007,
	"AC1024": container.VerR2010,
	"AC1027": container.VerR2013,
	"AC1032": container.VerR2018,
}

// dxfVersionEnum 版本串转枚举；未知值保守归 R2018（仅影响 Version() 展示）。
func dxfVersionEnum(ver string) container.DwgVersion {
	if v, ok := dxfVersionMap[strings.TrimSpace(ver)]; ok {
		return v
	}
	return container.VerR2018
}

// ---- TABLES 段 ----

// parseTables 符号表区：仅 LAYER 表需要（名字+颜色），其余表跳到 ENDTAB。
func (st *dxfState) parseTables() error {
	for {
		p, ok, err := st.lexer.next()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if p.code != 0 {
			continue
		}
		switch p.strValue() {
		case "ENDSEC":
			return nil
		case "TABLE":
			name, err := st.readSectionName()
			if err != nil {
				return err
			}
			if name == "LAYER" {
				if err := st.parseLayerTable(); err != nil {
					return err
				}
			} else if err := st.skipToEndTab(); err != nil {
				return err
			}
		}
	}
}

// skipToEndTab 消费到 (0, ENDTAB)。
func (st *dxfState) skipToEndTab() error {
	for {
		p, ok, err := st.lexer.next()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if p.code == 0 && (p.strValue() == "ENDTAB" || p.strValue() == "ENDSEC") {
			return nil
		}
	}
}

// parseLayerTable LAYER 表记录：句柄（组码 5）、名字（2）、ACI 颜色（62，
// 负值=图层关闭取绝对值）、真彩色（420）。渲染与颜色继承依赖该表。
func (st *dxfState) parseLayerTable() error {
	for {
		rec, err := st.readRecord()
		if err != nil {
			return err
		}
		if rec == nil {
			return nil // ENDSEC/EOF（readRecord 已回吐该边界对）
		}
		switch rec.typ {
		case "ENDTAB":
			return nil
		case "LAYER":
			h := st.recordHandle(rec)
			name := rec.str(2)
			st.layerByName[name] = h
			lc := layerColor{index: 7, name: name} // DXF 缺省 ACI 白
			if c, ok := rec.intVal(62); ok {
				idx := c
				if idx < 0 {
					idx = -idx
				}
				if idx > 0 && idx <= 257 {
					lc.index = uint16(idx)
				}
			}
			if tc, ok := rec.intVal(420); ok {
				lc.trueColor = uint32(tc) & 0x00FFFFFF
				lc.hasTrue = lc.trueColor != 0
			}
			st.doc.layerColors[h] = lc
		}
	}
}

// readRecord 读取一个 (0, 类型) 开头的记录（实体/表记录/块定义通用），
// 返回 nil 表示遇到 ENDSEC/EOF（不消费该结束对——由调用方处理边界）。
func (st *dxfState) readRecord() (*dxfRec, error) {
	var head dxfPair
	for {
		p, ok, err := st.lexer.next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, nil
		}
		if p.code == 0 {
			v := p.strValue()
			if v == "ENDSEC" || v == "EOF" {
				st.lexer.pushBack(p)
				return nil, nil
			}
			head = p
			break
		}
		// 记录边界外的散落对：容忍（如上一记录的收尾 SEQEND 标记）
	}
	rec := &dxfRec{typ: head.strValue()}
	for {
		p, ok, err := st.lexer.next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return rec, nil
		}
		if p.code == 0 {
			st.lexer.pushBack(p)
			return rec, nil
		}
		rec.pairs = append(rec.pairs, p)
	}
}

// dxfRec 一个 0 组码记录：类型名与全部非 0 组码对。
type dxfRec struct {
	typ   string
	pairs []dxfPair
}

// first 返回首个指定组码的对。
func (r *dxfRec) first(code int) (dxfPair, bool) {
	for _, p := range r.pairs {
		if p.code == code {
			return p, true
		}
	}
	return dxfPair{}, false
}

// all 返回指定组码的全部对（顶点/节点等重复组码场景）。
func (r *dxfRec) all(code int) []dxfPair {
	var out []dxfPair
	for _, p := range r.pairs {
		if p.code == code {
			out = append(out, p)
		}
	}
	return out
}

// str 首个字符串类组码值（去两端空白；DXF 写出端常填充对齐空格）。
func (r *dxfRec) str(code int) string {
	if p, ok := r.first(code); ok {
		return strings.TrimSpace(p.strValue())
	}
	return ""
}

// strAll 指定组码的字符串值序列（MTEXT 的 3+1 分段拼接用）。
func (r *dxfRec) strAll(code int) []string {
	var out []string
	for _, p := range r.all(code) {
		out = append(out, p.strValue())
	}
	return out
}

// floatVal 首个浮点组码值。
func (r *dxfRec) floatVal(code int) (float64, bool) {
	if p, ok := r.first(code); ok {
		return p.floatValue(), true
	}
	return 0, false
}

// intVal 首个整数组码值（浮点截断；DXF 整型常写成 "     0"）。
func (r *dxfRec) intVal(code int) (int64, bool) {
	if p, ok := r.first(code); ok {
		return int64(p.floatValue()), true
	}
	return 0, false
}

// point2 取 (code, code+10) 平面点。
func (r *dxfRec) point2(code int) entity.Point2 {
	x, _ := r.floatVal(code)
	y, _ := r.floatVal(code + 10)
	return entity.Point2{x, y}
}

// point3 取 (code, code+10, code+20) 空间点。
func (r *dxfRec) point3(code int) entity.Point3 {
	x, _ := r.floatVal(code)
	y, _ := r.floatVal(code + 10)
	z, _ := r.floatVal(code + 20)
	return entity.Point3{x, y, z}
}

// hexHandle 句柄类组码值（十六进制字符串，二进制编码同样是 hex 文本）。
func (r *dxfRec) hexHandle(code int) (uint64, bool) {
	if p, ok := r.first(code); ok {
		if h, err := strconv.ParseUint(strings.TrimSpace(p.strValue()), 16, 64); err == nil {
			return h, true
		}
	}
	return 0, false
}

// recordHandle 记录句柄：组码 5 缺失时分配合成句柄（保持 Document 内
// 句柄唯一性不变式）。
func (st *dxfState) recordHandle(rec *dxfRec) uint64 {
	if h, ok := rec.hexHandle(5); ok && h != 0 {
		if h >= st.nextHandle {
			st.nextHandle = h + 1
		}
		return h
	}
	h := st.nextHandle
	st.nextHandle++
	return h
}

// ---- BLOCKS 段 ----

// isModelSpaceBlockName 模型空间布局块名（R2000+ 与 R12 两种拼写）。
func isModelSpaceBlockName(name string) bool {
	return strings.EqualFold(name, "*Model_Space") || strings.EqualFold(name, "$Model_Space")
}

// isPaperSpaceBlockName 图纸空间布局块名。
func isPaperSpaceBlockName(name string) bool {
	return strings.HasPrefix(strings.ToLower(name), "*paper_space") ||
		strings.EqualFold(name, "$Paper_Space")
}

// parseBlocks 块定义区：登记块名 → 句柄；*Model_Space 内容直接进模型
// 空间，图纸空间布局跳过，普通块内容按 owner 归入 blocks。
func (st *dxfState) parseBlocks() error {
	for {
		rec, err := st.readRecord()
		if err != nil {
			return err
		}
		if rec == nil {
			return nil
		}
		if rec.typ != "BLOCK" {
			continue // ENDBLK 等结构标记
		}
		name := rec.str(2)
		h := st.recordHandle(rec)
		if name != "" {
			st.blockByName[name] = h
		}
		// 块定义元数据（真名 + 基点）以 BLOCK_HEADER 内部对象形态登记，
		// 与 DWG/JSON 来源统一：DXF 写出侧按句柄从 internalObjects 取
		// 名称与 base_pt，符号名/块基点三个来源同路消费。
		bg := &object.ObjGeneric{Name: "BLOCK_HEADER", Handle: h,
			Fields: []object.ObjField{{Key: "name", Val: name}}}
		if bp, ok := rec.floatVal(10); ok {
			bpy, _ := rec.floatVal(20)
			bpz, _ := rec.floatVal(30)
			bg.Fields = append(bg.Fields, object.ObjField{Key: "base_pt", Val: []float64{bp, bpy, bpz}})
		}
		st.doc.internalObjects[h] = bg
		// 收集块内容直到 ENDBLK
		var inModelSpace, inPaperSpace bool
		if isModelSpaceBlockName(name) {
			inModelSpace = true
		} else if isPaperSpaceBlockName(name) {
			inPaperSpace = true
		}
		for {
			erec, err := st.readRecord()
			if err != nil {
				return err
			}
			if erec == nil {
				return nil
			}
			if erec.typ == "ENDBLK" {
				st.recordHandle(erec) // ENDBLK 也可能带句柄，推进合成器
				break
			}
			switch {
			case inPaperSpace:
				st.skipSpaceEntity(erec) // 图纸空间布局不参与模型空间渲染
			case inModelSpace:
				st.buildSpaceEntity(erec, 2, 0)
			default:
				st.buildBlockEntity(erec, h)
			}
		}
	}
}

// ---- ENTITIES 段与实体构建 ----

// parseEntitiesSpace 顶级实体区：全部按模型空间实体处理（67=1 的图纸
// 空间实体在 buildEntity 内统一跳过，与既有 DWG 解析口径一致）。
func (st *dxfState) parseEntitiesSpace() error {
	for {
		rec, err := st.readRecord()
		if err != nil {
			return err
		}
		if rec == nil {
			// ENDSEC/EOF：清理回吐的边界对（外层主循环忽略 ENDSEC）
			st.lexer.popBack()
			return nil
		}
		st.buildSpaceEntity(rec, 2, 0)
	}
}

// skipSpaceEntity 图纸空间实体：仅消费句柄合成器状态，不构建实体。
func (st *dxfState) skipSpaceEntity(rec *dxfRec) {
	st.recordHandle(rec)
	st.unsupported++
}

// buildSpaceEntity 构建一个指定空间归属的实体（mode 2=模型空间）。
func (st *dxfState) buildSpaceEntity(rec *dxfRec, mode uint8, owner uint64) {
	if ent := st.buildEntity(rec, mode, owner); ent != nil {
		st.doc.classify(ent)
	}
}

// buildBlockEntity 块内实体：mode=0 且 owner 指向块定义句柄，
// classify 会按 owner 归入 blocks。
func (st *dxfState) buildBlockEntity(rec *dxfRec, blockHandle uint64) {
	if ent := st.buildEntity(rec, 0, blockHandle); ent != nil {
		st.doc.classify(ent)
	}
}

// dxfSupportedEntities DXF 读取支持的实体类型集合（对照测试口径）。
var dxfSupportedEntities = map[string]bool{
	"LINE": true, "CIRCLE": true, "ARC": true, "POINT": true, "ELLIPSE": true,
	"TEXT": true, "MTEXT": true, "LWPOLYLINE": true, "POLYLINE": true,
	"VERTEX": true, "INSERT": true, "ATTRIB": true, "SOLID": true,
	"TRACE": true, "3DFACE": true, "RAY": true, "XLINE": true, "SPLINE": true,
	// 批次 R：复杂实体（DIMENSION 各型/HATCH/LEADER/MULTILEADER/MLINE/
	// TOLERANCE/VIEWPORT），组码构造对照 in_dxf.c + DWG 侧同名解码器
	"DIMENSION": true, "HATCH": true, "LEADER": true, "MULTILEADER": true,
	"MLINE": true, "TOLERANCE": true, "VIEWPORT": true,
	// 极限批次 A：ATTDEF 属性定义（与 ATTRIB 同构 + prompt 组码 3）
	"ATTDEF": true,
	// 极限批次 A：ACIS 系（SAT 文本行拼接建模，几何内核不在解析范围）
	"REGION": true, "3DSOLID": true, "BODY": true,
}

// buildEntity 单实体构建主分发：返回 nil 表示该类型不构建（SEQEND 结构
// 标记按宿主归属或静默跳过、其余未支持类型计数后丢弃）。
func (st *dxfState) buildEntity(rec *dxfRec, mode uint8, owner uint64) any {
	// 67=1：图纸空间实体，模型空间渲染不涉及
	if sp, ok := rec.intVal(67); ok && sp == 1 {
		st.unsupported++
		return nil
	}
	// 结构标记先行处理（不推进合成句柄，避免挤占真实句柄空间）
	if rec.typ == "SEQEND" {
		return st.buildSeqend(rec, mode, owner)
	}
	if !dxfSupportedEntities[rec.typ] {
		st.unsupported++
		return nil
	}

	base := st.dxfBase(rec, mode, owner)
	var ent any
	switch rec.typ {
	case "LINE":
		ent = &entity.EntLine{BaseEntity: *base, Start: rec.point3(10), End: rec.point3(11)}
	case "CIRCLE":
		radius, _ := rec.floatVal(40)
		ent = &entity.EntCircle{BaseEntity: *base, Center: rec.point3(10), Radius: radius}
	case "ARC":
		radius, _ := rec.floatVal(40)
		a0, _ := rec.floatVal(50)
		a1, _ := rec.floatVal(51)
		ent = &entity.EntArc{BaseEntity: *base, Center: rec.point3(10), Radius: radius,
			AngleStart: a0 * math.Pi / 180, AngleEnd: a1 * math.Pi / 180}
	case "POINT":
		rot, _ := rec.floatVal(50)
		ent = &entity.EntPoint{BaseEntity: *base, Location: rec.point3(10), Rotation: rot * math.Pi / 180}
	case "ELLIPSE":
		ratio, _ := rec.floatVal(40)
		sa, _ := rec.floatVal(41)
		ea, _ := rec.floatVal(42)
		ent = &entity.EntEllipse{BaseEntity: *base, Center: rec.point3(10),
			MajorAxis: rec.point3(11), Ratio: ratio,
			StartAng: sa, EndAng: ea}
	case "TEXT":
		ent = st.buildText(rec, base)
	case "MTEXT":
		ent = st.buildMText(rec, base)
	case "LWPOLYLINE":
		ent = st.buildLwPolyline(rec, base)
	case "POLYLINE":
		ent = st.buildPolyline(rec, base)
	case "VERTEX":
		ent = st.buildVertex(rec, base)
	case "INSERT":
		ent = st.buildInsert(rec, base)
	case "ATTRIB":
		ent = st.buildAttrib(rec, base)
	case "ATTDEF":
		ent = st.buildAttdef(rec, base)
	case "SOLID", "TRACE":
		elevation, _ := rec.floatVal(30)
		ent = &entity.EntSolid{BaseEntity: *base,
			P1: rec.point2(10), P2: rec.point2(11), P3: rec.point2(12), P4: rec.point2(13),
			Elevation: elevation, Trace: rec.typ == "TRACE"}
	case "3DFACE":
		ent = &entity.EntFace3d{BaseEntity: *base,
			P1: rec.point3(10), P2: rec.point3(11), P3: rec.point3(12), P4: rec.point3(13)}
	case "RAY", "XLINE":
		ent = &entity.EntRay{BaseEntity: *base, Start: rec.point3(10), UnitVector: rec.point3(11),
			Xline: rec.typ == "XLINE"}
	case "SPLINE":
		ent = st.buildSpline(rec, base)
	// ---- 批次 R：复杂实体 ----
	case "DIMENSION":
		ent = st.buildDimension(rec, base)
	case "HATCH":
		ent = st.buildHatch(rec, base)
	case "LEADER":
		ent = st.buildLeader(rec, base)
	case "MULTILEADER":
		ent = st.buildMLeader(rec, base)
	case "MLINE":
		ent = st.buildMLine(rec, base)
	case "TOLERANCE":
		ent = st.buildTolerance(rec, base)
	case "VIEWPORT":
		ent = st.buildViewport(rec, base)
	case "REGION", "3DSOLID", "BODY":
		ent = st.buildAcis(rec, base)
	}
	if ent == nil {
		return nil
	}
	return ent
}

// dxfBase 公共字段（句柄/图层/颜色/归属）。
func (st *dxfState) dxfBase(rec *dxfRec, mode uint8, owner uint64) *entity.BaseEntity {
	if owner == 0 {
		if h, ok := rec.hexHandle(330); ok {
			owner = h
		}
	}
	return &entity.BaseEntity{
		Handle: st.recordHandle(rec),
		Color:  dxfEntityColor(rec),
		Layer:  st.layerHandle(rec.str(8)),
		Owner:  owner,
		Mode:   mode,
	}
}

// dxfEntityColor 实体颜色：62 ACI 索引与 420 真彩色（420 优先级更高，
// 与渲染 entityColor 的取色顺序一致）。
func dxfEntityColor(rec *dxfRec) entity.EntColor {
	var c entity.EntColor
	if v, ok := rec.intVal(62); ok {
		idx := v
		if idx < 0 {
			idx = -idx
		}
		if idx > 0 && idx <= 257 {
			c.Index = uint16(idx)
			c.HasIndex = true
		}
	}
	if v, ok := rec.intVal(420); ok && v != 0 {
		c.TrueColor = uint32(v) & 0x00FFFFFF
		c.HasTrue = true
	}
	return c
}

// layerHandle 图层名 → LAYER 表句柄；未知名字兜底注册合成图层，
// 保证实体 layer 引用总能落到 layerColors（渲染按图层取色的前提）。
func (st *dxfState) layerHandle(name string) uint64 {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "0"
	}
	if h, ok := st.layerByName[name]; ok {
		return h
	}
	h := st.nextHandle
	st.nextHandle++
	st.layerByName[name] = h
	st.doc.layerColors[h] = layerColor{index: 7}
	return h
}

// buildText TEXT：文本（1）、字高（40）、插入点（10）、旋转（50，度）、
// 对齐点（11）与对齐模式（72/73）。
func (st *dxfState) buildText(rec *dxfRec, base *entity.BaseEntity) any {
	t := &entity.EntText{BaseEntity: *base}
	t.Text = rec.str(1)
	t.Insertion = rec.point3(10)
	if h, ok := rec.floatVal(40); ok {
		t.Height = h
	} else {
		t.Height = 1
	}
	if rot, ok := rec.floatVal(50); ok {
		t.Rotation = rot * math.Pi / 180
	}
	if v, ok := rec.intVal(72); ok {
		t.HAlign = uint16(v)
	}
	if v, ok := rec.intVal(73); ok {
		t.VAlign = uint16(v)
	}
	if v, ok := rec.intVal(71); ok {
		t.Gen = uint16(v)
	}
	// 有对齐模式（非 0）时 11 组码为第二对齐点
	if (t.HAlign != 0 || t.VAlign != 0) && len(rec.all(11)) > 0 {
		p := rec.point2(11)
		t.AlignPt = &p
	}
	return t
}

// buildMText MTEXT：分段文本 3*（前置段）+1（末段）拼接为原始富文本，
// 40 字高、41 矩形宽、71 附着点。
func (st *dxfState) buildMText(rec *dxfRec, base *entity.BaseEntity) any {
	m := &entity.EntMText{BaseEntity: *base}
	var sb strings.Builder
	for _, s := range rec.strAll(3) {
		sb.WriteString(s)
	}
	sb.WriteString(rec.str(1))
	m.Text = sb.String()
	m.Insertion = rec.point3(10)
	if h, ok := rec.floatVal(40); ok {
		m.TextHeight = h
	} else {
		m.TextHeight = 1
	}
	if w, ok := rec.floatVal(41); ok {
		m.RectWidth = w
	}
	if v, ok := rec.intVal(71); ok {
		m.Attachment = uint16(v)
	}
	// 行距系数（DXF 44 linespace_factor，R2000+ DXF 才写出；缺省 0=未存储）
	if f, ok := rec.floatVal(44); ok {
		m.LineFactor = f
	}
	return m
}

// buildLwPolyline LWPOLYLINE：顶点按 10/20 对序收集，42 凸度关联最近
// 顶点（DXF 逐顶点交错写出），43 常量宽、38 标高、70 标志。
func (st *dxfState) buildLwPolyline(rec *dxfRec, base *entity.BaseEntity) any {
	lw := &entity.EntLwPolyline{BaseEntity: *base}
	if v, ok := rec.intVal(70); ok {
		lw.Flags = uint16(v)
	}
	if w, ok := rec.floatVal(43); ok {
		lw.ConstWidth = w
	}
	if e, ok := rec.floatVal(38); ok {
		lw.Elevation = e
	}
	for _, p := range rec.pairs {
		switch p.code {
		case 10:
			lw.Vertices = append(lw.Vertices, entity.Point2{X: p.floatValue()})
		case 20:
			if len(lw.Vertices) > 0 {
				lw.Vertices[len(lw.Vertices)-1].Y = p.floatValue()
			}
		case 42:
			for len(lw.Bulges) < len(lw.Vertices)-1 {
				lw.Bulges = append(lw.Bulges, 0)
			}
			lw.Bulges = append(lw.Bulges, p.floatValue())
		}
	}
	for len(lw.Bulges) < len(lw.Vertices) {
		lw.Bulges = append(lw.Bulges, 0)
	}
	return lw
}

// buildPolyline POLYLINE（R12 布局）：70 标志区分 2D/3D/面网格/多面
// 网格多段线（bit3=3D、bit4=MESH、bit6=PFACE，与 DWG 侧类型分布对齐），
// 顶点由后续 VERTEX 记录按文件顺序回填。渲染依赖 ownedHandles 句柄表。
func (st *dxfState) buildPolyline(rec *dxfRec, base *entity.BaseEntity) any {
	flags, _ := rec.intVal(70)
	switch {
	case flags&8 != 0:
		p3 := &entity.EntPolyline3d{BaseEntity: *base, Flags70: uint8(flags & 0xFF)}
		st.curPolylineHost = p3
		return p3
	case flags&64 != 0:
		pf := &entity.EntPolylinePface{BaseEntity: *base}
		if nv, ok := rec.intVal(71); ok {
			pf.NumVertices = int(nv)
		}
		if nf, ok := rec.intVal(72); ok {
			pf.NumFaces = int(nf)
		}
		st.curPolylineHost = pf
		return pf
	case flags&16 != 0:
		m := &entity.EntPolylineMesh{BaseEntity: *base, Flags: uint16(flags & 0xFFFF)}
		if v, ok := rec.intVal(71); ok {
			m.MVertexCount = uint16(v)
		}
		if v, ok := rec.intVal(72); ok {
			m.NVertexCount = uint16(v)
		}
		if v, ok := rec.intVal(73); ok {
			m.MDensity = uint16(v)
		}
		if v, ok := rec.intVal(74); ok {
			m.NDensity = uint16(v)
		}
		if v, ok := rec.intVal(75); ok {
			m.CurveType = uint16(v)
		}
		st.curPolylineHost = m
		return m
	default:
		p2 := &entity.EntPolyline2d{BaseEntity: *base, Flags: uint16(flags & 0xFFFF)}
		st.curPolylineHost = p2
		return p2
	}
}

// buildVertex VERTEX：位置（10）、凸度（42）、标志（70）；按宿主 POLYLINE
// 类型构造对应顶点实体，归属句柄改为宿主（mode=0），并回填宿主句柄表
// ——与 DWG 侧 VERTEX 的对象归属语义一致（blocks[宿主句柄]）。
func (st *dxfState) buildVertex(rec *dxfRec, base *entity.BaseEntity) any {
	host := st.curPolylineHost
	if h, ok := rec.hexHandle(330); ok && h != 0 {
		// R2000+：330 owner 指回宿主，按句柄覆盖文件顺序归属
		if e := st.doc.entityByHandle[h]; e != nil {
			switch e.(type) {
			case *entity.EntPolyline2d, *entity.EntPolyline3d, *entity.EntPolylinePface, *entity.EntPolylineMesh:
				host = e
			}
		}
	}
	base.Mode = 0 // 顶点实体归属宿主对象，不直接进模型空间
	if host == nil {
		// 孤立 VERTEX：无宿主可归属，退回模型空间直挂（渲染不消费）
		base.Mode = 2
		return &entity.EntVertex2d{BaseEntity: *base, Position: rec.point3(10), Bulge: bulgeOf(rec)}
	}
	base.Owner = entity.EntityBase(host).Handle
	switch h := host.(type) {
	case *entity.EntPolyline3d:
		v := &entity.EntVertex3d{BaseEntity: *base, Position: rec.point3(10)}
		if f, ok := rec.intVal(70); ok {
			v.Flags = uint8(f & 0xFF)
		}
		h.OwnedHandles = append(h.OwnedHandles, v.Handle)
		return v
	case *entity.EntPolylineMesh:
		v := &entity.EntVertexPface{BaseEntity: *base, Position: rec.point3(10)}
		if f, ok := rec.intVal(70); ok {
			v.Flag = uint8(f & 0xFF)
		}
		h.OwnedHandles = append(h.OwnedHandles, v.Handle)
		return v
	case *entity.EntPolylinePface:
		// 子类标记判别（in_dxf UPGRADE_ENTITY 口径）：AcDbFaceRecord 为
		// 面记录（71-74 顶点索引），AcDbPolyFaceMeshVertex 为定位顶点
		if dxfRecHasSubclass(rec, "AcDbFaceRecord") {
			f := &entity.EntVertexPfaceFace{BaseEntity: *base, Flag: 128}
			for i := 0; i < 4; i++ {
				if v, ok := rec.intVal(71 + i); ok {
					f.Vertind[i] = int32(int16(v))
				}
			}
			return f
		}
		v := &entity.EntVertexPface{BaseEntity: *base, Position: rec.point3(10)}
		if f, ok := rec.intVal(70); ok {
			v.Flag = uint8(f & 0xFF)
		}
		return v
	case *entity.EntPolyline2d:
		v := &entity.EntVertex2d{BaseEntity: *base, Position: rec.point3(10), Bulge: bulgeOf(rec)}
		if f, ok := rec.intVal(70); ok {
			v.Flags = uint16(f)
		}
		h.OwnedHandles = append(h.OwnedHandles, v.Handle)
		return v
	default:
		v := &entity.EntVertex2d{BaseEntity: *base, Position: rec.point3(10), Bulge: bulgeOf(rec)}
		if f, ok := rec.intVal(70); ok {
			v.Flags = uint16(f)
		}
		return v
	}
}

// dxfRecHasSubclass 记录是否携带指定子类标记（100 组码值匹配）。
func dxfRecHasSubclass(rec *dxfRec, name string) bool {
	for _, p := range rec.pairs {
		if p.code == 100 && p.strValue() == name {
			return true
		}
	}
	return false
}

// bulgeOf VERTEX 凸度组码。
func bulgeOf(rec *dxfRec) float64 {
	b, _ := rec.floatVal(42)
	return b
}

// buildInsert INSERT：块名（2）解析为块定义句柄，插入点（10）、缩放
// （41/42/43，缺省 1）、旋转（50，度）、66 属性跟随标志。
func (st *dxfState) buildInsert(rec *dxfRec, base *entity.BaseEntity) any {
	ins := &entity.EntInsert{BaseEntity: *base,
		Position: rec.point3(10),
		Scale:    entity.Point3{X: 1, Y: 1, Z: 1}}
	if name := rec.str(2); name != "" {
		ins.BlockHeader = st.blockHandle(name)
	}
	if v, ok := rec.floatVal(41); ok {
		ins.Scale.X = v
	}
	if v, ok := rec.floatVal(42); ok {
		ins.Scale.Y = v
	}
	if v, ok := rec.floatVal(43); ok {
		ins.Scale.Z = v
	}
	if rot, ok := rec.floatVal(50); ok {
		ins.Rotation = rot * math.Pi / 180
	}
	if attribs, ok := rec.intVal(66); ok && attribs == 1 {
		st.curInsert = ins
	} else {
		st.curInsert = nil
	}
	return ins
}

// buildAttrib ATTRIB：属性文本（1）、标签（2）、插入点与字高/旋转；
// 归属最近的 INSERT（66=1 序列或 330 owner），归属语义与 DWG 侧一致
// （mode=0、owner=块参照句柄，进 blocks 而非模型空间直挂）。
func (st *dxfState) buildAttrib(rec *dxfRec, base *entity.BaseEntity) any {
	a := &entity.EntAttrib{BaseEntity: *base}
	a.Text = rec.str(1)
	a.Tag = rec.str(2)
	a.Insertion = rec.point3(10)
	if h, ok := rec.floatVal(40); ok {
		a.Height = h
	} else {
		a.Height = 1
	}
	if rot, ok := rec.floatVal(50); ok {
		a.Rotation = rot * math.Pi / 180
	}
	if v, ok := rec.intVal(72); ok {
		a.HAlign = uint16(v)
	}
	if v, ok := rec.intVal(74); ok {
		a.VAlign = uint16(v)
	}
	// 关联宿主 INSERT：330 owner 优先，否则取 66=1 序列的最近 INSERT
	host := st.curInsert
	if h, ok := rec.hexHandle(330); ok && h != 0 {
		if ins, ok2 := st.doc.entityByHandle[h].(*entity.EntInsert); ok2 {
			host = ins
		}
	}
	if host != nil {
		a.Mode = 0
		a.Owner = host.Handle
		host.Attribs = append(host.Attribs, a.Handle)
	}
	return a
}

// buildSeqend SEQEND 序列终止标记的建模口径（极限批次 A 确认）：
// DWG 侧 SEQEND 解析为 entBlockLike（owner=POLYLINE/INSERT 宿主，进
// blocks 归宿主句柄），DXF 读侧对齐——有宿主时建模归宿主，无宿主的
// 顶层孤立 SEQEND（文件尾散落标记等）不建模，静默跳过。
// SEQEND 同时终止宿主序列：处理完后清空 POLYLINE 聚合与 INSERT 属性
// 游标，其后散落的孤立 VERTEX/ATTRIB 不再误归属（DXF 格式语义）。
// 宿主判定：330 owner 优先（R2000+），否则取最近 POLYLINE 聚合宿主
// /66=1 属性序列 INSERT；宿主不在实体表时同样视为孤立。
func (st *dxfState) buildSeqend(rec *dxfRec, mode uint8, owner uint64) any {
	defer func() {
		st.curPolylineHost = nil
		st.curInsert = nil
	}()
	var host any
	if h, ok := rec.hexHandle(330); ok && h != 0 {
		host = st.doc.entityByHandle[h]
	}
	if host == nil && st.curPolylineHost != nil {
		host = st.curPolylineHost
	}
	// curInsert 为具体指针类型，须先判 nil 再装箱，避免 nil 指针包装
	// 成非 nil 接口导致宿主解引用崩溃
	if host == nil && st.curInsert != nil {
		host = st.curInsert
	}
	if host == nil {
		return nil
	}
	base := st.dxfBase(rec, 0, entity.EntityBase(host).Handle)
	return &entity.EntBlockLike{BaseEntity: *base}
}

// buildAttdef ATTDEF 属性定义：DXF 组码与 ATTRIB 同构（1 默认值/2 标签/
// 10 插入点/40 字高/50 旋转/71 生成/72 水平/74 垂直对齐），另带 3 提示串
// （ATTRIB 无此组码）。定义类建模：不进 INSERT 的 attribs 引用链（那是
// ATTRIB 的归属语义），按 330 owner（块定义句柄）归入 blocks；顶层
// ATTDEF 按 dxfBase 的 owner 兜底逻辑处理（渲染不消费，与 DWG 侧
// ATTDEF→entAttrib 同构口径一致）。
func (st *dxfState) buildAttdef(rec *dxfRec, base *entity.BaseEntity) any {
	a := &entity.EntAttrib{BaseEntity: *base}
	a.Text = rec.str(1)
	a.Tag = rec.str(2)
	a.Prompt = rec.str(3)
	a.Insertion = rec.point3(10)
	if h, ok := rec.floatVal(40); ok {
		a.Height = h
	} else {
		a.Height = 1
	}
	if rot, ok := rec.floatVal(50); ok {
		a.Rotation = rot * math.Pi / 180
	}
	if v, ok := rec.intVal(72); ok {
		a.HAlign = uint16(v)
	}
	if v, ok := rec.intVal(74); ok {
		a.VAlign = uint16(v)
	}
	return a
}

// buildSpline SPLINE：70 标志、71 阶数、节点（40*）、控制点（10*）、
// 拟合点（11*）、拟合容差（43）。scenario 按拟合点有无推断（渲染仅
// 用控制点）。
func (st *dxfState) buildSpline(rec *dxfRec, base *entity.BaseEntity) any {
	s := &entity.EntSpline{BaseEntity: *base, Scenario: 1}
	if f, ok := rec.intVal(70); ok {
		s.Closed = f&1 != 0
		s.Periodic = f&2 != 0
		s.Rational = f&4 != 0
	}
	if d, ok := rec.intVal(71); ok {
		s.Degree = uint32(d)
	}
	if t, ok := rec.floatVal(43); ok {
		s.FitTolerance = t
	}
	for _, p := range rec.all(40) {
		s.Knots = append(s.Knots, p.floatValue())
	}
	// 点分量按 (code, code+10, code+20) 三元组顺序配对
	collect3 := func(code int) []entity.Point3 {
		var pts []entity.Point3
		cur := entity.Point3{}
		stage := 0
		for _, p := range rec.pairs {
			switch p.code {
			case code:
				cur.X = p.floatValue()
				stage = 1
			case code + 10:
				if stage == 1 {
					cur.Y = p.floatValue()
					stage = 2
				}
			case code + 20:
				if stage == 2 {
					cur.Z = p.floatValue()
					pts = append(pts, cur)
					stage = 0
				}
			}
		}
		return pts
	}
	s.ControlPoints = collect3(10)
	s.FitPoints = collect3(11)
	if len(s.FitPoints) > 0 {
		s.Scenario = 2
	}
	return s
}

// blockHandle 块名 → 块定义句柄；未知名字（前向引用或孤立 INSERT）
// 兜底注册合成块句柄，保证 INSERT.blockHeader 指向有效归属。
func (st *dxfState) blockHandle(name string) uint64 {
	name = strings.TrimSpace(name)
	if h, ok := st.blockByName[name]; ok {
		return h
	}
	h := st.nextHandle
	st.nextHandle++
	st.blockByName[name] = h
	st.doc.blocks[h] = nil
	return h
}

// ---- 批次 R：复杂实体构造（组码对照 in_dxf.c + dwg.spec DXF 标注）----

// dxfDegToRad DXF 角度组码（度）→ 内部弧度存储。
func dxfDegToRad(deg float64) float64 { return deg * math.Pi / 180 }

// buildDimension DIMENSION：70 低 3 位分派 7 型（0=rotated、1=aligned、
// 2=ang2ln、3=diameter、4=radius、5=ang3pt、6=ordinate），公共段（10 def
// 点、11 文本中点+31 标高、70 flag、1 用户文字、71 附着、42 实测值、
// 51/53/54 角度、12 clone_ins）+ 子类段专属点（13/14/15/16）。ANG2LN 的
// 子类段 16（2RD）对应 DWG 位流尾部 2RD（解码侧 point16x/p16y 载体），
// 10 恒为 def 点（gold ex2000 h=43B 仲裁）；ORDINATE 的 13/14 为
// feature_location/leader_end（直接载入 point13/14，与 JSON 侧同口径）。
// DIMENSION 走显式组码字段，无 dimSpecificLayout 位流布局问题；
// dimstyle 名（组码 3）与匿名块名（组码 2）无句柄对应，不恢复句柄引用。
func (st *dxfState) buildDimension(rec *dxfRec, base *entity.BaseEntity) any {
	d := &entity.EntDimension{BaseEntity: *base}
	flag, _ := rec.intVal(70)
	d.DimFlag = uint8(flag & 0xFF)
	d.DimFlags = d.DimFlag // DXF 无独立 flag1 位流，审计口径以 flag 为准
	sub := d.DimFlag & 0x7
	// 公共段
	// 10 组码恒为 def 点（DWG 侧 point10 载体，含 ANG2LN——gold 仲裁：
	// ex2000 h=43B 的 DXF 10 与 gold xline2end_pt 一致，16 与 def_pt 一致）
	d.Point10 = rec.point3(10)
	d.TextMidpoint = rec.point3(11)
	if el, ok := rec.floatVal(31); ok {
		d.Elevation = el
		d.TextMidpoint.Z = el // text_midpt 的 z 分量即标高
	}
	d.UserText = rec.str(1)
	if v, ok := rec.intVal(71); ok {
		d.AttachmentPoint = uint16(v)
	}
	if v, ok := rec.floatVal(42); ok {
		d.ActualMeasurement = v
	}
	if v, ok := rec.floatVal(51); ok {
		d.HorizontalDir = dxfDegToRad(v)
	}
	if v, ok := rec.floatVal(53); ok {
		d.TextRotation = dxfDegToRad(v)
	}
	if v, ok := rec.floatVal(54); ok {
		d.InsertRotation = dxfDegToRad(v)
	}
	if v, ok := rec.floatVal(52); ok {
		d.ExtLineRotation = dxfDegToRad(v)
	}
	if v, ok := rec.floatVal(50); ok {
		d.DimRotation = dxfDegToRad(v)
	}
	d.Point13 = rec.point3(13)
	d.Point14 = rec.point3(14)
	if _, ok := rec.first(12); ok {
		d.InsertPoint = rec.point3(12)
		d.HasInsertPoint = true
	}
	// 子类段专属点
	if len(rec.all(15)) > 0 {
		d.Point15 = rec.point3(15)
		d.HasPoint15 = true
	}
	if sub == 2 && len(rec.all(16)) > 0 {
		// ANG2LN 子类段的 16（2RD，16/26）为 gold def_pt 载体（DWG 位流
		// 尾部 2RD，即解码侧 point16x/p16y 字段）
		if p, ok := rec.first(16); ok {
			d.Point16x = p.floatValue()
		}
		if p, ok := rec.first(26); ok {
			d.P16y = p.floatValue()
		}
		d.HasPoint16 = true
	}
	return d
}

// buildHatch HATCH：公共段（30 标高、210 挤出、2 图案名、70/71 标志、
// 75/76/52/41/77 图案参数、78 起定义线）+ 91 起的路径序列。路径按 92 flag
// 分派：bit1=多段线路径（72 凸度标志/73 闭合/93 顶点数/10+20(+42) 顶点），
// 否则边集路径（93 段数 + 直线段 10/20+11/21+72 或 弧段 10/20+40+50+51+73）。
// 尾段按 DXF 顺序：47 像素尺寸（92 flag bit2=has_derived 时）→ 98 种子点
// （98 计数 + 10/20 点对，其后 10/20 不再属路径）→ 渐变段（450 标志/451
// 保留/460 角度（度，入库转弧度）/461 偏移/452 单色标志/462 tint/453 色数/
// 463+63/421 逐色三元组/470 渐变名）。
// 边界细分点列与 DWG/JSON 来源同口径（多段线带凸度细分，边集直线段拼接，
// 弧段保留原始参数）。
func (st *dxfState) buildHatch(rec *dxfRec, base *entity.BaseEntity) any {
	h := &entity.EntHatch{BaseEntity: *base}
	if e, ok := rec.floatVal(30); ok {
		h.Elevation = e
	}
	h.Extrusion = rec.point3(210)
	name := rec.str(2)
	h.Name = name
	if v, ok := rec.intVal(70); ok {
		h.SolidFill = v&1 != 0
	}
	if v, ok := rec.intVal(71); ok {
		h.Associative = v&1 != 0
	}
	if v, ok := rec.intVal(75); ok {
		h.Style = uint16(v)
	}
	if v, ok := rec.intVal(76); ok {
		h.PatternType = uint16(v)
	}
	if v, ok := rec.floatVal(52); ok {
		h.Angle = v
	}
	if v, ok := rec.floatVal(41); ok {
		h.ScaleSpacing = v
	}
	if v, ok := rec.intVal(77); ok {
		h.DoubleFlag = v != 0
	}
	numPaths, _ := rec.intVal(91)
	// 图案定义线：53 角度、43/44 原点、45/46 偏移、79 划线数、49 划线值
	numDeflines, _ := rec.intVal(78)
	if numDeflines > 0 && numDeflines < 10_000 {
		var cur *entity.HatchDefLine
		var dashes int
		for _, p := range rec.pairs {
			switch p.code {
			case 53:
				if cur != nil {
					h.Deflines = append(h.Deflines, *cur)
				}
				h.Deflines = append(h.Deflines, entity.HatchDefLine{Angle: p.floatValue()})
				cur = &h.Deflines[len(h.Deflines)-1]
				dashes = 0
			case 43:
				if cur != nil {
					cur.Pt0.X = p.floatValue()
				}
			case 44:
				if cur != nil {
					cur.Pt0.Y = p.floatValue()
				}
			case 45:
				if cur != nil {
					cur.Offset.X = p.floatValue()
				}
			case 46:
				if cur != nil {
					cur.Offset.Y = p.floatValue()
				}
			case 79:
				if cur != nil {
					dashes = int(p.floatValue())
				}
			case 49:
				if cur != nil && dashes > 0 {
					cur.Dashes = append(cur.Dashes, p.floatValue())
					dashes--
				}
			}
		}
		if cur != nil {
			h.Deflines = append(h.Deflines, *cur)
		}
	}
	if numPaths <= 0 || numPaths > 100_000 {
		return h
	}
	// 路径序列游标解析：92 开启新路径；组码语义按路径类型分流——
	// 多段线路径：10/20 顶点、42 凸度、73 闭合、72 凸度标志；
	// 边集路径：72 段类型（1 直线/2 弧/3 椭圆弧/4 样条）、段内 10/20
	// 起点或圆心、11/21 第二端点或主轴端点、40 半径、50/51 起止角、73 逆时针。
	curPath := -1
	segStage := 0      // 边集路径当前段类型（0 待定）
	expect11Y := false // 21 为 11 的 y 分量
	var curPt entity.Point2
	// 尾段状态：98 后 10/20 归种子点；453 色数为 463/63/421 三元组游标
	// （对齐 in_dxf add_HATCH：num_seeds 与渐变组码由外层条件分流）
	inSeeds := false
	numSeeds := 0
	curColor := -1
	var seedX float64
	for _, p := range rec.pairs {
		if expect11Y && p.code == 21 {
			expect11Y = false
			pa := &h.Paths[curPath]
			if !pa.IsPolyline && len(pa.Segs) > 0 {
				seg := &pa.Segs[len(pa.Segs)-1]
				switch segStage {
				case 1:
					seg.Second.Y = p.floatValue()
				case 3:
					seg.Endpoint.Y = p.floatValue()
				}
			}
			continue
		}
		expect11Y = false
		switch p.code {
		case 92:
			flg := uint32(p.floatValue())
			h.Paths = append(h.Paths, entity.HatchPath{Flag: flg, IsPolyline: flg&2 != 0})
			// has_derived 为各路径 flag bit2 的或（in_dxf 口径），置位时
			// 读者期待随后的 47 像素尺寸
			h.HasDerived = h.HasDerived || flg&4 != 0
			curPath = len(h.Paths) - 1
			segStage = 0
		case 72:
			if curPath < 0 {
				continue
			}
			pa := &h.Paths[curPath]
			if pa.IsPolyline {
				pa.BulgesPresent = p.floatValue() != 0
			} else {
				pa.Segs = append(pa.Segs, entity.HatchSeg{CurveType: uint8(p.floatValue())})
				segStage = int(p.floatValue())
			}
		case 73:
			if curPath < 0 {
				continue
			}
			pa := &h.Paths[curPath]
			if pa.IsPolyline {
				pa.Closed = p.floatValue() != 0
			} else if len(pa.Segs) > 0 && (segStage == 2 || segStage == 3) {
				// 弧段 73 为逆时针标志（样条段 73 为 rational 位，此处同码）
				pa.Segs[len(pa.Segs)-1].Ccw = p.floatValue() != 0
			}
		case 93:
			if curPath >= 0 {
				h.Paths[curPath].NumSegsOrPaths = uint32(p.floatValue())
			}
		case 94:
			if curPath >= 0 && segStage == 4 && len(h.Paths[curPath].Segs) > 0 {
				h.Paths[curPath].Segs[len(h.Paths[curPath].Segs)-1].Degree = uint32(p.floatValue())
			}
		case 47:
			if h.HasDerived {
				h.PixelSize = p.floatValue()
			}
		case 98:
			numSeeds = int(p.floatValue())
			inSeeds = numSeeds > 0
		case 10:
			if inSeeds {
				seedX = p.floatValue()
				continue
			}
			if curPath < 0 {
				continue
			}
			curPt.X = p.floatValue()
		case 20:
			if inSeeds {
				if len(h.Seeds) < numSeeds {
					h.Seeds = append(h.Seeds, entity.Point2{X: seedX, Y: p.floatValue()})
				}
				continue
			}
			if curPath < 0 {
				continue
			}
			curPt.Y = p.floatValue()
			pa := &h.Paths[curPath]
			if pa.IsPolyline {
				pa.PolyVerts = append(pa.PolyVerts, entity.HatchPolyVert{P: curPt})
				continue
			}
			if len(pa.Segs) > 0 {
				seg := &pa.Segs[len(pa.Segs)-1]
				switch segStage {
				case 1: // 直线段第一端点
					seg.First = curPt
				case 2, 3: // 弧/椭圆弧圆心
					seg.Center = curPt
				}
			}
		case 11:
			if curPath < 0 {
				continue
			}
			pa := &h.Paths[curPath]
			if pa.IsPolyline {
				continue
			}
			// 边集路径：11 为第二端点（直线）或主轴端点（椭圆弧）的 x 分量
			if len(pa.Segs) > 0 {
				seg := &pa.Segs[len(pa.Segs)-1]
				switch segStage {
				case 1:
					seg.Second.X = p.floatValue()
					expect11Y = true
				case 3:
					seg.Endpoint.X = p.floatValue()
					expect11Y = true
				}
			}
		case 40:
			if curPath < 0 || h.Paths[curPath].IsPolyline || len(h.Paths[curPath].Segs) == 0 {
				continue
			}
			seg := &h.Paths[curPath].Segs[len(h.Paths[curPath].Segs)-1]
			if segStage == 2 {
				seg.Radius = p.floatValue()
			}
		case 50, 51:
			if curPath < 0 || h.Paths[curPath].IsPolyline || len(h.Paths[curPath].Segs) == 0 {
				continue
			}
			seg := &h.Paths[curPath].Segs[len(h.Paths[curPath].Segs)-1]
			if segStage == 2 || segStage == 3 {
				if p.code == 50 {
					seg.StartAng = p.floatValue()
				} else {
					seg.EndAng = p.floatValue()
				}
			}
		case 42:
			// 多段线路径顶点凸度（DXF 逐顶点交错；缺省 0）
			if curPath < 0 {
				continue
			}
			pa := &h.Paths[curPath]
			if pa.IsPolyline && len(pa.PolyVerts) > 0 {
				pa.PolyVerts[len(pa.PolyVerts)-1].Bulge = p.floatValue()
			}
		// ---- 渐变填充段（R2004+；组码对照 dwg2.spec/dwg.spec
		// _HATCH_gradientfill 与 in_dxf add_HATCH 分支）----
		case 450:
			h.IsGradientFill = uint32(p.floatValue())
		case 451:
			h.Reserved = uint32(p.floatValue())
		case 452:
			h.SingleColorGradient = uint32(p.floatValue())
		case 453:
			curColor = -1 // 色数声明后三元组游标归零
		case 460:
			// DXF 渐变角度为度，DWG 位流/模型侧为弧度（in_dxf deg2rad 同款）
			h.GradientAngle = p.floatValue() * math.Pi / 180
		case 461:
			h.GradientShift = p.floatValue()
		case 462:
			h.GradientTint = p.floatValue()
		case 463:
			curColor++
			if curColor < len(h.Colors) {
				h.Colors[curColor].ShiftValue = p.floatValue()
			} else if curColor < 1000 {
				h.Colors = append(h.Colors, entity.HatchGradientColor{ShiftValue: p.floatValue()})
			}
		case 63:
			if curColor >= 0 && curColor < len(h.Colors) {
				h.Colors[curColor].ColorIndex = int64(p.floatValue())
			}
		case 421:
			if curColor >= 0 && curColor < len(h.Colors) {
				h.Colors[curColor].ColorRGB = fmt.Sprintf("%08x", uint32(p.floatValue()))
			}
		case 470:
			h.GradientName = p.strValue()
		}
	}
	// 细分点列（渲染）：与 JSON 构造同口径
	for i := range h.Paths {
		pa := &h.Paths[i]
		if pa.IsPolyline {
			verts := make([]entity.Point2, len(pa.PolyVerts))
			bulges := make([]float64, len(pa.PolyVerts))
			for j, pv := range pa.PolyVerts {
				verts[j], bulges[j] = pv.P, pv.Bulge
			}
			pts := verts
			if pa.BulgesPresent {
				pts = entity.PolylineWithBulges(verts, bulges, pa.Closed, 64)
			}
			if pa.Closed {
				pts = entity.ClosePath(pts)
			}
			pa.Points = pts
		} else {
			var pts []entity.Point2
			for _, seg := range pa.Segs {
				if seg.CurveType == 1 {
					if len(pts) == 0 {
						pts = append(pts, seg.First)
					}
					pts = append(pts, seg.Second)
				}
			}
			pa.Points = pts
		}
	}
	return h
}

// buildLeader LEADER：71 箭头可见、72 路径类型、73 注释类型、74 钩线方向、
// 40/41 文本框宽高、76 顶点数 + 10/20/30 折点、210 挤出、211 X 方向、
// 212 插入偏移、213 端点投影（组码对照 dwg.spec LEADER 的 DXF 分支）。
func (st *dxfState) buildLeader(rec *dxfRec, base *entity.BaseEntity) any {
	l := &entity.EntLeader{BaseEntity: *base}
	if v, ok := rec.intVal(71); ok {
		l.ArrowheadOn = v != 0
	}
	if v, ok := rec.intVal(72); ok {
		l.PathType = uint16(v)
	}
	if v, ok := rec.intVal(73); ok {
		l.AnnotationType = uint16(v)
	}
	if v, ok := rec.intVal(74); ok {
		l.HooklineDir = v != 0
	}
	if v, ok := rec.floatVal(40); ok {
		l.BoxHeight = v
	}
	if v, ok := rec.floatVal(41); ok {
		l.BoxWidth = v
	}
	for _, p := range collect3Seq(rec, 10) {
		l.Points = append(l.Points, p)
	}
	l.Extrusion = rec.point3(210)
	l.XDirection = rec.point3(211)
	l.InsptOffset = rec.point3(212)
	l.Endptproj = rec.point3(213)
	l.CalcHooklineOn()
	return l
}

// collect3Seq 按出现顺序收集 (code, code+10, code+20) 三元组序列
// （LEADER/MULTILEADER 的重复 10 组码顶点）。
func collect3Seq(rec *dxfRec, code int) []entity.Point3 {
	var out []entity.Point3
	var cur entity.Point3
	stage := 0
	for _, p := range rec.pairs {
		switch p.code {
		case code:
			if stage == 3 {
				out = append(out, cur)
			}
			cur.X = p.floatValue()
			stage = 1
		case code + 10:
			if stage == 1 {
				cur.Y = p.floatValue()
				stage = 2
			}
		case code + 20:
			if stage == 2 {
				cur.Z = p.floatValue()
				stage = 3
			}
		}
	}
	if stage == 3 {
		out = append(out, cur)
	}
	return out
}

// buildMLine MLINE：2 样式名、340 样式句柄、40 比例、70 对齐、71 开闭、
// 73 样式线数、72 顶点数、10 基点、210 挤出、每顶点 11/21/31 位置 +
// 12/22/32 方向 + 13/23/33 miter + 每线 74/41 段参数、75/42 区域参数
// （组码对照 dwg.spec MLINE 的 DXF 分支：justification=70、num_verts=72、
// num_lines=73）。
func (st *dxfState) buildMLine(rec *dxfRec, base *entity.BaseEntity) any {
	m := &entity.EntMLine{BaseEntity: *base}
	if sh, ok := rec.hexHandle(340); ok {
		m.StyleHandle = sh
	}
	if v, ok := rec.floatVal(40); ok {
		m.Scale = v
	}
	if v, ok := rec.intVal(70); ok {
		m.Justification = uint8(v & 0xFF)
	}
	if v, ok := rec.intVal(71); ok {
		m.OpenClosed = uint16(v)
	}
	if v, ok := rec.intVal(73); ok {
		m.LinesInStyle = uint8(v & 0xFF)
	}
	// 顶点流：11/21/31 位置 → 12/22/32 方向 → 13/23/33 miter →
	// 每线 74 计数 + 41×N → 75 计数 + 42×N → 下一顶点 11。
	const (
		stIdle = iota
		stPos
		stDir
		stMiter
	)
	stage := stIdle
	parmsStage := 0 // 0 无、1 segparms、2 areafillparms
	parmCount := 0
	var cur entity.EntMLineVertex
	for _, p := range rec.pairs {
		switch p.code {
		case 11:
			if stage != stIdle {
				m.Vertices = append(m.Vertices, cur)
			}
			cur = entity.EntMLineVertex{Position: entity.Point3{X: p.floatValue()}}
			stage = stPos
		case 21:
			if stage == stPos {
				cur.Position.Y = p.floatValue()
			}
		case 31:
			if stage == stPos {
				cur.Position.Z = p.floatValue()
				stage = stDir
			}
		case 12:
			if stage >= stPos {
				cur.Direction.X = p.floatValue()
			}
		case 22:
			if stage >= stPos {
				cur.Direction.Y = p.floatValue()
			}
		case 32:
			if stage >= stPos {
				cur.Direction.Z = p.floatValue()
				stage = stMiter
			}
		case 13:
			if stage >= stPos {
				cur.Miter.X = p.floatValue()
			}
		case 23:
			if stage >= stPos {
				cur.Miter.Y = p.floatValue()
			}
		case 33:
			if stage >= stPos {
				cur.Miter.Z = p.floatValue()
			}
		case 74:
			if stage >= stPos {
				parmsStage = 1
				parmCount = int(p.floatValue())
			}
		case 75:
			if stage >= stPos {
				parmsStage = 2
				parmCount = int(p.floatValue())
			}
		case 41:
			if stage >= stPos && parmsStage == 1 && parmCount > 0 {
				cur.SegParams = append(cur.SegParams, p.floatValue())
				parmCount--
			}
		case 42:
			if stage >= stPos && parmsStage == 2 && parmCount > 0 {
				cur.AreaParams = append(cur.AreaParams, p.floatValue())
				parmCount--
			}
		}
	}
	if stage != stIdle {
		m.Vertices = append(m.Vertices, cur)
	}
	return m
}

// buildMLeader MULTILEADER：270 版本 + 300 CONTEXT_DATA{...}301 上下文段
// + 301 后顶层尾段（340 样式句柄、90 override 掩码、170 类型、91 线色、
// 341 线型、171 线宽、290/291 落地/狗腿、41 落地距、342/42 箭头、172 内容
// 样式、343 文字样式、173/95/174/175 文字对齐组、92 文字色、292 边框、
// 344/93/10/43 块内容、176/293 附属/注释性、294/178/179/45 负向/对齐/
// 比例、271/273/272/295 R2010+ 附着组）。组码对照 dwg2.spec MULTILEADER
// 的 DXF 标注与 in_dxf.c add_MULTILEADER。上下文段解析顶层标量、文字
// 内容与 302 LEADER{...} 引线骨架（点列/狗腿/断开），LEADER_LINE 段读
// 点列与线索引；块内容（296 起）结构深且渲染不消费，跳过。
func (st *dxfState) buildMLeader(rec *dxfRec, base *entity.BaseEntity) any {
	m := &entity.EntMLeader{BaseEntity: *base}
	if v, ok := rec.intVal(270); ok {
		m.HasVersion = true
		m.ClassVersion = uint16(v)
	}
	// ---- 上下文段（300 CONTEXT_DATA{ 到 301 }）----
	inCtx := false
	inLeader := false // 302 LEADER{ 到 303 }
	inLine := false   // 304 LEADER_LINE{ 到 305 }
	var curNode *entity.MleaderNode
	var curLine *entity.MleaderLine
	numLeaders := 0
	blkTf := 0 // blk 变换矩阵 47×16 游标
	for _, p := range rec.pairs {
		code := p.code
		if code == 300 {
			inCtx = p.strValue() == "CONTEXT_DATA{"
			continue
		}
		if code == 301 {
			inCtx = false
			continue
		}
		if !inCtx {
			continue
		}
		switch code {
		case 302:
			if inLeader { // 嵌套异常，容错关闭
				m.Ctx.Leaders = append(m.Ctx.Leaders, *curNode)
			}
			m.Ctx.Leaders = append(m.Ctx.Leaders, entity.MleaderNode{})
			curNode = &m.Ctx.Leaders[len(m.Ctx.Leaders)-1]
			curNode.BranchIndex = uint32(numLeaders)
			numLeaders++
			inLeader = true
			inLine = false
			curLine = nil
		case 303:
			if inLeader {
				if curNode != nil {
					curNode.NumLines = uint32(len(curNode.Lines))
				}
				inLeader = false
				curNode = nil
			}
		case 304:
			if inLeader && curNode != nil {
				curNode.Lines = append(curNode.Lines, entity.MleaderLine{})
				curLine = &curNode.Lines[len(curNode.Lines)-1]
				inLine = true
			}
		case 305:
			if inLine && curLine != nil {
				curLine.NumBreaks = uint32(len(curLine.Breaks))
			}
			inLine = false
			curLine = nil
		case 40:
			if !inLine {
				if inLeader && curNode != nil {
					curNode.DoglegLength = p.floatValue()
				} else {
					m.Ctx.ScaleFactor = p.floatValue()
				}
			}
		}
		if inLine && curLine != nil {
			switch code {
			case 10:
				curLine.Points = append(curLine.Points, entity.Point3{X: p.floatValue()})
			case 20:
				if n := len(curLine.Points); n > 0 {
					curLine.Points[n-1].Y = p.floatValue()
				}
			case 30:
				if n := len(curLine.Points); n > 0 {
					curLine.Points[n-1].Z = p.floatValue()
				}
			case 91:
				curLine.LineIndex = uint32(p.floatValue())
			}
			continue
		}
		if inLeader && curNode != nil {
			switch code {
			case 290:
				curNode.HasLastLeaderLinePoint = p.floatValue() != 0
			case 291:
				curNode.HasDogleg = p.floatValue() != 0
			case 90:
				curNode.BranchIndex = uint32(p.floatValue())
			case 10:
				curNode.LastLeaderLinePoint.X = p.floatValue()
			case 20:
				curNode.LastLeaderLinePoint.Y = p.floatValue()
			case 30:
				curNode.LastLeaderLinePoint.Z = p.floatValue()
			case 11:
				curNode.DoglegVector.X = p.floatValue()
			case 21:
				curNode.DoglegVector.Y = p.floatValue()
			case 31:
				curNode.DoglegVector.Z = p.floatValue()
			}
			continue
		}
		// ctx 顶层（txt 内容分支 has_content_txt / blk 内容分支
		// has_content_blk，组码对照 dwg2.spec MLEADER_CONTEXT_DATA_fields
		// 与 in_dxf add_MULTILEADER）
		switch code {
		case 10:
			m.Ctx.ContentBase.X = p.floatValue()
		case 20:
			m.Ctx.ContentBase.Y = p.floatValue()
		case 30:
			m.Ctx.ContentBase.Z = p.floatValue()
		case 41:
			m.Ctx.TextHeight = p.floatValue()
		case 140:
			m.Ctx.ArrowSize = p.floatValue()
		case 145:
			m.Ctx.LandingGap = p.floatValue()
		case 174:
			m.Ctx.TextLeft = uint16(p.floatValue())
		case 175:
			m.Ctx.TextRight = uint16(p.floatValue())
		case 176:
			m.Ctx.TextAngletype = uint16(p.floatValue())
		case 177:
			m.Ctx.TextAlignment = uint16(p.floatValue())
		case 290:
			m.Ctx.HasContentTxt = p.floatValue() != 0
		case 296:
			m.Ctx.HasContentBlk = p.floatValue() != 0
		case 304:
			if m.Ctx.HasContentTxt {
				m.Ctx.Txt.DefaultText = p.strValue()
			}
		case 110:
			m.Ctx.Base.X = p.floatValue()
		case 120:
			m.Ctx.Base.Y = p.floatValue()
		case 130:
			m.Ctx.Base.Z = p.floatValue()
		}
		if m.Ctx.HasContentTxt {
			// txt 内容标量（304 default_text 已在上方处理）
			switch code {
			case 11:
				m.Ctx.Txt.Normal.X = p.floatValue()
			case 21:
				m.Ctx.Txt.Normal.Y = p.floatValue()
			case 31:
				m.Ctx.Txt.Normal.Z = p.floatValue()
			case 340:
				if h, ok := dxfPairHandle(p); ok {
					m.Ctx.Txt.StyleHandle = h
				}
			case 12:
				m.Ctx.Txt.Location.X = p.floatValue()
			case 22:
				m.Ctx.Txt.Location.Y = p.floatValue()
			case 32:
				m.Ctx.Txt.Location.Z = p.floatValue()
			case 13:
				m.Ctx.Txt.Direction.X = p.floatValue()
			case 23:
				m.Ctx.Txt.Direction.Y = p.floatValue()
			case 33:
				m.Ctx.Txt.Direction.Z = p.floatValue()
			case 42:
				m.Ctx.Txt.Rotation = p.floatValue() * math.Pi / 180
			case 43:
				m.Ctx.Txt.Width = p.floatValue()
			case 44:
				m.Ctx.Txt.Height = p.floatValue()
			case 45:
				m.Ctx.Txt.LineSpacingFactor = p.floatValue()
			case 170:
				m.Ctx.Txt.LineSpacingStyle = uint16(p.floatValue())
			case 90:
				dxfSetMLeaderCMC(&m.Ctx.Txt.Color, p)
			case 91:
				dxfSetMLeaderCMC(&m.Ctx.Txt.BgColor, p)
			case 171:
				m.Ctx.Txt.Alignment = uint16(p.floatValue())
			case 172:
				m.Ctx.Txt.Flow = uint16(p.floatValue())
			case 141:
				m.Ctx.Txt.BgScale = p.floatValue()
			case 92:
				m.Ctx.Txt.BgTransparency = uint32(p.floatValue())
			case 291:
				m.Ctx.Txt.IsBgFill = p.floatValue() != 0
			case 292:
				m.Ctx.Txt.IsBgMaskFill = p.floatValue() != 0
			case 173:
				m.Ctx.Txt.ColType = uint16(p.floatValue())
			case 293:
				m.Ctx.Txt.IsHeightAuto = p.floatValue() != 0
			case 142:
				m.Ctx.Txt.ColWidth = p.floatValue()
			case 143:
				m.Ctx.Txt.ColGutter = p.floatValue()
			case 294:
				m.Ctx.Txt.IsColFlowReversed = p.floatValue() != 0
			case 144:
				m.Ctx.Txt.ColSizes = append(m.Ctx.Txt.ColSizes, p.floatValue())
			case 295:
				m.Ctx.Txt.WordBreak = p.floatValue() != 0
			}
			continue
		}
		if m.Ctx.HasContentBlk {
			// blk 内容分支：296 开关 + 341 块表 + 14/15/16 三组 3BD +
			// 46 旋转 + 93 颜色 + 47×16 变换矩阵（dxfin 同序）
			switch code {
			case 341:
				if h, ok := dxfPairHandle(p); ok {
					m.Ctx.Blk.BlockTable = h
				}
			case 14:
				m.Ctx.Blk.Normal.X = p.floatValue()
			case 24:
				m.Ctx.Blk.Normal.Y = p.floatValue()
			case 34:
				m.Ctx.Blk.Normal.Z = p.floatValue()
			case 15:
				m.Ctx.Blk.Location.X = p.floatValue()
			case 25:
				m.Ctx.Blk.Location.Y = p.floatValue()
			case 35:
				m.Ctx.Blk.Location.Z = p.floatValue()
			case 16:
				m.Ctx.Blk.Scale.X = p.floatValue()
			case 26:
				m.Ctx.Blk.Scale.Y = p.floatValue()
			case 36:
				m.Ctx.Blk.Scale.Z = p.floatValue()
			case 46:
				m.Ctx.Blk.Rotation = p.floatValue() * math.Pi / 180
			case 93:
				dxfSetMLeaderCMC(&m.Ctx.Blk.Color, p)
			case 47:
				if blkTf < 16 {
					m.Ctx.Blk.Transform[blkTf] = p.floatValue()
					blkTf++
				}
			}
		}
	}
	// ---- 顶层尾段（301 } 之后；二次扫描跳过上下文段内同名组码）----
	tail := false
	for _, p := range rec.pairs {
		switch p.code {
		case 300:
			tail = true
		case 301:
			if tail {
				tail = false
				continue
			}
		}
		if tail {
			continue
		}
		switch p.code {
		case 340:
			if h, ok := dxfPairHandle(p); ok {
				m.MleaderStyle = h
			}
		case 90:
			m.Flags = uint32(p.floatValue())
		case 170:
			m.MleaderType = uint16(p.floatValue())
		case 91:
			m.LineColor.Index = uint16(uint32(p.floatValue()) & 0xFFFFFFFF)
			m.LineColor.IsTrue = true
		case 341:
			if h, ok := dxfPairHandle(p); ok {
				m.LineLtype = h
			}
		case 171:
			m.LineLinewt = int32(p.floatValue())
		case 290:
			m.HasLanding = p.floatValue() != 0
		case 291:
			m.HasDogleg = p.floatValue() != 0
		case 41:
			m.LandingDist = p.floatValue()
		case 342:
			if h, ok := dxfPairHandle(p); ok {
				m.ArrowHandle = h
			}
		case 42:
			m.ArrowSize = p.floatValue()
		case 172:
			m.StyleContent = uint16(p.floatValue())
		case 343:
			if h, ok := dxfPairHandle(p); ok {
				m.TextStyle = h
			}
		case 173:
			m.TextLeft = uint16(p.floatValue())
		case 95:
			m.TextRight = uint16(p.floatValue())
		case 174:
			m.TextAngletype = uint16(p.floatValue())
		case 175:
			m.TextAlignment = uint16(p.floatValue())
		case 92:
			m.TextColor.Index = uint16(uint32(p.floatValue()) & 0xFFFFFFFF)
			m.TextColor.IsTrue = true
		case 292:
			m.HasTextFrame = p.floatValue() != 0
		case 344:
			if h, ok := dxfPairHandle(p); ok {
				m.BlockStyle = h
			}
		case 93:
			m.BlockColor.Index = uint16(uint32(p.floatValue()) & 0xFFFFFFFF)
			m.BlockColor.IsTrue = true
		case 176:
			m.StyleAttachment = uint16(p.floatValue())
		case 293:
			m.IsAnnotative = p.floatValue() != 0
		case 294:
			m.IsNegTextdir = p.floatValue() != 0
		case 178:
			m.IpeAlignment = uint16(p.floatValue())
		case 179:
			m.Justification = uint16(p.floatValue())
		case 45:
			m.ScaleFactor = p.floatValue()
		case 271:
			m.AttachDir = uint16(p.floatValue())
		case 273:
			m.AttachTop = uint16(p.floatValue())
		case 272:
			m.AttachBottom = uint16(p.floatValue())
		case 295:
			m.IsTextExtended = p.floatValue() != 0
		case 10:
			// 顶层块缩放（3BD，dxfin 尾段与 ctx 段 10 分属两遍扫描）
			m.BlockScale.X = p.floatValue()
		case 20:
			m.BlockScale.Y = p.floatValue()
		case 30:
			m.BlockScale.Z = p.floatValue()
		case 43:
			m.BlockRotation = p.floatValue() * math.Pi / 180
		}
	}
	return m
}

// dxfPairHandle 单组码对中的句柄值（十六进制字符串 → 数值）。
func dxfPairHandle(p dxfPair) (uint64, bool) {
	h, err := strconv.ParseUint(strings.TrimSpace(p.strValue()), 16, 64)
	return h, err == nil
}

// dxfSetMLeaderCMC DXF 整数形态的 MLEADER 颜色组码（90/91/92/93）：
// 值 >257 为真彩 rgb（0xc2/0xc3 前缀 method + 24 位 RGB，index 置 256），
// 否则为 ACI 索引（对齐 in_dxf add_MULTILEADER 的 CMC 双分支）。
func dxfSetMLeaderCMC(c *entity.MleaderCMC, p dxfPair) {
	v := uint32(p.floatValue())
	if v > 257 {
		c.Rgb = v
		c.Index = 256
		return
	}
	c.Index = uint16(v)
}

// buildTolerance TOLERANCE：10 插入点、11 对称轴方向、210 挤出、1 标注
// 文本（组码对照 dwg.spec TOLERANCE 的 DXF 分支；3 为 dimstyle 名，无
// 句柄对应不恢复；R13/R14 专属 height/dimgap 位流字段 DXF 不输出）。
func (st *dxfState) buildTolerance(rec *dxfRec, base *entity.BaseEntity) any {
	t := &entity.EntTolerance{BaseEntity: *base}
	t.Insertion = rec.point3(10)
	t.XDirection = rec.point3(11)
	t.Extrusion = rec.point3(210)
	t.Text = rec.str(1)
	return t
}

// buildViewport VIEWPORT：10 中心、40/41 宽高、12-15 视图/捕捉/网格点、
// 16/17 视向与目标、42-45 镜头/前后裁剪/视高、50/51 角度、72 圆缩放、
// 90 状态、1 样式表、281 渲染模式、71/74 UCS 标志、110-112 UCS 三轴、
// 79 正交视图、146 标高（组码对照 dwg.spec VIEWPORT 的 DXF 分支）。
func (st *dxfState) buildViewport(rec *dxfRec, base *entity.BaseEntity) any {
	vp := &entity.EntViewport{BaseEntity: *base}
	vp.Center = rec.point3(10)
	if v, ok := rec.floatVal(40); ok {
		vp.Width = v
	}
	if v, ok := rec.floatVal(41); ok {
		vp.Height = v
	}
	vp.ViewCtr = rec.point2(12)
	vp.SnapBase = rec.point2(13)
	vp.SnapUnit = rec.point2(14)
	vp.GridUnit = rec.point2(15)
	vp.ViewDir = rec.point3(16)
	vp.ViewTarget = rec.point3(17)
	if v, ok := rec.floatVal(42); ok {
		vp.LensLength = v
	}
	if v, ok := rec.floatVal(43); ok {
		vp.FrontZ = v
	}
	if v, ok := rec.floatVal(44); ok {
		vp.BackZ = v
	}
	if v, ok := rec.floatVal(45); ok {
		vp.ViewSize = v
	}
	if v, ok := rec.floatVal(50); ok {
		vp.SnapAng = dxfDegToRad(v)
	}
	if v, ok := rec.floatVal(51); ok {
		vp.ViewTwist = dxfDegToRad(v)
	}
	if v, ok := rec.intVal(72); ok {
		vp.CircleZoom = uint16(v)
	}
	if v, ok := rec.intVal(90); ok {
		vp.StatusFlag = uint32(v)
	}
	vp.StyleSheet = rec.str(1)
	if v, ok := rec.intVal(281); ok {
		vp.RenderMode = uint8(v & 0xFF)
	}
	if v, ok := rec.intVal(71); ok {
		vp.UcsVP = v != 0
	}
	if v, ok := rec.intVal(74); ok {
		vp.UcsAtOrigin = v != 0
	}
	vp.Ucsorg = rec.point3(110)
	vp.Ucsxdir = rec.point3(111)
	vp.Ucsydir = rec.point3(112)
	if v, ok := rec.intVal(79); ok {
		vp.UcsOrthoView = uint16(v)
	}
	if v, ok := rec.floatVal(146); ok {
		vp.UcsElevation = v
	}
	if v, ok := rec.intVal(61); ok {
		vp.GridMajor = uint16(v)
	}
	if v, ok := rec.intVal(292); ok {
		vp.UseDefaultLights = v != 0
	}
	if v, ok := rec.intVal(282); ok {
		vp.DefaultLightingType = uint8(v & 0xFF)
	}
	return vp
}

// buildAcis REGION/3DSOLID/BODY（极限批次 A）：SAT 文本行拼接建模。
// DXF 组码 1/3 行为加密态 SAT（每行一个组码，code 1 行尾补换行），
// 按码值逐行拼接后做 in_dxf 同款解密（'^ ' 还原为明文 'A'，其余字节
// b≤32 保留、否则 159-b）得 acisData；290 acis_empty/70 version 一并
// 消费。几何内核不在解析范围，与 DWG 侧 entAcis 同构。
func (st *dxfState) buildAcis(rec *dxfRec, base *entity.BaseEntity) any {
	a := &entity.EntAcis{BaseEntity: *base, Kind: rec.typ}
	if v, ok := rec.intVal(290); ok {
		a.AcisEmpty = v != 0
	}
	if v, ok := rec.intVal(70); ok {
		a.Version = uint16(v)
	}
	if a.AcisEmpty {
		return a
	}
	var buf []byte
	for _, p := range rec.pairs {
		if p.code != 1 && p.code != 3 {
			continue
		}
		s := p.strValue()
		for i := 0; i < len(s); i++ {
			switch {
			case s[i] == '^' && i+1 < len(s) && s[i+1] == ' ':
				buf = append(buf, 'A')
				i++
			case s[i] <= 32:
				buf = append(buf, s[i])
			default:
				buf = append(buf, 159-s[i])
			}
		}
		if p.code == 1 {
			buf = append(buf, '\n')
		}
	}
	if len(buf) > 0 {
		a.AcisData = buf
		a.Blocks = [][]byte{buf}
	}
	return a
}

// ---- 词法层 ----

// dxfValKind 组码值的语义类型（对齐 dwg_resbuf_value_type）。
type dxfValKind int

const (
	dxfValString dxfValKind = iota // NUL 结尾/行文本
	dxfValReal                     // 8 字节 double
	dxfValInt16                    // 2 字节短整
	dxfValInt8                     // 280-289（二进制用 2 字节存）
	dxfValBool                     // 290-299
	dxfValInt32                    // 4 字节长整
	dxfValInt64                    // 8 字节长长整
	dxfValHandle                   // 十六进制句柄字符串
	dxfValBinary                   // 长度前缀字节块
)

// dxfValueType 组码 → 值类型（移植 dwg.c dwg_resbuf_value_type 分支表）。
func dxfValueType(code int) dxfValKind {
	switch {
	case code < 0:
		return dxfValHandle
	case code <= 4:
		return dxfValString
	case code == 5:
		return dxfValHandle
	case code <= 9:
		return dxfValString
	case code <= 59:
		return dxfValReal
	case code <= 79:
		return dxfValInt16
	case code <= 99:
		return dxfValInt32
	case code <= 102:
		return dxfValString
	case code == 105:
		return dxfValHandle
	case code <= 109:
		return dxfValString // 106-109 非法区间，按文本跳过最稳
	case code <= 149:
		return dxfValReal
	case code <= 169:
		return dxfValInt64
	case code <= 179:
		return dxfValInt16
	case code <= 209:
		return dxfValString // 180-209 非法区间
	case code <= 269:
		return dxfValReal
	case code <= 279:
		return dxfValInt16
	case code <= 289:
		return dxfValInt8
	case code <= 299:
		return dxfValBool
	case code <= 309:
		return dxfValString
	case code <= 319:
		return dxfValBinary
	case code <= 369:
		return dxfValHandle
	case code <= 389:
		return dxfValInt16
	case code <= 399:
		return dxfValHandle
	case code <= 409:
		return dxfValInt16
	case code <= 419:
		return dxfValString
	case code <= 429:
		return dxfValInt32
	case code <= 439:
		return dxfValString
	case code <= 459:
		return dxfValInt32
	case code <= 469:
		return dxfValReal
	case code <= 479:
		return dxfValString
	case code <= 998:
		return dxfValString // 480-998 非法区间
	case code == 999:
		return dxfValString
	case code == 1004:
		return dxfValBinary
	case code <= 1009:
		return dxfValString
	case code <= 1059:
		return dxfValReal
	case code == 1060, code == 1070:
		return dxfValInt16
	case code == 1071:
		return dxfValInt32
	default:
		return dxfValString
	}
}

// dxfPair 单个组码对（ASCII/二进制统一表示）。
type dxfPair struct {
	code int
	s    string // 字符串/句柄类原始值
	num  float64
}

// strValue 字符串值（句柄类返回原文）。
func (p dxfPair) strValue() string { return p.s }

// floatValue 数值（字符串类解析失败为 0，与 LibreDWG 的容错口径一致）。
func (p dxfPair) floatValue() float64 { return p.num }

// dxfLexer 组码对流：ASCII 与二进制两种编码的统一读取游标。
type dxfLexer struct {
	data    []byte
	pos     int
	binary  bool // "AutoCAD Binary DXF" 变体
	preR14  bool // 二进制 pre-R14：1 字节组码（0xFF 前缀扩展到 RS）
	back    *dxfPair
	version container.DwgVersion
	cp      uint16 // 文本解码码页（R12 系由 HEADER $DWGCODEPAGE 启用；0=字节直读）
}

// newDXFLexer 构造词法器并完成编码识别：22 字节魔数判定二进制；随后按
// LibreDWG 的探测法区分 pre-R14（1 字节组码）与 R14+（2 字节组码）——
// 魔数后第一个记录是 (0, SECTION)，pre-R14 在 0x00 后直接是 'S'。
func newDXFLexer(data []byte) (*dxfLexer, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("cad: DXF 输入为空")
	}
	l := &dxfLexer{data: data, version: container.VerR2018}
	if bytes.HasPrefix(data, dxfBinaryMagic) {
		l.binary = true
		l.pos = len(dxfBinaryMagic)
		// 截断保护：至少还能容纳一个最短记录
		if len(data) < l.pos+2 {
			return nil, fmt.Errorf("cad: 二进制 DXF 头部不完整")
		}
		if data[l.pos] == 0 && l.pos+1 < len(data) && data[l.pos+1] == 0 {
			l.version = container.VerR14
		} else {
			l.preR14 = true
			l.version = container.VerR13
		}
	}
	return l, nil
}

// pushBack 回退一个组码对（readRecord 的边界回吐；深度恒为 1）。
func (l *dxfLexer) pushBack(p dxfPair) { l.back = &p }

// popBack 丢弃回退对（EOF 场景清理）。
func (l *dxfLexer) popBack() { l.back = nil }

// next 读取下一组码对：ok=false 表示文件结束。
func (l *dxfLexer) next() (dxfPair, bool, error) {
	if l.back != nil {
		p := *l.back
		l.back = nil
		return p, true, nil
	}
	if l.binary {
		return l.nextBinary()
	}
	return l.nextASCII()
}

// readLine 读取一行（不含行尾；容忍 CR/LF/CRLF）。
func (l *dxfLexer) readLine() (string, bool) {
	if l.pos >= len(l.data) {
		return "", false
	}
	end := bytes.IndexByte(l.data[l.pos:], '\n')
	var line []byte
	if end < 0 {
		line = l.data[l.pos:]
		l.pos = len(l.data)
	} else {
		line = l.data[l.pos : l.pos+end]
		l.pos += end + 1
	}
	line = bytes.TrimSuffix(line, []byte("\r"))
	return string(line), true
}

// nextASCII ASCII 编码：组码行 + 值行。
func (l *dxfLexer) nextASCII() (dxfPair, bool, error) {
	for {
		codeLine, ok := l.readLine()
		if !ok {
			return dxfPair{}, false, nil
		}
		code, err := strconv.Atoi(strings.TrimSpace(codeLine))
		if err != nil {
			// 组码行必须为整数；无法解析视为文件损坏（截断/混入垃圾字节）
			return dxfPair{}, false, fmt.Errorf("cad: DXF 组码行非法 %q（偏移 %d）", codeLine, l.pos)
		}
		valLine, ok := l.readLine()
		if !ok {
			return dxfPair{}, false, fmt.Errorf("cad: DXF 在组码 %d 后意外结束（截断）", code)
		}
		p := dxfPair{code: code}
		l.fillValue(&p, strings.TrimSuffix(valLine, "\r"))
		return p, true, nil
	}
}

// nextBinary 二进制编码：按值类型读定长记录。
func (l *dxfLexer) nextBinary() (dxfPair, bool, error) {
	code, err := l.readBinCode()
	if err != nil {
		return dxfPair{}, false, nil // 尾部残缺按 EOF 处理
	}
	p := dxfPair{code: code}
	if err := l.readBinValue(&p); err != nil {
		return dxfPair{}, false, err
	}
	return p, true, nil
}

// readBinCode 组码：R14+ 为 2 字节小端；pre-R14 为 1 字节、0xFF 前缀扩展。
func (l *dxfLexer) readBinCode() (int, error) {
	if l.preR14 {
		if l.pos >= len(l.data) {
			return 0, fmt.Errorf("cad: 二进制 DXF 组码越界")
		}
		c := int(l.data[l.pos])
		l.pos++
		if c == 0xFF {
			if l.pos+2 > len(l.data) {
				return 0, fmt.Errorf("cad: 二进制 DXF 扩展组码越界")
			}
			c = int(binary.LittleEndian.Uint16(l.data[l.pos:]))
			l.pos += 2
		}
		return c, nil
	}
	if l.pos+2 > len(l.data) {
		return 0, fmt.Errorf("cad: 二进制 DXF 组码越界")
	}
	c := int(binary.LittleEndian.Uint16(l.data[l.pos:]))
	l.pos += 2
	return c, nil
}

// readBinValue 按组码类型读取二进制值。
func (l *dxfLexer) readBinValue(p *dxfPair) error {
	need := func(n int) error {
		if l.pos+n > len(l.data) {
			l.pos = len(l.data)
			return fmt.Errorf("cad: 二进制 DXF 值越界（组码 %d，截断）", p.code)
		}
		return nil
	}
	switch dxfValueType(p.code) {
	case dxfValReal, dxfValInt64:
		if err := need(8); err != nil {
			return err
		}
		if dxfValueType(p.code) == dxfValReal {
			p.num = math.Float64frombits(binary.LittleEndian.Uint64(l.data[l.pos:]))
		} else {
			p.num = float64(int64(binary.LittleEndian.Uint64(l.data[l.pos:])))
		}
		l.pos += 8
	case dxfValInt32:
		if err := need(4); err != nil {
			return err
		}
		p.num = float64(int32(binary.LittleEndian.Uint32(l.data[l.pos:])))
		l.pos += 4
	case dxfValInt16, dxfValInt8:
		// 二进制编码统一用 2 字节（280-289 INT8 亦然，对齐 in_dxf.c）
		if err := need(2); err != nil {
			return err
		}
		p.num = float64(int16(binary.LittleEndian.Uint16(l.data[l.pos:])))
		l.pos += 2
	case dxfValBool:
		if err := need(1); err != nil {
			return err
		}
		p.num = float64(l.data[l.pos])
		l.pos++
	case dxfValBinary:
		if err := need(1); err != nil {
			return err
		}
		n := int(l.data[l.pos])
		l.pos++
		if err := need(n); err != nil {
			return err
		}
		l.pos += n // 字节块当前无消费方，跳过即可
	case dxfValString, dxfValHandle:
		end := bytes.IndexByte(l.data[l.pos:], 0)
		if end < 0 {
			l.pos = len(l.data)
			return fmt.Errorf("cad: 二进制 DXF 字符串未以 NUL 结束（组码 %d，截断）", p.code)
		}
		raw := string(l.data[l.pos : l.pos+end])
		l.pos += end + 1
		if dxfValueType(p.code) == dxfValReal {
			p.num, _ = strconv.ParseFloat(p.s, 64)
		} else {
			p.s = l.decodeText(raw) // R12 系按码页解码（与 ASCII 路径同口径）
		}
	}
	return nil
}

// fillValue 填充 ASCII 值：数值类组码立即解析（解析失败保留 0，与
// LibreDWG strtod 失败返回 NaN 的容错口径一致，不视为文件损坏）；
// 字符串类按词法层码页解码（R12 系 GBK 等，先码页后转义——GBK 第二
// 字节可含 ^ 字符，先转义会误改字节流），再还原 ^J/^M 转义（写出端
// cquote 的逆过程）。
func (l *dxfLexer) fillValue(p *dxfPair, raw string) {
	switch dxfValueType(p.code) {
	case dxfValReal, dxfValInt16, dxfValInt8, dxfValBool, dxfValInt32, dxfValInt64:
		p.num, _ = strconv.ParseFloat(strings.TrimSpace(raw), 64)
	case dxfValHandle, dxfValBinary:
		p.s = raw
	case dxfValString:
		p.s = dxfUnquote(l.decodeText(raw))
	}
}

// decodeText 按 lexer 码页解码文本字节串（0=字节直读，R13+ 恒为直读）。
func (l *dxfLexer) decodeText(raw string) string {
	if l.cp == 0 {
		return raw
	}
	return bitstream.DecodeCodepage([]byte(raw), l.cp)
}

// dxfUnquote 还原写出端转义：^J → 换行、^M → 回车。
func dxfUnquote(s string) string {
	if !strings.Contains(s, "^") {
		return s
	}
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '^' && i+1 < len(s) {
			switch s[i+1] {
			case 'J':
				sb.WriteByte('\n')
				i++
				continue
			case 'M':
				sb.WriteByte('\r')
				i++
				continue
			}
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}
