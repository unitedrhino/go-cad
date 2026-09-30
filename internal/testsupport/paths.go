// paths.go 测试数据定位助手：gold JSON 与 LibreDWG 参考样本目录的环境
// 变量覆盖与默认回退顺序，供各子包门禁测试统一取径。
package testsupport

import (
	"os"
	"path/filepath"
	"runtime"
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
