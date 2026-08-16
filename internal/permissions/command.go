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
