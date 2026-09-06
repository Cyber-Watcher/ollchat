package graph

import (
	"os"
	"path/filepath"
	"testing"
)

// vecsAt — векторы понятий в отдельном каталоге, посчитанные наполовину.
func vecsAt(t *testing.T, dir, model, digest string, dim, count int) *EntityVectors {
	t.Helper()
	v := &EntityVectors{dir: dir}
	data := make([]int8, count*dim)
	for i := range data {
		data[i] = int8(i % 100)
	}
	if err := v.save(model, digest, dim, data); err != nil {
		t.Fatalf("первая запись векторов: %v", err)
	}
	return v
}

// Дозапись добавляет хвост и не переписывает посчитанное.
//
// Ради этого всё и делается: файл на 178 МБ (замер 06.09.2026), и переписывать
// его пачками по ходу недельной сборки значит сделать переписывание одного
// и того же основной работой на диске.
func TestAppendAddsTail(t *testing.T) {
	dir := t.TempDir()
	const dim = 4
	v := vecsAt(t, dir, "bge-m3", "sha256:aaa", dim, 3)
	before, err := os.ReadFile(filepath.Join(dir, entVecDataFile))
	if err != nil {
		t.Fatal(err)
	}

	tail := []int8{1, 2, 3, 4, 5, 6, 7, 8} // два понятия
	if err := v.appendVectors("bge-m3", "sha256:aaa", dim, tail); err != nil {
		t.Fatalf("дозапись: %v", err)
	}
	if v.Count() != 5 {
		t.Errorf("понятий в паспорте %d, ожидалось 5", v.Count())
	}

	after, err := os.ReadFile(filepath.Join(dir, entVecDataFile))
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before)+len(tail) {
		t.Fatalf("файл вырос на %d байт вместо %d", len(after)-len(before), len(tail))
	}
	// Прежняя часть обязана остаться байт в байт: дозапись не трогает
	// посчитанное, и это её единственный смысл.
	for i := range before {
		if after[i] != before[i] {
			t.Fatalf("прежние данные изменились на месте %d", i)
		}
	}

	// Файл переоткрывается с диска: паспорт, сумма и данные должны сойтись.
	again := openEntityVectors(dir)
	if p := again.Problem(); p != "" {
		t.Fatalf("после дозаписи файл не принят: %s", p)
	}
	if again.Count() != 5 {
		t.Errorf("после переоткрытия понятий %d, ожидалось 5", again.Count())
	}
	if c, ok := again.cosine(4, 4); !ok || c == 0 {
		t.Errorf("дописанное понятие не читается: cos=%.3f ok=%v", c, ok)
	}
}

// Оборвавшаяся дозапись не портит файл: лишний хвост читается как небывший.
//
// Данные пишутся первыми, паспорт вторым, и точка фиксации — паспорт.
// Без терпимости к хвосту один обрыв питания стоил бы недель счёта.
func TestTornAppendIsTolerated(t *testing.T) {
	dir := t.TempDir()
	const dim = 4
	vecsAt(t, dir, "bge-m3", "", dim, 3)

	// Дописываем мусор мимо паспорта — ровно то, что оставит обрыв.
	path := filepath.Join(dir, entVecDataFile)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte{9, 9, 9, 9, 9}); err != nil {
		t.Fatal(err)
	}
	f.Close()

	v := openEntityVectors(dir)
	if p := v.Problem(); p != "" {
		t.Fatalf("оборванная дозапись объявлена порчей: %s", p)
	}
	if !v.Ready() || v.Count() != 3 {
		t.Fatalf("понятий %d, ожидалось 3 (хвост не в счёт)", v.Count())
	}

	// Следующая дозапись обязана срезать мусор, а не приписать к нему.
	if err := v.appendVectors("bge-m3", "", dim, []int8{1, 2, 3, 4}); err != nil {
		t.Fatalf("дозапись после обрыва: %v", err)
	}
	again := openEntityVectors(dir)
	if p := again.Problem(); p != "" {
		t.Fatalf("после дозаписи поверх обрыва файл не принят: %s", p)
	}
	if again.Count() != 4 {
		t.Errorf("понятий %d, ожидалось 4", again.Count())
	}
}

