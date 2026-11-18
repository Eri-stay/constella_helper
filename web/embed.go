// Package web embeds the admin UI (plain HTML, CSS and ES modules, no build
// step) so the gateway binary carries it.
package web

import (
	"embed"
	"io/fs"
)

//go:embed index.html style.css js
var files embed.FS

// FS returns the UI files rooted at the web directory.
func FS() fs.FS { return files }
