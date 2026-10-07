package tools

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/permissions"
)

func scanRegistry(t *testing.T) (*Registry, string) {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	sb, err := permissions.NewSandbox(root, false, false, 512)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewRegistry([]string{NameScanRedact}, Options{Sandbox: sb, MaxOutputKB: 64})
	if err != nil {
		t.Fatal(err)
	}
	return reg, root
}

// Проверке прав достаётся каждая цель: главная — первая запись (её человек
// видит в окне подтверждения), чтение исходника и остальные три записи —
// в Extra. Пропусти хоть одну — запрет на неё в настройках не сработает.
// Все четыре файла — по formats=all; по умолчанию копий со всеми данными
// нет (слово владельца 07.10.2026).
func TestScanRedactPlanCoversEveryTarget(t *testing.T) {
	reg, root := scanRegistry(t)
	if plan, err := reg.Plan(NameScanRedact, map[string]any{"path": "a.pdf"}); err != nil || len(plan.Extra) != 2 {
		t.Errorf("по умолчанию — чтение и одна запись сверх главной, а вышло %v, %+v", err, plan)
	}
	plan, err := reg.Plan(NameScanRedact, map[string]any{"path": "docs/скан.pdf", "formats": "all"})
	if err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(root, "docs", "скан.pdf")
	outPDF := filepath.Join(root, "docs", "скан.redacted.pdf")
	write := func(name string) permissions.Request {
		return permissions.Request{Kind: permissions.KindWrite, Target: filepath.Join(root, "docs", name), Tool: NameScanRedact}
	}
	if plan.Req.Kind != permissions.KindWrite || plan.Req.Target != outPDF {
		t.Errorf("главная цель %+v, а должна быть запись %s", plan.Req, outPDF)
	}
	want := map[permissions.Request]bool{
		{Kind: permissions.KindRead, Target: in, Tool: NameScanRedact}: false,
		write("скан.redacted.md"):                                      false,
		write("скан.ocr.pdf"):                                          false,
		write("скан.ocr.md"):                                           false,
	}
	for _, r := range plan.Extra {
		if _, ok := want[r]; !ok {
			t.Errorf("лишняя цель %+v", r)
		}
		want[r] = true
	}
	for r, seen := range want {
		if !seen {
			t.Errorf("нет цели %+v", r)
		}
	}
	if !plan.Foreign {
		t.Error("текст документа пришёл извне и должен помечаться как данные")
	}
}

