package ui

import (
	"strings"
)

// Панель вложенных изображений: F3 открывает и закрывает её, стрелки выбирают,
// Del или Ctrl+D убирает картинку. Показывает то, что действительно приложено
// к текущему вопросу, а не то, что когда-то вставляли.
//
// Стоит там же, где список файлов по «@», — над разделителем, и по тем же
// причинам: панель относится к тому, что показано выше, и не должна разрывать
// поле ввода. Обе панели взаимно исключают друг друга: две сразу сжали бы ленту
// до нечитаемого огрызка.

// maxImageRows — сколько строк панели видно одновременно.
const maxImageRows = 5

// imagePanel — состояние панели вложений. Сам список живёт в Model.pending:
// панель только показывает его, поэтому расходиться им негде.
type imagePanel struct {
	listCursor
}

// height — высота панели вместе с рамкой и заголовком.
func (p *imagePanel) height(count int) int {
	rows := count
	if rows > maxImageRows {
		rows = maxImageRows
	}
	if rows == 0 {
		rows = 1 // строка «вложений нет»
	}
	return rows + 3 // рамка (2) + заголовок
}

// clampCursor удерживает выбор внутри списка после удаления строки.

// view рисует панель вложений.
func (p *imagePanel) move(delta, count int) { p.step(delta, count, maxImageRows) }
func (p *imagePanel) clampCursor(count int) { p.clamp(count, maxImageRows) }

func (p *imagePanel) view(width int, images []pendingImage) string {
	title := "вложения — Del удалить, F3 закрыть"
	if len(images) > maxImageRows {
		title = strings.Replace(title, "вложения",
			"вложения "+itoa(p.cursor+1)+"/"+itoa(len(images)), 1)
	}

	var b strings.Builder
	b.WriteString(styPickerTitle.Render(title) + "\n")

	inner := panelInner(width)

	if len(images) == 0 {
		b.WriteString(styPickerHint.Render("вложений нет — вставить картинку можно по Ctrl+V"))
		return styPickerBox.Width(width - 2).Render(b.String())
	}

	end := p.offset + maxImageRows
	if end > len(images) {
		end = len(images)
	}
	rows := make([]string, 0, end-p.offset)
	for i := p.offset; i < end; i++ {
		img := images[i]
		style, marker := styPickerName, "  "
		if i == p.cursor {
			style, marker = styPickerNameSel, styPickerMarker.Render("▸ ")
		}
		line := img.label() + "  " + img.describe()
		rows = append(rows, clip(marker+style.Render(truncateLine(line, inner)), inner+2))
	}
	b.WriteString(strings.Join(rows, "\n"))

	return styPickerBox.Width(width - 2).Render(b.String())
}

// ── Связь с моделью ──────────────────────────────────────────────────────────

// imagesHeight — сколько строк занимает панель вложений сейчас.
func (m *Model) imagesHeight() int {
	if m.images == nil {
		return 0
	}
	return m.images.height(len(m.pending))
}

// toggleImagePanel открывает и закрывает панель по F3.
func (m *Model) toggleImagePanel() {
	if m.images != nil {
		m.closeImagePanel()
		return
	}
	// Две панели над разделителем сразу не показываем.
	m.files, m.findPane = nil, nil
	m.images = &imagePanel{}
	m.images.clampCursor(len(m.pending))
	m.relayout()
}

// closeImagePanel убирает панель и возвращает ленте отданные ей строки.
func (m *Model) closeImagePanel() {
	if m.images == nil {
		return
	}
	m.images = nil
	m.relayout()
}

// handleImagePanelKey обрабатывает клавиши, пока панель открыта.
// Второе значение — взяли ли клавишу себе.
func (m *Model) handleImagePanelKey(key string) bool {
	switch key {
	case "up", "ctrl+p":
		m.images.move(-1, len(m.pending))
		return true
	case "down", "ctrl+n":
		m.images.move(1, len(m.pending))
		return true
	// Ctrl+D здесь безопасен: это отдельный байт, с другими клавишами он не
	// совпадает, а прокрутка ленты на полстраницы при открытой панели не нужна.
	case "delete", "ctrl+d":
		m.deleteSelectedImage()
		return true
	case "esc", "f3":
		m.closeImagePanel()
		return true
	}
	return false
}

// deleteSelectedImage убирает выбранное вложение вместе с его меткой в промпте.
//
// Метку убираем обязательно: иначе в вопросе останется «[Image01]», за которым
// уже ничего нет, и модель получит ссылку в пустоту.
func (m *Model) deleteSelectedImage() {
	if m.images == nil || len(m.pending) == 0 {
		return
	}
	idx := m.images.cursor
	if idx < 0 || idx >= len(m.pending) {
		return
	}

	label := m.pending[idx].label()
	m.ta.SetValue(stripLabel(m.ta.Value(), label))
	m.ta.CursorEnd()

	// Дальше метки в промпте нет, и общая уборка сама выбросит вложение
	// из списка, поправит выбор и пересчитает высоту панели.
	m.syncPendingImages()
	m.syncFileMenu()
}

// stripLabel убирает метку из текста вместе с одним пробелом после неё,
// чтобы на месте вложения не оставалось двойных пробелов.
func stripLabel(text, label string) string {
	if strings.Contains(text, label+" ") {
		return strings.Replace(text, label+" ", "", 1)
	}
	return strings.Replace(text, label, "", 1)
}

// imagesStatus — что показать о вложениях в строке состояния.
func (m *Model) imagesStatus() string {
	switch len(m.pending) {
	case 0:
		return ""
	case 1:
		return m.pending[0].label() + " — " + m.pending[0].describe()
	default:
		return "[Images attached]"
	}
}
