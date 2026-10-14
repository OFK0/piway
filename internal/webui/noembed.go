//go:build !embedui

package webui

import "io/fs"

// FS returns nil: this binary was built without the dashboard.
func FS() fs.FS { return nil }
