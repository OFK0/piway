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
	// busybox's default command exits at once, and the unless-stopped
	// restart policy keeps starting it again: proof that it ran.
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, err := c.Inspect(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !st.StartedAt.IsZero() {
			t.Logf("container started (status %s)", st.Status)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("container never started (status %s)", st.Status)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Stop must end the restart loop: that is what "unless stopped" means.
	if err := c.Stop(ctx, id, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	if st, err := c.Inspect(ctx, id); err != nil || st.Running || st.Status != "exited" {
		t.Fatalf("after Stop: %+v, %v; want exited and not restarted", st, err)
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
