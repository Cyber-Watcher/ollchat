package ui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/itpro/ollchat/internal/chatlog"
	"github.com/itpro/ollchat/internal/document"
	"github.com/itpro/ollchat/internal/permissions"
)

// Рисунки из документов PDF.
//
// Текст документа прикладывается командой /add и содержит метки вида
// «[рисунок 3: 804×551]» — по ним видно, где на странице картинка. Саму
// картинку модель увидеть не может, пока её не приложат: /addimg достаёт
// рисунки из документа и вкладывает их в вопрос обычными вложениями,
// теми же, что и вставка из буфера обмена.
//
// Отрисовать страницу целиком приложение не умеет — это отдельный движок
// вывода. Достаются только те картинки, что вложены в документ: схемы,
// диаграммы, снимки экрана, а у сканов — страница целиком.

// maxPDFImages ограничивает разовую выгрузку: в книге картинок сотни, а каждая
// уходит модели в base64 и занимает контекстное окно.
const maxPDFImages = 12

// minPDFImagePx отсекает мелочь: подложки, линейки, значки и прочее убранство
// вёрстки смысла не несут, а вложения тратят.
const minPDFImagePx = 150

// addImagesCmd разбирает «/addimg путь [страницы]» и прикладывает рисунки.
func (m *Model) addImagesCmd(arg string) tea.Cmd {
	path, spec := splitPathSpec(arg)
	if path == "" {
		m.addBlock(block{kind: blockError, text: "использование: /addimg путь/к/документу [страницы или разделы]\n" +
			"  примеры: /addimg книга.pdf 44      — рисунки со страницы 44\n" +
			"           /addimg книга.pdf 40-60   — со страниц 40…60\n" +
			"           /addimg книга.epub 8      — рисунки из раздела 8 книги\n" +
			"           /addimg книга.pdf         — с начала документа"})
		return nil
	}
	abs, err := m.guard.Sandbox().Resolve(path)
	if err != nil {
		m.addBlock(block{kind: blockError, text: err.Error()})
		return nil
	}
	res := m.guard.Check(permissions.Request{Kind: permissions.KindRead, Target: abs, Tool: "addimg"})
	if res.Decision == permissions.DecisionDeny {
		m.addBlock(block{kind: blockError, text: "чтение запрещено: " + res.Reason})
		return nil
	}
	if document.DetectFile(abs) == document.KindNone {
		m.addBlock(block{kind: blockError, text: "это не документ PDF и не книга EPUB: " + path})
		return nil
	}

	first, count, err := parsePageSpec(spec)
	if err != nil {
		m.addBlock(block{kind: blockError, text: "/addimg: " + err.Error()})
		return nil
	}

	free := maxPDFImages - len(m.pending)
	if free <= 0 {
		m.addBlock(block{kind: blockError, text: fmt.Sprintf(
			"к вопросу уже приложено %d картинок — больше не помещается", len(m.pending))})
		return nil
	}
	images, err := document.Images(abs, m.guard.Sandbox().MaxPDFBytes(), document.ImageOptions{
		First: first, Count: count,
		MinWidth: minPDFImagePx, MinHeight: minPDFImagePx,
		MaxCount: free,
	})
	if err != nil {
		m.addBlock(block{kind: blockError, text: "разбор документа: " + err.Error()})
		return nil
	}
	if len(images) == 0 {
		m.addBlock(block{kind: blockNotice, text: "в этой части документа рисунков не нашлось " +
			"(мелкие элементы вёрстки не считаются)"})
		return nil
	}

	rel := m.guard.Sandbox().Rel(abs)
	before := m.imagesHeight()
	var labels []string
	for _, im := range images {
		p := pendingImage{
			num:  m.nextImageNum(),
			data: im.Data,
			mime: "image/" + im.Format,
			w:    im.Width,
			h:    im.Height,
		}
		m.pending = append(m.pending, p)
		m.ta.InsertString(p.label() + " ")
		labels = append(labels, fmt.Sprintf("%s = рисунок %s", p.label(), im.Label))
	}
	if m.images != nil && m.imagesHeight() != before {
		m.relayout()
	}

	m.addBlock(block{kind: blockNotice, text: fmt.Sprintf(
		"приложено рисунков из %s: %d\n  %s", rel, len(images), strings.Join(labels, "\n  "))})
	_ = m.logger.Write(chatlog.KindSystem, fmt.Sprintf("К вопросу приложены рисунки из %s: %d", rel, len(images)))

	if m.visionUnsupported() {
		m.addBlock(block{kind: blockNotice, text: fmt.Sprintf(
			"модель %s не умеет смотреть изображения — выберите модель с возможностью vision (Ctrl+R)",
			m.modelName)})
	}
	return nil
}

// splitPathSpec отделяет от аргумента хвост с указанием страниц. Путь может
// содержать пробелы, поэтому отрезается только хвост, похожий на номера.
func splitPathSpec(arg string) (path, spec string) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", ""
	}
	i := strings.LastIndex(arg, " ")
	if i < 0 {
		return arg, ""
	}
	tail := strings.TrimSpace(arg[i+1:])
	if tail != "" && strings.IndexFunc(tail, func(r rune) bool {
		return !(r >= '0' && r <= '9') && r != '-'
	}) < 0 {
		return strings.TrimSpace(arg[:i]), tail
	}
	return arg, ""
}

// parsePageSpec разбирает «44» или «40-60» в начальную единицу и их число.
// Единица — страница у PDF и раздел у книги.
func parsePageSpec(spec string) (first, count int, err error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return 1, 0, nil
	}
	from, to, ok := strings.Cut(spec, "-")
	first, err = strconv.Atoi(strings.TrimSpace(from))
	if err != nil || first < 1 {
		return 0, 0, fmt.Errorf("непонятный номер страницы: %q", spec)
	}
	if !ok {
		return first, 1, nil
	}
	last, err := strconv.Atoi(strings.TrimSpace(to))
	if err != nil || last < first {
		return 0, 0, fmt.Errorf("непонятный диапазон страниц: %q", spec)
	}
	return first, last - first + 1, nil
}
