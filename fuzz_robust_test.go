// 本文件为全量对齐批次 T 的模糊鲁棒性测试：以确定性随机（固定 seed，可
// 重复）对代表性合法样本做变异模糊（截断/字节翻转/插入/版本串篡改），
// 并对纯随机序列、全零序列与边界长度输入做解析轰炸。验收口径：错误返回
// 允许，任何 panic 都判失败（防御检查修复后按用例固化到
// fuzzRegressionPanicCases，保证回归不复发）。
//
// 样本选择：包内 testdata 五个代际代表（R13/R2000/R2007/R2013/R2018），
// 覆盖段目录式、R2000 页式、R2007 RS+R21、R2004 家族与 R2018 容器五条
// 解析路径，不依赖外部语料目录（无 CAD_LIBREDWG_DATA 也全量可跑）。
package cad

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// fuzzSeed 模糊测试全局随机种子（固定保证可重复；调整会改变全部变异体）。
const fuzzSeed = 20260928

// fuzzMutationRounds 每个代表样本生成的变异体数量。
const fuzzMutationRounds = 200

// fuzzSeedSamples 模糊种子样本（每代际 1 个，相对 testdata 路径）。
var fuzzSeedSamples = []string{
	"lw_example_r13.dwg", // R13：段目录式容器
	"line_2000.dwg",      // R2000：对象图页式容器
	"lw_example2007.dwg", // R2007：RS 去交织 + R21 解压容器
	"lw_example2013.dwg", // R2013：R2004 家族页式容器
	"lw_example2018.dwg", // R2018：R2004 家族最新布局
}

// fuzzVersionStrings 头部版本串篡改候选：全部已知 DWG 版本魔数（含本包
// 不支持的 AC1003 及更早）+ 垃圾值。篡改后按新版本分派解析路径，
// 验证各路径对「头部合法但身体异构」输入的防御性。
var fuzzVersionStrings = []string{
	"AC1032", "AC1027", "AC1024", "AC1021", "AC1018", "AC1015",
	"AC1014", "AC1012", "AC1009", "AC1006", "AC1004", "AC1003",
	"AC2.11", "AC2.10", "AC1.50", "AC1.40",
	"XXXXXX", "\x00\x00\x00\x00\x00\x00", "\xff\xff\xff\xff\xff\xff", "AC10xx",
}

// fuzzRunParseChain 对一份候选输入执行 Parse → RenderPNG → DumpEntities
// 消费链；错误返回视为正常（畸形输入被拒绝），panic 判测试失败。
func fuzzRunParseChain(t *testing.T, tag string, data []byte) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("[%s] panic（须防御修复并固化回归用例）: %v", tag, r)
		}
	}()
	doc, err := Parse(data)
	if err != nil || doc == nil {
		return // 错误返回是合法的拒绝方式
	}
	_, _ = RenderPNG(doc, RenderOptions{Width: 512})
	_, _ = DumpEntities(data)
}

// fuzzMutate 生成第 idx 个变异体（算子按 idx 轮转：截断/翻转/插入/版本串
// 篡改），rng 由调用方按样本播种保证确定性。
func fuzzMutate(rng *rand.Rand, base []byte, idx int) []byte {
	out := append([]byte(nil), base...)
	switch idx % 4 {
	case 0: // 随机截断：1/4、1/2、9、10 字节轮转（9/10 为紧贴版本串的临界长度）
		switch (idx / 4) % 4 {
		case 0:
			out = out[:len(out)/4]
		case 1:
			out = out[:len(out)/2]
		case 2:
			if len(out) > 9 {
				out = out[:9]
			}
		case 3:
			if len(out) > 10 {
				out = out[:10]
			}
		}
	case 1: // 随机字节翻转 ×N（N=1..64）
		n := 1 + rng.Intn(64)
		for i := 0; i < n && len(out) > 0; i++ {
			out[rng.Intn(len(out))] = byte(rng.Intn(256))
		}
	case 2: // 随机位置插入 1~16 随机字节
		n := 1 + rng.Intn(16)
		pos := rng.Intn(len(out) + 1)
		ins := make([]byte, n)
		rng.Read(ins)
		out = append(out[:pos], append(ins, out[pos:]...)...)
	case 3: // 头部版本串篡改：轮换已知魔数与垃圾值
		v := fuzzVersionStrings[(idx/4)%len(fuzzVersionStrings)]
		if len(out) >= 6 {
			copy(out[:6], v)
		}
	}
	return out
}

// testdataFileBytes 读取包内 testdata 样本字节（go test 工作目录即包目录）。
func testdataFileBytes(name string) ([]byte, error) {
	return os.ReadFile(filepath.Join("testdata", name))
}

