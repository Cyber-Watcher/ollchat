package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Роли серверов по имени (этап 123): [graph].server, kb.embed_server и узлы
// у самого сервера, [[servers.nodes]].

const rolesServers = `
[[graph.nodes]]
name = "old"
url  = "http://ollama.example:11111"

[kb]
embed_model = "bge-m3"
embed_url   = "http://ollama.example:11111"

[[servers]]
name  = "tuf"
url   = "http://tuf.example:11434/"
model = "qwen3.8:latest"

[servers.headers]
Authorization = "Bearer tuf"

[[servers.nodes]]
name    = "gpu0"
url     = "http://tuf.example:11434"
workers = 2
probe   = "http://tuf.example:11435"

[[servers.nodes]]
name = "gpu1"
url  = "http://tuf.example:11436"

[[servers]]
name  = "solo"
url   = "http://solo.example:11434"
model = "qwen3.8:latest"
probe = "http://solo.example:11435"
`

// rolesCfg — общий образец с server в [graph] (пусто — без ключа) и
// дописанным хвостом.
func rolesCfg(graphServer, tail string) string {
	base := baseCfg
	if graphServer != "" {
		base = strings.Replace(base, "[graph]\n", "[graph]\nserver = \""+graphServer+"\"\n", 1)
	}
	return base + rolesServers + tail
}

func loadErr(t *testing.T, body string) error {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(p)
	return err
}

func nodeNames(nodes []GraphNode) string {
	var out []string
	for _, n := range nodes {
		out = append(out, n.Name)
	}
	return strings.Join(out, ",")
}

// Сервер со своими узлами: адрес и узлы сборки — его, [[graph.nodes]]
// не действуют, и об этом сказано замечанием, а не молча (my-mistakes #140).
func TestGraphServerTakesItsNodes(t *testing.T) {
	cfg := writeCfg(t, rolesCfg("tuf", ""))
	if cfg.Graph.URL != "http://tuf.example:11434" {
		t.Errorf("адрес графа %q, ждали адрес сервера tuf без косой черты", cfg.Graph.URL)
	}
	nodes := cfg.Graph.ExtractNodes()
	if got := nodeNames(cfg.Graph.Nodes); got != "gpu0,gpu1" {
		t.Fatalf("узлы %q, ждали gpu0,gpu1 — узлы сервера вместо [[graph.nodes]]", got)
	}
	if nodes[0].Workers != 2 || nodes[0].Probe != "http://tuf.example:11435" {
		t.Errorf("первый узел: %+v", nodes[0])
	}
	if nodes[1].Workers != cfg.Graph.Workers {
		t.Errorf("второй узел не унаследовал graph.workers: %+v", nodes[1])
	}
	if !strings.Contains(strings.Join(cfg.Warnings, "\n"), "перекрывает [[graph.nodes]]") {
		t.Errorf("перекрытые узлы не названы замечанием: %q", cfg.Warnings)
	}
}

// Сервер без узлов, но с probe — один узел ради наблюдателя; без probe —
// один адрес, без пула, как при [graph].url.
func TestGraphServerWithoutNodes(t *testing.T) {
	cfg := writeCfg(t, rolesCfg("solo", ""))
	n := cfg.Graph.ExtractNodes()
	if len(n) != 1 || n[0].Name != "solo" || n[0].URL != "http://solo.example:11434" ||
		n[0].Probe != "http://solo.example:11435" {
		t.Errorf("сервер с probe не стал одним узлом: %+v", n)
	}

	cfg = writeCfg(t, rolesCfg("lab", ""))
	if n := cfg.Graph.ExtractNodes(); len(n) != 0 {
		t.Errorf("сервер без узлов и probe дал пул: %+v", n)
	}
	if cfg.Graph.URL != "http://ollama.example:11434" {
		t.Errorf("адрес графа %q, ждали адрес lab", cfg.Graph.URL)
	}
}

// Опечатка в имени — ошибка запуска с перечнем имён, а не тихая сборка
// на прежнем адресе.
func TestGraphServerUnknownName(t *testing.T) {
	err := loadErr(t, rolesCfg("tfu", ""))
	if err == nil || !strings.Contains(err.Error(), `graph.server = "tfu"`) ||
		!strings.Contains(err.Error(), "lab, tuf, solo") {
		t.Errorf("ошибка %v, ждали имя ключа и перечень серверов", err)
	}
	if err := loadErr(t, rolesCfg("", "")); err != nil {
		t.Fatalf("образец без server не прочитался: %v", err)
	}
	body := strings.Replace(rolesCfg("", ""), "[kb]\n", "[kb]\nembed_server = \"nope\"\n", 1)
	if err := loadErr(t, body); err == nil || !strings.Contains(err.Error(), "kb.embed_server") {
		t.Errorf("неизвестный kb.embed_server прошёл: %v", err)
	}
}

