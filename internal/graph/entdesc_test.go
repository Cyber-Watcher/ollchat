package graph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Описания читаются из файла, битые строки пропускаются, а повторная запись
// о том же понятии побеждает прежнюю: файл дописывается, и пересбор описаний
// не должен требовать чистки старых строк руками.
func TestOpenDescriptionsReadsAndOverrides(t *testing.T) {
	dir := t.TempDir()
	lines := strings.Join([]string{
		`{"id":1,"name":"Go","desc":"Язык программирования.","model":"проба"}`,
		`не json вовсе`,
		`{"id":0,"desc":"без номера — пропустить"}`,
		`{"id":2,"desc":"   "}`, // пустое описание — не запись
		`{"id":1,"desc":"Язык для сервисов.","model":"проба-2"}`,
		`{"id":3,"desc":"Многострочное\nописание   с   пробелами"}`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, entDescFile), []byte(lines+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d, err := openDescriptions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Count(); got != 2 {
		t.Fatalf("описаний %d, ожидалось 2 (номера 1 и 3)", got)
	}
	if got := d.Of(1); got != "Язык для сервисов." {
		t.Fatalf("последняя запись не победила: %q", got)
	}
	if got := d.Of(3); got != "Многострочное описание с пробелами" {
		t.Fatalf("перевод строки и лишние пробелы не убраны: %q", got)
	}
	if got := d.Of(2); got != "" {
		t.Fatalf("пустое описание попало в словарь: %q", got)
	}
}

// Отсутствие файла — обычное состояние: описания собираются отдельной работой.
func TestOpenDescriptionsNoFile(t *testing.T) {
	d, err := openDescriptions(t.TempDir())
	if err != nil {
		t.Fatalf("отсутствие файла стало ошибкой: %v", err)
	}
	if d.Count() != 0 || d.Of(1) != "" {
		t.Fatal("пустой словарь должен быть пустым")
	}
}

// Описание идёт в текст вектора отдельным предложением после синонимов
// и обрезается по длине: длинный хвост размывает вектор так же, как хвост
// синонимов (замер 03.09.2026).
func TestEmbedTextAppendsDescription(t *testing.T) {
	e := Entity{ID: 1, Name: "Pod"}
	got := embedText(e, []string{"под"}, 4, "Наименьшая единица развёртывания в Kubernetes.")
	want := "Pod, под. Наименьшая единица развёртывания в Kubernetes."
	if got != want {
		t.Fatalf("текст вектора:\n  получили %q\n  ожидали  %q", got, want)
	}

	long := strings.Repeat("я", descMaxRunes+50)
	got = embedText(e, nil, 4, long)
	body := strings.TrimPrefix(got, "Pod. ")
	if n := len([]rune(body)); n != descMaxRunes {
		t.Fatalf("описание обрезано до %d знаков, ожидалось %d", n, descMaxRunes)
	}

	if got := embedText(e, []string{"под"}, 4, "   "); got != "Pod, под" {
		t.Fatalf("пробельное описание изменило текст: %q", got)
	}
}

// Пока правило выключено, описание в вектор не идёт — даже если файл есть.
// Это главное свойство правки: включение меняет векторы, то есть смысловой
// вход, и происходить это должно по отдельному решению, а не заодно.
func TestVectorDescIsOffByDefault(t *testing.T) {
	dir := t.TempDir()
	write := func(g *Graph) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(g.Dir(), entDescFile),
			[]byte(`{"id":1,"desc":"Язык программирования."}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	g, err := Create(dir, "проба", 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := g.Entities().Add("Go", TypeConcept); err != nil {
		t.Fatal(err)
	}
	write(g)
	if err := g.Save(); err != nil {
		t.Fatal(err)
	}
	g.Close()

	off, err := Open(dir, 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	ent, ok := off.Entities().Get(1)
	if !ok {
		t.Fatal("понятие не нашлось")
	}
	if got := off.EmbedTextOf(ent); got != "Go" {
		t.Fatalf("при выключенном правиле описание попало в вектор: %q", got)
	}
	if off.Descriptions().Count() != 1 {
		t.Fatal("файл описаний не прочитан — правило не должно мешать чтению")
	}
	off.Close()

	on, err := Open(dir, 100, Rules{VectorDesc: true})
	if err != nil {
		t.Fatal(err)
	}
	defer on.Close()
	ent, _ = on.Entities().Get(1)
	if got := on.EmbedTextOf(ent); got != "Go. Язык программирования." {
		t.Fatalf("при включённом правиле описание не попало в вектор: %q", got)
	}
}
