// layer.go 实现 LAYER 表记录解析：handle → 颜色映射（渲染的图层颜色继承
// 依据）。R2010+ 的图层名存于字符串流，渲染不需要图层名，此处仅解析颜色。
// 颜色 CMC 的字段排列存在版本间变体，采用「变体位模式扫描 + 合理性评分」
// 的消解策略：对每种前后未知位数的排列各自试解，按候选合理性评分择优。
package cad

import (
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/objrec"
)

// layerColor LAYER 记录解析结果。name 为图层真名（DXF 写出与符号表
// 消费方使用；渲染不需要，故历史路径未保存——R13-R2007 在扫描定位时
// 顺手捕获，R2010+ 从对象字符串流补读，JSON/DXF 来源直接来自解析输入）。
type layerColor struct {
	index     uint16
	trueColor uint32
	hasTrue   bool
	name      string
}

// layerCMCLayouts CMC 变体位模式表：每项 3 个位标志编码一种字段排列——
// bit0=flag 前有 2 位前缀、bit1=flag 后有 2 位前缀、bit2=values 前有
// 2 位前缀。表顺序即扫描顺序（评分取最低分，平局取先）。
var layerCMCLayouts = [8]uint8{0x0, 0x1, 0x2, 0x4, 0x3, 0x5, 0x6, 0x7}

// decodeLayerRecord 解析 LAYER 记录的 handle 与颜色（各版本统一入口）。
// 记录级布局（dat 流，LibreDWG dwg_decode_object + dwg.spec LAYER）：
// [R2000~R2007 RL bitsize] + H handle + EED + [R13/R14 RL bitsize] + BL reactors
// + TV 名称 + B is_xref_ref + BS is_xref_resolved + B is_xref_dep（R2004+ 无 ref 位）
// + [R13/R14 B×4 状态位 / R2000+ BS flag0] + CMC 颜色；
// owner/xdic/xref/ltype 等 handle 字段在 bitsize 起的 handle 流，不占 dat 流。
// R2010+ 走 UMC/OT 前缀 + 字符串流名称路径。
func decodeLayerRecord(rec *objrec.ObjectRecord, objHandle uint64, ver dwgVersion) (layerColor, error) {
	if ver == verR13 || ver == verR14 || ver == verR2000 {
		// R13/R14/R2000：dat 流顺序完全确定，直接按 spec 解析并以
		// bitsize 闭环校验；失败时回退历史扫描路径。
		if lc, err := decodeLayerRecordPreR2004(rec, objHandle, ver); err == nil {
			return lc, nil
		}
	}
	if rec.R2010Plus {
		return decodeLayerRecordR2010Plus(rec, objHandle, ver)
	}
	return scanLayerRecordClassic(rec, objHandle, ver)
}

// decodeLayerRecordR2010Plus R2010+ 路径：先走 dat 流确定式解析
// （对照 LibreDWG dwg_decode_object 与 dwg.spec COMMON_TABLE_FLAGS(Layer)，
// 以记录头 UMC 推导的 bitsize 闭环校验），失败回退历史扫描路径
// （类型码前缀 + 8 变体颜色扫描，名称走字符串流）。
func decodeLayerRecordR2010Plus(rec *objrec.ObjectRecord, objHandle uint64, ver dwgVersion) (layerColor, error) {
	if lc, err := parseLayerSpecR2010Plus(rec, objHandle, ver); err == nil {
		return lc, nil
	}
	var lc layerColor
	r := rec.BodyBitStream()
	steps := []func(*bitstream.BitStream) error{
		func(r *bitstream.BitStream) error { _, e := r.ReadUMC(); return e }, // handle-stream-size
		func(r *bitstream.BitStream) error { _, e := r.ReadOT(); return e },  // 类型码
		func(r *bitstream.BitStream) error { _, e := r.ReadH(); return e },   // 记录句柄
		skipLayerEED,
		func(r *bitstream.BitStream) error { _, e := r.ReadBL(); return e }, // reactors
		func(r *bitstream.BitStream) error { _, e := r.ReadB(); return e },  // xdic missing flag
	}
	for _, step := range steps {
		if err := step(r); err != nil {
			return lc, err
		}
	}
	if ver == verR2013 || ver == verR2018 {
		// ds binary 位（仅 R2013+/R2018，R2010 无）
		if _, err := r.ReadB(); err != nil {
			return lc, err
		}
	}
	lc, err := scanLayerColorVariants(r, objHandle)
	if err != nil {
		return lc, err
	}
	lc.name = readR2010PlusLayerName(rec)
	return lc, nil
}

