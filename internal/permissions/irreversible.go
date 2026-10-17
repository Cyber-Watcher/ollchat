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
// что allow и deny) плюс КЛАСС, которого списком не записать (аудит
// 16.10.2026, находка 1: список из 22 правил пропускал в noask 33 из 60
// команд таблицы — `cp /dev/null main.go`, `tee`, `install`, `ln -sf`,
// `python3 -c "open(…,'w')"`, `git commit --amend`; чинить по классу — слово
// владельца):
//   - перезапись существующего файла любой пишущей программой (`writeTargets`:
//     cp, install, ln -f, mv, tee, sort -o, dd of=, rsync, sed/perl -i, awk -i
//     inplace, ed, truncate, shred) и `>` в него;
//   - код в строке интерпретатора (`python -c`, `perl -e`, `ruby -e`, `node -e`,
//     `php -r`): что он пишет, не разобрать — считается необратимым;
//   - `find -delete`, `rsync --delete`, распаковка `tar -x` без `--keep-old-files`;
//   - подкоманды git, теряющие данные или историю: push, clean, restore, rebase,
//     reset --hard, checkout -- / . / <существующий путь>, branch -D, stash
//     drop|clear, commit --amend, tag -d, switch -f|--discard-changes, update-ref
//     -d, reflog expire|delete, gc --prune, prune, filter-branch, rm без --cached.
// Таблица обходов — контрактный тест `TestIrreversibleClassAsksInNoask`.

// OwnDirName — каталог ollchat в каталоге запуска: конфиг проекта, копии файлов
// перед правкой, позже — скиллы и хуки (решение владельца 16.10.2026).
const OwnDirName = ".ollchat"

// GitDirName — каталог git: его конфиг и хуки исполняются разрешёнными по
// умолчанию `git status|diff|log` (`core.fsmonitor`, `core.pager`, хуки), так
// что запись туда — исполнение чего угодно мимо deny (аудит 16.10.2026,
// находка 3; закрыт по слову владельца).
const GitDirName = ".git"

// protectedDirs — каталоги, куда модели писать нельзя ни в каком режиме,
// даже в yolo: иначе она правит собственные разрешения, копии, по которым
// правку откатывают, и конфиг git, который исполняется.
var protectedDirs = []struct{ name, why string }{
	{OwnDirName, "настройки и копии ollchat"},
	{GitDirName, "конфиг и хуки git исполняются разрешёнными командами git"},
}

// protectedDir — запрет модели писать в каталоги protectedDirs. Запрет в коде,
// а не в списке deny: у людей свои списки deny в конфигах, и новое правило до
// них не дошло бы. Для bash проверяются ВСЕ цели записи строки: `>`, `>>`,
// цели пишущих программ (`writeTargets`), `cd` внутрь и цели с подстановкой,
// в тексте которых стоит имя каталога (аудит 16.10.2026, находка 2: раньше
// ловился только `>`, а `>>`, `cp`, `tee`, `python` проходили даже в yolo).
// От программы, пишущей туда по своему разумению без имени каталога в строке,
// защищает только изоляция (sandbox.isolation).
//
// scan — готовый разбор req.Target из checkBash; nil — разобрать здесь.
// Для действий, кроме bash, не читается.
func protectedDir(req Request, sb *Sandbox, scan *commandScan) string {
	if sb == nil {
		return ""
	}
	root := sb.RealRoot()
	refuse := func(p string) string {
		if p == "" || strings.HasPrefix(p, "/dev/") {
			return ""
		}
		// `~/проект/.git/…` — тот же каталог через дом (второй круг 16.10:
		// ревью нашло, что `~/` пропускался мимо защиты).
		p = filepath.Clean(resolveTarget(p, root))
		for _, d := range protectedDirs {
			dir := filepath.Join(root, d.name)
			if p == dir || strings.HasPrefix(p, dir+string(filepath.Separator)) {
				return fmt.Sprintf("каталог %s — %s: модели писать туда нельзя", d.name, d.why)
			}
		}
		return ""
	}
	// Имя каталога — отдельная часть пути (`/.git/`, `/.git` в конце, `.git/` в
	// начале), а не хвост чужого имени: `proj.git` зеркала — не наш каталог.
	mentions := func(raw string) string {
		for _, d := range protectedDirs {
			if raw == d.name || strings.HasPrefix(raw, d.name+"/") ||
				strings.Contains(raw, "/"+d.name+"/") || strings.HasSuffix(raw, "/"+d.name) {
				return fmt.Sprintf("каталог %s — %s: цель с подстановкой «%s» его называет, модели писать туда нельзя", d.name, d.why, raw)
			}
		}
		return ""
	}
	switch req.Kind {
	case KindWrite:
		return refuse(req.Target)
	case KindBash:
		if scan == nil {
			scan = scanCommands(req.Target)
		}
		for _, t := range scan.truncates {
			if why := refuse(t); why != "" {
				return why + " (> " + t + ")"
			}
		}
		for _, t := range scan.appends {
			if why := refuse(t); why != "" {
				return why + " (>> " + t + ")"
			}
		}
		for _, raw := range scan.dynWrites {
			if why := mentions(raw); why != "" {
				return why
			}
		}
		for _, c := range scan.found {
			over, touch, _ := writeTargets(c.text)
			for _, t := range append(over, touch...) {
				if why := refuse(t); why != "" {
					return why + " (" + c.text + ")"
				}
			}
			if c.name == "cd" || c.name == "pushd" {
				if words, err := SplitWords(c.text); err == nil && len(words) > 1 {
					if why := refuse(words[len(words)-1]); why != "" {
						return why + " (" + c.text + ")"
					}
				}
			}
		}
	}
	return ""
}

