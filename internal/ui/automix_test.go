package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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

// graphClosed — закрыт ли граф: закрытый не принимает дозапись, буфер журнала
// упирается в закрытый файл. Открытому граф проверки дописывает одно
// упоминание — тестовому графу это безразлично.
func graphClosed(t *testing.T, g *graph.Graph) bool {
	t.Helper()
	if err := g.Mentions().Add(1, graph.ChunkKey{Doc: 1, Ord: 0}); err != nil {
		return true
	}
	return g.Mentions().Flush() != nil
}

// openModelGraph открывает граф коллекции и кладёт его в модель — так,
// как он лежит после первого вопроса.
func openModelGraph(t *testing.T, m *Model, coll *kb.Collection) *graph.Graph {
	t.Helper()
	g, err := graph.Open(coll.Dir(), coll.ChunkCount(), m.cfg.Graph.Rules())
	if err != nil {
		t.Fatal(err)
	}
	m.gr.open, m.gr.dir, m.gr.stamp = g, coll.Dir(), graphStamp(coll.Dir(), m.cfg.Graph.Name)
	return g
}

// Конец индексации закрывал граф модели, пока подмешивание считало им же
// в горутине команды (аудит 07.10.2026). Теперь граф отпускается, а закрывается,
// когда подмешивание вернётся — даже если вопрос к тому времени брошен.
func TestJobEndDoesNotCloseGraphUnderMixing(t *testing.T) {
	m, books := kbTestModel(t)
	m.cfg.KB.EmbedModel = "" // без эмбеддера подмешивание считается без сети
	writeTestBook(t, books, "go.pdf", "goroutines and channels explained")
	drainJob(t, m, m.runCommand("/kb add go "+books))
	m.runCommand("/kb use go")
	coll, err := m.kbCollection("go")
	if err != nil {
		t.Fatal(err)
	}
	buildTestGraph(t, coll)
	g := openModelGraph(t, m, coll)
	m.gr.autoOn = true
	gen := idleJob(t, m)

	cmd := m.send("как связаны goroutine и channel")
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || !m.mixing {
		t.Fatal("подготовка: подмешивание не ушло в фон")
	}
	mix := batch[0] // runMixCmd: считает графом модели

	m.Update(jobDoneMsg{gen: gen, p: kb.Progress{Done: true}})
	if graphClosed(t, g) {
		t.Fatal("граф закрыт под работающим подмешиванием")
	}
	if m.gr.open != nil {
		t.Error("модель держит граф, который велено закрыть")
	}

	// Человек передумал, а подмешивание вернулось позже.
	m.Update(keyPress("esc"))
	ready, ok := mix().(mixReadyMsg)
	if !ok {
		t.Fatal("подмешивание вернуло не mixReadyMsg")
	}
	m.Update(ready)
	if !graphClosed(t, g) {
		t.Fatal("граф не закрыт, когда подмешивание вернулось")
	}
}

// Отпущенный граф закрывается, когда его вернёт последний, кто им считает
// (вопрос и /mix show могут считать одновременно), а граф, который модель
// держит по-прежнему, после возврата остаётся открытым.
func TestLentGraphClosesOnLastReturn(t *testing.T) {
	m, books := kbTestModel(t)
	writeTestBook(t, books, "go.pdf", "goroutines and channels explained")
	drainJob(t, m, m.runCommand("/kb add go "+books))
	coll, err := m.kbCollection("go")
	if err != nil {
		t.Fatal(err)
	}
	buildTestGraph(t, coll)

	g := openModelGraph(t, m, coll)
	m.lendGraph(g)
	m.lendGraph(g)
	m.closeGraph() // решение «y» в окне разбора, /kb use и прочие
	m.returnGraph(g)
	if graphClosed(t, g) {
		t.Fatal("граф закрыт, хотя им ещё считают")
	}
	m.returnGraph(g)
	if !graphClosed(t, g) {
		t.Fatal("отпущенный граф не закрыт после последнего возврата")
	}

	kept := openModelGraph(t, m, coll)
	m.lendGraph(kept)
	m.returnGraph(kept)
	if graphClosed(t, kept) {
		t.Fatal("закрыт граф, который модель держит")
	}
	m.closeGraph()
	if !graphClosed(t, kept) {
		t.Fatal("свободный граф не закрыт")
	}
}
