package kb

import "testing"

const refsBlock = `Traag, V. A., Waltman, L., & van Eck, N. J. (2019). From Louvain to Leiden:
Guaranteeing well-connected communities. Scientific Reports, 9, 5233.
Girvan, M., & Newman, M. E. J. (2002). Community structure in social networks.
Proceedings of the National Academy of Sciences, 99(12), 7821-7826.
Gao, L., Ma, X., Lin, J. (2022). Precise Zero-Shot Dense Retrieval. arXiv:2212.10496.`

const prose = `Сообщества графа считаются алгоритмом Louvain. Он жадный: узел переносится
в то сообщество, где прирост модулярности наибольший. При равных приростах выбор
произволен, и два запуска на одних данных дают разные сообщества — поэтому узлы
обходятся в порядке возрастания номера. Цена — чуть худшее разбиение.`

func TestLooksLikeRefsFindsBibliography(t *testing.T) {
	if !LooksLikeRefs(refsBlock) {
		t.Error("список литературы не распознан")
	}
	if LooksLikeRefs(prose) {
		t.Error("обычный текст принят за список литературы")
	}
}

// Абзац, ссылающийся на источник, списком литературы не является: одна запись
// из шести строк — это цитирование в тексте, и терять такой кусок нельзя.
func TestLooksLikeRefsIgnoresSingleCitation(t *testing.T) {
	text := prose + "\nПодробности см. Traag et al. (2019), где доказана связность."
	if LooksLikeRefs(text) {
		t.Error("абзац со ссылкой принят за список литературы")
	}
}

// Короткий кусок не решается по доле: четыре строки — нижняя граница, как
// у эвристики оглавлений.
func TestLooksLikeRefsNeedsEnoughLines(t *testing.T) {
	if LooksLikeRefs("Traag, V. (2019). From Louvain to Leiden.") {
		t.Error("одна строка признана списком литературы")
	}
}

func TestRefsHeading(t *testing.T) {
	for _, s := range []string{"References\nTraag, V. (2019).", "Bibliography",
		"Список литературы\n1. Кнут Д.", "Further Reading:"} {
		if !RefsHeading(s) {
			t.Errorf("заголовок не распознан: %q", s)
		}
	}
	// Слово внутри предложения заголовком не делает.
	for _, s := range []string{
		"The references section lists every paper cited in this chapter, and it is long.",
		"Литература по теме графов огромна, и мы приводим лишь основные работы автора.",
	} {
		if RefsHeading(s) {
			t.Errorf("предложение принято за заголовок: %q", s)
		}
	}
}

func TestLooksLikeColophonNeedsSeveralMarks(t *testing.T) {
	colophon := `The cover image is a nineteenth-century engraving.
The cover designer is Karen Montgomery. The text font is Adobe Minion Pro;
the heading font is Adobe Myriad Condensed. ISBN: 978-1-098-15601-3.
All rights reserved. Printed in the United States of America.`
	if !LooksLikeColophon(colophon) {
		t.Error("колофон не распознан")
	}
	// Одно упоминание ISBN в тексте о книгоиздании — не колофон.
	if LooksLikeColophon("Каждая книга имеет ISBN, и по нему её можно найти в каталоге.") {
		t.Error("упоминание ISBN принято за колофон")
	}
	if LooksLikeColophon(prose) {
		t.Error("обычный текст принят за колофон")
	}
}

// Год в скобках распознаётся во всех обычных написаниях и не путается
// с числами в скобках вообще.
func TestBracketYear(t *testing.T) {
	for _, s := range []string{"Knuth (1997)", "Traag (2019a)", "см. (2002, с. 12)"} {
		if !hasBracketYear(s) {
			t.Errorf("год не распознан: %q", s)
		}
	}
	for _, s := range []string{"результат (0.667)", "шаг (12)", "окно (8192)", "(19)"} {
		if hasBracketYear(s) {
			t.Errorf("число принято за год: %q", s)
		}
	}
}

// Список полезных ссылок — не библиография: маркированный перечень сообществ
// и репозиториев есть в каждой второй технической книге, и терять его нельзя.
// Пойман переписью 09.09.2026 в «Learning Angular».
func TestLooksLikeRefsSpareLinkLists(t *testing.T) {
	links := `• GitHub : https://github.com/PacktPublishing/Learning-Angular-Fifth-Edition
• Node.js : https://nodejs.org
• Git : https://git-scm.com
• VSCode : https://code.visualstudio.com
• Angular DevTools : https://angular.dev/tools/devtools
Angular is a web framework written in the TypeScript language.`
	if LooksLikeRefs(links) {
		t.Error("список ссылок принят за библиографию")
	}
}

