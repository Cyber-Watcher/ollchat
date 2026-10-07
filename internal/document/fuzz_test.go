package document

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/epub"
	"github.com/Cyber-Watcher/ollchat/internal/pdf"
)

// Обстрел определения вида файла и быстрой пробы: go test -fuzz=FuzzDetect
// ./internal/document/
//
// Файл пишется с расширением .md: без подписи PDF или EPUB он читается как
// текст, и обстрел проходит все три дороги. Без -fuzz прогоняются только
// затравки — обычный быстрый тест. Находка — паника (у разборов PDF и EPUB
// она становится ErrDamaged) и вход, который разбирается дольше fuzzTimeout.

const fuzzTimeout = 10 * time.Second

func FuzzDetect(f *testing.F) {
	f.Add(samplePDF())
	f.Add(sampleEPUB(f))
	f.Add([]byte("# Заголовок\n\nТекст © 2019, издание 2020 года.\n```\ncode\n```\n"))
	f.Add([]byte("PK\x03\x04 не архив"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip("вход больше мегабайта")
		}
		Detect(data)
		path := writeTemp(t, "doc.md", data)
		var live atomic.Bool
		live.Store(true)
		defer live.Store(false)
		done := make(chan struct{})
		go func() {
			defer close(done)
			if DetectFile(path) == KindNone {
				return
			}
			_, err := Probe(path, 0, 2)
			if (errors.Is(err, pdf.ErrDamaged) || errors.Is(err, epub.ErrDamaged)) && live.Load() {
				t.Errorf("паника: %v", err)
			}
		}()
		select {
		case <-done:
		case <-time.After(fuzzTimeout):
			t.Fatalf("файл разбирается дольше %v", fuzzTimeout)
		}
	})
}
