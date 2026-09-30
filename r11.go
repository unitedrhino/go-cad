// r11.go 实现 pre-R13 家族（R9/AC1004、R10/AC1006、R11/AC1009）的读取。
// 该家族与 R13+ 完全不同：字节对齐、无对象图/句柄流/位压缩，实体表与
// 块表/层表等为固定偏移表驱动，R11 起各表与实体区以 16 字节 sentinel
// 包夹且记录尾带 CRC。字段布局逐位对齐 LibreDWG decode_r11.c 及
// header.spec / header_variables_r11.spec / dwg.spec / common_entity_data.spec。
// 解码产物复用 R13+ 的实体类型（entLine 等）与 Document 模型，
// 使 RenderPNG / Texts 全链路无差别工作。
package cad

import (
	"encoding/binary"
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"math"
)

// preR13 版本家族的实体类型码（LibreDWG Dwg_Object_Type_r11）。
const (
	preR13TypeLine      = 1
	preR13TypePoint     = 2
	preR13TypeCircle    = 3
	preR13TypeShape     = 4
	preR13TypeText      = 7
	preR13TypeArc       = 8
	preR13TypeTrace     = 9
	preR13TypeSolid     = 11
	preR13TypeBlock     = 12
	preR13TypeEndblk    = 13
	preR13TypeInsert    = 14
	preR13TypeAttdef    = 15
	preR13TypeAttrib    = 16
	preR13TypeSeqend    = 17
	preR13TypeJump      = 18
	preR13TypePolyline  = 19
	preR13TypeVertex    = 20
	preR13Type3DLine    = 21
	preR13Type3DFace    = 22
	preR13TypeDimension = 23
	preR13TypeViewport  = 24
)

// 实体公共头 flag_r11 位（LibreDWG FLAG_R11_*）。
const (
	preR13FlagHasColor     = 0x01
	preR13FlagHasLtype     = 0x02
	preR13FlagHasElevation = 0x04
	preR13FlagHasThickness = 0x08
	preR13FlagHasHandling  = 0x20
	preR13FlagHasPspace    = 0x40
	preR13FlagHasAttribs   = 0x80
)

// pre-R13 POLYLINE 专有 opts 位（LibreDWG OPTS_R11_POLYLINE_*）。
const (
	preR13OptsPolylineHasFlag       = 0x0001
	preR13OptsPolylineHasStartWidth = 0x0002
	preR13OptsPolylineHasEndWidth   = 0x0004
	preR13OptsPolylineHasExtrusion  = 0x0008
	preR13OptsPolylineHasMVerts     = 0x0010
	preR13OptsPolylineHasNVerts     = 0x0020
	preR13OptsPolylineHasMDensity   = 0x0040
	preR13OptsPolylineHasNDensity   = 0x0080
	preR13OptsPolylineHasCurvetype  = 0x0100
	preR13OptsPolylineInExtra       = 0x8000
)

// pre-R13 VERTEX 专有 opts 位（LibreDWG OPTS_R11_VERTEX_*）。
const (
	preR13OptsVertexHasStartWidth = 0x0001
	preR13OptsVertexHasEndWidth   = 0x0002
	preR13OptsVertexHasBulge      = 0x0004
	preR13OptsVertexHasFlag       = 0x0008
	preR13OptsVertexHasTangentDir = 0x0010
	preR13OptsVertexHasIndex1     = 0x0020
	preR13OptsVertexHasIndex2     = 0x0040
	preR13OptsVertexHasIndex3     = 0x0080
	preR13OptsVertexHasIndex4     = 0x0100
	preR13OptsVertexHasNotXY      = 0x4000
)

// pre-R13 POLYLINE pline_flag 位（LibreDWG FLAG_POLYLINE_*）。
const (
	preR13FlagPolyline3D        = 0x08
	preR13FlagPolylineMesh      = 0x10
	preR13FlagPolylinePfaceMesh = 0x40
)

// pre-R13 VERTEX vertex_flag 位（LibreDWG FLAG_VERTEX_*）。
const (
	preR13FlagVertex3D        = 0x20
	preR13FlagVertexMesh      = 0x40
	preR13FlagVertexPfaceMesh = 0x80
)

// preR13BlockKeyBase pre-R13 块定义在 Document.blocks 中的句柄基数。
// 块引用以 BLOCK_HEADER 表索引表达，与 R13+ 句柄空间无冲突。
const preR13BlockKeyBase = 0x100000

// preR13Table 表头（10 字节）：size RS + number RS + flags RS + address RL。
type preR13Table struct {
	size    uint16
	number  uint16
	flags   uint16
	address uint32
}

// preR13Header 文件头字段（版本串 11 字节之后，0x0B 起）。
type preR13Header struct {
	numEntitySections int
	// sections 字段值：R9/R10 为 5，R11 为 5/6（决定 num_sections 语义）
	sections     int
	numHeaderVar int
	dwgVersion   byte
	// 三个实体数据区：主实体 / 块实体 / 附加实体；块与附加实体区的
	// size 高位（0x40000000/0x80000000）为标记位，需屏蔽
	entitiesStart uint32
	entitiesEnd   uint32
	blocksStart   uint32
	blocksSize    uint32
	extrasStart   uint32
	extrasSize    uint32
	// tables 基本五表（BLOCK/LAYER/STYLE/LTYPE/VIEW，位于 0x2C）加上
	// 头变量流内读出的附加表头（UCS/VPORT/APPID/DIMSTYLE/VX）
	tables map[string]preR13Table
	// numentities R9 头变量中的实体个数（R10+ 循环由 entities_end 界定）
	numEntities int
	// codepage 头变量流 UCS 段头后的 RS 码页编号（numheader_vars>129 才有，
	// header_variables_r11.spec 的 FIELD_RS codepage）；缺失或 0 按默认 30
	// （windows-1252 家族，LibreDWG header.codepage 初始值）
	codepage uint16
}

// preR13Layer LAYER 表条目解析结果。
type preR13Layer struct {
	name  string
	color int16 // ACI 颜色索引；负值表示图层关闭
}

// preR13BlockHeader BLOCK_HEADER 表条目解析结果。
type preR13BlockHeader struct {
	name        string
	blockOffset uint32 // 块实体区内偏移（0x40000000 标记已屏蔽；0xFFFFFFFF 为模型空间占位）
}

// preR13Reader 字节对齐小端读取器（pre-R13 全部为字节流，无位压缩）。
type preR13Reader struct {
	data []byte
	pos  int
}

func (r *preR13Reader) rc() uint8 {
	v := r.data[r.pos]
	r.pos++
	return v
}

func (r *preR13Reader) rs() uint16 {
	v := binary.LittleEndian.Uint16(r.data[r.pos:])
	r.pos += 2
	return v
}

func (r *preR13Reader) rsd() int16 { return int16(r.rs()) }

func (r *preR13Reader) rl() uint32 {
	v := binary.LittleEndian.Uint32(r.data[r.pos:])
	r.pos += 4
	return v
}

func (r *preR13Reader) rd() float64 {
	v := math.Float64frombits(binary.LittleEndian.Uint64(r.data[r.pos:]))
	r.pos += 8
	return v
}

// rllBE 大端 8 字节整数（pre-R13 的 HANDSEED 与 HAS_HANDLING 句柄值）。
func (r *preR13Reader) rllBE() uint64 {
	var v uint64
	for i := 0; i < 8; i++ {
		v = v<<8 | uint64(r.rc())
	}
	return v
}

// skip 推进 n 字节（版本分支中与本实现无关的头变量占位）。
func (r *preR13Reader) skip(n int) { r.pos += n }

// bytes 取 n 字节切片并推进。
func (r *preR13Reader) bytes(n int) []byte {
	v := r.data[r.pos : r.pos+n]
	r.pos += n
	return v
}

// preR13FixName 截断定长名字缓冲的首个 \0 之前内容。
func preR13FixName(b []byte) string {
	return string(preR13TruncNul(b))
}

// preR13TruncNul 截断缓冲的首个 \0 之前内容（pre-R13 定长/变长字符串尾
// 可能带 \0 填充）。
func preR13TruncNul(b []byte) []byte {
	for i, c := range b {
		if c == 0 {
			return b[:i]
		}
	}
	return b
}

