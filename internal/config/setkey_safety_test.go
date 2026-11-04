package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/BurntSushi/toml"
)

// Запись ключа после аудита этапа 123: два писателя разом, NaN и BOM в
// файле, файл только для чтения и жёсткая ссылка.

const setkeySafetyCfg = safetyServers + "\n[kb]\nembed_model = \"bge-m3\"\n\n[graph]\nmodel = \"m:latest\"\n"

// Два писателя разом (TUI /graph-server и --use-embed-server из обвязки):
// обе правки в файле. Без замка вторая перезаписывала первую — аудит видел
// потерю в 23, 4 и 6 случаях из 300.
func TestSetStringConcurrentWritersKeepBoth(t *testing.T) {
	lost, refused := 0, 0
	for i := 0; i < 300; i++ {
		p := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(p, []byte(setkeySafetyCfg), 0o600); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make([]error, 2)
		start := make(chan struct{})
		edits := []KeyEdit{
			{Section: "graph", Key: "server", Value: "b"},
			{Section: "kb", Key: "embed_server", Value: "b"},
		}
		for j, e := range edits {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[j] = SetString(p, e)
			}()
		}
		close(start)
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			refused++
			continue
		}
		got, _ := os.ReadFile(p)
		var m map[string]any
		if _, err := toml.Decode(string(got), &m); err != nil {
			t.Fatalf("файл не разбирается: %v", err)
		}
		g, _ := m["graph"].(map[string]any)
		k, _ := m["kb"].(map[string]any)
		if g["server"] != "b" || k["embed_server"] != "b" {
			lost++
		}
	}
	if lost > 0 || refused > 0 {
		t.Errorf("из 300 пар правок потеряна одна в %d, отказ в %d", lost, refused)
	}
}

// NaN в чужом разделе не запрещает правку: NaN равен себе при сверке
// «до и после» (reflect.DeepEqual считал иначе, и любая правка отказывала).
func TestSetStringNaNElsewhere(t *testing.T) {
	src := "[graph]\nmodel = \"m\"\n\n[other]\nratio = nan\nlist = [nan, 1.0]\n"
	out, err := setStringIn([]byte(src), KeyEdit{Section: "graph", Key: "server", Value: "a"})
	if err != nil {
		t.Fatalf("правка отвергнута из-за nan в чужом разделе: %v", err)
	}
	if !strings.Contains(string(out), "server = \"a\"") || !strings.Contains(string(out), "ratio = nan") {
		t.Errorf("правка не та:\n%s", out)
	}
}

// BOM в начале файла: программа его читает — значит, и правка в него пишется,
// BOM остаётся на месте.
func TestSetStringBOM(t *testing.T) {
	src := "\xEF\xBB\xBF[graph]\nmodel = \"m\"\n"
	out, err := setStringIn([]byte(src), KeyEdit{Section: "graph", Key: "server", Value: "a", After: []string{"model"}})
	if err != nil {
		t.Fatalf("файл с BOM: %v", err)
	}
	if !strings.HasPrefix(string(out), "\xEF\xBB\xBF[graph]\nmodel = \"m\"\nserver = \"a\"\n") {
		t.Errorf("правка не та:\n%q", out)
	}
}

// Файл только для чтения и файл с жёсткой ссылкой не заменяются молча:
// первый закрыт от записи нарочно, у второго замена переименованием
// разорвала бы связь — по второму имени остался бы прежний конфиг.
func TestSetStringRefusesReadOnlyAndHardLinked(t *testing.T) {
	dir := t.TempDir()
	ro := filepath.Join(dir, "ro.toml")
	if err := os.WriteFile(ro, []byte(setkeySafetyCfg), 0o444); err != nil {
		t.Fatal(err)
	}
	err := SetString(ro, KeyEdit{Section: "graph", Key: "server", Value: "b"})
	if err == nil || !strings.Contains(err.Error(), "только для чтения") {
		t.Errorf("файл 0444: ошибка %v, ждали отказ «только для чтения»", err)
	}
	if data, _ := os.ReadFile(ro); string(data) != setkeySafetyCfg {
		t.Error("файл 0444 переписан")
	}

	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte(setkeySafetyCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(p, filepath.Join(dir, "hard.toml")); err != nil {
		t.Skip(err)
	}
	err = SetString(p, KeyEdit{Section: "graph", Key: "server", Value: "b"})
	if err == nil || !strings.Contains(err.Error(), "жёстк") {
		t.Errorf("жёсткая ссылка: ошибка %v, ждали отказ о жёсткой ссылке", err)
	}
	if data, _ := os.ReadFile(p); string(data) != setkeySafetyCfg {
		t.Error("файл с жёсткой ссылкой переписан")
	}
}
