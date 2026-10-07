package maint

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Сухой прогон обязан быть сухим у каждой разрушительной команды графа:
// каталог коллекции вместе с графом после него тот же до байта (аудит
// 07.10.2026: «граф — священная корова», а обещание сухого прогона держалось
// на словах — --graph-merge-drop стирал журнал склеек и при нём).
//
// Строки идут по одной фикстуре подряд: каждая сама снимает отпечаток до
// и после, так что команда, которая всё-таки что-то записала, попадётся
// на своей строке, а не на чужой. --graph-forget-toc здесь нет: в фикстуре
// нет кусков с признаком оглавления, а его сухой ход — тот же
// graph.ForgetChunks, что у --graph-forget-chunks.
func TestDestructiveCommandsDryRunTouchNothing(t *testing.T) {
	f := newMaintFixture(t)
	f.markMixed(t)
	// Карта книг — для переноса нумерации; записывается до отпечатков.
	if err := RebaseBooks(io.Discard, f.cfg, f.name, true, false); err != nil {
		t.Fatalf("запись карты книг: %v", err)
	}
	tmp := t.TempDir()
	file := func(name, body string) string {
		path := filepath.Join(tmp, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	verdicts := file("verdicts.tsv", "вердикт\tcos\tсиноним\tцифры\tязык\tid_a\tid_b\tкниг_a\tкниг_b\n"+
		"ДА\t0.97\ttrue\tfalse\tfalse\t1\t4\t1\t1\n")
	unmerge := file("unmerge.tsv", "3\t2\tфикстура: k8s отделяется от Kubernetes\n")
	deny := file("deny.tsv", "2\tкубер\tфикстура: ложный синоним\n")
	forget := file("forget.txt", fmt.Sprintf("%d#%d  фикстура\n%d#*  удалённая книга целиком\n",
		f.keep, f.keepOrds[0], f.gone))

	cases := []struct {
		name string
		run  func(cfg *config.Config, name string) error
	}{
		{"--graph-merge --graph-merge-file … --graph-merge-dry", func(cfg *config.Config, name string) error {
			return Merge(io.Discard, cfg, name, verdicts, "all-yes", 0, false, true, false)
		}},
		{"--graph-merge --graph-merge-drop --graph-merge-dry", func(cfg *config.Config, name string) error {
			return Merge(io.Discard, cfg, name, "", "strict", 0, true, true, false)
		}},
		{"--graph-unmerge --kb-dry-run", func(cfg *config.Config, name string) error {
			return Unmerge(io.Discard, cfg, name, unmerge, "", true)
		}},
		{"--graph-deny-aliases --kb-dry-run", func(cfg *config.Config, name string) error {
			return DenyAliases(io.Discard, cfg, name, deny, true)
		}},
		{"--graph-drop-dead-marks --kb-dry-run", func(cfg *config.Config, name string) error {
			return DropDeadMarks(io.Discard, cfg, name, true)
		}},
		{"--graph-forget-chunks --kb-dry-run", func(cfg *config.Config, name string) error {
			return ForgetList(io.Discard, cfg, name, forget, false, true)
		}},
		{"--graph-rebase-books --kb-dry-run", func(cfg *config.Config, name string) error {
			return RebaseBooks(io.Discard, cfg, name, false, true)
		}},
		{"--graph-drop-book без --apply", func(cfg *config.Config, name string) error {
			return DropBook(io.Discard, cfg, name, "keep.txt", DropRun{})
		}},
		{"--graph-drop-book --graph-restore-book без --apply", func(cfg *config.Config, name string) error {
			return DropBook(io.Discard, cfg, name, "keep.txt", DropRun{Restore: true})
		}},
		{"--graph-compact --graph-compact-check", func(cfg *config.Config, name string) error {
			return Compact(io.Discard, cfg, name, true, false, false)
		}},
		{"--graph-compact --graph-compact-check --graph-compact-drop-dead", func(cfg *config.Config, name string) error {
			return Compact(io.Discard, cfg, name, true, false, true)
		}},
	}
	for _, c := range cases {
		before := dirHash(t, f.coll)
		if err := c.run(f.cfg, f.name); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if after := dirHash(t, f.coll); after != before {
			t.Errorf("%s: сухой прогон изменил каталог коллекции; файлы графа теперь: %v", c.name, dirFiles(t, f.dir))
		}
	}
}

// Запрет синонима адресуется тому, у кого синоним записан на деле: в карточке
// выжившего видны синонимы всех поглощённых им, и запрет, записанный
// выжившему, при снятии склейки ушёл бы не с тем понятием.
func TestResolveDenyOwners(t *testing.T) {
	f := newMaintFixture(t) // Kubernetes (2) с синонимом «кубер» поглотил k8s (3) с «кубернетес»
	base, err := kb.OpenBase(f.cfg.KB.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	coll, err := base.Open(f.name)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := resolveDenyOwners(coll, f.cfg, []graph.AliasDeny{
		{ID: 2, Alias: "кубер", Why: "свой синоним выжившего"},
		{ID: 2, Alias: "Кубернетес", Why: "синоним поглощённого, другой регистр"},
		{ID: 3, Alias: "кубер", Why: "назван поглощённый, синоним у выжившего"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 || recs[0].ID != 2 || recs[1].ID != 3 || recs[2].ID != 2 {
		t.Fatalf("хозяева синонимов: %+v, ожидались 2, 3, 2", recs)
	}
	if _, err := resolveDenyOwners(coll, f.cfg, []graph.AliasDeny{{ID: 1, Alias: "кубер"}}); err == nil {
		t.Error("синоним, которого нет ни у понятия, ни у склеенных с ним, принят")
	}
}
