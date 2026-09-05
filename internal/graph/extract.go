package graph

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Извлечение сущностей и связей из куска книги.
//
// Модель читает кусок и отвечает строгим JSON: какие понятия в нём названы
// и как они связаны. Всё остальное в этом файле — защита от того, что модель
// ответит не так, как просили, а она ответит: то обернёт JSON в пояснения,
// то придумает связь между понятиями, которых в куске нет, то выдаст
// полстраницы текста вместо имени сущности.
//
// Главное правило разбора: **сомнительное отбрасывается молча**. Граф, в котором
// половина связей выдумана, хуже отсутствующего графа — по нему нельзя принять
// ни одного решения, а выглядит он убедительно.

// Extractor — то, что умеет спросить модель.
//
// Интерфейс на голых типах, как у kb.Embedder, и по той же причине: пакет
// не должен зависеть от клиента Ollama, иначе граф окажется привязан к одному
// способу разговаривать с моделью.
type Extractor interface {
	Extract(ctx context.Context, system, user string) (string, error)
	Model() string
}

// Пределы разбора. Числа не с потолка: они отсекают то, что модели выдают
// на деле, — простыни вместо имён и списки из полусотни «сущностей», где
// половина это общие слова.
const (
	maxNameRunes         = 80 // имя длиннее — это фраза, а не понятие
	minNameRunes         = 2  // «а», «и» сущностями не бывают
	maxEntitiesPerChunk  = 16
	maxRelationsPerChunk = 24
)

// FactEntity — сущность, названная моделью.
type FactEntity struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Aliases []string `json:"aliases,omitempty"`
}

// FactRelation — связь, названная моделью.
type FactRelation struct {
	Src  string `json:"src"`
	Dst  string `json:"dst"`
	Type string `json:"type"`
	Note string `json:"note,omitempty"`
}

// Facts — что модель вытащила из куска.
type Facts struct {
	Entities  []FactEntity   `json:"entities"`
	Relations []FactRelation `json:"relations"`
}

// SystemPrompt — постановка задачи модели.
//
// Три требования в нём не украшения, а следствия того, как ломается результат:
// закрытый список типов — иначе каждая книга заводит свои; только явно
// названное — иначе модель пересказывает свои знания, а не книгу; связи только
// между перечисленными сущностями — иначе появляются связи с понятиями,
// которых в куске нет и проверить их нечем.
//
//go:embed prompts/extract.txt
var SystemPrompt string

// SystemPromptV2 — промпт извлечения графа формата 2: те же правила, но
// правило о синонимах ужесточено примерами настоящих провалов 03.09.2026
// (GraphSchemaV2.md, п. 4). Форма ответа та же.
//
//go:embed prompts/extract2.txt
var SystemPromptV2 string

// UserPrompt собирает вопрос по куску: откуда он и что в нём написано.
//
// Заголовок книги обязателен: без него «он», «эта библиотека» и «данный
// подход» не к чему привязать, и модель начинает угадывать.
func UserPrompt(book string, unit string, from, to int, text string) string {
	var b strings.Builder
	b.WriteString("Книга: ")
	if book == "" {
		b.WriteString("без названия")
	} else {
		b.WriteString(book)
	}
	if from > 0 {
		fmt.Fprintf(&b, "\n%s %d", unit, from)
		if to > from {
			fmt.Fprintf(&b, "–%d", to)
		}
	}
	b.WriteString("\n\nФрагмент:\n")
	b.WriteString(text)
	return b.String()
}

// ParseFacts разбирает ответ модели.
//
// Ответ приходит по-разному: голый JSON, JSON в ограде ```json, JSON после
// пары вводных предложений. Разбирается всё это одинаково — берётся первый
// сбалансированный объект. Если его нет, это ошибка, и кусок пойдёт в пропуск.
func ParseFacts(answer, chunkText string) (Facts, error) {
	raw := firstJSONObject(answer)
	if raw == "" {
		// Ответ мог оборваться на середине: модель упёрлась в потолок длины.
		// Замерено 23.08.2026 на живой сборке: так пропадал каждый пятый кусок,
		// причём почти всегда потому, что модель зацикливалась и до самого
		// потолка повторяла одно понятие. Начало ответа при этом целое
		// и годное — его и берём.
		raw = repairJSON(answer)
	}
	if raw == "" {
		return Facts{}, fmt.Errorf("в ответе нет объекта JSON")
	}
	var f Facts
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		if fixed := repairJSON(answer); fixed != "" && fixed != raw {
			if err2 := json.Unmarshal([]byte(fixed), &f); err2 == nil {
				return clean(f, chunkText), nil
			}
		}
		return Facts{}, fmt.Errorf("разбор JSON: %w", err)
	}
	return clean(f, chunkText), nil
}

