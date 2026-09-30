// classes.go 实现 AcDb:Classes 动态类名表解析。
// R2010+ 布局：数值条目（类号/代理标志/本体类 id 等）在主流，类名字符串
// （appName/cppName/dxfName ×N）集中在段尾的 UTF-16 字符串流；流的位置由
// 段尾 16 字节长度字段描述。由于各版本写入端存在布局抖动，此处对段头
// 采用「候选布局逐个探测」的定位策略，失败时返回空表（基础图元类型码
// <500，不依赖该表，解析失败不影响渲染）。
package cad

import (
	"fmt"
	"github.com/unitedrhino/go-cad/internal/bitstream"
	"os"
)

// classEntry 类表条目。
type classEntry struct {
	classNumber uint16
	itemClassID uint16 // 0x1F2=实体类 0x1F3=对象类
	dxfName     string
}

const stringStreamMetaBits = 16 * 8

// parseClassesSection 解析类段，返回 类型码→DXF 名称 映射。
func parseClassesSection(data []byte, ver dwgVersion) (map[uint16]string, error) {
	classes, err := parseClassRecords(data, ver)
	if err != nil {
		return nil, err
	}
	out := make(map[uint16]string, len(classes))
	for _, c := range classes {
		if c.dxfName != "" {
			out[c.classNumber] = c.dxfName
		}
	}
	return out, nil
}

// parseClassRecords 解析类段条目（数值 + 名称）。
// ver < R2007（R2004 压缩段）时类名 3×TV 内联在主流条目之间
// （LibreDWG decode.c else 分支），直接读出填充 dxfName；
// R2007+ 类名在独立字符串流，数值区连续，由 fillClassNamesFromStream
// 事后填充。
func parseClassRecords(data []byte, ver dwgVersion) ([]classEntry, error) {
	r := bitstream.NewBitStream(data)
	before, err := r.ReadRCS(len(sentinelClassesBefore))
	if err != nil {
		return nil, err
	}
	if !bytesEqual(before, sentinelClassesBefore[:]) {
		return nil, errClassesSentinel
	}
	if _, err := r.ReadRL(); err != nil { // size（低 32 位）
		return nil, err
	}
	maxClassNumber, err := locateClassHeader(r)
	if err != nil {
		return nil, err
	}
	// 三种布局公共尾部：RC zero1 + B bit flag
	if _, err := r.ReadRC(); err != nil {
		return nil, err
	}
	if _, err := r.ReadB(); err != nil {
		return nil, err
	}

	dbg := os.Getenv("CAD_CLASSES_DBG") != ""
	var classes []classEntry
	for {
		if r.Pos > len(data) {
			return nil, errClassesTruncated
		}
		entry, err := readClassNumericEntry(r, ver)
		if err != nil {
			return nil, err
		}
		classes = append(classes, entry)
		if dbg && len(classes) <= 2 {
			fmt.Fprintf(os.Stderr, "[cls] 条目%d: class=%d itemID=%d @%d\n", len(classes), entry.classNumber, entry.itemClassID, r.TellBits())
		}
		if entry.classNumber == maxClassNumber {
			break
		}
	}

	if ver >= verR2007 {
		fillClassNamesFromStream(data, r, classes)
	}
	return classes, nil
}

// classHeaderProbe 段头布局探测：在读取器副本上按某版本布局推进，
// 成功时给出 maxNum 合理值。探测失败不留任何游标副作用。
type classHeaderProbe struct {
	name string
	run  func(rr *bitstream.BitStream) (uint16, bool)
}

// probeR2010Header R2010+/R2018 布局：RL hsize + RL bitsize + BS maxNum。
func probeR2010Header(rr *bitstream.BitStream) (uint16, bool) {
	if _, e := rr.ReadRL(); e != nil {
		return 0, false
	}
	if _, e := rr.ReadRL(); e != nil {
		return 0, false
	}
	return readClassMax(rr)
}

// probeR2007Header R2007+ 布局：RL bitsize + BS maxNum。
func probeR2007Header(rr *bitstream.BitStream) (uint16, bool) {
	if _, e := rr.ReadRL(); e != nil {
		return 0, false
	}
	return readClassMax(rr)
}

// probeR2004Header R2004 布局：直接 BS maxNum + RC zero==0。
func probeR2004Header(rr *bitstream.BitStream) (uint16, bool) {
	return readClassMax(rr)
}

