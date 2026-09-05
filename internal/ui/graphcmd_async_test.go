package ui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// /graph status не должна занимать поток интерфейса.
//
// Открытие графа стоит восемь секунд на живой коллекции: реестр понятий вырос
// до 90 МБ, а кэш не спасает — во время сборки отметка файлов меняется, и граф
// переоткрывается на каждый вызов. Раньше это делалось прямо в Update:
// интерфейс замирал, нажатия копились, и человек жал Enter несколько раз,
// решив, что команда не сработала.
//
// Здесь проверяется не скорость (на пустой коллекции она любая), а устройство:
// Update возвращается сразу, работа уходит фоновой командой, и человеку сразу
// показано, что нажатие принято.
func TestGraphStatusDoesNotBlockUpdate(t *testing.T) {
	m, books := kbTestModel(t)
	writeTestBook(t, books, "kniga.pdf", "graph status subject")
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	cmd := m.runCommand("/kb add proba " + books)
	if cmd == nil {
		t.Fatalf("коллекция не создалась: %q", lastBlock(m).text)
	}
	drainJob(t, m, cmd)

	for _, r := range "/graph status proba" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}

	before := len(m.blocks)
	start := time.Now()
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if el := time.Since(start); el > 300*time.Millisecond {
		t.Fatalf("Update держал поток %s — работа должна уходить в фон", el)
	}
	if cmd == nil {
		t.Fatal("работа не ушла в фон: команда не вернула tea.Cmd")
	}
	if len(m.blocks) <= before {
		t.Error("человеку не показано, что нажатие принято")
	}

	// Фоновая работа возвращает готовый текст сообщением, а не трогает модель.
	msg := cmd()
	if _, ok := msg.(noticeMsg); !ok {
		t.Fatalf("из фона пришло %T, ожидалось noticeMsg", msg)
	}
}
