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

func bookAt(t *testing.T, dir, name, content string, id uint32) BookRec {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return BookRec{ID: id, Path: p, Size: info.Size(), Kind: BookOK, Chunks: 100}
}

// Побайтово одинаковые книги под разными именами находятся.
//
// Именно этот вид повтора не ловится по названию: копию почти всегда
// переименовывают. А вред от него прямой — две одинаковые выдержки занимают
// два места из восьми в выдаче.
func TestSameContentFindsRenamedCopy(t *testing.T) {
	dir := t.TempDir()
	a := bookAt(t, dir, "AI/Книга.pdf", "одинаковое содержимое", 1)
	b := bookAt(t, dir, "DevOps/Book (1).pdf", "одинаковое содержимое", 2)
	other := bookAt(t, dir, "AI/Другая.pdf", "иное содержимое", 3)

	groups := SameContent([]BookRec{b, a, other})
	if len(groups) != 1 {
		t.Fatalf("групп повторов %d, ожидалась 1", len(groups))
	}
	if len(groups[0]) != 2 {
		t.Fatalf("в группе %d книг, ожидалось 2", len(groups[0]))
	}
	// Первой идёт книга с меньшим номером: она проиндексирована раньше,
	// и на неё больше ссылок в графе — оставлять правильно её.
	if groups[0][0].ID != 1 {
		t.Errorf("первой в группе id=%d, ожидался 1", groups[0][0].ID)
	}
}

// Книги одинакового размера, но разного содержания повтором не считаются.
//
// Размер — только сито: совпадений по нему много, и объявлять их повторами
// значило бы предлагать человеку удалить нужную книгу.
func TestSameContentDistinguishesBySize(t *testing.T) {
	dir := t.TempDir()
	a := bookAt(t, dir, "a.pdf", "12345", 1)
	b := bookAt(t, dir, "b.pdf", "54321", 2) // тот же размер, другое содержимое

	if got := SameContent([]BookRec{a, b}); len(got) != 0 {
		t.Errorf("разные книги одного размера объявлены повтором: %v", got)
	}
}

// Пропавший с диска файл не роняет сверку.
func TestSameContentToleratesMissingFile(t *testing.T) {
	dir := t.TempDir()
	a := bookAt(t, dir, "a.pdf", "текст", 1)
	ghost := BookRec{ID: 2, Path: filepath.Join(dir, "нет.pdf"), Size: a.Size, Kind: BookOK}

	if got := SameContent([]BookRec{a, ghost}); len(got) != 0 {
		t.Errorf("пропавший файл не должен считаться повтором: %v", got)
	}
}

// Пустой отчёт говорит, что всё в порядке, а не молчит.
func TestDoctorSaysAllGood(t *testing.T) {
	base, err := OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	c, err := base.Create("proba", "")
	if err != nil {
		t.Fatal(err)
	}
	out := Doctor(c, DoctorOpts{Deep: true})
	if !strings.Contains(out, "Всё в порядке") {
		t.Errorf("пустая коллекция должна отвечать «всё в порядке»:\n%s", out)
	}
}

// Цифры в названии различают книги, а не украшают его.
//
// **Замер 30.08.2026 на живой библиотеке.** Первая редакция выбрасывала все
// цифры и объявляла одной книгой три тома «Using and Administering Linux»,
// «Tools and Skills for .NET 8» и «для .NET 10», «C# 12 and .NET 8»
// и «C# 13 and .NET 9». Ложное срабатывание тут дороже пропуска: человек,
// поверив отчёту, удалит второй том книги, которую читает.
func TestNormalizeTitleKeepsDistinguishingDigits(t *testing.T) {
	different := [][2]string{
		{"Using and Administering Linux Volume 1, Zero to SysAdmin, 2nd Edition 2023",
			"Using and Administering Linux Volume 3, Zero to SysAdmin, 2nd Edition 2023"},
		{"Tools and Skills for .NET 8", "Tools and Skills for .NET 10"},
		{"C# 12 and .NET 8 - Modern Cross-Platform Development Fundamentals",
			"C# 13 and .NET 9  Modern Cross-Platform Development Fundamentals"},
	}
	for _, p := range different {
		if normalizeTitle(p[0]) == normalizeTitle(p[1]) {
			t.Errorf("разные книги слились в одну:\n  %s\n  %s\n  обе → %q",
				p[0], p[1], normalizeTitle(p[0]))
		}
	}
}

