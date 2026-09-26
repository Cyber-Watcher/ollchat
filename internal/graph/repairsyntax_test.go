package graph

import "testing"

// Починка синтаксиса ответа модели (26.09.2026).
//
// Образцы взяты НЕ из головы: это настоящие ответы qwen3.8 на кусках, которые
// сборка устойчиво помечала пропущенными (`work/2026-09-26-rawprobe/raw.txt`,
// замер `extractprobe -raw`). Из 16 таких кусков 11 не разобрались, и все 11
// содержательно верны — понятия и связи извлечены, теряется всё из-за одного
// знака. Правило проверки одно: починка обязана вернуть ДАННЫЕ МОДЕЛИ, а не
// придумать свои.

// TestRepairLoneBackslash — имя вида «\du» из книги про psql.
// Пять отказов из одиннадцати были такими.
func TestRepairLoneBackslash(t *testing.T) {
	answer := `{"entities":[{"name":"psql","type":"инструмент","aliases":[]},` +
		`{"name":"\du","type":"понятие","aliases":["meta-command"]}],` +
		`"relations":[{"src":"psql","dst":"\du","type":"использует"}]}`
	f, err := ParseFacts(answer, `docker exec -it postgres psql; \du покажет роли`)
	if err != nil {
		t.Fatalf("ответ не разобрался: %v", err)
	}
	if len(f.Entities) != 2 {
		t.Fatalf("понятий %d, ожидалось 2: %+v", len(f.Entities), f.Entities)
	}
	var found bool
	for _, e := range f.Entities {
		if e.Name == `\du` {
			found = true
		}
	}
	if !found {
		t.Fatalf("имя «\\du» потерялось: %+v", f.Entities)
	}
	if len(f.Relations) != 1 || f.Relations[0].Dst != `\du` {
		t.Fatalf("связь потерялась или изменилась: %+v", f.Relations)
	}
}

// TestRepairLoneBackslashKeepsRealEscapes — законные последовательности
// чинилка трогать не смеет: экранированная кавычка, «\n» и «\uXXXX» должны
// дойти до разбора неизменными, иначе починка одного сломает другое.
func TestRepairLoneBackslashKeepsRealEscapes(t *testing.T) {
	answer := `{"entities":[{"name":"net.Dial","type":"инструмент",` +
		`"aliases":["кавычка \" внутри","\u0441\u0438\u043c\u0432\u043e\u043b"]}],` +
		`"relations":[]}`
	// Прямая проверка починки: годную строку она обязана вернуть как есть.
	if got := escapeLoneBackslashes(answer); got != answer {
		t.Fatalf("починка тронула законные escape:\n было: %s\nстало: %s", answer, got)
	}
	// И перенос строки внутри значения — тоже законный escape.
	withNL := `{"entities":[{"name":"x","type":"t","aliases":["a\nb"]}],"relations":[]}`
	if got := escapeLoneBackslashes(withNL); got != withNL {
		t.Fatalf("починка испортила \\n: %s", got)
	}
	// Пустой текст куска — намеренно: проверяется РАЗБОР, а не отсев clean.
	f, err := ParseFacts(answer, "")
	if err != nil {
		t.Fatalf("годный ответ не разобрался: %v", err)
	}
	if len(f.Entities) != 1 || f.Entities[0].Name != "net.Dial" {
		t.Fatalf("понятие потеряно: %+v", f.Entities)
	}
	al := f.Entities[0].Aliases
	if len(al) != 2 || al[0] != `кавычка " внутри` || al[1] != "символ" {
		t.Fatalf("escape-последовательности раскрыты неверно: %+v", al)
	}
}

// TestRepairRepeatedKey — модель написала ключ дважды, второй раз вместо
// другого: {"src":"else","src":"dst":"if"}. Три отказа из одиннадцати.
func TestRepairRepeatedKey(t *testing.T) {
	answer := `{"entities":[{"name":"if","type":"понятие","aliases":[]},` +
		`{"name":"else","type":"понятие","aliases":[]}],` +
		`"relations":[{"src":"else","src":"dst":"if","type":"часть"}]}`
	f, err := ParseFacts(answer, "if и else в сценарии bash")
	if err != nil {
		t.Fatalf("ответ не разобрался: %v", err)
	}
	if len(f.Relations) != 1 {
		t.Fatalf("связей %d, ожидалась одна: %+v", len(f.Relations), f.Relations)
	}
	r := f.Relations[0]
	if r.Src != "else" || r.Dst != "if" || r.Type != "часть" {
		t.Fatalf("связь восстановлена неверно: %+v", r)
	}
}

