package graph

import (
	"strings"
	"testing"
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
	if deny := loadAliasDeny(dir); len(deny) != 0 {
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
	if deny := loadAliasDeny(dir); deny[creds]["указатель"] {
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
	if deny := loadAliasDeny(dir); len(deny) != 0 {
		t.Fatalf("отказ оставил записи в журнале: %v", deny)
	}
}
