package main

import (
	"os"
	"path/filepath"
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

func TestSuiteValidateОтвергаетПлохое(t *testing.T) {
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
func TestSuiteValidateТребуетСуммуОдин(t *testing.T) {
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
func TestChecklistСоШтрафом(t *testing.T) {
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
