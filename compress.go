// compress.go 实现两个 DWG 写出方向压缩器，与解压端逐格式互逆：
//   - compressLZ77：R2004+ 页面数据的 Autodesk LZ77 变体（逆向 lz77.go）；
//   - compressR21：R2007 专用 R21 流（逆向 r21.go，含字面量块重排怪癖）。
//
// 两者均采用「哈希链贪心匹配 + 全字面量兜底」策略，输出保证能被本包
// decompressLZ77 / decompressR21 逐字节还原；不追求与 Autodesk 原生
// 压缩器字节一致，正确性优先于压缩率。
package cad

// lzMatch 一次贪心回溯匹配：在 pos 处声明「回溯 dist 复制 length 字节」。
type lzMatch struct {
	pos    int
	dist   int
	length int
}

// lzGreedyMatches 哈希链贪心匹配扫描：对每个位置在窗口 [pos-maxDist, pos)
// 内找最长匹配（允许重叠，重叠对应解压端逐字节复制语义）。
// 匹配起点不早于 minMatch（保证首个匹配前的字面量段可独立编码），
// 单条匹配长度不超过 maxLen（超过则拆成多条相邻匹配）。
func lzGreedyMatches(src []byte, minMatch, maxDist, maxLen int) []lzMatch {
	const hashBits = 15
	n := len(src)
	head := make([]int32, 1<<hashBits)
	for i := range head {
		head[i] = -1
	}
	prev := make([]int32, n)
	hashAt := func(p int) uint32 {
		h := uint32(src[p])<<16 | uint32(src[p+1])<<8 | uint32(src[p+2])
		return (h * 2654435761) >> (32 - hashBits)
	}
	insert := func(p int) {
		if p+3 > n {
			return
		}
		h := hashAt(p)
		prev[p] = head[h]
		head[h] = int32(p)
	}
	var matches []lzMatch
	for pos := 0; pos+minMatch <= n; pos++ {
		best, bestDist := 0, 0
		// 查找须在插入当前位之前：回溯距离至少为 1
		if pos >= minMatch {
			limit := pos - maxDist
			cand := head[hashAt(pos)]
			for steps := 0; cand >= int32(limit) && cand >= 0 && steps < 64; steps++ {
				c := int(cand)
				l := 0
				for l < n-pos && src[c+l] == src[pos+l] {
					l++
				}
				if l > best {
					best, bestDist = l, pos-c
					if best >= maxLen {
						break
					}
				}
				cand = prev[c]
			}
		}
		insert(pos)
		if best >= minMatch {
			if best > maxLen {
				best = maxLen
			}
			matches = append(matches, lzMatch{pos: pos, dist: bestDist, length: best})
			for p := pos + 1; p < pos+best && p+3 <= n; p++ {
				insert(p)
			}
			pos += best - 1
		}
	}
	return matches
}

// compressLZ77LiteralOnly 输出全字面量 LZ77 流（零匹配）：输出长度是
// 输入长度的确定性函数、与内容无关。专供 R2004 页表页使用——LibreDWG
// 读侧对系统段无条件走 LZ77 解压（不识别 store/compression_type=1，
// 0.14 实测 decode.c:1706 无条件调用），而页表内容含自身条目数值，
// 启用匹配的压缩会因数值抖动产生自引用长度振荡（173↔175 二循环），
// 全字面量流从根上消除振荡。
func compressLZ77LiteralOnly(src []byte) []byte {
	n := len(src)
	if n == 0 {
		return []byte{0x11}
	}
	if n < 4 {
		data := make([]byte, 4)
		copy(data, src)
		return append(append([]byte{0x01}, data...), 0x11)
	}
	out := lzWriteLitLen(make([]byte, 0, n+n/16+8), n)
	out = append(out, src...)
	return append(out, 0x11)
}

// ---- R2004 Autodesk LZ77 变体压缩 ----

