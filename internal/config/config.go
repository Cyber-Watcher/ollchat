// Package config загружает и валидирует файл настроек ollchat (TOML).
package config

import (
	"bytes"
	"fmt"
	"github.com/Cyber-Watcher/ollchat/internal/fsx"
	"github.com/Cyber-Watcher/ollchat/internal/graphex"
	"github.com/Cyber-Watcher/ollchat/internal/kbembed"
	"github.com/Cyber-Watcher/ollchat/internal/kbrerank"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	glamourstyles "charm.land/glamour/v2/styles"
	"github.com/BurntSushi/toml"
	"github.com/alecthomas/chroma/v2"
	chromastyles "github.com/alecthomas/chroma/v2/styles"

	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// Режимы подтверждения действий агента.
const (
	ModeSafe     = "safe"      // запись и bash — всегда спрашивать
	ModeAutoEdit = "auto-edit" // запись без спроса, bash — спрашивать
	ModeNoAsk    = "noask"     // ничего не спрашивать (deny-правила всё равно действуют)

	// ModeYolo — прежнее имя ModeNoAsk, оставлено ради чужих конфигов.
	// «yolo» ничего не говорило человеку о том, что подтверждения выключены.
	ModeYolo = "yolo"
)

// Config — корень файла настроек.
type Config struct {
	General     General     `toml:"general"`
	Input       Input       `toml:"input"`
	Theme       Theme       `toml:"theme"`
	Log         Log         `toml:"log"`
	Agent       Agent       `toml:"agent"`
	Permissions Permissions `toml:"permissions"`
	Sandbox     Sandbox     `toml:"sandbox"`
	KB          KB          `toml:"kb"`
	Graph       Graph       `toml:"graph"`

	// GraphNamed — настройки именованных графов: [graph.lab] поверх [graph].
	// Заполняется при чтении файла, в самом файле отдельного ключа не имеет.
	GraphNamed map[string]Graph `toml:"-"`

	// graphBase — общий раздел [graph], каким он прочитан из файла.
	//
	// Хранится отдельно потому, что UseGraph подменяет c.Graph настройками
	// выбранного графа. Без сохранённой основы возврат к рабочему графу
	// вернул бы настройки опытного — поймано тестом 03.09.2026.
	graphBase    Graph
	graphBaseSet bool
	Mix          Mix        `toml:"mix"`
	Web          Web        `toml:"web"`
	Confluence   Confluence `toml:"confluence"`
	Viewers      Viewers    `toml:"viewers"`
	Servers      []Server   `toml:"servers"`

	// Path — фактический путь, откуда загружен конфиг (не из файла).
	Path string `toml:"-"`

	// sections — разделы, написанные в самом файле. Пусто означает «файла
	// не было»; см. Has.
	sections []string
}

// Viewers — чем открывать книгу по Ctrl+O в панели найденного.
//
// Пусто — системный просмотрщик (`xdg-open`), тот же, что открывает файл
// из проводника. Настройка нужна там, где системный выбор неудобен: открыть
// PDF сразу на нужной странице умеет `zathura -P {page} {file}`, а системный
// просмотрщик страницу не знает.
//
// Строка задаётся как командная. Подстановки: `{file}` — путь к книге
// (без неё путь дописывается в конец), `{page}` — страница из выдачи.
// Довод со страницей выбрасывается целиком, когда страница неизвестна:
// у книги EPUB страниц нет, там единица ссылки — раздел.
type Viewers struct {
	PDF  string `toml:"pdf"`
	EPUB string `toml:"epub"`
	MD   string `toml:"md"`
}

// General — общие настройки приложения.
type General struct {
	DefaultServer string `toml:"default_server"`
	// VRAMProfile — путь к профилю замеров olldiagtools. Пустой путь означает
	// стандартное расположение рядом с конфигом.
	VRAMProfile    string `toml:"vram_profile"`
	RenderMarkdown bool   `toml:"render_markdown"`
	ShowThinking   bool   `toml:"show_thinking"`
	// ShowTurnID — показывать ли идентификатор обмена в строке состояния.
	// В ленте под ответом и по команде /id он виден всегда.
	ShowTurnID bool `toml:"show_turn_id"`

	// StartupHints — показывать ли советы при запуске: о выключенных
	// инструментах, об устаревших настройках конфига и подобные. "on"
	// (умолчание) или "off".
	//
	// Выключается там, где конфиг сокращён намеренно. На рабочей машине
	// пользователей рабочей машины из конфигов убраны база книг и граф, и совет
	// «допишите kb_search в agent.tools» там не помощь, а помеха: человек
	// каждый запуск читает предложение включить то, чего на машине нет.
	// Ошибки и предупреждения по ходу работы настройка не трогает.
	StartupHints string `toml:"startup_hints"`

	// TokenSpeed — что показывать в строке состояния о скорости ответа:
	// off, live (текущая во время ответа), final (итог по ответу сервера),
	// full (и то и другое плюс время до первого токена).
	TokenSpeed string `toml:"token_speed"`

	// Today — подставлять ли сегодняшнюю дату в системное сообщение.
	// Модель её не знает: в весах остался предел обучения, и на вопрос «какой
	// сейчас год» она называет год выпуска (замерено на deepseek-r1 — 2023-й).
	// Стоит около двух десятков токенов на запрос.
	Today bool `toml:"today"`

	Mode string `toml:"mode"`
}

// Input — поле ввода: курсор и работа с мышью.
type Input struct {
	// Mouse — запрашивать ли у терминала события мыши. При true работают колесо
	// и бегунок прокрутки, но выделить текст мышью нельзя: события уходят
	// приложению. При false лента выделяется и копируется средствами самого
	// терминала. Переключается на лету, в конфиге задано лишь начальное значение.
	Mouse bool `toml:"mouse"`

	// CommandRows — сколько команд видно в подсказке, которая открывается,
	// когда строка ввода начинается с косой черты. Панель занимает на три
	// строки больше: рамка и заголовок. 0 — четыре, как задумано;
	// на большом экране можно поставить больше.
	CommandRows int `toml:"command_rows"`

	// FindRows — сколько строк видно в списке найденного (Ctrl+F). Панель
	// занимает на три строки больше: рамка и заголовок. 0 — пять.
	FindRows int `toml:"find_rows"`

	Cursor Cursor `toml:"cursor"`
}

// Cursor — вид курсора в поле ввода. Курсор рисует сам терминал, поэтому
// символ под ним остаётся виден, а строка не сдвигается.
type Cursor struct {
	Shape string `toml:"shape"` // block, underline или bar
	Blink bool   `toml:"blink"`
	// Color — пусто означает цвет курсора по умолчанию, заданный терминалом.
	// Иначе номер цвета ANSI 256 ("212") либо запись вида "#ff87d7".
	Color string `toml:"color"`
}

// Допустимые формы курсора.
const (
	CursorBlock     = "block"
	CursorUnderline = "underline"
	CursorBar       = "bar"
)

// ThemeAuto — определять светлую или тёмную базу по цвету фона терминала.
const ThemeAuto = "auto"

// Умолчания оформления. Красный код на серой заливке из встроенного стиля
// glamour читается как сообщение об ошибке (ANSI 203 — тот же цвет, что у
// ошибок в ленте), а любая заливка вырезает прямоугольник в фоне терминала,
// если у него обои или прозрачность. Поэтому свои умолчания: песочный код
// без заливки и тема gruvbox в блоках кода.
const (
	DefaultCodeTheme  = "gruvbox"
	DefaultInlineCode = "179"
)

// DefaultTokens — правки цветов подсветки поверх темы по умолчанию.
//
// gruvbox красит токен NameTag своим ярко-красным #fb4934 — в этой теме цвет
// предназначался тегам HTML. Но тем же токеном лексер размечает ключи YAML
// и JSON, и файл GitLab CI в ответе модели выглядит так, будто в нём всё
// сломано. Синий #83a598 — из той же палитры gruvbox.
func DefaultTokens() map[string]string { return map[string]string{"NameTag": "#83a598"} }

// Theme — оформление markdown в ленте ответов.
type Theme struct {
	// Style — база оформления: auto (по фону терминала), dark, light,
	// dracula, tokyo-night, notty, ascii, pink.
	Style string `toml:"style"`
	// CodeTheme — тема подсветки синтаксиса в блоках кода, любая из тем
	// chroma. Пусто — цвета базового стиля.
	CodeTheme string `toml:"code_theme"`
	// CodeBG — заливка блока кода. Пусто — фон терминала.
	CodeBG string `toml:"code_bg"`
	// Italic — рисовать ли заметки, рассуждения и выделение курсивом:
	// auto (по терминалу), on, off. Терминал без курсива показывает вместо
	// него инверсию — серую заливку строки; так выглядит справка в tmux
	// с умолчанием TERM=screen-256color.
	Italic string `toml:"italic"`
	// InlineCode и InlineCodeBG — цвет и заливка кода в тексте (`так`).
	InlineCode   string `toml:"inline_code"`
	InlineCodeBG string `toml:"inline_code_bg"`
	// Tokens — цвета отдельных видов токенов поверх темы подсветки,
	// например {"NameTag": "#83a598"}. Имена — виды токенов chroma.
	Tokens map[string]string `toml:"tokens"`
}

// Значения настройки theme.italic.
const (
	ItalicAuto = "auto"
	ItalicOn   = "on"
	ItalicOff  = "off"
)

// validate проверяет имена стилей и цвета.
//
// Неизвестное имя темы chroma особенно коварно: chroma на него не ругается,
// а молча берёт запасную тему swapoff. Настройка выглядела бы рабочей, а вид
// менялся бы неизвестно на что — поэтому имя проверяется здесь, до запуска.
func (t *Theme) validate() error {
	if t.Style == "" {
		t.Style = ThemeAuto
	}
	if t.Style != ThemeAuto {
		if _, ok := glamourstyles.DefaultStyles[t.Style]; !ok {
			return fmt.Errorf("theme.style: неизвестный стиль %q (допустимы %s и %s)",
				t.Style, ThemeAuto, strings.Join(themeStyleNames(), ", "))
		}
	}
	// Курсив: auto — смотреть на терминал. Терминал без поддержки курсива
	// (TERM=screen-256color, умолчание tmux) рисует его инверсией, и лента
	// выглядит залитой серым.
	if t.Italic == "" {
		t.Italic = ItalicAuto
	}
	switch t.Italic {
	case ItalicAuto, ItalicOn, ItalicOff:
	default:
		return fmt.Errorf("theme.italic: %q — допустимы %s, %s и %s",
			t.Italic, ItalicAuto, ItalicOn, ItalicOff)
	}
	if t.CodeTheme != "" {
		if _, ok := chromastyles.Registry[t.CodeTheme]; !ok {
			return fmt.Errorf("theme.code_theme: неизвестная тема подсветки %q (см. %s)",
				t.CodeTheme, strings.Join(chromastyles.Names(), ", "))
		}
	}
	// Раздел не задан вовсе — берём умолчание. Заданный пустым означает
	// «цвета темы как есть», поэтому пустая карта умолчанием не заменяется:
	// иначе от правки нельзя было бы отказаться. Различить эти два случая
	// позволяет то, что Load снимает умолчание перед разбором конфига.
	if t.Tokens == nil && t.CodeTheme != "" {
		t.Tokens = DefaultTokens()
	}
	if len(t.Tokens) > 0 && t.CodeTheme == "" {
		return fmt.Errorf("theme.tokens: правки цветов действуют только вместе с theme.code_theme; " +
			"без темы подсветку рисует glamour своим набором цветов")
	}
	for name, colour := range t.Tokens {
		if _, err := chroma.TokenTypeString(name); err != nil {
			return fmt.Errorf("theme.tokens: неизвестный вид токена %q "+
				"(ходовые: NameTag — ключи YAML и теги XML, Keyword, LiteralString, Comment, "+
				"NameFunction, Operator, Punctuation, LiteralNumber)", name)
		}
		if err := checkHexColor(colour); err != nil {
			return fmt.Errorf("theme.tokens.%s: %w", name, err)
		}
	}
	for _, c := range []struct {
		name, value string
	}{
		{"theme.code_bg", t.CodeBG},
		{"theme.inline_code", t.InlineCode},
		{"theme.inline_code_bg", t.InlineCodeBG},
	} {
		if err := checkColor(c.value); err != nil {
			return fmt.Errorf("%s: %w", c.name, err)
		}
	}
	return nil
}

// checkHexColor принимает только запись вида "#83a598" или "#8ae".
//
// Обычной checkColor тут мало: она пропускает номера ANSI, а chroma читает
// значение как шестнадцатеричное число — номер 179 молча превратился бы
// в #000179, тёмно-синий вместо песочного. Молчаливая подмена цвета хуже
// отказа при запуске.
func checkHexColor(s string) error {
	if !strings.HasPrefix(s, "#") {
		return fmt.Errorf("значение %q должно быть записью вида \"#83a598\": "+
			"номера цветов ANSI здесь не работают, chroma читает их как шестнадцатеричное число", s)
	}
	return checkColor(s)
}

// themeStyleNames возвращает имена встроенных стилей glamour по порядку.
func themeStyleNames() []string {
	out := make([]string, 0, len(glamourstyles.DefaultStyles))
	for name := range glamourstyles.DefaultStyles {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// DefaultFilePattern — имя файла журнала по умолчанию: свой файл на каждый
// запуск ollchat. Иначе несколько экземпляров, запущенных на одной машине
// в разных сеансах ssh или окнах tmux, писали бы в общий файл вперемешку.
const DefaultFilePattern = "chat-%Y-%m-%d_%H-%M-%S.md"

// Log — настройки журнала чата.
type Log struct {
	Enabled bool   `toml:"enabled"`
	Dir     string `toml:"dir"`
	// FilePattern — шаблон имени файла в духе strftime, см. chatlog.ParsePattern.
	FilePattern string `toml:"file_pattern"`
	// Pattern — устаревшая настройка: раскладка времени Go ("chat-2006-01-02.md").
	// Оставлена, чтобы не ломать существующие конфиги; действует, только когда
	// file_pattern не задан.
	Pattern     string `toml:"pattern"`
	LogThinking bool   `toml:"log_thinking"`
	LogTools    bool   `toml:"log_tools"`
	// StepsFilePattern — шаблон имени журнала шагов (строка JSON на каждый
	// обмен с моделью, вызов инструмента и поиск). Пусто — умолчание
	// steps-%Y-%m-%d_%H-%M-%S.jsonl рядом с журналом чата; "off" — не писать.
	StepsFilePattern string `toml:"steps_file_pattern"`
	// StepsFile — готовый путь журнала шагов для этого запуска (ключ
	// --steps-file, для замеров). Не настройка: в TOML не читается. Сильнее
	// шаблона и может быть абсолютным — шаблон обязан быть относительным,
	// потому что ложится под каталог журналов, а этот путь называют целиком.
	StepsFile string `toml:"-"`
}

// DefaultStepsPattern — имя журнала шагов, когда настройка не задана.
const DefaultStepsPattern = "steps-%Y-%m-%d_%H-%M-%S.jsonl"

// StepsPattern возвращает разобранный шаблон имени журнала шагов;
// nil без ошибки — журнал шагов выключен словом "off".
func (l Log) StepsPattern() (*chatlog.Pattern, error) {
	if f := strings.TrimSpace(l.StepsFile); f != "" {
		return chatlog.FixedName(f), nil
	}
	s := strings.TrimSpace(l.StepsFilePattern)
	if strings.EqualFold(s, "off") {
		return nil, nil
	}
	if s == "" {
		s = DefaultStepsPattern
	}
	return chatlog.ParsePattern(s)
}

// NamePattern возвращает разобранный шаблон имени файла журнала.
// Новая настройка file_pattern имеет приоритет над устаревшей pattern.
func (l Log) NamePattern() (*chatlog.Pattern, error) {
	if strings.TrimSpace(l.FilePattern) != "" {
		return chatlog.ParsePattern(l.FilePattern)
	}
	return chatlog.LegacyPattern(l.Pattern), nil
}

// LegacyPatternIgnored сообщает, что в конфиге заданы обе настройки сразу
// и устаревшая не действует — об этом стоит предупредить пользователя.
func (l Log) LegacyPatternIgnored() bool {
	return strings.TrimSpace(l.FilePattern) != "" && strings.TrimSpace(l.Pattern) != ""
}

// Agent — настройки агентного режима.
type Agent struct {
	Enabled bool `toml:"enabled"`
	// MaxIterations — сколько раз подряд модель может попросить инструмент
	// в одном обмене. Предохранитель от зацикливания: модель, застрявшая
	// в цикле «прочитать — не понять — прочитать снова», иначе крутится, пока
	// не кончится контекст.
	//
	// **-1 — без ограничения.** Годится для обхода документации и разбора
	// каталогов, где вызовов заведомо много; прервать ход всегда можно Esc.
	// 0 — умолчание (25).
	MaxIterations int      `toml:"max_iterations"`
	MaxRetries    int      `toml:"max_retries"`
	Tools         []string `toml:"tools"`
	BashTimeout   string   `toml:"bash_timeout"`
	MaxOutputKB   int      `toml:"max_output_kb"`

	// CompactAt — доля окна, при которой история чата сжимается сводкой
	// перед следующим вопросом: 0.75 — при трёх четвертях. Проверяется по
	// точному числу из последнего ответа сервера. 0 — выключено.
	// В агентном режиме сжатия нет: сводка теряет точность истории вызовов,
	// и при превышении порога ход не начинается, а подсказывается /compact.
	CompactAt float64 `toml:"compact_at"`
	// CompactKeep — сколько последних сообщений оставить дословно; остальное
	// уходит в сводку. То же умолчание, что у ручной /compact.
	CompactKeep int `toml:"compact_keep"`
	// CompactModel — какой моделью писать сводку; пусто — той же, что отвечает.
	// Лёгкая модель быстрее, но на одной карте может вытеснить основную.
	CompactModel string `toml:"compact_model"`

	bashTimeout time.Duration
}

// BashTimeoutDuration возвращает разобранный таймаут выполнения команд.
func (a Agent) BashTimeoutDuration() time.Duration { return a.bashTimeout }

// Permissions — правила разрешений в стиле Claude Code.
type Permissions struct {
	Allow []string `toml:"allow"`
	Ask   []string `toml:"ask"`
	Deny  []string `toml:"deny"`
}

// Sandbox — границы файловой песочницы.
type Sandbox struct {
	Root           string `toml:"root"`
	AllowOutside   bool   `toml:"allow_outside"`
	FollowSymlinks bool   `toml:"follow_symlinks"`
	MaxFileKB      int    `toml:"max_file_kb"`
	MaxPDFMB       int    `toml:"max_pdf_mb"`
}

// KB — база знаний по книгам.
type KB struct {
	// TableBoost — надбавка кускам-таблицам при словесном поиске. 0 — умолчание
	// (1.5, выбрано перебором 03.09.2026: recall 0.325 → 0.358 на наборе
	// терминов, и без потерь на переформулированных вопросах). 1.0 выключает.
	TableBoost float64 `toml:"table_boost"`

	Dir         string   `toml:"dir"`           // где держать индексы
	Roots       []string `toml:"roots"`         // откуда разрешено брать книги
	Default     string   `toml:"default"`       // коллекция по умолчанию
	Auto        bool     `toml:"auto"`          // подмешивать найденное перед каждым вопросом
	SyncOnStart bool     `toml:"sync_on_start"` // сверять папки коллекций при запуске
	Workers     int      `toml:"workers"`
	MaxBookMB   int      `toml:"max_book_mb"`
	TopK        int      `toml:"top_k"`
	MaxPerBook  int      `toml:"max_per_book"`

	// Смысловой поиск. Пустой embed_model выключает его целиком: коллекция
	// ищется по словам, как и до его появления.
	Semantic       bool    `toml:"semantic"`
	EmbedURL       string  `toml:"embed_url"`   // пусто — сервер, выбранный для чата
	EmbedModel     string  `toml:"embed_model"` // пусто — смысловой поиск выключен
	EmbedBatch     int     `toml:"embed_batch"`
	EmbedWorkers   int     `toml:"embed_workers"`   // пачек одновременно
	MinCosine      float64 `toml:"min_cosine"`      // порог близости по смыслу
	SemanticWeight float64 `toml:"semantic_weight"` // вес смыслового списка против словесного
	// AbstainGap — порог воздержания kb_search: если разрыв первого и второго
	// места выдачи ((score₁ − score₂) / score₁) ниже него, инструмент помечает
	// выдачу как неуверенную и велит модели сказать «в книгах нет», а не
	// ссылаться на случайную страницу. 0 — не помечать. Значение снимается
	// замером `--kb-eval` (таблица «воздержание»), не назначается.
	AbstainGap float64 `toml:"abstain_gap"`
	// AbstainScore — порог воздержания по абсолютной оценке реранкера: лучший
	// кусок оценён ниже — выдача помечается как «в книгах, скорее всего, нет».
	// Действует только при включённом реранкере (у него шкала сопоставима между
	// запросами). Не задан — выключено. Замер 04.09.2026 на 457 вопросах: −2
	// молчит честно на 11 вопросах и зря на одном (0.6% найденного).
	AbstainScore   *float64 `toml:"abstain_score"`
	AnswerStyle    string   `toml:"answer_style"` // как модель должна отвечать по книгам
	EmbedKeepAlive string   `toml:"embed_keep_alive"`

	// Общая библиотека организации.
	//
	// Пусто (умолчание) — коллекции лежат файлами рядом с человеком, и это
	// главный сценарий: один бинарь, ни служб, ни портов. Задан адрес —
	// поиск по книгам идёт к общей библиотеке, которую администратор собрал
	// один раз: индекс строится часами на видеокарте, и повторять это
	// на каждом рабочем месте незачем.
	//
	// Пишет по-прежнему только администратор, локально. Клиент читает.
	ServerURL string `toml:"server_url"`

	// Ключ доступа к общей библиотеке. **В самом файле настроек его нет
	// намеренно**: файл копируют и пересылают. Порядок тот же, что у токена
	// Confluence: файл с правами 600, переменная окружения, команда.
	ServerTokenFile string `toml:"server_token_file"`
	ServerTokenEnv  string `toml:"server_token_env"`
	ServerTokenCmd  string `toml:"server_token_cmd"`

	// ServerTimeout — сколько ждать ответа общей библиотеки. Пусто — 30 с:
	// поиск по сети идёт дольше местного, но не минутами.
	ServerTimeout string `toml:"server_timeout"`

	// RerankURL — адрес службы переранжирования (llama-server с --reranking).
	// Пусто — второй ступени нет, поиск работает одной.
	//
	// Отдельная служба потому, что Ollama кросс-энкодеры не обслуживает:
	// ручки переранжирования у неё нет, а эмбеддинги она отдаёт только при
	// заданном типе пулинга, которого у реранкеров особый.
	RerankURL string `toml:"rerank_url"`

	// RerankModel — имя модели для отчётов; служба обслуживает одну и без него.
	RerankModel string `toml:"rerank_model"`

	// RerankTimeout — предел ожидания ответа. Пусто — минута: сорок пар
	// по замеру идут 1.7 с, минуты хватает с большим запасом.
	RerankTimeout string `toml:"rerank_timeout"`

	// RerankCandidates — сколько кусков отдавать второй ступени. 0 — двадцать.
	//
	// Двадцать, а не больше, выбрано замером 27.08.2026 на 457 вопросах:
	// с ростом до восьмидесяти полнота растёт (0.396 → 0.440), но среднее
	// место ухудшается (1.7 → 1.9), а цена вчетверо выше (0.9 с против 3.4).
	// Для ответа по книгам важнее место: модель читает первые куски, и разница
	// между «первый» и «второй» решает, ответит она по существу или по соседнему.
	RerankCandidates int `toml:"rerank_candidates"`

	// RerankSnippet — подавать выдержку вокруг совпадения вместо куска целиком.
	//
	// По умолчанию выключено: замер 27.08.2026 показал, что выдержки хуже
	// на всех трёх глубинах отбора (nDCG 0.289 против 0.344 при двадцати
	// кандидатах, 0.295 против 0.359 при сорока, 0.301 против 0.372 при
	// восьмидесяти). Предположение, что половина работы уходит на текст мимо
	// вопроса и выдержки сработают не хуже, замером опровергнуто:
	// кросс-энкодеру нужен весь кусок.
	RerankSnippet bool `toml:"rerank_snippet"`

	// EmbedTimeout — сколько ждать ответа сервера эмбеддингов.
	//
	// Отдельно от общего timeout по той же причине, что и chat_timeout: первый
	// запрос заставляет сервер выгрузить чужую модель и загрузить эту, и это
	// минуты, а не секунды. Замер 24.08.2026: `--kb-embed` при загруженной
	// glm-4.7-flash (32 ГБ) отдал четыре попытки по 300 с и сдался, а ручной
	// запрос сразу после этого прошёл за 1 мин 42 с.
	EmbedTimeout string `toml:"embed_timeout"`

	// QueryTimeout — сколько ждать вектор ОДНОГО вопроса при поиске.
	//
	// Отдельно от embed_timeout (пятнадцать минут): тот про счёт векторов всей
	// библиотеки, где минуты нормальны. Здесь человек ждёт ответа, и недоступный
	// эмбеддер должен уступить место словесному поиску за секунды. Замер
	// 29.08.2026: пока карту занимает сборка графа, сервер не отдаёт эмбеддинги
	// вовсе — и поиск вставал на четверть часа.
	//
	// Пусто — пятнадцать секунд. "0" — не ждать вовсе, сразу искать по словам.
	QueryTimeout string `toml:"query_timeout"`

	// ReadAround — сколько соседних кусков показывает /read с каждой стороны.
	// 0 — один: оборванная мысль чаще всего дочитывается соседом.
	ReadAround int `toml:"read_around"`

	// EmbedCheck — как часто проверять, отвечает ли модель эмбеддингов.
	//
	// Смысловой поиск отваливается молча: модель могли выгрузить, сервер
	// перезапустить, место кончиться. Поиск при этом не падает — он теряет
	// половину, и человек видит только ухудшившиеся ответы. Поэтому состояние
	// проверяется не раз при запуске, а по кругу, и видно в строке состояния.
	// Пусто — минута; "0" — проверять только при запуске.
	EmbedCheck string `toml:"embed_check"`
}

// Mix — подмешивание знаний к вопросу: обычный RAG и граф понятий.
//
// Вынесено в отдельный раздел затем, что это единственные настройки, которые
// тратят контекст на **каждый** вопрос, включая «спасибо». Всё остальное
// в [kb] и [graph] описывает, как устроены поиск и сборка, а здесь сказано,
// что уходит модели без её просьбы.
//
// Привратник: вопрос сперва связывается с понятиями графа — местная работа
// на миллисекунды. Не связался ни с одним понятием — не подмешивается ничего,
// ни граф, ни выдержки из книг.
type Mix struct {
	// Books — класть выдержки из книг к каждому вопросу (обычный RAG).
	// По умолчанию выключено: восемь фрагментов это около двух тысяч токенов
	// на вопрос, а модель с инструментами возьмёт их сама, когда они нужны.
	// Прежнее имя этой настройки — kb.auto, оно продолжает работать.
	Books bool `toml:"books"`

	// Graph — класть карту понятий из графа (GraphRAG). Включено: карта
	// короткая (две-три сотни токенов) и появляется только тогда, когда
	// вопрос связался с графом.
	Graph bool `toml:"graph"`

	// Entities и Neighbors — размер карты: сколько понятий и сколько связей
	// у каждого.
	Entities  int `toml:"entities"`
	Neighbors int `toml:"neighbors"`

	// QuotesWithoutTools — сколько выдержек из книг добавлять к карте, когда
	// у модели нет инструментов. Такая модель не может ничего дозапросить,
	// и без выдержек ей не на что сослаться. 0 — не добавлять.
	QuotesWithoutTools int `toml:"quotes_without_tools"`
}

// Graph — граф понятий поверх базы знаний (см. GraphRAGPlan.md).
//
// Пустая model выключает сборку графа: искать по уже собранному это не мешает,
// а собирать нечем — и об этом честно говорится словами.
type Graph struct {
	URL         string  `toml:"url"`   // пусто — сервер, выбранный для чата
	Model       string  `toml:"model"` // чем извлекать сущности и связи
	Workers     int     `toml:"workers"`
	NumCtx      int     `toml:"num_ctx"`
	Temperature float64 `toml:"temperature"`

	// MaxTokens — потолок длины ответа модели (num_predict). Замерено
	// 23.08.2026: без него на отдельных кусках модель писала 3 648 токенов
	// вместо трёхсот, то есть тридцать пять секунд вместо трёх.
	MaxTokens int    `toml:"max_tokens"`
	KeepAlive string `toml:"keep_alive"`

	// Retry — повторять ли один раз кусок, на котором модель не выдала JSON.
	Retry bool `toml:"retry"`

	// Ниже — разметка тем и их описание. Числа вынесены сюда не для красоты:
	// подобранные на одном графе, они перестают работать на графе вдвое
	// большем, и подбирать их приходится замером. Пересобирать ради этого
	// бинарь незачем.

	// Resolution — разрешение Louvain (γ): чем больше, тем мельче темы.
	// 0 — значение по умолчанию, подобранное замером.
	Resolution float64 `toml:"resolution"`

	// MaxCommunity — тема крупнее дробится заново. 0 — двести.
	MaxCommunity int `toml:"max_community"`

	// SplitDepth — сколько раз подряд дробить. 0 — шесть.
	SplitDepth int `toml:"split_depth"`

	// SummaryModel — чем описывать темы. Пусто — той же моделью, что и извлечение.
	//
	// Отдельная настройка нужна потому, что Ollama отказывает некоторым
	// архитектурам в параллельных запросах, и на такой модели описание тем идёт
	// втрое дольше при любом числе воркеров. Резюме темы — обычный абзац
	// по-русски, и его можно доверить другой модели, не трогая извлечение.
	SummaryModel string `toml:"summary_model"`

	// MinRating — не показывать в обзоре темы с оценкой ниже. 0 — пять.
	// Столько же в MS GraphRAG: глобальный поиск отбирает сообщества
	// по оценке (Essential GraphRAG, 2025, стр. 127).
	MinRating int `toml:"min_rating"`

	// SummaryWorkers — сколько тем описывать одновременно. 0 — четыре.
	SummaryWorkers int `toml:"summary_workers"`

	// SummaryMinMembers — темы мельче не описываются: тема из двух понятий
	// темой не является, а запрос стоит столько же. 0 — пять.
	SummaryMinMembers int `toml:"summary_min_members"`

	// SummaryMaxMembers — сколько понятий показывать модели. 0 — сорок.
	SummaryMaxMembers int `toml:"summary_max_members"`

	// SummaryMaxRelations — сколько связей показывать. 0 — тридцать.
	SummaryMaxRelations int `toml:"summary_max_relations"`

	// NeighborSenseWeight — сколько стоит близость связи к вопросу против её
	// подтверждённости книгами. **0 (умолчание) — не пересортировывать вовсе.**
	//
	// Замер 28.08.2026 на графе books: 100 вопросов, слепое судейство
	// qwen3.5:122b в оба порядка, из 24 согласных вердиктов 18 в пользу выдачи
	// БЕЗ пересортировки, 5 за, p = 0.011. Пересортировка выбивала конкретное
	// и подтверждённое ради близкого к теме вообще. Настройка оставлена
	// потому, что ответ зависит от набора книг: на другом графе он может
	// оказаться иным, и проверять это пересборкой бинаря неправильно —
	// у пользователей бинарь готовый. Осмысленное значение для проб — 1.5.
	NeighborSenseWeight float64 `toml:"neighbor_sense_weight"`

	// Cache — держать открытый граф в памяти между вопросами.
	//
	// Включено. Открытие стоит 11.5 с и до 1.03 ГБ пика (замер 29.08.2026),
	// а вопросов за сеанс десятки — платить за каждый незачем. Выключают там,
	// где памяти мало: тогда граф читается на каждый вопрос заново и сразу
	// освобождается. В службе ollmcp выключение означает то же самое.
	// Name — с каким графом работать. Пусто — рабочий, в каталоге `graph`
	// коллекции. Иное имя выбирает опытный граф рядом: `lab` → `graph-lab`.
	// Заведено 03.09.2026: опыты со схемой извлечения и связыванием сущностей
	// требуют новой сборки, а рабочий граф стоит недель работы видеокарты.
	Name string `toml:"name"`

	// Format — версия формата, которой заводится НОВЫЙ граф: 1 — рабочий
	// формат, 2 — схема 2 (GraphSchemaV2.md: журнал синонимов с
	// источником и свой промпт извлечения). 0 — формат 1. На собранный граф
	// не действует: его версия в паспорте. Формат 2 допустим только в разделе
	// именованного графа ([graph.lab]), в рабочий каталог graph он не попадает
	// никогда — решение владельца 04.09.2026.
	Format int `toml:"format"`

	// StemMinLen — короче этой длины основа слова не становится ключом поиска
	// по графу. 0 — умолчание (3). Замер 03.09.2026: понятие «ИИ» лежало
	// в указателе под основой «и» и приходило в 60 вопросов из 60.
	StemMinLen int `toml:"stem_min_len"`

	// StemMinBooks — сколько книг должно знать понятие, чтобы оно годилось
	// в ответ на совпадение по ОСНОВЕ слова (догадка о форме). 0 — умолчание (2).
	// Замер: «Связанность» (6 упоминаний в одной книге) приходила в 59 вопросов
	// из 60, потому что «связаны» совпадает с ней основой.
	StemMinBooks int `toml:"stem_min_books"`

	// SenseTie — ступень огрубления смысловой близости: всё, что попало в одну
	// ступень, считается одинаково близким, и выбор внутри неё решает
	// распространённость понятия. 0 — умолчание (0.05).
	SenseTie float64 `toml:"sense_tie"`

	// SenseMargin — насколько понятие должно быть выше середины верхушки, чтобы
	// попасть в смысловой вход. Порог относительный: абсолютный не работает,
	// у чужой пары близость бывает выше, чем у своей. 0 — умолчание (0.03).
	SenseMargin float64 `toml:"sense_margin"`

	// VectorAliases — сколько синонимов уходит в вектор понятия вместе с именем.
	// Длинный хвост размывает вектор, мусорный синоним в первых строках его
	// отравляет. 0 — умолчание (4).
	VectorAliases int `toml:"vector_aliases"`

	// MaxEvidences — сколько кусков-подтверждений показывать у одной связи.
	// 0 — умолчание (4).
	MaxEvidences int `toml:"max_evidences"`

	// Groups — как применять группы понятий в поиске: "union" (объединять
	// выдачу), "expand" (расширять запрос), "off". Пусто — "off". Ключ
	// --graph-groups перекрывает на один запуск.
	Groups string `toml:"groups"`

	// GroupsEnabled — учитывать группы вообще. false выключает их полностью,
	// не трогая режим. Пусто в TOML читается как false, поэтому умолчание
	// (true) подставляется в UseGraph, если строки groups нет вовсе.
	GroupsEnabled *bool `toml:"groups_enabled"`

	// MergesEnabled — действуют ли склейки двойников. Держится для отката и
	// сравнения «граф со склейками» против «граф с группами». Nil — true.
	MergesEnabled *bool `toml:"merges_enabled"`

	Cache bool `toml:"cache"`

	// ShowMemory — показывать в строке состояния, сколько памяти держит
	// открытый граф («GR: 160 МБ»). Включено; значок появляется, только когда
	// граф действительно открыт.
	ShowMemory bool `toml:"show_memory"`

	// OpenSlowSeconds, OpenHotSeconds — пороги для сообщения о том, во что
	// обошлось открытие графа. Меньше первого — спокойный цвет, между ними —
	// оранжевый, выше второго — красный. 0 — 10 и 20 секунд.
	//
	// Числа вынесены в настройки не для красоты: они зависят от машины и от
	// того, сколько человек готов ждать. На быстром диске 10 секунд — уже
	// много, на медленном ноутбуке — норма.
	OpenSlowSeconds int `toml:"open_slow_seconds"`
	OpenHotSeconds  int `toml:"open_hot_seconds"`

	// OpenColorOK, OpenColorSlow, OpenColorHot — цвета для трёх состояний.
	// Пусто — песочный тусклый, светло-оранжевый, красный. Формат тот же,
	// что в [theme]: номер ANSI 256 либо "#rrggbb".
	OpenColorOK   string `toml:"open_color_ok"`
	OpenColorSlow string `toml:"open_color_slow"`
	OpenColorHot  string `toml:"open_color_hot"`

	// NeighborPool — во сколько раз шире показанного берётся пул для
	// пересортировки. 0 — три. Действует только при ненулевом весе.
	// Восьмикратный пул менял не порядок, а состав выдачи — треть связей
	// в ней оказывалась новой.
	NeighborPool int `toml:"neighbor_pool"`

	// RelationSnippet — сколько знаков выдержки из книги показывать под связью
	// графа. 0 — умолчание (140), отрицательное — не показывать вовсе.
	//
	// Строка «горутина —использует→ канал (подтверждений 48)» говорит, ЧТО
	// связано, но не говорит, ЧЕМ именно. Выдержка берётся из куска, которым
	// связь подтверждена, — окном вокруг имени понятия. Стоит она места
	// в контексте модели, поэтому вынесена в настройку: на коротком окне
	// её разумно выключить.
	RelationSnippet int `toml:"relation_snippet"`

	// LinkMinCos — порог близости, с которого новое имя при сборке с ключом
	// --graph-link-new показывается арбитру как возможный двойник существующего
	// понятия (этап 90, пункт 5). 0 — умолчание 0.85. Ниже — кандидатов
	// больше и арбитр чаще; выше — двойники проскакивают в новые узлы.
	LinkMinCos float64 `toml:"link_min_cos"`

	// Ниже — архив коллекции с графом (см. internal/graph/archive.go).
	// Рабочий граф — недели работы видеокарты, и лучше старый рабочий граф,
	// чем никакой: снимок делается сам, по расписанию, пока ollchat запущен.
	// Настройки лежат в разделе графа, потому что архив нужен графу; снимается
	// при этом коллекция целиком — иначе граф после пересборки коллекции
	// указывал бы не на те книги (номера книг раздаёт индексация).

	// ArchiveDir — каталог архивов. Пусто — ~/.local/share/ollchat/kb_archives.
	ArchiveDir string `toml:"archive_dir"`
	// ArchiveEvery — как часто снимать архив: "24h". Пусто — раз в сутки;
	// "off" или "0" — не снимать по расписанию (руками — /graph archive).
	ArchiveEvery string `toml:"archive_every"`
	// ArchiveKeep — сколько последних плановых архивов коллекции хранить,
	// старые удаляются. 0 — хранить все. Умолчание — 7.
	ArchiveKeep int `toml:"archive_keep"`
}

// DefaultArchiveDir — где лежат архивы, если archive_dir не задан.
const DefaultArchiveDir = "~/.local/share/ollchat/kb_archives"

// ArchiveDirPath — каталог архивов с раскрытым «~» и умолчанием.
//
// Раскрывается при чтении, а не в finalize: основа настроек графа
// запоминается до раскрытия путей, и именованный граф без своего раздела
// получил бы нераскрытый путь.
func (g Graph) ArchiveDirPath() string {
	if strings.TrimSpace(g.ArchiveDir) == "" {
		return ExpandPath(DefaultArchiveDir)
	}
	return ExpandPath(g.ArchiveDir)
}

// ArchiveEveryDuration — период плановых архивов; 0 — по расписанию не снимать.
func (g Graph) ArchiveEveryDuration() (time.Duration, error) {
	v := strings.TrimSpace(strings.ToLower(g.ArchiveEvery))
	switch v {
	case "":
		return 24 * time.Hour, nil
	case "off", "0", "false", "никогда":
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("graph.archive_every = %q: ожидается срок вроде \"24h\" или \"off\"", g.ArchiveEvery)
	}
	if d < 10*time.Minute {
		return 0, fmt.Errorf("graph.archive_every = %q: архив занимает до минуты и снимается не чаще, чем раз в 10 минут", g.ArchiveEvery)
	}
	return d, nil
}

// ServerTimeoutDuration — предел ожидания общей библиотеки.
func (k KB) ServerTimeoutDuration() time.Duration {
	if d, err := time.ParseDuration(k.ServerTimeout); err == nil && d > 0 {
		return d
	}
	return 30 * time.Second
}

// HintsAtStartup — показывать ли советы при запуске.
func (g General) HintsAtStartup() bool { return !strings.EqualFold(g.StartupHints, "off") }

// Пороги и цвета сообщения об открытии графа: подставляют умолчания.
//
// Умолчания названы владельцем 29.08.2026: до 10 секунд — песочный тусклый,
// от 10 до 20 — светло-оранжевый, выше — красный.
func (g Graph) OpenSlow() time.Duration {
	if g.OpenSlowSeconds <= 0 {
		return 10 * time.Second
	}
	return time.Duration(g.OpenSlowSeconds) * time.Second
}

func (g Graph) OpenHot() time.Duration {
	if g.OpenHotSeconds <= 0 {
		return 20 * time.Second
	}
	return time.Duration(g.OpenHotSeconds) * time.Second
}

func (g Graph) ColorOK() string   { return colorOr(g.OpenColorOK, "180") }
func (g Graph) ColorSlow() string { return colorOr(g.OpenColorSlow, "214") }
func (g Graph) ColorHot() string  { return colorOr(g.OpenColorHot, "203") }

func colorOr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
}

// Has сообщает, есть ли раздел в файле настроек.
//
// **Зачем это, если у настроек есть умолчания.** Раздел `[kb]` можно не писать
// вовсе — программа возьмёт значения по умолчанию и будет работать. Но там,
// где библиотеки книг нет и не будет, команды `/kb` и `/graph` в меню только
// мешают: человек их видит, зовёт и получает «коллекций нет». Отсутствие
// раздела — внятное «мне это не нужно», и по нему команды скрываются.
//
// Конфига нет на диске вовсе (первый запуск) — считаем, что есть всё: прятать
// от нового пользователя половину команд незачем.
func (c *Config) Has(section string) bool {
	if c.sections == nil {
		return true
	}
	for _, s := range c.sections {
		if s == section {
			return true
		}
	}
	return false
}

// EmbedTimeoutDuration — сколько ждать ответа сервера эмбеддингов.
// QueryTimeoutDuration — сколько ждать вектор одного вопроса.
//
// Ноль означает «не ждать вовсе», и это осмысленный выбор: на машине, где карта
// вечно занята сборкой, ожидание не даёт ничего, кроме задержки.
func (k KB) QueryTimeoutDuration() time.Duration {
	v := strings.TrimSpace(k.QueryTimeout)
	if v == "" {
		return 15 * time.Second
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 15 * time.Second
	}
	return d
}

// ReadAroundCount — сколько соседних кусков показывает /read.
func (k KB) ReadAroundCount() int {
	if k.ReadAround <= 0 {
		return 1
	}
	return k.ReadAround
}

// EmbedCheckDuration — как часто проверять доступность модели эмбеддингов.
// Ноль выключает повторные проверки, оставляя только начальную.
func (k KB) EmbedCheckDuration() time.Duration {
	v := strings.TrimSpace(k.EmbedCheck)
	if v == "" {
		return time.Minute
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return time.Minute
	}
	return d
}

func (k KB) EmbedTimeoutDuration() time.Duration {
	if d, err := time.ParseDuration(k.EmbedTimeout); err == nil && d > 0 {
		return d
	}
	return 15 * time.Minute
}

// RerankTimeoutDuration — предел ожидания ответа службы переранжирования.
func (k KB) RerankTimeoutDuration() time.Duration {
	if d, err := time.ParseDuration(k.RerankTimeout); err == nil && d > 0 {
		return d
	}
	return time.Minute
}

// Confluence — доступ к корпоративной вики.
//
// Токена здесь нет и не будет. Файл настроек копируют, пересылают и кладут
// в репозиторий; секрету в нём не место. Указывается только **откуда взять**
// токен, а сам он живёт в файле с правами 600, в переменной окружения
// или приходит командой на сеанс.
type Confluence struct {
	URL string `toml:"url"`

	// TokenFile — путь к файлу с токеном. Права проверяются при чтении:
	// файл, читаемый всеми, отклоняется с объяснением.
	TokenFile string `toml:"token_file"`

	// TokenEnv — имя переменной окружения с токеном.
	TokenEnv string `toml:"token_env"`

	// TokenCmd — команда, печатающая токен: для хранилищ паролей.
	TokenCmd string `toml:"token_cmd"`

	Timeout string `toml:"timeout"`
}

// TimeoutDuration — предел ожидания Confluence.
func (c Confluence) TimeoutDuration() time.Duration {
	if d, err := time.ParseDuration(c.Timeout); err == nil && d > 0 {
		return d
	}
	return 60 * time.Second
}

// Web — поиск в сети.
//
// Публичные поисковики запросы от программ отклоняют, поэтому нужен свой
// экземпляр SearXNG: ollscripts/searxngmanage.sh поднимает его одной командой.
// Пустой адрес выключает инструмент web_search целиком.
type Web struct {
	SearxngURL string `toml:"searxng_url"`
	Timeout    string `toml:"timeout"`
}

// TimeoutDuration разбирает предел ожидания поисковика.
func (w Web) TimeoutDuration() time.Duration {
	if d, err := time.ParseDuration(w.Timeout); err == nil && d > 0 {
		return d
	}
	return 20 * time.Second
}

// Server — описание одного сервера Ollama.
type Server struct {
	Name        string `toml:"name"`
	URL         string `toml:"url"`
	Model       string `toml:"model"`
	Timeout     string `toml:"timeout"`
	ChatTimeout string `toml:"chat_timeout"`

	// StallTimeout — сколько поток ответа может молчать, прежде чем запрос
	// будет оборван. Пусто — две минуты, «0» — сторож выключен.
	//
	// Это не то же, что chat_timeout: тот ограничивает ожидание заголовков,
	// то есть время до первого признака ответа. Молчание уже начавшегося
	// потока не ограничивал никто, и 27.08.2026 это стоило дорого: запрос
	// с боевого сервера провисел в очереди 9 часов 49 минут и всё это время
	// держал единственный слот модели, из-за чего сервер был недоступен
	// остальным. Разбор — в internal/ollama/stall.go.
	StallTimeout string            `toml:"stall_timeout"`
	KeepAlive    string            `toml:"keep_alive"`
	SystemPrompt string            `toml:"system_prompt"`
	Think        *bool             `toml:"think"`
	Headers      map[string]string `toml:"headers"`
	Options      map[string]any    `toml:"options"`
	// VRAMGiB — сколько видеопамяти у карты сервера. Нужен только для грубого
	// расчёта /calc, когда замеров olldiagtools ещё нет: по сети размер карты
	// узнать неоткуда, Ollama его не сообщает.
	VRAMGiB float64 `toml:"vram_gib"`

	timeout      time.Duration
	chatTimeout  time.Duration
	stallTimeout time.Duration
}

// VRAMMiB возвращает размер видеопамяти сервера в МиБ, 0 — не задан.
func (s Server) VRAMMiB() float64 { return s.VRAMGiB * 1024 }

// TimeoutDuration возвращает разобранный таймаут ожидания заголовков у
// быстрых вызовов (Version/Tags/PS/Show).
func (s Server) TimeoutDuration() time.Duration { return s.timeout }

// ChatTimeoutDuration возвращает разобранный таймаут ожидания заголовков у
// потокового /api/chat. Он намного щедрее TimeoutDuration: Ollama не шлёт ни
// байта ответа, пока не обработает весь промпт, а на большом контексте,
// пересчитываемом с нуля, это может честно занять много минут.
func (s Server) ChatTimeoutDuration() time.Duration { return s.chatTimeout }

// StallTimeoutDuration — сколько поток ответа может молчать. Ноль — без предела.
func (s Server) StallTimeoutDuration() time.Duration { return s.stallTimeout }

// NumCtx возвращает num_ctx из options, если он задан явно.
func (s Server) NumCtx() (int, bool) {
	v, ok := s.Options["num_ctx"]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case int64:
		return int(n), n > 0
	case int:
		return n, n > 0
	case float64:
		return int(n), n > 0
	}
	return 0, false
}

// Default возвращает конфигурацию по умолчанию — она же основа для --init-config.
func Default() *Config {
	return &Config{
		General: General{
			DefaultServer:  "local",
			RenderMarkdown: true,
			ShowThinking:   true,
			// По умолчанию итог: он точен, берётся из ответа сервера
			// и не мельтешит в строке во время генерации.
			TokenSpeed: "final",
			Today:      true,
			ShowTurnID: true,
			Mode:       ModeSafe,
		},
		Theme: Theme{
			Style:      ThemeAuto,
			CodeTheme:  DefaultCodeTheme,
			InlineCode: DefaultInlineCode,
		},
		Input: Input{
			Mouse: true,
			Cursor: Cursor{
				Shape: CursorBlock,
				Blink: true,
				Color: "",
			},
		},
		Log: Log{
			Enabled:     true,
			Dir:         "~/.local/share/ollchat/logs",
			FilePattern: DefaultFilePattern,
			LogThinking: false,
			LogTools:    true,
		},
		Agent: Agent{
			Enabled:       true,
			MaxIterations: 25,
			MaxRetries:    2,
			CompactAt:     0.75,
			CompactKeep:   6,
			Tools: []string{"read_file", "list_dir", "grep", "write_file", "edit_file",
				"bash", "http_fetch", "view_image", "kb_search", "kb_read"},
			BashTimeout: "120s",
			MaxOutputKB: 64,
		},
		Permissions: Permissions{
			Allow: []string{"Read(./**)", "Bash(go build:*)", "Bash(go test:*)", "Bash(git status:*)", "Bash(git diff:*)"},
			Ask:   []string{"Write(./**)", "Bash(*)", "Fetch(*)"},
			Deny: []string{
				"Read(./.env)", "Read(~/.ssh/**)", "Read(~/.aws/**)",
				"Write(~/.ssh/**)",
				"Bash(rm:*)", "Bash(rmdir:*)", "Bash(sudo:*)", "Bash(su:*)",
				"Bash(mkfs:*)", "Bash(mkfs.ext4:*)", "Bash(dd:*)", "Bash(fdisk:*)", "Bash(parted:*)",
				"Bash(shutdown:*)", "Bash(reboot:*)", "Bash(halt:*)", "Bash(poweroff:*)", "Bash(init:*)",
				"Bash(chmod:*)", "Bash(chown:*)", "Bash(mount:*)", "Bash(umount:*)",
				"Bash(kill:*)", "Bash(killall:*)", "Bash(pkill:*)",
				"Bash(systemctl:*)", "Bash(service:*)", "Bash(crontab:*)",
				"Bash(apt:*)", "Bash(apt-get:*)", "Bash(dpkg:*)", "Bash(snap:*)",
				"Bash(useradd:*)", "Bash(userdel:*)", "Bash(passwd:*)", "Bash(visudo:*)",
				"Bash(iptables:*)", "Bash(nft:*)", "Bash(ip:*)",
				"Bash(curl:*)", "Bash(wget:*)", "Bash(nc:*)", "Bash(ssh:*)", "Bash(scp:*)",
			},
		},
		KB: KB{
			Dir:        "~/.local/share/ollchat/kb",
			Workers:    0, // 0 — по числу физических ядер
			MaxBookMB:  512,
			TopK:       8,
			MaxPerBook: 3,
			// Смысловой поиск включён, но модель не задана: без неё он молчит,
			// а с ней начинает работать без правки остальных настроек.
			Semantic:     true,
			EmbedModel:   "",
			EmbedBatch:   64,
			EmbedWorkers: 2,
			// Порог близости выключен намеренно: замер на живой библиотеке
			// показал, что он режет полезное раньше вредного. Подробности —
			// в kb_semantic_search.md.
			MinCosine: 0,
			// Веса поровну: замер показал, что между 0.3 и 1.0 разницы нет,
			// а ниже 0.3 польза от смысла пропадает.
			SemanticWeight: 1.5,
			EmbedKeepAlive: "5m",
			EmbedTimeout:   "15m",
		},
		Graph: Graph{
			// Модель не задана: сборка графа включается сознательно, потому
			// что это часы работы карты. Поиск по уже собранному графу
			// от этой настройки не зависит.
			Model:   "",
			Workers: 4,
			// Архив коллекции с графом: раз в сутки, семь последних —
			// решение владельца 04.09.2026. Каталог — DefaultArchiveDir.
			ArchiveEvery: "24h",
			ArchiveKeep:  7,
			// Кэш и показ памяти включены: и то и другое помогает молча,
			// а выключение осмысленно лишь там, где памяти мало.
			Cache:      true,
			ShowMemory: true,
			// Окно короткое: кусок книги это около тысячи знаков, а большое
			// окно занимает видеопамять зря и замедляет загрузку модели.
			NumCtx:    4096,
			MaxTokens: 900,
			// Низкая температура — из замеров в ModelsParams.md: чем она
			// выше, тем чаще модель отвечает не в том виде, который просили,
			// а здесь просят строгий JSON.
			Temperature: 0.2,
			KeepAlive:   "30m",
			Retry:       true,
		},
		Mix: Mix{
			// Книги выключены, граф включён. Это не вкусовщина, а цена:
			// восемь фрагментов книг — около двух тысяч токенов на каждый
			// вопрос, карта понятий — две-три сотни, и появляется она только
			// тогда, когда вопрос вообще связался с графом.
			Books:              false,
			Graph:              true,
			Entities:           6,
			Neighbors:          4,
			QuotesWithoutTools: 3,
		},
		Web: Web{Timeout: "20s"},
		Sandbox: Sandbox{
			Root:           ".",
			AllowOutside:   false,
			FollowSymlinks: false,
			MaxFileKB:      512,
			MaxPDFMB:       64,
		},
		Servers: []Server{{
			Name:      "local",
			URL:       "http://127.0.0.1:11434",
			Model:     "",
			Timeout:   "300s",
			KeepAlive: "10m",
		}},
	}
}

// DefaultPath — стандартное расположение конфига.
func DefaultPath() string {
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return filepath.Join(dir, "ollchat", "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "ollchat", "config.toml")
}

// ExpandPath раскрывает ведущий "~" в домашний каталог пользователя.
func ExpandPath(p string) string { return fsx.ExpandHome(p) }

// checkColor проверяет запись цвета: пусто (цвет терминала), номер ANSI 256
// от 0 до 255 либо шестнадцатеричная запись "#rgb" или "#rrggbb".
// Проверяем здесь, при загрузке, чтобы опечатка в конфиге всплыла сразу,
// а не превратилась молча в чёрный цвет на чёрном фоне.
func checkColor(s string) error {
	if s == "" {
		return nil
	}
	if strings.HasPrefix(s, "#") {
		hex := s[1:]
		if len(hex) != 3 && len(hex) != 6 {
			return fmt.Errorf("в записи %q должно быть 3 или 6 шестнадцатеричных цифр после #", s)
		}
		for _, r := range hex {
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return fmt.Errorf("в записи %q символ %q не является шестнадцатеричной цифрой", s, string(r))
			}
		}
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("значение %q не является ни номером цвета ANSI 256, ни записью вида \"#ff87d7\"", s)
	}
	if n < 0 || n > 255 {
		return fmt.Errorf("номер цвета ANSI 256 должен быть от 0 до 255, получено %d", n)
	}
	return nil
}

// Load читает конфиг из path. Отсутствие файла — не ошибка: возвращаются значения
// по умолчанию с флагом exists=false, чтобы приложение могло предложить --init-config.
func Load(path string) (cfg *Config, exists bool, err error) {
	cfg = Default()
	cfg.Path = path

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if verr := cfg.finalize(); verr != nil {
				return nil, false, verr
			}
			return cfg, false, nil
		}
		return nil, false, fmt.Errorf("чтение конфига %s: %w", path, err)
	}

	// Пользовательский конфиг накладывается на значения по умолчанию, но списки
	// (servers, tools, правила) заменяются целиком — так задумано: иначе от
	// deny-правил по умолчанию нельзя было бы отказаться осознанно.
	loaded := Default()
	loaded.Servers = nil
	// Шаблон имени журнала снимаем с умолчания перед разбором: иначе конфиг,
	// созданный до появления file_pattern, получил бы новый шаблон поверх своей
	// настройки pattern и незаметно сменил бы поведение журнала. Пустыми обе
	// настройки остаться не могут — умолчание подставит finalize.
	loaded.Log.FilePattern = ""
	// И по той же причине — правки цветов подсветки: TOML дописывает ключи
	// в существующую карту, а не заменяет её, и умолчание пережило бы любой
	// пользовательский раздел.
	loaded.Theme.Tokens = nil
	md, err := toml.Decode(string(data), loaded)
	if err != nil {
		return nil, true, fmt.Errorf("разбор конфига %s: %w", path, err)
	}
	loaded.Path = path
	// Какие разделы человек в файле написал. Не то же, что «у настройки есть
	// значение»: умолчания подставляются всем, и по значению не отличить
	// «не настраивал» от «настроил так же». А отличать нужно: команды
	// ненастроенной возможности в меню не показываются — см. Config.Has.
	for _, name := range []string{"kb", "graph", "mix", "confluence", "web"} {
		if md.IsDefined(name) {
			loaded.sections = append(loaded.sections, name)
		}
	}

	// Настройки отдельных графов: [graph.lab] поверх общего [graph].
	if loaded.GraphNamed, err = namedGraphs(string(data), loaded.Graph); err != nil {
		return nil, true, fmt.Errorf("разбор конфига %s: %w", path, err)
	}

	if err := loaded.finalize(); err != nil {
		return nil, true, err
	}
	return loaded, true, nil
}

