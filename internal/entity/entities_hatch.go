// entities_hatch.go 实现 HATCH 实体解码：渐变填充段、边界路径
// （边集/多段线两类）、图案定义段与种子点段（保留原始参数供审计对照）。
// 位级布局对照 LibreDWG dwg.spec DWG_ENTITY (HATCH)。
package entity

import (
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/objrec"
	"math"
	"strconv"
)

// hatchStringMode 图案名字符串的存储模式。
type hatchStringMode int

const (
	HatchStrInlineTv     hatchStringMode = iota // 主流内 TV（codepage）
	HatchStrInlineTu                            // 内联 UTF-16
	HatchStrStringStream                        // R2010+ 字符串流（数据流内为空串）
)

// entHatch 填充实体。
type EntHatch struct {
	BaseEntity
	codepage uint16 // 文档码页（pre-R2007 内联 TV 字符串按此解码）
	// 渐变填充段（R2004+）
	IsGradientFill      uint32
	Reserved            uint32
	GradientAngle       float64
	GradientShift       float64
	SingleColorGradient uint32
	GradientTint        float64
	GradientName        string
	Colors              []HatchGradientColor // 渐变色数组（shift + CMC 标量）
	PixelSize           float64              // has_derived 时的像素尺寸
	Elevation           float64
	Extrusion           Point3
	Name                string
	SolidFill           bool
	Associative         bool
	Style               uint16
	PatternType         uint16
	Angle               float64
	ScaleSpacing        float64
	DoubleFlag          bool
	HasDerived          bool
	Deflines            []HatchDefLine
	Paths               []HatchPath
	Seeds               []Point2 // 种子点（DXF 98+10/20；DWG 位流侧暂不消费）
}

// hatchGradientColor 渐变填充单色标量（CMC 名/书名不保存）。
type HatchGradientColor struct {
	ShiftValue float64 // BD 偏移值（0~1）
	ColorIndex int64   // CMC index
	ColorRGB   string  // CMC rgb 32 位十六进制
}

// hatchDefLine 图案定义线（angle + 原点 + 偏移 + 划线参数）。
type HatchDefLine struct {
	Angle  float64
	Pt0    Point2
	Offset Point2
	Dashes []float64
}

// hatchPath 单条边界路径：保留原始 flag/段参数（审计对照），
// points 为已细分的闭合点列（渲染用）。
type HatchPath struct {
	Flag           uint32
	IsPolyline     bool // true 为多段线路径（flag&2），false 为边集路径
	BulgesPresent  bool
	Closed         bool
	NumSegsOrPaths uint32
	Segs           []HatchSeg      // 边集路径的原始段参数
	PolyVerts      []HatchPolyVert // 多段线路径的原始顶点
	Points         []Point2        // 细分点列（渲染）
	// 路径尾部 BL 计数与 handle 流中的边界对象句柄（gold
	// paths[i].boundary_handles 键导出）
	numBoundaryHandles uint32
	boundaryHandles    []uint64
}

// hatchPolyVert 多段线路径顶点（含可选凸度）。
type HatchPolyVert struct {
	P     Point2
	Bulge float64
}

// hatchSeg 边集路径段：按 curve_type 保留全部原始标量/数组参数。
type HatchSeg struct {
	CurveType uint8
	// curve_type 1 直线：first/second endpoint
	First, Second Point2
	// curve_type 2 圆弧 / 3 椭圆弧：center + 半轴（radius 或 endpoint+ratio）
	Center   Point2
	Radius   float64 // 圆弧半径
	Endpoint Point2  // 椭圆弧主轴端点
	Ratio    float64 // 椭圆弧主次轴比
	StartAng float64
	EndAng   float64
	Ccw      bool
	// curve_type 4 样条
	Degree   uint32
	Rational bool
	Periodic bool
	Knots    []float64
	Ctrl     []Point2
	Weights  []float64
	FitPts   []Point2
	startTan Point2
	endTan   Point2
}