// compressLZ77 将 src 压缩为可被 decompressLZ77(src, len(src)) 还原的流。
// 长度 <4 的输入用零填充到最短字面量块（解压端按声明尺寸截断还原），
// 空输入输出单个 0x11 终止符。
func compressLZ77(src []byte) []byte {
	n := len(src)
	if n == 0 {
		return []byte{0x11}
	}
	if n < 4 {
		// 字面量游程最短可编码长度为 4：补零占位，解压端按 dstSize 截断
		data := make([]byte, 4)
		copy(data, src)
		return append(append([]byte{0x01}, data...), 0x11)
	}

	matches := lzGreedyMatches(src, 4, 0x7FFF, n)
	out := make([]byte, 0, n/2+16)
	if len(matches) == 0 {
		out = lzWriteLitLen(out, n)
		out = append(out, src...)
		return append(out, 0x11)
	}
	if matches[0].pos > 0 {
		// 首匹配起点 ≥ minMatch，前导字面量段必然可独立编码
		out = lzWriteLitLen(out, matches[0].pos)
		out = append(out, src[:matches[0].pos]...)
	}
	for i, m := range matches {
		// 该匹配之后的字面量段（至下一匹配或流尾）：1..3 内嵌进匹配块
		// 低 2 位，≥4 单独编码——与解压端 litCount 分支一一对应
		segStart := m.pos + m.length
		segEnd := n
		if i+1 < len(matches) {
			segEnd = matches[i+1].pos
		}
		litLen := segEnd - segStart
		litCount := 0
		if litLen <= 3 {
			litCount = litLen
		}
		out = lzWriteMatch(out, m.dist, m.length, litCount)
		if litCount > 0 {
			out = append(out, src[segStart:segEnd]...)
		} else if litLen > 0 {
			out = lzWriteLitLen(out, litLen)
			out = append(out, src[segStart:segEnd]...)
		}
	}
	return append(out, 0x11)
}

// lzWriteLitLen 写字面量游程长度（lz77LiteralRunX 的逆向）：
// 4..18 短编码（值-3），19+ 走 0x00 扩展链（0x0F 基数 + 255×k + 尾字节）。
func lzWriteLitLen(out []byte, length int) []byte {
	if length <= 18 {
		return append(out, byte(length-3))
	}
	e := length - 18
	k := (e - 1) / 255
	t := e - 255*k
	out = append(out, 0x00)
	for i := 0; i < k; i++ {
		out = append(out, 0x00)
	}
	return append(out, byte(t))
}

// lzWriteExtLen 写扩展复制长度链（lz77ExtChainX 的逆向）：
// 1..255 单字节，256+ 走 0x00 扩展链（0xFF 基数 + 255×k + 尾字节）。
func lzWriteExtLen(out []byte, length int) []byte {
	if length <= 255 {
		return append(out, byte(length))
	}
	e := length - 255
	k := (e - 1) / 255
	t := e - 255*k
	out = append(out, 0x00)
	for i := 0; i < k; i++ {
		out = append(out, 0x00)
	}
	return append(out, byte(t))
}

// lzWriteMatch 写一个回溯匹配块，litCount 为其后紧跟的字面量字节数（0..3，
// 内嵌在偏移字段低 2 位）。按距离与长度选择最省字节的 opcode 格式：
//   - 0x40-0xFF：近距（≤0x400）短匹配（3..14），2 字节；
//   - 0x21-0x3F / 0x20：中距（≤0x4000），长度 ≤33 / 长度链；
//   - 0x12-0x1F / 0x10：远距（0x4000+0x4000 基准），长度 ≤17 / 长度链。
func lzWriteMatch(out []byte, dist, length, litCount int) []byte {
	switch {
	case dist <= 0x400 && length <= 14:
		off := dist - 1
		op := byte((length+1)<<4) | byte(off&0x03)<<2 | byte(litCount)
		return append(out, op, byte(off>>2))
	case dist <= 0x4000 && length <= 33:
		off := dist - 1
		return append(out, byte(0x1E+length), byte(off&0x3F)<<2|byte(litCount), byte(off>>6))
	case dist <= 0x4000:
		off := dist - 1
		out = append(out, 0x20)
		out = lzWriteExtLen(out, length-0x21)
		return append(out, byte(off&0x3F)<<2|byte(litCount), byte(off>>6))
	case length <= 17:
		off := dist - 0x4000
		return append(out, byte(0x10|(length-2)), byte(off&0x3F)<<2|byte(litCount), byte(off>>6))
	default:
		off := dist - 0x4000
		out = append(out, 0x10)
		out = lzWriteExtLen(out, length-9)
		return append(out, byte(off&0x3F)<<2|byte(litCount), byte(off>>6))
	}
}

// ---- R2007 (AC1021) R21 压缩 ----

// r21MaxMatchLen 单条匹配指令可编码的最大复制长度：
// 16 位偏移扩展格式的长度上限（0xF800 + 0x7F8 + 0x7 + 0x100）。
const r21MaxMatchLen = 0x100FF

// r21ChunkOrder 余数长度 r（0..31）的字面量块重排表：
// 压缩流第 k 个字节取原始块内 r21ChunkOrder[r][k] 下标的字节，
// 由解压端 r21CopyCompressedChunk 的拷贝组合逆向推导。
var r21ChunkOrder [32][32]int

