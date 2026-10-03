package redact

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"

	"github.com/signintech/gopdf"
)

// jpegQuality — качество цветных страниц. Скан уже прошёл через JPEG
// сканера; 85 не добавляет заметных потерь и держит размер рядом с исходным.
const jpegQuality = 85

// PDF собирает страницы-картинки в документ того же размера: чёрно-белые —
// PNG (Flate, DeviceGray), серые с полутонами и цветные — JPEG. Сведений о документе (/Info) нет:
// в них могли бы остаться имя файла, автор или программа сканера. Текстового
// слоя нет тоже — под чёрным прямоугольником ничего не лежит.
func PDF(pages []Page, imgs []image.Image) ([]byte, error) {
	if len(pages) == 0 || len(pages) != len(imgs) {
		return nil, fmt.Errorf("страниц %d, картинок %d", len(pages), len(imgs))
	}
	pdf := &gopdf.GoPdf{}
	pdf.Start(gopdf.Config{Unit: gopdf.UnitPT, PageSize: gopdf.Rect{W: pages[0].Width, H: pages[0].Height}})
	for i, p := range pages {
		size := &gopdf.Rect{W: p.Width, H: p.Height}
		pdf.AddPageWithOption(gopdf.PageOption{PageSize: size})
		var buf bytes.Buffer
		var err error
		if g, gray := imgs[i].(*image.Gray); gray && bilevel(g) {
			err = png.Encode(&buf, imgs[i])
		} else {
			err = jpeg.Encode(&buf, imgs[i], &jpeg.Options{Quality: jpegQuality})
		}
		if err != nil {
			return nil, fmt.Errorf("страница %d: %w", i+1, err)
		}
		holder, err := gopdf.ImageHolderByBytes(buf.Bytes())
		if err != nil {
			return nil, fmt.Errorf("страница %d: %w", i+1, err)
		}
		if err := pdf.ImageByHolder(holder, 0, 0, size); err != nil {
			return nil, fmt.Errorf("страница %d: %w", i+1, err)
		}
	}
	// GetBytesPdf при ошибке зовёт log.Fatalf — только вариант с ошибкой.
	return pdf.GetBytesPdfReturnErr()
}

// bilevel — в серой странице только чёрное и белое (скан CCITT): такая
// сжимается PNG лучше JPEG и без потерь. Страница с полутонами (серый скан
// из JPEG) в PNG раздувалась вчетверо — 1 МБ исходника против 4,3 МБ итога
// (синтетический набор 03.10.2026), — поэтому она идёт в серый JPEG.
func bilevel(g *image.Gray) bool {
	b := g.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for _, v := range g.Pix[g.PixOffset(b.Min.X, y):][:b.Dx()] {
			if v != 0 && v != 255 {
				return false
			}
		}
	}
	return true
}

// WriteFile пишет файл через временный рядом и переименование: прерванная
// запись не оставляет полфайла на месте готового.
func WriteFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
