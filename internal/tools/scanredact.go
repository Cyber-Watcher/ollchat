package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

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
			"По умолчанию делает четыре файла: PDF точно такой же, как исходный, но с замазанными чёрным " +
			"персональными данными; файл .md с текстом документа и таблицами, где имя клиента заменено на CLIENT, " +
			"имя врача — на DOCTOR, а адреса, телефоны, почта, номера карт, полисов, страховок и исследований " +
			"и даты рождения убраны; и две распознанные копии со всеми данными — .md и текстовый PDF с таблицами. " +
			"Возвращает сводку и текст документа уже без персональных данных; текст распознанных копий не возвращается. " +
			"Если в обезличенном тексте осталось чьё-то имя, адрес, телефон или номер — вызови инструмент снова " +
			"и передай это в clients, doctors или hide. " +
			"Распознавание делает установленная программа tesseract; ничего ставить и писать самому не нужно.",
		Parameters: ollama.ToolParams{
			Type: "object",
			Properties: map[string]ollama.ToolProp{
				"path":        {Type: "string", Description: "Путь к документу PDF"},
				"formats":     {Type: "string", Description: "Что сделать, " + redact.FormatsHelp + "; по умолчанию все четыре"},
				"out_pdf":     {Type: "string", Description: "Куда записать PDF с замазанными данными; по умолчанию рядом с исходным, <имя>.redacted.pdf"},
				"out_md":      {Type: "string", Description: "Куда записать .md без персональных данных; по умолчанию рядом с исходным, <имя>.redacted.md"},
				"out_ocr_pdf": {Type: "string", Description: "Куда записать текстовый PDF распознанного; по умолчанию рядом с исходным, <имя>.ocr.pdf"},
				"out_ocr_md":  {Type: "string", Description: "Куда записать .md распознанного; по умолчанию рядом с исходным, <имя>.ocr.md"},
				"lang":        {Type: "string", Description: "Языки распознавания: «eng», «rus» или «eng+rus»; по умолчанию их выбирают первые листы документа"},
				"clients":     {Type: "string", Description: "Имена клиентов, которые надо скрыть сверх найденного, через «;»"},
				"doctors":     {Type: "string", Description: "Имена врачей, которые надо скрыть сверх найденного, через «;»"},
				"hide":        {Type: "string", Description: "Прочие строки, которые надо скрыть (адрес, номер, название клиники), через «;»"},
			},
			Required: []string{"path"},
		},
	}}
}

// SafeArgs — аргументы для журналов без самих подсказок: сколько имён и строк
// передано, но не какие. Путь и форматы остаются — по ним разбирают, что
// сделано. Работает и на аргументах, которые Plan отверг: отказ тоже пишется
// в журнал, и имена не должны попасть туда этой дорогой.
func (t *scanRedactTool) SafeArgs(args map[string]any) string {
	b, _ := json.Marshal(map[string]any{
		"path":    argStringOr(args, "path", ""),
		"formats": strings.ToLower(argStringOr(args, "formats", redact.DefaultFormats)),
		"clients": len(splitList(argStringOr(args, "clients", ""))),
		"doctors": len(splitList(argStringOr(args, "doctors", ""))),
		"hide":    len(splitList(argStringOr(args, "hide", ""))),
	})
	return string(b)
}

