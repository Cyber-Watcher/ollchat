package kb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/fsx"
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

// graphDirName — имя каталога графа понятий внутри коллекции.
//
// Строкой, а не импортом из internal/graph: тот пакет сам зависит от kb,
// и обратная ссылка замкнула бы круг. Имя каталога — часть раскладки
// коллекции, и знать его здесь законно.
const graphDirName = "graph"

// GraphDirs перечисляет каталоги графов понятий внутри коллекции: рабочий
// `graph` и именованные `graph-<имя>` (graph.DirFor), по алфавиту.
//
// **Любой такой каталог, а не только с паспортом.** До 07.10.2026 граф
// узнавался по одному `graph/graph.meta`, и именованный граф `graph-lab`
// уплотнение не замечало вовсе: оно шло без `--kb-merge-force` и без единого
// предупреждения. Ложная тревога здесь стоит одного ключа, пропуск — недель
// работы видеокарты, поэтому граф — всё, что так названо.
func (c *Collection) GraphDirs() []string { return graphDirs(c.dir) }

func graphDirs(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && e.Type()&os.ModeSymlink == 0 {
			continue
		}
		if n := e.Name(); n == graphDirName || strings.HasPrefix(n, graphDirName+"-") {
			out = append(out, n)
		}
	}
	return out
}

// HasGraph сообщает, есть ли в коллекции граф понятий — рабочий или именованный.
func (c *Collection) HasGraph() bool { return len(c.GraphDirs()) > 0 }

// MergeOpts — как уплотнять.
type MergeOpts struct {
	// Force разрешает уплотнение коллекции, по которой собран граф.
	// По умолчанию это отказ: каталоги графов уплотнение сохраняет, но граф,
	// у коллекции которого стало меньше кусков, открываться отказывается
	// (graph.ErrCompacted), а стоит он часов работы видеокарты.
	Force bool
}

