package maint

import (
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// Числа в объяснении должны сходиться между собой.
func TestMergeExplainCountsAgree(t *testing.T) {
	mi := mergeInfo{
		liveBooks: 386, liveChunks: 409708,
		delBooks: 15, delChunks: 18685,
		orphanChunk: 12277, physical: 440670,
		bytes: 1026700000, segments: 18, hasGraph: true,
	}
	if got := mi.freed(); got != 18685+12277 {
		t.Fatalf("к освобождению %d кусков, ожидалось %d", got, 18685+12277)
	}
	out := mi.explain("books")
	for _, want := range []string{
		"остаются только живые книги: 386",
		"15 книг, помеченных удалёнными",
		"12277 кусков книг, перечитанных заново",
		"18 сегментов сливаются в один",
		"ГРАФ ПОНЯТИЙ ПЕРЕСТАНЕТ ОТКРЫВАТЬСЯ",
		"отмены нет",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("в объяснении нет %q:\n%s", want, out)
		}
	}
}

// Именованный граф называется в объяснении наравне с рабочим, и сказано
// честно: каталоги остаются, недоступными становятся понятия и связи.
// До 07.10.2026 объяснение читало паспорт одного `graph` и про `graph-lab`
// молчало, а обещало «потеряется», хотя граф просто перестаёт открываться.
func TestMergeExplainNamesEveryGraph(t *testing.T) {
	mi := mergeInfo{
		liveBooks: 3, physical: 10, segments: 1, hasGraph: true,
		graphs: []namedGraph{
			{dir: "graph"},
			{dir: "graph-lab", meta: graph.Meta{Entities: 5, Edges: 7, Mentions: 9, Covered: 4, Model: "qwen"}},
		},
	}
	out := mi.explain("books")
	for _, want := range []string{
		"ГРАФ ПОНЯТИЙ ПЕРЕСТАНЕТ ОТКРЫВАТЬСЯ",
		"Каталоги графов (graph, graph-lab) останутся на месте",
		"graph-lab: недоступными станут 5 понятий, 7 связей, 9 упоминаний",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("в объяснении нет %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Потеряется") {
		t.Errorf("объяснение обещает потерю графа, а он остаётся на диске:\n%s", out)
	}
}

// Без графа про граф не говорится: пугать нечем, и лишнее предупреждение
// обесценивает настоящее.
func TestMergeExplainSilentWithoutGraph(t *testing.T) {
	out := mergeInfo{liveBooks: 3, physical: 10, segments: 1}.explain("proba")
	if strings.Contains(out, "ГРАФ") {
		t.Errorf("про граф сказано там, где графа нет:\n%s", out)
	}
	if !strings.Contains(out, "отмены нет") {
		t.Errorf("о необратимости не сказано:\n%s", out)
	}
}

// Отказ по графу не снимается подтверждением словом — только явным ключом.
//
// Иначе два вопроса превратились бы в способ обойти предохранитель: человек,
// набравший ДА дважды, чувствует себя разрешившим всё, а он разрешил уплотнение,
// а не потерю графа ценой десятков часов.
func TestMergeGraphRefusalNotWaivedByConfirmation(t *testing.T) {
	mi := mergeInfo{hasGraph: true, physical: 10}
	err := confirmMerge("books", mi, false, true) // yes=true — подтверждения пропущены
	if err == nil {
		t.Fatal("отказ по графу снялся ключом --kb-yes")
	}
	if !strings.Contains(err.Error(), "--kb-merge-force") {
		t.Errorf("в отказе не сказано, чем он снимается: %v", err)
	}
}

// В скрипте без --kb-yes уплотнение не начинается: спросить некого.
func TestMergeRefusesWithoutTerminal(t *testing.T) {
	err := confirmMerge("proba", mergeInfo{physical: 10}, false, false)
	if err == nil {
		t.Fatal("уплотнение пошло без подтверждения и без терминала")
	}
	if !strings.Contains(err.Error(), "--kb-yes") {
		t.Errorf("в отказе не сказано, как быть в скрипте: %v", err)
	}
}

func TestChunksWord(t *testing.T) {
	for n, want := range map[int]string{
		1: "1 кусок", 2: "2 куска", 5: "5 кусков", 11: "11 кусков",
		21: "21 кусок", 22: "22 куска", 114: "114 кусков", 30962: "30962 куска",
	} {
		if got := chunksWord(n); got != want {
			t.Errorf("chunksWord(%d) = %q, ожидалось %q", n, got, want)
		}
	}
}
