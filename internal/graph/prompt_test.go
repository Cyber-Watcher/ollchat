package graph

import (
	"strings"
	"testing"
)

func TestPromptFilesLoaded(t *testing.T) {
	for name, p := range map[string]string{"extract": SystemPrompt, "summary": SummaryPrompt, "findings": FindingsPrompt} {
		if strings.TrimSpace(p) == "" {
			t.Fatalf("промпт %s пуст: файл не вшит", name)
		}
	}
	if !strings.HasPrefix(SystemPrompt, "Ты извлекаешь понятия и связи") {
		t.Fatalf("промпт извлечения начинается не так: %.40q", SystemPrompt)
	}
}

func TestPromptIDChangesWithText(t *testing.T) {
	if len(PromptID) != 8 {
		t.Fatalf("PromptID %q: ожидалось 8 знаков", PromptID)
	}
	a := promptID("a", "b")
	if a == promptID("a", "b ") || a == promptID("ab", "") {
		t.Fatal("PromptID не различает тексты")
	}
	if a != promptID("a", "b") {
		t.Fatal("PromptID недетерминирован")
	}
}

// Паспорт нового графа несёт версию промпта; сборка другим промптом
// отказывается идти без явного разрешения, с разрешением — записывает смену.
func TestPromptStamp(t *testing.T) {
	dir := t.TempDir()
	g, err := Create(dir, "c", 10, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if g.Meta().PromptID != PromptID || len(g.Meta().PromptHistory) != 1 {
		t.Fatalf("паспорт нового графа: %+v", g.Meta())
	}
	if err := g.stampPrompt("deadbeef", false); err == nil {
		t.Fatal("сборка другим промптом должна отклоняться")
	}
	if err := g.stampPrompt("deadbeef", true); err != nil {
		t.Fatalf("с разрешением смена должна записываться: %v", err)
	}
	m := g.Meta()
	if m.PromptID != "deadbeef" || len(m.PromptHistory) != 2 {
		t.Fatalf("после смены: %+v", m)
	}

	// Граф без версии (собран до 04.09.2026): первая сборка записывает её задним числом.
	g.meta.PromptID, g.meta.PromptHistory = "", nil
	if err := g.stampPrompt(PromptID, false); err != nil {
		t.Fatalf("запись задним числом: %v", err)
	}
	if g.Meta().PromptID != PromptID || !strings.Contains(g.Meta().PromptHistory[0].Note, "задним числом") {
		t.Fatalf("после записи задним числом: %+v", g.Meta())
	}
}
