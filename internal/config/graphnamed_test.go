package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

func writeCfg(t *testing.T, body string) *Config {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, ok, err := Load(p)
	if err != nil {
		t.Fatalf("конфиг не прочитался: %v", err)
	}
	if !ok {
		t.Fatal("конфиг не найден")
	}
	return cfg
}

const baseCfg = `
[general]
mode           = "safe"
default_server = "lab"

[[servers]]
name  = "lab"
url   = "http://ollama.example:11434"
model = "qwen3.8:latest"

[graph]
model       = "qwen3.8:latest"
workers     = 4
num_ctx     = 4096
temperature = 0.2
`

// Главное свойство: без раздела опытного графа настройки рабочего не меняются
// ни на поле. Тест стоит здесь затем, что подмена настроек при выборе графа
// может незаметно сломать сборку рабочего — того, что стоил недель карты.
func TestWorkingGraphSettingsUnchanged(t *testing.T) {
	cfg := writeCfg(t, baseCfg)
	before := cfg.Graph
	if err := cfg.UseGraph(""); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, cfg.Graph) {
		t.Fatalf("настройки рабочего графа изменились:\nбыло  %+v\nстало %+v", before, cfg.Graph)
	}
}

// Раздел графа перекрывает только написанное в нём, остальное наследуется.
func TestNamedGraphInheritsAndOverrides(t *testing.T) {
	cfg := writeCfg(t, baseCfg+`
[graph.lab]
model       = "glm-4.7-flash:q8_0"
temperature = 0.7
`)
	lab := cfg.GraphFor("lab")
	if lab.Model != "glm-4.7-flash:q8_0" || lab.Temperature != 0.7 {
		t.Fatalf("перекрытие не сработало: %+v", lab)
	}
	if lab.Workers != cfg.Graph.Workers || lab.NumCtx != cfg.Graph.NumCtx {
		t.Fatalf("ненаписанное не унаследовалось: workers=%d num_ctx=%d", lab.Workers, lab.NumCtx)
	}
	if lab.Name != "lab" {
		t.Fatalf("имя графа не проставлено: %q", lab.Name)
	}
	// И общий раздел от этого не пострадал.
	if cfg.Graph.Model != "qwen3.8:latest" || cfg.Graph.Temperature != 0.2 {
		t.Fatalf("общий раздел испорчен подразделом: %+v", cfg.Graph)
	}
}

// Выбор графа подменяет и каталог, и настройки — одним действием.
func TestUseGraphSwitchesSettings(t *testing.T) {
	cfg := writeCfg(t, baseCfg+`
[graph.lab]
model = "glm-4.7-flash:q8_0"
`)
	if err := cfg.UseGraph("lab"); err != nil {
		t.Fatal(err)
	}
	if cfg.Graph.Model != "glm-4.7-flash:q8_0" {
		t.Fatalf("настройки не подменились: %q", cfg.Graph.Model)
	}
	if err := cfg.UseGraph(""); err != nil {
		t.Fatal(err)
	}
	if cfg.Graph.Model != "qwen3.8:latest" {
		t.Fatalf("возврат к рабочему графу не вернул настройки: %q", cfg.Graph.Model)
	}
}

// Графа без раздела достаточно: он собирается настройками рабочего.
func TestNamedGraphWithoutSection(t *testing.T) {
	cfg := writeCfg(t, baseCfg)
	lab := cfg.GraphFor("lab")
	if lab.Model != cfg.Graph.Model || lab.Workers != cfg.Graph.Workers {
		t.Fatalf("настройки не унаследованы: %+v", lab)
	}
}

// Имя раздела становится каталогом на диске, поэтому мусор отвергается
// при чтении конфига, а не при открытии графа.
func TestBadNamedGraphRejected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(baseCfg+"\n[graph.\"../побег\"]\nmodel = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(p); err == nil {
		t.Fatal("недопустимое имя графа принято")
	}
}

// Правила словесного входа обязаны применяться при выборе графа, иначе
// достигнутое замером 03.09.2026 (найдено 60 из 60, среднее место 1.50)
// потеряется молча: числа останутся в конфиге, а вход будет работать по-старому.
func TestStemRulesAppliedOnUseGraph(t *testing.T) {
	cfg := writeCfg(t, baseCfg+`
[graph.lab]
stem_min_len   = 5
stem_min_books = 7
`)
	if err := cfg.UseGraph("lab"); err != nil {
		t.Fatal(err)
	}
	if cfg.Graph.StemMinLen != 5 || cfg.Graph.StemMinBooks != 7 {
		t.Fatalf("настройки графа не подхвачены: %+v", cfg.Graph)
	}
	if r := cfg.Graph.Rules(); r.StemMinLen != 5 || r.StemMinBooks != 7 || r.Name != "lab" {
		t.Fatalf("правила не собрались из настроек графа: %+v", r)
	}
	// Возврат к рабочему графу возвращает и правила.
	if err := cfg.UseGraph(""); err != nil {
		t.Fatal(err)
	}
	if r := cfg.Graph.Rules().Normalized(); r.StemMinLen != graph.DefaultStemMinLen || r.StemMinBooks != graph.DefaultStemMinBooks {
		t.Fatalf("правила рабочего графа не вернулись: %+v", r)
	}
}

// Формат 2 — только у именованного графа: в общем разделе он означал бы
// заведение рабочего графа новой схемой (решение владельца 04.09.2026).
func TestGraphFormatOnlyForNamedGraph(t *testing.T) {
	cfg := writeCfg(t, baseCfg+`
[graph.lab]
format = 2
`)
	if cfg.Graph.Rules().Format != 0 {
		t.Fatalf("у рабочего графа формат должен остаться умолчанием, получено %d", cfg.Graph.Rules().Format)
	}
	if err := cfg.UseGraph("lab"); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Graph.Rules().Format; got != 2 {
		t.Fatalf("у графа lab формат %d, ожидался 2", got)
	}
	if err := cfg.UseGraph(""); err != nil {
		t.Fatal(err)
	}
	if cfg.Graph.Rules().Format != 0 {
		t.Fatal("возврат к рабочему графу оставил формат опытного")
	}

	for _, body := range []string{"[graph]\nformat = 2\n", "[graph.lab]\nformat = 7\n"} {
		p := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(p, []byte(baseCfg+"\n"+body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Load(p); err == nil {
			t.Fatalf("конфиг с %q должен отвергаться", body)
		}
	}
}
