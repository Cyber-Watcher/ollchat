package mcp

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/signal"
	"sync"
	"syscall"

	"github.com/Cyber-Watcher/ollchat/internal/kbserve"
	"os"
	"strings"
)

// Два способа доставки сообщений.
//
// stdio — как работает большинство клиентов MCP: клиент сам запускает программу
// и разговаривает с ней через её же потоки ввода-вывода. Сообщения разделяются
// переводом строки.
//
// HTTP — постоянная служба: один сервер, много клиентов, юнит systemd. Здесь
// сообщение приходит телом POST, ответ уходит телом ответа.

// ServeStdio ведёт разговор через стандартные потоки.
//
// В этом режиме **в stdout нельзя писать ничего, кроме ответов протокола**:
// любая посторонняя строка ломает разбор у клиента. Поэтому все пояснения
// уходят в поток ошибок, и потому же у сервера нет «приветствия».
//
// Набор инструментов зашит в бинарь, а процесс stdio живёт столько же, сколько
// сеанс клиента. Поэтому служба следит за своим файлом: когда ~/bin/ollmcp
// подменён, она в паузе между запросами перезапускает себя тем же exec на тех же
// потоках; если набор при этом сменился, первым сообщением шлёт клиенту
// «набор сменился» (этап 109, А1).
// Где слежка не поддержана (не Linux), остаётся прежнее поведение.
func ServeStdio(srv *Server, verbose bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if prev := os.Getenv(reexecEnv); prev != "" {
		// Клиент уже поздоровался с прежним экземпляром — второго initialize не будет.
		_ = os.Unsetenv(reexecEnv)
		srv.markInited()
		fp := srv.Fingerprint()
		fmt.Fprintf(os.Stderr, "ollmcp: перезапущен на новом бинаре, набор %s (был %s)\n", fp, prev)
		if fp != prev {
			if _, err := os.Stdout.Write(append(append([]byte{}, toolsChanged...), '\n')); err != nil {
				return err
			}
		}
	}
	return serveStdio(ctx, srv, verbose)
}

// reexecEnv — метка «этот процесс — продолжение прежнего после exec»;
// значение — отпечаток набора прежнего: совпал — клиенту перечитывать нечего.
const reexecEnv = "OLLMCP_REEXEC"

// handleLine отвечает на одну строку протокола и сразу отправляет ответ.
func handleLine(ctx context.Context, srv *Server, line []byte, out *bufio.Writer, verbose bool) error {
	if len(strings.TrimSpace(string(line))) == 0 {
		return nil
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "→ %s\n", trim(string(line), 200))
	}
	resp := srv.Handle(ctx, line)
	if resp == nil {
		return nil // уведомление: ответа быть не должно
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "← %s\n", trim(string(resp), 200))
	}
	if _, err := out.Write(append(resp, '\n')); err != nil {
		return err
	}
	return out.Flush()
}

// Serve ведёт разговор по строкам через произвольные потоки: то же, что
// ServeStdio, но без привязки к процессу — так режим stdio проверяется тестом
// (этап 91, R5.8). Возвращается по EOF на входе или по отмене контекста.
func Serve(ctx context.Context, srv *Server, r io.Reader, w io.Writer, verbose bool) error {
	in := bufio.NewReaderSize(r, 1<<20)
	out := bufio.NewWriter(w)
	defer out.Flush()

	// Чтение вынесено в отдельную горутину, и это не украшение.
	//
	// Раньше цикл стоял прямо на readLine(os.Stdin) — блокирующем чтении,
	// которое отмена контекста не будит. Обработчик сигналов при этом
	// **подавлял** обычное поведение Go: без него SIGTERM завершил бы процесс
	// сам, а с ним сигнал молча уходил в никуда. Служба переставала слушаться
	// чего-либо, кроме SIGKILL и закрытия stdin. Проверено на живом процессе
	// 25.08.2026: два SIGTERM подряд не сделали ничего.
	//
	// Горутина остаётся висеть на чтении, когда мы уходим по сигналу, — и это
	// нормально: процесс всё равно завершается следующей строкой.
	type incoming struct {
		line []byte
		err  error
	}
	lines := make(chan incoming, 1)
	go func() {
		for {
			line, err := readLine(in)
			lines <- incoming{line: line, err: err}
			if err != nil {
				return
			}
		}
	}()

	for {
		var msg incoming
		select {
		case <-ctx.Done():
			// Клиент нас не закрывал — попросил уйти человек или служба.
			return nil
		case msg = <-lines:
		}
		line, err := msg.line, msg.err
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := handleLine(ctx, srv, line, out, verbose); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
	}
}

// readLine читает одну строку целиком, какой бы длинной она ни была.
// Ответ поиска по книгам легко перевалит за буфер по умолчанию.
func readLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		part, isPrefix, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		buf = append(buf, part...)
		if !isPrefix {
			return buf, nil
		}
	}
}

