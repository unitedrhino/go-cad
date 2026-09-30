// encode_entity_test 实体回放编码器的合成位流单测：手工构造已知内容的
// R2000 LINE 记录位流（对齐 parseEntityHead R2000+LW 布局与
// decodeLine 几何），解码 → encodeEntityR200x 回放 → 要求与构造位流
// **逐字节一致**（坐标系不变方案的位级门禁），再重解码比对字段。
package cad

import (
	"bytes"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/objrec"
	"testing"
)

// buildSyntheticLineR2000 构造 R2000 LINE 完整记录 body：返回位流与
// handle 流起点位（datEnd）。RL objSize 先占位，构造完成后按位回填
// （其起点在 BS 类型码之后的位 10 处，非字节对齐）。
func buildSyntheticLineR2000() ([]byte, uint64) {
	w := bitstream.NewEncWriter()
	w.WriteBS(0x13)    // BS 类型码：LINE=0x13（R2000 非 R2010+ 布局）
	w.WriteRL(0)       // RL objSize 占位（单位=位，body 局部 handle 流起点）
	w.WriteH(0, 1, 50) // 主句柄
	w.WriteBS(0)       // EED 链终止
	w.WriteB(false)    // 图形图像不存在
	w.WriteBB(2)       // entmode=2（模型空间）
	w.WriteBL(0)       // num_reactors=0
	w.WriteB(true)     // nolinks 位（R2000 布局该位为 noLinks）
	// 颜色段（parseEntityColorHead）：no_links=0 → mode=1 → RC 索引
	w.WriteB(false)
	w.WriteB(true)
	w.WriteRC(7)
	w.WriteBD(1.0) // ltype_scale
	w.WriteBB(0)   // ltype_flags=0（handle 流不读 ltype 句柄）
	w.WriteBB(0)   // plotstyle_flags=0
	w.WriteBS(0)   // invisibility
	w.WriteRC(25)  // lineweight（R2000+LW 布局尾部 RC）
	// LINE 几何（decodeLine）：z 对 + 差分端点 + BT 厚度 + BE 挤出。
	// DD 字段手工按 readDD 语义写（0=same/3=full RD）：bitwriter.go 的
	// writeDD 与 readDD 分支错位（缺陷另行记录），不使用
	w.WriteB(false) // z_is_zero=0 → 读 z 对
	w.WriteRD(1.0)  // x_start
	w.WriteBB(3)    // x_end：DD full RD
	w.WriteRD(2.0)
	w.WriteRD(3.0) // y_start
	w.WriteBB(3)   // y_end：DD full RD
	w.WriteRD(4.0)
	w.WriteRD(0.0) // z_start
	w.WriteBB(0)   // z_end：DD same as default
	w.WriteB(true) // thickness BT flag=1 → 0
	w.WriteB(true) // extrusion BE flag=1 → (0,0,1)
	datEnd := w.TellBits()
	// handle 流：mode=2 无 owner、reactors=0、xdicMissing=false → xdic + layer
	w.WriteH(4, 1, 10) // xdicobjhandle
	w.WriteH(4, 1, 11) // layer
	for w.Bit != 0 {
		w.WriteBitsMsb(0, 1)
	}
	// 按位回填 RL objSize = datEnd（位 10 起 32 位小端）
	const objSizePos = 10
	for i := 0; i < 4; i++ {
		v := uint8(datEnd >> (8 * i))
		for j := 0; j < 8; j++ {
			idx := objSizePos + 8*i + j
			if (v>>(7-j))&1 == 1 {
				w.Data[idx/8] |= 1 << (7 - idx%8)
			} else {
				w.Data[idx/8] &^= 1 << (7 - idx%8)
			}
		}
	}
	return w.Bytes(), datEnd
}

