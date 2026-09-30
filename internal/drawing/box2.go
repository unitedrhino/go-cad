// box2.go 世界坐标包围盒与稳健边界估计：离散图元的包围盒合并、
// 中位数稳健取界，供渲染视口与 DXF 头图幅字段共用。
package drawing

import (
	"math"
	"sort"

	"github.com/unitedrhino/go-cad/internal/entity"
)

// box2 世界坐标包围盒。
type Box2 struct {
	MinX, MinY, MaxX, MaxY float64
}

// MtextLineFactor MTEXT 缺省行距（linespace_factor 未存储时的历史默认
// 口径，DXF 44 同源）：包围盒估算与文本渲染共用。
const MtextLineFactor = 1.66

// extend 扩展包围盒以包含点。
func (b *Box2) Extend(x, y float64) {
	if x < b.MinX {
		b.MinX = x
	}
	if y < b.MinY {
		b.MinY = y
	}
	if x > b.MaxX {
		b.MaxX = x
	}
	if y > b.MaxY {
		b.MaxY = y
	}
}

// invalid 包围盒是否无有效内容（从未扩展过）。
func (b Box2) Invalid() bool {
	return math.IsInf(b.MinX, 1) || math.IsInf(b.MaxX, -1)
}

// robustBounds 鲁棒包围盒：以各端点坐标中位数为中心，取离群鲁棒的分位范围，
// 放大 3 倍作为视口；错位解码产生的少量天文数字坐标不影响视口。
// 性能：坐标收集按端点数预分配、中位/分位共用排序副本并原地变换为偏差
// 数组——大图元集（数十万端点）下 append 翻倍与重复复制排序曾是渲染
// 侧最大分配源（pprof ~35%）。
func RobustBounds(prims []Primitive) Box2 {
	n := 0
	for _, p := range prims {
		switch p.Kind {
		case 0:
			n += len(p.Strokes)
		case 1:
			if Plausible(p.Lb.X, p.Lb.Y) {
				n++
			}
		}
	}
	if n == 0 {
		return PrimitivesBounds(prims)
	}
	xs := make([]float64, 0, n)
	ys := make([]float64, 0, n)
	for _, p := range prims {
		switch p.Kind {
		case 0:
			for _, s := range p.Strokes {
				if Plausible(s.X1, s.Y1) {
					xs = append(xs, s.X1)
					ys = append(ys, s.Y1)
				}
				if Plausible(s.X2, s.Y2) {
					xs = append(xs, s.X2)
					ys = append(ys, s.Y2)
				}
			}
		case 1:
			if Plausible(p.Lb.X, p.Lb.Y) {
				xs = append(xs, p.Lb.X)
				ys = append(ys, p.Lb.Y)
			}
		}
	}
	if len(xs) < 4 {
		return PrimitivesBounds(prims)
	}
	// 排序副本求中位数（xs/ys 成对收集，长度恒相等）
	sortedX := append([]float64(nil), xs...)
	sort.Float64s(sortedX)
	sortedY := append([]float64(nil), ys...)
	sort.Float64s(sortedY)
	mx := sortedMedian(sortedX)
	my := sortedMedian(sortedY)
	// 原地变换为相对中位的偏差数组，排序后取 90 分位（xs/ys 此后不再使用）
	for i := range xs {
		xs[i] = math.Abs(xs[i] - mx)
		ys[i] = math.Abs(ys[i] - my)
	}
	sort.Float64s(xs)
	sort.Float64s(ys)
	last := len(xs) - 1
	rx := xs[int(float64(last)*0.9)] * 3
	ry := ys[int(float64(last)*0.9)] * 3
	if rx < 1e-6 {
		rx = 10
	}
	if ry < 1e-6 {
		ry = 10
	}
	return Box2{MinX: mx - rx, MinY: my - ry, MaxX: mx + rx, MaxY: my + ry}
}

