package graph

import "testing"

// resolveFixture собирает граф с понятиями-двойниками и векторами к ним.
//
// Порядок заведения важен и повторяет настоящий: понятие с простым именем
// заводится РАНЬШЕ того, кто назовёт это имя своим синонимом. Иначе `Add`
// вернёт уже заведённое: годный синоним занимает ключ в реестре, и второго
// узла просто не возникнет — а разбирать мы хотим именно случай двух узлов.
func resolveFixture(t *testing.T) *Graph {
	t.Helper()
	g := newGraphWith(t)

	add := func(name, typ string, aliases ...string) uint32 {
		id, _, err := g.Entities().Add(name, typ, aliases...)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	// 1–2: настоящие двойники, синоним негодный для поиска (разное число слов).
	add("regular expression", TypeConcept)
	add("regex", TypeConcept, "regular expression")
	// 3–4: модель напутала — синоним есть, а понятия разные.
	add("vulnerability scanner", TypeConcept)
	add("Nessus", TypeTool, "vulnerability scanner")
	// 5–6: разные версии одного и того же.
	add("Mistral-7B-Instruct-v0.1", TypeTech)
	add("Mistral-7B-Instruct-v0.3", TypeTech, "Mistral-7B-Instruct-v0.1")
	// 7–8: перевод — синоним годный и для поиска.
	add("video", TypeConcept)
	add("видео", TypeConcept, "video")
	// 9: понятие без вектора — счёт не должен на нём падать.
	add("понятие без вектора", TypeConcept)

	// Векторы: единица по своей оси. Совпадающая ось даёт близость 1,
	// разные оси — 0.
	const dim = 4
	//              1  2  3  4  5  6  7  8   ← номера понятий, девятый не покрыт
	axis := []int{0, 0, 1, 2, 3, 3, 0, 0, 0}
	data := make([]int8, 0, 8*dim)
	for i := 0; i < 8; i++ {
		v := make([]int8, dim)
		v[axis[i]] = 127
		data = append(data, v...)
	}
	if err := g.SaveEntityVectors("проба", dim, data); err != nil {
		t.Fatal(err)
	}
	return g
}

func find(pairs []ResolvePair, a, b uint32) *ResolvePair {
	for i := range pairs {
		if pairs[i].A == a && pairs[i].B == b {
			return &pairs[i]
		}
	}
	return nil
}

// Двойники находятся по синониму, а ошибка модели отсеивается вектором.
//
// В этом и весь замысел: синоним от модели говорит «это одно и то же»,
// а вектор проверяет её. На настоящем графе так отсеивается половина
// синонимов — замер 27.08.2026.
func TestResolveFindsAliasPairsAndDropsMistakes(t *testing.T) {
	g := resolveFixture(t)
	defer g.Close()

	pairs, st, err := g.ResolveCandidates(ResolveOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if st.AliasPairs != 4 {
		t.Fatalf("пар, связанных синонимом, %d, ожидалось 4", st.AliasPairs)
	}
	if p := find(pairs, 1, 2); p == nil {
		t.Fatal("«regular expression» и «regex» должны попасть в кандидаты")
	} else if !p.AliasLink || p.AliasUsable {
		t.Errorf("синоним есть, но для поиска негоден: link=%v usable=%v",
			p.AliasLink, p.AliasUsable)
	}
	if find(pairs, 3, 4) != nil {
		t.Error("«vulnerability scanner» и «Nessus» — ошибка модели, вектор обязан их развести")
	}
}

// Перевод помечается и как годный синоним, и как пара через границу алфавита.
func TestResolveMarksTranslation(t *testing.T) {
	g := resolveFixture(t)
	defer g.Close()

	pairs, _, err := g.ResolveCandidates(ResolveOpts{})
	if err != nil {
		t.Fatal(err)
	}
	p := find(pairs, 7, 8)
	if p == nil {
		t.Fatal("«video» и «видео» должны попасть в кандидаты")
	}
	if !p.AliasUsable {
		t.Error("перевод одним словом — годный синоним и для поиска")
	}
	if !p.Cross {
		t.Error("пара через границу алфавита не помечена")
	}
	if !p.SameType {
		t.Error("типы совпадают, а помечено иначе")
	}
}

// Разные версии одного и того же помечаются признаком «цифры».
//
// Без него они неразличимы: на настоящем графе `Mistral-7B-Instruct-v0.1`
// и `v0.3` дают близость 0.967 и прошли бы любой порог.
func TestResolveMarksVersionDifference(t *testing.T) {
	g := resolveFixture(t)
	defer g.Close()

	pairs, _, err := g.ResolveCandidates(ResolveOpts{})
	if err != nil {
		t.Fatal(err)
	}
	p := find(pairs, 5, 6)
	if p == nil {
		t.Fatal("пара версий должна попасть в кандидаты: развести их — дело человека")
	}
	if !p.DigitDiff {
		t.Error("имена различаются только цифрами, признак не проставлен")
	}
	if q := find(pairs, 7, 8); q != nil && q.DigitDiff {
		t.Error("«video» и «видео» цифрами не различаются")
	}
}

// CrossOnly оставляет только пары через границу алфавита.
func TestResolveCrossOnly(t *testing.T) {
	g := resolveFixture(t)
	defer g.Close()

	pairs, _, err := g.ResolveCandidates(ResolveOpts{CrossOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) == 0 {
		t.Fatal("хотя бы одна межалфавитная пара должна найтись")
	}
	for _, p := range pairs {
		if !p.Cross {
			t.Fatalf("в выдачу попала пара внутри одного алфавита: %d ↔ %d", p.A, p.B)
		}
	}
}

// Понятие без вектора не роняет прогон и в кандидаты не попадает.
//
// Случай обычный, а не редкий: граф растёт заходами, и хвост без векторов
// есть почти всегда — на 27.08.2026 это 13 377 понятий из 76 182.
func TestResolveSurvivesEntitiesWithoutVectors(t *testing.T) {
	g := resolveFixture(t)
	defer g.Close()

	pairs, st, err := g.ResolveCandidates(ResolveOpts{Full: true})
	if err != nil {
		t.Fatal(err)
	}
	if st.Entities != 9 || st.WithVectors != 8 {
		t.Fatalf("понятий %d, с векторами %d — ожидалось 9 и 8", st.Entities, st.WithVectors)
	}
	for _, p := range pairs {
		if p.A == 9 || p.B == 9 {
			t.Fatalf("понятие без вектора попало в кандидаты: %+v", p)
		}
	}
}

// Полный перебор находит двойников, которых модель синонимом не связывала.
func TestResolveFullFindsUnlinkedPair(t *testing.T) {
	g := resolveFixture(t)
	defer g.Close()

	quiet, _, err := g.ResolveCandidates(ResolveOpts{})
	if err != nil {
		t.Fatal(err)
	}
	full, _, err := g.ResolveCandidates(ResolveOpts{Full: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(full) <= len(quiet) {
		t.Fatalf("полный перебор нашёл %d пар против %d — должен находить больше",
			len(full), len(quiet))
	}
	// «regex» и «video» лежат на одной оси, синонимом не связаны.
	if p := find(full, 2, 7); p == nil {
		t.Error("полный перебор обязан найти близкую пару без связи по синониму")
	} else if p.AliasLink {
		t.Error("эта пара синонимом не связана, а помечена связанной")
	}
}

// Близость не выходит за единицу.
//
// Векторы хранятся в int8, и после округления скалярное произведение самых
// близких пар вылезает за единицу на тысячные. «Близость 1.001» читается
// как поломка.
func TestResolveCosineNeverExceedsOne(t *testing.T) {
	g := resolveFixture(t)
	defer g.Close()

	pairs, _, err := g.ResolveCandidates(ResolveOpts{Full: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pairs {
		if p.Cos > 1 {
			t.Fatalf("близость %.4f больше единицы у пары %d ↔ %d", p.Cos, p.A, p.B)
		}
	}
}

// digitsOnlyDiffer срабатывает только на различии в цифрах.
func TestDigitsOnlyDiffer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"mistral 7b instruct v0.1", "mistral 7b instruct v0.3", true},
		{"gpt 4", "gpt 5", true},
		{"video", "видео", false},
		{"gpt 4", "gpt 4", false},   // одно и то же
		{"redis", "valkey", false},  // цифр нет вовсе
		{"class=1", "class=", true}, // цифра пропала целиком
	}
	for _, c := range cases {
		if got := digitsOnlyDiffer(c.a, c.b); got != c.want {
			t.Errorf("digitsOnlyDiffer(%q, %q) = %v, ожидалось %v", c.a, c.b, got, c.want)
		}
	}
}

// Взаимный синоним допускается с более низкого порога, односторонний — нет.
//
// Замер 02.09.2026: `горутина ↔ goroutine` даёт 0.853 и при общем пороге 0.90
// в кандидаты не попадает вовсе — то есть разрешение сущностей проходит мимо
// главного двойника библиотеки.
func TestMutualAliasHasLowerThreshold(t *testing.T) {
	o := ResolveOpts{}.norm()
	if o.MinCos != 0.90 {
		t.Fatalf("порог по умолчанию должен остаться 0.90, получено %v", o.MinCos)
	}
	if o.MinCosMutual != 0.70 {
		t.Fatalf("порог взаимных должен быть 0.70, получено %v", o.MinCosMutual)
	}

	// Порог взаимных выше общего смысла не имеет: он бы отсекал строже,
	// чем правило для одностороннего синонима, ради которого и вводился.
	o = ResolveOpts{MinCos: 0.85, MinCosMutual: 0.95}.norm()
	if o.MinCosMutual != 0.85 {
		t.Fatalf("порог взаимных обязан быть прижат к общему, получено %v", o.MinCosMutual)
	}
}
