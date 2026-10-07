package confluence

import (
	"context"
	"encoding/json"
	"errors"
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
	// Token добывает токен; ошибка объясняет, почему его нет (Resolver).
	// Зовётся один раз на клиента и ещё раз на отказ 401 — см. token.
	Token func() (string, error)
	HTTP  *http.Client

	mu  sync.Mutex
	tok string
}

// New собирает клиента. Токен запрашивается функцией, а не хранится строкой:
// он может прийти командой посреди сеанса и не должен переживать её отмену.
// Клиент живёт один вызов инструмента, так что смена токена между вызовами
// подхватывается.
func New(base string, token func() (string, error), timeout time.Duration) *Client {
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

	// FilesCut и ChildrenCut — список неполон: дальше предела есть ещё или
	// он не дочитался. Молча обрезанный список выглядел бы полным.
	FilesCut, ChildrenCut bool
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

	if len(p.Files) > 0 || p.FilesCut {
		b.WriteString("\n## Вложения\n\n")
		for _, f := range p.Files {
			fmt.Fprintf(&b, "- %s (%s, %d КБ)\n", f.Title, f.Type, f.Size/1024)
		}
		cutNote(&b, p.FilesCut, len(p.Files))
	}
	if len(p.Children) > 0 || p.ChildrenCut {
		b.WriteString("\n## Дочерние страницы\n\n")
		for _, c := range p.Children {
			fmt.Fprintf(&b, "- %s (страница %s)\n", c.Title, c.ID)
		}
		cutNote(&b, p.ChildrenCut, len(p.Children))
	}
	return b.String(), nil
}

// cutNote помечает неполный список: модель и человек должны видеть, что
// это не всё.
func cutNote(b *strings.Builder, cut bool, shown int) {
	switch {
	case !cut:
	case shown == 0:
		b.WriteString("- _(список не получен)_\n")
	default:
		fmt.Fprintf(b, "- _(список неполон: показаны первые %d)_\n", shown)
	}
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
	//
	// Оба списка постраничные. Раньше бралась только первая страница (50
	// вложений, 100 детей), и остальное пропадало молча — список выглядел
	// полным. Теперь страницы читаются до предела, а неполный список помечен.
	p.FilesCut = c.list(ctx, "/rest/api/content/"+id+"/child/attachment", maxFiles, func(raw json.RawMessage) {
		var f struct {
			Title    string                     `json:"title"`
			Metadata struct{ MediaType string } `json:"metadata"`
			Ext      struct{ FileSize int64 }   `json:"extensions"`
		}
		if json.Unmarshal(raw, &f) == nil {
			p.Files = append(p.Files, Attachment{Title: f.Title, Type: f.Metadata.MediaType, Size: f.Ext.FileSize})
		}
	})
	if withChildren {
		p.ChildrenCut = c.list(ctx, "/rest/api/content/"+id+"/child/page", maxChildren, func(raw json.RawMessage) {
			var k struct{ ID, Title string }
			if json.Unmarshal(raw, &k) == nil {
				p.Children = append(p.Children, Child{ID: k.ID, Title: k.Title})
			}
		})
	}
	return p, nil
}

// Пределы списков страницы. Страница уходит модели целиком, и тысяча строк
// вложений съела бы контекст; дальше предела — пометка, а не молчание.
const (
	maxFiles    = 200
	maxChildren = 500
	listPage    = 50 // элементов за запрос
)

// list читает постраничный список Confluence (results и _links.next) не дальше
// max элементов и отдаёт каждый в each. true — список неполон: за пределом
// есть ещё или очередная страница не прочиталась.
func (c *Client) list(ctx context.Context, path string, max int, each func(json.RawMessage)) bool {
	got := 0
	for start := 0; ; {
		var page struct {
			Results []json.RawMessage `json:"results"`
			Links   struct {
				Next string `json:"next"`
			} `json:"_links"`
		}
		if err := c.get(ctx, fmt.Sprintf("%s?limit=%d&start=%d", path, listPage, start), &page); err != nil {
			return true
		}
		for _, r := range page.Results {
			if got == max {
				return true
			}
			each(r)
			got++
		}
		if page.Links.Next == "" || len(page.Results) == 0 {
			return false
		}
		if got == max {
			return true
		}
		start += len(page.Results)
	}
}

