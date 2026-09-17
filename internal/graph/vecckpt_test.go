package graph

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// breakingEmbedder считает как fakeEmbedder, но после limit запросов отказывает
// по существу (не обрывом связи — того счёт ждёт сам): так выглядит для счёта
// снятый процесс, упавшая служба, выключенный стенд.
type breakingEmbedder struct {
	fakeEmbedder
	limit int
}

var errBroken = errors.New("сервер выключен")

func (b *breakingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if b.calls >= b.limit {
		return nil, errBroken
	}
	return b.fakeEmbedder.Embed(ctx, texts)
}

func probeNames(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		// Разная длина имени — разный вектор у fakeEmbedder: перепутанный
		// порядок был бы виден.
		out[i] = fmt.Sprintf("%s%0*d", prefix, i%7+1, i)
	}
	return out
}

// vectorsOf — копия всех векторов графа, как они лежат на диске после
// повторного открытия: проверяется файл, а не память процесса.
func vectorsOnDisk(t *testing.T, g *Graph) (int, []int8) {
	t.Helper()
	v := openEntityVectors(g.dir)
	if p := v.Problem(); p != "" {
		t.Fatalf("векторы с диска не приняты: %s", p)
	}
	return v.Count(), append([]int8(nil), v.data...)
}

// Обрыв посреди досчёта хвоста теряет одну порцию, а не весь счёт, и повтор
// той же команды доводит дело до конца с тем же итогом, что счёт без обрыва.
func TestEmbedTailSurvivesBreak(t *testing.T) {
	g := newGraphWith(t, probeNames("голова", 3)...)
	defer g.Close()
	ctx := context.Background()
	o := EmbedOpts{Batch: 2, Workers: 1, Checkpoint: 4}
	if err := g.EmbedEntities(ctx, &fakeEmbedder{model: "проба"}, o, nil); err != nil {
		t.Fatal(err)
	}
	for _, n := range probeNames("хвост", 11) {
		if _, _, err := g.Entities().Add(n, TypeConcept); err != nil {
			t.Fatal(err)
		}
	}

	// Порция — 4 понятия, два запроса; хвост — порции 4, 4 и 3. Пять запросов:
	// две порции целиком, третья оборвана на втором запросе.
	bad := &breakingEmbedder{fakeEmbedder: fakeEmbedder{model: "проба"}, limit: 5}
	err := g.EmbedEntities(ctx, bad, o, nil)
	if !errors.Is(err, errBroken) {
		t.Fatalf("ожидался отказ сервера, получено: %v", err)
	}
	count, _ := vectorsOnDisk(t, g)
	if count != 3+8 {
		t.Fatalf("после обрыва на диске %d векторов, ожидалось 11: две порции сохранены, третья нет", count)
	}

	good := &fakeEmbedder{model: "проба"}
	if err := g.EmbedEntities(ctx, good, o, nil); err != nil {
		t.Fatal(err)
	}
	if len(good.seen) != 3 {
		t.Fatalf("повтор отправил в модель %d понятий, ожидалось 3 оставшихся", len(good.seen))
	}
	count, got := vectorsOnDisk(t, g)
	if count != 14 {
		t.Fatalf("после повтора векторов %d, ожидалось 14", count)
	}

	// Эталон: тот же граф, посчитанный разом и без обрыва.
	ref := newGraphWith(t, append(probeNames("голова", 3), probeNames("хвост", 11)...)...)
	defer ref.Close()
	if err := ref.EmbedEntities(ctx, &fakeEmbedder{model: "проба"}, EmbedOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	_, want := vectorsOnDisk(t, ref)
	if string(unsafeBytes(got)) != string(unsafeBytes(want)) {
		t.Fatal("векторы после обрыва и повтора отличаются от счёта без обрыва")
	}
	if ids, unknown, have := g.StaleEntities(); !have || len(ids) != 0 || unknown != 0 {
		t.Fatalf("отпечатки после досчёта порциями: устаревших %v, без отпечатка %d, есть %v", ids, unknown, have)
	}
}

// Обрыв полного пересчёта не трогает прежние векторы: смысловой вход работает
// по-старому, посчитанное лежит рядом и повтор продолжает с места.
func TestRecountSurvivesBreak(t *testing.T) {
	all := probeNames("понятие", 11)
	g := newGraphWith(t, all...)
	defer g.Close()
	ctx := context.Background()
	o := EmbedOpts{Batch: 2, Workers: 1, Checkpoint: 4}
	if err := g.EmbedEntities(ctx, &fakeEmbedder{model: "проба"}, o, nil); err != nil {
		t.Fatal(err)
	}
	_, before := vectorsOnDisk(t, g)

	o.Recount = true
	bad := &breakingEmbedder{fakeEmbedder: fakeEmbedder{model: "проба"}, limit: 3}
	if err := g.EmbedEntities(ctx, bad, o, nil); !errors.Is(err, errBroken) {
		t.Fatalf("ожидался отказ сервера, получено: %v", err)
	}
	count, after := vectorsOnDisk(t, g)
	if count != 11 || string(unsafeBytes(after)) != string(unsafeBytes(before)) {
		t.Fatal("оборванный пересчёт тронул прежние векторы")
	}
	if _, err := os.Stat(filepath.Join(g.dir, entVecPartMeta)); err != nil {
		t.Fatalf("посчитанная порция не сохранена: %v", err)
	}

	good := &fakeEmbedder{model: "проба"}
	if err := g.EmbedEntities(ctx, good, o, nil); err != nil {
		t.Fatal(err)
	}
	if len(good.seen) != 11-4 {
		t.Fatalf("повтор пересчитал %d понятий, ожидалось 7: первая порция уже лежала", len(good.seen))
	}
	count, final := vectorsOnDisk(t, g)
	if count != 11 || string(unsafeBytes(final)) != string(unsafeBytes(before)) {
		t.Fatal("итог пересчёта с обрывом отличается от счёта без обрыва")
	}
	for _, n := range []string{entVecPartMeta, entVecPartData, entVecPartStamp} {
		if _, err := os.Stat(filepath.Join(g.dir, n)); err == nil {
			t.Fatalf("после удачного пересчёта остался файл %s", n)
		}
	}
	if ids, unknown, have := g.StaleEntities(); !have || len(ids) != 0 || unknown != 0 {
		t.Fatalf("отпечатки после пересчёта: устаревших %v, без отпечатка %d, есть %v", ids, unknown, have)
	}
}

// Текст понятия изменился между обрывом пересчёта и его продолжением: вектор
// посчитан от прежнего текста, и отпечаток обязан говорить именно это — иначе
// такое понятие никогда не найдётся как устаревшее.
func TestRecountKeepsStampsOfWhatWasEmbedded(t *testing.T) {
	g := newGraphWith(t, probeNames("понятие", 6)...)
	defer g.Close()
	ctx := context.Background()
	o := EmbedOpts{Batch: 2, Workers: 1, Checkpoint: 4, Recount: true}
	bad := &breakingEmbedder{fakeEmbedder: fakeEmbedder{model: "проба"}, limit: 2}
	if err := g.EmbedEntities(ctx, bad, o, nil); !errors.Is(err, errBroken) {
		t.Fatalf("ожидался отказ сервера, получено: %v", err)
	}
	first := probeNames("понятие", 6)[0]
	if _, _, err := g.Entities().Add(first, TypeConcept, "synonym"); err != nil {
		t.Fatal(err)
	}
	if err := g.EmbedEntities(ctx, &fakeEmbedder{model: "проба"}, o, nil); err != nil {
		t.Fatal(err)
	}
	ids, _, have := g.StaleEntities()
	if !have || len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("понятие 1 посчитано от старого текста и должно быть устаревшим, получено %v", ids)
	}
}

