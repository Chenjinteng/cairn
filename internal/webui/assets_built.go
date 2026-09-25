//go:build webui

package webui

import (
	"embed"
	"io/fs"
)

// dist is the vite build output (web/ builds directly into this directory
// via build.outDir = ../internal/webui/dist). `all:` includes files starting
// with _ or . too (vite emits none today, but be safe).
//
//go:embed all:dist
var dist embed.FS

func assetsFS() (fs.FS, bool) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, false
	}
	// Sanity check: index.html must exist, otherwise the embed is useless.
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, false
	}
	return sub, true
}
