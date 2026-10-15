package push

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"
)

// Routes registers the push API on mux, each handler wrapped in protect
// (sign-in).
func (s *Service) Routes(mux *http.ServeMux, protect func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/push", protect(http.HandlerFunc(s.status)))
	mux.Handle("POST /api/v1/push/subscriptions", protect(http.HandlerFunc(s.subscribe)))
	mux.Handle("DELETE /api/v1/push/subscriptions/{id}", protect(http.HandlerFunc(s.unsubscribe)))
	mux.Handle("POST /api/v1/push/test", protect(http.HandlerFunc(s.test)))
}

func (s *Service) status(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	subs := make([]Subscription, 0, len(s.subs))
	for _, sub := range s.subs {
		subs = append(subs, *sub)
	}
	s.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	writeBody(w, http.StatusOK, map[string]any{"public_key": s.vapid.pub, "subscriptions": subs})
}

// subscribe stores PushSubscription.toJSON() from a browser. Subscribing
// the same endpoint again updates it.
func (s *Service) subscribe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Subscription struct {
			Endpoint string `json:"endpoint"`
			Keys     struct {
				P256dh string `json:"p256dh"`
				Auth   string `json:"auth"`
			} `json:"keys"`
		} `json:"subscription"`
		Label string `json:"label"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, `send {"subscription": PushSubscription.toJSON(), "label": "..."}`)
		return
	}
	sub := req.Subscription
	if err := s.checkEndpoint(sub.Endpoint); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Check the keys now rather than at the first alert.
	if _, err := encrypt([]byte("{}"), decode(sub.Keys.P256dh), decode(sub.Keys.Auth), randReader); err != nil {
		writeErr(w, http.StatusBadRequest, "keys.p256dh and keys.auth must be the browser's P-256 key and 16-byte secret")
		return
	}
	label := strings.TrimSpace(req.Label)
	if label == "" {
		label = "Unnamed device"
	}
	if utf8.RuneCountInString(label) > 64 {
		label = string([]rune(label)[:64])
	}

	id := subscriptionID(sub.Endpoint)
	s.mu.Lock()
	defer s.mu.Unlock()
	existing := s.findLocked(id)
	if existing == nil && len(s.subs) >= maxSubscriptions {
		writeErr(w, http.StatusConflict, "too many subscribed devices: remove some first")
		return
	}
	if existing == nil {
		existing = &Subscription{ID: id, Created: s.opts.now()}
		s.subs = append(s.subs, existing)
	}
	existing.Label, existing.Service = label, hostOf(sub.Endpoint)
	existing.Endpoint, existing.P256dh, existing.Auth = sub.Endpoint, sub.Keys.P256dh, sub.Keys.Auth
	if err := s.saveLocked(); err != nil {
		writeErr(w, http.StatusInternalServerError, "save subscriptions: "+err.Error())
		return
	}
	s.opts.Logger.Info("push subscription saved", "id", id, "label", label, "service", existing.Service)
	writeBody(w, http.StatusCreated, existing)
}

func (s *Service) findLocked(id string) *Subscription {
	for _, sub := range s.subs {
		if sub.ID == id {
			return sub
		}
	}
	return nil
}

func (s *Service) unsubscribe(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.removeLocked(r.PathValue("id")) {
		writeErr(w, http.StatusNotFound, "no subscription with that id")
		return
	}
	if err := s.saveLocked(); err != nil {
		writeErr(w, http.StatusInternalServerError, "save subscriptions: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// test sends a test notification now, to one subscription or all, and
// says how each went.
func (s *Service) test(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, `send {"id": "..."} or nothing`)
			return
		}
	}
	results := s.sendAll(r.Context(), Message{
		Title: "piway test notification",
		Body:  "Alerts from this Pi will arrive like this.",
		Tag:   "test",
		URL:   "/settings",
	}, req.ID)
	if req.ID != "" && len(results) == 0 {
		writeErr(w, http.StatusNotFound, "no subscription with that id")
		return
	}
	writeBody(w, http.StatusOK, map[string]any{"results": results})
}

func decode(s string) []byte {
	b, _ := base64Decode(s)
	return b
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeBody(w, status, map[string]string{"error": msg})
}

func writeBody(w http.ResponseWriter, status int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(append(b, '\n'))
}
