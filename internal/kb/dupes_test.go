package kb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCand делает кандидата с заданным размером.
func fakeCand(t *testing.T, dir, name string, content string) candidate {
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
	return candidate{path: p, info: info}
}

// Одна и та же книга в двух каталогах индексируется один раз.
//
// Ровно тот случай, ради которого проверка и заведена: книгу положили в /AI,
// потом скопировали в /DevOps и переименовали. Разбирать обе — значит потратить
// вдвое больше времени видеокарты и получить две одинаковые выдержки в выдаче,
// вытесняющие другие книги.
func TestDedupeSameFileInTwoCatalogs(t *testing.T) {
	dir := t.TempDir()
	a := fakeCand(t, dir, "AI/Grokking.pdf", "содержимое книги")
	b := fakeCand(t, dir, "DevOps/Grokking (1).pdf", "содержимое книги")
	other := fakeCand(t, dir, "AI/Другая.pdf", "совсем другое")

	files, dupes := dedupe([]candidate{a, b, other}, nil, nil)

	if len(files) != 2 {
		t.Errorf("к разбору осталось %d книг, ожидалось 2", len(files))
	}
	if len(dupes) != 1 {
		t.Fatalf("повторов найдено %d, ожидался 1", len(dupes))
	}
	if dupes[0].Indexed {
		t.Error("обе книги новые — это не совпадение с проиндексированной")
	}
	if filepath.Base(dupes[0].Same) != "Grokking.pdf" {
		t.Errorf("совпало с %q, ожидалось с первой по порядку", dupes[0].Same)
	}
}

// Повтор уже проиндексированной книги отличается от повтора среди новых.
//
// Человеку это разные новости: «эта книга у вас уже есть» и «вы положили
// её дважды прямо сейчас».
func TestDedupeAgainstIndexed(t *testing.T) {
	dir := t.TempDir()
	old := fakeCand(t, dir, "AI/Старая.pdf", "одинаковый текст")
	known := []BookRec{{Path: old.path, Size: old.info.Size(), Kind: BookOK}}

	newOne := fakeCand(t, dir, "DevOps/Копия.pdf", "одинаковый текст")
	files, dupes := dedupe([]candidate{newOne}, known, nil)

	if len(files) != 0 {
		t.Errorf("копия проиндексированной книги не должна разбираться: %v", files)
	}
	if len(dupes) != 1 || !dupes[0].Indexed {
		t.Fatalf("ожидалось совпадение с проиндексированной, получено %+v", dupes)
	}
}

// Книги разного размера не сравниваются по содержимому вовсе.
//
// Это не оптимизация ради красоты: иначе доливка четырёх книг заставляла бы
// перечитать всю библиотеку — сотни мегабайт ради ничего.
func TestDedupeSkipsHashingBySize(t *testing.T) {
	dir := t.TempDir()
	newOne := fakeCand(t, dir, "AI/Новая.pdf", "коротко")
	known := []BookRec{
		{Path: filepath.Join(dir, "нет-такого.pdf"), Size: 999999, Kind: BookOK},
	}

	var hashed []string
	hashOf := func(p string) (string, error) {
		hashed = append(hashed, p)
		return fileHash(p)
	}
	files, dupes := dedupe([]candidate{newOne}, known, hashOf)

	if len(files) != 1 || len(dupes) != 0 {
		t.Errorf("книга без совпадений должна пройти: файлов %d, повторов %d", len(files), len(dupes))
	}
	for _, p := range hashed {
		if strings.Contains(p, "нет-такого") {
			t.Error("считан отпечаток книги, чей размер заведомо не совпадает")
		}
	}
}

// Нечитаемый файл не роняет проверку: пусть с ним разбирается индексация.
func TestDedupeToleratesUnreadable(t *testing.T) {
	dir := t.TempDir()
	c := fakeCand(t, dir, "AI/Книга.pdf", "текст")
	if err := os.Remove(c.path); err != nil {
		t.Fatal(err)
	}

	files, dupes := dedupe([]candidate{c}, nil, nil)
	if len(files) != 1 {
		t.Errorf("нечитаемый файл должен пройти дальше, а не пропасть: %v", files)
	}
	if len(dupes) != 0 {
		t.Errorf("нечитаемый файл не повтор: %+v", dupes)
	}
}

// Отчёт называет обе книги и говорит, что именно случилось.
func TestDuplicateReport(t *testing.T) {
	if DuplicateReport(nil) != "" {
		t.Error("без повторов отчёта быть не должно")
	}
	out := DuplicateReport([]Duplicate{
		{Path: "/книги/DevOps/Копия.pdf", Same: "/книги/AI/Книга.pdf", Indexed: true, Size: 5 << 20},
	})
	for _, want := range []string{"Копия.pdf", "Книга.pdf", "уже проиндексирована", "МБ"} {
		if !strings.Contains(out, want) {
			t.Errorf("в отчёте нет %q:\n%s", want, out)
		}
	}
}

// Правка файла без изменения его размера обязана попасть в индекс.
//
// Беда, пойманная 04.09.2026 на `GraphHealth.md`. Отбор повторов считает
// отпечатки уже проиндексированных книг, чей размер совпал с чьим-то из новых,
// — а у переиндексируемого файла запись в индексе своя же, с тем же путём
// и тем же размером. Файл сравнивался сам с собой, объявлялся повтором
// и молча не переиндексировался: ни `--kb-sync`, ни `--kb-reindex` его
// не брали, а поиск продолжал отдавать прежний текст. Размер совпадал
// не по случайности: правка меняла `project_plan.md` на `plan/stage89.md`,
// а это ровно та же длина.
func TestDedupeSameSizeEditIsNotSelfDuplicate(t *testing.T) {
	dir := t.TempDir()
	c := fakeCand(t, dir, "notes/Doc.md", "ссылка на project_plan.md")
	known := []BookRec{{Path: c.path, Size: c.info.Size(), Kind: BookOK}}

	files, dupes := dedupe([]candidate{c}, known, nil)

	if len(files) != 1 {
		t.Errorf("к разбору осталось %d книг, ожидалась 1: файл объявлен повтором самого себя", len(files))
	}
	if len(dupes) != 0 {
		t.Errorf("повторов найдено %d, ожидалось 0", len(dupes))
	}
}

// Настоящий повтор под другим путём по-прежнему ловится.
//
// Проверка того, что заплата выше не выключила саму защиту: копия книги
// в другом каталоге обязана остаться повтором, даже когда первая книга
// в этом же заходе переиндексируется.
func TestDedupeStillCatchesCopyUnderAnotherPath(t *testing.T) {
	dir := t.TempDir()
	orig := fakeCand(t, dir, "AI/Grokking.pdf", "содержимое книги")
	copyOf := fakeCand(t, dir, "DevOps/Grokking (1).pdf", "содержимое книги")
	known := []BookRec{{Path: orig.path, Size: orig.info.Size(), Kind: BookOK}}

	files, dupes := dedupe([]candidate{orig, copyOf}, known, nil)

	if len(files) != 1 {
		t.Errorf("к разбору осталось %d книг, ожидалась 1 (сам файл, но не его копия)", len(files))
	}
	if len(dupes) != 1 {
		t.Fatalf("повторов найдено %d, ожидался 1", len(dupes))
	}
	if filepath.Base(dupes[0].Path) != "Grokking (1).pdf" {
		t.Errorf("повтором объявлен %q, ожидалась копия", dupes[0].Path)
	}
}
