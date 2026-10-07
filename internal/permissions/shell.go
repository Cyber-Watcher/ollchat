package permissions

import (
	"errors"
	"runtime"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Разбор командной строки для сверки с запретами.
//
// **Почему не сравнение строк.** Запрет `Bash(rm:*)` сверялся с сырой строкой
// по префиксу и пропускал всё, что начинается не с самого «rm»: `/bin/rm -rf x`,
// `\rm`, `"rm"`, `FOO=1 rm`, `env rm`, `nice -n 5 rm`, `echo $(rm -rf x)`,
// `xargs rm`, `find . -exec rm {} \;`, `sh -c 'rm -rf x'`, `eval "rm -rf x"`.
// В режиме noask и после ответа «разрешить инструмент целиком» такие строки
// выполнялись без единого вопроса, хотя шаблон конфига обещал, что запрет
// не обходится ничем.
//
// Поэтому строка разбирается так, как её разберёт оболочка: кавычки и
// экранирование снимаются, присваивания перед командой пропускаются, имя
// программы берётся без каталога, а разбор заходит внутрь подстановок,
// `sh -c`, eval и обёрток (env, nice, timeout, xargs, find -exec …) и достаёт
// команды оттуда. Запрет сверяется с каждой найденной командой.
//
// **Чего здесь нет и быть не может.** Это ограждение, а не изоляция:
// интерпретатор (`python -c`, `perl -e`), скрипт, записанный на диск и
// запущенный следом, `make` с чужим Makefile делают что угодно, и по строке
// команды этого не увидеть. Границу для самой программы ставит изоляция
// уровня ОС — sandbox.isolation (tools/isolation.go).

// maxShellDepth — предел вложенности разбора: подстановки, `sh -c`, eval,
// обёртки. Живые команды глубже трёх-четырёх уровней не уходят; дальше —
// либо мусор, либо попытка спрятать команду, и тогда решает человек.
const maxShellDepth = 24

// foldNames — сравнивать имена программ без учёта регистра. На macOS
// файловая система по умолчанию регистра не различает, и `RM -rf x`
// запускает тот же /bin/rm. Переменная, а не константа, — шов для теста.
var foldNames = runtime.GOOS == "darwin"

// errUnclosedQuote — незакрытая кавычка: такую команду не запустить.
var errUnclosedQuote = errors.New("незакрытая кавычка в команде")

// SplitWords разбирает простую команду на аргументы так, как её запускает
// инструмент bash без оболочки: кавычки снимаются, обратная косая черта
// экранирует следующий знак (и в двойных кавычках тоже), подстановок
// и операторов нет. Составные команды (IsCompound) идут через sh -c.
//
// Функция одна и для запуска, и для сверки с запретами: разойдись они хоть
// в одном знаке, проверялось бы не то, что выполняется.
func SplitWords(s string) ([]string, error) {
	var (
		args     []string
		cur      strings.Builder
		hasToken bool
		inSingle bool
		inDouble bool
	)
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case c == '\\' && !inSingle && i+1 < len(runes):
			i++
			cur.WriteRune(runes[i])
			hasToken = true
		case c == '\'' && !inDouble:
			inSingle = !inSingle
			hasToken = true
		case c == '"' && !inSingle:
			inDouble = !inDouble
			hasToken = true
		case (c == ' ' || c == '\t') && !inSingle && !inDouble:
			if hasToken {
				args = append(args, cur.String())
				cur.Reset()
				hasToken = false
			}
		default:
			cur.WriteRune(c)
			hasToken = true
		}
	}
	if inSingle || inDouble {
		return nil, errUnclosedQuote
	}
	if hasToken {
		args = append(args, cur.String())
	}
	return args, nil
}

// shellWord — слово команды после снятия кавычек и экранирования.
type shellWord struct {
	text string
	// dynamic — значение станет известно только при запуске: $X, $(…), `…`,
	// шаблоны имён (*, ?, […]), раскрытие скобок {a,b}.
	dynamic bool
	// quoted — в слове были кавычки или экранирование: `"then"` и `\!`
	// уже не служебные слова оболочки.
	quoted bool
}

// foundCommand — команда, которую запустит строка.
type foundCommand struct {
	name string // имя программы без каталога
	text string // имя и аргументы через пробел — с этим сверяются правила
}

// commandScan собирает все команды, которые запустит командная строка.
type commandScan struct {
	found []foundCommand
	// unknown — первая команда, чьё имя станет известно только при запуске
	// (`$X -rf ~`, `$(echo rm) …`): сверить её с запретами нельзя.
	unknown string
}

