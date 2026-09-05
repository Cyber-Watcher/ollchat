package permissions

import "testing"

func guardWith(t *testing.T, allow, deny []string, mode string) *Guard {
	t.Helper()
	sb, err := NewSandbox(t.TempDir(), false, false, 512)
	if err != nil {
		t.Fatalf("NewSandbox: %v", err)
	}
	set, err := Compile(allow, nil, deny, sb.Root())
	if err != nil {
		t.Fatalf("правила не разобрались: %v", err)
	}
	return NewGuard(set, sb, mode)
}

func decide(g *Guard, cmd string) Result {
	return g.Check(Request{Tool: "bash", Kind: KindBash, Target: cmd})
}

// Составная команда из разрешённых частей вопросов не задаёт — ради этого
// правка и делалась: «grep … | head -50; ls …» это три чтения, каждое
// разрешено правилом.
func TestPipelineOfAllowedPartsPasses(t *testing.T) {
	g := guardWith(t, []string{"Bash(grep:*)", "Bash(head:*)", "Bash(ls:*)", "Bash(echo:*)"}, nil, "safe")
	for _, cmd := range []string{
		`grep -n "Task" Server/IOmsGateway.cs | head -50; echo ===MQTT===; ls Libraries/`,
		"ls -la | head -5",
		"grep foo a.txt && ls",
		"ls\nls -la",
	} {
		if got := decide(g, cmd); got.Decision != DecisionAllow {
			t.Errorf("%q: вышло %v (%s), ожидалось разрешение", cmd, got.Decision, got.Reason)
		}
	}
}

// Одна неразрешённая часть — и спрашивают про всю строку.
func TestPipelineWithUnknownPartAsks(t *testing.T) {
	g := guardWith(t, []string{"Bash(ls:*)"}, nil, "safe")
	if got := decide(g, "ls | rm -rf /tmp/x"); got.Decision != DecisionAsk {
		t.Errorf("вышло %v (%s), ожидался вопрос", got.Decision, got.Reason)
	}
}

// Перенаправление, подстановка, фон и подоболочка тихого разрешения
// не получают никогда — даже когда сама программа разрешена.
func TestUnsafeGluingAlwaysAsks(t *testing.T) {
	g := guardWith(t, []string{"Bash(cat:*)", "Bash(ls:*)", "Bash(echo:*)"}, nil, "safe")
	for _, cmd := range []string{
		"cat /etc/hosts > /tmp/copy",
		"echo привет >> ~/.bashrc",
		"ls $(rm -rf /tmp/x)",
		"ls `whoami`",
		"ls &",
		"(ls; ls)",
		`echo "$(rm -rf /tmp/x)"`,
	} {
		if got := decide(g, cmd); got.Decision != DecisionAsk {
			t.Errorf("%q: вышло %v (%s), ожидался вопрос", cmd, got.Decision, got.Reason)
		}
	}
}

// Запрет сильнее всего и разбирается по частям — в том числе при небезобидной
// склейке. Это главный инвариант: на нём правка едва не сломалась.
func TestDenyStillWinsInsideAnyGluing(t *testing.T) {
	g := guardWith(t, []string{"Bash(*)"}, []string{"Bash(rm:*)"}, "yolo")
	for _, cmd := range []string{
		"ls | rm -rf /tmp/x",
		"cat a > b; rm -rf /tmp/x",
		"ls && rm /tmp/x",
	} {
		if got := decide(g, cmd); got.Decision != DecisionDeny {
			t.Errorf("%q: вышло %v (%s), ожидался запрет", cmd, got.Decision, got.Reason)
		}
	}
}

// Разрешение «весь инструмент на сеанс» на составную команду по-прежнему
// не распространяется: правка не должна была этого ослабить.
func TestSessionToolGrantStillDoesNotCoverPipelines(t *testing.T) {
	g := guardWith(t, nil, nil, "safe")
	if err := g.GrantSessionTool("bash"); err != nil {
		t.Fatal(err)
	}
	if got := decide(g, "ls | head -5"); got.Decision != DecisionAsk {
		t.Errorf("вышло %v (%s), ожидался вопрос", got.Decision, got.Reason)
	}
}
