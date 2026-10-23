package mcphub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/mcpclient"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
	"github.com/Cyber-Watcher/ollchat/internal/textx"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// extTool — инструмент стороннего сервера в реестре модели.
type extTool struct {
	hub *Hub
	srv *server
	e   entry
}

func (t *extTool) Name() string { return t.e.name }

func (t *extTool) Spec() ollama.Tool {
	return ollama.Tool{Type: "function", Function: ollama.ToolSpec{
		Name: t.e.name, Description: t.e.desc, Parameters: t.e.params}}
}

// maxPreview — сколько знаков доводов показывает окно подтверждения.
const maxPreview = 4000

// refreshTimeout — срок перечитывания списка после уведомления сервера, когда
// срок самого вызова уже съеден.
const refreshTimeout = 10 * time.Second

// Plan — вызов через подтверждение: вид Fetch, цель mcp://<сервер>/<инструмент>,
// признак внешнего инструмента для охранника (в noask он спрашивает), окно
// подтверждения с сервером и доводами, вывод помечен как чужой.
func (t *extTool) Plan(args map[string]any) (*tools.Plan, error) {
	if args == nil {
		args = map[string]any{}
	}
	compact, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("доводы не сводятся в JSON: %v", err)
	}
	pretty, _ := json.MarshalIndent(args, "", "  ")
	t.hub.mu.Lock()
	changed := t.srv.changed[t.e.remote]
	pending := t.srv.pending > 0
	t.hub.mu.Unlock()

	preview := fmt.Sprintf("сервер MCP %s (%s), инструмент %s\nдоводы:\n%s",
		t.srv.name, t.srv.where, t.e.remote, textx.Shorten(string(pretty), maxPreview))
	switch {
	case changed:
		preview += "\n\nописание или схема этого инструмента поменялись за сеанс — разрешение allow до перезапуска не действует"
	case pending:
		// Сервер сказал «список поменялся», а перечитать ещё не успели
		// (refreshAfterCall): сравнить описание пока не с чем.
		preview += "\n\nсервер сообщил, что список инструментов поменялся, а перечитать его ещё не успели — " +
			"разрешение allow до конца перечитывания не действует"
	}
	p := &tools.Plan{
		Tool: t.e.name,
		Req: permissions.Request{Kind: permissions.KindFetch, Target: Target(t.srv.name, t.e.remote),
			Tool: t.e.name, External: true, Changed: changed || pending},
		Title:   fmt.Sprintf("%s(%s)", t.e.name, textx.ShortenOneLine(string(compact), 60)),
		Preview: preview,
		Foreign: true,
	}
	var images []string
	p.Images = func() []string { return images }
	p.Run = func(ctx context.Context) (string, error) {
		out, imgs, err := t.call(ctx, args)
		images = imgs
		return out, err
	}
	return p, nil
}

// call исполняет вызов. Текст сервера — и ответа, и его ошибки — уходит
// выводом, а не текстом error: вывод цикл агента помечает как чужой
// (Plan.Foreign), а текст ошибки лёг бы модели без пометки, и распоряжение
// «в ошибке» выглядело бы словами самого ollchat.
func (t *extTool) call(ctx context.Context, args map[string]any) (string, []string, error) {
	c := t.hub.use(t.srv)
	if c == nil {
		return "", nil, fmt.Errorf("сервер MCP %s: %w — /mcp reconnect %s", t.srv.name, errNotConnected, t.srv.name)
	}
	defer t.hub.release(t.srv, c)
	cctx, cancel := context.WithTimeout(ctx, t.srv.cfg.TimeoutDuration())
	res, err := c.CallTool(cctx, t.e.remote, args)
	cancel()
	// Сервер прислал «список инструментов поменялся» — перечитать (Б5).
	if c.Stale() {
		t.hub.refreshAfterCall(ctx, t.srv, c)
	}
	if err != nil {
		return t.limit(err.Error()), nil, fmt.Errorf("сервер MCP %s: вызов %s не удался (%s)", t.srv.name, t.e.remote, errKind(err))
	}
	imgs, notes := serverImages(res.Images)
	text := res.Text
	if len(notes) > 0 {
		if text != "" {
			text += "\n"
		}
		text += strings.Join(notes, "\n")
	}
	out := t.limit(text)
	if res.IsError {
		return out, nil, fmt.Errorf("сервер MCP %s: инструмент %s сообщил об ошибке", t.srv.name, t.e.remote)
	}
	return out, imgs, nil
}

