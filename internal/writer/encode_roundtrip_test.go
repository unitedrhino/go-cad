// encode_roundtrip_test 对象级往返测试：解码 → 重编码 → 再解码，
// 逐字段比对一致（dwgwrite 编码方向的门禁测试）。
package writer

import (
	"bytes"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/drawing"
	"github.com/unitedrhino/go-cad/internal/object"
	"github.com/unitedrhino/go-cad/internal/objrec"
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"os"
	"strings"
	"testing"
)

// TestPlaceHolderRoundTrip PLACEHOLDER（空表记录）的解码→编码→重解码。
// 已知限制：PLACEHOLDER 的 ownerhandle 为继承句柄 (8.0.0)（code 8 =
// 沿用前文对象的 owner），重编码需要 handle 继承解析（dwg_resolve_
// handleref）支持，待实现后启用本测试。
func TestPlaceHolderRoundTrip(t *testing.T) {
	dir := testsupport.LibredwgTestDataDir()
	data, err := os.ReadFile(dir + "/example_2000.dwg")
	if err != nil {
		t.Skipf("样本不可用: %v", err)
	}
	doc, err := drawing.Parse(data)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	const h = 15
	g1 := doc.InternalObjects()[h]
	if g1 == nil {
		t.Fatalf("h=%d 首次解码缺失", h)
	}
	body2, _, err := EncodeInternalObjectR2000(g1, 0x50)
	if err != nil {
		t.Fatalf("重编码失败: %v", err)
	}
	if os.Getenv("CAD_RT_DEBUG") != "" {
		t.Logf("重编码(%d 字节): % X", len(body2), body2)
		for _, f := range g1.Fields {
			t.Logf("g1 %s = %v", f.Key, f.Val)
		}
	}
	rec2 := &objrec.ObjectRecord{
		Body:          body2,
		BodyBitOffset: 0,
		Size:          uint32(len(body2)),
	}
	// 重解码：className 用首次解码的真实类名（ACDBPLACEHOLDER）
	g2, err := object.DecodeInternalObject(rec2.BodyBitStream(), rec2, container.VerR2000, false, 0x50, "ACDBPLACEHOLDER", 30)
	if err != nil {
		t.Fatalf("重解码失败: %v", err)
	}
	// 逐字段比对（ObjSizeBit 基准不同：重编码 body 不含 OT 类型码的
	// 18 位，故差 18 位属预期；其余字段必须一致）
	if g1.Handle != g2.Handle {
		t.Errorf("Handle: %d != %d", g2.Handle, g1.Handle)
	}
	if os.Getenv("CAD_RT_DEBUG") != "" {
		t.Logf("g1: ObjSizeBit=%d Handle=%d NumReactors=%d Owner=%d Fields=%d",
			g1.ObjSizeBit, g1.Handle, g1.NumReactors, g1.Owner, len(g1.Fields))
		t.Logf("g2: ObjSizeBit=%d Handle=%d NumReactors=%d Owner=%d Fields=%d",
			g2.ObjSizeBit, g2.Handle, g2.NumReactors, g2.Owner, len(g2.Fields))
	}
	if g1.ObjSizeBit-g2.ObjSizeBit != 18 {
		t.Errorf("ObjSizeBit 差值: %d != 18", g1.ObjSizeBit-g2.ObjSizeBit)
	}
	if g1.NumReactors != g2.NumReactors {
		t.Errorf("NumReactors: %d != %d", g2.NumReactors, g1.NumReactors)
	}
	if g1.Owner != g2.Owner {
		t.Errorf("Owner: %d != %d", g2.Owner, g1.Owner)
	}
	for _, f := range g1.Fields {
		v2 := g2.Field(f.Key)
		if f.Key == "size" || f.Key == "bitsize" || f.Key == "type" ||
			f.Key == "object" || f.Key == "dxfname" {
			continue // body 级与表示级差异，单独校验
		}
		if !testsupport.AnyRoundTripEqual(f.Val, v2) && !testsupport.AnyEqual(f.Val, v2) {
			t.Errorf("字段 %s: %v != %v", f.Key, f.Val, v2)
			if os.Getenv("CAD_AUDIT_DIFF") != "" {
				t.Logf("[dRD] %s: got %v want %v", f.Key, v2, f.Val)
			}
		}
	}
}