// parsePreR13Document 解析 pre-R13（R9/R10/R11）文档：
// 头部与表头定位 → 头变量步进（顺带收集附加表头）→ 表条目（层/块头）→
// 主实体区/块实体区/附加实体区逐实体解码 → Document 组装。
func parsePreR13Document(data []byte) (*Document, error) {
	version, err := container.DetectVersion(data)
	if err != nil {
		return nil, err
	}
	if len(data) < 0x5e {
		return nil, fmt.Errorf("cad: pre-R13 文件过小: %d", len(data))
	}
	doc := &Document{
		version:     version,
		blocks:      make(map[uint64][]any),
		attribs:     make(map[uint64]*entAttrib),
		layerColors: make(map[uint64]layerColor),
	}
	r := &preR13Reader{data: data}
	hdr, err := parsePreR13Header(r, version)
	if err != nil {
		return nil, err
	}

	// LAYER 表条目：flag RC + name[32] + [R11 used RS] + color RS + ltype RS
	// （size==38 时末尾多 1 字节 flag0；颜色负值即图层关闭，取绝对值）
	if tbl, ok := hdr.tables["LAYER"]; ok && tbl.number > 0 && tbl.address > 0 {
		lr := &preR13Reader{data: data, pos: int(tbl.address)}
		for i := 0; i < int(tbl.number) && lr.pos+38 <= len(data); i++ {
			layer := parsePreR13LayerEntry(lr, version, tbl.size)
			color := int(layer.color)
			if color < 0 {
				color = -color
			}
			doc.layerColors[uint64(i)] = layerColor{index: uint16(color)}
		}
	}
	// BLOCK_HEADER 表条目：flag RC + name[32] + [R11 used RS] +
	// block_offset_r11 RL + [条件 unknown RC] + [R11 block_entity RS + flag2 RS]。
	// 条目顺序即 INSERT 流内引用的块索引；块内容归属也依赖该表。
	blockHeaders := make([]preR13BlockHeader, 0, 8)
	if tbl, ok := hdr.tables["BLOCK"]; ok && tbl.number > 0 && tbl.address > 0 {
		br := &preR13Reader{data: data, pos: int(tbl.address)}
		for i := 0; i < int(tbl.number) && br.pos+38 <= len(data); i++ {
			blockHeaders = append(blockHeaders, parsePreR13BlockHeaderEntry(br, version, tbl.size))
		}
	}

	// 主实体区：全部为模型空间实体（gold entmode=2），HAS_PSPACE 实体
	// （图纸空间布局，如 ACEB10）解码后存档 pspaceSpace。
	// R11 在 entities_start 前有 16 字节 ENTITIES_BEGIN sentinel，从 start 直接解。
	agg := &preR13EntityAgg{}
	preR13CP := hdr.codepage
	if preR13CP == 0 {
		preR13CP = 30 // 缺省码页（LibreDWG header.codepage 初始值）
	}
	doc.codepage = preR13CP
	parsePreR13Entities(doc, agg, data, hdr.entitiesStart, hdr.entitiesEnd, version, blockHeaders, preR13CP)

	// 块实体区：BLOCK/ENDBLK 界定各块定义内容；块边界与
	// BLOCK_HEADER.block_offset_r11（相对 blocks_start）匹配。
	blocksSize := int(hdr.blocksSize & 0xffffff)
	if hdr.blocksStart > 0 && blocksSize > 0 && int(hdr.blocksStart)+blocksSize <= len(data) {
		parsePreR13BlockEntities(doc, &preR13EntityAgg{}, data, hdr.blocksStart, hdr.blocksStart+uint32(blocksSize), version, blockHeaders, preR13CP)
	}
	// 附加实体区（R2.0b+ 存在）：实体并入模型空间
	extrasSize := int(hdr.extrasSize & 0xffffff)
	if hdr.extrasStart > 0 && extrasSize > 0 && int(hdr.extrasStart)+extrasSize <= len(data) {
		parsePreR13Entities(doc, &preR13EntityAgg{}, data, hdr.extrasStart, hdr.extrasStart+uint32(extrasSize), version, blockHeaders, preR13CP)
	}
	preR13Archive(doc, doc.modelSpace)
	preR13Archive(doc, doc.pspaceSpace)
	for _, list := range doc.blocks {
		preR13Archive(doc, list)
	}
	return doc, nil
}

// preR13Archive pre-R13 路径的实体归档（pre-R13 无对象图、不走 classify）：
// 句柄 → entityByHandle（EntityByHandle API），ATTRIB → attribs
// （INSERT 属性展开与 Texts 渲染路径依赖）。
func preR13Archive(doc *Document, list []any) {
	for _, ent := range list {
		ec, ok := ent.(entityCommon)
		if !ok {
			continue
		}
		b := ec.common()
		if b.handle != 0 {
			if doc.entityByHandle == nil {
				doc.entityByHandle = make(map[uint64]any)
			}
			doc.entityByHandle[b.handle] = ent
		}
		if a, ok := ent.(*entAttrib); ok && a.handle != 0 {
			doc.attribs[a.handle] = a
		}
	}
}

// parsePreR13Header 解析文件头与全部表头：
// 0x0B 起头字段、0x2C 起基本五表、头变量按 numheader_vars 步进并在
// 流内收集附加表头（UCS/VPORT/APPID/DIMSTYLE/VX）。
// R11 头变量结束后有 2 字节 CRC，此处跳过不校验（错误 CRC 不阻断解析）。
func parsePreR13Header(r *preR13Reader, ver container.DwgVersion) (*preR13Header, error) {
	hdr := &preR13Header{tables: make(map[string]preR13Table, 10)}
	r.pos = 0x0b
	_ = r.rc() // maint_rel_version
	_ = r.rc() // zero_one_or_three
	hdr.numEntitySections = int(r.rs())
	hdr.sections = int(r.rs())
	hdr.numHeaderVar = int(r.rs())
	hdr.dwgVersion = r.rc()
	hdr.entitiesStart = r.rl()
	hdr.entitiesEnd = r.rl()
	hdr.blocksStart = r.rl()
	hdr.blocksSize = r.rl()
	hdr.extrasStart = r.rl()
	hdr.extrasSize = r.rl()
	for _, name := range []string{"BLOCK", "LAYER", "STYLE", "LTYPE", "VIEW"} {
		hdr.tables[name] = parsePreR13TableHdr(r)
	}
	parsePreR13HeaderVars(r, hdr, ver)
	if hdr.entitiesEnd <= hdr.entitiesStart {
		return nil, fmt.Errorf("cad: pre-R13 实体区无效: %#x-%#x", hdr.entitiesStart, hdr.entitiesEnd)
	}
	if int(hdr.entitiesEnd) > len(r.data) {
		hdr.entitiesEnd = uint32(len(r.data))
	}
	return hdr, nil
}

// parsePreR13TableHdr 读 10 字节表头。
func parsePreR13TableHdr(r *preR13Reader) preR13Table {
	var t preR13Table
	t.size = r.rs()
	t.number = r.rs()
	t.flags = r.rs()
	t.address = r.rl()
	return t
}

// parsePreR13HeaderVars 头变量区步进（header_variables_r11.spec 逐字段）。
// 数值本身渲染不需要，但必须精确推进以到达附加表头与实体区；
// numheader_vars 决定各版本读到哪一档（R9=129、R10=158/160、R11=204/205）。
func parsePreR13HeaderVars(r *preR13Reader, hdr *preR13Header, ver container.DwgVersion) {
	rd2 := func() { r.skip(16) } // 2RD
	rd3 := func() { r.skip(24) } // 3RD
	rs1 := func() { r.skip(2) }  // RS
	rc1 := func() { r.skip(1) }  // RC
	rd1 := func() { r.skip(8) }  // RD
	h2 := func() { r.skip(2) }   // HANDLE(RS)

	rd3() // INSBASE
	if ver < container.VerR10 {
		hdr.numEntities = int(r.rs()) // numentities（仅 R9）
	} else {
		rs1() // PLINEGEN
	}
	rd3()     // EXTMIN
	rd3()     // EXTMAX
	rd2()     // LIMMIN
	rd2()     // LIMMAX
	rd3()     // VIEWCTR
	rd1()     // VIEWSIZE
	rs1()     // SNAPMODE
	rd2()     // SNAPUNIT
	rd2()     // SNAPBASE
	rd1()     // SNAPANG
	rs1()     // SNAPSTYLE
	rs1()     // SNAPISOPAIR
	rs1()     // GRIDMODE
	rd2()     // GRIDUNIT
	rs1()     // ORTHOMODE
	rs1()     // REGENMODE
	rs1()     // FILLMODE
	rs1()     // QTEXTMODE
	rs1()     // DRAGMODE
	rd1()     // LTSCALE
	rd1()     // TEXTSIZE
	rd1()     // TRACEWID
	h2()      // CLAYER
	r.skip(8) // oldCECOLOR（2×RL）
	rs1()     // unknown_5
	if ver < container.VerR10 {
		rs1() // unknown_6a
		rs1() // unknown_6b
		rs1() // unknown_6c
	} else {
		rs1() // PSLTSCALE
		rs1() // TREEDEPTH
		rs1() // unknown_6
	}
	rd1()      // aspect_ratio
	rs1()      // LUNITS
	rs1()      // LUPREC
	rs1()      // AXISMODE
	rd2()      // AXISUNIT
	rd1()      // SKETCHINC
	rd1()      // FILLETRAD
	rs1()      // AUNITS
	rs1()      // AUPREC
	h2()       // TEXTSTYLE
	rs1()      // OSMODE
	rs1()      // ATTMODE
	r.skip(15) // MENU（TFv 15）
	for i := 0; i < 10; i++ {
		rd1() // DIMSCALE DIMASZ DIMEXO DIMDLI DIMEXE DIMTP DIMTM DIMTXT DIMCEN DIMTSZ
	}
	for i := 0; i < 7; i++ {
		rc1() // DIMTOL DIMLIM DIMTIH DIMTOH DIMSE1 DIMSE2 DIMTAD
	}
	if hdr.numHeaderVar <= 74 {
		return
	}
	rc1()      // LIMCHECK
	r.skip(46) // MENUEXT（TFF 46）
	rd1()      // ELEVATION
	rd1()      // THICKNESS
	for i := 0; i < 7; i++ {
		rd3() // VIEWDIR VPOINTX/Y/Z VPOINTXALT/YALT/ZALT
	}
	rs1() // flag_3d
	rs1() // BLIPMODE
	if hdr.numHeaderVar <= 83 {
		return
	}
	rc1()      // DIMZIN
	rd1()      // DIMRND
	rd1()      // DIMDLE
	r.skip(33) // DIMBLK_T
	rs1()      // circle_zoom
	rs1()      // COORDS
	rs1()      // CECOLOR
	h2()       // CELTYPE
	r.skip(32) // TDCREATE/TDUPDATE/TDINDWG/TDUSRTIMER（4×TIMERLL：RL days+RL ms）
	rs1()      // USRTIMER
	rs1()      // FASTZOOM
	rs1()      // SKPOLY
	r.skip(14) // unknown_mon/day/year/hour/min/sec/ms（7×RS）
	rd1()      // ANGBASE
	rs1()      // ANGDIR
	if hdr.numHeaderVar <= 101 {
		return
	}
	rs1() // PDMODE
	rd1() // PDSIZE
	rd1() // PLINEWID
	if hdr.numHeaderVar <= 104 {
		return
	}
	r.skip(10) // USERI1-5（5×RSd）
	for i := 0; i < 5; i++ {
		rd1() // USERR1-5
	}
	if hdr.numHeaderVar <= 114 {
		return
	}
	rc1()      // DIMALT
	rc1()      // DIMALTD
	rc1()      // DIMASO
	rc1()      // DIMSHO
	r.skip(16) // DIMPOST
	r.skip(16) // DIMAPOST
	if hdr.numHeaderVar <= 120 {
		return
	}
	rd1() // DIMALTF
	rd1() // DIMLFAC
	if hdr.numHeaderVar <= 122 {
		return
	}
	rs1() // SPLINESEGS
	rs1() // SPLFRAME
	rs1() // ATTREQ
	rs1() // ATTDIA
	rd1() // CHAMFERA
	rd1() // CHAMFERB
	rs1() // MIRRTEXT
	if hdr.numHeaderVar <= 129 {
		return // R9 到此结束
	}
	hdr.tables["UCS"] = parsePreR13TableHdr(r)
	hdr.codepage = r.rs() // codepage（RS，spec FIELD_RS codepage；文本解码用）
	rd3()                 // UCSORG
	rd3()                 // UCSXDIR
	rd3()                 // UCSYDIR
	rd3()                 // TARGET
	rd1()                 // LENSLENGTH
	rd1()                 // VIEWTWIST
	rd1()                 // FRONTZ
	rd1()                 // BACKZ
	rs1()                 // VIEWMODE
	rc1()                 // DIMTOFL
	r.skip(33)
	r.skip(33) // DIMBLK1_T/DIMBLK2_T
	rc1()      // DIMSAH
	rc1()      // DIMTIX
	rc1()      // DIMSOXD
	rd1()      // DIMTVP
	r.skip(33) // unknown_string
	rs1()      // HANDLING
	r.rllBE()  // HANDSEED（大端）
	rs1()      // SURFU
	rs1()      // SURFV
	rs1()      // SURFTYPE
	rs1()      // SURFTAB1
	rs1()      // SURFTAB2
	hdr.tables["VPORT"] = parsePreR13TableHdr(r)
	rs1() // FLATLAND
	rs1() // SPLINETYPE
	rs1() // UCSICON
	h2()  // UCSNAME
	if hdr.numHeaderVar <= 158 {
		return // R10（158 档）到此结束
	}
	hdr.tables["APPID"] = parsePreR13TableHdr(r)
	rs1() // WORLDVIEW
	if hdr.numHeaderVar <= 160 {
		return // R10（160 档）到此结束
	}
	rs1() // unknown_51e
	rs1() // unknown_520
	hdr.tables["DIMSTYLE"] = parsePreR13TableHdr(r)
	rs1() // unknown_52c
	rs1() // unknown_52e
	rc1() // unknown_530
	rs1() // DIMCLRD_C
	rs1() // DIMCLRE_C
	rs1() // DIMCLRT_C
	rs1() // SHADEDGE
	rs1() // SHADEDIF
	rs1() // unknown_59
	rs1() // UNITMODE
	rd1() // unit1_ratio
	rd1() // unit2_ratio
	rd1() // unit3_ratio
	rd1() // unit4_ratio
	for i := 0; i < 4; i++ {
		r.skip(32) // unit1-4_name
	}
	rd1() // DIMTFAC
	rd3() // PUCSORG
	rd3() // PUCSXDIR
	rd3() // PUCSYDIR
	h2()  // PUCSNAME
	rs1() // TILEMODE
	rs1() // PLIMCHECK
	rs1() // unknown_10
	rd3() // PEXTMIN
	rd3() // PEXTMAX
	rd2() // PLIMMIN
	rd2() // PLIMMAX
	rd3() // PINSBASE
	hdr.tables["VX"] = parsePreR13TableHdr(r)
	rs1() // MAXACTVP
	rd1() // DIMGAP
	rd1() // PELEVATION
	if hdr.numHeaderVar <= 204 {
		return
	}
	rs1() // VISRETAIN（R11=205 的最后一个头变量）
}

