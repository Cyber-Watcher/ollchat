package graph

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Перед перезаписью разбиения сохраняется прежнее.
//
// 02.09.2026 пересчёт стёр 1584 описания тем без всякой возможности посмотреть,
// что именно потеряно. Половина потери неизбежна — таких тем больше нет, — но
// текст писала модель, и выбрасывать его молча нельзя.
func TestPreviousPartitionIsKept(t *testing.T) {
	dir := t.TempDir()
	g, err := Create(dir, "проба", 10, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	first := &Communities{Entities: 10, List: []Community{{ID: 1, Title: "первое разбиение"}}}
	if err := g.saveCommunities(first); err != nil {
		t.Fatal(err)
	}
	second := &Communities{Entities: 20, List: []Community{{ID: 2, Title: "второе разбиение"}}}
	if err := g.saveCommunities(second); err != nil {
		t.Fatal(err)
	}

	// Нынешнее — второе.
	cur, err := g.LoadCommunities()
	if err != nil {
		t.Fatal(err)
	}
	if len(cur.List) != 1 || cur.List[0].Title != "второе разбиение" {
		t.Fatalf("нынешнее разбиение не то: %+v", cur.List)
	}

	// Прежнее — рядом, целиком.
	b, err := os.ReadFile(filepath.Join(dir, DirName, PrevCommunityFile))
	if err != nil {
		t.Fatalf("копии прежнего разбиения нет: %v", err)
	}
	var prev Communities
	if err := json.Unmarshal(b, &prev); err != nil {
		t.Fatalf("копия не читается: %v", err)
	}
	if len(prev.List) != 1 || prev.List[0].Title != "первое разбиение" {
		t.Errorf("в копии не то разбиение: %+v", prev.List)
	}
}

// Первое сохранение копии не делает: копировать нечего, и пустого файла
// на диске быть не должно.
func TestFirstSaveMakesNoBackup(t *testing.T) {
	dir := t.TempDir()
	g, err := Create(dir, "проба", 10, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	if err := g.saveCommunities(&Communities{List: []Community{{ID: 1}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, DirName, PrevCommunityFile)); err == nil {
		t.Error("копия создана на первом же сохранении, хотя копировать было нечего")
	}
}
