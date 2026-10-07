package kb

import (
	"context"
)

// HashProgress — ход простановки хешей книгам.
type HashProgress struct {
	Done, Total, Set, Skipped, Failed int
	Book                              string
}

// RefreshHashes проставляет книгам хеш содержимого (BookRec.Hash) без
// переиндексации: читается файл, дописывается запись реестра. Книги с хешем
// пропускаются, если не force; пропавшие с диска — считаются несостоявшимися.
//
// Цена — чтение библиотеки целиком (у нас ~10 ГБ, минуты диска), и она
// платится один раз: новые книги получают хеш при индексации.
func (c *Collection) RefreshHashes(ctx context.Context, force bool, onProgress func(HashProgress)) (HashProgress, error) {
	// Под замком коллекции, как индексация: запись дописывается в реестр
	// копией прежней, и побеждает последняя по пути — копия, снятая до того,
	// как параллельная индексация обновила книгу, вернула бы устаревшую.
	if err := c.lock(); err != nil {
		return HashProgress{}, err
	}
	defer c.unlock()
	books := c.Books()
	p := HashProgress{Total: len(books)}
	report := func() {
		if onProgress != nil {
			onProgress(p)
		}
	}
	for _, b := range books {
		if err := ctx.Err(); err != nil {
			return p, err
		}
		p.Done++
		p.Book = bookTitle(b)
		if b.Kind != BookOK || c.isDeleted(b.ID) || (b.Hash != "" && !force) {
			p.Skipped++
			report()
			continue
		}
		h, err := fileHash(b.Path)
		if err != nil {
			p.Failed++
			report()
			continue
		}
		rec := b
		rec.Hash = h
		if err := c.appendDoc(rec); err != nil {
			return p, err
		}
		p.Set++
		report()
	}
	p.Book = ""
	report()
	return p, nil
}
