// entities_vertex_pface_test.go VERTEX_PFACE/VERTEX_PFACE_FACE/VERTEX_MESH
// 解码器单元测试：bitWriter 构造标准位流验证字段解码，另用 example_r13
// 样本的 gold 值（handle 1253/1259）做端到端断言。
package cad

import (
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/entity"
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"math"
	"os"
	"testing"
)

// TestDecodeVertexPfaceFromBits VERTEX_PFACE：RC flag + 3BD point。
func TestDecodeVertexPfaceFromBits(t *testing.T) {
	w := testsupport.WriteEntityPrefix(testsupport.NewBitWriter(), 0x0D)
	testsupport.WriteCommonHead(w, 1253, 0)
	w.RC(0xc0)                // flag：MESH|PFACE_MESH 位
	w.B3BD(10.5, -2.25, 0.75) // point
	r := bitstream.NewBitStream(w.Bytes())
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	head, err := entity.ParseEntityHead(r, 0, entity.FeatMaterialFlags|entity.FeatVisualStyles|entity.FeatDSBinary)
	if err != nil {
		t.Fatal(err)
	}
	ent, err := entity.DecodeVertexPface(r, &head)
	if err != nil {
		t.Fatal(err)
	}
	v := ent.(*entity.EntVertexPface)
	if v.Flag != 0xc0 {
		t.Fatalf("flag: %#x", v.Flag)
	}
	if v.Position.X != 10.5 || v.Position.Y != -2.25 || v.Position.Z != 0.75 {
		t.Fatalf("point: %v", v.Position)
	}
}

// TestDecodeVertexPfaceFaceFromBits VERTEX_PFACE_FACE：4×BSd vertind，
// flag 恒 128 不从流读取。
func TestDecodeVertexPfaceFaceFromBits(t *testing.T) {
	w := testsupport.WriteEntityPrefix(testsupport.NewBitWriter(), 0x0E)
	testsupport.WriteCommonHead(w, 1259, 0)
	w.BS(2).BS(0).BS(0).BS(0) // vertind[0..3]
	r := bitstream.NewBitStream(w.Bytes())
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	head, err := entity.ParseEntityHead(r, 0, entity.FeatMaterialFlags|entity.FeatVisualStyles|entity.FeatDSBinary)
	if err != nil {
		t.Fatal(err)
	}
	ent, err := entity.DecodeVertexPfaceFace(r, &head)
	if err != nil {
		t.Fatal(err)
	}
	f := ent.(*entity.EntVertexPfaceFace)
	if f.Flag != 128 {
		t.Fatalf("flag: %d", f.Flag)
	}
	want := [4]int32{2, 0, 0, 0}
	if f.Vertind != want {
		t.Fatalf("vertind: %v", f.Vertind)
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
	v, ok := doc.EntityByHandle(1253).(*entity.EntVertexPface)
	if !ok {
		t.Fatalf("handle 1253 不是 VERTEX_PFACE: %T", doc.EntityByHandle(1253))
	}
	if v.Flag != 192 {
		t.Fatalf("flag: %d", v.Flag)
	}
	if math.Abs(v.Position.X-7589.907311657487) > 1e-6 || math.Abs(v.Position.Y-3459.3382354664864) > 1e-6 {
		t.Fatalf("point: %v", v.Position)
	}
	// VERTEX_PFACE_FACE h=1261：vertind={1,5,4,3}
	f, ok := doc.EntityByHandle(1261).(*entity.EntVertexPfaceFace)
	if !ok {
		t.Fatalf("handle 1261 不是 VERTEX_PFACE_FACE: %T", doc.EntityByHandle(1261))
	}
	if f.Vertind != [4]int32{1, 5, 4, 3} {
		t.Fatalf("vertind: %v", f.Vertind)
	}
}
