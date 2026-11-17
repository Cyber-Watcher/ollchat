package clipboard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Буфер обмена предлагает список типов, и выбрать надо тот, который Ollama
// действительно примет, а не первый попавшийся.
func TestPickPrefersPNG(t *testing.T) {
	cases := []struct {
		name    string
		offered []string
		want    string
	}{
		{"только PNG", []string{"TARGETS", "image/png"}, "image/png"},
		{"PNG предпочтительнее JPEG", []string{"image/jpeg", "image/png"}, "image/png"},
		{"JPEG, если PNG нет", []string{"TARGETS", "image/jpeg"}, "image/jpeg"},
		{"регистр и пробелы не мешают", []string{" IMAGE/PNG "}, "image/png"},
		{"только текст", []string{"TARGETS", "UTF8_STRING", "text/plain"}, ""},
		{"пусто", nil, ""},
		{"неподдерживаемая картинка", []string{"image/webp", "image/tiff"}, ""},
	}
	for _, c := range cases {
		if got := pick(c.offered); got != c.want {
			t.Errorf("%s: pick(%v) = %q, ожидалось %q", c.name, c.offered, got, c.want)
		}
	}
}

// TestDetectDistinguishesSessionAndHelper закрепляет, что «сессии нет вовсе»
// и «сессия есть, а утилиты нет» — разные случаи.
//
// Различать их по тексту ошибки нельзя: совет пользователю в этих случаях
// разный (проброс X11 против установки пакета), и интерфейс выбирает подсказку
// именно по виду ошибки.
func TestDetectDistinguishesSessionAndHelper(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")
	if _, err := detect(); !errors.Is(err, ErrNoSession) {
		t.Errorf("без графических переменных ожидался ErrNoSession, получено: %v", err)
	}

	// Сессия есть, а утилиты в PATH нет: подменяем PATH на пустой каталог.
	t.Setenv("DISPLAY", ":0")
	t.Setenv("PATH", t.TempDir())
	_, err := detect()
	if !errors.Is(err, ErrNoHelper) {
		t.Errorf("при заданном DISPLAY и отсутствии xclip ожидался ErrNoHelper, получено: %v", err)
	}
	if errors.Is(err, ErrNoSession) {
		t.Error("отсутствие утилиты не должно выглядеть как отсутствие сессии")
	}
}

// TestDetectWriterSessionIsRecognisable: у записи тот же случай должен быть
// отличим и как «писать нечем», и как «сессии нет».
func TestDetectWriterSessionIsRecognisable(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")
	_, err := detectWriter()
	if !errors.Is(err, ErrNoWriter) {
		t.Errorf("ожидался ErrNoWriter, получено: %v", err)
	}
	if !errors.Is(err, ErrNoSession) {
		t.Errorf("ожидался и ErrNoSession, получено: %v", err)
	}
}

// fakeXclip ставит в PATH подменный xclip: на TARGETS он отвечает image/png
// (или молчит 30 с, если slowTargets), на чтение картинки — ждёт readSleep.
// Так медленный канал ssh -X воспроизводится без графической сессии. Ждёт
// только exec sleep: дочерний sleep держал бы вывод, и обрыв по сроку ждал бы его.
func fakeXclip(t *testing.T, slowTargets bool, readSleep string) {
	t.Helper()
	dir := t.TempDir()
	targets := "echo image/png"
	if slowTargets {
		targets = "exec sleep 30"
	}
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *TARGETS*) " + targets + " ;;\n" +
		"  *) exec sleep " + readSleep + " ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "xclip"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", ":0")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestReadImageSlowImageIsTimeout: картинка не уложилась в срок вызывающего —
// ошибка ErrImageTimeout, а не «signal: killed» без объяснения (замер владельца:
// снимок по ssh -X шёл дольше прежних 5 с).
func TestReadImageSlowImageIsTimeout(t *testing.T) {
	fakeXclip(t, false, "30")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := ReadImage(ctx, 0)
	if !errors.Is(err, ErrImageTimeout) {
		t.Fatalf("ожидался ErrImageTimeout, получено: %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("обрыв по сроку занял %v — утилита не снята вовремя", took)
	}
}

// TestReadImageTargetsKeepShortLimit: длинный срок картинки не растягивает
// ожидание списка форматов — молчащий буфер виден через typesTimeout, и это не
// «картинки нет» и не «картинка не пришла».
func TestReadImageTargetsKeepShortLimit(t *testing.T) {
	fakeXclip(t, true, "0")
	prev := typesTimeout
	typesTimeout = 300 * time.Millisecond
	t.Cleanup(func() { typesTimeout = prev })

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	start := time.Now()
	_, err := ReadImage(ctx, 0)
	if err == nil || errors.Is(err, ErrNoImage) || errors.Is(err, ErrImageTimeout) {
		t.Fatalf("ожидалась ошибка срока списка форматов, получено: %v", err)
	}
	if !strings.Contains(err.Error(), "список форматов") {
		t.Errorf("в ошибке нет слов «список форматов»: %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("список форматов ждали %v при сроке 300ms", took)
	}
}
