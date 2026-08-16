package kb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/document"
)

// Индексация: от папки с книгами до готового сегмента.
//
// Работа идёт двумя фазами с разной ценой прерывания:
//
//	извлечение — дорогая: чтение и разбор книг. После каждой книги коммит
//	             и строка в журнал, поэтому прерывание стоит одной книги.
//	сегмент    — дешёвая: читает уже извлечённые тексты. Прерывание стоит
//	             всего сегмента, но он строится минуты и без чтения диска.
//
// Из этого разделения бесплатно следует главное: смена правил разбора требует
// пересборки только сегментов, а не повторного чтения библиотеки.

// IndexOpts — настройки индексации.
type IndexOpts struct {
	Workers   int   // сколько книг разбирать разом; 0 — по числу ядер
	MaxBytes  int64 // предел размера книги
	BookLimit int   // предел числа книг за раз; 0 — без предела
}

// Progress — сообщение о ходе работы.
type Progress struct {
	Phase      string // «обход», «извлечение», «индекс»
	Collection string
	DocsDone   int
	DocsTotal  int
	Chunks     int64
	Current    string // имя книги, которая разбирается сейчас
	Added      int
	Skipped    int
	Scans      int
	Errors     int
	Elapsed    time.Duration
	Done       bool
	Canceled   bool
	Err        error
}

// IndexResult — итог индексации.
type IndexResult struct {
	Added    int
	Skipped  int
	Scans    int
	Errors   int
	Removed  int
	Chunks   int64
	Elapsed  time.Duration
	Canceled bool
}

// progressEvery задаёт, как часто идут сообщения о ходе работы. Чаще незачем:
// на тысячах книг поток сообщений перегружает цикл событий интерфейса, и лента
// начинает дёргаться.
const progressEvery = 300 * time.Millisecond

// Add добавляет книги из указанных путей и строит по ним сегмент.
//
// Уже проиндексированные книги пропускаются: сверка идёт по паре «размер
// и время изменения», без чтения файла. Поэтому повторный вызов на той же папке
// стоит секунды и нужен ровно для доливки.
func (c *Collection) Add(ctx context.Context, paths []string, opt IndexOpts, report func(Progress)) (IndexResult, error) {
	if err := c.lock(); err != nil {
		return IndexResult{}, err
	}
	defer c.unlock()

	start := time.Now()
	send := throttle(report)

	send(Progress{Phase: "обход", Collection: c.name})
	files, err := c.collect(paths, opt)
	if err != nil {
		return IndexResult{}, err
	}
	if len(files) == 0 {
		return IndexResult{Elapsed: time.Since(start)}, nil
	}

	res, err := c.extract(ctx, files, opt, start, send)
	if err != nil {
		return res, err
	}
	if res.Added == 0 || ctx.Err() != nil {
		// После отмены сегмент не строим: это ещё минуты работы, а Esc должен
		// останавливать по-настоящему. Извлечённые куски никуда не денутся —
		// следующий запуск построит сегмент по ним заодно с новыми книгами.
		res.Elapsed = time.Since(start)
		res.Canceled = ctx.Err() != nil
		send(Progress{
			Phase: "индекс", Collection: c.name, Done: true, Canceled: res.Canceled,
			Added: res.Added, Skipped: res.Skipped, Scans: res.Scans, Errors: res.Errors,
			Chunks: res.Chunks, Elapsed: res.Elapsed,
		})
		return res, nil
	}
	if err := c.buildSegment(ctx, send); err != nil {
		return res, err
	}
	res.Elapsed = time.Since(start)
	send(Progress{
		Phase: "индекс", Collection: c.name, Done: true,
		Added: res.Added, Skipped: res.Skipped, Scans: res.Scans, Errors: res.Errors,
		Chunks: res.Chunks, Elapsed: res.Elapsed,
	})
	return res, nil
}

// candidate — книга, отобранная к индексации.
type candidate struct {
	path string
	info os.FileInfo
}

