//go:build windows

package updater

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// CleanupStaleExecutableArtifacts removes any leftover .old executable renamed during a prior Windows update.
func CleanupStaleExecutableArtifacts(execPath string) {
	if execPath == "" {
		if p, err := os.Executable(); err == nil {
			execPath = p
		}
	}
	if execPath != "" {
		_ = os.Remove(execPath + ".old")
	}
}

// replaceExecutableFile renames the running .exe to .old before moving newBinPath into place.
func replaceExecutableFile(newBinPath, targetExecPath string) error {
	oldBackup := targetExecPath + ".old"
	_ = os.Remove(oldBackup)

	hadOriginal := false
	if _, err := os.Stat(targetExecPath); err == nil {
		if err := os.Rename(targetExecPath, oldBackup); err != nil {
			return fmt.Errorf("rename running executable %s -> %s: %w", targetExecPath, oldBackup, err)
		}
		hadOriginal = true
	}

	if err := os.Rename(newBinPath, targetExecPath); err != nil {
		if hadOriginal {
			_ = os.Rename(oldBackup, targetExecPath)
		}
		return fmt.Errorf("move updated executable into %s: %w", targetExecPath, err)
	}
	return nil
}

// ExecOrSpawnSelf starts the updated executable after the caller has released ports and DB handles, then exits.
func ExecOrSpawnSelf(targetExecPath string, argv []string, envv []string) error {
	var args []string
	if len(argv) > 1 {
		args = argv[1:]
	}
	cmd := exec.Command(targetExecPath, args...)
	cmd.Env = envv
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if wd, err := os.Getwd(); err == nil && wd != "" {
		cmd.Dir = wd
	} else {
		cmd.Dir = filepath.Dir(targetExecPath)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
