package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/kbrerank"
)

// Проверка модели эмбеддингов при запуске.
//
// **Зачем.** Смысловой поиск отваливается молча. Модель эмбеддингов живёт
// на сервере, и если её там нет — не скачали, удалили, сервер сменили, — поиск
// не падает: он просто теряет половину. Остаётся BM25 по словам, «горутина»
// перестаёт находить `goroutine`, а смысловой вход в граф отключается вовсе.
// Со стороны это выглядит как «модель стала хуже отвечать», и искать причину
// человек будет где угодно, только не здесь.
//
// Поэтому при запуске делается один короткий запрос вектора. Он стоит доли
// секунды и идёт в фоне: интерфейс его не ждёт.
//
// **Проверяется только то, что действительно нужно.** Если модель эмбеддингов
// не задана или в коллекциях нет ни одного вектора — проверять нечего, смысловой
// поиск и не предполагался.
//
// **Это не совет, а поломка.** Поэтому предупреждение не выключается настройкой
// general.startup_hints: та гасит советы об устройстве конфига, а здесь речь
// о том, что заявленная возможность не работает прямо сейчас.

// embedCheckMsg — итог проверки эмбеддера.
type embedCheckMsg struct {
	model string
	err   error
}

// embedState — что показывать в строке состояния.
type embedState int

const (
	embedUnknown embedState = iota // ещё не проверяли
	embedReady                     // модель ответила
	embedDown                      // не ответила
)

// checkEmbedderCmd проверяет, отвечает ли модель эмбеддингов.
func (m *Model) checkEmbedderCmd() tea.Cmd {
	emb := m.embedder()
	if emb == nil {
		return nil // смысловой поиск не настроен — проверять нечего
	}
	if !m.haveVectors() {
		return nil // векторов нет ни у одной коллекции — поиск и так словесный
	}
	model := emb.Model()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Короткий текст: проверяется доступность, а не качество.
		_, err := emb.Embed(ctx, []string{"проба"})
		return embedCheckMsg{model: model, err: err}
	}
}

// nextEmbedCheckCmd назначает следующую проверку.
//
// Проверка повторяется, потому что модель отваливается **посреди работы**:
// сервер перезапустили, модель выгрузили, место кончилось. Одна проверка при
// запуске поймала бы только первый случай.
func (m *Model) nextEmbedCheckCmd() tea.Cmd {
	every := m.cfg.KB.EmbedCheckDuration()
	if every <= 0 {
		return nil // повторные проверки выключены настройкой
	}
	return tea.Tick(every, func(time.Time) tea.Msg { return embedTickMsg{} })
}

// embedTickMsg — пора проверить эмбеддер заново.
type embedTickMsg struct{}

// rerankStatus — показывать ли значок второй ступени и в каком состоянии.
func (m *Model) rerankStatus() (bool, embedState) {
	if m.rerank == embedUnknown {
		return false, embedUnknown
	}
	return true, m.rerank
}

// embedStatus — строка индикатора для строки состояния.
//
// Показывается **только когда есть что показывать**: смысловой поиск настроен
// и векторы посчитаны. На машине без библиотеки книг индикатор был бы вечным
// напоминанием о возможности, которой там нет.
func (m *Model) embedStatus() (string, embedState) {
	if m.embed == embedUnknown || m.embedModel == "" {
		return "", embedUnknown
	}
	return m.embedModel, m.embed
}

// ── Реранкер: вторая ступень поиска ─────────────────────────────────────────
//
// Проверяется тем же порядком и по тем же причинам, но показывается **коротко**:
// в строке состояния и так тесно, а имени службы там не нужно — важно лишь,
// работает она или нет. Имя модели говорится в сообщении, когда есть что сказать.
//
// **Индикатора нет, если переранжирование не настроено.** Пустой `kb.rerank_url`
// означает «второй ступени не хотим», и красный значок сообщал бы о поломке там,
// где ничего не ломалось.

