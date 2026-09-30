// bitbuilders.go 实体/对象位流构造的公共片段助手（记录前缀与 R2013
// 公共实体头），供各子包测试统一使用；仅依赖本包 BitWriter。
package testsupport

// WriteEntityPrefix 写记录前缀：UMC handle-stream-size（1 字节）+ OT 类型码。
func WriteEntityPrefix(w *BitWriter, typeCode uint16) *BitWriter {
	return w.UMC(8).OT(typeCode)
}

// WriteCommonHead 构造 R2013 布局（idxFlags-noShadow-noLW）公共实体头：
// H handle + EED(0) + pic(0) + entmode + reactors + xdic + ds + nolinks +
// color unknown 位 + ltscale + ltype + plot + mat + visual×3 + invis。
// 与读取侧特征位（featMaterialFlags|featVisualStyles|featDSBinary，
// noShadow-noLW 变体）严格对称。
// nolinks=1（ByLayer 颜色）路径：color 为 1 位 unknown。
func WriteCommonHead(w *BitWriter, handle uint64, entmode uint8) *BitWriter {
	w.H(0, handle)
	w.BS(0)          // EED 结束
	w.B(0)           // pic 无
	w.BB(entmode)    // entmode
	w.BL(0)          // reactors
	w.B(0)           // xdic 存在
	w.B(0)           // ds binary
	w.B(1)           // nolinks=1 → ByLayer
	w.B(0)           // color unknown 位
	w.BD(1.0)        // ltscale
	w.BB(0)          // ltype flags
	w.BB(0)          // plotstyle flags
	w.BB(0)          // material flags
	w.B(0).B(0).B(0) // visual styles
	w.BS(0)          // invisibility
	// 无 lineweight：与 hasLineWt=false 布局对称
	return w
}
