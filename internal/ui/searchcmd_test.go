package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/find"
)

// Разбор доводов /search: ключи в любом порядке, число — только когда за ним
// есть текст.
func TestParseSearchArgs(t *testing.T) {
	cases := []struct {
		in    string
		query string
		topK  int
		full  bool
		rr    bool
		bad   bool
	}{
		{in: "как связаны RAG и дообучение", query: "как связаны RAG и дообучение", topK: 8},
		{in: "-f 3 goroutine scheduler", query: "goroutine scheduler", topK: 3, full: true},
		{in: "3 -f goroutine", query: "goroutine", topK: 3, full: true},
		{in: "--full -r 5 память", query: "память", topK: 5, full: true, rr: true},
		// Голое число без текста — это и есть запрос: «/search 2026» спрашивает
		// про год, а не просит две тысячи двадцать шесть выдержек.
		{in: "2026", query: "2026", topK: 8},
		{in: "-f", bad: true},
		{in: "", bad: true},
		{in: "-x запрос", bad: true},
		{in: "0 запрос", bad: true},
		// Потолок: сотня кусков в ленте не читается.
		{in: "500 запрос", query: "запрос", topK: maxSearchTopK},
	}
	for _, c := range cases {
		got, err := parseSearchArgs(c.in, 8)
		if c.bad {
			if err == nil {
				t.Errorf("%q: ожидалась ошибка, вышло %+v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: неожиданная ошибка: %v", c.in, err)
			continue
		}
		if got.Query != c.query || got.TopK != c.topK || got.Full != c.full || got.Rerank != c.rr {
			t.Errorf("%q: вышло %+v, ожидалось запрос=%q topK=%d full=%v rr=%v",
				c.in, got, c.query, c.topK, c.full, c.rr)
		}
	}
}

// Ошибка разбора объясняет и показывает примеры: «неверный довод» отправляет
// человека гадать.
func TestSearchUsageIsHelpful(t *testing.T) {
	_, err := parseSearchArgs("-x", 8)
	if err == nil {
		t.Fatal("неизвестный ключ должен отклоняться")
	}
	for _, want := range []string{"-f", "-r", "/search"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("в объяснении нет %q: %s", want, err)
		}
	}
}

// Устаревшая выдача не затирает свежую: пока считался первый поиск, человек
// успел задать второй.
func TestStaleSearchResultIgnored(t *testing.T) {
	m := newTestModel(t)
	m.gen.find = 2
	m.finds = []find.Excerpt{{ID: "books/1#1", Book: "свежая"}}

	m.applySearchResult(searchDoneMsg{gen: 1, res: find.Result{
		Query: "старый", Excerpts: []find.Excerpt{{ID: "books/9#9", Book: "устаревшая"}},
	}})

	if len(m.finds) != 1 || m.finds[0].Book != "свежая" {
		t.Errorf("устаревший ответ затёр свежую выдачу: %+v", m.finds)
	}
}

// Свежая выдача запоминается: по ней работают /read <номер> и панель Ctrl+F.
func TestSearchResultRemembered(t *testing.T) {
	m := newTestModel(t)
	m.gen.find = 1
	m.applySearchResult(searchDoneMsg{gen: 1, res: find.Result{
		Query: "вопрос", Excerpts: []find.Excerpt{{ID: "books/1#1", Book: "К", Snippet: "текст"}},
	}})
	if len(m.finds) != 1 || m.findQuery != "вопрос" {
		t.Errorf("выдача не запомнена: %+v %q", m.finds, m.findQuery)
	}
}

// /search не держит Update: тяжёлое (открытие графа, вектор вопроса) уходит
// в фон, а команда возвращает управление сразу.
func TestSearchDoesNotBlockUpdate(t *testing.T) {
	m := newTestModel(t)
	// Коллекции нет — команда обязана сказать об этом, а не считать.
	if cmd := m.searchCmd("вопрос"); cmd != nil {
		t.Error("без коллекции считать нечего — фоновая работа не нужна")
	}
	if len(m.blocks) == 0 {
		t.Fatal("человеку ничего не сказано")
	}
}