func (t *scanRedactTool) Plan(args map[string]any) (*Plan, error) {
	raw, err := requireText(args, "path")
	if err != nil {
		return nil, err
	}
	in, err := t.opts.Sandbox.Resolve(raw)
	if err != nil {
		return nil, err
	}
	want, err := redact.ParseFormats(argStringOr(args, "formats", ""))
	if err != nil {
		return nil, fmt.Errorf("formats: %w", err)
	}
	outs := redact.DefaultOutputs(in, want)
	for _, o := range []struct {
		dst *string
		key string
	}{{&outs.PDF, "out_pdf"}, {&outs.MD, "out_md"}, {&outs.OCRPDF, "out_ocr_pdf"}, {&outs.OCRMD, "out_ocr_md"}} {
		s := strings.TrimSpace(argStringOr(args, o.key, ""))
		if *o.dst == "" || s == "" {
			continue
		}
		abs, err := t.opts.Sandbox.Resolve(s)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", o.key, err)
		}
		*o.dst = abs
	}
	if err := outs.Check(in); err != nil {
		return nil, err
	}
	opt := redact.Options{
		Lang:    argStringOr(args, "lang", ""),
		Clients: splitList(argStringOr(args, "clients", "")),
		Doctors: splitList(argStringOr(args, "doctors", "")),
		Hide:    splitList(argStringOr(args, "hide", "")),
	}

	// Главная цель — запись: её человек видит в окне подтверждения. Чтение
	// исходника и остальные записи проверяются теми же правилами (Plan.Extra):
	// каждый из четырёх файлов — своя цель, иначе запрет на распознанную
	// копию со всеми данными проскочил бы мимо правил.
	writes := outs.List()
	req := permissions.Request{Kind: permissions.KindWrite, Target: writes[0], Tool: NameScanRedact}
	extra := []permissions.Request{{Kind: permissions.KindRead, Target: in, Tool: NameScanRedact}}
	for _, p := range writes[1:] {
		extra = append(extra, permissions.Request{Kind: permissions.KindWrite, Target: p, Tool: NameScanRedact})
	}
	rel := t.opts.Sandbox.Rel
	shown := make([]string, 0, len(writes))
	for _, p := range writes {
		shown = append(shown, rel(p))
	}
	preview := "персональные данные в PDF замазываются чёрным, в .md имена заменяются на CLIENT и DOCTOR, " +
		"прочее убирается."
	if outs.OCRPDF != "" || outs.OCRMD != "" {
		preview += " Распознанные копии (.ocr) содержат ВСЕ данные документа."
	}

	return &Plan{
		Tool:    NameScanRedact,
		Req:     req,
		Extra:   extra,
		LogArgs: t.SafeArgs(args),
		Title:   fmt.Sprintf("%s(%s → %s)", NameScanRedact, rel(in), strings.Join(shown, ", ")),
		Preview: fmt.Sprintf("Прочитает скан: %s\nЗапишет: %s\nРаспознаёт программа tesseract; %s",
			rel(in), strings.Join(shown, ", "), preview),
		// Текст документа пришёл извне: в скане может оказаться что угодно,
		// в том числе похожее на указания.
		Foreign: true,
		Run: func(ctx context.Context) (string, error) {
			return t.run(ctx, in, outs, opt)
		},
	}, nil
}

func (t *scanRedactTool) run(ctx context.Context, in string, outs redact.Outputs, opt redact.Options) (string, error) {
	pages, notes, err := redact.Load(ctx, in, t.opts.Sandbox.MaxPDFBytes())
	if err != nil {
		return "", err
	}
	title := filepath.Base(redact.Stem(in))
	res, err := redact.Process(ctx, title, pages, opt)
	if err != nil {
		return "", err
	}
	rel := t.opts.Sandbox.Rel
	var b strings.Builder
	fmt.Fprintf(&b, "Скан %s обработан: страниц %d, распознано слов %d, язык распознавания %s.\n",
		rel(in), len(pages), len(res.Words), res.Lang)
	b.WriteString(res.CountsLine())
	// Распознанные копии — со всеми данными: модели уходят только их пути
	// и размеры, а текст — один обезличенный, ниже.
	written, err := redact.WriteOutputs(outs, title, pages, res)
	for _, w := range written {
		fmt.Fprintf(&b, "Записан %s: %s (%d байт).\n", w.What, rel(w.Path), w.Bytes)
		if w.Note != "" {
			fmt.Fprintf(&b, "Оговорка: %s.\n", w.Note)
		}
	}
	if err != nil {
		return "", err
	}
	for _, n := range notes {
		fmt.Fprintf(&b, "Заметка: %s.\n", n)
	}
	b.WriteString(res.Check.Line())
	left := redact.Leftovers(res.MD)
	if len(left) > 0 {
		fmt.Fprintf(&b, "\nПРОВЕРЬ: в тексте остались слова с заглавной буквы посреди предложения или рядом "+
			"с CLIENT/DOCTOR/PERSON — возможно, это имена, фамилии, улицы или названия: %s. "+
			"Если среди них есть люди, адреса или организации — вызови %s снова и передай их "+
			"в clients (клиент), doctors (врач) или hide (прочее). Если всё это обычные слова — повтор не нужен.\n",
			strings.Join(left, ", "), NameScanRedact)
	}
	b.WriteString("\n" + ScanRedactTextMark + " Если в нём осталось имя человека, " +
		"адрес, телефон или номер, вызови " + NameScanRedact + " снова и передай это в clients, doctors или hide.\n")
	text, dropped := onceEach(res.MD)
	if dropped > 0 {
		fmt.Fprintf(&b, "Строки, повторяющиеся на разных листах (шапки, подвалы), показаны один раз: "+
			"пропущено повторов %d; в файле .md они на месте.\n", dropped)
	}
	text, omitted := withinBudget(text, left, modelTextMax)
	if omitted > 0 {
		fmt.Fprintf(&b, "Документ длинный: показаны строки с возможными именами и с CLIENT/DOCTOR/PERSON, "+
			"затем начало документа; пропущено строк %d — весь текст в файле .md.\n", omitted)
	}
	b.WriteString("\n" + text)
	return t.opts.truncate(b.String()), nil
}

