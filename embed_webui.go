//go:build webui

package gatewayembed

import (
	"embed"
	"io/fs"
)

//go:embed all:web/dist
var embeddedWebDist embed.FS

func embeddedWebUI() fs.FS {
	sub, err := fs.Sub(embeddedWebDist, "web/dist")
	if err != nil {
		return nil
	}
	return sub
}
