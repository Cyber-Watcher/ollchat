package mcphub

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/mcpclient"
)

// Отчёты — общие для /mcp в TUI и ключей --mcp-servers, --mcp-ping,
// --mcp-tools (этап 121, В1–В3, В7): человек видит одно и то же, где бы ни
// спросил, а тест одного проверяет и другое.

// Status — состояние одного сервера на сейчас.
type Status struct {
	Name     string
	Where    string // адрес без строки запроса
	Enabled  bool
	Library  bool // служба kb.server_url: инструменты у модели встроенные
	ToolsCfg int  // сколько имён в tools (0 — все)
	AllowCfg int  // сколько в allow

	State   string
	Err     string
	Info    mcpclient.Info
	HasInfo bool
	Tools   int       // сколько инструментов видит модель
	Allowed int       // из них без вопроса
	Changed []string  // имена у сервера, чьё описание поменялось за сеанс
	Skipped int       // отброшено: имя, схема, нет у сервера
	Listed  time.Time // когда читался список
	Stale   bool      // сервер сообщил, что список устарел, а перечитать не успели
}

// Up — сервер подключён и его инструменты в реестре.
func (s Status) Up() bool { return s.State == stateUp }

// Problem — что с сервером, одной строкой: состояние и ошибка.
func (s Status) Problem() string {
	if s.Err == "" {
		return s.State
	}
	return s.State + ": " + oneLine(s.Err)
}

// Statuses — состояние всех серверов.
func (h *Hub) Statuses() []Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Status, 0, len(h.names))
	for _, name := range h.names {
		s := h.servers[name]
		st := Status{Name: name, Where: s.where, Enabled: s.cfg.On(), Library: s.library,
			ToolsCfg: len(s.cfg.Tools), AllowCfg: len(s.cfg.Allow), State: s.state,
			Info: s.info, HasInfo: s.hasInfo, Tools: len(s.tools), Skipped: len(s.skipped), Listed: s.listed}
		if s.err != nil {
			st.Err = s.err.Error()
		}
		if s.client != nil {
			st.Stale = s.client.Stale()
		}
		for _, e := range s.tools {
			if s.allow[e.remote] {
				st.Allowed++
			}
		}
		for n := range s.changed {
			st.Changed = append(st.Changed, n)
		}
		sort.Strings(st.Changed)
		out = append(out, st)
	}
	return out
}

