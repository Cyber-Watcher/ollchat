//go:build !(linux || darwin || freebsd)

package graph

// lockDirFor — на системах без flock(2) снятие брошенного признака идёт
// без него, как до 07.10.2026 (см. lockdir_flock.go).
func lockDirFor(string) (release func()) { return func() {} }
