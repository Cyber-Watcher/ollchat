package kb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mbSize переводит мегабайты в байты. Функцией, а не выражением в месте
// вызова: постоянное 8.7*1<<20 в int64 Go не переводит — дробная часть теряется.
func mbSize(v float64) int64 { return int64(v * float64(int64(1)<<20)) }

// TestThinVerdictOnRealBooks — вердикт на НАСТОЯЩИХ числах библиотеки.
//
// Числа взяты из замера 24.09.2026 по коллекции books (559 книг плюс две
// удалённые). Выдуманные пары «кусков и мегабайт» тут бесполезны: порог
// и выбирался по этим книгам, и защищать надо именно от них.
func TestThinVerdictOnRealBooks(t *testing.T) {
	mb := mbSize
	cases := []struct {
		name   string
		chunks int
		size   int64
		want   bool
	}{
		// Те две, из-за которых проверка и появилась.
		{"N8N Intelligence: страницы-картинки", 7, mb(67.1), true},
		{"Building Resilient Distributed Systems: превью издательства", 65, mb(8.7), true},

		// Ближайшие к порогу НАСТОЯЩИЕ книги: их трогать нельзя.
		{"Vector Databases for Enterprise AI", 116, mb(3.4), false},
		{"Kubernetes Networking & Cilium", 118, mb(8.1), false},
		{"CompTIA Linux+ XK0-005: 900 страниц вёрстки", 435, mb(8.6), false},
		{"Building Ambient AI Agents: 17.9 МБ картинок", 172, mb(17.9), false},
		{"Knowledge Graphs", 156, mb(7.2), false},

		// Маленькая книга сама по себе не подозрительна: предел по кускам
		// работает только на файлах крупнее MinMB.
		{"статья на 40 страниц", 60, mb(1.2), false},
		{"тощая, но крошечная", 7, mb(0.3), false},
	}
	var lim ThinLimits // нулевое значение — умолчания замера
	for _, c := range cases {
		got, why := lim.Verdict(c.chunks, c.size)
		if got != c.want {
			t.Errorf("%s: вердикт %v, ожидался %v (причина %q)", c.name, got, c.want, why)
		}
		if got && why == "" {
			t.Errorf("%s: отказ без причины — человеку нечего перепроверить", c.name)
		}
		if got && !strings.Contains(why, "порог") {
			t.Errorf("%s: в причине нет порога: %q", c.name, why)
		}
	}
}

// TestThinLimitsOff — отрицательное значение выключает признак.
func TestThinLimitsOff(t *testing.T) {
	size := mbSize(8.7)
	if thin, _ := (ThinLimits{MinChunks: -1, ChunksPerMB: -1}).Verdict(65, size); thin {
		t.Error("оба признака выключены, а книга объявлена тощей")
	}
	// Выключен только предел по кускам — плотность всё ещё ловит картинки.
	if thin, _ := (ThinLimits{MinChunks: -1}).Verdict(7, mbSize(67.1)); !thin {
		t.Error("плотность текста должна ловить страницы-картинки и при выключенном пределе по кускам")
	}
}

// TestThinLimitsDefaults — умолчания именно те, что выбраны замером.
func TestThinLimitsDefaults(t *testing.T) {
	got := ThinLimits{}.resolve()
	if got.MinChunks != DefaultThinMinChunks || got.MinMB != DefaultThinMinMB || got.ChunksPerMB != DefaultThinChunksPerMB {
		t.Errorf("умолчания %+v, ожидались %d/%d/%.1f", got,
			DefaultThinMinChunks, DefaultThinMinMB, DefaultThinChunksPerMB)
	}
}

