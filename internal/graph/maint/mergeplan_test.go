package maint

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// randomMerges — пачка решений по немногим номерам, чтобы повторы, цепочки,
// круги и встречные пары выпадали часто; изредка — нулевой номер и склейка
// с собой.
func randomMerges(rng *rand.Rand, n int) []graph.MergeRec {
	out := make([]graph.MergeRec, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, graph.MergeRec{From: uint32(rng.Intn(7)), To: uint32(rng.Intn(7))})
	}
	return out
}

// План сухого прогона обязан совпадать с тем, что запишет сам Add.
//
// Аудит 07.10.2026: сухой прогон --graph-merge называл число пар разбора,
// а Add молча отбрасывает уже склеенное и встречное — обещано было больше,
// чем делалось. planMerges повторяет отбор Add; здесь повтор сверяется
// с оригиналом на случайных журналах, в том числе с кругами, которые Add
// сам не пишет, но которые бывают после ручной правки журнала.
func TestPlanMergesAgreesWithAdd(t *testing.T) {
	rng := rand.New(rand.NewSource(20261007))
	coll := t.TempDir()
	g, err := graph.Create(coll, "проба", 0, graph.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	dir := g.Dir()
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	for iter := 0; iter < 300; iter++ {
		var journal bytes.Buffer
		for _, r := range randomMerges(rng, rng.Intn(10)) {
			b, _ := json.Marshal(r)
			journal.Write(append(b, '\n'))
		}
		if err := os.WriteFile(filepath.Join(dir, "merges.jsonl"), journal.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}

		g, err = graph.Open(coll, 0, graph.Rules{})
		if err != nil {
			t.Fatal(err)
		}
		had := g.Merges().Records()
		batch := randomMerges(rng, rng.Intn(10))
		plan := planMerges(had, batch)
		n, err := g.Merges().Add(batch)
		if err != nil {
			t.Fatal(err)
		}
		wrote := g.Merges().Records()[len(had):]
		g.Close()

		if n != len(plan.fresh) || len(wrote) != len(plan.fresh) {
			t.Fatalf("журнал %v, пачка %v: Add записал %d, план обещал %d", had, batch, n, len(plan.fresh))
		}
		for i := range wrote {
			if wrote[i].From != plan.fresh[i].From || wrote[i].To != plan.fresh[i].To {
				t.Fatalf("журнал %v, пачка %v: записано %v, в плане %v", had, batch, wrote, plan.fresh)
			}
		}
		if sum := len(plan.fresh) + plan.done + plan.opposite + plan.invalid; sum != len(batch) {
			t.Fatalf("план разобрал %d решений из %d", sum, len(batch))
		}
	}
}

// Сухой прогон --graph-merge называет то, что запишется на деле, а не число
// пар разбора, — и настоящая склейка затем записывает ровно столько же.
func TestMergeDryCountsWhatAddAccepts(t *testing.T) {
	f := newMaintFixture(t)
	// В фикстуре k8s (3) уже поглощено Kubernetes (2).
	verdicts := filepath.Join(t.TempDir(), "verdicts.tsv")
	body := "вердикт\tcos\tсиноним\tцифры\tязык\tid_a\tid_b\tкниг_a\tкниг_b\n" +
		"ДА\t0.97\ttrue\tfalse\tfalse\t1\t4\t1\t1\n" + // новая: CoreDNS ← Docker
		"ДА\t0.97\ttrue\tfalse\tfalse\t2\t3\t1\t1\n" + // уже склеено: 3 → 2
		"ДА\t0.97\ttrue\tfalse\tfalse\t3\t2\t2\t1\n" + // встречная: 2 → 3 при 3 → 2
		"ДА\t0.97\ttrue\tfalse\tfalse\t1\t4\t1\t1\n" // повтор первой
	if err := os.WriteFile(verdicts, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	before := dirHash(t, f.dir)
	var out bytes.Buffer
	if err := Merge(&out, f.cfg, f.name, verdicts, "all-yes", 0, false, true, false); err != nil {
		t.Fatalf("сухой прогон склейки: %v", err)
	}
	if dirHash(t, f.dir) != before {
		t.Fatalf("сухой прогон склейки изменил каталог графа: %v", dirFiles(t, f.dir))
	}
	for _, want := range []string{"пар по уровню 4", "уже склеено 2, встречных к прежним склейкам 1", "записалось бы решений 1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("в выводе сухого прогона нет %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	if err := Merge(&out, f.cfg, f.name, verdicts, "all-yes", 0, false, false, false); err != nil {
		t.Fatalf("склейка: %v", err)
	}
	if !strings.Contains(out.String(), "записано решений: 1") {
		t.Errorf("склейка записала не столько, сколько обещал сухой прогон:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "понятий было 3, стало 2") {
		t.Errorf("счёт понятий после склейки не тот:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(f.dir, "LOCK")); !os.IsNotExist(err) {
		t.Errorf("замок сборки не снят после склейки: %v", err)
	}
}
