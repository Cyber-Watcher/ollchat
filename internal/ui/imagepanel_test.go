package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// Панель вложений (F3) показывает то, что действительно приложено к вопросу.
// Главное, что она чинит: стёртая в промпте метка больше не оставляет за собой
// ни висящего вложения, ни строчки о нём в состоянии.

func TestDeletedLabelDropsAttachmentAndStatus(t *testing.T) {
	m := newTestModel(t)
	visionModel(m)
	paste(t, m, 760, 220)

	if len(m.pending) != 1 || !strings.Contains(m.statusView(), "[Image01]") {
		t.Fatalf("подготовка: вложение должно быть видно в состоянии: %q", m.statusView())
	}

	// Стираем метку — ровно так, как это делает пользователь: клавишей.
	for range "[Image01] " {
		m.Update(pressKey(tea.KeyBackspace))
	}

	if len(m.pending) != 0 {
		t.Errorf("после удаления метки вложение должно исчезнуть, осталось %d", len(m.pending))
	}
	if strings.Contains(m.statusView(), "[Image01]") {
		t.Errorf("в состоянии осталась строка об удалённом вложении: %q", m.statusView())
	}
}

func TestF3TogglesImagePanel(t *testing.T) {
	m := newTestModel(t)
	visionModel(m)
	paste(t, m, 8, 8)

	m.Update(pressKey(tea.KeyF3))
	if m.images == nil {
		t.Fatal("F3 должен открывать панель вложений")
	}
	if view := m.View().Content; !strings.Contains(view, "[Image01]") {
		t.Error("в панели не видно вложения")
	}

	m.Update(pressKey(tea.KeyF3))
	if m.images != nil {
		t.Error("повторный F3 должен закрывать панель")
	}
}

// Панель показывает все вложения, а не только последнее.
func TestImagePanelListsAllAttachments(t *testing.T) {
	m := newTestModel(t)
	visionModel(m)
	paste(t, m, 8, 8)
	paste(t, m, 16, 16)
	paste(t, m, 32, 32)

	m.Update(pressKey(tea.KeyF3))
	view := m.images.view(m.width, m.pending)
	for _, want := range []string{"[Image01]", "[Image02]", "[Image03]"} {
		if !strings.Contains(view, want) {
			t.Errorf("в панели нет %s:\n%s", want, view)
		}
	}
}

// Del убирает вложение вместе с меткой в промпте — иначе в вопросе осталась бы
// ссылка в пустоту.
func TestDeleteRemovesAttachmentAndLabel(t *testing.T) {
	m := newTestModel(t)
	visionModel(m)
	paste(t, m, 8, 8)
	paste(t, m, 16, 16)
	typeText(m, "сравни их")

	m.Update(pressKey(tea.KeyF3))
	m.Update(pressKey(tea.KeyDelete)) // удаляем [Image01]

	if len(m.pending) != 1 {
		t.Fatalf("вложений осталось %d, ожидалось 1", len(m.pending))
	}
	if m.pending[0].label() != "[Image02]" {
		t.Errorf("удалили не то вложение, осталось %s", m.pending[0].label())
	}
	value := m.ta.Value()
	if strings.Contains(value, "[Image01]") {
		t.Errorf("метка удалённого вложения осталась в промпте: %q", value)
	}
	if !strings.Contains(value, "[Image02]") || !strings.Contains(value, "сравни их") {
		t.Errorf("удаление задело лишнее в промпте: %q", value)
	}
}

// Ctrl+D делает то же самое: клавиша не совпадает ни с какой другой по байту.
func TestCtrlDDeletesAttachment(t *testing.T) {
	m := newTestModel(t)
	visionModel(m)
	paste(t, m, 8, 8)

	m.Update(pressKey(tea.KeyF3))
	m.Update(pressCtrl('d'))

	if len(m.pending) != 0 {
		t.Errorf("Ctrl+D должен удалять вложение, осталось %d", len(m.pending))
	}
}

// Когда вложений не осталось, панель закрывается сама: показывать нечего.
func TestPanelClosesWhenLastAttachmentRemoved(t *testing.T) {
	m := newTestModel(t)
	visionModel(m)
	paste(t, m, 8, 8)

	m.Update(pressKey(tea.KeyF3))
	m.Update(pressKey(tea.KeyDelete))

	if m.images != nil {
		t.Error("после удаления последнего вложения панель должна закрыться")
	}
}

