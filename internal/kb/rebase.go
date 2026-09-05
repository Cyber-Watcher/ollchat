package kb

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Перенос коллекции туда, где книги лежат по другому пути.
//
// **Зачем.** Коллекция переносима: ни поиск, ни смыслы, ни граф файлов книг
// не открывают — текст лежит внутри неё. Но реестр `docs.jsonl` хранит путь
// каждой книги, а корни коллекции — каталоги, откуда книги пришли. На новой
// машине эти пути обычно другие, и тогда:
//
//   - `/kb doctor` объявляет пропавшими все книги, хотя они на месте;
//   - **`--kb-sync` не узнаёт ни одной книги и индексирует библиотеку заново.**
//     Совпадение ищется по точному пути (`collect` строит `known[d.Path]`),
//     поэтому коллекция удвоится, а смыслы к копиям придётся считать часами.
//
// Второе и есть настоящая беда: она тихая и дорогая. Rebase переписывает пути,
// ничего не переиндексируя.
//
// **Что не меняется.** Куски, словесный индекс, векторы и граф. Они путей
// не содержат вовсе, и трогать их незачем: перенос — это правка двух файлов.

// Roots — каталоги, из которых в коллекцию добавляли книги.
//
// Не то же, что `kb.roots` в настройках: там граница разрешённого, здесь
// история того, откуда книги пришли. По корням `--kb-sync` без пути знает,
// что сверять, а `--kb-list` считает разбивку по каталогам.
func (c *Collection) Roots() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]string(nil), c.meta.Roots...)
}

// RebaseResult — что вышло.
type RebaseResult struct {
	Books   int      // книг с переписанным путём
	Roots   int      // корней коллекции с переписанным путём
	Skipped int      // книг, чей путь не начинался со старого корня
	Missing []string // из переписанных не нашлось на диске (первые несколько)
	DryRun  bool
}

// Rebase переписывает старый корень на новый в реестре книг и в паспорте.
//
// Оба пути приводятся к абсолютным и очищаются: «/books/» и «/books» — один
// и тот же корень, и различать их значило бы ловить пользователя на черте.
//
// Сухой прогон обязателен по замыслу вызывающего: правка необратима, а ошибка
// в корне переписала бы пути в чужой каталог молча.
func (c *Collection) Rebase(from, to string, dry bool) (RebaseResult, error) {
	res := RebaseResult{DryRun: dry}

	oldRoot, err := cleanRoot(from)
	if err != nil {
		return res, fmt.Errorf("старый корень: %w", err)
	}
	newRoot, err := cleanRoot(to)
	if err != nil {
		return res, fmt.Errorf("новый корень: %w", err)
	}
	if oldRoot == newRoot {
		return res, fmt.Errorf("старый и новый корень совпадают: %s", oldRoot)
	}

	if err := c.lock(); err != nil {
		return res, err
	}
	defer c.unlock()

	c.mu.Lock()
	docs := append([]BookRec(nil), c.docs...)
	meta := c.meta
	c.mu.Unlock()

	// Пути книг.
	changed := make([]BookRec, len(docs))
	copy(changed, docs)
	for i := range changed {
		np, ok := swapPrefix(changed[i].Path, oldRoot, newRoot)
		if !ok {
			res.Skipped++
			continue
		}
		changed[i].Path = np
		res.Books++
		if _, err := os.Stat(np); err != nil && len(res.Missing) < 5 {
			res.Missing = append(res.Missing, np)
		}
	}

	// Корни коллекции: по ним `--kb-sync` без пути знает, что сверять,
	// а `--kb-list` считает разбивку по каталогам.
	roots := append([]string(nil), meta.Roots...)
	for i, r := range roots {
		if np, ok := swapPrefix(r, oldRoot, newRoot); ok {
			roots[i] = np
			res.Roots++
		}
	}

	if res.Books == 0 && res.Roots == 0 {
		return res, fmt.Errorf("под корнем %s ничего не нашлось; в коллекции корни: %s",
			oldRoot, strings.Join(meta.Roots, ", "))
	}
	if dry {
		return res, nil
	}

	// Реестр переписывается целиком новым файлом и подменяется переименованием:
	// дозапись тут не годится — правится каждая строка, а не хвост. Порядок
	// именно такой, потому что переименование в пределах файловой системы
	// неделимо, и оборваться можно только до него.
	if err := writeDocs(c.dir, changed); err != nil {
		return res, err
	}
	meta.Roots = roots
	if err := writeJSON(filepath.Join(c.dir, "meta.json"), meta); err != nil {
		return res, err
	}

	c.mu.Lock()
	c.docs = changed
	c.byPath = make(map[string]int, len(changed))
	for i, d := range changed {
		c.byPath[d.Path] = i
	}
	c.meta = meta
	c.mu.Unlock()
	c.restamp() // правка своей же коллекции не должна выглядеть чужой
	return res, nil
}

// cleanRoot приводит корень к абсолютному пути без хвостовой черты.
func cleanRoot(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("пусто")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

// swapPrefix меняет начало пути, если оно совпадает с корнем.
//
// Совпадение проверяется **по границе каталога**, а не по подстроке: иначе
// корень `/books` подменил бы начало у `/booksold/…`.
func swapPrefix(path, oldRoot, newRoot string) (string, bool) {
	clean := filepath.Clean(path)
	if clean == oldRoot {
		return newRoot, true
	}
	prefix := oldRoot + string(filepath.Separator)
	if !strings.HasPrefix(clean, prefix) {
		return "", false
	}
	return filepath.Join(newRoot, clean[len(prefix):]), true
}

// writeDocs перезаписывает реестр книг целиком.
func writeDocs(dir string, docs []BookRec) error {
	var b strings.Builder
	for _, d := range docs {
		line, err := json.Marshal(d)
		if err != nil {
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	tmp := filepath.Join(dir, "docs.jsonl.new")
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "docs.jsonl"))
}
