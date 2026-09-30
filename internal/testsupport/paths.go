// paths.go 测试数据定位助手：gold JSON 与 LibreDWG 参考样本目录的环境
// 变量覆盖与默认回退顺序，供各子包门禁测试统一取径。
package testsupport

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// libredwgTestDataDir 相对候选基于模块根目录解析（测试工作目录随所在包
// 变化，直接用相对路径会在子包测试下失效；历史口径为模块根 + ../../../）。
func moduleRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	// file = <模块根>/internal/testsupport/paths.go
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// LibredwgGoldJSONPath 返回 LibreDWG gold JSON 对照文件路径（可经
// CAD_LIBREDWG_JSON / CAD_GOLD_JSON_DIR 环境变量覆盖）。
func LibredwgGoldJSONPath(version string) string {
	if p := os.Getenv("CAD_LIBREDWG_JSON"); p != "" {
		return p
	}
	name := "ex" + version + ".json"
	if dir := os.Getenv("CAD_GOLD_JSON_DIR"); dir != "" {
		return dir + "/" + name
	}
	return "/tmp/" + name
}

// LibredwgTestDataDir 返回 LibreDWG 参考样本目录（可经 CAD_LIBREDWG_DATA
// 环境变量覆盖；默认按模块根历史相对口径与 /tmp 回退逐个探测）。
func LibredwgTestDataDir() string {
	if dir := os.Getenv("CAD_LIBREDWG_DATA"); dir != "" {
		return dir
	}
	for _, dir := range []string{
		filepath.Join(moduleRoot(), "../../../.reference/libredwg/test/test-data"),
		"/tmp/libredwg/test/test-data",
	} {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	return ""
}

// requirePreR13Sample 返回样本字节；语料不可用时跳过测试。
func RequirePreR13Sample(t *testing.T, rel string) []byte {
	t.Helper()
	path := filepath.Join(LibredwgTestDataDir(), rel)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("pre-R13 样本不可用: %v", err)
	}
	return data
}

// parsePreR13Gold 用 LibreDWG dwgread 生成样本 gold JSON（不可用时跳过）。
func ParsePreR13Gold(t *testing.T, dwgPath string) string {
	t.Helper()
	dwgread, err := exec.LookPath("/tmp/libredwg-build/dwgread")
	if err != nil {
		if dwgread, err = exec.LookPath("dwgread"); err != nil {
			t.Skip("dwgread 不可用，跳过 gold 对照")
		}
	}
	out := filepath.Join(t.TempDir(), "gold.json")
	cmd := exec.Command(dwgread, "-O", "JSON", "-o", out, dwgPath)
	if err := cmd.Run(); err != nil {
		t.Skipf("dwgread 生成 gold 失败: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Skipf("读取 gold 失败: %v", err)
	}
	return string(data)
}

// resolveSamplePath 由 gold 别名推导 test-data 相对路径。三条规则：
// ex*→example_*.dwg、sample*→sample_*.dwg（批次 0 根目录样本）；
// c_<目录>_<文件名>→<目录>/<文件名>.dwg（批次 I 语料扩容样本，文件名
// 保留原始大小写，目录取别名首个下划线前的版本段）。
func ResolveSamplePath(alias string) string {
	base := alias + ".json"
	if len(base) > 4 && base[:2] == "ex" {
		return "example_" + base[2:len(base)-5] + ".dwg"
	}
	if len(base) > 10 && base[:6] == "sample" {
		return "sample_" + base[6:len(base)-5] + ".dwg"
	}
	if len(alias) > 2 && alias[:2] == "c_" {
		if i := strings.Index(alias[2:], "_"); i >= 0 {
			return alias[2:2+i] + "/" + alias[3+i:] + ".dwg"
		}
	}
	return ""
}
