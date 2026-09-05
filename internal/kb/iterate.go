package kb

import (
	"strings"
)

// Обход кусков коллекции.
//
// Понадобился графу понятий (internal/graph): он проходит библиотеку кусок
// за куском и спрашивает у модели, какие сущности в нём названы. Поиску такой
// обход не нужен — он ходит по индексу, — поэтому раньше его и не было.
//
// Главное здесь — какие номера отдаются наружу. Сквозной номер куска внутри
// файла меняется при уплотнении (`/kb merge` переписывает хранилище), а пара
// «книга + порядковый номер куска внутри книги» его переживает: на ней держатся
// внешние ссылки вида «go/12#37». Поэтому обход отдаёт **и то и другое**,
// и всё, что хочет пережить уплотнение, обязано хранить пару.

// ChunkInfo — кусок коллекции для внешнего обхода.
type ChunkInfo struct {
	Index int // сквозной номер в хранилище: меняется при уплотнении

	Doc uint32 // номер книги в реестре коллекции
	Ord uint32 // номер куска внутри книги — вместе с Doc устойчивая ссылка

	UnitFrom int    // страница или раздел, где кусок начинается
	UnitTo   int    // где заканчивается
	Unit     string // «стр.» или «разд.»

	Book BookRec // книга целиком: заголовок, автор, путь
	Text string  // текст куска
	Code bool    // кусок похож на листинг
}

// ChunkFilter — какие куски нужны.
type ChunkFilter struct {
	// Docs — только эти книги. Пусто — все.
	Docs []uint32

	// PathContains — только книги, в пути которых есть эта строка.
	// Так выбирается каталог библиотеки: «/AI/» — книги по искусственному
	// интеллекту, и граф собирается по одной теме за раз.
	PathContains string

	// From и Limit — окно обхода по сквозному номеру. Нужны калибровке:
	// «прогони первые двести кусков и замерь».
	From  int
	Limit int
}

// ChunkCount возвращает число кусков в коллекции.
func (c *Collection) ChunkCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.store == nil {
		return 0
	}
	return c.store.Count()
}

// MatchingDocs возвращает книги, подходящие под отбор: только читаемые
// и не помеченные удалёнными.
func (c *Collection) MatchingDocs(f ChunkFilter) []BookRec {
	c.mu.RLock()
	defer c.mu.RUnlock()
	want := make(map[uint32]bool, len(f.Docs))
	for _, id := range f.Docs {
		want[id] = true
	}
	var out []BookRec
	for _, d := range c.docs {
		if c.deleted[d.ID] || d.Kind != BookOK {
			continue
		}
		if len(want) > 0 && !want[d.ID] {
			continue
		}
		if f.PathContains != "" && !strings.Contains(d.Path, f.PathContains) {
			continue
		}
		out = append(out, d)
	}
	return out
}

// CountChunks считает куски, подходящие под отбор. Нужен до начала работы:
// «граф по каталогу AI — это 22 156 кусков» лучше сказать заранее, чем через
// три часа.
func (c *Collection) CountChunks(f ChunkFilter) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.store == nil {
		return 0
	}
	ok := c.docFilter(f)
	var n int
	for i := 0; i < c.store.Count(); i++ {
		if i < f.From {
			continue
		}
		if ok(c.store.Rec(i).Doc) {
			n++
			if f.Limit > 0 && n >= f.Limit {
				break
			}
		}
	}
	return n
}

// docFilter собирает проверку «годится ли книга» один раз на весь обход.
func (c *Collection) docFilter(f ChunkFilter) func(uint32) bool {
	want := make(map[uint32]bool, len(f.Docs))
	for _, id := range f.Docs {
		want[id] = true
	}
	return func(doc uint32) bool {
		if c.deleted[doc] {
			return false
		}
		if len(want) > 0 && !want[doc] {
			return false
		}
		if f.PathContains != "" {
			b, ok := c.book(doc)
			if !ok || !strings.Contains(b.Path, f.PathContains) {
				return false
			}
		}
		return true
	}
}

// book ищет книгу без блокировки — вызывается изнутри уже занятого замка.
func (c *Collection) book(id uint32) (BookRec, bool) {
	for _, d := range c.docs {
		if d.ID == id {
			return d, true
		}
	}
	return BookRec{}, false
}

// ChunkRef — кусок без текста: только откуда он.
//
// Нужен обходу, которому текст пока не нужен: собрать список работы на сутки
// вперёд. Тексты всей библиотеки — это триста мегабайт, и держать их в памяти
// ради одного лишь пересчёта остатка незачем.
type ChunkRef struct {
	Index    int
	Doc      uint32
	Ord      uint32
	UnitFrom int
	UnitTo   int
	Unit     string
	Book     BookRec
}

