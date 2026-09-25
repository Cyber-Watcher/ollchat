package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/find"
)

// Инструмент `search` отдаёт РОВНО то, что видит человек по /search.
//
// **Зачем такая проверка, а не «в выдаче есть слово».** Разрыв, ради которого
// инструмент заведён, был не в правах и не в данных: ядро поиска одно с этапа
// 91, но слитую выдачу (`find.Search` + `find.Render`) звал только интерфейс,
// а у модели те же половины лежали порознь — `graph_search` и `kb_search`.
// Значит проверять надо не наличие слов, а совпадение с самим ядром: если
// инструмент однажды заведёт свою копию отбора, тест это поймает.
func TestSearchToolMatchesTheCore(t *testing.T) {
	reg, base, _ := newKBRegistry(t)

	plan, err := reg.Plan(NameSearch, map[string]any{"query": "goroutines and channels"})
	if err != nil {
		t.Fatalf("план инструмента: %v", err)
	}
	got, err := plan.Run(context.Background())
	if err != nil {
		t.Fatalf("запуск инструмента: %v", err)
	}

	// То же самое напрямую ядром, теми же числами, что у реестра выше.
	coll, err := base.Open("test")
	if err != nil {
		t.Fatal(err)
	}
	res, err := find.Search(context.Background(), find.Deps{Coll: coll, Source: coll},
		"goroutines and channels", find.Opts{
			Mode: "tool", Collection: "test", TopK: 5, MaxPerBook: 3,
		})
	if err != nil {
		t.Fatal(err)
	}
	want := find.Render(res, false, coll)
	if got != want {
		t.Errorf("выдача инструмента не совпала с выдачей ядра.\nинструмент:\n%s\nядро:\n%s", got, want)
	}
	if !strings.Contains(got, "goroutines") {
		t.Errorf("в выдаче нет искомого: %s", got)
	}
}

// Выключатель — список имён: без него инструмент модели не достаётся.
//
// Проверяется обе стороны: имя известно реестру (иначе конфиг с ним не
// запустится) и при этом НЕ входит в поверхность службы MCP, которая задана
// перечислением намеренно.
func TestSearchToolIsOffUnlessListed(t *testing.T) {
	if _, err := NewRegistry([]string{NameKBSearch}, Options{KBDir: t.TempDir()}); err != nil {
		t.Fatalf("реестр без нового имени не собрался: %v", err)
	}
	reg, err := NewRegistry([]string{NameKBSearch}, Options{KBDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Plan(NameSearch, map[string]any{"query": "что угодно"}); err == nil {
		t.Error("невключённый инструмент отдал план — выключатель не работает")
	}

	var inAll bool
	for _, n := range AllNames() {
		if n == NameSearch {
			inAll = true
		}
	}
	if !inAll {
		t.Error("имя не в AllNames: конфиг с ним не запустится")
	}
	for _, n := range ReadOnlyNames() {
		if n == NameSearch {
			t.Error("инструмент попал в поверхность службы MCP — она расширяется только осознанно")
		}
	}
}

// Без местной коллекции инструмент отказывает с объяснением, а не отдаёт
// половину выдачи под видом целой: подтверждения графа и сборка выдачи
// читаются по файлам коллекции.
func TestSearchToolRefusesWithoutLocalCollection(t *testing.T) {
	reg, err := NewRegistry([]string{NameSearch}, Options{KBDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reg.Plan(NameSearch, map[string]any{"query": "вопрос"})
	if err != nil {
		t.Fatalf("план: %v", err)
	}
	if _, err := plan.Run(context.Background()); err == nil {
		t.Error("без базы знаний инструмент не отказал")
	}
}

// Пустой запрос отклоняется на плане, до всякой работы.
func TestSearchToolRejectsEmptyQuery(t *testing.T) {
	reg, _, _ := newKBRegistry(t)
	if _, err := reg.Plan(NameSearch, map[string]any{"query": "   "}); err == nil {
		t.Error("пустой запрос принят")
	}
}
