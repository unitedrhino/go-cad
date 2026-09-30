// entities_light.go LIGHT 光源实体解码（dwg2.spec DWG_ENTITY(LIGHT)，
// AcDbLight：SpotLight/PointLight/DistantLight）。基线段 19 字段 + 光度
// 分支（NOD 字典 LIGHTINGUNITS=2 触发的 has_photometric_data 子段，
// IES 光域网模型 22 字段）。
package entity

import (
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
)

// entLight 光源实体：基线字段 + light_color CMC 的版本双形态。
// hasCMCTrue 标记 R2004+ 结构（BS index + BL rgb + RC flag），其余
// 版本 light_color 仅 BS 索引；rgb 原值保留供审计导出（gold 的
// light_color.rgb 为完整 32 位 rgb 的 %06x 形态，含 method 高字节）。
type EntLight struct {
	BaseEntity
	ClassVersion         uint32 // BL 90：类版本（VALUEOUTOFBOUNDS 10）
	Name                 string // TV/TU 1
	LightType            uint32 // BL 70：0 点光 1 远光 3 聚光；gold 的 type 键即该值（覆盖顶层类型码）
	Status               bool   // B 290
	LightColorIndex      uint16 // CMC.BS 63：ACI 索引（R2004+ 经调色板反查修正）
	LightColorRGB        uint32 // CMC.BL：R2004+ 真彩色原始 32 位（0xMRRGGBB）
	LightColorFlag       uint8  // CMC.RC：R2004+ 标志位（&1 name &2 book_name）
	HasLightColorTrue    bool   // R2004+ CMC 结构标记（决定审计导出形态）
	PlotGlyph            bool   // B 291
	Intensity            float64
	Position             Point3 // 3BD 10（数组键，审计不比对）
	Target               Point3 // 3BD 11（数组键，审计不比对）
	AttenuationType      uint32 // BL 72
	UseAttenuationLimits bool   // B 292
	AttenuationStart     float64
	AttenuationEnd       float64
	HotspotAngle         float64
	FalloffAngle         float64
	CastShadows          bool   // B 293
	ShadowType           uint32 // BL 73
	ShadowMapSize        uint16 // BS 91
	ShadowMapSoftness    int8   // RCd 280：有符号字节

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
	webRotation       Point3    // 3BD_1 43：光域网旋转
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
func decodeLight(r *bitstream.BitStream, Head *CommonEntityHead, ver container.DwgVersion, codepage uint16, isPhotometric bool) (any, error) {
	l := &EntLight{BaseEntity: BaseEntity{Handle: Head.Handle, Color: Head.Color, Mode: Head.EntityMode}}
	var err error
	if l.ClassVersion, err = r.ReadBL(); err != nil {
		return nil, err
	}
	// VALUEOUTOFBOUNDS(class_version, 10)：越界视为布局错位（扫描框架按候选淘汰）
	if l.ClassVersion > 10 {
		return nil, bitstream.ErrUnexpectedEOF
	}
	if ver >= container.VerR2007 {
		// R2007+ 的 name 存于记录尾字符串区（obj_string_stream 机制），
		// dat 流不占位；字符串区不可读时回退 dat 流内联读取
		if strs := readStringAreaStrings(r, Head, 1); len(strs) > 0 {
			l.Name = strs[0]
		} else if l.Name, err = r.ReadTU(); err != nil {
			return nil, err
		}
	} else {
		if l.Name, err = r.ReadTV(codepage); err != nil {
			return nil, err
		}
	}
	if l.LightType, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if sb, e := r.ReadB(); e != nil {
		return nil, e
	} else {
		l.Status = sb == 1
	}
	// FIELD_CMC light_color：R2004+ 为 BS+BL+RC 结构（含调色板反查修正），
	// 更早版本仅 BS 索引（对齐 LibreDWG bit_read_CMC）
	if ver >= container.VerR2004 {
		l.HasLightColorTrue = true
		idx, e := r.ReadBS()
		if e != nil {
			return nil, e
		}
		l.LightColorIndex = idx
		Rgb, e := r.ReadBL()
		if e != nil {
			return nil, e
		}
		l.LightColorRGB = Rgb
		fb, e := r.ReadRC()
		if e != nil {
			return nil, e
		}
		l.LightColorFlag = fb
		// flag>=4 非法（LibreDWG LOG_ERROR 后清零，不读 name/book_name）；
		// flag&1/&2 的 name/book_name 在 R2007+ 存于字符串流（dat 不占位，
		// 需在主体全部读完后按读取序回填），样本未覆盖该分支，暂不读取
		if l.LightColorFlag >= 4 {
			l.LightColorFlag = 0
		}
		if method := Rgb >> 24; method < 0xc0 || method > 0xc8 {
			l.LightColorRGB = 0xc2000000 | (Rgb & 0xffffff)
		}
		l.LightColorIndex = uint16(DwgFindColorIndex(l.LightColorRGB))
	} else {
		idx, e := r.ReadBS()
		if e != nil {
			return nil, e
		}
		l.LightColorIndex = idx
	}
	if pb, e := r.ReadB(); e != nil {
		return nil, e
	} else {
		l.PlotGlyph = pb == 1
	}
	if l.Intensity, err = r.ReadBD(); err != nil {
		return nil, err
	}
	X, Y, Z, e := r.Read3BD()
	if e != nil {
		return nil, e
	}
	l.Position = Point3{X, Y, Z}
	if X, Y, Z, e = r.Read3BD(); e != nil {
		return nil, e
	}
	l.Target = Point3{X, Y, Z}
	if l.AttenuationType, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if ub, e := r.ReadB(); e != nil {
		return nil, e
	} else {
		l.UseAttenuationLimits = ub == 1
	}
	if l.AttenuationStart, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if l.AttenuationEnd, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if l.HotspotAngle, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if l.FalloffAngle, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if cb, e := r.ReadB(); e != nil {
		return nil, e
	} else {
		l.CastShadows = cb == 1
	}
	if l.ShadowType, err = r.ReadBL(); err != nil {
		return nil, err
	}
	if l.ShadowMapSize, err = r.ReadBS(); err != nil {
		return nil, err
	}
	sm, e := r.ReadRC()
	if e != nil {
		return nil, e
	}
	l.ShadowMapSoftness = int8(sm)
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
			if ver >= container.VerR2007 {
				if strs := readStringAreaStrings(r, Head, 1); len(strs) > 0 {
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
			l.webRotation = Point3{wx, wy, wz}
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
	Owner, Layer := decodeOwnerLayer(r, Head)
	l.Owner, l.Layer = Owner, Layer
	return l, nil
}