// parsePreR13LayerEntry 单条 LAYER 表记录（COMMON_TABLE_FLAGS(Layer) + 颜色）。
// R11 的记录尾含 2 字节 CRC，统一按表头 size 对齐到下一条。
func parsePreR13LayerEntry(r *preR13Reader, ver container.DwgVersion, size uint16) preR13Layer {
	start := r.pos
	var l preR13Layer
	_ = r.rc() // flag
	l.name = preR13FixName(r.bytes(32))
	if ver == container.VerR11 {
		_ = r.rsd() // used
	}
	l.color = r.rsd()
	_ = r.rs() // ltype
	if size == 38 {
		_ = r.rc() // flag0
	}
	r.pos = start + int(size)
	return l
}

// parsePreR13BlockHeaderEntry 单条 BLOCK_HEADER 表记录：名字与块偏移
// （块实体归属与 INSERT 引用的锚点）。R11 记录尾含 CRC，按 size 对齐。
func parsePreR13BlockHeaderEntry(r *preR13Reader, ver container.DwgVersion, size uint16) preR13BlockHeader {
	start := r.pos
	var b preR13BlockHeader
	_ = r.rc() // flag
	b.name = preR13FixName(r.bytes(32))
	if ver == container.VerR11 {
		_ = r.rsd() // used
	}
	b.blockOffset = r.rl()
	if b.blockOffset != 0xFFFFFFFF && b.blockOffset >= 0x40000000 {
		b.blockOffset &= 0x3fffffff
	}
	// 条件字段 unknown_r11（表头 size 为 0 或 38 时存在）
	if size == 0 || size == 38 {
		_ = r.rc()
	}
	if ver == container.VerR11 {
		_ = r.rs()  // block_entity
		_ = r.rsd() // flag2
	}
	r.pos = start + int(size)
	return b
}

// preR13EntHead 实体公共头（common_entity_data.spec PRE(R_13b1) SINCE(R_2_0b)）。
type preR13EntHead struct {
	startOff  int   // 实体记录起始偏移（type 字节处）
	rawType   uint8 // 原始类型码（≥0x80 表示已删除并入块）
	typ       uint8 // 有效类型（&0x7F）
	flag      uint8
	size      uint16 // 记录总长（R11 含记录尾 CRC），下一条实体 = startOff + size
	layerIdx  uint16
	opts      uint16
	isPspace  bool // HAS_PSPACE：图纸空间实体（不进模型空间渲染）
	colorIdx  int16
	elevation float64
	thickness float64
	handle    uint64 // HAS_HANDLING 时的显式句柄
}

// 实体公共头 extra_r11 位（HAS_PSPACE 置位时读入的附加标志字节）。
const (
	preR13ExtraHasEed      = 0x02
	preR13ExtraHasViewport = 0x04
)

// parsePreR13CommonHead 读实体公共头（含 flag/extra 展开字段）。
// 字段顺序：type/flag/size/layer/opts → [PSPACE extra] → [EED 链] →
// color → ltype → elevation → thickness → [handling 句柄] → [viewport 句柄]。
func parsePreR13CommonHead(data []byte, pos int, ver container.DwgVersion) (preR13EntHead, int) {
	var h preR13EntHead
	if pos+8 > len(data) {
		return h, 0
	}
	r := &preR13Reader{data: data, pos: pos}
	h.startOff = pos
	h.rawType = r.rc()
	h.typ = h.rawType & 0x7f
	h.flag = r.rc()
	h.size = r.rs()
	if h.typ != preR13TypeJump {
		h.layerIdx = r.rs()
		h.opts = r.rs()
	}
	// HAS_PSPACE：图纸空间标记 + 1 字节附加标志（LibreDWG 置 entmode=1）
	var extra uint8
	if h.flag&preR13FlagHasPspace != 0 {
		h.isPspace = true
		extra = r.rc()
	}
	if extra&preR13ExtraHasEed != 0 {
		// EED 链（pre-R13 单条）：RS 长度 + 2 字节应用索引 + 数据体
		if n := int(r.rs()); n >= 2 && r.pos+n <= len(r.data) {
			r.skip(n)
		} else {
			r.skip(n)
		}
	}
	if h.flag&preR13FlagHasColor != 0 {
		// color_r11 为有符号字节（RCd）：负值在 DXF 62 中表示图层关闭取反
		h.colorIdx = int16(int8(r.rc()))
	}
	if h.flag&preR13FlagHasLtype != 0 {
		if ver == container.VerR11 {
			_ = r.rs() // ltype（R11 为 2 字节表索引）
		} else {
			_ = r.rc() // R10 及以前为 1 字节
		}
	}
	// HAS_ELEVATION：R9 全类型读 elevation；R10+ 对 LINE/POINT/3DFACE/
	// 3DLINE 不读（这些类型以该位决定自身几何是否带 z）
	excluded := h.typ == preR13TypeLine || h.typ == preR13TypePoint ||
		h.typ == preR13Type3DFace || h.typ == preR13Type3DLine
	if h.flag&preR13FlagHasElevation != 0 && (ver < container.VerR10 || !excluded) {
		h.elevation = r.rd()
	}
	if h.flag&preR13FlagHasThickness != 0 {
		h.thickness = r.rd()
	}
	if h.flag&preR13FlagHasHandling != 0 {
		// 可变长句柄：RC 长度 + 大端值（bit_read_H preR13 分支）
		n := int(r.rc())
		for i := 0; i < n && i < 8; i++ {
			h.handle = h.handle<<8 | uint64(r.rc())
		}
	}
	if extra&preR13ExtraHasViewport != 0 {
		_ = r.rs() // viewport 句柄（2 字节表索引）
	}
	return h, r.pos
}

