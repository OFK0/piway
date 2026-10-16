package backup

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"
)

// HelperImage is built on first use from helperDockerfile: Alpine with
// SQLite's shell and GNU tar. The tag changes when the Dockerfile does.
const HelperImage = "piway/backup-helper:1"

const helperDockerfile = `FROM alpine:3.22
RUN apk add --no-cache sqlite tar
`

// helperScript runs in the helper with the volume at /v. It backs each
// SQLite database up to /tmp/sq/<hex of its path>, then writes one tar to
// stdout: the volume's files without the live databases (and their WAL,
// shared-memory and journal files), and the backups under sq/. It names
// each database it backed up on stderr.
const helperScript = `set -eu
cd /v
mkdir -p /tmp/sq
: > /tmp/exclude
find . -type f \( -name '*.db' -o -name '*.sqlite' -o -name '*.sqlite3' \) > /tmp/candidates
while IFS= read -r f; do
	[ "$(head -c 15 "$f")" = "SQLite format 3" ] || continue
	key=$(printf '%s' "$f" | od -An -tx1 | tr -d ' \n')
	sqlite3 -bail "$f" ".timeout 10000" ".backup main /tmp/sq/$key"
	# The copy keeps the database's owner and mode, so the app can write
	# it again after a restore.
	chown "$(stat -c %u:%g "$f")" "/tmp/sq/$key"
	chmod "$(stat -c %a "$f")" "/tmp/sq/$key"
	printf '%s\n%s-wal\n%s-shm\n%s-journal\n' "$f" "$f" "$f" "$f" >> /tmp/exclude
	printf 'sqlite %s\n' "$f" >&2
done < /tmp/candidates
tar -cf - --no-wildcards --exclude-from=/tmp/exclude -C /v . -C /tmp sq
`

// Docker reads volumes with the helper image, through the docker CLI.
type Docker struct {
	Host string    // DOCKER_HOST, such as unix:///run/user/1000/docker.sock
	Log  io.Writer // image build output
	once sync.Once
	err  error
}

func (d *Docker) docker(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = append(os.Environ(), "DOCKER_HOST="+d.Host)
	return cmd
}

// image builds HelperImage unless it exists.
func (d *Docker) image(ctx context.Context) error {
	d.once.Do(func() {
		if d.docker(ctx, "image", "inspect", HelperImage).Run() == nil {
			return
		}
		fmt.Fprintf(d.Log, "building %s (once)\n", HelperImage)
		cmd := d.docker(ctx, "build", "--quiet", "--platform", "linux/arm64", "--tag", HelperImage, "-")
		cmd.Stdin = strings.NewReader(helperDockerfile)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Run(); err != nil {
			d.err = fmt.Errorf("build %s: %w\n%s", HelperImage, err, strings.TrimSpace(out.String()))
		}
	})
	return d.err
}

// ReadVolume implements VolumeReader.
func (d *Docker) ReadVolume(ctx context.Context, volume string, sink TarSink) ([]string, error) {
	if err := d.image(ctx); err != nil {
		return nil, err
	}
	// No network, and the volume read-write only because SQLite needs to
	// take its locks; nothing in it is changed.
	cmd := d.docker(ctx, "run", "--rm", "--network", "none", "--label", "piway.backup="+volume,
		"--mount", "type=volume,source="+volume+",target=/v",
		HelperImage, "sh", "-c", helperScript)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	sqlite, readErr := relay(stdout, sink)
	if readErr != nil {
		io.Copy(io.Discard, stdout) // let the helper finish
	}
	waitErr := cmd.Wait()
	var names []string
	sc := bufio.NewScanner(&stderr)
	var other []string
	for sc.Scan() {
		if f, ok := strings.CutPrefix(sc.Text(), "sqlite "); ok {
			names = append(names, strings.TrimPrefix(f, "./"))
		} else if sc.Text() != "" {
			other = append(other, sc.Text())
		}
	}
	if waitErr != nil {
		return nil, fmt.Errorf("helper: %w: %s", waitErr, strings.Join(other, "; "))
	}
	if readErr != nil {
		return nil, readErr
	}
	if len(names) != len(sqlite) {
		return nil, fmt.Errorf("helper backed up %d databases but sent %d", len(names), len(sqlite))
	}
	return names, nil
}

// relay passes the helper's tar on to sink, putting each SQLite backup
// (sq/<hex of ./path>) at its database's path. It returns the paths.
func relay(r io.Reader, sink TarSink) ([]string, error) {
	tr := tar.NewReader(r)
	var sqlite []string
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return sqlite, nil
		}
		if err != nil {
			return nil, err
		}
		name := hdr.Name
		if rest, ok := strings.CutPrefix(name, "sq/"); ok {
			if rest == "" || hdr.Typeflag != tar.TypeReg {
				continue
			}
			p, err := hex.DecodeString(rest)
			if err != nil {
				return nil, fmt.Errorf("helper sent %q", name)
			}
			name = path.Clean(string(p))
			sqlite = append(sqlite, strings.TrimPrefix(name, "./"))
		} else if name == "sq" {
			continue
		}
		h := *hdr
		h.Name = name
		if err := sink(&h, tr); err != nil {
			return nil, err
		}
	}
}

// VolumeExists implements VolumeWriter.
func (d *Docker) VolumeExists(ctx context.Context, volume string) (bool, error) {
	err := d.docker(ctx, "volume", "inspect", volume).Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return false, nil
	}
	return err == nil, err
}

// WriteVolume implements VolumeWriter: it creates the volume, labelled as
// piway's release would, and extracts r into it in the helper, owners
// and modes kept.
func (d *Docker) WriteVolume(ctx context.Context, volume string, r io.Reader, replace bool) error {
	if err := d.image(ctx); err != nil {
		return err
	}
	create := []string{"volume", "create"}
	if app, name, ok := splitVolume(volume); ok {
		create = append(create, "--label", "piway.app="+app, "--label", "piway.volume="+name)
	}
	if out, err := d.docker(ctx, append(create, volume)...).CombinedOutput(); err != nil {
		return fmt.Errorf("create: %w: %s", err, strings.TrimSpace(string(out)))
	}
	mode := "keep"
	if replace {
		mode = "replace"
	}
	cmd := d.docker(ctx, "run", "--rm", "-i", "--network", "none", "--label", "piway.restore="+volume,
		"--mount", "type=volume,source="+volume+",target=/v",
		HelperImage, "sh", "-c", restoreScript, "sh", mode)
	cmd.Stdin = r
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("helper: %w: %s", err, strings.TrimSpace(out.String()))
	}
	return nil
}

// restoreScript extracts stdin into /v; with "replace" it empties /v
// first, so the volume ends up exactly as backed up.
const restoreScript = `set -eu
if [ "$1" = replace ]; then find /v -mindepth 1 -delete; fi
tar -xpf - -C /v --numeric-owner --same-owner
`

// splitVolume undoes deploy.VolumeName: piway-<app>_<name>.
func splitVolume(v string) (app, name string, ok bool) {
	rest, ok := strings.CutPrefix(v, "piway-")
	if !ok {
		return "", "", false
	}
	app, name, ok = strings.Cut(rest, "_")
	return app, name, ok && app != "" && name != ""
}
