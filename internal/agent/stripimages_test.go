package agent

import (
	"errors"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// TestStripImages — модель сменили посреди диалога, картинки в истории остались.
//
// Ollama отвергает такой запрос целиком: «Multimodal data provided, but model
// does not support multimodal requests». Значит без вычистки разговор обрывается
// насовсем — падает каждый следующий вопрос. Найдено на живом сеансе.
func TestStripImages(t *testing.T) {
	msgs := []ollama.Message{
		{Role: ollama.RoleUser, Content: "посмотри на это"},
		{Role: ollama.RoleUser, Content: "Вот запрошенная картинка.", Images: []string{"AAA", "BBB"}},
		{Role: ollama.RoleAssistant, Content: "вижу таблицу"},
	}
	out, dropped := stripImages(msgs)
	if dropped != 2 {
		t.Fatalf("убрано картинок %d, ожидалось 2", dropped)
	}
	for i, m := range out {
		if len(m.Images) != 0 {
			t.Fatalf("в сообщении %d остались картинки", i)
		}
	}
	// Текст «Вот запрошенная картинка» без картинки заставляет модель описывать
	// то, чего ей не показали, — поэтому нужна пометка.
	if !strings.Contains(out[1].Content, "не приложена") {
		t.Fatalf("нет пометки об отсутствующей картинке: %q", out[1].Content)
	}
	if !strings.Contains(out[1].Content, "Вот запрошенная картинка") {
		t.Fatalf("потерян исходный текст: %q", out[1].Content)
	}

	// Историю править нельзя: вернувшись к модели с vision, пользователь должен
	// снова увидеть картинки на местах.
	if len(msgs[1].Images) != 2 {
		t.Fatal("исходная история изменена")
	}
}

// TestStripImagesNoop — без картинок список не копируется и не меняется.
func TestStripImagesNoop(t *testing.T) {
	msgs := []ollama.Message{{Role: ollama.RoleUser, Content: "как скачать файлы по scp?"}}
	out, dropped := stripImages(msgs)
	if dropped != 0 {
		t.Fatalf("убрано %d картинок там, где их нет", dropped)
	}
	if len(out) != 1 || out[0].Content != msgs[0].Content {
		t.Fatalf("сообщение изменено: %+v", out)
	}
}

// TestIsMultimodalRefusal — второй рубеж: возможности модели известны не сразу,
// и после переключения картинки могут уйти вслепую. Отказ сервера — самый
// надёжный признак, и по нему обмен повторяется без картинок.
func TestIsMultimodalRefusal(t *testing.T) {
	yes := []string{
		`сервер вернул 400 Bad Request: {"error":"{\"error\":{\"code\":400,\"message\":\"Multimodal data provided, but model does not support multimodal requests.\",\"type\":\"invalid_request_error\"}}"}`,
		"image input is not supported by this model",
	}
	for _, s := range yes {
		if !isMultimodalRefusal(errors.New(s)) {
			t.Fatalf("отказ не распознан: %s", s)
		}
	}
	no := []string{
		"connection refused",
		`сервер вернул 500: {"error":"CUDA error: out of memory"}`,
		"model requires more system memory",
	}
	for _, s := range no {
		if isMultimodalRefusal(errors.New(s)) {
			t.Fatalf("обычная ошибка принята за отказ от картинок: %s", s)
		}
	}
	if isMultimodalRefusal(nil) {
		t.Fatal("nil принят за отказ")
	}
}
