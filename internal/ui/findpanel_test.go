package ui

import (
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/find"
)

// В заголовке панели написаны все три клавиши: человек узнаёт о Ctrl+O оттуда,
// а не из документации.
func TestFindPanelTitleNamesKeys(t *testing.T) {
	p := &findPanel{}
	view := p.view(100, []findRow{{head: "книга", tail: "цитата"}}, "горутина")
	for _, want := range []string{"Enter прочитать", "Ctrl+O открыть книгу", "Ctrl+F закрыть"} {
		if !strings.Contains(view, want) {
			t.Errorf("в заголовке панели нет %q", want)
		}
	}
}

// Ctrl+O берётся панелью и не уходит ни в поле ввода, ни в отправку вопроса.
func TestCtrlOTakenByFindPanel(t *testing.T) {
	m := newTestModel(t)
	m.finds = []find.Excerpt{{ID: "books/1#1", Book: "книга", Path: "/нет/такой/книги.pdf",
		Unit: "стр.", From: 10}}
	m.toggleFindPanel()

	taken, id := m.handleFindPanelKey("ctrl+o")
	if !taken {
		t.Fatal("Ctrl+O должен браться панелью")
	}
	if id != "" {
		t.Errorf("Ctrl+O не читает кусок, а открывает книгу: вернулся id %q", id)
	}
	// Файла нет — человек обязан увидеть причину, а не тишину.
	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockError || !strings.Contains(last.text, "нет") {
		t.Errorf("ожидалась ошибка об отсутствующем файле, вышло %q", last.text)
	}
}

// Прочитанное показывается сразу: лента перематывается к нему, даже если
// панель до этого сдвинула низ содержимого.
func TestReadScrollsToTheEnd(t *testing.T) {
	m := newTestModel(t)
	for i := 0; i < 40; i++ {
		m.addBlock(block{kind: blockNotice, text: strings.Repeat("строка\n", 5)})
	}
	m.vp.GotoTop()
	m.addBlockAndShow(block{kind: blockNotice, text: "прочитанный кусок"})
	if !m.vp.AtBottom() {
		t.Error("после чтения лента должна стоять в конце, иначе кусок не виден")
	}
}
