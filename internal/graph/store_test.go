package graph

import (
	"os"
	"path/filepath"
	"testing"
)

func graph(t *testing.T) (*Graph, string) {
	t.Helper()
	dir := collection(t)
	g, err := Create(dir, "books", 1000, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	return g, dir
}

// Сущности заводятся и находятся.
func TestEntitiesAddedAndFound(t *testing.T) {
	g, _ := graph(t)
	e := g.Entities()

	id, isNew, err := e.Add("KV-кэш", TypeConcept, "KV cache")
	if err != nil || !isNew || id != 1 {
		t.Fatalf("Add = %d, %v, %v", id, isNew, err)
	}
	// То же понятие в другом написании — та же сущность, а не вторая.
	id2, isNew2, err := e.Add("kv кэш", TypeConcept)
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id || isNew2 {
		t.Errorf("«kv кэш» завёл вторую сущность: %d, новая=%v", id2, isNew2)
	}
	// Синоним тоже ведёт к ней.
	if got, ok := e.Lookup("KV cache"); !ok || got.ID != id {
		t.Errorf("поиск по синониму: %+v, %v", got, ok)
	}
	if e.Count() != 1 {
		t.Errorf("сущностей = %d, ожидалась одна", e.Count())
	}
}

// Неизвестный тип становится понятием.
func TestUnknownTypeBecomesConcept(t *testing.T) {
	g, _ := graph(t)
	id, _, err := g.Entities().Add("Kubernetes", "какой-то новый тип")
	if err != nil {
		t.Fatal(err)
	}
	ent, _ := g.Entities().Get(id)
	if ent.Type != TypeConcept {
		t.Errorf("тип = %q, ожидалось %q", ent.Type, TypeConcept)
	}
}

// Реестр сущностей переживает перезапуск.
func TestEntityRegistrySurvivesRestart(t *testing.T) {
	g, dir := graph(t)
	id, _, err := g.Entities().Add("вытеснение контекста", TypeConcept, "context eviction")
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}

	g2, err := Open(dir, 1000, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g2.Close()
	ent, ok := g2.Entities().Get(id)
	if !ok || ent.Name != "вытеснение контекста" {
		t.Fatalf("сущность потеряна: %+v", ent)
	}
	if _, ok := g2.Entities().Lookup("context eviction"); !ok {
		t.Error("синоним потерян при перезапуске")
	}
}

// Упоминания и связи.
func TestMentionsAndEdges(t *testing.T) {
	g, _ := graph(t)
	e := g.Entities()
	kv, _, _ := e.Add("KV-кэш", TypeConcept)
	ctx, _, _ := e.Add("контекстное окно", TypeConcept)

	chunk := ChunkKey{Doc: 12, Ord: 37}
	if err := g.Mentions().Add(kv, chunk); err != nil {
		t.Fatal(err)
	}
	if err := g.Mentions().Add(ctx, chunk); err != nil {
		t.Fatal(err)
	}
	// Повтор того же упоминания не должен раздваивать выдачу.
	if err := g.Mentions().Add(kv, chunk); err != nil {
		t.Fatal(err)
	}

	if got := g.Mentions().Of(kv); len(got) != 1 || got[0] != chunk {
		t.Errorf("упоминания = %v", got)
	}
	if got := g.Mentions().In(chunk); len(got) != 2 {
		t.Errorf("сущностей в куске = %v", got)
	}

	if err := g.Edges().Add(Edge{Src: ctx, Dst: kv, Type: RelUses, Weight: 1, Evidence: chunk}); err != nil {
		t.Fatal(err)
	}
	if err := g.Edges().Add(Edge{Src: ctx, Dst: kv, Type: RelAffects, Weight: 2, Evidence: ChunkKey{12, 38}}); err != nil {
		t.Fatal(err)
	}
	neighbors := g.Edges().Neighbors(ctx)
	if len(neighbors) != 1 || neighbors[0].ID != kv {
		t.Fatalf("соседи = %+v", neighbors)
	}
	// Одна связь, подтверждённая дважды, — это одна связь весом три.
	if neighbors[0].Weight != 3 || neighbors[0].Count != 2 || len(neighbors[0].Types) != 2 {
		t.Errorf("слияние повторов неверно: %+v", neighbors[0])
	}
	if got := g.Edges().Between(kv, ctx); len(got) != 2 {
		t.Errorf("связей между понятиями = %d, ожидалось 2", len(got))
	}
}

// Петли и связи без опоры отбрасываются.
func TestLoopsAndUnsupportedEdgesDropped(t *testing.T) {
	g, _ := graph(t)
	a, _, _ := g.Entities().Add("Go", TypeTech)
	if err := g.Edges().Add(Edge{Src: a, Dst: a, Type: RelIs, Evidence: ChunkKey{1, 1}}); err != nil {
		t.Fatal(err)
	}
	if err := g.Edges().Add(Edge{Src: a, Dst: 0, Type: RelIs}); err != nil {
		t.Fatal(err)
	}
	if g.Edges().Count() != 0 {
		t.Errorf("записано связей = %d, ожидался ноль", g.Edges().Count())
	}
}

// Сборка идёт часами и обрывается: оборванная запись в конце журнала не должна
// уносить с собой всё, что записано до неё.
func TestTruncatedLogTailStillReads(t *testing.T) {
	g, dir := graph(t)
	id, _, _ := g.Entities().Add("Go", TypeTech)
	for ord := uint32(1); ord <= 4; ord++ {
		if err := g.Mentions().Add(id, ChunkKey{Doc: 3, Ord: ord}); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}

	// Дописываем половину записи — так выглядит файл после внезапной остановки.
	path := filepath.Join(dir, DirName, mentionsFile)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte{1, 0, 0, 0, 7}); err != nil {
		t.Fatal(err)
	}
	f.Close()

	g2, err := Open(dir, 1000, Rules{})
	if err != nil {
		t.Fatalf("журнал с оборванным хвостом не открылся: %v", err)
	}
	defer g2.Close()
	if got := g2.Mentions().Count(); got != 4 {
		t.Errorf("уцелело упоминаний = %d, ожидалось 4", got)
	}
}

