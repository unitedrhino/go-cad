// r21 解压器：R2007（AC1021）专用压缩算法，与 R2004 的 LZ77 变体不同。
// 流结构：可选 0x20 引导 opcode → 循环「字面量长度 + 字面量 + 回溯引用
// 块序列」。字面量块与回溯引用的拷贝均按字节序怪癖规则执行（2/3 字节块
// 反序、16 字节块两半块交换），由 r21ChunkPermute 统一描述。
package container

import (
	"fmt"
	"os"
)

// r21Trace 环境变量门控的解压 trace（CAD_R21_TRACE）。
var r21Trace = os.Getenv("CAD_R21_TRACE") != ""

// r21Decoder R21 解压上下文：src 为压缩流（at 为游标），dst 为预分配的
// 输出缓冲（put 为写入游标）。
type r21Decoder struct {
	src []byte
	at  int
	dst []byte
	put int
}

// u8 消费一个字节，耗尽即报错。
func (d *r21Decoder) u8() (int, error) {
	if d.at >= len(d.src) {
		return 0, fmt.Errorf("cad: R2007 压缩流读取越界")
	}
	v := int(d.src[d.at])
	d.at++
	return v, nil
}

// literalRun 由字面量长度 opcode 计算字面量长度：opcode+8；0x0F（0x17）
// 追加单字节，0xFF 再进 16 位小端累加链（每对最大累加 0xFFFF，出现
// 0xFFFF 续读下一对）。
func (d *r21Decoder) literalRun(opcode int) (int, error) {
	Length := opcode + 8
	if Length != 0x17 {
		return Length, nil
	}
	n, err := d.u8()
	if err != nil {
		return 0, fmt.Errorf("cad: R2007 字面量长度读取越界")
	}
	Length += n
	if n != 0xFF {
		return Length, nil
	}
	for {
		if d.at+2 > len(d.src) {
			return 0, fmt.Errorf("cad: R2007 字面量扩展越界")
		}
		lo := int(d.src[d.at])
		hi := int(d.src[d.at+1])
		d.at += 2
		n = lo | hi<<8
		Length += n
		if n != 0xFFFF {
			return Length, nil
		}
	}
}

// r21Instr 单条回溯引用指令解析结果。
type r21Instr struct {
	nextOp int // 指令尾字节（低 3 位携带其后字面量长度）
	Offset int
	Length int
}

// instr 按高半字节解析回溯引用指令的四种形态：
//   - 0x0_：长 0x13+低半字节，9 位拼合偏移，长可再 += 尾字节 (>>3)&0x10
//   - 0x1_：长 3+低半字节，13 位拼合偏移
//   - 0x2_：16 位偏移（0x08 标志区分长度扩展的两种拼接）
//   - 其余：紧凑形态，长=高半字节，5 位拼合偏移+1
func (d *r21Decoder) instr(op int) (r21Instr, error) {
	switch op >> 4 {
	case 0:
		b, err := d.u8()
		if err != nil {
			return r21Instr{}, err
		}
		Offset := b
		b, err = d.u8()
		if err != nil {
			return r21Instr{}, err
		}
		return r21Instr{
			nextOp: b,
			Length: (op & 0x0F) + 0x13 + ((b >> 3) & 0x10),
			Offset: ((b&0x78)<<5 + 1) + Offset,
		}, nil
	case 1:
		b, err := d.u8()
		if err != nil {
			return r21Instr{}, err
		}
		Offset := b
		b, err = d.u8()
		if err != nil {
			return r21Instr{}, err
		}
		return r21Instr{
			nextOp: b,
			Length: (op & 0x0F) + 0x03,
			Offset: ((b&0xF8)<<5 + 1) + Offset,
		}, nil
	case 2:
		b, err := d.u8()
		if err != nil {
			return r21Instr{}, err
		}
		off := b
		b, err = d.u8()
		if err != nil {
			return r21Instr{}, err
		}
		off |= (b << 8) & 0xFF00
		Length := op & 0x07
		if op&0x08 == 0 {
			tail, err := d.u8()
			if err != nil {
				return r21Instr{}, err
			}
			Length += tail & 0xF8
			return r21Instr{nextOp: tail, Offset: off, Length: Length}, nil
		}
		off++
		b, err = d.u8()
		if err != nil {
			return r21Instr{}, err
		}
		Length += b << 3
		tail, err := d.u8()
		if err != nil {
			return r21Instr{}, err
		}
		return r21Instr{
			nextOp: tail,
			Length: ((tail&0xF8)<<8 + Length) + 0x100,
			Offset: off,
		}, nil
	default:
		b, err := d.u8()
		if err != nil {
			return r21Instr{}, err
		}
		return r21Instr{
			nextOp: b,
			Length: op >> 4,
			Offset: ((b&0xF8)<<1 + op&0x0F) + 1,
		}, nil
	}
}

