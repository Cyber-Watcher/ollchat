package permissions

import (
	"os"
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

// Этап 119, решение владельца 17.10.2026: `.env` и `.env.*` в ЛЮБОМ каталоге
// закрыты кодом, а не правилом шаблона, во всех режимах, включая yolo, и для
// всех инструментов чтения. Правил deny на .env здесь нет вовсе: у людей свои
// списки deny, и запрет обязан действовать без них.
func TestEnvFilesDeniedAnywhereByCode(t *testing.T) {
	denied := []string{"sub/.env", "a/b/.env.local", ".env.example", "sub/.env.example", ".env", "a/.env.prod"}
	allowed := []string{"env.txt", "my.env", ".environment", "dotenv.go", "sub/.envrc", "a/env/x.txt"}
	for _, mode := range []string{ModeSafe, ModeAutoEdit, ModeNoAsk, ModeYolo} {
		g := guardWith(t, []string{"Read(./**)"}, nil, mode)
		root := g.sandbox.RealRoot()
		for _, tool := range []string{"read_file", "grep", "view_image", "scan_redact"} {
			for _, p := range denied {
				got := g.Check(Request{Kind: KindRead, Target: filepath.Join(root, p), Tool: tool})
				if got.Decision != DecisionDeny {
					t.Errorf("%s, %s %s: %v (%s), ожидался запрет", mode, tool, p, got.Decision, got.Reason)
				}
			}
			for _, p := range allowed {
				got := g.Check(Request{Kind: KindRead, Target: filepath.Join(root, p), Tool: tool})
				if got.Decision != DecisionAllow {
					t.Errorf("%s, %s %s: %v (%s), ожидалось разрешение правилом Read(./**)", mode, tool, p, got.Decision, got.Reason)
				}
			}
		}
	}
}

// Ссылка с безобидным именем на .env — то же чтение .env: имя берётся и у
// самой цели, и у файла, куда ведёт ссылка.
func TestEnvFileDeniedThroughSymlink(t *testing.T) {
	g := guardWith(t, []string{"Read(./**)"}, nil, ModeYolo)
	root := g.sandbox.RealRoot()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", ".env.local"), []byte("TOKEN=выдуманный\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("sub", ".env.local"), filepath.Join(root, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	got := g.Check(Request{Kind: KindRead, Target: filepath.Join(root, "notes.txt"), Tool: "read_file"})
	if got.Decision != DecisionDeny {
		t.Errorf("notes.txt → sub/.env.local: %v (%s), ожидался запрет", got.Decision, got.Reason)
	}
}
