package clipboard

import (
	"errors"
	"testing"
)

// Буфер обмена предлагает список типов, и выбрать надо тот, который Ollama
// действительно примет, а не первый попавшийся.
func TestPickPrefersPNG(t *testing.T) {
	cases := []struct {
		name    string
		offered []string
		want    string
	}{
		{"только PNG", []string{"TARGETS", "image/png"}, "image/png"},
		{"PNG предпочтительнее JPEG", []string{"image/jpeg", "image/png"}, "image/png"},
		{"JPEG, если PNG нет", []string{"TARGETS", "image/jpeg"}, "image/jpeg"},
		{"регистр и пробелы не мешают", []string{" IMAGE/PNG "}, "image/png"},
		{"только текст", []string{"TARGETS", "UTF8_STRING", "text/plain"}, ""},
		{"пусто", nil, ""},
		{"неподдерживаемая картинка", []string{"image/webp", "image/tiff"}, ""},
	}
	for _, c := range cases {
		if got := pick(c.offered); got != c.want {
			t.Errorf("%s: pick(%v) = %q, ожидалось %q", c.name, c.offered, got, c.want)
		}
	}
}

// TestDetectDistinguishesSessionAndHelper закрепляет, что «сессии нет вовсе»
// и «сессия есть, а утилиты нет» — разные случаи.
//
// Различать их по тексту ошибки нельзя: совет пользователю в этих случаях
// разный (проброс X11 против установки пакета), и интерфейс выбирает подсказку
// именно по виду ошибки.
func TestDetectDistinguishesSessionAndHelper(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")
	if _, err := detect(); !errors.Is(err, ErrNoSession) {
		t.Errorf("без графических переменных ожидался ErrNoSession, получено: %v", err)
	}

	// Сессия есть, а утилиты в PATH нет: подменяем PATH на пустой каталог.
	t.Setenv("DISPLAY", ":0")
	t.Setenv("PATH", t.TempDir())
	_, err := detect()
	if !errors.Is(err, ErrNoHelper) {
		t.Errorf("при заданном DISPLAY и отсутствии xclip ожидался ErrNoHelper, получено: %v", err)
	}
	if errors.Is(err, ErrNoSession) {
		t.Error("отсутствие утилиты не должно выглядеть как отсутствие сессии")
	}
}

// TestDetectWriterSessionIsRecognisable: у записи тот же случай должен быть
// отличим и как «писать нечем», и как «сессии нет».
func TestDetectWriterSessionIsRecognisable(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")
	_, err := detectWriter()
	if !errors.Is(err, ErrNoWriter) {
		t.Errorf("ожидался ErrNoWriter, получено: %v", err)
	}
	if !errors.Is(err, ErrNoSession) {
		t.Errorf("ожидался и ErrNoSession, получено: %v", err)
	}
}
