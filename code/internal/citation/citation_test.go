package citation

import (
	"fmt"
	"strings"
	"testing"

	"citewise/internal/models"
)

func article() models.Publication {
	return models.Publication{
		DOI:   "10.1111/jcpp.1",
		Title: "Social media and adolescent wellbeing",
		Authors: []models.Author{
			{Given: "Jane Mary", Family: "Doe"},
			{Given: "Jean-Paul", Family: "Sartre"},
		},
		Year: 2021, Venue: "Journal of Child Psychology", Volume: "62", Issue: "4", Pages: "100-110",
		Type: "journal-article",
	}
}

func TestParseStyle(t *testing.T) {
	if s, ok := ParseStyle(" APA "); !ok || s != APA {
		t.Fatal("APA not parsed")
	}
	if s, ok := ParseStyle("harvard"); !ok || s != Harvard {
		t.Fatal("Harvard not parsed")
	}
	if _, ok := ParseStyle("mla"); ok {
		t.Fatal("MLA must be rejected")
	}
}

func TestInitials(t *testing.T) {
	cases := map[string]string{"Jane Mary": "J. M.", "Jean-Paul": "J.-P.", "J.R.": "J. R.", "": "", "d'Arcy": "D."}
	for in, want := range cases {
		if got := Initials(in, " "); got != want {
			t.Errorf("Initials(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Initials("Jane Mary", ""); got != "J.M." {
		t.Errorf("Harvard initials = %q", got)
	}
}

func TestAPAJournalArticle(t *testing.T) {
	c := Format(article(), APA)
	want := "Doe, J. M., & Sartre, J.-P. (2021). Social media and adolescent wellbeing. Journal of Child Psychology, 62(4), 100–110. https://doi.org/10.1111/jcpp.1"
	if got := c.Text(); got != want {
		t.Fatalf("APA:\n got  %q\n want %q", got, want)
	}
	h := string(c.HTML())
	if !strings.Contains(h, "<em>Journal of Child Psychology</em>") || !strings.Contains(h, "<em>62</em>(4)") {
		t.Fatalf("APA HTML italics wrong: %s", h)
	}
}

func TestHarvardJournalArticle(t *testing.T) {
	got := Format(article(), Harvard).Text()
	want := "Doe, J.M. and Sartre, J.-P. (2021) ‘Social media and adolescent wellbeing’, Journal of Child Psychology, 62(4), pp. 100–110. Available at: https://doi.org/10.1111/jcpp.1."
	if got != want {
		t.Fatalf("Harvard:\n got  %q\n want %q", got, want)
	}
}

func TestMissingMetadataIsNotInvented(t *testing.T) {
	p := models.Publication{Title: "An untraceable report", Type: "report"}
	apa := Format(p, APA).Text()
	if apa != "An untraceable report. (n.d.)." {
		t.Fatalf("APA missing metadata: %q", apa)
	}
	harv := Format(p, Harvard).Text()
	if harv != "‘An untraceable report’ (no date)." {
		t.Fatalf("Harvard missing metadata: %q", harv)
	}
	if strings.Contains(apa+harv, "doi") {
		t.Fatal("must not invent a DOI")
	}
}

func TestBookAndChapter(t *testing.T) {
	book := models.Publication{Title: "Thinking, fast and slow", Authors: []models.Author{{Given: "Daniel", Family: "Kahneman"}},
		Year: 2011, Publisher: "Farrar, Straus and Giroux", Type: "book"}
	if got := Format(book, APA).Text(); got != "Kahneman, D. (2011). Thinking, fast and slow. Farrar, Straus and Giroux." {
		t.Fatalf("APA book: %q", got)
	}
	if got := string(Format(book, APA).HTML()); !strings.Contains(got, "<em>Thinking, fast and slow</em>") {
		t.Fatalf("APA book title should be italic: %s", got)
	}
	if got := Format(book, Harvard).Text(); got != "Kahneman, D. (2011) Thinking, fast and slow. Farrar, Straus and Giroux." {
		t.Fatalf("Harvard book: %q", got)
	}
	ch := models.Publication{Title: "A chapter", Authors: []models.Author{{Given: "A", Family: "Author"}}, Year: 2000,
		Venue: "The Big Book", Pages: "5-9", Publisher: "Press", Type: "book-chapter"}
	if got := Format(ch, APA).Text(); got != "Author, A. (2000). A chapter. In The Big Book (pp. 5–9). Press." {
		t.Fatalf("APA chapter: %q", got)
	}
	if got := Format(ch, Harvard).Text(); got != "Author, A. (2000) ‘A chapter’, in The Big Book. Press, pp. 5–9." {
		t.Fatalf("Harvard chapter: %q", got)
	}
}

func TestAuthorLists(t *testing.T) {
	var many []models.Author
	for i := 1; i <= 22; i++ {
		many = append(many, models.Author{Given: "X", Family: fmt.Sprintf("A%d", i)})
	}
	apa := apaAuthors(many)
	if !strings.Contains(apa, "A19, X., . . . A22, X.") || strings.Contains(apa, "A20") {
		t.Fatalf("APA 21+ rule: %q", apa)
	}
	if got := harvardAuthors(many[:4]); got != "A1, X. et al." {
		t.Fatalf("Harvard 4+: %q", got)
	}
	if got := harvardAuthors(many[:3]); got != "A1, X., A2, X. and A3, X." {
		t.Fatalf("Harvard 3: %q", got)
	}
	org := []models.Author{{Literal: "World Health Organization"}}
	p := models.Publication{Title: "Report", Authors: org, Year: 2020}
	if got := Format(p, APA).Text(); !strings.HasPrefix(got, "World Health Organization. (2020).") {
		t.Fatalf("APA organisation author: %q", got)
	}
}

func TestInText(t *testing.T) {
	p := article()
	if got := InText(p, APA); got != "(Doe & Sartre, 2021)" {
		t.Fatalf("APA in-text: %q", got)
	}
	if got := InText(p, Harvard); got != "(Doe and Sartre, 2021)" {
		t.Fatalf("Harvard in-text: %q", got)
	}
	p.Authors = append(p.Authors, models.Author{Family: "Lee", Given: "B"})
	if got := InText(p, APA); got != "(Doe et al., 2021)" {
		t.Fatalf("APA 3 authors: %q", got)
	}
	if got := InText(models.Publication{Title: "One two three four five"}, APA); got != "(‘One two three four …’, n.d.)" {
		t.Fatalf("APA no author: %q", got)
	}
}

func TestHTMLEscapes(t *testing.T) {
	p := models.Publication{Title: `<script>alert(1)</script>`, Venue: "A & B"}
	h := string(Format(p, APA).HTML())
	if strings.Contains(h, "<script>") || !strings.Contains(h, "A &amp; B") {
		t.Fatalf("HTML not escaped: %s", h)
	}
}