// Год издания по-прежнему не различает: одна книга разных годов — это
// как раз то, что стоит показать рядом.
func TestNormalizeTitleDropsYear(t *testing.T) {
	same := [][2]string{
		{"Docker Deep Dive 2023", "Docker Deep Dive 2025"},
		{"Linux Command Reference", "Linux Command Reference 2024"},
	}
	for _, p := range same {
		if normalizeTitle(p[0]) != normalizeTitle(p[1]) {
			t.Errorf("одна книга разных лет не сошлась:\n  %q\n  %q",
				normalizeTitle(p[0]), normalizeTitle(p[1]))
		}
	}
	// А номер версии из четырёх цифр, не похожий на год, остаётся.
	if normalizeTitle("Windows 3111 guide") == normalizeTitle("Windows guide") {
		t.Error("число, не похожее на год, выбрасывать нельзя")
	}
}

// Знак препинания разделяет слова, а не склеивает их.
func TestNormalizeTitleSplitsOnPunctuation(t *testing.T) {
	if got := normalizeTitle("Go:Concurrency"); got != "go concurrency" {
		t.Errorf("normalizeTitle = %q, ожидалось \"go concurrency\"", got)
	}
}

// Наличие файла проверяется у книги любого вида, а не только прочитанной.
//
// **Замер 30.08.2026.** Человек удалил с диска три книги-скана, а доктор
// продолжал показывать их среди «сканов без текста» и молчал о том, что
// файлов нет: проверка `os.Stat` стояла только в ветке успешно прочитанных.
func TestDoctorSeesMissingScans(t *testing.T) {
	base, err := OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	c, err := base.Create("proba", "")
	if err != nil {
		t.Fatal(err)
	}
	// Скан, чьего файла на диске нет.
	if err := c.appendDoc(BookRec{ID: 1, Path: "/нет/такого/скан.pdf", Kind: BookScan}); err != nil {
		t.Fatal(err)
	}

	out := Doctor(c, DoctorOpts{})
	if !strings.Contains(out, "Пропали с диска") {
		t.Errorf("удалённый скан должен считаться пропавшим:\n%s", out)
	}
	if strings.Contains(out, "Сканы без текстового слоя") {
		t.Errorf("пропавшая книга не должна числиться и сканом:\n%s", out)
	}
}

