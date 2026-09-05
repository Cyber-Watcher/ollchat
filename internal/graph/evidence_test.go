package graph

import (
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// chunkSource — хранилище кусков для проверки отрисовки.
type chunkSource struct{ text string }

func (c chunkSource) ChunkByRef(doc, ord uint32) (kb.ChunkInfo, bool) {
	if doc == 0 && ord == 0 {
		return kb.ChunkInfo{}, false
	}
	return kb.ChunkInfo{Doc: doc, Ord: ord, Unit: "стр.", UnitFrom: 42,
		Text: c.text, Book: kb.BookRec{Title: "Проба", Year: 2026}}, true
}

// Под связью печатается фраза из книги — та, ради которой связь и извлекли.
//
// Пояснение к связи модель возвращает, но мы его не храним, и добрать задним
// числом нельзя (110 часов карты и перекос между каталогами). Кусок-подтверждение
// есть у каждой связи, и в нём та же мысль словами книги.
func TestRelationShowsEvidenceLine(t *testing.T) {
	src := chunkSource{text: "Пролог, не относящийся к делу, занимает несколько строк " +
		"и продолжается дальше. Горутины обмениваются данными через каналы, " +
		"и это основной способ связи между ними. Дальше снова посторонний текст."}
	res := SearchResult{
		Entities: []FoundEntity{{Entity: Entity{Name: "горутина", Type: "понятие"}}},
		Relations: []FoundRelation{{Src: "горутина", Dst: "канал", Type: "использует",
			Count: 48, Evidence: ChunkKey{Doc: 1, Ord: 1}}},
	}
	out := Render(src, res, RenderOpts{Collection: "books"})

	if !strings.Contains(out, "горутина —использует→ канал") {
		t.Fatalf("связь не напечатана:\n%s", out)
	}
	if !strings.Contains(out, "каналы") {
		t.Errorf("под связью нет фразы из книги:\n%s", out)
	}
	if strings.Contains(out, "Пролог, не относящийся") {
		t.Errorf("выдержка взята с начала куска, а не вокруг понятия:\n%s", out)
	}
}

// Выдержку можно выключить: она стоит места в контексте модели.
func TestRelationEvidenceCanBeDisabled(t *testing.T) {
	src := chunkSource{text: "Горутины обмениваются данными через каналы."}
	res := SearchResult{
		Entities: []FoundEntity{{Entity: Entity{Name: "горутина", Type: "понятие"}}},
		Relations: []FoundRelation{{Src: "горутина", Dst: "канал", Type: "использует",
			Count: 1, Evidence: ChunkKey{Doc: 1, Ord: 1}}},
	}
	out := Render(src, res, RenderOpts{Collection: "books", RelationRunes: -1})
	if strings.Contains(out, "обмениваются") {
		t.Errorf("выдержка напечатана при RelationRunes = -1:\n%s", out)
	}
}

// Связь без подтверждения не роняет отрисовку и не печатает пустой строки.
func TestRelationWithoutEvidence(t *testing.T) {
	src := chunkSource{text: "неважно"}
	res := SearchResult{
		Entities:  []FoundEntity{{Entity: Entity{Name: "а", Type: "понятие"}}},
		Relations: []FoundRelation{{Src: "а", Dst: "б", Type: "связано", Count: 1}},
	}
	out := Render(src, res, RenderOpts{Collection: "books"})
	if !strings.Contains(out, "а —связано→ б") {
		t.Fatalf("связь не напечатана:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "…" {
			t.Errorf("напечатана пустая выдержка:\n%s", out)
		}
	}
}

// Окно вырезается вокруг слова, а не с начала текста.
func TestAroundCutsWindow(t *testing.T) {
	text := strings.Repeat("посторонний текст ", 20) + "КАНАЛ здесь " + strings.Repeat("хвост ", 20)
	got := around(text, []string{"канал"}, 60)
	if !strings.Contains(strings.ToLower(got), "канал") {
		t.Errorf("искомое слово не попало в окно: %q", got)
	}
	if strings.HasPrefix(got, "посторонний текст посторонний") {
		t.Errorf("окно взято с начала текста: %q", got)
	}
}

// Окно берётся там, где оба конца связи стоят рядом, а не у первого вхождения.
//
// Замер 02.09.2026: у связи «Go —использует→ Garbage collection» выдержкой стал
// абзац про приведение типов — потому что имя `Go` встречается в техническом
// тексте на каждом шагу, и первое его вхождение почти никогда не там, где книга
// говорит о связи.
func TestAroundPrefersWindowWithBothEnds(t *testing.T) {
	text := "Go компилируется в один файл, и это удобно. " +
		strings.Repeat("Посторонний текст про совсем другое. ", 12) +
		"В Go сборка мусора освобождает память кучи автоматически."

	got := around(text, []string{"Go", "сборка мусора"}, 140)

	if !strings.Contains(got, "сборка мусора") {
		t.Fatalf("окно не накрыло второй конец связи:\n%s", got)
	}
	if strings.Contains(got, "компилируется в один файл") {
		t.Fatalf("окно взято у первого вхождения, а не у связи:\n%s", got)
	}
}

// Нашёлся только один конец — прежнее поведение, окно вокруг него.
func TestAroundFallsBackToSingleEnd(t *testing.T) {
	text := strings.Repeat("Начало, не относящееся к делу. ", 10) +
		"Каналы связывают горутины между собой."

	got := around(text, []string{"горутин", "чего-то-чего-нет"}, 140)

	if !strings.Contains(got, "горутины") {
		t.Fatalf("окно не накрыло единственный найденный конец:\n%s", got)
	}
}

// Ни одного конца не нашлось — отдаём текст как есть, а не пустоту.
func TestAroundWithoutAnyEnd(t *testing.T) {
	text := "Текст, в котором нет ни одного из имён."
	if got := around(text, []string{"горутина", "канал"}, 140); got != text {
		t.Fatalf("ожидался текст целиком, получено: %q", got)
	}
}

// multiChunk — хранилище, где у каждой пары (doc,ord) свой текст.
type multiChunk map[uint32]string

func (m multiChunk) ChunkByRef(doc, ord uint32) (kb.ChunkInfo, bool) {
	t, ok := m[ord]
	if !ok {
		return kb.ChunkInfo{}, false
	}
	return kb.ChunkInfo{Doc: doc, Ord: ord, Unit: "стр.", UnitFrom: 1, Text: t,
		Book: kb.BookRec{Title: "Проба", Year: 2026}}, true
}

// Из нескольких подтверждений берётся то, где стоят ОБА конца связи.
//
// Замер 02.09.2026: у связи «Go —использует→ Garbage collection» (186
// подтверждений) первым куском оказалась шпаргалка по приведению типов,
// где имя Go стоит само по себе, а сборки мусора нет вовсе.
func TestEvidencePicksChunkWithBothEnds(t *testing.T) {
	src := multiChunk{
		1: "Go Quick Start. Type conversion: a := 1, b := int64(a). Ничего про память.",
		2: "В Go сборка мусора освобождает память кучи автоматически, без ручного free.",
	}
	res := SearchResult{
		Entities: []FoundEntity{{Entity: Entity{Name: "Go", Type: "технология"}}},
		Relations: []FoundRelation{{
			Src: "Go", Dst: "сборка мусора", Type: "использует", Count: 186,
			Evidence:  ChunkKey{Doc: 1, Ord: 1},
			Evidences: []ChunkKey{{Doc: 1, Ord: 1}, {Doc: 1, Ord: 2}},
		}},
	}
	out := Render(src, res, RenderOpts{Collection: "books"})

	if !strings.Contains(out, "освобождает память кучи") {
		t.Fatalf("взят не тот кусок — в выдаче нет фразы про связь:\n%s", out)
	}
	if strings.Contains(out, "Type conversion") {
		t.Fatalf("взят первый кусок, где второго конца связи нет:\n%s", out)
	}
}

// Ни в одном подтверждении обоих концов нет — показывается первое годное,
// а не пустота: половина сведений лучше молчания.
func TestEvidenceFallsBackToFirst(t *testing.T) {
	src := multiChunk{1: "Go компилируется в один файл, и это удобно при поставке."}
	res := SearchResult{
		Entities: []FoundEntity{{Entity: Entity{Name: "Go", Type: "технология"}}},
		Relations: []FoundRelation{{
			Src: "Go", Dst: "сборка мусора", Type: "использует", Count: 3,
			Evidence:  ChunkKey{Doc: 1, Ord: 1},
			Evidences: []ChunkKey{{Doc: 1, Ord: 1}},
		}},
	}
	out := Render(src, res, RenderOpts{Collection: "books"})
	if !strings.Contains(out, "компилируется в один файл") {
		t.Fatalf("запасной кусок не показан:\n%s", out)
	}
}
