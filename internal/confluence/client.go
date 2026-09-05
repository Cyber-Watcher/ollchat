package confluence

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Клиент Confluence: чтение страниц по REST API.
//
// Только чтение. Записи здесь нет и не будет: страницу правит человек
// в Confluence, а не модель через инструмент — цена ошибки несопоставима
// с удобством.

// Client — доступ к одному серверу Confluence.
type Client struct {
	BaseURL string
	Token   func() string // берётся при каждом запросе: токен могли сменить на лету
	HTTP    *http.Client
}

// New собирает клиента. Токен запрашивается функцией, а не хранится строкой:
// он может прийти командой посреди сеанса и не должен переживать её отмену.
func New(base string, token func() string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &Client{
		BaseURL: strings.TrimRight(base, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: timeout},
	}
}

// Page — прочитанная страница.
type Page struct {
	ID       string
	Title    string
	Space    string
	Version  int
	Updated  string
	Author   string
	Storage  string // исходная разметка
	Children []Child
	Files    []Attachment
	URL      string
}

// Child — дочерняя страница: только имя и номер, без содержимого.
//
// Содержимое детей не тянется намеренно: у страницы их бывают десятки,
// и один вызов мог бы вывалить в контекст мегабайт. Модель видит список
// и просит нужное отдельно.
type Child struct {
	ID    string
	Title string
}

// Attachment — вложение страницы.
type Attachment struct {
	Title string
	Type  string
	Size  int64
}

// Markdown переводит страницу в markdown вместе с шапкой о происхождении.
func (p Page) Markdown() (string, error) {
	body, err := ToMarkdown([]byte(p.Storage))
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", strings.TrimSpace(p.Title))
	fmt.Fprintf(&b, "<!-- Источник: %s\n", p.URL)
	fmt.Fprintf(&b, "     Пространство %s, страница %s, версия %d", p.Space, p.ID, p.Version)
	if p.Author != "" {
		fmt.Fprintf(&b, ", правил %s", p.Author)
	}
	if p.Updated != "" {
		fmt.Fprintf(&b, " %s", p.Updated[:min(len(p.Updated), 10)])
	}
	b.WriteString("\n     Собрано выгрузкой из Confluence: правки руками пропадут " +
		"при следующей выгрузке -->\n\n")
	b.WriteString(body)

	if len(p.Files) > 0 {
		b.WriteString("\n## Вложения\n\n")
		for _, f := range p.Files {
			fmt.Fprintf(&b, "- %s (%s, %d КБ)\n", f.Title, f.Type, f.Size/1024)
		}
	}
	if len(p.Children) > 0 {
		b.WriteString("\n## Дочерние страницы\n\n")
		for _, c := range p.Children {
			fmt.Fprintf(&b, "- %s (страница %s)\n", c.Title, c.ID)
		}
	}
	return b.String(), nil
}

var rePageID = regexp.MustCompile(`(?:pageId=|/pages/)(\d+)`)

// PageID вытаскивает номер страницы из того, что дал человек: голого номера
// или адреса вида .../pages/viewpage.action?pageId=66158622.
func PageID(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("не указана страница")
	}
	if isDigits(s) {
		return s, nil
	}
	if m := rePageID.FindStringSubmatch(s); len(m) == 2 {
		return m[1], nil
	}
	return "", fmt.Errorf("в %q не видно номера страницы: дайте номер или адрес вида "+
		".../pages/viewpage.action?pageId=12345", s)
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// Get читает страницу целиком: разметку, сведения о правке, детей и вложения.
func (c *Client) Get(ctx context.Context, page string, withChildren bool) (*Page, error) {
	id, err := PageID(page)
	if err != nil {
		return nil, err
	}
	if c.BaseURL == "" {
		return nil, fmt.Errorf("адрес Confluence не задан: раздел [confluence] в настройках")
	}

	var raw struct {
		ID      string               `json:"id"`
		Title   string               `json:"title"`
		Space   struct{ Key string } `json:"space"`
		Version struct {
			Number int                          `json:"number"`
			When   string                       `json:"when"`
			By     struct{ DisplayName string } `json:"by"`
		} `json:"version"`
		Body struct {
			Storage struct{ Value string } `json:"storage"`
		} `json:"body"`
	}
	if err := c.get(ctx, "/rest/api/content/"+id+"?expand=body.storage,version,space", &raw); err != nil {
		return nil, err
	}

	p := &Page{
		ID: raw.ID, Title: raw.Title, Space: raw.Space.Key,
		Version: raw.Version.Number, Updated: raw.Version.When,
		Author: raw.Version.By.DisplayName, Storage: raw.Body.Storage.Value,
		URL: c.BaseURL + "/pages/viewpage.action?pageId=" + id,
	}

	// Вложения и дети — отдельными запросами: они не всегда нужны, но когда
	// нужны, без них страница бессмысленна. Замер 25.08.2026: у страницы
	// «Инструкция по переходу с JWT на UUID» 713 знаков текста и скриншот,
	// в котором и лежит вся суть.
	var files struct {
		Results []struct {
			Title    string                     `json:"title"`
			Metadata struct{ MediaType string } `json:"metadata"`
			Ext      struct{ FileSize int64 }   `json:"extensions"`
		} `json:"results"`
	}
	if err := c.get(ctx, "/rest/api/content/"+id+"/child/attachment?limit=50", &files); err == nil {
		for _, f := range files.Results {
			p.Files = append(p.Files, Attachment{
				Title: f.Title, Type: f.Metadata.MediaType, Size: f.Ext.FileSize})
		}
	}
	if withChildren {
		var kids struct {
			Results []struct{ ID, Title string } `json:"results"`
		}
		if err := c.get(ctx, "/rest/api/content/"+id+"/child/page?limit=100", &kids); err == nil {
			for _, k := range kids.Results {
				p.Children = append(p.Children, Child{ID: k.ID, Title: k.Title})
			}
		}
	}
	return p, nil
}

