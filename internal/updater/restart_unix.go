//go:build !windows

package updater

import (
	"os"
	"syscall"
)

// CleanupStaleExecutableArtifacts is a no-op on Unix because os.Rename replaces inodes directly.
func CleanupStaleExecutableArtifacts(_ string) {}

// replaceExecutableFile atomically swaps newBinPath over targetExecPath on Unix.
func replaceExecutableFile(newBinPath, targetExecPath string) error {
	if err := os.Chmod(newBinPath, 0755); err != nil {
		return err
	}
	return os.Rename(newBinPath, targetExecPath)
}

// ExecOrSpawnSelf replaces the current process image in-place via execve.
func ExecOrSpawnSelf(targetExecPath string, argv []string, envv []string) error {
	if len(argv) == 0 {
		argv = []string{targetExecPath}
	}
	return syscall.Exec(targetExecPath, argv, envv)
}
