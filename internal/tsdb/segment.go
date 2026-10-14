package tsdb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
)

const (
	headerSize      = 8
	frameHeaderSize = 8
	trailerSize     = 24
	formatVersion   = 1

	kindSeries  = 1
	kindSamples = 2
)

var (
	headerMagic  = [4]byte{'P', 'W', 'S', 'G'}
	trailerMagic = [4]byte{'P', 'W', 'S', 'E'}
	castagnoli   = crc32.MakeTable(crc32.Castagnoli)

	errCorrupt = errors.New("corrupt record")
)

// segmentInfo describes a segment file. Readers only look at its first
// dataEnd bytes, so a trailer or a torn write after them is never parsed as
// records.
type segmentInfo struct {
	path       string
	dataEnd    int64
	minT, maxT int64
}

func segmentName(seq uint64) string { return fmt.Sprintf("%08d.seg", seq) }

func appendHeader(b []byte) []byte {
	b = append(b, headerMagic[:]...)
	return append(b, formatVersion, 0, 0, 0)
}

func checkHeader(b []byte) error {
	if len(b) < headerSize || [4]byte(b[:4]) != headerMagic {
		return errors.New("not a segment file")
	}
	if b[4] != formatVersion {
		return fmt.Errorf("unsupported segment version %d", b[4])
	}
	return nil
}

// beginFrame reserves room for a frame header; endFrame fills it in once the
// payload has been appended after it.
func beginFrame(b []byte) ([]byte, int) {
	return append(b, 0, 0, 0, 0, 0, 0, 0, 0), len(b)
}

func endFrame(b []byte, start int) []byte {
	p := b[start+frameHeaderSize:]
	binary.LittleEndian.PutUint32(b[start:], uint32(len(p)))
	binary.LittleEndian.PutUint32(b[start+4:], crc32.Checksum(p, castagnoli))
	return b
}

// walkFrames calls fn with the payload of each intact frame in b and returns
// how many bytes of b those frames cover. It stops at the first frame that is
// truncated or fails its checksum, or for which fn returns an error.
func walkFrames(b []byte, fn func(payload []byte) error) int {
	off := 0
	for len(b)-off >= frameHeaderSize {
		n := uint64(binary.LittleEndian.Uint32(b[off:]))
		sum := binary.LittleEndian.Uint32(b[off+4:])
		if n == 0 || n > uint64(len(b)-off-frameHeaderSize) {
			break
		}
		p := b[off+frameHeaderSize : off+frameHeaderSize+int(n)]
		if crc32.Checksum(p, castagnoli) != sum || fn(p) != nil {
			break
		}
		off += frameHeaderSize + int(n)
	}
	return off
}

func appendTrailer(b []byte, minT, maxT int64) []byte {
	start := len(b)
	b = binary.LittleEndian.AppendUint64(b, uint64(minT))
	b = binary.LittleEndian.AppendUint64(b, uint64(maxT))
	b = binary.LittleEndian.AppendUint32(b, crc32.Checksum(b[start:], castagnoli))
	return append(b, trailerMagic[:]...)
}

func parseTrailer(b []byte) (minT, maxT int64, ok bool) {
	if len(b) != trailerSize || [4]byte(b[20:]) != trailerMagic ||
		crc32.Checksum(b[:16], castagnoli) != binary.LittleEndian.Uint32(b[16:]) {
		return 0, 0, false
	}
	return int64(binary.LittleEndian.Uint64(b)), int64(binary.LittleEndian.Uint64(b[8:])), true
}

// decoder decodes the records of one segment in order. Series ids are only
// valid within the segment that defines them.
type decoder struct {
	names map[uint64]string
}

// record decodes one frame payload. A series record defines ids; a samples
// record calls fn for each sample. On error, fn may already have been called
// for part of the record.
func (d *decoder) record(p []byte, fn func(t int64, id uint64, v float64)) error {
	if len(p) == 0 {
		return errCorrupt
	}
	r := reader{b: p[1:]}
	switch p[0] {
	case kindSeries:
		n := r.uvarint()
		for i := uint64(0); i < n && r.err == nil; i++ {
			id := r.uvarint()
			name := r.bytes(r.uvarint())
			if r.err == nil {
				if d.names == nil {
					d.names = make(map[uint64]string)
				}
				d.names[id] = string(name)
			}
		}
	case kindSamples:
		t := r.varint()
		n := r.uvarint()
		for i := uint64(0); i < n && r.err == nil; i++ {
			id := r.uvarint()
			v := math.Float64frombits(r.uint64())
			if r.err != nil {
				break
			}
			if _, ok := d.names[id]; !ok {
				return errCorrupt
			}
			fn(t, id, v)
		}
	default:
		return errCorrupt
	}
	if r.err == nil && len(r.b) != 0 {
		return errCorrupt
	}
	return r.err
}

// reader consumes a record body; after the first error every read returns
// zero and err stays set.
type reader struct {
	b   []byte
	err error
}