// Инициалы — признак записи, но два сокращения в обычной строке им не являются.
func TestHasInitials(t *testing.T) {
	for _, s := range []string{"I. Goodfellow, Y. Bengio, A. Courville", "Klein, P. N.", "[3] W. Kurt, A. Ng"} {
		if !hasInitials(s) {
			t.Errorf("инициалы не распознаны: %q", s)
		}
	}
	for _, s := range []string{"см. п. 3", "Модель обучается на данных", "T. — это температура"} {
		if hasInitials(s) {
			t.Errorf("обычная строка принята за запись: %q", s)
		}
	}
}

// Академический стиль ссылок ставит год в скобках в обычный текст: «Artificial
// Intelligence: A Modern Approach» так написана целиком, и вторая редакция
// эвристики взяла из неё 331 кусок прозы (перепись 09.09.2026). Решать должен
// зачин строки, а не год где-то в ней.
func TestLooksLikeRefsSparesAuthorYearProse(t *testing.T) {
	prose := `Somewhat remarkably, almost all AI research until very recently has assumed
that the performance measure can be exactly specified (Russell and Norvig, 2020).
The idea of utility was developed by von Neumann and Morgenstern (1944), and
extended to sequential decisions by Bellman (1957). Later work (Kaelbling, 1998)
showed that partial observability makes the problem much harder in practice.`
	if LooksLikeRefs(prose) {
		t.Error("проза с ссылками автор-год принята за список литературы")
	}
}

// Настоящая запись распознаётся во всех трёх обычных написаниях зачина.
func TestStartsLikeRefEntry(t *testing.T) {
	for _, s := range []string{
		"Abbeel, P. and Ng, A. Y. (2004). Apprenticeship learning. In Proceedings of ICML.",
		"[12] Traag V., Waltman L., van Eck N. From Louvain to Leiden. Sci Rep, 2019.",
		"1. Fortune, M. J. AI-powered coding tool wiped out a database, 2025.",
		"I. Goodfellow, Y. Bengio, A. Courville, Deep Learning. MIT Press, 2016.",
	} {
		if !startsLikeRefEntry(s) {
			t.Errorf("зачин записи не распознан: %q", s)
		}
	}
	for _, s := range []string{
		"Модель обучается на данных, собранных в 2019 году, и это важно.",
		"The idea of utility was developed by von Neumann and Morgenstern (1944).",
		"• Angular DevTools : https://angular.dev/tools/devtools",
	} {
		if startsLikeRefEntry(s) {
			t.Errorf("обычная строка принята за запись: %q", s)
		}
	}
}

// Признак служебного текста ставится при нарезке: и оглавлению, и списку
// литературы, и колофону — по одному куску на каждый вид.
func TestServiceFlagsAtChunking(t *testing.T) {
	toc := "Глава 1. Введение .... 7\nГлава 2. Устройство .... 21\nГлава 3. Поиск .... 45\nГлава 4. Граф .... 78"
	if f := tocFlag(toc); f&FlagTOC == 0 || f&FlagRefs != 0 {
		t.Errorf("оглавление помечено неверно: %b", f)
	}
	if f := tocFlag(refsBlock); f&FlagRefs == 0 {
		t.Errorf("список литературы не помечен: %b", f)
	}
	if f := tocFlag(prose); f != 0 {
		t.Errorf("обычному тексту поставлен служебный признак: %b", f)
	}
}

// Заголовок списка литературы делает кусок служебным, даже когда самих записей
// в нём две-три и доля «строк-ссылок» не набирается (этап 104, Ж1.2).
func TestServiceRefsByHeading(t *testing.T) {
	heading := "Further reading\n\nYou can refer to the following links for more information:\n• Ansible 2 for Configuration Management"
	if LooksLikeRefs(heading) {
		t.Fatal("образец должен не проходить по доле строк — иначе тест ничего не проверяет")
	}
	if !ServiceRefs(heading) {
		t.Fatal("кусок с заголовком «Further reading» не признан служебным")
	}
	// Слово внутри предложения заголовком не считается.
	for _, prose := range []string{
		"References to the original object are kept by the garbage collector until the scope ends.",
		"Литература по этой теме обширна, и мы разберём три подхода к проектированию.",
	} {
		if ServiceRefs(prose) {
			t.Errorf("обычный текст принят за список литературы: %q", prose)
		}
	}
}
