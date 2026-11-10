//go:build windows

package acp

import (
	"os/exec"
	"time"
)

func configureWorkerProcAttr(cmd *exec.Cmd) {
	// Windows process creation does not use Unix Setpgid
}

func terminateWorkerProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	var err error
	_ = cmd.Process.Kill()
	waitDone := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(250 * time.Millisecond):
		err = cmd.Process.Kill()
		<-waitDone
	}
	return err
}
