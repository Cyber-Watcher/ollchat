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
// Правило deny не обходится ни режимом noask, ни ответом «всегда разрешать»;
// для bash оно сверяется с каждой командой, которую запустит строка (shell.go).
package permissions

import (
	"fmt"
	"github.com/Cyber-Watcher/ollchat/internal/fsx"
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
	// Для запретов Bash — тот же шаблон с именем программы без каталога
	// и кавычек: запрет сверяется с командами после разбора (shell.go),
	// и Bash(/usr/bin/curl:*) обязан ловить и `curl`, и `/bin/curl`.
	denyPrefix string
	denyExact  string
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
			r.denyPrefix = canonicalPattern(r.cmdPrefix)
		default:
			r.cmdExact = normalizeCommand(strings.ReplaceAll(pattern, ":", " "))
			r.denyExact = canonicalPattern(r.cmdExact)
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
		p = fsx.ExpandHome(p)
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

// canonicalPattern приводит шаблон команды к виду, в котором разбор отдаёт
// найденные команды: кавычки сняты, у программы отрезан каталог.
//
// Присваивания в начале шаблона снимаются, как и у команды: запрет
// Bash(LANG=C rm:*) иначе не совпал бы ни с одной найденной командой.
func canonicalPattern(pattern string) string {
	words, err := SplitWords(pattern)
	if err != nil {
		return pattern
	}
	for len(words) > 1 && isAssignment(words[0]) {
		words = words[1:]
	}
	if len(words) == 0 {
		return pattern
	}
	words[0] = programName(words[0])
	return normalizeCommand(strings.Join(words, " "))
}

// matchCommand сверяет запрет Bash с командой, найденной разбором строки.
func (r Rule) matchCommand(cmd string) bool {
	switch {
	case r.cmdAny:
		return true
	case r.denyPrefix != "":
		return cmd == r.denyPrefix || strings.HasPrefix(cmd, r.denyPrefix+" ")
	default:
		return cmd == r.denyExact
	}
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
//
// Запрет для bash сверяется с каждой командой, которую запустит строка,
// а разрешение — по-прежнему с самой строкой: `Bash(ls:*)` разрешает `ls`,
// но не `/tmp/ls` и не `env ls` — разрешение расширять незачем.
func (s *Set) Check(kind Kind, target string) (Decision, *Rule) {
	if r := s.DeniedBy(kind, target); r != nil {
		return DecisionDeny, r
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
// Для bash это значит: под которое подходит хоть одна команда строки.
func (s *Set) DeniedBy(kind Kind, target string) *Rule {
	if kind == KindBash {
		return s.bashDeny(target).rule
	}
	for i := range s.deny {
		if s.deny[i].Match(kind, target) {
			return &s.deny[i]
		}
	}
	return nil
}

// bashVerdict — итог сверки командной строки с запретами.
type bashVerdict struct {
	rule *Rule  // сработавшее правило deny
	part string // команда, на которой оно сработало
	// unknown — команда, имя которой станет известно только при запуске:
	// сверить её с запретами нельзя, и решать должен человек.
	unknown string
	// named — ключ, которым программа запускает другую (`go -toolexec`):
	// названная программа сверена с запретами, а решает человек.
	named string
}

// bashDeny сверяет с запретами каждую команду, которую запустит строка:
// части составной команды, подстановки, тела `sh -c` и eval, команды
// внутри обёрток (env, nice, timeout, xargs, find -exec …).
func (s *Set) bashDeny(target string) bashVerdict {
	hasBash := false
	for i := range s.deny {
		if s.deny[i].Kind != KindBash {
			continue
		}
		hasBash = true
		// Bash(*) в запретах — «никаких команд»: даже строка без программы
		// (`> файл` обнуляет файл) под него попадает.
		if s.deny[i].cmdAny {
			return bashVerdict{rule: &s.deny[i], part: target}
		}
	}
	if !hasBash {
		// Сверять не с чем: имя, вычисляемое при запуске, тоже ничего не обходит.
		// Но ключ, которым программа запускает другую, решает человек и без
		// запретов (слово владельца 08.10.2026).
		return bashVerdict{named: scanCommands(target).named}
	}
	scan := scanCommands(target)
	for _, c := range scan.found {
		for i := range s.deny {
			if s.deny[i].Kind == KindBash && s.deny[i].matchCommand(c.text) {
				return bashVerdict{rule: &s.deny[i], part: c.text}
			}
		}
	}
	return bashVerdict{unknown: scan.unknown, named: scan.named}
}

// Rules возвращает все правила по категориям — для команды /permissions.
func (s *Set) Rules() (allow, ask, deny []Rule) { return s.allow, s.ask, s.deny }

// AddAllow добавляет правило allow в набор (используется ответом «всегда разрешать»).
// Правило не добавляется, если действие запрещено списком deny.
func (s *Set) AddAllow(r Rule) { s.allow = append(s.allow, r) }
