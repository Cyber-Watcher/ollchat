package kbremote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbserve"
)

// fakeServer — служба, отвечающая заранее заданным набором.
//
// Настоящий `ollmcp` поднимать в юнит-тесте нельзя: ему нужны база на диске
// и сервер эмбеддингов. Здесь проверяется **договор между клиентом и службой** —
// что клиент шлёт, что понимает в ответе и как ведёт себя при отказе.
// Совпадение выдачи с локальной коллекцией проверяется отдельно, живым
// прогоном (см. README службы).
type fakeServer struct {
	token string

	gen      string
	books    int
	chunks   int
	vectors  int
	results  []kb.Result
	note     string
	lastReq  searchRequest
	requests int
	fail     int // код ответа вместо выдачи; 0 — отвечать нормально

	booksFail int // сколько следующих запросов реестра книг отклонить

	// Поиск по графу: тело последнего запроса как пришло и ответ как есть —
	// договор проверяется по самим байтам, без серверных типов.
	graphBody []byte
	graphResp string
}

func (f *fakeServer) handler() http.Handler {
	mux := http.NewServeMux()
	auth := func(r *http.Request) bool {
		if f.token == "" {
			return true
		}
		return strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer")) == f.token
	}
	mux.HandleFunc("/api/v1/collections", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			http.Error(w, "нужен ключ доступа", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"collections": []collectionInfo{{
				Name: "books", Books: f.books, Chunks: f.chunks, Vectors: f.vectors,
				VecModel: "bge-m3", VecDim: 1024, Generation: f.gen,
			}},
			"default": "books",
		})
	})
	mux.HandleFunc("/api/v1/books", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			http.Error(w, "нужен ключ доступа", http.StatusUnauthorized)
			return
		}
		if f.booksFail > 0 {
			f.booksFail--
			http.Error(w, "служба перезапускается", http.StatusServiceUnavailable)
			return
		}
		recs := make([]kb.BookRec, 0, f.books)
		for i := 0; i < f.books; i++ {
			recs = append(recs, kb.BookRec{ID: uint32(i + 1), Title: "книга", Kind: kb.BookOK})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"books": recs, "generation": f.gen})
	})
	mux.HandleFunc("/api/v1/search", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			http.Error(w, "нужен ключ доступа", http.StatusUnauthorized)
			return
		}
		if f.fail != 0 {
			w.WriteHeader(f.fail)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "коллекция books: нет такой"})
			return
		}
		f.requests++
		_ = json.NewDecoder(r.Body).Decode(&f.lastReq)
		_ = json.NewEncoder(w).Encode(searchResponse{
			Results: f.results, Note: f.note, Generation: f.gen, Collection: "books",
		})
	})
	mux.HandleFunc("/api/v1/graph/search", func(w http.ResponseWriter, r *http.Request) {
		f.graphBody, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, f.graphResp)
	})
	mux.HandleFunc("/api/v1/around", func(w http.ResponseWriter, r *http.Request) {
		var req aroundRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(searchResponse{
			Results:    []kb.Result{{ID: req.ID, Text: "кусок целиком"}},
			Generation: f.gen,
		})
	})
	return mux
}

