package ui

import (
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/Cyber-Watcher/ollchat/internal/fsx"
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
	return fmt.Sprintf("%s %d×%d, %s", format, p.w, p.h, fsx.HumanSize(int64(len(p.data))))
}

// base64 — изображение в том виде, в каком его ждёт Ollama.
func (p pendingImage) base64() string { return base64.StdEncoding.EncodeToString(p.data) }

// imagePastedMsg — результат чтения буфера обмена.
type imagePastedMsg struct {
	img *clipboard.Image
	err error
}

// clipboardRead — шов для тестов: подменяя его, тест проверяет поведение
// при отказе буфера обмена, не завися от того, что творится на машине.
var clipboardRead = clipboard.ReadImage

// pasteImageCmd читает буфер обмена в отдельной горутине: обращение к внешней
// утилите нельзя делать прямо в цикле событий.
func pasteImageCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := contextWithTimeout(5)
		defer cancel()
		img, err := clipboardRead(ctx, maxImageBytes)
		return imagePastedMsg{img: img, err: err}
	}
}

// sshPasteHint объясняет, что делать, когда графической сессии нет.
//
// Отказ здесь ожидаем и не является ошибкой пользователя: он запустил ollchat
// по SSH, а буфер обмена остался на его машине. Забрать его через терминал
// нельзя — OSC 52 передаёт только текст, изображения им не вытащить, — зато
// работает проброс X11: тогда локальный xclip читает буфер локального
// X-сервера. Проверено: картинка доходит побайтово той же.
const sshPasteHint = "графической сессии здесь нет — похоже, ollchat запущен по SSH, " +
	"а буфер обмена остался на вашей машине.\n" +
	"Чтобы вставка заработала: подключайтесь с пробросом X11 (ssh -X, для долгих сеансов -Y) " +
	"и установите на этой машине xclip (sudo apt install xclip). " +
	"Проверить: echo $DISPLAY должен быть непустым.\n" +
	"Осторожно с tmux и screen: внутри уже созданной сессии DISPLAY остаётся от прежнего " +
	"подключения, и после переподключения его надо обновить — иначе вставка будет молчать."

// handleImagePasted прикладывает прочитанное изображение к текущему вопросу.
func (m *Model) handleImagePasted(msg imagePastedMsg) {
	if msg.err != nil {
		m.statusMsg = ""

		// Нет графической сессии — это не ошибка пользователя, а обстоятельство
		// среды, у которого есть внятный выход. Подсказку показываем один раз
		// за сеанс: способ доступа к буферу за время работы не изменится.
		if errors.Is(msg.err, clipboard.ErrNoSession) {
			if !m.sshPasteNoted {
				m.sshPasteNoted = true
				m.addBlock(block{kind: blockHint, text: sshPasteHint})
			} else {
				m.statusMsg = "буфер обмена недоступен: графической сессии нет"
			}
			return
		}

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
