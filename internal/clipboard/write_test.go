package clipboard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDetectWriterNeedsSession(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")

	_, err := detectWriter()
	if !errors.Is(err, ErrNoWriter) {
		t.Fatalf("без графической сессии ожидался ErrNoWriter, получено: %v", err)
	}
}

func TestWriteTextWithoutSessionIsErrNoWriter(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")

	err := WriteText(context.Background(), "текст")
	if !errors.Is(err, ErrNoWriter) {
		t.Fatalf("ожидался ErrNoWriter, получено: %v", err)
	}
}

func TestFeedPassesTextToStdin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	want := "привет\nмир"
	if err := feed(ctx, "sh", []byte(want), "-c", "cat > "+path); err != nil {
		t.Fatalf("подача текста: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("чтение результата: %v", err)
	}
	if string(got) != want {
		t.Errorf("на вход утилиты пришло %q, ожидалось %q", string(got), want)
	}
}

// TestFeedDoesNotWaitForBackgroundChild закрепляет главное требование к записи:
// не ждать потомка, оставшегося в фоне.
//
// Утилиты буфера обмена именно так и устроены — владелец X11 selection обязан
// жить, пока держит буфер. Замерено на настоящем xclip: с Stdout = nil вызов
// возвращается за 0.01 с, а с bytes.Buffer не возвращается вовсе, потому что
// Wait ждёт копирующую горутину, а конец канала держит фоновый потомок.
// Контекстный таймаут от этого не спасает. Если кто-то заведёт здесь буферы,
// приложение будет зависать прямо на копировании — этот тест поймает.
func TestFeedDoesNotWaitForBackgroundChild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- feed(ctx, "sh", []byte("текст"), "-c", "sleep 30 & exit 0")
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("подача текста: %v", err)
		}
		if el := time.Since(start); el > 2*time.Second {
			t.Errorf("ждали фонового потомка %v — запись обязана возвращаться сразу", el)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("запись в буфер обмена ждёт фонового потомка — интерфейс с таким кодом зависнет")
	}
}

func TestFeedRespectsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := feed(ctx, "sh", []byte("текст"), "-c", "sleep 30"); err == nil {
		t.Fatal("на отменённом контексте ожидалась ошибка")
	}
}

func TestSessionKind(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("DISPLAY", ":0")
	if got := sessionKind(); got != "wayland" {
		t.Errorf("при заданном WAYLAND_DISPLAY ожидалась wayland, получено %q", got)
	}

	t.Setenv("WAYLAND_DISPLAY", "")
	if got := sessionKind(); got != "x11" {
		t.Errorf("при заданном DISPLAY ожидалась x11, получено %q", got)
	}

	t.Setenv("DISPLAY", "")
	if got := sessionKind(); got != "" {
		t.Errorf("без переменных ожидалась пустая строка, получено %q", got)
	}
}
