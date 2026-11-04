package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Роли серверов после аудита этапа 123: заголовки уходят только своему
// хосту, подраздел графа со своим адресом не уезжает за [graph].server,
// память и файл после /graph-server совпадают, конфиг проекта без сервера
// векторов накладывается, учётные данные в адресах не принимаются.

const safetyServers = `
[general]
mode           = "safe"
default_server = "a"

[[servers]]
name  = "a"
url   = "http://198.51.100.10:11434"
model = "m:latest"
[servers.headers]
Authorization = "Bearer secret-a"

[[servers]]
name  = "b"
url   = "http://198.51.100.11:11434"
model = "m:latest"
[servers.headers]
Authorization = "Bearer secret-b"
`

// Явный kb.embed_url на чужой хост: токен ни первого сервера, ни сервера
// чата туда не уходит. Тот же хост (другой порт) — заголовки его сервера;
// пустой embed_url — эмбеддер идёт на сам сервер, и заголовки его.
func TestEmbedHeadersOnlyForSameHost(t *testing.T) {
	foreign := writeCfg(t, safetyServers+"\n[kb]\nembed_model = \"bge-m3\"\nembed_url = \"http://198.51.100.99:11434\"\n")
	b, _ := foreign.ServerByName("b")
	if h := foreign.EmbedHeaders(); h != nil {
		t.Errorf("embed_url на чужом хосте получил заголовки первого сервера: %v", h)
	}
	if h := foreign.EmbedHeadersFor(b); h != nil {
		t.Errorf("embed_url на чужом хосте получил заголовки сервера чата: %v", h)
	}

	samePort := writeCfg(t, safetyServers+"\n[kb]\nembed_model = \"bge-m3\"\nembed_url = \"http://198.51.100.10:11500\"\n")
	a, _ := samePort.ServerByName("a")
	b, _ = samePort.ServerByName("b")
	if h := samePort.EmbedHeaders(); h["Authorization"] != "Bearer secret-a" {
		t.Errorf("embed_url на хосте первого сервера: заголовки %v, ждали его", h)
	}
	if h := samePort.EmbedHeadersFor(a); h["Authorization"] != "Bearer secret-a" {
		t.Errorf("embed_url на хосте сервера чата: заголовки %v, ждали его", h)
	}
	if h := samePort.EmbedHeadersFor(b); h != nil {
		t.Errorf("embed_url на хосте сервера a, чат на b: ушли заголовки b %v", h)
	}

	empty := writeCfg(t, safetyServers+"\n[kb]\nembed_model = \"bge-m3\"\n")
	b, _ = empty.ServerByName("b")
	if h := empty.EmbedHeaders(); h["Authorization"] != "Bearer secret-a" {
		t.Errorf("пустой embed_url: заголовки %v, ждали первого сервера", h)
	}
	if h := empty.EmbedHeadersFor(b); h["Authorization"] != "Bearer secret-b" {
		t.Errorf("пустой embed_url: заголовки %v, ждали сервера чата", h)
	}
}

// Подраздел графа со своим url или своими узлами не уезжает за
// унаследованным [graph].server; явный server подраздела перекрывает его
// узлы — с замечанием, как у [graph].
func TestNamedGraphOwnAddressBeatsInheritedServer(t *testing.T) {
	body := safetyServers + `
[graph]
model  = "m:latest"
server = "a"

[graph.lab]
url = "http://198.51.100.20:11434"

[graph.pool]
[[graph.pool.nodes]]
name = "p1"
url  = "http://198.51.100.21:11434"

[graph.exp]
server = "b"
[[graph.exp.nodes]]
name = "e1"
url  = "http://198.51.100.22:11434"

[graph.inh]
format = 2
`
	cfg := writeCfg(t, body)
	if g := cfg.GraphFor("lab"); g.URL != "http://198.51.100.20:11434" {
		t.Errorf("[graph.lab].url заменён унаследованным сервером: %q", g.URL)
	}
	if g := cfg.GraphFor("pool"); nodeNames(g.Nodes) != "p1" {
		t.Errorf("[[graph.pool.nodes]] заменены узлами унаследованного сервера: %q", nodeNames(g.Nodes))
	}
	if g := cfg.GraphFor("exp"); g.URL != "http://198.51.100.11:11434" || len(g.Nodes) != 0 {
		t.Errorf("явный server подраздела не перекрыл его узлы: %q %q", g.URL, nodeNames(g.Nodes))
	}
	if !strings.Contains(strings.Join(cfg.Warnings, "\n"), `graph.exp.server = "b" перекрывает [[graph.exp.nodes]]`) {
		t.Errorf("перекрытые узлы подраздела не названы: %q", cfg.Warnings)
	}
	if g := cfg.GraphFor("inh"); g.URL != "http://198.51.100.10:11434" {
		t.Errorf("подраздел без своего адреса не унаследовал сервер: %q", g.URL)
	}
}

