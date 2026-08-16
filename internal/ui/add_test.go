package ui

import (
	"github.com/itpro/ollchat/internal/ctxmeter"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Вопрос пользователя: примет ли /add абсолютный путь. Ответ — да, если путь
// ведёт внутрь песочницы; наружу не пустит, пока в конфиге не разрешено явно.
// Закрепляем оба исхода, потому что это граница безопасности, а не удобство.

func TestAddAcceptsAbsolutePathInsideSandbox(t *testing.T) {
	m := newTestModel(t)
	root := m.guard.Sandbox().Root()

	abs := filepath.Join(root, "заметка.txt")
	if err := os.WriteFile(abs, []byte("содержимое файла"), 0o600); err != nil {
		t.Fatalf("подготовка: %v", err)
	}

	m.runCommand("/add " + abs)

	msgs := m.conv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("файл не приложен к контексту, сообщений: %d", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "содержимое файла") {
		t.Errorf("в контекст попало не содержимое файла: %q", msgs[0].Content)
	}
	// В сообщении для модели путь показывается относительным — так короче
	// и не утекает расположение рабочего каталога.
	if !strings.Contains(msgs[0].Content, "заметка.txt") {
		t.Errorf("в сообщении нет имени файла: %q", msgs[0].Content)
	}
}

func TestAddRejectsAbsolutePathOutsideSandbox(t *testing.T) {
	m := newTestModel(t)

	outside := filepath.Join(t.TempDir(), "чужой.txt")
	if err := os.WriteFile(outside, []byte("секрет"), 0o600); err != nil {
		t.Fatalf("подготовка: %v", err)
	}

	m.runCommand("/add " + outside)

	if m.conv.Len() != 0 {
		t.Fatal("файл вне песочницы не должен попадать в контекст")
	}
	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockError {
		t.Fatalf("ожидалась ошибка, получено: %+v", last)
	}
	if !strings.Contains(last.text, "вне рабочего каталога") {
		t.Errorf("ошибка должна объяснять причину отказа: %q", last.text)
	}
}

// «ollama show» показывает максимум модели, строка статуса — действующее окно.
// Путать их дорого, поэтому /context печатает обе величины.
func TestContextReportShowsModelMaximum(t *testing.T) {
	m := newTestModel(t)
	m.modelMaxCtx = 262144
	m.meter.SetCapacity(32768, ctxmeter.SourceConfig)

	report := m.contextReport()
	if !strings.Contains(report, "262144") {
		t.Errorf("в отчёте нет максимума модели:\n%s", report)
	}
	if !strings.Contains(report, "32768") {
		t.Errorf("в отчёте нет действующей ёмкости:\n%s", report)
	}
	if !strings.Contains(report, "12%") {
		t.Errorf("в отчёте нет доли используемого окна (32768 из 262144 — 12%%):\n%s", report)
	}
	if !strings.Contains(report, "num_ctx") {
		t.Errorf("в отчёте не сказано, чем окно поднимается:\n%s", report)
	}
}

// Когда окно и максимум совпадают, лишней строки быть не должно.
func TestContextReportSilentWhenFullWindowUsed(t *testing.T) {
	m := newTestModel(t)
	m.modelMaxCtx = 262144
	m.meter.SetCapacity(262144, ctxmeter.SourceConfig)

	if report := m.contextReport(); strings.Contains(report, "возможного окна") {
		t.Errorf("при полном окне подсказка не нужна:\n%s", report)
	}
}

// Пока сервер не ответил, максимум неизвестен — строку не выдумываем.
func TestContextReportOmitsUnknownMaximum(t *testing.T) {
	m := newTestModel(t)
	if report := m.contextReport(); strings.Contains(report, "максимум модели") {
		t.Errorf("неизвестный максимум показывать нельзя:\n%s", report)
	}
}
