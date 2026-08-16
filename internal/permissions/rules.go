// Package permissions реализует правила доступа инструментов по образцу Claude Code.
//
// Правило записывается как Инструмент(шаблон):
//
//	Read(./**)          — чтение внутри рабочего каталога
//	Write(./src/**)     — запись в подкаталог
//	Bash(go build:*)    — команда «go build» с любыми аргументами
//	Bash(*)             — любая команда
//	Fetch(https://pkg.go.dev/**)
//
// Порядок проверки: deny → allow → ask → режим по умолчанию.
// Правило deny не обходится ничем: ни режимом yolo, ни ответом «всегда разрешать».
package permissions

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// Kind — вид проверяемого действия.
type Kind string

// Виды действий, к которым применяются правила.
const (
	KindRead  Kind = "Read"
	KindWrite Kind = "Write"
	KindBash  Kind = "Bash"
	KindFetch Kind = "Fetch"
)

// ParseKind разбирает имя вида действия из правила.
func ParseKind(s string) (Kind, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "read":
		return KindRead, nil
	case "write":
		return KindWrite, nil
	case "bash":
		return KindBash, nil
	case "fetch":
		return KindFetch, nil
	default:
		return "", fmt.Errorf("неизвестный вид действия %q (ожидается Read, Write, Bash или Fetch)", s)
	}
}

// Decision — результат проверки правил.
type Decision int

// Возможные решения.
const (
	DecisionAsk   Decision = iota // требуется подтверждение пользователя
	DecisionAllow                 // разрешено правилом
	DecisionDeny                  // запрещено правилом
)

// String возвращает решение по-русски.
func (d Decision) String() string {
	switch d {
	case DecisionAllow:
		return "разрешено"
	case DecisionDeny:
		return "запрещено"
	default:
		return "спросить"
	}
}

// Rule — одно разобранное правило.
type Rule struct {
	Kind    Kind
	Pattern string // исходный шаблон, как записан в конфиге
	Source  string // полный текст правила для показа пользователю

	// Для путей — шаблон, приведённый к абсолютному виду.
	absPattern string
	// Для Bash: префикс команды и признак «любые аргументы».
	cmdPrefix string
	cmdAny    bool
	cmdExact  string
}

// ParseRule разбирает строку правила вида "Bash(go build:*)".
// root — корень песочницы, нужен для раскрытия относительных шаблонов путей.
func ParseRule(s, root string) (Rule, error) {
	s = strings.TrimSpace(s)
	open := strings.Index(s, "(")
	if open < 0 || !strings.HasSuffix(s, ")") {
		return Rule{}, fmt.Errorf("правило %q: ожидается запись вида Инструмент(шаблон)", s)
	}
	kind, err := ParseKind(s[:open])
	if err != nil {
		return Rule{}, fmt.Errorf("правило %q: %w", s, err)
	}
	pattern := s[open+1 : len(s)-1]
	if pattern == "" {
		return Rule{}, fmt.Errorf("правило %q: пустой шаблон", s)
	}

	r := Rule{Kind: kind, Pattern: pattern, Source: s}

	switch kind {
	case KindBash:
		switch {
		case pattern == "*":
			r.cmdAny = true
		case strings.HasSuffix(pattern, ":*"):
			r.cmdPrefix = normalizeCommand(strings.TrimSuffix(pattern, ":*"))
			if r.cmdPrefix == "" {
				return Rule{}, fmt.Errorf("правило %q: пустой префикс команды", s)
			}
		default:
			r.cmdExact = normalizeCommand(strings.ReplaceAll(pattern, ":", " "))
		}
	case KindFetch:
		// URL сопоставляется как строка с шаблонами * и **.
	case KindRead, KindWrite:
		r.absPattern = absolutePattern(pattern, root)
	}
	return r, nil
}

// absolutePattern приводит шаблон пути к абсолютному виду.
func absolutePattern(pattern, root string) string {
	p := pattern
	switch {
	case p == "~" || strings.HasPrefix(p, "~/"):
		p = expandHome(p)
	case filepath.IsAbs(p):
	default:
		p = strings.TrimPrefix(p, "./")
		p = filepath.Join(root, p)
	}
	return filepath.ToSlash(p)
}

