// entities_proxy_test.go PROXY_ENTITY 合成位流测试：R2013/R2018 版本分支、
// preview 复用（proxy_data_size=preview_size）、hdlpos 定界的原始数据位
// 捕获与 handle 流剩余句柄收集。位长按 MSB 序手工推算并断言。
package cad

import (
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"testing"
)

// TestDecodeProxyEntityFromBits R2013 口径：proxy_id BL + version BLx（拆分
// maint/dwg）+ from_dxf B + 16 位填充（原始数据捕获区）+ handle 流
// （xdic+layer 消耗 2 句柄，剩余 2 句柄记为 objids）。
func TestDecodeProxyEntityFromBits(t *testing.T) {
	w := writeEntityPrefix(testsupport.NewBitWriter(), 0x1F2) // 18 位
	writeCommonHead(w, 500, 2)                                // 48 位 → 累计 66
	w.BL(499)                                                 // proxy_id：34 位 → 100
	w.BL(0x0201)                                              // version：34 位 → 134（maint=2 dwg=1）
	w.B(0)                                                    // from_dxf：1 位 → 135
	w.RCS([]byte{0xAB, 0xCD})                                 // 原始数据填充：16 位 → 151
	w.H(5, 20)                                                // xdic
	w.H(5, 21)                                                // layer
	w.H(5, 22)                                                // objids[0]
	w.H(5, 23)                                                // objids[1] → dataEnd=215
	const objSizeBit, dataEnd = uint64(151), uint64(215)
	r := bitstream.NewBitStream(w.Bytes())
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	head, err := parseEntityHead(r, 0, featMaterialFlags|featVisualStyles|featDSBinary)
	if err != nil {
		t.Fatal(err)
	}
	head.objSizeBit = objSizeBit
	ent, err := decodeProxyEntityVer(r, &head, dataEnd, verR2013)
	if err != nil {
		t.Fatal(err)
	}
	px := ent.(*entProxyEntity)
	if px.proxyID != 499 || px.maintVersion != 2 || px.dwgVersionNum != 1 || px.fromDxf {
		t.Fatalf("PROXY 元数据: id=%d maint=%d dwg=%d fromDxf=%v",
			px.proxyID, px.maintVersion, px.dwgVersionNum, px.fromDxf)
	}
	if px.version != 0x0201 {
		t.Fatalf("PROXY version: %X", px.version)
	}
	if px.dataNumBits != 16 || len(px.data) != 2 || px.data[0] != 0xAB || px.data[1] != 0xCD {
		t.Fatalf("PROXY 原始数据捕获: bits=%d data=%v", px.dataNumBits, px.data)
	}
	if px.numObjids != 2 || len(px.objids) != 2 || px.objids[0] != 22 || px.objids[1] != 23 {
		t.Fatalf("PROXY objids: n=%d v=%v", px.numObjids, px.objids)
	}
	if px.layer != 21 {
		t.Fatalf("PROXY layer: %d", px.layer)
	}
}