// IsEnvName — имя файла с тайнами окружения: ровно `.env` или `.env.<что
// угодно>` (`.env.local`, `.env.prod`, `.env.example` и копия `.env.150405`
// в .ollchat/backups). `.envrc`, `.environment`, `env.txt`, `my.env` — не они.
// name — последний элемент пути, а не путь.
func IsEnvName(name string) bool {
	return name == ".env" || strings.HasPrefix(name, ".env.")
}

// envIn — среди элементов пути p есть имя .env или .env.*. Внутри корня root
// судятся только элементы ниже него: проект, лежащий в `~/.env.d/proj`, не
// закрывает сам себя. root == "" — судятся все элементы.
func envIn(p, root string) bool {
	p = filepath.Clean(p)
	if root != "" {
		if rel, err := filepath.Rel(root, p); err == nil && rel != ".." &&
			!strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			p = rel
		}
	}
	for _, el := range strings.Split(p, string(filepath.Separator)) {
		if IsEnvName(el) {
			return true
		}
	}
	return false
}

// EnvPath — путь p ведёт к тайнам окружения: сам файл `.env`/`.env.*`, что
// угодно внутри КАТАЛОГА с таким именем (`.env/prod`) или ссылка на одно из
// них (`notes.txt → sub/.env`, `cfg → .env/`). Относительный путь и `~/`
// считаются от корня root, элементы пути судятся ниже него (envIn).
//
// Ссылка, которой нет на диске (`x → .env`, а `.env` ещё не создан), судится
// по тексту ссылки: запись через неё создала бы сам `.env`.
func EnvPath(p, root string) bool {
	abs := resolveTarget(p, root)
	if envIn(abs, root) {
		return true
	}
	realRoot := root
	if r, err := filepath.EvalSymlinks(root); err == nil {
		realRoot = r
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return envIn(real, realRoot)
	}
	if link, err := os.Readlink(abs); err == nil {
		if !filepath.IsAbs(link) {
			link = filepath.Join(filepath.Dir(abs), link)
		}
		return envIn(link, root) || envIn(link, realRoot)
	}
	return false
}

// envFile — запрет модели читать и писать `.env` и `.env.*` в ЛЮБОМ каталоге
// и что угодно в каталоге с таким именем (этап 119, решение владельца
// 17.10.2026: в коде, без исключений, включая `.env.example`). Правила шаблона
// `Read(./.env)` и `Read(./.env.*)` закрывали только корень песочницы, а
// `Read(./**)` разрешал `sub/.env`. Запрет в коде, а не в списке deny, по той
// же причине, что protectedDir: у людей свои списки deny. Действует во всех
// режимах, даже в yolo, как защищённые каталоги: yolo снимает вопросы, а не
// запреты, и тайна, ушедшая в модель, назад не возвращается. Запись закрыта
// тоже: `edit_file .env` показал бы модели старый текст в ответе об ошибке
// сверки, а запись поверх — подмена тайн, которой человек не заметит.
//
// Цели KindRead — read_file, grep, list_dir, view_image, чтение исходника
// scan_redact, /add; KindWrite — write_file, edit_file. Файлы .env внутри
// каталога, по которому идёт grep, отсекает сам grep (tools.grepSearch, по
// IsEnvName/EnvPath). Строку bash судит envBash.
func envFile(req Request, root string) string {
	if req.Kind != KindRead && req.Kind != KindWrite || !EnvPath(req.Target, root) {
		return ""
	}
	verb := "читать"
	if req.Kind == KindWrite {
		verb = "писать"
	}
	return fmt.Sprintf("%s — переменные окружения с тайнами (.env, .env.*, каталог .env): модели %s его нельзя ни в каком режиме", req.Target, verb)
}

