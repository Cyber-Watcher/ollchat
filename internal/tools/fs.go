package tools

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Cyber-Watcher/ollchat/internal/document"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
)

// ── read_file ────────────────────────────────────────────────────────────────

type readFileTool struct{ opts Options }

func (t *readFileTool) Name() string { return NameReadFile }

func (t *readFileTool) Spec() ollama.Tool {
	return ollama.Tool{Type: "function", Function: ollama.ToolSpec{
		Name: NameReadFile,
		Description: "Читает файл из рабочего каталога и возвращает его содержимое с номерами строк. " +
			"Документы PDF и книги EPUB читаются напрямую: инструмент сам извлекает из них текст, " +
			"постранично или по разделам. Никаких внешних программ для этого не нужно — " +
			"не пытайтесь ставить pdftotext, pypdf, ebook-convert, средства распознавания " +
			"или иные библиотеки: всё уже есть. " +
			"Метки вида [рисунок 4.1] в тексте означают картинку на этом месте. Текста в ней нет, " +
			"и часто именно в ней лежит важное — таблицы цен в коммерческих предложениях " +
			"нередко свёрстаны картинкой целиком. Чтобы посмотреть на неё, вызовите " +
			NameViewImage + ".",
		Parameters: ollama.ToolParams{
			Type: "object",
			Properties: map[string]ollama.ToolProp{
				"path":   {Type: "string", Description: "Путь к файлу относительно рабочего каталога"},
				"offset": {Type: "integer", Description: "Номер первой читаемой строки, начиная с 1"},
				"limit":  {Type: "integer", Description: "Сколько строк прочитать (по умолчанию 2000)"},
			},
			Required: []string{"path"},
		},
	}}
}

func (t *readFileTool) Plan(args map[string]any) (*Plan, error) {
	raw, err := requireString(args, "path")
	if err != nil {
		return nil, err
	}
	abs, err := t.opts.Sandbox.Resolve(raw)
	if err != nil {
		return nil, err
	}
	offset := argInt(args, "offset", 1)
	limit := argInt(args, "limit", 2000)
	rel := t.opts.Sandbox.Rel(abs)

	return &Plan{
		Tool:  NameReadFile,
		Req:   permissions.Request{Kind: permissions.KindRead, Target: abs, Tool: NameReadFile},
		Title: fmt.Sprintf("%s(%s)", NameReadFile, rel),
		Run: func(ctx context.Context) (string, error) {
			return readFile(abs, offset, limit, t.opts)
		},
	}, nil
}

func readFile(abs string, offset, limit int, opts Options) (string, error) {
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s — это каталог, используйте list_dir", abs)
	}
	// Документы читаются отдельным путём: в контекст идёт извлечённый текст,
	// поэтому предел размера у них свой.
	if document.DetectFile(abs) != document.KindNone {
		return readDocument(abs, offset, limit, opts)
	}

	if info.Size() > opts.Sandbox.MaxFileBytes() {
		return "", fmt.Errorf("файл слишком велик: %d байт, предел %d (sandbox.max_file_kb)",
			info.Size(), opts.Sandbox.MaxFileBytes())
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("файл не является текстом в кодировке UTF-8 (%d байт)", len(data))
	}

	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	return opts.truncate(numberLines(lines, offset, limit)), nil
}

// numberLines нумерует запрошенный участок строк и говорит, если показано не всё.
func numberLines(lines []string, offset, limit int) string {
	if offset < 1 {
		offset = 1
	}
	if limit <= 0 {
		limit = 2000
	}
	if offset > len(lines) {
		return fmt.Sprintf("[в файле %d строк, запрошено начало со строки %d]\n", len(lines), offset)
	}
	end := offset - 1 + limit
	if end > len(lines) {
		end = len(lines)
	}

	var b strings.Builder
	for i := offset - 1; i < end; i++ {
		fmt.Fprintf(&b, "%6d\t%s\n", i+1, lines[i])
	}
	if end < len(lines) {
		fmt.Fprintf(&b, "\n[показаны строки %d-%d из %d]\n", offset, end, len(lines))
	}
	return b.String()
}

