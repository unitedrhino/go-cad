// entities_mleader.go MULTILEADER 多重引线实体解码（dwg2.spec
// DWG_ENTITY(MULTILEADER)，AcDbMLeader）。主体位流按 spec 顺序直读：
// 三层嵌套 REPEAT（leaders→lines→breaks/points）、ctx 内容联合
// （txt ~25 字段 / blk ~7 字段二选一）、R2010b/R2013b 版本分支；
// 所有 FIELD_HANDLE 在尾部 handle 流按 spec 顺序读取（dat 不占位，
// 对齐 LibreDWG obj_handle_stream + VALUE_HANDLE 行为）；R2007+ 的
// T 字符串存于记录尾字符串区（obj_string_stream），dat 不占位。
package cad

import (
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/objrec"
	"math"
)

// mleaderCMC MULTILEADER 颜色字段：isTrue 标记 R2004+ 结构（BS index +
// BL rgb + RC flag，含 method 修正与调色板反查），更早版本仅 BS 索引。
type mleaderCMC struct {
	index  uint16
	rgb    uint32
	flag   uint8
	isTrue bool
}

// mleaderTxtContent ctx 的 txt 内容分支（has_content_txt=1）。
type mleaderTxtContent struct {
	defaultText       string // TV/TU 304（R2007+ 不占 dat 位）
	normal            point3
	styleHandle       uint64 // H 340（handle 流）
	location          point3
	direction         point3
	rotation          float64
	width             float64
	height            float64
	lineSpacingFactor float64
	lineSpacingStyle  uint16
	color             mleaderCMC
	alignment         uint16
	flow              uint16
	bgColor           mleaderCMC
	bgScale           float64
	bgTransparency    uint32
	isBgFill          bool
	isBgMaskFill      bool
	colType           uint16
	isHeightAuto      bool
	colWidth          float64
	colGutter         float64
	isColFlowReversed bool
	numColSizes       uint32
	colSizes          []float64
	wordBreak         bool
	unknown           bool
}

// mleaderBlkContent ctx 的 blk 内容分支（has_content_txt=0 且
// has_content_blk=1，块参照内容；九样本未出现，按 spec 实现保位序）。
type mleaderBlkContent struct {
	blockTable uint64 // H 341（handle 流）
	normal     point3
	location   point3
	scale      point3
	rotation   float64
	color      mleaderCMC
	transform  [16]float64
}

// mleaderBreak 引线断开起点/终点（数组键，审计不比对）。
type mleaderBreak struct{ start, end point3 }

// mleaderLine 引线节下的单条引线。
type mleaderLine struct {
	points    []point3
	numBreaks uint32
	breaks    []mleaderBreak
	lineIndex uint32
	// SINCE R_2010b
	mleaderType uint16
	color       mleaderCMC
	ltype       uint64 // H 340（handle 流）
	linewt      int32  // BLd
	arrowSize   float64
	arrowHandle uint64 // H 341（handle 流）
	flags       uint32
}

// mleaderNode 引线节点（ctx.leaders[i]）。
type mleaderNode struct {
	hasLastLeaderLinePoint bool
	lastLeaderLinePoint    point3
	hasDogleg              bool
	doglegVector           point3
	numBreaks              uint32
	breaks                 []mleaderBreak
	branchIndex            uint32
	doglegLength           float64
	numLines               uint32
	lines                  []mleaderLine
	// SINCE R_2010b
	attachDir uint16
}

// mleaderArrowhead / mleaderBlockLabel R14-R2007 的箭头/块标签数组项。
type mleaderArrowhead struct {
	isDefault bool
	arrowhead uint64 // H 345（handle 流）
}

type mleaderBlockLabel struct {
	attdef    uint64 // H 330（handle 流）
	labelText string // T 302（R2007+ 不占 dat 位）
	uiIndex   uint16
	width     float64
}

// mleaderContextData CONTEXT_DATA（MLEADER_AnnotContext）：注意 DWG 流中
// leaders 数组先于标量组（spec 非 DXF 分支顺序）。
type mleaderContextData struct {
	numLeaders       uint32
	leaders          []mleaderNode
	scaleFactor      float64
	contentBase      point3
	textHeight       float64
	arrowSize        float64
	landingGap       float64
	textLeft         uint16
	textRight        uint16
	textAngletype    uint16
	textAlignment    uint16
	hasContentTxt    bool
	txt              mleaderTxtContent
	hasContentBlk    bool
	blk              mleaderBlkContent
	base             point3
	baseDir          point3
	baseVert         point3
	isNormalReversed bool
	// SINCE R_2010b
	textTop    uint16
	textBottom uint16
}

