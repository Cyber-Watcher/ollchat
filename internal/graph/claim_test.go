package graph

import (
	"os"
	"path/filepath"
	"testing"
)

// loadFrom читает реестр из строк и отдаёт словари для сличения.
func loadFrom(t *testing.T, lines []string) *Entities {
	return loadFromWith(t, DefaultStemMinLen, lines)
}

func loadFromWith(t *testing.T, stemMinLen int, lines []string) *Entities {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, entitiesFile)
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := readEntitiesFileWith(path, stemMinLen)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// Главное свойство правила: итог не зависит от порядка записей в файле.
// Ради него всё и затевалось — иначе уплотнение реестра меняет поиск.
func TestClaimIndependentOfOrder(t *testing.T) {
	a := `{"id":1,"name":"смещение","norm":"смещение","type":"понятие","aliases":["bias"],"count":443,"at":1}`
	b := `{"id":2,"name":"предвзятости","norm":"предвзятости","type":"понятие","aliases":["bias"],"count":4,"at":2}`

	first := loadFrom(t, []string{a, b})
	second := loadFrom(t, []string{b, a})

	if first.byKey["bias"] != second.byKey["bias"] {
		t.Fatalf("порядок записей решил спор: %d против %d",
			first.byKey["bias"], second.byKey["bias"])
	}
	if got := first.byKey["bias"]; got != 1 {
		t.Fatalf("ключ достался не тому: %d, ожидалось понятие с 443 упоминаниями", got)
	}
}

// Собственное имя сильнее чужого синонима, сколько бы упоминаний у того ни было.
func TestClaimOwnNameBeatsAlias(t *testing.T) {
	e := loadFrom(t, []string{
		`{"id":1,"name":"кэш","norm":"кэш","type":"понятие","aliases":["буфер"],"count":900,"at":1}`,
		`{"id":2,"name":"буфер","norm":"буфер","type":"понятие","count":3,"at":2}`,
	})
	if got := e.byKey["буфер"]; got != 2 {
		t.Fatalf("синоним отобрал ключ у собственного имени: %d", got)
	}
}

// Повторная запись того же понятия (реестр дозаписывается) ничего не ломает
// и не отбирает ключ у более крупного понятия.
func TestClaimStableOnRewrite(t *testing.T) {
	e := loadFrom(t, []string{
		`{"id":1,"name":"смещение","norm":"смещение","type":"понятие","aliases":["bias"],"count":10,"at":1}`,
		`{"id":2,"name":"предвзятости","norm":"предвзятости","type":"понятие","aliases":["bias"],"count":4,"at":2}`,
		`{"id":1,"name":"смещение","norm":"смещение","type":"понятие","aliases":["bias"],"count":443,"at":3}`,
		`{"id":2,"name":"предвзятости","norm":"предвзятости","type":"понятие","aliases":["bias"],"count":4,"at":4}`,
	})
	if got := e.byKey["bias"]; got != 1 {
		t.Fatalf("после дозаписи ключ ушёл к мелкому понятию: %d", got)
	}
}

// При равном числе упоминаний решает меньший номер: нужно любое правило,
// лишь бы оно не зависело от порядка.
func TestClaimTieGoesToLowerID(t *testing.T) {
	e := loadFrom(t, []string{
		`{"id":7,"name":"alpha","norm":"alpha","type":"понятие","aliases":["общий"],"count":5,"at":1}`,
		`{"id":3,"name":"beta","norm":"beta","type":"понятие","aliases":["общий"],"count":5,"at":2}`,
	})
	if got := e.byKey["общий"]; got != 3 {
		t.Fatalf("при равенстве выбран не меньший номер: %d", got)
	}
}

