package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// Этап 112, Х3: автосохранение переписывает один файл сессии после каждого
// сообщения. Шаг пункта — «N сообщений по одному, оборвать, восстановить» — для
// файла, подменяемого переименованием, означает: обрыв посреди записи оставляет
// рядом недописанный временный файл, а сама сессия на диске целая, с N − 1
// сообщениями, и список её находит.
func TestSaveAsOneFileSurvivesTornWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	st := NewStore(dir)
	c := New("")
	id := ""
	const n = 5
	for i := 0; i < n-1; i++ {
		c.Append(ollama.Message{Role: ollama.RoleUser, Content: "вопрос"})
		var err error
		if id, _, err = st.SaveAs(id, c, "local", "m"); err != nil {
			t.Fatal(err)
		}
	}
	// N-е сообщение «пишется», и процесс падает до переименования: на диске
	// остаётся недописанный временный файл с именем, каким его заводит fsx.
	torn := filepath.Join(dir, "."+id+".json.12345.tmp")
	if err := os.WriteFile(torn, []byte(`{"id":"`+id+`","messages":[{"role":"us`), 0o600); err != nil {
		t.Fatal(err)
	}

	list, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != id {
		t.Fatalf("сессий %d, ждали одну %s: %+v", len(list), id, list)
	}
	if got := len(list[0].Messages); got != n-1 {
		t.Errorf("в сессии сообщений %d, ждали %d", got, n-1)
	}
	rec, err := st.Load(id)
	if err != nil || len(rec.Messages) != n-1 {
		t.Errorf("загрузка после обрыва: %v, сообщений %d", err, len(rec.Messages))
	}
}

// TruncateTo — откат /retry (Х4): ровно до границы обмена, за пределами — ничего.
func TestTruncateTo(t *testing.T) {
	c := New("")
	for _, r := range []string{ollama.RoleUser, ollama.RoleAssistant, ollama.RoleUser, ollama.RoleAssistant, ollama.RoleTool} {
		c.Append(ollama.Message{Role: r})
	}
	if d := c.TruncateTo(9); d != 0 || c.Len() != 5 {
		t.Errorf("n за концом истории: отброшено %d, осталось %d", d, c.Len())
	}
	if d := c.TruncateTo(2); d != 3 || c.Len() != 2 {
		t.Errorf("откат до 2: отброшено %d, осталось %d", d, c.Len())
	}
	if !c.HasAnswer() {
		t.Error("ответ модели в истории есть, а HasAnswer — нет")
	}
	if New("").HasAnswer() {
		t.Error("в пустой истории HasAnswer — да")
	}
}
