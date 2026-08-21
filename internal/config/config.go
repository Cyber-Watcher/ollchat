// Package config загружает и валидирует файл настроек ollchat (TOML).
package config

import (
	"fmt"
	"maps"
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
)

// Режимы подтверждения действий агента.
const (
	ModeSafe     = "safe"      // запись и bash — всегда спрашивать
	ModeAutoEdit = "auto-edit" // запись без спроса, bash — спрашивать
	ModeYolo     = "yolo"      // ничего не спрашивать (deny-правила всё равно действуют)
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
	Web         Web         `toml:"web"`
	Servers     []Server    `toml:"servers"`

	// Path — фактический путь, откуда загружен конфиг (не из файла).
	Path string `toml:"-"`
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
	ShowTurnID bool   `toml:"show_turn_id"`
	Mode       string `toml:"mode"`
}

// Input — поле ввода: курсор и работа с мышью.
type Input struct {
	// Mouse — запрашивать ли у терминала события мыши. При true работают колесо
	// и бегунок прокрутки, но выделить текст мышью нельзя: события уходят
	// приложению. При false лента выделяется и копируется средствами самого
	// терминала. Переключается на лету, в конфиге задано лишь начальное значение.
	Mouse  bool   `toml:"mouse"`
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
var DefaultTokens = map[string]string{"NameTag": "#83a598"}

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
		t.Tokens = maps.Clone(DefaultTokens)
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
	Enabled       bool     `toml:"enabled"`
	MaxIterations int      `toml:"max_iterations"`
	MaxRetries    int      `toml:"max_retries"`
	Tools         []string `toml:"tools"`
	BashTimeout   string   `toml:"bash_timeout"`
	MaxOutputKB   int      `toml:"max_output_kb"`

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
	AnswerStyle    string  `toml:"answer_style"`    // как модель должна отвечать по книгам
	EmbedKeepAlive string  `toml:"embed_keep_alive"`
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
	Name         string            `toml:"name"`
	URL          string            `toml:"url"`
	Model        string            `toml:"model"`
	Timeout      string            `toml:"timeout"`
	ChatTimeout  string            `toml:"chat_timeout"`
	KeepAlive    string            `toml:"keep_alive"`
	SystemPrompt string            `toml:"system_prompt"`
	Think        *bool             `toml:"think"`
	Headers      map[string]string `toml:"headers"`
	Options      map[string]any    `toml:"options"`
	// VRAMGiB — сколько видеопамяти у карты сервера. Нужен только для грубого
	// расчёта /calc, когда замеров olldiagtools ещё нет: по сети размер карты
	// узнать неоткуда, Ollama его не сообщает.
	VRAMGiB float64 `toml:"vram_gib"`

	timeout     time.Duration
	chatTimeout time.Duration
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
			ShowTurnID:     true,
			Mode:           ModeSafe,
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
			// в docs/kb_semantic_search.md.
			MinCosine: 0,
			// Веса поровну: замер показал, что между 0.3 и 1.0 разницы нет,
			// а ниже 0.3 польза от смысла пропадает.
			SemanticWeight: 1.0,
			EmbedKeepAlive: "5m",
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
func ExpandPath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			if p == "~" {
				return home
			}
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

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
	if _, err := toml.Decode(string(data), loaded); err != nil {
		return nil, true, fmt.Errorf("разбор конфига %s: %w", path, err)
	}
	loaded.Path = path

	if err := loaded.finalize(); err != nil {
		return nil, true, err
	}
	return loaded, true, nil
}

// finalize проверяет корректность значений и раскрывает производные поля.
func (c *Config) finalize() error {
	switch c.General.Mode {
	case ModeSafe, ModeAutoEdit, ModeYolo:
	case "":
		c.General.Mode = ModeSafe
	default:
		return fmt.Errorf("general.mode: недопустимое значение %q (ожидается safe, auto-edit или yolo)", c.General.Mode)
	}

	switch c.Input.Cursor.Shape {
	case CursorBlock, CursorUnderline, CursorBar:
	case "":
		c.Input.Cursor.Shape = CursorBlock
	default:
		return fmt.Errorf("input.cursor.shape: недопустимое значение %q (ожидается block, underline или bar)",
			c.Input.Cursor.Shape)
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

	if c.Agent.MaxIterations <= 0 {
		c.Agent.MaxIterations = 25
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

		if s.ChatTimeout == "" {
			s.ChatTimeout = "30m"
		}
		cd, err := time.ParseDuration(s.ChatTimeout)
		if err != nil {
			return fmt.Errorf("сервер %q: chat_timeout: %w", s.Name, err)
		}
		s.chatTimeout = cd

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