// MountHTTP подключает вход MCP к готовому маршрутизатору.
//
// Не «поднимает службу», а именно подключает маршрут: на том же порту рядом
// живёт вход данных для клиентов ollchat (`internal/kbserve`), и заводить ради
// двух протоколов две службы с двумя портами и двумя ключами было бы незачем.
//
// Проверка ключа — та же самая, общая с входом данных: разных дверей с разными
// замками в одной службе быть не должно.
func MountHTTP(mux *http.ServeMux, srv *Server, token string, verbose bool) {
	sessions := &sessionBook{seen: map[string]bool{}}
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if !kbserve.Auth(r, token) {
			// Без подробностей: чем меньше сказано, тем меньше подсказано.
			http.Error(w, "нужен ключ доступа", http.StatusUnauthorized)
			if verbose {
				fmt.Fprintf(os.Stderr, "× %s без ключа\n", r.RemoteAddr)
			}
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "нужен POST", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err != nil {
			http.Error(w, "тело запроса не прочиталось", http.StatusBadRequest)
			return
		}
		if verbose {
			fmt.Fprintf(os.Stderr, "→ %s %s\n", r.RemoteAddr, trim(string(body), 200))
		}
		resp := srv.Handle(r.Context(), body)
		if resp == nil {
			// Уведомление: по протоколу ответа нет, но HTTP требует хоть чего-то.
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if isInitialize(body) {
			id := randomHex(12)
			sessions.known(id)
			w.Header().Set(sessionHeader, id)
		} else if sessions.stale(r) {
			// Сеанс начат у прежнего экземпляра службы: набор мог смениться.
			// Ответ уходит потоком SSE: сперва «набор сменился», потом сам ответ —
			// так протокол разрешает слать уведомления в ответ на любой запрос.
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			fmt.Fprintf(w, "event: message\ndata: %s\n\nevent: message\ndata: %s\n\n", toolsChanged, resp)
			if verbose {
				fmt.Fprintf(os.Stderr, "↻ %s: сеанс прежнего экземпляра, клиент уведомлён\n", r.RemoteAddr)
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(resp)
	})
}

// Как HTTP-клиент узнаёт о смене набора (этап 109, А1).
//
// Своего канала для уведомлений у службы нет: HTTP отвечает только на запрос.
// Поэтому служба выдаёт на initialize номер сеанса, клиент шлёт его заголовком
// в каждом запросе, а служба помнит номера, выданные ЭТИМ экземпляром. Пришёл
// незнакомый номер — сеанс начат до перезапуска, и его список мог устареть:
// ближайший ответ несёт уведомление, один раз на сеанс.
//
// Почему не сравнение отпечатков. Первый вариант (04.10.2026) клал в номер
// отпечаток набора на момент initialize и уведомлял при несовпадении. Живая
// проба его опровергла: после уведомления клиент перечитывает список, а номер
// остаётся прежним. Подмена A→B уведомила, возврат B→A — нет: отпечаток в номере
// снова «совпал», а у клиента остался набор B. Уведомлять всех после каждого
// перезапуска стоит одного лишнего tools/list на сеанс и не промахивается.
//
// Неизвестный номер не отвергается (протокол разрешил бы ответить 404
// и заставить клиента здороваться заново), потому что вызов важнее: клиент
// продолжает работать, а свежий список получает.
//
// Без заголовка — старый клиент или свой скрипт с curl — ответ прежний,
// простой JSON: скрипты разбирают его как есть. Уведомление уходит только
// тому, кто сам сказал в Accept, что понимает text/event-stream.
const sessionHeader = "Mcp-Session-Id"

// sessionBook — номера сеансов, которые этот экземпляр выдал сам или о прежних
// которых уже уведомил. Пропадает с процессом — так и задумано.
type sessionBook struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (s *sessionBook) known(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.seen) > 4096 {
		s.seen = map[string]bool{} // сеансов единицы; это защита от мусора, а не учёт
	}
	s.seen[id] = true
}

// stale — нужно ли этому запросу уведомление о смене набора.
func (s *sessionBook) stale(r *http.Request) bool {
	id := r.Header.Get(sessionHeader)
	if id == "" || !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		return false
	}
	s.mu.Lock()
	seen := s.seen[id]
	s.mu.Unlock()
	if seen {
		return false
	}
	s.known(id)
	return true
}

// isInitialize — рукопожатие ли это.
func isInitialize(body []byte) bool {
	var req struct {
		Method string `json:"method"`
	}
	return json.Unmarshal(body, &req) == nil && req.Method == "initialize"
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Info — что отдаётся в /health про часть MCP.
func Info(srv *Server) map[string]any {
	return map[string]any{
		"name": serverName, "version": serverVersion,
		"protocol": protocolVersion, "tools": len(srv.list()),
	}
}

func loopback(host string) bool {
	if host == "" || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func trim(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