// /graph-server в памяти переводит ровно те подразделы, которые переведёт
// запись [graph].server в файл: после перечитывания файл и окно совпадают.
func TestSetGraphServerMatchesFileAfterReload(t *testing.T) {
	body := safetyServers + `
[graph]
model  = "m:latest"
server = "a"

[graph.pinned]
server = "a"

[graph.own]
url = "http://198.51.100.20:11434"

[graph.inh]
format = 2
`
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetGraphServer("b"); err != nil {
		t.Fatal(err)
	}
	if err := SetString(p, KeyEdit{Section: "graph", Key: "server", Value: "b"}); err != nil {
		t.Fatal(err)
	}
	fresh, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "pinned", "own", "inh"} {
		mem, disk := cfg.GraphFor(name), fresh.GraphFor(name)
		if mem.URL != disk.URL || mem.Server != disk.Server {
			t.Errorf("граф %q: в окне %q (%q), в файле %q (%q)", name, mem.URL, mem.Server, disk.URL, disk.Server)
		}
	}
	if g := fresh.GraphFor("inh"); g.URL != "http://198.51.100.11:11434" {
		t.Errorf("наследующий подраздел не переведён: %q", g.URL)
	}
}

// Конфиг проекта со своими [[servers]] без сервера векторов общего
// конфига: замечание и прежний адрес векторов, а не отказ запуска.
func TestProjectWithoutEmbedServerStillApplies(t *testing.T) {
	cfg := writeCfg(t, safetyServers+"\n[kb]\nembed_model = \"bge-m3\"\nembed_server = \"b\"\n")
	proj := []byte("[general]\ndefault_server = \"c\"\n\n[[servers]]\nname = \"c\"\nurl = \"http://198.51.100.30:11434\"\nmodel = \"m:latest\"\n")
	if err := cfg.ApplyProject("/p/.ollchat/config.toml", proj); err != nil {
		t.Fatalf("конфиг проекта без сервера b не наложился: %v", err)
	}
	if cfg.KB.EmbedURL != "http://198.51.100.11:11434" {
		t.Errorf("адрес векторов %q, ждали прежний (сервер b общего конфига)", cfg.KB.EmbedURL)
	}
	if !strings.Contains(strings.Join(cfg.Warnings, "\n"), `kb.embed_server = "b"`) {
		t.Errorf("о неизвестном kb.embed_server не сказано: %q", cfg.Warnings)
	}
}

// Учётные данные в адресе не принимаются нигде, где адрес печатается:
// сервер, узлы, адрес сборки, адрес векторов. Сам пароль в отказ не попадает.
func TestCredentialsInAddressesRefused(t *testing.T) {
	cases := map[string]string{
		"servers.url":   strings.Replace(safetyServers, "http://198.51.100.10:11434", "http://user:s3cret@198.51.100.10:11434", 1),
		"graph.url":     safetyServers + "\n[graph]\nmodel = \"m\"\nurl = \"http://user:s3cret@198.51.100.10:11434\"\n",
		"graph.nodes":   safetyServers + "\n[graph]\nmodel = \"m\"\n[[graph.nodes]]\nname = \"n\"\nurl = \"http://user:s3cret@198.51.100.10:11434\"\n",
		"graph.lab":     safetyServers + "\n[graph]\nmodel = \"m\"\n[graph.lab]\nurl = \"http://user:s3cret@198.51.100.10:11434\"\n",
		"kb":            safetyServers + "\n[kb]\nembed_model = \"bge-m3\"\nembed_url = \"http://user:s3cret@198.51.100.10:11434\"\n",
		"servers.nodes": safetyServers + "\n[[servers.nodes]]\nname = \"n\"\nurl = \"http://user:s3cret@198.51.100.11:11434\"\n",
	}
	for where, body := range cases {
		err := loadErr(t, body)
		if err == nil {
			t.Errorf("%s: адрес с user:pass@ принят", where)
			continue
		}
		if strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%s: пароль в тексте отказа: %v", where, err)
		}
		if !strings.Contains(err.Error(), "user:pass@") {
			t.Errorf("%s: отказ не называет причину: %v", where, err)
		}
	}
}
