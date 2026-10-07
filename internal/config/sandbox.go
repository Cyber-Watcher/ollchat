package config

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

// IsolationBwrap — изоляция команд bash через bubblewrap.
const IsolationBwrap = "bwrap"

// isolationGOOS — система, для которой проверяется sandbox.isolation.
// Переменная, а не константа, — шов для теста: отказ на macOS проверяется
// и в Linux.
var isolationGOOS = runtime.GOOS

// validate проверяет настройки изоляции и раскрывает ~ в путях isolation_hide.
//
// Ошибка здесь — ошибка запуска, а не тихое выключение: человек, вписавший
// isolation = "bwrap", рассчитывает на изоляцию, и молча работать без неё
// хуже, чем не запуститься.
func (s *Sandbox) validate() error {
	switch s.Isolation {
	case "":
	case IsolationBwrap:
		if isolationGOOS != "linux" {
			return fmt.Errorf("sandbox.isolation = %q: bubblewrap есть только в Linux, "+
				"в %s изоляции команд нет — уберите настройку", s.Isolation, isolationGOOS)
		}
	default:
		return fmt.Errorf("sandbox.isolation = %q: допустимо \"\" (выключена) или %q",
			s.Isolation, IsolationBwrap)
	}
	for i, p := range s.IsolationHide {
		p = ExpandPath(strings.TrimSpace(p))
		if !filepath.IsAbs(p) {
			return fmt.Errorf("sandbox.isolation_hide: путь %q должен быть абсолютным или начинаться с ~",
				s.IsolationHide[i])
		}
		s.IsolationHide[i] = filepath.Clean(p)
	}
	return nil
}
