// Package citation formats publications in APA 7th edition and Harvard (Cite
// Them Right) styles using only retrieved metadata. Missing fields are
// omitted or marked as such ("n.d."/"no date"), never invented.
package citation

import (
	"fmt"
	"html"
	"html/template"
	"strings"
	"unicode"

	"citewise/internal/models"
)

// Style is a supported citation style.
type Style string

// Supported styles.
const (
	APA     Style = "apa"
	Harvard Style = "harvard"
)

// ParseStyle returns the style for s, or ok=false if unsupported.
func ParseStyle(s string) (Style, bool) {
	switch Style(strings.ToLower(strings.TrimSpace(s))) {
	case APA:
		return APA, true
	case Harvard:
		return Harvard, true
	}
	return "", false
}

// Label returns the human-readable style name.
func (s Style) Label() string {
	if s == Harvard {
		return "Harvard"
	}
	return "APA 7th edition"
}

// Segment is a run of citation text, optionally italic.
type Segment struct {
	Text   string
	Italic bool
}

// Citation is a formatted reference.
type Citation []Segment

// Text renders the citation as plain text.
func (c Citation) Text() string {
	var b strings.Builder
	for _, s := range c {
		b.WriteString(s.Text)
	}
	return b.String()
}

// HTML renders the citation with <em> for italics. All text is escaped.
func (c Citation) HTML() template.HTML {
	var b strings.Builder
	for _, s := range c {
		if s.Italic {
			b.WriteString("<em>" + html.EscapeString(s.Text) + "</em>")
		} else {
			b.WriteString(html.EscapeString(s.Text))
		}
	}
	return template.HTML(b.String())
}

type builder struct{ c Citation }

func (b *builder) add(text string, italic bool) {
	if text == "" {
		return
	}
	if n := len(b.c); n > 0 && b.c[n-1].Italic == italic {
		b.c[n-1].Text += text
		return
	}
	b.c = append(b.c, Segment{Text: text, Italic: italic})
}
func (b *builder) plain(s string)  { b.add(s, false) }
func (b *builder) italic(s string) { b.add(s, true) }

// Format returns the reference-list entry for p.
func Format(p models.Publication, style Style) Citation {
	if style == Harvard {
		return harvard(p)
	}
	return apa(p)
}

// InText returns a parenthetical in-text citation, e.g. "(Doe & Lee, 2020)".
func InText(p models.Publication, style Style) string {
	names := make([]string, 0, len(p.Authors))
	for _, a := range p.Authors {
		if a.Family != "" {
			names = append(names, a.Family)
		} else if a.Literal != "" {
			names = append(names, a.Literal)
		}
	}
	year := yearOr(p.Year, "n.d.")
	and := "&"
	if style == Harvard {
		year = yearOr(p.Year, "no date")
		and = "and"
	}
	var who string
	switch {
	case len(names) == 0:
		who = shortTitle(p.Title)
	case len(names) == 1:
		who = names[0]
	case len(names) == 2:
		who = names[0] + " " + and + " " + names[1]
	case style == Harvard && len(names) == 3:
		who = names[0] + ", " + names[1] + " and " + names[2]
	default:
		who = names[0] + " et al."
	}
	return "(" + who + ", " + year + ")"
}

func shortTitle(t string) string {
	words := strings.Fields(t)
	if len(words) > 4 {
		words = append(words[:4], "…")
	}
	return "‘" + strings.Join(words, " ") + "’"
}

func yearOr(y int, missing string) string {
	if y <= 0 {
		return missing
	}
	return fmt.Sprint(y)
}

// Initials converts given names to initials. sep is placed between initials
// ("J. R." for APA with sep " ", "J.R." for Harvard with sep "").
func Initials(given, sep string) string {
	var parts []string
	for _, tok := range strings.FieldsFunc(given, func(r rune) bool { return r == ' ' || r == '.' }) {
		var hy []string
		for _, h := range strings.Split(tok, "-") {
			r := []rune(h)
			if len(r) == 0 || !unicode.IsLetter(r[0]) {
				continue
			}
			hy = append(hy, string(unicode.ToUpper(r[0]))+".")
		}
		if len(hy) > 0 {
			parts = append(parts, strings.Join(hy, "-"))
		}
	}
	return strings.Join(parts, sep)
}

func personName(a models.Author, sep string) string {
	if a.Family == "" {
		return a.Literal
	}
	if in := Initials(a.Given, sep); in != "" {
		return a.Family + ", " + in
	}
	return a.Family
}

func apaAuthors(authors []models.Author) string {
	names := make([]string, 0, len(authors))
	for _, a := range authors {
		if n := personName(a, " "); n != "" {
			names = append(names, n)
		}
	}
	switch n := len(names); {
	case n == 0:
		return ""
	case n == 1:
		return names[0]
	case n == 2:
		return names[0] + ", & " + names[1]
	case n <= 20:
		return strings.Join(names[:n-1], ", ") + ", & " + names[n-1]
	default:
		return strings.Join(names[:19], ", ") + ", . . . " + names[n-1]
	}
}