// Файл **короче** паспорта — это настоящая порча, и о ней говорится.
func TestShortFileIsAProblem(t *testing.T) {
	dir := t.TempDir()
	const dim = 4
	vecsAt(t, dir, "bge-m3", "", dim, 3)

	path := filepath.Join(dir, entVecDataFile)
	if err := os.Truncate(path, 4); err != nil {
		t.Fatal(err)
	}
	v := openEntityVectors(dir)
	if v.Ready() {
		t.Fatal("обрезанный файл принят как годный")
	}
	if v.Problem() == "" {
		t.Error("о порче не сказано ни слова")
	}
}

// Другая модель или другие её веса — отказ, а не молчаливая склейка.
//
// Векторы разных пространств, склеенные в один файл, **по выдаче не видны**:
// поиск продолжает отвечать, просто хуже.
func TestAppendRefusesForeignWeights(t *testing.T) {
	const dim = 4
	tail := []int8{1, 2, 3, 4}

	t.Run("другая модель", func(t *testing.T) {
		v := vecsAt(t, t.TempDir(), "bge-m3", "sha256:aaa", dim, 3)
		if err := v.appendVectors("other-model", "sha256:aaa", dim, tail); err == nil {
			t.Error("векторы другой модели дописаны молча")
		}
	})
	t.Run("другие веса", func(t *testing.T) {
		v := vecsAt(t, t.TempDir(), "bge-m3", "sha256:aaa", dim, 3)
		if err := v.appendVectors("bge-m3", "sha256:bbb", dim, tail); err == nil {
			t.Error("векторы других весов дописаны молча")
		}
	})
	t.Run("другая размерность", func(t *testing.T) {
		v := vecsAt(t, t.TempDir(), "bge-m3", "sha256:aaa", dim, 3)
		if err := v.appendVectors("bge-m3", "sha256:aaa", 8, []int8{1, 2, 3, 4, 5, 6, 7, 8}); err == nil {
			t.Error("векторы другой размерности дописаны молча")
		}
	})
}

// Пустой отпечаток с любой стороны сверку пропускает, а не заваливает.
//
// Его нет у паспортов старого образца и у серверов, которые его не отдают;
// отказ работать из-за отсутствующего поля хуже, чем несделанная проверка.
func TestDigestCheckSkipsWhenUnknown(t *testing.T) {
	cases := []struct {
		name       string
		have, got  string
		wantErrNil bool
	}{
		{"оба пусты", "", "", true},
		{"паспорт старый", "", "sha256:bbb", true},
		{"сервер молчит", "sha256:aaa", "", true},
		{"совпали", "sha256:aaa", "sha256:aaa", true},
		{"разошлись", "sha256:aaa", "sha256:bbb", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkDigest(c.have, c.got, "bge-m3")
			if (err == nil) != c.wantErrNil {
				t.Errorf("checkDigest(%q, %q) = %v", c.have, c.got, err)
			}
		})
	}
}

// Отпечаток, которого в паспорте не было, дозапись проставляет.
func TestAppendFillsMissingDigest(t *testing.T) {
	dir := t.TempDir()
	const dim = 4
	v := vecsAt(t, dir, "bge-m3", "", dim, 3)

	if err := v.appendVectors("bge-m3", "sha256:aaa", dim, []int8{1, 2, 3, 4}); err != nil {
		t.Fatalf("дозапись: %v", err)
	}
	if got := openEntityVectors(dir).Digest(); got != "sha256:aaa" {
		t.Errorf("отпечаток в паспорте %q, ожидался sha256:aaa", got)
	}
}

// Дозапись в пустое место — обычная первая запись, а не ошибка.
func TestAppendToEmptyIsFirstWrite(t *testing.T) {
	dir := t.TempDir()
	v := &EntityVectors{dir: dir}
	if err := v.appendVectors("bge-m3", "sha256:aaa", 4, []int8{1, 2, 3, 4}); err != nil {
		t.Fatalf("первая дозапись: %v", err)
	}
	if openEntityVectors(dir).Count() != 1 {
		t.Error("первая дозапись не создала файл")
	}
}
