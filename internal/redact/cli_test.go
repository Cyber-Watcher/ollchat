package redact

import (
	"bytes"
	"errors"
	"image"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Обвязка ключа --scan-redact: имена итогов, права, коды выхода, уборка
// временного. Настоящий tesseract не нужен: на PATH кладётся поддельный —
// сценарий оболочки, который на --list-langs отвечает «eng, rus», а на
// распознавание печатает заданный tsv. Все значения выдуманы.

// fakeTesseract кладёт поддельный tesseract первым на PATH; body — что он
// делает при распознавании.
func fakeTesseract(t *testing.T, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("поддельный tesseract — сценарий оболочки")
	}
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in --list-langs) printf 'List of available languages (2):\\neng\\nrus\\n'; exit 0;; esac\n" + body
	if err := os.WriteFile(filepath.Join(bin, "tesseract"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// tsvWords — команда, печатающая tsv со словами одной строки.
func tsvWords(words ...string) string {
	var b strings.Builder
	b.WriteString(`printf 'level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n`)
	for i, w := range words {
		b.WriteString(`5\t1\t1\t1\t1\t` + string(rune('1'+i)) + `\t` + string(rune('1'+i)) + `00\t100\t90\t30\t95\t` + w + `\n`)
	}
	b.WriteString("'\n")
	return b.String()
}

// scanFile — скан из одной белой страницы в два дюйма, собранный нашим же PDF.
func scanFile(t *testing.T) string {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 600, 600))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	p := Page{Width: 144, Height: 144, Image: img}
	data, err := PDF([]Page{p}, []image.Image{p.Image})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "скан.pdf")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Проверка нашла скрытое: код выхода 3 возвращается ошибкой, а не os.Exit
// посреди библиотеки, обезличенные файлы лежат с пометкой UNVERIFIED и с
// правами 0600, а копий со всеми данными по умолчанию нет.
func TestRunCLILeakExitCode(t *testing.T) {
	// Поддельный tesseract и в замазанном итоге «видит» тот же номер: утечка.
	fakeTesseract(t, tsvWords("MRN:", "AB1234567"))
	in := scanFile(t)
	dir := filepath.Dir(in)
	var stdout, stderr bytes.Buffer
	err := RunCLI(&stdout, &stderr, in, nil)
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 3 {
		t.Fatalf("ожидался код выхода 3, получено %v\n%s%s", err, stdout.String(), stderr.String())
	}
	for _, name := range []string{"скан.redacted.UNVERIFIED.pdf", "скан.redacted.UNVERIFIED.md"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("нет %s: %v", name, err)
			continue
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("права %s: %o", name, info.Mode().Perm())
		}
	}
	for _, name := range []string{"скан.redacted.pdf", "скан.redacted.md", "скан.ocr.pdf", "скан.ocr.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("лишний итог %s", name)
		}
	}
	if strings.Contains(stdout.String(), "AB1234567") {
		t.Error("скрытое значение попало в вывод на экран")
	}

	// Без находок — обычные имена и без ошибки.
	fakeTesseract(t, tsvWords("Glucose", "5.4"))
	in = scanFile(t)
	if err := RunCLI(io.Discard, io.Discard, in, nil); err != nil {
		t.Fatalf("без персональных данных: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(in), "скан.redacted.pdf")); err != nil {
		t.Errorf("проверенный итог: %v", err)
	}
}

// Ctrl+C посреди распознавания прерывает работу, а не процесс: временные
// картинки страниц — с персональными данными — убираются, а код выхода 130
// возвращается ошибкой. Прежде сигнал убивал процесс, и каталог
// ollchat-redact-* со страницами оставался во временном.
func TestRunCLIInterruptCleansTemp(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "started")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("именованный канал: %v", err)
	}
	t.Setenv("FAKE_STARTED", fifo)
	fakeTesseract(t, "echo started > \"$FAKE_STARTED\"\nexec sleep 30\n")
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	in := scanFile(t)

	done := make(chan error, 1)
	go func() { done <- RunCLI(io.Discard, io.Discard, in, nil) }()

	// Распознавание началось — значит, перехват сигнала уже стоит.
	started := make(chan error, 1)
	go func() {
		f, err := os.Open(fifo)
		if err == nil {
			_, err = io.ReadAll(f)
			f.Close()
		}
		started <- err
	}()
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case err := <-done:
		t.Fatalf("RunCLI кончился до распознавания: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("распознавание не началось")
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		var exit *ExitError
		if !errors.As(err, &exit) || exit.Code != 130 {
			t.Errorf("ожидался код выхода 130, получено %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("RunCLI не вернулся после Ctrl+C")
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("во временном каталоге осталось %s", e.Name())
	}
}
