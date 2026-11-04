package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/roleserver"
	"github.com/Cyber-Watcher/ollchat/internal/textx"
)

// Выбор сервера графа и эмбеддера из TUI (этап 123): /graph-server и
// /embed-server пишут имя сервера в сам конфиг — [graph].server и
// kb.embed_server. Сборка графа, ночной cron и индексация идут отдельным
// процессом и видят выбор, перечитав конфиг (решение владельца 29.10.2026).
// Чат выбирается на сеанс (/server, Ctrl+S) и в файл не пишется; сервер чата
// по умолчанию меняет ключ --use-chat-server.
//
// Проверка сервера и запись выбора — в пакете roleserver, общем с ключами
// --use-graph-server и --use-embed-server: TUI и скрипт проверяют одинаково.
// Здесь — только разбор команды, фон и лента.

const (
	roleGraph = roleserver.Graph
	roleEmbed = roleserver.Embed
)

// roleCheckMsg — итог фоновой проверки сервера перед записью выбора.
type roleCheckMsg struct {
	roleserver.Result
	force    bool
	collDirs []string // с чем сверялся выбор: перед записью — ещё раз на идущую работу
	gen      int      // поколение проверки (gen.role): чужое — итог отбрасывается
}

// roleBlock кладёт в ленту текст команды выбора сервера. Чужой текст в нём —
// имена моделей и ответы сервера, имена и адреса из конфига — идёт через
// textx.TermSafe: ESC/OSC в имени модели иначе перекрашивали бы ленту и
// меняли заголовок окна (аудит этапа 123).
func (m *Model) roleBlock(kind blockKind, text string) {
	m.addBlock(block{kind: kind, text: textx.TermSafe(text)})
}

// roleServerCmd — /graph-server и /embed-server: без имени — кто сейчас несёт
// роли; с именем — проверка в фоне и запись по её итогу.
func (m *Model) roleServerCmd(kind roleserver.Kind, arg string) tea.Cmd {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		m.roleBlock(blockNotice, m.rolesText())
		return nil
	}
	name, force := fields[0], len(fields) > 1 && fields[1] == "force"
	if len(fields) > 2 || (len(fields) == 2 && !force) {
		m.roleBlock(blockError, "использование: /"+kind.Command()+" [<имя сервера> [force]]")
		return nil
	}
	if m.cfg == nil || m.cfg.Path == "" {
		m.roleBlock(blockError, "конфиг прочитан не из файла — выбор записать некуда")
		return nil
	}
	if m.roleCancel != nil {
		m.roleBlock(blockError, "проверка сервера уже идёт — дождитесь её итога или снимите её: Esc")
		return nil
	}
	collDirs, err := roleserver.CollDirs(m.kb.base)
	if err != nil {
		m.roleBlock(blockError, "коллекции не перечислены, сверять выбор не с чем: "+err.Error())
		return nil
	}
	// Выбор пишется в общий конфиг (m.cfg.Path) — и проверяется сервер общего
	// конфига: у конфига проекта свои [[servers]], и то же имя там может
	// значить другой адрес (аудит этапа 123).
	jobCfg := m.cfg
	if m.cfg.ProjectPath != "" {
		global, _, err := config.Load(m.cfg.Path)
		if err != nil {
			m.roleBlock(blockError, "общий конфиг не прочитан, проверять выбор не по чему: "+err.Error())
			return nil
		}
		jobCfg = global
	}
	// Всё, что нужно проверке, копирует NewJob: горутина не читает m.cfg,
	// который Update меняет под ней.
	job, err := roleserver.NewJob(jobCfg, kind, name, collDirs)
	if err != nil {
		m.roleBlock(blockError, err.Error())
		return nil
	}
	if err := roleserver.CheckIdle(kind, job.Server.Name, collDirs); err != nil {
		m.roleBlock(blockError, err.Error())
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.roleCancel = cancel
	m.gen.role++
	gen := m.gen.role
	m.statusMsg = fmt.Sprintf("проверяю сервер %s для %s… (Esc — снять)", job.Server.Name, kind.Title())
	return func() tea.Msg {
		return roleCheckMsg{Result: job.RunContext(ctx), force: force, collDirs: collDirs, gen: gen}
	}
}

// cancelRoleCheck снимает идущую проверку сервера (Esc): итог её, когда
// придёт, отбросится по поколению. false — проверки нет.
func (m *Model) cancelRoleCheck() bool {
	if m.roleCancel == nil {
		return false
	}
	m.roleCancel()
	m.roleCancel = nil
	m.gen.role++
	m.statusMsg = ""
	return true
}