// scanCommands находит все команды, которые запустит строка cmd.
//
// Режим разбора повторяет режим запуска в инструменте bash: составная
// команда выполняется через sh -c — и разбирается по правилам оболочки;
// простая запускается напрямую — и делится на слова так же, как при запуске.
// Разница не формальная: `#` у оболочки начинает комментарий, а без неё —
// обычный знак, и `env -u #X rm -rf ~`, запущенная напрямую, удаляет.
func scanCommands(cmd string) *commandScan {
	s := &commandScan{}
	if IsCompound(cmd) {
		s.script(cmd, 0)
		return s
	}
	args, err := SplitWords(cmd)
	if err != nil {
		// Незакрытая кавычка: инструмент такую команду не запустит. Разбираем
		// как оболочка — лишняя осторожность здесь ничего не стоит.
		s.script(cmd, 0)
		return s
	}
	words := make([]shellWord, len(args))
	for i, a := range args {
		words[i] = shellWord{text: a}
	}
	s.command(words, 0)
	return s
}

// script разбирает текст по правилам оболочки: тело `sh -c`, eval,
// подстановки.
func (s *commandScan) script(src string, depth int) {
	if depth > maxShellDepth {
		s.markUnknown(src)
		return
	}
	for _, words := range lexShell(src, s, depth) {
		s.command(words, depth)
	}
}

// expansions ищет подстановки команд в теле встроенного документа: само
// тело — данные, кавычки в нём — простые знаки, а $(…) и `…` выполняются.
func (s *commandScan) expansions(src string, depth int) {
	if depth > maxShellDepth {
		s.markUnknown(src)
		return
	}
	l := &shellLexer{src: []rune(src), scan: s, depth: depth}
	for l.pos < len(l.src) {
		switch l.src[l.pos] {
		case '\\':
			l.pos = min(l.pos+2, len(l.src))
		case '$':
			l.dollar(true)
		case '`':
			l.backtick()
		default:
			l.pos++
		}
	}
}

// splitWords делит текст на слова без операторов — как `env -S`.
func (s *commandScan) splitWords(src string, depth int) []shellWord {
	var out []shellWord
	for _, words := range lexShell(src, s, depth) {
		out = append(out, words...)
	}
	return out
}

func (s *commandScan) markUnknown(text string) {
	if s.unknown != "" {
		return
	}
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) > 80 {
		text = string([]rune(text)[:80]) + "…"
	}
	s.unknown = text
}

// leadingKeywords — служебные слова, за которыми начинается команда:
// `if rm …`, `then rm …`, `! rm …`, `do rm …`.
var leadingKeywords = map[string]bool{
	"!": true, "if": true, "then": true, "elif": true, "else": true,
	"while": true, "until": true, "do": true,
	"fi": true, "done": true, "esac": true, "coproc": true,
}

// notCommands — слова, за которыми команды нет: заголовки циклов
// и ветвлений, условное выражение. Подстановки внутри них уже разобраны.
var notCommands = map[string]bool{
	"for": true, "select": true, "case": true, "function": true, "[[": true,
}

// command разбирает одну простую команду: снимает присваивания и служебные
// слова, запоминает программу и заходит внутрь обёрток.
func (s *commandScan) command(words []shellWord, depth int) {
	if depth > maxShellDepth {
		s.markUnknown(joinWords(words))
		return
	}
	for len(words) > 0 {
		w := words[0]
		switch {
		case isAssignment(w.text):
			words = words[1:]
			continue
		case !w.quoted && leadingKeywords[w.text]:
			words = words[1:]
			continue
		case !w.quoted && notCommands[w.text]:
			return
		}
		break
	}
	if len(words) == 0 {
		return
	}
	if words[0].dynamic {
		s.markUnknown(joinWords(words))
		return
	}
	name := programName(words[0].text)
	args := words[1:]
	s.found = append(s.found, foundCommand{name: name,
		text: normalizeCommand(name + " " + joinWords(args))})
	s.unwrap(name, args, depth+1)
}

// programName — имя программы, как его найдёт поиск по PATH: без каталога.
func programName(word string) string {
	if i := strings.LastIndexByte(word, '/'); i >= 0 {
		word = word[i+1:]
	}
	if foldNames {
		word = strings.ToLower(word)
	}
	return word
}

// isAssignment распознаёт присваивание перед командой: FOO=1, A+=x, a[1]=y.
func isAssignment(t string) bool {
	eq := strings.IndexByte(t, '=')
	if eq <= 0 {
		return false
	}
	name := strings.TrimSuffix(t[:eq], "+")
	if i := strings.IndexByte(name, '['); i > 0 && strings.HasSuffix(name, "]") {
		name = name[:i]
	}
	return isIdentifier(name)
}

func joinWords(words []shellWord) string {
	parts := make([]string, len(words))
	for i, w := range words {
		parts[i] = w.text
	}
	return strings.Join(parts, " ")
}