// copyRun 从输出缓冲回溯拷贝 length 字节（逐字节，支持重叠区域）。
func (d *r21Decoder) copyRun(Offset, Length int) error {
	srcPos := d.put - Offset
	if srcPos < 0 {
		return fmt.Errorf("cad: R2007 回溯偏移超前缀（%d > %d）", Offset, d.put)
	}
	if d.put+Length > len(d.dst) {
		return fmt.Errorf("cad: R2007 输出写入越界")
	}
	for i := 0; i < Length; i++ {
		if srcPos+i >= len(d.dst) {
			return fmt.Errorf("cad: R2007 回溯读取越界")
		}
		d.dst[d.put+i] = d.dst[srcPos+i]
	}
	d.put += Length
	return nil
}

// copyRuns 执行一条回溯引用链（连续多段复制），返回其后字面量状态：
// litPending>0 为内嵌字面量长度；litPending==0 时 litOpcode 是下一字面量
// 长度 opcode（或链尾出现的高半字节 0 字节）。
func (d *r21Decoder) copyRuns() (litPending, litOpcode int, err error) {
	op, err := d.u8()
	if err != nil {
		return 0, 0, err
	}
	var ins r21Instr
	if ins, err = d.instr(op); err != nil {
		return 0, 0, err
	}
	for {
		if r21Trace {
			fmt.Fprintf(os.Stderr, "[r21] copy opcode=%#x off=%d len=%d src=%d dst=%d\n", op, ins.Offset, ins.Length, d.at, d.put)
		}
		if err = d.copyRun(ins.Offset, ins.Length); err != nil {
			return 0, 0, err
		}
		litPending = ins.nextOp & 0x07
		if litPending != 0 || d.at >= len(d.src) {
			return litPending, op, nil
		}
		if op, err = d.u8(); err != nil {
			return 0, 0, err
		}
		if op>>4 == 0 {
			// 高半字节 0：该字节本身是下一字面量长度 opcode
			return 0, op, nil
		}
		if op>>4 == 15 {
			op &= 0x0F
		}
		if ins, err = d.instr(op); err != nil {
			return 0, 0, err
		}
	}
}

// r21ChunkPermute n 字节块内输出第 j 字节对应的源字节偏移（R21 拷贝的
// 字节序怪癖：2/3 字节块整体反序；16 字节块 = 两个 8 字节半块交换；
// 1/4/8 字节块直序）。
func r21ChunkPermute(n, j int) int {
	switch n {
	case 2:
		return 1 - j
	case 3:
		return 2 - j
	case 16:
		if j < 8 {
			return j + 8
		}
		return j - 8
	default:
		return j
	}
}

