package router

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

// siteHandler serves the files in dir. The deploy pipeline copies a site
// into dir without symbolic links, so nothing outside it can be served.
func siteHandler(dir string, spa bool) http.Handler {
	fsys := os.DirFS(dir)
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if spa && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
			if name == "" {
				name = "."
			}
			if _, err := fs.Stat(fsys, name); errors.Is(err, fs.ErrNotExist) {
				// Client-side routing: the app's index.html renders the path.
				r = r.Clone(r.Context())
				r.URL.Path, r.URL.RawPath = "/", ""
			}
		}
		files.ServeHTTP(w, r)
	})
}
