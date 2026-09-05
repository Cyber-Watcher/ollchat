package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
	"github.com/Cyber-Watcher/ollchat/internal/config"
)

// withLogger подменяет журнал модели на пишущий во временный каталог:
// newTestModel создаёт выключенный, а здесь нужно проверить сам файл.
func withLogger(t *testing.T, m *Model) (*chatlog.Logger, string) {
	t.Helper()
	dir := t.TempDir()
	l := chatlog.New(dir, "chat.md", true)
	t.Cleanup(func() { l.Close() })
	m.logger = l
	return l, filepath.Join(dir, "chat.md")
}

// headsOf возвращает шапки записей журнала — то, что стоит до пробела перед датой.
func headsOf(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("чтение журнала: %v", err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "[") {
			continue
		}
		if i := strings.Index(line, "]"); i > 0 {
			out = append(out, line[1:i])
		}
	}
	return out
}

// Главное свойство задачи: вопрос, вызов инструмента и ответ одного обмена
// помечены в журнале одним значением, а служебная запись между обменами — 00.
func TestTurnIDGroupsEntriesOfOneTurn(t *testing.T) {
	m := newTestModel(t)
	l, path := withLogger(t, m)
	clearTranscript(m)

	id := m.logger.BeginTurn()
	m.turnID = id
	ts := time.Date(2026, 8, 20, 14, 30, 0, 0, time.Local)
	if err := m.logger.WriteAt(ts, chatlog.KindQuestion, "что в файле?"); err != nil {
		t.Fatal(err)
	}
	if err := m.logger.WriteTool("read_file", "main.go", "package main", true); err != nil {
		t.Fatal(err)
	}
	if err := m.logger.WriteFromAt(ts, chatlog.KindAnswer, "test-model", "пакет main"); err != nil {
		t.Fatal(err)
	}
	m.logger.EndTurn()
	if err := m.logger.Write(chatlog.KindSystem, "К контексту приложен файл main.go"); err != nil {
		t.Fatal(err)
	}

	heads := headsOf(t, path)
	want := []string{id, id, id, l.SessionID() + "-00"}
	if len(heads) != len(want) {
		t.Fatalf("записей %d, ожидалось %d: %v", len(heads), len(want), heads)
	}
	for i := range want {
		if heads[i] != want[i] {
			t.Errorf("запись %d помечена %q, ожидалось %q", i+1, heads[i], want[i])
		}
	}
}

// stampTurn проставляет id блокам ответа и помечает последний кусок — только
// под ним идентификатор показывается в ленте.
func TestStampTurnMarksLastAnswerBlock(t *testing.T) {
	m := newTestModel(t)
	clearTranscript(m)
	m.turnID = "k7f3-01"
	m.answeredBy = "test-model"

	m.addBlock(block{kind: blockUser, text: "вопрос", turn: m.turnID})
	m.addBlock(block{kind: blockAssistant, text: "первая часть ответа"})
	m.addBlock(block{kind: blockTool, title: "read_file(main.go)", turn: m.turnID})
	m.addBlock(block{kind: blockAssistant, text: "вторая часть ответа"})

	m.stampTurn(time.Date(2026, 8, 20, 14, 30, 0, 0, time.Local))

	var marked []int
	for i, b := range m.blocks {
		if b.kind != blockAssistant {
			continue
		}
		if b.turn != "k7f3-01" {
			t.Errorf("блок %d не помечен обменом: %q", i, b.turn)
		}
		if b.showTurnID {
			marked = append(marked, i)
		}
	}
	if len(marked) != 1 || marked[0] != 3 {
		t.Fatalf("метку должен нести только последний кусок ответа, получено %v", marked)
	}
	if !strings.Contains(m.rendered[3], "k7f3-01") {
		t.Errorf("идентификатор не попал в отрисованный блок:\n%q", m.rendered[3])
	}
	if strings.Contains(m.rendered[1], "k7f3-01") {
		t.Errorf("первый кусок ответа не должен показывать идентификатор:\n%q", m.rendered[1])
	}
}

// Копия по Shift+F5 обязана совпадать с журналом побайтно, а значит нести
// тот же идентификатор.
func TestCopyAnswerCarriesTurnID(t *testing.T) {
	m := newTestModel(t)
	clearTranscript(m)
	at := time.Date(2026, 8, 20, 14, 30, 0, 0, time.Local)

	m.addBlock(block{kind: blockUser, text: "вопрос", at: at, turn: "k7f3-07"})
	m.addBlock(block{kind: blockAssistant, text: "ответ", at: at, model: "test-model", turn: "k7f3-07"})

	p, ok := m.copyVisibleAnswer(true)
	if !ok {
		t.Fatal("ответ на экране есть, копирование обязано его найти")
	}
	if strings.Count(p.text, "[k7f3-07]") != 2 {
		t.Errorf("в копии должно быть две шапки с идентификатором:\n%s", p.text)
	}
}

// Строка состояния показывает идентификатор только при включённой настройке.
func TestStatusTurnIDIsOptional(t *testing.T) {
	on := newTestModelWith(t, func(cfg *config.Config) { cfg.General.ShowTurnID = true })
	id := on.logger.BeginTurn()
	if !strings.Contains(on.statusView(), id) {
		t.Errorf("при show_turn_id = true строка состояния должна показывать %q:\n%s",
			id, on.statusView())
	}

	off := newTestModelWith(t, func(cfg *config.Config) { cfg.General.ShowTurnID = false })
	idOff := off.logger.BeginTurn()
	if strings.Contains(off.statusView(), idOff) {
		t.Errorf("при show_turn_id = false идентификатора в строке состояния быть не должно:\n%s",
			off.statusView())
	}
}

// /id показывает сеанс, номер последнего обмена и путь к журналу.
func TestIDCommandReport(t *testing.T) {
	m := newTestModel(t)
	l, path := withLogger(t, m)
	clearTranscript(m)
	id := m.logger.BeginTurn()

	m.runCommand("/id")
	notices := blocksOfKind(m, blockNotice)
	if len(notices) != 1 {
		t.Fatalf("ожидалось одно пояснение, получено %d", len(notices))
	}
	for _, want := range []string{l.SessionID(), id, path} {
		if !strings.Contains(notices[0].text, want) {
			t.Errorf("в выводе /id нет %q:\n%s", want, notices[0].text)
		}
	}
}
