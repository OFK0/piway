// Package notify decides which alerts and deploys become push
// notifications (docs/ALERTS.md), and what they say.
//
// Deploys get one tag per repository branch, so on a device the newest
// notification about a branch replaces the older one: a success replaces
// the failure before it.
package notify

import (
	"fmt"
	"strings"
	"time"

	"github.com/ofk0/piway/internal/alert"
	"github.com/ofk0/piway/internal/deploy"
	"github.com/ofk0/piway/internal/push"
)

// Mode is which deploys are notified (setting notify_deploys).
type Mode string

const (
	All      Mode = "all"      // every finished deploy, rollback and restart, and failures
	Failures Mode = "failures" // failures, and the next success after one
	Off      Mode = "off"      // none; the alert is still listed
)

// Modes are the valid values, for the config.
var Modes = []Mode{All, Failures, Off}

// Sender sends a notification. *push.Service implements it.
type Sender interface {
	Notify(push.Message)
}

// Notifier turns alert events and finished builds into notifications.
type Notifier struct {
	Mode   Mode
	Send   Sender
	AppURL func(app string) string // optional: where users reach an app
}

// Target is how deploy alerts and notifications name a repository branch.
func Target(repo, branch string) string { return strings.ToLower(repo) + "@" + branch }

func deployTag(target string) string { return "deploy:" + target }

// Alert handles an alert that fired or resolved.
func (n *Notifier) Alert(ev alert.Event) {
	if ev.Rule != alert.DeployRule {
		m := push.Message{Title: ev.Rule, Body: ev.Summary, Tag: "alert:" + ev.Rule + ":" + ev.Series, URL: "/", Urgent: true}
		if ev.State == alert.Resolved {
			m.Title, m.Urgent = "Resolved: "+ev.Rule, false
		}
		n.Send.Notify(m)
		return
	}
	target := strings.TrimPrefix(ev.Series, "deploy:")
	switch {
	case n.Mode == Off:
	case ev.State != alert.Resolved:
		n.Send.Notify(push.Message{Title: ev.Rule, Body: ev.Summary, Tag: deployTag(target), URL: "/apps", Urgent: true})
	case n.Mode == Failures:
		// In All mode, Build's success notification says this instead.
		n.Send.Notify(push.Message{Title: "Resolved: " + ev.Rule, Body: ev.Summary, Tag: deployTag(target), URL: "/apps"})
	}
}

// Build handles a finished build: in All mode, a success is notified.
// Failures arrive as alerts.
func (n *Notifier) Build(b deploy.Build) {
	if n.Mode != All || b.State != deploy.Succeeded {
		return
	}
	verb := "Deployed"
	switch b.Kind {
	case deploy.KindRollback:
		verb = "Rolled back"
	case deploy.KindRestart:
		verb = "Restarted"
	}
	name := b.App
	if name == "" {
		name = b.Repo
	}
	parts := []string{"commit " + short(b.Commit)}
	if !b.Started.IsZero() && b.Finished.After(b.Started) {
		parts = append(parts, "in "+duration(b.Finished.Sub(b.Started)))
	}
	if n.AppURL != nil && b.App != "" {
		if u := n.AppURL(b.App); u != "" {
			parts = append(parts, strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://"))
		}
	}
	n.Send.Notify(push.Message{
		Title: verb + " " + name,
		Body:  strings.Join(parts, " · "),
		Tag:   deployTag(Target(b.Repo, b.Branch)),
		URL:   "/builds/" + b.ID,
	})
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// duration reads "42 s" or "2 min 5 s".
func duration(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	if s < 60 {
		return fmt.Sprintf("%d s", s)
	}
	if s%60 == 0 {
		return fmt.Sprintf("%d min", s/60)
	}
	return fmt.Sprintf("%d min %d s", s/60, s%60)
}
