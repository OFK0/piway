// Command apiload sends GET requests to one URL from many workers and
// reports the throughput and latency.
//
//	go run ./cmd/apiload -url 'http://localhost:8080/api/v1/series?name=cpu_usage_percent&from=-1h' -workers 50 -duration 30s
//
// The URL is called as given, so it may be any endpoint; the load test of
// docs/PERFORMANCE.md uses /api/v1/series.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"
)

func main() {
	url := flag.String("url", "http://localhost:8080/api/v1/series?name=cpu_usage_percent", "URL to request")
	workers := flag.Int("workers", 20, "concurrent workers, each sending one request at a time")
	duration := flag.Duration("duration", 30*time.Second, "how long to keep sending")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: *workers}}

	var (
		mu      sync.Mutex
		lats    []time.Duration
		errs    int
		lastErr error
		wg      sync.WaitGroup
	)
	start := time.Now()
	for range *workers {
		wg.Go(func() {
			for ctx.Err() == nil {
				d, err := get(ctx, client, *url)
				if ctx.Err() != nil {
					return // the run ended mid-request; not a failure
				}
				mu.Lock()
				if err != nil {
					errs++
					lastErr = err
				} else {
					lats = append(lats, d)
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	elapsed := time.Since(start)

	slices.Sort(lats)
	fmt.Printf("workers:  %d for %s\n", *workers, elapsed.Round(time.Millisecond))
	fmt.Printf("requests: %d ok, %d failed (%.0f/s)\n", len(lats), errs, float64(len(lats))/elapsed.Seconds())
	if lastErr != nil {
		fmt.Fprintf(os.Stderr, "last error: %v\n", lastErr)
	}
	if len(lats) > 0 {
		fmt.Printf("latency:  p50 %s, p99 %s, max %s\n", pct(lats, 50), pct(lats, 99), lats[len(lats)-1].Round(10*time.Microsecond))
	}
	if errs > 0 {
		os.Exit(1)
	}
}

func get(ctx context.Context, c *http.Client, url string) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	start := time.Now()
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("status %d", resp.StatusCode)
	}
	return time.Since(start), nil
}

func pct(sorted []time.Duration, p int) time.Duration {
	return sorted[(len(sorted)-1)*p/100].Round(10 * time.Microsecond)
}
