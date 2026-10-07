package redact

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

// ExitError — ошибка ключа командной строки со своим кодом выхода: 3 —
// проверка нашла скрытое в итоге, 130 — прервано сигналом. Процесс
// завершает main, а не библиотека: прежде RunCLI звал os.Exit(3) сам,
// и отложенные действия вызывающего не выполнялись.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// RunCLI — ключ --scan-redact: та же обработка, что у инструмента scan_redact,
// но без модели. Нужна, чтобы проверить документ, не занимая видеокарту,
// и чтобы сравнить итог с тем, что сделает модель. На экран — только числа:
// значения скрытого в вывод не попадают.
func RunCLI(stdout, stderr io.Writer, path string, args []string) (err error) {
	fs := flag.NewFlagSet("scan-redact", flag.ContinueOnError)
	fs.SetOutput(stderr)
	formats := fs.String("formats", DefaultFormats, "что сделать — "+FormatsHelp)
	outPDF := fs.String("out-pdf", "", "куда записать PDF с замазанными данными; по умолчанию <имя>.redacted.pdf рядом с исходным")
	outMD := fs.String("out-md", "", "куда записать .md без персональных данных; по умолчанию <имя>.redacted.md рядом с исходным")
	outOCRPDF := fs.String("out-ocr-pdf", "", "куда записать текстовый PDF распознанного (с персональными данными), "+
		"если он заказан в -formats; по умолчанию <имя>.ocr.pdf")
	outOCRMD := fs.String("out-ocr-md", "", "куда записать .md распознанного (с персональными данными), "+
		"если он заказан в -formats; по умолчанию <имя>.ocr.md")
	lang := fs.String("lang", "", "языки tesseract: eng, rus, eng+rus; по умолчанию решают первые листы — английский документ читается одним eng, прочие eng+rus")
	clients := fs.String("clients", "", "имена клиентов сверх найденного, через «;»")
	doctors := fs.String("doctors", "", "имена врачей сверх найденного, через «;»")
	hide := fs.String("hide", "", "прочие строки, которые скрыть, через «;»")
	report := fs.String("report", "", "записать разбор по словам в этот файл JSON — В НЁМ САМИ ПЕРСОНАЛЬНЫЕ ДАННЫЕ, только для исследования")
	if err := fs.Parse(args); err != nil {
		return err
	}
	want, err := ParseFormats(*formats)
	if err != nil {
		return fmt.Errorf("-formats: %w", err)
	}
	out := DefaultOutputs(path, want)
	for _, o := range []struct {
		dst  *string
		flag string
	}{{&out.PDF, *outPDF}, {&out.MD, *outMD}, {&out.OCRPDF, *outOCRPDF}, {&out.OCRMD, *outOCRMD}} {
		if *o.dst != "" && o.flag != "" {
			*o.dst = o.flag
		}
	}
	// Те же отказы, что у инструмента scan_redact (tools/scanredact.go).
	if err := out.Check(path); err != nil {
		return err
	}

	// Ctrl+C прерывает распознавание, а не процесс: картинки страниц для
	// tesseract и pdftoppm — с персональными данными, и убирают их отложенные
	// действия, которых при гибели процесса по сигналу нет; во временном
	// каталоге оставались страницы медицинского документа. Повторное нажатие
	// завершает процесс сразу: перехват снимается после первого.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	context.AfterFunc(ctx, stop)
	defer func() {
		if err != nil && ctx.Err() != nil {
			err = &ExitError{Code: 130, Err: fmt.Errorf("прервано, временные файлы убраны: %w", err)}
		}
	}()

	fmt.Fprintf(stderr, "читаю %s\n", path)
	pages, notes, err := Load(ctx, path, 0)
	if err != nil {
		return err
	}
	title := filepath.Base(Stem(path))
	res, err := Process(ctx, title, pages, Options{
		Lang: *lang, Clients: split(*clients), Doctors: split(*doctors), Hide: split(*hide),
		Progress: func(s string) { fmt.Fprintln(stderr, s) },
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "страниц %d, слов %d, язык распознавания %s\n", len(pages), len(res.Words), res.Lang)
	fmt.Fprint(stdout, res.CountsLine())
	fmt.Fprintln(stderr, "пишу файлы")
	written, err := WriteOutputs(out, title, pages, res)
	for _, w := range written {
		fmt.Fprintf(stdout, "%s: %s (%d байт)\n", w.What, w.Path, w.Bytes)
		if w.Note != "" {
			fmt.Fprintf(stdout, "  оговорка: %s\n", w.Note)
		}
	}
	if err != nil {
		return err
	}
	for _, n := range notes {
		fmt.Fprintf(stdout, "заметка: %s\n", n)
	}
	if *report != "" {
		data, err := json.MarshalIndent(res.Report(), "", " ")
		if err != nil {
			return err
		}
		if err := WriteFile(*report, data); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "разбор: %s (в нём персональные данные)\n", *report)
	}
	fmt.Fprint(stdout, res.Check.Line())
	if !res.Check.OK() {
		return &ExitError{Code: 3, Err: errors.New("проверка повторным распознаванием нашла скрытое в итоге: " +
			"обезличенные файлы записаны с пометкой UNVERIFIED в имени")}
	}
	return nil
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ";") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