// entMLeader 多重引线实体。
type entMLeader struct {
	baseEntity
	hasVersion   bool // SINCE R_2010b 才读 class_version
	classVersion uint16
	ctx          mleaderContextData
	// 主体尾段（spec 顺序）
	mleaderStyle    uint64 // H 340（handle 流）
	flags           uint32 // BLx 90（override 掩码）
	mleaderType     uint16 // BS 170；gold 的 type 键即该值（覆盖顶层类型码）
	lineColor       mleaderCMC
	lineLtype       uint64 // H 341（handle 流）
	lineLinewt      int32  // BLd
	hasLanding      bool
	hasDogleg       bool
	landingDist     float64
	arrowHandle     uint64 // H0 342（handle 流）
	arrowSize       float64
	styleContent    uint16
	textStyle       uint64 // H 343（handle 流）
	textLeft        uint16
	textRight       uint16
	textAngletype   uint16
	textAlignment   uint16
	textColor       mleaderCMC
	hasTextFrame    bool
	blockStyle      uint64 // H0 344（handle 流）
	blockColor      mleaderCMC
	blockScale      point3
	blockRotation   float64
	styleAttachment uint16
	isAnnotative    bool
	// VERSIONS(R_14, R_2007) 段
	arrowheads    []mleaderArrowhead
	blocklabels   []mleaderBlockLabel
	isNegTextdir  bool
	ipeAlignment  uint16
	justification uint16
	scaleFactor   float64
	// SINCE R_2010b / R_2013b
	attachDir      uint16
	attachTop      uint16
	attachBottom   uint16
	isTextExtended bool
}

// readMLeaderCMC 读颜色字段（对齐 LibreDWG bit_read_CMC 的版本分支）：
// R2004+ 为 BS index + BL rgb + RC flag（flag>=4 非法清零；method 越界
// 修正为 0xc2；index 按调色板反查覆盖），更早版本仅 BS 索引。
func readMLeaderCMC(r *bitstream.BitStream, ver dwgVersion) (mleaderCMC, error) {
	var c mleaderCMC
	idx, err := r.ReadBS()
	if err != nil {
		return c, err
	}
	c.index = idx
	if ver < verR2004 {
		return c, nil
	}
	c.isTrue = true
	rgb, err := r.ReadBL()
	if err != nil {
		return c, err
	}
	c.rgb = rgb
	flag, err := r.ReadRC()
	if err != nil {
		return c, err
	}
	// flag&1/&2 的 name/book_name 在 R2007+ 存于字符串流（dat 不占位），
	// 样本未覆盖该分支，暂不读取
	if flag >= 4 {
		flag = 0
	}
	c.flag = flag
	if method := rgb >> 24; method < 0xc0 || method > 0xc8 {
		c.rgb = 0xc2000000 | (rgb & 0xffffff)
	}
	c.index = uint16(dwgFindColorIndex(c.rgb))
	return c, nil
}

// readMLeader3BD 读 3BD 点。
func readMLeader3BD(r *bitstream.BitStream) (point3, error) {
	x, y, z, err := r.Read3BD()
	if err != nil {
		return point3{}, err
	}
	return point3{x, y, z}, nil
}

// mleaderStrArea R2007+ 字符串区一次性预读的串序列与游标。
type mleaderStrArea struct {
	strs []string
	idx  int
}

// next 按序取下一个字符串（不足时返回空串，保持主体位流继续）。
func (s *mleaderStrArea) next() string {
	if s.idx < len(s.strs) {
		v := s.strs[s.idx]
		s.idx++
		return v
	}
	return ""
}

