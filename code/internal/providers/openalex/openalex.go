// Package openalex is a client for the OpenAlex works API.
package openalex

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"citewise/internal/models"
	"citewise/internal/providers"
	"citewise/internal/providers/httpx"
)

// DefaultBaseURL is the public OpenAlex API.
const DefaultBaseURL = "https://api.openalex.org"

// Client queries OpenAlex.
type Client struct {
	BaseURL string
	APIKey  string
	Mailto  string
	http    *httpx.Client
}

// New returns an OpenAlex client. apiKey and mailto are optional.
func New(apiKey, mailto string) *Client {
	return &Client{BaseURL: DefaultBaseURL, APIKey: apiKey, Mailto: mailto, http: httpx.New("OpenAlex", 20*time.Second)}
}

// Name implements providers.Provider.
func (c *Client) Name() string { return "openalex" }

// Search implements providers.Provider.
func (c *Client) Search(ctx context.Context, q providers.Query) ([]models.Publication, error) {
	rows := q.Rows
	if rows <= 0 || rows > 50 {
		rows = 20
	}
	v := url.Values{}
	v.Set("search", q.Text)
	v.Set("per-page", strconv.Itoa(rows))
	v.Set("select", "id,doi,title,display_name,publication_year,authorships,primary_location,abstract_inverted_index,type,biblio")
	filters := []string{"type:article|book|book-chapter|review|preprint|dissertation|report"}
	switch {
	case q.YearFrom > 0 && q.YearTo > 0:
		filters = append(filters, fmt.Sprintf("publication_year:%d-%d", q.YearFrom, q.YearTo))
	case q.YearFrom > 0:
		filters = append(filters, fmt.Sprintf("publication_year:>%d", q.YearFrom-1))
	case q.YearTo > 0:
		filters = append(filters, fmt.Sprintf("publication_year:<%d", q.YearTo+1))
	}
	v.Set("filter", strings.Join(filters, ","))
	if c.APIKey != "" {
		v.Set("api_key", c.APIKey)
	}
	if c.Mailto != "" {
		v.Set("mailto", c.Mailto)
	}
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/works?" + v.Encode()

	var resp response
	err := c.http.Do(ctx, func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "CiteWiseAI/1.0")
		req.Header.Set("Accept", "application/json")
		return req, nil
	}, &resp)
	if err != nil {
		return nil, err
	}
	return Normalize(resp), nil
}

type work struct {
	ID              string `json:"id"`
	DOI             string `json:"doi"`
	Title           string `json:"title"`
	DisplayName     string `json:"display_name"`
	PublicationYear int    `json:"publication_year"`
	Type            string `json:"type"`
	Authorships     []struct {
		Author struct {
			DisplayName string `json:"display_name"`
		} `json:"author"`
		RawAuthorName string `json:"raw_author_name"`
	} `json:"authorships"`
	PrimaryLocation *struct {
		LandingPageURL string `json:"landing_page_url"`
		Source         *struct {
			DisplayName          string `json:"display_name"`
			HostOrganizationName string `json:"host_organization_name"`
		} `json:"source"`
	} `json:"primary_location"`
	AbstractInvertedIndex map[string][]int `json:"abstract_inverted_index"`
	Biblio                struct {
		Volume    string `json:"volume"`
		Issue     string `json:"issue"`
		FirstPage string `json:"first_page"`
		LastPage  string `json:"last_page"`
	} `json:"biblio"`
}

type response struct {
	Results []work `json:"results"`
}

// typeMap converts OpenAlex work types to the Crossref-style vocabulary used
// internally.
var typeMap = map[string]string{
	"article":      "journal-article",
	"review":       "journal-article",
	"book":         "book",
	"book-chapter": "book-chapter",
	"preprint":     "posted-content",
	"dissertation": "dissertation",
	"report":       "report",
}

// Normalize converts an OpenAlex response into publications, skipping records
// without a title.
func Normalize(r response) []models.Publication {
	out := make([]models.Publication, 0, len(r.Results))
	for _, w := range r.Results {
		title := w.Title
		if title == "" {
			title = w.DisplayName
		}
		title = models.StripTags(title)
		if title == "" {
			continue
		}
		p := models.Publication{
			DOI:      models.NormalizeDOI(w.DOI),
			Title:    title,
			Year:     w.PublicationYear,
			Volume:   strings.TrimSpace(w.Biblio.Volume),
			Issue:    strings.TrimSpace(w.Biblio.Issue),
			Type:     typeMap[w.Type],
			Abstract: ReconstructAbstract(w.AbstractInvertedIndex),
			Source:   "openalex",
		}
		if p.Type == "" {
			p.Type = w.Type
		}
		switch first, last := strings.TrimSpace(w.Biblio.FirstPage), strings.TrimSpace(w.Biblio.LastPage); {
		case first != "" && last != "" && first != last:
			p.Pages = first + "-" + last
		case first != "":
			p.Pages = first
		}
		if pl := w.PrimaryLocation; pl != nil {
			p.URL = pl.LandingPageURL
			if pl.Source != nil {
				p.Venue = strings.TrimSpace(pl.Source.DisplayName)
				p.Publisher = strings.TrimSpace(pl.Source.HostOrganizationName)
			}
		}
		if p.URL == "" {
			p.URL = w.ID
		}
		for _, a := range w.Authorships {
			name := strings.TrimSpace(a.Author.DisplayName)
			if name == "" {
				name = strings.TrimSpace(a.RawAuthorName)
			}
			if name != "" {
				p.Authors = append(p.Authors, SplitName(name))
			}
		}
		out = append(out, p)
	}
	return out
}

// ReconstructAbstract rebuilds abstract text from OpenAlex's inverted index.
func ReconstructAbstract(idx map[string][]int) string {
	if len(idx) == 0 {
		return ""
	}
	type wp struct {
		pos  int
		word string
	}
	var words []wp
	for w, positions := range idx {
		for _, p := range positions {
			words = append(words, wp{p, w})
		}
	}
	sort.Slice(words, func(i, j int) bool { return words[i].pos < words[j].pos })
	parts := make([]string, len(words))
	for i, w := range words {
		parts[i] = w.word
	}
	return models.StripTags(strings.Join(parts, " "))
}

var particles = map[string]bool{"van": true, "von": true, "der": true, "den": true, "de": true, "del": true, "della": true, "da": true, "di": true, "du": true, "la": true, "le": true, "dos": true, "das": true, "bin": true, "al": true}

// SplitName splits an OpenAlex display name ("Given Middle Family") into
// given and family parts. Lowercase particles stay with the family name.
// Single-word names are kept literal.
func SplitName(name string) models.Author {
	fields := strings.Fields(name)
	if len(fields) < 2 {
		return models.Author{Literal: name}
	}
	familyStart := len(fields) - 1
	for familyStart > 1 && particles[fields[familyStart-1]] {
		familyStart--
	}
	return models.Author{
		Given:  strings.Join(fields[:familyStart], " "),
		Family: strings.Join(fields[familyStart:], " "),
	}
}
