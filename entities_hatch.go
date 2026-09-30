// entities_hatch.go 实现 HATCH 实体解码：渐变填充段、边界路径
// （边集/多段线两类）、图案定义段与种子点段（保留原始参数供审计对照）。
// 位级布局对照 LibreDWG dwg.spec DWG_ENTITY (HATCH)。
package cad

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
	hatchStrInlineTv     hatchStringMode = iota // 主流内 TV（codepage）
	hatchStrInlineTu                            // 内联 UTF-16
	hatchStrStringStream                        // R2010+ 字符串流（数据流内为空串）
)

// entHatch 填充实体。
type entHatch struct {
	baseEntity
	codepage uint16 // 文档码页（pre-R2007 内联 TV 字符串按此解码）
	// 渐变填充段（R2004+）
	isGradientFill      uint32
	reserved            uint32
	gradientAngle       float64
	gradientShift       float64
	singleColorGradient uint32
	gradientTint        float64
	gradientName        string
	colors              []hatchGradientColor // 渐变色数组（shift + CMC 标量）
	pixelSize           float64              // has_derived 时的像素尺寸
	elevation           float64
	extrusion           point3
	name                string
	solidFill           bool
	associative         bool
	style               uint16
	patternType         uint16
	angle               float64
	scaleSpacing        float64
	doubleFlag          bool
	hasDerived          bool
	deflines            []hatchDefLine
	paths               []hatchPath
	seeds               []point2 // 种子点（DXF 98+10/20；DWG 位流侧暂不消费）
}

// hatchGradientColor 渐变填充单色标量（CMC 名/书名不保存）。
type hatchGradientColor struct {
	shiftValue float64 // BD 偏移值（0~1）
	colorIndex int64   // CMC index
	colorRGB   string  // CMC rgb 32 位十六进制
}

// hatchDefLine 图案定义线（angle + 原点 + 偏移 + 划线参数）。
type hatchDefLine struct {
	angle  float64
	pt0    point2
	offset point2
	dashes []float64
}

// hatchPath 单条边界路径：保留原始 flag/段参数（审计对照），
// points 为已细分的闭合点列（渲染用）。
type hatchPath struct {
	flag           uint32
	isPolyline     bool // true 为多段线路径（flag&2），false 为边集路径
	bulgesPresent  bool
	closed         bool
	numSegsOrPaths uint32
	segs           []hatchSeg      // 边集路径的原始段参数
	polyVerts      []hatchPolyVert // 多段线路径的原始顶点
	points         []point2        // 细分点列（渲染）
	// 路径尾部 BL 计数与 handle 流中的边界对象句柄（gold
	// paths[i].boundary_handles 键导出）
	numBoundaryHandles uint32
	boundaryHandles    []uint64
}

// hatchPolyVert 多段线路径顶点（含可选凸度）。
type hatchPolyVert struct {
	p     point2
	bulge float64
}

// hatchSeg 边集路径段：按 curve_type 保留全部原始标量/数组参数。
type hatchSeg struct {
	curveType uint8
	// curve_type 1 直线：first/second endpoint
	first, second point2
	// curve_type 2 圆弧 / 3 椭圆弧：center + 半轴（radius 或 endpoint+ratio）
	center   point2
	radius   float64 // 圆弧半径
	endpoint point2  // 椭圆弧主轴端点
	ratio    float64 // 椭圆弧主次轴比
	startAng float64
	endAng   float64
	ccw      bool
	// curve_type 4 样条
	degree   uint32
	rational bool
	periodic bool
	knots    []float64
	ctrl     []point2
	weights  []float64
	fitPts   []point2
	startTan point2
	endTan   point2
}

