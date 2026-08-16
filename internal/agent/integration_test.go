package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/itpro/ollchat/internal/kb"
	"github.com/itpro/ollchat/internal/ollama"
	"github.com/itpro/ollchat/internal/permissions"
	"github.com/itpro/ollchat/internal/session"
	"github.com/itpro/ollchat/internal/tools"
)

// Интеграционные тесты работают с настоящим сервером Ollama.
// Запуск: OLLCHAT_TEST_SERVER=http://192.168.77.77:11434 go test ./internal/agent/ -v
//
// Без переменной окружения тесты пропускаются, поэтому обычный `go test ./...`
// не требует доступа к сети.
func testServer(t *testing.T) (*ollama.Client, string) {
	t.Helper()
	url := os.Getenv("OLLCHAT_TEST_SERVER")
	if url == "" {
		t.Skip("не задан OLLCHAT_TEST_SERVER — интеграционный тест пропущен")
	}
	model := os.Getenv("OLLCHAT_TEST_MODEL")
	if model == "" {
		model = "qwen3.6:latest"
	}
	return ollama.New(url, 300*time.Second, nil), model
}

func testRunner(t *testing.T, client *ollama.Client, model, root string, mode string) *Runner {
	t.Helper()
	sb, err := permissions.NewSandbox(root, false, false, 512)
	if err != nil {
		t.Fatalf("NewSandbox: %v", err)
	}
	set, err := permissions.Compile(
		[]string{"Read(./**)"},
		[]string{"Write(./**)", "Bash(*)", "Fetch(*)"},
		[]string{"Bash(rm:*)", "Bash(sudo:*)"},
		sb.Root())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	reg, err := tools.NewRegistry(tools.AllNames(), tools.Options{
		Sandbox: sb, BashTimeout: 30 * time.Second, MaxOutputKB: 64,
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return &Runner{
		Client: client, Model: model, KeepAlive: "5m",
		Options:        map[string]any{"num_ctx": 8192, "temperature": 0.1},
		Tools:          reg,
		Guard:          permissions.NewGuard(set, sb, mode),
		MaxIterations:  8,
		ToolsSupported: true,
	}
}

// TestLiveReadFileTool проверяет полный цикл: модель запрашивает инструмент,
// разрешение выдаётся правилом, результат возвращается модели и она отвечает.
func TestLiveReadFileTool(t *testing.T) {
	client, model := testServer(t)
	root := t.TempDir()
	realRoot, _ := filepath.EvalSymlinks(root)

	if err := os.WriteFile(filepath.Join(realRoot, "version.txt"),
		[]byte("Версия приложения: 4.7.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := testRunner(t, client, model, realRoot, permissions.ModeSafe)
	conv := session.New("Ты помощник. Используй инструменты, чтобы отвечать точно.")
	conv.Append(ollama.Message{Role: ollama.RoleUser,
		Content: "Прочитай файл version.txt в рабочем каталоге и скажи, какая там версия."})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var (
		answer   strings.Builder
		toolUsed bool
		asked    bool
	)
	for ev := range r.Run(ctx, conv) {
		switch ev.Kind {
		case EventContent:
			answer.WriteString(ev.Text)
		case EventToolResult:
			if ev.Tool.Name == tools.NameReadFile && ev.Tool.OK {
				toolUsed = true
			}
			t.Logf("инструмент %s → ok=%v", ev.Tool.Title, ev.Tool.OK)
		case EventToolConfirm:
			// Чтение разрешено правилом Read(./**) — подтверждения быть не должно.
			asked = true
			ev.Confirm.Reply <- AnswerNo
		case EventError:
			t.Fatalf("ошибка агента: %v", ev.Err)
		}
	}

	if asked {
		t.Error("чтение внутри песочницы не должно требовать подтверждения")
	}
	if !toolUsed {
		t.Errorf("модель не воспользовалась read_file; ответ: %s", answer.String())
	}
	if !strings.Contains(answer.String(), "4.7.1") {
		t.Errorf("в ответе нет версии из файла:\n%s", answer.String())
	}
}

// TestLiveWriteRequiresConfirmation проверяет, что запись файла спрашивает
// подтверждение и отказ пользователя действительно предотвращает запись.
func TestLiveWriteRequiresConfirmation(t *testing.T) {
	client, model := testServer(t)
	root := t.TempDir()
	realRoot, _ := filepath.EvalSymlinks(root)

	r := testRunner(t, client, model, realRoot, permissions.ModeSafe)
	conv := session.New("Ты помощник. Используй инструменты.")
	conv.Append(ollama.Message{Role: ollama.RoleUser,
		Content: "Создай в рабочем каталоге файл note.txt со строкой: привет"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	confirmed := false
	for ev := range r.Run(ctx, conv) {
		switch ev.Kind {
		case EventToolConfirm:
			confirmed = true
			if ev.Confirm.Kind != permissions.KindWrite {
				t.Errorf("вид действия = %v, ожидалось Write", ev.Confirm.Kind)
			}
			if ev.Confirm.Preview == "" {
				t.Error("для записи должен показываться предпросмотр изменений")
			}
			t.Logf("запрошено подтверждение: %s\n%s", ev.Confirm.Title, ev.Confirm.Preview)
			ev.Confirm.Reply <- AnswerNo // отказываемся
		case EventError:
			t.Fatalf("ошибка агента: %v", ev.Err)
		}
	}

	if !confirmed {
		t.Fatal("запись файла обязана запрашивать подтверждение")
	}
	if _, err := os.Stat(filepath.Join(realRoot, "note.txt")); !os.IsNotExist(err) {
		t.Error("после отказа пользователя файл не должен появиться")
	}
}

// TestLiveDeniedCommandNeverRuns проверяет, что запрещённая команда
// не выполняется даже в режиме yolo и подтверждение не запрашивается.
func TestLiveDeniedCommandNeverRuns(t *testing.T) {
	client, model := testServer(t)
	root := t.TempDir()
	realRoot, _ := filepath.EvalSymlinks(root)

	victim := filepath.Join(realRoot, "important.txt")
	if err := os.WriteFile(victim, []byte("важные данные\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := testRunner(t, client, model, realRoot, permissions.ModeYolo)
	conv := session.New("Ты помощник. Выполняй команды через инструмент bash.")
	conv.Append(ollama.Message{Role: ollama.RoleUser,
		Content: "Выполни команду: rm important.txt"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	denied := false
	for ev := range r.Run(ctx, conv) {
		switch ev.Kind {
		case EventToolResult:
			if strings.Contains(ev.Tool.Output, "запрещено настройками") {
				denied = true
				t.Logf("отказ: %s", ev.Tool.Reason)
			}
		case EventToolConfirm:
			t.Error("запрещённая команда не должна доходить до подтверждения")
			ev.Confirm.Reply <- AnswerNo
		case EventError:
			t.Fatalf("ошибка агента: %v", ev.Err)
		}
	}

	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("запрещённая команда удалила файл: %v", err)
	}
	if !denied {
		t.Log("модель не пыталась вызвать rm — проверка запрета не сработала на этом прогоне")
	}
}

// TestLiveContextCounters проверяет, что сервер отдаёт счётчики токенов,
// на которых строится индикатор заполнения контекста.
func TestLiveContextCounters(t *testing.T) {
	client, model := testServer(t)
	root := t.TempDir()
	realRoot, _ := filepath.EvalSymlinks(root)

	r := testRunner(t, client, model, realRoot, permissions.ModeSafe)
	r.ToolsSupported = false
	conv := session.New("")
	conv.Append(ollama.Message{Role: ollama.RoleUser, Content: "Ответь одним словом: столица Франции?"})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var stats ollama.Stats
	for ev := range r.Run(ctx, conv) {
		if ev.Kind == EventStats {
			stats = ev.Stats
		}
		if ev.Kind == EventError {
			t.Fatalf("ошибка агента: %v", ev.Err)
		}
	}

	if stats.PromptEvalCount <= 0 || stats.EvalCount <= 0 {
		t.Fatalf("сервер не вернул счётчики токенов: %+v", stats)
	}
	t.Logf("промпт %d токенов, ответ %d токенов, %.1f ток/с, всего занято %d",
		stats.PromptEvalCount, stats.EvalCount, stats.TokensPerSecond(), stats.TotalTokens())
}

// TestLivePDFRead проверяет то, ради чего чтение PDF и появилось: модель должна
// разобрать документ одним вызовом read_file, а не искать pdftotext, pypdf и
// прочие внешние средства, выжигая на этом лимит итераций.
func TestLivePDFRead(t *testing.T) {
	client, model := testServer(t)
	src := os.Getenv("OLLCHAT_TEST_PDF")
	if src == "" {
		t.Skip("не задан OLLCHAT_TEST_PDF — тест пропущен")
	}
	name := os.Getenv("OLLCHAT_TEST_DOCNAME")
	if name == "" {
		name = "документ.pdf"
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	realRoot, _ := filepath.EvalSymlinks(root)
	if err := os.WriteFile(filepath.Join(realRoot, name), data, 0o644); err != nil {
		t.Fatal(err)
	}

	r := testRunner(t, client, model, realRoot, permissions.ModeSafe)
	conv := session.New("Ты помощник. Используй инструменты, чтобы отвечать точно.")
	conv.Append(ollama.Message{Role: ollama.RoleUser,
		Content: "Прочитай " + name + " в рабочем каталоге и скажи, о чём он и кто в нём упомянут."})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var (
		answer   strings.Builder
		readOK   bool
		bashUsed int
	)
	for ev := range r.Run(ctx, conv) {
		switch ev.Kind {
		case EventContent:
			answer.WriteString(ev.Text)
		case EventToolResult:
			t.Logf("инструмент %s → ok=%v", ev.Tool.Title, ev.Tool.OK)
			switch ev.Tool.Name {
			case tools.NameReadFile:
				readOK = readOK || ev.Tool.OK
			case tools.NameBash:
				bashUsed++
			}
		case EventToolConfirm:
			t.Logf("запрошено подтверждение: %s", ev.Confirm.Title)
			ev.Confirm.Reply <- AnswerNo
		case EventError:
			t.Fatalf("ошибка агента: %v", ev.Err)
		}
	}

	if !readOK {
		t.Errorf("модель не прочитала документ через read_file; ответ:\n%s", answer.String())
	}
	if bashUsed > 0 {
		t.Errorf("модель зачем-то полезла в bash %d раз(а)", bashUsed)
	}
	t.Logf("ответ модели:\n%s", answer.String())
}

// TestLiveKBSearch — ради этого всё и делалось: модель должна сама сообразить
// заглянуть в книги пользователя и ответить со ссылкой на книгу и страницу,
// а не по памяти.
//
// Запуск:
//
//	OLLCHAT_TEST_SERVER=http://192.168.77.77:11434 OLLCHAT_TEST_MODEL=qwen3.5:122b \
//	OLLCHAT_TEST_KB=<каталог базы> OLLCHAT_TEST_KB_COLL=go \
//	go test ./internal/agent/ -run TestLiveKBSearch -v
func TestLiveKBSearch(t *testing.T) {
	client, model := testServer(t)
	dir := os.Getenv("OLLCHAT_TEST_KB")
	if dir == "" {
		t.Skip("не задан OLLCHAT_TEST_KB — тест пропущен")
	}
	coll := os.Getenv("OLLCHAT_TEST_KB_COLL")
	if coll == "" {
		coll = "go"
	}
	question := os.Getenv("OLLCHAT_TEST_KB_Q")
	if question == "" {
		question = "Что такое sync.WaitGroup и как им пользоваться? " +
			"Ответь по книгам из моей библиотеки, обязательно укажи книгу и страницу."
	}

	base, err := kb.OpenBase(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()

	root := t.TempDir()
	realRoot, _ := filepath.EvalSymlinks(root)
	sb, err := permissions.NewSandbox(realRoot, false, false, 512)
	if err != nil {
		t.Fatal(err)
	}
	set, err := permissions.Compile([]string{"Read(./**)", "Read(" + dir + "/**)"},
		nil, nil, sb.Root())
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tools.NewRegistry([]string{tools.NameKBSearch, tools.NameKBRead}, tools.Options{
		Sandbox: sb, MaxOutputKB: 64,
		KB: base, KBDir: dir, KBDefault: coll, KBTopK: 6, KBMaxPerBook: 3,
	})
	if err != nil {
		t.Fatal(err)
	}

	r := &Runner{
		Client: client, Model: model, KeepAlive: "5m",
		Options:        map[string]any{"num_ctx": 16384, "temperature": 0.1},
		Tools:          reg,
		Guard:          permissions.NewGuard(set, sb, permissions.ModeSafe),
		MaxIterations:  8,
		ToolsSupported: true,
	}
	conv := session.New("Ты помощник-программист. У тебя есть доступ к библиотеке книг " +
		"пользователя через kb_search. Отвечая по книгам, всегда указывай книгу и страницу.")
	conv.Append(ollama.Message{Role: ollama.RoleUser, Content: question})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var (
		answer   strings.Builder
		searched bool
		snippets string
	)
	for ev := range r.Run(ctx, conv) {
		switch ev.Kind {
		case EventContent:
			answer.WriteString(ev.Text)
		case EventToolResult:
			t.Logf("инструмент %s → ok=%v", ev.Tool.Title, ev.Tool.OK)
			if ev.Tool.Name == tools.NameKBSearch && ev.Tool.OK {
				searched = true
				snippets = ev.Tool.Output
			}
		case EventToolConfirm:
			ev.Confirm.Reply <- AnswerYes
		case EventError:
			t.Fatalf("ошибка агента: %v", ev.Err)
		}
	}

	if !searched {
		t.Errorf("модель не воспользовалась поиском по книгам; ответ:\n%s", answer.String())
	}
	// Ответ должен опираться на найденное: ссылка на источник — то, чем
	// ответ по книгам отличается от выдумки.
	text := answer.String()
	if !strings.Contains(text, "[1]") && !strings.Contains(text, "стр.") &&
		!strings.Contains(text, "с. ") {
		t.Errorf("в ответе нет ссылки на источник:\n%s", text)
	}
	t.Logf("выдача поиска (начало):\n%.600s", snippets)
	t.Logf("ответ модели:\n%s", text)
}

// TestLiveViewImage — проверка случая, с которого началась вся затея:
// в коммерческом предложении страница «Финансовое предложение» со всей таблицей
// цен свёрстана картинкой, и в текстовом слое её нет вовсе. Модель должна
// сообразить посмотреть на картинку — и назвать сумму, — а не идти искать
// в системе средства распознавания.
//
//	OLLCHAT_TEST_SERVER=… OLLCHAT_TEST_MODEL=gemma4:31b \
//	OLLCHAT_TEST_DOCDIR=tmp/infosec go test ./internal/agent/ -run TestLiveViewImage -v
func TestLiveViewImage(t *testing.T) {
	client, model := testServer(t)
	src := os.Getenv("OLLCHAT_TEST_DOCDIR")
	if src == "" {
		t.Skip("не задан OLLCHAT_TEST_DOCDIR — тест пропущен")
	}
	want := os.Getenv("OLLCHAT_TEST_EXPECT")
	if want == "" {
		want = "2 218 500"
	}

	root := t.TempDir()
	realRoot, _ := filepath.EvalSymlinks(root)
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	var name string
	for _, e := range entries {
		if strings.HasSuffix(strings.ToLower(e.Name()), ".pdf") && strings.Contains(e.Name(), "159") {
			data, err := os.ReadFile(filepath.Join(src, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			name = "предложение.pdf"
			if err := os.WriteFile(filepath.Join(realRoot, name), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if name == "" {
		t.Skip("в каталоге не нашлось нужного документа")
	}

	r := testRunner(t, client, model, realRoot, permissions.ModeSafe)
	r.VisionSupported = true
	conv := session.New("Ты помощник. Используй инструменты, чтобы отвечать точно.")
	conv.Append(ollama.Message{Role: ollama.RoleUser, Content: "Прочитай " + name +
		" и скажи, сколько стоят работы и сколько занимают по срокам. " +
		"Если в тексте этого нет, посмотри в картинках документа."})

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	var (
		answer   strings.Builder
		thinking strings.Builder
		viewed   int
		bashUsed int
	)
	for ev := range r.Run(ctx, conv) {
		switch ev.Kind {
		case EventContent:
			answer.WriteString(ev.Text)
		case EventThinking:
			thinking.WriteString(ev.Text)
		case EventToolResult:
			t.Logf("инструмент %s → ok=%v\n  вывод: %.300s", ev.Tool.Title, ev.Tool.OK, ev.Tool.Output)
			switch ev.Tool.Name {
			case tools.NameViewImage:
				if ev.Tool.OK {
					viewed++
				}
			case tools.NameBash:
				bashUsed++
			}
		case EventStats:
			t.Logf("обмен: промпт %d ток., ответ %d ток.", ev.Stats.PromptEvalCount, ev.Stats.EvalCount)
		case EventToolConfirm:
			t.Logf("запрошено подтверждение: %s", ev.Confirm.Title)
			ev.Confirm.Reply <- AnswerNo
		case EventError:
			t.Fatalf("ошибка агента: %v", ev.Err)
		}
	}

	text := answer.String()
	if viewed == 0 {
		t.Errorf("модель не посмотрела на картинки; ответ:\n%s", text)
	}
	if bashUsed > 0 {
		t.Errorf("модель полезла в bash %d раз(а) вместо просмотра картинки", bashUsed)
	}
	normalized := strings.NewReplacer(" ", "", " ", "", ",", "", ".", "").Replace(text)
	if !strings.Contains(normalized, strings.NewReplacer(" ", "", " ", "").Replace(want)) {
		t.Errorf("в ответе нет суммы %q:\n%s", want, text)
	}
	if strings.TrimSpace(text) == "" && thinking.Len() > 0 {
		t.Logf("рассуждения (ответа не было):\n%.1500s", thinking.String())
	}
	t.Logf("ответ модели:\n%s", text)
}
