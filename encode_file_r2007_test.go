// encode_file_r2007_test.go 是 WriteDwgR2007 文件级回放的门禁：对 R2007
// 样本执行 Parse → WriteDwgR2007 → 再 Parse，断言版本、实体数/模型空间
// 图元逐字段、图层集合不变；对象图条目（句柄+段内偏移）随 AcDbObjects/
// Handles 段原样回放而逐条相等。LibreDWG 交叉验证（dwgread）由
// CAD_LIBREDWG_BUILD 环境变量门控，默认环境 skip 不影响全绿。
package cad

import (
	"bytes"
	"github.com/unitedrhino/go-cad/internal/objrec"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// r2007GateSamples 门禁样本：覆盖 R2007 testdata 全部图元类型
// （ARC/CIRCLE/ELLIPSE/LINE/LWPOLYLINE/POINT/POLYLINE2D）。
var r2007GateSamples = []string{
	"arc_2007.dwg", "circle_2007.dwg", "ellipse_2007.dwg",
	"line_2007.dwg", "lw_example2007.dwg", "point2d_2007.dwg",
	"point3d_2007.dwg", "polyline2d_line_2007.dwg",
}

// writeSampleR2007 对样本执行 Parse → WriteDwgR2007，返回源字节与写出字节。
func writeSampleR2007(t *testing.T, name string) (original, written []byte) {
	t.Helper()
	var err error
	original, err = os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("读取样本 %s 失败: %v", name, err)
	}
	doc, err := Parse(original)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", name, err)
	}
	if doc.r2007Raw == nil {
		t.Fatalf("%s 未保留 R2007 回放素材", name)
	}
	buf := &bytes.Buffer{}
	if err := WriteDwgR2007(doc, buf); err != nil {
		t.Fatalf("写出 %s 失败: %v", name, err)
	}
	return original, buf.Bytes()
}

// TestWriteReadR2007 回放门禁主测试：写出文件须被本包完整重新解析且
// 语义不变。
func TestWriteReadR2007(t *testing.T) {
	for _, name := range r2007GateSamples {
		t.Run(name, func(t *testing.T) {
			original, written := writeSampleR2007(t, name)
			if string(written[:6]) != "AC1021" {
				t.Fatalf("写出版本串错误: %q", string(written[:6]))
			}
			doc1, err := Parse(original)
			if err != nil {
				t.Fatalf("源文件解析失败: %v", err)
			}
			doc2, err := Parse(written)
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
			// ④ 模型空间图元逐字段一致：DumpEntities 覆盖句柄、类型、几何
			// 关键值、图层与颜色，JSON 全等即为不变量
			d1, err := DumpEntities(original)
			if err != nil {
				t.Fatalf("源文件实体导出失败: %v", err)
			}
			d2, err := DumpEntities(written)
			if err != nil {
				t.Fatalf("写出文件实体导出失败: %v", err)
			}
			if d1 != d2 {
				t.Errorf("模型空间图元不一致:\n源: %s\n写: %s", d1, d2)
			}
			// ⑤ 图层集合一致（句柄 → 颜色索引/真彩色）
			if len(doc2.layerColors) != len(doc1.layerColors) {
				t.Errorf("图层数不一致: %d != %d", len(doc2.layerColors), len(doc1.layerColors))
			}
			for h, lc1 := range doc1.layerColors {
				lc2, ok := doc2.layerColors[h]
				if !ok {
					t.Errorf("写出文件缺少图层句柄 %d", h)
					continue
				}
				if lc1 != lc2 {
					t.Errorf("图层 %d 颜色不一致: %+v != %+v", h, lc2, lc1)
				}
			}
			// ⑥ 对象图条目逐条相等：AcDbObjects/Handles 段原样回放，
			// 句柄与段内偏移都不随容器重建变化
			refs1, refs2 := r2007GateMapRefs(t, original), r2007GateMapRefs(t, written)
			if len(refs1) != len(refs2) {
				t.Fatalf("对象图条目数不一致: %d != %d", len(refs2), len(refs1))
			}
			for i := range refs1 {
				if refs1[i] != refs2[i] {
					t.Errorf("条目 %d 不一致: (%d,%d) != (%d,%d)",
						i, refs2[i].Handle, refs2[i].Offset, refs1[i].Handle, refs1[i].Offset)
				}
			}
		})
	}
}

// r2007GateMapRefs 解析样本的对象图条目（R2007 容器 AcDb:Handles 段）。
func r2007GateMapRefs(t *testing.T, data []byte) []objrec.ObjectRef {
	t.Helper()
	handles, err := loadNamedSectionData(data, "AcDb:Handles")
	if err != nil {
		t.Fatalf("加载 AcDb:Handles 段失败: %v", err)
	}
	refs, err := objrec.ParseObjectMapHandles(handles)
	if err != nil {
		t.Fatalf("解析对象图失败: %v", err)
	}
	return refs
}

// TestWriteReadR2007LibreDWG LibreDWG 交叉验证（可选门控）：把写出的文件
// 喂给 dwgread -v3，要求退出码为 0、ERROR 行数与原文件基线一致，且对象数
// 与基线一致。设置 CAD_LIBREDWG_BUILD=<构建目录>（如 /tmp/libredwg-build）
// 启用。
func TestWriteReadR2007LibreDWG(t *testing.T) {
	buildDir := os.Getenv("CAD_LIBREDWG_BUILD")
	if buildDir == "" {
		t.Skip("未设置 CAD_LIBREDWG_BUILD，跳过 LibreDWG 交叉验证")
	}
	dwgread := filepath.Join(buildDir, "dwgread")
	for _, name := range r2007GateSamples {
		t.Run(name, func(t *testing.T) {
			original, written := writeSampleR2007(t, name)
			dir := t.TempDir()
			outPath := filepath.Join(dir, "written_"+name)
			if err := os.WriteFile(outPath, written, 0o644); err != nil {
				t.Fatalf("写出临时文件失败: %v", err)
			}
			baseObjs, baseErrs := r2007GateDwgread(t, dwgread, mustTempFile(t, original))
			newObjs, newErrs := r2007GateDwgread(t, dwgread, outPath)
			if newErrs != baseErrs {
				t.Errorf("dwgread error 行数不一致: 写出 %d != 基线 %d", newErrs, baseErrs)
			}
			if newObjs != baseObjs {
				t.Errorf("dwgread 对象数不一致: %d != %d", newObjs, baseObjs)
			}
		})
	}
}

// r2007GateDwgread 执行 dwgread -v3，返回 (num_objects, ERROR 级日志行数)。
// num_objects 缺失时返回 -1（与基线对称缺失比较）。
func r2007GateDwgread(t *testing.T, dwgread, path string) (int, int) {
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
	return objects, errLines
}
