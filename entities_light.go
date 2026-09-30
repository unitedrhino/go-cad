// entities_light.go LIGHT 光源实体解码（dwg2.spec DWG_ENTITY(LIGHT)，
// AcDbLight：SpotLight/PointLight/DistantLight）。基线段 19 字段 + 光度
// 分支（NOD 字典 LIGHTINGUNITS=2 触发的 has_photometric_data 子段，
// IES 光域网模型 22 字段）。
package cad

import (
	"github.com/unitedrhino/go-cad/internal/bitstream"
)

// entLight 光源实体：基线字段 + light_color CMC 的版本双形态。
// hasCMCTrue 标记 R2004+ 结构（BS index + BL rgb + RC flag），其余
// 版本 light_color 仅 BS 索引；rgb 原值保留供审计导出（gold 的
// light_color.rgb 为完整 32 位 rgb 的 %06x 形态，含 method 高字节）。
type entLight struct {
	baseEntity
	classVersion         uint32 // BL 90：类版本（VALUEOUTOFBOUNDS 10）
	name                 string // TV/TU 1
	lightType            uint32 // BL 70：0 点光 1 远光 3 聚光；gold 的 type 键即该值（覆盖顶层类型码）
	status               bool   // B 290
	lightColorIndex      uint16 // CMC.BS 63：ACI 索引（R2004+ 经调色板反查修正）
	lightColorRGB        uint32 // CMC.BL：R2004+ 真彩色原始 32 位（0xMRRGGBB）
	lightColorFlag       uint8  // CMC.RC：R2004+ 标志位（&1 name &2 book_name）
	hasLightColorTrue    bool   // R2004+ CMC 结构标记（决定审计导出形态）
	plotGlyph            bool   // B 291
	intensity            float64
	position             point3 // 3BD 10（数组键，审计不比对）
	target               point3 // 3BD 11（数组键，审计不比对）
	attenuationType      uint32 // BL 72
	useAttenuationLimits bool   // B 292
	attenuationStart     float64
	attenuationEnd       float64
	hotspotAngle         float64
	falloffAngle         float64
	castShadows          bool   // B 293
	shadowType           uint32 // BL 73
	shadowMapSize        uint16 // BS 91
	shadowMapSoftness    int8   // RCd 280：有符号字节

	// ---- 光度分支（is_photometric：NOD 字典 LIGHTINGUNITS=="2"）----
	isPhotometric     bool      // 解码上下文判定结果（不占位流）
	hasPhotometricImg bool      // B 295：has_photometric_data（IES 数据存在）
	hasWebfile        bool      // B 290：光域网文件标志
	webfile           string    // T 300：光域网文件名（R2007+ 走字符串流）
	physIntensityMthd uint16    // BS 70：物理强度方法
	physIntensity     float64   // BD 40：物理强度
	illuminanceDist   float64   // BD 41：照度距离
	lampColorType     uint16    // BS 71：0 色温 1 预设
	lampColorTemp     float64   // BD 42：色温（K）
	lampColorPreset   uint16    // BS 72：预设编号
	webRotation       point3    // 3BD_1 43：光域网旋转
	extlightShape     uint16    // BS 73：扩展灯形状
	extlightLength    float64   // BD 46
	extlightWidth     float64   // BD 47
	extlightRadius    float64   // BD 48
	webfileType       uint16    // BS 74：光域网类型
	webSymetry        uint16    // BS 75：对称性（spec 拼写 web_symetry）
	hasTargetGrip     uint16    // BS 76：目标夹具标志
	webFlux           float64   // BD 49：光通量
	webAngles         []float64 // BD 50~54：web_angle1~5
	glyphDisplayType  uint16    // BS 77：图示显示类型
}

