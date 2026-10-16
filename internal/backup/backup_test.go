package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var ctx = context.Background()

// memVolumes is an in-memory VolumeReader and VolumeWriter: each volume is
// a list of tar entries with their bodies.
type memVolumes struct {
	vols map[string][]entry
}

type entry struct {
	hdr  tar.Header
	body string
}

func (m *memVolumes) ReadVolume(_ context.Context, volume string, sink TarSink) ([]string, error) {
	var sqlite []string
	for _, e := range m.vols[volume] {
		h := e.hdr
		if strings.HasSuffix(h.Name, ".db") {
			sqlite = append(sqlite, strings.TrimPrefix(h.Name, "./"))
		}
		if err := sink(&h, strings.NewReader(e.body)); err != nil {
			return nil, err
		}
	}
	return sqlite, nil
}

func (m *memVolumes) VolumeExists(_ context.Context, volume string) (bool, error) {
	_, ok := m.vols[volume]
	return ok, nil
}

func (m *memVolumes) WriteVolume(_ context.Context, volume string, r io.Reader, replace bool) error {
	if _, ok := m.vols[volume]; ok && !replace {
		return errors.New("exists")
	}
	var got []entry
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		b, _ := io.ReadAll(tr)
		got = append(got, entry{*hdr, string(b)})
	}
	m.vols[volume] = got
	return nil
}

func write(t *testing.T, dir, rel, body string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(dir, rel)
	os.MkdirAll(filepath.Dir(p), 0o700)
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func list(t *testing.T, archive []byte) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out[hdr.Name] = string(b)
	}
}

func dir(name string, uid int) entry {
	return entry{tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0o755, Uid: uid}, ""}
}

func file(name, body string, uid int) entry {
	return entry{tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(body)), Uid: uid}, body}
}

func TestWriteAndRestore(t *testing.T) {
	src := t.TempDir()
	config, data := filepath.Join(src, "config"), filepath.Join(src, "state")
	write(t, config, "apps.yaml", "deploy: []\n", 0o644)
	write(t, config, "secrets/guestbook.env", "API_KEY=x\n", 0o600)
	write(t, config, "keys/o_app", "PRIVATE", 0o600)
	write(t, data, "tokens.json", "[]", 0o600)
	write(t, data, "sessions.json", "{}", 0o600) // left out
	write(t, data, "deploys/b1.json", "{}", 0o644)
	write(t, data, "push/vapid.json", "{}", 0o600)
	write(t, data, "raw/0001", "metrics", 0o644) // only with Metrics
	os.Symlink("apps.yaml", filepath.Join(config, "link"))

	vols := &memVolumes{vols: map[string][]entry{
		"piway-guestbook_data": {dir("./", 1000), dir("./db/", 1000), file("./db/app.db", "SQLite format 3...", 1000), file("./note.txt", "hi", 1000)},
	}}
	var buf bytes.Buffer
	m, err := Write(ctx, &buf, Options{ConfigDir: config, DataDir: data, Volumes: []string{"piway-guestbook_data"}, Reader: vols, Version: "v1",
		now: func() time.Time { return time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Volumes) != 1 || m.Volumes[0].Files != 2 || !slices.Equal(m.Volumes[0].SQLite, []string{"db/app.db"}) {
		t.Errorf("manifest %+v", m)
	}

	got := list(t, buf.Bytes())
	for _, want := range []string{
		"piway-backup/config/apps.yaml", "piway-backup/config/secrets/guestbook.env", "piway-backup/config/keys/o_app",
		"piway-backup/state/tokens.json", "piway-backup/state/deploys/b1.json", "piway-backup/state/push/vapid.json",
		"piway-backup/volumes/piway-guestbook_data/", "piway-backup/volumes/piway-guestbook_data/db/app.db",
		"piway-backup/volumes/piway-guestbook_data/note.txt", "piway-backup/MANIFEST.json",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %s", want)
		}
	}
	for _, unwanted := range []string{"piway-backup/state/sessions.json", "piway-backup/state/raw/0001", "piway-backup/config/link"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("%s should not be in it", unwanted)
		}
	}

	// Restore into an empty installation.
	dst := t.TempDir()
	restored := &memVolumes{vols: map[string][]entry{}}
	archive := filepath.Join(dst, "b.tar.gz")
	os.WriteFile(archive, buf.Bytes(), 0o600)
	ropts := RestoreOptions{ConfigDir: filepath.Join(dst, "config"), DataDir: filepath.Join(dst, "state"), Writer: restored}
	if err := Restore(ctx, archive, ropts); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "config", "secrets", "guestbook.env")); string(b) != "API_KEY=x\n" {
		t.Errorf("secret %q", b)
	}
	if fi, _ := os.Stat(filepath.Join(dst, "config", "keys", "o_app")); fi.Mode().Perm() != 0o600 {
		t.Errorf("key mode %v", fi.Mode().Perm())
	}
	v := restored.vols["piway-guestbook_data"]
	if len(v) != 4 || v[0].hdr.Name != "./" || v[0].hdr.Uid != 1000 || v[2].hdr.Name != "db/app.db" || v[2].body != "SQLite format 3..." {
		t.Errorf("volume entries %+v", v)
	}

	// Again: everything exists, so nothing changes without Force.
	os.WriteFile(filepath.Join(dst, "config", "apps.yaml"), []byte("changed"), 0o644)
	err = Restore(ctx, archive, ropts)
	if !errors.Is(err, ErrExists) || !strings.Contains(err.Error(), "volume piway-guestbook_data") {
		t.Errorf("restore over an installation: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "config", "apps.yaml")); string(b) != "changed" {
		t.Error("a refused restore changed a file")
	}
	ropts.Force = true
	if err := Restore(ctx, archive, ropts); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "config", "apps.yaml")); string(b) != "deploy: []\n" {
		t.Errorf("forced restore: %q", b)
	}
}

