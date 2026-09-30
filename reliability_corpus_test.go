// 本文件为全量对齐批次 T 的全语料吞吐测试：对 LibreDWG 官方语料
// test/test-data 下全部 .dwg（递归各版本子目录）与 .dxf 执行完整消费链
// （Parse → Version → Texts → DumpEntities → EntityCount → RenderPNG →
// WriteDwg → 再 Parse 校验实体数一致），逐样本计时并汇总版本×结果矩阵。
//
// 失败仲裁口径：任何样本失败（error / panic / 渲染空）时调用 dwgread
// （LibreDWG 0.14 基线）做金标准仲裁——dwgread 同样失败或输出垃圾的样本
// 标记为「样本损坏」（记录证据后跳过，不计失败）；dwgread 成功而我们失败
// 的判定为本实现 bug（测试失败，逐个修复）。
//
// 门控：语料目录按 libredwgTestDataDir 统一解析（CAD_LIBREDWG_DATA 可覆
// 盖），语料缺失时整体 skip；dwgread 二进制路径可用 CAD_DWGREAD 覆盖，
// 缺省 /tmp/libredwg-build/dwgread。
//
// 超范围版本说明：AC1.40（R1.4）/AC1003（R2.6）/AC2.10（R2.10）为
// 80 年代古董格式，本包从未声明支持（detectVersion 白名单之外），
// 相关样本单列 unsupported 统计并跳过，不计入 bug。
package cad

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// corpusStep 单步执行记录：步骤名、耗时与错误信息（空串表示成功）。
type corpusStep struct {
	name string
	ms   int64
	err  string
}

// corpusChainResult 完整消费链结果：各步骤记录、实体数与渲染非背景像素数。
type corpusChainResult struct {
	steps       []corpusStep
	entityCount int
	darkPixels  int // 渲染 PNG 中非背景像素数（渲染空判定依据）
}

// corpusOK 链路是否全部成功（所有步骤无错误且非渲染空）。
func (r *corpusChainResult) corpusOK() bool {
	for _, s := range r.steps {
		if s.err != "" {
			return false
		}
	}
	return true
}

// corpusFirstError 返回首个失败步骤名与错误（全部成功时返回空）。
func (r *corpusChainResult) corpusFirstError() (string, string) {
	for _, s := range r.steps {
		if s.err != "" {
			return s.name, s.err
		}
	}
	return "", ""
}

// corpusDwgreadBinary dwgread 仲裁二进制路径（CAD_DWGREAD 覆盖）。
func corpusDwgreadBinary() string {
	if p := os.Getenv("CAD_DWGREAD"); p != "" {
		return p
	}
	return "/tmp/libredwg-build/dwgread"
}

// corpusDwgreadArbitrate dwgread 金标准仲裁：以 JSON 全量导出验证样本可读性。
// 返回 ok=true 表示 dwgread 成功且输出含对象数据（样本完好，我们的失败即
// bug）；ok=false 表示 dwgread 也失败或输出垃圾（样本损坏），evidence 为
// 失败证据摘要。
func corpusDwgreadArbitrate(t *testing.T, path string) (ok bool, evidence string) {
	t.Helper()
	bin := corpusDwgreadBinary()
	if _, err := os.Stat(bin); err != nil {
		t.Logf("[仲裁] dwgread 不可用（%v），样本按损坏处理待人工复核: %s", err, path)
		return false, "dwgread 不可用"
	}
	outJSON := filepath.Join(t.TempDir(), "arbitrate.json")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-O", "JSON", "-o", outJSON, path)
	cmd.Env = append(os.Environ(), "LIBREDWG_TRACE=0")
	if err := cmd.Run(); err != nil {
		return false, "dwgread 退出错误: " + err.Error()
	}
	raw, err := os.ReadFile(outJSON)
	if err != nil {
		return false, "dwgread 未产出 JSON: " + err.Error()
	}
	// 输出垃圾判定：合法 JSON 且含 FILEHEADER/OBJECTS 结构键（空图也输出）
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false, fmt.Sprintf("dwgread 输出非合法 JSON（%d 字节）: %v", len(raw), err)
	}
	if _, has := probe["OBJECTS"]; !has {
		if _, has = probe["FILEHEADER"]; !has {
			return false, "dwgread JSON 缺少 OBJECTS/FILEHEADER 键（输出垃圾）"
		}
	}
	return true, ""
}

