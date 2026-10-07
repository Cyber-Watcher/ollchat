package tools

import (
	"context"
	"fmt"
	"github.com/Cyber-Watcher/ollchat/internal/textx"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
)

type httpFetchTool struct{ opts Options }

func (t *httpFetchTool) Name() string { return NameHTTPFetch }

func (t *httpFetchTool) Spec() ollama.Tool {
	return ollama.Tool{Type: "function", Function: ollama.ToolSpec{
		Name:        NameHTTPFetch,
		Description: "Загружает страницу или ответ API по адресу http/https и возвращает текст.",
		Parameters: ollama.ToolParams{
			Type: "object",
			Properties: map[string]ollama.ToolProp{
				"url":       {Type: "string", Description: "Адрес, начинающийся с http:// или https://"},
				"max_bytes": {Type: "integer", Description: "Предел объёма загрузки в байтах"},
			},
			Required: []string{"url"},
		},
	}}
}

func (t *httpFetchTool) Plan(args map[string]any) (*Plan, error) {
	raw, err := requireText(args, "url")
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("некорректный адрес %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("поддерживаются только адреса http и https, получено %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("в адресе %q не указан хост", raw)
	}
	// Адрес служебной сети записан цифрами — отказ сразу, до подтверждения:
	// разрешать здесь нечего, такие адреса закрыты всегда.
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && closedAddr(ip) {
		return nil, fmt.Errorf("адрес %s закрыт: %s", ip, closedWhy)
	}

	maxBytes := argInt(args, "max_bytes", t.opts.MaxOutputKB*1024)
	if maxBytes <= 0 || maxBytes > 4*1024*1024 {
		maxBytes = t.opts.MaxOutputKB * 1024
	}
	target := u.String()

	return &Plan{
		Tool:    NameHTTPFetch,
		Foreign: true,
		Req:     permissions.Request{Kind: permissions.KindFetch, Target: target, Tool: NameHTTPFetch},
		Title:   fmt.Sprintf("%s(%s)", NameHTTPFetch, textx.ShortenOneLine(target, 70)),
		Preview: "Будет выполнен запрос GET " + target,
		Run: func(ctx context.Context) (string, error) {
			return fetchURL(ctx, target, maxBytes, t.opts)
		},
	}, nil
}

// fetchClient — один клиент на все загрузки: до этапа 91 (R8.4) он создавался
// на каждый вызов, и соединения не переиспользовались.
var fetchClient = newFetchClient()

// maxRedirects — сколько перенаправлений в пределах того же адреса пройти
// подряд. Больше — почти всегда петля.
const maxRedirects = 5

// newFetchClient собирает клиент http_fetch.
//
// **Перенаправления.** Прежний клиент сам проходил до десяти перенаправлений,
// куда бы они ни вели, и правило Fetch проверяло только первый адрес: узкое
// `Fetch(https://доверенный/**)` обходилось ответом 302 на любой другой хост,
// в том числе на внутренний. Теперь клиент идёт только в пределах того же
// источника (схема, хост и порт — те, что проверены правилом), а на чужой
// останавливается и говорит модели, куда её отправили: следующий запрос туда
// пройдёт через проверку разрешений, как любой другой.
//
// **Служебные адреса.** Link-local (169.254.0.0/16, fe80::/10) и адрес
// метаданных AWS по IPv6 (fd00:ec2::254) закрыты всегда: там живут метаданные
// облака — ключи учётной записи машины, — и ни одной законной страницы.
// Проверка стоит в Control у Dialer, то есть после разрешения имени, на
// каждом соединении: подмена записи DNS между проверкой и запросом (DNS
// rebinding) её не обходит. Петля и частные сети не закрыты: владелец ходит
// на свои службы, и решает о них правило Fetch.
func newFetchClient() *http.Client {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: refuseClosed}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = dialer.DialContext
	return &http.Client{
		Timeout:   60 * time.Second,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if !sameOrigin(req.URL, via[0].URL) {
				return http.ErrUseLastResponse
			}
			if len(via) > maxRedirects {
				return fmt.Errorf("больше %d перенаправлений подряд — похоже на петлю", maxRedirects)
			}
			return nil
		},
	}
}

