package tools

import (
	"context"

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
}

func (t *remoteGraphTool) Name() string      { return t.inner.Name() }
func (t *remoteGraphTool) Spec() ollama.Tool { return t.inner.Spec() }

// Plan строит план местным инструментом и подменяет только исполнение.
//
// Разбор доводов, заголовок и запрашиваемое разрешение остаются местными:
// отклонить неверный довод надо здесь, не занимая сеть, а подтверждение
// действия у человека спрашивается на его же машине.
func (t *remoteGraphTool) Plan(args map[string]any) (*Plan, error) {
	p, err := t.inner.Plan(args)
	if err != nil {
		return nil, err
	}
	p.Run = func(ctx context.Context) (string, error) {
		return t.caller.GraphTool(ctx, t.collection, t.inner.Name(), args)
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
