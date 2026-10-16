package permissions

import (
	"os"
	"path/filepath"
	"testing"
)

// Прежние написания noask обязаны работать: они записаны в чужих конфигах,
// и переименование не должно ломать чужую машину. «yolo» с 16.10.2026 — снова
// отдельный режим (слово владельца): noask стал спрашивать про необратимое,
// а yolo не спрашивает ничего — для старого конфига это прежний смысл.
func TestOldModeNameStillWorks(t *testing.T) {
	for _, name := range []string{"no-ask", "dont-ask", "no_ask", "noask"} {
		if got := NormalizeMode(name); got != ModeNoAsk {
			t.Errorf("%q привелось к %q, ожидалось %q", name, got, ModeNoAsk)
		}
	}
	for _, name := range []string{ModeSafe, ModeAutoEdit, ModeYolo} {
		if got := NormalizeMode(name); got != name {
			t.Errorf("%q не должно меняться, вышло %q", name, got)
		}
	}
}

// Режим без вопросов не отменяет запретов — это главное, что о нём надо знать.
func TestNoAskStillObeysDeny(t *testing.T) {
	for _, mode := range []string{ModeNoAsk, ModeYolo} {
		g := guardWith(t, []string{"Bash(*)"}, []string{"Bash(rm:*)"}, mode)
		if got := decide(g, "rm -rf /tmp/x"); got.Decision != DecisionDeny {
			t.Errorf("%s: вышло %v (%s), ожидался запрет", mode, got.Decision, got.Reason)
		}
		if got := decide(g, "make install"); got.Decision != DecisionAllow {
			t.Errorf("%s: в режиме без вопросов прочее разрешается: вышло %v", mode, got.Decision)
		}
	}
}

// Переключение по кругу приводит к новому имени, а не к старому; yolo в круг
// Shift+Tab не входит — из него круг возвращает в safe.
func TestNextModeUsesNewName(t *testing.T) {
	g := guardWith(t, nil, nil, ModeAutoEdit)
	if got := g.NextMode(); got != ModeNoAsk {
		t.Errorf("после auto-edit ожидался %q, вышло %q", ModeNoAsk, got)
	}
	if got := g.NextMode(); got != ModeSafe {
		t.Errorf("после noask ожидался %q, вышло %q — yolo в круг не входит", ModeSafe, got)
	}
	y := guardWith(t, nil, nil, ModeYolo)
	if got := y.NextMode(); got != ModeSafe {
		t.Errorf("после yolo ожидался %q, вышло %q", ModeSafe, got)
	}
}

// Этап 112, Н5 и Т9: необратимое спрашивается в safe, auto-edit и noask — даже
// под правилом allow и при разрешении инструмента на сеанс; без вопроса — только
// в yolo; запрет сильнее.
func TestIrreversibleAsksExceptYolo(t *testing.T) {
	irr := []string{"Bash(git push:*)", "Bash(mv:*)", "Bash(psql:*)"}
	cases := []struct {
		cmd  string
		want bool // необратимо
	}{
		{"git push origin main", true},
		{"git -C sub push", true}, // подкоманда мимо общего ключа — встроенная проверка
		{"git stash drop", true},
		{"git stash list", false},
		{"git branch -D old", true},
		{"git branch -d merged", false},
		{"mv a b", true},
		{"go test ./...", false},
		{"git status", false},
		{"sed -i 's/a/b/' f.txt", true},
		{"sed -E -i.bak 's/a/b/' f.txt", true},
		{"sed -n '1,5p' f.txt", false},
		{"perl -pi -e 's/a/b/' f.txt", true},
		{"find . -name '*.tmp' -delete", true},
		{"find . -name '*.tmp'", false},
		{"git reset --hard HEAD~1", true},
		{"git reset HEAD~1 --hard", true},
		{"git reset --soft HEAD~1", false},
		{"git checkout -- main.go", true},
		{"git checkout -b new", false},
		{"ls && git push", true}, // часть составной команды
		{"bash -c 'psql -c \"drop table x\"'", true},
	}
	for _, mode := range []string{ModeSafe, ModeAutoEdit, ModeNoAsk, ModeYolo} {
		g := guardWith(t, []string{"Bash(*)"}, []string{"Bash(rm:*)"}, mode)
		if err := g.set.SetIrreversible(irr, g.sandbox.Root()); err != nil {
			t.Fatal(err)
		}
		_ = g.GrantSessionTool("bash")
		for _, c := range cases {
			got := g.Check(Request{Kind: KindBash, Target: c.cmd, Tool: "bash"})
			switch {
			case c.want && mode != ModeYolo && (got.Decision != DecisionAsk || !got.Irreversible):
				t.Errorf("%s: %q — ждали вопрос «необратимо», вышло %v (%s)", mode, c.cmd, got.Decision, got.Reason)
			case c.want && mode == ModeYolo && got.Decision != DecisionAllow:
				t.Errorf("yolo: %q — ждали без вопроса, вышло %v (%s)", c.cmd, got.Decision, got.Reason)
			case !c.want && got.Irreversible:
				t.Errorf("%s: %q — помечено необратимым: %s", mode, c.cmd, got.Reason)
			}
		}
		if got := decide(g, "rm -rf x"); got.Decision != DecisionDeny {
			t.Errorf("%s: запрет должен быть сильнее необратимости, вышло %v", mode, got.Decision)
		}
	}
}

