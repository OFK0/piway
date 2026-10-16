package deploy

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ofk0/piway/internal/databases"
	"github.com/ofk0/piway/internal/docker"
	"github.com/ofk0/piway/internal/gitauth"
	"github.com/ofk0/piway/internal/manifest"
	"github.com/ofk0/piway/internal/router"
	"github.com/ofk0/piway/internal/secrets"
)

// ExecSteps runs builds with git, the docker CLI (BuildKit) and the Docker
// Engine API.
type ExecSteps struct {
	Docker     *docker.Client
	Router     *router.Router   // owns each app's stable port
	DockerHost string           // unix://… for the docker CLI
	Secrets    *secrets.Store   // the values of run.secrets
	Git        *gitauth.Access  // credentials for private repositories
	Databases  DatabaseProvider // run.databases; nil refuses apps that ask
}

// DatabaseProvider gives an app its databases. *databases.Manager
// implements it.
type DatabaseProvider interface {
	// Prepare starts the engines, sets up app's database and user on each
	// and returns the variables to inject, as NAME=value.
	Prepare(ctx context.Context, app string, engines []string, out io.Writer) ([]string, error)
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

// Fetch checks out exactly the pushed commit, shallowly, with the
// repository's credential if it is private.
func (s ExecSteps) Fetch(ctx context.Context, b Build, dir string, out io.Writer) error {
	if err := s.command(ctx, out, dir, "git", "init", "--quiet"); err != nil {
		return err
	}
	src := gitauth.Source{Repo: b.Repo, Auth: b.Auth, Provider: b.Provider, Host: b.Host, URL: b.CloneURL}
	err := s.Git.Run(ctx, src, dir, out, func(url string) []string {
		return []string{"fetch", "--quiet", "--depth", "1", url, b.Commit}
	})
	if err != nil {
		return err
	}
	return s.command(ctx, out, dir, "git", "checkout", "--quiet", "FETCH_HEAD")
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

// LabelVolume on a volume is its name in run.volumes; LabelApp names
// the app.
const LabelVolume = "piway.volume"

// VolumeName is the Docker volume behind an app's run.volumes entry. App
// names have no "_", so the name is unambiguous.
func VolumeName(app, volume string) string { return "piway-" + app + "_" + volume }

// volumes makes sure the app's volumes exist and returns their mounts.
// The old and the new container mount the same volumes during a release.
func (s ExecSteps) volumes(ctx context.Context, m *manifest.Manifest, out io.Writer) ([]docker.Mount, error) {
	var mounts []docker.Mount
	for _, v := range m.Run.Volumes {
		name := VolumeName(m.Name, v.Name)
		if _, err := s.Docker.CreateVolume(ctx, name, map[string]string{docker.LabelApp: m.Name, LabelVolume: v.Name}); err != nil {
			return nil, fmt.Errorf("volume %s: %w", v.Name, err)
		}
		fmt.Fprintf(out, "Volume %s (%s) at %s\n", v.Name, name, v.Path)
		mounts = append(mounts, docker.Mount{Volume: name, Target: v.Path})
	}
	return mounts, nil
}

// AppVolumes returns the Docker volumes piway made for app.
func (s ExecSteps) AppVolumes(ctx context.Context, app string) ([]docker.Volume, error) {
	all, err := s.Docker.Volumes(ctx, LabelVolume)
	if err != nil {
		return nil, err
	}
	var out []docker.Volume
	for _, v := range all {
		if v.Labels[docker.LabelApp] == app {
			out = append(out, v)
		}
	}
	return out, nil
}

// RemoveAppVolume deletes one of app's volumes and its data.
func (s ExecSteps) RemoveAppVolume(ctx context.Context, app, volume string) error {
	return s.Docker.RemoveVolume(ctx, VolumeName(app, volume))
}

// startWithRetry starts a container of the app. Docker picks the host
// port, from the range the kernel also uses for outgoing connections, so
// on a busy machine it can pick one that is taken a moment later. A new
// container draws a new port: try a few times before failing the deploy.
func (s ExecSteps) startWithRetry(ctx context.Context, name string, m *manifest.Manifest, spec docker.Spec, out io.Writer) (string, error) {
	for attempt := 1; ; attempt++ {
		id, err := s.startContainer(ctx, name, m, spec)
		if err == nil || attempt == 4 || !strings.Contains(err.Error(), "address already in use") {
			return id, err
		}
		fmt.Fprintf(out, "Docker's port was taken (%v); trying a new container\n", err)
	}
}

// startContainer creates and starts one container of the app. A container
// that does not start is removed.
func (s ExecSteps) startContainer(ctx context.Context, name string, m *manifest.Manifest, spec docker.Spec) (string, error) {
	id, err := s.Docker.Create(ctx, name, m.Name, spec)
	if err != nil {
		return "", err
	}
	if len(m.Run.Databases) > 0 {
		// The engines are on their own internal network; the app joins it
		// as well as the default one, which reaches the internet.
		if err := s.Docker.Connect(ctx, databases.Network, id); err != nil {
			s.Docker.Remove(context.WithoutCancel(ctx), id, true)
			return "", fmt.Errorf("join %s: %w", databases.Network, err)
		}
	}
	if err := s.Docker.Start(ctx, id); err != nil {
		s.Docker.Remove(context.WithoutCancel(ctx), id, true)
		return "", err
	}
	return id, nil
}

// LabelSecrets on a container is the digest of the secret values it was
// started with, so the app page can tell when they changed since.
const LabelSecrets = "piway.secrets"

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
	env, digest, err := s.env(m)
	if err != nil {
		return err
	}
	if len(m.Run.Databases) > 0 {
		if s.Databases == nil {
			return errors.New("run.databases: databases are not available on this piway")
		}
		vars, err := s.Databases.Prepare(ctx, m.Name, m.Run.Databases, out)
		if err != nil {
			return fmt.Errorf("databases: %w", err)
		}
		env = append(env, vars...)
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

	mounts, err := s.volumes(ctx, m, out)
	if err != nil {
		return err
	}

	name := "piway-" + m.Name + "-" + strings.ToLower(b.ID)
	spec := docker.Spec{
		Mounts:      mounts,
		Image:       b.Image,
		Cmd:         m.Run.Command,
		Env:         env,
		Labels:      map[string]string{"piway.commit": b.Commit, "piway.build": b.ID, LabelSecrets: digest},
		Port:        m.Run.Port,
		MemoryBytes: m.Run.MemoryBytes,
		CPUs:        m.Run.CPUs,
	}
	id, err := s.startWithRetry(ctx, name, m, spec, out)
	if errors.Is(err, docker.ErrNotFound) {
		return fmt.Errorf("image %s no longer exists (piway keeps the last %d per app)", b.Image, keepImages)
	}
	if err != nil {
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

// env is run.env plus the values of run.secrets, and the digest of
// those values (secrets.Digest), which labels the container.
func (s ExecSteps) env(m *manifest.Manifest) (env []string, digest string, err error) {
	for _, v := range m.Run.Env {
		env = append(env, v.Name+"="+v.Value)
	}
	if len(m.Run.Secrets) == 0 {
		return env, "", nil
	}
	values, err := s.Secrets.Values(m.Name)
	if err != nil {
		return nil, "", err
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
		return nil, "", fmt.Errorf("secrets not set on the Pi: %s (set them on the app's page, or add NAME=value lines to %s)",
			strings.Join(missing, ", "), s.Secrets.Path(m.Name))
	}
	return env, secrets.Digest(m.Run.Secrets, values), nil
}

// Containers returns each app's running container: the one its route
// points at, else any running one.
func (s ExecSteps) Containers(ctx context.Context) (map[string]Container, error) {
	all, err := s.Docker.List(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]Container{}
	for _, c := range all {
		app := c.App()
		if cur, ok := out[app]; ok && cur.State == "running" && c.State != "running" {
			continue
		}
		d, err := s.Docker.Details(ctx, c.ID)
		if err != nil {
			continue
		}
		served := false
		if s.Router != nil {
			for _, port := range d.Ports {
				served = s.Router.Target(app) == fmt.Sprintf("http://127.0.0.1:%d", port)
			}
		}
		if _, ok := out[app]; ok && !served {
			continue
		}
		out[app] = Container{Name: d.Name, State: d.State.Status, Image: d.Image, Started: d.State.StartedAt,
			SecretsDigest: d.Labels[LabelSecrets]}
	}
	return out, nil
}

// Logs returns the last lines of the container that serves app.
func (s ExecSteps) Logs(ctx context.Context, app string, lines int) (string, error) {
	all, err := s.Docker.List(ctx)
	if err != nil {
		return "", err
	}
	var fallback string
	for _, c := range all {
		if c.App() != app || c.State != "running" {
			continue
		}
		fallback = c.ID
		d, err := s.Docker.Details(ctx, c.ID)
		if err != nil {
			continue
		}
		for _, port := range d.Ports {
			if s.Router != nil && s.Router.Target(app) == fmt.Sprintf("http://127.0.0.1:%d", port) {
				return s.Docker.Logs(ctx, c.ID, lines)
			}
		}
	}
	if fallback == "" {
		return "", fmt.Errorf("%w: no container of %s is running", ErrNotRunning, app)
	}
	return s.Docker.Logs(ctx, fallback, lines)
}
