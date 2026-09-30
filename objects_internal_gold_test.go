// 本文件验证 DICTIONARY/XRECORD 内部对象解码与 LibreDWG dwgread JSON 输出一致。
// gold 值取自 dwgread -O JSON 对 example_2018.dwg 的解析结果（/tmp/ex2018.json，
// 生成命令：dwgread -O JSON -o /tmp/ex2018.json example_2018.dwg）。
// 样本目录经 libredwgTestDataDir 统一解析（CAD_LIBREDWG_DATA 覆盖 →
// 仓库根 .reference/libredwg → /tmp 历史位置），不存在则跳过。
package cad

import (
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"os"
	"path/filepath"
	"testing"
)

// goldDictionary2018 example_2018.dwg 中 handle=12 (0xC) 的 DICTIONARY 期望值
// （dwgread JSON: numitems=20, bitsize=5813, is_xdic_missing=1, cloning=1, is_hardowner=0）
var goldDictionary2018 = struct {
	numItems    int
	bitsize     uint64
	cloning     uint16
	xdicMissing bool
	texts       []string
	itemHandles []uint64
}{
	numItems:    20,
	bitsize:     5813,
	cloning:     1,
	xdicMissing: true,
	texts: []string{
		"ACAD_ASSOCNETWORK", "ACAD_ASSOCPERSSUBENTMANAGER", "ACAD_CIP_PREVIOUS_PRODUCT_INFO",
		"ACAD_COLOR", "ACAD_DETAILVIEWSTYLE", "ACAD_GROUP", "ACAD_LAYOUT", "ACAD_MATERIAL",
		"ACAD_MLEADERSTYLE", "ACAD_MLINESTYLE", "ACAD_PERSUBENTMGR", "ACAD_PLOTSETTINGS",
		"ACAD_PLOTSTYLENAME", "ACAD_SCALELIST", "ACAD_SECTIONVIEWSTYLE", "ACAD_TABLESTYLE",
		"ACAD_VISUALSTYLE", "ACAD_WIPEOUT_VARS", "ACDB_RECOMPOSE_DATA", "AcDbVariableDictionary",
	},
	itemHandles: []uint64{911, 735, 694, 107, 690, 13, 26, 106, 686, 23, 736, 25, 14, 668, 688, 666, 641, 601, 2492, 94},
}

// goldXrecord2018 example_2018.dwg 中 handle=619 (0x26B) 的 XRECORD 期望值
// （dwgread JSON: bitsize=133, is_xdic_missing=1, ownerhandle=618, reactors=1 个(618),
// xdata_size=8, xdata=[[270,1],[271,1]], cloning=1）
var goldXrecord2018 = struct {
	handle      uint64
	bitsize     uint64
	cloning     uint16
	xdicMissing bool
	owner       uint64
	numReactors int
	xdataSize   int
}{
	handle:      619,
	bitsize:     133,
	cloning:     1,
	xdicMissing: true,
	owner:       618,
	numReactors: 1,
	xdataSize:   8,
}

// TestDictionaryXrecordGold2018 对照 example_2018.dwg 的 DICTIONARY/XRECORD 解码结果
func TestDictionaryXrecordGold2018(t *testing.T) {
	dir := testsupport.LibredwgTestDataDir()
	data, err := os.ReadFile(filepath.Join(dir, "example_2018.dwg"))
	if err != nil {
		t.Skipf("样本不可用: %v", err)
	}
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	// DICTIONARY h=0xC
	dic, ok := doc.Dictionaries[goldDictionary2018TextHandle()]
	if !ok {
		t.Fatalf("DICTIONARY h=12 未解析到（当前字典数=%d）", len(doc.Dictionaries))
	}
	if dic.NumItems != goldDictionary2018.numItems {
		t.Errorf("DICTIONARY numitems=%d, 期望 %d", dic.NumItems, goldDictionary2018.numItems)
	}
	if dic.ObjSizeBit != goldDictionary2018.bitsize {
		t.Errorf("DICTIONARY bitsize=%d, 期望 %d", dic.ObjSizeBit, goldDictionary2018.bitsize)
	}
	if dic.Cloning != goldDictionary2018.cloning {
		t.Errorf("DICTIONARY cloning=%d, 期望 %d", dic.Cloning, goldDictionary2018.cloning)
	}
	if dic.XdicMissing != goldDictionary2018.xdicMissing {
		t.Errorf("DICTIONARY xdic_missing=%v, 期望 %v", dic.XdicMissing, goldDictionary2018.xdicMissing)
	}
	if len(dic.Texts) != len(goldDictionary2018.texts) {
		t.Fatalf("DICTIONARY texts=%d 项, 期望 %d: %v", len(dic.Texts), len(goldDictionary2018.texts), dic.Texts)
	}
	for i, want := range goldDictionary2018.texts {
		if dic.Texts[i] != want {
			t.Errorf("DICTIONARY text[%d]=%q, 期望 %q", i, dic.Texts[i], want)
		}
	}
	if len(dic.ItemHandles) != len(goldDictionary2018.itemHandles) {
		t.Fatalf("DICTIONARY itemHandles=%d 项, 期望 %d: %v", len(dic.ItemHandles), len(goldDictionary2018.itemHandles), dic.ItemHandles)
	}
	for i, want := range goldDictionary2018.itemHandles {
		if dic.ItemHandles[i] != want {
			t.Errorf("DICTIONARY itemHandle[%d]=%d, 期望 %d", i, dic.ItemHandles[i], want)
		}
	}

	// XRECORD h=0x26B
	xr, ok := doc.Xrecs[goldXrecord2018.handle]
	if !ok {
		t.Fatalf("XRECORD h=619 未解析到（当前 XRECORD 数=%d）", len(doc.Xrecs))
	}
	if xr.ObjSizeBit != goldXrecord2018.bitsize {
		t.Errorf("XRECORD bitsize=%d, 期望 %d", xr.ObjSizeBit, goldXrecord2018.bitsize)
	}
	if xr.Cloning != goldXrecord2018.cloning {
		t.Errorf("XRECORD cloning=%d, 期望 %d", xr.Cloning, goldXrecord2018.cloning)
	}
	if xr.XdicMissing != goldXrecord2018.xdicMissing {
		t.Errorf("XRECORD xdic_missing=%v, 期望 %v", xr.XdicMissing, goldXrecord2018.xdicMissing)
	}
	if xr.Owner != goldXrecord2018.owner {
		t.Errorf("XRECORD owner=%d, 期望 %d", xr.Owner, goldXrecord2018.owner)
	}
	if xr.NumReactors != goldXrecord2018.numReactors {
		t.Errorf("XRECORD numreactors=%d, 期望 %d", xr.NumReactors, goldXrecord2018.numReactors)
	}
	if xr.XdataSize != goldXrecord2018.xdataSize {
		t.Errorf("XRECORD xdata_size=%d, 期望 %d", xr.XdataSize, goldXrecord2018.xdataSize)
	}
}

// goldDictionary2018TextHandle 返回 gold DICTIONARY 的句柄（0xC = 12）
func goldDictionary2018TextHandle() uint64 { return 12 }