// decodeHatch HATCH：R2004+ 解析渐变段；R2007+ 图案名内联 TU。
// 同位流多候选（TV/TU）解析按路径数评分取最优，容忍版本差异。
func decodeHatch(r *bitstream.BitStream, Head *CommonEntityHead, ver container.DwgVersion, codepage uint16) (any, error) {
	unicodeText := ver >= container.VerR2007
	var modes []hatchStringMode
	switch {
	case ver == container.VerR2004 || ver == container.VerR2007:
		modes = []hatchStringMode{HatchStrInlineTv}
		if ver == container.VerR2007 {
			modes = []hatchStringMode{HatchStrInlineTu, HatchStrInlineTv}
		}
	case unicodeText:
		modes = []hatchStringMode{HatchStrInlineTu, HatchStrInlineTv}
	default:
		modes = []hatchStringMode{HatchStrInlineTv}
	}
	pos := r.TellBits()
	var best *EntHatch
	bestScore := uint64(0)
	var lastErr error
	streamName := ver >= container.VerR2007 // R2007+ 图案名/渐变名存于对象字符串区
	hasGradient := ver >= container.VerR2004
	for _, Mode := range modes {
		r.SetBitPos(pos)
		ent, err := decodeHatchBody(r, Head, hatchBodyOpts{strMode: Mode, FitPoints: ver >= container.VerR2010, gradient: ver >= container.VerR2004, streamName: streamName, codepage: codepage})
		if err != nil {
			lastErr = err
			continue
		}
		score := uint64(len(ent.Paths))
		if best == nil || score > bestScore {
			best, bestScore = ent, score
		}
	}
	if best != nil {
		if streamName {
			// R2013+ 名称存于对象字符串区（主数据流零占位），按 spec
			// 顺序恢复：渐变名在前、图案名在后
			if hasGradient {
				if tus := hatchStreamStrings(r, Head, 8); len(tus) > 0 {
					best.GradientName = tus[0]
					if len(tus) > 1 {
						best.Name = tus[1]
					}
				}
			}
			if best.Name == "" {
				best.Name = streamAreaText(r, Head)
			}
		}
		return best, nil
	}
	return nil, fmt.Errorf("cad: HATCH 全部字符串模式失败: %w", lastErr)
}

// hatchBodyOpts HATCH 主体的版本化读取选项。
type hatchBodyOpts struct {
	strMode    hatchStringMode // 名称/渐变名字符串编码
	FitPoints  bool            // R2010+ 样条边拟合点段
	gradient   bool            // R2004+ 渐变段
	streamName bool            // R2007+ 名称存于对象字符串区（主数据流零占位）
	codepage   uint16
}

// decodeHatchBody 解析 HATCH 主体：[渐变段] 高程/挤出/名称/实体填充/关联标志/
// 路径数组/图案定义段/种子点段，全部字段解析保存（不再盲跳）。
func decodeHatchBody(r *bitstream.BitStream, Head *CommonEntityHead, opt hatchBodyOpts) (*EntHatch, error) {
	h := &EntHatch{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}, codepage: opt.codepage}
	var err error
	if opt.gradient {
		// 渐变名与图案名同为 FIELD_T：R2013+ 走字符串流，否则 R2007+ 内联 TU / TV
		if err = decodeHatchGradient(r, h, opt.streamName, opt.strMode != HatchStrInlineTv); err != nil {
			return nil, err
		}
	}
	if h.Elevation, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if h.Extrusion, err = read3pt(r); err != nil {
		return nil, err
	}
	if !opt.streamName {
		if h.Name, err = ReadHatchString(r, opt.strMode, opt.codepage); err != nil {
			return nil, err
		}
	}
	var v uint8
	if v, err = r.ReadB(); err != nil {
		return nil, err
	}
	h.SolidFill = v != 0
	if v, err = r.ReadB(); err != nil {
		return nil, err
	}
	h.Associative = v != 0

	if h.Paths, h.HasDerived, err = decodeHatchPaths(r, opt.FitPoints); err != nil {
		return nil, err
	}

	// 图案样式/定义线段
	if h.Style, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if h.PatternType, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if !h.SolidFill {
		if h.Angle, err = r.ReadBD(); err != nil {
			return nil, err
		}
		if h.ScaleSpacing, err = r.ReadBD(); err != nil {
			return nil, err
		}
		if v, err = r.ReadB(); err != nil {
			return nil, err
		}
		h.DoubleFlag = v != 0
		if h.Deflines, err = readHatchDefLines(r); err != nil {
			return nil, err
		}
	}
	return finishHatchBody(r, Head, h)
}

