package mcpclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

// Транспорт Streamable HTTP: одно сообщение JSON-RPC — один POST, ответ —
// телом JSON или потоком SSE, который может нести уведомления перед ответом.
//
// Типы JSON-RPC свои: серверные в `internal/mcp` не экспортированы, и связывать
// клиента с сервером общим пакетом незачем — чужие серверы живут по
// спецификации, а не по нашему коду.

// maxBody — предел ответа, как у kbremote: выдача поиска по книгам бывает
// большой, но не больше этого. Поток SSE считается целиком, со всеми
// переподключениями.
const maxBody = 32 << 20

// Пределы всего списка инструментов, по всем страницам tools/list вместе.
// maxBody стоит на один ответ, а страниц до maxPages: без общего предела
// чужой сервер держал бы в памяти клиента до maxPages×maxBody (3,2 ГБ)
// и сотни тысяч инструментов (аудит 23.10.2026, находка 5).
const (
	// maxListBytes — байт result всех страниц вместе: столько же, сколько
	// разрешено одному ответу.
	maxListBytes = maxBody
	// maxListTools — инструментов в списке, с запасом: модели всё равно
	// уходит только то, что влезло в предел схем реестра (mcphub).
	maxListTools = 1000
)

// Паузы — переменные, а не постоянные, только ради тестов: ждать секунду
// в каждом из них незачем.
var (
	// retryPause — пауза перед повтором, когда в соединении отказано.
	retryPause = time.Second
	// resumePause — пауза перед переподключением к потоку, если сервер не
	// назвал свою (поле retry).
	resumePause = time.Second
)

// maxResume — сколько раз переподключаться к потоку, который сервер закрыл до
// ответа. Спецификация разрешает серверу так «опрашиваться»; бесконечно — нет.
const maxResume = 5

// rpcRequest — исходящий запрос или уведомление (ID nil).
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcMessage — входящее сообщение любого вида: ответ (id и result или error),
// уведомление (method без id) или запрос сервера (method и id).
type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *RPCError       `json:"error"`
}

