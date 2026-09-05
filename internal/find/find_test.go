package find

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// deadEmbedder — эмбеддер, который не отвечает: так ведёт себя сервер, пока
// карту занимает сборка графа (замер 29.08.2026 — запрос не ответил за 120 с).
// Считает обращения: их должно быть ровно одно.
type deadEmbedder struct{ calls int }

func (e *deadEmbedder) Model() string { return "bge-m3" }
func (e *deadEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	e.calls++
	<-ctx.Done()
	return nil, ctx.Err()
}

// Мёртвый эмбеддер спрашивают ОДИН раз.
//
// Если после неудачной пробы отдать его же поиску по книгам, человек ждёт срок
// дважды: пятнадцать секунд превращаются в тридцать на ровном месте.
func TestDeadEmbedderAskedOnce(t *testing.T) {
	emb := &deadEmbedder{}
	d := Deps{Coll: nil, Embedder: emb}

	// Коллекции нет — до поиска по книгам не дойдём, но проба уже случится
	// только если её вызвать; проверяем сам порядок на уровне queryVector.
	_, gotEmb, why := QueryVector(context.Background(), d, "вопрос",
		Opts{Semantic: true, QueryTimeout: 50 * time.Millisecond})

	if emb.calls != 1 {
		t.Errorf("эмбеддер спрошен %d раз, ожидался один", emb.calls)
	}
	if gotEmb != nil {
		t.Error("после неудачи эмбеддер не должен уходить в поиск по книгам")
	}
	if !strings.Contains(why, "искал по словам") {
		t.Errorf("объяснение не сказано: %q", why)
	}
}

// Причины «сломался» и «не настроен» звучат по-разному: чинятся они тоже
// по-разному, и одинаковый текст отправил бы человека не туда.
func TestWordsOnlyReasonsDiffer(t *testing.T) {
	noModel := Deps{}
	_, _, whyNotSet := QueryVector(context.Background(), noModel, "в",
		Opts{Semantic: true, QueryTimeout: time.Second})

	dead := Deps{Embedder: &deadEmbedder{}}
	_, _, whyDead := QueryVector(context.Background(), dead, "в",
		Opts{Semantic: true, QueryTimeout: 50 * time.Millisecond})

	if !strings.Contains(whyNotSet, "не настроен") {
		t.Errorf("для ненастроенного поиска ожидалось «не настроен»: %q", whyNotSet)
	}
	if !strings.Contains(whyDead, "недоступен") {
		t.Errorf("для поломки ожидалось «недоступен»: %q", whyDead)
	}
	if whyNotSet == whyDead {
		t.Error("причины обязаны различаться словами, а не только признаком")
	}
}

// Ожидание выключено настройкой — смысл не спрашиваем вовсе.
func TestZeroTimeoutSkipsEmbedder(t *testing.T) {
	emb := &deadEmbedder{}
	d := Deps{Embedder: emb}

	_, gotEmb, why := QueryVector(context.Background(), d, "в", Opts{Semantic: true, QueryTimeout: 0})
	if emb.calls != 0 {
		t.Errorf("при выключенном ожидании эмбеддер спрашивать нельзя, спрошен %d раз", emb.calls)
	}
	if gotEmb != nil || !strings.Contains(why, "выключено") {
		t.Errorf("ожидалось объяснение о выключенном ожидании: %q", why)
	}
}

// Пустой запрос и отсутствие коллекции — ошибки, а не пустая выдача.
func TestSearchRefusesEmptyInput(t *testing.T) {
	if _, err := Search(context.Background(), Deps{}, "   ", Opts{}); err == nil {
		t.Error("пустой запрос должен отклоняться")
	}
	if _, err := Search(context.Background(), Deps{}, "вопрос", Opts{}); err == nil {
		t.Error("без коллекции искать нечего — нужна ошибка")
	}
}

