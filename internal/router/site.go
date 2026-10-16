package router

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
)

// siteHandler serves the files in dir. The deploy pipeline copies a site
// into dir without symbolic links, so nothing outside it can be served.
//
// With spa, a request for a page that is not a file gets the site's
// index.html, for apps that route in the browser. Only pages: a missing
// file such as /assets/old.js is a 404, not HTML a browser then fails to
// run as a script (#144).
//
// Caching: build tools put a content hash in the name of files they
// emit (index-BIy_QpcH.js), so those never change and are cached for a
// year; HTML, and every other file, is revalidated (Last-Modified), so a
// deploy shows at once.
func siteHandler(dir string, spa bool) http.Handler {
	fsys := os.DirFS(dir)
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "."
		}
		fi, err := fs.Stat(fsys, name)
		if spa && (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
			errors.Is(err, fs.ErrNotExist) && wantsPage(name, r.Header.Get("Accept")) {
			// Client-side routing: the app's index.html renders the path.
			r = r.Clone(r.Context())
			r.URL.Path, r.URL.RawPath = "/", ""
			name, fi, err = "index.html", nil, nil
		}
		if err == nil && fi != nil && fi.Mode().IsRegular() && hashed(name) {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

// wantsPage reports whether a request for the missing name is for a page:
// a browser navigation (it asks for text/html, whatever the path looks
// like, as for /docs/v1.2), or a path without a file extension from a
// client that takes any type.
func wantsPage(name, accept string) bool {
	if strings.Contains(accept, "text/html") {
		return true
	}
	anything := accept == "" || strings.Contains(accept, "*/*")
	return anything && path.Ext(name) == ""
}

// contentHash matches the hash build tools put before the extension:
// index-BIy_QpcH.js (Vite, esbuild), main.3f2a9c1d.js (webpack).
var contentHash = regexp.MustCompile(`[.-]([A-Za-z0-9_-]{8,})\.[A-Za-z0-9]+$`)

// hashed reports whether the file's name carries a content hash. A long
// plain word is not one (normalize-document.css), nor is a date
// (photo-2024-05-01.jpg): the hash must have a letter, and a digit or both
// upper and lower case letters.
func hashed(name string) bool {
	m := contentHash.FindStringSubmatch(path.Base(name))
	if m == nil {
		return false
	}
	h := m[1]
	hasDigit := strings.ContainsAny(h, "0123456789")
	hasUpper := h != strings.ToLower(h)
	hasLower := h != strings.ToUpper(h)
	return (hasUpper || hasLower) && (hasDigit || (hasUpper && hasLower))
}
