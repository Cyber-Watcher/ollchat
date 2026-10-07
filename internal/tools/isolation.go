package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Изоляция команд bash средствами ОС (sandbox.isolation).
//
// **Зачем, когда есть правила deny.** Правила видят строку команды, но не то,
// что сделает запущенная программа: `python -c`, `make`, скрипт, записанный
// на диск минуту назад, делают что угодно, и по строке этого не видно.
// Изоляция ограничивает саму программу: вся система видна ей только для
// чтения, писать можно лишь в корень песочницы и в собственный пустой /tmp,
// каталоги с ключами (isolation_hide) пусты, а без сети наружу не уйти.
//
// **Без изоляции — никогда.** Включённая изоляция, которая не нашлась или
// не запустилась, означает отказ выполнить команду, а не запуск как раньше:
// человек включил её, чтобы не думать о каждой команде в режиме noask,
// и тихий откат вернул бы ровно то, от чего он защищался.

// Isolation — настройки изоляции из раздела [sandbox]. Нулевое значение —
// изоляция выключена, команды запускаются как раньше.
type Isolation struct {
	Kind    string   // "" — выключена, "bwrap" — bubblewrap
	Network bool     // оставить командам сеть
	Hide    []string // абсолютные пути, которые команда видит пустыми
}

// hiddenPath — путь, который команда увидит пустым.
type hiddenPath struct {
	path string
	dir  bool // каталог прячется пустым tmpfs, файл — пустым /dev/null
}

// isolate оборачивает argv в запуск через bubblewrap.
func isolate(iso Isolation, root string, argv []string) ([]string, error) {
	if iso.Kind != "bwrap" {
		return nil, fmt.Errorf("sandbox.isolation = %q: такой изоляции нет — команда не выполнена", iso.Kind)
	}
	bin, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, fmt.Errorf("sandbox.isolation = \"bwrap\", но программа bwrap не найдена (%v): "+
			"поставьте пакет bubblewrap. Без изоляции команда не выполняется", err)
	}
	return append([]string{bin}, bwrapArgs(iso.Network, root, hiddenPaths(iso.Hide), argv)...), nil
}

// hiddenPaths оставляет из списка то, что есть на диске, со ссылками,
// раскрытыми до настоящего места: прятать надо само содержимое.
func hiddenPaths(paths []string) []hiddenPath {
	out := make([]hiddenPath, 0, len(paths))
	for _, p := range paths {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = real
		}
		info, err := os.Stat(p)
		if err != nil {
			continue // нет — прятать нечего
		}
		out = append(out, hiddenPath{path: p, dir: info.IsDir()})
	}
	return out
}

// bwrapArgs собирает ключи bubblewrap для команды argv в корне root.
//
// Порядок важен, bwrap монтирует по очереди: сперва вся система только
// для чтения, свежие /dev, /proc и /tmp, затем корень песочницы на запись
// (после /tmp — иначе корень внутри /tmp пропал бы под пустым tmpfs), затем
// спрятанные пути — поверх корня, если они внутри него. Спрятанный путь,
// внутри которого лежит сам корень, монтируется раньше корня: иначе он
// закрыл бы и корень, и команде некуда было бы перейти.
func bwrapArgs(network bool, root string, hide []hiddenPath, argv []string) []string {
	args := []string{"--die-with-parent", "--unshare-all"}
	if network {
		args = append(args, "--share-net")
	}
	args = append(args, "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp")
	var before, after []string
	for _, h := range hide {
		spec := []string{"--tmpfs", h.path}
		if !h.dir {
			spec = []string{"--ro-bind", "/dev/null", h.path}
		}
		if within(h.path, root) {
			before = append(before, spec...)
		} else {
			after = append(after, spec...)
		}
	}
	args = append(args, before...)
	args = append(args, "--bind", root, root)
	args = append(args, after...)
	args = append(args, "--chdir", root, "--")
	return append(args, argv...)
}

// within сообщает, что path лежит внутри dir или совпадает с ним.
func within(dir, path string) bool {
	return path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, "/")+"/")
}

// bwrapFailed распознаёт отказ самого bwrap: изоляцию не удалось построить
// (нет пространств имён пользователя, недоступен путь), и команда не
// запускалась. bwrap пишет такие ошибки с префиксом «bwrap: » и выходит
// с кодом 1.
func bwrapFailed(exitCode int, output string) bool {
	return exitCode == 1 && strings.HasPrefix(output, "bwrap: ")
}
