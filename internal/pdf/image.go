package pdf

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
)

// Извлечение картинок из документа.
//
// Отрисовать страницу целиком приложение не может — это полноценный движок
// вывода со шрифтами и векторной графикой. Зато вложенные картинки достаются
// прямо: схемы и диаграммы технических книг лежат в документе отдельными
// объектами, а у сканов картинка — это и есть вся страница.
//
// JPEG отдаётся как есть: он уже в том виде, в каком его ждёт модель.
// Остальное распаковывается по цветовой модели и перекладывается в PNG.

// Image — картинка, извлечённая из документа.
type Image struct {
	Page   int // страница, на которой она нарисована
	Index  int // номер на этой странице, в порядке отрисовки
	Width  int // размер в точках самой картинки
	Height int
	Format string // "jpeg" или "png"
	Data   []byte
}

// Label — то же обозначение, что стоит меткой в тексте: «44.2».
func (im Image) Label() string { return fmt.Sprintf("%d.%d", im.Page, im.Index) }

// ImageOptions ограничивает выборку.
type ImageOptions struct {
	FirstPage int // с какой страницы, начиная с 1
	MaxPages  int // сколько страниц просмотреть; 0 — все
	MinWidth  int // не брать мельче (иконки, линейки, подложки)
	MinHeight int
	MaxCount  int // сколько картинок вернуть; 0 — без ограничения
}

// ErrUnsupportedImage сообщает, что картинку не удалось перевести в обычный
// формат: так бывает с факсимильным сжатием и JPEG 2000.
var ErrUnsupportedImage = errors.New("формат картинки не поддерживается")

// ExtractImages достаёт картинки документа в порядке страниц.
//
// Обход идёт тем же путём, что и извлечение текста, — по операторам отрисовки.
// Это не прихоть: нумерация картинок обязана совпадать с метками «[рисунок N]»
// в тексте, иначе модель, попросив «рисунок 3», получит другой.
func ExtractImages(data []byte, opt ImageOptions) (out []Image, err error) {
	defer catch("извлечение картинок PDF", &err)

	doc, err := Open(data)
	if err != nil {
		return nil, err
	}
	pages := doc.Pages()
	first := opt.FirstPage
	if first < 1 {
		first = 1
	}
	last := len(pages)
	if opt.MaxPages > 0 && first-1+opt.MaxPages < last {
		last = first - 1 + opt.MaxPages
	}

	seen := map[int]bool{} // подложки и логотипы повторяются на каждой странице
	ex := newExtractor(doc)
	for i := first - 1; i < last && i < len(pages); i++ {
		ex.unit = i + 1
		ex.page(pages[i])
		for idx, im := range ex.images {
			if im.obj != 0 {
				if seen[im.obj] {
					continue
				}
				seen[im.obj] = true
			}
			if im.w < opt.MinWidth || im.h < opt.MinHeight {
				continue
			}
			format, raw, err := doc.imageBytes(im.stream)
			if err != nil {
				continue
			}
			out = append(out, Image{
				Page: i + 1, Index: idx + 1,
				Width: im.w, Height: im.h, Format: format, Data: raw,
			})
			if opt.MaxCount > 0 && len(out) >= opt.MaxCount {
				return out, nil
			}
		}
	}
	return out, nil
}

// imageBytes переводит поток картинки в готовый файл JPEG или PNG.
func (d *Document) imageBytes(s *Stream) (string, []byte, error) {
	filters := asArray(d.Resolve(s.Dict["Filter"]))
	last := Name("")
	if len(filters) > 0 {
		last, _ = d.Resolve(filters[len(filters)-1]).(Name)
	}

	switch last {
	case "DCTDecode":
		// JPEG уже готов: снимаем только обёртки, наложенные поверх него.
		data := s.Raw
		for i := 0; i < len(filters)-1; i++ {
			name, _ := d.Resolve(filters[i]).(Name)
			var err error
			data, err = d.applyFilter(name, data, nil)
			if err != nil {
				return "", nil, err
			}
		}
		return "jpeg", data, nil
	case "JPXDecode", "JBIG2Decode", "CCITTFaxDecode":
		return "", nil, ErrUnsupportedImage
	}

	// Всё остальное — несжатые отсчёты: собираем растр и кладём в PNG.
	data, err := d.Decode(s)
	if err != nil && len(data) == 0 {
		return "", nil, err
	}
	img, err := d.rasterize(s, data)
	if err != nil {
		return "", nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", nil, err
	}
	return "png", buf.Bytes(), nil
}

