package setup

import (
	"context"
	"path/filepath"
)

// UninstallPlan removes what setup added to the user's account: the
// services, the binaries and, with purge, the configuration and the
// metrics history. System packages (Docker, avahi) and settings made with
// sudo stay; the last step lists how to remove them.
func UninstallPlan(ctx context.Context, h Host, env Env, purge bool) []Step {
	var steps []Step
	unitDir := filepath.Join(env.Home, ".config", "systemd", "user")
	bin := filepath.Join(env.Home, ".local", "bin")
	exists := func(p string) bool { _, err := h.Stat(p); return err == nil }

	for _, svc := range []string{"piway", "caddy"} {
		unit := filepath.Join(unitDir, svc+".service")
		if !exists(unit) {
			continue
		}
		steps = append(steps, Step{ID: "remove-" + svc, Title: "Stop and remove the " + svc + " service",
			Actions: []Action{
				{Argv: []string{"systemctl", "--user", "disable", "--now", svc}},
				{Argv: []string{"rm", "-f", unit}},
				{Argv: []string{"systemctl", "--user", "daemon-reload"}},
			}})
	}

	var rm []Action
	for _, b := range []string{"piway", "caddy"} {
		if p := filepath.Join(bin, b); exists(p) {
			rm = append(rm, Action{Argv: []string{"rm", "-f", p}})
		}
	}
	if len(rm) > 0 {
		steps = append(steps, Step{ID: "remove-binaries", Title: "Remove the binaries", Actions: rm})
	}

	if purge {
		var del []Action
		for _, p := range []string{filepath.Join(env.Home, ".config", "piway"), env.Config.DataDir} {
			if p != "" && exists(p) {
				del = append(del, Action{Argv: []string{"rm", "-rf", p}})
			}
		}
		if len(del) > 0 {
			steps = append(steps, Step{ID: "purge", Title: "Delete the configuration, password, tokens and metrics history", Actions: del})
		}
	}

	manual := "Left in place, since other programs may use them:\n" +
		"rootless Docker and its images: systemctl --user disable --now docker; dockerd-rootless-setuptool.sh uninstall\n" +
		"Docker packages: sudo apt-get purge docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-ce-rootless-extras\n" +
		"lingering: sudo loginctl disable-linger " + env.User
	if !purge {
		manual = "Your configuration and metrics history are kept (piway uninstall --purge deletes them).\n" + manual
	}
	steps = append(steps, Step{ID: "uninstall-note", Title: "Note", Manual: manual})
	return steps
}
