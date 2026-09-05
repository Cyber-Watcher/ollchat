package ui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
	chromastyles "github.com/alecthomas/chroma/v2/styles"

	"github.com/Cyber-Watcher/ollchat/internal/config"
)

// Главная ловушка glamour: поле Theme действует, только когда не задан Chroma.
// Иначе тема из конфига молча подменяется на встроенную «charm», настройка
// выглядит рабочей и не делает ничего.
func TestCodeThemeClearsChroma(t *testing.T) {
	st, err := buildStyle(config.Theme{Style: "dark", CodeTheme: "gruvbox"})
	if err != nil {
		t.Fatal(err)
	}
	if st.CodeBlock.Chroma != nil {
		t.Error("при заданной теме подсветки Chroma обязан быть пустым, иначе glamour игнорирует Theme")
	}
	if !strings.Contains(st.CodeBlock.Theme, "gruvbox") {
		t.Errorf("тема подсветки = %q, ожидалась gruvbox", st.CodeBlock.Theme)
	}
}

// Заливка снимается со всей темы: у пользователя в терминале обои,
// и любой фон вырезает в них прямоугольник.
func TestCodeThemeWithoutBackground(t *testing.T) {
	st, err := buildStyle(config.Theme{Style: "dark", CodeTheme: "gruvbox", CodeBG: ""})
	if err != nil {
		t.Fatal(err)
	}
	if st.CodeBlock.BackgroundColor != nil {
		t.Errorf("у блока кода не должно быть заливки, получено %q", *st.CodeBlock.BackgroundColor)
	}

	derived := chromastyles.Registry[st.CodeBlock.Theme]
	if derived == nil {
		t.Fatalf("производная тема %q не зарегистрирована", st.CodeBlock.Theme)
	}
	for _, tt := range derived.Types() {
		if derived.Get(tt).Background.IsSet() {
			t.Errorf("в теме %q у токена %s осталась заливка", st.CodeBlock.Theme, tt)
		}
	}

	// Исходная тема должна остаться нетронутой: производная пишется в общий
	// список тем chroma, и совпадение имён затёрло бы настоящий gruvbox.
	src := chromastyles.Registry["gruvbox"]
	if src == nil {
		t.Fatal("исходная тема gruvbox пропала из списка")
	}
	if !src.Get(chroma.Background).Background.IsSet() {
		t.Error("у исходной темы gruvbox отняли заливку — затёрта производной")
	}
}

// Заданная заливка попадает в стиль как есть.
func TestCodeBackgroundApplied(t *testing.T) {
	st, err := buildStyle(config.Theme{Style: "dark", CodeTheme: "gruvbox", CodeBG: "#282828"})
	if err != nil {
		t.Fatal(err)
	}
	if st.CodeBlock.BackgroundColor == nil || *st.CodeBlock.BackgroundColor != "#282828" {
		t.Errorf("заливка блока кода не применилась: %v", st.CodeBlock.BackgroundColor)
	}
	if st.CodeBlock.Theme != "gruvbox" {
		t.Errorf("при заданной заливке берётся тема как есть, получено %q", st.CodeBlock.Theme)
	}
}

