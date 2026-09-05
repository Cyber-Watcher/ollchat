package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// kbTestModel собирает модель с базой знаний и папкой книг внутри временного
// каталога, разрешённой в настройках.
func kbTestModel(t *testing.T) (*Model, string) {
	t.Helper()
	root := t.TempDir()
	books := filepath.Join(root, "books")
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	m := newTestModelWith(t, func(cfg *config.Config) {
		cfg.KB.Dir = filepath.Join(root, "kb")
		cfg.KB.Roots = []string{books}
	})
	return m, books
}

// writeTestBook кладёт документ PDF с одной страницей текста.
func writeTestBook(t *testing.T, dir, name, text string) {
	t.Helper()
	body := text + ". " + strings.Repeat("plain sentence of book text about the subject. ", 12)
	content := "BT /F1 12 Tf 50 700 Td (" + body + ") Tj ET"
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /Resources << /Font << /F1 5 0 R >> >> >>",
		"<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var b strings.Builder
	b.WriteString("%PDF-1.7\n")
	for i, o := range objs {
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	b.WriteString("trailer\n<< /Root 1 0 R >>\n%%EOF\n")
	if err := os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func lastBlock(m *Model) block {
	if len(m.blocks) == 0 {
		return block{}
	}
	return m.blocks[len(m.blocks)-1]
}

func TestKBStatusOnEmptyBase(t *testing.T) {
	m, _ := kbTestModel(t)
	settle(t, m, m.runCommand("/kb"))
	if got := lastBlock(m).text; !strings.Contains(got, "Коллекций пока нет") {
		t.Fatalf("состояние пустой базы: %q", got)
	}
}

// TestKBAddRefusesOutsideRoots закрепляет границу безопасности: индексировать
// можно только каталоги, разрешённые в настройках. Песочница тут не годится —
// книги лежат вне рабочего каталога, — поэтому список корней и есть разрешение.
func TestKBAddRefusesOutsideRoots(t *testing.T) {
	m, _ := kbTestModel(t)
	m.runCommand("/kb add go /etc")
	got := lastBlock(m)
	if got.kind != blockError {
		t.Fatalf("индексация чужого каталога не отклонена: %q", got.text)
	}
	if !strings.Contains(got.text, "kb.roots") {
		t.Fatalf("не объяснено, где разрешать каталоги: %q", got.text)
	}
}

// TestKBAddIndexesAndSearches — полный путь пользователя: добавить книги,
// дождаться конца, найти.
func TestKBAddIndexesAndSearches(t *testing.T) {
	m, books := kbTestModel(t)
	writeTestBook(t, books, "go.pdf", "goroutines and channels explained")
	writeTestBook(t, books, "k8s.pdf", "kubernetes pods and deployments")

	cmd := m.runCommand("/kb add go " + books)
	if cmd == nil {
		t.Fatalf("индексация не запустилась: %q", lastBlock(m).text)
	}
	if m.job == nil {
		t.Fatal("задача не заведена")
	}
	drainJob(t, m, cmd)

	if m.job != nil {
		t.Fatal("задача не завершилась")
	}
	// Итог должен быть виден в ленте.
	var summary string
	for _, b := range m.blocks {
		if strings.Contains(b.text, "добавлено книг") {
			summary = b.text
		}
	}
	if summary == "" {
		t.Fatalf("в ленте нет итога индексации; последний блок: %q", lastBlock(m).text)
	}

	settle(t, m, m.runCommand("/kb search goroutines -c go"))
	// Выдача /kb search — та же, что у /search (этап 91, R2.7): книга и страница
	// стоят в строке выдержки, а не в заголовке.
	got := ""
	for _, b := range m.blocks {
		if strings.Contains(b.text, "go.pdf") {
			got = b.text
		}
	}
	if got == "" || !strings.Contains(got, "стр.") {
		t.Fatalf("поиск не дал ссылки на книгу и страницу: %q", got)
	}
}

// TestKBUseAndAuto — выбор коллекции и режим подмешивания.
func TestKBUseAndAuto(t *testing.T) {
	m, books := kbTestModel(t)
	writeTestBook(t, books, "go.pdf", "goroutines and channels explained")
	drainJob(t, m, m.runCommand("/kb add go "+books))

	// Подмешивание нельзя включить, пока не выбрана коллекция.
	m.runCommand("/kb auto on")
	if lastBlock(m).kind != blockError {
		t.Fatal("подмешивание включилось без выбранной коллекции")
	}

	m.runCommand("/kb use go")
	if m.kb.use != "go" {
		t.Fatalf("коллекция не выбрана: %q", m.kb.use)
	}
	m.runCommand("/kb auto on")
	if !m.kb.autoOn {
		t.Fatal("подмешивание не включилось")
	}

	// Теперь вопрос должен подмешать найденное. Графа у коллекции нет,
	// поэтому привратник молчит и решает одна настройка подмешивания книг.
	mix := m.autoMix("что такое goroutines")
	found := mix.Text
	if mix.Chunks == 0 || !strings.Contains(found, "go.pdf") {
		t.Fatalf("подмешивание не сработало: %d фрагментов, %q", mix.Chunks, found)
	}
	// Политика ответа обязана дойти до модели целиком: и требование помечать
	// взятое из книг, и требование объяснять своими словами. Одно без другого
	// даёт либо выдумки, либо подборку цитат вместо ответа.
	for _, want := range []string{"помечай", "своими словами"} {
		if !strings.Contains(found, want) {
			t.Fatalf("в подмешанном тексте нет %q: %q", want, found)
		}
	}

	m.runCommand("/kb auto off")
	if mix := m.autoMix("что такое goroutines"); !mix.Empty() {
		t.Fatalf("подмешивание не выключилось: %q", mix.Text)
	}
}

// TestKBSecondJobRefused — одна задача за раз, и об этом надо сказать прямо.
func TestKBSecondJobRefused(t *testing.T) {
	m, books := kbTestModel(t)
	for i := 0; i < 3; i++ {
		writeTestBook(t, books, fmt.Sprintf("b%d.pdf", i), fmt.Sprintf("topic%d unique", i))
	}
	cmd := m.runCommand("/kb add go " + books)
	if m.job == nil {
		t.Fatal("первая задача не запустилась")
	}
	m.runCommand("/kb add go " + books)
	if got := lastBlock(m); got.kind != blockError || !strings.Contains(got.text, "уже идёт") {
		t.Fatalf("вторая задача не отклонена: %q", got.text)
	}
	drainJob(t, m, cmd)
}

// TestKBStopByEsc — Esc вне генерации останавливает индексацию.
func TestKBStopByEsc(t *testing.T) {
	m, books := kbTestModel(t)
	for i := 0; i < 4; i++ {
		writeTestBook(t, books, fmt.Sprintf("b%d.pdf", i), fmt.Sprintf("topic%d unique", i))
	}
	m.runCommand("/kb add go " + books)
	if m.job == nil {
		t.Fatal("задача не запустилась")
	}
	events := m.job.events
	m.stopJob("остановлено")
	if m.job != nil {
		t.Fatal("задача не остановилась")
	}
	if got := lastBlock(m).text; !strings.Contains(got, "останов") {
		t.Fatalf("в ленте нет отметки об остановке: %q", got)
	}
	// Остановка не мгновенна и не должна быть: индексация доводит текущую
	// книгу до целого состояния и только потом отпускает коллекцию. Ждём
	// закрытия канала — это единственный надёжный признак того, что горутина
	// действительно вышла. Замок для этого не годится: его может ещё не быть,
	// если задача не успела начаться.
	waitJobFinished(t, events)
}

// waitJobFinished ждёт, пока горутина задачи завершится и закроет свой канал.
func waitJobFinished(t *testing.T, events <-chan kb.Progress) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range events {
		}
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("задача не завершилась после остановки")
	}
}

