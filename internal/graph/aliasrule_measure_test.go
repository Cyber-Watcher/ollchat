package graph

// Замер правила usableAlias на эталоне и на реестре (этап 90, п. 6.3).
//
// Не проверка, а измерение: тесты печатают таблицы через t.Log и падают только
// на сломанном входе. Эталон — размеченный глазами набор из 120 синонимов частых
// понятий (перевод / выдумка / спорно, 03.09.2026). В репозитории его нет —
// путь к файлу передаётся в OLLCHAT_ALIASES_SAMPLE, без переменной замер
// пропускается. Реестр — entities.jsonl рабочего графа, только чтение;
// путь в OLLCHAT_GRAPH_DIR.
//
//	OLLCHAT_ALIASES_SAMPLE=<эталон.tsv> go test ./internal/graph/ -run UsableAliasOn -v
//	OLLCHAT_GRAPH_DIR=<каталог графа> go test ./internal/graph/ -run UsableAliasOnRegistry -v

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode"
)

// aliasBranch — какой ветвью usableAlias принимает или отвергает пару.
// Повторяет порядок ветвей самой функции; расхождение ловится в тесте.
func aliasBranch(norm, alias string) (usable bool, branch string) {
	nw, aw := strings.Fields(norm), strings.Fields(alias)
	switch {
	case len(nw) == 0 || len(aw) == 0:
		return false, "пусто"
	case acronymOf(norm, aw) || acronymOf(alias, nw):
		return true, "аббревиатура"
	}
	if a, b := lettersDigits(norm), lettersDigits(alias); a != "" && a == b {
		return true, "то же написание"
	}
	if subseqWords(nw, aw) || subseqWords(aw, nw) {
		return true, "раскрытие/уточнение"
	}
	if len(nw) == len(aw) {
		distinct, translated := 0, 0
		for i := range nw {
			if nw[i] == aw[i] {
				continue
			}
			distinct++
			if script(nw[i]) != script(aw[i]) {
				translated++
			}
		}
		if distinct == translated {
			return true, "перевод пословно"
		}
		return false, "ОТКАЗ: замена слова внутри алфавита"
	}
	if scriptOf(norm) != scriptOf(alias) && scriptOf(norm) != "mix" && scriptOf(alias) != "mix" {
		return false, "ОТКАЗ: разное число слов, стороны на разных алфавитах"
	}
	return false, "ОТКАЗ: разное число слов, тот же/смешанный алфавит"
}

// scriptOf — алфавит строки по буквам: lat, cyr, mix, other.
func scriptOf(s string) string {
	lat, cyr := 0, 0
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Latin, r):
			lat++
		case unicode.Is(unicode.Cyrillic, r):
			cyr++
		}
	}
	switch {
	case lat > 0 && cyr > 0:
		return "mix"
	case lat > 0:
		return "lat"
	case cyr > 0:
		return "cyr"
	}
	return "other"
}

// aliasSamplePath — путь к эталонному набору синонимов. Набор размечен руками
// и в репозитории не хранится; путь передаётся переменной окружения.
func aliasSamplePath() string { return os.Getenv("OLLCHAT_ALIASES_SAMPLE") }

type etalonRow struct {
	label, concept, alias, scripts, clash, inText string
}

func readEtalon(t *testing.T) []etalonRow {
	t.Helper()
	path := aliasSamplePath()
	if path == "" {
		t.Skip("эталона нет: путь задаётся в OLLCHAT_ALIASES_SAMPLE")
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		t.Skipf("эталона нет: %s", path)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var rows []etalonRow
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "label\t") || strings.TrimSpace(line) == "" {
			continue
		}
		c := strings.Split(line, "\t")
		if len(c) != 8 {
			t.Fatalf("строка эталона не из 8 столбцов: %q", line)
		}
		rows = append(rows, etalonRow{label: c[0], concept: c[1], alias: c[4], scripts: c[5], clash: c[6], inText: c[7]})
	}
	return rows
}

