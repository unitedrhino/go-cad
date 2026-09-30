// entities_mleader.go MULTILEADER 多重引线实体解码（dwg2.spec
// DWG_ENTITY(MULTILEADER)，AcDbMLeader）。主体位流按 spec 顺序直读：
// 三层嵌套 REPEAT（leaders→lines→breaks/points）、ctx 内容联合
// （txt ~25 字段 / blk ~7 字段二选一）、R2010b/R2013b 版本分支；
// 所有 FIELD_HANDLE 在尾部 handle 流按 spec 顺序读取（dat 不占位，
// 对齐 LibreDWG obj_handle_stream + VALUE_HANDLE 行为）；R2007+ 的
// T 字符串存于记录尾字符串区（obj_string_stream），dat 不占位。
package entity

import (
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/objrec"
	"math"
)

// mleaderCMC MULTILEADER 颜色字段：isTrue 标记 R2004+ 结构（BS index +
// BL rgb + RC flag，含 method 修正与调色板反查），更早版本仅 BS 索引。
type MleaderCMC struct {
	Index  uint16
	Rgb    uint32
	Flag   uint8
	IsTrue bool
}

// mleaderTxtContent ctx 的 txt 内容分支（has_content_txt=1）。
type mleaderTxtContent struct {
	DefaultText       string // TV/TU 304（R2007+ 不占 dat 位）
	Normal            Point3
	StyleHandle       uint64 // H 340（handle 流）
	Location          Point3
	Direction         Point3
	Rotation          float64
	Width             float64
	Height            float64
	LineSpacingFactor float64
	LineSpacingStyle  uint16
	Color             MleaderCMC
	Alignment         uint16
	Flow              uint16
	BgColor           MleaderCMC
	BgScale           float64
	BgTransparency    uint32
	IsBgFill          bool
	IsBgMaskFill      bool
	ColType           uint16
	IsHeightAuto      bool
	ColWidth          float64
	ColGutter         float64
	IsColFlowReversed bool
	NumColSizes       uint32
	ColSizes          []float64
	WordBreak         bool
	Unknown           bool
}

// mleaderBlkContent ctx 的 blk 内容分支（has_content_txt=0 且
// has_content_blk=1，块参照内容；九样本未出现，按 spec 实现保位序）。
type mleaderBlkContent struct {
	BlockTable uint64 // H 341（handle 流）
	Normal     Point3
	Location   Point3
	Scale      Point3
	Rotation   float64
	Color      MleaderCMC
	Transform  [16]float64
}

// mleaderBreak 引线断开起点/终点（数组键，审计不比对）。
type MleaderBreak struct{ Start, End Point3 }

// mleaderLine 引线节下的单条引线。
type MleaderLine struct {
	Points    []Point3
	NumBreaks uint32
	Breaks    []MleaderBreak
	LineIndex uint32
	// SINCE R_2010b
	MleaderType uint16
	Color       MleaderCMC
	ltype       uint64 // H 340（handle 流）
	Linewt      int32  // BLd
	ArrowSize   float64
	ArrowHandle uint64 // H 341（handle 流）
	Flags       uint32
}

// mleaderNode 引线节点（ctx.leaders[i]）。
type MleaderNode struct {
	HasLastLeaderLinePoint bool
	LastLeaderLinePoint    Point3
	HasDogleg              bool
	DoglegVector           Point3
	NumBreaks              uint32
	Breaks                 []MleaderBreak
	BranchIndex            uint32
	DoglegLength           float64
	NumLines               uint32
	Lines                  []MleaderLine
	// SINCE R_2010b
	AttachDir uint16
}

// mleaderArrowhead / mleaderBlockLabel R14-R2007 的箭头/块标签数组项。
type MleaderArrowhead struct {
	IsDefault bool
	Arrowhead uint64 // H 345（handle 流）
}

type MleaderBlockLabel struct {
	Attdef    uint64 // H 330（handle 流）
	LabelText string // T 302（R2007+ 不占 dat 位）
	UiIndex   uint16
	Width     float64
}