// scanLayerRecordClassic R14/R2000/R2004/R2007 扫描路径：类型码与 handle
// 之间存在版本间长度不确定的字段（如 RL objSize），位置无法静态确定——
// 在 dataStart 起的窗口内逐位偏移扫描「H==记录句柄」的位置，前向解析
// （EED + reactors + xdic + TV 名称）全部通过且 CMC 变体可解时采信；
// 多个位置均可解时取最后一个（误命中多为前部垃圾位型的巧合解码，真实
// handle 位置在其后，经验证 R2004/R2000 样本）。
func scanLayerRecordClassic(rec *objrec.ObjectRecord, objHandle uint64, ver dwgVersion) (layerColor, error) {
	var lc layerColor
	var lastName string
	for delta := uint64(0); delta <= 160; delta++ {
		r := rec.BodyBitStream()
		r.SetBitPos(delta)
		hd, e := r.ReadH()
		if e != nil || hd.Value != objHandle {
			continue
		}
		if e := skipLayerEED(r); e != nil {
			continue
		}
		if _, e := r.ReadBL(); e != nil { // reactors
			continue
		}
		if _, e := r.ReadB(); e != nil { // xdic missing flag
			continue
		}
		nm, e := r.ReadTV(256) // entry name
		if e != nil {
			continue
		}
		cand, e := scanLayerColorVariants(r, objHandle)
		if e != nil {
			continue
		}
		lc = cand
		lastName = nm
	}
	lc.name = lastName
	if ver == verR2007 {
		// R2007 的名称已入字符串流（COMMON_TABLE_FLAGS FIELD_T 自 R2007
		// 起 TU 字符串区存储），主位流扫描读不到——按内联 bitsize 定位
		// 字符串区补读（与通用内部对象解码同款公式）。
		if nm, ok := readLayerNameStringStream(rec); ok {
			lc.name = nm
		}
	}
	if lc.index != 0 || lc.hasTrue {
		return lc, nil
	}
	return lc, fmt.Errorf("cad: LAYER 颜色解析失败（handle %d, %v）", objHandle, ver)
}

// parseLayerSpecR2010Plus R2010~R2018 的 LAYER dat 流确定式解析。
// 布局对照 LibreDWG dwg_decode_object 与 dwg.spec COMMON_TABLE_FLAGS(Layer)：
// H handle + EED + BL num_reactors + B is_xdic_missing（R2004+）
// + [R2013+ B has_ds_data] + BS is_xref_resolved + BS flag0
// + CMC（BS 颜色索引 + BL rgb + RC 标志；R2004+ 真彩形态，与实体 CMC
// 同构，name/book_name 附件走对象字符串流，不占 dat 流）。
// bitsize 无内联字段，由记录头 UMC 推导（dataEndBit，即 handle 流起点）：
// dat 流解析结束位与之相等即整条 dat 流零歧义。名称不在主位流
// （R2007+ FIELD_T 走字符串流），从字符串区首个 TU 补读。
func parseLayerSpecR2010Plus(rec *objrec.ObjectRecord, objHandle uint64, ver dwgVersion) (layerColor, error) {
	var lc layerColor
	h, err := objrec.ParseObjHeader(rec)
	if err != nil {
		return lc, err
	}
	r := rec.BodyBitStream()
	r.SetBitPos(h.DataStartBit)
	hd, err := r.ReadH()
	if err != nil {
		return lc, err
	}
	if hd.Value != objHandle {
		return lc, fmt.Errorf("cad: LAYER 记录句柄不匹配（got %d want %d）", hd.Value, objHandle)
	}
	if err := skipLayerEED(r); err != nil {
		return lc, err
	}
	// 公共字段序列：reactors / xdic missing / [R2013+ ds 位] / xref resolved / flag0
	fields := []func(*bitstream.BitStream) error{
		func(r *bitstream.BitStream) error { _, e := r.ReadBL(); return e },
		func(r *bitstream.BitStream) error { _, e := r.ReadB(); return e },
	}
	if ver == verR2013 || ver == verR2018 {
		fields = append(fields, func(r *bitstream.BitStream) error { _, e := r.ReadB(); return e })
	}
	fields = append(fields,
		func(r *bitstream.BitStream) error { _, e := r.ReadBS(); return e },
		func(r *bitstream.BitStream) error { _, e := r.ReadBS(); return e },
	)
	for _, step := range fields {
		if err := step(r); err != nil {
			return lc, err
		}
	}
	idx, err := r.ReadBS() // CMC 颜色索引
	if err != nil {
		return lc, err
	}
	rgb, err := r.ReadBL() // CMC 真彩（高字节为 method 标记）
	if err != nil {
		return lc, err
	}
	if _, err := r.ReadRC(); err != nil { // CMC 标志字节
		return lc, err
	}
	// bitsize 闭环：R2010+ 无内联 bitsize，由记录头 UMC 推导。dat 流 =
	// 字段区 + 字符串流区（物理在 dat 流尾部）：has_strings 位在
	// dataEndBit-1，data_size RS 在 dataEndBit-17，字符串区起点 =
	// dataEndBit-17-data_size。字段区（CMC 后）结束位与字符串区起点严丝
	// 合缝即整条 dat 流零歧义（has_strings=0 时无字符串区，仅校验字段区
	// 不越过 has_strings 位）。
	endBit := r.TellBits()
	strEnd := rec.DataEndBit()
	if strEnd < 34 {
		return lc, fmt.Errorf("cad: LAYER 记录过短（handle %d）", objHandle)
	}
	rr := rec.BodyBitStream()
	rr.SetBitPos(strEnd - 1)
	hasStrings, err := rr.ReadB()
	if err != nil {
		return lc, err
	}
	strStart := strEnd - 1
	if hasStrings == 1 {
		rr.SetBitPos(strEnd - 17)
		ds, e := rr.ReadRS()
		if e != nil {
			return lc, e
		}
		if ds&0x8000 != 0 {
			// 扩展格式：hi_size RS 在 bitsize-33（对齐 readStringAreaBitRange）
			rr.SetBitPos(strEnd - 33)
			hi, e := rr.ReadRS()
			if e != nil {
				return lc, e
			}
			ds = (ds & 0x7FFF) | (hi << 15)
		}
		strStart = strEnd - 17 - uint64(ds)
	}
	if endBit != strStart {
		return lc, fmt.Errorf("cad: LAYER bitsize 校验失败（字段区结束 %d != 字符串区起点 %d, handle %d）",
			endBit, strStart, objHandle)
	}
	lc.index = idx
	if tc, ok := extractTrueColor(rgb); ok {
		lc.trueColor = tc
		lc.hasTrue = true
	}
	lc.name = readR2010PlusLayerName(rec)
	return lc, nil
}

