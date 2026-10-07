package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Summarize средний балл и медианное время.
func TestSummarizeMeanScoreAndMedianTime(t *testing.T) {
	recs := []Metrics{
		{Model: "m", Suite: "go", Score: 1, WallSeconds: 10, TokensPerSecond: 70},
		{Model: "m", Suite: "go", Score: 0, WallSeconds: 20, TokensPerSecond: 70, Error: "обрыв"},
		{Model: "m", Suite: "go", Score: 0.5, WallSeconds: 600, TokensPerSecond: 70, NeedsReview: true},
	}
	sum := Summarize(recs)["m|go"]
	if sum.Attempts != 3 || sum.Errors != 1 || sum.Review != 1 {
		t.Errorf("счётчики: %+v", sum)
	}
	if sum.MeanScore < 0.49 || sum.MeanScore > 0.51 {
		t.Errorf("средний балл = %.3f, ожидалось 0.5", sum.MeanScore)
	}
	// Медиана, а не среднее: одна задача, упёршаяся в таймаут, иначе сдвинула бы
	// цифру так, что она перестала бы описывать обычный ответ.
	if sum.MedianSeconds != 20 {
		t.Errorf("медиана времени = %.0f, ожидалось 20", sum.MedianSeconds)
	}
}

// Балл области взвешен уровнями задач, как обещает README: задача «с нуля»
// (У3) стоит трёх мелочей (У1). Простое среднее давало здесь 0.5.
func TestSummaryWeightsLevels(t *testing.T) {
	recs := []Metrics{
		{Model: "m", Suite: "go", Level: LevelSmall, Score: 1},
		{Model: "m", Suite: "go", Level: LevelScratch, Score: 0},
	}
	got := Summarize(recs)["m|go"].MeanScore
	if got < 0.249 || got > 0.251 {
		t.Errorf("балл области %.3f, ожидалось 0.25 (1·1 + 3·0) / 4", got)
	}
}

// ReadIndex пропускает битую строку.
func TestReadIndexSkipsBrokenLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.jsonl")
	body := `{"model":"m","suite":"go","score":1}
это не json
{"model":"m","suite":"go","score":0}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	recs, err := ReadIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Errorf("прочитано записей: %d, ожидалось 2", len(recs))
	}
}

// Заход в начатую ночь не переписывает паспорт: время начала, пометка
// и карточки первого захода остаются, а новый заход ложится в resumes —
// с моделями, которых не было или чьи веса сменились под тем же тегом.
func TestPassportKeptOnResume(t *testing.T) {
	t0 := time.Date(2026, 8, 22, 0, 5, 0, 0, time.UTC)
	s, err := NewStore(t.TempDir(), "2026-08-22")
	if err != nil {
		t.Fatal(err)
	}
	first := &Passport{Night: s.Night, StartedAt: t0, OllamaVersion: "0.32.13", Note: "первый прогон",
		Models: []ModelCard{{Name: "a", Digest: "d1"}, {Name: "b", Digest: "d2", Skipped: "не в списке --models"}}}
	if err := s.SavePassport(resumePassport(nil, first)); err != nil {
		t.Fatal(err)
	}
	prev, err := s.LoadPassport()
	if err != nil {
		t.Fatal(err)
	}
	second := &Passport{Night: s.Night, StartedAt: t0.Add(6 * time.Hour), OllamaVersion: "0.33.0",
		Models: []ModelCard{{Name: "a", Digest: "d9"}, {Name: "b", Digest: "d2"}, {Name: "c", Digest: "d3"}}}
	if err := s.SavePassport(resumePassport(prev, second)); err != nil {
		t.Fatal(err)
	}

	got, err := s.LoadPassport()
	if err != nil {
		t.Fatal(err)
	}
	if !got.StartedAt.Equal(t0) || got.Note != "первый прогон" || got.OllamaVersion != "0.32.13" {
		t.Errorf("паспорт первого захода переписан: %+v", got)
	}
	if len(got.Models) != 3 || got.Models[0].Digest != "d1" {
		t.Errorf("карточки моделей: %+v", got.Models)
	}
	if len(got.Resumes) != 1 || got.Resumes[0].OllamaVersion != "0.33.0" {
		t.Fatalf("заход не записан: %+v", got.Resumes)
	}
	var changed []string
	for _, c := range got.Resumes[0].Changed {
		changed = append(changed, c.Name+"@"+c.Digest)
	}
	if strings.Join(changed, " ") != "a@d9 b@d2 c@d3" {
		t.Errorf("изменения захода: %v", changed)
	}
}

// Store докатывает попытки.
func TestStoreAppendsAttempts(t *testing.T) {
	s, err := NewStore(t.TempDir(), "2026-08-22")
	if err != nil {
		t.Fatal(err)
	}
	if s.Done("qwen3.5:122b", "go-u1", 1) {
		t.Fatal("попытка считается сделанной до прогона")
	}
	dir := s.AttemptDir("qwen3.5:122b", "go-u1", 1)
	if err := WriteJSON(dir, "metrics.json", Metrics{Task: "go-u1"}); err != nil {
		t.Fatal(err)
	}
	if !s.Done("qwen3.5:122b", "go-u1", 1) {
		t.Error("метрики записаны, а попытка не считается сделанной — ночь пойдёт по второму кругу")
	}
}

// Предел времени — не сбой стенда, а результат про модель: она не уложилась
// или ушла в бесконечные рассуждения. Считается отдельно.
func TestSummaryCountsTimeApartFromFailures(t *testing.T) {
	recs := []Metrics{
		{Model: "м", Suite: "go", Score: 1, WallSeconds: 10, TokensPerSecond: 50},
		{Model: "м", Suite: "go", Score: 0, WallSeconds: 600, TimedOut: true,
			Error: "предел времени 10m0s: ответа не было, рассуждений 41902 знаков"},
		{Model: "м", Suite: "go", Score: 0, WallSeconds: 5, Error: "сеть отвалилась"},
	}
	s := Summarize(recs)["м|go"]
	if s.TimedOut != 1 {
		t.Errorf("по времени = %d, ожидалась одна", s.TimedOut)
	}
	if s.Errors != 1 {
		t.Errorf("сбоев = %d, ожидался один", s.Errors)
	}
	if s.Attempts != 3 {
		t.Errorf("попыток = %d", s.Attempts)
	}
}
