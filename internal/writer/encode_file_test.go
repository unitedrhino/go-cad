// encode_file_test.go 是 WriteDwgR2000 文件级回放的门禁：对样本执行
// Parse → WriteDwgR2000 → 再 Parse，断言版本、实体数/类型分布、模型空间
// 图元句柄与几何关键值、图层集合不变；对象图条目句柄序列不变、偏移整体
// 平移。LibreDWG 交叉验证（dwgread）由 CAD_LIBREDWG_BUILD 环境变量门控，
// 默认环境 skip 不影响全绿。
package writer

import (
	"bytes"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/drawing"
	"github.com/unitedrhino/go-cad/internal/objrec"
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// r2000GateSamples 门禁样本：覆盖 LINE/TEXT/MTEXT 三类几何与文本图元。
var r2000GateSamples = []string{"line_2000.dwg", "text_2000.dwg", "mtext_2000.dwg"}

// writeSampleR2000 对样本执行 Parse → WriteDwgR2000，返回源字节与写出字节。
func writeSampleR2000(t *testing.T, name string) (original, written []byte) {
	t.Helper()
	var err error
	original, err = os.ReadFile(testsupport.TestdataPath(name))
	if err != nil {
		t.Fatalf("读取样本 %s 失败: %v", name, err)
	}
	doc, err := drawing.Parse(original)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", name, err)
	}
	buf := &bytes.Buffer{}
	if err := WriteDwgR2000(doc, buf); err != nil {
		t.Fatalf("写出 %s 失败: %v", name, err)
	}
	return original, buf.Bytes()
}

// TestWriteReadR2000 回放门禁主测试：写出文件须被本包完整重新解析且
// 语义不变。
func TestWriteReadR2000(t *testing.T) {
	for _, name := range r2000GateSamples {
		t.Run(name, func(t *testing.T) {
			original, written := writeSampleR2000(t, name)
			if string(written[:6]) != "AC1015" {
				t.Fatalf("写出版本串错误: %q", string(written[:6]))
			}
			doc1, err := drawing.Parse(original)
			if err != nil {
				t.Fatalf("源文件解析失败: %v", err)
			}
			doc2, err := drawing.Parse(written)
			if err != nil {
				t.Fatalf("写出文件重新解析失败: %v", err)
			}
			// ① 版本一致
			if doc2.Version() != doc1.Version() {
				t.Errorf("版本不一致: %s != %s", doc2.Version(), doc1.Version())
			}
			// ② 跳过对象数一致（回放不丢记录、不新增坏记录）
			if doc2.Skipped() != doc1.Skipped() {
				t.Errorf("跳过对象数不一致: %d != %d", doc2.Skipped(), doc1.Skipped())
			}
			// ③ 实体总数一致（模型空间 + 块内容）
			if doc2.EntityCount() != doc1.EntityCount() {
				t.Errorf("实体数不一致: %d != %d", doc2.EntityCount(), doc1.EntityCount())
			}
			// ④ 模型空间图元逐字段一致：DumpEntities 覆盖句柄、类型、
			// 几何关键值（LINE start/end、CIRCLE 圆心半径等）、图层与颜色，
			// JSON 输出对 map 键排序、对实体序列敏感，整体比对即为不变量
			d1, err := drawing.DumpEntities(original)
			if err != nil {
				t.Fatalf("源文件实体导出失败: %v", err)
			}
			d2, err := drawing.DumpEntities(written)
			if err != nil {
				t.Fatalf("写出文件实体导出失败: %v", err)
			}
			if d1 != d2 {
				t.Errorf("模型空间图元不一致:\n源: %s\n写: %s", d1, d2)
			}
			// ⑤ 图层集合一致（句柄 → 颜色索引/真彩色）
			if len(doc2.LayerColors) != len(doc1.LayerColors) {
				t.Errorf("图层数不一致: %d != %d", len(doc2.LayerColors), len(doc1.LayerColors))
			}
			for h, lc1 := range doc1.LayerColors {
				lc2, ok := doc2.LayerColors[h]
				if !ok {
					t.Errorf("写出文件缺少图层句柄 %d", h)
					continue
				}
				if lc1 != lc2 {
					t.Errorf("图层 %d 颜色不一致: %+v != %+v", h, lc2, lc1)
				}
			}
			// ⑥ 对象图条目：句柄序列不变，偏移整体平移（回放布局基址差）
			refs1, refs2 := r2000GateMapRefs(t, original), r2000GateMapRefs(t, written)
			if len(refs1) != len(refs2) {
				t.Fatalf("对象图条目数不一致: %d != %d", len(refs2), len(refs1))
			}
			delta := int64(refs2[0].Offset) - int64(refs1[0].Offset)
			for i := range refs1 {
				if refs1[i].Handle != refs2[i].Handle {
					t.Errorf("条目 %d 句柄不一致: %d != %d", i, refs2[i].Handle, refs1[i].Handle)
				}
				if got := int64(refs2[i].Offset) - int64(refs1[i].Offset); got != delta {
					t.Errorf("条目 %d 偏移平移不均匀: %d != %d", i, got, delta)
				}
			}
		})
	}
}

