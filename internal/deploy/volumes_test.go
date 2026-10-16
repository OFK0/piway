package deploy

import (
	"context"
	"errors"
	"testing"

	"github.com/ofk0/piway/internal/docker"
)

const volumeManifest = `piway: 1
name: guestbook
build:
  dockerfile: Dockerfile
run:
  port: 3000
  volumes:
    - data:/data
    - cache:/cache
`

// volumeRuntime is a Runtime with volumes.
type volumeRuntime struct {
	fakeRuntime
	volumes []docker.Volume
	removed []string
}

func (v *volumeRuntime) AppVolumes(_ context.Context, app string) ([]docker.Volume, error) {
	var out []docker.Volume
	for _, d := range v.volumes {
		if d.Labels[docker.LabelApp] == app {
			out = append(out, d)
		}
	}
	return out, nil
}

func (v *volumeRuntime) RemoveAppVolume(_ context.Context, app, volume string) error {
	v.removed = append(v.removed, VolumeName(app, volume))
	return nil
}

func dockerVolume(app, name string, size, refs int64) docker.Volume {
	d := docker.Volume{Name: VolumeName(app, name), Labels: map[string]string{docker.LabelApp: app, LabelVolume: name}}
	d.UsageData = &struct {
		Size     int64 `json:"Size"`
		RefCount int64 `json:"RefCount"`
	}{size, refs}
	return d
}

func TestVolumes(t *testing.T) {
	ctx := context.Background()
	rt := &volumeRuntime{fakeRuntime: fakeRuntime{}, volumes: []docker.Volume{
		dockerVolume("guestbook", "data", 12345, 1),
		dockerVolume("guestbook", "old", 99, 0), // from an earlier piway.yaml
		dockerVolume("blog", "data", 1, 1),
	}}
	h := start(t, t.TempDir(), Options{Runtime: rt})
	h.steps.mu.Lock()
	h.steps.manifest = volumeManifest
	h.steps.mu.Unlock()

	if _, err := h.p.Volumes(ctx, "guestbook"); !errors.Is(err, ErrNotFound) {
		t.Errorf("before any build: %v", err)
	}
	id := push(t, h.p, "aaaaaaaaaaaa1")
	started(t, h.steps)
	h.steps.release <- nil
	waitState(t, h.p, id, Succeeded)

	vs, err := h.p.Volumes(ctx, "guestbook")
	if err != nil {
		t.Fatal(err)
	}
	want := []AppVolume{
		{Name: "data", Path: "/data", Docker: "piway-guestbook_data", Declared: true, Created: true, Size: 12345, InUse: true},
		{Name: "cache", Path: "/cache", Docker: "piway-guestbook_cache", Declared: true, Size: -1},
		{Name: "old", Docker: "piway-guestbook_old", Created: true, Size: 99},
	}
	if len(vs) != len(want) {
		t.Fatalf("volumes = %+v", vs)
	}
	for i := range want {
		if vs[i] != want[i] {
			t.Errorf("volume %d = %+v, want %+v", i, vs[i], want[i])
		}
	}

	if err := h.p.RemoveVolume(ctx, "guestbook", "data"); !errors.Is(err, ErrVolumeDeclared) {
		t.Errorf("removing a declared volume: %v", err)
	}
	if err := h.p.RemoveVolume(ctx, "guestbook", "old"); err != nil || len(rt.removed) != 1 || rt.removed[0] != "piway-guestbook_old" {
		t.Errorf("remove old: %v, %v", err, rt.removed)
	}
}

func TestVolumeName(t *testing.T) {
	// App names have no "_", so these cannot collide.
	if VolumeName("a-b", "c") == VolumeName("a", "b-c") {
		t.Error("volume names collide")
	}
}
