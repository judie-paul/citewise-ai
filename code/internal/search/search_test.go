package search

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"citewise/internal/models"
	"citewise/internal/providers"
)

type fakeProvider struct {
	mu    sync.Mutex
	name  string
	pubs  []models.Publication
	err   error
	calls []providers.Query
}

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) Search(_ context.Context, q providers.Query) ([]models.Publication, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, q)
	return f.pubs, f.err
}

type fakeAI struct {
	queries []string
	err     error
}

func (f fakeAI) GenerateQueries(context.Context, string, string, int) ([]string, error) {
	return f.queries, f.err
}

func pub(doi, title string, year int, family, source string) models.Publication {
	p := models.Publication{DOI: doi, Title: title, Year: year, Source: source}
	if family != "" {
		p.Authors = []models.Author{{Given: "A", Family: family}}
	}
	return p
}

func TestValidate(t *testing.T) {
	ok := Request{Topic: "Climate change and agriculture", Count: 10}
	if errs := ok.Validate(2026); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	cases := map[string]Request{
		"topic":       {Topic: "", Count: 10},
		"count":       {Topic: "valid topic", Count: 0},
		"year_from":   {Topic: "valid topic", Count: 5, YearFrom: 1500},
		"year_to":     {Topic: "valid topic", Count: 5, YearFrom: 2020, YearTo: 2010},
		"description": {Topic: "valid topic", Count: 5, Description: strings.Repeat("x", MaxDescLen+1)},
	}
	for field, req := range cases {
		if errs := req.Validate(2026); errs[field] == "" {
			t.Errorf("expected error on %s, got %v", field, errs)
		}
	}
	if errs := (Request{Topic: "the of and", Count: 5}).Validate(2026); errs["topic"] == "" {
		t.Error("stopword-only topic should be rejected")
	}
	if errs := (Request{Topic: strings.Repeat("a", MaxTopicLen+1), Count: 5}).Validate(2026); errs["topic"] == "" {
		t.Error("overlong topic should be rejected")
	}
}

func TestKeywordsAndFallbackQueries(t *testing.T) {
	got := Keywords("The impact of Social-Media on teenagers' mental health, and social media")
	want := []string{"social-media", "teenagers", "mental", "health", "social", "media"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Keywords = %v, want %v", got, want)
	}
	qs := FallbackQueries("Social media and teen mental health", "Focus on anxiety and sleep")
	if len(qs) != 3 || qs[0] != "Social media and teen mental health" || !strings.Contains(qs[2], "anxiety") {
		t.Fatalf("FallbackQueries = %v", qs)
	}
	if got := CleanQueries([]string{" a  b ", "A B", "", "c"}); !reflect.DeepEqual(got, []string{"a b", "c"}) {
		t.Fatalf("CleanQueries = %v", got)
	}
}

func TestDedupe(t *testing.T) {
	in := []models.Publication{
		pub("10.1/ABC", "Deep learning in medicine", 2020, "Smith", "openalex"),
		pub("https://doi.org/10.1/abc", "Deep Learning in Medicine", 2020, "Smith", "crossref"),
		pub("", "Deep learning in medicine.", 2021, "Smith", "openalex"),  // title fallback, year within 1
		pub("", "Deep learning in medicine", 2010, "Smith", "openalex"),   // year too far: distinct
		pub("", "Deep learning in medicine", 2020, "Jones", "openalex"),   // different author: distinct
		pub("10.9/other", "Deep learning in medicine", 2020, "Smith", ""), // different DOI: distinct
	}
	in[0].Abstract = "short"
	in[1].Venue = "Nature Medicine"
	out := Dedupe(in)
	if len(out) != 4 {
		for _, p := range out {
			t.Logf("%+v", p)
		}
		t.Fatalf("expected 4 unique publications, got %d", len(out))
	}
	m := out[0]
	if m.DOI != "10.1/abc" || m.Venue != "Nature Medicine" || m.Abstract != "short" || m.Source != "crossref+openalex" {
		t.Fatalf("merge incorrect: %+v", m)
	}
}

func TestFilterYears(t *testing.T) {
	in := []models.Publication{pub("", "a", 2000, "", ""), pub("", "b", 2015, "", ""), pub("", "c", 0, "", "")}
	if got := FilterYears(in, 2010, 2020); len(got) != 1 || got[0].Title != "b" {
		t.Fatalf("FilterYears = %+v", got)
	}
	if got := FilterYears(in, 0, 0); len(got) != 3 {
		t.Fatal("no range should keep everything")
	}
}

func TestRank(t *testing.T) {
	pubs := []models.Publication{
		pub("", "Gardening for beginners", 2020, "A", "crossref"),
		pub("", "Social media use and adolescent mental health", 2019, "B", "crossref"),
		pub("", "Adolescent wellbeing", 2021, "C", "openalex"),
	}
	pubs[2].Abstract = "Effects of social media on adolescents."
	ranked := Rank(pubs, Keywords("social media adolescent mental health"), "social media adolescent mental health")
	if len(ranked) != 2 {
		t.Fatalf("irrelevant publication should be dropped, got %d", len(ranked))
	}
	if !strings.HasPrefix(ranked[0].Publication.Title, "Social media use") || ranked[0].Score <= ranked[1].Score {
		t.Fatalf("unexpected order: %+v", ranked)
	}
}

