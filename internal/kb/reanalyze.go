package kb

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Пересборка словесного индекса по новым правилам разбора.
//
// Когда меняются правила разбора (AnalyzerVersion), построенный индекс
// остаётся собранным по старым: слово с переносом лежит в нём двумя
// обрубками, лигатура — отдельной буквой. Перечитывать ради этого книги
// нельзя: перечитанная книга получает новый номер, и граф понятий, собранный
// неделями видеокарты, теряет ссылки на свои куски (см. reindex.go).
//
// Перечитывать и не нужно. Индекс строится по текстам кусков, а тексты уже
// лежат в хранилище. Reanalyze заново разбирает их на термы и подменяет
// сегменты; хранилище кусков, номера книг и кусков, векторы и граф
// не затрагиваются вовсе — их файлы даже не открываются на запись.
//
// До 17.09.2026 такой операции не было, хотя комментарий в analyze.go её
// обещал, а доктор при смене правил советовал --kb-reindex — то есть ровно
// то, что ломает граф.

const (
	// reanalyzeWindow — сколько кусков покрывает один новый сегмент. Постинги
	// копятся в памяти (около 2,4 КБ на кусок), окно держит потолок в ~250 МБ.
	reanalyzeWindow = 100_000

	reanalyzeMark = "REANALYZE" // план подмены; его наличие = подмена не закончена
	newSegPrefix  = "reseg-"    // готовые новые сегменты до подмены
	deadSegPrefix = "deadseg-"  // прежние сегменты после подмены, до удаления
)

// ReanalyzeResult — итог пересборки индекса.
type ReanalyzeResult struct {
	Chunks         int
	SegmentsBefore int
	SegmentsAfter  int
	TermsBefore    int
	TermsAfter     int
	From, To       string // версии правил разбора
	Elapsed        time.Duration
}

// reanalyzePlan — что подменить; пишется на диск до первой перестановки,
// чтобы прерванную подмену довело до конца следующее открытие коллекции.
type reanalyzePlan struct {
	Old      []string `json:"old"` // имена каталогов прежних сегментов
	New      []string `json:"new"` // имена каталогов новых сегментов (reseg-…)
	Analyzer string   `json:"analyzer"`
	NextSeg  int      `json:"next_seg"`
}

// Reanalyze пересобирает словесный индекс коллекции по её же кускам.
func (c *Collection) Reanalyze(ctx context.Context, report func(Progress)) (ReanalyzeResult, error) {
	started := time.Now()
	if report == nil {
		report = func(Progress) {}
	}
	if err := c.lock(); err != nil {
		return ReanalyzeResult{}, err
	}
	defer c.unlock()
	defer c.restamp() // запись своей же коллекции не должна выглядеть чужой

	store, err := OpenStore(c.dir)
	if err != nil {
		return ReanalyzeResult{}, err
	}
	defer store.Close()

	oldDirs, err := segmentDirs(c.dir)
	if err != nil {
		return ReanalyzeResult{}, err
	}
	st := c.Stats()
	res := ReanalyzeResult{
		Chunks: store.Count(), SegmentsBefore: len(oldDirs), TermsBefore: st.Terms,
		From: st.Analyzer, To: AnalyzerVersion,
	}

	// Остатки прерванной прошлой попытки: недостроенные новые сегменты.
	removeByPrefix(c.dir, newSegPrefix)

	c.mu.RLock()
	segNo := c.meta.NextSeg
	c.mu.RUnlock()

	plan := reanalyzePlan{Analyzer: AnalyzerVersion}
	for _, d := range oldDirs {
		plan.Old = append(plan.Old, filepath.Base(d))
	}
	total := store.Count()
	for first := 0; first < total; first += reanalyzeWindow {
		n := min(reanalyzeWindow, total-first)
		name := fmt.Sprintf("%s%05d", newSegPrefix, segNo)
		segNo++
		_, err := BuildSegment(filepath.Join(c.dir, name), store, first, n, func(done int) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			report(Progress{Phase: "индекс", Collection: c.name, DocsDone: first + done, DocsTotal: total})
			return nil
		})
		if err != nil {
			// Прежний индекс не тронут: новые сегменты лежат под своим именем
			// и поиском не читаются.
			removeByPrefix(c.dir, newSegPrefix)
			return res, err
		}
		plan.New = append(plan.New, name)
	}
	plan.NextSeg = segNo

	// С этой строки пути назад нет, только вперёд: план на диске, и подмену
	// доведёт до конца либо этот вызов, либо следующее открытие коллекции.
	if err := writeJSON(filepath.Join(c.dir, reanalyzeMark), plan); err != nil {
		removeByPrefix(c.dir, newSegPrefix)
		return res, err
	}
	// Подмена и переоткрытие — под замком коллекции: поиск, идущий в этом же
	// процессе, не должен увидеть закрытые файлы.
	c.mu.Lock()
	c.closeFiles()
	err = applyReanalyze(c.dir)
	if err == nil {
		var meta Meta
		if err = readJSON(filepath.Join(c.dir, "meta.json"), &meta); err == nil {
			c.meta.Analyzer, c.meta.NextSeg, c.meta.Updated = meta.Analyzer, meta.NextSeg, meta.Updated
			err = c.reopenIndex()
		}
	}
	c.mu.Unlock()
	if err != nil {
		return res, err
	}
	after := c.Stats()
	res.SegmentsAfter, res.TermsAfter = after.Segments, after.Terms
	res.Elapsed = time.Since(started)
	return res, nil
}

