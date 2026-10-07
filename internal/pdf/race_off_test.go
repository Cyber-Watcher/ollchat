//go:build !race

package pdf

// raceEnabled — тесты собраны с детектором гонок (см. race_on_test.go).
const raceEnabled = false