// /read по номеру без выдачи объясняет, а не молчит и не падает.
func TestReadByNumberWithoutSearch(t *testing.T) {
	m := newTestModel(t)
	m.readCmd("3")
	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockError || !strings.Contains(last.text, "/search") {
		t.Errorf("ожидалось объяснение про /search: %+v", last)
	}
}

// /read без довода показывает, как им пользоваться.
func TestReadUsage(t *testing.T) {
	m := newTestModel(t)
	m.readCmd("")
	if !strings.Contains(m.blocks[len(m.blocks)-1].text, "/read") {
		t.Error("под пустым /read нужна подсказка об использовании")
	}
}

// Имя коллекции берётся из ссылки: «books/12#37» → «books».
func TestCollOfID(t *testing.T) {
	if got := collOfID("books/12#37"); got != "books" {
		t.Errorf("вышло %q, ожидалось books", got)
	}
	if got := collOfID("12#37"); got != "" {
		t.Errorf("без имени коллекции ожидалась пустая строка, вышло %q", got)
	}
}

// Ctrl+F открывает панель, а не съедается полем ввода: там эта клавиша значила
// «на символ вправо», и её пришлось отобрать.
func TestCtrlFOpensFindPanel(t *testing.T) {
	m := newTestModel(t)
	// Привязка поля ввода перенастроена: Ctrl+F там больше не значит ничего,
	// стрелка вправо осталась.
	for _, k := range m.ta.KeyMap.CharacterForward.Keys() {
		if k == "ctrl+f" {
			t.Fatal("Ctrl+F остался за полем ввода — панель не откроется")
		}
	}
	m.Update(pressCtrl('f'))
	if m.findPane == nil {
		t.Fatal("Ctrl+F не открыл панель найденного")
	}
	if strings.Contains(m.ta.Value(), "\x06") {
		t.Error("клавиша дошла до поля ввода")
	}
	m.Update(pressCtrl('f'))
	if m.findPane != nil {
		t.Error("повторное нажатие должно закрывать панель")
	}
}

// Панель найденного взаимно исключает панель вложений: две сразу сжали бы
// ленту до огрызка.
func TestFindPanelExcludesImages(t *testing.T) {
	m := newTestModel(t)
	m.toggleImagePanel()
	m.toggleFindPanel()
	if m.images != nil {
		t.Error("панель вложений должна была закрыться")
	}
	m.toggleImagePanel()
	if m.findPane != nil {
		t.Error("панель найденного должна была закрыться")
	}
}

// Сумма высот зон не превышает экрана: панель добавлена в четырёх местах,
// и пропуск одного не даёт ошибки сборки — экран просто уезжает за край.
func TestFindPanelFitsScreen(t *testing.T) {
	m := newTestModel(t)
	m.finds = make([]find.Excerpt, 12)
	m.toggleFindPanel()

	total := 1 + m.vp.Height() + 1 + m.findHeight() + m.ta.Height() + 1
	if total > m.height {
		t.Errorf("зоны не помещаются: %d строк при экране %d", total, m.height)
	}
	if m.inputTop() >= m.height {
		t.Errorf("поле ввода уехало за край экрана: строка %d из %d", m.inputTop(), m.height)
	}
}

// Enter в панели читает выбранный кусок, а не отправляет вопрос модели.
func TestFindPanelEnterReads(t *testing.T) {
	m := newTestModel(t)
	m.finds = []find.Excerpt{{ID: "books/1#1"}, {ID: "books/2#2"}}
	m.toggleFindPanel()
	m.findPane.move(1, len(m.finds))

	taken, id := m.handleFindPanelKey("enter")
	if !taken || id != "books/2#2" {
		t.Errorf("Enter должен отдавать id выбранного куска, вышло taken=%v id=%q", taken, id)
	}
}

// Клавиши меню команд важнее клавиш панели: меню открыто поверх строки ввода,
// и стрелки там водят по командам.
func TestCmdMenuWinsOverFindPanel(t *testing.T) {
	m := newTestModel(t)
	m.toggleFindPanel()
	m.setInput("/k")
	m.syncCmdMenu()
	if m.cmds == nil {
		t.Fatal("меню команд не открылось")
	}
	if m.findPane != nil {
		t.Error("панель найденного должна была уступить место меню команд")
	}
}

var _ tea.Msg = searchDoneMsg{}