// Слияние: кусок, пришедший и поиском, и подтверждением графа, показывается
// один раз, и побеждает найденный поиском — он отобран по вопросу целиком.
func TestMergeDropsDuplicates(t *testing.T) {
	hits := []kb.Result{
		{ID: "books/1#1", Book: "Книга", Text: "текст первый", Snippet: "текст первый"},
		{ID: "books/2#7", Book: "Другая", Text: "текст второй", Snippet: "текст второй"},
	}
	fromG := []Excerpt{
		{ID: "books/1#1", Book: "Книга", Text: "текст первый", Graph: true},
		{ID: "books/9#3", Book: "Третья", Text: "текст третий", Graph: true},
	}

	out := merge(hits, fromG, Opts{TopK: 10, SnippetRunes: 100})
	if len(out) != 3 {
		t.Fatalf("ожидалось три выдержки без повторов, вышло %d", len(out))
	}
	if out[0].ID != "books/1#1" || out[0].Graph {
		t.Errorf("повторившийся кусок должен остаться от поиска, а не от графа: %+v", out[0])
	}
	if !out[2].Graph {
		t.Error("кусок, которого не было в поиске, должен прийти от графа с пометкой")
	}
}

// TopK не превышается: лента не должна забиваться сверх просимого.
func TestMergeRespectsTopK(t *testing.T) {
	hits := make([]kb.Result, 10)
	for i := range hits {
		hits[i] = kb.Result{ID: string(rune('a'+i)) + "/1#1", Text: "т"}
	}
	if out := merge(hits, nil, Opts{TopK: 3, SnippetRunes: 50}); len(out) != 3 {
		t.Errorf("вышло %d выдержек, просили 3", len(out))
	}
}

// Ссылка на книгу содержит всё, по чему её находят: название, автора, год,
// страницы и id для /read.
func TestExcerptLineHasEverything(t *testing.T) {
	line := Line(Excerpt{
		ID: "books/12#37", Book: "Learning Go", Author: "Jon Bodner",
		Year: 2024, Unit: "стр.", From: 136, To: 138,
	})
	for _, want := range []string{"Learning Go", "Jon Bodner", "2024 г.", "стр. 136–138", "id=books/12#37"} {
		if !strings.Contains(line, want) {
			t.Errorf("в ссылке нет %q: %s", want, line)
		}
	}
}

// Одна страница печатается без диапазона: «стр. 137–137» человек читает
// как ошибку.
func TestExcerptLineSinglePage(t *testing.T) {
	line := Line(Excerpt{Book: "К", Unit: "стр.", From: 137, To: 137})
	if strings.Contains(line, "–") {
		t.Errorf("для одной страницы диапазон не нужен: %s", line)
	}
}

// Короткая выдержка обрезается, полная — нет.
func TestRenderFullVsSnippet(t *testing.T) {
	long := strings.Repeat("слово ", 200)
	res := Result{
		Query: "проба", Collection: "books",
		Excerpts: []Excerpt{{ID: "books/1#1", Book: "К", Text: long, Snippet: cut(long, 100)}},
	}

	short := Render(res, false, nil)
	full := Render(res, true, nil)

	if len(short) >= len(full) {
		t.Error("полный вывод обязан быть длиннее короткого")
	}
	if !strings.Contains(short, "…") {
		t.Error("короткая выдержка должна заканчиваться многоточием")
	}
	if !strings.Contains(short, "/read books/1#1") {
		t.Error("под короткой выдачей нужна подсказка, как прочитать целиком")
	}
	if strings.Contains(full, "/search -f") {
		t.Error("при -f подсказка про -f не нужна")
	}
}

// Оговорка о поиске по словам стоит в начале, а не в хвосте: она меняет
// прочтение всего, что ниже.
func TestRenderPutsWordsNoteOnTop(t *testing.T) {
	out := Render(Result{
		Query: "проба", WordsOnly: true, WordsWhy: "смысловой поиск недоступен — искал по словам",
		Excerpts: []Excerpt{{ID: "books/1#1", Book: "К", Text: "т", Snippet: "т"}},
	}, false, nil)

	lines := strings.Split(out, "\n")
	if len(lines) < 2 || !strings.Contains(lines[1], "по словам") {
		t.Errorf("оговорка должна стоять второй строкой:\n%s", out)
	}
}

