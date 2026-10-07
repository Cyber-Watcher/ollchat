package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Секреты из окружения ollchat до команд модели не доходят: внедрённой
// инструкции хватило бы одного `env`, чтобы их унести. Всё остальное —
// PATH, HOME, LANG, GOPATH — проходит, иначе сломались бы инструменты.
func TestCommandEnvDropsSecrets(t *testing.T) {
	in := []string{
		"PATH=/usr/bin", "HOME=/home/u", "LANG=ru_RU.UTF-8", "GOPATH=/home/u/go",
		"HTTPS_PROXY=http://proxy:3128", "TERM=xterm", "KEYBOARD=ru",
		"GITHUB_TOKEN=a", "OLLMCP_TOKEN=b", "github_token=c", "MY_SECRET=d",
		"DB_PASSWORD=e", "MYSQL_PASSWD=f", "GPG_PASSPHRASE=g", "OPENAI_API_KEY=h",
		"SERVICE_APIKEY=i", "AWS_ACCESS_KEY_ID=j", "AWS_REGION=k", "aws_profile=l",
		"SSH_PRIVATE_KEY=m", "GOOGLE_APPLICATION_CREDENTIALS=n", "AZURE_CREDENTIAL=o",
		"SOME_ACCESS_KEY=p",
	}
	got := commandEnv(in)
	kept := map[string]bool{}
	for _, kv := range got {
		name, _, _ := strings.Cut(kv, "=")
		kept[name] = true
	}
	for _, name := range []string{"PATH", "HOME", "LANG", "GOPATH", "HTTPS_PROXY", "TERM", "KEYBOARD"} {
		if !kept[name] {
			t.Errorf("%s снята, а должна остаться", name)
		}
	}
	if len(got) != 7 {
		t.Errorf("осталось %d переменных, ожидалось 7: %q", len(got), got)
	}
}

// Сквозная проверка: команда видит PATH, но не видит секрет.
func TestBashCommandDoesNotSeeSecrets(t *testing.T) {
	t.Setenv("OLLCHAT_TEST_TOKEN", "секрет-токен")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "секрет-облака")
	t.Setenv("OLLCHAT_TEST_PLAIN", "обычная")
	opts, _ := hangTestOptions(t)

	out, err := runCommand(context.Background(), "env", opts, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "секрет") {
		t.Fatalf("секрет дошёл до команды:\n%s", out)
	}
	for _, want := range []string{"PATH=", "OLLCHAT_TEST_PLAIN=обычная", "OLLCHAT=1"} {
		if !strings.Contains(out, want) {
			t.Errorf("в окружении команды нет %s", want)
		}
	}
}
