package main

import (
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

func TestSelectModels(t *testing.T) {
	tags := []ollama.ModelInfo{
		{Name: "qwen3.5:122b", Digest: "8b9d11d807c57feb1e2e", Size: 81370036360,
			Details:      ollama.ModelDetails{QuantizationLevel: "Q4_K_M", ParameterSize: "125.1B", ContextLength: 262144},
			Capabilities: []string{"vision", "completion", "tools", "thinking"}},
		{Name: "bge-m3:latest", Capabilities: []string{"embedding"}},
		{Name: "qwen38-szkm:latest", Capabilities: []string{"completion"}},
	}
	cards := SelectModels(tags, []string{"qwen38-szkm:latest"})
	byName := map[string]ModelCard{}
	for _, c := range cards {
		byName[c.Name] = c
	}
	if got := byName["qwen3.5:122b"]; got.Skipped != "" || got.Digest != "8b9d11d807c5" {
		t.Errorf("рабочая модель отброшена или digest не обрезан: %+v", got)
	}
	if byName["bge-m3:latest"].Skipped == "" {
		t.Error("эмбеддинги попали в чат-прогон")
	}
	if byName["qwen38-szkm:latest"].Skipped == "" {
		t.Error("исключённая модель попала в прогон")
	}
}

func TestMissingCapability(t *testing.T) {
	card := ModelCard{Capabilities: []string{"completion", "tools"}}
	if got := missingCapability(card, &Task{Needs: []string{"tools"}}); got != "" {
		t.Errorf("задача отклонена зря: %q", got)
	}
	if got := missingCapability(card, &Task{Needs: []string{"vision"}}); got != "vision" {
		t.Errorf("нехватка vision не замечена: %q", got)
	}
}

// Стенд живёт по UTC, а окно ночи задаётся по московскому времени: без явного
// пояса «до 06:45» означало бы 09:45 по Москве, то есть три часа чужого утра.
func TestParseDeadlineHonorsZone(t *testing.T) {
	msk, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Skip("нет базы часовых поясов")
	}
	got, err := parseDeadline("06:45", "Europe/Moscow", 0)
	if err != nil {
		t.Fatalf("parseDeadline: %v", err)
	}
	in := got.In(msk)
	if in.Hour() != 6 || in.Minute() != 45 {
		t.Errorf("предел = %s, ожидалось 06:45 по Москве", in.Format(time.RFC3339))
	}
	if !got.After(time.Now()) {
		t.Error("предел должен быть в будущем: заданный вечером, он относится к завтрашнему утру")
	}
}

// ParseDeadline пустой и длительность.
func TestParseDeadlineEmptyAndDuration(t *testing.T) {
	if got, err := parseDeadline("", "Europe/Moscow", 0); err != nil || !got.IsZero() {
		t.Errorf("без предела ожидался нулевой момент: %v %v", got, err)
	}
	got, err := parseDeadline("", "Europe/Moscow", 2*time.Hour)
	if err != nil || got.Sub(time.Now()) < 119*time.Minute {
		t.Errorf("--for 2h дал %v (%v)", got, err)
	}
	if _, err := parseDeadline("завтра", "Europe/Moscow", 0); err == nil {
		t.Error("мусор в --until принят")
	}
}

func TestSafeName(t *testing.T) {
	if got := SafeName("glm-4.7-flash:q8_0"); got != "glm-4.7-flash_q8_0" {
		t.Errorf("SafeName = %q", got)
	}
}