// refreshAfterCall перечитывает список после уведомления сервера (Stale).
// Вызов ждёт перечитывания — следующий ход модели должен видеть новый
// список, — но лишь пока его не прервали: после Ctrl+C перечитывание
// доживает отдельно, до refreshTimeout, а не держит вызов (до 23.10.2026
// держало до 10 с, аудит). Пока оно идёт, вызовы сервера спрашивают (Plan,
// pending): описание могло поменяться, а сравнить ещё не с чем.
func (h *Hub) refreshAfterCall(ctx context.Context, s *server, c *mcpclient.Client) {
	h.mu.Lock()
	s.pending++
	s.busy[c]++ // перечитывание — тоже пользователь клиента (release)
	h.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer h.release(s, c)
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
		defer cancel()
		h.refresh(rctx, s, c)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// Картинки ответа сервера, которые уходят модели (поле images запроса к
// Ollama). Картинка не того вида или испорченная легла бы в историю и
// ломала бы каждый следующий обмен — Ollama отвергает запрос целиком, —
// большая занимала бы его место в каждом запросе. Числа выбраны, а не
// замерены: столько же картинок, сколько отдаёт view_image.
const (
	maxServerImages     = 4       // картинок одного ответа
	maxServerImageBytes = 4 << 20 // байт одной картинки
)

// imageKinds — виды картинок, которые принимает модель.
var imageKinds = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// serverImages отбирает картинки ответа: вид из imageKinds, содержимое ему
// соответствует (по первым байтам, как http.DetectContentType), размер и
// число в пределах. Прочее отброшено с заметкой — она уходит в текст ответа,
// а не пропадает молча: иначе модель решит, что сервер вернул меньше.
func serverImages(list []mcpclient.Image) (keep, notes []string) {
	extra := 0
	for _, im := range list {
		kind := strings.ToLower(strings.TrimSpace(strings.SplitN(im.MIME, ";", 2)[0]))
		if kind == "image/jpg" {
			kind = "image/jpeg"
		}
		why := ""
		switch {
		case !imageKinds[kind]:
			why = "модель принимает только png, jpeg, gif и webp"
		case base64.StdEncoding.DecodedLen(len(im.Data)) > maxServerImageBytes:
			why = fmt.Sprintf("больше %d МиБ", maxServerImageBytes>>20)
		case sniffImage(im.Data) != kind:
			why = "содержимое не совпадает с заявленным видом"
		case len(keep) >= maxServerImages:
			extra++
			continue
		}
		if why != "" {
			label := clip(cleanText(im.MIME), 40)
			if label == "" {
				label = "без вида"
			}
			notes = append(notes, fmt.Sprintf("[картинка %s отброшена: %s]", label, why))
			continue
		}
		keep = append(keep, im.Data)
	}
	if extra > 0 {
		notes = append(notes, fmt.Sprintf("[картинок сверх %d отброшено: %d]", maxServerImages, extra))
	}
	return keep, notes
}

// sniffImage — вид картинки по её первым байтам; пусто — не разобрались.
func sniffImage(data string) string {
	n := min(len(data), 1024)
	n -= n % 4
	head, err := base64.StdEncoding.DecodeString(data[:n])
	if err != nil {
		return ""
	}
	return http.DetectContentType(head)
}

// limit — предел вывода agent.max_output_kb, как у встроенных инструментов.
func (t *extTool) limit(s string) string { return tools.Truncate(s, t.hub.maxOutKB*1024) }

// errKind — род ошибки своими словами, без текста сервера.
func errKind(err error) string {
	var rpc *mcpclient.RPCError
	switch {
	case errors.Is(err, mcpclient.ErrUnavailable):
		return "сервер не ответил"
	case errors.Is(err, mcpclient.ErrUnauthorized):
		return "нужен ключ доступа или он не тот"
	case errors.Is(err, mcpclient.ErrRevision):
		return "ревизия протокола"
	case errors.Is(err, context.DeadlineExceeded):
		return "истёк срок"
	case errors.As(err, &rpc):
		return fmt.Sprintf("отказ сервера, код %d", rpc.Code)
	}
	return "ошибка обмена"
}

// Plan — вызов инструмента tool сервера server, собранный так же, как его
// собрал бы реестр модели: для --mcp-call, которому проверять права тем же
// охранником, что TUI. Сервер должен быть подключён (Reconnect).
func (h *Hub) Plan(server, tool string, args map[string]any) (*tools.Plan, error) {
	s, err := h.lookup(server)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	var found *entry
	var names []string
	for i := range s.tools {
		if s.tools[i].remote == tool {
			found = &s.tools[i]
		}
		names = append(names, s.tools[i].remote)
	}
	state := s.state
	h.mu.Unlock()
	if state != stateUp {
		return nil, fmt.Errorf("сервер MCP %s: %s", server, state)
	}
	if found == nil {
		return nil, fmt.Errorf("у сервера MCP %s нет инструмента %q среди видимых модели (есть: %s)",
			server, tool, strings.Join(names, ", "))
	}
	return (&extTool{hub: h, srv: s, e: *found}).Plan(args)
}
