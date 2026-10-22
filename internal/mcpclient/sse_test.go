package mcpclient

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Разбор SSE по стандарту: три вида концов строк, комментарии, метка порядка
// байтов, многострочные data, поле без двоеточия, id с NUL, retry не числом,
// оборванное последнее событие.
func TestSSEParser(t *testing.T) {
	stream := byteOrderMark + ": комментарий\r\n" +
		"event: message\r\ndata: раз\r\ndata:  два\r\n\r\n" + // второй пробел — часть значения
		"id: 1\rdata\r\r" + // поле без двоеточия: data с пустым значением
		"id: a\x00b\nretry: 25\ndata: {\"x\":1}\n\n" +
		"retry: 1с\n" +
		"\n\n" + // пустые строки без данных — не событие
		"data: оборвано"
	sse := newSSE(strings.NewReader(stream))
	var got []string
	for {
		ev, err := sse.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ev.name+"|"+ev.data)
	}
	want := []string{"message|раз\n два", "|", `|{"x":1}`}
	if strings.Join(got, "§") != strings.Join(want, "§") {
		t.Fatalf("события:\n%q\nждали\n%q", got, want)
	}
	if sse.lastID != "1" {
		t.Fatalf("id с NUL должен быть пропущен: lastID %q", sse.lastID)
	}
	if sse.retry != 25*time.Millisecond {
		t.Fatalf("retry %s", sse.retry)
	}
}

// Предел ответа: ровно в предел — законно, на байт больше — ошибка, а не
// молча обрезанное тело.
func TestCapReader(t *testing.T) {
	read := func(body string, limit int64) error {
		_, err := io.ReadAll(&capReader{r: strings.NewReader(body), left: limit + 1})
		return err
	}
	if err := read("0123456789", 10); err != nil {
		t.Fatalf("ответ ровно в предел: %v", err)
	}
	if err := read("0123456789A", 10); !errors.Is(err, errTooBig) {
		t.Fatalf("ответ больше предела: %v", err)
	}
}

// Ключ: файл, переменная, команда — первый заданный; пустой источник —
// ошибка; ни одно сообщение не содержит значения.
func TestToken(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	const secret = "тайный-ключ-42"

	good := filepath.Join(dir, "good")
	if err := os.WriteFile(good, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := Token(ctx, good, "", ""); err != nil || got != secret {
		t.Fatalf("файл: %q, %v", got, err)
	}

	open := filepath.Join(dir, "open")
	if err := os.WriteFile(open, []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Token(ctx, open, "", ""); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("файл, читаемый всеми: %v", err)
	}

	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Token(ctx, empty, "", ""); err == nil || !strings.Contains(err.Error(), "пуст") {
		t.Fatalf("пустой файл: %v", err)
	}

	t.Setenv("OLLCHAT_MCPCLIENT_TEST_TOKEN", secret)
	if got, err := Token(ctx, "", "OLLCHAT_MCPCLIENT_TEST_TOKEN", "false"); err != nil || got != secret {
		t.Fatalf("переменная прежде команды: %q, %v", got, err)
	}
	t.Setenv("OLLCHAT_MCPCLIENT_TEST_EMPTY", "")
	if _, err := Token(ctx, "", "OLLCHAT_MCPCLIENT_TEST_EMPTY", ""); err == nil ||
		!strings.Contains(err.Error(), "OLLCHAT_MCPCLIENT_TEST_EMPTY") {
		t.Fatalf("пустая переменная: %v", err)
	}

	if got, err := Token(ctx, "", "", "printf '%s' '"+secret+"'"); err != nil || got != secret {
		t.Fatalf("команда: %q, %v", got, err)
	}
	if _, err := Token(ctx, "", "", "echo "+secret+" >&2; exit 3"); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("упавшая команда: %v", err)
	}
	if _, err := Token(ctx, "", "", "true"); err == nil {
		t.Fatal("команда, не напечатавшая ключа, принята")
	}

	if got, err := Token(ctx, " ", "", ""); err != nil || got != "" {
		t.Fatalf("без источников: %q, %v", got, err)
	}
}