// decodeMLeader MULTILEADER 主体：公共头之后按 spec 顺序解码。
func decodeMLeader(r *bitstream.BitStream, head *commonEntityHead, ver dwgVersion, codepage uint16) (any, error) {
	m := &entMLeader{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}}
	r2010 := ver >= verR2010
	r2013 := ver >= verR2013
	var strArea *mleaderStrArea
	if ver >= verR2007 {
		// 字符串区容量上界：default_text 1 条 + blocklabels 上界（计数
		// 未知，预读按非空即停；样本 blocklabels 恒 0）
		strArea = &mleaderStrArea{strs: readStringAreaStrings(r, head, 4)}
	}

	if r2010 {
		m.hasVersion = true
		cv, err := r.ReadBS()
		if err != nil {
			return nil, err
		}
		m.classVersion = cv
		// VALUEOUTOFBOUNDS(class_version, 10)：越界视为布局错位
		if m.classVersion > 10 {
			return nil, bitstream.ErrUnexpectedEOF
		}
	}
	if err := decodeMLeaderLeaders(r, m, ver, r2010); err != nil {
		return nil, err
	}
	if err := decodeMLeaderContext(r, m, ver, codepage, strArea); err != nil {
		return nil, err
	}
	if r2010 {
		v, err := r.ReadBS()
		if err != nil {
			return nil, err
		}
		m.ctx.textTop = v
		if v, err = r.ReadBS(); err != nil {
			return nil, err
		}
		m.ctx.textBottom = v
	}

	var err error
	if m.flags, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if m.mleaderType, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if m.lineColor, err = readMLeaderCMC(r, ver); err != nil {
		return nil, err
	}
	if lw, e := r.ReadBL(); e != nil {
		return nil, e
	} else {
		m.lineLinewt = int32(lw)
	}
	if b, e := r.ReadB(); e != nil {
		return nil, e
	} else {
		m.hasLanding = b == 1
	}
	if b, e := r.ReadB(); e != nil {
		return nil, e
	} else {
		m.hasDogleg = b == 1
	}
	if m.landingDist, err = r.ReadBD(); err != nil {
		return nil, err
	}
	// NaN 防御（spec DECODER bit_isnan → 置 0）：防止垃圾位流污染导出
	if math.IsNaN(m.landingDist) {
		m.landingDist = 0
	}
	if m.arrowSize, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if m.styleContent, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if m.textLeft, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if m.textRight, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if m.textAngletype, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if m.textAlignment, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if m.textColor, err = readMLeaderCMC(r, ver); err != nil {
		return nil, err
	}
	if b, e := r.ReadB(); e != nil {
		return nil, e
	} else {
		m.hasTextFrame = b == 1
	}
	if m.blockColor, err = readMLeaderCMC(r, ver); err != nil {
		return nil, err
	}
	if m.blockScale, err = readMLeader3BD(r); err != nil {
		return nil, err
	}
	if m.blockRotation, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if m.styleAttachment, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if b, e := r.ReadB(); e != nil {
		return nil, e
	} else {
		m.isAnnotative = b == 1
	}

	// VERSIONS(R_14, R_2007)：箭头/块标签数组（R13 无该段，R2010b+ 移除）
	if ver == verR14 || ver == verR2000 || ver == verR2004 || ver == verR2007 {
		numArrowheads, e := r.ReadBL()
		if e != nil {
			return nil, e
		}
		if numArrowheads > 5000 {
			return nil, bitstream.ErrUnexpectedEOF
		}
		for i := uint32(0); i < numArrowheads; i++ {
			var ah mleaderArrowhead
			if b, e := r.ReadB(); e != nil {
				return nil, e
			} else {
				ah.isDefault = b == 1
			}
			m.arrowheads = append(m.arrowheads, ah)
		}
		numLabels, e := r.ReadBL()
		if e != nil {
			return nil, e
		}
		if numLabels > 5000 {
			return nil, bitstream.ErrUnexpectedEOF
		}
		for i := uint32(0); i < numLabels; i++ {
			var bl mleaderBlockLabel
			if ver >= verR2007 {
				bl.labelText = strArea.next() // 字符串区，dat 不占位
			} else {
				if bl.labelText, e = r.ReadTV(codepage); e != nil {
					return nil, e
				}
			}
			if bl.uiIndex, e = r.ReadBS(); e != nil {
				return nil, e
			}
			if bl.width, e = r.ReadBD(); e != nil {
				return nil, e
			}
			m.blocklabels = append(m.blocklabels, bl)
		}
		if b, e := r.ReadB(); e != nil {
			return nil, e
		} else {
			m.isNegTextdir = b == 1
		}
		if m.ipeAlignment, err = r.ReadBS(); err != nil {
			return nil, err
		}
		if m.justification, err = r.ReadBS(); err != nil {
			return nil, err
		}
		if m.scaleFactor, err = r.ReadBD(); err != nil {
			return nil, err
		}
	}
	if r2010 {
		if m.attachDir, err = r.ReadBS(); err != nil {
			return nil, err
		}
		if m.attachTop, err = r.ReadBS(); err != nil {
			return nil, err
		}
		if m.attachBottom, err = r.ReadBS(); err != nil {
			return nil, err
		}
	}
	if r2013 {
		if b, e := r.ReadB(); e != nil {
			return nil, e
		} else {
			m.isTextExtended = b == 1
		}
	}

	// 尾部 handle 流：owner/layer（公共）之后按 spec 的 FIELD_HANDLE
	// 出现序读取（dat 不占位）。pre-R2010 与 R2010b+ 顺序不同：
	//   pre-R2010：content style/blockTable → mleaderstyle → line_ltype →
	//     arrow_handle → text_style → block_style → arrowheads[] →
	//     blocklabels[]
	//   R2010b+：lline ltype/arrow → content style/blockTable →
	//     mleaderstyle → arrow_handle → text_style → block_style → line_ltype
	// 句柄失败不阻断（审计不比对句柄键）。
	savedByte, savedBit := r.Cursor()
	r.SetBitPos(head.objSizeBit)
	if owner, layer, herr := parseCommonEntityHandles(r, head); herr == nil {
		m.owner, m.layer = owner, layer
		ok := true
		if !r2010 {
			if m.ctx.hasContentTxt {
				m.ctx.txt.styleHandle, _ = objrec.ReadHandleReference(r, head.handle)
			} else if m.ctx.hasContentBlk {
				m.ctx.blk.blockTable, _ = objrec.ReadHandleReference(r, head.handle)
			}
			// spec FIELD_HANDLE 序：mleaderstyle → line_ltype →
			// arrow_handle → text_style → block_style → arrowheads[] →
			// blocklabels[]
			if m.mleaderStyle, herr = objrec.ReadHandleReference(r, head.handle); herr != nil {
				ok = false
			} else if m.lineLtype, herr = objrec.ReadHandleReference(r, head.handle); herr != nil {
				ok = false
			} else if m.arrowHandle, herr = objrec.ReadHandleReference(r, head.handle); herr != nil {
				ok = false
			} else if m.textStyle, herr = objrec.ReadHandleReference(r, head.handle); herr != nil {
				ok = false
			} else if m.blockStyle, herr = objrec.ReadHandleReference(r, head.handle); herr != nil {
				ok = false
			}
			if ok {
				for i := range m.arrowheads {
					if m.arrowheads[i].arrowhead, herr = objrec.ReadHandleReference(r, head.handle); herr != nil {
						ok = false
						break
					}
				}
			}
			if ok {
				for i := range m.blocklabels {
					if m.blocklabels[i].attdef, herr = objrec.ReadHandleReference(r, head.handle); herr != nil {
						ok = false
						break
					}
				}
			}
			r.Restore(savedByte, savedBit)
			return m, nil
		}
		for i := range m.ctx.leaders {
			for j := range m.ctx.leaders[i].lines {
				if r2010 {
					if m.ctx.leaders[i].lines[j].ltype, herr = objrec.ReadHandleReference(r, head.handle); herr != nil {
						ok = false
						break
					}
					if m.ctx.leaders[i].lines[j].arrowHandle, herr = objrec.ReadHandleReference(r, head.handle); herr != nil {
						ok = false
						break
					}
				}
			}
			if !ok {
				break
			}
		}
		if ok && m.ctx.hasContentTxt {
			m.ctx.txt.styleHandle, _ = objrec.ReadHandleReference(r, head.handle)
		} else if ok && m.ctx.hasContentBlk {
			m.ctx.blk.blockTable, _ = objrec.ReadHandleReference(r, head.handle)
		}
		if ok {
			if m.mleaderStyle, herr = objrec.ReadHandleReference(r, head.handle); herr == nil {
				// spec FIELD_HANDLE 序：mleaderstyle → line_ltype →
				// arrow_handle → text_style → block_style（R2010b+ 无
				// arrowheads/blocklabels 句柄）
				if m.lineLtype, herr = objrec.ReadHandleReference(r, head.handle); herr == nil {
					if m.arrowHandle, herr = objrec.ReadHandleReference(r, head.handle); herr == nil {
						if m.textStyle, herr = objrec.ReadHandleReference(r, head.handle); herr == nil {
							m.blockStyle, herr = objrec.ReadHandleReference(r, head.handle)
						}
					}
				}
			}
		}
	}
	r.Restore(savedByte, savedBit)
	return m, nil
}

