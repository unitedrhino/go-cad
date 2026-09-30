// entities_hatch_fit_test.go HATCH 样条边拟合点切线条件读取的回归门禁：
// 真实语料 uhengshenhua（R2018 智能化深化设计图，1721 个 HATCH 含 13 个
// 渐变+样条边界实例）曾因 num_fitpts=0 时无条件读首末切线越位 256 位
// 全部解码失败，本测试锁定该行为。
package cad

import (
	"os"
	"path/filepath"
	"testing"
)

// uhengshenhua 样本中 13 个曾失败的渐变 HATCH 句柄（EED 链解析失败 /
// 非法句柄字节数 / 字符串模式位流结束均为切线越位后的错位候选症状）。
var uhengshenhuaFixHandles = []uint64{
	8753, 10248,
	104946, 104947, 104948, 104950, 104951,
	104974, 104975, 104976, 104978, 104979, 105004,
}

// TestUhengshenhuaHatchFit 样本整文档零跳过 + 修复句柄全部可取且为 HATCH。
func TestUhengshenhuaHatchFit(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(libredwgTestDataDir(), "corpus-hunt2/uhengshenhua.dwg"))
	if err != nil {
		t.Skip("uhengshenhua 样本不可用")
	}
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if n := doc.Skipped(); n != 0 {
		t.Errorf("skipped=%d, 期望 0（13 个样条边界 HATCH 不应再跳过）", n)
	}
	for _, h := range uhengshenhuaFixHandles {
		ent := doc.EntityByHandle(h)
		if ent == nil {
			t.Errorf("h=%d: 实体缺失", h)
			continue
		}
		hh, ok := ent.(*entHatch)
		if !ok {
			t.Errorf("h=%d: 类型 %T, 期望 *entHatch", h, ent)
			continue
		}
		if len(hh.paths) == 0 {
			t.Errorf("h=%d: 无边界路径", h)
		}
	}
	// 字段抽查：h=104946 为渐变实心填充（样条+双圆弧边界），名称走主
	// 数据流零占位 + 对象字符串区恢复（gold: HEMISPHERICAL / SOLID,_O）。
	ent := doc.EntityByHandle(104946)
	hh, ok := ent.(*entHatch)
	if !ok {
		t.Fatalf("h=104946: 类型 %T", ent)
	}
	if hh.isGradientFill == 0 {
		t.Errorf("h=104946: is_gradient_fill=0, 期望 1")
	}
	if !hh.solidFill {
		t.Errorf("h=104946: is_solid_fill=0, 期望 1")
	}
	if hh.gradientName != "HEMISPHERICAL" {
		t.Errorf("h=104946: gradient_name=%q, 期望 HEMISPHERICAL", hh.gradientName)
	}
	if hh.name != "SOLID,_O" {
		t.Errorf("h=104946: name=%q, 期望 SOLID,_O", hh.name)
	}
	if len(hh.paths) != 1 || len(hh.paths[0].segs) != 3 {
		t.Fatalf("h=104946: paths=%d segs=%d, 期望 1 路径 3 段", len(hh.paths), len(hh.paths[0].segs))
	}
	if ct := hh.paths[0].segs[0].curveType; ct != 4 {
		t.Errorf("h=104946: segs[0].curve_type=%d, 期望 4（样条）", ct)
	}
	if len(hh.paths[0].segs[0].fitPts) != 0 {
		t.Errorf("h=104946: 样条拟合点 %d 个, 期望 0（切线段不读）", len(hh.paths[0].segs[0].fitPts))
	}
}
