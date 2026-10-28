// Package search implements reference discovery: query generation, provider
// fan-out, normalization, deduplication, relevance ranking and filtering.
package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"citewise/internal/models"
	"citewise/internal/providers"
)

// Limits on user requests.
const (
	MinTopicLen   = 3
	MaxTopicLen   = 300
	MaxDescLen    = 2000
	MinCount      = 1
	MaxCount      = 50
	MinYear       = 1800
	MaxQueries    = 4
	rowsPerSource = 25
)

// QueryGenerator turns a topic into search queries (implemented by the AI
// client). It must never be relied on for bibliographic data.
type QueryGenerator interface {
	GenerateQueries(ctx context.Context, topic, description string, max int) ([]string, error)
}

// Request is a validated title search.
type Request struct {
	Topic       string
	Description string
	Count       int
	YearFrom    int
	YearTo      int
}

// Validate checks a request and returns user-facing error messages keyed by
// form field.
func (r Request) Validate(currentYear int) map[string]string {
	errs := map[string]string{}
	topicLen := len([]rune(strings.TrimSpace(r.Topic)))
	switch {
	case topicLen == 0:
		errs["topic"] = "Enter an essay title or research topic."
	case topicLen < MinTopicLen:
		errs["topic"] = fmt.Sprintf("The topic must be at least %d characters.", MinTopicLen)
	case topicLen > MaxTopicLen:
		errs["topic"] = fmt.Sprintf("The topic must be at most %d characters.", MaxTopicLen)
	case len(Keywords(r.Topic)) == 0:
		errs["topic"] = "The topic needs at least one meaningful word to search for."
	}
	if len([]rune(r.Description)) > MaxDescLen {
		errs["description"] = fmt.Sprintf("The description must be at most %d characters.", MaxDescLen)
	}
	if r.Count < MinCount || r.Count > MaxCount {
		errs["count"] = fmt.Sprintf("Choose between %d and %d references.", MinCount, MaxCount)
	}
	maxYear := currentYear + 1
	if r.YearFrom != 0 && (r.YearFrom < MinYear || r.YearFrom > maxYear) {
		errs["year_from"] = fmt.Sprintf("The start year must be between %d and %d.", MinYear, maxYear)
	}
	if r.YearTo != 0 && (r.YearTo < MinYear || r.YearTo > maxYear) {
		errs["year_to"] = fmt.Sprintf("The end year must be between %d and %d.", MinYear, maxYear)
	}
	if r.YearFrom != 0 && r.YearTo != 0 && r.YearFrom > r.YearTo && errs["year_from"] == "" && errs["year_to"] == "" {
		errs["year_to"] = "The end year must not be before the start year."
	}
	return errs
}

// Ranked is a publication with its relevance score.
type Ranked struct {
	Publication models.Publication
	Score       float64
}

// Result is the outcome of a title search.
type Result struct {
	Queries  []string
	AIUsed   bool
	Items    []Ranked
	Warnings []string // non-fatal problems shown to the user
}

// ErrProvidersUnavailable means every provider request failed.
var ErrProvidersUnavailable = errors.New("the academic search services could not be reached")

// Service runs searches.
type Service struct {
	Providers []providers.Provider
	AI        QueryGenerator // nil when AI is not configured
	Log       *slog.Logger
	Timeout   time.Duration
}

// Find runs a full title search.
func (s *Service) Find(ctx context.Context, req Request) (Result, error) {
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 45 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var res Result
	res.Queries = FallbackQueries(req.Topic, req.Description)
	if s.AI != nil {
		aiCtx, aiCancel := context.WithTimeout(ctx, 20*time.Second)
		qs, err := s.AI.GenerateQueries(aiCtx, req.Topic, req.Description, MaxQueries)
		aiCancel()
		qs = CleanQueries(qs)
		if err != nil || len(qs) == 0 {
			s.logger().Warn("AI query generation failed; using keyword fallback", "err", err)
			res.Warnings = append(res.Warnings, "AI query generation was unavailable, so a keyword search was used instead.")
		} else {
			// Always keep the user's own topic as the first query.
			res.Queries = CleanQueries(append([]string{strings.TrimSpace(req.Topic)}, qs...))
			res.AIUsed = true
		}
	}
	if len(res.Queries) > MaxQueries {
		res.Queries = res.Queries[:MaxQueries]
	}

	pubs, failures := s.Gather(ctx, res.Queries, rowsPerSource, req.YearFrom, req.YearTo)
	if len(pubs) == 0 && len(failures) > 0 {
		return res, ErrProvidersUnavailable
	}
	res.Warnings = append(res.Warnings, failures...)

	pubs = Dedupe(pubs)
	pubs = FilterYears(pubs, req.YearFrom, req.YearTo)
	ranked := Rank(pubs, Keywords(req.Topic+" "+req.Description), req.Topic)
	if len(ranked) > req.Count {
		ranked = ranked[:req.Count]
	}
	res.Items = ranked
	return res, nil
}

