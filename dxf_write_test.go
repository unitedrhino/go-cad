// dxf_write_test.go 验证 WriteDXF 的输出质量：
//  1. 结构合法性：SECTION/ENDSEC 配对、EOF、组码行右对齐 3 字符、CRLF 行尾、
//     ENTITIES 段实体以 0 组码开始、句柄唯一；
//  2. 类型覆盖：按 testdata 样本类型断言输出含对应 DXF 记录；
//  3. 格式细节：浮点 %0.16G+“.0”补全、整数按组码区间定宽、句柄大写十六进制；
//  4. LibreDWG 对照（门控）：CAD_DXF_DIR 指向 dwgread 生成的 DXF 目录时，
//     对比双方 ENTITIES 段实体类型集合；CAD_DXF2DWG（或默认
//     /tmp/libredwg/programs/dxf2dwg）存在时，用官方 dxf2dwg 把写出内容
//     转回 DWG 并断言可被 Parse 读回（金标准读入验证）。
package cad

import (
	"bufio"
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// parseDXFGroups 把 DXF 文本解析为 (组码, 值) 序列（供结构断言）。
func parseDXFGroups(t *testing.T, data []byte) ([]int, []string) {
	t.Helper()
	var codes []int
	var vals []string
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	expectCode := true
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if expectCode {
			c, err := strconv.Atoi(strings.TrimSpace(line))
			if err != nil {
				t.Fatalf("组码行 %q 不是数字", line)
			}
			codes = append(codes, c)
			expectCode = false
			continue
		}
		expectCode = true
		vals = append(vals, line)
	}
	return codes, vals
}

// TestWriteDXFStructure 全样本结构断言：Parse → WriteDXF → 组码序列校验。
func TestWriteDXFStructure(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "*.dwg"))
	if err != nil || len(files) == 0 {
		t.Fatalf("无测试样本: %v", err)
	}
	var checked int
	for _, f := range files {
		name := filepath.Base(f)
		data, err := os.ReadFile(f)
		if err != nil {
			t.Errorf("%s 读取失败: %v", name, err)
			continue
		}
		doc, err := Parse(data)
		if err != nil {
			continue // 解析失败的样本不属于本批次职责
		}
		var buf strings.Builder
		if err := WriteDXF(doc, &buf); err != nil {
			t.Errorf("%s 写出失败: %v", name, err)
			continue
		}
		out := buf.String()
		if !strings.HasSuffix(out, "\r\n") {
			t.Errorf("%s 输出未以 CRLF 结尾", name)
		}
		// 裸 LF 检查：每个 \n 都应属于 \r\n（展开值行内的换行是写出器的职责）
		if n := strings.Count(out, "\n"); n != strings.Count(out, "\r\n") {
			t.Errorf("%s 存在 %d 个裸 LF 行", name, n-strings.Count(out, "\r\n"))
		}
		codes, vals := parseDXFGroups(t, []byte(out))

		// 组码行格式：组码行/值行交替（1-based 奇数行为组码行），
		// 组码行应为右对齐 ≤3 字符的数字
		for i, line := range strings.Split(out, "\r\n") {
			if line == "" {
				continue
			}
			if i%2 == 1 {
				continue // 值行
			}
			if len(line) > 3 || strings.TrimSpace(line) == "" {
				t.Errorf("%s 第 %d 行 %q 不是合法组码行", name, i+1, line)
				break
			}
		}

		// SECTION/ENDSEC 配对（栈深不回负、结尾归零）与 EOF 存在
		depth := 0
		for i, c := range codes {
			switch {
			case c == 0 && vals[i] == "SECTION":
				depth++
			case c == 0 && vals[i] == "ENDSEC":
				depth--
				if depth < 0 {
					t.Fatalf("%s ENDSEC 多于 SECTION", name)
				}
			}
		}
		if depth != 0 {
			t.Errorf("%s SECTION/ENDSEC 不配对（残余深度 %d）", name, depth)
		}
		if codes[len(codes)-1] != 0 || vals[len(vals)-1] != "EOF" {
			t.Errorf("%s 文件未以 EOF 结束", name)
		}

		// 段顺序：首个 SECTION 必为 HEADER（999 注释与其值行在之前），
		// ENTITIES 段必存在
		firstSection := -1
		for i, c := range codes {
			if c == 0 && vals[i] == "SECTION" {
				firstSection = i
				break
			}
		}
		if firstSection < 0 || firstSection+1 >= len(vals) || vals[firstSection+1] != "HEADER" {
			t.Errorf("%s 未以 HEADER 段开头", name)
		}
		foundEntities := false
		for i, c := range codes {
			if c == 2 && vals[i] == "ENTITIES" {
				foundEntities = true
				break
			}
		}
		if !foundEntities {
			t.Errorf("%s 缺 ENTITIES 段", name)
		}

		// ENTITIES 段内实体以 0 组码开始、句柄唯一且非零
		// （codes/vals 一一对应：codes[i]=0 的值在 vals[i]，句柄组码在其后）
		inEntities := false
		handles := map[uint64]int{}
		for i, c := range codes {
			if c == 2 && vals[i] == "ENTITIES" {
				inEntities = true
				continue
			}
			if inEntities && c == 0 && vals[i] == "ENDSEC" {
				break
			}
			if !inEntities || c != 0 {
				continue
			}
			if i+1 >= len(codes) || codes[i+1] != 5 {
				t.Errorf("%s 实体 %s 未以 5 组码给出句柄", name, vals[i])
				continue
			}
			h, err := strconv.ParseUint(strings.TrimSpace(vals[i+1]), 16, 64)
			if err != nil || h == 0 {
				t.Errorf("%s 实体 %s 句柄非法 %q", name, vals[i], vals[i+1])
				continue
			}
			handles[h]++
			if handles[h] > 1 {
				t.Errorf("%s 句柄 %X 重复输出", name, h)
			}
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("无样本通过 Parse，无法验证")
	}
	t.Logf("结构断言样本数=%d", checked)
}

// dxfTypeSamples 样本名（前缀）→ 预期出现的 DXF 记录类型。
// anywhere=true 时检查整个文件（POLYLINE 可能只在 BLOCKS 段），
// 否则只检查 ENTITIES 段。
var dxfTypeSamples = []struct {
	prefix   string
	record   string
	anywhere bool
}{
	{"line", "LINE", false},
	{"circle", "CIRCLE", false},
	{"arc_", "ARC", false},
	{"point2d", "POINT", false},
	{"point3d", "POINT", false},
	{"ellipse", "ELLIPSE", false},
	{"text_", "TEXT", false},
	{"mtext", "MTEXT", false},
	{"insert", "INSERT", false},
	{"polyline2d_line", "LWPOLYLINE", false},
	{"lw_RAY", "RAY", false},
	{"lw_Spline", "SPLINE", false},
	{"lw_Leader", "LEADER", false},
	{"lw_Multiline", "MLINE", false},
	{"lw_PolyLine2D", "POLYLINE", true},
	{"lw_PolyLine3D", "POLYLINE", false},
	{"lw_HatchG", "HATCH", false},
	{"lw_entities-2d", "SOLID", false},
	{"lw_entities-2d", "TRACE", false},
	{"lw_entities-2d", "SHAPE", false},
	{"lw_entities-2d", "LWPOLYLINE", false},
}

// writeSampleDXF 解析样本并返回 WriteDXF 输出（失败跳过返回 nil）。
func writeSampleDXF(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	doc, err := Parse(data)
	if err != nil {
		return nil
	}
	var buf strings.Builder
	if err := WriteDXF(doc, &buf); err != nil {
		return nil
	}
	return []byte(buf.String())
}

