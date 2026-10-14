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
//
// # Write batching
//
// With Options.FlushInterval set, appended records are kept in memory and
// written to the file at most once per interval (or when MaxPending bytes
// are waiting). Every write that grows a file also dirties its inode, and
// on ext4 that costs a journal commit; batching turns one commit per
// sample into one per interval (docs/STORAGE.md has the measurements).
// Pending records are visible to Select. The price is durability: a crash
// of the process, not just of the machine, loses up to one interval.
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
	// FlushInterval is how long appended records may wait in memory before
	// they are written to the segment file. Zero writes every Append
	// immediately. See "Write batching" above.
	FlushInterval time.Duration
	// MaxPending flushes early once this many bytes are waiting, bounding
	// memory. Default 1 MiB.
	MaxPending int

	// now is the clock for flush decisions; tests replace it.
	now func() time.Time
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
// first record has been appended, so minT/maxT are valid. Its bytes are the
// file's first dataEnd bytes followed by pending, which is not written yet.
type activeSegment struct {
	segmentInfo
	f         *os.File
	ids       map[string]uint64
	pending   []byte
	lastFlush time.Time
}

// size is the segment's length once pending bytes are written.
func (a *activeSegment) size() int64 { return a.dataEnd + int64(len(a.pending)) }

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
	if opts.MaxPending <= 0 {
		opts.MaxPending = 1 << 20
	}
	if opts.now == nil {
		opts.now = time.Now
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
	return a.size() >= s.opts.SegmentBytes ||
		max(a.maxT, t)-min(a.minT, t) >= s.opts.SegmentSpan.Milliseconds()
}

func (s *Store) create() error {
	path := filepath.Join(s.dir, segmentName(s.nextSeq))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("tsdb: %w", err)
	}
	s.nextSeq++
	s.active = &activeSegment{
		segmentInfo: segmentInfo{path: path},
		f:           f,
		ids:         make(map[string]uint64),
		lastFlush:   s.opts.now(),
	}
	return nil
}

func (s *Store) write(t int64, points []Point) error {
	a := s.active
	empty := a.size() == 0
	b := a.pending
	if empty {
		b = appendHeader(b)
	}

	// New ids are assigned before the bytes reach the file; if writing them
	// fails, the segment is abandoned along with them.
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
	a.pending = b

	if empty {
		a.minT, a.maxT = t, t
	}
	a.minT, a.maxT = min(a.minT, t), max(a.maxT, t)

	if s.opts.FlushInterval <= 0 || len(a.pending) >= s.opts.MaxPending ||
		s.opts.now().Sub(a.lastFlush) >= s.opts.FlushInterval {
		return s.flush()
	}
	return nil
}

// flush writes the active segment's pending records to its file in one
// write call. On failure the segment is abandoned, losing them.
func (s *Store) flush() error {
	a := s.active
	if a == nil || len(a.pending) == 0 {
		return nil
	}
	if _, err := a.f.Write(a.pending); err != nil {
		s.abandon()
		return fmt.Errorf("tsdb: %w", err)
	}
	a.dataEnd += int64(len(a.pending))
	a.pending = a.pending[:0]
	a.lastFlush = s.opts.now()
	return nil
}

// Flush writes any pending records to the active segment's file now.
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	return s.flush()
}

// seal writes the active segment's trailer and fsyncs it. Even if that
// fails, the segment stops being active: readers still see its records up
// to dataEnd, and the next Open repairs it.
func (s *Store) seal() error {
	if err := s.flush(); err != nil {
		return err // flush abandoned the segment
	}
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
	a.pending = nil
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
	var out []Sample
	err := s.Scan(mint, maxt, func(name string, t int64, v float64) {
		if name == series {
			out = append(out, Sample{T: t, V: v})
		}
	})
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(out, func(a, b Sample) int { return cmp.Compare(a.T, b.T) })
	return out, nil
}

// Scan calls fn for every sample of every series with mint <= T <= maxt,
// segment by segment in append order. Reading all series in one pass is
// what downsampling needs; Select would read each segment once per series.
// fn must not call back into the Store.
func (s *Store) Scan(mint, maxt int64, fn func(series string, t int64, v float64)) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	segs := slices.Clone(s.segments)
	var tail []byte // the active segment's records not yet in its file
	hasActive := s.active != nil
	if hasActive {
		segs = append(segs, s.active.segmentInfo)
		tail = slices.Clone(s.active.pending)
	}
	s.mu.Unlock()

	for i, seg := range segs {
		if seg.maxT < mint || seg.minT > maxt {
			continue
		}
		var extra []byte
		if hasActive && i == len(segs)-1 {
			extra = tail
		}
		err := scanSegment(seg, extra, func(series string, t int64, v float64) {
			if t >= mint && t <= maxt {
				fn(series, t, v)
			}
		})
		if errors.Is(err, os.ErrNotExist) {
			continue // deleted by retention after the snapshot above
		}
		if err != nil {
			return fmt.Errorf("tsdb: %w", err)
		}
	}
	return nil
}

// Bounds returns the smallest and largest sample time in the store. ok is
// false when it holds no samples.
func (s *Store) Bounds() (minT, maxT int64, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	segs := s.segments
	if s.active != nil {
		segs = append(slices.Clone(segs), s.active.segmentInfo)
	}
	for i, seg := range segs {
		if i == 0 {
			minT, maxT = seg.minT, seg.maxT
			continue
		}
		minT, maxT = min(minT, seg.minT), max(maxT, seg.maxT)
	}
	return minT, maxT, len(segs) > 0
}

// DeleteBefore removes sealed segments whose samples are all older than t
// and returns how many it removed. Retention is per segment, so samples up
// to one SegmentSpan older than t may remain; the active segment is never
// removed.
func (s *Store) DeleteBefore(t int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, ErrClosed
	}
	var keep []segmentInfo
	removed := 0
	var errs []error
	for _, seg := range s.segments {
		if seg.maxT >= t {
			keep = append(keep, seg)
			continue
		}
		if err := os.Remove(seg.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
			keep = append(keep, seg)
			continue
		}
		removed++
	}
	s.segments = keep
	if removed > 0 {
		errs = append(errs, syncDir(s.dir))
	}
	if err := errors.Join(errs...); err != nil {
		return removed, fmt.Errorf("tsdb: %w", err)
	}
	return removed, nil
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