// preR13EntityAgg pre-R13 实体序列聚合器：pre-R13 无 owner 句柄流，
// 依赖记录顺序表达归属——VERTEX 跟随其 POLYLINE（至 SEQEND/JUMP/下一条
// 非顶点实体止），ATTRIB 跟随带 HAS_ATTRIBS 的 INSERT（至下一条实体止）。
type preR13EntityAgg struct {
	poly       any        // 当前 POLYLINE（2d/3d/mesh/pface）
	insert     *entInsert // 当前待挂属性的 INSERT
	insertOpen bool       // INSERT 属性收集窗口开启（HAS_ATTRIBS 置位）
}

// step 序列推进一个解码实体，维护归属窗口。
func (a *preR13EntityAgg) step(ent any, hasAttribs bool) {
	switch v := ent.(type) {
	case *entPolyline2d:
		a.poly, a.insert, a.insertOpen = v, nil, false
	case *entPolyline3d:
		a.poly, a.insert, a.insertOpen = v, nil, false
	case *entPolylineMesh:
		a.poly, a.insert, a.insertOpen = v, nil, false
	case *entPolylinePface:
		a.poly, a.insert, a.insertOpen = v, nil, false
	case *entVertex2d:
		preR13AppendOwned(a.poly, v.handle)
	case *entVertex3d:
		preR13AppendOwned(a.poly, v.handle)
	case *entVertexPface:
		preR13AppendOwned(a.poly, v.handle)
	case *entInsert:
		a.poly = nil
		a.insert, a.insertOpen = v, hasAttribs
	case *entAttrib:
		// 仅 ATTRIB（非 ATTDEF 定义）挂接 INSERT；非窗口内ATTRIB 照常独立
		if a.insertOpen && a.insert != nil && v.typeName == "ATTRIB" {
			a.insert.attribs = append(a.insert.attribs, v.handle)
		} else {
			a.insert, a.insertOpen = nil, false
		}
	default:
		a.poly, a.insert, a.insertOpen = nil, nil, false
	}
}

// preR13AppendOwned 将顶点伪句柄挂到当前 POLYLINE 的 owned 列表
// （POLYLINE_PFACE 的 entPolylinePface 无 owned 列表，顶点仅独立存在，
// 与 R13+ 该类型的模型一致）。
func preR13AppendOwned(poly any, handle uint64) {
	switch p := poly.(type) {
	case *entPolyline2d:
		p.ownedHandles = append(p.ownedHandles, handle)
	case *entPolyline3d:
		p.ownedHandles = append(p.ownedHandles, handle)
	case *entPolylineMesh:
		p.ownedHandles = append(p.ownedHandles, handle)
	}
}

// parsePreR13Entities 解析一个实体区（主实体/附加实体）：
// 逐实体读公共头并按类型解码，下一条实体地址 = 记录起点 + size，
// 与字段解析解耦（单实体偏差不破坏后续定位）。
// HAS_PSPACE（图纸空间）实体解码后存档 doc.pspaceSpace（mode=1，对齐
// R13+ classify 的图纸空间语义），不进模型空间列表。type 高位（≥0x80）
// 为「已删除并入块」标记，&0x7F 后照常解码。
func parsePreR13Entities(doc *Document, agg *preR13EntityAgg, data []byte, start, end uint32, ver container.DwgVersion, blockHeaders []preR13BlockHeader, codepage uint16) {
	if start == 0 || end <= start || int(end) > len(data) {
		return
	}
	pos := int(start)
	for pos+8 <= int(end) {
		head, next := parsePreR13CommonHead(data, pos, ver)
		if head.size == 0 || next <= pos {
			break // 无法推进（截断记录），终止避免死循环
		}
		ent := decodePreR13Entity(data, head, next, ver, blockHeaders, codepage)
		// SEQEND/JUMP 等未建模类型返回 nil，同样要推进聚合器关闭归属窗口
		agg.step(ent, head.flag&preR13FlagHasAttribs != 0)
		if ent != nil {
			if head.isPspace {
				if ec, ok := ent.(entityCommon); ok {
					ec.common().mode = 1
				}
				doc.pspaceSpace = append(doc.pspaceSpace, ent)
			} else {
				doc.modelSpace = append(doc.modelSpace, ent)
			}
		}
		pos = int(head.startOff) + int(head.size)
	}
}

// decodePreR13Entity 按类型解码实体为 R13+ 的实体类型
// （LINE/POINT/CIRCLE/ARC/TEXT/SOLID/TRACE/INSERT；其余类型按 size 跳过）。
// headEnd 为专有字段区起点（公共头结束处）；记录尾 CRC 属于 size，无需显式跳过。
func decodePreR13Entity(data []byte, h preR13EntHead, headEnd int, ver container.DwgVersion, blockHeaders []preR13BlockHeader, codepage uint16) any {
	r := &preR13Reader{data: data, pos: headEnd}
	switch h.typ {
	case preR13TypeLine:
		return decodePreR13Line(r, h, ver)
	case preR13Type3DLine:
		return decodePreR133DLine(r, h, ver)
	case preR13Type3DFace:
		return decodePreR133DFace(r, h, ver)
	case preR13TypePoint:
		return decodePreR13Point(r, h, ver)
	case preR13TypeCircle:
		return decodePreR13Circle(r, h, ver)
	case preR13TypeArc:
		return decodePreR13Arc(r, h, ver)
	case preR13TypeText:
		return decodePreR13Text(r, h, codepage)
	case preR13TypeAttdef:
		return decodePreR13Attrib(r, h, true, codepage)
	case preR13TypeAttrib:
		return decodePreR13Attrib(r, h, false, codepage)
	case preR13TypeSolid, preR13TypeTrace:
		return decodePreR13Solid(r, h)
	case preR13TypeShape:
		return decodePreR13Shape(r, h)
	case preR13TypeInsert:
		return decodePreR13Insert(r, h, ver, blockHeaders)
	case preR13TypeDimension:
		return decodePreR13Dimension(r, h, ver, codepage)
	case preR13TypePolyline:
		return decodePreR13Polyline(data, h, ver)
	case preR13TypeVertex:
		return decodePreR13Vertex(data, h, ver)
	case preR13TypeViewport:
		return decodePreR13Viewport(r, h)
	}
	return nil
}

// pre-R13 DIMENSION 类型码（flag 低 4 位，LibreDWG FLAG_R11_DIMENSION_*）。
const (
	preR13DimTypeLinear   = 0 // 线性（旋转/水平/垂直）
	preR13DimTypeAligned  = 1 // 对齐
	preR13DimTypeAng2Ln   = 2 // 角度（两线）
	preR13DimTypeDiameter = 3 // 直径
	preR13DimTypeRadius   = 4 // 半径
	preR13DimTypeAng3Pt   = 5 // 角度（三点）
	preR13DimTypeOrdinate = 6 // 坐标
)

// pre-R13 DIMENSION 专有 opts 位（LibreDWG OPTS_R11_DIMENSION_*）。
const (
	preR13OptsDimHasDXF12     = 0x0001 // clone_ins_pt（基线/连续标注）
	preR13OptsDimHasFlag      = 0x0002 // flag RC
	preR13OptsDimHasText      = 0x0004 // user_text TV
	preR13OptsDimHasDXF13     = 0x0008 // 第一扩展线点
	preR13OptsDimHasDXF14     = 0x0010 // 第二扩展线点
	preR13OptsDimHasDXF15     = 0x0020 // 第三定义点
	preR13OptsDimHasAngles    = 0x0040 // 角度线终点（2RD）
	preR13OptsDimHasDXF40     = 0x0080 // leader_len
	preR13OptsDimHasRot       = 0x0100 // dim_rotation（仅线性）
	preR13OptsDimUnknown512   = 0x0200 // oblique_angle/ext_line_rotation
	preR13OptsDimHasDXF53     = 0x0400 // text_rotation
	preR13OptsDimHasExtrusion = 0x4000
	preR13OptsDimHasDimstyle  = 0x8000
)

// preR13DimPt DIMENSION 定义点：R10+ 为 3RD，R9 为 2RD（z=elevation）。
func preR13DimPt(r *preR13Reader, h preR13EntHead, ver container.DwgVersion) point3 {
	x, y := r.rd(), r.rd()
	if ver >= container.VerR10 {
		return point3{x, y, r.rd()}
	}
	return point3{x, y, preR13Z(h)}
}