// decodeMLeaderLeaders ctx.num_leaders + 三层嵌套 REPEAT
// （leaders→lines→breaks/points），数量越界视为布局错位。
func decodeMLeaderLeaders(r *bitstream.BitStream, m *entMLeader, ver dwgVersion, r2010 bool) error {
	numLeaders, err := r.ReadBL()
	if err != nil {
		return err
	}
	if numLeaders > 5000 {
		return bitstream.ErrUnexpectedEOF
	}
	m.ctx.numLeaders = numLeaders
	for i := uint32(0); i < numLeaders; i++ {
		var node mleaderNode
		var b uint8
		if b, err = r.ReadB(); err != nil {
			return err
		}
		node.hasLastLeaderLinePoint = b == 1
		if b, err = r.ReadB(); err != nil {
			return err
		}
		node.hasDogleg = b == 1
		if node.hasLastLeaderLinePoint {
			if node.lastLeaderLinePoint, err = readMLeader3BD(r); err != nil {
				return err
			}
		}
		if node.hasDogleg {
			if node.doglegVector, err = readMLeader3BD(r); err != nil {
				return err
			}
		}
		if node.numBreaks, err = r.ReadBL(); err != nil {
			return err
		}
		if node.numBreaks > 5000 {
			return bitstream.ErrUnexpectedEOF
		}
		for j := uint32(0); j < node.numBreaks; j++ {
			var brk mleaderBreak
			if brk.start, err = readMLeader3BD(r); err != nil {
				return err
			}
			if brk.end, err = readMLeader3BD(r); err != nil {
				return err
			}
			node.breaks = append(node.breaks, brk)
		}
		if node.branchIndex, err = r.ReadBL(); err != nil {
			return err
		}
		if node.doglegLength, err = r.ReadBD(); err != nil {
			return err
		}
		if node.numLines, err = r.ReadBL(); err != nil {
			return err
		}
		if node.numLines > 5000 {
			return bitstream.ErrUnexpectedEOF
		}
		for j := uint32(0); j < node.numLines; j++ {
			var line mleaderLine
			var numPoints uint32
			if numPoints, err = r.ReadBL(); err != nil {
				return err
			}
			if numPoints > 5000 {
				return bitstream.ErrUnexpectedEOF
			}
			for k := uint32(0); k < numPoints; k++ {
				var pt point3
				if pt, err = readMLeader3BD(r); err != nil {
					return err
				}
				line.points = append(line.points, pt)
			}
			if line.numBreaks, err = r.ReadBL(); err != nil {
				return err
			}
			if line.numBreaks > 5000 {
				return bitstream.ErrUnexpectedEOF
			}
			for k := uint32(0); k < line.numBreaks; k++ {
				var brk mleaderBreak
				if brk.start, err = readMLeader3BD(r); err != nil {
					return err
				}
				if brk.end, err = readMLeader3BD(r); err != nil {
					return err
				}
				line.breaks = append(line.breaks, brk)
			}
			if line.lineIndex, err = r.ReadBL(); err != nil {
				return err
			}
			if r2010 {
				if line.mleaderType, err = r.ReadBS(); err != nil {
					return err
				}
				if line.color, err = readMLeaderCMC(r, ver); err != nil {
					return err
				}
				if lw, e := r.ReadBL(); e != nil {
					return e
				} else {
					line.linewt = int32(lw)
				}
				if line.arrowSize, err = r.ReadBD(); err != nil {
					return err
				}
				if line.flags, err = r.ReadBL(); err != nil {
					return err
				}
			}
			node.lines = append(node.lines, line)
		}
		if r2010 {
			if node.attachDir, err = r.ReadBS(); err != nil {
				return err
			}
		}
		m.ctx.leaders = append(m.ctx.leaders, node)
	}
	return nil
}

