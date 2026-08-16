package epub

import (
	"archive/zip"
	"bytes"
	"errors"
	"math/rand"
	"strings"
	"testing"
)

// TestDamagedBooksDoNotPanic — книга приходит извне, и повреждённый архив
// не имеет права ронять приложение вместе с диалогом.
func TestDamagedBooksDoNotPanic(t *testing.T) {
	good := sampleBook(t, true)

	cases := []struct {
		name string
		data []byte
	}{
		{"обрезана пополам", good[:len(good)/2]},
		{"обрезана почти сразу", good[:60]},
		{"оглавление архива испорчено", bytes.ReplaceAll(good, []byte("PK\x01\x02"), []byte("PK\x01\x09"))},
		{"мусор вместо архива", append([]byte("PK\x03\x04"), bytes.Repeat([]byte{0xFF, 0x00}, 500)...)},
		{"нет container.xml", bytes.ReplaceAll(good, []byte("META-INF/container.xml"), []byte("META-INF/container.XXX"))},
		{"незакрытые теги в главе", bytes.ReplaceAll(good, []byte("</p>"), []byte("<p><p>"))},
		{"глубокая вложенность разметки", buildEPUB(t, true, map[string]string{
			"META-INF/container.xml": container,
			"OEBPS/content.opf":      opf,
			"OEBPS/ch1.xhtml":        "<html><body>" + strings.Repeat("<div>", 30000) + "текст",
			"OEBPS/ch2.xhtml":        chapter2,
			"OEBPS/toc.ncx":          ncx,
		})},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Extract(c.data, Options{}); err == nil {
				t.Log("разбор неожиданно удался — допустимо, лишь бы не паника")
			}
			if _, err := ExtractImages(c.data, ImageOptions{}); err == nil {
				t.Log("картинки извлеклись")
			}
		})
	}
}

// TestRandomlyCorruptedBook портит исправную книгу в случайных местах.
func TestRandomlyCorruptedBook(t *testing.T) {
	good := sampleBook(t, true)
	rnd := rand.New(rand.NewSource(20260815))
	for i := 0; i < 400; i++ {
		data := append([]byte(nil), good...)
		for n := rnd.Intn(30) + 1; n > 0; n-- {
			data[rnd.Intn(len(data))] = byte(rnd.Intn(256))
		}
		Extract(data, Options{})
		ExtractImages(data, ImageOptions{})
	}
}

// TestDamagedErrorIsRecognizable — сорвавшийся разбор должен быть отличим.
func TestDamagedErrorIsRecognizable(t *testing.T) {
	var err error
	func() {
		defer catch("проверка", &err)
		panic("нарочно")
	}()
	if !errors.Is(err, ErrDamaged) {
		t.Fatalf("ошибка не опознаётся как повреждение: %v", err)
	}
}

// zip нужен, чтобы тест собирался вместе с buildEPUB из соседнего файла.
var _ = zip.Store
