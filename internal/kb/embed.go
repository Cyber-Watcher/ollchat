package kb

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Векторизация коллекции.
//
// Запускается только по команде пользователя и никогда сама. На общем сервере
// это заняло бы чужую видеопамять на десятки минут без спроса; вместо этого
// приложение напоминает, что книги стоят без смыслов.
//
// Работа возобновляема по построению: векторы дописываются в конец, счётчик
// сохраняется после каждой пачки. Прерывание стоит одной пачки, а не всей
// работы, и повторный запуск продолжает с того же места.

// Embedder — то, что умеет превращать тексты в векторы.
//
// Интерфейс намеренно собран на голых типах: пакет kb не должен зависеть
// от клиента Ollama, иначе база знаний окажется привязанной к одному способу
// считать смыслы.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Model() string
}

// EmbedOpts — как считать.
type EmbedOpts struct {
	Batch   int  // сколько кусков в одном запросе; 0 — 64
	Workers int  // сколько пачек слать одновременно; 0 — 2
	Recount bool // пересчитать всё заново, а не только хвост
}

// norm подставляет значения по умолчанию.
//
// Числа не с потолка: на стенде пачка 32 в один поток даёт 16 кусков в секунду,
// а пачка 64 в два потока — 32. Дело не в видеокарте, а в том, что при одной
// пачке за раз половина времени уходит на дорогу до сервера и обратно.
func (o EmbedOpts) norm() EmbedOpts {
	if o.Batch <= 0 {
		o.Batch = 64
	}
	if o.Workers <= 0 {
		o.Workers = 2
	}
	if o.Workers > 8 {
		o.Workers = 8
	}
	return o
}

// EmbedResult — что вышло.
type EmbedResult struct {
	Added     int // сколько кусков получили векторы за эту работу
	Covered   int // сколько покрыто всего
	Total     int // сколько кусков в коллекции
	Dim       int
	Model     string
	Bytes     int64
	Elapsed   time.Duration
	Canceled  bool
	Estimated bool // это оценка, а не настоящая работа
	Batch     int  // с какой пачкой закончили
	Shrunk    bool // пачку пришлось уменьшать
}

// growAfter — через сколько удачных волн пробовать вернуть прежний размер пачки.
const growAfter = 20

// shrink уменьшает пачку вдвое.
//
// Сбой векторизации почти всегда означает не «запрос неверен», а «серверу сейчас
// тяжело». Замерено на живом стенде: пока видеопамять свободна, проходят пачки
// по 64 куска; стоит кому-то загрузить рядом модель на 82 ГБ — обработчик
// падает уже на 16, а на 8 работает. Предел зависит не от нас, а от того, кто
// ещё сейчас на сервере, поэтому его нельзя записать числом в настройки:
// его можно только нащупать на ходу.
//
// Возвращает false, когда уменьшать больше некуда — тогда сбой настоящий.
func shrink(o EmbedOpts) (EmbedOpts, bool) {
	switch {
	case o.Batch > 1:
		o.Batch /= 2
	case o.Workers > 1:
		// Пачка уже минимальна: остаётся снизить поток.
		o.Workers = 1
	default:
		return o, false
	}
	return o, true
}

// grow возвращает пачку к заданному размеру, когда сервер снова справляется.
func grow(cur, want EmbedOpts) EmbedOpts {
	if cur.Workers < want.Workers {
		cur.Workers++
		return cur
	}
	if cur.Batch < want.Batch {
		cur.Batch *= 2
		if cur.Batch > want.Batch {
			cur.Batch = want.Batch
		}
	}
	return cur
}

// tailErr оставляет от сообщения сервера хвост: причина сбоя в нём, а начало
// занято обёртками вида «сервер вернул 400 Bad Request».
func tailErr(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i > 0 && i < len(s)-2 {
		s = strings.TrimSpace(s[i+2:])
	}
	if r := []rune(s); len(r) > 50 {
		s = string(r[:49]) + "…"
	}
	return s
}

// Coverage — сколько кусков коллекции обеспечено смыслами.
type Coverage struct {
	Covered int
	Total   int
	Model   string
	Dim     int
	Usable  bool // годятся ли векторы для нынешней модели
}

// Percent — покрытие в процентах.
func (c Coverage) Percent() int {
	if c.Total == 0 {
		return 0
	}
	return c.Covered * 100 / c.Total
}

// Full сообщает, что смыслы посчитаны для всей коллекции.
func (c Coverage) Full() bool { return c.Total > 0 && c.Covered >= c.Total }

// Coverage возвращает состояние векторов относительно модели model.
func (c *Collection) Coverage(model string) Coverage {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.coverage(model)
}

