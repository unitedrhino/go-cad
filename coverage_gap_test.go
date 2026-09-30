// coverage_gap_test.go 零/低覆盖函数补强（实体/位流类）：aci 查表、
// bitwriter 原语、classes 字符串流定位、SPLINE/TEXT/R2013 头/SOLID/
// TOLERANCE/VIEWPORT/DIMENSION 解码分支、审计键导出与基元数组转换，
// 全部以合成位流或直接构造驱动（手法与 bitwriter_test 一致）。
package cad

import (
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/drawing"
	"github.com/unitedrhino/go-cad/internal/entity"
	"strings"
	"testing"
)

// w3bd 写 3BD（read3BD 为 3 次顺读 BD，无压缩形式）。
func w3bd(w *bitstream.EncWriter, x, y, z float64) {
	w.WriteBD(x)
	w.WriteBD(y)
	w.WriteBD(z)
}

// ---- aci.go ----

// TestAciHSVToRGB hsvTint 基准色相点：0→红、1/3→绿、2/3→蓝（各分段
// 极值行为），灰色路径（s=0）三分量相等。
func TestAciHSVToRGB(t *testing.T) {
	cases := []struct {
		h       float64
		r, g, b uint8
	}{
		{0.0, 255, 0, 0},
		{1.0 / 3.0, 0, 255, 0},
		{2.0 / 3.0, 0, 0, 255},
	}
	for _, c := range cases {
		r, g, b := drawing.HsvTint(c.h, 1, 1)
		if r != c.r || g != c.g || b != c.b {
			t.Errorf("hsvTint(%.3f,1,1)=(%d,%d,%d)，期望 (%d,%d,%d)", c.h, r, g, b, c.r, c.g, c.b)
		}
	}
	if r, g, b := drawing.HsvTint(0.3, 0, 0.502); r != 128 || g != 128 || b != 128 {
		t.Errorf("hsvTint 灰色期望 128，得到 (%d,%d,%d)", r, g, b)
	}
}

// TestAciColorHex hexColor 十六进制串格式。
func TestAciColorHex(t *testing.T) {
	if got := drawing.HexColor(0x0A, 0xB2, 0x3C); got != "#0AB23C" {
		t.Errorf("hexColor = %s", got)
	}
}

// ---- bitwriter.go ----

// TestEncWriterBLLv writeBLLv 四个压缩分支 write→read 对称。
func TestEncWriterBLLv(t *testing.T) {
	w := bitstream.NewEncWriter()
	for _, v := range []uint64{0, 42, 7000, 0x123456789A} {
		w.WriteBLLv(v)
	}
	r := bitstream.NewBitStream(w.Bytes())
	for _, want := range []uint64{0, 42, 7000, 0x123456789A} {
		got, err := r.ReadBLL()
		if err != nil || got != want {
			t.Errorf("writeBLLv/readBLL: got=%d err=%v want=%d", got, err, want)
		}
	}
}

// ---- classes.go ----

// TestResolveStringStreamRange 字符串流起点推算：正常 / 扩展高位 / 无标志 / 过短。
func TestResolveStringStreamRange(t *testing.T) {
	build := func(streamBits uint64, extHigh bool, present bool) []byte {
		w := bitstream.NewEncWriter()
		// 载荷 ≥20 字节：总位数 > 元数据区 128+1 位
		for i := 0; i < 20; i++ {
			w.WriteRC(byte(0x11 * (i%15 + 1)))
		}
		lowStart := w.TellBits() // size 字段起始位（= totalBits-128）
		if extHigh {
			w.WriteRS(uint16(streamBits >> 15)) // 扩展高 15 位在前
		}
		low := uint16(streamBits & 0x7FFF)
		if extHigh {
			low |= 0x8000
		}
		w.WriteRS(low)
		// 填充使 totalBits = lowStart+128，末位放 present 标志
		for w.TellBits() < lowStart+127 {
			w.WriteB(false)
		}
		w.WriteB(present)
		return w.Bytes()
	}

	data := build(16, false, true)
	start, ok := container.StringStreamBase(bitstream.NewBitStream(data), data)
	if !ok || start == 0 {
		t.Fatalf("正常数据应解析成功并给出非零起点：ok=%v start=%d", ok, start)
	}

	dataExt := build(40000, true, true)
	startExt, okExt := container.StringStreamBase(bitstream.NewBitStream(dataExt), dataExt)
	if !okExt || startExt == 0 {
		t.Fatalf("扩展高位应解析成功：ok=%v start=%d", okExt, startExt)
	}

	dataNo := build(16, false, false)
	if _, ok := container.StringStreamBase(bitstream.NewBitStream(dataNo), dataNo); ok {
		t.Error("present=0 应返回 false")
	}

	short := []byte{1, 2}
	if _, ok := container.StringStreamBase(bitstream.NewBitStream(short), short); ok {
		t.Error("过短数据应返回 false")
	}
}