// TestRepairMissingBracket — массив синонимов закрыт фигурной скобкой:
// «aliases":["Neo4j Inc."}». Два отказа из одиннадцати.
func TestRepairMissingBracket(t *testing.T) {
	answer := `{"entities":[{"name":"Neo4j","type":"технология","aliases":["Neo4j Inc."},` +
		`{"name":"Cypher","type":"язык","aliases":[]}],"relations":[]}`
	f, err := ParseFacts(answer, "Cypher — язык запросов Neo4j, компания Neo4j Inc.")
	if err != nil {
		t.Fatalf("ответ не разобрался: %v", err)
	}
	if len(f.Entities) != 2 {
		t.Fatalf("понятий %d, ожидалось 2: %+v", len(f.Entities), f.Entities)
	}
	if len(f.Entities[0].Aliases) != 1 || f.Entities[0].Aliases[0] != "Neo4j Inc." {
		t.Fatalf("синоним потерян: %+v", f.Entities[0])
	}
}

// TestRepairTruncatedKeepsWhatWasExtracted — ответ оборвался на цитате
// «</s>» в куске про EOS-токен: модель вывела признак конца и умолкла.
// Здесь чинить нечего, кроме как взять уже названное.
func TestRepairTruncatedKeepsWhatWasExtracted(t *testing.T) {
	answer := `{"entities":[{"name":"EOS token","type":"понятие","aliases":["конец"]},` +
		`{"name":"токенизатор","type":"понятие","aliases":[]},` +
		`{"name":"обрыв","type":"понятие","aliases":["</s>","`
	f, err := ParseFacts(answer, "EOS token и токенизатор: обрыв последовательности")
	if err != nil {
		t.Fatalf("оборванный ответ не разобрался: %v", err)
	}
	if len(f.Entities) < 2 {
		t.Fatalf("целая часть ответа потеряна: %+v", f.Entities)
	}
	if f.Entities[0].Name != "EOS token" {
		t.Fatalf("первое понятие изменилось: %+v", f.Entities[0])
	}
}

// TestRepairLeavesGoodAnswerAlone — годный ответ не должен даже попадать
// в починку, и уж точно не меняться от неё.
func TestRepairLeavesGoodAnswerAlone(t *testing.T) {
	answer := `{"entities":[{"name":"PostgreSQL","type":"технология","aliases":["Postgres"]}],` +
		`"relations":[{"src":"PostgreSQL","dst":"SQL","type":"использует"}]}`
	f, err := ParseFacts(answer, "PostgreSQL (Postgres) использует SQL")
	if err != nil {
		t.Fatalf("годный ответ не разобрался: %v", err)
	}
	if len(f.Entities) != 1 || f.Entities[0].Name != "PostgreSQL" ||
		len(f.Entities[0].Aliases) != 1 || f.Entities[0].Aliases[0] != "Postgres" {
		t.Fatalf("годный ответ изменён починкой: %+v", f.Entities)
	}
	if s := repairSyntax(answer); s != answer {
		t.Fatalf("починка тронула годный JSON:\n было: %s\nстало: %s", answer, s)
	}
}

// TestRepairDoesNotInventData — из мусора не должно родиться данных.
// Сторож против чинилки, которая «чинит» что угодно до правдоподобного вида.
func TestRepairDoesNotInventData(t *testing.T) {
	for _, bad := range []string{
		"я не могу разобрать этот фрагмент",
		"{{{{",
		`{"entities":`,
		"",
	} {
		if f, err := ParseFacts(bad, "какой-то текст"); err == nil && len(f.Entities) > 0 {
			t.Fatalf("из мусора %q получились данные: %+v", bad, f.Entities)
		}
	}
}
