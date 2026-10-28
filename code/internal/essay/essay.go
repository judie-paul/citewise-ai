// Package essay turns an essay's text into claim-by-claim citation
// suggestions: AI analysis identifies claims, providers are searched per
// claim, and the AI assesses which retrieved publications fit each claim.
package essay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"citewise/internal/ai"
	"citewise/internal/models"
	"citewise/internal/search"
)

// Limits on how much of an essay is sent to the AI.
const (
	SectionChars = 2500
	MaxSections  = 12
	rowsPerClaim = 8
	parallelism  = 3
)

// ErrAINotConfigured is returned when no AI analyzer is available.
var ErrAINotConfigured = errors.New("AI-assisted essay analysis requires an OpenAI API key (OPENAI_API_KEY)")

// Analyzer is implemented by the AI client.
type Analyzer interface {
	AnalyzeEssay(ctx context.Context, sections []ai.Section) (ai.Analysis, error)
	AssessRelevance(ctx context.Context, items []ai.ClaimCandidates) (map[int][]ai.Match, error)
}

// Match is a publication suggested for a claim.
type Match struct {
	Publication models.Publication
	Score       float64
	Rationale   string
}

// ClaimResult is one claim with its suggested publications.
type ClaimResult struct {
	Index   int
	Section int
	Claim   string
	Query   string
	Matches []Match
}

// Report is the outcome of analysing an essay.
type Report struct {
	Topic    string
	Claims   []ClaimResult
	Queries  []string
	Warnings []string
}

// Service analyses essays.
type Service struct {
	AI     Analyzer // nil when AI is not configured
	Search *search.Service
	Log    *slog.Logger
}

// Split divides text into sections of roughly SectionChars characters at
// paragraph boundaries. It returns the sections and whether the text was cut
// to MaxSections.
func Split(text string) ([]ai.Section, bool) {
	var (
		out     []ai.Section
		current strings.Builder
	)
	flush := func() {
		if s := strings.TrimSpace(current.String()); s != "" {
			out = append(out, ai.Section{Index: len(out) + 1, Text: s})
		}
		current.Reset()
	}
	for _, para := range strings.Split(text, "\n\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		// Very long paragraphs are split on sentence-ish boundaries.
		for len([]rune(para)) > SectionChars {
			r := []rune(para)
			cut := strings.LastIndex(string(r[:SectionChars]), ". ")
			if cut <= 0 {
				cut = len(string(r[:SectionChars]))
			} else {
				cut++
			}
			if current.Len() > 0 {
				flush()
			}
			current.WriteString(para[:cut])
			flush()
			para = strings.TrimSpace(para[cut:])
		}
		if current.Len() > 0 && len([]rune(current.String()))+len([]rune(para)) > SectionChars {
			flush()
		}
		if current.Len() > 0 {
			current.WriteString("\n\n")
		}
		current.WriteString(para)
	}
	flush()
	if len(out) > MaxSections {
		return out[:MaxSections], true
	}
	return out, false
}

// Analyze runs the full essay pipeline on extracted text.
func (s *Service) Analyze(ctx context.Context, text string, yearFrom, yearTo int) (Report, error) {
	if s.AI == nil {
		return Report{}, ErrAINotConfigured
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	var rep Report
	sections, cut := Split(text)
	if len(sections) == 0 {
		return rep, errors.New("the essay contains no text")
	}
	if cut {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("The essay is long, so only the first %d sections (about %d characters) were analysed.", MaxSections, MaxSections*SectionChars))
	}

	analysis, err := s.AI.AnalyzeEssay(ctx, sections)
	if err != nil {
		return rep, err
	}
	rep.Topic = analysis.Topic
	if rep.Topic == "" {
		rep.Topic = "Untitled essay"
	}
	for i, c := range analysis.Claims {
		rep.Claims = append(rep.Claims, ClaimResult{Index: i, Section: c.Section, Claim: c.Text, Query: c.Query})
		rep.Queries = append(rep.Queries, c.Query)
	}
	if len(rep.Claims) == 0 {
		rep.Warnings = append(rep.Warnings, "No specific claims needing citations were identified in the essay.")
		return rep, nil
	}

	candidates, failures := s.searchClaims(ctx, rep.Claims, yearFrom, yearTo)
	if failures == len(rep.Claims) {
		return rep, search.ErrProvidersUnavailable
	}
	if failures > 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("Academic searches failed for %d claim(s); those claims may have no suggestions.", failures))
	}

	// Offer the AI the top candidates per claim, identified by a global ID.
	var (
		items  []ai.ClaimCandidates
		byID   = map[int]search.Ranked{}
		nextID = 1
	)
	for i, c := range rep.Claims {
		cc := ai.ClaimCandidates{ClaimIndex: i, Claim: c.Claim}
		for _, r := range candidates[i] {
			if len(cc.Candidates) >= ai.MaxCandidates {
				break
			}
			byID[nextID] = r
			cc.Candidates = append(cc.Candidates, ai.Candidate{
				ID: nextID, Title: r.Publication.Title, Year: r.Publication.Year,
				Venue: r.Publication.Venue, Abstract: r.Publication.Abstract,
			})
			nextID++
		}
		if len(cc.Candidates) > 0 {
			items = append(items, cc)
		}
	}
	if len(items) == 0 {
		return rep, nil
	}

	assessed, err := s.AI.AssessRelevance(ctx, items)
	if err != nil {
		s.logger().Warn("AI relevance assessment failed; using keyword ranking", "err", err)
		rep.Warnings = append(rep.Warnings, "The AI relevance check was unavailable, so suggestions are based on keyword matching only. Review them carefully.")
		for _, it := range items {
			for _, cd := range it.Candidates[:min(2, len(it.Candidates))] {
				r := byID[cd.ID]
				rep.Claims[it.ClaimIndex].Matches = append(rep.Claims[it.ClaimIndex].Matches,
					Match{Publication: r.Publication, Score: r.Score, Rationale: "Keyword match (not checked by AI)."})
			}
		}
		return rep, nil
	}
	for idx, matches := range assessed {
		for _, m := range matches {
			r := byID[m.ID]
			rep.Claims[idx].Matches = append(rep.Claims[idx].Matches, Match{Publication: r.Publication, Score: r.Score, Rationale: m.Reason})
		}
	}
	return rep, nil
}

// searchClaims searches the providers for each claim with bounded
// concurrency. It returns ranked candidates per claim index and the number
// of claims whose searches all failed.
func (s *Service) searchClaims(ctx context.Context, claims []ClaimResult, yearFrom, yearTo int) (map[int][]search.Ranked, int) {
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		sem      = make(chan struct{}, parallelism)
		out      = map[int][]search.Ranked{}
		failures int
	)
	for i, c := range claims {
		wg.Add(1)
		go func(i int, c ClaimResult) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			pubs, errs := s.Search.Gather(ctx, []string{c.Query}, rowsPerClaim, yearFrom, yearTo)
			pubs = search.FilterYears(search.Dedupe(pubs), yearFrom, yearTo)
			ranked := search.Rank(pubs, search.Keywords(c.Query+" "+c.Claim), c.Query)
			mu.Lock()
			defer mu.Unlock()
			if len(pubs) == 0 && len(errs) > 0 {
				failures++
			}
			out[i] = ranked
		}(i, c)
	}
	wg.Wait()
	return out, failures
}

func (s *Service) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}
