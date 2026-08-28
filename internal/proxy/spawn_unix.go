//go:build !windows

package proxy

import (
	"os"
	"os/exec"
	"syscall"
)

func setDetachAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func terminateProcess(proc *os.Process) {
	_ = proc.Signal(syscall.SIGTERM)
}
