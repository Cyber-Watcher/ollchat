package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// Образец конфига (--init-config) и Default() не расходятся в том, что
// Default() задаёт сам.
//
// Конфиг без ключа берёт значение из Default(), а созданный командой
// --init-config — из образца, и при расхождении две машины с «нетронутыми»
// настройками ведут себя по-разному. Так разошёлся agent.tools: образец
// 25.09.2026 перешёл на слитый search, а Default() остался с kb_search.
//
// Сверяется каждый ключ, который образец пишет, а Default() задаёт ненулевым.
// Нулевые в Default() значения подставляет finalize или сам потребитель
// («0 — умолчание»), и их здесь не видно.
func TestTemplateMatchesDefaults(t *testing.T) {
	var tpl Config
	md, err := toml.Decode(Template, &tpl)
	if err != nil {
		t.Fatalf("образец не разбирается: %v", err)
	}
	// Расхождения, оставленные намеренно, — с причиной.
	intended := map[string]string{
		"general.default_server": "образец указывает на свой пример сервера lab из [[servers]]",
		"input.cursor.shape": "по умолчанию — блок, как было всегда, в образце владелец выбрал черту: " +
			"оба значения закреплены TestCursorDefaults и TestTemplateIsValid",
	}
	// Разделы, которые здесь не сверяются.
	skipped := map[string]string{
		"servers": "в образце примеры серверов, а не умолчания",
	}
	var walk func(path []string, def, got reflect.Value)
	walk = func(path []string, def, got reflect.Value) {
		for i := 0; i < def.NumField(); i++ {
			field := def.Type().Field(i)
			name, _, _ := strings.Cut(field.Tag.Get("toml"), ",")
			if !field.IsExported() || name == "" || name == "-" {
				continue
			}
			key := append(append([]string(nil), path...), name)
			if _, skip := skipped[key[0]]; skip {
				continue
			}
			d, g := def.Field(i), got.Field(i)
			if d.Kind() == reflect.Struct {
				walk(key, d, g)
				continue
			}
			full := strings.Join(key, ".")
			if !md.IsDefined(key...) || d.IsZero() {
				continue
			}
			if _, ok := intended[full]; ok {
				continue
			}
			if !reflect.DeepEqual(d.Interface(), g.Interface()) {
				t.Errorf("%s: в Default() %v, в образце %v", full, d.Interface(), g.Interface())
			}
		}
	}
	walk(nil, reflect.ValueOf(Default()).Elem(), reflect.ValueOf(&tpl).Elem())

	// Заодно — что намеренные расхождения ещё существуют: иначе запись
	// о них устарела и только прячет будущие.
	for key := range intended {
		if !md.IsDefined(strings.Split(key, ".")...) {
			t.Errorf("%s: образец его больше не пишет — уберите из списка намеренных", key)
		}
	}
}