// shells — оболочки, у которых ключ -c означает «выполнить строку».
var shells = map[string]bool{
	"sh": true, "bash": true, "dash": true, "zsh": true, "ksh": true,
	"mksh": true, "ash": true, "posh": true, "yash": true, "rbash": true, "fish": true,
}

// unwrap заходит внутрь программ, которые сами запускают другую команду.
//
// Список не исчерпывающий и не может им быть: запустить команду умеют
// сотни программ. Здесь — те, что встречаются в командах модели и в
// подсказках к обходу запретов. Ключи разбираются по правилам getopt: ключ
// со значением съедает следующее слово, иначе его значение приняли бы за имя
// команды и пропустили бы саму команду.
func (s *commandScan) unwrap(prog string, args []shellWord, depth int) {
	if shells[prog] {
		s.shell(args, depth)
		return
	}
	switch prog {
	case "env":
		i, seen := options(args, "uCSa", longs("unset", "chdir", "split-string", "argv0"))
		if i < len(args) && args[i].text == "-" {
			i++ // «env -» — то же, что -i
		}
		inner := args[i:]
		for _, k := range []string{"S", "split-string"} {
			if v, ok := seen[k]; ok {
				inner = append(s.splitWords(v, depth), inner...)
				break
			}
		}
		s.command(inner, depth)
	case "command":
		i, seen := options(args, "", nil)
		if _, ok := seen["v"]; ok {
			return // command -v только ищет программу, а не запускает её
		}
		if _, ok := seen["V"]; ok {
			return
		}
		s.command(args[i:], depth)
	case "exec":
		i, _ := options(args, "a", nil)
		s.command(args[i:], depth)
	case "nohup", "setsid", "builtin":
		i, _ := options(args, "", nil)
		s.command(args[i:], depth)
	case "nice":
		i, _ := options(args, "n", longs("adjustment"))
		s.command(args[i:], depth)
	case "time":
		i, _ := options(args, "fo", longs("format", "output"))
		s.command(args[i:], depth)
	case "timeout":
		i, _ := options(args, "sk", longs("signal", "kill-after"))
		s.command(after(args, i+1), depth) // первое слово — срок
	case "stdbuf":
		i, _ := options(args, "ioe", longs("input", "output", "error"))
		s.command(args[i:], depth)
	case "ionice":
		i, seen := options(args, "cnpPu", longs("class", "classdata", "pid", "pgid", "uid"))
		if hasAny(seen, "p", "P", "u", "pid", "pgid", "uid") {
			return // меняет приоритет уже работающих процессов
		}
		s.command(args[i:], depth)
	case "taskset", "chrt":
		i, seen := options(args, "", nil)
		if hasAny(seen, "p", "pid") {
			return
		}
		s.command(after(args, i+1), depth) // первое слово — маска или приоритет
	case "chroot":
		i, _ := options(args, "", longs("userspec", "groups"))
		s.command(after(args, i+1), depth) // первое слово — новый корень
	case "sudo":
		i, seen := options(args, "aCcDghpRrTtUu", longs("user", "group", "close-from", "chdir",
			"prompt", "role", "type", "command-timeout", "other-user", "chroot", "login-class",
			"auth-type", "host"))
		if hasAny(seen, "s", "i", "shell", "login") {
			s.script(joinWords(args[i:]), depth) // команда идёт в оболочку строкой
			return
		}
		s.command(args[i:], depth)
	case "doas":
		i, _ := options(args, "aCu", nil)
		s.command(args[i:], depth)
	case "su", "runuser", "script":
		if v, ok := commandOption(args, "gGsuwEOIT"); ok {
			s.script(v, depth)
		}
	case "busybox", "toybox":
		if len(args) > 0 && !strings.HasPrefix(args[0].text, "-") {
			s.command(args, depth) // первое слово — имя встроенной программы
		}
	case "xargs":
		i, _ := options(args, "aEILnPsdJRS", longs("arg-file", "max-args", "max-procs",
			"max-chars", "delimiter", "process-slot-var"))
		s.command(args[i:], depth) // без команды xargs зовёт echo
	case "find":
		s.execClauses(args, depth, false, "-exec", "-execdir", "-ok", "-okdir")
	case "fd", "fdfind":
		s.execClauses(args, depth, true, "-x", "--exec", "-X", "--exec-batch")
	case "eval":
		i, _ := options(args, "", nil)
		s.script(joinWords(args[i:]), depth)
	case "trap":
		i, _ := options(args, "", nil)
		if i < len(args) && args[i].text != "-" {
			s.script(args[i].text, depth) // строка выполнится по сигналу
		}
	case "alias":
		for _, a := range args {
			if _, val, ok := strings.Cut(a.text, "="); ok {
				s.script(val, depth)
			}
		}
	case "watch":
		i, seen := options(args, "nq", longs("interval", "equexit"))
		if hasAny(seen, "x", "exec") {
			s.command(args[i:], depth)
			return
		}
		s.script(joinWords(args[i:]), depth) // без -x watch отдаёт строку в sh -c
	case "flock":
		i, _ := options(args, "wE", longs("timeout", "conflict-exit-code"))
		rest := after(args, i+1) // первое слово — файл замка
		if len(rest) > 1 && (rest[0].text == "-c" || rest[0].text == "--command") {
			s.script(rest[1].text, depth)
			return
		}
		s.command(rest, depth)
	}
}

