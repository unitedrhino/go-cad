// r11.go 实现 pre-R13 家族（R9/AC1004、R10/AC1006、R11/AC1009）的读取。
// 该家族与 R13+ 完全不同：字节对齐、无对象图/句柄流/位压缩，实体表与
// 块表/层表等为固定偏移表驱动，R11 起各表与实体区以 16 字节 sentinel
// 包夹且记录尾带 CRC。字段布局逐位对齐 LibreDWG decode_r11.c 及
// header.spec / header_variables_r11.spec / dwg.spec / common_entity_data.spec。
// 解码产物复用 R13+ 的实体类型（entLine 等）与 Document 模型，
// 使 RenderPNG / Texts 全链路无差别工作。
package drawing

import (
	"encoding/binary"
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/entity"
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
	PreR13TypePolyline  = 19
	PreR13TypeVertex    = 20
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
	PreR13OptsPolylineHasFlag       = 0x0001
	PreR13OptsPolylineHasStartWidth = 0x0002
	PreR13OptsPolylineHasEndWidth   = 0x0004
	PreR13OptsPolylineHasExtrusion  = 0x0008
	PreR13OptsPolylineHasMVerts     = 0x0010
	PreR13OptsPolylineHasNVerts     = 0x0020
	PreR13OptsPolylineHasMDensity   = 0x0040
	PreR13OptsPolylineHasNDensity   = 0x0080
	PreR13OptsPolylineHasCurvetype  = 0x0100
	preR13OptsPolylineInExtra       = 0x8000
)

// pre-R13 VERTEX 专有 opts 位（LibreDWG OPTS_R11_VERTEX_*）。
const (
	PreR13OptsVertexHasStartWidth = 0x0001
	PreR13OptsVertexHasEndWidth   = 0x0002
	PreR13OptsVertexHasBulge      = 0x0004
	PreR13OptsVertexHasFlag       = 0x0008
	PreR13OptsVertexHasTangentDir = 0x0010
	PreR13OptsVertexHasIndex1     = 0x0020
	PreR13OptsVertexHasIndex2     = 0x0040
	PreR13OptsVertexHasIndex3     = 0x0080
	PreR13OptsVertexHasIndex4     = 0x0100
	PreR13OptsVertexHasNotXY      = 0x4000
)

// pre-R13 POLYLINE pline_flag 位（LibreDWG FLAG_POLYLINE_*）。
const (
	PreR13FlagPolyline3D        = 0x08
	PreR13FlagPolylineMesh      = 0x10
	PreR13FlagPolylinePfaceMesh = 0x40
)

// pre-R13 VERTEX vertex_flag 位（LibreDWG FLAG_VERTEX_*）。
const (
	PreR13FlagVertex3D        = 0x20
	PreR13FlagVertexMesh      = 0x40
	PreR13FlagVertexPfaceMesh = 0x80
)

// preR13BlockKeyBase pre-R13 块定义在 Document.blocks 中的句柄基数。
// 块引用以 BLOCK_HEADER 表索引表达，与 R13+ 句柄空间无冲突。
const preR13BlockKeyBase = 0x100000

// preR13Table 表头（10 字节）：size RS + number RS + flags RS + address RL。
type preR13Table struct {
	Size    uint16
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
	Codepage uint16
}

// preR13Layer LAYER 表条目解析结果。
type preR13Layer struct {
	Name  string
	Color int16 // ACI 颜色索引；负值表示图层关闭
}

// preR13BlockHeader BLOCK_HEADER 表条目解析结果。
type preR13BlockHeader struct {
	Name        string
	blockOffset uint32 // 块实体区内偏移（0x40000000 标记已屏蔽；0xFFFFFFFF 为模型空间占位）
}

// preR13Reader 字节对齐小端读取器（pre-R13 全部为字节流，无位压缩）。
type PreR13Reader struct {
	Data []byte
	pos  int
}

func (r *PreR13Reader) rc() uint8 {
	v := r.Data[r.pos]
	r.pos++
	return v
}

func (r *PreR13Reader) rs() uint16 {
	v := binary.LittleEndian.Uint16(r.Data[r.pos:])
	r.pos += 2
	return v
}

func (r *PreR13Reader) rsd() int16 { return int16(r.rs()) }

func (r *PreR13Reader) rl() uint32 {
	v := binary.LittleEndian.Uint32(r.Data[r.pos:])
	r.pos += 4
	return v
}

func (r *PreR13Reader) rd() float64 {
	v := math.Float64frombits(binary.LittleEndian.Uint64(r.Data[r.pos:]))
	r.pos += 8
	return v
}

// rllBE 大端 8 字节整数（pre-R13 的 HANDSEED 与 HAS_HANDLING 句柄值）。
func (r *PreR13Reader) rllBE() uint64 {
	var v uint64
	for i := 0; i < 8; i++ {
		v = v<<8 | uint64(r.rc())
	}
	return v
}

// skip 推进 n 字节（版本分支中与本实现无关的头变量占位）。
func (r *PreR13Reader) skip(n int) { r.pos += n }

// bytes 取 n 字节切片并推进。
func (r *PreR13Reader) bytes(n int) []byte {
	v := r.Data[r.pos : r.pos+n]
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
		Ver:         version,
		Blocks:      make(map[uint64][]any),
		Attribs:     make(map[uint64]*entity.EntAttrib),
		LayerColors: make(map[uint64]LayerColor),
	}
	r := &PreR13Reader{Data: data}
	hdr, err := parsePreR13Header(r, version)
	if err != nil {
		return nil, err
	}

	// LAYER 表条目：flag RC + name[32] + [R11 used RS] + color RS + ltype RS
	// （size==38 时末尾多 1 字节 flag0；颜色负值即图层关闭，取绝对值）
	if tbl, ok := hdr.tables["LAYER"]; ok && tbl.number > 0 && tbl.address > 0 {
		lr := &PreR13Reader{Data: data, pos: int(tbl.address)}
		for i := 0; i < int(tbl.number) && lr.pos+38 <= len(data); i++ {
			layer := parsePreR13LayerEntry(lr, version, tbl.Size)
			color := int(layer.Color)
			if color < 0 {
				color = -color
			}
			doc.LayerColors[uint64(i)] = LayerColor{Index: uint16(color)}
		}
	}
	// BLOCK_HEADER 表条目：flag RC + name[32] + [R11 used RS] +
	// block_offset_r11 RL + [条件 unknown RC] + [R11 block_entity RS + flag2 RS]。
	// 条目顺序即 INSERT 流内引用的块索引；块内容归属也依赖该表。
	blockHeaders := make([]preR13BlockHeader, 0, 8)
	if tbl, ok := hdr.tables["BLOCK"]; ok && tbl.number > 0 && tbl.address > 0 {
		br := &PreR13Reader{Data: data, pos: int(tbl.address)}
		for i := 0; i < int(tbl.number) && br.pos+38 <= len(data); i++ {
			blockHeaders = append(blockHeaders, parsePreR13BlockHeaderEntry(br, version, tbl.Size))
		}
	}

	// 主实体区：全部为模型空间实体（gold entmode=2），HAS_PSPACE 实体
	// （图纸空间布局，如 ACEB10）解码后存档 pspaceSpace。
	// R11 在 entities_start 前有 16 字节 ENTITIES_BEGIN sentinel，从 start 直接解。
	agg := &preR13EntityAgg{}
	preR13CP := hdr.Codepage
	if preR13CP == 0 {
		preR13CP = 30 // 缺省码页（LibreDWG header.codepage 初始值）
	}
	doc.Codepage = preR13CP
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
	preR13Archive(doc, doc.ModelSpace)
	preR13Archive(doc, doc.PspaceSpace)
	for _, list := range doc.Blocks {
		preR13Archive(doc, list)
	}
	return doc, nil
}

