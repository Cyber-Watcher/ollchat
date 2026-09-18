package kbserve

import (
	"net/http/httptest"
	"testing"
)

// Проверка ключа службы: заголовок Bearer, запасной заголовок, пустой ключ —
// «проверки нет», чужой и пустой ключ при заданном — отказ (аудит 17.09.2026, Б17:
// у Auth не было ни одного теста).
func TestAuth(t *testing.T) {
	cases := []struct {
		name   string
		header string
		value  string
		token  string
		want   bool
	}{
		{"без ключа на службе — пускать всех", "Authorization", "Bearer что угодно", "", true},
		{"Bearer с верным ключом", "Authorization", "Bearer секрет", "секрет", true},
		{"Bearer с пробелами вокруг", "Authorization", "Bearer   секрет  ", "секрет", true},
		{"запасной заголовок", "X-Ollmcp-Token", "секрет", "секрет", true},
		{"чужой ключ", "Authorization", "Bearer другой", "секрет", false},
		{"ключа нет в запросе", "", "", "секрет", false},
		{"ключ — префикс верного", "Authorization", "Bearer секре", "секрет", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/api/v1/search", nil)
		if c.header != "" {
			r.Header.Set(c.header, c.value)
		}
		if got := Auth(r, c.token); got != c.want {
			t.Errorf("%s: получено %v, ожидалось %v", c.name, got, c.want)
		}
	}
}