// repairJSON чинит оборванный ответ: отрезает хвост по последний целый элемент
// массива и достраивает закрывающие скобки.
//
// Половина объекта — не мусор: в ней уже перечислены понятия, ради которых
// кусок и разбирался. Выбрасывать её целиком значит терять работу модели
// из-за того, что она не вовремя замолчала.
func repairJSON(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	var stack []byte
	var lastGood int
	var lastStack []byte
	var inString, escaped bool

	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case escaped:
			escaped = false
		case c == '\\' && inString:
			escaped = true
		case c == '"':
			inString = !inString
		case inString:
			// внутри строки скобки не считаются
		case c == '{' || c == '[':
			stack = append(stack, c)
		case c == '}' || c == ']':
			if len(stack) == 0 {
				return ""
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return s[start : i+1] // ответ оказался целым
			}
			if c == '}' && stack[len(stack)-1] == '[' {
				lastGood = i + 1
				lastStack = append(lastStack[:0], stack...)
			}
		}
	}
	if lastGood == 0 || len(lastStack) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(s[start:lastGood])
	for i := len(lastStack) - 1; i >= 0; i-- {
		if lastStack[i] == '[' {
			b.WriteByte(']')
		} else {
			b.WriteByte('}')
		}
	}
	return b.String()
}

// Clean отбраковывает то, что нельзя класть в граф.
//
// Возвращает только сущности с годными именами и только те связи, оба конца
// которых есть среди этих сущностей. Связь с концом «в никуда» проверить
// нечем, а значит, и хранить её незачем.
// Clean без текста куска — для мест, где текста нет (тесты, формат 1).
// Проверку синонимов на буквальное присутствие она не делает.
// collapseSpaces сводит подряд идущие пробелы и переносы к одному пробелу:
// в куске слова синонима могут быть разделены переносом строки, а в синониме —
// одним пробелом, и без этого совпадение терялось бы.
func collapseSpaces(s string) string { return strings.Join(strings.Fields(s), " ") }

// containsWord — встречается ли фраза a в h так, что слева и справа от неё
// не буква и не цифра (или край строки). Знаки ВНУТРИ фразы (точки, дефисы,
// скобки в «net.Dial», «KV-кэш») не важны — важны только границы, чтобы «go»
// не совпало внутри «goroutine», а «внешний источник» в кавычках-ёлочках —
// совпало.
func containsWord(h, a string) bool {
	if a == "" {
		return false
	}
	for from := 0; ; {
		i := strings.Index(h[from:], a)
		if i < 0 {
			return false
		}
		i += from
		if boundaryBefore(h, i) && boundaryAfter(h, i+len(a)) {
			return true
		}
		from = i + 1
	}
}

// boundaryBefore — перед позицией pos граница слова: край строки или не-буква.
func boundaryBefore(h string, pos int) bool {
	if pos <= 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(h[:pos])
	return !isWordRune(r)
}

// boundaryAfter — после позиции pos граница слова.
func boundaryAfter(h string, pos int) bool {
	if pos >= len(h) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(h[pos:])
	return !isWordRune(r)
}

// isWordRune — руна принадлежит букве или цифре. Именно руна, а не байт:
// русская пунктуация («ёлочки», тире) многобайтна и по первому байту
// неотличима от буквы, а по руне — отличима.
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func Clean(f Facts) Facts { return clean(f, "") }