// Пустая выдача говорит об этом прямо и не притворяется найденной.
func TestRenderEmpty(t *testing.T) {
	out := Render(Result{Query: "чего нет"}, false, nil)
	if !strings.Contains(out, "ничего не нашлось") {
		t.Errorf("пустая выдача должна сказать об этом:\n%s", out)
	}
	if strings.Contains(out, "/read") {
		t.Error("подсказка «читать целиком» под пустой выдачей не нужна")
	}
}

// Выдержка под связью печатается и в интерфейсе, а не только в командной строке.
//
// До 02.09.2026 Render звал graph.Render с nil вместо источника кусков, и
// `/search` показывал «горутина —использует→ канал (подтверждений 48)» без
// единого слова о том, чем они связаны. Строка есть у командной строки —
// значит обязана быть и здесь: это одна и та же выдача.
func TestRenderShowsRelationEvidence(t *testing.T) {
	res := Result{
		Query:      "горутина",
		Collection: "проба",
		Entities:   []graph.FoundEntity{{Entity: graph.Entity{Name: "горутина", Type: "понятие"}}},
		Relations: []graph.FoundRelation{{
			Src: "горутина", Dst: "канал", Type: "использует", Count: 48,
			Evidence:  graph.ChunkKey{Doc: 1, Ord: 1},
			Evidences: []graph.ChunkKey{{Doc: 1, Ord: 1}},
		}},
	}
	src := testChunks{1: "Горутины обмениваются данными через каналы — это основной способ."}

	if out := Render(res, false, src); !strings.Contains(out, "обмениваются данными через каналы") {
		t.Fatalf("выдержки под связью нет:\n%s", out)
	}
	// Без источника — прежнее поведение, связь печатается без выдержки.
	if out := Render(res, false, nil); strings.Contains(out, "обмениваются данными") {
		t.Fatalf("без источника выдержки быть не должно:\n%s", out)
	}
}

// testChunks — куски по номеру для проверки отрисовки.
type testChunks map[uint32]string

func (c testChunks) ChunkByRef(doc, ord uint32) (kb.ChunkInfo, bool) {
	t, ok := c[ord]
	if !ok {
		return kb.ChunkInfo{}, false
	}
	return kb.ChunkInfo{Doc: doc, Ord: ord, Unit: "стр.", UnitFrom: 1, Text: t,
		Book: kb.BookRec{Title: "Проба", Year: 2026}}, true
}

// Заметка воздержания появляется только при пороге и слабой выдаче.
func TestAbstainNote(t *testing.T) {
	weak := Signals{Hits: 8, Top1Gap: 0.02}
	if AbstainNote(weak, 0) != "" {
		t.Error("без порога заметки быть не должно")
	}
	if AbstainNote(Signals{Hits: 1}, 0.1) != "" {
		t.Error("по одному куску разрыв не считается")
	}
	if AbstainNote(Signals{Hits: 8, Top1Gap: 0.3}, 0.1) != "" {
		t.Error("уверенная выдача не помечается")
	}
	if n := AbstainNote(weak, 0.1); n == "" || !strings.Contains(n, "0.020") {
		t.Errorf("слабая выдача должна помечаться с числом: %q", n)
	}
}

// Заметка по абсолютной оценке — только у реранкера и только ниже порога.
func TestAbstainScoreNote(t *testing.T) {
	low := -2.0
	if AbstainScoreNote(Signals{Hits: 8, Top1: -5}, nil, true) != "" {
		t.Error("без порога заметки нет")
	}
	if AbstainScoreNote(Signals{Hits: 8, Top1: -5}, &low, false) != "" {
		t.Error("без реранкера шкала несопоставима — заметки нет")
	}
	if AbstainScoreNote(Signals{Hits: 8, Top1: 1.1}, &low, true) != "" {
		t.Error("оценка выше порога — заметки нет")
	}
	if n := AbstainScoreNote(Signals{Hits: 8, Top1: -5.25}, &low, true); n == "" || !strings.Contains(n, "-5.25") {
		t.Errorf("ниже порога заметка с числом: %q", n)
	}
}
