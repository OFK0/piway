package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ofk0/piway/internal/history"
)

// SeriesQuerier answers range queries. *history.History implements it.
type SeriesQuerier interface {
	Query(name string, from, to time.Time, step time.Duration) (history.Result, error)
}

// seriesResponse is the body of GET /api/v1/series (docs/API.md).
type seriesResponse struct {
	Resolution  history.Resolution `json:"resolution"`
	StepSeconds float64            `json:"step_seconds"`
	From        time.Time          `json:"from"`
	To          time.Time          `json:"to"`
	Series      []seriesJSON       `json:"series"`
}

type seriesJSON struct {
	Name string `json:"name"`
	// Points are [time_ms, avg, min, max]: arrays, not objects, because a
	// day of four series is thousands of them.
	Points [][4]float64 `json:"points"`
}

// defaultPoints is the resolution used when the request has no step.
const defaultPoints = 300

func series(q SeriesQuerier, now func() time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if q == nil {
			writeError(w, http.StatusServiceUnavailable, "metrics history is disabled (data_dir is empty)")
			return
		}
		params := r.URL.Query()
		name := params.Get("name")
		if name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
		t := now()
		to, err := parseTime(params.Get("to"), t, t)
		if err != nil {
			writeError(w, http.StatusBadRequest, "to: "+err.Error())
			return
		}
		from, err := parseTime(params.Get("from"), to.Add(-time.Hour), t)
		if err != nil {
			writeError(w, http.StatusBadRequest, "from: "+err.Error())
			return
		}
		if !to.After(from) {
			writeError(w, http.StatusBadRequest, "to must be after from")
			return
		}
		step := to.Sub(from) / defaultPoints
		if s := params.Get("step"); s != "" {
			if step, err = time.ParseDuration(s); err != nil || step <= 0 {
				writeError(w, http.StatusBadRequest, "step: want a positive duration such as 1m")
				return
			}
		}

		res, err := q.Query(name, from, to, step)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		body := seriesResponse{
			Resolution:  res.Resolution,
			StepSeconds: res.Step.Seconds(),
			From:        from.UTC(),
			To:          to.UTC(),
			Series:      []seriesJSON{},
		}
		for _, s := range res.Series {
			js := seriesJSON{Name: s.Name, Points: make([][4]float64, len(s.Points))}
			for i, p := range s.Points {
				js.Points[i] = [4]float64{float64(p.T), p.Avg, p.Min, p.Max}
			}
			body.Series = append(body.Series, js)
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, body)
	}
}

// parseTime accepts RFC 3339, Unix milliseconds, or a duration relative to
// now ("-24h"). Empty means def.
func parseTime(s string, def, now time.Time) (time.Time, error) {
	switch {
	case s == "":
		return def, nil
	case strings.HasPrefix(s, "-") || strings.HasPrefix(s, "+"):
		d, err := time.ParseDuration(s)
		if err != nil {
			return time.Time{}, errors.New("want RFC 3339, Unix milliseconds or a relative duration such as -24h")
		}
		return now.Add(d), nil
	}
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.UnixMilli(ms), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, errors.New("want RFC 3339, Unix milliseconds or a relative duration such as -24h")
	}
	return t, nil
}
