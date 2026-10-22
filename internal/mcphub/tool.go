package mcphub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	t.hub.mu.Unlock()

	preview := fmt.Sprintf("сервер MCP %s (%s), инструмент %s\nдоводы:\n%s",
		t.srv.name, t.srv.where, t.e.remote, textx.Shorten(string(pretty), maxPreview))
	if changed {
		preview += "\n\nописание или схема этого инструмента поменялись за сеанс — разрешение allow до перезапуска не действует"
	}
	p := &tools.Plan{
		Tool: t.e.name,
		Req: permissions.Request{Kind: permissions.KindFetch, Target: Target(t.srv.name, t.e.remote),
			Tool: t.e.name, External: true, Changed: changed},
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
	t.hub.mu.Lock()
	c := t.srv.client
	t.hub.mu.Unlock()
	if c == nil {
		return "", nil, fmt.Errorf("сервер MCP %s: %w — /mcp reconnect %s", t.srv.name, errNotConnected, t.srv.name)
	}
	cctx, cancel := context.WithTimeout(ctx, t.srv.cfg.TimeoutDuration())
	res, err := c.CallTool(cctx, t.e.remote, args)
	cancel()
	// Сервер прислал «список инструментов поменялся» — перечитать (Б5).
	if c.Stale() {
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
		t.hub.refresh(rctx, t.srv, c)
		rcancel()
	}
	if err != nil {
		return t.limit(err.Error()), nil, fmt.Errorf("сервер MCP %s: вызов %s не удался (%s)", t.srv.name, t.e.remote, errKind(err))
	}
	out := t.limit(res.Text)
	var imgs []string
	for _, im := range res.Images {
		imgs = append(imgs, im.Data)
	}
	if res.IsError {
		return out, nil, fmt.Errorf("сервер MCP %s: инструмент %s сообщил об ошибке", t.srv.name, t.e.remote)
	}
	return out, imgs, nil
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
