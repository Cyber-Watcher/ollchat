package kbremote

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/confluence"
)

// Ключ доступа к общей библиотеке — откуда угодно, только не из конфига.
//
// Файл настроек копируют, пересылают и кладут в систему хранения версий;
// секрету в нём не место. Порядок ровно тот же, что у токена Confluence,
// и функции переиспользуются его же — заводить второй способ читать секрет
// значило бы однажды получить второй набор ошибок.

// Token достаёт ключ доступа: файл, переменная окружения, команда.
//
// Ни один источник не задан — пусто, и это законно: на своей машине служба
// может стоять без ключа, а требовать его там, где он не нужен, значит
// приучать людей его отключать.
func Token(ctx context.Context, k config.KB) (string, error) {
	if p := strings.TrimSpace(k.ServerTokenFile); p != "" {
		t, err := confluence.TokenFromFile(p)
		if err != nil {
			return "", fmt.Errorf("kb.server_token_file: %w", err)
		}
		return t, nil
	}
	if name := strings.TrimSpace(k.ServerTokenEnv); name != "" {
		if t := strings.TrimSpace(os.Getenv(name)); t != "" {
			return t, nil
		}
		return "", fmt.Errorf("kb.server_token_env: переменная %s пуста", name)
	}
	if line := strings.TrimSpace(k.ServerTokenCmd); line != "" {
		// Один запуск команды на процесс: при kb.server_url ключ нужен клиенту
		// библиотеки, инструменту search и серверу kb в хабе MCP, и прежде
		// команда выполнялась до трёх раз за запуск (аудит 23.10.2026) — у
		// команды, спрашивающей пароль или ходящей в хранилище секретов, это
		// три вопроса и три обращения.
		if t, ok := cmdTokens.Load(line); ok {
			return t.(string), nil
		}
		t, err := confluence.TokenFromCmd(ctx, line)
		if err != nil {
			return "", fmt.Errorf("kb.server_token_cmd: %w", err)
		}
		if t != "" {
			cmdTokens.Store(line, t)
		}
		return t, nil
	}
	return "", nil
}

// cmdTokens — ключи, выданные kb.server_token_cmd, по строке команды: на
// процесс. Неудача и пустой ключ не запоминаются — следующий вызов попробует
// снова.
var cmdTokens sync.Map

// FromConfig заводит клиента общей библиотеки по настройкам.
//
// Пустой адрес — не ошибка: значит работаем с файлами, как раньше. Возвращает
// nil-клиента, и вызывающий по нему решает, каким путём идти.
//
// Адрес проверяется до ключа: адрес с ключом внутри (New отказывает) не должен
// стоить запуска kb.server_token_cmd.
func FromConfig(ctx context.Context, k config.KB) (*Client, error) {
	if strings.TrimSpace(k.ServerURL) == "" {
		return nil, nil
	}
	c, err := New(Opts{URL: k.ServerURL, Timeout: k.ServerTimeoutDuration()})
	if err != nil {
		return nil, err
	}
	token, err := Token(ctx, k)
	if err != nil {
		return nil, err
	}
	c.token = strings.TrimSpace(token)
	return c, nil
}
