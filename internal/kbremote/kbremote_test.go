package kbremote

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
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