// namedGraphs собирает настройки именованных графов: [graph.lab] поверх [graph].
//
// **Зачем.** Над одной библиотекой живут несколько графов, и опытный собирается
// другой моделью, другим промптом и другой схемой. Хранить это ключами запуска
// нельзя: граф стоит недель работы видеокарты, и «каким промптом он собран»
// должно читаться из файла, а не вспоминаться. Решение владельца 03.09.2026.
//
// Наследование по полям: раздел графа перекрывает только те настройки, которые
// в нём написаны, остальные берутся из общего [graph]. Поэтому подраздел
// разбирается ПОВЕРХ копии общих настроек, а не отдельно.
func namedGraphs(data string, base Graph) (map[string]Graph, error) {
	var raw map[string]any
	if _, err := toml.Decode(data, &raw); err != nil {
		return nil, err
	}
	sec, _ := raw["graph"].(map[string]any)
	if len(sec) == 0 {
		return nil, nil
	}
	out := map[string]Graph{}
	for name, v := range sec {
		sub, ok := v.(map[string]any)
		if !ok {
			continue // обычная настройка общего раздела, а не подраздел графа
		}
		if !graph.ValidName(name) || name == "" {
			return nil, fmt.Errorf("graph.%s: недопустимое имя графа "+
				"(строчные латинские буквы, цифры и дефис, до 32 знаков)", name)
		}
		var buf bytes.Buffer
		if err := toml.NewEncoder(&buf).Encode(sub); err != nil {
			return nil, fmt.Errorf("graph.%s: %w", name, err)
		}
		eff := base
		if _, err := toml.Decode(buf.String(), &eff); err != nil {
			return nil, fmt.Errorf("graph.%s: %w", name, err)
		}
		eff.Name = name
		out[name] = eff
	}
	return out, nil
}

