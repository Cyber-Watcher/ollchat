package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/permissions"
)

// Меню команд и разбор команд обязаны совпадать — в обе стороны.
//
// **Почему проверка нужна.** `/graph find` была объявлена в меню полгода
// и всё это время отвечала «неизвестная подкоманда»: ветки разбора у неё
// не было вовсе. Обратная беда тише и потому хуже: работающие
// `/graph communities`, `/graph check`, `/graph pack`, `/graph rm` в меню
// не показывались, и узнать о них можно было только из исходников.
//
// **Откуда берутся имена разбора.** У /kb и /graph — из таблиц `kbSubs`
// и `graphSubs`: они и есть разбор. У верхнего уровня и у /context разбор
// остался `switch`, и его ветки читаются из исходника (go/ast) — списком
// рядом со switch он бы точно так же разошёлся, а тут читается сама правда.
// Режимы /mode берутся из постоянных пакета permissions.

// caseStrings отдаёт строковые метки первого switch внутри названной функции.
func caseStrings(t *testing.T, file, fn string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("разбор %s: %v", file, err)
	}
	var out []string
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != fn {
			continue
		}
		ast.Inspect(fd, func(n ast.Node) bool {
			cc, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, e := range cc.List {
				lit, ok := e.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if s, err := strconv.Unquote(lit.Value); err == nil {
					out = append(out, s)
				}
			}
			return true
		})
	}
	if len(out) == 0 {
		t.Fatalf("в %s не нашлось ни одной ветки switch — проверка ослепла", fn)
	}
	return out
}

// parsedNames — все написания, которые разбор действительно принимает.
// tableNames собирает написания команд из таблицы обработчиков:
// строковые литералы в полях names внутри функции fn.
func tableNames(t *testing.T, file, fn string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("разбор %s: %v", file, err)
	}
	var out []string
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != fn {
			continue
		}
		ast.Inspect(fd, func(n ast.Node) bool {
			kv, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			if id, ok := kv.Key.(*ast.Ident); !ok || id.Name != "names" {
				return true
			}
			ast.Inspect(kv.Value, func(n ast.Node) bool {
				if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if s, err := strconv.Unquote(lit.Value); err == nil {
						out = append(out, s)
					}
				}
				return true
			})
			return false
		})
	}
	if len(out) == 0 {
		t.Fatalf("в %s не нашлось ни одного имени команды — проверка ослепла", fn)
	}
	return out
}

func parsedNames(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, c := range tableNames(t, "commands.go", "commandHandlers") {
		out["/"+c] = true
	}
	for _, c := range caseStrings(t, "commands.go", "contextCmd") {
		out["/context "+c] = true
	}
	for _, mode := range []string{permissions.ModeSafe, permissions.ModeAutoEdit,
		permissions.ModeNoAsk, permissions.ModeYolo} {
		out["/mode "+mode] = true
	}
	for _, s := range kbSubs {
		for _, n := range s.names {
			out["/kb "+n] = true
		}
	}
	for _, s := range graphSubs {
		for _, n := range s.names {
			out["/graph "+n] = true
		}
	}
	return out
}

// Каждая команда меню разбирается. Иначе человек зовёт обещанное и получает
// «неизвестная подкоманда».
func TestMenuCommandsAreParsed(t *testing.T) {
	parsed := parsedNames(t)
	var missing []string
	for _, name := range knownCommandNames() {
		if !parsed[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("объявлены в меню, но не разбираются: %s", strings.Join(missing, ", "))
	}
}

// И наоборот: каждая разбираемая команда есть в меню. Работающая команда,
// о которой нельзя узнать из программы, всё равно что отсутствует.
func TestParsedCommandsAreInMenu(t *testing.T) {
	known := map[string]bool{}
	for _, n := range knownCommandNames() {
		known[n] = true
	}
	// Служебные написания, которых в меню нет намеренно: подсказка «?» и
	// синонимы верхнего уровня, показывать которые отдельной строкой значит
	// удвоить список без пользы.
	skip := map[string]bool{"/?": true, "/kb ?": true, "/graph ?": true,
		// Прежнее имя режима no-ask. Работать обязано — оно записано
		// в чужих конфигах, — но в меню ему делать нечего: строка «yolo»
		// человеку ничего не объясняет, ради чего имя и меняли.
		"/mode yolo": true}

	var extra []string
	for name := range parsedNames(t) {
		if !known[name] && !skip[name] {
			extra = append(extra, name)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		t.Errorf("разбираются, но в меню их нет: %s", strings.Join(extra, ", "))
	}
}
