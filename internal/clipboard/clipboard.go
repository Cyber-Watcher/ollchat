// Package clipboard читает изображение из системного буфера обмена.
//
// Своего доступа к буферу у терминального приложения нет: терминал передаёт
// только текст, а Ctrl+V в нём — обычный управляющий символ, никакой картинки
// с ним не приходит. Поэтому изображение забирается у графической сессии
// внешней утилитой: wl-paste в Wayland, xclip в X11.
package clipboard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"os/exec"
	"strings"

	// Регистрируем разборщики форматов, которые понимает Ollama. Импорт нужен
	// ради побочного эффекта: без него image.DecodeConfig не узнает формат.
	_ "image/jpeg"
	_ "image/png"
)

// ErrNoImage означает, что буфер обмена существует, но картинки в нём нет.
var ErrNoImage = errors.New("в буфере обмена нет изображения")

// Image — изображение, полученное из буфера обмена.
type Image struct {
	Data          []byte
	MIME          string
	Width, Height int
}

// Форматы в порядке предпочтения. Ollama принимает PNG и JPEG.
var wantTypes = []string{"image/png", "image/jpeg", "image/jpg"}

// ReadImage забирает изображение из буфера обмена.
//
// maxBytes ограничивает размер: картинка едет к модели в base64 внутри JSON,
// и мегабайтные вложения незаметно съедают и трафик, и контекстное окно.
func ReadImage(ctx context.Context, maxBytes int) (*Image, error) {
	tool, err := detect()
	if err != nil {
		return nil, err
	}

	offered, err := tool.types(ctx)
	if err != nil {
		return nil, err
	}

	mime := pick(offered)
	if mime == "" {
		if len(offered) == 0 {
			return nil, ErrNoImage
		}
		return nil, fmt.Errorf("%w: буфер предлагает только %s",
			ErrNoImage, strings.Join(offered, ", "))
	}

	data, err := tool.read(ctx, mime)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, ErrNoImage
	}
	if maxBytes > 0 && len(data) > maxBytes {
		return nil, fmt.Errorf("изображение %d КБ больше допустимых %d КБ",
			len(data)/1024, maxBytes/1024)
	}

	// Разбираем заголовок: заодно убеждаемся, что это действительно картинка
	// заявленного формата, а не мусор с подходящим именем типа.
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("буфер обмена отдал %s, но разобрать изображение не удалось: %w", mime, err)
	}

	return &Image{
		Data:   data,
		MIME:   "image/" + format,
		Width:  cfg.Width,
		Height: cfg.Height,
	}, nil
}

// pick выбирает первый подходящий тип из предложенных буфером.
func pick(offered []string) string {
	for _, want := range wantTypes {
		for _, have := range offered {
			if strings.EqualFold(strings.TrimSpace(have), want) {
				return want
			}
		}
	}
	return ""
}

// helper — утилита работы с буфером обмена конкретной графической сессии.
type helper struct {
	name      string
	typesArgs []string
	readArgs  func(mime string) []string
}

func (h helper) types(ctx context.Context) ([]string, error) {
	out, err := run(ctx, h.name, h.typesArgs...)
	if err != nil {
		// Пустой буфер обмена утилиты считают ошибкой — для нас это не сбой,
		// а обычное «картинки нет».
		return nil, ErrNoImage
	}
	var list []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			list = append(list, l)
		}
	}
	return list, nil
}

func (h helper) read(ctx context.Context, mime string) ([]byte, error) {
	out, err := run(ctx, h.name, h.readArgs(mime)...)
	if err != nil {
		return nil, fmt.Errorf("%s не смог прочитать %s из буфера обмена: %w", h.name, mime, err)
	}
	return out, nil
}

// detect выбирает утилиту по типу графической сессии.
func detect() (helper, error) {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		if _, err := exec.LookPath("wl-paste"); err == nil {
			return helper{
				name:      "wl-paste",
				typesArgs: []string{"--list-types"},
				readArgs: func(mime string) []string {
					return []string{"--no-newline", "--type", mime}
				},
			}, nil
		}
		return helper{}, errors.New("сессия Wayland, но утилита wl-paste не найдена — установите wl-clipboard")
	}

	if os.Getenv("DISPLAY") != "" {
		if _, err := exec.LookPath("xclip"); err == nil {
			return helper{
				name:      "xclip",
				typesArgs: []string{"-selection", "clipboard", "-t", "TARGETS", "-o"},
				readArgs: func(mime string) []string {
					return []string{"-selection", "clipboard", "-t", mime, "-o"}
				},
			}, nil
		}
		return helper{}, errors.New("сессия X11, но утилита xclip не найдена — установите xclip")
	}

	return helper{}, errors.New("графическая сессия не обнаружена: ни WAYLAND_DISPLAY, ни DISPLAY не заданы")
}

// run выполняет утилиту и возвращает её вывод.
func run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%s: %s", err, msg)
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}
