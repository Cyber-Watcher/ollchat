package main

import (
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/mcp"
)

// Образец в репозитории — годный файл и в точности умолчания: человек,
// скопировавший его как есть, не должен получить другое поведение.
func TestSettingsExampleIsDefaults(t *testing.T) {
	ex, err := mcp.LoadSettings("ollmcp.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := ex.Policy(64)
	def, _ := mcp.Settings{}.Policy(64)
	if got.OutputBytes != def.OutputBytes || got.CallTimeout != def.CallTimeout || ex.Watch() != (mcp.Settings{}).Watch() {
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