// GraphFor отдаёт настройки для названного графа: раздел графа поверх общего.
// Для безымянного (рабочего) — общие настройки как есть.
func (c *Config) GraphFor(name string) Graph {
	base := c.Graph
	if c.graphBaseSet {
		base = c.graphBase
	}
	if name == "" {
		return base
	}
	if g, ok := c.GraphNamed[name]; ok {
		return g
	}
	// Раздела нет — это не ошибка: опытный граф может собираться теми же
	// настройками, что рабочий. Имя всё равно проставляем, чтобы дальше
	// по коду было видно, о каком графе речь.
	g := base
	g.Name = name
	return g
}

// UseGraph выбирает граф: и каталог на диске, и настройки его сборки.
//
// Настройки подменяются здесь, в одном месте, а не тащатся параметром через
// шесть десятков мест, где читается cfg.Graph. Цена — состояние; выигрыш —
// невозможность забыть про подмену в одном из них.
func (c *Config) UseGraph(name string) error {
	if err := (graph.Rules{Name: name}).Validate(); err != nil {
		return err
	}
	c.Graph = c.GraphFor(name)
	return nil
}

// EmbedOptions — настройки эмбеддера для kbembed.New.
func (k KB) EmbedOptions() kbembed.Options {
	return kbembed.Options{URL: k.EmbedURL, Model: k.EmbedModel, KeepAlive: k.EmbedKeepAlive,
		Timeout: k.EmbedTimeoutDuration(), CacheDir: k.Dir}
}

