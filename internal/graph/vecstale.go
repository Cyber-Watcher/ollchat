package graph

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"

	"github.com/Cyber-Watcher/ollchat/internal/fsx"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Отпечатки текстов, от которых посчитаны векторы понятий.
//
// **Зачем.** Вектор понятия считается не от «смысла», а от строки «имя,
// синоним, синоним» (`embedText`). Понятие, впервые встреченное в книге, часто
// не имеет ни одного синонима — вектор считается от одного слова. Потом то же
// понятие попадается в следующих книгах, извлечение приносит `goroutine`
// к «горутине», склейка двойников переносит чужие синонимы — строка меняется,
// а вектор остаётся посчитанным от прежней.
//
// **Замер 06.09.2026** (`docs/eval/vecage-0906.md`, 196 647 понятий): текст
// менялся у 13.2%, синонимы приходят сутками (медиана 2.6 ч, 90% в шесть
// суток), и у 3.5% понятий в старом тексте нет второго языка — того самого
// моста «горутина → goroutine», ради которого синонимы в вектор и кладутся.
//
// **Почему отпечаток, а не время.** Время счёта у нас общее на весь файл,
// а понятий четверть миллиона; отпечаток текста отвечает на вопрос точно:
// «посчитан ли этот вектор от того, что написано сейчас». Восемь байт
// на понятие — два мегабайта на нашем графе.
const entVecStampFile = "entities.vecstamp"

// textStamp — отпечаток текста понятия. FNV-1a: он не криптографический,
// но нам нужна не защита, а совпадение с самим собой.
func textStamp(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// saveStamps пишет отпечатки по местам: место N-1 — понятие с номером N.
func saveStamps(dir string, texts []string) error {
	raw := make([]byte, 8*len(texts))
	for i, t := range texts {
		binary.LittleEndian.PutUint64(raw[i*8:], textStamp(t))
	}
	return fsx.WriteFileAtomic(filepath.Join(dir, entVecStampFile), raw, 0o644)
}

// loadStamps читает отпечатки. Файла нет — пусто, и это не ошибка: графы,
// посчитанные до появления отпечатков, живут без них.
func loadStamps(dir string) []uint64 {
	raw, err := os.ReadFile(filepath.Join(dir, entVecStampFile))
	if err != nil || len(raw) < 8 {
		return nil
	}
	out := make([]uint64, len(raw)/8)
	for i := range out {
		out[i] = binary.LittleEndian.Uint64(raw[i*8:])
	}
	return out
}

// StaleEntities — номера понятий, чей текст изменился после счёта вектора.
//
// Второе значение — есть ли вообще отпечатки: без них сказать нечего, и это
// надо отличать от «всё в порядке».
func (g *Graph) StaleEntities() (ids []uint32, haveStamps bool) {
	texts, err := g.embedTexts()
	if err != nil {
		return nil, false
	}
	stamps := loadStamps(g.dir)
	if len(stamps) == 0 {
		return nil, false
	}
	_, already := g.vecs.Existing(g.vecs.Model(), 0)
	for i, t := range texts {
		if i >= len(stamps) || i >= already {
			break // дальше векторов нет вовсе — это работа обычного досчёта
		}
		if stamps[i] != textStamp(t) {
			ids = append(ids, uint32(i+1))
		}
	}
	return ids, true
}

// EmbedStale пересчитывает векторы понятий, чей текст изменился после счёта.
//
// Дешёвая половина лечения: полный пересчёт нашего графа — двадцать девять
// минут карты и четверть миллиона понятий, а изменившихся за сутки — единицы
// тысяч, то есть секунды эмбеддера. Вектор перезаписывается на своём месте,
// остальные не трогаются вовсе.
func (g *Graph) EmbedStale(ctx context.Context, emb kb.Embedder, o EmbedOpts,
	onProgress func(EmbedProgress)) (fixed int, err error) {

	if emb == nil {
		return 0, fmt.Errorf("эмбеддер не задан")
	}
	o = o.norm()

	release, err := lockVectors(g.dir)
	if err != nil {
		return 0, err
	}
	defer release()

	ids, have := g.StaleEntities()
	if !have {
		return 0, fmt.Errorf("отпечатков текстов нет: они появляются при счёте векторов " +
			"этой сборкой. Один раз пересчитайте всё (--graph-embed-recount), " +
			"дальше устаревшие будут находиться сами")
	}
	if len(ids) == 0 {
		return 0, nil
	}

	texts, err := g.embedTexts()
	if err != nil {
		return 0, err
	}
	digest := embedderDigest(ctx, emb)
	if err := checkDigest(g.vecs.Digest(), digest, emb.Model()); err != nil {
		return 0, err
	}
	data, already := g.vecs.Existing(emb.Model(), 0)
	if already == 0 {
		return 0, fmt.Errorf("векторов нет — сначала посчитайте их обычным способом")
	}
	dim := len(data) / already

	// Считаем только изменившиеся тексты, пачками того же размера.
	want := make([]string, 0, len(ids))
	for _, id := range ids {
		want = append(want, texts[id-1])
	}
	newDim, fresh, err := embedBatches(ctx, emb, want, o, onProgress)
	if err != nil {
		return 0, err
	}
	if newDim != dim {
		return 0, fmt.Errorf("размерность нового счёта %d против прежней %d", newDim, dim)
	}

	// Перезапись на месте: чужие векторы не трогаются.
	for k, id := range ids {
		copy(data[int(id-1)*dim:int(id)*dim], fresh[k*dim:(k+1)*dim])
	}
	if err := g.SaveEntityVectors(emb.Model(), digest, dim, data); err != nil {
		return 0, err
	}
	return len(ids), nil
}