func TestUsableAliasOnEtalon(t *testing.T) {
	rows := readEtalon(t)
	if len(rows) == 0 {
		t.Skip("эталон пуст")
	}
	labels := []string{"перевод", "выдумка", "спорно"}
	type cell struct{ usable, rejected, inText, inTextUsable, inTextRejected int }
	byLabel := map[string]*cell{}
	byLabelBranch := map[string]map[string]int{}
	var lostTranslations, leakedFakes []string
	for _, r := range rows {
		norm, k := Normalize(r.concept), Normalize(r.alias)
		usable, branch := aliasBranch(norm, k)
		if usable != usableAlias(norm, k) {
			t.Fatalf("aliasBranch разошёлся с usableAlias на %q ← %q", r.concept, r.alias)
		}
		c := byLabel[r.label]
		if c == nil {
			c = &cell{}
			byLabel[r.label] = c
		}
		if byLabelBranch[r.label] == nil {
			byLabelBranch[r.label] = map[string]int{}
		}
		byLabelBranch[r.label][branch]++
		inText := r.inText == "да"
		if usable {
			c.usable++
		} else {
			c.rejected++
		}
		if inText {
			c.inText++
			if usable {
				c.inTextUsable++
			} else {
				c.inTextRejected++
			}
		}
		if r.label == "перевод" && !usable {
			lostTranslations = append(lostTranslations, fmt.Sprintf("%s ← %s [%s, in_text=%s]", r.concept, r.alias, branch, r.inText))
		}
		if r.label == "выдумка" && usable {
			leakedFakes = append(leakedFakes, fmt.Sprintf("%s ← %s [%s, in_text=%s]", r.concept, r.alias, branch, r.inText))
		}
	}
	t.Logf("эталон: %d пар", len(rows))
	t.Logf("%-8s %6s %6s %8s | in_text=да: %5s %8s %8s", "разметка", "всего", "ключ", "отсечён", "всего", "и ключ", "отсечён")
	for _, l := range labels {
		c := byLabel[l]
		if c == nil {
			continue
		}
		t.Logf("%-8s %6d %6d %8d | %17d %8d %8d", l, c.usable+c.rejected, c.usable, c.rejected, c.inText, c.inTextUsable, c.inTextRejected)
	}
	for _, l := range labels {
		var ks []string
		for b := range byLabelBranch[l] {
			ks = append(ks, b)
		}
		sort.Slice(ks, func(i, j int) bool { return byLabelBranch[l][ks[i]] > byLabelBranch[l][ks[j]] })
		for _, b := range ks {
			t.Logf("  %-8s %3d  %s", l, byLabelBranch[l][b], b)
		}
	}
	t.Logf("потерянные переводы (%d):", len(lostTranslations))
	for _, s := range lostTranslations {
		t.Logf("  · %s", s)
	}
	t.Logf("пропущенные выдумки (%d):", len(leakedFakes))
	for _, s := range leakedFakes {
		t.Logf("  · %s", s)
	}
}

func TestUsableAliasOnRegistry(t *testing.T) {
	dir := os.Getenv("OLLCHAT_GRAPH_DIR")
	if dir == "" {
		t.Skip("нужен OLLCHAT_GRAPH_DIR=<каталог графа> (только чтение entities.jsonl)")
	}
	f, err := os.Open(filepath.Join(dir, "entities.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// Реестр дозаписывается: последняя запись с данным id — действующая.
	type rec struct {
		ID      uint32   `json:"id"`
		Norm    string   `json:"norm"`
		Aliases []string `json:"aliases"`
	}
	last := map[uint32]rec{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var r rec
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil || r.ID == 0 {
			continue
		}
		last[r.ID] = r
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	total, usable, withAliases := 0, 0, 0
	branches := map[string]int{}
	pairScripts := map[string]int{} // отсечённые: алфавит имени→синонима
	for _, r := range last {
		if len(r.Aliases) > 0 {
			withAliases++
		}
		for _, a := range r.Aliases {
			k := Normalize(a)
			if k == "" {
				continue
			}
			total++
			ok, branch := aliasBranch(r.Norm, k)
			branches[branch]++
			if ok {
				usable++
			} else {
				pairScripts[scriptOf(r.Norm)+"→"+scriptOf(k)]++
			}
		}
	}
	t.Logf("понятий %d, с синонимами %d, синонимов %d, ключами поиска стали %d (%.1f%%), отсечено %d (%.1f%%)",
		len(last), withAliases, total, usable, 100*float64(usable)/float64(total), total-usable, 100*float64(total-usable)/float64(total))
	var ks []string
	for b := range branches {
		ks = append(ks, b)
	}
	sort.Slice(ks, func(i, j int) bool { return branches[ks[i]] > branches[ks[j]] })
	for _, b := range ks {
		t.Logf("  %7d (%5.1f%%)  %s", branches[b], 100*float64(branches[b])/float64(total), b)
	}
	var ps []string
	for p := range pairScripts {
		ps = append(ps, p)
	}
	sort.Slice(ps, func(i, j int) bool { return pairScripts[ps[i]] > pairScripts[ps[j]] })
	t.Logf("отсечённые по алфавитам имя→синоним:")
	for _, p := range ps {
		t.Logf("  %7d  %s", pairScripts[p], p)
	}
}
