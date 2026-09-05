package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// prepareTree раскладывает в рабочем каталоге дерево, на котором видно всё
// важное: каталоги выше файлов, скрытое прячется, фильтрация по началу имени.
func prepareTree(t *testing.T, m *Model) {
	t.Helper()
	root := m.guard.Sandbox().Root()
	for _, dir := range []string{"internal", "internal/ui", "notes", ".git"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatalf("подготовка: %v", err)
		}
	}
	for _, file := range []string{"main.go", "README.md", ".env", "internal/ui/view.go"} {
		if err := os.WriteFile(filepath.Join(root, file), []byte("x"), 0o600); err != nil {
			t.Fatalf("подготовка: %v", err)
		}
	}
}

// typeText прогоняет строку через Update по символу — так же, как её набирают.
func typeText(m *Model, s string) {
	for _, r := range s {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func TestAtOpensFileMenu(t *testing.T) {
	m := newTestModel(t)
	prepareTree(t, m)

	if m.files != nil {
		t.Fatal("до набора @ списка быть не должно")
	}
	typeText(m, "@")

	if m.files == nil {
		t.Fatal("после @ должен открыться список файлов")
	}
	names := entryNames(m.files)
	if len(names) == 0 {
		t.Fatal("список пуст")
	}
	// Каталоги идут первыми.
	if names[0] != "internal" && names[0] != "notes" {
		t.Errorf("первыми должны идти каталоги, получено %v", names)
	}
	// Скрытое не показываем, пока о нём не спросят.
	for _, n := range names {
		if strings.HasPrefix(n, ".") {
			t.Errorf("скрытое имя %q не должно попадать в список: %v", n, names)
		}
	}
}

func TestFileMenuFiltersAsYouType(t *testing.T) {
	m := newTestModel(t)
	prepareTree(t, m)

	typeText(m, "@RE")
	if m.files == nil {
		t.Fatal("список должен быть открыт")
	}
	names := entryNames(m.files)
	if len(names) != 1 || names[0] != "README.md" {
		t.Errorf("фильтр по началу имени должен быть нечувствителен к регистру, получено %v", names)
	}
}

// Скрытое показывается, когда его спрашивают явно.
func TestFileMenuShowsHiddenOnDemand(t *testing.T) {
	m := newTestModel(t)
	prepareTree(t, m)

	typeText(m, "@.")
	names := entryNames(m.files)
	if !contains(names, ".env") || !contains(names, ".git") {
		t.Errorf("по точке должны показываться скрытые имена, получено %v", names)
	}
}

func TestArrowsMoveSelection(t *testing.T) {
	m := newTestModel(t)
	prepareTree(t, m)
	typeText(m, "@")

	first := m.files.cursor
	m.Update(pressKey(tea.KeyDown))
	if m.files.cursor == first {
		t.Error("стрелка вниз должна двигать выбор")
	}
	m.Update(pressKey(tea.KeyUp))
	if m.files.cursor != first {
		t.Error("стрелка вверх должна возвращать выбор обратно")
	}
}

// Каталог вставляется со слешем, и список сразу показывает его содержимое —
// так путь набирается вглубь без повторного @.
func TestSelectingDirectoryDescends(t *testing.T) {
	m := newTestModel(t)
	prepareTree(t, m)

	typeText(m, "@internal")
	m.Update(pressKey(tea.KeyEnter))

	if got := m.ta.Value(); got != "@internal/" {
		t.Fatalf("после выбора каталога в промпте %q, ожидалось \"@internal/\"", got)
	}
	if m.files == nil {
		t.Fatal("на каталоге список должен оставаться открытым")
	}
	if names := entryNames(m.files); !contains(names, "ui") {
		t.Errorf("список должен показывать содержимое каталога, получено %v", names)
	}
}

// Файл вставляется целиком, список закрывается, дальше можно писать вопрос.
func TestSelectingFileInsertsPathAndCloses(t *testing.T) {
	m := newTestModel(t)
	prepareTree(t, m)

	typeText(m, "@internal")
	m.Update(pressKey(tea.KeyEnter)) // internal/
	typeText(m, "ui/")
	m.Update(pressKey(tea.KeyEnter)) // internal/ui/view.go

	if got := m.ta.Value(); got != "@internal/ui/view.go " {
		t.Fatalf("в промпте %q, ожидалось \"@internal/ui/view.go \"", got)
	}
	if m.files != nil {
		t.Error("после выбора файла список должен закрываться")
	}

	// И дальше набирается обычный текст.
	typeText(m, "что тут")
	if got := m.ta.Value(); got != "@internal/ui/view.go что тут" {
		t.Errorf("после выбора файла текст должен набираться как обычно: %q", got)
	}
}

// Enter отправляет вопрос только когда список закрыт.
func TestEnterSendsWhenMenuClosed(t *testing.T) {
	m := newTestModel(t)
	prepareTree(t, m)

	typeText(m, "@main.go")
	m.Update(pressKey(tea.KeyEnter)) // выбор файла, не отправка
	if m.conv.Len() != 0 {
		t.Fatal("Enter при открытом списке не должен отправлять вопрос")
	}

	m.Update(pressKey(tea.KeyEnter)) // теперь список закрыт — отправка
	if m.conv.Len() != 1 {
		t.Errorf("Enter при закрытом списке должен отправлять вопрос, сообщений: %d", m.conv.Len())
	}
}

func TestEscClosesFileMenu(t *testing.T) {
	m := newTestModel(t)
	prepareTree(t, m)

	typeText(m, "@doc")
	m.Update(pressKey(tea.KeyEscape))

	if m.files != nil {
		t.Error("Esc должен закрывать список")
	}
	if got := m.ta.Value(); got != "@doc" {
		t.Errorf("Esc не должен трогать набранный текст, в промпте %q", got)
	}
}

// Пробел после токена закрывает список: пользователь ушёл писать вопрос.
func TestSpaceClosesFileMenu(t *testing.T) {
	m := newTestModel(t)
	prepareTree(t, m)

	typeText(m, "@notes")
	if m.files == nil {
		t.Fatal("подготовка: список должен быть открыт")
	}
	typeText(m, " ")
	if m.files != nil {
		t.Error("после пробела список должен закрываться")
	}
}

// @ внутри слова — не команда: почта и ссылки не должны открывать список.
func TestAtInsideWordDoesNotOpenMenu(t *testing.T) {
	m := newTestModel(t)
	prepareTree(t, m)

	typeText(m, "пиши на user@example")
	if m.files != nil {
		t.Errorf("@ внутри слова не должен открывать список: %q", m.ta.Value())
	}
}

func TestLastAtToken(t *testing.T) {
	cases := []struct {
		in    string
		token string
		ok    bool
	}{
		{"@", "@", true},
		{"@doc", "@doc", true},
		{"смотри @internal/ui", "@internal/ui", true},
		{"@notes ", "", false},
		{"user@example.com", "", false},
		{"", "", false},
		{"без собаки", "", false},
		{"@один @два", "@два", true},
	}
	for _, c := range cases {
		token, ok := lastAtToken(c.in)
		if ok != c.ok || token != c.token {
			t.Errorf("lastAtToken(%q) = %q,%v — ожидалось %q,%v", c.in, token, ok, c.token, c.ok)
		}
	}
}

func TestSplitToken(t *testing.T) {
	cases := []struct{ in, dir, prefix string }{
		{"@", "", ""},
		{"@doc", "", "doc"},
		{"@internal/", "internal", ""},
		{"@internal/u", "internal", "u"},
		{"@internal/ui/vi", "internal/ui", "vi"},
	}
	for _, c := range cases {
		dir, prefix := splitToken(c.in)
		if dir != c.dir || prefix != c.prefix {
			t.Errorf("splitToken(%q) = %q,%q — ожидалось %q,%q", c.in, dir, prefix, c.dir, c.prefix)
		}
	}
}

// Путь наружу песочницы не показывается: приложить его всё равно нечем.
func TestFileMenuStaysInsideSandbox(t *testing.T) {
	m := newTestModel(t)
	prepareTree(t, m)

	typeText(m, "@../")
	if m.files != nil && len(m.files.entries) > 0 {
		t.Errorf("за пределами рабочего каталога список должен быть пуст, получено %v", entryNames(m.files))
	}
}

func entryNames(f *fileMenu) []string {
	if f == nil {
		return nil
	}
	out := make([]string, 0, len(f.entries))
	for _, e := range f.entries {
		out = append(out, e.name)
	}
	return out
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// Панель списка обязана умещаться в окно вместе с остальными зонами.
// Если её высоту не вычесть из ленты, экран становится выше терминала
// и нижние зоны уезжают за край — именно так это и выглядело у пользователя.
func TestFileMenuKeepsLayoutWithinTerminal(t *testing.T) {
	m := newTestModel(t) // окно 100×30
	prepareTree(t, m)
	fillTranscript(m, 200)

	lines := func() int { return len(strings.Split(m.View().Content, "\n")) }
	if got := lines(); got != 30 {
		t.Fatalf("подготовка: экран занимает %d строк, а окно 30", got)
	}

	typeText(m, "@")
	if m.files == nil {
		t.Fatal("подготовка: список должен быть открыт")
	}
	if got := lines(); got != 30 {
		t.Errorf("со списком экран занимает %d строк вместо 30 — панель уедет за край", got)
	}

	m.Update(pressKey(tea.KeyEscape))
	if got := lines(); got != 30 {
		t.Errorf("после закрытия списка экран занимает %d строк вместо 30", got)
	}
}

// Список стоит над разделителем, а не между ним и полем ввода.
func TestFileMenuSitsAboveSeparator(t *testing.T) {
	m := newTestModel(t)
	prepareTree(t, m)
	typeText(m, "@")

	lines := strings.Split(m.View().Content, "\n")
	menuRow, sepRow, inputRow := -1, -1, -1
	for i, l := range lines {
		switch {
		case strings.Contains(l, "файлы рабочего каталога"):
			menuRow = i
		// Рамка панели тоже нарисована символами «─», поэтому разделитель
		// узнаём по отсутствию углов и боковин рамки.
		case sepRow < 0 && strings.Count(l, "─") > 10 && !strings.ContainsAny(l, "╭╮╰╯│"):
			sepRow = i
		case strings.Contains(l, "›"):
			if inputRow < 0 {
				inputRow = i
			}
		}
	}
	if menuRow < 0 || sepRow < 0 || inputRow < 0 {
		t.Fatalf("не нашёл зоны: список=%d разделитель=%d ввод=%d", menuRow, sepRow, inputRow)
	}
	if !(menuRow < sepRow && sepRow < inputRow) {
		t.Errorf("порядок зон список(%d) → разделитель(%d) → ввод(%d) нарушен", menuRow, sepRow, inputRow)
	}
}

// Видно ровно пять строк, остальное прокручивается.
func TestFileMenuShowsFiveRowsAndScrolls(t *testing.T) {
	m := newTestModel(t)
	root := m.guard.Sandbox().Root()
	for _, n := range []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8"} {
		if err := os.WriteFile(filepath.Join(root, n+".txt"), []byte("x"), 0o600); err != nil {
			t.Fatalf("подготовка: %v", err)
		}
	}

	typeText(m, "@a")
	if got := len(m.files.entries); got != 8 {
		t.Fatalf("подготовка: в списке %d строк, ожидалось 8", got)
	}

	visible := func() int {
		n := 0
		for _, l := range strings.Split(m.files.view(m.width), "\n") {
			if strings.Contains(l, ".txt") {
				n++
			}
		}
		return n
	}
	if got := visible(); got != maxFileMenuRows {
		t.Errorf("видно %d строк, ожидалось %d", got, maxFileMenuRows)
	}

	// Уходим за нижнюю границу видимого окна — список должен прокрутиться.
	for i := 0; i < 6; i++ {
		m.Update(pressKey(tea.KeyDown))
	}
	if m.files.offset == 0 {
		t.Error("при уходе курсора вниз список должен прокручиваться")
	}
	if got := visible(); got != maxFileMenuRows {
		t.Errorf("после прокрутки видно %d строк, ожидалось %d", got, maxFileMenuRows)
	}
	if m.files.cursor < m.files.offset || m.files.cursor >= m.files.offset+maxFileMenuRows {
		t.Error("выбранная строка должна оставаться видимой")
	}
}

// Колесо мыши над панелью прокручивает список, а не ленту.
func TestMouseWheelScrollsFileMenu(t *testing.T) {
	m := newTestModel(t)
	prepareTree(t, m)
	fillTranscript(m, 200)
	typeText(m, "@")

	beforeCursor := m.files.cursor
	beforeOffset := m.vp.YOffset()
	row := m.filesTop() + 1

	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 5, Y: row})
	if m.files.cursor == beforeCursor {
		t.Error("колесо над списком должно двигать выбор")
	}
	if m.vp.YOffset() != beforeOffset {
		t.Error("колесо над списком не должно прокручивать ленту")
	}

	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 5, Y: row})
	if m.files.cursor != beforeCursor {
		t.Error("колесо вверх должно возвращать выбор обратно")
	}

	// А над лентой колесо по-прежнему листает ленту.
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 5, Y: transcriptTop + 1})
	if m.vp.YOffset() == beforeOffset {
		t.Error("над лентой колесо должно прокручивать ленту")
	}
}
