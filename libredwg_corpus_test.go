// 本文件提供 LibreDWG 官方测试语料的统一路径解析逻辑（test/test-data
// 样本目录与 dwgread gold JSON），供各测试按一致顺序回退；语料长期保留
// 在仓库根 .reference/libredwg，避免放在 /tmp 时被磁盘清理或系统回收
// 误删导致测试静默跳过。
package cad

import "os"

// libredwgGoldJSONPath 返回 dwgread -O JSON 导出的 gold JSON 文件路径。
// 两个既有环境变量语义合并：CAD_LIBREDWG_JSON（完整文件路径，兼容既有
// 口径）优先；其次 CAD_GOLD_JSON_DIR（目录，与 "ex<版本>.json" 拼接）；
// 均未设置时缺省 /tmp/ex<版本>.json（version 为 "2000"/"2018" 等版本串）。
func libredwgGoldJSONPath(version string) string {
	if p := os.Getenv("CAD_LIBREDWG_JSON"); p != "" {
		return p
	}
	name := "ex" + version + ".json"
	if dir := os.Getenv("CAD_GOLD_JSON_DIR"); dir != "" {
		return dir + "/" + name
	}
	return "/tmp/" + name
}

// libredwgTestDataDir 返回 LibreDWG test/test-data 语料目录：
// 优先 CAD_LIBREDWG_DATA 环境变量；其次仓库根 .reference/libredwg/test/test-data
// （相对 go test 的工作目录，即 cad 包目录向上三级）；最后回落历史位置
// /tmp/libredwg/test/test-data。目录都不存在时仍返回历史位置，
// 由调用方按"样本不可用"跳过测试。
func libredwgTestDataDir() string {
	if dir := os.Getenv("CAD_LIBREDWG_DATA"); dir != "" {
		return dir
	}
	for _, dir := range []string{
		"../../../.reference/libredwg/test/test-data",
		"/tmp/libredwg/test/test-data",
	} {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	return "/tmp/libredwg/test/test-data"
}
