package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func nights(t *testing.T, names ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(root, "runs", n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// Ночь продолжается если осталась работа.
func TestNightResumesIfWorkLeft(t *testing.T) {
	root := nights(t, "2026-08-21", "2026-08-22")
	now := time.Date(2026, 8, 23, 0, 0, 30, 0, time.UTC)
	remaining := map[string]int{"2026-08-22": 496, "2026-08-21": 0}

	got, err := ResolveNight(root, "2026-08-23", 72*time.Hour, now,
		func(n string) (int, error) { return remaining[n], nil })
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "2026-08-22" || !got.Carried || got.Pending != 496 {
		t.Errorf("решение = %+v, ожидалось продолжение 2026-08-22", got)
	}
}

// Доделанная ночь не продолжается.
func TestFinishedNightNotResumed(t *testing.T) {
	root := nights(t, "2026-08-22")
	now := time.Date(2026, 8, 23, 0, 0, 30, 0, time.UTC)
	got, err := ResolveNight(root, "2026-08-23", 72*time.Hour, now,
		func(string) (int, error) { return 0, nil })
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "2026-08-23" || got.Carried {
		t.Errorf("решение = %+v, ожидалась новая ночь", got)
	}
}

// Состав моделей на стенде меняется: докатывать позапрошлую неделю
// бессмысленно, сравнивать её цифры будет не с чем.
func TestOldNightNotResumed(t *testing.T) {
	root := nights(t, "2026-08-01")
	now := time.Date(2026, 8, 23, 0, 0, 30, 0, time.UTC)
	got, err := ResolveNight(root, "2026-08-23", 72*time.Hour, now,
		func(string) (int, error) { return 900, nil })
	if err != nil {
		t.Fatal(err)
	}
	if got.Carried {
		t.Errorf("докатывается ночь трёхнедельной давности: %+v", got)
	}
}

// Ручные прогоны не докатываются.
func TestManualRunsNotResumed(t *testing.T) {
	root := nights(t, "проба", "smoke-test")
	now := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	got, err := ResolveNight(root, "2026-08-23", 72*time.Hour, now,
		func(string) (int, error) { return 100, nil })
	if err != nil {
		t.Fatal(err)
	}
	if got.Carried {
		t.Errorf("ручной прогон принят за ночь: %+v", got)
	}
}

// Берётся самая свежая незаконченная.
func TestPicksLatestUnfinished(t *testing.T) {
	root := nights(t, "2026-08-20", "2026-08-21", "2026-08-22")
	now := time.Date(2026, 8, 23, 0, 0, 30, 0, time.UTC)
	asked := []string{}
	got, err := ResolveNight(root, "2026-08-23", 72*time.Hour, now, func(n string) (int, error) {
		asked = append(asked, n)
		return 10, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "2026-08-22" {
		t.Errorf("взята ночь %q, ожидалась самая свежая 2026-08-22", got.Name)
	}
	if len(asked) != 1 {
		t.Errorf("опрошено ночей %v — хватало одной, самой свежей", asked)
	}
}

// Пустой корень даёт сегодняшнюю ночь.
func TestEmptyRootGivesTonight(t *testing.T) {
	got, err := ResolveNight(t.TempDir(), "2026-08-23", 72*time.Hour, time.Now(),
		func(string) (int, error) { return 0, fmt.Errorf("не должно вызываться") })
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "2026-08-23" || got.Carried {
		t.Errorf("решение = %+v", got)
	}
}
