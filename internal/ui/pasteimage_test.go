package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/clipboard"
	"github.com/Cyber-Watcher/ollchat/internal/config"
)

// TestPasteUsesConfiguredTimeout: Ctrl+V ждёт картинку весь срок
// input.paste_image_timeout, а не прежние 5 с, и строка состояния называет срок.
func TestPasteUsesConfiguredTimeout(t *testing.T) {
	m := newTestModel(t)
	clearTranscript(m)
	var left time.Duration
	prev := clipboardRead
	clipboardRead = func(ctx context.Context, _ int) (*clipboard.Image, error) {
		if dl, ok := ctx.Deadline(); ok {
			left = time.Until(dl)
		}
		return nil, clipboard.ErrNoImage
	}
	t.Cleanup(func() { clipboardRead = prev })

	want := m.cfg.Input.PasteImageTimeoutDuration()
	_, cmd := m.Update(pressCtrl('v'))
	if cmd == nil {
		t.Fatal("Ctrl+V должен запускать чтение буфера обмена")
	}
	if !strings.Contains(m.statusMsg, fmt.Sprintf("%d с", int(want.Seconds()))) {
		t.Errorf("строка состояния не называет срок %v: %q", want, m.statusMsg)
	}
	cmd()
	if left < want-10*time.Second || left > want {
		t.Errorf("до конца срока чтения %v, ожидалось около %v (input.paste_image_timeout)", left, want)
	}
	if want != config.DefaultPasteImageTimeout {
		t.Errorf("у тестовой модели срок %v, ожидалось умолчание %v", want, config.DefaultPasteImageTimeout)
	}
}

// TestPasteTimeoutExplainsSlowChannel: срок вышел посреди картинки — ошибка
// называет медленный канал и настройку, а не «signal: killed».
func TestPasteTimeoutExplainsSlowChannel(t *testing.T) {
	m := newTestModel(t)
	clearTranscript(m)
	catchPaste(t, nil, fmt.Errorf("%w: xclip читал image/png дольше срока", clipboard.ErrImageTimeout))

	_, cmd := m.Update(pressCtrl('v'))
	m.Update(cmd())

	errs := blocksOfKind(m, blockError)
	if len(errs) != 1 {
		t.Fatalf("ожидалась одна ошибка, получено %d", len(errs))
	}
	for _, want := range []string{"input.paste_image_timeout", "ssh -X", "120 с"} {
		if !strings.Contains(errs[0].text, want) {
			t.Errorf("в ошибке нет %q:\n%s", want, errs[0].text)
		}
	}
	if strings.Contains(errs[0].text, "signal: killed") {
		t.Errorf("в ошибке осталось «signal: killed»:\n%s", errs[0].text)
	}
}
