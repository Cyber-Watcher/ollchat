package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/Cyber-Watcher/ollchat/internal/fsx"
)

// Конфиг проекта (решение владельца 16.10.2026): `.ollchat/config.toml` в каталоге,
// откуда запущен ollchat, переопределяет общий `~/.config/ollchat/config.toml`.
// Там же копии файлов перед правкой (internal/backup), позже — скиллы и хуки.
//
// Правила слияния (выбор владельца): ключи проекта заменяют общие, а четыре
// списка правил [permissions] — allow, ask, deny, irreversible — СКЛАДЫВАЮТСЯ
// с общими: проект не может снять общий запрет или вычеркнуть необратимое.
// Серверы MCP [mcp.servers.<имя>] тоже складываются с общими — по имени
// (mergeMCPServers, аудит 23.10.2026): раньше один свой сервер в проекте молча
// убирал все общие.
// Раздел [graph] проекту не даётся: граф общий для всех проектов и стоит недель
// работы видеокарты (закон 1) — его настройки живут только в общем конфиге.
//
// Доверие (выбор владельца): конфиг проекта может включить yolo, расширить
// allow, сменить сервер, завести сервер MCP со своим token_cmd (команда
// исполнится при подключении) и allow (его инструменты пойдут без вопроса) —
// скачанный чужой проект сделал бы это за человека.
// Поэтому он читается только после ответа «да» на вопрос при запуске; ответ
// помнится по пути и отпечатку файла (TrustFile), и правка файла спрашивает снова.

// ProjectDirName — каталог ollchat в каталоге запуска.
const ProjectDirName = ".ollchat"

// ProjectPath — конфиг проекта для каталога запуска dir.
func ProjectPath(dir string) string { return filepath.Join(dir, ProjectDirName, "config.toml") }

// ProjectSum — отпечаток содержимого конфига проекта.
func ProjectSum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// TrustFile — где помнятся конфиги проектов, которым человек доверился: вне
// каталога проекта, иначе модель могла бы отметить доверие сама.
func TrustFile() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "ollchat", "trusted-projects.json")
}

func readTrust(file string) map[string]string {
	m := map[string]string{}
	if data, err := os.ReadFile(file); err == nil {
		_ = json.Unmarshal(data, &m)
	}
	return m
}

// ProjectTrusted — доверял ли человек именно этому содержимому файла path.
func ProjectTrusted(file, path, sum string) bool {
	return file != "" && readTrust(file)[path] == sum
}

// TrustProject запоминает доверие к содержимому sum файла path.
func TrustProject(file, path, sum string) error {
	if file == "" {
		return errors.New("некуда записать доверие: не найден домашний каталог")
	}
	m := readTrust(file)
	m[path] = sum
	data, _ := json.MarshalIndent(m, "", "  ")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	return fsx.WriteFileAtomic(file, data, 0o600)
}

