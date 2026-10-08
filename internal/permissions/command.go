package permissions

import "strings"

// SplitCommand разбивает командную строку на самостоятельные команды по
// операторам &&, ||, ;, | и переводам строк, не заглядывая внутрь кавычек.
//
// Разбор нужен, чтобы правила deny нельзя было обойти составной командой
// вида «go build && rm -rf /».
func SplitCommand(cmd string) []string {
	var (
		parts    []string
		cur      strings.Builder
		inSingle bool
		inDouble bool
	)
	flush := func() {
		s := strings.TrimSpace(cur.String())
		if s != "" {
			parts = append(parts, s)
		}
		cur.Reset()
	}

	runes := []rune(cmd)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case c == '\\' && !inSingle && i+1 < len(runes):
			cur.WriteRune(c)
			i++
			cur.WriteRune(runes[i])
			continue
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == '"' && !inSingle:
			inDouble = !inDouble
		}
		if inSingle || inDouble {
			cur.WriteRune(c)
			continue
		}
		switch c {
		case ';', '\n', '|', '&':
			// Двойные операторы && и || поглощаем целиком.
			if (c == '|' || c == '&') && i+1 < len(runes) && runes[i+1] == c {
				i++
			}
			flush()
		default:
			cur.WriteRune(c)
		}
	}
	flush()
	return parts
}

// IsCompound сообщает, что команда содержит операторы объединения, перенаправление
// или подстановку. Такие команды никогда не разрешаются правилами allow
// автоматически — по ним всегда спрашивается подтверждение.
func IsCompound(cmd string) bool {
	var inSingle, inDouble bool
	runes := []rune(cmd)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case c == '\\' && !inSingle && i+1 < len(runes):
			i++
			continue
		case c == '\'' && !inDouble:
			inSingle = !inSingle
			continue
		case c == '"' && !inSingle:
			inDouble = !inDouble
			continue
		}
		if inSingle {
			continue
		}
		// В двойных кавычках подстановки продолжают работать.
		if inDouble {
			if c == '$' || c == '`' {
				return true
			}
			continue
		}
		switch c {
		case ';', '|', '&', '\n', '>', '<', '`', '$', '(', ')', '{', '}':
			return true
		}
	}
	return false
}

// OnlySafePipeline сообщает, склеена ли команда только конвейером и цепочкой:
// `|`, `||`, `&&`, `;` и перевод строки.
//
// **Зачем отдельно от IsCompound.** Составная команда из разрешённых частей
// («grep … | head -50; ls …») ничем не опаснее этих частей по отдельности,
// и спрашивать про неё незачем. Но это верно ровно до тех пор, пока части
// склеены безобидным способом. Опасны не они, а всё остальное:
//
//   - `>` и `<` — перенаправление: `cat файл > /etc/passwd` состоит из одной
//     «разрешённой» части `cat`, а переписывает системный файл;
//   - `$(…)` и обратные кавычки — подстановка: `ls $(rm -rf ~)` тоже выглядит
//     как безобидный `ls`;
//   - одиночный `&` — запуск в фоне: работа уходит из-под присмотра;
//   - `(`, `)`, `{`, `}` — подоболочки и группы.
//
// Ни одного из этих знаков SplitCommand не разделяет, поэтому проверка частей
// их бы не заметила. Здесь они прямо запрещают тихое разрешение.
func OnlySafePipeline(cmd string) bool {
	var inSingle, inDouble bool
	runes := []rune(cmd)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case c == '\\' && !inSingle && i+1 < len(runes):
			i++
			continue
		case c == '\'' && !inDouble:
			inSingle = !inSingle
			continue
		case c == '"' && !inSingle:
			inDouble = !inDouble
			continue
		}
		if inSingle {
			continue
		}
		if inDouble {
			// В двойных кавычках подстановки продолжают работать.
			if c == '$' || c == '`' {
				return false
			}
			continue
		}
		switch c {
		case '>', '<', '$', '`', '(', ')', '{', '}':
			return false
		case '&':
			// `&&` — цепочка, одиночный `&` — фон.
			if i+1 < len(runes) && runes[i+1] == '&' {
				i++
				continue
			}
			return false
		}
	}
	return true
}