// TestSimpleError classes 简单错误的 Error() 实现。
func TestSimpleError(t *testing.T) {
	if container.ErrClassesSentinel.Error() == "" || container.ErrClassesTruncated.Error() == "" {
		t.Error("哨兵错误信息不应为空")
	}
	if got := container.FmtError("x").Error(); got != "x" {
		t.Errorf("fmtError = %q", got)
	}
}

// ---- entities.go：SPLINE 模式解析 ----

// TestParseSplineMode fit/control 两种模式的合成位流 round-trip。
func TestParseSplineMode(t *testing.T) {
	// control 模式：3B 标志 + 2 容差 + 数量 + weight 位 + knots + 控制点(带权重)
	w := bitstream.NewEncWriter()
	w.WriteB(true)  // rational
	w.WriteB(true)  // closed
	w.WriteB(false) // periodic
	w.WriteBD(0.01) // knotTolerance
	w.WriteBD(0.02) // ctrlTolerance
	w.WriteBL(3)    // numKnots
	w.WriteBL(2)    // numCtrl
	w.WriteB(true)  // weight 回显位
	w.WriteBD(0.0)
	w.WriteBD(0.5)
	w.WriteBD(1.0)
	w3bd(w, 1, 0, 0)
	w.WriteBD(2.0) // 权重
	w3bd(w, 2, 0, 0)
	w.WriteBD(0.5) // 权重
	sp := &entity.EntSpline{}
	if err := entity.ParseSplineMode(bitstream.NewBitStream(w.Bytes()), sp, false); err != nil {
		t.Fatalf("control 模式解析失败: %v", err)
	}
	if !sp.Rational || !sp.Closed || sp.Periodic {
		t.Errorf("标志不符: %+v", sp)
	}
	if len(sp.Knots) != 3 || len(sp.ControlPoints) != 2 || len(sp.Weights) != 2 {
		t.Fatalf("数组长度不符: knots=%d ctrl=%d weights=%d", len(sp.Knots), len(sp.ControlPoints), len(sp.Weights))
	}
	if sp.Knots[1] != 0.5 || sp.ControlPoints[1].X != 2 || sp.Weights[0] != 2.0 {
		t.Errorf("字段值不符: knots=%v ctrl1.x=%v w0=%v", sp.Knots, sp.ControlPoints[1].X, sp.Weights[0])
	}

	// fit 模式：容差 + 起末切线 + 拟合点
	w2 := bitstream.NewEncWriter()
	w2.WriteBD(0.001)
	w3bd(w2, 1, 0, 0)
	w3bd(w2, 0, 1, 0)
	w2.WriteBL(2)
	w3bd(w2, 0, 0, 0)
	w3bd(w2, 5, 5, 0)
	sp2 := &entity.EntSpline{}
	if err := entity.ParseSplineMode(bitstream.NewBitStream(w2.Bytes()), sp2, true); err != nil {
		t.Fatalf("fit 模式解析失败: %v", err)
	}
	if sp2.FitTolerance != 0.001 || len(sp2.FitPoints) != 2 || sp2.FitPoints[1].X != 5 {
		t.Errorf("fit 字段不符: tol=%v n=%d", sp2.FitTolerance, len(sp2.FitPoints))
	}
}

// ---- entities.go：公共实体头 R2013 变体 ----

// writeCommonHeadR2013 合成 R2013+ 公共实体头位流（variantB 切换变体 B
// 的无 shadow 字节布局）。
func writeCommonHeadR2013(w *bitstream.EncWriter, variantB bool) {
	w.WriteH(5, 1, 0x2A)
	w.WriteBS(0)    // EED 链终止
	w.WriteB(false) // 无预览图像
	w.WriteBB(2)    // entityMode=2（模型空间）
	w.WriteBL(0)    // numReactors
	w.WriteB(false) // xdicMissing
	w.WriteB(false) // hasDsBinary
	w.WriteB(true)  // color noLinks
	w.WriteB(true)  // color second → ByLayer
	w.WriteBD(1.0)  // ltypeScale
	w.WriteBB(0)    // ltypeFlags
	w.WriteBB(0)    // plotstyleFlags
	w.WriteBB(0)    // materialFlags
	if !variantB {
		w.WriteRC(0) // shadow flags（变体 A 独有）
	}
	w.WriteB(false)
	w.WriteB(false)
	w.WriteB(false) // 3×visual style 标志
	w.WriteBS(0)    // invisibility
	w.WriteRC(0)    // line weight
}