// collect обходит пути и отбирает то, что стоит индексировать.
func (c *Collection) collect(paths []string, opt IndexOpts) ([]candidate, error) {
	maxBytes := opt.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 512 << 20
	}
	seen := map[string]bool{}
	var out []candidate

	c.mu.RLock()
	known := make(map[string]BookRec, len(c.docs))
	for _, d := range c.docs {
		known[d.Path] = d
	}
	c.mu.RUnlock()

	for _, root := range paths {
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		err = filepath.WalkDir(abs, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // недоступную папку молча пропускаем
			}
			if d.IsDir() {
				if strings.HasPrefix(d.Name(), ".") && d.Name() != "." {
					return filepath.SkipDir
				}
				return nil
			}
			if seen[p] {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(p))
			if ext != ".pdf" && ext != ".epub" {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			if info.Size() > maxBytes {
				return nil
			}
			// Уже проиндексированная и не изменившаяся книга пропускается
			// без чтения — на этом и держится дешёвая доливка.
			if rec, ok := known[p]; ok && rec.Unchanged(info) {
				return nil
			}
			seen[p] = true
			out = append(out, candidate{path: p, info: info})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	// Порядок обхода — по пути: так чтение ближе к последовательному.
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	if opt.BookLimit > 0 && len(out) > opt.BookLimit {
		out = out[:opt.BookLimit]
	}
	return out, nil
}

// extract разбирает книги и дописывает их куски в хранилище.
func (c *Collection) extract(ctx context.Context, files []candidate, opt IndexOpts, start time.Time, send func(Progress)) (IndexResult, error) {
	workers := opt.Workers
	if workers <= 0 {
		// Разбор упирается в процессор, а не в диск — это измерено. Но каждый
		// воркер держит книгу целиком в памяти, поэтому берём физические ядра.
		workers = runtime.NumCPU() / 2
	}
	if workers < 1 {
		workers = 1
	}
	if workers > len(files) {
		workers = len(files)
	}

	w, err := CreateWriter(c.dir)
	if err != nil {
		return IndexResult{}, err
	}
	defer w.Close()

	// Незавершённая книга от прошлого раза отбрасывается: журнал знает,
	// на чём остановились.
	if state, ok := c.lastCommit(); ok {
		if err := w.Rollback(state); err != nil {
			return IndexResult{}, err
		}
	}

	type parsed struct {
		cand   candidate
		doc    *document.Doc
		chunks []Chunk
		err    error
	}

	jobs := make(chan candidate)
	done := make(chan parsed)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for cand := range jobs {
				if ctx.Err() != nil {
					return
				}
				doc, chunks, err := parseBook(cand.path, opt.MaxBytes, c.meta.Chunk)
				select {
				case done <- parsed{cand: cand, doc: doc, chunks: chunks, err: err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, f := range files {
			select {
			case jobs <- f:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(done)
	}()

	var res IndexResult
	for p := range done {
		if ctx.Err() != nil {
			res.Canceled = true
			break
		}
		rec := BookRec{
			Path: p.cand.path, Size: p.cand.info.Size(),
			ModTime: p.cand.info.ModTime().UnixNano(), At: time.Now().Unix(),
		}
		switch {
		case p.err != nil:
			rec.Kind = classifyErr(p.err)
			rec.Err = shortErr(p.err)
			switch rec.Kind {
			case BookScan:
				res.Scans++
			case BookSkipped:
				res.Skipped++
			default:
				res.Errors++
			}
			if err := c.appendDoc(rec); err != nil {
				return res, err
			}
		case len(p.chunks) == 0:
			rec.Kind = BookScan
			rec.Err = "текста не нашлось"
			res.Scans++
			if err := c.appendDoc(rec); err != nil {
				return res, err
			}
		default:
			c.mu.Lock()
			rec.ID = c.meta.NextDoc
			c.meta.NextDoc++
			c.mu.Unlock()

			rec.Kind = BookOK
			rec.Title, rec.Author = p.doc.Title, p.doc.Author
			rec.Format, rec.Units, rec.UnitWord = string(p.doc.Kind), p.doc.Units, p.doc.Unit
			rec.Chunks = len(p.chunks)
			if rec.Title == "" {
				rec.Title = filepath.Base(p.cand.path)
			}
			if err := w.Append(rec.ID, p.chunks); err != nil {
				return res, err
			}
			state, err := w.Commit()
			if err != nil {
				return res, err
			}
			if err := c.appendDoc(rec); err != nil {
				return res, err
			}
			if err := c.journal(state, rec.ID); err != nil {
				return res, err
			}
			res.Added++
			res.Chunks += int64(len(p.chunks))
		}

		send(Progress{
			Phase: "извлечение", Collection: c.name,
			DocsDone: res.Added + res.Skipped + res.Scans + res.Errors, DocsTotal: len(files),
			Chunks: res.Chunks, Current: filepath.Base(p.cand.path),
			Added: res.Added, Skipped: res.Skipped, Scans: res.Scans, Errors: res.Errors,
			Elapsed: time.Since(start),
		})
	}
	// Даже при отмене доводим запись до целого состояния.
	if _, err := w.Commit(); err != nil {
		return res, err
	}
	if ctx.Err() != nil {
		res.Canceled = true
	}
	return res, nil
}

// parseBook читает книгу и режет её на куски.
func parseBook(path string, maxBytes int64, opt ChunkOpts) (*document.Doc, []Chunk, error) {
	if maxBytes <= 0 {
		maxBytes = 512 << 20
	}
	// Сначала дешёвая проба: у скана она отвечает по пяти страницам вместо
	// полного разбора всех трёхсот.
	if _, err := document.Probe(path, maxBytes, 5); err != nil {
		return nil, nil, err
	}
	doc, parts, err := document.Parts(path, maxBytes)
	if err != nil {
		return nil, nil, err
	}
	return doc, Split(parts, opt), nil
}

func classifyErr(err error) BookKind {
	s := err.Error()
	switch {
	case strings.Contains(s, "текстового слоя"):
		return BookScan
	case strings.Contains(s, "нечитаем"):
		return BookGarbled
	case strings.Contains(s, "слишком велик"), strings.Contains(s, "не документ"):
		return BookSkipped
	}
	return BookBroken
}

func shortErr(err error) string {
	s := err.Error()
	if i := strings.Index(s, ":"); i > 0 && i < 60 {
		s = s[:i]
	}
	if len([]rune(s)) > 120 {
		s = string([]rune(s)[:120])
	}
	return s
}

// buildSegment строит сегмент по кускам, ещё не попавшим ни в один сегмент.
func (c *Collection) buildSegment(ctx context.Context, send func(Progress)) error {
	store, err := OpenStore(c.dir)
	if err != nil {
		return err
	}
	defer store.Close()

	covered := 0
	dirs, err := segmentDirs(c.dir)
	if err != nil {
		return err
	}
	for _, d := range dirs {
		var meta SegMeta
		if err := readJSON(filepath.Join(d, "seg.meta"), &meta); err != nil {
			continue
		}
		if end := meta.FirstID + meta.Chunks; end > covered {
			covered = end
		}
	}
	n := store.Count() - covered
	if n <= 0 {
		return nil
	}

	c.mu.Lock()
	segNo := c.meta.NextSeg
	c.meta.NextSeg++
	c.mu.Unlock()

	dir := filepath.Join(c.dir, fmt.Sprintf("seg-%05d", segNo))
	send(Progress{Phase: "индекс", Collection: c.name, DocsTotal: n})
	_, err = BuildSegment(dir, store, covered, n, func(done int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		send(Progress{Phase: "индекс", Collection: c.name, DocsDone: done, DocsTotal: n})
		return nil
	})
	if err != nil {
		// Недостроенный сегмент не имеет seg.meta и будет пропущен при чтении,
		// но лучше убрать его сразу.
		os.RemoveAll(dir)
		return err
	}
	if err := c.saveMeta(); err != nil {
		return err
	}
	return c.reopenIndex()
}

// Sync сверяет коллекцию с диском: доиндексирует новые книги, помечает
// пропавшие и переиндексирует изменившиеся.
func (c *Collection) Sync(ctx context.Context, report func(Progress)) (IndexResult, error) {
	c.mu.RLock()
	roots := append([]string(nil), c.meta.Roots...)
	docs := append([]BookRec(nil), c.docs...)
	c.mu.RUnlock()
	if len(roots) == 0 {
		return IndexResult{}, fmt.Errorf("у коллекции %q не записано ни одного каталога — добавьте книги через /kb add", c.name)
	}

	// Пропавшие книги помечаем удалёнными: строка дозаписью, мгновенно
	// и без риска испортить индекс.
	var removed int
	for _, d := range docs {
		if d.Kind != BookOK || c.isDeleted(d.ID) {
			continue
		}
		if _, err := os.Stat(d.Path); err != nil && os.IsNotExist(err) {
			if err := c.markDeleted(d.ID); err != nil {
				return IndexResult{}, err
			}
			removed++
		}
	}

	res, err := c.Add(ctx, roots, IndexOpts{}, report)
	res.Removed = removed
	if removed > 0 {
		if rerr := c.reopenIndex(); err == nil {
			err = rerr
		}
	}
	return res, err
}

// PendingChanges — что изменилось в папках коллекции с прошлой индексации.
type PendingChanges struct {
	New     int // новых или изменившихся книг
	Missing int // книг, пропавших с диска
}

// Any сообщает, есть ли о чём говорить.
func (p PendingChanges) Any() bool { return p.New > 0 || p.Missing > 0 }

// Pending сверяет папки коллекции с реестром, ничего не индексируя.
//
// Нужна настройке kb.sync_on_start: сказать при запуске «появилось 3 новые
// книги» дёшево, а вот индексировать самовольно нельзя — это минуты работы
// и чужое решение. Файлы не читаются: сверка идёт по размеру и времени.
func (c *Collection) Pending() (PendingChanges, error) {
	c.mu.RLock()
	roots := append([]string(nil), c.meta.Roots...)
	docs := append([]BookRec(nil), c.docs...)
	c.mu.RUnlock()
	if len(roots) == 0 {
		return PendingChanges{}, nil
	}

	var res PendingChanges
	for _, d := range docs {
		if d.Kind != BookOK || c.isDeleted(d.ID) {
			continue
		}
		if _, err := os.Stat(d.Path); err != nil && os.IsNotExist(err) {
			res.Missing++
		}
	}
	// collect отбирает как раз новое и изменившееся: известные и нетронутые
	// книги он отбрасывает сам.
	cands, err := c.collect(roots, IndexOpts{})
	if err != nil {
		return res, err
	}
	res.New = len(cands)
	return res, nil
}

// Forget помечает книгу удалённой по её пути.
func (c *Collection) Forget(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	c.mu.RLock()
	var id uint32
	for _, d := range c.docs {
		if d.Path == abs || filepath.Base(d.Path) == path {
			id = d.ID
			break
		}
	}
	c.mu.RUnlock()
	if id == 0 {
		return fmt.Errorf("книги %q в коллекции нет", path)
	}
	if err := c.markDeleted(id); err != nil {
		return err
	}
	return c.reopenIndex()
}

func (c *Collection) isDeleted(id uint32) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.deleted[id]
}

func (c *Collection) markDeleted(id uint32) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.deleted[id] {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(c.dir, "deleted.ids"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%d\n", id); err != nil {
		return err
	}
	c.deleted[id] = true
	return nil
}

// appendDoc дописывает запись о книге в реестр.
func (c *Collection) appendDoc(rec BookRec) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	f, err := os.OpenFile(filepath.Join(c.dir, "docs.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	if i, ok := c.byPath[rec.Path]; ok {
		c.docs[i] = rec
	} else {
		c.byPath[rec.Path] = len(c.docs)
		c.docs = append(c.docs, rec)
	}
	return nil
}

// journalEntry — строка журнала коммитов.
type journalEntry struct {
	At    string     `json:"at"`
	Doc   uint32     `json:"doc"`
	State StoreState `json:"state"`
}

// journal записывает состояние после успешно добавленной книги.
func (c *Collection) journal(state StoreState, doc uint32) error {
	entry := journalEntry{At: time.Now().Format(time.RFC3339), Doc: doc, State: state}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(c.dir, "journal.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	c.mu.Lock()
	c.meta.State = state
	c.meta.Updated = time.Now()
	c.mu.Unlock()
	return c.saveMeta()
}

// lastCommit возвращает состояние последнего успешного коммита.
func (c *Collection) lastCommit() (StoreState, bool) {
	data, err := os.ReadFile(filepath.Join(c.dir, "journal.log"))
	if err != nil {
		return StoreState{}, false
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var e journalEntry
		if err := json.Unmarshal([]byte(lines[i]), &e); err == nil {
			return e.State, true
		}
	}
	return StoreState{}, false
}

func (c *Collection) saveMeta() error {
	c.mu.RLock()
	meta := c.meta
	c.mu.RUnlock()
	return writeJSON(filepath.Join(c.dir, "meta.json"), meta)
}

// AddRoots запоминает каталоги коллекции, чтобы /kb sync знал, что сверять.
func (c *Collection) AddRoots(paths []string) error {
	c.mu.Lock()
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		found := false
		for _, r := range c.meta.Roots {
			if r == abs {
				found = true
				break
			}
		}
		if !found {
			c.meta.Roots = append(c.meta.Roots, abs)
		}
	}
	c.mu.Unlock()
	return c.saveMeta()
}

// throttle склеивает частые сообщения о ходе работы: без этого тысячи книг
// дают тысячи сообщений и перегружают цикл событий интерфейса.
func throttle(report func(Progress)) func(Progress) {
	if report == nil {
		return func(Progress) {}
	}
	var last time.Time
	return func(p Progress) {
		if p.Done || p.Err != nil || time.Since(last) >= progressEvery {
			last = time.Now()
			report(p)
		}
	}
}

// ErrCanceled сообщает, что работу прервал пользователь.
var ErrCanceled = errors.New("индексация прервана")
