package ui

import (
	"fmt"
	"sort"
	"strings"
)

// Список команд — один источник правды.
//
// **Зачем таблицей, а не строкой справки.** До 28.08.2026 команды жили только
// внутри `helpText` как размеченный вручную текст. Меню подсказок по `/`
// потребовало тех же имён и описаний в разборном виде, и второй список рядом
// разошёлся бы с первым в первую же правку: добавил команду — забыл про справку.
// Поэтому таблица одна, а справка собирается из неё.
//
// **Псевдонимы в меню не показываются.** У половины команд есть короткие
// написания (`/q` для `/quit`, `/token` для `/confluencetoken`), и вывод их
// отдельными строками удвоил бы список без пользы: человек ищет команду
// по смыслу, а не перебирает написания. Псевдонимы участвуют в **отборе** —
// набранное `/q` находит `/quit`.

// cmdInfo — одна команда меню и справки.
type cmdInfo struct {
	Name  string    // без ведущей косой черты
	Alias []string  // другие написания; в списке не показываются
	Args  string    // подсказка о доводах, например "<имя>"
	Desc  string    // одна строка описания
	Sub   []cmdInfo // подкоманды: /kb add, /graph find

	// Need — раздел конфига, без которого команды нет в меню и в справке.
	// Пусто — команда есть всегда.
	//
	// Показывать команду ненастроенной возможности хуже, чем не показывать:
	// человек видит `/kb`, зовёт её и получает «коллекций нет», а на машине,
	// где библиотеки книг не будет никогда, это повторяется каждый день.
	Need string
}

