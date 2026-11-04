package graphex

// Сверка модели на узлах пула: отпечаток (Stamp), сверка при старте (Check)
// и при возвращении узла в строй (revive). Адреса — только httptest.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// tagEntry — один тег модели на поддельном сервере: что о нём скажут
// /api/tags и /api/show.
type tagEntry struct {
	name     string
	quant    string
	digest   string
	blob     string // файл весов в FROM; пусто — Modelfile без FROM (старая Ollama, мост)
	template string // пусто — «{{ .Prompt }}»
}

// newTagServer — сервер Ollama с несколькими тегами. /api/show ищет тег
// по имени так же, как Ollama: имя без тега — это «:latest».
func newTagServer(t *testing.T, tags []tagEntry) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/version"):
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "0.40.11"})
		case strings.HasSuffix(r.URL.Path, "/api/tags"):
			models := make([]map[string]any, 0, len(tags))
			for _, e := range tags {
				models = append(models, map[string]any{"name": e.name, "model": e.name, "size": 17,
					"digest": e.digest, "details": map[string]any{"quantization_level": e.quant}})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"models": models})
		case strings.HasSuffix(r.URL.Path, "/api/show"):
			var req struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			name := req.Model
			if !strings.Contains(name, ":") {
				name += ":latest"
			}
			for _, e := range tags {
				if e.name != name {
					continue
				}
				mf, tpl := "", e.template
				if e.blob != "" {
					mf = "FROM /m/blobs/" + e.blob + "\n"
				}
				if tpl == "" {
					tpl = "{{ .Prompt }}"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"modelfile": mf, "template": tpl,
					"parameters": "top_k 20", "details": map[string]any{"quantization_level": e.quant}})
				return
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"model not found"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Узел, вернувшийся в строй посреди захода, сверяется с моделью, которой
// заход начат. После `ollama pull` на упавшем сервере веса другие, и без
// сверки граф дальше собирался бы двумя моделями — молча. Такой узел
// выключается до конца захода, и причина видна в строке хода; узел с той же
// моделью возвращается, как прежде.
func TestPoolReviveChecksModelStamp(t *testing.T) {
	ctx := context.Background()
	a := newFakeNode(t, 0)
	b := newFakeNode(t, 0)
	p := pool(t,
		Node{Name: "a", URL: a.srv.URL, Workers: 1},
		Node{Name: "b", URL: b.srv.URL, Workers: 1})
	if err := p.Check(ctx); err != nil {
		t.Fatalf("исходная сверка: %v", err)
	}
	extract := func() int32 {
		t.Helper()
		before := atomic.LoadInt32(&b.calls)
		for i := 0; i < 10; i++ {
			if _, err := p.Extract(ctx, "с", "в"); err != nil {
				t.Fatal(err)
			}
		}
		return atomic.LoadInt32(&b.calls) - before
	}
	down := func() {
		t.Helper()
		atomic.StoreInt32(&b.fail, 1)
		extract()
		if !statOf(t, p, "b").Off {
			t.Fatal("узел b не выключился")
		}
	}
	back := func() {
		atomic.StoreInt32(&b.fail, 0)
		p.mu.Lock()
		for _, n := range p.nodes {
			n.nextTry = time.Now()
		}
		p.mu.Unlock()
		p.revive(ctx)
	}

	// Та же модель — узел возвращается и снова получает куски.
	down()
	back()
	if statOf(t, p, "b").Off {
		t.Fatal("узел с той же моделью не вернулся в строй")
	}
	if extract() == 0 {
		t.Fatal("вернувшийся узел с той же моделью не получил ни одного куска")
	}

	// Другие веса — узел остаётся выключенным и кусков не получает.
	down()
	b.weights = "sha256-0000111122223333"
	back()
	if !statOf(t, p, "b").Off {
		t.Fatal("узел с другими весами вернулся в строй")
	}
	if got := extract(); got != 0 {
		t.Fatalf("узел с другими весами разобрал %d кусков из 10", got)
	}
	if line := p.Line(); !strings.Contains(line, "другой моделью") {
		t.Errorf("в строке хода нет причины выключения: %q", line)
	}
	if s := statOf(t, p, "b"); !strings.Contains(s.Banned, "разные веса") || !strings.Contains(s.Banned, "sha256-000011112222") {
		t.Errorf("в NodeStat.Banned нет внятной причины: %q", s.Banned)
	}

	// Выключен до конца захода: даже с прежними весами revive его не трогает —
	// часть захода он уже пропустил, а сервер, сменивший модель однажды,
	// сменит её и снова.
	b.weights = a.weights
	back()
	if !statOf(t, p, "b").Off {
		t.Error("выключенный за другую модель узел вернулся в строй")
	}
}

