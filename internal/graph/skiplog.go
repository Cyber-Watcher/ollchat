package graph

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Журнал причин пропуска: почему кусок не попал в граф.
//
// ЗАЧЕМ. Кусок, на котором модель не дала разбираемого ответа, помечается
// пропущенным — и штатная сборка больше НИКОГДА его не берёт (см. takesChunk:
// возвратные случаи есть только у служебных и у пустых по явной просьбе).
// То есть пропуск — это потеря, и до 27.09.2026 она была немой: в итоге книги
// печаталось одно число «пропущено N», а причина жила только в памяти процесса
// и умирала вместе с ним.
//
// Цена немоты, найдено 27.09.2026: за один день три книги подряд потеряли
// 45, 26 и 36 кусков (107 всего), следующие три — ни одного. Чем они
// отличались, выяснить было нечем: ни текста ответа модели, ни вида ошибки
// нигде не осталось. Владелец в тот же вечер велел завести запись причин.
//
// ЧТО ПИШЕТСЯ. По строке на пропущенный кусок в `skipped.jsonl` рядом с графом:
// книга, номер куска, единица текста (страница/глава), вид причины и её текст.
// Файл append-only, как реестр сущностей и журнал склеек: его можно удалить,
// и граф от этого не изменится — он только для человека и для разбора.
//
// ЧЕГО НЕ ДЕЛАЕТ. Не возвращает куски в работу (это `--graph-forget-chunks`)
// и не хранит сам текст куска: он есть в коллекции по Doc+Ord, а в журнале
// занял бы мегабайты.
const skipLogFile = "skipped.jsonl"

// skipWhyMax — сколько знаков причины хранить. Текст ошибки модели бывает
// в килобайт (весь неразобранный ответ), а для разбора хватает начала:
// вид ошибки и первые слова стоят впереди.
const skipWhyMax = 300

// Виды причин. Строкой, а не числом: журнал читают глазами через месяц.
const (
	// SkipEmptyAnswer — модель вернула пустой ответ (и повтор не помог).
	SkipEmptyAnswer = "пустой ответ модели"
	// SkipParse — ответ есть, но разобрать его не удалось.
	SkipParse = "ответ не разобран"
	// SkipOther — прочее: всё, что не подошло под два вида выше.
	SkipOther = "прочее"
)

// SkipRec — одна запись журнала.
type SkipRec struct {
	Doc  uint32 `json:"doc"`
	Ord  uint32 `json:"ord"`
	Book string `json:"book,omitempty"`
	Unit string `json:"unit,omitempty"`
	Kind string `json:"kind"`
	Why  string `json:"why,omitempty"`
	At   int64  `json:"at"`
}

// SkipLog — журнал пропусков одной коллекции.
type SkipLog struct {
	mu   sync.Mutex
	path string
	recs []SkipRec
	bad  string // беда чтения: говорим о ней, но работать не мешаем
	// noNL — файл кончается не переводом строки (запись оборвалась): перед
	// следующей записью его надо поставить, иначе две строки склеятся в одну
	// битую.
	noNL bool
}

// openSkipLog читает журнал, если он есть. Отсутствие файла — не ошибка:
// у коллекции, где пропусков не было, его и не должно быть.
func openSkipLog(dir string) *SkipLog {
	s := &SkipLog{path: filepath.Join(dir, skipLogFile)}
	b, err := os.ReadFile(s.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.bad = err.Error()
		}
		return s
	}
	s.noNL = len(b) > 0 && b[len(b)-1] != '\n'
	broken := 0
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r SkipRec
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			// Обрезанная строка (падение на записи) — не повод терять журнал.
			// И не повод бросать чтение: файл дозаписывается, и за битой
			// строкой идут целые — прежний break терял их все.
			broken++
			s.bad = fmt.Sprintf("журнал пропусков прочитан не до конца: нечитаемых строк %d, они пропущены (%v)", broken, err)
			continue
		}
		s.recs = append(s.recs, r)
	}
	return s
}

// SkipLog — журнал пропусков этого графа.
func (g *Graph) SkipLog() *SkipLog {
	if g == nil {
		return nil
	}
	g.skipOnce.Do(func() { g.skipLog = openSkipLog(g.dir) })
	return g.skipLog
}

// Add дозаписывает причину пропуска. Ошибка записи возвращается, но вызывающий
// не обязан на ней останавливать сборку: журнал — для человека, а не для графа.
func (s *SkipLog) Add(r SkipRec) error {
	if s == nil {
		return nil
	}
	r.Why = trimRunes(r.Why, skipWhyMax)
	if r.Kind == "" {
		r.Kind = SkipOther
	}
	if r.At == 0 {
		r.At = time.Now().Unix()
	}
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if s.noNL {
		line = append([]byte{'\n'}, line...)
	}
	if _, err := f.Write(line); err != nil {
		f.Close()
		return err
	}
	s.noNL = false
	if err := f.Close(); err != nil {
		return err
	}
	s.recs = append(s.recs, r)
	return nil
}

// Count — сколько записей в журнале всего.
func (s *SkipLog) Count() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.recs)
}

// Problem — беда чтения журнала или пустая строка.
func (s *SkipLog) Problem() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bad
}

// KindCount — пара «вид причины — сколько раз».
type KindCount struct {
	Kind  string
	Count int
}

// Kinds — виды причин по убыванию частоты. `since` отсекает старое
// (нуль — считать всё): после книги интересны её пропуски, а не годовые.
func (s *SkipLog) Kinds(since int64) []KindCount {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	by := map[string]int{}
	for _, r := range s.recs {
		if since > 0 && r.At < since {
			continue
		}
		by[r.Kind]++
	}
	out := make([]KindCount, 0, len(by))
	for k, n := range by {
		out = append(out, KindCount{Kind: k, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// Books — сколько пропусков у каждой книги, по убыванию. Нужно, чтобы через
// сутки было видно, какую книгу перечитывать.
func (s *SkipLog) Books(since int64) []KindCount {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	by := map[string]int{}
	for _, r := range s.recs {
		if since > 0 && r.At < since {
			continue
		}
		name := r.Book
		if name == "" {
			name = "(книга не названа)"
		}
		by[name]++
	}
	out := make([]KindCount, 0, len(by))
	for k, n := range by {
		out = append(out, KindCount{Kind: k, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// SkipKindOf — вид причины по ошибке разбора. Отдельной функцией, чтобы
// и сборка, и тесты называли виды одинаково.
func SkipKindOf(err error) string {
	switch {
	case err == nil:
		return SkipOther
	case errors.Is(err, ErrEmptyAnswer):
		return SkipEmptyAnswer
	case isParseError(err):
		return SkipParse
	default:
		return SkipOther
	}
}

// isParseError — ошибка пришла от разбора ответа модели, а не от дороги.
// Признак — обёртка ParseFactsFor: она добавляет к тексту свои слова.
func isParseError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, mark := range []string{"разбор ответа", "не разобрал", "JSON", "json"} {
		if strings.Contains(msg, mark) {
			return true
		}
	}
	return false
}

// trimRunes режет по знакам, а не по байтам: обрезанный посередине русский
// знак превратил бы строку JSON в нечитаемую.
func trimRunes(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
