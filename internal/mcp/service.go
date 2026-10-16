package mcp

import (
	"fmt"

	"github.com/Cyber-Watcher/ollchat/internal/steplog"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// ServiceOptions — что нужно сборке службы MCP сверх набора инструментов.
type ServiceOptions struct {
	// Settings — путь к ollmcp.toml; пусто или файла нет — умолчания.
	Settings string
	// OutputKB — agent.max_output_kb: потолок ответа, если файл его не задаёт.
	OutputKB int
	// Steps — журнал вызовов; nil — не писать.
	Steps *steplog.Writer
}

// NewService собирает службу MCP так, как её поднимают обе программы —
// `ollmcp` и `ollchat --serve --mcp`: набор инструментов, политика из
// ollmcp.toml (пределы параметров, срок вызова, потолок ответа) и журнал шагов.
//
// Сборка одна намеренно. До 07.10.2026 `ollchat --serve --mcp` звал голый
// NewServer: тот же набор инструментов шёл без пределов, без срока вызова,
// без потолка ответа и без журнала, а ollmcp всё это соблюдал. Две сборки
// одной службы разошлись в первой же правке — и разошлись бы снова.
func NewService(registry *tools.Registry, extra []Tool, o ServiceOptions) (*Server, error) {
	var st Settings
	if o.Settings != "" {
		var err error
		if st, err = LoadSettings(o.Settings); err != nil {
			return nil, err
		}
	}
	pol, err := st.Policy(o.OutputKB)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", o.Settings, err)
	}
	s := NewServer(registry, extra...)
	// Набор до выключения: сверять disabled_tools надо с ним, а не со списком,
	// из которого выключенное уже убрано.
	names := s.toolNames()
	if err := st.CheckDisabled(names); err != nil {
		return nil, fmt.Errorf("%s: %w", o.Settings, err)
	}
	s.Policy, s.Steps = pol, o.Steps
	if o.Settings != "" && st.Watch() {
		path := o.Settings
		s.WatchFiles = []string{path}
		s.Validate = func() error {
			st, err := LoadSettings(path)
			if err != nil {
				return err
			}
			return st.CheckDisabled(names)
		}
	}
	return s, nil
}
