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

// Правило перевеса (этап 126, Я1): ключ hierarchy_majority доходит до правил
// графа из [graph] и из [graph.<имя>], подраздел без ключа наследует общий;
// без ключа вовсе — ноль, то есть умолчание кода 3 при открытии.
func TestHierarchyMajoritySetting(t *testing.T) {
	if got := writeCfg(t, baseCfg).Graph.Rules(); got.HierarchyMajority != 0 || got.Normalized().HierarchyMajority != graph.DefaultHierarchyMajority {
		t.Fatalf("без ключа K=%d (после умолчаний %d), ожидалось 0 и %d",
			got.HierarchyMajority, got.Normalized().HierarchyMajority, graph.DefaultHierarchyMajority)
	}
	cfg := writeCfg(t, baseCfg+`hierarchy_majority = -1

[graph.lab]
hierarchy_majority = 2

[graph.other]
model = "qwen3.8:latest"
`)
	if got := cfg.Graph.Rules().HierarchyMajority; got != -1 {
		t.Fatalf("[graph] hierarchy_majority = -1 дал %d", got)
	}
	for name, want := range map[string]int{"lab": 2, "other": -1} {
		if err := cfg.UseGraph(name); err != nil {
			t.Fatal(err)
		}
		if got := cfg.Graph.Rules().HierarchyMajority; got != want {
			t.Errorf("граф %s: K=%d, ожидалось %d", name, got, want)
		}
	}
	if len(cfg.Warnings) != 0 {
		t.Errorf("ключ назван неизвестным: %v", cfg.Warnings)
	}
}

// Число цепочек «как связаны X и Y» (этап 126, Я2): ключ path_count доходит
// до правил графа из [graph] и из [graph.<имя>], подраздел без ключа наследует
// общий; без ключа — ноль, при открытии 1. Отрицательное при открытии тоже 1:
// выключать нечего.
func TestPathCountSetting(t *testing.T) {
	if got := writeCfg(t, baseCfg).Graph.Rules(); got.PathCount != 0 || got.Normalized().PathCount != graph.DefaultPathCount {
		t.Fatalf("без ключа путей %d (после умолчаний %d), ожидалось 0 и %d",
			got.PathCount, got.Normalized().PathCount, graph.DefaultPathCount)
	}
	cfg := writeCfg(t, baseCfg+`path_count = 3

[graph.lab]
path_count = -1

[graph.other]
model = "qwen3.8:latest"
`)
	if got := cfg.Graph.Rules(); got.PathCount != 3 || got.Normalized().PathCount != 3 {
		t.Fatalf("[graph] path_count = 3 дал %d (после умолчаний %d)", got.PathCount, got.Normalized().PathCount)
	}
	for name, want := range map[string]int{"lab": 1, "other": 3} {
		if err := cfg.UseGraph(name); err != nil {
			t.Fatal(err)
		}
		if got := cfg.Graph.Rules().Normalized().PathCount; got != want {
			t.Errorf("граф %s: путей %d, ожидалось %d", name, got, want)
		}
	}
	if len(cfg.Warnings) != 0 {
		t.Errorf("ключ назван неизвестным: %v", cfg.Warnings)
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

// graph.partition: leiden у именованного графа, умолчание — пусто (Лувен),
// опечатка отвергается при загрузке (этап 105, Б6).
func TestGraphPartitionSetting(t *testing.T) {
	cfg := writeCfg(t, baseCfg+`
[graph.lab]
partition = "leiden"
`)
	if cfg.Graph.Partition != "" {
		t.Fatalf("у рабочего графа алгоритм должен остаться умолчанием, получено %q", cfg.Graph.Partition)
	}
	if err := cfg.UseGraph("lab"); err != nil {
		t.Fatal(err)
	}
	if cfg.Graph.Partition != "leiden" {
		t.Fatalf("у графа lab алгоритм %q, ожидался leiden", cfg.Graph.Partition)
	}
	for _, body := range []string{"[graph]\npartition = \"kmeans\"\n", "[graph.lab]\npartition = \"Leiden \"\n"} {
		p := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(p, []byte(baseCfg+"\n"+body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Load(p); err == nil {
			t.Fatalf("конфиг с %q должен отвергаться", body)
		}
	}
}
