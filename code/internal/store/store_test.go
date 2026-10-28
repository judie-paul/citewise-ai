package store

import (
	"context"
	"path/filepath"
	"testing"

	"citewise/internal/models"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "nested", "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func samplePub(doi, title string) models.Publication {
	return models.Publication{
		DOI: doi, Title: title, Year: 2020, Venue: "Journal of Tests",
		Authors: []models.Author{{Given: "Ada", Family: "Lovelace"}}, Source: "crossref",
	}
}

func TestOpenCreatesSchemaAndReopens(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "db.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	var fk int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign keys not enabled: %d %v", fk, err)
	}
	s.Close()
	s2, err := Open(path) // schema creation must be idempotent
	if err != nil {
		t.Fatal(err)
	}
	s2.Close()
}

func TestSaveSearchAndResults(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	results := []ResultInput{
		{Publication: samplePub("10.1/A", "First paper"), Score: 2, ClaimIndex: -1},
		{Publication: samplePub("https://doi.org/10.1/a", "First paper (dup)"), Score: 1, ClaimIndex: -1},
		{Publication: samplePub("", "No DOI paper"), Score: 0.5, ClaimIndex: -1},
	}
	id, err := s.SaveSearch(ctx, Search{Topic: "testing", Type: "title", Style: "apa", RequestedCount: 5, Queries: []string{"q1"}}, results)
	if err != nil {
		t.Fatal(err)
	}
	srch, err := s.GetSearch(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if srch.Topic != "testing" || len(srch.Queries) != 1 || srch.CreatedAt.IsZero() {
		t.Fatalf("unexpected search: %+v", srch)
	}
	got, err := s.ListResults(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected DOI duplicate to collapse to 2 results, got %d", len(got))
	}
	if got[0].Publication.DOI != "10.1/a" || got[0].Publication.Authors[0].Family != "Lovelace" {
		t.Fatalf("publication not persisted correctly: %+v", got[0].Publication)
	}
	if _, err := s.GetSearch(ctx, 9999); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	list, err := s.ListSearches(ctx, 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListSearches: %v %d", err, len(list))
	}
}

func TestSavedReferences(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	id, err := s.SaveSearch(ctx, Search{Topic: "t", Type: "title", Style: "harvard"},
		[]ResultInput{{Publication: samplePub("10.2/x", "Saved paper"), ClaimIndex: -1}})
	if err != nil {
		t.Fatal(err)
	}
	res, _ := s.ListResults(ctx, id)
	pubID := res[0].Publication.ID

	if err := s.SaveReference(ctx, pubID); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveReference(ctx, pubID); err != nil { // idempotent
		t.Fatal(err)
	}
	if err := s.SaveReference(ctx, 424242); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound for unknown publication, got %v", err)
	}
	saved, err := s.ListSaved(ctx)
	if err != nil || len(saved) != 1 || saved[0].Publication.Title != "Saved paper" {
		t.Fatalf("ListSaved: %v %+v", err, saved)
	}
	res, _ = s.ListResults(ctx, id)
	if !res[0].Saved {
		t.Fatal("result should be marked saved")
	}
	pubs, err := s.GetPublications(ctx, []int64{pubID, pubID, 999})
	if err != nil || len(pubs) != 1 {
		t.Fatalf("GetPublications: %v %d", err, len(pubs))
	}
	if err := s.RemoveReference(ctx, pubID); err != nil {
		t.Fatal(err)
	}
	saved, _ = s.ListSaved(ctx)
	if len(saved) != 0 {
		t.Fatal("reference not removed")
	}
}

func TestApprovalAndDocuments(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	id, err := s.SaveSearch(ctx, Search{Topic: "essay", Type: "essay", Style: "apa"},
		[]ResultInput{{Publication: samplePub("10.3/y", "Claim paper"), ClaimIndex: 0, Claim: "A claim", Rationale: "fits"}})
	if err != nil {
		t.Fatal(err)
	}
	res, _ := s.ListResults(ctx, id)
	if err := s.SetApproval(ctx, res[0].ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.SetApproval(ctx, 9999, 1); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := s.SetApproval(ctx, res[0].ID, 5); err == nil {
		t.Fatal("expected invalid value error")
	}
	res, _ = s.ListResults(ctx, id)
	if res[0].Approved != 1 || res[0].Claim != "A claim" {
		t.Fatalf("unexpected result: %+v", res[0])
	}

	docID, err := s.CreateDocument(ctx, "essay.docx", 1234)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDocument(ctx, docID, "processed", id, ""); err != nil {
		t.Fatal(err)
	}
	doc, err := s.DocumentForSearch(ctx, id)
	if err != nil || doc.Filename != "essay.docx" || doc.Status != "processed" {
		t.Fatalf("DocumentForSearch: %v %+v", err, doc)
	}
	if err := s.UpdateDocument(ctx, docID, "bogus", 0, ""); err == nil {
		t.Fatal("expected CHECK constraint failure for invalid status")
	}
}
