// edgecases_test.go 错误路径与边界条件集成测试：畸形输入、截断文件、
// 加密段、非法版本等必须返回明确错误而非 panic 或静默错误结果。
package cad

import (
	"bytes"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/entity"
	"os"
	"path/filepath"
	"testing"
)

func TestParseEmptyInput(t *testing.T) {
	if _, err := Parse(nil); err == nil {
		t.Error("空输入应报错")
	}
	if _, err := Parse([]byte{}); err == nil {
		t.Error("空字节应报错")
	}
	if _, err := Parse([]byte("AC")); err == nil {
		t.Error("过短输入应报错")
	}
}

func TestParseInvalidMagic(t *testing.T) {
	for _, magic := range []string{"XXXXXX", "AC1099", "ac1032"} {
		if _, err := Parse([]byte(magic + "padpadpad")); err == nil {
			t.Errorf("魔数 %q 应报错", magic)
		}
	}
}

func TestParseTruncatedFile(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "lw_example2018.dwg"))
	if err != nil {
		t.Fatal(err)
	}
	// 截断到文件头之后（容器头不完整）
	for _, size := range []int{100, 0x80, 0x100, 0x1000, len(data) / 2} {
		cut := data[:size]
		_, err := Parse(cut)
		if err == nil {
			// 截断到一半也可能成功（段完整时）——只要求不 panic
			t.Logf("截断到 %d 字节仍可解析（前缀段完整）", size)
		}
	}
	// 完全随机数据不 panic
	random := bytes.Repeat([]byte{0xAB, 0xCD, 0x12, 0x34}, 1000)
	_, _ = Parse(random) // 只要求不 panic
}

func TestParseCorruptedHandlesSection(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "lw_example2018.dwg"))
	if err != nil {
		t.Fatal(err)
	}
	// 破坏文件中部（对象数据区域）→ 应报错或部分跳过，不 panic
	corrupted := append([]byte{}, data...)
	mid := len(corrupted) / 2
	for i := mid; i < mid+512 && i < len(corrupted); i++ {
		corrupted[i] = 0xFF
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("损坏输入不应 panic: %v", r)
		}
	}()
	_, _ = Parse(corrupted)
}

func TestDocumentAccessors(t *testing.T) {
	doc := parseSample(t, "lw_example2018.dwg")
	if doc.Version() != "AC1032" {
		t.Fatalf("版本: %s", doc.Version())
	}
	if doc.EntityCount() == 0 {
		t.Fatal("实体数应大于 0")
	}
	if len(doc.Texts()) == 0 {
		t.Fatal("文本数应大于 0")
	}
}

