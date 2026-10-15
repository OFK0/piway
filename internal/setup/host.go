package setup

import (
	"context"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/ofk0/piway/internal/docker"
)

// Host is everything the checks read from the machine. Checks never touch
// the system directly, so they are tested against a fake (host_test.go).
type Host interface {
	// Run runs a command and returns its output with surrounding space
	// trimmed. A non-zero exit is returned as an error, with the output.
	Run(ctx context.Context, name string, args ...string) (string, error)
	ReadFile(name string) ([]byte, error)
	Stat(name string) (fs.FileInfo, error)
	LookPath(file string) (string, error)
	Getenv(key string) string
	// HTTPStatus GETs url and returns the response status.
	HTTPStatus(ctx context.Context, url string) (int, error)
	// DockerInfo asks the daemon at socket what it supports; an error means
	// it is not reachable.
	DockerInfo(ctx context.Context, socket string) (docker.Info, error)
	// DiskFree is the space available to unprivileged users on path's
	// filesystem, in bytes.
	DiskFree(path string) (uint64, error)
	Arch() string
	Root() bool
}

// System is the real machine.
type System struct{}

var _ Host = System{}

func (System) Run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (System) ReadFile(name string) ([]byte, error)  { return os.ReadFile(name) }
func (System) Stat(name string) (fs.FileInfo, error) { return os.Stat(name) }
func (System) LookPath(file string) (string, error)  { return exec.LookPath(file) }
func (System) Getenv(key string) string              { return os.Getenv(key) }
func (System) Arch() string                          { return runtime.GOARCH }
func (System) Root() bool                            { return os.Geteuid() == 0 }

func (System) HTTPStatus(ctx context.Context, url string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

func (System) DockerInfo(ctx context.Context, socket string) (docker.Info, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return docker.New(socket).Info(ctx)
}

func (System) DiskFree(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}