// shell разбирает ключи оболочки: при -c первое слово не ключ — это сама
// команда (`bash -lc "…"`, `sh -c -- '…'`), без -c — файл скрипта, внутрь
// которого по строке не заглянуть.
func (s *commandScan) shell(args []shellWord, depth int) {
	inline := false
	for i := 0; i < len(args); i++ {
		t := args[i].text
		switch {
		case t == "--" || t == "-":
			if inline && i+1 < len(args) {
				s.script(args[i+1].text, depth)
			}
			return
		case strings.HasPrefix(t, "--command="):
			s.script(strings.TrimPrefix(t, "--command="), depth)
			return
		case t == "--command":
			if i+1 < len(args) {
				s.script(args[i+1].text, depth)
			}
			return
		case t == "--rcfile" || t == "--init-file":
			i++
			continue
		case strings.HasPrefix(t, "--"):
			continue
		case len(t) > 1 && (t[0] == '-' || t[0] == '+'):
			if t[0] == '-' && strings.ContainsRune(t[1:], 'c') {
				inline = true
			}
			if strings.HasSuffix(t, "o") || strings.HasSuffix(t, "O") {
				i++ // -o pipefail, +O extglob
			}
			continue
		}
		if inline {
			s.script(t, depth)
		}
		return
	}
}

// execClauses достаёт команды из -exec … ; у find и -x … у fd.
// toEnd — команда без «;» тянется до конца строки (так у fd).
func (s *commandScan) execClauses(args []shellWord, depth int, toEnd bool, flags ...string) {
	for i := 0; i < len(args); i++ {
		if !contains(flags, args[i].text) {
			continue
		}
		j := i + 1
		for j < len(args) && args[j].text != ";" && (toEnd || args[j].text != "+") {
			j++
		}
		s.command(args[i+1:j], depth)
		i = j
	}
}

// options разбирает ключи обёртки так, как это делает getopt: связки
// коротких (`-iu ИМЯ`), значение слитно (`-n5`) или следующим словом,
// длинные с «=» или через пробел. Разбор кончается на «--» или на первом
// слове не с «-»: эти программы не переставляют аргументы, иначе ключи
// самой команды съедались бы как свои. short — короткие ключи со значением,
// long — длинные со значением. Возвращает позицию первого слова команды
// и увиденные ключи со значениями.
func options(args []shellWord, short string, long map[string]bool) (int, map[string]string) {
	seen := map[string]string{}
	i := 0
	for i < len(args) {
		t := args[i].text
		if t == "--" {
			return i + 1, seen
		}
		if len(t) < 2 || t[0] != '-' {
			return i, seen
		}
		i++
		if strings.HasPrefix(t, "--") {
			name, val, has := strings.Cut(t[2:], "=")
			if !has && long[name] && i < len(args) {
				val = args[i].text
				i++
			}
			seen[name] = val
			continue
		}
		for j := 1; j < len(t); j++ {
			c := t[j : j+1]
			if !strings.Contains(short, c) {
				seen[c] = ""
				continue
			}
			val := t[j+1:]
			if val == "" && i < len(args) {
				val = args[i].text
				i++
			}
			seen[c] = val
			break
		}
	}
	return i, seen
}

// commandOption ищет строку команды у ключа -c (--command) где угодно:
// su и script переставляют аргументы, и ключ стоит и до имени пользователя
// или файла, и после него. valued — короткие ключи со своим значением:
// в связке `-sc` буква c — значение ключа -s, а не команда.
func commandOption(args []shellWord, valued string) (string, bool) {
	for i := 0; i < len(args); i++ {
		t := args[i].text
		switch {
		case t == "--":
			return "", false
		case t == "--command":
			if i+1 < len(args) {
				return args[i+1].text, true
			}
		case strings.HasPrefix(t, "--command="):
			return strings.TrimPrefix(t, "--command="), true
		case len(t) > 1 && t[0] == '-' && t[1] != '-':
			for j := 1; j < len(t); j++ {
				if t[j] == 'c' {
					if val := t[j+1:]; val != "" || i+1 >= len(args) {
						return val, true
					}
					return args[i+1].text, true
				}
				if strings.IndexByte(valued, t[j]) >= 0 {
					if j == len(t)-1 {
						i++ // значение — следующее слово
					}
					break
				}
			}
		}
	}
	return "", false
}

