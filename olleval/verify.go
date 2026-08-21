package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// VerifyResult — что дала проверка одной попытки. Ложится в verify.json рядом
// с ответом: по нему балл можно перепроверить, не перезапуская модель.
type VerifyResult struct {
	Kind        string       `json:"kind"`
	Score       float64      `json:"score"`
	NeedsReview bool         `json:"needs_review"`
	Verdict     string       `json:"verdict"`
	Steps       []StepResult `json:"steps,omitempty"`
	Checks      []ItemResult `json:"checks,omitempty"`
}

// StepResult — результат шага проверки в контейнере.
type StepResult struct {
	Name     string  `json:"name"`
	Cmd      string  `json:"cmd"`
	ExitCode int     `json:"exit_code"`
	Seconds  float64 `json:"seconds"`
	Score    float64 `json:"score"`
	Output   string  `json:"output"`
}

// ItemResult — результат пункта чек-листа. Балл предварительный: окончательный
// ставит человек при утреннем разборе, поэтому рядом лежит найденное совпадение.
type ItemResult struct {
	Name    string  `json:"name"`
	Hit     bool    `json:"hit"`
	Matched string  `json:"matched,omitempty"`
	Score   float64 `json:"score"`
}

// Verifier проверяет ответы. Docker и лимиты контейнера вынесены в поля, чтобы
// проверку можно было прогнать и без контейнера — в тестах.
type Verifier struct {
	Fixtures string
	Docker   string        // путь к docker; пусто — проверка в контейнере не делается
	Memory   string        // предел памяти контейнера, например "2g"
	CPUs     string        // предел ядер, например "4"
	Timeout  time.Duration // предел на всю проверку, если задача не задала свой
	Runner   func(ctx context.Context, name string, args ...string) (string, int, error)
}

// NewVerifier готовит проверку по лимитам из настроек.
func NewVerifier(fixtures string, cfg VerifyCfg) *Verifier {
	docker, _ := exec.LookPath("docker")
	return &Verifier{
		Fixtures: fixtures, Docker: docker,
		Memory: cfg.Memory, CPUs: cfg.CPUs, Timeout: cfg.Timeout.Get(10 * time.Minute),
		Runner: runCommand,
	}
}

// Verify проверяет ответ задачи и возвращает балл. Ошибки самой проверки
// (нет образа, отвалился docker) не молчат: они попадают в вердикт, иначе
// сломанная проверка выглядела бы как глупая модель.
func (v *Verifier) Verify(ctx context.Context, t *Task, dir, answer string) *VerifyResult {
	switch t.Verify.Kind {
	case VerifyChecklist:
		return v.checklist(t, answer)
	case VerifyContainer:
		return v.container(ctx, t, dir, answer)
	default:
		return &VerifyResult{Kind: VerifyNone, NeedsReview: true, Verdict: "проверки нет, нужен разбор человеком"}
	}
}

// checklist сверяет ответ с пунктами. Пункт с положительным баллом добавляет
// его при попадании, пункт со штрафом снимает — за выдуманное сверх набранного.
func (v *Verifier) checklist(t *Task, answer string) *VerifyResult {
	res := &VerifyResult{Kind: VerifyChecklist, NeedsReview: true}
	low := strings.ToLower(answer)
	for _, item := range t.Verify.Items {
		r := ItemResult{Name: item.Name, Score: 0}
		if m, ok := matchAny(low, answer, item.Any); ok {
			r.Hit, r.Matched, r.Score = true, m, item.Score
		}
		if m, ok := matchAny(low, answer, item.None); ok {
			r.Hit, r.Matched = true, m
			r.Score = item.Score // у запрещающих пунктов балл отрицательный
		}
		res.Score += r.Score
		res.Checks = append(res.Checks, r)
	}
	if res.Score < 0 {
		res.Score = 0
	}
	res.Verdict = fmt.Sprintf("предварительно %.2f по чек-листу, нужен разбор человеком", res.Score)
	return res
}

// matchAny ищет любое из выражений. Обычная строка ищется без учёта регистра
// **с начала слова**, строка с приставкой "re:" — как регулярное выражение
// по исходному тексту.
//
// Начало слова проверяется, а конец нет, и это не небрежность. Русские пункты
// пишутся основой («вытесн» ловит и «вытеснение», и «вытесняется»), поэтому
// продолжение слова разрешено. А вот совпадение в середине слова обманывает:
// пункт «назвал num_ctx» иначе засчитывался бы за выдуманную переменную
// `OLLAMA_NUM_CTX`, то есть ровно за ошибку, которую пункт должен ловить.
func matchAny(low, raw string, patterns []string) (string, bool) {
	for _, p := range patterns {
		if rx, ok := strings.CutPrefix(p, "re:"); ok {
			re, err := regexp.Compile(rx)
			if err != nil {
				continue
			}
			if m := re.FindString(raw); m != "" {
				return m, true
			}
			continue
		}
		if containsAtWordStart(low, strings.ToLower(p)) {
			return p, true
		}
	}
	return "", false
}