// classHeaderProbes 段头候选布局（新→旧排列，头部随版本有三种布局，
// 对照 LibreDWG decode.c 类段逻辑；以 maxNum 合理（≥100 且 ≤65535）且
// zero0==0 探测采用哪种）。
var classHeaderProbes = []classHeaderProbe{
	{"R2010+", probeR2010Header},
	{"R2007", probeR2007Header},
	{"R2004", probeR2004Header},
}

// locateClassHeader 在当前游标处探测采用哪种段头布局：按候选顺序在副本
// 上试读，命中者把主流游标推进到该布局结束位。
func locateClassHeader(r *bitstream.BitStream) (uint16, error) {
	dbg := os.Getenv("CAD_CLASSES_DBG") != ""
	for _, probe := range classHeaderProbes {
		rr := *r
		if maxNum, ok := probe.run(&rr); ok {
			r.Restore(rr.Pos, rr.Sub)
			if dbg {
				fmt.Fprintf(os.Stderr, "[cls] 分支命中 max=%d @%d\n", maxNum, r.TellBits())
			}
			return maxNum, nil
		}
	}
	return 0, fmt.Errorf("cad: 类段头解析失败（max_num 异常）")
}

// readClassMax 试读 BS maxNum + RC zero==0 的头部布局尾部。
func readClassMax(rr *bitstream.BitStream) (uint16, bool) {
	maxNum, err := rr.ReadBS()
	if err != nil || maxNum < 100 {
		return 0, false
	}
	z0, e := rr.ReadRC()
	if e != nil || z0 != 0 {
		return 0, false
	}
	return maxNum, true
}

// readClassNumericEntry 读取单个类条目的数值区（及 R2004- 的内联名称）：
// BS classNumber + BS proxyFlags + [R2004-: TV app/cpp/dxf] + B zombie +
// BS itemClassID + BL numInstances + BS dwgVersion + BS maintVersion +
// BL unknown + BL unknown（尾部五字段对照 LibreDWG decode.c 类条目）。
func readClassNumericEntry(r *bitstream.BitStream, ver dwgVersion) (classEntry, error) {
	var entry classEntry
	classNumber, err := r.ReadBS()
	if err != nil {
		return entry, err
	}
	if _, err := r.ReadBS(); err != nil { // proxy flags
		return entry, err
	}
	if ver < verR2007 {
		// R2004-：appname/cppname/dxfname 3×TV 内联（长度含尾部 \0）
		for i := 0; i < 3; i++ {
			s, e := r.ReadTV(0)
			if e != nil {
				return entry, e
			}
			if i == 2 {
				entry.dxfName = s
			}
		}
	}
	if _, err := r.ReadB(); err != nil { // zombie
		return entry, err
	}
	itemClassID, err := r.ReadBS()
	if err != nil {
		return entry, err
	}
	for i := 0; i < 5; i++ {
		var e error
		if i < 1 || i >= 3 {
			_, e = r.ReadBL() // num_instances + unknown×2
		} else {
			_, e = r.ReadBS() // dwg_version + maint_version
		}
		if e != nil {
			return entry, e
		}
	}
	entry.classNumber = classNumber
	entry.itemClassID = itemClassID
	return entry, nil
}

// fillClassNamesFromStream 尝试为主流数值条目填充类名：在若干候选起点
// （游标当前位置 / 段尾长度字段推算位置）各读一遍 3×TU 名称序列，
// 逐候选累加类名可读性评分，取最优候选回填。
func fillClassNamesFromStream(data []byte, r *bitstream.BitStream, classes []classEntry) {
	if len(classes) == 0 {
		return
	}
	startCandidates := []uint64{r.TellBits()}
	if base, ok := stringStreamBase(r, data); ok {
		startCandidates = append(startCandidates, base)
	}
	bestScore := -1 << 30
	for _, start := range startCandidates {
		names, ok := readClassNamesAt(data, start, len(classes))
		if !ok {
			continue
		}
		score := 0
		for _, n := range names {
			score += classNameScore(n)
		}
		if score > bestScore {
			bestScore = score
			for i := range classes {
				classes[i].dxfName = names[i]
			}
		}
	}
}

