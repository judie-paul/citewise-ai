// Package providers defines the interface implemented by academic search
// providers (Crossref, OpenAlex).
package providers

import (
	"context"

	"citewise/internal/models"
)

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
