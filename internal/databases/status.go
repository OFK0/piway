package databases

import (
	"context"
	"errors"
	"time"

	"github.com/ofk0/piway/internal/docker"
)

// EngineStatus is one engine as the Databases page shows it.
type EngineStatus struct {
	Name string `json:"name"`
	// State is "not_started" before any app asked for the engine, else
	// the container's state: running, exited, restarting, ...
	State  string `json:"state"`
	Image  string `json:"image,omitempty"` // what runs
	Pinned string `json:"pinned"`          // what this piway starts new servers from
	// UpgradeAvailable: Pinned is another major version than Image
	// (piway db upgrade).
	UpgradeAvailable bool   `json:"upgrade_available"`
	Volume           string `json:"volume,omitempty"`
	Size             int64  `json:"size_bytes"`   // the volume; -1 when unknown
	Memory           int64  `json:"memory_bytes"` // in use now; -1 when unknown
	MemoryLimit      int64  `json:"memory_limit_bytes"`
	Apps             []App  `json:"apps"`
}

// App is an app with a database on an engine.
type App struct {
	Name       string     `json:"name"`
	Backups    int        `json:"backups"`
	LastBackup *time.Time `json:"last_backup,omitempty"`
	BackupSize int64      `json:"last_backup_bytes,omitempty"`
}

// Status reports every engine, started or not.
func (m *Manager) Status(ctx context.Context) ([]EngineStatus, error) {
	volumes, err := m.Docker.Volumes(ctx, LabelEngine)
	if err != nil {
		return nil, err
	}
	var out []EngineStatus
	for _, name := range Names() {
		e := engines[name]
		st := EngineStatus{Name: name, State: "not_started", Pinned: m.image(e), Size: -1, Memory: -1, MemoryLimit: e.memory, Apps: []App{}}
		d, err := m.Docker.Details(ctx, containerName(name))
		switch {
		case err == nil:
			st.State, st.Image = d.State.Status, d.Image
			st.UpgradeAvailable = major(d.Image) != major(st.Pinned)
			st.Volume = volumeName(name, d.Image)
			for _, v := range volumes {
				if v.Name == st.Volume && v.UsageData != nil {
					st.Size = v.UsageData.Size
				}
			}
			if d.State.Running {
				if used, err := m.Docker.MemoryUsage(ctx, d.ID); err == nil {
					st.Memory = used
				}
			}
		case !errors.Is(err, docker.ErrNotFound):
			return nil, err
		}
		apps, err := m.Apps(name)
		if err != nil {
			return nil, err
		}
		for _, app := range apps {
			a := App{Name: app}
			if bs, err := m.Backups(name, app); err == nil && len(bs) > 0 {
				a.Backups = len(bs)
				t := bs[0].Time
				a.LastBackup, a.BackupSize = &t, bs[0].Size
			}
			st.Apps = append(st.Apps, a)
		}
		out = append(out, st)
	}
	return out, nil
}
