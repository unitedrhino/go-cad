// entities_mleader_blk_test MULTILEADER ctx 内容联合 blk 分支的合成位流
// 门禁：真实九样本均为 txt 分支（has_content_txt=1），blk 分支（块参照
// 内容）按 dwg2.spec MLEADER_CONTEXT_DATA_fields 的 else 路径构造已知
// 位流，解码后逐字段断言——has_content_blk 位、normal/location/scale/
// rotation/color/transform 七个 dat 流字段，以及不误读后续 base 组。
package cad

import "testing"

// TestMLeaderContextBlkContent blk 内容分支合成验证（verR2000 布局：
// CMC 为 BS 索引单形态、无字符串流）。
func TestMLeaderContextBlkContent(t *testing.T) {
	w3bd := func(w *encWriter, x, y, z float64) {
		w.writeBD(x)
		w.writeBD(y)
		w.writeBD(z)
	}
	w := newEncWriter()
	// ctx 标量组
	w.writeBD(1.0)   // scaleFactor
	w3bd(w, 0, 0, 0) // contentBase
	w.writeBD(2.0)   // textHeight
	w.writeBD(0.5)   // arrowSize
	w.writeBD(0.1)   // landingGap
	w.writeBS(0)     // textLeft
	w.writeBS(0)     // textRight
	w.writeBS(1)     // textAngletype
	w.writeBS(0)     // textAlignment
	w.writeB(false)  // has_content_txt = 0 → 走 else 联合
	w.writeB(true)   // has_content_blk = 1
	// blk 七字段
	w3bd(w, 0, 0, 1)  // normal
	w3bd(w, 5, 6, 7)  // location
	w3bd(w, 2, 2, 2)  // scale
	w.writeBD(1.5708) // rotation
	w.writeBS(3)      // color CMC 索引
	for i := 0; i < 16; i++ {
		w.writeBD(float64(i) * 0.25) // transform BD×16
	}
	// base 三点 + is_normal_reversed（blk 段必须精确消费后才能对齐）
	w3bd(w, 9, 8, 7)
	w3bd(w, 0, 1, 0)
	w3bd(w, 1, 0, 0)
	w.writeB(true)

	m := &entMLeader{}
	if err := decodeMLeaderContext(newBitStream(w.bytes()), m, verR2000, 0, nil); err != nil {
		t.Fatalf("decodeMLeaderContext 失败: %v", err)
	}
	c := &m.ctx
	if c.hasContentTxt || !c.hasContentBlk {
		t.Fatalf("内容分支: hasTxt=%v hasBlk=%v（应为 blk 分支）", c.hasContentTxt, c.hasContentBlk)
	}
	b := &c.blk
	if !nearEq(b.normal.z, 1) || !nearEq(b.location.x, 5) || !nearEq(b.location.y, 6) || !nearEq(b.location.z, 7) {
		t.Errorf("blk normal/location = %v / %v", b.normal, b.location)
	}
	if !nearEq(b.scale.x, 2) || !nearEq(b.scale.z, 2) {
		t.Errorf("blk scale = %v", b.scale)
	}
	if !nearEq(b.rotation, 1.5708) {
		t.Errorf("blk rotation = %v", b.rotation)
	}
	if b.color.index != 3 {
		t.Errorf("blk color index = %d", b.color.index)
	}
	for i := 0; i < 16; i++ {
		if !nearEq(b.transform[i], float64(i)*0.25) {
			t.Fatalf("transform[%d] = %v", i, b.transform[i])
		}
	}
	// 联合段后的 base 组必须精确对齐（位序无误的证据）
	if !nearEq(c.base.x, 9) || !nearEq(c.base.y, 8) || !nearEq(c.base.z, 7) {
		t.Errorf("base = %v（blk 段消费错位）", c.base)
	}
	if !c.isNormalReversed {
		t.Error("is_normal_reversed = false, want true")
	}
}
