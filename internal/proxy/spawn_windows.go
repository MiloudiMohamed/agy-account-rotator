//go:build windows

package proxy

import (
	"os"
	"os/exec"
	"syscall"
)

func setDetachAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x00000200 | 0x00000008, // CREATE_NEW_PROCESS_GROUP | DETACHED_PROCESS
	}
}

func terminateProcess(proc *os.Process) {
	_ = proc.Kill()
}
