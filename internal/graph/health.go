package graph

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Быстрая проверка состояния графа — для приветствия при запуске.
//
// **Почему не graph.Open.** Открытие графа коллекции books стоит 25 секунд
// и четверть гигабайта памяти: столько ждать запуск программы нельзя, и человек
// справедливо решит, что она зависла. Поэтому читаются только паспорта —
// `graph.meta` (301 байт), `entities.vecmeta` (62 байта) и **начало**
// `communities.json`, где поле «entities» стоит до списка тем. Вместе это
// килобайт чтения и никакой работы.
//
// **Зачем вообще.** Всё, что здесь проверяется, портится молча. Ошибок нет,
// просто ответы тихо становятся хуже: понятия, добавленные после последнего
// счёта векторов, находятся лишь точным написанием; куски долитых книг ищутся
// одними словами; понятия, появившиеся после разметки тем, не попадают
// в обзор вовсе. Замер 02.09.2026: 101 тысяча понятий из 161 оказалась вне тем,
// и обзор несколько дней работал по трети графа, ни разу об этом не сказав.

// Health — состояние графа в числах. Пустая структура означает, что графа нет.
type Health struct {
	Model string // чем извлекались понятия

	// Kind — рабочий граф или опытный. У опытного советов не спрашивают:
	// он неполон по замыслу.
	Kind Kind
	// Note — чем этот граф отличается от рабочего.
	Note string

	Chunks  int // кусков в коллекции сейчас
	Covered int // из них разобрано графом

	Entities int // понятий в графе
	Vectors  int // из них со смыслами

	// TopicsBuiltAt — сколько было понятий, когда размечались темы. 0 — тем нет.
	TopicsBuiltAt int

	// RegistryBytes — размер файла реестра понятий. Реестр дозаписывается, и
	// каждое обновление счётчиков кладёт в конец новую копию записи; за недели
	// сборки перевес доходит до двадцатикратного и оплачивается при каждом
	// открытии графа. Отношение размера к числу понятий это и показывает.
	RegistryBytes int64
}

// registryBloat — во сколько раз реестр толще уплотнённого.
//
// Мера — байт на понятие. Уплотнённый реестр даёт около 160 байт на понятие
// (замер 02.09.2026: 161 239 понятий в 25 МБ), раздутый — около 3.2 КБ
// (те же понятия в 499 МБ). Отношение к 160 и есть перевес.
func (h Health) registryBloat() float64 {
	if h.Entities == 0 || h.RegistryBytes == 0 {
		return 0
	}
	return float64(h.RegistryBytes) / float64(h.Entities) / 160
}

// QuickHealth читает паспорта графа. Второе значение — есть ли граф вообще.
func QuickHealth(collDir, name string, chunks int) (Health, bool) {
	dir := filepath.Join(collDir, DirFor(name))
	b, err := os.ReadFile(filepath.Join(dir, metaFile))
	if err != nil {
		return Health{}, false
	}
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return Health{}, false
	}
	h := Health{Model: m.Model, Kind: m.Kind, Note: m.Note,
		Chunks: chunks, Covered: m.Covered, Entities: m.Entities}
	if h.Chunks == 0 {
		h.Chunks = m.Chunks
	}

	if b, err := os.ReadFile(filepath.Join(dir, entVecMetaFile)); err == nil {
		var vm entVecMeta
		if json.Unmarshal(b, &vm) == nil {
			h.Vectors = vm.Count
		}
	}
	h.TopicsBuiltAt = topicsBuiltAt(filepath.Join(dir, CommunityFile))
	if fi, err := os.Stat(filepath.Join(dir, entitiesFile)); err == nil {
		h.RegistryBytes = fi.Size()
	}
	return h, true
}

