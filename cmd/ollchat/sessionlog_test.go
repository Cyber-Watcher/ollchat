package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// Запись начала сеанса (этап 112, Е6, Ц2): отпечатки меняются вместе с тем,
// что отпечатывают, а сами тексты промпта и правил в журнал не попадают.
func TestSessionStepFingerprints(t *testing.T) {
	cfg := &config.Config{}
	cfg.Permissions.Allow = []string{"Read(./**)"}
	cfg.KB.EmbedModel, cfg.KB.RerankModel = "bge-m3", "bge-reranker-v2-m3"
	srv := &config.Server{Name: "проба", SystemPrompt: "Ты — секретный помощник Икс."}
	sb, err := permissions.NewSandbox(t.TempDir(), false, false, 512)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tools.NewRegistry([]string{tools.NameReadFile, tools.NameListDir}, tools.Options{Sandbox: sb, MaxOutputKB: 64})
	if err != nil {
		t.Fatal(err)
	}
	a := sessionStep(cfg, srv, "m1", reg)
	if a.Kind != steplog.KindSession || a.Model != "m1" || a.Extra["tools_n"] != 2 || a.Extra["build"] == "" ||
		a.Extra["embed_model"] != "bge-m3" || a.Extra["rerank_model"] != "bge-reranker-v2-m3" {
		t.Fatalf("запись: %+v", a)
	}
	raw, _ := json.Marshal(a)
	if strings.Contains(string(raw), "секретный") || strings.Contains(string(raw), "Read(./**)") {
		t.Fatalf("в журнал ушёл сам текст промпта или правил: %s", raw)
	}

	srv2 := *srv
	srv2.SystemPrompt = "Ты — другой помощник."
	b := sessionStep(cfg, &srv2, "m1", reg)
	if a.Extra["system_prompt"] == b.Extra["system_prompt"] {
		t.Error("отпечаток системного промпта не изменился вместе с ним")
	}
	if a.Extra["permissions"] != b.Extra["permissions"] || a.Extra["tools"] != b.Extra["tools"] {
		t.Error("отпечатки правил или инструментов изменились без причины")
	}
	cfg.Permissions.Deny = []string{"Bash(rm:*)"}
	c := sessionStep(cfg, srv, "m1", reg)
	if c.Extra["permissions"] == a.Extra["permissions"] {
		t.Error("отпечаток правил не изменился вместе с ними")
	}
	if d := sessionStep(cfg, srv, "m1", nil); d.Extra["tools"] != nil {
		t.Errorf("без инструментов отпечаток набора не нужен: %+v", d.Extra)
	}
}
