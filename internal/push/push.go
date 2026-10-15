// Package push sends Web Push notifications (RFC 8030) to the browsers and
// phones that subscribed in the dashboard, with no third-party account:
// payloads are encrypted for each subscriber (RFC 8291) and requests are
// signed with piway's own VAPID key (RFC 8292). See docs/ALERTS.md.
package push

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Message is one notification.
type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	// Tag groups notifications on the device: a newer one with the same
	// tag replaces the older (a resolved alert replaces its firing one).
	Tag string `json:"tag,omitempty"`
	URL string `json:"url,omitempty"` // opened when the notification is clicked
	// Urgent asks the push service to deliver at once even to a device
	// saving battery (RFC 8030 "Urgency: high").
	Urgent bool `json:"-"`
}

// Subscription is one browser's push subscription.
type Subscription struct {
	ID        string     `json:"id"`
	Label     string     `json:"label"`
	Service   string     `json:"service"` // the push service's host
	Created   time.Time  `json:"created_at"`
	LastOK    *time.Time `json:"last_ok_at,omitempty"`
	LastError string     `json:"last_error,omitempty"`
	Endpoint  string     `json:"-"`
	P256dh    string     `json:"-"`
	Auth      string     `json:"-"`
}

// stored is a Subscription on disk, secrets included.
type stored struct {
	Subscription
	Endpoint string `json:"endpoint"`
	P256dh   string `json:"p256dh"`
	Auth     string `json:"auth"`
}

// Options configure a Service.
type Options struct {
	Dir     string // vapid.json and subscriptions.json go here
	Subject string // a mailto: or https: contact for push services (RFC 8292)
	Logger  *slog.Logger
	Client  *http.Client
	// AllowedHosts are push service host suffixes subscriptions may use;
	// nil means the big browser vendors' (allowedHosts).
	AllowedHosts []string
	now          func() time.Time
}

// allowedHosts are the push services of Chrome and Edge (FCM), Firefox,
// Safari and iOS, and Windows. Restricting endpoints to them keeps a
// subscription from making piway send requests anywhere else.
var allowedHosts = []string{
	"fcm.googleapis.com", "android.googleapis.com",
	"push.services.mozilla.com",
	"push.apple.com",
	"notify.windows.com",
}

const (
	maxSubscriptions = 20
	queueLen         = 64
	ttl              = 24 * time.Hour // how long a push service keeps an undelivered message
)

// Service keeps the subscriptions and sends to them.
type Service struct {
	opts  Options
	vapid *vapid
	queue chan Message

	mu   sync.Mutex
	subs []*Subscription
}

// New loads (or on first start, creates) the VAPID key and the
// subscriptions.
func New(opts Options) (*Service, error) {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Client == nil {
		opts.Client = &http.Client{
			Timeout: 15 * time.Second,
			// A redirect could lead anywhere; push services do not redirect.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	if opts.AllowedHosts == nil {
		opts.AllowedHosts = allowedHosts
	}
	if opts.now == nil {
		opts.now = time.Now
	}
	v, created, err := loadOrCreateVAPID(filepath.Join(opts.Dir, "vapid.json"))
	if err != nil {
		return nil, fmt.Errorf("push: VAPID key: %w", err)
	}
	if created {
		opts.Logger.Info("created a VAPID key for push notifications", "file", filepath.Join(opts.Dir, "vapid.json"))
	}
	s := &Service{opts: opts, vapid: v, queue: make(chan Message, queueLen)}
	b, err := os.ReadFile(s.subsFile())
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		var list []stored
		if err := json.Unmarshal(b, &list); err != nil {
			return nil, fmt.Errorf("push: %s: %w", s.subsFile(), err)
		}
		for _, st := range list {
			sub := st.Subscription
			sub.Endpoint, sub.P256dh, sub.Auth = st.Endpoint, st.P256dh, st.Auth
			s.subs = append(s.subs, &sub)
		}
	}
	return s, nil
}

func (s *Service) subsFile() string { return filepath.Join(s.opts.Dir, "subscriptions.json") }

// PublicKey is the applicationServerKey browsers subscribe with.
func (s *Service) PublicKey() string { return s.vapid.pub }

// saveLocked writes the subscriptions; caller holds s.mu.
func (s *Service) saveLocked() error {
	list := make([]stored, 0, len(s.subs))
	for _, sub := range s.subs {
		list = append(list, stored{Subscription: *sub, Endpoint: sub.Endpoint, P256dh: sub.P256dh, Auth: sub.Auth})
	}
	return writeJSON(s.subsFile(), list)
}

