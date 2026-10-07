package kbrerank

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// server — llama-server с ручкой /rerank, отвечающий заданными строками.
// Запрос сохраняется: по нему проверяется, что ушло службе.
func server(t *testing.T, answer func(req rerankRequest) any) (*httptest.Server, *rerankRequest) {
	t.Helper()
	var got rerankRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rerank" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(answer(got))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

type result struct {
	Index int     `json:"index"`
	Score float64 `json:"relevance_score"`
}

func results(rs ...result) map[string]any { return map[string]any{"results": rs} }

// Пустой адрес — «переранжирование не настроено», и это nil, а не клиент
// в никуда. Методы nil-клиента не падают: так их зовут проверки состояния.
func TestNewWithoutURLIsNil(t *testing.T) {
	if r := New(Options{URL: "  "}); r != nil {
		t.Fatalf("без адреса собран клиент: %+v", r)
	}
	var r *Reranker
	if r.Model() != "" {
		t.Errorf("у ненастроенного клиента модель %q", r.Model())
	}
	if scores, err := r.Rerank(context.Background(), "q", []string{"a"}); scores != nil || err != nil {
		t.Errorf("ненастроенный клиент ответил: %v, %v", scores, err)
	}
	if err := r.Check(context.Background()); err == nil {
		t.Error("проверка ненастроенного клиента прошла")
	}
}

// Служба отвечает списком по убыванию оценки, а вызывающий ждёт оценки
// в порядке документов: ответ раскладывается обратно по номерам.
func TestRerankRestoresDocumentOrder(t *testing.T) {
	srv, got := server(t, func(req rerankRequest) any {
		return results(result{2, 0.9}, result{0, 0.5}, result{1, -3})
	})
	r := New(Options{URL: srv.URL + "/", Model: " bge-reranker ", Timeout: 5 * time.Second})
	scores, err := r.Rerank(context.Background(), "вопрос", []string{"а", "б", "в"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []float64{0.5, -3, 0.9}; len(scores) != 3 || scores[0] != want[0] || scores[1] != want[1] || scores[2] != want[2] {
		t.Fatalf("оценки %v, ожидалось %v", scores, want)
	}
	if got.Query != "вопрос" || got.TopN != 3 || got.Model != "bge-reranker" || len(got.Documents) != 3 {
		t.Errorf("служба получила не то: %+v", *got)
	}
	if r.Model() != "bge-reranker" {
		t.Errorf("имя модели %q", r.Model())
	}
}

// Повтор номера в ответе — не оценка второго куска.
//
// Ответ «0, 0» на два куска проходил проверку числом строк, и второму куску
// доставался ноль: выдача переставлялась по оценке, которой не было.
func TestRerankRejectsDuplicateIndexes(t *testing.T) {
	srv, _ := server(t, func(rerankRequest) any {
		return results(result{0, 0.9}, result{0, 0.8})
	})
	r := New(Options{URL: srv.URL, Timeout: 5 * time.Second})
	if scores, err := r.Rerank(context.Background(), "q", []string{"а", "б"}); err == nil {
		t.Fatalf("ответ с повтором номера принят: %v", scores)
	}
}

// Чужой номер и недостача оценок — ошибка, а не нули на месте недостающих.
func TestRerankRejectsIncompleteAnswer(t *testing.T) {
	srv, _ := server(t, func(rerankRequest) any {
		return results(result{0, 0.9}, result{7, 0.8})
	})
	r := New(Options{URL: srv.URL, Timeout: 5 * time.Second})
	if _, err := r.Rerank(context.Background(), "q", []string{"а", "б"}); err == nil {
		t.Fatal("неполный ответ принят")
	}
}

// Ошибка службы доходит до вызывающего вместе с её текстом: «500» без
// объяснения заставляет лезть в журнал сервера.
func TestRerankReportsServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not loaded", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	r := New(Options{URL: srv.URL, Timeout: 5 * time.Second})
	_, err := r.Rerank(context.Background(), "q", []string{"а"})
	if err == nil || !strings.Contains(err.Error(), "model not loaded") {
		t.Fatalf("ошибка службы потеряна: %v", err)
	}
}

// Check проверяет службу настоящим вызовом на двух документах.
func TestCheck(t *testing.T) {
	srv, _ := server(t, func(req rerankRequest) any {
		var rs []result
		for i := range req.Documents {
			rs = append(rs, result{i, float64(i)})
		}
		return results(rs...)
	})
	if err := New(Options{URL: srv.URL, Timeout: 5 * time.Second}).Check(context.Background()); err != nil {
		t.Fatalf("исправная служба не прошла проверку: %v", err)
	}
}