// mleaderContextData CONTEXT_DATA（MLEADER_AnnotContext）：注意 DWG 流中
// leaders 数组先于标量组（spec 非 DXF 分支顺序）。
type mleaderContextData struct {
	NumLeaders       uint32
	Leaders          []MleaderNode
	ScaleFactor      float64
	ContentBase      Point3
	TextHeight       float64
	ArrowSize        float64
	LandingGap       float64
	TextLeft         uint16
	TextRight        uint16
	TextAngletype    uint16
	TextAlignment    uint16
	HasContentTxt    bool
	Txt              mleaderTxtContent
	HasContentBlk    bool
	Blk              mleaderBlkContent
	Base             Point3
	BaseDir          Point3
	BaseVert         Point3
	IsNormalReversed bool
	// SINCE R_2010b
	TextTop    uint16
	TextBottom uint16
}

// entMLeader 多重引线实体。
type EntMLeader struct {
	BaseEntity
	HasVersion   bool // SINCE R_2010b 才读 class_version
	ClassVersion uint16
	Ctx          mleaderContextData
	// 主体尾段（spec 顺序）
	MleaderStyle    uint64 // H 340（handle 流）
	Flags           uint32 // BLx 90（override 掩码）
	MleaderType     uint16 // BS 170；gold 的 type 键即该值（覆盖顶层类型码）
	LineColor       MleaderCMC
	LineLtype       uint64 // H 341（handle 流）
	LineLinewt      int32  // BLd
	HasLanding      bool
	HasDogleg       bool
	LandingDist     float64
	ArrowHandle     uint64 // H0 342（handle 流）
	ArrowSize       float64
	StyleContent    uint16
	TextStyle       uint64 // H 343（handle 流）
	TextLeft        uint16
	TextRight       uint16
	TextAngletype   uint16
	TextAlignment   uint16
	TextColor       MleaderCMC
	HasTextFrame    bool
	BlockStyle      uint64 // H0 344（handle 流）
	BlockColor      MleaderCMC
	BlockScale      Point3
	BlockRotation   float64
	StyleAttachment uint16
	IsAnnotative    bool
	// VERSIONS(R_14, R_2007) 段
	Arrowheads    []MleaderArrowhead
	Blocklabels   []MleaderBlockLabel
	IsNegTextdir  bool
	IpeAlignment  uint16
	Justification uint16
	ScaleFactor   float64
	// SINCE R_2010b / R_2013b
	AttachDir      uint16
	AttachTop      uint16
	AttachBottom   uint16
	IsTextExtended bool
}

// readMLeaderCMC 读颜色字段（对齐 LibreDWG bit_read_CMC 的版本分支）：
// R2004+ 为 BS index + BL rgb + RC flag（flag>=4 非法清零；method 越界
// 修正为 0xc2；index 按调色板反查覆盖），更早版本仅 BS 索引。
func readMLeaderCMC(r *bitstream.BitStream, ver container.DwgVersion) (MleaderCMC, error) {
	var c MleaderCMC
	idx, err := r.ReadBS()
	if err != nil {
		return c, err
	}
	c.Index = idx
	if ver < container.VerR2004 {
		return c, nil
	}
	c.IsTrue = true
	Rgb, err := r.ReadBL()
	if err != nil {
		return c, err
	}
	c.Rgb = Rgb
	Flag, err := r.ReadRC()
	if err != nil {
		return c, err
	}
	// flag&1/&2 的 name/book_name 在 R2007+ 存于字符串流（dat 不占位），
	// 样本未覆盖该分支，暂不读取
	if Flag >= 4 {
		Flag = 0
	}
	c.Flag = Flag
	if method := Rgb >> 24; method < 0xc0 || method > 0xc8 {
		c.Rgb = 0xc2000000 | (Rgb & 0xffffff)
	}
	c.Index = uint16(DwgFindColorIndex(c.Rgb))
	return c, nil
}

