package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Cyber-Watcher/ollchat/internal/config"
)

// Подсказка открывается только когда косая черта — первый знак строки.
//
// Иначе она вылезала бы посреди вопроса «что делает /api/chat» и мешала
// набирать. Это и есть условие, поставленное владельцем.
func TestCommandPrefixOnlyAtLineStart(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"/", true},
		{"/mo", true},
		{"/kb add", true},
		{"", false},
		{"что делает /api/chat", false},
		{" /model", false}, // пробел впереди — это уже не команда
		{"/model\nвторая строка", false},
	}
	for _, c := range cases {
		if _, got := commandPrefix(c.in); got != c.want {
			t.Errorf("commandPrefix(%q) = %v, ожидалось %v", c.in, got, c.want)
		}
	}
}

// Одна косая черта показывает весь список, набор его сужает.
func TestMatchCommandsNarrows(t *testing.T) {
	all := matchCommands("/", nil)
	if len(all) < 30 {
		t.Fatalf("по «/» найдено %d команд — ожидался весь список", len(all))
	}
	mo := matchCommands("/mo", nil)
	if len(mo) == 0 || len(mo) >= len(all) {
		t.Fatalf("по «/mo» найдено %d из %d — список должен сузиться", len(mo), len(all))
	}
	for _, e := range mo {
		if !strings.HasPrefix(e.Full, "/mo") {
			t.Errorf("в выдаче по «/mo» оказалась %q", e.Full)
		}
	}
	if len(matchCommands("/такойкомандынет", nil)) != 0 {
		t.Error("несуществующая команда не должна ничего находить")
	}
}

// Отбор идёт по началу, а не по вхождению.
//
// Подстрочный поиск выдавал бы `/savetopdf` на запрос `/top`, и это читается
// как ошибка отбора, а не как удобство.
func TestMatchCommandsIsPrefixNotSubstring(t *testing.T) {
	for _, e := range matchCommands("/con", nil) {
		if e.Full == "/addcontext" {
			t.Fatal("«/con» не должно находить /addcontext: отбор по началу строки")
		}
	}
	if len(matchCommands("/con", nil)) == 0 {
		t.Error("«/con» должно находить /context и /config")
	}
}

// Псевдоним ищет, но в списке показывается основное написание.
func TestMatchCommandsFindsByAlias(t *testing.T) {
	got := matchCommands("/q", nil)
	var found bool
	for _, e := range got {
		if e.Full == "/quit" {
			found = true
		}
		if e.Full == "/q" {
			t.Error("псевдоним не должен показываться отдельной строкой")
		}
	}
	if !found {
		t.Fatalf("«/q» должно находить /quit, получено %v", names(got))
	}
}

// Подкоманды идут следом за своей командой и находятся по её началу.
func TestSubcommandsFollowParent(t *testing.T) {
	got := matchCommands("/kb", nil)
	if len(got) < 5 {
		t.Fatalf("по «/kb» найдено %d — ожидались подкоманды", len(got))
	}
	if got[0].Full != "/kb" {
		t.Errorf("первой должна идти сама команда, а не %q", got[0].Full)
	}
	var sub bool
	for _, e := range got {
		if e.Full == "/kb add" {
			sub = true
		}
	}
	if !sub {
		t.Errorf("подкоманда /kb add не найдена: %v", names(got))
	}
	// Набранное с пробелом отбирает уже среди подкоманд.
	narrow := matchCommands("/kb ad", nil)
	if len(narrow) != 1 || narrow[0].Full != "/kb add" {
		t.Errorf("«/kb ad» должно оставить одну /kb add, получено %v", names(narrow))
	}
}

func names(list []cmdEntry) []string {
	out := make([]string, len(list))
	for i, e := range list {
		out[i] = e.Full
	}
	return out
}

// Стрелки ходят по списку и заворачиваются на краях.
func TestCmdMenuMoveWraps(t *testing.T) {
	m := &cmdMenu{entries: matchCommands("/kb", nil), rows: 4}
	n := len(m.entries)

	m.move(-1)
	if m.cursor != n-1 {
		t.Errorf("вверх с первой строки должно вести в конец, а не в %d", m.cursor)
	}
	m.move(1)
	if m.cursor != 0 {
		t.Errorf("вниз с последней должно вести в начало, а не в %d", m.cursor)
	}
	// Курсор всегда виден: смещение подтягивается за ним.
	for i := 0; i < n; i++ {
		m.move(1)
		if m.cursor < m.offset || m.cursor >= m.offset+m.visibleRows() {
			t.Fatalf("курсор %d вне окна [%d, %d)", m.cursor, m.offset, m.offset+m.visibleRows())
		}
	}
}

