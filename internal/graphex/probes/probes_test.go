package probes

import (
	"bytes"
	"strings"
	"testing"
)

// Отказы разбираются раньше, чем Run открывает коллекцию или графex, — nil
// вместо *config.Config это проверяет: если бы код дошёл до чтения cfg,
// тест упал бы паникой, а не ошибкой.

// TestRunNoOnly — без -only Run отказывает, а не выбирает режим по умолчанию.
func TestRunNoOnly(t *testing.T) {
	var buf bytes.Buffer
	err := Run(&buf, nil, "books", nil)
	if err == nil {
		t.Fatal("ожидал ошибку без -only")
	}
	if !strings.Contains(err.Error(), "укажите режим") {
		t.Fatalf("ошибка = %q, хочу про «укажите режим»", err.Error())
	}
}

// TestRunUnknownOnly — неизвестный режим называется в ошибке.
func TestRunUnknownOnly(t *testing.T) {
	var buf bytes.Buffer
	err := Run(&buf, nil, "books", []string{"-only", "bogus"})
	if err == nil {
		t.Fatal("ожидал ошибку на неизвестном режиме")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("ошибка = %q, хочу упоминание bogus", err.Error())
	}
}

// TestRunStabilityNoAxis — stability без -axis отказывает, не выбирая ось
// по умолчанию.
func TestRunStabilityNoAxis(t *testing.T) {
	var buf bytes.Buffer
	err := Run(&buf, nil, "books", []string{"-only", "stability"})
	if err == nil {
		t.Fatal("ожидал ошибку без -axis")
	}
	if !strings.Contains(err.Error(), "ось") {
		t.Fatalf("ошибка = %q, хочу про ось", err.Error())
	}
}

// TestRunStabilityUnknownAxis — неизвестная ось называется в ошибке.
func TestRunStabilityUnknownAxis(t *testing.T) {
	var buf bytes.Buffer
	err := Run(&buf, nil, "books", []string{"-only", "stability", "-axis", "bogus"})
	if err == nil {
		t.Fatal("ожидал ошибку на неизвестной оси")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("ошибка = %q, хочу упоминание bogus", err.Error())
	}
}

// TestModeNames — список режимов для сообщений об ошибке не пуст и содержит
// все три режима.
func TestModeNames(t *testing.T) {
	names := modeNames()
	want := map[string]bool{"stability": true, "seq": true, "det": true}
	if len(names) != len(want) {
		t.Fatalf("modeNames() = %v, хочу три режима", names)
	}
	for _, n := range names {
		if !want[n] {
			t.Fatalf("неожиданный режим %q", n)
		}
	}
}
