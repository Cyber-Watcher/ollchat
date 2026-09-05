package ui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// Три состояния окрашены по-разному: цвет и есть сигнал, читать текст каждый
// раз никто не станет.
func TestGraphOpenNoteColorsByThreshold(t *testing.T) {
	cfg := &config.Graph{} // умолчания: 10 и 20 секунд

	fast := GraphOpenNote(graph.OpenStats{Elapsed: 3 * time.Second, Heap: 500 << 20}, cfg)
	slow := GraphOpenNote(graph.OpenStats{Elapsed: 12 * time.Second, Heap: 1 << 30}, cfg)
	hot := GraphOpenNote(graph.OpenStats{Elapsed: 45 * time.Second, Heap: 4 << 30}, cfg)

	for name, s := range map[string]string{"быстро": fast, "средне": slow, "долго": hot} {
		if s == "" {
			t.Fatalf("%s: строка не должна быть пустой", name)
		}
	}
	if fast == slow || slow == hot || fast == hot {
		t.Error("три состояния обязаны отличаться оформлением")
	}
}

// Нечего показывать — молчим: граф не открывали, а создали пустым.
func TestGraphOpenNoteSilentWithoutMeasurement(t *testing.T) {
	if s := GraphOpenNote(graph.OpenStats{}, &config.Graph{}); s != "" {
		t.Errorf("без замера строки быть не должно: %q", s)
	}
}

// В строке видны обе величины — время и память, ради них всё и затевалось.
func TestGraphOpenNoteShowsTimeAndMemory(t *testing.T) {
	s := GraphOpenNote(graph.OpenStats{Elapsed: 11700 * time.Millisecond, Heap: 1127428915}, &config.Graph{})
	if !strings.Contains(s, "11.7 с") {
		t.Errorf("нет времени открытия:\n%s", s)
	}
	if !strings.Contains(s, "1.05 ГБ") {
		t.Errorf("нет занятой памяти:\n%s", s)
	}
}

// Пороги и цвета берутся из настроек, а не зашиты.
func TestGraphOpenNoteHonoursConfig(t *testing.T) {
	strict := &config.Graph{OpenSlowSeconds: 2, OpenHotSeconds: 4}
	loose := &config.Graph{OpenSlowSeconds: 60, OpenHotSeconds: 120}

	three := 3 * time.Second
	if GraphOpenNote(graph.OpenStats{Elapsed: three, Heap: 1 << 20}, strict) ==
		GraphOpenNote(graph.OpenStats{Elapsed: three, Heap: 1 << 20}, loose) {
		t.Error("при разных порогах одно и то же время должно оформляться по-разному")
	}
}

// Память словами человека, а не в байтах.
func TestHumanBytes(t *testing.T) {
	cases := map[uint64]string{
		0:                  "меньше мегабайта",
		700 << 10:          "700 КБ",
		512 << 20:          "512 МБ",
		uint64(1127428915): "1.05 ГБ",
		uint64(5) << 30:    "5.00 ГБ",
	}
	for n, want := range cases {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, ожидалось %q", n, got, want)
		}
	}
}

// Значок памяти появляется только при открытом графе и только если не выключен
// настройкой: закрытый граф памяти не занимает, и «GR: 0» сообщал бы о том,
// чего нет.
func TestGraphMemoryStatus(t *testing.T) {
	m := newTestModel(t)
	if s := m.graphMemoryStatus(); s != "" {
		t.Errorf("без открытого графа значка быть не должно: %q", s)
	}

	// Настройка выключена — молчим, даже когда граф открыт.
	m.cfg.Graph.ShowMemory = false
	if s := m.graphMemoryStatus(); s != "" {
		t.Errorf("при show_memory = false значка быть не должно: %q", s)
	}
}

// Единица выбирается сама: сотни мегабайт остаются мегабайтами, дальше гигабайты.
func TestGraphMemoryUnitsAuto(t *testing.T) {
	cases := map[uint64]string{
		160 << 20:     "160 МБ",
		900 << 20:     "900 МБ",
		1<<30 + 1<<28: "1.25 ГБ",
	}
	for n, want := range cases {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, ожидалось %q", n, got, want)
		}
	}
}

// Полоса хода рисуется только там, где есть что показывать: без знаменателя
// вместо пустой рамки должна остаться честная строка о шаге.
func TestGraphBarTextWithoutTotal(t *testing.T) {
	s := graphBarText(graph.OpenProgress{Stage: "связи"}, 3*time.Second, 100)
	if strings.Contains(s, "[") || strings.Contains(s, "░") {
		t.Fatalf("полоса без знаменателя: %q", s)
	}
	if !strings.Contains(s, "связи") || !strings.Contains(s, "3 с") {
		t.Fatalf("нет шага или времени: %q", s)
	}
}

func TestGraphBarTextShowsShare(t *testing.T) {
	s := graphBarText(graph.OpenProgress{Stage: "реестр понятий", Done: 50, Total: 200},
		12*time.Second, 100)
	if !strings.Contains(s, "25%") {
		t.Fatalf("нет доли: %q", s)
	}
	if !strings.Contains(s, "█") || !strings.Contains(s, "░") {
		t.Fatalf("нет полосы: %q", s)
	}
	if !strings.Contains(s, "12 с") {
		t.Fatalf("нет времени: %q", s)
	}
}

// В узком окне полоса уступает место подписи, а не лезет за край.
func TestGraphBarTextNarrowWindow(t *testing.T) {
	s := graphBarText(graph.OpenProgress{Stage: "реестр понятий", Done: 1, Total: 2},
		1*time.Second, 40)
	if strings.Contains(s, "[") {
		t.Fatalf("полоса не должна помещаться: %q", s)
	}
	if lipgloss.Width(s) > 60 {
		t.Fatalf("строка длиннее окна: %q", s)
	}
}

// Доля больше единицы возможна: счётчик байт считает и перевод строки,
// а размер файла мог быть снят до дозаписи. Полоса при этом не должна ломаться.
func TestGraphBarTextClampsOverflow(t *testing.T) {
	s := graphBarText(graph.OpenProgress{Stage: "реестр понятий", Done: 300, Total: 200},
		1*time.Second, 120)
	if !strings.Contains(s, "100%") {
		t.Fatalf("доля не ограничена: %q", s)
	}
}
