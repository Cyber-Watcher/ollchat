package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/mcphub"
)

// mcpReconnectSeconds — срок переподключения по /mcp reconnect: дольше,
// чем общий срок при запуске (mcphub.ConnectTimeout), — человек сам попросил
// и ждёт, а не запускает программу.
const mcpReconnectSeconds = 30

// mcpCmd — команда /mcp (этап 121, В7): серверы и их состояние, инструменты
// сервера, переподключение. Отчёты те же, что у ключей --mcp-servers,
// --mcp-tools (internal/mcphub), — логика одна.
func (m *Model) mcpCmd(arg string) tea.Cmd {
	if m.mcp == nil {
		m.addBlock(block{kind: blockNotice, text: "серверы MCP в этом сеансе не подключены"})
		return nil
	}
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		m.addBlock(block{kind: blockNotice, text: m.mcp.Report()})
		return nil
	}
	switch strings.ToLower(fields[0]) {
	case "tools":
		if len(fields) < 2 {
			m.addBlock(block{kind: blockError, text: "использование: /mcp tools <сервер>"})
			return nil
		}
		rep, err := m.mcp.ToolsReport(fields[1])
		if err != nil {
			m.addBlock(block{kind: blockError, text: mcphub.OneLine(err.Error())})
			return nil
		}
		m.addBlock(block{kind: blockNotice, text: rep})
		return nil
	case "reconnect":
		name := ""
		if len(fields) > 1 {
			name = fields[1]
		}
		h := m.mcp
		m.statusMsg = "подключаюсь к серверам MCP…"
		return func() tea.Msg {
			ctx, cancel := contextWithTimeout(mcpReconnectSeconds)
			defer cancel()
			err := h.Reconnect(ctx, name)
			text := h.Report()
			if err != nil {
				// Текст ошибки несёт слова сервера: ESC, OSC и C1 из него
				// исполнил бы терминал человека (аудит 23.10.2026).
				text = "переподключение: " + mcphub.OneLine(err.Error()) + "\n\n" + text
			}
			return noticeMsg{text: text}
		}
	}
	m.addBlock(block{kind: blockError, text: "не понял: /mcp [tools <сервер> | reconnect [сервер]]"})
	return nil
}
