package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
)

func newTestRegistry(t *testing.T) (*Registry, string) {
	t.Helper()
	root := t.TempDir()
	realRoot, _ := filepath.EvalSymlinks(root)
	sb, err := permissions.NewSandbox(realRoot, false, false, 512)
	if err != nil {
		t.Fatalf("NewSandbox: %v", err)
	}
	r, err := NewRegistry(AllNames(), Options{
		Sandbox:     sb,
		BashTimeout: 10 * time.Second,
		MaxOutputKB: 64,
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return r, realRoot
}

func TestUnknownToolRejected(t *testing.T) {
	if _, err := NewRegistry([]string{"read_file", "delete_everything"}, Options{}); err == nil {
		t.Fatal("неизвестный инструмент в конфиге должен быть ошибкой запуска")
	}
}

func TestReadFilePlanRejectsEscape(t *testing.T) {
	r, _ := newTestRegistry(t)
	if _, err := r.Plan(NameReadFile, map[string]any{"path": "../../etc/passwd"}); err == nil {
		t.Fatal("чтение за пределами песочницы должно отклоняться на этапе плана")
	}
}

func TestReadFile(t *testing.T) {
	r, root := newTestRegistry(t)
	path := filepath.Join(root, "test.txt")
	if err := os.WriteFile(path, []byte("первая\nвторая\nтретья\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan, err := r.Plan(NameReadFile, map[string]any{"path": "test.txt"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Req.Kind != permissions.KindRead {
		t.Errorf("вид действия = %v, ожидалось Read", plan.Req.Kind)
	}

	out, err := plan.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "вторая") || !strings.Contains(out, "     2\t") {
		t.Errorf("вывод не содержит нумерованных строк:\n%s", out)
	}
}

func TestWriteFileShowsDiffAndRequiresWritePermission(t *testing.T) {
	r, root := newTestRegistry(t)
	path := filepath.Join(root, "code.txt")
	if err := os.WriteFile(path, []byte("строка 1\nстрока 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan, err := r.Plan(NameWriteFile, map[string]any{
		"path": "code.txt", "content": "строка 1\nстрока 2 изменена\n",
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Req.Kind != permissions.KindWrite {
		t.Errorf("вид действия = %v, ожидалось Write", plan.Req.Kind)
	}
	if !strings.Contains(plan.Preview, "- строка 2") || !strings.Contains(plan.Preview, "+ строка 2 изменена") {
		t.Errorf("предпросмотр не похож на diff:\n%s", plan.Preview)
	}

	// Файл не должен меняться, пока план не выполнен.
	data, _ := os.ReadFile(path)
	if string(data) != "строка 1\nстрока 2\n" {
		t.Error("построение плана не должно менять файл")
	}

	if _, err := plan.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	data, _ = os.ReadFile(path)
	if string(data) != "строка 1\nстрока 2 изменена\n" {
		t.Errorf("файл после записи: %q", string(data))
	}
}

func TestEditFileRequiresUniqueMatch(t *testing.T) {
	r, root := newTestRegistry(t)
	path := filepath.Join(root, "dup.txt")
	if err := os.WriteFile(path, []byte("повтор\nповтор\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Plan(NameEditFile, map[string]any{
		"path": "dup.txt", "old_string": "повтор", "new_string": "новое",
	}); err == nil {
		t.Fatal("неоднозначная замена должна отклоняться")
	}

	if _, err := r.Plan(NameEditFile, map[string]any{
		"path": "dup.txt", "old_string": "нет такого", "new_string": "x",
	}); err == nil {
		t.Fatal("отсутствующий фрагмент должен отклоняться")
	}

	plan, err := r.Plan(NameEditFile, map[string]any{
		"path": "dup.txt", "old_string": "повтор\nповтор", "new_string": "один",
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, err := plan.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "один\n" {
		t.Errorf("файл после правки: %q", string(data))
	}
}

func TestGrep(t *testing.T) {
	r, root := newTestRegistry(t)
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("func здесь тоже\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan, err := r.Plan(NameGrep, map[string]any{"pattern": "func", "glob": "*.go"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	out, err := plan.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "a.go:2") {
		t.Errorf("ожидалось совпадение в a.go:\n%s", out)
	}
	if strings.Contains(out, "b.txt") {
		t.Errorf("фильтр *.go не сработал:\n%s", out)
	}

	if _, err := r.Plan(NameGrep, map[string]any{"pattern": "([unclosed"}); err == nil {
		t.Error("некорректное регулярное выражение должно отклоняться")
	}
}

func TestBashPlanTargetIsCommand(t *testing.T) {
	r, _ := newTestRegistry(t)
	plan, err := r.Plan(NameBash, map[string]any{"command": "echo привет"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Req.Kind != permissions.KindBash || plan.Req.Target != "echo привет" {
		t.Errorf("запрос разрешения = %v %q", plan.Req.Kind, plan.Req.Target)
	}

	out, err := plan.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "привет") || !strings.Contains(out, "Код возврата: 0") {
		t.Errorf("вывод команды:\n%s", out)
	}
}

func TestBashTimeout(t *testing.T) {
	r, _ := newTestRegistry(t)
	plan, err := r.Plan(NameBash, map[string]any{"command": "sleep 5", "timeout": 1})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	start := time.Now()
	if _, err := plan.Run(context.Background()); err == nil {
		t.Fatal("превышение таймаута должно возвращать ошибку")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("таймаут не сработал вовремя: %s", elapsed)
	}
}

func TestHTTPFetchRejectsNonHTTP(t *testing.T) {
	r, _ := newTestRegistry(t)
	for _, u := range []string{"file:///etc/passwd", "ftp://example.com", "не адрес"} {
		if _, err := r.Plan(NameHTTPFetch, map[string]any{"url": u}); err == nil {
			t.Errorf("адрес %q должен отклоняться", u)
		}
	}
}

func TestDiffStat(t *testing.T) {
	added, removed := DiffStat("a\nb\nc\n", "a\nB\nc\nd\n")
	if added != 2 || removed != 1 {
		t.Errorf("DiffStat = +%d -%d, ожидалось +2 -1", added, removed)
	}
	if a, r := DiffStat("одинаково\n", "одинаково\n"); a != 0 || r != 0 {
		t.Errorf("для одинаковых текстов ожидалось +0 -0, получено +%d -%d", a, r)
	}
}

func TestUnifiedDiffNoChange(t *testing.T) {
	if got := UnifiedDiff("текст\n", "текст\n", 3); !strings.Contains(got, "не изменится") {
		t.Errorf("UnifiedDiff без изменений = %q", got)
	}
}

func TestTruncateOutput(t *testing.T) {
	o := Options{MaxOutputKB: 1}
	long := strings.Repeat("я", 5000) // 10000 байт
	got := o.truncate(long)
	if !strings.Contains(got, "вывод обрезан") {
		t.Error("длинный вывод должен помечаться как обрезанный")
	}
	if len(got) > 2048 {
		t.Errorf("длина обрезанного вывода = %d байт", len(got))
	}
}

// TestReadFilePDF проверяет, что read_file читает документ PDF сам, без внешних
// программ: раньше модель на таком файле упиралась в отсутствие pdftotext и
// выжигала лимит итераций на попытках его установить.
func TestReadFilePDF(t *testing.T) {
	reg, root := newTestRegistry(t)
	const text = "Договор поставки № 12"
	if err := os.WriteFile(filepath.Join(root, "doc.pdf"), samplePDF(text), 0o644); err != nil {
		t.Fatal(err)
	}

	plan, err := reg.Plan(NameReadFile, map[string]any{"path": "doc.pdf"})
	if err != nil {
		t.Fatalf("план: %v", err)
	}
	out, err := plan.Run(context.Background())
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if !strings.Contains(out, "Документ PDF, страниц: 1") {
		t.Fatalf("нет сведений о документе:\n%s", out)
	}
	if !strings.Contains(out, text) {
		t.Fatalf("текст документа не извлечён:\n%s", out)
	}
}

// TestReadFilePDFIgnoresFileSizeLimit закрепляет, что к документам PDF предел
// sandbox.max_file_kb не применяется: в контекст идёт извлечённый текст, а сам
// файл почти всегда весит мегабайты.
func TestReadFilePDFIgnoresFileSizeLimit(t *testing.T) {
	reg, root := newTestRegistry(t)
	// Дополняем документ комментарием так, чтобы он заведомо превысил предел
	// для текстовых файлов, но остался меньше предела для PDF.
	data := append(samplePDF("Отчёт"), []byte("\n%"+strings.Repeat("x", 600*1024))...)
	if err := os.WriteFile(filepath.Join(root, "big.pdf"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	plan, err := reg.Plan(NameReadFile, map[string]any{"path": "big.pdf"})
	if err != nil {
		t.Fatalf("план: %v", err)
	}
	out, err := plan.Run(context.Background())
	if err != nil {
		t.Fatalf("документ крупнее max_file_kb должен читаться: %v", err)
	}
	if !strings.Contains(out, "Отчёт") {
		t.Fatalf("текст не извлечён:\n%s", out)
	}
}

// samplePDF собирает документ с одной страницей, содержащей заданный текст.
// Шрифт сделан составным с таблицей /ToUnicode — так устроены русские
// документы, которые приходят на разбор.
func samplePDF(text string) []byte {
	var codes, chars strings.Builder
	runes := []rune(text)
	for i, r := range runes {
		code := 0x20 + i
		fmt.Fprintf(&codes, "%04X", code)
		fmt.Fprintf(&chars, "<%04X> <%04X>\n", code, r)
	}
	cmap := fmt.Sprintf("begincmap\n1 begincodespacerange\n<0000> <FFFF>\n"+
		"endcodespacerange\n%d beginbfchar\n%sendbfchar\nendcmap", len(runes), chars.String())
	content := fmt.Sprintf("BT /F1 12 Tf 72 720 Td <%s> Tj ET", codes.String())

	stream := func(data string) string {
		return fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(data), data)
	}
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		stream(content),
		"<< /Type /Font /Subtype /Type0 /BaseFont /X /Encoding /Identity-H /ToUnicode 6 0 R >>",
		stream(cmap),
	}
	var b strings.Builder
	b.WriteString("%PDF-1.7\n")
	for i, body := range objs {
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	b.WriteString("trailer\n<< /Root 1 0 R >>\n%%EOF\n")
	return []byte(b.String())
}

// TestTruncateKeepsValidUTF8 закрепляет исправленный дефект: обрезка вывода
// приходилась на середину символа, и модели уходил испорченный хвост. Нашла
// его сама модель, разбиравшая книгу: «...машинного о�».
func TestTruncateKeepsValidUTF8(t *testing.T) {
	opts := Options{MaxOutputKB: 1}
	// Строка из двухбайтовых символов: любой предел в байтах приходится
	// на середину символа примерно в половине случаев.
	text := strings.Repeat("аб", 2000)
	got := opts.truncate(text)
	if !utf8.ValidString(got) {
		t.Fatal("после обрезки строка перестала быть корректной UTF-8")
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Fatal("в обрезанном выводе появился символ замены")
	}
	if !strings.Contains(got, "вывод обрезан") {
		t.Fatalf("нет пометки об обрезке: %q", got[len(got)-60:])
	}

	// То же для строки, где предел приходится ровно на ведущий байт.
	for shift := 0; shift < 4; shift++ {
		s := strings.Repeat("x", shift) + strings.Repeat("я", 2000)
		if got := opts.truncate(s); !utf8.ValidString(got) {
			t.Fatalf("сдвиг %d: строка испорчена", shift)
		}
	}
}

// newKBRegistry собирает реестр с базой знаний из готовых книг.
func newKBRegistry(t *testing.T) (*Registry, *kb.Base, string) {
	t.Helper()
	root := t.TempDir()
	books := filepath.Join(root, "books")
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	// Две книги: одна про каналы, другая про контейнеры.
	writeBook(t, books, "go.pdf", "goroutines and channels explained in detail")
	writeBook(t, books, "k8s.pdf", "kubernetes pods and deployments explained")

	baseDir := filepath.Join(root, "kb")
	base, err := kb.OpenBase(baseDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	coll, err := base.Create("test", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coll.Add(context.Background(), []string{books}, kb.IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}

	sb, err := permissions.NewSandbox(root, false, false, 512)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewRegistry(AllNames(), Options{
		Sandbox: sb, MaxOutputKB: 64,
		KB: base, KBDir: baseDir, KBDefault: "test", KBTopK: 5, KBMaxPerBook: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	return reg, base, baseDir
}

// newKBRegistryWithStyle — то же, но со своей политикой ответа.
func newKBRegistryWithStyle(t *testing.T, style string) (*Registry, *kb.Base, string) {
	t.Helper()
	root := t.TempDir()
	books := filepath.Join(root, "books")
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	// Две книги: одна про каналы, другая про контейнеры.
	writeBook(t, books, "go.pdf", "goroutines and channels explained in detail")
	writeBook(t, books, "k8s.pdf", "kubernetes pods and deployments explained")

	baseDir := filepath.Join(root, "kb")
	base, err := kb.OpenBase(baseDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	coll, err := base.Create("test", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coll.Add(context.Background(), []string{books}, kb.IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}

	sb, err := permissions.NewSandbox(root, false, false, 512)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewRegistry(AllNames(), Options{
		Sandbox: sb, MaxOutputKB: 64,
		KB: base, KBDir: baseDir, KBDefault: "test", KBTopK: 5, KBMaxPerBook: 3,
		AnswerStyle: style,
	})
	if err != nil {
		t.Fatal(err)
	}
	return reg, base, baseDir
}

// writeBook кладёт простой документ PDF с одной страницей текста.
func writeBook(t *testing.T, dir, name, text string) {
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

// TestKBSearchToolCitesSources — главное требование к выдаче: она должна быть
// проверяемой. Без книги и страницы ответ по книгам ничем не отличается
// от выдумки.
func TestKBSearchToolCitesSources(t *testing.T) {
	reg, _, _ := newKBRegistry(t)
	plan, err := reg.Plan(NameKBSearch, map[string]any{"query": "goroutines channels"})
	if err != nil {
		t.Fatalf("план: %v", err)
	}
	out, err := plan.Run(context.Background())
	if err != nil {
		t.Fatalf("поиск: %v", err)
	}
	for _, want := range []string{"[1]", "go.pdf", "стр.", "id=test/"} {
		if !strings.Contains(out, want) {
			t.Errorf("в выдаче нет %q:\n%s", want, out)
		}
	}
	// Два требования должны стоять рядом и уравновешивать друг друга.
	// Одно указание ссылаться приводит к подборке цитат вместо ответа:
	// на живом сеансе модель на вопрос «как работают горутины» выдала
	// только выдержки из книг, ничего не объяснив от себя.
	if !strings.Contains(out, "помечай") {
		t.Errorf("в выдаче нет указания помечать взятое из книг:\n%s", out)
	}
	if !strings.Contains(out, "своими словами") {
		t.Errorf("в выдаче нет требования объяснять своими словами:\n%s", out)
	}
}

// TestKBSearchToolFiltersByBook — поиск можно сузить до нужной книги.
func TestKBSearchToolFiltersByBook(t *testing.T) {
	reg, _, _ := newKBRegistry(t)
	plan, _ := reg.Plan(NameKBSearch, map[string]any{"query": "explained", "book": "k8s"})
	out, err := plan.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "go.pdf") {
		t.Errorf("в выдачу попала книга, не подходящая под фильтр:\n%s", out)
	}
	if !strings.Contains(out, "k8s.pdf") {
		t.Errorf("нужная книга не найдена:\n%s", out)
	}
}

// TestKBSearchToolSaysWhenNothingFound — молчание хуже прямого ответа:
// модель должна знать, что искать больше негде.
func TestKBSearchToolSaysWhenNothingFound(t *testing.T) {
	reg, _, _ := newKBRegistry(t)
	plan, _ := reg.Plan(NameKBSearch, map[string]any{"query": "квантовая хромодинамика"})
	out, err := plan.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ничего не нашлось") {
		t.Errorf("не сказано, что ничего не найдено:\n%s", out)
	}
}

// TestKBReadToolExpandsFragment — модель просит продолжение, когда во фрагменте
// мысль оборвана.
func TestKBReadToolExpandsFragment(t *testing.T) {
	reg, _, _ := newKBRegistry(t)
	plan, _ := reg.Plan(NameKBSearch, map[string]any{"query": "goroutines"})
	out, err := plan.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(out, "id=")
	if i < 0 {
		t.Fatalf("в выдаче нет номера фрагмента:\n%s", out)
	}
	id := strings.Fields(out[i+3:])[0]

	plan, err = reg.Plan(NameKBRead, map[string]any{"id": id, "around": 1})
	if err != nil {
		t.Fatalf("план чтения: %v", err)
	}
	text, err := plan.Run(context.Background())
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if !strings.Contains(text, "goroutines") {
		t.Errorf("фрагмент не прочитан:\n%s", text)
	}
}

// TestKBToolsDoNotTouchFiles закрепляет границу безопасности: поиск читает
// только собственный индекс приложения, а не книги на диске. Поэтому модель
// не может через него добраться до чужих каталогов.
func TestKBToolsDoNotTouchFiles(t *testing.T) {
	reg, _, baseDir := newKBRegistry(t)
	for _, name := range []string{NameKBSearch, NameKBRead} {
		args := map[string]any{"query": "x", "id": "test/1#0"}
		plan, err := reg.Plan(name, args)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if plan.Req.Kind != permissions.KindRead {
			t.Errorf("%s: вид проверки %q, ожидалось чтение", name, plan.Req.Kind)
		}
		if plan.Req.Target != baseDir {
			t.Errorf("%s: цель проверки %q, а должна быть база знаний %q", name, plan.Req.Target, baseDir)
		}
	}
}

// TestKBSearchWithoutBase — без настроенной базы инструмент должен объяснить,
// что делать, а не падать.
func TestKBSearchWithoutBase(t *testing.T) {
	sb, _ := permissions.NewSandbox(t.TempDir(), false, false, 512)
	reg, err := NewRegistry([]string{NameKBSearch}, Options{Sandbox: sb, MaxOutputKB: 64})
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := reg.Plan(NameKBSearch, map[string]any{"query": "что угодно"})
	if _, err := plan.Run(context.Background()); err == nil {
		t.Fatal("поиск без базы знаний прошёл успешно")
	} else if !strings.Contains(err.Error(), "не настроена") {
		t.Errorf("непонятная ошибка: %v", err)
	}
}

// TestViewImageFindsFigure закрепляет случай, ради которого инструмент появился:
// в коммерческом предложении страница «Финансовое предложение» со всей таблицей
// цен свёрстана картинкой, и в текстовом слое её нет вовсе. Модель верно
// заключала, что цену надо смотреть в картинке, но смотреть было нечем —
// и уходила искать в системе средства распознавания.
func TestViewImageFindsFigure(t *testing.T) {
	reg, root := newTestRegistry(t)
	path := filepath.Join(root, "предложение.pdf")
	if err := os.WriteFile(path, pdfWithImage(), 0o644); err != nil {
		t.Fatal(err)
	}

	plan, err := reg.Plan(NameViewImage, map[string]any{"path": "предложение.pdf"})
	if err != nil {
		t.Fatalf("план: %v", err)
	}
	out, err := plan.Run(context.Background())
	if err != nil {
		t.Fatalf("показ картинки: %v", err)
	}
	if !strings.Contains(out, "рисунок 1.1") {
		t.Fatalf("в ответе нет метки рисунка:\n%s", out)
	}
	if plan.Images == nil {
		t.Fatal("инструмент не отдал картинки")
	}
	imgs := plan.Images()
	if len(imgs) != 1 {
		t.Fatalf("картинок %d, ожидалась одна", len(imgs))
	}
	// Картинка должна быть годной к отправке: base64 разумного размера.
	if len(imgs[0]) < 100 {
		t.Fatalf("картинка подозрительно мала: %d байт base64", len(imgs[0]))
	}
	if !strings.Contains(out, "приложены к диалогу") {
		t.Fatalf("модели не сказано, что картинка придёт следующим сообщением:\n%s", out)
	}
}

// TestViewImageExplainsWhenNothing — молчание модель принимает за сбой
// инструмента и начинает искать обходные пути. Поэтому пустой ответ обязан
// объяснять причину.
func TestViewImageExplainsWhenNothing(t *testing.T) {
	reg, root := newTestRegistry(t)
	// Документ без картинок.
	if err := os.WriteFile(filepath.Join(root, "текст.pdf"), samplePDF("Только текст"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, _ := reg.Plan(NameViewImage, map[string]any{"path": "текст.pdf"})
	out, err := plan.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "нет картинок") {
		t.Fatalf("не объяснено, почему пусто:\n%s", out)
	}
	if plan.Images != nil && len(plan.Images()) != 0 {
		t.Fatal("картинки отданы там, где их нет")
	}
}

// TestViewImageWrongFigure — при неверной метке инструмент должен подсказать,
// какие рисунки в документе есть.
func TestViewImageWrongFigure(t *testing.T) {
	reg, root := newTestRegistry(t)
	if err := os.WriteFile(filepath.Join(root, "документ.pdf"), pdfWithImage(), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, _ := reg.Plan(NameViewImage, map[string]any{"path": "документ.pdf", "figure": "99.9"})
	out, err := plan.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Есть такие") || !strings.Contains(out, "1.1") {
		t.Fatalf("не подсказано, какие рисунки есть:\n%s", out)
	}
}

// TestViewImageStaysInSandbox — инструмент читает файлы, значит подчиняется
// песочнице так же, как чтение.
func TestViewImageStaysInSandbox(t *testing.T) {
	reg, _ := newTestRegistry(t)
	if _, err := reg.Plan(NameViewImage, map[string]any{"path": "../../etc/passwd"}); err == nil {
		t.Fatal("выход за песочницу не отклонён")
	}
	plan, err := reg.Plan(NameViewImage, map[string]any{"path": "документ.pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Req.Kind != permissions.KindRead {
		t.Fatalf("вид проверки %q, ожидалось чтение", plan.Req.Kind)
	}
}

// pdfWithImage собирает документ с одной картинкой 400×300 на первой странице.
func pdfWithImage() []byte {
	pixels := make([]byte, 400*300*3)
	for i := range pixels {
		pixels[i] = byte(i * 7 % 251)
	}
	stream := func(dict string, data []byte) string {
		if dict == "" {
			dict = "<< >>"
		}
		dict = strings.TrimSuffix(strings.TrimSpace(dict), ">>")
		return fmt.Sprintf("%s /Length %d >>\nstream\n%s\nendstream", dict, len(data), data)
	}
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 6 0 R >> " +
			"/XObject << /Im0 5 0 R >> >> /Contents 4 0 R >>",
		stream("", []byte("BT /F1 12 Tf 50 700 Td (Finansovoe predlozhenie) Tj ET\n"+
			"q 400 0 0 300 50 300 cm /Im0 Do Q")),
		stream("<< /Type /XObject /Subtype /Image /Width 400 /Height 300 "+
			"/ColorSpace /DeviceRGB /BitsPerComponent 8 >>", pixels),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var b strings.Builder
	b.WriteString("%PDF-1.7\n")
	for i, body := range objs {
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	b.WriteString("trailer\n<< /Root 1 0 R >>\n%%EOF\n")
	return []byte(b.String())
}

// newTestRegistryWith собирает набор с показом картинок или без него.
func newTestRegistryWith(t *testing.T, canView bool) *Registry {
	t.Helper()
	root := t.TempDir()
	realRoot, _ := filepath.EvalSymlinks(root)
	sb, err := permissions.NewSandbox(realRoot, false, false, 512)
	if err != nil {
		t.Fatalf("NewSandbox: %v", err)
	}
	names := AllNames()
	if !canView {
		names = nil
		for _, n := range AllNames() {
			if n != NameViewImage {
				names = append(names, n)
			}
		}
	}
	r, err := NewRegistry(names, Options{Sandbox: sb, BashTimeout: 10 * time.Second, MaxOutputKB: 64})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if r.Has(NameViewImage) != canView {
		t.Fatalf("Has(view_image) = %v, ожидалось %v", r.Has(NameViewImage), canView)
	}
	return r
}

// TestBashRefusesToolName закрепляет: имя инструмента приложения — не программа.
//
// Живой случай: подсказка к документу звала посмотреть картинку инструментом
// view_image, а в agent.tools того сеанса его не было. Модель попросила
// подтверждение на «bash view_image path=… figure="4.1"». Отказ должен приходить
// до окна подтверждения и объяснять, что делать дальше.
func TestBashRefusesToolName(t *testing.T) {
	cases := []struct {
		name    string
		canView bool
		cmd     string
		want    []string
	}{
		{"выключенный view_image", false,
			`view_image path="кп.pdf" figure="4.1"`,
			[]string{"agent.tools", "/addimg", "не помогут"}},
		{"включённый view_image", true,
			`view_image path="кп.pdf" figure="4.1"`,
			[]string{"Вызовите его как инструмент"}},
		{"любой другой инструмент", true,
			"read_file kb.md",
			[]string{"Вызовите его как инструмент"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newTestRegistryWith(t, c.canView)
			_, err := r.Plan(NameBash, map[string]any{"command": c.cmd})
			if err == nil {
				t.Fatal("запуск инструмента через оболочку не отклонён")
			}
			for _, want := range c.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("в отказе нет %q: %v", want, err)
				}
			}
		})
	}
}

// TestBashAllowsNamesakeArgument — отказ не должен ловить обычные команды,
// в которых имя инструмента стоит не первым словом.
func TestBashAllowsNamesakeArgument(t *testing.T) {
	r := newTestRegistryWith(t, true)
	if _, err := r.Plan(NameBash, map[string]any{"command": "grep -rn view_image ."}); err != nil {
		t.Fatalf("обычная команда отклонена: %v", err)
	}
}

// TestKBSearchUsesConfiguredStyle — своя политика ответа доходит до модели
// обоими путями: в описании инструмента и в подписи под его результатом.
func TestKBSearchUsesConfiguredStyle(t *testing.T) {
	reg, _, _ := newKBRegistryWithStyle(t, "Пиши по-английски и очень кратко.")

	var spec string
	for _, s := range reg.Specs() {
		if s.Function.Name == NameKBSearch {
			spec = s.Function.Description
		}
	}
	if !strings.Contains(spec, "Пиши по-английски") {
		t.Fatalf("политика не попала в описание инструмента: %q", spec)
	}
	// Договор при этом остаётся на месте: без него модель не поймёт,
	// что инструмент вообще делает.
	if !strings.Contains(spec, "личной библиотеке") {
		t.Fatalf("описание инструмента потеряло суть: %q", spec)
	}

	plan, err := reg.Plan(NameKBSearch, map[string]any{"query": "explained"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := plan.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Пиши по-английски") {
		t.Fatalf("политика не попала в результат поиска:\n%s", out)
	}
}
