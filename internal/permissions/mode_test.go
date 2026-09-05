package permissions

import "testing"

// Прежнее имя режима обязано работать: оно записано в чужих конфигах,
// и переименование не должно ломать чужую машину.
func TestOldModeNameStillWorks(t *testing.T) {
	for _, name := range []string{"yolo", "no-ask", "dont-ask", "no_ask", "noask"} {
		if got := NormalizeMode(name); got != ModeNoAsk {
			t.Errorf("%q привелось к %q, ожидалось %q", name, got, ModeNoAsk)
		}
	}
	for _, name := range []string{ModeSafe, ModeAutoEdit} {
		if got := NormalizeMode(name); got != name {
			t.Errorf("%q не должно меняться, вышло %q", name, got)
		}
	}
}

// Режим без вопросов не отменяет запретов — это главное, что о нём надо знать.
func TestNoAskStillObeysDeny(t *testing.T) {
	g := guardWith(t, []string{"Bash(*)"}, []string{"Bash(rm:*)"}, "yolo")
	if got := decide(g, "rm -rf /tmp/x"); got.Decision != DecisionDeny {
		t.Errorf("вышло %v (%s), ожидался запрет", got.Decision, got.Reason)
	}
	if got := decide(g, "make install"); got.Decision != DecisionAllow {
		t.Errorf("в режиме без вопросов прочее разрешается: вышло %v", got.Decision)
	}
}

// Переключение по кругу приводит к новому имени, а не к старому.
func TestNextModeUsesNewName(t *testing.T) {
	g := guardWith(t, nil, nil, ModeAutoEdit)
	if got := g.NextMode(); got != ModeNoAsk {
		t.Errorf("после auto-edit ожидался %q, вышло %q", ModeNoAsk, got)
	}
}
