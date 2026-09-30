// lz77.go 实现 DWG R2004+ 页面数据的 Autodesk LZ77 变体解压。
// 该算法以字节流 opcode 驱动（非位流）：由字面量前导长度与
// (复制长度, 回溯偏移) 对交替组成，是 R2004 家族 system section 与
// data page 的统一压缩方式。各 opcode 形态由形态表 lz77Forms 描述，
// 主循环查表分派。
package cad

import "fmt"

// lz77Stream 压缩流游标：at 为下一读取位置。
type lz77Stream struct {
	src []byte
	at  int
}

// take 消费一个字节，耗尽即报错。
func (s *lz77Stream) take() (uint8, error) {
	if s.at >= len(s.src) {
		return 0, fmt.Errorf("cad: 压缩流意外结束")
	}
	v := s.src[s.at]
	s.at++
	return v, nil
}

// splitWord 消费 2 字节并拆包：高 8 位来自第二字节，低 2 位是其后内嵌
// 的字面量计数，其余 10 位为复制偏移。
func (s *lz77Stream) splitWord() (offset, litCount int, err error) {
	b1, err := s.take()
	if err != nil {
		return
	}
	b2, err := s.take()
	if err != nil {
		return
	}
	offset = int(b1>>2) | int(b2)<<6
	litCount = int(b1 & 0x03)
	return
}

// extendedCount 读取扩展长度链：非零首字节即值；0x00 进入续字节链
// （每个 0x00 续字节累计 255），链尾非零字节并入合计。
func (s *lz77Stream) extendedCount() (int, error) {
	b, err := s.take()
	if err != nil {
		return 0, err
	}
	total := 0
	if b == 0x00 {
		total = 0xFF
		for {
			next, e := s.take()
			if e != nil {
				return 0, e
			}
			if next != 0x00 {
				b = next
				break
			}
			total += 0xFF
		}
	}
	return total + int(b), nil
}

// literalRun 读取字面量游程长度，返回 (长度, 复用的下一 opcode)。
// 0x01-0x0F 为短长度（+3）；高半字节非零的字节实为下一条复制 opcode，
// 返回给调用方直接消费；0x00 进入扩展链（0x0F 基数 + 每 0x00 续字节
// 累计 255）。
func (s *lz77Stream) literalRun() (int, uint8, error) {
	b, err := s.take()
	if err != nil {
		return 0, 0, err
	}
	switch {
	case b >= 0x01 && b <= 0x0F:
		return int(b) + 3, 0, nil
	case b&0xF0 != 0:
		return 0, b, nil
	case b == 0x00:
		length := 0x0F
		for {
			next, e := s.take()
			if e != nil {
				return 0, 0, e
			}
			if next != 0x00 {
				return length + int(next) + 3, 0, nil
			}
			length += 0xFF
		}
	}
	return 0, 0, nil
}

// lz77Form 复制指令形态：lo..hi 为 opcode 区间，长度 = lenBase + opcode
// 贡献（opMask 取 0x0F/0xFF，0 表示无 opcode 项）+ 扩展链（ext）；
// offBase 为偏移基数（0x3FFF 形态或 0）。
type lz77Form struct {
	lo, hi  uint8
	lenBase int
	opMask  uint8
	ext     bool
	offBase int
}

// lz77Forms 长复制形态表（紧凑形态 opcode≥0x40 结构不同，单独处理；
// 0x11 为流结束标记）：
//   - 0x10        长度=扩展链+9，偏移=字对+0x3FFF
//   - 0x12..0x1F  长度=低半字节+2，偏移=字对+0x3FFF
//   - 0x20        长度=扩展链+0x21，偏移=字对
//   - 0x21..0x3F  长度=opcode-0x1E，偏移=字对
var lz77Forms = []lz77Form{
	{0x10, 0x10, 9, 0, true, 0x3FFF},
	{0x12, 0x1F, 2, 0x0F, false, 0x3FFF},
	{0x20, 0x20, 0x21, 0, true, 0},
	{0x21, 0x3F, -0x1E, 0xFF, false, 0},
}

