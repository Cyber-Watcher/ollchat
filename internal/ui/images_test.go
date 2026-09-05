package ui

import (
	"bytes"
	"encoding/base64"
	"github.com/Cyber-Watcher/ollchat/internal/fsx"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/clipboard"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// pngBytes рисует настоящий PNG заданного размера: разбор заголовка должен
// работать на честном файле, а не на подделке из нескольких байт.
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("кодирование PNG: %v", err)
	}
	return buf.Bytes()
}

// paste подкладывает модели изображение так, как если бы его прочитали
// из буфера обмена.
func paste(t *testing.T, m *Model, w, h int) pendingImage {
	t.Helper()
	data := pngBytes(t, w, h)
	m.handleImagePasted(imagePastedMsg{img: &clipboard.Image{
		Data: data, MIME: "image/png", Width: w, Height: h,
	}})
	return m.pending[len(m.pending)-1]
}

// visionModel помечает текущую модель как умеющую смотреть картинки.
func visionModel(m *Model) { m.modelCaps = []string{"completion", "vision"} }

func TestPasteInsertsPlaceholder(t *testing.T) {
	m := newTestModel(t)
	visionModel(m)

	p := paste(t, m, 640, 480)
	if p.label() != "[Image01]" {
		t.Errorf("первая метка = %q, ожидалась [Image01]", p.label())
	}
	if got := m.ta.Value(); !strings.Contains(got, "[Image01]") {
		t.Errorf("метка не попала в промпт: %q", got)
	}
	if !strings.Contains(m.statusView(), "640×480") {
		t.Errorf("в строке состояния нет размеров картинки: %q", m.statusView())
	}

	// Вторая картинка получает следующий номер, текст между ними сохраняется.
	m.ta.InsertString("что здесь написано? ")
	second := paste(t, m, 100, 50)
	if second.label() != "[Image02]" {
		t.Errorf("вторая метка = %q, ожидалась [Image02]", second.label())
	}
	if got := m.ta.Value(); !strings.Contains(got, "[Image01]") ||
		!strings.Contains(got, "что здесь написано?") || !strings.Contains(got, "[Image02]") {
		t.Errorf("промпт собрался неверно: %q", got)
	}
}

func TestSendAttachesImagesInOrder(t *testing.T) {
	m := newTestModel(t)
	visionModel(m)

	first := paste(t, m, 8, 8)
	second := paste(t, m, 16, 16)

	// Ссылаемся на картинки в обратном порядке — важен порядок в тексте.
	m.send("сравни " + second.label() + " и " + first.label())

	msgs := m.conv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("в истории %d сообщений, ожидалось 1", len(msgs))
	}
	if msgs[0].Role != ollama.RoleUser {
		t.Fatalf("роль сообщения = %q", msgs[0].Role)
	}
	if len(msgs[0].Images) != 2 {
		t.Fatalf("приложено %d изображений, ожидалось 2", len(msgs[0].Images))
	}
	if msgs[0].Images[0] != base64.StdEncoding.EncodeToString(second.data) {
		t.Error("первым должно идти изображение, метка которого встретилась в тексте раньше")
	}
	if msgs[0].Images[1] != base64.StdEncoding.EncodeToString(first.data) {
		t.Error("вторым должно идти изображение со второй по порядку меткой")
	}
	if len(m.pending) != 0 {
		t.Errorf("после отправки список вложений должен очищаться, осталось %d", len(m.pending))
	}
}

// Стёртая метка отменяет вложение: в промпте картинки нет — значит и модели
// её посылать незачем.
func TestDeletedPlaceholderDropsImage(t *testing.T) {
	m := newTestModel(t)
	visionModel(m)

	paste(t, m, 8, 8)
	kept := paste(t, m, 16, 16)

	m.send("смотри только на " + kept.label())

	msgs := m.conv.Messages()
	if len(msgs[0].Images) != 1 {
		t.Fatalf("приложено %d изображений, ожидалось 1", len(msgs[0].Images))
	}
	if msgs[0].Images[0] != base64.StdEncoding.EncodeToString(kept.data) {
		t.Error("приложено не то изображение, метка которого осталась в тексте")
	}
}