func (c *Collection) Merge(ctx context.Context, opt MergeOpts, report func(Progress)) (res MergeResult, err error) {
	defer c.restamp() // запись своей же коллекции не должна выглядеть чужой
	start := time.Now()

	// Граф ссылается на куски парой «книга, номер внутри книги» и уплотнение
	// пережил бы, но кусков становится меньше, и граф отказывается открываться
	// (graph.ErrCompacted) — предохранитель против тихого неверного ответа.
	// Цена ошибки несимметрична: уплотнение освобождает десятки мегабайт,
	// а пересборка графа стоит часов работы видеокарты. Поэтому здесь отказ,
	// а не предупреждение, и обходится он только явно.
	//
	// Сами каталоги графов уплотнение не трогает и с ключом: они переезжают
	// в новый каталог коллекции как есть (swapIn). До 07.10.2026 они стирались
	// вместе с прежним каталогом — с ключом и рабочий граф, и без ключа любой
	// именованный.
	if graphs := c.GraphDirs(); len(graphs) > 0 && !opt.Force {
		return res, fmt.Errorf(
			"по коллекции %s собран граф понятий (%s), и после уплотнения он перестанет открываться.\n"+
				"Граф лежит в %s и стоит часов работы видеокарты; уплотнение его не удалит,\n"+
				"но кусков станет меньше, и открыть его будет нельзя.\n"+
				"Если он больше не нужен — уберите каталог и повторите; "+
				"если нужен — уплотнять нельзя",
			c.name, strings.Join(graphs, ", "), c.dir)
	}

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
	// Остаток прошлой попытки убирается так же бережно, как при открытии:
	// в нём могут лежать перенесённые каталоги графов.
	if err := retireDir(tmp, c.dir); err != nil {
		return res, err
	}
	if err := ensureDir(tmp); err != nil {
		return res, err
	}
	// Копия замка в рабочем каталоге: по ней открытие коллекции из соседнего
	// процесса узнаёт идущее уплотнение и в мгновение между двумя
	// переименованиями подмены, и после неё — в новом каталоге она и станет
	// замком коллекции, который снимет unlock.
	if err := placeMarker(filepath.Join(tmp, lockMark)); err != nil {
		return res, err
	}
	// Недоделанный каталог за собой не оставляем: он не мешает работе,
	// но занимает место и сбивает с толку. Чужое из него (графы, если подмена
	// сорвалась после переноса) возвращается в коллекцию, а не стирается.
	defer func() {
		if err != nil || res.Canceled {
			retireDir(tmp, c.dir)
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
		// Подмена не состоялась, а файлы прежнего индекса уже закрыты:
		// открываем их снова, иначе поиск в этом процессе молча опустеет.
		c.reopenIndexLocked()
		return res, err
	}
	// Паспорт — тоже из нового каталога: в памяти остались прежние состояние
	// хранилища и номер следующего сегмента, и первая же запись паспорта
	// вернула бы их на диск.
	if err := readJSON(filepath.Join(c.dir, "meta.json"), &c.meta); err != nil {
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
		// Ничьи куски — книги, которой нет в реестре, — уходят вместе
		// с удалёнными: это прежние версии перечитанных книг и обрывки
		// прерванной записи, поиск их не выдаёт (SearchWith). Предпросмотр
		// уплотнения обещал их стереть всегда («кусков книг, перечитанных
		// заново»), а до 07.10.2026 они переезжали в новое хранилище и копились.
		if _, registered := c.book(doc); c.deleted[doc] || !registered {
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

	// Реестр и журнал — с fsync, как и всё остальное в новом каталоге: после
	// подмены прежнего уже не будет, и недописанный реестр означал бы
	// коллекцию без книг.
	if err := writeDocs(tmp, kept); err != nil {
		return err
	}

	// Номера книг не переиспользуем: NextDoc остаётся прежним. Иначе новая
	// книга получила бы номер удалённой, и ссылка из старого ответа модели
	// привела бы не туда.
	meta := c.meta
	meta.NextSeg = 2
	// Сегмент только что построен нынешними правилами разбора.
	meta.Analyzer = AnalyzerVersion
	meta.Updated = time.Now()
	meta.State = state
	if err := writeJSON(filepath.Join(tmp, "meta.json"), meta); err != nil {
		return err
	}
	return fsx.WriteFileAtomic(filepath.Join(tmp, "journal.log"), nil, 0o644)
}

// swapIn ставит готовый каталог на место прежнего.
//
// Два переименования вместо копирования: переименование каталога в пределах
// файловой системы неделимо, поэтому оборваться можно только между ними —
// и это состояние распознаётся при следующем открытии базы.
//
// **Перед подменой в новый каталог переезжает всё чужое** — то, чего kb
// не пишет сам (kbOwned): каталоги графов понятий `graph` и `graph-<имя>`,
// файлы человека. Переименованием, без копирования: тот же каталог базы,
// та же файловая система. До 07.10.2026 этого шага не было, и прежний
// каталог уходил в RemoveAll вместе с графами — неделями работы видеокарты.
func (c *Collection) swapIn(tmp string) error {
	old := c.base.tempDir("old-" + c.name)
	if err := retireDir(old, c.dir); err != nil {
		return err
	}
	if err := carryOver(c.dir, tmp); err != nil {
		return err
	}
	c.closeFilesLocked() // Merge держит c.mu.Lock всё время подмены
	if err := os.Rename(c.dir, old); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.dir); err != nil {
		// Возврат к прежнему состоянию: коллекция должна остаться рабочей.
		// Перенесённое чужое вернёт из tmp отложенная уборка Merge.
		os.Rename(old, c.dir)
		return err
	}
	// Прежний каталог убирается тем же бережным способом: чужое, появившееся
	// в нём уже после переноса, возвращается, а не стирается.
	retireDir(old, c.dir)
	return nil
}

// kbOwned сообщает, что запись каталога коллекции пишет сам kb и при подмене
// каталога её заменяет новая. Всё остальное — чужое, и уплотнение обязано его
// сохранить: каталоги графов, заметки человека, что угодно ещё.
//
// Список закрытый намеренно. Открытый («графы — это graph*») однажды пропустил
// бы новый вид данных, а цена пропуска — их потеря; лишняя запись в списке
// чужого стоит только места.
func kbOwned(name string) bool {
	switch name {
	case "meta.json", "docs.jsonl", "docs.jsonl.new", "deleted.ids", "journal.log",
		"chunks.dat", "chunks.idx", "vectors.dat", "vectors.meta",
		lockMark, archiveMark, reanalyzeMark:
		return true
	}
	for _, prefix := range []string{"seg-", newSegPrefix, deadSegPrefix} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	// Временные файлы атомарной записи (fsx.WriteFileAtomic): «.<имя>.<число>.tmp».
	if strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".tmp") {
		for _, own := range []string{"meta.json", "docs.jsonl", "chunks.idx", "vectors.meta", reanalyzeMark} {
			if strings.HasPrefix(name, "."+own+".") {
				return true
			}
		}
	}
	return false
}

// carryOver переносит чужие записи каталога from в каталог to.
//
// Сорвался перенос одной записи — уже перенесённые возвращаются назад: граф
// не должен остаться в рабочем каталоге уплотнения, который потом уберут.
func carryOver(from, to string) error {
	entries, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	var moved []string
	for _, e := range entries {
		name := e.Name()
		if kbOwned(name) {
			continue
		}
		if err := os.Rename(filepath.Join(from, name), filepath.Join(to, name)); err != nil {
			for _, m := range moved {
				os.Rename(filepath.Join(to, m), filepath.Join(from, m))
			}
			return fmt.Errorf("перенос %s в новый каталог коллекции: %w", name, err)
		}
		moved = append(moved, name)
	}
	return nil
}

// retireDir убирает рабочий каталог уплотнения (`.compact-…` или `.old-…`),
// ничего чужого не теряя: всё, чего kb не пишет сам, сперва возвращается
// в каталог коллекции live, а удаляются только файлы kb.
//
// Вернуть не вышло (каталога коллекции нет, или в нём уже есть запись с тем же
// именем) — каталог остаётся на месте вместе с чужим, и об этом говорит ошибка.
// Каталоги с точкой Names() не показывает, так что коллекцией он не прикинется,
// а разобраться с ним человек сможет руками.
func retireDir(dir, live string) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var kept []string
	for _, e := range entries {
		name := e.Name()
		path := filepath.Join(dir, name)
		if kbOwned(name) {
			if err := os.RemoveAll(path); err != nil {
				return err
			}
			continue
		}
		target := filepath.Join(live, name)
		if _, err := os.Lstat(target); err == nil {
			kept = append(kept, name)
			continue
		}
		if err := os.Rename(path, target); err != nil {
			kept = append(kept, name)
		}
	}
	if len(kept) > 0 {
		return fmt.Errorf("в %s остались записи, которые не удалось вернуть в коллекцию (%s): "+
			"каталог не удалён, разберите его вручную", dir, strings.Join(kept, ", "))
	}
	return os.Remove(dir)
}

