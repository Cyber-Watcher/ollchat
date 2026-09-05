package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/session"
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
)

// Тесты агента ждать секунды между повторами не должны: пауза — шов.
func TestMain(m *testing.M) {
	retryPause = func(int) time.Duration { return 0 }
	os.Exit(m.Run())
}

// TestToolStatsCountRejectedAndDone — счёт вызовов за ход: запрещённая правилами
// команда считается отклонённой, разрешённая — выполненной.
func TestToolStatsCountRejectedAndDone(t *testing.T) {
	client, _ := fakeServer(t, []string{"rm -rf /", "echo ok"})
	r := fakeRunner(t, client)

	conv := session.New("")
	conv.Append(ollama.Message{Role: ollama.RoleUser, Content: "работай"})

	var stats *ToolStats
	for ev := range r.Run(context.Background(), conv) {
		switch ev.Kind {
		case EventToolConfirm:
			ev.Confirm.Reply <- AnswerYes
		case EventTurnDone:
			stats = ev.Tools
		}
	}
	if stats == nil {
		t.Fatal("EventTurnDone без счёта вызовов")
	}
	if stats.Calls != 2 || stats.Rejected != 1 || stats.Failed != 0 {
		t.Fatalf("счёт вызовов: %+v, ожидалось {2 1 0}", *stats)
	}
}

// TestEmitReleasesWhenConsumerGone — если интерфейс перестал читать события и
// отменил ход, горутина хода обязана завершиться, а не повиснуть на отправке.
func TestEmitReleasesWhenConsumerGone(t *testing.T) {
	// Сервер отдаёт длинный поток кусков: больше, чем вмещает буфер канала.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		enc := json.NewEncoder(w)
		for i := 0; i < 500; i++ {
			_ = enc.Encode(ollama.ChatResponse{Message: ollama.Message{Role: ollama.RoleAssistant, Content: "x"}})
		}
		_ = enc.Encode(ollama.ChatResponse{Done: true, DoneReason: "stop"})
	}))
	t.Cleanup(srv.Close)

	r := plainRunner(ollama.New(srv.URL, 10*time.Second, 0, nil), 0)
	conv := session.New("")
	conv.Append(ollama.Message{Role: ollama.RoleUser, Content: "вопрос"})

	ctx, cancel := context.WithCancel(context.Background())
	ch := r.Run(ctx, conv)
	<-ch // прочитали одно событие и ушли, не дочитывая
	cancel()

	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // канал закрыт: горутина хода завершилась
			}
			// События, успевшие лечь в буфер, дочитываем только здесь,
			// в проверке; сам ход после отмены больше не должен ничего ждать.
		case <-deadline:
			t.Fatal("ход не завершился после отмены: отправка события повисла")
		}
	}
}

// TestIncompleteAnswerIsRetried — поток, оборванный без завершающего чанка,
// повторяется так же, как явный сбой сервера.
func TestIncompleteAnswerIsRetried(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/x-ndjson")
		enc := json.NewEncoder(w)
		if calls == 1 {
			return // пустой ответ 200: ни куска, ни done
		}
		_ = enc.Encode(ollama.ChatResponse{Message: ollama.Message{Role: ollama.RoleAssistant, Content: "ответ"}})
		_ = enc.Encode(ollama.ChatResponse{Done: true, DoneReason: "stop"})
	}))
	t.Cleanup(srv.Close)

	r := plainRunner(ollama.New(srv.URL, 10*time.Second, 0, nil), 1)
	answer, retries, err := collect(t, r)
	if err != nil {
		t.Fatalf("ошибка вместо повтора: %v", err)
	}
	if retries != 1 || answer != "ответ" {
		t.Fatalf("повторов %d, ответ %q", retries, answer)
	}
}

// TestRetryPauseIsCancellable — отмена во время паузы между попытками
// завершает ход без третьего запроса.
func TestRetryPauseIsCancellable(t *testing.T) {
	prev := retryPause
	retryPause = func(int) time.Duration { return time.Hour }
	t.Cleanup(func() { retryPause = prev })

	client, calls := flakyServer(t, 5, false, false)
	r := plainRunner(client, 3)
	conv := session.New("")
	conv.Append(ollama.Message{Role: ollama.RoleUser, Content: "вопрос"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := r.Run(ctx, conv)
	var got error
	for ev := range ch {
		if ev.Kind == EventRetry {
			cancel()
		}
		if ev.Kind == EventError {
			got = ev.Err
		}
	}
	if *calls != 1 {
		t.Fatalf("запросов %d, ожидался один: отмена в паузе должна остановить повторы", *calls)
	}
	if got == nil {
		t.Fatal("после отмены ожидалась ошибка хода")
	}
}

// TestStepsJournal — журнал шагов получает по строке на обмен с моделью
// и на вызов инструмента, с исходом вызова.
func TestStepsJournal(t *testing.T) {
	client, _ := fakeServer(t, []string{"rm -rf /", "echo ok"})
	r := fakeRunner(t, client)
	pat, err := chatlog.ParsePattern("steps.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	r.Steps = steplog.New(t.TempDir(), pat, time.Now(), "test", true)
	r.Turn = "k7f3-01"
	defer r.Steps.Close()

	conv := session.New("")
	conv.Append(ollama.Message{Role: ollama.RoleUser, Content: "работай"})
	for ev := range r.Run(context.Background(), conv) {
		if ev.Kind == EventToolConfirm {
			ev.Confirm.Reply <- AnswerYes
		}
	}
	if err := r.Steps.LastError(); err != nil {
		t.Fatalf("журнал шагов: %v", err)
	}
	data, err := os.ReadFile(r.Steps.Path())
	if err != nil {
		t.Fatal(err)
	}
	var chats, tools int
	var outcomes []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		s, err := steplog.Parse([]byte(line))
		if err != nil {
			t.Fatalf("строка %q: %v", line, err)
		}
		if s.Turn != "k7f3-01" {
			t.Fatalf("строка без идентификатора обмена: %+v", s)
		}
		switch s.Kind {
		case steplog.KindChat:
			chats++
		case steplog.KindTool:
			tools++
			outcomes = append(outcomes, s.Outcome)
		}
	}
	// Два вызова инструмента — три обмена с моделью (после каждого вызова
	// и заключительный ответ).
	if chats != 3 || tools != 2 {
		t.Fatalf("обменов %d, вызовов %d; ожидалось 3 и 2", chats, tools)
	}
	if outcomes[0] != steplog.OutcomeDenied || outcomes[1] != steplog.OutcomeOK {
		t.Fatalf("исходы: %v", outcomes)
	}
}