// closedWhy — объяснение отказа для модели и человека.
const closedWhy = "это служебный адрес (link-local, метаданные облака), запросы к нему не выполняются никогда"

// awsMetadataV6 — адрес службы метаданных AWS по IPv6.
var awsMetadataV6 = netip.MustParseAddr("fd00:ec2::254")

// closedAddr сообщает, что к адресу ходить нельзя ни при каких правилах.
func closedAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsLinkLocalUnicast() || ip.WithZone("") == awsMetadataV6
}

// refuseClosed — Control для Dialer: адрес здесь уже разрешён из имени.
//
// Через прокси соединение идёт к самому прокси, и имя конечного хоста
// разрешает уже он; тогда остаётся проверка адреса, записанного цифрами
// (в Plan) — она от прокси не зависит.
func refuseClosed(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	if closedAddr(ip) {
		return fmt.Errorf("адрес %s закрыт: %s", ip, closedWhy)
	}
	return nil
}

// sameOrigin — схема, хост и порт совпадают (порт по умолчанию учитывается).
func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) &&
		strings.EqualFold(a.Hostname(), b.Hostname()) &&
		portOf(a) == portOf(b)
}

func portOf(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}

func fetchURL(ctx context.Context, target string, maxBytes int, opts Options) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "ollchat/1.0 (+https://github.com/Cyber-Watcher/ollchat)")
	req.Header.Set("Accept", "text/html,application/json,text/plain,*/*")

	resp, err := fetchClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("запрос %s: %w", target, err)
	}
	defer resp.Body.Close()

	// Перенаправление на другой адрес клиент не прошёл (см. newFetchClient):
	// модель узнаёт, куда её отправили, и сама решает, запрашивать ли.
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		if next, err := resp.Location(); err == nil {
			return fmt.Sprintf("HTTP %d → %s; запросите этот адрес отдельно "+
				"(на другой адрес перенаправление само не выполняется: каждый адрес проходит "+
				"проверку разрешений)", resp.StatusCode, next), nil
		}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	if err != nil {
		return "", err
	}
	truncated := len(body) > maxBytes
	if truncated {
		body = body[:maxBytes]
	}

	ctype := resp.Header.Get("Content-Type")
	text := string(body)
	if strings.Contains(ctype, "text/html") {
		text = htmlToText(text)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "HTTP %s, тип: %s, байт: %d\n\n", resp.Status, ctype, len(body))
	b.WriteString(text)
	if truncated {
		fmt.Fprintf(&b, "\n\n[загрузка обрезана на %d байтах]", maxBytes)
	}
	return opts.truncate(b.String()), nil
}

var (
	reScriptStyle = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	reTag         = regexp.MustCompile(`(?s)<[^>]+>`)
	reSpaces      = regexp.MustCompile(`\n{3,}`)
)

// htmlToText грубо превращает HTML в текст: убирает скрипты, стили и теги.
// Это не полноценный разбор HTML — задача лишь снять разметку, чтобы модель
// не тратила контекст на теги.
func htmlToText(s string) string {
	s = reScriptStyle.ReplaceAllString(s, " ")
	s = strings.NewReplacer(
		"<br>", "\n", "<br/>", "\n", "<br />", "\n",
		"</p>", "\n\n", "</div>", "\n", "</li>", "\n",
		"</h1>", "\n\n", "</h2>", "\n\n", "</h3>", "\n\n",
		"</tr>", "\n", "</td>", "\t",
	).Replace(s)
	s = reTag.ReplaceAllString(s, "")
	s = strings.NewReplacer(
		"&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">",
		"&quot;", `"`, "&#39;", "'", "&mdash;", "—", "&ndash;", "–",
	).Replace(s)

	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	s = strings.Join(lines, "\n")
	return strings.TrimSpace(reSpaces.ReplaceAllString(s, "\n\n"))
}
