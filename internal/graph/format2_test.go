package graph

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Рабочий граф формата 1 собран промптом 1a2fa975 (паспорт на 04.09.2026),
// и ночная сборка продолжает им же. Любая правка prompts/extract.txt,
// summary.txt или findings.txt меняет это число, и сборка откажется
// продолжаться «другим промптом». Формат 2 живёт своим файлом extract2.txt
// именно затем, чтобы его ужесточение не трогало это значение.
func TestPromptIDOfFormat1Frozen(t *testing.T) {
	const frozen = "1a2fa975"
	if PromptID != frozen {
		t.Fatalf("PromptID рабочего графа изменился: %s вместо %s — правка промптов формата 1 остановит ночную сборку", PromptID, frozen)
	}
	if PromptIDV2 == PromptID {
		t.Fatal("промпт формата 2 совпал с промптом формата 1: ужесточение потеряно")
	}
	if PromptIDFor(1) != PromptID || PromptIDFor(FormatV2) != PromptIDV2 {
		t.Fatal("PromptIDFor выбирает не тот промпт")
	}
	if SystemPromptFor(1) != SystemPrompt || SystemPromptFor(FormatV2) != SystemPromptV2 {
		t.Fatal("SystemPromptFor выбирает не тот промпт")
	}
}

// Формат 2 заводится только у именованного графа: в каталог `graph`
// рабочего графа он не попадает никогда (решение владельца 04.09.2026).
func TestFormat2RequiresName(t *testing.T) {
	dir := collection(t)
	if _, err := Create(dir, "books", 100, Rules{Format: FormatV2}); err == nil {
		t.Fatal("формат 2 без имени графа должен отвергаться")
	}
	if _, err := os.Stat(filepath.Join(dir, DirName)); !os.IsNotExist(err) {
		t.Fatal("отказ обязан ничего не создавать в каталоге рабочего графа")
	}
	if _, err := Create(dir, "books", 100, Rules{Name: "lab", Format: 9}); err == nil {
		t.Fatal("неизвестный формат должен отвергаться")
	}
	// Умолчание не изменилось: без Format — формат 1 и промпт формата 1.
	g, err := Create(dir, "books", 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if g.Meta().Version != 1 || g.Meta().PromptID != PromptID || g.Aliases() != nil {
		t.Fatalf("рабочий граф изменился: %+v", g.Meta())
	}
}

// Опытный граф формата 2: журнал синонимов пишется с источником, читается
// после переоткрытия, а рабочий граф формата 1 рядом его не получает.
func TestFormat2WritesAliasesWithSource(t *testing.T) {
	dir := collection(t)
	lab, err := CreateKind(dir, "books", 100, KindExperimental, "схема 2", Rules{Name: "lab", Format: FormatV2})
	if err != nil {
		t.Fatal(err)
	}
	if lab.Meta().Version != FormatV2 || lab.Meta().PromptID != PromptIDV2 {
		t.Fatalf("паспорт формата 2: %+v", lab.Meta())
	}
	if lab.Aliases() == nil {
		t.Fatal("у формата 2 обязан быть журнал синонимов")
	}
	key := ChunkKey{Doc: 7, Ord: 3}
	facts := Facts{Entities: []FactEntity{
		{Name: "горутина", Type: "понятие", Aliases: []string{"goroutine", "Goroutine"}},
		{Name: "канал", Type: "понятие"},
	}}
	if _, err := writeFacts(context.Background(), lab, key, facts, nil); err != nil {
		t.Fatal(err)
	}
	ent, ok := lab.Entities().Lookup("горутина")
	if !ok {
		t.Fatal("понятие не заведено")
	}
	id := ent.ID
	recs := lab.Aliases().Of(id)
	if len(recs) != 2 || recs[0].Norm != Normalize("goroutine") || recs[0].Chunk != key || recs[0].ID != 1 {
		t.Fatalf("вхождения синонима: %+v", recs)
	}
	if w := lab.Aliases().Where("GOROUTINE"); len(w) != 2 || w[1].Entity != id {
		t.Fatalf("поиск по написанию: %+v", w)
	}
	if err := lab.Close(); err != nil {
		t.Fatal(err)
	}

	// После переоткрытия — то же самое, номера записей прежние.
	lab, err = Open(dir, 100, Rules{Name: "lab"})
	if err != nil {
		t.Fatal(err)
	}
	defer lab.Close()
	if got := lab.Aliases().Count(); got != 2 {
		t.Fatalf("после переоткрытия %d вхождений, ожидалось 2", got)
	}
	if rec, ok := lab.Aliases().Get(2); !ok || rec.Entity != id || rec.Chunk != key {
		t.Fatalf("запись 2: %+v, %v", rec, ok)
	}

	// Рабочий граф рядом — формата 1, без журнала и без файла.
	prod, err := Create(dir, "books", 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer prod.Close()
	if _, err := writeFacts(context.Background(), prod, key, facts, nil); err != nil {
		t.Fatal(err)
	}
	if prod.Aliases() != nil {
		t.Fatal("у формата 1 не должно быть журнала синонимов")
	}
	if _, err := os.Stat(filepath.Join(dir, DirName, aliasesFile)); !os.IsNotExist(err) {
		t.Fatal("в каталоге рабочего графа появился aliases.log")
	}
}

// Оборванная запись в хвосте журнала (обрыв сборки) не мешает чтению: всё
// до неё годится, как у mentions.log.
func TestAliasesTruncatedTailTolerated(t *testing.T) {
	dir := t.TempDir()
	a, err := openAliases(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Add(5, ChunkKey{Doc: 1, Ord: 2}, "Tool Call"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Add(5, ChunkKey{Doc: 1, Ord: 3}, "toolcall"); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, aliasesFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw[:len(raw)-3], 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := openAliases(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.Count() != 1 || b.Of(5)[0].Norm != Normalize("Tool Call") {
		t.Fatalf("после обрыва: %d записей, %+v", b.Count(), b.Of(5))
	}
}

// Отчёт по журналу синонимов: считает вхождения, пары и виды; чужое имя
// помечает; у графа формата 1 отчёта нет.
func TestAliasReportCountsKindsAndClashes(t *testing.T) {
	dir := t.TempDir()
	g, err := CreateKind(dir, "books", 100, KindExperimental, "проба", Rules{Name: "lab", Format: FormatV2})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	rag, _, err := g.Entities().Add("RAG", TypeConcept)
	if err != nil {
		t.Fatal(err)
	}
	vdb, _, err := g.Entities().Add("векторная база данных", TypeConcept)
	if err != nil {
		t.Fatal(err)
	}
	al := g.Aliases()
	for i, a := range []string{"Retrieval-Augmented Generation", "Retrieval-Augmented Generation", "генерация с поиском", "векторная база данных"} {
		if _, err := al.Add(rag, ChunkKey{Doc: 1, Ord: uint32(i)}, a); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := al.Add(vdb, ChunkKey{Doc: 2, Ord: 1}, "vector DB"); err != nil {
		t.Fatal(err)
	}
	r, ok := g.AliasReportOf(3)
	if !ok {
		t.Fatal("у графа формата 2 отчёт обязан быть")
	}
	if r.Records != 5 || r.Chunks != 5 || r.Pairs != 4 || r.Entities != 2 {
		t.Fatalf("счёт: %+v", r)
	}
	if r.Acronyms != 1 || r.Translations != 2 || r.Other != 0 || r.Clashes != 1 {
		t.Fatalf("виды: аббревиатур %d, переводов %d, иных %d, чужих имён %d", r.Acronyms, r.Translations, r.Other, r.Clashes)
	}
	if len(r.Top) != 3 || r.Top[0].Count != 2 || r.Top[0].Entity != "RAG" {
		t.Fatalf("верхушка: %+v", r.Top)
	}
	clashSeen := false
	for _, p := range r.Top {
		if p.Clash && p.Alias == Normalize("векторная база данных") {
			clashSeen = true
		}
	}
	if !clashSeen {
		t.Fatalf("чужое имя не помечено: %+v", r.Top)
	}

	one, err := Create(t.TempDir(), "books", 10, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	if _, ok := one.AliasReportOf(3); ok {
		t.Fatal("у графа формата 1 журнала нет — отчёта быть не должно")
	}
}
