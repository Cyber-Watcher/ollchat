package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// Инструменты графа, выполняемые общей библиотекой организации.
//
// **Одна обёртка на все пять.** У каждого инструмента графа разбор доводов,
// описание для модели и заголовок действия лежат в `Plan`, а сама работа —
// в замыкании `Run`. Значит достаточно подменить `Run`, оставив всё остальное
// нетронутым: модель видит те же описания, разрешения проверяются теми же
// правилами, а отличается только место, где считается ответ.
//
// Писать пять отдельных удалённых инструментов было бы не только впятеро
// дольше, но и опаснее: пять описаний разошлись бы с местными в первую же
// правку, и модель на клиенте стала бы звать инструменты не так, как на
// сервере.
//
// **Текст приходит готовым.** Инструмент графа и локально отдаёт модели текст,
// а не структуру, поэтому по сети передавать нечего, кроме него. Служба
// выполняет тот же инструмент из того же реестра — второй реализации поиска
// по графу не существует.

// GraphCaller — то, что умеет выполнить инструмент графа на стороне службы.
type GraphCaller interface {
	GraphTool(ctx context.Context, collection, name string, args map[string]any) (string, error)
}

// remoteGraphTool — местный инструмент, работа которого уехала на службу.
type remoteGraphTool struct {
	inner      Tool
	caller     GraphCaller
	collection string
	// local — свои файлы есть (Options.KB): служба не ответила — тот же вызов
	// исполняет местный инструмент (этап 121, А4).
	local bool
	// mode — положение /kb source (Options.LibraryMode); читается на каждом
	// вызове: человек переключает его посреди сеанса.
	mode *kb.SourceSwitch
}

func (t *remoteGraphTool) Name() string      { return t.inner.Name() }
func (t *remoteGraphTool) Spec() ollama.Tool { return t.inner.Spec() }

// Plan строит план местным инструментом и подменяет только исполнение.
//
// Разбор доводов, заголовок и запрашиваемое разрешение остаются местными:
// отклонить неверный довод надо здесь, не занимая сеть, а подтверждение
// действия у человека спрашивается на его же машине.
//
// **Служба не ответила — свои файлы** (решение владельца, этап 121, А4): отказ
// в соединении или срок (kb.ErrUnreachable) — тот же вызов исполняет местный
// инструмент, и граф берётся из общего кэша графов (Options.GraphCache),
// который закроет его после простоя. Ответ помечен строкой, журнал шагов —
// Plan.Note. Своих файлов нет — остаётся ошибка службы с её адресом. Отказ
// по смыслу (ключ, неизвестная коллекция, служба без графа) переходом не
// лечится: это ошибка.
//
// **Положение /kb source** (этап 121, Д3): local — сразу местный инструмент,
// служба не спрашивается, пометки нет (это выбор человека, а не переход);
// service — только служба, её недоступность остаётся ошибкой.
func (t *remoteGraphTool) Plan(args map[string]any) (*Plan, error) {
	p, err := t.inner.Plan(args)
	if err != nil {
		return nil, err
	}
	localRun := p.Run
	p.Run = func(ctx context.Context) (string, error) {
		mode := t.mode.Mode()
		if mode == kb.SourceLocal {
			if !t.local {
				return "", errNoOwnFiles
			}
			return localRun(ctx)
		}
		out, err := t.caller.GraphTool(ctx, t.collection, t.inner.Name(), args)
		if err == nil || !errors.Is(err, kb.ErrUnreachable) {
			return out, err
		}
		if mode == kb.SourceService {
			return out, fmt.Errorf("%w (%s)", err, kb.ServiceOnlyNote)
		}
		if !t.local {
			return out, err
		}
		if ctx.Err() != nil {
			// Срок вызывающего съела служба: местному инструменту — свой.
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), localGraphTimeout)
			defer cancel()
		}
		lout, lerr := localRun(ctx)
		if lerr != nil {
			return "", fmt.Errorf("%w; по своим файлам: %v", err, lerr)
		}
		p.Note = kb.FallbackNote
		return "(" + kb.FallbackNote + ")\n" + lout, nil
	}
	return p, nil
}

// localGraphTimeout — срок местного инструмента графа после службы, съевшей
// срок вызова: открытие графа из холодного кэша — десятки секунд (замер
// 02.09.2026: 41 с на библиотеке из 465 тысяч кусков).
const localGraphTimeout = 2 * time.Minute

// RemoteCaller — исполнитель инструмента службы по протоколу MCP (этап 121, А3):
// `tools/call` к `<kb.server_url>/mcp`. Ответ — текст инструмента; ошибка
// инструмента (isError) — error с его текстом. Ошибка «служба не ответила
// вовсе» обязана нести метку kb.ErrUnreachable: по ней — переход на свои файлы.
//
// Интерфейс, а не клиент MCP напрямую: internal/mcp зовёт этот пакет, а тесты
// клиента MCP поднимают internal/mcp, и прямой импорт замкнул бы круг.
type RemoteCaller interface {
	CallTool(ctx context.Context, name string, args map[string]any) (string, error)
}

