package export

import (
	"strings"
	"testing"
	"time"

	"citewise/internal/citation"
	"citewise/internal/models"
)

var when = time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)

func pubs() []models.Publication {
	return []models.Publication{
		{Title: "Zebra stripes & 100% cooling_effects", Authors: []models.Author{{Given: "Tim", Family: "Caro"}},
			Year: 2019, Venue: "Journal of Zoology", Volume: "3", Issue: "1", Pages: "1-10", DOI: "10.1/zebra", Type: "journal-article"},
		{Title: "Animal coloration", Authors: []models.Author{{Given: "Ann", Family: "Abel"}}, Year: 2010, Publisher: "Press", Type: "book"},
		{Title: "Second Caro paper", Authors: []models.Author{{Given: "Tim", Family: "Caro"}}, Year: 2019, Type: "journal-article", URL: "https://x.org/1"},
	}
}

func TestParseFormat(t *testing.T) {
	if f, ok := ParseFormat("TXT"); !ok || f != TXT {
		t.Fatal("txt")
	}
	if f, ok := ParseFormat("bib"); !ok || f != BibTeX {
		t.Fatal("bib")
	}
	if _, ok := ParseFormat("docx"); ok {
		t.Fatal("docx should not be supported")
	}
}

func TestToTextSortedWithHeader(t *testing.T) {
	out := ToText(pubs(), citation.APA, when)
	if !strings.HasPrefix(out, "References (APA 7th edition)\nExported from CiteWise AI on 28 October 2026\n\n") {
		t.Fatalf("header: %q", out)
	}
	ia, ic := strings.Index(out, "Abel, A."), strings.Index(out, "Caro, T.")
	if ia < 0 || ic < 0 || ia > ic {
		t.Fatalf("entries not alphabetical:\n%s", out)
	}
	if !strings.Contains(ToText(pubs(), citation.Harvard, when), "Available at: https://doi.org/10.1/zebra.") {
		t.Fatal("Harvard text export missing DOI")
	}
}

func TestToBibTeX(t *testing.T) {
	out := ToBibTeX(pubs())
	for _, want := range []string{
		"@article{caro2019zebra,",
		"author = {Caro, Tim}",
		`title = {{Zebra stripes \& 100\% cooling\_effects}}`,
		"journal = {Journal of Zoology}",
		"pages = {1--10}",
		"doi = {10.1/zebra}",
		"@book{abel2010animal,",
		"publisher = {Press}",
		"@article{caro2019second,",
		"url = {https://x.org/1}",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("BibTeX missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, ",\n}") {
		t.Errorf("trailing comma before closing brace:\n%s", out)
	}
	if strings.Count(out, "@") != 3 {
		t.Errorf("expected 3 entries")
	}
}

func TestCiteKeyCollision(t *testing.T) {
	p := models.Publication{Title: "Same title", Authors: []models.Author{{Family: "Lee"}}, Year: 2020}
	out := ToBibTeX([]models.Publication{p, p})
	if !strings.Contains(out, "{lee2020same,") || !strings.Contains(out, "{lee2020sameb,") {
		t.Fatalf("keys not unique:\n%s", out)
	}
}

func TestRenderFileNames(t *testing.T) {
	if f := Render(pubs(), BibTeX, citation.APA, when); f.Name != "citewise-references.bib" || len(f.Body) == 0 {
		t.Fatalf("bib file: %+v", f.Name)
	}
	if f := Render(pubs(), TXT, citation.APA, when); f.Name != "citewise-references.txt" || !strings.HasPrefix(f.ContentType, "text/plain") {
		t.Fatalf("txt file: %+v", f.Name)
	}
}
