//go:build unix

package tools

import (
	"os/exec"
	"syscall"
)

// setProcessGroup помещает запускаемую команду в собственную группу процессов.
//
// Без этого мы можем убить только саму команду, а порождённых ею потомков — нет.
// Именно на этом приложение однажды зависло: `dotnet run` запускает собранное
// приложение отдельным процессом, и оно продолжало держать канал вывода после
// того, как сам dotnet был снят по таймауту.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup снимает всё дерево процессов команды.
//
// Отрицательный pid в Kill означает «всей группе процессов». Сначала мягко,
// сигналом TERM, затем — если не помогло — принудительно.
func killProcessGroup(cmd *exec.Cmd, force bool) {
	if cmd.Process == nil {
		return
	}
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}
	pgid := cmd.Process.Pid
	if err := syscall.Kill(-pgid, sig); err != nil {
		// Группы может не оказаться — тогда бьём по самому процессу.
		_ = cmd.Process.Signal(sig)
	}
}
