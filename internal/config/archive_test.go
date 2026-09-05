package config

import (
	"github.com/BurntSushi/toml"
	"strings"
	"testing"
	"time"
)

// Срок архива: пусто — сутки, off — выключено, слишком часто — отказ.
func TestArchiveEveryDuration(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		err  string
	}{
		{"", 24 * time.Hour, ""},
		{"24h", 24 * time.Hour, ""},
		{"12h", 12 * time.Hour, ""},
		{"off", 0, ""},
		{"0", 0, ""},
		{"5m", 0, "не чаще"},
		{"сутки", 0, "ожидается срок"},
	}
	for _, c := range cases {
		got, err := (Graph{ArchiveEvery: c.in}).ArchiveEveryDuration()
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%q: ожидалась ошибка со словами %q, получено %v", c.in, c.err, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%q: %v, %v", c.in, got, err)
		}
	}
}

// Каталог архивов: умолчание и раскрытие «~».
func TestArchiveDirPath(t *testing.T) {
	if got := (Graph{}).ArchiveDirPath(); got == "" || strings.Contains(got, "~") {
		t.Errorf("умолчание не раскрыто: %q", got)
	}
	if got := (Graph{ArchiveDir: "~/архивы"}).ArchiveDirPath(); strings.Contains(got, "~") {
		t.Errorf("«~» не раскрыт: %q", got)
	}
}

// Умолчания: раз в сутки, семь последних; отрицательное keep — ошибка запуска.
func TestArchiveDefaultsAndValidation(t *testing.T) {
	cfg := Default()
	if cfg.Graph.ArchiveEvery != "24h" || cfg.Graph.ArchiveKeep != 7 {
		t.Errorf("умолчания: every=%q keep=%d", cfg.Graph.ArchiveEvery, cfg.Graph.ArchiveKeep)
	}
	cfg.Graph.ArchiveKeep = -1
	if err := cfg.finalize(); err == nil || !strings.Contains(err.Error(), "archive_keep") {
		t.Errorf("отрицательное archive_keep должно быть ошибкой: %v", err)
	}
	cfg = Default()
	cfg.Graph.ArchiveEvery = "1m"
	if err := cfg.finalize(); err == nil || !strings.Contains(err.Error(), "archive_every") {
		t.Errorf("слишком частый архив должен быть ошибкой: %v", err)
	}
}

// abstain_score — указатель: незаданное значение и ноль различаются.
func TestAbstainScoreOptional(t *testing.T) {
	if Default().KB.AbstainScore != nil {
		t.Fatal("по умолчанию порог не задан")
	}
	cfg := Default()
	if _, err := toml.Decode("[kb]\nabstain_score = -2.0\n", cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.KB.AbstainScore == nil || *cfg.KB.AbstainScore != -2.0 {
		t.Fatalf("порог не прочитан: %v", cfg.KB.AbstainScore)
	}
}
