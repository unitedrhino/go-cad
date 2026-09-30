// 本文件实现表记录类内部对象的解码：BLOCK_HEADER、LTYPE、9 个表
// CONTROL、STYLE、VPORT、MLINESTYLE、DIMSTYLE、VX_CONTROL/
// VX_TABLE_RECORD/CELLSTYLEMAP。各表记录按 R13-R14/R2000-R2004/
// R2007+ 三档版本布局分流，字段序对照 dwgread -v9 位级日志。

package cad

import (
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/objrec"
)

// readCommonTableFlags 读取 COMMON_TABLE_FLAGS 并存储字段：
// pre-R2004 为 B is_xref_ref + BS is_xref_resolved + B is_xref_dep；
// R2004+ 仅 BS is_xref_resolved。
func readCommonTableFlags(r *bitstream.BitStream, fr *gfRead, g *objGeneric, ver dwgVersion) error {
	if !verUntilR2004(ver) {
		return fr.BS("is_xref_resolved", g)
	}
	xr, err := r.ReadB()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, objField{"is_xref_ref", xr != 0})
	if err := fr.BS("is_xref_resolved", g); err != nil {
		return err
	}
	xd, err := r.ReadB()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, objField{"is_xref_dep", xd != 0})
	return nil
}

// ---- BLOCK_HEADER（dwg.spec DWG_TABLE(BLOCK_HEADER)，固定码 0x31）----

// decodeGenericBLOCKHEADER BLOCK_HEADER 的 dat 流字段：
// COMMON_TABLE_FLAGS（name + is_xref_resolved）+ 块标志位 + num_owned +
// base_pt + xref_pname + num_inserts/description/preview +
// insert_units/explodable/block_scaling（R2007a+）。
func decodeGenericBLOCKHEADER(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := fr.T("name", g); err != nil {
		return err
	}
	if verUntilR2004(ver) {
		// COMMON_TABLE_FLAGS R2004 前：B is_xref_ref + BS is_xref_resolved + B is_xref_dep
		if xr, err := r.ReadB(); err != nil {
			return err
		} else {
			g.Fields = append(g.Fields, objField{"is_xref_ref", xr != 0})
		}
		if err := fr.BS("is_xref_resolved", g); err != nil {
			return err
		}
		if xd, err := r.ReadB(); err != nil {
			return err
		} else {
			g.Fields = append(g.Fields, objField{"is_xref_dep", xd != 0})
		}
	} else {
		if err := fr.BS("is_xref_resolved", g); err != nil {
			return err
		}
	}
	anon, err := r.ReadB()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, objField{"anonymous", anon != 0})
	ha, err := r.ReadB()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, objField{"hasattrs", ha != 0})
	bx0, err := r.ReadB()
	if err != nil {
		return err
	}
	bx := bx0 != 0
	g.Fields = append(g.Fields, objField{"blkisxref", bx})
	xo0, err := r.ReadB()
	if err != nil {
		return err
	}
	xo := xo0 != 0
	g.Fields = append(g.Fields, objField{"xrefoverlaid", xo})
	if ver >= verR2000 && ver != verR13 && ver != verR14 {
		// R2000b+：xref_loaded（R2000 及之后；R14 无）
		xl, err := r.ReadB()
		if err != nil {
			return err
		}
		g.Fields = append(g.Fields, objField{"xref_loaded", xl != 0})
	}
	// num_owned：R2004a+ 且非 xref 块才存在
	if ver >= verR2004 && bx0 == 0 && xo0 == 0 {
		if _, err = fr.BLv("num_owned", g); err != nil {
			return err
		}
	}
	if err := fr.Point3("base_pt", g); err != nil {
		return err
	}
	if err := fr.T("xref_pname", g); err != nil {
		return err
	}
	// num_inserts 块：R2000（AC1015）与 R2004+ 均有，仅 R14 无；
	// pre-R2004 时不读 R2007a+ 尾块
	if ver == verR13 || ver == verR14 {
		return nil // R2002 及更早：num_inserts/description/preview 与 R2007a+ 尾块均无
	}
	// num_inserts 块：R2000b+（含全部 R2004+/R2007+）必有，仅
	// pre-R2000b 的 AC1015 文件没有。
	// R2000b 二义性：AC1015 无法从版本号区分 pre/post-R2000b，用探测
	// 试读决定；R2007+ 探测会误用 TV 语义读 description（R2007+ 的
	// description 为字符串流 TU 不占 dat 位），且该版本必有本块，
	// 因此 R2004+ 跳过探测直接读取。
	if ver >= verR2007 {
		return readBlockHeaderR2000bBlock(r, fr, g, ver, true)
	}
	savedLen := len(g.Fields)
	savedBits := r.TellBits()
	if probeBlockHeaderR2000b(r, fr) {
		r.SetBitPos(savedBits)
		return readBlockHeaderR2000bBlock(r, fr, g, ver, false)
	}
	// pre-R2000b：回退位置与字段，跳过整块（直接是后续 handle 流）
	r.SetBitPos(savedBits)
	g.Fields = g.Fields[:savedLen]
	return nil
}

