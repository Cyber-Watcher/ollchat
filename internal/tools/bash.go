package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/Cyber-Watcher/ollchat/internal/textx"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
)

type bashTool struct{ opts Options }

func (t *bashTool) Name() string { return NameBash }

func (t *bashTool) Spec() ollama.Tool {
	return ollama.Tool{Type: "function", Function: ollama.ToolSpec{
		Name: NameBash,
		Description: "Выполняет команду в рабочем каталоге и возвращает её вывод. " +
			"Опасные команды запрещены настройками, остальные требуют подтверждения пользователя.",
		Parameters: ollama.ToolParams{
			Type: "object",
			Properties: map[string]ollama.ToolProp{
				"command": {Type: "string", Description: "Командная строка целиком, например: go test ./..."},
				"timeout": {Type: "integer", Description: "Предельное время выполнения в секундах"},
			},
			Required: []string{"command"},
		},
	}}
}

func (t *bashTool) Plan(args map[string]any) (*Plan, error) {
	cmd, err := requireString(args, "command")
	if err != nil {
		return nil, err
	}
	cmd = strings.TrimSpace(cmd)
	if err := refuseToolAsCommand(cmd, t.opts.CanViewImages); err != nil {
		return nil, err
	}

	timeout := t.opts.BashTimeout
	if sec := argInt(args, "timeout", 0); sec > 0 {
		timeout = time.Duration(sec) * time.Second
	}

	preview := cmd
	if permissions.IsCompound(cmd) {
		preview = cmd + "\n\n(составная команда — будет выполнена через оболочку sh)"
	}

	return &Plan{
		Tool:    NameBash,
		Req:     permissions.Request{Kind: permissions.KindBash, Target: cmd, Tool: NameBash},
		Title:   fmt.Sprintf("%s(%s)", NameBash, textx.ShortenOneLine(cmd, 70)),
		Preview: preview,
		Run: func(ctx context.Context) (string, error) {
			return runCommand(ctx, cmd, t.opts, timeout)
		},
	}, nil
}

// Сколько ждать завершения после сигнала и сколько — дочитывания вывода.
const (
	killGrace = 2 * time.Second
	readGrace = 500 * time.Millisecond
)

// runCommand выполняет команду в корне песочницы.
//
// Простая команда запускается напрямую, без оболочки: так подстановки и
// перенаправления не могут появиться неожиданно. Составная команда требует
// оболочки, но она в любом случае проходит через подтверждение пользователя.
//
// Функция обязана возвращать управление всегда: по завершении команды,
// по таймауту или по отмене. Наивная реализация через exec.CommandContext
// и cmd.Run() этого не обеспечивает — подробности в комментарии к waitOrKill.
func runCommand(ctx context.Context, command string, opts Options, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if permissions.IsCompound(command) {
		cmd = exec.Command("sh", "-c", command)
	} else {
		argv, err := shellSplit(command)
		if err != nil {
			return "", err
		}
		if len(argv) == 0 {
			return "", errors.New("пустая команда")
		}
		bin, err := exec.LookPath(argv[0])
		if err != nil {
			return "", fmt.Errorf("команда %q не найдена: %w", argv[0], err)
		}
		cmd = exec.Command(bin, argv[1:]...)
	}

	cmd.Dir = opts.Sandbox.Root()
	// Окружение наследуется: без PATH и HOME большинство инструментов разработки
	// не работает. Отключаем только интерактивность.
	cmd.Env = append(os.Environ(),
		"TERM=dumb",
		"GIT_PAGER=cat",
		"PAGER=cat",
		"OLLCHAT=1",
	)
	cmd.Stdin = nil
	setProcessGroup(cmd)

	// Вывод собираем через собственный канал из *os.File. Это принципиально:
	// когда Stdout — это bytes.Buffer, пакет exec заводит горутину копирования
	// и ждёт её в Wait(). Горутина не завершится, пока канал открыт хоть у
	// одного потомка, и Wait() зависает навсегда. Передавая *os.File, мы отдаём
	// дескриптор напрямую и ничего не ждём.
	pr, pw, err := os.Pipe()
	if err != nil {
		return "", fmt.Errorf("создание канала вывода: %w", err)
	}
	cmd.Stdout = pw
	cmd.Stderr = pw

	start := time.Now()
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return "", err
	}
	// Свою копию записывающего конца закрываем сразу, иначе не увидим конец файла.
	pw.Close()

	outCh := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		// Читаем с запасом к пределу вывода: обрезка ниже сделает остальное.
		limit := int64(opts.MaxOutputKB)*1024 + 4096
		_, _ = io.Copy(&buf, io.LimitReader(pr, limit))
		outCh <- buf.String()
	}()

	runErr, killed := waitOrKill(ctx, cmd)
	elapsed := time.Since(start)

	// Закрытие читающего конца снимает горутину чтения, даже если канал всё ещё
	// держит выживший потомок.
	pr.Close()
	var text string
	select {
	case text = <-outCh:
	case <-time.After(readGrace):
		text = "(вывод не удалось дочитать: команда оставила после себя работающие процессы)"
	}

	if killed {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return opts.truncate(text), fmt.Errorf(
				"команда прервана по таймауту %s (дерево процессов снято)", timeout)
		}
		return opts.truncate(text), fmt.Errorf("выполнение прервано пользователем (дерево процессов снято)")
	}

	var header string
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			header = fmt.Sprintf("Код возврата: %d (за %s)\n\n", ee.ExitCode(), elapsed.Round(time.Millisecond))
		} else {
			return opts.truncate(text), runErr
		}
	} else {
		header = fmt.Sprintf("Код возврата: 0 (за %s)\n\n", elapsed.Round(time.Millisecond))
	}

	if strings.TrimSpace(text) == "" {
		text = "(команда не вывела ничего)"
	}
	return opts.truncate(header + text), nil
}