func harvardAuthors(authors []models.Author) string {
	names := make([]string, 0, len(authors))
	for _, a := range authors {
		if n := personName(a, ""); n != "" {
			names = append(names, n)
		}
	}
	switch n := len(names); {
	case n == 0:
		return ""
	case n == 1:
		return names[0]
	case n == 2:
		return names[0] + " and " + names[1]
	case n == 3:
		return names[0] + ", " + names[1] + " and " + names[2]
	default:
		return names[0] + " et al."
	}
}

// endWith appends punct unless s already ends with terminal punctuation.
func endWith(s, punct string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	switch s[len(s)-1] {
	case '.', '?', '!':
		return s
	}
	return s + punct
}

func pageRange(p string) string { return strings.ReplaceAll(strings.TrimSpace(p), "-", "–") }

func link(p models.Publication) string {
	if p.DOI != "" {
		return "https://doi.org/" + p.DOI
	}
	if strings.HasPrefix(p.URL, "https://") || strings.HasPrefix(p.URL, "http://") {
		return p.URL
	}
	return ""
}

func apa(p models.Publication) Citation {
	var b builder
	authors := apaAuthors(p.Authors)
	year := "(" + yearOr(p.Year, "n.d.") + "). "
	isBook := p.IsBook()
	title := strings.TrimSpace(p.Title)

	if authors != "" {
		b.plain(endWith(authors, ".") + " " + year)
		if isBook {
			b.italic(endWith(title, ""))
			b.plain(". ")
		} else {
			b.plain(endWith(title, ".") + " ")
		}
	} else {
		// No author: the title moves to the author position.
		if isBook {
			b.italic(endWith(title, ""))
			b.plain(". " + year)
		} else {
			b.plain(endWith(title, ".") + " " + year)
		}
	}

	switch {
	case isBook:
		if p.Publisher != "" {
			b.plain(endWith(p.Publisher, ".") + " ")
		}
	case p.Type == "book-chapter":
		if p.Venue != "" {
			b.plain("In ")
			b.italic(p.Venue)
			if p.Pages != "" {
				b.plain(" (pp. " + pageRange(p.Pages) + ")")
			}
			b.plain(". ")
		}
		if p.Publisher != "" {
			b.plain(endWith(p.Publisher, ".") + " ")
		}
	default:
		if p.Venue != "" {
			b.italic(p.Venue)
			if p.Volume != "" {
				b.plain(", ")
				b.italic(p.Volume)
				if p.Issue != "" {
					b.plain("(" + p.Issue + ")")
				}
			}
			if p.Pages != "" {
				b.plain(", " + pageRange(p.Pages))
			}
			b.plain(". ")
		}
	}
	if l := link(p); l != "" {
		b.plain(l)
	}
	b.c = trimTrailing(b.c)
	return b.c
}

func harvard(p models.Publication) Citation {
	var b builder
	authors := harvardAuthors(p.Authors)
	year := "(" + yearOr(p.Year, "no date") + ")"
	isBook := p.IsBook()
	title := strings.TrimSpace(p.Title)

	if authors != "" {
		b.plain(authors + " " + year + " ")
		if isBook {
			b.italic(title)
			b.plain(". ")
		} else {
			b.plain("‘" + title + "’")
		}
	} else if isBook {
		b.italic(title)
		b.plain(" " + year + ". ")
	} else {
		b.plain("‘" + title + "’ " + year)
	}

	switch {
	case isBook:
		if p.Publisher != "" {
			b.plain(endWith(p.Publisher, ".") + " ")
		}
	case p.Type == "book-chapter":
		if p.Venue != "" {
			b.plain(", in ")
			b.italic(p.Venue)
		}
		if p.Publisher != "" {
			b.plain(". " + p.Publisher)
		}
		if p.Pages != "" {
			b.plain(", " + pagesLabel(p.Pages))
		}
		b.plain(". ")
	default:
		if p.Venue != "" {
			b.plain(", ")
			b.italic(p.Venue)
			if p.Volume != "" {
				b.plain(", " + p.Volume)
				if p.Issue != "" {
					b.plain("(" + p.Issue + ")")
				}
			}
			if p.Pages != "" {
				b.plain(", " + pagesLabel(p.Pages))
			}
		}
		b.plain(". ")
	}
	if l := link(p); l != "" {
		b.plain("Available at: " + l + ".")
	}
	b.c = trimTrailing(b.c)
	return b.c
}

func pagesLabel(p string) string {
	if strings.Contains(p, "-") {
		return "pp. " + pageRange(p)
	}
	return "p. " + strings.TrimSpace(p)
}

func trimTrailing(c Citation) Citation {
	if n := len(c); n > 0 {
		c[n-1].Text = strings.TrimRight(c[n-1].Text, " ")
		if c[n-1].Text == "" {
			c = c[:n-1]
		}
	}
	return c
}
