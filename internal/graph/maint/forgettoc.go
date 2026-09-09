package maint

import (
	"fmt"
	"io"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// ForgetTOC убирает из графа всё, что извлечено из СЛУЖЕБНЫХ кусков:
// оглавлений и указателей (FlagTOC, этап 99), списков литературы и выходных
// данных (FlagRefs, этап 101). Признаки ставит --kb-flag-toc; без них команде
// нечего забывать, и она так и скажет. Граф при этом закрыт, замок сборки свободен.
func ForgetTOC(stdout io.Writer, cfg *config.Config, name string, dry bool) error {
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()
	coll, err := base.Open(name)
	if err != nil {
		return err
	}
	toc := map[uint64]bool{}
	if err := coll.EachChunkRef(kb.ChunkFilter{}, func(c kb.ChunkRef) error {
		if c.TOC || c.Refs {
			toc[graph.ChunkKey{Doc: c.Doc, Ord: c.Ord}.Pack()] = true
		}
		return nil
	}); err != nil {
		return err
	}
	if len(toc) == 0 {
		return fmt.Errorf("в коллекции %s нет кусков со служебным признаком — сначала ollchat --kb-flag-toc %s", name, name)
	}
	dir := cfg.Graph.Rules().Dir(coll.Dir())
	if busy := graph.Busy(coll.Dir()); busy != "" {
		return fmt.Errorf("коллекция занята: %s — забывать куски под идущей работой нельзя", busy)
	}
	if err := kb.WaitArchive(coll.Dir(), kb.ArchiveWait); err != nil {
		return err
	}
	unmark, err := graph.MarkWork(dir, "чистка графа от оглавлений")
	if err != nil {
		return err
	}
	defer unmark()

	started := time.Now()
	st, err := graph.ForgetChunks(dir, func(k graph.ChunkKey) bool { return toc[k.Pack()] }, dry)
	if err != nil {
		return err
	}
	what := "граф очищен"
	if dry {
		what = "было бы убрано"
	}
	fmt.Fprintf(stdout, "%s (%s): помеченных кусков в индексе %d; %s; за %s\n",
		what, name, len(toc), st, time.Since(started).Round(time.Second))
	for _, b := range st.Backups {
		fmt.Fprintf(stdout, "  прежний журнал: %s\n", b)
	}
	if !dry && st.Chunks > 0 {
		fmt.Fprintf(stdout, "  дальше: ollchat --graph-communities %s (разбиение тем по очищенным связям), затем --graph-doctor %s\n", name, name)
	}
	return nil
}