// modelTextMax — сколько байт обезличенного текста уходит модели в одном
// ответе. Прогон 06.10.2026 (39 листов, qwen3.8, num_ctx 32768): два ответа
// по ~48 КБ не поместились в окно, сервер отрезал начало разговора вместе
// с просьбой пользователя и отказал («no user query found in messages»),
// и модель не ответила. 24 КБ — около 6–8 тыс. токенов: два-три вызова
// с системным промптом ложатся в 32768.
const modelTextMax = 24 << 10

// withinBudget укладывает текст в max байт: сперва строки, где есть слова
// из подсказки Leftovers или роли CLIENT/DOCTOR/PERSON (там и остаются
// пропущенные имена), и заголовки листов, затем прочие по порядку. Порядок
// строк в итоге — документа. Вторым значением — сколько непустых строк
// не вошло.
func withinBudget(text string, left []string, max int) (string, int) {
	if len(text) <= max {
		return text, 0
	}
	suspect := map[string]bool{}
	for _, w := range left {
		suspect[w] = true
	}
	lines := strings.Split(text, "\n")
	hot := func(ln string) bool {
		if strings.HasPrefix(ln, "## ") {
			return true
		}
		for _, f := range strings.FieldsFunc(ln, func(r rune) bool {
			return !unicode.IsLetter(r) && r != '\'' && r != '’' && r != '-'
		}) {
			if suspect[f] || f == "CLIENT" || f == "DOCTOR" || f == "PERSON" {
				return true
			}
		}
		return false
	}
	keep := make([]bool, len(lines))
	size := 0
	for pass := 0; pass < 2; pass++ {
		for i, ln := range lines {
			if keep[i] || (pass == 0 && !hot(ln)) || strings.TrimSpace(ln) == "" {
				continue
			}
			if size+len(ln)+1 > max {
				continue
			}
			keep[i] = true
			size += len(ln) + 1
		}
	}
	var out []string
	omitted := 0
	for i, ln := range lines {
		switch {
		case keep[i]:
			out = append(out, ln)
		case strings.TrimSpace(ln) == "":
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
		default:
			omitted++
		}
	}
	return strings.Join(out, "\n"), omitted
}

// onceEach оставляет каждую непустую строку текста один раз. Шапка факса
// и подвал лаборатории повторяются на каждом листе: в образце 06.10.2026
// (39 листов) обезличенный текст — 61830 байт, без повторов — 47994. Ответ
// инструмента ложится в окно модели целиком при каждом вызове, и уже на
// втором вызове полный текст не помещался бы в 32768 токенов. Чтобы найти
// оставшееся имя, строку хватает увидеть однажды. Строки разметки таблиц
// и заголовки листов не считаются повторами.
func onceEach(md string) (string, int) {
	seen := map[string]bool{}
	var out []string
	dropped := 0
	for _, ln := range strings.Split(md, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "| ---") {
			out = append(out, ln)
			continue
		}
		if seen[t] {
			dropped++
			continue
		}
		seen[t] = true
		out = append(out, ln)
	}
	return strings.Join(out, "\n"), dropped
}

// ScanRedactTextMark — с этой фразы в ответе инструмента идёт текст
// документа: ключ --scan-redact-llm печатает сводку до неё, а текст — уже
// в файле .md.
const ScanRedactTextMark = "Ниже текст документа без персональных данных."

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
