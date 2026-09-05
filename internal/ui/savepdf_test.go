package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/pdf"
	"github.com/Cyber-Watcher/ollchat/internal/pdfout"
)

// withAnswer кладёт в ленту пару «вопрос — ответ», чтобы было что сохранять.
func withAnswer(m *Model, question, answer string) {
	clearTranscript(m)
	m.addBlock(block{kind: blockUser, text: question})
	m.addBlock(block{kind: blockAssistant, text: answer, model: "test-model"})
}

// typeRune отправляет модели набор обычного символа.
func typeRune(m *Model, r rune) {
	m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
}

// catchPDFWrite подменяет запись файла: тесту нужны переданные параметры,
// а не документ на диске.
type pdfCall struct {
	path       string
	src        string
	withHeader bool
	overwrite  bool
}

func catchPDFWrite(t *testing.T) *[]pdfCall {
	t.Helper()
	var calls []pdfCall
	prev := pdfWriteFile
	pdfWriteFile = func(path, src string, opt pdfout.Options, overwrite bool) (*pdfout.Result, error) {
		calls = append(calls, pdfCall{path: path, src: src, withHeader: opt.WithHeader, overwrite: overwrite})
		return &pdfout.Result{Data: []byte("%PDF-1.7"), Pages: 1}, nil
	}
	t.Cleanup(func() { pdfWriteFile = prev })
	return &calls
}

// TestSavePDFPanelKeepsFrameHeight — главная защита от забытой точки
// подключения: панель обязана отобрать высоту у ленты, а не вылезти за экран.
func TestSavePDFPanelKeepsFrameHeight(t *testing.T) {
	m := newTestModel(t)
	withAnswer(m, "вопрос", "ответ модели")

	lines := func() int { return len(strings.Split(m.View().Content, "\n")) }
	before, vpBefore := lines(), m.vp.Height()

	m.openSavePDF(false)
	if m.savePDF == nil {
		t.Fatal("окно сохранения не открылось")
	}
	if got := lines(); got != before {
		t.Errorf("высота кадра изменилась: было %d, стало %d", before, got)
	}
	if m.vp.Height() != vpBefore-savePDFHeight+m.ta.Height() {
		t.Errorf("лента не отдала панели место: %d при %d до открытия", m.vp.Height(), vpBefore)
	}
	if h := len(strings.Split(m.savePDFView(), "\n")); h != savePDFHeight {
		t.Errorf("панель занимает %d строк, а в расчёте высоты стоит %d", h, savePDFHeight)
	}

	m.closeSavePDF()
	if m.vp.Height() != vpBefore {
		t.Errorf("после закрытия лента не вернула высоту: %d вместо %d", m.vp.Height(), vpBefore)
	}
	if got := lines(); got != before {
		t.Errorf("после закрытия высота кадра %d вместо %d", got, before)
	}
}

// TestSavePDFCursorOnNameLine: курсор должен стоять в строке с именем файла.
// Расчёт координаты ручной, и промах в одну строку заметен только глазами.
func TestSavePDFCursorOnNameLine(t *testing.T) {
	m := newTestModel(t)
	withAnswer(m, "вопрос про горутины", "ответ")
	m.openSavePDF(false)

	v := m.View()
	if v.Cursor == nil {
		t.Fatal("курсора нет — поле имени его не отдало")
	}
	lines := strings.Split(v.Content, "\n")
	if v.Cursor.Y < 0 || v.Cursor.Y >= len(lines) {
		t.Fatalf("курсор вне кадра: строка %d при высоте %d", v.Cursor.Y, len(lines))
	}
	if row := lines[v.Cursor.Y]; !strings.Contains(row, ".pdf") {
		t.Errorf("курсор не в строке имени файла: %q", row)
	}
}

// TestSavePDFTakesAllKeys: пока окно открыто, клавиши идут в имя файла,
// а Esc закрывает окно и не прерывает генерацию.
func TestSavePDFTakesAllKeys(t *testing.T) {
	m := newTestModel(t)
	withAnswer(m, "вопрос", "ответ")
	m.ta.SetValue("недописанный вопрос")
	m.openSavePDF(false)

	m.savePDF.input.SetValue("")
	for _, r := range "отчёт" {
		typeRune(m, r)
	}
	if got := m.savePDF.input.Value(); got != "отчёт" {
		t.Errorf("имя набралось как %q", got)
	}
	if got := m.ta.Value(); got != "недописанный вопрос" {
		t.Errorf("клавиши просочились в поле вопроса: %q", got)
	}

	// Esc при открытом окне закрывает окно, а не рвёт генерацию.
	m.streaming = true
	m.Update(pressKey(tea.KeyEsc))
	if m.savePDF != nil {
		t.Error("Esc не закрыл окно")
	}
	if !m.streaming {
		t.Error("Esc прервал генерацию вместо закрытия окна")
	}
	m.streaming = false
}