// readClassNamesAt 从指定位起点连续读 count 组 3×TU（app/cpp/dxf），
// 任一读取失败即整组作废。
func readClassNamesAt(data []byte, startBit uint64, count int) ([]string, bool) {
	probe := bitstream.NewBitStream(data)
	probe.SetBitPos(startBit)
	names := make([]string, count)
	for i := 0; i < count; i++ {
		for j := 0; j < 3; j++ {
			s, err := probe.ReadTU()
			if err != nil {
				return nil, false
			}
			if j == 2 {
				names[i] = s
			}
		}
	}
	return names, true
}

// stringStreamBase 从段尾的 16 字节长度字段推算字符串流起点：
// 末尾倒数第 1 位为 present 标志；尾部 16 字节为流大小（位单位），
// 最高位标志扩展高 15 位（此时再往前取一个 RS 拼接）。
func stringStreamBase(r *bitstream.BitStream, data []byte) (uint64, bool) {
	totalBits := uint64(len(data)) * 8
	if totalBits <= stringStreamMetaBits+1 {
		return 0, false
	}
	present := bitstream.NewBitStream(data)
	present.SetBitPos(totalBits - 1)
	if flag, err := present.ReadB(); err != nil || flag == 0 {
		return 0, false
	}
	sizeFieldStart := totalBits - stringStreamMetaBits
	sr := bitstream.NewBitStream(data)
	sr.SetBitPos(sizeFieldStart)
	low, err := sr.ReadRS()
	if err != nil {
		return 0, false
	}
	streamSizeBits := uint64(low)
	if low&0x8000 != 0 {
		if sizeFieldStart < stringStreamMetaBits {
			return 0, false
		}
		sizeFieldStart -= stringStreamMetaBits
		sr.SetBitPos(sizeFieldStart)
		high, err := sr.ReadRS()
		if err != nil {
			return 0, false
		}
		streamSizeBits = uint64(low&0x7FFF) | uint64(high)<<15
	}
	if sizeFieldStart < streamSizeBits {
		return 0, false
	}
	return sizeFieldStart - streamSizeBits, true
}

// classNameScore 类名可读性评分（大写/数字为正、下划线等符号次之、
// 小写再次、控制字符重罚——真实类名几乎全为大写标识符）。
func classNameScore(name string) int {
	if name == "" {
		return -16
	}
	score := 0
	for _, ch := range name {
		switch {
		case ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
			score += 4
		case ch == '_', ch == '$', ch == '*':
			score += 2
		case ch >= 'a' && ch <= 'z':
			score++
		case ch < 0x20:
			score -= 12
		default:
			score -= 4
		}
	}
	return score
}

// errClasses* 类段解析错误。
var (
	errClassesSentinel  = fmtError("cad: AcDb:Classes 前哨兵不匹配")
	errClassesTruncated = fmtError("cad: AcDb:Classes 条目截断")
)

// fmtError 简单错误构造（避免在包顶层重复 import errors）。
func fmtError(msg string) error { return &simpleError{msg} }

type simpleError struct{ msg string }

func (e *simpleError) Error() string { return e.msg }

// bytesEqual 字节序列相等比较。
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// parseClassesSectionR13R15 R2000 类段：名字以 TV 直接在主流，
// 条目循环以 RL 声明的数据长度为界。
func parseClassesSectionR13R15(data []byte) (map[uint16]string, error) {
	r := bitstream.NewBitStream(data)
	before, err := r.ReadRCS(len(sentinelClassesBefore))
	if err != nil {
		return nil, err
	}
	if !bytesEqual(before, sentinelClassesBefore[:]) {
		return nil, errClassesSentinel
	}
	dataSize, err := r.ReadRL()
	if err != nil {
		return nil, err
	}
	endBit := r.TellBits() + uint64(dataSize)*8
	out := map[uint16]string{}
	for r.TellBits() < endBit {
		classNumber, err := r.ReadBS()
		if err != nil {
			break
		}
		if _, err := r.ReadBS(); err != nil { // proxy flags / version
			break
		}
		if _, err := r.ReadTV(30); err != nil { // app name
			break
		}
		if _, err := r.ReadTV(30); err != nil { // cpp name
			break
		}
		dxfName, err := r.ReadTV(30)
		if err != nil {
			break
		}
		if _, err := r.ReadB(); err != nil { // zombie
			break
		}
		if _, err := r.ReadBS(); err != nil { // item class id
			break
		}
		if dxfName != "" {
			out[classNumber] = dxfName
		}
	}
	return out, nil
}
