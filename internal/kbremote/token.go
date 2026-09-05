package kbremote

import (
	"context"
	"fmt"
	"os"
	"strings"

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
		t, err := confluence.TokenFromCmd(ctx, line)
		if err != nil {
			return "", fmt.Errorf("kb.server_token_cmd: %w", err)
		}
		return t, nil
	}
	return "", nil
}

// FromConfig заводит клиента общей библиотеки по настройкам.
//
// Пустой адрес — не ошибка: значит работаем с файлами, как раньше. Возвращает
// nil-клиента, и вызывающий по нему решает, каким путём идти.
func FromConfig(ctx context.Context, k config.KB) (*Client, error) {
	if strings.TrimSpace(k.ServerURL) == "" {
		return nil, nil
	}
	token, err := Token(ctx, k)
	if err != nil {
		return nil, err
	}
	return New(Opts{URL: k.ServerURL, Token: token, Timeout: k.ServerTimeoutDuration()})
}
