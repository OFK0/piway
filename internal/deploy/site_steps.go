package deploy

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/ofk0/piway/internal/manifest"
)

// SiteBuild runs static.build.command with sh -c in static.build.image,
// with the checkout mounted as the working directory. With rootless
// Docker, root in the container is the user piway runs as, so the files
// the build writes are piway's to copy and delete.
func (s ExecSteps) SiteBuild(ctx context.Context, m *manifest.Manifest, dir string, out io.Writer) error {
	name := "piway-site-build-" + m.Name + "-" + filepath.Base(dir)
	name = strings.ToLower(name)
	err := s.command(ctx, out, dir, "docker", "run", "--rm", "--name", name,
		"--platform", "linux/arm64",
		"--label", "piway.site-build="+m.Name, // not docker.LabelApp: it serves nothing
		"--volume", dir+":/src", "--workdir", "/src",
		"--entrypoint", "sh", m.Static.Build.Image, "-c", m.Static.Build.Command)
	if err != nil {
		// Killing the CLI (on cancel or timeout) leaves the container running.
		s.command(context.WithoutCancel(ctx), io.Discard, dir, "docker", "rm", "--force", name)
	}
	return err
}

// ServeSite points site.App's stable port at the site's directory.
func (s ExecSteps) ServeSite(site Site) error {
	return s.Router.SetSite(site.App, HostPort(site.App), site.Dir, site.SPA)
}

// ReleaseSite switches the app's route to the site in dir, in one step
// like a container release, then removes any container that served the
// app before (an app can change from a container to a static site).
func (s ExecSteps) ReleaseSite(ctx context.Context, m *manifest.Manifest, b Build, dir string, out io.Writer) error {
	if err := s.ServeSite(Site{App: m.Name, Dir: dir, SPA: m.Static.SPA}); err != nil {
		return err
	}
	fmt.Fprintf(out, "Switched http://127.0.0.1:%d to the site of build %s\n", HostPort(m.Name), b.Site)

	bg := context.WithoutCancel(ctx)
	all, err := s.Docker.List(bg)
	if err != nil {
		fmt.Fprintf(out, "WARNING: list containers: %v\n", err)
		return nil
	}
	for _, c := range all {
		if c.App() == m.Name {
			// Let requests in flight on the old container finish first.
			time.Sleep(drain)
			s.removeOthers(bg, m.Name, "", out)
			break
		}
	}
	return nil
}
