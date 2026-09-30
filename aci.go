// aci.go 实现 AutoCAD 颜色索引（ACI）到 RGB 的换算，供渲染与审计导出共用。
// 索引语义分四段：1-9 为固定标准色；10-249 为 24 色相 × 10 明暗带的 HSV
// 近似色（包初始化时一次性预展开为查找表）；250-255 为线性灰阶；
// 0/256/257 及其余值表示随块/随层/未定义语义，不产生具体颜色。
package cad

import "fmt"

// aciExtendedTable 扩展段（10..249）预展开的 RGB 查找表：下标 i 对应索引
// i+10。构建逻辑在包初始化时执行一次，运行时查表免浮点开销。
var aciExtendedTable [240][3]uint8

func init() {
	for slot := range aciExtendedTable {
		step := slot % 24
		band := slot / 24
		hue := float64(step) / 24.0
		sat := 1.0
		if band >= 5 {
			sat = 0.7
		}
		val := 1.0 - float64(band)*0.08
		if val < 0.28 {
			val = 0.28
		}
		aciExtendedTable[slot][0], aciExtendedTable[slot][1], aciExtendedTable[slot][2] = hsvTint(hue, sat, val)
	}
}

// aciStandardTable 固定标准色表（下标即索引，0 号位不使用）：红黄绿青蓝
// 品红与两级灰。
var aciStandardTable = [10][3]uint8{
	{},
	{255, 0, 0},
	{255, 255, 0},
	{0, 255, 0},
	{0, 255, 255},
	{0, 0, 255},
	{255, 0, 255},
	{0, 0, 0},
	{128, 128, 128},
	{192, 192, 192},
}

// aciColor 将 ACI 索引换算为 RGB；found 表示索引携带具体颜色。
// 索引 7 在白底上翻转为黑色以保证可见性。
func aciColor(index uint16, backgroundIsWhite bool) (uint8, uint8, uint8, bool) {
	switch {
	case index == 7:
		if backgroundIsWhite {
			return 0, 0, 0, true
		}
		return 255, 255, 255, true
	case 1 <= index && index <= 9:
		c := aciStandardTable[index]
		return c[0], c[1], c[2], true
	case 10 <= index && index <= 249:
		c := aciExtendedTable[index-10]
		return c[0], c[1], c[2], true
	case 250 <= index && index <= 255:
		gray := uint8(float64(255) * float64(index-250) / 5.0)
		return gray, gray, gray, true
	default:
		return 0, 0, 0, false
	}
}

// hsvTint HSV（h∈[0,1)）转 8bit RGB：按色相扇区在 v/t/p/q 四个亮度基值间
// 选取 RGB 三通道（标准 HSV→RGB 展开），四舍五入量化。
func hsvTint(h, s, v float64) (uint8, uint8, uint8) {
	sector := int(h*6) % 6
	frac := h*6 - float64(int(h*6))
	p := v * (1 - s)
	q := v * (1 - frac*s)
	t := v * (1 - (1-frac)*s)
	var r, g, b float64
	switch sector {
	case 0:
		r, g, b = v, t, p
	case 1:
		r, g, b = q, v, p
	case 2:
		r, g, b = p, v, t
	case 3:
		r, g, b = p, q, v
	case 4:
		r, g, b = t, p, v
	default:
		r, g, b = v, p, q
	}
	return uint8(r*255 + 0.5), uint8(g*255 + 0.5), uint8(b*255 + 0.5)
}

// splitTrueColor 将 0x00RRGGBB 真彩值拆为字节分量。
func splitTrueColor(rgb uint32) (uint8, uint8, uint8) {
	return uint8(rgb >> 16), uint8(rgb >> 8), uint8(rgb)
}

// hexColor 调试用：颜色十六进制串。
func hexColor(r, g, b uint8) string {
	return fmt.Sprintf("#%02X%02X%02X", r, g, b)
}