// readHatchDefLines 读取图案定义线数组（计数 + 逐条 角度/原点/偏移/划线表）。
func readHatchDefLines(r *bitstream.BitStream) ([]HatchDefLine, error) {
	numDefLines, err := r.ReadBS()
	if err != nil {
		return nil, err
	}
	if uint32(numDefLines) > 100_000 {
		return nil, fmt.Errorf("cad: HATCH 定义线数异常 %d", numDefLines)
	}
	Deflines := make([]HatchDefLine, 0, numDefLines)
	for i := uint32(0); i < uint32(numDefLines); i++ {
		dl, err := readHatchDefLine(r)
		if err != nil {
			return nil, err
		}
		Deflines = append(Deflines, dl)
	}
	return Deflines, nil
}

// readHatchDefLine 读取单条图案定义线。
func readHatchDefLine(r *bitstream.BitStream) (HatchDefLine, error) {
	var dl HatchDefLine
	var err error
	if dl.Angle, err = r.ReadBD(); err != nil {
		return dl, err
	}
	if dl.Pt0.X, err = r.ReadBD(); err != nil {
		return dl, err
	}
	if dl.Pt0.Y, err = r.ReadBD(); err != nil {
		return dl, err
	}
	if dl.Offset.X, err = r.ReadBD(); err != nil {
		return dl, err
	}
	if dl.Offset.Y, err = r.ReadBD(); err != nil {
		return dl, err
	}
	numDashes, err := r.ReadBS()
	if err != nil {
		return dl, err
	}
	if uint32(numDashes) > 100_000 {
		return dl, fmt.Errorf("cad: HATCH 划线数异常 %d", numDashes)
	}
	for j := uint32(0); j < uint32(numDashes); j++ {
		d, e := r.ReadBD()
		if e != nil {
			return dl, e
		}
		dl.Dashes = append(dl.Dashes, d)
	}
	return dl, nil
}

// finishHatchBody 收尾：has_derived 推导（路径 flag & 0x04）→ pixel_size +
// 种子点段 + 边界句柄流（句柄按各 path 尾部计数顺序连续排列）。
func finishHatchBody(r *bitstream.BitStream, Head *CommonEntityHead, h *EntHatch) (*EntHatch, error) {
	var err error
	if h.HasDerived {
		if h.PixelSize, err = r.ReadBD(); err != nil {
			return nil, err
		}
	}

	numSeeds, err := r.ReadBL()
	if err != nil {
		return nil, err
	}
	if numSeeds > 100_000 {
		return nil, fmt.Errorf("cad: HATCH 种子点数异常 %d", numSeeds)
	}
	for i := uint32(0); i < numSeeds; i++ {
		sx, e := r.ReadRD()
		if e != nil {
			return nil, e
		}
		sy, e := r.ReadRD()
		if e != nil {
			return nil, e
		}
		h.Seeds = append(h.Seeds, Point2{sx, sy}) // gold seeds 键导出
	}
	r.SetBitPos(Head.ObjSizeBit)
	if _, Layer, e := ParseCommonEntityHandles(r, Head); e == nil {
		h.Layer = Layer
	}
	for pi := range h.Paths {
		P := &h.Paths[pi]
		for i := uint32(0); i < P.numBoundaryHandles; i++ {
			hh, e := objrec.ReadHandleReference(r, Head.Handle)
			if e != nil {
				break
			}
			P.boundaryHandles = append(P.boundaryHandles, hh)
		}
	}
	return h, nil
}

