package publicurl

import (
	"context"
	"errors"
	"testing"
)

const webhook = "/api/v1/webhooks/github"

// The layout docs/REMOTE.md sets up: the dashboard on 443, tailnet only;
// the webhook path alone published with Funnel on 10000.
const piStatus = `{
  "TCP": {"10000": {"HTTPS": true}, "443": {"HTTPS": true}},
  "Web": {
    "pi.example.ts.net:10000": {"Handlers": {"/api/v1/webhooks/github": {"Proxy": "http://127.0.0.1:8080/api/v1/webhooks/github"}}},
    "pi.example.ts.net:443": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:8080"}}}
  },
  "AllowFunnel": {"pi.example.ts.net:10000": true}
}`

func TestFunnel(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		port         int
		want         string
	}{
		{"webhook path on its own port", piStatus, 8080, "https://pi.example.ts.net:10000" + webhook},
		{"another piway port", piStatus, 9090, ""},
		{"everything published on 443", `{"Web": {"pi.example.ts.net:443": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:8080"}}}}, "AllowFunnel": {"pi.example.ts.net:443": true}}`, 8080, "https://pi.example.ts.net" + webhook},
		{"published, but not public", `{"Web": {"pi.example.ts.net:443": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:8080"}}}}, "AllowFunnel": {}}`, 8080, ""},
		{"mounted under a prefix", `{"Web": {"h.ts.net:8443": {"Handlers": {"/piway": {"Proxy": "http://localhost:8080"}}}}, "AllowFunnel": {"h.ts.net:8443": true}}`, 8080, ""},
		{"a prefix forwarded as is", `{"Web": {"h.ts.net:8443": {"Handlers": {"/api": {"Proxy": "http://127.0.0.1:8080/api"}}}}, "AllowFunnel": {"h.ts.net:8443": true}}`, 8080, "https://h.ts.net:8443" + webhook},
		{"another path only", `{"Web": {"h.ts.net:443": {"Handlers": {"/metrics": {"Proxy": "http://127.0.0.1:8080/metrics"}}}}, "AllowFunnel": {"h.ts.net:443": true}}`, 8080, ""},
		{"not JSON", "Funnel off", 8080, ""},
	} {
		if got := Funnel([]byte(tc.status), tc.port, webhook); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestFinder(t *testing.T) {
	calls := 0
	f := &Finder{Addr: ":8080", Path: webhook, run: func(context.Context) ([]byte, error) { calls++; return []byte(piStatus), nil }}
	for range 3 {
		if u, src := f.URL(context.Background()); u != "https://pi.example.ts.net:10000"+webhook || src != FromFunnel {
			t.Fatalf("URL = %q %q", u, src)
		}
	}
	if calls != 1 {
		t.Errorf("tailscale asked %d times, want once a minute", calls)
	}

	f = &Finder{Configured: "https://hooks.example.com/gh", run: func(context.Context) ([]byte, error) { t.Fatal("asked tailscale"); return nil, nil }}
	if u, src := f.URL(context.Background()); u != "https://hooks.example.com/gh" || src != FromConfig {
		t.Errorf("configured: %q %q", u, src)
	}

	f = &Finder{Addr: ":8080", Path: webhook, run: func(context.Context) ([]byte, error) { return nil, errors.New("not installed") }}
	if u, src := f.URL(context.Background()); u != "" || src != "" {
		t.Errorf("without tailscale: %q %q", u, src)
	}
}