func (c *Collection) coverage(model string) Coverage {
	cov := Coverage{}
	if c.store != nil {
		cov.Total = c.store.Count()
	}
	if c.vectors == nil {
		return cov
	}
	m := c.vectors.Meta()
	cov.Covered = m.Count
	cov.Model = m.Model
	cov.Dim = m.Dim
	cov.Usable = m.Compatible(model, 0)
	return cov
}

// Embed считает недостающие векторы коллекции.
func (c *Collection) Embed(ctx context.Context, emb Embedder, opt EmbedOpts, report func(Progress)) (res EmbedResult, err error) {
	if emb == nil {
		return res, errors.New("не задана модель эмбеддингов: укажите kb.embed_model в настройках")
	}
	start := time.Now()
	if err := c.lock(); err != nil {
		return res, err
	}
	defer c.unlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.store == nil || c.store.Count() == 0 {
		return res, fmt.Errorf("коллекция %q пуста — сначала добавьте книги", c.name)
	}
	opt = opt.norm()
	res.Total = c.store.Count()
	res.Model = emb.Model()

	from := 0
	if !opt.Recount && c.vectors != nil && c.vectors.Meta().Compatible(emb.Model(), 0) {
		from = c.vectors.Count()
	}
	if from >= res.Total {
		res.Covered = from
		res.Dim = c.vectors.Dim()
		res.Bytes = vecBytes(c.dir)
		res.Elapsed = time.Since(start)
		return res, nil
	}

	// Размерность узнаём у сервера, а не берём из таблицы: она зависит от того,
	// какой именно тег модели вытянут, и ошибка испортила бы весь файл.
	probe, err := emb.Embed(ctx, []string{"размерность"})
	if err != nil {
		return res, err
	}
	dim := len(probe[0])
	if dim <= 0 {
		return res, errors.New("модель вернула вектор нулевой длины")
	}
	res.Dim = dim
	// Несовместимые прежние векторы дописывать нельзя — только заменять.
	if c.vectors != nil && !c.vectors.Meta().Compatible(emb.Model(), dim) {
		from = 0
	}

	w, err := CreateVecWriter(c.dir, emb.Model(), dim, from)
	if err != nil {
		return res, err
	}
	defer w.Close()
	// Открытые векторы больше не соответствуют файлу, который мы правим.
	c.vectors = nil

	th := throttle(report)
	// Размер пачки подстраивается на ходу — см. комментарий к shrink.
	cur, good := opt, 0
	for i := from; i < res.Total; {
		if ctx.Err() != nil {
			res.Canceled = true
			break
		}
		end := i + cur.Batch*cur.Workers
		if end > res.Total {
			end = res.Total
		}
		vecs, err := c.embedWave(ctx, emb, i, end, cur)
		if err != nil {
			if ctx.Err() != nil {
				res.Canceled = true
				break
			}
			if smaller, ok := shrink(cur); ok {
				cur, good = smaller, 0
				res.Shrunk = true
				th(Progress{Phase: fmt.Sprintf("смыслы (пачка уменьшена до %d: %s)",
					cur.Batch, tailErr(err)), Collection: c.name,
					DocsDone: i, DocsTotal: res.Total, Chunks: int64(w.Added())})
				continue
			}
			// То, что успели посчитать, сохраняем: работа возобновляема,
			// и терять час из-за одной пачки незачем.
			if _, cerr := w.Commit(); cerr != nil {
				return res, cerr
			}
			return res, err
		}
		if err := w.Append(vecs); err != nil {
			return res, err
		}
		// Счётчик сохраняем после каждой волны: цена обрыва — одна волна.
		if _, err := w.Commit(); err != nil {
			return res, err
		}
		i = end
		if good++; good >= growAfter {
			cur, good = grow(cur, opt), 0
		}
		th(Progress{Phase: "смыслы", Collection: c.name,
			DocsDone: end, DocsTotal: res.Total, Chunks: int64(w.Added())})
	}
	res.Batch = cur.Batch

	meta, err := w.Commit()
	if err != nil {
		return res, err
	}
	res.Added = w.Added()
	res.Covered = meta.Count
	res.Bytes = vecBytes(c.dir)
	res.Elapsed = time.Since(start)

	if c.vectors, err = OpenVectors(c.dir); err != nil {
		return res, err
	}
	return res, nil
}

