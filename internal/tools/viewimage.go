package tools

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/itpro/ollchat/internal/document"
	"github.com/itpro/ollchat/internal/ollama"
	"github.com/itpro/ollchat/internal/permissions"
)

// Просмотр картинок из документа.
//
// Инструмент появился по живому случаю. В коммерческом предложении страница
// «Финансовое предложение» со всей таблицей цен оказалась картинкой: в текстовом
// слое документа слова «стоимость» не было вовсе. Модель рассудила верно —
// цена в картинке, картинку надо посмотреть, — но смотреть было нечем, и она
// пошла искать в системе python и средства распознавания. Виновата была
// не модель, а отсутствие возможности.
//
// Текст в ответе инструмента ничего не решает: картинку нельзя вернуть строкой.
// Поэтому инструмент отдаёт её отдельно, а цикл агента подкладывает её в диалог
// сообщением с полем images — так её видит модель с возможностью vision.

// maxViewImages ограничивает разовый показ: каждая картинка едет в base64
// и занимает контекстное окно.
const maxViewImages = 4

type viewImageTool struct {
	opts Options

	// pending — картинки, добытые последним запуском. Читаются агентом сразу
	// после Run, поэтому переживать за состояние между вызовами не нужно.
	pending []string
}

func (t *viewImageTool) Name() string { return NameViewImage }

func (t *viewImageTool) Spec() ollama.Tool {
	return ollama.Tool{Type: "function", Function: ollama.ToolSpec{
		Name: NameViewImage,
		Description: "Показывает картинку из документа PDF или EPUB, чтобы её можно было " +
			"рассмотреть. Нужен, когда в тексте документа стоит метка [рисунок N.M] " +
			"и важно, что на ней изображено: схема, снимок экрана или страница, " +
			"свёрстанная картинкой (так часто делают таблицы цен в коммерческих " +
			"предложениях — в тексте их нет вовсе). " +
			"Никаких внешних средств распознавания не требуется и ставить их не нужно: " +
			"картинка просто показывается модели. Нужна модель с возможностью vision.",
		Parameters: ollama.ToolParams{
			Type: "object",
			Properties: map[string]ollama.ToolProp{
				"path":   {Type: "string", Description: "Путь к документу"},
				"figure": {Type: "string", Description: "Метка рисунка из текста, например «4.1»; пусто — все крупные"},
				"page":   {Type: "integer", Description: "Страница или раздел, картинки с которого показать"},
				"limit":  {Type: "integer", Description: "Сколько картинок показать, 1..4"},
			},
			Required: []string{"path"},
		},
	}}
}

func (t *viewImageTool) Plan(args map[string]any) (*Plan, error) {
	raw, err := requireString(args, "path")
	if err != nil {
		return nil, err
	}
	abs, err := t.opts.Sandbox.Resolve(raw)
	if err != nil {
		return nil, err
	}
	figure := strings.TrimSpace(argStringOr(args, "figure", ""))
	page := argInt(args, "page", 0)
	limit := argInt(args, "limit", 0)
	rel := t.opts.Sandbox.Rel(abs)

	title := fmt.Sprintf("%s(%s)", NameViewImage, rel)
	if figure != "" {
		title = fmt.Sprintf("%s(%s, рисунок %s)", NameViewImage, rel, figure)
	} else if page > 0 {
		title = fmt.Sprintf("%s(%s, стр. %d)", NameViewImage, rel, page)
	}

	return &Plan{
		Tool:  NameViewImage,
		Req:   permissions.Request{Kind: permissions.KindRead, Target: abs, Tool: NameViewImage},
		Title: title,
		Run: func(ctx context.Context) (string, error) {
			return t.run(abs, rel, figure, page, limit)
		},
		Images: func() []string { return t.pending },
	}, nil
}

func (t *viewImageTool) run(abs, rel, figure string, page, limit int) (string, error) {
	t.pending = nil

	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if document.DetectFile(abs) == document.KindNone {
		return "", fmt.Errorf("%s — не документ PDF и не книга EPUB", rel)
	}
	if info.Size() > t.opts.Sandbox.MaxPDFBytes() {
		return "", fmt.Errorf("документ слишком велик: %d байт, предел %d (sandbox.max_pdf_mb)",
			info.Size(), t.opts.Sandbox.MaxPDFBytes())
	}
	if limit <= 0 || limit > maxViewImages {
		limit = maxViewImages
	}

	opt := document.ImageOptions{MinWidth: 200, MinHeight: 200}
	// Метка «4.1» означает первый рисунок со страницы 4 — так их нумерует
	// извлечение текста, и именно это модель видит в документе.
	if unit, _, ok := parseFigure(figure); ok {
		opt.First, opt.Count = unit, 1
	} else if page > 0 {
		opt.First, opt.Count = page, 1
	}

	imgs, err := document.Images(abs, t.opts.Sandbox.MaxPDFBytes(), opt)
	if err != nil {
		return "", err
	}
	if figure != "" {
		imgs = filterByLabel(imgs, figure)
	}
	if len(imgs) == 0 {
		return t.explainEmpty(abs, rel, figure, page)
	}
	if len(imgs) > limit {
		imgs = imgs[:limit]
	}

	var b strings.Builder
	for _, im := range imgs {
		t.pending = append(t.pending, base64.StdEncoding.EncodeToString(im.Data))
		fmt.Fprintf(&b, "рисунок %s из %s: %d×%d, %s\n", im.Label, rel, im.Width, im.Height, im.Format)
	}
	b.WriteString("\nКартинки приложены к диалогу следующим сообщением — посмотри на них " +
		"и опиши то, что видишь, своими словами. Если это таблица, перечисли строки и числа.")
	return b.String(), nil
}

// explainEmpty объясняет, почему показывать нечего: молчание модель толкует
// как сбой инструмента и начинает искать обходные пути.
func (t *viewImageTool) explainEmpty(abs, rel, figure string, page int) (string, error) {
	all, err := document.Images(abs, t.opts.Sandbox.MaxPDFBytes(), document.ImageOptions{MinWidth: 200, MinHeight: 200})
	if err != nil {
		return "", err
	}
	if len(all) == 0 {
		return fmt.Sprintf("В документе %s нет картинок крупнее 200×200 точек — "+
			"смотреть нечего, всё содержимое в тексте.", rel), nil
	}
	labels := make([]string, 0, len(all))
	for i, im := range all {
		if i >= 20 {
			labels = append(labels, "…")
			break
		}
		labels = append(labels, im.Label)
	}
	what := "по запросу"
	if figure != "" {
		what = "с меткой " + figure
	} else if page > 0 {
		what = fmt.Sprintf("на странице %d", page)
	}
	return fmt.Sprintf("Картинки %s в документе %s не нашлось. Есть такие: %s.",
		what, rel, strings.Join(labels, ", ")), nil
}

// parseFigure разбирает метку «4.1»: страница и номер на ней.
func parseFigure(s string) (unit, index int, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, false
	}
	parts := strings.SplitN(s, ".", 2)
	u, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || u <= 0 {
		return 0, 0, false
	}
	if len(parts) == 1 {
		return u, 0, true
	}
	i, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return u, 0, true
	}
	return u, i, true
}

func filterByLabel(imgs []document.Image, figure string) []document.Image {
	unit, index, ok := parseFigure(figure)
	if !ok {
		return imgs
	}
	var out []document.Image
	for _, im := range imgs {
		u, i, _ := parseFigure(im.Label)
		if u != unit {
			continue
		}
		if index > 0 && i != index {
			continue
		}
		out = append(out, im)
	}
	return out
}
