package maint

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Метки последней строки вывода: её читает скрипт докатки.
//
// **Почему метка, а не код выхода.** Код выхода у `ollchat` один на все беды
// (1 — ошибка), и заводить ради вердикта вторую систему значений значит однажды
// перепутать «рано» с «не смог». Метка латиницей — чтобы сравнение в оболочке
// не зависело от кодировки, а числа выше неё человек читает глазами.
const (
	repartitionYes = "REPARTITION=yes"
	repartitionNo  = "REPARTITION=no"
)

// Repartition сообщает, пора ли пересчитывать разметку тем, и ничего не меняет.
//
// **Зачем отдельная команда.** Докатка после каждой книги размечала темы
// и писала описания рефлексом — а описания это минуты карты на книгу (этап 105,
// А3: ~3 мин, вся докатка 10–13 мин). Пересчёт нужен не после каждой книги,
// а когда обзор начинает врать: понятия новой книги в темы не попали, и обзор
// их не видит. Мера у этого одна и та же и у доктора, и здесь — доля живых
// понятий вне тем (порог 10 %, `repartitionDue`, замер 02.09.2026).
//
// **Отдельного счётчика книг не нужно.** Каждая разобранная книга добавляет
// понятия, которые в темы не попадают, то есть сама двигает эту долю вверх:
// счётчик книг был бы вторым именем того же числа. Сколько понятий пришло
// с прошлого пересчёта, печатается рядом — чтобы решение можно было проверить.
func Repartition(stdout io.Writer, cfg *config.Config, name string) error {
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()

	if name == "" {
		name = cfg.KB.Default
	}
	coll, err := base.Open(name)
	if err != nil {
		return graphNeedsLocalFiles(cfg, name, err)
	}
	chunks := coll.ChunkCount()

	g, err := graph.Open(coll.Dir(), chunks, cfg.Graph.Rules())
	if err != nil {
		return err
	}
	defer g.Close()

	st := g.Stats(chunks)
	comms, cerr := g.LoadCommunities()
	if cerr != nil {
		// Битый файл — не «темы не размечены». До 07.10.2026 он молча давал
		// REPARTITION=yes, и докатка пересчитывала разметку поверх него: описания
		// тем (часы карты) не переносились, а битый файл уходил в копию
		// communities.prev.json поверх прежней, ещё целой. Ошибка без метки —
		// и докатка разметку не тронет, и человек увидит причину.
		return brokenCommunities(g, cerr)
	}
	if comms == nil || len(comms.List) == 0 {
		fmt.Fprintln(stdout, "темы не размечены — обзор тем работать не будет")
		fmt.Fprintln(stdout, repartitionYes)
		return nil
	}

	// Живые понятия, а не все: поглощённое склейкой понятие не самостоятельный
	// узел, и в темах его быть не должно. На этом месте доктор 15.09.2026 уже
	// ошибался — обход по All объявлял каждую склейку «понятием вне тем»
	// и звал пересчитывать без повода.
	inTopic := make(map[uint32]bool)
	for _, c := range comms.List {
		if c.Level != 0 {
			continue
		}
		for _, m := range c.Members {
			inTopic[m] = true
		}
	}
	// Живые и в числителе, и в знаменателе: делить живые понятия на записи
	// реестра (там остаются поглощённые склейкой) значит занижать долю.
	uncovered := countUncovered(g.Entities().Live(), inTopic)
	live := st.Live()
	share := 100 * uncovered / max(live, 1)
	fmt.Fprintf(stdout, "понятий вне тем %d из %d живых (%d%%), порог %d%%\n",
		uncovered, live, share, repartitionThreshold)
	if comms.Entities > 0 {
		fmt.Fprintf(stdout, "разбиение считалось при %d записях реестра, сейчас их %d (прибавка %+d)\n",
			comms.Entities, st.Entities, st.Entities-comms.Entities)
	}
	if repartitionDue(uncovered, live) {
		fmt.Fprintln(stdout, repartitionYes)
	} else {
		fmt.Fprintln(stdout, repartitionNo)
	}
	return nil
}

// brokenCommunities — отказ по нечитаемой разметке тем с тем, что с ней
// делать. Общий у доктора и --graph-repartition-due.
func brokenCommunities(g *graph.Graph, cerr error) error {
	return fmt.Errorf("%w\nфайл: %s\nпересчитывать поверх него вслепую нельзя: --graph-communities не перенесёт описаний тем "+
		"и отложит битый файл в %s поверх прежней копии.\nСначала сохраните оба файла и попробуйте восстановить разметку",
		cerr, filepath.Join(g.Dir(), graph.CommunityFile), graph.PrevCommunityFile)
}
