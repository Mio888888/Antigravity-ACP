package version

import (
	"os"
	"strings"
)

var (
	// Version is the semantic version of Antigravity ACP Gateway.
	// Overridden at release build time via -ldflags "-X antigravity-gateway/internal/version.Version=vX.Y.Z".
	Version = "v1.6.0"

	// Commit is the optional git commit SHA embedded at build time.
	Commit = ""

	// Repo is the GitHub owner/name used to query prebuilt release binaries.
	Repo = "Mio888888/Antigravity-ACP"
)

// EffectiveRepo returns AG_UPDATE_REPO if set, otherwise Repo.
func EffectiveRepo() string {
	if env := strings.TrimSpace(os.Getenv("AG_UPDATE_REPO")); env != "" {
		return env
	}
	return strings.TrimSpace(Repo)
}