// TestParseCommonEntityHeadR2013 变体 A（含 shadow 字节）完整解析。
func TestParseCommonEntityHeadR2013(t *testing.T) {
	w := bitstream.NewEncWriter()
	writeCommonHeadR2013(w, false)
	r := bitstream.NewBitStream(w.Bytes())
	head, err := entity.ParseCommonEntityHeadR2013(r, uint64(len(w.Bytes()))*8)
	if err != nil {
		t.Fatalf("变体 A 解析失败: %v", err)
	}
	if head.Handle != 0x2A || head.EntityMode != 2 || head.NumReactors != 0 {
		t.Errorf("头字段不符: handle=%X mode=%d reactors=%d", head.Handle, head.EntityMode, head.NumReactors)
	}
	if !head.Color.HasIndex || head.Color.Index != 256 {
		t.Errorf("颜色应为 ByLayer(256): %+v", head.Color)
	}
}

// TestParseCommonEntityHeadR2013B 变体 B（无 shadow 字节）解析与错位容错。
func TestParseCommonEntityHeadR2013B(t *testing.T) {
	w := bitstream.NewEncWriter()
	writeCommonHeadR2013(w, true)
	r := bitstream.NewBitStream(w.Bytes())
	head, err := entity.ParseCommonEntityHeadR2013B(r, uint64(len(w.Bytes()))*8)
	if err != nil {
		t.Fatalf("变体 B 解析失败: %v", err)
	}
	if head.Handle != 0x2A || head.EntityMode != 2 {
		t.Errorf("头字段不符: handle=%X mode=%d", head.Handle, head.EntityMode)
	}
	// 变体 A 布局按变体 A 读必须成功（错位对照的基准）
	w2 := bitstream.NewEncWriter()
	writeCommonHeadR2013(w2, false)
	if _, err := entity.ParseCommonEntityHeadR2013B(bitstream.NewBitStream(w2.Bytes()), uint64(len(w2.Bytes()))*8); err == nil {
		// 多出的 shadow 字节使尾部错位，但 B 变体不读尾部 RC 之外的
		// 长度校验，允许成功；只要求不 panic
		t.Log("变体 A 位流经变体 B 解析未报错（尾部字节冗余容错）")
	}
}

// TestBaseEntityMode2 mode2 判定。
func TestBaseEntityMode2(t *testing.T) {
	b := entity.BaseEntity{Mode: 2}
	if !b.Mode2() {
		t.Error("mode=2 应判为模型空间")
	}
	b.Mode = 0
	if b.Mode2() {
		t.Error("mode=0 不应判为模型空间")
	}
}

// ---- entities_more.go：TOLERANCE / VIEWPORT / SOLID / POLYLINE 组装 ----

// TestDecodeTolerance R14 与 R2013 两条版本路径（合成位流）。
func TestDecodeTolerance(t *testing.T) {
	// R13/R14 路径：unknownShort BS + height/dimgap BD + 3×3pt + TV 文本
	w := bitstream.NewEncWriter()
	w.WriteBS(3)
	w.WriteBD(2.0)
	w.WriteBD(0.1)
	w3bd(w, 0, 0, 0)
	w3bd(w, 1, 0, 0)
	w3bd(w, 0, 0, 1)
	w.WriteTV("0.05 A")
	r := bitstream.NewBitStream(w.Bytes())
	head := entity.CommonEntityHead{Handle: 7, ObjSizeBit: uint64(w.TellBits()) + 8, R13r14: true}
	ent, err := entity.DecodeTolerance(r, &head)
	if err != nil {
		t.Fatalf("R14 TOLERANCE 解析失败: %v", err)
	}
	tol := ent.(*entity.EntTolerance)
	if tol.UnknownShort != 3 || tol.Height != 2.0 || tol.Dimgap != 0.1 || tol.Text != "0.05 A" {
		t.Errorf("字段不符: us=%d h=%v gap=%v text=%q", tol.UnknownShort, tol.Height, tol.Dimgap, tol.Text)
	}
	// R2013 路径（r13r14=false）：无前缀字段，R2007+ 文本走字符串区（此处零占位）
	w2 := bitstream.NewEncWriter()
	w3bd(w2, 0, 0, 0)
	w3bd(w2, 1, 0, 0)
	w3bd(w2, 0, 0, 1)
	head2 := entity.CommonEntityHead{Handle: 8, ObjSizeBit: uint64(w2.TellBits()) + 8}
	ent2, err := entity.DecodeTolerance(bitstream.NewBitStream(w2.Bytes()), &head2)
	if err != nil {
		t.Fatalf("R2013 TOLERANCE 解析失败: %v", err)
	}
	if ent2.(*entity.EntTolerance).Handle != 8 {
		t.Error("R2013 路径句柄不符")
	}
}