// decodeLight LIGHT 主体：公共头之后按 spec 基线顺序逐字段读取；
// isPhotometric（NOD 字典 LIGHTINGUNITS=="2"，由 Document 预扫描探测）为真时
// 继续读 IES 光度子段（has_photometric_data 位展开的 22 字段）；
// COMMON_ENTITY_HANDLE_DATA 尾部 handle 流由 decodeOwnerLayer 处理。
func decodeLight(r *bitstream.BitStream, head *commonEntityHead, ver dwgVersion, codepage uint16, isPhotometric bool) (any, error) {
	l := &entLight{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}}
	var err error
	if l.classVersion, err = r.ReadBL(); err != nil {
		return nil, err
	}
	// VALUEOUTOFBOUNDS(class_version, 10)：越界视为布局错位（扫描框架按候选淘汰）
	if l.classVersion > 10 {
		return nil, bitstream.ErrUnexpectedEOF
	}
	if ver >= verR2007 {
		// R2007+ 的 name 存于记录尾字符串区（obj_string_stream 机制），
		// dat 流不占位；字符串区不可读时回退 dat 流内联读取
		if strs := readStringAreaStrings(r, head, 1); len(strs) > 0 {
			l.name = strs[0]
		} else if l.name, err = r.ReadTU(); err != nil {
			return nil, err
		}
	} else {
		if l.name, err = r.ReadTV(codepage); err != nil {
			return nil, err
		}
	}
	if l.lightType, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if sb, e := r.ReadB(); e != nil {
		return nil, e
	} else {
		l.status = sb == 1
	}
	// FIELD_CMC light_color：R2004+ 为 BS+BL+RC 结构（含调色板反查修正），
	// 更早版本仅 BS 索引（对齐 LibreDWG bit_read_CMC）
	if ver >= verR2004 {
		l.hasLightColorTrue = true
		idx, e := r.ReadBS()
		if e != nil {
			return nil, e
		}
		l.lightColorIndex = idx
		rgb, e := r.ReadBL()
		if e != nil {
			return nil, e
		}
		l.lightColorRGB = rgb
		fb, e := r.ReadRC()
		if e != nil {
			return nil, e
		}
		l.lightColorFlag = fb
		// flag>=4 非法（LibreDWG LOG_ERROR 后清零，不读 name/book_name）；
		// flag&1/&2 的 name/book_name 在 R2007+ 存于字符串流（dat 不占位，
		// 需在主体全部读完后按读取序回填），样本未覆盖该分支，暂不读取
		if l.lightColorFlag >= 4 {
			l.lightColorFlag = 0
		}
		if method := rgb >> 24; method < 0xc0 || method > 0xc8 {
			l.lightColorRGB = 0xc2000000 | (rgb & 0xffffff)
		}
		l.lightColorIndex = uint16(dwgFindColorIndex(l.lightColorRGB))
	} else {
		idx, e := r.ReadBS()
		if e != nil {
			return nil, e
		}
		l.lightColorIndex = idx
	}
	if pb, e := r.ReadB(); e != nil {
		return nil, e
	} else {
		l.plotGlyph = pb == 1
	}
	if l.intensity, err = r.ReadBD(); err != nil {
		return nil, err
	}
	x, y, z, e := r.Read3BD()
	if e != nil {
		return nil, e
	}
	l.position = point3{x, y, z}
	if x, y, z, e = r.Read3BD(); e != nil {
		return nil, e
	}
	l.target = point3{x, y, z}
	if l.attenuationType, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if ub, e := r.ReadB(); e != nil {
		return nil, e
	} else {
		l.useAttenuationLimits = ub == 1
	}
	if l.attenuationStart, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if l.attenuationEnd, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if l.hotspotAngle, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if l.falloffAngle, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if cb, e := r.ReadB(); e != nil {
		return nil, e
	} else {
		l.castShadows = cb == 1
	}
	if l.shadowType, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if l.shadowMapSize, err = r.ReadBS(); err != nil {
		return nil, err
	}
	sm, e := r.ReadRC()
	if e != nil {
		return nil, e
	}
	l.shadowMapSoftness = int8(sm)
	// 光度分支：is_photometric 判定来自 NOD 字典 LIGHTINGUNITS=="2"；
	// 子段存在性由位流 has_photometric_data 位决定
	l.isPhotometric = isPhotometric
	if isPhotometric {
		if hb, e := r.ReadB(); e != nil {
			return nil, e
		} else {
			l.hasPhotometricImg = hb == 1
		}
		if l.hasPhotometricImg {
			if hb, e := r.ReadB(); e != nil {
				return nil, e
			} else {
				l.hasWebfile = hb == 1
			}
			// webfile T：R2007+ 走记录尾字符串区（同 name），回退 dat 内联
			if ver >= verR2007 {
				if strs := readStringAreaStrings(r, head, 1); len(strs) > 0 {
					l.webfile = strs[0]
				} else if l.webfile, e = r.ReadTU(); e != nil {
					return nil, e
				}
			} else if l.webfile, e = r.ReadTV(codepage); e != nil {
				return nil, e
			}
			if l.physIntensityMthd, err = r.ReadBS(); err != nil {
				return nil, err
			}
			if l.physIntensity, err = r.ReadBD(); err != nil {
				return nil, err
			}
			if l.illuminanceDist, err = r.ReadBD(); err != nil {
				return nil, err
			}
			if l.lampColorType, err = r.ReadBS(); err != nil {
				return nil, err
			}
			if l.lampColorTemp, err = r.ReadBD(); err != nil {
				return nil, err
			}
			if l.lampColorPreset, err = r.ReadBS(); err != nil {
				return nil, err
			}
			wx, wy, wz, e := r.Read3BD()
			if e != nil {
				return nil, e
			}
			l.webRotation = point3{wx, wy, wz}
			if l.extlightShape, err = r.ReadBS(); err != nil {
				return nil, err
			}
			if l.extlightLength, err = r.ReadBD(); err != nil {
				return nil, err
			}
			if l.extlightWidth, err = r.ReadBD(); err != nil {
				return nil, err
			}
			if l.extlightRadius, err = r.ReadBD(); err != nil {
				return nil, err
			}
			if l.webfileType, err = r.ReadBS(); err != nil {
				return nil, err
			}
			if l.webSymetry, err = r.ReadBS(); err != nil {
				return nil, err
			}
			if l.hasTargetGrip, err = r.ReadBS(); err != nil {
				return nil, err
			}
			if l.webFlux, err = r.ReadBD(); err != nil {
				return nil, err
			}
			for i := 0; i < 5; i++ {
				var a float64
				if a, err = r.ReadBD(); err != nil {
					return nil, err
				}
				l.webAngles = append(l.webAngles, a)
			}
			if l.glyphDisplayType, err = r.ReadBS(); err != nil {
				return nil, err
			}
		}
	}
	owner, layer := decodeOwnerLayer(r, head)
	l.owner, l.layer = owner, layer
	return l, nil
}
