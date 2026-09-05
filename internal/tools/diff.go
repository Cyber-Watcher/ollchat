package tools

import (
	"fmt"
	"strings"
)

// UnifiedDiff строит построчный diff для предпросмотра правки перед подтверждением.
// Формат упрощённый: строки с префиксами "+", "-" и " ", с пропуском больших
// совпадающих участков.
func UnifiedDiff(oldText, newText string, context int) string {
	if context <= 0 {
		context = 3
	}
	oldLines := splitLines(oldText)
	newLines := splitLines(newText)

	ops := diffOps(oldLines, newLines)
	changed := false
	for _, op := range ops {
		if op.kind != opEqual {
			changed = true
			break
		}
	}
	if !changed {
		return "(файл не изменится)"
	}

	// Отмечаем строки, попадающие в окно контекста вокруг изменений.
	keep := make([]bool, len(ops))
	for i, op := range ops {
		if op.kind == opEqual {
			continue
		}
		for j := i - context; j <= i+context; j++ {
			if j >= 0 && j < len(ops) {
				keep[j] = true
			}
		}
	}

	var b strings.Builder
	skipped := 0
	flushSkip := func() {
		if skipped > 0 {
			fmt.Fprintf(&b, "  … пропущено строк: %d\n", skipped)
			skipped = 0
		}
	}
	for i, op := range ops {
		if !keep[i] {
			skipped++
			continue
		}
		flushSkip()
		switch op.kind {
		case opEqual:
			b.WriteString("  " + op.text + "\n")
		case opDel:
			b.WriteString("- " + op.text + "\n")
		case opAdd:
			b.WriteString("+ " + op.text + "\n")
		}
	}
	flushSkip()
	return strings.TrimRight(b.String(), "\n")
}

// DiffStat возвращает число добавленных и удалённых строк.
func DiffStat(oldText, newText string) (added, removed int) {
	for _, op := range diffOps(splitLines(oldText), splitLines(newText)) {
		switch op.kind {
		case opAdd:
			added++
		case opDel:
			removed++
		}
	}
	return added, removed
}

type opKind int

const (
	opEqual opKind = iota
	opDel
	opAdd
)

type diffOp struct {
	kind opKind
	text string
}

// diffOps вычисляет различия по наибольшей общей подпоследовательности.
// Для больших файлов таблица LCS дорога, поэтому при превышении предела
// сравнение вырождается в «весь старый текст удалён, весь новый добавлен».
func diffOps(a, b []string) []diffOp {
	const maxCells = 4_000_000
	if len(a)*len(b) > maxCells {
		ops := make([]diffOp, 0, len(a)+len(b))
		for _, l := range a {
			ops = append(ops, diffOp{opDel, l})
		}
		for _, l := range b {
			ops = append(ops, diffOp{opAdd, l})
		}
		return ops
	}

	// Отсекаем совпадающие начало и конец — это резко уменьшает таблицу.
	start := 0
	for start < len(a) && start < len(b) && a[start] == b[start] {
		start++
	}
	endA, endB := len(a), len(b)
	for endA > start && endB > start && a[endA-1] == b[endB-1] {
		endA--
		endB--
	}

	midA, midB := a[start:endA], b[start:endB]
	lcs := make([][]int, len(midA)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(midB)+1)
	}
	for i := len(midA) - 1; i >= 0; i-- {
		for j := len(midB) - 1; j >= 0; j-- {
			if midA[i] == midB[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	ops := make([]diffOp, 0, len(a)+len(b))
	for _, l := range a[:start] {
		ops = append(ops, diffOp{opEqual, l})
	}
	i, j := 0, 0
	for i < len(midA) && j < len(midB) {
		switch {
		case midA[i] == midB[j]:
			ops = append(ops, diffOp{opEqual, midA[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{opDel, midA[i]})
			i++
		default:
			ops = append(ops, diffOp{opAdd, midB[j]})
			j++
		}
	}
	for ; i < len(midA); i++ {
		ops = append(ops, diffOp{opDel, midA[i]})
	}
	for ; j < len(midB); j++ {
		ops = append(ops, diffOp{opAdd, midB[j]})
	}
	for _, l := range a[endA:] {
		ops = append(ops, diffOp{opEqual, l})
	}
	return ops
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	// Завершающий перевод строки не считаем отдельной пустой строкой.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
