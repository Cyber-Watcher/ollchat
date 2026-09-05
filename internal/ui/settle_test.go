package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// settle выполняет команду и скармливает её сообщения модели, пока команды
// не кончатся: с этапа 91 (R6) команды интерфейса считают в фоне и кладут
// результат в ленту через Update, поэтому «вызвал — проверил» без этого шага
// видит пустую ленту.
func settle(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	for depth := 0; cmd != nil && depth < 50; depth++ {
		msg := cmd()
		if msg == nil {
			return
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				settle(t, m, c)
			}
			return
		}
		_, cmd = m.Update(msg)
	}
}
