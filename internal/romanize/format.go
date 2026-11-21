package romanize

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"unsafe"

	"github.com/ikawaha/kagome-dict/dict"
	"github.com/ikawaha/kagome-dict/ipa"
)

// Binary layout of ipa.mmap: a 256-byte little-endian fileHeader followed by
// 8-byte-aligned flat tables mapped directly via unsafe.Slice.
//
// Only the tables needed for Normal-mode Viterbi and pronunciation/reading
// lookup are stored; POS tables and other feature columns are omitted.
var fileMagic = [8]byte{'M', 'L', 'W', 'I', 'P', 'A', '0', '1'}

const (
	formatVersion = 1
	headerSize    = 256
)

type daNode struct {
	Base, Check int32
}

type morph struct {
	LeftID, RightID, Weight int16
}

type fileHeader struct {
	Magic   [8]byte
	Version uint32
	Flags   uint32

	DaOff    uint64
	DaLen    uint64
	DupOff   uint64
	DupLen   uint64
	MorphOff uint64
	MorphLen uint64

	ConnOff uint64
	ConnRow uint32
	ConnCol uint32

	CharCatOff uint64
	CharCatLen uint64
	InvokeOff  uint64
	GroupOff   uint64
	ClassLen   uint64

	UnkIdxOff   uint64
	UnkDupOff   uint64
	UnkMorphOff uint64
	UnkMorphLen uint64

	ReadOffOff  uint64
	ReadDataOff uint64
	ReadDataLen uint64

	UnkReadOffOff  uint64
	UnkReadDataOff uint64
	UnkReadDataLen uint64
}

// mmapDict holds read-only slices into either an OS file mapping or an
// in-memory copy built from an embedded dict.Dict.
type mmapDict struct {
	raw     []byte
	closeFn func() error

	da      []daNode
	dup     []uint16
	morphs  []morph
	conn    []int16
	connRow int

	charCat []byte
	invoke  []byte
	group   []byte

	unkIdx    []int32
	unkDup    []int32
	unkMorphs []morph

	readOff  []uint32
	readData []byte

	unkReadOff  []uint32
	unkReadData []byte
}

func align8(n uint64) uint64 {
	return (n + 7) &^ 7
}

// extractKnownReading prefers pronunciation over reading so particles such as
// は convert as wa. Empty string signals fallback to surface.
func extractKnownReading(d *dict.Dict, id int) string {
	pick := func(idx int) string {
		pos := d.POSTable.POSs[id]
		if idx < len(pos) {
			pid := pos[idx]
			if int(pid) < len(d.POSTable.NameList) {
				v := d.POSTable.NameList[pid]
				if v != "" && v != "*" {
					return v
				}
			}

			return ""
		}
		ci := idx - len(pos)
		if id >= len(d.Contents) {
			return ""
		}
		c := d.Contents[id]
		if ci >= len(c) {
			return ""
		}
		v := c[ci]
		if v == "" || v == "*" {
			return ""
		}

		return v
	}
	if v := pick(ipa.Pronunciation); v != "" {
		return v
	}

	return pick(ipa.Reading)
}

func extractUnkReading(d *dict.Dict, id int) string {
	pick := func(idx int) string {
		if id >= len(d.UnkDict.Contents) {
			return ""
		}
		c := d.UnkDict.Contents[id]
		if idx >= len(c) {
			return ""
		}
		v := c[idx]
		if v == "" || v == "*" {
			return ""
		}

		return v
	}
	if v := pick(ipa.Pronunciation); v != "" {
		return v
	}

	return pick(ipa.Reading)
}

