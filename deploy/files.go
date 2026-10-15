// Package deploy holds the files piway installs on a host, so `piway setup`
// writes exactly the files the docs describe and the release ships.
package deploy

import _ "embed"

var (
	// PiwayService is the systemd user unit for piway.
	//go:embed systemd/piway.service
	PiwayService []byte

	// CaddyService is the systemd user unit for Caddy (docs/PROXY.md).
	//go:embed systemd/caddy.service
	CaddyService []byte

	// ConfigExample is the commented config file with every default.
	//go:embed config.example.yaml
	ConfigExample []byte
)