// preR13Archive pre-R13 路径的实体归档（pre-R13 无对象图、不走 classify）：
// 句柄 → entityByHandle（EntityByHandle API），ATTRIB → attribs
// （INSERT 属性展开与 Texts 渲染路径依赖）。
func preR13Archive(doc *Document, list []any) {
	for _, ent := range list {
		ec, ok := ent.(entity.EntityCommon)
		if !ok {
			continue
		}
		b := ec.Common()
		if b.Handle != 0 {
			if doc.ByHandle == nil {
				doc.ByHandle = make(map[uint64]any)
			}
			doc.ByHandle[b.Handle] = ent
		}
		if a, ok := ent.(*entity.EntAttrib); ok && a.Handle != 0 {
			doc.Attribs[a.Handle] = a
		}
	}
}

// parsePreR13Header 解析文件头与全部表头：
// 0x0B 起头字段、0x2C 起基本五表、头变量按 numheader_vars 步进并在
// 流内收集附加表头（UCS/VPORT/APPID/DIMSTYLE/VX）。
// R11 头变量结束后有 2 字节 CRC，此处跳过不校验（错误 CRC 不阻断解析）。
func parsePreR13Header(r *PreR13Reader, ver container.DwgVersion) (*preR13Header, error) {
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
	if int(hdr.entitiesEnd) > len(r.Data) {
		hdr.entitiesEnd = uint32(len(r.Data))
	}
	return hdr, nil
}

// parsePreR13TableHdr 读 10 字节表头。
func parsePreR13TableHdr(r *PreR13Reader) preR13Table {
	var t preR13Table
	t.Size = r.rs()
	t.number = r.rs()
	t.flags = r.rs()
	t.address = r.rl()
	return t
}