// makeThinDoc кладёт настоящий текстовый документ, который велик, но текста
// в нём почти нет: так выглядят книги-картинки и превью издательства.
// Разбор идёт настоящий — документ читается тем же кодом, что и книги.
//
// Добивка — длинными строками из пробелов, а не переводами строки: у текстовых
// документов есть предел в 65 535 строк, и миллион пустых строк отвергается
// раньше, чем дело доходит до проверки на тощую книгу (поймано тестом).
func makeThinDoc(t *testing.T, dir, name string, mb int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	var b strings.Builder
	b.WriteString("# Building Resilient Distributed Systems\n\n")
	// Настоящего текста — на десяток кусков: превью издательства именно такое,
	// от каждой главы по абзацу. Меньше нельзя: документ без кусков вовсе
	// ловится проверкой на скан, то есть до нашей (поймано тестом).
	for i := 1; i <= 16; i++ {
		fmt.Fprintf(&b, "## Chapter %d. Timeouts and retries\n\n", i)
		b.WriteString("Timeouts are the humble beginning of resilience, and a retry " +
			"without a budget is an outage waiting for a queue. This preview keeps " +
			"only the opening paragraph of the chapter, and the rest of the book " +
			"lives in pictures that carry no text layer at all.\n\n")
	}
	pad := strings.Repeat(" ", 8<<10) + "\n"
	for written := 0; written < mb*(1<<20); written += len(pad) {
		b.WriteString(pad)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestThinBookNotIndexed — тощая книга в индекс не попадает, но запись о ней
// остаётся с причиной, и поиск по ней ничего не находит.
func TestThinBookNotIndexed(t *testing.T) {
	base, books := newBase(t)
	os.MkdirAll(books, 0o755)
	makeThinDoc(t, books, "preview.md", 4)
	makeBook(t, books, "real.pdf", longPage("kubernetes deployment rollout"))

	c, _ := base.Create("test", "")
	res, err := c.Add(context.Background(), []string{books}, IndexOpts{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Thin != 1 {
		t.Fatalf("тощих книг %d, ожидалась одна (добавлено %d, сканов %d, пропущено %d, сбоев %d)",
			res.Thin, res.Added, res.Scans, res.Skipped, res.Errors)
	}
	if res.Added != 1 {
		t.Fatalf("добавлено книг %d, ожидалась одна — настоящая", res.Added)
	}
	if len(res.ThinBooks) != 1 || !strings.Contains(res.ThinBooks[0].Reason, "порог") {
		t.Fatalf("отчёт о тощей книге без причины с порогом: %+v", res.ThinBooks)
	}
	if hits, _ := c.Search("timeouts", DefaultSearchOpts()); len(hits) != 0 {
		t.Errorf("тощая книга попала в индекс: %d совпадений", len(hits))
	}
	if hits, _ := c.Search("kubernetes", DefaultSearchOpts()); len(hits) == 0 {
		t.Error("настоящая книга не нашлась")
	}
	// Запись должна остаться: иначе каждая доливка перечитывала бы её заново.
	var found bool
	for _, d := range c.LiveBooks() {
		if strings.HasSuffix(d.Path, "preview.md") {
			found = true
			if d.Kind != BookThin {
				t.Errorf("вид книги %q, ожидался %q", d.Kind, BookThin)
			}
			if d.Err == "" {
				t.Error("в реестре нет причины отказа")
			}
		}
	}
	if !found {
		t.Error("записи о тощей книге нет в реестре — доливка будет читать её снова и снова")
	}
}

// TestThinBookKeepThin — человек посмотрел на причину и велел взять книгу:
// ключ обязан вернуть к разбору уже отвергнутый и не изменившийся файл.
func TestThinBookKeepThin(t *testing.T) {
	base, books := newBase(t)
	os.MkdirAll(books, 0o755)
	makeThinDoc(t, books, "preview.md", 4)

	c, _ := base.Create("test", "")
	c.AddRoots([]string{books})
	if _, err := c.Add(context.Background(), []string{books}, IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	if hits, _ := c.Search("timeouts", DefaultSearchOpts()); len(hits) != 0 {
		t.Fatal("книга взята в индекс без ключа")
	}

	res, err := c.Sync(context.Background(), IndexOpts{KeepThin: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 {
		t.Fatalf("с --kb-keep-thin добавлено книг %d, ожидалась одна", res.Added)
	}
	if res.Thin != 0 {
		t.Errorf("с --kb-keep-thin отвергнуто %d книг, ожидалось ноль", res.Thin)
	}
	if hits, _ := c.Search("timeouts", DefaultSearchOpts()); len(hits) == 0 {
		t.Error("книга не ищется, хотя её велели взять")
	}
}

// TestShortTextFileIsNotScan — текстовый файл короче одного куска записывается
// как короткий, а не как скан, и доктор называет его своим разделом, без совета
// про /addimg. До 02.11.2026 заглушка Go на 176 знаков числилась «сканом без
// текстового слоя», и из пяти таких доктор называл три.
func TestShortTextFileIsNotScan(t *testing.T) {
	base, books := newBase(t)
	os.MkdirAll(books, 0o755)
	stub := "//go:build !unix\n\npackage kb\n\n// На системах без сигналов процесс проверяется иначе.\nvar sigZero = 0\n"
	if err := os.WriteFile(filepath.Join(books, "signal_other.go"), []byte(stub), 0o644); err != nil {
		t.Fatal(err)
	}
	makeBook(t, books, "real.pdf", longPage("kubernetes deployment rollout"))

	c, _ := base.Create("test", "")
	res, err := c.Add(context.Background(), []string{books}, IndexOpts{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Short != 1 || res.Scans != 0 {
		t.Fatalf("коротких %d, сканов %d — ожидался один короткий и ни одного скана", res.Short, res.Scans)
	}
	var rec BookRec
	for _, d := range c.LiveBooks() {
		if filepath.Base(d.Path) == "signal_other.go" {
			rec = d
		}
	}
	if rec.Kind != BookShort || !strings.Contains(rec.Err, "короче одного куска") {
		t.Fatalf("запись о коротком файле: вид %q, причина %q", rec.Kind, rec.Err)
	}
	if st := c.Stats(); st.Short != 1 || st.Scans != 0 {
		t.Errorf("сводка: коротких %d, сканов %d", st.Short, st.Scans)
	}

	out := Doctor(c, DoctorOpts{})
	if !strings.Contains(out, "Короче одного куска: 1") || !strings.Contains(out, "signal_other.go") {
		t.Errorf("доктор не назвал короткий файл своим разделом:\n%s", out)
	}
	if strings.Contains(out, "Сканы без текстового слоя") || strings.Contains(out, "/addimg") {
		t.Errorf("короткий файл попал в сканы:\n%s", out)
	}
}

// TestDataURIMarkdownNotThin — markdown с картинками base64 не объявляется
// тощим и индексируется.
//
// Куски считаются по тексту, из которого тела картинок вырезаны, а размер
// до правки брался у файла вместе с картинками: 600 строк текста и 14 картинок
// по 330 КБ — 64 куска на 4,6 МБ, «похоже на превью издательства», и файл
// не попадал в индекс вовсе (так случилось бы с файлом плана 02.11.2026).
func TestDataURIMarkdownNotThin(t *testing.T) {
	base, books := newBase(t)
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	img := fakeBase64(330 << 10)
	var b strings.Builder
	b.WriteString("# План работ\n\n")
	images := 0
	for i := 0; i < 600; i++ {
		if i%43 == 10 && images < 14 {
			fmt.Fprintf(&b, "![](data:image/png;base64,%s)\n", img)
			images++
		}
		fmt.Fprintf(&b, "Строка плана %d: настройка таймаутов службы и проверка индекса книг по разделам.\n", i)
	}
	if n := strings.Count(b.String(), ";base64,"); n != 14 {
		t.Fatalf("картинок в образце %d, нужно 14", n)
	}
	path := filepath.Join(books, "plan.md")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Size() <= int64(DefaultThinMinMB)<<20 {
		t.Fatalf("образец %d байт — не крупнее порога в %d МБ, проверка ничего не проверит", info.Size(), DefaultThinMinMB)
	}

	c, err := base.Create("test", "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Add(context.Background(), []string{books}, IndexOpts{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var rec BookRec
	for _, d := range c.LiveBooks() {
		rec = d
	}
	if rec.Kind != BookOK || res.Added != 1 || res.Thin != 0 {
		t.Fatalf("вид %q, добавлено %d, тощих %d, причина %q — markdown с картинками должен войти в индекс",
			rec.Kind, res.Added, res.Thin, rec.Err)
	}
	if hits, _ := c.Search("таймаутов службы", DefaultSearchOpts()); len(hits) == 0 {
		t.Error("книга в индексе, но не ищется")
	}
}

// TestOldScanTextRecBecomesShort — запись «скан» у короткого текстового файла,
// оставшаяся от прежних версий, при доливке переходит в «короче одного куска».
//
// Файл не менялся, и до правки обход пропускал его без чтения: запись «скан»
// жила вечно, а доктор советовал ей /addimg — распознавание картинок для
// заглушки Go на 176 знаков. Скан PDF при этом заново не читается: у него
// причина та же, а чтение стоит.
func TestOldScanTextRecBecomesShort(t *testing.T) {
	base, books := newBase(t)
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := base.Create("test", "")
	if err != nil {
		t.Fatal(err)
	}
	old := func(name, body string) string {
		p := filepath.Join(books, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		// Так запись «скан» выглядела до 02.11.2026: без формата и без номера.
		if err := c.appendDoc(BookRec{Path: p, Size: info.Size(), ModTime: info.ModTime().UnixNano(),
			At: time.Now().Unix(), Kind: BookScan, Err: "текста не нашлось"}); err != nil {
			t.Fatal(err)
		}
		return p
	}
	stub := old("signal_other.go", "//go:build !unix\n\npackage kb\n\nvar sigZero = 0\n")
	note := old("note.md", "# Заметка\n\nКоротко.\n")
	old("scan.pdf", "%PDF-1.4")

	res, err := c.Add(context.Background(), []string{books}, IndexOpts{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Short != 2 || res.Scans != 0 {
		t.Errorf("при доливке коротких %d, сканов %d — ожидались два коротких и скан PDF без перечитывания",
			res.Short, res.Scans)
	}
	kinds := map[string]BookKind{}
	for _, d := range c.LiveBooks() {
		kinds[filepath.Base(d.Path)] = d.Kind
	}
	for _, p := range []string{stub, note} {
		if k := kinds[filepath.Base(p)]; k != BookShort {
			t.Errorf("%s: вид %q, ожидался %q", filepath.Base(p), k, BookShort)
		}
	}
	if k := kinds["scan.pdf"]; k != BookScan {
		t.Errorf("scan.pdf: вид %q — скан PDF трогать незачем", k)
	}

	// Запись дошла до реестра на диске, а не только до памяти процесса.
	again, err := base.Open("test")
	if err != nil {
		t.Fatal(err)
	}
	if st := again.Stats(); st.Short != 2 || st.Scans != 1 {
		t.Errorf("после переоткрытия коротких %d, сканов %d — ожидалось 2 и 1", st.Short, st.Scans)
	}
	if out := Doctor(again, DoctorOpts{}); !strings.Contains(out, "Сканы без текстового слоя: 1") ||
		!strings.Contains(out, "Короче одного куска: 2") {
		t.Errorf("доктор числит короткие файлы сканами:\n%s", out)
	}
}

// TestDoctorScansTailCounted — из пяти сканов доктор называет три и говорит
// «…и ещё 2», а не молчит об остальных.
func TestDoctorScansTailCounted(t *testing.T) {
	base, books := newBase(t)
	os.MkdirAll(books, 0o755)
	c, _ := base.Create("test", "")
	for i := 1; i <= 5; i++ {
		p := filepath.Join(books, fmt.Sprintf("scan%d.pdf", i))
		if err := os.WriteFile(p, []byte("%PDF-1.4"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := c.appendDoc(BookRec{ID: uint32(i), Path: p, Kind: BookScan}); err != nil {
			t.Fatal(err)
		}
	}
	out := Doctor(c, DoctorOpts{})
	if !strings.Contains(out, "Сканы без текстового слоя: 5") || !strings.Contains(out, "…и ещё 2") {
		t.Errorf("доктор не досчитал хвост сканов:\n%s", out)
	}
}
