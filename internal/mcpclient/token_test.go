package mcpclient

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TokenAs называет поле, которое человек правит (api_key_file, api_key_env —
// этап 127), и не печатает сам ключ ни в одном отказе.
func TestTokenAsNamesFieldAndHidesKey(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	secret := "sk-test-0123456789"

	wide := filepath.Join(dir, "wide")
	if err := os.WriteFile(wide, []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := TokenAs(ctx, wide, "", "", "api_key_file", "api_key_env", "api_key_cmd")
	if err == nil || !strings.HasPrefix(err.Error(), "api_key_file:") || strings.Contains(err.Error(), secret) {
		t.Errorf("файл с правами 644: ждали отказ с именем api_key_file и без ключа, получили %v", err)
	}

	t.Setenv("OLLCHAT_TEST_EMPTY_KEY", "")
	_, err = TokenAs(ctx, "", "OLLCHAT_TEST_EMPTY_KEY", "", "api_key_file", "api_key_env", "api_key_cmd")
	if err == nil || !strings.HasPrefix(err.Error(), "api_key_env:") {
		t.Errorf("пустая переменная: ждали отказ с именем api_key_env, получили %v", err)
	}

	two := filepath.Join(dir, "two")
	if err := os.WriteFile(two, []byte(secret+"\nвторая строка"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = TokenAs(ctx, two, "", "", "api_key_file", "api_key_env", "api_key_cmd")
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Errorf("ключ в две строки: ждали отказ без ключа, получили %v", err)
	}

	ok := filepath.Join(dir, "ok")
	if err := os.WriteFile(ok, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := TokenAs(ctx, ok, "", "", "api_key_file", "api_key_env", "api_key_cmd")
	if err != nil || got != secret {
		t.Errorf("файл 600: ключ %q, ошибка %v", got, err)
	}

	// Старое имя по-прежнему у Token: отказ называет token_file.
	if _, err := Token(ctx, wide, "", ""); err == nil || !strings.HasPrefix(err.Error(), "token_file:") {
		t.Errorf("Token: ждали отказ с именем token_file, получили %v", err)
	}
}
