package tools

import (
	"path/filepath"
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
// видит в окне подтверждения), чтение исходника и вторая запись — в Extra.
// Пропусти хоть одну — запрет на неё в настройках не сработает.
func TestScanRedactPlanCoversEveryTarget(t *testing.T) {
	reg, root := scanRegistry(t)
	plan, err := reg.Plan(NameScanRedact, map[string]any{"path": "docs/скан.pdf"})
	if err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(root, "docs", "скан.pdf")
	outPDF := filepath.Join(root, "docs", "скан.redacted.pdf")
	outMD := filepath.Join(root, "docs", "скан.redacted.md")
	if plan.Req.Kind != permissions.KindWrite || plan.Req.Target != outPDF {
		t.Errorf("главная цель %+v, а должна быть запись %s", plan.Req, outPDF)
	}
	want := map[permissions.Request]bool{
		{Kind: permissions.KindRead, Target: in, Tool: NameScanRedact}:     false,
		{Kind: permissions.KindWrite, Target: outMD, Tool: NameScanRedact}: false,
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

func TestScanRedactRefusesBadArgs(t *testing.T) {
	reg, _ := scanRegistry(t)
	for name, args := range map[string]map[string]any{
		"исходник перезаписан": {"path": "a.pdf", "out_pdf": "a.pdf"},
		"нет форматов":         {"path": "a.pdf", "formats": "docx"},
		"выход из песочницы":   {"path": "a.pdf", "out_md": "/etc/x.md"},
	} {
		if _, err := reg.Plan(NameScanRedact, args); err == nil {
			t.Errorf("%s: план построен", name)
		}
	}
}

func TestSplitList(t *testing.T) {
	got := splitList(" Анна Смирнова ; John Roe\n;; ")
	if len(got) != 2 || got[0] != "Анна Смирнова" || got[1] != "John Roe" {
		t.Errorf("%q", got)
	}
}
