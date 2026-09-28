package maint

import (
	"fmt"
	"io"
	"sort"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// DropDeadMarks — команда чистки отметок разбора у книг, которых в коллекции
// больше нет (`--graph-drop-dead-marks`).
//
// ЗАЧЕМ. Удаление книги оставляет в графе только одно: отметки разбора её
// кусков в `progress.log`. Перепись 27.09.2026 (этап 110, Б1) показала, что
// упоминаний, подтверждений связей и понятий от удалённых книг не остаётся
// вовсе, а отметок к 28.09 накопилось 11 861 у 12 записей. Они ничего
// не ломают — книг нет, куски не берутся, — но идут отдельной строкой в отчёте
// доктора и путают глаз. Слово владельца 28.09.2026: «убери этот мусор».
//
// ПРАВИЛО 1 («граф — священная корова») исполняется здесь буквально:
//
//   - без `--kb-dry-run` команда СНАЧАЛА печатает, что нашла, и только потом
//     правит; с ним — только печатает;
//   - перезапись журнала оставляет прежний файл копией с отметкой времени
//     (это делает `rewriteBinary`, которым работает и забывание кусков);
//   - живость книги определяется `LiveBooks`, а не `Books`: второй отдаёт
//     реестр вместе с удалёнными, и тогда «мёртвых» не нашлось бы вовсе
//     (на этом 27.09 ошибся доктор — печатал 1362 вместо 47);
//   - замок сборки берёт вызывающий (обвязка `graph-after.sh`,
//     `compact-after-catchup.sh`): журнал дозаписывается заходом, и править
//     его под идущей сборкой нельзя.
func DropDeadMarks(stdout io.Writer, cfg *config.Config, name string, dry bool) error {
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()
	coll, err := base.Open(name)
	if err != nil {
		return err
	}
	dir := cfg.Graph.Rules().Dir(coll.Dir())
	// Правило 1 буквально, порядок взят у забывания кусков (forgetlist.go):
	// занята коллекция — отказ, а не «подожду немного»; ждём конец архивации;
	// ставим признак работы, чтобы чужой заход ждал, а не писал поверх.
	if busy := graph.Busy(coll.Dir()); busy != "" {
		return fmt.Errorf("коллекция занята: %s — чистить отметки под идущей работой нельзя", busy)
	}
	if err := kb.WaitArchive(coll.Dir(), kb.ArchiveWait); err != nil {
		return err
	}
	unmark, err := graph.MarkWork(dir, "чистка отметок удалённых книг")
	if err != nil {
		return err
	}
	defer unmark()

	alive := map[uint32]bool{}
	for _, b := range coll.LiveBooks() {
		alive[b.ID] = true
	}
	if len(alive) == 0 {
		return fmt.Errorf("в коллекции %s нет ни одной живой книги — чистку не начинаю", name)
	}
	isAlive := func(doc uint32) bool { return alive[doc] }

	by, err := graph.DeadBookMarks(dir, isAlive)
	if err != nil {
		return err
	}
	if len(by) == 0 {
		fmt.Fprintf(stdout, "коллекция %s: отметок книг, которых нет, не найдено — чистить нечего\n", name)
		return nil
	}
	docs := make([]uint32, 0, len(by))
	total := 0
	for doc, n := range by {
		docs = append(docs, doc)
		total += n
	}
	sort.Slice(docs, func(i, j int) bool {
		if by[docs[i]] != by[docs[j]] {
			return by[docs[i]] > by[docs[j]]
		}
		return docs[i] < docs[j]
	})

	// Сперва показать, потом править: человек читает числа и сверяет их
	// с доктором, прежде чем журнал графа будет переписан.
	fmt.Fprintf(stdout, "коллекция %s: отметок у книг, которых в коллекции НЕТ — %d в %d записях\n",
		name, total, len(docs))
	names := map[uint32]string{}
	for _, b := range coll.Books() {
		names[b.ID] = b.Title
	}
	for _, doc := range docs {
		title := names[doc]
		if title == "" {
			title = "(нет и в реестре)"
		}
		if len(title) > 70 {
			title = title[:70]
		}
		fmt.Fprintf(stdout, "  %4d  %6d  %s\n", doc, by[doc], title)
	}
	if dry {
		fmt.Fprintf(stdout, "это --kb-dry-run: журнал не тронут\n")
		return nil
	}

	st, err := graph.DropDeadBookMarks(dir, isAlive, false)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "убрано отметок: %d (записей книг %d)\n", st.Marks, st.Books)
	if st.Backup != "" {
		fmt.Fprintf(stdout, "  прежний журнал: %s\n", st.Backup)
	}
	fmt.Fprintf(stdout, "  дальше — проверить двумя приборами: ollchat --graph-doctor %s "+
		"и python3 ollscripts/graphdoctorcheck.py\n", name)
	return nil
}
