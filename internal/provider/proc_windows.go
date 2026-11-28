//go:build windows

package provider

import (
	"os/exec"
	"syscall"
)

const createNewProcessGroup = 0x00000200

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

func terminateProc(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func killProc(cmd *exec.Cmd) { terminateProc(cmd) }
