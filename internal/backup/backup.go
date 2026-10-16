// Package backup writes one archive with what is needed to bring a piway
// installation back: its configuration (apps.yaml, secrets, deploy keys,
// the password), the state worth keeping (deploy records, API tokens,
// push subscriptions; metrics only when asked) and every app volume.
//
// # Volumes
//
// Volumes are read from inside a small helper container, not from the
// host: with rootless Docker, files an app writes as a non-root user
// belong to a sub-UID piway cannot read directly. In the helper, SQLite
// databases (*.db, *.sqlite, *.sqlite3 that start with SQLite's header)
// are copied with SQLite's online backup, which gives a consistent copy
// while the app keeps writing; a raw copy of a live database, or of its
// WAL, can be corrupt. Their -wal, -shm and -journal files are left out,
// as the backup already contains what they held.
//
// # Layout
//
//	piway-backup/MANIFEST.json
//	piway-backup/config/…              the config directory
//	piway-backup/state/…               part of the data directory
//	piway-backup/volumes/<volume>/…    each volume's files, owners kept
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Root is the directory every entry of an archive is in.
const Root = "piway-backup"

// VolumeReader streams a tar of one volume's files. Databases it copied
// with SQLite's online backup are listed in sqlite (volume-relative).
type VolumeReader interface {
	ReadVolume(ctx context.Context, volume string, tw TarSink) (sqlite []string, err error)
}

// TarSink takes the entries of a volume, with names relative to it.
type TarSink func(hdr *tar.Header, body io.Reader) error

// Options says what to back up.
type Options struct {
	ConfigDir string   // the directory of apps.yaml: config, secrets, keys
	DataDir   string   // piway's state; "" skips it
	Metrics   bool     // include the metrics history (it can be large)
	Volumes   []string // Docker volumes to include
	Reader    VolumeReader
	Version   string // piway's version, for the manifest
	Log       io.Writer
	now       func() time.Time
}

// Manifest describes an archive.
type Manifest struct {
	Format  int       `json:"format"`
	Created time.Time `json:"created"`
	Version string    `json:"piway_version"`
	Volumes []Volume  `json:"volumes"`
	Metrics bool      `json:"metrics"`
}

// Volume is one volume in an archive.
type Volume struct {
	Name   string   `json:"name"`
	Files  int      `json:"files"`
	Bytes  int64    `json:"bytes"`
	SQLite []string `json:"sqlite,omitempty"` // copied with the online backup
}

// stateKept are the parts of the data directory a backup includes.
// Sessions are left out on purpose: they are short-lived credentials,
// and signing in again is cheap.
var stateKept = []string{"db-backups", "deploys", "push", "tokens.json"}

// metricsDirs hold the metrics history.
var metricsDirs = []string{"raw", "1m", "1h"}

// Write writes the archive, gzipped, to w.
func Write(ctx context.Context, w io.Writer, opts Options) (*Manifest, error) {
	if opts.now == nil {
		opts.now = time.Now
	}
	if opts.Log == nil {
		opts.Log = io.Discard
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	m := &Manifest{Format: 1, Created: opts.now().UTC(), Version: opts.Version, Metrics: opts.Metrics, Volumes: []Volume{}}

	if opts.ConfigDir != "" {
		fmt.Fprintf(opts.Log, "config  %s\n", opts.ConfigDir)
		if err := addTree(tw, opts.ConfigDir, Root+"/config", nil); err != nil {
			return nil, fmt.Errorf("config: %w", err)
		}
	}
	if opts.DataDir != "" {
		keep := slices.Clone(stateKept)
		if opts.Metrics {
			keep = append(keep, metricsDirs...)
		}
		fmt.Fprintf(opts.Log, "state   %s (%s)\n", opts.DataDir, strings.Join(keep, ", "))
		err := addTree(tw, opts.DataDir, Root+"/state", func(rel string) bool {
			top, _, _ := strings.Cut(rel, "/")
			return slices.Contains(keep, top)
		})
		if err != nil {
			return nil, fmt.Errorf("state: %w", err)
		}
	}
	for _, name := range opts.Volumes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		v := Volume{Name: name}
		prefix := Root + "/volumes/" + name
		sink := func(hdr *tar.Header, body io.Reader) error {
			rel := strings.TrimPrefix(path.Clean("/"+hdr.Name), "/")
			h := *hdr
			h.Name = prefix + "/" + rel
			if rel == "" {
				// The volume's root: its owner and mode matter to an
				// app that does not run as root.
				h.Name = prefix
			}
			if h.Typeflag == tar.TypeDir {
				h.Name = strings.TrimSuffix(h.Name, "/") + "/"
			}
			if err := tw.WriteHeader(&h); err != nil {
				return err
			}
			if h.Typeflag == tar.TypeReg {
				n, err := io.Copy(tw, body)
				v.Files++
				v.Bytes += n
				return err
			}
			return nil
		}
		sqlite, err := opts.Reader.ReadVolume(ctx, name, sink)
		if err != nil {
			return nil, fmt.Errorf("volume %s: %w", name, err)
		}
		v.SQLite = sqlite
		fmt.Fprintf(opts.Log, "volume  %s: %d files, %d bytes", name, v.Files, v.Bytes)
		if len(sqlite) > 0 {
			fmt.Fprintf(opts.Log, ", SQLite online backup of %s", strings.Join(sqlite, ", "))
		}
		fmt.Fprintln(opts.Log)
		m.Volumes = append(m.Volumes, v)
	}

	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	b = append(b, '\n')
	if err := tw.WriteHeader(&tar.Header{Name: Root + "/MANIFEST.json", Mode: 0o600, Size: int64(len(b)), ModTime: m.Created, Typeflag: tar.TypeReg}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(b); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return m, gz.Close()
}

// addTree adds the regular files and directories under dir (those keep
// says yes to, by slash-separated relative path) as prefix/<rel>. A
// missing dir adds nothing. Symbolic links are skipped.
func addTree(tw *tar.Writer, dir, prefix string, keep func(rel string) bool) error {
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if keep != nil && !keep(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
		hdr.Name = prefix + "/" + rel
		if d.IsDir() {
			hdr.Name += "/"
			return tw.WriteHeader(hdr)
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		// A file that grew since Info is cut at the size in the header.
		_, err = io.CopyN(tw, f, hdr.Size)
		return err
	})
}
