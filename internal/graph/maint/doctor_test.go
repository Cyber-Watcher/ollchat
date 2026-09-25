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

// Знаменатель — ЖИВЫЕ понятия, а не записи реестра (24.09.2026).
//
// Числа настоящие: на графе 24.09 живых понятий 306 947, записей реестра
// 339 092 (склейкой поглощено 32 145), вне тем 12 600. Деление на реестр
// давало 3 %, на живые — 4 %; порог 10 % ни в том, ни в другом случае
// не достигнут, но занижение отодвигало бы его на 10 % графа.
func TestRepartitionDenominatorIsLive(t *testing.T) {
	const registry, merged, uncovered = 339092, 32145, 12600
	live := registry - merged
	if 100*uncovered/registry != 3 || 100*uncovered/live != 4 {
		t.Fatalf("доли не те, на которых писан тест: реестр %d%%, живые %d%%",
			100*uncovered/registry, 100*uncovered/live)
	}
	// Сам порог на этих числах не срабатывает ни так, ни так — проверяем,
	// что разница знаменателей действительно меняет вердикт там, где она важна.
	if repartitionDue(uncovered, live) || repartitionDue(uncovered, registry) {
		t.Error("на числах 24.09 пересчёт не нужен ни по живым, ни по реестру")
	}
	// Граница: вне тем десятая часть ЖИВЫХ, с округлением вверх — доля
	// считается целочисленно, и ровно 10,0 % при делении нацело даёт 9.
	tenth := (live + 9) / 10
	if !repartitionDue(tenth, live) {
		t.Errorf("десятая часть живых (%d из %d) должна звать пересчёт", tenth, live)
	}
	if repartitionDue(tenth, registry) {
		t.Errorf("та же десятая часть, делённая на реестр (%d), порога не достигает — "+
			"это и есть занижение, из-за которого знаменатель исправлен", registry)
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