// TestSavePDFKeyNames: у Shift+F4 три имени, и все три обязаны давать
// сохранение вместе с вопросом.
func TestSavePDFKeyNames(t *testing.T) {
	cases := []struct {
		name       string
		key        tea.KeyPressMsg
		wantHeader bool
	}{
		{"f4", pressKey(tea.KeyF4), false},
		{"shift+f4", pressMod(tea.KeyF4, tea.ModShift), true},
		{"f16", pressKey(tea.KeyF16), true},
		{"shift+f16", pressMod(tea.KeyF16, tea.ModShift), true},
	}
	for _, c := range cases {
		key, wantHeader := c.name, c.wantHeader
		m := newTestModel(t)
		withAnswer(m, "вопрос", "ответ")

		m.Update(c.key)
		if m.savePDF == nil {
			t.Errorf("%s не открыла окно сохранения", key)
			continue
		}
		if m.savePDF.withHeader != wantHeader {
			t.Errorf("%s: withHeader=%v, ожидалось %v", key, m.savePDF.withHeader, wantHeader)
		}
	}
}

// TestSavePDFWithoutAnswer: сохранять нечего — окно не открывается.
func TestSavePDFWithoutAnswer(t *testing.T) {
	m := newTestModel(t)
	clearTranscript(m)

	m.openSavePDF(false)
	if m.savePDF != nil {
		t.Error("окно открылось при пустой ленте")
	}
	if m.statusMsg == "" {
		t.Error("пользователю ничего не сказали")
	}
}

// TestSavePDFOverwriteFlow: существующий файл требует второго Enter,
// а правка имени отменяет уже данное согласие.
func TestSavePDFOverwriteFlow(t *testing.T) {
	m := newTestModel(t)
	withAnswer(m, "вопрос", "ответ")
	calls := catchPDFWrite(t)

	root := m.guard.Sandbox().Root()
	if err := os.WriteFile(filepath.Join(root, "отчёт.pdf"), []byte("старое"), 0o644); err != nil {
		t.Fatal(err)
	}

	m.openSavePDF(true)
	m.savePDF.input.SetValue("отчёт")

	if cmd := m.submitSavePDF(); cmd != nil {
		t.Fatal("файл существует, но запись пошла без подтверждения")
	}
	if m.savePDF == nil || m.savePDF.overwrite == nil {
		t.Fatal("не запрошено подтверждение перезаписи")
	}
	if view := m.savePDFView(); !strings.Contains(view, "уже есть") {
		t.Errorf("в окне нет предупреждения о существующем файле:\n%s", view)
	}

	// Правка имени снимает согласие: оно давалось на другой файл.
	typeRune(m, 'я')
	if m.savePDF.overwrite != nil {
		t.Error("согласие на перезапись пережило правку имени")
	}

	m.savePDF.input.SetValue("отчёт")
	m.submitSavePDF()
	cmd := m.submitSavePDF()
	if cmd == nil {
		t.Fatal("второй Enter не запустил запись")
	}
	cmd()

	if len(*calls) != 1 {
		t.Fatalf("вызовов записи %d, ожидался один", len(*calls))
	}
	c := (*calls)[0]
	if !c.overwrite {
		t.Error("запись пошла без разрешения перезаписи")
	}
	if !c.withHeader {
		t.Error("потерян признак «с вопросом и шапкой»")
	}
	if m.savePDF != nil {
		t.Error("окно осталось открытым после записи")
	}
}

// TestSavePDFReportsBadPath: ошибка пути остаётся в окне вместе с именем,
// чтобы его можно было поправить одной клавишей.
func TestSavePDFReportsBadPath(t *testing.T) {
	m := newTestModel(t)
	withAnswer(m, "вопрос", "ответ")
	m.openSavePDF(false)

	m.savePDF.input.SetValue(filepath.Join(t.TempDir(), "нет-такого-каталога", "файл"))
	if cmd := m.submitSavePDF(); cmd != nil {
		t.Fatal("запись пошла в несуществующий каталог")
	}
	if m.savePDF == nil {
		t.Fatal("окно закрылось вместо показа ошибки")
	}
	if !strings.Contains(m.savePDF.err, "каталог") {
		t.Errorf("непонятная ошибка: %q", m.savePDF.err)
	}
	if !strings.Contains(m.savePDFView(), "каталог") {
		t.Error("ошибка не показана в окне")
	}
}