// decodeHatchPaths 解析 HATCH/MPOLYGON 共用的边界路径数组（dwg.spec 两类
// 实体的 REPEAT(paths) 段位布局一致）：每条路径 flag BL（bit2 置位为多段线
// 路径）→ 边集路径（curve_type RC 逐段：1 直线/2 圆弧/3 椭圆弧/4 样条）或
// 多段线路径（bulge/closed 位 + 2RD 顶点序列），路径尾部 BL 为边界对象
// 句柄数（句柄本体存于 handle 流，此处仅占位跳过）。
// splineFitPoints 为 R2010+ 样条边拟合点段标志；hasDerived 由路径
// flag bit4 推导（HATCH 主体 pixel_size 段的读取条件，MPOLYGON 不使用）。
func decodeHatchPaths(r *bitstream.BitStream, splineFitPoints bool) (Paths []HatchPath, HasDerived bool, err error) {
	numPaths, err := r.ReadBL()
	if err != nil {
		return nil, false, err
	}
	if numPaths > 100_000 {
		return nil, false, fmt.Errorf("cad: HATCH 路径数异常 %d", numPaths)
	}
	for i := uint32(0); i < numPaths; i++ {
		pathFlag, err := r.ReadBL()
		if err != nil {
			return nil, false, err
		}
		P := HatchPath{Flag: pathFlag}
		if pathFlag&0x04 != 0 {
			HasDerived = true
		}
		if pathFlag&0x02 == 0 {
			// 边集路径
			P.IsPolyline = false
			numSegs, err := r.ReadBL()
			if err != nil {
				return nil, false, err
			}
			if numSegs > 100_000 {
				return nil, false, fmt.Errorf("cad: HATCH 边数异常 %d", numSegs)
			}
			P.NumSegsOrPaths = numSegs
			var pts []Point2
			for j := uint32(0); j < numSegs; j++ {
				segType, err := r.ReadRC()
				if err != nil {
					return nil, false, err
				}
				seg := HatchSeg{CurveType: segType}
				switch segType {
				case 1: // 直线边
					if seg.First.X, err = r.ReadRD(); err != nil {
						return nil, false, err
					}
					if seg.First.Y, err = r.ReadRD(); err != nil {
						return nil, false, err
					}
					if seg.Second.X, err = r.ReadRD(); err != nil {
						return nil, false, err
					}
					if seg.Second.Y, err = r.ReadRD(); err != nil {
						return nil, false, err
					}
					pts = append(pts, seg.First, seg.Second)
				case 2: // 圆弧边
					if seg.Center.X, err = r.ReadRD(); err != nil {
						return nil, false, err
					}
					if seg.Center.Y, err = r.ReadRD(); err != nil {
						return nil, false, err
					}
					if seg.Radius, err = r.ReadBD(); err != nil {
						return nil, false, err
					}
					if seg.StartAng, err = r.ReadBD(); err != nil {
						return nil, false, err
					}
					if seg.EndAng, err = r.ReadBD(); err != nil {
						return nil, false, err
					}
					if Ccw, e := r.ReadB(); e != nil {
						return nil, false, e
					} else {
						seg.Ccw = Ccw != 0
					}
					pts = appendArcPoints(pts, seg.Center, seg.Radius, seg.Radius, seg.StartAng, seg.EndAng, seg.Ccw, 64)
				case 3: // 椭圆弧边
					if seg.Center.X, err = r.ReadRD(); err != nil {
						return nil, false, err
					}
					if seg.Center.Y, err = r.ReadRD(); err != nil {
						return nil, false, err
					}
					if seg.Endpoint.X, err = r.ReadRD(); err != nil {
						return nil, false, err
					}
					if seg.Endpoint.Y, err = r.ReadRD(); err != nil {
						return nil, false, err
					}
					if seg.Ratio, err = r.ReadBD(); err != nil {
						return nil, false, err
					}
					if seg.StartAng, err = r.ReadBD(); err != nil {
						return nil, false, err
					}
					if seg.EndAng, err = r.ReadBD(); err != nil {
						return nil, false, err
					}
					if Ccw, e := r.ReadB(); e != nil {
						return nil, false, e
					} else {
						seg.Ccw = Ccw != 0
					}
					pts = appendArcPoints(pts, seg.Center, seg.Endpoint.X, seg.Endpoint.Y*seg.Ratio, seg.StartAng, seg.EndAng, seg.Ccw, 96)
				case 4: // 样条边
					fit, segPts, e := DecodeHatchSplineEdge(r, splineFitPoints)
					if e != nil {
						return nil, false, e
					}
					seg = *fit
					pts = append(pts, segPts...)
				default:
					return nil, false, fmt.Errorf("cad: 不支持的 HATCH 边类型 %d", segType)
				}
				P.Segs = append(P.Segs, seg)
			}
			if n, err := r.ReadBL(); err != nil { // 边界对象句柄数
				return nil, false, err
			} else {
				P.numBoundaryHandles = n
			}
			P.Points = ClosePath(pts)
			P.Closed = true
		} else {
			// 多段线路径
			P.IsPolyline = true
			BulgesPresent, e := r.ReadB()
			if e != nil {
				return nil, false, e
			}
			Closed, e := r.ReadB()
			if e != nil {
				return nil, false, e
			}
			P.BulgesPresent = BulgesPresent != 0
			P.Closed = Closed != 0
			numVerts, err := r.ReadBL()
			if err != nil {
				return nil, false, err
			}
			if numVerts > 100_000 {
				return nil, false, fmt.Errorf("cad: HATCH 顶点数异常 %d", numVerts)
			}
			P.NumSegsOrPaths = numVerts
			var verts []Point2
			var Bulges []float64
			for j := uint32(0); j < numVerts; j++ {
				pv := HatchPolyVert{}
				if pv.P.X, err = r.ReadRD(); err != nil {
					return nil, false, err
				}
				if pv.P.Y, err = r.ReadRD(); err != nil {
					return nil, false, err
				}
				if P.BulgesPresent {
					if pv.Bulge, err = r.ReadBD(); err != nil {
						return nil, false, err
					}
				}
				verts = append(verts, pv.P)
				Bulges = append(Bulges, pv.Bulge)
				P.PolyVerts = append(P.PolyVerts, pv)
			}
			if n, err := r.ReadBL(); err != nil { // 边界对象句柄数
				return nil, false, err
			} else {
				P.numBoundaryHandles = n
			}
			pts := verts
			if P.BulgesPresent {
				pts = PolylineWithBulges(verts, Bulges, P.Closed, 64)
			}
			if P.Closed {
				pts = ClosePath(pts)
			}
			P.Points = pts
		}
		Paths = append(Paths, P)
	}
	return Paths, HasDerived, nil
}

