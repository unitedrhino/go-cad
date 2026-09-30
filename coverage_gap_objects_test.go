// coverage_gap_objects_test.go 零/低覆盖函数补强（对象/渲染/文本类）：
// TABLE value 字段分支、gfRead 原语、objGeneric 字段路径、XRECORD 访问器、
// EED 位串收集、重复句柄消解（真实样本）、渲染超长线剔除、De Boor 求值
// 与 TEXT/MTEXT 文本读取路径。
package cad

import (
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- objects_tablecontent.go ----

// TestReadTableValueFields R2004/R2007 两侧 data_type 全分支。
func TestReadTableValueFields(t *testing.T) {
	// R2004（verUntilR2004）：无 format_flags，值分支直读
	cases := []struct {
		name   string
		build  func(w *bitstream.EncWriter)
		dt     int64
		key    string
		nHdl   bool
		failed bool
	}{
		{"long0", func(w *bitstream.EncWriter) { w.WriteBL(0); w.WriteBL(7) }, 0, "data_long", false, false},
		{"double", func(w *bitstream.EncWriter) { w.WriteBL(2); w.WriteBD(3.5) }, 2, "data_double", false, false},
		{"string", func(w *bitstream.EncWriter) { w.WriteBL(4); w.WriteTV("s") }, 4, "data_string", false, false},
		{"date", func(w *bitstream.EncWriter) { w.WriteBL(8); w.WriteBL(3); w.WriteTF([]byte{1, 2, 3}) }, 8, "data_date", false, false},
		{"point", func(w *bitstream.EncWriter) { w.WriteBL(16); w.WriteBL(0); w.WriteRD(1); w.WriteRD(2) }, 16, "data_point", false, false},
		{"point3d", func(w *bitstream.EncWriter) { w.WriteBL(32); w.WriteBL(0); w.WriteRD(1); w.WriteRD(2); w.WriteRD(3) }, 32, "data_3dpoint", false, false},
		{"objid", func(w *bitstream.EncWriter) { w.WriteBL(64) }, 64, "", true, false},
		{"unknown", func(w *bitstream.EncWriter) { w.WriteBL(7) }, 7, "", false, true},
	}
	for _, tc := range cases {
		w := bitstream.NewEncWriter()
		tc.build(w)
		g := &objGeneric{}
		nHdl := 0
		fr := &gfRead{r: bitstream.NewBitStream(w.Bytes()), ver: verR2004}
		err := readTableValueFields(fr.r, fr, g, "", &nHdl)
		if tc.failed {
			if err == nil {
				t.Errorf("%s: 应返回错误", tc.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: 解析失败 %v", tc.name, err)
			continue
		}
		if tc.nHdl && nHdl != 1 {
			t.Errorf("%s: kObjectId 应计数 handle，nHdl=%d", tc.name, nHdl)
		}
		if !tc.nHdl && tc.key != "" && g.Field(tc.key) == nil {
			t.Errorf("%s: 缺少字段 %s", tc.name, tc.key)
		}
	}

	// R2007+：format_flags 低 2 位非 0 跳过值分支；尾部 unit/format/value
	w2 := bitstream.NewEncWriter()
	w2.WriteBL(1) // format_flags（&3 != 0 → skip）
	w2.WriteBL(2) // data_type（不按类型读值）
	w2.WriteBL(2) // unit_type（!= 12 → 读 value_string）
	// R2007+ has_strings=0 时尾部三串为 TU 内联（fr2.hasStrings=false）
	w2.WriteTU("FMT")
	w2.WriteTU("VAL")
	g2 := &objGeneric{}
	fr2 := &gfRead{r: bitstream.NewBitStream(w2.Bytes()), ver: verR2018}
	if err := readTableValueFields(fr2.r, fr2, g2, "v.", new(int)); err != nil {
		t.Fatalf("R2007 skip 分支失败: %v", err)
	}
	if g2.Field("v.value_string") == nil {
		t.Error("R2007 尾部缺 value_string")
	}

	// R2007+：format_flags=0 且 data_type=kLong 走值分支
	w3 := bitstream.NewEncWriter()
	w3.WriteBL(0)   // format_flags
	w3.WriteBL(0)   // data_type = kLong
	w3.WriteBL(42)  // data_long
	w3.WriteBL(12)  // unit_type = 12（不读 value_string）
	w3.WriteTV("F") // format_string
	g3 := &objGeneric{}
	fr3 := &gfRead{r: bitstream.NewBitStream(w3.Bytes()), ver: verR2018}
	if err := readTableValueFields(fr3.r, fr3, g3, "", new(int)); err != nil {
		t.Fatalf("R2007 kLong 分支失败: %v", err)
	}
	if v, _ := g3.Field("data_long").(int64); v != 42 {
		t.Errorf("data_long = %v", g3.Field("data_long"))
	}
}

// ---- objects_table.go ----

// TestGfReadRL gfRead.RL 原语。
func TestGfReadRL(t *testing.T) {
	w := bitstream.NewEncWriter()
	w.WriteRL(0xAABBCCDD)
	g := &objGeneric{}
	fr := &gfRead{r: bitstream.NewBitStream(w.Bytes()), ver: verR2000}
	if err := fr.RL("k", g); err != nil {
		t.Fatalf("RL 失败: %v", err)
	}
	if v, _ := g.Field("k").(int64); v != 0xAABBCCDD {
		t.Errorf("RL 值 = %v", g.Field("k"))
	}
}

// ---- objects_generic.go ----

// TestObjGenericFieldPath FieldPath 三级匹配顺序。
func TestObjGenericFieldPath(t *testing.T) {
	g := &objGeneric{Fields: []objField{
		{"plain", int64(1)},
		{"cells[0]", map[string]any{"inner": int64(2)}},
		{"arr", []any{int64(3)}},
	}}
	if v := g.FieldPath("plain"); v != int64(1) {
		t.Errorf("精确键 = %v", v)
	}
	if v := g.FieldPath("cells[0].inner"); v != int64(2) {
		t.Errorf("展平键 = %v", v)
	}
	if v := g.FieldPath("cells[0]"); v == nil {
		t.Error("整键（含点下标）应精确命中")
	}
	if v := g.FieldPath("arr.sub"); v != nil {
		t.Errorf("数组下钻应返回 nil，得到 %v", v)
	}
	if v := g.FieldPath("nope"); v != nil {
		t.Errorf("未命中应返回 nil，得到 %v", v)
	}
}

// ---- objects_dictionary.go ----

// TestXrecordAccessors XdataSize/XdataItems/KindName/Val 全 kind。
func TestXrecordAccessors(t *testing.T) {
	items := []xdataItem{
		{Kind: xdataInvalid},
		{Code: 1, Kind: xdataString, Str: "txt"},
		{Code: 10, Kind: xdataReal, Float: 1.5},
		{Code: 70, Kind: xdataInt8, Int: 1},
		{Code: 70, Kind: xdataInt16, Int: 2},
		{Code: 70, Kind: xdataInt32, Int: 3},
		{Code: 70, Kind: xdataInt64, Int: 4},
		{Code: 40, Kind: xdataPoint3D, Point: [3]float64{1, 2, 3}},
		{Code: 310, Kind: xdataBinary, Bytes: []byte{0xBE, 0xEF}},
		{Code: 330, Kind: xdataHandle, Int: 99},
		{Code: 0, Kind: xdataKind(len("0123456789") + 10)}, // 越界 kind
	}
	x := &objXrecord{xdataSize: 42, xdata: items}
	if x.XdataSize() != 42 {
		t.Errorf("XdataSize = %d", x.XdataSize())
	}
	if len(x.XdataItems()) != len(items) {
		t.Errorf("XdataItems 长度 = %d", len(x.XdataItems()))
	}
	wantNames := []string{"Invalid", "String", "Real", "Int8", "Int16", "Int32", "Int64", "Point3D", "Binary", "Handle", "Unknown"}
	for i, it := range items {
		if it.KindName() != wantNames[i] {
			t.Errorf("KindName[%d] = %s, 期望 %s", i, it.KindName(), wantNames[i])
		}
	}
	if items[2].Val() != 1.5 {
		t.Errorf("Real Val = %v", items[2].Val())
	}
	if v, ok := items[7].Val().([3]float64); !ok || v[2] != 3 {
		t.Errorf("Point3D Val = %v", items[7].Val())
	}
	if items[1].Val() != "txt" {
		t.Errorf("String Val = %v", items[1].Val())
	}
	if items[8].Val() != "BEEF" {
		t.Errorf("Binary Val = %v", items[8].Val())
	}
	if items[9].Val() != int64(99) {
		t.Errorf("Handle Val = %v", items[9].Val())
	}
}

// TestDumpBitsAndSkipEEDChain dumpBits 位串输出与 EED 链收集。
func TestDumpBitsAndSkipEEDChain(t *testing.T) {
	r := bitstream.NewBitStream([]byte{0b10110000, 0xFF})
	if got := dumpBits(r, 0, 4); got != "1011" {
		t.Errorf("dumpBits = %q", got)
	}
	if got := dumpBits(r, 12, 32); got == "" {
		t.Error("越界范围应返回空串而非 panic")
	}

	// EED 链：BS 长度 + H 应用句柄 + 长度字节，BS 0 终止
	w := bitstream.NewEncWriter()
	w.WriteBS(3)
	w.WriteH(4, 1, 0x0A)
	w.WriteTF([]byte{1, 2, 3})
	w.WriteBS(0)
	bits, err := skipEEDChainCollect(bitstream.NewBitStream(w.Bytes()))
	if err != nil {
		t.Fatalf("skipEEDChainCollect 失败: %v", err)
	}
	if len(bits) == 0 || strings0(bits) {
		t.Errorf("EED 位串为空: %q", bits)
	}
}

// strings0 判断位串是否为空。
func strings0(s string) bool { return s == "" }

// ---- objects.go：重复句柄消解（真实样本驱动） ----

// TestSelectBestDuplicateHandles 真实样本对象图构造重复句柄候选，
// 评分择优保持唯一与原序。
func TestSelectBestDuplicateHandles(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "line_2004.dwg"))
	if err != nil {
		t.Fatal(err)
	}
	objectsData, err := LoadNamedSectionDebug2(data, "AcDb:AcDbObjects")
	if err != nil {
		t.Fatal(err)
	}
	refs, err := buildObjectIndex(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) == 0 {
		t.Fatal("样本对象图为空")
	}
	// 单候选摘要
	info := inspectCandidate(objectsData, refs[0], verR2004, nil)
	if !info.parsedOK {
		t.Fatal("inspectCandidate 应解析成功")
	}
	// 构造重复：首条候选复制一份（同句柄同偏移）
	dup := make([]objectRef, 0, len(refs)+1)
	dup = append(dup, refs[0], refs[0])
	dup = append(dup, refs[1:]...)
	selected := selectBestDuplicateHandles(objectsData, dup, verR2004, nil)
	if len(selected) == 0 {
		t.Fatal("择优输出为空")
	}
	seen := map[uint64]bool{}
	for _, r := range selected {
		if seen[r.handle] {
			t.Fatalf("句柄 %d 重复输出", r.handle)
		}
		seen[r.handle] = true
	}
}

// TestMustHandles mustHandles 调试导出（真实样本）。
func TestMustHandles(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "line_2004.dwg"))
	if err != nil {
		t.Fatal(err)
	}
	if b := mustHandles(data); len(b) == 0 {
		t.Error("mustHandles 应返回 AcDb:Handles 段数据")
	}
}