// TestFuzzMutatedSamples 变异模糊主测试：5 样本 × 200 变异体，任何 panic 失败。
func TestFuzzMutatedSamples(t *testing.T) {
	for si, name := range fuzzSeedSamples {
		base, err := testdataFileBytes(name)
		if err != nil {
			t.Skipf("种子样本缺失，跳过: %v", err)
		}
		t.Run(name, func(t *testing.T) {
			rng := rand.New(rand.NewSource(fuzzSeed + int64(si)))
			for i := 0; i < fuzzMutationRounds; i++ {
				mut := fuzzMutate(rng, base, i)
				fuzzRunParseChain(t, fmt.Sprintf("变异#%d", i), mut)
			}
		})
	}
}

// TestFuzzRandomInputs 纯随机输入测试：1000 个随机字节序列（长度 1~4096，
// 固定 seed）+ 100 个全零序列 + 边界长度（0/1/6/11/0x100），任何 panic 失败。
func TestFuzzRandomInputs(t *testing.T) {
	rng := rand.New(rand.NewSource(fuzzSeed))
	for i := 0; i < 1000; i++ {
		n := 1 + rng.Intn(4096)
		buf := make([]byte, n)
		rng.Read(buf)
		fuzzRunParseChain(t, fmt.Sprintf("随机#%d(len=%d)", i, n), buf)
	}
	for i := 0; i < 100; i++ {
		n := 1 + rng.Intn(4096)
		fuzzRunParseChain(t, fmt.Sprintf("全零#%d(len=%d)", i, n), make([]byte, n))
	}
	for _, n := range []int{0, 1, 6, 11, 0x100} {
		buf := make([]byte, n)
		rng.Read(buf)
		fuzzRunParseChain(t, fmt.Sprintf("边界(len=%d)", n), buf)
		fuzzRunParseChain(t, fmt.Sprintf("边界全零(len=%d)", n), make([]byte, n))
	}
}

// fuzzRegressionPanicCase 模糊发现的 panic 固化回归用例：按种子样本与
// 变异轮次重放生成输入（fuzzMutate 确定性保证输入与发现时逐位一致，
// 避免在仓库存放大体积字节），修复后长期保留防复发。
type fuzzRegressionPanicCase struct {
	name   string // 用例名（含缺陷定位线索）
	sample string // 种子样本（相对 testdata）
	round  int    // 变异轮次
	desc   string // panic 现象与根因
}

// fuzzRegressionPanicCases 已修复 panic 的固化清单（批次 T 首轮全语料
// 模糊实证；空清单表示当前无已知 panic 回归）。
var fuzzRegressionPanicCases = []fuzzRegressionPanicCase{
	{
		name: "nil-dynamic-map", sample: "lw_example_r13.dwg", round: 189,
		desc: "assignment to entry in nil map：类段损坏时 loadDynamicTypes 返回 nil map，ensureFixedEntityTypes 直接赋值 panic；修复为 nil 判空返回",
	},
	{
		name: "nil-dynamic-map-2013", sample: "lw_example2013.dwg", round: 19,
		desc: "同 nil-dynamic-map（R2004 家族 decodeObjects 路径）",
	},
	{
		name: "nil-dynamic-map-2018", sample: "lw_example2018.dwg", round: 19,
		desc: "同 nil-dynamic-map（R2018 容器路径）",
	},
	{
		name: "tessarc-nan-angle", sample: "lw_example2007.dwg", round: 5,
		desc: "makeslice: cap out of range：错位解码产生 NaN 弧角，tessArc 的 span/segments 计算溢出为负 cap；修复为角度/圆心/半径有限性防御",
	},
}

// TestFuzzRegressionPanicCases 固化回归：按发现时的种子样本与轮次重放
// 变异输入，任何 panic（含恢复路径的静默吞噬）都判失败。
func TestFuzzRegressionPanicCases(t *testing.T) {
	sampleCache := map[string][]byte{}
	for _, tc := range fuzzRegressionPanicCases {
		t.Run(tc.name, func(t *testing.T) {
			base, ok := sampleCache[tc.sample]
			if !ok {
				var err error
				base, err = testdataFileBytes(tc.sample)
				if err != nil {
					t.Skipf("种子样本缺失: %v", err)
				}
				sampleCache[tc.sample] = base
			}
			var si int
			for i, n := range fuzzSeedSamples {
				if n == tc.sample {
					si = i
				}
			}
			rng := rand.New(rand.NewSource(fuzzSeed + int64(si)))
			mut := base
			for i := 0; i <= tc.round; i++ {
				mut = fuzzMutate(rng, base, i)
			}
			fuzzRunParseChain(t, tc.desc, mut)
		})
	}
}
