package agent_test

import (
	"context"
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/agent"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/session"
)

// TestLiveModelSwitchAfterImage воспроизводит живой сбой: в диалоге есть
// картинка, пользователь переключается на модель без vision и задаёт обычный
// вопрос. Сервер отвечал 400 «Multimodal data provided…» и разговор обрывался
// насовсем — падал каждый следующий вопрос.
func TestLiveModelSwitchAfterImage(t *testing.T) {
	url := os.Getenv("OLLCHAT_TEST_SERVER")
	model := os.Getenv("OLLCHAT_TEST_NOVISION")
	if url == "" || model == "" {
		t.Skip("нужны OLLCHAT_TEST_SERVER и OLLCHAT_TEST_NOVISION")
	}
	// Однопиксельный PNG: содержимое не важно, важен сам факт вложения.
	png, _ := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")

	conv := session.New("")
	conv.Append(ollama.Message{Role: ollama.RoleUser, Content: "посмотри на картинку",
		Images: []string{base64.StdEncoding.EncodeToString(png)}})
	conv.Append(ollama.Message{Role: ollama.RoleAssistant, Content: "вижу однотонный квадрат"})
	conv.Append(ollama.Message{Role: ollama.RoleUser, Content: "Как скачать файлы с удалённой машины по scp?"})

	r := &agent.Runner{
		Client:          ollama.New(url, 5*time.Minute, 0, nil),
		Model:           model,
		VisionSupported: false, // модель без vision — это и есть условие сбоя
		MaxIterations:   3,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var answer strings.Builder
	var notice string
	for ev := range r.Run(ctx, conv) {
		switch ev.Kind {
		case agent.EventContent:
			answer.WriteString(ev.Text)
		case agent.EventNotice:
			notice = ev.Text
			t.Logf("предупреждение: %s", ev.Text)
		case agent.EventError:
			t.Fatalf("обмен не удался: %v", ev.Err)
		}
	}
	if notice == "" {
		t.Error("пользователю не сказали, что картинки не отправлены")
	}
	got := answer.String()
	t.Logf("ответ модели: %s", strings.TrimSpace(firstLines(got, 4)))
	if !strings.Contains(strings.ToLower(got), "scp") {
		t.Fatalf("модель ответила не по вопросу: %q", got)
	}
	// История обязана остаться нетронутой.
	for _, m := range conv.Messages() {
		if m.Role == ollama.RoleUser && strings.Contains(m.Content, "посмотри на картинку") {
			if len(m.Images) != 1 {
				t.Fatal("картинка вычищена из истории, а не только из запроса")
			}
		}
	}
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
