// Package mcpclient — клиент MCP поверх Streamable HTTP: рукопожатие, список
// инструментов и их вызов у чужого сервера (этап 121, Б1).
//
// **Зачем.** ollchat умеет быть сервером MCP (`internal/mcp`, служба `ollmcp`),
// но не умеет быть клиентом: инструменты сторонних серверов модель не видит,
// а поиск через `ollmcp` идёт мимо MCP. Этот пакет — нижний слой для того
// и другого: реестр инструментов, права и ключи `--mcp-*` строятся поверх него.
//
// **Почему не готовое.** `internal/kbremote` — клиент собственного входа
// `/api/v1/*` службы ollmcp: ни JSON-RPC, ни сеансов, ни SSE, и сторонний
// сервер его не поймёт. `internal/mcp` — серверная сторона, её типы не
// экспортированы и описывают ответы, а не их разбор. Помощники
// `httpPeer.post` (`ollmcp/conformance_test.go`) и `postMCP`
// (`internal/mcp/transport_test.go`) — тестовые: шлют готовую строку и
// отдают тело как есть, без сеанса, повтора, срока и разбора потока.
//
// **Что поддержано — и что намеренно нет.** Только HTTP: запуск серверов
// командой (stdio) требует песочницы и отдельного этапа. Только ревизии
// с рукопожатием, 2024-11-05…2025-11-25 (решение владельца). Ревизия
// 2026-07-28 убрала `initialize` и сеансы: сервер, говорящий только на ней,
// получает понятный отказ с её названием, а не невнятную ошибку разбора.
// Старый транспорт HTTP+SSE ревизии 2024-11-05 (GET на `/sse`, потом POST на
// присланный адрес) не поддержан: на него — подсказка про адрес.
//
// Своя реализация, а не библиотека, по той же причине, что и у сервера
// (`internal/mcp/server.go`): протокол здесь — три метода и формат сообщения,
// а внешняя зависимость стоит дороже этих строк.
package mcpclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/buildinfo"
)

// Revision — ревизия, которую клиент просит в рукопожатии: новейшая из тех,
// что ещё с рукопожатием. Сервер вправе ответить своей, и тогда она годится,
// если входит в Revisions.
const Revision = "2025-11-25"

// revisions — ревизии, на которых клиент умеет разговаривать, от новой к старой.
//
// Разница между ними для клиента невелика, потому все четыре и принимаются:
// с 2025-03-26 ответ может прийти потоком SSE и появился номер сеанса,
// с 2025-06-18 после рукопожатия обязателен заголовок MCP-Protocol-Version,
// 2025-11-25 разрешил серверу закрыть поток до ответа и ждать переподключения.
// Всё это клиент делает всегда: старому серверу лишний заголовок не мешает,
// а Accept с обоими типами он и так обязан был понимать.
var revisions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// Revisions — ревизии MCP, которые понимает клиент, от новой к старой.
func Revisions() []string { return append([]string(nil), revisions...) }

// knownRevision — понимает ли клиент эту ревизию.
func knownRevision(v string) bool {
	for _, r := range revisions {
		if r == v {
			return true
		}
	}
	return false
}

// Ошибки, по которым вызывающий выбирает, что делать дальше. Сравниваются
// через errors.Is; текст у конкретной ошибки свой и называет сервер.
var (
	// ErrUnavailable — ответа нет вовсе: соединение не установилось,
	// оборвалось до ответа или истёк срок. Поиск через службу по нему
	// переходит на свои файлы (этап 121, А4).
	ErrUnavailable = errors.New("сервер MCP недоступен")

	// ErrUnauthorized — сервер ответил 401: ключа нет или он не тот.
	ErrUnauthorized = errors.New("сервер MCP требует ключ доступа")

	// ErrRevision — договориться о ревизии протокола не вышло.
	ErrRevision = errors.New("ревизия MCP не поддерживается")
)

