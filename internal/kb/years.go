package kb

import (
	"context"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/document"
)

// Проставление года изданиям, собранным до появления этого поля.
//
// Переиндексировать библиотеку ради одного числа нельзя: это часы работы
// и лишний риск. Но год почти всегда лежит в имени файла — замер на настоящей
// библиотеке дал 94% книг, — а имя у нас уже записано. Поэтому проход идёт
// в два круга: сперва бесплатный по именам, и только для оставшихся книга
// открывается ради копирайта и метаданных.
//
// Файл на диске при этом может уже пропасть — библиотека живая. Пропавшая
// книга просто остаётся без года: это не ошибка прохода.

// YearsProgress — ход проставления годов.
type YearsProgress struct {
	Total   int    // книг в коллекции
	Done    int    // просмотрено
	Named   int    // год взят из имени файла
	Opened  int    // книг пришлось открыть
	Found   int    // год определился
	Missing int    // года нет
	Book    string // что смотрим сейчас
}

// RefreshYears проставляет год книгам, у которых его нет.
//
// force — перечитать год и у тех книг, где он уже стоит (нужен, когда меняются
// правила разбора). onProgress может быть nil.
func (c *Collection) RefreshYears(ctx context.Context, maxBytes int64, force bool,
	onProgress func(YearsProgress)) (YearsProgress, error) {

	books := c.Books()
	p := YearsProgress{Total: len(books)}
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
		if b.Kind != BookOK {
			report()
			continue
		}
		if b.Year > 0 && !force {
			p.Found++
			report()
			continue
		}

		year, src := 0, document.YearNone
		if y := document.YearFromFilename(b.Path); y > 0 && plausible(y) {
			year, src = y, document.YearFromName
		} else {
			// Только здесь платим за чтение файла: пять первых единиц, а не
			// весь документ — копирайт дальше первых страниц не встречается.
			p.Opened++
			if doc, err := document.Probe(b.Path, maxBytes, 5); err == nil {
				year, src = doc.Year, doc.YearSrc
			}
		}

		switch {
		case year > 0:
			p.Found++
			if src == document.YearFromName {
				p.Named++
			}
			rec := b
			rec.Year, rec.YearSrc = year, string(src)
			if err := c.appendDoc(rec); err != nil {
				return p, err
			}
		default:
			p.Missing++
		}
		report()
	}
	p.Book = ""
	report()
	return p, nil
}

// plausible — тот же предел, что и в document: книга следующего года бывает,
// книга через три года — нет.
func plausible(y int) bool { return y >= 1900 && y <= time.Now().Year()+1 }

// YearSpan — самый старый и самый новый год среди книг выдачи и оговорка
// о давности, если она нужна. Пустая оговорка означает, что все книги свежие
// либо годов у них нет.
//
// Оговорка одна на всю выдачу, а не на каждый фрагмент: перечислять возраст
// у каждой цитаты — это шум, а вывод из него один и тот же.
func YearSpan(res []Result, now time.Time) (from, to int, note string) {
	oldest := 0
	for _, r := range res {
		if r.Year <= 0 {
			continue
		}
		if from == 0 || r.Year < from {
			from = r.Year
		}
		if r.Year > to {
			to = r.Year
		}
		if oldest == 0 || r.Year < oldest {
			oldest = r.Year
		}
	}
	if oldest > 0 {
		note = document.YearNote(oldest, now)
	}
	return from, to, note
}