// decodePreR13Dimension DIMENSION（decode.c decode_preR13_DIMENSION）：
// 公共字段为匿名块句柄 RS + def_pt + text_midpt 2RD + [clone_ins_pt] +
// [flag RC] + [user_text TV]，再按 flag 低 4 位分派七种类型专属布局；
// 类型决定 typeName（gold entity 键），几何字段映射 R13+ 的
// entDimension 模型（point13/14/15/10、p16、textRotation 等），
// dimstyle 为 2 字节 DIMSTYLE 表索引。
func decodePreR13Dimension(r *preR13Reader, h preR13EntHead, ver container.DwgVersion, codepage uint16) *entDimension {
	e := &entDimension{}
	e.typeName = "DIMENSION_LINEAR"
	e.typeCode = preR13TypeDimension
	e.mode = 2
	preR13HandleBase(&e.baseEntity, h)
	preR13Color(&e.baseEntity, h)
	opts := h.opts
	e.anonymousBlock = uint64(r.rs()) // block HANDLE(2)
	e.point10 = preR13DimPt(r, h, ver)
	tx, ty := r.rd(), r.rd()
	e.textMidpoint = point3{tx, ty, preR13Z(h)}
	dimtype := uint8(0)
	if opts&preR13OptsDimHasDXF12 != 0 {
		cx, cy := r.rd(), r.rd()
		e.insertPoint = point3{cx, cy, preR13Z(h)}
		e.hasInsertPoint = true
	}
	if opts&preR13OptsDimHasFlag != 0 {
		dimtype = r.rc()
	}
	e.dimFlags = dimtype
	e.dimFlag = dimtype // pre-R13 flag 低 4 位即类型语义，无需 R13+ 合成
	if opts&preR13OptsDimHasText != 0 {
		e.userText = preR13TV(r, codepage)
	}
	switch dimtype & 15 {
	case preR13DimTypeLinear:
		e.typeName = "DIMENSION_LINEAR"
		if opts&preR13OptsDimHasDXF13 != 0 {
			e.point13 = preR13DimPt(r, h, ver)
		}
		if opts&preR13OptsDimHasDXF14 != 0 {
			e.point14 = preR13DimPt(r, h, ver)
		}
		if opts&preR13OptsDimHasRot != 0 {
			e.dimRotation = r.rd()
		}
		if opts&preR13OptsDimUnknown512 != 0 {
			e.extLineRotation = r.rd() // oblique_angle/ext_line_rotation
		}
		if opts&preR13OptsDimHasDXF53 != 0 {
			e.textRotation = r.rd()
		}
		if opts&preR13OptsDimHasExtrusion != 0 {
			e.extrusion = point3{r.rd(), r.rd(), r.rd()}
		}
		if opts&preR13OptsDimHasDimstyle != 0 {
			e.dimstyleHandle = uint64(r.rs())
		}
	case preR13DimTypeAligned:
		e.typeName = "DIMENSION_ALIGNED"
		if opts&preR13OptsDimHasDXF13 != 0 {
			e.point13 = preR13DimPt(r, h, ver)
		}
		if opts&preR13OptsDimHasDXF14 != 0 {
			e.point14 = preR13DimPt(r, h, ver)
		}
		if opts&preR13OptsDimHasRot != 0 {
			e.extLineRotation = r.rd() // oblique_angle（对齐型无独立转角）
		}
		if opts&preR13OptsDimHasDXF53 != 0 {
			e.textRotation = r.rd()
		}
		if opts&preR13OptsDimHasDimstyle != 0 {
			e.dimstyleHandle = uint64(r.rs())
		}
	case preR13DimTypeAng2Ln:
		e.typeName = "DIMENSION_ANG2LN"
		if opts&preR13OptsDimHasDXF13 != 0 {
			e.point13 = preR13DimPt(r, h, ver) // xline1start
		}
		if opts&preR13OptsDimHasDXF14 != 0 {
			e.point14 = preR13DimPt(r, h, ver) // xline1end
		}
		if opts&preR13OptsDimHasDXF15 != 0 {
			e.point15 = preR13DimPt(r, h, ver) // xline2start
			e.hasPoint15 = true
		}
		if opts&preR13OptsDimHasAngles != 0 {
			e.point16x, e.p16y = r.rd(), r.rd() // xline2end 2RD
			e.hasPoint16 = true
		}
		if opts&preR13OptsDimHasDXF53 != 0 {
			e.textRotation = r.rd()
		}
		if opts&preR13OptsDimHasDimstyle != 0 {
			e.dimstyleHandle = uint64(r.rs())
		}
	case preR13DimTypeDiameter:
		e.typeName = "DIMENSION_DIAMETER"
		if opts&preR13OptsDimHasDXF15 != 0 {
			// first_arc_pt：R10 且无 HAS_ELEVATION 时 3RD，否则 2RD
			x, y := r.rd(), r.rd()
			if ver >= container.VerR10 && h.flag&preR13FlagHasElevation == 0 {
				e.point15 = point3{x, y, r.rd()}
			} else {
				e.point15 = point3{x, y, preR13Z(h)}
			}
			e.hasPoint15 = true
		}
		if opts&preR13OptsDimHasDXF40 != 0 {
			_ = r.rd() // leader_len
		}
		if opts&preR13OptsDimHasDXF53 != 0 {
			e.textRotation = r.rd()
		}
		if opts&preR13OptsDimHasExtrusion != 0 {
			e.extrusion = point3{r.rd(), r.rd(), r.rd()}
		}
		if opts&preR13OptsDimHasDimstyle != 0 {
			e.dimstyleHandle = uint64(r.rs())
		}
	case preR13DimTypeRadius:
		e.typeName = "DIMENSION_RADIUS"
		if opts&preR13OptsDimHasDXF15 != 0 {
			e.point15 = preR13DimPt(r, h, ver) // first_arc_pt
			e.hasPoint15 = true
		}
		if opts&preR13OptsDimHasDXF40 != 0 {
			_ = r.rd() // leader_len
		}
		if opts&preR13OptsDimHasDXF53 != 0 {
			e.textRotation = r.rd()
		}
		if opts&preR13OptsDimHasExtrusion != 0 {
			e.extrusion = point3{r.rd(), r.rd(), r.rd()}
		}
		if opts&preR13OptsDimHasDimstyle != 0 {
			e.dimstyleHandle = uint64(r.rs())
		}
	case preR13DimTypeAng3Pt:
		e.typeName = "DIMENSION_ANG3PT"
		if opts&preR13OptsDimHasDXF13 != 0 {
			e.point13 = preR13DimPt(r, h, ver) // xline1_pt
		}
		if opts&preR13OptsDimHasDXF14 != 0 {
			e.point14 = preR13DimPt(r, h, ver) // xline2_pt
		}
		if opts&preR13OptsDimHasDXF15 != 0 {
			e.point15 = preR13DimPt(r, h, ver) // center_pt
			e.hasPoint15 = true
		}
		if opts&preR13OptsDimHasAngles != 0 {
			e.point16x, e.p16y = r.rd(), r.rd() // xline2end 2RD
			e.hasPoint16 = true
		}
		if opts&preR13OptsDimHasDXF53 != 0 {
			e.textRotation = r.rd()
		}
		if opts&preR13OptsDimHasDimstyle != 0 {
			e.dimstyleHandle = uint64(r.rs())
		}
	case preR13DimTypeOrdinate:
		e.typeName = "DIMENSION_ORDINATE"
		if opts&preR13OptsDimHasDXF13 != 0 {
			e.point13 = preR13DimPt(r, h, ver) // feature_location_pt
		}
		if opts&preR13OptsDimHasDXF14 != 0 {
			e.point14 = preR13DimPt(r, h, ver) // leader_endpt
		}
		if opts&preR13OptsDimHasDXF53 != 0 {
			e.textRotation = r.rd()
		}
		if opts&preR13OptsDimHasDimstyle != 0 {
			e.dimstyleHandle = uint64(r.rs())
		}
	}
	return e
}

// preR13PlineFlagOffset POLYLINE 预扫描：定位 pline_flag 字节在记录内的
// 偏移（decode.c Detect polyline 分支，展开公共字段步进；字段顺序按该
// 参考实现：PSPACE extra → color → ltype → thickness → elevation → EED →
// handling → viewport）。opts 无 HAS_FLAG 位时返回 -1（无 flag 字节，
// 恒 POLYLINE_2D）。data 为自记录 type 字节起的切片。
func preR13PlineFlagOffset(data []byte, ver container.DwgVersion) int {
	r := &preR13Reader{data: data}
	_ = r.rc() // type
	flag := r.rc()
	r.skip(4) // size RS + layer RS
	opts := r.rs()
	if opts&preR13OptsPolylineHasFlag == 0 {
		return -1
	}
	var extra uint8
	if flag&preR13FlagHasPspace != 0 {
		extra = r.rc()
	}
	if flag&preR13FlagHasColor != 0 {
		_ = r.rc()
	}
	if flag&preR13FlagHasLtype != 0 {
		if ver == container.VerR11 {
			r.skip(2)
		} else {
			r.skip(1)
		}
	}
	if flag&preR13FlagHasThickness != 0 {
		r.skip(8)
	}
	if flag&preR13FlagHasElevation != 0 {
		r.skip(8)
	}
	if extra&preR13ExtraHasEed != 0 {
		n := int(r.rs())
		r.skip(n)
	}
	if flag&preR13FlagHasHandling != 0 {
		n := int(r.rc())
		r.skip(n)
	}
	if extra&preR13ExtraHasViewport != 0 {
		r.skip(2)
	}
	return r.pos
}