// parsePreR13HeaderVars 头变量区步进（header_variables_r11.spec 逐字段）。
// 数值本身渲染不需要，但必须精确推进以到达附加表头与实体区；
// numheader_vars 决定各版本读到哪一档（R9=129、R10=158/160、R11=204/205）。
func parsePreR13HeaderVars(r *PreR13Reader, hdr *preR13Header, ver container.DwgVersion) {
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
	hdr.Codepage = r.rs() // codepage（RS，spec FIELD_RS codepage；文本解码用）
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
func parsePreR13LayerEntry(r *PreR13Reader, ver container.DwgVersion, size uint16) preR13Layer {
	start := r.pos
	var l preR13Layer
	_ = r.rc() // flag
	l.Name = preR13FixName(r.bytes(32))
	if ver == container.VerR11 {
		_ = r.rsd() // used
	}
	l.Color = r.rsd()
	_ = r.rs() // ltype
	if size == 38 {
		_ = r.rc() // flag0
	}
	r.pos = start + int(size)
	return l
}

// parsePreR13BlockHeaderEntry 单条 BLOCK_HEADER 表记录：名字与块偏移
// （块实体归属与 INSERT 引用的锚点）。R11 记录尾含 CRC，按 size 对齐。
func parsePreR13BlockHeaderEntry(r *PreR13Reader, ver container.DwgVersion, size uint16) preR13BlockHeader {
	start := r.pos
	var b preR13BlockHeader
	_ = r.rc() // flag
	b.Name = preR13FixName(r.bytes(32))
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
type PreR13EntHead struct {
	StartOff  int   // 实体记录起始偏移（type 字节处）
	RawType   uint8 // 原始类型码（≥0x80 表示已删除并入块）
	Typ       uint8 // 有效类型（&0x7F）
	Flag      uint8
	Size      uint16 // 记录总长（R11 含记录尾 CRC），下一条实体 = startOff + size
	layerIdx  uint16
	Opts      uint16
	isPspace  bool // HAS_PSPACE：图纸空间实体（不进模型空间渲染）
	colorIdx  int16
	elevation float64
	thickness float64
	Handle    uint64 // HAS_HANDLING 时的显式句柄
}

// 实体公共头 extra_r11 位（HAS_PSPACE 置位时读入的附加标志字节）。
const (
	preR13ExtraHasEed      = 0x02
	preR13ExtraHasViewport = 0x04
)

// parsePreR13CommonHead 读实体公共头（含 flag/extra 展开字段）。
// 字段顺序：type/flag/size/layer/opts → [PSPACE extra] → [EED 链] →
// color → ltype → elevation → thickness → [handling 句柄] → [viewport 句柄]。
func parsePreR13CommonHead(data []byte, pos int, ver container.DwgVersion) (PreR13EntHead, int) {
	var h PreR13EntHead
	if pos+8 > len(data) {
		return h, 0
	}
	r := &PreR13Reader{Data: data, pos: pos}
	h.StartOff = pos
	h.RawType = r.rc()
	h.Typ = h.RawType & 0x7f
	h.Flag = r.rc()
	h.Size = r.rs()
	if h.Typ != preR13TypeJump {
		h.layerIdx = r.rs()
		h.Opts = r.rs()
	}
	// HAS_PSPACE：图纸空间标记 + 1 字节附加标志（LibreDWG 置 entmode=1）
	var extra uint8
	if h.Flag&preR13FlagHasPspace != 0 {
		h.isPspace = true
		extra = r.rc()
	}
	if extra&preR13ExtraHasEed != 0 {
		// EED 链（pre-R13 单条）：RS 长度 + 2 字节应用索引 + 数据体
		if n := int(r.rs()); n >= 2 && r.pos+n <= len(r.Data) {
			r.skip(n)
		} else {
			r.skip(n)
		}
	}
	if h.Flag&preR13FlagHasColor != 0 {
		// color_r11 为有符号字节（RCd）：负值在 DXF 62 中表示图层关闭取反
		h.colorIdx = int16(int8(r.rc()))
	}
	if h.Flag&preR13FlagHasLtype != 0 {
		if ver == container.VerR11 {
			_ = r.rs() // ltype（R11 为 2 字节表索引）
		} else {
			_ = r.rc() // R10 及以前为 1 字节
		}
	}
	// HAS_ELEVATION：R9 全类型读 elevation；R10+ 对 LINE/POINT/3DFACE/
	// 3DLINE 不读（这些类型以该位决定自身几何是否带 z）
	excluded := h.Typ == preR13TypeLine || h.Typ == preR13TypePoint ||
		h.Typ == preR13Type3DFace || h.Typ == preR13Type3DLine
	if h.Flag&preR13FlagHasElevation != 0 && (ver < container.VerR10 || !excluded) {
		h.elevation = r.rd()
	}
	if h.Flag&preR13FlagHasThickness != 0 {
		h.thickness = r.rd()
	}
	if h.Flag&preR13FlagHasHandling != 0 {
		// 可变长句柄：RC 长度 + 大端值（bit_read_H preR13 分支）
		n := int(r.rc())
		for i := 0; i < n && i < 8; i++ {
			h.Handle = h.Handle<<8 | uint64(r.rc())
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
	poly       any               // 当前 POLYLINE（2d/3d/mesh/pface）
	insert     *entity.EntInsert // 当前待挂属性的 INSERT
	insertOpen bool              // INSERT 属性收集窗口开启（HAS_ATTRIBS 置位）
}

// step 序列推进一个解码实体，维护归属窗口。
func (a *preR13EntityAgg) step(ent any, hasAttribs bool) {
	switch v := ent.(type) {
	case *entity.EntPolyline2d:
		a.poly, a.insert, a.insertOpen = v, nil, false
	case *entity.EntPolyline3d:
		a.poly, a.insert, a.insertOpen = v, nil, false
	case *entity.EntPolylineMesh:
		a.poly, a.insert, a.insertOpen = v, nil, false
	case *entity.EntPolylinePface:
		a.poly, a.insert, a.insertOpen = v, nil, false
	case *entity.EntVertex2d:
		preR13AppendOwned(a.poly, v.Handle)
	case *entity.EntVertex3d:
		preR13AppendOwned(a.poly, v.Handle)
	case *entity.EntVertexPface:
		preR13AppendOwned(a.poly, v.Handle)
	case *entity.EntInsert:
		a.poly = nil
		a.insert, a.insertOpen = v, hasAttribs
	case *entity.EntAttrib:
		// 仅 ATTRIB（非 ATTDEF 定义）挂接 INSERT；非窗口内ATTRIB 照常独立
		if a.insertOpen && a.insert != nil && v.TypeName == "ATTRIB" {
			a.insert.Attribs = append(a.insert.Attribs, v.Handle)
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
	case *entity.EntPolyline2d:
		p.OwnedHandles = append(p.OwnedHandles, handle)
	case *entity.EntPolyline3d:
		p.OwnedHandles = append(p.OwnedHandles, handle)
	case *entity.EntPolylineMesh:
		p.OwnedHandles = append(p.OwnedHandles, handle)
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
		if head.Size == 0 || next <= pos {
			break // 无法推进（截断记录），终止避免死循环
		}
		ent := decodePreR13Entity(data, head, next, ver, blockHeaders, codepage)
		// SEQEND/JUMP 等未建模类型返回 nil，同样要推进聚合器关闭归属窗口
		agg.step(ent, head.Flag&preR13FlagHasAttribs != 0)
		if ent != nil {
			if head.isPspace {
				if ec, ok := ent.(entity.EntityCommon); ok {
					ec.Common().Mode = 1
				}
				doc.PspaceSpace = append(doc.PspaceSpace, ent)
			} else {
				doc.ModelSpace = append(doc.ModelSpace, ent)
			}
		}
		pos = int(head.StartOff) + int(head.Size)
	}
}

// decodePreR13Entity 按类型解码实体为 R13+ 的实体类型
// （LINE/POINT/CIRCLE/ARC/TEXT/SOLID/TRACE/INSERT；其余类型按 size 跳过）。
// headEnd 为专有字段区起点（公共头结束处）；记录尾 CRC 属于 size，无需显式跳过。
func decodePreR13Entity(data []byte, h PreR13EntHead, headEnd int, ver container.DwgVersion, blockHeaders []preR13BlockHeader, codepage uint16) any {
	r := &PreR13Reader{Data: data, pos: headEnd}
	switch h.Typ {
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
		return DecodePreR13Dimension(r, h, ver, codepage)
	case PreR13TypePolyline:
		return DecodePreR13Polyline(data, h, ver)
	case PreR13TypeVertex:
		return DecodePreR13Vertex(data, h, ver)
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
func preR13DimPt(r *PreR13Reader, h PreR13EntHead, ver container.DwgVersion) entity.Point3 {
	x, y := r.rd(), r.rd()
	if ver >= container.VerR10 {
		return entity.Point3{X: x, Y: y, Z: r.rd()}
	}
	return entity.Point3{X: x, Y: y, Z: preR13Z(h)}
}

// decodePreR13Dimension DIMENSION（decode.c decode_preR13_DIMENSION）：
// 公共字段为匿名块句柄 RS + def_pt + text_midpt 2RD + [clone_ins_pt] +
// [flag RC] + [user_text TV]，再按 flag 低 4 位分派七种类型专属布局；
// 类型决定 typeName（gold entity 键），几何字段映射 R13+ 的
// entDimension 模型（point13/14/15/10、p16、textRotation 等），
// dimstyle 为 2 字节 DIMSTYLE 表索引。
func DecodePreR13Dimension(r *PreR13Reader, h PreR13EntHead, ver container.DwgVersion, codepage uint16) *entity.EntDimension {
	e := &entity.EntDimension{}
	e.TypeName = "DIMENSION_LINEAR"
	e.TypeCode = preR13TypeDimension
	e.Mode = 2
	preR13HandleBase(&e.BaseEntity, h)
	preR13Color(&e.BaseEntity, h)
	opts := h.Opts
	e.AnonymousBlock = uint64(r.rs()) // block HANDLE(2)
	e.Point10 = preR13DimPt(r, h, ver)
	tx, ty := r.rd(), r.rd()
	e.TextMidpoint = entity.Point3{X: tx, Y: ty, Z: preR13Z(h)}
	dimtype := uint8(0)
	if opts&preR13OptsDimHasDXF12 != 0 {
		cx, cy := r.rd(), r.rd()
		e.InsertPoint = entity.Point3{X: cx, Y: cy, Z: preR13Z(h)}
		e.HasInsertPoint = true
	}
	if opts&preR13OptsDimHasFlag != 0 {
		dimtype = r.rc()
	}
	e.DimFlags = dimtype
	e.DimFlag = dimtype // pre-R13 flag 低 4 位即类型语义，无需 R13+ 合成
	if opts&preR13OptsDimHasText != 0 {
		e.UserText = preR13TV(r, codepage)
	}
	switch dimtype & 15 {
	case preR13DimTypeLinear:
		e.TypeName = "DIMENSION_LINEAR"
		if opts&preR13OptsDimHasDXF13 != 0 {
			e.Point13 = preR13DimPt(r, h, ver)
		}
		if opts&preR13OptsDimHasDXF14 != 0 {
			e.Point14 = preR13DimPt(r, h, ver)
		}
		if opts&preR13OptsDimHasRot != 0 {
			e.DimRotation = r.rd()
		}
		if opts&preR13OptsDimUnknown512 != 0 {
			e.ExtLineRotation = r.rd() // oblique_angle/ext_line_rotation
		}
		if opts&preR13OptsDimHasDXF53 != 0 {
			e.TextRotation = r.rd()
		}
		if opts&preR13OptsDimHasExtrusion != 0 {
			e.Extrusion = entity.Point3{X: r.rd(), Y: r.rd(), Z: r.rd()}
		}
		if opts&preR13OptsDimHasDimstyle != 0 {
			e.DimstyleHandle = uint64(r.rs())
		}
	case preR13DimTypeAligned:
		e.TypeName = "DIMENSION_ALIGNED"
		if opts&preR13OptsDimHasDXF13 != 0 {
			e.Point13 = preR13DimPt(r, h, ver)
		}
		if opts&preR13OptsDimHasDXF14 != 0 {
			e.Point14 = preR13DimPt(r, h, ver)
		}
		if opts&preR13OptsDimHasRot != 0 {
			e.ExtLineRotation = r.rd() // oblique_angle（对齐型无独立转角）
		}
		if opts&preR13OptsDimHasDXF53 != 0 {
			e.TextRotation = r.rd()
		}
		if opts&preR13OptsDimHasDimstyle != 0 {
			e.DimstyleHandle = uint64(r.rs())
		}
	case preR13DimTypeAng2Ln:
		e.TypeName = "DIMENSION_ANG2LN"
		if opts&preR13OptsDimHasDXF13 != 0 {
			e.Point13 = preR13DimPt(r, h, ver) // xline1start
		}
		if opts&preR13OptsDimHasDXF14 != 0 {
			e.Point14 = preR13DimPt(r, h, ver) // xline1end
		}
		if opts&preR13OptsDimHasDXF15 != 0 {
			e.Point15 = preR13DimPt(r, h, ver) // xline2start
			e.HasPoint15 = true
		}
		if opts&preR13OptsDimHasAngles != 0 {
			e.Point16x, e.P16y = r.rd(), r.rd() // xline2end 2RD
			e.HasPoint16 = true
		}
		if opts&preR13OptsDimHasDXF53 != 0 {
			e.TextRotation = r.rd()
		}
		if opts&preR13OptsDimHasDimstyle != 0 {
			e.DimstyleHandle = uint64(r.rs())
		}
	case preR13DimTypeDiameter:
		e.TypeName = "DIMENSION_DIAMETER"
		if opts&preR13OptsDimHasDXF15 != 0 {
			// first_arc_pt：R10 且无 HAS_ELEVATION 时 3RD，否则 2RD
			x, y := r.rd(), r.rd()
			if ver >= container.VerR10 && h.Flag&preR13FlagHasElevation == 0 {
				e.Point15 = entity.Point3{X: x, Y: y, Z: r.rd()}
			} else {
				e.Point15 = entity.Point3{X: x, Y: y, Z: preR13Z(h)}
			}
			e.HasPoint15 = true
		}
		if opts&preR13OptsDimHasDXF40 != 0 {
			_ = r.rd() // leader_len
		}
		if opts&preR13OptsDimHasDXF53 != 0 {
			e.TextRotation = r.rd()
		}
		if opts&preR13OptsDimHasExtrusion != 0 {
			e.Extrusion = entity.Point3{X: r.rd(), Y: r.rd(), Z: r.rd()}
		}
		if opts&preR13OptsDimHasDimstyle != 0 {
			e.DimstyleHandle = uint64(r.rs())
		}
	case preR13DimTypeRadius:
		e.TypeName = "DIMENSION_RADIUS"
		if opts&preR13OptsDimHasDXF15 != 0 {
			e.Point15 = preR13DimPt(r, h, ver) // first_arc_pt
			e.HasPoint15 = true
		}
		if opts&preR13OptsDimHasDXF40 != 0 {
			_ = r.rd() // leader_len
		}
		if opts&preR13OptsDimHasDXF53 != 0 {
			e.TextRotation = r.rd()
		}
		if opts&preR13OptsDimHasExtrusion != 0 {
			e.Extrusion = entity.Point3{X: r.rd(), Y: r.rd(), Z: r.rd()}
		}
		if opts&preR13OptsDimHasDimstyle != 0 {
			e.DimstyleHandle = uint64(r.rs())
		}
	case preR13DimTypeAng3Pt:
		e.TypeName = "DIMENSION_ANG3PT"
		if opts&preR13OptsDimHasDXF13 != 0 {
			e.Point13 = preR13DimPt(r, h, ver) // xline1_pt
		}
		if opts&preR13OptsDimHasDXF14 != 0 {
			e.Point14 = preR13DimPt(r, h, ver) // xline2_pt
		}
		if opts&preR13OptsDimHasDXF15 != 0 {
			e.Point15 = preR13DimPt(r, h, ver) // center_pt
			e.HasPoint15 = true
		}
		if opts&preR13OptsDimHasAngles != 0 {
			e.Point16x, e.P16y = r.rd(), r.rd() // xline2end 2RD
			e.HasPoint16 = true
		}
		if opts&preR13OptsDimHasDXF53 != 0 {
			e.TextRotation = r.rd()
		}
		if opts&preR13OptsDimHasDimstyle != 0 {
			e.DimstyleHandle = uint64(r.rs())
		}
	case preR13DimTypeOrdinate:
		e.TypeName = "DIMENSION_ORDINATE"
		if opts&preR13OptsDimHasDXF13 != 0 {
			e.Point13 = preR13DimPt(r, h, ver) // feature_location_pt
		}
		if opts&preR13OptsDimHasDXF14 != 0 {
			e.Point14 = preR13DimPt(r, h, ver) // leader_endpt
		}
		if opts&preR13OptsDimHasDXF53 != 0 {
			e.TextRotation = r.rd()
		}
		if opts&preR13OptsDimHasDimstyle != 0 {
			e.DimstyleHandle = uint64(r.rs())
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
	r := &PreR13Reader{Data: data}
	_ = r.rc() // type
	flag := r.rc()
	r.skip(4) // size RS + layer RS
	opts := r.rs()
	if opts&PreR13OptsPolylineHasFlag == 0 {
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
func DecodePreR13Polyline(data []byte, h PreR13EntHead, ver container.DwgVersion) any {
	flagOff := preR13PlineFlagOffset(data[h.StartOff:], ver)
	plineFlag := uint8(0)
	if flagOff >= 0 {
		plineFlag = data[h.StartOff+flagOff]
	}
	// 真实解码：公共头展开（含 PSPACE/EED/color/ltype/elevation/thickness/
	// handling/viewport 完整顺序）后接专有字段
	_, bodyPos := parsePreR13CommonHead(data, h.StartOff, ver)
	r := &PreR13Reader{Data: data, pos: bodyPos}
	opts := h.Opts
	switch {
	case plineFlag&PreR13FlagPolyline3D != 0:
		e := &entity.EntPolyline3d{}
		e.TypeName = "POLYLINE_3D"
		if opts&PreR13OptsPolylineHasFlag != 0 {
			e.Flags70 = r.rc()
		}
		if opts&PreR13OptsPolylineHasStartWidth != 0 {
			_ = r.rd() // start_width
		}
		if opts&PreR13OptsPolylineHasEndWidth != 0 {
			_ = r.rd() // end_width
		}
		if opts&PreR13OptsPolylineHasExtrusion != 0 {
			r.skip(24) // extrusion 3RD
		}
		if opts&PreR13OptsPolylineHasCurvetype != 0 {
			e.Flags75 = uint8(r.rs()) // curve_type（3D 网格）
		}
		e.TypeCode = PreR13TypePolyline
		e.Mode = 2
		preR13HandleBase(&e.BaseEntity, h)
		preR13Color(&e.BaseEntity, h)
		return e
	case plineFlag&PreR13FlagPolylineMesh != 0:
		e := &entity.EntPolylineMesh{}
		e.TypeName = "POLYLINE_MESH"
		if opts&PreR13OptsPolylineHasFlag != 0 {
			e.Flags = uint16(r.rc())
		}
		if opts&PreR13OptsPolylineHasMVerts != 0 {
			e.MVertexCount = r.rs()
		}
		if opts&PreR13OptsPolylineHasNVerts != 0 {
			e.NVertexCount = r.rs()
		}
		if opts&PreR13OptsPolylineHasMDensity != 0 {
			e.MDensity = r.rs()
		}
		if opts&PreR13OptsPolylineHasNDensity != 0 {
			e.NDensity = r.rs()
		}
		if opts&PreR13OptsPolylineHasCurvetype != 0 {
			e.CurveType = r.rs()
		}
		e.TypeCode = PreR13TypePolyline
		e.Mode = 2
		preR13HandleBase(&e.BaseEntity, h)
		preR13Color(&e.BaseEntity, h)
		return e
	case plineFlag&PreR13FlagPolylinePfaceMesh != 0:
		e := &entity.EntPolylinePface{}
		e.TypeName = "POLYLINE_PFACE"
		if opts&PreR13OptsPolylineHasFlag != 0 {
			_ = r.rc() // flag
		}
		if opts&PreR13OptsPolylineHasMVerts != 0 {
			e.NumVertices = int(r.rs()) // numverts（gold 键 71）
		}
		if opts&PreR13OptsPolylineHasNVerts != 0 {
			e.NumFaces = int(r.rs()) // numfaces（gold 键 72）
		}
		e.TypeCode = PreR13TypePolyline
		e.Mode = 2
		preR13HandleBase(&e.BaseEntity, h)
		preR13Color(&e.BaseEntity, h)
		return e
	default:
		e := &entity.EntPolyline2d{}
		e.TypeName = "POLYLINE_2D"
		if opts&PreR13OptsPolylineHasFlag != 0 {
			e.Flags = uint16(r.rc())
		}
		if opts&PreR13OptsPolylineHasStartWidth != 0 {
			e.WidthStart = r.rd()
		}
		if opts&PreR13OptsPolylineHasEndWidth != 0 {
			e.WidthEnd = r.rd()
		}
		if opts&PreR13OptsPolylineHasExtrusion != 0 {
			e.Extrusion = entity.Point3{X: r.rd(), Y: r.rd(), Z: r.rd()}
		}
		if opts&PreR13OptsPolylineHasMVerts != 0 {
			_ = r.rs() // num_m_verts（顶点以记录顺序聚合表达）
		}
		if opts&PreR13OptsPolylineHasNVerts != 0 {
			_ = r.rs() // num_n_verts
		}
		if opts&PreR13OptsPolylineHasCurvetype != 0 {
			e.CurveType = r.rs()
		}
		if opts&preR13OptsPolylineInExtra != 0 && h.Size > 20 {
			// 附加实体区引用文本：记录尾（R11 扣 2 字节 CRC）前剩余整段
			n := int(h.Size) - (r.pos - h.StartOff)
			if ver == container.VerR11 {
				n -= 2
			}
			if n > 0 {
				r.skip(n)
			}
		}
		e.Elevation = h.elevation
		e.Thickness = h.thickness
		e.TypeCode = PreR13TypePolyline
		e.Mode = 2
		preR13HandleBase(&e.BaseEntity, h)
		preR13Color(&e.BaseEntity, h)
		return e
	}
}

// preR13VertexFlagOffset VERTEX 预扫描：定位 vertex_flag 字节偏移
// （decode.c Detect vertex 分支；字段顺序按该参考实现：color → ltype →
// thickness → elevation → PSPACE extra → EED → handling → viewport →
// [x,y] → [start_width] → [end_width] → [bulge]）。
func preR13VertexFlagOffset(data []byte, ver container.DwgVersion) (int, uint16) {
	r := &PreR13Reader{Data: data}
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
	if opts&PreR13OptsVertexHasNotXY == 0 {
		r.skip(16) // point 2RD
	}
	if opts&PreR13OptsVertexHasStartWidth != 0 {
		r.skip(8)
	}
	if opts&PreR13OptsVertexHasEndWidth != 0 {
		r.skip(8)
	}
	if opts&PreR13OptsVertexHasBulge != 0 {
		r.skip(8)
	}
	return r.pos, opts
}

// decodePreR13Vertex VERTEX（type 20，pre-R13 单类型多变体）：预扫描
// vertex_flag 决定变体（MESH|PFACE_MESH→PFACE、MESH→MESH、
// PFACE_MESH→PFACE_FACE、3D→3D、默认 2D），再按公共头展开后读专有字段。
func DecodePreR13Vertex(data []byte, h PreR13EntHead, ver container.DwgVersion) any {
	flagOff, opts := preR13VertexFlagOffset(data[h.StartOff:], ver)
	vertexFlag := uint8(0)
	if opts&PreR13OptsVertexHasFlag != 0 && flagOff >= 0 && h.StartOff+flagOff < len(data) {
		vertexFlag = data[h.StartOff+flagOff]
	}
	_, bodyPos := parsePreR13CommonHead(data, h.StartOff, ver)
	r := &PreR13Reader{Data: data, pos: bodyPos}
	readPoint := func() entity.Point3 {
		x, y := r.rd(), r.rd()
		return entity.Point3{X: x, Y: y, Z: preR13Z(h)}
	}
	base := func() entity.BaseEntity {
		var b entity.BaseEntity
		b.TypeCode = PreR13TypeVertex
		b.Mode = 2
		preR13HandleBase(&b, h)
		preR13Color(&b, h)
		return b
	}
	switch {
	case vertexFlag&PreR13FlagVertexMesh != 0 && vertexFlag&PreR13FlagVertexPfaceMesh != 0:
		e := &entity.EntVertexPface{}
		e.BaseEntity = base()
		e.TypeName = "VERTEX_PFACE"
		if opts&PreR13OptsVertexHasNotXY == 0 {
			e.Position = readPoint()
		}
		if opts&PreR13OptsVertexHasFlag != 0 {
			e.Flag = r.rc()
		}
		return e
	case vertexFlag&PreR13FlagVertexMesh != 0:
		e := &entity.EntVertexPface{}
		e.BaseEntity = base()
		e.TypeName = "VERTEX_MESH"
		e.Position = readPoint()
		e.Flag = r.rc() // VERTEX_MESH 的 flag 无条件存在
		return e
	case vertexFlag&PreR13FlagVertexPfaceMesh != 0:
		e := &entity.EntVertexPfaceFace{}
		e.BaseEntity = base()
		e.TypeName = "VERTEX_PFACE_FACE"
		if opts&PreR13OptsVertexHasFlag != 0 {
			e.Flag = r.rc()
		}
		if opts&PreR13OptsVertexHasIndex1 != 0 {
			e.Vertind[0] = int32(int16(r.rs()))
		}
		if opts&PreR13OptsVertexHasIndex2 != 0 {
			e.Vertind[1] = int32(int16(r.rs()))
		}
		if opts&PreR13OptsVertexHasIndex3 != 0 {
			e.Vertind[2] = int32(int16(r.rs()))
		}
		if opts&PreR13OptsVertexHasIndex4 != 0 {
			e.Vertind[3] = int32(int16(r.rs()))
		}
		return e
	case vertexFlag&PreR13FlagVertex3D != 0:
		e := &entity.EntVertex3d{}
		e.BaseEntity = base()
		e.TypeName = "VERTEX_3D"
		e.Position = readPoint()
		if opts&PreR13OptsVertexHasFlag != 0 {
			e.Flags = r.rc()
		}
		return e
	default:
		e := &entity.EntVertex2d{}
		e.BaseEntity = base()
		e.TypeName = "VERTEX_2D"
		e.Position = readPoint()
		if opts&PreR13OptsVertexHasStartWidth != 0 {
			e.StartWidth = r.rd()
		}
		if opts&PreR13OptsVertexHasEndWidth != 0 {
			e.EndWidth = r.rd()
		}
		if opts&PreR13OptsVertexHasBulge != 0 {
			e.Bulge = r.rd()
		}
		if opts&PreR13OptsVertexHasFlag != 0 {
			e.Flags = uint16(r.rc())
		}
		if opts&PreR13OptsVertexHasTangentDir != 0 {
			e.TangentDir = r.rd()
		}
		return e
	}
}

// decodePreR13Viewport VIEWPORT 视口（dwg.spec PRE(R_13b1) 分支）：
// center 3RD + width RD + height RD + id RS。仅解码不渲染
// （与 R13+ VIEWPORT 渲染策略一致），id 无模型字段挂 extra。
func decodePreR13Viewport(r *PreR13Reader, h PreR13EntHead) *entity.EntViewport {
	e := &entity.EntViewport{}
	e.TypeName = "VIEWPORT"
	e.TypeCode = preR13TypeViewport
	e.Mode = 2
	preR13HandleBase(&e.BaseEntity, h)
	preR13Color(&e.BaseEntity, h)
	cx, cy, cz := r.rd(), r.rd(), r.rd()
	e.Center = entity.Point3{X: cx, Y: cy, Z: cz}
	e.Width = r.rd()
	e.Height = r.rd()
	id := r.rs()
	if e.Extra == nil {
		e.Extra = map[string]any{}
	}
	e.Extra["id"] = id
	return e
}

// decodePreR13Shape SHAPE 形参照（dwg.spec VERSIONS(R_2_0,R_11) 分支）：
// 插入点 2RD + 缩放 RD + style_id RC，opts 位展开旋转/样式句柄/
// 宽度因子/倾斜角；thickness 取公共头（HAS_THICKNESS），插入点 z 取
// 公共头 elevation（spec DECODER 显式回填语义，同 TEXT）。
func decodePreR13Shape(r *PreR13Reader, h PreR13EntHead) *entity.EntShape {
	e := &entity.EntShape{}
	e.TypeName = "SHAPE"
	e.TypeCode = preR13TypeShape
	e.Mode = 2
	preR13HandleBase(&e.BaseEntity, h)
	preR13Color(&e.BaseEntity, h)
	ix, iy := r.rd(), r.rd()
	e.Scale = r.rd()
	e.ShapeNo = uint16(r.rc()) // style_id：SHAPEFILE 内形编号（1 字节表索引）
	if h.Opts&0x01 != 0 {
		e.Rotation = r.rd() // rotation（弧度，gold 0.5236 = 30°）
	}
	if h.Opts&0x02 != 0 {
		_ = r.rc() // style（HAS_LOAD_NUM，1 字节 STYLE 表索引）
	}
	if h.Opts&0x04 != 0 {
		e.WidthFactor = r.rd()
	}
	if h.Opts&0x08 != 0 {
		e.Oblique = r.rd()
	}
	e.Insertion = entity.Point3{X: ix, Y: iy, Z: preR13Z(h)}
	e.Thickness = h.thickness
	return e
}

// preR13Color 应用实体颜色与图层索引到公共字段（ACI 索引；0/256 语义
// 由 colorResolved 统一处理，这里原样带过）。
func preR13Color(base *entity.BaseEntity, h PreR13EntHead) {
	base.Layer = uint64(h.layerIdx)
	if h.Flag&preR13FlagHasColor != 0 && h.colorIdx > 0 {
		base.Color.Index = uint16(h.colorIdx)
		base.Color.HasIndex = true
	}
}

// preR13Z elevation_r11 的 z 补齐：pre-R13 的 2RD 坐标 z 来自公共头 elevation。
func preR13Z(h PreR13EntHead) float64 { return h.elevation }

// decodePreR13Line LINE：R9 恒 2RD×2；R10+ 以 HAS_ELEVATION 位区分
// 2RD×2（z=elevation）与 3RD×2。
func decodePreR13Line(r *PreR13Reader, h PreR13EntHead, ver container.DwgVersion) *entity.EntLine {
	e := &entity.EntLine{}
	e.TypeName = "LINE"
	e.TypeCode = preR13TypeLine
	e.Mode = 2
	preR13HandleBase(&e.BaseEntity, h)
	preR13Color(&e.BaseEntity, h)
	is3d := ver >= container.VerR10 && h.Flag&preR13FlagHasElevation == 0
	if is3d {
		e.Start = entity.Point3{X: r.rd(), Y: r.rd(), Z: r.rd()}
		e.End = entity.Point3{X: r.rd(), Y: r.rd(), Z: r.rd()}
	} else {
		e.Start = entity.Point3{X: r.rd(), Y: r.rd(), Z: preR13Z(h)}
		e.End = entity.Point3{X: r.rd(), Y: r.rd(), Z: preR13Z(h)}
	}
	return e
}

// decodePreR133DLine 3DLINE（dwg.spec _3DLINE）：R9（R_2_4~R_9c1）以 opts
// 位逐点选择 3RD（bit0 起点、bit1 终点）否则 2RD（z=elevation）；R10+ 以
// HAS_ELEVATION 位区分（置位 2RD×2、否则 3RD×2），opts bit0 为挤出方向。
// 复用 entLine 模型（R13+ 渲染直连）。
func decodePreR133DLine(r *PreR13Reader, h PreR13EntHead, ver container.DwgVersion) *entity.EntLine {
	e := &entity.EntLine{}
	e.TypeName = "3DLINE"
	e.TypeCode = preR13Type3DLine
	e.Mode = 2
	preR13HandleBase(&e.BaseEntity, h)
	preR13Color(&e.BaseEntity, h)
	if ver < container.VerR10 {
		if h.Opts&0x01 != 0 {
			e.Start = entity.Point3{X: r.rd(), Y: r.rd(), Z: r.rd()}
		} else {
			e.Start = entity.Point3{X: r.rd(), Y: r.rd(), Z: preR13Z(h)}
		}
		if h.Opts&0x02 != 0 {
			e.End = entity.Point3{X: r.rd(), Y: r.rd(), Z: r.rd()}
		} else {
			e.End = entity.Point3{X: r.rd(), Y: r.rd(), Z: preR13Z(h)}
		}
		return e
	}
	is2d := h.Flag&preR13FlagHasElevation != 0
	if is2d {
		e.Start = entity.Point3{X: r.rd(), Y: r.rd(), Z: preR13Z(h)}
		e.End = entity.Point3{X: r.rd(), Y: r.rd(), Z: preR13Z(h)}
	} else {
		e.Start = entity.Point3{X: r.rd(), Y: r.rd(), Z: r.rd()}
		e.End = entity.Point3{X: r.rd(), Y: r.rd(), Z: r.rd()}
	}
	if h.Opts&0x01 != 0 {
		r.skip(24) // extrusion 3RD
	}
	return e
}

// decodePreR133DFace 3DFACE（dwg.spec _3DFACE）：R9 以 opts 四个位逐角点
// 选择 3RD（bit0~bit3 对应角 1~4）否则 2RD（z=elevation）；R10+ 以
// HAS_ELEVATION 位区分 2RD×4/3RD×4，opts bit0 为不可见边标志 RS。
// 复用 entFace3d 模型（R13+ 渲染直连）。
func decodePreR133DFace(r *PreR13Reader, h PreR13EntHead, ver container.DwgVersion) *entity.EntFace3d {
	e := &entity.EntFace3d{}
	e.TypeName = "3DFACE"
	e.TypeCode = preR13Type3DFace
	e.Mode = 2
	preR13HandleBase(&e.BaseEntity, h)
	preR13Color(&e.BaseEntity, h)
	// hasZ：该角点是否带显式 z（R9 按 opts 位、R10+ 按 HAS_ELEVATION 全局位）
	hasZ := func(cornerBit uint16) bool {
		if ver < container.VerR10 {
			return h.Opts&cornerBit != 0
		}
		return h.Flag&preR13FlagHasElevation == 0
	}
	readCorner := func(cornerBit uint16) entity.Point3 {
		x, y := r.rd(), r.rd()
		if hasZ(cornerBit) {
			return entity.Point3{X: x, Y: y, Z: r.rd()}
		}
		return entity.Point3{X: x, Y: y, Z: preR13Z(h)}
	}
	e.P1 = readCorner(0x01)
	e.P2 = readCorner(0x02)
	e.P3 = readCorner(0x04)
	e.P4 = readCorner(0x08)
	// 不可见边标志仅 R10+ 存在（R9 的 opts 位被角点 z 占用）
	if ver >= container.VerR10 && h.Opts&0x01 != 0 {
		e.InvisibleEdgeFlags = r.rs()
	}
	return e
}

// decodePreR13Point POINT：x/y 恒 RD；z 仅 R10+ 且无 HAS_ELEVATION 时存在。
func decodePreR13Point(r *PreR13Reader, h PreR13EntHead, ver container.DwgVersion) *entity.EntPoint {
	e := &entity.EntPoint{}
	e.TypeName = "POINT"
	e.TypeCode = preR13TypePoint
	e.Mode = 2
	preR13HandleBase(&e.BaseEntity, h)
	preR13Color(&e.BaseEntity, h)
	x, y := r.rd(), r.rd()
	z := 0.0
	if ver >= container.VerR10 && h.Flag&preR13FlagHasElevation == 0 {
		z = r.rd() // z（DXF 30，仅 R10+ 且无 HAS_ELEVATION）
	}
	e.Location = entity.Point3{X: x, Y: y, Z: z}
	if h.Opts&0x01 != 0 {
		r.skip(24) // extrusion 3RD
	}
	if h.Opts&0x02 != 0 {
		e.Rotation = r.rd() // x_ang（弧度，pre-R13 角度字段与 R13+ 同为弧度口径）
	}
	return e
}

// decodePreR13Circle CIRCLE：center 2RD + radius RD。
func decodePreR13Circle(r *PreR13Reader, h PreR13EntHead, ver container.DwgVersion) *entity.EntCircle {
	e := &entity.EntCircle{}
	e.TypeName = "CIRCLE"
	e.TypeCode = preR13TypeCircle
	e.Mode = 2
	preR13HandleBase(&e.BaseEntity, h)
	preR13Color(&e.BaseEntity, h)
	cx, cy := r.rd(), r.rd()
	e.Radius = r.rd()
	e.Center = entity.Point3{X: cx, Y: cy, Z: preR13Z(h)}
	if h.Opts&0x01 != 0 {
		r.skip(24) // extrusion 3RD
	}
	if h.Opts&0x02 != 0 {
		e.Center.Z = r.rd() // center.z 显式字段（DXF 38）
	}
	return e
}

// decodePreR13Arc ARC：center 2RD + radius + 起终角（度 → 弧度对齐 R13+）。
func decodePreR13Arc(r *PreR13Reader, h PreR13EntHead, ver container.DwgVersion) *entity.EntArc {
	e := &entity.EntArc{}
	e.TypeName = "ARC"
	e.TypeCode = preR13TypeArc
	e.Mode = 2
	preR13HandleBase(&e.BaseEntity, h)
	preR13Color(&e.BaseEntity, h)
	cx, cy := r.rd(), r.rd()
	e.Radius = r.rd()
	// pre-R13 角度字段即弧度（gold start=4.712=270°），与 R13+ 口径一致
	e.AngleStart = r.rd()
	e.AngleEnd = r.rd()
	e.Center = entity.Point3{X: cx, Y: cy, Z: preR13Z(h)}
	if h.Opts&0x01 != 0 {
		r.skip(24) // extrusion 3RD
	}
	if h.Opts&0x02 != 0 {
		e.Center.Z = r.rd() // center.z（DXF 30）
	}
	return e
}

// decodePreR13Text TEXT：插入点 + 字高 + 变长文字 + opts 位展开的可选字段
// （rotation/宽度因子/样式/生成标志/水平对齐/对齐点/挤出/垂直对齐）。
// codepage 为头变量流的码页编号（文字按其解码，30 默认 Latin-1 近似）。
func decodePreR13Text(r *PreR13Reader, h PreR13EntHead, codepage uint16) *entity.EntText {
	e := &entity.EntText{}
	e.TypeName = "TEXT"
	e.TypeCode = preR13TypeText
	e.Mode = 2
	preR13HandleBase(&e.BaseEntity, h)
	preR13Color(&e.BaseEntity, h)
	ix, iy := r.rd(), r.rd()
	e.Height = r.rd()
	e.Insertion = entity.Point3{X: ix, Y: iy, Z: preR13Z(h)}
	n := int(r.rs())
	if n > 0 && r.pos+n <= len(r.Data) {
		// pre-R2000 字符串尾可能带 \0 填充，截断
		e.Text = bitstream.DecodeCodepage(preR13TruncNul(r.bytes(n)), codepage)
	} else {
		r.skip(n)
	}
	if h.Opts&0x01 != 0 {
		e.Rotation = r.rd() // rotation（弧度口径）
	}
	if h.Opts&0x02 != 0 {
		_ = r.rd() // width_factor
	}
	if h.Opts&0x04 != 0 {
		_ = r.rd() // oblique_angle
	}
	if h.Opts&0x08 != 0 {
		_ = r.rc() // style（HANDLE code=1，1 字节表索引）
	}
	if h.Opts&0x10 != 0 {
		e.Gen = uint16(r.rc()) // generation
	}
	if h.Opts&0x20 != 0 {
		e.HAlign = uint16(r.rc()) // horiz_alignment
	}
	if h.Opts&0x40 != 0 {
		ax, ay := r.rd(), r.rd()
		e.AlignPt = &entity.Point2{X: ax, Y: ay}
	}
	if h.Opts&0x80 != 0 {
		r.skip(24) // extrusion 3RD
	}
	if h.Opts&0x100 != 0 {
		e.VAlign = uint16(r.rc()) // vert_alignment
	}
	return e
}

// preR13TV pre-R13 变长字符串（RS 长度 + 字节体，尾 \0 截断），按码页解码。
func preR13TV(r *PreR13Reader, codepage uint16) string {
	n := int(r.rs())
	if n <= 0 {
		return ""
	}
	if r.pos+n > len(r.Data) {
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
func decodePreR13Attrib(r *PreR13Reader, h PreR13EntHead, attdef bool, codepage uint16) *entity.EntAttrib {
	e := &entity.EntAttrib{}
	if attdef {
		e.TypeName = "ATTDEF"
	} else {
		e.TypeName = "ATTRIB"
	}
	e.TypeCode = uint16(h.Typ)
	e.Mode = 2
	preR13HandleBase(&e.BaseEntity, h)
	preR13Color(&e.BaseEntity, h)
	ix, iy := r.rd(), r.rd()
	e.Height = r.rd()
	e.Text = preR13TV(r, codepage)
	if attdef {
		e.Prompt = preR13TV(r, codepage) // prompt（仅 ATTDEF）
	}
	e.Tag = preR13TV(r, codepage)
	// flags：1 不可见 2 常量 4 校验 8 预置（无对应模型字段，仅推进流）
	_ = r.rc()
	if h.Opts&0x02 != 0 {
		e.Rotation = r.rd()
	}
	if h.Opts&0x04 != 0 {
		_ = r.rd() // width_factor
	}
	if h.Opts&0x08 != 0 {
		_ = r.rd() // oblique_angle
	}
	if h.Opts&0x10 != 0 {
		_ = r.rc() // style（1 字节 STYLE 表索引）
	}
	if h.Opts&0x20 != 0 {
		e.Gen = uint16(r.rc()) // generation
	}
	if h.Opts&0x40 != 0 {
		e.HAlign = uint16(r.rc()) // horiz_alignment
	}
	if h.Opts&0x80 != 0 {
		_ = r.rd() // alignment_pt x
		_ = r.rd() // alignment_pt y
	}
	if h.Opts&0x100 != 0 {
		r.skip(24) // extrusion 3RD
	}
	if h.Opts&0x200 != 0 {
		e.VAlign = uint16(r.rc()) // vert_alignment
	}
	e.Insertion = entity.Point3{X: ix, Y: iy, Z: preR13Z(h)}
	return e
}

// decodePreR13Solid SOLID/TRACE：四角 2RD（z=elevation，单独 elevation 字段）。
func decodePreR13Solid(r *PreR13Reader, h PreR13EntHead) *entity.EntSolid {
	e := &entity.EntSolid{Trace: h.Typ == preR13TypeTrace}
	if e.Trace {
		e.TypeName = "TRACE"
	} else {
		e.TypeName = "SOLID"
	}
	e.TypeCode = uint16(h.Typ)
	e.Mode = 2
	preR13HandleBase(&e.BaseEntity, h)
	preR13Color(&e.BaseEntity, h)
	z := preR13Z(h)
	e.Elevation = z
	e.P1 = entity.Point2{X: r.rd(), Y: r.rd()}
	e.P2 = entity.Point2{X: r.rd(), Y: r.rd()}
	e.P3 = entity.Point2{X: r.rd(), Y: r.rd()}
	e.P4 = entity.Point2{X: r.rd(), Y: r.rd()}
	if h.Opts&0x01 != 0 {
		r.skip(24) // extrusion 3RD
	}
	if h.Opts&0x02 != 0 {
		e.Elevation = r.rd() // 显式 elevation（DXF 38）
	}
	return e
}

// decodePreR13Insert INSERT：块表索引引用 + 插入点 + 按位展开的
// 缩放/旋转/阵列参数。scale 缺省 1.0（仅 opts 置位时存储）。
func decodePreR13Insert(r *PreR13Reader, h PreR13EntHead, ver container.DwgVersion, blockHeaders []preR13BlockHeader) *entity.EntInsert {
	e := &entity.EntInsert{}
	e.TypeName = "INSERT"
	e.TypeCode = preR13TypeInsert
	e.Mode = 2
	preR13HandleBase(&e.BaseEntity, h)
	preR13Color(&e.BaseEntity, h)
	idx := r.rs() // block_header（BLOCK_HEADER 表索引）
	if int(idx) < len(blockHeaders) {
		e.BlockHeader = preR13BlockKeyBase + uint64(idx)
	}
	ix, iy := r.rd(), r.rd()
	e.Position = entity.Point3{X: ix, Y: iy, Z: preR13Z(h)}
	e.Scale = entity.Point3{X: 1, Y: 1, Z: 1}
	if h.Opts&0x01 != 0 {
		e.Scale.X = r.rd()
	}
	if h.Opts&0x02 != 0 {
		e.Scale.Y = r.rd()
	}
	if h.Opts&0x04 != 0 {
		e.Rotation = r.rd() // 弧度（gold 0.5236 = 30°）
	}
	if h.Opts&0x08 != 0 {
		e.Scale.Z = r.rd()
	}
	if h.Opts&0x10 != 0 {
		_ = r.rs() // num_cols（MINSERT 阵列，渲染按单次插入处理）
	}
	if h.Opts&0x20 != 0 {
		_ = r.rs() // num_rows
	}
	if h.Opts&0x40 != 0 {
		_ = r.rd() // col_spacing
	}
	if h.Opts&0x80 != 0 {
		_ = r.rd() // row_spacing
	}
	if h.Opts&0x100 != 0 {
		r.skip(24) // extrusion 3RD
	}
	return e
}

// preR13HandleBase pre-R13 实体无全局句柄流：HAS_HANDLING 显式句柄优先，
// 否则以记录偏移作稳定伪句柄（同一次解析内唯一，满足 entityByHandle 归档）。
func preR13HandleBase(base *entity.BaseEntity, h PreR13EntHead) {
	if h.Handle != 0 {
		base.Handle = uint64(h.Handle)
	} else {
		base.Handle = uint64(h.StartOff)
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
		if head.Size == 0 || next <= pos {
			break
		}
		switch head.Typ {
		case preR13TypeBlock:
			// 块边界锚点：BLOCK 记录起点相对块区起点的偏移
			offset := uint64(head.StartOff) - uint64(start)
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
					doc.Blocks[curKey] = append(doc.Blocks[curKey], ent)
					agg.step(ent, head.Flag&preR13FlagHasAttribs != 0)
				}
			}
		}
		pos = int(head.StartOff) + int(head.Size)
	}
}
