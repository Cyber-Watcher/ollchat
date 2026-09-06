package nodeprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// Клиент наблюдателя: как ollchat спрашивает ollnode о состоянии сервера.
//
// **Данные вспомогательные.** Наблюдатель недоступен — работа идёт как прежде.
// Сломанный градусник не повод отменять работу, и ни одна ошибка этого клиента
// не должна останавливать сборку графа: она стоит недель видеокарты, а он —
// удобство.

// DefaultPort — порт ollnode по умолчанию.
const DefaultPort = "11435"

// Client опрашивает одного наблюдателя.
type Client struct {
	url   string
	token string
	http  *http.Client
}

// NewClient готовит клиент. Пустой адрес даёт nil — «у этого узла наблюдателя
// нет», и это обычное положение дел, а не ошибка.
//
// token пустой означает «взять из OLLNODE_TOKEN»: держать секрет в переменной
// окружения безопаснее, чем в файле настроек, который копируют между машинами.
func NewClient(url, token string, timeout time.Duration) *Client {
	url = strings.TrimRight(strings.TrimSpace(url), "/")
	if url == "" {
		return nil
	}
	if token == "" {
		token = strings.TrimSpace(os.Getenv("OLLNODE_TOKEN"))
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Client{url: url, token: token, http: &http.Client{Timeout: timeout}}
}

// URL — адрес наблюдателя.
func (c *Client) URL() string {
	if c == nil {
		return ""
	}
	return c.url
}

// Node спрашивает снимок. light — дешёвая часть, без журнала и диска: ею
// опрашивают часто.
func (c *Client) Node(ctx context.Context, light bool) (*Report, error) {
	if c == nil {
		return nil, fmt.Errorf("наблюдатель не задан")
	}
	url := c.url + "/api/v1/node"
	if light {
		url += "?light=1"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("наблюдатель %s не отвечает: %w", c.url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("наблюдатель %s не принял токен: задайте OLLNODE_TOKEN "+
			"или probe_token у узла", c.url)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("наблюдатель %s ответил %s", c.url, resp.Status)
	}
	var rep Report
	if err := json.NewDecoder(resp.Body).Decode(&rep); err != nil {
		return nil, fmt.Errorf("наблюдатель %s ответил неразбираемым: %w", c.url, err)
	}
	return &rep, nil
}

// Busy — почему на этой машине сейчас не стоит считать. Пусто — можно.
//
// **Критерий — чужой процесс на карте и вытеснение, а не загрузка.** Загрузка
// карты во время нашей же сборки и так под сотню процентов, и порог по ней
// объявил бы занятым каждый работающий узел. А вот чужой процесс в списке
// nvidia-smi — однозначный факт: кто-то считает своё, и наша работа встанет
// с ним в очередь. Вытеснение модели в оперативную память — другой такой факт:
// узел формально работает, но втрое медленнее, и раздавать ему куски незачем.
//
// minForeignMiB — с какого размера чужой процесс считается работой, а не
// случайной мелочью вроде рабочего стола. 0 — 512 МиБ.
func (r *Report) Busy(minForeignMiB int) string {
	if r == nil {
		return ""
	}
	if minForeignMiB <= 0 {
		minForeignMiB = 512
	}
	var parts []string
	var foreign []string
	for _, p := range r.Foreign() {
		if p.UsedMiB < minForeignMiB {
			continue
		}
		name := p.Name
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		if name == "" {
			name = fmt.Sprintf("процесс %d", p.PID)
		}
		foreign = append(foreign, fmt.Sprintf("%s (%d МиБ)", name, p.UsedMiB))
	}
	if len(foreign) > 0 {
		parts = append(parts, "чужое на карте: "+strings.Join(foreign, ", "))
	}
	for _, m := range r.Evicted() {
		parts = append(parts, fmt.Sprintf("модель %s вытеснена в ОЗУ (на карте %d%%)",
			m.Name, m.VRAMPct))
	}
	return strings.Join(parts, "; ")
}

// Line — одна строка о состоянии машины для человека.
func (r *Report) Line() string {
	if r == nil {
		return "нет данных"
	}
	var parts []string
	for _, g := range r.GPUs {
		s := fmt.Sprintf("%s %d/%d МиБ, загрузка %d%%", g.Name, g.MemUsed, g.MemTotal, g.Util)
		if g.TempC > 0 {
			s += fmt.Sprintf(", %d °C", g.TempC)
		}
		if g.Throttle != "" {
			s += ", " + g.Throttle
		}
		parts = append(parts, s)
	}
	if len(r.GPUs) == 0 {
		parts = append(parts, "карта не видна")
	}
	if st := r.Service.State; st != "" && st != "active" {
		parts = append(parts, "служба "+st)
	}
	if n := r.Service.Slots(); n > 0 {
		parts = append(parts, fmt.Sprintf("слотов %d", n))
	}
	if h := r.Host; h != nil {
		parts = append(parts, fmt.Sprintf("ОЗУ свободно %d МиБ", h.RAMFreeMiB))
	}
	return strings.Join(parts, " · ")
}
