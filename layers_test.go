// layers_test.go container/objects/classes/layer 层的单元测试：
// 用测试位流构造器验证各解析函数的字段语义。
package cad

import (
	"encoding/binary"
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"testing"
)

func TestDecryptDataPageHeader(t *testing.T) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i)
	}
	pageAddr := uint64(0x1000)
	dec := unmaskPageHeader(raw, pageAddr)
	// 手工计算首块：mask = 0x4164536B ^ 0x1000；期望 = raw ^ mask
	mask := uint32(0x4164536B ^ uint32(pageAddr))
	want := binary.LittleEndian.Uint32(raw[0:]) ^ mask
	got := binary.LittleEndian.Uint32(dec[0:])
	if got != want {
		t.Fatalf("首块解密: got=%#x want=%#x", got, want)
	}
	// 第 8 块同样规则
	want7 := binary.LittleEndian.Uint32(raw[28:]) ^ mask
	got7 := binary.LittleEndian.Uint32(dec[28:])
	if got7 != want7 {
		t.Fatalf("末块解密: got=%#x want=%#x", got7, want7)
	}
}

func TestReadCString(t *testing.T) {
	cases := []struct {
		in   []byte
		want string
	}{
		{[]byte{'A', 'B', 0, 'C'}, "AB"},
		{[]byte{'X', 'Y'}, "XY"},
		{[]byte{0}, ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := readCString(c.in); got != c.want {
			t.Errorf("readCString(%v)=%q 期望 %q", c.in, got, c.want)
		}
	}
}

func TestParseR2000SectionDirectory(t *testing.T) {
	// 0x15 处条目数 + 每条 9 字节（record_no + offset u32 + size u32）
	buf := make([]byte, 0x15+9*2+0x50+0x10)
	binary.LittleEndian.PutUint32(buf[0x15:], 2)
	buf[0x19] = r2000SecObjectMap
	binary.LittleEndian.PutUint32(buf[0x19+1:], 0x30)
	binary.LittleEndian.PutUint32(buf[0x19+5:], 0x50)
	buf[0x19+9] = r2000SecClasses
	binary.LittleEndian.PutUint32(buf[0x19+10:], 0x200)
	binary.LittleEndian.PutUint32(buf[0x19+14:], 0x30)

	locs, err := parseR2000Directory(buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(locs) != 2 {
		t.Fatalf("期望 2 条得到 %d", len(locs))
	}
	if locs[0].recordNo != r2000SecObjectMap || locs[0].offset != 0x30 || locs[0].size != 0x50 {
		t.Fatalf("条目 0 错误: %+v", locs[0])
	}
	if locs[1].recordNo != r2000SecClasses || locs[1].offset != 0x200 {
		t.Fatalf("条目 1 错误: %+v", locs[1])
	}
	// readR2000Section 按段号取数据
	sec, err := readR2000Section(buf, r2000SecObjectMap)
	if err != nil {
		t.Fatal(err)
	}
	if len(sec) != 0x50 {
		t.Fatalf("段长度期望 0x50 得到 %d", len(sec))
	}
	// 不存在的段号报错
	if _, err := readR2000Section(buf, r2000SecMeasurement); err == nil {
		t.Error("不存在段应报错")
	}
	// 条目数超限报错
	bad := make([]byte, 0x15+4)
	binary.LittleEndian.PutUint32(bad[0x15:], 100)
	if _, err := parseR2000Directory(bad); err == nil {
		t.Error("条目数超限应报错")
	}
}

func TestParseObjectRecordR2010Layout(t *testing.T) {
	// 构造：MS size + UMC hss + OT + 数据（hss 位=0 全数据）
	// MS(30) + UMC(0) + OT(0x13) + 数据
	w := testsupport.NewBitWriter()
	w.MS(8)
	w.UMC(0)
	w.OT(0x13)
	payload := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x01, 0x02, 0x03, 0x04}
	w.RCS(payload)
	w.RCS(make([]byte, 8)) // CRC + 余量
	data := w.Bytes()

	objects := append([]byte{}, data...)
	refs := []objectRef{{handle: 1, offset: 0}}
	rec, err := parseObjectRecord(objects, refs[0], true)
	if err != nil {
		t.Fatal(err)
	}
	if rec.size != 8 {
		t.Fatalf("size=%d 期望 8", rec.size)
	}
	if !rec.r2010Plus {
		t.Fatal("r2010Plus 应为 true")
	}
	// hss=0 → 数据结束位 = bodyBitOffset + hsf + size*8 - 0
	h, err := parseObjHeader(rec)
	if err != nil {
		t.Fatal(err)
	}
	if h.typeCode != 0x13 {
		t.Fatalf("typeCode=%#x 期望 0x13", h.typeCode)
	}
	if rec.dataEndBit() < uint64(rec.size)*8 {
		t.Fatalf("dataEndBit=%d 异常", rec.dataEndBit())
	}
	_ = payload
}

func TestParseObjHeaderR2000Layout(t *testing.T) {
	// 非 R2010+：MS size + BS 类型码
	w := testsupport.NewBitWriter()
	w.MS(8)
	w.BS(0x13)
	w.RCS(make([]byte, 16))
	objects := w.Bytes()
	rec, err := parseObjectRecord(objects, objectRef{handle: 2, offset: 0}, false)
	if err != nil {
		t.Fatal(err)
	}
	h, err := parseObjHeader(rec)
	if err != nil {
		t.Fatal(err)
	}
	if h.typeCode != 0x13 {
		t.Fatalf("typeCode=%#x", h.typeCode)
	}
}

func TestReadModularChars(t *testing.T) {
	data := []byte{0x05}
	pos := 0
	v, err := readUnsignedModularChar(data, &pos)
	if err != nil || v != 5 || pos != 1 {
		t.Fatalf("UMC: v=%d pos=%d err=%v", v, pos, err)
	}
	// 有符号：0x44（低 6 位 4，符号位 0x40 置位）→ -4
	data2 := []byte{0x44}
	pos2 := 0
	v2, err := readModularChar(data2, &pos2)
	if err != nil || v2 != -4 {
		t.Fatalf("MC(-4): v=%d err=%v", v2, err)
	}
	// 正数：0x04 → 4
	data3 := []byte{0x04}
	pos3 := 0
	v4, _ := readModularChar(data3, &pos3)
	if v4 != 4 {
		t.Fatalf("MC(4): v=%d", v4)
	}
}

func TestParseObjectMapHandlesTerminator(t *testing.T) {
	// 只有终止块 → 空索引
	data := []byte{0x00, 0x02}
	objects, err := parseObjectMapHandles(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 0 {
		t.Fatalf("期望空索引得到 %d 条", len(objects))
	}
	// 数据不足报错
	if _, err := parseObjectMapHandles([]byte{0x00, 0x10, 0x01}); err == nil {
		t.Error("块越界应报错")
	}
}

func TestParseClassesSectionR13R15(t *testing.T) {
	// 哨兵 + RL size + 条目（BS classNumber + BS proxy + TV app + TV cpp + TV dxf + B zombie + BS itemID）
	w := testsupport.NewBitWriter()
	w.RCS(sentinelClassesBefore[:])
	w.RL(0x1000) // 数据长度（足够大让循环按 maxClass 终止）
	// 条目 1：classNumber=500 dxfName=ACDBXYZ
	w.BS(500)
	w.BS(0)
	w.TV("App")
	w.TV("Cpp")
	w.TV("AcDbXYZ")
	w.B(0)
	w.BS(0x1F2)
	// 条目 2：classNumber=501（=max，终止）
	w.BS(501)
	w.BS(0)
	w.TV("App")
	w.TV("Cpp")
	w.TV("AcDbABC")
	w.B(0)
	w.BS(0x1F3)
	w.RCS(sentinelClassesAfter[:])

	out, err := parseClassesSectionR13R15(w.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if out[500] != "AcDbXYZ" || out[501] != "AcDbABC" {
		t.Fatalf("类表错误: %#v", out)
	}
}

func TestScoreClassName(t *testing.T) {
	if classNameScore("ACDBXYZ") <= 0 {
		t.Error("正常类名应为正分")
	}
	if classNameScore("") >= 0 {
		t.Error("空名应为负分")
	}
	if classNameScore("\x01\x02") >= 0 {
		t.Error("控制字符应为负分")
	}
}

func TestEntityTypeName(t *testing.T) {
	if entityTypeName(0x13, nil) != "LINE" {
		t.Fatal("0x13 应为 LINE")
	}
	if entityTypeName(0x33, nil) != "LAYER" {
		t.Fatal("0x33 应为 LAYER")
	}
	dyn := map[uint16]string{500: "ACDBSOMETHING"}
	if entityTypeName(500, dyn) != "ACDBSOMETHING" {
		t.Fatal("动态类名应命中")
	}
	if entityTypeName(999, nil) != "" {
		t.Fatal("未知码应返回空")
	}
}

func TestIsEntityType(t *testing.T) {
	if !isEntityType(0x13, nil) || !isEntityType(0x07, nil) || !isEntityType(0x4D, nil) {
		t.Error("基础图元类型应为实体")
	}
	if isEntityType(0x33, nil) {
		t.Error("LAYER 不是实体")
	}
	dyn := map[uint16]string{500: "ACDBHATCH"}
	if !isEntityType(500, dyn) {
		t.Error("动态实体类应命中")
	}
}