// readDocument извлекает текст из PDF или книги EPUB. Внешние программы для
// этого не нужны: разбор идёт внутри приложения, поэтому чтение работает и там,
// где ничего нельзя доустановить.
func readDocument(abs string, offset, limit int, opts Options) (string, error) {
	doc, err := document.Read(abs, opts.Sandbox.MaxPDFBytes())
	if err != nil {
		return "", err
	}
	lines := strings.Split(doc.Text, "\n")
	return opts.truncate(doc.Header(opts.CanViewImages) + "\n\n" + numberLines(lines, offset, limit)), nil
}

// ── list_dir ─────────────────────────────────────────────────────────────────

type listDirTool struct{ opts Options }

func (t *listDirTool) Name() string { return NameListDir }

func (t *listDirTool) Spec() ollama.Tool {
	return ollama.Tool{Type: "function", Function: ollama.ToolSpec{
		Name:        NameListDir,
		Description: "Показывает содержимое каталога в рабочем каталоге проекта.",
		Parameters: ollama.ToolParams{
			Type: "object",
			Properties: map[string]ollama.ToolProp{
				"path":   {Type: "string", Description: "Путь к каталогу (по умолчанию — корень рабочего каталога)"},
				"depth":  {Type: "integer", Description: "Глубина обхода вложенных каталогов, по умолчанию 1"},
				"hidden": {Type: "boolean", Description: "Показывать файлы, начинающиеся с точки"},
			},
		},
	}}
}

func (t *listDirTool) Plan(args map[string]any) (*Plan, error) {
	raw, ok := argString(args, "path")
	if !ok || strings.TrimSpace(raw) == "" {
		raw = "."
	}
	abs, err := t.opts.Sandbox.Resolve(raw)
	if err != nil {
		return nil, err
	}
	depth := argInt(args, "depth", 1)
	if depth < 1 {
		depth = 1
	}
	if depth > 8 {
		depth = 8
	}
	hidden := argBool(args, "hidden", false)
	rel := t.opts.Sandbox.Rel(abs)

	return &Plan{
		Tool:  NameListDir,
		Req:   permissions.Request{Kind: permissions.KindRead, Target: abs, Tool: NameListDir},
		Title: fmt.Sprintf("%s(%s)", NameListDir, rel),
		Run: func(ctx context.Context) (string, error) {
			return listDir(abs, depth, hidden, t.opts)
		},
	}, nil
}

func listDir(root string, depth int, hidden bool, opts Options) (string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s — это файл, используйте read_file", root)
	}

	var b strings.Builder
	count := 0
	const maxEntries = 2000

	var walk func(dir string, level int, prefix string) error
	walk = func(dir string, level int, prefix string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].IsDir() != entries[j].IsDir() {
				return entries[i].IsDir()
			}
			return entries[i].Name() < entries[j].Name()
		})
		for _, e := range entries {
			if count >= maxEntries {
				return nil
			}
			name := e.Name()
			if !hidden && strings.HasPrefix(name, ".") {
				continue
			}
			if e.IsDir() {
				fmt.Fprintf(&b, "%s%s/\n", prefix, name)
				count++
				if level < depth {
					if err := walk(filepath.Join(dir, name), level+1, prefix+"  "); err != nil {
						fmt.Fprintf(&b, "%s  [нет доступа: %v]\n", prefix, err)
					}
				}
				continue
			}
			size := int64(0)
			if fi, err := e.Info(); err == nil {
				size = fi.Size()
			}
			fmt.Fprintf(&b, "%s%s (%s)\n", prefix, name, humanSize(size))
			count++
		}
		return nil
	}

	if err := walk(root, 1, ""); err != nil {
		return "", err
	}
	if count == 0 {
		return "(каталог пуст)", nil
	}
	if count >= maxEntries {
		fmt.Fprintf(&b, "\n[показаны первые %d записей]\n", maxEntries)
	}
	return opts.truncate(b.String()), nil
}

func humanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d Б", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f КБ", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f МБ", float64(n)/(1024*1024))
	}
}

// ── write_file ───────────────────────────────────────────────────────────────

type writeFileTool struct{ opts Options }

func (t *writeFileTool) Name() string { return NameWriteFile }