// remoteSearchTool — инструмент search, исполняемый службой ollmcp по MCP.
//
// **Зачем через MCP, а не /api/v1.** У службы `search` есть только как
// инструмент MCP; правка службы ради второго входа не нужна (решение,
// названное владельцу 22.10.2026). Описание, разбор доводов и проверка прав —
// местные, как у remoteGraphTool: модель видит то же, что без службы.
//
// **Свои файлы — запасным путём** (А4): служба не ответила (kb.ErrUnreachable),
// а коллекция на диске есть — тот же поиск по своим файлам, граф через общий
// кэш, пометка kb.FallbackNote в ответе и в Plan.Note.
type remoteSearchTool struct {
	inner      *searchTool
	caller     RemoteCaller
	collection string
	// local — поиск по своим файлам: те же настройки без общей библиотеки.
	// nil — своих файлов нет, ошибка службы остаётся ошибкой.
	local *searchTool
	// mode — положение /kb source, как у remoteGraphTool.
	mode *kb.SourceSwitch
}

// errNoOwnFiles — положение local, а своих файлов библиотеки нет.
var errNoOwnFiles = errors.New("положение local (/kb source), а своих файлов библиотеки нет: " +
	"каталог kb.dir не открылся")

func (t *remoteSearchTool) Name() string      { return t.inner.Name() }
func (t *remoteSearchTool) Spec() ollama.Tool { return t.inner.Spec() }

// Plan — местный план с исполнением в службе.
func (t *remoteSearchTool) Plan(args map[string]any) (*Plan, error) {
	p, err := t.inner.Plan(args)
	if err != nil {
		return nil, err
	}
	// Доводы уходят службе как прислала модель; коллекция по умолчанию —
	// своя (kb.default), а не службы: человек выбирал её у себя.
	remote := make(map[string]any, len(args)+1)
	for k, v := range args {
		remote[k] = v
	}
	if s, _ := remote["collection"].(string); strings.TrimSpace(s) == "" && t.collection != "" {
		remote["collection"] = t.collection
	}
	p.Run = func(ctx context.Context) (string, error) {
		mode := t.mode.Mode()
		if mode == kb.SourceLocal {
			// Свои файлы по выбору человека: служба не спрашивается, пометки
			// о переходе нет — перехода не было.
			if t.local == nil {
				return "", errNoOwnFiles
			}
			lp, err := t.local.Plan(args)
			if err != nil {
				return "", err
			}
			return lp.Run(ctx)
		}
		out, err := t.caller.CallTool(ctx, NameSearch, remote)
		if err == nil || !errors.Is(err, kb.ErrUnreachable) {
			return out, err
		}
		if mode == kb.SourceService {
			return out, fmt.Errorf("%w (%s)", err, kb.ServiceOnlyNote)
		}
		if t.local == nil {
			return out, err
		}
		if ctx.Err() != nil {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), localGraphTimeout)
			defer cancel()
		}
		lp, lerr := t.local.Plan(args)
		if lerr != nil {
			return "", fmt.Errorf("%w; по своим файлам: %v", err, lerr)
		}
		lout, lerr := lp.Run(ctx)
		if lerr != nil {
			return "", fmt.Errorf("%w; по своим файлам: %v", err, lerr)
		}
		p.Note = kb.FallbackNote
		return "(" + kb.FallbackNote + ")\n" + lout, nil
	}
	return p, nil
}

// graphTools — какие инструменты уезжают на службу.
var graphTools = map[string]bool{
	NameGraphSearch:   true,
	NameGraphEntity:   true,
	NameGraphPath:     true,
	NameGraphOverview: true,
	NameGraphTopic:    true,
}

// IsGraphTool сообщает, что имя — один из инструментов графа, которые клиент
// общей библиотеки отправляет на службу.
//
// **Служба сверяется с этим же набором.** Вход `/api/v1/graph/tool` обязан
// исполнять ровно то, что клиент вправе туда послать, и ничего сверх. Раньше
// служба проверяла лишь «есть ли такой инструмент в реестре», а реестр был
// диалоговым — с `bash` и `write_file`: запрос с именем `bash` исполнялся
// без правил и без подтверждения, от имени службы.
func IsGraphTool(name string) bool { return graphTools[name] }

// GraphToolNames — те же имена списком, в постоянном порядке.
//
// Порядок нужен сборке урезанного реестра службы: из карты он выходил бы
// случайным, и описания инструментов менялись бы местами от запуска к запуску.
func GraphToolNames() []string {
	return []string{NameGraphSearch, NameGraphEntity, NameGraphPath,
		NameGraphOverview, NameGraphTopic}
}
