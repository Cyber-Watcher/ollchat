package kb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Перечитывание книг, которые сами не менялись.
//
// Доливка ориентируется на размер и время файла — и правильно делает, на этом
// держится дешевизна. Но когда меняются **правила разбора**, книга остаётся
// прежней, а её куски устаревают. Так вышло 23.08.2026 с метками предметного
// указателя: разбор научился их выбрасывать, а в собранных кусках они остались.
//
// Что здесь важно знать заранее: перечитанная книга получает **новый номер**,
// а прежний помечается удалённым. Поиск сразу перестаёт выдавать старые куски,
// но граф понятий, если он собран, продолжает на них ссылаться — и такие ссылки
// теряют название книги и страницу. Поэтому книги, попавшие в граф, перечитывают
// вместе с догоном графа, а не отдельно.

// Reindex перечитывает названные книги заново.
//
// paths — файлы или каталоги внутри разрешённых корней. Прежние записи о них
// помечаются удалёнными, затем книги индексируются как новые.
func (c *Collection) Reindex(ctx context.Context, paths []string, opt IndexOpts,
	report func(Progress)) (IndexResult, error) {
	defer c.restamp() // запись своей же коллекции не должна выглядеть чужой

	targets, err := c.matchBooks(paths)
	if err != nil {
		return IndexResult{}, err
	}
	if len(targets) == 0 {
		return IndexResult{}, fmt.Errorf("в коллекции %s нет книг по этим путям", c.name)
	}
	for _, path := range targets {
		if err := c.Forget(path); err != nil {
			return IndexResult{}, err
		}
	}
	opt.Force = true
	return c.Add(ctx, targets, opt, report)
}

// matchBooks находит в коллекции книги по путям: точное совпадение, файл внутри
// названного каталога или совпадение по имени файла.
func (c *Collection) matchBooks(paths []string) ([]string, error) {
	c.mu.RLock()
	docs := append([]BookRec(nil), c.docs...)
	c.mu.RUnlock()

	var out []string
	seen := map[string]bool{}
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		dir := false
		if info, err := os.Stat(abs); err == nil && info.IsDir() {
			dir = true
		}
		for _, d := range docs {
			if seen[d.Path] {
				continue
			}
			switch {
			case d.Path == abs,
				filepath.Base(d.Path) == filepath.Base(p),
				dir && strings.HasPrefix(d.Path, abs+string(filepath.Separator)):
				seen[d.Path] = true
				out = append(out, d.Path)
			}
		}
	}
	return out, nil
}
