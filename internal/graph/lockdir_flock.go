//go:build linux || darwin || freebsd

package graph

import (
	"os"
	"syscall"
	"time"
)

// lockDirFor занимает flock(2) на каталоге dir на время снятия брошенного
// признака (acquireLock) и возвращает снятие.
//
// Ждёт недолго: внутри — пара системных вызовов, и дольше секунд его никто
// не держит. Не вышло (файловая система без flock, например часть сетевых) —
// работа идёт как прежде, без него: хуже прежнего от этого не станет.
func lockDirFor(dir string) (release func()) {
	d, err := os.Open(dir)
	if err != nil {
		return func() {}
	}
	fd := int(d.Fd())
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(fd, syscall.LOCK_UN)
				d.Close()
			}
		}
		if err != syscall.EWOULDBLOCK || time.Now().After(deadline) {
			d.Close()
			return func() {}
		}
		time.Sleep(2 * time.Millisecond)
	}
}
