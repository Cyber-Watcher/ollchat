package epub

import (
	"bytes"
	"image"
	"image/png"
	"path"

	_ "image/gif"  // распознавание размеров и чтение GIF
	_ "image/jpeg" // и JPEG
)

// Картинки книги.
//
// В отличие от PDF, доставать их почти нечего: это обычные файлы внутри
// архива, на которые ссылается разметка. Работы ровно две — найти ссылку
// и понять размер, чтобы отсеять значки и разделители.
//
// GIF перекладывается в PNG: модели ждут привычный растр. Векторные SVG
// пропускаются — их нечем показать, да и смысла в них для модели нет.

// Image — картинка из книги.
type Image struct {
	Section int    // раздел, в котором она встретилась
	Index   int    // номер внутри раздела, в порядке появления
	Name    string // имя файла в книге — по нему её узнают
	Width   int
	Height  int
	Format  string // "jpeg", "png" или "gif"
	Data    []byte
}

// Label — то же обозначение, что стоит меткой в тексте: «8.1».
func (im Image) Label() string { return itoa(im.Section) + "." + itoa(im.Index) }

// ImageOptions ограничивает выборку.
type ImageOptions struct {
	FirstSection int
	MaxSections  int
	MinWidth     int
	MinHeight    int
	MaxCount     int
}

// ExtractImages достаёт картинки книги в порядке чтения.
func ExtractImages(data []byte, opt ImageOptions) (out []Image, err error) {
	defer catch("извлечение картинок EPUB", &err)

	b, err := open(data)
	if err != nil {
		return nil, err
	}
	pkg, err := b.readPackage()
	if err != nil {
		return nil, err
	}
	first := opt.FirstSection
	if first < 1 {
		first = 1
	}
	last := len(pkg.spine)
	if opt.MaxSections > 0 && first-1+opt.MaxSections < last {
		last = first - 1 + opt.MaxSections
	}

	seen := map[string]bool{} // обложка и украшения повторяются в каждом разделе
	for i := first - 1; i < last && i < len(pkg.spine); i++ {
		href := pkg.spine[i]
		raw, err := b.read(href)
		if err != nil {
			continue
		}
		doc := parseHTML(raw, i+1)
		base := path.Dir(href)
		for idx, ref := range doc.imgs {
			name := cleanPath(path.Join(base, ref.href))
			if seen[name] {
				continue
			}
			seen[name] = true
			blob, err := b.read(name)
			if err != nil {
				continue
			}
			format, w, h, ok := imageInfo(blob)
			if !ok || w < opt.MinWidth || h < opt.MinHeight {
				continue
			}
			if format == "gif" {
				if conv, err := toPNG(blob); err == nil {
					blob, format = conv, "png"
				}
			}
			out = append(out, Image{
				Section: i + 1, Index: idx + 1,
				Name:  path.Base(name),
				Width: w, Height: h, Format: format, Data: blob,
			})
			if opt.MaxCount > 0 && len(out) >= opt.MaxCount {
				return out, nil
			}
		}
	}
	return out, nil
}

// imageInfo определяет формат и размер, не распаковывая картинку целиком.
func imageInfo(data []byte) (format string, w, h int, ok bool) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", 0, 0, false
	}
	switch format {
	case "jpeg", "png", "gif":
		return format, cfg.Width, cfg.Height, true
	}
	return "", 0, 0, false
}

// toPNG перекладывает картинку в PNG.
func toPNG(data []byte) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
