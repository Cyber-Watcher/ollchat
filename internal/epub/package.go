package epub

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
)

// Список глав и сведения о книге.
//
// Порядок чтения задаётся не именами файлов и не порядком в архиве, а разделом
// spine в файле OPF: там перечислены идентификаторы глав в том порядке, в каком
// их читают. Имена файлов бывают какими угодно — «ch1-15.xhtml» может идти
// первой главой, — поэтому опираться на них нельзя.

type pkgInfo struct {
	title  string
	author string
	// date — год из <dc:date>. У EPUB это чаще всего дата издания, а не файла,
	// поэтому источник надёжнее, чем /CreationDate у PDF, но всё равно не
	// главный: сборщики книг проставляют туда что попало.
	date   int
	spine  []string          // пути к главам в порядке чтения
	titles map[string]string // путь главы → заголовок из оглавления
	base   string            // каталог, относительно которого считаются пути
}

// readPackage находит и разбирает файл OPF.
func (b *book) readPackage() (*pkgInfo, error) {
	opfPath, err := b.rootFile()
	if err != nil {
		return nil, err
	}
	raw, err := b.read(opfPath)
	if err != nil {
		return nil, err
	}

	var doc struct {
		Metadata struct {
			Title   []string `xml:"title"`
			Creator []string `xml:"creator"`
			Date    []string `xml:"date"`
		} `xml:"metadata"`
		Manifest struct {
			Items []struct {
				ID        string `xml:"id,attr"`
				Href      string `xml:"href,attr"`
				MediaType string `xml:"media-type,attr"`
				Props     string `xml:"properties,attr"`
			} `xml:"item"`
		} `xml:"manifest"`
		Spine struct {
			TOC   string `xml:"toc,attr"`
			Items []struct {
				IDRef  string `xml:"idref,attr"`
				Linear string `xml:"linear,attr"`
			} `xml:"itemref"`
		} `xml:"spine"`
	}
	if err := decodeXML(raw, &doc); err != nil {
		return nil, fmt.Errorf("разбор OPF: %w", err)
	}

	info := &pkgInfo{base: path.Dir(opfPath), titles: map[string]string{}}
	if len(doc.Metadata.Title) > 0 {
		info.title = strings.TrimSpace(doc.Metadata.Title[0])
	}
	if len(doc.Metadata.Creator) > 0 {
		info.author = strings.TrimSpace(doc.Metadata.Creator[0])
	}
	for _, d := range doc.Metadata.Date {
		if y := yearOf(d); y > 0 {
			info.date = y
			break
		}
	}

	byID := map[string]string{}
	var navHref, ncxHref string
	for _, it := range doc.Manifest.Items {
		href := info.resolve(it.Href)
		byID[it.ID] = href
		if strings.Contains(it.Props, "nav") {
			navHref = href
		}
		if it.MediaType == "application/x-dtbncx+xml" {
			ncxHref = href
		}
	}
	for _, ref := range doc.Spine.Items {
		if strings.EqualFold(ref.Linear, "no") {
			// Обложки и служебные страницы читать незачем.
			continue
		}
		if href, ok := byID[ref.IDRef]; ok {
			info.spine = append(info.spine, href)
		}
	}
	if len(info.spine) == 0 {
		// Spine пуст или битый — берём все главы из манифеста по порядку.
		for _, it := range doc.Manifest.Items {
			if it.MediaType == "application/xhtml+xml" {
				info.spine = append(info.spine, info.resolve(it.Href))
			}
		}
	}
	if ncxHref == "" {
		if href, ok := byID[doc.Spine.TOC]; ok {
			ncxHref = href
		}
	}
	b.readTOC(info, navHref, ncxHref)
	return info, nil
}

// resolve приводит путь из OPF к пути внутри архива.
func (p *pkgInfo) resolve(href string) string {
	href = strings.SplitN(href, "#", 2)[0]
	if p.base == "" || p.base == "." {
		return cleanPath(href)
	}
	return cleanPath(path.Join(p.base, href))
}

// rootFile читает META-INF/container.xml и возвращает путь к OPF.
func (b *book) rootFile() (string, error) {
	raw, err := b.read("META-INF/container.xml")
	if err != nil {
		// Некоторые книги собраны небрежно; ищем OPF прямо в архиве.
		for name := range b.files {
			if strings.HasSuffix(strings.ToLower(name), ".opf") {
				return name, nil
			}
		}
		return "", errors.New("в книге нет ни container.xml, ни файла OPF")
	}
	var doc struct {
		Rootfiles []struct {
			FullPath string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if err := decodeXML(raw, &doc); err != nil {
		return "", fmt.Errorf("разбор container.xml: %w", err)
	}
	for _, rf := range doc.Rootfiles {
		if rf.FullPath != "" {
			return cleanPath(rf.FullPath), nil
		}
	}
	return "", errors.New("в container.xml не указан файл OPF")
}

// readTOC достаёт заголовки разделов из оглавления: у глав часто нет своего
// заголовка внутри, и без оглавления раздел остался бы безымянным.
func (b *book) readTOC(info *pkgInfo, navHref, ncxHref string) {
	if ncxHref != "" {
		if raw, err := b.read(ncxHref); err == nil {
			var doc struct {
				Points []struct {
					Label   string `xml:"navLabel>text"`
					Content struct {
						Src string `xml:"src,attr"`
					} `xml:"content"`
				} `xml:"navMap>navPoint"`
			}
			if decodeXML(raw, &doc) == nil {
				base := path.Dir(ncxHref)
				for _, p := range doc.Points {
					src := cleanPath(path.Join(base, strings.SplitN(p.Content.Src, "#", 2)[0]))
					if label := strings.TrimSpace(p.Label); label != "" {
						if _, ok := info.titles[src]; !ok {
							info.titles[src] = label
						}
					}
				}
			}
		}
	}
	if navHref == "" {
		return
	}
	raw, err := b.read(navHref)
	if err != nil {
		return
	}
	base := path.Dir(navHref)
	for _, link := range navLinks(raw) {
		src := cleanPath(path.Join(base, strings.SplitN(link.href, "#", 2)[0]))
		if link.text != "" {
			if _, ok := info.titles[src]; !ok {
				info.titles[src] = link.text
			}
		}
	}
}

// decodeXML разбирает служебные файлы книги: OPF, container.xml, оглавление.
//
// Правила автозакрытия HTML здесь применять нельзя, хотя для самих глав они
// нужны: в списке автозакрываемых есть meta, а в OPF стоит <meta …>aut</meta>
// с содержимым. Автозакрытие обрывает разбор на закрывающем теге, и книга
// выглядит пустой — ни манифеста, ни списка глав.
func decodeXML(data []byte, v any) error {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = false
	d.Entity = xml.HTMLEntity
	return d.Decode(v)
}

func readFile(path string) ([]byte, error) { return os.ReadFile(path) }

// yearOf достаёт год из <dc:date>: там бывает и «2019», и «2019-04-01»,
// и полная отметка времени.
func yearOf(s string) int {
	digits := 0
	n := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			n = n*10 + int(r-'0')
			digits++
			if digits == 4 {
				if n >= 1900 && n <= 2100 {
					return n
				}
				return 0
			}
			continue
		}
		if digits > 0 {
			return 0
		}
	}
	return 0
}
