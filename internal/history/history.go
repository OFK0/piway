// Package history keeps the metrics history at three resolutions and
// expires each after its retention period:
//
//	raw  every sample (one per collection interval)   default 24 h
//	1m   one rollup per series per minute             default 7 days
//	1h   one rollup per series per hour               default 1 year
//
// A rollup stores four series per source series: name|min, name|max,
// name|sum and name|n (the number of raw samples). All four combine
// exactly: the 1h rollup is built from the 1m rollups by taking the min of
// the mins, the max of the maxes and the sums of sum and n, and the average
// is sum/n at query time. Storing averages instead would make the hourly
// average an average of averages, which is wrong whenever minutes have
// different sample counts (a restart, a slow round).
//
// Each tier is its own tsdb.Store in a subdirectory of the data dir.
// Compaction resumes from the newest rollup already stored, so a restart
// picks up where it left off and fills the minutes piway was down for, as
// far as raw data still exists.
package history

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ofk0/piway/internal/tsdb"
)

// Rollup series suffixes.
const (
	SuffixMin   = "|min"
	SuffixMax   = "|max"
	SuffixSum   = "|sum"
	SuffixCount = "|n"
)

// Retention is how long each tier keeps data.
type Retention struct {
	Raw, Minute, Hour time.Duration
}

// DefaultRetention keeps about 70 MB on a Pi 4 (docs/STORAGE.md).
func DefaultRetention() Retention {
	return Retention{Raw: 24 * time.Hour, Minute: 7 * 24 * time.Hour, Hour: 365 * 24 * time.Hour}
}

// Options configures a History. Zero Retention fields take the defaults.
type Options struct {
	Dir           string
	FlushInterval time.Duration // for the raw tier
	Retention     Retention
	Logger        *slog.Logger

	now func() time.Time // tests replace the clock
}

// grace is how long after a minute ends its rollup waits, so the samples
// of the round that started just before the boundary have arrived.
const grace = 5 * time.Second

// History is the raw store plus its rollups.
type History struct {
	opts                 Options
	raw, minute, hour    *tsdb.Store
	nextMinute, nextHour int64 // start of the next bucket to roll up; 0 = unknown
}

