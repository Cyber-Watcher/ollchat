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
		lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		for i, l := range lines {
			if i == 60 {
				fmt.Fprintf(out, "  … ещё строк: %d\n", len(lines)-60)
				break
			}
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

// stdinIsTerminal — ввод с терминала, то есть человек может ответить на вопрос.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