// decodeMLeaderContext MLEADER_CONTEXT_DATA_fields（非 DXF 顺序：leaders
// 之后）：标量组 → txt/blk 内容联合 → base 三点 + is_normal_reversed。
func decodeMLeaderContext(r *bitstream.BitStream, m *entMLeader, ver dwgVersion, codepage uint16, strArea *mleaderStrArea) error {
	c := &m.ctx
	var err error
	if c.scaleFactor, err = r.ReadBD(); err != nil {
		return err
	}
	if c.contentBase, err = readMLeader3BD(r); err != nil {
		return err
	}
	if c.textHeight, err = r.ReadBD(); err != nil {
		return err
	}
	if c.arrowSize, err = r.ReadBD(); err != nil {
		return err
	}
	if c.landingGap, err = r.ReadBD(); err != nil {
		return err
	}
	if c.textLeft, err = r.ReadBS(); err != nil {
		return err
	}
	if c.textRight, err = r.ReadBS(); err != nil {
		return err
	}
	if c.textAngletype, err = r.ReadBS(); err != nil {
		return err
	}
	if c.textAlignment, err = r.ReadBS(); err != nil {
		return err
	}
	var b uint8
	if b, err = r.ReadB(); err != nil {
		return err
	}
	c.hasContentTxt = b == 1
	if c.hasContentTxt {
		t := &c.txt
		// DECODER 语义：txt 分支 contentType=2；default_text R2007+ 走
		// 字符串区（dat 不占位）
		if ver >= verR2007 && strArea != nil {
			t.defaultText = strArea.next()
		} else {
			if t.defaultText, err = r.ReadTV(codepage); err != nil {
				return err
			}
		}
		if t.normal, err = readMLeader3BD(r); err != nil {
			return err
		}
		// style H 340 在 handle 流（dat 不占位）
		if t.location, err = readMLeader3BD(r); err != nil {
			return err
		}
		if t.direction, err = readMLeader3BD(r); err != nil {
			return err
		}
		if t.rotation, err = r.ReadBD(); err != nil {
			return err
		}
		if t.width, err = r.ReadBD(); err != nil {
			return err
		}
		if t.height, err = r.ReadBD(); err != nil {
			return err
		}
		if t.lineSpacingFactor, err = r.ReadBD(); err != nil {
			return err
		}
		if t.lineSpacingStyle, err = r.ReadBS(); err != nil {
			return err
		}
		if t.color, err = readMLeaderCMC(r, ver); err != nil {
			return err
		}
		if t.alignment, err = r.ReadBS(); err != nil {
			return err
		}
		if t.flow, err = r.ReadBS(); err != nil {
			return err
		}
		if t.bgColor, err = readMLeaderCMC(r, ver); err != nil {
			return err
		}
		if t.bgScale, err = r.ReadBD(); err != nil {
			return err
		}
		if t.bgTransparency, err = r.ReadBL(); err != nil {
			return err
		}
		if b, err = r.ReadB(); err != nil {
			return err
		}
		t.isBgFill = b == 1
		if b, err = r.ReadB(); err != nil {
			return err
		}
		t.isBgMaskFill = b == 1
		if t.colType, err = r.ReadBS(); err != nil {
			return err
		}
		if b, err = r.ReadB(); err != nil {
			return err
		}
		t.isHeightAuto = b == 1
		if t.colWidth, err = r.ReadBD(); err != nil {
			return err
		}
		if t.colGutter, err = r.ReadBD(); err != nil {
			return err
		}
		if b, err = r.ReadB(); err != nil {
			return err
		}
		t.isColFlowReversed = b == 1
		if t.numColSizes, err = r.ReadBL(); err != nil {
			return err
		}
		if t.numColSizes > 5000 {
			return bitstream.ErrUnexpectedEOF
		}
		for i := uint32(0); i < t.numColSizes; i++ {
			var v float64
			if v, err = r.ReadBD(); err != nil {
				return err
			}
			t.colSizes = append(t.colSizes, v)
		}
		if b, err = r.ReadB(); err != nil {
			return err
		}
		t.wordBreak = b == 1
		if b, err = r.ReadB(); err != nil {
			return err
		}
		t.unknown = b == 1
	} else {
		if b, err = r.ReadB(); err != nil {
			return err
		}
		c.hasContentBlk = b == 1
		if c.hasContentBlk {
			k := &c.blk
			// block_table H 341 在 handle 流（dat 不占位）
			if k.normal, err = readMLeader3BD(r); err != nil {
				return err
			}
			if k.location, err = readMLeader3BD(r); err != nil {
				return err
			}
			if k.scale, err = readMLeader3BD(r); err != nil {
				return err
			}
			if k.rotation, err = r.ReadBD(); err != nil {
				return err
			}
			if k.color, err = readMLeaderCMC(r, ver); err != nil {
				return err
			}
			for i := 0; i < 16; i++ {
				if k.transform[i], err = r.ReadBD(); err != nil {
					return err
				}
			}
		}
	}
	if c.base, err = readMLeader3BD(r); err != nil {
		return err
	}
	if c.baseDir, err = readMLeader3BD(r); err != nil {
		return err
	}
	if c.baseVert, err = readMLeader3BD(r); err != nil {
		return err
	}
	if b, err = r.ReadB(); err != nil {
		return err
	}
	c.isNormalReversed = b == 1
	return nil
}
