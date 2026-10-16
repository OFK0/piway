package deploy

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ofk0/piway/internal/docker"
	"github.com/ofk0/piway/internal/manifest"
)

// VolumeStore lists and deletes apps' volumes. ExecSteps implements it.
type VolumeStore interface {
	AppVolumes(ctx context.Context, app string) ([]docker.Volume, error)
	RemoveAppVolume(ctx context.Context, app, volume string) error
}

// AppVolume is one of an app's volumes as its page shows it.
type AppVolume struct {
	Name   string `json:"name"`
	Path   string `json:"path,omitempty"` // where the live version mounts it
	Docker string `json:"docker"`         // the Docker volume
	// Declared: the live version's run.volumes lists it. An undeclared
	// volume is kept, with its data, until it is deleted.
	Declared bool `json:"declared"`
	// Created: false for a declared volume no release has made yet.
	Created bool  `json:"created"`
	Size    int64 `json:"size_bytes"` // -1 when unknown
	InUse   bool  `json:"in_use"`
}

// ErrVolumeDeclared means a volume cannot be deleted while the app uses it.
var ErrVolumeDeclared = errors.New("deploy: the app's piway.yaml declares this volume")

// Volumes lists app's volumes: those its live version declares, then
// those left from earlier versions.
func (p *Pipeline) Volumes(ctx context.Context, app string) ([]AppVolume, error) {
	m, _, err := p.liveManifest(app)
	if err != nil {
		return nil, err
	}
	out := []AppVolume{}
	if m.Run != nil {
		for _, v := range m.Run.Volumes {
			out = append(out, AppVolume{Name: v.Name, Path: v.Path, Docker: VolumeName(app, v.Name), Declared: true, Size: -1})
		}
	}
	vs, ok := p.opts.Runtime.(VolumeStore)
	if !ok {
		return out, nil
	}
	existing, err := vs.AppVolumes(ctx, app)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(existing, func(a, b docker.Volume) int { return strings.Compare(a.Name, b.Name) })
	for _, d := range existing {
		name := d.Labels[LabelVolume]
		i := slices.IndexFunc(out, func(v AppVolume) bool { return v.Name == name })
		if i < 0 {
			out = append(out, AppVolume{Name: name, Docker: d.Name, Size: -1})
			i = len(out) - 1
		}
		out[i].Created = true
		if d.UsageData != nil {
			out[i].Size, out[i].InUse = d.UsageData.Size, d.UsageData.RefCount > 0
		}
	}
	return out, nil
}

// RemoveVolume deletes one of app's volumes and its data. A volume the
// live version declares cannot be deleted; nor can one a container uses.
func (p *Pipeline) RemoveVolume(ctx context.Context, app, volume string) error {
	m, _, err := p.liveManifest(app)
	if err != nil {
		return err
	}
	if m.Run != nil && slices.ContainsFunc(m.Run.Volumes, func(v manifest.Volume) bool { return v.Name == volume }) {
		return fmt.Errorf("%w: remove it from run.volumes and deploy first", ErrVolumeDeclared)
	}
	vs, ok := p.opts.Runtime.(VolumeStore)
	if !ok {
		return fmt.Errorf("%w: no container runtime", ErrNotRunning)
	}
	return vs.RemoveAppVolume(ctx, app, volume)
}
