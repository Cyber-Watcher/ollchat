package ui

import (
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// buildTestGraph кладёт рядом с коллекцией крошечный граф: два понятия
// и связь между ними, подтверждённая первым куском первой книги.
func buildTestGraph(t *testing.T, coll *kb.Collection) {
	t.Helper()
	g, err := graph.Create(coll.Dir(), coll.Name(), coll.ChunkCount(), graph.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	goID, _, err := g.Entities().Add("goroutine", "понятие", "горутина")
	if err != nil {
		t.Fatal(err)
	}
	chID, _, err := g.Entities().Add("channel", "понятие")
	if err != nil {
		t.Fatal(err)
	}
	key := graph.ChunkKey{Doc: 0, Ord: 0}
	// По два упоминания на понятие: одноразовые в карту не берутся намеренно,
	// их в настоящем графе большинство и это шум разбора.
	for _, id := range []uint32{goID, chID} {
		for _, k := range []graph.ChunkKey{key, {Doc: 0, Ord: 1}} {
			g.Entities().Touch(id, true)
			if err := g.Mentions().Add(id, k); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := g.Edges().Add(graph.Edge{Src: goID, Dst: chID, Weight: 1, Evidence: key}); err != nil {
		t.Fatal(err)
	}
	if err := g.Progress().Mark(key, graph.MarkDone); err != nil {
		t.Fatal(err)
	}
	if err := g.Entities().SaveCounters(); err != nil {
		t.Fatal(err)
	}
	if err := g.Save(); err != nil {
		t.Fatal(err)
	}
}

// TestBooksQueryAddsEntityNames — вопрос дополняется именами понятий из графа.
//
// Это перевод вопроса на язык библиотеки: «дообучение» в книгах называется
// fine-tuning, и без имён понятий поиск по русскому вопросу цепляется
// за служебные слова.
func TestMixGatekeeper(t *testing.T) {
	m, books := kbTestModel(t)
	writeTestBook(t, books, "go.pdf", "goroutines and channels explained")
	drainJob(t, m, m.runCommand("/kb add go "+books))
	m.runCommand("/kb use go")

	coll, err := m.kbCollection("go")
	if err != nil {
		t.Fatal(err)
	}
	buildTestGraph(t, coll)

	// Модель с инструментами: карта подмешивается, цитат нет. Возможность
	// проверяется дважды — по объявлению и по устройству сборки, потому что
	// строка «tools» бывает ложной (DeepSeekFakeTools.md).
	m.modelCaps = []string{"completion", "tools"}
	m.modelRealTools = true
	mix := m.autoMix("как связаны goroutine и channel")
	if mix.Empty() || mix.Entities == 0 {
		t.Fatalf("карта понятий не подмешалась: %+v", mix)
	}
	if mix.Chunks != 0 {
		t.Fatalf("модели с инструментами подмешаны цитаты: %+v", mix)
	}
	if !strings.Contains(mix.Text, "goroutine") {
		t.Fatalf("в карте нет найденного понятия: %q", mix.Text)
	}
	if !strings.Contains(mixLine(mix), "граф") {
		t.Fatalf("строка под вопросом не говорит про граф: %q", mixLine(mix))
	}

	// Вопрос не о библиотеке — привратник не пускает ничего.
	if mix := m.autoMix("спасибо, это то что нужно"); !mix.Empty() {
		t.Fatalf("подмешано на постороннем вопросе: %q", mix.Text)
	}
	// Распоряжение о здешнем коде отсекается, даже если слова из него в графе
	// есть: замер на живом графе показал, что «сделай коммит» связывается
	// с понятием «коммит» из книг.
	if mix := m.autoMix("перепиши функцию про goroutine покороче"); !mix.Empty() {
		t.Fatalf("подмешано на распоряжении о коде: %q", mix.Text)
	}

	// Модель без инструментов сама ничего не дозапросит: к карте добавляются
	// выдержки, хотя /kb auto никто не включал.
	m.modelCaps = []string{"completion"}
	m.modelRealTools = false
	mix = m.autoMix("как связаны goroutine и channel")
	if mix.Chunks == 0 {
		t.Fatalf("модели без инструментов не подмешаны выдержки: %+v", mix)
	}
	if mix.Chunks > m.cfg.Mix.QuotesWithoutTools {
		t.Fatalf("выдержек больше заказанного: %d > %d", mix.Chunks, m.cfg.Mix.QuotesWithoutTools)
	}
	if !strings.Contains(mixLine(mix), "без инструментов") {
		t.Fatalf("строка под вопросом не объясняет цитаты: %q", mixLine(mix))
	}

	// Ложная строка «tools»: возможность объявлена, а вызывать модель не умеет —
	// выдержки всё равно нужны, иначе она ответит по памяти без ссылок.
	m.modelCaps = []string{"completion", "tools"}
	m.modelRealTools = false
	if mix := m.autoMix("как связаны goroutine и channel"); mix.Chunks == 0 {
		t.Fatalf("модели с ложной поддержкой инструментов не подмешаны выдержки: %+v", mix)
	}

	// Выключенный граф выключает и карту: у модели без инструментов остаются
	// только выдержки.
	m.runCommand("/graph auto off")
	mix = m.autoMix("как связаны goroutine и channel")
	if mix.Entities != 0 {
		t.Fatalf("карта подмешалась при /graph auto off: %+v", mix)
	}
	if mix.Chunks == 0 {
		t.Fatalf("выдержки пропали вместе с картой: %+v", mix)
	}
	// А привратник продолжает работать: посторонний вопрос по-прежнему пуст.
	if mix := m.autoMix("перепиши эту функцию покороче"); !mix.Empty() {
		t.Fatalf("подмешано на постороннем вопросе при /graph auto off: %q", mix.Text)
	}
}
