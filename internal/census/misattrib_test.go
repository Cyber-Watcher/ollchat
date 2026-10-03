// Тесты общих частей переписей ложных приписываний: то, что можно проверить
// без коллекции и графа на диске — косинус, равномерная выборка, обрезка
// строк, формат списка кусков и текстовые признаки misattrib-acronym.
package census

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// Косинус считается по определению, не подгоняется под реализацию:
// ортогональные векторы дают 0, одинаковые — 1, противоположные — -1,
// нулевой вектор — 0 (делить не на что).
func TestCosineKnownVectors(t *testing.T) {
	orthoA := make([]int8, entVecDim)
	orthoB := make([]int8, entVecDim)
	orthoA[0], orthoB[1] = 5, 5
	if got := cosine(orthoA, orthoB); got != 0 {
		t.Errorf("ортогональные векторы: cos = %v, ожидался 0", got)
	}

	same := make([]int8, entVecDim)
	same[0], same[1], same[2] = 3, -4, 5
	if got := cosine(same, same); absFloat(got-1) > 1e-9 {
		t.Errorf("вектор с самим собой: cos = %v, ожидался 1", got)
	}

	a := make([]int8, entVecDim)
	b := make([]int8, entVecDim)
	a[0], a[1] = 7, -2
	b[0], b[1] = -7, 2
	if got := cosine(a, b); absFloat(got-(-1)) > 1e-9 {
		t.Errorf("противоположные векторы: cos = %v, ожидался -1", got)
	}

	zero := make([]int8, entVecDim)
	nonzero := make([]int8, entVecDim)
	nonzero[0] = 1
	if got := cosine(zero, nonzero); got != 0 {
		t.Errorf("нулевой вектор: cos = %v, ожидался 0 (делить не на что)", got)
	}
}

