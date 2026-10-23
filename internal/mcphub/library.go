package mcphub

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbremote"
	"github.com/Cyber-Watcher/ollchat/internal/mcpclient"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// LibrarySearch — исполнитель инструмента search в службе общей библиотеки
// по MCP (этап 121, А3): `<kb.server_url>/mcp`, ключ — тот же, что у
// kb.server_url (kb.server_token_*), срок — kb.server_timeout.
//
// Пустой kb.server_url — nil без ошибки: search ищет сам. Сеть не трогает:
// рукопожатие — при первом вызове.
func LibrarySearch(ctx context.Context, k config.KB, version string) (tools.RemoteCaller, error) {
	u := strings.TrimSpace(k.ServerURL)
	if u == "" {
		return nil, nil
	}
	// Ключ в адресе не поддерживается (решение владельца 23.10.2026): конфиг
	// такой адрес отвергает при загрузке, а здесь — вторая проверка, до
	// команды за ключом, как в kbremote.FromConfig.
	if err := kbremote.CheckURL(u); err != nil {
		return nil, err
	}
	token, err := kbremote.Token(ctx, k)
	if err != nil {
		return nil, err
	}
	c, err := mcpclient.New(mcpclient.Opts{Server: "библиотеки", URL: libraryURL(u),
		Token: token, Timeout: k.ServerTimeoutDuration(), Version: version})
	if err != nil {
		return nil, err
	}
	return libraryCaller{c: c}, nil
}

// libraryCaller переводит ответы клиента MCP на язык реестра: «не ответил»
// несёт kb.ErrUnreachable (по ней search переходит на свои файлы), ошибка
// инструмента службы — error с её текстом.
type libraryCaller struct{ c *mcpclient.Client }

func (l libraryCaller) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	res, err := l.c.CallTool(ctx, name, args)
	if err != nil {
		if errors.Is(err, mcpclient.ErrUnavailable) {
			return "", fmt.Errorf("%w: %w", kb.ErrUnreachable, err)
		}
		return "", err
	}
	if res.IsError {
		return "", fmt.Errorf("служба библиотеки: %s", OneLine(res.Text))
	}
	return res.Text, nil
}
