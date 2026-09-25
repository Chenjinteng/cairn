//go:build !webui

package webui

import "io/fs"

// assetsFS reports "no embedded assets" when built without the webui tag, so
// Handler() degrades to a 404-with-hint instead of failing the build.
func assetsFS() (fs.FS, bool) { return nil, false }