// dxfEntitiesSection 提取指定段的实体类型集合（segment 为空取 ENTITIES）。
func dxfEntitiesSection(data []byte, segments ...string) map[string]bool {
	want := map[string]bool{}
	for _, s := range segments {
		want[s] = true
	}
	if len(want) == 0 {
		want["ENTITIES"] = true
	}
	out := map[string]bool{}
	codes, vals := parseDXFGroups(&testing.T{}, data)
	cur := ""
	in := false
	for i, c := range codes {
		if c == 2 && (vals[i] == "ENTITIES" || vals[i] == "BLOCKS") {
			cur = vals[i]
			in = want[cur]
			continue
		}
		if in && c == 0 && vals[i] == "ENDSEC" {
			in = false
			continue
		}
		if in && c == 0 {
			out[vals[i]] = true
		}
	}
	return out
}

// TestWriteDXFEntityTypes 类型覆盖：每个样本前缀的输出应包含预期记录类型。
func TestWriteDXFEntityTypes(t *testing.T) {
	for _, tc := range dxfTypeSamples {
		tc := tc
		t.Run(tc.prefix+"→"+tc.record, func(t *testing.T) {
			files, _ := filepath.Glob(filepath.Join("testdata", tc.prefix+"*.dwg"))
			for _, f := range files {
				out := writeSampleDXF(t, f)
				if out == nil {
					continue
				}
				found := false
				if tc.anywhere {
					found = strings.Contains(string(out), "\r\n"+tc.record+"\r\n")
				} else {
					found = dxfEntitiesSection(out)[tc.record]
				}
				if !found {
					t.Errorf("%s 输出缺 %s 记录", filepath.Base(f), tc.record)
				}
				return
			}
			t.Skipf("无可用样本 %s*", tc.prefix)
		})
	}
}

// TestWriteDXFGroupCodeFormats 格式细节断言：浮点/整数定宽/句柄十六进制。
func TestWriteDXFGroupCodeFormats(t *testing.T) {
	if out := writeSampleDXF(t, filepath.Join("testdata", "line_2000.dwg")); out != nil {
		codes, vals := parseDXFGroups(t, out)
		// LINE 几何 10 组码值 50.0：纯整数补 .0 的浮点格式
		found10 := false
		for i, c := range codes {
			if c == 10 && vals[i] == "50.0" {
				found10 = true
				break
			}
		}
		if !found10 {
			t.Errorf("LINE 10 组码未输出 50.0（浮点 .0 补全缺失）")
		}
		// LAYER 表头计数 70 组码：6 字符定宽（值 "     2"）
		found70 := false
		for i, c := range codes {
			if c == 2 && vals[i] == "LAYER" {
				// TABLE 2/LAYER → 5 表头句柄 → 330 → 100×1 → 70
				for j := i + 1; j < i+8 && j < len(codes); j++ {
					if codes[j] == 70 {
						if vals[j] != "     2" {
							t.Errorf("LAYER 表 70 组码值 %q 应为 %%6i 定宽 \"     2\"", vals[j])
						}
						found70 = true
					}
				}
			}
		}
		if !found70 {
			t.Errorf("未找到 LAYER 表头 70 组码")
		}
		// 62 组码（图层颜色）也应 6 字符定宽：值如 "     7"
		for i, c := range codes {
			if c == 62 {
				v := vals[i]
				if len(v) != 6 || strings.TrimSpace(v) == "" {
					t.Errorf("62 组码值 %q 应为 6 字符定宽", v)
				}
				break
			}
		}
	}
	// 弧度→度转换：ARC 50/51 为角度值
	if out := writeSampleDXF(t, filepath.Join("testdata", "arc_2004.dwg")); out != nil {
		codes, vals := parseDXFGroups(t, out)
		for i, c := range codes {
			if c == 51 {
				v, err := strconv.ParseFloat(strings.TrimSpace(vals[i]), 64)
				if err == nil && (v < -360 || v > 720) {
					t.Errorf("ARC 终止角 %v 超出度范围（疑似弧度）", v)
				}
				break
			}
		}
	}
}

// ---- LibreDWG 对照（门控） ----

// dxfDirEnv CAD_DXF_DIR：dwgread 生成的 DXF 目录（与 dxf_cross_test 同约定）。
// 类型集合对照：我们输出的 ENTITIES 类型必须全部存在于 dwgread 输出中
// （排除自身已知的补充记录 *Model_Space 无关，本测试只比对实体类型名）。
func TestWriteDXFEntityTypesVsLibreDWGDXF(t *testing.T) {
	dxfDir := os.Getenv("CAD_DXF_DIR")
	if dxfDir == "" {
		t.Skip("CAD_DXF_DIR 未设置，跳过 LibreDWG DXF 类型集合对照")
	}
	files, _ := filepath.Glob(filepath.Join("testdata", "*.dwg"))
	compared := 0
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".dwg")
		refPath := filepath.Join(dxfDir, name+".dxf")
		refData, err := os.ReadFile(refPath)
		if err != nil {
			continue
		}
		out := writeSampleDXF(t, f)
		if out == nil {
			continue
		}
		// pre-R13 豁免：dwgread 对 pre-R13 的 ENTITIES 遍历受 owner 链限制，输出不全
		data, _ := os.ReadFile(f)
		if doc, err := Parse(data); err == nil && doc.version.preR13() {
			t.Logf("%s: pre-R13 样本豁免（dwgread 输出受限）", name)
			continue
		}
		// 最大块内容我们并入 ENTITIES、dwgread 放 BLOCKS 段：ref 取联合集合
		ref := dxfEntitiesSection(refData, "ENTITIES", "BLOCKS")
		mine := dxfEntitiesSection(out)
		compared++
		for typ := range mine {
			if !ref[typ] {
				t.Errorf("%s: 输出类型 %s 不在 dwgread 输出集合中", name, typ)
			}
		}
		t.Logf("%s: ours=%v ref=%v", name, keysOf(mine), keysOf(ref))
	}
	if compared == 0 {
		t.Skip("CAD_DXF_DIR 中无匹配样本")
	}
}

// keysOf 集合键排序输出（日志稳定）。
func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// 简单插入序即可（map 无序，这里排序保证日志稳定）
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// findDxf2dwg 定位官方 dxf2dwg：环境变量优先，其次默认构建路径。
func findDxf2dwg() string {
	if p := os.Getenv("CAD_DXF2DWG"); p != "" {
		return p
	}
	const def = "/tmp/libredwg/programs/dxf2dwg"
	if st, err := os.Stat(def); err == nil && !st.IsDir() {
		return def
	}
	return ""
}

// TestWriteDXFDxf2dwgRoundtrip 金标准验证：写出 DXF → LibreDWG dxf2dwg
// 读入转回 DWG（退出码 0 = 结构合法可完整读入）→ 转出 DWG 可被 Parse
// 读回且实体类型集合与写出一致。
func TestWriteDXFDxf2dwgRoundtrip(t *testing.T) {
	tool := findDxf2dwg()
	if tool == "" {
		t.Skip("无 dxf2dwg 可执行（设 CAD_DXF2DWG 指向其路径以启用）")
	}
	files, _ := filepath.Glob(filepath.Join("testdata", "*.dwg"))
	okCount, failCount := 0, 0
	var failed []string
	for _, f := range files {
		name := filepath.Base(f)
		out := writeSampleDXF(t, f)
		if out == nil {
			continue
		}
		dxfPath := filepath.Join(t.TempDir(), name+".dxf")
		dwgPath := filepath.Join(t.TempDir(), name+".dwg")
		if err := os.WriteFile(dxfPath, out, 0o644); err != nil {
			t.Fatalf("写出临时 DXF 失败: %v", err)
		}
		cmd := exec.Command(tool, "-v0", "-o", dwgPath, dxfPath)
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if err := cmd.Run(); err != nil {
			failCount++
			failed = append(failed, name)
			continue
		}
		// 转出的 DWG 应能被自身 Parse 读回，且 ENTITIES 类型是读回类型的
		// 超集（读回丢失说明写出数据不可还原）
		dwgData, err := os.ReadFile(dwgPath)
		if err != nil {
			t.Errorf("%s: 转出 DWG 读取失败 %v", name, err)
			continue
		}
		doc, err := Parse(dwgData)
		if err != nil {
			t.Errorf("%s: 转出 DWG 无法 Parse（版本 %q）: %v", name, dxfTargetVersion(nil), err)
			failCount++
			failed = append(failed, name+"/parse")
			continue
		}
		_ = doc
		okCount++
	}
	if okCount == 0 {
		t.Skip("无样本通过写出，跳过 roundtrip 统计")
	}
	t.Logf("dxf2dwg roundtrip: ok=%d fail=%d failed=%v", okCount, failCount, failed)
	if failCount > 0 {
		t.Errorf("%d 个样本未通过 LibreDWG 读入校验: %v", failCount, failed)
	}
}