// ReadProject читает конфиг проекта; нет файла — nil без ошибки.
func ReadProject(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// ApplyProject накладывает конфиг проекта (path, содержимое data) на c.
func (c *Config) ApplyProject(path string, data []byte) error {
	// Какие ключи задал проект — разбором в пустую структуру, до наложения.
	doc, err := toml.Decode(string(data), &Config{})
	if err != nil {
		return fmt.Errorf("разбор конфига проекта %s: %w", path, err)
	}
	// Копии, а не ссылки: срезы общих правил обнуляются ниже.
	global := Permissions{Allow: slices.Clone(c.Permissions.Allow), Ask: slices.Clone(c.Permissions.Ask),
		Deny: slices.Clone(c.Permissions.Deny), Irreversible: slices.Clone(c.Permissions.Irreversible)}
	graphBefore, namedBefore := c.Graph, c.GraphNamed
	globalMCP := maps.Clone(c.MCP.Servers)
	// Декодер TOML пишет массив в уже заполненный срез поверх старых элементов и
	// не укорачивает его: `allow = ["Bash(make:*)"]` заменял первое общее правило,
	// а семь остальных оставались (поймал TestApplyProjectOverridesAndMerges).
	// Поэтому заданные проектом срезы и карты сперва обнуляются — их значение
	// целиком из проекта, а правила складываются с копией общих ниже.
	resetDefined(reflect.ValueOf(c).Elem(), doc, nil)
	md, err := toml.Decode(string(data), c)
	if err != nil {
		return fmt.Errorf("разбор конфига проекта %s: %w", path, err)
	}
	if md.IsDefined("graph") {
		c.Graph, c.GraphNamed = graphBefore, namedBefore
		c.Warnings = append(c.Warnings, "конфиг проекта: раздел [graph] не применяется — граф общий для всех проектов, его настройки только в общем конфиге")
	}
	for _, l := range []struct {
		key    string
		global []string
		dst    *[]string
	}{
		{"allow", global.Allow, &c.Permissions.Allow},
		{"ask", global.Ask, &c.Permissions.Ask},
		{"deny", global.Deny, &c.Permissions.Deny},
		{"irreversible", global.Irreversible, &c.Permissions.Irreversible},
	} {
		if md.IsDefined("permissions", l.key) {
			*l.dst = union(l.global, *l.dst)
		}
	}
	if md.IsDefined("mcp", "servers") {
		c.MCP.Servers = mergeMCPServers(globalMCP, c.MCP.Servers, md)
	}
	for _, name := range []string{"kb", "mix", "confluence", "web", "mcp"} {
		if md.IsDefined(name) && !slices.Contains(c.sections, name) {
			c.sections = append(c.sections, name)
		}
	}
	for _, w := range unknownKeys(md.Undecoded(), nil, nil) {
		c.Warnings = append(c.Warnings, "конфиг проекта: "+w)
	}
	c.ProjectPath = path
	return c.finalize()
}

// resetDefined обнуляет срезы и карты структуры v, которые документ doc задаёт
// (по тегам toml, вглубь вложенных разделов).
func resetDefined(v reflect.Value, doc toml.MetaData, path []string) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := strings.Split(f.Tag.Get("toml"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		p := append(slices.Clone(path), name)
		if !doc.IsDefined(p...) {
			continue
		}
		fv := v.Field(i)
		switch fv.Kind() {
		case reflect.Slice, reflect.Map:
			fv.Set(reflect.Zero(fv.Type()))
		case reflect.Struct:
			resetDefined(fv, doc, p)
		}
	}
}

// mergeMCPServers складывает серверы MCP проекта (project, разобраны из md)
// с общими (global) по имени: новый сервер добавляется, у сервера с именем
// общего меняются только ключи, заданные в проекте. Источник ключа доступа
// (token_file|token_env|token_cmd) берётся из проекта целиком: берётся первый
// заданный, и token_env проекта при общем token_file иначе молча проиграл бы.
func mergeMCPServers(global, project map[string]MCPServer, md toml.MetaData) map[string]MCPServer {
	out := maps.Clone(global)
	if out == nil {
		out = map[string]MCPServer{}
	}
	for name, ps := range project {
		gs, ok := global[name]
		if !ok {
			out[name] = ps
			continue
		}
		gv, pv := reflect.ValueOf(&gs).Elem(), reflect.ValueOf(ps)
		token := false
		for i := 0; i < gv.NumField(); i++ {
			key, _, _ := strings.Cut(gv.Type().Field(i).Tag.Get("toml"), ",")
			if !md.IsDefined("mcp", "servers", name, key) {
				continue
			}
			if strings.HasPrefix(key, "token_") {
				token = true
				continue
			}
			gv.Field(i).Set(pv.Field(i))
		}
		if token {
			gs.TokenFile, gs.TokenEnv, gs.TokenCmd = ps.TokenFile, ps.TokenEnv, ps.TokenCmd
		}
		out[name] = gs
	}
	return out
}

// union — сначала общие правила, затем новые из проекта, без повторов.
func union(global, project []string) []string {
	out := append([]string(nil), global...)
	for _, s := range project {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}
