package graph

import (
	"errors"
	"os"
	"path/filepath"
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
	g1, err := CreateKind(coll, "books", 100, KindProduction, "", Rules{})
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