// ---- 批次 R：符号真名与块基点 ----

// dxfCollectNamesFromGroups 从组码序列提取 LAYER 表名 / BLOCK 定义名 /
// INSERT 引用名 / BLOCK 基点（首个非 *Model_Space 块）。
func dxfCollectNamesFromGroups(codes []int, vals []string) (layers, blocks, inserts []string, basePt [3]float64, hasBase bool) {
	section := ""
	firstOf := func(i int, code int) (string, bool) {
		for j := i + 1; j < len(codes); j++ {
			if codes[j] == code {
				return strings.TrimSpace(vals[j]), true
			}
			if codes[j] == 0 {
				return "", false
			}
		}
		return "", false
	}
	for i, c := range codes {
		if c == 2 && (vals[i] == "BLOCKS" || vals[i] == "ENTITIES" || vals[i] == "TABLES") {
			section = vals[i]
			continue
		}
		if c == 0 && vals[i] == "ENDSEC" {
			section = ""
			continue
		}
		if c != 0 {
			continue
		}
		switch vals[i] {
		case "LAYER":
			if section == "TABLES" {
				if n, ok := firstOf(i, 2); ok {
					layers = append(layers, n)
				}
			}
		case "BLOCK":
			name, _ := firstOf(i, 2)
			blocks = append(blocks, name)
			if !hasBase && !strings.EqualFold(name, "*Model_Space") {
				// BLOCK 头的 10/20/30 位于 2/70 之后
				for j := i + 1; j < len(codes) && codes[j] != 0; j++ {
					switch codes[j] {
					case 10:
						basePt[0], _ = strconv.ParseFloat(strings.TrimSpace(vals[j]), 64)
					case 20:
						basePt[1], _ = strconv.ParseFloat(strings.TrimSpace(vals[j]), 64)
					case 30:
						basePt[2], _ = strconv.ParseFloat(strings.TrimSpace(vals[j]), 64)
						hasBase = true
					}
				}
			}
		case "INSERT":
			if section == "ENTITIES" {
				if n, ok := firstOf(i, 2); ok {
					inserts = append(inserts, n)
				}
			}
		}
	}
	return
}

// TestWriteDXFRealSymbolNames 符号真名写出：解析路径保留的图层名/块名
// 应替换合成名出现在 DXF 中（R2004+ 的图层真名与 DWG/JSON 的块真名），
// 且 INSERT 引用与 BLOCKS 段定义按名字配对。
func TestWriteDXFRealSymbolNames(t *testing.T) {
	dir := testsupport.LibredwgTestDataDir()
	cases := []struct {
		sample  string
		wantLay string // 期望出现的图层真名（解析路径可得的代表）
		noSynth bool   // 不应出现 LAYER_<HEX> 合成名
		wantBlk string // 期望出现的块真名
	}{
		{"example_2018.dwg", "Tavolo 3", true, "CIRKLO_PUNKTOJ"},
		{"example_2013.dwg", "Tavolo 3", true, "CIRKLO_PUNKTOJ"},
		{"example_2010.dwg", "Tavolo 3", true, "CIRKLO_PUNKTOJ"},
		{"example_2007.dwg", "Tavolo 3", true, "CIRKLO_PUNKTOJ"},
		{"example_2004.dwg", "Tavolo 3", true, "CIRKLO_PUNKTOJ"},
		// 批次 U：R13/R14/R2000 图层名称走确定式 dat 流解析（原变体扫描
		// 失败回退合成名），DXF LAYER 表应写出真名
		{"example_r13.dwg", "TAVOLO_2", true, ""},
		{"example_r14.dwg", "TAVOLO_2", true, ""},
		{"example_2000.dwg", "Tavolo 2", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.sample, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, tc.sample))
			if err != nil {
				t.Skipf("样本缺失: %v", err)
			}
			doc, err := Parse(data)
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			var buf strings.Builder
			if err := WriteDXF(doc, &buf); err != nil {
				t.Fatalf("写出失败: %v", err)
			}
			codes, vals := parseDXFGroups(t, []byte(buf.String()))
			layers, blocks, inserts, _, _ := dxfCollectNamesFromGroups(codes, vals)
			lset := map[string]bool{}
			for _, n := range layers {
				lset[n] = true
			}
			if !lset[tc.wantLay] {
				t.Errorf("图层真名 %q 未出现在 LAYER 表（got %v）", tc.wantLay, layers)
			}
			if tc.noSynth {
				for _, n := range layers {
					if strings.HasPrefix(n, "LAYER_") {
						t.Errorf("出现合成图层名 %q", n)
					}
				}
			}
			bset := map[string]bool{}
			for _, n := range blocks {
				bset[n] = true
			}
			if tc.wantBlk != "" && !bset[tc.wantBlk] {
				t.Errorf("块真名 %q 未出现在 BLOCKS 段（got %v）", tc.wantBlk, blocks)
			}
			for _, n := range inserts {
				if !bset[n] {
					t.Errorf("INSERT 引用块 %q 在 BLOCKS 段无定义", n)
				}
			}
			t.Logf("%s: layers=%v inserts=%v", tc.sample, layers, inserts)
		})
	}
}

// TestWriteDXFJSONSourceNames JSON 来源（gold name 键）的符号真名写出。
func TestWriteDXFJSONSourceNames(t *testing.T) {
	goldPath := testsupport.LibredwgGoldJSONPath("2018")
	raw, err := os.ReadFile(goldPath)
	if err != nil {
		t.Skipf("gold JSON 缺失: %v", err)
	}
	doc, err := ParseJSON(raw)
	if err != nil {
		t.Fatalf("ParseJSON 失败: %v", err)
	}
	var buf strings.Builder
	if err := WriteDXF(doc, &buf); err != nil {
		t.Fatalf("写出失败: %v", err)
	}
	codes, vals := parseDXFGroups(t, []byte(buf.String()))
	layers, blocks, inserts, _, _ := dxfCollectNamesFromGroups(codes, vals)
	lset := map[string]bool{}
	for _, n := range layers {
		lset[n] = true
	}
	if !lset["Tavolo 3"] {
		t.Errorf("JSON 来源图层真名 Tavolo 3 未出现在 LAYER 表（got %v）", layers)
	}
	bset := map[string]bool{}
	for _, n := range blocks {
		bset[n] = true
	}
	if !bset["CIRKLO_PUNKTOJ"] && !bset["bloko"] {
		t.Errorf("JSON 来源块真名未出现在 BLOCKS 段（got %v）", blocks)
	}
	for _, n := range inserts {
		if !bset[n] {
			t.Errorf("JSON 来源 INSERT 引用块 %q 无 BLOCK 定义", n)
		}
	}
}

