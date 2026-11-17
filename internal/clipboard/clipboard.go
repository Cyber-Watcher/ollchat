// Package clipboard читает изображение из системного буфера обмена и кладёт
// в него текст.
//
// Своего доступа к буферу у терминального приложения нет: терминал передаёт
// только текст, а Ctrl+V в нём — обычный управляющий символ, никакой картинки
// с ним не приходит. Поэтому буфер обслуживается утилитами графической сессии:
// wl-paste и wl-copy в Wayland, xclip в X11. Запись живёт в write.go.
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
	"time"

	// Регистрируем разборщики форматов, которые понимает Ollama. Импорт нужен
	// ради побочного эффекта: без него image.DecodeConfig не узнает формат.
	_ "image/jpeg"
	_ "image/png"
)

// ErrNoImage означает, что буфер обмена существует, но картинки в нём нет.
var ErrNoImage = errors.New("в буфере обмена нет изображения")

// ErrNoSession означает, что графической сессии нет вовсе. Обычно это работа
// по SSH: буфер обмена остался на машине пользователя, а здесь его нет.
//
// Отдельный вид ошибки нужен потому, что совет пользователю здесь совсем
// другой, чем при отсутствии утилиты, а разбирать текст ошибки — плохая опора.
var ErrNoSession = errors.New("графическая сессия не обнаружена: ни WAYLAND_DISPLAY, ни DISPLAY не заданы")

// ErrNoHelper означает, что графическая сессия есть, а утилиты для доступа
// к буферу обмена нет — её достаточно установить.
var ErrNoHelper = errors.New("утилита работы с буфером обмена не найдена")

// ErrImageTimeout означает, что картинка не пришла за срок вызывающего. Так
// бывает по ssh -X: снимок экрана идёт через проброс X11 десятки секунд, и
// утилиту приходится обрывать. Отдельный вид ошибки — чтобы интерфейс сказал
// про медленный канал и настройку срока, а не показал «signal: killed».
var ErrImageTimeout = errors.New("картинка не пришла из буфера обмена в срок")

// typesTimeout — свой короткий срок на список форматов. Он мал при любом
// канале, и долгое молчание здесь значит, что буфер не отвечает вовсе, —
// ждать весь срок картинки ради этого незачем. Переменная — ради тестов.
var typesTimeout = 5 * time.Second

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
// Срок ctx — на всю картинку; список форматов ждётся не дольше typesTimeout.
func ReadImage(ctx context.Context, maxBytes int) (*Image, error) {
	tool, err := detect()
	if err != nil {
		return nil, err
	}

	tctx, cancel := context.WithTimeout(ctx, typesTimeout)
	offered, err := tool.types(tctx)
	cancel()
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
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%s не отдал список форматов буфера обмена за %s: %w",
				h.name, typesTimeout, err)
		}
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
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: %s читал %s дольше срока", ErrImageTimeout, h.name, mime)
		}
		return nil, fmt.Errorf("%s не смог прочитать %s из буфера обмена: %w", h.name, mime, err)
	}
	return out, nil
}

// sessionKind сообщает, какая графическая сессия запущена: wayland, x11 или
// пусто, если ни одной. Общий помощник для чтения и записи — иначе они
// разъедутся в понимании того, где мы работаем.
func sessionKind() string {
	switch {
	case os.Getenv("WAYLAND_DISPLAY") != "":
		return "wayland"
	case os.Getenv("DISPLAY") != "":
		return "x11"
	}
	return ""
}

// lookPath — поиск утилиты в PATH; переменная ради тестов без wl-paste и xclip.
var lookPath = exec.LookPath

// toolFor выбирает утилиту по типу графической сессии: wayland → одна,
// x11 → другая. Один поиск на чтение и на запись (этап 91, R5.7): раньше
// detect и detectWriter повторяли его порознь с двумя разными ошибками.
// missing — чем обернуть отсутствие утилиты; нет сессии — ErrNoSession.
func toolFor(wayland, x11 string, missing error) (string, error) {
	switch sessionKind() {
	case "wayland":
		if _, err := lookPath(wayland); err == nil {
			return wayland, nil
		}
		return "", fmt.Errorf("%w: сессия Wayland, но %s нет — установите wl-clipboard", missing, wayland)
	case "x11":
		if _, err := lookPath(x11); err == nil {
			return x11, nil
		}
		return "", fmt.Errorf("%w: сессия X11, но %s нет — установите xclip", missing, x11)
	}
	return "", ErrNoSession
}

// detect выбирает утилиту чтения по типу графической сессии.
func detect() (helper, error) {
	name, err := toolFor("wl-paste", "xclip", ErrNoHelper)
	if err != nil {
		return helper{}, err
	}
	if name == "wl-paste" {
		return helper{
			name:      "wl-paste",
			typesArgs: []string{"--list-types"},
			readArgs: func(mime string) []string {
				return []string{"--no-newline", "--type", mime}
			},
		}, nil
	}
	return helper{
		name:      "xclip",
		typesArgs: []string{"-selection", "clipboard", "-t", "TARGETS", "-o"},
		readArgs: func(mime string) []string {
			return []string{"-selection", "clipboard", "-t", mime, "-o"}
		},
	}, nil
}

// run выполняет утилиту и возвращает её вывод.
func run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Оборванная по сроку утилита отвечает «signal: killed»; причина —
		// срок, и её видно по виду ошибки, а не по тексту.
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w (%s)", ctx.Err(), err)
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%s: %s", err, msg)
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}
