package pdf

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"math"

	"golang.org/x/image/ccitt"
)

// Страницы скана целиком.
//
// Нужны замазыванию персональных данных (internal/redact): оно распознаёт
// страницу внешней программой и рисует поверх чёрное, поэтому ему нужна
// не выгрузка картинок файлами, как view_image, а растр в исходном
// разрешении и знание, какую часть страницы картинка покрывает.
//
// Факсимильное сжатие здесь раскрывается, в отличие от ExtractImages: сканер
// кладёт чёрно-белые листы именно в CCITT (образец 03.10.2026, ScanSnap:
// лист 1 — CCITT Group 4, 400 dpi; лист 2 — JPEG, 200 dpi).

// ScanPage — страница скана.
type ScanPage struct {
	Width, Height float64     // размер страницы (MediaBox), pt
	Image         image.Image // картинка, покрывающая страницу; nil — см. Note
	Note          string      // почему картинки нет: её нет вовсе, её не раскрыть, она не на всю страницу
}

// minScanCover — какую долю страницы должна покрывать картинка, чтобы
// страница считалась сканом. Поля сканера бывают обрезаны на несколько
// точек, поэтому не единица.
const minScanCover = 0.9

// ScanPages возвращает страницы документа как сканы: размер и самую крупную
// картинку каждой страницы, раскрытую в растр.
func ScanPages(data []byte) (out []ScanPage, err error) {
	defer catch("разбор страниц скана", &err)

	doc, err := Open(data)
	if err != nil {
		return nil, err
	}
	ex := newExtractor(doc)
	pages := doc.Pages()
	for i, pg := range pages {
		sp := ScanPage{}
		sp.Width, sp.Height = doc.pageSize(pg)
		ex.unit = i + 1
		ex.page(pg)

		best, bestArea := -1, 0.0
		for j, im := range ex.images {
			if a := math.Abs(im.ctm.a*im.ctm.dd - im.ctm.b*im.ctm.c); a > bestArea {
				best, bestArea = j, a
			}
		}
		rot, _ := toInt(doc.Resolve(pg["Rotate"]))
		switch {
		case best < 0:
			sp.Note = "на странице нет картинки"
		case rot%360 != 0:
			sp.Note = fmt.Sprintf("страница повёрнута на %d°", rot)
		default:
			m := ex.images[best].ctm
			cover := bestArea / (sp.Width * sp.Height)
			switch {
			case m.b != 0 || m.c != 0 || m.a <= 0 || m.dd <= 0:
				sp.Note = "картинка повёрнута или отражена"
			case cover < minScanCover:
				sp.Note = fmt.Sprintf("картинка покрывает %.0f%% страницы — это не скан целиком", cover*100)
			default:
				img, err := doc.decodeImage(ex.images[best].stream)
				if err != nil {
					sp.Note = err.Error()
				} else {
					sp.Image = img
				}
			}
		}
		out = append(out, sp)
		if doc.overspent {
			return nil, heavy(i+1, len(pages))
		}
	}
	return out, nil
}

// pageSize — ширина и высота страницы по MediaBox; без него — лист Letter,
// как велит спецификация для документа, где размер не указан.
func (d *Document) pageSize(pg Dict) (float64, float64) {
	box := asArray(d.Resolve(pg["MediaBox"]))
	if len(box) == 4 {
		var v [4]float64
		ok := true
		for i := range v {
			v[i], ok = toFloat(d.Resolve(box[i]))
			if !ok {
				break
			}
		}
		if ok && v[2] != v[0] && v[3] != v[1] {
			return math.Abs(v[2] - v[0]), math.Abs(v[3] - v[1])
		}
	}
	return 612, 792
}

// decodeImage раскрывает картинку в растр: JPEG и CCITT Group 4 — сами,
// остальное — тем же путём, что и ExtractImages.
func (d *Document) decodeImage(s *Stream) (image.Image, error) {
	filters := asArray(d.Resolve(s.Dict["Filter"]))
	last := Name("")
	if len(filters) > 0 {
		last, _ = d.Resolve(filters[len(filters)-1]).(Name)
	}
	switch last {
	case "DCTDecode", "CCITTFaxDecode":
		data := s.Raw
		for i := 0; i < len(filters)-1; i++ {
			name, _ := d.Resolve(filters[i]).(Name)
			var err error
			if data, err = d.applyFilter(name, data, nil); err != nil {
				return nil, err
			}
		}
		if last == "DCTDecode" {
			// Память декодер берёт по заголовку, раньше точек: сначала
			// размер (CheckImageSize).
			cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
			if err != nil {
				return nil, err
			}
			if err := CheckImageConfig(cfg); err != nil {
				return nil, err
			}
			return jpeg.Decode(bytes.NewReader(data))
		}
		return d.decodeCCITT(s, data, len(filters)-1)
	case "JPXDecode", "JBIG2Decode":
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedImage, last)
	}
	data, err := d.Decode(s)
	if err != nil && len(data) == 0 {
		return nil, err
	}
	return d.rasterize(s, data)
}

// decodeCCITT раскрывает факсимильное сжатие. Поддержан Group 4 (K < 0) — его
// пишут сканеры — и одномерный Group 3 (K = 0) с метками конца строки;
// двумерный Group 3 (K > 0) пакет golang.org/x/image/ccitt не знает.
func (d *Document) decodeCCITT(s *Stream, data []byte, filterIdx int) (image.Image, error) {
	var parm Dict
	parms := asArray(d.Resolve(s.Dict["DecodeParms"]))
	if len(parms) == 0 {
		parms = asArray(d.Resolve(s.Dict["DP"]))
	}
	if filterIdx < len(parms) {
		parm, _ = d.Resolve(parms[filterIdx]).(Dict)
	}
	intOr := func(key Name, def int) int {
		if v, ok := toInt(d.Resolve(parm[key])); ok {
			return v
		}
		return def
	}
	boolOr := func(key Name, def bool) bool {
		if v, ok := d.Resolve(parm[key]).(bool); ok {
			return v
		}
		return def
	}

	k := intOr("K", 0)
	sf := ccitt.Group4
	switch {
	case k > 0:
		return nil, fmt.Errorf("%w: CCITT Group 3 двумерный (K=%d)", ErrUnsupportedImage, k)
	case k == 0:
		sf = ccitt.Group3
	}
	h, _ := toInt(d.Resolve(s.Dict["Height"]))
	w := intOr("Columns", 1728)
	if rows := intOr("Rows", 0); rows > 0 {
		h = rows
	}
	if err := CheckImageSize(w, h, 1); err != nil {
		return nil, fmt.Errorf("CCITT: %w", err)
	}
	// BlackIs1 = false (умолчание PDF): ноль — чёрный, как и у ccitt без Invert.
	opts := &ccitt.Options{Align: boolOr("EncodedByteAlign", false), Invert: boolOr("BlackIs1", false)}
	img := image.NewGray(image.Rect(0, 0, w, h))
	if err := ccitt.DecodeIntoGray(img, bytes.NewReader(data), ccitt.MSB, sf, opts); err != nil {
		return nil, fmt.Errorf("CCITT: %w", err)
	}
	if dec := asArray(d.Resolve(s.Dict["Decode"])); len(dec) >= 2 {
		if v, ok := toFloat(d.Resolve(dec[0])); ok && v == 1 {
			for i := range img.Pix {
				img.Pix[i] = 255 - img.Pix[i]
			}
		}
	}
	return img, nil
}
