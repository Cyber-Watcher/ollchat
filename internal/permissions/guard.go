package permissions

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Режимы подтверждения (дублируют config, чтобы пакет не зависел от конфига).
const (
	ModeSafe     = "safe"
	ModeAutoEdit = "auto-edit"
	ModeNoAsk    = "noask"

	// ModeYolo — прежнее имя режима no-ask.
	//
	// «yolo» (You Only Live Once, «живём один раз») — шутка из чужих
	// инструментов, и человеку в строке состояния она не говорит ничего:
	// по ней не понять, что подтверждения выключены. Имя заменено 31.08.2026,
	// старое продолжает работать — в чужих конфигах оно уже записано,
	// и ломать их из-за переименования нельзя.
	ModeYolo = "yolo"
)

// NormalizeMode приводит написание режима к нынешнему имени.
//
// Пустая строка возвращается как есть: «не задано» решает вызывающий.
func NormalizeMode(mode string) string {
	switch mode {
	case ModeYolo, "no-ask", "dont-ask", "no_ask":
		return ModeNoAsk
	}
	return mode
}

// Request — проверяемое действие.
type Request struct {
	Kind   Kind   // Read, Write, Bash, Fetch
	Target string // абсолютный путь, команда или URL
	Tool   string // имя инструмента — для сообщений пользователю

	// Fixed — цель выбрана настройкой, а не моделью.
	//
	// Подтверждение существует затем, что модель сама называет, что прочитать
	// или запустить. У поиска по книгам и графу цель всегда одна и та же —
	// каталог базы знаний из конфига, — и модель повлиять на неё не может.
	// Спрашивать про такое значит требовать подтверждения на то, что человек
	// уже разрешил, заведя базу; проверено вживую 24.08.2026: первый же вопрос
	// по книгам упирался в окно подтверждения.
	//
	// Запрет сильнее: правило deny на этот путь по-прежнему закрывает доступ.
	Fixed bool
}

// Result — итог проверки.
type Result struct {
	Decision Decision
	Rule     *Rule  // сработавшее правило, если было
	Reason   string // пояснение для пользователя
}

// Guard объединяет правила, песочницу, режим и разрешения, выданные на сеанс.
type Guard struct {
	mu      sync.Mutex
	set     *Set
	sandbox *Sandbox
	mode    string

	// granted — разрешения «всегда в этой сессии»; в конфиг не пишутся.
	granted []Rule
	// grantedTools — инструменты, разрешённые целиком до конца сеанса.
	// Правила deny и границы песочницы продолжают действовать и для них.
	grantedTools map[string]bool
}

// NewGuard создаёт охранника действий.
func NewGuard(set *Set, sb *Sandbox, mode string) *Guard {
	if mode == "" {
		mode = ModeSafe
	}
	// Имя приводится здесь, а не только в SetMode: охранника создают и из
	// конфига, и из ключа командной строки, и из тестов. Пропусти это —
	// и прежнее имя «yolo» сохранится в поле как есть, а сравнения с ним
	// перестанут срабатывать: режим окажется включённым по названию,
	// но не по действию. Ровно это и поймал TestModeAffectsOnlyAsk.
	mode = NormalizeMode(mode)
	return &Guard{set: set, sandbox: sb, mode: mode, grantedTools: map[string]bool{}}
}

// Sandbox возвращает песочницу.
func (g *Guard) Sandbox() *Sandbox { return g.sandbox }

// Set возвращает набор правил.
func (g *Guard) Set() *Set { return g.set }

// Mode возвращает текущий режим подтверждений.
func (g *Guard) Mode() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.mode
}

// SetMode меняет режим подтверждений.
func (g *Guard) SetMode(mode string) error {
	mode = NormalizeMode(mode)
	switch mode {
	case ModeSafe, ModeAutoEdit, ModeNoAsk:
	default:
		return fmt.Errorf("неизвестный режим %q (ожидается safe, auto-edit или noask)", mode)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.mode = mode
	return nil
}

// NextMode переключает режим по кругу — для клавиши Shift+Tab.
func (g *Guard) NextMode() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch g.mode {
	case ModeSafe:
		g.mode = ModeAutoEdit
	case ModeAutoEdit:
		g.mode = ModeNoAsk
	default:
		g.mode = ModeSafe
	}
	return g.mode
}

