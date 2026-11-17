package ui

import (
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/clipboard"
)

// Этап 129: шапка различает включённое, выключенное движком и непроверенное,
// а картинка при выключенном в движке зрении не уходит.

// TestCapsMarksThreeKinds: выключенное движком — тускло, непроверенное — с «?»,
// включённое — как прежде; completion в шапке не показывается.
func TestCapsMarksThreeKinds(t *testing.T) {
	m := newTestModel(t)
	m.modelCaps = []string{"completion", "tools", "thinking"}
	m.modelCapsOff = []string{"vision"}
	m.modelCapsUnknown = []string{"thinking"}
	got := m.capsMarks()
	for _, want := range []string{"tools", "thinking?", styHeaderDim.Render("vision")} {
		if !strings.Contains(got, want) {
			t.Errorf("в отметках нет %q: %q", want, got)
		}
	}
	if strings.Contains(got, "completion") {
		t.Errorf("completion в шапке не показывается: %q", got)
	}
	if !strings.Contains(m.headerView(), "vision") {
		t.Error("выключенное движком зрение должно остаться в шапке (тускло), а не пропасть")
	}

	m.modelCaps, m.modelCapsOff, m.modelCapsUnknown = []string{"completion"}, nil, nil
	if got := m.capsMarks(); got != "" {
		t.Errorf("без отличительных возможностей отметок нет, получено %q", got)
	}
}

// TestVisionOffInEngineRefusesImage: зрение у модели есть, движок выключил —
// при вставке сразу объяснение про движок, при отправке отказ и вопрос
// возвращается в поле (прежде картинка уходила и приходило 400 «At most 0 image(s)»).
func TestVisionOffInEngineRefusesImage(t *testing.T) {
	m := newTestModel(t)
	clearTranscript(m)
	m.modelCaps = []string{"completion", "tools", "thinking"}
	m.modelCapsOff = []string{"vision"}
	catchPaste(t, &clipboard.Image{Data: []byte("png"), MIME: "image/png", Width: 1, Height: 1}, nil)

	_, cmd := m.Update(pressCtrl('v'))
	m.Update(cmd())
	notes := blocksOfKind(m, blockNotice)
	if len(notes) == 0 || !strings.Contains(notes[len(notes)-1].text, "--language-model-only") {
		t.Fatalf("при вставке ожидалось объяснение про движок, пометки: %v", notes)
	}

	q := "[Image01] что на снимке?"
	if cmd := m.send(q); cmd != nil {
		t.Error("отправка с картинкой при выключенном зрении должна быть отклонена")
	}
	errs := blocksOfKind(m, blockError)
	if len(errs) != 1 || !strings.Contains(errs[0].text, "движок сервера") {
		t.Fatalf("ожидалась одна ошибка про движок сервера, получено: %v", errs)
	}
	if got := m.ta.Value(); got != q {
		t.Errorf("вопрос должен вернуться в поле как был, в поле %q", got)
	}
	if len(m.conv.Messages()) != 0 {
		t.Errorf("в историю ничего не должно уйти, сообщений: %d", len(m.conv.Messages()))
	}
}

// TestVisionAbsentKeepsOldWords: у модели зрения нет вовсе (Ollama без vision) —
// прежний совет выбрать другую модель, без слов про движок.
func TestVisionAbsentKeepsOldWords(t *testing.T) {
	m := newTestModel(t)
	m.modelCaps = []string{"completion", "tools"}
	if got := m.visionRefusal(); !strings.Contains(got, "не умеет смотреть изображения") || strings.Contains(got, "движок") {
		t.Errorf("для модели без зрения — прежние слова, получено %q", got)
	}
}
