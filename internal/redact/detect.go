package redact

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Kind — вид персональных данных.
type Kind int

const (
	KindNone    Kind = iota
	KindClient       // имя клиента (пациента) — в .md становится CLIENT
	KindDoctor       // имя врача — DOCTOR
	KindPerson       // имя, чья роль из документа не ясна, — PERSON
	KindAddress      // адрес — из .md убирается
	KindPhone        // телефон, факс
	KindEmail        // почта
	KindID           // номер карты, полиса, исследования, страховки, СНИЛС
	KindBirth        // дата рождения
	KindOrg          // название клиники, лаборатории: по нему выходят на врача
	KindOther        // строка, которую велели скрыть
	KindHidden       // неразборчивое: почерк, подпись, печать, логотип
)

var kindNames = map[Kind]string{
	KindClient: "имя клиента", KindDoctor: "имя врача", KindPerson: "имя (роль не ясна)",
	KindAddress: "адрес", KindPhone: "телефон", KindEmail: "почта", KindID: "номер",
	KindBirth: "дата рождения", KindOrg: "организация", KindOther: "строка по просьбе", KindHidden: "неразборчивое",
}

func (k Kind) String() string { return kindNames[k] }

// Replacement — чем имя заменяется в .md. Прочее персональное убирается
// вовсе: пустая строка.
func (k Kind) Replacement() string {
	switch k {
	case KindClient:
		return "CLIENT"
	case KindDoctor:
		return "DOCTOR"
	case KindPerson:
		return "PERSON"
	}
	return ""
}

// removed — вид, который из .md убирается целиком.
func (k Kind) removed() bool { return k != KindNone && k.Replacement() == "" }

func (k Kind) person() bool { return k == KindClient || k == KindDoctor || k == KindPerson }

// nameMaxWords — имя в поле не длиннее четырёх слов: дальше в той же строке
// идёт уже другое (дата подписи, номер станции), и его ловят свои правила.
const nameMaxWords = 4

// line — строка распознавания: слова подряд и их место в тексте строки.
type line struct {
	key   [4]int // страница, блок, абзац, строка
	idx   []int
	text  string
	spans []span
}

type span struct{ start, end, word int }

func buildLines(words []Word) []line {
	var out []line
	pos := map[[4]int]int{}
	for i, w := range words {
		k := [4]int{w.Page, w.Block, w.Par, w.Line}
		j, ok := pos[k]
		if !ok {
			j = len(out)
			pos[k] = j
			out = append(out, line{key: k})
		}
		out[j].idx = append(out[j].idx, i)
	}
	for j := range out {
		var b strings.Builder
		for n, i := range out[j].idx {
			if n > 0 {
				b.WriteByte(' ')
			}
			s := b.Len()
			b.WriteString(words[i].Text)
			out[j].spans = append(out[j].spans, span{s, b.Len(), i})
		}
		out[j].text = b.String()
	}
	return out
}

// mark помечает слова, задетые отрезком [s, e) текста строки, и возвращает,
// сколько задето. Имя (person) кончается на первом слове с цифрой и не длиннее
// nameMaxWords: «John Roe MD 01/02/2026» — имя, а дата уже нет.
func (l *line) mark(words []Word, s, e int, k Kind, why string) int {
	n := 0
	for _, sp := range l.spans {
		if sp.start >= e || sp.end <= s {
			continue
		}
		w := &words[sp.word]
		if k.person() && (n >= nameMaxWords || strings.ContainsAny(w.Text, "0123456789")) {
			break
		}
		if w.Kind == KindNone {
			w.Kind, w.Why = k, why
		}
		n++
	}
	return n
}

// Подписи полей. Значение поля тянется от двоеточия до следующей подписи
// в той же строке: «Patient: ИМЯ DOB: ДАТА Exam Date: …». Поэтому в словаре
// есть и подписи, значения которых не тайна («Exam», «Date»): они нужны как
// граница.
const labelVocab = `(?:patient|name|client|insured|subscriber|mrn|dob|date|of|birth|birthdate|` +
	`exam|acc|accession|no|number|referring|ordering|attending|primary|treating|provider|` +
	`physician|doctor|radiologist|dictated|read|address|addr|copies|to|signed|by|` +
	`electronically|workstation|phone|telephone|tel|fax|mobile|cell|e-?mail|insurance|` +
	`member|id|policy|group|ssn|account|chart|claim|npi|sex|gender|age|location|reported|` +
	`born|residence|resident|` +
	`пациент|пациентка|фио|ф\.\s?и\.\s?о\.?|имя|фамилия|отчество|клиент|застрахованный|` +
	`дата|рождения|полис|омс|дмс|снилс|инн|паспорт|номер|карта|карты|телефон|тел\.?|факс|` +
	`адрес|почта|эл\.?|врач|лечащий|направивший|доктор|подпись|` +
	`родился|родилась|прописан|прописана|проживает|зарегистрирован|зарегистрирована)`