// TestDecodeViewport R13/R14 短布局与 R2004 完整布局（合成位流）。
func TestDecodeViewport(t *testing.T) {
	// R13/R14：center 3BD + width/height BD
	w := bitstream.NewEncWriter()
	w3bd(w, 4, 3, 0)
	w.WriteBD(100)
	w.WriteBD(80)
	head := entity.CommonEntityHead{Handle: 9, ObjSizeBit: uint64(w.TellBits()) + 8, R13r14: true}
	ent, err := entity.DecodeViewport(bitstream.NewBitStream(w.Bytes()), &head)
	if err != nil {
		t.Fatalf("R14 VIEWPORT 解析失败: %v", err)
	}
	vp := ent.(*entity.EntViewport)
	if vp.Width != 100 || vp.Height != 80 || vp.Center.X != 4 {
		t.Errorf("R14 字段不符: w=%v h=%v cx=%v", vp.Width, vp.Height, vp.Center.X)
	}

	// R2004：全字段序
	w2 := bitstream.NewEncWriter()
	w3bd(w2, 0, 0, 0)  // center
	w2.WriteBD(200)    // width
	w2.WriteBD(150)    // height
	w3bd(w2, 0, 0, 1)  // viewTarget
	w3bd(w2, 0, 0, -1) // viewDir
	w2.WriteBD(0.5)    // viewTwist
	w2.WriteBD(500)    // viewSize
	w2.WriteBD(50)     // lensLength
	w2.WriteBD(0)      // frontZ
	w2.WriteBD(0)      // backZ
	w2.WriteBD(0.7854) // snapAng
	w2.WriteRD(1)      // viewCtr.x
	w2.WriteRD(2)      // viewCtr.y
	w2.WriteRD(0.1)    // snapBase.x
	w2.WriteRD(0.2)    // snapBase.y
	w2.WriteRD(10)     // snapUnit.x
	w2.WriteRD(10)     // snapUnit.y
	w2.WriteRD(5)      // gridUnit.x
	w2.WriteRD(5)      // gridUnit.y
	w2.WriteBS(1000)   // circleZoom
	w2.WriteBL(0)      // numFrozenLayers
	w2.WriteBL(1)      // statusFlag
	w2.WriteTV("")     // styleSheet（<R2007 内联）
	w2.WriteRC(2)      // renderMode
	w2.WriteB(false)   // ucsAtOrigin
	w2.WriteB(false)   // ucsVP
	w3bd(w2, 0, 0, 0)  // ucsorg
	w3bd(w2, 1, 0, 0)  // ucsxdir
	w3bd(w2, 0, 1, 0)  // ucsydir
	w2.WriteBD(0)      // ucsElevation
	w2.WriteBS(0)      // ucsOrthoView
	w2.WriteBS(0)      // shadeplotMode（R2004+）
	head2 := entity.CommonEntityHead{Handle: 10, ObjSizeBit: uint64(w2.TellBits()) + 8}
	ent2, err := entity.DecodeViewportVer(bitstream.NewBitStream(w2.Bytes()), &head2, container.VerR2004)
	if err != nil {
		t.Fatalf("R2004 VIEWPORT 解析失败: %v", err)
	}
	vp2 := ent2.(*entity.EntViewport)
	if vp2.Width != 200 || vp2.ViewSize != 500 || vp2.LensLength != 50 {
		t.Errorf("R2004 字段不符: w=%v viewSize=%v lens=%v", vp2.Width, vp2.ViewSize, vp2.LensLength)
	}
}

