package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
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
	raw, err := requireString(args, "url")
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

	maxBytes := argInt(args, "max_bytes", t.opts.MaxOutputKB*1024)
	if maxBytes <= 0 || maxBytes > 4*1024*1024 {
		maxBytes = t.opts.MaxOutputKB * 1024
	}
	target := u.String()

	return &Plan{
		Tool:    NameHTTPFetch,
		Req:     permissions.Request{Kind: permissions.KindFetch, Target: target, Tool: NameHTTPFetch},
		Title:   fmt.Sprintf("%s(%s)", NameHTTPFetch, shorten(target, 70)),
		Preview: "Будет выполнен запрос GET " + target,
		Run: func(ctx context.Context) (string, error) {
			return fetchURL(ctx, target, maxBytes, t.opts)
		},
	}, nil
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

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("запрос %s: %w", target, err)
	}
	defer resp.Body.Close()

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
