package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// slowTool — инструмент, который работает, пока его не отпустят или не отменят.
// started — вызов начался, cancelled — его контекст отменили.
func slowTool(release, started, cancelled chan struct{}) Tool {
	return Tool{
		Spec: ollama.ToolSpec{Name: "долгий", Description: "ждёт",
			Parameters: ollama.ToolParams{Type: "object"}},
		Run: func(ctx context.Context, _ map[string]any) (string, error) {
			started <- struct{}{}
			select {
			case <-release:
				return "дождался", nil
			case <-ctx.Done():
				cancelled <- struct{}{}
				return "", ctx.Err()
			}
		},
	}
}

// stdioPeer — разговор с Serve через трубы: строки на вход, ответы по одному.
type stdioPeer struct {
	t       *testing.T
	in      *io.PipeWriter
	replies chan map[string]any
	done    chan error
}

func startServe(t *testing.T, srv *Server) *stdioPeer {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &stdioPeer{t: t, in: inW, replies: make(chan map[string]any, 8), done: make(chan error, 1)}
	go func() {
		p.done <- Serve(context.Background(), srv, inR, outW, false)
		outW.Close()
	}()
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				t.Errorf("в выводе не JSON: %q", sc.Text())
				continue
			}
			p.replies <- m
		}
		close(p.replies)
	}()
	t.Cleanup(func() { inW.Close() })
	return p
}

func (p *stdioPeer) send(line string) {
	p.t.Helper()
	if _, err := io.WriteString(p.in, line+"\n"); err != nil {
		p.t.Fatal(err)
	}
}

func (p *stdioPeer) next() map[string]any {
	p.t.Helper()
	select {
	case m, ok := <-p.replies:
		if !ok {
			p.t.Fatal("вывод закрыт")
		}
		return m
	case <-time.After(5 * time.Second):
		p.t.Fatal("ответа нет 5 с")
	}
	return nil
}

// Долгий вызов не держит разговор: ping отвечается, пока вызов идёт.
// Прежде строки шли строго по одной, и клиент, не дождавшись ответа на ping,
// объявлял службу мёртвой.
func TestStdioPingDuringLongCall(t *testing.T) {
	release, started, cancelled := make(chan struct{}), make(chan struct{}, 1), make(chan struct{}, 1)
	p := startServe(t, server(slowTool(release, started, cancelled)))

	p.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"долгий","arguments":{}}}`)
	<-started
	p.send(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	if m := p.next(); m["id"] != float64(2) {
		t.Fatalf("первым ждали ответ на ping, пришло %v", m)
	}
	close(release)
	if m := p.next(); m["id"] != float64(1) {
		t.Fatalf("ждали ответ на вызов, пришло %v", m)
	}
}

// notifications/cancelled отменяет вызов: инструмент получает отмену через
// контекст, а ответа на отменённый вызов нет (MCP). Прежде уведомление
// молча пропускалось, и вызов шёл до конца.
func TestStdioCancelledCall(t *testing.T) {
	release, started, cancelled := make(chan struct{}), make(chan struct{}, 1), make(chan struct{}, 1)
	p := startServe(t, server(slowTool(release, started, cancelled)))

	p.send(`{"jsonrpc":"2.0","id":"x7","method":"tools/call","params":{"name":"долгий","arguments":{}}}`)
	<-started
	p.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"x7","reason":"передумал"}}`)
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("отмена до инструмента не дошла")
	}
	p.send(`{"jsonrpc":"2.0","id":8,"method":"ping"}`)
	if m := p.next(); m["id"] != float64(8) {
		t.Fatalf("на отменённый вызов пришёл ответ: %v", m)
	}
	p.in.Close()
	if err := <-p.done; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	for m := range p.replies {
		t.Errorf("лишний ответ: %v", m)
	}
}

// Вход кончился, а вызов ещё идёт: ответ дописывается до выхода — так
// скрипты кормят службу запросами через трубу.
func TestStdioEOFWaitsForCalls(t *testing.T) {
	quick := Tool{
		Spec: ollama.ToolSpec{Name: "неспешный", Parameters: ollama.ToolParams{Type: "object"}},
		Run: func(context.Context, map[string]any) (string, error) {
			time.Sleep(50 * time.Millisecond)
			return "готово", nil
		},
	}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"неспешный","arguments":{}}}` + "\n")
	var out strings.Builder
	if err := Serve(context.Background(), server(quick), in, &out, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"id":1`) || !strings.Contains(out.String(), "готово") {
		t.Fatalf("ответ на вызов потерян при закрытии входа: %q", out.String())
	}
}