// envBash — запрет строки bash, которая называет `.env`: в любом слове любой
// команды строки, в теле `sh -c` и eval, в цели перенаправления (`< .env`,
// `> .env`), в аргументе ключа (`--env-file=.env`, `if=.env`) и в коде
// интерпретатора (`python3 -c "open('.env')"`). Слово делится на части по
// знакам, которых в пути не бывает (пробел, кавычки, скобки, `=`, `,`, `:`,
// `;`), и каждая часть судится как путь (EnvPath). Шаблон имён (`.e*`,
// `*/.env*`) раскрывается по диску, как его раскроет оболочка: `*` не берёт
// имя с точкой в начале, если точки нет в самом шаблоне.
//
// Чего разбор строки не видит: программы, читающие каталог целиком (`grep -r
// KEY .`, `cat *` при dotglob, `tar c .`, `find -exec cat`), имя, собранное
// при запуске (`cat $(printf .en)v`), и скрипт, записанный раньше. От них
// защищает только изоляция (sandbox.isolation).
func envBash(scan *commandScan, root string) string {
	for _, w := range scan.words {
		for _, part := range strings.FieldsFunc(w.text, notPathRune) {
			if EnvPath(part, root) {
				return fmt.Sprintf("строка называет %s — переменные окружения с тайнами (.env, .env.*, каталог .env): модели читать и писать их нельзя ни в каком режиме", part)
			}
			if w.dynamic && strings.ContainsAny(part, "*?[") {
				if m := envGlob(part, root); m != "" {
					return fmt.Sprintf("шаблон %s раскроется в %s — переменные окружения с тайнами: модели читать и писать их нельзя ни в каком режиме", part, m)
				}
			}
		}
	}
	return ""
}

// notPathRune — знак, которого в пути из командной строки не бывает.
func notPathRune(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '"', '\'', '`', '(', ')', '=', ',', ':', ';', '<', '>', '|', '&', '{', '}', '$':
		return true
	}
	return false
}

