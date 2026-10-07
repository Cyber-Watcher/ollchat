package maint

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// Порог пересчёта разметки: судим по понятиям вне тем, а не по перекроенным темам.
func TestRepartitionDue(t *testing.T) {
	cases := []struct {
		uncovered, entities int
		want                bool
		why                 string
	}{
		{101012, 161239, true, "63% графа вне тем — обзор работает по трети (замер 02.09.2026)"},
		{7733, 161239, false, "4% — свежая разметка, пересчёт дороже пользы"},
		{16124, 161239, true, "ровно десятая часть — порог сработал"},
		{16000, 161239, false, "чуть меньше десятой — ещё рано"},
		{0, 161239, false, "все понятия размечены"},
		{5, 0, false, "пустой граф не повод для пересчёта"},
		{0, 0, false, "ничего нет"},
	}
	for _, c := range cases {
		if got := repartitionDue(c.uncovered, c.entities); got != c.want {
			t.Errorf("repartitionDue(%d, %d) = %v, ожидалось %v — %s",
				c.uncovered, c.entities, got, c.want, c.why)
		}
	}
}

// Знаменатель — ЖИВЫЕ понятия, а не записи реестра (24.09.2026).
//
// Числа настоящие: на графе 24.09 живых понятий 306 947, записей реестра
// 339 092 (склейкой поглощено 32 145), вне тем 12 600. Деление на реестр
// давало 3 %, на живые — 4 %; порог 10 % ни в том, ни в другом случае
// не достигнут, но занижение отодвигало бы его на 10 % графа.
func TestRepartitionDenominatorIsLive(t *testing.T) {
	const registry, merged, uncovered = 339092, 32145, 12600
	live := registry - merged
	if 100*uncovered/registry != 3 || 100*uncovered/live != 4 {
		t.Fatalf("доли не те, на которых писан тест: реестр %d%%, живые %d%%",
			100*uncovered/registry, 100*uncovered/live)
	}
	// Сам порог на этих числах не срабатывает ни так, ни так — проверяем,
	// что разница знаменателей действительно меняет вердикт там, где она важна.
	if repartitionDue(uncovered, live) || repartitionDue(uncovered, registry) {
		t.Error("на числах 24.09 пересчёт не нужен ни по живым, ни по реестру")
	}
	// Граница: вне тем десятая часть ЖИВЫХ, с округлением вверх — доля
	// считается целочисленно, и ровно 10,0 % при делении нацело даёт 9.
	tenth := (live + 9) / 10
	if !repartitionDue(tenth, live) {
		t.Errorf("десятая часть живых (%d из %d) должна звать пересчёт", tenth, live)
	}
	if repartitionDue(tenth, registry) {
		t.Errorf("та же десятая часть, делённая на реестр (%d), порога не достигает — "+
			"это и есть занижение, из-за которого знаменатель исправлен", registry)
	}
}