// Отчёт кончается тем, что делать, а не только тем, что не так.
func TestDoctorTellsWhatToDo(t *testing.T) {
	base, err := OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	c, err := base.Create("proba", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.appendDoc(BookRec{ID: 1, Path: "/нет/такого/книга.pdf", Kind: BookOK}); err != nil {
		t.Fatal(err)
	}

	out := Doctor(c, DoctorOpts{})
	if !strings.Contains(out, "Что сделать") || !strings.Contains(out, "--kb-sync proba") {
		t.Errorf("отчёт должен назвать команду:\n%s", out)
	}
}

// Список удалённых книг показывается целиком и с меткой про граф.
//
// Обрезанный список негоден для того, ради чего он и заведён: его читают
// глазами, сверяя с тем, что человек удалял сам.
func TestDoctorListsAllDeletedWithGraphMark(t *testing.T) {
	base, err := OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	c, err := base.Create("proba", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for i := 1; i <= 12; i++ {
		r := bookAt(t, dir, fmt.Sprintf("книга-%02d.pdf", i), fmt.Sprintf("текст %d", i), uint32(i))
		if err := c.appendDoc(r); err != nil {
			t.Fatal(err)
		}
		if err := c.Forget(r.Path); err != nil {
			t.Fatal(err)
		}
	}

	// Граф якобы разобрал только первую книгу.
	out := Doctor(c, DoctorOpts{InGraph: func(id uint32) bool { return id == 1 }})

	for i := 1; i <= 12; i++ {
		if !strings.Contains(out, fmt.Sprintf("книга-%02d.pdf", i)) {
			t.Errorf("в списке нет книги %d — список обрезан:\n%s", i, out)
		}
	}
	if strings.Contains(out, "и ещё") {
		t.Error("список удалённых обрезаться не должен")
	}
	if !strings.Contains(out, "[в графе] книга-01.pdf") {
		t.Errorf("нет метки о графе у разобранной книги:\n%s", out)
	}
	if strings.Contains(out, "[в графе] книга-02.pdf") {
		t.Error("метка о графе поставлена неразобранной книге")
	}
}

// Два числа в отчёте — про разное, и это должно быть видно.
//
// «Пропали с диска: 5» и «Уже помечены удалёнными: 15» читаются как
// противоречие, если не сказать, что первое — незаконченная работа,
// а второе — законченная.
func TestDoctorSeparatesMissingFromDeleted(t *testing.T) {
	base, err := OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	c, err := base.Create("proba", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	// Одна помечена удалённой, другая просто исчезла с диска.
	marked := bookAt(t, dir, "помеченная.pdf", "текст", 1)
	if err := c.appendDoc(marked); err != nil {
		t.Fatal(err)
	}
	if err := c.Forget(marked.Path); err != nil {
		t.Fatal(err)
	}
	if err := c.appendDoc(BookRec{ID: 2, Path: dir + "/исчезнувшая.pdf", Kind: BookOK}); err != nil {
		t.Fatal(err)
	}

	out := Doctor(c, DoctorOpts{})
	if !strings.Contains(out, "но ещё числятся в коллекции") {
		t.Errorf("не сказано, что пропавшие ещё числятся:\n%s", out)
	}
	if !strings.Contains(out, "Уже помечены удалёнными") {
		t.Errorf("не сказано, что помеченные — сделанная работа:\n%s", out)
	}
	// Помеченная книга не должна попасть в «пропали»: это разные множества.
	if strings.Contains(out[:strings.Index(out, "Уже помечены")], "помеченная.pdf") {
		t.Error("помеченная удалённой книга попала в раздел пропавших")
	}
}

// Кисть красит названия книг и не трогает сообщения программы.
func TestDoctorPaintsOnlyBookNames(t *testing.T) {
	base, err := OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	c, err := base.Create("proba", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.appendDoc(BookRec{ID: 1, Path: t.TempDir() + "/пропавшая.pdf", Kind: BookOK}); err != nil {
		t.Fatal(err)
	}

	out := Doctor(c, DoctorOpts{Paint: func(s string) string { return "<<" + s + ">>" }})
	if !strings.Contains(out, "<<пропавшая.pdf>>") {
		t.Errorf("название книги не покрашено:\n%s", out)
	}
	// Совет в конце — сообщение программы, а не название книги.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "ollchat --kb-sync") && strings.Contains(line, "<<") {
			t.Errorf("покрашено сообщение программы: %q", line)
		}
	}
	// Без кисти отчёт остаётся чистым текстом: его перенаправляют в файл.
	if plain := Doctor(c, DoctorOpts{}); strings.Contains(plain, "<<") {
		t.Errorf("отчёт без кисти покрашен:\n%s", plain)
	}
}

// Доктор докладывает о ходе долгих шагов: молчащая программа неотличима
// от зависшей, а проверка идёт секунды.
func TestDoctorReportsProgress(t *testing.T) {
	base, err := OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	c, err := base.Create("proba", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// Две одинаковые книги: только они и доходят до сверки по содержимому.
	for i, name := range []string{"одна.pdf", "другая.pdf"} {
		if err := c.appendDoc(bookAt(t, dir, name, "один и тот же текст", uint32(i+1))); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[string]int{}
	Doctor(c, DoctorOpts{Deep: true, Step: func(stage string, done, total int) {
		if done > total && total > 0 {
			t.Errorf("шаг %q: сделано %d больше, чем всего %d", stage, done, total)
		}
		seen[stage]++
	}})
	for _, want := range []string{"проверяю книги на месте", "сверяю книги по содержимому"} {
		if seen[want] == 0 {
			t.Errorf("о шаге %q не доложено; доложено: %v", want, seen)
		}
	}
}

// Пояснение и список книг разделены пустой строкой.
//
// Без неё перечень читается как продолжение фразы: и объяснение, и первая
// книга идут с одним отступом в два пробела и сливаются в один абзац.
func TestDoctorBlankLineBeforeDeletedList(t *testing.T) {
	base, err := OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	c, err := base.Create("proba", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	rec := bookAt(t, dir, "убранная.pdf", "текст", 1)
	if err := c.appendDoc(rec); err != nil {
		t.Fatal(err)
	}
	if err := c.Forget(rec.Path); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(Doctor(c, DoctorOpts{}), "\n")
	for i, l := range lines {
		if !strings.Contains(l, "убранная.pdf") {
			continue
		}
		if i == 0 || strings.TrimSpace(lines[i-1]) != "" {
			t.Fatalf("над списком книг нет пустой строки, выше стоит %q", lines[i-1])
		}
		return
	}
	t.Fatal("книги в списке помеченных удалёнными нет вовсе")
}

// Книгу положили в каталог, а проиндексировать забыли — доктор обязан сказать.
//
// Худший вид молчания: человек уверен, что книга в базе, поиск её не находит,
// и объяснить это нечем. Прежде доктор проверял только одну сторону — на месте
// ли книги, о которых он знает, — и отвечал «всё в порядке».
func TestDoctorFindsUnindexedBooks(t *testing.T) {
	base, books := newBase(t)
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	makeBook(t, books, "старая.pdf", longPage("goroutines and channels"))

	c, err := base.Create("proba", "")
	if err != nil {
		t.Fatal(err)
	}
	// Каталоги записывает вызывающий: Add сам их не запоминает.
	if err := c.AddRoots([]string{books}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Add(context.Background(), []string{books}, IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	if out := Doctor(c, DoctorOpts{}); !strings.Contains(out, "Всё в порядке") {
		t.Fatalf("сразу после индексации отчёт не чист:\n%s", out)
	}

	// Книга появилась в каталоге уже после индексации.
	makeBook(t, books, "новая.pdf", longPage("kubernetes deployment"))

	out := Doctor(c, DoctorOpts{})
	if !strings.Contains(out, "Лежат в каталогах, но в индексе их нет: 1") {
		t.Errorf("новая книга не замечена:\n%s", out)
	}
	if !strings.Contains(out, "новая.pdf") {
		t.Errorf("имя новой книги не названо:\n%s", out)
	}
	if !strings.Contains(out, "--kb-refresh proba") {
		t.Errorf("не сказано, чем это лечится:\n%s", out)
	}
	if strings.Contains(out, "Всё в порядке") {
		t.Errorf("отчёт называет непрочитанную книгу порядком:\n%s", out)
	}
}

// Сводка не расходится с разделами отчёта.
//
// Она существует затем, чтобы не читать сотню строк ради ответа «всё ли
// в порядке». Соврав числом, она хуже своего отсутствия: человек поверит ей
// и не станет читать подробности.
func TestDoctorSummaryMatchesSections(t *testing.T) {
	base, books := newBase(t)
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	makeBook(t, books, "живая.pdf", longPage("goroutines and channels"))
	rec := makeBook(t, books, "убранная.pdf", longPage("kubernetes deployment"))

	c, err := base.Create("proba", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AddRoots([]string{books}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Add(context.Background(), []string{books}, IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Forget(rec); err != nil {
		t.Fatal(err)
	}
	// Две книги, которых нет в индексе.
	makeBook(t, books, "новая1.pdf", longPage("docker containers"))
	makeBook(t, books, "новая2.pdf", longPage("mutex and locking"))

	out := Doctor(c, DoctorOpts{})
	head, rest, ok := strings.Cut(out, "\nЛежат в каталогах")
	if !ok {
		t.Fatalf("в отчёте нет раздела о непроиндексированных:\n%s", out)
	}
	if !strings.Contains(head, "Коротко") {
		t.Fatalf("сводки нет:\n%s", head)
	}
	// Число в сводке должно совпасть с числом в заголовке раздела.
	if !strings.Contains(head, "   2  не в индексе") {
		t.Errorf("сводка говорит не то же, что раздел (2 книги):\n%s", head)
	}
	if !strings.Contains(rest, "их нет: 2\n") {
		t.Errorf("раздел говорит не о двух книгах:\n%s", rest[:120])
	}
	if !strings.Contains(head, "   1  помечены удалёнными") {
		t.Errorf("забытая книга не попала в сводку:\n%s", head)
	}
}

// У здоровой коллекции сводки нет: перечислять нечего.
func TestDoctorNoSummaryWhenClean(t *testing.T) {
	base, books := newBase(t)
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	makeBook(t, books, "одна.pdf", longPage("goroutines and channels"))

	c, err := base.Create("proba", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AddRoots([]string{books}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Add(context.Background(), []string{books}, IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	out := Doctor(c, DoctorOpts{})
	if strings.Contains(out, "Коротко") {
		t.Errorf("у чистой коллекции появилась сводка:\n%s", out)
	}
	if !strings.Contains(out, "Всё в порядке") {
		t.Errorf("чистая коллекция не названа чистой:\n%s", out)
	}
}

// Копия уже проиндексированной книги не выдаётся за «новую».
//
// Иначе получается круг: проверка говорит «не в индексе, поиск не находит»,
// человек запускает доливку, доливка пропускает файл как повтор — и проверка
// говорит то же самое снова. Владелец прошёл этот круг дважды (30.08.2026).
func TestDoctorSeparatesDiskCopiesFromNew(t *testing.T) {
	base, books := newBase(t)
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	orig := makeBook(t, books, "оригинал.pdf", longPage("goroutines and channels"))

	c, err := base.Create("proba", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AddRoots([]string{books}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Add(context.Background(), []string{books}, IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}

	// Ту же книгу положили в каталог второй раз под другим именем.
	data, err := os.ReadFile(orig)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(books, "копия.pdf"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	out := Doctor(c, DoctorOpts{})
	if strings.Contains(out, "но в индексе их нет") {
		t.Errorf("копия объявлена новой книгой:\n%s", out)
	}
	if !strings.Contains(out, "Лежат в каталогах копиями: 1") {
		t.Errorf("копия не названа копией:\n%s", out)
	}
	if !strings.Contains(out, "то же, что: оригинал.pdf") {
		t.Errorf("не сказано, с чем совпала копия:\n%s", out)
	}
	// Совет «долить» на копию не действует — предлагать его нельзя.
	if strings.Contains(out, "--kb-refresh") {
		t.Errorf("отчёт советует доливку, которая эту книгу пропустит:\n%s", out)
	}
	if strings.Contains(out, "Всё в порядке") {
		t.Errorf("копия на диске названа порядком:\n%s", out)
	}
}

// Книга, изменившаяся на диске после индексации, показывается доктором отдельно
// от новых — и особо, если размер не изменился: такая правка (этап 92) уезжала
// мимо индекса молча, и найти её можно было только глазами.
func TestDoctorSeesFileChangedAfterIndexing(t *testing.T) {
	base, err := OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	c, err := base.Create("proba", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := c.AddRoots([]string{dir}); err != nil {
		t.Fatal(err)
	}
	rec := bookAt(t, dir, "заметки.md", "строка один\nстрока два\n", 1)
	info, _ := os.Stat(rec.Path)
	rec.ModTime = info.ModTime().UnixNano()
	if err := c.appendDoc(rec); err != nil {
		t.Fatal(err)
	}

	// Пока файл не тронут — изменений нет.
	p, err := c.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Changed) != 0 || len(p.Files) != 0 {
		t.Fatalf("нетронутый файл считается изменённым: %+v", p)
	}

	// Правка на месте: тот же размер, новее время.
	if err := os.WriteFile(rec.Path, []byte("строка одна\nстрока два\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Unix(0, rec.ModTime).Add(2 * time.Second)
	if err := os.Chtimes(rec.Path, later, later); err != nil {
		t.Fatal(err)
	}
	p, err = c.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Changed) != 1 || !p.Changed[0].SameSize || len(p.Files) != 0 {
		t.Fatalf("правка на месте должна быть в Changed с SameSize, а не среди новых: %+v", p)
	}
	out := Doctor(c, DoctorOpts{})
	if !strings.Contains(out, "Изменились на диске после индексации: 1") ||
		!strings.Contains(out, "размер тот же") {
		t.Errorf("доктор должен отдельно назвать изменившийся файл и правку на месте:\n%s", out)
	}
	if strings.Contains(out, "в индексе их нет") {
		t.Errorf("изменившийся файл не должен числиться «в индексе их нет»:\n%s", out)
	}
}
