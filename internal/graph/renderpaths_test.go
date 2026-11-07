package graph

import (
	"strings"
	"testing"
)

// Цепочки для тестов выдачи нескольких путей (этап 126, Я2): первая — через
// «канал», вторая — через «планировщик», третья делит с первой первый шаг.
func pathsForRender() [][]PathStep {
	return [][]PathStep{
		{
			{From: "горутина", To: "канал", Type: "использует", Evidence: ChunkKey{Doc: 1, Ord: 1}},
			{From: "канал", To: "рантайм", Type: "часть", Evidence: ChunkKey{Doc: 1, Ord: 2}, Back: true},
		},
		{
			{From: "горутина", To: "планировщик", Type: "связано", Evidence: ChunkKey{Doc: 2, Ord: 1}},
			{From: "планировщик", To: "рантайм", Type: "часть", Evidence: ChunkKey{Doc: 2, Ord: 2}},
		},
		{
			{From: "горутина", To: "канал", Type: "использует", Evidence: ChunkKey{Doc: 1, Ord: 1}},
			{From: "канал", To: "стек", Type: "связано", Evidence: ChunkKey{Doc: 2, Ord: 3}},
			{From: "стек", To: "рантайм", Type: "часть", Evidence: ChunkKey{Doc: 2, Ord: 4}},
		},
	}
}

func chunksForRender() yearChunks {
	return yearChunks{
		ChunkKey{Doc: 1, Ord: 1}.Pack(): 2019,
		ChunkKey{Doc: 1, Ord: 2}.Pack(): 2026,
		ChunkKey{Doc: 2, Ord: 1}.Pack(): 2024,
		ChunkKey{Doc: 2, Ord: 2}.Pack(): 2024,
		ChunkKey{Doc: 2, Ord: 3}.Pack(): 2025,
		ChunkKey{Doc: 2, Ord: 4}.Pack(): 2025,
	}
}

// Одна цепочка — побайтно прежний вид RenderPath (graph.path_count = 1 выдачу
// не меняет). Образец выписан по строкам формата RenderPath до этапа 126:
// правка, что сдвинет хоть пробел, упадёт здесь, а не у модели.
func TestRenderPathsSingleIsOldText(t *testing.T) {
	src, opt := chunksForRender(), RenderOpts{Collection: "books"}
	first := pathsForRender()[0]
	want := "Путь из 2 шагов:\n" +
		"  1. горутина —использует→ канал\n" +
		"     подтверждение: Книга · 2019 г. · id=books/1#1\n" +
		"  2. канал ←часть— рантайм\n" +
		"     подтверждение: Книга · 2026 г. · id=books/1#2\n"
	if got := RenderPath(src, "горутина", "рантайм", first, true, opt); got != want {
		t.Fatalf("RenderPath изменил вид:\n%s\nожидалось:\n%s", got, want)
	}
	if got := RenderPaths(src, "горутина", "рантайм", [][]PathStep{first}, true, opt); got != want {
		t.Fatalf("одна цепочка напечатана не как RenderPath:\n%s\nожидалось:\n%s", got, want)
	}
	// Пустые случаи — те же строки, что у RenderPath: нет связи и одно понятие.
	for _, ok := range []bool{false, true} {
		if got, old := RenderPaths(src, "X", "Y", nil, ok, opt), RenderPath(src, "X", "Y", nil, ok, opt); got != old {
			t.Fatalf("ok=%v: %q, ожидалось %q", ok, got, old)
		}
	}
}

// Несколько цепочек: первая — как RenderPath, остальные под ней с подписью,
// подтверждение уже показанного шага не повторяется.
func TestRenderPathsShowsOtherPaths(t *testing.T) {
	src, opt := chunksForRender(), RenderOpts{Collection: "books"}
	paths := pathsForRender()
	got := RenderPaths(src, "горутина", "рантайм", paths, true, opt)

	first := RenderPath(src, "горутина", "рантайм", paths[0], true, opt)
	if !strings.HasPrefix(got, first) {
		t.Fatalf("первая цепочка напечатана не как RenderPath:\n%s", got)
	}
	for _, want := range []string{
		"\nДругой путь из 2 шагов:\n  1. горутина —связано→ планировщик\n",
		"\nДругой путь из 3 шагов:\n  1. горутина —использует→ канал\n     подтверждение — выше\n",
		"  2. канал —связано→ стек\n     подтверждение: Книга · 2025 г. · id=books/2#3\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("нет %q в выдаче:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "id=books/1#1"); n != 1 {
		t.Fatalf("подтверждение общего шага напечатано %d раз, ожидался 1:\n%s", n, got)
	}

	// Без кусков (карта понятий) подтверждений нет вовсе, и «выше» тоже.
	bare := RenderPaths(nil, "горутина", "рантайм", paths, true, opt)
	if strings.Contains(bare, "подтверждение") || strings.Count(bare, "Другой путь") != 2 {
		t.Fatalf("выдача без кусков:\n%s", bare)
	}
}