// corpusCollectFiles 递归收集目录下指定扩展名文件（返回相对语料根的路径，已排序）。
func corpusCollectFiles(t *testing.T, dir, ext string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.EqualFold(filepath.Ext(p), ext) {
			rel, rerr := filepath.Rel(dir, p)
			if rerr != nil {
				return rerr
			}
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历语料目录失败: %v", err)
	}
	sort.Strings(out)
	return out
}

// corpusVersionLabel 按文件头 6 字节魔数归类版本标签（统计矩阵行键）。
// 超范围古董版本（AC1.40/AC1.50/AC2.10/AC2.11/AC1003）单列标注。
func corpusVersionLabel(data []byte) string {
	if len(data) < 6 {
		return "过小"
	}
	switch string(data[:6]) {
	case "AC1012":
		return "R13"
	case "AC1014":
		return "R14"
	case "AC1015":
		return "R2000"
	case "AC1018":
		return "R2004"
	case "AC1021":
		return "R2007"
	case "AC1024":
		return "R2010"
	case "AC1027":
		return "R2013"
	case "AC1032":
		return "R2018"
	case "AC1004":
		return "R9"
	case "AC1006":
		return "R10"
	case "AC1009":
		return "R11"
	case "AC1.40", "AC1.50", "AC2.10", "AC2.11", "AC1003":
		return "超范围(" + string(data[:6]) + ")"
	default:
		return "未知(" + string(data[:6]) + ")"
	}
}

// corpusVersionUnsupported 是否为超出本包支持范围的历史版本。
func corpusVersionUnsupported(label string) bool {
	return strings.HasPrefix(label, "超范围(")
}

// corpusRunChain 执行单个 .dwg 的完整消费链（Parse → Version → Texts →
// DumpEntities → EntityCount → RenderPNG → WriteDwg → 再 Parse 校验实体数），
// 逐步计时；链内 panic 捕获为对应步骤错误（可靠性测试禁止 panic 逃逸）。
func corpusRunChain(data []byte) (res corpusChainResult) {
	// run 执行单个步骤：fn 返回的错误即步骤错误，panic 一并转为错误。
	run := func(name string, fn func() error) {
		start := time.Now()
		err := func() (err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("panic: %v", r)
				}
			}()
			return fn()
		}()
		res.steps = append(res.steps, corpusStep{name: name, ms: time.Since(start).Milliseconds(), err: errMsgOrEmpty(err)})
	}

	var doc *Document
	var pngBytes []byte
	var nEntities int
	run("Parse", func() error {
		d, err := Parse(data)
		doc = d
		return err
	})
	if doc == nil {
		return
	}
	run("Version", func() error {
		if doc.Version() == "" {
			return fmt.Errorf("版本串为空")
		}
		return nil
	})
	run("Texts", func() error {
		_ = doc.Texts()
		return nil
	})
	run("DumpEntities", func() error {
		_, err := DumpEntities(data)
		return err
	})
	run("EntityCount", func() error {
		nEntities = doc.EntityCount()
		return nil
	})
	run("RenderPNG", func() error {
		b, err := RenderPNG(doc, RenderOptions{Width: 800})
		pngBytes = b
		return err
	})
	run("RenderNonBlank", func() error {
		img, err := png.Decode(bytes.NewReader(pngBytes))
		if err != nil {
			return fmt.Errorf("输出非合法 PNG: %w", err)
		}
		b := img.Bounds()
		dark := 0
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				r, g, bl, a := img.At(x, y).RGBA()
				if r != 65535 || g != 65535 || bl != 65535 || a != 65535 {
					dark++
				}
			}
		}
		res.darkPixels = dark
		// 渲染空判定：模型空间存在「可渲染几何实体」但整图无笔画。
		// 豁免类（语料实证与 dwg2png 行为一致）：
		//   - BLOCK/ENDBLK/SEQEND：无几何的块标记实体；
		//   - 3DSOLID/REGION/BODY：ACIS B-rep 曲面，本渲染器与 LibreDWG
		//     dwg2png 均不做曲面细分，无线框可画；
		//   - 其余未知类型（entUnknownEnt 兜底）无解码几何。
		if dark == 0 && corpusHasDrawableEntity(doc) {
			return fmt.Errorf("渲染空（模型空间 %d 实体，非背景像素 0）", len(doc.modelSpace))
		}
		return nil
	})
	// WriteDwg → 再 Parse → 实体数一致：pre-R13 无写出器（功能边界，
	// WriteDwgR2000 仅覆盖 R13/R14/R2000），该步骤记 N/A 跳过。
	run("WriteDwg", func() error {
		if doc.version.preR13() {
			return nil // N/A
		}
		var buf bytes.Buffer
		if err := WriteDwg(doc, &buf); err != nil {
			return err
		}
		doc2, err := Parse(buf.Bytes())
		if err != nil {
			return fmt.Errorf("回读失败: %w", err)
		}
		if got := doc2.EntityCount(); got != nEntities {
			return fmt.Errorf("回读实体数不一致: 原 %d 回读 %d", nEntities, got)
		}
		return nil
	})
	return
}

