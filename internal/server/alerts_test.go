package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/ofk0/piway/internal/alert"
	"github.com/ofk0/piway/internal/tsdb"
)

func TestAlertsAPI(t *testing.T) {
	above := 75.0
	e := alert.New(alert.Config{Rules: []alert.Rule{{Name: "CPU is hot", Metric: "temperature_celsius", Above: &above, ClearFor: time.Minute}}})
	e.Observe([]tsdb.Point{{Series: "temperature_celsius", Value: 80}}, time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC))
	h, err := New(Deps{Snapshots: stubSource{}, Alerts: e})
	if err != nil {
		t.Fatal(err)
	}
	rec := serve(t, h, http.MethodGet, "/api/v1/alerts")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var body struct {
		Active []alert.Alert `json:"active"`
		Events []alert.Event `json:"events"`
		Rules  []struct {
			Name, Condition, For, ClearFor string
		} `json:"rules"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Active) != 1 || body.Active[0].State != alert.Firing || len(body.Events) != 1 {
		t.Errorf("active %+v, events %+v", body.Active, body.Events)
	}
	if len(body.Rules) != 1 || body.Rules[0].Condition != "> 75" || body.Rules[0].For != "0s" {
		t.Errorf("rules %+v", body.Rules)
	}

	off, _ := New(Deps{Snapshots: stubSource{}})
	if rec := serve(t, off, http.MethodGet, "/api/v1/alerts"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("without alerts: %d", rec.Code)
	}
}
