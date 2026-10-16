package permissions

import (
	"path/filepath"
	"testing"
)

// Аудит 16.10.2026, находка 11: deny на чтение файла распространяется на его
// копию в .ollchat/backups и на соседей .env.* — с запретами по умолчанию.
func TestDefaultDenyCoversBackupMirrorAndEnvSiblings(t *testing.T) {
	g := guardWith(t, []string{"Read(./**)"}, []string{"Read(./.env)", "Read(./.env.*)", "Read(./.ollchat/backups/**)"}, ModeNoAsk)
	root := g.sandbox.RealRoot()
	for _, p := range []string{".env", ".env.local", ".env.production", ".ollchat/backups/2026-10-16/.env.150405", ".ollchat/backups/2026-10-16/sub/secret.toml.120000"} {
		got := g.Check(Request{Kind: KindRead, Target: filepath.Join(root, p), Tool: "read_file"})
		if got.Decision != DecisionDeny {
			t.Errorf("read %s: %v (%s), ожидался запрет", p, got.Decision, got.Reason)
		}
	}
	for _, p := range []string{"main.go", ".ollchat/config.toml", "env.txt", ".envrc"} {
		got := g.Check(Request{Kind: KindRead, Target: filepath.Join(root, p), Tool: "read_file"})
		if got.Decision == DecisionDeny {
			t.Errorf("read %s запрещён зря: %s", p, got.Reason)
		}
	}
}
