// Package web embeds the single-page app that `make ui` builds into web/dist
// (design D13, D14). Without a build, dist holds only the committed
// placeholder, so `go build` and `go test` never need Node.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var files embed.FS

// Dist returns the built UI rooted at web/dist: index.html and assets/*, or
// only the placeholder .gitkeep when the UI has not been built.
func Dist() fs.FS {
	dist, err := fs.Sub(files, "dist")
	if err != nil {
		// fs.Sub fails only for an invalid path, and "dist" is valid.
		panic(err)
	}
	return dist
}
