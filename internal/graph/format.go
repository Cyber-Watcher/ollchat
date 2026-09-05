package graph

import (
	"fmt"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Как выглядит выдача поиска по графу.
//
// Один вид на всех: и модели в ollchat, и человеку в командной строке, и любому
// клиенту службы MCP. Разводить два вида — значит однажды показать человеку
// одно, а модели другое, и потом полдня искать, почему они расходятся.
//
// Порядок частей выбран так, чтобы ответ можно было проверить сверху вниз:
// сперва понятия (о чём вообще речь), затем связи (как это соотносится),
// затем цитаты с книгой и страницей (где это написано). Цитаты последние
// намеренно: они длинные, и без карты понятий над ними в них тонешь.

// Chunks — откуда берутся тексты кусков. Коллекция базы знаний ему удовлетворяет.
type Chunks interface {
	ChunkByRef(doc, ord uint32) (kb.ChunkInfo, bool)
}

// RenderOpts — как печатать.
type RenderOpts struct {
	// Collection — имя коллекции для ссылок вида «books/12#37».
	Collection string
	// SnippetRunes — сколько знаков цитаты показывать. 0 — 400.
	SnippetRunes int
	// MaxRelations — сколько связей печатать. 0 — 12.
	MaxRelations int

	// ForModel — карточка уходит модели, а не человеку.
	//
	// **Что с синонимами.** Синоним, который является собственным именем другого
	// понятия, — не «он же»: среди таких есть верные переводы («WaitGroup ←
	// sync.WaitGroup»), а есть прямая ложь («ИИ ← ChatGPT»). Замер 03.09.2026
	// на эталоне: правило ловит 16 выдумок из 32 и задевает 11 верных переводов
	// из 65 (aliases_sample.tsv, разбор в GraphHealth.md).
	// До 04.09.2026 модели такие синонимы не показывались вовсе, человеку шли
	// подряд с остальными. Теперь (этап 89, А1) обоим читателям они показываются
	// **отдельной строкой с оговоркой** — «близкие понятия графа, не то же самое»:
	// подсказка остаётся, подмены нет. В вектор понятия и в расширение запроса
	// они по-прежнему не идут (Entities.SafeAliases).
	ForModel bool

	// RelationRunes — сколько знаков выдержки показывать под связью.
	// 0 — 140, отрицательное — не показывать вовсе.
	//
	// **Зачем выдержка под связью.** Строка «горутина —использует→ канал
	// (подтверждений 48)» говорит, ЧТО связано, но не говорит, ЧЕМ именно.
	// Модель извлечения возвращает пояснение к связи, но мы его не храним,
	// и добрать задним числом нельзя: это повторный прогон по всем разобранным
	// кускам, около 110 часов карты, да ещё и с перекосом между каталогами
	// (см. план, «что следовало бы сделать в самом начале»).
	//
	// Зато кусок-подтверждение у каждой связи уже есть — и в нём та самая
	// фраза из книги, которую пояснение бы пересказало. Показать её стоит
	// одного обращения к хранилищу и работает на всём графе сразу, включая
	// разобранное год назад.
	RelationRunes int
}

func (o RenderOpts) norm() RenderOpts {
	if o.SnippetRunes <= 0 {
		o.SnippetRunes = 400
	}
	if o.MaxRelations <= 0 {
		o.MaxRelations = 12
	}
	if o.RelationRunes == 0 {
		o.RelationRunes = 140
	}
	return o
}

// Render печатает выдачу поиска по графу.
// aliasesFor — что показывать под именем понятия: синонимы («он же») и
// отдельно близкие понятия — синонимы, совпавшие с собственным именем другого
// узла графа. Список один для человека и модели: расхождение карточек
// означало бы, что человек и модель говорят о разных графах.
func aliasesFor(e FoundEntity, _ RenderOpts) (same, near []string) {
	safe := map[string]bool{}
	for _, a := range e.AliasesSafe {
		safe[a] = true
	}
	for _, a := range e.Aliases {
		if !safe[a] {
			near = append(near, a)
		}
	}
	return e.AliasesSafe, near
}

// nearNote — строка о близких понятиях. Оговорка «не то же самое» обязательна:
// без неё модель прочитает список как тождество и станет отвечать про ChatGPT
// на вопрос про ИИ.
func nearNote(near []string) string {
	return "близкие понятия графа (не то же самое): " + strings.Join(near, ", ")
}

func Render(src Chunks, res SearchResult, opt RenderOpts) string {
	opt = opt.norm()
	var b strings.Builder

	if len(res.Entities) == 0 {
		if res.Note != "" {
			return res.Note
		}
		return "по этому вопросу в графе ничего не нашлось"
	}

	b.WriteString("Понятия по вопросу:\n")
	for _, e := range res.Entities {
		fmt.Fprintf(&b, "  %s (%s) — упоминаний %d в %s\n",
			e.Name, e.Type, e.Mentions, plural(e.Books, "книге", "книгах", "книгах"))
		same, near := aliasesFor(e, opt)
		if len(same) > 0 {
			fmt.Fprintf(&b, "      он же: %s\n", strings.Join(same, ", "))
		}
		if len(near) > 0 {
			fmt.Fprintf(&b, "      %s\n", nearNote(near))
		}
	}

	if len(res.Relations) > 0 {
		b.WriteString("\nСвязи:\n")
		for i, r := range res.Relations {
			if i >= opt.MaxRelations {
				fmt.Fprintf(&b, "  … ещё %d связей\n", len(res.Relations)-i)
				break
			}
			fmt.Fprintf(&b, "  %s —%s→ %s (подтверждений %d)\n", r.Src, r.Type, r.Dst, r.Count)
			if opt.RelationRunes > 0 && src != nil {
				if q := evidenceLine(src, r, opt.RelationRunes); q != "" {
					fmt.Fprintf(&b, "      %s\n", q)
				}
			}
		}
	}

	if len(res.Chunks) > 0 && src != nil {
		b.WriteString("\nПодтверждения из книг:\n")
		var n int
		for _, k := range res.Chunks {
			info, ok := src.ChunkByRef(k.Doc, k.Ord)
			if !ok {
				continue
			}
			n++
			fmt.Fprintf(&b, "\n[%d] %s", n, bookLine(info))
			if opt.Collection != "" {
				fmt.Fprintf(&b, " · id=%s/%d#%d", opt.Collection, k.Doc, k.Ord)
			}
			b.WriteString("\n")
			b.WriteString(indent(cut(info.Text, opt.SnippetRunes), "    "))
			b.WriteString("\n")
		}
	}

	if res.Note != "" {
		fmt.Fprintf(&b, "\n%s\n", res.Note)
	}
	return b.String()
}

// RenderEntity печатает карточку понятия.
func RenderEntity(src Chunks, e FoundEntity, chunks []ChunkKey, opt RenderOpts) string {
	opt = opt.norm()
	var b strings.Builder

	fmt.Fprintf(&b, "%s (%s)\n", e.Name, e.Type)
	same, near := aliasesFor(e, opt)
	if len(same) > 0 {
		fmt.Fprintf(&b, "он же: %s\n", strings.Join(same, ", "))
	}
	if len(near) > 0 {
		fmt.Fprintln(&b, nearNote(near))
	}
	fmt.Fprintf(&b, "упоминаний %d в %s\n", e.Mentions, plural(e.Books, "книге", "книгах", "книгах"))

	if len(e.Neighbors) > 0 {
		b.WriteString("\nСвязано с:\n")
		for _, n := range e.Neighbors {
			fmt.Fprintf(&b, "  %s (подтверждений %d)\n", n.Name, n.Count)
		}
	}
	if len(chunks) > 0 && src != nil {
		b.WriteString("\nГде об этом написано:\n")
		var i int
		for _, k := range chunks {
			info, ok := src.ChunkByRef(k.Doc, k.Ord)
			if !ok {
				continue
			}
			i++
			fmt.Fprintf(&b, "\n[%d] %s", i, bookLine(info))
			if opt.Collection != "" {
				fmt.Fprintf(&b, " · id=%s/%d#%d", opt.Collection, k.Doc, k.Ord)
			}
			b.WriteString("\n")
			b.WriteString(indent(cut(info.Text, opt.SnippetRunes), "    "))
			b.WriteString("\n")
		}
	}
	return b.String()
}

// RenderPath печатает цепочку между двумя понятиями.
func RenderPath(src Chunks, from, to string, steps []PathStep, ok bool, opt RenderOpts) string {
	opt = opt.norm()
	if !ok {
		return fmt.Sprintf("между «%s» и «%s» связи в графе нет.\n"+
			"Это не значит, что её нет в книгах: граф собран не по всей библиотеке, "+
			"и связь могла не попасться модели при разборе.", from, to)
	}
	if len(steps) == 0 {
		return fmt.Sprintf("«%s» и «%s» — это одно и то же понятие", from, to)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Путь из %d шагов:\n", len(steps))
	for i, s := range steps {
		fmt.Fprintf(&b, "  %d. %s —%s→ %s\n", i+1, s.From, s.Type, s.To)
		if src == nil {
			continue
		}
		if info, found := src.ChunkByRef(s.Evidence.Doc, s.Evidence.Ord); found {
			fmt.Fprintf(&b, "     подтверждение: %s", bookLine(info))
			if opt.Collection != "" {
				fmt.Fprintf(&b, " · id=%s/%d#%d", opt.Collection, s.Evidence.Doc, s.Evidence.Ord)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// bookLine — «Книга · Автор · стр. 40».
func bookLine(info kb.ChunkInfo) string {
	name := info.Book.Title
	if name == "" {
		name = info.Book.Path
	}
	parts := []string{name}
	if info.Book.Author != "" {
		parts = append(parts, info.Book.Author)
	}
	if info.Book.Year > 0 {
		parts = append(parts, fmt.Sprintf("%d г.", info.Book.Year))
	}
	if info.UnitFrom > 0 {
		if info.UnitTo > info.UnitFrom {
			parts = append(parts, fmt.Sprintf("%s %d–%d", info.Unit, info.UnitFrom, info.UnitTo))
		} else {
			parts = append(parts, fmt.Sprintf("%s %d", info.Unit, info.UnitFrom))
		}
	}
	return strings.Join(parts, " · ")
}

// cut обрезает текст по знакам, а не по байтам: библиотека двуязычная,
// и обрезка по байтам разрубила бы русскую букву пополам.
// evidenceLine — фраза из книги, подтверждающая связь.
//
// Берётся окно вокруг первого упоминания одного из концов связи: начало куска
// часто попадает на середину чужой мысли, а рядом с именем понятия стоит как раз
// то предложение, ради которого связь и была извлечена.
func evidenceLine(src Chunks, r FoundRelation, runes int) string {
	keys := r.Evidences
	if len(keys) == 0 {
		keys = []ChunkKey{r.Evidence}
	}

	// Из нескольких подтверждений берётся то, где рядом стоят ОБА конца связи:
	// именно там книга их и связывает. Замер 02.09.2026: у связи
	// `Go —использует→ Garbage collection` (186 подтверждений) первым куском
	// оказалась шпаргалка по приведению типов, где имя Go стоит само по себе.
	//
	// Первый подходящий кусок и берётся: перебирать все ради «лучшего» незачем,
	// разницы между двумя кусками, где связь названа прямо, для читателя нет.
	var first string
	for _, k := range keys {
		if k.Doc == 0 && k.Ord == 0 {
			continue
		}
		info, ok := src.ChunkByRef(k.Doc, k.Ord)
		if !ok {
			continue
		}
		text := strings.Join(strings.Fields(info.Text), " ") // в одну строку
		line := cut(around(text, []string{r.Src, r.Dst}, runes), runes)
		if line == "" {
			continue
		}
		if hasBoth(text, r.Src, r.Dst) {
			return line
		}
		if first == "" {
			first = line
		}
	}
	return first
}

// hasBoth — стоят ли в куске оба конца связи.
func hasBoth(text, a, b string) bool {
	low := strings.ToLower(text)
	a, b = strings.ToLower(strings.TrimSpace(a)), strings.ToLower(strings.TrimSpace(b))
	return a != "" && b != "" && strings.Contains(low, a) && strings.Contains(low, b)
}

// around вырезает из куска окно, в котором стоит связь.
//
// **Почему не «первое вхождение любого из концов».** Так было сделано сначала,
// и замер 02.09.2026 показал, чем это кончается: у связи `Go —использует→
// Garbage collection` выдержкой стал абзац про приведение типов, а у `Go
// —использует→ chan` — кусок C-кода про suid_binary. Имя `Go` встречается
// в техническом тексте на каждом шагу, и первое его вхождение почти никогда
// не там, где книга говорит о связи.
//
// **Что вместо.** Ищется место, где оба конца связи стоят ближе всего друг
// к другу: именно там книга их и связывает. Окно берётся так, чтобы накрыть
// оба имени, с небольшим отступом влево — фраза не должна начинаться с самого
// слова. Не нашлось обоих — прежнее поведение по одному, не нашлось ни одного —
// начало куска.
func around(text string, words []string, runes int) string {
	low := strings.ToLower(text)

	// Вхождения каждого конца по отдельности. Списки короткие: имя понятия
	// встречается в куске единицы раз.
	occ := make([][]int, 0, len(words))
	for _, w := range words {
		w = strings.ToLower(strings.TrimSpace(w))
		if w == "" {
			continue
		}
		var list []int
		for from := 0; ; {
			i := strings.Index(low[from:], w)
			if i < 0 {
				break
			}
			list = append(list, from+i)
			from += i + len(w)
		}
		if len(list) > 0 {
			occ = append(occ, list)
		}
	}
	if len(occ) == 0 {
		return text
	}

	at, last := occ[0][0], occ[0][0]
	if len(occ) > 1 {
		// Ближайшая пара вхождений разных концов. Перебор по двум коротким
		// спискам дешевле любой хитрости и не требует их упорядочивать.
		best := -1
		for _, i := range occ[0] {
			for _, j := range occ[1] {
				d := i - j
				if d < 0 {
					d = -d
				}
				if best < 0 || d < best {
					best, at, last = d, min(i, j), max(i, j)
				}
			}
		}
	}

	r := []rune(text)
	pos := len([]rune(text[:at]))
	// Отступаем назад на треть окна, чтобы фраза не начиналась с самого слова.
	from := pos - runes/3
	if from < 0 {
		from = 0
	}
	// Но не настолько, чтобы второй конец связи вывалился за край окна.
	if tail := len([]rune(text[:last])); tail-from > runes {
		from = tail - runes
		if from < 0 {
			from = 0
		}
	}
	if from >= len(r) {
		return text
	}
	out := string(r[from:])
	if from > 0 {
		out = "…" + out
	}
	return out
}

func cut(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// plural склоняет существительное по числу — по-русски, без «книг(и)».
func plural(n int, one, few, many string) string {
	word := many
	switch mod100 := n % 100; {
	case mod100 >= 11 && mod100 <= 14:
	default:
		switch n % 10 {
		case 1:
			word = one
		case 2, 3, 4:
			word = few
		}
	}
	return fmt.Sprintf("%d %s", n, word)
}
