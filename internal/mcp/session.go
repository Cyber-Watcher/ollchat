package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
)

// Разговор по строкам (stdio): вызов инструмента не держит разговор.
//
// Раньше строки обрабатывались строго по одной: долгий tools/call (холодный
// граф — десятки секунд, поиск с переранжированием) держал весь разговор,
// и клиент не получал ответа даже на ping, а по молчанию на ping клиенты MCP
// объявляют сервер мёртвым. Отменить такой вызов было нечем:
// notifications/cancelled молча пропускалось.
//
// Теперь tools/call идёт в своей горутине со своим контекстом. Рукопожатие,
// ping и список инструментов отвечаются сразу и по порядку, а
// notifications/cancelled отменяет вызов — и ответа на отменённый, как велит
// MCP, не будет. Ответы пишутся целой строкой, по одному: строки двух ответов
// не перемешаются. Порядок ответов на вызовы может не совпасть с порядком
// запросов — JSON-RPC это разрешает, клиент сводит их по id.
type session struct {
	srv     *Server
	verbose bool

	wmu    sync.Mutex // запись ответа целиком — одна за раз
	out    *bufio.Writer
	closed bool  // разговор окончен: поздние ответы не пишутся
	werr   error // первая ошибка записи из горутины вызова

	mu       sync.Mutex
	inflight map[string]*running // id вызова → его отмена
	active   atomic.Int32        // вызовов, ещё не записавших ответ
	wg       sync.WaitGroup
}

// running — вызов инструмента в работе.
type running struct {
	cancel    context.CancelFunc
	cancelled bool // отменён клиентом: ответа не будет
}

func newSession(srv *Server, out *bufio.Writer, verbose bool) *session {
	return &session{srv: srv, out: out, verbose: verbose, inflight: map[string]*running{}}
}

// handle разбирает одну строку: вызов инструмента уходит в свою горутину,
// отмена отменяет, остальное отвечается сразу.
func (s *session) handle(ctx context.Context, line []byte) error {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil
	}
	if s.verbose {
		fmt.Fprintf(os.Stderr, "→ %s\n", trim(string(line), 200))
	}
	var head struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			RequestID json.RawMessage `json:"requestId"`
		} `json:"params"`
	}
	if json.Unmarshal(line, &head) == nil {
		switch {
		case head.Method == "notifications/cancelled" && len(head.ID) == 0:
			s.cancel(head.Params.RequestID)
			return nil // уведомление: ответа нет
		case head.Method == "tools/call" && len(head.ID) > 0:
			s.start(ctx, idKey(head.ID), line)
			return nil
		}
	}
	return s.write(s.srv.Handle(ctx, line))
}

// start запускает вызов инструмента в своей горутине.
func (s *session) start(ctx context.Context, key string, line []byte) {
	cctx, cancel := context.WithCancel(ctx)
	c := &running{cancel: cancel}
	s.mu.Lock()
	s.inflight[key] = c
	s.mu.Unlock()
	s.active.Add(1)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.active.Add(-1)
		resp := s.srv.Handle(cctx, line)
		s.mu.Lock()
		if s.inflight[key] == c {
			delete(s.inflight, key)
		}
		cancelled := c.cancelled
		s.mu.Unlock()
		cancel()
		if cancelled {
			return // отменён клиентом: по MCP ответа на него не шлют
		}
		if err := s.write(resp); err != nil {
			s.wmu.Lock()
			if s.werr == nil {
				s.werr = err
			}
			s.wmu.Unlock()
		}
	}()
}

// cancel отменяет вызов по id из notifications/cancelled. Неизвестный id —
// вызов уже закончился или не начинался: делать нечего.
func (s *session) cancel(id json.RawMessage) {
	if len(id) == 0 {
		return
	}
	s.mu.Lock()
	c := s.inflight[idKey(id)]
	if c != nil {
		c.cancelled = true
	}
	s.mu.Unlock()
	if c != nil {
		c.cancel()
	}
}

// write отправляет ответ целой строкой; nil — отправлять нечего.
func (s *session) write(resp []byte) error {
	if resp == nil {
		return nil
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if s.closed {
		return nil
	}
	if s.verbose {
		fmt.Fprintf(os.Stderr, "← %s\n", trim(string(resp), 200))
	}
	if _, err := s.out.Write(append(resp, '\n')); err != nil {
		return err
	}
	return s.out.Flush()
}

// err — первая ошибка записи из горутины вызова: поток вывода сломан,
// и разговор пора заканчивать.
func (s *session) err() error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.werr
}

// idle — ни один вызов не ждёт записи ответа. Только в таком состоянии
// процесс можно подменять exec: ответ, не записанный до exec, пропал бы.
func (s *session) idle() bool { return s.active.Load() == 0 }

// wait дожидается всех начатых вызовов: вход кончился, а ответы клиент
// ещё читает.
func (s *session) wait() { s.wg.Wait() }

// flush досылает записанное — перед exec, который выбросил бы буфер.
func (s *session) flush() error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.out.Flush()
}

// close заканчивает разговор: поздние ответы уже никто не прочтёт.
func (s *session) close() error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.closed = true
	return s.out.Flush()
}

// idKey — id запроса как ключ: число и строка с теми же цифрами — разные id.
func idKey(id json.RawMessage) string {
	var b bytes.Buffer
	if json.Compact(&b, id) != nil {
		return string(id)
	}
	return b.String()
}
