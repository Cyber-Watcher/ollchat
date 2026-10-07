package redact

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/Cyber-Watcher/ollchat/internal/pdf"
)

// renderDPI — разрешение, в котором pdftoppm рисует страницу, которую свой
// разбор не раскрыл.
const renderDPI = 300

// Load читает страницы скана из файла PDF. Страницу, которую свой разбор
// не раскрыл (JBIG2, JPEG 2000, повёрнутая, не скан целиком, вовсе без
// картинки), рисует внешняя программа pdftoppm, если она есть. notes — какие
// страницы пришлось рисовать и почему: человеку это знать нужно, на такой
// странице всё, что было текстом, станет картинкой.
func Load(ctx context.Context, path string, maxBytes int64) (pages []Page, notes []string, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		return nil, nil, fmt.Errorf("документ слишком велик: %d байт, предел %d", info.Size(), maxBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	if !pdf.IsPDF(data) {
		return nil, nil, fmt.Errorf("%s — не документ PDF", filepath.Base(path))
	}
	scans, err := pdf.ScanPages(data)
	if err != nil {
		return nil, nil, err
	}
	for i, sp := range scans {
		if sp.Image != nil {
			pages = append(pages, Page{Width: sp.Width, Height: sp.Height, Image: sp.Image})
			continue
		}
		img, err := renderPage(ctx, path, i+1)
		if err != nil {
			return nil, notes, fmt.Errorf("страница %d: %s, а нарисовать её нечем: %w", i+1, sp.Note, err)
		}
		b := img.Bounds()
		pages = append(pages, Page{
			Width: float64(b.Dx()) / renderDPI * 72, Height: float64(b.Dy()) / renderDPI * 72, Image: img,
		})
		notes = append(notes, fmt.Sprintf("страница %d нарисована программой pdftoppm (%s)", i+1, sp.Note))
	}
	if len(pages) == 0 {
		return nil, notes, errors.New("в документе нет страниц")
	}
	return pages, notes, nil
}

func renderPage(ctx context.Context, path string, n int) (image.Image, error) {
	bin, err := exec.LookPath("pdftoppm")
	if err != nil {
		return nil, errors.New("программы pdftoppm в системе нет — установите пакет poppler-utils")
	}
	// Путь — полный: относительный, начинающийся с «-» («-скан.pdf»),
	// pdftoppm прочёл бы как свой ключ, а не как файл.
	if path, err = filepath.Abs(path); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "ollchat-render-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	prefix := filepath.Join(tmp, "page")
	num := strconv.Itoa(n)
	out, err := exec.CommandContext(ctx, bin, "-f", num, "-l", num, "-r", strconv.Itoa(renderDPI),
		"-png", "-singlefile", path, prefix).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("pdftoppm: %v: %s", err, out)
	}
	f, err := os.Open(prefix + ".png")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// Размер рисунка задаёт MediaBox из чужого файла: лист в двести дюймов
	// при 300 dpi — это 3,6 миллиарда точек, и декодер просил бы их память
	// раньше, чем прочтёт картинку. Проверка та же, что у картинок PDF.
	cfg, err := png.DecodeConfig(f)
	if err != nil {
		return nil, err
	}
	if err := pdf.CheckImageConfig(cfg); err != nil {
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return png.Decode(f)
}