// lz77FormOf 查 opcode 对应的复制形态；未登记返回 nil。
func lz77FormOf(op uint8) *lz77Form {
	for i := range lz77Forms {
		if op >= lz77Forms[i].lo && op <= lz77Forms[i].hi {
			return &lz77Forms[i]
		}
	}
	return nil
}

// emitLiterals 从压缩流搬移 length 字节原始字面量到输出并推进游标。
func emitLiterals(dst []byte, s *lz77Stream, length int) ([]byte, error) {
	if length == 0 {
		return dst, nil
	}
	if s.at+length > len(s.src) {
		return nil, fmt.Errorf("cad: 字面量游程超出压缩数据")
	}
	dst = append(dst, s.src[s.at:s.at+length]...)
	s.at += length
	return dst, nil
}

// emitWindow 从输出回溯 distance 字节处复制 length 字节（允许重叠）。
// 回溯越界（损坏数据）按参考行为以零填充保持宽容。
func emitWindow(dst []byte, distance, length int) []byte {
	if length == 0 {
		return dst
	}
	if distance > len(dst) {
		return append(dst, make([]byte, length)...)
	}
	base := len(dst) - distance
	for i := 0; i < length; i++ {
		dst = append(dst, dst[base+i])
	}
	return dst
}

// lz77FitSize 将解压输出对齐到声明的目标尺寸（截断或补零）。
func lz77FitSize(dst []byte, dstSize int) []byte {
	switch {
	case len(dst) > dstSize:
		return dst[:dstSize]
	case len(dst) < dstSize:
		padded := make([]byte, dstSize)
		copy(padded, dst)
		return padded
	}
	return dst
}

// decompressLZ77 将 src 解压为 dstSize 字节的输出；dstSize==0 直接返回空。
func decompressLZ77(src []byte, dstSize int) ([]byte, error) {
	if dstSize == 0 {
		return []byte{}, nil
	}
	var out []byte
	s := &lz77Stream{src: src}

	litLen, nextOp, err := s.literalRun()
	if err != nil {
		return nil, err
	}
	if out, err = emitLiterals(out, s, litLen); err != nil {
		return nil, err
	}

	for s.at < len(src) {
		if nextOp == 0x00 {
			if nextOp, err = s.take(); err != nil {
				return nil, err
			}
		}
		op := nextOp
		nextOp = 0x00

		if op == 0x11 {
			// 流结束标记
			return lz77FitSize(out, dstSize), nil
		}
		var copyLen, backOff int
		if op >= 0x40 {
			// 紧凑形态：高半字节-1 为复制长度，低 2+2 位参与偏移，
			// 低 2 位兼作其后内嵌字面量计数
			copyLen = int(op&0xF0)>>4 - 1
			op2, e := s.take()
			if e != nil {
				return nil, e
			}
			backOff = int(op2)<<2 | int(op&0x0C)>>2
			if op&0x03 != 0 {
				litLen = int(op & 0x03)
			} else {
				if litLen, nextOp, err = s.literalRun(); err != nil {
					return nil, err
				}
			}
		} else {
			form := lz77FormOf(op)
			if form == nil {
				return nil, fmt.Errorf("cad: 非法 LZ77 压缩 opcode 0x%02X", op)
			}
			copyLen = form.lenBase
			if form.opMask == 0xFF {
				copyLen += int(op)
			} else if form.opMask == 0x0F {
				copyLen += int(op & form.opMask)
			}
			if form.ext {
				ext, e := s.extendedCount()
				if e != nil {
					return nil, e
				}
				copyLen += ext
			}
			var litCount int
			var off int
			if off, litCount, err = s.splitWord(); err != nil {
				return nil, err
			}
			backOff = off + form.offBase
			if litCount == 0 {
				if litLen, nextOp, err = s.literalRun(); err != nil {
					return nil, err
				}
			} else {
				litLen = litCount
			}
		}

		out = emitWindow(out, backOff+1, copyLen)
		if out, err = emitLiterals(out, s, litLen); err != nil {
			return nil, err
		}
	}

	return lz77FitSize(out, dstSize), nil
}
