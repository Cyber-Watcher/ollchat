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

// envFixture — корень песочницы с файлами .env, каталогом .env и ссылками
// на них; имена выдуманы.
func envFixture(t *testing.T, g *Guard) string {
	t.Helper()
	root := g.sandbox.RealRoot()
	for _, p := range []string{"sub/.env", "sub/.env.local", ".env.d/prod", ".env/prod.conf", "src/main.go"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte("TOKEN=выдуманный\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	links := map[string]string{
		"notes.txt": "sub/.env",    // ссылка на файл .env
		"cfg":       ".env",        // ссылка на каталог .env
		"later.txt": "new/.env",    // ссылка на .env, которого ещё нет
		"ok.txt":    "src/main.go", // обычная ссылка
	}
	for name, to := range links {
		if err := os.Symlink(to, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// Этап 119, 17.10.2026, слово владельца «закрой оставшиеся дыры с .env: cat,
// запись, каталог»: строка bash, называющая .env, запрещена во всех режимах,
// включая yolo, как бы она его ни называла.
func TestEnvNamedInBashDenied(t *testing.T) {
	denied := []string{
		"cat .env",
		"cat sub/.env.local",
		"head -n1 ./sub/.env",
		"cat < sub/.env",
		"sort -o out.txt sub/.env",
		"cp sub/.env /tmp/x",
		"echo TOKEN=1 > .env",
		"echo TOKEN=1 >> sub/.env",
		"cat .env/prod.conf",
		"ls .env.d",
		"cat cfg/prod.conf",
		"cat notes.txt",
		"echo x > later.txt",
		"docker run --env-file=.env img",
		"dd if=sub/.env of=/tmp/x",
		`python3 -c "print(open('sub/.env').read())"`,
		`sh -c 'cat sub/.env'`,
		"bash -c \"cd sub && cat .env\"",
		"echo $(cat sub/.env)",
		// Шаблон раскрывает только оболочка, то есть составная команда:
		// простая идёт напрямую, и `cat sub/.e*` получит звёздочку буквально.
		"true; cat sub/.e*",
		"cd sub && cat .env*",
		"true; cat su?/.[e]nv",
		"cat */.env",
		"cat sub/{.env,x}",
		"cat ~/.env",
		"cat /etc/app/.env",
		"true; cat sub/.env | head",
	}
	allowed := []string{
		"cat src/main.go",
		"cat ok.txt",
		"ls sub",
		"cat sub/*",
		"true; cat sub/*", // `*` без dotglob не берёт .env
		"true; cat src/*.go",
		"grep -rn TOKEN src",
		"cat .envrc",
		"cat my.env env.txt .environment",
		"go test ./internal/permissions/",
	}
	for _, mode := range []string{ModeSafe, ModeAutoEdit, ModeNoAsk, ModeYolo} {
		g := guardWith(t, []string{"Bash(*)"}, nil, mode)
		envFixture(t, g)
		for _, c := range denied {
			if got := g.Check(Request{Kind: KindBash, Target: c, Tool: "bash"}); got.Decision != DecisionDeny {
				t.Errorf("%s, «%s»: %v (%s), ожидался запрет", mode, c, got.Decision, got.Reason)
			}
		}
		for _, c := range allowed {
			if got := g.Check(Request{Kind: KindBash, Target: c, Tool: "bash"}); got.Decision == DecisionDeny {
				t.Errorf("%s, «%s»: запрет (%s), .env строка не называет", mode, c, got.Reason)
			}
		}
	}
}

// Запись в .env и всё, что лежит в каталоге .env, закрыты так же, как чтение.
func TestEnvWriteAndEnvDirDenied(t *testing.T) {
	for _, mode := range []string{ModeSafe, ModeAutoEdit, ModeNoAsk, ModeYolo} {
		g := guardWith(t, []string{"Read(./**)", "Write(./**)"}, nil, mode)
		root := envFixture(t, g)
		for _, tc := range []struct {
			kind Kind
			tool string
			path string
		}{
			{KindWrite, "write_file", ".env"},
			{KindWrite, "edit_file", "sub/.env.local"},
			{KindWrite, "write_file", "later.txt"},
			{KindWrite, "write_file", ".env/new.conf"},
			{KindRead, "read_file", ".env/prod.conf"},
			{KindRead, "list_dir", ".env"},
			{KindRead, "read_file", ".env.d/prod"},
			{KindRead, "read_file", "cfg/prod.conf"},
			{KindRead, "grep", "cfg"},
		} {
			got := g.Check(Request{Kind: tc.kind, Target: filepath.Join(root, tc.path), Tool: tc.tool})
			if got.Decision != DecisionDeny {
				t.Errorf("%s, %s %s: %v (%s), ожидался запрет", mode, tc.tool, tc.path, got.Decision, got.Reason)
			}
		}
		for _, p := range []string{"src/main.go", "ok.txt", "src/new.go"} {
			if got := g.Check(Request{Kind: KindWrite, Target: filepath.Join(root, p), Tool: "write_file"}); got.Decision == DecisionDeny {
				t.Errorf("%s, write_file %s: запрет (%s)", mode, p, got.Reason)
			}
		}
	}
}

// Проект, лежащий внутри каталога с именем .env.*, не закрывает сам себя:
// элементы пути судятся ниже корня песочницы.
func TestEnvRootAboveSandboxNotCounted(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, ".env.d", "proj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if EnvPath(filepath.Join(root, "main.go"), root) {
		t.Errorf("main.go в %s принят за .env", root)
	}
	if !EnvPath(filepath.Join(root, "sub", ".env"), root) {
		t.Errorf("sub/.env в %s не принят за .env", root)
	}
}