// readBlockHeaderR2000bBlock 读取 R2000b 块字段：num_inserts RC 计数
// 循环（遇 0 结束，计非零个数）+ description T + preview_size BL +
// preview 二进制；withTail 为 true 时（R2007a+）继续读 insert_units/
// explodable/block_scaling 尾块。
func readBlockHeaderR2000bBlock(r *bitstream.BitStream, fr *gfRead, g *objGeneric, ver dwgVersion, withTail bool) error {
	ni := int64(0)
	for {
		b, e := r.ReadRC()
		if e != nil {
			break
		}
		if b == 0 {
			break
		}
		ni++
	}
	g.Fields = append(g.Fields, objField{"num_inserts", ni})
	if e := fr.T("description", g); e != nil {
		return e
	}
	ps, e2 := fr.BLv("preview_size", g)
	if e2 != nil {
		return e2
	}
	b, e := r.ReadRCS(int(ps))
	if e != nil {
		return e
	}
	g.Fields = append(g.Fields, objField{"preview", fmt.Sprintf("%X", b)})
	if !withTail {
		return nil
	}
	if e := fr.BS("insert_units", g); e != nil {
		return e
	}
	if e := fr.B("explodable", g); e != nil {
		return e
	}
	return fr.RC("block_scaling", g)
}

