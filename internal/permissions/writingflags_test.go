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

// Похожий по началу ключ вопросом не становится: `-original` это не `-o`.
func TestSimilarFlagIsNotWriting(t *testing.T) {
	if WritesSomething("sort -original файл") {
		t.Error("-original принят за -o")
	}
	if WritesSomething("find . -type f -name '*-delete-me*'") {
		t.Error("имя файла со словом -delete принято за ключ -delete")
	}
}
