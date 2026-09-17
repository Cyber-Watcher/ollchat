package graph

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/fsx"
)

// Снятие отдельных склеек.
//
// **Зачем.** До 17.09.2026 склейки снимались только все разом (DropMerges),
// а ошибаются они поштучно: перепись склеек-цепочек нашла 47 пар из 357,
// где выжившие понятия далеки друг от друга (`API secret → Secrets API`), —
// вердикт арбитра выносился другой паре (этап 104, S14). Снимать ради них
// 22 тысячи верных склеек нельзя.
//
// **Почему журнал переписывается, а не дополняется записью «отмена».**
// `merges.jsonl` читают напрямую больше десятка скриптов замеров и сверка
// доктора вторым путём (`graphdoctorcheck.py`): для них каждая строка с from
// и to — действующая склейка. Запись-отмена сделала бы неверным каждый из них
// молча. Поэтому снятые строки из журнала уходят, сам журнал подменяется
// атомарно с копией `merges.jsonl.bak-<время>`, а след решения не теряется:
// снятые записи дописываются в `merges-undone.jsonl` — когда, почему и что
// именно стояло в журнале.
//
// **Что происходит с графом.** Склейка — наложение при чтении, поэтому снятие
// ничего не пересчитывает: поглощённое понятие снова находится само, его
// упоминания и связи (они хранятся по номерам) возвращаются к нему. Устаревают
// вектор выжившего (в его текст входили имена поглощённого — лечит
// `--graph-embed-stale`) и разбиение на темы (у вернувшегося понятия темы нет —
// лечит `--graph-communities`). Упоминания, записанные ЗА ВРЕМЯ склейки под
// номером выжившего по имени поглощённого, остаются у выжившего: отличить их
// в журнале упоминаний нечем.

const undoneMergesFile = "merges-undone.jsonl"

// UndoneMerge — снятая склейка, как она стояла в журнале, и почему снята.
type UndoneMerge struct {
	MergeRec
	UndoneAt int64  `json:"undone_at"`
	UndoneBy string `json:"undone_why,omitempty"`
}

// UnmergeResult — что сделано (или было бы сделано) снятием склеек.
type UnmergeResult struct {
	Asked    int        // пар в запросе
	Undone   []MergeRec // снятые записи журнала
	Missing  [][2]uint32
	Mismatch [][3]uint32 // from, to из запроса, to из журнала
	// Carried — поглощённые, которые вернувшееся понятие уводит с собой:
	// A→B, B→C; снимаем B→C — A остаётся внутри B.
	Carried int
	Backup  string
}

// UndoMerges снимает склейки «from → to». To, равное нулю, значит «куда бы
// ни вело». Пара, которой в журнале нет или которая ведёт не туда, не снимается
// и возвращается в отчёте: список снятий готовят по снимку журнала, а журнал
// с тех пор мог измениться.
func (g *Graph) UndoMerges(pairs [][2]uint32, why string, dry bool) (UnmergeResult, error) {
	res := UnmergeResult{Asked: len(pairs)}
	if g == nil || g.merges == nil {
		return res, fmt.Errorf("граф не открыт")
	}
	if !dry {
		// Журнал подменяется целиком: сборка, дописывающая склейки при связывании,
		// в эти секунды потеряла бы свою запись.
		release, err := holdBuildLock(g.dir)
		if err != nil {
			return res, err
		}
		defer release()
	}
	return g.merges.undo(pairs, why, dry, &res)
}

func (m *Merges) undo(pairs [][2]uint32, why string, dry bool, res *UnmergeResult) (UnmergeResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Действует ПОСЛЕДНЯЯ запись о номере — так читает rebuild. Add повторов
	// не пишет, но журнал могли править руками; снимаются все записи о номере,
	// иначе уцелевший повтор оставил бы понятие склеенным.
	lastTo := map[uint32]uint32{}
	for _, r := range m.recs {
		lastTo[r.From] = r.To
	}
	drop := map[uint32]bool{}
	for _, p := range pairs {
		to, ok := lastTo[p[0]]
		switch {
		case !ok:
			res.Missing = append(res.Missing, p)
		case p[1] != 0 && to != p[1]:
			res.Mismatch = append(res.Mismatch, [3]uint32{p[0], p[1], to})
		default:
			drop[p[0]] = true
		}
	}
	var keep []MergeRec
	for _, r := range m.recs {
		if drop[r.From] {
			res.Undone = append(res.Undone, r)
			continue
		}
		keep = append(keep, r)
	}
	// Кого вернувшиеся понятия уносят с собой. По сырым записям, а не по m.from:
	// там цепочки уже сжаты, и A из «A→B, B→C» числится за C, а не за B.
	for _, r := range keep {
		seen := map[uint32]bool{}
		for cur := r.To; !seen[cur]; {
			if drop[cur] {
				res.Carried++
				break
			}
			seen[cur] = true
			next, ok := lastTo[cur]
			if !ok {
				break
			}
			cur = next
		}
	}
	if dry || len(res.Undone) == 0 {
		return *res, nil
	}

	// 1. Копия прежнего журнала. 2. След снятого. 3. Атомарная подмена.
	stamp := time.Now().Format("20060102-150405")
	old, err := os.ReadFile(m.path)
	if err != nil {
		return *res, err
	}
	res.Backup = m.path + ".bak-" + stamp
	if err := fsx.WriteFileAtomic(res.Backup, old, 0o644); err != nil {
		return *res, fmt.Errorf("копия журнала склеек: %w", err)
	}
	now := time.Now().Unix()
	uf, err := os.OpenFile(filepath.Join(filepath.Dir(m.path), undoneMergesFile),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return *res, err
	}
	uw := bufio.NewWriter(uf)
	for _, r := range res.Undone {
		b, err := json.Marshal(UndoneMerge{MergeRec: r, UndoneAt: now, UndoneBy: why})
		if err != nil {
			uf.Close()
			return *res, err
		}
		uw.Write(append(b, '\n'))
	}
	if err := uw.Flush(); err != nil {
		uf.Close()
		return *res, err
	}
	if err := uf.Sync(); err != nil {
		uf.Close()
		return *res, err
	}
	if err := uf.Close(); err != nil {
		return *res, err
	}

	var buf bytes.Buffer
	for _, r := range keep {
		b, err := json.Marshal(r)
		if err != nil {
			return *res, err
		}
		buf.Write(append(b, '\n'))
	}
	if err := fsx.WriteFileAtomic(m.path, buf.Bytes(), 0o644); err != nil {
		return *res, fmt.Errorf("подмена журнала склеек: %w", err)
	}
	m.recs = keep
	m.rebuild()
	return *res, nil
}

// RawEntity отдаёт запись понятия БЕЗ наложения склеек: у поглощённого —
// его собственное имя, а не имя выжившего. Нужна там, где склейки разбирают,
// а не применяют.
func (e *Entities) RawEntity(id uint32) (Entity, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if id == 0 || uint32(len(e.list)) < id || e.list[id-1].ID == 0 {
		return Entity{}, false
	}
	return e.list[id-1], true
}