// r2000GateMapRefs 解析样本的对象图条目（R2000 容器 2 号段）。
func r2000GateMapRefs(t *testing.T, data []byte) []objrec.ObjectRef {
	t.Helper()
	omap, err := container.ReadR2000Section(data, container.R2000SecObjectMap)
	if err != nil {
		t.Fatalf("加载对象图失败: %v", err)
	}
	refs, err := objrec.ParseObjectMapHandles(omap)
	if err != nil {
		t.Fatalf("解析对象图失败: %v", err)
	}
	return refs
}

// TestBuildR2000ObjectMapChunks 对象图重建单元测试：合成多块规模的条目，
// 经 parseObjectMapHandles 回读须逐条还原；块 size 字段不得超过读侧
// 2040 上限；偏移平移（负 baseDelta）同样保真。
func TestBuildR2000ObjectMapChunks(t *testing.T) {
	build := func(n int, base uint32) []objrec.ObjectRef {
		refs := make([]objrec.ObjectRef, 0, n)
		h, off := uint64(0x30), base
		for i := 0; i < n; i++ {
			refs = append(refs, objrec.ObjectRef{Handle: h, Offset: off})
			h += 2
			off += 48 + uint32(i%7)*8 // 变长记录间隔，覆盖多种 MC 编码长度
		}
		return refs
	}
	for _, tc := range []struct {
		name  string
		n     int
		base  uint32
		delta int64
	}{
		{"单块", 100, 0x1000, 0},
		{"多块", 800, 0x1000, 0},
		{"负平移", 800, 0x1C000, -17000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refs := build(tc.n, tc.base)
			stream := buildR2000ObjectMap(refs, tc.delta)
			// 块尺寸合法性：逐块检查 BE size ≤ 2040（读侧硬上限）
			pos := 0
			chunks := 0
			for pos+2 <= len(stream) {
				size := int(stream[pos])<<8 | int(stream[pos+1])
				if size == 2 {
					chunks++
					break
				}
				if size > 2040 {
					t.Fatalf("块 %d size=%d 超过读侧 2040 上限", chunks, size)
				}
				chunks++
				pos += 2 + size
			}
			if tc.n >= 800 && chunks < 3 {
				t.Errorf("预期多块（含终止块），实际 %d 块", chunks)
			}
			got, err := objrec.ParseObjectMapHandles(stream)
			if err != nil {
				t.Fatalf("重建流解析失败: %v", err)
			}
			if len(got) != len(refs) {
				t.Fatalf("条目数不一致: %d != %d", len(got), len(refs))
			}
			for i := range refs {
				wantOff := uint32(int64(refs[i].Offset) + tc.delta)
				if got[i].Handle != refs[i].Handle || got[i].Offset != wantOff {
					t.Fatalf("条目 %d 不一致: got (%d,%d) want (%d,%d)",
						i, got[i].Handle, got[i].Offset, refs[i].Handle, wantOff)
				}
			}
		})
	}
}

// TestWriteReadR2000LibreDWG LibreDWG 交叉验证（可选门控）：把写出的文件
// 喂给 dwgread -v3，要求退出码为 0、无 error 级输出，且对象数与原文件
// 基线一致。设置 CAD_LIBREDWG_BUILD=<构建目录>（如 /tmp/libredwg-build）启用。
func TestWriteReadR2000LibreDWG(t *testing.T) {
	buildDir := os.Getenv("CAD_LIBREDWG_BUILD")
	if buildDir == "" {
		t.Skip("未设置 CAD_LIBREDWG_BUILD，跳过 LibreDWG 交叉验证")
	}
	dwgread := filepath.Join(buildDir, "dwgread")
	for _, name := range r2000GateSamples {
		t.Run(name, func(t *testing.T) {
			original, written := writeSampleR2000(t, name)
			dir := t.TempDir()
			outPath := filepath.Join(dir, "written_"+name)
			if err := os.WriteFile(outPath, written, 0o644); err != nil {
				t.Fatalf("写出临时文件失败: %v", err)
			}
			baseObjs, baseErrs := runDwgreadGate(t, dwgread, mustTempFile(t, original))
			newObjs, newErrs := runDwgreadGate(t, dwgread, outPath)
			if newErrs != baseErrs {
				t.Errorf("dwgread error 行数不一致: 写出 %d != 基线 %d", newErrs, baseErrs)
			}
			if newObjs != baseObjs {
				t.Errorf("dwgread 对象数不一致: %d != %d", newObjs, baseObjs)
			}
		})
	}
}

// mustTempFile 落盘源字节供 dwgread 读取基线。
func mustTempFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "original.dwg")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("写临时文件失败: %v", err)
	}
	return path
}

// runDwgreadGate 执行 dwgread -v3，返回 (num_objects, error 级日志行数)。
func runDwgreadGate(t *testing.T, dwgread, path string) (int, int) {
	t.Helper()
	cmd := exec.Command(dwgread, "-v3", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dwgread 执行失败: %v\n%s", err, tailLines(string(out), 20))
	}
	errLines := 0
	objects := -1
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "ERROR") {
			errLines++
			t.Logf("dwgread error: %s", line)
		}
		if n, ok := strings.CutPrefix(line, "num_objects:"); ok {
			if v, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
				objects = v
			}
		}
	}
	if objects < 0 {
		t.Fatalf("dwgread 输出缺少 num_objects:\n%s", tailLines(string(out), 20))
	}
	return objects, errLines
}

// tailLines 取文本末尾若干行（失败信息摘录用）。
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
