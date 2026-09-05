package graph

import (
	"sort"
	"strings"
	"time"
	"unicode"
)

// Разрешение сущностей: поиск двойников среди понятий графа.
//
// **Зачем.** Узлы графа склеиваются только по точному совпадению
// нормализованного имени (`Entities.Add`). Поэтому «сборщик мусора»
// и `Garbage collection` — два разных узла, и у каждого своя половина связей:
// поиск находит один из них и отдаёт половину знания.
//
// Недостающий шаг называется в книгах entity resolution (*Essential GraphRAG*,
// 2025, стр. 104–105; *Building Knowledge Graphs*, 2023, стр. 150–152).
//
// **Этот файл ничего не склеивает.** Он только показывает кандидатов вместе
// со всеми признаками, по которым человек решает сам. Причина в замерах ниже:
// заранее придуманное правило склейки один раз уже оказалось негодным.
//
// **Замер 27.08.2026, три вывода.**
//
//  1. Двойников много: у 18.4% понятий есть сосед с косинусом ≥0.90.
//
//  2. Одного вектора мало. Даже выше 0.95 попадается чужое:
//     `Mistral-7B-Instruct-v0.3` ↔ `v0.1` — 0.967, а это разные модели.
//     Ровно об этом предупреждает *Agentic GraphRAG* (2026, стр. 165):
//     «Rather than relying **solely** on attribute similarity».
//
//  3. Общие соседи по графу вторым признаком **не годятся** — проверено
//     и отвергнуто. У 67% межалфавитных пар с косинусом ≥0.93 общих соседей
//     нет вовсе: граф разрежен (медиана степени 2), а двойники приходят
//     из разных книг с непересекающимся окружением. Мера всё равно считается
//     и показывается — но как справка, а не как условие.
//
// **Что работает вместо них.** Модель извлечения сама называла эти имена
// синонимами, а `Add` отказался сливать. Таких пар тысячи, и вектор делит их
// начисто: `regular expression` ↔ `regex` — 0.991, а `vulnerability scanner`
// ↔ `Nessus` — 0.352. Признак бесплатный: обе величины уже посчитаны.

// ResolvePair — пара понятий, похожих на двойников.
type ResolvePair struct {
	A, B uint32
	Cos  float64 // близость векторов имён; -1, если вектора нет

	// AliasLink — имя одного понятия записано в синонимах другого. Это
	// утверждение модели извлечения о том, что перед ней одно и то же.
	AliasLink bool

	// AliasUsable — тот же синоним прошёл бы и проверку `usableAlias`,
	// то есть годится не только на склейку, но и на поиск.
	AliasUsable bool

	// Mutual — синоним назван С ОБЕИХ сторон: A перечислил B среди своих
	// синонимов, и B перечислил A.
	//
	// **Почему это отдельный признак.** Односторонний синоним — одно суждение
	// модели, сделанное при разборе одного куска. Взаимный — два независимых
	// суждения, сделанных на разных кусках, а часто и на разных книгах.
	// Замер 02.09.2026 по всему графу:
	//
	//	пары            сколько   медиана cos   ≥0.90   ≥0.80   ≥0.70
	//	взаимные          5 978       0.856      32%     69%     91%
	//	односторонние    36 188       0.782      11%     44%     76%
	//	случайные        50 000       0.425     0.00%   0.00%   0.03%
	//
	// Случайная пара понятий не поднимается выше 0.80 ни разу из пятидесяти
	// тысяч. Значит для взаимных пар порог 0.90 избыточно строг: он отсекает
	// `горутина ↔ goroutine` (0.853) и `канал ↔ channels` (0.843) — ровно те
	// пары, ради которых разрешение сущностей и затевалось.
	Mutual bool

	SameType bool // совпадают ли типы сущностей
	Cross    bool // пара через границу алфавита

	// DigitDiff — имена различаются только цифрами. Признак «не сливать»:
	// так выглядят разные версии одного и того же (`v0.1` против `v0.3`).
	DigitDiff bool

	SharedNb int     // общих соседей по графу
	Jaccard  float64 // их доля; -1, если хотя бы у одного соседей нет

	// BooksA, BooksB — в скольких книгах встречается каждое понятие.
	// Считается по журналу упоминаний, а не по полю `Entity.Docs`: то поле
	// никогда не заполняется (`build.go` зовёт `Touch(id, false)`) и нигде
	// не читается, то есть мертво, — верить ему нельзя.
	BooksA, BooksB int
	CountA, CountB int // всего упоминаний

	// Keep — кого предлагать главным при будущей склейке. Только предложение:
	// здесь оно ни на что не влияет.
	Keep uint32
}

