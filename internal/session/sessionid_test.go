package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Аудит 16.10.2026, находка 5: идентификатор сессии — имя файла, а не поле
// внутри JSON; подложенный файл с `"id":"../../escape"` или с чужим id не
// выводит автосохранение за каталог и не затирает чужую сессию.
func TestSessionIDComesFromFileName(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "home", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	planted := `{"id":"../../escape","saved_at":"2026-10-16T10:00:00Z","server":"s","model":"m","messages":[]}`
	if err := os.WriteFile(filepath.Join(dir, "2026-10-16_100000-abc.json"), []byte(planted), 0o600); err != nil {
		t.Fatal(err)
	}
	st := NewStore(dir)
	list, err := st.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("List: %v, %d", err, len(list))
	}
	if list[0].ID != "2026-10-16_100000-abc" {
		t.Errorf("ID из JSON, а не из имени файла: %q", list[0].ID)
	}
	if _, _, err := st.SaveAs("../../escape", New(""), "s", "m"); err == nil {
		t.Error("SaveAs с id «../../escape» прошёл")
	}
	if _, err := os.Stat(filepath.Join(base, "escape.json")); err == nil {
		t.Error("файл записан вне каталога сессий")
	}
	if _, err := st.Load("../x"); err == nil || !strings.Contains(err.Error(), "недопустим") {
		t.Errorf("Load с «../x» не отказал: %v", err)
	}
	other := `{"id":"2026-10-15_090000-def","saved_at":"2026-10-15T09:00:00Z","server":"s","model":"m","messages":[{"role":"user","content":"ценный диалог"}]}`
	if err := os.WriteFile(filepath.Join(dir, "2026-10-15_090000-def.json"), []byte(other), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "copy.json"), []byte(strings.Replace(other, "ценный диалог", "копия", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, err := st.load(filepath.Join(dir, "copy.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SaveAs(rec.ID, New(""), "s", "m"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "2026-10-15_090000-def.json"))
	if !strings.Contains(string(data), "ценный диалог") {
		t.Error("возобновление copy.json затёрло чужую сессию")
	}
	for id, ok := range map[string]bool{"2026-10-16_100000-abc": true, "a_b-C9": true, "": false, "../x": false, "a/b": false, "a b": false, "а": false} {
		if ValidID(id) != ok {
			t.Errorf("ValidID(%q) = %v", id, !ok)
		}
	}
}