// applyReanalyze выполняет записанный план подмены сегментов. Каждый шаг
// безопасно повторять: прерванную подмену доводит до конца повторный вызов.
func applyReanalyze(dir string) error {
	var plan reanalyzePlan
	data, err := os.ReadFile(filepath.Join(dir, reanalyzeMark))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		return fmt.Errorf("план пересборки индекса не читается: %w", err)
	}
	// Сперва прежние сегменты уходят из-под имени, которое читает поиск, затем
	// на их место встают новые: между этими шагами индекс на миг пуст, но
	// никогда не удвоен — удвоенный исказил бы частоты молча.
	for _, name := range plan.Old {
		from := filepath.Join(dir, name)
		if _, err := os.Stat(from); err == nil {
			if err := os.Rename(from, filepath.Join(dir, deadSegPrefix+name)); err != nil {
				return err
			}
		}
	}
	for _, name := range plan.New {
		from := filepath.Join(dir, name)
		if _, err := os.Stat(from); err == nil {
			to := filepath.Join(dir, "seg-"+name[len(newSegPrefix):])
			if err := os.Rename(from, to); err != nil {
				return err
			}
		}
	}
	var meta Meta
	if err := readJSON(filepath.Join(dir, "meta.json"), &meta); err != nil {
		return err
	}
	meta.Analyzer, meta.Updated = plan.Analyzer, time.Now()
	if plan.NextSeg > meta.NextSeg {
		meta.NextSeg = plan.NextSeg
	}
	if err := writeJSON(filepath.Join(dir, "meta.json"), meta); err != nil {
		return err
	}
	removeByPrefix(dir, deadSegPrefix)
	return os.Remove(filepath.Join(dir, reanalyzeMark))
}

// recoverReanalyze доводит до конца подмену, прерванную на середине,
// и убирает мусор попытки, прерванной до неё.
func recoverReanalyze(dir string) {
	if _, err := os.Stat(filepath.Join(dir, reanalyzeMark)); err == nil {
		_ = applyReanalyze(dir)
		return
	}
	// Без плана новые сегменты — недостроенная попытка: прежний индекс цел.
	// Идущую прямо сейчас пересборку не трогаем: её сегменты под замком.
	if markerOwner(filepath.Join(dir, lockMark)) == "" {
		removeByPrefix(dir, newSegPrefix)
	}
}

func removeByPrefix(dir, prefix string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && len(e.Name()) > len(prefix) && e.Name()[:len(prefix)] == prefix {
			os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}