// RerankOptions — настройки переранжирования для kbrerank.New.
func (k KB) RerankOptions() kbrerank.Options {
	return kbrerank.Options{URL: k.RerankURL, Model: k.RerankModel, Timeout: k.RerankTimeoutDuration()}
}

// ExtractOptions — настройки извлечения для graphex.New.
func (g Graph) ExtractOptions() graphex.Options {
	return graphex.Options{URL: g.URL, Model: g.Model, KeepAlive: g.KeepAlive, Workers: g.Workers,
		NumCtx: g.NumCtx, MaxTokens: g.MaxTokens, Temperature: g.Temperature}
}

// Rules собирает правила открытия графа из его раздела настроек.
//
// С этапа 91 (R3) это единственный путь от настроек к поведению графа:
// пакет graph глобалов не держит, правила передаются в graph.Open и живут
// в открытом графе. Два графа с разными правилами в одном процессе — норма.
func (g Graph) Rules() graph.Rules {
	// Режим групп: выключен, если groups_enabled=false, иначе — что задано.
	mode := g.Groups
	if g.GroupsEnabled != nil && !*g.GroupsEnabled {
		mode = graph.GroupOff
	}
	return graph.Rules{
		Name:          g.Name,
		StemMinLen:    g.StemMinLen,
		StemMinBooks:  g.StemMinBooks,
		SenseTie:      g.SenseTie,
		SenseMargin:   g.SenseMargin,
		VectorAliases: g.VectorAliases,
		MaxEvidences:  g.MaxEvidences,
		Groups:        mode,
		MergesOff:     g.MergesEnabled != nil && !*g.MergesEnabled,
		Format:        g.Format,
	}
}

