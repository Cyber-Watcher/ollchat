package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// unreachableLibrary — общая библиотека, до которой нет соединения.
type unreachableLibrary struct{}

var errLibraryDown = fmt.Errorf("библиотека http://kb.example:8377 недоступна: %w", kb.ErrUnreachable)

func (unreachableLibrary) Names() ([]string, error)         { return nil, errLibraryDown }
func (unreachableLibrary) Source(string) (kb.Source, error) { return nil, errLibraryDown }

// Служба библиотеки не ответила, kb_search искал по своим файлам — запись
// вызова в журнале шагов говорит об этом в note (этап 121, А4): иначе отчёт
// по журналу не отличил бы ответ службы от ответа своих файлов.
func TestToolStepNotesFallbackToOwnFiles(t *testing.T) {
	root := t.TempDir()
	books := filepath.Join(root, "books")
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("Goroutines and channels explained in plain words.\n", 40)
	if err := os.WriteFile(filepath.Join(books, "go.txt"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	base, err := kb.OpenBase(filepath.Join(root, "kb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	coll, err := base.Create("books", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coll.Add(context.Background(), []string{books}, kb.IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}

	sb, err := permissions.NewSandbox(root, false, false, 512)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tools.NewRegistry([]string{tools.NameKBSearch}, tools.Options{
		Sandbox: sb, MaxOutputKB: 64, Library: unreachableLibrary{},
		KB: base, KBDir: base.Dir(), KBDefault: "books", KBTopK: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	set, err := permissions.Compile(nil, nil, nil, sb.Root())
	if err != nil {
		t.Fatal(err)
	}
	call := []ollama.Event{
		{Kind: ollama.EventToolCalls, ToolCalls: []ollama.ToolCall{{
			ID:       "call_kb",
			Function: ollama.ToolCallFunc{Name: tools.NameKBSearch, Arguments: map[string]any{"query": "goroutines"}},
		}}},
		{Kind: ollama.EventDone, Stats: ollama.Stats{DoneReason: "stop"}},
	}
	r := &Runner{
		Client: &fakeChat{turns: [][]ollama.Event{call, answer("готово")}}, Model: "test", Tools: reg,
		Guard:         permissions.NewGuard(set, sb, permissions.ModeSafe),
		MaxIterations: 10, ToolsSupported: true,
	}
	steps, _, err := journaled(t, r, AnswerYes)
	if err != nil {
		t.Fatal(err)
	}
	calls := ofKind(steps, steplog.KindTool)
	if len(calls) != 1 {
		t.Fatalf("записей вызова %d, ожидалась одна: %+v", len(calls), steps)
	}
	if calls[0].Outcome != steplog.OutcomeOK || calls[0].Note != kb.FallbackNote {
		t.Errorf("запись вызова: исход %q, note %q; ждали ok и %q", calls[0].Outcome, calls[0].Note, kb.FallbackNote)
	}
}
