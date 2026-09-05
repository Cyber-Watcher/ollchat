//go:build unix

package viewer

import (
	"os/exec"
	"syscall"
)

// setProcessGroup уводит просмотрщик в собственную группу процессов.
//
// Без этого Ctrl+C в ollchat уходит всей группе переднего плана — вместе
// с только что открытым просмотрщиком, чего человек никак не ожидает.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