// r21CopyPlan 余数长度 1..31 的拷贝组成：(块大小, 流内偏移) 序列，
// 按数组顺序执行；块大小走 r21ChunkPermute 字节序规则。
var r21CopyPlans = [32][]struct {
	n  uint8
	at uint8
}{
	{},
	{{1, 0}},
	{{2, 0}},
	{{3, 0}},
	{{4, 0}},
	{{1, 4}, {4, 0}},
	{{1, 5}, {4, 1}, {1, 0}},
	{{2, 5}, {4, 1}, {1, 0}},
	{{4, 0}, {4, 4}},
	{{1, 8}, {8, 0}},
	{{1, 9}, {8, 1}, {1, 0}},
	{{2, 9}, {8, 1}, {1, 0}},
	{{4, 8}, {8, 0}},
	{{1, 12}, {4, 8}, {8, 0}},
	{{1, 13}, {4, 9}, {8, 1}, {1, 0}},
	{{2, 13}, {4, 9}, {8, 1}, {1, 0}},
	{{16, 0}},
	{{8, 9}, {1, 8}, {8, 0}},
	{{1, 17}, {16, 1}, {1, 0}},
	{{3, 16}, {16, 0}},
	{{4, 16}, {16, 0}},
	{{1, 20}, {4, 16}, {16, 0}},
	{{2, 20}, {4, 16}, {16, 0}},
	{{3, 20}, {4, 16}, {16, 0}},
	{{8, 16}, {16, 0}},
	{{8, 17}, {1, 16}, {16, 0}},
	{{1, 25}, {8, 17}, {1, 16}, {16, 0}},
	{{2, 25}, {8, 17}, {1, 16}, {16, 0}},
	{{4, 24}, {8, 16}, {16, 0}},
	{{1, 28}, {4, 24}, {8, 16}, {16, 0}},
	{{2, 28}, {4, 24}, {8, 16}, {16, 0}},
	{{1, 30}, {4, 26}, {8, 18}, {16, 2}, {2, 0}},
}

// emitBlock 按字节序怪癖从流内 srcAt 拷贝 n 字节到输出。
func (d *r21Decoder) emitBlock(srcAt, n int) error {
	for j := 0; j < n; j++ {
		si := srcAt + r21ChunkPermute(n, j)
		o := d.put + j
		if si >= len(d.src) || o >= len(d.dst) {
			return fmt.Errorf("cad: R2007 拷贝越界")
		}
		d.dst[o] = d.src[si]
	}
	d.put += n
	return nil
}

// emitChunk 拷贝 length 字节字面量块：≥32 按「半块交换」的 16 字节步进，
// 余数 1..31 查拷贝计划表执行。
func (d *r21Decoder) emitChunk(srcAt, Length int) error {
	for Length >= 32 {
		if err := d.emitBlock(srcAt+16, 16); err != nil {
			return err
		}
		if err := d.emitBlock(srcAt, 16); err != nil {
			return err
		}
		srcAt += 32
		Length -= 32
	}
	for _, step := range r21CopyPlans[Length] {
		if err := d.emitBlock(srcAt+int(step.at), int(step.n)); err != nil {
			return err
		}
	}
	return nil
}

// decompressR21 解压 R2007 压缩流到 dstSize 字节；dstSize==0 返回空。
func DecompressR21(src []byte, dstSize int) ([]byte, error) {
	if dstSize == 0 {
		return []byte{}, nil
	}
	if len(src) == 0 {
		return nil, fmt.Errorf("cad: R2007 压缩流为空")
	}
	d := &r21Decoder{src: src, dst: make([]byte, dstSize)}

	lead, err := d.u8()
	if err != nil {
		return nil, err
	}
	litOpcode := lead
	litPending := 0
	if lead&0xF0 == 0x20 {
		// 0x20 引导：跳过 2 字节保留字段，第 4 字节低 3 位为首段字面量长度
		d.at += 2
		if d.at >= len(d.src) {
			return nil, fmt.Errorf("cad: R2007 opcode 引导越界")
		}
		litPending = int(d.src[d.at] & 0x07)
		d.at++
	}

	for d.at < len(d.src) {
		Length := litPending
		if Length == 0 {
			if Length, err = d.literalRun(litOpcode); err != nil {
				return nil, err
			}
		}
		if r21Trace {
			fmt.Fprintf(os.Stderr, "[r21] literal opcode=%#x len=%d src=%d dst=%d\n", litOpcode, Length, d.at, d.put)
		}
		if d.put+Length > dstSize {
			break
		}
		// 字面量块：与回溯引用一样经由拷贝组合（含字节序怪癖）
		if err = d.emitChunk(d.at, Length); err != nil {
			return nil, err
		}
		d.at += Length
		if d.at >= len(d.src) {
			break
		}
		if litPending, litOpcode, err = d.copyRuns(); err != nil {
			return nil, err
		}
	}
	return d.dst, nil
}