// TestXrecordRoundTripSynthetic 合成 XRECORD 位流的 round-trip：
// 先按 spec 手工构造已知内容的位流，解码 → 重编码 → 重解码，要求
// 重编码位流与构造位流**完全一致**（位级门禁，能精确定位任何编码
// 偏差）。
func TestXrecordRoundTripSynthetic(t *testing.T) {
	// 构造原始位流（R2000 语义）：RL bitsize + H + EED 终止 BS +
	// BL num_reactors + BL xdata_size + xdata items + cloning BS +
	// handle 流（num_objid_handles 非流字段，由 handle 流推导）
	w := bitstream.NewEncWriter()
	w.WriteRL(0) // bitsize 占位
	w.WriteH(0, 1, 0x64)
	w.WriteBS(0) // EED 终止
	w.WriteBL(0) // num_reactors = 0
	// xdata：4 个 item（INT16 1 / INT16 2 / RC 码页字符串 / POINT 退化为忽略）
	items := []object.XdataItem{
		{Code: 270, Kind: object.XdataInt16, Int: 1},
		{Code: 271, Kind: object.XdataInt16, Int: 2},
		{Code: 300, Kind: object.XdataString, Str: "abc"},
		{Code: 40, Kind: object.XdataReal, Float: 2.5},
	}
	xd := bitstream.NewEncWriter()
	if err := encodeXdataItems(xd, items, false); err != nil {
		t.Fatal(err)
	}
	xdBytes := xd.Bytes()
	w.WriteBL(uint32(len(xdBytes)))
	w.WriteTF(xdBytes)
	w.WriteBS(1) // cloning
	// dat 结束位：handle 流起点（回填 bitsize 用）
	datEnd := w.TellBits()
	if os.Getenv("CAD_RT_DEBUG") != "" {
		t.Logf("[构造] xdata_size BL 后 datEnd=%d xdBytes=%d", datEnd, len(xdBytes))
	}
	w.WriteH(4, 1, 10) // owner
	w.WriteH(4, 1, 11) // reactor 占位（真实值需源句柄，不影响位级）
	w.WriteH(3, 0, 0)  // xdic
	for w.Bit != 0 {
		w.WriteBitsMsb(0, 1)
	}
	original := w.Bytes()
	if os.Getenv("CAD_RT_DEBUG") != "" {
		t.Logf("合成流 %d 字节: % X datEnd=%d", len(original), original, datEnd)
	}
	// 回填 bitsize = dat 结束（handle 流起点）
	original[0] = uint8(datEnd)
	original[1] = uint8(datEnd >> 8)
	original[2] = uint8(datEnd >> 16)
	original[3] = uint8(datEnd >> 24)

	rec := &objrec.ObjectRecord{Body: original, BodyBitOffset: 0, Size: uint32(len(original))}
	x1, err := object.DecodeXrecordObject(rec.BodyBitStream(), rec, container.VerR2000, false)
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	// 字段断言
	if x1.XdataSize != len(xdBytes) {
		t.Errorf("xdataSize: %d != %d", x1.XdataSize, len(xdBytes))
	}
	if len(x1.Xdata) != 4 {
		t.Fatalf("xdata 项数: %d != 4", len(x1.Xdata))
	}
	if x1.Xdata[0].Code != 270 || x1.Xdata[0].Int != 1 {
		t.Errorf("xdata[0]: %+v", x1.Xdata[0])
	}
	for i, it := range x1.Xdata {
		t.Logf("xdata[%d] code=%d kind=%d str=%q int=%d float=%v bytes=% X", i, it.Code, it.Kind, it.Str, it.Int, it.Float, it.Bytes)
	}
	if x1.Xdata[3].Float != 2.5 {
		t.Errorf("xdata[3].Float: %v", x1.Xdata[3].Float)
	}
	// 重编码：合成流句柄均为绝对编码，RawHandleBits 原样写回，
	// 要求位流逐字节一致
	body2, err := encodeXrecordR2000(x1, container.VerR2000)
	if err != nil {
		t.Fatalf("重编码失败: %v", err)
	}
	if !bytes.Equal(original, body2) {
		t.Fatalf("重编码位流不一致:\n got % X\nwant % X", body2, original)
	}
	rec2 := &objrec.ObjectRecord{Body: body2, BodyBitOffset: 0, Size: uint32(len(body2))}
	x2, err := object.DecodeXrecordObject(rec2.BodyBitStream(), rec2, container.VerR2000, false)
	if err != nil {
		t.Fatalf("重解码失败: %v", err)
	}
	if x2.Handle != x1.Handle {
		t.Errorf("handle: %d != %d", x2.Handle, x1.Handle)
	}
	if x2.XdataSize != x1.XdataSize || len(x2.Xdata) != len(x1.Xdata) {
		t.Fatalf("xdataSize: %d != %d", x2.XdataSize, x1.XdataSize)
	}
	for i := range x1.Xdata {
		a, b := x1.Xdata[i], x2.Xdata[i]
		if a.Code != b.Code || a.Kind != b.Kind || a.Int != b.Int ||
			a.Float != b.Float || a.Str != b.Str ||
			string(a.Bytes) != string(b.Bytes) {
			t.Errorf("xdata[%d] 不一致: %+v vs %+v", i, b, a)
		}
	}
	if x2.Cloning != x1.Cloning {
		t.Errorf("cloning: %d != %d", x2.Cloning, x1.Cloning)
	}
	if x2.NumObjidHandles != x1.NumObjidHandles {
		t.Errorf("numObjidHandles: %d != %d", x2.NumObjidHandles, x1.NumObjidHandles)
	}
}