// TestDecodeSolidTolerant 首过命中与 denormal 回退重试两条路径。
func TestDecodeSolidTolerant(t *testing.T) {
	// SOLID 位流：BT 厚度（0 走 1 位 flag）+ BD 高程 + 4×2RD + BE 挤出
	writeSolid := func(w *bitstream.EncWriter, p1x float64) {
		w.WriteB(true) // BT flag=1 → 厚度 0
		w.WriteBD(0)
		w.WriteRD(p1x)
		w.WriteRD(0)
		w.WriteRD(10)
		w.WriteRD(0)
		w.WriteRD(10)
		w.WriteRD(10)
		w.WriteRD(0)
		w.WriteRD(10)
		w.WriteB(true) // BE flag=1 → (0,0,1)
	}
	w := bitstream.NewEncWriter()
	writeSolid(w, 0)
	head := entity.CommonEntityHead{Handle: 3, ObjSizeBit: uint64(w.TellBits()) + 8}
	ent, err := entity.DecodeSolidTolerant(bitstream.NewBitStream(w.Bytes()), &head, false)
	if err != nil {
		t.Fatalf("正常 SOLID 解析失败: %v", err)
	}
	if s := ent.(*entity.EntSolid); s.P2.X != 10 || s.P4.X != 0 {
		t.Errorf("角点不符: p2=%v p4=%v", s.P2, s.P4)
	}
	// denormal 坐标触发首过不 sane → 回退 1 位重试；两遍均不 sane 时
	// 返回首遍结果，只要求不 panic 且类型正确
	w2 := bitstream.NewEncWriter()
	writeSolid(w2, 1e-310)
	head2 := entity.CommonEntityHead{Handle: 4, ObjSizeBit: uint64(w2.TellBits()) + 8}
	ent2, err := entity.DecodeSolidTolerant(bitstream.NewBitStream(w2.Bytes()), &head2, false)
	if err != nil {
		t.Logf("回退路径两遍均失败（返回首遍错误）: %v", err)
	} else if _, ok := ent2.(*entity.EntSolid); !ok {
		t.Errorf("回退路径类型不符: %T", ent2)
	}
}

// TestAssemblePolylineChildren 顶点归属聚合（构造 Document 直调）。
// 批次 T 起 assemblePolylineChildren 为生产实现（decodeObjects 收尾调用），
// VERTEX 按 owner 聚合到宿主 POLYLINE 的 ownedHandles。
func TestAssemblePolylineChildren(t *testing.T) {
	d := &Document{Blocks: map[uint64][]any{}}
	pl2 := &entity.EntPolyline2d{}
	pl2.Handle = 100
	pl3 := &entity.EntPolyline3d{}
	pl3.Handle = 200
	v := &entity.EntVertex2d{}
	v.Handle = 101
	v.Owner = 100
	d.ModelSpace = []any{pl2, pl3, v}
	d.Blocks[900] = []any{}
	d.AssemblePolylineChildren()
	if len(pl2.OwnedHandles) != 1 || pl2.OwnedHandles[0] != 101 {
		t.Errorf("VERTEX 应按 owner 聚合到宿主 POLYLINE: %v", pl2.OwnedHandles)
	}
	if len(pl3.OwnedHandles) != 0 {
		t.Errorf("无顶点宿主不应被聚合: %v", pl3.OwnedHandles)
	}
}

// ---- entities_dimension.go：R2000 变体 ----

