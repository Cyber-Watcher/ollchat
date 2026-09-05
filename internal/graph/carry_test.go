package graph

import "testing"

// members собирает список понятий от a до b включительно.
func members(a, b uint32) []uint32 {
	out := make([]uint32, 0, b-a+1)
	for i := a; i <= b; i++ {
		out = append(out, i)
	}
	return out
}

// Тема, состав которой почти не изменился, сохраняет описание.
//
// Ради этого перенос и делался: замер 27.08.2026 показал, что при пересчёте
// на выросшем графе 89% тем сохранили бы почти прежний состав, то есть девять
// описаний из десяти переписывались бы заново без нужды — полтора часа карты.
func TestCarryKeepsDescriptionWhenMembersMatch(t *testing.T) {
	old := &Communities{List: []Community{{
		ID: 1, Level: 0, Members: members(1, 20),
		Title: "Векторный поиск", Summary: "о поиске", Rating: 9, Why: "основа",
		Findings: []Finding{{Title: "вывод", Text: "пояснение"}},
	}}}
	// Новая тема: те же двадцать понятий плюс два новых — сходство 20/22 = 0.91.
	fresh := &Communities{List: []Community{{ID: 7, Level: 0, Members: members(1, 22)}}}

	res := carryDescriptions(old, fresh, 0)
	if res.Carried != 1 {
		t.Fatalf("перенесено %d описаний, ожидалось 1 (%+v)", res.Carried, res)
	}
	got := fresh.List[0]
	if got.Title != "Векторный поиск" || got.Rating != 9 || len(got.Findings) != 1 {
		t.Fatalf("описание перенесено не целиком: %+v", got)
	}
	if got.CarriedFrom != 1 || got.CarriedSim < 0.9 {
		t.Errorf("пометка о переносе не проставлена: from=%d sim=%.2f",
			got.CarriedFrom, got.CarriedSim)
	}
}

// Тема, состав которой изменился сильно, описание не получает.
//
// Иначе обзор врал бы: резюме про одно, а понятия про другое.
func TestCarrySkipsWhenMembersDiffer(t *testing.T) {
	old := &Communities{List: []Community{{
		ID: 1, Level: 0, Members: members(1, 20), Title: "Векторный поиск", Rating: 9,
	}}}
	// Общих понятий пять из двадцати пяти: сходство 5/40 = 0.125.
	fresh := &Communities{List: []Community{{ID: 7, Level: 0, Members: members(16, 40)}}}

	res := carryDescriptions(old, fresh, 0)
	if res.Carried != 0 {
		t.Fatalf("описание перенесено на непохожую тему: %+v", fresh.List[0])
	}
	if res.Lost != 1 {
		t.Errorf("потерянных описаний %d, ожидалось 1", res.Lost)
	}
}

// Распавшаяся тема отдаёт описание только одной половине — той, что похожа
// сильнее. Иначе две разные темы получили бы одно название.
func TestCarryGivesDescriptionOnce(t *testing.T) {
	old := &Communities{List: []Community{{
		ID: 1, Level: 0, Members: members(1, 20), Title: "Общая тема", Rating: 8,
	}}}
	fresh := &Communities{List: []Community{
		{ID: 10, Level: 0, Members: members(1, 16)}, // сходство 16/20 = 0.80
		{ID: 11, Level: 0, Members: members(1, 18)}, // сходство 18/20 = 0.90 — сильнее
	}}

	res := carryDescriptions(old, fresh, 0)
	if res.Carried != 1 {
		t.Fatalf("перенесено %d, ожидался ровно один перенос", res.Carried)
	}
	if fresh.List[1].Title == "" {
		t.Error("описание должно достаться более похожей теме (#11)")
	}
	if fresh.List[0].Title != "" {
		t.Error("менее похожая тема не должна получать чужое описание")
	}
}

// Порог настраиваемый: при строгом пороге не переносится ничего.
func TestCarryThresholdRespected(t *testing.T) {
	old := &Communities{List: []Community{{
		ID: 1, Level: 0, Members: members(1, 20), Title: "Тема", Rating: 7,
	}}}
	fresh := &Communities{List: []Community{{ID: 5, Level: 0, Members: members(1, 22)}}}

	if res := carryDescriptions(old, fresh, 0.95); res.Carried != 0 {
		t.Fatalf("при пороге 0.95 сходство 0.91 переносить нельзя: %+v", res)
	}
	if res := carryDescriptions(old, fresh, 0.5); res.Carried != 1 {
		t.Fatalf("при пороге 0.5 сходство 0.91 переносить нужно: %+v", res)
	}
}

// Темы без описания в счёт не идут: переносить с них нечего.
func TestCarryIgnoresUndescribed(t *testing.T) {
	old := &Communities{List: []Community{
		{ID: 1, Level: 0, Members: members(1, 20)},                // без названия
		{ID: 2, Level: 1, Members: members(1, 20), Title: "Верх"}, // объединение
	}}
	fresh := &Communities{List: []Community{{ID: 5, Level: 0, Members: members(1, 20)}}}

	res := carryDescriptions(old, fresh, 0)
	if res.Carried != 0 || res.Lost != 0 {
		t.Fatalf("темы без описания и объединения переносу не подлежат: %+v", res)
	}
	if res.Fresh != 1 {
		t.Errorf("новых тем без описания %d, ожидалась 1", res.Fresh)
	}
}
