package agent

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// Защита и пределы хода поверх правил разрешений (этап 112, решения владельца
// 15.10.2026). Каждая проверка включается ключом agent.* и может только
// УЖЕСТОЧИТЬ решение Guard: разрешённое сделать вопросом или запретом,
// запрещённое — никогда не разрешить.

// egressTools — инструменты, которые отправляют данные наружу.
var egressTools = map[string]bool{tools.NameHTTPFetch: true, tools.NameWebSearch: true}

// foreignSources — инструменты, чей вывод — чужой текст: страницы, поисковая
// выдача, книги, прочитанные документы. После них в разговоре может лежать
// инструкция, которую модель примет за просьбу человека (Т7, О15: «смертельная
// триада» — частное, чужой текст и выход наружу в одном сеансе).
var foreignSources = map[string]bool{
	tools.NameHTTPFetch: true, tools.NameWebSearch: true, tools.NameReadFile: true,
	tools.NameKBSearch: true, tools.NameKBRead: true, tools.NameViewImage: true,
	"search": true, "graph_entity": true, "graph_path": true, "graph_overview": true,
	"graph_topic": true, "graph_search": true, "scan_redact": true, "confluence_connector": true,
}

// tainted — вошёл ли в разговор чужой текст: есть результат инструмента-источника,
// и это не отказ (отклонённый вызов ничего не принёс).
func tainted(msgs []ollama.Message) bool {
	for _, m := range msgs {
		if m.Role == ollama.RoleTool && foreignSources[m.ToolName] && !refusal(m.Content) {
			return true
		}
	}
	return false
}

// refusal — результат вызова, который не исполнялся (отказ правил, человека
// или отключённые инструменты): чужого текста в нём нет.
func refusal(s string) bool {
	for _, p := range []string{"Пользователь ", "Действие ", "Инструменты отключены", "Инструмент недоступен"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

var privateIP = regexp.MustCompile(`\b(10\.\d{1,3}\.\d{1,3}\.\d{1,3}|192\.168\.\d{1,3}\.\d{1,3}|172\.(1[6-9]|2\d|3[01])\.\d{1,3}\.\d{1,3})\b`)

// sshConfigPath — шов для тестов: настоящий ~/.ssh/config в тест не берётся.
var sshConfigPath = func() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ssh", "config")
}

var (
	sshOnce  sync.Once
	sshNames []string
)

// sshHosts — псевдонимы (Host) и имена (HostName) из ~/.ssh/config без масок,
// в нижнем регистре; короче четырёх знаков не берутся — слишком часто
// совпадают с обычными словами. Читается один раз на процесс.
func sshHosts() []string {
	sshOnce.Do(func() {
		f, err := os.Open(sshConfigPath())
		if err != nil {
			return
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 2 {
				continue
			}
			key := strings.ToLower(fields[0])
			if key != "host" && key != "hostname" {
				continue
			}
			for _, v := range fields[1:] {
				v = strings.ToLower(v)
				if len(v) >= 4 && !strings.ContainsAny(v, "*?!") {
					sshNames = append(sshNames, v)
				}
			}
		}
	})
	return sshNames
}

// privateTarget — что частного в аргументах исходящего вызова (Н16); пусто —
// ничего. Само имя хоста в ответ не выписывается: оно и есть то, что нельзя
// разносить.
func privateTarget(args string) string {
	if m := privateIP.FindString(args); m != "" {
		return "частный адрес " + m
	}
	low := strings.ToLower(args)
	for _, h := range sshHosts() {
		re := regexp.MustCompile(`(^|[^a-z0-9.-])` + regexp.QuoteMeta(h) + `($|[^a-z0-9-])`)
		if re.MatchString(low) {
			return "имя хоста из ~/.ssh/config"
		}
	}
	return ""
}

// gate ужесточает решение Guard проверками agent.* (этап 112: Н6, Н16, Т7/О15, Ч3).
func (r *Runner) gate(name string, plan *tools.Plan, rawArgs string, d permissions.Result) permissions.Result {
	if d.Decision == permissions.DecisionDeny {
		return d
	}
	kind := plan.Req.Kind
	risky := kind == permissions.KindWrite || kind == permissions.KindBash || kind == permissions.KindFetch || egressTools[name]

	// Н6: действие с последствиями без следа в журнале не исполняется.
	if r.JournalFailClosed && risky && r.Steps != nil {
		if err := r.Steps.LastError(); err != nil {
			return permissions.Result{Decision: permissions.DecisionDeny,
				Reason: fmt.Sprintf("журнал шагов не пишется (%v) — действие без следа не исполняется (agent.journal_fail_closed)", err)}
		}
	}
	// Н16: частные адреса и имена наружу не уходят.
	if r.PrivateEgressCheck && egressTools[name] {
		if what := privateTarget(rawArgs); what != "" {
			return permissions.Result{Decision: permissions.DecisionDeny,
				Reason: "в исходящем запросе " + what + " (agent.private_egress_check)"}
		}
	}
	// Т7, О15: после чужого текста исходящий запрос — только с согласия человека.
	// В yolo не спрашивается ничего (слово владельца 16.10.2026).
	if r.UntrustedGate && egressTools[name] && r.tainted && d.Decision == permissions.DecisionAllow &&
		(r.Guard == nil || r.Guard.Mode() != permissions.ModeYolo) {
		return permissions.Result{Decision: permissions.DecisionAsk,
			Reason: "в разговоре чужой текст (страница, книга, документ) — исходящий запрос только с вашего согласия (agent.untrusted_gate)"}
	}
	// Ч3: в noask после предела правок за ход следующая — с вопросом.
	if kind == permissions.KindWrite && r.MaxWritesNoask > 0 && r.writes >= r.MaxWritesNoask &&
		d.Decision == permissions.DecisionAllow && r.Guard != nil && r.Guard.Mode() == permissions.ModeNoAsk {
		return permissions.Result{Decision: permissions.DecisionAsk,
			Reason: fmt.Sprintf("правок за ход уже %d — следующая с вашего согласия (agent.max_writes_noask)", r.writes)}
	}
	return d
}
