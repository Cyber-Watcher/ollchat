package kb

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Уплотнение коллекции.
//
// Сегменты неизменяемы, а удаление книги — это одна строка в deleted.ids: поиск
// сразу перестаёт её выдавать, но куски в chunks.dat и постинги в сегментах
// остаются на диске навсегда. Так задумано и менять это нельзя — именно
// неизменяемость даёт главное свойство базы: доливка книг стоит ровно столько,
// сколько новых книг, а не пересборку всего. Плата за это — место.
//
// Уплотнение переписывает хранилище без удалённого и сливает все сегменты
// в один. Автоматически не запускается никогда: на большой коллекции это минуты
// чтения и записи, и момент выбирает пользователь. Подсказка о том, что пора,
// появляется в /kb stats.
//
// Внешние номера кусков («go/12#37») переживают уплотнение: они состоят
// из номера книги и порядкового номера куска внутри неё, а меняется только
// сквозная нумерация внутри файла. Ссылки в старых ответах модели остаются
// верными.

// MergeResult — что дало уплотнение.
type MergeResult struct {
	SegmentsBefore int
	SegmentsAfter  int
	ChunksBefore   int
	ChunksAfter    int
	VectorsAfter   int
	BooksDropped   int
	BytesBefore    int64
	BytesAfter     int64
	Elapsed        time.Duration
	Canceled       bool
}

// NeedsMerge подсказывает, стоит ли уплотнять: много сегментов или много
// мусора. Пороги невысокие намеренно — это подсказка, а не требование.
func (c *Collection) NeedsMerge() (bool, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	segs := len(c.segs)
	var dead int
	for _, rec := range c.docs {
		if c.deleted[rec.ID] {
			dead += rec.Chunks
		}
	}
	total := 0
	if c.store != nil {
		total = c.store.Count()
	}
	switch {
	case total > 0 && dead*100/total >= 20:
		return true, fmt.Sprintf("удалённое занимает %d%% кусков — стоит уплотнить: /kb merge %s",
			dead*100/total, c.name)
	case segs >= 8:
		return true, fmt.Sprintf("сегментов %d — поиск станет быстрее после /kb merge %s", segs, c.name)
	}
	return false, ""
}

// Merge уплотняет коллекцию.
//
// Работа идёт в отдельном каталоге, и только готовый результат встаёт на место
// прежнего переименованием. Прерывание на любом шаге оставляет коллекцию целой:
// либо ещё старую, либо уже новую, третьего состояния нет.
func (c *Collection) Merge(ctx context.Context, report func(Progress)) (res MergeResult, err error) {
	start := time.Now()
	if err := c.lock(); err != nil {
		return res, err
	}
	defer c.unlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.store == nil {
		return res, fmt.Errorf("коллекция %q пуста — уплотнять нечего", c.name)
	}
	res.SegmentsBefore = len(c.segs)
	res.ChunksBefore = c.store.Count()
	res.BytesBefore = dirSize(c.dir)

	tmp := c.base.tempDir("compact-" + c.name)
	if err := os.RemoveAll(tmp); err != nil {
		return res, err
	}
	if err := ensureDir(tmp); err != nil {
		return res, err
	}
	// Недоделанный каталог за собой не оставляем: он не мешает работе,
	// но занимает место и сбивает с толку.
	defer func() {
		if err != nil || res.Canceled {
			os.RemoveAll(tmp)
		}
	}()

	kept, dropped := c.survivors()
	res.BooksDropped = dropped

	written, state, keep, err := c.rewriteChunks(ctx, tmp, report)
	if err != nil {
		return res, err
	}
	if written < 0 {
		res.Canceled = true
		return res, nil
	}
	res.ChunksAfter = written

	// Векторы обязаны переехать вместе с кусками. Номер куска — это и есть
	// адрес вектора, поэтому уплотнение без этого шага не «теряет смыслы»,
	// а заставляет их указывать на чужой текст.
	vm, err := copyVectors(c.vectors, tmp, keep)
	if err != nil {
		return res, err
	}
	res.VectorsAfter = vm.Count

	if err := c.writeCompacted(tmp, kept, written, state, report); err != nil {
		return res, err
	}
	if err := ctx.Err(); err != nil {
		res.Canceled = true
		return res, nil
	}

	if err := c.swapIn(tmp); err != nil {
		return res, err
	}
	if err := c.load(); err != nil {
		return res, err
	}
	res.SegmentsAfter = len(c.segs)
	res.BytesAfter = dirSize(c.dir)
	res.Elapsed = time.Since(start)
	return res, nil
}

// survivors отбирает книги, пережившие уплотнение.
func (c *Collection) survivors() (kept []BookRec, dropped int) {
	for _, rec := range c.docs {
		if c.deleted[rec.ID] {
			dropped++
			continue
		}
		kept = append(kept, rec)
	}
	return kept, dropped
}

