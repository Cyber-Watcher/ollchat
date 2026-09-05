package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Узлы сборки графа (этап 95): разбор, наследование и проверка настроек.

const nodesCfg = baseCfg + `
[[graph.nodes]]
name    = "a100"
url     = "http://ollama.example:11434"
workers = 4

[[graph.nodes]]
name = "rtx3090"
url  = "http://ollama.example:11435"
`

// Узлы читаются, а незаданный workers берётся из graph.workers: у карты,
// про которую ничего не сказано, слотов столько же, сколько было у одиночного
// сервера, — а не ноль и не потолок.
func TestGraphNodesParsed(t *testing.T) {
	cfg := writeCfg(t, nodesCfg)
	nodes := cfg.Graph.ExtractNodes()
	if len(nodes) != 2 {
		t.Fatalf("узлов %d, ожидалось 2", len(nodes))
	}
	if nodes[0].Name != "a100" || nodes[0].Workers != 4 {
		t.Errorf("первый узел: %+v", nodes[0])
	}
	if nodes[1].Name != "rtx3090" || nodes[1].Workers != cfg.Graph.Workers {
		t.Errorf("второй узел не унаследовал workers: %+v", nodes[1])
	}
}

// Без раздела узлов сборка идёт по-старому: ExtractNodes пуст, и пул
// не заводится вовсе.
func TestGraphWithoutNodes(t *testing.T) {
	cfg := writeCfg(t, baseCfg)
	if n := cfg.Graph.ExtractNodes(); len(n) != 0 {
		t.Errorf("узлы взялись из ниоткуда: %+v", n)
	}
}

// Опытный граф наследует узлы общего раздела и умеет задать свои.
func TestNamedGraphNodes(t *testing.T) {
	cfg := writeCfg(t, nodesCfg+`
[graph.lab]
format = 2
`)
	if got := len(cfg.GraphFor("lab").ExtractNodes()); got != 2 {
		t.Errorf("опытный граф не унаследовал узлы: %d", got)
	}

	cfg2 := writeCfg(t, nodesCfg+`
[graph.lab]
format = 2

[[graph.lab.nodes]]
name = "только-эта"
url  = "http://ollama.example:11436"
`)
	own := cfg2.GraphFor("lab").ExtractNodes()
	if len(own) != 1 || own[0].Name != "только-эта" {
		t.Errorf("свои узлы опытного графа не перекрыли общие: %+v", own)
	}
	if len(cfg2.Graph.ExtractNodes()) != 2 {
		t.Error("узлы рабочего графа изменились из-за раздела опытного")
	}
}

// Дурные настройки — ошибка запуска с именем узла, а не выяснение через час
// работы, когда узел впервые понадобится.
func TestGraphNodesValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"без имени", `
[[graph.nodes]]
url = "http://ollama.example:11434"
`, "не задано имя"},
		{"без адреса", `
[[graph.nodes]]
name = "a100"
`, "не задан url"},
		{"имя дважды", `
[[graph.nodes]]
name = "a100"
url  = "http://ollama.example:11434"

[[graph.nodes]]
name = "a100"
url  = "http://ollama.example:11435"
`, "дважды"},
		{"чужая схема", `
[[graph.nodes]]
name = "a100"
url  = "ftp://ollama.example:11434"
`, "http://"},
		{"слишком много слотов", `
[[graph.nodes]]
name    = "a100"
url     = "http://ollama.example:11434"
workers = 99
`, "от 1 до 16"},
	}
	for _, c := range cases {
		p := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(p, []byte(baseCfg+c.body), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, err := Load(p)
		if err == nil {
			t.Errorf("%s: конфиг принят", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: невнятный отказ: %v", c.name, err)
		}
	}
}