func TestEntityRoundTripSynthetic(t *testing.T) {
	original, datEnd := buildSyntheticLineR2000()
	// 解码
	rec := &objrec.ObjectRecord{Body: original, BodyBitOffset: 0, Size: uint32(len(original))}
	h, err := objrec.ParseObjHeader(rec)
	if err != nil {
		t.Fatalf("parseObjHeader 失败: %v", err)
	}
	if h.TypeCode != 0x13 {
		t.Fatalf("typeCode: %X != 0x13", h.TypeCode)
	}
	rr := rec.BodyBitStream()
	rr.SetBitPos(h.DataStartBit)
	e1, err := decodeEntityFieldsVer(rr, h, 50, rec.Size, "LINE", 0x13, container.VerR2000, 0, nil, "")
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	ln, ok := e1.(*entLine)
	if !ok {
		t.Fatalf("类型: %T != *entLine", e1)
	}
	if ln.handle != 50 {
		t.Errorf("handle: %d != 50", ln.handle)
	}
	if ln.start.x != 1.0 || ln.start.y != 3.0 || ln.start.z != 0.0 ||
		ln.end.x != 2.0 || ln.end.y != 4.0 || ln.end.z != 0.0 {
		t.Errorf("几何: start=%v end=%v", ln.start, ln.end)
	}
	if ln.color.index != 7 {
		t.Errorf("color.index: %d != 7", ln.color.index)
	}
	if ln.layer != 11 {
		t.Errorf("layer: %d != 11", ln.layer)
	}
	// 位串收集完整性：preBits = BS 类型码前导位，RawHandleBits 从
	// handle 流起点到记录尾
	b := entBase(e1)
	if uint64(len(b.preBits)) != h.DataStartBit {
		t.Errorf("preBits 位长: %d != %d", len(b.preBits), h.DataStartBit)
	}
	if b.objSizeBit != datEnd {
		t.Errorf("objSizeBit: %d != %d", b.objSizeBit, datEnd)
	}
	if uint64(len(b.RawHandleBits)) != uint64(len(original))*8-datEnd {
		t.Errorf("RawHandleBits 位长: %d != %d", len(b.RawHandleBits), uint64(len(original))*8-datEnd)
	}
	if b.r2010Plus {
		t.Errorf("r2010Plus 应为 false")
	}
	// 回放编码：要求与构造位流逐字节一致（位级门禁）
	body2, datEnd2, err := encodeEntityR200x(e1, container.VerR2000)
	if err != nil {
		t.Fatalf("回放编码失败: %v", err)
	}
	if datEnd2 != datEnd {
		t.Errorf("datEnd: %d != %d", datEnd2, datEnd)
	}
	if !bytes.Equal(original, body2) {
		t.Fatalf("回放位流不一致:\n got % X\nwant % X", body2, original)
	}
	// 重解码：字段级一致
	rec2 := &objrec.ObjectRecord{Body: body2, BodyBitOffset: 0, Size: uint32(len(body2))}
	h2, err := objrec.ParseObjHeader(rec2)
	if err != nil {
		t.Fatalf("重解码 parseObjHeader 失败: %v", err)
	}
	rr2 := rec2.BodyBitStream()
	rr2.SetBitPos(h2.DataStartBit)
	e2, err := decodeEntityFieldsVer(rr2, h2, 50, rec2.Size, "LINE", 0x13, container.VerR2000, 0, nil, "")
	if err != nil {
		t.Fatalf("重解码失败: %v", err)
	}
	ln2 := e2.(*entLine)
	if ln2.handle != ln.handle || ln2.layer != ln.layer || ln2.owner != ln.owner ||
		ln2.start != ln.start || ln2.end != ln.end || ln2.color.index != ln.color.index {
		t.Errorf("重解码字段不一致: %+v vs %+v", ln2, ln)
	}
	// entityField 键级一致（got==nil 的键跳过，与审计口径一致）
	for _, k := range []string{"handle", "entmode", "color", "start", "end", "thickness", "z_is_zero", "bitsize"} {
		v1 := entityField(e1, k)
		if v1 == nil {
			continue
		}
		if !entityValueEqual(v1, entityField(e2, k)) {
			t.Errorf("entityField %s: %v != %v", k, entityField(e2, k), v1)
		}
	}
}