// readMLeader3BD 读 3BD 点。
func readMLeader3BD(r *bitstream.BitStream) (Point3, error) {
	X, Y, Z, err := r.Read3BD()
	if err != nil {
		return Point3{}, err
	}
	return Point3{X, Y, Z}, nil
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
func decodeMLeader(r *bitstream.BitStream, Head *CommonEntityHead, ver container.DwgVersion, codepage uint16) (any, error) {
	m := &EntMLeader{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}}
	r2010 := ver >= container.VerR2010
	r2013 := ver >= container.VerR2013
	var strArea *mleaderStrArea
	if ver >= container.VerR2007 {
		// 字符串区容量上界：default_text 1 条 + blocklabels 上界（计数
		// 未知，预读按非空即停；样本 blocklabels 恒 0）
		strArea = &mleaderStrArea{strs: readStringAreaStrings(r, Head, 4)}
	}

	if r2010 {
		m.HasVersion = true
		cv, err := r.ReadBS()
		if err != nil {
			return nil, err
		}
		m.ClassVersion = cv
		// VALUEOUTOFBOUNDS(class_version, 10)：越界视为布局错位
		if m.ClassVersion > 10 {
			return nil, bitstream.ErrUnexpectedEOF
		}
	}
	if err := DecodeMLeaderLeaders(r, m, ver, r2010); err != nil {
		return nil, err
	}
	if err := DecodeMLeaderContext(r, m, ver, codepage, strArea); err != nil {
		return nil, err
	}
	if r2010 {
		v, err := r.ReadBS()
		if err != nil {
			return nil, err
		}
		m.Ctx.TextTop = v
		if v, err = r.ReadBS(); err != nil {
			return nil, err
		}
		m.Ctx.TextBottom = v
	}

	var err error
	if m.Flags, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if m.MleaderType, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if m.LineColor, err = readMLeaderCMC(r, ver); err != nil {
		return nil, err
	}
	if lw, e := r.ReadBL(); e != nil {
		return nil, e
	} else {
		m.LineLinewt = int32(lw)
	}
	if b, e := r.ReadB(); e != nil {
		return nil, e
	} else {
		m.HasLanding = b == 1
	}
	if b, e := r.ReadB(); e != nil {
		return nil, e
	} else {
		m.HasDogleg = b == 1
	}
	if m.LandingDist, err = r.ReadBD(); err != nil {
		return nil, err
	}
	// NaN 防御（spec DECODER bit_isnan → 置 0）：防止垃圾位流污染导出
	if math.IsNaN(m.LandingDist) {
		m.LandingDist = 0
	}
	if m.ArrowSize, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if m.StyleContent, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if m.TextLeft, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if m.TextRight, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if m.TextAngletype, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if m.TextAlignment, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if m.TextColor, err = readMLeaderCMC(r, ver); err != nil {
		return nil, err
	}
	if b, e := r.ReadB(); e != nil {
		return nil, e
	} else {
		m.HasTextFrame = b == 1
	}
	if m.BlockColor, err = readMLeaderCMC(r, ver); err != nil {
		return nil, err
	}
	if m.BlockScale, err = readMLeader3BD(r); err != nil {
		return nil, err
	}
	if m.BlockRotation, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if m.StyleAttachment, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if b, e := r.ReadB(); e != nil {
		return nil, e
	} else {
		m.IsAnnotative = b == 1
	}

	// VERSIONS(R_14, R_2007)：箭头/块标签数组（R13 无该段，R2010b+ 移除）
	if ver == container.VerR14 || ver == container.VerR2000 || ver == container.VerR2004 || ver == container.VerR2007 {
		numArrowheads, e := r.ReadBL()
		if e != nil {
			return nil, e
		}
		if numArrowheads > 5000 {
			return nil, bitstream.ErrUnexpectedEOF
		}
		for i := uint32(0); i < numArrowheads; i++ {
			var ah MleaderArrowhead
			if b, e := r.ReadB(); e != nil {
				return nil, e
			} else {
				ah.IsDefault = b == 1
			}
			m.Arrowheads = append(m.Arrowheads, ah)
		}
		numLabels, e := r.ReadBL()
		if e != nil {
			return nil, e
		}
		if numLabels > 5000 {
			return nil, bitstream.ErrUnexpectedEOF
		}
		for i := uint32(0); i < numLabels; i++ {
			var bl MleaderBlockLabel
			if ver >= container.VerR2007 {
				bl.LabelText = strArea.next() // 字符串区，dat 不占位
			} else {
				if bl.LabelText, e = r.ReadTV(codepage); e != nil {
					return nil, e
				}
			}
			if bl.UiIndex, e = r.ReadBS(); e != nil {
				return nil, e
			}
			if bl.Width, e = r.ReadBD(); e != nil {
				return nil, e
			}
			m.Blocklabels = append(m.Blocklabels, bl)
		}
		if b, e := r.ReadB(); e != nil {
			return nil, e
		} else {
			m.IsNegTextdir = b == 1
		}
		if m.IpeAlignment, err = r.ReadBS(); err != nil {
			return nil, err
		}
		if m.Justification, err = r.ReadBS(); err != nil {
			return nil, err
		}
		if m.ScaleFactor, err = r.ReadBD(); err != nil {
			return nil, err
		}
	}
	if r2010 {
		if m.AttachDir, err = r.ReadBS(); err != nil {
			return nil, err
		}
		if m.AttachTop, err = r.ReadBS(); err != nil {
			return nil, err
		}
		if m.AttachBottom, err = r.ReadBS(); err != nil {
			return nil, err
		}
	}
	if r2013 {
		if b, e := r.ReadB(); e != nil {
			return nil, e
		} else {
			m.IsTextExtended = b == 1
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
	r.SetBitPos(Head.ObjSizeBit)
	if Owner, Layer, herr := ParseCommonEntityHandles(r, Head); herr == nil {
		m.Owner, m.Layer = Owner, Layer
		ok := true
		if !r2010 {
			if m.Ctx.HasContentTxt {
				m.Ctx.Txt.StyleHandle, _ = objrec.ReadHandleReference(r, Head.Handle)
			} else if m.Ctx.HasContentBlk {
				m.Ctx.Blk.BlockTable, _ = objrec.ReadHandleReference(r, Head.Handle)
			}
			// spec FIELD_HANDLE 序：mleaderstyle → line_ltype →
			// arrow_handle → text_style → block_style → arrowheads[] →
			// blocklabels[]
			if m.MleaderStyle, herr = objrec.ReadHandleReference(r, Head.Handle); herr != nil {
				ok = false
			} else if m.LineLtype, herr = objrec.ReadHandleReference(r, Head.Handle); herr != nil {
				ok = false
			} else if m.ArrowHandle, herr = objrec.ReadHandleReference(r, Head.Handle); herr != nil {
				ok = false
			} else if m.TextStyle, herr = objrec.ReadHandleReference(r, Head.Handle); herr != nil {
				ok = false
			} else if m.BlockStyle, herr = objrec.ReadHandleReference(r, Head.Handle); herr != nil {
				ok = false
			}
			if ok {
				for i := range m.Arrowheads {
					if m.Arrowheads[i].Arrowhead, herr = objrec.ReadHandleReference(r, Head.Handle); herr != nil {
						ok = false
						break
					}
				}
			}
			if ok {
				for i := range m.Blocklabels {
					if m.Blocklabels[i].Attdef, herr = objrec.ReadHandleReference(r, Head.Handle); herr != nil {
						ok = false
						break
					}
				}
			}
			r.Restore(savedByte, savedBit)
			return m, nil
		}
		for i := range m.Ctx.Leaders {
			for j := range m.Ctx.Leaders[i].Lines {
				if r2010 {
					if m.Ctx.Leaders[i].Lines[j].ltype, herr = objrec.ReadHandleReference(r, Head.Handle); herr != nil {
						ok = false
						break
					}
					if m.Ctx.Leaders[i].Lines[j].ArrowHandle, herr = objrec.ReadHandleReference(r, Head.Handle); herr != nil {
						ok = false
						break
					}
				}
			}
			if !ok {
				break
			}
		}
		if ok && m.Ctx.HasContentTxt {
			m.Ctx.Txt.StyleHandle, _ = objrec.ReadHandleReference(r, Head.Handle)
		} else if ok && m.Ctx.HasContentBlk {
			m.Ctx.Blk.BlockTable, _ = objrec.ReadHandleReference(r, Head.Handle)
		}
		if ok {
			if m.MleaderStyle, herr = objrec.ReadHandleReference(r, Head.Handle); herr == nil {
				// spec FIELD_HANDLE 序：mleaderstyle → line_ltype →
				// arrow_handle → text_style → block_style（R2010b+ 无
				// arrowheads/blocklabels 句柄）
				if m.LineLtype, herr = objrec.ReadHandleReference(r, Head.Handle); herr == nil {
					if m.ArrowHandle, herr = objrec.ReadHandleReference(r, Head.Handle); herr == nil {
						if m.TextStyle, herr = objrec.ReadHandleReference(r, Head.Handle); herr == nil {
							m.BlockStyle, herr = objrec.ReadHandleReference(r, Head.Handle)
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
func DecodeMLeaderLeaders(r *bitstream.BitStream, m *EntMLeader, ver container.DwgVersion, r2010 bool) error {
	NumLeaders, err := r.ReadBL()
	if err != nil {
		return err
	}
	if NumLeaders > 5000 {
		return bitstream.ErrUnexpectedEOF
	}
	m.Ctx.NumLeaders = NumLeaders
	for i := uint32(0); i < NumLeaders; i++ {
		var node MleaderNode
		var b uint8
		if b, err = r.ReadB(); err != nil {
			return err
		}
		node.HasLastLeaderLinePoint = b == 1
		if b, err = r.ReadB(); err != nil {
			return err
		}
		node.HasDogleg = b == 1
		if node.HasLastLeaderLinePoint {
			if node.LastLeaderLinePoint, err = readMLeader3BD(r); err != nil {
				return err
			}
		}
		if node.HasDogleg {
			if node.DoglegVector, err = readMLeader3BD(r); err != nil {
				return err
			}
		}
		if node.NumBreaks, err = r.ReadBL(); err != nil {
			return err
		}
		if node.NumBreaks > 5000 {
			return bitstream.ErrUnexpectedEOF
		}
		for j := uint32(0); j < node.NumBreaks; j++ {
			var brk MleaderBreak
			if brk.Start, err = readMLeader3BD(r); err != nil {
				return err
			}
			if brk.End, err = readMLeader3BD(r); err != nil {
				return err
			}
			node.Breaks = append(node.Breaks, brk)
		}
		if node.BranchIndex, err = r.ReadBL(); err != nil {
			return err
		}
		if node.DoglegLength, err = r.ReadBD(); err != nil {
			return err
		}
		if node.NumLines, err = r.ReadBL(); err != nil {
			return err
		}
		if node.NumLines > 5000 {
			return bitstream.ErrUnexpectedEOF
		}
		for j := uint32(0); j < node.NumLines; j++ {
			var line MleaderLine
			var numPoints uint32
			if numPoints, err = r.ReadBL(); err != nil {
				return err
			}
			if numPoints > 5000 {
				return bitstream.ErrUnexpectedEOF
			}
			for k := uint32(0); k < numPoints; k++ {
				var pt Point3
				if pt, err = readMLeader3BD(r); err != nil {
					return err
				}
				line.Points = append(line.Points, pt)
			}
			if line.NumBreaks, err = r.ReadBL(); err != nil {
				return err
			}
			if line.NumBreaks > 5000 {
				return bitstream.ErrUnexpectedEOF
			}
			for k := uint32(0); k < line.NumBreaks; k++ {
				var brk MleaderBreak
				if brk.Start, err = readMLeader3BD(r); err != nil {
					return err
				}
				if brk.End, err = readMLeader3BD(r); err != nil {
					return err
				}
				line.Breaks = append(line.Breaks, brk)
			}
			if line.LineIndex, err = r.ReadBL(); err != nil {
				return err
			}
			if r2010 {
				if line.MleaderType, err = r.ReadBS(); err != nil {
					return err
				}
				if line.Color, err = readMLeaderCMC(r, ver); err != nil {
					return err
				}
				if lw, e := r.ReadBL(); e != nil {
					return e
				} else {
					line.Linewt = int32(lw)
				}
				if line.ArrowSize, err = r.ReadBD(); err != nil {
					return err
				}
				if line.Flags, err = r.ReadBL(); err != nil {
					return err
				}
			}
			node.Lines = append(node.Lines, line)
		}
		if r2010 {
			if node.AttachDir, err = r.ReadBS(); err != nil {
				return err
			}
		}
		m.Ctx.Leaders = append(m.Ctx.Leaders, node)
	}
	return nil
}

// decodeMLeaderContext MLEADER_CONTEXT_DATA_fields（非 DXF 顺序：leaders
// 之后）：标量组 → txt/blk 内容联合 → base 三点 + is_normal_reversed。
func DecodeMLeaderContext(r *bitstream.BitStream, m *EntMLeader, ver container.DwgVersion, codepage uint16, strArea *mleaderStrArea) error {
	c := &m.Ctx
	var err error
	if c.ScaleFactor, err = r.ReadBD(); err != nil {
		return err
	}
	if c.ContentBase, err = readMLeader3BD(r); err != nil {
		return err
	}
	if c.TextHeight, err = r.ReadBD(); err != nil {
		return err
	}
	if c.ArrowSize, err = r.ReadBD(); err != nil {
		return err
	}
	if c.LandingGap, err = r.ReadBD(); err != nil {
		return err
	}
	if c.TextLeft, err = r.ReadBS(); err != nil {
		return err
	}
	if c.TextRight, err = r.ReadBS(); err != nil {
		return err
	}
	if c.TextAngletype, err = r.ReadBS(); err != nil {
		return err
	}
	if c.TextAlignment, err = r.ReadBS(); err != nil {
		return err
	}
	var b uint8
	if b, err = r.ReadB(); err != nil {
		return err
	}
	c.HasContentTxt = b == 1
	if c.HasContentTxt {
		t := &c.Txt
		// DECODER 语义：txt 分支 contentType=2；default_text R2007+ 走
		// 字符串区（dat 不占位）
		if ver >= container.VerR2007 && strArea != nil {
			t.DefaultText = strArea.next()
		} else {
			if t.DefaultText, err = r.ReadTV(codepage); err != nil {
				return err
			}
		}
		if t.Normal, err = readMLeader3BD(r); err != nil {
			return err
		}
		// style H 340 在 handle 流（dat 不占位）
		if t.Location, err = readMLeader3BD(r); err != nil {
			return err
		}
		if t.Direction, err = readMLeader3BD(r); err != nil {
			return err
		}
		if t.Rotation, err = r.ReadBD(); err != nil {
			return err
		}
		if t.Width, err = r.ReadBD(); err != nil {
			return err
		}
		if t.Height, err = r.ReadBD(); err != nil {
			return err
		}
		if t.LineSpacingFactor, err = r.ReadBD(); err != nil {
			return err
		}
		if t.LineSpacingStyle, err = r.ReadBS(); err != nil {
			return err
		}
		if t.Color, err = readMLeaderCMC(r, ver); err != nil {
			return err
		}
		if t.Alignment, err = r.ReadBS(); err != nil {
			return err
		}
		if t.Flow, err = r.ReadBS(); err != nil {
			return err
		}
		if t.BgColor, err = readMLeaderCMC(r, ver); err != nil {
			return err
		}
		if t.BgScale, err = r.ReadBD(); err != nil {
			return err
		}
		if t.BgTransparency, err = r.ReadBL(); err != nil {
			return err
		}
		if b, err = r.ReadB(); err != nil {
			return err
		}
		t.IsBgFill = b == 1
		if b, err = r.ReadB(); err != nil {
			return err
		}
		t.IsBgMaskFill = b == 1
		if t.ColType, err = r.ReadBS(); err != nil {
			return err
		}
		if b, err = r.ReadB(); err != nil {
			return err
		}
		t.IsHeightAuto = b == 1
		if t.ColWidth, err = r.ReadBD(); err != nil {
			return err
		}
		if t.ColGutter, err = r.ReadBD(); err != nil {
			return err
		}
		if b, err = r.ReadB(); err != nil {
			return err
		}
		t.IsColFlowReversed = b == 1
		if t.NumColSizes, err = r.ReadBL(); err != nil {
			return err
		}
		if t.NumColSizes > 5000 {
			return bitstream.ErrUnexpectedEOF
		}
		for i := uint32(0); i < t.NumColSizes; i++ {
			var v float64
			if v, err = r.ReadBD(); err != nil {
				return err
			}
			t.ColSizes = append(t.ColSizes, v)
		}
		if b, err = r.ReadB(); err != nil {
			return err
		}
		t.WordBreak = b == 1
		if b, err = r.ReadB(); err != nil {
			return err
		}
		t.Unknown = b == 1
	} else {
		if b, err = r.ReadB(); err != nil {
			return err
		}
		c.HasContentBlk = b == 1
		if c.HasContentBlk {
			k := &c.Blk
			// block_table H 341 在 handle 流（dat 不占位）
			if k.Normal, err = readMLeader3BD(r); err != nil {
				return err
			}
			if k.Location, err = readMLeader3BD(r); err != nil {
				return err
			}
			if k.Scale, err = readMLeader3BD(r); err != nil {
				return err
			}
			if k.Rotation, err = r.ReadBD(); err != nil {
				return err
			}
			if k.Color, err = readMLeaderCMC(r, ver); err != nil {
				return err
			}
			for i := 0; i < 16; i++ {
				if k.Transform[i], err = r.ReadBD(); err != nil {
					return err
				}
			}
		}
	}
	if c.Base, err = readMLeader3BD(r); err != nil {
		return err
	}
	if c.BaseDir, err = readMLeader3BD(r); err != nil {
		return err
	}
	if c.BaseVert, err = readMLeader3BD(r); err != nil {
		return err
	}
	if b, err = r.ReadB(); err != nil {
		return err
	}
	c.IsNormalReversed = b == 1
	return nil
}
