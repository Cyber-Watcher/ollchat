package graph

import "testing"

// Два графа с разными правилами в одном процессе — входной билет этапа 90:
// у каждого свои правила, и они не мешают друг другу.
func TestTwoGraphsWithDifferentRulesInOneProcess(t *testing.T) {
	coll := t.TempDir()
	work, err := Create(coll, "books", 10, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer work.Close()
	lab, err := Create(coll, "books", 10, Rules{Name: "lab", StemMinLen: 1, Groups: GroupUnion, MergesOff: true})
	if err != nil {
		t.Fatal(err)
	}
	defer lab.Close()

	if work.Dir() == lab.Dir() {
		t.Fatalf("графы легли в один каталог: %s", work.Dir())
	}
	if r := work.Rules(); r.StemMinLen != DefaultStemMinLen || r.Groups != GroupOff || r.MergesOff {
		t.Fatalf("правила рабочего графа: %+v", r)
	}
	if r := lab.Rules(); r.StemMinLen != 1 || r.Groups != GroupUnion || !r.MergesOff || r.Name != "lab" {
		t.Fatalf("правила опытного графа: %+v", r)
	}

	// Правило действует в самом графе, а не в пакете: у опытного основа «и»
	// становится ключом (длина 1), у рабочего — нет (длина 3).
	if _, _, err := work.Entities().Add("ИИ", "технология"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := lab.Entities().Add("ИИ", "технология"); err != nil {
		t.Fatal(err)
	}
	if _, ok := work.Entities().byStem["и"]; ok {
		t.Fatal("рабочий граф: союз «и» стал ключом")
	}
	if _, ok := lab.Entities().byStem["и"]; !ok {
		t.Fatal("опытный граф: при длине 1 основа «и» обязана быть ключом")
	}
	if work.merges.off || !lab.merges.off {
		t.Fatal("выключатель склеек не дошёл до графа")
	}
}