// topicsBuiltAt достаёт число понятий на момент разметки, не читая список тем.
//
// Файл разбиения весит десятки мегабайт, а нужное поле лежит в его начале —
// читаем первый килобайт и ищем «entities». Не нашлось — значит формат
// изменился, и лучше промолчать, чем соврать.
func topicsBuiltAt(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	head := make([]byte, 4096)
	n, _ := io.ReadFull(f, head)
	if n == 0 {
		return 0
	}
	head = head[:n]

	// Обрезаем ровно до ключа «list» и закрываем объект: всё, что до него, —
	// это паспорт разбиения, а сам список весит десятки мегабайт.
	//
	// Первая редакция просто дописывала скобки к первому килобайту и получала
	// неразбираемый огрызок: килобайт кончается уже внутри списка тем. Ошибка
	// была тихой — программа говорила «темы не размечены» там, где их 34 тысячи.
	i := bytes.Index(head, []byte(`"list"`))
	if i < 0 {
		return 0
	}
	body := bytes.TrimRight(bytes.TrimSpace(head[:i]), ",\n\t ")
	var probe struct {
		Entities int `json:"entities"`
	}
	if json.Unmarshal(append(body, '}'), &probe) != nil {
		return 0
	}
	return probe.Entities
}

// Advice — что не в порядке и какой командой чинится. Пусто — всё хорошо.
//
// Порядок советов повторяет порядок работ: сперва разбор, потом разметка тем,
// потом смыслы. Каждый совет — готовая команда, а не намёк.
func (h Health) Advice(collection string) []string {
	var out []string
	if h.Entities == 0 {
		return nil // графа ещё нет, советовать нечего
	}
	// Опытный граф неполон по замыслу: он собирается по одному каталогу ради
	// сравнения. Советовать «доразобрать библиотеку» здесь — значит приучить
	// не читать советов вовсе.
	if h.Kind == KindExperimental {
		return nil
	}
	if h.Chunks > h.Covered {
		out = append(out, fmt.Sprintf(
			"граф собран по %d%% библиотеки (%d кусков из %d) — какие каталоги остались, скажет ollchat --graph-doctor %s",
			100*h.Covered/max(h.Chunks, 1), h.Covered, h.Chunks, collection))
	}
	// Раздутый реестр не портит граф, но оплачивается временем при каждом
	// открытии: 41 с против 2.7 с на библиотеке из 465 тыс. кусков.
	// Порог втрое — ниже него овчинка выделки не стоит.
	if b := h.registryBloat(); b >= 3 {
		out = append(out, fmt.Sprintf(
			"реестр понятий раздут примерно в %.0f раз (%d МБ на %d понятий) — граф оттого открывается долго: ollchat --graph-compact %s",
			b, h.RegistryBytes>>20, h.Entities, collection))
	}
	if h.Vectors < h.Entities {
		out = append(out, fmt.Sprintf(
			"%d понятий без смыслов — находятся только точным написанием: ollchat --graph-embed %s",
			h.Entities-h.Vectors, collection))
	}
	switch {
	case h.TopicsBuiltAt == 0:
		out = append(out, fmt.Sprintf(
			"темы графа не размечены — обзор тем работать не будет: ollchat --graph-communities %s", collection))
	case repartitionOverdue(h.Entities, h.TopicsBuiltAt):
		out = append(out, fmt.Sprintf(
			"после разметки тем добавилось %d понятий (было %d, стало %d) — они не входят ни в одну тему: ollchat --graph-doctor %s",
			h.Entities-h.TopicsBuiltAt, h.TopicsBuiltAt, h.Entities, collection))
	}
	return out
}

// repartitionOverdue — считать ли разметку тем отставшей.
//
// Порог тот же, что у доктора: десятая часть понятий. Ниже неё пересчёт стоит
// дороже пользы — сама разметка секундная, но следом идут часы карты на описания.
//
// Здесь считается по приросту с момента разметки, а не по точному числу понятий
// вне тем: точное требует прочесть список тем целиком, то есть десятки мегабайт
// при каждом запуске. Прирост — верхняя оценка того же самого, и для
// предупреждения её достаточно; точное число скажет доктор.
func repartitionOverdue(now, builtAt int) bool {
	if builtAt <= 0 || now <= builtAt {
		return false
	}
	return 100*(now-builtAt)/now >= 10
}
