package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
)

// Поиск в сети.
//
// Инструмент появился по простой причине: в системном промпте пользователя
// стояло «если что-то не знаешь — поищи в интернет», а искать было нечем.
// Единственный сетевой инструмент, http_fetch, ходит по известному адресу,
// а адрес ещё надо откуда-то взять.
//
// Работает через **свой** экземпляр SearXNG. Разбирать чужую вёрстку нельзя:
// проверено на DuckDuckGo — первый запрос проходит, второй уже возвращает
// заглушку для роботов; шесть публичных экземпляров SearXNG отвечают 403 и 429.
// Свой поднимается скриптом ollscripts/searxngmanage.sh и отдаёт честный JSON.

// maxWebResults — потолок выдачи. Больше десяти ссылок модель всё равно
// не разбирает, а контекст они занимают.
const maxWebResults = 10

type webSearchTool struct{ opts Options }

func (t *webSearchTool) Name() string { return NameWebSearch }

func (t *webSearchTool) Spec() ollama.Tool {
	return ollama.Tool{Type: "function", Function: ollama.ToolSpec{
		Name: NameWebSearch,
		Description: "Ищет в интернете и возвращает список ссылок с заголовками и краткими " +
			"выдержками. Нужен, когда ответа нет ни в твоих знаниях, ни в книгах пользователя, " +
			"или когда сведения могли устареть: версии, выпуски, цены, новости, текущее " +
			"состояние проектов. Выдержки коротки — чтобы прочитать страницу целиком, " +
			"вызови " + NameHTTPFetch + " с нужным адресом из выдачи. " +
			"Ссылайся на источник адресом, а не пересказывай без указания, откуда взято.",
		Parameters: ollama.ToolParams{
			Type: "object",
			Properties: map[string]ollama.ToolProp{
				"query":    {Type: "string", Description: "Поисковый запрос обычными словами"},
				"limit":    {Type: "integer", Description: "Сколько ссылок вернуть, 1..10"},
				"language": {Type: "string", Description: "Язык выдачи: ru, en; пусто — любой"},
			},
			Required: []string{"query"},
		},
	}}
}

func (t *webSearchTool) Plan(args map[string]any) (*Plan, error) {
	query, err := requireString(args, "query")
	if err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("пустой поисковый запрос")
	}
	if t.opts.SearxURL == "" {
		return nil, errors.New("поиск в сети не настроен: укажите web.searxng_url в файле настроек. " +
			"Свой экземпляр поднимается скриптом ollscripts/searxngmanage.sh — публичные поисковики " +
			"запросы от программ отклоняют")
	}
	limit := argInt(args, "limit", 0)
	if limit <= 0 || limit > maxWebResults {
		limit = 5
	}
	lang := strings.TrimSpace(argStringOr(args, "language", ""))

	return &Plan{
		Tool: NameWebSearch,
		// Тот же вид проверки, что у http_fetch: наружу уходит запрос.
		// Целью служит адрес поисковика, а не сам запрос — правила
		// разрешений пишутся на адреса.
		Req:   permissions.Request{Kind: permissions.KindFetch, Target: t.opts.SearxURL, Tool: NameWebSearch},
		Title: fmt.Sprintf("%s(%s)", NameWebSearch, shorten(query, 60)),
		Run: func(ctx context.Context) (string, error) {
			return t.run(ctx, query, lang, limit)
		},
	}, nil
}

// webResult — одна ссылка выдачи.
type webResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
	Engine  string `json:"engine"`
}

type webResponse struct {
	Results []webResult `json:"results"`
	Answers []string    `json:"answers"`
	Query   string      `json:"query"`
}

func (t *webSearchTool) run(ctx context.Context, query, lang string, limit int) (string, error) {
	q := url.Values{}
	q.Set("q", query)
	q.Set("format", "json")
	if lang != "" {
		q.Set("language", lang)
	}
	endpoint := strings.TrimRight(t.opts.SearxURL, "/") + "/search?" + q.Encode()

	timeout := t.opts.SearxTimeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("поисковик %s недоступен: %w", t.opts.SearxURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("поисковик вернул %s — проверьте, что в его settings.yml "+
			"разрешён формат json (search.formats)", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	var out webResponse
	if err := json.Unmarshal(body, &out); err != nil {
		// Самая частая беда: JSON выключен, и в ответ приходит страница.
		return "", fmt.Errorf("поисковик ответил не JSON — в его settings.yml нужно "+
			"разрешить формат json (search.formats: [html, json]); получено %d байт", len(body))
	}
	if len(out.Results) == 0 {
		return fmt.Sprintf("По запросу %q поисковик ничего не нашёл. "+
			"Попробуйте другие слова или другой язык.", query), nil
	}
	if len(out.Results) > limit {
		out.Results = out.Results[:limit]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Найдено ссылок: %d (запрос %q).\n\n", len(out.Results), query)
	for _, a := range out.Answers {
		if a = strings.TrimSpace(a); a != "" {
			fmt.Fprintf(&b, "Краткий ответ поисковика: %s\n\n", a)
		}
	}
	for i, r := range out.Results {
		fmt.Fprintf(&b, "[%d] %s\n%s\n", i+1, strings.TrimSpace(r.Title), r.URL)
		if c := strings.TrimSpace(collapse(r.Content)); c != "" {
			fmt.Fprintf(&b, "%s\n", c)
		}
		b.WriteString("\n")
	}
	b.WriteString("Это только выдержки. Чтобы прочитать страницу целиком, вызови " +
		NameHTTPFetch + " с её адресом. В ответе указывай, откуда взято.")
	return t.opts.truncate(b.String()), nil
}

// collapse убирает переводы строк из выдержки: в выдаче они только рвут вид,
// а модели не добавляют ничего.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