// buildTables compiles a kagome Dict into the flat table representation shared
// by in-memory engines and the on-disk mmap writer.
func buildTables(d *dict.Dict) (*mmapDict, error) {
	if unsafe.Sizeof(daNode{}) != 8 || unsafe.Sizeof(morph{}) != 6 {
		return nil, errors.New("romanize: unexpected struct packing")
	}

	numMorphs := len(d.Morphs)
	dupSlice := make([]uint16, numMorphs)
	for k, v := range d.Index.Dup {
		if k < 0 || int(k) >= numMorphs {
			return nil, fmt.Errorf("romanize: dup key %d out of bounds", k)
		}
		if v < 0 || v > math.MaxUint16 {
			return nil, fmt.Errorf("romanize: dup value %d out of uint16 range", v)
		}
		dupSlice[k] = uint16(v)
	}

	daNodes := make([]daNode, len(d.Index.Da))
	for i, n := range d.Index.Da {
		daNodes[i] = daNode{Base: n.Base, Check: n.Check}
	}

	morphs := make([]morph, numMorphs)
	for i, m := range d.Morphs {
		morphs[i] = morph{LeftID: m.LeftID, RightID: m.RightID, Weight: m.Weight}
	}

	classLen := len(d.InvokeList)
	if len(d.GroupList) != classLen {
		return nil, fmt.Errorf("romanize: invoke/group len mismatch: %d vs %d", len(d.InvokeList), len(d.GroupList))
	}
	invokeBytes := make([]byte, classLen)
	groupBytes := make([]byte, classLen)
	unkIdx := make([]int32, classLen)
	unkDup := make([]int32, classLen)
	for i := range classLen {
		if d.InvokeList[i] {
			invokeBytes[i] = 1
		}
		if d.GroupList[i] {
			groupBytes[i] = 1
		}
		unkIdx[i] = d.UnkDict.Index[int32(i)]
		unkDup[i] = d.UnkDict.IndexDup[int32(i)]
	}
	for k := range d.UnkDict.Index {
		if k < 0 || int(k) >= classLen {
			return nil, fmt.Errorf("romanize: unk index key %d >= classLen %d", k, classLen)
		}
	}

	numUnk := len(d.UnkDict.Morphs)
	unkMorphs := make([]morph, numUnk)
	for i, m := range d.UnkDict.Morphs {
		unkMorphs[i] = morph{LeftID: m.LeftID, RightID: m.RightID, Weight: m.Weight}
	}

	readOff := make([]uint32, numMorphs+1)
	readBuf := make([]byte, 0, numMorphs*10)
	for i := range numMorphs {
		readOff[i] = uint32(len(readBuf))
		if r := extractKnownReading(d, i); r != "" {
			readBuf = append(readBuf, r...)
		}
	}
	readOff[numMorphs] = uint32(len(readBuf))

	unkReadOff := make([]uint32, numUnk+1)
	unkReadBuf := make([]byte, 0, 256)
	for i := range numUnk {
		unkReadOff[i] = uint32(len(unkReadBuf))
		if r := extractUnkReading(d, i); r != "" {
			unkReadBuf = append(unkReadBuf, r...)
		}
	}
	unkReadOff[numUnk] = uint32(len(unkReadBuf))

	return &mmapDict{
		da:          daNodes,
		dup:         dupSlice,
		morphs:      morphs,
		conn:        d.Connection.Vec,
		connRow:     int(d.Connection.Row),
		charCat:     d.CharCategory,
		invoke:      invokeBytes,
		group:       groupBytes,
		unkIdx:      unkIdx,
		unkDup:      unkDup,
		unkMorphs:   unkMorphs,
		readOff:     readOff,
		readData:    readBuf,
		unkReadOff:  unkReadOff,
		unkReadData: unkReadBuf,
	}, nil
}

