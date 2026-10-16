package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Этап 112 (решения владельца 16.10.2026): конфиг проекта .ollchat/config.toml
// заменяет общие ключи, а списки правил складывает; [graph] проекту не даётся.
func TestApplyProjectOverridesAndMerges(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "config.toml")
	if err := WriteTemplate(global); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(global)
	if err != nil {
		t.Fatal(err)
	}
	denyBefore := len(cfg.Permissions.Deny)
	graphBefore := cfg.Graph.Name
	serversBefore := len(cfg.Servers)

	proj := []byte(`
[general]
mode = "noask"
no_such_key = 1

[agent]
backup_keep_days = 30

[permissions]
allow = ["Bash(make:*)"]
deny = ["Bash(git push:*)"]

[graph]
name = "чужой"
`)
	if err := cfg.ApplyProject("/p/.ollchat/config.toml", proj); err != nil {
		t.Fatal(err)
	}
	if cfg.General.Mode != ModeNoAsk || cfg.Agent.BackupKeepDays != 30 {
		t.Errorf("ключи проекта не заменили общие: mode %q, keep %d", cfg.General.Mode, cfg.Agent.BackupKeepDays)
	}
	if !slices.Contains(cfg.Permissions.Allow, "Bash(make:*)") || !slices.Contains(cfg.Permissions.Allow, "Read(./**)") {
		t.Errorf("allow не сложен с общим: %v", cfg.Permissions.Allow)
	}
	if len(cfg.Permissions.Deny) != denyBefore+1 || !slices.Contains(cfg.Permissions.Deny, "Bash(rm:*)") {
		t.Errorf("deny: было %d, стало %d — общий запрет должен остаться, проектный добавиться", denyBefore, len(cfg.Permissions.Deny))
	}
	if cfg.Graph.Name != graphBefore {
		t.Errorf("[graph] проекта применён: имя %q", cfg.Graph.Name)
	}
	if len(cfg.Servers) != serversBefore {
		t.Errorf("серверов было %d, стало %d — проект их не задавал", serversBefore, len(cfg.Servers))
	}
	if len(cfg.Permissions.Allow) != 9 {
		t.Errorf("allow: общих 8 + проектное 1, а вышло %d: %v", len(cfg.Permissions.Allow), cfg.Permissions.Allow)
	}

	// Список, заданный проектом, заменяет общий целиком (не поверх элементов):
	// проектный список инструментов из одного имени — это одно имя.
	cfg3, _, _ := Load(global)
	if err := cfg3.ApplyProject("/p/c.toml", []byte("[agent]\ntools = [\"read_file\"]\n")); err != nil {
		t.Fatal(err)
	}
	if len(cfg3.Agent.Tools) != 1 || cfg3.Agent.Tools[0] != "read_file" {
		t.Errorf("agent.tools проекта: %v — ждали ровно [read_file]", cfg3.Agent.Tools)
	}
	var graphWarn, unknownWarn bool
	for _, w := range cfg.Warnings {
		graphWarn = graphWarn || strings.Contains(w, "[graph] не применяется")
		unknownWarn = unknownWarn || strings.Contains(w, "конфиг проекта:") && strings.Contains(w, "no_such_key")
	}
	if !graphWarn || !unknownWarn {
		t.Errorf("предупреждения: %v", cfg.Warnings)
	}
	if cfg.ProjectPath != "/p/.ollchat/config.toml" {
		t.Errorf("ProjectPath %q", cfg.ProjectPath)
	}

	bad := []byte("[general]\nmode = \"всё-можно\"\n")
	cfg2, _, _ := Load(global)
	if err := cfg2.ApplyProject("/p/c.toml", bad); err == nil {
		t.Error("недопустимый режим в конфиге проекта принят")
	}
}

// Доверие помнится по пути и отпечатку: правка файла спрашивает снова.
func TestProjectTrust(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state", "trusted.json")
	sum := ProjectSum([]byte("a"))
	if ProjectTrusted(file, "/p/c.toml", sum) {
		t.Fatal("доверие без ответа")
	}
	if err := TrustProject(file, "/p/c.toml", sum); err != nil {
		t.Fatal(err)
	}
	if !ProjectTrusted(file, "/p/c.toml", sum) {
		t.Error("доверие не запомнилось")
	}
	if ProjectTrusted(file, "/p/c.toml", ProjectSum([]byte("b"))) {
		t.Error("правка файла не сбросила доверие")
	}
	if ProjectTrusted(file, "/q/c.toml", sum) {
		t.Error("доверие перешло на другой путь")
	}
	if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("файл доверия: %v %v", fi, err)
	}
}