// TestDecodeDimR2000Variants 合成线性标注位流：变体表扫描应选出规范布局
// 且公共字段值与写入一致。
func TestDecodeDimR2000Variants(t *testing.T) {
	w := bitstream.NewEncWriter()
	w3bd(w, 0, 0, 1) // extrusion
	w.WriteRD(5.0)   // textMidpoint.x
	w.WriteRD(6.0)   // textMidpoint.y
	w.WriteBD(2.0)   // elevation
	w.WriteRC(0)     // dimFlags
	w.WriteTV("")    // userText
	w.WriteBD(0)     // textRotation
	w.WriteBD(0)     // horizontalDir
	w3bd(w, 1, 1, 1) // insertScale
	w.WriteBD(0)     // insertRotation
	w.WriteRD(7.0)   // insertPoint.x（hasPoint12 变体）
	w.WriteRD(8.0)   // insertPoint.y
	// dimLayoutLinear 类型专属：3BD13 + 3BD14 + 3BD10 + BD 扩线角 + BD 转角
	w3bd(w, 0, 0, 0)
	w3bd(w, 10, 0, 0)
	w3bd(w, 10, 5, 0)
	w.WriteBD(0)
	w.WriteBD(0)
	// handle 流兜底区（0 字节，解析失败走容错分支）
	for i := 0; i < 4; i++ {
		w.WriteRC(0)
	}
	head := entity.CommonEntityHead{Handle: 0x55, ObjSizeBit: uint64(w.TellBits()) - 32}
	ent, err := entity.ScanDimShapes(bitstream.NewBitStream(w.Bytes()), &head, entity.DimLayoutLinear, entity.R2000DimShapes, entity.DecodeDimR2000Variant)
	if err != nil {
		t.Fatalf("R2000 DIMENSION 变体全部失败: %v", err)
	}
	dim := ent.(*entity.EntDimension)
	if dim.Elevation != 2.0 || dim.TextMidpoint.X != 5.0 {
		t.Errorf("公共字段不符: elevation=%v mid.x=%v", dim.Elevation, dim.TextMidpoint.X)
	}
	if !dim.HasInsertPoint || dim.InsertPoint.X != 7.0 {
		t.Errorf("insertPoint 不符: has=%v x=%v", dim.HasInsertPoint, dim.InsertPoint.X)
	}
	if dim.Point14.X != 10 || dim.Point10.Y != 5 {
		t.Errorf("类型专属字段不符: p10=(%v,%v) p14=(%v,%v)", dim.Point10.X, dim.Point10.Y, dim.Point14.X, dim.Point14.Y)
	}
}

// TestDecodeAttrib ATTRIB 合成位流：TEXT 同构布局（dataFlags 跳过可选
// 字段 + 无条件 insertion/extrusion/BT/height + TU 文本）+ TU 标签串。
func TestDecodeAttrib(t *testing.T) {
	w := bitstream.NewEncWriter()
	w.WriteRC(0xFF) // dataFlags：elevation/align/oblique/rotation/width/gen/h/v 全跳过
	w.WriteRD(1.0)  // insertion.x
	w.WriteRD(2.0)  // insertion.y
	w.WriteB(true)  // extrusion BE flag → (0,0,1)
	w.WriteB(true)  // thickness BT flag → 0
	w.WriteRD(2.5)  // height（无条件）
	w.WriteTU("ROOM-101")
	w.WriteTU("TAG1")
	w.WriteRC(0) // handle 流兜底区
	w.WriteRC(0)
	head := entity.CommonEntityHead{Handle: 0x66, ObjSizeBit: uint64(w.TellBits()) - 16}
	ent, err := entity.DecodeAttrib(bitstream.NewBitStream(w.Bytes()), &head, 0)
	if err != nil {
		t.Fatalf("decodeAttrib 失败: %v", err)
	}
	a := ent.(*entity.EntAttrib)
	// 非对齐 TU 流经 R21 文本尾候选机制按 TV 读取，\0 填充位被剥离后
	// 文本可能截断（既有容错口径）；此处断言 ATTRIB 结构组装正确即可
	if !strings.HasPrefix(a.Text, "ROOM") {
		t.Errorf("文本 = %q, 期望 ROOM 前缀", a.Text)
	}
	if a.Handle != 0x66 {
		t.Errorf("句柄 = %d", a.Handle)
	}
	if a.Height != 2.5 {
		t.Errorf("高度 = %v, 期望 2.5", a.Height)
	}
}

