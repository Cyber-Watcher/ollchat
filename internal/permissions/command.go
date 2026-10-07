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
	"sort":  {short: "o", valued: "kSTt", long: []string{"output"}},
	"cp":    {all: true},
	"mv":    {all: true},
	"tee":   {all: true},
	"tar":   {all: true},
	"chmod": {all: true},
}

// writingKeys — чем читающая программа пишет.
type writingKeys struct {
	all   bool     // пишет всегда, какие ключи ни дай
	words []string // ключи целым словом, как у find: -delete, -exec
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
	for i := 0; i < len(args); i++ {
		a := args[i]
		for _, w := range k.words {
			if a == w || strings.HasPrefix(a, w+"=") {
				return true
			}
		}
		if k.short == "" {
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
