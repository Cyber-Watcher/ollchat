package mcp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// Настройки службы — свой файл ollmcp.toml рядом с конфигом ollchat
// (решение владельца 04.10.2026, этап 109): служба не трогает конфиг чата,
// а модель внутри ollchat этими пределами не задевается.
//
// Файла нет — действуют умолчания, равные тому, что было до него. Правка
// файла подхватывается без перезапуска сеанса клиента: служба перезапускает
// себя на новых настройках и сообщает клиенту, что набор сменился (описания
// параметров строятся из пределов).
//
// Файл один на обе службы MCP — `ollmcp` и `ollchat --serve --mcp` (NewService):
// до 07.10.2026 он жил в пакете ollmcp, и вторая служба шла без пределов,
// срока вызова и потолка ответа. `ollchat --serve` читает его при запуске:
// перезапуска на правке у неё нет.

// Settings — содержимое ollmcp.toml.
type Settings struct {
	// OutputKB — потолок текста ответа любого инструмента, КБ; 0 — как
	// agent.max_output_kb в конфиге ollchat; отрицательное — без потолка.
	OutputKB int `toml:"output_kb"`
	// CallTimeout — общий срок одного вызова («30s», «2m»); пусто — без срока.
	CallTimeout string `toml:"call_timeout"`
	// WatchConfig — следить ли за этим файлом и перезапускаться на правке.
	WatchConfig *bool `toml:"watch_config"`
	// Limits — верхние пределы числовых параметров; 0 — умолчание.
	Limits LimitSettings `toml:"limits"`
}

// LimitSettings — верхние пределы. Поднять выше потолка кода инструмента нельзя:
// инструмент всё равно урежет, а описание солгало бы модели.
type LimitSettings struct {
	SearchTopK        int `toml:"search_top_k"`
	KBReadAround      int `toml:"kb_read_around"`
	GraphPathMaxHops  int `toml:"graph_path_max_hops"`
	GraphOverviewTopK int `toml:"graph_overview_top_k"`
	WebSearchLimit    int `toml:"web_search_limit"`
}

// limitSpec — один предел: где он действует, умолчание и потолок кода
// (0 — у инструмента своего потолка нет).
type limitSpec struct {
	key, tool, param string
	min, def, hard   int
	get              func(*LimitSettings) int
}

// limitSpecs — ровно те числовые параметры, что видит клиент службы.
// Потолки кода: search — min(top_k, 20) (internal/tools/kb.go), kb_read —
// Collection.Around режет до 5 (internal/kb), web_search — maxWebResults 10.
var limitSpecs = []limitSpec{
	{"search_top_k", tools.NameSearch, "top_k", 1, 20, 20, func(l *LimitSettings) int { return l.SearchTopK }},
	{"kb_read_around", tools.NameKBRead, "around", 0, 5, 5, func(l *LimitSettings) int { return l.KBReadAround }},
	{"graph_path_max_hops", tools.NameGraphPath, "max_hops", 1, 6, 0, func(l *LimitSettings) int { return l.GraphPathMaxHops }},
	{"graph_overview_top_k", tools.NameGraphOverview, "top_k", 1, 10, 0, func(l *LimitSettings) int { return l.GraphOverviewTopK }},
	{"web_search_limit", tools.NameWebSearch, "limit", 1, 10, 10, func(l *LimitSettings) int { return l.WebSearchLimit }},
}

// SettingsPath — где лежит файл: рядом с конфигом ollchat.
func SettingsPath(cfgPath string) string {
	return filepath.Join(filepath.Dir(cfgPath), "ollmcp.toml")
}

// LoadSettings читает файл; нет файла — пустые настройки (всё по умолчанию).
// Неизвестный ключ — ошибка: опечатка в имени предела иначе молча
// оставила бы умолчание.
func LoadSettings(path string) (Settings, error) {
	var s Settings
	md, err := toml.DecodeFile(path, &s)
	if errors.Is(err, os.ErrNotExist) {
		return Settings{}, nil
	}
	if err != nil {
		return s, fmt.Errorf("%s: %w", path, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		var keys []string
		for _, k := range und {
			keys = append(keys, k.String())
		}
		sort.Strings(keys)
		return s, fmt.Errorf("%s: неизвестные ключи: %s", path, strings.Join(keys, ", "))
	}
	if _, err := s.Policy(64); err != nil {
		return s, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// Watch — следить ли за файлом (по умолчанию да).
func (s Settings) Watch() bool { return s.WatchConfig == nil || *s.WatchConfig }

// Policy переводит настройки в политику службы. defaultKB — agent.max_output_kb.
func (s Settings) Policy(defaultKB int) (Policy, error) {
	p := Policy{Limits: map[string]map[string]Limit{}}
	switch {
	case s.OutputKB > 0:
		p.OutputBytes = s.OutputKB * 1024
	case s.OutputKB == 0 && defaultKB > 0:
		p.OutputBytes = defaultKB * 1024
	}
	if t := strings.TrimSpace(s.CallTimeout); t != "" {
		d, err := time.ParseDuration(t)
		if err != nil || d < 0 {
			return p, fmt.Errorf("call_timeout = %q: нужен срок вида «30s» или «2m»", s.CallTimeout)
		}
		p.CallTimeout = d
	}
	for _, sp := range limitSpecs {
		v := sp.get(&s.Limits)
		switch {
		case v == 0:
			v = sp.def
		case v < sp.min:
			return p, fmt.Errorf("limits.%s = %d: меньше %d не бывает (0 — умолчание %d)", sp.key, v, sp.min, sp.def)
		case sp.hard > 0 && v > sp.hard:
			return p, fmt.Errorf("limits.%s = %d: выше потолка самого инструмента (%d) — он всё равно урежет", sp.key, v, sp.hard)
		}
		if p.Limits[sp.tool] == nil {
			p.Limits[sp.tool] = map[string]Limit{}
		}
		p.Limits[sp.tool][sp.param] = Limit{Min: sp.min, Max: v}
	}
	return p, nil
}
