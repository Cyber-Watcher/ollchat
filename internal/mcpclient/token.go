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
		return t, nil
	}
	if name := strings.TrimSpace(env); name != "" {
		if t := strings.TrimSpace(os.Getenv(name)); t != "" {
			return t, nil
		}
		return "", fmt.Errorf("token_env: переменная %s пуста", name)
	}
	if line := strings.TrimSpace(cmd); line != "" {
		t, err := confluence.TokenFromCmd(ctx, line)
		if err != nil {
			return "", fmt.Errorf("token_cmd: %w", err)
		}
		if t == "" {
			return "", fmt.Errorf("token_cmd: команда ничего не напечатала")
		}
		return t, nil
	}
	return "", nil
}