// rasterize собирает растр из отсчётов по цветовой модели картинки.
func (d *Document) rasterize(s *Stream, data []byte) (image.Image, error) {
	w, _ := toInt(d.Resolve(s.Dict["Width"]))
	h, _ := toInt(d.Resolve(s.Dict["Height"]))
	if w <= 0 || h <= 0 || w*h > 64<<20 {
		return nil, fmt.Errorf("неподходящий размер картинки %d×%d", w, h)
	}
	bpc, _ := toInt(d.Resolve(s.Dict["BitsPerComponent"]))
	mask := false
	if v, ok := d.Resolve(s.Dict["ImageMask"]).(bool); ok && v {
		mask, bpc = true, 1
	}
	if bpc == 0 {
		bpc = 8
	}

	cs := d.Resolve(s.Dict["ColorSpace"])
	comps, palette := d.colorSpace(cs)
	if mask {
		comps = 1
	}
	if comps == 0 {
		return nil, errors.New("неизвестная цветовая модель")
	}

	// Инверсия из /Decode: у масок [1 0] встречается сплошь и рядом.
	invert := false
	if dec := asArray(d.Resolve(s.Dict["Decode"])); len(dec) >= 2 {
		if v, ok := toFloat(d.Resolve(dec[0])); ok && v == 1 {
			invert = true
		}
	}

	rowBytes := (w*comps*bpc + 7) / 8
	if len(data) < rowBytes*h {
		// Битый или обрезанный поток: берём столько строк, сколько есть.
		h = len(data) / rowBytes
		if h == 0 {
			return nil, errors.New("данных картинки не хватает даже на строку")
		}
	}

	maxVal := float64(int(1)<<bpc - 1)
	sample := func(row []byte, i int) float64 {
		switch bpc {
		case 8:
			if i < len(row) {
				return float64(row[i])
			}
		case 16:
			if 2*i+1 < len(row) {
				return float64(row[2*i])
			}
		case 1, 2, 4:
			bit := i * bpc
			if bit/8 < len(row) {
				shift := 8 - bpc - bit%8
				v := (row[bit/8] >> shift) & byte(1<<bpc-1)
				return float64(v)
			}
		}
		return 0
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		row := data[y*rowBytes : min(len(data), (y+1)*rowBytes)]
		for x := 0; x < w; x++ {
			var c color.RGBA
			switch {
			case palette != nil:
				idx := int(sample(row, x))
				c = paletteColor(palette, idx)
			case comps == 1:
				v := sample(row, x) / maxVal
				if invert {
					v = 1 - v
				}
				g := uint8(v * 255)
				c = color.RGBA{g, g, g, 255}
			case comps == 3:
				r := uint8(sample(row, x*3) / maxVal * 255)
				g := uint8(sample(row, x*3+1) / maxVal * 255)
				b := uint8(sample(row, x*3+2) / maxVal * 255)
				c = color.RGBA{r, g, b, 255}
			case comps == 4:
				cy := sample(row, x*4) / maxVal
				m := sample(row, x*4+1) / maxVal
				ye := sample(row, x*4+2) / maxVal
				k := sample(row, x*4+3) / maxVal
				c = color.RGBA{
					uint8((1 - min(cy+k, 1)) * 255),
					uint8((1 - min(m+k, 1)) * 255),
					uint8((1 - min(ye+k, 1)) * 255),
					255,
				}
			default:
				return nil, fmt.Errorf("неподдержанное число составляющих цвета: %d", comps)
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img, nil
}

// colorSpace определяет число составляющих цвета и палитру, если она есть.
func (d *Document) colorSpace(cs Object) (int, []byte) {
	switch v := d.Resolve(cs).(type) {
	case Name:
		switch v {
		case "DeviceGray", "CalGray", "G":
			return 1, nil
		case "DeviceRGB", "CalRGB", "RGB":
			return 3, nil
		case "DeviceCMYK", "CMYK":
			return 4, nil
		}
	case Array:
		if len(v) == 0 {
			return 0, nil
		}
		family, _ := d.Resolve(v[0]).(Name)
		switch family {
		case "ICCBased":
			if len(v) > 1 {
				if st, ok := d.Resolve(v[1]).(*Stream); ok {
					if n, ok := toInt(d.Resolve(st.Dict["N"])); ok {
						return n, nil
					}
				}
			}
			return 3, nil
		case "Indexed", "I":
			if len(v) < 4 {
				return 0, nil
			}
			var table []byte
			switch lookup := d.Resolve(v[3]).(type) {
			case String:
				table = lookup
			case *Stream:
				table, _ = d.Decode(lookup)
			}
			if len(table) == 0 {
				return 0, nil
			}
			base, _ := d.colorSpace(v[1])
			if base == 0 {
				base = 3
			}
			// Палитра хранится как есть; число составляющих базовой модели
			// нужно, чтобы правильно из неё выбирать.
			return 1, append([]byte{byte(base)}, table...)
		case "DeviceN":
			if len(v) > 1 {
				return len(asArray(d.Resolve(v[1]))), nil
			}
		case "Separation":
			return 1, nil
		case "CalGray":
			return 1, nil
		case "CalRGB", "Lab":
			return 3, nil
		}
	}
	return 0, nil
}

// paletteColor выбирает цвет из палитры индексированной модели.
func paletteColor(palette []byte, idx int) color.RGBA {
	if len(palette) == 0 {
		return color.RGBA{0, 0, 0, 255}
	}
	base := int(palette[0])
	table := palette[1:]
	if base <= 0 {
		base = 3
	}
	off := idx * base
	if off+base > len(table) {
		return color.RGBA{0, 0, 0, 255}
	}
	switch base {
	case 1:
		g := table[off]
		return color.RGBA{g, g, g, 255}
	case 4:
		c := float64(table[off]) / 255
		m := float64(table[off+1]) / 255
		y := float64(table[off+2]) / 255
		k := float64(table[off+3]) / 255
		return color.RGBA{
			uint8((1 - min(c+k, 1)) * 255),
			uint8((1 - min(m+k, 1)) * 255),
			uint8((1 - min(y+k, 1)) * 255),
			255,
		}
	default:
		return color.RGBA{table[off], table[off+1], table[off+2], 255}
	}
}
