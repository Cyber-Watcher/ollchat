package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/config"
)

// useProjectConfig накладывает конфиг проекта (.ollchat/config.toml в каталоге
// запуска dir) поверх общего — только после доверия человека (решение владельца
// 16.10.2026, internal/config/project.go). Не доверялся — вопрос, если ввод с
// терминала; иначе (скрипт, --ask из cron) конфиг не применяется и об этом
// сказано в out. Зовётся после dispatchCLI: обслуживание библиотеки и графа
// конфигом проекта не меняется.
func useProjectConfig(cfg *config.Config, dir string, in io.Reader, out io.Writer, interactive bool, trustFile string) error {
	path, err := filepath.Abs(config.ProjectPath(dir))
	if err != nil {
		return err
	}
	data, err := config.ReadProject(path)
	if err != nil || data == nil {
		return err
	}
	sum := config.ProjectSum(data)
	if !config.ProjectTrusted(trustFile, path, sum) {
		if !interactive {
			fmt.Fprintf(out, "конфиг проекта %s НЕ применён: ему ещё не доверяли — запустите ollchat в этом каталоге с терминала и ответьте «да»\n", path)
			return nil
		}
		fmt.Fprintf(out, "\nВ каталоге запуска есть конфиг проекта %s:\n\n", path)
		for _, l := range previewLines(strings.Split(strings.TrimRight(string(data), "\n"), "\n")) {
			fmt.Fprintf(out, "  %s\n", l)
		}
		fmt.Fprint(out, "\nОн переопределяет общий конфиг и может менять режим подтверждений, разрешения и сервер.\n"+
			"Доверять ему? Ответ запомнится, пока файл не изменится. [д/N] ")
		answer, _ := bufio.NewReader(in).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "д", "да", "y", "yes":
		default:
			fmt.Fprintln(out, "конфиг проекта не применён")
			return nil
		}
		if err := config.TrustProject(trustFile, path, sum); err != nil {
			return fmt.Errorf("доверие к конфигу проекта не записано: %w", err)
		}
	}
	if err := cfg.ApplyProject(path, data); err != nil {
		return err
	}
	fmt.Fprintf(out, "конфиг проекта: %s\n", path)
	return nil
}

// previewHead — сколько первых строк конфига проекта показывать целиком.
const previewHead = 60

// previewLines — что показать человеку перед вопросом о доверии (аудит
// 16.10.2026, находка 4). Два правила:
//   - управляющие символы (ESC и прочие) печатаются видимо (`\x1b`): строка
//     комментария с ESC-последовательностью чистила экран и прятала показанное;
//   - после previewHead строк всё равно показываются строки с заголовками
//     разделов и ключами, которые меняют права, режим и сервер: доверие
//     даётся на отпечаток ВСЕГО файла, и `mode = "yolo"` на 61-й строке не
//     должен прятаться за «… ещё строк: N».
func previewLines(lines []string) []string {
	out := make([]string, 0, len(lines)+2)
	hidden := 0
	open := false // внутри многострочного массива ключа: `allow = [` … `]`
	for i, l := range lines {
		key := previewKey(l)
		switch {
		case i < previewHead || key || open:
			if hidden > 0 {
				out = append(out, fmt.Sprintf("  … пропущено строк: %d", hidden))
				hidden = 0
			}
			out = append(out, visible(l))
		default:
			hidden++
		}
		// Массив правил на несколько строк показывается целиком: `allow = [`
		// без `]` открывает его, строка с `]` закрывает (второй круг 16.10).
		switch {
		case key && strings.Count(l, "[") > strings.Count(l, "]"):
			open = true
		case open && strings.Contains(l, "]"):
			open = false
		}
	}
	if hidden > 0 {
		out = append(out, fmt.Sprintf("  … пропущено строк: %d", hidden))
	}
	return out
}

// previewKey — строка, которую нельзя прятать: заголовок раздела или ключ,
// меняющий права, режим, песочницу или сервер.
func previewKey(l string) bool {
	t := strings.TrimSpace(l)
	if strings.HasPrefix(t, "[") {
		return true
	}
	key, _, ok := strings.Cut(t, "=")
	if !ok {
		return false
	}
	switch strings.TrimSpace(key) {
	case "mode", "allow", "ask", "deny", "irreversible", "tools", "url", "root", "allow_outside",
		"follow_symlinks", "isolation", "default", "autosave", "untrusted_gate", "private_egress_check",
		"journal_fail_closed", "max_writes_noask", "dir", "token", "headers":
		return true
	}
	return false
}

// visible делает управляющие символы видимыми: `\x1b`, `\r`, `\x07`.
func visible(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteString("    ")
		case r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0:
			fmt.Fprintf(&b, "\\x%02x", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// stdinIsTerminal — ввод с терминала, то есть человек может ответить на вопрос.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