// clean отбирает годные сущности и связи и проверяет синонимы по тексту куска.
//
// **Проверка синонима на присутствие (формат 2, замер 03.09.2026).** Модель
// приписывает понятию синонимы, которых в куске нет: «Knowledge base ← внешний
// источник», «ИИ ← ChatGPT» — 20.1% синонимов оказались чужими именами или
// выдумкой. Книга прямо советует сверять именованные сущности с исходным
// текстом строкой, а не моделью: «factuality verification can be performed by
// using string matches» («Designing Large Language Model Applications»,
// Suhas Pai, 2025, стр. 276).
//
// Проверяется ФРАЗА ЦЕЛИКОМ, а не по словам: «Practical LLM Evaluation» (2026,
// стр. 362) предупреждает, что токенная сверка выглядит сильной даже когда
// сущность вытащена не целиком. «Внешний источник» должно стоять в тексте
// именно так, а не «внешний» в одном месте и «источник» в другом.
//
// Пустой chunkText отключает проверку — для формата 1 и тестов без текста.
func clean(f Facts, chunkText string) Facts {
	haystack := strings.ToLower(collapseSpaces(chunkText))
	inText := func(alias string) bool {
		if chunkText == "" {
			return true // текста нет — проверять нечем, оставляем как было
		}
		// Фраза целиком, но по ГРАНИЦЕ СЛОВА, а не по ведущему пробелу:
		// в тексте синоним часто в скобках или кавычках — «(KV cache)»,
		// и требовать пробел перед ним значит терять верные совпадения.
		// А граница нужна, чтобы «go» не совпало внутри «goroutine».
		a := strings.ToLower(collapseSpaces(alias))
		return containsWord(haystack, a)
	}

	var out Facts
	known := map[string]string{} // нормализованное имя или синоним → каноническое

	for _, e := range f.Entities {
		name := strings.TrimSpace(e.Name)
		if !goodName(name) {
			continue
		}
		norm := Normalize(name)
		if _, dup := known[norm]; dup {
			continue
		}
		ent := FactEntity{Name: name, Type: NormalizeType(e.Type)}
		for _, a := range e.Aliases {
			a = strings.TrimSpace(a)
			if !goodName(a) || Normalize(a) == norm {
				continue
			}
			// Синоним, которого нет в тексте куска, — выдумка модели. Своё имя
			// в тексте есть (иначе кусок бы его не породил), а вот приписанное
			// «одно и то же» проверяется здесь.
			if !inText(a) {
				continue
			}
			ent.Aliases = append(ent.Aliases, a)
			known[Normalize(a)] = name
		}
		known[norm] = name
		out.Entities = append(out.Entities, ent)
		if len(out.Entities) >= maxEntitiesPerChunk {
			break
		}
	}

	for _, r := range f.Relations {
		src, okSrc := known[Normalize(r.Src)]
		dst, okDst := known[Normalize(r.Dst)]
		if !okSrc || !okDst || src == dst {
			continue
		}
		out.Relations = append(out.Relations, FactRelation{
			Src: src, Dst: dst, Type: strings.TrimSpace(r.Type), Note: strings.TrimSpace(r.Note),
		})
		if len(out.Relations) >= maxRelationsPerChunk {
			break
		}
	}
	return out
}

// goodName проверяет, годится ли строка в имя сущности.
func goodName(s string) bool {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) < minNameRunes || len(r) > maxNameRunes {
		return false
	}
	// Имя из одних цифр и знаков — это номер страницы или ссылка на раздел.
	var letters int
	for _, c := range r {
		if unicode.IsLetter(c) {
			letters++
		}
	}
	if letters == 0 {
		return false
	}
	// Целое предложение — не имя понятия. Пять слов уже перебор даже
	// для длинных названий стандартов.
	if len(strings.Fields(s)) > 5 {
		return false
	}
	// Строки с переводами и кавычками-ёлочками внутри — обычно кусок текста,
	// который модель приняла за название.
	if strings.ContainsAny(s, "\n\r\t") {
		return false
	}
	return !stopName(Normalize(s))
}

// stopName — слова, которые сущностями не бывают. Модель выдаёт их постоянно:
// они часто встречаются в тексте и выглядят важными.
var stopWords = map[string]bool{
	"система": true, "данные": true, "пример": true, "глава": true,
	"раздел": true, "книга": true, "автор": true, "рисунок": true,
	"таблица": true, "листинг": true, "код": true, "текст": true,
	"метод": true, "способ": true, "задача": true, "решение": true,
	"проблема": true, "вопрос": true, "ответ": true, "часть": true,
	"system": true, "data": true, "example": true, "chapter": true,
	"section": true, "book": true, "figure": true, "table": true,
	"listing": true, "code": true, "text": true, "method": true,
	"problem": true, "solution": true, "question": true, "answer": true,
}

func stopName(norm string) bool { return stopWords[norm] }

// firstJSONObject вырезает первый сбалансированный объект JSON.
//
// Считает скобки, пропуская их внутри строк и после обратной косой черты:
// иначе `{"note":"скобка } внутри"}` обрезается посередине.
func firstJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	var depth int
	var inString, escaped bool
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case escaped:
			escaped = false
		case c == '\\' && inString:
			escaped = true
		case c == '"':
			inString = !inString
		case inString:
			// внутри строки скобки не считаются
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}
