package permissions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Класс «необратимое» (этап 112, Н5 и Т9, решение владельца 16.10.2026).
//
// «The reversibility test: can this action be undone in under ten minutes? If
// not, it requires human approval before executing» (раздел 9, `books/658#225`);
// «High: Explicit human approval is required before execution … a rollback path
// must be defined» («Agentic AI for Platform Engineering», 2026, стр. 215–216).
// До 16.10.2026 правила deny → allow → ask силы действия не различали: правка
// файла под git и `git push` шли через одно окно, а в noask — без вопроса обе.
//
// Необратимое спрашивается в safe, auto-edit и noask — даже под правилом allow
// и при разрешении инструмента на сеанс (оно выдаётся на программу целиком:
// «всегда для git» иначе молча пропускало бы `git push`). Без вопроса — только
// в yolo. Запрет deny сильнее: необратимость его не ослабляет.
//
// Что необратимо — список [permissions] irreversible в конфиге (в том же виде,
// что allow и deny) плюс то, чего префиксом правила не записать: ключ в любом
// месте строки (`sed -i`, `find … -delete`, `git reset … --hard`) и `>` в уже
// существующий файл.

// OwnDirName — каталог ollchat в каталоге запуска: конфиг проекта, копии файлов
// перед правкой, позже — скиллы и хуки (решение владельца 16.10.2026).
const OwnDirName = ".ollchat"

// ownDir — запрет модели писать в каталог ollchat: иначе она правит
// собственные разрешения (конфиг проекта) и копии, по которым правку откатывают.
// Запрет в коде, а не в списке deny: у людей свои списки deny в конфигах, и
// новое правило до них не дошло бы. Для bash ловится `>` в этот каталог;
// программа, пишущая туда сама (`cp x .ollchat/…`), строкой не видна —
// от неё защищает только изоляция (sandbox.isolation).
func ownDir(req Request, sb *Sandbox) string {
	if sb == nil {
		return ""
	}
	dir := filepath.Join(sb.RealRoot(), OwnDirName)
	inside := func(p string) bool {
		if !filepath.IsAbs(p) {
			p = filepath.Join(sb.RealRoot(), p)
		}
		p = filepath.Clean(p)
		return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
	}
	switch req.Kind {
	case KindWrite:
		if inside(req.Target) {
			return "каталог " + OwnDirName + " — настройки и копии ollchat: модели писать туда нельзя"
		}
	case KindBash:
		for _, t := range scanCommands(req.Target).truncates {
			if inside(t) {
				return "каталог " + OwnDirName + " — настройки и копии ollchat: модели писать туда нельзя (" + t + ")"
			}
		}
	}
	return ""
}

// SetIrreversible задаёт список правил класса «необратимое».
func (s *Set) SetIrreversible(list []string, root string) error {
	rules, err := compileList(list, root, "permissions.irreversible")
	if err != nil {
		return err
	}
	s.irreversible = rules
	return nil
}

// Irreversible — правила класса «необратимое», для команды /permissions.
func (s *Set) Irreversible() []Rule { return s.irreversible }

// IrreversibleBy объясняет, чем действие необратимо; пусто — обратимо.
// root — корень песочницы: от него считаются относительные цели `>`.
func (s *Set) IrreversibleBy(kind Kind, target, root string) string {
	if kind != KindBash {
		for i := range s.irreversible {
			if s.irreversible[i].Match(kind, target) {
				return fmt.Sprintf("необратимо по правилу %s", s.irreversible[i].Source)
			}
		}
		return ""
	}
	scan := scanCommands(target)
	for _, c := range scan.found {
		for i := range s.irreversible {
			if s.irreversible[i].Kind == KindBash && s.irreversible[i].matchCommand(c.text) {
				return fmt.Sprintf("необратимо по правилу %s (часть команды: %s)", s.irreversible[i].Source, c.text)
			}
		}
		if why := builtinIrreversible(c.text); why != "" {
			return why
		}
	}
	for _, t := range scan.truncates {
		if why := truncatesExisting(t, root); why != "" {
			return why
		}
	}
	return ""
}

// builtinIrreversible — то, что префиксом правила не записать: ключ стоит
// в любом месте строки.
func builtinIrreversible(text string) string {
	words, err := SplitWords(text)
	if err != nil || len(words) == 0 {
		return ""
	}
	prog, args := programName(words[0]), words[1:]
	switch prog {
	case "sed":
		for _, a := range args {
			if a == "--in-place" || strings.HasPrefix(a, "--in-place=") ||
				(strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a[1:], "i")) {
				return "необратимо: sed -i правит файл на месте, мимо копий ollchat"
			}
		}
	case "perl":
		for _, a := range args {
			if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a[1:], "i") {
				return "необратимо: perl -i правит файл на месте, мимо копий ollchat"
			}
		}
	case "find":
		if contains(args, "-delete") {
			return "необратимо: find -delete удаляет найденное"
		}
	case "git":
		// Подкоманда — мимо общих ключей (`git -C dir push`): префикс правила
		// `Bash(git push:*)` такую строку не ловит.
		sub, rest := gitSubcommand(args)
		switch {
		case sub == "push":
			return "необратимо: git push отправляет наружу"
		case sub == "clean" || sub == "restore" || sub == "rebase":
			return "необратимо: git " + sub + " теряет или переписывает несохранённое"
		case sub == "branch" && (contains(rest, "-D") || contains(rest, "--delete") && contains(rest, "--force")):
			return "необратимо: git branch -D удаляет ветку без проверки слияния"
		case sub == "stash" && len(rest) > 0 && (rest[0] == "drop" || rest[0] == "clear"):
			return "необратимо: git stash " + rest[0] + " удаляет отложенные правки"
		case sub == "reset" && contains(rest, "--hard"):
			return "необратимо: git reset --hard теряет несохранённые правки"
		case sub == "checkout" && (contains(rest, "--") || contains(rest, ".")):
			return "необратимо: git checkout -- / . теряет несохранённые правки"
		}
	}
	return ""
}

// gitSubcommand — подкоманда git и её доводы, мимо общих ключей (-C, -c …).
func gitSubcommand(args []string) (string, []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-C" || a == "-c" || a == "--git-dir" || a == "--work-tree" || a == "--namespace":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return a, args[i+1:]
		}
	}
	return "", nil
}

// truncatesExisting — `>` обнулит уже существующий обычный файл?
func truncatesExisting(target, root string) string {
	if target == "" || strings.HasPrefix(target, "/dev/") {
		return ""
	}
	p := target
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	} else if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
		return fmt.Sprintf("необратимо: «> %s» перезапишет существующий файл", target)
	}
	return ""
}