// Эмбеддер на выбранном сервере: адрес, запасной адрес и заголовки — его,
// а не первого сервера и не сервера чата.
func TestEmbedServer(t *testing.T) {
	body := strings.Replace(rolesCfg("", ""), "[kb]\n", "[kb]\nembed_server = \"tuf\"\n", 1)
	cfg := writeCfg(t, body)
	if cfg.KB.EmbedURL != "http://tuf.example:11434" {
		t.Errorf("kb.embed_url %q, ждали адрес tuf", cfg.KB.EmbedURL)
	}
	// Запасной адрес — прежний первый сервер: его же берут извлечение графа
	// при пустом [graph].url и переводчик, и сервер эмбеддера им не годится.
	if cfg.EmbedFallback() != "http://ollama.example:11434" || cfg.Graph.URL != "" {
		t.Errorf("embed_server сдвинул запасной адрес %q или адрес графа %q", cfg.EmbedFallback(), cfg.Graph.URL)
	}
	lab, _ := cfg.ServerByName("lab")
	if h := cfg.EmbedHeadersFor(lab); h["Authorization"] != "Bearer tuf" {
		t.Errorf("заголовки эмбеддера %v — ушли бы заголовки сервера чата", h)
	}
	if h := cfg.EmbedHeaders(); h["Authorization"] != "Bearer tuf" {
		t.Errorf("заголовки эмбеддера службы %v", h)
	}
	if h := cfg.EmbedFallbackHeaders(); h != nil {
		t.Errorf("заголовки переводчика %v — ждали заголовки первого сервера (их нет)", h)
	}
	if !strings.Contains(strings.Join(cfg.Warnings, "\n"), "перекрывает kb.embed_url") {
		t.Errorf("перекрытый kb.embed_url не назван: %q", cfg.Warnings)
	}

	// Без embed_server — как раньше: заголовки сервера чата.
	cfg = writeCfg(t, rolesCfg("", ""))
	lab, _ = cfg.ServerByName("lab")
	lab.Headers = map[string]string{"Authorization": "Bearer lab"}
	if h := cfg.EmbedHeadersFor(lab); h["Authorization"] != "Bearer lab" {
		t.Errorf("без embed_server заголовки %v, ждали заголовки сервера чата", h)
	}
}

// Подраздел графа наследует server, как любой ключ, и умеет его сбросить.
func TestNamedGraphServer(t *testing.T) {
	cfg := writeCfg(t, rolesCfg("tuf", "\n[graph.lab]\nformat = 2\n"))
	if g := cfg.GraphFor("lab"); g.URL != "http://tuf.example:11434" || nodeNames(g.Nodes) != "gpu0,gpu1" {
		t.Errorf("опытный граф не унаследовал сервер: %q %q", g.URL, nodeNames(g.Nodes))
	}
	if err := cfg.UseGraph("lab"); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Graph.ExtractNodes()) != 2 || nodeNames(cfg.Graph.Nodes) != "gpu0,gpu1" {
		t.Errorf("после выбора графа узлы %q", nodeNames(cfg.Graph.Nodes))
	}

	cfg = writeCfg(t, rolesCfg("tuf", "\n[graph.lab]\nformat = 2\nserver = \"\"\n"))
	if g := cfg.GraphFor("lab"); nodeNames(g.Nodes) != "old" {
		t.Errorf("server = \"\" не сбросил выбор: узлы %q", nodeNames(g.Nodes))
	}
	if cfg.Graph.URL != "http://tuf.example:11434" {
		t.Errorf("рабочий граф потерял сервер: %q", cfg.Graph.URL)
	}
}

// Узлы сервера проверяются так же строго, как [[graph.nodes]].
func TestServerNodesValidated(t *testing.T) {
	body := rolesCfg("", "\n[[servers]]\nname = \"bad\"\nurl = \"http://bad.example:1\"\n\n[[servers.nodes]]\nname = \"x\"\n")
	if err := loadErr(t, body); err == nil || !strings.Contains(err.Error(), "servers.bad.nodes") {
		t.Errorf("узел без url прошёл: %v", err)
	}
	body = rolesCfg("", "\n[[servers]]\nname = \"bad\"\nurl = \"http://bad.example:1\"\nprobe = \"bad.example:11435\"\n")
	if err := loadErr(t, body); err == nil || !strings.Contains(err.Error(), "probe") {
		t.Errorf("probe без схемы прошёл: %v", err)
	}
}

// Новые ключи известны разбору: о них нет замечания «неизвестный ключ».
func TestRoleKeysAreKnown(t *testing.T) {
	body := strings.Replace(rolesCfg("tuf", ""), "[kb]\n", "[kb]\nembed_server = \"solo\"\n", 1)
	cfg := writeCfg(t, body)
	for _, w := range cfg.Warnings {
		if strings.Contains(w, "server") && !strings.Contains(w, "перекрывает") {
			t.Errorf("замечание о новом ключе: %q", w)
		}
	}
}
