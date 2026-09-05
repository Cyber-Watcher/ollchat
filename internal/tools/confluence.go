package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/confluence"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
)

// confluence_connector — чтение страницы корпоративной вики.
//
// Инструмент только читает и отдаёт markdown. Что делать дальше — не его дело:
// сказали в промпте сохранить в репозиторий, модель возьмёт write_file
// и запишет полученное; сказали пересказать — перескажет. Разделение
// намеренное: чтение и запись — разные права, и складывать их в один
// инструмент значит терять возможность разрешить одно без другого.

type confluenceTool struct{ opts Options }

func (t *confluenceTool) Name() string { return NameConfluence }

func (t *confluenceTool) Spec() ollama.Tool {
	return ollama.Tool{Type: "function", Function: ollama.ToolSpec{
		Name: NameConfluence,
		Description: "Читает страницу Confluence и возвращает её в markdown: " +
			"заголовки, списки, таблицы, блоки кода дословно, свёрнутые блоки раскрытыми. " +
			"Принимает адрес страницы или её номер. " +
			"В конце перечисляет вложения и, если попросить, дочерние страницы — " +
			"их содержимое отдельным вызовом, по одной. " +
			"Инструмент только читает; чтобы сохранить полученное в файл, " +
			"возьми " + NameWriteFile + " и запиши markdown как есть — " +
			"таблицы и коды переписывать своими словами нельзя, они должны доехать дословно.",
		Parameters: ollama.ToolParams{
			Type: "object",
			Properties: map[string]ollama.ToolProp{
				"page": {Type: "string", Description: "Адрес страницы или её номер"},
				"children": {Type: "boolean",
					Description: "Добавить список дочерних страниц (без их содержимого)"},
			},
			Required: []string{"page"},
		},
	}}
}

func (t *confluenceTool) Plan(args map[string]any) (*Plan, error) {
	page, err := requireString(args, "page")
	if err != nil {
		return nil, err
	}
	id, err := confluence.PageID(page)
	if err != nil {
		return nil, err
	}
	withKids := argBool(args, "children", false)

	base := strings.TrimRight(t.opts.ConfluenceURL, "/")
	if base == "" {
		return nil, fmt.Errorf("адрес Confluence не задан: раздел [confluence] в настройках")
	}
	target := base + "/pages/viewpage.action?pageId=" + id

	return &Plan{
		Tool:    NameConfluence,
		Foreign: true,
		// Тот же вид разрешения, что у http_fetch: это сетевой запрос наружу,
		// и правила deny на хост обязаны действовать здесь так же.
		Req:     permissions.Request{Kind: permissions.KindFetch, Target: target, Tool: NameConfluence},
		Title:   fmt.Sprintf("%s(%s)", NameConfluence, id),
		Preview: "Будет прочитана страница " + target,
		Run: func(ctx context.Context) (string, error) {
			cl := confluence.New(base, t.opts.ConfluenceToken, t.opts.ConfluenceTimeout)
			p, err := cl.Get(ctx, id, withKids)
			if err != nil {
				return "", err
			}
			md, err := p.Markdown()
			if err != nil {
				return "", fmt.Errorf("страница прочитана, но не переводится в markdown: %w", err)
			}
			return t.opts.truncate(md), nil
		},
	}, nil
}
