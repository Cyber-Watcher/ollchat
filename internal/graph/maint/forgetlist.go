package maint

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// ForgetList убирает из графа всё, что извлечено из кусков, перечисленных
// в файле (этап 104, Ж1.1).
//
// **Зачем.** Ошибки прошлых извлечений живут в журналах графа и без
// перезвлечения не лечатся, а перезвлечение всего — недели видеокарты.
// Лечить можно прицельно: перепись находит виноватые куски (библиография,
// распавшаяся вёрстка, ложное раскрытие аббревиатуры, связь, которой нет
// в тексте), эта команда за секунды убирает извлечённое из них, а следующая
// сборка перечитывает только их. Номера понятий, векторы и разбиение
// не меняются; прежние журналы остаются рядом копиями `.bak-<время>`.
//
// **Формат файла:** номер куска `книга#кусок` в начале строки, дальше через
// пробел или табуляцию — что угодно (причина, имя понятия): перепись пишет
// список сразу с объяснением, и человек читает тот же файл, что и команда.
// Пустые строки и строки с `//` пропускаются.
//
// skip — судьба куска: false — «разобрать заново следующей сборкой»,
// true — «не разбирать вовсе» (мусор).
func ForgetList(stdout io.Writer, cfg *config.Config, name, file string, skip, dry bool) error {
	keys, wholeDocs, err := readChunkList(file)
	if err != nil {
		return err
	}
	if len(keys) == 0 && len(wholeDocs) == 0 {
		return fmt.Errorf("в файле %s нет ни одного номера куска", file)
	}
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()
	coll, err := base.Open(name)
	if err != nil {
		return err
	}
	// Опечатка в списке не должна пройти молча: куска с таким номером нет —
	// отказ до любой правки.
	var unknown []string
	for packed := range keys {
		k := graph.UnpackChunk(packed)
		if _, ok := coll.ChunkByRef(k.Doc, k.Ord); !ok {
			unknown = append(unknown, k.String())
		}
	}
	// Книга целиком («12#*»): нужна перед перечитыванием книги и после её
	// удаления. По живым кускам её не развернуть — у удалённой книги их нет,
	// а в графе её след остался; поэтому книга сверяется с реестром
	// коллекции (живые и удалённые), а куски отбираются по номеру книги
	// прямо в журналах графа.
	known := map[uint32]bool{}
	for _, b := range coll.Books() {
		known[b.ID] = true
	}
	for doc := range wholeDocs {
		if !known[doc] {
			unknown = append(unknown, fmt.Sprintf("%d#*", doc))
		}
	}
	if len(unknown) > 0 {
		shown := unknown
		if len(shown) > 8 {
			shown = shown[:8]
		}
		return fmt.Errorf("в коллекции %s нет кусков: %s (всего таких %d из %d) — список не от этой коллекции?",
			name, strings.Join(shown, ", "), len(unknown), len(keys)+len(wholeDocs))
	}

	dir := cfg.Graph.Rules().Dir(coll.Dir())
	if busy := graph.Busy(coll.Dir()); busy != "" {
		return fmt.Errorf("коллекция занята: %s — забывать куски под идущей работой нельзя", busy)
	}
	if err := kb.WaitArchive(coll.Dir(), kb.ArchiveWait); err != nil {
		return err
	}
	unmark, err := graph.MarkWork(dir, "чистка графа по списку кусков")
	if err != nil {
		return err
	}
	defer unmark()

	mark, fate := graph.MarkService, "будут разобраны заново следующей сборкой"
	if skip {
		mark, fate = graph.MarkSkipped, "больше разбираться не будут"
	}
	started := time.Now()
	drop := func(k graph.ChunkKey) bool { return keys[k.Pack()] || wholeDocs[k.Doc] }
	st, err := graph.ForgetChunksAs(dir, drop, mark, dry)
	if err != nil {
		return err
	}
	what := "граф очищен"
	if dry {
		what = "было бы убрано"
	}
	fmt.Fprintf(stdout, "%s (%s): кусков в списке %d, книг целиком %d; %s; за %s\n",
		what, name, len(keys), len(wholeDocs), st, time.Since(started).Round(time.Second))
	fmt.Fprintf(stdout, "  судьба кусков: %s\n", fate)
	for _, b := range st.Backups {
		fmt.Fprintf(stdout, "  прежний журнал: %s\n", b)
	}
	if !dry && st.Chunks > 0 {
		fmt.Fprintf(stdout, "  дальше: ollchat --graph-doctor %s; разбиение тем — --graph-communities %s\n", name, name)
	}
	return nil
}

// readChunkList читает номера кусков из файла: первое поле строки. Запись
// «12#*» означает все куски книги №12.
func readChunkList(file string) (map[uint64]bool, map[uint32]bool, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	keys := map[uint64]bool{}
	docs := map[uint32]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff"))
		if text == "" || strings.HasPrefix(text, "//") {
			continue
		}
		first := strings.Fields(text)[0]
		if doc, ok := strings.CutSuffix(first, "#*"); ok {
			n, err := strconv.ParseUint(doc, 10, 32)
			if err != nil || n == 0 {
				return nil, nil, fmt.Errorf("%s, строка %d: книга целиком записывается как «12#*», а не %q", file, line, first)
			}
			docs[uint32(n)] = true
			continue
		}
		k, err := graph.ParseChunkKey(first)
		if err != nil {
			return nil, nil, fmt.Errorf("%s, строка %d: %w", file, line, err)
		}
		keys[k.Pack()] = true
	}
	return keys, docs, sc.Err()
}
