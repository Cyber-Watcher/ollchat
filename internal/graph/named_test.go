package graph

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestValidName(t *testing.T) {
	good := []string{"", "lab", "lab-2", "x", "a1-b2", "abcdefghij0123456789abcdefghij12"}
	for _, n := range good {
		if !ValidName(n) {
			t.Fatalf("имя %q должно быть допустимым", n)
		}
	}
	// Имя становится частью пути: всё, чем можно выйти из каталога или
	// сломать раскладку, обязано отвергаться.
	bad := []string{"Lab", "лаб", "lab/2", "../graph", "lab.2", "lab 2", "-lab", "a" + string(make([]byte, 40))}
	for _, n := range bad {
		if ValidName(n) {
			t.Fatalf("имя %q не должно быть допустимым", n)
		}
	}
}

func TestDirFor(t *testing.T) {
	if got := DirFor(""); got != "graph" {
		t.Fatalf("умолчание изменилось: %q — собранное раньше перестанет читаться", got)
	}
	if got := DirFor("lab"); got != "graph-lab" {
		t.Fatalf("неожиданный каталог: %q", got)
	}
}

func TestRulesNameAndDir(t *testing.T) {
	if err := (Rules{Name: "lab"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if got := (Rules{Name: "lab"}).Dir("/c"); got != "/c/graph-lab" {
		t.Fatalf("каталог опытного графа: %q", got)
	}
	if got := (Rules{}).Dir("/c"); got != "/c/graph" {
		t.Fatalf("каталог рабочего графа: %q", got)
	}
	if err := (Rules{Name: "../побег"}).Validate(); err == nil {
		t.Fatal("имя с путём должно отклоняться")
	}
}

// ── Версии формата ───────────────────────────────────────────────────────────

// Главное свойство: нынешний граф читается как читался. Тест стоит здесь
// затем, что список версий заводится ради опытного графа, а сломать он может
// рабочий — тот, что стоил недель работы видеокарты.
func TestCurrentFormatStaysReadable(t *testing.T) {
	if !KnownVersion(FormatVersion) {
		t.Fatalf("версия, которой пишутся графы (%d), перестала читаться", FormatVersion)
	}
	if !KnownVersion(1) {
		t.Fatal("формат 1 перестал читаться — нынешний граф собран им")
	}
}

// Чужая версия по-прежнему отвергается: разбирать наугад файл, стоивший недель
// счёта, хуже честного отказа.
func TestUnknownFormatRefused(t *testing.T) {
	for _, v := range []int{0, 99, -1} {
		if KnownVersion(v) {
			t.Fatalf("версия %d не должна считаться знакомой", v)
		}
	}
}

// Перечень для человека — не пустой и содержит нынешнюю версию.
func TestVersionsHuman(t *testing.T) {
	got := versionsHuman()
	if got == "" || !strings.Contains(got, "1") {
		t.Fatalf("перечень версий бесполезен: %q", got)
	}
}

// Ради этого свойства список версий и заведён: рабочий граф формата 1 и опытный
// формата 2 открываются ОДНИМ бинарём, по очереди, без пересборки друг друга.
// Требование владельца 03.09.2026, и оно должно быть проверено, а не обещано.
func TestTwoFormatsInOneProcess(t *testing.T) {
	coll := t.TempDir()

	// Рабочий граф — тем форматом, которым пишутся все нынешние.
	g1, err := CreateKind(coll, "books", 100, Rules{}, CreateOpts{Kind: KindProduction})
	if err != nil {
		t.Fatalf("рабочий граф не создался: %v", err)
	}
	_ = g1.Close()

	// Чужой формат (3) читатель обязан отвергнуть: это защита от разбора
	// наугад файла, стоившего недель счёта.
	labDir := filepath.Join(coll, DirFor("lab"))
	if err := os.MkdirAll(labDir, 0o755); err != nil {
		t.Fatal(err)
	}
	m := Meta{Version: FormatV2 + 1, Collection: "books", Chunks: 100,
		Kind: KindExperimental, Note: "формат из будущего"}
	if err := writeMeta(labDir, m); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(coll, 100, Rules{Name: "lab"}); !errors.Is(err, ErrVersion) {
		t.Fatalf("незнакомый формат должен отвергаться, получено: %v", err)
	}

	// Опытный граф формата 2 (с 04.09.2026 — поддерживаемый) читается тем же
	// бинарём, что и рабочий формата 1, по очереди, в одном процессе.
	m.Version = FormatV2
	m.Note = "новая схема извлечения"
	if err := writeMeta(labDir, m); err != nil {
		t.Fatal(err)
	}
	g2, err := Open(coll, 100, Rules{Name: "lab"})
	if err != nil {
		t.Fatalf("опытный граф формата 2 не открылся: %v", err)
	}
	if g2.meta.Kind != KindExperimental || g2.meta.Note == "" {
		t.Fatalf("паспорт опытного графа потерян: %+v", g2.meta)
	}
	_ = g2.Close()

	g3, err := Open(coll, 100, Rules{})
	if err != nil {
		t.Fatalf("рабочий граф перестал открываться после опытного: %v", err)
	}
	if g3.meta.Version != 1 {
		t.Fatalf("у рабочего графа подменилась версия: %d", g3.meta.Version)
	}
	_ = g3.Close()
}

// ── Отпечаток весов в паспорте ───────────────────────────────────────────────

// Отпечаток весов модели извлечения (решение владельца 05.11.2026) ложится в
// graph.meta и читается обратно обоими читателями паспорта: Open (сборка)
// и QuickHealth (ключ --role-check, файлом без реестра). Версия формата от
// него не меняется.
func TestMetaWeightsRoundTrip(t *testing.T) {
	coll := t.TempDir()
	g, err := Create(coll, "books", 10, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	version := g.Meta().Version
	want := Weights{Files: []string{"sha256-f5f1dd8920d4aaaa", "sha256-0123456789abbbbb"}, Digest: "aaaabbbbccccdddd"}
	in := Weights{Files: slices.Clone(want.Files), Digest: want.Digest}
	if err := g.SetWeights(in); err != nil {
		t.Fatal(err)
	}
	in.Files[0] = "испорчено снаружи" // паспорт держит свою копию среза
	if got := g.Meta().Weights.Files[0]; got != want.Files[0] {
		t.Errorf("паспорт делит срез файлов с вызывающим: %q", got)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}

	g2, err := Open(coll, 10, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	m := g2.Meta()
	_ = g2.Close()
	if m.Weights == nil || !slices.Equal(m.Weights.Files, want.Files) || m.Weights.Digest != want.Digest {
		t.Fatalf("Open: отпечаток %+v, записан %+v", m.Weights, want)
	}
	if m.Version != version {
		t.Fatalf("версия формата сменилась от отпечатка: %d → %d", version, m.Version)
	}
	h, ok := QuickHealth(coll, "", 0)
	if !ok || !slices.Equal(h.Weights.Files, want.Files) || h.Weights.Digest != want.Digest {
		t.Fatalf("QuickHealth: отпечаток %+v (граф найден: %v), записан %+v", h.Weights, ok, want)
	}
	if got := h.Weights.String(); got != "файлы sha256-f5f1dd8920d4 sha256-0123456789ab, digest aaaabbbbcccc" {
		t.Errorf("короткий вид отпечатка: %q", got)
	}
}

// Паспорт до 05.11.2026 — без поля: читается, отпечаток пуст, и граф без
// отпечатка не пишет в паспорт «weights: {}» — старый читатель видит
// прежний файл.
func TestMetaWithoutWeights(t *testing.T) {
	coll := t.TempDir()
	dir := Rules{}.Dir(coll)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := `{"version":1,"collection":"books","model":"qwen3.8:latest","chunks":10,"covered":0,` +
		`"entities":0,"edges":0,"mentions":0,"communities":0,` +
		`"created":"2026-09-01T10:00:00Z","updated":"2026-09-01T10:00:00Z",` +
		`"runs":[{"at":"2026-09-01T10:00:00Z","model":"qwen3.8:latest","prompt_id":"","format":1,"done":5,"covered":5,"seconds":1}]}`
	if err := os.WriteFile(filepath.Join(dir, metaFile), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Stat(dir)
	if err != nil {
		t.Fatalf("старый паспорт не читается: %v", err)
	}
	if m.Weights != nil || m.Model != "qwen3.8:latest" {
		t.Fatalf("старый паспорт: отпечаток %+v, модель %q", m.Weights, m.Model)
	}
	if len(m.Runs) != 1 || m.Runs[0].Weights != nil || m.Runs[0].Replaced != nil {
		t.Fatalf("заход старого паспорта: %+v", m.Runs)
	}
	if h, ok := QuickHealth(coll, "", 0); !ok || !h.Weights.IsZero() || h.Model != "qwen3.8:latest" {
		t.Fatalf("QuickHealth старого паспорта: %+v (граф найден: %v)", h, ok)
	}

	fresh := t.TempDir()
	g, err := Create(fresh, "books", 10, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.SetWeights(Weights{}); err != nil {
		t.Fatal(err)
	}
	_ = g.Close()
	b, err := os.ReadFile(filepath.Join(Rules{}.Dir(fresh), metaFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"weights"`) {
		t.Errorf("пустой отпечаток лёг в паспорт:\n%s", b)
	}
}
