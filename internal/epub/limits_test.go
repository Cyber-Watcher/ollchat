package epub

import (
	"testing"
	"time"
)

// Пределы разбора книг на нарочно составленных файлах: каждый случай до
// правки ронял процесс нехваткой памяти (её recover не ловит) или выдавал
// несоразмерно много. Проверки ограничены по времени.

// bounded выполняет f не дольше d.
func bounded(t *testing.T, d time.Duration, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("разбор не уложился в %v — работа снова не ограничена", d)
	}
}

// hugeGIF — GIF в несколько десятков байт, где и экран, и кадр — 65535×65535.
func hugeGIF() string {
	b := []byte("GIF89a")
	b = append(b, 0xFF, 0xFF, 0xFF, 0xFF, 0x80, 0, 0) // экран 65535×65535, палитра на 2 цвета
	b = append(b, 0, 0, 0, 255, 255, 255)
	b = append(b, 0x2C, 0, 0, 0, 0, 0xFF, 0xFF, 0xFF, 0xFF, 0) // кадр во весь экран
	b = append(b, 2, 2, 0x4C, 0x01, 0, 0x3B)
	return string(b)
}

// GIF раскрывается при извлечении (в PNG), и декодер просил под кадр
// 65535×65535 четыре гигабайта раньше, чем читал точки. Такая картинка
// пропускается; JPEG и PNG с огромным заголовком — тоже: их раскроет тот,
// кому их отдадут.
func TestHugeImagesSkipped(t *testing.T) {
	book := buildEPUB(t, true, map[string]string{
		"META-INF/container.xml": container,
		"OEBPS/content.opf":      opf,
		"OEBPS/ch1.xhtml":        chapter1,
		"OEBPS/ch2.xhtml":        `<html><body><p>текст</p><img src="images/pic.png"/><img src="big.gif"/></body></html>`,
		"OEBPS/toc.ncx":          ncx,
		"OEBPS/images/pic.png":   pngPixel,
		"OEBPS/big.gif":          hugeGIF(),
	})
	bounded(t, 20*time.Second, func() {
		imgs, err := ExtractImages(book, ImageOptions{})
		if err != nil {
			t.Errorf("извлечение: %v", err)
			return
		}
		if len(imgs) != 1 || imgs[0].Name != "pic.png" {
			t.Errorf("картинки: %+v", imgs)
		}
	})
}
