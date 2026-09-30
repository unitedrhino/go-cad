// acis_test.go ACIS 类实体（REGION/3DSOLID/BODY）单元测试：
// 验证 acis_empty 标志、SAT 加密文本块提取与解混淆（≤32 保留/其余 159-字节）。
package cad

import (
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"strings"
	"testing"
)

// TestDecodeAcisEmpty 空的 3DSOLID（290=1）：仅公共头 + handle 流。
func TestDecodeAcisEmpty(t *testing.T) {
	w := writeEntityPrefix(testsupport.NewBitWriter(), 0x20)
	writeCommonHead(w, 300, 2)
	w.B(1) // acis_empty=1
	w.B(0) // wireframe_data_present=0（COMMON_3DSOLID 无条件读）
	w.B(0) // acis_empty_bit
	// R2013+ 修订段（COMMON_3DSOLID SINCE R_2013b，不受 version>1 约束，
	// acis_empty=1 的 AcDs 场景同样存在）
	w.B(0)    // has_revision_guid
	w.BL(256) // revision_major
	w.BS(256) // revision_minor1
	w.BS(202) // revision_minor2
	for i := 0; i < 8; i++ {
		w.RC(byte(i + 1)) // revision_bytes
	}
	w.BL(0) // end_marker
	r := newBitStream(w.Bytes())
	_, _ = r.readUMC()
	_, _ = r.readOT()
	head, err := parseEntityHead(r, 0, featMaterialFlags|featVisualStyles|featDSBinary)
	if err != nil {
		t.Fatal(err)
	}
	ent, err := decodeAcis(r, &head, "3DSOLID", verR2018)
	if err != nil {
		t.Fatal(err)
	}
	a := ent.(*entAcis)
	if !a.acisEmpty {
		t.Fatal("应为空 ACIS")
	}
	if len(a.blocks) != 0 || len(a.acisData) != 0 {
		t.Fatalf("空 ACIS 不应有块: %d", len(a.blocks))
	}
}

// TestDecodeAcisSATBlocks 非空 SAT：version=1 + 多个加密文本块。
func TestDecodeAcisSATBlocks(t *testing.T) {
	// ACIS 文本样例（解混淆后应可读）
	satText := "ACIS 4.0 Test File"
	// 加密：≤32 保留，其余 159-b
	enc := func(b byte) byte {
		if b <= 32 {
			return b
		}
		return 159 - b
	}
	w := writeEntityPrefix(testsupport.NewBitWriter(), 0x1F)
	writeCommonHead(w, 301, 2)
	w.B(0) // acis_empty=0
	w.B(0) // unknown
	w.BS(1)
	// 块 1：长度 = len(satText)
	w.BL(uint32(len(satText)))
	for i := 0; i < len(satText); i++ {
		w.RC(enc(satText[i]))
	}
	// 块 2：长度 0 终止
	w.BL(0)
	// COMMON_3DSOLID 尾段 + R2013+ 修订段（version=1 时修订段同样无条件读）
	w.B(0)  // wireframe_data_present=0
	w.B(0)  // acis_empty_bit
	w.B(0)  // has_revision_guid
	w.BL(0) // revision_major
	w.BS(0) // revision_minor1
	w.BS(0) // revision_minor2
	for i := 0; i < 8; i++ {
		w.RC(0) // revision_bytes
	}
	w.BL(0) // end_marker
	r := newBitStream(w.Bytes())
	_, _ = r.readUMC()
	_, _ = r.readOT()
	head, err := parseEntityHead(r, 0, featMaterialFlags|featVisualStyles|featDSBinary)
	if err != nil {
		t.Fatal(err)
	}
	ent, err := decodeAcis(r, &head, "REGION", verR2018)
	if err != nil {
		t.Fatal(err)
	}
	a := ent.(*entAcis)
	if a.acisEmpty || a.version != 1 {
		t.Fatalf("标志: empty=%v version=%d", a.acisEmpty, a.version)
	}
	if len(a.blocks) != 1 {
		t.Fatalf("块数: %d", len(a.blocks))
	}
	if string(a.acisData) != satText {
		t.Fatalf("SAT 文本解混淆失败: %q", string(a.acisData))
	}
	if !strings.HasPrefix(string(a.acisData), "ACIS") {
		t.Fatalf("SAT 前缀: %q", string(a.acisData))
	}
}