// Все узлы вернулись с другой моделью — ждать node_wait незачем: revive их
// в строй не вернёт. Заход останавливается сразу, и в ошибке сказано почему.
func TestPoolStopsWhenAllNodesBanned(t *testing.T) {
	ctx := context.Background()
	a := newFakeNode(t, 0)
	b := newFakeNode(t, 0)
	p := pool(t,
		Node{Name: "a", URL: a.srv.URL, Workers: 1},
		Node{Name: "b", URL: b.srv.URL, Workers: 1})
	if err := p.Check(ctx); err != nil {
		t.Fatalf("исходная сверка: %v", err)
	}
	atomic.StoreInt32(&a.fail, 1)
	atomic.StoreInt32(&b.fail, 1)
	for i := 0; i < 10; i++ {
		_, _ = p.Extract(ctx, "с", "в") // node_wait 0: мёртвый пул отвечает ошибкой сразу
	}
	if !statOf(t, p, "a").Off || !statOf(t, p, "b").Off {
		t.Fatal("узлы не выключились")
	}

	a.weights, b.weights = "sha256-0000111122223333", "sha256-0000111122223333"
	atomic.StoreInt32(&a.fail, 0)
	atomic.StoreInt32(&b.fail, 0)
	p.nodeWait, p.deadEvery = time.Minute, 10*time.Millisecond
	started := time.Now()
	_, err := p.Extract(ctx, "с", "в")
	if err == nil || !strings.Contains(err.Error(), "до конца захода") {
		t.Fatalf("ожидался отказ «выключены до конца захода», получено %v", err)
	}
	if spent := time.Since(started); spent > 10*time.Second {
		t.Errorf("пул ждал %s возвращения узлов, которых revive не вернёт", spent)
	}
}