// decodeHatchGradient 解析 R2004+ 渐变填充段（dwg.spec _HATCH_gradientfill）：
// is_gradient_fill/reserved/角度/偏移/单色标志/色 tint + 色数组（shift BD + CMC）
// + 渐变名。
func decodeHatchGradient(r *bitstream.BitStream, h *EntHatch, streamName, tu bool) error {
	var err error
	if h.IsGradientFill, err = r.ReadBL(); err != nil {
		return err
	}
	if h.Reserved, err = r.ReadBL(); err != nil {
		return err
	}
	if h.GradientAngle, err = r.ReadBD(); err != nil {
		return err
	}
	if h.GradientShift, err = r.ReadBD(); err != nil {
		return err
	}
	if h.SingleColorGradient, err = r.ReadBL(); err != nil {
		return err
	}
	if h.GradientTint, err = r.ReadBD(); err != nil {
		return err
	}
	numColors, err := r.ReadBL()
	if err != nil {
		return err
	}
	if numColors > 1000 {
		return fmt.Errorf("cad: HATCH 渐变色数异常 %d", numColors)
	}
	for i := uint32(0); i < numColors; i++ {
		var gc HatchGradientColor
		if gc.ShiftValue, err = r.ReadBD(); err != nil { // shift value
			return err
		}
		c, e := readColorCMCR2004(r)
		if e != nil {
			return e
		}
		gc.ColorIndex, e = strconv.ParseInt(c[0], 10, 64)
		if e != nil {
			return e
		}
		gc.ColorRGB = c[1]
		h.Colors = append(h.Colors, gc)
	}
	if streamName {
		return nil // R2013+ 渐变名在字符串区，主数据流零占位
	}
	if tu {
		h.GradientName, err = r.ReadTU()
	} else {
		h.GradientName, err = r.ReadTV(h.codepage)
	}
	return err
}

