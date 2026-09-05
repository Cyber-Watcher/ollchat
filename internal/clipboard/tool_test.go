package clipboard

import (
	"errors"
	"testing"
)

// Один поиск утилиты на чтение и запись: выбор по сессии, отсутствие
// утилиты — своя ошибка у каждого направления, отсутствие сессии — общая.
func TestToolForUsesLookPathSeam(t *testing.T) {
	prev := lookPath
	t.Cleanup(func() { lookPath = prev })
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("DISPLAY", "")

	lookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	h, err := detect()
	if err != nil || h.name != "wl-paste" {
		t.Fatalf("wayland с утилитой: %+v, %v", h, err)
	}
	w, err := detectWriter()
	if err != nil || w.name != "wl-copy" {
		t.Fatalf("wayland с утилитой записи: %+v, %v", w, err)
	}

	lookPath = func(string) (string, error) { return "", errors.New("нет") }
	if _, err := detect(); !errors.Is(err, ErrNoHelper) {
		t.Fatalf("без wl-paste ожидался ErrNoHelper: %v", err)
	}
	if _, err := detectWriter(); !errors.Is(err, ErrNoWriter) || errors.Is(err, ErrNoSession) {
		t.Fatalf("без wl-copy ожидался ErrNoWriter без ErrNoSession: %v", err)
	}

	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", ":0")
	lookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	if h, err := detect(); err != nil || h.name != "xclip" {
		t.Fatalf("x11: %+v, %v", h, err)
	}
	if w, err := detectWriter(); err != nil || w.name != "xclip" || len(w.args) == 0 {
		t.Fatalf("x11 запись: %+v, %v", w, err)
	}

	t.Setenv("DISPLAY", "")
	if _, err := detect(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("без сессии ожидался ErrNoSession: %v", err)
	}
	if _, err := detectWriter(); !errors.Is(err, ErrNoWriter) || !errors.Is(err, ErrNoSession) {
		t.Fatalf("без сессии у записи обе ошибки: %v", err)
	}
}
