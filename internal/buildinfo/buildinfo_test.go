package buildinfo

import (
	"runtime/debug"
	"strings"
	"testing"
)

func info(settings ...[2]string) *debug.BuildInfo {
	bi := &debug.BuildInfo{}
	for _, s := range settings {
		bi.Settings = append(bi.Settings, debug.BuildSetting{Key: s[0], Value: s[1]})
	}
	return bi
}

// Помеченная выпуском версия сильнее всего остального: ровно её человек видит
// в Releases и ровно её называет, сообщая о беде.
func TestStampedVersionWins(t *testing.T) {
	bi := info([2]string{"vcs.revision", "a6eda30f1234"}, [2]string{"vcs.time", "2026-09-03T00:40:00Z"})
	bi.Main.Version = "(devel)"
	got := describe("v0.1.5", bi, "go1.26.5", "linux/amd64")
	if !strings.HasPrefix(got, "v0.1.5 (") {
		t.Fatalf("версия выпуска потеряна: %q", got)
	}
	if !strings.Contains(got, "a6eda30") || !strings.Contains(got, "2026-09-03") {
		t.Fatalf("нет примет сборки: %q", got)
	}
}

// go install …@v0.1.5 — ldflags там никто не задаёт, версию знает модуль.
func TestModuleVersionUsedWhenNotStamped(t *testing.T) {
	bi := info()
	bi.Main.Version = "v0.1.5"
	if got := describe(Unknown, bi, "go1.26.5", "linux/amd64"); !strings.HasPrefix(got, "v0.1.5 (") {
		t.Fatalf("версия модуля не подхвачена: %q", got)
	}
}

// «(devel)» — не версия, и выдавать её за версию нельзя.
func TestDevelIsNotAVersion(t *testing.T) {
	bi := info([2]string{"vcs.revision", "abcdef1234567"})
	bi.Main.Version = "(devel)"
	got := describe(Unknown, bi, "go1.26.5", "linux/amd64")
	if !strings.HasPrefix(got, "dev (") || strings.Contains(got, "devel") {
		t.Fatalf("«(devel)» просочилось: %q", got)
	}
}

// Изменённое дерево обязано быть видно: собранное из правленого кода нельзя
// принимать за выпуск.
func TestDirtyTreeIsVisible(t *testing.T) {
	bi := info([2]string{"vcs.revision", "abcdef1234567"}, [2]string{"vcs.modified", "true"})
	if got := describe(Unknown, bi, "go1.26.5", "linux/amd64"); !strings.Contains(got, "изменённое дерево") {
		t.Fatalf("изменённое дерево не показано: %q", got)
	}
}

// Без сведений о сборке строка всё равно должна быть осмысленной.
func TestNoBuildInfo(t *testing.T) {
	got := describe("", nil, "go1.26.5", "linux/amd64")
	if got != "dev (go1.26.5, linux/amd64)" {
		t.Fatalf("неожиданная строка: %q", got)
	}
}

// Псевдоверсия Go выпуском не является: человек прочтёт в ней «v0.1.6»
// и пойдёт искать выпуск, которого нет.
func TestPseudoVersionRejected(t *testing.T) {
	bi := info([2]string{"vcs.revision", "a6eda309f262aaa"}, [2]string{"vcs.modified", "true"})
	bi.Main.Version = "v0.1.6-0.20260902213920-a6eda309f262+dirty"
	got := describe(Unknown, bi, "go1.26.5", "linux/amd64")
	if !strings.HasPrefix(got, "dev (") {
		t.Fatalf("псевдоверсия выдана за выпуск: %q", got)
	}
	if strings.Contains(got, "20260902213920") {
		t.Fatalf("псевдоверсия просочилась в строку: %q", got)
	}
}

// А настоящая версия модуля — годится.
func TestPlainModuleVersionAccepted(t *testing.T) {
	for _, v := range []string{"v0.1.5", "v1.2.3", "v2.0.0-rc.1"} {
		bi := info()
		bi.Main.Version = v
		if got := describe(Unknown, bi, "go1.26.5", "linux/amd64"); !strings.HasPrefix(got, v+" (") {
			t.Fatalf("версия %q не принята: %q", v, got)
		}
	}
}