// Opts — к какому серверу и как подключаться.
type Opts struct {
	// Server — имя сервера из конфига (`[mcp.servers.<имя>]`). Только для
	// сообщений: человеку надо сразу видеть, какой из серверов отказал.
	// Пусто — имя машины из адреса.
	Server string

	// URL — вход Streamable HTTP, например https://mcp.example/mcp.
	URL string

	// Token — ключ доступа, уходит заголовком Authorization: Bearer.
	// Пусто — без ключа. В текст ошибок не попадает никогда.
	Token string

	// Timeout — срок одного обмена с сервером. 0 и меньше — 60 с: чужой
	// инструмент может честно работать дольше поиска по своей библиотеке,
	// но не бесконечно, а модель всё это время ждёт.
	Timeout time.Duration

	// Version — что ollchat называет о себе в clientInfo.version. Пусто —
	// версия модуля из сборки или «dev»: строку, вшитую в main ключом
	// -ldflags, пакет из internal увидеть не может, и cmd/ollchat вправе
	// передать её сюда сам.
	Version string
}

// Client — соединение с одним сервером MCP. Безопасен для одновременных
// вызовов: рукопожатие одно на всех, номер сеанса общий.
type Client struct {
	name    string
	url     string
	where   string // адрес без строки запроса и учётных данных — для сообщений
	token   string
	version string
	timeout time.Duration
	http    *http.Client

	ids   atomic.Int64
	stale atomic.Bool

	// initMu держит рукопожатие одним: десять вызовов, пришедших до него,
	// не должны поздороваться десять раз и разойтись по десяти сеансам.
	initMu sync.Mutex

	mu      sync.Mutex
	session string
	proto   string
	info    *Info
}

// Info — что сервер сказал о себе в рукопожатии.
type Info struct {
	Protocol      string // договорённая ревизия
	ServerName    string // serverInfo.name
	ServerVersion string // serverInfo.version
	Instructions  string // как сервер просит пользоваться собой; текст чужой
	// ListChanged — сервер обещал уведомлять о смене набора инструментов
	// (capabilities.tools.listChanged).
	ListChanged bool
	At          time.Time     // когда рукопожатие закончилось
	Took        time.Duration // сколько оно шло, вместе с notifications/initialized
}