// writeMmap atomically writes a kagome Dict as a flat mmap-ready file at dst.
func writeMmap(d *dict.Dict, dst string) error {
	md, err := buildTables(d)
	if err != nil {
		return err
	}

	hdr := fileHeader{
		Magic:   fileMagic,
		Version: formatVersion,
		ConnRow: uint32(d.Connection.Row),
		ConnCol: uint32(d.Connection.Col),
	}

	off := uint64(headerSize)
	place := func(n uint64) uint64 {
		off = align8(off)
		start := off
		off += n

		return start
	}

	hdr.DaLen = uint64(len(md.da))
	hdr.DaOff = place(hdr.DaLen * 8)

	hdr.DupLen = uint64(len(md.dup))
	hdr.DupOff = place(hdr.DupLen * 2)

	hdr.MorphLen = uint64(len(md.morphs))
	hdr.MorphOff = place(hdr.MorphLen * 6)

	hdr.ConnOff = place(uint64(len(md.conn)) * 2)

	hdr.CharCatLen = uint64(len(md.charCat))
	hdr.CharCatOff = place(hdr.CharCatLen)
	hdr.ClassLen = uint64(len(md.invoke))
	hdr.InvokeOff = place(hdr.ClassLen)
	hdr.GroupOff = place(hdr.ClassLen)

	hdr.UnkIdxOff = place(hdr.ClassLen * 4)
	hdr.UnkDupOff = place(hdr.ClassLen * 4)
	hdr.UnkMorphLen = uint64(len(md.unkMorphs))
	hdr.UnkMorphOff = place(hdr.UnkMorphLen * 6)

	hdr.ReadOffOff = place(uint64(len(md.readOff)) * 4)
	hdr.ReadDataLen = uint64(len(md.readData))
	hdr.ReadDataOff = place(hdr.ReadDataLen)

	hdr.UnkReadOffOff = place(uint64(len(md.unkReadOff)) * 4)
	hdr.UnkReadDataLen = uint64(len(md.unkReadData))
	hdr.UnkReadDataOff = place(hdr.UnkReadDataLen)

	totalSize := align8(off)

	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("romanize: cache dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(dst)+".tmp*")
	if err != nil {
		return fmt.Errorf("romanize: cache file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	w := bufio.NewWriterSize(tmp, 256*1024)
	var written uint64

	padTo := func(target uint64) error {
		if target < written {
			return fmt.Errorf("romanize: offset regression %d < %d", target, written)
		}
		var zeros [headerSize]byte
		for written < target {
			step := min(uint64(len(zeros)), target-written)
			n, err := w.Write(zeros[:step])
			written += uint64(n)
			if err != nil {
				return err
			}
		}

		return nil
	}
	writeVal := func(v any) error {
		sz := binary.Size(v)
		if err := binary.Write(w, binary.LittleEndian, v); err != nil {
			return err
		}
		written += uint64(sz)

		return nil
	}
	writeBytes := func(b []byte) error {
		if len(b) == 0 {
			return nil
		}
		n, err := w.Write(b)
		written += uint64(n)

		return err
	}

	writeSection := func(target uint64, fn func() error) error {
		if err := padTo(target); err != nil {
			return err
		}

		return fn()
	}

	if err := writeVal(hdr); err != nil {
		_ = tmp.Close()

		return err
	}
	if err := writeSection(hdr.DaOff, func() error {
		return writeBytes(unsafe.Slice((*byte)(unsafe.Pointer(unsafe.SliceData(md.da))), len(md.da)*8))
	}); err != nil {
		_ = tmp.Close()

		return err
	}
	if err := writeSection(hdr.DupOff, func() error {
		return writeBytes(unsafe.Slice((*byte)(unsafe.Pointer(unsafe.SliceData(md.dup))), len(md.dup)*2))
	}); err != nil {
		_ = tmp.Close()

		return err
	}
	if err := writeSection(hdr.MorphOff, func() error {
		return writeBytes(unsafe.Slice((*byte)(unsafe.Pointer(unsafe.SliceData(md.morphs))), len(md.morphs)*6))
	}); err != nil {
		_ = tmp.Close()

		return err
	}
	if err := writeSection(hdr.ConnOff, func() error {
		return writeBytes(unsafe.Slice((*byte)(unsafe.Pointer(unsafe.SliceData(md.conn))), len(md.conn)*2))
	}); err != nil {
		_ = tmp.Close()

		return err
	}
	if err := writeSection(hdr.CharCatOff, func() error { return writeBytes(md.charCat) }); err != nil {
		_ = tmp.Close()

		return err
	}
	if err := writeSection(hdr.InvokeOff, func() error { return writeBytes(md.invoke) }); err != nil {
		_ = tmp.Close()

		return err
	}
	if err := writeSection(hdr.GroupOff, func() error { return writeBytes(md.group) }); err != nil {
		_ = tmp.Close()

		return err
	}
	if err := writeSection(hdr.UnkIdxOff, func() error { return writeVal(md.unkIdx) }); err != nil {
		_ = tmp.Close()

		return err
	}
	if err := writeSection(hdr.UnkDupOff, func() error { return writeVal(md.unkDup) }); err != nil {
		_ = tmp.Close()

		return err
	}
	if err := writeSection(hdr.UnkMorphOff, func() error {
		if len(md.unkMorphs) == 0 {
			return nil
		}

		return writeBytes(unsafe.Slice((*byte)(unsafe.Pointer(unsafe.SliceData(md.unkMorphs))), len(md.unkMorphs)*6))
	}); err != nil {
		_ = tmp.Close()

		return err
	}
	if err := writeSection(hdr.ReadOffOff, func() error {
		return writeBytes(unsafe.Slice((*byte)(unsafe.Pointer(unsafe.SliceData(md.readOff))), len(md.readOff)*4))
	}); err != nil {
		_ = tmp.Close()

		return err
	}
	if err := writeSection(hdr.ReadDataOff, func() error { return writeBytes(md.readData) }); err != nil {
		_ = tmp.Close()

		return err
	}
	if err := writeSection(hdr.UnkReadOffOff, func() error { return writeVal(md.unkReadOff) }); err != nil {
		_ = tmp.Close()

		return err
	}
	if err := writeSection(hdr.UnkReadDataOff, func() error { return writeBytes(md.unkReadData) }); err != nil {
		_ = tmp.Close()

		return err
	}
	if err := padTo(totalSize); err != nil {
		_ = tmp.Close()

		return err
	}
	if err := w.Flush(); err != nil {
		_ = tmp.Close()

		return err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("romanize: cache file: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("romanize: commit cache: %w", err)
	}

	return nil
}

func sliceAt[T any](raw []byte, off, count uint64) ([]T, error) {
	if count == 0 {
		return nil, nil
	}
	var zero T
	elemSize := uint64(unsafe.Sizeof(zero))
	elemAlign := uint64(unsafe.Alignof(zero))
	if off%elemAlign != 0 {
		return nil, fmt.Errorf("romanize: section offset %d misaligned for align %d", off, elemAlign)
	}
	if count > math.MaxUint64/elemSize {
		return nil, fmt.Errorf("romanize: section size overflow (%d * %d)", count, elemSize)
	}
	end := off + count*elemSize
	if off > uint64(len(raw)) || end < off || end > uint64(len(raw)) {
		return nil, fmt.Errorf("romanize: section [%d:%d] out of file bounds (%d)", off, end, len(raw))
	}
	ptr := (*T)(unsafe.Pointer(&raw[off]))

	return unsafe.Slice(ptr, int(count)), nil
}

// openMmap maps an ipa.mmap file into memory without copying tables to the Go heap.
func openMmap(path string) (*mmapDict, error) {
	raw, closeFn, err := mmapReadOnly(path)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*mmapDict, error) {
		_ = closeFn()

		return nil, err
	}
	if len(raw) < headerSize {
		return fail(fmt.Errorf("romanize: file too small: %d", len(raw)))
	}
	var hdr fileHeader
	if _, err := binary.Decode(raw[:headerSize], binary.LittleEndian, &hdr); err != nil {
		return fail(err)
	}
	if hdr.Magic != fileMagic {
		return fail(fmt.Errorf("romanize: bad magic: %q", hdr.Magic))
	}
	if hdr.Version != formatVersion {
		return fail(fmt.Errorf("romanize: unsupported version: %d", hdr.Version))
	}
	if hdr.DaLen == 0 || hdr.CharCatLen == 0 || hdr.ClassLen == 0 {
		return fail(errors.New("romanize: empty required section"))
	}
	if hdr.DupLen != hdr.MorphLen {
		return fail(fmt.Errorf("romanize: dup/morph length mismatch: %d vs %d", hdr.DupLen, hdr.MorphLen))
	}

	d := &mmapDict{
		raw:     raw,
		closeFn: closeFn,
		connRow: int(hdr.ConnRow),
	}
	if d.da, err = sliceAt[daNode](raw, hdr.DaOff, hdr.DaLen); err != nil {
		return fail(err)
	}
	if d.dup, err = sliceAt[uint16](raw, hdr.DupOff, hdr.DupLen); err != nil {
		return fail(err)
	}
	if d.morphs, err = sliceAt[morph](raw, hdr.MorphOff, hdr.MorphLen); err != nil {
		return fail(err)
	}
	if d.conn, err = sliceAt[int16](raw, hdr.ConnOff, uint64(hdr.ConnRow)*uint64(hdr.ConnCol)); err != nil {
		return fail(err)
	}
	if d.charCat, err = sliceAt[byte](raw, hdr.CharCatOff, hdr.CharCatLen); err != nil {
		return fail(err)
	}
	if d.invoke, err = sliceAt[byte](raw, hdr.InvokeOff, hdr.ClassLen); err != nil {
		return fail(err)
	}
	if d.group, err = sliceAt[byte](raw, hdr.GroupOff, hdr.ClassLen); err != nil {
		return fail(err)
	}
	if d.unkIdx, err = sliceAt[int32](raw, hdr.UnkIdxOff, hdr.ClassLen); err != nil {
		return fail(err)
	}
	if d.unkDup, err = sliceAt[int32](raw, hdr.UnkDupOff, hdr.ClassLen); err != nil {
		return fail(err)
	}
	if d.unkMorphs, err = sliceAt[morph](raw, hdr.UnkMorphOff, hdr.UnkMorphLen); err != nil {
		return fail(err)
	}
	if d.readOff, err = sliceAt[uint32](raw, hdr.ReadOffOff, hdr.MorphLen+1); err != nil {
		return fail(err)
	}
	if d.readData, err = sliceAt[byte](raw, hdr.ReadDataOff, hdr.ReadDataLen); err != nil {
		return fail(err)
	}
	if d.unkReadOff, err = sliceAt[uint32](raw, hdr.UnkReadOffOff, hdr.UnkMorphLen+1); err != nil {
		return fail(err)
	}
	if d.unkReadData, err = sliceAt[byte](raw, hdr.UnkReadDataOff, hdr.UnkReadDataLen); err != nil {
		return fail(err)
	}

	return d, nil
}

func (d *mmapDict) close() error {
	if d == nil || d.closeFn == nil {
		return nil
	}
	fn := d.closeFn
	d.closeFn = nil
	d.raw = nil
	d.da = nil
	d.dup = nil
	d.morphs = nil
	d.conn = nil
	d.charCat = nil
	d.invoke = nil
	d.group = nil
	d.unkIdx = nil
	d.unkDup = nil
	d.unkMorphs = nil
	d.readOff = nil
	d.readData = nil
	d.unkReadOff = nil
	d.unkReadData = nil

	return fn()
}

func (d *mmapDict) charCategory(r rune) byte {
	if r >= 0 && int(r) < len(d.charCat) {
		return d.charCat[r]
	}

	return d.charCat[0]
}

func (d *mmapDict) connAt(row, col int) int16 {
	idx := d.connRow*col + row
	if idx < 0 || idx >= len(d.conn) {
		return 0
	}

	return d.conn[idx]
}

func (d *mmapDict) commonPrefixSearch(input string, cb func(id, byteLen int)) {
	var p, q int
	bufLen := len(d.da)
	for i := range len(input) {
		b := input[i]
		if b == 0 {
			return
		}
		p = q
		q = int(d.da[p].Base) + int(b)
		if q < 0 || q >= bufLen || int(d.da[q].Check) != p {
			break
		}
		ahead := int(d.da[q].Base)
		if ahead >= 0 && ahead < bufLen && int(d.da[ahead].Check) == q && d.da[ahead].Base <= 0 {
			seed := int(-d.da[ahead].Base)
			if seed < 0 || seed >= len(d.dup) {
				continue
			}
			dup := int(d.dup[seed])
			limit := min(seed+dup, len(d.morphs)-1)
			for id := seed; id <= limit; id++ {
				cb(id, i+1)
			}
		}
	}
}

func (d *mmapDict) readingOf(class nodeClass, id int, surface string) string {
	switch class {
	case classKnown:
		if id >= 0 && id+1 < len(d.readOff) {
			lo, hi := d.readOff[id], d.readOff[id+1]
			if lo < hi && int(hi) <= len(d.readData) {
				return string(d.readData[lo:hi])
			}
		}
	case classUnknown:
		if id >= 0 && id+1 < len(d.unkReadOff) {
			lo, hi := d.unkReadOff[id], d.unkReadOff[id+1]
			if lo < hi && int(hi) <= len(d.unkReadData) {
				return string(d.unkReadData[lo:hi])
			}
		}
	}

	return surface
}
