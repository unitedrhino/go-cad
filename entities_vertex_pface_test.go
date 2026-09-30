// entities_vertex_pface_test.go VERTEX_PFACE/VERTEX_PFACE_FACE/VERTEX_MESH
// 解码器单元测试：bitWriter 构造标准位流验证字段解码，另用 example_r13
// 样本的 gold 值（handle 1253/1259）做端到端断言。
package cad

import (
	"math"
	"os"
	"testing"
)

// TestDecodeVertexPfaceFromBits VERTEX_PFACE：RC flag + 3BD point。
func TestDecodeVertexPfaceFromBits(t *testing.T) {
	w := writeEntityPrefix(newBitWriter(), 0x0D)
	writeCommonHead(w, 1253, 0)
	w.RC(0xc0)                // flag：MESH|PFACE_MESH 位
	w.B3BD(10.5, -2.25, 0.75) // point
	r := newBitStream(w.bytes())
	_, _ = r.readUMC()
	_, _ = r.readOT()
	head, err := parseEntityHead(r, 0, featMaterialFlags|featVisualStyles|featDSBinary)
	if err != nil {
		t.Fatal(err)
	}
	ent, err := decodeVertexPface(r, &head)
	if err != nil {
		t.Fatal(err)
	}
	v := ent.(*entVertexPface)
	if v.flag != 0xc0 {
		t.Fatalf("flag: %#x", v.flag)
	}
	if v.position.x != 10.5 || v.position.y != -2.25 || v.position.z != 0.75 {
		t.Fatalf("point: %v", v.position)
	}
}

// TestDecodeVertexPfaceFaceFromBits VERTEX_PFACE_FACE：4×BSd vertind，
// flag 恒 128 不从流读取。
func TestDecodeVertexPfaceFaceFromBits(t *testing.T) {
	w := writeEntityPrefix(newBitWriter(), 0x0E)
	writeCommonHead(w, 1259, 0)
	w.BS(2).BS(0).BS(0).BS(0) // vertind[0..3]
	r := newBitStream(w.bytes())
	_, _ = r.readUMC()
	_, _ = r.readOT()
	head, err := parseEntityHead(r, 0, featMaterialFlags|featVisualStyles|featDSBinary)
	if err != nil {
		t.Fatal(err)
	}
	ent, err := decodeVertexPfaceFace(r, &head)
	if err != nil {
		t.Fatal(err)
	}
	f := ent.(*entVertexPfaceFace)
	if f.flag != 128 {
		t.Fatalf("flag: %d", f.flag)
	}
	want := [4]int32{2, 0, 0, 0}
	if f.vertind != want {
		t.Fatalf("vertind: %v", f.vertind)
	}
}

// TestVertexPfaceGoldR13 example_r13 样本端到端：POLYLINE_PFACE（handle
// 1252）的 6 个顶点与 3 个面记录对照 gold JSON 关键值。
func TestVertexPfaceGoldR13(t *testing.T) {
	data, err := os.ReadFile("/tmp/libredwg/test/test-data/example_r13.dwg")
	if err != nil {
		t.Skipf("样本不可用: %v", err)
	}
	doc, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	// VERTEX_PFACE h=1253：flag=192 point=(7589.907…, 3459.338…, 0)
	v, ok := doc.EntityByHandle(1253).(*entVertexPface)
	if !ok {
		t.Fatalf("handle 1253 不是 VERTEX_PFACE: %T", doc.EntityByHandle(1253))
	}
	if v.flag != 192 {
		t.Fatalf("flag: %d", v.flag)
	}
	if math.Abs(v.position.x-7589.907311657487) > 1e-6 || math.Abs(v.position.y-3459.3382354664864) > 1e-6 {
		t.Fatalf("point: %v", v.position)
	}
	// VERTEX_PFACE_FACE h=1261：vertind={1,5,4,3}
	f, ok := doc.EntityByHandle(1261).(*entVertexPfaceFace)
	if !ok {
		t.Fatalf("handle 1261 不是 VERTEX_PFACE_FACE: %T", doc.EntityByHandle(1261))
	}
	if f.vertind != [4]int32{1, 5, 4, 3} {
		t.Fatalf("vertind: %v", f.vertind)
	}
}