// То же правило действует и на словарь основ слов.
func TestClaimStemFollowsSameRule(t *testing.T) {
	e := loadFrom(t, []string{
		`{"id":1,"name":"переранжирование","norm":"переранжирование","type":"понятие","count":3,"at":1}`,
		`{"id":2,"name":"переранжирования","norm":"переранжирования","type":"понятие","count":300,"at":2}`,
	})
	// Обе основы совпадают; побеждает понятие с бо́льшим числом упоминаний.
	for stem, id := range e.byStem {
		if id != 2 && id != 0 {
			t.Fatalf("основа %q ушла к понятию %d вместо более крупного 2", stem, id)
		}
	}
}

// Союз не должен быть ключом поиска. Замер 03.09.2026: понятие «ИИ» лежало
// в указателе основ под «и» и попадало в выдачу входа 60 раз из 60 вопросов.
func TestShortStemIsNotAKey(t *testing.T) {
	e := loadFrom(t, []string{
		`{"id":1,"name":"ИИ","norm":"ии","type":"технология","count":1662,"at":1}`,
		`{"id":2,"name":"переранжирование","norm":"переранжирование","type":"понятие","count":159,"at":2}`,
	})
	if id, ok := e.byStem["и"]; ok {
		t.Fatalf("основа «и» стала ключом и ведёт к понятию %d", id)
	}
	if _, ok := e.LookupStem("и"); ok {
		t.Fatal("поиск по основе «и» что-то нашёл")
	}
	// А длинные основы работают как работали: ради них указатель и заведён.
	// Форма берётся падежная, а не глагольная: стеммер Портера сводит падежи,
	// но глагол с существительным не сводит — это замер 26.08.2026,
	// записанный в шапке internal/graph/vectors.go.
	if _, ok := e.LookupStem("переранжирования"); !ok {
		t.Fatal("длинная основа перестала находиться — сломан смысл указателя")
	}
}

// Стража достигнутого: два правила словесного входа, добытых замером 03.09.2026.
// Если кто-то поменяет умолчания или порядок проверок, эти тесты скажут об этом
// раньше, чем замер на 60 вопросах, который идёт полторы минуты и требует графа.
func TestLexicalRulesGuardMeasuredBehaviour(t *testing.T) {
	if r := DefaultRules(); r.StemMinLen != 3 || r.StemMinBooks != 2 {
		t.Fatalf("умолчания правил изменились: %+v — замер 03.09.2026 сделан на 3 и 2", r)
	}

	e := loadFrom(t, []string{
		`{"id":1,"name":"ИИ","norm":"ии","type":"технология","count":1662,"at":1}`,
		`{"id":2,"name":"Связанность","norm":"связанность","type":"понятие","count":6,"at":2}`,
	})
	if _, ok := e.byStem["и"]; ok {
		t.Fatal("союз «и» снова стал ключом: понятие «ИИ» вернётся в каждый русский вопрос")
	}
	// А основа «связан» ключом остаётся: правило про книги применяется позже,
	// при поиске, и точное имя «связанность» обязано находиться.
	if _, ok := e.byStem["связан"]; !ok {
		t.Fatal("основа «связан» пропала из указателя — вопрос «что такое связанность» перестанет находить понятие")
	}
	if _, ok := e.Lookup("связанность"); !ok {
		t.Fatal("точное имя перестало находиться")
	}
}

// Настройка обязана действовать: правило, которое нельзя ослабить, однажды
// придётся править кодом на чужой библиотеке.
func TestStemRulesAreSettable(t *testing.T) {
	if r := (Rules{StemMinLen: 1, StemMinBooks: 5}).norm(); r.StemMinLen != 1 || r.StemMinBooks != 5 {
		t.Fatalf("правила не применились: %+v", r)
	}
	// При длине 1 однобуквенная основа снова становится ключом — так видно,
	// что настройка действует, а не украшает конфиг.
	e := loadFromWith(t, 1, []string{`{"id":1,"name":"ИИ","norm":"ии","type":"технология","count":9,"at":1}`})
	if _, ok := e.byStem["и"]; !ok {
		t.Fatal("настройка не действует: при длине 1 основа «и» обязана быть ключом")
	}
}
