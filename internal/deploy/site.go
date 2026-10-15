package deploy

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// keepSites is how many versions of a static site are kept on disk, so a
// rollback has something to roll back to (like keepImages).
const keepSites = 3

// siteDir is where the site of build id lives.
func (p *Pipeline) siteDir(app, id string) string {
	return filepath.Join(p.opts.Dir, "sites", app, id)
}

// siteStats counts what copySite copied and skipped.
type siteStats struct {
	files    int
	bytes    int64
	skipped  []string // symbolic links and other non-regular files
	hasIndex bool
}

// copySite copies the directory rel inside checkout to dst, which must not
// exist. Only directories and regular files are copied: a symbolic link in
// the repository could otherwise make piway serve any file on the Pi. rel
// itself must resolve inside checkout, and .git directories are left out.
// The copy is made next to dst and renamed into place, so dst is either
// complete or missing.
func copySite(checkout, rel, dst string) (siteStats, error) {
	var st siteStats
	root, err := os.OpenRoot(checkout)
	if err != nil {
		return st, err
	}
	defer root.Close()
	src, err := root.OpenRoot(filepath.FromSlash(rel))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return st, fmt.Errorf("static.dir %q does not exist after the build", rel)
		}
		return st, fmt.Errorf("static.dir %q: %w", rel, err)
	}
	defer src.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return st, err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dst), ".tmp-")
	if err != nil {
		return st, err
	}
	defer os.RemoveAll(tmp)                      // a no-op after the rename
	if err := os.Chmod(tmp, 0o755); err != nil { // MkdirTemp makes it 0700
		return st, err
	}

	fsys := src.FS()
	err = fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		out := filepath.Join(tmp, filepath.FromSlash(name))
		switch {
		case d.IsDir() && d.Name() == ".git":
			return fs.SkipDir
		case d.IsDir():
			if name == "." {
				return nil
			}
			return os.Mkdir(out, 0o755)
		case !d.Type().IsRegular():
			st.skipped = append(st.skipped, name)
			return nil
		}
		n, err := copyFile(fsys, name, out)
		st.files++
		st.bytes += n
		if name == "index.html" {
			st.hasIndex = true
		}
		return err
	})
	if err != nil {
		return st, err
	}
	return st, os.Rename(tmp, dst)
}

func copyFile(fsys fs.FS, name, dst string) (int64, error) {
	in, err := fsys.Open(name)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return n, err
}

// pruneSites keeps the app's keepSites newest sites, and always keep, the
// one being served. Build ids sort by time.
func (p *Pipeline) pruneSites(app, keep string, out io.Writer) {
	dir := filepath.Join(p.opts.Dir, "sites", app)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() && e.Name()[0] != '.' {
			ids = append(ids, e.Name())
		}
	}
	slices.Sort(ids)
	slices.Reverse(ids)
	for i, id := range ids {
		if i < keepSites || id == keep {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, id)); err == nil {
			fmt.Fprintf(out, "Removed old site %s\n", id)
		}
	}
}