// token — токен на время жизни клиента; fresh — спросить источник заново.
//
// Раньше добытчик звался на каждый HTTP-запрос, а с token_cmd это `sh -c`
// с хранилищем паролей — три запуска на страницу (текст, вложения, дети)
// и больше с перелистыванием списков. Теперь один на клиента и ещё один,
// если сервер ответил 401: токен мог истечь или смениться у источника.
func (c *Client) token(fresh bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tok != "" && !fresh {
		return c.tok, nil
	}
	if c.Token == nil {
		return "", nil
	}
	t, err := c.Token()
	c.tok = strings.TrimSpace(t)
	return c.tok, err
}

// get выполняет запрос и разбирает ответ.
//
// Токен подставляется заголовком и **никогда не печатается**: ни в ошибках,
// ни в отладке. Ошибка называет код ответа и путь, но не то, чем мы
// представились.
func (c *Client) get(ctx context.Context, path string, out any) error {
	token, terr := c.token(false)
	if token == "" {
		if terr != nil {
			// Причина — словами источника: «chmod 600» от файла с открытыми
			// правами прежде терялся, и человек слышал «токен не задан»
			// при заданном token_file.
			return fmt.Errorf("токен Confluence не получен: %w", terr)
		}
		return fmt.Errorf("токен Confluence не задан: команда /confluencetoken, " +
			"файл token_file или переменная token_env")
	}
	status, body, err := c.fetch(ctx, path, token)
	if err != nil {
		return err
	}
	if status == http.StatusUnauthorized {
		// Тот же токен повторять незачем: заново — только если источник
		// отдал другой.
		if again, _ := c.token(true); again != "" && again != token {
			if status, body, err = c.fetch(ctx, path, again); err != nil {
				return err
			}
		}
	}
	switch status {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("Confluence не пустил (%d): проверьте токен и права на пространство",
			status)
	case http.StatusNotFound:
		// Confluence отвечает 404 и на «нет такой страницы», и на «нет прав
		// её видеть»: существование чужой страницы он не подтверждает.
		return fmt.Errorf("страница не найдена — её нет либо она не видна этому токену")
	default:
		return fmt.Errorf("Confluence ответил %d на %s", status, safePath(path))
	}
	return json.Unmarshal(body, out)
}

// fetch — один запрос с данным токеном: код ответа и тело.
func (c *Client) fetch(ctx context.Context, path, token string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("Confluence не отвечает: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
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
//
// Токена нет ни в одном источнике — ошибка объясняет, что не так с файлом
// и командой. Прежде она глоталась: файл с открытыми правами молча
// пропускался, и вместо подсказки «chmod 600» человек слышал «токен не задан».
func Resolver(sess *Session, tokenFile, tokenCmd, tokenEnv string) func() (string, error) {
	return func() (string, error) {
		if sess != nil {
			sess.mu.RLock()
			t := sess.token
			sess.mu.RUnlock()
			if t != "" {
				return t, nil
			}
		}
		var why []error
		if tokenFile != "" {
			t, err := TokenFromFile(tokenFile)
			switch {
			case err != nil:
				why = append(why, fmt.Errorf("token_file: %w", err))
			case t != "":
				return t, nil
			default:
				why = append(why, fmt.Errorf("token_file: файл %s пуст", tokenFile))
			}
		}
		if tokenCmd != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			t, err := TokenFromCmd(ctx, tokenCmd)
			cancel()
			switch {
			case err != nil:
				why = append(why, fmt.Errorf("token_cmd: %w", err))
			case t != "":
				return t, nil
			default:
				why = append(why, errors.New("token_cmd: команда ничего не вывела"))
			}
		}
		if tokenEnv != "" {
			if t := strings.TrimSpace(os.Getenv(tokenEnv)); t != "" {
				return t, nil
			}
		}
		return "", errors.Join(why...)
	}
}
