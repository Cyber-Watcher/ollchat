package ui

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/vram"
)

// Команда /calc отвечает на вопрос «какое окно контекста дать каждому из N
// человек, чтобы всё поместилось в видеопамять».
//
// Считает по той же зависимости, что и olldiagtools:
//
//	VRAM = A + P × C × k
//
// где A — вес модели с постоянными расходами, C — окно на слот, P — число
// одновременно работающих, k — расход на токен. Отсюда обратная пропорция:
// вдвое больше людей — вдвое меньше окно каждому.
//
// Точность зависит от того, откуда взяты A и k:
//
//   - профиль olldiagtools — замеры на самом сервере, включая потолок,
//     подтверждённый загрузкой;
//   - без профиля — грубая оценка по одному состоянию из /api/ps, где вес
//     модели принимается равным размеру файла. Такая оценка годится понять
//     порядок величины, не более, и помечена в выводе.

// calcMsg — состояние сервера, собранное для расчёта.
type calcMsg struct {
	users       int
	model       string
	running     *ollama.RunningModel
	fileSizeMiB float64
	err         error
}

// calcCmd собирает то, что знает сервер: чем занята память сейчас и сколько
// весит файл модели.
func (m *Model) calcCmd(users int) tea.Cmd {
	client := m.client
	model := m.modelName
	return func() tea.Msg {
		ctx, cancel := contextWithTimeout(15)
		defer cancel()

		out := calcMsg{users: users, model: model}

		running, err := client.PS(ctx)
		if err != nil {
			out.err = err
			return out
		}
		for i, r := range running {
			if r.Name == model || r.Model == model {
				out.running = &running[i]
			}
		}
		if tags, err := client.Tags(ctx); err == nil {
			for _, t := range tags {
				if t.Name == model || t.Model == model {
					out.fileSizeMiB = float64(t.Size) / (1 << 20)
				}
			}
		}
		return out
	}
}

