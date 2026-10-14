package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ofk0/piway/internal/history"
)

var now = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

type fakeQuerier struct {
	name     string
	from, to time.Time
	step     time.Duration
	err      error
}

func (f *fakeQuerier) Query(name string, from, to time.Time, step time.Duration) (history.Result, error) {
	f.name, f.from, f.to, f.step = name, from, to, step
	return history.Result{
		Resolution: history.ResolutionMinute,
		Step:       5 * time.Minute,
		Series: []history.Series{{
			Name:   `cpu_usage_percent`,
			Points: []history.Point{{T: 1000, Avg: 12.5, Min: 3, Max: 40}},
		}},
	}, f.err
}

func getSeries(t *testing.T, q SeriesQuerier, query string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	series(q, func() time.Time { return now })(rec, httptest.NewRequest("GET", "/api/v1/series?"+query, nil))
	return rec
}

func TestSeriesDefaults(t *testing.T) {
	q := &fakeQuerier{}
	rec := getSeries(t, q, "name=cpu_usage_percent")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if !q.to.Equal(now) || !q.from.Equal(now.Add(-time.Hour)) {
		t.Errorf("range %v..%v, want the last hour", q.from, q.to)
	}
	if q.step != time.Hour/defaultPoints {
		t.Errorf("step %v, want %v", q.step, time.Hour/defaultPoints)
	}

	var body struct {
		Resolution  string  `json:"resolution"`
		StepSeconds float64 `json:"step_seconds"`
		Series      []struct {
			Name   string
			Points [][4]float64
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Resolution != "1m" || body.StepSeconds != 300 {
		t.Errorf("resolution %q step %v", body.Resolution, body.StepSeconds)
	}
	if got := body.Series[0].Points[0]; got != [4]float64{1000, 12.5, 3, 40} {
		t.Errorf("point %v, want [t avg min max]", got)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("missing Cache-Control: no-store")
	}
}

func TestSeriesTimeFormats(t *testing.T) {
	tests := []struct {
		query    string
		from, to time.Time
		step     time.Duration
	}{
		{"from=-24h&step=2m", now.Add(-24 * time.Hour), now, 2 * time.Minute},
		{"from=2026-10-14T10:00:00Z&to=2026-10-14T11:00:00Z", now.Add(-2 * time.Hour), now.Add(-time.Hour), 12 * time.Second},
		{"from=1791972000000&to=1791975600000", time.UnixMilli(1791972000000), time.UnixMilli(1791975600000), 12 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			q := &fakeQuerier{}
			if rec := getSeries(t, q, "name=x&"+tt.query); rec.Code != 200 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			if !q.from.Equal(tt.from) || !q.to.Equal(tt.to) || q.step != tt.step {
				t.Errorf("got %v..%v step %v, want %v..%v step %v", q.from, q.to, q.step, tt.from, tt.to, tt.step)
			}
		})
	}
}

func TestSeriesErrors(t *testing.T) {
	tests := []struct {
		query  string
		status int
		msg    string
	}{
		{"", 400, "name is required"},
		{"name=x&from=yesterday", 400, "from:"},
		{"name=x&to=-1h&from=-1m", 400, "to must be after from"},
		{"name=x&step=0", 400, "step:"},
		{"name=x&step=fast", 400, "step:"},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			rec := getSeries(t, &fakeQuerier{}, tt.query)
			if rec.Code != tt.status || !strings.Contains(rec.Body.String(), tt.msg) {
				t.Errorf("status %d body %s, want %d with %q", rec.Code, rec.Body, tt.status, tt.msg)
			}
		})
	}

	if rec := getSeries(t, &fakeQuerier{err: errors.New("disk")}, "name=x"); rec.Code != 500 {
		t.Errorf("query error gave %d, want 500", rec.Code)
	}
	if rec := getSeries(t, nil, "name=x"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("history disabled gave %d, want 503", rec.Code)
	}
}

func TestSeriesRoute(t *testing.T) {
	h, err := New(stubSource{}, http.NotFoundHandler(), &fakeQuerier{}, http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec := serve(t, h, "GET", "/api/v1/series?name=x"); rec.Code != 200 {
		t.Errorf("GET /api/v1/series = %d", rec.Code)
	}
	if rec := serve(t, h, "POST", "/api/v1/series"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", rec.Code)
	}
}
