package chatclient

import (
	"context"
	"errors"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

var errNoServer = errors.New("сервер чата не выбран")

// broken — клиент сервера, который не удалось собрать (ключ API не читается):
// каждый вызов возвращает ту же ошибку настройки. Так интерфейс и --ask
// показывают её обычным путём ошибок сервера и не падают.
type broken struct{ err error }

func (b broken) Chat(_ context.Context, _ ollama.ChatRequest) <-chan ollama.Event {
	out := make(chan ollama.Event, 1)
	out <- ollama.Event{Kind: ollama.EventError, Err: b.err}
	close(out)
	return out
}

func (b broken) BaseURL() string { return "" }

func (b broken) Version(context.Context) (string, error) { return "", b.err }

func (b broken) Tags(context.Context) ([]ollama.ModelInfo, error) { return nil, b.err }

func (b broken) PS(context.Context) ([]ollama.RunningModel, error) { return nil, b.err }

func (b broken) Show(context.Context, string) (*ollama.ShowResponse, error) { return nil, b.err }

func (b broken) Resident(context.Context, string) ollama.Residency { return ollama.Residency{} }
