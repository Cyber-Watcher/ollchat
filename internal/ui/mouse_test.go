package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/itpro/ollchat/internal/config"
)

// Выделить текст мышью можно только тогда, когда приложение не просит у
// терминала событий мыши. Поэтому проверяем именно режим, который уходит
// в Bubble Tea вместе с экраном.

func TestMouseOnByDefault(t *testing.T) {
	m := newTestModel(t)
	if got := m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Errorf("по умолчанию мышь должна работать на приложение, режим = %v", got)
	}
}

func TestF2TogglesMouseMode(t *testing.T) {
	m := newTestModel(t)

	m.Update(pressKey(tea.KeyF2))
	if got := m.View().MouseMode; got != tea.MouseModeNone {
		t.Fatalf("после F2 отчёт о мыши должен выключаться, режим = %v", got)
	}
	if !strings.Contains(m.statusView(), "мышь") {
		t.Error("выключенная мышь должна быть видна в статус-баре")
	}

	m.Update(pressKey(tea.KeyF2))
	if got := m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Errorf("повторное F2 должно возвращать мышь приложению, режим = %v", got)
	}
}

func TestMouseCommandSetsMode(t *testing.T) {
	m := newTestModel(t)

	m.runCommand("/mouse off")
	if got := m.View().MouseMode; got != tea.MouseModeNone {
		t.Errorf("/mouse off не выключил отчёт о мыши, режим = %v", got)
	}

	m.runCommand("/mouse on")
	if got := m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Errorf("/mouse on не включил отчёт о мыши, режим = %v", got)
	}

	// Без аргумента команда только рассказывает о текущем состоянии.
	before := m.mouseOn
	m.runCommand("/mouse")
	if m.mouseOn != before {
		t.Error("/mouse без аргумента не должна ничего менять")
	}
}

// Начальное состояние берётся из конфига.
func TestMouseStartsOffWhenConfigured(t *testing.T) {
	m := newTestModelWith(t, func(cfg *config.Config) {
		cfg.Input.Mouse = false
	})
	if got := m.View().MouseMode; got != tea.MouseModeNone {
		t.Errorf("при mouse=false в конфиге отчёт о мыши не должен запрашиваться, режим = %v", got)
	}
}

// Когда мышь у терминала, выделение забирает ячейки экрана как есть. Ни
// колонка бегунка, ни добивка пробелами не должны попадать в буфер обмена.
func TestTranscriptCopiesWithoutScrollbar(t *testing.T) {
	m := newTestModel(t)
	fillTranscript(m, 300)

	withBar := m.transcriptView()
	if !strings.Contains(withBar, scrollThumbChar) {
		t.Fatal("подготовка: при мыши у приложения бегунок должен быть виден")
	}

	m.Update(pressKey(tea.KeyF2))
	clean := m.transcriptView()

	if strings.Contains(clean, scrollThumbChar) || strings.Contains(clean, scrollTrackChar) {
		t.Error("при мыши у терминала бегунок не должен попадать в ленту")
	}
	for i, line := range strings.Split(clean, "\n") {
		if strings.HasSuffix(line, " ") {
			t.Errorf("строка %d кончается пробелами — они попадут в буфер обмена: %q", i, line)
		}
	}
	// Текст при этом никуда не делся.
	if !strings.Contains(clean, "строка ответа номер") {
		t.Error("текст ленты пропал вместе с бегунком")
	}
}

// Захват бегунка не должен пережить выключение мыши: отпускания кнопки
// приложение уже не увидит и лента залипла бы за указателем.
func TestMouseOffReleasesScrollbarGrab(t *testing.T) {
	m := newTestModel(t)
	fillTranscript(m, 300)

	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft,
		X: m.scrollbarColumn(), Y: transcriptTop})
	if !m.draggingBar {
		t.Fatal("подготовка: бегунок должен быть захвачен")
	}

	m.Update(pressKey(tea.KeyF2))
	if m.draggingBar {
		t.Error("после выключения мыши захват бегунка должен сниматься")
	}
}

// Переключение режима мыши сообщается только строкой состояния: в ленте
// диалога служебным строкам не место, а переключают мышь часто.
func TestMouseToggleDoesNotWriteToTranscript(t *testing.T) {
	m := newTestModel(t)
	before := len(m.blocks)

	m.Update(pressKey(tea.KeyF2))
	m.Update(pressKey(tea.KeyF2))
	m.runCommand("/mouse off")
	m.runCommand("/mouse on")

	if len(m.blocks) != before {
		var texts []string
		for _, b := range m.blocks[before:] {
			texts = append(texts, b.text)
		}
		t.Errorf("в ленте появились лишние строки: %v", texts)
	}
	if !strings.Contains(m.statusView(), "мышь") {
		t.Error("состояние мыши должно оставаться в строке состояния")
	}
}

// Явный вопрос «а как сейчас?» отвечает в ленте — его задают редко и осознанно.
func TestMouseQueryAnswersInTranscript(t *testing.T) {
	m := newTestModel(t)
	before := len(m.blocks)

	m.runCommand("/mouse")

	if len(m.blocks) != before+1 {
		t.Fatalf("на /mouse без аргумента ожидался ответ в ленте, блоков стало %d", len(m.blocks))
	}
	if !strings.Contains(m.blocks[len(m.blocks)-1].text, "мышь") {
		t.Errorf("ответ должен рассказывать о мыши: %q", m.blocks[len(m.blocks)-1].text)
	}
}

// Бегунок и дорожка одного цвета: бегунок отличается формой, а не акцентом.
func TestScrollbarThumbMatchesTrackColour(t *testing.T) {
	thumb := styScrollThumb.Render(scrollThumbChar)
	track := styScrollTrack.Render(scrollTrackChar)

	strip := func(s, glyph string) string { return strings.ReplaceAll(s, glyph, "") }
	if strip(thumb, scrollThumbChar) != strip(track, scrollTrackChar) {
		t.Errorf("оформление бегунка и дорожки различается:\n  бегунок %q\n  дорожка %q", thumb, track)
	}
}