// commands — все команды приложения.
var commands = []cmdInfo{
	{Name: "help", Alias: []string{"?"}, Desc: "справка по командам и клавишам"},
	{Name: "servers", Desc: "список серверов из конфига (Ctrl+S)"},
	{Name: "server", Args: "<имя>", Desc: "переключиться на сервер"},
	{Name: "models", Desc: "список моделей — запрашивается у сервера заново (Ctrl+R)"},
	{Name: "model", Args: "<имя>", Desc: "переключиться на модель"},
	{Name: "ps", Desc: "что загружено в память сервера"},
	{Name: "info", Desc: "подробности о текущей модели"},
	{Name: "context", Args: "[подкоманда]", Desc: "контекстное окно: сведения и размер", Sub: []cmdInfo{
		{Name: "set", Args: "<N>", Desc: "поставить окно в N токенов (/context set 256k)"},
		{Name: "add", Args: "<N>", Desc: "прибавить к окну N токенов (/context add 32k)"},
		{Name: "max", Desc: "поставить наибольшее окно, какое поддерживает модель"},
	}},
	{Name: "calc", Args: "[N]", Desc: "какое окно дать каждому из N пользователей (без N — одному)"},
	{Name: "mode", Args: "[режим]", Desc: "режим подтверждений (Shift+Tab — по кругу)", Sub: []cmdInfo{
		{Name: "safe", Desc: "спрашивать про запись и команды"},
		{Name: "auto-edit", Desc: "запись без спроса, команды с подтверждением"},
		{Name: "noask", Desc: "не спрашивать; правила deny всё равно действуют (прежние имена — yolo, no-ask)"},
	}},
	{Name: "permissions", Desc: "действующие правила разрешений"},
	{Name: "tools", Args: "[iterations <n|off>]", Desc: "доступные инструменты и предел их вызовов"},
	{Name: "think", Args: "[on|off]", Desc: "режим рассуждений модели"},
	{Name: "mouse", Args: "[on|off]", Desc: "мышь приложению или терминалу (F2)"},
	{Name: "paste", Alias: []string{"image"}, Desc: "вставить изображение из буфера обмена (Ctrl+V)"},
	{Name: "system", Args: "[текст]", Desc: "показать или изменить системный промпт"},
	{Name: "search", Alias: []string{"найти"}, Args: "[-f] [-r] [N] <текст>",
		Desc: "искать по графу и книгам, не спрашивая модель", Need: "kb"},
	{Name: "read", Alias: []string{"читать"}, Args: "<id|номер>",
		Desc: "показать найденный кусок целиком (Ctrl+F — список найденного)", Need: "kb"},
	{Name: "kb", Args: "[подкоманда]", Desc: "база знаний по книгам", Need: "kb", Sub: []cmdInfo{
		{Name: "help", Desc: "подробности по базе знаний"},
		{Name: "add", Args: "<имя> <путь>", Desc: "собрать коллекцию из книг каталога"},
		{Name: "use", Args: "<имя>", Desc: "выбрать коллекцию для поиска"},
		{Name: "list", Alias: []string{"ls"}, Desc: "какие коллекции есть, сколько в них книг и векторов(смыслов)"},
		{Name: "sync", Args: "<имя>", Desc: "долить новые книги и убрать пропавшие"},
		{Name: "embed", Alias: []string{"векторы"}, Args: "<имя>", Desc: "посчитать векторы(смыслы) — включить смысловой поиск"},
		{Name: "search", Alias: []string{"find"}, Args: "<запрос>", Desc: "искать по книгам, не спрашивая модель"},
		{Name: "tune", Alias: []string{"отбор"}, Args: "[что] [знач.]", Desc: "числа отбора на сеанс: top_k, min_cosine, semantic_weight"},
		{Name: "years", Alias: []string{"годы"}, Args: "<имя>", Desc: "проставить книгам год издания"},
		{Name: "merge", Alias: []string{"compact"}, Args: "<имя>", Desc: "уплотнить хранилище, освободив место удалённых"},
		{Name: "doctor", Args: "<имя>", Desc: "показать тихие потери в коллекции"},
		{Name: "auto", Args: "on|off", Desc: "подмешивать найденное перед каждым вопросом"},
		{Name: "new", Args: "<имя> [описание]", Desc: "создать пустую коллекцию"},
		{Name: "reindex", Alias: []string{"перечитать"}, Args: "<имя> <путь>", Desc: "перечитать книги заново"},
		{Name: "style", Alias: []string{"стиль"}, Desc: "как модель отвечает по книгам: действующая политика"},
		{Name: "stats", Args: "<имя>", Desc: "подробности: книги, куски, термы, размеры"},
		{Name: "stop", Desc: "остановить индексацию (то же, что Esc)"},
		{Name: "rm", Alias: []string{"remove", "drop"}, Args: "<имя>", Desc: "удалить коллекцию или одну книгу"},
		{Name: "off", Alias: []string{"выкл"}, Desc: "перестать подмешивать книги к вопросам"},
	}},
	{Name: "mix", Alias: []string{"подмес"}, Args: "show <вопрос>", Desc: "показать, что подмешалось бы к вопросу", Need: "mix"},
	{Name: "graph", Alias: []string{"граф"}, Args: "[подкоманда]", Desc: "граф понятий по книгам", Need: "graph", Sub: []cmdInfo{
		{Name: "help", Desc: "подробности по графу"},
		{Name: "status", Alias: []string{"стат"}, Desc: "понятия, связи, охват библиотеки"},
		{Name: "use", Alias: []string{"взять"}, Args: "<имя>|-", Desc: "с каким графом работать: рабочий или опытный рядом"},
		{Name: "find", Alias: []string{"найти"}, Args: "<вопрос>", Desc: "искать по графу: понятия, связи, цитаты"},
		{Name: "tune", Alias: []string{"отбор"}, Args: "[что] [знач.]", Desc: "отбор связей на сеанс: sense, pool"},
		{Name: "auto", Args: "on|off", Desc: "подмешивать карту понятий к каждому вопросу"},
		{Name: "communities", Alias: []string{"сообщества", "темы"}, Desc: "темы графа: размеры, названия, описания"},
		{Name: "check", Alias: []string{"проверить"}, Desc: "целостность графа и привязка к коллекции"},
		{Name: "pack", Alias: []string{"упаковать"}, Args: "<файл.tar>", Desc: "коллекция вместе с графом в переносимый архив"},
		{Name: "archive", Alias: []string{"архив"}, Args: "[коллекция]", Desc: "снять архив коллекции с графом в graph.archive_dir (в фоне)"},
		{Name: "archives", Alias: []string{"архивы"}, Args: "[коллекция]", Desc: "какие архивы есть в graph.archive_dir"},
		{Name: "review", Alias: []string{"разбор"}, Args: "[--judge] [коллекция]", Desc: "разобрать пары, в которых машина усомнилась: y одно и то же, n разные"},
		{Name: "rm", Alias: []string{"remove", "удалить"}, Args: "[коллекция] точно", Desc: "удалить граф, книги не трогая"},
		{Name: "build", Alias: []string{"собрать"}, Desc: "как собрать граф (сборка идёт отдельным запуском)"},
		{Name: "off", Alias: []string{"выкл"}, Desc: "перестать подмешивать карту понятий к вопросам"},
	}},
	{Name: "confluencetoken", Alias: []string{"token"}, Args: "[<токен>|off]",
		Desc: "токен Confluence на сеанс (в историю не попадает)", Need: "confluence"},
	{Name: "add", Args: "<путь>", Desc: "приложить файл к контексту (PDF и EPUB — текстом)"},
	{Name: "addimg", Alias: []string{"addimage"}, Args: "<путь> [стр.]",
		Desc: "приложить рисунки из PDF или EPUB (/addimg книга.pdf 40-60)"},
	{Name: "savetopdf", Alias: []string{"savepdf"}, Args: "<файл.pdf>",
		Desc: "сохранить видимый ответ в PDF (F4 — с запросом имени)"},
	{Name: "log", Args: "[on|off]", Desc: "путь к журналу, включение и выключение"},
	{Name: "id", Desc: "идентификатор обмена — по нему обмен ищется в журнале"},
	{Name: "save", Desc: "сохранить текущую сессию"},
	{Name: "resume", Args: "[id]", Desc: "восстановить сессию (без id — выбор из списка)"},
	{Name: "clear", Desc: "очистить историю диалога"},
	{Name: "compact", Args: "[N]", Desc: "оставить последние N сообщений (по умолчанию 6)"},
	{Name: "config", Desc: "путь к файлу настроек"},
	{Name: "quit", Alias: []string{"exit", "q"}, Desc: "выход"},
}

