package crossref

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"citewise/internal/providers"
)

const fixture = `{"status":"ok","message":{"items":[
 {"DOI":"10.1111/JCPP.1","title":["Social <i>media</i> and teens"],
  "author":[{"given":"Jane","family":"Doe"},{"name":"WHO Consortium"}],
  "issued":{"date-parts":[[2021,3,1]]},"container-title":["Journal of Child Psychology"],
  "URL":"https://doi.org/10.1111/jcpp.1","abstract":"<jats:p>We study teens.</jats:p>",
  "type":"journal-article","volume":"62","issue":"4","page":"100-110","publisher":"Wiley"},
 {"DOI":"10.1/notitle","title":[],"type":"journal-article"},
 {"DOI":"10.2/b","title":["Undated book"],"issued":{"date-parts":[[null]]},
  "published-online":{"date-parts":[[2019]]},"type":"book","publisher":"Press"}
]}}`

func newTestClient(url string) *Client {
	c := New("me@example.com")
	c.BaseURL = url
	c.http.Backoff = time.Millisecond
	return c
}

func TestSearchParsesAndSendsParams(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		if !strings.Contains(r.Header.Get("User-Agent"), "mailto:me@example.com") {
			t.Errorf("missing polite user agent: %q", r.Header.Get("User-Agent"))
		}
		w.Write([]byte(fixture))
	}))
	defer srv.Close()

	pubs, err := newTestClient(srv.URL).Search(context.Background(), providers.Query{Text: "social media", Rows: 5, YearFrom: 2010, YearTo: 2022})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"query=social+media", "rows=5", "from-pub-date%3A2010", "until-pub-date%3A2022", "mailto=me%40example.com"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}
	if len(pubs) != 2 {
		t.Fatalf("expected 2 publications (untitled skipped), got %d", len(pubs))
	}
	p := pubs[0]
	if p.DOI != "10.1111/jcpp.1" || p.Title != "Social media and teens" || p.Year != 2021 {
		t.Fatalf("bad normalization: %+v", p)
	}
	if p.Venue != "Journal of Child Psychology" || p.Pages != "100-110" || p.Abstract != "We study teens." {
		t.Fatalf("bad fields: %+v", p)
	}
	if len(p.Authors) != 2 || p.Authors[0].Family != "Doe" || p.Authors[1].Literal != "WHO Consortium" {
		t.Fatalf("bad authors: %+v", p.Authors)
	}
	if pubs[1].Year != 2019 {
		t.Fatalf("expected fallback year 2019, got %d", pubs[1].Year)
	}
}

func TestSearchRetriesOnServerError(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(fixture))
	}))
	defer srv.Close()
	pubs, err := newTestClient(srv.URL).Search(context.Background(), providers.Query{Text: "x"})
	if err != nil || len(pubs) != 2 || calls.Load() != 2 {
		t.Fatalf("err=%v pubs=%d calls=%d", err, len(pubs), calls.Load())
	}
}

func TestSearchDoesNotRetryClientError(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	_, err := newTestClient(srv.URL).Search(context.Background(), providers.Query{Text: "x"})
	if err == nil || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
	if !strings.Contains(err.Error(), "Crossref returned HTTP 400") {
		t.Fatalf("unexpected error text: %v", err)
	}
}

func TestSearchInvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>not json"))
	}))
	defer srv.Close()
	if _, err := newTestClient(srv.URL).Search(context.Background(), providers.Query{Text: "x"}); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestSearchHonoursCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := newTestClient(srv.URL).Search(ctx, providers.Query{Text: "x"}); err == nil {
		t.Fatal("expected cancellation error")
	}
}