// Понятия вне тем считаются по ЖИВЫМ, а не по всему реестру: поглощённое
// склейкой понятие не самостоятельный узел, и в темах его быть не должно.
// Дефект 15.09.2026: обход по All() после склейки 15 311 пар показал
// 33 059 понятий вне тем вместо 12 530 и звал пересчитывать разметку.
func TestCountUncoveredSkipsMerged(t *testing.T) {
	// Настоящий граф: понятие 4 поглощено склейкой понятием 1 и в темах не
	// числится — вне тем оно считаться не должно. До 18.09.2026 тест собирал
	// список понятий руками и проверял функцию на её же входе (тавтология,
	// аудит Б17); теперь список берётся у Live() настоящего графа.
	g, err := graph.Create(t.TempDir(), "проба", 100, graph.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	for _, n := range []string{"один", "два", "три", "четыре"} {
		if _, _, err := g.Entities().Add(n, graph.TypeConcept); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := g.Merges().Add([]graph.MergeRec{{From: 4, To: 1}}); err != nil {
		t.Fatal(err)
	}
	live := g.Entities().Live()
	if len(live) != 3 {
		t.Fatalf("живых понятий %d, ожидалось 3 (четвёртое поглощено)", len(live))
	}
	inTopic := map[uint32]bool{1: true, 2: true}

	if got := countUncovered(live, inTopic); got != 1 {
		t.Errorf("вне тем = %d, ожидалась 1 (только беспризорное понятие)", got)
	}
	// Пустая карта тем — вне тем все живые, но не больше их числа.
	if got := countUncovered(live, map[uint32]bool{}); got != 3 {
		t.Errorf("при пустой разметке вне тем = %d, ожидалось 3", got)
	}
	if got := countUncovered(nil, inTopic); got != 0 {
		t.Errorf("без понятий вне тем = %d, ожидался 0", got)
	}
}

// Массовый сбой шага с моделью — ошибка команды, а не «код 0» (этап 102):
// 15.09.2026 сервер лёг посреди описаний тем, 236 сбоев из 298, а докатка
// записала успех.
func TestTooManyFailures(t *testing.T) {
	if err := tooManyFailures("описания тем", 236, 298); err == nil {
		t.Fatal("236 сбоев из 298 сошли за успех")
	}
	if err := tooManyFailures("описания тем", 60, 298); err == nil {
		t.Fatal("каждый пятый сбой сошёл за успех")
	}
	for _, c := range [][2]int{{0, 298}, {3, 298}, {0, 0}} {
		if err := tooManyFailures("описания тем", c[0], c[1]); err != nil {
			t.Fatalf("%d сбоев из %d — обычный шум, а не беда: %v", c[0], c[1], err)
		}
	}
}

// lineWith — строка вывода, начинающаяся с prefix (без отступа); пусто, если нет.
func lineWith(out, prefix string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), prefix) {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

// Доктор, --graph-status и --graph-pending называют одни и те же числа,
// и отметки удалённых книг в них не входят (аудит 07.10.2026, раздел 4.6).
//
// До правки заголовок доктора брал все отметки журнала: на этой фикстуре —
// 68 отметок при 34 кусках хранилища, то есть «осталось 0» и ни одного совета
// разобрать остаток, хотя у живой книги разобраны 3 куска из 10. Статус
// вдобавок складывал «не разобрала модель» со «служебными» в одно «пропущено».
func TestDoctorCountsAgreeWithStatusAndPending(t *testing.T) {
	f := newMaintFixture(t)
	f.markMixed(t)

	total := f.keepChunks
	marked, pending := 3, total-2 // забытый кусок и в «разобрано», и в «осталось»
	var out bytes.Buffer
	if err := DoctorTo(&out, io.Discard, f.cfg, f.name); err != nil {
		t.Fatal(err)
	}
	doctor := out.String()
	checks := []struct{ got, want string }{
		{lineWith(doctor, "разобрано кусков"),
			fmt.Sprintf("разобрано кусков %d из %d (%d%%), осталось %d", marked, total, 100*marked/total, pending)},
		{lineWith(doctor, "с понятиями"),
			"с понятиями 1, пустых 0, не разобрала модель 1, служебных 1"},
		{lineWith(doctor, "из разобранных сборка возьмёт снова"),
			"из разобранных сборка возьмёт снова 1 — «служебные» без признака: забыты чисткой графа или признак снят; они же в «осталось»"},
		{lineWith(doctor, "отметок книг, которых в коллекции НЕТ"),
			fmt.Sprintf("отметок книг, которых в коллекции НЕТ: %d (в 2 книгах) — в счёт выше не входят", f.goneChunks+40)},
		{lineWith(doctor, "итого по каталогам"),
			fmt.Sprintf("%-26s %10d %10d %10d   %5.1f%%", "итого по каталогам", marked, pending, total, 100*float64(marked)/float64(total))},
	}
	if !strings.Contains(doctor, "разобрать оставшееся") {
		t.Errorf("доктор не советует разобрать остаток:\n%s", doctor)
	}

	out.Reset()
	if err := Status(&out, f.cfg, f.name, "", true); err != nil {
		t.Fatal(err)
	}
	status := out.String()
	checks = append(checks, []struct{ got, want string }{
		{lineWith(status, "разобрано кусков"),
			fmt.Sprintf("разобрано кусков %d из %d (осталось %d)", marked, total, pending)},
		{lineWith(status, "из них с понятиями"),
			"из них с понятиями 1, пустых 0, не разобрала модель 1, служебных 1"},
		{lineWith(status, "вся коллекция:"),
			fmt.Sprintf("вся коллекция: кусков %d, разобрано %d, осталось %d (%d%%)", total, marked, pending, 100*marked/total)},
	}...)

	out.Reset()
	if err := Pending(&out, f.cfg, f.name, "", "", ""); err != nil {
		t.Fatal(err)
	}
	checks = append(checks, struct{ got, want string }{strings.TrimSpace(out.String()), fmt.Sprint(pending)})

	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("строка\n  %q\nожидалась\n  %q", c.got, c.want)
		}
	}
	if t.Failed() {
		t.Logf("доктор:\n%s\nстатус:\n%s", doctor, status)
	}
}

// Верхняя папка книги — по границе каталога и от самого короткого корня
// (аудит 07.10.2026, раздел 4.6). Прежняя копия сравнивала корень подстрокой:
// /data/lib находил книги из /data/library, и каталогом выходил «/rary».
func TestTopFolder(t *testing.T) {
	cases := []struct {
		path  string
		roots []string
		want  string
	}{
		{"/data/lib/AI/agents.pdf", []string{"/data/lib"}, "/AI"},
		{"/data/lib/AI/Agents/rag.pdf", []string{"/data/lib/"}, "/AI"},
		{"/data/library/AI/agents.pdf", []string{"/data/lib"}, ""},
		{"/data/library/Go/book.pdf", []string{"/data/lib", "/data/library"}, "/Go"},
		{"/data/lib/AI/Agents/rag.pdf", []string{"/data/lib/AI", "/data/lib"}, "/AI"},
		{"/data/lib/book.pdf", []string{"/data/lib"}, ""},
		{"/data/lib", []string{"/data/lib"}, ""},
		{"/elsewhere/AI/x.pdf", []string{"/data/lib"}, ""},
		{"/data/lib/AI/x.pdf", nil, ""},
		{"/AI/x.pdf", []string{"/"}, "/AI"},
		{"/data/lib/Machine Learning/x.pdf", []string{"", "/data/lib"}, "/Machine Learning"},
	}
	for _, c := range cases {
		if got := TopFolder(c.path, c.roots); got != c.want {
			t.Errorf("TopFolder(%q, %q) = %q, ожидалось %q", c.path, c.roots, got, c.want)
		}
	}
}

