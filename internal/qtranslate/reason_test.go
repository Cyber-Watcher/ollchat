package qtranslate

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
)

// Аудит 16.10.2026, находка 9: текст ошибки клиента HTTP несёт адрес сервера
// модели, а note выдачи уходит модели и клиенту MCP; и один отказ соединения
// стоил 6,0 с на каждый поиск — после отказа модель не зовётся минуту.
func TestTranslateFailureReasonAndCooldown(t *testing.T) {
	cfg := config.Default()
	cfg.KB.Translate = true
	cfg.KB.TranslateModel = "model-x"
	cfg.Graph.Model = "extract-model"
	cfg.Graph.SummaryModel = "summary-model"
	tr := New(cfg, "http://127.0.0.1:1", nil) // закрытый порт на петле: отказ сразу
	if tr == nil {
		t.Fatal("переводчик не создан")
	}
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	tr.now = func() time.Time { return now }
	_, err := tr.Translate(context.Background(), "как устроен индекс")
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if r := Reason(err); strings.Contains(r, "127.0.0.1") || strings.Contains(r, "http") {
		t.Errorf("Reason несёт адрес сервера: %q", r)
	}
	start := time.Now()
	_, err = tr.Translate(context.Background(), "второй вопрос")
	if !errors.Is(err, ErrCooldown) {
		t.Errorf("второй вызов после отказа: %v, ждали ErrCooldown", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("второй вызов шёл %v: пауза не сработала", d)
	}
	now = now.Add(Cooldown + time.Second)
	if _, err = tr.Translate(context.Background(), "третий вопрос"); errors.Is(err, ErrCooldown) {
		t.Error("после паузы переводчик всё ещё молчит")
	}
	if r := Reason(context.DeadlineExceeded); !strings.Contains(r, "не ответила") {
		t.Errorf("Reason(DeadlineExceeded) = %q", r)
	}
}
