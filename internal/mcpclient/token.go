package mcpclient

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/confluence"
)

// Ключ доступа к серверу MCP — откуда угодно, только не из конфига.
//
// Порядок и функции те же, что у ключа общей библиотеки
// (`internal/kbremote/token.go`) и токена Confluence: файл, переменная
// окружения, команда. Своя функция здесь только потому, что та привязана
// к `config.KB`, а у каждого сервера MCP свои три поля.

// Token достаёт ключ: файл, переменная окружения, команда — первый заданный.
//
// Ни один не задан — пусто, и это законно: сервер на своей машине бывает без
// ключа. Источник задан, а ключа в нём нет — ошибка: иначе ушёл бы запрос
// без ключа, и человек получил бы «нужен ключ доступа», глядя на настроенный
// файл. Ни одно сообщение не содержит самого ключа.
func Token(ctx context.Context, file, env, cmd string) (string, error) {
	if p := strings.TrimSpace(file); p != "" {
		t, err := confluence.TokenFromFile(p)
		if err != nil {
			return "", fmt.Errorf("token_file: %w", err)
		}
		if t == "" {
			return "", fmt.Errorf("token_file: файл %s пуст", p)
		}
		if err := checkToken(t); err != nil {
			return "", fmt.Errorf("token_file: файл %s: %w", p, err)
		}
		return t, nil
	}
	if name := strings.TrimSpace(env); name != "" {
		t := strings.TrimSpace(os.Getenv(name))
		if t == "" {
			return "", fmt.Errorf("token_env: переменная %s пуста", name)
		}
		if err := checkToken(t); err != nil {
			return "", fmt.Errorf("token_env: переменная %s: %w", name, err)
		}
		return t, nil
	}
	if line := strings.TrimSpace(cmd); line != "" {
		t, err := confluence.TokenFromCmd(ctx, line)
		if err != nil {
			return "", fmt.Errorf("token_cmd: %w", err)
		}
		if t == "" {
			return "", fmt.Errorf("token_cmd: команда ничего не напечатала")
		}
		if err := checkToken(t); err != nil {
			return "", fmt.Errorf("token_cmd: вывод команды: %w", err)
		}
		return t, nil
	}
	return "", nil
}

// checkToken — ключ годится в заголовок Authorization: в нём нет управляющих
// знаков (табуляцию заголовок HTTP допускает).
//
// Перевод строки внутри ключа (две строки в token_file, многострочный вывод
// token_cmd) иначе всплыл бы только при запросе — отказом net/http, который
// выглядит как «сервер недоступен» с меткой ErrUnavailable и молча уводит
// поиск на свои файлы вместо ошибки настройки (аудит 23.10.2026). Ни сам
// ключ, ни его длина в сообщение не попадают.
func checkToken(t string) error {
	for i := 0; i < len(t); i++ {
		if b := t[i]; (b < 0x20 && b != '\t') || b == 0x7f {
			if b == '\n' || b == '\r' {
				return fmt.Errorf("в ключе доступа перевод строки — ключ должен быть одной строкой")
			}
			return fmt.Errorf("в ключе доступа управляющий знак — в заголовок HTTP его не поставить")
		}
	}
	return nil
}
