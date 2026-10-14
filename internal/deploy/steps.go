package deploy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ofk0/piway/internal/docker"
	"github.com/ofk0/piway/internal/manifest"
)

// ExecSteps runs builds with git, the docker CLI (BuildKit) and the Docker
// Engine API.
type ExecSteps struct {
	Docker     *docker.Client
	DockerHost string // unix://… for the docker CLI
	SecretsDir string // <app>.env files with the values of run.secrets
}

// command runs name with args in dir, streaming stdout and stderr to out.
// Cancelling ctx kills it.
func (s ExecSteps) command(ctx context.Context, out io.Writer, dir, name string, args ...string) error {
	fmt.Fprintf(out, "$ %s %s\n", name, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Env = append(os.Environ(),
		"DOCKER_HOST="+s.DockerHost,
		"GIT_TERMINAL_PROMPT=0", // fail instead of waiting for a password
	)
	cmd.WaitDelay = 5 * time.Second // after the kill, stop waiting on stuck pipes
	return cmd.Run()
}

// Fetch checks out exactly the pushed commit, shallowly.
func (s ExecSteps) Fetch(ctx context.Context, b Build, dir string, out io.Writer) error {
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"fetch", "--quiet", "--depth", "1", b.CloneURL, b.Commit},
		{"checkout", "--quiet", "FETCH_HEAD"},
	} {
		if err := s.command(ctx, out, dir, "git", args...); err != nil {
			return err
		}
	}
	return nil
}

// Image builds m with BuildKit for the Pi's own platform.
func (s ExecSteps) Image(ctx context.Context, m *manifest.Manifest, dir, image string, out io.Writer) error {
	args := []string{"build", "--progress=plain", "--platform", "linux/arm64",
		"--tag", image, "--label", docker.LabelApp + "=" + m.Name,
		"--file", filepath.Join(dir, m.Build.Dockerfile)}
	for _, a := range m.Build.Args {
		args = append(args, "--build-arg", a.Name+"="+a.Value)
	}
	args = append(args, filepath.Join(dir, m.Build.Context))
	return s.command(ctx, out, dir, "docker", args...)
}

// HostPort is the 127.0.0.1 port an app is published on: derived from its
// name, so it stays the same across deploys and restarts. Caddy (#24)
// proxies to it.
func HostPort(app string) int {
	h := fnv.New32a()
	h.Write([]byte(app))
	return 20000 + int(h.Sum32()%10000)
}

// startupGrace is how long a new container must stay up to count as
// started. Health checks and a zero-downtime switch are #25.
const startupGrace = 5 * time.Second

// Release replaces the app's container with a new one from the build's
// image. The old container is stopped first, so there is a short gap.
func (s ExecSteps) Release(ctx context.Context, m *manifest.Manifest, b Build, out io.Writer) error {
	env, err := s.env(m)
	if err != nil {
		return err
	}
	all, err := s.Docker.List(ctx)
	if err != nil {
		return err
	}
	for _, c := range all {
		if c.App() != m.Name {
			continue
		}
		fmt.Fprintf(out, "Removing the previous container %s (%s)\n", strings.TrimPrefix(c.Names[0], "/"), c.Image)
		if err := s.Docker.Remove(ctx, c.ID, true); err != nil && !errors.Is(err, docker.ErrNotFound) {
			return err
		}
	}

	// Docker accepts limits it cannot enforce; say so instead of letting
	// the manifest's memory: look like protection.
	if info, err := s.Docker.Info(ctx); err == nil {
		if m.Run.MemoryBytes > 0 && !info.MemoryLimit {
			fmt.Fprintf(out, "WARNING: run.memory (%d MiB) is not enforced: the memory cgroup is off on this machine (see docs/DOCKER.md, Memory limits)\n", m.Run.MemoryBytes>>20)
		}
		if m.Run.CPUs > 0 && !info.CPUCfsQuota {
			fmt.Fprintf(out, "WARNING: run.cpus is not enforced: no CPU quota support\n")
		}
	}

	port := HostPort(m.Name)
	id, err := s.Docker.Create(ctx, "piway-"+m.Name, m.Name, docker.Spec{
		Image:       b.Image,
		Cmd:         m.Run.Command,
		Env:         env,
		Labels:      map[string]string{"piway.commit": b.Commit, "piway.build": b.ID},
		Port:        m.Run.Port,
		HostPort:    port,
		MemoryBytes: m.Run.MemoryBytes,
		CPUs:        m.Run.CPUs,
	})
	if err != nil {
		return err
	}
	if err := s.Docker.Start(ctx, id); err != nil {
		return err
	}
	fmt.Fprintf(out, "Started %s, waiting %v for it to stay up\n", id[:min(12, len(id))], startupGrace)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(startupGrace):
	}
	st, err := s.Docker.Inspect(ctx, id)
	if err != nil {
		return err
	}
	if !st.Running || st.Status == "restarting" {
		msg := fmt.Sprintf("the container exited with code %d", st.ExitCode)
		if st.OOMKilled {
			msg += " (out of memory: raise run.memory)"
		}
		return errors.New(msg)
	}
	fmt.Fprintf(out, "Running on http://127.0.0.1:%d\n", port)
	return nil
}

// env is run.env plus the values of run.secrets from SecretsDir/<app>.env.
func (s ExecSteps) env(m *manifest.Manifest) ([]string, error) {
	var env []string
	for _, v := range m.Run.Env {
		env = append(env, v.Name+"="+v.Value)
	}
	if len(m.Run.Secrets) == 0 {
		return env, nil
	}
	path := filepath.Join(s.SecretsDir, m.Name+".env")
	values, err := readEnvFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var missing []string
	for _, name := range m.Run.Secrets {
		v, ok := values[name]
		if !ok {
			missing = append(missing, name)
			continue
		}
		env = append(env, name+"="+v)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("secrets not set on the Pi: %s (add NAME=value lines to %s)", strings.Join(missing, ", "), path)
	}
	return env, nil
}

// readEnvFile reads NAME=value lines; blank lines and # comments are
// skipped, and the value is everything after the first '='.
func readEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok || name == "" || slices.Contains([]rune(name), ' ') {
			return nil, fmt.Errorf("%s:%s: want NAME=value", path, strconv.Itoa(n))
		}
		out[name] = value
	}
	return out, sc.Err()
}
