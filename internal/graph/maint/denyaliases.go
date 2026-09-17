package maint

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// DenyAliases запрещает понятиям ложные синонимы по списку из файла
// (этап 104, Ж1.5; устройство — internal/graph/aliasdeny.go).
//
// **Формат файла:** `номер понятия <TAB> синоним <TAB> причина`; пустые строки
// и строки с `//` пропускаются. Причина обязательна: через месяц по журналу
// должно быть видно, почему «cluster» перестал вести в Kubernetes.
func DenyAliases(stdout io.Writer, cfg *config.Config, name, file string, dry bool) error {
	recs, err := readDenyList(file)
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		return fmt.Errorf("в файле %s нет ни одной записи", file)
	}
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
	if busy := graph.Busy(coll.Dir()); busy != "" {
		return fmt.Errorf("коллекция занята: %s — править реестр под идущей работой нельзя", busy)
	}
	// Синоним может быть записан не у названного понятия, а у склеенного с ним:
	// в карточке выжившего видны синонимы всех поглощённых. Запрет адресуется
	// тому, у кого синоним записан на деле, — иначе при разводе склейки он
	// вернулся бы вместе с прежним хозяином.
	recs, err = resolveDenyOwners(coll, cfg, recs)
	if err != nil {
		return err
	}
	if !dry {
		if err := kb.WaitArchive(coll.Dir(), kb.ArchiveWait); err != nil {
			return err
		}
		unmark, err := graph.MarkWork(dir, "запрет ложных синонимов")
		if err != nil {
			return err
		}
		defer unmark()
	}
	effects, err := graph.DenyAliases(dir, recs, dry)
	if err != nil {
		return err
	}
	freed, moved, notKey := 0, 0, 0
	for _, ef := range effects {
		switch {
		case !ef.WasKey:
			notKey++
		case ef.OwnerAfterID == 0:
			freed++
		default:
			moved++
		}
		fmt.Fprintf(stdout, "  #%d %s ⊘ «%s»: ключ %s → %s\n", ef.Rec.ID, ef.Name, ef.Rec.Alias, ef.OwnerBefore, ef.OwnerAfter)
	}
	what := "запрещено"
	if dry {
		what = "было бы запрещено"
	}
	fmt.Fprintf(stdout, "%s синонимов: %d; ключей освобождено %d, перешло к другому понятию %d, ключом не был %d\n",
		what, len(effects), freed, moved, notKey)
	if !dry {
		fmt.Fprintf(stdout, "  векторы задетых понятий устарели: ollchat --graph-embed %s --graph-embed-stale (видеокарта, минуты)\n", name)
		fmt.Fprintf(stdout, "  уже приписанное по этим ключам убирает --graph-forget-chunks, возвращает следующая сборка\n")
	}
	return nil
}

// resolveDenyOwners заменяет в записях номер выжившего понятия номером того
// поглощённого, у которого синоним записан; запись, чей синоним есть у самого
// названного понятия, остаётся как есть. Один синоним у нескольких поглощённых
// даёт несколько записей.
func resolveDenyOwners(coll *kb.Collection, cfg *config.Config, recs []graph.AliasDeny) ([]graph.AliasDeny, error) {
	g, err := graph.Open(coll.Dir(), coll.ChunkCount(), cfg.Graph.Rules())
	if err != nil {
		return nil, err
	}
	defer g.Close()
	// Один проход по реестру вместо поиска на каждую запись.
	owners := map[string][]uint32{} // «выживший|синоним» → у кого записан
	want := map[string]bool{}
	for _, r := range recs {
		want[fmt.Sprintf("%d|%s", g.Merges().Resolve(r.ID), graph.Normalize(r.Alias))] = true
	}
	for _, e := range g.Entities().All() {
		live := g.Merges().Resolve(e.ID)
		for _, a := range e.Aliases {
			k := fmt.Sprintf("%d|%s", live, graph.Normalize(a))
			if want[k] {
				owners[k] = append(owners[k], e.ID)
			}
		}
	}
	var out []graph.AliasDeny
	for _, r := range recs {
		k := fmt.Sprintf("%d|%s", g.Merges().Resolve(r.ID), graph.Normalize(r.Alias))
		ids := owners[k]
		if len(ids) == 0 {
			ent, _ := g.Entities().Get(r.ID)
			return nil, fmt.Errorf("ни у понятия #%d %q, ни у склеенных с ним нет синонима %q", r.ID, ent.Name, r.Alias)
		}
		for _, id := range ids {
			rr := r
			rr.ID = id
			out = append(out, rr)
		}
	}
	return out, nil
}

func readDenyList(file string) ([]graph.AliasDeny, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []graph.AliasDeny
	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "//") {
			continue
		}
		p := strings.Split(text, "\t")
		if len(p) < 3 || strings.TrimSpace(p[1]) == "" || strings.TrimSpace(p[2]) == "" {
			return nil, fmt.Errorf("%s, строка %d: нужно «номер<TAB>синоним<TAB>причина»", file, line)
		}
		id, err := strconv.ParseUint(strings.TrimSpace(p[0]), 10, 32)
		if err != nil || id == 0 {
			return nil, fmt.Errorf("%s, строка %d: номер понятия %q", file, line, p[0])
		}
		out = append(out, graph.AliasDeny{ID: uint32(id), Alias: strings.TrimSpace(p[1]), Why: strings.TrimSpace(p[2]), By: "перепись aliascensus, просмотр глазами"})
	}
	return out, sc.Err()
}