// New заводит клиента. Сеть не трогает: рукопожатие — при первом вызове или
// явным Initialize, чтобы запуск TUI не ждал чужих машин (Б5).
func New(o Opts) (*Client, error) {
	name := strings.TrimSpace(o.Server)
	raw := strings.TrimSpace(o.URL)
	label := name
	if label == "" {
		label = "MCP"
	}
	u, err := url.Parse(raw)
	if err != nil {
		// Сам адрес в сообщение не идёт: в строке запроса бывает ключ.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("сервер %s: адрес не разобрался: %v", label, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("сервер %s: адрес должен начинаться с http:// или https://, "+
			"например https://mcp.example/mcp (другие транспорты не поддержаны)", label)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("сервер %s: в адресе нет имени машины", label)
	}
	if name == "" {
		name = u.Hostname()
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	version := strings.TrimSpace(o.Version)
	if version == "" {
		version = buildVersion()
	}
	return &Client{
		name:    name,
		url:     raw,
		where:   u.Scheme + "://" + u.Host + u.EscapedPath(),
		token:   strings.TrimSpace(o.Token),
		version: version,
		timeout: timeout,
		http: &http.Client{
			// Перенаправления не выполняются. Вход MCP не переезжает молча,
			// а повторить POST по чужому адресу значит отдать туда тело
			// вызова — и, при совпадении домена, ключ. Лучше сказать человеку
			// новый адрес, пусть поправит конфиг сам.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// buildVersion — версия для clientInfo: версия выпуска, если сборка её знает,
// иначе «dev». Берётся первым словом строки buildinfo, чтобы правило «что
// считать выпуском» не завести второй раз.
func buildVersion() string {
	if f := strings.Fields(buildinfo.Describe("")); len(f) > 0 {
		return f[0]
	}
	return buildinfo.Unknown
}

// Server — имя сервера, как его называют сообщения.
func (c *Client) Server() string { return c.name }

// Info — итог последнего рукопожатия; false — его ещё не было или сеанс
// потерян и не восстановлен.
func (c *Client) Info() (Info, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.info == nil {
		return Info{}, false
	}
	return *c.info, true
}

// Stale — список инструментов устарел: сервер прислал
// notifications/tools/list_changed или сеанс пришлось начать заново.
// Вызывающий перечитывает список; ListTools снимает признак.
func (c *Client) Stale() bool { return c.stale.Load() }

// state — то, что уходит заголовками: номер сеанса и договорённая ревизия.
type state struct {
	session string
	proto   string
}

// current — заголовки текущего сеанса и было ли рукопожатие.
func (c *Client) current() (state, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return state{session: c.session, proto: c.proto}, c.info != nil
}

// Initialize здоровается с сервером заново: новый сеанс, свежие сведения.
//
// Явный вызов нужен проверке связи (`--mcp-ping`) и переподключению из `/mcp`;
// обычным вызовам он не обязателен — ListTools и CallTool здороваются сами.
func (c *Client) Initialize(ctx context.Context) (Info, error) {
	c.initMu.Lock()
	defer c.initMu.Unlock()
	return c.handshake(ctx)
}

// ready — заголовки готового сеанса; рукопожатие, если его ещё не было.
func (c *Client) ready(ctx context.Context) (state, error) {
	if st, ok := c.current(); ok {
		return st, nil
	}
	c.initMu.Lock()
	defer c.initMu.Unlock()
	if st, ok := c.current(); ok {
		return st, nil // пока ждали замка, поздоровался другой вызов
	}
	if _, err := c.handshake(ctx); err != nil {
		return state{}, err
	}
	st, _ := c.current()
	return st, nil
}

// renew начинает сеанс заново после 404 на сеанс old.
//
// Если, пока ждали замка, сеанс уже сменил другой вызов, второго рукопожатия
// нет: сервер, потерявший сеанс, получил бы лишний.
func (c *Client) renew(ctx context.Context, old state) (state, error) {
	c.initMu.Lock()
	defer c.initMu.Unlock()
	if st, ok := c.current(); ok && st.session != old.session {
		return st, nil
	}
	if _, err := c.handshake(ctx); err != nil {
		return state{}, err
	}
	// Сервер, потерявший сеанс, скорее всего перезапущен, и набор его
	// инструментов мог смениться.
	c.stale.Store(true)
	st, _ := c.current()
	return st, nil
}

// initResult — ответ на initialize, только то, что клиенту нужно.
type initResult struct {
	ProtocolVersion string `json:"protocolVersion"`
	Capabilities    struct {
		Tools *struct {
			ListChanged bool `json:"listChanged"`
		} `json:"tools"`
	} `json:"capabilities"`
	ServerInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
	Instructions string `json:"instructions"`
}

// handshake — initialize и notifications/initialized. Вызывается под initMu.
func (c *Client) handshake(ctx context.Context) (Info, error) {
	started := time.Now()
	c.mu.Lock()
	// Новый сеанс начинается без номера прежнего: так велит спецификация
	// после 404, и так же честнее при явном переподключении.
	c.session, c.proto, c.info = "", "", nil
	c.mu.Unlock()

	ask := Revision
	var (
		res     initResult
		session string
	)
	for try := 0; ; try++ {
		params := map[string]any{
			"protocolVersion": ask,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "ollchat", "version": c.version},
		}
		raw, sess, err := c.roundTrip(ctx, state{}, "initialize", params, false)
		if err == nil {
			if err := json.Unmarshal(raw, &res); err != nil {
				return Info{}, fmt.Errorf("сервер %s: ответ на initialize не разобрался: %v", c.name, err)
			}
			session = sess
			break
		}
		var re *RPCError
		if errors.As(err, &re) {
			// Сервер старой ревизии, вместо того чтобы назвать свою (как велит
			// спецификация), отказал и перечислил, что умеет: пример такого
			// отказа есть в самой спецификации (lifecycle, код -32602). Одна
			// попытка с общей ревизией, не больше.
			if alt := commonRevision(re); alt != "" && alt != ask && try == 0 {
				ask = alt
				continue
			}
			if rev := c.revisionRefusal(re); rev != nil {
				return Info{}, rev
			}
		}
		var he *httpError
		if errors.As(err, &he) && (he.status == http.StatusMethodNotAllowed || he.status == http.StatusNotFound) {
			return Info{}, fmt.Errorf("%w — по этому адресу нет входа Streamable HTTP. "+
				"Если сервер на старом транспорте HTTP+SSE (ревизия 2024-11-05, адрес вида …/sse), "+
				"ollchat его не поддерживает; вход Streamable HTTP обычно оканчивается на /mcp", err)
		}
		return Info{}, err
	}

	if !knownRevision(res.ProtocolVersion) {
		return Info{}, &tagged{tag: ErrRevision, msg: fmt.Sprintf(
			"сервер %s говорит на ревизии MCP %q, ollchat понимает %s",
			c.name, res.ProtocolVersion, strings.Join(revisions, ", "))}
	}
	st := state{session: session, proto: res.ProtocolVersion}
	if _, _, err := c.roundTrip(ctx, st, "notifications/initialized", nil, true); err != nil {
		return Info{}, err
	}
	now := time.Now()
	info := &Info{
		Protocol:      res.ProtocolVersion,
		ServerName:    res.ServerInfo.Name,
		ServerVersion: res.ServerInfo.Version,
		Instructions:  res.Instructions,
		ListChanged:   res.Capabilities.Tools != nil && res.Capabilities.Tools.ListChanged,
		At:            now,
		Took:          now.Sub(started),
	}
	c.mu.Lock()
	c.session, c.proto, c.info = st.session, st.proto, info
	c.mu.Unlock()
	return *info, nil
}

// supportedOf — список ревизий из data.supported отказа, если он там есть.
func supportedOf(e *RPCError) []string {
	var d struct {
		Supported []string `json:"supported"`
	}
	if len(e.Data) == 0 || json.Unmarshal(e.Data, &d) != nil {
		return nil
	}
	return d.Supported
}

// commonRevision — новейшая ревизия из отказа, которую понимает и клиент.
func commonRevision(e *RPCError) string {
	sup := supportedOf(e)
	for _, r := range revisions {
		for _, s := range sup {
			if s == r {
				return r
			}
		}
	}
	return ""
}

// Как узнаётся сервер ревизии 2026-07-28 и новее.
//
// Проверено 22.10.2026 по спецификации через privatescripts/websearch.py:
// changelog 2026-07-28 (перенумерация: HeaderMismatch -32001 → -32020,
// MissingRequiredClientCapability -32003 → -32021, UnsupportedProtocolVersion
// -32004 → -32022), basic/versioning («Legacy client — Modern server: … HTTP:
// the request is missing the required headers and is rejected … with 400 Bad
// Request»; сервер SHOULD назвать свои ревизии в отказе на initialize)
// и basic/transports/streamable-http (400 с -32020 при нехватке заголовков
// MCP-Protocol-Version, Mcp-Method, Mcp-Name; 404 с -32601 на незнакомый метод).
// Старые номера черновика (-32004) тоже узнаются: серверы, собранные по нему,
// ещё живут. -32001 и -32003 не узнаются: диапазон -32000…-32019 спецификация
// оставила реализациям, и -32001 в SDK — это «срок истёк».
var modernCodes = map[int]bool{-32020: true, -32021: true, -32022: true, -32004: true}

// revisionRefusal — объяснение отказа в рукопожатии, если он про ревизию;
// nil — отказ про другое.
func (c *Client) revisionRefusal(e *RPCError) error {
	sup := supportedOf(e)
	modern := modernCodes[e.Code] || (e.status == http.StatusNotFound && e.Code == -32601)
	for _, s := range sup {
		if s > revisions[0] { // ревизии — даты, и строки сравниваются как даты
			modern = true
		}
	}
	ours := strings.Join(revisions, ", ")
	if modern {
		named := "2026-07-28 или новее"
		if len(sup) > 0 {
			named = "сервер называет: " + strings.Join(sup, ", ")
		}
		return &tagged{tag: ErrRevision, msg: fmt.Sprintf(
			"сервер %s говорит на ревизии MCP без рукопожатия (%s); ревизия 2026-07-28 "+
				"ollchat пока не поддерживается — он понимает %s (отказ сервера: %d %s)",
			c.name, named, ours, e.Code, clip(e.Message, 200))}
	}
	if len(sup) > 0 {
		return &tagged{tag: ErrRevision, msg: fmt.Sprintf(
			"сервер %s поддерживает ревизии MCP %s, ollchat понимает %s",
			c.name, strings.Join(sup, ", "), ours)}
	}
	return nil
}

// request — вызов метода в готовом сеансе.
//
// 404 на запрос с номером сеанса — сеанс истёк: спецификация велит начать
// новый, и запрос повторяется один раз. Повтор здесь безопасен: сервер
// отказал, не исполнив, — сеанса, в котором исполнять, у него нет.
func (c *Client) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	st, err := c.ready(ctx)
	if err != nil {
		return nil, err
	}
	res, _, err := c.roundTrip(ctx, st, method, params, false)
	if !errors.Is(err, errSessionGone) {
		return res, err
	}
	if st, err = c.renew(ctx, st); err != nil {
		return nil, err
	}
	res, _, err = c.roundTrip(ctx, st, method, params, false)
	if errors.Is(err, errSessionGone) {
		return nil, fmt.Errorf("сервер %s: сеанс не найден и сразу после нового рукопожатия (404)", c.name)
	}
	return res, err
}

// Tool — инструмент сервера, как он его описал.
//
// Схема и пометки — КАК ЕСТЬ, без разбора в типы: у чужих серверов в схеме
// бывают items, вложенные объекты, anyOf и type списком, а `ollama.ToolProp`
// знает только type, description и enum. Решать, как подать схему модели,
// будет реестр (Б3), а не клиент.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
}

