package ui

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"unicode"
)

// Выбор файла по «@» — по образцу Claude Code: набрал @, над полем ввода
// появился список рабочего каталога, стрелки выбирают, Enter вставляет путь.
//
// Набранный текст при этом идёт в поле ввода как обычно: список читает из
// промпта последний токен, начинающийся с @, и по нему же фильтрует. Поэтому
// продолжать печатать можно не задумываясь о том, открыт список или нет.
//
// Список ограничен песочницей: показывается только то, что лежит внутри
// рабочего каталога. Путь наружу и показать нельзя, и приложить потом нечем.

// maxFileMenuRows — сколько строк списка видно одновременно. Остальное
// прокручивается стрелками и колесом мыши.
const maxFileMenuRows = 5

// fileEntry — одна строка списка.
type fileEntry struct {
	name  string // имя внутри каталога
	rel   string // путь относительно корня песочницы — он и вставляется в промпт
	isDir bool
}

// fileMenu — список файлов под текущий токен «@…».
type fileMenu struct {
	token   string // токен вместе с @, например "@internal/u"
	dir     string // каталог, содержимое которого показано (относительно корня)
	entries []fileEntry
	listCursor
}

// height — высота панели вместе с рамкой и заголовком.
func (f *fileMenu) height() int {
	rows := len(f.entries)
	if rows > maxFileMenuRows {
		rows = maxFileMenuRows
	}
	if rows == 0 {
		rows = 1 // строка «ничего не найдено»
	}
	return rows + 3 // рамка (2) + заголовок
}

// selected возвращает выбранную строку.
// move двигает выбор по кругу.
func (f *fileMenu) move(delta int) { f.step(delta, len(f.entries), maxFileMenuRows) }

func (f *fileMenu) selected() (fileEntry, bool) {
	if f.cursor < 0 || f.cursor >= len(f.entries) {
		return fileEntry{}, false
	}
	return f.entries[f.cursor], true
}

// view рисует список над разделителем.
func (f *fileMenu) view(width int) string {
	title := "файлы: " + f.dir
	if f.dir == "" {
		title = "файлы рабочего каталога"
	}
	// Счётчик встроен в заголовок, а не в отдельную строку: так высота панели
	// не пляшет между длинным и коротким списком.
	if len(f.entries) > maxFileMenuRows {
		title += fmt.Sprintf("  %d/%d  ↑↓ или колесо", f.cursor+1, len(f.entries))
	}

	var b strings.Builder
	b.WriteString(styPickerTitle.Render(title) + "\n")

	inner := panelInner(width)

	if len(f.entries) == 0 {
		b.WriteString(styPickerHint.Render("ничего не найдено"))
		return styPickerBox.Width(width - 2).Render(b.String())
	}

	end := f.offset + maxFileMenuRows
	if end > len(f.entries) {
		end = len(f.entries)
	}
	rows := make([]string, 0, end-f.offset)
	for i := f.offset; i < end; i++ {
		e := f.entries[i]
		name := e.name
		if e.isDir {
			name += "/"
		}

		style := styPickerName
		marker := "  "
		if i == f.cursor {
			style, marker = styPickerNameSel, styPickerMarker.Render("▸ ")
		}
		rows = append(rows, clip(marker+style.Render(truncateLine(name, inner)), inner+2))
	}
	b.WriteString(strings.Join(rows, "\n"))

	return styPickerBox.Width(width - 2).Render(b.String())
}

// ── Связь со строкой ввода ───────────────────────────────────────────────────

// lastAtToken возвращает последний токен вида «@…» в конце текста.
//
// Токен обязан стоять в конце строки и не содержать пробелов: пока
// пользователь его набирает, список открыт, а как только он поставил пробел
// или ушёл писать дальше — список закрывается. Это осознанное упрощение:
// правка «@пути» в середине уже написанного вопроса списком не сопровождается.
func lastAtToken(s string) (string, bool) {
	if s == "" {
		return "", false
	}
	runes := []rune(s)
	for i := len(runes) - 1; i >= 0; i-- {
		r := runes[i]
		if unicode.IsSpace(r) {
			return "", false // между @ и концом строки появился пробел
		}
		if r == '@' {
			// Перед @ должно быть начало строки или пробел, иначе это часть
			// слова: почта, ссылка, что угодно.
			if i > 0 && !unicode.IsSpace(runes[i-1]) {
				return "", false
			}
			return string(runes[i:]), true
		}
	}
	return "", false
}

