package gatewayembed

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed all:web/dist
var embeddedWebDist embed.FS

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
	sub, err := fs.Sub(embeddedWebDist, "web/dist")
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat("web/dist"); err == nil && st.IsDir() {
		return &liveOrEmbeddedFS{
			diskDir:  "web/dist",
			embedded: sub,
		}, nil
	}
	return sub, nil
}
