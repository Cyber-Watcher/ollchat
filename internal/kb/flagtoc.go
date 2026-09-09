package kb

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/Cyber-Watcher/ollchat/internal/fsx"
)

// Проход по индексу: признак оглавления у уже проиндексированных книг (этап 99).
//
// **Почему проход, а не перенарезка.** Кусок — единица адресации графа:
// упоминания, связи и отметки разбора хранят пару «книга, номер куска».
// Перенарезать книгу значит раздать новые номера, и граф укажет не туда.
// Здесь переписывается только поле признаков в `chunks.idx`; `chunks.dat`,
// номера, страницы и длины не меняются. Запись атомарна (temp → rename),
// поэтому читатель с открытым старым индексом видит старый, а новый —
// целиком; половинчатого состояния нет.
//
// **Под замком индексации.** Доливка книг дописывает `chunks.idx`; проход,
// переписывающий файл целиком, потерял бы дописанное. Поэтому берётся тот же
// замок, что у индексации, и идущей индексации проход уступает.

// FlagTOCResult — что дал проход.
type FlagTOCResult struct {
	Total   int // кусков в коллекции
	Flagged int // с признаком после прохода
	Was     int // с признаком до прохода
	Changed int // у скольких признак изменился

	WorstDoc   uint32 // книга, где помеченных больше всего
	WorstN     int
	WorstTotal int

	// FlaggedRefs — сколько кусков помечено списком литературы или
	// выходными данными (входит в общий счёт Flagged не отдельно).
	FlaggedRefs int
}

// FlagTOC ставит признаки СЛУЖЕБНОГО текста: оглавление (LooksLikeTOC) и
// список литературы или выходные данные (LooksLikeRefs, LooksLikeColophon,
// этап 101). Снимает их там,
// где эвристика больше не срабатывает (она может уточняться). dry — только
// посчитать, файл не трогать. Повторный проход ничего не меняет.
func (c *Collection) FlagTOC(ctx context.Context, dry bool, progress func(done, total int)) (FlagTOCResult, error) {
	var res FlagTOCResult
	if c.store == nil {
		return res, fmt.Errorf("коллекция %q без хранилища кусков", c.name)
	}
	if !dry {
		if err := c.lock(); err != nil {
			return res, err
		}
		defer c.unlock()
	}

	recs := c.store.Recs()
	res.Total = len(recs)
	flags := make([]uint16, len(recs))
	perDoc := map[uint32][2]int{}
	const batch = chunksPerBlock * 4
	ids := make([]int, 0, batch)
	for from := 0; from < len(recs); from += batch {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		to := min(from+batch, len(recs))
		ids = ids[:0]
		for i := from; i < to; i++ {
			ids = append(ids, i)
		}
		texts, err := c.store.Texts(ids)
		if err != nil {
			return res, err
		}
		for i := from; i < to; i++ {
			f := recs[i].Flags
			if f&uint16(FlagTOC|FlagRefs) != 0 {
				res.Was++
			}
			// Признаки снимаются оба и ставятся заново: проход идёт и после
			// правки эвристики, и тогда кусок, переставший быть служебным,
			// обязан вернуться в выдачу.
			f &^= uint16(FlagTOC | FlagRefs)
			pd := perDoc[recs[i].Doc]
			pd[0]++
			if LooksLikeTOC(texts[i]) {
				f |= uint16(FlagTOC)
				res.Flagged++
				pd[1]++
			} else if LooksLikeRefs(texts[i]) || LooksLikeColophon(texts[i]) {
				f |= uint16(FlagRefs)
				res.FlaggedRefs++
				pd[1]++
			}
			perDoc[recs[i].Doc] = pd
			if f != recs[i].Flags {
				res.Changed++
			}
			flags[i] = f
		}
		if progress != nil {
			progress(to, len(recs))
		}
	}
	for d, pd := range perDoc {
		if pd[1] > res.WorstN {
			res.WorstDoc, res.WorstN, res.WorstTotal = d, pd[1], pd[0]
		}
	}
	if dry || res.Changed == 0 {
		return res, nil
	}

	raw := make([]byte, len(recs)*chunkRecSize)
	for i := range recs {
		r := recs[i]
		r.Flags = flags[i]
		r.encode(raw[i*chunkRecSize:])
	}
	if err := fsx.WriteFileAtomic(filepath.Join(c.dir, "chunks.idx"), raw, 0o644); err != nil {
		return res, err
	}
	c.mu.Lock()
	for i := range c.store.recs {
		c.store.recs[i].Flags = flags[i]
	}
	c.mu.Unlock()
	return res, nil
}