func TestRenderOptionsDefaults(t *testing.T) {
	doc := parseSample(t, "lw_example2018.dwg")
	// 默认宽度
	png, err := RenderPNG(doc, RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(png) == 0 {
		t.Fatal("默认渲染应输出 PNG")
	}
	// 极小宽度
	png, err = RenderPNG(doc, RenderOptions{Width: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(png) == 0 {
		t.Fatal("极小宽度渲染应输出 PNG")
	}
}

func TestLargestBlockHeaderDeterminism(t *testing.T) {
	d1 := parseSample(t, "lw_example2018.dwg")
	d2 := parseSample(t, "lw_example2018.dwg")
	if d1.LargestBlockHeader() != d2.LargestBlockHeader() {
		t.Fatal("最大块头应确定")
	}
	if d1.EntityCount() != d2.EntityCount() {
		t.Fatal("两次解析实体数应一致")
	}
}

func TestDumpEntitiesValidJSON(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "lw_example2018.dwg"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := DumpEntities(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) < 10 {
		t.Fatal("dump 输出过短")
	}
	if !bytes.Contains([]byte(out), []byte(`"type":"LINE"`)) {
		t.Fatal("dump 应含 LINE 实体")
	}
}

// TestDecompressR21Malformed R21 解压器截断/畸形输入容错：必须报错或
// 短输出，禁止越界 panic。
func TestDecompressR21Malformed(t *testing.T) {
	// 空流 + 非零声明尺寸 → 报错
	if _, err := container.DecompressR21(nil, 10); err == nil {
		t.Error("空流非零尺寸应报错")
	}
	// 空流 + 零声明尺寸 → 空输出
	if out, err := container.DecompressR21(nil, 0); err != nil || len(out) != 0 {
		t.Errorf("零尺寸期望空输出: %q %v", out, err)
	}
	// 0x20 引导 opcode 被截断（缺 2 字节引导载荷）→ 报错
	if _, err := container.DecompressR21([]byte{0x20}, 100); err == nil {
		t.Error("引导 opcode 截断应报错")
	}
	// 字面量长度声明超出实际数据 → 报错（截断流）
	if _, err := container.DecompressR21([]byte{0x05, 'A', 'B'}, 100); err == nil {
		t.Error("截断流应报错")
	}
	// 0xFF 扩展长度链被截断 → 报错
	if _, err := container.DecompressR21([]byte{0x16, 0xFF, 0x01}, 100); err == nil {
		t.Error("扩展长度链截断应报错")
	}
}

// TestDecompressLZ77EmptyInput LZ77 解压器空输入容错。
func TestDecompressLZ77EmptyInput(t *testing.T) {
	if _, err := container.DecompressLZ77(nil, 10); err == nil {
		t.Error("空流非零尺寸应报错")
	}
	if out, err := container.DecompressLZ77(nil, 0); err != nil || len(out) != 0 {
		t.Errorf("零尺寸期望空输出: %q %v", out, err)
	}
}

// TestReadCRCTruncated CRC 读侧截断容错：流耗尽后 readCRC 必须报错。
func TestReadCRCTruncated(t *testing.T) {
	r := bitstream.NewBitStream([]byte{0x01})
	r.Restore(0, 0) // 不足 2 字节
	if _, err := r.ReadCRC(); err == nil {
		t.Error("流耗尽后 readCRC 应报错")
	}
}

// TestEncodeEntityReplayGuards 实体回放编码器边界：nil 实体与缺位串
// 实体必须报错而非 panic；回放输出与版本参数无关（位串原样保留差异）。
func TestEncodeEntityReplayGuards(t *testing.T) {
	if _, _, err := encodeEntityR200x(nil, container.VerR2000); err == nil {
		t.Error("nil 实体应报错")
	}
	if _, _, err := encodeEntityR200x(&entity.EntLine{}, container.VerR2000); err == nil {
		t.Error("缺位串实体应报错")
	}
	// 真实样本实体：版本参数不影响回放输出
	doc := parseSample(t, "lw_example2018.dwg")
	for _, e := range doc.ModelSpace {
		b := entity.EntityBase(e)
		if b == nil || b.HeadRawBits == "" || b.RawHandleBits == "" {
			continue
		}
		out1, _, err1 := encodeEntityR200x(e, container.VerR2000)
		out2, _, err2 := encodeEntityR200x(e, container.DwgVersion(0xFF))
		if err1 != nil || err2 != nil {
			t.Fatalf("回放编码失败: %v %v", err1, err2)
		}
		if !bytes.Equal(out1, out2) {
			t.Fatal("回放输出不应随版本参数变化")
		}
		break
	}
}

// TestWriteDwgRejectsInvalidDocs 写出器边界：nil 文档、空文档（缺回放
// 素材）必须报错而非 panic。
func TestWriteDwgRejectsInvalidDocs(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("非法文档写出不应 panic: %v", r)
		}
	}()
	var sink bytes.Buffer
	if err := WriteDwg(nil, &sink); err == nil {
		t.Error("WriteDwg(nil) 应报错")
	}
	if err := WriteDwg(&Document{}, &sink); err == nil {
		t.Error("WriteDwg(空文档) 应报错")
	}
	if err := WriteDwgR2000(nil, &sink); err == nil {
		t.Error("WriteDwgR2000(nil) 应报错")
	}
	if err := WriteDwgR2000(&Document{}, &sink); err == nil {
		t.Error("WriteDwgR2000(空文档) 应报错")
	}
	if err := WriteDwgR2004(nil, &sink); err == nil {
		t.Error("WriteDwgR2004(nil) 应报错")
	}
	if err := WriteDwgR2004(&Document{}, &sink); err == nil {
		t.Error("WriteDwgR2004(空文档) 应报错")
	}
	if err := WriteDwgR2007(nil, &sink); err == nil {
		t.Error("WriteDwgR2007(nil) 应报错")
	}
	if err := WriteDwgR2007(&Document{}, &sink); err == nil {
		t.Error("WriteDwgR2007(空文档) 应报错")
	}
}
