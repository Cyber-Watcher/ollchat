package mixer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/find"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	gmaint "github.com/Cyber-Watcher/ollchat/internal/graph/maint"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbremote"
	"github.com/Cyber-Watcher/ollchat/internal/kbserve"
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
)

// Подмес через службу общей библиотеки (этап 121, А2, А4, А6).

// remoteFixture — коллекция и граф «горутина → канал → рантайм» без прямой
// связи между крайними: на таком графе есть и карта, и цепочка.
func remoteFixture(t *testing.T) (*kb.Base, *kb.Collection, *graph.Graph) {
	t.Helper()
	base, err := kb.OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	coll, err := base.Create("books", "проверочная коллекция")
	if err != nil {
		t.Fatal(err)
	}
	g, err := graph.Create(coll.Dir(), "books", 100, graph.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	add := func(name string) uint32 {
		id, _, err := g.Entities().Add(name, graph.TypeConcept)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	a, mid, b := add("горутина"), add("канал"), add("рантайм")
	for _, e := range []graph.Edge{
		{Src: a, Dst: mid, Type: graph.RelUses, Weight: 2, Evidence: graph.ChunkKey{Doc: 1, Ord: 1}},
		{Src: mid, Dst: b, Type: graph.RelPart, Weight: 2, Evidence: graph.ChunkKey{Doc: 1, Ord: 2}},
	} {
		if err := g.Edges().Add(e); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []uint32{a, mid, b} {
		for ord := uint32(1); ord <= 3; ord++ {
			if err := g.Mentions().Add(id, graph.ChunkKey{Doc: 1, Ord: ord}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return base, coll, g
}

// fakeMap — служба, считающая карту по тому же графу тем же поиском, что
// подмешивание по своему графу, и цепочку тем же find.Chain
// (gmaint.GraphService.Search устроен так же).
type fakeMap struct {
	g   *graph.Graph
	err error

	calls               int
	coll                string
	entities, neighbors int
	deadline            time.Time     // срок, с которым подмес ждал карту
	queued              time.Duration // «ждал в очереди» в ответе службы
}

func (f *fakeMap) GraphMap(ctx context.Context, coll, query string, entities, neighbors int) (kbremote.GraphMap, error) {
	f.calls++
	f.coll, f.entities, f.neighbors = coll, entities, neighbors
	f.deadline, _ = ctx.Deadline()
	if f.err != nil {
		return kbremote.GraphMap{}, f.err
	}
	res := f.g.Search(query, graph.SearchOpts{TopEntities: entities, TopNeighbors: neighbors,
		TopChunks: 1, MinMentions: 2})
	return kbremote.GraphMap{Result: res, Chain: find.Chain(f.g, res.Entities, res.Relations, find.Opts{}),
		Queued: f.queued}, nil
}

// errDown — так kbremote сообщает об отказе в соединении: адрес в тексте,
// метка kb.ErrUnreachable внутри.
var errDown = fmt.Errorf("библиотека http://kb.example:8377 недоступна: %w", kb.ErrUnreachable)

const pairQuestion = "как связаны горутина и рантайм"

func remoteSettings() Settings {
	return Settings{Entities: 6, Neighbors: 4, Chain: true, Collection: "books"}
}

// Карта от службы подмешивается так же, как по своему графу: тот же текст
// и та же цепочка, а размер карты — числа клиента. Числа уходят службе.
func TestServiceMapSendsClientNumbers(t *testing.T) {
	_, coll, g := remoteFixture(t)
	local := Build(pairQuestion, Deps{Coll: coll, Graph: g, GraphOn: true}, remoteSettings())
	if local.Empty() || local.Chain == 0 {
		t.Fatalf("подготовка: по своему графу нет карты с цепочкой: %+v", local)
	}

	fm := &fakeMap{g: g}
	remote := Build(pairQuestion, Deps{Coll: coll, Map: fm, GraphOn: true}, remoteSettings())
	if remote.Text != local.Text {
		t.Errorf("подмес через службу разошёлся с подмесом по своему графу:\n--- служба ---\n%s\n--- свой граф ---\n%s",
			remote.Text, local.Text)
	}
	if remote.Chain != local.Chain || remote.Entities != local.Entities {
		t.Errorf("цепочка или понятия разошлись: служба %+v, свой граф %+v", remote, local)
	}
	if remote.Remote != "" {
		t.Errorf("служба ответила, а заметка о ней есть: %q", remote.Remote)
	}
	if fm.entities != 6 || fm.neighbors != 4 || fm.coll != "books" {
		t.Errorf("службе ушло: коллекция %q, понятий %d, связей %d; ждали books, 6, 4",
			fm.coll, fm.entities, fm.neighbors)
	}
}

// Карта через НАСТОЯЩУЮ службу (kbserve + gmaint.GraphService за httptest)
// и настоящий клиент kbremote совпадает с картой по своим файлам — с годами
// связей (mix.relation_years, умолчание конфига). Прежде этот тест подставлял
// вместо службы *kb.Collection и шёл с выключенными годами — и не видел, что
// через службу «свежайшее N г.» пропадает (аудит 23.10.2026, находка 11).
func TestServiceMapMatchesOwnGraph(t *testing.T) {
	base, coll, g := datedFixture(t)
	s := remoteSettings()
	s.RelationYears = true

	local := Build(pairQuestion, Deps{Coll: coll, Graph: g, GraphOn: true}, s)
	if local.Chain == 0 || !strings.Contains(local.Text, "свежайшее 2021 г.") ||
		!strings.Contains(local.Text, "свежайшее 2019 г.") {
		t.Fatalf("подготовка: по своим файлам нет карты с цепочкой и годами 2021 и 2019:\n%s", local.Text)
	}

	client := serviceClient(t, base, nil, 0)
	src, err := kb.Fallback{Remote: client}.Source("books")
	if err != nil {
		t.Fatal(err)
	}
	remote := Build(pairQuestion, Deps{Coll: src, Map: client, GraphOn: true}, s)
	if remote.Text != local.Text {
		t.Errorf("карта через службу разошлась с картой по своим файлам:\n--- служба ---\n%s\n--- свои файлы ---\n%s",
			remote.Text, local.Text)
	}
	if remote.Remote != "" {
		t.Errorf("служба ответила, а заметка о ней есть: %q", remote.Remote)
	}
}

// Годы связей у клиента прежней сборки и у службы прежней сборки: поля нет —
// у связей нет года, остальное то же (совместимость в обе стороны).
func TestServiceMapWithoutYears(t *testing.T) {
	_, coll, g := remoteFixture(t)
	s := remoteSettings()
	s.RelationYears = true
	out := Build(pairQuestion, Deps{Coll: coll, Map: &fakeMap{g: g}, GraphOn: true}, s)
	if out.Empty() || strings.Contains(out.Text, "свежайшее") {
		t.Errorf("служба без годов: карта пуста или год взялся ниоткуда:\n%s", out.Text)
	}
}

// Срок карты — kb.server_timeout клиента (Settings.ServiceTimeout), а не
// постоянная в 30 с (аудит 23.10.2026, находка 12).
func TestServiceMapTimeoutFromSettings(t *testing.T) {
	_, coll, g := remoteFixture(t)
	s := remoteSettings()
	s.ServiceTimeout = 3 * time.Minute
	fm := &fakeMap{g: g}
	started := time.Now()
	Build(pairQuestion, Deps{Coll: coll, Map: fm, GraphOn: true}, s)
	if left := fm.deadline.Sub(started); left < 2*time.Minute {
		t.Errorf("карту ждали %v при kb.server_timeout = 3m", left.Round(time.Second))
	}
}

// Служба дождалась места в очереди — подмес говорит, сколько ждал.
func TestServiceMapQueueNote(t *testing.T) {
	_, coll, g := remoteFixture(t)
	out := Build(pairQuestion, Deps{Coll: coll, Map: &fakeMap{g: g, queued: 1500 * time.Millisecond}, GraphOn: true},
		remoteSettings())
	if !strings.Contains(out.Remote, "ждал в очереди 1500 мс") {
		t.Errorf("заметка о службе молчит об очереди: %q", out.Remote)
	}
}

// Занятая служба (очередь полна) — не недоступная: свой граф не открывается,
// книги не уходят на свои файлы, заметка говорит об очереди (решение
// владельца 23.10.2026). Прежде срок истекал, ошибка шла с меткой
// kb.ErrUnreachable, и клиент открывал свой граф.
func TestBusyServiceDoesNotFallBack(t *testing.T) {
	base, coll, _ := datedFixture(t)
	hold := &holdingGraph{entered: make(chan struct{}, 1), release: make(chan struct{})}
	defer close(hold.release)
	client := serviceClient(t, base, hold, 2*time.Second)

	// Единственное место занято чужим запросом карты.
	go func() { _, _, _ = client.GraphSearch(context.Background(), "books", "занять", 0, 0) }()
	<-hold.entered

	opened := 0
	lib := kb.Fallback{Remote: client, Local: base}
	src, err := lib.Source("books")
	if err != nil {
		t.Fatal(err)
	}
	s := remoteSettings()
	s.TopK = 3
	out := Build(pairQuestion, Deps{Coll: src, Map: client, GraphOn: true, BooksOn: true,
		LocalGraph: func() (*graph.Graph, func(), error) {
			opened++
			return nil, nil, errors.New("свой граф")
		}}, s)
	if opened != 0 {
		t.Errorf("занятая служба сочтена недоступной: свой граф открывали %d раз; заметка %q", opened, out.Remote)
	}
	if !strings.Contains(out.Remote, "очередь службы полна") {
		t.Errorf("заметка молчит об очереди: %q", out.Remote)
	}
	if strings.Contains(out.Remote, kb.FallbackNote) || kb.Fell(src) {
		t.Errorf("книги ушли на свои файлы при занятой службе: %q", out.Remote)
	}
	_ = coll
}

// holdingGraph — граф службы, который держит каждый запрос карты, пока тест
// не отпустит: так единственное место в очереди службы занято.
type holdingGraph struct {
	entered chan struct{}
	release chan struct{}
}

func (h *holdingGraph) Tool(context.Context, string, string, map[string]any) (string, error) {
	return "", nil
}

func (h *holdingGraph) Search(ctx context.Context, _ kbserve.GraphSearchRequest) (kbserve.GraphSearchResponse, error) {
	h.entered <- struct{}{}
	select {
	case <-h.release:
	case <-ctx.Done():
	}
	return kbserve.GraphSearchResponse{}, nil
}

func (h *holdingGraph) Graph(string) (*graph.Graph, graph.NeighborRank, func(), error) {
	return nil, graph.NeighborRank{}, nil, errors.New("графа нет")
}

// serviceClient поднимает службу над base (kbserve за httptest) и отдаёт
// настоящего клиента kbremote. gs — граф службы; nil — gmaint.GraphService
// без эмбеддера, как у службы без смыслового входа. Одно место для тяжёлых
// запросов: занятость службы в тестах — одним держащим запросом.
func serviceClient(t *testing.T, base *kb.Base, gs kbserve.GraphServer, timeout time.Duration) *kbremote.Client {
	t.Helper()
	if gs == nil {
		cfg := config.Default()
		cfg.KB.EmbedModel = "" // без эмбеддера: в сеть тест не ходит
		cfg.Mix.Entities, cfg.Mix.Neighbors = 6, 4
		gs = &gmaint.GraphService{Cfg: cfg, Base: base}
	}
	srv := httptest.NewServer(kbserve.Handler(kbserve.Opts{Base: base, Graph: gs, HeavySlots: 1}))
	t.Cleanup(srv.Close)
	client, err := kbremote.New(kbremote.Opts{URL: srv.URL, Timeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// datedFixture — коллекция из двух настоящих книг (2019 и 2021 годов по имени
// файла) и граф «горутина → канал → рантайм» над их кусками: связь
// «горутина → канал» подтверждена обеими книгами (свежайшее 2021), «канал →
// рантайм» — только старой (свежайшее 2019), и кусок старой книги
// подтверждает обе связи.
func datedFixture(t *testing.T) (*kb.Base, *kb.Collection, *graph.Graph) {
	t.Helper()
	root := t.TempDir()
	books := filepath.Join(root, "books")
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	writePDF(t, filepath.Join(books, "goroutines-2019.pdf"), "goroutines channels runtime scheduler")
	writePDF(t, filepath.Join(books, "channels-2021.pdf"), "goroutines channels select statement")
	base, err := kb.OpenBase(filepath.Join(root, "kb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	coll, err := base.Create("books", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coll.Add(context.Background(), []string{books}, kb.IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := coll.RefreshYears(context.Background(), 1<<20, false, nil); err != nil {
		t.Fatal(err)
	}
	byYear := map[int]graph.ChunkKey{}
	var keys []graph.ChunkKey
	if err := coll.EachChunkRef(kb.ChunkFilter{}, func(c kb.ChunkRef) error {
		k := graph.ChunkKey{Doc: c.Doc, Ord: c.Ord}
		keys = append(keys, k)
		if _, ok := byYear[c.Book.Year]; !ok {
			byYear[c.Book.Year] = k
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	old, fresh := byYear[2019], byYear[2021]
	if len(byYear) != 2 || old == fresh {
		t.Fatalf("подготовка: годы книг по именам файлов не проставились: %v", byYear)
	}

	g, err := graph.Create(coll.Dir(), coll.Name(), coll.ChunkCount(), graph.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	add := func(name string) uint32 {
		id, _, err := g.Entities().Add(name, graph.TypeConcept)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	a, mid, b := add("горутина"), add("канал"), add("рантайм")
	for _, e := range []graph.Edge{
		{Src: a, Dst: mid, Type: graph.RelUses, Weight: 2, Evidence: old},
		{Src: a, Dst: mid, Type: graph.RelUses, Weight: 2, Evidence: fresh},
		{Src: mid, Dst: b, Type: graph.RelPart, Weight: 2, Evidence: old},
	} {
		if err := g.Edges().Add(e); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []uint32{a, mid, b} {
		for _, k := range keys {
			g.Entities().Touch(id, true)
			if err := g.Mentions().Add(id, k); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, k := range keys {
		if err := g.Progress().Mark(k, graph.MarkDone); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.Entities().SaveCounters(); err != nil {
		t.Fatal(err)
	}
	if err := g.Save(); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	g, err = graph.Open(coll.Dir(), coll.ChunkCount(), graph.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	return base, coll, g
}

// writePDF пишет одностраничный PDF с текстом: индексатор читает его как
// настоящую книгу.
func writePDF(t *testing.T, path, text string) {
	t.Helper()
	body := text + ". " + strings.Repeat("plain sentence of book text about the subject. ", 12)
	content := "BT /F1 12 Tf 50 700 Td (" + body + ") Tj ET"
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /Resources << /Font << /F1 5 0 R >> >> >>",
		"<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var b strings.Builder
	b.WriteString("%PDF-1.7\n")
	for i, o := range objs {
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	b.WriteString("trailer\n<< /Root 1 0 R >>\n%%EOF\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Служба не ответила — карту считает свой граф, заметка говорит об этом,
// граф отпускается в конце подмеса (решение владельца, А4).
func TestServiceDownUsesOwnGraph(t *testing.T) {
	_, coll, g := remoteFixture(t)
	opened, released := 0, 0
	out := Build(pairQuestion, Deps{Coll: coll, Map: &fakeMap{err: errDown}, GraphOn: true,
		LocalGraph: func() (*graph.Graph, func(), error) {
			opened++
			return g, func() { released++ }, nil
		}}, remoteSettings())
	if out.Entities == 0 || out.Chain == 0 {
		t.Fatalf("при недоступной службе карта по своему графу не подмешалась: %+v", out)
	}
	if out.Remote != kb.FallbackNote {
		t.Errorf("заметка %q, ждали %q", out.Remote, kb.FallbackNote)
	}
	if opened != 1 || released != 1 {
		t.Errorf("свой граф взят %d раз, отпущен %d; ждали по разу", opened, released)
	}
}

// Отказ службы по смыслу — не повод искать у себя: свой граф не трогается,
// карты нет, а человеку видна причина.
func TestServiceRefusalIsNotFallback(t *testing.T) {
	_, coll, _ := remoteFixture(t)
	refusal := errors.New("библиотека http://kb.example:8377: на этой службе граф понятий не собран или не подключён")
	out := Build(pairQuestion, Deps{Coll: coll, Map: &fakeMap{err: refusal}, GraphOn: true,
		LocalGraph: func() (*graph.Graph, func(), error) {
			t.Error("при отказе по смыслу открыт свой граф")
			return nil, nil, errors.New("не нужно")
		}}, remoteSettings())
	if out.Entities != 0 {
		t.Errorf("карта подмешана при отказе службы: %+v", out)
	}
	if !strings.Contains(out.Remote, "граф понятий не собран") {
		t.Errorf("причина отказа не дошла до человека: %q", out.Remote)
	}
}

// Карта выключена (GraphOn = false): у службы её не спрашивают, а при
// недоступной службе свой граф не открывается — карта не нужна (рецензия
// этапа 121: каждый вопрос открывал граф через CachedGraph).
func TestMapOffSkipsServiceAndOwnGraph(t *testing.T) {
	_, coll, _ := remoteFixture(t)
	fm := &fakeMap{err: errDown}
	out := Build(pairQuestion, Deps{Coll: coll, Map: fm, GraphOn: false,
		LocalGraph: func() (*graph.Graph, func(), error) {
			t.Error("карта выключена, а свой граф открыт")
			return nil, nil, errors.New("не нужно")
		}}, remoteSettings())
	if fm.calls != 0 {
		t.Errorf("карта выключена, а у службы спрошена %d раз", fm.calls)
	}
	if out.Entities != 0 {
		t.Errorf("карта подмешана при выключенной карте: %+v", out)
	}
}

// Служба не ответила, а своего графа нет — карты нет, и сказано почему.
func TestServiceDownWithoutOwnGraph(t *testing.T) {
	_, coll, _ := remoteFixture(t)
	out := Build(pairQuestion, Deps{Coll: coll, Map: &fakeMap{err: errDown}, GraphOn: true}, remoteSettings())
	if !out.Empty() {
		t.Errorf("подмешано без службы и без своего графа: %q", out.Text)
	}
	if !strings.Contains(out.Remote, "kb.example") || !strings.Contains(out.Remote, "своего графа нет") {
		t.Errorf("заметка не называет ни службу, ни причину: %q", out.Remote)
	}
}

// downLibrary — служба, до которой нет соединения.
type downLibrary struct{}

func (downLibrary) Names() ([]string, error)         { return nil, errDown }
func (downLibrary) Source(string) (kb.Source, error) { return nil, errDown }

// Если книги уже ушли на свои файлы при открытии коллекции, карту у службы
// не спрашивают второй раз — сразу свой граф.
func TestCollectionFellBackSkipsServiceMap(t *testing.T) {
	base, _, g := remoteFixture(t)
	src, err := kb.Fallback{Remote: downLibrary{}, Local: base, Default: "books"}.Source("")
	if err != nil {
		t.Fatal(err)
	}
	fm := &fakeMap{g: g}
	out := Build(pairQuestion, Deps{Coll: src, Map: fm, GraphOn: true,
		LocalGraph: func() (*graph.Graph, func(), error) { return g, func() {}, nil }}, remoteSettings())
	if fm.calls != 0 {
		t.Errorf("служба, не ответившая на открытие коллекции, спрошена ещё %d раз", fm.calls)
	}
	if out.Entities == 0 || out.Remote != kb.FallbackNote {
		t.Errorf("подмес по своим файлам: %+v", out)
	}
}

// Журнал шагов: запись подмеса через службу помечена, а переход на свои
// файлы назван в note (А4).
func TestServiceMixLogsFallback(t *testing.T) {
	_, coll, g := remoteFixture(t)
	pat, err := chatlog.ParsePattern("steps.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	w := steplog.New(t.TempDir(), pat, time.Now(), "test", true)
	Build(pairQuestion, Deps{Coll: coll, Map: &fakeMap{err: errDown}, GraphOn: true, Steps: w, Turn: "t1",
		LocalGraph: func() (*graph.Graph, func(), error) { return g, func() {}, nil }}, remoteSettings())
	w.Close()
	data, err := os.ReadFile(w.Path())
	if err != nil {
		t.Fatalf("журнал не записан: %v", err)
	}
	var st steplog.Step
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var s steplog.Step
		if json.Unmarshal([]byte(line), &s) == nil && s.Kind == steplog.KindSearch {
			st = s
		}
	}
	if st.Note != kb.FallbackNote || st.Extra["service"] != true {
		t.Errorf("запись подмеса: note %q, extra %v", st.Note, st.Extra)
	}
}