// extractTrueColor 从 CMC 真彩字段提取 24 位真值：高字节为 0 视为未设置
// （优先索引色），仅保留带标记字节的非零真值。
func extractTrueColor(rgb uint32) (uint32, bool) {
	if rgb == 0 || rgb>>24 == 0 {
		return 0, false
	}
	tc := rgb & 0x00FFFFFF
	return tc, tc != 0
}

// readR2010PlusLayerName R2010+ 表记录名称的字符串流补读：bitsize 由
// 记录头推导，has_strings 位在 bitsize-1，名称是字符串区首个 TU。
// 读取失败返回空串（颜色解析不受影响）。
func readR2010PlusLayerName(rec *objrec.ObjectRecord) string {
	r := rec.BodyBitStream()
	bitsize := rec.DataEndBit() - rec.BodyBitOffset - uint64(rec.HandleSizeFieldBits)
	libreBase := uint64(rec.HandleSizeFieldBits) + rec.BodyBitOffset
	r.SetBitPos(libreBase + bitsize - 1)
	if has, e := r.ReadB(); e == nil && has == 1 {
		if strs := readStringAreaBitRange(r, libreBase+bitsize, 1, true); len(strs) > 0 {
			return strs[0]
		}
	}
	return ""
}

// decodeLayerRecordPreR2004 R13/R14/R2000 的 LAYER dat 流确定式解析。
// 布局对照 LibreDWG dwg_decode_object 与 dwg.spec COMMON_TABLE_FLAGS(Layer)：
// [R2000 RL bitsize] + H handle + EED + [R13/R14 RL bitsize] + BL reactors
// + TV 名称 + B is_xref_ref + BS is_xref_resolved + B is_xref_dep
// + [R13/R14 B frozen/off/frozen_in_new/locked | R2000 BS flag0] + CMC(BS index)。
// bitsize 以 body 起点（BS 类型码之前，不含 MS 尺寸字段）为基准，是 dat 流
// 到 handle 流的边界位：解析结束位 == bitsize 即整条 dat 流零歧义（对全部
// LAYER 记录实测成立）。CMC 为 R2004 前形态（仅 BS 索引，off 时负值；
// 真彩/rgb 段是 R2004+ 才引入）。
func decodeLayerRecordPreR2004(rec *objrec.ObjectRecord, objHandle uint64, ver dwgVersion) (layerColor, error) {
	h, err := objrec.ParseObjHeader(rec)
	if err != nil {
		return layerColor{}, err
	}
	r := rec.BodyBitStream()
	r.SetBitPos(h.DataStartBit)
	var bitsize uint64
	if ver == verR2000 {
		bs, err := r.ReadRL()
		if err != nil {
			return layerColor{}, err
		}
		bitsize = uint64(bs)
	}
	hd, err := r.ReadH()
	if err != nil {
		return layerColor{}, err
	}
	if hd.Value != objHandle {
		return layerColor{}, fmt.Errorf("cad: LAYER 记录句柄不匹配（got %d want %d）", hd.Value, objHandle)
	}
	if err := skipLayerEED(r); err != nil {
		return layerColor{}, err
	}
	if ver == verR13 || ver == verR14 {
		bs, err := r.ReadRL()
		if err != nil {
			return layerColor{}, err
		}
		bitsize = uint64(bs)
	}
	if _, err := r.ReadBL(); err != nil { // num_reactors
		return layerColor{}, err
	}
	nm, err := r.ReadTV(256)
	if err != nil {
		return layerColor{}, err
	}
	// xref 标志组：R2004 前在 dat 流（is_xref_ref 恒 1 的占位位 + resolved + dep）
	for _, step := range []func(*bitstream.BitStream) error{
		func(r *bitstream.BitStream) error { _, e := r.ReadB(); return e },  // is_xref_ref
		func(r *bitstream.BitStream) error { _, e := r.ReadBS(); return e }, // is_xref_resolved
		func(r *bitstream.BitStream) error { _, e := r.ReadB(); return e },  // is_xref_dep
	} {
		if err := step(r); err != nil {
			return layerColor{}, err
		}
	}
	if ver == verR13 || ver == verR14 {
		// R13/R14：frozen/off/frozen_in_new/locked 各 1 位；off 表现为
		// 颜色索引取负（dwg.spec VERSIONS(R_13b1,R_14) DECODER）
		for i := 0; i < 4; i++ {
			if _, err := r.ReadB(); err != nil {
				return layerColor{}, err
			}
		}
	} else {
		// R2000：flag0 位包（frozen/off/frozen_in_new/locked/plotflag/linewt），
		// 渲染只需颜色，位包值不展开
		if _, err := r.ReadBS(); err != nil {
			return layerColor{}, err
		}
	}
	idx, err := r.ReadBS() // CMC：BS 颜色索引（off 时负）
	if err != nil {
		return layerColor{}, err
	}
	// bitsize 闭环：LibreDWG 的 bitsize 以 body 起点（BS 类型码前）为基准，
	// 与 dat 流结束局部位直接相等即零歧义
	if bitsize == 0 || r.TellBits() != bitsize {
		return layerColor{}, fmt.Errorf("cad: LAYER bitsize 校验失败（end %d != %d, handle %d）",
			r.TellBits(), bitsize, objHandle)
	}
	if idx&(1<<15) != 0 {
		// off 图层的索引以 16 位补码负值存储（dwg.spec：off = index < 0）；
		// 本包不建模图层开关，取绝对值保持与 on 图层一致的颜色语义
		idx = ^idx + 1
	}
	return layerColor{index: idx, name: nm}, nil
}

