package graph

import (
	"testing"
)

// Порядок поглощённых понятий повторяем от запуска к запуску.
//
// **Чем это стоило.** Список поглощённых собирался обходом карты, а он
// в каждом процессе свой. Порядок поглощённых становится порядком синонимов
// выжившего (withAbsorbed), синонимы идут в текст вектора понятия и тройки
// и обрезаются по graph.vector_aliases — значит от запуска к запуску менялись
// и текст, и сам СОСТАВ синонимов в векторе. Замер 24.09.2026 на рабочем
// графе: два запуска подряд на неизменных файлах дали 183 005 и 183 003
// тройки, разошлось 7 562 текста из 183 тысяч.
//
// Проверяется не «одинаково два раза подряд» (карта может случайно совпасть),
// а заданный порядок — по возрастанию номера.
func TestAbsorbedOrderIsStable(t *testing.T) {
	m := &Merges{to: map[uint32]uint32{}, from: map[uint32][]uint32{}}
	// Поглощённые записаны в журнал вперемешку — порядок журнала не должен
	// просачиваться в выдачу.
	for _, r := range []MergeRec{
		{From: 77, To: 5}, {From: 12, To: 5}, {From: 40, To: 5}, {From: 3, To: 5},
	} {
		m.recs = append(m.recs, r)
	}
	m.rebuild()

	got := m.Absorbed(5)
	want := []uint32{3, 12, 40, 77}
	if len(got) != len(want) {
		t.Fatalf("поглощённых %v, ожидалось %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("порядок поглощённых %v, ожидался %v", got, want)
		}
	}

	// Повторная сборка по тому же журналу обязана дать то же самое: иначе
	// «устаревшими» будут считаться векторы, которых никто не менял.
	for i := 0; i < 20; i++ {
		m.rebuild()
		again := m.Absorbed(5)
		for j := range want {
			if again[j] != want[j] {
				t.Fatalf("пересборка %d дала %v, ожидалось %v", i, again, want)
			}
		}
	}
}

// Сжатие цепочек не зависит от порядка обхода: A→B→C даёт A→C при любом
// порядке записей в журнале.
func TestMergeChainsCompressRegardlessOfOrder(t *testing.T) {
	for _, recs := range [][]MergeRec{
		{{From: 1, To: 2}, {From: 2, To: 3}, {From: 3, To: 4}},
		{{From: 3, To: 4}, {From: 2, To: 3}, {From: 1, To: 2}},
		{{From: 2, To: 3}, {From: 1, To: 2}, {From: 3, To: 4}},
	} {
		m := &Merges{to: map[uint32]uint32{}, from: map[uint32][]uint32{}}
		m.recs = append(m.recs, recs...)
		m.rebuild()
		for _, id := range []uint32{1, 2, 3} {
			if got := m.Resolve(id); got != 4 {
				t.Fatalf("журнал %+v: Resolve(%d) = %d, ожидалось 4", recs, id, got)
			}
		}
		got := m.Absorbed(4)
		want := []uint32{1, 2, 3}
		for i := range want {
			if i >= len(got) || got[i] != want[i] {
				t.Fatalf("журнал %+v: поглощённые %v, ожидались %v", recs, got, want)
			}
		}
	}
}
