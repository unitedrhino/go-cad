// 本文件实现实体的 round-trip 位级回放（dwgwrite 编码方向的实体侧）：
// 解码时由 scanEntityBest 的 fillMeta 挂载点调用 collectEntityRawBits，
// 从最终命中候选收集原始位串（preBits/headRawBits/RawHandleBits，字段
// 挂在 baseEntity 上，生产解码路径默认填充，供写出回放与测试共用）；
// 编码侧 encodeEntityR200x 将三段
// 位串原样回放为完整记录 body。与内部对象的 encodeInternalObjectR2000
// 不同，实体回放不回填流内 RL objSize：preBits 从 body 局部 0 起收集，
// 重编码 body 的坐标系与源记录逐位一致，重解码可直接走 parseObjHeader
// 标准入口（R2000 系含 BS 类型码前导，R2010+ 含 UMC+OT 前导）。
package cad

import "fmt"

// collectEntityRawBits 收集实体 round-trip 回放所需的原始位串（解码侧）：
// preBits = [body 局部 0, startBit)（MS 后填充、类型码与扫描跳过位），
// headRawBits = [startBit, handle 流起点)（公共头与专有字段，含流内
// RL objSize 原值），RawHandleBits = [handle 流起点, 记录尾)（handle 流、
// R2007+ 字符串区与 R2010+ 尾部 UMC）。handle 流起点取公共头解析结果
// head.objSizeBit（objSizeInSub 布局为流内 RL 值，externalSize 布局为
// dataEndBit，均为 body 局部坐标）；结构异常（handle 流起点越出候选起点
// 与数据区末尾之间）时放弃收集，回放端按缺少位串处理。
func collectEntityRawBits(r *bitStream, b *baseEntity, head *commonEntityHead, startBit, dataEnd uint64) {
	if b == nil || head == nil {
		return
	}
	total := uint64(len(r.src)) * 8
	hdStart := head.objSizeBit
	if hdStart < startBit || hdStart > dataEnd || dataEnd > total {
		return
	}
	b.preBits = collectBits(r, 0, startBit)
	b.headRawBits = collectBits(r, startBit, hdStart)
	b.RawHandleBits = collectBits(r, hdStart, total)
}

// attachEntityRecordMeta 回填源记录元数据（解码侧挂载）：重解码端重建
// objectRecord 时需要 MS size 与 R2010+ 的 UMC 元数据（dataEndBit 推导
// 依赖 size/handleSizeFieldBits/handleStreamSizeBits/bodyBitOffset），
// 这些值无法在 scanEntityBest 内部获得，由版本化解码入口补记。
func attachEntityRecordMeta(ent any, rec *objectRecord) {
	if ent == nil || rec == nil {
		return
	}
	b := entBase(ent)
	if b == nil {
		return
	}
	b.r2010Plus = rec.r2010Plus
	b.sizeBytes = rec.size
	b.hSizeField = rec.handleSizeFieldBits
	b.hssBits = rec.handleStreamSizeBits
	b.bodyBitOff = rec.bodyBitOffset
}

// encodeEntityR200x 将实体重编码为完整记录 body 位流（R13-R2018 家族
// 通用）：preBits + headRawBits + RawHandleBits 三段原样回放，返回 body
// 字节与 handle 流起点位（datEnd，body 局部坐标，与源记录一致）。位串
// 未收集（生产路径结构异常或解码失败兜底产物）时报错，由调用方按
// 能力边界处理。
func encodeEntityR200x(ent any, ver dwgVersion) ([]byte, uint64, error) {
	_ = ver // 回放方案与版本无关：位串原样保留全部版本差异
	b := entBase(ent)
	if b == nil || b.headRawBits == "" || b.RawHandleBits == "" {
		return nil, 0, fmt.Errorf("cad: 实体 round-trip 缺少位串收集")
	}
	w := newEncWriter()
	w.writeBitsString(b.preBits)
	w.writeBitsString(b.headRawBits)
	datEnd := w.tellBits()
	w.writeBitsString(b.RawHandleBits)
	return w.bytes(), datEnd, nil
}