// TestResolvePDFPathVariants перебирает все способы назвать файл.
func TestResolvePDFPathVariants(t *testing.T) {
	m := newTestModel(t)
	root := m.guard.Sandbox().Root()
	for _, dir := range []string{"notes", "каталог.pdf"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()

	cases := []struct {
		name string
		in   string
		want string // ожидаемый абсолютный путь, пусто — ждём ошибку
	}{
		{"голое имя", "ответ.pdf", filepath.Join(root, "ответ.pdf")},
		{"без расширения", "ответ", filepath.Join(root, "ответ.pdf")},
		{"чужое расширение", "ответ.txt", filepath.Join(root, "ответ.txt.pdf")},
		{"подкаталог", "notes/ответ", filepath.Join(root, "notes", "ответ.pdf")},
		{"абсолютный вне песочницы", filepath.Join(outside, "ответ"), filepath.Join(outside, "ответ.pdf")},
		{"в кавычках", `"ответ.pdf"`, filepath.Join(root, "ответ.pdf")},
		{"пусто", "   ", ""},
		{"нет каталога", filepath.Join(root, "нет", "ответ.pdf"), ""},
		{"цель — каталог", "каталог.pdf", ""},
	}

	for _, c := range cases {
		got, err := m.resolvePDFPath(c.in)
		if c.want == "" {
			if err == nil {
				t.Errorf("%s: ожидалась ошибка, получен %s", c.name, got.abs)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got.abs != c.want {
			t.Errorf("%s: путь %s, ожидался %s", c.name, got.abs, c.want)
		}
	}
}

// TestResolvePDFPathExpandsHome: «~» раскрывается — ради этого команда
// и разрешает выходить за песочницу.
func TestResolvePDFPathExpandsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("нет домашнего каталога")
	}
	m := newTestModel(t)

	got, err := m.resolvePDFPath("~/ответ")
	if err != nil {
		t.Fatalf("resolvePDFPath: %v", err)
	}
	if want := filepath.Join(home, "ответ.pdf"); got.abs != want {
		t.Errorf("путь %s, ожидался %s", got.abs, want)
	}
}

func TestAddPDFExt(t *testing.T) {
	cases := map[string]string{
		"ответ":     "ответ.pdf",
		"ответ.pdf": "ответ.pdf",
		"ответ.PDF": "ответ.PDF",
		"ответ.txt": "ответ.txt.pdf",
		"отчёт.v2":  "отчёт.v2.pdf",
	}
	for in, want := range cases {
		if got := addPDFExt(in); got != want {
			t.Errorf("addPDFExt(%q) = %q, ожидалось %q", in, got, want)
		}
	}
}

// TestDefaultPDFName: предложенное имя обязано остаться именем файла,
// а не превратиться в путь.
func TestDefaultPDFName(t *testing.T) {
	cases := map[string]string{
		"Что такое горутины":         "Что_такое_горутины.pdf",
		"первая строка\nвторая":      "первая_строка.pdf",
		"путь/в/каталог":             "путьвкаталог.pdf",
		"  ":                         "ответ.pdf",
		"":                           "ответ.pdf",
		strings.Repeat("длинно", 20): strings.Repeat("длинно", 8) + ".pdf",
	}
	for in, want := range cases {
		if got := defaultPDFName(in); got != want {
			t.Errorf("defaultPDFName(%q) = %q, ожидалось %q", in, got, want)
		}
		if strings.ContainsRune(defaultPDFName(in), filepath.Separator) {
			t.Errorf("в предложенном имени остался разделитель пути: %q", defaultPDFName(in))
		}
	}
}

// TestSaveToPDFCommandEndToEnd — сквозная проверка команды: документ
// действительно ложится на диск и читается нашим же разборщиком PDF.
func TestSaveToPDFCommandEndToEnd(t *testing.T) {
	m := newTestModel(t)
	withAnswer(m, "Что такое каналы", "## Каналы\n\nКанал — **труба** между горутинами.")

	cmd := m.saveToPDFCmd("ответ")
	if cmd == nil {
		t.Fatal("команда не вернула задания на запись")
	}
	m.Update(cmd())

	path := filepath.Join(m.guard.Sandbox().Root(), "ответ.pdf")
	res, err := pdf.ExtractFile(path, pdf.Options{})
	if err != nil {
		t.Fatalf("получившийся файл не читается: %v", err)
	}
	flat := strings.Join(strings.Fields(res.Text()), " ")
	for _, want := range []string{"Каналы", "труба между горутинами"} {
		if !strings.Contains(flat, want) {
			t.Errorf("в документе нет %q, извлечено:\n%s", want, flat)
		}
	}

	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockNotice || !strings.Contains(last.text, "ответ.pdf") {
		t.Errorf("нет сообщения об удачном сохранении: %+v", last)
	}
}

// TestSaveToPDFCommandRefusesExisting: из командной строки чужой файл
// не затирается — там негде спросить подтверждение.
func TestSaveToPDFCommandRefusesExisting(t *testing.T) {
	m := newTestModel(t)
	withAnswer(m, "вопрос", "ответ")
	calls := catchPDFWrite(t)

	path := filepath.Join(m.guard.Sandbox().Root(), "ответ.pdf")
	if err := os.WriteFile(path, []byte("старое"), 0o644); err != nil {
		t.Fatal(err)
	}

	if cmd := m.saveToPDFCmd("ответ.pdf"); cmd != nil {
		t.Fatal("команда пошла записывать поверх существующего файла")
	}
	if len(*calls) != 0 {
		t.Fatalf("запись всё-таки была: %+v", *calls)
	}
	if last := m.blocks[len(m.blocks)-1]; last.kind != blockError {
		t.Errorf("нет сообщения об отказе: %+v", last)
	}
	if data, _ := os.ReadFile(path); string(data) != "старое" {
		t.Error("существующий файл изменён")
	}
}