// Совет доктора — команда, которую можно вставить в bash как есть
// (аудит 07.10.2026, раздел 4.6): для книг в корне библиотеки печаталось
// `--graph-folder (корень библиотеки)` — синтаксическая ошибка.
func TestBuildAdviceIsShellSafe(t *testing.T) {
	root := buildAdvice("books", folderPending{folder: rootFolderLabel, pending: 9})
	if strings.Contains(root, "--graph-folder") || !strings.HasPrefix(root, "ollchat --graph-build books ") {
		t.Errorf("совет для книг в корне: %q — ключа каталога у них нет", root)
	}
	if got := buildAdvice("books", folderPending{folder: "/Machine Learning", pending: 3}); !strings.HasPrefix(got,
		"ollchat --graph-build books --graph-folder '/Machine Learning'   (3 кусков)") {
		t.Errorf("каталог с пробелом: %q — без кавычек оболочка разрежет его на два довода", got)
	}
	if got := buildAdvice("books", folderPending{folder: "/Безопасность", pending: 5}); got !=
		"ollchat --graph-build books --graph-folder /Безопасность   (5 кусков)" {
		t.Errorf("обычный каталог: %q — кавычки ему не нужны", got)
	}

	f := newMaintFixture(t) // книги фикстуры лежат прямо в корне библиотеки
	var out bytes.Buffer
	if err := DoctorTo(&out, io.Discard, f.cfg, f.name); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "--graph-folder (") {
		t.Errorf("доктор советует несуществующий каталог:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "ollchat --graph-build proba   (") {
		t.Errorf("нет совета разобрать книги корня библиотеки:\n%s", out.String())
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/AI":                 "/AI",
		"/Coding/Go":          "/Coding/Go",
		"/Безопасность":       "/Безопасность",
		"/Machine Learning":   "'/Machine Learning'",
		"(корень библиотеки)": "'(корень библиотеки)'",
		"/O'Reilly":           `'/O'\''Reilly'`,
		"":                    "''",
		"/a$b":                "'/a$b'",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, ожидалось %s", in, got, want)
		}
	}
}

// Битая разметка тем — ошибка, а не «темы не размечены» (аудит 07.10.2026,
// раздел 4.6). Прежде она молча давала REPARTITION=yes, докатка пересчитывала
// разметку поверх битого файла, описания тем не переносились, а битый файл
// уходил в communities.prev.json поверх прежней, ещё целой копии.
func TestBrokenCommunitiesIsAnError(t *testing.T) {
	f := newMaintFixture(t)
	var out bytes.Buffer
	if err := Repartition(&out, f.cfg, f.name); err != nil {
		t.Fatalf("без разметки тем: %v", err)
	}
	if !strings.HasSuffix(out.String(), repartitionYes+"\n") {
		t.Errorf("без разметки тем пересчёт нужен:\n%s", out.String())
	}

	path := filepath.Join(f.dir, graph.CommunityFile)
	if err := os.WriteFile(path, []byte(`{"list": [{"id": 1, "members": [1, 2`), 0o644); err != nil {
		t.Fatal(err)
	}
	before := dirHash(t, f.dir)
	out.Reset()
	err := Repartition(&out, f.cfg, f.name)
	if err == nil || !strings.Contains(err.Error(), graph.PrevCommunityFile) {
		t.Errorf("битая разметка: ошибка %v, ожидался отказ с объяснением", err)
	}
	if strings.Contains(out.String(), "REPARTITION=") {
		t.Errorf("по битой разметке выдана метка для докатки:\n%s", out.String())
	}

	out.Reset()
	if err := DoctorTo(&out, io.Discard, f.cfg, f.name); err != nil {
		t.Fatal(err)
	}
	doctor := out.String()
	if !strings.Contains(doctor, "разметка НЕ ЧИТАЕТСЯ") || strings.Contains(doctor, "не размечены") {
		t.Errorf("доктор путает битую разметку с отсутствующей:\n%s", doctor)
	}
	if strings.Contains(doctor, "ollchat --graph-communities") {
		t.Errorf("доктор советует пересчёт поверх битой разметки:\n%s", doctor)
	}
	if dirHash(t, f.dir) != before {
		t.Error("проверка разметки изменила каталог графа")
	}
}
