package graph

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Реестр сущностей.
//
// Устроен как реестр книг коллекции: обычный JSONL, только дозапись. Правка
// сущности — это новая строка с тем же номером, и при чтении побеждает
// последняя. Так прогон, оборванный на середине, теряет ровно последнюю строку,
// а не весь реестр.

// Типы сущностей. Список закрыт намеренно: свободные типы у разных книг
// разъезжаются («язык», «ЯП», «programming language»), и граф перестаёт быть
// сравнимым. Незнакомый тип приводится к TypeConcept.
const (
	TypeConcept  = "понятие"
	TypeTech     = "технология"
	TypeTool     = "инструмент"
	TypeStandard = "стандарт"
	TypeFormat   = "формат"
	TypePerson   = "человек"
	TypeOrg      = "организация"
)

// KnownTypes — все допустимые типы сущностей.
func KnownTypes() []string {
	return []string{TypeConcept, TypeTech, TypeTool, TypeStandard, TypeFormat, TypePerson, TypeOrg}
}

// NormalizeType приводит тип к известному. Неизвестное — «понятие»: потерять
// оттенок смысла лучше, чем завести новый вид узла из-за опечатки модели.
func NormalizeType(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	for _, k := range KnownTypes() {
		if t == k {
			return k
		}
	}
	return TypeConcept
}

// Entity — сущность графа.
type Entity struct {
	ID   uint32 `json:"id"`
	Name string `json:"name"` // как пишется в книгах
	Norm string `json:"norm"` // приведённое имя, по нему ищется совпадение
	Type string `json:"type"`

	// Aliases — другие написания того же понятия: «KV cache», «кэш ключей».
	// Без них «Go» и «golang» стали бы разными узлами.
	Aliases []string `json:"aliases,omitempty"`

	// Docs и Count — в скольких книгах встречается и сколько всего упоминаний.
	// Нужны для веса при поиске: понятие из одной книги и понятие из сорока —
	// разной ценности.
	Docs  int `json:"docs,omitempty"`
	Count int `json:"count,omitempty"`

	At int64 `json:"at"`
}

// SearchableAliases — синонимы, которым можно верить: те, по которым понятие
// вообще ищется.
//
// Показывать надо именно их. Модель, увидев в карточке «RAG, он же векторная
// база данных», так и ответит — а это разные вещи, и в графе они разные узлы.
// Отсеянные синонимы остаются в файле: по ним видно, что модель напутала
// при разборе, и это пригодится, когда дойдут руки до чистки.
func (e Entity) SearchableAliases() []string {
	if len(e.Aliases) == 0 {
		return nil
	}
	out := make([]string, 0, len(e.Aliases))
	for _, a := range e.Aliases {
		if usableAlias(e.Norm, Normalize(a)) {
			out = append(out, a)
		}
	}
	return out
}

// Entities — реестр сущностей с поиском по нормализованному имени.
type Entities struct {
	mu   sync.RWMutex
	path string
	f    *os.File
	w    *bufio.Writer

	// merges — склейки, надеваемые при чтении. Поиск обязан вести к выжившему.
	merges *Merges

	// stemMinLen — короче этой основы ключом byStem не становится
	// (Rules.StemMinLen); ноль — умолчание.
	stemMinLen int

	list  []Entity          // по номеру: list[id-1]
	byKey map[string]uint32 // нормализованное имя или синоним → номер

	// byStem — то же, но по основам слов: «переранжирование» и
	// «переранжировать» дают один ключ. Нужен входу в граф: вопрос задают
	// живой речью, а понятия записаны словарной формой. Замер 26.08.2026:
	// «чем переранжировать найденные куски» не находило ничего, тогда как
	// «переранжирование найденных кусков» находило reranking.
	//
	// Отдельная карта, а не замена byKey: точное совпадение надёжнее и должно
	// проверяться первым. Стеммер иногда сливает разные слова, и пускать его
	// вперёд точного попадания нельзя.
	byStem map[string]uint32
}