// Метка без вложения — просто текст: пользователь мог написать её руками.
func TestUnknownPlaceholderIsPlainText(t *testing.T) {
	m := newTestModel(t)
	visionModel(m)

	m.send("что такое [Image07]?")

	msgs := m.conv.Messages()
	if len(msgs[0].Images) != 0 {
		t.Errorf("приложено %d изображений, ожидалось 0", len(msgs[0].Images))
	}
	if msgs[0].Content != "что такое [Image07]?" {
		t.Errorf("текст вопроса изменился: %q", msgs[0].Content)
	}
}

// Модель без vision — отказ с сохранением вопроса: терять набранный текст
// и вставленную картинку из-за не той модели пользователь не должен.
func TestSendRefusedWithoutVisionKeepsPrompt(t *testing.T) {
	m := newTestModel(t)
	m.modelCaps = []string{"completion", "tools"}

	p := paste(t, m, 8, 8)
	text := "распознай текст на " + p.label()
	m.ta.Reset()

	if cmd := m.send(text); cmd != nil {
		t.Error("отправка без поддержки vision не должна запускать обмен")
	}
	if m.conv.Len() != 0 {
		t.Errorf("в историю ничего не должно попасть, сообщений: %d", m.conv.Len())
	}
	if m.ta.Value() != text {
		t.Errorf("вопрос должен вернуться в поле ввода, там %q", m.ta.Value())
	}
	if len(m.pending) != 1 {
		t.Errorf("вложение должно сохраниться до смены модели, осталось %d", len(m.pending))
	}
	if last := m.blocks[len(m.blocks)-1]; last.kind != blockError ||
		!strings.Contains(last.text, "vision") {
		t.Errorf("должна быть внятная ошибка про vision, получено: %+v", last)
	}
}

// Ошибка чтения буфера обмена не должна ни ронять приложение, ни трогать промпт.
func TestPasteErrorIsReported(t *testing.T) {
	m := newTestModel(t)
	m.ta.SetValue("текст вопроса")

	m.handleImagePasted(imagePastedMsg{err: clipboard.ErrNoImage})

	if m.ta.Value() != "текст вопроса" {
		t.Errorf("промпт не должен меняться при ошибке, стало %q", m.ta.Value())
	}
	if len(m.pending) != 0 {
		t.Errorf("вложений быть не должно, есть %d", len(m.pending))
	}
	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockError || !strings.Contains(last.text, "буфере обмена") {
		t.Errorf("ожидалось сообщение об ошибке, получено: %+v", last)
	}
}

// Пустой ответ без ошибки не должен ронять приложение.
func TestPasteWithoutImageOrErrorIsSafe(t *testing.T) {
	m := newTestModel(t)
	m.handleImagePasted(imagePastedMsg{})

	if len(m.pending) != 0 {
		t.Errorf("вложений быть не должно, есть %d", len(m.pending))
	}
	if last := m.blocks[len(m.blocks)-1]; last.kind != blockError {
		t.Errorf("ожидалось сообщение об ошибке, получено: %+v", last)
	}
}

func TestClearDropsPendingImages(t *testing.T) {
	m := newTestModel(t)
	visionModel(m)
	paste(t, m, 8, 8)

	m.runCommand("/clear")
	if len(m.pending) != 0 {
		t.Errorf("после /clear вложения должны исчезать, осталось %d", len(m.pending))
	}
}

func TestHumanSize(t *testing.T) {
	cases := map[int]string{
		512:            "512 Б",
		2048:           "2 КБ",
		1024*1024 + 10: "1.0 МБ",
	}
	for n, want := range cases {
		if got := fsx.HumanSize(int64(n)); got != want {
			t.Errorf("fsx.HumanSize(int64(%d)) = %q, ожидалось %q", n, got, want)
		}
	}
}
