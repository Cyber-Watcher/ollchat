package maint

import "testing"

// Порог пересчёта разметки: судим по понятиям вне тем, а не по перекроенным темам.
func TestRepartitionDue(t *testing.T) {
	cases := []struct {
		uncovered, entities int
		want                bool
		why                 string
	}{
		{101012, 161239, true, "63% графа вне тем — обзор работает по трети (замер 02.09.2026)"},
		{7733, 161239, false, "4% — свежая разметка, пересчёт дороже пользы"},
		{16124, 161239, true, "ровно десятая часть — порог сработал"},
		{16000, 161239, false, "чуть меньше десятой — ещё рано"},
		{0, 161239, false, "все понятия размечены"},
		{5, 0, false, "пустой граф не повод для пересчёта"},
		{0, 0, false, "ничего нет"},
	}
	for _, c := range cases {
		if got := repartitionDue(c.uncovered, c.entities); got != c.want {
			t.Errorf("repartitionDue(%d, %d) = %v, ожидалось %v — %s",
				c.uncovered, c.entities, got, c.want, c.why)
		}
	}
}