// get выполняет запрос и разбирает ответ.
//
// Токен подставляется заголовком и **никогда не печатается**: ни в ошибках,
// ни в отладке. Ошибка называет код ответа и путь, но не то, чем мы
// представились.
func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	token := ""
	if c.Token != nil {
		token = strings.TrimSpace(c.Token())
	}
	if token == "" {
		return fmt.Errorf("токен Confluence не задан: команда /confluencetoken, " +
			"файл token_file или переменная token_env")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("Confluence не отвечает: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("Confluence не пустил (%d): проверьте токен и права на пространство",
			resp.StatusCode)
	case http.StatusNotFound:
		// Confluence отвечает 404 и на «нет такой страницы», и на «нет прав
		// её видеть»: существование чужой страницы он не подтверждает.
		return fmt.Errorf("страница не найдена — её нет либо она не видна этому токену")
	default:
		return fmt.Errorf("Confluence ответил %d на %s", resp.StatusCode, safePath(path))
	}
	return json.Unmarshal(body, out)
}

// safePath убирает из пути возможные параметры запроса: в сообщение об ошибке
// они не нужны, а мало ли что там окажется.
func safePath(p string) string {
	if u, err := url.Parse(p); err == nil {
		return u.Path
	}
	return p
}

// TokenFromFile читает токен из файла, проверяя права.
//
// Файл, читаемый всеми, — это не хранилище секрета, и молча брать из него
// токен нельзя: человек будет уверен, что всё в порядке.
func TokenFromFile(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("файл с токеном %s читаем не только вам (права %o): chmod 600 %s",
			path, fi.Mode().Perm(), path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// TokenFromCmd берёт токен у команды — хранилища паролей вроде pass.
func TokenFromCmd(ctx context.Context, line string) (string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", line)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("команда за токеном не отработала: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Session — токен, живущий один сеанс.
//
// Приходит командой /confluencetoken и главнее всего прочего: файл
// и переменная окружения — про «обычно», а команда — про «сейчас и вот этим».
// На диск не пишется никогда и умирает вместе с процессом.
type Session struct {
	mu    sync.RWMutex
	token string
}

// Set запоминает токен на сеанс.
func (s *Session) Set(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = strings.TrimSpace(token)
}

// Clear забывает токен.
func (s *Session) Clear() { s.Set("") }

// Has сообщает, задан ли токен на сеанс. Сам токен наружу не отдаётся:
// показывать его негде и незачем.
func (s *Session) Has() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.token != ""
}

// Resolver собирает добытчика токена: сеанс, затем файл, затем команда,
// затем переменная окружения.
//
// Порядок задан решением владельца 25.08.2026: команда главнее всего,
// потому что ею пользуются, когда прочее не сработало или токен сменился.
func Resolver(sess *Session, tokenFile, tokenCmd, tokenEnv string) func() string {
	return func() string {
		if sess != nil {
			sess.mu.RLock()
			t := sess.token
			sess.mu.RUnlock()
			if t != "" {
				return t
			}
		}
		if tokenFile != "" {
			if t, err := TokenFromFile(tokenFile); err == nil && t != "" {
				return t
			}
		}
		if tokenCmd != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			t, err := TokenFromCmd(ctx, tokenCmd)
			cancel()
			if err == nil && t != "" {
				return t
			}
		}
		if tokenEnv != "" {
			return strings.TrimSpace(os.Getenv(tokenEnv))
		}
		return ""
	}
}