// Лечение устаревших тоже фиксируется порциями: после обрыва устаревшими
// остаются только те, до которых не дошли.
func TestEmbedStaleSurvivesBreak(t *testing.T) {
	all := probeNames("понятие", 9)
	g := newGraphWith(t, all...)
	defer g.Close()
	ctx := context.Background()
	o := EmbedOpts{Batch: 2, Workers: 1, Checkpoint: 4}
	if err := g.EmbedEntities(ctx, &fakeEmbedder{model: "проба"}, o, nil); err != nil {
		t.Fatal(err)
	}
	for _, n := range all {
		if _, _, err := g.Entities().Add(n, TypeConcept, n+"-alias"); err != nil {
			t.Fatal(err)
		}
	}
	if ids, _, _ := g.StaleEntities(); len(ids) != 9 {
		t.Fatalf("устаревших %d, ожидалось 9", len(ids))
	}
	bad := &breakingEmbedder{fakeEmbedder: fakeEmbedder{model: "проба"}, limit: 3}
	fixed, err := g.EmbedStale(ctx, bad, o, nil)
	if !errors.Is(err, errBroken) || fixed != 4 {
		t.Fatalf("ожидались отказ сервера и 4 вылеченных, получено: %d, %v", fixed, err)
	}
	if ids, _, _ := g.StaleEntities(); len(ids) != 5 {
		t.Fatalf("после обрыва устаревших %d, ожидалось 5", len(ids))
	}
	if _, err := g.EmbedStale(ctx, &fakeEmbedder{model: "проба"}, o, nil); err != nil {
		t.Fatal(err)
	}
	if ids, _, _ := g.StaleEntities(); len(ids) != 0 {
		t.Fatalf("после повтора остались устаревшие: %v", ids)
	}
	if _, data := vectorsOnDisk(t, g); len(data) != 9*4 {
		t.Fatalf("размер файла векторов %d, ожидалось 36", len(data))
	}
}

func unsafeBytes(d []int8) []byte {
	out := make([]byte, len(d))
	for i, v := range d {
		out[i] = byte(v)
	}
	return out
}