// Высота панели — заданное число команд плюс рамка и заголовок.
func TestCmdMenuHeightFollowsSetting(t *testing.T) {
	entries := matchCommands("/", nil)
	four := &cmdMenu{entries: entries, rows: 4}
	if got := four.height(); got != 7 {
		t.Errorf("при четырёх строках высота %d, ожидалось 7 (рамка 2 + заголовок 1)", got)
	}
	eight := &cmdMenu{entries: entries, rows: 8}
	if got := eight.height(); got != 11 {
		t.Errorf("при восьми строках высота %d, ожидалось 11", got)
	}
	byDefault := &cmdMenu{entries: entries}
	if got := byDefault.height(); got != defaultCmdMenuRows+3 {
		t.Errorf("без настройки высота %d, ожидалось %d", got, defaultCmdMenuRows+3)
	}
	empty := &cmdMenu{entries: nil, rows: 4}
	if got := empty.height(); got != 4 {
		t.Errorf("пустой список должен занимать 4 строки, а не %d", got)
	}
}

// Описание, не помещающееся в строку, сворачивается многоточием.
func TestCmdMenuTruncatesToWidth(t *testing.T) {
	m := &cmdMenu{prefix: "/", entries: matchCommands("/", nil), rows: 4}
	const width = 46
	view := m.view(width)
	for i, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Fatalf("строка %d шире окна: %d против %d", i, w, width)
		}
	}
	if !strings.Contains(view, "…") {
		t.Error("на узком экране описания должны сворачиваться многоточием")
	}
}

// Призрак дописывает хвост выбранной команды и исчезает, когда дописывать нечего.
func TestCmdMenuGhost(t *testing.T) {
	m := &cmdMenu{prefix: "/mod", entries: matchCommands("/mod", nil), rows: 4}
	e, _ := m.selected()
	if got, want := m.ghost(), e.Full[len("/mod"):]; got != want {
		t.Errorf("призрак %q, ожидался %q", got, want)
	}

	full := &cmdMenu{prefix: "/models", entries: matchCommands("/models", nil), rows: 4}
	if got := full.ghost(); got != "" {
		t.Errorf("у набранной целиком команды призрака быть не должно, получено %q", got)
	}

	// После перехода стрелкой на строку, которая не продолжает набранное,
	// призрак пропадает — иначе он врал бы о том, что выйдет по Enter.
	kb := &cmdMenu{prefix: "/kb", entries: matchCommands("/kb", nil), rows: 4}
	kb.move(1)
	if sel, _ := kb.selected(); strings.HasPrefix(sel.Full, "/kb") && kb.ghost() == "" {
		t.Log("подкоманда продолжает набранное — призрак уместен")
	}
	kb.entries = append(kb.entries, cmdEntry{Full: "/quit"})
	kb.cursor = len(kb.entries) - 1
	if got := kb.ghost(); got != "" {
		t.Errorf("выбранное не продолжает набранное, призрака быть не должно: %q", got)
	}
}

// Наложение призрака не меняет ширину строки и не съедает управляющие коды.
func TestOverlayAtKeepsWidth(t *testing.T) {
	line := "› /mod" + strings.Repeat(" ", 20)
	out := overlayAt(line, 6, "els")
	if lipgloss.Width(out) != lipgloss.Width(line) {
		t.Fatalf("ширина изменилась: %d против %d", lipgloss.Width(out), lipgloss.Width(line))
	}
	if !strings.Contains(out, "/models") {
		t.Errorf("призрак не встал на место: %q", out)
	}

	styled := lipgloss.NewStyle().Bold(true).Render("› /mod") + strings.Repeat(" ", 20)
	got := overlayAt(styled, lipgloss.Width("› /mod"), "els")
	if lipgloss.Width(got) != lipgloss.Width(styled) {
		t.Errorf("на размеченной строке ширина изменилась: %d против %d",
			lipgloss.Width(got), lipgloss.Width(styled))
	}
	if !strings.Contains(got, "\x1b") {
		t.Error("управляющие коды строки потерялись")
	}
}