// splitToken делит «@internal/u» на каталог "internal" и начало имени "u".
func splitToken(token string) (dir, prefix string) {
	body := strings.TrimPrefix(token, "@")
	idx := strings.LastIndex(body, "/")
	if idx < 0 {
		return "", body
	}
	return body[:idx], body[idx+1:]
}

// syncFileMenu открывает, обновляет или закрывает список по текущему промпту.
// Вызывается после каждого изменения строки ввода.
func (m *Model) syncFileMenu() {
	token, ok := lastAtToken(m.ta.Value())
	if !ok {
		m.closeFileMenu()
		return
	}
	if m.files != nil && m.files.token == token {
		return // список уже собран под этот токен
	}

	dir, prefix := splitToken(token)
	entries := m.listDir(dir, prefix)

	m.findPane = nil // две панели над разделителем сразу не показываем
	before := m.filesHeight()
	m.files = &fileMenu{token: token, dir: dir, entries: entries}
	if m.filesHeight() != before {
		m.relayout()
	}
}

// filesHeight — сколько строк занимает список файлов сейчас.
func (m *Model) filesHeight() int {
	if m.files == nil {
		return 0
	}
	return m.files.height()
}

// closeFileMenu убирает список и возвращает ленте отданные ему строки.
func (m *Model) closeFileMenu() {
	if m.files == nil {
		return
	}
	m.files = nil
	m.relayout()
}

// listDir читает каталог внутри песочницы и отбирает подходящие имена.
func (m *Model) listDir(dir, prefix string) []fileEntry {
	sb := m.guard.Sandbox()
	abs, err := sb.Resolve(dirRelativeToRoot(dir))
	if err != nil {
		return nil // каталога нет или он вне песочницы — показывать нечего
	}
	items, err := os.ReadDir(abs)
	if err != nil {
		return nil
	}

	lower := strings.ToLower(prefix)
	out := make([]fileEntry, 0, len(items))
	for _, it := range items {
		name := it.Name()
		// Скрытые файлы показываем только когда их спрашивают явно: иначе
		// список рабочего каталога начинается с .git и прочего служебного.
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(prefix, ".") {
			continue
		}
		if lower != "" && !strings.HasPrefix(strings.ToLower(name), lower) {
			continue
		}
		out = append(out, fileEntry{
			name:  name,
			rel:   path.Join(dir, name),
			isDir: it.IsDir(),
		})
	}

	// Каталоги выше файлов: по ним идут вглубь, а значит к ним обращаются чаще.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].isDir != out[j].isDir {
			return out[i].isDir
		}
		return strings.ToLower(out[i].name) < strings.ToLower(out[j].name)
	})
	return out
}

// dirRelativeToRoot переводит пустой каталог в «.», понятный песочнице.
func dirRelativeToRoot(dir string) string {
	if dir == "" {
		return "."
	}
	return dir
}

// acceptFileMenu подставляет выбранный путь вместо токена «@…».
//
// Для каталога список остаётся открытым и показывает его содержимое — так
// путь набирается вглубь без повторного «@».
func (m *Model) acceptFileMenu() {
	if m.files == nil {
		return
	}
	entry, ok := m.files.selected()
	if !ok {
		return
	}

	value := m.ta.Value()
	cut := strings.LastIndex(value, m.files.token)
	if cut < 0 {
		m.closeFileMenu()
		return
	}

	replacement := "@" + entry.rel
	if entry.isDir {
		replacement += "/"
	} else {
		replacement += " "
	}

	m.ta.SetValue(value[:cut] + replacement)
	m.ta.CursorEnd()
	m.syncFileMenu()
}
