// crc.go 实现 DWG 位流的 CRC16 计算与写出，与读侧 bitStream.readCRC 的
// 「对齐后读 16 位」语义对称：写出端对 [startBit, 当前位) 覆盖的字节区间
// 计算 CRC 后按 RS（小端 2 字节）写入。
// 算法为 CRC-16/ARC（反射多项式 0xA001），初值由 seed 传入：文件头与
// system section 惯例 0xC0C1，R2000 对象图块惯例 0x00。
// 对应 LibreDWG 的 crctable / bit_calc_CRC / bit_write_CRC(dat, addr, 0xC0C1)。
package bitstream

// crc16Table DWG CRC16 查询表：table[i] 为字节 i 独立贡献的 16 位余式，
// 由反射多项式 0xA001 展开 8 次生成（与 LibreDWG crctable 一致）。
var crc16Table = func() (t [256]uint16) {
	for i := 0; i < 256; i++ {
		c := uint16(i)
		for j := 0; j < 8; j++ {
			if c&1 != 0 {
				c = c>>1 ^ 0xA001
			} else {
				c >>= 1
			}
		}
		t[i] = c
	}
	return t
}()

// crc16DWG 以 seed 为初值累积计算 data 的 DWG CRC16。
// seed 允许链式分段计算：crc16DWG(crc16DWG(s, a), b) == crc16DWG(s, a||b)。
func Crc16DWG(seed uint16, Data []byte) uint16 {
	crc := seed
	for _, b := range Data {
		crc = crc>>8 ^ crc16Table[byte(crc)^b]
	}
	return crc
}

// writeCRC 对齐到字节边界（未对齐时当前部分字节补零）后，对从 startBit
// （须为字节边界）到当前写入位覆盖的字节区间计算 CRC（seed 0xC0C1）并写入，
// 返回 CRC 值。区间不含 CRC 自身。对应 LibreDWG bit_write_CRC 的默认调用形式。
func (w *EncWriter) WriteCRC(startBit uint64) uint16 {
	return w.WriteCRCSeed(startBit, 0xC0C1)
}

// writeCRCSeed 同 writeCRC，seed 由调用方指定（对象图等场景用 0）。
func (w *EncWriter) WriteCRCSeed(startBit uint64, seed uint16) uint16 {
	w.AlignByte()
	start := (startBit + 7) / 8
	if start > uint64(len(w.Data)) {
		start = uint64(len(w.Data))
	}
	crc := Crc16DWG(seed, w.Data[start:])
	w.WriteRS(crc)
	return crc
}

// alignByte 对齐到字节边界：位偏移非零时以 0 填满当前字节剩余位。
func (w *EncWriter) AlignByte() {
	if w.Bit != 0 {
		w.WriteBitsMsb(0, 8-w.Bit)
	}
}