// «;» вместо «:» — частая ошибка распознавания моноширинного шрифта
// (образец: «Patient;» в шапке второго листа).
//
// Апостроф — тоже двоеточие, прочитанное с ошибкой («Accession ID' …»,
// образец 06.10.2026), но только с пробелом следом: «Patient's» — не подпись.
var labelRe = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(` + labelVocab +
	`(?:[\s#.]+` + labelVocab + `){0,3}\s*[#.№]?\s*(?:[:;]|['’]\s))`)

var pdLabels = []struct {
	re   *regexp.Regexp
	kind Kind
}{
	{full(`patient( name)?|(full )?name|client( name)?|insured( name)?|subscriber|пациент(ка)?|` +
		`фио|ф\. ?и\. ?о\.?|имя|фамилия( имя отчество)?|клиент|застрахованный`), KindClient},
	{full(`((referring|ordering|attending|primary|treating) )?(provider|physician|doctor)|radiologist|` +
		`copies to|(electronically )?signed by|dictated by|read by|((лечащий|направивший) )?врач|доктор|подпись`), KindDoctor},
	{full(`mrn|acc(ession)?( (no|number))?|member id|policy( (no|number))?|insurance( (id|no|number))?|` +
		`group( (no|number))?|ssn|account( (no|number))?|chart( (no|number))?|claim( (no|number))?|` +
		`npi|workstation|id|полис( омс| дмс)?|снилс|инн|паспорт( рф| гражданина рф)?( серия)?|номер( карты)?|карта`), KindID},
	{full(`dob|d\.o\.b|date of birth|birth ?date|born|дата рождения|д\. ?р|родил(ся|ась)`), KindBirth},
	{full(`((referring|home|postal|mailing) )?address|addr|residence|resident|адрес|прописана?|проживает|зарегистрирована?`), KindAddress},
	{full(`phone|telephone|tel|fax|mobile|cell|телефон|тел\.?|факс`), KindPhone},
	{full(`e-?mail|почта|эл\.? почта`), KindEmail},
}

func full(s string) *regexp.Regexp { return regexp.MustCompile(`^(?:` + s + `)$`) }

var spaceRe = regexp.MustCompile(`\s+`)

func labelKind(label string) Kind {
	name := strings.ToLower(strings.TrimRight(label, ":;#№. \t'’"))
	name = spaceRe.ReplaceAllString(strings.ReplaceAll(name, "#", ""), " ")
	for _, l := range pdLabels {
		if l.re.MatchString(name) {
			return l.kind
		}
	}
	return KindNone
}

// labelBareRe — подписи, после которых значение идёт без двоеточия:
// «DOB 03/04/1971», «Acc No. 50505», «ID# 404040», «(id #404040» (значения
// здесь выдуманы) — так написаны шапка факса и карточка приёма в образце
// 06.10.2026, и номера
// с датами рождения уходили в .md. Только явные подписи номера и даты
// рождения и только перед цифрой: «No» или «ID» в тексте подписью не
// считаются, а «ID» без «#» — только перед четырьмя цифрами и больше.
//
// Русские бланки пишут так же: «Дата рождения 01.02.1970», «Паспорт серия
// 4508 № 123456», «Полис ОМС 1234 5678 9012 3456», «ИНН 7712…», «Тел.
// (4822) 12-34-56» (значения выдуманы), — и до 07.10.2026 всё это оставалось
// в .md: подпись без двоеточия не узнавалась. Подписи телефона — только
// явные: «ph» и «cell» в анализах — это pH и клетки.
var labelBareRe = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])((?:dob|d\.o\.b\.?|mrn|ssn|npi|` +
	`acc(?:ession)?\s*(?:no\.?|#|number)|account\s*(?:no\.?|#)|member\s*id|id(?:\s*#)?|снилс|` +
	`полис(?:\s+(?:омс|дмс))?|инн|паспорт(?:\s+(?:рф|гражданина\s+рф))?(?:\s+серия)?|` +
	`date\s+of\s+birth|birth\s*date|дата\s+рождения|д\.\s?р\.?|` +
	`телефон|тел\.?|phone|tel\.?|fax|факс)` +
	`\s*[#№]?)\s*(\(?\+?\d+)`)

// labelMatches — подписи строки по порядку: с двоеточием и без него.
func labelMatches(text string) [][]int {
	ms := labelRe.FindAllStringSubmatchIndex(text, -1)
	for _, m := range labelBareRe.FindAllStringSubmatchIndex(text, -1) {
		label := strings.ToLower(text[m[2]:m[3]])
		if strings.TrimSpace(label) == "id" && m[5]-m[4] < 4 {
			continue
		}
		// Номер телефона — от пяти цифр: в «Fax 2 pages» и «кетоновых тел
		// 0,5» за словом стоит не номер.
		if labelKind(label) == KindPhone && phoneDigits(text[m[4]:]) < 5 {
			continue
		}
		inside := false
		for _, o := range ms {
			if m[2] < o[3] && o[2] < m[3] {
				inside = true
				break
			}
		}
		if !inside {
			ms = append(ms, m[:4])
		}
	}
	sort.Slice(ms, func(a, b int) bool { return ms[a][2] < ms[b][2] })
	return ms
}

// phoneDigits — сколько цифр в начале s, пока идут цифры, пробелы, скобки,
// дефисы, точки и плюс.
func phoneDigits(s string) int {
	n := 0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			n++
		case !strings.ContainsRune(" ()-+.", r):
			return n
		}
	}
	return n
}

func byLabels(words []Word, lines []line) {
	for li := range lines {
		l := &lines[li]
		ms := labelMatches(l.text)
		for j, m := range ms {
			k := labelKind(l.text[m[2]:m[3]])
			if k == KindNone {
				continue
			}
			end := len(l.text)
			if j+1 < len(ms) {
				end = ms[j+1][2]
			}
			end = l.valueEnd(words, m[3], end, k)
			n := l.mark(words, m[3], end, k, "подпись поля")
			if n > 0 && k.removed() {
				for _, sp := range l.spans {
					if sp.start >= m[2] && sp.end <= m[3] {
						words[sp.word].LabelOf = k
					}
				}
			}
		}
	}
}

// byCells — таблица и бланк: подпись поля стоит в своей клетке без значения
// («Date of birth», «Full name:»), а значение — в соседней клетке справа на той
// же высоте. tesseract отдаёт клетки отдельными строками, и подпись со
// значением в одну строку не попадает (синтетический набор 03.10.2026:
// «21 June 1988» под подписью «Date of birth» осталось в .md).
const cellGapMax = 250.0 // pt; дальше справа — уже не соседняя клетка

func byCells(words []Word, lines []line) {
	box := func(l line) Rect {
		r := words[l.idx[0]].Box
		for _, i := range l.idx[1:] {
			b := words[i].Box
			r = Rect{min(r.X0, b.X0), min(r.Y0, b.Y0), max(r.X1, b.X1), max(r.Y1, b.Y1)}
		}
		return r
	}
	for i := range lines {
		k := labelKind(lines[i].text)
		if k == KindNone {
			continue
		}
		lb := box(lines[i])
		best, gap := -1, cellGapMax
		for j := range lines {
			if j == i || lines[j].key[0] != lines[i].key[0] {
				continue
			}
			b := box(lines[j])
			cy := (b.Y0 + b.Y1) / 2
			if cy < lb.Y0 || cy > lb.Y1 || b.X0 < lb.X1 {
				continue
			}
			if d := b.X0 - lb.X1; d < gap {
				best, gap = j, d
			}
		}
		// Справа — тоже подпись: это строка заголовков, а не поле.
		if best < 0 || labelKind(lines[best].text) != KindNone {
			continue
		}
		v := &lines[best]
		end := v.valueEnd(words, 0, len(v.text), k)
		// Номер, телефон и дата без единой цифры — не значение, а соседний
		// заголовок: в строке «Service Dept. DOB Provider» дата рождения
		// доставалась слову «Service» (образец 06.10.2026).
		if (k == KindID || k == KindPhone || k == KindBirth) && !strings.ContainsAny(v.text[:end], "0123456789") &&
			!(k == KindBirth && dateWordRe.MatchString(strings.Fields(v.text[:end] + " .")[0])) {
			continue
		}
		if n := v.mark(words, 0, end, k, "подпись в клетке"); n > 0 && k.removed() {
			for _, wi := range lines[i].idx {
				words[wi].LabelOf = k
			}
		}
	}
}

// valueEnd — где кончается значение поля с номером, телефоном или датой
// рождения: на первом слове без цифр после уже взятого. Без этого значение
// тянулось до конца строки, и в «Полис ДМС: ВС-8135007. Услуги оказал
// кардиолог» номером становились «Услуги оказал кардиолог», а повтор разносил
// «кардиолога» по всему документу (синтетический набор, 03.10.2026). Названия
// месяцев и «г.» — часть даты: «12 мая 1969 г.».
func (l *line) valueEnd(words []Word, s, e int, k Kind) int {
	if k != KindID && k != KindPhone && k != KindBirth {
		return e
	}
	n := 0
	for i, sp := range l.spans {
		if sp.start >= e || sp.end <= s {
			continue
		}
		t := words[sp.word].Text
		if n > 0 && !strings.ContainsAny(t, "0123456789") && !(k == KindBirth && dateWordRe.MatchString(t)) &&
			!(k == KindID && idJoinRe.MatchString(t) && i+1 < len(l.spans) && l.spans[i+1].start < e &&
				strings.ContainsAny(words[l.spans[i+1].word].Text, "0123456789")) {
			return sp.start
		}
		n++
	}
	return e
}

// idJoinRe — слово внутри номера: «серия 4508 № 123456». Без него значение
// паспорта кончалось на «№», и сам номер оставался в .md. Слово берётся,
// только если за ним снова цифры: «No» в «12345 No new findings» — уже текст.
var idJoinRe = regexp.MustCompile(`(?i)^(?:№|n|no\.?|#|номер|серия)$`)

// dateWordRe — слово внутри даты: месяц (полностью или сокращением) либо
// «г.», «года». Слово сверяется ЦЕЛИКОМ: по одному началу «mar», «dec», «мар»
// в дату рождения уходили «Marital», «Decreased», «Мария», стоящие следом.
var dateWordRe = regexp.MustCompile(`(?i)^(?:jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|june?|july?|` +
	`aug(?:ust)?|sep(?:t(?:ember)?)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?|` +
	`янв(?:ар[ья])?|фев(?:рал[ья])?|мар(?:та?)?|апр(?:ел[ья])?|ма[йя]|июн[ья]?|июл[ья]?|авг(?:уста?)?|` +
	`сен(?:т(?:ябр[ья])?)?|окт(?:ябр[ья])?|ноя(?:бр[ья])?|дек(?:абр[ья])?|г|года?)[.,]*$`)

// datePat — дата в свободном тексте: 04.03.1990, 1972-04-09, 12 мая 1969 г.,
// 2 February 1978, February 2, 1978.
const datePat = `\d{1,2}[./-]\d{1,2}[./-]\d{2,4}|\d{4}-\d{2}-\d{2}|\d{1,2}\s+\p{L}+\.?\s+\d{4}(?:\s*г\.)?|\p{L}+\s+\d{1,2},?\s+\d{4}`

// Русские имя и отчество: отчество узнаётся по окончанию в любом падеже
// («Георгиевичу», «Львовной»), перед ним имя, перед именем — может быть
// фамилия. Подписи поля в свободном тексте нет, а по отчеству имя видно
// без всякого словаря.
const patronymicPat = `(?:[А-ЯЁ][а-яё]+\s+)?[А-ЯЁ][а-яё]+\s+[А-ЯЁ][а-яё]+(?:(?:ович|евич|ич)(?:а|у|ем|е)?|(?:овн|евн|ичн)(?:а|ы|е|у|ой))`

// Русский адрес: улица (сокращение или слово в любом падеже, перед ним — до
// двух слов с заглавной: «Кленовой улице»), дом, корпус, квартира; город перед
// ним по желанию.
const ruAddressPat = `(?:(?:г\.|город)\s*[А-ЯЁ][\p{L}-]+,?\s*)?(?:[А-ЯЁ][\p{L}-]*\s+){0,2}` +
	`(?:ул\.|улиц\p{L}*|пр-т|просп\.|проспект\p{L}*|пер\.|переул\p{L}*|ш\.|шоссе|б-р|бульвар\p{L}*|` +
	`наб\.|набережн\p{L}*|пл\.|площад\p{L}*|мкр\.?|микрорайон\p{L}*)(?:\s*[\p{L}-]+){0,3},?\s*` +
	`(?:д\.|дом[аеу]?)\s*\d+\p{L}?(?:\s*/\s*\d+)?(?:,?\s*(?:корп\.|корпус|к\.|стр\.|строение)\s*\d+)?` +
	`(?:,?\s*(?:кв\.|квартир\p{L}*|оф\.|офис)\s*\d+)?`

// rule — шаблон; group — какая скобка и есть данные (0 — всё совпадение).
// Скобка нужна там, где граница слова задана руками: \b в RE2 знает только
// латиницу, и для кириллицы граница пишется как [^\p{L}].
type rule struct {
	re    *regexp.Regexp
	kind  Kind
	group int
}

// usStates — коды штатов США: по ним «город ШТАТ индекс» узнаётся и без
// запятой.
const usStates = `(?:AL|AK|AZ|AR|CA|CO|CT|DE|FL|GA|HI|ID|IL|IN|IA|KS|KY|LA|ME|MD|MA|MI|MN|MS|MO|MT|NE|NV|` +
	`NH|NJ|NM|NY|NC|ND|OH|OK|OR|PA|RI|SC|SD|TN|TX|UT|VT|VA|WA|WV|WI|WY|DC)`

var patterns = []rule{
	{regexp.MustCompile(`[\w.+-]+@[\w-]+(?:\.[\w-]+)+`), KindEmail, 0},
	{regexp.MustCompile(`\+\d{1,3}[\s.-]?\(?\d{2,4}\)?[\s.-]?\d{2,4}[\s.-]?\d{2,4}(?:[\s.-]?\d{2,4})?`), KindPhone, 0},
	{regexp.MustCompile(`\(?\b\d{3}\)?[\s.-]*\d{3}[\s.-]\d{4}\b`), KindPhone, 0},
	{regexp.MustCompile(`\b8[\s(-]*\d{3}[\s)-]*\d{3}[\s-]?\d{2}[\s-]?\d{2}\b`), KindPhone, 0},
	{regexp.MustCompile(`\b\d{3}-\d{4}\b`), KindPhone, 0},              // местный номер без кода: 555-0147
	{regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`), KindID, 0},           // SSN
	{regexp.MustCompile(`\b\d{3}-\d{3}-\d{3}[\s-]\d{2}\b`), KindID, 0}, // СНИЛС
	{regexp.MustCompile(`\b[A-Z]{1,6}-?\d{5,}\b`), KindID, 0},
	{regexp.MustCompile(`\b\d{7,}\b`), KindID, 0},
	// Улица: суффикс и заглавными — «12 MAPLE STREET», как в шапке аптеки
	// образца 06.10.2026 (значения выдуманы).
	{regexp.MustCompile(`\b\d{1,6}\s+(?:[A-Z][\w.]*\s+){1,3}(?i:Ln|Lane|St|Street|Ave|Avenue|Rd|Road|` +
		`Blvd|Boulevard|Dr|Drive|Ct|Court|Way|Pl|Place|Pkwy|Parkway|Hwy|Highway|Cir|Circle|Ter|Terrace|` +
		`Trl|Trail|Row|Close|Crescent|Cres|Square|Sq|Mews|Gardens)\b\.?(?:\s*(?i:Ste|Suite|Apt|Unit|#)\s*\w+)?`), KindAddress, 0},
	// Город, штат, индекс — с округом между ними и запятой или без:
	// «Springfield, Example County, NV, 99999», «NORTH SPRINGFIELD NV 99998». Без
	// запятой — только перед настоящим кодом штата.
	{regexp.MustCompile(`\b[A-Z][a-zA-Z]+(?:\s+[A-Z][a-zA-Z]+)*(?:,\s*[A-Z][a-zA-Z]+(?:\s+[A-Z][a-zA-Z]+)*){0,2}` +
		`(?:,\s*[A-Z]{2}|\s+` + usStates + `),?\s+\d{5}(?:-\d{4})?\b`), KindAddress, 0},
	// Фамилия после «Dr», «врача», «доктора» — и заглавными целиком: шапку
	// бланка tesseract читает «доктора ГРОМОВОЙ» (синтетический набор, зерно 2).
	{regexp.MustCompile(`\b(?:Dr\.?|Doctor)\s+[A-Z](?:[a-z]+|[A-Z]+)(?:\s+[A-Z](?:[a-z]+|[A-Z]+))?`), KindDoctor, 0},
	{regexp.MustCompile(`\b[A-Z][a-z]+,?\s+[A-Z][a-z]+(?:\s+[A-Z]\.?)?,?\s+(?:MD|M\.D|DO|PhD|NP|PA-C|RN)\b`), KindDoctor, 0},
	// То же заглавными, с двойным именем: «JANE Q ROE MD», «MEI-LIN ROE, MD» —
	// так врачей печатает факс направления (образец 06.10.2026).
	{regexp.MustCompile(`\b[A-Z]{2,}(?:-[A-Z]{2,})?(?:\s+[A-Z]\.?)?\s+[A-Z]{2,}(?:-[A-Z]{2,})?,?\s+(?:MD|M\.D|DO|NP|PA-C|RN)\b`), KindDoctor, 0},
	{regexp.MustCompile(`\b(?:Mr|Mrs|Ms|Miss)\.?\s+[A-Z][a-z]+(?:\s+[A-Z][a-z]+)?`), KindClient, 0},
	// Группа — только фамилия с инициалами. Пока в неё входило и само слово
	// «врача»/«доктора», оно помечалось как имя врача, разносилось по
	// документу повтором (в skipWords лишь именительный падеж) — и каждое
	// «врача» в тексте закрашивалось.
	{regexp.MustCompile(`(?:^|[^\p{L}])(?:[Вв]рач|[Дд]октор|ВРАЧ|ДОКТОР)(?:а|у|ом|е|А|У|ОМ|Е)?\s+([А-ЯЁ](?:[а-яё]+|[А-ЯЁ]+)(?:\s+[А-ЯЁ]\.\s?[А-ЯЁ]\.)?)`), KindDoctor, 1},
	{regexp.MustCompile(`(?:^|[^\p{L}])(` + patronymicPat + `)(?:[^\p{L}]|$)`), KindPerson, 1},
	{regexp.MustCompile(`(?:^|[^\p{L}])(` + ruAddressPat + `)`), KindAddress, 1},
	{regexp.MustCompile(`(?i)\bborn(?:\s+on)?\s+(` + datePat + `)`), KindBirth, 1},
	{regexp.MustCompile(`(?i)(?:^|[^\p{L}])(?:родил(?:ся|ась)|д\.\s?р\.)\s*[:.]?\s*(` + datePat + `)`), KindBirth, 1},
	// Скрывается только год: «года рождения» — обычные слова, и проверка
	// повторным распознаванием находила бы их в шапке .md как утечку.
	{regexp.MustCompile(`(?:^|[^\p{L}])(\d{4})\s+(?:года|г\.)\s+рождения`), KindBirth, 1},
	// «1970 г.р.» — год стоит перед сокращением, подписи перед ним нет.
	// После «р» — точка или конец слова: «2015 г. р-н» — уже район.
	{regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(\d{4})\s*г\.\s?р(?:\.|[\s,;)]|$)`), KindBirth, 1},
	// Серия и номер паспорта без слова «паспорт» — оно часто стоит строкой
	// выше: «серия 45 08 № 123456» (значения выдуманы).
	{regexp.MustCompile(`(?i)(?:^|[^\p{L}])серия\s*(\d{2}\s?\d{2}\s*(?:№|n|номер)\s*\d{6})(?:\D|$)`), KindID, 1},
	{regexp.MustCompile(`(?:^|[^\p{L}])([А-ЯЁ][а-яё]+\s+[А-ЯЁ]\.\s?[А-ЯЁ]\.)`), KindPerson, 1},
	{regexp.MustCompile(`(?:^|[^\p{L}])([А-ЯЁ]\.\s?[А-ЯЁ]\.\s?[А-ЯЁ][а-яё]+)`), KindPerson, 1},
}

// Название организации: слово-признак и до трёх слов с заглавной перед ним
// («NORTHWIND RADIOLOGY», «Contoso Medical Center», «Клиника Здоровье»). Само
// по себе название не имя, но по клинике и дате выходят на врача, а врач
// по просьбе пользователей — такое же персональное, как клиент.
//
// После признака обязана стоять граница слова: без неё «clinic» срабатывал
// на «Clinical indication», слово уходило в образцы повтора, и каждое
// «clinical» пропадало из документа.
var orgRe = regexp.MustCompile(`(?:^|[^\p{L}])((?:\p{Lu}[\p{L}'&.-]*\s+){0,3}` +
	`(?i:radiology|imaging|clinic|hospital|medical\s+(?:center|centre|group)|health\s*care|healthcare|practice|` +
	// «Northwind Cancer Center», «… CARE OBGYN OFFICE», страховая «… HEALTHPLAN»
	// (образец 06.10.2026; названия здесь выдуманы по виду).
	`(?:cancer|oncology|surgical|surgery|wellness|women'?s|care)\s+(?:center|centre)|ob-?gyn|ob/gyn|health\s*plan|` +
	`laborator(?:y|ies)|diagnostics?|клиника|больница|поликлиника|госпиталь|медицинский\s+центр|` +
	`лаборатория|диагностический\s+центр|кабинет)(?:\s+\p{Lu}[\p{L}'&.-]*){0,2})(?:[^\p{L}]|$)`)

func byPatterns(words []Word, lines []line) {
	for li := range lines {
		l := &lines[li]
		for _, r := range patterns {
			for _, m := range r.re.FindAllStringSubmatchIndex(l.text, -1) {
				l.mark(words, m[2*r.group], m[2*r.group+1], r.kind, "шаблон")
			}
		}
		// Заголовок раздела («### RADIOLOGY») — не название: в нём нет
		// ничего, кроме признака, и строка кончается двоеточием.
		for _, m := range orgRe.FindAllStringSubmatchIndex(l.text, -1) {
			if !strings.HasSuffix(strings.TrimSpace(l.text), ":") {
				l.mark(words, m[2], m[3], KindOrg, "шаблон")
			}
		}
	}
}

// byNameDate — дата сразу после имени через запятую: «выдана гражданину
// Сидорову Ивану Кузьмичу, 01.02.1970». Подписи у неё нет, а модель её не
// замечает (прогон Qwen 03.10.2026, синтетический набор: единственная
// оставшаяся утечка). Дата визита или исследования стоит так же, поэтому
// датой рождения считается только дата не позже чем за nameDateMinAge лет
// до текущего года.
const nameDateMinAge = 2

var nameDateRe = regexp.MustCompile(`^(?:\d{1,2}[./-]\d{1,2}[./-](\d{4})|(\d{4})-\d{2}-\d{2})[,.;]?$`)

// ageSexRe — слово возраста или пола между именем и датой рождения:
// «29», «Y,», «(57yo,», «F)», «лет,», «Ж». Больше nameDateSkip таких слов
// подряд не пропускается: дальше уже не шапка, а текст.
var ageSexRe = regexp.MustCompile(`(?i)^\(?(?:\d{1,3}\s*(?:y|yo|yr|yrs|years?|г|лет|года?)?|` +
	`y|yo|yr|yrs|years?|f|m|male|female|лет|года?|ж|м|жен|муж)` +
	// возраст и пол одним словом: «29Y,F», «(57yo,F)»
	`(?:[,/]\s*(?:f|m|ж|м))?[,.;)]*$`)

const nameDateSkip = 4

func byNameDate(words []Word, lines []line, year int) {
	for _, l := range lines {
		for n := 1; n < len(l.idx); n++ {
			prev := &words[l.idx[n-1]]
			if !prev.Kind.person() {
				continue
			}
			// Между именем и датой бывают возраст и пол: «Doe, Jane, 29 Y, F
			// 02/03/1997» — вид шапки лабораторного бланка в образце 06.10.2026.
			j := n
			for j < len(l.idx) && j-n < nameDateSkip && words[l.idx[j]].Kind == KindNone &&
				ageSexRe.MatchString(words[l.idx[j]].Text) {
				j++
			}
			if j >= len(l.idx) {
				continue
			}
			w := &words[l.idx[j]]
			if w.Kind != KindNone || (j == n && !strings.HasSuffix(prev.Text, ",")) {
				continue
			}
			m := nameDateRe.FindStringSubmatch(w.Text)
			if m == nil {
				continue
			}
			y := m[1] + m[2]
			if v, ok := atoi(y); ok && v <= year-nameDateMinAge {
				w.Kind, w.Why = KindBirth, "дата после имени"
			}
		}
	}
}

// byHints помечает то, что назвала модель: имена — всюду, где встречаются
// их слова (через byRepeat), прочие строки — там, где стоят целиком.
func byHints(words []Word, lines []line, opt Options) map[string]Kind {
	seeds := map[string]Kind{}
	phrase := func(s string, k Kind) {
		f := strings.Fields(s)
		if len(f) == 0 {
			return
		}
		for i := range f {
			f[i] = regexp.QuoteMeta(f[i])
		}
		re, err := regexp.Compile(`(?i)` + strings.Join(f, `\s+`))
		if err != nil {
			return
		}
		for li := range lines {
			for _, m := range re.FindAllStringIndex(lines[li].text, -1) {
				lines[li].mark(words, m[0], m[1], k, "подсказка")
			}
		}
	}
	for _, set := range []struct {
		list []string
		kind Kind
	}{{opt.Clients, KindClient}, {opt.Doctors, KindDoctor}} {
		for _, name := range set.list {
			phrase(name, set.kind)
			for _, tok := range strings.Fields(name) {
				if n := norm(tok); len([]rune(n)) >= 2 && !skipWords[n] {
					seeds[n] = set.kind
				}
			}
		}
	}
	for _, s := range opt.Hide {
		phrase(s, KindOther)
	}
	return seeds
}

// skipWords не разносятся по документу, даже если попали в имя или адрес:
// служебные слова, подписи полей, типы улиц.
var skipWords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields("MD DR DO THE AND NEW UPDATE MR MRS MS MISS PHD RN NP " +
		"LN LANE ST STREET AVE AVENUE RD ROAD BLVD BOULEVARD DRIVE CT COURT WAY PL PLACE " +
		"PKWY PARKWAY HWY HIGHWAY CIR CIRCLE TER TERRACE TRL TRAIL ROW CLOSE CRESCENT CRES SQUARE SQ MEWS GARDENS STE SUITE APT UNIT " +
		"УЛ УЛИЦА Г ГОРОД ДОМ КВ КОРП ВРАЧ ДОКТОР " +
		// Признаки организаций: разносится название («NORTHWIND»), а не
		// «radiology» — иначе пропало бы каждое такое слово в тексте.
		"RADIOLOGY IMAGING CLINIC HOSPITAL MEDICAL CENTER CENTRE GROUP HEALTH HEALTHCARE " +
		"LABORATORY LABORATORIES DIAGNOSTIC DIAGNOSTICS КЛИНИКА БОЛЬНИЦА ПОЛИКЛИНИКА " +
		"ГОСПИТАЛЬ МЕДИЦИНСКИЙ ЦЕНТР ЛАБОРАТОРИЯ ДИАГНОСТИЧЕСКИЙ КАБИНЕТ") {
		m[w] = true
	}
	for _, w := range regexp.MustCompile(`[\p{L}-]+`).FindAllString(labelVocab, -1) {
		m[norm(w)] = true
	}
	return m
}()

// norm приводит слово к виду для сравнения: только буквы и цифры, заглавные.
// «@» вместо нуля — ошибка распознавания моноширинного шрифта («RM2@48855»).
func norm(t string) string {
	var b strings.Builder
	for _, r := range strings.ReplaceAll(t, "@", "0") {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToUpper(r))
		}
	}
	return b.String()
}

func allDigits(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return s != ""
}

// byRepeat разносит найденное по всему документу: имя из шапки первого листа
// закрывается и в строке над текстом второго, где у него нет подписи поля.
// Название клиники, совпавшее со словом её адреса, — так же.
func byRepeat(words []Word, extra map[string]Kind) {
	seeds := map[string]Kind{}
	for k, v := range extra {
		seeds[k] = v
	}
	for _, w := range words {
		n := norm(w.Text)
		size := len([]rune(n))
		if n == "" || skipWords[n] || allDigits(n) {
			continue
		}
		switch {
		case (w.Kind.person() || w.Kind == KindID) && size >= 3,
			(w.Kind == KindAddress || w.Kind == KindOther || w.Kind == KindOrg) && size >= 4 && !strings.ContainsAny(n, "0123456789"):
			if _, dup := seeds[n]; !dup {
				seeds[n] = w.Kind
			}
		}
	}
	// Дата рождения, найденная по подписи, повторяется в шапке каждого листа
	// уже без подписи (образец 06.10.2026: «…, 29Y,F <дата>»). После norm
	// она — одни цифры, и проверка выше её пропускает.
	for _, w := range words {
		if n := norm(w.Text); w.Kind == KindBirth && len(n) >= 6 && allDigits(n) {
			seeds[n] = KindBirth
		}
	}
	keys := make([]string, 0, len(seeds))
	for k := range seeds {
		keys = append(keys, k)
	}
	sort.Strings(keys) // ближайшее по правке ищется в одном порядке от прогона к прогону
	for i := range words {
		w := &words[i]
		if w.Kind != KindNone {
			continue
		}
		n := norm(w.Text)
		size := len([]rune(n))
		if size < 3 || skipWords[n] {
			continue
		}
		if k, ok := seeds[n]; ok {
			w.Kind, w.Why = k, "повтор"
			continue
		}
		if size < 5 {
			continue
		}
		for _, s := range keys {
			// С ошибкой в букву повторяются только имена и номера. Слова
			// адреса и названия — обычные слова словаря: улица «Dancer»
			// с одной ошибкой — аллерген «Dander» в списке аллергий (образец
			// 06.10.2026; слово закрашивалось, а проверка поднимала тревогу).
			// Дата рождения — тоже: прочитанная как «02/\63/19xx» вместо
			// «02/03/19xx» (вид из образца, цифры выдуманы) на одном листе
			// прошла мимо и правил, и встроенной проверки, а
			// отдельное распознавание замазанного PDF прочло её чисто.
			if k := seeds[s]; (k.person() || k == KindID || k == KindBirth) && len([]rune(s)) >= 5 && near(n, s) {
				w.Kind, w.Why = k, "повтор"
				break
			}
		}
	}
	byFragments(words, seeds, keys)

	// Номер дома перед словом адреса, найденным повтором: «- 1234 Maple,»
	// без «Ave» шаблон улицы не узнаёт, а повтор чисел не разносит (образец
	// 06.10.2026, адрес лаборатории в подвале листов 7–11).
	for i := 0; i+1 < len(words); i++ {
		w, next := &words[i], words[i+1]
		n := norm(w.Text)
		if w.Kind == KindNone && next.Kind == KindAddress && allDigits(n) && len(n) <= 6 &&
			w.Page == next.Page && w.Block == next.Block && w.Par == next.Par && w.Line == next.Line {
			w.Kind, w.Why = KindAddress, "номер дома"
		}
	}
}

// Обрывки имени. Крупный кегль tesseract режет на куски: «Ex amp l e,
// An nab eln,29Y,F» вместо «Example, Annabel, 29Y,F» (вид — как в шапке
// бланка образца 06.10.2026, листы 6–11, имя выдумано; настоящее оставалось
// в PDF). Слово за словом такое имя
// не узнаётся, поэтому сравнивается склейка соседних обрывков одной строки:
// совпала с найденным именем или начинается с него (одна ошибка допускается,
// хвост — мусор вроде возраста) — закрываются все её обрывки. Чтобы не
// склеивать обычные слова, среди обрывков должен быть хотя бы один из одной
// или двух букв, а промежутки — уже fragGapK высоты слова.
const (
	fragMax   = 5
	fragGapK  = 0.6
	fragShort = 2
)

func byFragments(words []Word, seeds map[string]Kind, keys []string) {
	sameLine := func(a, b Word) bool {
		return a.Page == b.Page && a.Block == b.Block && a.Par == b.Par && a.Line == b.Line
	}
	for i := 0; i < len(words); i++ {
		joined, short := "", false
		for j := i; j < len(words) && j-i < fragMax; j++ {
			w := words[j]
			if j > i {
				p := words[j-1]
				if !sameLine(p, w) || w.Box.X0-p.Box.X1 > fragGapK*max(w.Box.Y1-w.Box.Y0, p.Box.Y1-p.Box.Y0) {
					break
				}
			}
			n := norm(w.Text)
			before := len([]rune(joined)) // букв до последнего обрывка
			joined += n
			if l := len([]rune(n)); l > 0 && l <= fragShort {
				short = true
			}
			if j == i || !short {
				continue
			}
			jr := []rune(joined)
			hitAt := false
			for _, s := range keys {
				k := seeds[s]
				sr := []rune(s)
				if !k.person() || len(sr) < 3 {
					continue
				}
				// Короткое имя — только точной склейкой: с ошибкой в одну
				// букву на трёх-четырёх буквах совпадёт что угодно.
				hit := joined == s
				if len(sr) >= 5 {
					hit = hit || near(joined, s)
					// Склейка длиннее имени — сравнивается её начало, но только
					// если имя кончается внутри последнего обрывка: его хвост —
					// возраст, пол, запятая. Иначе к имени «Royce» в «Royce
					// MD 11/12/2026» пристраивались бы и подпись, и дата.
					for _, cut := range []int{len(sr), len(sr) + 1} {
						if !hit && len(jr) > cut && before < len(sr) && near(string(jr[:cut]), s) {
							hit = true
						}
					}
				}
				if !hit {
					continue
				}
				for m := i; m <= j; m++ {
					if words[m].Kind == KindNone {
						words[m].Kind, words[m].Why = k, "повтор обрывками"
					}
				}
				hitAt = true
				break
			}
			if hitAt {
				break
			}
		}
	}
}

// near — расстояние правки между словами не больше единицы: распознавание
// путает букву-другую («SMIRNOVA» и «SMIRN0VA»).
func near(a, b string) bool {
	x, y := []rune(a), []rune(b)
	if len(x) > len(y) {
		x, y = y, x
	}
	if len(y)-len(x) > 1 {
		return false
	}
	i := 0
	for i < len(x) && x[i] == y[i] {
		i++
	}
	if i == len(x) {
		return true
	}
	if len(x) == len(y) {
		return string(x[i+1:]) == string(y[i+1:])
	}
	return string(x[i:]) == string(y[i+1:])
}

// Неразборчивое: строка, где tesseract в среднем не уверен, — это почерк
// или мусор, и прочитанное в ней может оказаться чьими-то инициалами.
const (
	unreadableLineConf = 45
	unreadableWordConf = 25
)

func byUnreadable(words []Word, lines []line) {
	for _, l := range lines {
		sum := 0.0
		for _, i := range l.idx {
			sum += words[i].Conf
		}
		bad := len(l.idx) >= 2 && sum/float64(len(l.idx)) < unreadableLineConf
		for _, i := range l.idx {
			w := &words[i]
			if w.Kind != KindNone {
				continue
			}
			if bad || (w.Conf < unreadableWordConf && len([]rune(norm(w.Text))) >= 3) {
				w.Kind, w.Why = KindHidden, "неразборчиво"
			}
		}
	}
}
