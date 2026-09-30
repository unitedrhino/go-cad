// encode_file_r2010_test.go 是 R2010/R2013/R2018（AC1024/AC1027/AC1032）文件级
// 回放写出的门禁：三代与 R2004 同容器（LCG 加密头 + 页表 + 压缩段），写出走
// WriteDwgR2004，对每版本样本执行 Parse → WriteDwgR2004 → 再 Parse，断言版本、
// 实体数/模型空间图元逐字段、图层集合不变；对象图条目（句柄+段内偏移）随
// AcDbObjects/Handles 段原样回放而逐条相等。R2010+ 特有段（如 AcDs 系）随
// 段表原样回放。LibreDWG 交叉验证（dwgread）由 CAD_LIBREDWG_BUILD 环境变量
// 门控，默认环境 skip 不影响全绿。
package cad

import (
	"bytes"
	"github.com/unitedrhino/go-cad/internal/objrec"
	"os"
	"path/filepath"
	"testing"
)

// r2010FamilyGateSamples 按版本分组的门禁样本：R2010/R2013 覆盖全部图元
// 类型样本，R2018 覆盖 libredwg 样本与工程图（01-1/01-2）。
var r2010FamilyGateSamples = map[string][]string{
	"AC1024": {
		"arc_2010.dwg", "circle_2010.dwg", "ellipse_2010.dwg", "line_2010.dwg",
		"lw_example2010.dwg", "point2d_2010.dwg", "point3d_2010.dwg",
		"polyline2d_line_2010.dwg",
	},
	"AC1027": {
		"arc_2013.dwg", "circle_2013.dwg", "ellipse_2013.dwg", "line_2013.dwg",
		"lw_example2013.dwg", "point2d_2013.dwg", "point3d_2013.dwg",
		"polyline2d_line_2013.dwg",
	},
	"AC1032": {
		"lw_example2018.dwg", "lw_Leader.dwg", "lw_example2018.dwg",
		"lw_Leader.dwg", "lw_PolyLine3D.dwg", "lw_Spline.dwg",
	},
}

// writeSampleR2010Family 对样本执行 Parse → WriteDwgR2004（R2010+ 与 R2004
// 同容器写出器），返回源字节与写出字节。
func writeSampleR2010Family(t *testing.T, name string) (original, written []byte) {
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
	if doc.r2004Raw == nil {
		t.Fatalf("%s 未保留 R2004 家族回放素材", name)
	}
	buf := &bytes.Buffer{}
	if err := WriteDwgR2004(doc, buf); err != nil {
		t.Fatalf("写出 %s 失败: %v", name, err)
	}
	return original, buf.Bytes()
}

// TestWriteReadR2010Family R2010/R2013/R2018 回放门禁主测试：写出文件须被
// 本包完整重新解析且语义不变（与 TestWriteReadR2004 同一套不变量）。
func TestWriteReadR2010Family(t *testing.T) {
	for ver, samples := range r2010FamilyGateSamples {
		t.Run(ver, func(t *testing.T) {
			for _, name := range samples {
				t.Run(name, func(t *testing.T) {
					original, written := writeSampleR2010Family(t, name)
					if string(written[:6]) != ver {
						t.Fatalf("写出版本串错误: %q，期望 %q", string(written[:6]), ver)
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
					// ④ 模型空间图元逐字段一致（句柄/类型/几何/图层/颜色）
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
					refs1, refs2 := r2010FamilyGateMapRefs(t, original), r2010FamilyGateMapRefs(t, written)
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
		})
	}
}

// r2010FamilyGateMapRefs 解析样本的对象图条目（R2010+ 容器同用 AcDb:Handles 段）。
func r2010FamilyGateMapRefs(t *testing.T, data []byte) []objrec.ObjectRef {
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

// TestWriteReadR2010FamilyLibreDWG LibreDWG 交叉验证（可选门控）：把写出的
// 文件喂给 dwgread -v3，要求退出码为 0，error 行数与对象数与原文件基线一致
// （源文件自带的 error 基线按行数对称比较）。设置 CAD_LIBREDWG_BUILD=<构建
// 目录>（如 /tmp/libredwg-build）启用。
func TestWriteReadR2010FamilyLibreDWG(t *testing.T) {
	buildDir := os.Getenv("CAD_LIBREDWG_BUILD")
	if buildDir == "" {
		t.Skip("未设置 CAD_LIBREDWG_BUILD，跳过 LibreDWG 交叉验证")
	}
	dwgread := filepath.Join(buildDir, "dwgread")
	for ver, samples := range r2010FamilyGateSamples {
		t.Run(ver, func(t *testing.T) {
			for _, name := range samples {
				t.Run(name, func(t *testing.T) {
					original, written := writeSampleR2010Family(t, name)
					dir := t.TempDir()
					outPath := filepath.Join(dir, "written_"+name)
					if err := os.WriteFile(outPath, written, 0o644); err != nil {
						t.Fatalf("写出临时文件失败: %v", err)
					}
					baseObjs, baseErrs := r2004GateDwgread(t, dwgread, mustTempFile(t, original))
					newObjs, newErrs := r2004GateDwgread(t, dwgread, outPath)
					if newErrs != baseErrs {
						t.Errorf("dwgread error 行数不一致: 写出 %d != 基线 %d", newErrs, baseErrs)
					}
					if newObjs != baseObjs {
						t.Errorf("dwgread 对象数不一致: %d != %d", newObjs, baseObjs)
					}
				})
			}
		})
	}
}
