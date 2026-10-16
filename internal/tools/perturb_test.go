package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/permissions"
)

// --tool-perturb (этап 112, О19): модель видит другое описание и имя параметра,
// исполняется прежний инструмент, остальные инструменты и исходное описание целы.
func TestPerturbRenamesForModelAndMapsBack(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("проба\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sb, err := permissions.NewSandbox(root, false, false, 512)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewRegistry([]string{NameReadFile, NameListDir}, Options{Sandbox: sb, MaxOutputKB: 64})
	if err != nil {
		t.Fatal(err)
	}
	before := reg.tools[NameListDir].Spec().Function.Description
	inner := reg.tools[NameReadFile]

	err = reg.Perturb(map[string]Perturbation{NameReadFile: {
		Description: "Показать содержимое файла.",
		Rename:      map[string]string{"path": "file"},
		Params:      map[string]string{"file": "Какой файл открыть"},
	}})
	if err != nil {
		t.Fatal(err)
	}

	var spec *struct{ desc, req string }
	for _, s := range reg.Specs() {
		if s.Function.Name != NameReadFile {
			continue
		}
		if _, old := s.Function.Parameters.Properties["path"]; old {
			t.Error("модель всё ещё видит старое имя параметра path")
		}
		p, ok := s.Function.Parameters.Properties["file"]
		if !ok || p.Description != "Какой файл открыть" {
			t.Errorf("нового параметра file с описанием нет: %+v", s.Function.Parameters.Properties)
		}
		spec = &struct{ desc, req string }{s.Function.Description, strings.Join(s.Function.Parameters.Required, ",")}
	}
	if spec == nil || spec.desc != "Показать содержимое файла." {
		t.Fatalf("описание read_file не заменено: %+v", spec)
	}
	if !strings.Contains(spec.req, "file") || strings.Contains(spec.req, "path") {
		t.Errorf("обязательные параметры не переименованы: %q", spec.req)
	}
	// Исходный инструмент не тронут: его карта свойств общая — правилась копия.
	if _, ok := inner.Spec().Function.Parameters.Properties["path"]; !ok {
		t.Error("исходное описание read_file испорчено правкой обёртки")
	}
	if got := reg.tools[NameListDir].Spec().Function.Description; got != before {
		t.Error("описание соседнего инструмента изменилось")
	}

	// Модель зовёт под новым именем — инструмент получает старое и работает.
	plan, err := reg.Plan(NameReadFile, map[string]any{"file": "a.txt"})
	if err != nil {
		t.Fatalf("вызов под новым именем не прошёл: %v", err)
	}
	if plan == nil || plan.Run == nil {
		t.Fatal("план без исполнения")
	}
}

// Опечатка в файле возмущения — отказ, а не молча пропущенная правка.
func TestPerturbRejectsUnknown(t *testing.T) {
	sb, err := permissions.NewSandbox(t.TempDir(), false, false, 512)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewRegistry([]string{NameReadFile, NameListDir}, Options{Sandbox: sb, MaxOutputKB: 64})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]map[string]Perturbation{
		"нет инструмента": {"grep": {Description: "x"}},
		"нет параметра":   {NameReadFile: {Rename: map[string]string{"pth": "file"}}},
		"имя занято":      {NameReadFile: {Rename: map[string]string{"path": "path"}}},
	}
	for name, p := range cases {
		if err := reg.Perturb(p); err == nil {
			t.Errorf("%s: ждали отказ", name)
		}
	}
}
