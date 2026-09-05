package ollama

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Молчащий поток обрывается по сторожу.
//
// Это главный случай: 27.08.2026 запрос с боевого сервера провисел в очереди
// 9 часов 49 минут и всё это время держал единственный слот модели, из-за чего
// сервер был недоступен остальным. Ни одна прежняя настройка такого не ловила:
// chat_timeout ограничивает ожидание заголовков, а молчание уже начавшегося
// потока не ограничивал никто.
func TestChatStallAborts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush() // заголовки ушли, а дальше — тишина
		<-r.Context().Done()     // сервер молчит, пока клиент не оборвёт сам
	}))
	defer srv.Close()

	c := NewWithStall(srv.URL, time.Second, time.Second, 300*time.Millisecond, nil)
	started := time.Now()
	var gotErr error
	for ev := range c.Chat(context.Background(), ChatRequest{Model: "проба"}) {
		if ev.Kind == EventError {
			gotErr = ev.Err
		}
	}
	if gotErr == nil {
		t.Fatal("молчащий поток не оборвался — сторож не сработал")
	}
	if took := time.Since(started); took > 3*time.Second {
		t.Fatalf("обрыв занял %s — сторож сработал слишком поздно", took.Round(time.Millisecond))
	}
	if !strings.Contains(gotErr.Error(), "молчит") {
		t.Errorf("ошибка не объясняет причину: %v", gotErr)
	}
	// Обрыв по молчанию — беда сервера, а не наша: обмен стоит повторить.
	if !Retryable(gotErr) {
		t.Errorf("обрыв по молчанию должен допускать повтор: %v", gotErr)
	}
}

// Поток, который шлёт куски, не обрывается, даже если идёт дольше предела.
//
// Иначе сторож ломал бы нормальную работу: честный длинный ответ идёт минутами.
func TestChatKeepsAliveWhileStreaming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		for i := 0; i < 8; i++ {
			fmt.Fprintf(w, `{"message":{"role":"assistant","content":"%d"}}`+"\n", i)
			f.Flush()
			time.Sleep(120 * time.Millisecond) // молчание короче предела
		}
		fmt.Fprint(w, `{"done":true,"done_reason":"stop"}`+"\n")
		f.Flush()
	}))
	defer srv.Close()

	// Предел 300 мс, а весь ответ идёт около секунды: сторож не должен мешать.
	c := NewWithStall(srv.URL, time.Second, time.Second, 300*time.Millisecond, nil)
	var chunks int
	var gotErr error
	for ev := range c.Chat(context.Background(), ChatRequest{Model: "проба"}) {
		switch ev.Kind {
		case EventContent:
			chunks++
		case EventError:
			gotErr = ev.Err
		}
	}
	if gotErr != nil {
		t.Fatalf("поток с кусками оборвался: %v", gotErr)
	}
	if chunks < 8 {
		t.Fatalf("получено кусков %d, ожидалось 8", chunks)
	}
}

// Отмена человеком остаётся отменой, а не превращается в жалобу на сервер.
func TestChatCancelStaysCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := NewWithStall(srv.URL, time.Second, time.Second, 10*time.Second, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(150 * time.Millisecond); cancel() }()

	var gotErr error
	for ev := range c.Chat(ctx, ChatRequest{Model: "проба"}) {
		if ev.Kind == EventError {
			gotErr = ev.Err
		}
	}
	if !errors.Is(gotErr, ErrCanceled) {
		t.Fatalf("отмена человеком должна давать ErrCanceled, получено: %v", gotErr)
	}
}

// Нулевой предел выключает сторожа целиком: бывают машины, где модель
// загружается минутами, и там сторож только мешал бы.
func TestStallReaderZeroDisabled(t *testing.T) {
	rc := io.NopCloser(strings.NewReader("данные"))
	if got := newStallReader(rc, 0, func() {}); got != rc {
		t.Fatal("при нулевом пределе тело ответа должно возвращаться как есть")
	}
}

// TestStallReaderCloseTwice — повторный Close не должен закрывать канал дважды.
func TestStallReaderCloseTwice(t *testing.T) {
	rc := newStallReader(io.NopCloser(strings.NewReader("")), time.Minute, func() {})
	if err := rc.Close(); err != nil {
		t.Fatalf("первый Close: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("второй Close: %v", err)
	}
}