// Вставка команды кладёт её в строку с пробелом и ничего не отправляет.
func TestCmdEntryInsertAddsSpace(t *testing.T) {
	e := cmdEntry{Full: "/model", Args: "<имя>"}
	if got := e.insert(); got != "/model " {
		t.Errorf("вставка %q, ожидалось «/model » с пробелом", got)
	}
}

// Сверку таблицы команд с разбором см. в commanddrift_test.go: там имена
// разбора читаются из самого разбора (таблицы подкоманд и ветки switch),
// а не переписываются в тест руками. Прежняя проверка держала список имён
// копией и потому пропускала ровно то, ради чего была написана: /graph find
// значилась в меню, в её списке была, а ветки разбора не имела вовсе.

// Справка собирается из таблицы и содержит каждую команду верхнего уровня.
func TestHelpBuiltFromTable(t *testing.T) {
	help := helpText(nil)
	for _, c := range commands {
		if !strings.Contains(help, "/"+c.Name) {
			t.Errorf("команда /%s отсутствует в справке", c.Name)
		}
	}
	if !strings.Contains(help, "Окно контекста меняется посреди сеанса") {
		t.Error("постоянная часть справки потерялась")
	}
	// Подкоманды тоже должны быть в справке: без них /context выглядит
	// командой, которая только показывает.
	for _, sub := range []string{"/context set", "/context add", "/context max"} {
		if !strings.Contains(help, sub) {
			t.Errorf("в справке нет %s", sub)
		}
	}
}

// ── Сквозная проверка через Model ────────────────────────────────────────────