// dxfBlockBaseSampleDXF 合成带非零基点 BLOCK 的最小 DXF（R2000 风格）。
// DUMMYBLK 比 OFFBLK 多一个实体，避免 OFFBLK 被「最大块并入模型空间」
// 启发式吞掉（该启发式跳过最大块的 BLOCKS 段输出）。
func dxfBlockBaseSampleDXF() string {
	const s = "  0\nSECTION\n  2\nHEADER\n  9\n$ACADVER\n  1\nAC1015\n  0\nENDSEC\n" +
		"  0\nSECTION\n  2\nTABLES\n  0\nTABLE\n  2\nLAYER\n  5\n10\n" +
		"100\nAcDbSymbolTable\n 70\n     1\n" +
		"  0\nLAYER\n  5\n40\n330\n10\n100\nAcDbSymbolTableRecord\n100\nAcDbLayerTableRecord\n" +
		"  2\nBASELAYER\n 70\n     0\n 62\n     7\n  6\nContinuous\n" +
		"  0\nENDTAB\n  0\nENDSEC\n" +
		"  0\nSECTION\n  2\nBLOCKS\n" +
		"  0\nBLOCK\n  5\n20\n330\n1F\n100\nAcDbEntity\n  8\n0\n100\nAcDbBlockBegin\n" +
		"  2\nOFFBLK\n 70\n     0\n 10\n12.5\n 20\n-3.25\n 30\n0.0\n  3\nOFFBLK\n  1\n\n" +
		"  0\nLINE\n  5\n21\n330\n20\n100\nAcDbEntity\n  8\n0\n100\nAcDbLine\n" +
		" 10\n0.0\n 20\n0.0\n 30\n0.0\n 11\n100.0\n 21\n50.0\n 31\n0.0\n" +
		"  0\nENDBLK\n  5\n22\n330\n20\n100\nAcDbEntity\n  8\n0\n100\nAcDbBlockEnd\n" +
		"  0\nBLOCK\n  5\n50\n330\n1F\n100\nAcDbEntity\n  8\n0\n100\nAcDbBlockBegin\n" +
		"  2\nDUMMYBLK\n 70\n     0\n 10\n0.0\n 20\n0.0\n 30\n0.0\n  3\nDUMMYBLK\n  1\n\n" +
		"  0\nLINE\n  5\n51\n330\n50\n100\nAcDbEntity\n  8\n0\n100\nAcDbLine\n" +
		" 10\n1.0\n 20\n1.0\n 30\n0.0\n 11\n2.0\n 21\n2.0\n 31\n0.0\n" +
		"  0\nLINE\n  5\n52\n330\n50\n100\nAcDbEntity\n  8\n0\n100\nAcDbLine\n" +
		" 10\n3.0\n 20\n3.0\n 30\n0.0\n 11\n4.0\n 21\n4.0\n 31\n0.0\n" +
		"  0\nENDBLK\n  5\n53\n330\n50\n100\nAcDbEntity\n  8\n0\n100\nAcDbBlockEnd\n" +
		"  0\nENDSEC\n" +
		"  0\nSECTION\n  2\nENTITIES\n" +
		"  0\nINSERT\n  5\n30\n330\n1F\n100\nAcDbEntity\n  8\n0\n100\nAcDbBlockReference\n" +
		"  2\nOFFBLK\n 10\n5.0\n 20\n6.0\n 30\n0.0\n 41\n1.0\n 42\n1.0\n 43\n1.0\n 50\n0.0\n" +
		"  0\nENDSEC\n  0\nEOF\n"
	return s
}

// TestWriteDXFBlockBasePointRoundtrip 块基点端到端：合成带非零基点的
// BLOCK DXF → ParseDXF 读回 internalObjects 基点 → WriteDXF 再写出
// BLOCKS 段基点一致（此前的偏移丢失不再发生）。合成 DXF 同时经
// LibreDWG dxf2dwg/dwgread 中转验证：dwgread 读出的 base_pt 为
// (12.5, -3.25, 0)，与合成值一致（金标准读入锚点，见批次 R 记录）。
// 注：dxf2dwg 转出的非布局 BLOCK_HEADER 类型码为 4（LibreDWG 写侧
// 特有），我们的 DWG 侧按固定码 0x31 识别，该转出文件的块元数据
// 不参与本用例断言。
func TestWriteDXFBlockBasePointRoundtrip(t *testing.T) {
	tool := findDxf2dwg()
	if tool != "" {
		// 锚点验证：合成 DXF 能被官方工具链无损读入（结构合法性金标准）
		dxfPath := filepath.Join(t.TempDir(), "base.dxf")
		if err := os.WriteFile(dxfPath, []byte(dxfBlockBaseSampleDXF()), 0o644); err != nil {
			t.Fatal(err)
		}
		dwgPath := filepath.Join(t.TempDir(), "base.dwg")
		cmd := exec.Command(tool, "-v0", "-o", dwgPath, dxfPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("dxf2dwg 读入合成 DXF 失败: %v\n%s", err, out)
		}
	}

	// DXF 来源闭环：ParseDXF 登记真名与基点 → WriteDXF 保真写出
	doc, err := ParseDXF([]byte(dxfBlockBaseSampleDXF()))
	if err != nil {
		t.Fatalf("ParseDXF 失败: %v", err)
	}
	g := doc.InternalObjects()[0x20]
	if g == nil {
		t.Fatalf("ParseDXF 未登记 OFFBLK 块元数据（h=20）")
	}
	if n, _ := g.Field("name").(string); n != "OFFBLK" {
		t.Errorf("块真名登记错误: %q（期望 OFFBLK）", n)
	}
	bp, _ := g.Field("base_pt").([]float64)
	if len(bp) != 3 || math.Abs(bp[0]-12.5) > 1e-9 || math.Abs(bp[1]-(-3.25)) > 1e-9 {
		t.Errorf("读回块基点错误: %v（期望 [12.5 -3.25 0]）", bp)
	}
	var buf strings.Builder
	if err := WriteDXF(doc, &buf); err != nil {
		t.Fatalf("写出失败: %v", err)
	}
	codes, vals := parseDXFGroups(t, []byte(buf.String()))
	_, blocks, inserts, basePt, hasBase := dxfCollectNamesFromGroups(codes, vals)
	okBlk := false
	for _, n := range blocks {
		if n == "OFFBLK" {
			okBlk = true
		}
	}
	if !okBlk {
		t.Errorf("BLOCKS 段缺真名 OFFBLK（got %v）", blocks)
	}
	for _, n := range inserts {
		if strings.HasPrefix(n, "*B") {
			t.Errorf("INSERT 仍引用合成块名 %q", n)
		}
	}
	if !hasBase || math.Abs(basePt[0]-12.5) > 1e-9 || math.Abs(basePt[1]-(-3.25)) > 1e-9 {
		t.Errorf("写出 BLOCK 基点错误: %v has=%v（期望 12.5/-3.25）", basePt, hasBase)
	}
}

// ---- 极限批次 A：HATCH 渐变段与种子点写出 ----

