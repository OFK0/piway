// Package tsdb stores time series samples in append-only segment files.
//
// A store is a directory of numbered segments. Samples are only ever appended
// to the newest (active) segment. When it reaches Options.SegmentBytes or
// spans Options.SegmentSpan, it is sealed and never written again; retention
// can later delete whole sealed segments.
//
// # File format
//
// All integers are little-endian; uvarint and varint are the encoding/binary
// variable-length forms.
//
//	segment = header frame* [trailer]
//	header  = "PWSG" version:u8 0 0 0                        8 bytes
//	frame   = length:u32 crc32c(payload):u32 payload
//	payload = kind:u8 body
//	  series  (1): count:uvarint {id:uvarint len:uvarint name}
//	  samples (2): t:varint count:uvarint {id:uvarint value:f64}
//	trailer = minT:i64 maxT:i64 crc32c(minT maxT):u32 "PWSE"  24 bytes
//
// One Append writes one samples record, preceded by a series record for
// names the segment has not seen yet, in a single write call. Series ids are
// local to a segment, so every segment can be read on its own.
//
// # Crash safety
//
// A sealed segment has a trailer and has been fsynced. A segment without a
// trailer was active when the process died. Open scans it, truncates it after
// its last intact frame (a torn write fails its length or checksum check) and
// seals it, so a crash loses at most the record being written and never
// touches earlier segments.
//
// Appends are not fsynced: after kill -9 the kernel still writes them out,
// but a power cut can lose the active segment's recent records. The SD card
// sees one fsync per sealed segment instead of one per sample.
package tsdb

import (
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"time"
)

// MaxSeriesLen is the longest series name Append accepts, in bytes.
const MaxSeriesLen = 256

const maxPoints = 1 << 16

// ErrClosed is returned by operations on a closed Store.
var ErrClosed = errors.New("tsdb: store is closed")

// Options configures a Store. Zero fields take their defaults.
type Options struct {
	// SegmentBytes is the size after which the active segment is sealed.
	// Default 16 MiB.
	SegmentBytes int64
	// SegmentSpan is the longest time range one segment covers. It is the
	// granularity at which retention can delete data. Default 1h.
	SegmentSpan time.Duration
	// Logger receives warnings about data dropped while recovering from a
	// crash. Default: discard.
	Logger *slog.Logger
}

// Point is one value of one series.
type Point struct {
	Series string
	Value  float64
}

// Sample is one stored value. T is in milliseconds since the Unix epoch.
type Sample struct {
	T int64
	V float64
}

// Store is an append-only time series store in one directory. It is safe for
// concurrent use. Only one Store may have a directory open at a time.
type Store struct {
	dir  string
	opts Options
	lock *os.File

	mu       sync.Mutex
	closed   bool
	segments []segmentInfo // not active, oldest first
	active   *activeSegment
	nextSeq  uint64
	buf      []byte
}

// activeSegment is the segment being appended to. It exists only once its
// first record has been written, so dataEnd > 0 and minT/maxT are valid.
type activeSegment struct {
	segmentInfo
	f   *os.File
	ids map[string]uint64
}

// Open opens the store in dir, creating the directory if needed, and
// recovers any segment left unsealed by a crash. Appends after Open go to a
// new segment.
func Open(dir string, opts Options) (*Store, error) {
	if opts.SegmentBytes <= 0 {
		opts.SegmentBytes = 16 << 20
	}
	if opts.SegmentSpan <= 0 {
		opts.SegmentSpan = time.Hour
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("tsdb: %w", err)
	}
	lock, err := lockDir(dir)
	if err != nil {
		return nil, err
	}

	s := &Store{dir: dir, opts: opts, lock: lock}
	if err := s.load(); err != nil {
		lock.Close()
		return nil, fmt.Errorf("tsdb: %w", err)
	}
	return s, nil
}

func (s *Store) load() error {
	paths, last, err := segmentPaths(s.dir)
	if err != nil {
		return err
	}
	for _, path := range paths {
		seg, ok, err := loadSegment(path, s.opts.Logger)
		if err != nil {
			return err
		}
		if ok {
			s.segments = append(s.segments, seg)
		}
	}
	s.nextSeq = last + 1
	return syncDir(s.dir)
}

// lockDir takes an exclusive flock on dir/LOCK. The kernel releases it when
// the process exits, so a crash never leaves a stale lock.
func lockDir(dir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, "LOCK"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("tsdb: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("tsdb: %s is already open", dir)
		}
		return nil, fmt.Errorf("tsdb: lock %s: %w", dir, err)
	}
	return f, nil
}

