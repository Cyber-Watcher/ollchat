package graph

import (
	"fmt"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// fakeChunks — хранилище кусков для проверки провенанса: что положили,
// то и отдаёт, а на чужой номер отвечает «нет такого».
type fakeChunks map[uint64]string

func (f fakeChunks) ChunkByRef(doc, ord uint32) (kb.ChunkInfo, bool) {
	text, ok := f[ChunkKey{Doc: doc, Ord: ord}.Pack()]
	if !ok {
		return kb.ChunkInfo{}, false
	}
	return kb.ChunkInfo{Doc: doc, Ord: ord, Text: text}, true
}

// Проверка ловит связь, у которой кусок-источник не существует, — именно эта
// беда была невидима доктору до 16.09.2026: число подтверждений оставалось
// прежним и когда выдержку уже нечем показать.
func TestProvenanceFindsMissingChunk(t *testing.T) {
	g := newGraphWith(t, "goroutine", "channel")
	defer g.Close()

	if err := g.Edges().Add(Edge{
		Src: 1, Dst: 2, Type: RelUses, Weight: 1,
		Evidence: ChunkKey{Doc: 7, Ord: 3}, // куска с таким номером не будет
	}); err != nil {
		t.Fatal(err)
	}

	rep := g.Provenance(fakeChunks{}, 20, 1)
	if rep.Checked == 0 {
		t.Fatal("ни одной связи не проверено — выборка не нашла рёбер")
	}
	if rep.Missing == 0 {
		t.Fatalf("битая ссылка не поймана: %+v", rep)
	}
	if rep.Bad() == 0 {
		t.Fatal("доля бед нулевая при найденной беде")
	}
	if len(rep.Examples) == 0 {
		t.Fatal("нет примера — человеку не на что посмотреть")
	}
}

// Синоним считается подтверждением: граф двуязычный, и модель извлечения
// законно называет понятие написанием, которого в этом куске нет.
// Без этого правила проверка записывала бы законные связи в ошибки
// (замер 16.09.2026: 6,73% против 2,9% с синонимами).
func TestProvenanceCountsAliasAsSeen(t *testing.T) {
	g := newGraphWith(t)
	defer g.Close()
	if _, _, err := g.Entities().Add("goroutine", TypeConcept, "горутина"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := g.Entities().Add("channel", TypeConcept, "канал"); err != nil {
		t.Fatal(err)
	}
	key := ChunkKey{Doc: 1, Ord: 1}
	if err := g.Edges().Add(Edge{Src: 1, Dst: 2, Type: RelUses, Weight: 1, Evidence: key}); err != nil {
		t.Fatal(err)
	}

	// В куске только русские написания — ни одного имени понятия дословно.
	src := fakeChunks{key.Pack(): "Горутина пишет в канал, и это дёшево."}
	rep := g.Provenance(src, 20, 1)
	if rep.Both == 0 {
		t.Fatalf("синонимы не зачтены: %+v", rep)
	}
	if rep.None > 0 {
		t.Fatalf("связь записана в ошибки, хотя оба понятия названы синонимами: %+v", rep)
	}

	// Тот же кусок без обоих написаний — связь не подтверждается.
	rep = g.Provenance(fakeChunks{key.Pack(): "Совсем про другое."}, 20, 1)
	if rep.None == 0 {
		t.Fatalf("неподтверждённая связь не замечена: %+v", rep)
	}
}

// Короткий синоним не годится в подтверждение: «ML» найдётся внутри
// случайного слова и даст ложное «связь подтверждена».
func TestProvenanceIgnoresShortAlias(t *testing.T) {
	g := newGraphWith(t)
	defer g.Close()
	if _, _, err := g.Entities().Add("machine learning", TypeConcept, "ML"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := g.Entities().Add("dataset", TypeConcept); err != nil {
		t.Fatal(err)
	}
	key := ChunkKey{Doc: 1, Ord: 1}
	if err := g.Edges().Add(Edge{Src: 1, Dst: 2, Type: RelUses, Weight: 1, Evidence: key}); err != nil {
		t.Fatal(err)
	}
	// «HTML» содержит «ml», но про машинное обучение здесь ничего нет.
	src := fakeChunks{key.Pack(): "Шаблон HTML и dataset лежат рядом."}
	rep := g.Provenance(src, 20, 1)
	if rep.Both > 0 {
		t.Fatalf("короткий синоним зачтён внутри чужого слова: %+v", rep)
	}
	if rep.One == 0 {
		t.Fatalf("ожидалось «только одно имя» (dataset): %+v", rep)
	}
}

// Пустой граф и отсутствие хранилища кусков не должны ронять проверку:
// доктор зовёт её на любом графе, в том числе только что созданном.
func TestProvenanceSafeOnEmpty(t *testing.T) {
	g := newGraphWith(t)
	defer g.Close()
	if rep := g.Provenance(fakeChunks{}, 10, 1); rep.Checked != 0 || rep.Bad() != 0 {
		t.Fatalf("на пустом графе что-то посчиталось: %+v", rep)
	}
	if rep := g.Provenance(nil, 10, 1); rep.Checked != 0 {
		t.Fatal("без хранилища кусков проверка должна вернуть пустой отчёт")
	}
}

// Выборка связей равномерна по записям и воспроизводима (аудит 17.09.2026, S9).
func TestSampleEdgesUniformAndRepeatable(t *testing.T) {
	g, _ := graph(t)
	hub, _, _ := g.Entities().Add("Go", TypeTech)
	var leaves []uint32
	for i := 0; i < 50; i++ {
		id, _, _ := g.Entities().Add(fmt.Sprintf("понятие %d", i), TypeConcept)
		leaves = append(leaves, id)
		// У хаба 50 связей, у одиночной пары — одна.
		must(t, g.Edges().Add(Edge{Src: hub, Dst: id, Type: RelUses, Weight: 1, Evidence: ChunkKey{Doc: 1, Ord: uint32(i)}}))
	}
	must(t, g.Edges().Add(Edge{Src: leaves[0], Dst: leaves[1], Type: RelUses, Weight: 1, Evidence: ChunkKey{Doc: 2, Ord: 1}}))

	a := g.SampleEdges(20, 7)
	b := g.SampleEdges(20, 7)
	if len(a) != 20 || len(b) != 20 {
		t.Fatalf("размер выборки: %d и %d", len(a), len(b))
	}
	seen := map[ChunkKey]bool{}
	fromHub := 0
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("одно зерно дало разные выборки на месте %d", i)
		}
		if seen[a[i].Evidence] {
			t.Fatalf("связь попала в выборку дважды: %+v", a[i])
		}
		seen[a[i].Evidence] = true
		if a[i].Src == hub {
			fromHub++
		}
	}
	// 50 из 51 записи — у хаба; выборка «понятие → его связь» дала бы ему
	// около одной связи из двадцати.
	if fromHub < 18 {
		t.Fatalf("связей хаба в выборке %d из 20 — выборка не по записям связей", fromHub)
	}
	if all := g.SampleEdges(1000, 7); len(all) != 51 {
		t.Fatalf("выборка больше графа: %d, ожидалось все 51", len(all))
	}
}