// cmdEntry — строка меню: команда целиком, уже с косой чертой.
type cmdEntry struct {
	Full  string   // "/kb add"
	Args  string   // "<имя> <путь>"
	Desc  string   // описание
	names []string // все написания для отбора: "/kb add", "/kb доб"…
}

// insert — что подставляется в строку ввода при выборе.
//
// Пробел в конце ставится всегда: у команды с доводами он нужен, чтобы сразу
// писать довод, а у команды без доводов — чтобы `Enter` отправил её как есть,
// не дописывая ничего лишнего.
func (e cmdEntry) insert() string { return e.Full + " " }

// matchCommands отбирает команды, подходящие под набранное.
//
// Отбор идёт по началу строки, а не по вхождению: человек набирает команду
// с начала, и подстрочный поиск выдал бы `/savetopdf` на запрос `/top`,
// что читается как ошибка отбора. Псевдонимы участвуют наравне с именами,
// но в выдаче показывается основное написание.
func matchCommands(input string, has sectionCheck) []cmdEntry {
	prefix := strings.ToLower(strings.TrimSpace(input))
	if !strings.HasPrefix(prefix, "/") {
		return nil
	}
	all := flatCommands(has)
	if prefix == "/" {
		return all
	}
	out := make([]cmdEntry, 0, len(all))
	for _, e := range all {
		for _, n := range e.names {
			if strings.HasPrefix(n, prefix) {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// flatCommands разворачивает таблицу в плоский список: сперва все команды
// верхнего уровня, затем подкоманды каждой.
//
// Подкоманды идут следом за своей командой, а не в конце общего списка:
// набрав `/kb`, человек видит `/kb` и сразу под ней её подкоманды.
func flatCommands(has sectionCheck) []cmdEntry {
	out := make([]cmdEntry, 0, len(commands)*2)
	for _, c := range commands {
		if !has.ok(c.Need) {
			continue
		}
		out = append(out, entryOf("", c))
		for _, s := range c.Sub {
			out = append(out, entryOf(c.Name, s))
		}
	}
	return out
}

// sectionCheck отвечает на вопрос «есть ли такой раздел в конфиге».
//
// Отдельный тип, а не голая функция, ради nil: список команд собирается
// и там, где конфига нет вовсе (справка в тестах), и тогда показывается всё.
type sectionCheck func(string) bool

func (h sectionCheck) ok(need string) bool {
	return need == "" || h == nil || h(need)
}

// entryOf собирает строку меню из описания команды.
func entryOf(parent string, c cmdInfo) cmdEntry {
	full := "/" + c.Name
	names := []string{full}
	for _, a := range c.Alias {
		names = append(names, "/"+a)
	}
	if parent != "" {
		full = "/" + parent + " " + c.Name
		names = []string{full}
		for _, a := range c.Alias {
			names = append(names, "/"+parent+" "+a)
		}
	}
	return cmdEntry{Full: full, Args: c.Args, Desc: c.Desc, names: names}
}

// commandsHelp собирает раздел «Команды» справки из той же таблицы.
//
// Подкоманды в справку не идут: у `/kb` и `/graph` своя подробная справка,
// а общий список из восьмидесяти строк перестал бы читаться.
func commandsHelp(has sectionCheck) string {
	type row struct{ left, desc string }
	rows := make([]row, 0, len(commands))
	width := 0
	for _, c := range commands {
		if !has.ok(c.Need) {
			continue
		}
		left := "/" + c.Name
		if c.Args != "" {
			left += " " + c.Args
		}
		if w := len([]rune(left)); w > width {
			width = w
		}
		rows = append(rows, row{left, c.Desc})
	}

	var b strings.Builder
	b.WriteString("Команды:\n")
	for _, r := range rows {
		b.WriteString(fmt.Sprintf("  %-*s  %s\n", width, r.left, r.desc))
	}
	return b.String()
}

// knownCommandNames — все написания, включая псевдонимы и подкоманды.
// Нужен проверке, которая стережёт расхождение таблицы с разбором команд,
// поэтому берёт список целиком, без отбора по конфигу.
func knownCommandNames() []string {
	seen := map[string]bool{}
	for _, e := range flatCommands(nil) {
		for _, n := range e.names {
			seen[n] = true
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