// Отметки разбора переживают перезапуск.
func TestParseMarksSurviveRestart(t *testing.T) {
	g, dir := graph(t)
	if err := g.Progress().Mark(ChunkKey{1, 1}, MarkDone); err != nil {
		t.Fatal(err)
	}
	if err := g.Progress().Mark(ChunkKey{1, 2}, MarkEmpty); err != nil {
		t.Fatal(err)
	}
	if err := g.Progress().Mark(ChunkKey{1, 3}, MarkSkipped); err != nil {
		t.Fatal(err)
	}
	g.Close()

	g2, err := Open(dir, 1000, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g2.Close()
	if !g2.Progress().Done(ChunkKey{1, 2}) {
		t.Error("отметка потеряна после перезапуска")
	}
	if g2.Progress().Done(ChunkKey{1, 9}) {
		t.Error("неразобранный кусок назван разобранным")
	}
	done, empty, skipped := g2.Progress().Counts()
	if done != 1 || empty != 1 || skipped != 1 {
		t.Errorf("разбор по признакам = %d/%d/%d", done, empty, skipped)
	}
}

// Нормализация имён.
func TestNameNormalization(t *testing.T) {
	pairs := map[string]string{
		"KV-кэш":         "kv кэш",
		"  KV   кэш.  ":  "kv кэш",
		"«Go»":           "go",
		"context_window": "context window",
		"Вытеснение":     "вытеснение",
		"жёсткий":        "жесткий",
	}
	for in, want := range pairs {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, ожидалось %q", in, got, want)
		}
	}
}

// Типы связей разбираются.
func TestRelationTypesParsed(t *testing.T) {
	if RelType("использует") != RelUses || RelType("uses") != RelUses {
		t.Error("«использует» разобрано неверно")
	}
	if RelType("что-то своё") != RelRelated {
		t.Error("незнакомый тип должен становиться «связано»")
	}
	if RelName(RelOpposed) != "противопоставлено" {
		t.Errorf("печать типа = %q", RelName(RelOpposed))
	}
}