func TestWriteMetrics(t *testing.T) {
	data := t.TempDir()
	write(t, data, "raw/0001", "metrics", 0o644)
	var buf bytes.Buffer
	if _, err := Write(ctx, &buf, Options{DataDir: data, Metrics: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := list(t, buf.Bytes())["piway-backup/state/raw/0001"]; !ok {
		t.Error("metrics left out with Metrics")
	}
}

func archiveOf(t *testing.T, entries ...entry) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := e.hdr
		tw.WriteHeader(&h)
		tw.Write([]byte(e.body))
	}
	tw.Close()
	gz.Close()
	p := filepath.Join(t.TempDir(), "a.tar.gz")
	os.WriteFile(p, buf.Bytes(), 0o600)
	return p
}

func TestRestoreRejects(t *testing.T) {
	manifest := file("piway-backup/MANIFEST.json", "{}", 0)
	for name, archive := range map[string]string{
		"escape":      archiveOf(t, file("piway-backup/config/../../etc/x", "x", 0), manifest),
		"absolute":    archiveOf(t, file("/etc/passwd", "x", 0), manifest),
		"foreign":     archiveOf(t, file("other/x", "x", 0), manifest),
		"no manifest": archiveOf(t, file("piway-backup/config/apps.yaml", "x", 0)),
		"symlink":     archiveOf(t, entry{tar.Header{Name: "piway-backup/config/l", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}, ""}, manifest),
	} {
		dst := t.TempDir()
		err := Restore(ctx, archive, RestoreOptions{ConfigDir: filepath.Join(dst, "c"), DataDir: filepath.Join(dst, "s")})
		if err == nil {
			t.Errorf("%s: restored", name)
		}
		if entries, _ := os.ReadDir(dst); len(entries) != 0 {
			t.Errorf("%s: wrote %v before refusing", name, entries)
		}
	}
}

func TestRelay(t *testing.T) {
	// What the helper sends: files, then sq/<hex of ./path>.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add := func(name, body string, typ byte) {
		tw.WriteHeader(&tar.Header{Name: name, Typeflag: typ, Size: int64(len(body)), Mode: 0o644})
		tw.Write([]byte(body))
	}
	add("./", "", tar.TypeDir)
	add("./notes.txt", "n", tar.TypeReg)
	add("sq/", "", tar.TypeDir)
	add("sq/"+hex.EncodeToString([]byte("./db/app.db")), "backup", tar.TypeReg)
	tw.Close()

	var names []string
	sqlite, err := relay(&buf, func(h *tar.Header, body io.Reader) error {
		b, _ := io.ReadAll(body)
		names = append(names, h.Name+"="+string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sqlite, []string{"db/app.db"}) || !slices.Equal(names, []string{"./=", "./notes.txt=n", "db/app.db=backup"}) {
		t.Errorf("sqlite %q, entries %q", sqlite, names)
	}
}

func TestSplitVolume(t *testing.T) {
	if app, name, ok := splitVolume("piway-guest-book_data"); !ok || app != "guest-book" || name != "data" {
		t.Errorf("%q %q %v", app, name, ok)
	}
	if _, _, ok := splitVolume("mydata"); ok {
		t.Error("a volume that is not piway's")
	}
}