// EachChunkRef обходит куски, не читая их тексты.
func (c *Collection) EachChunkRef(f ChunkFilter, fn func(ChunkRef) error) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.store == nil {
		return nil
	}
	ok := c.docFilter(f)
	var seen int
	for i := 0; i < c.store.Count(); i++ {
		if i < f.From {
			continue
		}
		rec := c.store.Rec(i)
		if !ok(rec.Doc) {
			continue
		}
		ref := ChunkRef{
			Index: i, Doc: rec.Doc, Ord: rec.Ord,
			UnitFrom: int(rec.UnitFrom), UnitTo: int(rec.UnitTo), Unit: "стр.",
		}
		if b, found := c.book(rec.Doc); found {
			ref.Book = b
			if b.UnitWord == "разделов" {
				ref.Unit = "разд."
			}
		}
		if err := fn(ref); err != nil {
			return err
		}
		seen++
		if f.Limit > 0 && seen >= f.Limit {
			return nil
		}
	}
	return nil
}

// ChunkByRef находит кусок по устойчивой ссылке «книга + номер внутри книги».
//
// Отдельный указатель, а не обход: граф понятий хранит именно такие ссылки,
// и на каждый ответ их приходится разрешать десятками. Перебор по всей
// коллекции стоил бы 268 тысяч сравнений на каждую, поэтому строится
// отображение — один раз при первом обращении. Память: по шестнадцать байт
// на кусок, на всю библиотеку это единицы мегабайт.
func (c *Collection) ChunkByRef(doc, ord uint32) (ChunkInfo, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.store == nil {
		return ChunkInfo{}, false
	}
	c.refOnce.Do(c.buildRefIndex)
	i, ok := c.byRef[uint64(doc)<<32|uint64(ord)]
	if !ok {
		return ChunkInfo{}, false
	}
	texts, err := c.store.Texts([]int{i})
	if err != nil {
		return ChunkInfo{}, false
	}
	rec := c.store.Rec(i)
	info := ChunkInfo{
		Index: i, Doc: rec.Doc, Ord: rec.Ord,
		UnitFrom: int(rec.UnitFrom), UnitTo: int(rec.UnitTo), Unit: "стр.",
		Text: texts[i], Code: ChunkFlags(rec.Flags)&FlagCode != 0,
	}
	if b, found := c.book(rec.Doc); found {
		info.Book = b
		if b.UnitWord == "разделов" {
			info.Unit = "разд."
		}
	}
	return info, true
}

// buildRefIndex собирает отображение «книга и номер куска → сквозной номер».
// Зовётся под уже взятым замком чтения.
func (c *Collection) buildRefIndex() {
	c.byRef = make(map[uint64]int, c.store.Count())
	for i := 0; i < c.store.Count(); i++ {
		rec := c.store.Rec(i)
		c.byRef[uint64(rec.Doc)<<32|uint64(rec.Ord)] = i
	}
}

// ChunkTexts читает тексты кусков по их сквозным номерам.
//
// Пачкой, а не по одному: куски лежат сжатыми блоками по 64 штуки, и чтение
// по одному разжимало бы один и тот же блок десятки раз.
func (c *Collection) ChunkTexts(indexes []int) (map[int]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.store == nil || len(indexes) == 0 {
		return map[int]string{}, nil
	}
	return c.store.Texts(indexes)
}

// eachChunkBatch — по скольку кусков читать текст за раз. Тексты лежат
// блоками по 64 куска, поэтому чтение пачкой обходится в одно разжатие блока
// вместо десятков.
const eachChunkBatch = 256

// EachChunk обходит куски коллекции, подходящие под отбор.
//
// Тексты читаются пачками: хранилище держит куски сжатыми блоками, и чтение
// по одному разжимало бы один блок по многу раз. Ошибка обработчика
// останавливает обход и возвращается наружу — так вызывающий код может
// прекратить долгую работу, не выдумывая для этого отдельного способа.
func (c *Collection) EachChunk(f ChunkFilter, fn func(ChunkInfo) error) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.store == nil {
		return nil
	}
	ok := c.docFilter(f)

	var seen int
	pending := make([]int, 0, eachChunkBatch)

	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		texts, err := c.store.Texts(pending)
		if err != nil {
			return err
		}
		for _, i := range pending {
			rec := c.store.Rec(i)
			info := ChunkInfo{
				Index:    i,
				Doc:      rec.Doc,
				Ord:      rec.Ord,
				UnitFrom: int(rec.UnitFrom),
				UnitTo:   int(rec.UnitTo),
				Unit:     "стр.",
				Text:     texts[i],
				Code:     ChunkFlags(rec.Flags)&FlagCode != 0,
			}
			if b, found := c.book(rec.Doc); found {
				info.Book = b
				// У книг единица измерения разная: у PDF страницы, у EPUB
				// разделы. Ссылка «с. 40» на раздел сбивает с толку.
				if b.UnitWord == "разделов" {
					info.Unit = "разд."
				}
			}
			if err := fn(info); err != nil {
				return err
			}
		}
		pending = pending[:0]
		return nil
	}

	for i := 0; i < c.store.Count(); i++ {
		if i < f.From {
			continue
		}
		if !ok(c.store.Rec(i).Doc) {
			continue
		}
		pending = append(pending, i)
		seen++
		if len(pending) >= eachChunkBatch {
			if err := flush(); err != nil {
				return err
			}
		}
		if f.Limit > 0 && seen >= f.Limit {
			break
		}
	}
	return flush()
}
