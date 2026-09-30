// entities_mleader_blk_test MULTILEADER ctx 内容联合 blk 分支的合成位流
// 门禁：真实九样本均为 txt 分支（has_content_txt=1），blk 分支（块参照
// 内容）按 dwg2.spec MLEADER_CONTEXT_DATA_fields 的 else 路径构造已知
// 位流，解码后逐字段断言——has_content_blk 位、normal/location/scale/
// rotation/color/transform 七个 dat 流字段，以及不误读后续 base 组。
package entity

import (
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"testing"
)

// TestMLeaderContextBlkContent blk 内容分支合成验证（verR2000 布局：
// CMC 为 BS 索引单形态、无字符串流）。
func TestMLeaderContextBlkContent(t *testing.T) {
	w3bd := func(w *bitstream.EncWriter, X, Y, Z float64) {
		w.WriteBD(X)
		w.WriteBD(Y)
		w.WriteBD(Z)
	}
	w := bitstream.NewEncWriter()
	// ctx 标量组
	w.WriteBD(1.0)   // scaleFactor
	w3bd(w, 0, 0, 0) // contentBase
	w.WriteBD(2.0)   // textHeight
	w.WriteBD(0.5)   // arrowSize
	w.WriteBD(0.1)   // landingGap
	w.WriteBS(0)     // textLeft
	w.WriteBS(0)     // textRight
	w.WriteBS(1)     // textAngletype
	w.WriteBS(0)     // textAlignment
	w.WriteB(false)  // has_content_txt = 0 → 走 else 联合
	w.WriteB(true)   // has_content_blk = 1
	// blk 七字段
	w3bd(w, 0, 0, 1)  // normal
	w3bd(w, 5, 6, 7)  // location
	w3bd(w, 2, 2, 2)  // scale
	w.WriteBD(1.5708) // rotation
	w.WriteBS(3)      // color CMC 索引
	for i := 0; i < 16; i++ {
		w.WriteBD(float64(i) * 0.25) // transform BD×16
	}
	// base 三点 + is_normal_reversed（blk 段必须精确消费后才能对齐）
	w3bd(w, 9, 8, 7)
	w3bd(w, 0, 1, 0)
	w3bd(w, 1, 0, 0)
	w.WriteB(true)

	m := &EntMLeader{}
	if err := DecodeMLeaderContext(bitstream.NewBitStream(w.Bytes()), m, container.VerR2000, 0, nil); err != nil {
		t.Fatalf("decodeMLeaderContext 失败: %v", err)
	}
	c := &m.Ctx
	if c.HasContentTxt || !c.HasContentBlk {
		t.Fatalf("内容分支: hasTxt=%v hasBlk=%v（应为 blk 分支）", c.HasContentTxt, c.HasContentBlk)
	}
	b := &c.Blk
	if !testsupport.NearEq(b.Normal.Z, 1) || !testsupport.NearEq(b.Location.X, 5) || !testsupport.NearEq(b.Location.Y, 6) || !testsupport.NearEq(b.Location.Z, 7) {
		t.Errorf("blk normal/location = %v / %v", b.Normal, b.Location)
	}
	if !testsupport.NearEq(b.Scale.X, 2) || !testsupport.NearEq(b.Scale.Z, 2) {
		t.Errorf("blk scale = %v", b.Scale)
	}
	if !testsupport.NearEq(b.Rotation, 1.5708) {
		t.Errorf("blk rotation = %v", b.Rotation)
	}
	if b.Color.Index != 3 {
		t.Errorf("blk color index = %d", b.Color.Index)
	}
	for i := 0; i < 16; i++ {
		if !testsupport.NearEq(b.Transform[i], float64(i)*0.25) {
			t.Fatalf("transform[%d] = %v", i, b.Transform[i])
		}
	}
	// 联合段后的 base 组必须精确对齐（位序无误的证据）
	if !testsupport.NearEq(c.Base.X, 9) || !testsupport.NearEq(c.Base.Y, 8) || !testsupport.NearEq(c.Base.Z, 7) {
		t.Errorf("base = %v（blk 段消费错位）", c.Base)
	}
	if !c.IsNormalReversed {
		t.Error("is_normal_reversed = false, want true")
	}
}