// decodePreR13Polyline POLYLINE（type 19，pre-R13 单类型多变体）：先预扫描
// pline_flag 决定变体（3D/MESH/PFACE/2D），再按公共头（spec 顺序）展开后
// 读各变体专有字段。opts 位在各变体间语义不同（dwg.spec 各 PRE(R_13b1)
// 分支），已删除实体（type 高位置位）同规则处理。
func decodePreR13Polyline(data []byte, h preR13EntHead, ver container.DwgVersion) any {
	flagOff := preR13PlineFlagOffset(data[h.startOff:], ver)
	plineFlag := uint8(0)
	if flagOff >= 0 {
		plineFlag = data[h.startOff+flagOff]
	}
	// 真实解码：公共头展开（含 PSPACE/EED/color/ltype/elevation/thickness/
	// handling/viewport 完整顺序）后接专有字段
	_, bodyPos := parsePreR13CommonHead(data, h.startOff, ver)
	r := &preR13Reader{data: data, pos: bodyPos}
	opts := h.opts
	switch {
	case plineFlag&preR13FlagPolyline3D != 0:
		e := &entPolyline3d{}
		e.typeName = "POLYLINE_3D"
		if opts&preR13OptsPolylineHasFlag != 0 {
			e.flags70 = r.rc()
		}
		if opts&preR13OptsPolylineHasStartWidth != 0 {
			_ = r.rd() // start_width
		}
		if opts&preR13OptsPolylineHasEndWidth != 0 {
			_ = r.rd() // end_width
		}
		if opts&preR13OptsPolylineHasExtrusion != 0 {
			r.skip(24) // extrusion 3RD
		}
		if opts&preR13OptsPolylineHasCurvetype != 0 {
			e.flags75 = uint8(r.rs()) // curve_type（3D 网格）
		}
		e.typeCode = preR13TypePolyline
		e.mode = 2
		preR13HandleBase(&e.baseEntity, h)
		preR13Color(&e.baseEntity, h)
		return e
	case plineFlag&preR13FlagPolylineMesh != 0:
		e := &entPolylineMesh{}
		e.typeName = "POLYLINE_MESH"
		if opts&preR13OptsPolylineHasFlag != 0 {
			e.flags = uint16(r.rc())
		}
		if opts&preR13OptsPolylineHasMVerts != 0 {
			e.mVertexCount = r.rs()
		}
		if opts&preR13OptsPolylineHasNVerts != 0 {
			e.nVertexCount = r.rs()
		}
		if opts&preR13OptsPolylineHasMDensity != 0 {
			e.mDensity = r.rs()
		}
		if opts&preR13OptsPolylineHasNDensity != 0 {
			e.nDensity = r.rs()
		}
		if opts&preR13OptsPolylineHasCurvetype != 0 {
			e.curveType = r.rs()
		}
		e.typeCode = preR13TypePolyline
		e.mode = 2
		preR13HandleBase(&e.baseEntity, h)
		preR13Color(&e.baseEntity, h)
		return e
	case plineFlag&preR13FlagPolylinePfaceMesh != 0:
		e := &entPolylinePface{}
		e.typeName = "POLYLINE_PFACE"
		if opts&preR13OptsPolylineHasFlag != 0 {
			_ = r.rc() // flag
		}
		if opts&preR13OptsPolylineHasMVerts != 0 {
			e.numVertices = int(r.rs()) // numverts（gold 键 71）
		}
		if opts&preR13OptsPolylineHasNVerts != 0 {
			e.numFaces = int(r.rs()) // numfaces（gold 键 72）
		}
		e.typeCode = preR13TypePolyline
		e.mode = 2
		preR13HandleBase(&e.baseEntity, h)
		preR13Color(&e.baseEntity, h)
		return e
	default:
		e := &entPolyline2d{}
		e.typeName = "POLYLINE_2D"
		if opts&preR13OptsPolylineHasFlag != 0 {
			e.flags = uint16(r.rc())
		}
		if opts&preR13OptsPolylineHasStartWidth != 0 {
			e.widthStart = r.rd()
		}
		if opts&preR13OptsPolylineHasEndWidth != 0 {
			e.widthEnd = r.rd()
		}
		if opts&preR13OptsPolylineHasExtrusion != 0 {
			e.extrusion = point3{r.rd(), r.rd(), r.rd()}
		}
		if opts&preR13OptsPolylineHasMVerts != 0 {
			_ = r.rs() // num_m_verts（顶点以记录顺序聚合表达）
		}
		if opts&preR13OptsPolylineHasNVerts != 0 {
			_ = r.rs() // num_n_verts
		}
		if opts&preR13OptsPolylineHasCurvetype != 0 {
			e.curveType = r.rs()
		}
		if opts&preR13OptsPolylineInExtra != 0 && h.size > 20 {
			// 附加实体区引用文本：记录尾（R11 扣 2 字节 CRC）前剩余整段
			n := int(h.size) - (r.pos - h.startOff)
			if ver == container.VerR11 {
				n -= 2
			}
			if n > 0 {
				r.skip(n)
			}
		}
		e.elevation = h.elevation
		e.thickness = h.thickness
		e.typeCode = preR13TypePolyline
		e.mode = 2
		preR13HandleBase(&e.baseEntity, h)
		preR13Color(&e.baseEntity, h)
		return e
	}
}

// preR13VertexFlagOffset VERTEX 预扫描：定位 vertex_flag 字节偏移
// （decode.c Detect vertex 分支；字段顺序按该参考实现：color → ltype →
// thickness → elevation → PSPACE extra → EED → handling → viewport →
// [x,y] → [start_width] → [end_width] → [bulge]）。
func preR13VertexFlagOffset(data []byte, ver container.DwgVersion) (int, uint16) {
	r := &preR13Reader{data: data}
	_ = r.rc() // type（与 parsePreR13CommonHead 的头布局对齐）
	flag := r.rc()
	r.skip(4) // size RS + layer RS
	opts := r.rs()
	var extra uint8
	if flag&preR13FlagHasColor != 0 {
		_ = r.rc()
	}
	if flag&preR13FlagHasLtype != 0 {
		if ver == container.VerR11 {
			r.skip(2)
		} else {
			r.skip(1)
		}
	}
	if flag&preR13FlagHasThickness != 0 {
		r.skip(8)
	}
	if flag&preR13FlagHasElevation != 0 {
		r.skip(8)
	}
	if flag&preR13FlagHasPspace != 0 {
		extra = r.rc()
	}
	if extra != 0 && extra&preR13ExtraHasEed != 0 {
		n := int(r.rs())
		r.skip(n)
	}
	if flag&preR13FlagHasHandling != 0 {
		n := int(r.rc())
		r.skip(n)
	}
	if extra != 0 && extra&preR13ExtraHasViewport != 0 {
		r.skip(2)
	}
	if opts&preR13OptsVertexHasNotXY == 0 {
		r.skip(16) // point 2RD
	}
	if opts&preR13OptsVertexHasStartWidth != 0 {
		r.skip(8)
	}
	if opts&preR13OptsVertexHasEndWidth != 0 {
		r.skip(8)
	}
	if opts&preR13OptsVertexHasBulge != 0 {
		r.skip(8)
	}
	return r.pos, opts
}

// decodePreR13Vertex VERTEX（type 20，pre-R13 单类型多变体）：预扫描
// vertex_flag 决定变体（MESH|PFACE_MESH→PFACE、MESH→MESH、
// PFACE_MESH→PFACE_FACE、3D→3D、默认 2D），再按公共头展开后读专有字段。
func decodePreR13Vertex(data []byte, h preR13EntHead, ver container.DwgVersion) any {
	flagOff, opts := preR13VertexFlagOffset(data[h.startOff:], ver)
	vertexFlag := uint8(0)
	if opts&preR13OptsVertexHasFlag != 0 && flagOff >= 0 && h.startOff+flagOff < len(data) {
		vertexFlag = data[h.startOff+flagOff]
	}
	_, bodyPos := parsePreR13CommonHead(data, h.startOff, ver)
	r := &preR13Reader{data: data, pos: bodyPos}
	readPoint := func() point3 {
		x, y := r.rd(), r.rd()
		return point3{x, y, preR13Z(h)}
	}
	base := func() baseEntity {
		var b baseEntity
		b.typeCode = preR13TypeVertex
		b.mode = 2
		preR13HandleBase(&b, h)
		preR13Color(&b, h)
		return b
	}
	switch {
	case vertexFlag&preR13FlagVertexMesh != 0 && vertexFlag&preR13FlagVertexPfaceMesh != 0:
		e := &entVertexPface{}
		e.baseEntity = base()
		e.typeName = "VERTEX_PFACE"
		if opts&preR13OptsVertexHasNotXY == 0 {
			e.position = readPoint()
		}
		if opts&preR13OptsVertexHasFlag != 0 {
			e.flag = r.rc()
		}
		return e
	case vertexFlag&preR13FlagVertexMesh != 0:
		e := &entVertexPface{}
		e.baseEntity = base()
		e.typeName = "VERTEX_MESH"
		e.position = readPoint()
		e.flag = r.rc() // VERTEX_MESH 的 flag 无条件存在
		return e
	case vertexFlag&preR13FlagVertexPfaceMesh != 0:
		e := &entVertexPfaceFace{}
		e.baseEntity = base()
		e.typeName = "VERTEX_PFACE_FACE"
		if opts&preR13OptsVertexHasFlag != 0 {
			e.flag = r.rc()
		}
		if opts&preR13OptsVertexHasIndex1 != 0 {
			e.vertind[0] = int32(int16(r.rs()))
		}
		if opts&preR13OptsVertexHasIndex2 != 0 {
			e.vertind[1] = int32(int16(r.rs()))
		}
		if opts&preR13OptsVertexHasIndex3 != 0 {
			e.vertind[2] = int32(int16(r.rs()))
		}
		if opts&preR13OptsVertexHasIndex4 != 0 {
			e.vertind[3] = int32(int16(r.rs()))
		}
		return e
	case vertexFlag&preR13FlagVertex3D != 0:
		e := &entVertex3d{}
		e.baseEntity = base()
		e.typeName = "VERTEX_3D"
		e.position = readPoint()
		if opts&preR13OptsVertexHasFlag != 0 {
			e.flags = r.rc()
		}
		return e
	default:
		e := &entVertex2d{}
		e.baseEntity = base()
		e.typeName = "VERTEX_2D"
		e.position = readPoint()
		if opts&preR13OptsVertexHasStartWidth != 0 {
			e.startWidth = r.rd()
		}
		if opts&preR13OptsVertexHasEndWidth != 0 {
			e.endWidth = r.rd()
		}
		if opts&preR13OptsVertexHasBulge != 0 {
			e.bulge = r.rd()
		}
		if opts&preR13OptsVertexHasFlag != 0 {
			e.flags = uint16(r.rc())
		}
		if opts&preR13OptsVertexHasTangentDir != 0 {
			e.tangentDir = r.rd()
		}
		return e
	}
}

// decodePreR13Viewport VIEWPORT 视口（dwg.spec PRE(R_13b1) 分支）：
// center 3RD + width RD + height RD + id RS。仅解码不渲染
// （与 R13+ VIEWPORT 渲染策略一致），id 无模型字段挂 extra。
func decodePreR13Viewport(r *preR13Reader, h preR13EntHead) *entViewport {
	e := &entViewport{}
	e.typeName = "VIEWPORT"
	e.typeCode = preR13TypeViewport
	e.mode = 2
	preR13HandleBase(&e.baseEntity, h)
	preR13Color(&e.baseEntity, h)
	cx, cy, cz := r.rd(), r.rd(), r.rd()
	e.center = point3{cx, cy, cz}
	e.width = r.rd()
	e.height = r.rd()
	id := r.rs()
	if e.extra == nil {
		e.extra = map[string]any{}
	}
	e.extra["id"] = id
	return e
}