// recoverCompaction доводит до конца прерванное уплотнение.
//
// Оборваться можно только между двумя переименованиями: прежний каталог уже
// отставлен в сторону, нового ещё нет. Тогда возвращаем прежний — потерять
// работу уплотнения не жалко, потерять коллекцию нельзя.
//
// Рабочие каталоги убираются через retireDir, а не RemoveAll: в них может
// лежать перенесённый граф (обрыв между carryOver и подменой).
//
// **Идущее уплотнение не трогается.** Зовётся это при каждом Base.Open, а
// коллекцию открывают и служба, и интерфейс, и соседние команды. До 07.10.2026
// замок не проверялся: открытие коллекции из ollchat или ollmcp посреди
// `--kb-merge` сносило его рабочий каталог, и уплотнение падало на подмене.
// Уплотнение держит замок и в каталоге коллекции, и в своём рабочем
// (Merge кладёт туда копию), поэтому между двумя переименованиями, когда
// каталога коллекции нет вовсе, живой замок виден в обоих отставленных.
func recoverCompaction(base *Base, name, dir string) {
	old := base.tempDir("old-" + name)
	tmp := base.tempDir("compact-" + name)
	for _, d := range []string{dir, old, tmp} {
		if held, _ := markerState(filepath.Join(d, lockMark)); held {
			return
		}
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if _, err := os.Stat(old); err == nil {
			os.Rename(old, dir)
		}
	}
	retireDir(old, dir)
	retireDir(tmp, dir)
}