// TestKBRemoveCollection — удаление коллекции снимает и её выбор.
func TestKBRemoveCollection(t *testing.T) {
	m, books := kbTestModel(t)
	writeTestBook(t, books, "go.pdf", "goroutines explained")
	drainJob(t, m, m.runCommand("/kb add go "+books))
	m.runCommand("/kb use go")

	m.runCommand("/kb rm go")
	if m.kb.use != "" {
		t.Fatal("после удаления коллекция осталась выбранной")
	}
	names, _ := m.kb.base.Names()
	if len(names) != 0 {
		t.Fatalf("коллекция не удалена: %v", names)
	}
}

// drainJob прокручивает фоновую задачу до конца, как это делает цикл событий.
func drainJob(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil || m.job == nil {
		return
	}
	// startJob возвращает батч из ожидания задачи и тика спиннера; в тесте
	// цикла событий нет, поэтому ждём задачу напрямую.
	gen, events := m.gen.job, m.job.events
	deadline := time.Now().Add(60 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("задача не завершилась за отведённое время")
		}
		switch v := waitForJob(gen, events)().(type) {
		case jobProgressMsg:
			m.handleJobProgress(v)
		case jobDoneMsg:
			m.handleJobDone(v)
			return
		default:
			return
		}
	}
}

// TestKBStatsShowsBreakdown — состав коллекции виден в /kb stats, и три строки
// про каталоги не путаются между собой.
func TestKBStatsShowsBreakdown(t *testing.T) {
	m, books := kbTestModel(t)
	for _, sub := range []string{"Go", "CSharp"} {
		if err := os.MkdirAll(filepath.Join(books, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeTestBook(t, filepath.Join(books, "Go"), "go.pdf", "goroutines explained")
	writeTestBook(t, filepath.Join(books, "CSharp"), "c1.pdf", "linq explained")
	writeTestBook(t, filepath.Join(books, "CSharp"), "c2.pdf", "async explained")
	drainJob(t, m, m.runCommand("/kb add lib "+books))

	settle(t, m, m.runCommand("/kb stats lib"))
	got := lastBlock(m).text
	for _, want := range []string{"состав:", "CSharp", "Go", "собрана из:", "разрешено:"} {
		if !strings.Contains(got, want) {
			t.Fatalf("в /kb stats нет %q:\n%s", want, got)
		}
	}
	// Самая крупная папка должна стоять выше — иначе разбивку читать неудобно.
	if strings.Index(got, "CSharp") > strings.Index(got, "\n    Go") && strings.Contains(got, "\n    Go") {
		t.Fatalf("папки не отсортированы по числу книг:\n%s", got)
	}
}

// Про коллекцию без смыслов, в которой модель не ищет, напоминать не надо:
// у документации проекта ищут точные слова, векторы там не нужны, а сообщение
// «смыслы посчитаны для 0%» висело бы при каждом запуске вечно.
func TestPendingSkipsZeroCoverageOfIdleCollection(t *testing.T) {
	cases := []struct {
		name    string
		percent int
		inUse   string
		coll    string
		warn    bool
	}{
		{"нулевая и не выбрана — молчим", 0, "books", "projectdocs", false},
		{"нулевая и выбрана — говорим", 0, "books", "books", true},
		{"брошена на середине — говорим всегда", 42, "books", "projectdocs", true},
		{"посчитана целиком — молчим", 100, "books", "books", false},
	}
	for _, c := range cases {
		got := shouldWarnCoverage(c.percent, c.coll, c.inUse)
		if got != c.warn {
			t.Errorf("%s: получили %v, ожидалось %v", c.name, got, c.warn)
		}
	}
}

// /kb style печатает действующую политику ответа и говорит, откуда она взята.
// До этой команды текст можно было прочитать только в исходнике: в конфиге
// по умолчанию пусто, а править вслепую — верный способ выбросить требование,
// о котором не знал.
func TestKBStyleShowsEffectivePolicy(t *testing.T) {
	m := newTestModel(t)
	m.kbStyleCmd()
	text := lastBlockText(t, m)

	if !strings.Contains(text, "встроенная") {
		t.Errorf("не сказано, что политика встроенная: %q", text)
	}
	for _, want := range []string{"своими словами", "Название", "перевод"} {
		if !strings.Contains(text, want) {
			t.Errorf("в выводе нет требования %q", want)
		}
	}
	if !strings.Contains(text, "answer_style") {
		t.Errorf("не сказано, чем заменить: %q", text)
	}

	// Своя политика опознаётся как своя.
	own := newTestModelWith(t, func(c *config.Config) {
		c.KB.AnswerStyle = "Отвечай коротко и по делу."
	})
	own.kbStyleCmd()
	text = lastBlockText(t, own)
	if !strings.Contains(text, "ваша") {
		t.Errorf("своя политика не опознана: %q", text)
	}
	if !strings.Contains(text, "Отвечай коротко и по делу.") {
		t.Errorf("свой текст не показан: %q", text)
	}
	if strings.Contains(text, "Название обязательно") {
		t.Errorf("показан встроенный текст вместо своего: %q", text)
	}
}

// lastBlockText — текст последнего блока ленты.
func lastBlockText(t *testing.T, m *Model) string {
	t.Helper()
	if len(m.blocks) == 0 {
		t.Fatal("в ленте нет блоков")
	}
	return m.blocks[len(m.blocks)-1].text
}
