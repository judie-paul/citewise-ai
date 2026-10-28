package essay

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"citewise/internal/ai"
	"citewise/internal/models"
	"citewise/internal/providers"
	"citewise/internal/search"
)

type fakeProvider struct {
	mu   sync.Mutex
	err  error
	seen []string
}

func (f *fakeProvider) Name() string { return "crossref" }
func (f *fakeProvider) Search(_ context.Context, q providers.Query) ([]models.Publication, error) {
	f.mu.Lock()
	f.seen = append(f.seen, q.Text)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	switch {
	case strings.Contains(q.Text, "maize"):
		return []models.Publication{
			{DOI: "10.1/maize", Title: "Warming and maize yields in Africa", Year: 2019, Authors: []models.Author{{Family: "Lobell"}}},
			{DOI: "10.1/maize2", Title: "Maize production under drought", Year: 2015},
		}, nil
	case strings.Contains(q.Text, "irrigation"):
		return []models.Publication{{DOI: "10.1/irr", Title: "Irrigation expansion and adaptation", Year: 2020}}, nil
	}
	return nil, nil
}

type fakeAnalyzer struct {
	analysis  ai.Analysis
	err       error
	assessErr error
	gotItems  []ai.ClaimCandidates
}

func (f *fakeAnalyzer) AnalyzeEssay(context.Context, []ai.Section) (ai.Analysis, error) {
	return f.analysis, f.err
}
func (f *fakeAnalyzer) AssessRelevance(_ context.Context, items []ai.ClaimCandidates) (map[int][]ai.Match, error) {
	f.gotItems = items
	if f.assessErr != nil {
		return nil, f.assessErr
	}
	out := map[int][]ai.Match{}
	for _, it := range items {
		out[it.ClaimIndex] = []ai.Match{{ID: it.Candidates[0].ID, Reason: "Directly addresses the claim."}}
	}
	return out, nil
}

func analysis() ai.Analysis {
	return ai.Analysis{Topic: "Climate change and African agriculture", Claims: []ai.Claim{
		{Section: 1, Text: "Rising temperatures reduce maize yields.", Query: "maize yields warming"},
		{Section: 2, Text: "Irrigation helps farmers adapt.", Query: "irrigation adaptation"},
		{Section: 2, Text: "Something obscure.", Query: "zzzz qqqq"},
	}}
}

func TestSplit(t *testing.T) {
	paras := []string{strings.Repeat("a", 1500), strings.Repeat("b", 1500), "short", strings.Repeat("Sentence here. ", 400)}
	secs, cut := Split(strings.Join(paras, "\n\n"))
	if cut {
		t.Fatal("should not be cut")
	}
	for i, s := range secs {
		if s.Index != i+1 {
			t.Fatalf("bad index %d at %d", s.Index, i)
		}
		if len([]rune(s.Text)) > SectionChars {
			t.Fatalf("section %d too long: %d", i, len(s.Text))
		}
	}
	if len(secs) < 4 {
		t.Fatalf("expected long paragraph to be split, got %d sections", len(secs))
	}
	many := strings.Repeat(strings.Repeat("x", 2000)+"\n\n", MaxSections+3)
	if secs, cut := Split(many); !cut || len(secs) != MaxSections {
		t.Fatalf("cut=%v len=%d", cut, len(secs))
	}
}

func TestAnalyzeRequiresAI(t *testing.T) {
	s := &Service{Search: &search.Service{}}
	if _, err := s.Analyze(context.Background(), "text", 0, 0); !errors.Is(err, ErrAINotConfigured) {
		t.Fatalf("err = %v", err)
	}
}

func TestAnalyzeMatchesClaims(t *testing.T) {
	fa := &fakeAnalyzer{analysis: analysis()}
	prov := &fakeProvider{}
	s := &Service{AI: fa, Search: &search.Service{Providers: []providers.Provider{prov}}}
	rep, err := s.Analyze(context.Background(), "Para one.\n\nPara two.", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Topic != "Climate change and African agriculture" || len(rep.Claims) != 3 {
		t.Fatalf("report = %+v", rep)
	}
	if len(rep.Claims[0].Matches) != 1 || rep.Claims[0].Matches[0].Publication.DOI != "10.1/maize" {
		t.Fatalf("claim 0 matches = %+v", rep.Claims[0].Matches)
	}
	if rep.Claims[0].Matches[0].Rationale == "" {
		t.Fatal("missing rationale")
	}
	if len(rep.Claims[2].Matches) != 0 {
		t.Fatal("claim without results must have no matches (nothing invented)")
	}
	if len(fa.gotItems) != 2 {
		t.Fatalf("only claims with candidates should be assessed, got %d", len(fa.gotItems))
	}
	ids := map[int]bool{}
	for _, it := range fa.gotItems {
		for _, c := range it.Candidates {
			if ids[c.ID] {
				t.Fatal("candidate IDs must be unique across claims")
			}
			ids[c.ID] = true
		}
	}
}

func TestAnalyzeFallsBackWhenAssessmentFails(t *testing.T) {
	fa := &fakeAnalyzer{analysis: analysis(), assessErr: ai.ErrUnavailable}
	s := &Service{AI: fa, Search: &search.Service{Providers: []providers.Provider{&fakeProvider{}}}}
	rep, err := s.Analyze(context.Background(), "text", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Claims[0].Matches) != 2 || !strings.Contains(rep.Claims[0].Matches[0].Rationale, "Keyword") {
		t.Fatalf("fallback matches = %+v", rep.Claims[0].Matches)
	}
	if len(rep.Warnings) == 0 {
		t.Fatal("expected warning about fallback")
	}
}

func TestAnalyzeErrors(t *testing.T) {
	s := &Service{AI: &fakeAnalyzer{err: ai.ErrUnavailable}, Search: &search.Service{}}
	if _, err := s.Analyze(context.Background(), "text", 0, 0); !errors.Is(err, ai.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	s = &Service{AI: &fakeAnalyzer{analysis: analysis()},
		Search: &search.Service{Providers: []providers.Provider{&fakeProvider{err: errors.New("down")}}}}
	if _, err := s.Analyze(context.Background(), "text", 0, 0); !errors.Is(err, search.ErrProvidersUnavailable) {
		t.Fatalf("err = %v", err)
	}
}
