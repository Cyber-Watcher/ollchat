package pdf

import (
	"bytes"
	"errors"
	"math/rand"
	"strings"
	"testing"
	"time"
)

// TestDamagedFilesDoNotPanic — главная проверка устойчивости: разбор чужих
// данных не имеет права ронять программу.
//
// До появления перехвата любой из этих случаев валил всё приложение вместе
// с историей диалога, а при обходе тысяч книг остановил бы работу на середине.
// Ошибка вместо паники — правильный ответ: повреждённый документ ничем не
// отличается от нечитаемого.
func TestDamagedFilesDoNotPanic(t *testing.T) {
	good := docWith(
		"/F1 5 0 R",
		"BT /F1 12 Tf 72 720 Td (Hello World) Tj ET",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
	)

	cases := []struct {
		name string
		data []byte
	}{
		{"обрезан на половине", good[:len(good)/2]},
		{"обрезан почти сразу", good[:40]},
		{"только заголовок", []byte("%PDF-1.7\n")},
		{"мусор после заголовка", append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte{0xFF, 0x00, 0xA5}, 400)...)},
		{"длина потока врёт в большую сторону", bytes.Replace(good, []byte("/Length"), []byte("/Length 999999 %"), 1)},
		{"смещения объектов сбиты", bytes.ReplaceAll(good, []byte(" 0 obj"), []byte(" 0 obX"))},
		{"словарь не закрыт", bytes.ReplaceAll(good, []byte(">>"), []byte("><"))},
		{"ссылка на несуществующий объект", bytes.ReplaceAll(good, []byte("5 0 R"), []byte("999 0 R"))},
		{"отрицательные размеры", bytes.ReplaceAll(good, []byte("/Width 2"), []byte("/Width -2147483648"))},
		{"глубокая вложенность массивов", []byte("%PDF-1.7\n1 0 obj\n" + strings.Repeat("[", 20000) + "\nendobj\ntrailer\n<< /Root 1 0 R >>")},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Паника внутри провалит тест сама по себе: она уронит горутину теста.
			if _, err := Extract(c.data, Options{}); err == nil {
				t.Log("разбор неожиданно удался — это допустимо, лишь бы не паника")
			}
			if _, err := ExtractImages(c.data, ImageOptions{}); err == nil {
				t.Log("картинки неожиданно извлеклись — тоже допустимо")
			}
			if _, err := Open(c.data); err == nil {
				t.Log("документ открылся")
			}
		})
	}
}

// TestRandomlyCorruptedDoesNotPanic портит исправный документ в случайных местах:
// так находятся ветки, до которых не додумаешься вручную.
func TestRandomlyCorruptedDoesNotPanic(t *testing.T) {
	good := docWith(
		"/F1 5 0 R",
		"BT /F1 12 Tf 72 720 Td <00030004> Tj ET",
		"<< /Type /Font /Subtype /Type0 /BaseFont /X /Encoding /Identity-H /ToUnicode 6 0 R >>",
		stream("", "begincmap\n1 beginbfchar\n<0003> <0414>\nendbfchar\nendcmap"),
	)

	rnd := rand.New(rand.NewSource(20260815))
	for i := 0; i < 300; i++ {
		data := append([]byte(nil), good...)
		// От одной до двадцати испорченных позиций.
		for n := rnd.Intn(20) + 1; n > 0; n-- {
			data[rnd.Intn(len(data))] = byte(rnd.Intn(256))
		}
		if _, err := Extract(data, Options{}); err != nil && errors.Is(err, ErrDamaged) {
			// Паника поймана и превращена в ошибку — ровно то, что нужно.
			continue
		}
	}
}

// TestDamagedErrorIsRecognizable проверяет, что сорвавшийся разбор отличим:
// вызывающий код должен уметь отделить повреждённый файл от прочих ошибок.
func TestDamagedErrorIsRecognizable(t *testing.T) {
	var err error
	func() {
		defer catch("проверка", &err)
		panic("нарочно")
	}()
	if !errors.Is(err, ErrDamaged) {
		t.Fatalf("ошибка не опознаётся как повреждение: %v", err)
	}
	if !strings.Contains(err.Error(), "нарочно") {
		t.Fatalf("в ошибке нет причины: %v", err)
	}
	if !strings.Contains(err.Error(), "проверка") {
		t.Fatalf("в ошибке нет места сбоя: %v", err)
	}
}

// TestParserLimitsBoundWork закрепляет исправление зависания, найденного
// обстрелом настоящих книг: разбор 33-мегабайтного испорченного файла не
// завершался за десять минут. Причин было две — незакрытая скобка заставляла
// набивать массив миллионами элементов, а поиск конца потока наивным перебором
// давал квадратичное время.
func TestParserLimitsBoundWork(t *testing.T) {
	const big = 8 << 20 // 8 МиБ мусора — этого хватало, чтобы разбор встал

	cases := []struct {
		name string
		data []byte
	}{
		{
			"незакрытый массив на весь файл",
			[]byte("%PDF-1.7\n1 0 obj\n[" + strings.Repeat("1 2 3 ", big/6) + "\nendobj\ntrailer\n<< /Root 1 0 R >>"),
		},
		{
			"незакрытый словарь на весь файл",
			[]byte("%PDF-1.7\n1 0 obj\n<< " + strings.Repeat("/K 1 ", big/5) + "\nendobj\ntrailer\n<< /Root 1 0 R >>"),
		},
		{
			"глубокая вложенность",
			[]byte("%PDF-1.7\n1 0 obj\n" + strings.Repeat("[", 50000) + "\nendobj\ntrailer\n<< /Root 1 0 R >>"),
		},
		{
			"много потоков с враньём в длине",
			[]byte("%PDF-1.7\n" + strings.Repeat(
				"1 0 obj\n<< /Length 999999999 >>\nstream\nмусор без конца потока\n", 4000) +
				"trailer\n<< /Root 1 0 R >>"),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			done := make(chan struct{})
			go func() {
				defer close(done)
				Extract(c.data, Options{})
			}()
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				t.Fatal("разбор не уложился в 20 секунд — работа снова не ограничена")
			}
		})
	}
}

// TestArrayLengthCapped проверяет сам предел: испорченный массив обрывается,
// а не растёт до размеров файла.
func TestArrayLengthCapped(t *testing.T) {
	p := newParser([]byte("["+strings.Repeat("1 ", maxArrayItems+5000)), nil)
	obj, err := p.object()
	if err != nil {
		t.Fatal(err)
	}
	arr, ok := obj.(Array)
	if !ok {
		t.Fatalf("ожидался массив, получено %T", obj)
	}
	if len(arr) > maxArrayItems {
		t.Fatalf("массив не ограничен: %d элементов", len(arr))
	}
}

// TestNormalArraysUnaffected — предел не должен мешать настоящим документам:
// у составных шрифтов массив ширин глифов бывает в тысячи элементов.
func TestNormalArraysUnaffected(t *testing.T) {
	const n = 20000
	p := newParser([]byte("["+strings.Repeat("500 ", n)+"]"), nil)
	obj, err := p.object()
	if err != nil {
		t.Fatal(err)
	}
	if arr, _ := obj.(Array); len(arr) != n {
		t.Fatalf("массив из %d элементов разобран как %d", n, len(arr))
	}
}
