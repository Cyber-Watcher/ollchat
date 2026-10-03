package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
	"github.com/Cyber-Watcher/ollchat/internal/redact"
)

// Обезличивание сканов.
//
// Инструмент появился по запросу пользователей 03.10.2026: присылают PDF,
// которые на деле сканы (медицинские заключения и т. п.), и просят либо
// такой же PDF с замазанными чёрным персональными данными, либо текст .md
// без них — имя клиента заменено на CLIENT, имя врача на DOCTOR, адреса,
// телефоны, почта и номера убраны.
//
// Модель видит только обезличенный текст: персональных данных нет ни
// в сводке, ни в тексте. Пропущенное правилами она замечает в этом тексте
// сама и называет в повторном вызове (clients, doctors, hide) — так имена
// в свободном тексте ловятся без распознавания имён по смыслу.

type scanRedactTool struct{ opts Options }

func (t *scanRedactTool) Name() string { return NameScanRedact }

func (t *scanRedactTool) Spec() ollama.Tool {
	return ollama.Tool{Type: "function", Function: ollama.ToolSpec{
		Name: NameScanRedact,
		Description: "Обрабатывает PDF, который на деле скан (страницы — картинки, текста нет): " +
			"распознаёт текст, находит персональные данные клиента и врача и скрывает их. " +
			"Делает PDF точно такой же, как исходный, но с замазанными чёрным персональными данными, " +
			"и/или файл .md с текстом документа, где имя клиента заменено на CLIENT, имя врача — на DOCTOR, " +
			"а адреса, телефоны, почта, номера карт, полисов, страховок и исследований и даты рождения убраны. " +
			"Возвращает сводку и текст документа уже без персональных данных. Если в этом тексте осталось " +
			"чьё-то имя, адрес, телефон или номер — вызови инструмент снова и передай это в clients, doctors или hide. " +
			"Распознавание делает установленная программа tesseract; ничего ставить и писать самому не нужно.",
		Parameters: ollama.ToolParams{
			Type: "object",
			Properties: map[string]ollama.ToolProp{
				"path":    {Type: "string", Description: "Путь к документу PDF"},
				"formats": {Type: "string", Description: "Что сделать: «pdf», «md» или «pdf,md» (по умолчанию оба)"},
				"out_pdf": {Type: "string", Description: "Куда записать PDF; по умолчанию рядом с исходным, <имя>.redacted.pdf"},
				"out_md":  {Type: "string", Description: "Куда записать .md; по умолчанию рядом с исходным, <имя>.redacted.md"},
				"lang":    {Type: "string", Description: "Языки распознавания: «eng», «rus» или «eng+rus»; по умолчанию оба из установленных"},
				"clients": {Type: "string", Description: "Имена клиентов, которые надо скрыть сверх найденного, через «;»"},
				"doctors": {Type: "string", Description: "Имена врачей, которые надо скрыть сверх найденного, через «;»"},
				"hide":    {Type: "string", Description: "Прочие строки, которые надо скрыть (адрес, номер, название клиники), через «;»"},
			},
			Required: []string{"path"},
		},
	}}
}

