package agent

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
//
// Сверяются ТОЧНЫЕ строки отказов из agent.go, а не их первые слова: страница
// или файл, начатые «Пользователь …» / «Действие …», снимали метку чужого
// текста, и untrusted_gate молчал (аудит 16.10.2026, находка 7).
func refusal(s string) bool {
	for _, p := range refusalPrefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// refusalPrefixes — начала результатов, которые пишет сам агент, когда вызов
// не исполнялся. Новый текст отказа в agent.go — сюда же.
var refusalPrefixes = []string{
	"Пользователь прервал выполнение.",
	"Пользователь отклонил выполнение этого действия.",
	"Действие запрещено настройками:",
	"Действие не выполнено:",
	"Инструменты отключены",
	"Инструмент недоступен",
}

// privateIP — частные адреса в тексте запроса: RFC 1918, петля, link-local
// (метаданные облака), CGNAT 100.64/10, «любой адрес», IPv6 петля, ULA и
// link-local, localhost. До 16.10.2026 — только три диапазона RFC 1918
// (аудит, находка 6).
//
// В тексте — только полные четвёрки и адреса IPv6 с границами слова: «127.0»
// из «Chrome 127.0.6533.72» и «[::1]» из «a[::1]» — не адреса (второй круг
// 16.10); короткие и числовые формы (`127.1`, `2130706433`) ловятся в адресах
// URL через urlHost.
var privateIP = regexp.MustCompile(`(?i)(\b(10\.\d{1,3}\.\d{1,3}\.\d{1,3}|192\.168\.\d{1,3}\.\d{1,3}|172\.(1[6-9]|2\d|3[01])\.\d{1,3}\.\d{1,3}` +
	`|127\.\d{1,3}\.\d{1,3}\.\d{1,3}|169\.254\.\d{1,3}\.\d{1,3}|100\.(6[4-9]|[7-9]\d|1[01]\d|12[0-7])\.\d{1,3}\.\d{1,3}|0\.0\.0\.0|localhost)\b` +
	`|(?:^|[\s"'=/])\[?::1\]?(?:[\s"'/:]|$)|\bf[cd][0-9a-f]{2}:[0-9a-f:]+|\bfe80:[0-9a-f:]+)`)

// urlHost — хосты из адресов в тексте: их проверяют и в числовых формах
// (`2130706433`, `0x7f000001`, `127.1`), которые регулярное выражение не видит.
// Скобочный IPv6 стоит ПЕРВОЙ альтернативой: иначе `[` съедалось как хост
// (второй круг 16.10).
var urlHost = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://(?:[^/\s@"'<>]*@)?(\[[^\]]+\]|[^/\s:"'<>?#]+)`)

// numericHostPrivate — хост в числовой записи, которую принимает inet_aton
// (`2130706433`, `0x7f000001`, `127.1`, `0177.0.0.1`): частный ли адрес.
func numericHostPrivate(host string) bool {
	host = strings.Trim(host, "[]")
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
			ip.IsUnspecified() || cgnat(ip)
	}
	parts := strings.Split(host, ".")
	if len(parts) > 4 {
		return false
	}
	var vals []uint64
	for _, p := range parts {
		v, err := strconv.ParseUint(p, 0, 32) // 0x…, 0… и десятичные, как inet_aton
		if err != nil {
			return false
		}
		vals = append(vals, v)
	}
	// inet_aton: последняя часть занимает оставшиеся байты.
	var n uint64
	for i, v := range vals {
		if i == len(vals)-1 {
			n = n<<(8*uint(4-i)) | v
		} else {
			if v > 255 {
				return false
			}
			n = n<<8 | v
		}
	}
	if n > 0xffffffff {
		return false
	}
	ip := net.IPv4(byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || cgnat(ip)
}

func cgnat(ip net.IP) bool {
	v4 := ip.To4()
	return v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127
}

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
		return "частный адрес " + strings.TrimSpace(m)
	}
	for _, m := range urlHost.FindAllStringSubmatch(args, -1) {
		if numericHostPrivate(m[1]) {
			return "частный адрес " + m[1]
		}
	}
	low := strings.ToLower(args)
	for _, h := range sshHosts() {
		if sshHostRe(h).MatchString(low) {
			return "имя хоста из ~/.ssh/config"
		}
	}
	return ""
}

var (
	sshReMu sync.Mutex
	sshRe   = map[string]*regexp.Regexp{}
)

// sshHostRe — выражение для имени хоста, собранное один раз на имя, а не на
// каждый вызов (до 16.10.2026 компилировалось в цикле при каждом запросе).
func sshHostRe(h string) *regexp.Regexp {
	sshReMu.Lock()
	defer sshReMu.Unlock()
	re, ok := sshRe[h]
	if !ok {
		re = regexp.MustCompile(`(^|[^a-z0-9.-])` + regexp.QuoteMeta(h) + `($|[^a-z0-9-])`)
		sshRe[h] = re
	}
	return re
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
