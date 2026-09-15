package maint

import (
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

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

// Понятия вне тем считаются по ЖИВЫМ, а не по всему реестру: поглощённое
// склейкой понятие не самостоятельный узел, и в темах его быть не должно.
// Дефект 15.09.2026: обход по All() после склейки 15 311 пар показал
// 33 059 понятий вне тем вместо 12 530 и звал пересчитывать разметку.
func TestCountUncoveredSkipsMerged(t *testing.T) {
	// 1 и 2 в теме, 3 — беспризорное. Поглощённых здесь нет вовсе: Live()
	// их уже не отдаёт, и функция обязана считать ровно то, что получила.
	live := []graph.Entity{{ID: 1}, {ID: 2}, {ID: 3}}
	inTopic := map[uint32]bool{1: true, 2: true}

	if got := countUncovered(live, inTopic); got != 1 {
		t.Errorf("вне тем = %d, ожидалась 1 (только беспризорное понятие)", got)
	}
	// Пустая карта тем — вне тем все живые, но не больше их числа.
	if got := countUncovered(live, map[uint32]bool{}); got != 3 {
		t.Errorf("при пустой разметке вне тем = %d, ожидалось 3", got)
	}
	if got := countUncovered(nil, inTopic); got != 0 {
		t.Errorf("без понятий вне тем = %d, ожидался 0", got)
	}
}