// envGlob раскрывает шаблон имён по диску и возвращает первое совпадение,
// ведущее к .env; "" — таких нет. Совпадение засчитывается, как его засчитает
// оболочка без dotglob: элемент с точкой в начале — только если шаблон этого
// элемента тоже начинается с точки.
func envGlob(pattern, root string) string {
	abs := resolveTarget(pattern, root)
	matches, err := filepath.Glob(abs)
	if err != nil {
		return ""
	}
	pat := strings.Split(filepath.Clean(abs), string(filepath.Separator))
	for _, m := range matches {
		els := strings.Split(filepath.Clean(m), string(filepath.Separator))
		if len(els) != len(pat) {
			continue
		}
		hidden := false
		for i := range els {
			if strings.HasPrefix(els[i], ".") && !strings.HasPrefix(pat[i], ".") {
				hidden = true
				break
			}
		}
		if !hidden && EnvPath(m, root) {
			return m
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
// root — корень песочницы: от него считаются относительные цели.
func (s *Set) IrreversibleBy(kind Kind, target, root string) string {
	return s.irreversibleBy(kind, target, root, nil)
}

// irreversibleBy — IrreversibleBy с готовым разбором строки bash из checkBash
// (ревью, замечание 7: строка лексилась заново). scan == nil — разобрать здесь;
// для действий, кроме bash, scan не читается.
func (s *Set) irreversibleBy(kind Kind, target, root string, scan *commandScan) string {
	if kind != KindBash {
		for i := range s.irreversible {
			if s.irreversible[i].Match(kind, target) {
				return fmt.Sprintf("необратимо по правилу %s", s.irreversible[i].Source)
			}
		}
		return ""
	}
	if scan == nil {
		scan = scanCommands(target)
	}
	for _, c := range scan.found {
		for i := range s.irreversible {
			if s.irreversible[i].Kind == KindBash && s.irreversible[i].matchCommand(c.text) {
				return fmt.Sprintf("необратимо по правилу %s (часть команды: %s)", s.irreversible[i].Source, c.text)
			}
		}
		if why := builtinIrreversible(c.text, root); why != "" {
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

// builtinIrreversible — класс, которого префиксом правила не записать: ключ
// стоит в любом месте строки, цель существует на диске, код в строке не
// разобрать. root — корень песочницы для относительных целей.
func builtinIrreversible(text, root string) string {
	words, err := SplitWords(text)
	if err != nil || len(words) == 0 {
		return ""
	}
	prog, args := programName(words[0]), words[1:]
	switch prog {
	case "sed":
		for _, a := range args {
			if a == "--in-place" || strings.HasPrefix(a, "--in-place=") || shortHas(a, 'i') {
				return "необратимо: sed -i правит файл на месте, мимо копий ollchat"
			}
		}
	case "perl":
		for _, a := range args {
			if shortHas(a, 'i') {
				return "необратимо: perl -i правит файл на месте, мимо копий ollchat"
			}
		}
	case "awk", "gawk", "mawk", "nawk":
		if awkInPlace(args) {
			return "необратимо: awk -i inplace правит файл на месте, мимо копий ollchat"
		}
	case "find":
		if contains(args, "-delete") {
			return "необратимо: find -delete удаляет найденное"
		}
	case "rsync":
		for _, a := range args {
			if strings.HasPrefix(a, "--delete") || a == "--remove-source-files" {
				return "необратимо: rsync " + a + " удаляет файлы"
			}
		}
	case "tar":
		if tarExtracts(args) {
			return "необратимо: tar -x распаковывает поверх существующих файлов (без --keep-old-files)"
		}
	case "git":
		if why := gitIrreversible(args, root); why != "" {
			return why
		}
	}
	over, _, opaque := writeTargets(text)
	for _, t := range over {
		if existingFile(t, root) {
			return fmt.Sprintf("необратимо: «%s» перепишет существующий файл %s", prog, t)
		}
	}
	if opaque != "" {
		return "необратимо: код в строке (" + opaque + ") не разобрать — что он пишет и удаляет, неизвестно"
	}
	return ""
}

// gitIrreversible — подкоманды git, теряющие правки, ветки или историю.
// Подкоманда ищется мимо общих ключей (`git -C dir push`): префикс правила
// `Bash(git push:*)` такую строку не ловит.
func gitIrreversible(args []string, root string) string {
	sub, rest := gitSubcommand(args)
	switch {
	case sub == "push":
		return "необратимо: git push отправляет наружу"
	case sub == "clean" || sub == "restore" || sub == "rebase" || sub == "filter-branch" || sub == "prune":
		return "необратимо: git " + sub + " теряет или переписывает несохранённое"
	case sub == "branch" && (contains(rest, "-D") || contains(rest, "--delete") && contains(rest, "--force")):
		return "необратимо: git branch -D удаляет ветку без проверки слияния"
	case sub == "stash" && len(rest) > 0 && (rest[0] == "drop" || rest[0] == "clear"):
		return "необратимо: git stash " + rest[0] + " удаляет отложенные правки"
	case sub == "reset" && contains(rest, "--hard"):
		return "необратимо: git reset --hard теряет несохранённые правки"
	case sub == "checkout" && (contains(rest, "--") || contains(rest, ".")):
		return "необратимо: git checkout -- / . теряет несохранённые правки"
	case sub == "checkout" && !contains(rest, "-b") && !contains(rest, "-B") && !contains(rest, "--orphan"):
		for _, a := range rest {
			if !strings.HasPrefix(a, "-") && existingFile(a, root) {
				return "необратимо: git checkout " + a + " — существующий файл, его правки пропадут"
			}
		}
	case sub == "commit" && contains(rest, "--amend"):
		return "необратимо: git commit --amend переписывает последний коммит"
	case sub == "tag" && (contains(rest, "-d") || contains(rest, "--delete")):
		return "необратимо: git tag -d удаляет тег"
	case sub == "switch" && (contains(rest, "-f") || contains(rest, "--force") || contains(rest, "--discard-changes") || contains(rest, "-C") || contains(rest, "--force-create")):
		return "необратимо: git switch с -f/--discard-changes теряет несохранённые правки"
	case sub == "update-ref" && (contains(rest, "-d") || contains(rest, "--delete")):
		return "необратимо: git update-ref -d удаляет ссылку"
	case sub == "reflog" && len(rest) > 0 && (rest[0] == "expire" || rest[0] == "delete"):
		return "необратимо: git reflog " + rest[0] + " стирает историю ссылок — после него не вернуть и reset --hard"
	case sub == "gc":
		for _, a := range rest {
			if strings.HasPrefix(a, "--prune") {
				return "необратимо: git gc --prune удаляет недостижимые объекты"
			}
		}
	case sub == "rm" && !contains(rest, "--cached") && !contains(rest, "-n") && !contains(rest, "--dry-run"):
		return "необратимо: git rm удаляет файл с диска вместе с несохранёнными правками"
	case sub == "worktree" && len(rest) > 0 && rest[0] == "remove" && (contains(rest, "-f") || contains(rest, "--force")):
		return "необратимо: git worktree remove -f удаляет рабочее дерево с правками"
	case sub == "replace" && (contains(rest, "-d") || contains(rest, "--delete")):
		return "необратимо: git replace -d удаляет замену"
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

// writeTargets — какие файлы команда перепишет (over) или создаст либо тронет
// (touch), судя по программе и её ключам; opaque — код в строке
// интерпретатора, который не разобрать. Пути — как в строке, относительные
// считаются от корня песочницы.
func writeTargets(text string) (over, touch []string, opaque string) {
	words, err := SplitWords(text)
	if err != nil || len(words) == 0 {
		return nil, nil, ""
	}
	prog, args := programName(words[0]), words[1:]
	pos := positionals(args)
	intoDir := func(dir string, srcs []string) []string {
		out := make([]string, 0, len(srcs))
		for _, s := range srcs {
			out = append(out, filepath.Join(dir, filepath.Base(s)))
		}
		return out
	}
	// Цель копирования: последний довод или -t DIR; в каталог — по имени источника.
	copyLike := func() []string {
		if dir := optValue(args, "t", "target-directory"); dir != "" {
			return intoDir(dir, pos)
		}
		if len(pos) < 2 {
			return nil
		}
		dst, srcs := pos[len(pos)-1], pos[:len(pos)-1]
		return append([]string{dst}, intoDir(dst, srcs)...)
	}
	switch prog {
	case "cp", "install", "mv":
		over = copyLike()
	case "ln":
		if hasShort(args, 'f') || contains(args, "--force") {
			over = copyLike()
		} else if len(pos) >= 2 {
			touch = []string{pos[len(pos)-1]}
		}
	case "tee":
		if hasShort(args, 'a') || contains(args, "--append") {
			touch = pos
		} else {
			over = pos
		}
	case "sort":
		if o := optValue(args, "o", "output"); o != "" {
			over = []string{o}
		}
	case "dd":
		for _, a := range args {
			if strings.HasPrefix(a, "of=") {
				over = append(over, a[3:])
			}
		}
	case "rsync":
		if len(pos) >= 2 {
			over = []string{pos[len(pos)-1]}
		}
	case "sed":
		if contains(args, "--in-place") || hasShort(args, 'i') || hasLongPrefix(args, "--in-place=") {
			over = pos
		}
	case "perl":
		if hasShort(args, 'i') {
			over = pos
		}
		if hasShort(args, 'e') || hasShort(args, 'E') {
			opaque = "perl -e"
		}
	case "awk", "gawk", "mawk", "nawk":
		if awkInPlace(args) && len(pos) > 1 {
			over = pos[1:]
		}
	case "ed", "ex", "truncate", "shred":
		over = pos
	case "touch", "mkdir", "chmod", "chown", "chgrp", "rm", "rmdir", "unlink", "mkfifo", "mknod":
		touch = pos
	case "tar":
		if dir := optValue(args, "C", "directory"); dir != "" {
			touch = []string{dir}
		} else if tarExtracts(args) {
			touch = []string{"."}
		}
	case "python", "python2", "python3", "pypy", "pypy3":
		if interpreterOpt(args, "c", "") {
			opaque = prog + " -c"
		}
	case "ruby", "node", "nodejs", "deno", "bun", "lua", "luajit":
		if interpreterOpt(args, "ep", "--eval") {
			opaque = prog + " -e"
		}
	case "php":
		if interpreterOpt(args, "r", "") {
			opaque = "php -r"
		}
	}
	return over, touch, opaque
}

// interpreterOpt — ключ кода в строке стоит среди ключей САМОГО интерпретатора,
// то есть до первого позиционного довода (имени скрипта), `-m` или `--`.
// `python3 foo.py -c config.yaml` — ключ скрипта, а не интерпретатора
// (второй круг 16.10: ревью нашло ложные срабатывания).
func interpreterOpt(args []string, shorts, long string) bool {
	for _, a := range args {
		switch {
		case a == "--" || a == "-m" || a == "-":
			return false
		case long != "" && a == long:
			return true
		case len(a) > 1 && a[0] == '-' && a[1] != '-':
			for j := 1; j < len(a); j++ {
				if strings.IndexByte(shorts, a[j]) >= 0 {
					return true
				}
			}
		case strings.HasPrefix(a, "--"):
		default:
			return false // имя скрипта: дальше ключи его, а не интерпретатора
		}
	}
	return false
}

// positionals — доводы команды без ключей: всё после «--» и слова не с «-».
// Значения ключей (`-o файл`) сюда тоже попадают: пишущая программа их и так
// перепишет, а лишний путь в проверке «существует ли» безвреден.
func positionals(args []string) []string {
	var out []string
	for i, a := range args {
		if a == "--" {
			return append(out, args[i+1:]...)
		}
		if len(a) > 1 && a[0] == '-' {
			continue
		}
		out = append(out, a)
	}
	return out
}

// optValue — значение короткого ключа `-X файл` / `-Xфайл` или длинного
// `--name файл` / `--name=файл`; пусто — ключа нет.
func optValue(args []string, short, long string) string {
	for i, a := range args {
		switch {
		case a == "--":
			return ""
		case long != "" && a == "--"+long:
			if i+1 < len(args) {
				return args[i+1]
			}
		case long != "" && strings.HasPrefix(a, "--"+long+"="):
			return a[len(long)+3:]
		case short != "" && len(a) > 1 && a[0] == '-' && a[1] != '-':
			if j := strings.IndexByte(a[1:], short[0]); j >= 0 {
				if rest := a[j+2:]; rest != "" {
					return rest
				}
				if i+1 < len(args) {
					return args[i+1]
				}
			}
		}
	}
	return ""
}

// shortHas — короткий ключ или связка (`-pi`, `-sf`) содержит букву c.
func shortHas(a string, c byte) bool {
	return len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.IndexByte(a[1:], c) >= 0
}

func hasShort(args []string, c byte) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if shortHas(a, c) {
			return true
		}
	}
	return false
}

func hasLongPrefix(args []string, p string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, p) {
			return true
		}
	}
	return false
}

// awkInPlace — gawk `-i inplace` / `--in-place` / `-iinplace`.
func awkInPlace(args []string) bool {
	for i, a := range args {
		switch {
		case a == "--in-place" || strings.HasPrefix(a, "--in-place="):
			return true
		case a == "-i" && i+1 < len(args) && args[i+1] == "inplace":
			return true
		case a == "-iinplace":
			return true
		}
	}
	return false
}

// tarExtracts — распаковка без защиты существующих файлов.
func tarExtracts(args []string) bool {
	extract := false
	for i, a := range args {
		switch {
		case a == "--extract" || a == "--get":
			extract = true
		case strings.HasPrefix(a, "--"):
		case len(a) > 1 && a[0] == '-' && strings.IndexByte(a[1:], 'x') >= 0:
			extract = true
		case i == 0 && !strings.HasPrefix(a, "-") && strings.IndexByte(a, 'x') >= 0 && len(a) <= 5:
			extract = true // старый стиль: `tar xf a.tar`
		}
	}
	if !extract {
		return false
	}
	for _, a := range args {
		if a == "--keep-old-files" || a == "--skip-old-files" || a == "--keep-newer-files" || shortHas(a, 'k') {
			return false
		}
	}
	return true
}

// resolveTarget — путь цели от корня песочницы; `~/` — от дома.
func resolveTarget(target, root string) string {
	p := target
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	} else if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	return p
}

// existingFile — цель есть на диске и это обычный файл: перезапись необратима.
func existingFile(target, root string) bool {
	if target == "" || strings.HasPrefix(target, "/dev/") || target == "." || target == ".." {
		return false
	}
	fi, err := os.Stat(resolveTarget(target, root))
	return err == nil && fi.Mode().IsRegular()
}

// truncatesExisting — `>` обнулит уже существующий обычный файл?
func truncatesExisting(target, root string) string {
	if existingFile(target, root) {
		return fmt.Sprintf("необратимо: «> %s» перезапишет существующий файл", target)
	}
	return ""
}
