package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// Писатель одного ключа (этап 123): меняется только значение, комментарии
// и порядок остаются байт в байт — это критерий этапа «diff показывает только
// изменённые значения ключей».

// Образец с ловушками разметки: тот же ключ url в трёх разделах, строка с
// «[» в начале внутри массива, заголовок внутри многострочной строки,
// продолжение комментария с отступом.
const setkeyCfg = `# конфиг владельца
[general]
default_server = "lab"   # какой сервер при старте

[[servers]]
name  = "lab"
url   = "http://ollama.example:11434"
model = "m"

[confluence]
url = "http://wiki.example"

[kb]
embed_model = "bge-m3"   # смысловой поиск
embed_url   = "http://ollama.example:11434"   # ВРЕМЕННО
allow = [
  "a",
[1, 2],
]
note = """
[fake]
server = "x"
"""

[graph]
model = "m"
url   = "http://ollama.example:11434"   # адрес сборки
                                       # продолжение пояснения
workers = 4

[[graph.nodes]]
name = "n1"
url  = "http://ollama.example:11434"
`

func mustSet(t *testing.T, data string, e KeyEdit) string {
	t.Helper()
	out, err := setStringIn([]byte(data), e)
	if err != nil {
		t.Fatalf("%s.%s: %v", e.Section, e.Key, err)
	}
	return string(out)
}

// Значение заменяется в своём разделе, хвостовой комментарий остаётся.
func TestSetKeyReplacesValueOnly(t *testing.T) {
	got := mustSet(t, setkeyCfg, KeyEdit{Section: "kb", Key: "embed_url", Value: "http://x.example:1"})
	want := strings.Replace(setkeyCfg,
		`embed_url   = "http://ollama.example:11434"   # ВРЕМЕННО`,
		`embed_url   = "http://x.example:1"   # ВРЕМЕННО`, 1)
	if got != want {
		t.Errorf("правка задела лишнее:\n%s", got)
	}

	// url есть в [[servers]], [confluence], [graph] и [[graph.nodes]] — меняется только в [graph].
	got = mustSet(t, setkeyCfg, KeyEdit{Section: "graph", Key: "url", Value: "http://tuf.example:11434"})
	want = strings.Replace(setkeyCfg,
		`url   = "http://ollama.example:11434"   # адрес сборки`,
		`url   = "http://tuf.example:11434"   # адрес сборки`, 1)
	if got != want {
		t.Errorf("url ушёл не в свой раздел:\n%s", got)
	}
}

// Нового ключа нет — строка встаёт после якоря и его продолжения
// комментария, а не разрезает пояснение надвое.
func TestSetKeyInsertsAfterAnchor(t *testing.T) {
	got := mustSet(t, setkeyCfg, KeyEdit{Section: "graph", Key: "server", Value: "tuf",
		After: []string{"nope", "url"}, Comment: "выбран из TUI"})
	want := strings.Replace(setkeyCfg,
		"# продолжение пояснения\nworkers = 4\n",
		"# продолжение пояснения\nserver = \"tuf\"   # выбран из TUI\nworkers = 4\n", 1)
	if got != want {
		t.Errorf("строка встала не туда:\n%s", got)
	}
}

// Без якоря — после последнего ключа раздела, даже если он многострочный.
func TestSetKeyInsertsAtSectionEnd(t *testing.T) {
	got := mustSet(t, setkeyCfg, KeyEdit{Section: "kb", Key: "embed_server", Value: "tuf"})
	if !strings.Contains(got, "server = \"x\"\n\"\"\"\nembed_server = \"tuf\"\n\n[graph]\n") {
		t.Errorf("строка не в конце раздела [kb]:\n%s", got)
	}
}

// Заголовок внутри многострочной строки — не раздел; нет раздела — он
// дописывается в конец файла.
func TestSetKeyNewSection(t *testing.T) {
	got := mustSet(t, setkeyCfg, KeyEdit{Section: "fake", Key: "server", Value: "y"})
	if !strings.HasPrefix(got, setkeyCfg) || !strings.HasSuffix(got, "\n\n[fake]\nserver = \"y\"\n") {
		t.Errorf("раздел внутри строки принят за настоящий:\n%s", got)
	}
	got = mustSet(t, setkeyCfg, KeyEdit{Section: "graph.lab", Key: "server", Value: "tuf"})
	if !strings.HasSuffix(got, "[graph.lab]\nserver = \"tuf\"\n") {
		t.Errorf("подраздел графа не дописан:\n%s", got)
	}
}