// decodeHatch HATCH：R2004+ 解析渐变段；R2007+ 图案名内联 TU。
// 同位流多候选（TV/TU）解析按路径数评分取最优，容忍版本差异。
func decodeHatch(r *bitstream.BitStream, head *commonEntityHead, ver container.DwgVersion, codepage uint16) (any, error) {
	unicodeText := ver >= container.VerR2007
	var modes []hatchStringMode
	switch {
	case ver == container.VerR2004 || ver == container.VerR2007:
		modes = []hatchStringMode{hatchStrInlineTv}
		if ver == container.VerR2007 {
			modes = []hatchStringMode{hatchStrInlineTu, hatchStrInlineTv}
		}
	case unicodeText:
		modes = []hatchStringMode{hatchStrInlineTu, hatchStrInlineTv}
	default:
		modes = []hatchStringMode{hatchStrInlineTv}
	}
	pos := r.TellBits()
	var best *entHatch
	bestScore := uint64(0)
	var lastErr error
	streamName := ver >= container.VerR2007 // R2007+ 图案名/渐变名存于对象字符串区
	hasGradient := ver >= container.VerR2004
	for _, mode := range modes {
		r.SetBitPos(pos)
		ent, err := decodeHatchBody(r, head, hatchBodyOpts{strMode: mode, fitPoints: ver >= container.VerR2010, gradient: ver >= container.VerR2004, streamName: streamName, codepage: codepage})
		if err != nil {
			lastErr = err
			continue
		}
		score := uint64(len(ent.paths))
		if best == nil || score > bestScore {
			best, bestScore = ent, score
		}
	}
	if best != nil {
		if streamName {
			// R2013+ 名称存于对象字符串区（主数据流零占位），按 spec
			// 顺序恢复：渐变名在前、图案名在后
			if hasGradient {
				if tus := hatchStreamStrings(r, head, 8); len(tus) > 0 {
					best.gradientName = tus[0]
					if len(tus) > 1 {
						best.name = tus[1]
					}
				}
			}
			if best.name == "" {
				best.name = streamAreaText(r, head)
			}
		}
		return best, nil
	}
	return nil, fmt.Errorf("cad: HATCH 全部字符串模式失败: %w", lastErr)
}

// hatchBodyOpts HATCH 主体的版本化读取选项。
type hatchBodyOpts struct {
	strMode    hatchStringMode // 名称/渐变名字符串编码
	fitPoints  bool            // R2010+ 样条边拟合点段
	gradient   bool            // R2004+ 渐变段
	streamName bool            // R2007+ 名称存于对象字符串区（主数据流零占位）
	codepage   uint16
}

// decodeHatchBody 解析 HATCH 主体：[渐变段] 高程/挤出/名称/实体填充/关联标志/
// 路径数组/图案定义段/种子点段，全部字段解析保存（不再盲跳）。
func decodeHatchBody(r *bitstream.BitStream, head *commonEntityHead, opt hatchBodyOpts) (*entHatch, error) {
	h := &entHatch{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}, codepage: opt.codepage}
	var err error
	if opt.gradient {
		// 渐变名与图案名同为 FIELD_T：R2013+ 走字符串流，否则 R2007+ 内联 TU / TV
		if err = decodeHatchGradient(r, h, opt.streamName, opt.strMode != hatchStrInlineTv); err != nil {
			return nil, err
		}
	}
	if h.elevation, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if h.extrusion, err = read3pt(r); err != nil {
		return nil, err
	}
	if !opt.streamName {
		if h.name, err = readHatchString(r, opt.strMode, opt.codepage); err != nil {
			return nil, err
		}
	}
	var v uint8
	if v, err = r.ReadB(); err != nil {
		return nil, err
	}
	h.solidFill = v != 0
	if v, err = r.ReadB(); err != nil {
		return nil, err
	}
	h.associative = v != 0

	if h.paths, h.hasDerived, err = decodeHatchPaths(r, opt.fitPoints); err != nil {
		return nil, err
	}

	// 图案样式/定义线段
	if h.style, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if h.patternType, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if !h.solidFill {
		if h.angle, err = r.ReadBD(); err != nil {
			return nil, err
		}
		if h.scaleSpacing, err = r.ReadBD(); err != nil {
			return nil, err
		}
		if v, err = r.ReadB(); err != nil {
			return nil, err
		}
		h.doubleFlag = v != 0
		if h.deflines, err = readHatchDefLines(r); err != nil {
			return nil, err
		}
	}
	return finishHatchBody(r, head, h)
}

// readHatchDefLines 读取图案定义线数组（计数 + 逐条 角度/原点/偏移/划线表）。
func readHatchDefLines(r *bitstream.BitStream) ([]hatchDefLine, error) {
	numDefLines, err := r.ReadBS()
	if err != nil {
		return nil, err
	}
	if uint32(numDefLines) > 100_000 {
		return nil, fmt.Errorf("cad: HATCH 定义线数异常 %d", numDefLines)
	}
	deflines := make([]hatchDefLine, 0, numDefLines)
	for i := uint32(0); i < uint32(numDefLines); i++ {
		dl, err := readHatchDefLine(r)
		if err != nil {
			return nil, err
		}
		deflines = append(deflines, dl)
	}
	return deflines, nil
}