// Notify queues m for every subscription. It never blocks: when the queue
// is full the message is dropped and logged.
func (s *Service) Notify(m Message) {
	select {
	case s.queue <- m:
	default:
		s.opts.Logger.Warn("push queue full; notification dropped", "title", m.Title)
	}
}

// Run sends queued messages until ctx ends.
func (s *Service) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-s.queue:
			s.sendAll(ctx, m, "")
		}
	}
}

// Result is the outcome of sending to one subscription.
type Result struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// sendAll sends m to every subscription, or only to id if it is set. It
// sends from copies, so the API can change subscriptions meanwhile.
func (s *Service) sendAll(ctx context.Context, m Message, id string) []Result {
	s.mu.Lock()
	var targets []Subscription
	for _, sub := range s.subs {
		if id == "" || sub.ID == id {
			targets = append(targets, *sub)
		}
	}
	s.mu.Unlock()
	var results []Result
	for _, sub := range targets {
		err := s.send(ctx, &sub, m)
		r := Result{ID: sub.ID, Label: sub.Label, OK: err == nil}
		s.mu.Lock()
		now := s.opts.now()
		cur := s.findLocked(sub.ID) // nil if removed meanwhile
		switch {
		case errors.Is(err, errGone):
			s.removeLocked(sub.ID)
			r.Error = "the browser ended this subscription; it was removed"
			s.opts.Logger.Info("push subscription gone; removed", "id", sub.ID, "label", sub.Label)
		case err != nil:
			r.Error = err.Error()
			if cur != nil {
				cur.LastError = err.Error()
			}
			s.opts.Logger.Warn("push failed", "id", sub.ID, "label", sub.Label, "err", err)
		case cur != nil:
			cur.LastOK, cur.LastError = &now, ""
		}
		if err := s.saveLocked(); err != nil {
			s.opts.Logger.Error("save push subscriptions", "err", err)
		}
		s.mu.Unlock()
		results = append(results, r)
	}
	return results
}

// errGone means the push service no longer knows the subscription.
var errGone = errors.New("subscription gone")

func (s *Service) send(ctx context.Context, sub *Subscription, m Message) error {
	payload, err := json.Marshal(m)
	if err != nil {
		return err
	}
	p256dh, err := base64Decode(sub.P256dh)
	if err != nil {
		return fmt.Errorf("subscriber key: %w", err)
	}
	auth, err := base64Decode(sub.Auth)
	if err != nil {
		return fmt.Errorf("subscriber auth secret: %w", err)
	}
	body, err := encrypt(payload, p256dh, auth, randReader)
	if err != nil {
		return err
	}
	authz, err := s.vapid.authorization(sub.Endpoint, s.opts.Subject, s.opts.now())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authz)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.Itoa(int(ttl.Seconds())))
	if m.Urgent {
		req.Header.Set("Urgency", "high")
	} else {
		req.Header.Set("Urgency", "normal")
	}
	if m.Tag != "" {
		// A newer message with the same topic replaces an undelivered one.
		sum := sha256.Sum256([]byte(m.Tag))
		req.Header.Set("Topic", base64.RawURLEncoding.EncodeToString(sum[:24])) // 32 characters
	}
	resp, err := s.opts.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return errGone
	}
	var detail [200]byte
	n, _ := resp.Body.Read(detail[:])
	msg := strings.TrimSpace(string(detail[:n]))
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return fmt.Errorf("%s: %d %s", hostOf(sub.Endpoint), resp.StatusCode, msg)
}

// base64Decode reads the base64url keys browsers send, padded or not.
func base64Decode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

func hostOf(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "?"
	}
	return u.Hostname()
}

// checkEndpoint accepts only https URLs of a known push service.
func (s *Service) checkEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return errors.New("endpoint must be an https URL")
	}
	if p := u.Port(); p != "" && p != "443" {
		return errors.New("endpoint must use port 443")
	}
	host := strings.ToLower(u.Hostname())
	if net.ParseIP(host) != nil {
		return errors.New("endpoint must be a push service's name, not an address")
	}
	for _, h := range s.opts.AllowedHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return nil
		}
	}
	return fmt.Errorf("%s is not a known push service", host)
}

func (s *Service) removeLocked(id string) bool {
	i := slices.IndexFunc(s.subs, func(sub *Subscription) bool { return sub.ID == id })
	if i < 0 {
		return false
	}
	s.subs = slices.Delete(s.subs, i, i+1)
	return true
}

func subscriptionID(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return hex.EncodeToString(sum[:8])
}

// writeJSON writes v as JSON to path (mode 0600) in one rename.
func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
