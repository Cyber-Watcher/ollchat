package clipboard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// ErrNoWriter означает, что положить текст в буфер обмена нечем: нет
// графической сессии или нет утилиты. Для вызывающего это не сбой, а знак
// уйти на запасной путь — отдать текст самому терминалу (OSC 52).
var ErrNoWriter = errors.New("утилиты записи в буфер обмена нет")

// WriteText кладёт текст в системный буфер обмена: wl-copy в Wayland,
// xclip в X11.
//
// Отсутствие утилиты возвращается как ErrNoWriter и отличимо через errors.Is —
// вызывающий по нему решает, уходить ли на запасной путь.
func WriteText(ctx context.Context, s string) error {
	w, err := detectWriter()
	if err != nil {
		return err
	}
	return feed(ctx, w.name, []byte(s), w.args...)
}

// writer — утилита записи в буфер обмена конкретной графической сессии.
type writer struct {
	name string
	args []string
}

// detectWriter выбирает утилиту по типу графической сессии.
//
// Все отказы обёрнуты ErrNoWriter: для вызывающего «нечем писать» — один
// случай, а подробность нужна лишь в тексте сообщения пользователю.
func detectWriter() (writer, error) {
	switch sessionKind() {
	case "wayland":
		if _, err := exec.LookPath("wl-copy"); err == nil {
			return writer{name: "wl-copy"}, nil
		}
		return writer{}, fmt.Errorf("%w: сессия Wayland, но утилита wl-copy не найдена — установите wl-clipboard", ErrNoWriter)

	case "x11":
		if _, err := exec.LookPath("xclip"); err == nil {
			return writer{name: "xclip", args: []string{"-selection", "clipboard", "-in"}}, nil
		}
		return writer{}, fmt.Errorf("%w: сессия X11, но утилита xclip не найдена — установите xclip", ErrNoWriter)
	}

	return writer{}, fmt.Errorf("%w: графическая сессия не обнаружена: ни WAYLAND_DISPLAY, ни DISPLAY не заданы", ErrNoWriter)
}

// feed запускает утилиту и подаёт ей текст на стандартный ввод.
//
// Ни stdout, ни stderr забирать в буферы нельзя. Владелец буфера обмена в X11 —
// работающий процесс, поэтому xclip (и так же wl-copy) после чтения текста
// уходит в фон и живёт, пока буфер принадлежит ему. Буфер под stdout заставляет
// os/exec завести копирующую горутину, а Wait ждёт её завершения — то есть
// закрытия унаследованного конца канала, которого не будет до конца сеанса.
//
// Замерено на этой машине: с Stdout = nil вызов возвращается за 0.01 с,
// с bytes.Buffer не возвращается вовсе, и context.WithTimeout не спасает —
// он убивает процесс, но не отпускает Wait. Приложение зависло бы прямо
// на копировании, как оно однажды уже висело на команде bash.
//
// С nil os/exec подставит /dev/null. Плата — от утилиты остаётся только код
// возврата, без её текста ошибки.
func feed(ctx context.Context, name string, stdin []byte, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stdout, cmd.Stderr = nil, nil
	// Если контекст истёк, а процесс ещё жив, не остаёмся ждать его вместе с ним.
	cmd.WaitDelay = time.Second

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("утилита %s не ответила за отведённое время", name)
		}
		return fmt.Errorf("%s не смог записать в буфер обмена: %w", name, err)
	}
	return nil
}