// readColorCMCR2004 读取 R2004+ CMC 颜色字段（BS index + BL rgb + RC
// flag + [flag&1 TU 名 + flag&2 TU 书名]），返回 [index, rgb 字串]。
// rgb 高字节 method 保留完整 32 位（对齐 gold 的 c200xxxx 口径）。
func readColorCMCR2004(r *bitstream.BitStream) ([2]string, error) {
	var out [2]string
	idx, err := r.ReadBS()
	if err != nil {
		return out, err
	}
	Rgb, err := r.ReadBL()
	if err != nil {
		return out, err
	}
	Flag, err := r.ReadRC()
	if err != nil {
		return out, err
	}
	if Flag < 4 {
		if Flag&1 != 0 {
			if _, err := r.ReadTU(); err != nil {
				return out, err
			}
		}
		if Flag&2 != 0 {
			if _, err := r.ReadTU(); err != nil {
				return out, err
			}
		}
	}
	_ = idx // index 由 palette 反查修正（对齐 bit_read_CMC fixup）
	out[0] = fmt.Sprintf("%d", DwgFindColorIndex(Rgb))
	out[1] = fmt.Sprintf("%08x", Rgb)
	return out, nil
}

// skipColorCMCR2004 读取并丢弃 R2004+ CMC 颜色字段。
func SkipColorCMCR2004(r *bitstream.BitStream) error {
	_, err := readColorCMCR2004(r)
	return err
}

// readHatchString 按模式读取图案名。
func ReadHatchString(r *bitstream.BitStream, Mode hatchStringMode, codepage uint16) (string, error) {
	switch Mode {
	case HatchStrInlineTv:
		return r.ReadTV(codepage)
	case HatchStrInlineTu:
		return r.ReadTU()
	default: // StringStream：数据流内为空串
		return "", nil
	}
}