// readLayerNameStringStream R2007 表记录名称的字符串流补读：
// RL bitsize（类型码之后的 dataStartBit 处）→ bitsize-1 位 has_strings →
// 字符串区首个 TU。读取失败返回 ("", false)，调用方回退合成名。
func readLayerNameStringStream(rec *objrec.ObjectRecord) (string, bool) {
	h, err := objrec.ParseObjHeader(rec)
	if err != nil {
		return "", false
	}
	r := rec.BodyBitStream()
	r.SetBitPos(h.DataStartBit)
	bitsize, err := readInlineBitsize(r)
	if err != nil {
		return "", false
	}
	base := rec.BodyBitOffset
	r.SetBitPos(base + bitsize - 1)
	has, err := r.ReadB()
	if err != nil || has != 1 {
		return "", false
	}
	strs := readStringAreaBitRange(r, base+bitsize, 1, false)
	if len(strs) == 0 {
		return "", false
	}
	return strs[0], true
}

// skipLayerEED 跳过 EED 链：BS size 为 0 结束；每项为 H 应用句柄 +
// size 字节原始数据。
func skipLayerEED(r *bitstream.BitStream) error {
	for {
		extSize, err := r.ReadBS()
		if err != nil {
			return err
		}
		if extSize == 0 {
			return nil
		}
		if _, err := r.ReadH(); err != nil {
			return err
		}
		if _, err := r.ReadRCS(int(extSize)); err != nil {
			return err
		}
	}
}