// embedWave векторизует куски [from, to) несколькими пачками сразу.
//
// Порядок возврата строго по номерам кусков: векторы кладутся в файл по адресу,
// равному номеру, и перепутать их местами означало бы испортить всю коллекцию.
// Поэтому пачки считаются одновременно, а склеиваются по порядку.
func (c *Collection) embedWave(ctx context.Context, emb Embedder, from, to int, opt EmbedOpts) ([][]float32, error) {
	type part struct {
		vecs [][]float32
		err  error
	}
	var bounds [][2]int
	for i := from; i < to; i += opt.Batch {
		end := i + opt.Batch
		if end > to {
			end = to
		}
		bounds = append(bounds, [2]int{i, end})
	}
	parts := make([]part, len(bounds))
	var wg sync.WaitGroup
	for k, b := range bounds {
		texts, err := c.embedTexts(b[0], b[1])
		if err != nil {
			return nil, err
		}
		wg.Add(1)
		go func(k int, texts []string) {
			defer wg.Done()
			vecs, err := emb.Embed(ctx, texts)
			parts[k] = part{vecs: vecs, err: err}
		}(k, texts)
	}
	wg.Wait()

	out := make([][]float32, 0, to-from)
	for k, p := range parts {
		if p.err != nil {
			return nil, p.err
		}
		if len(p.vecs) != bounds[k][1]-bounds[k][0] {
			return nil, fmt.Errorf("пачка %d вернула %d векторов на %d кусков",
				k, len(p.vecs), bounds[k][1]-bounds[k][0])
		}
		out = append(out, p.vecs...)
	}
	return out, nil
}

// embedTexts готовит тексты кусков [from, to) к векторизации.
//
// Векторизуется не голый кусок, а кусок с шапкой «книга · страница»: фрагмент
// из середины главы часто не содержит самого предмета, и без шапки его смысл
// беднее, чем на самом деле.
func (c *Collection) embedTexts(from, to int) ([]string, error) {
	ids := make([]int, 0, to-from)
	for i := from; i < to; i++ {
		ids = append(ids, i)
	}
	texts, err := c.store.Texts(ids)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		rec := c.store.Rec(id)
		var b strings.Builder
		if book, ok := c.bookByID(rec.Doc); ok {
			b.WriteString(bookTitle(book))
			if rec.UnitFrom > 0 {
				fmt.Fprintf(&b, " · с. %d", rec.UnitFrom)
			}
			b.WriteString("\n")
		}
		b.WriteString(texts[id])
		out = append(out, b.String())
	}
	return out, nil
}

// bookByID ищет книгу по её номеру, не беря замок: вызывается под ним.
func (c *Collection) bookByID(id uint32) (BookRec, bool) {
	for _, d := range c.docs {
		if d.ID == id {
			return d, true
		}
	}
	return BookRec{}, false
}

// EstimateEmbed меряет скорость на небольшой пробе и оценивает всю работу.
//
// Придумывать производительность чужого сервера нельзя: она зависит от модели,
// видеокарты и от того, кто ещё сейчас на нём работает. Поэтому оценка —
// это замер, а не таблица.
func (c *Collection) EstimateEmbed(ctx context.Context, emb Embedder, opt EmbedOpts, sample int) (EmbedResult, error) {
	var res EmbedResult
	if emb == nil {
		return res, errors.New("не задана модель эмбеддингов")
	}
	c.mu.RLock()
	total := 0
	if c.store != nil {
		total = c.store.Count()
	}
	from := 0
	if !opt.Recount && c.vectors != nil && c.vectors.Meta().Compatible(emb.Model(), 0) {
		from = c.vectors.Count()
	}
	c.mu.RUnlock()

	res.Total = total
	res.Covered = from
	res.Model = emb.Model()
	res.Estimated = true
	left := total - from
	if left <= 0 {
		return res, nil
	}
	opt = opt.norm()
	if sample <= 0 {
		sample = opt.Batch * opt.Workers
	}
	if sample > left {
		sample = left
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	// Меряем ровно тем же способом, каким пойдёт работа: иначе оценка врёт.
	// Первая волна прогревает модель на сервере, поэтому её не считаем.
	if _, err := c.embedWave(ctx, emb, from, min(from+opt.Batch, from+sample), opt); err != nil {
		return res, err
	}
	start := time.Now()
	done := 0
	for i := from; i < from+sample; i += opt.Batch * opt.Workers {
		end := i + opt.Batch*opt.Workers
		if end > from+sample {
			end = from + sample
		}
		vecs, err := c.embedWave(ctx, emb, i, end, opt)
		if err != nil {
			return res, err
		}
		if res.Dim == 0 && len(vecs) > 0 {
			res.Dim = len(vecs[0])
		}
		done += end - i
	}
	spent := time.Since(start)
	if done > 0 {
		res.Elapsed = time.Duration(float64(spent) / float64(done) * float64(left))
	}
	res.Added = left
	res.Bytes = int64(left) * int64(res.Dim)
	return res, nil
}

// bookTitle выбирает, чем называть книгу: заголовком, если он есть,
// иначе именем файла.
func bookTitle(b BookRec) string {
	if t := strings.TrimSpace(b.Title); t != "" {
		return t
	}
	return filepath.Base(b.Path)
}