func longs(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

func hasAny(seen map[string]string, keys ...string) bool {
	for _, k := range keys {
		if _, ok := seen[k]; ok {
			return true
		}
	}
	return false
}

func after(words []shellWord, i int) []shellWord {
	if i >= len(words) {
		return nil
	}
	return words[i:]
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ── Разбор текста по правилам оболочки ───────────────────────────────────────

// hereDoc — встроенный документ (`<<EOF`), чьё тело начнётся со следующей строки.
type hereDoc struct {
	delim  string
	quoted bool // ограничитель в кавычках: подстановок в теле нет
	strip  bool // <<- : ведущие табуляции снимаются
}

// Состояния конструкции case: после «in» и после «;;» идут образцы до «)»,
// за ними — команды ветви.
const (
	caseHead    = iota // case СЛОВО — до in
	casePattern        // образец до «)»
	caseBody           // команды ветви до ;; или esac
)

// shellLexer делит текст на простые команды по правилам sh/bash и попутно
// отдаёт тела подстановок разбору команд. Ошибок он не возвращает: текст,
// который оболочка отвергла бы, разбирается как получится — лишняя команда
// в списке проверяемых безвредна, пропущенная опасна.
//
// **Конец подстановки ищет тот же разбор, что и команды.** Подсчёт скобок
// в отрыве от разбора ошибается там, где ошибаться нельзя: «)» образца case,
// «)» в комментарии или во встроенном документе тело $(…) у оболочки
// не закрывают. Закрой их подсчёт — и остаток тела достался бы внешнему
// разбору, а в двойных кавычках он читается как простой текст:
// `"$(case x in *) rm -rf ~;; esac)"` прятал бы rm от проверки.
type shellLexer struct {
	src   []rune
	pos   int
	scan  *commandScan
	depth int

	// closer — разбор тела $(…), <(…) или значений массива: закончить
	// на парной «)», done — она найдена.
	closer bool
	done   bool

	cmds  [][]shellWord
	words []shellWord

	cur     strings.Builder
	inWord  bool
	dynamic bool
	quoted  bool
	bracket bool // в слове была «[» без кавычек — возможно, шаблон имени
	brace   bool // в слове была «{» без кавычек — возможно, раскрытие скобок

	redirect bool      // следующее слово — цель перенаправления, не аргумент
	heredoc  int       // 1 — следующее слово ограничитель <<, 2 — <<-
	pending  []hereDoc // тела, которые начнутся после перевода строки

	parens int   // открытые «(» подоболочек и групп
	cases  []int // вложенные case: caseHead, casePattern или caseBody
}

func lexShell(src string, s *commandScan, depth int) [][]shellWord {
	l := &shellLexer{src: []rune(src), scan: s, depth: depth}
	l.run()
	return l.cmds
}

// sub — вложенный разбор того же текста с позиции pos.
func (l *shellLexer) sub(pos int) *shellLexer {
	return &shellLexer{src: l.src, pos: pos, scan: l.scan, depth: l.depth + 1}
}

// tooDeep останавливает разбор, ушедший глубже maxShellDepth: остаток
// строки проверить нельзя, и решать будет человек.
func (l *shellLexer) tooDeep() bool {
	if l.depth+1 <= maxShellDepth {
		return false
	}
	l.scan.markUnknown(string(l.src[l.pos:]))
	l.pos = len(l.src)
	return true
}

func (l *shellLexer) peek(k int) rune {
	if l.pos+k < len(l.src) {
		return l.src[l.pos+k]
	}
	return 0
}

func (l *shellLexer) inCase(state int) bool {
	return len(l.cases) > 0 && l.cases[len(l.cases)-1] == state
}

func (l *shellLexer) run() {
	for l.pos < len(l.src) && !l.done {
		c := l.src[l.pos]
		switch {
		case c == '\\':
			l.backslash()
		case c == '\'':
			l.single()
		case c == '"':
			l.double()
		case c == '$':
			l.dollar(false)
		case c == '`':
			l.backtick()
		case c == ' ' || c == '\t':
			l.endWord()
			l.pos++
		case c == '\n':
			l.endCommand()
			l.pos++
			l.hereDocs()
		case c == '#' && !l.inWord:
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.pos++
			}
		case c == '<' || c == '>':
			l.angle()
		case c == '&' && l.peek(1) == '>':
			// &> и &>> — перенаправление обоих потоков, а не фон.
			l.endWord()
			l.pos++
			l.angle()
		case c == '(':
			l.openParen()
		case c == ')':
			l.pos++
			l.closeParen()
		case c == '|' && l.inCase(casePattern):
			l.endWord() // a|b) — второй образец той же ветви
			l.pos++
		case c == ';':
			l.endCommand()
			l.pos++
			if l.inCase(caseBody) && (l.peek(0) == ';' || l.peek(0) == '&') {
				// ;; ;& ;;& — ветвь кончилась, дальше снова образец.
				for l.peek(0) == ';' || l.peek(0) == '&' {
					l.pos++
				}
				l.cases[len(l.cases)-1] = casePattern
			}
		case c == '&' || c == '|':
			l.endCommand()
			l.pos++
		default:
			l.literal(c)
			l.pos++
		}
	}
	l.endCommand()
	if l.closer {
		// Тело встроенного документа, начатого в подстановке на одной строке
		// с её «)», за пределы подстановки не ищем: следующие строки пусть
		// разберутся как команды — лишняя проверка безвредна.
		l.pending = nil
		return
	}
	l.hereDocs()
}

func (l *shellLexer) literal(c rune) {
	l.cur.WriteRune(c)
	l.inWord = true
	switch c {
	case '*', '?':
		l.dynamic = true
	case '[':
		l.bracket = true
	case '{':
		l.brace = true
	}
}

// raw дописывает к слову исходный текст подстановки: её значение станет
// известно только при запуске.
func (l *shellLexer) raw(start int) {
	l.cur.WriteString(string(l.src[start:l.pos]))
	l.inWord = true
	l.dynamic = true
}

func (l *shellLexer) backslash() {
	if l.pos+1 >= len(l.src) {
		l.literal('\\')
		l.pos++
		return
	}
	next := l.src[l.pos+1]
	l.pos += 2
	if next == '\n' {
		return // продолжение строки
	}
	l.cur.WriteRune(next)
	l.inWord = true
	l.quoted = true
}

func (l *shellLexer) single() {
	l.pos++
	end := l.pos
	for end < len(l.src) && l.src[end] != '\'' {
		end++
	}
	l.cur.WriteString(string(l.src[l.pos:end]))
	l.pos = min(end+1, len(l.src))
	l.inWord = true
	l.quoted = true
}

func (l *shellLexer) double() {
	l.pos++
	l.inWord = true
	l.quoted = true
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch c {
		case '"':
			l.pos++
			return
		case '\\':
			switch l.peek(1) {
			case '$', '`', '"', '\\':
				l.cur.WriteRune(l.src[l.pos+1])
				l.pos += 2
				continue
			case '\n':
				l.pos += 2
				continue
			}
			l.cur.WriteRune('\\')
			l.pos++
		case '$':
			l.dollar(true)
		case '`':
			l.backtick()
		default:
			l.cur.WriteRune(c)
			l.pos++
		}
	}
}