// writingFlags — ключи, превращающие читающую программу в пишущую.
//
// Разрешать `find` и `sort` целиком нельзя: `find … -delete` удаляет,
// `find … -exec rm {} ;` запускает что угодно, `sort -o файл` переписывает
// файл. Но и спрашивать про `find заметки -type f | wc -l` незачем — это счёт
// файлов. Поэтому разрешение даётся программе, а ключи из этой таблицы
// возвращают вопрос.
var writingFlags = map[string]writingKeys{
	"find": {words: []string{"-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprintf", "-fls"}},
	// Короткие ключи sort со значением: -k, -o, -S, -t, -T.
	// --compress-program запускает названную программу на каждый временный
	// файл: при Bash(sort:*) это был запуск чего угодно без вопроса.
	"sort":  {short: "o", valued: "kSTt", long: []string{"output", "compress-program"}},
	"cp":    {all: true},
	"mv":    {all: true},
	"tee":   {all: true},
	"tar":   {all: true},
	"chmod": {all: true},
	// go и git стоят в разрешённых по умолчанию, и у обоих есть ключи,
	// которыми сборка или чтение истории запускают программу, пишут файл
	// по выбранному пути или читают файл вне проекта.
	"go":  {scan: goRunsOrWrites},
	"git": {scan: gitRunsOrWrites},
}

// writingKeys — чем читающая программа пишет.
type writingKeys struct {
	all   bool                     // пишет всегда, какие ключи ни дай
	scan  func(args []string) bool // свой разбор ключей, когда таблицы мало
	words []string                 // ключи целым словом, как у find: -delete, -exec
	// short — короткие пишущие ключи. Разбираются как у getopt: связка
	// `-rofile` — это `-r -o file`, и `sort -ofile` пишет в file так же,
	// как `sort -o file`. Сравнение слова целиком такое пропускало.
	short string
	// valued — прочие короткие ключи со значением: после такой буквы
	// в связке идёт значение, а не ключи (`sort -to` — разделитель «o»),
	// а без слитного значения им становится следующее слово.
	valued string
	// long — длинные пишущие ключи. getopt принимает и сокращения:
	// `--out=файл` — это `--output=файл`.
	long []string
}

// WritesSomething сообщает, несёт ли команда ключ, которым читающая программа
// пишет или запускает чужое. Проверяется только имя программы и её ключи:
// путей и содержимого мы не знаем и знать не должны.
func WritesSomething(cmd string) bool {
	words, err := SplitWords(cmd)
	if err != nil {
		words = strings.Fields(cmd)
	}
	for len(words) > 0 && isAssignment(words[0]) {
		words = words[1:]
	}
	if len(words) == 0 {
		return false
	}
	keys, ok := writingFlags[words[0]]
	if !ok {
		return false
	}
	return keys.writes(words[1:])
}

func (k writingKeys) writes(args []string) bool {
	if k.all {
		return true
	}
	if k.scan != nil {
		return k.scan(args)
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		for _, w := range k.words {
			if a == w || strings.HasPrefix(a, w+"=") {
				return true
			}
		}
		if k.short == "" && len(k.long) == 0 {
			continue
		}
		switch {
		case a == "--":
			return false // дальше только имена файлов
		case strings.HasPrefix(a, "--"):
			name, _, _ := strings.Cut(a[2:], "=")
			for _, l := range k.long {
				if name != "" && strings.HasPrefix(l, name) {
					return true
				}
			}
		case len(a) > 1 && a[0] == '-':
			for j := 1; j < len(a); j++ {
				if strings.IndexByte(k.short, a[j]) >= 0 {
					return true
				}
				if strings.IndexByte(k.valued, a[j]) >= 0 {
					if j == len(a)-1 {
						i++ // значение — следующее слово, это не ключ
					}
					break
				}
			}
		}
	}
	return false
}