// Check принимает решение по действию с учётом правил, режима и разрешений сеанса.
func (g *Guard) Check(req Request) Result {
	g.mu.Lock()
	mode := g.mode
	granted := append([]Rule(nil), g.granted...)
	toolGranted := g.grantedTools[req.Tool]
	g.mu.Unlock()

	// Для bash составную команду разбираем на части: deny должен срабатывать
	// на любой из них, а сама составная команда не разрешается автоматически.
	if req.Kind == KindBash {
		return g.checkBash(req, mode, granted, toolGranted)
	}

	if r := g.set.DeniedBy(req.Kind, req.Target); r != nil {
		return Result{Decision: DecisionDeny, Rule: r,
			Reason: fmt.Sprintf("запрещено правилом %s", r.Source)}
	}
	// Цель, заданная настройкой, а не моделью: спрашивать не о чем.
	// Проверка идёт после deny — запрет остаётся сильнее всего.
	if req.Fixed {
		return Result{Decision: DecisionAllow,
			Reason: "цель задана настройками, а не моделью"}
	}
	// Инструмент, разрешённый целиком, не спрашивает про каждую цель отдельно.
	if toolGranted {
		return Result{Decision: DecisionAllow,
			Reason: fmt.Sprintf("инструмент %s разрешён на время сеанса", req.Tool)}
	}
	for i := range granted {
		if granted[i].Match(req.Kind, req.Target) {
			return Result{Decision: DecisionAllow, Rule: &granted[i],
				Reason: "разрешено на время сеанса"}
		}
	}

	decision, rule := g.set.Check(req.Kind, req.Target)
	if decision == DecisionAllow {
		return Result{Decision: DecisionAllow, Rule: rule,
			Reason: fmt.Sprintf("разрешено правилом %s", rule.Source)}
	}
	return Result{Decision: applyMode(decision, req.Kind, mode), Rule: rule,
		Reason: reasonForMode(req.Kind, mode)}
}

// allowedPipeline проверяет, разрешена ли правилами КАЖДАЯ часть составной
// команды. Возвращает правило последней части — его показывают в объяснении.
//
// Часть, сама оказавшаяся составной, разрешения не даёт: это значит, что внутри
// неё осталось перенаправление или подстановка, которых SplitCommand не делит.
func allowedPipeline(g *Guard, cmd string, segments []string) *Rule {
	if len(segments) == 0 {
		return nil
	}
	// Разбор на части решает только вопрос РАЗРЕШЕНИЯ. Запрет разбирается
	// всегда и до этого места: иначе `cat x > y; rm -rf ~` с небезобидной
	// склейкой перестал бы натыкаться на deny для rm.
	if !OnlySafePipeline(cmd) {
		return nil
	}
	var last *Rule
	for _, seg := range segments {
		if IsCompound(seg) {
			return nil
		}
		decision, rule := g.set.Check(KindBash, seg)
		if decision != DecisionAllow || rule == nil || WritesSomething(seg) {
			return nil
		}
		last = rule
	}
	return last
}

func (g *Guard) checkBash(req Request, mode string, granted []Rule, toolGranted bool) Result {
	segments := SplitCommand(req.Target)
	if len(segments) == 0 {
		segments = []string{req.Target}
	}
	// Любая часть составной команды под deny — запрет всей команды.
	for _, seg := range segments {
		if r := g.set.DeniedBy(KindBash, seg); r != nil {
			return Result{Decision: DecisionDeny, Rule: r,
				Reason: fmt.Sprintf("запрещено правилом %s (часть команды: %s)", r.Source, seg)}
		}
	}

	if IsCompound(req.Target) {
		// Составная команда из разрешённых частей вопросов не стоит.
		//
		// «grep … | head -50; ls …» — это три чтения, каждое из которых
		// разрешено правилом. Спрашивать про их склейку значит спрашивать
		// про то, что человек уже разрешил, и приучать отвечать «да» не глядя.
		//
		// Условий три, и все обязательны:
		//   1. склейка безобидная — только `|`, `||`, `&&`, `;` и перевод
		//      строки (OnlySafePipeline); перенаправление, подстановка, фон
		//      и подоболочки такого разрешения не получают никогда;
		//   2. каждая часть разрешена ПРАВИЛОМ из настроек. Сеансовые
		//      разрешения («это действие до конца сеанса», «весь инструмент»)
		//      на составную команду не распространяются, как и раньше:
		//      разрешение, данное на одну строку, не должно молча
		//      расширяться на её склейки;
		//   3. ни одна часть не под deny — это проверено выше и не обходится
		//      ничем.
		if rule := allowedPipeline(g, req.Target, segments); rule != nil {
			return Result{Decision: DecisionAllow, Rule: rule,
				Reason: fmt.Sprintf("все части составной команды разрешены (%s)", rule.Source)}
		}
		// Составную команду разрешаем только явным подтверждением
		// (в режиме noask — по общему правилу режима). Разрешение инструмента
		// целиком на неё не распространяется: слишком широкий доступ.
		return Result{Decision: applyMode(DecisionAsk, KindBash, mode),
			Reason: "составная команда: требуется подтверждение"}
	}

	if toolGranted {
		return Result{Decision: DecisionAllow,
			Reason: fmt.Sprintf("инструмент %s разрешён на время сеанса", req.Tool)}
	}

	for i := range granted {
		if granted[i].Match(KindBash, req.Target) {
			return Result{Decision: DecisionAllow, Rule: &granted[i],
				Reason: "разрешено на время сеанса"}
		}
	}

	decision, rule := g.set.Check(KindBash, req.Target)
	if decision == DecisionAllow && WritesSomething(req.Target) {
		// Разрешение дано читающей программе, а ключ делает её пишущей:
		// `find … -delete`, `sort -o файл`. Спрашиваем.
		return Result{Decision: applyMode(DecisionAsk, KindBash, mode),
			Reason: "команда разрешена, но этот её ключ пишет или запускает чужое"}
	}
	if decision == DecisionAllow {
		return Result{Decision: DecisionAllow, Rule: rule,
			Reason: fmt.Sprintf("разрешено правилом %s", rule.Source)}
	}
	return Result{Decision: applyMode(decision, KindBash, mode), Rule: rule,
		Reason: reasonForMode(KindBash, mode)}
}

