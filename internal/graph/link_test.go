package graph

import (
	"context"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// nameEmbedder — подменный эмбеддер: у заранее названных имён вектор общий,
// у прочих — свой, ортогональный. Так близость пары управляется тестом.
type nameEmbedder struct{ same map[string]int }

func (e nameEmbedder) Model() string { return "проба" }
func (e nameEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, 8)
		k, ok := e.same[strings.ToLower(t)]
		if !ok {
			k = 7
		}
		v[k] = 1
		out[i] = v
	}
	return out, nil
}

// judge — подменный арбитр с заданным ответом.
type judge struct{ answer string }

func (j judge) Model() string                                          { return "судья" }
func (j judge) Extract(_ context.Context, _, _ string) (string, error) { return j.answer, nil }

// linkGraph — ОПЫТНЫЙ граф с понятием «Garbage collection» и его вектором:
// связывание на рабочем графе Build отказывает (TestLinkNewRefusedOnProductionGraph).
func linkGraph(t *testing.T, emb nameEmbedder) *Graph {
	t.Helper()
	g, err := CreateKind(collection(t), "", 1000, Rules{}, CreateOpts{Kind: KindExperimental})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	id, _, err := g.Entities().Add("Garbage collection", TypeConcept)
	if err != nil || id != 1 {
		t.Fatalf("Add: %d %v", id, err)
	}
	vecs, _ := emb.Embed(context.Background(), []string{"Garbage collection"})
	if err := g.SaveEntityVectors(emb.Model(), "", 8, kb.Quantize(vecs[0])); err != nil {
		t.Fatal(err)
	}
	return g
}

func chunkWith(text string) *source {
	s := &source{}
	s.chunks = append(s.chunks, kb.ChunkInfo{Index: 0, Doc: 1, Ord: 1, UnitFrom: 1, Unit: "стр.",
		Book: kb.BookRec{ID: 1, Title: "Проба", Path: "/AI/к.pdf"}, Text: text})
	return s
}