func (s *Service) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Gather runs every query against every provider concurrently. It returns all
// publications found and a de-duplicated list of user-facing failure
// messages (one per failing provider).
func (s *Service) Gather(ctx context.Context, queries []string, rows, yearFrom, yearTo int) ([]models.Publication, []string) {
	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		pubs   []models.Publication
		failed = map[string]bool{}
		okProv = map[string]bool{}
	)
	for _, q := range queries {
		for _, p := range s.Providers {
			wg.Add(1)
			go func(p providers.Provider, q string) {
				defer wg.Done()
				found, err := p.Search(ctx, providers.Query{Text: q, Rows: rows, YearFrom: yearFrom, YearTo: yearTo})
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					s.logger().Warn("provider search failed", "provider", p.Name(), "err", err)
					failed[p.Name()] = true
					return
				}
				okProv[p.Name()] = true
				pubs = append(pubs, found...)
			}(p, q)
		}
	}
	wg.Wait()
	var msgs []string
	for _, p := range s.Providers {
		if failed[p.Name()] && !okProv[p.Name()] {
			msgs = append(msgs, fmt.Sprintf("%s could not be reached, so its results are missing.", displayName(p.Name())))
		}
	}
	return pubs, msgs
}

func displayName(name string) string {
	switch name {
	case "crossref":
		return "Crossref"
	case "openalex":
		return "OpenAlex"
	}
	return name
}

