//go:build !webui

package gatewayembed

import "io/fs"

func embeddedWebUI() fs.FS { return nil }