// primitivesBounds 计算图元世界包围盒。
func PrimitivesBounds(prims []Primitive) Box2 {
	var b Box2
	b.MinX, b.MinY = math.Inf(1), math.Inf(1)
	b.MaxX, b.MaxY = math.Inf(-1), math.Inf(-1)
	for _, p := range prims {
		switch p.Kind {
		case 0:
			for _, s := range p.Strokes {
				// 剔除错位解码产生的异常坐标（超出工程量级），避免包围盒被撑爆
				if !Plausible(s.X1, s.Y1) || !Plausible(s.X2, s.Y2) {
					continue
				}
				b.Extend(s.X1, s.Y1)
				b.Extend(s.X2, s.Y2)
			}
		case 1:
			if !Plausible(p.Lb.X, p.Lb.Y) {
				continue
			}
			cos, sin := math.Cos(p.Lb.Rot), math.Sin(p.Lb.Rot)
			b.Extend(p.Lb.X, p.Lb.Y)
			b.Extend(p.Lb.X+p.Lb.W*cos, p.Lb.Y+p.Lb.W*sin)
			// 字形字面上延接近整字高（真实字形路径 ~0.88em），按整高计入
			// 包围盒，避免贴近视口边缘的文字整体越界被裁
			b.Extend(p.Lb.X+p.Lb.W/2, p.Lb.Y+p.Lb.H)
			// MTEXT 块自锚点向下（附着 1-3）/双向（4-6）/向上（7-9）排布，
			// 按换行行数与行距系数估算块高计入包围盒：墨迹锚定位置修复后
			// 仅向上扩展整字高不再覆盖下行块（2000/Text.dwg 单 MTEXT 样本
			// 渲染空实证——块体全部落在锚点下方视口之外）
			if p.Lb.Tx != nil && p.Lb.Tx.Attachment != 0 {
				bh := mtextBlockHeightEstimate(p.Lb.Tx)
				mid := p.Lb.X + p.Lb.W*cos/2
				switch (p.Lb.Tx.Attachment-1)/3 + 1 {
				case 2:
					b.Extend(mid, p.Lb.Y-bh/2)
					b.Extend(mid, p.Lb.Y+bh/2)
				case 3:
					b.Extend(mid, p.Lb.Y+bh)
				default:
					b.Extend(mid, p.Lb.Y-bh)
				}
			}
		}
	}
	return b
}

// sortedMedian 已升序数组的中位数（robustBounds 内部用，免重复排序）。
func sortedMedian(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// median 中位数（测试与通用用途；robustBounds 走 sortedMedian 免重复排序）。
func Median(v []float64) float64 {
	sorted := append([]float64(nil), v...)
	sort.Float64s(sorted)
	return sortedMedian(sorted)
}

// plausible 坐标量级合理性（工程图纸世界坐标通常 <1e7）。
func Plausible(x, y float64) bool {
	return entity.IsFinite(x) && entity.IsFinite(y) && math.Abs(x) < 1e7 && math.Abs(y) < 1e7
}

// mtextBlockHeightEstimate MTEXT 块高估算（包围盒口径）：按列宽贪心换行的
// 行数（字符宽 CJK 1.0/其余 0.5 em，与 render 侧换行同口径）与行距系数
// （DXF 44，未存储按 1.66 缺省行距兜底）估算自块顶到块底的世界高度。
func mtextBlockHeightEstimate(tx *GlyphTextInfo) float64 {
	h := tx.HWorld
	if h <= 0 {
		h = 1
	}
	limit := tx.RectWidth / h
	lines := 0
	for _, ln := range tx.Lines {
		em, n := 0.0, 1
		for _, r := range ln {
			w := 1.0
			if r <= 0x2e80 {
				w = 0.5
			}
			if limit > 0 && em+w > limit {
				n++
				em = w
			} else {
				em += w
			}
		}
		lines += n
	}
	if lines < 1 {
		lines = 1
	}
	f := tx.LineFactor
	if f <= 0 {
		f = MtextLineFactor
	}
	return float64(lines-1)*f*h + h
}