// decodeHatchSplineEdge 样条边全量解析（degree/标志/节点/控制点/R2010+ 拟合点），
// 返回原始段参数与细分点列。
func DecodeHatchSplineEdge(r *bitstream.BitStream, hasFitPoints bool) (*HatchSeg, []Point2, error) {
	seg := &HatchSeg{CurveType: 4}
	var err error
	if seg.Degree, err = r.ReadBL(); err != nil {
		return nil, nil, err
	}
	if seg.Degree > 25 {
		return nil, nil, fmt.Errorf("cad: HATCH 样条度数异常 %d", seg.Degree)
	}
	var Rational, Periodic uint8
	if Rational, err = r.ReadB(); err != nil {
		return nil, nil, err
	}
	if Periodic, err = r.ReadB(); err != nil {
		return nil, nil, err
	}
	seg.Rational = Rational != 0
	seg.Periodic = Periodic != 0
	numKnots, err := r.ReadBL()
	if err != nil {
		return nil, nil, err
	}
	if numKnots > 1_000_000 {
		return nil, nil, fmt.Errorf("cad: HATCH 样条节点数异常 %d", numKnots)
	}
	numControl, err := r.ReadBL()
	if err != nil {
		return nil, nil, err
	}
	if numControl > 1_000_000 {
		return nil, nil, fmt.Errorf("cad: HATCH 样条控制点数异常 %d", numControl)
	}
	for i := uint32(0); i < numKnots; i++ {
		k, e := r.ReadBD()
		if e != nil {
			return nil, nil, e
		}
		seg.Knots = append(seg.Knots, k)
	}
	for i := uint32(0); i < numControl; i++ {
		var c Point2
		if c.X, err = r.ReadRD(); err != nil {
			return nil, nil, err
		}
		if c.Y, err = r.ReadRD(); err != nil {
			return nil, nil, err
		}
		seg.Ctrl = append(seg.Ctrl, c)
		if seg.Rational {
			w, e := r.ReadBD()
			if e != nil {
				return nil, nil, e
			}
			seg.Weights = append(seg.Weights, w)
		}
	}
	// R2010+ 拟合点段：AutoCAD 仅在样条由拟合点定义（num_fitpts>0）时
	// 写入拟合点与首末端点切线；num_fitpts=0 时整段缺席，无条件读切线
	// 会越位 256 位并丢失后续边界/路径（LibreDWG dwg.spec 同注）。
	if hasFitPoints {
		numFit, e := r.ReadBL()
		if e != nil {
			return nil, nil, e
		}
		if numFit > 1_000_000 {
			return nil, nil, fmt.Errorf("cad: HATCH 拟合点数异常 %d", numFit)
		}
		if numFit > 0 {
			for i := uint32(0); i < numFit; i++ {
				var f Point2
				if f.X, err = r.ReadRD(); err != nil {
					return nil, nil, err
				}
				if f.Y, err = r.ReadRD(); err != nil {
					return nil, nil, err
				}
				seg.FitPts = append(seg.FitPts, f)
			}
			if seg.startTan.X, err = r.ReadRD(); err != nil {
				return nil, nil, err
			}
			if seg.startTan.Y, err = r.ReadRD(); err != nil {
				return nil, nil, err
			}
			if seg.endTan.X, err = r.ReadRD(); err != nil {
				return nil, nil, err
			}
			if seg.endTan.Y, err = r.ReadRD(); err != nil {
				return nil, nil, err
			}
		}
	}
	// 细分：优先 B 样条插值，退化用拟合点/控制点
	var pts []Point2
	ctrl3 := make([]Point3, len(seg.Ctrl))
	for i, c := range seg.Ctrl {
		ctrl3[i] = Point3{c.X, c.Y, 0}
	}
	deg := int(seg.Degree)
	if len(seg.Ctrl) >= 2 && deg > 0 && deg < len(seg.Ctrl) && len(seg.Knots) >= len(seg.Ctrl)+deg+1 {
		spanCount := len(seg.Ctrl) - deg
		for i := 0; i < spanCount; i++ {
			t0, t1 := seg.Knots[i+deg], seg.Knots[i+deg+1]
			if t1 <= t0 {
				continue
			}
			for s := 0; s < 8; s++ {
				u := t0 + (t1-t0)*float64(s)/8
				P := DeBoor(ctrl3, seg.Weights, seg.Knots, deg, i+deg, u)
				pts = append(pts, Point2{P.X, P.Y})
			}
		}
		last := seg.Ctrl[len(seg.Ctrl)-1]
		pts = append(pts, last)
	} else if len(seg.FitPts) >= 2 {
		pts = append(pts, seg.FitPts...)
	} else {
		pts = append(pts, seg.Ctrl...)
	}
	return seg, pts, nil
}

// appendArcPoints 追加圆/椭圆弧细分点（半径参数直接传椭圆半轴）。
func appendArcPoints(pts []Point2, Center Point2, ax, ay, a0, a1 float64, Ccw bool, Segs int) []Point2 {
	if Segs < 2 {
		Segs = 2
	}
	if !Ccw && a1 < a0 {
		a1 += 2 * math.Pi
	}
	if Ccw && a1 > a0 {
		// 顺时针声明但角度区间正向：按 ccw 语义反转采样
		a0, a1 = a1, a0+2*math.Pi
	}
	for i := 0; i <= Segs; i++ {
		a := a0 + (a1-a0)*float64(i)/float64(Segs)
		pts = append(pts, Point2{Center.X + ax*math.Cos(a), Center.Y + ay*math.Sin(a)})
	}
	return pts
}

// closePath 闭合路径：首尾不重合时追加首点。
func ClosePath(pts []Point2) []Point2 {
	if len(pts) < 2 {
		return pts
	}
	f, l := pts[0], pts[len(pts)-1]
	const eps = 1e-9
	if math.Abs(f.X-l.X) > eps || math.Abs(f.Y-l.Y) > eps {
		return append(pts, f)
	}
	return pts
}

