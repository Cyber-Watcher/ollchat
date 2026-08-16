package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

func modelList(names ...string) []ollama.ModelInfo {
	out := make([]ollama.ModelInfo, 0, len(names))
	for _, n := range names {
		out = append(out, ollama.ModelInfo{
			Name:         n,
			Details:      ollama.ModelDetails{ParameterSize: "8B", ContextLength: 32768},
			Capabilities: []string{"completion", "tools"},
		})
	}
	return out
}

func pickerValues(p *picker) []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p.items))
	for _, it := range p.items {
		out = append(out, it.value)
	}
	return out
}

// TestModelsCommandRefreshesInsteadOfUsingCache — суть исправления: список
// моделей запрашивается в момент вызова, а не берётся из прочитанного при
// запуске. Иначе удалённые на сервере модели продолжают показываться.
func TestModelsCommandRefreshesInsteadOfUsingCache(t *testing.T) {
	m := newTestModel(t)
	m.models = modelList("удалённая:latest", "оставшаяся:latest")

	cmd := m.runCommand("/models")
	if cmd == nil {
		t.Fatal("команда должна запрашивать список у сервера")
	}
	if m.picker != nil {
		t.Fatal("список выбора не должен открываться до ответа сервера")
	}
	if !strings.Contains(m.statusMsg, "обновляю") {
		t.Errorf("пользователю стоит показать, что идёт обновление: %q", m.statusMsg)
	}

	// Сервер отвечает списком, в котором одной модели уже нет.
	m.Update(modelsMsg{models: modelList("оставшаяся:latest"), action: modelsOpenPicker})

	if m.picker == nil {
		t.Fatal("после ответа сервера список выбора должен открыться")
	}
	got := pickerValues(m.picker)
	if len(got) != 1 || got[0] != "оставшаяся:latest" {
		t.Errorf("в списке выбора оказалось %v — удалённая модель не должна показываться", got)
	}
}

// Ctrl+R — та же дорога, что и /models.
func TestCtrlRRefreshesModels(t *testing.T) {
	m := newTestModel(t)
	m.models = modelList("старая:latest")

	_, cmd := m.Update(pressCtrl('r'))
	if cmd == nil {
		t.Fatal("Ctrl+R должен запрашивать свежий список")
	}
	if m.picker != nil {
		t.Error("список выбора не должен открываться из кеша")
	}
}

// TestDeletedCurrentModelIsReported — если выбранная модель исчезла с сервера,
// пользователь должен узнать об этом сразу, а не по ошибке в ответ на вопрос.
func TestDeletedCurrentModelIsReported(t *testing.T) {
	m := newTestModel(t)
	m.modelName = "исчезнувшая:latest"
	m.models = modelList("исчезнувшая:latest", "живая:latest")

	m.Update(modelsMsg{models: modelList("живая:latest"), action: modelsOpenPicker})

	var reported bool
	for _, b := range m.blocks {
		if b.kind == blockError && strings.Contains(b.text, "исчезнувшая:latest") {
			reported = true
		}
	}
	if !reported {
		t.Error("исчезновение выбранной модели должно попадать в ленту как ошибка")
	}
}

// TestModelSelectChecksFreshList — переключение по имени сверяется со свежим
// списком: и на удалённые модели, и на появившиеся уже после запуска.
func TestModelSelectChecksFreshList(t *testing.T) {
	t.Run("удалённую выбрать нельзя", func(t *testing.T) {
		m := newTestModel(t)
		m.modelName = "живая:latest"
		m.models = modelList("живая:latest", "удалённая:latest")

		m.Update(modelsMsg{models: modelList("живая:latest"),
			action: modelsSelect, target: "удалённая:latest"})

		if m.modelName != "живая:latest" {
			t.Errorf("модель не должна была смениться, стало %q", m.modelName)
		}
		var errored bool
		for _, b := range m.blocks {
			if b.kind == blockError && strings.Contains(b.text, "не найдена") {
				errored = true
			}
		}
		if !errored {
			t.Error("нужно сообщить, что модели нет на сервере")
		}
	})

	t.Run("появившуюся после запуска выбрать можно", func(t *testing.T) {
		m := newTestModel(t)
		m.modelName = "старая:latest"
		m.models = modelList("старая:latest") // на старте новой модели ещё не было

		m.Update(modelsMsg{models: modelList("старая:latest", "новая:latest"),
			action: modelsSelect, target: "новая:latest"})

		if m.modelName != "новая:latest" {
			t.Errorf("переключение не произошло, модель = %q", m.modelName)
		}
	})
}

// При недоступном сервере проверить список нечем, но явную просьбу пользователя
// переключиться выполняем — он знает лучше.
func TestModelSelectSurvivesRefreshFailure(t *testing.T) {
	m := newTestModel(t)
	m.modelName = "текущая:latest"
	m.models = modelList("текущая:latest")

	m.Update(modelsMsg{err: errors.New("connection refused"),
		action: modelsSelect, target: "другая:latest"})

	if m.modelName != "другая:latest" {
		t.Errorf("при недоступном сервере переключение по явной просьбе должно проходить, модель = %q", m.modelName)
	}
	var mentioned bool
	for _, b := range m.blocks {
		if strings.Contains(b.text, "connection refused") {
			mentioned = true
		}
	}
	if !mentioned {
		t.Error("ошибку обновления списка нужно показать")
	}
}

func TestModelsDiffNote(t *testing.T) {
	cases := []struct {
		name          string
		before, after []ollama.ModelInfo
		wantContains  []string
		wantEmptyNote bool
	}{
		{
			name:          "первое получение списка молчит",
			before:        nil,
			after:         modelList("a", "b"),
			wantEmptyNote: true,
		},
		{
			name:          "без изменений молчит",
			before:        modelList("a", "b"),
			after:         modelList("a", "b"),
			wantEmptyNote: true,
		},
		{
			name:         "удаление",
			before:       modelList("a", "b"),
			after:        modelList("a"),
			wantContains: []string{"удалены", "b"},
		},
		{
			name:         "добавление",
			before:       modelList("a"),
			after:        modelList("a", "c"),
			wantContains: []string{"появились", "c"},
		},
		{
			name:         "и то и другое",
			before:       modelList("a", "b"),
			after:        modelList("a", "c"),
			wantContains: []string{"появились", "c", "удалены", "b"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := modelsDiffNote(c.before, c.after)
			if c.wantEmptyNote {
				if got != "" {
					t.Errorf("ожидалось молчание, получено %q", got)
				}
				return
			}
			for _, want := range c.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("в сообщении %q нет %q", got, want)
				}
			}
		})
	}
}
