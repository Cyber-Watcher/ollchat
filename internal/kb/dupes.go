package kb

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Один и тот же файл, разложенный по разным каталогам.
//
// **Почему это бывает у всех.** Книгу кладут в `/AI`, потом она же пригождается
// в `/DevOps` — и её копируют, а не ссылаются. Через месяц никто не помнит,
// что она лежит дважды. Индексация покорно разбирает обе копии, и дальше:
//
//   - место на диске и время видеокарты тратятся вдвое;
//   - **выдача портится**: две одинаковые выдержки занимают два места из восьми,
//     вытесняя другие книги. Ограничение «не больше трёх кусков из одной книги»
//     тут не спасает — книги-то формально разные;
//   - граф считает понятие встреченным в двух книгах вместо одной, и вес связи
//     оказывается завышенным.
//
// **Как ищется.** По содержимому, а не по имени: копия почти всегда переименована
// («Book (1).pdf»), а время правки сбивается самим копированием. Сравнивается
// хеш файла целиком.
//
// **Почему не хеш текста.** Он поймал бы и одну книгу в разных форматах — PDF
// и EPUB, — но требует разбора обеих, то есть той самой работы, которую мы
// и хотим не делать. Одинаковые файлы — случай подавляющий и ловится даром.

// Duplicate — книга, совпавшая с другой по содержимому.
type Duplicate struct {
	Path string // что не стали индексировать
	Same string // с чем совпало
	// Indexed — совпало с уже проиндексированной книгой (иначе — обе новые,
	// и в этом заходе взята первая).
	Indexed bool
	Size    int64
}

// fileHash считает отпечаток содержимого.
//
// Читается файл целиком: у книги это десятки мегабайт и доли секунды, а
// частичный хеш (начало плюс конец) на PDF ненадёжен — у них совпадают
// и заголовок, и хвостовая таблица объектов.
func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// dedupe убирает из списка кандидатов повторы по содержимому.
//
// Возвращает очищенный список и рассказ о том, что убрано: молча пропустить
// книгу нельзя — человек положил её намеренно и должен узнать, почему её нет
// в выдаче.
//
// Ошибка чтения файла здесь не фатальна: пусть его разбирает обычный путь
// индексации, он сообщит о беде понятнее.
func dedupe(files []candidate, known []BookRec, hashOf func(string) (string, error)) ([]candidate, []Duplicate) {
	if len(files) == 0 {
		return files, nil
	}
	if hashOf == nil {
		hashOf = fileHash
	}

	// Отпечатки уже проиндексированных книг считаются только для тех, чей
	// размер совпал с чьим-то из новых: перечитывать всю библиотеку ради
	// четырёх добавленных книг незачем.
	sizes := map[int64]bool{}
	self := map[string]bool{} // пути кандидатов: их записи в индексе не в счёт
	for _, f := range files {
		sizes[f.info.Size()] = true
		self[filepath.Clean(f.path)] = true
	}
	seen := map[string]string{} // хеш → путь
	for _, rec := range known {
		if rec.Kind != BookOK || !sizes[rec.Size] {
			continue
		}
		// Файл, который переиндексируют, лежит в индексе под тем же путём.
		// Считать его отпечаток значит сравнить файл сам с собой: совпадение
		// будет всегда, и правка никогда не попадёт в индекс. Цена ошибки —
		// молчаливая: поиск продолжает отдавать прежний текст. Поймано
		// 04.09.2026 на GraphHealth.md, где правка не изменила размер
		// файла, и потому запись в индексе прошла отбор по размеру.
		if self[filepath.Clean(rec.Path)] {
			continue
		}
		if h, err := hashOf(rec.Path); err == nil {
			if _, dup := seen[h]; !dup {
				seen[h] = rec.Path
			}
		}
	}
	indexed := map[string]bool{}
	for h := range seen {
		indexed[h] = true
	}

	out := make([]candidate, 0, len(files))
	var dupes []Duplicate
	for _, f := range files {
		h, err := hashOf(f.path)
		if err != nil {
			out = append(out, f) // пусть разбирается обычным путём
			continue
		}
		if first, ok := seen[h]; ok {
			dupes = append(dupes, Duplicate{
				Path: f.path, Same: first, Indexed: indexed[h], Size: f.info.Size(),
			})
			continue
		}
		seen[h] = f.path
		out = append(out, f)
	}
	return out, dupes
}

// DuplicateReport — что сказать человеку про найденные повторы.
//
// Отдельной функцией, потому что печатают их двое: командная строка и лента
// диалога. Два текста об одном разошлись бы в первой же правке.
func DuplicateReport(dupes []Duplicate) string {
	if len(dupes) == 0 {
		return ""
	}
	sort.Slice(dupes, func(i, j int) bool { return dupes[i].Path < dupes[j].Path })

	var b strings.Builder
	fmt.Fprintf(&b, "пропущено повторов: %d — те же файлы уже есть в коллекции\n", len(dupes))
	const show = 20
	var saved int64
	for i, d := range dupes {
		saved += d.Size
		if i >= show && len(dupes) > show+3 {
			continue
		}
		what := "то же, что новая"
		if d.Indexed {
			what = "уже проиндексирована"
		}
		fmt.Fprintf(&b, "  ~ %s\n      %s: %s\n",
			filepath.Base(d.Path), what, filepath.Base(d.Same))
	}
	if len(dupes) > show+3 {
		fmt.Fprintf(&b, "  … и ещё %d\n", len(dupes)-show)
	}
	fmt.Fprintf(&b, "  сэкономлено разбора: %.1f МБ\n", float64(saved)/1e6)
	return b.String()
}
