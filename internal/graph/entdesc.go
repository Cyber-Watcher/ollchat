package graph

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Описания понятий: надстройка поверх графа, отдельным файлом.
//
// **Зачем они вообще.** У понятия есть имя, синонимы и выдержки, но описания
// нет: поле `Summary` заведено только у темы. «Essential GraphRAG» (Bratanic,
// Hane, 2025, стр. 116) описывает, как MS GraphRAG сливает упоминания сущности
// моделью в краткую сводку. На все 264 тысячи понятий это больше двух суток
// карты, поэтому описываются только хабы — понятия от `ChainHubLimit` связей,
// через которые и идёт вход в граф (на 16.09.2026 их 262).
//
// **Почему файлом рядом, а не полем в реестре.** Реестр `entities.jsonl`
// дописывается и уже дорастал до двух гигабайт; поле пришлось бы поддерживать
// в уплотнении, и старый бинарь терял бы его молча при следующем уплотнении.
// Описание — надстройка: его можно выбросить и пересчитать, в отличие от
// связей. Убрать файл — и граф прежний, ровно как со склейками.
//
// **Что с ними делает граф.** Ничего, пока не включено правилом
// `Rules.VectorDesc`: тогда описание идёт в текст вектора понятия следом
// за синонимами. Умолчание «выключено» нарочно — включение меняет векторы,
// а это замер, а не мелкая правка.

const entDescFile = "entdesc.jsonl"

// descMaxRunes — сколько знаков описания уходит в вектор.
//
// Замер 16.09.2026: средняя длина собранного описания 237 знаков, поэтому
// предел 240 оставляет почти все целиком, но страхует от простыни, которой
// модель однажды ответит вместо двух предложений. Длинный хвост размывает
// вектор ровно так же, как размывал его хвост синонимов (замер 03.09.2026).
const descMaxRunes = 240

// DescRec — одна запись файла описаний.
//
// Поля сверх пары «номер — текст» нужны человеку: через месяц по ним видно,
// какая модель и когда это написала, и стоит ли верить.
type DescRec struct {
	ID    uint32 `json:"id"`
	Name  string `json:"name,omitempty"`
	Desc  string `json:"desc"`
	Model string `json:"model,omitempty"`
	At    string `json:"at,omitempty"`
}

// Descriptions — описания понятий, прочитанные из файла.
type Descriptions struct {
	path    string
	byID    map[uint32]string
	problem string // почему файл прочитан не до конца; пусто — всё в порядке
}

// openDescriptions читает файл описаний. Отсутствие файла — обычное состояние:
// описания собираются отдельной работой и есть не у каждого графа.
func openDescriptions(dir string) (*Descriptions, error) {
	d := &Descriptions{
		path: filepath.Join(dir, entDescFile),
		byID: map[uint32]string{},
	}
	f, err := os.Open(d.path)
	if err != nil {
		if os.IsNotExist(err) {
			return d, nil
		}
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r DescRec
		// Оборванная последняя строка — не беда: файл дописывается.
		if json.Unmarshal(line, &r) != nil || r.ID == 0 {
			continue
		}
		text := strings.Join(strings.Fields(r.Desc), " ")
		if text == "" {
			continue
		}
		// Последняя запись о понятии побеждает: файл дописывается, и повторный
		// сбор описаний не должен требовать чистки прежних строк.
		d.byID[r.ID] = text
	}
	// Сбой чтения посреди файла (диск, строка длиннее мегабайта) раньше молча
	// обрывал загрузку: описаний становилось меньше, и узнать об этом было
	// неоткуда. Прочитанное остаётся — описания не повод не открыть граф, —
	// но причина сохраняется и видна через Problem (аудит 17.09.2026, Б17).
	if err := sc.Err(); err != nil {
		d.problem = fmt.Sprintf("%s прочитан не до конца (%v): описаний загружено %d", entDescFile, err, len(d.byID))
	}
	return d, nil
}

// Problem — почему описания загружены не все; пусто — всё в порядке.
func (d *Descriptions) Problem() string {
	if d == nil {
		return ""
	}
	return d.problem
}

// Of — описание понятия; пусто, если его нет.
func (d *Descriptions) Of(id uint32) string {
	if d == nil {
		return ""
	}
	return d.byID[id]
}

// Count — сколько понятий описано.
func (d *Descriptions) Count() int {
	if d == nil {
		return 0
	}
	return len(d.byID)
}

// Descriptions отдаёт описания понятий наружу — для доктора и замеров.
func (g *Graph) Descriptions() *Descriptions {
	if g == nil {
		return nil
	}
	return g.desc
}