// `>` в уже существующий файл обнуляет его — необратимо; в новый файл и `>>` — нет.
func TestTruncatingRedirectIrreversible(t *testing.T) {
	g := guardWith(t, []string{"Bash(*)"}, nil, ModeNoAsk)
	root := g.sandbox.RealRoot()
	if err := os.WriteFile(filepath.Join(root, "есть.txt"), []byte("данные"), 0o600); err != nil {
		t.Fatal(err)
	}
	for cmd, want := range map[string]bool{
		"echo x > есть.txt":     true,
		"echo x >| есть.txt":    true,
		"echo x >> есть.txt":    false,
		"echo x > новый.txt":    false,
		"go test 2>&1 | head":   false,
		"echo x > /dev/null":    false,
		"ls; echo x > есть.txt": true,
	} {
		got := decide(g, cmd)
		if got.Irreversible != want {
			t.Errorf("%q: необратимо %v, ждали %v (%v, %s)", cmd, got.Irreversible, want, got.Decision, got.Reason)
		}
	}
}

// Каталог .ollchat модели закрыт в любом режиме, даже в yolo: иначе она правит
// собственные разрешения и копии, по которым откатывают.
func TestOwnDirDeniedEvenInYolo(t *testing.T) {
	for _, mode := range []string{ModeSafe, ModeNoAsk, ModeYolo} {
		g := guardWith(t, []string{"Bash(*)", "Write(./**)"}, nil, mode)
		root := g.sandbox.RealRoot()
		if got := g.Check(Request{Kind: KindWrite, Target: filepath.Join(root, ".ollchat", "config.toml"), Tool: "write_file"}); got.Decision != DecisionDeny {
			t.Errorf("%s: запись в .ollchat/config.toml — %v (%s)", mode, got.Decision, got.Reason)
		}
		if got := decide(g, "echo 'mode = \"yolo\"' > .ollchat/config.toml"); got.Decision != DecisionDeny {
			t.Errorf("%s: > в .ollchat — %v (%s)", mode, got.Decision, got.Reason)
		}
		if got := g.Check(Request{Kind: KindWrite, Target: filepath.Join(root, ".ollchatx", "f"), Tool: "write_file"}); got.Decision == DecisionDeny {
			t.Errorf("%s: соседний каталог .ollchatx запрещён зря", mode)
		}
	}
}

// Правка файла без копии (WriteIrreversible) — необратима: noask спрашивает.
func TestWriteWithoutBackupAsks(t *testing.T) {
	g := guardWith(t, nil, nil, ModeNoAsk)
	path := filepath.Join(g.sandbox.RealRoot(), "f.txt")
	if got := g.Check(Request{Kind: KindWrite, Target: path, Tool: "write_file"}); got.Decision != DecisionAllow {
		t.Fatalf("без копий правка в noask — без вопроса, вышло %v", got.Decision)
	}
	g.WriteIrreversible = func(string) string {
		return "копию сделать нельзя: файл больше предела"
	}
	if got := g.Check(Request{Kind: KindWrite, Target: path, Tool: "write_file"}); got.Decision != DecisionAsk || !got.Irreversible {
		t.Errorf("правка без копии в noask: вышло %v (%s)", got.Decision, got.Reason)
	}
	g.mode = ModeYolo
	if got := g.Check(Request{Kind: KindWrite, Target: path, Tool: "write_file"}); got.Decision != DecisionAllow {
		t.Errorf("yolo: правка без копии без вопроса, вышло %v", got.Decision)
	}
}