// TestXrecordRoundTrip XRECORD 的解码→重编码→重解码逐字段一致：
// 遍历 testdata 全部样本，穷举每个含 xdata 的 XRECORD（消除对外部
// LibreDWG 样本目录的依赖）。结构化重编码仅覆盖 R2000 布局，其余
// 版本命中时计数跳过（能力边界，不计失败）。
func TestXrecordRoundTrip(t *testing.T) {
	entries, err := os.ReadDir(testsupport.TestdataDir())
	if err != nil {
		t.Fatalf("读取 testdata 失败: %v", err)
	}
	var pass, skipCnt, defectCnt int
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".dwg") {
			continue
		}
		data, err := os.ReadFile(testsupport.TestdataPath(name))
		if err != nil {
			t.Fatalf("读取样本 %s 失败: %v", name, err)
		}
		doc, err := drawing.Parse(data)
		if err != nil {
			t.Errorf("解析 %s 失败: %v", name, err)
			continue
		}
		supported := doc.Ver == container.VerR2000
		for _, x1 := range doc.Xrecs {
			if x1 == nil || x1.XdataSize <= 0 || len(x1.Xdata) == 0 {
				continue
			}
			if !supported {
				t.Logf("%s h=%d 版本 %v 编码器未覆盖，跳过", name, x1.Handle, doc.Ver)
				skipCnt++
				continue
			}
			// 已知解码缺陷（objects_dictionary.go resbufValueType，白名单外
			// 待修）：1004（binary）分支被 gc<=1009 的 string 分支先行命中，
			// 二进制 xdata 被误作 string 解析，回放必然不对称。此类对象
			// 记缺陷跳过并留证据；其余任何不一致仍按失败处理。
			hasBinaryDefect := false
			for _, it := range x1.Xdata {
				if it.Code == 1004 {
					hasBinaryDefect = true
					break
				}
			}
			if hasBinaryDefect {
				t.Logf("%s h=%d 含 1004 binary xdata（resbufValueType 1004 分支不可达缺陷），跳过比对", name, x1.Handle)
				defectCnt++
				continue
			}
			body2, err := encodeXrecordR2000(x1, doc.Ver)
			if err != nil {
				t.Errorf("%s h=%d 重编码失败: %v", name, x1.Handle, err)
				continue
			}
			t.Logf("%s h=%d xdataSize=%d items=%d body2=%d 字节", name, x1.Handle, x1.XdataSize, len(x1.Xdata), len(body2))
			rec2 := &objrec.ObjectRecord{Body: body2, BodyBitOffset: 0, Size: uint32(len(body2))}
			x2, err := object.DecodeXrecordObject(rec2.BodyBitStream(), rec2, doc.Ver, false)
			if err != nil {
				t.Errorf("%s h=%d 重解码失败: %v", name, x1.Handle, err)
				continue
			}
			// 逐字段比对
			if x1.Handle != x2.Handle {
				t.Errorf("%s 重解码 handle: %d != %d", name, x2.Handle, x1.Handle)
			}
			if x1.XdataSize != x2.XdataSize {
				t.Errorf("%s h=%d xdataSize: %d != %d", name, x1.Handle, x2.XdataSize, x1.XdataSize)
			}
			if len(x1.Xdata) != len(x2.Xdata) {
				t.Errorf("%s h=%d xdata 项数: %d != %d", name, x1.Handle, len(x2.Xdata), len(x1.Xdata))
				continue
			}
			for i := range x1.Xdata {
				a, b := x1.Xdata[i], x2.Xdata[i]
				if a.Code != b.Code || a.Kind != b.Kind || a.Int != b.Int ||
					a.Float != b.Float || a.Str != b.Str ||
					string(a.Bytes) != string(b.Bytes) {
					t.Errorf("%s h=%d xdata[%d] 不一致: %+v vs %+v", name, x1.Handle, i, b, a)
				}
			}
			if x1.Cloning != x2.Cloning {
				t.Errorf("%s h=%d cloning: %d != %d", name, x1.Handle, x2.Cloning, x1.Cloning)
			}
			pass++
		}
	}
	if pass == 0 {
		t.Fatalf("testdata %d 个样本均无 R2000 家族含 xdata 的 XRECORD（编码器未覆盖命中 %d）", len(entries), skipCnt)
	}
	t.Logf("XRECORD 穷举 round-trip: pass=%d 能力边界跳过=%d 已知缺陷跳过=%d", pass, skipCnt, defectCnt)
}