// decodePreR13Shape SHAPE 形参照（dwg.spec VERSIONS(R_2_0,R_11) 分支）：
// 插入点 2RD + 缩放 RD + style_id RC，opts 位展开旋转/样式句柄/
// 宽度因子/倾斜角；thickness 取公共头（HAS_THICKNESS），插入点 z 取
// 公共头 elevation（spec DECODER 显式回填语义，同 TEXT）。
func decodePreR13Shape(r *preR13Reader, h preR13EntHead) *entShape {
	e := &entShape{}
	e.typeName = "SHAPE"
	e.typeCode = preR13TypeShape
	e.mode = 2
	preR13HandleBase(&e.baseEntity, h)
	preR13Color(&e.baseEntity, h)
	ix, iy := r.rd(), r.rd()
	e.scale = r.rd()
	e.shapeNo = uint16(r.rc()) // style_id：SHAPEFILE 内形编号（1 字节表索引）
	if h.opts&0x01 != 0 {
		e.rotation = r.rd() // rotation（弧度，gold 0.5236 = 30°）
	}
	if h.opts&0x02 != 0 {
		_ = r.rc() // style（HAS_LOAD_NUM，1 字节 STYLE 表索引）
	}
	if h.opts&0x04 != 0 {
		e.widthFactor = r.rd()
	}
	if h.opts&0x08 != 0 {
		e.oblique = r.rd()
	}
	e.insertion = point3{ix, iy, preR13Z(h)}
	e.thickness = h.thickness
	return e
}

// preR13Color 应用实体颜色与图层索引到公共字段（ACI 索引；0/256 语义
// 由 colorResolved 统一处理，这里原样带过）。
func preR13Color(base *baseEntity, h preR13EntHead) {
	base.layer = uint64(h.layerIdx)
	if h.flag&preR13FlagHasColor != 0 && h.colorIdx > 0 {
		base.color.index = uint16(h.colorIdx)
		base.color.hasIndex = true
	}
}

// preR13Z elevation_r11 的 z 补齐：pre-R13 的 2RD 坐标 z 来自公共头 elevation。
func preR13Z(h preR13EntHead) float64 { return h.elevation }

// decodePreR13Line LINE：R9 恒 2RD×2；R10+ 以 HAS_ELEVATION 位区分
// 2RD×2（z=elevation）与 3RD×2。
func decodePreR13Line(r *preR13Reader, h preR13EntHead, ver container.DwgVersion) *entLine {
	e := &entLine{}
	e.typeName = "LINE"
	e.typeCode = preR13TypeLine
	e.mode = 2
	preR13HandleBase(&e.baseEntity, h)
	preR13Color(&e.baseEntity, h)
	is3d := ver >= container.VerR10 && h.flag&preR13FlagHasElevation == 0
	if is3d {
		e.start = point3{r.rd(), r.rd(), r.rd()}
		e.end = point3{r.rd(), r.rd(), r.rd()}
	} else {
		e.start = point3{r.rd(), r.rd(), preR13Z(h)}
		e.end = point3{r.rd(), r.rd(), preR13Z(h)}
	}
	return e
}

// decodePreR133DLine 3DLINE（dwg.spec _3DLINE）：R9（R_2_4~R_9c1）以 opts
// 位逐点选择 3RD（bit0 起点、bit1 终点）否则 2RD（z=elevation）；R10+ 以
// HAS_ELEVATION 位区分（置位 2RD×2、否则 3RD×2），opts bit0 为挤出方向。
// 复用 entLine 模型（R13+ 渲染直连）。
func decodePreR133DLine(r *preR13Reader, h preR13EntHead, ver container.DwgVersion) *entLine {
	e := &entLine{}
	e.typeName = "3DLINE"
	e.typeCode = preR13Type3DLine
	e.mode = 2
	preR13HandleBase(&e.baseEntity, h)
	preR13Color(&e.baseEntity, h)
	if ver < container.VerR10 {
		if h.opts&0x01 != 0 {
			e.start = point3{r.rd(), r.rd(), r.rd()}
		} else {
			e.start = point3{r.rd(), r.rd(), preR13Z(h)}
		}
		if h.opts&0x02 != 0 {
			e.end = point3{r.rd(), r.rd(), r.rd()}
		} else {
			e.end = point3{r.rd(), r.rd(), preR13Z(h)}
		}
		return e
	}
	is2d := h.flag&preR13FlagHasElevation != 0
	if is2d {
		e.start = point3{r.rd(), r.rd(), preR13Z(h)}
		e.end = point3{r.rd(), r.rd(), preR13Z(h)}
	} else {
		e.start = point3{r.rd(), r.rd(), r.rd()}
		e.end = point3{r.rd(), r.rd(), r.rd()}
	}
	if h.opts&0x01 != 0 {
		r.skip(24) // extrusion 3RD
	}
	return e
}

// decodePreR133DFace 3DFACE（dwg.spec _3DFACE）：R9 以 opts 四个位逐角点
// 选择 3RD（bit0~bit3 对应角 1~4）否则 2RD（z=elevation）；R10+ 以
// HAS_ELEVATION 位区分 2RD×4/3RD×4，opts bit0 为不可见边标志 RS。
// 复用 entFace3d 模型（R13+ 渲染直连）。
func decodePreR133DFace(r *preR13Reader, h preR13EntHead, ver container.DwgVersion) *entFace3d {
	e := &entFace3d{}
	e.typeName = "3DFACE"
	e.typeCode = preR13Type3DFace
	e.mode = 2
	preR13HandleBase(&e.baseEntity, h)
	preR13Color(&e.baseEntity, h)
	// hasZ：该角点是否带显式 z（R9 按 opts 位、R10+ 按 HAS_ELEVATION 全局位）
	hasZ := func(cornerBit uint16) bool {
		if ver < container.VerR10 {
			return h.opts&cornerBit != 0
		}
		return h.flag&preR13FlagHasElevation == 0
	}
	readCorner := func(cornerBit uint16) point3 {
		x, y := r.rd(), r.rd()
		if hasZ(cornerBit) {
			return point3{x, y, r.rd()}
		}
		return point3{x, y, preR13Z(h)}
	}
	e.p1 = readCorner(0x01)
	e.p2 = readCorner(0x02)
	e.p3 = readCorner(0x04)
	e.p4 = readCorner(0x08)
	// 不可见边标志仅 R10+ 存在（R9 的 opts 位被角点 z 占用）
	if ver >= container.VerR10 && h.opts&0x01 != 0 {
		e.invisibleEdgeFlags = r.rs()
	}
	return e
}

// decodePreR13Point POINT：x/y 恒 RD；z 仅 R10+ 且无 HAS_ELEVATION 时存在。
func decodePreR13Point(r *preR13Reader, h preR13EntHead, ver container.DwgVersion) *entPoint {
	e := &entPoint{}
	e.typeName = "POINT"
	e.typeCode = preR13TypePoint
	e.mode = 2
	preR13HandleBase(&e.baseEntity, h)
	preR13Color(&e.baseEntity, h)
	x, y := r.rd(), r.rd()
	z := 0.0
	if ver >= container.VerR10 && h.flag&preR13FlagHasElevation == 0 {
		z = r.rd() // z（DXF 30，仅 R10+ 且无 HAS_ELEVATION）
	}
	e.location = point3{x, y, z}
	if h.opts&0x01 != 0 {
		r.skip(24) // extrusion 3RD
	}
	if h.opts&0x02 != 0 {
		e.rotation = r.rd() // x_ang（弧度，pre-R13 角度字段与 R13+ 同为弧度口径）
	}
	return e
}

// decodePreR13Circle CIRCLE：center 2RD + radius RD。
func decodePreR13Circle(r *preR13Reader, h preR13EntHead, ver container.DwgVersion) *entCircle {
	e := &entCircle{}
	e.typeName = "CIRCLE"
	e.typeCode = preR13TypeCircle
	e.mode = 2
	preR13HandleBase(&e.baseEntity, h)
	preR13Color(&e.baseEntity, h)
	cx, cy := r.rd(), r.rd()
	e.radius = r.rd()
	e.center = point3{cx, cy, preR13Z(h)}
	if h.opts&0x01 != 0 {
		r.skip(24) // extrusion 3RD
	}
	if h.opts&0x02 != 0 {
		e.center.z = r.rd() // center.z 显式字段（DXF 38）
	}
	return e
}

// decodePreR13Arc ARC：center 2RD + radius + 起终角（度 → 弧度对齐 R13+）。
func decodePreR13Arc(r *preR13Reader, h preR13EntHead, ver container.DwgVersion) *entArc {
	e := &entArc{}
	e.typeName = "ARC"
	e.typeCode = preR13TypeArc
	e.mode = 2
	preR13HandleBase(&e.baseEntity, h)
	preR13Color(&e.baseEntity, h)
	cx, cy := r.rd(), r.rd()
	e.radius = r.rd()
	// pre-R13 角度字段即弧度（gold start=4.712=270°），与 R13+ 口径一致
	e.angleStart = r.rd()
	e.angleEnd = r.rd()
	e.center = point3{cx, cy, preR13Z(h)}
	if h.opts&0x01 != 0 {
		r.skip(24) // extrusion 3RD
	}
	if h.opts&0x02 != 0 {
		e.center.z = r.rd() // center.z（DXF 30）
	}
	return e
}

