package deploy

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/ofk0/piway/internal/manifest"
	"github.com/ofk0/piway/internal/secrets"
)

// AppEnv is an app's environment as its page shows it: run.env and the
// secrets from its live build's piway.yaml.
type AppEnv struct {
	App    string `json:"app"`
	Build  string `json:"build"`  // the live build whose piway.yaml this is
	Static bool   `json:"static"` // a static site has no process and no environment
	// Env is run.env, as written in the repository.
	Env []EnvVar `json:"env"`
	// Secrets are run.secrets, then names in the secrets file that
	// piway.yaml does not declare (Declared false; the app does not get them).
	Secrets []Secret `json:"secrets"`
	// Injected are variables piway sets itself, read-only.
	Injected []EnvVar `json:"injected"`
	// RestartNeeded: the running container was started with other
	// secret values than the ones stored now.
	RestartNeeded bool   `json:"restart_needed"`
	File          string `json:"file"` // where the secret values live
	// ValuesShown says whether secret values are included; they are only
	// for a signed-in browser.
	ValuesShown bool `json:"values_shown"`
}

// EnvVar is a variable with its value.
type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Secret is one of an app's secrets. Value is present only when values
// are shown and it is set.
type Secret struct {
	Name     string  `json:"name"`
	Set      bool    `json:"set"`
	Declared bool    `json:"declared"`
	Value    *string `json:"value,omitempty"`
}

// ErrNoSecrets means secret values cannot be stored (no secrets store).
var ErrNoSecrets = errors.New("deploy: no secrets store")

// Env returns app's environment. Secret values are included only with
// values set.
func (p *Pipeline) Env(ctx context.Context, app string, values bool) (AppEnv, error) {
	m, live, err := p.liveManifest(app)
	if err != nil {
		return AppEnv{}, err
	}
	out := AppEnv{App: app, Build: live.ID, Env: []EnvVar{}, Secrets: []Secret{}, Injected: []EnvVar{}, ValuesShown: values}
	if m.Static != nil {
		out.Static = true
		return out, nil
	}
	for _, v := range m.Run.Env {
		out.Env = append(out.Env, EnvVar{Name: v.Name, Value: v.Value})
	}
	if p.opts.Secrets == nil {
		for _, name := range m.Run.Secrets {
			out.Secrets = append(out.Secrets, Secret{Name: name, Declared: true})
		}
		return out, nil
	}
	out.File = p.opts.Secrets.Path(app)
	stored, err := p.opts.Secrets.Values(app)
	if err != nil {
		return AppEnv{}, err
	}
	add := func(name string, declared bool) {
		s := Secret{Name: name, Declared: declared}
		if v, ok := stored[name]; ok {
			s.Set = true
			if values {
				s.Value = &v
			}
		}
		out.Secrets = append(out.Secrets, s)
	}
	for _, name := range m.Run.Secrets {
		add(name, true)
	}
	var extra []string
	for name := range stored {
		if !slices.Contains(m.Run.Secrets, name) {
			extra = append(extra, name)
		}
	}
	slices.Sort(extra)
	for _, name := range extra {
		add(name, false)
	}

	// Containers started before piway labelled them have no digest; they
	// are not reported, as nothing is known about them.
	if len(m.Run.Secrets) > 0 && p.opts.Runtime != nil {
		if cs, err := p.opts.Runtime.Containers(ctx); err == nil {
			if c, ok := cs[app]; ok && c.SecretsDigest != "" {
				out.RestartNeeded = c.SecretsDigest != secrets.Digest(m.Run.Secrets, stored)
			}
		}
	}
	return out, nil
}

// SetSecret stores a value for one of app's secrets. It applies on the
// next deploy or restart.
func (p *Pipeline) SetSecret(app, name, value string) error {
	if _, _, err := p.liveManifest(app); err != nil {
		return err
	}
	if p.opts.Secrets == nil {
		return ErrNoSecrets
	}
	return p.opts.Secrets.Set(app, name, value)
}

// RemoveSecret deletes the value of one of app's secrets.
func (p *Pipeline) RemoveSecret(app, name string) error {
	if _, _, err := p.liveManifest(app); err != nil {
		return err
	}
	if p.opts.Secrets == nil {
		return ErrNoSecrets
	}
	return p.opts.Secrets.Remove(app, name)
}

// liveManifest is the piway.yaml of what app runs now. A rollback or a
// restart runs an earlier build's, which is kept with that build.
func (p *Pipeline) liveManifest(app string) (*manifest.Manifest, Build, error) {
	p.mu.Lock()
	live := p.liveLocked(app)
	var b Build
	if live != nil {
		b = *live
	}
	p.mu.Unlock()
	if live == nil {
		return nil, Build{}, ErrNotFound
	}
	id := b.ID
	if b.released() {
		id = b.RollbackOf
	}
	m, err := manifest.Load(p.store.manifestPath(id))
	if err != nil {
		return nil, Build{}, fmt.Errorf("piway.yaml of build %s: %w", id, err)
	}
	return m, b, nil
}
