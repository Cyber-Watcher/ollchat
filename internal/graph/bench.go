package graph

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// Сравнение моделей извлечения.
//
// **Зачем в самой программе, а не разовым черновиком.** Модели на стенде
// меняются, и выбор между ними решает качество всего графа: одна и та же
// библиотека, разобранная разными моделями, даёт разные понятия и разные связи.
// Пересобирать граф стоит суток, поэтому ошибка выбора обходится дороже всего
// остального. Сравнение должно быть под рукой и давать одни и те же числа
// на одних и тех же кусках.
//
// **Настоящий граф не трогается.** Каждый прогон пишет в свой пустой граф
// во временном каталоге, который потом удаляется. Иначе замер портил бы то,
// что меряет: понятия одной модели попадали бы в граф, собранный другой.
//
// **Что меряется, кроме скорости.** Замер 24.08.2026 показал, что скорость —
// не главное: `qwen3.8` на 20% медленнее `glm-4.7-flash`, но даёт русские
// синонимы у половины английских понятий против трёх процентов у glm, а именно
// синонимы связывают русский вопрос с английской книгой. Поэтому в отчёте
// рядом со скоростью стоят: сколько понятий на кусок, у скольких есть синонимы,
// сколько среди синонимов русских и сколько ответов вообще не разобралось.
// Модель, которая вдвое быстрее, но каждый пятый ответ отдаёт неразбираемым,
// на деле медленнее.

// BenchResult — итог одной модели.
type BenchResult struct {
	Model string

	Chunks   int           // сколько кусков разобрано
	Elapsed  time.Duration //
	Entities int
	Edges    int

	Empty   int // кусков без единого понятия
	Skipped int // ответов, которые не разобрались

	WithAlias   int // понятий, у которых есть хоть один синоним
	RussianName int // понятий с русским именем
	RussianOnly int // английских понятий, у которых синоним по-русски

	Err error
}

// Rate — кусков в секунду.
func (r BenchResult) Rate() float64 {
	if r.Elapsed <= 0 {
		return 0
	}
	return float64(r.Chunks) / r.Elapsed.Seconds()
}

// PerChunk — понятий на кусок: густота извлечения.
func (r BenchResult) PerChunk() float64 {
	if r.Chunks == 0 {
		return 0
	}
	return float64(r.Entities) / float64(r.Chunks)
}

// AliasShare — доля понятий, у которых есть синоним.
func (r BenchResult) AliasShare() float64 {
	if r.Entities == 0 {
		return 0
	}
	return float64(r.WithAlias) / float64(r.Entities)
}

// BridgeShare — доля английских понятий с русским синонимом.
//
// Это и есть мост от русского вопроса к английской книге, ради которого
// выбиралась модель извлечения. Считается от английских понятий, а не от всех:
// у русского понятия русский синоним моста не строит.
func (r BenchResult) BridgeShare() float64 {
	english := r.Entities - r.RussianName
	if english <= 0 {
		return 0
	}
	return float64(r.RussianOnly) / float64(english)
}

// BenchOpts — как сравнивать.
type BenchOpts struct {
	Folder string // каталог книг, откуда брать куски
	Limit  int    // сколько кусков на модель; 0 — 50

	// Пробу размазывать по книгам нечем: сборка берёт куски подряд, и первые
	// из них — титул, оглавление, колофон. Отсюда правило пользования: брать
	// каталог с несколькими книгами и не меньше полусотни кусков, иначе замер
	// сравнит модели на служебных страницах. Черновик умел пропускать начало
	// книги и брать каждый N-й кусок; здесь этого нет, потому что в BuildOpts
	// такого отбора нет, а выдумывать второй путь разбора ради замера — значит
	// мерить не то, что работает.
	Workers int
	Retry   bool
	NumCtx  int
	Keep    string // куда сложить временные графы; пусто — удалить
}

func (o BenchOpts) norm() BenchOpts {
	if o.Limit <= 0 {
		o.Limit = 50
	}
	if o.Workers <= 0 {
		o.Workers = 4
	}
	return o
}

// BenchModel прогоняет одну модель по кускам и считает итог.
//
// Граф создаётся во временном каталоге и удаляется, если не задано Keep.
// Ошибка удаления не ошибка замера: числа уже получены.
func BenchModel(ctx context.Context, coll Source, ex Extractor, o BenchOpts,
	onProgress func(BuildProgress)) (BenchResult, error) {

	o = o.norm()
	res := BenchResult{Model: ex.Model()}

	dir, err := os.MkdirTemp("", "ollchat-bench-")
	if err != nil {
		return res, err
	}
	if o.Keep == "" {
		defer os.RemoveAll(dir)
	} else {
		defer func() {
			_ = os.MkdirAll(o.Keep, 0o755)
			_ = os.Rename(dir, filepath.Join(o.Keep, safeName(ex.Model())))
		}()
	}

	g, err := Create(dir, "bench", coll.ChunkCount(), Rules{})
	if err != nil {
		return res, err
	}
	defer g.Close()

	started := time.Now()
	br, err := Build(ctx, coll, g, ex, BuildOpts{
		Folder:  o.Folder,
		Limit:   o.Limit,
		Workers: o.Workers,
		Retry:   o.Retry,
	}, onProgress)
	res.Elapsed = time.Since(started)
	if err != nil {
		res.Err = err
		return res, err
	}
	res.Chunks = br.Done + br.Empty + br.Skipped
	res.Empty, res.Skipped = br.Empty, br.Skipped
	res.Entities, res.Edges = g.Entities().Count(), g.Edges().Count()

	for _, e := range g.Entities().All() {
		nameRu := hasCyrillic(e.Name)
		if nameRu {
			res.RussianName++
		}
		if len(e.Aliases) == 0 {
			continue
		}
		res.WithAlias++
		if nameRu {
			continue // мост нужен от английского имени к русскому, не наоборот
		}
		for _, a := range e.Aliases {
			if hasCyrillic(a) {
				res.RussianOnly++
				break
			}
		}
	}
	return res, nil
}

// hasCyrillic — есть ли в строке кириллица.
func hasCyrillic(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

// safeName делает из имени модели имя каталога: двоеточия и слеши в путях
// живут плохо.
func safeName(model string) string {
	r := strings.NewReplacer("/", "-", ":", "-", " ", "-")
	return r.Replace(model)
}
