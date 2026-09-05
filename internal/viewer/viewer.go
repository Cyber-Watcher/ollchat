// Открытие книги во внешней программе.
//
// **Зачем.** Найденный в библиотеке кусок можно прочитать в ленте, но иногда
// нужна сама книга: посмотреть рисунок, соседнюю главу, оглавление. Набирать
// путь руками не годится — путь и так известен коллекции.
//
// **Почему отдельным пакетом, а не тремя строчками в интерфейсе.** Запуск
// чужой программы из TUI требует трёх предосторожностей, и каждую легко забыть:
// вывод программы обязан идти в никуда (иначе GUI-просмотрщик пишет
// предупреждения прямо поверх ленты), процесс обязан жить в своей группе
// (иначе Ctrl+C в ollchat закроет и просмотрщик), и ждать его завершения
// нельзя, но подобрать зомби надо.
package viewer

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Commands — чем открывать книгу каждого вида. Пусто — системный просмотрщик.
//
// Строка задаётся как командная: `zathura -P {page} {file}`. Подстановки
// необязательны — без `{file}` путь дописывается в конец.
type Commands struct {
	PDF  string
	EPUB string
	MD   string
}

// Open открывает файл во внешней программе и сразу возвращает управление.
//
// page — страница, на которую хочется попасть; 0 — неизвестна. Она попадает
// в команду только если в настройке есть `{page}`: угадывать ключ страницы
// за просмотрщик нельзя, у каждого он свой (`-P`, `-p`, `--page-index`).
func Open(path string, page int, c Commands) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("у этой книги в коллекции не записан путь к файлу")
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("файла книги нет: %s (книгу переместили или диск не подключён)", path)
	}

	name, args, err := command(path, page, c)
	if err != nil {
		return err
	}
	cmd := exec.Command(name, args...)
	// Вывод — в никуда: строка предупреждения от GUI-программы иначе ляжет
	// поверх ленты, и экран придётся перерисовывать вручную.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("не удалось запустить %s: %w", name, err)
	}
	// Ждать нельзя — просмотрщик живёт минутами, — но и бросать нельзя:
	// незакрытый процесс остаётся зомби до конца работы ollchat.
	go func() { _ = cmd.Wait() }()
	return nil
}

// command выбирает программу и собирает её доводы.
//
// Вынесено отдельно ради проверок: собрать команду можно без запуска чего-либо.
func command(path string, page int, c Commands) (string, []string, error) {
	tmpl := strings.TrimSpace(byExt(path, c))
	if tmpl == "" {
		return system(path)
	}
	fields := strings.Fields(tmpl)
	if len(fields) == 0 {
		return system(path)
	}

	out := make([]string, 0, len(fields))
	hasFile := false
	for _, f := range fields[1:] {
		// Довод со страницей, когда страница неизвестна, выбрасывается целиком:
		// иначе просмотрщик получит `-P {page}` или голый ключ без значения.
		if strings.Contains(f, "{page}") {
			if page <= 0 {
				continue
			}
			f = strings.ReplaceAll(f, "{page}", strconv.Itoa(page))
		}
		if strings.Contains(f, "{file}") {
			f = strings.ReplaceAll(f, "{file}", path)
			hasFile = true
		}
		out = append(out, f)
	}
	if !hasFile {
		out = append(out, path)
	}
	return fields[0], out, nil
}

// byExt — какая настройка отвечает за этот файл.
func byExt(path string, c Commands) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf":
		return c.PDF
	case ".epub":
		return c.EPUB
	case ".md", ".markdown":
		return c.MD
	}
	return ""
}

// system — просмотрщик по умолчанию, тот же, что открывает файл из проводника.
func system(path string) (string, []string, error) {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	if _, err := exec.LookPath(name); err != nil {
		return "", nil, fmt.Errorf("нечем открыть: %s в системе нет — "+
			"укажите программу в разделе [viewers] файла настроек", name)
	}
	return name, []string{path}, nil
}
