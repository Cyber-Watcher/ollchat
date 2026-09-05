// Package buildinfo отвечает на вопрос «какая это сборка».
//
// **Зачем отдельно.** Версия попадает в бинарь тремя разными путями, и путать
// их дорого:
//
//  1. выпуск — `scripts/build-dist.sh` подставляет тег ключом
//     `-ldflags "-X main.version=v0.1.5"`; ровно эту строку видит человек,
//     скачавший архив из Releases;
//  2. `go install github.com/…/cmd/ollchat@v0.1.5` — версию знает модуль,
//     она лежит в `debug.BuildInfo.Main.Version`, а ldflags там никто не задаёт;
//  3. сборка руками (`go build`) — версии нет вообще, зато есть отпечаток
//     репозитория: ревизия, время и признак изменённого дерева.
//
// До 03.09.2026 умолчанием стояло `1.0.0`. Такого выпуска никогда не было —
// теги идут до v0.1.5, — и любая ручная сборка представлялась несуществующим
// релизом. По такой строке нельзя ответить на единственный вопрос, ради
// которого её и спрашивают: «что именно у меня запущено».
package buildinfo

import (
	"fmt"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
)

// pseudoVersion — признак псевдоверсии Go: между дефисами четырнадцать цифр
// времени сборки, дальше ревизия. Такой «версии» в Releases нет и не будет.
var pseudoVersion = regexp.MustCompile(`-[0-9]{14}-`)

// isRelease — годится ли строка как версия выпуска.
//
// Go 1.24+ подставляет в BuildInfo.Main.Version псевдоверсию даже для сборки
// руками: `v0.1.6-0.20260902213920-a6eda309f262+dirty`. Выдавать её человеку
// нельзя — он прочтёт «v0.1.6» и станет искать несуществующий выпуск.
func isRelease(v string) bool {
	if v == "" || v == "(devel)" || !strings.HasPrefix(v, "v") {
		return false
	}
	return !strings.Contains(v, "+") && !pseudoVersion.MatchString(v)
}

// Unknown — значение version, когда сборку никто не помечал.
const Unknown = "dev"

// Describe собирает строку версии: сама версия и приметы сборки в скобках.
//
//	v0.1.5 (a6eda30, 2026-09-03, go1.26.5, linux/amd64)
//	dev (a6eda30, изменённое дерево, 2026-09-03, go1.26.5, linux/amd64)
func Describe(stamped string) string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		bi = nil
	}
	return describe(stamped, bi, runtime.Version(), runtime.GOOS+"/"+runtime.GOARCH)
}

func describe(stamped string, bi *debug.BuildInfo, goVer, platform string) string {
	ver := strings.TrimSpace(stamped)
	if ver == "" {
		ver = Unknown
	}

	var rev, when string
	var dirty bool
	if bi != nil {
		// Версия модуля годится, только если сборку не пометили ключом: у
		// выпуска ldflags точнее, а «(devel)» не значит ничего.
		if ver == Unknown {
			if m := strings.TrimSpace(bi.Main.Version); isRelease(m) {
				ver = m
			}
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.time":
				when = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
	}

	var parts []string
	if len(rev) >= 7 {
		parts = append(parts, rev[:7])
	} else if rev != "" {
		parts = append(parts, rev)
	}
	if dirty {
		// Отдельным словом, а не суффиксом `-dirty`: человек читает строку
		// глазами, и «изменённое дерево» ему понятнее знака.
		parts = append(parts, "изменённое дерево")
	}
	if len(when) >= 10 {
		parts = append(parts, when[:10])
	}
	parts = append(parts, goVer, platform)

	return fmt.Sprintf("%s (%s)", ver, strings.Join(parts, ", "))
}