// normalizeCommand убирает лишние пробелы, чтобы сравнение команд было устойчивым.
func normalizeCommand(cmd string) string {
	return strings.Join(strings.Fields(cmd), " ")
}

// Match проверяет, подходит ли действие под правило.
// target — абсолютный путь (Read/Write), команда (Bash) или URL (Fetch).
func (r Rule) Match(kind Kind, target string) bool {
	if r.Kind != kind {
		return false
	}
	switch kind {
	case KindBash:
		cmd := normalizeCommand(target)
		switch {
		case r.cmdAny:
			return true
		case r.cmdPrefix != "":
			return cmd == r.cmdPrefix || strings.HasPrefix(cmd, r.cmdPrefix+" ")
		default:
			return cmd == r.cmdExact
		}
	case KindFetch:
		return matchGlob(r.Pattern, target)
	default:
		return matchGlob(r.absPattern, filepath.ToSlash(target))
	}
}

// matchGlob сопоставляет строку с шаблоном, где ** — любое число сегментов,
// * — любая часть одного сегмента.
func matchGlob(pattern, s string) bool {
	if pattern == "*" || pattern == "**" {
		return true
	}
	return matchSegments(strings.Split(pattern, "/"), strings.Split(s, "/"))
}

func matchSegments(pat, seg []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			// ** поглощает любое число сегментов, включая ноль.
			rest := pat[1:]
			if len(rest) == 0 {
				return true
			}
			for i := 0; i <= len(seg); i++ {
				if matchSegments(rest, seg[i:]) {
					return true
				}
			}
			return false
		}
		if len(seg) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], seg[0])
		if err != nil || !ok {
			return false
		}
		pat, seg = pat[1:], seg[1:]
	}
	return len(seg) == 0
}

// Set — скомпилированный набор правил.
type Set struct {
	deny  []Rule
	allow []Rule
	ask   []Rule
}

// Compile разбирает три списка правил. Ошибочное правило — ошибка запуска,
// а не молчаливо пропущенная строка.
func Compile(allow, ask, deny []string, root string) (*Set, error) {
	s := &Set{}
	var err error
	if s.deny, err = compileList(deny, root, "permissions.deny"); err != nil {
		return nil, err
	}
	if s.allow, err = compileList(allow, root, "permissions.allow"); err != nil {
		return nil, err
	}
	if s.ask, err = compileList(ask, root, "permissions.ask"); err != nil {
		return nil, err
	}
	return s, nil
}

func compileList(list []string, root, section string) ([]Rule, error) {
	out := make([]Rule, 0, len(list))
	for _, item := range list {
		if strings.TrimSpace(item) == "" {
			continue
		}
		r, err := ParseRule(item, root)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", section, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// Check возвращает решение по правилам и правило, которое сработало.
func (s *Set) Check(kind Kind, target string) (Decision, *Rule) {
	for i := range s.deny {
		if s.deny[i].Match(kind, target) {
			return DecisionDeny, &s.deny[i]
		}
	}
	for i := range s.allow {
		if s.allow[i].Match(kind, target) {
			return DecisionAllow, &s.allow[i]
		}
	}
	for i := range s.ask {
		if s.ask[i].Match(kind, target) {
			return DecisionAsk, &s.ask[i]
		}
	}
	return DecisionAsk, nil
}

// DeniedBy возвращает первое правило deny, под которое подходит действие.
func (s *Set) DeniedBy(kind Kind, target string) *Rule {
	for i := range s.deny {
		if s.deny[i].Match(kind, target) {
			return &s.deny[i]
		}
	}
	return nil
}

// Rules возвращает все правила по категориям — для команды /permissions.
func (s *Set) Rules() (allow, ask, deny []Rule) { return s.allow, s.ask, s.deny }

// AddAllow добавляет правило allow в набор (используется ответом «всегда разрешать»).
// Правило не добавляется, если действие запрещено списком deny.
func (s *Set) AddAllow(r Rule) { s.allow = append(s.allow, r) }
