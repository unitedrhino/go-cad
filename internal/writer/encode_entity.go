// 本文件实现实体的 round-trip 位级回放（dwgwrite 编码方向的实体侧）：
// 解码时由 scanEntityBest 的 fillMeta 挂载点调用 collectEntityRawBits，
// 从最终命中候选收集原始位串（preBits/headRawBits/RawHandleBits，字段
// 挂在 baseEntity 上，生产解码路径默认填充，供写出回放与测试共用）；
// 编码侧 encodeEntityR200x 将三段
// 位串原样回放为完整记录 body。与内部对象的 encodeInternalObjectR2000
// 不同，实体回放不回填流内 RL objSize：preBits 从 body 局部 0 起收集，
// 重编码 body 的坐标系与源记录逐位一致，重解码可直接走 parseObjHeader
// 标准入口（R2000 系含 BS 类型码前导，R2010+ 含 UMC+OT 前导）。
package writer

import (
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/entity"
)

// encodeEntityR200x 将实体重编码为完整记录 body 位流（R13-R2018 家族
// 通用）：preBits + headRawBits + RawHandleBits 三段原样回放，返回 body
// 字节与 handle 流起点位（datEnd，body 局部坐标，与源记录一致）。位串
// 未收集（生产路径结构异常或解码失败兜底产物）时报错，由调用方按
// 能力边界处理。
func EncodeEntityR200x(ent any, ver container.DwgVersion) ([]byte, uint64, error) {
	_ = ver // 回放方案与版本无关：位串原样保留全部版本差异
	b := entity.EntityBase(ent)
	if b == nil || b.HeadRawBits == "" || b.RawHandleBits == "" {
		return nil, 0, fmt.Errorf("cad: 实体 round-trip 缺少位串收集")
	}
	w := bitstream.NewEncWriter()
	w.WriteBitsString(b.PreBits)
	w.WriteBitsString(b.HeadRawBits)
	datEnd := w.TellBits()
	w.WriteBitsString(b.RawHandleBits)
	return w.Bytes(), datEnd, nil
}
