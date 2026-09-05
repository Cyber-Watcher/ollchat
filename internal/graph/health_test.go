package graph

import (
	"os"
	"strings"
	"testing"
)

// Порог отставшей разметки — тот же, что у доктора: десятая часть понятий.
func TestRepartitionOverdue(t *testing.T) {
	cases := []struct {
		now, builtAt int
		want         bool
		why          string
	}{
		{161239, 62805, true, "граф вырос в 2.5 раза — обзор видит треть (замер 02.09.2026)"},
		{161239, 161239, false, "разметка свежая"},
		{100, 91, false, "9% прироста — ещё рано"},
		{100, 90, true, "ровно десятая часть — пора"},
		{100, 0, false, "тем нет вовсе — это другой случай, о нём свой совет"},
		{0, 0, false, "графа нет"},
		{50, 100, false, "понятий стало меньше — такого не бывает, но падать нельзя"},
	}
	for _, c := range cases {
		if got := repartitionOverdue(c.now, c.builtAt); got != c.want {
			t.Errorf("repartitionOverdue(%d, %d) = %v, ожидалось %v — %s",
				c.now, c.builtAt, got, c.want, c.why)
		}
	}
}

// Совет обязан быть готовой командой, а не намёком.
func TestAdviceNamesCommands(t *testing.T) {
	h := Health{Entities: 161239, Vectors: 150000, Chunks: 464767, Covered: 158535, TopicsBuiltAt: 62805}
	adv := h.Advice("books")
	if len(adv) != 3 {
		t.Fatalf("советов %d, ожидалось три (разбор, смыслы, темы):\n%s", len(adv), strings.Join(adv, "\n"))
	}
	for _, a := range adv {
		if !strings.Contains(a, "ollchat --") {
			t.Errorf("совет без команды: %q", a)
		}
	}
	// Первым идёт разбор: он необратим, остальное досчитывается когда угодно.
	// Команду при этом называем не прямую, а доктора: назвать каталоги при
	// запуске нельзя — это проход по всем кускам коллекции, а приветствие
	// обязано быть мгновенным.
	if !strings.Contains(adv[0], "граф собран по") {
		t.Errorf("первым должен идти охват разбора: %q", adv[0])
	}
}

// Всё в порядке — молчим. Предупреждение, которое горит всегда, перестают читать.
func TestAdviceSilentWhenHealthy(t *testing.T) {
	h := Health{Entities: 1000, Vectors: 1000, Chunks: 500, Covered: 500, TopicsBuiltAt: 1000}
	if adv := h.Advice("books"); len(adv) != 0 {
		t.Errorf("на здоровом графе выданы советы: %v", adv)
	}
}

// Графа нет — советовать нечего, а не «соберите граф»: он не обязателен.
func TestAdviceQuietWithoutGraph(t *testing.T) {
	if adv := (Health{}).Advice("books"); len(adv) != 0 {
		t.Errorf("без графа выданы советы: %v", adv)
	}
}

// Число понятий на момент разметки читается из начала файла тем.
//
// Файл весит десятки мегабайт, и читать его целиком при каждом запуске нельзя.
// Первая редакция дописывала скобки к первому килобайту и получала огрызок:
// килобайт кончается внутри списка тем. Ошибка была тихой — программа говорила
// «темы не размечены» там, где их тридцать четыре тысячи.
func TestTopicsBuiltAtReadsHeadOnly(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/communities.json"

	// Такой же формат, как пишет saveCommunities: отступы, паспорт, потом список.
	var b strings.Builder
	b.WriteString("{\n \"built\": \"2026-09-02T07:36:36+03:00\",\n \"carry\": {\n" +
		"  \"Carried\": 1006,\n  \"Fresh\": 31793,\n  \"Lost\": 1584\n },\n" +
		" \"entities\": 161239,\n \"edges\": 820760,\n \"list\": [\n")
	for i := 0; i < 500; i++ { // список заведомо длиннее любого читаемого начала
		b.WriteString("  {\n   \"id\": 0,\n   \"members\": [1, 2, 3]\n  },\n")
	}
	b.WriteString(" ]\n}\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := topicsBuiltAt(path); got != 161239 {
		t.Errorf("вышло %d, ожидалось 161239 — паспорт разбиения не прочитан", got)
	}
}

