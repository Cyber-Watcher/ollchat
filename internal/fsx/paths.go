package fsx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ExpandHome раскрывает «~» и «~/…» в домашний каталог. Другие формы
// («~user») не трогаются: их раскрывает оболочка, а не программа.
// До этапа 91 (R8.1) четыре копии этой функции жили в четырёх пакетах,
// и одна из них раскрывала «~foo» как «$HOME/foo».
func ExpandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}

// HumanSize — размер словами человека: байты, килобайты целым числом,
// мегабайты и гигабайты с одной десятой. Одна шкала на инструменты,
// интерфейс и команды обслуживания (этап 91, R8.2): до того три копии
// давали три разных записи одного числа.
func HumanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f ГБ", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f МБ", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d КБ", n/(1<<10))
	default:
		return fmt.Sprintf("%d Б", n)
	}
}
