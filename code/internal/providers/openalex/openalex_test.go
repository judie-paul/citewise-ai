package openalex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"citewise/internal/providers"
)

const fixture = `{"meta":{"count":2},"results":[
 {"id":"https://openalex.org/W1","doi":"https://doi.org/10.1111/OBR.13631",
  "title":"Associations between social media and diet","publication_year":2023,"type":"review",
  "authorships":[{"author":{"display_name":"Laurence Blanchard"}},{"author":{"display_name":"Ludwig van Beethoven"}},{"author":{"display_name":"Plato"}}],
  "primary_location":{"landing_page_url":"https://example.org/a","source":{"display_name":"Obesity Reviews","host_organization_name":"Wiley"}},
  "abstract_inverted_index":{"Social":[0],"media":[1,4],"affects":[2],"diet.":[5],"and":[3]},
  "biblio":{"volume":"24","issue":"12","first_page":"e1","last_page":"e1"}},
 {"id":"https://openalex.org/W2","doi":null,"title":null,"display_name":"Fallback title","publication_year":2015,"type":"book",
  "authorships":[],"primary_location":null,"biblio":{"first_page":"5","last_page":"9"}},
 {"id":"https://openalex.org/W3","title":"","display_name":""}
]}`

func TestSearchParsesAndSendsParams(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Write([]byte(fixture))
	}))
	defer srv.Close()
	c := New("secret-key", "")
	c.BaseURL = srv.URL
	c.http.Backoff = time.Millisecond

	pubs, err := c.Search(context.Background(), providers.Query{Text: "social media diet", Rows: 3, YearFrom: 2010, YearTo: 2024})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"search=social+media+diet", "per-page=3", "publication_year%3A2010-2024", "api_key=secret-key"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}
	if len(pubs) != 2 {
		t.Fatalf("expected 2 publications, got %d", len(pubs))
	}
	p := pubs[0]
	if p.DOI != "10.1111/obr.13631" || p.Year != 2023 || p.Venue != "Obesity Reviews" || p.Publisher != "Wiley" {
		t.Fatalf("bad normalization: %+v", p)
	}
	if p.Type != "journal-article" || p.Pages != "e1" || p.Volume != "24" {
		t.Fatalf("bad biblio/type: %+v", p)
	}
	if p.Abstract != "Social media affects and media diet." {
		t.Fatalf("bad abstract: %q", p.Abstract)
	}
	if p.Authors[0].Family != "Blanchard" || p.Authors[1].Family != "van Beethoven" || p.Authors[2].Literal != "Plato" {
		t.Fatalf("bad authors: %+v", p.Authors)
	}
	if pubs[1].Title != "Fallback title" || pubs[1].Pages != "5-9" || pubs[1].URL != "https://openalex.org/W2" || pubs[1].DOI != "" {
		t.Fatalf("bad fallback record: %+v", pubs[1])
	}
}

func TestYearFilterOpenRanges(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("filter")
		w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()
	c := New("", "")
	c.BaseURL = srv.URL
	c.Search(context.Background(), providers.Query{Text: "x", YearFrom: 2018})
	if !strings.Contains(gotQuery, "publication_year:>2017") {
		t.Fatalf("filter = %q", gotQuery)
	}
	c.Search(context.Background(), providers.Query{Text: "x", YearTo: 2000})
	if !strings.Contains(gotQuery, "publication_year:<2001") {
		t.Fatalf("filter = %q", gotQuery)
	}
}