// Файла нет или он не тот — молчим, а не врём числом.
func TestTopicsBuiltAtQuietOnGarbage(t *testing.T) {
	dir := t.TempDir()
	if got := topicsBuiltAt(dir + "/нет-такого.json"); got != 0 {
		t.Errorf("на отсутствующем файле вышло %d", got)
	}
	bad := dir + "/bad.json"
	if err := os.WriteFile(bad, []byte("это не json вовсе"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := topicsBuiltAt(bad); got != 0 {
		t.Errorf("на мусоре вышло %d", got)
	}
}

// Доктор обязан показывать живые понятия, а не записи реестра.
//
// Склейка двойников лежит отдельным журналом и надевается при чтении, поэтому
// запись из реестра не исчезает. До 02.09.2026 сводка показывала только реестр
// и после склейки 2693 двойников уверяла, что понятий столько же, сколько было.
func TestStatsLiveExcludesMerged(t *testing.T) {
	st := Stats{Merged: 2693}
	st.Entities = 161239
	if got := st.Live(); got != 158546 {
		t.Fatalf("живых понятий: ожидалось 158546, получено %d", got)
	}
	// Без склеек живое число совпадает с реестром — прежнее поведение.
	none := Stats{}
	none.Entities = 100
	if got := none.Live(); got != 100 {
		t.Fatalf("без склеек живое число обязано равняться реестру, получено %d", got)
	}
}

// Раздутый реестр обязан попадать в советы: он не портит граф, но каждый
// запуск платит за него десятками секунд, и заметить это иначе нечем.
func TestAdviceOnBloatedRegistry(t *testing.T) {
	h := Health{Entities: 161239, Vectors: 161239, Chunks: 100, Covered: 100,
		RegistryBytes: 499 << 20}
	adv := h.Advice("books")
	found := false
	for _, a := range adv {
		if strings.Contains(a, "--graph-compact") {
			found = true
		}
	}
	if !found {
		t.Fatalf("совет уплотнить реестр не выдан: %v", adv)
	}
}

// Уплотнённый реестр советов не вызывает: сторож, кричащий без повода,
// перестаёт читаться.
func TestNoAdviceOnCompactRegistry(t *testing.T) {
	h := Health{Entities: 161239, Vectors: 161239, Chunks: 100, Covered: 100,
		RegistryBytes: 25 << 20}
	for _, a := range h.Advice("books") {
		if strings.Contains(a, "--graph-compact") {
			t.Fatalf("совет выдан на уплотнённом реестре: %q", a)
		}
	}
}

// Опытный граф неполон по замыслу: советов о нём быть не должно, иначе сторож
// начнёт кричать не по делу и его перестанут читать.
func TestExperimentalGraphIsSilent(t *testing.T) {
	h := Health{Kind: KindExperimental, Entities: 1000, Vectors: 0,
		Chunks: 100000, Covered: 3000, RegistryBytes: 499 << 20}
	if adv := h.Advice("books"); len(adv) != 0 {
		t.Fatalf("опытный граф выдал советы: %v", adv)
	}
}

// А рабочий на тех же числах обязан заговорить.
func TestProductionGraphStillSpeaks(t *testing.T) {
	h := Health{Kind: KindProduction, Entities: 1000, Vectors: 0,
		Chunks: 100000, Covered: 3000, RegistryBytes: 499 << 20}
	if adv := h.Advice("books"); len(adv) == 0 {
		t.Fatal("рабочий граф промолчал на неполноте и отсутствии векторов")
	}
}