// maxPages — предел страниц списка. Сервер, у которого курсор не кончается,
// иначе держал бы вызов до срока на каждой странице.
const maxPages = 100

// ListTools читает весь список инструментов, со всеми страницами.
//
// Признак Stale снимается в начале: уведомление, пришедшее, пока список
// читается, выставит его снова, и вызывающий перечитает. У ollmcp после
// перезапуска уведомление приходит потоком перед самим ответом — одно лишнее
// перечитывание на перезапуск службы, зато без промаха.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	c.stale.Store(false)
	var out []Tool
	cursor := ""
	seen := map[string]bool{}
	for page := 0; ; page++ {
		if page >= maxPages {
			return nil, fmt.Errorf("сервер %s: список инструментов длиннее %d страниц", c.name, maxPages)
		}
		var params any
		if cursor != "" {
			params = map[string]any{"cursor": cursor}
		}
		raw, err := c.request(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var r struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("сервер %s: список инструментов не разобрался: %v", c.name, err)
		}
		for _, t := range r.Tools {
			if strings.TrimSpace(t.Name) == "" {
				continue // без имени инструмент не вызвать
			}
			if len(t.InputSchema) == 0 || string(t.InputSchema) == "null" {
				// Схема обязательна по спецификации, но без неё вызов без
				// доводов законен — модели отдаётся пустой объект, а не null.
				t.InputSchema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			out = append(out, t)
		}
		if r.NextCursor == "" {
			return out, nil
		}
		if seen[r.NextCursor] {
			return nil, fmt.Errorf("сервер %s: курсор списка инструментов пошёл по кругу", c.name)
		}
		seen[r.NextCursor] = true
		cursor = r.NextCursor
	}
}

