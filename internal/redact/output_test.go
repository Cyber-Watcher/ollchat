package redact

import (
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Проверка нашла скрытое — обезличенные файлы ложатся под именами
// *.UNVERIFIED.*, а под обычными именами их нет: прежде такой файл ничем
// не отличался от проверенного, кроме кода выхода, которого в папке не видно.
func TestUnverifiedOutputsRenamed(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "скан.pdf")
	p := page(72, Rect{})
	res := &Result{Redacted: []image.Image{p.Image}, MD: "# Обезличенный документ\n", OCRMD: "# скан\n"}

	outs := DefaultOutputs(in, Formats{PDF: true, MD: true})
	written, err := WriteOutputs(outs, "скан", []Page{p}, res)
	if err != nil || len(written) != 2 || written[0].Path != outs.PDF {
		t.Fatalf("проверка без находок: %v, %+v", err, written)
	}

	res.Check = Check{LeaksMD: []string{"имя клиента"}}
	dir = t.TempDir()
	outs = DefaultOutputs(filepath.Join(dir, "скан.pdf"), Formats{PDF: true, MD: true, OCRMD: true})
	written, err = WriteOutputs(outs, "скан", []Page{p}, res)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"скан.redacted.UNVERIFIED.pdf", "скан.redacted.UNVERIFIED.md", "скан.ocr.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("нет %s: %v", name, err)
		}
	}
	for _, name := range []string{"скан.redacted.pdf", "скан.redacted.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("непроверенный итог лёг под обычным именем %s", name)
		}
	}
	if len(written) != 3 || !strings.Contains(written[0].What, "НЕ ПРОВЕРЕН") || strings.Contains(written[2].What, "НЕ ПРОВЕРЕН") {
		t.Errorf("сводка о записанном: %+v", written)
	}
	if !strings.Contains(res.Check.Line(), "UNVERIFIED") {
		t.Errorf("строка проверки не говорит об именах: %q", res.Check.Line())
	}

	// Исходник с пометкой в имени не затирается непроверенным итогом.
	src := filepath.Join(dir, "скан.UNVERIFIED.pdf")
	if err := (Outputs{PDF: filepath.Join(dir, "скан.pdf")}).Check(src); err == nil {
		t.Error("непроверенный итог затёр бы исходник — должен быть отказ")
	}
}

// Итоги пишутся с правами только владельцу: в распознанных копиях и разборе
// -report лежат все персональные данные документа. Прежде файл открывался
// всем на чтение (0644), в том числе поверх прежнего файла с правами 0600.
func TestWriteFileOwnerOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "новый каталог")
	path := filepath.Join(dir, "скан.ocr.md")
	if err := WriteFile(path, []byte("все данные")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("все данные ещё раз")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("права итога %o, а должны быть 600", mode)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("права созданного каталога: %v, %v", info.Mode().Perm(), err)
	}
}