var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a about above after again against all also am an and any are as at be because been
	before being below between both but by can could did do does doing down during each essay few for from further had
	has have having how however i if in into is it its itself just more most my no nor not of off on once only or other
	our out over own same should so some such than that the their them then there these they this those through to too
	under until up very was we were what when where which while who whom why will with would you your role impact
	effect effects analysis study discuss discussion examine explore evaluate critically assess describe towards toward
	within among using use based paper research topic title`) {
		stopwords[w] = true
	}
}

// Keywords extracts distinct, lowercase content words from text in order of
// first appearance.
func Keywords(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-'
	}) {
		w = strings.Trim(w, "-")
		if len([]rune(w)) < 3 || stopwords[w] || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

// FallbackQueries builds keyword queries without AI: the topic itself, its
// keywords, and (when a description is given) topic keywords combined with
// the most important description keywords.
func FallbackQueries(topic, description string) []string {
	qs := []string{strings.TrimSpace(topic)}
	tk := Keywords(topic)
	if len(tk) > 0 {
		qs = append(qs, strings.Join(limit(tk, 8), " "))
	}
	if dk := Keywords(description); len(dk) > 0 {
		combined := append(limit(tk, 4), limit(dk, 5)...)
		qs = append(qs, strings.Join(combined, " "))
	}
	return CleanQueries(qs)
}

func limit(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return append([]string(nil), s...)
}

// CleanQueries trims, length-limits and de-duplicates queries.
func CleanQueries(qs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, q := range qs {
		q = strings.Join(strings.Fields(q), " ")
		if r := []rune(q); len(r) > 200 {
			q = string(r[:200])
		}
		key := strings.ToLower(q)
		if q == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, q)
	}
	return out
}

// Dedupe merges duplicate publications. DOI equality is the primary key; for
// records without a matching DOI, an identical normalized title with
// compatible first author and year is treated as the same work.
func Dedupe(pubs []models.Publication) []models.Publication {
	var out []models.Publication
	byDOI := map[string]int{}
	byTitle := map[string][]int{}
	for _, p := range pubs {
		p.DOI = models.NormalizeDOI(p.DOI)
		idx := -1
		if p.DOI != "" {
			if i, ok := byDOI[p.DOI]; ok {
				idx = i
			}
		}
		tkey := models.NormalizeTitle(p.Title)
		if idx < 0 && tkey != "" {
			for _, i := range byTitle[tkey] {
				if compatible(out[i], p) {
					idx = i
					break
				}
			}
		}
		if idx >= 0 {
			out[idx] = merge(out[idx], p)
			if out[idx].DOI != "" {
				byDOI[out[idx].DOI] = idx
			}
			continue
		}
		out = append(out, p)
		i := len(out) - 1
		if p.DOI != "" {
			byDOI[p.DOI] = i
		}
		if tkey != "" {
			byTitle[tkey] = append(byTitle[tkey], i)
		}
	}
	return out
}

func firstFamily(p models.Publication) string {
	if len(p.Authors) == 0 {
		return ""
	}
	a := p.Authors[0]
	if a.Family != "" {
		return models.NormalizeTitle(a.Family)
	}
	return models.NormalizeTitle(a.Literal)
}

func compatible(a, b models.Publication) bool {
	if a.DOI != "" && b.DOI != "" && a.DOI != b.DOI {
		return false
	}
	if a.Year != 0 && b.Year != 0 && abs(a.Year-b.Year) > 1 {
		return false
	}
	fa, fb := firstFamily(a), firstFamily(b)
	if fa != "" && fb != "" && fa != fb && !strings.Contains(fa, fb) && !strings.Contains(fb, fa) {
		return false
	}
	return true
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// merge fills empty fields of a from b. Crossref records are preferred for
// bibliographic detail because they come from publishers.
func merge(a, b models.Publication) models.Publication {
	if b.Source == "crossref" && a.Source != "crossref" {
		a, b = b, a
	}
	fill := func(dst *string, src string) {
		if *dst == "" {
			*dst = src
		}
	}
	fill(&a.DOI, b.DOI)
	fill(&a.Venue, b.Venue)
	fill(&a.Volume, b.Volume)
	fill(&a.Issue, b.Issue)
	fill(&a.Pages, b.Pages)
	fill(&a.Publisher, b.Publisher)
	fill(&a.Type, b.Type)
	fill(&a.URL, b.URL)
	if len(b.Abstract) > len(a.Abstract) {
		a.Abstract = b.Abstract
	}
	if a.Year == 0 {
		a.Year = b.Year
	}
	if len(a.Authors) == 0 {
		a.Authors = b.Authors
	}
	if a.Source != b.Source && !strings.Contains(a.Source, b.Source) {
		a.Source = a.Source + "+" + b.Source
	}
	return a
}

// FilterYears removes publications outside the range. When a range is set,
// publications with an unknown year are removed because they cannot be shown
// to satisfy it.
func FilterYears(pubs []models.Publication, from, to int) []models.Publication {
	if from == 0 && to == 0 {
		return pubs
	}
	out := pubs[:0:0]
	for _, p := range pubs {
		if p.Year == 0 || (from != 0 && p.Year < from) || (to != 0 && p.Year > to) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// Rank scores publications by keyword overlap and drops those with no overlap
// at all. Title matches weigh most, then abstract and venue; an exact phrase
// match with the topic and presence in both providers add small bonuses.
func Rank(pubs []models.Publication, keywords []string, phrase string) []Ranked {
	if len(keywords) == 0 {
		return nil
	}
	phraseKey := models.NormalizeTitle(phrase)
	out := make([]Ranked, 0, len(pubs))
	for _, p := range pubs {
		title := tokenSet(p.Title)
		abstract := tokenSet(p.Abstract)
		venue := tokenSet(p.Venue)
		var tHits, aHits, vHits int
		for _, k := range keywords {
			if matches(title, k) {
				tHits++
			}
			if matches(abstract, k) {
				aHits++
			}
			if matches(venue, k) {
				vHits++
			}
		}
		if tHits == 0 && aHits == 0 {
			continue
		}
		n := float64(len(keywords))
		score := 3*float64(tHits)/n + 1.5*float64(aHits)/n + 0.5*float64(vHits)/n
		if phraseKey != "" && len(phraseKey) > 8 && strings.Contains(models.NormalizeTitle(p.Title), phraseKey) {
			score += 1
		}
		if strings.Contains(p.Source, "+") {
			score += 0.25
		}
		if p.DOI != "" {
			score += 0.1
		}
		if len(p.Authors) == 0 {
			score -= 0.2
		}
		out = append(out, Ranked{Publication: p, Score: round(score)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Publication.Year > out[j].Publication.Year
	})
	return out
}

func round(f float64) float64 { return float64(int(f*1000+0.5)) / 1000 }

func tokenSet(s string) map[string]bool {
	set := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		set[w] = true
	}
	return set
}

// matches checks a keyword against a token set, allowing simple plural and
// suffix variants (e.g. "teen" ~ "teens", "learning" ~ "learn").
func matches(set map[string]bool, k string) bool {
	for _, part := range strings.Split(k, "-") {
		if len(part) < 3 {
			continue
		}
		if set[part] || set[part+"s"] || set[part+"es"] || set[strings.TrimSuffix(part, "s")] ||
			set[strings.TrimSuffix(part, "ing")] || set[part+"ing"] {
			return true
		}
	}
	return false
}