func absFloat(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// Шаг равномерной выборки — n/limit с округлением ВВЕРХ, когда n больше
// предела, иначе 1 (перебор всех без урезания). Прежний тест закреплял
// деление вниз (601/600 = 1, 1250/600 = 2), при котором выборка превышала
// предел почти вдвое; проверка переписана 03.10.2026 вместе с кодом.
func TestEvenStep(t *testing.T) {
	cases := []struct{ n, limit, want int }{
		{0, 600, 1},
		{600, 600, 1}, // ровно предел — урезать нечего
		{599, 600, 1},
		{601, 600, 2},
		{1199, 600, 2},
		{1250, 600, 3},
		{1, 8, 1},
		{50, 0, 1},  // предела нет — не делить на ноль
		{50, -1, 1}, // отрицательным предел выключают
	}
	for _, c := range cases {
		if got := evenStep(c.n, c.limit); got != c.want {
			t.Errorf("evenStep(%d, %d) = %d, ожидалось %d", c.n, c.limit, got, c.want)
		}
	}
}

// Главное обещание выборки — «не больше limit»: проверяется по существу,
// перебором, а не по таблице шагов.
func TestEvenStepNeverExceedsLimit(t *testing.T) {
	for _, limit := range []int{1, 7, 600} {
		for n := 0; n <= 4*limit+3; n++ {
			step, taken := evenStep(n, limit), 0
			for i := 0; i < n; i += step {
				taken++
			}
			if taken > limit {
				t.Fatalf("n=%d, limit=%d: шаг %d даёт %d элементов", n, limit, step, taken)
			}
		}
	}
}

// Обрезка по рунам, не по байтам: многобайтовая буква не режется пополам.
func TestCutRuneSafe(t *testing.T) {
	s := "определение" // 11 рун
	if got := cut(s, 20); got != s {
		t.Errorf("короче предела — обрезки быть не должно: %q", got)
	}
	got := cut(s, 5)
	wantRunes := []rune(s)[:5]
	if got != string(wantRunes)+"…" {
		t.Errorf("cut(%q, 5) = %q, ожидалось %q", s, got, string(wantRunes)+"…")
	}
	if n := len([]rune(got)); n != 6 { // 5 рун текста + многоточие
		t.Errorf("обрезанная строка содержит %d рун, ожидалось 6", n)
	}
}

func TestCutListLimitsCountAndWidth(t *testing.T) {
	in := []string{"один", "два", "три", "четыре", "пять", "шесть"}
	got := cutList(in, 4)
	if len(got) != 4 {
		t.Fatalf("cutList вернул %d элементов, ожидалось 4", len(got))
	}
	for i, s := range got {
		if s != in[i] {
			t.Errorf("элемент %d изменился без нужды: %q вместо %q", i, s, in[i])
		}
	}
	long := []string{strings.Repeat("а", 30)}
	got2 := cutList(long, 4)
	if r := []rune(got2[0]); len(r) != 19 { // 18 рун + многоточие
		t.Errorf("длинный элемент cutList не обрезан до 18 рун: %d рун", len(r))
	}
}

// Вектор понятия читается по номеру: id=1 — с нулевого байта, границы —
// отказ, а не чтение чужой памяти.
func TestEntityVectorAt(t *testing.T) {
	raw := make([]byte, entVecDim*2) // ровно два понятия
	var minusOne int8 = -1
	for i := 0; i < entVecDim; i++ {
		raw[i] = byte(int8(1))            // понятие 1: все байты 1
		raw[entVecDim+i] = byte(minusOne) // понятие 2: все байты -1
	}
	v1, ok := entityVectorAt(raw, 1)
	if !ok || len(v1) != entVecDim || v1[0] != 1 {
		t.Fatalf("понятие 1: ok=%v len=%d v[0]=%v", ok, len(v1), v1[0])
	}
	v2, ok := entityVectorAt(raw, 2)
	if !ok || v2[0] != -1 {
		t.Fatalf("понятие 2: ok=%v v[0]=%v", ok, v2[0])
	}
	if _, ok := entityVectorAt(raw, 0); ok {
		t.Error("id=0 обязан отказать")
	}
	if _, ok := entityVectorAt(raw, 3); ok {
		t.Error("векторов ещё не досчитано до понятия 3 — обязан отказать")
	}
	if _, ok := entityVectorAt(nil, 1); ok {
		t.Error("пустой файл векторов — обязан отказать, а не читать чужую память")
	}
}

// Список кусков пишется отсортированным и в формате --graph-forget-chunks:
// первое поле строки — «книга#кусок», распознаваемое тем же graph.ParseChunkKey,
// которым его читает загрузчик списка (internal/graph/maint/forgetlist.go).
func TestWriteForgetListFormatAndSort(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		"9#3\t#5 понятие Б ← ложный синоним «X»",
		"1#1\t#5 понятие А ← ложный синоним «X»",
		"1#20\t#5 понятие А ← ложный синоним «X»",
	}
	if err := ensureOutDir(dir); err != nil {
		t.Fatalf("ensureOutDir: %v", err)
	}
	if err := writeForgetList(dir, "list.txt", lines); err != nil {
		t.Fatalf("writeForgetList: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "list.txt"))
	if err != nil {
		t.Fatalf("файл не записан: %v", err)
	}
	got := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	want := []string{
		"1#1\t#5 понятие А ← ложный синоним «X»",
		"1#20\t#5 понятие А ← ложный синоним «X»",
		"9#3\t#5 понятие Б ← ложный синоним «X»",
	}
	if len(got) != len(want) {
		t.Fatalf("строк %d, ожидалось %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("строка %d: %q, ожидалось %q (сортировка по алфавиту, не по числу)", i, got[i], want[i])
		}
	}
	for _, l := range got {
		first := strings.Fields(l)[0]
		if _, err := graph.ParseChunkKey(first); err != nil {
			t.Errorf("первое поле строки %q не разбирается как номер куска: %v", l, err)
		}
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Error("файл обязан кончаться переводом строки")
	}
}

// Без -out (пустой каталог) писать нечего и некуда: ни файла, ни отказа.
func TestWriteForgetListSkipsWithoutOutDir(t *testing.T) {
	if err := writeForgetList("", "list.txt", []string{"1#1\tчто-то"}); err != nil {
		t.Fatalf("без -out обязано молча пропустить запись, а не отказать: %v", err)
	}
}

func TestEnsureOutDirEmptyIsNoop(t *testing.T) {
	if err := ensureOutDir(""); err != nil {
		t.Fatalf("без -out каталог создавать не нужно: %v", err)
	}
}

// otherExpansion (misattrib-acronym) ищет ДРУГОЕ раскрытие тех же букв рядом
// с самим сокращением — «regular expressions (RE)» — и не путает его
// с уже известным длинным именем понятия.
func TestOtherExpansionFindsHomonymNotKnownName(t *testing.T) {
	longs := map[string]bool{"relation extraction": true}
	words := strings.Fields("go supports regular expressions re via the regexp package")
	got := otherExpansion(words, "re", longs)
	if got != "regular expressions" {
		t.Errorf("otherExpansion = %q, ожидалось «regular expressions»", got)
	}
	// Собственное длинное имя понятия (уже в longs) рядом с сокращением —
	// не «другое» раскрытие: otherExpansion сама пропускает фразы из longs,
	// иначе законное «relation extraction (RE)» попало бы в подозрительные.
	words2 := strings.Fields("relation extraction re stands for that")
	if got := otherExpansion(words2, "re", longs); got != "" {
		t.Errorf("otherExpansion нашла известную длинную форму как «другую»: %q", got)
	}
	// Сокращения рядом вовсе нет — отказ, а не совпадение по одним первым буквам.
	words3 := strings.Fields("random early detection queueing without the letters nearby")
	if got := otherExpansion(words3, "re", longs); got != "" {
		t.Errorf("otherExpansion нашла %q без сокращения рядом — ложное совпадение", got)
	}
}

func TestLettersOnlyAndInitials(t *testing.T) {
	if got := lettersOnly("RE-2, привет!"); got != "re2привет" {
		t.Errorf("lettersOnly = %q", got)
	}
	if got := initials("Relation Extraction"); got != "re" {
		t.Errorf("initials(\"Relation Extraction\") = %q, ожидалось \"re\"", got)
	}
	// Дефис для initials — тоже разделитель слов (как для FieldsFunc внутри):
	// «K-fold Cross-Validation» — четыре слова, а не два через дефис.
	if got := initials("K-fold Cross-Validation"); got != "kfcv" {
		t.Errorf("initials с дефисами = %q, ожидалось \"kfcv\"", got)
	}
}
