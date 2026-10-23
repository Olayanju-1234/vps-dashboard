// Package web holds the dashboard's frontend, compiled into the binary.
// Hand-written HTML, CSS and JS: no build step, no npm.
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var assets embed.FS

// FS returns the assets rooted at static/.
func FS() fs.FS {
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		panic("web: embedded assets missing: " + err.Error())
	}
	return sub
}