// waitOrKill ждёт завершения команды, а по отмене или таймауту снимает всё
// дерево процессов и возвращает управление в любом случае.
//
// Здесь два подвоха, на которых приложение уже обжигалось:
//
//   - exec.CommandContext убивает только саму команду, но не порождённых ею
//     потомков. Поэтому группу процессов снимаем сами.
//   - даже после сигнала Wait() может не вернуться сразу, поэтому у ожидания
//     есть предел: не дождавшись, добиваем принудительно и уходим.
func waitOrKill(ctx context.Context, cmd *exec.Cmd) (err error, killed bool) {
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	select {
	case err := <-waitCh:
		return err, false
	case <-ctx.Done():
	}

	killProcessGroup(cmd, false) // сначала мягко
	select {
	case <-waitCh:
		return nil, true
	case <-time.After(killGrace):
	}

	killProcessGroup(cmd, true) // затем принудительно
	select {
	case <-waitCh:
	case <-time.After(killGrace):
		// Процесс не снимается (например, застрял в системном вызове).
		// Дальше ждать нельзя: интерфейс не должен оставаться заблокированным.
	}
	return nil, true
}

// shellSplit разбирает командную строку на аргументы, учитывая кавычки.
// Подстановки и операторы здесь не поддерживаются намеренно: такие команды
// определяются как составные и идут через оболочку после подтверждения.
func shellSplit(s string) ([]string, error) {
	var (
		args     []string
		cur      strings.Builder
		hasToken bool
		inSingle bool
		inDouble bool
	)
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case c == '\\' && !inSingle && i+1 < len(runes):
			i++
			cur.WriteRune(runes[i])
			hasToken = true
		case c == '\'' && !inDouble:
			inSingle = !inSingle
			hasToken = true
		case c == '"' && !inSingle:
			inDouble = !inDouble
			hasToken = true
		case (c == ' ' || c == '\t') && !inSingle && !inDouble:
			if hasToken {
				args = append(args, cur.String())
				cur.Reset()
				hasToken = false
			}
		default:
			cur.WriteRune(c)
			hasToken = true
		}
	}
	if inSingle || inDouble {
		return nil, errors.New("незакрытая кавычка в команде")
	}
	if hasToken {
		args = append(args, cur.String())
	}
	return args, nil
}

// refuseToolAsCommand отклоняет попытку запустить инструмент приложения через
// оболочку: имя инструмента — не программа, и такой команды в системе нет.
//
// Случай живой. Подсказка к документу звала посмотреть картинку инструментом
// view_image, а в agent.tools того сеанса его не было — конфиг остался от
// прежней версии. Модель поступила буквально: раз инструмента среди доступных
// нет, значит это программа, — и попросила подтверждение на «bash view_image …».
// Без отказа здесь она получила бы «command not found» и пошла бы искать,
// что же установить.
func refuseToolAsCommand(cmd string, canViewImages bool) error {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return errors.New("пустая команда")
	}
	name := fields[0]
	known := false
	for _, n := range AllNames() {
		if n == name {
			known = true
			break
		}
	}
	if !known {
		return nil
	}
	// Часть имён совпадает с настоящими программами: grep и bash есть в любой
	// системе, и запрещать их — значит ломать обычную работу. Проверяем не имя,
	// а факт: если такая программа существует, запуск законен.
	if _, err := exec.LookPath(name); err == nil {
		return nil
	}
	if name == NameViewImage && !canViewImages {
		return fmt.Errorf("%s — инструмент приложения, а не программа, и в этом сеансе он "+
			"выключен настройкой agent.tools. Через оболочку его не запустить, и распознавать "+
			"картинку нечем: внешние средства для этого не нужны и не помогут. Скажите об этом "+
			"пользователю — картинку он может приложить сам командой /addimg, "+
			"а включить показ — добавив %q в agent.tools", NameViewImage, NameViewImage)
	}
	return fmt.Errorf("%s — инструмент приложения, а не программа: такой команды в системе нет. "+
		"Вызовите его как инструмент, с его собственными параметрами, а не через %s", name, NameBash)
}