// Отказы: многострочное значение, массив таблиц под видом раздела.
func TestSetKeyRefuses(t *testing.T) {
	if _, err := setStringIn([]byte(setkeyCfg), KeyEdit{Section: "kb", Key: "allow", Value: "x"}); err == nil ||
		!strings.Contains(err.Error(), "несколько строк") {
		t.Errorf("многострочное значение заменено: %v", err)
	}
	if _, err := setStringIn([]byte(setkeyCfg), KeyEdit{Section: "graph.nodes", Key: "probe", Value: "x"}); err == nil {
		t.Error("[[graph.nodes]] принят за обычный раздел")
	}
	if _, err := setStringIn([]byte("a = [\n"), KeyEdit{Section: "kb", Key: "x", Value: "y"}); err == nil {
		t.Error("незакрытый массив разобран")
	}
}

// Перевод строки файла сохраняется, кавычки и обратная косая в значении
// экранируются, то же значение — файл не меняется.
func TestSetKeyFormatting(t *testing.T) {
	crlf := strings.ReplaceAll("[graph]\nmodel = \"m\"\n", "\n", "\r\n")
	got := mustSet(t, crlf, KeyEdit{Section: "graph", Key: "server", Value: "tuf"})
	if got != "[graph]\r\nmodel = \"m\"\r\nserver = \"tuf\"\r\n" {
		t.Errorf("перевод строки CRLF потерян: %q", got)
	}

	odd := `a"b\c`
	got = mustSet(t, setkeyCfg, KeyEdit{Section: "graph", Key: "server", Value: odd})
	var m map[string]any
	if _, err := toml.Decode(got, &m); err != nil {
		t.Fatal(err)
	}
	if v := m["graph"].(map[string]any)["server"]; v != odd {
		t.Errorf("значение после разбора %q, ждали %q", v, odd)
	}

	got = mustSet(t, setkeyCfg, KeyEdit{Section: "kb", Key: "embed_url", Value: "http://ollama.example:11434"})
	if got != setkeyCfg {
		t.Error("то же значение изменило файл")
	}
}

// Настоящий шаблон с его комментариями: после записи отличается ровно одна
// строка — значение server в [graph], с её хвостовым комментарием, — файл
// проходит Load, а выбор виден в разобранных настройках.
func TestSetStringOnTemplate(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte(Template), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	name := cfg.Servers[0].Name
	if err := SetString(p, KeyEdit{Section: "graph", Key: "server", Value: name, After: []string{"url"}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	oldLines := strings.Split(Template, "\n")
	newLines := strings.Split(string(data), "\n")
	if len(newLines) != len(oldLines) {
		t.Fatalf("строк было %d, стало %d — ждали замену значения, а не новую строку", len(oldLines), len(newLines))
	}
	var changed []int
	for i := range oldLines {
		if oldLines[i] != newLines[i] {
			changed = append(changed, i)
		}
	}
	if len(changed) != 1 {
		t.Fatalf("изменилось строк: %d, ждали одну", len(changed))
	}
	i := changed[0]
	if want := strings.Replace(oldLines[i], `server      = ""`, `server      = "`+name+`"`, 1); newLines[i] != want ||
		!strings.Contains(newLines[i], "# имя сервера") {
		t.Errorf("строка %q, ждали %q — с прежним комментарием", newLines[i], want)
	}
	cfg, _, err = Load(p)
	if err != nil || cfg.Graph.Server != name {
		t.Errorf("выбор не прочитался: server %q, ошибка %v", cfg.Graph.Server, err)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Errorf("права файла %v, были 0600", st.Mode().Perm())
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Errorf("рядом с конфигом остались файлы: %d", len(ents))
	}
}

// Правка, после которой конфиг не проходит проверку запуска, не делается;
// ссылка на файл остаётся ссылкой.
func TestSetStringRefusesAndKeepsSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.toml")
	link := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(real, []byte(baseCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	err := SetString(link, KeyEdit{Section: "graph", Key: "server", Value: "nope"})
	if err == nil || !strings.Contains(err.Error(), "не проходит проверку") || strings.Contains(err.Error(), ".set-") {
		t.Errorf("ошибка %v, ждали отказ проверки с путём конфига, а не копии", err)
	}
	if data, _ := os.ReadFile(real); string(data) != baseCfg {
		t.Error("файл изменён несмотря на отказ")
	}

	if err := SetString(link, KeyEdit{Section: "graph", Key: "server", Value: "lab"}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Error("ссылка заменена обычным файлом")
	}
	if data, _ := os.ReadFile(real); !strings.Contains(string(data), `server = "lab"`) {
		t.Error("цель ссылки не изменена")
	}
	if st, _ := os.Stat(real); st.Mode().Perm() != 0o644 {
		t.Errorf("права цели %v, были 0644", st.Mode().Perm())
	}
}
