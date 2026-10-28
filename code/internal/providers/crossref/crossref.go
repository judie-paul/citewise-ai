// Package crossref is a client for the Crossref REST API works endpoint.
package crossref

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"citewise/internal/models"
	"citewise/internal/providers"
	"citewise/internal/providers/httpx"
)

// DefaultBaseURL is the public Crossref API.
const DefaultBaseURL = "https://api.crossref.org"

// Scholarly work types worth citing; others (peer reviews, components,
// datasets) are excluded.
var workTypes = []string{"journal-article", "book", "book-chapter", "monograph", "edited-book",
	"proceedings-article", "report", "posted-content", "dissertation", "reference-book"}

// Client queries Crossref.
type Client struct {
	BaseURL string
	Mailto  string
	http    *httpx.Client
}

// New returns a Crossref client. mailto (optional) opts into Crossref's
// polite pool.
func New(mailto string) *Client {
	return &Client{BaseURL: DefaultBaseURL, Mailto: mailto, http: httpx.New("Crossref", 20*time.Second)}
}

// Name implements providers.Provider.
func (c *Client) Name() string { return "crossref" }

// Search implements providers.Provider.
func (c *Client) Search(ctx context.Context, q providers.Query) ([]models.Publication, error) {
	rows := q.Rows
	if rows <= 0 || rows > 50 {
		rows = 20
	}
	v := url.Values{}
	v.Set("query", q.Text)
	v.Set("rows", strconv.Itoa(rows))
	v.Set("select", "DOI,title,author,issued,published-print,published-online,container-title,URL,abstract,type,volume,issue,page,publisher")
	filters := make([]string, 0, len(workTypes)+2)
	for _, t := range workTypes {
		filters = append(filters, "type:"+t)
	}
	if q.YearFrom > 0 {
		filters = append(filters, fmt.Sprintf("from-pub-date:%d", q.YearFrom))
	}
	if q.YearTo > 0 {
		filters = append(filters, fmt.Sprintf("until-pub-date:%d", q.YearTo))
	}
	v.Set("filter", strings.Join(filters, ","))
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
		ua := "CiteWiseAI/1.0"
		if c.Mailto != "" {
			ua += " (mailto:" + c.Mailto + ")"
		}
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Accept", "application/json")
		return req, nil
	}, &resp)
	if err != nil {
		return nil, err
	}
	return Normalize(resp), nil
}

type dateParts struct {
	DateParts [][]any `json:"date-parts"`
}

func (d *dateParts) year() int {
	if d == nil || len(d.DateParts) == 0 || len(d.DateParts[0]) == 0 {
		return 0
	}
	switch y := d.DateParts[0][0].(type) {
	case float64:
		return int(y)
	case string:
		n, _ := strconv.Atoi(y)
		return n
	}
	return 0
}

type item struct {
	DOI            string     `json:"DOI"`
	Title          []string   `json:"title"`
	Author         []author   `json:"author"`
	Issued         *dateParts `json:"issued"`
	PublishedPrint *dateParts `json:"published-print"`
	PublishedOnl   *dateParts `json:"published-online"`
	Container      []string   `json:"container-title"`
	URL            string     `json:"URL"`
	Abstract       string     `json:"abstract"`
	Type           string     `json:"type"`
	Volume         string     `json:"volume"`
	Issue          string     `json:"issue"`
	Page           string     `json:"page"`
	Publisher      string     `json:"publisher"`
}

type author struct {
	Given  string `json:"given"`
	Family string `json:"family"`
	Name   string `json:"name"`
}

type response struct {
	Status  string `json:"status"`
	Message struct {
		Items []item `json:"items"`
	} `json:"message"`
}

// Normalize converts a Crossref response into publications, skipping records
// without a title.
func Normalize(r response) []models.Publication {
	out := make([]models.Publication, 0, len(r.Message.Items))
	for _, it := range r.Message.Items {
		title := ""
		if len(it.Title) > 0 {
			title = models.StripTags(it.Title[0])
		}
		if title == "" {
			continue
		}
		year := it.Issued.year()
		if year == 0 {
			year = it.PublishedPrint.year()
		}
		if year == 0 {
			year = it.PublishedOnl.year()
		}
		p := models.Publication{
			DOI:       models.NormalizeDOI(it.DOI),
			Title:     title,
			Year:      year,
			Volume:    strings.TrimSpace(it.Volume),
			Issue:     strings.TrimSpace(it.Issue),
			Pages:     strings.TrimSpace(it.Page),
			Publisher: strings.TrimSpace(it.Publisher),
			Type:      it.Type,
			URL:       it.URL,
			Abstract:  models.StripTags(it.Abstract),
			Source:    "crossref",
		}
		if len(it.Container) > 0 {
			p.Venue = models.StripTags(it.Container[0])
		}
		for _, a := range it.Author {
			switch {
			case strings.TrimSpace(a.Family) != "":
				p.Authors = append(p.Authors, models.Author{Given: strings.TrimSpace(a.Given), Family: strings.TrimSpace(a.Family)})
			case strings.TrimSpace(a.Name) != "":
				p.Authors = append(p.Authors, models.Author{Literal: strings.TrimSpace(a.Name)})
			}
		}
		out = append(out, p)
	}
	return out
}