// scanLayerColorVariants 在 CMC 区域按 layerCMCLayouts 全部 8 种变体各自
// 试解，按 layerColorPlausibility 合理性评分择优（越低越可信，平局取先）；
// 全部失败时按最简变体兜底解析以保持推进。
func scanLayerColorVariants(r *bitstream.BitStream, objHandle uint64) (layerColor, error) {
	mark, markBit := r.Cursor()
	var best layerColor
	bestScore := uint64(0)
	found := false
	for _, layout := range layerCMCLayouts {
		r.Restore(mark, markBit)
		idx, tc, hasT, cb, err := decodeLayerCMC(r, layout)
		if err != nil {
			continue
		}
		score := layerColorPlausibility(idx, tc, hasT, cb)
		if !found || score < bestScore {
			found = true
			bestScore = score
			best = layerColor{index: idx, trueColor: tc, hasTrue: hasT}
		}
	}
	if found {
		return best, nil
	}
	r.Restore(mark, markBit)
	idx, tc, hasT, _, err := decodeLayerCMC(r, layerCMCLayouts[0])
	if err != nil {
		return layerColor{}, fmt.Errorf("cad: LAYER 颜色解析失败（handle %d）: %w", objHandle, err)
	}
	return layerColor{index: idx, trueColor: tc, hasTrue: hasT}, nil
}

// decodeLayerCMC 按单一变体位模式解析图层颜色 CMC：
// [flag 前 2 位×bit0] + B flag64 + [flag 后 2 位×bit1] + BS xref +
// B×5 状态位 + [values 前 2 位×bit2] + BS values + BS 颜色索引 +
// BL rgb + RC 颜色字节 [+ TV 名称串×标志位]。
// 注意：颜色索引不做 0x01FF 掩码——保留 BS 原始值（gold 中可见 idx>256）。
func decodeLayerCMC(r *bitstream.BitStream, layout uint8) (uint16, uint32, bool, uint8, error) {
	skipBits := func(n int) error {
		if n > 0 {
			if _, err := r.ReadBitsMsb(uint8(n)); err != nil {
				return err
			}
		}
		return nil
	}
	expectB := func() error {
		_, err := r.ReadB()
		return err
	}
	expectBS := func() error {
		_, err := r.ReadBS()
		return err
	}
	if err := skipBits(2 * int(layout&1)); err != nil {
		return 0, 0, false, 0, err
	}
	if err := expectB(); err != nil { // flag 64
		return 0, 0, false, 0, err
	}
	if err := skipBits(2 * int(layout>>1&1)); err != nil {
		return 0, 0, false, 0, err
	}
	if err := expectBS(); err != nil { // xref index + 1
		return 0, 0, false, 0, err
	}
	for i := 0; i < 5; i++ { // xdep/frozen/on/frozen_new/locked
		if err := expectB(); err != nil {
			return 0, 0, false, 0, err
		}
	}
	if err := skipBits(2 * int(layout>>2&1)); err != nil {
		return 0, 0, false, 0, err
	}
	if err := expectBS(); err != nil { // values
		return 0, 0, false, 0, err
	}
	idx, err := r.ReadBS()
	if err != nil {
		return 0, 0, false, 0, err
	}
	rgb, err := r.ReadBL()
	if err != nil {
		return 0, 0, false, 0, err
	}
	cb, err := r.ReadRC()
	if err != nil {
		return 0, 0, false, 0, err
	}
	if cb&0x01 != 0 {
		if _, err := r.ReadTV(256); err != nil { // name
			return 0, 0, false, 0, err
		}
	}
	if cb&0x02 != 0 {
		if _, err := r.ReadTV(256); err != nil { // book name
			return 0, 0, false, 0, err
		}
	}
	tc, hasTrue := extractTrueColor(rgb)
	return idx, tc, hasTrue, cb, nil
}

// layerColorPlausibility 颜色候选合理性评分（越低越可信）：
// 索引 ≤257 零罚、≤4096 轻罚、越界重罚；颜色标志字节异常罚；
// 带标记但真彩值非法罚。
func layerColorPlausibility(idx uint16, tc uint32, hasT bool, cb uint8) uint64 {
	score := uint64(0)
	switch {
	case idx <= 257:
	case idx <= 4096:
		score += 1000
	default:
		score += 100000
	}
	if cb > 3 {
		score += 10000
	}
	if hasT && (tc == 0 || tc > 0x00FFFFFF) {
		score += 10000
	}
	return score
}
