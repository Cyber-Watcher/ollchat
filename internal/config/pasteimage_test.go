package config

import (
	"strings"
	"testing"
	"time"
)

// TestPasteImageTimeout: срок ожидания картинки из буфера обмена — настройка
// input.paste_image_timeout. По ssh -X снимок экрана идёт десятки секунд, и
// прежние жёсткие 5 с обрывали xclip.
func TestPasteImageTimeout(t *testing.T) {
	cfg, err := writeConfig(t, "")
	if err != nil {
		t.Fatalf("конфиг без настройки: %v", err)
	}
	if got := cfg.Input.PasteImageTimeoutDuration(); got != DefaultPasteImageTimeout {
		t.Errorf("без настройки срок %v, ожидалось умолчание %v", got, DefaultPasteImageTimeout)
	}

	cfg, err = writeConfig(t, "[input]\npaste_image_timeout = \"45s\"\n")
	if err != nil {
		t.Fatalf("paste_image_timeout = 45s: %v", err)
	}
	if got := cfg.Input.PasteImageTimeoutDuration(); got != 45*time.Second {
		t.Errorf("срок %v, ожидалось 45s", got)
	}

	for _, bad := range []string{"полминуты", "0s", "-5s"} {
		_, err := writeConfig(t, "[input]\npaste_image_timeout = \""+bad+"\"\n")
		if err == nil || !strings.Contains(err.Error(), "input.paste_image_timeout") {
			t.Errorf("paste_image_timeout = %q: ожидался отказ загрузки с именем настройки, получено %v", bad, err)
		}
	}

	// Конфиг, собранный без загрузки, тоже получает умолчание, а не ноль:
	// нулевой срок оборвал бы чтение сразу.
	if got := (Input{}).PasteImageTimeoutDuration(); got != DefaultPasteImageTimeout {
		t.Errorf("Input{} даёт %v, ожидалось умолчание", got)
	}
}