// finalize проверяет корректность значений и раскрывает производные поля.
func (c *Config) finalize() error {
	// Основа настроек графа запоминается до любых подмен выбором графа.
	if !c.graphBaseSet {
		c.graphBase, c.graphBaseSet = c.Graph, true
	}
	// Старые написания приводятся к нынешнему имени молча: конфиг человека
	// не должен ломаться из-за того, что мы переименовали режим.
	switch c.General.Mode {
	case ModeYolo, "no-ask", "dont-ask", "no_ask":
		c.General.Mode = ModeNoAsk
	}
	switch c.General.Mode {
	case ModeSafe, ModeAutoEdit, ModeNoAsk:
	case "":
		c.General.Mode = ModeSafe
	default:
		return fmt.Errorf("general.mode: недопустимое значение %q (ожидается safe, auto-edit или noask)", c.General.Mode)
	}

	// Имя графа становится частью пути на диске, поэтому проверяется здесь,
	// а не там, где открывается: опечатка в имени должна быть ошибкой запуска,
	// а не тихой сборкой второго графа в каталоге со странным именем.
	if !graph.ValidName(c.Graph.Name) {
		return fmt.Errorf("graph.name: недопустимое имя %q "+
			"(строчные латинские буквы, цифры и дефис, до 32 знаков; пусто — рабочий граф)",
			c.Graph.Name)
	}
	// Формат графа: неизвестный — ошибка запуска; формат 2 — только у
	// именованного графа. Проверяется и общий раздел, и каждый подраздел:
	// [graph] с format = 2 означал бы заведение рабочего графа новым форматом.
	if f := c.graphBase.Format; f != 0 && (!graph.KnownVersion(f) || f >= graph.FormatV2) {
		return fmt.Errorf("graph.format = %d: в общем разделе допустим только формат 1; "+
			"формат %d задаётся в разделе именованного графа, например [graph.lab]", f, graph.FormatV2)
	}
	for name, g := range c.GraphNamed {
		if g.Format != 0 && !graph.KnownVersion(g.Format) {
			return fmt.Errorf("graph.%s.format = %d: неизвестный формат графа", name, g.Format)
		}
	}

	// Скорость ответа в строке состояния. Недопустимое значение — ошибка
	// запуска с именем настройки, а не тихая подмена умолчанием: человек
	// написал что-то осмысленное и должен узнать, что его не поняли.
	switch c.General.TokenSpeed {
	case "off", "live", "final", "full":
	case "":
		c.General.TokenSpeed = "final"
	default:
		return fmt.Errorf("general.token_speed: недопустимое значение %q (ожидается off, live, final или full)",
			c.General.TokenSpeed)
	}

	switch c.Input.Cursor.Shape {
	case CursorBlock, CursorUnderline, CursorBar:
	case "":
		c.Input.Cursor.Shape = CursorBlock
	default:
		return fmt.Errorf("input.cursor.shape: недопустимое значение %q (ожидается block, underline или bar)",
			c.Input.Cursor.Shape)
	}
	// Ноль означает «по умолчанию», всё остальное должно быть разумным:
	// панель выше экрана не поместится, а отрицательная высота — опечатка.
	if c.Input.FindRows < 0 || c.Input.FindRows > 20 {
		return fmt.Errorf("input.find_rows = %d: допустимо от 1 до 20 (0 — по умолчанию)",
			c.Input.FindRows)
	}
	if c.Input.CommandRows < 0 || c.Input.CommandRows > 20 {
		return fmt.Errorf("input.command_rows = %d: допустимо от 1 до 20 (0 — по умолчанию)",
			c.Input.CommandRows)
	}
	if err := checkColor(c.Input.Cursor.Color); err != nil {
		return fmt.Errorf("input.cursor.color: %w", err)
	}

	if err := c.Theme.validate(); err != nil {
		return err
	}

	// Устаревшая настройка pattern продолжает работать, но только когда
	// file_pattern не задан; обе пустые — берём умолчание.
	if strings.TrimSpace(c.Log.FilePattern) == "" && strings.TrimSpace(c.Log.Pattern) == "" {
		c.Log.FilePattern = DefaultFilePattern
	}
	if _, err := c.Log.NamePattern(); err != nil {
		return fmt.Errorf("log.file_pattern: %w", err)
	}
	c.Log.Dir = ExpandPath(c.Log.Dir)

	// Ноль — настройка не задана, подставляем умолчание. Отрицательное —
	// осознанное «без ограничения», и затирать его нельзя.
	if c.Agent.MaxIterations == 0 {
		c.Agent.MaxIterations = 25
	}
	if c.Agent.CompactAt < 0 || c.Agent.CompactAt >= 1 {
		return fmt.Errorf("agent.compact_at = %v: доля окна от 0 до 1 (0 — не сжимать)", c.Agent.CompactAt)
	}
	if c.Agent.CompactKeep < 0 {
		return fmt.Errorf("agent.compact_keep = %d: число сообщений не бывает отрицательным", c.Agent.CompactKeep)
	}
	if c.Agent.MaxRetries < 0 {
		c.Agent.MaxRetries = 0
	}
	if c.Agent.MaxOutputKB <= 0 {
		c.Agent.MaxOutputKB = 64
	}
	if c.Agent.BashTimeout == "" {
		c.Agent.BashTimeout = "120s"
	}
	d, err := time.ParseDuration(c.Agent.BashTimeout)
	if err != nil {
		return fmt.Errorf("agent.bash_timeout: %w", err)
	}
	c.Agent.bashTimeout = d

	// Просмотрщики: строка должна быть командой, а не голой подстановкой.
	//
	// Проверяется наличие имени программы, а не её существование в системе:
	// конфиг переезжает между машинами, и отказ запускаться из-за отсутствия
	// zathura на другой машине хуже, чем внятная ошибка в момент открытия.
	for name, v := range map[string]string{
		"viewers.pdf":  c.Viewers.PDF,
		"viewers.epub": c.Viewers.EPUB,
		"viewers.md":   c.Viewers.MD,
	} {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		first := strings.Fields(v)[0]
		if strings.Contains(first, "{file}") || strings.Contains(first, "{page}") {
			return fmt.Errorf("%s = %q: первым словом должна стоять программа, "+
				"а не подстановка — например \"zathura -P {page} {file}\"", name, v)
		}
	}

	// Советы при запуске: пусто — показывать. Значение проверяется, а не
	// приводится молча к умолчанию: "of" вместо "off" иначе включило бы
	// то, что человек выключал.
	switch strings.ToLower(strings.TrimSpace(c.General.StartupHints)) {
	case "", "on":
		c.General.StartupHints = "on"
	case "off":
		c.General.StartupHints = "off"
	default:
		return fmt.Errorf("general.startup_hints = %q: допустимо on или off",
			c.General.StartupHints)
	}

	// «0» здесь законно и означает «не ждать»; отрицательное — опечатка.
	if v := strings.TrimSpace(c.KB.QueryTimeout); v != "" {
		if d, err := time.ParseDuration(v); err != nil || d < 0 {
			return fmt.Errorf("kb.query_timeout = %q: ожидается длительность вида \"15s\" или \"0\"", v)
		}
	}
	if c.KB.ReadAround < 0 || c.KB.ReadAround > 5 {
		return fmt.Errorf("kb.read_around = %d: допустимо от 0 до 5", c.KB.ReadAround)
	}

	if v := strings.TrimSpace(c.KB.EmbedCheck); v != "" {
		if d, err := time.ParseDuration(v); err != nil || d < 0 {
			return fmt.Errorf("kb.embed_check = %q: ожидается длительность вида \"1m\", \"30s\" или \"0\"", v)
		}
	}

	// Общая библиотека: адрес проверяется при запуске, а не при первом вопросе.
	// «kb.corp.local:8377» без схемы — самая частая опечатка, и узнавать о ней
	// посреди работы дороже, чем на старте.
	if u := strings.TrimSpace(c.KB.ServerURL); u != "" {
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return fmt.Errorf("kb.server_url = %q: нужен адрес со схемой, например http://%s", u, u)
		}
		c.KB.ServerURL = strings.TrimRight(u, "/")
	}
	if c.KB.ServerTimeout != "" {
		if _, err := time.ParseDuration(c.KB.ServerTimeout); err != nil {
			return fmt.Errorf("kb.server_timeout: %w", err)
		}
	}
	c.KB.ServerTokenFile = ExpandPath(c.KB.ServerTokenFile)

	// Ранжирование связей графа. Отрицательный вес — не «выключено наоборот»,
	// а опечатка: он поднял бы наверх связи, наиболее далёкие от вопроса.
	if c.Graph.NeighborSenseWeight < 0 {
		return fmt.Errorf("graph.neighbor_sense_weight = %v: вес не бывает отрицательным (0 — не пересортировывать)",
			c.Graph.NeighborSenseWeight)
	}
	// Пороги открытия графа: порядок важен, иначе «жёлтый» никогда не покажется.
	if c.Graph.OpenSlowSeconds < 0 || c.Graph.OpenHotSeconds < 0 {
		return fmt.Errorf("graph.open_slow_seconds и graph.open_hot_seconds не бывают отрицательными")
	}
	if c.Graph.OpenSlowSeconds > 0 && c.Graph.OpenHotSeconds > 0 &&
		c.Graph.OpenHotSeconds <= c.Graph.OpenSlowSeconds {
		return fmt.Errorf("graph.open_hot_seconds (%d) должен быть больше graph.open_slow_seconds (%d): "+
			"иначе среднее состояние никогда не показывается",
			c.Graph.OpenHotSeconds, c.Graph.OpenSlowSeconds)
	}
	for name, v := range map[string]string{
		"graph.open_color_ok":   c.Graph.OpenColorOK,
		"graph.open_color_slow": c.Graph.OpenColorSlow,
		"graph.open_color_hot":  c.Graph.OpenColorHot,
	} {
		if err := checkColor(v); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}

	if c.Graph.NeighborPool < 0 {
		return fmt.Errorf("graph.neighbor_pool = %d: допустимо от 1 (0 — по умолчанию)", c.Graph.NeighborPool)
	}
	if _, err := c.Graph.ArchiveEveryDuration(); err != nil {
		return err
	}
	if c.Graph.ArchiveKeep < 0 {
		return fmt.Errorf("graph.archive_keep = %d: допустимо 0 (хранить все) и больше", c.Graph.ArchiveKeep)
	}

	if c.Sandbox.Root == "" {
		c.Sandbox.Root = "."
	}
	c.Sandbox.Root = ExpandPath(c.Sandbox.Root)
	// База знаний.
	c.KB.Dir = ExpandPath(c.KB.Dir)
	if c.KB.Dir == "" {
		c.KB.Dir = ExpandPath("~/.local/share/ollchat/kb")
	}
	for i, r := range c.KB.Roots {
		c.KB.Roots[i] = ExpandPath(r)
	}
	if c.KB.MaxBookMB <= 0 {
		c.KB.MaxBookMB = 512
	}
	if c.KB.TopK <= 0 {
		c.KB.TopK = 8
	}
	if c.KB.MaxPerBook <= 0 {
		c.KB.MaxPerBook = 3
	}
	if c.KB.Workers < 0 {
		c.KB.Workers = 0
	}
	if _, err := c.Log.StepsPattern(); err != nil {
		return fmt.Errorf("log.steps_file_pattern: %w", err)
	}
	if _, err := time.ParseDuration(c.KB.EmbedTimeout); c.KB.EmbedTimeout != "" && err != nil {
		return fmt.Errorf("kb.embed_timeout: %w (пример: \"15m\")", err)
	}

	if c.Mix.Entities <= 0 {
		c.Mix.Entities = 6
	}
	if c.Mix.Neighbors < 0 {
		c.Mix.Neighbors = 4
	}
	if c.Mix.QuotesWithoutTools < 0 {
		c.Mix.QuotesWithoutTools = 0
	}
	// Прежнее имя настройки. kb.auto жил в конфигах раньше [mix], и молча
	// перестать его слушать значит выключить человеку подмешивание, которым
	// он пользовался.
	if c.KB.Auto {
		c.Mix.Books = true
	}

	if c.Sandbox.MaxPDFMB <= 0 {
		c.Sandbox.MaxPDFMB = 64
	}
	if c.Sandbox.MaxFileKB <= 0 {
		c.Sandbox.MaxFileKB = 512
	}

	if len(c.Servers) == 0 {
		return fmt.Errorf("в конфиге не задано ни одного сервера ([[servers]])")
	}

	seen := make(map[string]bool, len(c.Servers))
	for i := range c.Servers {
		s := &c.Servers[i]
		if s.Name == "" {
			return fmt.Errorf("servers[%d]: не задано поле name", i)
		}
		if seen[s.Name] {
			return fmt.Errorf("servers[%d]: имя сервера %q встречается дважды", i, s.Name)
		}
		seen[s.Name] = true

		if s.URL == "" {
			return fmt.Errorf("сервер %q: не задан url", s.Name)
		}
		u, err := url.Parse(s.URL)
		if err != nil {
			return fmt.Errorf("сервер %q: некорректный url: %w", s.Name, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("сервер %q: url должен начинаться с http:// или https://", s.Name)
		}
		if u.Host == "" {
			return fmt.Errorf("сервер %q: в url не указан хост", s.Name)
		}
		s.URL = strings.TrimRight(s.URL, "/")

		if s.Timeout == "" {
			s.Timeout = "300s"
		}
		d, err := time.ParseDuration(s.Timeout)
		if err != nil {
			return fmt.Errorf("сервер %q: timeout: %w", s.Name, err)
		}
		s.timeout = d

		if s.StallTimeout == "" {
			s.StallTimeout = "2m"
		}
		if s.ChatTimeout == "" {
			s.ChatTimeout = "30m"
		}
		cd, err := time.ParseDuration(s.ChatTimeout)
		if err != nil {
			return fmt.Errorf("сервер %q: chat_timeout: %w", s.Name, err)
		}
		s.chatTimeout = cd

		// Ноль здесь — осознанное «сторожа не надо», а не забытая настройка:
		// такое бывает нужно на машине, где модель загружается минутами.
		sd, err := time.ParseDuration(s.StallTimeout)
		if err != nil {
			return fmt.Errorf("сервер %q: stall_timeout: %w", s.Name, err)
		}
		if sd < 0 {
			return fmt.Errorf("сервер %q: stall_timeout не может быть отрицательным", s.Name)
		}
		s.stallTimeout = sd

		if s.KeepAlive != "" {
			if _, err := time.ParseDuration(s.KeepAlive); err != nil {
				return fmt.Errorf("сервер %q: keep_alive: %w", s.Name, err)
			}
		}
	}

	if c.General.DefaultServer == "" {
		c.General.DefaultServer = c.Servers[0].Name
	} else if !seen[c.General.DefaultServer] {
		return fmt.Errorf("general.default_server: сервер %q не описан в конфиге", c.General.DefaultServer)
	}

	return nil
}

