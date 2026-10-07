package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// runConcurrently исполняет команду в своей горутине, как это делает Bubble
// Tea, а цикл событий тем временем рисует экран и принимает сообщения.
//
// settle исполняет команды синхронно, между двумя Update, и гонку команды
// с отрисовкой не видит по построению. Здесь они идут одновременно — так,
// как в живой программе, — и -race ловит каждое касание модели из команды.
func runConcurrently(t *testing.T, m *Model, cmd tea.Cmd) tea.Msg {
	t.Helper()
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	for i := 0; ; i++ {
		select {
		case msg := <-done:
			return msg
		default:
		}
		_ = m.View()
		if i%16 == 0 {
			m.Update(noticeMsg{text: "сообщение посреди команды"})
		}
	}
}

// /kb doctor считает отчёт в горутине команды и открывал граф через graphOf —
// метод цикла событий: он закрывал и подменял m.gr и писал строку в ленту
// прямо из горутины (аудит 07.10.2026, находка 17). Запускать с -race.
func TestKBDoctorLeavesModelToEventLoop(t *testing.T) {
	m, books := kbTestModel(t)
	writeTestBook(t, books, "go.pdf", "goroutines and channels explained")
	drainJob(t, m, m.runCommand("/kb add go "+books))
	m.runCommand("/kb use go")
	coll, err := m.kbCollection("go")
	if err != nil {
		t.Fatal(err)
	}
	buildTestGraph(t, coll)

	cmd := m.runCommand("/kb doctor go")
	if cmd == nil {
		t.Fatalf("проверка не ушла в фон: %q", lastBlock(m).text)
	}
	msg := runConcurrently(t, m, cmd)

	if m.gr.open != nil {
		t.Error("команда подменила граф модели из своей горутины")
	}
	raw, ok := msg.(rawMsg)
	if !ok {
		t.Fatalf("из фона пришло %T, ожидался готовый отчёт", msg)
	}
	m.Update(raw)
	if got := lastBlock(m); got.kind != blockRaw || !strings.Contains(got.text, "Всё в порядке") {
		t.Errorf("отчёт доктора не попал в ленту: %q", got.text)
	}
}
