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
	if err := g.saveNewPartition(first); err != nil {
		t.Fatal(err)
	}
	second := &Communities{Entities: 20, List: []Community{{ID: 2, Title: "второе разбиение"}}}
	if err := g.saveNewPartition(second); err != nil {
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

	if err := g.saveNewPartition(&Communities{List: []Community{{ID: 1}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, DirName, PrevCommunityFile)); err == nil {
		t.Error("копия создана на первом же сохранении, хотя копировать было нечего")
	}
}

// Копию прежнего разбиения перекладывает только новое разбиение. Описания
// тем — ленивые и пачками — пишут то же разбиение и копию не трогают.
//
// До 07.10.2026 копия перекладывалась при любой записи, и первое же ленивое
// описание после пересчёта заменяло прежнее разбиение копией текущего
// (аудит, 4.5).
func TestDescriptionsKeepPreviousPartition(t *testing.T) {
	dir := t.TempDir()
	g, err := Create(dir, "проба", 10, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	prevTitle := func() string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, DirName, PrevCommunityFile))
		if err != nil {
			return ""
		}
		var prev Communities
		if err := json.Unmarshal(b, &prev); err != nil {
			t.Fatalf("копия не читается: %v", err)
		}
		return prev.List[0].Title
	}

	first := &Communities{List: []Community{{ID: 1, Members: members(1, 5)}}}
	must(t, g.saveNewPartition(first))
	first.List[0].Title = "первое, описанное"
	must(t, g.saveCommunities(first)) // порция резюме
	if got := prevTitle(); got != "" {
		t.Fatalf("описание того же разбиения переложило копию: %q", got)
	}

	second := &Communities{List: []Community{{ID: 1, Members: members(1, 6)}}}
	must(t, g.saveNewPartition(second))
	loaded, err := g.LoadCommunities()
	if err != nil {
		t.Fatal(err)
	}
	loaded.List[0].Title = "второе, описанное лениво"
	must(t, g.saveCommunitiesGuarded(loaded))
	if got := prevTitle(); got != "первое, описанное" {
		t.Fatalf("копия прежнего разбиения после ленивого описания: %q, ожидалось описанное первое", got)
	}
}
