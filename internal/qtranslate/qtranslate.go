// Package qtranslate переводит русский вопрос на английский для словесной
// ветки поиска по книгам (этап 112, шаг 2; настройки kb.translate и
// kb.translate_model).
//
// **Зачем.** Вопросы русские, книги чаще английские. Векторы язык переходят,
// а поиск по словам русский вопрос в английской книге почти не находит
// (recall@8 0,092 на kb_golden). Перевод дописывается ТОЛЬКО к словесной ветке
// (find.Deps.Translator → kb.SearchOpts.WordsExtra), вектор считается по
// вопросу как есть. Замер 13.10.2026, kb_golden, k = 8, с реранкером:
// слияние 0,317 → 0,361 (145 → 165 из 457, парно 1 против 21).
//
// Перевод графом (имена понятий) и PRF (слова из первых кусков) слияние не
// подняли — этап 112, О8; модель переводит весь вопрос связной фразой.
package qtranslate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graphex"
)

// Prompt — просьба перевести вопрос, ничего не добавляя.
const Prompt = `Translate the user's question into English.
Keep technical terms, commands, product names and code exactly as they are.
Output only the translated question on one line: no quotes, no comments, no explanations.`

// Timeout — сколько ждать перевода одного вопроса. Тёплый вызов шёл
// 0,70–0,83 с, загрузка модели — 7,8 с (замер 13.10.2026): срок покрывает
// загрузку, но не держит поиск дольше, чем вектор вопроса (kb.DefaultQueryTimeout).
const Timeout = 15 * time.Second

// cacheMax — сколько переводов помнить: один вопрос за обмен переводят
// и подмес, и инструмент поиска.
const cacheMax = 256

// Translator переводит вопросы одной моделью и помнит последние переводы.
// Нулевой указатель — перевод выключен: Translate отдаёт пустую строку.
type Translator struct {
	ex *graphex.Extractor

	mu    sync.Mutex
	cache map[string]string
	order []string
	// failUntil — до этого времени переводчик не зовёт модель после отказа
	// сервера: один отказ соединения стоил 6,0 с на каждый поиск (повторы
	// клиента; проба аудита 16.10.2026), и лежащий сервер перевода тормозил
	// бы каждый вопрос, пока его не поднимут.
	failUntil time.Time
	now       func() time.Time
}

// Cooldown — сколько не звать модель после отказа сервера перевода.
const Cooldown = time.Minute

// ErrCooldown — перевод пропущен: сервер недавно отказал.
var ErrCooldown = errors.New("сервер перевода недавно не ответил, перевод пропущен до следующей минуты")

// Reason — причина отказа перевода для человека и для note выдачи: без
// адресов и текста ошибки клиента HTTP (там стоит адрес сервера модели,
// а note уходит модели и клиенту MCP — закон 3).
func Reason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrCooldown):
		return "сервер перевода недавно не ответил, пауза минуту"
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Sprintf("модель не ответила за %s", Timeout)
	case errors.Is(err, context.Canceled):
		return "запрос отменён"
	default:
		return "сервер перевода недоступен"
	}
}

// New — переводчик по настройкам или nil, если kb.translate выключен или
// модели взять негде (нет ни kb.translate_model, ни graph.summary_model,
// ни graph.model).
func New(cfg *config.Config, fallbackURL string, headers map[string]string) *Translator {
	if cfg == nil || !cfg.KB.Translate {
		return nil
	}
	ex := graphex.New(cfg.Graph.ExtractOptions(), fallbackURL, Timeout, headers)
	if ex == nil {
		return nil
	}
	model := cfg.KB.TranslateModel
	if model == "" {
		model = cfg.Graph.SummaryModel
	}
	// Несколько человек и инструментов могут спросить разом — четыре вызова
	// одновременно, как у замера (--kb-eval-translate).
	ex = ex.WithModel(model, 4)
	return &Translator{ex: ex, cache: map[string]string{}}
}

// Model — какой моделью переводит; пусто у выключенного.
func (t *Translator) Model() string {
	if t == nil || t.ex == nil {
		return ""
	}
	return t.ex.Model()
}

// Translate — английский перевод вопроса. Вопрос без кириллицы не переводится
// (пустая строка, без ошибки): ему перевод не нужен.
func (t *Translator) Translate(ctx context.Context, question string) (string, error) {
	if t == nil || t.ex == nil || !HasCyrillic(question) {
		return "", nil
	}
	now := time.Now
	if t.now != nil {
		now = t.now
	}
	t.mu.Lock()
	if en, ok := t.cache[question]; ok {
		t.mu.Unlock()
		return en, nil
	}
	if now().Before(t.failUntil) {
		t.mu.Unlock()
		return "", ErrCooldown
	}
	t.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	raw, err := t.ex.Extract(ctx, Prompt, question)
	if err != nil {
		// Отмена своим контекстом — не отказ сервера: паузу не ставим.
		if !errors.Is(err, context.Canceled) {
			t.mu.Lock()
			t.failUntil = now().Add(Cooldown)
			t.mu.Unlock()
		}
		return "", err
	}
	en := Clean(raw)
	if en == "" {
		return "", nil
	}
	t.mu.Lock()
	if _, ok := t.cache[question]; !ok {
		if len(t.order) >= cacheMax {
			delete(t.cache, t.order[0])
			t.order = t.order[1:]
		}
		t.cache[question] = en
		t.order = append(t.order, question)
	}
	t.mu.Unlock()
	return en, nil
}

// HasCyrillic — есть ли в тексте хоть одна кириллическая буква.
func HasCyrillic(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

// Clean берёт из ответа модели первую непустую строку без кавычек и пометки
// вида «Translation:».
func Clean(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if i := strings.Index(line, ":"); i > 0 && i < 20 && strings.EqualFold(strings.TrimSpace(line[:i]), "translation") {
			line = strings.TrimSpace(line[i+1:])
		}
		return strings.Trim(line, "\"'«»“”` ")
	}
	return ""
}