// ServerByName возвращает сервер по имени.
func (c *Config) ServerByName(name string) (*Server, bool) {
	for i := range c.Servers {
		if c.Servers[i].Name == name {
			return &c.Servers[i], true
		}
	}
	return nil, false
}

// ServerNames возвращает имена серверов в порядке их описания в конфиге.
func (c *Config) ServerNames() []string {
	names := make([]string, 0, len(c.Servers))
	for i := range c.Servers {
		names = append(names, c.Servers[i].Name)
	}
	return names
}

// ToolEnabled сообщает, разрешён ли инструмент списком agent.tools.
func (c *Config) ToolEnabled(name string) bool {
	for _, t := range c.Agent.Tools {
		if t == name {
			return true
		}
	}
	return false
}

// ParseTokens разбирает запись числа токенов: 32768, 32k, 128К, 1m.
// Суффиксы считаются двоичными: k — это 1024, как принято для размеров окна.
func ParseTokens(s string) (int, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	t = strings.ReplaceAll(t, " ", "")
	runes := []rune(t)
	if len(runes) == 0 {
		return 0, fmt.Errorf("пустое значение")
	}

	// Буквы принимаем и латинские, и русские: раскладку ради одной клавиши
	// переключать не хочется.
	mult := 1
	switch runes[len(runes)-1] {
	case 'k', 'к':
		mult, runes = 1024, runes[:len(runes)-1]
	case 'm', 'м':
		mult, runes = 1024*1024, runes[:len(runes)-1]
	}

	n, err := strconv.Atoi(string(runes))
	if err != nil {
		return 0, fmt.Errorf("не понимаю %q — ожидается число токенов, например 32768 или 32k", s)
	}
	if n <= 0 {
		return 0, fmt.Errorf("прибавка должна быть положительной, получено %d", n)
	}
	return n * mult, nil
}