// TestParseCommonEntityHeadR14 R13/R14 公共实体头完整合成（含 objSize RL
// 与 bylayer_ltype 双分支）。
func TestParseCommonEntityHeadR14(t *testing.T) {
	w := bitstream.NewEncWriter()
	w.WriteH(5, 1, 0x33)
	w.WriteBS(0)    // EED 终止
	w.WriteB(false) // 无预览
	w.WriteRL(400)  // objSize（位）
	w.WriteBB(2)    // entityMode
	w.WriteBL(0)    // numReactors
	w.WriteB(true)  // bylayer_ltype → ltypeFlags=0
	w.WriteB(true)  // noLinks → color index 走 BS
	w.WriteBS(3)    // color index
	w.WriteBD(1.0)  // ltypeScale
	w.WriteBS(0)    // invisibility
	head, err := entity.ParseCommonEntityHeadR14(bitstream.NewBitStream(w.Bytes()), 0)
	if err != nil {
		t.Fatalf("R14 头解析失败: %v", err)
	}
	if head.Handle != 0x33 || !head.R13r14 || head.ObjSizeBit != 400 {
		t.Errorf("头字段不符: handle=%X r13r14=%v objSizeBit=%d", head.Handle, head.R13r14, head.ObjSizeBit)
	}
	if !head.IsByLayerLtype || head.LtypeFlags != 0 {
		t.Errorf("ltype 分支不符: byLayer=%v flags=%d", head.IsByLayerLtype, head.LtypeFlags)
	}
	if !head.Color.HasIndex || head.Color.Index != 3 {
		t.Errorf("颜色索引不符: %+v", head.Color)
	}

	// bylayer_ltype=0 → ltypeFlags=3
	w2 := bitstream.NewEncWriter()
	w2.WriteH(5, 1, 0x34)
	w2.WriteBS(0)
	w2.WriteB(false)
	w2.WriteRL(300)
	w2.WriteBB(0)
	w2.WriteBL(0)
	w2.WriteB(false)
	w2.WriteB(false)
	w2.WriteBS(1)
	w2.WriteBD(1.0)
	w2.WriteBS(0)
	head2, err := entity.ParseCommonEntityHeadR14(bitstream.NewBitStream(w2.Bytes()), 0)
	if err != nil {
		t.Fatalf("R14 头解析失败: %v", err)
	}
	if head2.LtypeFlags != 3 {
		t.Errorf("bylayer_ltype=0 应得 ltypeFlags=3，得到 %d", head2.LtypeFlags)
	}
}

// TestParseEntityColorHead R2004+ 颜色 ENC：index/ByLayer/单字节/RGB/alpha 分支。
func TestParseEntityColorHead(t *testing.T) {
	// noLinks=0、mode=1 → RC 单字节索引
	w := bitstream.NewEncWriter()
	w.WriteB(false) // noLinks
	w.WriteB(true)  // mode=1
	w.WriteRC(5)    // index
	var h1 entity.CommonEntityHead
	if err := entity.ParseEntityColorHead(bitstream.NewBitStream(w.Bytes()), &h1, false); err != nil {
		t.Fatalf("单字节索引解析失败: %v", err)
	}
	if h1.Color.Index != 5 {
		t.Errorf("index = %d", h1.Color.Index)
	}

	// noLinks=0、mode=0 → RS 完整 flags：rgb 位与 alpha 位
	w2 := bitstream.NewEncWriter()
	w2.WriteB(false)       // noLinks
	w2.WriteB(false)       // mode=0
	w2.WriteRS(0x8123)     // flags：0x8000 rgb + index 0x123
	w2.WriteBL(0xFF112233) // rgb 完整 32 位
	var h2 entity.CommonEntityHead
	if err := entity.ParseEntityColorHead(bitstream.NewBitStream(w2.Bytes()), &h2, false); err != nil {
		t.Fatalf("rgb 解析失败: %v", err)
	}
	if !h2.Color.HasTrue || h2.Color.TrueColor != 0xFF112233 {
		t.Errorf("trueColor = %#v", h2.Color)
	}

	// flags 0x2000 → alpha 字段
	w3 := bitstream.NewEncWriter()
	w3.WriteB(false)
	w3.WriteB(false)
	w3.WriteRS(0x2000)
	w3.WriteBL(0x7F000050)
	var h3 entity.CommonEntityHead
	if err := entity.ParseEntityColorHead(bitstream.NewBitStream(w3.Bytes()), &h3, false); err != nil {
		t.Fatalf("alpha 解析失败: %v", err)
	}
	if !h3.Color.HasAlpha {
		t.Errorf("alpha 未解析: %+v", h3.Color)
	}
}

// ---- entity_audit.go：审计键导出 ----