// containsAtWordStart ищет подстроку, стоящую в начале слова.
func containsAtWordStart(text, sub string) bool {
	if sub == "" {
		return false
	}
	for from := 0; ; {
		i := strings.Index(text[from:], sub)
		if i < 0 {
			return false
		}
		at := from + i
		if at == 0 || !isWordRune(lastRune(text[:at])) {
			return true
		}
		from = at + len(sub)
		if from >= len(text) {
			return false
		}
	}
}

func lastRune(s string) rune {
	r := []rune(s)
	if len(r) == 0 {
		return ' '
	}
	return r[len(r)-1]
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// container готовит рабочий каталог и прогоняет шаги проверки в контейнере.
func (v *Verifier) container(ctx context.Context, t *Task, dir, answer string) *VerifyResult {
	res := &VerifyResult{Kind: VerifyContainer}
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		res.Verdict = "не удалось создать рабочий каталог: " + err.Error()
		return res
	}
	for _, rel := range t.Verify.Setup {
		if err := copyTree(filepath.Join(v.Fixtures, rel), filepath.Join(work, filepath.Base(rel))); err != nil {
			res.Verdict = "не удалось разложить входные файлы: " + err.Error()
			return res
		}
	}

	code := ExtractCode(answer, t.Answer.Lang)
	if code == "" {
		res.Verdict = "в ответе нет блока кода — проверять нечего"
		return res
	}
	name := t.Answer.File
	if name == "" {
		name = "answer.txt"
	}
	target := filepath.Join(work, name)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		res.Verdict = "не удалось создать каталог ответа: " + err.Error()
		return res
	}
	if err := os.WriteFile(target, []byte(code), 0o644); err != nil {
		res.Verdict = "не удалось записать код: " + err.Error()
		return res
	}

	if v.Docker == "" {
		res.NeedsReview = true
		res.Verdict = "docker недоступен — проверка не выполнялась"
		return res
	}

	limit := t.Verify.Timeout.Get(v.Timeout)
	if limit <= 0 {
		limit = 10 * time.Minute
	}
	stepCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()

	for _, st := range t.Verify.Steps {
		args := []string{"run", "--rm", "--network=none",
			"--memory=" + v.Memory, "--cpus=" + v.CPUs,
			"-v", work + ":/w", "-w", "/w", t.Verify.Image,
			// Именно "sh -c", а не "sh -lc": логин-оболочка перечитывает
			// /etc/profile и затирает PATH, выставленный в образе, — в образе
			// golang после этого не находится сам go.
			"sh", "-c", st.Cmd}
		began := time.Now()
		out, code, err := v.Runner(stepCtx, v.Docker, args...)
		r := StepResult{Name: st.Name, Cmd: st.Cmd, ExitCode: code,
			Seconds: time.Since(began).Seconds(), Output: tail(out, 4000)}
		if err != nil && code == 0 {
			// Сам docker не запустился: это наша беда, не модели.
			r.ExitCode = -1
			r.Output = tail(out+"\n"+err.Error(), 4000)
			res.Steps = append(res.Steps, r)
			res.NeedsReview = true
			res.Verdict = "проверка сорвалась: " + err.Error()
			return res
		}
		if code == 0 {
			r.Score = st.Score
			res.Score += st.Score
		}
		res.Steps = append(res.Steps, r)
		if code != 0 {
			// Дальнейшие шаги без предыдущего смысла не имеют: не собралось —
			// нечего и тестировать.
			res.Verdict = fmt.Sprintf("шаг %q не прошёл (код %d)", st.Name, code)
			return res
		}
	}
	res.Verdict = "все шаги пройдены"
	return res
}

// runCommand выполняет команду и возвращает вывод и код возврата.
func runCommand(ctx context.Context, name string, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		var ee *exec.ExitError
		if ok := asExitError(err, &ee); ok {
			return string(out), ee.ExitCode(), nil
		}
		return string(out), 0, err
	}
	return string(out), 0, nil
}

// copyTree копирует файл или каталог целиком.
func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, data, info.Mode().Perm())
	}
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, fi.Mode().Perm())
	})
}

// tail оставляет от вывода последние n знаков: сборочные логи бывают
// на мегабайты, а интересен всегда конец.
func tail(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…\n" + string(r[len(r)-n:])
}