func (r *reader) uvarint() uint64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Uvarint(r.b)
	if n <= 0 {
		r.err = errCorrupt
		return 0
	}
	r.b = r.b[n:]
	return v
}

func (r *reader) varint() int64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Varint(r.b)
	if n <= 0 {
		r.err = errCorrupt
		return 0
	}
	r.b = r.b[n:]
	return v
}

func (r *reader) uint64() uint64 {
	b := r.bytes(8)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint64(b)
}

func (r *reader) bytes(n uint64) []byte {
	if r.err != nil {
		return nil
	}
	if n > uint64(len(r.b)) {
		r.err = errCorrupt
		return nil
	}
	b := r.b[:n]
	r.b = r.b[n:]
	return b
}

// loadSegment opens an existing segment.
//
// A sealed segment is trusted as is: only its trailer is read, so opening the
// store does not read every file. A segment without a trailer was being
// written when the process died. It is scanned, truncated after its last
// intact record and sealed. If no samples survive, the file is removed and ok
// is false.
func loadSegment(path string, log *slog.Logger) (seg segmentInfo, ok bool, err error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return seg, false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return seg, false, err
	}
	size := st.Size()
	if size >= headerSize+trailerSize {
		var tail [trailerSize]byte
		if _, err := f.ReadAt(tail[:], size-trailerSize); err != nil {
			return seg, false, err
		}
		if minT, maxT, ok := parseTrailer(tail[:]); ok {
			return segmentInfo{path: path, dataEnd: size - trailerSize, minT: minT, maxT: maxT}, true, nil
		}
	}

	data, err := io.ReadAll(f)
	if err != nil {
		return seg, false, err
	}
	if len(data) < headerSize {
		// Torn while the first record was being written.
		log.Warn("tsdb: removing empty segment", "path", path, "bytes", len(data))
		return seg, false, os.Remove(path)
	}
	if err := checkHeader(data); err != nil {
		return seg, false, fmt.Errorf("%s: %w", path, err)
	}

	var d decoder
	minT, maxT, records := int64(math.MaxInt64), int64(math.MinInt64), 0
	end := headerSize + walkFrames(data[headerSize:], func(p []byte) error {
		lo, hi := minT, maxT
		err := d.record(p, func(t int64, _ uint64, _ float64) {
			lo, hi = min(lo, t), max(hi, t)
		})
		if err == nil && p[0] == kindSamples {
			minT, maxT = lo, hi
			records++
		}
		return err
	})
	if records == 0 {
		log.Warn("tsdb: removing segment without samples", "path", path, "bytes", len(data))
		return seg, false, os.Remove(path)
	}
	if dropped := len(data) - end; dropped > 0 {
		log.Warn("tsdb: dropped torn tail of segment", "path", path, "bytes", dropped)
	}

	if err := f.Truncate(int64(end)); err != nil {
		return seg, false, err
	}
	if _, err := f.WriteAt(appendTrailer(nil, minT, maxT), int64(end)); err != nil {
		return seg, false, err
	}
	if err := f.Sync(); err != nil {
		return seg, false, err
	}
	return segmentInfo{path: path, dataEnd: int64(end), minT: minT, maxT: maxT}, true, nil
}

// readSegment calls fn for every sample of series in seg, whose bytes are
// the file's first dataEnd bytes followed by tail (records not yet written).
func readSegment(seg segmentInfo, tail []byte, series string, fn func(t int64, v float64)) error {
	f, err := os.Open(seg.path)
	if err != nil {
		return err
	}
	defer f.Close()
	data := make([]byte, seg.dataEnd, seg.dataEnd+int64(len(tail)))
	if _, err := f.ReadAt(data, 0); err != nil && seg.dataEnd > 0 {
		return err
	}
	data = append(data, tail...)
	if err := checkHeader(data); err != nil {
		return fmt.Errorf("%s: %w", seg.path, err)
	}

	var d decoder
	end := headerSize + walkFrames(data[headerSize:], func(p []byte) error {
		return d.record(p, func(t int64, id uint64, v float64) {
			if d.names[id] == series {
				fn(t, v)
			}
		})
	})
	if end != len(data) {
		return fmt.Errorf("%s: %w at offset %d", seg.path, errCorrupt, end)
	}
	return nil
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// segmentPaths returns the segment files in dir with their sequence numbers,
// oldest first. Files that are not named like segments are ignored.
func segmentPaths(dir string) ([]string, uint64, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.seg"))
	if err != nil {
		return nil, 0, err
	}
	var paths []string
	var last uint64
	for _, m := range matches { // Glob sorts, and names are zero-padded.
		var seq uint64
		if _, err := fmt.Sscanf(filepath.Base(m), "%08d.seg", &seq); err != nil || segmentName(seq) != filepath.Base(m) {
			continue
		}
		paths = append(paths, m)
		last = seq
	}
	return paths, last, nil
}