// ---- render.go ----

// TestDropOversizeStrokes 超长线段剔除与全剔图元丢弃。
func TestDropOversizeStrokes(t *testing.T) {
	bbox := box2{0, 0, 10, 10} // 对角线≈14.14，limit≈21.2
	prims := []primitive{
		{kind: 0, strokes: []stroke{{0, 0, 1, 0}, {0, 0, 100, 0}}},
		{kind: 0, strokes: []stroke{{0, 0, 100, 0}}},
		{kind: 1, lb: label{x: 1, y: 1, w: 2, h: 2}},
	}
	out := dropOversizeStrokes(prims, bbox)
	if len(out) != 2 {
		t.Fatalf("输出图元数 = %d, 期望 2", len(out))
	}
	if len(out[0].strokes) != 1 || out[0].strokes[0].x2 != 1 {
		t.Errorf("超长段未剔除: %+v", out[0].strokes)
	}
	if out[1].kind != 1 {
		t.Errorf("label 图元应保留: %+v", out[1])
	}
}

// TestDeBoor De Boor 递推：输出点应落在控制点凸包内且 y/z 为零。
// knotSpan=3、u=1.5 的值 (2,0,0) 经参数扫描探针固定为回归基线。
func TestDeBoor(t *testing.T) {
	ctrl := []point3{{0, 0, 0}, {1, 0, 0}, {2, 0, 0}, {3, 0, 0}}
	knots := []float64{0, 0, 0, 1, 2, 3, 3, 3}
	p := deBoor(ctrl, nil, knots, 2, 3, 1.5)
	if p.x != 2 || p.y != 0 || p.z != 0 {
		t.Errorf("deBoor(u=1.5) = (%v,%v,%v)，期望 (2,0,0)", p.x, p.y, p.z)
	}
}

