// r11_codepage_test pre-R13 码页支持验证：头变量流 UCS 段后的 RS 码页
// 字段读取（真实样本字段定位）与 TEXT 按码页解码（合成位流自证）。
// 语料无中文 pre-R13 样本，GBK 文本路径以构造字节序列验证，
// 真实中文样本出现后需补端到端 gold 对照。
package cad

import (
	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/testsupport"
	"math"
	"path/filepath"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// TestPreR13HeaderCodepageField 真实 R11 样本的头变量码页字段定位验证：
// numheader_vars>129 时 UCS 表头后 2 字节为 RS 码页（该样本族为默认 30 或 0）。
func TestPreR13HeaderCodepageField(t *testing.T) {
	for _, rel := range []string{
		filepath.Join("r9", "entities.dwg"),
		filepath.Join("r10", "entities.dwg"),
		filepath.Join("r11", "entities-2d.dwg"),
	} {
		data := requirePreR13Sample(t, rel)
		version, err := container.DetectVersion(data)
		if err != nil {
			t.Fatalf("%s: detectVersion: %v", rel, err)
		}
		r := &preR13Reader{data: data}
		hdr, err := parsePreR13Header(r, version)
		if err != nil {
			t.Fatalf("%s: parsePreR13Header: %v", rel, err)
		}
		// 样本族原始值 0（缺省）或 30；parsePreR13Document 把 0 归一化为 30
		if hdr.codepage != 0 && hdr.codepage != 30 {
			t.Errorf("%s: hdr.codepage=%d, want 0 或 30", rel, hdr.codepage)
		}
	}
}

// TestPreR13TextCodepageDecode 合成 TEXT 记录位流按码页解码自证：
// GBK（码页 39=WINDOWS-936）编码的中文文本经 decodePreR13Text 解出 UTF-8；默认 30 走
// Latin-1 近似（多字节序列不被合并，行为与此前一致）。
func TestPreR13TextCodepageDecode(t *testing.T) {
	const text = "母排标注TMY-3x(80x10)"
	gbk, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(text))
	if err != nil {
		t.Fatalf("GBK 编码失败: %v", err)
	}
	// 合成 TEXT 专有字段：ins_pt 2RD + height RD + RS 文本长度 + 文本字节，
	// opts=0 无可选段
	w := []byte{}
	appendRD := func(f float64) {
		bits := math.Float64bits(f)
		for i := 0; i < 8; i++ {
			w = append(w, byte(bits>>(8*i)))
		}
	}
	appendRD(10.0)
	appendRD(20.0)
	appendRD(2.5)
	w = append(w, byte(len(gbk)), byte(len(gbk)>>8))
	w = append(w, gbk...)

	head := preR13EntHead{typ: preR13TypeText, opts: 0, elevation: 0}
	r := &preR13Reader{data: w}
	e := decodePreR13Text(r, head, 39)
	if e.text != text {
		t.Errorf("GBK 码页文本=%q, want %q", e.text, text)
	}
	if !testsupport.NearEq(e.insertion.x, 10.0) || !testsupport.NearEq(e.insertion.y, 20.0) || !testsupport.NearEq(e.height, 2.5) {
		t.Errorf("TEXT 几何 = (%v,%v) h=%v", e.insertion.x, e.insertion.y, e.height)
	}

	// 默认码页 30（windows-1252 家族 Latin-1 近似）：GBK 字节不解合并，
	// 与历史行为一致（13 个 ASCII 字节各 1 字节、8 个高位字节映射为
	// 2 字节 UTF-8，len=13+8×2=29）
	r2 := &preR13Reader{data: w}
	e2 := decodePreR13Text(r2, head, 30)
	if e2.text == text {
		t.Error("码页 30 不应解出 GBK 中文（行为回归检查）")
	}
	if len(e2.text) != 29 {
		t.Errorf("码页 30 下应保持逐字节 Latin-1 映射：len=%d want 29", len(e2.text))
	}
}

// TestPreR13AttribCodepageDecode 合成 ATTRIB 文字/标签 TV 按码页解码自证。
func TestPreR13AttribCodepageDecode(t *testing.T) {
	const tag = "编号"
	gbk, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(tag))
	if err != nil {
		t.Fatalf("GBK 编码失败: %v", err)
	}
	tv := func(s []byte) []byte {
		out := []byte{byte(len(s)), byte(len(s) >> 8)}
		return append(out, s...)
	}
	w := []byte{}
	appendRD := func(f float64) {
		bits := math.Float64bits(f)
		for i := 0; i < 8; i++ {
			w = append(w, byte(bits>>(8*i)))
		}
	}
	appendRD(1.0)
	appendRD(2.0)
	appendRD(2.5)                     // height
	w = append(w, tv(gbk)...)         // text
	w = append(w, tv([]byte("P"))...) // prompt（ATTDEF）
	w = append(w, tv(gbk)...)         // tag
	w = append(w, 0)                  // flags RC

	head := preR13EntHead{typ: preR13TypeAttdef, opts: 0}
	r := &preR13Reader{data: w}
	e := decodePreR13Attrib(r, head, true, 39)
	if e.text != tag {
		t.Errorf("ATTRIB text=%q, want %q", e.text, tag)
	}
	if e.tag != tag {
		t.Errorf("ATTRIB tag=%q, want %q", e.tag, tag)
	}
	if e.prompt != "P" {
		t.Errorf("ATTRIB prompt=%q, want %q", e.prompt, "P")
	}
}
