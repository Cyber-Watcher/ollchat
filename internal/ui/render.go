package ui

import (
	"os"
	"strings"
	"time"

	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
)

// blockKind — вид блока в ленте диалога.
type blockKind int

const (
	blockUser blockKind = iota
	blockAssistant
	blockThinking
	blockTool
	blockNotice
	blockHint
	blockError
)

// block — одна запись ленты диалога.
type block struct {
	kind    blockKind
	text    string // основной текст
	title   string // заголовок для инструментов
	status  string // ok | fail | skip — для инструментов
	preview string // вывод инструмента, показывается свёрнутым

	// at и model нужны копированию в буфер обмена (Shift+F5): оно собирает
	// запись в формате журнала, а у прокрученного вверх старого ответа взять
	// отметку времени и название модели больше неоткуда.
	at    time.Time
	model string
}

// renderer превращает блоки в текст для области истории.
type renderer struct {
	width    int
	markdown bool
	style    string // имя стиля glamour: dark или light
	glam     *glamour.TermRenderer
}

// detectStyle определяет тему оформления по цвету фона терминала.
//
// Вызывать эту функцию можно только до запуска цикла событий Bubble Tea:
// определение темы отправляет терминалу запрос и ждёт ответа, а после старта
// программы ответ перехватит обработчик ввода Bubble Tea — цикл событий
// заблокируется, и интерфейс останется на заставке.
func detectStyle() string {
	if lipgloss.HasDarkBackground(os.Stdin, os.Stdout) {
		return "dark"
	}
	return "light"
}

func newRenderer(width int, markdown bool, style string) *renderer {
	if style == "" {
		style = "dark"
	}
	r := &renderer{markdown: markdown, style: style}
	r.setWidth(width)
	return r
}

func (r *renderer) setWidth(w int) {
	if w < 20 {
		w = 20
	}
	if r.width == w && r.glam != nil {
		return
	}
	r.width = w
	if !r.markdown {
		r.glam = nil
		return
	}
	// Стиль задаётся явно, без обращения к терминалу: этот код выполняется
	// внутри цикла событий, и любой запрос к терминалу его заблокирует.
	g, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(r.style),
		glamour.WithWordWrap(w),
		glamour.WithEmoji(),
	)
	if err != nil {
		// Ошибка построения рендерера markdown не должна ломать приложение:
		// в этом случае текст показывается как есть.
		r.glam = nil
		return
	}
	r.glam = g
}

func (r *renderer) setMarkdown(on bool) {
	r.markdown = on
	w := r.width
	r.width = 0 // заставляем пересоздать рендерер
	r.setWidth(w)
}

// renderMarkdown форматирует текст ответа модели.
func (r *renderer) renderMarkdown(s string) string {
	if r.glam == nil || strings.TrimSpace(s) == "" {
		return wrap(s, r.width)
	}
	out, err := r.glam.Render(s)
	if err != nil {
		return wrap(s, r.width)
	}
	return strings.Trim(out, "\n")
}

// Render превращает блок в готовый к показу текст.
func (r *renderer) Render(b block, showThinking bool) string {
	switch b.kind {
	case blockUser:
		head := styUserPrefix.Render("› ")
		body := wrap(b.text, r.width-2)
		return head + indentRest(styUserText.Render(body), 2)

	case blockAssistant:
		if strings.TrimSpace(b.text) == "" {
			return ""
		}
		return r.renderMarkdown(b.text)

	case blockThinking:
		if !showThinking || strings.TrimSpace(b.text) == "" {
			return ""
		}
		body := wrap(b.text, r.width-2)
		return styThinking.Render("┆ " + indentRest(body, 2))

	case blockTool:
		return r.renderTool(b)

	case blockNotice:
		return styNotice.Render(wrap(b.text, r.width))

	case blockHint:
		return styHint.Render("△ " + indentRest(wrap(b.text, r.width-2), 2))

	case blockError:
		return styError.Render("✗ " + wrap(b.text, r.width-2))
	}
	return ""
}

func (r *renderer) renderTool(b block) string {
	mark, style := "⏺", styTool
	switch b.status {
	case "ok":
		mark, style = "⏺", styToolOK
	case "fail":
		mark, style = "⏺", styToolFail
	case "skip":
		mark, style = "⊘", styToolSkip
	}

	var sb strings.Builder
	sb.WriteString(style.Render(mark+" ") + styTool.Render(b.title))
	if b.preview != "" {
		sb.WriteString("\n")
		sb.WriteString(styToolOutput.Render(indentAll(previewLines(b.preview, 12, r.width-4), 2)))
	}
	return sb.String()
}

// renderDiff подкрашивает строки предпросмотра правки.
func renderDiff(s string, width int) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		trimmed := truncateLine(l, width)
		switch {
		case strings.HasPrefix(l, "+"):
			out = append(out, styDiffAdd.Render(trimmed))
		case strings.HasPrefix(l, "-"):
			out = append(out, styDiffDel.Render(trimmed))
		case strings.HasPrefix(strings.TrimSpace(l), "…"):
			out = append(out, styDiffMeta.Render(trimmed))
		default:
			out = append(out, styDiffMeta.Render(trimmed))
		}
	}
	return strings.Join(out, "\n")
}

// previewLines оставляет от вывода инструмента не более maxLines строк.
func previewLines(s string, maxLines, width int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	trimmedCount := 0
	if len(lines) > maxLines {
		trimmedCount = len(lines) - maxLines
		lines = lines[:maxLines]
	}
	for i, l := range lines {
		lines[i] = truncateLine(l, width)
	}
	if trimmedCount > 0 {
		lines = append(lines, "… ещё строк: "+itoa(trimmedCount))
	}
	return strings.Join(lines, "\n")
}

func truncateLine(s string, width int) string {
	if width < 8 {
		width = 8
	}
	r := []rune(strings.ReplaceAll(s, "\t", "    "))
	if len(r) <= width {
		return string(r)
	}
	return string(r[:width-1]) + "…"
}

// wrap переносит текст по словам на заданную ширину.
func wrap(s string, width int) string {
	if width < 10 {
		width = 10
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		if para == "" {
			out = append(out, "")
			continue
		}
		line := strings.Builder{}
		lineLen := 0
		for _, word := range strings.Fields(para) {
			wl := lipgloss.Width(word)
			switch {
			case lineLen == 0:
				line.WriteString(word)
				lineLen = wl
			case lineLen+1+wl <= width:
				line.WriteString(" " + word)
				lineLen += 1 + wl
			default:
				out = append(out, line.String())
				line.Reset()
				line.WriteString(word)
				lineLen = wl
			}
		}
		out = append(out, line.String())
	}
	return strings.Join(out, "\n")
}

// indentRest добавляет отступ всем строкам, кроме первой.
func indentRest(s string, n int) string {
	lines := strings.Split(s, "\n")
	pad := strings.Repeat(" ", n)
	for i := 1; i < len(lines); i++ {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}

// indentAll добавляет отступ всем строкам.
func indentAll(s string, n int) string {
	pad := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
