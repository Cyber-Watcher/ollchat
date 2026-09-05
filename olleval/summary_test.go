package main

import (
	"os"
	"path/filepath"
	"testing"
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
