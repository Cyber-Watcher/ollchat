package permissions

import "testing"

// Ради этого и делалось: счёт файлов проходит молча.
func TestReadingFindAndSortPass(t *testing.T) {
	g := guardWith(t, []string{"Bash(find:*)", "Bash(sort:*)", "Bash(echo:*)", "Bash(wc:*)"}, nil, "safe")
	cmd := `find заметки -type f | sort; echo ===; echo "Всего файлов:"; find заметки -type f | wc -l`
	if got := decide(g, cmd); got.Decision != DecisionAllow {
		t.Errorf("вышло %v (%s), ожидалось разрешение", got.Decision, got.Reason)
	}
}

// А ключ, которым та же программа удаляет, пишет или запускает чужое,
// возвращает вопрос — сколько бы правил ни стояло.
func TestWritingFlagsAsk(t *testing.T) {
	g := guardWith(t, []string{"Bash(find:*)", "Bash(sort:*)", "Bash(ls:*)"}, nil, "safe")
	for _, cmd := range []string{
		"find . -name '*.tmp' -delete",
		"find . -type f -exec rm {} ;",
		"find . -okdir rm {} ;",
		"sort -o важное.txt важное.txt",
		"sort --output=важное.txt важное.txt",
		"find заметки -type f | sort -o список.txt",
	} {
		if got := decide(g, cmd); got.Decision != DecisionAsk {
			t.Errorf("%q: вышло %v (%s), ожидался вопрос", cmd, got.Decision, got.Reason)
		}
	}
}

// Имя файла со словом -delete ключом не становится: у find ключи — целые слова.
func TestSimilarFlagIsNotWriting(t *testing.T) {
	if WritesSomething("find . -type f -name '*-delete-me*'") {
		t.Error("имя файла со словом -delete принято за ключ -delete")
	}
}

// Слитное значение короткого ключа — тот же ключ: sort разбирает ключи
// через getopt, и `-ofile` пишет в file так же, как `-o file`. Раньше слово
// сравнивалось целиком, и при `Bash(sort:*)` в allow файл переписывался
// без вопроса. `-original` из той же породы: это `-o riginal`, и sort
// действительно создаёт файл riginal (проверено на GNU coreutils 9.4).
func TestGluedShortOptionWrites(t *testing.T) {
	g := guardWith(t, []string{"Bash(sort:*)"}, nil, "safe")
	for _, cmd := range []string{
		"sort -ofile data",
		"sort -original data",
		"sort -rofile data",
		"sort -nro file data",
		`sort -o"file" data`,
		`sort '-ofile' data`,
		"sort -t, -ofile data",
		"sort data -o file",
		"sort --out=file data",
		"sort --outp file data",
		"LC_ALL=C sort -ofile data",
	} {
		if !WritesSomething(cmd) {
			t.Errorf("%q: пишущий ключ не замечен", cmd)
		}
		if got := decide(g, cmd); got.Decision != DecisionAsk {
			t.Errorf("%q: вышло %v (%s), ожидался вопрос", cmd, got.Decision, got.Reason)
		}
	}
}

// Значение другого ключа и имя файла после «--» пишущим ключом не считаются.
func TestValueOfOtherOptionIsNotWriting(t *testing.T) {
	for _, cmd := range []string{
		"sort -t o data",
		"sort -to data",
		"sort -k 1,1 data",
		"sort -T /tmp -r data",
		"sort -- -ofile",
		"sort -r data",
	} {
		if WritesSomething(cmd) {
			t.Errorf("%q: принято за запись", cmd)
		}
	}
}