// decodePreR13Text TEXT：插入点 + 字高 + 变长文字 + opts 位展开的可选字段
// （rotation/宽度因子/样式/生成标志/水平对齐/对齐点/挤出/垂直对齐）。
// codepage 为头变量流的码页编号（文字按其解码，30 默认 Latin-1 近似）。
func decodePreR13Text(r *preR13Reader, h preR13EntHead, codepage uint16) *entText {
	e := &entText{}
	e.typeName = "TEXT"
	e.typeCode = preR13TypeText
	e.mode = 2
	preR13HandleBase(&e.baseEntity, h)
	preR13Color(&e.baseEntity, h)
	ix, iy := r.rd(), r.rd()
	e.height = r.rd()
	e.insertion = point3{ix, iy, preR13Z(h)}
	n := int(r.rs())
	if n > 0 && r.pos+n <= len(r.data) {
		// pre-R2000 字符串尾可能带 \0 填充，截断
		e.text = bitstream.DecodeCodepage(preR13TruncNul(r.bytes(n)), codepage)
	} else {
		r.skip(n)
	}
	if h.opts&0x01 != 0 {
		e.rotation = r.rd() // rotation（弧度口径）
	}
	if h.opts&0x02 != 0 {
		_ = r.rd() // width_factor
	}
	if h.opts&0x04 != 0 {
		_ = r.rd() // oblique_angle
	}
	if h.opts&0x08 != 0 {
		_ = r.rc() // style（HANDLE code=1，1 字节表索引）
	}
	if h.opts&0x10 != 0 {
		e.gen = uint16(r.rc()) // generation
	}
	if h.opts&0x20 != 0 {
		e.hAlign = uint16(r.rc()) // horiz_alignment
	}
	if h.opts&0x40 != 0 {
		ax, ay := r.rd(), r.rd()
		e.alignPt = &point2{ax, ay}
	}
	if h.opts&0x80 != 0 {
		r.skip(24) // extrusion 3RD
	}
	if h.opts&0x100 != 0 {
		e.vAlign = uint16(r.rc()) // vert_alignment
	}
	return e
}

// preR13TV pre-R13 变长字符串（RS 长度 + 字节体，尾 \0 截断），按码页解码。
func preR13TV(r *preR13Reader, codepage uint16) string {
	n := int(r.rs())
	if n <= 0 {
		return ""
	}
	if r.pos+n > len(r.data) {
		r.skip(n)
		return ""
	}
	return bitstream.DecodeCodepage(preR13TruncNul(r.bytes(n)), codepage)
}

// decodePreR13Attrib ATTRIB/ATTDEF（dwg.spec PRE(R_13b1) 分支）：插入点 2RD +
// 字高 RD + 文字 TV + [ATTDEF 提示 TV] + 标签 TV + flags RC，opts 位展开
// 旋转/宽度因子/倾斜角/样式/生成标志/水平对齐/对齐点/挤出/垂直对齐。
// ATTDEF 的文字为 default_value（gold default_value 键），ATTRIB 为
// text_value；插入点 z 取公共头 elevation（同 TEXT 口径）。
func decodePreR13Attrib(r *preR13Reader, h preR13EntHead, attdef bool, codepage uint16) *entAttrib {
	e := &entAttrib{}
	if attdef {
		e.typeName = "ATTDEF"
	} else {
		e.typeName = "ATTRIB"
	}
	e.typeCode = uint16(h.typ)
	e.mode = 2
	preR13HandleBase(&e.baseEntity, h)
	preR13Color(&e.baseEntity, h)
	ix, iy := r.rd(), r.rd()
	e.height = r.rd()
	e.text = preR13TV(r, codepage)
	if attdef {
		e.prompt = preR13TV(r, codepage) // prompt（仅 ATTDEF）
	}
	e.tag = preR13TV(r, codepage)
	// flags：1 不可见 2 常量 4 校验 8 预置（无对应模型字段，仅推进流）
	_ = r.rc()
	if h.opts&0x02 != 0 {
		e.rotation = r.rd()
	}
	if h.opts&0x04 != 0 {
		_ = r.rd() // width_factor
	}
	if h.opts&0x08 != 0 {
		_ = r.rd() // oblique_angle
	}
	if h.opts&0x10 != 0 {
		_ = r.rc() // style（1 字节 STYLE 表索引）
	}
	if h.opts&0x20 != 0 {
		e.gen = uint16(r.rc()) // generation
	}
	if h.opts&0x40 != 0 {
		e.hAlign = uint16(r.rc()) // horiz_alignment
	}
	if h.opts&0x80 != 0 {
		_ = r.rd() // alignment_pt x
		_ = r.rd() // alignment_pt y
	}
	if h.opts&0x100 != 0 {
		r.skip(24) // extrusion 3RD
	}
	if h.opts&0x200 != 0 {
		e.vAlign = uint16(r.rc()) // vert_alignment
	}
	e.insertion = point3{ix, iy, preR13Z(h)}
	return e
}

// decodePreR13Solid SOLID/TRACE：四角 2RD（z=elevation，单独 elevation 字段）。
func decodePreR13Solid(r *preR13Reader, h preR13EntHead) *entSolid {
	e := &entSolid{trace: h.typ == preR13TypeTrace}
	if e.trace {
		e.typeName = "TRACE"
	} else {
		e.typeName = "SOLID"
	}
	e.typeCode = uint16(h.typ)
	e.mode = 2
	preR13HandleBase(&e.baseEntity, h)
	preR13Color(&e.baseEntity, h)
	z := preR13Z(h)
	e.elevation = z
	e.p1 = point2{r.rd(), r.rd()}
	e.p2 = point2{r.rd(), r.rd()}
	e.p3 = point2{r.rd(), r.rd()}
	e.p4 = point2{r.rd(), r.rd()}
	if h.opts&0x01 != 0 {
		r.skip(24) // extrusion 3RD
	}
	if h.opts&0x02 != 0 {
		e.elevation = r.rd() // 显式 elevation（DXF 38）
	}
	return e
}

// decodePreR13Insert INSERT：块表索引引用 + 插入点 + 按位展开的
// 缩放/旋转/阵列参数。scale 缺省 1.0（仅 opts 置位时存储）。
func decodePreR13Insert(r *preR13Reader, h preR13EntHead, ver container.DwgVersion, blockHeaders []preR13BlockHeader) *entInsert {
	e := &entInsert{}
	e.typeName = "INSERT"
	e.typeCode = preR13TypeInsert
	e.mode = 2
	preR13HandleBase(&e.baseEntity, h)
	preR13Color(&e.baseEntity, h)
	idx := r.rs() // block_header（BLOCK_HEADER 表索引）
	if int(idx) < len(blockHeaders) {
		e.blockHeader = preR13BlockKeyBase + uint64(idx)
	}
	ix, iy := r.rd(), r.rd()
	e.position = point3{ix, iy, preR13Z(h)}
	e.scale = point3{1, 1, 1}
	if h.opts&0x01 != 0 {
		e.scale.x = r.rd()
	}
	if h.opts&0x02 != 0 {
		e.scale.y = r.rd()
	}
	if h.opts&0x04 != 0 {
		e.rotation = r.rd() // 弧度（gold 0.5236 = 30°）
	}
	if h.opts&0x08 != 0 {
		e.scale.z = r.rd()
	}
	if h.opts&0x10 != 0 {
		_ = r.rs() // num_cols（MINSERT 阵列，渲染按单次插入处理）
	}
	if h.opts&0x20 != 0 {
		_ = r.rs() // num_rows
	}
	if h.opts&0x40 != 0 {
		_ = r.rd() // col_spacing
	}
	if h.opts&0x80 != 0 {
		_ = r.rd() // row_spacing
	}
	if h.opts&0x100 != 0 {
		r.skip(24) // extrusion 3RD
	}
	return e
}

// preR13HandleBase pre-R13 实体无全局句柄流：HAS_HANDLING 显式句柄优先，
// 否则以记录偏移作稳定伪句柄（同一次解析内唯一，满足 entityByHandle 归档）。
func preR13HandleBase(base *baseEntity, h preR13EntHead) {
	if h.handle != 0 {
		base.handle = uint64(h.handle)
	} else {
		base.handle = uint64(h.startOff)
	}
}

// parsePreR13BlockEntities 块实体区：BLOCK（type 12）开启一个块定义内容，
// 与 BLOCK_HEADER.block_offset_r11（块区内偏移）匹配归属，ENDBLK（13）收尾。
// 中间实体挂到 Document.blocks[preR13BlockKeyBase+表索引] 供 INSERT 展开。
func parsePreR13BlockEntities(doc *Document, agg *preR13EntityAgg, data []byte, start, end uint32, ver container.DwgVersion, blockHeaders []preR13BlockHeader, codepage uint16) {
	pos := int(start)
	var curKey uint64
	for pos+8 <= int(end) {
		head, next := parsePreR13CommonHead(data, pos, ver)
		if head.size == 0 || next <= pos {
			break
		}
		switch head.typ {
		case preR13TypeBlock:
			// 块边界锚点：BLOCK 记录起点相对块区起点的偏移
			offset := uint64(head.startOff) - uint64(start)
			curKey = 0
			for i, bh := range blockHeaders {
				if bh.blockOffset != 0xFFFFFFFF && uint64(bh.blockOffset) == offset {
					curKey = preR13BlockKeyBase + uint64(i)
					break
				}
			}
			agg.step(nil, false)
		case preR13TypeEndblk:
			curKey = 0
			agg.step(nil, false)
		default:
			if curKey != 0 {
				if ent := decodePreR13Entity(data, head, next, ver, blockHeaders, codepage); ent != nil {
					doc.blocks[curKey] = append(doc.blocks[curKey], ent)
					agg.step(ent, head.flag&preR13FlagHasAttribs != 0)
				}
			}
		}
		pos = int(head.startOff) + int(head.size)
	}
}
