// entities_mpolygon_test.go MPOLYGON 合成位流测试：复用 HATCH 路径机制的
// 边集路径（直线/圆弧/椭圆弧/样条四种 seg 类型）、多段线路径、渐变段、
// 图案定义段与 MPOLYGON 专属字段（style 双读/x_dir/CMC 占位）。
// MPOLYGON 无上游语料实例（dwg.spec DEBUG_CLASSES 分支），按 spec 逐位
// 构造并验证解码一致性。
package cad

import (
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"testing"
)

// writeMpolygonHead 写 R2004+ 公共头与 MPOLYGON 主体首段（style + 渐变段 +
// 高程/挤出/名称占位/填充标志）。R2013 口径（streamName，名称零占位）。
func writeMpolygonHead(w *testsupport.BitWriter, handle uint64, style uint16) {
	writeEntityPrefix(w, 500)
	writeCommonHead(w, handle, 2)
	w.BS(style)     // 主体首 style
	w.BL(0)         // is_gradient_fill=0
	w.BL(0)         // reserved
	w.BD(0)         // gradient angle
	w.BD(0)         // gradient shift
	w.BL(0)         // single_color_gradient
	w.BD(0)         // tint
	w.BL(0)         // num_colors=0（R2013+ 渐变名走字符串区，主流零占位）
	w.BD(2.5)       // elevation
	w.B3BD(0, 0, 1) // extrusion
	w.B(1)          // is_solid_fill=1
	w.B(0)          // is_associative=0
}

// TestDecodeMpolygonFromBits solid 填充 + 边集路径（四种 seg 类型齐备）+
// 多段线路径，验证 HATCH 路径解析复用与 MPOLYGON 专属字段。
func TestDecodeMpolygonFromBits(t *testing.T) {
	w := testsupport.NewBitWriter()
	writeMpolygonHead(w, 700, 1)
	w.BL(2) // num_paths
	// 路径 1：边集路径，四种曲线类型各一段
	w.BL(0) // flag（bit1=0 边集）
	w.BL(4) // num_segs
	w.RC(1) // 直线
	w.RD(0).RD(0)
	w.RD(10).RD(0)
	w.RC(2) // 圆弧
	w.RD(10).RD(0)
	w.BD(5).BD(0).BD(3.14159)
	w.B(1)
	w.RC(3) // 椭圆弧
	w.RD(10).RD(10)
	w.RD(5).RD(5)
	w.BD(0.5).BD(0).BD(1.5)
	w.B(0)
	w.RC(4) // 样条
	w.BL(3) // degree
	w.B(0)  // rational
	w.B(0)  // periodic
	w.BL(0) // num_knots（无节点 → 细分退化用控制点）
	w.BL(2) // num_control_points
	w.RD(10).RD(20)
	w.RD(0).RD(20)
	// R2013+ 拟合点段：num_fitpts=0 时拟合点与首末切矢整段缺席
	// （LibreDWG dwg.spec：仅样条由拟合点定义时写入，批次 H 修复语义）
	w.BL(0)
	w.BL(0) // 路径 1 边界句柄数
	// 路径 2：多段线路径（3 顶点闭合）
	w.BL(2) // flag（bit1=1 多段线）
	w.B(0)  // bulges_present
	w.B(1)  // closed
	w.BL(3) // num_verts
	w.RD(0).RD(0)
	w.RD(20).RD(0)
	w.RD(20).RD(20)
	w.BL(0) // 路径 2 边界句柄数
	// 图案段（solid → 无 angle/定义线）
	w.BS(0) // 路径后重复 style（spec 双读）
	w.BS(1) // pattern_type
	// hatch_color CMC（R2004+ 布局：BS index + BL rgb + RC flag）
	w.BS(7)
	w.BL(0xFF0000)
	w.RC(0)
	// x_dir + 总边界句柄数
	w.RD(1).RD(0)
	w.BL(0)
	// handle 流（xdic + layer）
	w.H(5, 30)
	w.H(5, 31)
	const objSizeBit = uint64(0) // 由解码断言单独核对路径，不依赖 objSizeBit
	r := newBitStream(w.Bytes())
	_, _ = r.readUMC()
	_, _ = r.readOT()
	head, err := parseEntityHead(r, 0, featMaterialFlags|featVisualStyles|featDSBinary)
	if err != nil {
		t.Fatal(err)
	}
	head.objSizeBit = objSizeBit
	ent, err := decodeMpolygonVer(r, &head, verR2013, 30)
	if err != nil {
		t.Fatal(err)
	}
	m := ent.(*entMpolygon)
	if m.style != 1 || m.styleTail != 0 || m.hatch.patternType != 1 {
		t.Fatalf("MPOLYGON style/pattern: style=%d tail=%d pattern=%d",
			m.style, m.styleTail, m.hatch.patternType)
	}
	if m.hatch.elevation != 2.5 || !m.hatch.solidFill {
		t.Fatalf("MPOLYGON 主体: elevation=%v solid=%v", m.hatch.elevation, m.hatch.solidFill)
	}
	if len(m.hatch.paths) != 2 {
		t.Fatalf("MPOLYGON 路径数: %d", len(m.hatch.paths))
	}
	p1 := m.hatch.paths[0]
	if p1.isPolyline || len(p1.segs) != 4 {
		t.Fatalf("路径 1: polyline=%v segs=%d", p1.isPolyline, len(p1.segs))
	}
	wantTypes := [4]uint8{1, 2, 3, 4}
	for i, seg := range p1.segs {
		if seg.curveType != wantTypes[i] {
			t.Fatalf("路径 1 seg[%d] 类型: %d", i, seg.curveType)
		}
	}
	if p1.segs[0].first.x != 0 || p1.segs[0].second.x != 10 {
		t.Fatalf("路径 1 直线段端点: %v→%v", p1.segs[0].first, p1.segs[0].second)
	}
	if p1.segs[1].radius != 5 || !p1.segs[1].ccw {
		t.Fatalf("路径 1 圆弧段: r=%v ccw=%v", p1.segs[1].radius, p1.segs[1].ccw)
	}
	if p1.segs[2].ratio != 0.5 || p1.segs[2].endpoint.x != 5 {
		t.Fatalf("路径 1 椭圆段: ratio=%v ep=%v", p1.segs[2].ratio, p1.segs[2].endpoint)
	}
	if p1.segs[3].degree != 3 || len(p1.segs[3].ctrl) != 2 {
		t.Fatalf("路径 1 样条段: degree=%d ctrl=%d", p1.segs[3].degree, len(p1.segs[3].ctrl))
	}
	if len(p1.points) == 0 {
		t.Fatal("路径 1 细分点列为空")
	}
	p2 := m.hatch.paths[1]
	if !p2.isPolyline || !p2.closed || len(p2.polyVerts) != 3 {
		t.Fatalf("路径 2: polyline=%v closed=%v verts=%d", p2.isPolyline, p2.closed, len(p2.polyVerts))
	}
	if m.xDir.x != 1 || m.xDir.y != 0 {
		t.Fatalf("MPOLYGON x_dir: %v", m.xDir)
	}
}