func init() {
	// chunk 一段同构拷贝：从流内偏移 f 起连续 c 字节（rev 表示反序），
	// 依次写入输出的 p, p+1, ... 位置（与解压端逐 case 对照）。
	type chunk struct {
		f, p, c int
		rev     bool
	}
	rows := [][]chunk{
		{},
		{{0, 0, 1, false}},
		{{0, 0, 2, true}},
		{{0, 0, 3, true}},
		{{0, 0, 4, false}},
		{{4, 0, 1, false}, {0, 1, 4, false}},
		{{5, 0, 1, false}, {1, 1, 4, false}, {0, 5, 1, false}},
		{{5, 0, 2, true}, {1, 2, 4, false}, {0, 6, 1, false}},
		{{0, 0, 4, false}, {4, 4, 4, false}},
		{{8, 0, 1, false}, {0, 1, 8, false}},
		{{9, 0, 1, false}, {1, 1, 8, false}, {0, 9, 1, false}},
		{{9, 0, 2, true}, {1, 2, 8, false}, {0, 10, 1, false}},
		{{8, 0, 4, false}, {0, 4, 8, false}},
		{{12, 0, 1, false}, {8, 1, 4, false}, {0, 5, 8, false}},
		{{13, 0, 1, false}, {9, 1, 4, false}, {1, 5, 8, false}, {0, 13, 1, false}},
		{{13, 0, 2, true}, {9, 2, 4, false}, {1, 6, 8, false}, {0, 14, 1, false}},
		{{8, 0, 8, false}, {0, 8, 8, false}},
		{{9, 0, 8, false}, {8, 8, 1, false}, {0, 9, 8, false}},
		{{17, 0, 1, false}, {9, 1, 8, false}, {1, 9, 8, false}, {0, 17, 1, false}},
		{{16, 0, 3, true}, {8, 3, 8, false}, {0, 11, 8, false}},
		{{16, 0, 4, false}, {8, 4, 8, false}, {0, 12, 8, false}},
		{{20, 0, 1, false}, {16, 1, 4, false}, {8, 5, 8, false}, {0, 13, 8, false}},
		{{20, 0, 2, true}, {16, 2, 4, false}, {8, 6, 8, false}, {0, 14, 8, false}},
		{{20, 0, 3, true}, {16, 3, 4, false}, {8, 7, 8, false}, {0, 15, 8, false}},
		{{16, 0, 8, false}, {8, 8, 8, false}, {0, 16, 8, false}},
		{{17, 0, 8, false}, {16, 8, 1, false}, {8, 9, 8, false}, {0, 17, 8, false}},
		{{25, 0, 1, false}, {17, 1, 8, false}, {16, 9, 1, false}, {8, 10, 8, false}, {0, 18, 8, false}},
		{{25, 0, 2, true}, {17, 2, 8, false}, {16, 10, 1, false}, {8, 11, 8, false}, {0, 19, 8, false}},
		{{24, 0, 4, false}, {16, 4, 8, false}, {8, 12, 8, false}, {0, 20, 8, false}},
		{{28, 0, 1, false}, {24, 1, 4, false}, {16, 5, 8, false}, {8, 13, 8, false}, {0, 21, 8, false}},
		{{28, 0, 2, true}, {24, 2, 4, false}, {16, 6, 8, false}, {8, 14, 8, false}, {0, 22, 8, false}},
		{{30, 0, 1, false}, {26, 1, 4, false}, {18, 5, 8, false}, {10, 13, 8, false}, {2, 21, 8, false}, {0, 29, 2, true}},
	}
	for r, cs := range rows {
		for _, c := range cs {
			for j := 0; j < c.c; j++ {
				src := j
				if c.rev {
					src = c.c - 1 - j
				}
				r21ChunkOrder[r][c.f+src] = c.p + j
			}
		}
	}
}

