package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChecklistСчитаетПопаданияИШтрафы(t *testing.T) {
	task := &Task{ID: "a", Level: 4, Verify: Verify{Kind: VerifyChecklist, Items: []CheckItem{
		{Name: "назвал num_ctx", Any: []string{"num_ctx"}, Score: 0.7},
		{Name: "назвал вытеснение", Any: []string{"вытесн", "оперативн"}, Score: 0.3},
		{Name: "выдумал переменную", None: []string{"OLLAMA_NUM_CTX"}, Score: -0.5},
	}}}
	v := &Verifier{}

	res := v.Verify(context.Background(), task, t.TempDir(),
		"Окно задаётся через num_ctx, иначе модель вытесняется в оперативную память.")
	if res.Score < 0.999 || res.Score > 1.001 {
		t.Errorf("балл = %.2f, ожидался 1.0", res.Score)
	}
	if !res.NeedsReview {
		t.Error("чек-лист обязан просить разбора человеком: регулярка не оценщик")
	}

	res = v.Verify(context.Background(), task, t.TempDir(),
		"Достаточно выставить OLLAMA_NUM_CTX=262144 в override.conf.")
	if res.Score != 0 {
		t.Errorf("балл за выдуманную переменную = %.2f, ожидался 0", res.Score)
	}
}

func TestКонтейнернаяПроверкаСчитаетШаги(t *testing.T) {
	dir := t.TempDir()
	task := &Task{
		ID: "a", Level: 1,
		Answer: AnswerSpec{File: "main.go", Lang: "go"},
		Verify: Verify{Kind: VerifyContainer, Image: "olleval/go", Steps: []Step{
			{Name: "сборка", Cmd: "go build ./...", Score: 0.3},
			{Name: "тесты", Cmd: "go test ./...", Score: 0.7},
		}},
	}
	var calls []string
	v := &Verifier{Docker: "docker", Memory: "2g", CPUs: "4",
		Runner: func(ctx context.Context, name string, args ...string) (string, int, error) {
			calls = append(calls, args[len(args)-1])
			return "ok", 0, nil
		}}

	res := v.Verify(context.Background(), task, dir, "```go\npackage main\n```")
	if res.Score < 0.999 {
		t.Errorf("балл = %.2f, ожидался 1.0", res.Score)
	}
	if len(calls) != 2 || !strings.Contains(calls[0], "go build") {
		t.Errorf("шаги выполнены не по порядку: %v", calls)
	}
	code, err := os.ReadFile(filepath.Join(dir, "work", "main.go"))
	if err != nil || !strings.Contains(string(code), "package main") {
		t.Errorf("код из ответа не разложен в рабочий каталог: %v %q", err, code)
	}
}

// Не собралось — тесты запускать бессмысленно: балл за них не начисляется,
// а шаг не выполняется вовсе.
func TestКонтейнернаяПроверкаОстанавливаетсяНаПервомПровале(t *testing.T) {
	task := &Task{ID: "a", Level: 1,
		Answer: AnswerSpec{File: "main.go", Lang: "go"},
		Verify: Verify{Kind: VerifyContainer, Image: "olleval/go", Steps: []Step{
			{Name: "сборка", Cmd: "go build ./...", Score: 0.3},
			{Name: "тесты", Cmd: "go test ./...", Score: 0.7},
		}}}
	var calls int
	v := &Verifier{Docker: "docker",
		Runner: func(ctx context.Context, name string, args ...string) (string, int, error) {
			calls++
			return "ошибка компиляции", 1, nil
		}}
	res := v.Verify(context.Background(), task, t.TempDir(), "```go\nне код\n```")
	if calls != 1 {
		t.Errorf("выполнено шагов: %d, ожидался один", calls)
	}
	if res.Score != 0 {
		t.Errorf("балл = %.2f, ожидался 0", res.Score)
	}
}

// Пустой ответ без кода — провал задачи, но не сбой прогона.
func TestКонтейнернаяПроверкаБезКода(t *testing.T) {
	task := &Task{ID: "a", Level: 1, Answer: AnswerSpec{File: "main.go", Lang: "go"},
		Verify: Verify{Kind: VerifyContainer, Image: "olleval/go",
			Steps: []Step{{Name: "сборка", Cmd: "true", Score: 1}}}}
	v := &Verifier{Docker: "docker", Runner: func(context.Context, string, ...string) (string, int, error) {
		t.Fatal("проверка не должна запускаться без кода")
		return "", 0, nil
	}}
	res := v.Verify(context.Background(), task, t.TempDir(), "Я подумаю об этом завтра.")
	if res.Score != 0 || !strings.Contains(res.Verdict, "нет блока кода") {
		t.Errorf("вердикт %q, балл %.2f", res.Verdict, res.Score)
	}
}

// Сорванный docker — наша поломка, а не глупость модели: попытка помечается
// «нужен разбор», иначе сломанная проверка выглядела бы как ноль за задачу.
func TestСбойDockerПометитсяКакНашаБеда(t *testing.T) {
	task := &Task{ID: "a", Level: 1, Answer: AnswerSpec{File: "main.go", Lang: "go"},
		Verify: Verify{Kind: VerifyContainer, Image: "нет-такого", Steps: []Step{{Name: "сборка", Cmd: "true", Score: 1}}}}
	v := &Verifier{Docker: "docker", Runner: func(context.Context, string, ...string) (string, int, error) {
		return "", 0, os.ErrNotExist
	}}
	res := v.Verify(context.Background(), task, t.TempDir(), "```go\npackage main\n```")
	if !res.NeedsReview || res.Score != 0 {
		t.Errorf("сбой проверки не помечен: %+v", res)
	}
}