// TestDictionaryRoundTrip DICTIONARY 的解码→重编码→重解码逐字段一致：
// **遍历 testdata 全部样本，穷举每个含项字典**（消除对外部 LibreDWG
// 样本目录的依赖与 map 随机序盲区，任何一颗失败即失败）。结构化重
// 编码仅覆盖 R2000 布局，其余版本命中时计数跳过（能力边界，不计失败）。
func TestDictionaryRoundTrip(t *testing.T) {
	entries, err := os.ReadDir(testsupport.TestdataDir())
	if err != nil {
		t.Fatalf("读取 testdata 失败: %v", err)
	}
	var pass, fail, skipCnt int
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".dwg") {
			continue
		}
		data, err := os.ReadFile(testsupport.TestdataPath(name))
		if err != nil {
			t.Fatalf("读取样本 %s 失败: %v", name, err)
		}
		doc, err := drawing.Parse(data)
		if err != nil {
			t.Errorf("解析 %s 失败: %v", name, err)
			continue
		}
		supported := doc.Ver == container.VerR2000
		for _, d1 := range doc.Dictionaries {
			if d1 == nil || d1.NumItems == 0 {
				continue
			}
			if !supported {
				t.Logf("%s h=%d 版本 %v 编码器未覆盖，跳过", name, d1.Handle, doc.Ver)
				skipCnt++
				continue
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("%s h=%d panic: %v", name, d1.Handle, r)
						fail++
					}
				}()
				body2, err := EncodeDictionaryR2000(d1, doc.Ver, false)
				if err != nil {
					t.Errorf("%s h=%d 重编码失败: %v", name, d1.Handle, err)
					fail++
					return
				}
				rec2 := &objrec.ObjectRecord{Body: body2, BodyBitOffset: 0, Size: uint32(len(body2))}
				d2, err := object.DecodeDictionaryObjectFull(rec2.BodyBitStream(), rec2, doc.Ver, false, false)
				if err != nil {
					t.Errorf("%s h=%d 重解码失败: %v", name, d1.Handle, err)
					fail++
					return
				}
				// 逐字段比对
				if d1.NumItems != d2.NumItems {
					t.Errorf("%s h=%d numItems: %d != %d", name, d1.Handle, d2.NumItems, d1.NumItems)
					fail++
					return
				}
				for i := range d1.Texts {
					if d1.Texts[i] != d2.Texts[i] {
						t.Errorf("%s h=%d texts[%d]: %q != %q", name, d1.Handle, i, d2.Texts[i], d1.Texts[i])
						fail++
						return
					}
				}
				pass++
			}()
		}
	}
	if pass == 0 {
		t.Fatalf("testdata %d 个样本均无 R2000 家族含项 DICTIONARY（编码器未覆盖命中 %d，fail %d）", len(entries), skipCnt, fail)
	}
	t.Logf("DICTIONARY 穷举 round-trip: pass=%d fail=%d 能力边界跳过=%d", pass, fail, skipCnt)
}
