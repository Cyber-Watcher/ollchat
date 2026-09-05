package tools

import "testing"

// В наборе службы не может быть инструмента, меняющего машину.
//
// **Это про безопасность, а не про порядок.** Служба знаний работает без
// человека: подтвердить опасное действие некому, а ключ доступа есть у всех
// сотрудников. `bash`, открытый в сеть, — это чужие руки на сервере.
//
// Ловушка уже сработала однажды: `ollchat --serve --mcp` в первой редакции
// раздал весь реестр диалога — семнадцать инструментов вместе с bash
// и write_file. Поймано живой проверкой; здесь закреплено, чтобы не вернулось.
func TestServedToolsAreReadOnly(t *testing.T) {
	changesMachine := map[string]bool{
		NameBash: true, NameWriteFile: true, NameEditFile: true,
		NameReadFile: true, NameListDir: true, NameGrep: true,
		NameViewImage: true, NameHTTPFetch: true, NameConfluence: true,
	}
	for _, name := range ReadOnlyNames() {
		if changesMachine[name] {
			t.Errorf("в наборе службы инструмент, которому там не место: %s", name)
		}
	}
	if len(ReadOnlyNames()) == 0 {
		t.Fatal("набор службы пуст — раздавать нечего")
	}
}

// Subset отдаёт те же самые инструменты, а не пересобранные.
//
// Пересобранные имели бы свои настройки, и служба отвечала бы не так,
// как диалог на той же машине.
func TestSubsetKeepsSameToolInstances(t *testing.T) {
	full, err := NewRegistry([]string{NameKBSearch, NameGraphSearch}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := full.Subset([]string{NameKBSearch})
	if err != nil {
		t.Fatal(err)
	}
	if len(sub.Names()) != 1 || sub.Names()[0] != NameKBSearch {
		t.Errorf("в подмножестве %v", sub.Names())
	}
	if sub.tools[NameKBSearch] != full.tools[NameKBSearch] {
		t.Error("инструмент пересобран, а должен быть тот же самый")
	}
	if sub.Has(NameGraphSearch) {
		t.Error("в подмножество попал невыбранный инструмент")
	}
}

// Просить невключённый инструмент — ошибка, а не тихий пропуск.
func TestSubsetRefusesUnknown(t *testing.T) {
	full, err := NewRegistry([]string{NameKBSearch}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := full.Subset([]string{NameBash}); err == nil {
		t.Error("ожидался отказ на невключённый инструмент")
	}
}
