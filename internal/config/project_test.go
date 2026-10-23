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

// Серверы MCP проекта складываются с общими по имени (аудит 23.10.2026):
// раньше один [mcp.servers.<имя>] в проекте молча убирал все общие серверы.
// Раздел проекта с именем общего сервера меняет только заданные в нём ключи,
// а источник ключа (token_file|token_env|token_cmd) — целиком.
func TestApplyProjectMergesMCPServers(t *testing.T) {
	cfg, err := loadText(t, `
[mcp.servers.wiki]
url        = "http://mcp.example:8377/mcp"
token_file = "/run/wiki.token"
tools      = ["echo", "search"]
allow      = ["echo"]

[mcp.servers.docs]
url = "http://mcp.example:8378/mcp"
`)
	if err != nil {
		t.Fatal(err)
	}
	proj := []byte(`
[mcp.servers.other]
url   = "http://mcp.example:9000/mcp"
allow = ["run"]

[mcp.servers.wiki]
allow     = ["search"]
token_env = "WIKI_TOKEN"

[mcp.servers.docs]
enabled = false
`)
	if err := cfg.ApplyProject("/p/.ollchat/config.toml", proj); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.MCP.Names(), ","); got != "docs,other,wiki" {
		t.Fatalf("серверы после проекта: %s", got)
	}
	w := cfg.MCP.Servers["wiki"]
	if w.URL != "http://mcp.example:8377/mcp" || strings.Join(w.Tools, ",") != "echo,search" ||
		strings.Join(w.Allow, ",") != "search" {
		t.Errorf("wiki: общие ключи не сохранены или ключ проекта не применён: %+v", w)
	}
	if w.TokenFile != "" || w.TokenEnv != "WIKI_TOKEN" {
		t.Errorf("wiki: источник ключа смешан из общего и проектного: file=%q env=%q", w.TokenFile, w.TokenEnv)
	}
	if d := cfg.MCP.Servers["docs"]; d.On() || d.URL != "http://mcp.example:8378/mcp" {
		t.Errorf("docs: %+v", d)
	}
	if o := cfg.MCP.Servers["other"]; o.URL != "http://mcp.example:9000/mcp" || len(o.Allow) != 1 {
		t.Errorf("other: %+v", o)
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