// TestDecodeMpolygonPatternFromBits 非实体填充（pattern_fill）变体：
// 渐变段全零 + 图案定义线段（angle/scale/double/定义线含划线数组）。
// 用 R2007 口径（名称走字符串区，主流程零占位）。
func TestDecodeMpolygonPatternFromBits(t *testing.T) {
	w2 := writeEntityPrefix(testsupport.NewBitWriter(), 500)
	writeCommonHead(w2, 701, 2)
	w2.BS(0)         // style=0
	w2.BL(0)         // is_gradient_fill
	w2.BL(0)         // reserved
	w2.BD(0)         // gradient angle
	w2.BD(0)         // gradient shift
	w2.BL(0)         // single_color
	w2.BD(0)         // tint
	w2.BL(0)         // num_colors
	w2.BD(0)         // elevation
	w2.B3BD(0, 0, 1) // extrusion
	w2.B(0)          // is_solid_fill=0（图案填充）
	w2.B(0)          // is_associative
	w2.BL(0)         // num_paths=0
	w2.BS(2)         // 路径后重复 style
	w2.BS(2)         // pattern_type=2 custom
	w2.BD(45.0)      // angle
	w2.BD(2.0)       // scale_spacing
	w2.B(0)          // double_flag
	w2.BS(1)         // num_deflines
	w2.BD(30.0)      // defline angle
	w2.BD(0).BD(0)   // pt0
	w2.BD(1).BD(1)   // offset
	w2.BS(2)         // num_dashes
	w2.BD(0.5).BD(0.25)
	w2.BS(1)       // hatch_color CMC index
	w2.BL(0)       // rgb
	w2.RC(0)       // flag
	w2.RD(1).RD(0) // x_dir
	w2.BL(0)       // 总边界句柄数
	w2.H(5, 30)    // xdic
	w2.H(5, 31)    // layer
	r := newBitStream(w2.Bytes())
	_, _ = r.readUMC()
	_, _ = r.readOT()
	head, err := parseEntityHead(r, 0, featMaterialFlags|featVisualStyles|featDSBinary)
	if err != nil {
		t.Fatal(err)
	}
	ent, err := decodeMpolygonVer(r, &head, verR2007, 30)
	if err != nil {
		t.Fatal(err)
	}
	m := ent.(*entMpolygon)
	if m.style != 0 || m.styleTail != 2 || m.hatch.patternType != 2 {
		t.Fatalf("MPOLYGON pattern style: style=%d tail=%d pattern=%d",
			m.style, m.styleTail, m.hatch.patternType)
	}
	if m.hatch.solidFill {
		t.Fatal("MPOLYGON 应为图案填充")
	}
	if m.hatch.angle != 45.0 || m.hatch.scaleSpacing != 2.0 || m.hatch.doubleFlag {
		t.Fatalf("MPOLYGON 图案段: angle=%v scale=%v double=%v",
			m.hatch.angle, m.hatch.scaleSpacing, m.hatch.doubleFlag)
	}
	if len(m.hatch.deflines) != 1 {
		t.Fatalf("MPOLYGON 定义线数: %d", len(m.hatch.deflines))
	}
	dl := m.hatch.deflines[0]
	if dl.angle != 30.0 || len(dl.dashes) != 2 || dl.dashes[1] != 0.25 {
		t.Fatalf("MPOLYGON 定义线: angle=%v dashes=%v", dl.angle, dl.dashes)
	}
	if m.xDir.x != 1 || m.xDir.y != 0 {
		t.Fatalf("MPOLYGON x_dir: %v", m.xDir)
	}
}