func TestScanRedactOnlyMarkdown(t *testing.T) {
	reg, root := scanRegistry(t)
	plan, err := reg.Plan(NameScanRedact, map[string]any{"path": "a.pdf", "formats": "md", "out_md": "out/итог.md"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Req.Target != filepath.Join(root, "out", "итог.md") {
		t.Errorf("главная цель %s", plan.Req.Target)
	}
	if len(plan.Extra) != 1 || plan.Extra[0].Kind != permissions.KindRead {
		t.Errorf("при одном .md лишних записей быть не должно: %+v", plan.Extra)
	}
}

// Форматы разбираются словами, а не подстрокой: прежний разбор нашёл бы
// в «ocr-pdf» ещё и «pdf». Пробел перед расширением в имени исходника
// («15-006-test .pdf» от сканера) в имена итогов не переходит.
func TestScanRedactFormatsAreWords(t *testing.T) {
	reg, root := scanRegistry(t)
	plan, err := reg.Plan(NameScanRedact, map[string]any{"path": "скан .pdf", "formats": "ocr-pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "скан.ocr.pdf"); plan.Req.Target != want {
		t.Errorf("главная цель %s, а должна быть %s", plan.Req.Target, want)
	}
	if len(plan.Extra) != 1 || plan.Extra[0].Kind != permissions.KindRead {
		t.Errorf("заказан один ocr-pdf, а целей записи больше: %+v", plan.Extra)
	}
	plan, err = reg.Plan(NameScanRedact, map[string]any{"path": "a.pdf", "formats": "pdf,md"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Extra) != 2 {
		t.Errorf("pdf,md — две записи и чтение, а целей в Extra %d: %+v", len(plan.Extra), plan.Extra)
	}
	if _, err := reg.Plan(NameScanRedact, map[string]any{"path": "a.pdf", "formats": "all", "out_pdf": "x.pdf", "out_ocr_pdf": "x.pdf"}); err == nil {
		t.Error("замазанный и распознанный PDF в один файл — должен быть отказ")
	}
}

func TestScanRedactRefusesBadArgs(t *testing.T) {
	reg, _ := scanRegistry(t)
	for name, args := range map[string]map[string]any{
		"исходник перезаписан":         {"path": "a.pdf", "out_pdf": "a.pdf"},
		"исходник затёрт распознанным": {"path": "a.pdf", "formats": "all", "out_ocr_md": "a.pdf"},
		"нет форматов":                 {"path": "a.pdf", "formats": "docx"},
		"выход из песочницы":           {"path": "a.pdf", "out_md": "/etc/x.md"},
		"распознанный вне песочницы":   {"path": "a.pdf", "formats": "ocr", "out_ocr_pdf": "/etc/x.pdf"},
	} {
		if _, err := reg.Plan(NameScanRedact, args); err == nil {
			t.Errorf("%s: план построен", name)
		}
	}
}

// Шапка, повторённая на каждом листе, уходит модели один раз; заголовки
// листов и разметка таблиц остаются.
func TestOnceEach(t *testing.T) {
	md := "## Страница 1\n\nCLIENT Acc No. DOS: 06/14/2026\n\n| a | b |\n| --- | --- |\n| 1 | 2 |\n\n" +
		"## Страница 2\n\nCLIENT Acc No. DOS: 06/14/2026\n\n| a | b |\n| --- | --- |\n| 3 | 4 |\n"
	got, dropped := onceEach(md)
	if dropped != 2 {
		t.Errorf("пропущено %d повторов, а их 2 (шапка и строка заголовков таблицы):\n%s", dropped, got)
	}
	for _, want := range []string{"## Страница 2", "| 3 | 4 |"} {
		if !strings.Contains(got, want) {
			t.Errorf("пропало %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "DOS: 06/14/2026") != 1 || strings.Count(got, "| --- | --- |") != 2 {
		t.Errorf("повторы:\n%s", got)
	}
}

// Длинный текст укладывается в бюджет: строки с подозрительным словом
// и с ролью входят первыми, где бы ни стояли, порядок — документа.
func TestWithinBudget(t *testing.T) {
	var lines []string
	for i := 0; i < 200; i++ {
		lines = append(lines, "Glucose value within the reference range today")
	}
	lines[150] = "Seen by Corwin for follow-up"
	lines[170] = "CLIENT tolerated the procedure"
	text := strings.Join(lines, "\n")
	got, omitted := withinBudget(text, []string{"Corwin"}, 1000)
	if len(got) > 1000 {
		t.Errorf("бюджет превышен: %d байт", len(got))
	}
	if omitted == 0 || !strings.Contains(got, "Corwin") || !strings.Contains(got, "CLIENT tolerated") {
		t.Errorf("пропущено %d, а строки с именем и ролью должны войти:\n%s", omitted, got)
	}
	if strings.Index(got, "Corwin") > strings.Index(got, "CLIENT tolerated") {
		t.Error("порядок строк не документа")
	}
	if short, n := withinBudget("коротко", nil, 1000); short != "коротко" || n != 0 {
		t.Errorf("короткий текст изменён: %q, %d", short, n)
	}
}

func TestSplitList(t *testing.T) {
	got := splitList(" Анна Смирнова ; John Roe\n;; ")
	if len(got) != 2 || got[0] != "Анна Смирнова" || got[1] != "John Roe" {
		t.Errorf("%q", got)
	}
}