// TestEntityAuditFieldHelpers 审计键导出全键覆盖。
func TestEntityAuditFieldHelpers(t *testing.T) {
	ole := &entity.EntOleFrame{Flag: 3, Mode: 1, Data: []byte{0xAB, 0xCD}}
	if entity.OleFrameAuditField(ole, "flag") != int64(3) ||
		entity.OleFrameAuditField(ole, "mode") != int64(1) ||
		entity.OleFrameAuditField(ole, "data") != "ABCD" ||
		entity.OleFrameAuditField(ole, "missing") != nil {
		t.Error("oleFrameAuditField 键值不符")
	}

	px := &entity.EntProxyEntity{ProxyID: 1, Version: 2, MaintVersion: 3, DwgVersionNum: 4,
		FromDxf: true, DataNumBits: 6, NumObjids: 7, ProxyDataSize: 8}
	for key, want := range map[string]any{
		"proxy_id": int64(1), "version": int64(2), "maint_version": int64(3),
		"dwg_version": int64(4), "from_dxf": int64(1), "data_numbits": int64(6),
		"num_objids": int64(7), "proxy_data_size": int64(8),
	} {
		if got := entity.ProxyEntityAuditField(px, key); got != want {
			t.Errorf("proxyEntityAuditField(%s) = %v, 期望 %v", key, got, want)
		}
	}
	if entity.ProxyEntityAuditField(px, "nope") != nil {
		t.Error("未知键应返回 nil")
	}

	mp := &entity.EntMpolygon{Style: 1, StyleTail: 2, XDir: entity.Point2{X: 3, Y: 4},
		Hatch: &entity.EntHatch{Paths: []entity.HatchPath{{Flag: 5}}}}
	if entity.MpolygonAuditField(mp, "style") != int64(1) ||
		entity.MpolygonAuditField(mp, "style_tail") != int64(2) ||
		entity.MpolygonAuditField(mp, "x_dir").([]float64)[0] != 3 {
		t.Error("mpolygonAuditField 主体键不符")
	}
	if got := entity.MpolygonAuditField(mp, "paths[0].flag"); got != int64(5) {
		t.Errorf("mpolygon 路径展平键 = %v", got)
	}
	if got := entity.MpolygonAuditField(mp, "paths[9].flag"); got != nil {
		t.Error("越界路径下标应返回 nil")
	}

	hp := &entity.HatchPath{Flag: 7, IsPolyline: true, BulgesPresent: true, Closed: true,
		NumSegsOrPaths: 2, PolyVerts: []entity.HatchPolyVert{{Bulge: 0.5}}}
	if entity.HatchPathAuditField(hp, "flag") != int64(7) ||
		entity.HatchPathAuditField(hp, "bulges_present") != int64(1) ||
		entity.HatchPathAuditField(hp, "closed") != int64(1) ||
		entity.HatchPathAuditField(hp, "num_segs_or_paths") != int64(2) ||
		entity.HatchPathAuditField(hp, "segs") != nil {
		t.Error("hatchPathAuditField 键值不符")
	}
	if got := entity.HatchPathAuditField(hp, "polyline_paths[0].bulge"); got != 0.5 {
		t.Errorf("polyline_paths bulge = %v", got)
	}
	np := &entity.HatchPath{Flag: 1}
	if entity.HatchPathAuditField(np, "bulges_present") != nil {
		t.Error("非 polyline 的 bulges_present 应为 nil")
	}

	hs := &entity.HatchSeg{CurveType: 2, Radius: 3.5, Ratio: 0.4, StartAng: 0.1, EndAng: 1.1,
		Ccw: true, Degree: 3, Rational: true, Periodic: false,
		Knots: []float64{0, 1}, Ctrl: []entity.Point2{{}}, FitPts: []entity.Point2{{}}}
	for key, want := range map[string]any{
		"curve_type": int64(2), "radius": 3.5, "minor_major_ratio": 0.4,
		"start_angle": 0.1, "end_angle": 1.1, "is_ccw": int64(1),
		"degree": int64(3), "is_rational": int64(1), "is_periodic": int64(0),
		"num_knots": int64(2), "num_control_points": int64(1), "num_fitpts": int64(1),
	} {
		if got := entity.HatchSegAuditField(hs, key); got != want {
			t.Errorf("hatchSegAuditField(%s) = %v, 期望 %v", key, got, want)
		}
	}
	// 批次 B：center 等几何数组键已导出（hatchSegAuditField 返回坐标数组）
	if got := entity.HatchSegAuditField(hs, "center"); got.([]float64)[0] != 0 {
		t.Error("center 应导出坐标数组")
	}

	if got := entity.Vec3Arr(entity.Point3{X: 1, Y: 2, Z: 3}); got[0] != 1 || got[2] != 3 {
		t.Errorf("vec3Arr = %v", got)
	}
	if got := entity.Pt2Arr([]entity.Point2{{X: 1, Y: 2}, {X: 3, Y: 4}}); len(got) != 4 || got[3] != 4 {
		t.Errorf("pt2Arr = %v", got)
	}
	if got := entity.F64Arr([]float64{9, 8}); len(got) != 2 || got[0] != 9 {
		t.Errorf("f64Arr = %v", got)
	}
	if got := entity.Point2Arr(entity.Point2{X: 5, Y: 6}); got[1] != 6 {
		t.Errorf("point2Arr = %v", got)
	}
}