func typeKeys(m *Model, s string) {
	for _, r := range s {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// Косая черта открывает подсказку, набор её сужает, Tab подставляет команду.
//
// Это и есть проверка всей цепочки, а не одного меню: клавиша дошла до поля,
// поле изменилось, меню пересобралось, кадр стал ниже на высоту панели.
func TestCmdMenuEndToEnd(t *testing.T) {
	m := newTestModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	before := m.vp.Height()
	typeKeys(m, "/")
	if m.cmds == nil {
		t.Fatal("после «/» подсказка не открылась")
	}
	if len(m.cmds.entries) < 30 {
		t.Errorf("сразу после «/» должен быть весь список, а не %d", len(m.cmds.entries))
	}
	if m.vp.Height() != before-m.cmds.height() {
		t.Errorf("лента не уступила место панели: было %d, стало %d, панель %d",
			before, m.vp.Height(), m.cmds.height())
	}

	typeKeys(m, "mod")
	if m.cmds == nil {
		t.Fatal("подсказка закрылась посреди набора")
	}
	for _, e := range m.cmds.entries {
		if !strings.HasPrefix(e.Full, "/mod") {
			t.Errorf("список не отобран: %q", e.Full)
		}
	}

	// Tab подставляет выбранное и ничего не отправляет. Считать надо прирост:
	// на старте в ленте уже есть уведомления о состоянии.
	blocksBefore := len(m.blocks)
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if got := m.ta.Value(); !strings.HasPrefix(got, "/mod") || !strings.HasSuffix(got, " ") {
		t.Errorf("после Tab в поле %q, ожидалась команда с пробелом", got)
	}
	if len(m.blocks) != blocksBefore {
		t.Errorf("Tab не должен ничего отправлять: блоков было %d, стало %d",
			blocksBefore, len(m.blocks))
	}
}

// Косая черта посреди вопроса подсказку не открывает.
func TestCmdMenuIgnoresSlashInsideText(t *testing.T) {
	m := newTestModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	typeKeys(m, "что делает /api/chat")
	if m.cmds != nil {
		t.Fatal("подсказка открылась на косой черте посреди текста")
	}
}

// Стрелки водят по списку, Esc убирает подсказку и возвращает строки ленте.
func TestCmdMenuArrowsAndEsc(t *testing.T) {
	m := newTestModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	typeKeys(m, "/kb")

	m.Update(arrowDown())
	if m.cmds.cursor != 1 {
		t.Errorf("стрелка вниз не сдвинула курсор: %d", m.cmds.cursor)
	}
	m.Update(arrowUp())
	if m.cmds.cursor != 0 {
		t.Errorf("стрелка вверх не вернула курсор: %d", m.cmds.cursor)
	}

	tall := m.vp.Height()
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.cmds != nil {
		t.Fatal("Esc не убрал подсказку")
	}
	if m.vp.Height() <= tall {
		t.Error("после закрытия лента должна снова вырасти")
	}
	if m.ta.Value() != "/kb" {
		t.Errorf("Esc не должен трогать набранное, в поле %q", m.ta.Value())
	}
}

// Высота панели берётся из настройки.
func TestCmdMenuRowsFromConfig(t *testing.T) {
	m := newTestModelWith(t, func(c *config.Config) { c.Input.CommandRows = 8 })
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	typeKeys(m, "/")
	if m.cmds == nil {
		t.Fatal("подсказка не открылась")
	}
	if got := m.cmds.height(); got != 11 {
		t.Errorf("при command_rows = 8 высота %d, ожидалось 11", got)
	}
}

// Призрак виден в отрисовке поля ввода и не попадает в отправляемый текст.
func TestGhostShownButNotSent(t *testing.T) {
	m := newTestModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	typeKeys(m, "/mode")

	view := m.ghostOverlay(m.ta.View())
	if !strings.Contains(view, "/mode") {
		t.Fatalf("набранное пропало из отрисовки: %q", firstLine(view))
	}
	if m.ta.Value() != "/mode" {
		t.Errorf("призрак попал в значение поля: %q", m.ta.Value())
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// Стрелка вправо дополняет наравне с Tab.
//
// Так принимают подсказку в fish и zsh, и владелец попросил того же.
func TestCmdMenuRightArrowCompletes(t *testing.T) {
	m := newTestModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	typeKeys(m, "/mod")

	m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if got := m.ta.Value(); !strings.HasPrefix(got, "/mod") || len(got) <= len("/mod ") {
		t.Fatalf("вправо не дополнила: в поле %q", got)
	}
}

// Когда дополнять нечего, вправо двигает курсор, а не пропадает впустую.
func TestCmdMenuRightArrowMovesCursorWhenComplete(t *testing.T) {
	m := newTestModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	typeKeys(m, "/models")
	// Курсор в конце — уводим его влево, чтобы было куда двигать вправо.
	m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	before := m.ta.Value()

	m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.ta.Value() != before {
		t.Errorf("вправо не должна менять текст набранной целиком команды: %q → %q",
			before, m.ta.Value())
	}
}

// Команд ненастроенной возможности в меню и справке нет.
//
// На машине без библиотеки книг /kb и /graph только сбивают с толку: человек
// их видит, зовёт и получает «коллекций нет». Отсутствие раздела в конфиге —
// это и есть «мне не нужно».
func TestCommandsHiddenWithoutConfigSection(t *testing.T) {
	// Конфиг, где из наших разделов есть только web.
	has := sectionCheck(func(s string) bool { return s == "web" })

	for _, in := range []string{"/kb", "/graph", "/confluencetoken"} {
		if got := matchCommands(in, has); len(got) != 0 {
			t.Errorf("%s не должна показываться без своего раздела: %v", in, got)
		}
	}
	// Обычные команды на месте — отбор не должен резать всё подряд.
	if got := matchCommands("/context", has); len(got) == 0 {
		t.Error("/context показывается всегда, раздела ей не нужно")
	}
	help := helpText(has)
	for _, s := range []string{"/kb ", "/graph ", "/confluencetoken"} {
		if strings.Contains(help, s) {
			t.Errorf("в справке осталось %q при отсутствующем разделе", s)
		}
	}
	if !strings.Contains(help, "/context") {
		t.Error("из справки пропало то, что скрывать не просили")
	}
}

// Разделы на месте — команды на месте.
func TestCommandsShownWithConfigSection(t *testing.T) {
	has := sectionCheck(func(string) bool { return true })
	for _, in := range []string{"/kb", "/graph", "/confluencetoken"} {
		if got := matchCommands(in, has); len(got) == 0 {
			t.Errorf("%s должна показываться при своём разделе", in)
		}
	}
}
