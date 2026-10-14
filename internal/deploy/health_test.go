package deploy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ofk0/piway/internal/manifest"
)

var fast = manifest.Health{Path: "/healthz", Interval: 10 * time.Millisecond, Timeout: 100 * time.Millisecond, StartPeriod: 50 * time.Millisecond}

func alive(context.Context) error { return nil }

func TestHealthyAfterStarting(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			http.Error(w, "warming up", 503) // fails inside the start period
			return
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	var log strings.Builder
	if err := waitHealthy(context.Background(), srv.URL+"/healthz", fast, alive, &log); err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if !strings.Contains(log.String(), "starting, does not count") || !strings.Contains(log.String(), "204, healthy") {
		t.Errorf("log:\n%s", log.String())
	}
}

func TestRedirectIsHealthy(t *testing.T) {
	srv := httptest.NewServer(http.RedirectHandler("/login", http.StatusFound))
	defer srv.Close()
	if err := waitHealthy(context.Background(), srv.URL+"/", fast, alive, io.Discard); err != nil {
		t.Error(err)
	}
}

func TestUnhealthyAfterThreeFailures(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	var log strings.Builder
	err := waitHealthy(context.Background(), srv.URL+"/healthz", fast, alive, &log)
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(log.String(), "(3/3)") {
		t.Errorf("log:\n%s", log.String())
	}
}

func TestDeadContainerFailsAtOnce(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	h := fast
	h.StartPeriod = time.Hour // would otherwise wait for an hour
	dead := errors.New("the container exited with code 1")
	start := time.Now()
	err := waitHealthy(context.Background(), srv.URL, h, func(context.Context) error { return dead }, io.Discard)
	if !errors.Is(err, dead) || time.Since(start) > time.Second {
		t.Errorf("err = %v after %v", err, time.Since(start))
	}
}

func TestHealthCheckTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond) // longer than fast.Timeout
	}))
	defer srv.Close()
	err := waitHealthy(context.Background(), srv.URL, fast, alive, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "unhealthy") {
		t.Errorf("err = %v, want unhealthy after timeouts", err)
	}
}