// ResolveOpts — как искать кандидатов.
type ResolveOpts struct {
	// MinCos — ниже этой близости пара не показывается. 0 — 0.90.
	MinCos float64

	// MinCosMutual — отдельный, более низкий порог для пар со взаимным
	// синонимом. 0 — 0.80. Довод — в описании ResolvePair.Mutual.
	// Значение выше MinCos смысла не имеет и приравнивается к нему.
	MinCosMutual float64

	// Full — перебирать все пары понятий, а не только связанные синонимом.
	// Дорого: счёт идёт на триллионы умножений и на минуты процессора.
	Full bool

	// CrossOnly — оставить только пары через границу алфавита.
	CrossOnly bool

	// Limit — сколько пар вернуть после сортировки. 0 — все.
	Limit int

	// Workers — сколько ядер занять при полном переборе. 0 — по числу ядер.
	Workers int
}

func (o ResolveOpts) norm() ResolveOpts {
	if o.MinCos == 0 {
		o.MinCos = 0.90
	}
	if o.MinCosMutual == 0 {
		// 0.70, а не 0.80: замер 02.09.2026 по полосе 0.70–0.80 (1311 взаимных
		// пар) дал соль 170 из 170 и ловушки 0 из 7, а настоящими двойниками
		// оказалась треть — включая `goroutines ↔ горутины` (0.703). Отбор
		// кандидатов и уровень склейки должны сходиться: иначе уровень
		// принимает то, чего отбор никогда не предложит.
		o.MinCosMutual = 0.70
	}
	if o.MinCosMutual > o.MinCos {
		o.MinCosMutual = o.MinCos
	}
	return o
}

// ResolveStats — что было просмотрено.
type ResolveStats struct {
	Entities    int // живых понятий (поглощённые склейкой не в счёт)
	Merged      int // поглощено склейкой
	WithVectors int // записей реестра с посчитанным вектором
	AliasPairs  int // пар, связанных синонимом, до отсечки по близости
	Found       int // пар после всех отборов
	Full        bool
	Elapsed     time.Duration
}