// dollar разбирает всё, что начинается со знака доллара.
func (l *shellLexer) dollar(inDouble bool) {
	start := l.pos
	next := l.peek(1)
	switch {
	case next == '(' && l.peek(2) == '(':
		// $((…)) — арифметика: сама не команда, но внутри бывают подстановки.
		if l.tooDeep() {
			return
		}
		sub := l.sub(l.pos + 1)
		sub.arithmetic()
		l.pos = sub.pos
		l.raw(start)
	case next == '(':
		if l.tooDeep() {
			return
		}
		l.pos = l.substitution(l.pos + 2)
		l.raw(start)
	case next == '{':
		if l.tooDeep() {
			return
		}
		sub := l.sub(l.pos + 1)
		sub.parameter()
		l.pos = sub.pos
		l.raw(start)
	case next == '\'' && !inDouble:
		// $'…' — строка с экранированием в духе C: $'\x72m' — это «rm».
		l.pos += 2
		l.cur.WriteString(ansiC(l.src, &l.pos))
		l.inWord = true
		l.quoted = true
	case next == '"' && !inDouble:
		l.pos++
		l.double()
	case next == '_' || next >= 'a' && next <= 'z' || next >= 'A' && next <= 'Z':
		l.pos++
		for l.pos < len(l.src) && isNameRune(l.src[l.pos]) {
			l.pos++
		}
		l.raw(start)
	case next != 0 && strings.ContainsRune("0123456789@*#?$!-", next):
		l.pos += 2
		l.raw(start)
	default:
		l.literal('$')
		l.pos++
	}
}

func isNameRune(c rune) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// substitution разбирает тело $(…) или <(…) с позиции pos как команды
// и возвращает позицию сразу за парной «)».
func (l *shellLexer) substitution(pos int) int {
	sub := l.sub(pos)
	sub.closer = true
	sub.run()
	for _, words := range sub.cmds {
		l.scan.command(words, sub.depth)
	}
	return sub.pos
}