// polylineWithBulges 展开带凸度顶点的多段线（每段最大 64 点细分）。
func PolylineWithBulges(verts []Point2, Bulges []float64, Closed bool, Segs int) []Point2 {
	var out []Point2
	n := len(verts)
	if n == 0 {
		return out
	}
	segCount := n - 1
	if Closed {
		segCount = n
	}
	for i := 0; i < segCount; i++ {
		a := verts[i]
		b := verts[(i+1)%n]
		var bg float64
		if i < len(Bulges) {
			bg = Bulges[i]
		}
		if bg == 0 {
			out = append(out, a)
			continue
		}
		// 凸度 → 圆弧：sagitta = bulge × 半弦长
		dx, dy := b.X-a.X, b.Y-a.Y
		chord := math.Hypot(dx, dy)
		if chord < 1e-12 {
			out = append(out, a)
			continue
		}
		theta := 4 * math.Atan(bg) // 圆心角
		Radius := chord / (2 * math.Sin(theta/2))
		// 圆心在弦的垂线上
		h := Radius * math.Cos(theta/2)
		nx, ny := -dy/chord, dx/chord
		if bg < 0 {
			nx, ny = -nx, -ny
		}
		cx := a.X + dx/2 + nx*h
		cy := a.Y + dy/2 + ny*h
		a0 := math.Atan2(a.Y-cy, a.X-cx)
		a1 := math.Atan2(b.Y-cy, b.X-cx)
		if bg > 0 && a1 < a0 {
			a1 += 2 * math.Pi
		}
		if bg < 0 && a1 > a0 {
			a1 -= 2 * math.Pi
		}
		for s := 0; s < Segs; s++ {
			ang := a0 + (a1-a0)*float64(s)/float64(Segs)
			out = append(out, Point2{cx + Radius*math.Cos(ang), cy + Radius*math.Sin(ang)})
		}
	}
	out = append(out, verts[n-1])
	if Closed {
		out = append(out, verts[0])
	}
	return out
}

// streamAreaText R2007+ 从对象尾部字符串区恢复文本字段值（HATCH 图案名、
// TOLERANCE 标注文本等 FIELD_T 走字符串区的对象）。
// 字符串区起始无显式锚点（bitsize = size×8 − 句柄流长，句柄流长可变），
// 在 dataEnd 前的窗口内逐位尝试读取 TU，取第一个可打印非空串。
// hatchStreamStrings 读取 R2013+ 对象字符串区的 TU 序列（区头 RS
// dataSize @objSizeBit-17，扩展格式 hi_size @objSizeBit-33），从区起点
// 顺序读取至多 max 个 TU。渐变 HATCH 的槽序：gradient_name、name。
func hatchStreamStrings(r *bitstream.BitStream, Head *CommonEntityHead, max int) []string {
	End := Head.ObjSizeBit
	if End < 40 {
		return nil
	}
	r2 := *r
	r2.SetBitPos(End - 17)
	ds, err := r2.ReadRS()
	if err != nil {
		return nil
	}
	if ds&0x8000 != 0 {
		r2.SetBitPos(End - 33)
		hi, e := r2.ReadRS()
		if e != nil {
			return nil
		}
		ds = (ds & 0x7FFF) | (hi << 15)
	}
	if ds == 0 || int(ds) > 1<<20 {
		return nil
	}
	Start := int64(End) - 17 - int64(ds)
	if Start < 0 {
		return nil
	}
	r2.SetBitPos(uint64(Start))
	out := make([]string, 0, max)
	for i := 0; i < max; i++ {
		s, e := r2.ReadTU()
		if e != nil {
			break
		}
		out = append(out, s)
	}
	return out
}

func streamAreaText(r *bitstream.BitStream, Head *CommonEntityHead) string {
	End := Head.ObjSizeBit
	if End < 40 {
		return ""
	}
	lo := int64(End) - 1200
	if lo < 0 {
		lo = 0
	}
	for Start := lo; Start < int64(End)-20; Start++ {
		r3 := *r
		r3.SetBitPos(uint64(Start))
		s, err := r3.ReadTU()
		if err != nil || s == "" || len(s) > 64 || !printableText(s) {
			continue
		}
		// 名称为字母/数字为主的短串；要求首字符为可见 ASCII，过滤巧合命中
		if s[0] < 0x20 || s[0] > 0x7E {
			continue
		}
		return s
	}
	return ""
}
