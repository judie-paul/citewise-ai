// Package models defines the normalized bibliographic types shared across
// providers, storage, citation formatting and export.
package models

import (
	"regexp"
	"strings"
	"unicode"
)

// Author is a publication contributor. Literal holds organisation names or
// names that could not be split into given/family parts.
type Author struct {
	Given   string `json:"given,omitempty"`
	Family  string `json:"family,omitempty"`
	Literal string `json:"literal,omitempty"`
}

// DisplayName returns a human-readable name.
func (a Author) DisplayName() string {
	if a.Family == "" {
		return a.Literal
	}
	if a.Given == "" {
		return a.Family
	}
	return a.Given + " " + a.Family
}

// Publication is normalized metadata retrieved from an academic provider.
// Every field must originate from a provider record; nothing is generated.
type Publication struct {
	ID        int64
	DOI       string
	Title     string
	Authors   []Author
	Year      int
	Venue     string
	Volume    string
	Issue     string
	Pages     string
	Publisher string
	Type      string
	URL       string
	Abstract  string
	Source    string
}

// IsBook reports whether the publication should be cited as a book.
func (p Publication) IsBook() bool {
	switch p.Type {
	case "book", "monograph", "edited-book", "reference-book":
		return true
	}
	return false
}

// Link returns the best URL for the publication.
func (p Publication) Link() string {
	if p.DOI != "" {
		return "https://doi.org/" + p.DOI
	}
	return p.URL
}

var doiPrefix = regexp.MustCompile(`(?i)^(https?://(dx\.)?doi\.org/|doi:\s*)`)

// NormalizeDOI lowercases a DOI and strips URL or "doi:" prefixes.
func NormalizeDOI(doi string) string {
	doi = strings.TrimSpace(doi)
	doi = doiPrefix.ReplaceAllString(doi, "")
	doi = strings.ToLower(doi)
	if !strings.HasPrefix(doi, "10.") {
		return ""
	}
	return doi
}

var tagRe = regexp.MustCompile(`<[^>]+>`)

// StripTags removes markup (e.g. JATS or HTML) and collapses whitespace.
func StripTags(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	r := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&nbsp;", " ")
	return strings.Join(strings.Fields(r.Replace(s)), " ")
}

// NormalizeTitle produces a comparison key: lowercase letters and digits
// separated by single spaces.
func NormalizeTitle(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
		} else if !space && b.Len() > 0 {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}
