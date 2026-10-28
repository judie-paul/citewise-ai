// Package providers defines the interface implemented by academic search
// providers (Crossref, OpenAlex).
package providers

import (
	"context"
	"errors"

	"citewise/internal/models"
)

// ErrNotFound is returned by a lookup when the provider has no such record.
var ErrNotFound = errors.New("no record found")

// Query is a provider-independent search request.
type Query struct {
	Text     string
	Rows     int
	YearFrom int // 0 = no lower bound
	YearTo   int // 0 = no upper bound
}

// Provider searches an academic index and returns normalized publications.
type Provider interface {
	Name() string
	Search(ctx context.Context, q Query) ([]models.Publication, error)
}

// DOILookuper is implemented by providers that can fetch a single record by
// DOI. doi must already be normalized (models.NormalizeDOI).
type DOILookuper interface {
	LookupDOI(ctx context.Context, doi string) (models.Publication, error)
}