// Синоним не имеет права отбирать имя у другой сущности.
//
// Модель постоянно кладёт в синонимы родственные понятия: «искусственный
// интеллект» получил синоним «ChatGPT», «глубокие нейронные сети» — «машинное
// обучение». Если такому синониму позволить занять чужой ключ, два разных
// понятия сливаются в одно и граф начинает уверенно врать.
func TestAliasDoesNotStealName(t *testing.T) {
	g, _ := graph(t)
	e := g.Entities()

	mlID, _, _ := e.Add("машинное обучение", TypeConcept)
	// Модель заводит другое понятие и ошибочно называет «машинное обучение»
	// его синонимом.
	dlID, isNew, err := e.Add("глубокое обучение", TypeConcept, "машинное обучение")
	if err != nil {
		t.Fatal(err)
	}
	if !isNew || dlID == mlID {
		t.Fatalf("понятия слились: %d и %d", dlID, mlID)
	}
	// Поиск по «машинному обучению» обязан приводить к нему самому.
	if got, ok := e.Lookup("машинное обучение"); !ok || got.ID != mlID {
		t.Errorf("ключ отобран: поиск дал %+v, ожидалось %d", got, mlID)
	}
}

// А своё, ещё не занятое написание синоним занимать должен: ради этого он и есть.
func TestLooseSpellingBecomesAlias(t *testing.T) {
	g, _ := graph(t)
	e := g.Entities()
	id, _, err := e.Add("KV-кэш", TypeConcept, "KV cache")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := e.Lookup("kv cache"); !ok || got.ID != id {
		t.Errorf("поиск по синониму не работает: %+v", got)
	}
}

// Число синонимов ограничено.
func TestAliasCountLimited(t *testing.T) {
	g, _ := graph(t)
	many := []string{"а1", "а2", "а3", "а4", "а5", "а6", "а7", "а8", "а9"}
	id, _, err := g.Entities().Add("тензор", TypeConcept, many...)
	if err != nil {
		t.Fatal(err)
	}
	ent, _ := g.Entities().Get(id)
	if len(ent.Aliases) > maxAliases {
		t.Errorf("синонимов %d, предел %d", len(ent.Aliases), maxAliases)
	}
}

// Синоним годится ключом поиска, только если он и вправду другое написание
// того же понятия. Замерено на живом графе: у «RAG» в синонимах оказалась
// «векторная база данных» — найдя RAG по такому ключу, поиск ответил бы не о том.
func TestAliasIsKeyOnlyIfSimilar(t *testing.T) {
	goodPairs := [][2]string{
		{"kv кэш", "kv cache"},                     // общее слово
		{"rag", "retrieval augmented generation"},  // сокращение по первым буквам
		{"retrieval augmented generation", "rag"},  // и наоборот
		{"kv кэш", "kv cache"},                     // одно слово переведено
		{"контекстное окно", "контекстное window"}, // и так тоже
	}
	for _, pair := range goodPairs {
		if !usableAlias(pair[0], pair[1]) {
			t.Errorf("годный синоним отвергнут: %q ← %q", pair[0], pair[1])
		}
	}
	badPairs := [][2]string{
		{"rag", "векторная база данных"},
		{"искусственный интеллект", "chatgpt"},
		{"глубокое обучение", "машинное обучение"},
		{"lambda выражение", "функция"},
	}
	for _, pair := range badPairs {
		if usableAlias(pair[0], pair[1]) {
			t.Errorf("чужое понятие принято за синоним: %q ← %q", pair[0], pair[1])
		}
	}
}

// Чужой синоним не ищется.
func TestForeignAliasNotFound(t *testing.T) {
	g, _ := graph(t)
	e := g.Entities()
	if _, _, err := e.Add("RAG", TypeTech, "Retrieval-Augmented Generation", "векторная база данных"); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Lookup("Retrieval Augmented Generation"); !ok {
		t.Error("настоящее сокращение перестало искаться")
	}
	if got, ok := e.Lookup("векторная база данных"); ok {
		t.Errorf("поиск по чужому понятию нашёл %q", got.Name)
	}
}