// Result — ответ инструмента.
type Result struct {
	// Text — текстовые части через перевод строки; текст вложенных ресурсов;
	// пометки о том, что не показано (звук, двоичный ресурс). Пусто, а сервер
	// прислал structuredContent, — его JSON.
	Text string
	// IsError — ошибка самого инструмента (isError): модели она показывается
	// как ответ, а не обрывает обмен.
	IsError bool
	// Images — картинки ответа, для моделей со зрением.
	Images []Image
}

// Image — картинка из ответа инструмента.
type Image struct {
	MIME string // mimeType, например image/png
	Data string // base64, как прислал сервер (проверено, что раскодируется)
}

// contentPart — часть content в ответе tools/call.
type contentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
	URI      string `json:"uri"`
	Name     string `json:"name"`
	Resource *struct {
		URI      string  `json:"uri"`
		MimeType string  `json:"mimeType"`
		Text     *string `json:"text"`
		Blob     string  `json:"blob"`
	} `json:"resource"`
}

// CallTool вызывает инструмент.
//
// Ошибка JSON-RPC (нет такого инструмента, неверные доводы) — error;
// ошибка самого инструмента — Result с IsError, как велит MCP.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (Result, error) {
	if strings.TrimSpace(name) == "" {
		return Result{}, fmt.Errorf("сервер %s: не указано имя инструмента", c.name)
	}
	if args == nil {
		args = map[string]any{} // часть серверов требует объект, а не его отсутствие
	}
	raw, err := c.request(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return Result{}, err
	}
	var r struct {
		Content           []contentPart   `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return Result{}, fmt.Errorf("сервер %s: ответ %s не разобрался: %v", c.name, name, err)
	}
	return assemble(r.Content, r.StructuredContent, r.IsError), nil
}

// assemble собирает Result из частей ответа.
//
// Ничего не выбрасывается молча: часть, которую модель не увидит (звук,
// двоичный ресурс, незнакомый тип), оставляет в тексте пометку — иначе модель
// решит, что инструмент вернул меньше, чем вернул.
func assemble(parts []contentPart, structured json.RawMessage, isError bool) Result {
	res := Result{IsError: isError}
	var texts []string
	for _, p := range parts {
		switch p.Type {
		case "text":
			texts = append(texts, p.Text)
		case "image":
			if _, err := base64.StdEncoding.DecodeString(p.Data); err != nil || p.Data == "" {
				texts = append(texts, fmt.Sprintf("[картинка %s пропущена: данные не в base64]", p.MimeType))
				continue
			}
			res.Images = append(res.Images, Image{MIME: p.MimeType, Data: p.Data})
		case "resource":
			switch {
			case p.Resource == nil:
				texts = append(texts, "[ресурс без содержимого]")
			case p.Resource.Text != nil:
				texts = append(texts, *p.Resource.Text)
			default:
				texts = append(texts, fmt.Sprintf("[ресурс %s (%s): двоичные данные не показаны]",
					p.Resource.URI, p.Resource.MimeType))
			}
		case "resource_link":
			texts = append(texts, fmt.Sprintf("[ссылка на ресурс: %s %s]", p.Name, p.URI))
		case "audio":
			texts = append(texts, fmt.Sprintf("[звук %s не показан]", p.MimeType))
		default:
			texts = append(texts, fmt.Sprintf("[часть типа %q не показана]", p.Type))
		}
	}
	res.Text = strings.Join(texts, "\n")
	if strings.TrimSpace(res.Text) == "" {
		if s := strings.TrimSpace(string(structured)); s != "" && s != "null" {
			res.Text = s
		}
	}
	return res
}

// Close заканчивает сеанс: DELETE с его номером, как просит спецификация
// клиента, которому сеанс больше не нужен. 405 — сервер так не умеет, это
// не ошибка. Клиентом после Close можно пользоваться: следующий вызов
// поздоровается заново.
func (c *Client) Close(ctx context.Context) error {
	c.initMu.Lock()
	defer c.initMu.Unlock()
	c.mu.Lock()
	st := state{session: c.session, proto: c.proto}
	c.session, c.proto, c.info = "", "", nil
	c.mu.Unlock()
	if st.session == "" {
		return nil
	}
	return c.deleteSession(ctx, st)
}
