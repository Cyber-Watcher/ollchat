package graph

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Запрещённый синоним перестаёт быть ключом, исчезает из карточки, не
// возвращается при следующем упоминании — и всё это переживает переоткрытие.
func TestDenyAliasesReleasesKeyForGood(t *testing.T) {
	g, collDir := graph(t)
	dir := g.Dir()
	creds, _, _ := g.Entities().Add("credentials", TypeConcept, "креденшелы", "указатель")
	must(t, g.Entities().SaveCounters())
	must(t, g.Close())

	// Сухой прогон ничего не пишет, но последствия показывает.
	recs := []AliasDeny{{ID: creds, Alias: "Указатель", Why: "другое понятие: pointer", By: "тест"}}
	eff, err := DenyAliases(dir, recs, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(eff) != 1 || !eff[0].WasKey || eff[0].OwnerBefore != "credentials" || eff[0].OwnerAfter != "—" {
		t.Fatalf("сухой прогон: %+v", eff)
	}
	if deny := denyOf(t, dir); len(deny) != 0 {
		t.Fatalf("сухой прогон записал журнал: %v", deny)
	}
	if _, err := DenyAliases(dir, recs, false); err != nil {
		t.Fatal(err)
	}

	g2, err := Open(collDir, 1000, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := g2.Entities().Lookup("указатель"); ok {
		t.Fatal("запрещённый синоним остался ключом")
	}
	if ent, ok := g2.Entities().Lookup("креденшелы"); !ok || ent.ID != creds {
		t.Fatal("законный синоним перестал быть ключом")
	}
	ent, _ := g2.Entities().Get(creds)
	if strings.Contains(strings.Join(ent.Aliases, "|"), "указатель") {
		t.Fatalf("запрещённый синоним остался в карточке: %v", ent.Aliases)
	}
	// Модель предлагает его снова — реестр не берёт; имя «указатель» теперь
	// заводит СВОЁ понятие, а не попадает в credentials.
	if _, _, err := g2.Entities().Add("credentials", TypeConcept, "указатель", "креды"); err != nil {
		t.Fatal(err)
	}
	ent, _ = g2.Entities().Get(creds)
	joined := strings.Join(ent.Aliases, "|")
	if strings.Contains(joined, "указатель") || !strings.Contains(joined, "креды") {
		t.Fatalf("после повторного предложения: %v", ent.Aliases)
	}
	ptr, created, _ := g2.Entities().Add("указатель", TypeConcept)
	if !created || ptr == creds {
		t.Fatalf("имя «указатель» обязано завести своё понятие: id %d, новое %v", ptr, created)
	}
	must(t, g2.Close())

	// Запрет снимается записью undo.
	if _, err := DenyAliases(dir, []AliasDeny{{ID: creds, Alias: "указатель", Undo: true}}, false); err != nil {
		t.Fatal(err)
	}
	if deny := denyOf(t, dir); deny[creds]["указатель"] {
		t.Fatal("запрет не снялся")
	}
}

// Опечатка в списке не проходит молча.
func TestDenyAliasesRejectsTypos(t *testing.T) {
	g, _ := graph(t)
	dir := g.Dir()
	id, _, _ := g.Entities().Add("Kubernetes", TypeTech, "K8s", "cluster")
	must(t, g.Entities().SaveCounters())
	must(t, g.Close())
	if _, err := DenyAliases(dir, []AliasDeny{{ID: id, Alias: "clusters"}}, false); err == nil {
		t.Fatal("несуществующий синоним принят")
	}
	if _, err := DenyAliases(dir, []AliasDeny{{ID: id + 100, Alias: "cluster"}}, false); err == nil {
		t.Fatal("несуществующее понятие принято")
	}
	if deny := denyOf(t, dir); len(deny) != 0 {
		t.Fatalf("отказ оставил записи в журнале: %v", deny)
	}
}

// Общий синоним — у трёх и более понятий — ключом реестра не служит (этап 104,
// Ж1.7): иначе имя «api» из ответа модели уходило бы в одно из 138 понятий,
// у которых «api» стоит синонимом. Собственное имя ключом остаётся.
func TestSharedAliasIsNotAKey(t *testing.T) {
	g, err := Create(t.TempDir(), "проба", 100, Rules{SharedAliasLimit: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	a, _, _ := g.Entities().Add("Kubernetes API", TypeTech, "api")
	b, _, _ := g.Entities().Add("REST API", TypeTech, "api")
	if id, ok := g.Entities().Lookup("api"); !ok || (id.ID != a && id.ID != b) {
		t.Fatalf("у двух владельцев синоним ещё ключ: %v %v", id, ok)
	}
	g.Entities().Add("OpenAI API", TypeTech, "api")
	if id, ok := g.Entities().Lookup("api"); ok {
		t.Fatalf("синоним трёх понятий остался ключом и ведёт к %q", id.Name)
	}
	// Собственное имя — ключ при любом числе чужих синонимов.
	own, _, _ := g.Entities().Add("API", TypeTech)
	if id, ok := g.Entities().Lookup("api"); !ok || id.ID != own {
		t.Fatalf("собственное имя «API» не находится: %v %v", id, ok)
	}
	// Правило переживает повторное открытие: счёт владельцев считается при загрузке.
	dir := g.dir
	g.Close()
	g2, err := Open(filepath.Dir(dir), 100, Rules{SharedAliasLimit: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer g2.Close()
	if id, ok := g2.Entities().Lookup("api"); !ok || id.ID != own {
		t.Fatalf("после открытия ключ «api» у %v (%v), ожидалось собственное имя", id, ok)
	}
	// Выключатель: с -1 прежнее поведение — синоним ключ.
	g3, err := Open(filepath.Dir(dir), 100, Rules{SharedAliasLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer g3.Close()
	if _, ok := g3.Entities().Lookup("api"); !ok {
		t.Fatal("с выключенным правилом синоним должен остаться ключом")
	}
}

// Запрет пишется под замком сборки: признак неживого процесса команда
// подбирает и снимает за собой; сухой прогон замка не берёт. До 07.10.2026
// замок только проверялся, и сборка, открывшая реестр между проверкой
// и записью, запрета не видела (аудит, №9).
func TestDenyAliasesTakesBuildLock(t *testing.T) {
	g, _ := graph(t)
	dir := g.Dir()
	id, _, _ := g.Entities().Add("Kubernetes", TypeTech, "K8s", "cluster")
	must(t, g.Entities().SaveCounters())
	must(t, g.Close())
	lock := filepath.Join(dir, lockFile)
	stale := fmt.Sprintf("pid %d, начато %s\n", findDeadPID(t), time.Now().Format(time.RFC3339))
	must(t, os.WriteFile(lock, []byte(stale), 0o644))

	recs := []AliasDeny{{ID: id, Alias: "cluster", Why: "кластеры Kafka и Redis"}}
	if _, err := DenyAliases(dir, recs, true); err != nil {
		t.Fatalf("сухой прогон: %v", err)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatal("сухой прогон тронул признак сборки")
	}
	if _, err := DenyAliases(dir, recs, false); err != nil {
		t.Fatalf("запрет: %v", err)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatal("запрет записан без замка сборки: брошенный признак остался на месте")
	}
	// Живая сборка — отказ, и журнал запретов не тронут.
	build, err := Open(filepath.Dir(dir), 1000, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer build.Close()
	must(t, build.Lock())
	if _, err := DenyAliases(dir, []AliasDeny{{ID: id, Alias: "K8s"}}, false); !errors.Is(err, ErrLocked) {
		t.Fatalf("запрет под идущей сборкой: %v, ожидался ErrLocked", err)
	}
	if deny := denyOf(t, dir); deny[id]["k8s"] {
		t.Fatal("запрет записан под идущей сборкой")
	}
}

// denyOf — журнал запретов каталога графа; ошибка чтения — провал теста.
func denyOf(t *testing.T, dir string) map[uint32]map[string]bool {
	t.Helper()
	deny, err := loadAliasDeny(dir)
	if err != nil {
		t.Fatal(err)
	}
	return deny
}

// Слишком длинная строка в журнале запретов пропускается, как битая, а запреты
// после неё действуют. До 07.10.2026 чтение на ней останавливалось молча,
// и все дальнейшие запреты пропадали (аудит, 4.5).
func TestAliasDenySurvivesLongLine(t *testing.T) {
	g, coll := graph(t)
	dir := g.Dir()
	id, _, _ := g.Entities().Add("Kubernetes", TypeTech, "K8s", "cluster")
	must(t, g.Close())
	long := `{"id":1,"alias":"` + strings.Repeat("x", 2<<20) + `"}` + "\n"
	rec := `{"id":1,"alias":"cluster","at":1}` + "\n"
	must(t, os.WriteFile(filepath.Join(dir, aliasDenyFile), []byte(long+rec), 0o644))

	if deny := denyOf(t, dir); !deny[id]["cluster"] {
		t.Fatalf("запрет после длинной строки потерян: %v", deny)
	}
	g2, err := Open(coll, 1000, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g2.Close()
	if _, ok := g2.Entities().Lookup("cluster"); ok {
		t.Fatal("запрещённый синоним остался ключом")
	}
}
