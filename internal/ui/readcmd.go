package ui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/find"
)

// Команда /read — показать найденный кусок целиком, вместе с соседями.
//
// **Зачем соседи.** Кусок нарезан по размеру, а не по смыслу: мысль часто
// начинается в предыдущем и кончается в следующем. Показывать один кусок —
// значит обрывать её с обеих сторон. Сколько брать, задаёт `kb.read_around`.
//
// **Разбор идентификатора не пишется заново.** `kb.Collection.Around` уже
// разбирает `books/12#37` и сам объясняет, что не так; своя копия разошлась бы
// с ней при первой же правке формата.

const readUsage = `использование: /read <id|номер>
  /read books/12#37   кусок по ссылке из выдачи
  /read 3             третий из последней выдачи /search`

// readCmd показывает кусок целиком.
func (m *Model) readCmd(arg string) tea.Cmd {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		m.addBlock(block{kind: blockError, text: readUsage})
		return nil
	}

	// Номер из последней выдачи: в ленте она пронумерована, и человек говорит
	// «третий», а не «books/12#37».
	id := arg
	if n, err := strconv.Atoi(arg); err == nil {
		if len(m.finds) == 0 {
			m.addBlock(block{kind: blockError, text: "выдачи ещё не было: /read по номеру работает после /search"})
			return nil
		}
		if n < 1 || n > len(m.finds) {
			m.addBlock(block{kind: blockError, text: fmt.Sprintf(
				"в последней выдаче %d выдержек, а просят %d", len(m.finds), n)})
			return nil
		}
		id = m.finds[n-1].ID
	}

	coll, err := m.kbCollection(collOfID(id))
	if err != nil {
		m.fail("/read", err)
		return nil
	}
	hits, err := coll.Around(id, m.cfg.KB.ReadAroundCount())
	if err != nil {
		m.fail("/read", err)
		return nil
	}
	if len(hits) == 0 {
		m.addBlock(block{kind: blockNotice, text: "куска " + id + " в коллекции нет"})
		return nil
	}

	var b strings.Builder
	for i, h := range hits {
		e := find.Excerpt{
			ID: h.ID, Book: h.Book, Author: h.Author, Year: h.Year,
			Unit: h.Unit, From: h.UnitFrom, To: h.UnitTo,
		}
		if i > 0 {
			b.WriteString("\n")
		}
		// Запрошенный кусок отмечен: соседи показаны ради связности, но читать
		// человек шёл именно этот.
		mark := "  "
		if h.ID == id {
			mark = "▸ "
		}
		fmt.Fprintf(&b, "%s%s\n%s\n", mark, find.Line(e), strings.TrimSpace(h.Text))
	}
	// Прочитанное показывается сразу: человек попросил именно это.
	m.addBlockAndShow(block{kind: blockNotice, text: strings.TrimRight(b.String(), "\n")})
	return nil
}

// collOfID — имя коллекции из ссылки «books/12#37».
//
// Пустое значение означает «выбранная»: так /read работает и с голым номером,
// и со ссылкой на другую коллекцию, если человек её назвал.
func collOfID(id string) string {
	if i := strings.IndexByte(id, '/'); i > 0 {
		return id[:i]
	}
	return ""
}
