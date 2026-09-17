package graph

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeEmbedder отдаёт вектор, по которому видно, какому тексту он принадлежит.
//
// Так проверяется главное свойство досчёта: старые векторы остаются на своих
// местах, а новые дописываются в хвост. Если досчёт перепутает порядок, поиск
// начнёт находить не то, и заметить это по числам будет невозможно.
type fakeEmbedder struct {
	model string
	calls int
	seen  []string
}

func (f *fakeEmbedder) Model() string { return f.model }

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	f.seen = append(f.seen, texts...)
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, 4)
		v[0] = float32(len(t)) // по длине текста узнаём, что именно посчитали
		v[1] = 1
		out[i] = v
	}
	return out, nil
}

// newGraphWith создаёт граф с заданными именами понятий.
func newGraphWith(t *testing.T, names ...string) *Graph {
	t.Helper()
	g, err := Create(t.TempDir(), "проба", 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if _, _, err := g.Entities().Add(n, TypeConcept); err != nil {
			t.Fatal(err)
		}
	}
	return g
}

// Досчёт считает только новые понятия, а прежние берёт как есть.
//
// Ради этого он и написан: граф растёт заходами по два часа, и пересчитывать
// при каждом все 63 тысячи понятий — двадцать минут карты на две минуты новой
// работы.
func TestEmbedEntitiesIncremental(t *testing.T) {
	g := newGraphWith(t, "первое", "второе")
	defer g.Close()

	emb := &fakeEmbedder{model: "проба"}
	if err := g.EmbedEntities(context.Background(), emb, EmbedOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	if got := g.VectorsInfo().Count; got != 2 {
		t.Fatalf("посчитано %d векторов, ожидалось 2", got)
	}
	first := emb.calls

	// Добавляем третье понятие и считаем снова.
	if _, _, err := g.Entities().Add("третье", TypeConcept); err != nil {
		t.Fatal(err)
	}
	emb.seen = nil
	if err := g.EmbedEntities(context.Background(), emb, EmbedOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	if got := g.VectorsInfo().Count; got != 3 {
		t.Fatalf("после досчёта векторов %d, ожидалось 3", got)
	}
	if len(emb.seen) != 1 || !strings.Contains(emb.seen[0], "третье") {
		t.Fatalf("досчёт отправил в модель %v, ожидалось только новое понятие", emb.seen)
	}
	if emb.calls <= first {
		t.Fatal("досчёт не сделал ни одного запроса")
	}
}

// Когда считать нечего, досчёт не трогает ни модель, ни файлы.
func TestEmbedEntitiesNothingToDo(t *testing.T) {
	g := newGraphWith(t, "одно")
	defer g.Close()

	emb := &fakeEmbedder{model: "проба"}
	if err := g.EmbedEntities(context.Background(), emb, EmbedOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	before := emb.calls
	if err := g.EmbedEntities(context.Background(), emb, EmbedOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	if emb.calls != before {
		t.Fatalf("повторный досчёт сделал %d лишних запросов", emb.calls-before)
	}
}

// Recount считает всё заново — нужен, когда прежние векторы негодны.
func TestEmbedEntitiesRecount(t *testing.T) {
	g := newGraphWith(t, "первое", "второе")
	defer g.Close()

	emb := &fakeEmbedder{model: "проба"}
	if err := g.EmbedEntities(context.Background(), emb, EmbedOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	emb.seen = nil
	if err := g.EmbedEntities(context.Background(), emb, EmbedOpts{Recount: true}, nil); err != nil {
		t.Fatal(err)
	}
	if len(emb.seen) != 2 {
		t.Fatalf("пересчёт отправил %d текстов, ожидалось 2", len(emb.seen))
	}
}

// Смена модели отменяет досчёт: векторы разных моделей живут в разных
// пространствах, и склеивать их — то же, что складывать метры с килограммами.
func TestEmbedEntitiesModelChangeForcesFull(t *testing.T) {
	g := newGraphWith(t, "первое", "второе")
	defer g.Close()

	if err := g.EmbedEntities(context.Background(), &fakeEmbedder{model: "старая"}, EmbedOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	other := &fakeEmbedder{model: "новая"}
	if err := g.EmbedEntities(context.Background(), other, EmbedOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	if len(other.seen) != 2 {
		t.Fatalf("при смене модели отправлено %d текстов, ожидалось 2 (всё заново)", len(other.seen))
	}
	if got := g.VectorsInfo().Model; got != "новая" {
		t.Fatalf("в паспорте осталась модель %q", got)
	}
}

// Досчёт хвостом на сервере с другими весами той же модели — отказ до счёта.
//
// Догонщик это сверял с 06.09.2026, а обычный --graph-embed нет: он смешал бы
// пространства и записал бы в паспорт новый отпечаток, стерев след смешения.
func TestEmbedTopUpRefusesForeignWeights(t *testing.T) {
	g := growGraph(t, 3)
	first := &countingEmbedder{model: "m", digest: "AAA", dim: 4}
	if err := g.EmbedEntities(t.Context(), first, EmbedOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	if got := g.VectorsInfo().Digest; got != "AAA" {
		t.Fatalf("в паспорте отпечаток %q", got)
	}
	for i := 0; i < 2; i++ {
		if _, _, err := g.Entities().Add("новое"+string(rune('a'+i)), TypeConcept); err != nil {
			t.Fatal(err)
		}
	}

	other := &countingEmbedder{model: "m", digest: "BBB", dim: 4}
	err := g.EmbedEntities(t.Context(), other, EmbedOpts{}, nil)
	if err == nil || !strings.Contains(err.Error(), "другим файлом модели") {
		t.Fatalf("досчёт чужими весами прошёл: %v", err)
	}
	if other.asked != 0 {
		t.Errorf("до отказа посчитали %d векторов", other.asked)
	}
	if got := g.VectorsInfo(); got.Digest != "AAA" || got.Count != 3 {
		t.Errorf("паспорт тронут: %+v", got)
	}

	// Полный пересчёт — единственный честный путь, и он идёт.
	if err := g.EmbedEntities(t.Context(), other, EmbedOpts{Recount: true}, nil); err != nil {
		t.Fatalf("полный пересчёт чужими весами: %v", err)
	}
	if got := g.VectorsInfo(); got.Digest != "BBB" || got.Count != 5 {
		t.Errorf("после пересчёта паспорт %+v", got)
	}
}

// flakyEmbedder отказывает обрывом связи заданное число раз, потом отвечает.
type flakyEmbedder struct {
	fakeEmbedder
	failsLeft int
}

func (f *flakyEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if f.failsLeft > 0 {
		f.failsLeft--
		return nil, fmt.Errorf("запрос к серверу: %w", syscall.ECONNREFUSED)
	}
	return f.fakeEmbedder.Embed(ctx, texts)
}

// Обрыв связи пережидается, а не роняет счёт: 10.09.2026 полный пересчёт
// books упал на семисекундном обрыве туннеля через 20 минут карты.
func TestEmbedEntitiesWaitsOutConnectionLoss(t *testing.T) {
	g := newGraphWith(t, "горутина", "канал")
	defer g.Close()
	prev := embedRetryEvery
	embedRetryEvery = time.Millisecond
	defer func() { embedRetryEvery = prev }()

	emb := &flakyEmbedder{fakeEmbedder: fakeEmbedder{model: "проба"}, failsLeft: 2}
	if err := g.EmbedEntities(context.Background(), emb, EmbedOpts{Workers: 1, NodeWait: time.Second}, nil); err != nil {
		t.Fatalf("обрыв на две попытки должен пережидаться: %v", err)
	}
	if !g.vecs.Ready() {
		t.Fatal("векторы не записаны")
	}
	// Отказ по существу не пережидается.
	bad := &fakeEmbedder{model: "проба"}
	_ = bad
	if transientEmbedErr(fmt.Errorf("модель не найдена")) {
		t.Fatal("отказ сервера принят за обрыв связи")
	}
}

// Ошибка настройки — не обрыв связи: счёт обязан упасть сразу, а не молча
// повторять запрос пятнадцать минут (аудит 17.09.2026, Б13). Клиент HTTP
// заворачивает ЛЮБУЮ ошибку в *url.Error, который сам выглядит как net.Error.
func TestTransientEmbedErrTellsTypoFromOutage(t *testing.T) {
	wrap := func(inner error) error {
		return fmt.Errorf("запрос векторов: %w", &url.Error{Op: "Post", URL: "http://ollama.example:11434/api/embed", Err: inner})
	}
	notTransient := map[string]error{
		"имени нет в DNS":     wrap(&net.OpError{Op: "dial", Err: &net.DNSError{Err: "no such host", Name: "ollama.example", IsNotFound: true}}),
		"неизвестная схема":   wrap(errors.New(`unsupported protocol scheme "htp"`)),
		"негодный сертификат": wrap(errors.New("x509: certificate signed by unknown authority")),
	}
	for name, err := range notTransient {
		if transientEmbedErr(err) {
			t.Errorf("%s принято за обрыв связи: %v", name, err)
		}
	}
	transient := map[string]error{
		"соединение отклонено": wrap(&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}),
		"соединение оборвано":  wrap(&net.OpError{Op: "read", Err: syscall.ECONNRESET}),
		"ответ оборван":        wrap(io.ErrUnexpectedEOF),
		"временный сбой DNS":   wrap(&net.OpError{Op: "dial", Err: &net.DNSError{Err: "server misbehaving", IsTemporary: true}}),
		"истёк срок запроса":   wrap(context.DeadlineExceeded),
	}
	for name, err := range transient {
		if !transientEmbedErr(err) {
			t.Errorf("%s НЕ принято за обрыв связи: %v", name, err)
		}
	}
}

// Ожидание сервера не молчит: о каждом повторе сообщается наружу.
func TestEmbedWaitReportsItself(t *testing.T) {
	prev := embedRetryEvery
	embedRetryEvery = time.Millisecond
	defer func() { embedRetryEvery = prev }()

	emb := &flakyEmbedder{fakeEmbedder: fakeEmbedder{model: "проба"}, failsLeft: 2}
	notes := 0
	o := EmbedOpts{NodeWait: time.Second, OnWait: func(error, time.Duration, time.Duration) { notes++ }}
	if _, err := embedWithWait(context.Background(), emb, []string{"горутина"}, o); err != nil {
		t.Fatal(err)
	}
	if notes != 2 {
		t.Fatalf("сообщений об ожидании %d, ожидалось 2 — по одному на обрыв", notes)
	}
}