// arithmetic пропускает $((…)) с позиции первой «(».
func (l *shellLexer) arithmetic() {
	depth := 0
	for l.pos < len(l.src) {
		switch l.src[l.pos] {
		case '(':
			depth++
			l.pos++
		case ')':
			depth--
			l.pos++
			if depth == 0 {
				return
			}
		default:
			l.expansionRune()
		}
	}
}

// parameter пропускает ${…} с позиции «{»: ${X:-$(rm -rf ~)} выполняет rm.
func (l *shellLexer) parameter() {
	depth := 0
	for l.pos < len(l.src) {
		switch l.src[l.pos] {
		case '{':
			depth++
			l.pos++
		case '}':
			depth--
			l.pos++
			if depth == 0 {
				return
			}
		default:
			l.expansionRune()
		}
	}
}

// expansionRune разбирает один знак выражения или ${…}: команд там нет,
// но кавычки действуют и подстановки выполняются.
func (l *shellLexer) expansionRune() {
	switch l.src[l.pos] {
	case '\\':
		l.pos = min(l.pos+2, len(l.src))
	case '\'':
		l.single()
	case '"':
		l.double()
	case '$':
		l.dollar(true)
	case '`':
		l.backtick()
	default:
		l.pos++
	}
}

func (l *shellLexer) backtick() {
	start := l.pos
	l.pos++
	var body strings.Builder
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c == '\\' {
			if n := l.peek(1); n == '`' || n == '\\' || n == '$' {
				body.WriteRune(n)
				l.pos += 2
				continue
			}
		}
		l.pos++
		if c == '`' {
			break
		}
		body.WriteRune(c)
	}
	l.scan.script(body.String(), l.depth+1)
	l.raw(start)
}

// angle разбирает перенаправления и подстановку процесса <(…), >(…).
func (l *shellLexer) angle() {
	c := l.src[l.pos]
	if l.peek(1) == '(' {
		start := l.pos
		if l.tooDeep() {
			return
		}
		l.pos = l.substitution(l.pos + 2)
		l.raw(start)
		return
	}
	// Номер потока перед знаком («2>», «{fd}>») — часть перенаправления.
	if l.inWord && !l.quoted && !l.dynamic && isFD(l.cur.String()) {
		l.resetWord()
	} else {
		l.endWord()
	}
	l.pos++
	switch {
	case c == '<' && l.peek(0) == '<' && l.peek(1) == '<':
		l.pos += 2 // <<< — строка на вход; её подстановки разберутся как у слова
		l.redirect = true
	case c == '<' && l.peek(0) == '<':
		l.pos++
		l.heredoc = 1
		if l.peek(0) == '-' {
			l.pos++
			l.heredoc = 2
		}
	default:
		if n := l.peek(0); n == '>' || n == '|' || n == '&' || c == '<' && n == '>' {
			l.pos++
		}
		l.redirect = true
	}
}

