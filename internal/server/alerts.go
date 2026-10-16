package server

import (
	"net/http"

	"github.com/ofk0/piway/internal/alert"
)

// Alerts reports the alert engine's state. *alert.Engine implements it.
type Alerts interface {
	Active() []alert.Alert
	Events() []alert.Event
	Rules() alert.Config
}

// ruleBody is a rule as the API shows it.
type ruleBody struct {
	Name      string `json:"name"`
	Metric    string `json:"metric"`
	Condition string `json:"condition"`
	For       string `json:"for"`
	ClearFor  string `json:"clear_for"`
}

func alerts(a Alerts) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if a == nil {
			writeError(w, http.StatusServiceUnavailable, "alerts are off")
			return
		}
		cfg := a.Rules()
		rules := make([]ruleBody, 0, len(cfg.Rules))
		for _, r := range cfg.Rules {
			rules = append(rules, ruleBody{r.Name, r.Metric, r.Condition(), r.For.String(), r.ClearFor.String()})
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]any{
			"active":          a.Active(),
			"events":          a.Events(),
			"rules":           rules,
			"deploy_failures": cfg.DeployFailures,
			"app_crashes":     cfg.AppCrashes,
		})
	}
}
