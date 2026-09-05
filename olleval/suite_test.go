package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSuite(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "проба.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadSuite(t *testing.T) {
	path := writeSuite(t, `
name = "go"
description = "Программирование на Go"

[[task]]
id = "go-u1-strftime"
level = 1
prompt = "Напиши разбор шаблона strftime."
[task.answer]
file = "logpattern.go"
lang = "go"
[task.verify]
kind = "container"
image = "olleval/go"
timeout = "5m"
[[task.verify.step]]
name = "сборка"
cmd = "go build ./..."
score = 0.3
[[task.verify.step]]
name = "тесты"
cmd = "go test ./..."
score = 0.7
`)
	s, err := LoadSuite(path)
	if err != nil {
		t.Fatalf("LoadSuite: %v", err)
	}
	if s.Name != "go" || len(s.Tasks) != 1 {
		t.Fatalf("прочитано не то: %+v", s)
	}
	task := s.Tasks[0]
	if task.Answer.File != "logpattern.go" || task.Answer.Lang != "go" {
		t.Errorf("описание ответа: %+v", task.Answer)
	}
	if got := task.Verify.Timeout.Get(0); got.Minutes() != 5 {
		t.Errorf("timeout = %v, ожидалось 5m", got)
	}
	if got := task.Weight(); got != 1 {
		t.Errorf("вес уровня 1 = %v, ожидался 1", got)
	}
}

// SuiteValidate отвергает плохое.
func TestSuiteValidateRejectsBad(t *testing.T) {
	cases := map[string]string{
		"пустой набор":         `name = "x"`,
		"нет идентификатора":   "[[task]]\nlevel = 1\nprompt = \"а\"\n",
		"уровень вне 1..4":     "[[task]]\nid = \"a\"\nlevel = 9\nprompt = \"а\"\n",
		"пустая постановка":    "[[task]]\nid = \"a\"\nlevel = 1\nprompt = \"  \"\n",
		"контейнер без образа": "[[task]]\nid = \"a\"\nlevel = 1\nprompt = \"а\"\n[task.verify]\nkind = \"container\"\n",
		"неизвестная проверка": "[[task]]\nid = \"a\"\nlevel = 1\nprompt = \"а\"\n[task.verify]\nkind = \"магия\"\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadSuite(writeSuite(t, body)); err == nil {
				t.Fatalf("набор %q принят, а должен быть отвергнут", name)
			}
		})
	}
}

// Сумма баллов должна давать единицу: иначе задача не наберёт максимум
// никогда, и область молча просядет — самая обидная разновидность ошибки.
func TestSuiteValidateRequiresWeightSumOne(t *testing.T) {
	body := `
[[task]]
id = "a"
level = 1
prompt = "а"
[task.verify]
kind = "container"
image = "olleval/go"
[[task.verify.step]]
name = "сборка"
cmd = "go build ./..."
score = 0.3
[[task.verify.step]]
name = "тесты"
cmd = "go test ./..."
score = 0.3
`
	if _, err := LoadSuite(writeSuite(t, body)); err == nil {
		t.Fatal("набор с суммой 0.6 принят")
	}
}

// Штрафной пункт (отрицательный балл) в сумму не входит: он снимает
// за выдуманное сверх набранного, а не участвует в максимуме.
func TestChecklistWithPenalty(t *testing.T) {
	body := `
[[task]]
id = "a"
level = 4
prompt = "а"
[task.verify]
kind = "checklist"
[[task.verify.item]]
name = "назвал причину"
any = ["num_ctx"]
score = 1.0
[[task.verify.item]]
name = "выдумал переменную"
none = ["OLLAMA_NUM_CTX"]
score = -0.5
`
	if _, err := LoadSuite(writeSuite(t, body)); err != nil {
		t.Fatalf("набор со штрафным пунктом отвергнут: %v", err)
	}
}

// Битое выражение в чек-листе — ошибка запуска, а не вечный ноль у пункта.
func TestChecklistRejectsBrokenRegexp(t *testing.T) {
	dir := t.TempDir()
	file := `name = "проба"
[[task]]
id = "проба-1"
level = 4
prompt = "вопрос"
[task.verify]
kind = "checklist"
[[task.verify.item]]
name = "пункт"
any = ["re:(незакрытая скобка"]
score = 1.0
`
	if err := os.WriteFile(filepath.Join(dir, "проба.toml"), []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadSuites(dir)
	if err == nil {
		t.Fatal("битое выражение принято")
	}
	if !strings.Contains(err.Error(), "не компилируется") {
		t.Errorf("ошибка не про выражение: %v", err)
	}
}

// Очередь наборов задаётся полем order, а не алфавитом: владелец стенда решает,
// что мерить раньше.
func TestSuiteOrderByOrderField(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"ai.toml":     "name = \"ai\"\norder = 10\n",
		"python.toml": "name = \"python\"\n",
		"agent.toml":  "name = \"agent\"\n",
		"vue.toml":    "name = \"vue\"\norder = 10\n",
	}
	task := "\n[[task]]\nid = \"%s-1\"\nlevel = 1\nprompt = \"вопрос\"\n"
	for name, body := range files {
		short := strings.TrimSuffix(name, ".toml")
		body := body + fmt.Sprintf(task, short)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	suites, err := LoadSuites(dir)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, s := range suites {
		order = append(order, s.Name)
	}
	want := []string{"agent", "python", "ai", "vue"}
	if len(order) != len(want) {
		t.Fatalf("наборов %d: %v", len(order), order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("порядок %v, ожидался %v", order, want)
		}
	}
}
