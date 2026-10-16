package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
)

// Этап 112 (решение владельца 16.10.2026): конфиг проекта применяется только
// после «да»; ответ помнится, пока файл не изменится; без терминала — не
// применяется и об этом сказано.
func TestProjectConfigNeedsTrust(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "global.toml")
	if err := config.WriteTemplate(global); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(dir, "work")
	if err := os.MkdirAll(filepath.Join(proj, ".ollchat"), 0o755); err != nil {
		t.Fatal(err)
	}
	pf := filepath.Join(proj, ".ollchat", "config.toml")
	if err := os.WriteFile(pf, []byte("[general]\nmode = \"yolo\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	trust := filepath.Join(dir, "state", "trusted.json")
	load := func() *config.Config {
		cfg, _, err := config.Load(global)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	var out bytes.Buffer
	cfg := load()
	if err := useProjectConfig(cfg, proj, strings.NewReader(""), &out, false, trust); err != nil {
		t.Fatal(err)
	}
	if cfg.General.Mode == config.ModeYolo || !strings.Contains(out.String(), "НЕ применён") {
		t.Errorf("без терминала: режим %q, вывод %q", cfg.General.Mode, out.String())
	}

	out.Reset()
	cfg = load()
	_ = useProjectConfig(cfg, proj, strings.NewReader("нет\n"), &out, true, trust)
	if cfg.General.Mode == config.ModeYolo || !strings.Contains(out.String(), `mode = "yolo"`) {
		t.Errorf("ответ «нет»: режим %q; файл показан? %q", cfg.General.Mode, out.String())
	}

	out.Reset()
	cfg = load()
	_ = useProjectConfig(cfg, proj, strings.NewReader("да\n"), &out, true, trust)
	if cfg.General.Mode != config.ModeYolo {
		t.Fatalf("ответ «да»: режим %q", cfg.General.Mode)
	}

	out.Reset()
	cfg = load()
	_ = useProjectConfig(cfg, proj, strings.NewReader(""), &out, false, trust)
	if cfg.General.Mode != config.ModeYolo || strings.Contains(out.String(), "Доверять") {
		t.Errorf("второй запуск после «да»: режим %q, вывод %q — вопроса быть не должно", cfg.General.Mode, out.String())
	}

	if err := os.WriteFile(pf, []byte("[general]\nmode = \"noask\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	cfg = load()
	_ = useProjectConfig(cfg, proj, strings.NewReader(""), &out, false, trust)
	if cfg.General.Mode == config.ModeNoAsk || !strings.Contains(out.String(), "НЕ применён") {
		t.Errorf("файл изменён — доверие должно сброситься: режим %q, вывод %q", cfg.General.Mode, out.String())
	}

	out.Reset()
	cfg = load()
	if err := useProjectConfig(cfg, dir, strings.NewReader(""), &out, true, trust); err != nil || out.Len() != 0 {
		t.Errorf("нет конфига проекта — ни ошибки, ни вывода: %v %q", err, out.String())
	}
}
