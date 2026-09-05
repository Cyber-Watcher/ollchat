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
var writingFlags = map[string][]string{
	"find":  {"-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprintf", "-fls"},
	"sort":  {"-o", "--output"},
	"cp":    {"*"},
	"mv":    {"*"},
	"tee":   {"*"},
	"tar":   {"*"},
	"chmod": {"*"},
}

// WritesSomething сообщает, несёт ли команда ключ, которым читающая программа
// пишет или запускает чужое. Проверяется только имя программы и её ключи:
// путей и содержимого мы не знаем и знать не должны.
func WritesSomething(cmd string) bool {
	flags, ok := writingFlags[CommandName(cmd)]
	if !ok {
		return false
	}
	if len(flags) == 1 && flags[0] == "*" {
		return true
	}
	fields := strings.Fields(cmd)
	for _, f := range fields[min(1, len(fields)):] {
		for _, bad := range flags {
			// `-o` и `-o=имя`, но не `-original`.
			if f == bad || strings.HasPrefix(f, bad+"=") {
				return true
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