func (t *writeFileTool) Spec() ollama.Tool {
	return ollama.Tool{Type: "function", Function: ollama.ToolSpec{
		Name:        NameWriteFile,
		Description: "Создаёт файл или полностью заменяет его содержимое. Требует подтверждения пользователя.",
		Parameters: ollama.ToolParams{
			Type: "object",
			Properties: map[string]ollama.ToolProp{
				"path":    {Type: "string", Description: "Путь к файлу относительно рабочего каталога"},
				"content": {Type: "string", Description: "Новое содержимое файла целиком"},
			},
			Required: []string{"path", "content"},
		},
	}}
}

func (t *writeFileTool) Plan(args map[string]any) (*Plan, error) {
	raw, err := requireString(args, "path")
	if err != nil {
		return nil, err
	}
	content, ok := argString(args, "content")
	if !ok {
		return nil, fmt.Errorf("не указан обязательный параметр %q", "content")
	}
	abs, err := t.opts.Sandbox.Resolve(raw)
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > t.opts.Sandbox.MaxFileBytes() {
		return nil, fmt.Errorf("содержимое слишком велико: %d байт, предел %d (sandbox.max_file_kb)",
			len(content), t.opts.Sandbox.MaxFileBytes())
	}

	old := ""
	exists := false
	if data, err := os.ReadFile(abs); err == nil {
		old = string(data)
		exists = true
	}
	rel := t.opts.Sandbox.Rel(abs)
	added, removed := DiffStat(old, content)

	title := fmt.Sprintf("%s(%s) +%d -%d", NameWriteFile, rel, added, removed)
	if !exists {
		title = fmt.Sprintf("%s(%s) новый файл, строк: %d", NameWriteFile, rel, len(splitLines(content)))
	}

	return &Plan{
		Tool:    NameWriteFile,
		Req:     permissions.Request{Kind: permissions.KindWrite, Target: abs, Tool: NameWriteFile},
		Title:   title,
		Preview: UnifiedDiff(old, content, 3),
		Run: func(ctx context.Context) (string, error) {
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
				return "", err
			}
			if exists {
				return fmt.Sprintf("Файл %s перезаписан (добавлено строк: %d, удалено: %d).", rel, added, removed), nil
			}
			return fmt.Sprintf("Файл %s создан (%d строк).", rel, len(splitLines(content))), nil
		},
	}, nil
}

// ── edit_file ────────────────────────────────────────────────────────────────

type editFileTool struct{ opts Options }

func (t *editFileTool) Name() string { return NameEditFile }

func (t *editFileTool) Spec() ollama.Tool {
	return ollama.Tool{Type: "function", Function: ollama.ToolSpec{
		Name: NameEditFile,
		Description: "Точечно заменяет фрагмент текста в файле. Фрагмент old_string должен встречаться " +
			"в файле ровно один раз. Требует подтверждения пользователя.",
		Parameters: ollama.ToolParams{
			Type: "object",
			Properties: map[string]ollama.ToolProp{
				"path":       {Type: "string", Description: "Путь к файлу относительно рабочего каталога"},
				"old_string": {Type: "string", Description: "Заменяемый фрагмент, точно как в файле"},
				"new_string": {Type: "string", Description: "Текст замены"},
			},
			Required: []string{"path", "old_string", "new_string"},
		},
	}}
}

func (t *editFileTool) Plan(args map[string]any) (*Plan, error) {
	raw, err := requireString(args, "path")
	if err != nil {
		return nil, err
	}
	oldStr, err := requireString(args, "old_string")
	if err != nil {
		return nil, err
	}
	newStr, ok := argString(args, "new_string")
	if !ok {
		return nil, fmt.Errorf("не указан обязательный параметр %q", "new_string")
	}
	abs, err := t.opts.Sandbox.Resolve(raw)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	content := string(data)
	switch n := strings.Count(content, oldStr); n {
	case 1:
	case 0:
		return nil, fmt.Errorf("фрагмент не найден в файле %s", t.opts.Sandbox.Rel(abs))
	default:
		return nil, fmt.Errorf("фрагмент встречается в файле %s %d раз — уточните его, чтобы совпадение было единственным",
			t.opts.Sandbox.Rel(abs), n)
	}

	updated := strings.Replace(content, oldStr, newStr, 1)
	rel := t.opts.Sandbox.Rel(abs)
	added, removed := DiffStat(content, updated)

	return &Plan{
		Tool:    NameEditFile,
		Req:     permissions.Request{Kind: permissions.KindWrite, Target: abs, Tool: NameEditFile},
		Title:   fmt.Sprintf("%s(%s) +%d -%d", NameEditFile, rel, added, removed),
		Preview: UnifiedDiff(content, updated, 3),
		Run: func(ctx context.Context) (string, error) {
			info, err := os.Stat(abs)
			mode := fs.FileMode(0o644)
			if err == nil {
				mode = info.Mode().Perm()
			}
			if err := os.WriteFile(abs, []byte(updated), mode); err != nil {
				return "", err
			}
			return fmt.Sprintf("Файл %s изменён (добавлено строк: %d, удалено: %d).", rel, added, removed), nil
		},
	}, nil
}