// goWriteKeys — ключи go, которыми сборка или тест пишут файл по названному
// пути. Имена — из `go help build`, `go help test`, `go help testflag`
// и cmd/go/internal/work/build.go (Go 1.26.5). Правила Bash(go build:*)
// и Bash(go test:*) стоят в разрешённых по умолчанию, и `go test
// -coverprofile=~/.bashrc` проходил без вопроса (проверка правок по аудиту,
// 08.10.2026). `go build` без -o пишет бинарь в рабочий каталог — это
// разрешено правилом; -o выбирает любой путь. Ключи, которыми go запускает
// программу (-toolexec, -exec, -vettool), — goRunKeys в shell.go: их
// программа сверяется ещё и с запретами.
var goWriteKeys = map[string]bool{
	"o": true, "pkgdir": true, "outputdir": true,
	"coverprofile": true, "cpuprofile": true, "memprofile": true,
	"blockprofile": true, "mutexprofile": true, "trace": true,
	"debug-actiongraph": true, "debug-runtime-trace": true, "debug-trace": true,
}

// goRunsOrWrites ищет такие ключи в любом месте строки: и до пакетов, и после
// -args, где их получает уже тестовый бинарь (-test.cpuprofile пишет файл и
// там). Ключ go пишется с одним или двумя дефисами, значение — через «=» или
// следующим словом; у ключей теста бывает приставка test.
func goRunsOrWrites(args []string) bool {
	for i, a := range args {
		// go env -w записывает настройку в файл окружения go насовсем:
		// GOFLAGS=-toolexec=… сработает в каждой следующей сборке.
		if i > 0 && args[0] == "env" && (a == "-w" || a == "-u") {
			return true
		}
		// Внешний компоновщик из -ldflags (-extld, -extldflags) — любая
		// программа; значение -ldflags приходит одним словом.
		if strings.Contains(a, "-extld") {
			return true
		}
		if len(a) < 2 || a[0] != '-' {
			continue
		}
		name := strings.TrimPrefix(a[1:], "-")
		name, _, _ = strings.Cut(name, "=")
		name = strings.TrimPrefix(name, "test.")
		if goWriteKeys[name] || goRunKeys[name] != "" {
			return true
		}
	}
	return false
}

// gitOutputKeys — длинные ключи git после подкоманды: --output пишет вывод
// log, diff и show в любой файл, а с --format — любой текст (в ~/.bashrc
// тоже); --no-index даёт diff читать файлы вне репозитория, мимо запрета
// Read(~/.ssh/**). На git 2.53.0 сокращений `--outp=` git не принимает, но
// сверяем и их: вопрос дешевле, чем разница версий.
var gitOutputKeys = writingKeys{long: []string{"output", "no-index"}}

// gitRunsOrWrites сверяет общие ключи git до подкоманды и ключи после неё.
// До подкоманды -c и --config-env задают настройку, а через настройку
// (core.fsmonitor, core.pager, diff.external) git запускает любую программу;
// --exec-path подменяет каталог его команд. Это важно при Bash(git:*): у
// Bash(git log:*) подкоманда стоит первым словом. -c ПОСЛЕ подкоманды —
// другой ключ (вид diff у log), его не трогаем.
func gitRunsOrWrites(args []string) bool {
	i := 0
	for ; i < len(args) && strings.HasPrefix(args[i], "-"); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-c") || strings.HasPrefix(a, "--config-env") || strings.HasPrefix(a, "--exec-path") {
			return true
		}
		if a == "-C" {
			i++ // каталог — следующее слово
		}
	}
	if i >= len(args) {
		return false
	}
	return gitOutputKeys.writes(args[i+1:])
}

// CommandName возвращает имя запускаемой программы — первое слово команды
// без присваиваний переменных окружения вида VAR=value.
func CommandName(cmd string) string {
	for _, f := range strings.Fields(cmd) {
		if strings.Contains(f, "=") && !strings.HasPrefix(f, "-") {
			// Пропускаем префиксные присваивания: FOO=bar команда.
			if idx := strings.Index(f, "="); idx > 0 && isIdentifier(f[:idx]) {
				continue
			}
		}
		return f
	}
	return ""
}

func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
