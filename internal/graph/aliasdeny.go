package graph

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Запрет синонима (этап 104, Ж1.5).
//
// **Беда.** Синоним понятия — ещё и ключ реестра: имя из ответа модели,
// совпавшее с синонимом, приводит к этому понятию. Ложный синоним поэтому
// работает воронкой. Перепись 17.09.2026 нашла такие на живом графе:
// «cluster» вёл в Kubernetes (789 кусков — и кластеры Kafka, и Redis),
// «указатель» — в credentials (соседи понятия: «адрес памяти», nil, heap),
// «Linux» — в Kali Linux, «ИИ» — в ChatGPT, «go» — в goroutine, имена «John»
// и «Dave» — в конкретных авторов. И каждая следующая сборка лила бы туда же.
//
// **Почему журнал, а не правка записи.** Убрать синоним из записи мало: модель
// предложит его снова на следующем же куске, и mergeAliases вернёт его на
// место. Запрет должен переживать сборки. Поэтому он лежит отдельным журналом
// рядом с графом, читается при каждом открытии реестра и действует в трёх
// местах: синоним не становится ключом, не показывается в карточке (и не
// попадает в текст вектора), не дописывается заново.
//
// **Обратимо.** Журнал только дополняется; запись с `undo` снимает запрет.
// Упоминания и связи, уже приписанные по ложному ключу, запрет не трогает —
// их убирает --graph-forget-chunks, а возвращает на место следующая сборка.

const aliasDenyFile = "alias-deny.jsonl"

// AliasDeny — одна запись журнала запретов.
type AliasDeny struct {
	ID    uint32 `json:"id"`
	Alias string `json:"alias"`
	Why   string `json:"why,omitempty"`
	By    string `json:"by,omitempty"`
	At    int64  `json:"at"`
	Undo  bool   `json:"undo,omitempty"` // снять ранее поставленный запрет
}

// loadAliasDeny читает журнал: номер понятия → нормализованные запрещённые
// синонимы. Нет файла — нет запретов. Битая строка пропускается, как в реестре.
func loadAliasDeny(dir string) map[uint32]map[string]bool {
	f, err := os.Open(filepath.Join(dir, aliasDenyFile))
	if err != nil {
		return nil
	}
	defer f.Close()
	deny := map[uint32]map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var r AliasDeny
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil || r.ID == 0 {
			continue
		}
		k := Normalize(r.Alias)
		if k == "" {
			continue
		}
		if r.Undo {
			delete(deny[r.ID], k)
			continue
		}
		if deny[r.ID] == nil {
			deny[r.ID] = map[string]bool{}
		}
		deny[r.ID][k] = true
	}
	return deny
}

// allowedAliases убирает из списка запрещённые для этого понятия синонимы.
func (e *Entities) allowedAliases(id uint32, aliases []string) []string {
	banned := e.deny[id]
	if len(banned) == 0 {
		return aliases
	}
	out := aliases[:0:0]
	for _, a := range aliases {
		if !banned[Normalize(a)] {
			out = append(out, a)
		}
	}
	return out
}

// DenyEffect — что изменит один запрет: чей был ключ и чьим станет.
type DenyEffect struct {
	Rec           AliasDeny
	Name          string // имя понятия
	Key           string // ключ, который освобождается
	OwnerBefore   string // кому ключ принадлежал до запрета
	OwnerAfter    string // кому достанется («—» — никому)
	WasKey        bool   // был ли синоним ключом вообще
	OwnerBeforeID uint32
	OwnerAfterID  uint32
}

// DenyAliases дописывает запреты в журнал графа в каталоге dir. dry — только
// показать последствия. Граф обязан быть закрыт, сборка — не идти.
//
// Опечатка не проходит молча: понятия с таким номером нет или у него нет
// такого синонима — ошибка до любой записи.
func DenyAliases(dir string, recs []AliasDeny, dry bool) ([]DenyEffect, error) {
	lock := filepath.Join(dir, lockFile)
	if _, err := os.Stat(lock); err == nil {
		if owner := readLock(lock); owner.alive() {
			return nil, &LockedError{Path: lock, PID: owner.PID, Since: owner.Since}
		}
	}
	path := filepath.Join(dir, entitiesFile)
	before, err := readEntitiesFile(path)
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		ent, ok := before.Get(r.ID)
		if !ok {
			return nil, fmt.Errorf("понятия #%d в реестре нет", r.ID)
		}
		if r.Undo {
			continue
		}
		found := false
		for _, a := range ent.Aliases {
			if Normalize(a) == Normalize(r.Alias) {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("у понятия #%d %q нет синонима %q (есть: %s)", r.ID, ent.Name, r.Alias, strings.Join(ent.Aliases, ", "))
		}
	}

	// Последствия считаются честно: реестр читается заново с будущим журналом.
	after := &Entities{stemMinLen: DefaultStemMinLen, path: path, byKey: map[string]uint32{}, byStem: map[string]uint32{}}
	after.extraDeny = recs
	if err := after.load(nil); err != nil {
		return nil, err
	}
	var effects []DenyEffect
	for _, r := range recs {
		ent, _ := before.Get(r.ID)
		k := Normalize(r.Alias)
		ef := DenyEffect{Rec: r, Name: ent.Name, Key: k, OwnerAfter: "—", OwnerBefore: "—"}
		if id, ok := before.byKey[k]; ok {
			o, _ := before.Get(id)
			ef.OwnerBefore, ef.OwnerBeforeID = o.Name, id
			ef.WasKey = id == r.ID
		}
		if id, ok := after.byKey[k]; ok {
			o, _ := after.Get(id)
			ef.OwnerAfter, ef.OwnerAfterID = o.Name, id
		}
		effects = append(effects, ef)
	}
	if dry {
		return effects, nil
	}

	f, err := os.OpenFile(filepath.Join(dir, aliasDenyFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	now := time.Now().Unix()
	for _, r := range recs {
		if r.At == 0 {
			r.At = now
		}
		line, err := json.Marshal(r)
		if err != nil {
			return nil, err
		}
		w.Write(line)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		return nil, err
	}
	if err := f.Sync(); err != nil {
		return nil, err
	}
	syncDir(dir)
	return effects, nil
}
