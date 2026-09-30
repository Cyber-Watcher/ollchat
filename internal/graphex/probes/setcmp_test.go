package probes

import "testing"

// TestMatchNamesBasic — жадное сопоставление по общему написанию: совпадение,
// пропуск (в одном списке лишнее), лишнее (в другом списке лишнее).
func TestMatchNamesBasic(t *testing.T) {
	a := []NameSet{
		{"go": true},
		{"python": true},
		{"rust": true},
	}
	b := []NameSet{
		{"go": true, "golang": true}, // синоним не мешает совпадению
		{"java": true},
	}
	matched, onlyA, onlyB := MatchNames(a, b)
	if matched != 1 {
		t.Fatalf("matched = %d, хочу 1", matched)
	}
	if len(onlyA) != 2 || onlyA[0] != 1 || onlyA[1] != 2 {
		t.Fatalf("onlyA = %v, хочу [1 2]", onlyA)
	}
	if len(onlyB) != 1 || onlyB[0] != 1 {
		t.Fatalf("onlyB = %v, хочу [1]", onlyB)
	}
}

// TestMatchNamesGreedyOneToOne — один элемент b занимает только одну пару:
// второй одинаковый элемент a остаётся без пары, а не совпадает повторно.
func TestMatchNamesGreedyOneToOne(t *testing.T) {
	a := []NameSet{{"x": true}, {"x": true}}
	b := []NameSet{{"x": true}}
	matched, onlyA, onlyB := MatchNames(a, b)
	if matched != 1 {
		t.Fatalf("matched = %d, хочу 1", matched)
	}
	if len(onlyA) != 1 || onlyA[0] != 1 {
		t.Fatalf("onlyA = %v, хочу [1]", onlyA)
	}
	if len(onlyB) != 0 {
		t.Fatalf("onlyB = %v, хочу []", onlyB)
	}
}

// TestMatchNamesEmpty — оба списка пусты: нулевой результат без паники.
func TestMatchNamesEmpty(t *testing.T) {
	matched, onlyA, onlyB := MatchNames(nil, nil)
	if matched != 0 || len(onlyA) != 0 || len(onlyB) != 0 {
		t.Fatalf("пустые списки дали matched=%d onlyA=%v onlyB=%v", matched, onlyA, onlyB)
	}
}

// TestMatchEdgesSwap — allowSwap решает, считать ли совпадением связь
// с переставленными концами: seqcheck сравнивает тройки без перестановки,
// detcheck — с ней (разные прогоны разбора могут поменять src/dst местами).
func TestMatchEdgesSwap(t *testing.T) {
	a := []EdgeSet{{Src: NameSet{"a": true}, Dst: NameSet{"b": true}, Type: "uses"}}
	b := []EdgeSet{{Src: NameSet{"b": true}, Dst: NameSet{"a": true}, Type: "uses"}}

	if matched, onlyA, onlyB := MatchEdges(a, b, false); matched != 0 || len(onlyA) != 1 || len(onlyB) != 1 {
		t.Fatalf("без allowSwap: matched=%d onlyA=%v onlyB=%v, хочу 0 [0] [0]", matched, onlyA, onlyB)
	}
	if matched, onlyA, onlyB := MatchEdges(a, b, true); matched != 1 || len(onlyA) != 0 || len(onlyB) != 0 {
		t.Fatalf("с allowSwap: matched=%d onlyA=%v onlyB=%v, хочу 1 [] []", matched, onlyA, onlyB)
	}
}

// TestMatchEdgesTypeMismatch — общие написания концов не спасают, если типы
// связи разные.
func TestMatchEdgesTypeMismatch(t *testing.T) {
	a := []EdgeSet{{Src: NameSet{"a": true}, Dst: NameSet{"b": true}, Type: "uses"}}
	b := []EdgeSet{{Src: NameSet{"a": true}, Dst: NameSet{"b": true}, Type: "extends"}}
	matched, onlyA, onlyB := MatchEdges(a, b, true)
	if matched != 0 || len(onlyA) != 1 || len(onlyB) != 1 {
		t.Fatalf("matched=%d onlyA=%v onlyB=%v, хочу 0 [0] [0]", matched, onlyA, onlyB)
	}
}

// TestNameSetFirst — first() не паникует на пустом наборе и отдаёт написание
// из непустого.
func TestNameSetFirst(t *testing.T) {
	if got := (NameSet{}).first(); got != "?" {
		t.Fatalf("first() пустого набора = %q, хочу \"?\"", got)
	}
	ns := NameSet{"go": true}
	if got := ns.first(); got != "go" {
		t.Fatalf("first() = %q, хочу \"go\"", got)
	}
}