// calcReport собирает отчёт для /calc.
func (m *Model) calcReport(msg calcMsg) string {
	var b strings.Builder
	users := msg.users
	if users < 1 {
		users = 1
	}

	fmt.Fprintf(&b, "Контекстное окно на %d %s\n", users,
		plural(users, "пользователя", "пользователей", "пользователей"))
	fmt.Fprintf(&b, "  модель: %s", msg.model)
	if m.modelMaxCtx > 0 {
		fmt.Fprintf(&b, ", максимум окна %s", formatTokens(m.modelMaxCtx))
	}
	b.WriteString("\n\n")

	// Недоступный сервер — не повод молчать: расчёт живёт в профиле на диске,
	// а от сервера нужно лишь текущее состояние. Скажем о нём и посчитаем.
	if msg.err != nil {
		fmt.Fprintf(&b, "  сервер недоступен: %s\n", msg.err.Error())
		if _, ok := m.vramProfile.Model(msg.model); !ok {
			return b.String()
		}
		b.WriteString("  Считаю по сохранённым замерам — они на диске и сервер для этого не нужен.\n\n")
	}

	if m.vramProfile.Outdated() {
		fmt.Fprintf(&b, "  Профиль замеров устарел (формат %d вместо %d): расход тогда\n",
			m.vramProfile.Format, vram.ProfileFormat)
		b.WriteString("  считался по /api/ps и занижался в разы. Снимите заново:\n")
		b.WriteString("  olldiag -calc N на сервере, затем скопируйте профиль сюда.\n\n")
		m.writeRoughCalc(&b, msg, users)
	} else if res, ok := m.vramProfile.Model(msg.model); ok {
		m.writeMeasuredCalc(&b, res, users)
	} else {
		m.writeRoughCalc(&b, msg, users)
	}

	if msg.running != nil {
		fmt.Fprintf(&b, "\nСейчас загружено: окно %s, занято %.1f ГиБ",
			formatTokens(msg.running.ContextLength), float64(msg.running.Size)/(1<<30))
		if msg.running.Size > msg.running.SizeVRAM {
			fmt.Fprintf(&b, ", из них в видеопамяти %.1f — часть вытеснена в ОЗУ",
				float64(msg.running.SizeVRAM)/(1<<30))
		} else {
			b.WriteString(", целиком на GPU")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// writeMeasuredCalc — расчёт по замерам olldiagtools.
func (m *Model) writeMeasuredCalc(b *strings.Builder, res vram.ModelResult, users int) {
	fit := res.Fit

	b.WriteString("Замеры: профиль olldiagtools")
	if m.vramProfile != nil {
		if m.vramProfile.Host != "" {
			fmt.Fprintf(b, ", сервер %s", m.vramProfile.Host)
		}
		if m.vramProfile.GeneratedAt != "" {
			fmt.Fprintf(b, ", от %s", m.vramProfile.GeneratedAt)
		}
	}
	b.WriteString("\n")
	fmt.Fprintf(b, "  вес модели и постоянные расходы  %.1f ГиБ\n", fit.BaseMiB/1024)
	fmt.Fprintf(b, "  расход на токен окна             %.1f КиБ\n", fit.Slope()/1024)
	fmt.Fprintf(b, "  бюджет видеопамяти               %.1f ГиБ\n", res.Budget.UsableMiB/1024)
	if res.VerifiedMaxContext > 0 {
		fmt.Fprintf(b, "  потолок для одного, проверенный загрузкой: %s", formatTokens(res.VerifiedMaxContext))
		if vram.LimitedByModel(res.VerifiedMaxContext, res.MaxContext) {
			b.WriteString(" — это максимум самой модели, видеопамять не предел")
		}
		b.WriteString("\n")
	}

	ctx := m.contextForUsers(res, users)
	b.WriteString("\n")
	if ctx <= 0 {
		b.WriteString("  столько сессий разом не помещается в видеопамять\n")
		return
	}
	fmt.Fprintf(b, "  окно на каждого: %s (%d токенов)\n", formatTokens(ctx), ctx)
	fmt.Fprintf(b, "  прогноз занятой памяти: %.1f ГиБ\n", fit.Predict(ctx, users)/1024)

	writeFullContextVerdict(b, res)

	// Соседние значения показывают саму зависимость: она обратно пропорциональна.
	b.WriteString("\n  для сравнения:\n")
	for _, u := range []int{1, 2, 4, 8} {
		if u == users {
			continue
		}
		if c := m.contextForUsers(res, u); c > 0 {
			fmt.Fprintf(b, "    %d — %s\n", u, formatTokens(c))
		}
	}
	fmt.Fprintf(b, "\n  Поставить окно: num_ctx = %d в [servers.options] или /addcontext.\n", ctx)
	if res.VerifiedMaxContext == 0 {
		b.WriteString("  Профиль снят без -verify: потолок посчитан, но загрузкой не проверен.\n")
	}
}

// writeFullContextVerdict отвечает на вопрос «сколько нас поместится, если
// каждому дать полное окно модели». Ноль — законный ответ, и говорить о нём
// надо прямо: у больших моделей полное окно не тянет даже один человек.
func writeFullContextVerdict(b *strings.Builder, res vram.ModelResult) {
	if res.MaxContext <= 0 || res.Budget.UsableMiB <= 0 {
		return
	}
	full := res.Fit.UsersAtFullContext(res.Budget.UsableMiB, res.MaxContext, res.VerifiedMaxContext)

	b.WriteString("\n")
	if full > 0 {
		fmt.Fprintf(b, "  Полное окно модели %s (%s) одновременно потянут: %d %s\n",
			res.Name, formatTokens(res.MaxContext), full,
			plural(full, "пользователь", "пользователя", "пользователей"))
		return
	}
	fmt.Fprintf(b, "  Полное окно модели %s (%s) не потянет ни один пользователь.\n",
		res.Name, formatTokens(res.MaxContext))
	single := res.Fit.ContextForUsers(res.Budget.UsableMiB, 1, res.MaxContext, res.VerifiedMaxContext)
	if single > 0 {
		fmt.Fprintf(b, "  Максимум для одного: %s (%d токенов).\n", formatTokens(single), single)
	} else {
		b.WriteString("  Модель не помещается в видеопамять даже с минимальным окном.\n")
	}
}

// contextForUsers — окно на одного из users при известных замерах.
func (m *Model) contextForUsers(res vram.ModelResult, users int) int {
	return res.Fit.ContextForUsers(res.Budget.UsableMiB, users, res.MaxContext, res.VerifiedMaxContext)
}

// writeRoughCalc — оценка без профиля, по одному состоянию сервера.
func (m *Model) writeRoughCalc(b *strings.Builder, msg calcMsg, users int) {
	budgetMiB := m.server.VRAMMiB()

	b.WriteString("Замеров нет — считаю грубо, по текущему состоянию сервера.\n")
	b.WriteString("  Оценка выйдет ЗАНИЖЕННОЙ, и заметно: единственное, что видно по сети, —\n")
	b.WriteString("  это /api/ps, а Ollama не учитывает в нём буферы вычислений. На стенде\n")
	b.WriteString("  gemma4:31b с окном 256k занимала по карте 42.5 ГиБ, а по /api/ps — 20.4.\n")
	b.WriteString("  Настоящий расход видно только с сервера: olldiagtools меряет по nvidia-smi\n")
	b.WriteString("  и ищет потолок настоящими загрузками.\n\n")

	if msg.running == nil {
		b.WriteString("  Модель сейчас не загружена — задайте вопрос и повторите /calc:\n")
		b.WriteString("  без загруженной модели мерить нечего.\n")
		return
	}
	if msg.fileSizeMiB <= 0 {
		b.WriteString("  Размер файла модели неизвестен — оценить вес не по чему.\n")
		return
	}

	// Всё, что не файл модели и не постоянные расходы, отнесём на кэш контекста.
	const overheadMiB = 400 // буферы вычислений, замерено на стенде
	totalMiB := float64(msg.running.Size) / (1 << 20)
	ctxCost := totalMiB - msg.fileSizeMiB - overheadMiB
	if ctxCost <= 0 || msg.running.ContextLength <= 0 {
		b.WriteString("  По одному замеру разделить вес модели и кэш контекста не вышло.\n")
		return
	}
	perToken := ctxCost / float64(msg.running.ContextLength) * (1024 * 1024) // байт на токен

	fmt.Fprintf(b, "  вес файла модели                 %.1f ГиБ\n", msg.fileSizeMiB/1024)
	fmt.Fprintf(b, "  занято сейчас при окне %-9s %.1f ГиБ\n",
		formatTokens(msg.running.ContextLength), totalMiB/1024)
	fmt.Fprintf(b, "  отсюда расход на токен окна      ≈%.1f КиБ\n", perToken/1024)

	if budgetMiB <= 0 {
		b.WriteString("\n  Размер видеопамяти неизвестен: укажите vram_gib у сервера в конфиге,\n")
		b.WriteString("  и расчёт покажет предельное окно на каждого.\n")
		return
	}

	fit := vram.Fit{
		BaseMiB:          msg.fileSizeMiB + overheadMiB,
		BytesPerToken:    perToken,
		TopBytesPerToken: perToken,
	}
	// Из бюджета вычитаем то, что карта тратит мимо учёта Ollama: на стенде
	// это стабильно около полутора гигабайт.
	const outsideMiB = 1536
	usable := budgetMiB - outsideMiB

	ctx := fit.ContextMax(usable, users, m.modelMaxCtx)
	fmt.Fprintf(b, "  бюджет видеопамяти               %.1f ГиБ (из %.0f ГиБ карты)\n",
		usable/1024, budgetMiB/1024)
	b.WriteString("\n")
	if ctx <= 0 {
		b.WriteString("  столько сессий разом не помещается в видеопамять\n")
		return
	}
	fmt.Fprintf(b, "  окно на каждого: ≈%s — и это ВЕРХНЯЯ граница, а не ответ\n", formatTokens(ctx))
	b.WriteString("  Оценка опирается на одну точку из /api/ps, который занижает расход\n")
	b.WriteString("  тем сильнее, чем больше окно. Настоящее значение будет меньше,\n")
	b.WriteString("  иногда в разы. Снимите профиль olldiagtools — он мерит по карте.\n")
}

// formatTokens печатает окно в привычном виде: 131072 → 128k.
func formatTokens(n int) string {
	if n <= 0 {
		return "—"
	}
	if n%1024 == 0 {
		return fmt.Sprintf("%dk", n/1024)
	}
	return fmt.Sprintf("%d", n)
}

// calcArgCmd разбирает аргумент /calc: без него считаем на одного.
func (m *Model) calcArgCmd(arg string) tea.Cmd {
	users := 1
	if s := strings.TrimSpace(arg); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			m.addBlock(block{kind: blockError, text: "использование: /calc или /calc 4 — " +
				"число одновременно работающих пользователей"})
			return nil
		}
		users = n
	}
	m.statusMsg = "считаю…"
	return m.calcCmd(users)
}

// loadVRAMProfile читает профиль замеров olldiagtools.
//
// Профиля может не быть — это обычное дело: инструмент запускают на сервере
// отдельно. Тогда /calc считает грубо и честно об этом предупреждает.
func loadVRAMProfile(cfg *config.Config) *vram.Profile {
	path := strings.TrimSpace(cfg.General.VRAMProfile)
	if path == "" {
		path = filepath.Join(filepath.Dir(cfg.Path), "olldiag-profile.json")
	}
	p, err := vram.LoadProfile(config.ExpandPath(path))
	if err != nil {
		return nil
	}
	return p
}