// compressR21 将 src 压缩为可被 decompressR21(src, len(src)) 还原的流。
// 空输入返回空流（解压端对 dstSize=0 直接返回）；长度 <8 时用 0x20 引导
// 头编码短字面量（引导长度字段仅 3 位），否则以字面量长度 opcode 开流。
func compressR21(src []byte) []byte {
	n := len(src)
	if n == 0 {
		return []byte{}
	}
	matches := lzGreedyMatches(src, 4, 0xFFFF, r21MaxMatchLen)
	out := make([]byte, 0, n/2+16)
	litEnd := n
	if len(matches) > 0 {
		litEnd = matches[0].pos
	}
	if litEnd >= 8 {
		out = r21WriteLitLen(out, litEnd)
	} else {
		// 0x20 引导：跳过 2 字节保留字段，第 4 字节低 3 位为首段字面量长度
		out = append(out, 0x20, 0x00, 0x00, byte(litEnd))
	}
	out = r21AppendLiteral(out, src[:litEnd])
	for i, m := range matches {
		segStart := m.pos + m.length
		segEnd := n
		if i+1 < len(matches) {
			segEnd = matches[i+1].pos
		}
		litLen := segEnd - segStart
		// 指令尾字节低 3 位携带其后字面量长度：1..7 直接内嵌；
		// 0 表示「无字面量」或「下个字节为独立字面量长度 opcode」
		nextLit := 0
		if litLen <= 7 {
			nextLit = litLen
		}
		out = r21WriteMatch(out, m.dist, m.length, nextLit)
		if nextLit > 0 {
			out = r21AppendLiteral(out, src[segStart:segEnd])
		} else if litLen > 0 {
			out = r21WriteLitLen(out, litLen)
			out = r21AppendLiteral(out, src[segStart:segEnd])
		}
	}
	return out
}

// r21WriteLitLen 写字面量前导长度（解压端 r21Decoder.literalRun 的逆向）：
// 8..22 直接 opcode（值-8）；23+ 走 0x0F 扩展（+单字节，0xFF 再进 16 位
// 小端累加链，链上每对最大累加 0xFFFF，出现 0xFFFF 时续写下一对）。
func r21WriteLitLen(out []byte, length int) []byte {
	if length <= 22 {
		return append(out, byte(length-8))
	}
	out = append(out, 0x0F)
	rem := length - 23
	if rem <= 254 {
		return append(out, byte(rem))
	}
	out = append(out, 0xFF)
	rem -= 255
	for rem >= 0xFFFF {
		out = append(out, 0xFF, 0xFF)
		rem -= 0xFFFF
	}
	return append(out, byte(rem), byte(rem>>8))
}

// r21WriteMatch 写一条回溯匹配指令，nextLit（0..7）编码进指令尾字节低 3 位，
// 作为其后字面量长度的低位（0 时由解压端读独立长度 opcode 或继续匹配链；
// 链内高 nibble 15 的字节被解压器保留为 case 0 长匹配入口，故紧凑格式
// 长度上限为 14，保证任何指令 opcode 都落在 0x30-0xEF 的安全区）：
//   - 紧凑格式：长 ≤14 且距 ≤0x200，高 nibble 为长度，9 位偏移（距离-1）；
//   - 0x10-0x1F：长 ≤18 且距 ≤0x2000，13 位偏移（距离-1）；
//   - 0x20-0x27：16 位直接偏移，长 ≤255；
//   - 0x28-0x2F：16 位偏移（距离-1），长度 = 0x100 + 三段拼接。
func r21WriteMatch(out []byte, dist, length, nextLit int) []byte {
	switch {
	case length <= 14 && dist <= 0x200:
		off := dist - 1
		return append(out, byte(length<<4|off&0x0F), byte((off>>4)<<3)|byte(nextLit))
	case length <= 18 && dist <= 0x2000:
		off := dist - 1
		return append(out, byte(0x10|(length-3)), byte(off), byte((off>>8)<<3)|byte(nextLit))
	case length <= 255 && dist <= 0xFFFF:
		return append(out, byte(0x20|length&0x07), byte(dist), byte(dist>>8), byte(length&0xF8)|byte(nextLit))
	default:
		off := dist - 1
		lm := length - 0x100
		return append(out, byte(0x28|length&0x07), byte(off), byte(off>>8),
			byte(lm>>3&0xFF), byte((lm>>8)&0xF8)|byte(nextLit))
	}
}

// r21AppendLiteral 按解压端块重排怪癖写入字面量：cp16 为两个 8 字节半块
// 交换，级联后每 32 字节块呈 4 个 8 字节半块完全逆序；余数块按
// r21ChunkOrder 置换。
func r21AppendLiteral(out []byte, lit []byte) []byte {
	i := 0
	for ; len(lit)-i >= 32; i += 32 {
		out = append(out, lit[i+24:i+32]...)
		out = append(out, lit[i+16:i+24]...)
		out = append(out, lit[i+8:i+16]...)
		out = append(out, lit[i:i+8]...)
	}
	r := len(lit) - i
	for k := 0; k < r; k++ {
		out = append(out, lit[i+r21ChunkOrder[r][k]])
	}
	return out
}
