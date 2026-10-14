package docker

import (
	"os"
	"testing"
	"time"
)

// TestRealDaemon runs a container lifecycle against the local daemon.
// It is skipped unless PIWAY_DOCKER_TEST=1, because it pulls an image and
// needs Docker installed (docs/DOCKER.md):
//
//	PIWAY_DOCKER_TEST=1 go test ./internal/docker -run RealDaemon -v
func TestRealDaemon(t *testing.T) {
	if os.Getenv("PIWAY_DOCKER_TEST") != "1" {
		t.Skip("set PIWAY_DOCKER_TEST=1 to run against the local Docker daemon")
	}
	path, err := SocketPath(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if !Available(path) {
		t.Fatalf("no Docker socket at %s; see docs/DOCKER.md", path)
	}
	c := New(path)

	v, err := c.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Docker %s (API %s) on %s/%s", v.Version, v.APIVersion, v.OS, v.Arch)

	const image = "busybox:1.37"
	if err := c.Pull(ctx, image); err != nil {
		t.Fatal(err)
	}
	name := "piway-test-" + time.Now().Format("150405")
	id, err := c.Create(ctx, name, "piway-test", Spec{Image: image, Env: []string{"A=1"}, CPUs: 0.25})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Remove(ctx, id, true) })

	if err := c.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	// busybox's default command exits at once; the container still ran.
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, err := c.Inspect(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !st.Running && !st.FinishedAt.IsZero() {
			t.Logf("container ran and exited with %d", st.ExitCode)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("container still %s after 10s", st.Status)
		}
		time.Sleep(200 * time.Millisecond)
	}

	list, err := c.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ct := range list {
		found = found || ct.ID == id
	}
	if !found {
		t.Errorf("List does not include %s", id)
	}
	if err := c.Remove(ctx, id, true); err != nil {
		t.Fatal(err)
	}
}
