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
	"github.com/ofk0/piway/internal/router"
)

// ExecSteps runs builds with git, the docker CLI (BuildKit) and the Docker
// Engine API.
type ExecSteps struct {
	Docker     *docker.Client
	Router     *router.Router // owns each app's stable port
	DockerHost string         // unix://… for the docker CLI
	SecretsDir string         // <app>.env files with the values of run.secrets
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

// HostPort is the app's stable address on 127.0.0.1, served by the router:
// derived from the name, so it stays the same across deploys and restarts.
// Caddy (#24) proxies to it.
func HostPort(app string) int {
	h := fnv.New32a()
	h.Write([]byte(app))
	return 20000 + int(h.Sum32()%10000)
}

// drain is how long the old container keeps serving requests that were
// already in flight when traffic switched.
const drain = 2 * time.Second

// keepImages is how many images per app are kept, so a rollback has
// something to roll back to.
const keepImages = 3

// Release starts a container from the build's image next to the one that
// serves now, waits until it is healthy, and only then switches the app's
// route to it (blue/green). If the new version never becomes healthy, it
// is removed and the old one keeps serving.
func (s ExecSteps) Release(ctx context.Context, m *manifest.Manifest, b Build, out io.Writer) error {
	env, err := s.env(m)
	if err != nil {
		return err
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

	name := "piway-" + m.Name + "-" + strings.ToLower(b.ID)
	id, err := s.Docker.Create(ctx, name, m.Name, docker.Spec{
		Image:       b.Image,
		Cmd:         m.Run.Command,
		Env:         env,
		Labels:      map[string]string{"piway.commit": b.Commit, "piway.build": b.ID},
		Port:        m.Run.Port,
		MemoryBytes: m.Run.MemoryBytes,
		CPUs:        m.Run.CPUs,
	})
	if errors.Is(err, docker.ErrNotFound) {
		return fmt.Errorf("image %s no longer exists (piway keeps the last %d per app)", b.Image, keepImages)
	}
	if err != nil {
		return err
	}
	if err := s.Docker.Start(ctx, id); err != nil {
		s.Docker.Remove(context.WithoutCancel(ctx), id, true)
		return err
	}
	d, err := s.Docker.Details(ctx, id)
	if err != nil {
		return err
	}
	port := d.Ports[strconv.Itoa(m.Run.Port)+"/tcp"]
	fmt.Fprintf(out, "Started %s on 127.0.0.1:%d; checking %s\n", name, port, m.Run.Health.Path)

	alive := func(ctx context.Context) error {
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
		return nil
	}
	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, m.Run.Health.Path)
	if err := waitHealthy(ctx, url, m.Run.Health, alive, out); err != nil {
		// Clean up even when the build was cancelled.
		s.Docker.Remove(context.WithoutCancel(ctx), id, true)
		return fmt.Errorf("%w; the previous version keeps serving", err)
	}

	stable := HostPort(m.Name)
	if err := s.Router.Set(m.Name, stable, fmt.Sprintf("http://127.0.0.1:%d", port)); err != nil {
		s.Docker.Remove(context.WithoutCancel(ctx), id, true)
		return err
	}
	fmt.Fprintf(out, "Switched http://127.0.0.1:%d to %s\n", stable, name)

	// From here the new version serves; failures below are only logged.
	bg := context.WithoutCancel(ctx)
	time.Sleep(drain)
	s.removeOthers(bg, m.Name, id, out)
	s.pruneImages(bg, m.Name, out)
	return nil
}

// removeOthers stops (SIGTERM, then SIGKILL after 10 s) and removes the
// app's containers other than keep.
func (s ExecSteps) removeOthers(ctx context.Context, app, keep string, out io.Writer) {
	all, err := s.Docker.List(ctx)
	if err != nil {
		fmt.Fprintf(out, "WARNING: list containers: %v\n", err)
		return
	}
	for _, c := range all {
		if c.App() != app || c.ID == keep {
			continue
		}
		fmt.Fprintf(out, "Stopping the previous container %s (%s)\n", strings.TrimPrefix(c.Names[0], "/"), c.Image)
		if err := s.Docker.Stop(ctx, c.ID, 10*time.Second); err != nil && !errors.Is(err, docker.ErrNotFound) {
			fmt.Fprintf(out, "WARNING: stop: %v\n", err)
		}
		if err := s.Docker.Remove(ctx, c.ID, true); err != nil && !errors.Is(err, docker.ErrNotFound) {
			fmt.Fprintf(out, "WARNING: remove: %v\n", err)
		}
	}
}

// pruneImages keeps the app's keepImages newest images.
func (s ExecSteps) pruneImages(ctx context.Context, app string, out io.Writer) {
	imgs, err := s.Docker.Images(ctx, app)
	if err != nil || len(imgs) <= keepImages {
		return
	}
	for _, img := range imgs[keepImages:] {
		if err := s.Docker.RemoveImage(ctx, img.ID); err == nil {
			fmt.Fprintf(out, "Removed old image %s\n", strings.Join(img.RepoTags, ", "))
		}
	}
}

// Restore re-creates the routes of running apps after piway starts. For an
// app with several containers (piway stopped mid-deploy), the one from
// live(app) wins, else the newest.
func (s ExecSteps) Restore(ctx context.Context, live func(app string) (buildID string)) error {
	all, err := s.Docker.List(ctx)
	if err != nil {
		return err
	}
	best := map[string]docker.Container{}
	for _, c := range all {
		if c.State != "running" {
			continue
		}
		app := c.App()
		cur, ok := best[app]
		if !ok {
			best[app] = c
			continue
		}
		liveID := live(app)
		curLive, cLive := cur.Labels["piway.build"] == liveID, c.Labels["piway.build"] == liveID
		if (cLive && !curLive) || (cLive == curLive && c.Created > cur.Created) {
			best[app] = c
		}
	}
	var errs []error
	for app, c := range best {
		d, err := s.Docker.Details(ctx, c.ID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, port := range d.Ports {
			if err := s.Router.Set(app, HostPort(app), fmt.Sprintf("http://127.0.0.1:%d", port)); err != nil {
				errs = append(errs, err)
			}
			break
		}
	}
	return errors.Join(errs...)
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
