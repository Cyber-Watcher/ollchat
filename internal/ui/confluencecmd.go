package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/confluence"
)

// Команда /confluencetoken — токен Confluence на сеанс.
//
// Она особенная: в строке команды приходит секрет. Значит, ни одного следа
// он оставить не должен —
//
//   - в ленту диалога попадает только уведомление, сам ввод не показывается
//     (команды и так не печатаются, но здесь это существенно);
//   - в историю ввода команда не кладётся: иначе стрелка вверх покажет токен
//     соседу, заглянувшему в экран;
//   - в журнал чата не пишется: команды туда не идут, но правило записано
//     здесь, чтобы его не потеряли при следующей правке;
//   - на диск не сохраняется никогда.
//
// Порядок источников — решение владельца 25.08.2026: команда главнее файла
// и переменной окружения, потому что ею пользуются, когда прочее не сработало
// или токен сменился посреди работы.

// SetConfluence отдаёт модели хранилище токена на сеанс.
func (m *Model) SetConfluence(s *confluence.Session) { m.cfSession = s }

// secretCommand сообщает, что в строке ввода секрет и её нельзя ни запоминать,
// ни показывать.
func secretCommand(text string) bool {
	f := strings.Fields(strings.TrimSpace(text))
	if len(f) == 0 {
		return false
	}
	switch strings.ToLower(strings.TrimPrefix(f[0], "/")) {
	case "confluencetoken", "token":
		return true
	}
	return false
}

// confluenceTokenCmd принимает токен на сеанс.
func (m *Model) confluenceTokenCmd(arg string) tea.Cmd {
	if m.cfSession == nil {
		m.addBlock(block{kind: blockError, text: "хранилище токена не подключено"})
		return nil
	}
	arg = strings.TrimSpace(arg)

	switch strings.ToLower(arg) {
	case "":
		if m.cfSession.Has() {
			m.addBlock(block{kind: blockNotice, text: "токен Confluence задан на этот сеанс.\n" +
				"  /confluencetoken <токен>  заменить\n  /confluencetoken off      забыть"})
		} else {
			m.addBlock(block{kind: blockNotice, text: "токен Confluence на сеанс не задан — " +
				"берётся из настроек ([confluence] token_file, token_cmd, token_env).\n" +
				"  /confluencetoken <токен>  задать на сеанс"})
		}
		return nil
	case "off", "выкл", "забыть":
		m.cfSession.Clear()
		m.addBlock(block{kind: blockNotice, text: "токен Confluence забыт; " +
			"дальше берётся из настроек"})
		return nil
	}

	// Сам токен не показывается и не повторяется — ни здесь, ни где-либо ещё.
	m.cfSession.Set(arg)
	m.addBlock(block{kind: blockNotice, text: "токен Confluence принят на этот сеанс. " +
		"Он держится в памяти, на диск не пишется и в историю ввода не попадает"})
	return nil
}