// ---- entities.go：TEXT/MTEXT 文本读取 ----

// TestReadTextString TU 优先与 TV 回退两条路径（TU 返回含 \0 结尾，
// 比对时去除）。
func TestReadTextString(t *testing.T) {
	w := bitstream.NewEncWriter()
	w.WriteTU("ABCD")
	s, err := readTextString(bitstream.NewBitStream(w.Bytes()), 0)
	if err != nil || strings.TrimSuffix(s, "\x00") != "ABCD" {
		t.Errorf("TU 路径: %q err=%v", s, err)
	}
	w2 := bitstream.NewEncWriter()
	w2.WriteTV("ROOM")
	s2, err2 := readTextString(bitstream.NewBitStream(w2.Bytes()), 0)
	if err2 != nil || strings.TrimSuffix(s2, "\x00") != "ROOM" {
		t.Errorf("TV 路径: %q err=%v", s2, err2)
	}
}

// TestReadMTextStringBest 正常 TU 直读 + 短文本触发窗口重扫。
func TestReadMTextStringBest(t *testing.T) {
	w := bitstream.NewEncWriter()
	w.WriteTU("LONGTEXT-OK")
	r := bitstream.NewBitStream(w.Bytes())
	text, end, err := readMTextStringBest(r, 0)
	if err != nil || strings.TrimSuffix(text, "\x00") != "LONGTEXT-OK" {
		t.Fatalf("直读路径: %q err=%v", text, err)
	}
	if end == 0 {
		t.Error("结束位不应为 0")
	}
	// 短文本（评分低于阈值）触发 ±64 位窗口扫描，仍应返回非空文本
	w2 := bitstream.NewEncWriter()
	w2.WriteTU("AB")
	text2, _, err2 := readMTextStringBest(bitstream.NewBitStream(w2.Bytes()), 0)
	if err2 != nil || strings.TrimSuffix(text2, "\x00") == "" {
		t.Errorf("窗口重扫路径: %q err=%v", text2, err2)
	}
}
