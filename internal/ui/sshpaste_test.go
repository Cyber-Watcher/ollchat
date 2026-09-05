package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/clipboard"
)

// Поведение вставки картинки, когда графической сессии нет: ollchat запущен
// по SSH, а буфер обмена остался на машине пользователя.

// catchPaste подменяет чтение буфера обмена: тест не должен зависеть ни от
// наличия графической сессии на машине, ни от того, что лежит в буфере.
func catchPaste(t *testing.T, img *clipboard.Image, err error) {
	t.Helper()
	prev := clipboardRead
	clipboardRead = func(context.Context, int) (*clipboard.Image, error) { return img, err }
	t.Cleanup(func() { clipboardRead = prev })
}

func blocksOfKind(m *Model, k blockKind) []block {
	var out []block
	for _, b := range m.blocks {
		if b.kind == k {
			out = append(out, b)
		}
	}
	return out
}

// TestPasteWithoutSessionExplainsSSH закрепляет главное: когда графической
// сессии нет, пользователь получает не тупиковую ошибку, а рецепт.
//
// Случай ожидаемый — ollchat нередко запускают по SSH, — поэтому это подсказка
// (blockHint), а не сообщение об ошибке.
func TestPasteWithoutSessionExplainsSSH(t *testing.T) {
	m := newTestModel(t)
	clearTranscript(m)
	catchPaste(t, nil, fmt.Errorf("%w", clipboard.ErrNoSession))

	_, cmd := m.Update(pressCtrl('v'))
	if cmd == nil {
		t.Fatal("Ctrl+V должен запускать чтение буфера обмена")
	}
	m.Update(cmd())

	hints := blocksOfKind(m, blockHint)
	if len(hints) != 1 {
		t.Fatalf("ожидалась одна подсказка, получено %d", len(hints))
	}
	// Проверяем именно те слова, без которых рецепт бесполезен.
	for _, want := range []string{"ssh -X", "xclip", "DISPLAY", "tmux"} {
		if !strings.Contains(hints[0].text, want) {
			t.Errorf("в подсказке нет упоминания %q:\n%s", want, hints[0].text)
		}
	}
	if got := len(blocksOfKind(m, blockError)); got != 0 {
		t.Errorf("отсутствие графической сессии — не ошибка пользователя, блоков ошибки: %d", got)
	}
}

// TestPasteHintShownOnce: подсказка не засоряет ленту — способ доступа
// к буферу обмена за сеанс не изменится.
func TestPasteHintShownOnce(t *testing.T) {
	m := newTestModel(t)
	clearTranscript(m)
	catchPaste(t, nil, clipboard.ErrNoSession)

	for i := 0; i < 3; i++ {
		if _, cmd := m.Update(pressCtrl('v')); cmd != nil {
			m.Update(cmd())
		}
	}

	if got := len(blocksOfKind(m, blockHint)); got != 1 {
		t.Errorf("подсказка должна появиться один раз, показано: %d", got)
	}
	if !strings.Contains(m.statusMsg, "недоступен") {
		t.Errorf("о повторных попытках надо сказать в строке состояния, там: %q", m.statusMsg)
	}
}

// TestPasteHelperMissingIsStillAnError: сессия есть, а утилиты нет — совет
// другой, поэтому сообщение остаётся ошибкой и про SSH не рассказывает.
func TestPasteHelperMissingIsStillAnError(t *testing.T) {
	m := newTestModel(t)
	clearTranscript(m)
	catchPaste(t, nil, fmt.Errorf("%w: сессия X11, но xclip нет — установите xclip", clipboard.ErrNoHelper))

	_, cmd := m.Update(pressCtrl('v'))
	m.Update(cmd())

	if got := len(blocksOfKind(m, blockHint)); got != 0 {
		t.Errorf("отсутствие утилиты не должно выдавать подсказку про SSH, подсказок: %d", got)
	}
	errs := blocksOfKind(m, blockError)
	if len(errs) != 1 || !strings.Contains(errs[0].text, "xclip") {
		t.Errorf("ожидалась ошибка с упоминанием xclip, получено: %+v", errs)
	}
}

// TestPasteEmptyClipboardStaysError: пустой буфер — не повод рассказывать
// про SSH.
func TestPasteEmptyClipboardStaysError(t *testing.T) {
	m := newTestModel(t)
	clearTranscript(m)
	catchPaste(t, nil, clipboard.ErrNoImage)

	_, cmd := m.Update(pressCtrl('v'))
	m.Update(cmd())

	if got := len(blocksOfKind(m, blockHint)); got != 0 {
		t.Errorf("подсказок про SSH быть не должно, показано: %d", got)
	}
	if got := len(blocksOfKind(m, blockError)); got != 1 {
		t.Errorf("ожидался один блок ошибки, получено %d", got)
	}
}

// TestPasteSuccessStillWorks: путь успеха не сломан правкой — картинка
// прикладывается, метка появляется в промпте, лента остаётся чистой.
func TestPasteSuccessStillWorks(t *testing.T) {
	m := newTestModel(t)
	clearTranscript(m)
	visionModel(m)
	catchPaste(t, &clipboard.Image{
		Data: pngBytes(t, 100, 50), MIME: "image/png", Width: 100, Height: 50,
	}, nil)

	_, cmd := m.Update(pressCtrl('v'))
	m.Update(cmd())

	if len(m.pending) != 1 {
		t.Fatalf("вложение не приложено, в pending: %d", len(m.pending))
	}
	if !strings.Contains(m.ta.Value(), "[Image01]") {
		t.Errorf("в промпте нет метки вложения: %q", m.ta.Value())
	}
	if got := len(blocksOfKind(m, blockError)) + len(blocksOfKind(m, blockHint)); got != 0 {
		t.Errorf("успешная вставка не должна ничего добавлять в ленту, добавлено блоков: %d", got)
	}
}