func (t *scanRedactTool) Plan(args map[string]any) (*Plan, error) {
	raw, err := requireString(args, "path")
	if err != nil {
		return nil, err
	}
	in, err := t.opts.Sandbox.Resolve(raw)
	if err != nil {
		return nil, err
	}
	formats := strings.ToLower(argStringOr(args, "formats", "pdf,md"))
	wantPDF, wantMD := strings.Contains(formats, "pdf"), strings.Contains(formats, "md")
	if !wantPDF && !wantMD {
		return nil, fmt.Errorf("formats: «pdf», «md» или «pdf,md», а не %q", formats)
	}
	stem := strings.TrimSuffix(in, filepath.Ext(in))
	out := func(key, def string, want bool) (string, error) {
		if !want {
			return "", nil
		}
		p := def
		if s := strings.TrimSpace(argStringOr(args, key, "")); s != "" {
			abs, err := t.opts.Sandbox.Resolve(s)
			if err != nil {
				return "", err
			}
			p = abs
		}
		if p == in {
			return "", fmt.Errorf("%s совпадает с исходным документом — исходник не перезаписывается", key)
		}
		return p, nil
	}
	outPDF, err := out("out_pdf", stem+".redacted.pdf", wantPDF)
	if err != nil {
		return nil, err
	}
	outMD, err := out("out_md", stem+".redacted.md", wantMD)
	if err != nil {
		return nil, err
	}
	opt := redact.Options{
		Lang:    argStringOr(args, "lang", ""),
		Clients: splitList(argStringOr(args, "clients", "")),
		Doctors: splitList(argStringOr(args, "doctors", "")),
		Hide:    splitList(argStringOr(args, "hide", "")),
	}

	// Главная цель — запись: её человек видит в окне подтверждения. Чтение
	// исходника и вторая запись проверяются теми же правилами (Plan.Extra).
	var writes []string
	for _, p := range []string{outPDF, outMD} {
		if p != "" {
			writes = append(writes, p)
		}
	}
	req := permissions.Request{Kind: permissions.KindWrite, Target: writes[0], Tool: NameScanRedact}
	extra := []permissions.Request{{Kind: permissions.KindRead, Target: in, Tool: NameScanRedact}}
	for _, p := range writes[1:] {
		extra = append(extra, permissions.Request{Kind: permissions.KindWrite, Target: p, Tool: NameScanRedact})
	}
	rel := t.opts.Sandbox.Rel
	outs := make([]string, 0, len(writes))
	for _, p := range writes {
		outs = append(outs, rel(p))
	}

	// В журналы — без самих подсказок: сколько имён и строк передано, но
	// не какие. Путь и форматы остаются — по ним разбирают, что сделано.
	logArgs, _ := json.Marshal(map[string]any{
		"path": raw, "formats": formats,
		"clients": len(opt.Clients), "doctors": len(opt.Doctors), "hide": len(opt.Hide),
	})

	return &Plan{
		Tool:    NameScanRedact,
		Req:     req,
		Extra:   extra,
		LogArgs: string(logArgs),
		Title:   fmt.Sprintf("%s(%s → %s)", NameScanRedact, rel(in), strings.Join(outs, ", ")),
		Preview: fmt.Sprintf("Прочитает скан: %s\nЗапишет: %s\nРаспознаёт программа tesseract; "+
			"персональные данные в PDF замазываются чёрным, в .md имена заменяются на CLIENT и DOCTOR, "+
			"прочее убирается.", rel(in), strings.Join(outs, ", ")),
		// Текст документа пришёл извне: в скане может оказаться что угодно,
		// в том числе похожее на указания.
		Foreign: true,
		Run: func(ctx context.Context) (string, error) {
			return t.run(ctx, in, outPDF, outMD, opt)
		},
	}, nil
}

func (t *scanRedactTool) run(ctx context.Context, in, outPDF, outMD string, opt redact.Options) (string, error) {
	pages, notes, err := redact.Load(ctx, in, t.opts.Sandbox.MaxPDFBytes())
	if err != nil {
		return "", err
	}
	title := strings.TrimSuffix(filepath.Base(in), filepath.Ext(in))
	res, err := redact.Process(ctx, title, pages, opt)
	if err != nil {
		return "", err
	}
	rel := t.opts.Sandbox.Rel
	var b strings.Builder
	fmt.Fprintf(&b, "Скан %s обработан: страниц %d, распознано слов %d.\n", rel(in), len(pages), len(res.Words))
	b.WriteString(res.CountsLine())
	if outPDF != "" {
		data, err := redact.PDF(pages, res.Redacted)
		if err != nil {
			return "", err
		}
		if err := redact.WriteFile(outPDF, data); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "Записан PDF с замазанными данными: %s (%d байт).\n", rel(outPDF), len(data))
	}
	if outMD != "" {
		if err := redact.WriteFile(outMD, []byte(res.MD)); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "Записан .md без персональных данных: %s (%d байт).\n", rel(outMD), len(res.MD))
	}
	for _, n := range notes {
		fmt.Fprintf(&b, "Заметка: %s.\n", n)
	}
	b.WriteString(res.Check.Line())
	if left := redact.Leftovers(res.MD); len(left) > 0 {
		fmt.Fprintf(&b, "\nПРОВЕРЬ: в тексте остались слова с заглавной буквы посреди предложения или рядом "+
			"с CLIENT/DOCTOR/PERSON — возможно, это имена, фамилии, улицы или названия: %s. "+
			"Если среди них есть люди, адреса или организации — вызови %s снова и передай их "+
			"в clients (клиент), doctors (врач) или hide (прочее). Если всё это обычные слова — повтор не нужен.\n",
			strings.Join(left, ", "), NameScanRedact)
	}
	b.WriteString("\nНиже текст документа без персональных данных. Если в нём осталось имя человека, " +
		"адрес, телефон или номер, вызови " + NameScanRedact + " снова и передай это в clients, doctors или hide.\n\n")
	b.WriteString(res.MD)
	return t.opts.truncate(b.String()), nil
}

// splitList делит список, заданный строкой через «;» или перевод строки.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == '\n' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
