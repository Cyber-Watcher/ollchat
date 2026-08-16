package ui

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/clipboard"
)

// Изображения прикладываются к вопросу по образцу Claude Code: вставка из
// буфера обмена подставляет в промпт метку [Image01], дальше можно писать
// текст как обычно, а при отправке метки превращаются в само изображение —
// оно уходит модели в поле images сообщения Ollama.
//
// Метка, а не картинка, стоит в тексте по двум причинам: в терминале картинку
// не нарисовать, и на неё удобно ссылаться словами («что написано на [Image01]»).

// maxImageBytes ограничивает размер вложения: картинка едет в base64 внутри
// JSON, и лишние мегабайты бьют и по трафику, и по контекстному окну.
const maxImageBytes = 20 << 20

// imageRef находит метки вида [Image01] в тексте вопроса.
var imageRef = regexp.MustCompile(`\[Image(\d+)\]`)

// pendingImage — изображение, приложенное к ещё не отправленному вопросу.
type pendingImage struct {
	num  int
	data []byte
	mime string
	w, h int
}

// label — метка, которую видно в промпте.
func (p pendingImage) label() string { return fmt.Sprintf("[Image%02d]", p.num) }

// describe — краткое описание для статус-бара и журнала.
func (p pendingImage) describe() string {
	format := strings.ToUpper(strings.TrimPrefix(p.mime, "image/"))
	return fmt.Sprintf("%s %d×%d, %s", format, p.w, p.h, humanSize(len(p.data)))
}

// base64 — изображение в том виде, в каком его ждёт Ollama.
func (p pendingImage) base64() string { return base64.StdEncoding.EncodeToString(p.data) }

// imagePastedMsg — результат чтения буфера обмена.
type imagePastedMsg struct {
	img *clipboard.Image
	err error
}

// pasteImageCmd читает буфер обмена в отдельной горутине: обращение к внешней
// утилите нельзя делать прямо в цикле событий.
func pasteImageCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := contextWithTimeout(5)
		defer cancel()
		img, err := clipboard.ReadImage(ctx, maxImageBytes)
		return imagePastedMsg{img: img, err: err}
	}
}

// handleImagePasted прикладывает прочитанное изображение к текущему вопросу.
func (m *Model) handleImagePasted(msg imagePastedMsg) {
	if msg.err != nil {
		m.statusMsg = ""
		m.addBlock(block{kind: blockError, text: "вставка изображения: " + msg.err.Error()})
		return
	}
	// Ни картинки, ни ошибки быть не может, но разыменование nil обошлось бы
	// пользователю в потерянный сеанс — проверить дешевле.
	if msg.img == nil {
		m.statusMsg = ""
		m.addBlock(block{kind: blockError, text: "вставка изображения: пустой ответ буфера обмена"})
		return
	}

	p := pendingImage{
		num:  m.nextImageNum(),
		data: msg.img.Data,
		mime: msg.img.MIME,
		w:    msg.img.Width,
		h:    msg.img.Height,
	}
	before := m.imagesHeight()
	m.pending = append(m.pending, p)
	m.ta.InsertString(p.label() + " ")
	// О вложении рассказывает постоянная пометка в строке состояния,
	// поэтому отдельным сообщением не дублируем — а «читаю буфер обмена…»
	// пора убрать, иначе оно останется висеть до следующего сообщения.
	m.statusMsg = ""
	if m.images != nil && m.imagesHeight() != before {
		m.relayout()
	}

	// Предупреждаем сразу, а не при отправке: модель можно сменить, не потеряв
	// ни вставленную картинку, ни набранный текст.
	if m.visionUnsupported() {
		m.addBlock(block{kind: blockNotice, text: fmt.Sprintf(
			"модель %s не умеет смотреть изображения — выберите модель с возможностью vision (Ctrl+R)",
			m.modelName)})
	}
}

// visionUnsupported сообщает, что модель **точно** не умеет смотреть картинки.
//
// Пока список возможностей не получен от сервера, мы этого не знаем — и молчим:
// отказать из-за незнания хуже, чем дать серверу ответить самому. Так бывает
// в первые секунды после запуска и сразу после смены модели.
func (m *Model) visionUnsupported() bool {
	return len(m.modelCaps) > 0 && !hasCap(m.modelCaps, "vision")
}

// nextImageNum выдаёт номер для очередной картинки текущего вопроса.
// Нумерация своя у каждого вопроса и начинается заново после отправки.
func (m *Model) nextImageNum() int {
	next := 1
	for _, p := range m.pending {
		if p.num >= next {
			next = p.num + 1
		}
	}
	return next
}

// imagesFor собирает картинки, на которые ссылается текст вопроса, в порядке
// появления меток. Метку могли стереть — тогда картинка не отправляется:
// в промпте её нет, значит пользователь передумал.
func (m *Model) imagesFor(text string) []pendingImage {
	if len(m.pending) == 0 {
		return nil
	}
	byNum := make(map[int]pendingImage, len(m.pending))
	for _, p := range m.pending {
		byNum[p.num] = p
	}

	seen := make(map[int]bool, len(m.pending))
	out := make([]pendingImage, 0, len(m.pending))
	for _, match := range imageRef.FindAllStringSubmatch(text, -1) {
		n, err := strconv.Atoi(match[1])
		if err != nil || seen[n] {
			continue
		}
		p, ok := byNum[n]
		if !ok {
			continue // метка есть, а картинки за ней нет — просто текст
		}
		seen[n] = true
		out = append(out, p)
	}
	return out
}

// dropPendingImages забывает приложенные картинки — после отправки и по /clear.
func (m *Model) dropPendingImages() {
	m.pending = nil
	m.closeImagePanel()
}

// syncPendingImages выбрасывает вложения, метки которых исчезли из промпта.
//
// Стёртая метка — это отказ от картинки: иначе она продолжала бы висеть
// в строке состояния и уехала бы к модели при следующей отправке, хотя
// в вопросе о ней уже нет ни слова.
func (m *Model) syncPendingImages() {
	if len(m.pending) == 0 {
		return
	}
	text := m.ta.Value()

	kept := make([]pendingImage, 0, len(m.pending))
	for _, p := range m.pending {
		if strings.Contains(text, p.label()) {
			kept = append(kept, p)
		}
	}
	if len(kept) == len(m.pending) {
		return
	}

	before := m.imagesHeight()
	m.pending = kept
	if len(m.pending) == 0 {
		m.closeImagePanel()
	} else if m.images != nil {
		m.images.clampCursor(len(m.pending))
	}
	if m.imagesHeight() != before {
		m.relayout()
	}
}

// humanSize переводит размер в килобайты и мегабайты.
func humanSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f МБ", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d КБ", n/(1<<10))
	default:
		return fmt.Sprintf("%d Б", n)
	}
}
