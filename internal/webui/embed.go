//go:build embedui

package webui

import (
	"embed"
	"io/fs"
)

// all: keeps files whose names start with "." or "_", which Vite may emit.
//
//go:embed all:dist
var dist embed.FS

// FS returns the dashboard files, rooted at index.html.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // "dist" is a constant that go:embed has verified
	}
	return sub
}
