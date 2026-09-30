// entities_gap.go 实体侧 spec 查漏补缺（批次 G）：LARGE_RADIAL_DIMENSION
// 的专属尾部布局常量（VERTEX_PFACE_FACE 已由批次 A 在 entities_more.go
// 实现，此处仅保留去重后的增量）。
package entity

// dimLayoutLargeRadial LARGE_RADIAL_DIMENSION 的类型专属尾部布局。
// 该类（R2000+ 动态类）共用 COMMON_ENTITY_DIMENSION 公共段与
// entDimension 载体，仅专属尾部顺序不同（def_pt/chord_pt/jog_angle/
// ovr_center/jog_pt），读取逻辑见 readDimSpecific 的同名 case。
// LibreDWG DECODER 的 flag 合成不给该类补类型位（switch 无此 case），
// 与 computeDimFlag 对未知布局的行为一致。
const DimLayoutLargeRadial DimSpecificLayout = DimLayoutArc + 1
