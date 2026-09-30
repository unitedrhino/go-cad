// entities_mpolygon.go 实现 MPOLYGON（AcDbMPolygon，多边形填充，动态类）：
// 主体复用 HATCH 的渐变填充段与边界路径解析（decodeHatchGradient/
// decodeHatchPaths），外围为 style/图案段/hatch_color/x_dir 等 MPOLYGON
// 专属字段。位级布局对照 LibreDWG dwg.spec DWG_ENTITY (MPOLYGON)
// （DEBUG_CLASSES 分支；上游正常解码器不编译该类，语料中亦无实例，
// 位流经合成流测试验证）。
package cad

import (
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
)

// entMpolygon 多边形填充实体。
type entMpolygon struct {
	baseEntity
	style     uint16    // style（BS 75，主体首）：0=normal 1=outer 2=whole
	hatch     *entHatch // HATCH 同构主体（渐变/高程/挤出/名称/填充标志/路径/图案段）
	styleTail uint16    // 路径数组后的重复 style 字段（spec 原文双 FIELD_BS(style,75)）
	xDir      point2    // x_dir（2RD 11）
}

// decodeMpolygonVer MPOLYGON：style BS + [渐变段（R2004+，复用 HATCH
// gradientfill）] + elevation/extrusion/name/is_solid_fill/is_associative +
// 边界路径数组（复用 HATCH 路径解析）+ style/pattern_type + [非 solid 时
// angle/scale_spacing/double_flag/定义线段] + hatch_color CMC（R2004+ 格式，
// 读取后丢弃）+ x_dir 2RD + 总边界句柄数 BL。与 HATCH 的差异：无
// pixel_size/种子点段，且 style 在主体首与路径后各出现一次（spec 字面）。
// MPOLYGON 随 AutoCAD 2004 引入，颜色字段按 R2004+ CMC 布局解析。
func decodeMpolygonVer(r *bitstream.BitStream, head *commonEntityHead, ver container.DwgVersion, codepage uint16) (any, error) {
	m := &entMpolygon{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}}
	h := &entHatch{baseEntity: baseEntity{handle: head.handle, color: head.color, mode: head.entityMode}}
	m.hatch = h
	streamName := ver >= container.VerR2007 // R2007+ 图案名/渐变名存于对象字符串区（同 HATCH 口径）
	var err error
	if m.style, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if ver >= container.VerR2004 {
		if err = decodeHatchGradient(r, h, streamName, ver == container.VerR2007); err != nil {
			return nil, err
		}
	}
	if h.elevation, err = r.ReadBD(); err != nil {
		return nil, err
	}
	if h.extrusion, err = read3pt(r); err != nil {
		return nil, err
	}
	if !streamName {
		if h.name, err = readHatchString(r, hatchStrInlineTv, codepage); err != nil {
			return nil, err
		}
	}
	var v uint8
	if v, err = r.ReadB(); err != nil {
		return nil, err
	}
	h.solidFill = v != 0
	if v, err = r.ReadB(); err != nil {
		return nil, err
	}
	h.associative = v != 0
	if h.paths, h.hasDerived, err = decodeHatchPaths(r, ver >= container.VerR2010); err != nil {
		return nil, err
	}
	// 路径后的重复 style（spec 原文双 FIELD_BS(style,75)，字面保留）
	if m.styleTail, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if h.patternType, err = r.ReadBS(); err != nil {
		return nil, err
	}
	if !h.solidFill {
		if h.angle, err = r.ReadBD(); err != nil {
			return nil, err
		}
		if h.scaleSpacing, err = r.ReadBD(); err != nil {
			return nil, err
		}
		if v, err = r.ReadB(); err != nil {
			return nil, err
		}
		h.doubleFlag = v != 0
		numDefLines, err := r.ReadBS()
		if err != nil {
			return nil, err
		}
		if uint32(numDefLines) > 100_000 {
			return nil, fmt.Errorf("cad: MPOLYGON 定义线数异常 %d", numDefLines)
		}
		for i := uint32(0); i < uint32(numDefLines); i++ {
			var dl hatchDefLine
			if dl.angle, err = r.ReadBD(); err != nil {
				return nil, err
			}
			if dl.pt0.x, err = r.ReadBD(); err != nil {
				return nil, err
			}
			if dl.pt0.y, err = r.ReadBD(); err != nil {
				return nil, err
			}
			if dl.offset.x, err = r.ReadBD(); err != nil {
				return nil, err
			}
			if dl.offset.y, err = r.ReadBD(); err != nil {
				return nil, err
			}
			numDashes, err := r.ReadBS()
			if err != nil {
				return nil, err
			}
			if uint32(numDashes) > 100_000 {
				return nil, fmt.Errorf("cad: MPOLYGON 划线数异常 %d", numDashes)
			}
			for j := uint32(0); j < uint32(numDashes); j++ {
				d, e := r.ReadBD()
				if e != nil {
					return nil, e
				}
				dl.dashes = append(dl.dashes, d)
			}
			h.deflines = append(h.deflines, dl)
		}
	}
	// hatch_color CMC（R2004+ 布局）：当前审计口径不保留颜色值，读取占位推进位流
	if ver != container.VerR13 && ver != container.VerR14 {
		if err = skipColorCMCR2004(r); err != nil {
			return nil, err
		}
	}
	if m.xDir.x, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if m.xDir.y, err = r.ReadRD(); err != nil {
		return nil, err
	}
	if _, err = r.ReadBL(); err != nil { // 总边界对象句柄数（本体在 handle 流）
		return nil, err
	}
	r.SetBitPos(head.objSizeBit)
	if _, layer, e := parseCommonEntityHandles(r, head); e == nil {
		m.layer = layer
	}
	return m, nil
}
