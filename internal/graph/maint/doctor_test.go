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
	// Настоящий граф: понятие 4 поглощено склейкой понятием 1 и в темах не
	// числится — вне тем оно считаться не должно. До 18.09.2026 тест собирал
	// список понятий руками и проверял функцию на её же входе (тавтология,
	// аудит Б17); теперь список берётся у Live() настоящего графа.
	g, err := graph.Create(t.TempDir(), "проба", 100, graph.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	for _, n := range []string{"один", "два", "три", "четыре"} {
		if _, _, err := g.Entities().Add(n, graph.TypeConcept); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := g.Merges().Add([]graph.MergeRec{{From: 4, To: 1}}); err != nil {
		t.Fatal(err)
	}
	live := g.Entities().Live()
	if len(live) != 3 {
		t.Fatalf("живых понятий %d, ожидалось 3 (четвёртое поглощено)", len(live))
	}
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

// Массовый сбой шага с моделью — ошибка команды, а не «код 0» (этап 102):
// 15.09.2026 сервер лёг посреди описаний тем, 236 сбоев из 298, а докатка
// записала успех.
func TestTooManyFailures(t *testing.T) {
	if err := tooManyFailures("описания тем", 236, 298); err == nil {
		t.Fatal("236 сбоев из 298 сошли за успех")
	}
	if err := tooManyFailures("описания тем", 60, 298); err == nil {
		t.Fatal("каждый пятый сбой сошёл за успех")
	}
	for _, c := range [][2]int{{0, 298}, {3, 298}, {0, 0}} {
		if err := tooManyFailures("описания тем", c[0], c[1]); err != nil {
			t.Fatalf("%d сбоев из %d — обычный шум, а не беда: %v", c[0], c[1], err)
		}
	}
}
