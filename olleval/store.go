package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/fsx"
)

// Store — раскладка результатов прогонов на диске.
//
//	<root>/runs/<ночь>/run.json      паспорт ночи
//	<root>/runs/<ночь>/index.jsonl   строка на попытку
//	<root>/runs/<ночь>/<модель>/<задача>/r<N>/…
//
// Готовность попытки определяется по файлу metrics.json в её каталоге, а не по
// отдельному списку: файловая система и есть состояние. После обрыва прогон
// докатывается сам, ничего не пересчитывая заново.
type Store struct {
	Root  string // корень (обычно ~/ollevals)
	Night string // имя ночи: 2026-08-22 или 2026-08-22-b
}

// NewStore готовит раскладку и создаёт каталог ночи.
func NewStore(root, night string) (*Store, error) {
	s := &Store{Root: root, Night: night}
	if err := os.MkdirAll(s.NightDir(), 0o755); err != nil {
		return nil, err
	}
	return s, nil
}

// NightDir — каталог ночи.
func (s *Store) NightDir() string { return filepath.Join(s.Root, "runs", s.Night) }

// SuitesDir и FixturesDir — где лежат задачи и их входные файлы.
func (s *Store) SuitesDir() string   { return filepath.Join(s.Root, "suites") }
func (s *Store) FixturesDir() string { return filepath.Join(s.Root, "fixtures") }
func (s *Store) StateDir() string    { return filepath.Join(s.Root, "state") }

// AttemptDir — каталог одной попытки.
func (s *Store) AttemptDir(model, task string, repeat int) string {
	return filepath.Join(s.NightDir(), SafeName(model), task, fmt.Sprintf("r%d", repeat))
}

// Done сообщает, что попытка уже прогнана: рядом лежат метрики.
func (s *Store) Done(model, task string, repeat int) bool {
	_, err := os.Stat(filepath.Join(s.AttemptDir(model, task, repeat), "metrics.json"))
	return err == nil
}

// SafeName приводит имя модели к виду, годному для каталога: двоеточие и слеш
// в пути живут плохо, хотя формально и допустимы.
func SafeName(model string) string {
	r := strings.NewReplacer(":", "_", "/", "_", " ", "_")
	return r.Replace(model)
}

// WriteJSON пишет значение в файл каталога попытки.
//
// Запись атомарная: по одному лишь наличию metrics.json попытка считается
// сделанной (Done), и файл, оборванный посреди записи (kill, пропало
// питание), навсегда засчитывал бы попытку с битыми метриками — заново её
// ночь уже не прогнала бы. Паспорт ночи так же переписывается целиком.
func WriteJSON(dir, name string, v any) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(filepath.Join(dir, name), append(b, '\n'), 0o644)
}

// WriteText пишет текст в файл каталога попытки — атомарно, как WriteJSON.
func WriteText(dir, name, text string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return fsx.WriteFileAtomic(filepath.Join(dir, name), []byte(text), 0o644)
}

// AppendIndex дописывает строку в index.jsonl ночи.
func (s *Store) AppendIndex(rec any) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.NightDir(), "index.jsonl"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// Passport — паспорт ночи: по нему потом видно, что именно мерили.
type Passport struct {
	Night         string      `json:"night"`
	StartedAt     time.Time   `json:"started_at"`
	FinishedAt    time.Time   `json:"finished_at,omitempty"`
	Host          string      `json:"host"`
	OllamaURL     string      `json:"ollama_url"`
	OllamaVersion string      `json:"ollama_version"`
	NumCtx        int         `json:"num_ctx"`
	Repeats       int         `json:"repeats"`
	Deadline      time.Time   `json:"deadline"`
	Suites        []string    `json:"suites"`
	Models        []ModelCard `json:"models"`
	Note          string      `json:"note,omitempty"`

	// Resumes — заходы в ночь после первого, см. resumePassport.
	Resumes []PassportResume `json:"resumes,omitempty"`
}

// PassportResume — заход в уже начатую ночь: с чем он шёл и что поменялось.
type PassportResume struct {
	At            time.Time `json:"at"`
	OllamaVersion string    `json:"ollama_version"`
	NumCtx        int       `json:"num_ctx"`
	Repeats       int       `json:"repeats"`
	Deadline      time.Time `json:"deadline"`
	Suites        []string  `json:"suites"`
	Note          string    `json:"note,omitempty"`
	// Changed — модели, которых в паспорте не было, сменившие digest (под тем
	// же тегом легли другие веса) или участие в прогоне.
	Changed []ModelCard `json:"changed_models,omitempty"`
}

// resumePassport — паспорт для нового захода в ночь, где prev — паспорт,
// уже лежащий на диске (nil — ночь новая).
//
// Ночь докатывается многими заходами: тик раз в четверть часа поднимает
// умерший прогон, недоделанное доезжает следующей ночью. Раньше каждый заход
// переписывал run.json целиком: пропадали время начала, пометка и карточки
// моделей, с которыми ночь начиналась, а смена весов под тем же тегом
// посреди ночи не оставляла следа. Теперь паспорт — запись первого захода,
// новые модели добавляются в его список, а каждый следующий заход
// дописывается в Resumes вместе с тем, что изменилось.
func resumePassport(prev, cur *Passport) *Passport {
	if prev == nil {
		return cur
	}
	known := make(map[string]ModelCard, len(prev.Models))
	for _, c := range prev.Models {
		known[c.Name] = c
	}
	r := PassportResume{At: cur.StartedAt, OllamaVersion: cur.OllamaVersion, NumCtx: cur.NumCtx,
		Repeats: cur.Repeats, Deadline: cur.Deadline, Suites: cur.Suites, Note: cur.Note}
	for _, c := range cur.Models {
		old, ok := known[c.Name]
		if !ok {
			prev.Models = append(prev.Models, c)
		}
		if !ok || old.Digest != c.Digest || old.Skipped != c.Skipped {
			r.Changed = append(r.Changed, c)
		}
	}
	prev.FinishedAt = time.Time{}
	prev.Resumes = append(prev.Resumes, r)
	return prev
}

// ModelCard — как модель выглядела на момент прогона. Digest важнее тега:
// тег можно перекачать, и под тем же именем окажутся другие веса.
type ModelCard struct {
	Name         string   `json:"name"`
	Digest       string   `json:"digest"`
	Quantization string   `json:"quantization"`
	ParameterSiz string   `json:"parameter_size"`
	SizeGiB      float64  `json:"size_gib"`
	Capabilities []string `json:"capabilities"`
	CtxTrained   int      `json:"ctx_trained,omitempty"`
	Skipped      string   `json:"skipped,omitempty"` // причина, если модель не участвует
}

// SavePassport записывает паспорт ночи.
func (s *Store) SavePassport(p *Passport) error { return WriteJSON(s.NightDir(), "run.json", p) }

// LoadPassport читает паспорт ночи, если он уже есть.
func (s *Store) LoadPassport() (*Passport, error) {
	b, err := os.ReadFile(filepath.Join(s.NightDir(), "run.json"))
	if err != nil {
		return nil, err
	}
	var p Passport
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	return &p, nil
}
