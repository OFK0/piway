package deploy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ofk0/piway/internal/manifest"
)

// unhealthyAfter is how many failed checks in a row, once the start period
// is over, reject a new version (Docker's HEALTHCHECK default).
const unhealthyAfter = 3

// waitHealthy polls url until it answers 2xx or 3xx (healthy) or the new
// version is judged unhealthy. Failures during h.StartPeriod do not count;
// after it, unhealthyAfter consecutive failures do. alive is asked before
// every check, so a container that exits fails at once instead of after
// the whole start period.
func waitHealthy(ctx context.Context, url string, h manifest.Health, alive func(context.Context) error, out io.Writer) error {
	client := &http.Client{
		Timeout: h.Timeout,
		// A redirect is an answer: the app is up.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	start := time.Now()
	failures := 0
	for attempt := 1; ; attempt++ {
		if err := alive(ctx); err != nil {
			return err
		}
		status, err := probe(ctx, client, url)
		if err == nil && status < 400 {
			fmt.Fprintf(out, "Health check %d: %d, healthy\n", attempt, status)
			return nil
		}
		why := fmt.Sprint(err)
		if err == nil {
			why = fmt.Sprintf("HTTP %d", status)
		}
		if time.Since(start) < h.StartPeriod {
			fmt.Fprintf(out, "Health check %d: %s (starting, does not count)\n", attempt, why)
		} else {
			failures++
			fmt.Fprintf(out, "Health check %d: %s (%d/%d)\n", attempt, why, failures, unhealthyAfter)
			if failures >= unhealthyAfter {
				return fmt.Errorf("unhealthy: %s", why)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(h.Interval):
		}
	}
}

func probe(ctx context.Context, c *http.Client, url string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "piway-health")
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	return resp.StatusCode, nil
}
