// Package gatewayembed exposes the admin console static assets.
//
// `//go:embed` fails the build when a pattern matches nothing, and web/dist is
// gitignored and recreated (emptied) by Vite, so it cannot be embedded
// unconditionally without breaking `go build ./...` on a fresh clone:
//
//   - default build: only the tracked webfallback/ page is embedded; it explains
//     how to build the console.
//   - `-tags webui` (used by deploy scripts, Dockerfile and CI after
//     `npm run build`): web/dist is embedded via embed_webui.go.
//   - AG_WEB_DIR=<dir> (optional, for UI development): files are served from that
//     directory first, falling back to the embedded assets.
package gatewayembed

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed webfallback
var embeddedFallback embed.FS

type liveOrEmbeddedFS struct {
	diskDir  string
	embedded fs.FS
}

func (l *liveOrEmbeddedFS) Open(name string) (fs.File, error) {
	if l.diskDir != "" && fs.ValidPath(name) {
		diskPath := filepath.Join(l.diskDir, filepath.FromSlash(name))
		if f, err := os.Open(diskPath); err == nil {
			return f, nil
		}
	}
	return l.embedded.Open(name)
}

func WebDistFS() (fs.FS, error) {
	var base fs.FS
	if ui := embeddedWebUI(); ui != nil {
		base = ui
	} else {
		sub, err := fs.Sub(embeddedFallback, "webfallback")
		if err != nil {
			return nil, err
		}
		base = sub
	}
	if dir := strings.TrimSpace(os.Getenv("AG_WEB_DIR")); dir != "" {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return &liveOrEmbeddedFS{diskDir: dir, embedded: base}, nil
		}
	}
	return base, nil
}