// Инлайновый код: песочный вместо красного и без серой заливки.
func TestInlineCodeColors(t *testing.T) {
	st, err := buildStyle(config.Theme{Style: "dark", InlineCode: "179"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Code.Color == nil || *st.Code.Color != "179" {
		t.Errorf("цвет кода в тексте = %v, ожидался 179", st.Code.Color)
	}
	if st.Code.BackgroundColor != nil {
		t.Errorf("заливки у кода в тексте быть не должно, получено %q", *st.Code.BackgroundColor)
	}

	// И то же с заливкой, когда её просят явно.
	st, err = buildStyle(config.Theme{Style: "dark", InlineCode: "179", InlineCodeBG: "236"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Code.BackgroundColor == nil || *st.Code.BackgroundColor != "236" {
		t.Errorf("заданная заливка кода не применилась: %v", st.Code.BackgroundColor)
	}
}

// Встроенный стиль glamour — общая переменная на всю программу: сборка своего
// оформления не имеет права её править.
func TestBuildStyleDoesNotTouchBuiltin(t *testing.T) {
	before, err := buildStyle(config.Theme{Style: "dark"})
	if err != nil {
		t.Fatal(err)
	}
	if before.Code.Color == nil || *before.Code.Color != "203" {
		t.Fatalf("без настроек цвет кода должен остаться из базового стиля, получено %v", before.Code.Color)
	}

	if _, err := buildStyle(config.Theme{Style: "dark", InlineCode: "179", CodeTheme: "gruvbox"}); err != nil {
		t.Fatal(err)
	}

	after, err := buildStyle(config.Theme{Style: "dark"})
	if err != nil {
		t.Fatal(err)
	}
	if after.Code.Color == nil || *after.Code.Color != "203" {
		t.Errorf("базовый стиль испорчен предыдущей сборкой: цвет кода стал %v", after.Code.Color)
	}
	if after.CodeBlock.Chroma == nil {
		t.Error("базовый стиль испорчен: пропала секция Chroma")
	}
}

// Без темы подсветки цвета берутся из базового стиля, но заливка снимается.
func TestNoCodeThemeKeepsBaseColors(t *testing.T) {
	st, err := buildStyle(config.Theme{Style: "dark", CodeTheme: ""})
	if err != nil {
		t.Fatal(err)
	}
	if st.CodeBlock.Chroma == nil {
		t.Fatal("без темы подсветки должны действовать цвета базового стиля")
	}
	if st.CodeBlock.Chroma.Background.BackgroundColor != nil {
		t.Errorf("заливка базового стиля не снята: %v", st.CodeBlock.Chroma.Background.BackgroundColor)
	}
	if st.CodeBlock.Chroma.Keyword.Color == nil || *st.CodeBlock.Chroma.Keyword.Color != "#00AAFF" {
		t.Errorf("цвета базового стиля потерялись: %v", st.CodeBlock.Chroma.Keyword.Color)
	}
}

func TestBuildStyleRejectsUnknownStyle(t *testing.T) {
	if _, err := buildStyle(config.Theme{Style: "нет такого"}); err == nil {
		t.Fatal("неизвестный стиль должен возвращать ошибку")
	}
}

// Отрисовка целиком: в готовом тексте есть песочный цвет и нет прежней
// красной заливки. Это единственная проверка всего пути, а не только сборки.
func TestRenderedMarkdownUsesConfiguredColors(t *testing.T) {
	r := newRenderer(80, true, config.Theme{
		Style: "dark", CodeTheme: "gruvbox", InlineCode: "179",
	})
	if r.glam == nil {
		t.Fatal("рендерер markdown не собрался")
	}
	out := r.renderMarkdown("Вызов `read_file` вернёт текст.\n\n```go\nfunc main() {}\n```\n")

	if !strings.Contains(out, "38;5;179") {
		t.Errorf("в выводе нет песочного цвета кода:\n%q", out)
	}
	for _, bad := range []string{"48;5;236", "38;5;203"} {
		if strings.Contains(out, bad) {
			t.Errorf("в выводе осталось оформление встроенного стиля (%s):\n%q", bad, out)
		}
	}
}

// Правка цвета токена ложится поверх темы, остальные цвета темы не трогает.
func TestTokenOverrideApplied(t *testing.T) {
	st, err := buildStyle(config.Theme{
		Style: "dark", CodeTheme: "gruvbox",
		Tokens: map[string]string{"NameTag": "#83a598"},
	})
	if err != nil {
		t.Fatal(err)
	}
	derived := chromastyles.Registry[st.CodeBlock.Theme]
	if derived == nil {
		t.Fatalf("производная тема %q не зарегистрирована", st.CodeBlock.Theme)
	}
	if got := derived.Get(chroma.NameTag).Colour.String(); got != "#83a598" {
		t.Errorf("цвет ключей YAML = %s, ожидался #83a598", got)
	}
	// Соседние цвета — как в теме: строки зелёные, ключевые слова оранжевые.
	if got := derived.Get(chroma.LiteralString).Colour.String(); got != "#b8bb26" {
		t.Errorf("цвет строк изменился: %s", got)
	}
	if got := derived.Get(chroma.Keyword).Colour.String(); got != "#fe8019" {
		t.Errorf("цвет ключевых слов изменился: %s", got)
	}
	// Исходная тема не тронута.
	if got := chromastyles.Registry["gruvbox"].Get(chroma.NameTag).Colour.String(); got != "#fb4934" {
		t.Errorf("исходная тема испорчена: NameTag стал %s", got)
	}
}

// Смена цвета не должна стирать начертание, заданное темой.
func TestTokenOverrideKeepsEmphasis(t *testing.T) {
	// В gruvbox NameAttribute — жирный #b8bb26.
	st, err := buildStyle(config.Theme{
		Style: "dark", CodeTheme: "gruvbox",
		Tokens: map[string]string{"NameAttribute": "#83a598"},
	})
	if err != nil {
		t.Fatal(err)
	}
	e := chromastyles.Registry[st.CodeBlock.Theme].Get(chroma.NameAttribute)
	if e.Colour.String() != "#83a598" {
		t.Errorf("цвет не применился: %s", e.Colour)
	}
	if e.Bold != chroma.Yes {
		t.Error("жирность, заданная темой, потерялась при смене цвета")
	}
}

// Разные наборы правок обязаны давать разные имена производных тем: список тем
// chroma общий, и при совпадении имён второй набор молча получил бы первый.
func TestDerivedThemeNamesDiffer(t *testing.T) {
	first, err := buildStyle(config.Theme{
		Style: "dark", CodeTheme: "gruvbox",
		Tokens: map[string]string{"NameTag": "#83a598"},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildStyle(config.Theme{
		Style: "dark", CodeTheme: "gruvbox",
		Tokens: map[string]string{"NameTag": "#8ec07c"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.CodeBlock.Theme == second.CodeBlock.Theme {
		t.Fatalf("две разные правки получили одно имя темы: %q", first.CodeBlock.Theme)
	}
	if got := chromastyles.Registry[second.CodeBlock.Theme].Get(chroma.NameTag).Colour.String(); got != "#8ec07c" {
		t.Errorf("вторая тема получила чужой цвет: %s", got)
	}

	// Одинаковые правки — наоборот, должны переиспользовать одну тему.
	again, err := buildStyle(config.Theme{
		Style: "dark", CodeTheme: "gruvbox",
		Tokens: map[string]string{"NameTag": "#83a598"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.CodeBlock.Theme != first.CodeBlock.Theme {
		t.Errorf("тот же набор правок дал новое имя: %q и %q", first.CodeBlock.Theme, again.CodeBlock.Theme)
	}
}

// Отрисовка целиком: ключи YAML синие, красного из темы не осталось.
//
// Проверяется код цвета ANSI, а не hex: блоки кода рисует форматтер chroma
// terminal256, и он квантует цвет темы в 256-цветную палитру. Красный gruvbox
// #fb4934 при этом попадает ровно в 203 — тот самый цвет, которым в ленте
// написаны ошибки, отчего ключи YAML и выглядели тревожно.
func TestRenderedYAMLKeysNotRed(t *testing.T) {
	const yaml = "```yaml\npages:\n  image: node:20-alpine\n```\n"

	keyColor := func(tokens map[string]string) string {
		t.Helper()
		r := newRenderer(80, true, config.Theme{
			Style: "dark", CodeTheme: "gruvbox", InlineCode: "179", Tokens: tokens,
		})
		if r.glam == nil {
			t.Fatal("рендерер markdown не собрался")
		}
		out := r.renderMarkdown(yaml)
		i := strings.Index(out, "pages")
		if i < 0 {
			t.Fatalf("в выводе нет ключа pages:\n%q", out)
		}
		m := regexp.MustCompile(`\x1b\[38;5;(\d+)m$`).FindStringSubmatch(out[:i])
		if m == nil {
			t.Fatalf("перед ключом нет кода цвета:\n%q", out[:i])
		}
		return m[1]
	}

	if got := keyColor(nil); got != "203" {
		t.Fatalf("без правки ключи должны краситься темой в 203 (красный), получено %s", got)
	}
	if got := keyColor(map[string]string{"NameTag": "#83a598"}); got != "108" {
		t.Errorf("с правкой ключи должны краситься в 108 (#83a598 в палитре 256), получено %s", got)
	}
}