func isFD(s string) bool {
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
		return isIdentifier(s[1 : len(s)-1])
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (l *shellLexer) openParen() {
	switch {
	case l.inWord && !l.quoted && !l.dynamic && strings.HasSuffix(l.cur.String(), "=") &&
		isAssignment(l.cur.String()):
		// a=( … ) — значения массива, а не команды. Конец ищет вложенный
		// разбор, он же разберёт подстановки внутри значений.
		start := l.pos
		if l.tooDeep() {
			return
		}
		sub := l.sub(l.pos + 1)
		sub.closer = true
		sub.run()
		l.pos = sub.pos
		l.cur.WriteString(string(l.src[start:l.pos]))
	case l.inCase(casePattern) && !l.inWord && len(l.words) == 0:
		l.pos++ // (образец) — необязательная скобка перед образцом
	default:
		l.endCommand()
		l.parens++
		l.pos++
	}
}

func (l *shellLexer) closeParen() {
	// Сперва дочитываем слово: «esac)» закрывает case, и только после этого
	// видно, что скобка — конец подстановки, а не конец образца.
	l.endWord()
	if l.inCase(casePattern) {
		// «образец)» в case: слова до скобки — образец, а не команда.
		l.words = nil
		l.cases[len(l.cases)-1] = caseBody
		return
	}
	l.endCommand()
	if l.parens > 0 {
		l.parens--
		return
	}
	if l.closer {
		l.done = true // парная скобка подстановки: тело кончилось
	}
}

func (l *shellLexer) resetWord() {
	l.cur.Reset()
	l.inWord, l.dynamic, l.quoted, l.bracket, l.brace = false, false, false, false, false
}

func (l *shellLexer) endWord() {
	if !l.inWord {
		return
	}
	text := l.cur.String()
	w := shellWord{text: text, dynamic: l.dynamic, quoted: l.quoted}
	if l.bracket {
		if i := strings.IndexByte(text, '['); i >= 0 && strings.Contains(text[i+1:], "]") {
			w.dynamic = true
		}
	}
	if l.brace && (strings.Contains(text, ",") || strings.Contains(text, "..")) {
		w.dynamic = true
	}
	l.resetWord()
	switch {
	case l.heredoc != 0:
		l.pending = append(l.pending, hereDoc{delim: text, quoted: w.quoted, strip: l.heredoc == 2})
		l.heredoc = 0
	case l.redirect:
		l.redirect = false
	case !w.quoted && (text == "{" || text == "}"):
		// Группа команд: { rm -rf x; } — скобка отделяет команды, как «;».
		l.endCommand()
	default:
		l.words = append(l.words, w)
		l.caseWord(w)
	}
}

// caseWord следит за конструкцией case по служебным словам в начале команды.
func (l *shellLexer) caseWord(w shellWord) {
	if w.quoted {
		return
	}
	lead := true // перед словом только служебные слова: «then case …»
	for _, p := range l.words[:len(l.words)-1] {
		if p.quoted || !leadingKeywords[p.text] {
			lead = false
			break
		}
	}
	switch {
	case lead && w.text == "case" && !l.inCase(casePattern):
		l.cases = append(l.cases, caseHead)
	case l.inCase(caseHead) && w.text == "in":
		l.cases[len(l.cases)-1] = casePattern
		l.words = nil // «case СЛОВО in» — заголовок, а не команда
	case lead && w.text == "esac" && (l.inCase(casePattern) || l.inCase(caseBody)):
		l.cases = l.cases[:len(l.cases)-1]
	}
}

func (l *shellLexer) endCommand() {
	l.endWord()
	l.redirect = false
	l.heredoc = 0
	if len(l.words) > 0 {
		l.cmds = append(l.cmds, l.words)
		l.words = nil
	}
}

// hereDocs пропускает тела встроенных документов: это данные, а не команды.
// Подстановки в теле без кавычек у ограничителя оболочка выполняет — их
// разбираем.
func (l *shellLexer) hereDocs() {
	for _, h := range l.pending {
		var body strings.Builder
		for l.pos < len(l.src) {
			end := l.pos
			for end < len(l.src) && l.src[end] != '\n' {
				end++
			}
			line := string(l.src[l.pos:end])
			l.pos = min(end+1, len(l.src))
			check := line
			if h.strip {
				check = strings.TrimLeft(line, "\t")
			}
			if check == h.delim {
				break
			}
			body.WriteString(line)
			body.WriteByte('\n')
		}
		if !h.quoted {
			l.scan.expansions(body.String(), l.depth+1)
		}
	}
	l.pending = nil
}

// ansiC раскрывает строку $'…' с позиции pos до закрывающей кавычки.
func ansiC(src []rune, pos *int) string {
	var b []byte
	i := *pos
	for i < len(src) {
		c := src[i]
		i++
		if c == '\'' {
			break
		}
		if c != '\\' || i >= len(src) {
			b = utf8.AppendRune(b, c)
			continue
		}
		e := src[i]
		i++
		switch e {
		case 'a':
			b = append(b, 7)
		case 'b':
			b = append(b, 8)
		case 'e', 'E':
			b = append(b, 27)
		case 'f':
			b = append(b, 12)
		case 'n':
			b = append(b, '\n')
		case 'r':
			b = append(b, '\r')
		case 't':
			b = append(b, '\t')
		case 'v':
			b = append(b, 11)
		case 'x', 'u', 'U':
			limit := 2
			if e == 'u' {
				limit = 4
			} else if e == 'U' {
				limit = 8
			}
			j := i
			for j < len(src) && j-i < limit && strings.ContainsRune("0123456789abcdefABCDEF", src[j]) {
				j++
			}
			if j == i {
				b = append(b, '\\', byte(e))
				continue
			}
			n, _ := strconv.ParseUint(string(src[i:j]), 16, 32)
			if e == 'x' {
				b = append(b, byte(n))
			} else {
				b = utf8.AppendRune(b, rune(n))
			}
			i = j
		case 'c':
			if i < len(src) {
				b = append(b, byte(src[i])&0x1f)
				i++
			}
		case '0', '1', '2', '3', '4', '5', '6', '7':
			j := i - 1
			for j < len(src) && j-(i-1) < 3 && src[j] >= '0' && src[j] <= '7' {
				j++
			}
			n, _ := strconv.ParseUint(string(src[i-1:j]), 8, 16)
			b = append(b, byte(n))
			i = j
		case '\\', '\'', '"', '?':
			b = utf8.AppendRune(b, e)
		default:
			// Незнакомое экранирование bash оставляет как есть, с чертой.
			b = append(b, '\\')
			b = utf8.AppendRune(b, e)
		}
	}
	*pos = i
	return string(b)
}
