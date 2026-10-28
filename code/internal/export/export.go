// Package export renders bibliographies as plain text or BibTeX.
package export

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"citewise/internal/citation"
	"citewise/internal/models"
)

// Format is a supported export format.
type Format string

// Supported formats.
const (
	TXT    Format = "txt"
	BibTeX Format = "bibtex"
)

// ParseFormat returns the format for s, or ok=false if unsupported.
func ParseFormat(s string) (Format, bool) {
	switch Format(strings.ToLower(strings.TrimSpace(s))) {
	case TXT:
		return TXT, true
	case BibTeX, "bib":
		return BibTeX, true
	}
	return "", false
}

// File describes an export download.
type File struct {
	Name        string
	ContentType string
	Body        []byte
}

// Render produces the export file for pubs.
func Render(pubs []models.Publication, f Format, style citation.Style, now time.Time) File {
	if f == BibTeX {
		return File{Name: "citewise-references.bib", ContentType: "application/x-bibtex; charset=utf-8", Body: []byte(ToBibTeX(pubs))}
	}
	return File{Name: "citewise-references.txt", ContentType: "text/plain; charset=utf-8", Body: []byte(ToText(pubs, style, now))}
}

// ToText returns a reference list sorted alphabetically, as required by both
// APA and Harvard.
func ToText(pubs []models.Publication, style citation.Style, now time.Time) string {
	entries := make([]string, 0, len(pubs))
	for _, p := range pubs {
		entries = append(entries, citation.Format(p, style).Text())
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return strings.ToLower(entries[i]) < strings.ToLower(entries[j])
	})
	var b strings.Builder
	fmt.Fprintf(&b, "References (%s)\n", style.Label())
	fmt.Fprintf(&b, "Exported from CiteWise AI on %s\n\n", now.Format("2 January 2006"))
	for _, e := range entries {
		b.WriteString(e)
		b.WriteString("\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// ToBibTeX returns BibTeX entries for pubs. Only retrieved fields are
// written; missing fields are omitted.
func ToBibTeX(pubs []models.Publication) string {
	used := map[string]int{}
	var b strings.Builder
	for i, p := range pubs {
		if i > 0 {
			b.WriteString("\n")
		}
		key := citeKey(p)
		used[key]++
		if n := used[key]; n > 1 {
			key += string(rune('a' + n - 1))
		}
		entry, venueField := bibType(p)
		fmt.Fprintf(&b, "@%s{%s,\n", entry, key)
		field := func(name, val string) {
			if strings.TrimSpace(val) != "" {
				fmt.Fprintf(&b, "  %s = {%s},\n", name, val)
			}
		}
		field("author", bibAuthors(p.Authors))
		field("title", "{"+escape(p.Title)+"}")
		if venueField != "" {
			field(venueField, escape(p.Venue))
		}
		if p.Year > 0 {
			field("year", fmt.Sprint(p.Year))
		}
		field("volume", escape(p.Volume))
		field("number", escape(p.Issue))
		field("pages", escape(strings.ReplaceAll(strings.ReplaceAll(p.Pages, "–", "-"), "-", "--")))
		field("publisher", escape(p.Publisher))
		field("doi", escape(p.DOI))
		if p.DOI == "" {
			field("url", escape(p.URL))
		}
		s := strings.TrimSuffix(b.String(), ",\n") + "\n}\n"
		b.Reset()
		b.WriteString(s)
	}
	return b.String()
}

func bibType(p models.Publication) (entry, venueField string) {
	switch {
	case p.IsBook():
		return "book", ""
	case p.Type == "book-chapter":
		return "incollection", "booktitle"
	case p.Type == "proceedings-article":
		return "inproceedings", "booktitle"
	case p.Type == "dissertation":
		return "phdthesis", "school"
	case p.Type == "report":
		return "techreport", "institution"
	case p.Type == "journal-article" || p.Venue != "":
		return "article", "journal"
	}
	return "misc", "howpublished"
}

func bibAuthors(as []models.Author) string {
	names := make([]string, 0, len(as))
	for _, a := range as {
		switch {
		case a.Family != "" && a.Given != "":
			names = append(names, escape(a.Family)+", "+escape(a.Given))
		case a.Family != "":
			names = append(names, escape(a.Family))
		case a.Literal != "":
			names = append(names, "{"+escape(a.Literal)+"}")
		}
	}
	return strings.Join(names, " and ")
}

// citeKey builds keys like "doe2021social".
func citeKey(p models.Publication) string {
	name := "anon"
	if len(p.Authors) > 0 {
		if n := asciiWord(p.Authors[0].Family + p.Authors[0].Literal); n != "" {
			name = n
		}
	}
	year := "nd"
	if p.Year > 0 {
		year = fmt.Sprint(p.Year)
	}
	word := ""
	for _, w := range strings.Fields(p.Title) {
		if aw := asciiWord(w); len(aw) > 3 {
			word = aw
			break
		}
	}
	return name + year + word
}

func asciiWord(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

var bibEscaper = strings.NewReplacer(
	`\`, `\textbackslash{}`,
	"{", `\{`, "}", `\}`,
	"&", `\&`, "%", `\%`, "$", `\$`, "#", `\#`, "_", `\_`,
	"~", `\textasciitilde{}`, "^", `\textasciicircum{}`,
)

func escape(s string) string { return bibEscaper.Replace(strings.TrimSpace(s)) }
