package server

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// Cache policy for the dashboard (docs/API.md › Dashboard):
//
//   - Files under assets/ have a content hash in their name, so a new build
//     gets new URLs. They can be cached for a year without revalidation.
//   - Everything else (index.html, favicon.svg) keeps its name across builds.
//     Browsers must revalidate it on every use; the ETag makes that a cheap
//     304 when nothing changed.
const (
	cacheImmutable  = "public, max-age=31536000, immutable"
	cacheRevalidate = "no-cache"
)

// uiFile is one dashboard file, read once at startup.
type uiFile struct {
	data []byte
	etag string
	// gz is a gzip copy for text files that compress well, nil otherwise.
	// The files never change while piway runs, so compressing them once at
	// startup costs nothing per request (the JS bundle shrinks to ~30%).
	gz     []byte
	gzEtag string
}

// compressible lists the text types Vite emits. Fonts and images are
// already compressed.
var compressible = map[string]bool{".html": true, ".js": true, ".css": true, ".svg": true, ".json": true, ".txt": true}

func newUIFile(name string, data []byte) (uiFile, error) {
	f := uiFile{data: data, etag: etagOf(data)}
	if !compressible[path.Ext(name)] {
		return f, nil
	}
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return f, err
	}
	if _, err := zw.Write(data); err != nil {
		return f, err
	}
	if err := zw.Close(); err != nil {
		return f, err
	}
	// Not worth it for tiny files, where headers dominate.
	if buf.Len() < len(data)*9/10 {
		f.gz = buf.Bytes()
		f.gzEtag = etagOf(f.gz)
	}
	return f, nil
}

func etagOf(data []byte) string {
	sum := sha256.Sum256(data)
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}

// uiHandler serves the single-page dashboard from fsys.
//
// A path that names a file serves that file. Any other path without a file
// extension (/design-system, /apps/photo-gallery) serves index.html so the
// client-side router can render it. A missing path *with* an extension
// (/assets/old-hash.js) is a 404: answering it with HTML would make the
// browser try to run index.html as JavaScript.
//
// A nil fsys means the binary was built without the dashboard.
func uiHandler(fsys fs.FS) (http.Handler, error) {
	if fsys == nil {
		return http.HandlerFunc(uiMissing), nil
	}

	files := make(map[string]uiFile)
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		files[name], err = newUIFile(name, data)
		return err
	})
	if err != nil {
		return nil, err
	}
	index, ok := files["index.html"]
	if !ok {
		return nil, fs.ErrNotExist
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// path.Clean on a rooted path also removes any "..", so a request
		// can never name a file outside fsys.
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")

		if f, ok := files[name]; ok && name != "index.html" {
			cache := cacheRevalidate
			if strings.HasPrefix(name, "assets/") {
				cache = cacheImmutable
			}
			serveUIFile(w, r, name, f, cache)
			return
		}
		if path.Ext(name) != "" && name != "index.html" {
			http.Error(w, "404 page not found", http.StatusNotFound)
			return
		}
		serveUIFile(w, r, "index.html", index, cacheRevalidate)
	}), nil
}

func serveUIFile(w http.ResponseWriter, r *http.Request, name string, f uiFile, cache string) {
	h := w.Header()
	h.Set("Cache-Control", cache)
	h.Set("X-Content-Type-Options", "nosniff")

	body, etag := f.data, f.etag
	if f.gz != nil {
		// Caches must key on Accept-Encoding, and each encoding has its
		// own ETag so a 304 never confirms the wrong bytes.
		h.Add("Vary", "Accept-Encoding")
		if acceptsGzip(r) {
			body, etag = f.gz, f.gzEtag
			h.Set("Content-Encoding", "gzip")
		}
	}
	h.Set("ETag", etag)
	// ServeContent sets Content-Type from the extension, answers
	// If-None-Match with 304 using the ETag above, handles Range and HEAD.
	// Embedded files have no modification time, hence the zero time.
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(body))
}

// acceptsGzip reports whether the client listed gzip in Accept-Encoding
// without refusing it (gzip;q=0).
func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(coding), "gzip") {
			q := strings.ReplaceAll(params, " ", "")
			return q != "q=0" && q != "q=0.0" && q != "q=0.00" && q != "q=0.000"
		}
	}
	return false
}

func uiMissing(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", cacheRevalidate)
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, `<!doctype html><meta charset="utf-8"><title>piway</title>
<p>This piway binary was built without the dashboard. Build it with <code>make build</code>,
or run the development server in <code>web/</code>. The API is available under <code>/api/v1</code>.</p>
`)
}
