//go:build unix

package kb

import "syscall"

// sigZero — «нулевой» сигнал: он ничего не делает, но проверяет, жив ли
// процесс. Так снимается замок, оставшийся от упавшей индексации.
var sigZero = syscall.Signal(0)
