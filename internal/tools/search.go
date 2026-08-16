package tools

import (
	"fmt"
	"regexp"
)

// searchRE — регулярное выражение поиска. Отдельное имя оставлено ради
// читаемости сигнатур в grep.
type searchRE = regexp.Regexp

// compileSearch компилирует шаблон поиска, поясняя ошибку по-русски.
func compileSearch(pattern string) (*searchRE, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("некорректное регулярное выражение %q: %w", pattern, err)
	}
	return re, nil
}