// applyMode превращает «спросить» в «разрешить» там, где это допускает режим.
// Решение «запрещено» режимом не меняется никогда.
func applyMode(d Decision, kind Kind, mode string) Decision {
	if d != DecisionAsk {
		return d
	}
	switch mode {
	case ModeNoAsk:
		return DecisionAllow
	case ModeAutoEdit:
		if kind == KindWrite {
			return DecisionAllow
		}
	}
	return DecisionAsk
}

func reasonForMode(kind Kind, mode string) string {
	switch mode {
	case ModeNoAsk:
		return "режим noask: подтверждение не запрашивается"
	case ModeAutoEdit:
		if kind == KindWrite {
			return "режим auto-edit: правка файлов без подтверждения"
		}
	}
	return "требуется подтверждение"
}

// GrantSession выдаёт разрешение на время сеанса по ответу «всегда разрешать».
// Действие под правилом deny разрешить нельзя.
func (g *Guard) GrantSession(kind Kind, target string) error {
	if r := g.set.DeniedBy(kind, target); r != nil {
		return fmt.Errorf("действие запрещено правилом %s и не может быть разрешено", r.Source)
	}
	rule, err := sessionRule(kind, target, g.sandbox)
	if err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.granted = append(g.granted, rule)
	return nil
}

// GrantedRules возвращает разрешения, выданные на время сеанса.
func (g *Guard) GrantedRules() []Rule {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]Rule(nil), g.granted...)
}

// GrantSessionTool разрешает инструмент целиком до конца сеанса: он перестаёт
// спрашивать подтверждение на каждую цель.
//
// Это не отключает защиту: правила deny проверяются раньше и продолжают
// действовать, песочница ограничивает пути, а составные команды bash
// по-прежнему требуют подтверждения.
func (g *Guard) GrantSessionTool(tool string) error {
	if strings.TrimSpace(tool) == "" {
		return fmt.Errorf("не указано имя инструмента")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.grantedTools[tool] = true
	return nil
}

// ToolGranted сообщает, разрешён ли инструмент целиком на время сеанса.
func (g *Guard) ToolGranted(tool string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.grantedTools[tool]
}

// GrantedTools возвращает список инструментов, разрешённых на время сеанса.
func (g *Guard) GrantedTools() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, 0, len(g.grantedTools))
	for name := range g.grantedTools {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// sessionRule строит правило для ответа «всегда разрешать».
// Для bash берётся имя программы с любыми аргументами, для путей — конкретный файл.
func sessionRule(kind Kind, target string, sb *Sandbox) (Rule, error) {
	switch kind {
	case KindBash:
		name := CommandName(target)
		if name == "" {
			return Rule{}, fmt.Errorf("не удалось определить имя команды в %q", target)
		}
		return ParseRule(fmt.Sprintf("Bash(%s:*)", name), sb.Root())
	case KindFetch:
		return ParseRule(fmt.Sprintf("Fetch(%s)", target), sb.Root())
	default:
		return ParseRule(fmt.Sprintf("%s(%s)", kind, target), sb.Root())
	}
}