// decodeGenericBLOCKHEADER_HDL BLOCK_HEADER 的 handle 流
// （owner/reactors/xdic 之后）：xref + block_entity + entities×num_owned +
// endblk_entity + inserts×num_inserts + layout。
func decodeGenericBLOCKHEADER_HDL(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	inserts := 0
	if v, ok := g.Field("num_inserts").(int64); ok && v >= 0 && v < 0xf00000 {
		inserts = int(v)
	}
	// handle 流引用数（owner/reactors/xdic 之后）：
	// R2004+：xref + block_entity + entities×num_owned + endblk + inserts×N + layout
	// R2000：xref + block_entity + first + last + endblk + inserts×N + layout
	// R14：xref + block_entity + first + last + endblk（无 inserts/layout）
	n := 0
	switch {
	case ver >= verR2004:
		owned := 0
		if v, ok := g.Field("num_owned").(int64); ok {
			owned = int(v)
		}
		n = 3 + owned + inserts
	case ver == verR2000:
		n = 5 + inserts
	default:
		n = 5
	}
	for i := 0; i < n; i++ {
		h, e := objrec.ReadHandleReference(r, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// BLv 读 BL 字段并返回数值（用于后续引用数量判定）。
func (f *gfRead) BLv(key string, g *objGeneric) (int64, error) {
	v, err := f.r.ReadBL()
	if err != nil {
		return 0, err
	}
	g.Fields = append(g.Fields, objField{key, int64(v)})
	return int64(v), nil
}

// RL 读 RL 字段。
func (f *gfRead) RL(key string, g *objGeneric) error {
	v, err := f.r.ReadRL()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, objField{key, int64(v)})
	return nil
}

// ---- LTYPE（dwg.spec DWG_TABLE(LTYPE)，固定码 0x39）----

// decodeGenericLTYPE 解析 LTYPE 线型表记录：
// dat 流 = COMMON_TABLE_FLAGS + T description + BD pattern_len + RC alignment
// + RCu numdashes + dashes×N（BD length + BS shapecode + ...，当前语料
// numdashes 恒 0，dash 的 shape 细节后续按需扩展）；
// handle 流 = owner + reactors + xdic + xref + dashes×N style。
func decodeGenericLTYPE(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	// COMMON_TABLE_FLAGS：name + is_xref_resolved（R2004+）/三字段（pre-R2004）
	if err := fr.T("name", g); err != nil {
		return err
	}
	if err := readCommonTableFlags(r, fr, g, ver); err != nil {
		return err
	}
	if err := fr.T("description", g); err != nil {
		return err
	}
	if err := fr.BD("pattern_len", g); err != nil {
		return err
	}
	if err := fr.RC("alignment", g); err != nil {
		return err
	}
	numdashes, err := fr.RCv("numdashes", g)
	if err != nil {
		return err
	}
	if numdashes > 0 {
		// dash 的 shape 细节（complex_shapecode/style/scale/rotation 等）
		// 当前语料未出现，遇到时按 spec 逐字段扩展
		return fmt.Errorf("cad: LTYPE dashes=%d 暂不支持", numdashes)
	}
	// UNTIL(R_2004)（含 R_2004）：strings_area 固定 256 字节 BINARY
	// （复杂线型的文字区）
	if ver <= verR2004 {
		b, err := r.ReadRCS(256)
		if err != nil {
			return err
		}
		g.Fields = append(g.Fields, objField{"strings_area", fmt.Sprintf("%X", b)})
	}
	return nil
}

// RCv 读 RC 字段并返回数值。
func (f *gfRead) RCv(key string, g *objGeneric) (int64, error) {
	v, err := f.r.ReadRC()
	if err != nil {
		return 0, err
	}
	g.Fields = append(g.Fields, objField{key, int64(v)})
	return int64(v), nil
}

// ---- 表 CONTROL 对象（BLOCK/LAYER/STYLE/LTYPE/VIEW/UCS/VPORT/APPID/DIMSTYLE_CONTROL）----

// controlSpec CONTROL 对象的差异描述。
type controlSpec struct {
	useBS       bool // num_entries 用 BS 读取（否则 BL）
	extraFixed  int  // entries 向量之后的固定 handle 数（BLOCK: model/paper，LTYPE: byblock/bylayer）
	moreHandles bool // DIMSTYLE：R2000b+ 的 RCu num_morehandles + morehandles 向量
}

// controlSpecs 固定码 → CONTROL 差异描述。
var controlSpecs = map[uint16]controlSpec{
	0x30: {extraFixed: 2},                  // BLOCK_CONTROL：model_space + paper_space
	0x32: {},                               // LAYER_CONTROL
	0x34: {},                               // STYLE_CONTROL
	0x38: {extraFixed: 2},                  // LTYPE_CONTROL：byblock + bylayer
	0x3C: {},                               // VIEW_CONTROL
	0x3E: {},                               // UCS_CONTROL
	0x40: {},                               // VPORT_CONTROL
	0x42: {},                               // APPID_CONTROL
	0x44: {useBS: true, moreHandles: true}, // DIMSTYLE_CONTROL
	0x46: {useBS: true},                    // VX_CONTROL
}

// decodeGenericCONTROL 解析 CONTROL 对象的 dat 流：
// num_entries（BL/BS 视类型）+ [DIMSTYLE: RCu num_morehandles]。
func decodeGenericCONTROL(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	cs, ok := controlSpecs[g.controlType]
	if !ok {
		return fmt.Errorf("cad: 无 CONTROL 规格 0x%X", g.controlType)
	}
	var err error
	if cs.useBS {
		err = fr.BS("num_entries", g)
	} else {
		err = fr.BL("num_entries", g)
	}
	if err != nil {
		return err
	}
	// DIMSTYLE_CONTROL：R2000b+ 的 num_morehandles RCu（dat 流字段）
	if cs.moreHandles && ver >= verR2000 && ver != verR13 && ver != verR14 {
		nm, e := r.ReadRC()
		if e != nil {
			return e
		}
		g.Fields = append(g.Fields, objField{"num_morehandles", int64(nm)})
	}
	return nil
}

// decodeGenericCONTROL_HDL CONTROL 对象的 handle 流
// （owner/reactors/xdic 之后）：entries×num_entries + 特定 handle
// + [DIMSTYLE: morehandles×N]。
func decodeGenericCONTROL_HDL(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	cs := controlSpecs[g.controlType]
	n := 0
	if v, ok := g.Field("num_entries").(int64); ok {
		n = int(v)
	}
	extra := cs.extraFixed
	if cs.moreHandles {
		if v, ok := g.Field("num_morehandles").(int64); ok {
			extra = int(v)
		}
	}
	for i := 0; i < n+extra; i++ {
		h, e := objrec.ReadHandleReference(r, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// ---- STYLE（dwg.spec DWG_TABLE(STYLE)，固定码 0x35，文字样式表记录）----

// decodeGenericSTYLE 解析 STYLE：
// COMMON_TABLE_FLAGS（name + is_xref_resolved/pre-R2004 三字段）+
// B is_shape + B is_vertical + BD text_size + BD width_factor +
// BD oblique_angle + RC generation + BD last_height +
// T font_file + T bigfont_file（R2007+ 字符串流）。
func decodeGenericSTYLE(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := fr.T("name", g); err != nil {
		return err
	}
	if err := readCommonTableFlags(r, fr, g, ver); err != nil {
		return err
	}
	if err := fr.B("is_shape", g); err != nil {
		return err
	}
	if err := fr.B("is_vertical", g); err != nil {
		return err
	}
	if err := fr.BD("text_size", g); err != nil {
		return err
	}
	if err := fr.BD("width_factor", g); err != nil {
		return err
	}
	if err := fr.BD("oblique_angle", g); err != nil {
		return err
	}
	if err := fr.RC("generation", g); err != nil {
		return err
	}
	if err := fr.BD("last_height", g); err != nil {
		return err
	}
	if err := fr.T("font_file", g); err != nil {
		return err
	}
	return fr.T("bigfont_file", g)
}

// ---- VPORT（dwg.spec DWG_TABLE(VPORT)，固定码 0x41，视口表记录）----
// 三种版本布局，字段序均对照 dwgread -v9 位级日志逐字段校准
// （example_r14 / example_2000 / example_2007）。

// decodeGenericVPORT 解析 VPORT 的 dat 流公共头并按版本分流：
// name TV + COMMON_TABLE_FLAGS（pre-R2004 为 B is_xref_ref +
// BS is_xref_resolved + B is_xref_dep，R2004+ 仅 BS）+ VIEWSIZE/view_width
// BD + VIEWCTR 2RD + view_target/VIEWDIR 3BD + VIEWTWIST/LENSLENGTH/
// FRONTZ/BACKZ BD + VIEWMODE 4BITS。xref、[R2007+ background/visualstyle/
// sun]、[R2000b+ named_ucs/base_ucs] 句柄在 handle 流
// （见 decodeGenericVPORT_HDL）。
func decodeGenericVPORT(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := fr.T("name", g); err != nil {
		return err
	}
	if err := readCommonTableFlags(r, fr, g, ver); err != nil {
		return err
	}
	// 公共视口几何：VIEWSIZE/view_width BD（aspect_ratio 由二者计算，
	// 不占位）→ VIEWCTR 2RD → view_target/VIEWDIR 3BD → VIEWTWIST/
	// LENSLENGTH/FRONTZ/BACKZ BD → VIEWMODE 4BITS
	if err := fr.BD("VIEWSIZE", g); err != nil {
		return err
	}
	vw, err := fr.BDv("view_width", g)
	if err != nil {
		return err
	}
	if vs, ok := g.Field("VIEWSIZE").(float64); ok && vs != 0 {
		g.Fields = append(g.Fields, objField{"aspect_ratio", vw / vs})
	} else {
		g.Fields = append(g.Fields, objField{"aspect_ratio", 0.0})
	}
	if err := fr.Point2RD("VIEWCTR", g); err != nil {
		return err
	}
	if err := fr.Point3("view_target", g); err != nil {
		return err
	}
	if err := fr.Point3("VIEWDIR", g); err != nil {
		return err
	}
	if err := fr.BD("VIEWTWIST", g); err != nil {
		return err
	}
	if err := fr.BD("LENSLENGTH", g); err != nil {
		return err
	}
	if err := fr.BD("FRONTZ", g); err != nil {
		return err
	}
	if err := fr.BD("BACKZ", g); err != nil {
		return err
	}
	if err := fr.FourBits("VIEWMODE", g); err != nil {
		return err
	}
	if ver != verR13 && ver != verR14 {
		// render_mode RC（SINCE R_2000b）
		if err := fr.RC("render_mode", g); err != nil {
			return err
		}
	}
	if ver >= verR2007 {
		// R2007a+ 渲染参数（background/visualstyle/sun 句柄在 handle 流）
		if err := fr.B("use_default_lights", g); err != nil {
			return err
		}
		if err := fr.RC("default_lightning_type", g); err != nil {
			return err
		}
		if err := fr.BD("brightness", g); err != nil {
			return err
		}
		if err := fr.BD("contrast", g); err != nil {
			return err
		}
		if err := fr.CMC("ambient_color", g); err != nil {
			return err
		}
	}
	// 公共屏幕/捕捉区：lower_left/upper_right 2RD → UCSFOLLOW B →
	// circle_zoom BS → FASTZOOM B → UCSICON BB → GRIDMODE B →
	// GRIDUNIT 2RD → SNAPMODE B → SNAPSTYLE B → SNAPISOPAIR BS →
	// SNAPANG BD → SNAPBASE 2RD → SNAPUNIT 2RD
	if err := fr.Point2RD("lower_left", g); err != nil {
		return err
	}
	if err := fr.Point2RD("upper_right", g); err != nil {
		return err
	}
	if err := fr.B("UCSFOLLOW", g); err != nil {
		return err
	}
	if err := fr.BS("circle_zoom", g); err != nil {
		return err
	}
	if err := fr.B("FASTZOOM", g); err != nil {
		return err
	}
	if err := fr.BB("UCSICON", g); err != nil {
		return err
	}
	if err := fr.B("GRIDMODE", g); err != nil {
		return err
	}
	if err := fr.Point2RD("GRIDUNIT", g); err != nil {
		return err
	}
	if err := fr.B("SNAPMODE", g); err != nil {
		return err
	}
	if err := fr.B("SNAPSTYLE", g); err != nil {
		return err
	}
	if err := fr.BS("SNAPISOPAIR", g); err != nil {
		return err
	}
	if err := fr.BD("SNAPANG", g); err != nil {
		return err
	}
	if err := fr.Point2RD("SNAPBASE", g); err != nil {
		return err
	}
	if err := fr.Point2RD("SNAPUNIT", g); err != nil {
		return err
	}
	if ver == verR13 || ver == verR14 {
		// R13/R14 到 SNAPUNIT 为止，无 UCS 系与 UCSORTHOVIEW
		return nil
	}
	// UCS 系（SINCE R_2000b）：ucs_at_origin/UCSVP B → ucsorg/ucsxdir/
	// ucsydir 3BD → ucs_elevation BD → UCSORTHOVIEW BS
	if err := fr.B("ucs_at_origin", g); err != nil {
		return err
	}
	if err := fr.B("UCSVP", g); err != nil {
		return err
	}
	for _, k := range []string{"ucsorg", "ucsxdir", "ucsydir"} {
		if err := fr.Point3(k, g); err != nil {
			return err
		}
	}
	if err := fr.BD("ucs_elevation", g); err != nil {
		return err
	}
	if err := fr.BS("UCSORTHOVIEW", g); err != nil {
		return err
	}
	if ver >= verR2007 {
		// grid_flags/grid_major BS（SINCE R_2007a）
		if err := fr.BS("grid_flags", g); err != nil {
			return err
		}
		if err := fr.BS("grid_major", g); err != nil {
			return err
		}
	}
	return nil
}

// decodeGenericVPORT_HDL VPORT 的 handle 流（owner/reactors/xdic 之后）：
// xref（COMMON_TABLE_FLAGS，全版本）+ [R2007+ background/visualstyle/sun]
// + [R2000b+ named_ucs/base_ucs]。R14 为 1 个，R2000-R2004 为 3 个，
// R2007+ 为 6 个。
func decodeGenericVPORT_HDL(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	n := 1
	switch ver {
	case verR2000, verR2004:
		n = 3
	case verR2007, verR2010, verR2013, verR2018:
		n = 6
	}
	for i := 0; i < n; i++ {
		h, e := objrec.ReadHandleReference(r, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// BB 读 2 位字段（如 UCSICON）。
func (f *gfRead) BB(key string, g *objGeneric) error {
	v, err := f.r.ReadBB()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, objField{key, int64(v)})
	return nil
}

// BDv 读 BD 字段并返回数值（需要参与计算的 BD 字段用）。
func (f *gfRead) BDv(key string, g *objGeneric) (float64, error) {
	v, err := f.r.ReadBD()
	if err != nil {
		return 0, err
	}
	g.Fields = append(g.Fields, objField{key, v})
	return v, nil
}

// FourBits 读 4 位字段（如 VPORT 的 VIEWMODE）。
func (f *gfRead) FourBits(key string, g *objGeneric) error {
	v, err := f.r.ReadBitsMsb(4)
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, objField{key, int64(v)})
	return nil
}

// ---- MLINESTYLE（dwg.spec，固定码 0x49，多线样式表记录）----

// decodeGenericMLINESTYLE 解析 MLINESTYLE 的 dat 流：
// T name + T description + BS flag + CMC fill_color + BD start_angle +
// BD end_angle + BL num_lines + lines×N（BD offset + CMC color +
// BS lt_type + BS lt_count + lt handles 计数暂存）。
// lines 的线型句柄在 handle 流尾。
func decodeGenericMLINESTYLE(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := fr.T("name", g); err != nil {
		return err
	}
	if err := fr.T("description", g); err != nil {
		return err
	}
	if err := fr.BS("flag", g); err != nil {
		return err
	}
	if err := fr.CMC("fill_color", g); err != nil {
		return err
	}
	if err := fr.BD("start_angle", g); err != nil {
		return err
	}
	if err := fr.BD("end_angle", g); err != nil {
		return err
	}
	// num_lines 为 RC（非 BL）
	numLines, err := fr.RCv("num_lines", g)
	if err != nil {
		return err
	}
	for i := 0; i < int(numLines); i++ {
		lp := fmt.Sprintf("lines[%d].", i)
		if err := fr.BD(lp+"offset", g); err != nil {
			return err
		}
		if err := fr.CMC(lp+"color", g); err != nil {
			return err
		}
		// PRE-R2018：每条线尾部有 BSd lt_index（32767 = BYLAYER 默认）；
		// R2018+ 改为 handle 流的 lt_ltype 句柄（dat 流 0 位）
		if ver < verR2018 {
			if err := fr.BS(lp+"lt_index", g); err != nil {
				return err
			}
		}
	}
	return nil
}

// BSv 读 BS 字段并返回数值。
func (f *gfRead) BSv(key string, g *objGeneric) (int64, error) {
	v, err := f.r.ReadBS()
	if err != nil {
		return 0, err
	}
	g.Fields = append(g.Fields, objField{key, int64(v)})
	return int64(v), nil
}

// ---- DIMSTYLE（dwg.spec DWG_TABLE(DIMSTYLE)，固定码 0x45，标注样式表记录）----
// 三种版本布局，字段序均对照 dwgread -v9 位级日志逐字段校准
// （example_r14 / example_2000 / example_2018）。

// decodeGenericDIMSTYLE 解析 DIMSTYLE 的 dat 流公共头并按版本分流：
// name TV + COMMON_TABLE_FLAGS（pre-R2004 为 B is_xref_ref +
// BS is_xref_resolved + B is_xref_dep，R2004+ 仅 BS）。DIMTXSTY、
// xref 与 DIMLDRBLK/DIMBLK/DIMBLK1/DIMBLK2/[R2007+ DIMLTYPE/
// DIMLTEX1/DIMLTEX2] 句柄在 handle 流（见 decodeGenericDIMSTYLE_HDL）。
func decodeGenericDIMSTYLE(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := fr.T("name", g); err != nil {
		return err
	}
	if err := readCommonTableFlags(r, fr, g, ver); err != nil {
		return err
	}
	switch ver {
	case verR13, verR14:
		return decodeDIMSTYLE_r14(fr, g)
	case verR2000, verR2004:
		return decodeDIMSTYLE_r2000(fr, g)
	default:
		return decodeDIMSTYLE_r2007(fr, g)
	}
}

// decodeDIMSTYLE_r14 R13/R14 布局：11×B 布尔 + RC/BS 混合状态位 +
// 6×BS 单位系列 + 17×BD 尺寸系列 + 5×TV 文字 + 3×CMC + flag0 B。
// 无 DIMALTRND/DIMADEC/DIMLWD/DIMLWE 等 R2000b+ 字段。
func decodeDIMSTYLE_r14(fr *gfRead, g *objGeneric) error {
	// 11×B
	for _, k := range []string{"DIMTOL", "DIMLIM", "DIMTIH", "DIMTOH", "DIMSE1",
		"DIMSE2", "DIMALT", "DIMTOFL", "DIMSAH", "DIMTIX", "DIMSOXD"} {
		if err := fr.B(k, g); err != nil {
			return err
		}
	}
	// DIMALTD/DIMZIN 为 RC（8 位，非 BS）
	if err := fr.RC("DIMALTD", g); err != nil {
		return err
	}
	if err := fr.RC("DIMZIN", g); err != nil {
		return err
	}
	if err := fr.B("DIMSD1", g); err != nil {
		return err
	}
	if err := fr.B("DIMSD2", g); err != nil {
		return err
	}
	// DIMTOLJ/DIMJUST/DIMFIT 为 RC
	for _, k := range []string{"DIMTOLJ", "DIMJUST", "DIMFIT"} {
		if err := fr.RC(k, g); err != nil {
			return err
		}
	}
	if err := fr.B("DIMUPT", g); err != nil {
		return err
	}
	// DIMTZIN/DIMALTZ/DIMALTTZ/DIMTAD 为 RC
	for _, k := range []string{"DIMTZIN", "DIMALTZ", "DIMALTTZ", "DIMTAD"} {
		if err := fr.RC(k, g); err != nil {
			return err
		}
	}
	// 6×BS 单位/精度系列
	for _, k := range []string{"DIMUNIT", "DIMAUNIT", "DIMDEC", "DIMTDEC",
		"DIMALTU", "DIMALTTD"} {
		if err := fr.BS(k, g); err != nil {
			return err
		}
	}
	// 9×BD 基础尺寸系列
	for _, k := range []string{"DIMSCALE", "DIMASZ", "DIMEXO", "DIMDLI", "DIMEXE",
		"DIMRND", "DIMDLE", "DIMTP", "DIMTM"} {
		if err := fr.BD(k, g); err != nil {
			return err
		}
	}
	// 8×BD 变量系列
	for _, k := range []string{"DIMTXT", "DIMCEN", "DIMTSZ", "DIMALTF", "DIMLFAC",
		"DIMTVP", "DIMTFAC", "DIMGAP"} {
		if err := fr.BD(k, g); err != nil {
			return err
		}
	}
	// 5×TV（DIMBLK 系列仅 R13/R14 内联）
	for _, k := range []string{"DIMPOST", "DIMAPOST", "DIMBLK_T", "DIMBLK1_T", "DIMBLK2_T"} {
		if err := fr.T(k, g); err != nil {
			return err
		}
	}
	// 3×CMC 颜色
	for _, k := range []string{"DIMCLRD", "DIMCLRE", "DIMCLRT"} {
		if err := fr.CMC(k, g); err != nil {
			return err
		}
	}
	return fr.B("flag0", g)
}

// decodeDIMSTYLE_r2000 R2000-R2004 布局：DIMPOST/DIMAPOST TV 在
// DIMSCALE 之前（v9 实测 @142/@144）；无 DIMFXL/DIMJOGANG/DIMTFILL 系
// （SINCE R_2007a）；DIMLWD/DIMLWE 为 BSd 有符号（-2 = BB 00 + RS
// 0xFFFE，18 位）；无 R2007+ 尾块。
func decodeDIMSTYLE_r2000(fr *gfRead, g *objGeneric) error {
	if err := fr.T("DIMPOST", g); err != nil {
		return err
	}
	if err := fr.T("DIMAPOST", g); err != nil {
		return err
	}
	// 9×BD 基础尺寸系列
	for _, k := range []string{"DIMSCALE", "DIMASZ", "DIMEXO", "DIMDLI", "DIMEXE",
		"DIMRND", "DIMDLE", "DIMTP", "DIMTM"} {
		if err := fr.BD(k, g); err != nil {
			return err
		}
	}
	// 6×B + 3×BS 状态位
	for _, k := range []string{"DIMTOL", "DIMLIM", "DIMTIH", "DIMTOH", "DIMSE1", "DIMSE2"} {
		if err := fr.B(k, g); err != nil {
			return err
		}
	}
	for _, k := range []string{"DIMTAD", "DIMZIN", "DIMAZIN"} {
		if err := fr.BS(k, g); err != nil {
			return err
		}
	}
	// 9×BD 变量系列（含 DIMALTRND，R2000 与 R2007+ 均有）
	for _, k := range []string{"DIMTXT", "DIMCEN", "DIMTSZ", "DIMALTF", "DIMLFAC",
		"DIMTVP", "DIMTFAC", "DIMGAP", "DIMALTRND"} {
		if err := fr.BD(k, g); err != nil {
			return err
		}
	}
	if err := fr.B("DIMALT", g); err != nil {
		return err
	}
	if err := fr.BS("DIMALTD", g); err != nil {
		return err
	}
	// 4×B + 3×CMC 颜色
	for _, k := range []string{"DIMTOFL", "DIMSAH", "DIMTIX", "DIMSOXD"} {
		if err := fr.B(k, g); err != nil {
			return err
		}
	}
	for _, k := range []string{"DIMCLRD", "DIMCLRE", "DIMCLRT"} {
		if err := fr.CMC(k, g); err != nil {
			return err
		}
	}
	// 11×BS 精度/单位系列
	for _, k := range []string{"DIMADEC", "DIMDEC", "DIMTDEC", "DIMALTU", "DIMALTTD",
		"DIMAUNIT", "DIMFRAC", "DIMLUNIT", "DIMDSEP", "DIMTMOVE", "DIMJUST"} {
		if err := fr.BS(k, g); err != nil {
			return err
		}
	}
	if err := fr.B("DIMSD1", g); err != nil {
		return err
	}
	if err := fr.B("DIMSD2", g); err != nil {
		return err
	}
	for _, k := range []string{"DIMTOLJ", "DIMTZIN", "DIMALTZ", "DIMALTTZ"} {
		if err := fr.BS(k, g); err != nil {
			return err
		}
	}
	if err := fr.B("DIMUPT", g); err != nil {
		return err
	}
	if err := fr.BS("DIMATFIT", g); err != nil {
		return err
	}
	if err := fr.BSd("DIMLWD", g); err != nil {
		return err
	}
	if err := fr.BSd("DIMLWE", g); err != nil {
		return err
	}
	return fr.B("flag0", g)
}

// decodeDIMSTYLE_r2007 R2007+ 布局：DIMPOST/DIMAPOST 走字符串流
// （dat 不占位）；DIMTFILL + DIMTFILLCLR CMC（SINCE R_2007a）；
// DIMARCSYM BS；尾块按版本分档——R2007 仅 DIMFXLON B，
// R2010+ 追加 DIMTXTDIRECTION B + DIMALTMZF/DIMALTMZS + DIMMZF/DIMMZS。
func decodeDIMSTYLE_r2007(fr *gfRead, g *objGeneric) error {
	// 字符串流字段（dat 不占位，仅为推进字符串序列）
	if err := fr.T("DIMPOST", g); err != nil {
		return err
	}
	if err := fr.T("DIMAPOST", g); err != nil {
		return err
	}
	// 9×BD 基础尺寸系列
	for _, k := range []string{"DIMSCALE", "DIMASZ", "DIMEXO", "DIMDLI", "DIMEXE",
		"DIMRND", "DIMDLE", "DIMTP", "DIMTM"} {
		if err := fr.BD(k, g); err != nil {
			return err
		}
	}
	// DIMFXL/DIMJOGANG/DIMTFILL/DIMTFILLCLR（SINCE R_2007a）
	if err := fr.BD("DIMFXL", g); err != nil {
		return err
	}
	if err := fr.BD("DIMJOGANG", g); err != nil {
		return err
	}
	if err := fr.BS("DIMTFILL", g); err != nil {
		return err
	}
	if err := fr.CMC("DIMTFILLCLR", g); err != nil {
		return err
	}
	// 6×B + 3×BS 状态位
	for _, k := range []string{"DIMTOL", "DIMLIM", "DIMTIH", "DIMTOH", "DIMSE1", "DIMSE2"} {
		if err := fr.B(k, g); err != nil {
			return err
		}
	}
	for _, k := range []string{"DIMTAD", "DIMZIN", "DIMAZIN"} {
		if err := fr.BS(k, g); err != nil {
			return err
		}
	}
	// DIMARCSYM（SINCE R_2007a）
	if err := fr.BS("DIMARCSYM", g); err != nil {
		return err
	}
	// 9×BD 变量系列（含 DIMALTRND）
	for _, k := range []string{"DIMTXT", "DIMCEN", "DIMTSZ", "DIMALTF", "DIMLFAC",
		"DIMTVP", "DIMTFAC", "DIMGAP", "DIMALTRND"} {
		if err := fr.BD(k, g); err != nil {
			return err
		}
	}
	if err := fr.B("DIMALT", g); err != nil {
		return err
	}
	if err := fr.BS("DIMALTD", g); err != nil {
		return err
	}
	// 4×B + 3×CMC 颜色
	for _, k := range []string{"DIMTOFL", "DIMSAH", "DIMTIX", "DIMSOXD"} {
		if err := fr.B(k, g); err != nil {
			return err
		}
	}
	for _, k := range []string{"DIMCLRD", "DIMCLRE", "DIMCLRT"} {
		if err := fr.CMC(k, g); err != nil {
			return err
		}
	}
	// 11×BS 精度/单位系列
	for _, k := range []string{"DIMADEC", "DIMDEC", "DIMTDEC", "DIMALTU", "DIMALTTD",
		"DIMAUNIT", "DIMFRAC", "DIMLUNIT", "DIMDSEP", "DIMTMOVE", "DIMJUST"} {
		if err := fr.BS(k, g); err != nil {
			return err
		}
	}
	if err := fr.B("DIMSD1", g); err != nil {
		return err
	}
	if err := fr.B("DIMSD2", g); err != nil {
		return err
	}
	for _, k := range []string{"DIMTOLJ", "DIMTZIN", "DIMALTZ", "DIMALTTZ"} {
		if err := fr.BS(k, g); err != nil {
			return err
		}
	}
	if err := fr.B("DIMUPT", g); err != nil {
		return err
	}
	if err := fr.BS("DIMATFIT", g); err != nil {
		return err
	}
	// DIMFXLON（SINCE R_2007a）
	if err := fr.B("DIMFXLON", g); err != nil {
		return err
	}
	// DIMTXTDIRECTION + DIMALTMZF/DIMALTMZS + DIMMZF/DIMMZS（SINCE R_2010b）
	if fr.ver.r2010Plus() {
		if err := fr.B("DIMTXTDIRECTION", g); err != nil {
			return err
		}
		if err := fr.BD("DIMALTMZF", g); err != nil {
			return err
		}
		if err := fr.T("DIMALTMZS", g); err != nil {
			return err
		}
		if err := fr.BD("DIMMZF", g); err != nil {
			return err
		}
		if err := fr.T("DIMMZS", g); err != nil {
			return err
		}
	}
	// DIMLWD/DIMLWE BSd（SINCE R_2000b）+ flag0 B
	if err := fr.BSd("DIMLWD", g); err != nil {
		return err
	}
	if err := fr.BSd("DIMLWE", g); err != nil {
		return err
	}
	return fr.B("flag0", g)
}

// decodeGenericDIMSTYLE_HDL DIMSTYLE 的 handle 流
// （owner/reactors/xdic 之后）：xref + DIMTXSTY + DIMLDRBLK + DIMBLK +
// DIMBLK1 + DIMBLK2 + [R2007+ DIMLTYPE/DIMLTEX1/DIMLTEX2]。
// R14 为 2 个（xref + DIMTXSTY），R2000-R2004 为 6 个，R2007+ 为 9 个。
func decodeGenericDIMSTYLE_HDL(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	n := 6
	switch ver {
	case verR13, verR14:
		n = 2
	case verR2007, verR2010, verR2013, verR2018:
		n = 9
	}
	for i := 0; i < n; i++ {
		h, e := objrec.ReadHandleReference(r, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// ---- VX_CONTROL/VX_TABLE_RECORD/CELLSTYLEMAP ----

// decodeGenericCELLSTYLEMAP 解析 CELLSTYLEMAP（类 527）：
// BL num_cells + cells×N（BL id + BL type + T name）。
func decodeGenericCELLSTYLEMAP(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	numCells, err := fr.BLv("num_cells", g)
	if err != nil {
		return err
	}
	if numCells > 10000 {
		numCells = 10000
	}
	nHdl := 0
	for i := 0; i < int(numCells); i++ {
		p := fmt.Sprintf("cells[%d].", i)
		// cellstyle 与 id/type/name 之间：text_style/borders ltype 句柄
		// 在 handle 流
		if err := readCellStyleFields(r, fr, g, p+"cellstyle.", &nHdl); err != nil {
			return err
		}
		if err := fr.BL(p+"id", g); err != nil {
			return err
		}
		if err := fr.BL(p+"type", g); err != nil {
			return err
		}
		if err := fr.T(p+"name", g); err != nil {
			return err
		}
	}
	g.Fields = append(g.Fields, objField{"num_style_handles", int64(nHdl)})
	return nil
}

// decodeGenericVX_TABLE_RECORD 解析 VX_TABLE_RECORD（固定码 0x47）：
// COMMON_TABLE_FLAGS（pre-R2004 三字段）+ RS vport_entity_address +
// RSd r11_viewport_index + RSd r11_prev_entry_index + B is_on；
// handle 流含 viewport + prev_entry（2 个）。
func decodeGenericVX_TABLE_RECORD(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	if err := fr.T("name", g); err != nil {
		return err
	}
	if err := readCommonTableFlags(r, fr, g, ver); err != nil {
		return err
	}
	// PRE(R_13b1) 才有 vport_entity_address 等 3×RS；R13b1+ 读 is_on B
	// （本库支持的最老版本 AC1012 已属 LATER_VERSIONS）
	isOn, err := r.ReadB()
	if err != nil {
		return err
	}
	g.Fields = append(g.Fields, objField{"is_on", isOn != 0})
	return nil
}

// decodeGenericVX_TABLE_RECORD_HDL VX_TABLE_RECORD 的 handle 流：
// viewport + prev_entry（2 个）。
func decodeGenericVX_TABLE_RECORD_HDL(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	for i := 0; i < 2; i++ {
		h, e := objrec.ReadHandleReference(r, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}

// decodeGenericCELLSTYLEMAP_HDL CELLSTYLEMAP 的 handle 流：各 cell 的
// content_format.text_style 与 borders ltype（顺序由 num_style_handles 决定）。
func decodeGenericCELLSTYLEMAP_HDL(r *bitstream.BitStream, ver dwgVersion, fr *gfRead, g *objGeneric) error {
	n := 0
	if v, ok := g.Field("num_style_handles").(int64); ok {
		n = int(v)
	}
	for i := 0; i < n; i++ {
		h, e := objrec.ReadHandleReference(r, g.Handle)
		if e != nil {
			return e
		}
		g.Handles = append(g.Handles, h)
	}
	return nil
}