const entitiesFile = "entities.jsonl"

func openEntities(dir string, stemMinLen int) (*Entities, error) {
	return openEntitiesWith(dir, stemMinLen, nil)
}

func openEntitiesWith(dir string, stemMinLen int, cb func(OpenProgress)) (*Entities, error) {
	e := &Entities{path: filepath.Join(dir, entitiesFile), stemMinLen: stemMinLen,
		byKey: map[string]uint32{}, byStem: map[string]uint32{}}
	if err := e.load(cb); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(e.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	e.f, e.w = f, bufio.NewWriter(f)
	return e, nil
}

// load читает реестр. Битая строка не роняет чтение: она пропускается,
// и загружается всё остальное — оборванная запись это последняя строка после
// внезапного выключения, а не повод потерять сорок тысяч сущностей.
func (e *Entities) load(cb func(OpenProgress)) error {
	f, err := os.Open(e.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	// Размер файла — знаменатель полосы хода. Не узнали — покажем шаг без
	// процентов, но чтение из-за этого останавливать незачем.
	var total int64
	if fi, err := f.Stat(); err == nil {
		total = fi.Size()
	}
	var read, next int64

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if cb != nil {
			read += int64(len(line)) + 1 // +1 — перевод строки, съеденный сканером
			if read >= next {
				cb(OpenProgress{Stage: "реестр понятий", Done: read, Total: total})
				// Раз в восемь мегабайт: чаще — лишняя работа на каждой строке,
				// реже — полоса прыгает рывками и выглядит зависшей.
				next = read + 8<<20
			}
		}
		if len(line) == 0 {
			continue
		}
		var ent Entity
		if err := json.Unmarshal(line, &ent); err != nil || ent.ID == 0 {
			continue
		}
		e.put(ent)
	}
	return sc.Err()
}

// put кладёт сущность в память, расширяя список под её номер.
//
// Собственное имя сущности всегда занимает свой ключ. А вот синоним — только
// свободный: чужое имя он не отбирает.
//
// Замерено 23.08.2026 на первых 1728 кусках: модель кладёт в синонимы не другое
// написание того же понятия, а родственное. «искусственный интеллект» получил
// в синонимы «ChatGPT» и «нейросети», «глубокие нейронные сети» — «машинное
// обучение». Если такому синониму позволить отобрать ключ, два разных понятия
// сливаются в одно, и граф начинает уверенно врать. Промпт это требование
// тоже проговаривает, но полагаться на послушание модели здесь нельзя.
func (e *Entities) put(ent Entity) {
	for uint32(len(e.list)) < ent.ID {
		e.list = append(e.list, Entity{})
	}
	e.list[ent.ID-1] = ent
	e.claimKey(ent.Norm, ent.ID)
	e.putStem(ent.Norm, ent.ID)
	for _, a := range ent.Aliases {
		k := Normalize(a)
		if k == "" || !usableAlias(ent.Norm, k) {
			continue
		}
		e.claimKey(k, ent.ID)
		e.putStem(k, ent.ID)
	}
}

// Правило владения спорным ключом.
//
// **Почему не «кто первый».** Раньше синоним доставался тому, чья запись
// раньше легла в файл, а собственное имя просто затирало ключ. И то и другое
// зависело от порядка записей — то есть от того, в каком порядке сборка
// встречала куски книг. Замер 02.09.2026: 755 ключей из 222 240 и 2008 основ
// слов из 154 128 меняли владельца от одной лишь перестановки записей. При
// этом ключ `bias` доставался понятию «предвзятости» с четырьмя упоминаниями,
// а не «смещению» с четырьмя сотнями — просто потому, что так легло.
//
// **Правило.** Сильнее тот, у кого это ключ собственного имени, а не синонима;
// при равенстве — у кого больше упоминаний; при равенстве и тут — меньший
// номер. Порядок сравнения полный, поэтому итог не зависит ни от порядка
// записей в файле, ни от того, сколько раз запись переписывалась. Это и делает
// уплотнение реестра (см. compact.go) тождественным по построению.
func (e *Entities) claimKey(key string, id uint32) {
	cur, taken := e.byKey[key]
	if !taken || e.strongerName(key, id, cur) {
		e.byKey[key] = id
	}
}

// strongerName — притязание a сильнее притязания b на ключ-написание.
func (e *Entities) strongerName(key string, a, b uint32) bool {
	x, y := e.rawAt(a), e.rawAt(b)
	return strongerClaim(x.Norm == key, x.Count, a, y.Norm == key, y.Count, b)
}

// strongerClaim — общий порядок притязаний, один на имена и на основы слов.
func strongerClaim(aOwn bool, aCount int, aID uint32, bOwn bool, bCount int, bID uint32) bool {
	if aOwn != bOwn {
		return aOwn
	}
	if aCount != bCount {
		return aCount > bCount
	}
	return aID < bID
}

// rawAt отдаёт запись по номеру без блокировки и без склеек: зовётся из put,
// который уже идёт под замком, а во время чтения реестра замок не нужен вовсе.
func (e *Entities) rawAt(id uint32) Entity {
	if id == 0 || uint32(len(e.list)) < id {
		return Entity{}
	}
	return e.list[id-1]
}

// minStem — действующая минимальная длина основы.
func (e *Entities) minStem() int {
	if e.stemMinLen > 0 {
		return e.stemMinLen
	}
	return DefaultStemMinLen
}

// putStem заводит ключ по основам слов.
//
// Занято — не перезаписываем: первым в реестр попадает то понятие, которое
// в книгах встретилось раньше, и отбирать у него ключ у более позднего
// однокоренного нет причин.
func (e *Entities) putStem(key string, id uint32) {
	st := kb.StemPhrase(key)
	if st == "" || st == key {
		return // основа совпала с написанием — byKey и так найдёт
	}
	if len([]rune(st)) < e.minStem() {
		return // «и», «с», «в» — не ключи, а союзы и предлоги (см. lexrules.go)
	}
	cur, taken := e.byStem[st]
	if !taken || e.strongerStem(st, id, cur) {
		e.byStem[st] = id
	}
}

// strongerStem — то же правило владения, но для словаря основ слов. Своей
// основа считается тогда, когда она получена из собственного имени понятия,
// а не из синонима.
func (e *Entities) strongerStem(stem string, a, b uint32) bool {
	x, y := e.rawAt(a), e.rawAt(b)
	return strongerClaim(kb.StemPhrase(x.Norm) == stem, x.Count, a,
		kb.StemPhrase(y.Norm) == stem, y.Count, b)
}

// LookupStem находит сущность по основам слов запроса.
//
// Отдельно от Lookup, потому что зовётся только после него: точное совпадение
// всегда вернее приблизительного.
func (e *Entities) LookupStem(name string) (Entity, bool) {
	st := kb.StemPhrase(Normalize(name))
	if st == "" || len([]rune(st)) < e.minStem() {
		// Тот же довод, что и при укладке: искать понятие по основе «и»
		// значит находить его в любом русском вопросе.
		return Entity{}, false
	}
	e.mu.RLock()
	id, ok := e.byStem[st]
	e.mu.RUnlock()
	if !ok {
		return Entity{}, false
	}
	return e.Get(id)
}

// usableAlias решает, можно ли искать сущность по этому синониму.
//
// Замерено 23.08.2026 на живом графе: у «RAG» в синонимах оказались «векторная
// база данных» и «векторная база знаний» — это не другие написания RAG, а другие
// понятия. Найдя такую сущность по такому ключу, поиск уверенно ответит не о том,
// о чём спросили.
//
// Общего слова мало: «глубокое обучение» и «машинное обучение» делят слово
// «обучение», но это разные понятия — и ровно так граф начинает врать.
// Годными считаются три случая:
//
//  1. сокращение по первым буквам: «RAG» и «Retrieval-Augmented Generation»;
//  2. те же слова в другом порядке или с другими знаками;
//  3. несовпавшие слова написаны другим алфавитом: «KV cache» и «KV-кэш»,
//     «вытеснение контекста» и «context eviction». Смена алфавита — почти
//     всегда перевод того же термина, а замена русского слова на другое русское
//     («машинное» на «глубокое») — почти всегда другое понятие. Это и отличает
//     «cache→кэш» от «машинное→глубокое».
//
// Всё прочее остаётся в карточке понятия справкой, но ключом поиска не
// становится. Правило не ловит переводы с другим числом слов — «knowledge graph»
// и «граф знаний» тут ещё пройдут, а «граф знаний» и «knowledge graph database»
// уже нет; это осознанная цена, такие случаи подберёт смысловой поиск
// по векторам имён.
func usableAlias(norm, alias string) bool {
	if norm == "" || alias == "" {
		return false
	}
	nw := strings.Fields(norm)
	aw := strings.Fields(alias)
	if len(nw) == 0 || len(aw) == 0 {
		return false
	}
	if acronymOf(norm, aw) || acronymOf(alias, nw) {
		return true
	}

	// Одно и то же написание, различающееся лишь знаками-разделителями:
	// «ToolCall ← Tool Call», «stdin ← os.Stdin». Сравниваем, выбросив всё,
	// кроме букв и цифр, и приведя к одному регистру. РАВЕНСТВО, а не вхождение:
	// вхождение ловило бы «go» внутри «goroutine» и «host» в «hostname» —
	// разные понятия. Замер 03.09.2026: равенство даёт +23 верных перевода при
	// 5 лишних выдумках, и все пять — сужения, а не подмена понятия.
	if a, b := lettersDigits(norm), lettersDigits(alias); a != "" && a == b {
		return true
	}

	// Раскрытие и уточнение: слова короткого набора идут в длинном по порядку.
	// «OSI model ← Open Systems Intercommunication model», «net.Dial ← net.Dial
	// function», «CPU profiling ← CPU». Это то же понятие, названное подробнее
	// или короче, а не другое.
	if subseqWords(nw, aw) || subseqWords(aw, nw) {
		return true
	}

	// Одинаковое число слов: перевод, если ВСЕ несовпавшие пары — через границу
	// алфавита. «вытеснение контекста ← context eviction» — да; «машинное
	// обучение ← глубокое обучение» (замена русского на русское) — нет.
	if len(nw) == len(aw) {
		distinct, translated := 0, 0
		for i := range nw {
			if nw[i] == aw[i] {
				continue
			}
			distinct++
			if script(nw[i]) != script(aw[i]) {
				translated++
			}
		}
		return distinct == 0 || distinct == translated
	}

	// Разное число слов и не подмножество — отвергаем.
	//
	// **Замер 03.09.2026, aliases_sample.tsv.** Пробовали смягчить и
	// эту ветку — принимать пару, где стороны целиком на разных алфавитах, как
	// перевод. Отвергнуто инвариантом: `RAG ← векторная база данных` и
	// `ИИ ← ChatGPT` структурно НЕОТЛИЧИМЫ от верного
	// `APIs ← интерфейсы прикладного программирования` — короткий латинский
	// термин против многословной русской фразы. Выдумка это ошибка смысла,
	// а не формы; текстом её тут не отделить. Один потерянный перевод дешевле
	// впущенной подмены понятия.
	return false
}

// lettersDigits — строка из одних букв и цифр в нижнем регистре: знаки-
// разделители (точки, дефисы, пробелы, скобки) выброшены. Так «sync.WaitGroup»
// и «WaitGroup» сводятся к сравнимому виду.
func lettersDigits(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// subseqWords — все слова a идут в b по порядку (подпоследовательность).
// Пустой a или a длиннее b — не подпоследовательность.
func subseqWords(a, b []string) bool {
	if len(a) == 0 || len(a) > len(b) {
		return false
	}
	j := 0
	for _, w := range b {
		if j < len(a) && a[j] == w {
			j++
		}
	}
	return j == len(a)
}

// script — какой письменностью написано слово: кириллица, латиница или прочее.
func script(w string) int {
	var cyr, lat bool
	for _, r := range w {
		switch {
		case r >= 'а' && r <= 'я':
			cyr = true
		case r >= 'a' && r <= 'z':
			lat = true
		}
	}
	switch {
	case cyr && !lat:
		return 1
	case lat && !cyr:
		return 2
	default:
		return 0
	}
}

// acronymOf проверяет, что short — сокращение слов words по первым буквам.
func acronymOf(short string, words []string) bool {
	short = strings.ReplaceAll(short, " ", "")
	if len(short) < 2 || len(words) < 2 || len([]rune(short)) != len(words) {
		return false
	}
	sr := []rune(short)
	for i, w := range words {
		wr := []rune(w)
		if len(wr) == 0 || wr[0] != sr[i] {
			return false
		}
	}
	return true
}

// Count возвращает число сущностей.
func (e *Entities) Count() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	n := 0
	for _, ent := range e.list {
		if ent.ID != 0 {
			n++
		}
	}
	return n
}

// Get возвращает сущность по номеру.
func (e *Entities) Get(id uint32) (Entity, bool) {
	id = e.merges.Resolve(id)
	e.mu.RLock()
	if id == 0 || uint32(len(e.list)) < id {
		e.mu.RUnlock()
		return Entity{}, false
	}
	ent := e.list[id-1]
	e.mu.RUnlock()
	if ent.ID == 0 {
		return Entity{}, false
	}
	return e.withAbsorbed(ent), true
}

// withAbsorbed приписывает выжившему имена поглощённых понятий.
//
// Без этого склейка теряла бы написания: понятие «видео» поглотило `video`,
// и слово `video` перестало бы находиться вовсе. Приписываются именно
// синонимами — так они и ищутся, и видны человеку в карточке.
func (e *Entities) withAbsorbed(ent Entity) Entity {
	gone := e.merges.Absorbed(ent.ID)
	if len(gone) == 0 {
		return ent
	}
	out := ent
	out.Aliases = append([]string(nil), ent.Aliases...)
	have := map[string]bool{ent.Norm: true}
	for _, a := range ent.Aliases {
		have[Normalize(a)] = true
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, id := range gone {
		if id == 0 || uint32(len(e.list)) < id {
			continue
		}
		src := e.list[id-1]
		for _, name := range append([]string{src.Name}, src.Aliases...) {
			k := Normalize(name)
			if k == "" || have[k] {
				continue
			}
			have[k] = true
			out.Aliases = append(out.Aliases, name)
		}
		out.Docs += src.Docs
		out.Count += src.Count
	}
	return out
}

// useMerges надевает журнал склеек на реестр.
func (e *Entities) useMerges(m *Merges) { e.merges = m }

// Live возвращает сущности, не поглощённые склейкой.
//
// Отдельно от All нарочно: All означает «все записи реестра» и нужен там, где
// место в файле определяется номером — прежде всего счёту векторов. Пропуск
// записей там оставил бы дырки, а поиск двойников и разбиение на сообщества,
// наоборот, должны видеть уже склеенный граф.
func (e *Entities) Live() []Entity {
	all := e.All()
	if e.merges.Count() == 0 {
		return all
	}
	out := make([]Entity, 0, len(all))
	for _, ent := range all {
		if e.merges.Gone(ent.ID) {
			continue
		}
		out = append(out, e.withAbsorbed(ent))
	}
	return out
}

// Синонимы понятия: два списка, потому что назначения разные.
//
// **Замер на эталоне 03.09.2026** (`aliases_sample.tsv`, 120 синонимов
// частых понятий, разметка глазами: 65 переводов, 32 выдумки, 23 спорных).
// Правило «синоним совпадает с собственным именем другого понятия» ловит
// 16 выдумок из 32 — и задевает **11 верных синонимов из 65**, каждый шестой.
// Задеваются `WaitGroup ← sync.WaitGroup`, `ECDHE ← ECDHE key exchange`: это
// и правда отдельные узлы графа, но человеку читаются как одно и то же.
//
// Отсюда разные ответы для разных мест:
//
//	DisplayAliases — человеку и модели в карточке: показываем всё пригодное.
//	                 «WaitGroup, он же sync.WaitGroup» это подсказка, а не подмена;
//	SafeAliases    — в вектор понятия и в расширение запроса: чужое имя тянет
//	                 вектор к чужому смыслу, а в запрос подмешивает чужой термин.
//
// Порядок в обоих: написания другим алфавитом впереди — это переводы, ради
// которых мост между русским вопросом и английской книгой и строится.

// DisplayAliases — синонимы для показа человеку и модели.
func (e *Entities) DisplayAliases(ent Entity) []string {
	return e.aliases(ent, false)
}

// SafeAliases — синонимы для вектора понятия и расширения запроса: без тех,
// что являются собственным именем другого понятия.
func (e *Entities) SafeAliases(ent Entity) []string {
	return e.aliases(ent, true)
}

func (e *Entities) aliases(ent Entity, dropClashes bool) []string {
	if len(ent.Aliases) == 0 {
		return nil
	}
	e.mu.RLock()
	defer e.mu.RUnlock()

	var translations, rest []string
	for _, a := range ent.Aliases {
		k := Normalize(a)
		if k == "" || !usableAlias(ent.Norm, k) {
			continue
		}
		if dropClashes {
			if id, ok := e.byKey[k]; ok && id != ent.ID {
				if other := e.rawAt(id); other.Norm == k {
					continue
				}
			}
		}
		if s1, s2 := script(ent.Norm), script(k); s1 != s2 && s2 != 0 {
			translations = append(translations, a)
			continue
		}
		rest = append(rest, a)
	}
	return append(translations, rest...)
}

// Lookup ищет сущность по любому её написанию.
func (e *Entities) Lookup(name string) (Entity, bool) {
	key := Normalize(name)
	if key == "" {
		return Entity{}, false
	}
	e.mu.RLock()
	id, ok := e.byKey[key]
	e.mu.RUnlock()
	if !ok {
		return Entity{}, false
	}
	return e.Get(id)
}

// All возвращает все сущности реестра.
func (e *Entities) All() []Entity {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Entity, 0, len(e.list))
	for _, ent := range e.list {
		if ent.ID != 0 {
			out = append(out, ent)
		}
	}
	return out
}

// Add заводит сущность или возвращает уже заведённую с тем же именем.
//
// Возвращает номер и признак того, что сущность новая. Совпадение ищется
// по нормализованному имени и по синонимам: модель называет одно и то же
// понятие по-разному в каждой второй книге.
func (e *Entities) Add(name, typ string, aliases ...string) (uint32, bool, error) {
	norm := Normalize(name)
	if norm == "" {
		return 0, false, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	// Совпадение по собственному имени — это то же самое понятие.
	if id, ok := e.byKey[norm]; ok {
		return id, false, e.mergeAliases(id, aliases)
	}
	// А вот по синониму сливать нельзя. Соблазн велик: «глубокое обучение»
	// с синонимом «машинное обучение» слилось бы с уже заведённым «машинным
	// обучением» — и два разных понятия стали бы одним узлом. Синоним годится,
	// только чтобы найти уже известное **под чужим написанием**, а решает это
	// человек при разборе, а не сборка вслепую.

	ent := Entity{
		ID:      uint32(len(e.list) + 1),
		Name:    strings.TrimSpace(name),
		Norm:    norm,
		Type:    NormalizeType(typ),
		Aliases: cleanAliases(name, aliases),
		At:      time.Now().Unix(),
	}
	if err := e.append(ent); err != nil {
		return 0, false, err
	}
	e.put(ent)
	return ent.ID, true, nil
}

// mergeAliases дописывает синонимы к уже известной сущности.
// AddAliases дописывает понятию синонимы под замком: снаружи реестра
// mergeAliases звать нельзя.
func (e *Entities) AddAliases(id uint32, aliases ...string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.mergeAliases(id, aliases)
}

func (e *Entities) mergeAliases(id uint32, aliases []string) error {
	if len(aliases) == 0 || uint32(len(e.list)) < id {
		return nil
	}
	ent := e.list[id-1]
	have := map[string]bool{ent.Norm: true}
	for _, a := range ent.Aliases {
		have[Normalize(a)] = true
	}
	var added bool
	for _, a := range aliases {
		k := Normalize(a)
		if k == "" || have[k] {
			continue
		}
		if len(ent.Aliases) >= maxAliases {
			break
		}
		have[k] = true
		ent.Aliases = append(ent.Aliases, strings.TrimSpace(a))
		added = true
	}
	if !added {
		return nil
	}
	ent.At = time.Now().Unix()
	if err := e.append(ent); err != nil {
		return err
	}
	e.put(ent)
	return nil
}

// Touch отмечает ещё одно упоминание сущности: счётчики нужны для веса
// при поиске. Запись в файл идёт не на каждое упоминание, а при Flush —
// иначе на двухстах тысячах кусков реестр распух бы на порядок.
func (e *Entities) Touch(id uint32, newDoc bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if id == 0 || uint32(len(e.list)) < id || e.list[id-1].ID == 0 {
		return
	}
	e.list[id-1].Count++
	if newDoc {
		e.list[id-1].Docs++
	}
}

// SaveCounters переписывает счётчики всех сущностей одной пачкой дозаписи.
// Зовётся в конце волны сборки, а не после каждого куска.
func (e *Entities) SaveCounters() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now().Unix()
	for i := range e.list {
		if e.list[i].ID == 0 {
			continue
		}
		e.list[i].At = now
		if err := e.append(e.list[i]); err != nil {
			return err
		}
	}
	return e.w.Flush()
}

func (e *Entities) append(ent Entity) error {
	b, err := json.Marshal(ent)
	if err != nil {
		return err
	}
	if _, err := e.w.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

// Sync сбрасывает буфер и просит диск записать его: Flush отдаёт данные
// системе, а при отказе питания они всё ещё в её кеше. Реестр понятий.
func (e *Entities) Sync() error {
	if err := e.Flush(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.f == nil {
		return nil
	}
	return e.f.Sync()
}

// Flush дописывает буфер на диск.
func (e *Entities) Flush() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.w == nil {
		return nil
	}
	return e.w.Flush()
}

// Close закрывает реестр.
func (e *Entities) Close() error {
	if err := e.Flush(); err != nil {
		return err
	}
	if e.f == nil {
		return nil
	}
	err := e.f.Close()
	e.f = nil
	return err
}

// maxAliases — сколько синонимов хранить у одной сущности.
//
// Предел нужен потому, что модель щедра не по делу: у «тензора» она успела
// записать в синонимы даже слово «термин». Чем длиннее список, тем выше
// вероятность, что в нём окажется чужое понятие.
const maxAliases = 6

// cleanAliases убирает из списка синонимов само имя, пустые строки и лишнее.
func cleanAliases(name string, aliases []string) []string {
	norm := Normalize(name)
	var out []string
	seen := map[string]bool{norm: true}
	for _, a := range aliases {
		k := Normalize(a)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, strings.TrimSpace(a))
		if len(out) >= maxAliases {
			break
		}
	}
	return out
}

// Normalize приводит имя сущности к виду, по которому ищется совпадение.
//
// Нижний регистр, ё к е, склейка пробелов, снятие кавычек и точек по краям.
// Без этого «KV-кэш», «kv кэш» и «KV кэш.» стали бы тремя разными узлами.
// Основы слов здесь намеренно не берутся: у имён собственных вроде «Kubernetes»
// основа отсекает лишнее, и разные вещи начинают сливаться.
func Normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "ё", "е")
	s = strings.Trim(s, ".,;:!?\"'«»()[]")

	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '\r', '-', '_', '/':
			space = true
		default:
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		}
	}
	return b.String()
}