// ResolveCandidates ищет пары понятий, похожих на двойников.
//
// Ничего не меняет: ни реестр сущностей, ни связи, ни сообщества.
func (g *Graph) ResolveCandidates(o ResolveOpts) ([]ResolvePair, ResolveStats, error) {
	o = o.norm()
	start := time.Now()
	var st ResolveStats
	st.Full = o.Full

	// Live: уже склеенные пары предлагать заново незачем.
	list := g.Entities().Live()
	st.Entities = len(list)
	if len(list) == 0 {
		return nil, st, nil
	}

	// Понятие по номеру и по собственному имени. Собственный byKey реестра
	// для этого не годится: в нём лежат ещё и синонимы, и поиск по нему
	// нашёл бы понятие само на себя через чужое написание.
	byID := make(map[uint32]Entity, len(list))
	byName := make(map[string]uint32, len(list))
	for _, e := range list {
		byID[e.ID] = e
		if _, taken := byName[e.Norm]; !taken {
			byName[e.Norm] = e.ID
		}
	}

	vecs := g.vecs
	st.WithVectors = vecs.Count()
	st.Merged = g.merges.Count()

	// Пары, связанные синонимом. Берутся ВСЕ синонимы, а не только прошедшие
	// `usableAlias`: тот отбор писался для поиска и намеренно строг, а здесь
	// отсеивать будет вектор. Прошёл ли синоним ещё и отбор — отдельный признак.
	// Направление синонима запоминается: взаимность — отдельный признак,
	// и восстановить её потом из одного флага «синоним есть» невозможно.
	type aliasFact struct{ usable, fromLo, fromHi bool }
	linked := map[[2]uint32]aliasFact{}
	for _, e := range list {
		for _, a := range e.Aliases {
			k := Normalize(a)
			if k == "" {
				continue
			}
			j, ok := byName[k]
			if !ok || j == e.ID {
				continue
			}
			key := pairKey(e.ID, j)
			f := linked[key]
			if usableAlias(e.Norm, k) {
				f.usable = true
			}
			if e.ID == key[0] {
				f.fromLo = true
			} else {
				f.fromHi = true
			}
			linked[key] = f
		}
	}
	st.AliasPairs = len(linked)

	// Кандидаты: сначала связанные синонимом, потом — при полном переборе —
	// все остальные достаточно близкие.
	cand := make(map[[2]uint32]float64, len(linked))
	for key := range linked {
		cand[key] = cosOf(vecs, key[0], key[1])
	}
	if o.Full {
		for _, p := range vecs.pairsAbove(o.MinCos, o.Workers) {
			key := pairKey(p.A, p.B)
			if _, have := cand[key]; !have {
				cand[key] = p.Cos
			}
		}
	}

	// Окружение считается один раз на весь прогон: строить его на каждую пару
	// значит обойти все связи графа столько раз, сколько пар.
	adj, _ := g.undirected()

	out := make([]ResolvePair, 0, len(cand))
	for key, cos := range cand {
		mutual := linked[key].fromLo && linked[key].fromHi
		min := o.MinCos
		if mutual {
			min = o.MinCosMutual
		}
		if cos < min {
			continue
		}
		a, b := byID[key[0]], byID[key[1]]
		if a.ID == 0 || b.ID == 0 {
			continue // строка реестра оборвана на середине записи
		}
		cross := hasCyrillic(a.Name) != hasCyrillic(b.Name)
		if o.CrossOnly && !cross {
			continue
		}
		f, isAlias := linked[key]
		shared, jac := overlap(adj[a.ID], adj[b.ID])
		booksA, seenA := g.booksOf(a.ID)
		booksB, seenB := g.booksOf(b.ID)
		out = append(out, ResolvePair{
			A: a.ID, B: b.ID, Cos: clampCos(cos),
			AliasLink:   isAlias,
			AliasUsable: f.usable,
			Mutual:      mutual,
			SameType:    a.Type == b.Type,
			Cross:       cross,
			DigitDiff:   digitsOnlyDiffer(a.Norm, b.Norm),
			SharedNb:    shared,
			Jaccard:     jac,
			BooksA:      booksA, BooksB: booksB,
			CountA: seenA, CountB: seenB,
			Keep: keepOf(a.ID, booksA, seenA, b.ID, booksB, seenB),
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Cos != out[j].Cos {
			return out[i].Cos > out[j].Cos
		}
		if out[i].A != out[j].A {
			return out[i].A < out[j].A
		}
		return out[i].B < out[j].B
	})
	st.Found = len(out)
	if o.Limit > 0 && len(out) > o.Limit {
		out = out[:o.Limit]
	}
	st.Elapsed = time.Since(start)
	return out, st, nil
}

// pairKey — устойчивый ключ пары: меньший номер первым.
func pairKey(a, b uint32) [2]uint32 {
	if a > b {
		a, b = b, a
	}
	return [2]uint32{a, b}
}

// cosOf — близость векторов двух понятий; -1, если вектор посчитан не всем.
//
// Отдельное значение, а не ноль: ноль означал бы «совсем не похожи», а здесь
// нам просто нечем судить, и путать это с ответом нельзя.
func cosOf(v *EntityVectors, a, b uint32) float64 {
	c, ok := v.cosine(a, b)
	if !ok {
		return -1
	}
	return c
}

// keepOf выбирает, кого предлагать главным: понятие из большего числа книг,
// при равенстве — с большим числом упоминаний, при равенстве — заведённое
// раньше. Ничего не решает, только подсказывает.
func keepOf(idA uint32, booksA, seenA int, idB uint32, booksB, seenB int) uint32 {
	switch {
	case booksA != booksB:
		if booksA > booksB {
			return idA
		}
		return idB
	case seenA != seenB:
		if seenA > seenB {
			return idA
		}
		return idB
	case idA < idB:
		return idA
	default:
		return idB
	}
}

// booksOf — в скольких книгах встречается понятие и сколько всего упоминаний.
//
// Считается по журналу упоминаний, потому что поле `Entity.Docs` не заполняется
// вовсе: сборка зовёт `Touch(id, false)`, и счётчик книг остаётся нулём.
func (g *Graph) booksOf(id uint32) (books, seen int) {
	keys := g.Mentions().Of(id)
	if len(keys) == 0 {
		return 0, 0
	}
	docs := make(map[uint32]struct{}, 4)
	for _, k := range keys {
		docs[k.Doc] = struct{}{}
	}
	return len(docs), len(keys)
}

// clampCos срезает косинус до единицы.
//
// Векторы хранятся в int8 с приведением к 127, и после округления скалярное
// произведение самых близких пар выходит за единицу на тысячные. Показывать
// «близость 1.001» нельзя: читается как поломка.
func clampCos(c float64) float64 {
	if c > 1 {
		return 1
	}
	return c
}

// overlap — сколько общих соседей у двух понятий и какова их доля.
//
// Возвращает -1 в доле, если хотя бы у одного соседей нет: ноль означал бы
// «соседи разные», а на деле сравнивать нечего. Замер 27.08.2026 показал,
// что таких пар среди межалфавитных двойников большинство.
func overlap(a, b map[uint32]float64) (int, float64) {
	if len(a) == 0 || len(b) == 0 {
		return 0, -1
	}
	small, big := a, b
	if len(small) > len(big) {
		small, big = big, small
	}
	var common int
	for id := range small {
		if _, ok := big[id]; ok {
			common++
		}
	}
	union := len(a) + len(b) - common
	if union == 0 {
		return common, -1
	}
	return common, float64(common) / float64(union)
}

// digitsOnlyDiffer — имена совпадают всюду, кроме цифр.
//
// Признак «не сливать»: так выглядят разные версии одного и того же —
// `Mistral-7B-Instruct-v0.1` и `v0.3` дают близость 0.967, а это разные
// модели. Без этой проверки они попали бы в двойники по любому порогу.
func digitsOnlyDiffer(a, b string) bool {
	if a == b {
		return false
	}
	da, sa := splitDigits(a)
	db, sb := splitDigits(b)
	if sa != sb || sa == "" {
		return false
	}
	return da != db
}

// splitDigits разделяет строку на цифры и всё остальное.
func splitDigits(s string) (digits, rest string) {
	var d, r strings.Builder
	for _, c := range s {
		if unicode.IsDigit(c) {
			d.WriteRune(c)
		} else {
			r.WriteRune(c)
		}
	}
	return d.String(), r.String()
}
