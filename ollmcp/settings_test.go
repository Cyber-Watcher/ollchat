package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

func writeSettings(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ollmcp.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Файла нет — всё как было до него: пределы из описаний инструментов,
// потолок ответа — agent.max_output_kb, срока нет, слежка включена.
func TestSettingsDefaults(t *testing.T) {
	s, err := LoadSettings(filepath.Join(t.TempDir(), "нет.toml"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Policy(64)
	if err != nil {
		t.Fatal(err)
	}
	if p.OutputBytes != 64*1024 || p.CallTimeout != 0 || !s.Watch() {
		t.Errorf("умолчания: %+v, watch=%v", p, s.Watch())
	}
	want := map[string]map[string]int{
		tools.NameSearch: {"top_k": 20}, tools.NameKBRead: {"around": 5},
		tools.NameGraphPath: {"max_hops": 6}, tools.NameGraphOverview: {"top_k": 10},
		tools.NameWebSearch: {"limit": 10},
	}
	for tool, params := range want {
		for param, max := range params {
			if got := p.Limits[tool][param].Max; got != max {
				t.Errorf("%s.%s: предел %d, ждали %d", tool, param, got, max)
			}
		}
	}
}

func TestSettingsValues(t *testing.T) {
	s, err := LoadSettings(writeSettings(t, `
output_kb = 32
call_timeout = "30s"
watch_config = false
[limits]
search_top_k = 8
graph_path_max_hops = 9
`))
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Policy(64)
	if err != nil {
		t.Fatal(err)
	}
	if p.OutputBytes != 32*1024 || p.CallTimeout != 30*time.Second || s.Watch() {
		t.Errorf("значения не применились: %+v, watch=%v", p, s.Watch())
	}
	if p.Limits[tools.NameSearch]["top_k"].Max != 8 || p.Limits[tools.NameGraphPath]["max_hops"].Max != 9 {
		t.Errorf("пределы: %+v", p.Limits)
	}
	// Отрицательный потолок ответа — без потолка.
	s.OutputKB = -1
	if p, _ := s.Policy(64); p.OutputBytes != 0 {
		t.Errorf("output_kb = -1 должен снимать потолок, а он %d", p.OutputBytes)
	}
}

// Негодные настройки — отказ с понятной причиной, а не молчаливое умолчание.
func TestSettingsRejected(t *testing.T) {
	cases := map[string]string{
		"опечатка в ключе":       "[limits]\nsearch_topk = 5\n",
		"выше потолка кода":      "[limits]\nsearch_top_k = 50\n",
		"ниже минимума":          "[limits]\ngraph_overview_top_k = -2\n",
		"негодный срок":          "call_timeout = \"полминуты\"\n",
		"не TOML":                "output_kb = = 3\n",
		"around выше потолка 5":  "[limits]\nkb_read_around = 6\n",
		"web_search выше 10":     "[limits]\nweb_search_limit = 11\n",
		"неизвестный ключ корня": "outputkb = 3\n",
	}
	for name, body := range cases {
		if _, err := LoadSettings(writeSettings(t, body)); err == nil {
			t.Errorf("%s: принято, а должно быть отвергнуто", name)
		} else if !strings.Contains(err.Error(), "ollmcp.toml") {
			t.Errorf("%s: в ошибке нет имени файла: %v", name, err)
		}
	}
}

// Образец в репозитории — годный файл и в точности умолчания: человек,
// скопировавший его как есть, не должен получить другое поведение.
func TestSettingsExampleIsDefaults(t *testing.T) {
	ex, err := LoadSettings("ollmcp.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := ex.Policy(64)
	def, _ := Settings{}.Policy(64)
	if got.OutputBytes != def.OutputBytes || got.CallTimeout != def.CallTimeout || ex.Watch() != (Settings{}).Watch() {
		t.Errorf("образец расходится с умолчаниями: %+v против %+v", got, def)
	}
	for tool, params := range def.Limits {
		for param, l := range params {
			if got.Limits[tool][param] != l {
				t.Errorf("%s.%s: в образце %+v, умолчание %+v", tool, param, got.Limits[tool][param], l)
			}
		}
	}
}