func newFake(t *testing.T, f *fakeServer) (*Client, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(f.handler())
	t.Cleanup(ts.Close)
	cl, err := New(Opts{URL: ts.URL, Token: f.token, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return cl, ts
}

// Драйвер остаётся взаимозаменяемым с локальной коллекцией.
func TestRemoteSatisfiesSource(t *testing.T) {
	var _ kb.Source = (*source)(nil)
	var _ kb.Library = (*Client)(nil)
}

// Пустой адрес — не ошибка: значит работаем с файлами, как раньше.
func TestEmptyURLMeansLocal(t *testing.T) {
	cl, err := New(Opts{URL: "  "})
	if err != nil {
		t.Fatalf("пустой адрес не должен быть ошибкой: %v", err)
	}
	if cl != nil {
		t.Error("при пустом адресе клиента быть не должно")
	}
}

// Мусор в адресе отклоняется сразу, с именем настройки.
func TestBadURLIsRejected(t *testing.T) {
	for _, bad := range []string{"kb.corp.local:8377", "просто текст", "://"} {
		if _, err := New(Opts{URL: bad}); err == nil {
			t.Errorf("адрес %q должен был отклониться", bad)
		}
	}
}

// Числа отбора доезжают до службы: ради этого весь замер и затевался.
func TestSearchOptionsReachServer(t *testing.T) {
	f := &fakeServer{gen: "g1", books: 3, chunks: 100, vectors: 100}
	cl, _ := newFake(t, f)

	src, err := cl.Source("books")
	if err != nil {
		t.Fatal(err)
	}
	opt := kb.DefaultSearchOpts()
	opt.TopK, opt.MaxPerDoc, opt.SemanticWeight = 12, 2, 1.5
	opt.Docs = []uint32{7}
	if _, err := src.SearchWith(context.Background(), "как связаны X и Y", opt, nil); err != nil {
		t.Fatal(err)
	}

	got := f.lastReq
	if got.Query != "как связаны X и Y" || got.TopK != 12 || got.MaxPerBook != 2 ||
		got.SemanticWeight != 1.5 || len(got.Books) != 1 || got.Books[0] != 7 {
		t.Errorf("до службы доехало не то: %+v", got)
	}
}

// Состав коллекции виден без обращения к сети: Stats и Books в интерфейсе
// объявлены без ошибки, и ходить из них по сети нельзя.
func TestStatsAndBooksAreLocal(t *testing.T) {
	f := &fakeServer{gen: "g1", books: 4, chunks: 999, vectors: 999}
	cl, ts := newFake(t, f)

	src, err := cl.Source("books")
	if err != nil {
		t.Fatal(err)
	}
	ts.Close() // сервер больше недоступен — состав обязан остаться известным

	if st := src.Stats(); st.Chunks != 999 || st.Indexed != 4 || st.VecModel != "bge-m3" {
		t.Errorf("состав коллекции: %+v", st)
	}
	if len(src.Books()) != 4 {
		t.Errorf("книг в реестре %d, ожидалось 4", len(src.Books()))
	}
}

// Смена поколения на сервере обновляет состав у клиента.
//
// Это главная проверка честности: администратор долил книги, и клиент не должен
// продолжать показывать вчерашние цифры.
func TestGenerationChangeRefreshesStats(t *testing.T) {
	f := &fakeServer{gen: "g1", books: 2, chunks: 100, vectors: 100}
	cl, _ := newFake(t, f)

	src, err := cl.Source("books")
	if err != nil {
		t.Fatal(err)
	}
	if src.Stats().Chunks != 100 {
		t.Fatalf("исходный состав не прочитался: %+v", src.Stats())
	}

	// Администратор долил книги.
	f.books, f.chunks, f.gen = 5, 250, "g2"
	if _, err := src.SearchWith(context.Background(), "вопрос", kb.DefaultSearchOpts(), nil); err != nil {
		t.Fatal(err)
	}

	if st := src.Stats(); st.Chunks != 250 || st.Indexed != 5 {
		t.Errorf("после смены поколения состав не обновился: %+v", st)
	}
	if len(src.Books()) != 5 {
		t.Errorf("реестр книг не обновился: %d", len(src.Books()))
	}
}

// Реестр книг не прочитался — поколение не принимается, и следующий ответ
// перечитывает состав снова. Прежде поколение ставилось до загрузки книг,
// и после одного сбоя реестр оставался вчерашним до следующей доливки.
func TestGenerationKeptUntilBooksLoaded(t *testing.T) {
	f := &fakeServer{gen: "g1", books: 2, chunks: 100}
	cl, _ := newFake(t, f)
	src, err := cl.Source("books")
	if err != nil {
		t.Fatal(err)
	}

	f.books, f.chunks, f.gen, f.booksFail = 5, 250, "g2", 1
	if _, err := src.SearchWith(context.Background(), "вопрос", kb.DefaultSearchOpts(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := src.SearchWith(context.Background(), "вопрос", kb.DefaultSearchOpts(), nil); err != nil {
		t.Fatal(err)
	}
	if n := len(src.Books()); n != 5 {
		t.Errorf("реестр книг после сбоя загрузки так и не обновился: %d книг, ожидалось 5", n)
	}
	if st := src.Stats(); st.Indexed != 5 || st.Chunks != 250 {
		t.Errorf("состав: %+v", st)
	}
}

// Без смены поколения лишних запросов состава не делается.
func TestSameGenerationDoesNotRefetch(t *testing.T) {
	f := &fakeServer{gen: "g1", books: 2, chunks: 100}
	cl, _ := newFake(t, f)
	src, err := cl.Source("books")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := src.SearchWith(context.Background(), "вопрос", kb.DefaultSearchOpts(), nil); err != nil {
			t.Fatal(err)
		}
	}
	if f.requests != 3 {
		t.Errorf("поисковых запросов %d, ожидалось 3", f.requests)
	}
}

// Объяснение о смысловом поиске доезжает до человека.
//
// Молчаливое вырождение смыслового поиска в словесный — ровно та беда, ради
// которой заведена эта строка; по сети она теряться не должна.
func TestSearchNoteIsCarriedOver(t *testing.T) {
	f := &fakeServer{gen: "g1", books: 1, note: "векторы посчитаны другой моделью"}
	cl, _ := newFake(t, f)
	src, _ := cl.Source("books")

	if _, err := src.SearchWith(context.Background(), "вопрос", kb.DefaultSearchOpts(), nil); err != nil {
		t.Fatal(err)
	}
	if got := src.SearchNote(); got != "векторы посчитаны другой моделью" {
		t.Errorf("объяснение потерялось: %q", got)
	}
}

// Ключ доступа: без него служба отвечает отказом, и отказ должен быть понятным.
func TestUnauthorizedIsExplained(t *testing.T) {
	f := &fakeServer{token: "верный-ключ", gen: "g1", books: 1}
	ts := httptest.NewServer(f.handler())
	defer ts.Close()

	cl, err := New(Opts{URL: ts.URL, Token: "чужой-ключ"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = cl.Source("books")
	if err == nil {
		t.Fatal("с чужим ключом открытие должно отклоняться")
	}
	if !strings.Contains(err.Error(), "ключ доступа") {
		t.Errorf("отказ не объясняет причину: %v", err)
	}
}

// Недоступная служба объясняется адресом: беда на чужой машине, а не в книгах.
func TestUnreachableServerNamesAddress(t *testing.T) {
	f := &fakeServer{gen: "g1", books: 1}
	cl, ts := newFake(t, f)
	addr := ts.URL
	ts.Close()

	_, err := cl.Source("books")
	if err == nil {
		t.Fatal("ожидалась ошибка соединения")
	}
	if !strings.Contains(err.Error(), addr) {
		t.Errorf("в ошибке нет адреса службы: %v", err)
	}
}

// Неизвестная коллекция — отказ со списком доступных.
func TestUnknownCollectionListsAvailable(t *testing.T) {
	f := &fakeServer{gen: "g1", books: 1}
	cl, _ := newFake(t, f)

	_, err := cl.Source("такой-нет")
	if err == nil {
		t.Fatal("ожидался отказ")
	}
	if !strings.Contains(err.Error(), "books") {
		t.Errorf("в отказе нет списка доступных коллекций: %v", err)
	}
}

// Пустое имя берёт коллекцию по умолчанию, названную службой.
func TestEmptyNameUsesServerDefault(t *testing.T) {
	f := &fakeServer{gen: "g1", books: 1}
	cl, _ := newFake(t, f)

	src, err := cl.Source("")
	if err != nil {
		t.Fatal(err)
	}
	if src.Name() != "books" {
		t.Errorf("выбрана коллекция %q, ожидалась books", src.Name())
	}
}

// Ошибка службы доносится текстом, а не голым кодом.
func TestServerErrorTextIsShown(t *testing.T) {
	f := &fakeServer{gen: "g1", books: 1, fail: http.StatusNotFound}
	cl, _ := newFake(t, f)
	src, err := cl.Source("books")
	if err != nil {
		t.Fatal(err)
	}

	_, err = src.SearchWith(context.Background(), "вопрос", kb.DefaultSearchOpts(), nil)
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if !strings.Contains(err.Error(), "нет такой") {
		t.Errorf("текст ошибки службы потерялся: %v", err)
	}
}

// Чтение куска целиком работает по сети так же, как локально.
func TestAroundOverNetwork(t *testing.T) {
	f := &fakeServer{gen: "g1", books: 1}
	cl, _ := newFake(t, f)
	src, _ := cl.Source("books")

	parts, err := src.Around("books/12#37", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 || parts[0].ID != "books/12#37" {
		t.Errorf("не тот кусок: %+v", parts)
	}
}

// Служба не дала ответа — ошибка помечена kb.ErrUnreachable: по метке клиент
// переходит на свои файлы (этап 121, А4). Текст прежний — с адресом.
func TestUnreachableIsMarked(t *testing.T) {
	f := &fakeServer{gen: "g1", books: 1}
	cl, ts := newFake(t, f)
	ts.Close()

	_, err := cl.Source("books")
	if !errors.Is(err, kb.ErrUnreachable) {
		t.Fatalf("отказ в соединении не помечен как недоступность службы: %v", err)
	}
	if !strings.Contains(err.Error(), ts.URL) {
		t.Errorf("в ошибке нет адреса службы: %v", err)
	}
}

// Срок ожидания — тоже недоступность: служба не ответила вовремя.
func TestTimeoutIsMarked(t *testing.T) {
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer ts.Close()
	defer close(release)
	cl, err := New(Opts{URL: ts.URL, Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cl.Names(); !errors.Is(err, kb.ErrUnreachable) {
		t.Errorf("срок ожидания не помечен как недоступность службы: %v", err)
	}
}

// Отказ по смыслу — ключ, неизвестная коллекция, ошибка службы — не
// недоступность: перехода на свои файлы по нему нет.
func TestRefusalIsNotUnreachable(t *testing.T) {
	f := &fakeServer{token: "верный-ключ", gen: "g1", books: 1}
	ts := httptest.NewServer(f.handler())
	defer ts.Close()

	stranger, err := New(Opts{URL: ts.URL, Token: "чужой-ключ"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stranger.Source("books"); err == nil || errors.Is(err, kb.ErrUnreachable) {
		t.Errorf("401: %v", err)
	}
	owner, err := New(Opts{URL: ts.URL, Token: "верный-ключ"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Source("нет-такой"); err == nil || errors.Is(err, kb.ErrUnreachable) {
		t.Errorf("неизвестная коллекция: %v", err)
	}
	src, err := owner.Source("books")
	if err != nil {
		t.Fatal(err)
	}
	f.fail = http.StatusNotFound
	if _, err := src.SearchWith(context.Background(), "q", kb.DefaultSearchOpts(), nil); err == nil ||
		errors.Is(err, kb.ErrUnreachable) {
		t.Errorf("ошибка службы со статусом: %v", err)
	}
}

// Отмена вызывающим (человек нажал Esc) — не недоступность службы: искать
// у себя брошенное незачем.
func TestCancelIsNotUnreachable(t *testing.T) {
	f := &fakeServer{gen: "g1", books: 1}
	cl, _ := newFake(t, f)
	src, err := cl.Source("books")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = src.SearchWith(ctx, "q", kb.DefaultSearchOpts(), nil)
	if err == nil || errors.Is(err, kb.ErrUnreachable) {
		t.Errorf("отменённый запрос: %v", err)
	}
}

// Размер карты решает клиент: entities и neighbors уходят службе. Ноль —
// «как у службы», и тогда полей в запросе нет вовсе — служба прежней сборки
// видит прежний запрос (этап 121, А2).
func TestGraphSearchSendsMapSize(t *testing.T) {
	f := &fakeServer{graphResp: `{"result":{"Entities":[{"Name":"горутина","Mentions":3}]},` +
		`"chain":[{"From":"горутина","To":"канал","Type":"uses"},{"From":"канал","To":"рантайм","Type":"part_of"}]}`}
	cl, _ := newFake(t, f)

	res, chain, err := cl.GraphSearch(context.Background(), "books", "как связаны горутина и рантайм", 6, 4)
	if err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if err := json.Unmarshal(f.graphBody, &sent); err != nil {
		t.Fatal(err)
	}
	if sent["entities"] != float64(6) || sent["neighbors"] != float64(4) || sent["collection"] != "books" {
		t.Errorf("службе ушло: %s", f.graphBody)
	}
	if len(res.Entities) != 1 || res.Entities[0].Name != "горутина" || res.Entities[0].Mentions != 3 {
		t.Errorf("карта не разобралась: %+v", res)
	}
	// Цепочка между понятиями вопроса приходит со службы (А6).
	if len(chain) != 2 || chain[0].From != "горутина" || chain[1].To != "рантайм" {
		t.Errorf("цепочка не разобралась: %+v", chain)
	}

	if _, _, err := cl.GraphSearch(context.Background(), "books", "q", 0, 0); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(f.graphBody), "entities") || strings.Contains(string(f.graphBody), "neighbors") {
		t.Errorf("при нуле поля размера ушли службе: %s", f.graphBody)
	}
}

// Служба прежней сборки отдаёт карту без цепочки — клиент читает карту,
// цепочка пустая.
func TestGraphSearchOldServiceWithoutChain(t *testing.T) {
	f := &fakeServer{graphResp: `{"result":{"Entities":[{"Name":"горутина"}]}}`}
	cl, _ := newFake(t, f)
	res, chain, err := cl.GraphSearch(context.Background(), "books", "q", 6, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entities) != 1 || len(chain) != 0 {
		t.Errorf("ответ прежней службы: карта %+v, цепочка %+v", res, chain)
	}
}

// Ключ в адресе службы не поддерживается (решение владельца 23.10.2026):
// учётные данные, строка запроса и якорь в kb.server_url — отказ при заведении
// клиента, и в тексте отказа самого ключа нет. FromConfig отказывает до
// запуска kb.server_token_cmd. Прежде адрес принимался, и «user:pass@» с
// «?key=» уходили в текст каждой ошибки — модели, в ленту и журнал шагов
// (аудит 23.10.2026, находка 8).
func TestKeyInURLIsRefused(t *testing.T) {
	for _, addr := range []string{
		"http://reader:s3cretpass@kb.example:8377",
		"http://kb.example:8377/?key=k3y",
		"http://kb.example:8377/kb?",
		"http://kb.example:8377/#k3y",
	} {
		c, err := New(Opts{URL: addr})
		if err == nil || c != nil {
			t.Errorf("%s: адрес с ключом принят", addr)
			continue
		}
		if strings.Contains(err.Error(), "s3cretpass") || strings.Contains(err.Error(), "k3y") {
			t.Errorf("%s: ключ в тексте отказа: %v", addr, err)
		}
		if !strings.Contains(err.Error(), "kb.example:8377") {
			t.Errorf("%s: отказ не называет службу: %v", addr, err)
		}
		if CheckURL(addr) == nil {
			t.Errorf("%s: CheckURL пропустил адрес, который New отвергает", addr)
		}
	}
	if err := CheckURL("http://kb.example:8377/kb"); err != nil {
		t.Errorf("обычный адрес с путём отвергнут: %v", err)
	}

	ran := filepath.Join(t.TempDir(), "ran")
	_, err := FromConfig(context.Background(), config.KB{ServerURL: "http://u:p@kb.example:8377",
		ServerTokenCmd: "echo x >> " + ran + "; echo ключ"})
	if err == nil {
		t.Error("FromConfig принял адрес с ключом")
	}
	if _, serr := os.Stat(ran); serr == nil {
		t.Error("kb.server_token_cmd выполнен ради адреса, который всё равно отвергнут")
	}
}

// kb.server_token_cmd выполняется один раз на процесс: при kb.server_url ключ
// берут клиент библиотеки, инструмент search и сервер kb в хабе — прежде
// команда шла до трёх раз за запуск (аудит 23.10.2026). Неудача не
// запоминается.
func TestTokenCmdRunsOncePerProcess(t *testing.T) {
	ran := filepath.Join(t.TempDir(), "ran")
	k := config.KB{ServerTokenCmd: "echo x >> " + ran + "; echo ключ-раз"}
	for i := 0; i < 3; i++ {
		tok, err := Token(context.Background(), k)
		if err != nil || tok != "ключ-раз" {
			t.Fatalf("вызов %d: ключ %q, ошибка %v", i+1, tok, err)
		}
	}
	data, err := os.ReadFile(ran)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "x"); n != 1 {
		t.Errorf("команда за ключом выполнена %d раз за три вызова Token", n)
	}

	failed := filepath.Join(t.TempDir(), "failed")
	bad := config.KB{ServerTokenCmd: "echo x >> " + failed + "; exit 3"}
	for i := 0; i < 2; i++ {
		if _, err := Token(context.Background(), bad); err == nil {
			t.Fatal("неудача команды за ключом не дала ошибки")
		}
	}
	if data, _ := os.ReadFile(failed); strings.Count(string(data), "x") != 2 {
		t.Errorf("неудача запомнилась: команда выполнена %d раз за два вызова", strings.Count(string(data), "x"))
	}
}

// Занятая служба — не недоступная (решение владельца 23.10.2026): клиент
// говорит службе, сколько готов ждать места в очереди (половину своего
// срока), служба отвечает 503 «занята» раньше, чем у клиента истечёт срок,
// и ошибка несёт kb.ErrBusy, а не kb.ErrUnreachable — перехода на свои файлы
// по ней нет. Прежде клиент ждал до своего срока и считал службу недоступной.
func TestBusyServiceIsNotUnreachable(t *testing.T) {
	base, err := kb.OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	hold := &holdGraph{entered: make(chan struct{}, 1), release: make(chan struct{})}
	defer close(hold.release)
	ts := httptest.NewServer(kbserve.Handler(kbserve.Opts{Base: base, Graph: hold, HeavySlots: 1}))
	defer ts.Close()
	cl, err := New(Opts{URL: ts.URL, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _, _ = cl.GraphSearch(context.Background(), "books", "занять", 0, 0) }()
	<-hold.entered

	started := time.Now()
	_, err = cl.GraphMap(context.Background(), "books", "второй", 0, 0)
	if err == nil {
		t.Fatal("занятая служба ответила картой")
	}
	if errors.Is(err, kb.ErrUnreachable) || !errors.Is(err, kb.ErrBusy) {
		t.Errorf("занятость сочтена недоступностью или не помечена: %v", err)
	}
	if !strings.Contains(err.Error(), "очередь службы полна") || !strings.Contains(err.Error(), "ждал в очереди") {
		t.Errorf("текст не говорит об очереди: %v", err)
	}
	if took := time.Since(started); took >= 2*time.Second {
		t.Errorf("ответ «занята» пришёл через %v — не раньше срока клиента", took)
	}
}

// holdGraph держит каждый запрос карты, пока тест не отпустит.
type holdGraph struct {
	entered chan struct{}
	release chan struct{}
}

func (h *holdGraph) Tool(context.Context, string, string, map[string]any) (string, error) {
	return "", nil
}

func (h *holdGraph) Search(ctx context.Context, _ kbserve.GraphSearchRequest) (kbserve.GraphSearchResponse, error) {
	h.entered <- struct{}{}
	select {
	case <-h.release:
	case <-ctx.Done():
	}
	return kbserve.GraphSearchResponse{}, nil
}

func (h *holdGraph) Graph(string) (*graph.Graph, graph.NeighborRank, func(), error) {
	return nil, graph.NeighborRank{}, nil, errors.New("графа нет")
}

// Ответ службы с годами связей и временем в очереди: клиент их читает; годы
// не по числу связей — отбрасываются. Ожидание в очереди у инструмента графа —
// первой строкой его текста.
func TestGraphMapYearsAndQueue(t *testing.T) {
	var gotWait string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotWait = r.Header.Get(queueWaitHeader)
		switch r.URL.Path {
		case "/api/v1/graph/search":
			io.WriteString(w, `{"result":{"Relations":[{"Src":"a"},{"Src":"b"}]},"relation_years":[2021,0],"queued_ms":1500}`)
		case "/api/v1/graph/tool":
			io.WriteString(w, `{"text":"карточка","queued_ms":700}`)
		}
	}))
	defer ts.Close()
	cl, err := New(Opts{URL: ts.URL, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	m, err := cl.GraphMap(context.Background(), "books", "q", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Years) != 2 || m.Years[0] != 2021 || m.Queued != 1500*time.Millisecond {
		t.Errorf("годы %v, очередь %v", m.Years, m.Queued)
	}
	if ms, err := strconv.Atoi(gotWait); err != nil || ms <= 0 || ms > 5000 {
		t.Errorf("заголовок ожидания очереди %q: ждали половину срока клиента (не больше 5000 мс)", gotWait)
	}
	text, err := cl.GraphTool(context.Background(), "books", "graph_entity", nil)
	if err != nil || !strings.HasPrefix(text, "(служба занята: ждал в очереди 700 мс)\nкарточка") {
		t.Errorf("текст инструмента: %q, %v", text, err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"result":{"Relations":[{"Src":"a"},{"Src":"b"}]},"relation_years":[2021]}`)
	}))
	defer bad.Close()
	cl, _ = New(Opts{URL: bad.URL})
	if m, err := cl.GraphMap(context.Background(), "books", "q", 0, 0); err != nil || m.Years != nil {
		t.Errorf("годы не по числу связей приняты: %v, %v", m.Years, err)
	}
}

// /search через службу: вход /api/v1/find; у службы прежней сборки его нет —
// ошибка ErrNotServed, а не недоступность.
func TestFindOverNetwork(t *testing.T) {
	var got FindRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/find" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, `{"result":{"Query":"q","Excerpts":[{"ID":"books/1#2"}]},"text":"выдача","collection":"books","queued_ms":40}`)
	}))
	defer ts.Close()
	cl, _ := New(Opts{URL: ts.URL})
	rep, err := cl.Find(context.Background(), FindRequest{Collection: "books", Query: "q", TopK: 7, Graph: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.TopK != 7 || !got.Graph || rep.Text != "выдача" || len(rep.Result.Excerpts) != 1 ||
		rep.Queued != 40*time.Millisecond {
		t.Errorf("запрос %+v, ответ %+v", got, rep)
	}

	old := httptest.NewServer(http.NotFoundHandler())
	defer old.Close()
	cl, _ = New(Opts{URL: old.URL})
	_, err = cl.Find(context.Background(), FindRequest{Query: "q"})
	if !errors.Is(err, ErrNotServed) || errors.Is(err, kb.ErrUnreachable) {
		t.Errorf("служба прежней сборки: %v", err)
	}
}
