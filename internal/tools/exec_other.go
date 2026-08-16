//go:build !unix

package tools

import "os/exec"

// На системах без групп процессов ограничиваемся самой командой.
func setProcessGroup(cmd *exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd, force bool) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