// Имя без тега — это «:latest», а не первый тег с тем же началом. Квантование
// и digest Stamp прежде брал у первого «проба:*» в /api/tags, а файлы —
// из /api/show «проба»: при двух тегах одна и та же модель на двух серверах
// отвергалась как «разное квантование» из-за порядка строк в /api/tags.
func TestStampTakesTheExactTag(t *testing.T) {
	ctx := context.Background()
	t8 := tagEntry{name: "проба:8b", quant: "Q8_0", digest: "d8", blob: "sha256-2222bbbb"}
	tl := tagEntry{name: "проба:latest", quant: "Q4_K_M", digest: "dl", blob: "sha256-1111aaaa"}
	srvA := newTagServer(t, []tagEntry{t8, tl})
	srvB := newTagServer(t, []tagEntry{tl, t8})

	st, err := New(Options{Model: "проба"}, srvA.URL, 5*time.Second, nil).Stamp(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Quant != "Q4_K_M" || st.Digest != "dl" || strings.Join(st.Files, " ") != "sha256-1111aaaa" {
		t.Errorf("«проба» — это проба:latest, а отпечаток %s %s %v", st.Quant, st.Digest, st.Files)
	}
	st, err = New(Options{Model: "проба:8b"}, srvA.URL, 5*time.Second, nil).Stamp(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Quant != "Q8_0" || st.Digest != "d8" {
		t.Errorf("«проба:8b» — отпечаток %s %s", st.Quant, st.Digest)
	}

	// Та же модель на двух серверах с разным порядком тегов — сверка проходит.
	p := pool(t, Node{Name: "a", URL: srvA.URL, Workers: 1}, Node{Name: "b", URL: srvB.URL, Workers: 1})
	if err := p.Check(ctx); err != nil {
		t.Errorf("одна модель отвергнута из-за порядка тегов: %v", err)
	}

	// Check: есть только проба:8b — чат по имени «проба» получит 404, значит
	// модели нет.
	only8 := newTagServer(t, []tagEntry{t8})
	if err := New(Options{Model: "проба"}, only8.URL, 5*time.Second, nil).Check(ctx); err == nil {
		t.Error("Check принял «проба», хотя на сервере только проба:8b")
	}
	if err := New(Options{Model: "проба:8b"}, only8.URL, 5*time.Second, nil).Check(ctx); err != nil {
		t.Errorf("Check не нашёл проба:8b: %v", err)
	}
}

// Параметры, которые запрос извлечения задаёт сам (options), перекрывают
// PARAMETER из Modelfile: расхождение в них на узлах ответа не меняет и не
// повод отказывать. Те, что запрос не задаёт, сверяются по-прежнему.
func TestPoolCheckIgnoresParamsTheRequestSets(t *testing.T) {
	ctx := context.Background()
	a := newFakeNode(t, 0)
	b := newFakeNode(t, 0)
	nodes := []Node{{Name: "a", URL: a.srv.URL, Workers: 1}, {Name: "b", URL: b.srv.URL, Workers: 1}}
	a.params = "num_ctx 4096\nnum_predict 100\ntemperature 1\ntop_k 20"
	b.params = "num_ctx 4096\nnum_predict 100\ntemperature 0.6\ntop_k 20"

	// Options{Model} — температура уходит всегда, num_ctx и num_predict нет.
	p := pool(t, nodes...)
	if err := p.Check(ctx); err != nil {
		t.Errorf("температура, которую задаёт сам запрос, остановила сверку: %v", err)
	}
	b.params = "num_ctx 32768\nnum_predict 900\ntemperature 0.6\ntop_k 20"
	if err := p.Check(ctx); err == nil || !strings.Contains(err.Error(), "параметры") {
		t.Errorf("num_ctx, которого запрос не задаёт, не сверен: %v", err)
	}

	// С окном и пределом ответа в настройках их задаёт запрос.
	p2, err := NewPool(nodes, Options{Model: "проба", NumCtx: 8192, MaxTokens: 512}, 5*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := p2.Check(ctx); err != nil {
		t.Errorf("num_ctx и num_predict, которые задаёт сам запрос, остановили сверку: %v", err)
	}
	b.params = "num_ctx 32768\nnum_predict 900\ntemperature 0.6\ntop_k 40"
	if err := p2.Check(ctx); err == nil || !strings.Contains(err.Error(), "top_k") {
		t.Errorf("top_k, которого запрос не задаёт, не сверен: %v", err)
	}
}

// Веса не сверены — об этом сказано в Notes, а не молча. Файлы весов
// известны только у одного узла — digest манифеста их не заменяет (он
// расходится и от лицензии), так что это тоже не отказ, а заметка.
func TestPoolCheckNotesUnverifiedWeights(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		a, b tagEntry
	}{
		{"файлы только у a, digest пуст",
			tagEntry{name: "проба:latest", quant: "Q4_K_M", blob: "sha256-1111aaaa"},
			tagEntry{name: "проба:latest", quant: "Q4_K_M"}},
		{"файлы только у a, digest разный",
			tagEntry{name: "проба:latest", quant: "Q4_K_M", digest: "d1", blob: "sha256-1111aaaa"},
			tagEntry{name: "проба:latest", quant: "Q4_K_M", digest: "d2"}},
		{"файлов нет ни у кого, digest пуст",
			tagEntry{name: "проба:latest", quant: "Q4_K_M"},
			tagEntry{name: "проба:latest", quant: "Q4_K_M", digest: "d2"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sa := newTagServer(t, []tagEntry{c.a})
			sb := newTagServer(t, []tagEntry{c.b})
			p := pool(t, Node{Name: "узелА", URL: sa.URL, Workers: 1}, Node{Name: "узелБ", URL: sb.URL, Workers: 1})
			if err := p.Check(ctx); err != nil {
				t.Fatalf("отказ вместо заметки: %v", err)
			}
			notes := strings.Join(p.Notes(), "\n")
			if !strings.Contains(notes, "сверить не удалось") || !strings.Contains(notes, "узелА") ||
				!strings.Contains(notes, "узелБ") {
				t.Errorf("веса не сверены, а в Notes об этом нет: %q", notes)
			}
		})
	}

	// Контроль: файлов нет ни у кого, digest разный — отказ, как прежде.
	sa := newTagServer(t, []tagEntry{{name: "проба:latest", quant: "Q4_K_M", digest: "d1"}})
	sb := newTagServer(t, []tagEntry{{name: "проба:latest", quant: "Q4_K_M", digest: "d2"}})
	p := pool(t, Node{Name: "a", URL: sa.URL, Workers: 1}, Node{Name: "b", URL: sb.URL, Workers: 1})
	if err := p.Check(ctx); err == nil || !strings.Contains(err.Error(), "разные веса") {
		t.Errorf("разный digest без файлов не отвергнут: %v", err)
	}
}

// Отказ по рецепту показывает расходящуюся строку с окрестностью, а не оба
// текста целиком: шаблон в 200 КБ давал отказ в 400 КБ — в журнал и на экран.
func TestPoolCheckRecipeErrorIsBounded(t *testing.T) {
	head := strings.Repeat("{{ .Prompt }}\n", 15000)
	tail := strings.Repeat("x", 100<<10)
	ea := tagEntry{name: "проба:latest", quant: "Q4_K_M", blob: "sha256-1111aaaa", template: head + tail + "A" + tail}
	eb := ea
	eb.template = head + tail + "B" + tail
	sa := newTagServer(t, []tagEntry{ea})
	sb := newTagServer(t, []tagEntry{eb})
	p := pool(t, Node{Name: "a", URL: sa.URL, Workers: 1}, Node{Name: "b", URL: sb.URL, Workers: 1})
	err := p.Check(context.Background())
	if err == nil {
		t.Fatal("разный шаблон принят")
	}
	msg := err.Error()
	if len(msg) >= 2<<10 {
		t.Fatalf("отказ %d байт — чужой текст без обрезки", len(msg))
	}
	if !strings.Contains(msg, "xxxA") || !strings.Contains(msg, "xxxB") || !strings.Contains(msg, "шаблон") {
		t.Errorf("в отказе нет расходящегося места: %s", msg)
	}
	if !strings.Contains(msg, "15001") {
		t.Errorf("в отказе нет номера расходящейся строки: %s", msg)
	}
}

// Отказ «модели нет» перечисляет не больше двадцати имён и каждое — в
// кавычках с экранированием: имена приходят с чужого сервера, а отказ
// печатается в ленту и журнал.
func TestCheckListsFewNamesQuoted(t *testing.T) {
	var tags []tagEntry
	for i := 0; i < 30; i++ {
		tags = append(tags, tagEntry{name: fmt.Sprintf("m%02d:latest", i)})
	}
	tags = append([]tagEntry{{name: "злая\x1b]0;PWNED\a:latest"}}, tags...)
	srv := newTagServer(t, tags)
	err := New(Options{Model: "проба"}, srv.URL, 5*time.Second, nil).Check(context.Background())
	if err == nil {
		t.Fatal("модели нет, а Check молчит")
	}
	msg := err.Error()
	if strings.ContainsAny(msg, "\x1b\a") {
		t.Errorf("управляющие знаки чужого имени попали в отказ: %q", msg)
	}
	if strings.Contains(msg, "m25") || !strings.Contains(msg, "ещё 11") {
		t.Errorf("в отказе больше двадцати имён или не сказано, сколько опущено: %s", msg)
	}
	if !strings.Contains(msg, `"m00:latest"`) {
		t.Errorf("имена не в кавычках: %s", msg)
	}
}
