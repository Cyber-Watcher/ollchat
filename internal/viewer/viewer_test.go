package viewer

import (
	"os/exec"
	"strings"
	"testing"
)

// Без настройки — системный просмотрщик, тот же, что открывает файл
// из проводника.
func TestSystemViewerWhenNotConfigured(t *testing.T) {
	if _, err := exec.LookPath("xdg-open"); err != nil {
		t.Skip("в системе нет xdg-open — проверять нечего")
	}
	name, args, err := command("/книги/go.pdf", 12, Commands{})
	if err != nil {
		t.Fatal(err)
	}
	if name != "xdg-open" {
		t.Errorf("ожидался xdg-open, вышло %q", name)
	}
	if len(args) != 1 || args[0] != "/книги/go.pdf" {
		t.Errorf("системному просмотрщику передаётся только путь, вышло %v", args)
	}
}

// Настроенная команда получает и путь, и страницу.
func TestConfiguredViewerGetsPageAndFile(t *testing.T) {
	name, args, err := command("/книги/go.pdf", 214, Commands{PDF: "zathura -P {page} {file}"})
	if err != nil {
		t.Fatal(err)
	}
	if name != "zathura" {
		t.Fatalf("ожидалась zathura, вышло %q", name)
	}
	if got := strings.Join(args, " "); got != "-P 214 /книги/go.pdf" {
		t.Errorf("доводы собраны неверно: %q", got)
	}
}

// Страница неизвестна — довод с ней выбрасывается целиком, иначе просмотрщик
// получит ключ без значения и откажется запускаться.
func TestUnknownPageDropsItsArgument(t *testing.T) {
	_, args, err := command("/книги/book.epub", 0, Commands{EPUB: "foliate --page-index={page} {file}"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(args, " "); got != "/книги/book.epub" {
		t.Errorf("довод со страницей должен был исчезнуть, вышло %q", got)
	}
}

// Без {file} путь дописывается в конец: так пишут команды чаще всего.
func TestFileAppendedWhenNoPlaceholder(t *testing.T) {
	_, args, err := command("/книги/зам.md", 0, Commands{MD: "ghostwriter"})
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 1 || args[0] != "/книги/зам.md" {
		t.Errorf("путь не дописан: %v", args)
	}
}

// Настройка выбирается по расширению, а не по первой попавшейся.
func TestPickedByExtension(t *testing.T) {
	c := Commands{PDF: "pdfview", EPUB: "epubview", MD: "mdview"}
	for path, want := range map[string]string{
		"/a/b.pdf": "pdfview", "/a/b.epub": "epubview",
		"/a/b.md": "mdview", "/a/b.markdown": "mdview",
	} {
		name, _, err := command(path, 0, c)
		if err != nil {
			t.Fatal(err)
		}
		if name != want {
			t.Errorf("%s → %q, ожидалось %q", path, name, want)
		}
	}
}

// Открывать нечего — внятный отказ, а не паника на пустом пути.
func TestOpenRefusesEmptyPath(t *testing.T) {
	if err := Open("  ", 0, Commands{}); err == nil {
		t.Error("пустой путь должен быть отказом")
	}
	if err := Open("/нет/такой/книги.pdf", 0, Commands{}); err == nil {
		t.Error("несуществующий файл должен быть отказом")
	} else if !strings.Contains(err.Error(), "нет") {
		t.Errorf("отказ должен объяснять, что файла нет: %v", err)
	}
}
