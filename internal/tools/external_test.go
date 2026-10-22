package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// extFake — инструмент стороннего сервера для проверок реестра.
type extFake struct{ name, desc string }

func (f extFake) Name() string { return f.name }
func (f extFake) Spec() ollama.Tool {
	return ollama.Tool{Type: "function", Function: ollama.ToolSpec{Name: f.name, Description: f.desc,
		Parameters: ollama.ToolParams{Type: "object"}}}
}
func (f extFake) Plan(map[string]any) (*Plan, error) {
	return &Plan{Tool: f.name, Run: func(context.Context) (string, error) { return "ok " + f.name, nil }}, nil
}

func TestExternalNames(t *testing.T) {
	if got := ExternalName("wiki", "page.read"); got != "mcp__wiki__page.read" {
		t.Fatalf("имя: %s", got)
	}
	cases := map[string][3]string{
		"mcp__wiki__search":    {"wiki", "search", "ok"},
		"mcp__wiki__a__b":      {"wiki", "a__b", "ok"}, // «__» в имени инструмента законно
		"mcp__wiki__":          {"", "", ""},
		"mcp____x":             {"", "", ""},
		"search":               {"", "", ""},
		"mcp_wiki__search":     {"", "", ""},
		"mcp__wiki-two__x.y-z": {"wiki-two", "x.y-z", "ok"},
	}
	for name, want := range cases {
		s, tool, ok := SplitExternalName(name)
		if s != want[0] || tool != want[1] || ok != (want[2] == "ok") {
			t.Errorf("%s: %q %q %v", name, s, tool, ok)
		}
		if IsExternalName(name) != ok {
			t.Errorf("IsExternalName(%s) = %v", name, !ok)
		}
	}
}

// Внешние инструменты — после встроенных, по серверам в алфавитном порядке;
// их нет в Names (там только agent.tools); Plan и Has их знают; пустой
// список убирает сервер.
func TestRegistryExternalTools(t *testing.T) {
	reg, err := NewRegistry([]string{NameReadFile}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.SetExternal("zeta", []Tool{extFake{name: "mcp__zeta__one"}}); err != nil {
		t.Fatal(err)
	}
	if err := reg.SetExternal("alpha", []Tool{extFake{name: "mcp__alpha__b"}, extFake{name: "mcp__alpha__a"}}); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range reg.Specs() {
		names = append(names, s.Function.Name)
	}
	if strings.Join(names, ",") != "read_file,mcp__alpha__b,mcp__alpha__a,mcp__zeta__one" {
		t.Errorf("порядок в запросе к модели: %v", names)
	}
	if strings.Join(reg.Names(), ",") != NameReadFile {
		t.Errorf("внешние попали в Names: %v", reg.Names())
	}
	if !reg.Has("mcp__alpha__a") || reg.Has("mcp__alpha__c") {
		t.Error("Has не видит внешний инструмент или видит лишний")
	}
	plan, err := reg.Plan("mcp__zeta__one", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := plan.Run(context.Background()); out != "ok mcp__zeta__one" {
		t.Errorf("исполнен не тот инструмент: %q", out)
	}
	if err := reg.SetExternal("alpha", nil); err != nil {
		t.Fatal(err)
	}
	if reg.Has("mcp__alpha__a") || len(reg.ExternalNames()) != 1 {
		t.Errorf("сервер не убран: %v", reg.ExternalNames())
	}
	if _, err := reg.Plan("mcp__alpha__a", nil); err == nil || !strings.Contains(err.Error(), "mcp__zeta__one") {
		t.Errorf("неизвестный инструмент: %v", err)
	}
}

// Встроенные не заменяются; имя чужого сервера и дубль — отказ всего набора.
func TestRegistryExternalRejects(t *testing.T) {
	reg, err := NewRegistry([]string{NameReadFile}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	bad := map[string][]Tool{
		"встроенное имя": {extFake{name: NameReadFile}},
		"чужой сервер":   {extFake{name: "mcp__other__x"}},
		"дубль":          {extFake{name: "mcp__wiki__x"}, extFake{name: "mcp__wiki__x"}},
		"без приставки":  {extFake{name: "wiki__x"}},
	}
	for what, list := range bad {
		if err := reg.SetExternal("wiki", list); err == nil {
			t.Errorf("%s: набор принят", what)
		}
	}
	if len(reg.ExternalNames()) != 0 {
		t.Errorf("отвергнутый набор попал в реестр наполовину: %v", reg.ExternalNames())
	}
	if specs := reg.Specs(); len(specs) != 1 || specs[0].Function.Name != NameReadFile ||
		!strings.Contains(specs[0].Function.Description, "файл") {
		t.Errorf("встроенный read_file подменён: %+v", specs)
	}
}

// Набор внешних меняется, пока модель читает реестр в другой горутине
// (переподключение посреди хода) — гонку ловит -race.
func TestRegistryExternalConcurrent(t *testing.T) {
	reg, err := NewRegistry([]string{NameReadFile}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = reg.SetExternal("wiki", []Tool{extFake{name: fmt.Sprintf("mcp__wiki__t%d", j%3)}})
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = reg.Specs()
				_ = reg.Has("mcp__wiki__t1")
				_, _ = reg.Plan("mcp__wiki__t2", nil)
				_ = reg.ExternalNames()
			}
		}()
	}
	wg.Wait()
}