// ── grep ─────────────────────────────────────────────────────────────────────

type grepTool struct{ opts Options }

func (t *grepTool) Name() string { return NameGrep }

func (t *grepTool) Spec() ollama.Tool {
	return ollama.Tool{Type: "function", Function: ollama.ToolSpec{
		Name:        NameGrep,
		Description: "Ищет регулярное выражение в файлах рабочего каталога и возвращает совпавшие строки.",
		Parameters: ollama.ToolParams{
			Type: "object",
			Properties: map[string]ollama.ToolProp{
				"pattern": {Type: "string", Description: "Регулярное выражение в синтаксисе Go (RE2)"},
				"path":    {Type: "string", Description: "Каталог или файл для поиска (по умолчанию — весь рабочий каталог)"},
				"glob":    {Type: "string", Description: "Фильтр имён файлов, например *.go"},
				"limit":   {Type: "integer", Description: "Предел числа совпадений, по умолчанию 200"},
			},
			Required: []string{"pattern"},
		},
	}}
}

func (t *grepTool) Plan(args map[string]any) (*Plan, error) {
	pattern, err := requireString(args, "pattern")
	if err != nil {
		return nil, err
	}
	re, err := compileSearch(pattern)
	if err != nil {
		return nil, err
	}
	raw, ok := argString(args, "path")
	if !ok || strings.TrimSpace(raw) == "" {
		raw = "."
	}
	abs, err := t.opts.Sandbox.Resolve(raw)
	if err != nil {
		return nil, err
	}
	glob, _ := argString(args, "glob")
	limit := argInt(args, "limit", 200)
	if limit <= 0 {
		limit = 200
	}
	rel := t.opts.Sandbox.Rel(abs)

	title := fmt.Sprintf("%s(%q в %s)", NameGrep, pattern, rel)
	if glob != "" {
		title = fmt.Sprintf("%s(%q в %s, %s)", NameGrep, pattern, rel, glob)
	}

	return &Plan{
		Tool:  NameGrep,
		Req:   permissions.Request{Kind: permissions.KindRead, Target: abs, Tool: NameGrep},
		Title: title,
		Run: func(ctx context.Context) (string, error) {
			return grepSearch(ctx, abs, re, glob, limit, t.opts)
		},
	}, nil
}

func grepSearch(ctx context.Context, root string, re *searchRE, glob string, limit int, opts Options) (string, error) {
	var b strings.Builder
	matches := 0

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // нечитаемые каталоги просто пропускаем
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", ".cache":
				if p != root {
					return fs.SkipDir
				}
			}
			return nil
		}
		if matches >= limit {
			return fs.SkipAll
		}
		if glob != "" {
			ok, _ := filepath.Match(glob, d.Name())
			if !ok {
				return nil
			}
		}
		info, err := d.Info()
		if err != nil || info.Size() > opts.Sandbox.MaxFileBytes() {
			return nil
		}

		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()

		rel := opts.Sandbox.Rel(p)
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		line := 0
		for sc.Scan() {
			line++
			text := sc.Text()
			if !re.MatchString(text) {
				continue
			}
			if len(text) > 400 {
				text = text[:400] + "…"
			}
			fmt.Fprintf(&b, "%s:%d: %s\n", rel, line, text)
			matches++
			if matches >= limit {
				return fs.SkipAll
			}
		}
		return nil
	})
	if err != nil && ctx.Err() != nil {
		return "", ctx.Err()
	}

	if matches == 0 {
		return "Совпадений не найдено.", nil
	}
	if matches >= limit {
		fmt.Fprintf(&b, "\n[показаны первые %d совпадений]\n", limit)
	}
	return opts.truncate(b.String()), nil
}