// rpcReply — наш ответ на запрос сервера.
type rpcReply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError — ошибка JSON-RPC, присланная сервером.
type RPCError struct {
	Server  string          `json:"-"`
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`

	status int // статус HTTP, с которым она пришла: 400 и 404 значат для рукопожатия своё
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("сервер %s: ошибка %d: %s", e.Server, e.Code, clip(e.Message, 500))
}

// tagged — ошибка со своим текстом, узнаваемая через errors.Is по метке
// (ErrUnavailable, ErrUnauthorized, ErrRevision) и по причине, если она есть.
type tagged struct {
	msg string
	tag error
	err error
}

func (e *tagged) Error() string { return e.msg }

func (e *tagged) Unwrap() []error {
	if e.err == nil {
		return []error{e.tag}
	}
	return []error{e.tag, e.err}
}

// errSessionGone — 404 на запрос с номером сеанса. Наружу не выходит:
// request начинает сеанс заново, handshake заменяет своей ошибкой.
var errSessionGone = errors.New("сеанс MCP истёк")

// httpError — не-2xx без ошибки JSON-RPC в теле.
type httpError struct {
	msg    string
	status int
	moved  string // новый адрес из Location (3xx) — для MovedTo, не для текста
}

func (e *httpError) Error() string { return e.msg }

// MovedTo — адрес, на который сервер перенаправлял, если err — такой отказ;
// пусто — нет. В текст ошибки адрес не входит: текст уходит модели, а адрес
// нужен человеку, чтобы поправить настройки (решение владельца 23.10.2026:
// адрес показывают /mcp и ключи --mcp-*, а не ошибки). Без строки запроса
// и учётных данных.
func MovedTo(err error) string {
	var he *httpError
	if errors.As(err, &he) {
		return he.moved
	}
	return ""
}

// roundTrip — одно сообщение и, для запроса, ответ на него. Возвращает
// result ответа и номер сеанса из заголовков (его выдаёт initialize).
//
// Срок — на весь обмен, с повтором и чтением потока: «сервер думает» и «сервер
// молчит» модели неотличимы, ждать дольше срока она не должна ни в том, ни
// в другом случае.
func (c *Client) roundTrip(ctx context.Context, st state, method string, params any, notify bool) (json.RawMessage, string, error) {
	msg := rpcRequest{JSONRPC: "2.0", Method: method, Params: params}
	var id int64
	if !notify {
		id = c.ids.Add(1)
		msg.ID = &id
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, "", fmt.Errorf("сервер %s: запрос %s не собрался: %v", c.name, method, err)
	}
	tctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	resp, err := c.postRetry(tctx, body, st)
	if err != nil {
		return nil, "", c.transportError(ctx, tctx, err)
	}
	defer resp.Body.Close()
	session := resp.Header.Get("Mcp-Session-Id")
	res, err := c.readReply(tctx, resp, id, notify, st)
	if err != nil && tctx.Err() != nil {
		// Срок вышел или вызов снят посреди чтения ответа: сказать об этом,
		// а не о «потоке, оборвавшемся без ответа».
		return nil, session, c.transportError(ctx, tctx, err)
	}
	return res, session, err
}

// postRetry шлёт POST; при отказе в соединении — ещё раз через секунду.
//
// Повтор только тут, и это не осторожность ради осторожности: отказ в
// соединении значит, что ни один байт запроса не ушёл, и сервер ничего не
// исполнил. Обрыв после отправки — другое дело: инструмент мог уже сработать
// (записать, отправить письмо), и второй вызов сделал бы это дважды
// (правило `ollchat-retry-policy`: повтор безопасен, пока ничего не выполнено).
func (c *Client) postRetry(ctx context.Context, body []byte, st state) (*http.Response, error) {
	resp, err := c.post(ctx, body, st)
	if err == nil || !refused(err) {
		return resp, err
	}
	t := time.NewTimer(retryPause)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.C:
	}
	return c.post(ctx, body, st)
}

// refused — в соединении отказано (никто не слушает порт).
//
// Только ECONNREFUSED. На Windows код другой (WSAECONNREFUSED), и там повтора
// не будет — это потеря удобства, а не повтор лишнего.
func refused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}

func (c *Client) post(ctx context.Context, body []byte, st state) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	// Без application/json ollmcp отказывает 415 ещё до ключа (kbserve.Protect),
	// а Accept с обоими типами спецификация требует от клиента прямо.
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	c.setHeaders(req, st)
	return c.http.Do(req)
}

// setHeaders — ключ, сеанс, ревизия. Origin не ставится никогда: его шлёт
// браузер, и служба с чужим Origin отказывает (403).
func (c *Client) setHeaders(req *http.Request, st state) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if st.session != "" {
		req.Header.Set("Mcp-Session-Id", st.session)
	}
	if st.proto != "" {
		req.Header.Set("MCP-Protocol-Version", st.proto)
	}
}

// transportError — ответа нет: срок, отмена или сбой соединения.
//
// Метка ErrUnavailable — та же, что у kbremote (transport): сервер не дал
// ответа — отказ в соединении, обрыв или истёкший срок, свой или вызывающего.
// Без метки — только отмена вызывающим (человек нажал Esc): искать у себя
// то, что человек сам бросил, незачем. Прежде без метки оставался и срок
// вызывающего, и search через MCP при нём не переходил на свои файлы, а
// kb_search и graph_* переходили (рецензия этапа 121).
func (c *Client) transportError(parent, tctx context.Context, err error) error {
	switch {
	case errors.Is(parent.Err(), context.Canceled):
		return fmt.Errorf("сервер %s: вызов прерван: %w", c.name, parent.Err())
	case parent.Err() != nil:
		return &tagged{tag: ErrUnavailable, err: parent.Err(),
			msg: fmt.Sprintf("сервер %s не ответил до срока вызова", c.name)}
	case errors.Is(tctx.Err(), context.DeadlineExceeded):
		return &tagged{tag: ErrUnavailable, err: context.DeadlineExceeded,
			msg: fmt.Sprintf("сервер %s не ответил за %s", c.name, c.timeout)}
	}
	return &tagged{tag: ErrUnavailable, err: err,
		msg: fmt.Sprintf("сервер %s недоступен: %s", c.name, reason(err))}
}

// reason — причина сбоя соединения или чтения словами, без адреса.
//
// Текст ошибки уходит модели выводом инструмента, в ленту и в журнал шагов,
// а адрес там не нужен (решение владельца 23.10.2026; аудит 23.10, находки
// 7 и 8, тот же класс, что находка 9 аудита 16.10): сервер называется именем
// из конфига, адрес показывают /mcp и ключи --mcp-*. Ошибки net/http несут
// адрес сами: *url.Error — весь адрес со строкой запроса, *net.OpError —
// машину, порт и IP, который вернул DNS, *net.DNSError — имя машины,
// *net.AddrError — адрес, x509.HostnameError — имена из сертификата. Знакомые причины — словами;
// прочее — текстом самой внутренней ошибки, с которой сняты эти обёртки.
//
// Причина цепочки не теряется: вызывающий кладёт err в tagged, и errors.Is
// (ECONNREFUSED, DeadlineExceeded) по-прежнему работает.
func reason(err error) string {
	var (
		dns  *net.DNSError
		addr *net.AddrError
		name x509.HostnameError
		cert *tls.CertificateVerificationError
		auth x509.UnknownAuthorityError
		bad  x509.CertificateInvalidError
		ne   net.Error
	)
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "отказ в соединении"
	case errors.Is(err, syscall.ECONNRESET):
		return "соединение сброшено"
	case errors.Is(err, syscall.EHOSTUNREACH):
		return "машина недоступна"
	case errors.Is(err, syscall.ENETUNREACH):
		return "сеть недоступна"
	case errors.As(err, &dns):
		if dns.IsNotFound {
			return "имя машины не найдено в DNS"
		}
		return "DNS не ответил"
	case errors.As(err, &addr):
		return "адрес машины не разобрался"
	case errors.As(err, &name), errors.As(err, &cert), errors.As(err, &auth), errors.As(err, &bad):
		return "сертификат сервера не принят"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded),
		errors.As(err, &ne) && ne.Timeout():
		return "срок истёк"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "соединение оборвалось"
	}
	for {
		var (
			ue *url.Error
			oe *net.OpError
		)
		switch {
		case errors.As(err, &ue) && ue.Err != nil:
			err = ue.Err
		case errors.As(err, &oe) && oe.Err != nil:
			err = oe.Err
		default:
			return serverText(err.Error(), 200)
		}
	}
}

// readReply разбирает ответ HTTP: статус, тип тела, само тело.
func (c *Client) readReply(ctx context.Context, resp *http.Response, id int64, notify bool, st state) (json.RawMessage, error) {
	status := resp.StatusCode
	switch {
	case status == http.StatusUnauthorized:
		hint := " (token_file, token_env или token_cmd в настройках сервера)"
		if c.token != "" {
			hint = ": переданный ключ не принят"
		}
		return nil, &tagged{tag: ErrUnauthorized, msg: fmt.Sprintf("сервер %s: нужен ключ доступа%s", c.name, hint)}
	case status == http.StatusNotFound && st.session != "":
		return nil, c.notFound(resp, id, notify)
	case status >= 300 && status < 400:
		he := &httpError{status: status}
		if loc, err := url.Parse(resp.Header.Get("Location")); err == nil && loc.Host != "" {
			he.moved = loc.Scheme + "://" + loc.Host + loc.EscapedPath()
		}
		he.msg = fmt.Sprintf("сервер %s ответил %s — перенаправлений клиент не выполняет, поправьте адрес в настройках",
			c.name, c.status(resp))
		return nil, he
	case status < 200 || status >= 300:
		return nil, c.statusError(resp)
	}
	if notify {
		return nil, nil // 202 без тела — как и положено; тело, если есть, не нужно
	}
	if status == http.StatusAccepted || status == http.StatusNoContent {
		return nil, fmt.Errorf("сервер %s ответил %d без тела, а ждали ответ на запрос", c.name, status)
	}
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch mt {
	case "text/event-stream":
		return c.readStream(ctx, resp.Body, id, st)
	case "application/json", "":
		body, err := io.ReadAll(&capReader{r: resp.Body, left: maxBody + 1})
		if err != nil {
			return nil, c.bodyError(err)
		}
		return c.pickJSON(ctx, body, id, st)
	default:
		return nil, fmt.Errorf("сервер %s прислал ответ типа %q, а ждали application/json или text/event-stream",
			c.name, serverText(mt, 100))
	}
}

// statusError — не-2xx: ошибка JSON-RPC из тела, если она там есть, иначе
// статус и начало тела.
func (c *Client) statusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var m rpcMessage
	if json.Unmarshal(bytes.TrimSpace(body), &m) == nil && m.Error != nil {
		m.Error.Server = c.name
		m.Error.status = resp.StatusCode
		m.Error.Message = serverText(c.redact(m.Error.Message), 500)
		return m.Error
	}
	text := serverText(c.redact(string(body)), 200)
	msg := fmt.Sprintf("сервер %s ответил %s", c.name, c.status(resp))
	if text != "" {
		msg += ": " + text
	}
	return &httpError{status: resp.StatusCode, msg: msg}
}

// notFound — 404 на запрос с номером сеанса.
//
// Спецификация велит серверу отвечать 404 на номер завершённого сеанса,
// и тогда клиент здоровается заново и повторяет запрос: сервер его не
// исполнял. Но 404 бывает и ответом на сам запрос — ошибкой JSON-RPC с его
// id («нет такого инструмента»): сервер запрос прочитал, сеанс у него есть,
// и повтор отправил бы tools/call второй раз (аудит 23.10.2026). Такая
// ошибка отдаётся как есть. Пустое тело (go-sdk v1.6.0 отвечает текстом
// «session not found»), не JSON-RPC, отказ с id null или чужим — сервер
// запроса не прочёл: сеанс истёк. Цена выбора: сервер, который на потерянный
// сеанс отвечает ошибкой с id нашего запроса, нового рукопожатия не получит,
// и человек увидит его ошибку, а не молчаливый повтор.
func (c *Client) notFound(resp *http.Response, id int64, notify bool) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var m rpcMessage
	if notify || json.Unmarshal(bytes.TrimSpace(body), &m) != nil || m.Error == nil ||
		strings.TrimSpace(string(m.ID)) != strconv.FormatInt(id, 10) {
		return errSessionGone
	}
	m.Error.Server = c.name
	m.Error.status = resp.StatusCode
	m.Error.Message = serverText(c.redact(m.Error.Message), 500)
	return m.Error
}

// status — строка статуса ответа: код и причина, которую назвал сервер,
// без управляющих знаков и не длиннее сотни знаков.
func (c *Client) status(resp *http.Response) string {
	return serverText(c.redact(resp.Status), 100)
}

// redact убирает ключ из текста сервера. Чужой сервер вправе повторить
// в ответе заголовки запроса, а в сообщение об ошибке ключ попасть не должен.
func (c *Client) redact(s string) string {
	if c.token == "" {
		return s
	}
	return strings.ReplaceAll(s, c.token, "***")
}

// serverText — чужая строка для текста ошибки: одной строкой, без
// управляющих знаков (ESC и OSC перекрасили бы ленту или заголовок окна)
// и невидимых знаков формата, не длиннее n знаков. Ошибка уходит модели,
// в ленту и в журнал шагов, а сервер вправе прислать что угодно и сколько
// угодно (аудит 23.10.2026: protocolVersion и data.supported шли как есть).
func serverText(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.Is(unicode.Cc, r):
			return ' '
		case unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, s)
	return clip(strings.Join(strings.Fields(s), " "), n)
}

// bodyError — тело ответа не дочиталось.
//
// Обрыв посреди тела — тоже «ответа нет», с меткой ErrUnavailable, как
// в kbremote. Срок и отмену сюда не приносят: их roundTrip отдаёт
// transportError, пока контекст обмена закрыт. Слишком большой ответ —
// отказ по смыслу, без метки: сервер ответил, но не тем.
func (c *Client) bodyError(err error) error {
	// ErrTooLong — строка потока SSE длиннее буфера разборщика, а он равен
	// тому же пределу: по сути та же причина.
	if errors.Is(err, errTooBig) || errors.Is(err, bufio.ErrTooLong) {
		return fmt.Errorf("сервер %s: ответ больше %d МиБ", c.name, maxBody>>20)
	}
	return &tagged{tag: ErrUnavailable, err: err,
		msg: fmt.Sprintf("сервер %s: ответ не дочитался: %s", c.name, reason(err))}
}

// pickJSON — ответ с нашим id из тела JSON (одно сообщение или массив).
func (c *Client) pickJSON(ctx context.Context, body []byte, id int64, st state) (json.RawMessage, error) {
	res, done, err := c.take(ctx, body, id, st)
	if done {
		return res, err
	}
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("сервер %s: в ответе нет ответа на запрос %d", c.name, id)
}

// take разбирает одно сообщение (или массив сообщений) и, если это ответ на
// наш запрос, возвращает его; done — ответ найден.
func (c *Client) take(ctx context.Context, raw []byte, id int64, st state) (json.RawMessage, bool, error) {
	raw = bytes.TrimSpace(raw)
	var msgs []rpcMessage
	if len(raw) > 0 && raw[0] == '[' {
		if err := json.Unmarshal(raw, &msgs); err != nil {
			return nil, false, fmt.Errorf("сервер %s прислал не JSON-RPC: %v", c.name, err)
		}
	} else {
		var m rpcMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, false, fmt.Errorf("сервер %s прислал не JSON-RPC: %v", c.name, err)
		}
		msgs = []rpcMessage{m}
	}
	want := strconv.FormatInt(id, 10)
	for _, m := range msgs {
		gotID := strings.TrimSpace(string(m.ID))
		switch {
		case m.Method != "" && (gotID == "" || gotID == "null"):
			c.notice(m.Method)
		case m.Method != "":
			c.answer(ctx, st, m)
		case gotID == want, gotID == "null" && m.Error != nil:
			// id null бывает у отказа «запрос не разобран»: сервер не смог
			// прочитать id, но отказ — наш, другого запроса в этом обмене нет.
			if m.Error != nil {
				m.Error.Server = c.name
				m.Error.Message = serverText(c.redact(m.Error.Message), 500)
				return nil, true, m.Error
			}
			if len(m.Result) == 0 {
				return nil, true, fmt.Errorf("сервер %s: ответ без result и без error", c.name)
			}
			return m.Result, true, nil
		}
	}
	return nil, false, nil
}

// notice — уведомление сервера. Нужно одно: «набор инструментов сменился».
// Прочие (прогресс, журнал сервера) клиенту ни к чему.
func (c *Client) notice(method string) {
	if method == "notifications/tools/list_changed" {
		c.stale.Store(true)
	}
}

// answer отвечает на запрос сервера, пришедший в потоке.
//
// Возможностей клиент не объявляет (capabilities пустые), так что серверу
// законно спрашивать только ping, а на ping отвечать обязательно. На прочее —
// «нет такого метода», чтобы сервер не ждал ответа до своего срока.
// Ответ идёт отдельным POST; его исход на наш вызов не влияет.
func (c *Client) answer(ctx context.Context, st state, m rpcMessage) {
	reply := rpcReply{JSONRPC: "2.0", ID: m.ID}
	if m.Method == "ping" {
		reply.Result = map[string]any{}
	} else {
		reply.Error = &RPCError{Code: -32601, Message: "ollchat не поддерживает запрос сервера " + m.Method}
	}
	body, err := json.Marshal(reply)
	if err != nil {
		return
	}
	if resp, err := c.post(ctx, body, st); err == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
	}
}

// readStream читает поток SSE до ответа на наш запрос.
//
// Сервер 2025-11-25 вправе закрыть поток до ответа, назвав номер последнего
// события (id): клиент переподключается GET с Last-Event-ID и читает дальше.
// Это не повтор запроса — запрос не шлётся заново, читается его же поток.
func (c *Client) readStream(ctx context.Context, body io.ReadCloser, id int64, st state) (json.RawMessage, error) {
	budget := &capReader{r: body, left: maxBody + 1}
	sse := newSSE(budget)
	var resumed io.Closer
	defer func() {
		if resumed != nil {
			resumed.Close()
		}
	}()
	for reconnects := 0; ; {
		ev, err := sse.next()
		if err == io.EOF {
			if sse.lastID == "" || reconnects >= maxResume {
				return nil, fmt.Errorf("сервер %s: поток SSE закрылся без ответа на запрос", c.name)
			}
			reconnects++
			pause := resumePause
			if sse.retry > 0 {
				pause = sse.retry
			}
			if err := sleep(ctx, pause); err != nil {
				return nil, err
			}
			next, err := c.resume(ctx, st, sse.lastID)
			if err != nil {
				return nil, err
			}
			if resumed != nil {
				resumed.Close()
			}
			resumed = next
			budget.r = next
			sse.reset(budget)
			continue
		}
		if err != nil {
			return nil, c.bodyError(err)
		}
		if strings.TrimSpace(ev.data) == "" {
			continue // «затравка» с одним id: по ней переподключаются, разбирать нечего
		}
		res, done, err := c.take(ctx, []byte(ev.data), id, st)
		if done || err != nil {
			return res, err
		}
	}
}

// resume — GET с Last-Event-ID: продолжение потока, закрытого сервером.
//
// Не вышло соединиться — «ответа нет», метка ErrUnavailable, как у обрыва
// тела; причина словами, без адреса (reason): *url.Error повторял бы адрес
// целиком (аудит 23.10.2026, находка 7). Срок и отмену сюда не приносят:
// их roundTrip отдаёт transportError, пока контекст обмена закрыт.
func (c *Client) resume(ctx context.Context, st state, lastID string) (io.ReadCloser, error) {
	gone := func(err error) error {
		return &tagged{tag: ErrUnavailable, err: err, msg: fmt.Sprintf(
			"сервер %s закрыл поток до ответа, и переподключиться не вышло: %s", c.name, reason(err))}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, gone(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Last-Event-ID", lastID)
	c.setHeaders(req, st)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, gone(err)
	}
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode == http.StatusOK && mt == "text/event-stream" {
		return resp.Body, nil
	}
	defer resp.Body.Close()
	// 404 здесь — не «сеанс истёк, повторите запрос»: запрос уже ушёл и,
	// может быть, исполнен. Повторять его нельзя, только сказать.
	return nil, fmt.Errorf("сервер %s закрыл поток до ответа, а продолжить его не дал: %s", c.name, c.status(resp))
}

// deleteSession — DELETE с номером сеанса (Close).
func (c *Client) deleteSession(ctx context.Context, st state) error {
	tctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(tctx, http.MethodDelete, c.url, nil)
	if err != nil {
		return fmt.Errorf("сервер %s: запрос DELETE не собрался: %s", c.name, reason(err))
	}
	c.setHeaders(req, st)
	resp, err := c.http.Do(req)
	if err != nil {
		return c.transportError(ctx, tctx, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode/100 == 2, resp.StatusCode == http.StatusMethodNotAllowed,
		resp.StatusCode == http.StatusNotFound:
		// 405 — сервер сеансов не закрывает; 404 — сеанса уже нет. Цель достигнута.
		return nil
	default:
		return c.statusError(resp)
	}
}

// sleep — пауза, которую снимает отмена.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// errTooBig — ответ больше maxBody.
var errTooBig = errors.New("ответ слишком велик")

// capReader — чтение с пределом, который в отличие от io.LimitReader говорит
// о себе ошибкой: молча обрезанный ответ выглядел бы как «поток кончился без
// ответа» или «JSON не разобрался», и искали бы не там.
type capReader struct {
	r    io.Reader
	left int64 // maxBody+1: ответ ровно в предел законен
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, errTooBig
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if c.left <= 0 && err == nil {
		err = errTooBig
	}
	return n, err
}

// clip — не больше n знаков, не разрезая букву.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