// rerankCheckMsg — итог проверки службы переранжирования.
type rerankCheckMsg struct {
	model string
	err   error
}

// checkRerankerCmd проверяет, отвечает ли служба переранжирования.
func (m *Model) checkRerankerCmd() tea.Cmd {
	if strings.TrimSpace(m.cfg.KB.RerankURL) == "" {
		return nil // вторая ступень не настроена — проверять нечего
	}
	if !m.haveCollections() {
		return nil // переставлять нечего: книг нет
	}
	rr := kbrerank.New(m.cfg.KB.RerankOptions())
	if rr == nil {
		return nil
	}
	model := rr.Model()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return rerankCheckMsg{model: model, err: rr.Check(ctx)}
	}
}

// nextRerankCheckCmd назначает следующую проверку — тем же сроком, что у эмбеддера.
func (m *Model) nextRerankCheckCmd() tea.Cmd {
	every := m.cfg.KB.EmbedCheckDuration()
	if every <= 0 {
		return nil
	}
	return tea.Tick(every, func(time.Time) tea.Msg { return rerankTickMsg{} })
}

// rerankTickMsg — пора проверить службу переранжирования.
type rerankTickMsg struct{}

// rerankCheckNote — что сказать, когда служба не ответила.
//
// Тон мягче, чем у эмбеддера, и это намеренно: без реранкера нужное всё равно
// находится, только стоит не на первом месте. Без эмбеддера оно не находится вовсе.
func rerankCheckNote(model string, err error) string {
	return fmt.Sprintf("Служба переранжирования (%s) не отвечает: %v\n"+
		"Поиск работает, но выдача остаётся в порядке первой ступени — верный кусок "+
		"чаще оказывается не первым. Это отдельная служба llama-server с ключом "+
		"--reranking; проверьте её и настройку kb.rerank_url.", model, err)
}

// haveCollections — есть ли в базе знаний хоть одна коллекция.
//
// Для реранкера этого достаточно: он переставляет то, что нашёл поиск по словам,
// и векторы ему не нужны.
func (m *Model) haveCollections() bool {
	if m.kb.base == nil {
		return false
	}
	names, err := m.kb.base.Names()
	return err == nil && len(names) > 0
}

// haveVectors — есть ли хоть у одной коллекции посчитанные смыслы.
func (m *Model) haveVectors() bool {
	if m.kb.base == nil {
		return false
	}
	names, err := m.kb.base.Names()
	if err != nil {
		return false
	}
	for _, n := range names {
		coll, err := m.kbCollection(n)
		if err != nil {
			continue
		}
		if coll.Stats().Vectors > 0 {
			return true
		}
	}
	return false
}

// embedCheckNote — что показать человеку, когда эмбеддер не ответил.
//
// Сообщение говорит три вещи, и все три нужны: что именно сломалось, чем это
// обернётся в ответах и что с этим делать. Без второй части человек решит,
// что предупреждение можно пропустить.
func embedCheckNote(model string, err error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Модель эмбеддингов %s не отвечает: %v\n", model, err)
	b.WriteString("Смысловой поиск выключен, пока она недоступна: по книгам ищем только " +
		"по совпадению слов, смысловой вход в граф не работает. " +
		"Русский вопрос перестаёт находить английский текст — качество ответов заметно падает.\n")

	switch {
	case strings.Contains(strings.ToLower(err.Error()), "not found"):
		fmt.Fprintf(&b, "Похоже, модели нет на сервере: ollama pull %s", model)
	case strings.Contains(strings.ToLower(err.Error()), "connection refused"),
		strings.Contains(strings.ToLower(err.Error()), "no such host"),
		strings.Contains(strings.ToLower(err.Error()), "timeout"):
		b.WriteString("Похоже, недоступен сам сервер эмбеддингов — проверьте kb.embed_url " +
			"(пусто означает сервер чата)")
	default:
		b.WriteString("Проверьте kb.embed_model и kb.embed_url в файле настроек")
	}
	return b.String()
}