// readHatchDefLine 读取单条图案定义线。
func readHatchDefLine(r *bitstream.BitStream) (hatchDefLine, error) {
	var dl hatchDefLine
	var err error
	if dl.angle, err = r.ReadBD(); err != nil {
		return dl, err
	}
	if dl.pt0.x, err = r.ReadBD(); err != nil {
		return dl, err
	}
	if dl.pt0.y, err = r.ReadBD(); err != nil {
		return dl, err
	}
	if dl.offset.x, err = r.ReadBD(); err != nil {
		return dl, err
	}
	if dl.offset.y, err = r.ReadBD(); err != nil {
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
		dl.dashes = append(dl.dashes, d)
	}
	return dl, nil
}

// finishHatchBody 收尾：has_derived 推导（路径 flag & 0x04）→ pixel_size +
// 种子点段 + 边界句柄流（句柄按各 path 尾部计数顺序连续排列）。
func finishHatchBody(r *bitstream.BitStream, head *commonEntityHead, h *entHatch) (*entHatch, error) {
	var err error
	if h.hasDerived {
		if h.pixelSize, err = r.ReadBD(); err != nil {
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
		h.seeds = append(h.seeds, point2{sx, sy}) // gold seeds 键导出
	}
	r.SetBitPos(head.objSizeBit)
	if _, layer, e := parseCommonEntityHandles(r, head); e == nil {
		h.layer = layer
	}
	for pi := range h.paths {
		p := &h.paths[pi]
		for i := uint32(0); i < p.numBoundaryHandles; i++ {
			hh, e := objrec.ReadHandleReference(r, head.handle)
			if e != nil {
				break
			}
			p.boundaryHandles = append(p.boundaryHandles, hh)
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
func decodeHatchPaths(r *bitstream.BitStream, splineFitPoints bool) (paths []hatchPath, hasDerived bool, err error) {
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
		p := hatchPath{flag: pathFlag}
		if pathFlag&0x04 != 0 {
			hasDerived = true
		}
		if pathFlag&0x02 == 0 {
			// 边集路径
			p.isPolyline = false
			numSegs, err := r.ReadBL()
			if err != nil {
				return nil, false, err
			}
			if numSegs > 100_000 {
				return nil, false, fmt.Errorf("cad: HATCH 边数异常 %d", numSegs)
			}
			p.numSegsOrPaths = numSegs
			var pts []point2
			for j := uint32(0); j < numSegs; j++ {
				segType, err := r.ReadRC()
				if err != nil {
					return nil, false, err
				}
				seg := hatchSeg{curveType: segType}
				switch segType {
				case 1: // 直线边
					if seg.first.x, err = r.ReadRD(); err != nil {
						return nil, false, err
					}
					if seg.first.y, err = r.ReadRD(); err != nil {
						return nil, false, err
					}
					if seg.second.x, err = r.ReadRD(); err != nil {
						return nil, false, err
					}
					if seg.second.y, err = r.ReadRD(); err != nil {
						return nil, false, err
					}
					pts = append(pts, seg.first, seg.second)
				case 2: // 圆弧边
					if seg.center.x, err = r.ReadRD(); err != nil {
						return nil, false, err
					}
					if seg.center.y, err = r.ReadRD(); err != nil {
						return nil, false, err
					}
					if seg.radius, err = r.ReadBD(); err != nil {
						return nil, false, err
					}
					if seg.startAng, err = r.ReadBD(); err != nil {
						return nil, false, err
					}
					if seg.endAng, err = r.ReadBD(); err != nil {
						return nil, false, err
					}
					if ccw, e := r.ReadB(); e != nil {
						return nil, false, e
					} else {
						seg.ccw = ccw != 0
					}
					pts = appendArcPoints(pts, seg.center, seg.radius, seg.radius, seg.startAng, seg.endAng, seg.ccw, 64)
				case 3: // 椭圆弧边
					if seg.center.x, err = r.ReadRD(); err != nil {
						return nil, false, err
					}
					if seg.center.y, err = r.ReadRD(); err != nil {
						return nil, false, err
					}
					if seg.endpoint.x, err = r.ReadRD(); err != nil {
						return nil, false, err
					}
					if seg.endpoint.y, err = r.ReadRD(); err != nil {
						return nil, false, err
					}
					if seg.ratio, err = r.ReadBD(); err != nil {
						return nil, false, err
					}
					if seg.startAng, err = r.ReadBD(); err != nil {
						return nil, false, err
					}
					if seg.endAng, err = r.ReadBD(); err != nil {
						return nil, false, err
					}
					if ccw, e := r.ReadB(); e != nil {
						return nil, false, e
					} else {
						seg.ccw = ccw != 0
					}
					pts = appendArcPoints(pts, seg.center, seg.endpoint.x, seg.endpoint.y*seg.ratio, seg.startAng, seg.endAng, seg.ccw, 96)
				case 4: // 样条边
					fit, segPts, e := decodeHatchSplineEdge(r, splineFitPoints)
					if e != nil {
						return nil, false, e
					}
					seg = *fit
					pts = append(pts, segPts...)
				default:
					return nil, false, fmt.Errorf("cad: 不支持的 HATCH 边类型 %d", segType)
				}
				p.segs = append(p.segs, seg)
			}
			if n, err := r.ReadBL(); err != nil { // 边界对象句柄数
				return nil, false, err
			} else {
				p.numBoundaryHandles = n
			}
			p.points = closePath(pts)
			p.closed = true
		} else {
			// 多段线路径
			p.isPolyline = true
			bulgesPresent, e := r.ReadB()
			if e != nil {
				return nil, false, e
			}
			closed, e := r.ReadB()
			if e != nil {
				return nil, false, e
			}
			p.bulgesPresent = bulgesPresent != 0
			p.closed = closed != 0
			numVerts, err := r.ReadBL()
			if err != nil {
				return nil, false, err
			}
			if numVerts > 100_000 {
				return nil, false, fmt.Errorf("cad: HATCH 顶点数异常 %d", numVerts)
			}
			p.numSegsOrPaths = numVerts
			var verts []point2
			var bulges []float64
			for j := uint32(0); j < numVerts; j++ {
				pv := hatchPolyVert{}
				if pv.p.x, err = r.ReadRD(); err != nil {
					return nil, false, err
				}
				if pv.p.y, err = r.ReadRD(); err != nil {
					return nil, false, err
				}
				if p.bulgesPresent {
					if pv.bulge, err = r.ReadBD(); err != nil {
						return nil, false, err
					}
				}
				verts = append(verts, pv.p)
				bulges = append(bulges, pv.bulge)
				p.polyVerts = append(p.polyVerts, pv)
			}
			if n, err := r.ReadBL(); err != nil { // 边界对象句柄数
				return nil, false, err
			} else {
				p.numBoundaryHandles = n
			}
			pts := verts
			if p.bulgesPresent {
				pts = polylineWithBulges(verts, bulges, p.closed, 64)
			}
			if p.closed {
				pts = closePath(pts)
			}
			p.points = pts
		}
		paths = append(paths, p)
	}
	return paths, hasDerived, nil
}

// decodeHatchGradient 解析 R2004+ 渐变填充段（dwg.spec _HATCH_gradientfill）：
// is_gradient_fill/reserved/角度/偏移/单色标志/色 tint + 色数组（shift BD + CMC）
// + 渐变名。
func decodeHatchGradient(r *bitstream.BitStream, h *entHatch, streamName, tu bool) error {
	var err error
	if h.isGradientFill, err = r.ReadBL(); err != nil {
		return err
	}
	if h.reserved, err = r.ReadBL(); err != nil {
		return err
	}
	if h.gradientAngle, err = r.ReadBD(); err != nil {
		return err
	}
	if h.gradientShift, err = r.ReadBD(); err != nil {
		return err
	}
	if h.singleColorGradient, err = r.ReadBL(); err != nil {
		return err
	}
	if h.gradientTint, err = r.ReadBD(); err != nil {
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
		var gc hatchGradientColor
		if gc.shiftValue, err = r.ReadBD(); err != nil { // shift value
			return err
		}
		c, e := readColorCMCR2004(r)
		if e != nil {
			return e
		}
		gc.colorIndex, e = strconv.ParseInt(c[0], 10, 64)
		if e != nil {
			return e
		}
		gc.colorRGB = c[1]
		h.colors = append(h.colors, gc)
	}
	if streamName {
		return nil // R2013+ 渐变名在字符串区，主数据流零占位
	}
	if tu {
		h.gradientName, err = r.ReadTU()
	} else {
		h.gradientName, err = r.ReadTV(h.codepage)
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
	rgb, err := r.ReadBL()
	if err != nil {
		return out, err
	}
	flag, err := r.ReadRC()
	if err != nil {
		return out, err
	}
	if flag < 4 {
		if flag&1 != 0 {
			if _, err := r.ReadTU(); err != nil {
				return out, err
			}
		}
		if flag&2 != 0 {
			if _, err := r.ReadTU(); err != nil {
				return out, err
			}
		}
	}
	_ = idx // index 由 palette 反查修正（对齐 bit_read_CMC fixup）
	out[0] = fmt.Sprintf("%d", dwgFindColorIndex(rgb))
	out[1] = fmt.Sprintf("%08x", rgb)
	return out, nil
}

// skipColorCMCR2004 读取并丢弃 R2004+ CMC 颜色字段。
func skipColorCMCR2004(r *bitstream.BitStream) error {
	_, err := readColorCMCR2004(r)
	return err
}

// readHatchString 按模式读取图案名。
func readHatchString(r *bitstream.BitStream, mode hatchStringMode, codepage uint16) (string, error) {
	switch mode {
	case hatchStrInlineTv:
		return r.ReadTV(codepage)
	case hatchStrInlineTu:
		return r.ReadTU()
	default: // StringStream：数据流内为空串
		return "", nil
	}
}

// decodeHatchSplineEdge 样条边全量解析（degree/标志/节点/控制点/R2010+ 拟合点），
// 返回原始段参数与细分点列。
func decodeHatchSplineEdge(r *bitstream.BitStream, hasFitPoints bool) (*hatchSeg, []point2, error) {
	seg := &hatchSeg{curveType: 4}
	var err error
	if seg.degree, err = r.ReadBL(); err != nil {
		return nil, nil, err
	}
	if seg.degree > 25 {
		return nil, nil, fmt.Errorf("cad: HATCH 样条度数异常 %d", seg.degree)
	}
	var rational, periodic uint8
	if rational, err = r.ReadB(); err != nil {
		return nil, nil, err
	}
	if periodic, err = r.ReadB(); err != nil {
		return nil, nil, err
	}
	seg.rational = rational != 0
	seg.periodic = periodic != 0
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
		seg.knots = append(seg.knots, k)
	}
	for i := uint32(0); i < numControl; i++ {
		var c point2
		if c.x, err = r.ReadRD(); err != nil {
			return nil, nil, err
		}
		if c.y, err = r.ReadRD(); err != nil {
			return nil, nil, err
		}
		seg.ctrl = append(seg.ctrl, c)
		if seg.rational {
			w, e := r.ReadBD()
			if e != nil {
				return nil, nil, e
			}
			seg.weights = append(seg.weights, w)
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
				var f point2
				if f.x, err = r.ReadRD(); err != nil {
					return nil, nil, err
				}
				if f.y, err = r.ReadRD(); err != nil {
					return nil, nil, err
				}
				seg.fitPts = append(seg.fitPts, f)
			}
			if seg.startTan.x, err = r.ReadRD(); err != nil {
				return nil, nil, err
			}
			if seg.startTan.y, err = r.ReadRD(); err != nil {
				return nil, nil, err
			}
			if seg.endTan.x, err = r.ReadRD(); err != nil {
				return nil, nil, err
			}
			if seg.endTan.y, err = r.ReadRD(); err != nil {
				return nil, nil, err
			}
		}
	}
	// 细分：优先 B 样条插值，退化用拟合点/控制点
	var pts []point2
	ctrl3 := make([]point3, len(seg.ctrl))
	for i, c := range seg.ctrl {
		ctrl3[i] = point3{c.x, c.y, 0}
	}
	deg := int(seg.degree)
	if len(seg.ctrl) >= 2 && deg > 0 && deg < len(seg.ctrl) && len(seg.knots) >= len(seg.ctrl)+deg+1 {
		spanCount := len(seg.ctrl) - deg
		for i := 0; i < spanCount; i++ {
			t0, t1 := seg.knots[i+deg], seg.knots[i+deg+1]
			if t1 <= t0 {
				continue
			}
			for s := 0; s < 8; s++ {
				u := t0 + (t1-t0)*float64(s)/8
				p := deBoor(ctrl3, seg.weights, seg.knots, deg, i+deg, u)
				pts = append(pts, point2{p.x, p.y})
			}
		}
		last := seg.ctrl[len(seg.ctrl)-1]
		pts = append(pts, last)
	} else if len(seg.fitPts) >= 2 {
		pts = append(pts, seg.fitPts...)
	} else {
		pts = append(pts, seg.ctrl...)
	}
	return seg, pts, nil
}

// appendArcPoints 追加圆/椭圆弧细分点（半径参数直接传椭圆半轴）。
func appendArcPoints(pts []point2, center point2, ax, ay, a0, a1 float64, ccw bool, segs int) []point2 {
	if segs < 2 {
		segs = 2
	}
	if !ccw && a1 < a0 {
		a1 += 2 * math.Pi
	}
	if ccw && a1 > a0 {
		// 顺时针声明但角度区间正向：按 ccw 语义反转采样
		a0, a1 = a1, a0+2*math.Pi
	}
	for i := 0; i <= segs; i++ {
		a := a0 + (a1-a0)*float64(i)/float64(segs)
		pts = append(pts, point2{center.x + ax*math.Cos(a), center.y + ay*math.Sin(a)})
	}
	return pts
}

// closePath 闭合路径：首尾不重合时追加首点。
func closePath(pts []point2) []point2 {
	if len(pts) < 2 {
		return pts
	}
	f, l := pts[0], pts[len(pts)-1]
	const eps = 1e-9
	if math.Abs(f.x-l.x) > eps || math.Abs(f.y-l.y) > eps {
		return append(pts, f)
	}
	return pts
}

// polylineWithBulges 展开带凸度顶点的多段线（每段最大 64 点细分）。
func polylineWithBulges(verts []point2, bulges []float64, closed bool, segs int) []point2 {
	var out []point2
	n := len(verts)
	if n == 0 {
		return out
	}
	segCount := n - 1
	if closed {
		segCount = n
	}
	for i := 0; i < segCount; i++ {
		a := verts[i]
		b := verts[(i+1)%n]
		var bg float64
		if i < len(bulges) {
			bg = bulges[i]
		}
		if bg == 0 {
			out = append(out, a)
			continue
		}
		// 凸度 → 圆弧：sagitta = bulge × 半弦长
		dx, dy := b.x-a.x, b.y-a.y
		chord := math.Hypot(dx, dy)
		if chord < 1e-12 {
			out = append(out, a)
			continue
		}
		theta := 4 * math.Atan(bg) // 圆心角
		radius := chord / (2 * math.Sin(theta/2))
		// 圆心在弦的垂线上
		h := radius * math.Cos(theta/2)
		nx, ny := -dy/chord, dx/chord
		if bg < 0 {
			nx, ny = -nx, -ny
		}
		cx := a.x + dx/2 + nx*h
		cy := a.y + dy/2 + ny*h
		a0 := math.Atan2(a.y-cy, a.x-cx)
		a1 := math.Atan2(b.y-cy, b.x-cx)
		if bg > 0 && a1 < a0 {
			a1 += 2 * math.Pi
		}
		if bg < 0 && a1 > a0 {
			a1 -= 2 * math.Pi
		}
		for s := 0; s < segs; s++ {
			ang := a0 + (a1-a0)*float64(s)/float64(segs)
			out = append(out, point2{cx + radius*math.Cos(ang), cy + radius*math.Sin(ang)})
		}
	}
	out = append(out, verts[n-1])
	if closed {
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
func hatchStreamStrings(r *bitstream.BitStream, head *commonEntityHead, max int) []string {
	end := head.objSizeBit
	if end < 40 {
		return nil
	}
	r2 := *r
	r2.SetBitPos(end - 17)
	ds, err := r2.ReadRS()
	if err != nil {
		return nil
	}
	if ds&0x8000 != 0 {
		r2.SetBitPos(end - 33)
		hi, e := r2.ReadRS()
		if e != nil {
			return nil
		}
		ds = (ds & 0x7FFF) | (hi << 15)
	}
	if ds == 0 || int(ds) > 1<<20 {
		return nil
	}
	start := int64(end) - 17 - int64(ds)
	if start < 0 {
		return nil
	}
	r2.SetBitPos(uint64(start))
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

func streamAreaText(r *bitstream.BitStream, head *commonEntityHead) string {
	end := head.objSizeBit
	if end < 40 {
		return ""
	}
	lo := int64(end) - 1200
	if lo < 0 {
		lo = 0
	}
	for start := lo; start < int64(end)-20; start++ {
		r3 := *r
		r3.SetBitPos(uint64(start))
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
