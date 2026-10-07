package epub

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Обстрел разбора книг: go test -fuzz=FuzzEPUB ./internal/epub/
//
// Без -fuzz прогоняются только затравки — обычный быстрый тест. Находка —
// паника (recover превращает её в ErrDamaged) и вход, который разбирается
// дольше fuzzTimeout.

const fuzzTimeout = 10 * time.Second

func FuzzEPUB(f *testing.F) {
	f.Add(sampleBook(f, true))
	f.Add(sampleBook(f, false))
	f.Add(buildEPUB(f, true, map[string]string{
		"META-INF/container.xml": container,
		"OEBPS/content.opf": strings.Replace(opf, `<item href="toc.ncx"`,
			`<item href="nav.xhtml" id="nav" properties="nav" media-type="application/xhtml+xml"/><item href="toc.ncx"`, 1),
		"OEBPS/ch1.xhtml":      chapter1,
		"OEBPS/ch2.xhtml":      chapter2 + `<pre>  код  </pre><h2>Заголовок</h2><svg><image href="images/pic.png"/></svg>`,
		"OEBPS/nav.xhtml":      `<html><body><nav><ol><li><a href="ch1.xhtml#a">Первая</a></li></ol></nav></body></html>`,
		"OEBPS/toc.ncx":        ncx,
		"OEBPS/images/pic.png": pngPixel,
	}))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip("вход больше мегабайта")
		}
		var live atomic.Bool
		live.Store(true)
		defer live.Store(false)
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, err := Extract(data, Options{})
			_, ierr := ExtractImages(data, ImageOptions{})
			for _, e := range []error{err, ierr} {
				if errors.Is(e, ErrDamaged) && live.Load() {
					t.Errorf("паника: %v", e)
				}
			}
		}()
		select {
		case <-done:
		case <-time.After(fuzzTimeout):
			t.Fatalf("книга разбирается дольше %v", fuzzTimeout)
		}
	})
}