// Append stores points as samples at time t, in milliseconds since the Unix
// epoch. The points are written as one record, so after a crash either all
// of them are recovered or none are.
func (s *Store) Append(t int64, points []Point) error {
	if len(points) == 0 {
		return nil
	}
	if len(points) > maxPoints {
		return fmt.Errorf("tsdb: %d points in one append, max %d", len(points), maxPoints)
	}
	for _, p := range points {
		if p.Series == "" || len(p.Series) > MaxSeriesLen {
			return fmt.Errorf("tsdb: series name must be 1-%d bytes, got %d", MaxSeriesLen, len(p.Series))
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if s.active != nil && s.full(t) {
		if err := s.seal(); err != nil {
			return err
		}
	}
	if s.active == nil {
		if err := s.create(); err != nil {
			return err
		}
	}
	return s.write(t, points)
}

// full reports whether a sample at t must go to a new segment.
func (s *Store) full(t int64) bool {
	a := s.active
	return a.dataEnd >= s.opts.SegmentBytes ||
		max(a.maxT, t)-min(a.minT, t) >= s.opts.SegmentSpan.Milliseconds()
}

func (s *Store) create() error {
	path := filepath.Join(s.dir, segmentName(s.nextSeq))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("tsdb: %w", err)
	}
	s.nextSeq++
	s.active = &activeSegment{segmentInfo: segmentInfo{path: path}, f: f, ids: make(map[string]uint64)}
	return nil
}

func (s *Store) write(t int64, points []Point) error {
	a := s.active
	b := s.buf[:0]
	if a.dataEnd == 0 {
		b = appendHeader(b)
	}

	// New ids are assigned before the write; if it fails, the segment is
	// abandoned along with them.
	var added []string
	for _, p := range points {
		if _, ok := a.ids[p.Series]; !ok {
			a.ids[p.Series] = uint64(len(a.ids))
			added = append(added, p.Series)
		}
	}
	var start int
	if len(added) > 0 {
		b, start = beginFrame(b)
		b = append(b, kindSeries)
		b = binary.AppendUvarint(b, uint64(len(added)))
		for _, name := range added {
			b = binary.AppendUvarint(b, a.ids[name])
			b = binary.AppendUvarint(b, uint64(len(name)))
			b = append(b, name...)
		}
		b = endFrame(b, start)
	}
	b, start = beginFrame(b)
	b = append(b, kindSamples)
	b = binary.AppendVarint(b, t)
	b = binary.AppendUvarint(b, uint64(len(points)))
	for _, p := range points {
		b = binary.AppendUvarint(b, a.ids[p.Series])
		b = binary.LittleEndian.AppendUint64(b, math.Float64bits(p.Value))
	}
	b = endFrame(b, start)
	s.buf = b

	if _, err := a.f.Write(b); err != nil {
		s.abandon()
		return fmt.Errorf("tsdb: %w", err)
	}
	if a.dataEnd == 0 {
		a.minT, a.maxT = t, t
	}
	a.dataEnd += int64(len(b))
	a.minT, a.maxT = min(a.minT, t), max(a.maxT, t)
	return nil
}

// seal writes the active segment's trailer and fsyncs it. Even if that
// fails, the segment stops being active: readers still see its records up
// to dataEnd, and the next Open repairs it.
func (s *Store) seal() error {
	a := s.active
	s.active = nil
	s.segments = append(s.segments, a.segmentInfo)

	_, err := a.f.Write(appendTrailer(s.buf[:0], a.minT, a.maxT))
	if err == nil {
		err = a.f.Sync()
	}
	err = errors.Join(err, a.f.Close())
	if err == nil {
		err = syncDir(s.dir)
	}
	if err != nil {
		return fmt.Errorf("tsdb: seal %s: %w", a.path, err)
	}
	return nil
}

// abandon gives up on the active segment after a failed write, which may
// have left part of a record on disk. The next append starts a new segment.
func (s *Store) abandon() {
	a := s.active
	s.active = nil
	if a.dataEnd == 0 {
		a.f.Close()
		os.Remove(a.path)
		return
	}
	a.f.Truncate(a.dataEnd) // Best effort; readers stop at dataEnd anyway.
	a.f.Close()
	s.segments = append(s.segments, a.segmentInfo)
}

// Select returns the samples of series with mint <= T <= maxt, sorted by
// time. Samples with equal T keep their append order.
func (s *Store) Select(series string, mint, maxt int64) ([]Sample, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	segs := slices.Clone(s.segments)
	if s.active != nil {
		segs = append(segs, s.active.segmentInfo)
	}
	s.mu.Unlock()

	var out []Sample
	for _, seg := range segs {
		if seg.maxT < mint || seg.minT > maxt {
			continue
		}
		err := readSegment(seg, series, func(t int64, v float64) {
			if t >= mint && t <= maxt {
				out = append(out, Sample{T: t, V: v})
			}
		})
		if err != nil {
			return nil, fmt.Errorf("tsdb: %w", err)
		}
	}
	slices.SortStableFunc(out, func(a, b Sample) int { return cmp.Compare(a.T, b.T) })
	return out, nil
}

// Close seals the active segment and releases the directory.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var err error
	if s.active != nil {
		err = s.seal()
	}
	return errors.Join(err, s.lock.Close())
}