// Новое имя, близкое по вектору и подтверждённое арбитром, не заводит узла:
// упоминание уходит существующему понятию, решение — в журнал, а при
// повторном открытии графа имя ведёт туда же без арбитра.
func TestLinkNewJoinsExistingEntity(t *testing.T) {
	emb := nameEmbedder{same: map[string]int{"garbage collection": 1, "сборщик мусора": 1}}
	g := linkGraph(t, emb)
	m := &model{answer: func(int) (string, error) {
		return `{"entities":[{"name":"сборщик мусора","type":"понятие"}],"relations":[]}`, nil
	}}
	res, err := Build(context.Background(), chunkWith("сборщик мусора освобождает память"), g, m,
		BuildOpts{Workers: 1, Link: &LinkOpts{Embedder: emb, Judge: judge{"ДА\nперевод"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Linked != 1 || res.NewEntities != 0 {
		t.Fatalf("связано %d, новых узлов %d: %+v", res.Linked, res.NewEntities, res)
	}
	if g.Entities().Count() != 1 {
		t.Fatalf("узлов %d, ожидался один", g.Entities().Count())
	}
	if n := len(g.Mentions().Of(1)); n != 1 {
		t.Fatalf("упоминание не досталось выжившему: %d", n)
	}
	if id, ok := g.Links().Linked(Normalize("сборщик мусора")); !ok || id != 1 {
		t.Fatalf("журнал: %d %v", id, ok)
	}
	dir := g.Dir()
	g.Close()
	again, err := Open(dir[:len(dir)-len("/"+DirName)], 1, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if id, ok := again.Links().Linked(Normalize("сборщик мусора")); !ok || id != 1 {
		t.Fatalf("после переоткрытия связь потеряна: %d %v", id, ok)
	}
	if again.Links().Queued() != 0 {
		t.Errorf("в очереди человеку ничего быть не должно: %d", again.Links().Queued())
	}
}

// «?» арбитра — очередь человеку, узел заводится как обычно; «НЕТ» —
// узел заводится, в очереди пусто; без ключа Link ничего не меняется.
func TestLinkNewQueuesDoubtAndRespectsNo(t *testing.T) {
	cases := []struct {
		name   string
		answer string
		queued int
	}{
		{"сомнение", "?\nне понять", 1},
		{"нет", "НЕТ\nразные", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			emb := nameEmbedder{same: map[string]int{"garbage collection": 1, "сборщик мусора": 1}}
			g := linkGraph(t, emb)
			m := &model{answer: func(int) (string, error) {
				return `{"entities":[{"name":"сборщик мусора","type":"понятие"}],"relations":[]}`, nil
			}}
			res, err := Build(context.Background(), chunkWith("сборщик мусора освобождает память"), g, m,
				BuildOpts{Workers: 1, Link: &LinkOpts{Embedder: emb, Judge: judge{c.answer}}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if res.Linked != 0 || res.NewEntities != 1 || res.Queued != c.queued {
				t.Fatalf("связано %d, новых %d, в очереди %d", res.Linked, res.NewEntities, res.Queued)
			}
			if g.Entities().Count() != 2 {
				t.Fatalf("узлов %d, ожидалось два", g.Entities().Count())
			}
		})
	}

	// Далёкое по вектору имя арбитру не показывается вовсе.
	emb := nameEmbedder{same: map[string]int{"garbage collection": 1}}
	g := linkGraph(t, emb)
	m := &model{answer: func(int) (string, error) {
		return `{"entities":[{"name":"сборщик мусора","type":"понятие"}],"relations":[]}`, nil
	}}
	res, err := Build(context.Background(), chunkWith("сборщик мусора освобождает память"), g, m,
		BuildOpts{Workers: 1, Link: &LinkOpts{Embedder: emb, Judge: judge{"ДА"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Linked != 0 || g.Links().Count() != 0 {
		t.Fatalf("далёкое имя связано: %+v, решений %d", res, g.Links().Count())
	}
}

// Очередь человеку: «?» арбитра ждёт решения; «ДА» человека склеивает узлы
// (merges.jsonl) и уводит имя к узлу; «НЕТ» человека снимает пару с очереди
// и отменяет связывание арбитра; судейский список показывает «ДА» арбитра,
// пока человек его не пересмотрел.
func TestLinkQueueAndHumanDecisions(t *testing.T) {
	emb := nameEmbedder{same: map[string]int{"garbage collection": 1, "сборщик мусора": 1}}
	g := linkGraph(t, emb)
	m := &model{answer: func(int) (string, error) {
		return `{"entities":[{"name":"сборщик мусора","type":"понятие"}],"relations":[]}`, nil
	}}
	if _, err := Build(context.Background(), chunkWith("сборщик мусора освобождает память"), g, m,
		BuildOpts{Workers: 1, Link: &LinkOpts{Embedder: emb, Judge: judge{"?\nне понять"}}}, nil); err != nil {
		t.Fatal(err)
	}
	q := g.Links().Queue()
	if len(q) != 1 || q[0].Cand != 1 || q[0].CandName != "Garbage collection" || q[0].Source != LinkFromBuild {
		t.Fatalf("очередь: %+v", q)
	}
	// Человек: одно и то же → склейка узла «сборщик мусора» (id 2) с 1.
	if err := g.DecideLink(q[0], LinkYes, "перевод"); err != nil {
		t.Fatal(err)
	}
	if len(g.Links().Queue()) != 0 || g.Links().Queued() != 0 {
		t.Fatalf("после решения очередь не пуста: %+v", g.Links().Queue())
	}
	if to := g.Merges().Resolve(2); to != 1 {
		t.Fatalf("склейка не записана: 2 → %d", to)
	}
	if id, ok := g.Links().Linked(Normalize("сборщик мусора")); !ok || id != 1 {
		t.Fatalf("имя не ведёт к узлу: %d %v", id, ok)
	}

	// Судейский режим: «ДА» арбитра видно, пока человек не отменил.
	g2 := linkGraph(t, emb)
	if _, err := Build(context.Background(), chunkWith("сборщик мусора освобождает память"), g2, m,
		BuildOpts{Workers: 1, Link: &LinkOpts{Embedder: emb, Judge: judge{"ДА\nперевод"}}}, nil); err != nil {
		t.Fatal(err)
	}
	j := g2.Links().Judged()
	if len(j) != 1 || j[0].To != 1 {
		t.Fatalf("судейский список: %+v", j)
	}
	if err := g2.DecideLink(j[0], LinkNo, "род и вид"); err != nil {
		t.Fatal(err)
	}
	if len(g2.Links().Judged()) != 0 {
		t.Fatalf("после отмены человеком решение арбитра всё ещё в списке")
	}
	if _, ok := g2.Links().Linked(Normalize("сборщик мусора")); ok {
		t.Fatal("после «НЕТ» человека имя всё ещё связано")
	}
	if got := LinkQueueSize(g2.Dir()); got != 0 {
		t.Fatalf("LinkQueueSize = %d", got)
	}
}

// Спорные пары ночного разбора двойников встают в очередь человеку из TSV
// арбитра: берутся только «?», уже разобранная пара второй раз не дописывается.
func TestQueueDoubtsTSV(t *testing.T) {
	dir := t.TempDir()
	tsv := "вердикт\tимя_a\tid_a\tid_b\tимя_b\tcos\tпричина\n" +
		"?\tСборщик мусора\t2\t1\tGarbage collection\t0.91\tперевод или род и вид\n" +
		"ДА\tgoroutines\t4\t3\tgoroutine\t0.97\tформы слова\n" +
		"?\tKV cache\t6\t5\tKV-кэш\t0.88\t" + strings.Repeat("длинная причина ", 20) + "\n"
	n, err := QueueDoubtsTSV(dir, strings.NewReader(tsv))
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("в очередь %d пар, ожидалось 2", n)
	}
	if again, err := QueueDoubtsTSV(dir, strings.NewReader(tsv)); err != nil || again != 0 {
		t.Fatalf("повторный вызов: %d, %v — пары уже в журнале", again, err)
	}
	if got := LinkQueueSize(dir); got != 2 {
		t.Fatalf("LinkQueueSize = %d, ожидалось 2", got)
	}
	l, err := openLinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	q := l.Queue()
	if len(q) != 2 || q[0].Norm != Normalize("Сборщик мусора") || q[0].From != 2 || q[0].Cand != 1 ||
		q[0].CandName != "Garbage collection" || q[0].Cos != 0.91 || q[0].By != LinkByJudge ||
		q[0].Source != LinkFromDoubles || q[0].Why != "перевод или род и вид" || q[0].Chunk != (ChunkKey{}) {
		t.Fatalf("очередь: %+v", q)
	}
	if got := len([]rune(q[1].Why)); got != 120 {
		t.Fatalf("причина не обрезана до 120 знаков: %d", got)
	}
}

// Кавычка в начале причины или имени не ломает чтение TSV арбитра.
//
// Файл пишется простым соединением через табуляцию, и разбор CSV принимал
// строку с кавычкой за начало поля в кавычках, глотая весь остаток файла.
func TestQueueDoubtsTSVQuotes(t *testing.T) {
	dir := t.TempDir()
	tsv := "вердикт\tимя_a\tid_a\tid_b\tимя_b\tcos\tпричина\n" +
		"?\tGo\t2\t1\tgolang\t0.91\t\"Go\" шире, чем golang\n" +
		"?\t\"smart\" pointer\t4\t3\tsmart pointer\t0.95\tкавычки в имени\n" +
		"?\tKV cache\t6\t5\tKV-кэш\t0.88\tобычная строка после кавычек\n"
	n, err := QueueDoubtsTSV(dir, strings.NewReader(tsv))
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("в очередь %d пар, ожидалось 3", n)
	}
	l, err := openLinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	why := map[string]string{}
	for _, r := range l.Queue() {
		why[r.Name] = r.Why
	}
	if len(why) != 3 || why["Go"] != "\"Go\" шире, чем golang" || why["\"smart\" pointer"] != "кавычки в имени" {
		t.Fatalf("очередь: %+v", why)
	}
}
