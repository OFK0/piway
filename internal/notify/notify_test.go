package notify

import (
	"strings"
	"testing"
	"time"

	"github.com/ofk0/piway/internal/alert"
	"github.com/ofk0/piway/internal/deploy"
	"github.com/ofk0/piway/internal/push"
)

type sent struct{ msgs []push.Message }

func (s *sent) Notify(m push.Message) { s.msgs = append(s.msgs, m) }

func (s *sent) titles() string {
	var t []string
	for _, m := range s.msgs {
		t = append(t, m.Title)
	}
	return strings.Join(t, " | ")
}

var t0 = time.Date(2026, 10, 15, 22, 6, 11, 0, time.UTC)

func build(state deploy.State, kind string) deploy.Build {
	return deploy.Build{ID: "20261015T220611Z-740f82f", App: "pinboard", Repo: "OFK0/pinboard", Branch: "main",
		Commit: "740f82f4f6fc9d7258ea34236dc572cb6c534720", Kind: kind, State: state, Started: t0, Finished: t0.Add(66 * time.Second)}
}

// episode runs a failed deploy and then a successful one through the
// alert engine, as main wires them.
func episode(mode Mode) *sent {
	s := &sent{}
	n := &Notifier{Mode: mode, Send: s, AppURL: func(app string) string { return "https://" + app + ".local:8443" }}
	e := alert.New(alert.Config{DeployFailures: true})
	e.OnEvent(n.Alert)
	finish := func(b deploy.Build) {
		e.DeployFinished(Target(b.Repo, b.Branch), b.App, b.State == deploy.Failed, b.Error, b.Finished)
		n.Build(b)
	}
	finish(build(deploy.Succeeded, ""))
	failed := build(deploy.Failed, "")
	failed.Error = "build: exit status 1"
	finish(failed)
	finish(build(deploy.Succeeded, ""))
	return s
}

func TestAllMode(t *testing.T) {
	s := episode(All)
	if got := s.titles(); got != "Deployed pinboard | Deploy failed | Deployed pinboard" {
		t.Fatalf("notifications: %s", got)
	}
	ok := s.msgs[0]
	if ok.Body != "commit 740f82f · in 1 min 6 s · pinboard.local:8443" || ok.URL != "/builds/20261015T220611Z-740f82f" || ok.Urgent {
		t.Errorf("success = %+v", ok)
	}
	if fail := s.msgs[1]; !fail.Urgent || !strings.Contains(fail.Body, "exit status 1") || fail.URL != "/apps" {
		t.Errorf("failure = %+v", fail)
	}
	// One tag per branch: each replaces the one before on the device.
	for _, m := range s.msgs {
		if m.Tag != "deploy:ofk0/pinboard@main" {
			t.Errorf("tag %q", m.Tag)
		}
	}
}

func TestFailuresMode(t *testing.T) {
	s := episode(Failures)
	if got := s.titles(); got != "Deploy failed | Resolved: Deploy failed" {
		t.Errorf("notifications: %s", got)
	}
}

func TestOffMode(t *testing.T) {
	if s := episode(Off); len(s.msgs) != 0 {
		t.Errorf("notifications: %s", s.titles())
	}
}

func TestRollbackRestartAndCancelled(t *testing.T) {
	s := &sent{}
	n := &Notifier{Mode: All, Send: s}
	n.Build(build(deploy.Succeeded, deploy.KindRollback))
	n.Build(build(deploy.Succeeded, deploy.KindRestart))
	n.Build(build(deploy.Cancelled, ""))
	if got := s.titles(); got != "Rolled back pinboard | Restarted pinboard" {
		t.Errorf("notifications: %s", got)
	}
	if s.msgs[0].Body != "commit 740f82f · in 1 min 6 s" {
		t.Errorf("body without an app URL: %q", s.msgs[0].Body)
	}
}

func TestOtherAlertsAreUnchanged(t *testing.T) {
	for _, mode := range Modes {
		s := &sent{}
		n := &Notifier{Mode: mode, Send: s}
		n.Alert(alert.Event{Alert: alert.Alert{Rule: "CPU is hot", Series: "temperature_celsius", Summary: "CPU is hot: 80 °C (> 75 °C)", State: alert.Firing}})
		n.Alert(alert.Event{Alert: alert.Alert{Rule: "CPU is hot", Series: "temperature_celsius", Summary: "CPU is hot: 70 °C (> 75 °C)", State: alert.Resolved}})
		if got := s.titles(); got != "CPU is hot | Resolved: CPU is hot" || !s.msgs[0].Urgent || s.msgs[1].Urgent {
			t.Errorf("%s: %s", mode, got)
		}
	}
}

func TestDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{42 * time.Second: "42 s", 2 * time.Minute: "2 min", 125 * time.Second: "2 min 5 s"} {
		if got := duration(d); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
}