// Строка состояния: одно вложение — подробно, несколько — общей пометкой.
func TestStatusShowsOneAttachmentInDetailAndManyBriefly(t *testing.T) {
	m := newTestModel(t)
	visionModel(m)

	if m.imagesStatus() != "" {
		t.Errorf("без вложений в состоянии ничего быть не должно: %q", m.imagesStatus())
	}

	paste(t, m, 760, 220)
	one := m.imagesStatus()
	if !strings.Contains(one, "[Image01]") || !strings.Contains(one, "760×220") {
		t.Errorf("одно вложение показывается подробно, получено %q", one)
	}

	paste(t, m, 8, 8)
	if got := m.imagesStatus(); got != "[Images attached]" {
		t.Errorf("несколько вложений показываются пометкой [Images attached], получено %q", got)
	}
}

// О режиме мыши постоянной пометки в состоянии больше нет — только временное
// сообщение при переключении, и в нём нет двоеточия после слова «мышь».
func TestMouseStatusIsTransientAndWithoutColon(t *testing.T) {
	m := newTestModel(t)

	m.Update(pressKey(tea.KeyF2))
	status := m.statusView()
	if strings.Contains(status, "мышь: терминал") || strings.Contains(status, "(F2)") {
		t.Errorf("постоянной пометки о мыши быть не должно: %q", status)
	}
	if !strings.Contains(status, "мышь у терминала") {
		t.Errorf("в сообщении ожидалось «мышь у терминала» без двоеточия: %q", status)
	}
}

// Панель занимает строки экрана — их надо отнять у ленты, иначе экран
// перерастёт окно терминала.
func TestImagePanelKeepsLayoutWithinTerminal(t *testing.T) {
	m := newTestModel(t)
	visionModel(m)
	fillTranscript(m, 200)
	paste(t, m, 8, 8)
	paste(t, m, 16, 16)

	lines := func() int { return len(strings.Split(m.View().Content, "\n")) }
	if got := lines(); got != 30 {
		t.Fatalf("подготовка: экран занимает %d строк, а окно 30", got)
	}

	m.Update(pressKey(tea.KeyF3))
	if got := lines(); got != 30 {
		t.Errorf("с панелью экран занимает %d строк вместо 30", got)
	}

	m.Update(pressKey(tea.KeyF3))
	if got := lines(); got != 30 {
		t.Errorf("после закрытия панели экран занимает %d строк вместо 30", got)
	}
}

// Две панели над разделителем сразу не показываем.
func TestImagePanelAndFileMenuAreExclusive(t *testing.T) {
	m := newTestModel(t)
	visionModel(m)
	prepareTree(t, m)
	paste(t, m, 8, 8)

	typeText(m, " @")
	if m.files == nil {
		t.Fatal("подготовка: список файлов должен быть открыт")
	}

	m.Update(pressKey(tea.KeyF3))
	if m.files != nil {
		t.Error("открытие панели вложений должно закрывать список файлов")
	}
	if m.images == nil {
		t.Error("панель вложений должна открыться")
	}
}

// Пока возможности модели не получены, отказывать из-за незнания нельзя:
// так бывает в первые секунды после запуска и сразу после смены модели.
func TestUnknownCapabilitiesDoNotBlockImages(t *testing.T) {
	m := newTestModel(t)
	m.modelCaps = nil // сервер ещё не ответил

	paste(t, m, 8, 8)
	for _, b := range m.blocks {
		if strings.Contains(b.text, "vision") {
			t.Errorf("предупреждать про vision, не зная возможностей, нельзя: %q", b.text)
		}
	}

	if cmd := m.send("что тут " + m.pending[0].label()); cmd == nil {
		t.Error("отправка не должна отклоняться, пока возможности модели неизвестны")
	}
	if m.conv.Len() != 1 || len(m.conv.Messages()[0].Images) != 1 {
		t.Error("картинка должна уйти вместе с вопросом")
	}
}

// Сообщение «читаю буфер обмена…» не должно оставаться висеть после вставки.
func TestPasteClearsProgressMessage(t *testing.T) {
	m := newTestModel(t)
	visionModel(m)
	m.statusMsg = "читаю буфер обмена…"

	paste(t, m, 8, 8)

	if strings.Contains(m.statusMsg, "читаю") {
		t.Errorf("после вставки сообщение о чтении буфера должно исчезать: %q", m.statusMsg)
	}
}
