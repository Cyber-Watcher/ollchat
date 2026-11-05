package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// roleNode — подставной узел Ollama для --role-check: отдаёт тег tag с файлом
// весов weights и пишет, какие пути у него спрашивали (модель грузить ключ
// не вправе: ни /api/chat, ни /api/generate).
type roleNode struct {
	srv          *httptest.Server
	mu           sync.Mutex
	paths        []string
	tag, weights string
}

func newRoleNode(t *testing.T, tag, weights string) *roleNode {
	t.Helper()
	n := &roleNode{tag: tag, weights: weights}
	n.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.mu.Lock()
		n.paths = append(n.paths, r.URL.Path)
		n.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/version"):
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "0.40.11"})
		case strings.HasSuffix(r.URL.Path, "/api/tags"):
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{{
				"name": n.tag, "model": n.tag, "size": 17, "digest": "digest-" + n.weights,
				"details": map[string]any{"quantization_level": "Q4_K_M"},
			}}})
		case strings.HasSuffix(r.URL.Path, "/api/show"):
			var req struct {
				Model string `json:"model"`
				Name  string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Model != n.tag && req.Name != n.tag {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"model not found"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"modelfile":  "FROM /models/blobs/" + n.weights + "\nTEMPLATE {{ .Prompt }}\n",
				"template":   "{{ .Prompt }}",
				"parameters": "top_k 20",
				"details":    map[string]any{"quantization_level": "Q4_K_M"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(n.srv.Close)
	return n
}

// loaded — спрашивали ли у узла что-то, кроме версии, тегов и описания модели.
func (n *roleNode) loaded() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []string
	for _, p := range n.paths {
		if !strings.HasSuffix(p, "/api/version") && !strings.HasSuffix(p, "/api/tags") &&
			!strings.HasSuffix(p, "/api/show") {
			out = append(out, p)
		}
	}
	return out
}

// roleCheckBase — база в t.TempDir() с коллекцией books; passport — модель в
// паспорте графа, пусто — графа нет; weights — отпечаток весов в паспорте,
// записанный тем же Graph.SetWeights, что у сборки (нет — паспорт без него).
// Настоящих коллекций тест не касается.
func roleCheckBase(t *testing.T, passport string, weights ...graph.Weights) string {
	t.Helper()
	kbDir := t.TempDir()
	base, err := kb.OpenBase(kbDir)
	if err != nil {
		t.Fatal(err)
	}
	coll := base.CollectionDir("books")
	base.Close()
	if err := os.MkdirAll(coll, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(coll, "meta.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if passport != "" {
		g, err := graph.Create(coll, "books", 10, graph.Rules{})
		if err != nil {
			t.Fatal(err)
		}
		if err := g.SetModel(passport); err != nil {
			t.Fatal(err)
		}
		for _, w := range weights {
			if err := g.SetWeights(w); err != nil {
				t.Fatal(err)
			}
		}
		if err := g.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return kbDir
}

func roleCheckCfg(t *testing.T, kbDir, model string, nodes ...*roleNode) *config.Config {
	t.Helper()
	body := "[general]\ndefault_server = \"lab\"\n\n[kb]\ndir = \"" + kbDir + "\"\n\n" +
		"[[servers]]\nname  = \"lab\"\nurl   = \"http://127.0.0.1:1\"\nmodel = \"m\"\n\n" +
		"[graph]\nmodel = \"" + model + "\"\n"
	for i, n := range nodes {
		body += "\n[[graph.nodes]]\nname = \"" + string(rune('a'+i)) + "\"\nurl  = \"" + n.srv.URL + "\"\n"
	}
	cfg, _, err := config.Load(writeTmp(t, body))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// snapshot — содержимое всех файлов каталога: ключ только читает, и после
// сверки граф обязан остаться байт в байт прежним.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		out[p] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// --role-check (аудит 05.11.2026, п. 11.3): модель узлов сборки против паспорта
// графа — имя, наличие тега на каждом узле, веса узлов между собой; код 1 при
// расхождении, граф и коллекция не меняются, модель на карту не грузится.
func TestCheckRoleModel(t *testing.T) {
	const model = "qwen3.8:latest"
	cases := []struct {
		name     string
		passport string // модель в паспорте; пусто — графа нет
		cfgModel string
		tags     [2]string // теги на узлах a и b
		weights  [2]string
		ok       bool
		want     []string // строки, которые обязаны быть в выводе
	}{
		{name: "совпадает", passport: model, cfgModel: model,
			tags: [2]string{model, model}, weights: [2]string{"sha256-aaaa", "sha256-aaaa"}, ok: true,
			want: []string{"в паспорте модель " + model + ", в настройках " + model + " — совпадает",
				"узел a: модель " + model + " digest digest-sha25 файлы sha256-aaaa квантование Q4_K_M — совпадает",
				"узел b: модель " + model, "итог: совпадает", "отпечатка весов"}},
		{name: "на узле другой тег (#140 п. 2)", passport: model, cfgModel: model,
			tags: [2]string{model, "qwen3.8:27b-q4_K_M"}, weights: [2]string{"sha256-aaaa", "sha256-aaaa"},
			want: []string{"узел a: модель " + model, "узел b: модель " + model + " — расходится", "нет модели"}},
		{name: "разные веса под одним именем", passport: model, cfgModel: model,
			tags: [2]string{model, model}, weights: [2]string{"sha256-aaaa", "sha256-bbbb"},
			want: []string{"узел b: модель " + model, "— расходится", "разные веса"}},
		{name: "в настройках не та модель, что в паспорте", passport: "glm-4.7-flash:q8_0", cfgModel: model,
			tags: [2]string{model, model}, weights: [2]string{"sha256-aaaa", "sha256-aaaa"},
			want: []string{"в паспорте модель glm-4.7-flash:q8_0, в настройках " + model + " — расходится",
				"--graph-allow-model-change"}},
		{name: "графа ещё нет", passport: "", cfgModel: model,
			tags: [2]string{model, model}, weights: [2]string{"sha256-aaaa", "sha256-aaaa"}, ok: true,
			want: []string{"паспорта графа нет", "итог: совпадает"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := newRoleNode(t, c.tags[0], c.weights[0])
			b := newRoleNode(t, c.tags[1], c.weights[1])
			kbDir := roleCheckBase(t, c.passport)
			cfg := roleCheckCfg(t, kbDir, c.cfgModel, a, b)
			before := snapshot(t, kbDir)
			var out, errw strings.Builder
			err := checkRoleModel(&out, &errw, cfg, "books")
			if c.ok && err != nil {
				t.Fatalf("ждали совпадение, получено %v\n%s", err, out.String())
			}
			if !c.ok {
				var ec *exitCode
				if !errors.As(err, &ec) || ec.code != 1 {
					t.Fatalf("ждали код 1, получено %v\n%s", err, out.String())
				}
				if strings.Contains(out.String(), "итог: совпадает") {
					t.Fatalf("при расхождении напечатано «совпадает»:\n%s", out.String())
				}
			}
			for _, w := range c.want {
				if !strings.Contains(out.String(), w) {
					t.Errorf("в выводе нет «%s»:\n%s", w, out.String())
				}
			}
			if !strings.Contains(errw.String(), "сверяю модель") {
				t.Errorf("ход сверки не назван в stderr: %q", errw.String())
			}
			after := snapshot(t, kbDir)
			if len(after) != len(before) {
				t.Errorf("файлов в базе было %d, стало %d: ключ что-то записал", len(before), len(after))
			}
			for p, v := range before {
				if after[p] != v {
					t.Errorf("файл %s изменён сверкой", p)
				}
			}
			for _, n := range []*roleNode{a, b} {
				if l := n.loaded(); len(l) > 0 {
					t.Errorf("у узла спрошено лишнее (модель грузить нельзя): %v", l)
				}
			}
		})
	}
}

// Отпечаток весов в паспорте графа (решение владельца 05.11.2026): перетег под
// тем же именем на единственном узле — код 1, хотя имя совпадает и сравнивать
// узел не с кем; те же веса — совпадает; паспорт без отпечатка — не
// расхождение, так и печатается. Граф сверкой не меняется.
func TestCheckRoleModelPassportWeights(t *testing.T) {
	const model = "qwen3.8:latest"
	cases := []struct {
		name     string
		passport []string // файлы весов в паспорте; nil — паспорт без отпечатка
		node     string   // файл весов на узле
		ok       bool
		want     []string
	}{
		{name: "перетег на единственном узле", passport: []string{"sha256-aaaa"}, node: "sha256-bbbb",
			want: []string{"в паспорте отпечаток весов: файлы sha256-aaaa",
				"узел a: модель " + model + " digest digest-sha25 файлы sha256-bbbb квантование Q4_K_M — расходится: " +
					"веса не те, что в паспорте графа: в паспорте файлы sha256-aaaa, на узле файлы sha256-bbbb",
				"--graph-allow-model-change"}},
		{name: "те же веса", passport: []string{"sha256-aaaa"}, node: "sha256-aaaa", ok: true,
			want: []string{"узел a: модель " + model + " digest digest-sha25 файлы sha256-aaaa квантование Q4_K_M — совпадает",
				"итог: совпадает"}},
		{name: "паспорт без отпечатка", node: "sha256-bbbb", ok: true,
			want: []string{"в паспорте отпечатка весов нет, запишется первым заходом", "итог: совпадает"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := newRoleNode(t, model, c.node)
			var kbDir string
			if c.passport == nil {
				kbDir = roleCheckBase(t, model)
			} else {
				kbDir = roleCheckBase(t, model, graph.Weights{Files: c.passport})
			}
			before := snapshot(t, kbDir)
			var out, errw strings.Builder
			err := checkRoleModel(&out, &errw, roleCheckCfg(t, kbDir, model, a), "books")
			var ec *exitCode
			switch {
			case c.ok && err != nil:
				t.Fatalf("ждали совпадение, получено %v\n%s", err, out.String())
			case !c.ok && (!errors.As(err, &ec) || ec.code != 1):
				t.Fatalf("ждали код 1, получено %v\n%s", err, out.String())
			case !c.ok && strings.Contains(out.String(), "итог: совпадает"):
				t.Fatalf("при расхождении напечатано «совпадает»:\n%s", out.String())
			}
			for _, w := range c.want {
				if !strings.Contains(out.String(), w) {
					t.Errorf("в выводе нет «%s»:\n%s", w, out.String())
				}
			}
			after := snapshot(t, kbDir)
			for p, v := range before {
				if after[p] != v {
					t.Errorf("файл %s изменён сверкой", p)
				}
			}
			if len(after) != len(before) {
				t.Errorf("файлов в базе было %d, стало %d", len(before), len(after))
			}
		})
	}
}

// Узел не ответил вовсе — код 3 «не сверено», а не 1: очередь подождёт сервер,
// заход сверит модель сам. Расхождение на живом узле при этом сильнее: код 1.
func TestCheckRoleModelSilentNode(t *testing.T) {
	const model = "qwen3.8:latest"
	a := newRoleNode(t, model, "sha256-aaaa")
	gone := newRoleNode(t, model, "sha256-aaaa")
	gone.srv.Close() // адрес остаётся, а связи нет
	kbDir := roleCheckBase(t, model)
	var out, errw strings.Builder
	err := checkRoleModel(&out, &errw, roleCheckCfg(t, kbDir, model, a, gone), "books")
	var ec *exitCode
	if !errors.As(err, &ec) || ec.code != 3 {
		t.Fatalf("узел без связи: ждали код 3, получено %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "узел b: модель "+model+" — не сверена: узел не ответил") ||
		strings.Contains(out.String(), "итог: совпадает") {
		t.Errorf("строка узла без связи не та:\n%s", out.String())
	}
	other := newRoleNode(t, "qwen3.8:27b-q4_K_M", "sha256-aaaa")
	out.Reset()
	err = checkRoleModel(&out, &errw, roleCheckCfg(t, kbDir, model, gone, other), "books")
	if !errors.As(err, &ec) || ec.code != 1 {
		t.Fatalf("без связи и с чужим тегом: ждали код 1, получено %v\n%s", err, out.String())
	}
}

// Коллекции нет или graph.model пуст — ошибка, а не «совпадает»; узлы при
// этом не опрашиваются.
func TestCheckRoleModelRefusesUnknown(t *testing.T) {
	a := newRoleNode(t, "qwen3.8:latest", "sha256-aaaa")
	kbDir := roleCheckBase(t, "qwen3.8:latest")
	var out, errw strings.Builder
	if err := checkRoleModel(&out, &errw, roleCheckCfg(t, kbDir, "qwen3.8:latest", a), "нет-такой"); err == nil ||
		!strings.Contains(err.Error(), "коллекции \"нет-такой\" нет") {
		t.Errorf("неизвестная коллекция: ждали отказ, получено %v", err)
	}
	if err := checkRoleModel(&out, &errw, roleCheckCfg(t, kbDir, "", a), "books"); err == nil ||
		!strings.Contains(err.Error(), "graph.model пуст") {
		t.Errorf("пустой graph.model: ждали отказ, получено %v", err)
	}
	a.mu.Lock()
	asked := append([]string(nil), a.paths...)
	a.mu.Unlock()
	if len(asked) != 0 {
		t.Errorf("при отказе до сверки узел опрошен: %v", asked)
	}
}

// --role-check — команда ровно для одной коллекции и проходит разбор строки.
func TestRoleCheckFlag(t *testing.T) {
	if err := checkCommandLine(newFlags(t, "--role-check=books")); err != nil {
		t.Errorf("--role-check books отклонён: %v", err)
	}
	if err := checkCommandLine(newFlags(t, "--role-check=books", "--role-servers")); err == nil {
		t.Error("две команды разом (--role-check и --role-servers) приняты")
	}
}
