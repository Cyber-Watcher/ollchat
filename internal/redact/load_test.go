package redact

import (
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Страница без картинки рисуется программой pdftoppm, и путь к скану ей
// передаётся полным: относительный «-скан.pdf» она прочла бы как свой ключ.
// Настоящий pdftoppm не нужен — поддельный записывает свои аргументы и кладёт
// готовую картинку.
func TestRenderPassesAbsolutePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("поддельный pdftoppm — сценарий оболочки")
	}
	work := t.TempDir()
	pngPath := filepath.Join(work, "page.png")
	f, err := os.Create(pngPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewGray(image.Rect(0, 0, 60, 60))); err != nil {
		t.Fatal(err)
	}
	f.Close()
	argsPath := filepath.Join(work, "args")
	bin := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$FAKE_ARGS\"\nfor last; do :; done\ncp \"$FAKE_PNG\" \"$last.png\"\n"
	if err := os.WriteFile(filepath.Join(bin, "pdftoppm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_ARGS", argsPath)
	t.Setenv("FAKE_PNG", pngPath)

	dir := t.TempDir()
	doc := "%PDF-1.7\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n" +
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n" +
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 144 144] >>\nendobj\n" +
		"trailer\n<< /Root 1 0 R >>\n"
	if err := os.WriteFile(filepath.Join(dir, "-скан.pdf"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	pages, notes, err := Load(context.Background(), "-скан.pdf", 0)
	if err != nil || len(pages) != 1 || len(notes) != 1 {
		t.Fatalf("страницы: %v, %d, заметки %q", err, len(pages), notes)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "-скан.pdf")
	found := false
	for _, a := range strings.Split(strings.TrimSpace(string(args)), "\n") {
		if a == "-скан.pdf" {
			t.Errorf("pdftoppm получил относительный путь, похожий на ключ: %q", args)
		}
		found = found || a == want
	}
	if !found {
		t.Errorf("в аргументах pdftoppm нет полного пути %s: %q", want, args)
	}
}
