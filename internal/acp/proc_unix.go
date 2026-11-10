//go:build !windows

package acp

import (
	"os/exec"
	"syscall"
	"time"
)

func configureWorkerProcAttr(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateWorkerProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	var err error
	pid := cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	waitDone := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(250 * time.Millisecond):
		err = syscall.Kill(-pid, syscall.SIGKILL)
		<-waitDone
	}
	return err
}
