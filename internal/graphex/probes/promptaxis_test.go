package probes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// TestRelBucket — тип связи, как его написала модель, попадает в одну из восьми
// корзин: семь названий graph.RelName и «не назван/неизвестный».
func TestRelBucket(t *testing.T) {
	if len(relBucketNames) != 8 || relBucketNames[0] != graph.RelName(graph.RelIs) ||
		relBucketNames[6] != graph.RelName(graph.RelRelated) || relBucketNames[7] != "не назван/неизвестный" {
		t.Fatalf("корзины %v: ожидались семь graph.RelName и «не назван/неизвестный»", relBucketNames)
	}
	cases := map[string]int{
		"является": 0, "is": 0, "часть": 1, "part": 1, "использует": 2, "влияет": 3,
		"противопоставлено": 4, "пример": 5,
		"связано": 6, "Связано": 6, // явное «связано» — не отказ модели назвать тип
		"": 7, "что-то своё": 7, "коррелирует": 7,
	}
	for raw, want := range cases {
		if got := relBucket(raw); got != want {
			t.Errorf("relBucket(%q) = %d, ожидалось %d", raw, got, want)
		}
	}
}

// promptFixtureChunks — три куска одной книги; номер куска стоит в тексте и по
// нему фиктивная модель выбирает ответ.
func promptFixtureChunks() []poolChunk {
	var out []poolChunk
	for ord := uint32(1); ord <= 3; ord++ {
		out = append(out, poolChunk{Key: graph.ChunkKey{Doc: 1, Ord: ord}, Title: "Книга", Unit: "глава", From: 1, To: 1,
			Text: fmt.Sprintf("фрагмент %d: alpha, beta, gamma", ord)})
	}
	return out
}

func chunkOrd(user string) int {
	for ord := 1; ord <= 3; ord++ {
		if strings.Contains(user, fmt.Sprintf("фрагмент %d:", ord)) {
			return ord
		}
	}
	return 0
}

