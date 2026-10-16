package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// VolumeWriter fills a volume from a tar stream with names relative to
// it. It creates the volume if needed; replace empties one that exists.
type VolumeWriter interface {
	VolumeExists(ctx context.Context, volume string) (bool, error)
	WriteVolume(ctx context.Context, volume string, r io.Reader, replace bool) error
}

// RestoreOptions says where an archive goes back to.
type RestoreOptions struct {
	ConfigDir string
	DataDir   string
	Writer    VolumeWriter // nil skips volumes, with a note in the log
	// Force replaces files and volumes that exist. Without it, Restore
	// changes nothing when anything would be overwritten.
	Force bool
	Log   io.Writer
}

// ErrExists lists what a restore without Force would overwrite.
var ErrExists = errors.New("already exists")

// Restore puts an archive written by Write back. It reads the archive
// twice: once to check every name and what would be overwritten, then to
// write.
func Restore(ctx context.Context, archive string, opts RestoreOptions) error {
	if opts.Log == nil {
		opts.Log = io.Discard
	}
	var conflicts []string
	volumes := map[string]bool{}
	err := walk(archive, func(kind, rel string, hdr *tar.Header, _ io.Reader) error {
		switch kind {
		case "config", "state":
			if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeDir {
				return fmt.Errorf("%s: only files and directories can be restored there", hdr.Name)
			}
			dst := filepath.Join(opts.dir(kind), filepath.FromSlash(rel))
			if hdr.Typeflag == tar.TypeReg {
				if _, err := os.Lstat(dst); err == nil {
					conflicts = append(conflicts, dst)
				}
			}
		case "volume":
			name, _, _ := strings.Cut(rel, "/")
			if !volumes[name] && opts.Writer != nil {
				volumes[name] = true
				exists, err := opts.Writer.VolumeExists(ctx, name)
				if err != nil {
					return err
				}
				if exists {
					conflicts = append(conflicts, "volume "+name)
				}
			}
			volumes[name] = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(conflicts) > 0 && !opts.Force {
		return fmt.Errorf("%w (use -force to replace them): %s", ErrExists, strings.Join(conflicts, ", "))
	}

	// Volumes stream into a helper one after another: entries of one
	// volume are contiguous in an archive Write made.
	var cur *volumeStream
	finish := func() error {
		if cur == nil {
			return nil
		}
		err := cur.close()
		fmt.Fprintf(opts.Log, "volume  %s\n", cur.name)
		cur = nil
		return err
	}
	err = walk(archive, func(kind, rel string, hdr *tar.Header, body io.Reader) error {
		switch kind {
		case "config", "state":
			if err := finish(); err != nil {
				return err
			}
			return restoreFile(filepath.Join(opts.dir(kind), filepath.FromSlash(rel)), hdr, body)
		case "volume":
			if opts.Writer == nil {
				return nil
			}
			name, inner, _ := strings.Cut(rel, "/")
			if cur == nil || cur.name != name {
				if err := finish(); err != nil {
					return err
				}
				cur = openVolume(ctx, opts.Writer, name, opts.Force)
			}
			h := *hdr
			h.Name = inner
			if inner == "" {
				h.Name = "." // the volume's root: owner and mode
			}
			if h.Typeflag == tar.TypeDir {
				h.Name += "/"
			}
			return cur.add(&h, body)
		}
		return nil
	})
	if ferr := finish(); err == nil {
		err = ferr
	}
	if err != nil {
		return err
	}
	if opts.Writer == nil && len(volumes) > 0 {
		fmt.Fprintf(opts.Log, "skipped %d volumes: no Docker daemon\n", len(volumes))
	}
	return nil
}

func (o RestoreOptions) dir(kind string) string {
	if kind == "config" {
		return o.ConfigDir
	}
	return o.DataDir
}

// walk calls fn for each entry of the archive with its kind (config,
// state, volume or manifest) and its clean path within that part.
func walk(archive string, fn func(kind, rel string, hdr *tar.Header, body io.Reader) error) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("%s: not a piway backup: %w", archive, err)
	}
	tr := tar.NewReader(gz)
	seenManifest := false
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%s: %w", archive, err)
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		if path.Clean(name) != name || path.IsAbs(name) || strings.HasPrefix(name, "../") {
			return fmt.Errorf("%s: unsafe name %q", archive, hdr.Name)
		}
		rest, ok := strings.CutPrefix(name, Root+"/")
		if !ok {
			return fmt.Errorf("%s: %q is outside %s/; not a piway backup", archive, hdr.Name, Root)
		}
		kind, rel, _ := strings.Cut(rest, "/")
		switch kind {
		case "MANIFEST.json":
			seenManifest = true
			continue
		case "config", "state":
		case "volumes":
			kind = "volume"
		default:
			return fmt.Errorf("%s: unexpected %q", archive, hdr.Name)
		}
		if rel == "" {
			continue
		}
		if err := fn(kind, rel, hdr, tr); err != nil {
			return err
		}
	}
	if !seenManifest {
		return fmt.Errorf("%s: no MANIFEST.json; not a complete piway backup", archive)
	}
	return nil
}

// restoreFile writes one config or state entry. Files are written next to
// their place and renamed, keeping the mode they had.
func restoreFile(dst string, hdr *tar.Header, body io.Reader) error {
	if hdr.Typeflag == tar.TypeDir {
		return os.MkdirAll(dst, fs.FileMode(hdr.Mode).Perm()|0o700)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(dst), ".restore-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := io.Copy(f, body); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(fs.FileMode(hdr.Mode).Perm()); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), dst)
}

// volumeStream feeds one volume's entries to a VolumeWriter.
type volumeStream struct {
	name string
	pw   *io.PipeWriter
	tw   *tar.Writer
	done chan error
	err  error // the writer's result, once received
	got  bool
}

func openVolume(ctx context.Context, w VolumeWriter, name string, replace bool) *volumeStream {
	pr, pw := io.Pipe()
	s := &volumeStream{name: name, pw: pw, tw: tar.NewWriter(pw), done: make(chan error, 1)}
	go func() {
		err := w.WriteVolume(ctx, name, pr, replace)
		pr.CloseWithError(err) // unblock the writer if the helper stopped early
		s.done <- err
	}()
	return s
}

// wait returns the VolumeWriter's result.
func (s *volumeStream) wait() error {
	if !s.got {
		s.err, s.got = <-s.done, true
	}
	if s.err != nil {
		return fmt.Errorf("volume %s: %w", s.name, s.err)
	}
	return nil
}

func (s *volumeStream) add(hdr *tar.Header, body io.Reader) error {
	err := s.tw.WriteHeader(hdr)
	if err == nil && hdr.Typeflag == tar.TypeReg {
		_, err = io.Copy(s.tw, body)
	}
	if err != nil {
		s.pw.CloseWithError(err)
		if werr := s.wait(); werr != nil {
			return werr // the helper's reason is the useful one
		}
	}
	return err
}

func (s *volumeStream) close() error {
	err := s.tw.Close()
	s.pw.CloseWithError(err)
	if werr := s.wait(); werr != nil {
		return werr
	}
	return err
}
