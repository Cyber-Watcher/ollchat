package ollama

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Тело успешного ответа /api/tags, /api/show и прочих быстрых вызовов
// ограничено: прежде список моделей с именем в 16 МБ принимался целиком,
// и чужой сервер мог занять память клиента сколько угодно. Ответ длиннее
// предела — ошибка с пределом в тексте, ответ короче проходит как прежде.
// Предел здесь уменьшен полем клиента: настоящий (maxResponseBody) — десятки
// мегабайт, гонять их в каждом прогоне тестов незачем.
func TestQuickCallBodyIsLimited(t *testing.T) {
	name := strings.Repeat("A", 4<<10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"name":"` + name + `"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, 5*time.Second, 5*time.Second, nil)
	if c.maxBody != maxResponseBody || maxResponseBody < 64<<20 {
		t.Fatalf("предел тела %d, умолчание %d: /api/show и /api/tags бывают большими", c.maxBody, maxResponseBody)
	}
	if _, err := c.Tags(context.Background()); err != nil {
		t.Fatalf("ответ короче предела не принят: %v", err)
	}

	c.maxBody = 1 << 10
	models, err := c.Tags(context.Background())
	if err == nil {
		t.Fatalf("ответ длиннее предела принят: %d моделей, имя %d байт", len(models), len(models[0].Name))
	}
	if !strings.Contains(err.Error(), "/api/tags") || !strings.Contains(err.Error(), "предел") {
		t.Errorf("невнятная ошибка: %v", err)
	}

	// Ответ ровно в предел — принят: граница не съедает последний байт.
	body := `{"models":[{"name":"` + name + `"}]}`
	c.maxBody = int64(len(body))
	if _, err := c.Tags(context.Background()); err != nil {
		t.Errorf("ответ ровно в предел не принят: %v", err)
	}
}
