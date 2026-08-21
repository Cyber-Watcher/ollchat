package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Уровни задач. Вес уровня влияет на итоговый балл области: мелочь и разбор
// стоят меньше, чем работа в чужом коде и написанное с нуля.
const (
	LevelSmall   = 1 // У1 — одна функция, проверяется автоматически
	LevelInCode  = 2 // У2 — правка в готовом коде
	LevelScratch = 3 // У3 — рабочий кусок с нуля
	LevelReview  = 4 // У4 — разбор: диагноз, аудит, объяснение
)

var levelWeight = map[int]float64{LevelSmall: 1, LevelInCode: 2, LevelScratch: 3, LevelReview: 2}

// Виды проверки ответа.
const (
	VerifyContainer = "container" // собрать и прогнать в контейнере
	VerifyChecklist = "checklist" // сверить с чек-листом, балл предварительный
	VerifyNone      = "none"      // только сохранить ответ
)

// Suite — набор задач одной области: программирование, DevOps, ИБ, ИИ.
type Suite struct {
	Name        string `toml:"name"`
	Description string `toml:"description"`
	Tasks       []Task `toml:"task"`

	path string // откуда прочитан — нужен для сообщений об ошибках
}

// Task — одна задача набора.
type Task struct {
	ID     string `toml:"id"`
	Level  int    `toml:"level"`
	Prompt string `toml:"prompt"`
	System string `toml:"system"`

	// Attach — файлы из fixtures, которые приложить к вопросу текстом.
	Attach []string `toml:"attach"`

	// Needs — возможности, без которых задача модели не даётся: tools, vision.
	// Модель без них задачу пропускает, и это не считается провалом.
	Needs []string `toml:"needs"`

	// NumCtx — своё окно контекста, если задаче нужно длинное. 0 — общее для прогона.
	NumCtx int `toml:"num_ctx"`

	// Timeout — предел на одну генерацию. 0 — предел прогона.
	Timeout Duration `toml:"timeout"`

	Answer AnswerSpec `toml:"answer"`
	Verify Verify     `toml:"verify"`
}

// AnswerSpec описывает, что вынуть из ответа модели перед проверкой.
type AnswerSpec struct {
	File string `toml:"file"` // куда положить код: work/<file>
	Lang string `toml:"lang"` // язык блока кода: go, rust, ts, yaml…
}

// Verify — как проверять ответ.
type Verify struct {
	Kind    string      `toml:"kind"`
	Image   string      `toml:"image"`   // для container
	Setup   []string    `toml:"setup"`   // что положить в work/ до проверки (пути в fixtures)
	Steps   []Step      `toml:"step"`    // для container
	Items   []CheckItem `toml:"item"`    // для checklist
	Timeout Duration    `toml:"timeout"` // предел на всю проверку
}

// Step — шаг проверки в контейнере. Балл начисляется за пройденный шаг:
// «собирается» стоит меньше, чем «проходит тесты».
type Step struct {
	Name  string  `toml:"name"`
	Cmd   string  `toml:"cmd"`
	Score float64 `toml:"score"`
}

// CheckItem — пункт чек-листа. Any — что должно быть в ответе, None — чего быть
// не должно (выдуманные числа, несуществующие параметры). Балл предварительный:
// окончательный ставится человеком при утреннем разборе.
type CheckItem struct {
	Name  string   `toml:"name"`
	Any   []string `toml:"any"`
	None  []string `toml:"none"`
	Score float64  `toml:"score"`
}

// Duration — длительность в духе "5m", читаемая из TOML.
type Duration time.Duration

// UnmarshalText читает длительность из строки TOML.
func (d *Duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// Get возвращает длительность или запасное значение, если она не задана.
func (d Duration) Get(fallback time.Duration) time.Duration {
	if d == 0 {
		return fallback
	}
	return time.Duration(d)
}

// Weight возвращает вес задачи в балле области.
func (t Task) Weight() float64 {
	if w, ok := levelWeight[t.Level]; ok {
		return w
	}
	return 1
}

// LoadSuite читает набор задач и проверяет его целиком: набор с ошибкой лучше
// отвергнуть до начала ночи, чем посреди неё.
func LoadSuite(path string) (*Suite, error) {
	var s Suite
	if _, err := toml.DecodeFile(path, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s.path = path
	if s.Name == "" {
		s.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &s, nil
}

// LoadSuites читает все наборы каталога, отсортированные по имени файла.
func LoadSuites(dir string) ([]*Suite, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".toml") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	out := make([]*Suite, 0, len(names))
	for _, n := range names {
		s, err := LoadSuite(filepath.Join(dir, n))
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// Validate проверяет набор: уникальность идентификаторов, известный вид
// проверки, сумму баллов. Сумма важна: если шаги дают 0.6, задача никогда
// не наберёт единицу, и область молча просядет.
func (s *Suite) Validate() error {
	if len(s.Tasks) == 0 {
		return fmt.Errorf("набор %q пуст", s.Name)
	}
	seen := make(map[string]bool, len(s.Tasks))
	for i := range s.Tasks {
		t := &s.Tasks[i]
		switch {
		case t.ID == "":
			return fmt.Errorf("задача №%d без идентификатора", i+1)
		case seen[t.ID]:
			return fmt.Errorf("задача %q повторяется", t.ID)
		case t.Level < LevelSmall || t.Level > LevelReview:
			return fmt.Errorf("задача %q: уровень %d вне 1..4", t.ID, t.Level)
		case strings.TrimSpace(t.Prompt) == "":
			return fmt.Errorf("задача %q: пустая постановка", t.ID)
		}
		seen[t.ID] = true

		if t.Verify.Kind == "" {
			t.Verify.Kind = VerifyNone
		}
		switch t.Verify.Kind {
		case VerifyContainer:
			if t.Verify.Image == "" {
				return fmt.Errorf("задача %q: проверка в контейнере без образа", t.ID)
			}
			if len(t.Verify.Steps) == 0 {
				return fmt.Errorf("задача %q: проверка в контейнере без шагов", t.ID)
			}
			if err := checkScores(t.ID, stepScores(t.Verify.Steps)); err != nil {
				return err
			}
		case VerifyChecklist:
			if len(t.Verify.Items) == 0 {
				return fmt.Errorf("задача %q: чек-лист без пунктов", t.ID)
			}
			if err := checkScores(t.ID, itemScores(t.Verify.Items)); err != nil {
				return err
			}
		case VerifyNone:
		default:
			return fmt.Errorf("задача %q: неизвестный вид проверки %q", t.ID, t.Verify.Kind)
		}
	}
	return nil
}

func stepScores(steps []Step) []float64 {
	out := make([]float64, len(steps))
	for i, s := range steps {
		out[i] = s.Score
	}
	return out
}

func itemScores(items []CheckItem) []float64 {
	out := make([]float64, len(items))
	for i, it := range items {
		out[i] = it.Score
	}
	return out
}

// checkScores требует, чтобы положительные баллы в сумме давали единицу.
// Пункты со штрафом (отрицательный балл) в сумму не входят: они снимают
// за выдуманное сверх набранного.
func checkScores(taskID string, scores []float64) error {
	var sum float64
	for _, v := range scores {
		if v > 0 {
			sum += v
		}
	}
	if sum < 0.999 || sum > 1.001 {
		return fmt.Errorf("задача %s: сумма баллов %.3f, а должна быть 1.0", taskID, sum)
	}
	return nil
}