// Open opens (or creates) the three tiers under opts.Dir.
func Open(opts Options) (*History, error) {
	def := DefaultRetention()
	if opts.Retention.Raw <= 0 {
		opts.Retention.Raw = def.Raw
	}
	if opts.Retention.Minute <= 0 {
		opts.Retention.Minute = def.Minute
	}
	if opts.Retention.Hour <= 0 {
		opts.Retention.Hour = def.Hour
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.now == nil {
		opts.now = time.Now
	}

	h := &History{opts: opts}
	var err error
	// Segment spans set the retention granularity: an hour of raw data,
	// a day of minutes, a month of hours, so each tier has a few dozen files.
	if h.raw, err = tsdb.Open(filepath.Join(opts.Dir, "raw"), tsdb.Options{
		SegmentSpan: time.Hour, FlushInterval: opts.FlushInterval, Logger: opts.Logger,
	}); err != nil {
		return nil, err
	}
	if h.minute, err = tsdb.Open(filepath.Join(opts.Dir, "1m"), tsdb.Options{
		SegmentSpan: 24 * time.Hour, Logger: opts.Logger,
	}); err != nil {
		h.raw.Close()
		return nil, err
	}
	if h.hour, err = tsdb.Open(filepath.Join(opts.Dir, "1h"), tsdb.Options{
		SegmentSpan: 30 * 24 * time.Hour, Logger: opts.Logger,
	}); err != nil {
		h.raw.Close()
		h.minute.Close()
		return nil, err
	}
	return h, nil
}

// Append records points at t (ms) in the raw tier.
func (h *History) Append(t int64, points []tsdb.Point) error {
	return h.raw.Append(t, points)
}

// Raw, Minute and Hour expose the tiers for queries.
func (h *History) Raw() *tsdb.Store    { return h.raw }
func (h *History) Minute() *tsdb.Store { return h.minute }
func (h *History) Hour() *tsdb.Store   { return h.hour }

// Run compacts and expires every interval until ctx is cancelled. Errors
// are logged when they start or change.
func (h *History) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	var last string
	for {
		msg := ""
		if err := h.Compact(); err != nil {
			msg = err.Error()
		}
		if msg != last {
			if msg == "" {
				h.opts.Logger.Info("history compaction recovered")
			} else {
				h.opts.Logger.Warn("history compaction failed", "err", msg)
			}
			last = msg
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Compact rolls up every complete minute and hour not yet rolled up, then
// deletes data past retention. It must not run concurrently with itself.
func (h *History) Compact() error {
	now := h.opts.now()
	minuteEnd := now.Add(-grace).Truncate(time.Minute).UnixMilli()
	if err := h.rollup(h.raw, h.minute, &h.nextMinute, time.Minute, minuteEnd, addRaw); err != nil {
		return fmt.Errorf("1m rollup: %w", err)
	}
	// An hour is complete once all its minutes are rolled up.
	if h.nextMinute > 0 {
		hourEnd := time.UnixMilli(h.nextMinute).Truncate(time.Hour).UnixMilli()
		if err := h.rollup(h.minute, h.hour, &h.nextHour, time.Hour, hourEnd, addRollup); err != nil {
			return fmt.Errorf("1h rollup: %w", err)
		}
	}
	return h.expire(now)
}

func (h *History) expire(now time.Time) error {
	cut := func(d time.Duration) int64 { return now.Add(-d).UnixMilli() }
	// Never expire raw data that has not been rolled up yet.
	rawCut := min(cut(h.opts.Retention.Raw), h.nextMinute)
	var errs []error
	for _, x := range []struct {
		s   *tsdb.Store
		cut int64
	}{{h.raw, rawCut}, {h.minute, cut(h.opts.Retention.Minute)}, {h.hour, cut(h.opts.Retention.Hour)}} {
		if _, err := x.s.DeleteBefore(x.cut); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// agg is a rollup in progress.
type agg struct {
	min, max, sum, n float64
}

func (a *agg) add(o agg) {
	if a.n == 0 {
		*a = o
		return
	}
	a.min, a.max = min(a.min, o.min), max(a.max, o.max)
	a.sum += o.sum
	a.n += o.n
}

// addRaw folds a raw sample into the bucket's rollup for its series.
func addRaw(bucket map[string]*agg, series string, v float64) {
	a := bucket[series]
	if a == nil {
		a = &agg{}
		bucket[series] = a
	}
	a.add(agg{min: v, max: v, sum: v, n: 1})
}

// addRollup folds one component of a finer rollup into the bucket.
func addRollup(bucket map[string]*agg, series string, v float64) {
	i := strings.LastIndexByte(series, '|')
	if i < 0 {
		return
	}
	base, part := series[:i], series[i:]
	a := bucket[base]
	if a == nil {
		// Components arrive in any order, so start from the identities.
		a = &agg{min: math.Inf(1), max: math.Inf(-1)}
		bucket[base] = a
	}
	switch part {
	case SuffixMin:
		a.min = min(a.min, v)
	case SuffixMax:
		a.max = max(a.max, v)
	case SuffixSum:
		a.sum += v
	case SuffixCount:
		a.n += v
	}
}

// maxBuckets bounds how much one compaction step reads into memory when
// catching up after downtime.
const maxBuckets = 120

// rollup aggregates src into dst in buckets of step, for every complete
// bucket from *next up to end (exclusive).
func (h *History) rollup(src, dst *tsdb.Store, next *int64, step time.Duration, end int64,
	add func(map[string]*agg, string, float64)) error {
	stepMs := step.Milliseconds()
	if *next == 0 {
		if _, maxT, ok := dst.Bounds(); ok {
			*next = maxT + stepMs
		} else if minT, _, ok := src.Bounds(); ok {
			*next = minT - minT%stepMs
		} else {
			return nil // nothing recorded yet
		}
	}

	for *next < end {
		chunkEnd := min(end, *next+maxBuckets*stepMs)
		buckets := make(map[int64]map[string]*agg)
		err := src.Scan(*next, chunkEnd-1, func(series string, t int64, v float64) {
			b := t - t%stepMs
			if buckets[b] == nil {
				buckets[b] = make(map[string]*agg)
			}
			add(buckets[b], series, v)
		})
		if err != nil {
			return err
		}
		for b := *next; b < chunkEnd; b += stepMs {
			if len(buckets[b]) == 0 {
				continue // piway was not running
			}
			if err := dst.Append(b, rollupPoints(buckets[b])); err != nil {
				return err
			}
		}
		*next = chunkEnd
	}
	return nil
}

func rollupPoints(bucket map[string]*agg) []tsdb.Point {
	points := make([]tsdb.Point, 0, 4*len(bucket))
	// Sorted, so the same data always produces the same bytes.
	for _, name := range slices.Sorted(maps.Keys(bucket)) {
		a := bucket[name]
		if len(name)+len(SuffixCount)+1 > tsdb.MaxSeriesLen {
			continue
		}
		points = append(points,
			tsdb.Point{Series: name + SuffixMin, Value: a.min},
			tsdb.Point{Series: name + SuffixMax, Value: a.max},
			tsdb.Point{Series: name + SuffixSum, Value: a.sum},
			tsdb.Point{Series: name + SuffixCount, Value: a.n},
		)
	}
	return points
}

// Close writes pending raw samples and closes the tiers.
func (h *History) Close() error {
	return errors.Join(h.raw.Close(), h.minute.Close(), h.hour.Close())
}

// Query limits (docs/API.md › GET /api/v1/series).
const (
	// MaxPoints is the most points a query returns per series; a smaller
	// step is raised to fit.
	MaxPoints = 1000
	// maxRawRange is the longest range served from raw samples. Longer
	// ranges read rollups: a day of raw data is ~29 MB to scan on a Pi.
	maxRawRange = 6 * time.Hour
)

// Resolution names a tier.
type Resolution string

const (
	ResolutionRaw    Resolution = "raw"
	ResolutionMinute Resolution = "1m"
	ResolutionHour   Resolution = "1h"
)

// Point is one bucket of a query result.
type Point struct {
	T             int64 // bucket start, ms
	Avg, Min, Max float64
}

// Series is one matching series of a query result.
type Series struct {
	Name   string
	Points []Point
}

// Result is the answer to a Query.
type Result struct {
	Resolution Resolution
	Step       time.Duration
	Series     []Series
}

// Query returns the series named name (exactly, or with any labels when
// name has none) in [from, to), in buckets of step. It reads the
// finest tier that suits the step and still holds data from the start of
// the range.
func (h *History) Query(name string, from, to time.Time, step time.Duration) (Result, error) {
	if !to.After(from) {
		return Result{}, errors.New("to must be after from")
	}
	span := to.Sub(from)
	step = max(step, (span+MaxPoints-1)/MaxPoints, time.Second)

	res, src := h.pickTier(from, span, step)
	switch res {
	case ResolutionMinute:
		step = max(step, time.Minute).Truncate(time.Minute)
	case ResolutionHour:
		step = max(step, time.Hour).Truncate(time.Hour)
	}
	stepMs := step.Milliseconds()
	match := matcher(name)

	add := addRollup
	if res == ResolutionRaw {
		add = addRaw
	}
	// Same shape as compaction: bucket -> series -> rollup, so a query
	// combines samples exactly as the 1m and 1h tiers do.
	buckets := make(map[int64]map[string]*agg)
	// The range is half-open, [from, to), so consecutive queries never
	// return the same bucket twice.
	err := src.Scan(from.UnixMilli(), to.UnixMilli()-1, func(series string, t int64, v float64) {
		base := series
		if i := strings.LastIndexByte(series, '|'); i >= 0 && res != ResolutionRaw {
			base = series[:i]
		}
		if !match(base) {
			return
		}
		b := t - t%stepMs
		if buckets[b] == nil {
			buckets[b] = make(map[string]*agg)
		}
		add(buckets[b], series, v)
	})
	if err != nil {
		return Result{}, err
	}

	bySeries := make(map[string][]Point)
	for _, b := range slices.Sorted(maps.Keys(buckets)) {
		for name, a := range buckets[b] {
			if a.n > 0 {
				bySeries[name] = append(bySeries[name], Point{T: b, Avg: a.sum / a.n, Min: a.min, Max: a.max})
			}
		}
	}
	out := Result{Resolution: res, Step: step}
	for _, name := range slices.Sorted(maps.Keys(bySeries)) {
		out.Series = append(out.Series, Series{Name: name, Points: bySeries[name]})
	}
	return out, nil
}

// pickTier chooses where a query reads from: raw for short ranges at
// sub-minute steps, then the finest rollup that has data from the start of
// the range, falling back to the coarsest tier that has any.
func (h *History) pickTier(from time.Time, span, step time.Duration) (Resolution, *tsdb.Store) {
	covers := func(s *tsdb.Store) bool {
		minT, _, ok := s.Bounds()
		return ok && minT <= from.UnixMilli()
	}
	if step < time.Minute && span <= maxRawRange && covers(h.raw) {
		return ResolutionRaw, h.raw
	}
	if step < time.Hour && covers(h.minute) {
		return ResolutionMinute, h.minute
	}
	if covers(h.hour) {
		return ResolutionHour, h.hour
	}
	// Nothing covers the start (a young install): use what exists.
	switch {
	case step < time.Minute && span <= maxRawRange:
		return ResolutionRaw, h.raw
	case step < time.Hour:
		if _, _, ok := h.minute.Bounds(); ok {
			return ResolutionMinute, h.minute
		}
		return ResolutionRaw, h.raw
	default:
		if _, _, ok := h.hour.Bounds(); ok {
			return ResolutionHour, h.hour
		}
		return ResolutionMinute, h.minute
	}
}

// matcher matches name exactly, and also every labelled variant of it
// (name{...}) when name has no labels of its own.
func matcher(name string) func(string) bool {
	if strings.Contains(name, "{") {
		return func(s string) bool { return s == name }
	}
	prefix := name + "{"
	return func(s string) bool { return s == name || strings.HasPrefix(s, prefix) }
}
