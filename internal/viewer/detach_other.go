//go:build !unix

package viewer

import "os/exec"

// На системах без групп процессов делать нечего.
func setProcessGroup(*exec.Cmd) {}
