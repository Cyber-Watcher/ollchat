package redact

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// RunCLI — ключ --scan-redact: та же обработка, что у инструмента scan_redact,
// но без модели. Нужна, чтобы проверить документ, не занимая видеокарту,
// и чтобы сравнить итог с тем, что сделает модель. На экран — только числа:
// значения скрытого в вывод не попадают.
func RunCLI(stdout, stderr io.Writer, path string, args []string) error {
	fs := flag.NewFlagSet("scan-redact", flag.ContinueOnError)
	fs.SetOutput(stderr)
	formats := fs.String("formats", "pdf,md", "что сделать: pdf, md или pdf,md")
	outPDF := fs.String("out-pdf", "", "куда записать PDF; по умолчанию <имя>.redacted.pdf рядом с исходным")
	outMD := fs.String("out-md", "", "куда записать .md; по умолчанию <имя>.redacted.md рядом с исходным")
	lang := fs.String("lang", "", "языки tesseract: eng, rus, eng+rus; по умолчанию eng и rus из установленных")
	clients := fs.String("clients", "", "имена клиентов сверх найденного, через «;»")
	doctors := fs.String("doctors", "", "имена врачей сверх найденного, через «;»")
	hide := fs.String("hide", "", "прочие строки, которые скрыть, через «;»")
	report := fs.String("report", "", "записать разбор по словам в этот файл JSON — В НЁМ САМИ ПЕРСОНАЛЬНЫЕ ДАННЫЕ, только для исследования")
	if err := fs.Parse(args); err != nil {
		return err
	}
	f := strings.ToLower(*formats)
	wantPDF, wantMD := strings.Contains(f, "pdf"), strings.Contains(f, "md")
	if !wantPDF && !wantMD {
		return fmt.Errorf("-formats: pdf, md или pdf,md, а не %q", *formats)
	}
	stem := strings.TrimSuffix(path, filepath.Ext(path))
	if *outPDF == "" {
		*outPDF = stem + ".redacted.pdf"
	}
	if *outMD == "" {
		*outMD = stem + ".redacted.md"
	}

	// Исходник не перезаписывается, и два результата не пишутся в один файл —
	// те же отказы, что у инструмента scan_redact (tools/scanredact.go).
	// Запись идёт переименованием, так что оригинал скана пропал бы без следа.
	same := func(a, b string) bool {
		aa, err1 := filepath.Abs(a)
		bb, err2 := filepath.Abs(b)
		return err1 == nil && err2 == nil && aa == bb
	}
	if wantPDF && same(*outPDF, path) {
		return fmt.Errorf("-out-pdf совпадает с исходным документом — исходник не перезаписывается")
	}
	if wantMD && same(*outMD, path) {
		return fmt.Errorf("-out-md совпадает с исходным документом — исходник не перезаписывается")
	}
	if wantPDF && wantMD && same(*outPDF, *outMD) {
		return fmt.Errorf("-out-pdf и -out-md указывают на один файл — второй затёр бы первый")
	}

	ctx := context.Background()
	fmt.Fprintf(stderr, "читаю %s\n", path)
	pages, notes, err := Load(ctx, path, 0)
	if err != nil {
		return err
	}
	res, err := Process(ctx, strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), pages, Options{
		Lang: *lang, Clients: split(*clients), Doctors: split(*doctors), Hide: split(*hide),
		Progress: func(s string) { fmt.Fprintln(stderr, s) },
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "страниц %d, слов %d\n", len(pages), len(res.Words))
	fmt.Fprint(stdout, res.CountsLine())
	if wantPDF {
		data, err := PDF(pages, res.Redacted)
		if err != nil {
			return err
		}
		if err := WriteFile(*outPDF, data); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "PDF: %s (%d байт)\n", *outPDF, len(data))
	}
	if wantMD {
		if err := WriteFile(*outMD, []byte(res.MD)); err != nil {
			return err
		}
		fmt.Fprintf(stdout, ".md: %s (%d байт)\n", *outMD, len(res.MD))
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
		os.Exit(3)
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