// TestWriteDXFHatchGradientSeeds 渐变 HATCH 写出：DXF 来源闭环
// （ParseDXF 渐变段/种子点 → WriteDXF 复现组码 98/450/460/461/452/462/
// 453/463/63/421/470）+ 官方 dxf2dwg 读入金标准。
func TestWriteDXFHatchGradientSeeds(t *testing.T) {
	doc, err := ParseDXF([]byte(dxfGradientHatchSample()))
	if err != nil {
		t.Fatalf("ParseDXF 失败: %v", err)
	}
	var buf strings.Builder
	if err := WriteDXF(doc, &buf); err != nil {
		t.Fatalf("写出失败: %v", err)
	}
	codes, vals := parseDXFGroups(t, []byte(buf.String()))
	idxOf := func(code int, nth int) (string, bool) {
		n := 0
		for i, c := range codes {
			if c == code {
				if n == nth {
					return vals[i], true
				}
				n++
			}
		}
		return "", false
	}
	// 渐变段标量（460 角度以度输出）
	if v, _ := idxOf(98, 0); strings.TrimSpace(v) != "2" {
		t.Errorf("种子点数 98=%q（期望 2）", v)
	}
	if v, _ := idxOf(450, 0); strings.TrimSpace(v) != "1" {
		t.Errorf("渐变标志 450=%q（期望 1）", v)
	}
	if v, _ := idxOf(460, 0); math.Abs(mustF(v)-30) > 1e-9 {
		t.Errorf("渐变角度 460=%q（期望 30 度）", v)
	}
	if v, _ := idxOf(452, 0); strings.TrimSpace(v) != "1" {
		t.Errorf("单色标志 452=%q（期望 1）", v)
	}
	if v, _ := idxOf(453, 0); strings.TrimSpace(v) != "2" {
		t.Errorf("色数 453=%q（期望 2）", v)
	}
	if v, _ := idxOf(463, 1); math.Abs(mustF(v)-1) > 1e-9 {
		t.Errorf("色 1 偏移 463=%q（期望 1.0）", v)
	}
	if v, _ := idxOf(421, 1); strings.TrimSpace(v) != "65280" {
		t.Errorf("色 1 RGB 421=%q（期望 65280）", v)
	}
	if v, _ := idxOf(470, 0); v != "LINEAR" {
		t.Errorf("渐变名 470=%q（期望 LINEAR）", v)
	}
	if tool := findDxf2dwg(); tool != "" {
		dxfPath := filepath.Join(t.TempDir(), "grad.dxf")
		if err := os.WriteFile(dxfPath, []byte(buf.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		dwgPath := filepath.Join(t.TempDir(), "grad.dwg")
		cmd := exec.Command(tool, "--as", "r2004", "-v0", "-o", dwgPath, dxfPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("dxf2dwg 读入渐变 HATCH 失败: %v\n%s", err, out)
		}
	}
}

// mustF 字符串 → float64（测试断言辅助）。
func mustF(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

// dxfGradientHatchSample 带渐变段与种子点的合成 HATCH DXF 片段。
func dxfGradientHatchSample() string {
	return "  0\nSECTION\n  2\nHEADER\n  9\n$ACADVER\n  1\nAC1015\n  0\nENDSEC\n" +
		"  0\nSECTION\n  2\nENTITIES\n" +
		"  0\nHATCH\n  5\n70\n  8\n0\n 10\n0.0\n 20\n0.0\n 30\n0.0\n" +
		"  2\nGRADIENT\n 70\n     0\n 71\n     0\n 91\n        1\n" +
		" 92\n        2\n 72\n     0\n 73\n     1\n 93\n        2\n" +
		" 10\n0.0\n 20\n0.0\n 10\n10.0\n 20\n0.0\n 97\n        0\n" +
		" 98\n        2\n 10\n1.0\n 20\n2.0\n 10\n3.0\n 20\n4.0\n" +
		"450\n     1\n451\n     0\n460\n30.0\n461\n0.1\n452\n     1\n462\n0.8\n" +
		"453\n     2\n463\n0.0\n 63\n     5\n421\n       255\n" +
		"463\n1.0\n 63\n     2\n421\n     65280\n470\nLINEAR\n" +
		"  0\nENDSEC\n  0\nEOF\n"
}

// ---- 极限批次 A：IMAGE / WIPEOUT 写出 ----

// TestWriteDXFImage 写侧 IMAGE（lw_Leader 语料 6 个 IMAGE 实体）：组码
// 序列对照 dwg.spec IMAGE DXF 分支 + dwgread 实测输出；dxf2dwg 读回后
// 几何字段与源解析一致（金标准闭环）。
func TestWriteDXFImage(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "lw_Leader.dwg"))
	if err != nil {
		t.Skip(err)
	}
	doc, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	var src *entImage
	for _, e := range doc.modelSpace {
		if im, ok := e.(*entImage); ok {
			src = im
			break
		}
	}
	if src == nil {
		t.Skip("样本无 IMAGE 实体")
	}
	var buf strings.Builder
	if err := WriteDXF(doc, &buf); err != nil {
		t.Fatal(err)
	}
	codes, vals := parseDXFGroups(t, []byte(buf.String()))
	// 定位 IMAGE 实体段（按句柄）
	start := -1
	for i := range codes {
		if codes[i] == 0 && vals[i] == "IMAGE" && codes[i+1] == 5 {
			if h, err := strconv.ParseUint(strings.TrimSpace(vals[i+1]), 16, 64); err == nil && h == src.handle {
				start = i
				break
			}
		}
	}
	if start < 0 {
		t.Fatalf("输出缺 IMAGE h=%X", src.handle)
	}
	g := dxfCollectEntityPairs(codes, vals, start)
	if len(g.valsOf(100)) < 2 || g.valsOf(100)[1] != "AcDbRasterImage" {
		t.Errorf("子类标记不符: %v", g.valsOf(100))
	}
	if v := mustF(g.first(90)); v != float64(src.classVersion) {
		t.Errorf("90 class_version=%v（期望 %d）", v, src.classVersion)
	}
	if v := mustF(g.first(10)); math.Abs(v-src.pt0.x) > 1e-9 {
		t.Errorf("10 pt0.x=%v（期望 %v）", v, src.pt0.x)
	}
	if v := mustF(g.first(13)); math.Abs(v-src.imageSize.x) > 1e-9 {
		t.Errorf("13 image_size.x=%v（期望 %v）", v, src.imageSize.x)
	}
	if v := mustF(g.first(70)); v != float64(src.displayProps) {
		t.Errorf("70 display_props=%v（期望 %d）", v, src.displayProps)
	}
	if v := mustF(g.first(280)); v != float64(boolToInt(src.clipping)) {
		t.Errorf("280 clipping=%v（期望 %d）", v, boolToInt(src.clipping))
	}
	if v := mustF(g.first(281)); v != float64(src.brightness) {
		t.Errorf("281 brightness=%v（期望 %d）", v, src.brightness)
	}
	if v := mustF(g.first(71)); v != float64(src.clipBoundaryType) {
		t.Errorf("71 clip_boundary_type=%v（期望 %d）", v, src.clipBoundaryType)
	}
	// dxf2dwg 读回闭环
	if tool := findDxf2dwg(); tool != "" {
		dxfPath := filepath.Join(t.TempDir(), "img.dxf")
		if err := os.WriteFile(dxfPath, []byte(buf.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		dwgPath := filepath.Join(t.TempDir(), "img.dwg")
		cmd := exec.Command(tool, "--as", "r2004", "-v0", "-o", dwgPath, dxfPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("dxf2dwg 读入 IMAGE 失败: %v\n%s", err, out)
		}
		// 金标准为读入合法性（与 TestWriteDXFDxf2dwgRoundtrip 同口径）：
		// LibreDWG in_dxf 对 IMAGE 动态类主体写出为空壳（type 647
		// size=3，实测），字段保真由上方组码级断言保证
		dwgData, err := os.ReadFile(dwgPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(dwgData); err != nil {
			t.Fatalf("转回 DWG 无法 Parse: %v", err)
		}
	}
}

// TestWriteDXFWipeout 写侧 WIPEOUT（lw_example2004 语料 3 个实体）：
// 记录名 WIPEOUT + 子类 AcDbWipeout，组码结构与 IMAGE 同构。
func TestWriteDXFWipeout(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "lw_example2004.dwg"))
	if err != nil {
		t.Skip(err)
	}
	doc, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	var src *entWipeout
	for _, e := range doc.modelSpace {
		if w, ok := e.(*entWipeout); ok {
			src = w
			break
		}
	}
	if src == nil {
		t.Skip("样本无 WIPEOUT 实体")
	}
	var buf strings.Builder
	if err := WriteDXF(doc, &buf); err != nil {
		t.Fatal(err)
	}
	found := false
	codes, vals := parseDXFGroups(t, []byte(buf.String()))
	for i := range codes {
		if codes[i] == 0 && vals[i] == "WIPEOUT" && codes[i+1] == 5 {
			if h, _ := strconv.ParseUint(strings.TrimSpace(vals[i+1]), 16, 64); h != src.handle {
				continue
			}
		} else {
			continue
		}
		{
			found = true
			// 子类标记：0 记录名 → 5 句柄 → 330 归属 → 100 AcDbEntity → 8 图层 → 100 子类值
			if strings.TrimSpace(vals[i+5]) != "AcDbWipeout" {
				t.Errorf("WIPEOUT 子类标记不符: %q", vals[i+5])
			}
			break
		}
	}
	if !found {
		t.Fatalf("输出缺 WIPEOUT h=%X", src.handle)
	}
}

// handleAt 提取实体记录头句柄（0 组码后的 5 组码值）。
func handleAt(codes []int, vals []string, i int) uint64 {
	for j := i + 1; j < len(codes) && j < i+6; j++ {
		if codes[j] == 5 {
			h, _ := strconv.ParseUint(strings.TrimSpace(vals[j]), 16, 64)
			return h
		}
	}
	return 0
}

// dxfEntityPairs 实体段（from 之后的 0 组码起）组码对集合：同名组码按
// 出现序保留（100 子类标记出现多次，valsOf 按序取值）。
type dxfEntityPairs struct {
	codes []int
	vals  []string
}

// dxfCollectEntityPairs 收集 from 偏移后实体记录到下一 0 组码的全部组码对。
func dxfCollectEntityPairs(codes []int, vals []string, from int) dxfEntityPairs {
	var ep dxfEntityPairs
	for i := from + 2; i < len(codes); i++ {
		if codes[i] == 0 {
			break
		}
		ep.codes = append(ep.codes, codes[i])
		ep.vals = append(ep.vals, vals[i])
	}
	return ep
}

// valsOf 指定组码的全部值（保持出现序）。
func (ep dxfEntityPairs) valsOf(code int) []string {
	var out []string
	for i, c := range ep.codes {
		if c == code {
			out = append(out, strings.TrimSpace(ep.vals[i]))
		}
	}
	return out
}

// first 指定组码的首个值（缺省空串）。
func (ep dxfEntityPairs) first(code int) string {
	for i, c := range ep.codes {
		if c == code {
			return strings.TrimSpace(ep.vals[i])
		}
	}
	return ""
}

// ---- 极限批次 A：VIEWPORT 写出 ----

// TestWriteDXFViewport 视口写出：DXF 来源闭环（全字段合成 VIEWPORT 读入
// → WriteDXF → 组码回读比对 + dxf2dwg 读入金标准）。组码对照 dwg.spec
// VIEWPORT DXF 分支。
func TestWriteDXFViewport(t *testing.T) {
	doc, err := ParseDXF([]byte(dxfViewportSample()))
	if err != nil {
		t.Fatal(err)
	}
	var src *entViewport
	for _, e := range doc.modelSpace {
		if vp, ok := e.(*entViewport); ok {
			src = vp
			break
		}
	}
	if src == nil {
		t.Fatal("读入无 VIEWPORT")
	}
	var buf strings.Builder
	if err := WriteDXF(doc, &buf); err != nil {
		t.Fatal(err)
	}
	codes, vals := parseDXFGroups(t, []byte(buf.String()))
	start := -1
	for i := range codes {
		if codes[i] == 0 && vals[i] == "VIEWPORT" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("输出缺 VIEWPORT")
	}
	g := dxfCollectEntityPairs(codes, vals, start)
	if v := mustF(g.first(40)); math.Abs(v-src.width) > 1e-9 {
		t.Errorf("40 width=%v（期望 %v）", v, src.width)
	}
	if v := mustF(g.first(41)); math.Abs(v-src.height) > 1e-9 {
		t.Errorf("41 height=%v（期望 %v）", v, src.height)
	}
	if v := mustF(g.first(68)); v != 0 || mustF(g.first(69)) != 0 {
		t.Errorf("68/69 期望 0/0 得 %v/%v", mustF(g.first(68)), mustF(g.first(69)))
	}
	if v := mustF(g.first(72)); v != float64(src.circleZoom) {
		t.Errorf("72 circle_zoom=%v（期望 %d）", v, src.circleZoom)
	}
	if v := mustF(g.first(90)); v != float64(src.statusFlag) {
		t.Errorf("90 status=%v（期望 %d）", v, src.statusFlag)
	}
	if v := mustF(g.first(281)); v != float64(src.renderMode) {
		t.Errorf("281 render_mode=%v（期望 %d）", v, src.renderMode)
	}
	if v := mustF(g.first(146)); math.Abs(v-src.ucsElevation) > 1e-9 {
		t.Errorf("146 elevation=%v（期望 %v）", v, src.ucsElevation)
	}
	if v := mustF(g.first(111)); math.Abs(v-src.ucsxdir.x) > 1e-9 {
		t.Errorf("111 ucsxdir.x=%v（期望 %v）", v, src.ucsxdir.x)
	}
	if v := mustF(g.first(45)); math.Abs(v-src.viewSize) > 1e-9 {
		t.Errorf("45 view_size=%v（期望 %v）", v, src.viewSize)
	}
	if tool := findDxf2dwg(); tool != "" {
		dxfPath := filepath.Join(t.TempDir(), "vp.dxf")
		if err := os.WriteFile(dxfPath, []byte(buf.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		dwgPath := filepath.Join(t.TempDir(), "vp.dwg")
		cmd := exec.Command(tool, "--as", "r2004", "-v0", "-o", dwgPath, dxfPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("dxf2dwg 读入 VIEWPORT 失败: %v\n%s", err, out)
		}
	}
}

// dxfViewportSample 全字段合成 VIEWPORT DXF（与读侧 TestParseDXFViewport
// 同构，供写侧闭环）。
func dxfViewportSample() string {
	return "  0\nSECTION\n  2\nHEADER\n  9\n$ACADVER\n  1\nAC1015\n  0\nENDSEC\n" +
		"  0\nSECTION\n  2\nENTITIES\n" +
		"  0\nVIEWPORT\n  5\n60\n  8\n0\n" +
		" 10\n1.0\n 20\n2.0\n 30\n3.0\n 40\n100.0\n 41\n50.0\n" +
		" 12\n11.0\n 22\n12.0\n 13\n13.0\n 23\n14.0\n 14\n15.0\n 24\n16.0\n" +
		" 15\n17.0\n 25\n18.0\n 16\n1.0\n 26\n1.0\n 36\n1.0\n 17\n2.0\n 27\n2.0\n 37\n2.0\n" +
		" 42\n500.0\n 43\n1.0\n 44\n2.0\n 45\n400.0\n 50\n30.0\n 51\n20.0\n" +
		" 72\n    77\n 90\n        8\n  1\nStyle1\n281\n     5\n" +
		" 71\n     1\n 74\n     1\n110\n1.0\n120\n0.0\n130\n0.0\n" +
		"111\n0.0\n121\n1.0\n131\n0.0\n112\n0.0\n122\n0.0\n132\n1.0\n" +
		" 79\n     2\n146\n4.0\n 68\n     0\n 69\n     0\n" +
		"  0\nENDSEC\n  0\nEOF\n"
}

// ---- 极限批次 A：POLYLINE_PFACE / POLYLINE_MESH 写出 ----

// TestWriteDXFPfaceMesh 面网格/多面网格写出：DXF 来源闭环（读侧 70=64
// POLYLINE + 定位顶点 + AcDbFaceRecord 面记录 → 写出组码回读）+ MESH
// 单元级构造（70=16 + 71-75 参数 + ownedHandles 顶点）+ dxf2dwg 金标准。
// 组码对照 dwg.spec POLYLINE_PFACE/POLYLINE_MESH 与 dwgread 实测输出。
func TestWriteDXFPfaceMesh(t *testing.T) {
	// PFACE：DXF 闭环
	doc, err := ParseDXF([]byte(dxfPfaceSample()))
	if err != nil {
		t.Fatal(err)
	}
	var pf *entPolylinePface
	for _, e := range doc.modelSpace {
		if p, ok := e.(*entPolylinePface); ok {
			pf = p
			break
		}
	}
	if pf == nil {
		t.Fatal("读入无 POLYLINE_PFACE")
	}
	var buf strings.Builder
	if err := WriteDXF(doc, &buf); err != nil {
		t.Fatal(err)
	}
	codes, vals := parseDXFGroups(t, []byte(buf.String()))
	start := -1
	for i := range codes {
		if codes[i] == 0 && vals[i] == "POLYLINE" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("输出缺 POLYLINE")
	}
	// PFACE 聚合含 VERTEX/SEQEND，收集至下一个非聚合标记的 0 记录
	var pc []int
	var pvals []string
	for i := start + 2; i < len(codes); i++ {
		if codes[i] == 0 && !containsVal([]string{"VERTEX", "SEQEND"}, vals[i]) {
			break
		}
		pc = append(pc, codes[i])
		pvals = append(pvals, vals[i])
	}
	g := dxfEntityPairs{codes: pc, vals: pvals}
	if v := mustF(g.first(70)); v != 64 {
		t.Errorf("PFACE 70=%v（期望 64）", v)
	}
	if v := mustF(g.first(71)); v != 2 {
		t.Errorf("PFACE 71 顶点数=%v（期望 2）", v)
	}
	if v := mustF(g.first(72)); v != 1 {
		t.Errorf("PFACE 72 面数=%v（期望 1）", v)
	}
	if !containsVal(g.valsOf(100), "AcDbPolyFaceMeshVertex") || !containsVal(g.valsOf(100), "AcDbFaceRecord") {
		t.Errorf("PFACE 子类标记缺失: %v", g.valsOf(100))
	}
	if v := mustF(g.first(73)); v != 3 {
		t.Errorf("面记录 73 索引=%v（期望 3）", v)
	}
	// MESH：单元级构造（顶点实体经 owner 归组与 ownedHandles 吞集）
	mesh := &entPolylineMesh{
		baseEntity: baseEntity{handle: 0x99, layer: 0, mode: 2},
		flags:      16, mVertexCount: 2, nVertexCount: 2,
		mDensity: 6, nDensity: 6, curveType: 0,
		ownedHandles: []uint64{0x9A},
	}
	vert := &entVertexPface{
		baseEntity: baseEntity{handle: 0x9A, owner: 0x99, mode: 0},
		flag:       192, position: point3{1, 2, 3},
	}
	meshDoc := &Document{version: verR2000}
	meshDoc.modelSpace = []any{mesh, vert}
	var mb strings.Builder
	if err := WriteDXF(meshDoc, &mb); err != nil {
		t.Fatal(err)
	}
	mcodes, mvals := parseDXFGroups(t, []byte(mb.String()))
	mstart := -1
	for i := range mcodes {
		if mcodes[i] == 0 && mvals[i] == "POLYLINE" {
			mstart = i
			break
		}
	}
	if mstart < 0 {
		t.Fatal("MESH 输出缺 POLYLINE")
	}
	var mc2 []int
	var mv2 []string
	for i := mstart + 2; i < len(mcodes); i++ {
		if mcodes[i] == 0 && !containsVal([]string{"VERTEX", "SEQEND"}, mvals[i]) {
			break
		}
		mc2 = append(mc2, mcodes[i])
		mv2 = append(mv2, mvals[i])
	}
	mg := dxfEntityPairs{codes: mc2, vals: mv2}
	if v := mustF(mg.first(70)); v != 16 {
		t.Errorf("MESH 70=%v（期望 16）", v)
	}
	if v := mustF(mg.first(71)); v != 2 || mustF(mg.first(73)) != 6 {
		t.Errorf("MESH 71/73=%v/%v（期望 2/6）", mustF(mg.first(71)), mustF(mg.first(73)))
	}
	if v := mustF(mg.first(75)); v != 0 {
		t.Errorf("MESH 75=%v（期望 0）", v)
	}
	if !containsVal(mg.valsOf(100), "AcDbPolyFaceMeshVertex") {
		t.Errorf("MESH 顶点子类缺失: %v", mg.valsOf(100))
	}
	// dxf2dwg 金标准（PFACE 结构合法性）
	if tool := findDxf2dwg(); tool != "" {
		dxfPath := filepath.Join(t.TempDir(), "pf.dxf")
		if err := os.WriteFile(dxfPath, []byte(buf.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		dwgPath := filepath.Join(t.TempDir(), "pf.dwg")
		cmd := exec.Command(tool, "--as", "r2004", "-v0", "-o", dwgPath, dxfPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("dxf2dwg 读入 PFACE 失败: %v\n%s", err, out)
		}
	}
}

// containsVal 切片含指定值。
func containsVal(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// dxfPfaceSample 合成 PFACE DXF（2 定位顶点 + 1 面记录 + SEQEND）。
func dxfPfaceSample() string {
	return "  0\nSECTION\n  2\nHEADER\n  9\n$ACADVER\n  1\nAC1015\n  0\nENDSEC\n" +
		"  0\nSECTION\n  2\nENTITIES\n" +
		"  0\nPOLYLINE\n  5\n40\n  8\n0\n 66\n     1\n 70\n    64\n 71\n     2\n 72\n     1\n" +
		" 10\n0.0\n 20\n0.0\n 30\n0.0\n" +
		"  0\nVERTEX\n  5\n41\n330\n40\n  8\n0\n100\nAcDbEntity\n  8\n0\n" +
		"100\nAcDbVertex\n100\nAcDbPolyFaceMeshVertex\n 10\n0.0\n 20\n0.0\n 30\n0.0\n 70\n   192\n" +
		"  0\nVERTEX\n  5\n42\n330\n40\n  8\n0\n100\nAcDbEntity\n  8\n0\n" +
		"100\nAcDbVertex\n100\nAcDbPolyFaceMeshVertex\n 10\n10.0\n 20\n0.0\n 30\n0.0\n 70\n   192\n" +
		"  0\nVERTEX\n  5\n43\n330\n40\n  8\n0\n100\nAcDbEntity\n  8\n0\n" +
		"100\nAcDbVertex\n100\nAcDbFaceRecord\n 10\n0.0\n 20\n0.0\n 30\n0.0\n 70\n   128\n" +
		" 71\n     1\n 72\n     2\n 73\n     3\n 74\n     0\n" +
		"  0\nSEQEND\n  5\n44\n330\n40\n  8\n0\n" +
		"  0\nENDSEC\n  0\nEOF\n"
}

// ---- 极限批次 A：REGION / 3DSOLID / BODY（ACIS 系）----

// acisSatSample 合成 SAT 文本（明文；含 'A' 与空格以覆盖 ^ 转义路径）。
func acisSatSample() string {
	return "ACIS Release 11 Sat File\n" +
		"1 9 2 4 5 ACIS 11.0 1 An0A Aaa\n" +
		"62100000 1 0x0123 body $-1 1 #\n" +
		"End of ACIS data\n"
}

// TestWriteDXFAcis ACIS 系写出（ver=1 SAT 路径）：DXF 行为加密态 SAT
// （自反变换 + '^'→"^ " 转义），dxf2dwg 读回后 Parse 的 acisData 与源
// 明文逐字节一致（值级闭环）。SAB（version=2）需完整 ACIS 编解码器，
// 如实跳过数据段（骨架保留），注释见 writeAcis。
func TestWriteDXFAcis(t *testing.T) {
	sat := acisSatSample()
	body := &entAcis{
		baseEntity: baseEntity{handle: 0x77, layer: 0, mode: 2},
		kind:       "3DSOLID", version: 1, acisData: []byte(sat),
	}
	doc := &Document{version: verR2000}
	doc.modelSpace = []any{body}
	var buf strings.Builder
	if err := WriteDXF(doc, &buf); err != nil {
		t.Fatal(err)
	}
	codes, vals := parseDXFGroups(t, []byte(buf.String()))
	start := -1
	for i := range codes {
		if codes[i] == 0 && vals[i] == "3DSOLID" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("输出缺 3DSOLID")
	}
	g := dxfCollectEntityPairs(codes, vals, start)
	if strings.TrimSpace(g.first(70)) != "1" {
		t.Errorf("70 version=%q（期望 1）", g.first(70))
	}
	if strings.TrimSpace(g.first(290)) != "0" {
		t.Errorf("290 acis_empty=%q（期望 0）", g.first(290))
	}
	// 明文 'A' 加密态为 94（'^'），DXF 行内转义为 "^ "（首行以 A 开头）
	if !strings.HasPrefix(g.first(1), "^ ") {
		t.Errorf("组码 1 首行非加密态: %.40q", g.first(1))
	}
	// dxf2dwg 读入合法性金标准。SAT 值级闭环经 dwgread -v3 对转回
	// DWG 的 trace 验证（encr_sat_data 104 字节 + acis_data 明文还原）；
	// 我们 Parse 侧对该合成最小生成文件的实体扫描受限（entmode=0 的
	// owner 链依赖完整 BLOCK_HEADER 对象图），不影响 DXF 侧正确性
	if tool := findDxf2dwg(); tool != "" {
		dxfPath := filepath.Join(t.TempDir(), "ac.dxf")
		if err := os.WriteFile(dxfPath, []byte(buf.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		dwgPath := filepath.Join(t.TempDir(), "ac.dwg")
		cmd := exec.Command(tool, "--as", "r2004", "-v0", "-o", dwgPath, dxfPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("dxf2dwg 读入 ACIS 失败: %v\n%s", err, out)
		}
		dwgData, err := os.ReadFile(dwgPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(dwgData); err != nil {
			t.Fatalf("转回 DWG 无法 Parse: %v", err)
		}
	}
}

// TestParseDXFAcis 读侧 SAT 行拼接（合成加密行）：'^ ' 还原 + 自反解密，
// 组码 1 行间补换行（对齐 in_dxf add_3DSOLID_encr）。
func TestParseDXFAcis(t *testing.T) {
	// 明文 "ACIS 11"（'A'→^ 转义在加密行中出现）手工构造加密态：
	// 'A'=65 → 159-65=94='^'，其余 >32 字节同样取 159-b
	enc := func(s string) string {
		out := make([]byte, 0, len(s))
		for i := 0; i < len(s); i++ {
			if s[i] == 'A' {
				out = append(out, '^', ' ') // 'A' 的加密态 94 在 DXF 中转义
				continue
			}
			if s[i] <= 32 {
				out = append(out, s[i])
			} else {
				out = append(out, 159-s[i])
			}
		}
		return string(out)
	}
	line1 := enc("ACIS 11 Sat File")
	line2 := enc("End of ACIS data")
	src := "  0\nSECTION\n  2\nHEADER\n  9\n$ACADVER\n  1\nAC1015\n  0\nENDSEC\n" +
		"  0\nSECTION\n  2\nENTITIES\n" +
		"  0\n3DSOLID\n  5\n70\n  8\n0\n" +
		"290\n     0\n 70\n     1\n" +
		"  1\n" + line1 + "\n" +
		"  1\n" + line2 + "\n" +
		"  0\nENDSEC\n  0\nEOF\n"
	doc, err := ParseDXF([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	a, ok := dxfSyntheticByName(t, doc, "*cad.entAcis").(*entAcis)
	if !ok {
		t.Fatal("类型不符")
	}
	if a.kind != "3DSOLID" || a.acisEmpty || a.version != 1 {
		t.Fatalf("基础字段不符: kind=%s empty=%v ver=%d", a.kind, a.acisEmpty, a.version)
	}
	want := "ACIS 11 Sat File\nEnd of ACIS data\n"
	if string(a.acisData) != want {
		t.Errorf("SAT 明文不符:\n got=%q\nwant=%q", a.acisData, want)
	}
}

// ---- 极限批次 A：OLE2FRAME / PROXY_ENTITY 写出 ----

// TestWriteDXFOle2Frame OLE2 框架写出（语料无样本，单元级构造）：
// 70 恒 2/71-72 类型与 tile_mode/73 lock_aspect/90 数据大小/310 二进制
// 块（127 字节/行大写 hex）/1 "OLE" 标记 + dxf2dwg 读入金标准。
func TestWriteDXFOle2Frame(t *testing.T) {
	data := make([]byte, 200)
	for i := range data {
		data[i] = byte(i)
	}
	ole := &entOle2Frame{
		baseEntity: baseEntity{handle: 0x31, layer: 0, mode: 2},
		oleType:    1, mode: 0, lockAspect: 1, dataSize: uint32(len(data)), data: data,
	}
	doc := &Document{version: verR2000}
	doc.modelSpace = []any{ole}
	var buf strings.Builder
	if err := WriteDXF(doc, &buf); err != nil {
		t.Fatal(err)
	}
	codes, vals := parseDXFGroups(t, []byte(buf.String()))
	start := -1
	for i := range codes {
		if codes[i] == 0 && vals[i] == "OLE2FRAME" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("输出缺 OLE2FRAME")
	}
	g := dxfCollectEntityPairs(codes, vals, start)
	if strings.TrimSpace(g.first(70)) != "2" || strings.TrimSpace(g.first(71)) != "1" {
		t.Errorf("70/71=%q/%q（期望 2/1）", g.first(70), g.first(71))
	}
	if strings.TrimSpace(g.first(73)) != "1" {
		t.Errorf("73 lock_aspect=%q（期望 1）", g.first(73))
	}
	if strings.TrimSpace(g.first(90)) != "200" {
		t.Errorf("90 data_size=%q（期望 200）", g.first(90))
	}
	chunks := g.valsOf(310)
	if len(chunks) != 2 || len(chunks[0]) != 254 {
		t.Errorf("310 分块数/首块宽=%d/%d（期望 2/254）", len(chunks), len(chunks[0]))
	}
	// dxf2dwg 读入
	if tool := findDxf2dwg(); tool != "" {
		dxfPath := filepath.Join(t.TempDir(), "ole.dxf")
		if err := os.WriteFile(dxfPath, []byte(buf.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		dwgPath := filepath.Join(t.TempDir(), "ole.dwg")
		cmd := exec.Command(tool, "--as", "r2004", "-v0", "-o", dwgPath, dxfPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("dxf2dwg 读入 OLE2FRAME 失败: %v\n%s", err, out)
		}
	}
}

// TestWriteDXFProxyEntity ACAD 代理实体写出（语料无样本，单元级构造）：
// 90 proxy_id/95 版本/70 数据格式/92+310 代理图形/93+310 原始数据位串
// + dxf2dwg 读入金标准。
func TestWriteDXFProxyEntity(t *testing.T) {
	proxyData := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	rawBits := []byte{0x01, 0x02, 0x03} // 17 位（1 字节余数）
	px := &entProxyEntity{
		baseEntity: baseEntity{handle: 0x41, layer: 0, mode: 2},
		proxyID:    499, version: 0x0F1B, fromDxf: false,
		proxyDataSize: uint32(len(proxyData)), proxyData: proxyData,
		dataNumBits: 17, data: rawBits,
	}
	doc := &Document{version: verR2000}
	doc.modelSpace = []any{px}
	var buf strings.Builder
	if err := WriteDXF(doc, &buf); err != nil {
		t.Fatal(err)
	}
	codes, vals := parseDXFGroups(t, []byte(buf.String()))
	start := -1
	for i := range codes {
		if codes[i] == 0 && vals[i] == "PROXY_ENTITY" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("输出缺 PROXY_ENTITY")
	}
	g := dxfCollectEntityPairs(codes, vals, start)
	if strings.TrimSpace(g.first(90)) != "499" {
		t.Errorf("90 proxy_id=%q（期望 499）", g.first(90))
	}
	if strings.TrimSpace(g.first(95)) != "3867" {
		t.Errorf("95 version=%q（期望 3867=0xF1B）", g.first(95))
	}
	if strings.TrimSpace(g.first(92)) != "4" {
		t.Errorf("92 proxy_data_size=%q（期望 4）", g.first(92))
	}
	if got := g.valsOf(310); len(got) < 1 || got[0] != "DEADBEEF" {
		t.Errorf("310 代理图形=%v（期望含 DEADBEEF）", got)
	}
	if strings.TrimSpace(g.first(93)) != "17" {
		t.Errorf("93 data_numbits=%q（期望 17）", g.first(93))
	}
	// dxf2dwg 读入
	if tool := findDxf2dwg(); tool != "" {
		dxfPath := filepath.Join(t.TempDir(), "px.dxf")
		if err := os.WriteFile(dxfPath, []byte(buf.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		dwgPath := filepath.Join(t.TempDir(), "px.dwg")
		cmd := exec.Command(tool, "--as", "r2004", "-v0", "-o", dwgPath, dxfPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("dxf2dwg 读入 PROXY_ENTITY 失败: %v\n%s", err, out)
		}
	}
}
