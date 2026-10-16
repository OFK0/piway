package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ofk0/piway/internal/docker"
	"github.com/ofk0/piway/internal/router"
)

// fakeContainers serves the two Docker endpoints Reconcile uses.
type fakeContainers struct {
	mu   sync.Mutex
	list []fakeCtr
}

type fakeCtr struct {
	id, build string
	port      int
}

func (f *fakeContainers) set(cs ...fakeCtr) {
	f.mu.Lock()
	f.list = cs
	f.mu.Unlock()
}

func startFakeDocker(t *testing.T, f *fakeContainers) *docker.Client {
	t.Helper()
	dir, err := os.MkdirTemp("", "dk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{v}/containers/json", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var out []map[string]any
		for _, c := range f.list {
			out = append(out, map[string]any{"Id": c.id, "State": "running", "Labels": map[string]string{docker.LabelApp: "web", "piway.build": c.build}})
		}
		json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("GET /{v}/containers/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, c := range f.list {
			if c.id == r.PathValue("id") {
				fmt.Fprintf(w, `{"Id":%q,"Name":"/%s","State":{"Status":"running","Running":true},"Config":{"Labels":{"piway.app":"web","piway.build":%q}},"NetworkSettings":{"Ports":{"80/tcp":[{"HostIp":"127.0.0.1","HostPort":"%d"}]}}}`, c.id, c.id, c.build, c.port)
				return
			}
		}
		http.NotFound(w, r)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return docker.New(sock)
}

func TestReconcile(t *testing.T) {
	f := &fakeContainers{}
	routes := router.New(slog.New(slog.DiscardHandler))
	t.Cleanup(func() { routes.Close() })
	s := ExecSteps{Docker: startFakeDocker(t, f), Router: routes}
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	live := func(string) string { return "A" }
	target := func(port int) string { return fmt.Sprintf("http://127.0.0.1:%d", port) }

	// Mid-release: the route already points at build B's container, while
	// A still counts as live. Reconcile must not move it back (the bug in
	// the first version of this fix).
	f.set(fakeCtr{"old", "A", 41001}, fakeCtr{"new", "B", 41002})
	if err := routes.Set("web", HostPort("web"), target(41002)); err != nil {
		t.Fatal(err)
	}
	s.Reconcile(ctx, live, "", log)
	if got := routes.Target("web"); got != target(41002) {
		t.Fatalf("mid-release: route moved to %s", got)
	}

	// Docker restarted the live container on a new port; the route points
	// at a port nothing has: Reconcile follows the live container.
	f.set(fakeCtr{"old", "A", 41003})
	routes.Set("web", HostPort("web"), target(41001))
	s.Reconcile(ctx, live, "", log)
	if got := routes.Target("web"); got != target(41003) {
		t.Errorf("after a restart: route %s, want %s", got, target(41003))
	}

	// Nothing to do: the route is right.
	s.Reconcile(ctx, live, "web", log)
	if got := routes.Target("web"); got != target(41003) {
		t.Errorf("a correct route changed to %s", got)
	}

	// The live container is not running (being restarted): left alone.
	f.set()
	s.Reconcile(ctx, live, "", log)
	if got := routes.Target("web"); !strings.HasSuffix(got, "41003") {
		t.Errorf("with no container, route changed to %s", got)
	}
}