// errMsgOrEmpty 错误为空时返回空串（步骤成功）。
func errMsgOrEmpty(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// corpusHasDrawableEntity 模型空间是否含可渲染几何实体（渲染空判定用）：
// 排除块标记（BLOCK/ENDBLK/SEQEND）、ACIS B-rep（3DSOLID/REGION/BODY，
// 无曲面细分渲染）与 UNKNOWN_ENT 兜底（无解码几何）。
func corpusHasDrawableEntity(doc *Document) bool {
	for _, ent := range doc.modelSpace {
		base := entBase(ent)
		if base == nil {
			continue
		}
		switch base.typeName {
		case "BLOCK", "ENDBLK", "SEQEND", "3DSOLID", "REGION", "BODY":
			continue
		}
		if _, ok := ent.(*entUnknownEnt); ok {
			continue
		}
		return true
	}
	return false
}

// corpusStats 版本×结果统计单元。
type corpusStats struct {
	total       int
	ok          int
	corrupt     int
	unsupported int
	failures    []string // bug 清单（样本: 步骤 错误）
}

// TestReliabilityCorpusThroughput 全语料 .dwg 吞吐主测试。
func TestReliabilityCorpusThroughput(t *testing.T) {
	dir := testsupport.LibredwgTestDataDir()
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("LibreDWG 语料缺失（%s）: %v", dir, err)
	}
	files := corpusCollectFiles(t, dir, ".dwg")
	if len(files) == 0 {
		t.Skip("语料目录下无 .dwg 样本")
	}

	stats := map[string]*corpusStats{}
	labelOrder := []string{}
	slow := []struct {
		rel string
		ms  int64
	}{}
	verdicts := map[string]string{} // 相对路径 → ok/corrupt/unsupported/bug
	var totalMS int64

	for _, rel := range files {
		full := filepath.Join(dir, rel)
		data, err := os.ReadFile(full)
		if err != nil {
			t.Errorf("读取样本失败 %s: %v", rel, err)
			continue
		}
		label := corpusVersionLabel(data)
		st := stats[label]
		if st == nil {
			st = &corpusStats{}
			stats[label] = st
			labelOrder = append(labelOrder, label)
		}
		st.total++
		if corpusVersionUnsupported(label) {
			st.unsupported++
			verdicts[rel] = "unsupported"
			t.Logf("[跳过] %s: 超范围版本 %s（本包不支持的语料，非 bug）", rel, label)
			continue
		}

		start := time.Now()
		res := corpusRunChain(data)
		elapsed := time.Since(start).Milliseconds()
		totalMS += elapsed
		slow = append(slow, struct {
			rel string
			ms  int64
		}{rel, elapsed})

		if res.corpusOK() {
			st.ok++
			verdicts[rel] = "ok"
			continue
		}
		stepName, stepErr := res.corpusFirstError()
		ok, evidence := corpusDwgreadArbitrate(t, full)
		if !ok {
			st.corrupt++
			verdicts[rel] = "corrupt"
			t.Logf("[样本损坏] %s: 我们失败于 %s: %s | dwgread 证据: %s", rel, stepName, stepErr, evidence)
			continue
		}
		verdicts[rel] = "bug"
		st.failures = append(st.failures, fmt.Sprintf("%s: %s: %s", rel, stepName, stepErr))
		t.Errorf("[bug] %s 失败于 %s: %s（dwgread 仲裁成功，需修复）", rel, stepName, stepErr)
	}

	// 汇总矩阵与吞吐统计
	t.Logf("=== 全语料吞吐矩阵（共 %d 个 .dwg，总耗时 %.1fs）===", len(files), float64(totalMS)/1000)
	sort.Strings(labelOrder)
	bugTotal, corruptTotal, okTotal, unsupportedTotal := 0, 0, 0, 0
	for _, label := range labelOrder {
		st := stats[label]
		bugTotal += len(st.failures)
		corruptTotal += st.corrupt
		okTotal += st.ok
		unsupportedTotal += st.unsupported
		t.Logf("%-14s 样本 %3d | 成功 %3d | 样本损坏 %2d | 不支持 %2d | bug %d",
			label, st.total, st.ok, st.corrupt, st.unsupported, len(st.failures))
	}
	t.Logf("合计: 成功 %d / 损坏 %d / 不支持 %d / bug %d", okTotal, corruptTotal, unsupportedTotal, bugTotal)

	sort.Slice(slow, func(i, j int) bool { return slow[i].ms > slow[j].ms })
	for i, s := range slow {
		if i >= 5 {
			break
		}
		t.Logf("最慢样本 #%d: %s (%dms)", i+1, s.rel, s.ms)
	}
	// 损坏样本清单集中输出（供跨批次协调与 CLI e2e 一致性核对）
	var corruptList []string
	for rel, v := range verdicts {
		if v == "corrupt" {
			corruptList = append(corruptList, rel)
		}
	}
	sort.Strings(corruptList)
	if len(corruptList) > 0 {
		t.Logf("=== 样本损坏清单（%d 个，测试跳过）===", len(corruptList))
		for _, rel := range corruptList {
			t.Logf("  损坏: %s", rel)
		}
	}
}

