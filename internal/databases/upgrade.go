package databases

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/ofk0/piway/internal/docker"
)

// ErrSameVersion means an upgrade would not change the major version.
var ErrSameVersion = errors.New("already on that major version")

// Current returns the image engine's server runs; ErrNotRunning if it
// has none.
func (m *Manager) Current(ctx context.Context, engine string) (string, error) {
	d, err := m.Docker.Details(ctx, containerName(engine))
	if errors.Is(err, docker.ErrNotFound) {
		return "", fmt.Errorf("%s: %w", engine, ErrNotRunning)
	}
	if err != nil {
		return "", err
	}
	if !d.State.Running {
		return d.Image, fmt.Errorf("%s: %w", engine, ErrNotRunning)
	}
	return d.Image, nil
}

// Upgrade moves engine's server to image (the pinned one when empty) by
// dump and restore, as data directories do not carry across major
// versions:
//
//  1. every app's database is backed up;
//  2. the old server is stopped and kept, renamed piway-<engine>-<major>;
//  3. a server from image starts on a new volume, each app gets its
//     database and user back with the same password, and its backup is
//     loaded.
//
// If any of 3 fails, the new server and its volume are removed and the
// old one comes back as it was. On success the old server is removed, and
// its volume kept, for a manual rollback. Apps see errors while it runs.
func (m *Manager) Upgrade(ctx context.Context, engine, image string, out io.Writer) error {
	e, ok := engines[engine]
	if !ok {
		return fmt.Errorf("unknown database %q", engine)
	}
	if image == "" {
		image = m.image(e)
	}
	unlock, err := m.lock()
	if err != nil {
		return err
	}
	defer unlock()

	current, err := m.Current(ctx, engine)
	if err != nil {
		return err
	}
	if major(current) == major(image) {
		return fmt.Errorf("%s runs %s: %w", engine, current, ErrSameVersion)
	}
	name := containerName(engine)
	oldName := name + "-" + major(current)
	newVolume := volumeName(engine, image)
	vs, err := m.Docker.Volumes(ctx, LabelEngine)
	if err != nil {
		return err
	}
	for _, v := range vs {
		if v.Name == newVolume {
			// Left from an earlier attempt or a downgrade: loading the
			// dumps into it would mix two histories.
			return fmt.Errorf("volume %s exists already; remove it first (docker volume rm %s)", newVolume, newVolume)
		}
	}

	apps, err := m.Apps(engine)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Upgrading %s from %s to %s (%d apps)\n", engine, current, image, len(apps))
	backups := map[string]string{}
	for _, app := range apps {
		b, err := m.BackupApp(ctx, engine, app, 0)
		if err != nil {
			return fmt.Errorf("back up before upgrading (nothing changed): %w", err)
		}
		fmt.Fprintf(out, "Backed up %s: %s\n", app, b.Path)
		backups[app] = b.Path
	}
	if ok, err := m.Docker.ImageExists(ctx, image); err != nil {
		return err
	} else if !ok {
		fmt.Fprintf(out, "Pulling %s\n", image)
		if err := m.Docker.Pull(ctx, image); err != nil {
			return fmt.Errorf("pull %s (nothing changed): %w", image, err)
		}
	}

	fmt.Fprintf(out, "Stopping %s, kept as %s\n", name, oldName)
	if err := m.Docker.Stop(ctx, name, 30*time.Second); err != nil {
		return err
	}
	if err := m.Docker.Rename(ctx, name, oldName); err != nil {
		m.Docker.Start(context.WithoutCancel(ctx), name)
		return err
	}

	rollback := func(cause error) error {
		bg := context.WithoutCancel(ctx)
		fmt.Fprintf(out, "Upgrade failed: %v\nPutting %s back\n", cause, current)
		m.Docker.Remove(bg, name, true)
		m.Docker.RemoveVolume(bg, newVolume)
		errs := []error{cause}
		if err := m.Docker.Rename(bg, oldName, name); err != nil {
			errs = append(errs, fmt.Errorf("rename %s back: %w", oldName, err))
		} else if err := m.Docker.Start(bg, name); err != nil {
			errs = append(errs, fmt.Errorf("start %s: %w", name, err))
		} else if err := m.wait(bg, e, readyTimeout); err != nil {
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	}

	fmt.Fprintf(out, "Starting %s (%s) on %s\n", name, image, newVolume)
	if err := m.create(ctx, e, image, out); err != nil {
		return rollback(err)
	}
	for _, app := range apps {
		if err := m.restoreLocked(ctx, e, app, backups[app]); err != nil {
			return rollback(fmt.Errorf("restore %s: %w", app, err))
		}
		fmt.Fprintf(out, "Restored %s\n", app)
	}

	if err := m.Docker.Remove(ctx, oldName, true); err != nil {
		fmt.Fprintf(out, "WARNING: remove %s: %v\n", oldName, err)
	}
	fmt.Fprintf(out, "%s now runs %s. The old data stays in volume %s until you remove it (docker volume rm %s).\n",
		engine, image, volumeName(engine, current), volumeName(engine, current))
	return nil
}