// onRoleCheck — итог проверки: запись выбора в конфиг или отказ.
func (m *Model) onRoleCheck(msg roleCheckMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.gen.role || m.roleCancel == nil {
		return m, nil // итог снятой проверки: выбор после неё уже другой
	}
	m.roleCancel()
	m.roleCancel = nil
	m.statusMsg = ""
	for _, n := range msg.Notes {
		m.roleBlock(blockHint, n)
	}
	if msg.Err != nil && !msg.force {
		m.roleBlock(blockError, fmt.Sprintf("сервер %s для %s не выбран: %v\n"+
			"записать всё равно: /%s %s force", msg.Server, msg.Kind.Title(), msg.Err, msg.Kind.Command(), msg.Server))
		return m, nil
	}
	if err := roleserver.CheckIdle(msg.Kind, msg.Server, msg.collDirs); err != nil {
		m.roleBlock(blockError, err.Error())
		return m, nil
	}
	if msg.Err != nil {
		m.roleBlock(blockHint, "проверка не прошла, записываю по force: "+msg.Err.Error())
	}

	if err := roleserver.Write(m.cfg.Path, msg.Kind, msg.Server, "выбран из ollchat: /"+msg.Kind.Command()); err != nil {
		m.roleBlock(blockError, "выбор не записан: "+err.Error())
		return m, nil
	}
	if err := roleserver.Apply(m.cfg, msg.Kind, msg.Server); err != nil {
		m.roleBlock(blockError, "записано в файл, но не применено в этом окне: "+err.Error())
		return m, nil
	}
	for _, w := range roleserver.Overrides(m.cfg.Path) {
		m.roleBlock(blockHint, w)
	}

	// Адрес в подтверждении — тот, что возьмут сборка и индексация: по общему
	// конфигу. Под конфигом проекта у окна свои [[servers]], и то же имя
	// значит в нём другой адрес.
	target := m.cfg
	if m.cfg.ProjectPath != "" {
		if global, _, err := config.Load(m.cfg.Path); err == nil {
			target = global
		}
	}
	var text string
	if msg.Kind == roleEmbed {
		text = fmt.Sprintf("векторы — на сервере %s (%s): записано в %s, kb.embed_server = %q. "+
			"Индексация и сборка графа возьмут его при следующем запуске, поиск в этом окне — со следующего вопроса.",
			msg.Server, target.KB.EmbedURL, m.cfg.Path, msg.Server)
		if m.cfg.KB.ServerURL != "" {
			text += " Служба библиотеки (kb.server_url) читает конфиг при запуске — у неё новый адрес после перезапуска."
		}
	} else {
		text = fmt.Sprintf("сборка графа — на сервере %s: записано в %s, [graph].server = %q; %s. "+
			"Следующая сборка (graph put, --graph-build) и ночной запуск пойдут туда.",
			msg.Server, m.cfg.Path, msg.Server, graphTargetText(target))
	}
	m.roleBlock(blockNotice, text)
	_ = m.logger.Write("Система", textx.TermSafe(text))
	return m, nil
}

// rolesText — кто сейчас несёт роли: для /graph-server и /embed-server без имени.
func (m *Model) rolesText() string {
	var b strings.Builder
	b.WriteString("чат: " + m.server.Name + " (" + m.server.URL + ") — /server, Ctrl+S; на сеанс; " +
		"по умолчанию " + m.cfg.General.DefaultServer + " — ключ --use-chat-server <имя>\n")
	g := "сборка графа: " + graphTargetText(m.cfg)
	if m.cfg.Graph.Server != "" {
		g += " — сервер " + m.cfg.Graph.Server
	}
	b.WriteString(g + " — /graph-server <имя>\n")
	switch {
	case m.cfg.KB.EmbedModel == "":
		b.WriteString("векторы: выключены (kb.embed_model пуст)")
	case m.cfg.KB.EmbedServer != "":
		b.WriteString("векторы: сервер " + m.cfg.KB.EmbedServer + " (" + m.cfg.KB.EmbedURL + ") — /embed-server <имя>")
	case m.cfg.KB.EmbedURL != "":
		b.WriteString("векторы: " + m.cfg.KB.EmbedURL + " (kb.embed_url) — /embed-server <имя>")
	default:
		b.WriteString("векторы: сервер чата — /embed-server <имя>")
	}
	b.WriteString("\nграф и векторы пишутся в конфиг: их читают сборка, ночной запуск и индексация; " +
		"сервер с чужой моделью ловится проверкой до записи (обойти — force), под идущей работой " +
		"с коллекцией выбор не пишется; из командной строки — --use-graph-server и --use-embed-server")
	return b.String()
}

// graphTargetText — адрес или узлы, на которые сейчас пойдёт сборка.
func graphTargetText(cfg *config.Config) string {
	nodes := roleserver.CurrentGraphNodes(cfg)
	if len(nodes) == 0 {
		return "адрес не задан"
	}
	if len(cfg.Graph.Nodes) == 0 {
		return "адрес " + nodes[0].URL
	}
	names := make([]string, 0, len(nodes))
	for _, n := range nodes {
		names = append(names, n.Name)
	}
	return "узлы " + strings.Join(names, ", ")
}

// roleMarks — какие роли, кроме чата, несёт сервер: для выборника Ctrl+S.
func (m *Model) roleMarks(s *config.Server) string {
	var marks []string
	g := m.cfg.Graph
	switch {
	case g.Server != "":
		if g.Server == s.Name {
			marks = append(marks, "граф")
		}
	default:
		for _, n := range roleserver.CurrentGraphNodes(m.cfg) {
			if n.URL == s.URL {
				marks = append(marks, "граф")
				break
			}
		}
	}
	k := m.cfg.KB
	if k.EmbedModel != "" {
		switch {
		case k.EmbedServer != "":
			if k.EmbedServer == s.Name {
				marks = append(marks, "векторы")
			}
		case k.EmbedURL != "":
			if strings.TrimRight(k.EmbedURL, "/") == s.URL {
				marks = append(marks, "векторы")
			}
		case m.server != nil && m.server.Name == s.Name:
			marks = append(marks, "векторы")
		}
	}
	return strings.Join(marks, ", ")
}
