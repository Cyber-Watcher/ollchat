package permissions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrOutsideSandbox возвращается при попытке выйти за корень песочницы.
var ErrOutsideSandbox = errors.New("путь вне рабочего каталога")

// ErrSymlinkEscape возвращается, когда путь ведёт через символическую ссылку,
// а переход по ссылкам запрещён настройкой sandbox.follow_symlinks.
var ErrSymlinkEscape = errors.New("путь проходит через символическую ссылку")

// Sandbox ограничивает файловые операции рабочим каталогом.
type Sandbox struct {
	root           string // абсолютный путь с раскрытыми символическими ссылками
	displayRoot    string // как показывать пользователю
	allowOutside   bool
	followSymlinks bool
	maxFileBytes   int64
	maxPDFBytes    int64
}

// defaultMaxPDFMB — предел размера документа PDF по умолчанию. Он отдельный и
// намного больше общего: PDF почти всегда весит мегабайты, а в контекст уходит
// не файл, а извлечённый из него текст, и его размер ограничен уже иначе.
const defaultMaxPDFMB = 64

// NewSandbox создаёт песочницу с корнем root.
func NewSandbox(root string, allowOutside, followSymlinks bool, maxFileKB int) (*Sandbox, error) {
	root = expandHome(root)
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("определение корня песочницы: %w", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// Корень может не существовать — это ошибка настройки, о ней нужно сказать сразу.
		return nil, fmt.Errorf("корень песочницы %s: %w", abs, err)
	}
	if maxFileKB <= 0 {
		maxFileKB = 512
	}
	return &Sandbox{
		root:           real,
		displayRoot:    abs,
		allowOutside:   allowOutside,
		followSymlinks: followSymlinks,
		maxFileBytes:   int64(maxFileKB) * 1024,
		maxPDFBytes:    int64(defaultMaxPDFMB) << 20,
	}, nil
}

// SetMaxPDFMB задаёт предел размера документа PDF в мегабайтах.
func (s *Sandbox) SetMaxPDFMB(mb int) {
	if mb > 0 {
		s.maxPDFBytes = int64(mb) << 20
	}
}

// MaxPDFBytes — предел размера документа PDF для чтения.
func (s *Sandbox) MaxPDFBytes() int64 { return s.maxPDFBytes }

// Root возвращает корень песочницы для показа в интерфейсе.
func (s *Sandbox) Root() string { return s.displayRoot }

// RealRoot возвращает корень с раскрытыми символическими ссылками.
func (s *Sandbox) RealRoot() string { return s.root }

// MaxFileBytes — предел размера файла для чтения и записи.
func (s *Sandbox) MaxFileBytes() int64 { return s.maxFileBytes }

// AllowOutside сообщает, разрешён ли выход за пределы корня.
func (s *Sandbox) AllowOutside() bool { return s.allowOutside }

// Resolve приводит путь к абсолютному виду и проверяет, что он не выходит за
// границы песочницы. Возвращает путь, пригодный для файловых операций.
func (s *Sandbox) Resolve(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("пустой путь")
	}
	p = expandHome(p)

	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(s.displayRoot, abs)
	}
	abs = filepath.Clean(abs)

	if s.allowOutside {
		return abs, nil
	}

	// Лексическая проверка отсекает очевидные «../» до обращения к диску.
	if !isWithin(s.displayRoot, abs) && !isWithin(s.root, abs) {
		return "", fmt.Errorf("%w: %s", ErrOutsideSandbox, abs)
	}

	// Реальная проверка: раскрываем символические ссылки у существующей части пути.
	real, existed, err := resolveExisting(abs)
	if err != nil {
		return "", err
	}
	if !isWithin(s.root, real) {
		return "", fmt.Errorf("%w: %s → %s", ErrOutsideSandbox, abs, real)
	}
	if !s.followSymlinks && existed {
		// Путь внутри корня, но пролегает через ссылку — сравниваем с ожидаемым
		// расположением относительно раскрытого корня.
		expected := abs
		if isWithin(s.displayRoot, abs) {
			rel, relErr := filepath.Rel(s.displayRoot, abs)
			if relErr == nil {
				expected = filepath.Join(s.root, rel)
			}
		}
		if real != expected {
			return "", fmt.Errorf("%w: %s → %s", ErrSymlinkEscape, abs, real)
		}
	}
	return abs, nil
}

// resolveExisting раскрывает символические ссылки у самой длинной существующей
// части пути и приписывает к результату остаток. Флаг existed показывает, что
// хотя бы часть пути существует на диске.
func resolveExisting(abs string) (string, bool, error) {
	rest := ""
	cur := abs
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			if rest == "" {
				return real, true, nil
			}
			return filepath.Join(real, rest), true, nil
		}
		if !os.IsNotExist(err) {
			return "", false, fmt.Errorf("проверка пути %s: %w", cur, err)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			// Дошли до корня файловой системы, ничего не существует.
			return abs, false, nil
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// isWithin сообщает, лежит ли path внутри root (или совпадает с ним).
func isWithin(root, path string) bool {
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

// Rel возвращает путь относительно корня песочницы — для компактного показа.
func (s *Sandbox) Rel(abs string) string {
	if rel, err := filepath.Rel(s.displayRoot, abs); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return abs
}

// expandHome раскрывает ведущий "~".
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return p
		}
		if p == "~" {
			return home
		}
		return filepath.Join(home, p[2:])
	}
	return p
}
