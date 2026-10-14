package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDaemon is an in-memory Docker Engine serving the API subset the client
// uses, on a real unix socket so the client's transport is exercised too.
type fakeDaemon struct {
	mu         sync.Mutex
	containers map[string]*fakeContainer // by id
	lastCreate createBody
	nextID     int
}

type fakeContainer struct {
	id, name, image, state string
	labels                 map[string]string
}

func startFake(t *testing.T) (*Client, *fakeDaemon) {
	t.Helper()
	// Unix socket paths are limited to ~108 bytes; t.TempDir can be longer.
	dir, err := os.MkdirTemp("", "dk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "docker.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeDaemon{containers: map[string]*fakeContainer{}}
	srv := &http.Server{Handler: f.routes()}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return New(sock), f
}

func (f *fakeDaemon) routes() http.Handler {
	mux := http.NewServeMux()
	v := "/v" + APIVersion
	mux.HandleFunc("GET "+v+"/version", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Version{Version: "26.1.5", APIVersion: "1.45", Arch: "arm64", OS: "linux"})
	})
	mux.HandleFunc("GET "+v+"/info", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"MemoryLimit": false, "CpuCfsQuota": true, "Driver": "overlay2"})
	})
	mux.HandleFunc("POST "+v+"/containers/create", func(w http.ResponseWriter, r *http.Request) {
		var body createBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		name := r.URL.Query().Get("name")
		for _, c := range f.containers {
			if c.name == name {
				writeMsg(w, 409, fmt.Sprintf("Conflict. The container name %q is already in use", "/"+name))
				return
			}
		}
		f.nextID++
		id := fmt.Sprintf("c%04d", f.nextID)
		f.containers[id] = &fakeContainer{id: id, name: name, image: body.Image, state: "created", labels: body.Labels}
		f.lastCreate = body
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]string{"Id": id})
	})
	mux.HandleFunc("POST "+v+"/containers/{id}/start", f.transition("running"))
	mux.HandleFunc("POST "+v+"/containers/{id}/stop", f.transition("exited"))
	mux.HandleFunc("DELETE "+v+"/containers/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		c, ok := f.containers[r.PathValue("id")]
		if !ok {
			writeMsg(w, 404, "No such container: "+r.PathValue("id"))
			return
		}
		if c.state == "running" && r.URL.Query().Get("force") != "true" {
			writeMsg(w, 409, "You cannot remove a running container")
			return
		}
		delete(f.containers, c.id)
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET "+v+"/containers/json", func(w http.ResponseWriter, r *http.Request) {
		var filters map[string][]string
		json.Unmarshal([]byte(r.URL.Query().Get("filters")), &filters)
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []Container{}
		for _, c := range f.containers {
			if label := filters["label"]; len(label) > 0 {
				if _, ok := c.labels[label[0]]; !ok {
					continue
				}
			}
			if c.state != "running" && r.URL.Query().Get("all") != "true" {
				continue
			}
			out = append(out, Container{ID: c.id, Names: []string{"/" + c.name}, Image: c.image, State: c.state, Labels: c.labels})
		}
		json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("GET "+v+"/containers/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		c, ok := f.containers[r.PathValue("id")]
		if !ok {
			writeMsg(w, 404, "No such container")
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"State": map[string]any{"Status": c.state, "Running": c.state == "running"}})
	})
	mux.HandleFunc("POST "+v+"/images/create", func(w http.ResponseWriter, r *http.Request) {
		// Like the daemon: 200 first, progress lines, errors inside the stream.
		w.WriteHeader(200)
		fmt.Fprintln(w, `{"status":"Pulling from library/`+r.URL.Query().Get("fromImage")+`","id":"`+r.URL.Query().Get("tag")+`"}`)
		if strings.HasPrefix(r.URL.Query().Get("fromImage"), "missing") {
			fmt.Fprintln(w, `{"errorDetail":{"message":"manifest unknown"},"error":"manifest unknown"}`)
			return
		}
		fmt.Fprintln(w, `{"status":"Status: Downloaded newer image"}`)
	})
	return mux
}

// transition moves a container to state; it answers 304 if it already is,
// as the daemon does for start-while-running and stop-while-stopped.
func (f *fakeDaemon) transition(state string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		c, ok := f.containers[r.PathValue("id")]
		if !ok {
			writeMsg(w, 404, "No such container: "+r.PathValue("id"))
			return
		}
		if c.state == state {
			w.WriteHeader(304)
			return
		}
		c.state = state
		w.WriteHeader(204)
	}
}

func writeMsg(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"message": msg})
}

var ctx = context.Background()

func TestVersion(t *testing.T) {
	c, _ := startFake(t)
	v, err := c.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v.Version != "26.1.5" || v.Arch != "arm64" {
		t.Errorf("version = %+v", v)
	}
}

