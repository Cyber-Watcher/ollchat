package maint

// Помощники, которые до этапа 91 (R4) делили один пакет main с командами графа.

// trimTitle укорачивает заголовок для строки хода.
func trimTitle(s string) string {
	r := []rune(s)
	if len(r) > 44 {
		return string(r[:44]) + "…"
	}
	return s
}
