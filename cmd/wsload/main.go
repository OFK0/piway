// Command wsload opens many WebSocket clients against piway's
// /api/v1/stream and reports how evenly and how quickly snapshots arrive.
//
//	go run ./cmd/wsload -url ws://localhost:8080/api/v1/stream -clients 100 -duration 30s
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type result struct {
	msgs     int
	lags     []time.Duration // receive time minus snapshot time
	closeErr error
}

func main() {
	url := flag.String("url", "ws://localhost:8080/api/v1/stream", "stream URL")
	clients := flag.Int("clients", 100, "number of concurrent clients")
	duration := flag.Duration("duration", 30*time.Second, "how long each client stays connected")
	slow := flag.Int("slow", 0, "clients that connect but never read, to show they do not hold back the others")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()

	results := make([]result, *clients)
	var wg sync.WaitGroup
	start := time.Now()
	for i := range *clients {
		wg.Go(func() { results[i] = runClient(ctx, *url) })
	}
	for range *slow {
		wg.Go(func() { runSlowClient(ctx, *url) })
	}
	wg.Wait()
	elapsed := time.Since(start)

	report(results, elapsed, *slow)
}

func runClient(ctx context.Context, url string) result {
	var r result
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		r.closeErr = fmt.Errorf("dial: %w", err)
		return r
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)

	for {
		_, msg, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() == nil {
				r.closeErr = err
			}
			return r
		}
		now := time.Now()
		var s struct {
			Time time.Time `json:"time"`
		}
		if err := json.Unmarshal(msg, &s); err == nil {
			r.lags = append(r.lags, now.Sub(s.Time))
		}
		r.msgs++
	}
}

// runSlowClient never reads. The server must evict it without slowing down
// anyone else.
func runSlowClient(ctx context.Context, url string) {
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	<-ctx.Done()
}

func report(results []result, elapsed time.Duration, slow int) {
	var (
		counts []int
		lags   []time.Duration
		failed int
	)
	for i, r := range results {
		if r.closeErr != nil {
			failed++
			if failed <= 5 {
				fmt.Fprintf(os.Stderr, "client %d: %v\n", i, r.closeErr)
			}
		}
		counts = append(counts, r.msgs)
		lags = append(lags, r.lags...)
	}
	slices.Sort(counts)
	slices.Sort(lags)

	fmt.Printf("clients:  %d (+%d never reading) for %s\n", len(results), slow, elapsed.Round(time.Millisecond))
	fmt.Printf("failed:   %d\n", failed)
	fmt.Printf("messages: min %d, median %d, max %d per client\n",
		counts[0], counts[len(counts)/2], counts[len(counts)-1])
	if len(lags) > 0 {
		fmt.Printf("lag:      p50 %s, p99 %s, max %s (snapshot time to receive)\n",
			pct(lags, 50), pct(lags, 99), lags[len(lags)-1])
	}
}

func pct(sorted []time.Duration, p int) time.Duration {
	return sorted[(len(sorted)-1)*p/100].Round(10 * time.Microsecond)
}