func TestContainerLifecycle(t *testing.T) {
	c, f := startFake(t)

	id, err := c.Create(ctx, "photos-1", "photos", Spec{
		Image:       "ghcr.io/example/photos:1.2",
		Env:         []string{"PORT=3000"},
		Port:        3000,
		HostPort:    41000,
		MemoryBytes: 256 << 20,
		CPUs:        0.5,
	})
	if err != nil {
		t.Fatal(err)
	}

	got := f.lastCreate
	if got.Labels[LabelApp] != "photos" {
		t.Errorf("labels = %v, want %s=photos", got.Labels, LabelApp)
	}
	b := got.HostConfig.PortBindings["3000/tcp"]
	if len(b) != 1 || b[0].HostIP != "127.0.0.1" || b[0].HostPort != "41000" {
		t.Errorf("port binding = %+v, want 127.0.0.1:41000 only", b)
	}
	if got.HostConfig.Memory != 256<<20 || got.HostConfig.NanoCPUs != 500_000_000 {
		t.Errorf("limits = %d bytes, %d nano-CPUs", got.HostConfig.Memory, got.HostConfig.NanoCPUs)
	}
	if got.HostConfig.RestartPolicy.Name != "unless-stopped" {
		t.Errorf("restart policy = %q", got.HostConfig.RestartPolicy.Name)
	}

	if err := c.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(ctx, id); err != nil {
		t.Errorf("starting a running container: %v, want no error (304)", err)
	}
	if st, err := c.Inspect(ctx, id); err != nil || !st.Running {
		t.Errorf("inspect after start = %+v, %v", st, err)
	}

	if err := c.Remove(ctx, id, false); !errors.Is(err, ErrConflict) {
		t.Errorf("removing a running container without force: %v, want ErrConflict", err)
	}
	if err := c.Stop(ctx, id, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(ctx, id, 10*time.Second); err != nil {
		t.Errorf("stopping a stopped container: %v, want no error (304)", err)
	}
	if err := c.Remove(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Inspect(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("inspect after remove: %v, want ErrNotFound", err)
	}
}

func TestCreateNameTaken(t *testing.T) {
	c, _ := startFake(t)
	if _, err := c.Create(ctx, "web", "web", Spec{Image: "nginx"}); err != nil {
		t.Fatal(err)
	}
	_, err := c.Create(ctx, "web", "web", Spec{Image: "nginx"})
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "already in use") {
		t.Errorf("err = %v, want ErrConflict with the daemon's message", err)
	}
}

func TestListOnlyPiwayContainers(t *testing.T) {
	c, f := startFake(t)
	if _, err := c.Create(ctx, "photos-1", "photos", Spec{Image: "a"}); err != nil {
		t.Fatal(err)
	}
	// A container someone else started on the same daemon.
	f.containers["other"] = &fakeContainer{id: "other", name: "db", state: "running", labels: map[string]string{}}

	list, err := c.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].App() != "photos" || list[0].State != "created" {
		t.Errorf("list = %+v, want only the stopped photos container", list)
	}
}

func TestPull(t *testing.T) {
	c, _ := startFake(t)
	if err := c.Pull(ctx, "nginx:1.27"); err != nil {
		t.Fatal(err)
	}
	err := c.Pull(ctx, "missing/app:latest")
	if err == nil || !strings.Contains(err.Error(), "manifest unknown") {
		t.Errorf("err = %v, want the error from inside the progress stream", err)
	}
}

func TestDaemonDown(t *testing.T) {
	c := New(filepath.Join(t.TempDir(), "nothing.sock"))
	if _, err := c.Version(ctx); err == nil {
		t.Fatal("no error without a daemon")
	}
	if Available(filepath.Join(t.TempDir(), "nothing.sock")) {
		t.Error("Available reports a missing socket")
	}
}

func TestSocketPath(t *testing.T) {
	tests := []struct {
		env  map[string]string
		want string
		err  bool
	}{
		{map[string]string{"DOCKER_HOST": "unix:///run/user/1000/docker.sock"}, "/run/user/1000/docker.sock", false},
		{map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}, "/run/user/1000/docker.sock", false},
		{map[string]string{}, "/var/run/docker.sock", false},
		{map[string]string{"DOCKER_HOST": "tcp://10.0.0.2:2375"}, "", true},
	}
	for _, tt := range tests {
		got, err := SocketPath(func(k string) string { return tt.env[k] })
		if got != tt.want || (err != nil) != tt.err {
			t.Errorf("env %v: got %q, %v", tt.env, got, err)
		}
	}
}

func TestInfo(t *testing.T) {
	c, _ := startFake(t)
	i, err := c.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if i.MemoryLimit || !i.CPUCfsQuota {
		t.Errorf("info = %+v", i)
	}
}