// TestDecodeProxyEntityR2018FromBits R2018 口径：dwg_version 与 maint_version
// 为独立 BL 字段（不再合并为 version）。
func TestDecodeProxyEntityR2018FromBits(t *testing.T) {
	w := writeEntityPrefix(testsupport.NewBitWriter(), 0x1F2) // 18 位
	writeCommonHead(w, 501, 2)                                // → 66
	w.BL(499)                                                 // → 100
	w.BL(30)                                                  // dwg_version：10 位 → 110
	w.BL(7)                                                   // maint_version：10 位 → 120
	w.B(1)                                                    // from_dxf → 121
	w.RCS([]byte{0x11})                                       // 填充 8 位 → 129
	w.H(5, 20)                                                // xdic
	w.H(5, 21)                                                // layer → dataEnd=161
	const objSizeBit, dataEnd = uint64(129), uint64(161)
	r := bitstream.NewBitStream(w.Bytes())
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	head, err := parseEntityHead(r, 0, featMaterialFlags|featVisualStyles|featDSBinary)
	if err != nil {
		t.Fatal(err)
	}
	head.objSizeBit = objSizeBit
	ent, err := decodeProxyEntityVer(r, &head, dataEnd, verR2018)
	if err != nil {
		t.Fatal(err)
	}
	px := ent.(*entProxyEntity)
	if px.dwgVersionNum != 30 || px.maintVersion != 7 || px.version != 0 || !px.fromDxf {
		t.Fatalf("PROXY R2018: dwg=%d maint=%d version=%X fromDxf=%v",
			px.dwgVersionNum, px.maintVersion, px.version, px.fromDxf)
	}
	if px.dataNumBits != 8 || px.data[0] != 0x11 {
		t.Fatalf("PROXY R2018 data: bits=%d v=%v", px.dataNumBits, px.data)
	}
}

// TestDecodeProxyEntityPreviewFromBits 公共头含 preview（pic=1）时：
// proxy_data_size 取 preview_size，proxy_data TF 从主体读取同长字节。
// 头部手工构造（pic=1 布局），位长手工推算。
func TestDecodeProxyEntityPreviewFromBits(t *testing.T) {
	w := writeEntityPrefix(testsupport.NewBitWriter(), 0x1F2) // 18
	w.H(0, 600)                                               // 24 → 42
	w.BS(0)                                                   // EED → 44
	w.B(1)                                                    // pic=1 → 45
	w.BLL(4)                                                  // preview_size（BLL，pictureRL=false）→ 49
	w.RCS([]byte{0xDE, 0xAD, 0xBE, 0xEF})                     // preview → 81
	w.BB(2)                                                   // entmode=2 → 83
	w.BL(0)                                                   // reactors → 85
	w.B(0)                                                    // xdic → 86
	w.B(0)                                                    // ds → 87
	w.B(1)                                                    // nolinks → 88
	w.B(0)                                                    // color unknown → 89
	w.BD(1.0)                                                 // ltscale → 91
	w.BB(0)                                                   // ltype → 93
	w.BB(0)                                                   // plot → 95
	w.BB(0)                                                   // mat → 97
	w.B(0).B(0).B(0)                                          // visuals → 100
	w.BS(0)                                                   // invis → 102
	w.BL(499)                                                 // proxy_id → 136
	w.BL(0x0301)                                              // version → 170
	w.B(0)                                                    // from_dxf → 171
	w.RCS([]byte{0x01, 0x02, 0x03, 0x04})                     // proxy_data TF（4 字节）→ 203
	w.H(5, 20)                                                // xdic
	w.H(5, 21)                                                // layer → dataEnd=235
	const objSizeBit, dataEnd = uint64(203), uint64(235)
	r := bitstream.NewBitStream(w.Bytes())
	_, _ = r.ReadUMC()
	_, _ = r.ReadOT()
	head, err := parseEntityHead(r, 0, featMaterialFlags|featVisualStyles|featDSBinary)
	if err != nil {
		t.Fatal(err)
	}
	if !head.previewExists || len(head.preview) != 4 {
		t.Fatalf("公共头 preview: exists=%v len=%d", head.previewExists, len(head.preview))
	}
	head.objSizeBit = objSizeBit
	ent, err := decodeProxyEntityVer(r, &head, dataEnd, verR2013)
	if err != nil {
		t.Fatal(err)
	}
	px := ent.(*entProxyEntity)
	if px.proxyDataSize != 4 || string(px.proxyData) != string([]byte{0x01, 0x02, 0x03, 0x04}) {
		t.Fatalf("PROXY proxy_data: size=%d v=%v", px.proxyDataSize, px.proxyData)
	}
	if px.dataNumBits != 0 {
		t.Fatalf("PROXY 无填充时 data 应为空: %d", px.dataNumBits)
	}
}