// Report — серверы и их состояние: имя, адрес, включён, сколько в tools
// и allow; у подключавшихся — ревизия, сервер, число инструментов.
func (h *Hub) Report() string {
	list := h.Statuses()
	if len(list) == 0 {
		return "Серверов MCP нет: в конфиге не описан ни один раздел [mcp.servers.<имя>]."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Серверы MCP (%d):\n", len(list))
	for _, st := range list {
		on := "включён"
		if !st.Enabled {
			on = "выключен"
		}
		tools := "все"
		if st.ToolsCfg > 0 {
			tools = fmt.Sprint(st.ToolsCfg)
		}
		fmt.Fprintf(&b, "  %s  %s  %s  tools: %s, allow: %d\n", st.Name, st.Where, on, tools, st.AllowCfg)
		if st.Library {
			b.WriteString("      служба библиотеки (kb.server_url): search, kb_read, graph_* модель получает под встроенными именами; " +
				"вторым набором — только перечисленные в tools без встроенного двойника\n")
		}
		switch st.State {
		case stateNever, stateOff:
			fmt.Fprintf(&b, "      %s\n", st.State)
			continue
		}
		line := "      " + st.State
		if st.HasInfo {
			line += fmt.Sprintf(", ревизия %s, сервер %s %s", st.Info.Protocol,
				oneLine(st.Info.ServerName), oneLine(st.Info.ServerVersion))
		}
		if st.State == stateUp && (!st.Library || st.ToolsCfg > 0) {
			line += fmt.Sprintf(", инструментов у модели %d, без вопроса %d", st.Tools, st.Allowed)
		}
		b.WriteString(line + "\n")
		if st.Skipped > 0 {
			fmt.Fprintf(&b, "      отброшено инструментов: %d (подробно — /mcp tools %s или --mcp-tools %s)\n", st.Skipped, st.Name, st.Name)
		}
		if len(st.Changed) > 0 {
			fmt.Fprintf(&b, "      описание поменялось за сеанс (спрашивают до перезапуска): %s\n", strings.Join(st.Changed, ", "))
		}
		if st.Stale {
			b.WriteString("      сервер сообщил, что список инструментов устарел\n")
		}
		if st.Err != "" {
			fmt.Fprintf(&b, "      %s\n", oneLine(st.Err))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// ToolStatus — инструмент сервера, как его видит модель.
type ToolStatus struct {
	Name    string // mcp__<сервер>__<имя>
	Remote  string // имя у сервера
	Allow   bool   // без вопроса
	Changed bool   // описание поменялось за сеанс
}

// Tools — инструменты сервера name по последнему прочитанному списку и
// отброшенные с причиной. Сеть не трогает.
func (h *Hub) Tools(name string) ([]ToolStatus, []string, error) {
	s, err := h.lookup(name)
	if err != nil {
		return nil, nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if s.state != stateUp {
		why := s.state
		if s.err != nil {
			why += ": " + oneLine(s.err.Error())
		}
		return nil, nil, fmt.Errorf("сервер MCP %s: %s", name, why)
	}
	out := make([]ToolStatus, 0, len(s.tools))
	for _, e := range s.tools {
		out = append(out, ToolStatus{Name: e.name, Remote: e.remote, Allow: s.allow[e.remote], Changed: s.changed[e.remote]})
	}
	return out, append([]string(nil), s.skipped...), nil
}

// ToolsReport — инструменты сервера name: имена, как их увидит модель,
// отдельно — без вопроса.
func (h *Hub) ToolsReport(name string) (string, error) {
	list, skipped, err := h.Tools(name)
	if err != nil {
		return "", err
	}
	s := h.server(name)
	var b strings.Builder
	if s != nil && s.library {
		fmt.Fprintf(&b, "Сервер %s — служба библиотеки: модель получает эти инструменты под встроенными именами, не так:\n", name)
	} else {
		fmt.Fprintf(&b, "Инструменты сервера %s, как их видит модель (%d):\n", name, len(list))
	}
	var allowed []string
	for _, t := range list {
		mark := ""
		if t.Changed {
			mark = "  (описание поменялось — спрашивает до перезапуска)"
		}
		fmt.Fprintf(&b, "  %s%s\n", t.Name, mark)
		if t.Allow {
			allowed = append(allowed, t.Name)
		}
	}
	if len(allowed) > 0 {
		fmt.Fprintf(&b, "Без вопроса (allow, %d):\n", len(allowed))
		for _, n := range allowed {
			fmt.Fprintf(&b, "  %s\n", n)
		}
	} else {
		b.WriteString("Без вопроса: ни одного — каждый вызов спрашивает (кроме режима yolo).\n")
	}
	if len(skipped) > 0 {
		fmt.Fprintf(&b, "Отброшено (%d):\n", len(skipped))
		for _, sk := range skipped {
			fmt.Fprintf(&b, "  %s\n", oneLine(sk))
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// PingReport — итог рукопожатия строкой: время, ревизия, имя и версия сервера.
func PingReport(name string, info mcpclient.Info) string {
	return fmt.Sprintf("сервер MCP %s: рукопожатие за %d мс, ревизия %s, сервер %s %s",
		name, info.Took.Milliseconds(), info.Protocol, oneLine(info.ServerName), oneLine(info.ServerVersion))
}

// oneLine — чужой текст в одну строку, без управляющих знаков, не длиннее 300.
func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	return clip(strings.Join(strings.Fields(s), " "), 300)
}