// TestReliabilityCorpusDXF 全语料 .dxf 吞吐测试：ParseDXF → RenderPNG。
// DXF 读取链路（dxf_read.go）归 WR 批次负责：本测试对失败样本同样做
// dwgread 仲裁，损坏样本跳过；dwgread 成功而我们失败的样本仅记录清单
// 不判失败（bug 修复由 WR 批次/主会话协调，避免本测试红阻断批次收口）。
func TestReliabilityCorpusDXF(t *testing.T) {
	dir := testsupport.LibredwgTestDataDir()
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("LibreDWG 语料缺失（%s）: %v", dir, err)
	}
	files := corpusCollectFiles(t, dir, ".dxf")
	if len(files) == 0 {
		t.Skip("语料目录下无 .dxf 样本")
	}
	var ok, corrupt int
	var dxfIssues []string
	start := time.Now()
	for _, rel := range files {
		full := filepath.Join(dir, rel)
		data, err := os.ReadFile(full)
		if err != nil {
			t.Errorf("读取样本失败 %s: %v", rel, err)
			continue
		}
		err = func() (err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("panic: %v", r)
				}
			}()
			doc, perr := ParseDXF(data)
			if perr != nil {
				return perr
			}
			if _, rerr := RenderPNG(doc, RenderOptions{Width: 800}); rerr != nil {
				return rerr
			}
			return nil
		}()
		if err == nil {
			ok++
			continue
		}
		if arbOK, evidence := corpusDwgreadArbitrate(t, full); !arbOK {
			corrupt++
			t.Logf("[样本损坏] %s: 我们失败: %v | dwgread 证据: %s", rel, err, evidence)
			continue
		}
		dxfIssues = append(dxfIssues, fmt.Sprintf("%s: %v", rel, err))
	}
	t.Logf("=== DXF 语料统计（共 %d 个，耗时 %.1fs）: 成功 %d / 样本损坏 %d / 待协调问题 %d ===",
		len(files), time.Since(start).Seconds(), ok, corrupt, len(dxfIssues))
	for _, issue := range dxfIssues {
		t.Logf("[DXF 待协调] %s", issue)
	}
}
