// entities_unknown_ent_test.go UNKNOWN_ENT 兜底实体单元测试：验证兜底
// 解码、审计键导出与类名名单判定，另用 example_r13 样本 gold 值
// （handle 1266 = ACAD_TABLE 兜底）做端到端断言。
package cad

import (
	"github.com/unitedrhino/go-cad/internal/entity"
	"os"
	"testing"
)

// TestUnknownEntFallbackNameList 名单判定：ACAD_TABLE 兜底，
// LIGHT/MULTILEADER 等有正式 spec 布局的类不兜底。
func TestUnknownEntFallbackNameList(t *testing.T) {
	if !entity.UnknownEntFallbackNames["ACAD_TABLE"] {
		t.Fatal("ACAD_TABLE 应在兜底名单")
	}
	for _, n := range []string{"LIGHT", "MULTILEADER", "ACDBLINE", ""} {
		if entity.UnknownEntFallbackNames[n] {
			t.Fatalf("%q 不应在兜底名单", n)
		}
	}
}

// TestDecodeUnknownEntAuditKeys 兜底实体的审计键：entity 强制
// UNKNOWN_ENT，dxfname 保留原类名（审计跳过该键，仅供诊断）。
func TestDecodeUnknownEntAuditKeys(t *testing.T) {
	e := &entity.EntUnknownEnt{BaseEntity: entity.BaseEntity{
		Handle: 1266, TypeName: "ACAD_TABLE", TypeCode: 528,
		Extra: map[string]any{"dxfname": "ACAD_TABLE"},
	}}
	if got := entity.EntityField(e, "entity"); got != "UNKNOWN_ENT" {
		t.Fatalf("entity 键: %v", got)
	}
	if got := entity.EntityField(e, "type"); got != int64(528) {
		t.Fatalf("type 键: %v", got)
	}
	if got := entity.EntityField(e, "dxfname"); got != "ACAD_TABLE" {
		t.Fatalf("dxfname 键: %v", got)
	}
}

// TestUnknownEntGoldR13 example_r13 样本端到端：h=1266 原类 ACAD_TABLE
// （type=528）兜底为 UNKNOWN_ENT，进入 entityByHandle。
func TestUnknownEntGoldR13(t *testing.T) {
	data, err := os.ReadFile("/tmp/libredwg/test/test-data/example_r13.dwg")
	if err != nil {
		t.Skipf("样本不可用: %v", err)
	}
	doc, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	u, ok := doc.EntityByHandle(1266).(*entity.EntUnknownEnt)
	if !ok {
		t.Fatalf("handle 1266 不是 entUnknownEnt: %T", doc.EntityByHandle(1266))
	}
	if u.TypeCode != 528 || u.TypeName != "ACAD_TABLE" {
		t.Fatalf("typeCode=%d typeName=%s", u.TypeCode, u.TypeName)
	}
	if entity.EntityField(u, "entity") != "UNKNOWN_ENT" {
		t.Fatalf("entity 键: %v", entity.EntityField(u, "entity"))
	}
	if u.Extra["dxfname"] != "ACAD_TABLE" {
		t.Fatalf("dxfname: %v", u.Extra["dxfname"])
	}
	// 位串收集完整（roundtrip 回放依赖）
	if u.HeadRawBits == "" || u.RawHandleBits == "" {
		t.Fatal("兜底实体缺少位串收集")
	}
}