// TestMeasurePrompt — сквозной прогон measurePrompt (тем же вызовом, что работа)
// на трёх кусках с заранее расписанными ответами. Пул — один рабочий: порядок
// запросов тогда известен (старый-1, старый-2, кандидат-1, кандидат-2 на каждом
// куске), и «n-й вызов» фиктивной модели однозначно называет прогон.
//
// Ответы (alpha, beta, gamma; тип в скобках, «?» — тип не назван):
//
//	кусок 1  старый-1: a→b (часть), b→g (является)       понятий 3
//	         старый-2: a→b (часть)                        понятий 2
//	         кандидат-1: b→a (часть), b→g (является)      понятий 3
//	         кандидат-2: b→a (часть), b→g (?)             понятий 3
//	кусок 2  старый-1, старый-2, кандидат-1: a→b (является), понятий 2;
//	         кандидат-2: связей нет, понятий 2
//	кусок 3  старый-1, старый-2: g→a (использует); кандидат-1: a→g (использует),
//	         понятий 2; кандидат-2: сбой запроса
func TestMeasurePrompt(t *testing.T) {
	al, be, ga := "alpha", "beta", "gamma"
	J := func(ents []string, rels ...[3]string) string { return factsJSON(t, ents, rels...) }
	r := func(s, d, ty string) [3]string { return [3]string{s, d, ty} }
	ab, abg, ag := []string{al, be}, []string{al, be, ga}, []string{al, ga}
	script := map[int]map[bool][]string{
		1: {
			false: {J(abg, r(al, be, "часть"), r(be, ga, "является")), J(ab, r(al, be, "часть"))},
			true:  {J(abg, r(be, al, "часть"), r(be, ga, "является")), J(abg, r(be, al, "часть"), r(be, ga, "что-то своё"))},
		},
		2: {
			false: {J(ab, r(al, be, "является")), J(ab, r(al, be, "является"))},
			true:  {J(ab, r(al, be, "является")), J(ab)},
		},
		3: {
			false: {J(ag, r(ga, al, "использует")), J(ag, r(ga, al, "использует"))},
			true:  {J(ag, r(al, ga, "использует")), "СБОЙ"},
		},
	}
	var mu sync.Mutex
	nth := map[string]int{}
	const candidate = "ПРОМПТ-КАНДИДАТ"
	ex := &fakeExtractor{candidate: candidate}
	ex.answer = func(isCand bool, user string) (string, error) {
		ord := chunkOrd(user)
		key := fmt.Sprintf("%v/%d", isCand, ord)
		mu.Lock()
		n := nth[key]
		nth[key]++
		mu.Unlock()
		ans := script[ord][isCand][n]
		if ans == "СБОЙ" {
			return "", errors.New("сбой фиктивной модели")
		}
		return ans, nil
	}

	var out, tsv, triples bytes.Buffer
	err := measurePrompt(context.Background(), &out, promptFixtureChunks(), ex, promptOpts{
		Candidate: candidate, Workers: 1, TSV: &tsv, Triples: &triples,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if n := ex.oldCalls.Load(); n != 6 {
		t.Errorf("запросов старым промптом %d, ожидалось 6 (3 куска × 2)", n)
	}
	if n := ex.candCalls.Load(); n != 6 {
		t.Errorf("запросов кандидатом %d, ожидалось 6 (3 куска × 2)", n)
	}

	// Итоги прогонов — по расписанию ответов выше. Корзины: является, часть,
	// использует, влияет, противопоставлено, пример, связано, не назван/неизвестный.
	wantStat := []promptStat{
		// старый-1: понятий 3+2+2, связей 2+1+1; является — b→g и a→b куска 2, часть — a→b, использует — g→a.
		{Answered: 3, Entities: 7, Relations: 4, ByBucket: []int{2, 1, 1, 0, 0, 0, 0, 0}},
		// старый-2: понятий 2+2+2, связей 1+1+1.
		{Answered: 3, Entities: 6, Relations: 3, ByBucket: []int{1, 1, 1, 0, 0, 0, 0, 0}},
		// кандидат-1: понятий 3+2+2, связей 2+1+1; то же распределение, что у старого-1.
		{Answered: 3, Entities: 7, Relations: 4, ByBucket: []int{2, 1, 1, 0, 0, 0, 0, 0}},
		// кандидат-2: кусок 3 — сбой; понятий 3+2, связей 2+0: часть и один «не назван».
		{Answered: 2, Failed: 1, Entities: 5, Relations: 2, ByBucket: []int{0, 1, 0, 0, 0, 0, 0, 1}},
	}
	for run, want := range wantStat {
		var block bytes.Buffer
		printPromptStat(&block, promptRunNames[run], want)
		if !strings.Contains(got, block.String()) {
			t.Errorf("в выводе нет блока прогона %s:\n%s\nвесь вывод:\n%s", promptRunNames[run], block.String(), got)
		}
	}
	for _, line := range []string{
		"  связей «является»: 2 (50.0%)",              // старый-1: 2 из 4
		"  доля «является»+«часть»: 75.0%",            // старый-1: (2+1) из 4
		"  доля «является»+«часть»: 50.0%",            // кандидат-2: (0+1) из 2
		"  связей «не назван/неизвестный»: 1 (50.0%)", // кандидат-2
		"  кусков с ответом: 2",
		"  сбоев запроса или разбора: 1",
	} {
		if !strings.Contains(got, line) {
			t.Errorf("в выводе нет строки %q:\n%s", line, got)
		}
	}

	// Жаккар: куски, где ответили все четыре прогона, — 1 и 2 (на куске 3 упал
	// кандидат-2). Значения — средние по двум кускам из наборов выше:
	//   старый-1/старый-2:   имена (2/3+1)/2, связи (1/2+1)/2, тройки (1/2+1)/2
	//   кандидат-1/кандидат-2: имена 1,       связи (1/3+0)/2, тройки (1/3+0)/2
	//   старый-1/кандидат-1:   имена 1,       связи 1,         тройки (1/3+1)/2 (общая тройка b→g)
	//   старый-2/кандидат-2:   имена (2/3+1)/2, связи (1/2+0)/2, тройки 0 (направления разные)
	wantJac := map[[2]int]promptJac{
		{0, 1}: {Names: 5.0 / 6, Pairs: 3.0 / 4, Triples: 3.0 / 4},
		{2, 3}: {Names: 1, Pairs: 1.0 / 6, Triples: 1.0 / 6},
		{0, 2}: {Names: 1, Pairs: 1, Triples: 2.0 / 3},
		{1, 3}: {Names: 5.0 / 6, Pairs: 1.0 / 4, Triples: 0},
	}
	if !strings.Contains(got, "устойчивость: кусков с ответами всех 4 прогонов: 2") {
		t.Errorf("в выводе нет числа кусков с ответами всех прогонов (2):\n%s", got)
	}
	for _, p := range promptRunPairs {
		w := wantJac[p]
		a, b := promptRunNames[p[0]], promptRunNames[p[1]]
		for _, line := range []string{
			fmt.Sprintf("Жаккар имён понятий, %s против %s: %.3f", a, b, w.Names),
			fmt.Sprintf("Жаккар связей (пара имён и тип), %s против %s: %.3f", a, b, w.Pairs),
			fmt.Sprintf("Жаккар направленных троек, %s против %s: %.3f", a, b, w.Triples),
		} {
			if !strings.Contains(got, line) {
				t.Errorf("в выводе нет строки %q:\n%s", line, got)
			}
		}
	}
	if !strings.Contains(got, "первый сбой запроса или разбора: сбой фиктивной модели") {
		t.Errorf("в выводе нет первой ошибки запроса:\n%s", got)
	}

	rows := strings.Split(strings.TrimRight(tsv.String(), "\n"), "\n")
	if len(rows) != 1+3*promptRuns {
		t.Fatalf("строк TSV %d, ожидалось заголовок и 3 куска × 4 прогона = %d", len(rows), 1+3*promptRuns)
	}
	wantHead := "кусок\tпрогон\tпонятий\tсвязей\t" + strings.Join(relBucketNames, "\t")
	if rows[0] != wantHead {
		t.Errorf("заголовок TSV %q, ожидался %q", rows[0], wantHead)
	}
	for _, row := range []string{
		"1#1\tстарый-1\t3\t2\t1\t1\t0\t0\t0\t0\t0\t0",
		"1#1\tкандидат-2\t3\t2\t0\t1\t0\t0\t0\t0\t0\t1",
		"1#2\tкандидат-2\t2\t0\t0\t0\t0\t0\t0\t0\t0\t0",
		"1#3\tкандидат-2\tсбой" + strings.Repeat("\t", 1+len(relBucketNames)),
	} {
		found := false
		for _, have := range rows {
			if have == row {
				found = true
			}
		}
		if !found {
			t.Errorf("в TSV нет строки %q:\n%s", row, tsv.String())
		}
	}

	// Тройки: по строке на связь ответившего прогона — 4+3+4+2 связей из
	// расписания; сбой (кандидат-2 на куске 3) строк не даёт.
	trows := strings.Split(strings.TrimRight(triples.String(), "\n"), "\n")
	if len(trows) != 1+4+3+4+2 {
		t.Fatalf("строк троек %d, ожидалось заголовок и 13 связей:\n%s", len(trows), triples.String())
	}
	if trows[0] != "кусок\tпрогон\tисточник\tтип\tцель" {
		t.Errorf("заголовок троек %q", trows[0])
	}
	for _, row := range []string{
		"1#1\tстарый-1\talpha\tчасть\tbeta",
		"1#1\tкандидат-1\tbeta\tчасть\talpha",
		"1#1\tкандидат-2\tbeta\tне назван/неизвестный\tgamma",
		"1#3\tкандидат-1\talpha\tиспользует\tgamma",
	} {
		if !strings.Contains(triples.String(), row+"\n") {
			t.Errorf("в тройках нет строки %q:\n%s", row, triples.String())
		}
	}
	if strings.Contains(triples.String(), "1#3\tкандидат-2") {
		t.Errorf("прогон со сбоем дал строки троек:\n%s", triples.String())
	}
}

// TestOneLine — табуляция и перевод строки в имени не ломают TSV троек.
func TestOneLine(t *testing.T) {
	if got := oneLine(" Pod\tspec\n template "); got != "Pod spec template" {
		t.Errorf("oneLine = %q", got)
	}
}

// TestTallyPromptDirect — итог прогона на готовых ответах: сбой не считается
// ответом, в корзины попадает тип как он написан.
func TestTallyPromptDirect(t *testing.T) {
	f := graph.Facts{
		Entities:  []graph.FactEntity{{Name: "alpha"}, {Name: "beta"}},
		Relations: []graph.FactRelation{{Src: "alpha", Dst: "beta", Type: "часть"}, {Src: "beta", Dst: "alpha", Type: ""}},
	}
	answers := [][]poolAnswer{{{OK: true, Facts: f}}, {{Err: errors.New("x")}}}
	got := tallyPrompt(answers, 0)
	want := promptStat{Answered: 1, Failed: 1, Entities: 2, Relations: 2, ByBucket: []int{0, 1, 0, 0, 0, 0, 0, 1}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("итог %+v, ожидалось %+v", got, want)
	}
}

// TestMeasurePromptParallel — несколько рабочих разом и время ответа: запросов
// столько же, старый промпт — рабочий graph.SystemPrompt, и медленный кандидат
// виден в среднем времени.
func TestMeasurePromptParallel(t *testing.T) {
	const candidate = "ПРОМПТ-КАНДИДАТ"
	const fast, slow = 10 * time.Millisecond, 80 * time.Millisecond
	one := factsJSON(t, []string{"alpha", "beta"}, [3]string{"alpha", "beta", "часть"})
	ex := &fakeExtractor{candidate: candidate,
		answer: func(bool, string) (string, error) { return one, nil },
		delay: func(isCand bool) time.Duration {
			if isCand {
				return slow
			}
			return fast
		}}
	var out bytes.Buffer
	err := measurePrompt(context.Background(), &out, promptFixtureChunks(), ex, promptOpts{Candidate: candidate, Workers: 4})
	if err != nil {
		t.Fatal(err)
	}
	if ex.oldCalls.Load() != 6 || ex.candCalls.Load() != 6 || ex.defaultCalls.Load() != 6 {
		t.Errorf("запросов: старым %d, кандидатом %d, graph.SystemPrompt %d; ожидалось по 6",
			ex.oldCalls.Load(), ex.candCalls.Load(), ex.defaultCalls.Load())
	}
	v := secondsLines(t, out.String())
	if len(v) != promptRuns {
		t.Fatalf("строк со временем %d, ожидалось %d:\n%s", len(v), promptRuns, out.String())
	}
	for i := 0; i < 2; i++ {
		if v[i] < fast.Seconds()-0.005 || v[2+i] < slow.Seconds()-0.005 || v[2+i] <= v[i] {
			t.Errorf("времена %v: старые не меньше %v, кандидаты не меньше %v и медленнее старых", v, fast, slow)
			break
		}
	}
	if strings.Contains(out.String(), "первый сбой") {
		t.Errorf("сбоев нет, а в выводе первый сбой:\n%s", out.String())
	}
}

// TestMeasurePromptRefuses — пустой кандидат и нет кусков — ошибка до запросов.
func TestMeasurePromptRefuses(t *testing.T) {
	ex := &fakeExtractor{candidate: "x", answer: func(bool, string) (string, error) { return "", errors.New("не должно звать") }}
	var out bytes.Buffer
	if err := measurePrompt(context.Background(), &out, promptFixtureChunks(), ex, promptOpts{Candidate: " "}); err == nil {
		t.Error("пустой кандидат принят")
	}
	if err := measurePrompt(context.Background(), &out, nil, ex, promptOpts{Candidate: "x"}); err == nil {
		t.Error("пустой список кусков принят")
	}
	if n := ex.oldCalls.Load() + ex.candCalls.Load(); n != 0 {
		t.Errorf("до отказа ушло %d запросов", n)
	}
}

// TestRunStabilityPromptKeys — ключи оси проверяются до открытия коллекции (cfg ==
// nil упал бы паникой): -url обязателен без запасного, кандидат обязателен и не пуст.
func TestRunStabilityPromptKeys(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(empty, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const url = "http://llama.example:8080"
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"без -url", []string{"-only", "stability", "-axis", "prompt", "-prompt-file", empty}, "-url"},
		{"без -prompt-file", []string{"-only", "stability", "-axis", "prompt", "-url", url}, "-prompt-file"},
		{"нет файла", []string{"-only", "stability", "-axis", "prompt", "-url", url, "-prompt-file", filepath.Join(dir, "нет.txt")}, "промпт-кандидат"},
		{"пустой файл", []string{"-only", "stability", "-axis", "prompt", "-url", url, "-prompt-file", empty}, "пуст"},
		{"неизвестная ось называет prompt", []string{"-only", "stability", "-axis", "bogus"}, "prompt"},
	}
	for _, c := range cases {
		var buf bytes.Buffer
		err := Run(&buf, nil, "books", c.args)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: ошибка %v, ожидалось упоминание %q", c.name, err, c.want)
		}
	}
}
