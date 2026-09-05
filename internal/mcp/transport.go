package mcp

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/signal"
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
func ServeStdio(srv *Server, verbose bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return Serve(ctx, srv, os.Stdin, os.Stdout, verbose)
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
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		if verbose {
			fmt.Fprintf(os.Stderr, "→ %s\n", trim(string(line), 200))
		}
		resp := srv.Handle(ctx, line)
		if resp == nil {
			continue // уведомление: ответа быть не должно
		}
		if verbose {
			fmt.Fprintf(os.Stderr, "← %s\n", trim(string(resp), 200))
		}
		if _, err := out.Write(append(resp, '\n')); err != nil {
			return err
		}
		if err := out.Flush(); err != nil {
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
		w.Header().Set("Content-Type", "application/json")
		if resp == nil {
			// Уведомление: по протоколу ответа нет, но HTTP требует хоть чего-то.
			w.WriteHeader(http.StatusAccepted)
			return
		}
		_, _ = w.Write(resp)
	})
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
