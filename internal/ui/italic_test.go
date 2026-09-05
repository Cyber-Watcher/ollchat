package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/glamour/v2/ansi"
	glamourstyles "charm.land/glamour/v2/styles"

	"github.com/Cyber-Watcher/ollchat/internal/config"
)

// Курсив в терминале, который его не умеет, показывается инверсией — строка
// выглядит залитой серым. Замерено на стенде: у screen и screen-256color
// (умолчание tmux) в терминфо нет sitm, у tmux-256color и xterm-256color есть.
func TestTerminalHasItalics(t *testing.T) {
	cases := map[string]bool{
		"xterm-256color":  true,
		"tmux-256color":   true,
		"alacritty":       true,
		"screen":          false,
		"screen-256color": false,
		"linux":           false,
		"dumb":            false,
		"":                false,
	}
	for term, want := range cases {
		if got := terminalHasItalics(term); got != want {
			t.Errorf("terminalHasItalics(%q) = %v, ожидалось %v", term, got, want)
		}
	}
}

// ItalicsEnabled настройка сильнее терминала.
func TestItalicsEnabledSettingBeatsTerminal(t *testing.T) {
	if !ItalicsEnabled(config.ItalicOn, "screen-256color") {
		t.Error("theme.italic = on не включил курсив")
	}
	if ItalicsEnabled(config.ItalicOff, "xterm-256color") {
		t.Error("theme.italic = off не выключил курсив")
	}
	if ItalicsEnabled(config.ItalicAuto, "screen-256color") {
		t.Error("auto оставил курсив в терминале без его поддержки")
	}
	if !ItalicsEnabled(config.ItalicAuto, "xterm-256color") {
		t.Error("auto снял курсив там, где он работает")
	}
}

// ApplyItalics правит стили ленты.
func TestApplyItalicsFixesFeedStyles(t *testing.T) {
	notice, thinking := styNotice, styThinking
	t.Cleanup(func() { styNotice, styThinking = notice, thinking })

	applyItalics(false)
	if styNotice.GetItalic() || styThinking.GetItalic() {
		t.Error("курсив остался в стилях ленты после выключения")
	}
	applyItalics(true)
	if !styNotice.GetItalic() || !styThinking.GetItalic() {
		t.Error("курсив не вернулся после включения")
	}
}

// Выделение в ответе модели тоже рисуется курсивом, и в том же tmux оно
// стало бы серой заливкой.
func TestDisableItalicsClearsGlamourStyle(t *testing.T) {
	base := glamourstyles.DarkStyleConfig
	style := base
	if style.Emph.Italic == nil || !*style.Emph.Italic {
		t.Skip("во встроенном стиле выделение и так не курсивом")
	}
	disableItalics(&style)
	if style.Emph.Italic == nil || *style.Emph.Italic {
		t.Error("курсив выделения не снят")
	}
	// Базовый стиль glamour общий на всю программу: правка копии не имеет
	// права его задеть, иначе курсив пропал бы у всех разом.
	if base.Emph.Italic == nil || !*base.Emph.Italic {
		t.Error("испорчен встроенный стиль glamour — правка ушла в общую память")
	}
}

// Обход отражением должен доставать курсив и из вложенных структур,
// а не только из верхнего уровня.
func TestDisableItalicsGoesDeep(t *testing.T) {
	yes := true
	style := ansi.StyleConfig{}
	style.Heading.StylePrimitive.Italic = &yes
	style.BlockQuote.StylePrimitive.Italic = &yes
	disableItalics(&style)
	if *style.Heading.StylePrimitive.Italic || *style.BlockQuote.StylePrimitive.Italic {
		t.Error("курсив во вложенных полях остался")
	}
}

// Конфиг проверяет значение Italic.
func TestConfigValidatesItalicValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := "[[servers]]\nname = \"lab\"\nurl = \"http://127.0.0.1:11434\"\nmodel = \"m\"\n\n" +
		"[theme]\nitalic = \"иногда\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := config.Load(path)
	if err == nil || !strings.Contains(err.Error(), "theme.italic") {
		t.Errorf("недопустимое значение принято: %v", err)
	}
}