func TestFindFallbackAndCount(t *testing.T) {
	cr := &fakeProvider{name: "crossref", pubs: []models.Publication{
		pub("10.1/a", "Climate change and crop yields", 2018, "Lobell", "crossref"),
		pub("10.1/b", "Climate adaptation in agriculture", 2012, "Howden", "crossref"),
	}}
	oa := &fakeProvider{name: "openalex", pubs: []models.Publication{
		pub("10.1/A", "Climate change and crop yields", 2018, "Lobell", "openalex"),
		pub("", "Unrelated cookery", 2018, "Chef", "openalex"),
	}}
	svc := &Service{Providers: []providers.Provider{cr, oa}}
	res, err := svc.Find(context.Background(), Request{Topic: "Climate change agriculture", Count: 1, YearFrom: 2015})
	if err != nil {
		t.Fatal(err)
	}
	if res.AIUsed || len(res.Queries) == 0 {
		t.Fatalf("expected keyword fallback: %+v", res)
	}
	if len(res.Items) != 1 || res.Items[0].Publication.DOI != "10.1/a" {
		t.Fatalf("unexpected items: %+v", res.Items)
	}
	if cr.calls[0].YearFrom != 2015 {
		t.Fatal("year filter not passed to provider")
	}
}

func TestFindWithAIAndFailures(t *testing.T) {
	good := &fakeProvider{name: "crossref", pubs: []models.Publication{pub("10.1/a", "Urban heat islands", 2020, "Oke", "crossref")}}
	bad := &fakeProvider{name: "openalex", err: errors.New("boom")}
	svc := &Service{Providers: []providers.Provider{good, bad}, AI: fakeAI{queries: []string{"urban heat island mitigation", "urban heat islands"}}}
	res, err := svc.Find(context.Background(), Request{Topic: "Urban heat islands", Count: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !res.AIUsed || res.Queries[0] != "Urban heat islands" || len(res.Queries) != 2 {
		t.Fatalf("AI queries not used correctly: %+v", res.Queries)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "OpenAlex") {
		t.Fatalf("expected OpenAlex warning: %v", res.Warnings)
	}

	svc.AI = fakeAI{err: errors.New("quota")}
	res, err = svc.Find(context.Background(), Request{Topic: "Urban heat islands", Count: 5})
	if err != nil || res.AIUsed || len(res.Warnings) != 2 {
		t.Fatalf("AI failure should fall back: err=%v %+v", err, res)
	}

	svc.Providers = []providers.Provider{bad}
	if _, err := svc.Find(context.Background(), Request{Topic: "Urban heat islands", Count: 5}); !errors.Is(err, ErrProvidersUnavailable) {
		t.Fatalf("expected ErrProvidersUnavailable, got %v", err)
	}
}

func TestFindNoResultsIsNotAnError(t *testing.T) {
	svc := &Service{Providers: []providers.Provider{&fakeProvider{name: "crossref"}}}
	res, err := svc.Find(context.Background(), Request{Topic: "zxqv nonsense", Count: 5})
	if err != nil || len(res.Items) != 0 {
		t.Fatalf("err=%v items=%d", err, len(res.Items))
	}
}

type lookupProvider struct {
	fakeProvider
	pub models.Publication
	err error
}

func (l *lookupProvider) LookupDOI(context.Context, string) (models.Publication, error) {
	return l.pub, l.err
}

func TestLookupDOI(t *testing.T) {
	found := models.Publication{DOI: "10.5281/zenodo.42", Title: "Found in OpenAlex"}
	notFound := &lookupProvider{fakeProvider: fakeProvider{name: "crossref"}, err: providers.ErrNotFound}
	down := &lookupProvider{fakeProvider: fakeProvider{name: "crossref"}, err: errors.New("timeout")}
	hit := &lookupProvider{fakeProvider: fakeProvider{name: "openalex"}, pub: found}
	miss := &lookupProvider{fakeProvider: fakeProvider{name: "openalex"}, err: providers.ErrNotFound}
	searchOnly := &fakeProvider{name: "other"}
	mismatch := &lookupProvider{fakeProvider: fakeProvider{name: "openalex"}, pub: models.Publication{DOI: "10.65215/other-version", Title: "Merged record"}}

	cases := []struct {
		name  string
		provs []providers.Provider
		want  error
	}{
		{"falls back to second provider", []providers.Provider{searchOnly, notFound, hit}, nil},
		{"second provider succeeds after failure", []providers.Provider{down, hit}, nil},
		{"all say not found", []providers.Provider{notFound, miss}, ErrDOINotFound},
		{"one down, other not found", []providers.Provider{down, miss}, ErrProvidersUnavailable},
		{"no lookup providers", []providers.Provider{searchOnly}, ErrDOINotFound},
		{"record with a different DOI is rejected", []providers.Provider{notFound, mismatch}, ErrDOINotFound},
		{"DOI comparison ignores case and prefix", []providers.Provider{&lookupProvider{fakeProvider: fakeProvider{name: "crossref"}, pub: models.Publication{DOI: "https://doi.org/10.5281/ZENODO.42", Title: "Found in OpenAlex"}}}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pub, err := (&Service{Providers: c.provs}).LookupDOI(context.Background(), "10.5281/zenodo.42")
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if c.want == nil && pub.Title != "Found in OpenAlex" {
				t.Fatalf("pub = %+v", pub)
			}
		})
	}
}
