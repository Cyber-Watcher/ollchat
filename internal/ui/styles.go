package ui

import "charm.land/lipgloss/v2"

// Палитра. Цвета заданы кодами ANSI 256, чтобы одинаково выглядеть
// в большинстве терминалов, включая подключение по ssh.
var (
	colAccent = lipgloss.Color("39")  // голубой — служебные акценты
	colUser   = lipgloss.Color("212") // розовый — маркер вопроса пользователя
	// Текст вопроса бледно-зелёный: при быстрой прокрутке ленты свои вопросы
	// должны находиться глазом сразу, не читая текст.
	colUserText = lipgloss.Color("157")
	colModel    = lipgloss.Color("36")  // бирюзовый — имя модели
	colDim      = lipgloss.Color("244") // приглушённый текст
	colFaint    = lipgloss.Color("240") // рамки и разделители
	colOK       = lipgloss.Color("42")  // успех
	colWarn     = lipgloss.Color("214") // предупреждение
	colError    = lipgloss.Color("203") // ошибка
	colTool     = lipgloss.Color("147") // вызовы инструментов
	colThink    = lipgloss.Color("245") // рассуждения модели

	// Подсказка о расхождении конфига со сборкой. Оттенок между оранжевым
	// и красным: заметнее приглушённой заметки, но это не ошибка и не тревога,
	// поэтому не 214 (предупреждение) и не 203 (ошибка).
	colHint = lipgloss.Color("216") // бледно-оранжевый с розовым отливом

	// Названия книг в отчётах о коллекции. Светло-жёлтый выбран за то, что
	// в палитре ленты жёлтого больше нигде нет: в списке из сотни строк, где
	// вперемешку идут пути, пояснения и советы, название книги должно
	// находиться глазом без чтения.
	colBook = lipgloss.Color("229") // светло-жёлтый — названия книг

	colNameBright = lipgloss.Color("215") // светло-оранжевый — имена в списках
	colNamePale   = lipgloss.Color("180") // бледно-оранжевый — пояснения в списках
	colRowSel     = lipgloss.Color("238") // фон строки под курсором
)

var (
	styHeader = lipgloss.NewStyle().
			Foreground(colAccent).
			Bold(true)

	styHeaderDim = lipgloss.NewStyle().Foreground(colDim)

	styBorder = lipgloss.NewStyle().Foreground(colFaint)

	// Подсказка о непрочитанных строках, встроенная в разделитель.
	styScrollHint = lipgloss.NewStyle().Foreground(colWarn).Bold(true)

	// Полоса прокрутки ленты диалога. Бегунок того же цвета, что и дорожка:
	// он и так выделяется формой (█ против │), а цветной акцент в углу экрана
	// только отвлекает от ответа.
	styScrollTrack = lipgloss.NewStyle().Foreground(colFaint)
	styScrollThumb = lipgloss.NewStyle().Foreground(colFaint)

	styUserPrefix = lipgloss.NewStyle().Foreground(colUser).Bold(true)
	styUserText   = lipgloss.NewStyle().Foreground(colUserText)

	styModelPrefix = lipgloss.NewStyle().Foreground(colModel).Bold(true)

	styThinking = lipgloss.NewStyle().Foreground(colThink).Italic(true)

	styTool       = lipgloss.NewStyle().Foreground(colTool)
	styToolOK     = lipgloss.NewStyle().Foreground(colOK)
	styToolFail   = lipgloss.NewStyle().Foreground(colError)
	styToolSkip   = lipgloss.NewStyle().Foreground(colWarn)
	styToolOutput = lipgloss.NewStyle().Foreground(colDim)

	styError  = lipgloss.NewStyle().Foreground(colError).Bold(true)
	styNotice = lipgloss.NewStyle().Foreground(colDim).Italic(true)
	styHint   = lipgloss.NewStyle().Foreground(colHint)
	styBook   = lipgloss.NewStyle().Foreground(colBook)

	// styTurnID — идентификатор обмена под ответом. Нарочно самый тусклый цвет:
	// он нужен изредка, а мозолить глаза под каждым ответом не должен.
	styTurnID = lipgloss.NewStyle().Foreground(colFaint)

	styStatus     = lipgloss.NewStyle().Foreground(colDim)
	styStatusWarn = lipgloss.NewStyle().Foreground(colWarn)
	styStatusHot  = lipgloss.NewStyle().Foreground(colError)
	styStatusMode = lipgloss.NewStyle().Foreground(colAccent).Bold(true)

	styConfirmBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colWarn).
			Padding(0, 1)

	styConfirmTitle = lipgloss.NewStyle().Foreground(colWarn).Bold(true)
	styConfirmKeys  = lipgloss.NewStyle().Foreground(colAccent)

	styPickerBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colAccent).
			Padding(0, 1)

	styPickerTitle = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	styPickerHint  = lipgloss.NewStyle().Foreground(colDim)

	// styGhost — дописываемый хвост команды в строке ввода. Тем же тусклым
	// цветом, что и подсказки: человек должен видеть, что это подсказка,
	// а не набранный им текст.
	styGhost = lipgloss.NewStyle().Foreground(colDim)

	// Имя выделяется светло-оранжевым, дополнительные колонки — бледно-оранжевым.
	styPickerName    = lipgloss.NewStyle().Foreground(colNameBright)
	styPickerCol     = lipgloss.NewStyle().Foreground(colNamePale)
	styPickerNameSel = lipgloss.NewStyle().Foreground(colNameBright).Background(colRowSel).Bold(true)
	styPickerColSel  = lipgloss.NewStyle().Foreground(colNamePale).Background(colRowSel)
	styPickerMarker  = lipgloss.NewStyle().Foreground(colAccent).Background(colRowSel).Bold(true)
	// Фон строки под курсором приглушённый: текст должен оставаться читаемым.
	styPickerRow = lipgloss.NewStyle().Background(colRowSel)

	styDiffAdd  = lipgloss.NewStyle().Foreground(colOK)
	styDiffDel  = lipgloss.NewStyle().Foreground(colError)
	styDiffMeta = lipgloss.NewStyle().Foreground(colDim)
)

// modeStyle подбирает цвет для названия режима подтверждений.
func modeStyle(mode string) lipgloss.Style {
	switch mode {
	case "noask", "yolo":
		return lipgloss.NewStyle().Foreground(colError).Bold(true)
	case "auto-edit":
		return lipgloss.NewStyle().Foreground(colWarn).Bold(true)
	default:
		return styStatusMode
	}
}

// ctxStyle подбирает цвет индикатора контекста по проценту заполнения.
func ctxStyle(percent int) lipgloss.Style {
	switch {
	case percent >= 90:
		return styStatusHot
	case percent >= 70:
		return styStatusWarn
	default:
		return styStatus
	}
}