// rewriteChunks переписывает хранилище без кусков удалённых книг.
// Возвращает −1, если работу прервали.
// Возвращает также порядок сохранённых кусков в прежней нумерации: по нему
// переезжают векторы.
func (c *Collection) rewriteChunks(ctx context.Context, tmp string, report func(Progress)) (int, StoreState, []int, error) {
	var state StoreState
	var keep []int
	w, err := CreateWriter(tmp)
	if err != nil {
		return 0, state, nil, err
	}
	defer w.Close()

	recs := c.store.Recs()
	th := throttle(report)
	written := 0

	// Куски одной книги лежат подряд и в порядке возрастания Ord — так их
	// записал Writer.Append, и он же присвоит те же номера заново. Поэтому
	// книгу переносим целиком, одним куском работы.
	for i := 0; i < len(recs); {
		doc := recs[i].Doc
		j := i
		for j < len(recs) && recs[j].Doc == doc {
			j++
		}
		if c.deleted[doc] {
			i = j
			continue
		}
		if ctx.Err() != nil {
			return -1, state, nil, nil
		}
		chunks := make([]Chunk, 0, j-i)
		for k := i; k < j; k++ {
			text, err := c.store.Text(k)
			if err != nil {
				return 0, state, nil, fmt.Errorf("чтение куска %d: %w", k, err)
			}
			keep = append(keep, k)
			chunks = append(chunks, Chunk{
				Text:     text,
				UnitFrom: int(recs[k].UnitFrom),
				UnitTo:   int(recs[k].UnitTo),
				Flags:    ChunkFlags(recs[k].Flags),
			})
		}
		if err := w.Append(doc, chunks); err != nil {
			return 0, state, nil, err
		}
		written += len(chunks)
		th(Progress{Phase: "уплотнение", Collection: c.name,
			DocsDone: written, DocsTotal: len(recs), Chunks: int64(written)})
		i = j
	}
	state, err = w.Commit()
	if err != nil {
		return 0, state, nil, err
	}
	return written, state, keep, nil
}

// writeCompacted достраивает подготовленный каталог до целой коллекции:
// единственный сегмент, реестр только из выживших книг, чистые списки.
func (c *Collection) writeCompacted(tmp string, kept []BookRec, chunks int, state StoreState, report func(Progress)) error {
	store, err := OpenStore(tmp)
	if err != nil {
		return err
	}
	defer store.Close()

	th := throttle(report)
	segDir := filepath.Join(tmp, "seg-00001")
	if _, err := BuildSegment(segDir, store, 0, store.Count(), func(done int) error {
		th(Progress{Phase: "сегмент", Collection: c.name, DocsDone: done, DocsTotal: chunks})
		return nil
	}); err != nil {
		return err
	}

	var b strings.Builder
	for _, rec := range kept {
		line, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(tmp, "docs.jsonl"), []byte(b.String()), 0o644); err != nil {
		return err
	}

	// Номера книг не переиспользуем: NextDoc остаётся прежним. Иначе новая
	// книга получила бы номер удалённой, и ссылка из старого ответа модели
	// привела бы не туда.
	meta := c.meta
	meta.NextSeg = 2
	meta.Updated = time.Now()
	meta.State = state
	if err := writeJSON(filepath.Join(tmp, "meta.json"), meta); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(tmp, "journal.log"), nil, 0o644)
}

// swapIn ставит готовый каталог на место прежнего.
//
// Два переименования вместо копирования: переименование каталога в пределах
// файловой системы неделимо, поэтому оборваться можно только между ними —
// и это состояние распознаётся при следующем открытии базы.
func (c *Collection) swapIn(tmp string) error {
	c.closeFiles()
	old := c.base.tempDir("old-" + c.name)
	if err := os.RemoveAll(old); err != nil {
		return err
	}
	if err := os.Rename(c.dir, old); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.dir); err != nil {
		// Возврат к прежнему состоянию: коллекция должна остаться рабочей.
		os.Rename(old, c.dir)
		return err
	}
	return os.RemoveAll(old)
}

// recoverCompaction доводит до конца прерванное уплотнение.
//
// Оборваться можно только между двумя переименованиями: прежний каталог уже
// отставлен в сторону, нового ещё нет. Тогда возвращаем прежний — потерять
// работу уплотнения не жалко, потерять коллекцию нельзя.
func recoverCompaction(base *Base, name, dir string) {
	old := base.tempDir("old-" + name)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if _, err := os.Stat(old); err == nil {
			os.Rename(old, dir)
		}
	}
	os.RemoveAll(old)
	os.RemoveAll(base.tempDir("compact-" + name))
}
